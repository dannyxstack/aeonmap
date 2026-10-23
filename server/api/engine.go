package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"mapsim/sim"
	"mapsim/store"
	"mapsim/world"
)

const (
	FrameEvery = 5
	CkptEvery  = 200
)

// Engine 管理多个世界的格子数据、演化任务与回放。每次演化的状态只由它自己的 goroutine 修改。
type Engine struct {
	RunsDir string
	// CacheRoot 预处理缓存根目录；记录里的缓存路径失效(如从宿主机挪进容器)时按目录名在此重新定位
	CacheRoot string

	mu      sync.Mutex
	worlds  map[string]*world.World // 预处理缓存目录 → 世界
	running map[string]*liveRun
	readers map[string]*store.Reader
	onStat  func(Status)

	inspMu   sync.Mutex
	inspRun  string
	inspStat *sim.State
}

type liveRun struct {
	id     string
	w      *store.Writer
	cancel context.CancelFunc
	year   int
	years  int
	start  time.Time
	tenant string
}

type Status struct {
	Type      string  `json:"type"`
	Running   bool    `json:"running"`
	Run       string  `json:"run,omitempty"`
	Tenant    string  `json:"-"`
	Year      int     `json:"year"`
	Years     int     `json:"years"`
	MsPerYear float64 `json:"msPerYear,omitempty"`
	Message   string  `json:"message,omitempty"`
}

func NewEngine(runsDir string) *Engine {
	return &Engine{RunsDir: runsDir, worlds: map[string]*world.World{}, running: map[string]*liveRun{},
		readers: map[string]*store.Reader{}}
}

func (e *Engine) SetStatusHook(f func(Status)) { e.onStat = f }

func (e *Engine) status(st Status) {
	st.Type = "status"
	if e.onStat != nil {
		e.onStat(st)
	}
}

// ResolveCacheDir：记录的缓存目录不存在时，按目录名(即缓存键)在 CacheRoot 下查找。
func (e *Engine) ResolveCacheDir(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil || e.CacheRoot == "" {
		return dir
	}
	alt := filepath.Join(e.CacheRoot, filepath.Base(dir))
	if _, err := os.Stat(filepath.Join(alt, "manifest.json")); err == nil {
		return alt
	}
	return dir
}

// LoadWorld 按预处理缓存目录加载(并缓存)世界数据。
func (e *Engine) LoadWorld(dir string) (*world.World, error) {
	dir = e.ResolveCacheDir(dir)
	e.mu.Lock()
	if w, ok := e.worlds[dir]; ok {
		e.mu.Unlock()
		return w, nil
	}
	e.mu.Unlock()
	w, err := world.Load(dir)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.worlds[dir] = w
	e.mu.Unlock()
	return w, nil
}

func (e *Engine) WorldOfRun(id string) (*world.World, *store.RunMeta, error) {
	rd, err := e.Reader(id)
	if err != nil {
		return nil, nil, err
	}
	w, err := e.LoadWorld(rd.Meta.World)
	if err != nil {
		return nil, nil, err
	}
	m := rd.Meta
	return w, &m, nil
}

// RunKey 是演化的复现键：同一键 = 同一历史，可以直接复用已有结果。
func RunKey(worldDir string, p sim.Params, c *sim.Constraints) string {
	h := sha256.New()
	h.Write([]byte(filepath.Base(worldDir)))
	h.Write([]byte(sim.EngineVersion))
	pj, _ := json.Marshal(p)
	h.Write(pj)
	if !c.Empty() {
		cj, _ := json.Marshal(c)
		h.Write(cj)
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// FindRun 找到同一复现键、已完成且对该租户可见的演化。
func (e *Engine) FindRun(key, tenant string) string {
	for _, m := range e.ListRuns("", "") {
		if m.Key == key && m.Done && (m.Tenant == tenant || m.Tenant == "public") {
			return m.ID
		}
	}
	return ""
}

// RunSync 执行一次演化直到结束(由任务队列调用)。progress 每年回调一次。
func (e *Engine) RunSync(ctx context.Context, w *world.World, meta store.RunMeta, p sim.Params, cons *sim.Constraints,
	onStart func(runID string), progress func(year, years int)) (string, error) {
	id := time.Now().Format("20060102-150405") + "-" + randHex(3)
	dir := filepath.Join(e.RunsDir, id)
	meta.ID = id
	meta.Params = p
	meta.Constraints = cons
	meta.World = w.Dir
	meta.EngineVersion = sim.EngineVersion
	meta.FrameEvery, meta.CkptEvery = FrameEvery, CkptEvery
	meta.Created = time.Now().Format(time.RFC3339)
	meta.Key = RunKey(w.Dir, p, cons)
	wr, err := store.Create(dir, meta, len(w.LandIdx))
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	lr := &liveRun{id: id, w: wr, cancel: cancel, years: p.Years, start: time.Now(), tenant: meta.Tenant}
	rd, err := store.Open(dir, len(w.LandIdx), wr.Index)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.running[id] = lr
	e.readers[id] = rd
	e.mu.Unlock()
	if onStart != nil {
		onStart(id)
	}
	err = e.loop(ctx, w, lr, p, cons, progress)
	e.mu.Lock()
	delete(e.running, id)
	if rd, err2 := store.Open(dir, len(w.LandIdx), nil); err2 == nil {
		e.readers[id] = rd
	}
	e.mu.Unlock()
	e.status(Status{Run: id, Tenant: meta.Tenant, Year: lr.year, Years: lr.years, Message: "演化结束"})
	return id, err
}

func (e *Engine) loop(ctx context.Context, w *world.World, lr *liveRun, p sim.Params, cons *sim.Constraints,
	progress func(int, int)) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("演化崩溃: %v", r)
			log.Print(err)
		}
		lr.w.Meta.ComputedUntil = lr.year
		if err != nil {
			lr.w.Meta.Failed = err.Error()
		}
		lr.w.Close()
	}()
	dir := filepath.Join(e.RunsDir, lr.id)
	s := sim.NewStateWith(w, p, cons)
	lr.w.Meta.PeoplePerUnit = s.PeoplePerUnit
	lr.w.SaveMeta()
	if err := store.SaveCheckpoint(dir, s); err != nil {
		return err
	}
	lastStat := time.Now()
	for y := 0; y < p.Years; y++ {
		if ctx.Err() != nil {
			return fmt.Errorf("已取消")
		}
		s.Step()
		m := s.Metrics()
		m.Year = y
		lr.w.AddMetrics(m)
		lr.w.AddEvents(s.Events)
		s.Events = s.Events[:0]
		if y%FrameEvery == 0 {
			if err := lr.w.AddFrame(y, s.BuildFrame(y)); err != nil {
				return fmt.Errorf("写帧失败: %w", err)
			}
		}
		if s.Year%CkptEvery == 0 {
			if err := store.SaveCheckpoint(dir, s); err != nil {
				return err
			}
		}
		if y%100 == 0 || y == p.Years-1 {
			lr.w.SavePolities(s.Polities)
			lr.w.Meta.ComputedUntil = y
			lr.w.SaveMeta()
			lr.w.Sync()
		}
		lr.year = y + 1
		if progress != nil {
			progress(y+1, p.Years)
		}
		if time.Since(lastStat) > 500*time.Millisecond {
			lastStat = time.Now()
			el := time.Since(lr.start).Seconds() * 1000
			e.status(Status{Running: true, Run: lr.id, Tenant: lr.tenant, Year: y, Years: p.Years, MsPerYear: el / float64(y+1)})
		}
	}
	lr.w.Meta.Done = true
	return nil
}

