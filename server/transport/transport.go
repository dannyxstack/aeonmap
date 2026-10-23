// Package transport 由人口分布与迁移阻力生成交通网络：城市 + 最小代价道路/近岸航线。
package transport

import (
	"container/heap"
	"math"
	"sort"

	"mapsim/world"
)

type Options struct {
	MaxCities  int     // 城市数上限
	Neighbors  int     // 每个城市连接的最近城市数
	MaxCostKm  float64 // 单条线路的最大通行代价(阻力加权 km)
	MinPop     float64 // 城市最低人口
	LocalRange int     // 局部极大值判定半径(格)
}

func DefaultOptions() Options {
	return Options{MaxCities: 200, Neighbors: 4, MaxCostKm: 2500, MinPop: 2000, LocalRange: 2}
}

type City struct {
	Cell    int32   `json:"cell"`
	Lon     float64 `json:"lon"`
	Lat     float64 `json:"lat"`
	Pop     float64 `json:"pop"`
	Tier    int     `json:"tier"` // 1 首都, 2 大城, 3 城镇
	Polity  int64   `json:"polity,omitempty"`
	Capital bool    `json:"capital,omitempty"`
}

type Route struct {
	From, To int     // 城市序号
	Cells    []int32 `json:"-"`
	CostKm   float64
	LengthKm float64
	Traffic  float64
	Tier     int  // 1 大道, 2 道路, 3 小路
	Sea      bool // 是否经过水域(近岸航线)
}

type Network struct {
	Cities []City
	Routes []Route
}

