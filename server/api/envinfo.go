package api

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"mapsim/platform"
	"mapsim/world"
)

// 环境分析(第 1、2 层)的只读接口：不需要演化。
//   GET /v1/worlds/{id}/stats             全图概况：气候带、地表覆盖、海拔/气温/降水分布、各生计潜在人口、极值点
//   GET /v1/worlds/{id}/point?lon=&lat=   单点环境档案(也可用 cell=格子下标)，含 12 个月气温与降水
//   GET /v1/worlds/{id}/monthly.bin       逐月气候原始数据(格式见 manifest.monthly)

var statsCache sync.Map // 预处理目录 → 概况(结果只取决于预处理输出)

type shareItem struct {
	Key     string  `json:"key"`
	Label   string  `json:"label"`
	Color   string  `json:"color,omitempty"`
	Cells   int     `json:"cells"`
	AreaKm2 float64 `json:"areaKm2"`
	Share   float64 `json:"share"`
}

type binItem struct {
	Lo    *float64 `json:"lo"` // 第一档下限、最后一档上限为 null(开区间)
	Hi    *float64 `json:"hi"`
	Share float64  `json:"share"`
}

type extreme struct {
	Lon   float64 `json:"lon"`
	Lat   float64 `json:"lat"`
	Cell  int     `json:"cell"`
	Value float64 `json:"value"`
}

func (s *Server) envRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/worlds/{id}/stats", s.optAuth(s.withReadyWorld(s.handleStats)))
	mux.HandleFunc("GET /v1/worlds/{id}/point", s.optAuth(s.withReadyWorld(s.handlePoint)))
	mux.HandleFunc("GET /v1/worlds/{id}/monthly.bin", s.optAuth(s.worldFile("monthly.bin")))
}

func (s *Server) withReadyWorld(h func(http.ResponseWriter, *http.Request, *platform.World, *world.World)) http.HandlerFunc {
	return s.withWorld(func(w http.ResponseWriter, r *http.Request, pw *platform.World) {
		if pw.Status != "ready" {
			fail(w, 409, "not_ready", "世界尚未完成预处理(状态 "+pw.Status+")")
			return
		}
		wd, err := s.E.LoadWorld(pw.CacheDir)
		if err != nil {
			fail(w, 500, "internal", err.Error())
			return
		}
		h(w, r, pw, wd)
	})
}

func manifestField(wd *world.World, id string) *world.FieldDesc {
	for i := range wd.Manifest.Fields {
		if wd.Manifest.Fields[i].ID == id {
			return &wd.Manifest.Fields[i]
		}
	}
	return nil
}

func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request, pw *platform.World, wd *world.World) {
	if v, ok := statsCache.Load(wd.Dir); ok {
		writeJSON(w, 200, v)
		return
	}
	v := computeStats(wd)
	statsCache.Store(wd.Dir, v)
	writeJSON(w, 200, v)
}