func (e *Engine) Stop(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if lr, ok := e.running[id]; ok {
		lr.cancel()
		return true
	}
	return false
}

func (e *Engine) Reader(id string) (*store.Reader, error) {
	e.mu.Lock()
	if r, ok := e.readers[id]; ok {
		e.mu.Unlock()
		return r, nil
	}
	e.mu.Unlock()
	meta, err := readRunMeta(filepath.Join(e.RunsDir, filepath.Base(id)))
	if err != nil {
		return nil, err
	}
	w, err := e.LoadWorld(meta.World)
	if err != nil {
		return nil, fmt.Errorf("演化 %s 的世界数据不可用: %w", id, err)
	}
	r, err := store.Open(filepath.Join(e.RunsDir, filepath.Base(id)), len(w.LandIdx), nil)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.readers[id] = r
	e.mu.Unlock()
	return r, nil
}

func readRunMeta(dir string) (*store.RunMeta, error) {
	b, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return nil, err
	}
	var m store.RunMeta
	return &m, json.Unmarshal(b, &m)
}

// ListRuns 列出演化(tenant 为空表示全部；worldID 为空表示所有世界)。只读 run.json，不加载格子数据。
func (e *Engine) ListRuns(tenant, worldID string) []store.RunMeta {
	ents, _ := os.ReadDir(e.RunsDir)
	out := []store.RunMeta{}
	for _, d := range ents {
		if !d.IsDir() {
			continue
		}
		m, err := readRunMeta(filepath.Join(e.RunsDir, d.Name()))
		if err != nil {
			continue
		}
		if tenant != "" && m.Tenant != tenant && m.Tenant != "public" {
			continue
		}
		if worldID != "" && m.WorldID != worldID {
			continue
		}
		e.mu.Lock()
		if lr, ok := e.running[m.ID]; ok {
			m.ComputedUntil = lr.year
		}
		e.mu.Unlock()
		out = append(out, *m)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID > out[b].ID })
	return out
}

func (e *Engine) RunningOf(tenant string) []Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Status
	for _, lr := range e.running {
		if tenant == "" || lr.tenant == tenant {
			out = append(out, Status{Type: "status", Running: true, Run: lr.id, Year: lr.year, Years: lr.years})
		}
	}
	return out
}

func (e *Engine) DeleteRun(id string) error {
	e.mu.Lock()
	if _, ok := e.running[id]; ok {
		e.mu.Unlock()
		return fmt.Errorf("演化正在运行，请先停止")
	}
	delete(e.readers, id)
	e.mu.Unlock()
	return os.RemoveAll(filepath.Join(e.RunsDir, filepath.Base(id)))
}

// WithStateAt 通过"最近检查点 + 向后重算"得到某帧年份结束时的完整状态，并在锁内调用 fn 读取。
func (e *Engine) WithStateAt(id string, frameYear int, fn func(*sim.State)) error {
	e.inspMu.Lock()
	defer e.inspMu.Unlock()
	w, _, err := e.WorldOfRun(id)
	if err != nil {
		return err
	}
	target := frameYear + 1 // 帧在第 y 年模拟结束后生成，此时 s.Year = y+1
	if !(e.inspRun == id && e.inspStat != nil && e.inspStat.Year <= target && target-e.inspStat.Year < CkptEvery) {
		s, err := store.LoadCheckpoint(filepath.Join(e.RunsDir, id), target, w)
		if err != nil {
			return err
		}
		e.inspRun, e.inspStat = id, s
	}
	s := e.inspStat
	for s.Year < target {
		s.Step()
		s.Events = s.Events[:0]
	}
	if fn != nil {
		fn(s)
	}
	return nil
}
