// Package buildinfo 汇总当前运行程序的版本信息，供 GET /v1/version 与 `mapsim version` 使用。
//
// Docker 镜像里没有 .git：构建时把源码指纹、构建时间与(可选的)提交号写进 /app/build.json(见 Dockerfile)；
// 本地 go build 时读取 Go 嵌入的版本控制信息(vcs.revision 等)。
package buildinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"time"
)

type Info struct {
	Commit     string `json:"commit"`               // 提交号(未知时为空)
	CommitTime string `json:"commitTime,omitempty"` // 提交时间
	Dirty      bool   `json:"dirty"`                // 构建时有未提交的改动(仅本地构建可知)
	SourceHash string `json:"sourceHash,omitempty"` // 源码指纹(deploy/source-hash.sh)，Docker 构建才有
	BuildTime  string `json:"buildTime,omitempty"`
	Go         string `json:"go"`
	Source     string `json:"source"` // docker | vcs | unknown
}

var started = time.Now().UTC()

func Started() time.Time { return started }

// Load 读取构建信息：优先 <root>/build.json，其次 Go 嵌入的版本控制信息。
func Load(root string) Info {
	in := Info{Go: runtime.Version(), Source: "unknown"}
	if b, err := os.ReadFile(filepath.Join(root, "build.json")); err == nil {
		var f Info
		if json.Unmarshal(b, &f) == nil {
			f.Go, f.Source = runtime.Version(), "docker"
			return f
		}
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				in.Commit, in.Source = s.Value, "vcs"
			case "vcs.time":
				in.CommitTime = s.Value
			case "vcs.modified":
				in.Dirty = s.Value == "true"
			}
		}
	}
	return in
}

var prepVer = regexp.MustCompile(`(?m)^VERSION\s*=\s*"([^"]+)"`)

// PrepVersion：预处理(Python)版本，读自 prep/mapprep/__init__.py。
func PrepVersion(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "prep", "mapprep", "__init__.py"))
	if err != nil {
		return ""
	}
	if m := prepVer.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

var apiVer = regexp.MustCompile(`(?m)^\s+version:\s*"?([^"\s]+)"?`)

// APIVersion：接口文档 docs/openapi.yaml 里 info.version。
func APIVersion(spec []byte) string {
	if m := apiVer.FindSubmatch(spec); m != nil {
		return string(m[1])
	}
	return ""
}