func computeStats(wd *world.World) map[string]any {
	area := wd.Area
	var total float64
	for _, i := range wd.LandIdx {
		total += float64(area[i])
	}
	if total == 0 {
		total = 1
	}
	// 分类字段的面积占比
	cat := func(id string) []shareItem {
		fd := manifestField(wd, id)
		arr, ok := wd.F[id]
		if fd == nil || !ok {
			return nil
		}
		m := map[int]*shareItem{}
		for _, i := range wd.LandIdx {
			k := int(arr[i])
			it := m[k]
			if it == nil {
				it = &shareItem{Key: strconv.Itoa(k)}
				if k < len(fd.Labels) {
					it.Key, it.Label = fd.Labels[k], fd.Labels[k]
				}
				if k < len(fd.Colors) {
					it.Color = fd.Colors[k]
				}
				m[k] = it
			}
			it.Cells++
			it.AreaKm2 += float64(area[i])
		}
		out := make([]shareItem, 0, len(m))
		for _, it := range m {
			it.Share = round(it.AreaKm2/total, 4)
			it.AreaKm2 = math.Round(it.AreaKm2)
			out = append(out, *it)
		}
		sort.Slice(out, func(a, b int) bool { return out[a].AreaKm2 > out[b].AreaKm2 })
		return out
	}
	// 连续字段按面积加权分档；edges[0] 为 -Inf 表示第一档没有下限
	bins := func(id string, edges []float64) []binItem {
		arr := wd.F[id]
		out := make([]binItem, len(edges))
		for k := range edges {
			if !math.IsInf(edges[k], -1) {
				lo := edges[k]
				out[k].Lo = &lo
			}
			if k+1 < len(edges) {
				hi := edges[k+1]
				out[k].Hi = &hi
			}
		}
		for _, i := range wd.LandIdx {
			v := float64(arr[i])
			k := sort.SearchFloat64s(edges, v)
			if k == len(edges) || edges[k] > v {
				k--
			}
			if k < 0 {
				k = 0
			}
			out[k].Share += float64(area[i])
		}
		for k := range out {
			out[k].Share = round(out[k].Share/total, 4)
		}
		return out
	}
	ext := func(id string, max bool) extreme {
		arr := wd.F[id]
		best := -1
		for _, i := range wd.LandIdx {
			if best < 0 || (max && arr[i] > arr[best]) || (!max && arr[i] < arr[best]) {
				best = int(i)
			}
		}
		if best < 0 {
			return extreme{}
		}
		return extreme{Lon: round(float64(wd.Lon[best]), 3), Lat: round(float64(wd.Lat[best]), 3), Cell: best, Value: round(float64(arr[best]), 1)}
	}
	mean := func(id string) float64 {
		var s float64
		for _, i := range wd.LandIdx {
			s += float64(wd.F[id][i]) * float64(area[i])
		}
		return round(s/total, 2)
	}
	// 各类资源若全部用于该生计方式可养活的人口；以及最优生计下的潜在人口(按主导生计分)
	caps := map[string]float64{}
	for _, id := range []string{"cap_dry", "cap_rice", "cap_irr", "cap_past", "cap_rein", "cap_hg", "cap_fish"} {
		var s float64
		for _, i := range wd.LandIdx {
			s += float64(wd.F[id][i]) * float64(area[i])
		}
		caps[id] = math.Round(s)
	}
	modes := cat("mode")
	modeIdx := map[string]int{}
	for k, n := range wd.Manifest.Modes {
		modeIdx[n] = k
	}
	popByMode := map[int]float64{}
	var popTotal float64
	for _, i := range wd.LandIdx {
		p := float64(wd.F["density"][i]) * float64(area[i])
		popByMode[int(wd.F["mode"][i])] += p
		popTotal += p
	}
	type modeItem struct {
		shareItem
		PotentialPop float64 `json:"potentialPop"`
	}
	mi := make([]modeItem, 0, len(modes))
	for _, m := range modes {
		mi = append(mi, modeItem{m, math.Round(popByMode[modeIdx[m.Key]])})
	}
	g := wd.Manifest.Grid
	return map[string]any{
		"grid":         map[string]any{"nx": g.NX, "ny": g.NY, "res": g.Res, "west": g.West, "north": g.North},
		"landCells":    len(wd.LandIdx),
		"landAreaKm2":  math.Round(total),
		"potentialPop": math.Round(popTotal),
		"means": map[string]float64{"elev": mean("elev"), "t_ann": mean("t_ann"), "p_ann": mean("p_ann"),
			"t_cold": mean("t_cold"), "t_warm": mean("t_warm")},
		"koppen":     cat("koppen"),
		"landcover":  cat("landcover"),
		"modes":      mi,
		"capacity":   caps,
		"elevBins":   bins("elev", []float64{math.Inf(-1), 0, 200, 500, 1000, 2000, 3000, 4000, 5000}),
		"tempBins":   bins("t_ann", []float64{math.Inf(-1), -10, -5, 0, 5, 10, 15, 20, 25}),
		"precipBins": bins("p_ann", []float64{0, 100, 250, 500, 750, 1000, 1500, 2000, 3000}),
		"extremes": map[string]extreme{"highest": ext("elev", true), "warmest": ext("t_ann", true), "coldest": ext("t_ann", false),
			"wettest": ext("p_ann", true), "driest": ext("p_ann", false)},
		"monthly": wd.Manifest.Monthly != nil,
	}
}

func (s *Server) handlePoint(w http.ResponseWriter, r *http.Request, pw *platform.World, wd *world.World) {
	g := wd.Manifest.Grid
	q := r.URL.Query()
	idx := -1
	if c := q.Get("cell"); c != "" {
		idx, _ = strconv.Atoi(c)
	} else {
		lon, e1 := strconv.ParseFloat(q.Get("lon"), 64)
		lat, e2 := strconv.ParseFloat(q.Get("lat"), 64)
		if e1 != nil || e2 != nil {
			fail(w, 400, "bad_request", "需要 lon 与 lat(或 cell)")
			return
		}
		col := int(math.Floor((lon - g.West) / g.Res))
		row := int(math.Floor((g.North - lat) / g.Res))
		if col >= 0 && col < g.NX && row >= 0 && row < g.NY {
			idx = row*g.NX + col
		}
	}
	if idx < 0 || idx >= g.NX*g.NY {
		fail(w, 400, "bad_request", "坐标不在地图范围内")
		return
	}
	fields := map[string]any{}
	for _, fd := range wd.Manifest.Fields {
		v := float64(wd.F[fd.ID][idx])
		item := map[string]any{"name": fd.Name, "unit": fd.Unit, "group": fd.Group}
		if fd.Style == "categorical" && int(v) < len(fd.Labels) {
			item["value"], item["label"] = int(v), fd.Labels[int(v)]
		} else {
			item["value"] = round(v, 3)
		}
		fields[fd.ID] = item
	}
	out := map[string]any{"cell": idx, "lon": round(float64(wd.Lon[idx]), 4), "lat": round(float64(wd.Lat[idx]), 4),
		"land": wd.Land[idx], "fields": fields}
	if t, p, ok := world.ReadMonthly(wd.Dir, wd.Manifest, idx); ok {
		for k := range t {
			t[k], p[k] = round(t[k], 1), math.Round(p[k])
		}
		out["monthly"] = map[string]any{"t": t, "p": p}
	}
	writeJSON(w, 200, out)
}
