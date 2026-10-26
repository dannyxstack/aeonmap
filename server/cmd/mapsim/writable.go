package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// checkWritable 在启动时确认目录可写；不可写时给出原因与修复命令，而不是让后续 mkdir 报 permission denied。
// 常见原因：用 Docker 运行时宿主机的 ./data 或 ./cache 不存在，被 Docker 以 root 身份自动创建，
// 而容器以 docker-compose.yml 里的 user(默认 1000:1000)运行。
func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err == nil {
		f, err := os.CreateTemp(dir, ".write-test-*")
		if err == nil {
			name := f.Name()
			f.Close()
			os.Remove(name)
			return nil
		}
	}
	abs, _ := filepath.Abs(dir)
	owner := "未知"
	if fi, err := os.Stat(dir); err == nil {
		if uid, gid, ok := fileOwner(fi); ok {
			owner = fmt.Sprintf("%d:%d", uid, gid)
		}
	}
	return fmt.Errorf(`目录 %s 不可写(当前进程 %d:%d，目录属主 %s)。
如果用 Docker 运行，在宿主机项目目录下执行其中一种：
  sudo chown -R %d:%d data cache                              # 让目录归容器用户所有
  printf 'UID=%%s\nGID=%%s\n' $(id -u) $(id -g) > .env && docker compose up -d   # 或让容器改用当前宿主机用户运行`,
		abs, os.Getuid(), os.Getgid(), owner, os.Getuid(), os.Getgid())
}
