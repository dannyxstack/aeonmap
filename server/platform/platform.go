// Package platform 管理租户、接口密钥、世界项目、异步任务与用量计量。
//
// MVP 阶段用 JSON 文件持久化(每个对象一个文件，原子写入)，数据量增大后再换数据库。
//
//	data/
//	  tenants.json
//	  worlds/<wid>/world.json  input/  map/
//	  jobs/<jid>.json
//	  runs/<rid>/              (store 包的回放格式)
//	  exports/<eid>.zip
//	  usage/<tenant>-<YYYYMM>.jsonl
package platform

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Plan 是套餐：每月算力点额度、单次演化上限与月费(管理后台可配置)。
type Plan struct {
	Name         string  `json:"name"`
	Label        string  `json:"label"`
	MonthlyUnits int     `json:"monthlyUnits"` // -1 不限
	MaxYears     int     `json:"maxYears"`
	MaxFamilies  int     `json:"maxFamilies"`
	MaxUploadMB  int     `json:"maxUploadMB"`
	PriceMonthly float64 `json:"priceMonthly"`         // >0 视为付费套餐
	NoSimulate   bool    `json:"noSimulate,omitempty"` // 只含环境分析(气候、水文、资源)，不能发起历史演化
	MaxActive    int     `json:"maxActiveJobs,omitempty"` // 同时排队+运行的任务上限；0 = 默认 2
}

// ActiveLimit：每用户同时进行中的任务上限。
func (p Plan) ActiveLimit() int {
	if p.MaxActive > 0 {
		return p.MaxActive
	}
	return 2
}

type KeyRec struct {
	Hash    string `json:"hash"`
	Prefix  string `json:"prefix"` // 便于识别，不含完整密钥
	Created string `json:"created"`
	Revoked bool   `json:"revoked,omitempty"`
}

type Tenant struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Email         string   `json:"email,omitempty"`
	Plan          string   `json:"plan"`
	Keys          []KeyRec `json:"keys"`
	WebhookSecret string   `json:"webhookSecret"`
	Suspended     bool     `json:"suspended,omitempty"`     // 停用后密钥全部失效
	QuotaOverride *int     `json:"quotaOverride,omitempty"` // 单独设置的每月额度(覆盖套餐)
	Note          string   `json:"note,omitempty"`          // 管理员备注
	Source        string   `json:"source,omitempty"`        // cli | admin | signup
	Created       string   `json:"created"`
}

// PublicTenant 拥有内置世界与历史回放，所有租户可读。
const PublicTenant = "public"

type Bounds struct {
	West  float64 `json:"west"`
	East  float64 `json:"east"`
	South float64 `json:"south"`
	North float64 `json:"north"`
}

type World struct {
	ID        string          `json:"id"`
	Tenant    string          `json:"tenant"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`  // builtin | upload
	Input     string          `json:"input"` // hypsometric | grayscale | sketch
	MapDir    string          `json:"mapDir"`
	Bounds    *Bounds         `json:"bounds,omitempty"`
	Elevation json.RawMessage `json:"elevation,omitempty"`
	Status    string          `json:"status"` // uploaded | prepping | ready | failed
	CacheDir  string          `json:"cacheDir,omitempty"`
	PrepKey   string          `json:"prepKey,omitempty"`
	LandCells int             `json:"landCells,omitempty"`
	Error     string          `json:"error,omitempty"`
	Created   string          `json:"created"`
}

type Job struct {
	ID       string          `json:"id"`
	Tenant   string          `json:"tenant"`
	World    string          `json:"world"`
	Status   string          `json:"status"` // queued | running | succeeded | failed | cancelled
	Stage    string          `json:"stage,omitempty"`
	Progress float64         `json:"progress"`
	Message  string          `json:"message,omitempty"`
	Request  json.RawMessage `json:"request"`
	Result   *JobResult      `json:"result,omitempty"`
	Error    string          `json:"error,omitempty"`
	Units    int             `json:"units"`
	Estimate int             `json:"estimate"` // 提交时预估的算力点(排队与运行期间计入额度占用)
	Created  string          `json:"created"`
	Started  string          `json:"started,omitempty"`
	Finished string          `json:"finished,omitempty"`
}

