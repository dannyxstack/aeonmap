// Package api 提供 REST 接口(/v1)、WebSocket 回放通道与本地网页。
package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"mapsim/export"
	"mapsim/platform"
	"mapsim/sim"
	"mapsim/store"
	"mapsim/transport"
)

type Server struct {
	E       *Engine
	R       *Runner
	P       *platform.Store
	Static  fs.FS
	OpenAPI []byte
	// AdminToken 管理后台令牌；为空时管理接口全部拒绝
	AdminToken string
	Auth       AuthConfig
	CFAccess   *CFAccess // 配置后，管理接口还要求 Cloudflare Access 签发的身份令牌
	tasks      *taskStore

	mu      sync.Mutex
	clients map[*client]bool
	warming sync.Map // 租户 → 是否正在预热
}

type client struct {
	conn   *websocket.Conn
	tenant string
	ip     string
	send   chan wsMsg
}

type wsMsg struct {
	bin  bool
	data []byte
}

func NewServer(e *Engine, r *Runner, p *platform.Store, static fs.FS, openapi []byte) *Server {
	s := &Server{E: e, R: r, P: p, Static: static, OpenAPI: openapi, clients: map[*client]bool{}, tasks: newTaskStore(),
		Auth: AuthConfigFromEnv(), CFAccess: CFAccessFromEnv()}
	e.SetStatusHook(func(st Status) { s.broadcast(st.Tenant, st) })
	r.OnJob = func(j platform.Job) { s.broadcast(j.Tenant, map[string]any{"type": "job", "job": j}) }
	return s
}

// ---------------- 通用 ----------------

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)
	e.Encode(v)
}

func fail(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]apiError{"error": {Code: kind, Message: msg}})
}

type ctxKey struct{}

func tenantOf(r *http.Request) *platform.Tenant {
	return r.Context().Value(ctxKey{}).(*platform.Tenant)
}

func apiKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if h := r.Header.Get("X-API-Key"); h != "" {
		return h
	}
	return r.URL.Query().Get("key") // 浏览器下载链接与 WebSocket 使用
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(s.Static)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		// 回放页原来是 /index.html(文件服务会把它规范成 /)：旧链接 /?run=… /?world=… 转到 /replay.html
		if q := r.URL.Query(); q.Get("run") != "" || q.Get("world") != "" {
			http.Redirect(w, r, "/replay.html?"+r.URL.RawQuery, http.StatusFound)
			return
		}
		http.ServeFileFS(w, r, s.Static, "home.html") // 首页
	})
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "engineVersion": sim.EngineVersion})
	})
	mux.HandleFunc("GET /v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Write(s.OpenAPI)
	})
	mux.HandleFunc("GET /v1/params", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, ParamSchema()) })
	mux.HandleFunc("GET /v1/me", s.optAuth(s.handleMe))
	mux.HandleFunc("GET /v1/usage", s.auth(s.handleUsage))

	mux.HandleFunc("GET /v1/worlds", s.optAuth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"worlds": s.P.Worlds(tenantOf(r).ID)})
	}))
	mux.HandleFunc("POST /v1/worlds", s.auth(s.handleCreateWorld))
	mux.HandleFunc("GET /v1/worlds/{id}", s.optAuth(s.withWorld(func(w http.ResponseWriter, r *http.Request, pw *platform.World) {
		writeJSON(w, 200, pw)
	})))
	mux.HandleFunc("DELETE /v1/worlds/{id}", s.auth(s.handleDeleteWorld))
	mux.HandleFunc("GET /v1/worlds/{id}/manifest", s.optAuth(s.worldFile("manifest")))
	mux.HandleFunc("GET /v1/worlds/{id}/cells.bin", s.optAuth(s.worldFile("cells.bin")))
	mux.HandleFunc("GET /v1/worlds/{id}/rivers.geojson", s.optAuth(s.worldFile("rivers.geojson")))
	mux.HandleFunc("GET /v1/worlds/{id}/preview/{name}", s.optAuth(s.worldFile("preview")))
	mux.HandleFunc("GET /v1/worlds/{id}/layers/{name}", s.optAuth(s.withWorld(s.handleLayer)))
	mux.HandleFunc("POST /v1/worlds/{id}/jobs", s.auth(s.handleSubmit))

	mux.HandleFunc("GET /v1/jobs", s.auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"jobs": s.P.Jobs(tenantOf(r).ID, 100)})
	}))
	mux.HandleFunc("GET /v1/jobs/{id}", s.auth(s.handleJob))
	mux.HandleFunc("DELETE /v1/jobs/{id}", s.auth(s.handleCancelJob))

	mux.HandleFunc("GET /v1/runs", s.optAuth(func(w http.ResponseWriter, r *http.Request) {
		t := tenantOf(r)
		out := []store.RunMeta{}
		for _, m := range s.E.ListRuns("", r.URL.Query().Get("world")) {
			if s.runVisible(t.ID, &m) {
				out = append(out, m)
			}
		}
		writeJSON(w, 200, map[string]any{"runs": out})
	}))
	mux.HandleFunc("GET /v1/runs/{id}", s.optAuth(s.withRun(func(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
		writeJSON(w, 200, m)
	})))
	mux.HandleFunc("DELETE /v1/runs/{id}", s.auth(s.withRun(func(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
		if m.Tenant != tenantOf(r).ID {
			fail(w, 403, "forbidden", "只能删除自己的演化")
			return
		}
		if err := s.E.DeleteRun(m.ID); err != nil {
			fail(w, 409, "conflict", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"deleted": m.ID})
	})))
	mux.HandleFunc("GET /v1/runs/{id}/metrics", s.optAuth(s.withRun(func(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
		ms, err := store.ReadMetrics(filepath.Join(s.E.RunsDir, m.ID))
		if err != nil {
			fail(w, 404, "not_found", err.Error())
			return
		}
		if ms == nil {
			ms = []sim.Metrics{}
		}
		writeJSON(w, 200, ms)
	})))
	mux.HandleFunc("GET /v1/runs/{id}/events", s.optAuth(s.withRun(func(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
		from, _ := strconv.Atoi(r.URL.Query().Get("from"))
		to, err := strconv.Atoi(r.URL.Query().Get("to"))
		if err != nil {
			to = 1 << 30
		}
		ev, err := store.ReadEvents(filepath.Join(s.E.RunsDir, m.ID), from, to)
		if err != nil {
			fail(w, 404, "not_found", err.Error())
			return
		}
		if ev == nil {
			ev = []sim.Event{}
		}
		writeJSON(w, 200, ev)
	})))
	mux.HandleFunc("GET /v1/runs/{id}/polities", s.optAuth(s.withRun(func(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
		http.ServeFile(w, r, filepath.Join(s.E.RunsDir, m.ID, "polities.json"))
	})))
	mux.HandleFunc("GET /v1/runs/{id}/frames/{year}", s.optAuth(s.withRun(s.handleFrame)))
	mux.HandleFunc("GET /v1/runs/{id}/roads", s.optAuth(s.withRun(s.handleRoads)))
	mux.HandleFunc("GET /v1/runs/{id}/borders", s.optAuth(s.withRun(s.handleBorders)))
	mux.HandleFunc("GET /v1/runs/{id}/inspect", s.optAuth(s.withRun(func(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
		if isGuest(tenantOf(r)) && !allow("inspect:"+clientIP(r), 30, time.Minute) { // 重算较耗 CPU，访客限流
			fail(w, 429, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}
		year, _ := strconv.Atoi(r.URL.Query().Get("year"))
		cell, _ := strconv.Atoi(r.URL.Query().Get("cell"))
		var out any
		if err := s.E.WithStateAt(m.ID, year, func(st *sim.State) { out = inspectState(st, year, cell) }); err != nil {
			fail(w, 400, "bad_request", err.Error())
			return
		}
		writeJSON(w, 200, out)
	})))
	mux.HandleFunc("GET /v1/exports/{id}", s.auth(s.handleExport))
	mux.HandleFunc("GET /ws", s.handleWS)
	s.adminRoutes(mux)
	s.aiRoutes(mux)
	s.envRoutes(mux)
	s.authRoutes(mux)
	s.taskRoutes(mux)
	mux.HandleFunc("POST /v1/colorbands", s.auth(s.handleColorBands))
	return logRequests(mux)
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		h.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/v1/") && r.Method != "GET" {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(t0).Round(time.Millisecond))
		}
	})
}

