package export

import (
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"sort"
)

// Feature / FeatureCollection 是最小的 GeoJSON 结构。
type Feature struct {
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
	Geometry   Geometry       `json:"geometry"`
}

type Geometry struct {
	Type        string `json:"type"`
	Coordinates any    `json:"coordinates"`
}

type FeatureCollection struct {
	Type     string    `json:"type"`
	Features []Feature `json:"features"`
}

func NewFC() *FeatureCollection {
	return &FeatureCollection{Type: "FeatureCollection", Features: []Feature{}}
}

type pt struct{ x, y int } // 格子角点坐标

// Polygons 把分类栅格(行优先，北→南)按值矢量化为多边形：每个非零值得到一个 MultiPolygon(含内洞)。
// 坐标为格子角点的经纬度。返回 值 → 多边形列表([]环，第一个为外环)。
func Polygons(ids []int32, W, H int, west, north, res float64) map[int32][][][][2]float64 {
	// 每个值收集有向边：沿格子边界逆时针(以 x 向东、y 向北为正)，同值相邻格的公共边相互抵消
	edges := map[int32]map[pt][]pt{}
	at := func(x, y int) int32 {
		if x < 0 || y < 0 || x >= W || y >= H {
			return 0
		}
		return ids[y*W+x]
	}
	add := func(v int32, a, b pt) {
		m := edges[v]
		if m == nil {
			m = map[pt][]pt{}
			edges[v] = m
		}
		m[a] = append(m[a], b)
	}
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			v := ids[y*W+x]
			if v == 0 {
				continue
			}
			// 行 y 向南增长；把角点 (x, y) 理解为左上角。逆时针(地理上)：上边自东向西、左边自北向南、下边自西向东、右边自南向北
			if at(x, y-1) != v {
				add(v, pt{x + 1, y}, pt{x, y})
			}
			if at(x-1, y) != v {
				add(v, pt{x, y}, pt{x, y + 1})
			}
			if at(x, y+1) != v {
				add(v, pt{x, y + 1}, pt{x + 1, y + 1})
			}
			if at(x+1, y) != v {
				add(v, pt{x + 1, y + 1}, pt{x + 1, y})
			}
		}
	}
	toLL := func(p pt) [2]float64 {
		return [2]float64{round6(west + float64(p.x)*res), round6(north - float64(p.y)*res)}
	}
	out := map[int32][][][][2]float64{}
	vals := make([]int32, 0, len(edges))
	for v := range edges {
		vals = append(vals, v)
	}
	sort.Slice(vals, func(a, b int) bool { return vals[a] < vals[b] })
	for _, v := range vals {
		m := edges[v]
		starts := make([]pt, 0, len(m))
		for k := range m {
			starts = append(starts, k)
		}
		sort.Slice(starts, func(a, b int) bool {
			if starts[a].y != starts[b].y {
				return starts[a].y < starts[b].y
			}
			return starts[a].x < starts[b].x
		})
		var rings [][][2]float64
		for _, s := range starts {
			for len(m[s]) > 0 {
				ring := []pt{s}
				cur := s
				for {
					nexts := m[cur]
					if len(nexts) == 0 {
						break
					}
					nx := nexts[len(nexts)-1]
					m[cur] = nexts[:len(nexts)-1]
					cur = nx
					ring = append(ring, cur)
					if cur == s {
						break
					}
				}
				if len(ring) >= 4 {
					rings = append(rings, simplify(ring, toLL))
				}
			}
		}
		// 区分外环(逆时针，面积>0)与内洞(顺时针)，把洞分配给包含它的外环
		var polys [][][][2]float64
		var holes [][][2]float64
		for _, r := range rings {
			if ringArea(r) > 0 {
				polys = append(polys, [][][2]float64{r})
			} else {
				holes = append(holes, r)
			}
		}
		for _, h := range holes {
			for k := range polys {
				if pointInRing(h[0], polys[k][0]) {
					polys[k] = append(polys[k], h)
					break
				}
			}
		}
		out[v] = polys
	}
	return out
}

// simplify 去掉共线的中间点。
func simplify(r []pt, toLL func(pt) [2]float64) [][2]float64 {
	var o [][2]float64
	n := len(r) - 1 // 末点与首点相同
	for i := 0; i < n; i++ {
		a, b, c := r[(i+n-1)%n], r[i], r[(i+1)%n]
		if (b.x-a.x)*(c.y-b.y)-(b.y-a.y)*(c.x-b.x) == 0 {
			continue
		}
		o = append(o, toLL(b))
	}
	if len(o) > 0 {
		o = append(o, o[0])
	}
	return o
}

