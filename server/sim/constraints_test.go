package sim

import (
	"bytes"
	"encoding/gob"
	"os"
	"testing"

	"mapsim/world"
)

func loadTestWorld(t *testing.T) *world.World {
	dir := os.Getenv("MAPSIM_WORLD")
	if dir == "" {
		dir = "../../cache/dev"
	}
	w, err := world.Load(dir)
	if err != nil {
		t.Skip("没有预处理数据:", err)
	}
	return w
}

// 约束区域内的家族始终归约束政权，且约束在检查点恢复后依然生效、结果一致。
func TestConstraintsLockRegion(t *testing.T) {
	w := loadTestWorld(t)
	p := DefaultParams()
	p.InitFamilies, p.MaxFamilies, p.TargetFamily = 20000, 40000, 30000
	cons := &Constraints{Polities: []ConsPolity{{
		Name:    "不列颠",
		Capital: [2]float64{-0.1, 51.5},
		Region:  [][][2]float64{{{-6, 50}, {2, 50}, {2, 59}, {-6, 59}, {-6, 50}}},
	}}}
	s := NewStateWith(w, p, cons)
	for s.Year < 60 {
		s.Step()
	}
	check := func(s *State) {
		roots := s.consRoots()
		if roots[0] < 0 {
			t.Fatal("约束政权首领消失")
		}
		n := 0
		for i := 0; i < s.F.Len(); i++ {
			if s.lockOwner[s.F.Cell[i]] == 0 {
				n++
				if s.F.Root[i] != roots[0] {
					t.Fatalf("第 %d 年：锁定区内家族 %d 不属于约束政权", s.Year, s.F.UID[i])
				}
			}
		}
		if n == 0 {
			t.Fatal("锁定区内没有家族")
		}
	}
	check(s)
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s.Snapshot()); err != nil {
		t.Fatal(err)
	}
	var sn Snapshot
	gob.NewDecoder(&buf).Decode(&sn)
	b := Restore(&sn, w)
	for s.Year < 75 {
		s.Step()
		b.Step()
	}
	if s.Hash() != b.Hash() {
		t.Fatal("带约束的演化在恢复后分叉")
	}
	check(b)
	if pi := s.Polities[s.Cons[0].RootUID]; pi == nil || pi.Name != "不列颠" || !pi.Custom {
		t.Fatal("约束政权名称未登记")
	}
}
