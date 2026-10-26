package api

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mapsim/platform"
	"mapsim/sim"
	"mapsim/store"
)

var trustCF = os.Getenv("MAPSIM_TRUST_CF") == "1"

// 管理后台接口 /admin/v1/*，使用独立的管理员令牌(data/admin.key 或环境变量 MAPSIM_ADMIN_TOKEN)。

func (s *Server) adminAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.CFAccess != nil {
			jwt := r.Header.Get("Cf-Access-Jwt-Assertion")
			if jwt == "" {
				if c, err := r.Cookie("CF_Authorization"); err == nil {
					jwt = c.Value
				}
			}
			if _, err := s.CFAccess.Verify(r.Context(), jwt); err != nil {
				fail(w, 403, "forbidden", "管理后台需要通过 Cloudflare Access 验证："+err.Error())
				return
			}
		}
		tok := ""
		if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
			tok = strings.TrimSpace(hdr[7:])
		} else if v := r.Header.Get("X-Admin-Token"); v != "" {
			tok = v
		}
		if s.AdminToken == "" || !platform.TokenEqual(tok, s.AdminToken) {
			fail(w, 401, "unauthorized", "需要管理员令牌(见 data/admin.key)")
			return
		}
		h(w, r)
	}
}

// clientIP 返回真实客户端地址。直连方是本机或内网(即反向代理，如 deploy/Caddyfile)时，
// 取 X-Forwarded-For 最右一项(代理追加的那一项，客户端伪造不了)；公网直连时忽略该头。
func clientIP(r *http.Request) string {
	// 源站只经 Cloudflare 访问时(设置 MAPSIM_TRUST_CF=1)，真实地址在 CF-Connecting-IP
	if trustCF {
		if v := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); net.ParseIP(v) != nil {
			return v
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		h = r.RemoteAddr
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if v := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(v) != nil {
				return v
			}
		}
	}
	return h
}

func (s *Server) adminRoutes(mux *http.ServeMux) {
	a := s.adminAuth
	mux.HandleFunc("GET /admin/v1/overview", a(s.adminOverview))
	mux.HandleFunc("GET /admin/v1/system", a(s.adminSystem))
	mux.HandleFunc("GET /admin/v1/tenants", a(s.adminTenants))
	mux.HandleFunc("POST /admin/v1/tenants", a(s.adminCreateTenant))
	mux.HandleFunc("GET /admin/v1/tenants/{id}", a(s.adminTenant))
	mux.HandleFunc("PATCH /admin/v1/tenants/{id}", a(s.adminPatchTenant))
	mux.HandleFunc("DELETE /admin/v1/tenants/{id}", a(s.adminDeleteTenant))
	mux.HandleFunc("POST /admin/v1/tenants/{id}/keys", a(s.adminAddKey))
	mux.HandleFunc("DELETE /admin/v1/tenants/{id}/keys/{prefix}", a(s.adminRevokeKey))
	mux.HandleFunc("GET /admin/v1/jobs", a(s.adminJobs))
	mux.HandleFunc("DELETE /admin/v1/jobs/{id}", a(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !s.R.Cancel(id) {
			fail(w, 409, "conflict", "只能取消排队中或正在运行的任务")
			return
		}
		s.P.Audit("job.cancel", id, clientIP(r), nil)
		writeJSON(w, 202, map[string]any{"cancelling": id})
	}))
	mux.HandleFunc("GET /admin/v1/settings", a(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.P.Settings())
	}))
	mux.HandleFunc("PUT /admin/v1/settings", a(s.adminPutSettings))
	mux.HandleFunc("GET /admin/v1/audit", a(func(w http.ResponseWriter, r *http.Request) {
		out := s.P.AuditLog(300)
		if out == nil {
			out = []platform.AuditRec{}
		}
		writeJSON(w, 200, out)
	}))
	mux.HandleFunc("POST /v1/signup", s.handleSignup)
}

// ---- 概览 ----