func ringArea(r [][2]float64) float64 {
	var a float64
	for i := 0; i+1 < len(r); i++ {
		a += r[i][0]*r[i+1][1] - r[i+1][0]*r[i][1]
	}
	return a / 2
}

func pointInRing(p [2]float64, r [][2]float64) bool {
	in := false
	for i, j := 0, len(r)-1; i < len(r); j, i = i, i+1 {
		if (r[i][1] > p[1]) != (r[j][1] > p[1]) &&
			p[0] < (r[j][0]-r[i][0])*(p[1]-r[i][1])/(r[j][1]-r[i][1])+r[i][0] {
			in = !in
		}
	}
	return in
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// ---- 游戏引擎资源 ----

// Resample 双线性重采样到 size×size(引擎地形要求正方形，如 Unity 的 2^n+1)。
func Resample(src []float32, W, H, size int) []float32 {
	out := make([]float32, size*size)
	for y := 0; y < size; y++ {
		fy := float64(y) / float64(size-1) * float64(H-1)
		y0 := int(fy)
		y1 := min(y0+1, H-1)
		ty := float32(fy - float64(y0))
		for x := 0; x < size; x++ {
			fx := float64(x) / float64(size-1) * float64(W-1)
			x0 := int(fx)
			x1 := min(x0+1, W-1)
			tx := float32(fx - float64(x0))
			a := src[y0*W+x0]*(1-tx) + src[y0*W+x1]*tx
			b := src[y1*W+x0]*(1-tx) + src[y1*W+x1]*tx
			out[y*size+x] = a*(1-ty) + b*ty
		}
	}
	return out
}

// ResampleNearest 最近邻重采样(分类数据)。
func ResampleNearest(src []uint8, W, H, size int) []uint8 {
	out := make([]uint8, size*size)
	for y := 0; y < size; y++ {
		sy := min(int(float64(y)/float64(size)*float64(H)), H-1)
		for x := 0; x < size; x++ {
			sx := min(int(float64(x)/float64(size)*float64(W)), W-1)
			out[y*size+x] = src[sy*W+sx]
		}
	}
	return out
}

// HeightPNG16 把高程写成 16 位灰度 PNG，返回映射用的最低/最高海拔：h = min + v/65535*(max-min)。
func HeightPNG16(w io.Writer, h []float32, size int) (float32, float32, error) {
	lo, hi := float32(math.MaxFloat32), float32(-math.MaxFloat32)
	for _, v := range h {
		lo = min(lo, v)
		hi = max(hi, v)
	}
	if hi <= lo {
		hi = lo + 1
	}
	img := image.NewGray16(image.Rect(0, 0, size, size))
	for i, v := range h {
		img.Pix[2*i], img.Pix[2*i+1] = 0, 0
		q := uint16((v - lo) / (hi - lo) * 65535)
		img.SetGray16(i%size, i/size, color.Gray16{Y: q})
	}
	return lo, hi, png.Encode(w, img)
}

// HeightRAW16 写出 Unity 地形可直接导入的 RAW(16 位小端)。
func HeightRAW16(w io.Writer, h []float32, lo, hi float32) error {
	b := make([]byte, 2*len(h))
	for i, v := range h {
		q := uint16((v - lo) / (hi - lo) * 65535)
		b[2*i], b[2*i+1] = byte(q), byte(q>>8)
	}
	_, err := w.Write(b)
	return err
}

// SplatPNG 由地表覆盖生成 RGBA 混合贴图：R 草原/灌丛，G 森林，B 荒漠/裸地，A 雪/岩/苔原/高山。
func SplatPNG(w io.Writer, lc []uint8, size int) error {
	// 地表覆盖编码见 prep/mapprep/resources.py 的 LANDCOVER
	ch := map[uint8][4]uint8{
		0: {0, 0, 0, 0}, 1: {0, 0, 0, 255}, 2: {60, 0, 0, 195}, 3: {0, 255, 0, 0}, 4: {0, 255, 0, 0},
		5: {0, 255, 0, 0}, 6: {255, 0, 0, 0}, 7: {0, 0, 255, 0}, 8: {170, 40, 45, 0}, 9: {80, 0, 0, 175},
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i, v := range lc {
		c := ch[v]
		img.Pix[4*i], img.Pix[4*i+1], img.Pix[4*i+2], img.Pix[4*i+3] = c[0], c[1], c[2], c[3]
	}
	return png.Encode(w, img)
}

// IndexPNG 写 8 位索引图(用于生物群系/政权编号等)。
func IndexPNG(w io.Writer, v []uint8, W, H int) error {
	img := image.NewGray(image.Rect(0, 0, W, H))
	copy(img.Pix, v)
	return png.Encode(w, img)
}
