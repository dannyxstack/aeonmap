package platform

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 自动备份：定期把数据目录做成快照 <备份目录>/<时间戳>/，保留最近 N 份。
// 与上一份快照相比大小和修改时间都没变的文件用硬链接(不占额外空间)，所以每份快照都是完整目录、可直接拷回恢复。
// 演化帧等大文件写完后不再变化，日常增量很小。异地备份可用 rclone 等工具同步整个备份目录。

type BackupConfig struct {
	Dir     string        // 备份目录。默认 <数据目录>/backups：与数据在同一挂载点，硬链接才生效(Docker 里不同挂载点之间不能硬链接)
	Every   time.Duration // 间隔
	Keep    int           // 保留份数
	Exclude []string      // 不备份的顶层子目录(如 exports：导出包可随时重新生成)
}

// RunBackups 在后台循环：启动 1 分钟后做第一份，之后按间隔执行。
func (s *Store) RunBackups(ctx context.Context, c BackupConfig) {
	if c.Every <= 0 {
		c.Every = 24 * time.Hour
	}
	if c.Keep <= 0 {
		c.Keep = 7
	}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		start := time.Now()
		dir, n, linked, err := s.Snapshot(c)
		if err != nil {
			log.Printf("备份失败: %v", err)
		} else {
			log.Printf("已备份到 %s：%d 个文件(其中 %d 个与上一份相同，用硬链接)，用时 %.1fs", dir, n, linked, time.Since(start).Seconds())
		}
		t.Reset(c.Every)
	}
}

// Snapshot 立即做一份快照，返回快照目录、文件数、硬链接数。
func (s *Store) Snapshot(c BackupConfig) (string, int, int, error) {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return "", 0, 0, err
	}
	prev := latestSnapshot(c.Dir)
	name := time.Now().UTC().Format("20060102-150405")
	tmp := filepath.Join(c.Dir, name+".partial")
	os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil { // 根目录下的文件(如 admin.key)可能排在所有子目录之前
		return "", 0, 0, err
	}
	skip := map[string]bool{}
	for _, e := range c.Exclude {
		skip[e] = true
	}
	files, linked := 0, 0
	backupAbs, _ := filepath.Abs(c.Dir)
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 文件在遍历中被删除：跳过
		}
		if abs, _ := filepath.Abs(p); d.IsDir() && abs == backupAbs {
			return filepath.SkipDir // 备份目录本身在数据目录里
		}
		rel, _ := filepath.Rel(s.Root, p)
		if rel == "." {
			return nil
		}
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if skip[top] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		if !d.Type().IsRegular() || strings.HasSuffix(p, ".tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if prev != "" {
			if pi, err := os.Stat(filepath.Join(prev, rel)); err == nil && pi.Size() == info.Size() && pi.ModTime().Equal(info.ModTime()) {
				if os.Link(filepath.Join(prev, rel), dst) == nil {
					files++
					linked++
					return nil
				}
			}
		}
		if err := copyFile(p, dst, info); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		files++
		return nil
	})
	if err != nil {
		os.RemoveAll(tmp)
		return "", 0, 0, err
	}
	final := filepath.Join(c.Dir, name)
	if err := os.Rename(tmp, final); err != nil {
		return "", 0, 0, err
	}
	pruneSnapshots(c.Dir, c.Keep)
	return final, files, linked, nil
}

func copyFile(src, dst string, info fs.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm()&0o700|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime()) // 保留修改时间，下一份才能识别未变化的文件
}

func snapshots(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if e.IsDir() && len(e.Name()) == 15 && !strings.HasSuffix(e.Name(), ".partial") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func latestSnapshot(dir string) string {
	s := snapshots(dir)
	if len(s) == 0 {
		return ""
	}
	return filepath.Join(dir, s[len(s)-1])
}

func pruneSnapshots(dir string, keep int) {
	s := snapshots(dir)
	for i := 0; i < len(s)-keep; i++ {
		os.RemoveAll(filepath.Join(dir, s[i]))
	}
}
