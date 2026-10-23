package sim

import "math"

// 资源类型：与技能 TFarm..TFish 一一对应
const NRes = 4

// 资源池化半径(格)：游牧、狩猎在更大范围内取食，与邻近同业者共享资源
var poolRadius = [NRes]int{0, 2, 2, 1}

func (s *State) ensureBuffers() {
	if s.buf != nil && len(s.buf.cellSize) == s.W.N {
		return
	}
	b := &buffers{}
	n := s.W.N
	b.cellStart = make([]int32, n+1)
	for r := 0; r < NRes; r++ {
		b.effort[r] = make([]float32, n)
		b.ret[r] = make([]float32, n)
	}
	b.cellSize = make([]float32, n)
	s.buf = b
}

// buildCellIndex 按格对家族做计数排序：order[cellStart[c]:cellStart[c+1]] 是格 c 中的家族。
func (s *State) buildCellIndex() {
	s.ensureBuffers()
	b := s.buf
	f := &s.F
	n := f.Len()
	for i := range b.cellStart {
		b.cellStart[i] = 0
	}
	for i := 0; i < n; i++ {
		b.cellStart[f.Cell[i]+1]++
	}
	for c := 1; c <= s.W.N; c++ {
		b.cellStart[c] += b.cellStart[c-1]
	}
	if cap(b.order) < n {
		b.order = make([]int32, n)
	}
	b.order = b.order[:n]
	pos := make([]int32, s.W.N)
	copy(pos, b.cellStart[:s.W.N])
	for i := 0; i < n; i++ {
		c := f.Cell[i]
		b.order[pos[c]] = int32(i)
		pos[c]++
	}
	for c := range b.cellSize {
		b.cellSize[c] = 0
	}
	for i := 0; i < n; i++ {
		b.cellSize[f.Cell[i]] += f.Size[i]
	}
}

// alloc 返回家族 i 在四类资源上的劳力分配比例与效率。
func (s *State) alloc(i int) (a [NRes]float32, eff [NRes]float32) {
	f := &s.F
	var sum float32
	for r := 0; r < NRes; r++ {
		k := f.Tr[r][i]
		a[r] = k * k
		sum += a[r]
		eff[r] = 0.3 + 0.7*k
	}
	if sum <= 0 {
		sum = 1
	}
	for r := 0; r < NRes; r++ {
		a[r] /= sum
	}
	return
}

// boxPool 对网格字段做 (2R+1)² 的盒式求和(可分离)。
func (s *State) boxPool(src []float32, R int, dst []float32) {
	w := s.W
	if R == 0 {
		copy(dst, src)
		return
	}
	tmp := make([]float32, w.N)
	for r := 0; r < w.NY; r++ {
		row := src[r*w.NX : (r+1)*w.NX]
		var acc float32
		for c := 0; c < w.NX+R; c++ {
			if c < w.NX {
				acc += row[c]
			}
			if c-2*R-1 >= 0 {
				acc -= row[c-2*R-1]
			}
			if c-R >= 0 && c-R < w.NX {
				tmp[r*w.NX+c-R] = acc
			}
		}
	}
	for c := 0; c < w.NX; c++ {
		var acc float32
		for r := 0; r < w.NY+R; r++ {
			if r < w.NY {
				acc += tmp[r*w.NX+c]
			}
			if r-2*R-1 >= 0 {
				acc -= tmp[(r-2*R-1)*w.NX+c]
			}
			if r-R >= 0 && r-R < w.NY {
				dst[(r-R)*w.NX+c] = acc
			}
		}
	}
}

// produce：生产、竞争、贡赋、消耗。
func (s *State) produce() {
	w := s.W
	b := s.buf
	f := &s.F
	n := f.Len()
	S := s.PeoplePerUnit
	p := s.P.Productivity

	for r := 0; r < NRes; r++ {
		e := b.effort[r]
		for c := range e {
			e[c] = 0
		}
	}
	allocs := make([][NRes]float32, n)
	effs := make([][NRes]float32, n)
	parallelFor(n, func(st, en, _ int) {
		for i := st; i < en; i++ {
			allocs[i], effs[i] = s.alloc(i)
		}
	})
	for i := 0; i < n; i++ {
		c := f.Cell[i]
		for r := 0; r < NRes; r++ {
			b.effort[r][c] += f.Size[i] * allocs[i][r] * effs[i][r]
		}
	}
	// 各资源的承载(家族单位)，含气候波动；耕地占用会挤压牧场
	caps := [NRes][]float32{w.CapFarm, w.CapHerd, w.CapHunt, w.CapFish}
	K := make([]float32, w.N)
	Kp := make([]float32, w.N)
	Ep := make([]float32, w.N)
	for r := 0; r < NRes; r++ {
		for c := 0; c < w.N; c++ {
			k := caps[r][c] / S * s.ClimA[c]
			if r == THerd && caps[TFarm][c] > 0 {
				use := b.effort[TFarm][c] / (caps[TFarm][c]/S + 1e-3)
				if use > 1 {
					use = 1
				}
				k *= 1 - 0.7*use
			}
			if r == TFish { // 渔业受气候影响小
				k = caps[r][c] / S
			}
			K[c] = k
		}
		s.boxPool(K, poolRadius[r], Kp)
		s.boxPool(b.effort[r], poolRadius[r], Ep)
		ret := b.ret[r]
		for c := 0; c < w.N; c++ {
			kp, ep := Kp[c], Ep[c]
			if kp <= 1e-6 {
				ret[c] = 0
				continue
			}
			x := p * ep / kp
			if x < 1e-4 {
				ret[c] = p
			} else {
				ret[c] = kp * float32(1-math.Exp(-float64(x))) / ep
			}
		}
	}
	// 家族收成
	parallelFor(n, func(st, en, _ int) {
		for i := st; i < en; i++ {
			c := f.Cell[i]
			var food float32
			for r := 0; r < NRes; r++ {
				food += f.Size[i] * allocs[i][r] * effs[i][r] * b.ret[r][c]
			}
			f.Food[i] = food
		}
	})
	// 贡赋：下级把剩余的一部分交给直接上级(逐年层层上流)
	tr := s.P.TributeRate
	for i := 0; i < n; i++ {
		if pa := f.Parent[i]; pa >= 0 {
			sur := f.Food[i] - f.Size[i]
			if sur > 0 {
				pay := tr * sur
				f.Food[i] -= pay
				f.Store[pa] += pay
			}
		}
	}
	for i := 0; i < n; i++ {
		f.Store[i] += f.Food[i] - f.Size[i]
		if m := 2 * f.Size[i]; f.Store[i] > m {
			f.Store[i] = m
		}
	}
}

// valueAt：家族 i 在格 c 的人均预期收成(用上一轮各格回报率估计)。
func (s *State) valueAt(i int, c int32, a, eff *[NRes]float32) float32 {
	var v float32
	for r := 0; r < NRes; r++ {
		v += a[r] * eff[r] * s.buf.ret[r][c]
	}
	return v
}
