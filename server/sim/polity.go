package sim

import (
	"container/heap"
	"math"
	"sort"
)

// Influence 是第5层的政权影响力场：每格前两名政权(UID)及其对数影响力。
type Influence struct {
	Top1, Top2 []int64
	V1, V2     []float32
}

// polityAgg 是一次政权步骤中的聚合结果(按首领下标)。
type polityAgg struct {
	root     int32
	uid      int64
	size     float32
	power    float32
	nfam     int
	mob      float32
	herd     float32
	aggr     float32
	loyal    float32
	reach    float32
	maxReach float32 // 影响力最远距离(km)
	super    bool
	capital  int32
	skills   [4]float32
}

func (s *State) aggregatePolities() ([]polityAgg, map[int32]int) {
	f := &s.F
	idx := map[int32]int{}
	var ps []polityAgg
	for i := 0; i < f.Len(); i++ {
		if f.Parent[i] < 0 && !f.Dead[i] {
			idx[int32(i)] = len(ps)
			ps = append(ps, polityAgg{root: int32(i), uid: f.UID[i], capital: f.Cell[i]})
		}
	}
	for i := 0; i < f.Len(); i++ {
		if f.Dead[i] {
			continue
		}
		k, ok := idx[f.Root[i]]
		if !ok {
			continue
		}
		p := &ps[k]
		sz := f.Size[i]
		p.size += sz
		p.power += s.powerEff(i)
		p.nfam++
		p.mob += sz * f.Tr[TMob][i]
		p.herd += sz * f.Tr[THerd][i]
		p.aggr += sz * f.Tr[TAggr][i]
		p.loyal += sz * f.Tr[TLoyal][i]
		for t := 0; t < 4; t++ {
			p.skills[t] += sz * f.Tr[t][i]
		}
	}
	for k := range ps {
		p := &ps[k]
		if p.size > 0 {
			p.mob /= p.size
			p.herd /= p.size
			p.aggr /= p.size
			p.loyal /= p.size
			for t := 0; t < 4; t++ {
				p.skills[t] /= p.size
			}
		}
		// 不同民族的疆域尺度不同：流动性高、善骑的游牧政权投送距离更远
		p.reach = s.P.ReachKm * (0.5 + p.mob + 0.8*p.herd)
		w := s.W
		frac := s.P.MaxReachFrac
		if _, rm := s.superMul(p.root); rm > 1 {
			p.super = true
			p.reach *= rm
			frac = min(frac*rm, 0.4) // 超级帝国放宽最远距离上限
		}
		p.maxReach = frac * float32(w.NX) * float32(w.Manifest.Grid.Res) * 111.2 * 0.71
	}
	return ps, idx
}

type infItem struct {
	v    float32
	dist float32
	cell int32
	pol  int32
}
type infHeap []infItem

func (h infHeap) Len() int { return len(h) }
func (h infHeap) Less(a, b int) bool {
	if h[a].v != h[b].v {
		return h[a].v > h[b].v
	}
	if h[a].pol != h[b].pol {
		return h[a].pol < h[b].pol
	}
	return h[a].cell < h[b].cell
}
func (h infHeap) Swap(a, b int) { h[a], h[b] = h[b], h[a] }
func (h *infHeap) Push(x any)   { *h = append(*h, x.(infItem)) }
func (h *infHeap) Pop() any {
	old := *h
	it := old[len(old)-1]
	*h = old[:len(old)-1]
	return it
}

