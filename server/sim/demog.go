package sim

import "math"

// demography：饥荒、出生、死亡。规模<1 的家族消亡。
func (s *State) demography() {
	f := &s.F
	n := f.Len()
	P := s.P
	births := make([]float32, (n+chunkSize-1)/chunkSize)
	parallelFor(n, func(st, en, ch int) {
		var nb float32
		for i := st; i < en; i++ {
			if f.Store[i] < 0 {
				loss := -f.Store[i] * 0.7
				if loss > f.Size[i]*0.5 {
					loss = f.Size[i] * 0.5
				}
				f.Size[i] -= loss
				f.Store[i] = 0
			}
			ratio := f.Store[i] / (f.Size[i] + 1e-6)
			b := P.BirthRate * f.Tr[TFert][i] * clamp(ratio/0.3, 0, 1) * f.Size[i]
			f.Size[i] += b
			f.Store[i] -= 0.5 * b
			nb += b
			f.Size[i] *= 1 - P.DeathRate
			f.Store[i] *= 0.9 // 存粮损耗
			if f.Size[i] < 1 {
				f.Dead[i] = true
			}
		}
		births[ch] = nb
	})
	for i := 0; i < n; i++ {
		if f.Dead[i] {
			s.Cnt.Deaths++
		}
	}
}

// split：规模达到阈值的家族分家，新家族继承属性并带微小变异，初始隶属于原家族。
func (s *State) split() {
	f := &s.F
	n := f.Len()
	P := s.P
	r := s.rng(phaseSplit, 0)
	var nbuf []int32
	for i := 0; i < n; i++ {
		if f.Dead[i] || f.Size[i] < P.SplitSize || f.Len() >= P.MaxFamilies {
			continue
		}
		half := f.Size[i] / 2
		f.Size[i] = half
		st := f.Store[i] / 2
		f.Store[i] = st
		cell := f.Cell[i]
		if r.Float32() < 0.2+0.6*f.Tr[TMob][i] {
			nbuf = s.W.Neighbors8(int(cell), nbuf)
			var land []int32
			for _, c := range nbuf {
				if s.W.Land[c] {
					land = append(land, c)
				}
			}
			if len(land) > 0 {
				cell = land[r.IntN(len(land))]
			}
		}
		parent := int32(i)
		if int(f.Depth[i]) >= P.MaxDepth && f.Parent[i] >= 0 {
			parent = f.Parent[i]
		}
		s.NextUID++
		f.UID = append(f.UID, s.NextUID)
		f.Cell = append(f.Cell, cell)
		f.Parent = append(f.Parent, parent)
		f.Size = append(f.Size, half)
		f.Store = append(f.Store, st)
		f.Food = append(f.Food, f.Food[i]/2)
		f.Born = append(f.Born, int32(s.Year))
		for t := 0; t < NTraits; t++ {
			v := f.Tr[t][i] + P.Mutation*float32(r.NormFloat64())
			if t == TFert {
				v = clamp(v, 0.5, 1.5)
			} else if t < 4 {
				v = clamp(v, 0.02, 1)
			} else {
				v = clamp(v, 0, 1)
			}
			f.Tr[t] = append(f.Tr[t], v)
		}
		for g := 0; g < NGrudge; g++ {
			f.GrT[g] = append(f.GrT[g], f.GrT[g][i])
			f.GrV[g] = append(f.GrV[g], f.GrV[g][i]*0.5)
		}
		f.Root = append(f.Root, f.Root[i])
		f.Depth = append(f.Depth, f.Depth[i]+1)
		f.Dead = append(f.Dead, false)
		s.Cnt.Splits++
	}
}

// migrate：家族比较本格与 8 邻格的预期收成(扣除迁移阻力)，择优迁移。
func (s *State) migrate() {
	f := &s.F
	w := s.W
	n := f.Len()
	dest := make([]int32, n)
	parallelFor(n, func(st, en, ch int) {
		r := s.rng(phaseMigrate, ch)
		var nb []int32
		for i := st; i < en; i++ {
			dest[i] = -1
			if f.Dead[i] {
				continue
			}
			mob := f.Tr[TMob][i]
			if r.Float32() > s.P.MoveRate*(0.3+1.4*mob) {
				continue
			}
			a, eff := s.alloc(i)
			c0 := f.Cell[i]
			best := s.valueAt(i, c0, &a, &eff) * 1.05
			bestC := int32(-1)
			nb = w.Neighbors8(int(c0), nb)
			for _, c := range nb {
				if !w.Land[c] {
					continue
				}
				v := s.valueAt(i, c, &a, &eff) - 0.04*w.Friction[c]*(1.2-mob) + 0.01*float32(r.NormFloat64())
				if v > best {
					best, bestC = v, c
				}
			}
			dest[i] = bestC
		}
	})
	for i := 0; i < n; i++ {
		if dest[i] >= 0 {
			f.Cell[i] = dest[i]
			s.Cnt.Moves++
		}
	}
}

// culture：向本格收成最好的家族学习生计技能(横向文化传播)。
func (s *State) culture() {
	f := &s.F
	b := s.buf
	rate := s.P.Culture
	if rate <= 0 {
		return
	}
	for c := 0; c < s.W.N; c++ {
		st, en := b.cellStart[c], b.cellStart[c+1]
		if en-st < 2 {
			continue
		}
		var wsum float32
		var m [4]float32
		for k := st; k < en; k++ {
			i := b.order[k]
			pc := f.Food[i] / (f.Size[i] + 1e-6)
			wt := f.Size[i] * pc * pc
			wsum += wt
			for t := 0; t < 4; t++ {
				m[t] += wt * f.Tr[t][i]
			}
		}
		if wsum <= 0 {
			continue
		}
		for t := 0; t < 4; t++ {
			m[t] /= wsum
		}
		for k := st; k < en; k++ {
			i := b.order[k]
			for t := 0; t < 4; t++ {
				f.Tr[t][i] += rate * (m[t] - f.Tr[t][i])
			}
		}
	}
}

// 气候波动：粗网格 AR(1) 噪声双线性放大，每 10 年更新一次
const climNX, climNY = 16, 6

func (s *State) climate() {
	if s.P.ClimateNoise <= 0 {
		return
	}
	r := s.rng(phaseClimate, 0)
	if len(s.ClimC) != climNX*climNY {
		s.ClimC = make([]float32, climNX*climNY)
	}
	for k := range s.ClimC {
		s.ClimC[k] = 0.6*s.ClimC[k] + 0.8*float32(r.NormFloat64())
	}
	w := s.W
	for i := 0; i < w.N; i++ {
		y := (float64(i/w.NX) + 0.5) / float64(w.NY) * float64(climNY-1)
		x := (float64(i%w.NX) + 0.5) / float64(w.NX) * float64(climNX-1)
		x0, y0 := int(x), int(y)
		x1, y1 := min(x0+1, climNX-1), min(y0+1, climNY-1)
		fx, fy := float32(x-float64(x0)), float32(y-float64(y0))
		v := (1-fx)*(1-fy)*s.ClimC[y0*climNX+x0] + fx*(1-fy)*s.ClimC[y0*climNX+x1] +
			(1-fx)*fy*s.ClimC[y1*climNX+x0] + fx*fy*s.ClimC[y1*climNX+x1]
		s.ClimA[i] = float32(math.Max(0.4, float64(1+s.P.ClimateNoise*v)))
	}
}
