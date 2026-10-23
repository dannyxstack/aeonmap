// Package prep 负责查找 conda 环境里的 Python，并调用预处理脚本生成模拟格数据。
package prep

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const EnvName = "mapsim"

type Progress struct {
	Stage    string  `json:"stage"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message"`
	Manifest string  `json:"manifest,omitempty"`
}

// FindPython 按顺序：显式路径 → conda 环境 mapsim → (可选)创建环境。
func FindPython(ctx context.Context, explicit, envYml string, create bool, log func(string)) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p := condaEnvPython(ctx); p != "" {
		return p, nil
	}
	if !create {
		return "", fmt.Errorf("找不到 conda 环境 %s；请先执行 conda env create -f %s", EnvName, envYml)
	}
	log("创建 conda 环境 " + EnvName + " ...")
	cmd := exec.CommandContext(ctx, "conda", "env", "create", "-y", "-f", envYml)
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 conda 失败: %w", err)
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		log(sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("conda env create 失败: %w", err)
	}
	if p := condaEnvPython(ctx); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("conda 环境创建后仍找不到解释器")
}

func condaEnvPython(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "conda", "env", "list", "--json").Output()
	if err != nil {
		return ""
	}
	var r struct {
		Envs []string `json:"envs"`
	}
	if json.Unmarshal(out, &r) != nil {
		return ""
	}
	for _, e := range r.Envs {
		if filepath.Base(e) == EnvName {
			p := filepath.Join(e, "bin", "python")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// CacheKey = hash(地图目录所有文件 + 预处理代码 + 参数)。
func CacheKey(mapDir, prepDir string, params map[string]any) (string, error) {
	h := sha256.New()
	for _, root := range []string{mapDir, prepDir} {
		var files []string
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// colorband 是色带识别工具，不参与预处理，改动它不应让已有缓存失效
			if d.IsDir() && (d.Name() == "__pycache__" || d.Name() == "colorband") {
				return filepath.SkipDir
			}
			if !d.IsDir() && (strings.HasSuffix(p, ".py") || strings.HasSuffix(p, ".json") ||
				strings.HasSuffix(p, ".jpg") || strings.HasSuffix(p, ".png") || strings.HasSuffix(p, ".yml")) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		sort.Strings(files)
		for _, f := range files {
			rel, _ := filepath.Rel(root, f)
			io.WriteString(h, rel)
			b, err := os.ReadFile(f)
			if err != nil {
				return "", err
			}
			h.Write(b)
		}
	}
	pj, _ := json.Marshal(params)
	h.Write(pj)
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// Run 调用预处理；缓存命中则直接返回目录。onProgress 会收到每一行进度。
func Run(ctx context.Context, python, prepDir, mapDir, cacheRoot string, params map[string]any,
	onProgress func(Progress)) (string, error) {
	key, err := CacheKey(mapDir, prepDir, params)
	if err != nil {
		return "", err
	}
	out := filepath.Join(cacheRoot, key)
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err == nil {
		onProgress(Progress{Stage: "cached", Progress: 1, Message: "使用缓存 " + key})
		return out, nil
	}
	pj, _ := json.Marshal(params)
	tmp := out + ".tmp"
	os.RemoveAll(tmp)
	cmd := exec.CommandContext(ctx, python, "-u", filepath.Join(prepDir, "main.py"),
		"--map", mapDir, "--out", tmp, "--params", string(pj))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 Python 失败: %w", err)
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		var p Progress
		if json.Unmarshal(sc.Bytes(), &p) == nil {
			onProgress(p)
		}
	}
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("预处理失败: %v\n%s", err, tail(stderr.String(), 3000))
	}
	os.RemoveAll(out)
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	return out, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
