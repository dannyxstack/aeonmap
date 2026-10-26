// mapsim：AeonMap 世界推演引擎的唯一入口。
//
//	mapsim [serve]                         启动接口服务与网页(默认 http://127.0.0.1:8765)
//	mapsim run -years 4000                 在内置欧亚世界上无界面演化(结果归公共租户)
//	mapsim tenant create -name 工作室A -plan pro    新建租户并打印接口密钥
//	mapsim tenant list | plan -tenant ID -plan studio
//	mapsim key add -tenant ID | key revoke -tenant ID -prefix msk_xxxxxx
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mapsim/api"
	"mapsim/platform"
	"mapsim/prep"
	"mapsim/sim"
	"mapsim/web"
)

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	sub := ""
	if (cmd == "tenant" || cmd == "key") && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fl := flag.NewFlagSet(cmd, flag.ExitOnError)
	root := fl.String("root", "", "项目根目录(默认自动查找含 prep/main.py 的目录)")
	dataDir := fl.String("data", envOr("MAPSIM_DATA", ""), "数据目录(默认 <root>/data；环境变量 MAPSIM_DATA)")
	python := fl.String("python", os.Getenv("MAPSIM_PYTHON"), "Python 解释器路径(默认使用 conda 环境 mapsim；环境变量 MAPSIM_PYTHON)")
	addr := fl.String("addr", envOr("MAPSIM_ADDR", "127.0.0.1:8765"), "监听地址(环境变量 MAPSIM_ADDR)")
	workers := fl.Int("workers", 2, "同时执行的任务数")
	years := fl.Int("years", 0, "run: 演化年数")
	params := fl.String("params", "{}", "run: 覆盖参数(JSON)")
	name := fl.String("name", "", "tenant create: 名称")
	plan := fl.String("plan", "free", "套餐：free | pro | studio | internal")
	tenantID := fl.String("tenant", "", "租户 ID")
	prefix := fl.String("prefix", "", "key revoke: 密钥前缀")
	fl.Parse(args)
	sim.SetWorkers(runtime.NumCPU())

	r, err := findRoot(*root)
	if err != nil {
		log.Fatal(err)
	}
	if *dataDir == "" {
		*dataDir = filepath.Join(r, "data")
	}
	for _, d := range []string{*dataDir, filepath.Join(r, "cache")} {
		if err := checkWritable(d); err != nil {
			log.Fatal(err)
		}
	}
	store, err := platform.Open(*dataDir)
	if err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "tenant":
		tenantCmd(store, sub, *name, *plan, *tenantID)
		return
	case "key":
		keyCmd(store, sub, *tenantID, *prefix)
		return
	}

	migrateLegacyRuns(r, *dataDir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	py, err := prep.FindPython(ctx, *python, filepath.Join(r, "prep", "environment.yml"), true, func(s string) { log.Println(s) })
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Python: %s", py)
	eng := api.NewEngine(filepath.Join(*dataDir, "runs"))
	eng.CacheRoot = filepath.Join(r, "cache")
	runner := &api.Runner{P: store, E: eng, Python: py, PrepDir: filepath.Join(r, "prep"),
		CacheRoot: eng.CacheRoot, Workers: *workers}
	runner.RepairPaths()
	if err := ensureBuiltin(ctx, store, runner, r); err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "run":
		runner.Start(ctx)
		p := sim.DefaultParams()
		if err := json.Unmarshal([]byte(*params), &p); err != nil {
			log.Fatal(err)
		}
		if *years > 0 {
			p.Years = *years
		}
		pj, _ := json.Marshal(p)
		pub, _ := store.Tenant(platform.PublicTenant)
		job, err := runner.Submit(pub, "eurasia", &api.JobRequest{Simulate: &api.SimReq{Params: pj}})
		if err != nil {
			log.Fatal(err)
		}
		for {
			time.Sleep(2 * time.Second)
			j, _ := store.Job(job.ID)
			fmt.Printf("\r%-9s %-8s %5.1f%% %s          ", j.Status, j.Stage, j.Progress*100, j.Message)
			if j.Status == "succeeded" || j.Status == "failed" {
				fmt.Println()
				if j.Status == "failed" {
					log.Fatal(j.Error)
				}
				log.Printf("完成：%s/runs/%s", *dataDir, j.Result.RunID)
				return
			}
		}
	case "serve":
		if os.Getenv("MAPSIM_LOCAL_TENANT") == "off" {
			revokeLocalKey(store, *dataDir) // 生产环境：不建本地租户密钥，已有的吊销
		} else {
			bootstrapLocalTenant(store, *dataDir, *addr)
		}
		runner.Start(ctx)
		if os.Getenv("MAPSIM_BACKUP") != "off" {
			bc := platform.BackupConfig{Dir: envOr("MAPSIM_BACKUP_DIR", filepath.Join(*dataDir, "backups")),
				Every: time.Duration(envInt("MAPSIM_BACKUP_HOURS", 24)) * time.Hour, Keep: envInt("MAPSIM_BACKUP_KEEP", 7),
				Exclude: []string{"exports"}}
			log.Printf("自动备份：每 %v 一份，保留 %d 份，目录 %s", bc.Every, bc.Keep, bc.Dir)
			go store.RunBackups(ctx, bc)
		}
		spec, _ := os.ReadFile(filepath.Join(r, "docs", "openapi.yaml"))
		srv := api.NewServer(eng, runner, store, web.FS(), spec)
		tok, created, err := store.AdminToken()
		if err != nil {
			log.Fatal(err)
		}
		srv.AdminToken = tok
		if created {
			log.Printf("已生成管理员令牌，保存在 %s(管理后台 /admin.html)", filepath.Join(*dataDir, "admin.key"))
		}
		hs := &http.Server{Addr: *addr, Handler: srv.Handler()}
		go func() {
			<-ctx.Done()
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			hs.Shutdown(c)
		}()
		log.Printf("服务已启动：http://%s  (控制台 /console.html，接口文档 /v1/openapi.yaml)", *addr)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	default:
		log.Fatalf("未知命令 %s", cmd)
	}
}