// ---------------- 账户 ----------------

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	if isGuest(t) {
		set := s.P.Settings()
		writeJSON(w, 200, map[string]any{"guest": true, "plan": set.Plans[set.SignupPlan], "engineVersion": sim.EngineVersion,
			"ai": map[string]any{"enabled": false}, "colorbands": map[string]any{"refdem": false}})
		return
	}
	keys := make([]map[string]any, 0)
	for _, k := range t.Keys {
		keys = append(keys, map[string]any{"prefix": k.Prefix, "created": k.Created, "revoked": k.Revoked})
	}
	writeJSON(w, 200, map[string]any{"id": t.ID, "name": t.Name, "email": t.Email, "anonymous": t.IsAnonymous(), "plan": s.P.PlanOf(t),
		"usedThisMonth": s.P.UsedThisMonth(t.ID), "activeJobs": s.P.ActiveJobs(t.ID), "keys": keys, "webhookSecret": t.WebhookSecret,
		"engineVersion": sim.EngineVersion, "ai": s.aiStatus(),
		"colorbands": map[string]any{"refdem": s.RefDEMPath() != ""}})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	recs := s.P.UsageRecords(t.ID)
	if recs == nil {
		recs = []platform.UsageRec{}
	}
	writeJSON(w, 200, map[string]any{"plan": s.P.PlanOf(t), "usedThisMonth": s.P.UsedThisMonth(t.ID), "records": recs})
}

// ---------------- 世界 ----------------

func (s *Server) withWorld(h func(http.ResponseWriter, *http.Request, *platform.World)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pw, ok := s.P.World(r.PathValue("id"))
		if !ok || !s.P.Visible(tenantOf(r).ID, pw) {
			fail(w, 404, "not_found", "世界不存在")
			return
		}
		h(w, r, pw)
	}
}

func (s *Server) worldFile(kind string) http.HandlerFunc {
	return s.withWorld(func(w http.ResponseWriter, r *http.Request, pw *platform.World) {
		if pw.Status != "ready" {
			fail(w, 409, "not_ready", "世界尚未完成预处理(状态 "+pw.Status+")")
			return
		}
		switch kind {
		case "manifest":
			s.serveManifest(w, pw)
		case "preview":
			http.ServeFile(w, r, filepath.Join(pw.CacheDir, "preview", filepath.Base(r.PathValue("name"))))
		default:
			http.ServeFile(w, r, filepath.Join(pw.CacheDir, kind))
		}
	})
}

// serveManifest 在预处理的 manifest 之外附上帧字段说明，前端据此解码回放帧。
func (s *Server) serveManifest(w http.ResponseWriter, pw *platform.World) {
	b, err := os.ReadFile(filepath.Join(pw.CacheDir, "manifest.json"))
	if err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	m["frameFields"] = frameFieldsJSON()
	m["traitNames"] = sim.TraitNames
	m["worldId"] = pw.ID
	writeJSON(w, 200, m)
}

func frameFieldsJSON() any {
	type ff struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Unit  string `json:"unit"`
		Group string `json:"group"`
		Style string `json:"style"`
		Desc  string `json:"desc"`
		Bytes int    `json:"bytes"`
	}
	var out []ff
	for _, f := range sim.FrameFields {
		out = append(out, ff{f.ID, f.Name, f.Unit, f.Group, f.Style, f.Desc, f.Bytes})
	}
	return out
}

// handleLayer：单个静态图层的 GeoTIFF，例如 /v1/worlds/{id}/layers/koppen.tif
func (s *Server) handleLayer(w http.ResponseWriter, r *http.Request, pw *platform.World) {
	name := strings.TrimSuffix(r.PathValue("name"), ".tif")
	if pw.Status != "ready" {
		fail(w, 409, "not_ready", "世界尚未完成预处理")
		return
	}
	wd, err := s.E.LoadWorld(pw.CacheDir)
	if err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	arr, ok := wd.F[name]
	if !ok {
		fail(w, 404, "not_found", "没有图层 "+name)
		return
	}
	g := wd.Manifest.Grid
	rs := &export.Raster{W: wd.NX, H: wd.NY, West: g.West, North: g.North, Res: g.Res, F32: arr, Description: name}
	w.Header().Set("Content-Type", "image/tiff")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.tif"`, name))
	export.WriteGeoTIFF(w, rs)
}