// influence：带阻力衰减的多源"最大值传播"(加权 Voronoi)，每格保留前两名政权。
// 影响力 v = ln(本地实力) - 通行代价/投送距离，超过最远距离即停止。
func (s *State) influence(ps []polityAgg, idx map[int32]int) {
	w := s.W
	f := &s.F
	N := w.N
	top1 := make([]int32, N)
	top2 := make([]int32, N)
	v1 := make([]float32, N)
	v2 := make([]float32, N)
	for c := 0; c < N; c++ {
		top1[c], top2[c] = -1, -1
	}
	// 源：每个(格, 政权)的本地实力
	type src struct {
		key uint64
		pw  float32
	}
	acc := map[uint64]float32{}
	for i := 0; i < f.Len(); i++ {
		if f.Dead[i] {
			continue
		}
		k, ok := idx[f.Root[i]]
		if !ok {
			continue
		}
		acc[uint64(uint32(f.Cell[i]))<<32|uint64(uint32(k))] += s.powerEff(i)
	}
	keys := make([]uint64, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })
	h := make(infHeap, 0, len(keys)*4)
	for _, k := range keys {
		c, pol := int32(k>>32), int32(uint32(k))
		p := &ps[pol]
		// 地方实力随离首府的距离衰减；超出最远投送距离的领地几乎无法维持
		d := s.distKm(c, p.capital)
		v := log32(acc[k]+1e-3) - d/(4*p.reach)
		if d > p.maxReach {
			v -= 3
		}
		h = append(h, infItem{v: v, cell: c, pol: pol})
	}
	heap.Init(&h)
	vmin := log32(0.3)
	var nb []int32
	for h.Len() > 0 {
		it := heap.Pop(&h).(infItem)
		c := it.cell
		if top1[c] == it.pol || top2[c] >= 0 {
			continue
		}
		if top1[c] < 0 {
			top1[c], v1[c] = it.pol, it.v
		} else {
			top2[c], v2[c] = it.pol, it.v
		}
		L := ps[it.pol].reach
		nb = w.Neighbors8(int(c), nb)
		for _, d := range nb {
			fr := w.Friction[d]
			if fr >= 1000 || top2[d] >= 0 || top1[d] == it.pol {
				continue
			}
			cost := w.StepKm(int(c), int(d)) * (w.Friction[c] + fr) / 2
			nd := it.dist + cost
			if nd > ps[it.pol].maxReach {
				continue
			}
			nv := it.v - cost/L
			if nv < vmin {
				continue
			}
			heap.Push(&h, infItem{v: nv, dist: nd, cell: d, pol: it.pol})
		}
	}
	s.smoothTerritory(top1, top2, v1, v2)
	inf := Influence{Top1: make([]int64, N), Top2: make([]int64, N), V1: v1, V2: v2}
	for c := 0; c < N; c++ {
		if top1[c] >= 0 {
			inf.Top1[c] = ps[top1[c]].uid
		}
		if top2[c] >= 0 {
			inf.Top2[c] = ps[top2[c]].uid
		}
	}
	s.Inf = inf
	s.infIdx = top1
	s.infIdx2 = top2
}

// smoothTerritory：被同一政权从多数方向(≥5/8 邻格)包围的飞地并入该政权。
// 对应现实中孤立飞地难以维持；之后倒戈步骤会让飞地上的家族改投周边政权。
func (s *State) smoothTerritory(top1, top2 []int32, v1, v2 []float32) {
	w := s.W
	var nb []int32
	for pass := 0; pass < 2; pass++ {
		next := append([]int32(nil), top1...)
		for _, c := range w.LandIdx {
			nb = w.Neighbors8(int(c), nb)
			var cand [8]int32
			var cnt [8]int
			k := 0
			for _, d := range nb {
				q := top1[d]
				if q < 0 || !w.Land[d] {
					continue
				}
				j := 0
				for j < k && cand[j] != q {
					j++
				}
				if j == k {
					cand[k] = q
					k++
				}
				cnt[j]++
			}
			for j := 0; j < k; j++ {
				if cnt[j] >= 5 && cand[j] != top1[c] {
					if top1[c] >= 0 && top2[c] != cand[j] {
						top2[c], v2[c] = top1[c], v1[c]
					} else if top2[c] == cand[j] {
						top2[c], v2[c] = top1[c], v1[c]
					}
					next[c] = cand[j]
					if v2[c] > v1[c] {
						v1[c] = v2[c] + 0.1
					}
					break
				}
			}
		}
		copy(top1, next)
	}
}

// polityStep：每 5 年执行一次。影响力场 → 战争 → 继承危机 → 倒戈/归附 → 政权登记。
func (s *State) polityStep() {
	ps, _ := s.aggregatePolities()
	s.superStep(ps)
	s.computeRoots()
	ps, idx := s.aggregatePolities()
	s.influence(ps, idx)
	s.wars(ps)
	s.computeRoots()
	ps, _ = s.aggregatePolities()
	s.succession(ps)
	s.computeRoots()
	ps, idx = s.aggregatePolities()
	s.influence(ps, idx) // 战争和继承改变了政权构成，倒戈前需要重算
	s.defection(ps, idx)
	s.computeRoots()
	s.enforceLocks()
	ps, idx = s.aggregatePolities()
	s.influence(ps, idx)
	s.register(ps)
	// 政权间仇恨衰减
	d := float32(math.Pow(float64(s.P.GrudgeDecay), 5))
	keys := make([][2]int64, 0, len(s.Hatred))
	for k := range s.Hatred {
		keys = append(keys, k)
	}
	for _, k := range keys {
		v := s.Hatred[k] * d
		if v < 0.05 {
			delete(s.Hatred, k)
		} else {
			s.Hatred[k] = v
		}
	}
}