// ensureBuiltin 登记内置的欧亚世界(公共租户)并确保已预处理。
func ensureBuiltin(ctx context.Context, store *platform.Store, runner *api.Runner, root string) error {
	mapDir := filepath.Join(root, "maps", "eurasia")
	w, ok := store.World("eurasia")
	if !ok {
		w = &platform.World{ID: "eurasia", Tenant: platform.PublicTenant, Name: "欧亚大陆(内置)", Kind: "builtin",
			Input: "hypsometric", MapDir: mapDir, Status: "uploaded", Created: time.Now().UTC().Format(time.RFC3339),
			Bounds: &platform.Bounds{West: -10, East: 180, South: 12, North: 78}}
	}
	w.MapDir = mapDir
	if err := store.PutWorld(w); err != nil {
		return err
	}
	log.Printf("检查内置世界的预处理…")
	_, err := runner.EnsurePrepped(ctx, w, func(p prep.Progress) {
		log.Printf("[预处理] %-10s %3.0f%% %s", p.Stage, p.Progress*100, p.Message)
	})
	return err
}

// bootstrapLocalTenant：首次启动时创建本地租户，密钥写入 <data>/local.key(仅本机可读)，便于本地使用控制台。
func bootstrapLocalTenant(store *platform.Store, dataDir, addr string) {
	keyFile := filepath.Join(dataDir, "local.key")
	if b, err := os.ReadFile(keyFile); err == nil {
		if _, ok := store.Authenticate(strings.TrimSpace(string(b))); ok {
			log.Printf("本地接口密钥见 %s", keyFile)
			return
		}
	}
	t, key, err := store.CreateTenant("本地", "internal")
	if err != nil {
		log.Printf("创建本地租户失败: %v", err)
		return
	}
	os.WriteFile(keyFile, []byte(key+"\n"), 0o600)
	log.Printf("已创建本地租户 %s，接口密钥保存在 %s", t.ID, keyFile)
}

