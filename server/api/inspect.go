package api

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	"mapsim/sim"
)

// ---- 格子详情 ----

type famOut struct {
	UID     int64                `json:"uid"`
	Pop     float32              `json:"pop"`
	Store   float32              `json:"store"`
	Depth   int32                `json:"depth"`
	Parent  int64                `json:"parent"`
	Root    int64                `json:"root"`
	Born    int32                `json:"born"`
	Traits  [sim.NTraits]float32 `json:"traits"`
	Grudges []grOut              `json:"grudges,omitempty"`
}
type grOut struct {
	Target int64   `json:"target"`
	Name   string  `json:"name"`
	V      float32 `json:"v"`
}
type chainOut struct {
	UID  int64   `json:"uid"`
	Cell int32   `json:"cell"`
	Pop  float32 `json:"pop"`
}

func inspectState(st *sim.State, year, cell int) any {
	f := &st.F
	S := st.PeoplePerUnit
	var idx []int
	for i := 0; i < f.Len(); i++ {
		if int(f.Cell[i]) == cell {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool { return f.Size[idx[a]] > f.Size[idx[b]] })
	total := len(idx)
	var mean [sim.NTraits]float32
	var msz float32
	for _, i := range idx {
		for t := 0; t < sim.NTraits; t++ {
			mean[t] += f.Size[i] * f.Tr[t][i]
		}
		msz += f.Size[i]
	}
	if msz > 0 {
		for t := range mean {
			mean[t] /= msz
		}
	}
	if len(idx) > 40 {
		idx = idx[:40]
	}
	name := func(uid int64) string {
		if p := st.Polities[uid]; p != nil {
			return p.Name
		}
		return ""
	}
	var fams []famOut
	for _, i := range idx {
		fo := famOut{UID: f.UID[i], Pop: f.Size[i] * S, Store: f.Store[i] / (f.Size[i] + 1e-6), Depth: f.Depth[i],
			Root: f.UID[f.Root[i]], Born: f.Born[i]}
		if p := f.Parent[i]; p >= 0 {
			fo.Parent = f.UID[p]
		}
		for t := 0; t < sim.NTraits; t++ {
			fo.Traits[t] = f.Tr[t][i]
		}
		for g := 0; g < sim.NGrudge; g++ {
			if f.GrV[g][i] > 0 {
				fo.Grudges = append(fo.Grudges, grOut{f.GrT[g][i], name(f.GrT[g][i]), f.GrV[g][i]})
			}
		}
		fams = append(fams, fo)
	}
	var chain []chainOut
	if len(idx) > 0 {
		i := int32(idx[0])
		for k := 0; k < 64 && i >= 0; k++ {
			chain = append(chain, chainOut{f.UID[i], f.Cell[i], f.Size[i] * S})
			i = f.Parent[i]
		}
	}
	pol := func(uid int64) any {
		if uid == 0 {
			return nil
		}
		var fam int
		var pop float32
		var capital int32 = -1
		for i := 0; i < f.Len(); i++ {
			if f.UID[f.Root[i]] == uid {
				fam++
				pop += f.Size[i] * S
				if f.Parent[i] < 0 {
					capital = f.Cell[i]
				}
			}
		}
		out := map[string]any{"uid": uid, "name": name(uid), "families": fam, "pop": pop, "capital": capital}
		if p := st.Polities[uid]; p != nil {
			out["born"] = p.Born
			out["mode"] = p.Mode
		}
		return out
	}
	var p1, p2 int64
	if len(st.Inf.Top1) > cell {
		p1, p2 = st.Inf.Top1[cell], st.Inf.Top2[cell]
	}
	var hate []map[string]any
	if p1 != 0 {
		for k, v := range st.Hatred {
			var other int64
			if k[0] == p1 {
				other = k[1]
			} else if k[1] == p1 {
				other = k[0]
			} else {
				continue
			}
			hate = append(hate, map[string]any{"uid": other, "name": name(other), "v": v})
		}
		sort.Slice(hate, func(a, b int) bool { return hate[a]["v"].(float32) > hate[b]["v"].(float32) })
		if len(hate) > 5 {
			hate = hate[:5]
		}
	}
	return map[string]any{"cell": cell, "year": year, "families": fams, "familyCount": total,
		"meanTraits": mean, "chain": chain, "polity": pol(p1), "polity2": pol(p2), "hatred": hate}
}

// ParamSchema 由 Params 的结构体标签生成，前端据此自动生成参数表单。
func ParamSchema() any {
	d := sim.DefaultParams()
	t := reflect.TypeOf(d)
	v := reflect.ValueOf(d)
	var out []map[string]any
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		min, _ := strconv.ParseFloat(f.Tag.Get("min"), 64)
		max, _ := strconv.ParseFloat(f.Tag.Get("max"), 64)
		out = append(out, map[string]any{
			"id": strings.Split(f.Tag.Get("json"), ",")[0], "group": f.Tag.Get("group"), "desc": f.Tag.Get("desc"),
			"min": min, "max": max, "default": v.Field(i).Interface(), "int": f.Type.Kind() != reflect.Float32,
		})
	}
	return out
}
