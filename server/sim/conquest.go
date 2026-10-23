package sim

import (
	"math"
	"sort"
)

func cellRootKey(c int32, root int32) uint64 { return uint64(uint32(c))<<32 | uint64(uint32(root)) }

// buildLocalPower 汇总每个(格, 最高首领)的实力与人口。
func (s *State) buildLocalPower() {
	f := &s.F
	b := s.buf
	b.rootPow = make(map[uint64]float32, f.Len())
	b.rootSize = make(map[uint64]float32, f.Len())
	for i := 0; i < f.Len(); i++ {
		if f.Dead[i] {
			continue
		}
		k := cellRootKey(f.Cell[i], f.Root[i])
		b.rootPow[k] += s.powerEff(i)
		b.rootSize[k] += f.Size[i]
	}
}

// rootOf 沿 Parent 链向上走(本轮内上级可能已变化，不能用缓存的 Root)。
func (s *State) rootOf(i int32) int32 {
	f := &s.F
	for k := 0; k < 4096; k++ {
		p := f.Parent[i]
		if p < 0 {
			return i
		}
		i = p
	}
	return -1 // 环(不应发生)
}

func (s *State) distKm(a, b int32) float32 {
	w := s.W
	lat := (w.Lat[a] + w.Lat[b]) / 2
	dx := (w.Lon[a] - w.Lon[b]) * 111.2 * float32(math.Cos(float64(lat)*math.Pi/180))
	dy := (w.Lat[a] - w.Lat[b]) * 111.2
	return float32(math.Hypot(float64(dx), float64(dy)))
}

type attack struct{ a, t int32 }

// conquest：资源充足的家族尝试征服邻近的异族家族，使其成为自己的下级。
func (s *State) conquest() {
	f := &s.F
	w := s.W
	b := s.buf
	n := f.Len()
	P := s.P
	nch := (n + chunkSize - 1) / chunkSize
	lists := make([][]attack, nch)
	parallelFor(n, func(st, en, ch int) {
		r := s.rng(phaseConquest, ch)
		var nb []int32
		var cand []int32
		var wts []float32
		for i := st; i < en; i++ {
			if f.Dead[i] {
				continue
			}
			aggr := f.Tr[TAggr][i]
			ratio := f.Store[i] / (f.Size[i] + 1e-6)
			if ratio < P.ConquestThr*(1.5-aggr) {
				continue
			}
			var hate float32
			for g := 0; g < NGrudge; g++ {
				if f.GrV[g][i] > hate {
					hate = f.GrV[g][i]
				}
			}
			if r.Float32() > P.ConquestRate*aggr*2*(1+clamp(hate, 0, 2)) {
				continue
			}
			// 目标格：本格与邻格，按"异族人口"(资源争夺强度)加权
			c0 := f.Cell[i]
			ri := f.Root[i]
			nb = w.Neighbors8(int(c0), nb)
			cand = append(cand[:0], c0)
			for _, c := range nb {
				if w.Land[c] {
					cand = append(cand, c)
				}
			}
			wts = wts[:0]
			var tot float32
			for _, c := range cand {
				other := b.cellSize[c] - b.rootSize[cellRootKey(c, ri)]
				if other < 0 {
					other = 0
				}
				wts = append(wts, other)
				tot += other
			}
			if tot <= 0 {
				continue
			}
			x := r.Float32() * tot
			tc := cand[len(cand)-1]
			for k, wv := range wts {
				if x < wv {
					tc = cand[k]
					break
				}
				x -= wv
			}
			st0, en0 := b.cellStart[tc], b.cellStart[tc+1]
			if en0 <= st0 {
				continue
			}
			for try := 0; try < 4; try++ {
				j := b.order[st0+int32(r.IntN(int(en0-st0)))]
				if f.Root[j] == ri || f.Dead[j] {
					continue
				}
				if s.grudge(i, f.UID[f.Root[j]]) > 0 || try == 3 || r.Float32() < 0.5 {
					lists[ch] = append(lists[ch], attack{int32(i), j})
					break
				}
			}
		}
	})
	var all []attack
	for _, l := range lists {
		all = append(all, l...)
	}
	sort.Slice(all, func(x, y int) bool {
		if all[x].t != all[y].t {
			return all[x].t < all[y].t
		}
		return all[x].a < all[y].a
	})
	r := s.rng(phaseConquest, 1<<20)
	acted := map[int32]bool{}
	for _, at := range all {
		i, j := at.a, at.t
		if acted[i] || acted[j] {
			continue
		}
		ri, rj := s.rootOf(i), s.rootOf(j)
		if ri < 0 || rj < 0 || ri == rj {
			continue // 同一最高首领 → 同一部落，放弃
		}
		if s.lockedAgainst(j, ri) {
			continue // 用户锁定的领土或约束政权首领不可被夺走
		}
		acted[i], acted[j] = true, true
		pa := s.powerEff(int(i)) + 0.3*b.rootPow[cellRootKey(f.Cell[i], f.Root[i])]
		pd := (s.powerEff(int(j)) + 0.3*b.rootPow[cellRootKey(f.Cell[j], f.Root[j])]) * (1 + w.Rugged[f.Cell[j]]/600)
		q := pd / (pa + 1e-6)
		pwin := 1 / (1 + q*q)
		uidRi, uidRj := f.UID[ri], f.UID[rj]
		if r.Float32() < pwin {
			old := f.Parent[j]
			f.Parent[j] = i
			f.Size[j] *= 0.95
			f.Size[i] *= 0.98
			s.addGrudge(int(j), uidRi, 0.5)
			if old >= 0 {
				s.addGrudge(int(old), uidRi, 0.3)
			}
			s.Cnt.Conquests++
		} else {
			f.Store[i] *= 0.6
			f.Size[i] *= 0.93
			f.Size[j] *= 0.97
			s.addGrudge(int(j), uidRi, 1.0)
			s.addGrudge(int(i), uidRj, 0.3)
			s.Hatred[hkey(uidRi, uidRj)] += 0.2
			s.Cnt.ConquestFails++
		}
	}
}

