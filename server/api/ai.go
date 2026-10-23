package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"

	"mapsim/ai"
	"mapsim/platform"
)

// AI 识图：管理后台配置服务商参数(/admin/v1/ai)，用户调用 POST /v1/analyze 得到建议参数。

type aiProviderView struct {
	BaseURL   string `json:"baseURL"`
	Model     string `json:"model"`
	HasKey    bool   `json:"hasKey"`
	KeyMasked string `json:"keyMasked"`
}

// aiView 是返回给管理后台的配置，密钥只显示首尾几位。
func aiView(c platform.AIConfig) map[string]any {
	pv := map[string]aiProviderView{}
	for k, p := range c.Providers {
		pv[k] = aiProviderView{BaseURL: p.BaseURL, Model: p.Model, HasKey: p.APIKey != "", KeyMasked: platform.MaskKey(p.APIKey)}
	}
	return map[string]any{"enabled": c.Enabled, "active": c.Active, "providers": pv, "timeoutSec": c.TimeoutSec,
		"maxImagePx": c.MaxImagePx, "unitsPerCall": c.UnitsPerCall}
}

func (s *Server) aiRoutes(mux *http.ServeMux) {
	a := s.adminAuth
	mux.HandleFunc("GET /admin/v1/ai", a(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"config": aiView(s.P.AIConfig()), "catalog": ai.Providers})
	}))
	mux.HandleFunc("PUT /admin/v1/ai", a(s.adminPutAI))
	mux.HandleFunc("POST /admin/v1/ai/test", a(s.adminTestAI))
	mux.HandleFunc("POST /v1/analyze", s.auth(s.handleAnalyze))
}