func (s *Server) handleDeleteWorld(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	pw, ok := s.P.World(r.PathValue("id"))
	if !ok || pw.Tenant != t.ID {
		fail(w, 404, "not_found", "世界不存在或不属于你")
		return
	}
	for _, m := range s.E.ListRuns(t.ID, pw.ID) {
		if m.Tenant == t.ID {
			s.E.DeleteRun(m.ID)
		}
	}
	if err := s.P.DeleteWorld(pw.ID); err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": pw.ID})
}

// handleCreateWorld：multipart 上传地图并自动排队预处理。
//
//	file       地图图片(PNG/JPG/TIFF/GeoTIFF)
//	name       名称
//	input      grayscale(高程图) | sketch(草图) | hypsometric(分层设色，需 elevation.legend)
//	bounds     {"west":..,"east":..,"south":..,"north":..}；GeoTIFF 可省略(自动读取)
//	elevation  可选 JSON：grayscale 的 min/max/encoding/sea_level/nodata；sketch 的 palette/seed；hypsometric 的 legend
//	hints      可选 JSON：区域气候修正(格式见 maps/eurasia/hints.json)
func (s *Server) handleCreateWorld(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	plan := s.P.PlanOf(t)
	r.Body = http.MaxBytesReader(w, r.Body, int64(plan.MaxUploadMB)<<20+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, 400, "bad_request", fmt.Sprintf("需要 multipart/form-data(文件不超过 %d MB): %v", plan.MaxUploadMB, err))
		return
	}
	input := r.FormValue("input")
	if input != "grayscale" && input != "sketch" && input != "hypsometric" {
		fail(w, 400, "bad_request", "input 必须是 grayscale、sketch 或 hypsometric")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "bad_request", "缺少 file")
		return
	}
	defer file.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	isTiff := ext == ".tif" || ext == ".tiff"
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && !isTiff {
		fail(w, 400, "bad_request", "只支持 PNG、JPG、TIFF/GeoTIFF")
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	if !isTiff {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			fail(w, 400, "bad_request", "图片无法解析: "+err.Error())
			return
		}
		if cfg.Width < 64 || cfg.Height < 64 || cfg.Width*cfg.Height > 64_000_000 {
			fail(w, 400, "bad_request", "图片尺寸需在 64×64 到 6400 万像素之间")
			return
		}
	} else if len(data) < 8 || (string(data[:2]) != "II" && string(data[:2]) != "MM") {
		fail(w, 400, "bad_request", "不是有效的 TIFF 文件")
		return
	}
	elev := map[string]any{}
	if v := r.FormValue("elevation"); v != "" {
		if err := json.Unmarshal([]byte(v), &elev); err != nil {
			fail(w, 400, "bad_request", "elevation 不是合法 JSON")
			return
		}
	}
	elev["type"] = input
	if input == "hypsometric" && elev["legend"] == nil {
		fail(w, 400, "bad_request", "分层设色图需要 elevation.legend(颜色→海拔的色带)")
		return
	}
	var bounds *platform.Bounds
	if v := r.FormValue("bounds"); v != "" && v != "auto" {
		bounds = &platform.Bounds{}
		if err := json.Unmarshal([]byte(v), bounds); err != nil {
			fail(w, 400, "bad_request", "bounds 不是合法 JSON")
			return
		}
		if !(bounds.West >= -180 && bounds.West < bounds.East && bounds.East <= 360 &&
			bounds.South >= -90 && bounds.South < bounds.North && bounds.North <= 90) {
			fail(w, 400, "bad_request", "bounds 范围无效")
			return
		}
	} else if !isTiff {
		fail(w, 400, "bad_request", "非 GeoTIFF 图片需要提供 bounds(经纬度范围)")
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(hdr.Filename, ext)
	}
	webhook := r.FormValue("webhook")
	if webhook != "" {
		if err := s.R.CheckWebhookURL(webhook); err != nil {
			fail(w, 400, "bad_request", err.Error())
			return
		}
	}
	pw := &platform.World{ID: platform.NewID("w_"), Tenant: t.ID, Name: name, Kind: "upload", Input: input,
		Bounds: bounds, Status: "uploaded", Created: time.Now().UTC().Format(time.RFC3339)}
	pw.MapDir = filepath.Join(s.P.WorldDir(pw.ID), "map")
	if err := os.MkdirAll(pw.MapDir, 0o755); err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	img := "input" + ext
	if err := os.WriteFile(filepath.Join(pw.MapDir, img), data, 0o644); err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	mapJSON := map[string]any{"name": name, "image": img, "projection": map[string]string{"type": "equirectangular"},
		"elevation": elev, "bounds": "auto"}
	if bounds != nil {
		mapJSON["bounds"] = bounds
	}
	if v := r.FormValue("hints"); v != "" {
		var h any
		if json.Unmarshal([]byte(v), &h) != nil {
			fail(w, 400, "bad_request", "hints 不是合法 JSON")
			return
		}
		hb, _ := json.MarshalIndent(h, "", " ")
		os.WriteFile(filepath.Join(pw.MapDir, "hints.json"), hb, 0o644)
		mapJSON["hints"] = "hints.json"
	}
	pw.Elevation, _ = json.Marshal(elev)
	mb, _ := json.MarshalIndent(mapJSON, "", " ")
	if err := os.WriteFile(filepath.Join(pw.MapDir, "map.json"), mb, 0o644); err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	if err := s.P.PutWorld(pw); err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	job, err := s.R.Submit(t, pw.ID, &JobRequest{Webhook: webhook})
	if err != nil {
		s.submitError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"world": pw, "job": job})
}

