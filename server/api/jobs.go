package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"mapsim/platform"
	"mapsim/prep"
	"mapsim/sim"
	"mapsim/store"
)

// JobRequest 是一次管线任务：确保世界已预处理 → (可选)演化 → (可选)导出。
type JobRequest struct {
	Simulate *SimReq    `json:"simulate,omitempty"`
	Export   *ExportReq `json:"export,omitempty"`
	Webhook  string     `json:"webhook,omitempty"`
}

type SimReq struct {
	Years       int              `json:"years,omitempty"`
	Seed        uint64           `json:"seed,omitempty"`
	Params      json.RawMessage  `json:"params,omitempty"` // 覆盖 sim.Params 中的任意字段
	Constraints *sim.Constraints `json:"constraints,omitempty"`
}

type ExportReq struct {
	Run        string   `json:"run,omitempty"`  // 不给则用本任务演化的结果；都没有时只导出世界(静态图层)
	Year       *int     `json:"year,omitempty"` // 不给则取最后一年
	Formats    []string `json:"formats,omitempty"`
	EngineSize int      `json:"engineSize,omitempty"`
}

// Runner 执行任务队列。
type Runner struct {
	P         *platform.Store
	E         *Engine
	Python    string
	PrepDir   string
	CacheRoot string
	Workers   int
	OnJob     func(platform.Job)

	queue    chan string
	submitMu sync.Mutex
	mu       sync.Mutex
	jobRun   map[string]string             // 任务 → 正在进行的演化
	jobStop  map[string]context.CancelFunc // 任务 → 取消函数(预处理、演化、导出都会响应)
	prepMu   sync.Map                      // 世界 ID → *sync.Mutex，同一世界的预处理串行
	started  bool
}

func (r *Runner) Start(ctx context.Context) {
	r.queue = make(chan string, 1024)
	r.jobRun = map[string]string{}
	r.jobStop = map[string]context.CancelFunc{}
	if r.Workers <= 0 {
		r.Workers = 2
	}
	for i := 0; i < r.Workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-r.queue:
					r.process(ctx, id)
				}
			}
		}()
	}
	for _, id := range r.P.RecoverJobs() {
		r.queue <- id
	}
	r.started = true
}

// Submit 校验请求、预估并检查额度，然后排队。
func (r *Runner) Submit(t *platform.Tenant, worldID string, req *JobRequest) (*platform.Job, error) {
	w, ok := r.P.World(worldID)
	if !ok || !r.P.Visible(t.ID, w) {
		return nil, fmt.Errorf("世界 %s 不存在", worldID)
	}
	plan := r.P.PlanOf(t)
	if req.Simulate != nil && plan.NoSimulate {
		return nil, &planError{fmt.Errorf("套餐 %s 只包含环境分析(气候、水文、资源)，不能发起历史演化", plan.Name)}
	}
	if req.Simulate != nil {
		years := req.Simulate.Years
		if years == 0 {
			years = sim.DefaultParams().Years
		}
		if years > plan.MaxYears {
			return nil, fmt.Errorf("套餐 %s 单次最多演化 %d 年", plan.Name, plan.MaxYears)
		}
	}
	if req.Webhook != "" {
		if err := r.CheckWebhookURL(req.Webhook); err != nil {
			return nil, err
		}
	}
	est := r.estimate(w, req)
	r.submitMu.Lock()
	defer r.submitMu.Unlock() // 额度检查与登记任务必须原子，避免并发提交同时通过
	if err := r.P.CheckQuota(t.ID, est); err != nil {
		return nil, &quotaError{err}
	}
	raw, _ := json.Marshal(req)
	j := &platform.Job{ID: platform.NewID("job_"), Tenant: t.ID, World: worldID, Status: "queued", Request: raw,
		Estimate: est, Created: time.Now().UTC().Format(time.RFC3339)}
	if err := r.P.PutJob(j); err != nil {
		return nil, err
	}
	r.notify(j)
	r.queue <- j.ID
	return j, nil
}

type quotaError struct{ error }

type planError struct{ error }