func (s *Server) adminPutAI(w http.ResponseWriter, r *http.Request) {
	var req struct {
		platform.AIConfig
		ClearKeys []string `json:"clearKeys"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		fail(w, 400, "bad_request", "配置格式错误: "+err.Error())
		return
	}
	for k := range req.Providers {
		if _, ok := ai.ProviderByID(k); !ok {
			fail(w, 400, "bad_request", "未知的服务商 "+k)
			return
		}
	}
	if req.Active != "" {
		if _, ok := ai.ProviderByID(req.Active); !ok {
			fail(w, 400, "bad_request", "未知的服务商 "+req.Active)
			return
		}
	}
	before := aiView(s.P.AIConfig())
	c, err := s.P.SetAIConfig(req.AIConfig, req.ClearKeys)
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	if c.Enabled {
		if _, err := ai.Resolve(c, ""); err != nil {
			c.Enabled = false
			s.P.SetAIConfig(c, nil)
			after := aiView(c)
			s.P.Audit("ai.update", "", clientIP(r), map[string]any{"before": before, "after": after})
			writeJSON(w, 200, map[string]any{"config": after, "warning": "已保存，但未启用：" + err.Error()})
			return
		}
	}
	after := aiView(c)
	s.P.Audit("ai.update", "", clientIP(r), map[string]any{"before": before, "after": after})
	writeJSON(w, 200, map[string]any{"config": after})
}

// adminTestAI：用表单里尚未保存的参数(密钥留空则用已保存的)识别一张图；不传图片时用内置的示例草图。
func (s *Server) adminTestAI(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, 400, "bad_request", "需要 multipart/form-data: "+err.Error())
		return
	}
	c := s.P.AIConfig()
	id := r.FormValue("provider")
	if id == "" {
		id = c.Active
	}
	pc := c.Providers[id]
	if v, ok := r.MultipartForm.Value["baseURL"]; ok {
		pc.BaseURL = strings.TrimRight(strings.TrimSpace(v[0]), "/")
	}
	if v, ok := r.MultipartForm.Value["model"]; ok {
		pc.Model = strings.TrimSpace(v[0])
	}
	if k := strings.TrimSpace(r.FormValue("apiKey")); k != "" {
		pc.APIKey = k
	}
	c.Providers[id] = pc
	o, err := ai.Resolve(c, id)
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	var data []byte
	if f, _, err := r.FormFile("file"); err == nil {
		data, _ = io.ReadAll(f)
		f.Close()
	} else {
		data = sampleSketch()
	}
	res, err := ai.Analyze(r.Context(), o, data)
	if err != nil {
		fail(w, 502, "ai_error", err.Error())
		return
	}
	s.P.Audit("ai.test", id, clientIP(r), map[string]any{"model": o.Model, "input": res.Input, "usage": res.Usage})
	writeJSON(w, 200, res)
}

// handleAnalyze：用户上传地图，AI 给出 input / elevation / bounds 建议。结果需用户确认后再用 POST /v1/worlds 提交。
func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	c := s.P.AIConfig()
	if !c.Enabled {
		fail(w, 503, "ai_disabled", ai.ErrDisabled.Error())
		return
	}
	o, err := ai.Resolve(c, "")
	if err != nil {
		fail(w, 503, "ai_disabled", "AI 识图配置不完整："+err.Error())
		return
	}
	plan := s.P.PlanOf(t)
	r.Body = http.MaxBytesReader(w, r.Body, int64(plan.MaxUploadMB)<<20+1<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, 400, "bad_request", fmt.Sprintf("需要 multipart/form-data(文件不超过 %d MB): %v", plan.MaxUploadMB, err))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "bad_request", "缺少 file")
		return
	}
	defer file.Close()
	if ext := strings.ToLower(filepath.Ext(hdr.Filename)); ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
		fail(w, 400, "bad_request", "AI 识图只支持 PNG、JPG")
		return
	}
	data, err := io.ReadAll(file)
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width*cfg.Height > 64_000_000 {
		fail(w, 400, "bad_request", "图片无法解析或超过 6400 万像素")
		return
	}
	if err := s.P.CheckQuota(t.ID, c.UnitsPerCall); err != nil {
		fail(w, 402, "quota_exceeded", err.Error())
		return
	}
	res, err := ai.Analyze(r.Context(), o, data)
	if err != nil {
		fail(w, 502, "ai_error", "识图失败："+err.Error())
		return
	}
	s.P.Charge(t.ID, "", "ai", c.UnitsPerCall)
	res.Raw = nil
	if res.Input == "hypsometric" {
		s.fuseBands(r.Context(), data, filepath.Ext(hdr.Filename), res)
	}
	writeJSON(w, 200, res)
}

// sampleSketch 生成一张示例手绘草图(左侧海、中间山脉、河流、高地)，用于测试连接。
func sampleSketch() []byte {
	const W, H = 640, 400
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	paper := color.RGBA{246, 241, 228, 255}
	sea := color.RGBA{70, 120, 210, 255}
	mtn := color.RGBA{130, 80, 40, 255}
	river := color.RGBA{60, 200, 220, 255}
	high := color.RGBA{235, 190, 60, 255}
	for y := 0; y < H; y++ {
		coast := 170 + 40*math.Sin(float64(y)/37)
		for x := 0; x < W; x++ {
			c := paper
			switch {
			case float64(x) < coast:
				c = sea
			case math.Hypot(float64(x-470), float64(y-290)*1.4) < 60:
				c = high
			case math.Abs(float64(x)-(380+30*math.Sin(float64(y)/50))) < 7 && y > 40 && y < 260:
				c = mtn
			case math.Abs(float64(y)-(330-0.35*float64(x-int(coast)))) < 3 && x < 560:
				c = river
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// aiStatus 告诉控制台是否可用 AI 识图及每次扣点(不含服务商细节)。
func (s *Server) aiStatus() map[string]any {
	c := s.P.AIConfig()
	_, err := ai.Resolve(c, "")
	return map[string]any{"enabled": c.Enabled && err == nil, "unitsPerCall": c.UnitsPerCall}
}

// fuseBands：分层设色图在 AI 读图之后再跑一遍色带识别(用 AI 给出的图例刻度、地图主体框与经纬度范围)。
// 识别结果来自图例读数或参考 DEM 时，用它替换 AI 直接取样得到的色带；只有拓扑模板时，AI 色带缺失才用它。
func (s *Server) fuseBands(ctx context.Context, data []byte, ext string, res *ai.Result) {
	path, cleanup, err := saveTemp(bytes.NewReader(data), strings.ToLower(ext))
	if err != nil {
		return
	}
	defer cleanup()
	p := ColorBandParams{MapBox: res.MapBox, LegendBox: res.LegendBox}
	if len(res.LegendStops) > 0 {
		p.Stops, _ = json.Marshal(res.LegendStops)
	}
	if b := res.Bounds; b != nil {
		p.Bounds = map[string]float64{"west": b.West, "east": b.East, "south": b.South, "north": b.North}
	}
	out, err := s.ColorBands(ctx, path, p)
	if err != nil {
		res.Warnings = append(res.Warnings, "色带自动识别失败："+err.Error())
		return
	}
	res.Bands = out
	var b struct {
		Legend [][]any  `json:"legend"`
		Water  []string `json:"water"`
		Mode   string   `json:"mode"`
	}
	if json.Unmarshal(out, &b) != nil || len(b.Legend) < 2 {
		return
	}
	_, hasAI := res.Elevation["legend"]
	if b.Mode == "legend" || b.Mode == "dem" || !hasAI {
		if res.Elevation == nil {
			res.Elevation = map[string]any{"type": "hypsometric"}
		}
		res.Elevation["legend"] = b.Legend
		if len(b.Water) > 0 {
			res.Elevation["water"] = b.Water
		}
		res.Warnings = append(res.Warnings, map[string]string{
			"legend":   "色带已按图例读数与地图颜色逐层配对",
			"dem":      "色带数值已用参考高程数据校准",
			"topology": "没有读出图例，色带按颜色邻接关系排序、数值按常见分级估计",
		}[b.Mode])
	}
}