func (s *State) wars(ps []polityAgg) {
	w := s.W
	f := &s.F
	contact := map[[2]int32]int{}
	add := func(a, b int32) {
		if a < 0 || b < 0 || a == b {
			return
		}
		if a > b {
			a, b = b, a
		}
		contact[[2]int32{a, b}]++
	}
	t1, t2 := s.infIdx, s.infIdx2
	for _, c := range w.LandIdx {
		a := t1[c]
		if a < 0 {
			continue
		}
		if t2[c] >= 0 && s.Inf.V2[c] > s.Inf.V1[c]-1.1 {
			add(a, t2[c]) // 重叠区：资源争夺
		}
		col := int(c) % w.NX
		if col+1 < w.NX {
			add(a, t1[c+1])
		}
		if int(c)+w.NX < w.N {
			add(a, t1[int(c)+w.NX])
		}
	}
	type pair struct {
		a, b int32
		k    int
	}
	pairs := make([]pair, 0, len(contact))
	for k, v := range contact {
		pairs = append(pairs, pair{k[0], k[1], v})
	}
	sort.Slice(pairs, func(x, y int) bool {
		if pairs[x].k != pairs[y].k {
			return pairs[x].k > pairs[y].k
		}
		if pairs[x].a != pairs[y].a {
			return pairs[x].a < pairs[y].a
		}
		return pairs[x].b < pairs[y].b
	})
	r := s.rng(phaseWar, 0)
	busy := map[int32]int{}
	limit := func(p *polityAgg) int {
		if p.super {
			return 3 // 超级帝国可多线作战
		}
		return 1
	}
	for _, pr := range pairs {
		A, B := &ps[pr.a], &ps[pr.b]
		if busy[pr.a] >= limit(A) || busy[pr.b] >= limit(B) || A.nfam < 3 || B.nfam < 3 {
			continue
		}
		hate := s.Hatred[hkey(A.uid, B.uid)]
		prob := s.P.WarRate * clamp(float32(pr.k)/12, 0, 1) * (A.aggr + B.aggr) * (1 + hate)
		if A.super || B.super {
			prob = min(prob*2, 0.9)
		}
		if r.Float32() >= prob {
			continue
		}
		busy[pr.a]++
		busy[pr.b]++
		// 进攻方：好战×实力更大者
		att, def := A, B
		if B.aggr*B.power > A.aggr*A.power {
			att, def = B, A
		}
		half := s.distKm(att.capital, def.capital) / 2 * 1.5
		pa := att.power * exp32(-half/att.reach)
		pd := def.power * exp32(-half/def.reach) * 1.2
		q := pd / (pa + 1e-6)
		pwin := 1 / (1 + q*q)
		s.Cnt.Wars++
		s.casualties(att.root, def.root, 0.04)
		if r.Float32() < pwin {
			if pa > 2*pd && !s.isConsRoot(def.root) {
				// 吞并整个部落：对方首领成为我方首领的下级
				f.Parent[def.root] = att.root
				s.addGrudge(int(def.root), att.uid, 0.8)
				s.Cnt.Annexations++
				s.event("annex", att.uid, def.uid)
				if pi := s.Polities[def.uid]; pi != nil {
					pi.AbsorbedBy = att.uid
				}
			} else {
				// 割地：对方在争夺区的家族改属我方
				moved := s.cede(def, att)
				s.event("cede", att.uid, def.uid, moved)
			}
		} else {
			f.Store[att.root] *= 0.5
			s.Hatred[hkey(att.uid, def.uid)] += 1
			s.Cnt.WarFails++
			s.event("repel", def.uid, att.uid)
		}
	}
}

// casualties：两个政权中处于对方影响范围内的家族损失人口。
func (s *State) casualties(ra, rb int32, rate float32) {
	f := &s.F
	ua, ub := f.UID[ra], f.UID[rb]
	for i := 0; i < f.Len(); i++ {
		c := f.Cell[i]
		ru := f.UID[f.Root[i]]
		if (ru == ua && (s.Inf.Top1[c] == ub || s.Inf.Top2[c] == ub)) ||
			(ru == ub && (s.Inf.Top1[c] == ua || s.Inf.Top2[c] == ua)) {
			f.Size[i] *= 1 - rate
		}
	}
}

func (s *State) cede(def, att *polityAgg) int {
	f := &s.F
	moved := 0
	for i := 0; i < f.Len(); i++ {
		if f.Root[i] != def.root || int32(i) == def.root {
			continue
		}
		if s.lockedAgainst(int32(i), att.root) {
			continue
		}
		c := f.Cell[i]
		if s.Inf.Top1[c] == att.uid || s.Inf.Top2[c] == att.uid {
			f.Parent[i] = att.root
			s.addGrudge(i, att.uid, 0.4)
			moved++
		}
	}
	return moved
}

