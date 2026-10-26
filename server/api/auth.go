package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mapsim/platform"
)

// ---------------- 配置(环境变量) ----------------
//   MAPSIM_PUBLIC_URL            对外地址，如 https://aeonmap.novakitestudio.com(邮件链接、Google 回调、Cookie Secure)
//   MAPSIM_SMTP_HOST/PORT/USER/PASS/FROM   发信(465 端口用 TLS，其余用 STARTTLS)
//   MAPSIM_GOOGLE_CLIENT_ID / MAPSIM_GOOGLE_CLIENT_SECRET   Google 登录
//   MAPSIM_AUTH_DEV=1            没有配置 SMTP 时，把登录链接写进服务日志并在响应里返回(仅本地开发)
//   MAPSIM_SITE_NAME / MAPSIM_SITE_OPERATOR / MAPSIM_CONTACT_EMAIL   首页、条款与隐私政策里显示的运营方信息

type AuthConfig struct {
	PublicURL    string
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPFrom     string
	GoogleID     string
	GoogleSecret string
	Dev          bool
	SiteName     string
	Operator     string
	Contact      string
}

func AuthConfigFromEnv() AuthConfig {
	port, _ := strconv.Atoi(os.Getenv("MAPSIM_SMTP_PORT"))
	if port == 0 {
		port = 587
	}
	c := AuthConfig{
		PublicURL: strings.TrimRight(os.Getenv("MAPSIM_PUBLIC_URL"), "/"),
		SMTPHost:  os.Getenv("MAPSIM_SMTP_HOST"), SMTPPort: port,
		SMTPUser: os.Getenv("MAPSIM_SMTP_USER"), SMTPPass: os.Getenv("MAPSIM_SMTP_PASS"), SMTPFrom: os.Getenv("MAPSIM_SMTP_FROM"),
		GoogleID: os.Getenv("MAPSIM_GOOGLE_CLIENT_ID"), GoogleSecret: os.Getenv("MAPSIM_GOOGLE_CLIENT_SECRET"),
		Dev:      os.Getenv("MAPSIM_AUTH_DEV") == "1",
		SiteName: os.Getenv("MAPSIM_SITE_NAME"), Operator: os.Getenv("MAPSIM_SITE_OPERATOR"), Contact: os.Getenv("MAPSIM_CONTACT_EMAIL"),
	}
	if c.SiteName == "" {
		c.SiteName = "AeonMap"
	}
	if c.SMTPFrom == "" {
		c.SMTPFrom = c.SMTPUser
	}
	return c
}

func (c AuthConfig) emailEnabled() bool  { return c.SMTPHost != "" && c.SMTPFrom != "" }
func (c AuthConfig) googleEnabled() bool { return c.GoogleID != "" && c.GoogleSecret != "" }

// baseURL：配置了 MAPSIM_PUBLIC_URL 用它；否则按请求推断(本地开发)。
func (s *Server) baseURL(r *http.Request) string {
	if s.Auth.PublicURL != "" {
		return s.Auth.PublicURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// ---------------- 认证中间件 ----------------

const sessionCookie = "aeon_session"

// GuestTenant：未登录的访客，只能读取公共世界与公开示例演化。
var guestTenant = &platform.Tenant{ID: "guest", Name: "访客", Plan: "free"}

func isGuest(t *platform.Tenant) bool { return t == nil || t.ID == guestTenant.ID }

// identify：接口密钥优先，其次是会话 Cookie。via = "key" | "session" | ""。
func (s *Server) identify(r *http.Request) (*platform.Tenant, string) {
	if k := apiKey(r); k != "" {
		if t, ok := s.P.Authenticate(k); ok {
			return t, "key"
		}
		return nil, ""
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if t, ok := s.P.SessionTenant(c.Value); ok {
			return t, "session"
		}
	}
	return nil, ""
}

// sameOrigin：Cookie 认证的写操作必须来自本站页面(防 CSRF)。
func sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// auth：需要登录(接口密钥或会话)。
func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, via := s.identify(r)
		if t == nil {
			fail(w, 401, "unauthorized", "缺少或无效的接口密钥(Authorization: Bearer msk_...)")
			return
		}
		if via == "session" && r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			fail(w, 403, "forbidden", "跨站请求被拒绝")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, t)))
	}
}