type JobResult struct {
	RunID         string `json:"runId,omitempty"`
	RunReused     bool   `json:"runReused,omitempty"`
	PrepCached    bool   `json:"prepCached,omitempty"`
	ExportID      string `json:"exportId,omitempty"`
	ExportURL     string `json:"exportUrl,omitempty"`
	EngineVersion string `json:"engineVersion,omitempty"`
}

type UsageRec struct {
	Time   string `json:"time"`
	Tenant string `json:"tenant"`
	Job    string `json:"job"`
	Kind   string `json:"kind"`
	Units  int    `json:"units"`
}

type Store struct {
	Root string
	mu   sync.Mutex
	ten  map[string]*Tenant
	keys map[string]string // 密钥哈希 → 租户
	wld  map[string]*World
	jobs map[string]*Job
	used map[string]int // 租户-月份 → 已用算力点
	tmod time.Time      // tenants.json 的修改时间(命令行新建租户后服务端自动重新加载)
	set  Settings
	seen map[string]string // 租户 → 最近一次调用接口的时间
	sess map[string]*Session // 会话令牌哈希 → 会话
}

var ErrNotFound = errors.New("不存在")

func Open(root string) (*Store, error) {
	s := &Store{Root: root, ten: map[string]*Tenant{}, keys: map[string]string{}, wld: map[string]*World{},
		jobs: map[string]*Job{}, used: map[string]int{}, seen: map[string]string{}}
	for _, d := range []string{"worlds", "jobs", "runs", "exports", "usage"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	if err := s.loadTenants(); err != nil {
		return nil, err
	}
	if err := s.loadSettings(); err != nil {
		return nil, err
	}
	s.loadSessions()
	if s.ten[PublicTenant] == nil {
		s.ten[PublicTenant] = &Tenant{ID: PublicTenant, Name: "公共", Plan: "internal", Created: now()}
		if err := s.saveTenantsLocked(); err != nil {
			return nil, err
		}
	}
	ents, _ := os.ReadDir(filepath.Join(root, "worlds"))
	for _, e := range ents {
		var w World
		if readJSON(filepath.Join(root, "worlds", e.Name(), "world.json"), &w) == nil {
			s.wld[w.ID] = &w
		}
	}
	ents, _ = os.ReadDir(filepath.Join(root, "jobs"))
	for _, e := range ents {
		var j Job
		if readJSON(filepath.Join(root, "jobs", e.Name()), &j) == nil {
			s.jobs[j.ID] = &j
		}
	}
	ents, _ = os.ReadDir(filepath.Join(root, "usage"))
	for _, e := range ents {
		b, _ := os.ReadFile(filepath.Join(root, "usage", e.Name()))
		for _, line := range strings.Split(string(b), "\n") {
			var u UsageRec
			if json.Unmarshal([]byte(line), &u) == nil && u.Tenant != "" {
				s.used[u.Tenant+"-"+u.Time[:7]] += u.Units
			}
		}
	}
	return s, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSON 原子写入：先写临时文件再改名。
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func NewID(prefix string) string {
	b := make([]byte, 6)
	rand.Read(b)
	return prefix + time.Now().UTC().Format("060102") + hex.EncodeToString(b)
}

func hashKey(k string) string {
	h := sha256.Sum256([]byte(k))
	return hex.EncodeToString(h[:])
}

// ---------------- 租户与密钥 ----------------

func (s *Store) loadTenants() error {
	path := filepath.Join(s.Root, "tenants.json")
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var ts []*Tenant
	if err := json.Unmarshal(b, &ts); err != nil {
		return fmt.Errorf("tenants.json: %w", err)
	}
	s.ten = map[string]*Tenant{}
	s.keys = map[string]string{}
	for _, t := range ts {
		s.ten[t.ID] = t
		for _, k := range t.Keys {
			if !k.Revoked {
				s.keys[k.Hash] = t.ID
			}
		}
	}
	s.tmod = st.ModTime()
	return nil
}

// reloadIfChanged：tenants.json 被其他进程(命令行)修改过时重新加载。调用方须持有 s.mu。
func (s *Store) reloadIfChanged() {
	st, err := os.Stat(filepath.Join(s.Root, "tenants.json"))
	if err == nil && st.ModTime().After(s.tmod) {
		if err := s.loadTenants(); err != nil {
			log.Printf("重新加载 tenants.json 失败: %v", err)
		}
	}
}

func (s *Store) saveTenantsLocked() error {
	ts := make([]*Tenant, 0, len(s.ten))
	for _, t := range s.ten {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(a, b int) bool { return ts[a].ID < ts[b].ID })
	path := filepath.Join(s.Root, "tenants.json")
	if err := writeJSON(path, ts); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil {
		s.tmod = st.ModTime()
	}
	return nil
}

// CreateTenant 新建租户并返回一把接口密钥(只在此时明文出现一次)。
func (s *Store) CreateTenant(name, plan string) (*Tenant, string, error) {
	return s.CreateTenantFull(name, "", plan, "cli")
}

func (s *Store) CreateTenantFull(name, email, plan, source string) (*Tenant, string, error) {
	return s.createTenant(name, email, plan, source, true)
}

// CreateTenantNoKey：网页登录(邮箱/Google)注册的用户不需要接口密钥，需要时在控制台自己新建。
func (s *Store) CreateTenantNoKey(name, email, plan, source string) (*Tenant, error) {
	t, _, err := s.createTenant(name, email, plan, source, false)
	return t, err
}

func (s *Store) createTenant(name, email, plan, source string, withKey bool) (*Tenant, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.set.Plans[plan]; !ok {
		return nil, "", fmt.Errorf("未知套餐 %s", plan)
	}
	if email != "" {
		for _, t := range s.ten {
			if strings.EqualFold(t.Email, email) {
				return nil, "", fmt.Errorf("邮箱 %s 已注册", email)
			}
		}
	}
	sec := make([]byte, 24)
	rand.Read(sec)
	t := &Tenant{ID: NewID("t_"), Name: name, Email: email, Plan: plan, Source: source,
		WebhookSecret: "whsec_" + hex.EncodeToString(sec), Created: now()}
	s.ten[t.ID] = t
	if !withKey {
		return t, "", s.saveTenantsLocked()
	}
	key, err := s.addKeyLocked(t)
	if err != nil {
		return nil, "", err
	}
	return t, key, nil
}

// DeleteTenant 删除租户记录(密钥随之失效)、其世界与任务记录。演化由调用方删除。
func (s *Store) DeleteTenant(tenantID string) error {
	if tenantID == PublicTenant {
		return fmt.Errorf("不能删除公共租户")
	}
	s.mu.Lock()
	if _, ok := s.ten[tenantID]; !ok {
		s.mu.Unlock()
		return ErrNotFound
	}
	delete(s.ten, tenantID)
	s.deleteTenantSessionsLocked(tenantID)
	for h, id := range s.keys {
		if id == tenantID {
			delete(s.keys, h)
		}
	}
	var worlds, jobs []string
	for id, w := range s.wld {
		if w.Tenant == tenantID {
			worlds = append(worlds, id)
			delete(s.wld, id)
		}
	}
	for id, j := range s.jobs {
		if j.Tenant == tenantID {
			jobs = append(jobs, id)
			delete(s.jobs, id)
		}
	}
	err := s.saveTenantsLocked()
	s.mu.Unlock()
	for _, id := range worlds {
		os.RemoveAll(s.WorldDir(id))
	}
	for _, id := range jobs {
		os.Remove(filepath.Join(s.Root, "jobs", id+".json"))
	}
	return err
}

func (s *Store) AddKey(tenantID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ten[tenantID]
	if t == nil {
		return "", ErrNotFound
	}
	return s.addKeyLocked(t)
}

func (s *Store) addKeyLocked(t *Tenant) (string, error) {
	b := make([]byte, 24)
	rand.Read(b)
	key := "msk_" + hex.EncodeToString(b)
	h := hashKey(key)
	t.Keys = append(t.Keys, KeyRec{Hash: h, Prefix: key[:10], Created: now()})
	s.keys[h] = t.ID
	return key, s.saveTenantsLocked()
}

func (s *Store) RevokeKey(tenantID, prefix string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ten[tenantID]
	if t == nil {
		return ErrNotFound
	}
	n := 0
	for i := range t.Keys {
		if t.Keys[i].Prefix == prefix && !t.Keys[i].Revoked {
			t.Keys[i].Revoked = true
			delete(s.keys, t.Keys[i].Hash)
			n++
		}
	}
	if n == 0 {
		return ErrNotFound
	}
	return s.saveTenantsLocked()
}

func (s *Store) SetPlan(tenantID, plan string) error {
	p := plan
	return s.UpdateTenant(tenantID, TenantPatch{Plan: &p})
}

// TenantPatch：管理后台可修改的租户字段(nil 表示不改)。
type TenantPatch struct {
	Name          *string `json:"name"`
	Email         *string `json:"email"`
	Plan          *string `json:"plan"`
	Suspended     *bool   `json:"suspended"`
	QuotaOverride *int    `json:"quotaOverride"` // 设为 -2 表示清除单独额度
	Note          *string `json:"note"`
}

func (s *Store) UpdateTenant(tenantID string, p TenantPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.ten[tenantID]
	if t == nil {
		return ErrNotFound
	}
	if p.Plan != nil {
		if _, ok := s.set.Plans[*p.Plan]; !ok {
			return fmt.Errorf("未知套餐 %s", *p.Plan)
		}
		t.Plan = *p.Plan
	}
	if p.Name != nil {
		t.Name = *p.Name
	}
	if p.Email != nil {
		t.Email = *p.Email
	}
	if p.Note != nil {
		t.Note = *p.Note
	}
	if p.Suspended != nil {
		if tenantID == PublicTenant && *p.Suspended {
			return fmt.Errorf("不能停用公共租户")
		}
		t.Suspended = *p.Suspended
		if t.Suspended {
			s.deleteTenantSessionsLocked(t.ID)
		}
	}
	if p.QuotaOverride != nil {
		if *p.QuotaOverride == -2 {
			t.QuotaOverride = nil
		} else if *p.QuotaOverride < -1 {
			return fmt.Errorf("额度无效")
		} else {
			v := *p.QuotaOverride
			t.QuotaOverride = &v
		}
	}
	return s.saveTenantsLocked()
}

// Authenticate 用接口密钥找到租户。
func (s *Store) Authenticate(key string) (*Tenant, bool) {
	if !strings.HasPrefix(key, "msk_") {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged() // 命令行新建租户或吊销密钥后立即生效
	id, ok := s.keys[hashKey(key)]
	if !ok || s.ten[id].Suspended {
		return nil, false
	}
	s.touch(id)
	t := *s.ten[id]
	return &t, true
}

func (s *Store) Tenants() []Tenant {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Tenant
	for _, t := range s.ten {
		out = append(out, *t)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

func (s *Store) Tenant(id string) (*Tenant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.ten[id]
	if !ok {
		return nil, false
	}
	c := *t
	return &c, true
}

// SignWebhook：回调请求体的 HMAC-SHA256 签名(十六进制)。
func SignWebhook(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// ---------------- 用量 ----------------

func month() string { return time.Now().UTC().Format("2006-01") }

// UsedThisMonth 返回本月已用算力点。
func (s *Store) UsedThisMonth(tenant string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used[tenant+"-"+month()]
}

// CheckQuota 在提交任务时检查额度：本月已用 + 排队/运行中任务的预估 + 本次预估。
func (s *Store) CheckQuota(tenant string, units int) error {
	t, ok := s.Tenant(tenant)
	if !ok {
		return ErrNotFound
	}
	p := s.PlanOf(t)
	if p.MonthlyUnits < 0 {
		return nil
	}
	used := s.UsedThisMonth(tenant)
	pending := s.PendingUnits(tenant)
	if used+pending+units > p.MonthlyUnits {
		return fmt.Errorf("本月额度不足：已用 %d，进行中任务预计 %d，本次约需 %d，套餐 %s 每月 %d 点",
			used, pending, units, p.Name, p.MonthlyUnits)
	}
	return nil
}

// PendingUnits：排队或运行中任务尚未结算的预估算力点。
func (s *Store) PendingUnits(tenant string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.Tenant == tenant && (j.Status == "queued" || j.Status == "running") {
			n += max(j.Estimate-j.Units, 0)
		}
	}
	return n
}

func (s *Store) Charge(tenant, job, kind string, units int) error {
	if units <= 0 {
		return nil
	}
	rec := UsageRec{Time: now(), Tenant: tenant, Job: job, Kind: kind, Units: units}
	b, _ := json.Marshal(rec)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used[tenant+"-"+rec.Time[:7]] += units
	f, err := os.OpenFile(filepath.Join(s.Root, "usage", tenant+"-"+strings.ReplaceAll(rec.Time[:7], "-", "")+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Store) UsageRecords(tenant string) []UsageRec {
	b, _ := os.ReadFile(filepath.Join(s.Root, "usage", tenant+"-"+strings.ReplaceAll(month(), "-", "")+".jsonl"))
	var out []UsageRec
	for _, line := range strings.Split(string(b), "\n") {
		var u UsageRec
		if json.Unmarshal([]byte(line), &u) == nil && u.Tenant != "" {
			out = append(out, u)
		}
	}
	return out
}

// ---------------- 世界 ----------------

func (s *Store) WorldDir(id string) string { return filepath.Join(s.Root, "worlds", id) }

func (s *Store) PutWorld(w *World) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *w
	s.wld[w.ID] = &c
	return writeJSON(filepath.Join(s.WorldDir(w.ID), "world.json"), &c)
}

func (s *Store) World(id string) (*World, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.wld[id]
	if !ok {
		return nil, false
	}
	c := *w
	return &c, true
}

// Visible：租户自己的世界，以及公共世界。
func (s *Store) Visible(tenant string, w *World) bool {
	return w.Tenant == tenant || w.Tenant == PublicTenant
}

func (s *Store) Worlds(tenant string) []World {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []World
	for _, w := range s.wld {
		if w.Tenant == tenant || w.Tenant == PublicTenant {
			out = append(out, *w)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created > out[b].Created })
	return out
}

func (s *Store) DeleteWorld(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.wld[id]; !ok {
		return ErrNotFound
	}
	delete(s.wld, id)
	return os.RemoveAll(s.WorldDir(id))
}

// ---------------- 任务 ----------------

func (s *Store) PutJob(j *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := *j
	s.jobs[j.ID] = &c
	return writeJSON(filepath.Join(s.Root, "jobs", j.ID+".json"), &c)
}

func (s *Store) Job(id string) (*Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	if !ok {
		return nil, false
	}
	c := *j
	return &c, true
}

func (s *Store) Jobs(tenant string, limit int) []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Job{}
	for _, j := range s.jobs {
		if j.Tenant == tenant {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created > out[b].Created })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RecoverJobs：服务重启后，排队中的任务重新排队，运行中的标记为失败。返回需要重新排队的任务。
func (s *Store) RecoverJobs() []string {
	s.mu.Lock()
	var requeue []string
	var failed []*Job
	for _, j := range s.jobs {
		switch j.Status {
		case "queued":
			requeue = append(requeue, j.ID)
		case "running":
			j.Status, j.Error, j.Finished = "failed", "服务重启，任务中断，请重新提交", now()
			failed = append(failed, j)
		}
	}
	s.mu.Unlock()
	for _, j := range failed {
		s.PutJob(j)
	}
	sort.Strings(requeue)
	return requeue
}
