package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 色带自动识别(prep/colorband)：分层设色图 → 每层颜色与海拔。不调用外部服务、不扣点。
//   1. 聚类 + 邻接拓扑排序(兜底，任何图都能给出顺序)
//   2. 参考 DEM(data/refdem/etopo.npz)校准，并用海岸线微调经纬度范围
//   3. 图例色块定位，与 AI 读出的刻度配对

var colorbandSem = make(chan struct{}, 2) // 同时最多两个识别进程

// ColorBandParams 对应 prep/colorband/cli.py 的 --params。
type ColorBandParams struct {
	Bounds    map[string]float64 `json:"bounds,omitempty"`
	MapBox    []int              `json:"mapBox,omitempty"`
	LegendBox []int              `json:"legendBox,omitempty"`
	Stops     json.RawMessage    `json:"stops,omitempty"`
	Refine    *bool              `json:"refine,omitempty"`
	Preview   *bool              `json:"preview,omitempty"`
	RefDEM    any                `json:"refdem,omitempty"`
}

// RefDEMPath：数据目录下的参考 DEM；不存在返回空。
func (s *Server) RefDEMPath() string {
	if v := os.Getenv("MAPSIM_REFDEM"); v != "" {
		return v
	}
	p := filepath.Join(s.P.Root, "refdem", "etopo.npz")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// ColorBands 在图片文件上运行识别，返回 Python 输出的 JSON。
func (s *Server) ColorBands(ctx context.Context, imagePath string, p ColorBandParams) (json.RawMessage, error) {
	if s.R == nil || s.R.Python == "" {
		return nil, fmt.Errorf("没有可用的 Python")
	}
	if ref := s.RefDEMPath(); ref != "" {
		p.RefDEM = ref
	} else {
		p.RefDEM = false
	}
	select {
	case colorbandSem <- struct{}{}:
		defer func() { <-colorbandSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	pj, _ := json.Marshal(p)
	cmd := exec.CommandContext(ctx, s.R.Python, filepath.Join(s.R.PrepDir, "colorband", "cli.py"),
		"--image", imagePath, "--params", string(pj))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := bytes.TrimSpace(stdout.Bytes())
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &e) == nil && e.Error != "" {
		return nil, fmt.Errorf("%s", e.Error)
	}
	if err != nil {
		return nil, fmt.Errorf("识别失败: %v %s", err, tail(stderr.String(), 1500))
	}
	if !json.Valid(out) {
		return nil, fmt.Errorf("识别输出无效: %s", tail(stderr.String(), 1500))
	}
	return out, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// handleColorBands：POST /v1/colorbands(multipart)。
// 字段：file(PNG/JPG)；可选 bounds(JSON)、mapBox / legendBox(JSON 数组，原图像素)、stops(JSON，AI 读出的图例刻度)、refine(false 关闭范围微调)。
func (s *Server) handleColorBands(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
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
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
		fail(w, 400, "bad_request", "色带识别只支持 PNG、JPG")
		return
	}
	var p ColorBandParams
	for name, dst := range map[string]any{"bounds": &p.Bounds, "mapBox": &p.MapBox, "legendBox": &p.LegendBox, "stops": &p.Stops} {
		if v := r.FormValue(name); v != "" {
			if err := json.Unmarshal([]byte(v), dst); err != nil {
				fail(w, 400, "bad_request", name+" 不是有效的 JSON: "+err.Error())
				return
			}
		}
	}
	if b := p.Bounds; b != nil {
		if !(b["west"] < b["east"] && b["south"] < b["north"] && b["south"] >= -90 && b["north"] <= 90) {
			fail(w, 400, "bad_request", "bounds 无效")
			return
		}
	}
	if r.FormValue("refine") == "false" {
		f := false
		p.Refine = &f
	}
	path, cleanup, err := saveTemp(file, ext)
	if err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	defer cleanup()
	out, err := s.ColorBands(r.Context(), path, p)
	if err != nil {
		fail(w, 422, "colorband_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(out)
}

func saveTemp(src io.Reader, ext string) (string, func(), error) {
	f, err := os.CreateTemp("", "colorband-*"+ext)
	if err != nil {
		return "", nil, err
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}