// optAuth：只读接口，未登录时以访客身份访问(只能看到公共内容)。
func (s *Server) optAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, _ := s.identify(r)
		if t == nil {
			if apiKey(r) != "" { // 带了错误的密钥：明确报错，而不是悄悄当成访客
				fail(w, 401, "unauthorized", "缺少或无效的接口密钥(Authorization: Bearer msk_...)")
				return
			}
			t = guestTenant
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, t)))
	}
}

// ---------------- 简单限流 ----------------

var limiter = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

// allow：key 在 per 时间内最多 n 次。
func allow(key string, n int, per time.Duration) bool {
	limiter.Lock()
	defer limiter.Unlock()
	var recent []time.Time
	for _, t := range limiter.m[key] {
		if time.Since(t) < per {
			recent = append(recent, t)
		}
	}
	if len(recent) >= n {
		limiter.m[key] = recent
		return false
	}
	limiter.m[key] = append(recent, time.Now())
	return true
}

// ---------------- 路由 ----------------

func (s *Server) authRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/site", s.handleSite)
	mux.HandleFunc("POST /v1/auth/email", s.handleEmailLogin)
	mux.HandleFunc("GET /v1/auth/email/callback", s.handleEmailCallback)
	mux.HandleFunc("GET /v1/auth/google", s.handleGoogleStart)
	mux.HandleFunc("GET /v1/auth/google/callback", s.handleGoogleCallback)
	mux.HandleFunc("POST /v1/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /v1/auth/anonymous", s.handleAnonymous)
	mux.HandleFunc("POST /v1/keys", s.auth(s.handleNewKey))
	mux.HandleFunc("DELETE /v1/keys/{prefix}", s.auth(s.handleRevokeKey))
	mux.HandleFunc("DELETE /v1/me", s.auth(s.handleDeleteMe))
}

// handleSite：首页、价格页、登录页需要的公开信息。
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	set := s.P.Settings()
	type planView struct {
		Name          string  `json:"name"`
		Label         string  `json:"label"`
		PriceMonthly  float64 `json:"priceMonthly"`
		MonthlyUnits  int     `json:"monthlyUnits"`
		MaxYears      int     `json:"maxYears"`
		MaxFamilies   int     `json:"maxFamilies"`
		MaxUploadMB   int     `json:"maxUploadMB"`
		MaxActiveJobs int     `json:"maxActiveJobs"`
		NoSimulate    bool    `json:"noSimulate"`
	}
	var plans []planView
	for _, p := range set.Plans {
		if p.Name == "internal" {
			continue
		}
		plans = append(plans, planView{p.Name, p.Label, p.PriceMonthly, p.MonthlyUnits, p.MaxYears, p.MaxFamilies, p.MaxUploadMB, p.ActiveLimit(), p.NoSimulate})
	}
	writeJSON(w, 200, map[string]any{
		"name": s.Auth.SiteName, "operator": s.Auth.Operator, "contact": s.Auth.Contact,
		"currency": set.Currency, "plans": plans, "signupPlan": set.SignupPlan, "featuredRuns": set.FeaturedRuns, "pricing": set.Pricing,
		"auth": map[string]bool{"email": s.Auth.emailEnabled() || s.Auth.Dev, "google": s.Auth.googleEnabled(), "signup": set.AllowSignup,
			"dev": s.Auth.Dev && !s.Auth.emailEnabled(), "anonymous": set.AllowAnonymous},
		"anonPlan": set.AnonPlanName(),
	})
}

// safeNext：登录后跳转的地址只允许本站相对路径。
func safeNext(n string) string {
	if n == "" || !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.Contains(n, "\\") {
		return "/console.html"
	}
	return n
}

// ---- 邮箱魔法链接 ----

type magicToken struct {
	email, next, lang string
	exp               time.Time
}

