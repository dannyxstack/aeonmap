package sim

import (
	"bytes"
	"encoding/gob"
	"os"
	"testing"

	"mapsim/world"
)

// 检查点 → 序列化 → 恢复 → 重算，必须与原始演化逐位一致(回放查看家族明细依赖这一点)。
func TestCheckpointDeterminism(t *testing.T) {
	dir := os.Getenv("MAPSIM_WORLD")
	if dir == "" {
		dir = "../../cache/dev"
	}
	w, err := world.Load(dir)
	if err != nil {
		t.Skip("没有预处理数据:", err)
	}
	p := DefaultParams()
	p.InitFamilies = 20000
	p.MaxFamilies = 40000
	p.TargetFamily = 30000
	a := NewState(w, p)
	for a.Year < 40 {
		a.Step()
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(a.Snapshot()); err != nil {
		t.Fatal(err)
	}
	var sn Snapshot
	if err := gob.NewDecoder(&buf).Decode(&sn); err != nil {
		t.Fatal(err)
	}
	b := Restore(&sn, w)
	if a.Hash() != b.Hash() {
		t.Fatal("恢复后状态不一致")
	}
	for a.Year < 63 {
		a.Step()
		b.Step()
		if a.Hash() != b.Hash() {
			t.Fatalf("第 %d 年分叉", a.Year)
		}
	}
	fa, fb := a.BuildFrame(62), b.BuildFrame(62)
	if !bytes.Equal(fa, fb) {
		t.Fatal("帧不一致")
	}
}