type dayStat struct {
	Date     string `json:"date"`
	Prep     int    `json:"prep"`
	Simulate int    `json:"simulate"`
	Export   int    `json:"export"`
	Jobs     int    `json:"jobs"`
	Failed   int    `json:"failed"`
	Signups  int    `json:"signups"`
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 366 {
		days = 30
	}
	set := s.P.Settings()
	tenants := s.P.Tenants()
	month := time.Now().UTC().Format("2006-01")
	since30 := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	jobs := s.P.AllJobs()
	activeT := map[string]bool{}
	for _, j := range jobs {
		if j.Created >= since30 {
			activeT[j.Tenant] = true
		}
	}
	byPlan := map[string]int{}
	var total, paid, suspended, newThisMonth, anon int
	var mrr float64
	for _, t := range tenants {
		if t.ID == platform.PublicTenant {
			continue
		}
		if t.IsAnonymous() { // 免登录试用账户单独计数，不算注册用户
			anon++
			continue
		}
		total++
		byPlan[t.Plan]++
		if t.Suspended {
			suspended++
			continue
		}
		if strings.HasPrefix(t.Created, month) {
			newThisMonth++
		}
		if p := set.Plans[t.Plan]; p.PriceMonthly > 0 {
			paid++
			mrr += p.PriceMonthly
		}
		if s.P.LastSeen(t.ID) >= since30 {
			activeT[t.ID] = true
		}
	}
	delete(activeT, platform.PublicTenant)
	// 每日序列
	series := map[string]*dayStat{}
	var order []string
	for d := days - 1; d >= 0; d-- {
		k := time.Now().UTC().AddDate(0, 0, -d).Format("2006-01-02")
		series[k] = &dayStat{Date: k}
		order = append(order, k)
	}
	byKind := map[string]int{}
	tenantUnits := map[string]int{}
	for _, u := range s.P.UsageRows(days) {
		if d := series[u.Time[:10]]; d != nil {
			switch u.Kind {
			case "prep":
				d.Prep += u.Units
			case "simulate":
				d.Simulate += u.Units
			case "export":
				d.Export += u.Units
			}
		}
		if strings.HasPrefix(u.Time, month) {
			byKind[u.Kind] += u.Units
			tenantUnits[u.Tenant] += u.Units
		}
	}
	status := map[string]int{}
	var last24, failed24 int
	since24 := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	for _, j := range jobs {
		status[j.Status]++
		if d := series[safeDay(j.Created)]; d != nil {
			d.Jobs++
			if j.Status == "failed" {
				d.Failed++
			}
		}
		if j.Created >= since24 {
			last24++
			if j.Status == "failed" {
				failed24++
			}
		}
	}
	for _, t := range tenants {
		if d := series[safeDay(t.Created)]; d != nil && t.ID != platform.PublicTenant {
			d.Signups++
		}
	}
	type top struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Plan  string `json:"plan"`
		Units int    `json:"units"`
	}
	var tops []top
	for id, u := range tenantUnits {
		t, ok := s.P.Tenant(id)
		if !ok {
			continue
		}
		tops = append(tops, top{id, t.Name, t.Plan, u})
	}
	sort.Slice(tops, func(a, b int) bool { return tops[a].Units > tops[b].Units })
	if len(tops) > 10 {
		tops = tops[:10]
	}
	var monthUnits int
	for _, v := range byKind {
		monthUnits += v
	}
	uploads := 0
	for _, wd := range s.P.AllWorlds() {
		if wd.Kind == "upload" {
			uploads++
		}
	}
	seriesOut := make([]*dayStat, 0, len(order))
	for _, k := range order {
		seriesOut = append(seriesOut, series[k])
	}
	writeJSON(w, 200, map[string]any{
		"tenants": map[string]any{"total": total, "paid": paid, "suspended": suspended, "newThisMonth": newThisMonth,
			"active30d": len(activeT), "byPlan": byPlan, "anonymous": anon, "anonUnitsToday": s.P.AnonUsedToday()},
		"revenue":   map[string]any{"mrr": mrr, "currency": set.Currency},
		"usage":     map[string]any{"month": month, "units": monthUnits, "byKind": byKind, "top": tops},
		"jobs":      map[string]any{"total": len(jobs), "last24h": last24, "failed24h": failed24, "byStatus": status},
		"worlds":    map[string]any{"uploads": uploads},
		"runs":      len(s.E.ListRuns("", "")),
		"running":   s.E.RunningOf(""),
		"storage":   map[string]any{"dataBytes": platform.DirSize(s.P.Root), "cacheBytes": platform.DirSize(s.R.CacheRoot)},
		"series":    seriesOut,
		"engine":    sim.EngineVersion,
		"generated": time.Now().UTC().Format(time.RFC3339),
	})
}