// ---------------- 任务 ----------------

func (s *Server) submitError(w http.ResponseWriter, err error) {
	var qe *quotaError
	if errors.As(err, &qe) {
		fail(w, 402, "quota_exceeded", err.Error())
		return
	}
	var be *busyError
	if errors.As(err, &be) {
		fail(w, 429, "too_many_jobs", err.Error())
		return
	}
	var pe *planError
	if errors.As(err, &pe) {
		fail(w, 403, "plan_restricted", err.Error())
		return
	}
	fail(w, 400, "bad_request", err.Error())
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req JobRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		fail(w, 400, "bad_request", "请求体不是合法的任务 JSON: "+err.Error())
		return
	}
	if req.Simulate != nil && len(req.Simulate.Params) > 0 {
		var p sim.Params
		d := json.NewDecoder(bytes.NewReader(req.Simulate.Params))
		d.DisallowUnknownFields()
		if err := d.Decode(&p); err != nil {
			fail(w, 400, "bad_request", "simulate.params 含未知字段或类型错误: "+err.Error())
			return
		}
	}
	if req.Export != nil {
		for _, f := range req.Export.Formats {
			ok := false
			for _, a := range allFormats {
				ok = ok || strings.EqualFold(a, f)
			}
			if !ok {
				fail(w, 400, "bad_request", "未知导出格式 "+f+"(可选 geotiff、geojson、csv、engine)")
				return
			}
		}
	}
	job, err := s.R.Submit(tenantOf(r), r.PathValue("id"), &req)
	if err != nil {
		s.submitError(w, err)
		return
	}
	writeJSON(w, 202, job)
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.P.Job(r.PathValue("id"))
	if !ok || j.Tenant != tenantOf(r).ID {
		fail(w, 404, "not_found", "任务不存在")
		return
	}
	writeJSON(w, 200, j)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.P.Job(r.PathValue("id"))
	if !ok || j.Tenant != tenantOf(r).ID {
		fail(w, 404, "not_found", "任务不存在")
		return
	}
	if (j.Status != "running" && j.Status != "queued") || !s.R.Cancel(j.ID) {
		fail(w, 409, "conflict", "只能取消排队中或正在演化的任务")
		return
	}
	writeJSON(w, 202, map[string]any{"cancelling": j.ID})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(r.PathValue("id"))
	owned := false
	for _, j := range s.P.Jobs(tenantOf(r).ID, 0) {
		if j.Result != nil && j.Result.ExportID == id {
			owned = true
			break
		}
	}
	if !owned {
		fail(w, 404, "not_found", "导出包不存在")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, id))
	http.ServeFile(w, r, filepath.Join(s.P.Root, "exports", id+".zip"))
}

