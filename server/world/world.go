// Package world 加载 Python 预处理输出的模拟格数据(manifest.json + cells.bin)。
package world

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

type FieldDesc struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Unit     string    `json:"unit"`
	Group    string    `json:"group"`
	Style    string    `json:"style"`
	Desc     string    `json:"desc"`
	Dtype    string    `json:"dtype"`
	Offset   int64     `json:"offset"`
	Bytes    int64     `json:"bytes"`
	Colormap string    `json:"colormap,omitempty"`
	Range    []float64 `json:"range,omitempty"`
	Labels   []string  `json:"labels,omitempty"`
	Colors   []string  `json:"colors,omitempty"`
}

type GridDesc struct {
	NX    int     `json:"nx"`
	NY    int     `json:"ny"`
	West  float64 `json:"west"`
	North float64 `json:"north"`
	Res   float64 `json:"res"`
}

type Manifest struct {
	Version  int             `json:"version"`
	Grid     GridDesc        `json:"grid"`
	Fields   []FieldDesc     `json:"fields"`
	Map      string          `json:"map"`
	Modes    []string        `json:"modes"`
	Mobility []float64       `json:"mobility"`
	Summary  json.RawMessage `json:"summary"`
	Monthly  *MonthlyDesc    `json:"monthly,omitempty"`
}

// World 是模拟格的只读数据。所有数组按 row-major(北→南, 西→东)，长度 NX*NY。
type World struct {
	Dir      string
	Manifest Manifest
	NX, NY   int
	N        int
	F        map[string][]float32 // 所有字段统一转成 float32
	Land     []bool
	LandIdx  []int32 // 陆地格下标列表
	Area     []float32
	Friction []float32
	Lat, Lon []float32
	DxKm     []float32 // 每格东西向宽度
	DyKm     float32
	// 4 类资源承载(单位：人/格，未换算成家族单位)
	CapFarm, CapHerd, CapHunt, CapFish []float32
	Rugged                             []float32
	Mode                               []uint8
}

