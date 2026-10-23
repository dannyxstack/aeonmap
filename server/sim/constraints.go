package sim

import "math"

// EngineVersion 写入每次演化结果；同一版本 + 同样输入 + 同样种子 = 同样的历史。
// 修改任何会影响演化结果的逻辑时必须递增。
const EngineVersion = "1.1.0"

// Constraints 是用户对演化的约束：指定政权的首都与领土。
type Constraints struct {
	Polities []ConsPolity `json:"polities"`
}

// ConsPolity：在 Capital 建立一个政权；Region(经纬度多边形，第一个环为外环)内的土地始终归它所有。
// 没有 Region 时只在首都周围 SeedRadiusKm 内聚拢初始家族，之后正常参与演化。
type ConsPolity struct {
	Name         string         `json:"name"`
	Capital      [2]float64     `json:"capital"` // [经度, 纬度]
	Region       [][][2]float64 `json:"region,omitempty"`
	SeedRadiusKm float64        `json:"seedRadiusKm,omitempty"`
	RootUID      int64          `json:"rootUid,omitempty"` // 运行时填入
}

func (c *Constraints) Empty() bool { return c == nil || len(c.Polities) == 0 }

func pointInRings(lon, lat float64, rings [][][2]float64) bool {
	in := false
	for _, ring := range rings {
		n := len(ring)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			xi, yi := ring[i][0], ring[i][1]
			xj, yj := ring[j][0], ring[j][1]
			if (yi > lat) != (yj > lat) && lon < (xj-xi)*(lat-yi)/(yj-yi+1e-12)+xi {
				in = !in
			}
		}
	}
	return in
}

// nearestLand 返回离 (lon,lat) 最近的陆地格。
func (s *State) nearestLand(lon, lat float64) int32 {
	w := s.W
	best, bd := int32(-1), math.MaxFloat64
	for _, c := range w.LandIdx {
		dx := (float64(w.Lon[c]) - lon) * math.Cos(lat*math.Pi/180)
		dy := float64(w.Lat[c]) - lat
		if d := dx*dx + dy*dy; d < bd {
			best, bd = c, d
		}
	}
	return best
}

// buildLocks 根据约束区域计算每格的锁定归属(-1 表示不锁定)。
func (s *State) buildLocks() {
	w := s.W
	s.lockOwner = make([]int16, w.N)
	for i := range s.lockOwner {
		s.lockOwner[i] = -1
	}
	for k, cp := range s.Cons {
		if len(cp.Region) == 0 {
			continue
		}
		for _, c := range w.LandIdx {
			if pointInRings(float64(w.Lon[c]), float64(w.Lat[c]), cp.Region) {
				s.lockOwner[c] = int16(k)
			}
		}
	}
}