// succession：首领约每 25 年更替一次；更替时可能爆发继承危机，直属下级分裂出去。
func (s *State) succession(ps []polityAgg) {
	f := &s.F
	r := s.rng(phaseSucc, 0)
	children := map[int32][]int32{}
	for i := 0; i < f.Len(); i++ {
		if p := f.Parent[i]; p >= 0 && f.Parent[p] < 0 {
			children[p] = append(children[p], int32(i))
		}
	}
	for k := range ps {
		p := &ps[k]
		if p.nfam < 5 || r.Float32() > 5.0/25.0 {
			continue
		}
		sizeF := clamp(log32(1+float32(p.nfam))/log32(3000), 0, 1)
		if r.Float32() > s.P.SuccCrisis*(1-p.loyal)*sizeF*2 {
			continue
		}
		split := 0
		for _, c := range children[p.root] {
			if r.Float32() < 0.5*(1-f.Tr[TLoyal][c])+0.1 {
				f.Parent[c] = -1
				s.Hatred[hkey(p.uid, f.UID[c])] += 0.5
				split++
			}
		}
		if split > 0 {
			s.Cnt.SuccessionSplits++
			s.event("succession", p.uid, 0, split)
		}
	}
}

// defection：处于他国强势影响下的家族倒戈；弱小独立家族归附强邻(消除重叠)。
func (s *State) defection(ps []polityAgg, idx map[int32]int) {
	f := &s.F
	n := f.Len()
	to := make([]int32, n)
	parallelFor(n, func(st, en, ch int) {
		r := s.rng(phaseDefect, ch)
		for i := st; i < en; i++ {
			to[i] = -1
			if f.Dead[i] {
				continue
			}
			c := f.Cell[i]
			q := s.infIdx[c]
			if q < 0 {
				continue
			}
			own, ok := idx[f.Root[i]]
			if !ok || int(q) == own || s.isConsRoot(int32(i)) || (s.lockOwner != nil && s.lockOwner[c] >= 0) {
				continue
			}
			var vOwn float32 = -10
			if int(s.infIdx2[c]) == own {
				vOwn = s.Inf.V2[c]
			}
			gap := s.Inf.V1[c] - vOwn
			loyal := f.Tr[TLoyal][i]
			if f.Parent[i] >= 0 {
				if gap > 0.7 && r.Float32() < s.P.DefectRate*(1-loyal)*clamp(gap/3, 0, 1) {
					to[i] = ps[q].root
				}
			} else if ps[own].nfam <= 3 {
				// 弱小独立家族主动归附
				if gap > 1.4 && r.Float32() < s.P.DefectRate*clamp(gap/4, 0, 1) {
					to[i] = ps[q].root
				}
			}
		}
	})
	for i := 0; i < n; i++ {
		if to[i] >= 0 && s.rootOf(to[i]) != s.rootOf(int32(i)) {
			f.Parent[i] = to[i]
			s.Cnt.Defects++
		}
	}
}

// register 更新政权历史表(只登记有一定规模的政权)。
func (s *State) register(ps []polityAgg) {
	cells := map[int64]int{}
	for _, c := range s.W.LandIdx {
		if u := s.Inf.Top1[c]; u != 0 {
			cells[u]++
		}
	}
	alive := map[int64]bool{}
	for k := range ps {
		p := &ps[k]
		nc := cells[p.uid]
		if p.nfam < 5 && nc < 4 {
			continue
		}
		alive[p.uid] = true
		pi := s.Polities[p.uid]
		if pi == nil {
			pi = &PolityInfo{UID: p.uid, Name: polityName(p.uid), Born: s.Year}
			s.Polities[p.uid] = pi
		}
		pi.Died = 0
		pop := float64(p.size * s.PeoplePerUnit)
		if pop > pi.PeakPop {
			pi.PeakPop = pop
		}
		if nc > pi.PeakCells {
			pi.PeakCells = nc
		}
		pi.Capital = p.capital
		pi.Mode = dominantSkill(p.skills)
	}
	for uid, pi := range s.Polities {
		if pi.Died == 0 && !alive[uid] {
			pi.Died = s.Year
		}
	}
}

func dominantSkill(sk [4]float32) int {
	best := 0
	for t := 1; t < 4; t++ {
		if sk[t] > sk[best] {
			best = t
		}
	}
	return best
}