func tenantCmd(store *platform.Store, sub, name, plan, id string) {
	switch sub {
	case "create":
		if name == "" {
			log.Fatal("需要 -name")
		}
		t, key, err := store.CreateTenant(name, plan)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("租户 %s (%s, 套餐 %s)\n接口密钥(只显示这一次)：%s\n回调签名密钥：%s\n", t.ID, t.Name, t.Plan, key, t.WebhookSecret)
	case "list":
		for _, t := range store.Tenants() {
			active := 0
			for _, k := range t.Keys {
				if !k.Revoked {
					active++
				}
			}
			fmt.Printf("%-22s %-10s %-9s 本月已用 %5d 点  有效密钥 %d\n", t.ID, t.Name, t.Plan, store.UsedThisMonth(t.ID), active)
		}
	case "plan":
		if err := store.SetPlan(id, plan); err != nil {
			log.Fatal(err)
		}
		fmt.Println("已更新")
	default:
		log.Fatal("用法：mapsim tenant create|list|plan")
	}
}

func keyCmd(store *platform.Store, sub, id, prefix string) {
	switch sub {
	case "add":
		k, err := store.AddKey(id)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("新接口密钥(只显示这一次)：", k)
	case "revoke":
		if err := store.RevokeKey(id, prefix); err != nil {
			log.Fatal(err)
		}
		fmt.Println("已吊销")
	default:
		log.Fatal("用法：mapsim key add|revoke -tenant ID [-prefix msk_xxxx]")
	}
}

// migrateLegacyRuns：把旧版 <root>/runs 下的演化移到数据目录，并归入公共租户的欧亚世界。
func migrateLegacyRuns(root, dataDir string) {
	old := filepath.Join(root, "runs")
	ents, err := os.ReadDir(old)
	if err != nil {
		return
	}
	for _, e := range ents {
		src := filepath.Join(old, e.Name())
		dst := filepath.Join(dataDir, "runs", e.Name())
		if _, err := os.Stat(dst); err == nil || !e.IsDir() {
			continue
		}
		var m map[string]any
		b, err := os.ReadFile(filepath.Join(src, "run.json"))
		if err != nil || json.Unmarshal(b, &m) != nil {
			continue
		}
		if m["tenant"] == nil || m["tenant"] == "" {
			m["tenant"] = platform.PublicTenant
			m["worldId"] = "eurasia"
			m["engineVersion"] = "1.0.0"
			m["done"] = true
			nb, _ := json.MarshalIndent(m, "", " ")
			os.WriteFile(filepath.Join(src, "run.json"), nb, 0o644)
		}
		if err := os.Rename(src, dst); err == nil {
			log.Printf("已迁移旧演化 %s → %s", e.Name(), dst)
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func findRoot(explicit string) (string, error) {
	cands := []string{explicit}
	if explicit == "" {
		wd, _ := os.Getwd()
		cands = []string{wd, filepath.Dir(wd)}
		if exe, err := os.Executable(); err == nil {
			cands = append(cands, filepath.Dir(exe), filepath.Dir(filepath.Dir(exe)))
		}
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(c, "prep", "main.py")); err == nil && !st.IsDir() {
			return filepath.Abs(c)
		}
	}
	return "", fmt.Errorf("找不到项目根目录(需要包含 prep/main.py)，请用 -root 指定")
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

// revokeLocalKey：MAPSIM_LOCAL_TENANT=off 时，吊销 data/local.key 里的密钥并删除该文件。
// 本地租户及其世界保留：在管理后台给它填上你的邮箱后，用邮箱登录即可继续使用。
func revokeLocalKey(store *platform.Store, dataDir string) {
	keyFile := filepath.Join(dataDir, "local.key")
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return
	}
	key := strings.TrimSpace(string(b))
	if t, ok := store.Authenticate(key); ok && len(key) >= 10 {
		if err := store.RevokeKey(t.ID, key[:10]); err != nil {
			log.Printf("吊销本地密钥失败: %v", err)
			return
		}
		log.Printf("MAPSIM_LOCAL_TENANT=off：已吊销本地租户 %s 的密钥 %s…", t.ID, key[:10])
	}
	os.Remove(keyFile)
}
