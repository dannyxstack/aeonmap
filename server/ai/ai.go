package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"math"
	"sort"
	"strings"
	"time"

	"mapsim/platform"
)

// Options 是一次调用使用的服务商参数(已合并默认值)。
type Options struct {
	Provider   Provider
	BaseURL    string
	Model      string
	APIKey     string
	Timeout    time.Duration
	MaxImagePx int
}

var ErrDisabled = errors.New("管理员未启用 AI 识图")

// Resolve 由管理后台配置得到调用参数；id 为空时用当前启用的服务商。
func Resolve(c platform.AIConfig, id string) (Options, error) {
	if id == "" {
		id = c.Active
	}
	p, ok := ProviderByID(id)
	if !ok {
		return Options{}, fmt.Errorf("未知的服务商 %q", id)
	}
	pc := c.Providers[id]
	o := Options{Provider: p, BaseURL: pc.BaseURL, Model: pc.Model, APIKey: pc.APIKey,
		Timeout: time.Duration(c.TimeoutSec) * time.Second, MaxImagePx: c.MaxImagePx}
	if o.BaseURL == "" {
		o.BaseURL = p.BaseURL
	}
	if o.Model == "" && len(p.Models) > 0 {
		o.Model = p.Models[0]
	}
	switch {
	case o.BaseURL == "":
		return o, fmt.Errorf("%s 需要填写接口地址", p.Label)
	case o.Model == "":
		return o, fmt.Errorf("%s 需要填写模型名", p.Label)
	case o.APIKey == "" && p.ID != "custom":
		return o, fmt.Errorf("%s 尚未填写 API Key", p.Label)
	}
	return o, nil
}