var magic = struct {
	sync.Mutex
	m map[string]magicToken // 令牌哈希 → 内容(只在内存：服务重启后旧链接失效，用户重新获取即可)
}{m: map[string]magicToken{}}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (s *Server) handleEmailLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "forbidden", "跨站请求被拒绝")
		return
	}
	var req struct {
		Email string `json:"email"`
		Next  string `json:"next"`
		Lang  string `json:"lang"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req) != nil {
		fail(w, 400, "bad_request", "请求体不是合法 JSON")
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(req.Email))
	if err != nil || len(addr.Address) > 254 {
		fail(w, 400, "bad_request", "邮箱格式不对")
		return
	}
	email := strings.ToLower(addr.Address)
	if !s.Auth.emailEnabled() && !s.Auth.Dev {
		fail(w, 503, "email_disabled", "邮箱登录尚未配置")
		return
	}
	ip := clientIP(r)
	if !allow("mail-ip:"+ip, 10, time.Hour) || !allow("mail-to:"+email, 5, time.Hour) {
		fail(w, 429, "rate_limited", "发送过于频繁，请稍后再试")
		return
	}
	if _, ok := s.P.TenantByEmail(email); !ok && !s.P.Settings().AllowSignup {
		fail(w, 403, "signup_closed", "未开放自助注册，请联系管理员开通")
		return
	}
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	magic.Lock()
	for k, v := range magic.m {
		if time.Now().After(v.exp) {
			delete(magic.m, k)
		}
	}
	magic.m[sha(tok)] = magicToken{email: email, next: safeNext(req.Next), lang: req.Lang, exp: time.Now().Add(15 * time.Minute)}
	magic.Unlock()
	link := s.baseURL(r) + "/v1/auth/email/callback?token=" + tok
	if !s.Auth.emailEnabled() { // 开发模式
		log.Printf("[开发模式] %s 的登录链接：%s", email, link)
		writeJSON(w, 200, map[string]any{"sent": true, "devLink": link})
		return
	}
	if err := s.sendLoginMail(email, link, req.Lang); err != nil {
		log.Printf("发送登录邮件失败(%s): %v", email, err)
		fail(w, 502, "mail_failed", "邮件发送失败，请稍后再试")
		return
	}
	writeJSON(w, 200, map[string]any{"sent": true})
}

func (s *Server) handleEmailCallback(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	magic.Lock()
	mt, ok := magic.m[sha(tok)]
	delete(magic.m, sha(tok)) // 一次性
	magic.Unlock()
	if !ok || time.Now().After(mt.exp) {
		http.Redirect(w, r, "/login.html?error=link_expired", http.StatusSeeOther)
		return
	}
	t, err := s.loginTenant(r, mt.email, "email")
	if err != nil {
		http.Redirect(w, r, "/login.html?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.startSession(w, r, t, "email", mt.next)
}

// loginTenant：按邮箱找到租户；没有则在允许自助注册时新建(注册套餐)。
// 当前浏览器是免登录试用账户、且该邮箱还没有账户时，试用账户直接转正，试用期间的作品保留。
func (s *Server) loginTenant(r *http.Request, email, via string) (*platform.Tenant, error) {
	if t, ok := s.P.TenantByEmail(email); ok {
		if t.Suspended {
			return nil, fmt.Errorf("account_suspended")
		}
		return t, nil
	}
	set := s.P.Settings()
	if !set.AllowSignup {
		return nil, fmt.Errorf("signup_closed")
	}
	if cur, kind := s.identify(r); kind == "session" && cur.IsAnonymous() {
		if t, err := s.P.ClaimAnonymous(cur.ID, email, via); err == nil {
			s.P.Audit("tenant.claim", t.ID, clientIP(r), map[string]any{"via": via, "email": email})
			return t, nil
		}
	}
	name := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		name = email[:i]
	}
	t, err := s.P.CreateTenantNoKey(name, email, set.SignupPlan, via)
	if err != nil {
		return nil, err
	}
	s.P.Audit("tenant.signup", t.ID, "", map[string]any{"via": via, "email": email})
	return t, nil
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, t *platform.Tenant, via, next string) {
	if err := s.setSession(w, r, t, via); err != nil {
		http.Redirect(w, r, "/login.html?error=internal", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, t *platform.Tenant, via string) error {
	tok, exp, err := s.P.CreateSession(t.ID, via, clientIP(r))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: tok, Path: "/", Expires: exp, HttpOnly: true,
		Secure: strings.HasPrefix(s.baseURL(r), "https://"), SameSite: http.SameSiteLaxMode})
	return nil
}

// handleAnonymous：开一个免登录试用账户并写入会话 Cookie。已登录时直接返回当前身份。
func (s *Server) handleAnonymous(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "forbidden", "跨站请求被拒绝")
		return
	}
	if t, _ := s.identify(r); t != nil {
		writeJSON(w, 200, map[string]any{"id": t.ID, "anonymous": t.IsAnonymous()})
		return
	}
	set := s.P.Settings()
	if !set.AllowAnonymous {
		fail(w, 403, "anonymous_disabled", "暂未开放免登录试用，请先登录")
		return
	}
	if !allow("anon-ip:"+clientIP(r), set.AnonIPLimit(), 24*time.Hour) {
		fail(w, 429, "rate_limited", "今天的试用次数已达上限，请登录后继续")
		return
	}
	t, err := s.P.CreateAnonymousTenant()
	if err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	if err := s.setSession(w, r, t, "anonymous"); err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	s.P.Audit("tenant.anonymous", t.ID, clientIP(r), nil)
	writeJSON(w, 201, map[string]any{"id": t.ID, "anonymous": true})
}

// StartMaintenance：后台定期清理过期未转正的试用账户(连同其世界与演化)。
func (s *Server) StartMaintenance(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			days := s.P.Settings().AnonMaxAge()
			for _, id := range s.P.AnonymousToPrune(time.Duration(days) * 24 * time.Hour) {
				runs := 0
				for _, m := range s.E.ListRuns("", "") {
					if m.Tenant == id {
						s.E.DeleteRun(m.ID)
						runs++
					}
				}
				if s.P.DeleteTenant(id) == nil {
					log.Printf("已清理过期试用账户 %s(%d 个演化)", id, runs)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "forbidden", "跨站请求被拒绝")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.P.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- 发信 ----

func (s *Server) sendLoginMail(to, link, lang string) error {
	c := s.Auth
	subj, body := "登录 "+c.SiteName, fmt.Sprintf("点击下面的链接登录 %s(15 分钟内有效，只能使用一次)：\r\n\r\n%s\r\n\r\n如果不是你本人操作，请忽略这封邮件。\r\n", c.SiteName, link)
	if lang != "zh" {
		subj = "Sign in to " + c.SiteName
		body = fmt.Sprintf("Click the link below to sign in to %s (valid for 15 minutes, single use):\r\n\r\n%s\r\n\r\nIf you did not request this, you can ignore this email.\r\n", c.SiteName, link)
	}
	from, err := mail.ParseAddress(c.SMTPFrom)
	if err != nil {
		return fmt.Errorf("MAPSIM_SMTP_FROM 无效: %w", err)
	}
	msg := "From: " + (&mail.Address{Name: c.SiteName, Address: from.Address}).String() + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + mimeHeader(subj) + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		wrap76(base64.StdEncoding.EncodeToString([]byte(body)))
	addr := net.JoinHostPort(c.SMTPHost, strconv.Itoa(c.SMTPPort))
	var cl *smtp.Client
	if c.SMTPPort == 465 {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 15 * time.Second}, "tcp", addr, &tls.Config{ServerName: c.SMTPHost})
		if err != nil {
			return err
		}
		if cl, err = smtp.NewClient(conn, c.SMTPHost); err != nil {
			return err
		}
	} else {
		conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
		if err != nil {
			return err
		}
		if cl, err = smtp.NewClient(conn, c.SMTPHost); err != nil {
			return err
		}
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(&tls.Config{ServerName: c.SMTPHost}); err != nil {
				return err
			}
		}
	}
	defer cl.Close()
	if c.SMTPUser != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.SMTPUser, c.SMTPPass, c.SMTPHost)); err != nil {
			return err
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return err
	}
	if err := cl.Rcpt(to); err != nil {
		return err
	}
	wc, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(wc, msg); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

func mimeHeader(s string) string {
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	b.WriteString(s + "\r\n")
	return b.String()
}

// ---- Google 登录(OAuth 2.0 授权码流程) ----

const oauthCookie = "aeon_oauth"

func (s *Server) googleRedirectURI(r *http.Request) string {
	return s.baseURL(r) + "/v1/auth/google/callback"
}

func (s *Server) handleGoogleStart(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.googleEnabled() {
		http.Redirect(w, r, "/login.html?error=google_disabled", http.StatusSeeOther)
		return
	}
	b := make([]byte, 24)
	rand.Read(b)
	state := hex.EncodeToString(b)
	// state 与登录后跳转地址放在短期 Cookie 里，回调时核对(防 CSRF)
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: state + "|" + url.QueryEscape(safeNext(r.URL.Query().Get("next"))),
		Path: "/v1/auth/google", MaxAge: 600, HttpOnly: true, Secure: strings.HasPrefix(s.baseURL(r), "https://"), SameSite: http.SameSiteLaxMode})
	q := url.Values{"client_id": {s.Auth.GoogleID}, "redirect_uri": {s.googleRedirectURI(r)}, "response_type": {"code"},
		"scope": {"openid email profile"}, "state": {state}, "prompt": {"select_account"}}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+q.Encode(), http.StatusSeeOther)
}

func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(oauthCookie)
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Value: "", Path: "/v1/auth/google", MaxAge: -1})
	if err != nil {
		http.Redirect(w, r, "/login.html?error=google_state", http.StatusSeeOther)
		return
	}
	state, nextEnc, _ := strings.Cut(c.Value, "|")
	if state == "" || r.URL.Query().Get("state") != state {
		http.Redirect(w, r, "/login.html?error=google_state", http.StatusSeeOther)
		return
	}
	next, _ := url.QueryUnescape(nextEnc)
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/login.html?error=google_denied", http.StatusSeeOther)
		return
	}
	email, err := s.googleEmail(r.Context(), code, s.googleRedirectURI(r))
	if err != nil {
		log.Printf("Google 登录失败: %v", err)
		http.Redirect(w, r, "/login.html?error=google_failed", http.StatusSeeOther)
		return
	}
	t, err := s.loginTenant(r, email, "google")
	if err != nil {
		http.Redirect(w, r, "/login.html?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.startSession(w, r, t, "google", next)
}

// googleEmail：用授权码换取 id_token，取出已验证的邮箱。
// id_token 是本服务直接通过 TLS 从 Google 令牌端点取得的(带客户端密钥)，按 OpenID Connect 规范可不再校验签名。
func (s *Server) googleEmail(ctx context.Context, code, redirect string) (string, error) {
	form := url.Values{"code": {code}, "client_id": {s.Auth.GoogleID}, "client_secret": {s.Auth.GoogleSecret},
		"redirect_uri": {redirect}, "grant_type": {"authorization_code"}}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tr struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil || tr.IDToken == "" {
		return "", fmt.Errorf("token 端点返回 %d %s", resp.StatusCode, tr.Error)
	}
	parts := strings.Split(tr.IDToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("id_token 格式不对")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Aud           string `json:"aud"`
		Iss           string `json:"iss"`
		Exp           int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	verified := claims.EmailVerified == true || claims.EmailVerified == "true"
	if claims.Aud != s.Auth.GoogleID || (claims.Iss != "https://accounts.google.com" && claims.Iss != "accounts.google.com") ||
		time.Now().Unix() > claims.Exp || !verified || claims.Email == "" {
		return "", fmt.Errorf("id_token 校验失败(aud/iss/exp/email_verified)")
	}
	return strings.ToLower(claims.Email), nil
}

// ---- 用户自己管理接口密钥 ----

func (s *Server) handleNewKey(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	if t.IsAnonymous() {
		fail(w, 403, "login_required", "试用账户不能创建接口密钥，请先登录")
		return
	}
	if len(t.Keys) >= 20 {
		fail(w, 400, "bad_request", "密钥数量已达上限(20)，请先吊销不用的密钥")
		return
	}
	key, err := s.P.AddKey(t.ID)
	if err != nil {
		fail(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"key": key, "prefix": key[:10], "message": "接口密钥只显示这一次，请妥善保存"})
}

func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	if err := s.P.RevokeKey(t.ID, r.PathValue("prefix")); err != nil {
		fail(w, 404, "not_found", "密钥不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": r.PathValue("prefix")})
}

// handleDeleteMe：用户删除自己的账户(连同世界、演化与任务)。请求体 {"confirm": 邮箱或账户 ID}。
func (s *Server) handleDeleteMe(w http.ResponseWriter, r *http.Request) {
	t := tenantOf(r)
	var req struct {
		Confirm string `json:"confirm"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req)
	c := strings.TrimSpace(req.Confirm)
	if c == "" || !(strings.EqualFold(c, t.Email) || c == t.ID) {
		fail(w, 400, "bad_request", "请输入账户邮箱(或账户 ID)确认删除")
		return
	}
	if t.ID == platform.PublicTenant {
		fail(w, 400, "bad_request", "不能删除公共租户")
		return
	}
	if s.P.ActiveJobs(t.ID) > 0 {
		fail(w, 409, "conflict", "还有进行中的任务，请先取消")
		return
	}
	runs := 0
	for _, m := range s.E.ListRuns("", "") {
		if m.Tenant == t.ID {
			s.E.DeleteRun(m.ID)
			runs++
		}
	}
	if err := s.P.DeleteTenant(t.ID); err != nil {
		fail(w, 400, "bad_request", err.Error())
		return
	}
	s.P.Audit("tenant.self_delete", t.ID, clientIP(r), map[string]any{"runs": runs, "email": t.Email})
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, 200, map[string]any{"deleted": t.ID, "runs": runs})
}