func safeDay(t string) string {
	if len(t) >= 10 {
		return t[:10]
	}
	return ""
}

// ---- 用户 ----

type tenantRow struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Email       string  `json:"email,omitempty"`
	Plan        string  `json:"plan"`
	PlanLabel   string  `json:"planLabel"`
	Paid        bool    `json:"paid"`
	Price       float64 `json:"price"`
	Suspended   bool    `json:"suspended"`
	Source      string  `json:"source,omitempty"`
	Created     string  `json:"created"`
	ActiveKeys  int     `json:"activeKeys"`
	UsedMonth   int     `json:"usedMonth"`
	Quota       int     `json:"quota"`
	QuotaCustom bool    `json:"quotaCustom"`
	Worlds      int     `json:"worlds"`
	Runs        int     `json:"runs"`
	Jobs        int     `json:"jobs"`
	FailedJobs  int     `json:"failedJobs"`
	LastJob     string  `json:"lastJob,omitempty"`
	LastSeen    string  `json:"lastSeen,omitempty"`
	Note        string  `json:"note,omitempty"`
}

func (s *Server) tenantRows() []tenantRow {
	set := s.P.Settings()
	worlds := map[string]int{}
	for _, wd := range s.P.AllWorlds() {
		worlds[wd.Tenant]++
	}
	runs := map[string]int{}
	for _, m := range s.E.ListRuns("", "") {
		runs[m.Tenant]++
	}
	jobs, failed, last := map[string]int{}, map[string]int{}, map[string]string{}
	for _, j := range s.P.AllJobs() {
		jobs[j.Tenant]++
		if j.Status == "failed" {
			failed[j.Tenant]++
		}
		if j.Created > last[j.Tenant] {
			last[j.Tenant] = j.Created
		}
	}
	var out []tenantRow
	for _, t := range s.P.Tenants() {
		p := s.P.PlanOf(&t)
		active := 0
		for _, k := range t.Keys {
			if !k.Revoked {
				active++
			}
		}
		out = append(out, tenantRow{ID: t.ID, Name: t.Name, Email: t.Email, Plan: t.Plan, PlanLabel: set.Plans[t.Plan].Label,
			Paid: set.Plans[t.Plan].PriceMonthly > 0, Price: set.Plans[t.Plan].PriceMonthly, Suspended: t.Suspended,
			Source: t.Source, Created: t.Created, ActiveKeys: active, UsedMonth: s.P.UsedThisMonth(t.ID),
			Quota: p.MonthlyUnits, QuotaCustom: t.QuotaOverride != nil, Worlds: worlds[t.ID], Runs: runs[t.ID],
			Jobs: jobs[t.ID], FailedJobs: failed[t.ID], LastJob: last[t.ID], LastSeen: s.P.LastSeen(t.ID), Note: t.Note})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created > out[b].Created })
	return out
}

func (s *Server) adminTenants(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.tenantRows())
}

