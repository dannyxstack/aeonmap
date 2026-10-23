package sim

import (
	"sort"

	"mapsim/world"
)

type HatredEntry struct {
	A, B int64
	V    float32
}

// Snapshot 是可序列化(gob)的完整状态，不含世界数据与工作缓冲。
type Snapshot struct {
	P             Params
	Year          int
	NextUID       int64
	PeoplePerUnit float32
	F             Families
	ClimA, ClimC  []float32
	Hatred        []HatredEntry
	Polities      []PolityInfo
	Inf           Influence
	TotalCnt      Counters
	Super         map[int64]SuperInfo
	Cons          []ConsPolity
}

func (s *State) Snapshot() *Snapshot {
	sn := &Snapshot{P: s.P, Year: s.Year, NextUID: s.NextUID, PeoplePerUnit: s.PeoplePerUnit, F: s.F,
		ClimA: s.ClimA, ClimC: s.ClimC, Inf: s.Inf, TotalCnt: s.TotalCnt, Super: s.Super, Cons: s.Cons}
	for k, v := range s.Hatred {
		sn.Hatred = append(sn.Hatred, HatredEntry{k[0], k[1], v})
	}
	sort.Slice(sn.Hatred, func(a, b int) bool {
		if sn.Hatred[a].A != sn.Hatred[b].A {
			return sn.Hatred[a].A < sn.Hatred[b].A
		}
		return sn.Hatred[a].B < sn.Hatred[b].B
	})
	for _, p := range s.Polities {
		sn.Polities = append(sn.Polities, *p)
	}
	sort.Slice(sn.Polities, func(a, b int) bool { return sn.Polities[a].UID < sn.Polities[b].UID })
	return sn
}

func Restore(sn *Snapshot, w *world.World) *State {
	s := &State{P: sn.P, Year: sn.Year, NextUID: sn.NextUID, PeoplePerUnit: sn.PeoplePerUnit, F: sn.F,
		ClimA: sn.ClimA, ClimC: sn.ClimC, Inf: sn.Inf, TotalCnt: sn.TotalCnt, W: w, Super: sn.Super,
		Hatred: map[[2]int64]float32{}, Polities: map[int64]*PolityInfo{}}
	for _, h := range sn.Hatred {
		s.Hatred[[2]int64{h.A, h.B}] = h.V
	}
	for i := range sn.Polities {
		p := sn.Polities[i]
		s.Polities[p.UID] = &p
	}
	s.Cons = sn.Cons
	if len(s.Cons) > 0 {
		s.buildLocks()
	}
	n := s.F.Len()
	if len(s.F.Dead) != n {
		s.F.Dead = make([]bool, n)
	}
	s.computeRoots()
	return s
}

// Hash 用于检验回放重算与原始演化是否一致。
func (s *State) Hash() uint64 {
	h := uint64(s.Year)
	f := &s.F
	for i := 0; i < f.Len(); i++ {
		h = mix(h, uint64(f.UID[i]))
		h = mix(h, uint64(f.Cell[i])<<32|uint64(uint32(f.Parent[i])))
		h = mix(h, uint64(int64(f.Size[i]*1000)))
	}
	return h
}