func Load(dir string) (*World, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("不支持的 manifest 版本 %d", m.Version)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cells.bin"))
	if err != nil {
		return nil, err
	}
	w := &World{Dir: dir, Manifest: m, NX: m.Grid.NX, NY: m.Grid.NY, F: map[string][]float32{}}
	w.N = w.NX * w.NY
	for _, f := range m.Fields {
		if f.Offset+f.Bytes > int64(len(data)) {
			return nil, fmt.Errorf("字段 %s 越界", f.ID)
		}
		b := data[f.Offset : f.Offset+f.Bytes]
		arr := make([]float32, w.N)
		switch f.Dtype {
		case "f32":
			if len(b) != 4*w.N {
				return nil, fmt.Errorf("字段 %s 长度不符", f.ID)
			}
			for i := range arr {
				arr[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
			}
		case "u8":
			if len(b) != w.N {
				return nil, fmt.Errorf("字段 %s 长度不符", f.ID)
			}
			for i := range arr {
				arr[i] = float32(b[i])
			}
		default:
			return nil, fmt.Errorf("字段 %s 未知类型 %s", f.ID, f.Dtype)
		}
		w.F[f.ID] = arr
	}
	for _, need := range []string{"land", "area_km2", "friction", "cap_dry", "cap_rice", "cap_irr",
		"cap_past", "cap_rein", "cap_hg", "cap_fish", "rugged", "mode"} {
		if _, ok := w.F[need]; !ok {
			return nil, fmt.Errorf("manifest 缺少字段 %s", need)
		}
	}
	w.Land = make([]bool, w.N)
	w.Mode = make([]uint8, w.N)
	w.Lat = make([]float32, w.N)
	w.Lon = make([]float32, w.N)
	w.DxKm = make([]float32, w.N)
	w.DyKm = float32(111.2 * m.Grid.Res)
	w.CapFarm = make([]float32, w.N)
	w.CapHerd = make([]float32, w.N)
	w.CapHunt = make([]float32, w.N)
	w.CapFish = make([]float32, w.N)
	w.Area = w.F["area_km2"]
	w.Friction = w.F["friction"]
	w.Rugged = w.F["rugged"]
	for i := 0; i < w.N; i++ {
		r, c := i/w.NX, i%w.NX
		lat := m.Grid.North - (float64(r)+0.5)*m.Grid.Res
		lon := m.Grid.West + (float64(c)+0.5)*m.Grid.Res
		w.Lat[i], w.Lon[i] = float32(lat), float32(lon)
		w.DxKm[i] = float32(111.2 * m.Grid.Res * math.Cos(lat*math.Pi/180))
		w.Land[i] = w.F["land"][i] > 0.5 && w.Area[i] > 0
		w.Mode[i] = uint8(w.F["mode"][i])
		if !w.Land[i] {
			continue
		}
		w.LandIdx = append(w.LandIdx, int32(i))
		a := w.Area[i]
		farm := max32(w.F["cap_dry"][i], max32(w.F["cap_rice"][i], w.F["cap_irr"][i]))
		w.CapFarm[i] = farm * a
		w.CapHerd[i] = (w.F["cap_past"][i] + w.F["cap_rein"][i]) * a
		w.CapHunt[i] = w.F["cap_hg"][i] * a
		w.CapFish[i] = w.F["cap_fish"][i] * a
	}
	return w, nil
}

func max32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

// Neighbors8 返回格 i 的 8 邻域(含水域)，越界跳过。
func (w *World) Neighbors8(i int, buf []int32) []int32 {
	buf = buf[:0]
	r, c := i/w.NX, i%w.NX
	for dr := -1; dr <= 1; dr++ {
		for dc := -1; dc <= 1; dc++ {
			if dr == 0 && dc == 0 {
				continue
			}
			rr, cc := r+dr, c+dc
			if rr < 0 || rr >= w.NY || cc < 0 || cc >= w.NX {
				continue
			}
			buf = append(buf, int32(rr*w.NX+cc))
		}
	}
	return buf
}

// StepKm 是从 i 走到相邻格 j 的距离(km)。
func (w *World) StepKm(i, j int) float32 {
	ri, ci := i/w.NX, i%w.NX
	rj, cj := j/w.NX, j%w.NX
	dx := float64(w.DxKm[i]) * float64(cj-ci)
	dy := float64(w.DyKm) * float64(rj-ri)
	return float32(math.Hypot(dx, dy))
}

// MonthlyDesc：逐月气候文件(monthly.bin，int16 小端 [24][ny][nx]：0-11 月气温×tScale，12-23 月降水×pScale)。
type MonthlyDesc struct {
	File   string  `json:"file"`
	TScale float64 `json:"tScale"`
	PScale float64 `json:"pScale"`
}

// ReadMonthly 读取一格的 12 个月气温(°C)与降水(mm)；旧版预处理没有该文件时 ok=false。
func ReadMonthly(dir string, m Manifest, idx int) (t, p []float64, ok bool) {
	if m.Monthly == nil || idx < 0 || idx >= m.Grid.NX*m.Grid.NY {
		return nil, nil, false
	}
	f, err := os.Open(filepath.Join(dir, filepath.Base(m.Monthly.File)))
	if err != nil {
		return nil, nil, false
	}
	defer f.Close()
	n := int64(m.Grid.NX * m.Grid.NY)
	t, p = make([]float64, 12), make([]float64, 12)
	var b [2]byte
	for k := 0; k < 24; k++ {
		if _, err := f.ReadAt(b[:], (int64(k)*n+int64(idx))*2); err != nil {
			return nil, nil, false
		}
		v := float64(int16(binary.LittleEndian.Uint16(b[:])))
		if k < 12 {
			t[k] = v * m.Monthly.TScale
		} else {
			p[k-12] = v * m.Monthly.PScale
		}
	}
	return t, p, true
}