// applyConstraints 在初始化时建立约束政权：首都放一个大家族作首领，区域内(或半径内)的初始家族归附它。
func (s *State) applyConstraints(c *Constraints) {
	if c.Empty() {
		return
	}
	f := &s.F
	w := s.W
	r := s.rng(phaseInit, 1)
	var mean float32
	for i := range f.Size {
		mean += f.Size[i]
	}
	mean /= float32(max(f.Len(), 1))
	s.Cons = append([]ConsPolity(nil), c.Polities...)
	s.buildLocks()
	for k := range s.Cons {
		cp := &s.Cons[k]
		cell := s.nearestLand(cp.Capital[0], cp.Capital[1])
		if cell < 0 {
			continue
		}
		s.NextUID++
		root := int32(f.Len())
		f.UID = append(f.UID, s.NextUID)
		f.Cell = append(f.Cell, cell)
		f.Parent = append(f.Parent, -1)
		f.Size = append(f.Size, mean*4)
		f.Store = append(f.Store, mean*2)
		f.Food = append(f.Food, 0)
		f.Born = append(f.Born, 0)
		sk := modeSkills(w.Mode[cell])
		for t := 0; t < 4; t++ {
			f.Tr[t] = append(f.Tr[t], sk[t])
		}
		f.Tr[TAggr] = append(f.Tr[TAggr], 0.55)
		f.Tr[TMob] = append(f.Tr[TMob], 0.4)
		f.Tr[TLoyal] = append(f.Tr[TLoyal], 0.85)
		f.Tr[TFert] = append(f.Tr[TFert], clamp(1+0.05*float32(r.NormFloat64()), 0.5, 1.5))
		for g := 0; g < NGrudge; g++ {
			f.GrT[g] = append(f.GrT[g], 0)
			f.GrV[g] = append(f.GrV[g], 0)
		}
		f.Root = append(f.Root, root)
		f.Depth = append(f.Depth, 0)
		f.Dead = append(f.Dead, false)
		cp.RootUID = s.NextUID
		radius := cp.SeedRadiusKm
		if radius <= 0 {
			radius = 300
		}
		for i := 0; i < int(root); i++ {
			if f.Parent[i] >= 0 {
				continue
			}
			inRegion := s.lockOwner[f.Cell[i]] == int16(k)
			if inRegion || (len(cp.Region) == 0 && float64(s.distKm(f.Cell[i], cell)) <= radius) {
				f.Parent[i] = root
				f.Tr[TLoyal][i] = clamp(f.Tr[TLoyal][i]+0.15, 0, 1)
			}
		}
		s.Polities[cp.RootUID] = &PolityInfo{UID: cp.RootUID, Name: cp.Name, Custom: true, Capital: cell}
	}
	s.computeRoots()
}

// consRoots 返回约束政权首领的当前下标(按约束顺序，-1 表示已不存在)。
func (s *State) consRoots() []int32 {
	if len(s.Cons) == 0 {
		return nil
	}
	out := make([]int32, len(s.Cons))
	for k := range out {
		out[k] = -1
	}
	want := map[int64]int{}
	for k, cp := range s.Cons {
		want[cp.RootUID] = k
	}
	for i := 0; i < s.F.Len(); i++ {
		if k, ok := want[s.F.UID[i]]; ok {
			out[k] = int32(i)
		}
	}
	return out
}

// isConsRoot：约束政权的首领不可被征服、吞并或倒戈。
func (s *State) isConsRoot(i int32) bool {
	if len(s.Cons) == 0 {
		return false
	}
	u := s.F.UID[i]
	for _, cp := range s.Cons {
		if cp.RootUID == u {
			return true
		}
	}
	return false
}

// lockedAgainst：家族 j 位于锁定区域且归属不是 attackerRoot 的约束政权时，不可被夺走。
func (s *State) lockedAgainst(j int32, attackerRoot int32) bool {
	if s.lockOwner == nil {
		return false
	}
	k := s.lockOwner[s.F.Cell[j]]
	if k < 0 {
		return s.isConsRoot(j)
	}
	return s.F.UID[attackerRoot] != s.Cons[k].RootUID
}

// protectConsRoots：约束政权的首领家族不会消亡(人口降到 1 以下时保底)。
func (s *State) protectConsRoots() {
	for _, i := range s.consRoots() {
		if i >= 0 {
			s.F.Dead[i] = false
			if s.F.Size[i] < 2 {
				s.F.Size[i] = 2
			}
			s.F.Parent[i] = -1
		}
	}
}

// enforceLocks：锁定区域内的家族始终归该约束政权(直接隶属其首领)。
func (s *State) enforceLocks() {
	if s.lockOwner == nil || len(s.Cons) == 0 {
		return
	}
	roots := s.consRoots()
	f := &s.F
	changed := false
	for i := 0; i < f.Len(); i++ {
		k := s.lockOwner[f.Cell[i]]
		if k < 0 || roots[k] < 0 || int32(i) == roots[k] || s.isConsRoot(int32(i)) {
			continue
		}
		if f.Root[i] != roots[k] {
			f.Parent[i] = roots[k]
			changed = true
		}
	}
	if changed {
		s.computeRoots()
	}
}