// rebellion：远离上级、忠诚低、心怀仇恨的下级家族尝试独立。
func (s *State) rebellion() {
	f := &s.F
	n := f.Len()
	P := s.P
	dec := make([]int8, n) // 1 独立, -1 失败
	parallelFor(n, func(st, en, ch int) {
		r := s.rng(phaseRebel, ch)
		for i := st; i < en; i++ {
			pa := f.Parent[i]
			if pa < 0 || f.Dead[i] {
				continue
			}
			root := f.Root[i]
			h := s.grudge(i, f.UID[root])
			prob := P.RebelRate * (1 - f.Tr[TLoyal][i]) * (1 + h) * (0.5 + P.TributeRate)
			if m, _ := s.superMul(root); m > 1 {
				prob *= 0.3 // 超级帝国鼎盛期强力压制叛乱
			}
			if r.Float32() >= prob {
				continue
			}
			projP := s.power(int(pa)) * exp32(-s.distKm(f.Cell[i], f.Cell[pa])/P.ReachKm)
			pm, rm := s.superMul(root)
			projR := s.power(int(root)) * pm * exp32(-s.distKm(f.Cell[i], f.Cell[root])/(P.ReachKm*rm))
			if projR > projP {
				projP = projR
			}
			if s.power(i) > projP {
				dec[i] = 1
			} else {
				dec[i] = -1
			}
		}
	})
	for i := 0; i < n; i++ {
		switch dec[i] {
		case 1:
			old := f.UID[f.Root[i]]
			f.Parent[i] = -1
			s.addGrudge(i, old, 0.5)
			s.Hatred[hkey(old, f.UID[i])] += 0.5
			s.Cnt.Rebellions++
		case -1:
			f.Size[i] *= 0.92
			s.addGrudge(i, f.UID[f.Root[i]], 0.3)
		}
	}
}

func (s *State) decayGrudges() {
	f := &s.F
	d := s.P.GrudgeDecay
	parallelFor(f.Len(), func(st, en, _ int) {
		for g := 0; g < NGrudge; g++ {
			v := f.GrV[g]
			t := f.GrT[g]
			for i := st; i < en; i++ {
				v[i] *= d
				if v[i] < 0.05 {
					v[i] = 0
					t[i] = 0
				}
			}
		}
	})
}