func (s *Server) adminTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, ok := s.P.Tenant(id)
	if !ok {
		fail(w, 404, "not_found", "用户不存在")
		return
	}
	var row tenantRow
	for _, x := range s.tenantRows() {
		if x.ID == id {
			row = x
		}
	}
	jobs := s.P.Jobs(id, 30)
	worlds := []platform.World{}
	for _, wd := range s.P.AllWorlds() {
		if wd.Tenant == id {
			worlds = append(worlds, wd)
		}
	}
	runs := []store.RunMeta{}
	for _, m := range s.E.ListRuns("", "") {
		if m.Tenant == id {
			runs = append(runs, m)
		}
	}
	keys := []map[string]any{}
	for _, k := range t.Keys {
		keys = append(keys, map[string]any{"prefix": k.Prefix, "created": k.Created, "revoked": k.Revoked})
	}
	writeJSON(w, 200, map[string]any{"tenant": row, "keys": keys, "usageByMonth": s.P.UsageByMonth(id),
		"usageRecords": s.P.UsageRecords(id), "jobs": jobs, "worlds": worlds, "runs": runs})
}

func (s *Server) adminCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Email string `json:"email"`
		Plan  string `json:"plan"`
		Note  string `json:"note"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req) != nil || strings.TrimSpace(req.Name) == "" {
		fail(w, 400, "bad_request", "需要 name")
		return
	}
	if req.Email != "" {
		if _, err := mail.ParseAddress(req.Email); err != nil {
			fail(w, 400, "bad_request", "邮箱格式不对")
			return
		}
	}
	if req.Plan == "" {
		req.Plan = s.P.Settings().SignupPlan
	}
	t, key, err := s.P.CreateTenantFull(strings.TrimSpace(req.Name), req.Email, req.Plan, "admin")
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	if req.Note != "" {
		s.P.UpdateTenant(t.ID, platform.TenantPatch{Note: &req.Note})
	}
	s.P.Audit("tenant.create", t.ID, clientIP(r), map[string]any{"name": t.Name, "plan": t.Plan})
	writeJSON(w, 201, map[string]any{"tenant": t, "key": key})
}

func (s *Server) adminPatchTenant(w http.ResponseWriter, r *http.Request) {
	var p platform.TenantPatch
	raw := map[string]any{}
	body, _ := readAllLimit(w, r, 1<<16)
	if json.Unmarshal(body, &p) != nil || json.Unmarshal(body, &raw) != nil {
		fail(w, 400, "bad_request", "请求体不是合法 JSON")
		return
	}
	id := r.PathValue("id")
	if err := s.P.UpdateTenant(id, p); err != nil {
		code := 400
		if err == platform.ErrNotFound {
			code = 404
		}
		fail(w, code, "bad_request", err.Error())
		return
	}
	s.P.Audit("tenant.update", id, clientIP(r), raw)
	writeJSON(w, 200, map[string]any{"updated": id})
}

// adminDeleteTenant：删除用户及其世界、演化与任务(不可恢复)。有运行中任务时拒绝。
func (s *Server) adminDeleteTenant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.P.Tenant(id); !ok {
		fail(w, 404, "not_found", "用户不存在")
		return
	}
	for _, j := range s.P.Jobs(id, 0) {
		if j.Status == "running" || j.Status == "queued" {
			fail(w, 409, "conflict", "该用户有进行中的任务，请先取消")
			return
		}
	}
	runs := 0
	for _, m := range s.E.ListRuns("", "") {
		if m.Tenant == id {
			s.E.DeleteRun(m.ID)
			runs++
		}
	}
	if err := s.P.DeleteTenant(id); err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	s.P.Audit("tenant.delete", id, clientIP(r), map[string]any{"runs": runs})
	writeJSON(w, 200, map[string]any{"deleted": id, "runs": runs})
}

func (s *Server) adminAddKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	k, err := s.P.AddKey(id)
	if err != nil {
		fail(w, 404, "not_found", err.Error())
		return
	}
	s.P.Audit("key.add", id, clientIP(r), map[string]any{"prefix": k[:10]})
	writeJSON(w, 201, map[string]any{"key": k})
}

func (s *Server) adminRevokeKey(w http.ResponseWriter, r *http.Request) {
	id, prefix := r.PathValue("id"), r.PathValue("prefix")
	if err := s.P.RevokeKey(id, prefix); err != nil {
		fail(w, 404, "not_found", "密钥不存在")
		return
	}
	s.P.Audit("key.revoke", id, clientIP(r), map[string]any{"prefix": prefix})
	writeJSON(w, 200, map[string]any{"revoked": prefix})
}

// ---- 任务 ----

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	names := map[string]string{}
	for _, t := range s.P.Tenants() {
		names[t.ID] = t.Name
	}
	type row struct {
		platform.Job
		TenantName string `json:"tenantName"`
	}
	out := []row{}
	for _, j := range s.P.AllJobs() {
		if (q.Get("tenant") != "" && j.Tenant != q.Get("tenant")) || (q.Get("status") != "" && j.Status != q.Get("status")) {
			continue
		}
		out = append(out, row{j, names[j.Tenant]})
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, 200, out)
}

// ---- 配置 ----

func (s *Server) adminPutSettings(w http.ResponseWriter, r *http.Request) {
	body, err := readAllLimit(w, r, 1<<18)
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	var st platform.Settings
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		fail(w, 400, "bad_request", "配置格式错误: "+err.Error())
		return
	}
	old := s.P.Settings()
	if err := s.P.SetSettings(st); err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	s.P.Audit("settings.update", "", clientIP(r), map[string]any{"before": old, "after": st})
	writeJSON(w, 200, st)
}

// ---- 自助注册 ----

var signupLimit = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

// handleSignup：管理后台开启"自助注册"后，任何人可用名称与邮箱注册，获得注册套餐与一把接口密钥。
func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	set := s.P.Settings()
	if !set.AllowSignup {
		fail(w, 403, "signup_closed", "未开放自助注册，请联系管理员开通")
		return
	}
	ip := clientIP(r)
	signupLimit.Lock()
	var recent []time.Time
	for _, t := range signupLimit.m[ip] {
		if time.Since(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		signupLimit.Unlock()
		fail(w, 429, "rate_limited", "注册过于频繁，请稍后再试")
		return
	}
	signupLimit.m[ip] = append(recent, time.Now())
	signupLimit.Unlock()
	var req struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&req) != nil || strings.TrimSpace(req.Name) == "" {
		fail(w, 400, "bad_request", "需要 name 与 email")
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		fail(w, 400, "bad_request", "邮箱格式不对")
		return
	}
	t, key, err := s.P.CreateTenantFull(strings.TrimSpace(req.Name), strings.TrimSpace(req.Email), set.SignupPlan, "signup")
	if err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	s.P.Audit("tenant.signup", t.ID, ip, map[string]any{"email": t.Email})
	writeJSON(w, 201, map[string]any{"tenantId": t.ID, "plan": t.Plan, "key": key,
		"note": "接口密钥只显示这一次，请妥善保存"})
}

func readAllLimit(w http.ResponseWriter, r *http.Request, n int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, n))
}

// adminSystem：登录方式、安全与备份的配置状态(只读，来自环境变量)。
func (s *Server) adminSystem(w http.ResponseWriter, r *http.Request) {
	c := s.Auth
	_, localKey := os.Stat(filepath.Join(s.P.Root, "local.key"))
	backups := []string{}
	dir := os.Getenv("MAPSIM_BACKUP_DIR")
	if dir == "" {
		dir = filepath.Join(s.P.Root, "backups")
	}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if e.IsDir() && len(e.Name()) == 15 {
				backups = append(backups, e.Name())
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"publicURL": c.PublicURL, "emailLogin": c.emailEnabled(), "smtpHost": c.SMTPHost, "googleLogin": c.googleEnabled(), "authDev": c.Dev,
		"cfAccess": s.CFAccess != nil, "trustCF": trustCF, "localKeyFile": localKey == nil, "localTenantOff": os.Getenv("MAPSIM_LOCAL_TENANT") == "off",
		"backupDir": dir, "backupOff": os.Getenv("MAPSIM_BACKUP") == "off", "backups": backups,
		"operator": c.Operator, "contact": c.Contact,
	})
}