// 算力点(系数可在管理后台配置)：预处理 = 陆地格/系数；演化 = 陆地格×年数/系数；导出 = 固定点数。
func (r *Runner) prepUnits(land int) int {
	pr := r.P.Settings().Pricing
	return max(1, int(math.Ceil(float64(land)/pr.PrepCellsPerUnit)))
}

func (r *Runner) simUnits(land, years int) int {
	pr := r.P.Settings().Pricing
	return max(1, int(math.Ceil(float64(land)*float64(years)/pr.SimCellYearsPerUnit)))
}

func (r *Runner) exportUnits() int { return r.P.Settings().Pricing.ExportUnits }

func (r *Runner) estimate(w *platform.World, req *JobRequest) int {
	land := w.LandCells
	u := 0
	if land == 0 {
		land = 30000
		u += r.prepUnits(land)
	}
	if req.Simulate != nil {
		years := req.Simulate.Years
		if years == 0 {
			years = sim.DefaultParams().Years
		}
		u += r.simUnits(land, years)
	}
	if req.Export != nil {
		u += r.exportUnits()
	}
	return u
}

func (r *Runner) notify(j *platform.Job) {
	if r.OnJob != nil {
		r.OnJob(*j)
	}
}

// Cancel：排队中的任务直接取消；正在演化的任务停止演化(已算出的部分保留)。
func (r *Runner) Cancel(jobID string) bool {
	r.submitMu.Lock()
	j, ok := r.P.Job(jobID)
	if ok && j.Status == "queued" {
		j.Status, j.Error, j.Finished = "cancelled", "已取消", time.Now().UTC().Format(time.RFC3339)
		r.P.PutJob(j)
		r.submitMu.Unlock()
		r.notify(j)
		return true
	}
	r.submitMu.Unlock()
	r.mu.Lock()
	stop, ok := r.jobStop[jobID]
	r.mu.Unlock()
	if ok {
		stop()
		return true
	}
	return false
}

func (r *Runner) process(ctx context.Context, id string) {
	r.submitMu.Lock()
	j, ok := r.P.Job(id)
	if !ok || j.Status != "queued" {
		r.submitMu.Unlock()
		return
	}
	j.Status, j.Started = "running", time.Now().UTC().Format(time.RFC3339)
	r.P.PutJob(j)
	r.submitMu.Unlock()
	r.notify(j)
	var req JobRequest
	json.Unmarshal(j.Request, &req)
	jctx, stop := context.WithCancel(ctx)
	r.mu.Lock()
	r.jobStop[j.ID] = stop
	r.mu.Unlock()
	res, err := r.execute(jctx, j, &req)
	if err != nil && jctx.Err() != nil {
		err = fmt.Errorf("已取消")
	}
	r.mu.Lock()
	delete(r.jobStop, j.ID)
	r.mu.Unlock()
	stop()
	j.Result = res
	j.Finished = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		j.Status, j.Error = "failed", err.Error()
		if err.Error() == "已取消" {
			j.Status = "cancelled"
		}
		log.Printf("任务 %s %s: %v", j.ID, j.Status, err)
	} else {
		j.Status, j.Progress, j.Stage, j.Message = "succeeded", 1, "done", ""
	}
	r.P.PutJob(j)
	r.notify(j)
	if req.Webhook != "" {
		go r.sendWebhook(j, req.Webhook)
	}
}

func (r *Runner) stage(j *platform.Job, stage string, p float64, msg string) {
	j.Stage, j.Progress, j.Message = stage, p, msg
	r.P.PutJob(j)
	r.notify(j)
}