// Usage 统计模型调用的 token。
type Usage struct {
	Calls        int   `json:"calls"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

func (u *Usage) add(v Usage) {
	u.Calls += v.Calls
	u.InputTokens += v.InputTokens
	u.OutputTokens += v.OutputTokens
}

// Result 是识图结论。Elevation 与 Bounds 可直接填入 POST /v1/worlds 的同名字段(请用户确认后再提交)。
type Result struct {
	Provider     string           `json:"provider"`
	Model        string           `json:"model"`
	Input        string           `json:"input"` // sketch | hypsometric | grayscale | unknown
	Confidence   float64          `json:"confidence"`
	Elevation    map[string]any   `json:"elevation,omitempty"`
	Bounds       *platform.Bounds `json:"bounds,omitempty"`
	BoundsSource string           `json:"boundsSource,omitempty"`
	MapBox       []int            `json:"mapBox,omitempty"`      // 地图主体在原图中的像素范围 [x0,y0,x1,y1]
	LegendBox    []int            `json:"legendBox,omitempty"`   // 图例在原图中的像素范围
	LegendStops  []stop           `json:"legendStops,omitempty"` // 图例刻度：位置(0-1000 归一化坐标)与数值
	Bands        json.RawMessage  `json:"bands,omitempty"`       // 色带自动识别结果(POST /v1/colorbands 的输出)
	Notes        string           `json:"notes,omitempty"`
	Warnings     []string         `json:"warnings"`
	Usage        Usage            `json:"usage"`
	Elapsed      float64          `json:"elapsedSec"`
	Raw          json.RawMessage  `json:"raw,omitempty"` // 模型原始输出，便于排查
}

// ---------------- 模型输出格式 ----------------

type box struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

type stop struct {
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	ElevationM float64 `json:"elevation_m"`
}

type colorSample struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Class string  `json:"class"`
	Desc  string  `json:"desc"`
}

type rawAnalysis struct {
	Input        string        `json:"input"`
	Confidence   float64       `json:"confidence"`
	MapBox       box           `json:"map_box"`
	HasLegend    bool          `json:"has_legend"`
	LegendBox    box           `json:"legend_box"`
	LegendStops  []stop        `json:"legend_stops"`
	SketchColors []colorSample `json:"sketch_colors"`
	HasGraticule bool          `json:"has_graticule"`
	West         float64       `json:"west"`
	East         float64       `json:"east"`
	South        float64       `json:"south"`
	North        float64       `json:"north"`
	Notes        string        `json:"notes"`
}

func obj(props map[string]any) map[string]any {
	req := make([]string, 0, len(props))
	for k := range props {
		req = append(req, k)
	}
	sort.Strings(req)
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}

var (
	num      = map[string]any{"type": "number"}
	str      = map[string]any{"type": "string"}
	boolean  = map[string]any{"type": "boolean"}
	boxSch   = obj(map[string]any{"x0": num, "y0": num, "x1": num, "y1": num})
	stopsSch = map[string]any{"type": "array", "items": obj(map[string]any{"x": num, "y": num, "elevation_m": num})}

	analysisSchema = obj(map[string]any{
		"input":        map[string]any{"type": "string", "enum": []string{"sketch", "hypsometric", "grayscale", "unknown"}},
		"confidence":   num,
		"map_box":      boxSch,
		"has_legend":   boolean,
		"legend_box":   boxSch,
		"legend_stops": stopsSch,
		"sketch_colors": map[string]any{"type": "array", "items": obj(map[string]any{"x": num, "y": num, "desc": str,
			"class": map[string]any{"type": "string", "enum": []string{"sea", "lake", "river", "mountain", "highland", "land"}}})},
		"has_graticule": boolean,
		"west":          num, "east": num, "south": num, "north": num,
		"notes": str,
	})
	legendSchema = obj(map[string]any{"legend_stops": stopsSch, "notes": str})
)

const systemPrompt = `你是地图判读助手，为一个"地形→气候→文明演化"模拟系统解析用户上传的地图图片。
你只负责读图并给出参数；像素级处理由程序完成。只输出一个 JSON 对象，不要输出其他文字。
坐标约定：所有 x、y 都是 0–1000 的归一化坐标，(0,0) 为图片左上角，(1000,1000) 为右下角。`

const analysisPrompt = `判读这张地图，按下列字段输出 JSON：
- input：图片类型。sketch=手绘/简笔草图(用几种颜色涂出海、河、山)；hypsometric=分层设色地形图(按海拔分色，通常带色带图例)；grayscale=灰度高程图(越亮越高)；unknown=都不是。
- confidence：0–1，对类型判断的把握。
- map_box：地图主体的范围，不含标题、图例、边框外的留白。
- has_legend / legend_box：是否有海拔色带图例及其范围；没有则 has_legend=false、legend_box 全填 0。
- legend_stops：分层设色图的图例中每一档色块的中心点坐标(x,y，落在色块内部，避开文字和边线)和该档代表的海拔(米，取该档区间的中间值；海洋深度用负数)。按海拔从低到高列出，尽量覆盖全部色块。没有图例时给空数组。
- sketch_colors：仅手绘草图需要。图中每种不同用途的颜色各取一个样点(x,y，落在色块中央、避开边线)，class 为 sea(海)、lake(湖)、river(河流)、mountain(山脉)、highland(高地丘陵)、land(平原/未涂色的纸面)。纸面或背景色必须作为 land 给出一个样点。desc 用几个字描述颜色。非草图给空数组。
- has_graticule：图上是否有可读的经纬度标注。有则 west/east/south/north 填 map_box 四条边对应的经纬度(西经、南纬为负数，按标注的经纬线间距外推到边缘)；没有则 has_graticule=false、四个值填 0。
- notes：一两句话，说明看不清或不确定的地方(中文)。`

const legendPrompt = `这是同一张地图中图例部分的放大裁剪图。请重新读取色带：列出每一档色块的中心点坐标(x,y，按这张裁剪图的 0–1000 坐标，落在色块内部、避开文字和边线)和该档代表的海拔(米，取区间中间值；海洋深度用负数)。按海拔从低到高列出，覆盖全部色块。只输出 JSON：{"legend_stops":[{"x":..,"y":..,"elevation_m":..}],"notes":"..."}`

// call 发送一张图片与提示词，返回模型输出的 JSON 文本。
func call(ctx context.Context, o Options, prompt string, img []byte, schema map[string]any) (string, Usage, error) {
	if o.Provider.Kind == "anthropic" {
		return callAnthropic(ctx, o, prompt, img, schema)
	}
	return callOpenAI(ctx, o, prompt, img)
}

// Analyze 解析一张 PNG/JPG 地图。
func Analyze(ctx context.Context, o Options, data []byte) (*Result, error) {
	start := time.Now()
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("图片无法解析(AI 识图支持 PNG/JPG): %w", err)
	}
	rgba := toRGBA(src)
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	res := &Result{Provider: o.Provider.ID, Model: o.Model, Warnings: []string{}}
	text, u, err := call(ctx, o, analysisPrompt, encodeJPEG(fit(rgba, o.MaxImagePx)), analysisSchema)
	res.Usage.add(u)
	if err != nil {
		return nil, err
	}
	var ra rawAnalysis
	js, err := extractJSON(text)
	if err == nil {
		err = json.Unmarshal(js, &ra)
	}
	if err != nil {
		return nil, fmt.Errorf("模型输出不是预期的 JSON(%v)：%s", err, truncate(text, 300))
	}
	res.Raw = js
	res.Input, res.Confidence, res.Notes = ra.Input, ra.Confidence, ra.Notes
	W, H := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	full := image.Rect(0, 0, W, H)
	if r, ok := toRect(ra.MapBox, full); ok {
		res.MapBox = []int{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y}
	}
	if ra.HasGraticule {
		b := platform.Bounds{West: ra.West, East: ra.East, South: ra.South, North: ra.North}
		if b.West >= -180 && b.West < b.East && b.East <= 360 && b.South >= -90 && b.South < b.North && b.North <= 90 {
			res.Bounds, res.BoundsSource = &b, "graticule"
			if res.MapBox != nil && (res.MapBox[2]-res.MapBox[0] < W*9/10 || res.MapBox[3]-res.MapBox[1] < H*9/10) {
				res.Warnings = append(res.Warnings, "经纬度范围对应的是地图主体(mapBox)，而整张图还包含边框或图例；请先裁剪到 mapBox 再上传，或手动调整范围")
			}
		} else {
			res.Warnings = append(res.Warnings, "模型读出的经纬度范围无效，已忽略")
		}
	} else {
		res.Warnings = append(res.Warnings, "图上没有可读的经纬度标注，需要手动填写 bounds")
	}

	switch res.Input {
	case "hypsometric":
		stops := ra.LegendStops
		lr, hasLR := toRect(ra.LegendBox, full)
		if ra.HasLegend && hasLR {
			res.LegendBox = []int{lr.Min.X, lr.Min.Y, lr.Max.X, lr.Max.Y}
			// 第二遍：按原分辨率裁出图例单独再读，小字和窄色块更准确
			crop := pad(lr, full, 0.08)
			if crop.Dx()*crop.Dy() < W*H/3 {
				t2, u2, err := call(ctx, o, legendPrompt, encodeJPEG(fit(rgba.SubImage(crop).(*image.RGBA), o.MaxImagePx)), legendSchema)
				res.Usage.add(u2)
				var lg struct {
					LegendStops []stop `json:"legend_stops"`
				}
				if js2, e := extractJSON(t2); err == nil && e == nil && json.Unmarshal(js2, &lg) == nil && len(lg.LegendStops) >= 2 {
					stops = lg.LegendStops
					for i := range stops { // 裁剪图坐标 → 整图坐标
						stops[i].X = (float64(crop.Min.X) + stops[i].X/1000*float64(crop.Dx())) / float64(W) * 1000
						stops[i].Y = (float64(crop.Min.Y) + stops[i].Y/1000*float64(crop.Dy())) / float64(H) * 1000
					}
				} else if err != nil {
					res.Warnings = append(res.Warnings, "图例放大复核失败，使用整图读数："+err.Error())
				}
			}
		}
		res.LegendStops = append([]stop(nil), stops...)
		legend := buildLegend(rgba, stops)
		if len(legend) >= 2 {
			res.Elevation = map[string]any{"type": "hypsometric", "legend": legend}
			if len(legend) < 4 {
				res.Warnings = append(res.Warnings, "图例只读出少量色档，建议人工核对")
			}
		} else {
			res.Warnings = append(res.Warnings, "没能读出图例色带，需要手动填写 elevation.legend")
		}
	case "sketch":
		pal, warn := buildPalette(rgba, ra.SketchColors)
		res.Warnings = append(res.Warnings, warn...)
		res.Elevation = map[string]any{"type": "sketch"}
		if pal != nil {
			res.Elevation["palette"] = pal
		}
	case "grayscale":
		res.Elevation = map[string]any{"type": "grayscale"}
		res.Warnings = append(res.Warnings, "灰度高程图需要手动填写海拔范围 elevation.min / elevation.max")
	default:
		res.Warnings = append(res.Warnings, "无法判断地图类型")
	}
	res.Elapsed = math.Round(time.Since(start).Seconds()*10) / 10
	return res, nil
}

// buildLegend：在模型给出的位置上取原图颜色，得到 [[颜色, 海拔], ...]。
func buildLegend(img *image.RGBA, stops []stop) [][]any {
	sort.SliceStable(stops, func(a, b int) bool { return stops[a].ElevationM < stops[b].ElevationM })
	var out [][]any
	last := math.Inf(-1)
	for _, s := range stops {
		if s.ElevationM == last || s.ElevationM < -12000 || s.ElevationM > 9000 {
			continue
		}
		c, ok := sampleColor(img, s.X, s.Y)
		if !ok {
			continue
		}
		out = append(out, []any{c, s.ElevationM})
		last = s.ElevationM
	}
	return out
}

var sketchClass = map[string]string{"sea": "sea", "lake": "sea", "river": "river", "mountain": "mountain", "highland": "highland", "land": "land"}

// buildPalette：草图各类颜色取样，得到 [[颜色, 类别], ...]；缺少海或陆地时退回内置的颜色规则。
func buildPalette(img *image.RGBA, samples []colorSample) ([][]any, []string) {
	var out [][]any
	var cols [][3]int
	has := map[string]bool{}
	for _, s := range samples {
		cls, ok := sketchClass[s.Class]
		if !ok {
			continue
		}
		c, ok := sampleColor(img, s.X, s.Y)
		if !ok {
			continue
		}
		rgb := hexRGB(c)
		dup := false
		for _, o := range cols {
			if colorDist(o, rgb) < 24 {
				dup = true
			}
		}
		if dup {
			continue
		}
		cols = append(cols, rgb)
		out = append(out, []any{c, cls})
		has[cls] = true
	}
	if !has["land"] || !has["sea"] {
		return nil, []string{"模型没有同时给出海和陆地的颜色样点，将使用内置颜色规则(蓝=海，青=河，棕/红/黑=山，黄/橙=高地)"}
	}
	return out, nil
}

// ---------------- 图像工具 ----------------

func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// fit 用区域平均把图片长边缩到不超过 maxPx。
func fit(src *image.RGBA, maxPx int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := float64(maxPx) / float64(max(w, h))
	if scale >= 1 {
		return src
	}
	nw, nh := max(int(float64(w)*scale), 1), max(int(float64(h)*scale), 1)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := y*h/nh, max((y+1)*h/nh, y*h/nh+1)
		for x := 0; x < nw; x++ {
			x0, x1 := x*w/nw, max((x+1)*w/nw, x*w/nw+1)
			var r, g, bl, n int
			for yy := y0; yy < y1; yy++ {
				i := src.PixOffset(b.Min.X+x0, b.Min.Y+yy)
				for xx := x0; xx < x1; xx++ {
					r += int(src.Pix[i])
					g += int(src.Pix[i+1])
					bl += int(src.Pix[i+2])
					i += 4
					n++
				}
			}
			j := dst.PixOffset(x, y)
			dst.Pix[j], dst.Pix[j+1], dst.Pix[j+2], dst.Pix[j+3] = uint8(r/n), uint8(g/n), uint8(bl/n), 255
		}
	}
	return dst
}

func encodeJPEG(img image.Image) []byte {
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

// toRect 把 0–1000 归一化框换算到像素；框无效时返回 false。
func toRect(b box, full image.Rectangle) (image.Rectangle, bool) {
	if b.X1-b.X0 < 5 || b.Y1-b.Y0 < 5 {
		return image.Rectangle{}, false
	}
	W, H := float64(full.Dx()), float64(full.Dy())
	r := image.Rect(int(math.Round(b.X0/1000*W)), int(math.Round(b.Y0/1000*H)), int(math.Round(b.X1/1000*W)), int(math.Round(b.Y1/1000*H))).Intersect(full)
	return r, !r.Empty()
}

func pad(r, full image.Rectangle, frac float64) image.Rectangle {
	d := int(frac * float64(max(r.Dx(), r.Dy())))
	return image.Rect(r.Min.X-d, r.Min.Y-d, r.Max.X+d, r.Max.Y+d).Intersect(full)
}

// sampleColor 在 (x,y)(0–1000 坐标)附近取众数颜色，避开文字与边线，返回 "#rrggbb"。
func sampleColor(img *image.RGBA, x, y float64) (string, bool) {
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	if x < 0 || x > 1000 || y < 0 || y > 1000 {
		return "", false
	}
	cx, cy := int(x/1000*float64(W)), int(y/1000*float64(H))
	rad := max(2, min(W, H)/200)
	type acc struct{ r, g, b, n int }
	buckets := map[int]*acc{}
	for yy := max(cy-rad, 0); yy <= min(cy+rad, H-1); yy++ {
		for xx := max(cx-rad, 0); xx <= min(cx+rad, W-1); xx++ {
			i := img.PixOffset(b.Min.X+xx, b.Min.Y+yy)
			r, g, bl := int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
			k := r>>4<<8 | g>>4<<4 | bl>>4
			a := buckets[k]
			if a == nil {
				a = &acc{}
				buckets[k] = a
			}
			a.r, a.g, a.b, a.n = a.r+r, a.g+g, a.b+bl, a.n+1
		}
	}
	var best *acc
	for _, a := range buckets {
		if best == nil || a.n > best.n {
			best = a
		}
	}
	if best == nil {
		return "", false
	}
	return fmt.Sprintf("#%02x%02x%02x", best.r/best.n, best.g/best.n, best.b/best.n), true
}

func hexRGB(s string) [3]int {
	var r, g, b int
	fmt.Sscanf(strings.TrimPrefix(s, "#"), "%02x%02x%02x", &r, &g, &b)
	return [3]int{r, g, b}
}

func colorDist(a, b [3]int) float64 {
	dr, dg, db := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// extractJSON 从模型输出中取出 JSON 对象(兼容 ```json 代码块与 <think> 段落)。
func extractJSON(s string) (json.RawMessage, error) {
	if i := strings.LastIndex(s, "</think>"); i >= 0 {
		s = s[i+len("</think>"):]
	}
	start := strings.Index(s, "{")
	if start < 0 {
		return nil, errors.New("输出中没有 JSON 对象")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				js := json.RawMessage(s[start : i+1])
				if !json.Valid(js) {
					return nil, errors.New("JSON 格式不完整")
				}
				return js, nil
			}
		}
	}
	return nil, errors.New("JSON 未闭合(输出可能被截断)")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