// Build：pop 为每格人口(人)，polity 为每格所属政权 UID(0 表示无)，capitals 为各政权首都所在格。
func Build(w *world.World, pop []float32, polity []int64, capitals map[int64]int32, o Options) *Network {
	// 1. 城市：人口局部极大值 + 政权首都
	var cand []int32
	for _, c := range w.LandIdx {
		p := pop[c]
		if float64(p) < o.MinPop {
			continue
		}
		r, col := int(c)/w.NX, int(c)%w.NX
		ok := true
		for dr := -o.LocalRange; dr <= o.LocalRange && ok; dr++ {
			for dc := -o.LocalRange; dc <= o.LocalRange; dc++ {
				rr, cc := r+dr, col+dc
				if (dr == 0 && dc == 0) || rr < 0 || rr >= w.NY || cc < 0 || cc >= w.NX {
					continue
				}
				q := pop[rr*w.NX+cc]
				if q > p || (q == p && rr*w.NX+cc < int(c)) {
					ok = false
					break
				}
			}
		}
		if ok {
			cand = append(cand, c)
		}
	}
	sort.Slice(cand, func(a, b int) bool {
		if pop[cand[a]] != pop[cand[b]] {
			return pop[cand[a]] > pop[cand[b]]
		}
		return cand[a] < cand[b]
	})
	isCap := map[int32]int64{}
	for uid, c := range capitals {
		if c >= 0 && int(c) < w.N && w.Land[c] {
			isCap[c] = uid
		}
	}
	chosen := map[int32]bool{}
	var cities []City
	addCity := func(c int32) {
		if chosen[c] {
			return
		}
		chosen[c] = true
		ci := City{Cell: c, Lon: float64(w.Lon[c]), Lat: float64(w.Lat[c]), Pop: float64(pop[c]), Tier: 3}
		if polity != nil {
			ci.Polity = polity[c]
		}
		if uid, ok := isCap[c]; ok {
			ci.Capital, ci.Tier, ci.Polity = true, 1, uid
		}
		cities = append(cities, ci)
	}
	capCells := make([]int32, 0, len(isCap))
	for c := range isCap {
		capCells = append(capCells, c)
	}
	sort.Slice(capCells, func(a, b int) bool { return capCells[a] < capCells[b] })
	for _, c := range capCells {
		if len(cities) < o.MaxCities {
			addCity(c)
		}
	}
	for _, c := range cand {
		if len(cities) >= o.MaxCities {
			break
		}
		addCity(c)
	}
	// 人口前 15% 的非首都城市为大城
	byPop := make([]int, len(cities))
	for i := range byPop {
		byPop[i] = i
	}
	sort.Slice(byPop, func(a, b int) bool { return cities[byPop[a]].Pop > cities[byPop[b]].Pop })
	for k, i := range byPop {
		if cities[i].Tier == 3 && k < len(cities)*15/100+1 {
			cities[i].Tier = 2
		}
	}
	cityAt := map[int32]int{}
	for i, c := range cities {
		cityAt[c.Cell] = i
	}

	// 2. 每个城市向外做受限 Dijkstra，连接最近的若干城市
	type key struct{ a, b int }
	routes := map[key]*Route{}
	dist := make([]float64, w.N)
	prev := make([]int32, w.N)
	for i := range dist {
		dist[i] = math.Inf(1)
	}
	var touched []int32
	var nb []int32
	for i, ci := range cities {
		for _, t := range touched {
			dist[t] = math.Inf(1)
		}
		touched = touched[:0]
		h := &pq{}
		dist[ci.Cell], prev[ci.Cell] = 0, -1
		touched = append(touched, ci.Cell)
		heap.Push(h, item{ci.Cell, 0})
		found := 0
		for h.Len() > 0 && found < o.Neighbors {
			it := heap.Pop(h).(item)
			if it.d > dist[it.c] {
				continue
			}
			if j, ok := cityAt[it.c]; ok && j != i {
				found++
				a, b := min(i, j), max(i, j)
				if _, dup := routes[key{a, b}]; !dup {
					var cells []int32
					for c := it.c; c >= 0; c = prev[c] {
						cells = append(cells, c)
					}
					r := &Route{From: a, To: b, Cells: cells, CostKm: it.d}
					for k := 0; k+1 < len(cells); k++ {
						r.LengthKm += float64(w.StepKm(int(cells[k]), int(cells[k+1])))
						if !w.Land[cells[k]] {
							r.Sea = true
						}
					}
					routes[key{a, b}] = r
				}
				continue // 不穿过其他城市继续扩展，避免"越城"长线
			}
			nb = w.Neighbors8(int(it.c), nb)
			for _, d := range nb {
				fr := w.Friction[d]
				if fr >= 1000 {
					continue
				}
				nd := it.d + float64(w.StepKm(int(it.c), int(d))*(w.Friction[it.c]+fr)/2)
				if nd > o.MaxCostKm || nd >= dist[d] {
					continue
				}
				if math.IsInf(dist[d], 1) {
					touched = append(touched, d)
				}
				dist[d], prev[d] = nd, it.c
				heap.Push(h, item{d, nd})
			}
		}
	}
	// 3. 引力模型流量 → 分级
	out := make([]Route, 0, len(routes))
	for _, r := range routes {
		pa, pb := cities[r.From].Pop, cities[r.To].Pop
		r.Traffic = math.Sqrt(pa*pb) / math.Pow(math.Max(r.CostKm, 10), 1.2)
		out = append(out, *r)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Traffic != out[b].Traffic {
			return out[a].Traffic > out[b].Traffic
		}
		if out[a].From != out[b].From {
			return out[a].From < out[b].From
		}
		return out[a].To < out[b].To
	})
	for k := range out {
		switch {
		case k < len(out)*15/100+1:
			out[k].Tier = 1
		case k < len(out)*50/100+1:
			out[k].Tier = 2
		default:
			out[k].Tier = 3
		}
	}
	return &Network{Cities: cities, Routes: out}
}

type item struct {
	c int32
	d float64
}
type pq []item

func (p pq) Len() int { return len(p) }
func (p pq) Less(a, b int) bool {
	if p[a].d != p[b].d {
		return p[a].d < p[b].d
	}
	return p[a].c < p[b].c
}
func (p pq) Swap(a, b int) { p[a], p[b] = p[b], p[a] }
func (p *pq) Push(x any)   { *p = append(*p, x.(item)) }
func (p *pq) Pop() any {
	o := *p
	it := o[len(o)-1]
	*p = o[:len(o)-1]
	return it
}