// ---------------- 演化 ----------------

func (s *Server) withRun(h func(http.ResponseWriter, *http.Request, *store.RunMeta)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		m, err := readRunMeta(filepath.Join(s.E.RunsDir, filepath.Base(r.PathValue("id"))))
		if err != nil || !s.runVisible(tenantOf(r).ID, m) {
			fail(w, 404, "not_found", "演化不存在")
			return
		}
		h(w, r, m)
	}
}

func (s *Server) handleFrame(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
	y, _ := strconv.Atoi(r.PathValue("year"))
	rd, err := s.E.Reader(m.ID)
	if err != nil {
		fail(w, 404, "not_found", err.Error())
		return
	}
	b, fy, err := rd.Frame(y)
	if err != nil {
		fail(w, 404, "not_found", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Frame-Year", strconv.Itoa(fy))
	w.Write(b)
}

func (s *Server) frameAt(m *store.RunMeta, r *http.Request) (*sim.FrameData, error) {
	wd, _, err := s.E.WorldOfRun(m.ID)
	if err != nil {
		return nil, err
	}
	rd, err := s.E.Reader(m.ID)
	if err != nil {
		return nil, err
	}
	idx := rd.Index()
	if len(idx) == 0 {
		return nil, fmt.Errorf("还没有帧")
	}
	year := int(idx[len(idx)-1].Year)
	if v := r.URL.Query().Get("year"); v != "" {
		year, _ = strconv.Atoi(v)
	}
	b, _, err := rd.Frame(year)
	if err != nil {
		return nil, err
	}
	return sim.DecodeFrame(b, wd.LandIdx, wd.N)
}

// handleRoads：某年的交通网络(GeoJSON)，?year= 不给则取最后一年；?layer=cities 返回城市点。
func (s *Server) handleRoads(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
	fr, err := s.frameAt(m, r)
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	wd, _, _ := s.E.WorldOfRun(m.ID)
	polity := make([]int64, wd.N)
	capitals := map[int64]int32{}
	for c := range polity {
		polity[c] = fr.PolUID(fr.Top1[c])
	}
	for _, p := range fr.Pols {
		if p.Cells >= 6 && p.Capital >= 0 {
			capitals[p.UID] = p.Capital
		}
	}
	opt := transport.DefaultOptions()
	if v, err := strconv.Atoi(r.URL.Query().Get("cities")); err == nil && v > 0 {
		opt.MaxCities = min(v, 1000)
	}
	net := transport.Build(wd, fr.Pop, polity, capitals, opt)
	roads, cities := networkGeoJSON(wd, net, polityNames(filepath.Join(s.E.RunsDir, m.ID)))
	if r.URL.Query().Get("layer") == "cities" {
		writeJSON(w, 200, cities)
		return
	}
	writeJSON(w, 200, roads)
}

// handleBorders：某年的政权疆域多边形(GeoJSON)。
func (s *Server) handleBorders(w http.ResponseWriter, r *http.Request, m *store.RunMeta) {
	fr, err := s.frameAt(m, r)
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	wd, _, _ := s.E.WorldOfRun(m.ID)
	ids := make([]int32, wd.N)
	for c := range ids {
		if v := fr.Top1[c]; v != 0 && v != sim.PolScattered {
			ids[c] = int32(v)
		}
	}
	g := wd.Manifest.Grid
	names := polityNames(filepath.Join(s.E.RunsDir, m.ID))
	fc := export.NewFC()
	polys := export.Polygons(ids, wd.NX, wd.NY, g.West, g.North, g.Res)
	for k := 1; k <= len(fr.Pols); k++ {
		pp, ok := polys[int32(k)]
		if !ok {
			continue
		}
		p := fr.Pols[k-1]
		fc.Features = append(fc.Features, export.Feature{Type: "Feature",
			Properties: map[string]any{"uid": p.UID, "name": names.name(p.UID), "population": int64(p.Pop), "families": p.Families, "cells": p.Cells},
			Geometry:   export.Geometry{Type: "MultiPolygon", Coordinates: pp}})
	}
	writeJSON(w, 200, fc)
}

// ---------------- WebSocket(回放界面) ----------------

type inMsg struct {
	Type string `json:"type"`
	Req  uint32 `json:"req"`
	Run  string `json:"run"`
	Year int    `json:"year"`
	Cell int    `json:"cell"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	t, _ := s.identify(r)
	if t == nil {
		if apiKey(r) != "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		t = guestTenant // 访客只能读公开内容(帧、格子详情)，收不到任何任务推送
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	c := &client{conn: conn, tenant: t.ID, ip: clientIP(r), send: make(chan wsMsg, 64)}
	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		conn.CloseNow()
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-c.send:
				typ := websocket.MessageText
				if m.bin {
					typ = websocket.MessageBinary
				}
				wctx, cc := context.WithTimeout(ctx, 30*time.Second)
				err := conn.Write(wctx, typ, m.data)
				cc()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	for _, st := range s.E.RunningOf(t.ID) {
		s.sendJSON(c, st)
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var m inMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		go s.dispatch(c, m)
	}
}

func (s *Server) sendJSON(c *client, v any) {
	b, _ := json.Marshal(v)
	select {
	case c.send <- wsMsg{data: b}:
	default:
	}
}

func (s *Server) broadcast(tenant string, v any) {
	b, _ := json.Marshal(v)
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		if tenant != "" && c.tenant != tenant {
			continue
		}
		select {
		case c.send <- wsMsg{data: b}:
		default:
		}
	}
}

func (s *Server) canRead(tenant, run string) bool {
	m, err := readRunMeta(filepath.Join(s.E.RunsDir, filepath.Base(run)))
	return err == nil && s.runVisible(tenant, m)
}

// runVisible：自己的演化、公共演化，以及管理员设为公开示例的演化(访客也能看)。
func (s *Server) runVisible(tenant string, m *store.RunMeta) bool {
	if m.Tenant == platform.PublicTenant || s.P.Settings().IsFeatured(m.ID) {
		return true
	}
	return tenant != guestTenant.ID && m.Tenant == tenant
}

func (s *Server) dispatch(c *client, m inMsg) {
	errOut := func(err error) {
		s.sendJSON(c, map[string]any{"type": "error", "req": m.Req, "message": err.Error()})
	}
	if m.Run == "" || !s.canRead(c.tenant, m.Run) {
		errOut(fmt.Errorf("演化不存在"))
		return
	}
	switch m.Type {
	case "frame":
		rd, err := s.E.Reader(m.Run)
		if err != nil {
			errOut(err)
			return
		}
		b, y, err := rd.Frame(m.Year)
		if err != nil {
			errOut(err)
			return
		}
		// 二进制回复：[req u32][year u32][帧]
		out := make([]byte, 8, 8+len(b))
		binary.LittleEndian.PutUint32(out, m.Req)
		binary.LittleEndian.PutUint32(out[4:], uint32(y))
		c.send <- wsMsg{bin: true, data: append(out, b...)}
	case "inspect":
		if c.tenant == guestTenant.ID && !allow("inspect:"+c.ip, 30, time.Minute) {
			errOut(fmt.Errorf("请求过于频繁，请稍后再试"))
			return
		}
		var out any
		if err := s.E.WithStateAt(m.Run, m.Year, func(st *sim.State) { out = inspectState(st, m.Year, m.Cell) }); err != nil {
			errOut(err)
			return
		}
		s.sendJSON(c, map[string]any{"type": "inspect", "req": m.Req, "data": out})
	case "warm":
		// 预热：每个租户同时只有一个，避免堆积
		if _, busy := s.warming.LoadOrStore(c.tenant, true); !busy {
			go func() {
				defer s.warming.Delete(c.tenant)
				s.E.WithStateAt(m.Run, m.Year, nil)
			}()
		}
	}
}