func (r *Runner) execute(ctx context.Context, j *platform.Job, req *JobRequest) (*platform.JobResult, error) {
	res := &platform.JobResult{EngineVersion: sim.EngineVersion}
	t, _ := r.P.Tenant(j.Tenant)
	plan := r.P.PlanOf(t)
	w, ok := r.P.World(j.World)
	if !ok {
		return res, fmt.Errorf("世界不存在")
	}
	// 1. 预处理(有缓存)。已预处理过、因程序升级而重算的不扣点
	firstPrep := w.PrepKey == ""
	r.stage(j, "prep", 0, "地形与气候预处理")
	cached, err := r.EnsurePrepped(ctx, w, func(p prep.Progress) {
		r.stage(j, "prep", p.Progress*0.99, p.Stage+" "+p.Message)
	})
	if err != nil {
		return res, err
	}
	w, _ = r.P.World(j.World)
	res.PrepCached = cached
	if !cached && firstPrep {
		u := r.prepUnits(w.LandCells)
		j.Units += u
		r.P.Charge(j.Tenant, j.ID, "prep", u)
	}
	world, err := r.E.LoadWorld(w.CacheDir)
	if err != nil {
		return res, err
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	// 2. 演化
	runID := ""
	if req.Simulate != nil {
		p := sim.DefaultParams()
		if len(req.Simulate.Params) > 0 {
			if err := json.Unmarshal(req.Simulate.Params, &p); err != nil {
				return res, fmt.Errorf("params 无效: %w", err)
			}
		}
		if req.Simulate.Years > 0 {
			p.Years = req.Simulate.Years
		}
		if req.Simulate.Seed > 0 {
			p.Seed = req.Simulate.Seed
		}
		if p.Years > plan.MaxYears {
			return res, fmt.Errorf("套餐 %s 单次最多演化 %d 年", plan.Name, plan.MaxYears)
		}
		p.MaxFamilies = min(p.MaxFamilies, plan.MaxFamilies)
		p.InitFamilies = min(p.InitFamilies, p.MaxFamilies)
		p.TargetFamily = min(p.TargetFamily, p.MaxFamilies*65/100)
		key := RunKey(world.Dir, p, req.Simulate.Constraints)
		if found := r.E.FindRun(key, j.Tenant); found != "" {
			runID, res.RunReused = found, true
			r.stage(j, "simulate", 1, "复用已有的相同演化 "+found)
		} else {
			meta := store.RunMeta{WorldID: w.ID, Tenant: j.Tenant}
			last := time.Now()
			id, err := r.E.RunSync(ctx, world, meta, p, req.Simulate.Constraints, func(run string) {
				r.setJobRun(j.ID, run)
				res.RunID = run
				j.Result = res // 让调用方在演化过程中就能打开实时回放
				r.P.PutJob(j)
			}, func(y, ys int) {
				if time.Since(last) > 2*time.Second || y == ys {
					last = time.Now()
					r.stage(j, "simulate", float64(y)/float64(ys), fmt.Sprintf("第 %d / %d 年", y, ys))
				}
			})
			r.setJobRun(j.ID, "")
			if err != nil {
				return res, err
			}
			runID = id
			u := r.simUnits(w.LandCells, p.Years)
			j.Units += u
			r.P.Charge(j.Tenant, j.ID, "simulate", u)
		}
		res.RunID = runID
	}
	// 3. 导出
	if req.Export != nil {
		if req.Export.Run != "" {
			runID = req.Export.Run
			m, err := readRunMeta(filepath.Join(r.E.RunsDir, filepath.Base(runID)))
			if err != nil || (m.Tenant != j.Tenant && m.Tenant != platform.PublicTenant) {
				return res, fmt.Errorf("演化 %s 不存在", runID)
			}
		}
		r.stage(j, "export", 0, "打包导出")
		eid := platform.NewID("exp_")
		path := filepath.Join(r.P.Root, "exports", eid+".zip")
		if err := r.buildExport(path, w, world, runID, req.Export); err != nil {
			os.Remove(path)
			return res, fmt.Errorf("导出失败: %w", err)
		}
		res.ExportID, res.ExportURL = eid, "/v1/exports/"+eid
		u := r.exportUnits()
		j.Units += u
		r.P.Charge(j.Tenant, j.ID, "export", u)
	}
	return res, nil
}

func (r *Runner) setJobRun(job, run string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if run == "" {
		delete(r.jobRun, job)
	} else {
		r.jobRun[job] = run
	}
}

// RepairPaths：数据目录被挪动(如宿主机 → 容器)后，修正世界记录里的绝对路径。
func (r *Runner) RepairPaths() {
	for _, w := range r.P.AllWorlds() {
		changed := false
		if w.Kind == "upload" {
			if md := filepath.Join(r.P.WorldDir(w.ID), "map"); md != w.MapDir {
				w.MapDir, changed = md, true
			}
		}
		if w.CacheDir != "" {
			if cd := r.E.ResolveCacheDir(w.CacheDir); cd != w.CacheDir {
				w.CacheDir, changed = cd, true
			}
		}
		if changed {
			r.P.PutWorld(&w)
			log.Printf("已修正世界 %s 的路径", w.ID)
		}
	}
}

// EnsurePrepped 确保世界已完成预处理；返回是否命中缓存。
func (r *Runner) EnsurePrepped(ctx context.Context, w *platform.World, onProgress func(prep.Progress)) (bool, error) {
	mu, _ := r.prepMu.LoadOrStore(w.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	w, _ = r.P.World(w.ID)
	params := map[string]any{"sim_res": "auto"}
	key, err := prep.CacheKey(w.MapDir, r.PrepDir, params)
	if err != nil {
		return false, err
	}
	if w.Status == "ready" && w.PrepKey == key {
		if _, err := os.Stat(filepath.Join(w.CacheDir, "manifest.json")); err == nil {
			return true, nil
		}
	}
	w.Status, w.Error = "prepping", ""
	r.P.PutWorld(w)
	cached := false
	dir, err := prep.Run(ctx, r.Python, r.PrepDir, w.MapDir, r.CacheRoot, params, func(p prep.Progress) {
		if p.Stage == "cached" {
			cached = true
		}
		if onProgress != nil {
			onProgress(p)
		}
	})
	if err != nil {
		w.Status, w.Error = "failed", err.Error()
		r.P.PutWorld(w)
		return false, err
	}
	wd, err := r.E.LoadWorld(dir)
	if err != nil {
		w.Status, w.Error = "failed", err.Error()
		r.P.PutWorld(w)
		return false, err
	}
	var mf struct {
		Bounds *platform.Bounds `json:"bounds"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "manifest.json")); err == nil {
		json.Unmarshal(b, &mf)
	}
	w.Status, w.CacheDir, w.PrepKey, w.LandCells = "ready", dir, key, len(wd.LandIdx)
	if mf.Bounds != nil {
		w.Bounds = mf.Bounds
	}
	return cached, r.P.PutWorld(w)
}

// ---- 回调 ----

func (r *Runner) sendWebhook(j *platform.Job, target string) {
	t, ok := r.P.Tenant(j.Tenant)
	if !ok {
		return
	}
	event := "job." + j.Status
	body, _ := json.Marshal(map[string]any{"event": event, "job": j})
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := platform.SignWebhook(t.WebhookSecret, append([]byte(ts+"."), body...))
	client := &http.Client{Timeout: 15 * time.Second}
	for attempt, wait := 0, 2*time.Second; attempt < 4; attempt, wait = attempt+1, wait*4 {
		if attempt > 0 {
			time.Sleep(wait)
		}
		req, _ := http.NewRequest("POST", target, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Mapsim-Event", event)
		req.Header.Set("X-Mapsim-Timestamp", ts)
		req.Header.Set("X-Mapsim-Signature", "sha256="+sig)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 300 {
				return
			}
			err = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		log.Printf("回调 %s 第 %d 次失败: %v", target, attempt+1, err)
	}
}

// CheckWebhookURL：只允许 http(s)；默认禁止回调到内网地址(防 SSRF)。
// 本地开发可在管理后台开启"允许内网回调"，或设置环境变量 MAPSIM_ALLOW_PRIVATE_WEBHOOKS=1。
func (r *Runner) CheckWebhookURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("webhook 必须是 http(s) 地址")
	}
	if os.Getenv("MAPSIM_ALLOW_PRIVATE_WEBHOOKS") == "1" || r.P.Settings().AllowPrivateWebhooks {
		return nil
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("webhook 域名无法解析: %w", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return fmt.Errorf("webhook 不允许指向内网地址(本地开发可设置 MAPSIM_ALLOW_PRIVATE_WEBHOOKS=1)")
		}
	}
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
