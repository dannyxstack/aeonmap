package sim

// Step 推进一年。阶段顺序固定(见设计文档 5.2)，同一阶段内每个家族最多改一次上级。
func (s *State) Step() {
	s.Cnt = Counters{}
	if s.Year%10 == 0 {
		s.climate()
	}
	s.buildCellIndex()
	s.produce()
	s.demography()
	s.protectConsRoots()
	s.culture()
	s.migrate()
	s.buildCellIndex()
	s.computeRoots()
	s.buildLocalPower()
	s.rebellion()
	s.computeRoots()
	s.conquest()
	s.computeRoots()
	s.decayGrudges()
	s.split()
	s.compact()
	s.computeRoots()
	s.enforceLocks()
	if s.Year%5 == 0 {
		s.polityStep()
	}
	s.Year++
	addCounters(&s.TotalCnt, &s.Cnt)
}

func addCounters(a, b *Counters) {
	a.Births += b.Births
	a.Splits += b.Splits
	a.Deaths += b.Deaths
	a.Moves += b.Moves
	a.Conquests += b.Conquests
	a.ConquestFails += b.ConquestFails
	a.Rebellions += b.Rebellions
	a.Defects += b.Defects
	a.Wars += b.Wars
	a.Annexations += b.Annexations
	a.WarFails += b.WarFails
	a.SuccessionSplits += b.SuccessionSplits
}

// Metrics 是每年一行的指标。
type Metrics struct {
	Year        int     `json:"year"`
	Families    int     `json:"families"`
	Population  float64 `json:"population"`
	Roots       int     `json:"roots"`
	Polities10  int     `json:"polities10"` // 拥有≥10个家族的政权数
	Largest     float64 `json:"largest"`    // 最大政权人口占比
	Top5        float64 `json:"top5"`       // 前5大政权人口占比
	MeanDepth   float64 `json:"meanDepth"`
	Conquests   int     `json:"conquests"`
	Rebellions  int     `json:"rebellions"`
	Wars        int     `json:"wars"`
	Annexations int     `json:"annexations"`
	Splits      int     `json:"successionSplits"`
	Defects     int     `json:"defects"`
	Supers      int     `json:"supers"` // 正处于鼎盛期的超级帝国数
}

func (s *State) Metrics() Metrics {
	f := &s.F
	m := Metrics{Year: s.Year, Families: f.Len(), Supers: len(s.Super), Conquests: s.Cnt.Conquests, Rebellions: s.Cnt.Rebellions,
		Wars: s.Cnt.Wars, Annexations: s.Cnt.Annexations, Splits: s.Cnt.SuccessionSplits, Defects: s.Cnt.Defects}
	size := map[int32]float32{}
	nf := map[int32]int{}
	var tot float64
	var dsum float64
	for i := 0; i < f.Len(); i++ {
		size[f.Root[i]] += f.Size[i]
		nf[f.Root[i]]++
		tot += float64(f.Size[i])
		dsum += float64(f.Depth[i])
		if f.Parent[i] < 0 {
			m.Roots++
		}
	}
	m.Population = tot * float64(s.PeoplePerUnit)
	if f.Len() > 0 {
		m.MeanDepth = dsum / float64(f.Len())
	}
	var sizes []float64
	for r, v := range size {
		if nf[r] >= 10 {
			m.Polities10++
		}
		sizes = append(sizes, float64(v))
	}
	// 前5
	for k := 0; k < 5 && k < len(sizes); k++ {
		bi := k
		for j := k + 1; j < len(sizes); j++ {
			if sizes[j] > sizes[bi] {
				bi = j
			}
		}
		sizes[k], sizes[bi] = sizes[bi], sizes[k]
		if tot > 0 {
			m.Top5 += sizes[k] / tot
			if k == 0 {
				m.Largest = sizes[k] / tot
			}
		}
	}
	return m
}
