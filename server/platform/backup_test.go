//go:build unix

package platform

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSnapshotHardlinks(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "runs", "r1"), 0o755)
	os.MkdirAll(filepath.Join(root, "exports"), 0o755)
	os.WriteFile(filepath.Join(root, "tenants.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(root, "admin.key"), []byte("mad_x"), 0o600) // 排在所有子目录之前
	os.WriteFile(filepath.Join(root, "runs", "r1", "frames.bin"), make([]byte, 1<<16), 0o644)
	os.WriteFile(filepath.Join(root, "exports", "big.zip"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "x.json.tmp"), []byte("partial"), 0o644)
	s := &Store{Root: root}
	c := BackupConfig{Dir: filepath.Join(root, "backups"), Keep: 2, Exclude: []string{"exports"}}
	d1, n1, l1, err := s.Snapshot(c)
	if err != nil || n1 != 3 || l1 != 0 {
		t.Fatalf("first: %v n=%d l=%d", err, n1, l1)
	}
	if _, err := os.Stat(filepath.Join(d1, "exports")); err == nil {
		t.Fatal("excluded dir was backed up")
	}
	time.Sleep(1100 * time.Millisecond) // 快照目录名精确到秒
	os.WriteFile(filepath.Join(root, "tenants.json"), []byte(`{"a":1}`), 0o644)
	d2, n2, l2, err := s.Snapshot(c)
	if err != nil || n2 != 3 || l2 != 2 {
		t.Fatalf("second: %v n=%d l=%d", err, n2, l2)
	}
	a, _ := os.Stat(filepath.Join(d1, "runs", "r1", "frames.bin"))
	b, _ := os.Stat(filepath.Join(d2, "runs", "r1", "frames.bin"))
	if a.Sys().(*syscall.Stat_t).Ino != b.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("unchanged file was copied instead of hard-linked")
	}
	if got, _ := os.ReadFile(filepath.Join(d2, "tenants.json")); string(got) != `{"a":1}` {
		t.Fatalf("changed file content: %s", got)
	}
	time.Sleep(1100 * time.Millisecond)
	s.Snapshot(c)
	if n := len(snapshots(c.Dir)); n != 2 {
		t.Fatalf("keep=2 but %d snapshots", n)
	}
}
