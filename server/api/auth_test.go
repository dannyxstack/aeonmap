package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"mapsim/platform"
)

func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	p, err := platform.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := p.Settings()
	st.AllowSignup = true
	if err := p.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(dir + "/runs")
	r := &Runner{P: p, E: e}
	s := NewServer(e, r, p, fstest.MapFS{"home.html": {Data: []byte("home")}}, nil)
	s.Auth = AuthConfig{Dev: true, SiteName: "AeonMap"}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// 登录：开发模式下返回链接 → 打开链接得到会话 Cookie → 用 Cookie 访问 /v1/me
func login(t *testing.T, ts *httptest.Server, email string) *http.Cookie {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/v1/auth/email", strings.NewReader(`{"email":"`+email+`","next":"/console.html"}`))
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("email login: %v %v", err, resp.Status)
	}
	var out struct{ DevLink string }
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	u, _ := url.Parse(out.DevLink)
	cb, err := noRedirect().Get(ts.URL + u.RequestURI())
	if err != nil || cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/console.html" {
		t.Fatalf("callback: %v %v %s", err, cb.Status, cb.Header.Get("Location"))
	}
	for _, c := range cb.Cookies() {
		if c.Name == sessionCookie && c.HttpOnly {
			// 同一链接不能再用
			again, _ := noRedirect().Get(ts.URL + u.RequestURI())
			if !strings.Contains(again.Header.Get("Location"), "link_expired") {
				t.Fatalf("magic link reused: %s", again.Header.Get("Location"))
			}
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func do(t *testing.T, method, u string, body string, hdr map[string]string, c *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestMagicLinkSessionAndGuest(t *testing.T) {
	s, ts := testServer(t)
	c := login(t, ts, "Alice@Example.com")
	me := do(t, "GET", ts.URL+"/v1/me", "", nil, c)
	var m map[string]any
	json.NewDecoder(me.Body).Decode(&m)
	if me.StatusCode != 200 || m["email"] != "alice@example.com" || m["guest"] == true {
		t.Fatalf("me: %d %v", me.StatusCode, m)
	}
	// 同一邮箱再次登录得到同一个租户
	c2 := login(t, ts, "alice@example.com")
	me2 := do(t, "GET", ts.URL+"/v1/me", "", nil, c2)
	var m2 map[string]any
	json.NewDecoder(me2.Body).Decode(&m2)
	if m2["id"] != m["id"] {
		t.Fatalf("second login made a new tenant: %v vs %v", m2["id"], m["id"])
	}

	// 访客：可读公共内容，不能写
	g := do(t, "GET", ts.URL+"/v1/me", "", nil, nil)
	var gm map[string]any
	json.NewDecoder(g.Body).Decode(&gm)
	if gm["guest"] != true {
		t.Fatalf("guest me: %v", gm)
	}
	if r := do(t, "GET", ts.URL+"/v1/worlds", "", nil, nil); r.StatusCode != 200 {
		t.Fatalf("guest worlds: %d", r.StatusCode)
	}
	if r := do(t, "POST", ts.URL+"/v1/worlds/eurasia/jobs", "{}", nil, nil); r.StatusCode != 401 {
		t.Fatalf("guest submit should be 401, got %d", r.StatusCode)
	}
	if r := do(t, "GET", ts.URL+"/v1/jobs", "", nil, nil); r.StatusCode != 401 {
		t.Fatalf("guest jobs should be 401, got %d", r.StatusCode)
	}
	// 错误的密钥不能降级为访客
	if r := do(t, "GET", ts.URL+"/v1/worlds", "", map[string]string{"Authorization": "Bearer msk_bad"}, nil); r.StatusCode != 401 {
		t.Fatalf("bad key: %d", r.StatusCode)
	}

	// CSRF：Cookie 认证的写操作来自其他站点 → 403；同站 → 通过认证
	if r := do(t, "POST", ts.URL+"/v1/keys", "", map[string]string{"Origin": "https://evil.example"}, c); r.StatusCode != 403 {
		t.Fatalf("cross-site POST should be 403, got %d", r.StatusCode)
	}
	r := do(t, "POST", ts.URL+"/v1/keys", "", map[string]string{"Origin": ts.URL}, c)
	var k struct{ Key, Prefix string }
	json.NewDecoder(r.Body).Decode(&k)
	if r.StatusCode != 201 || !strings.HasPrefix(k.Key, "msk_") {
		t.Fatalf("new key: %d %v", r.StatusCode, k)
	}
	if r := do(t, "GET", ts.URL+"/v1/me", "", map[string]string{"Authorization": "Bearer " + k.Key}, nil); r.StatusCode != 200 {
		t.Fatalf("new key unusable: %d", r.StatusCode)
	}
	if r := do(t, "DELETE", ts.URL+"/v1/keys/"+k.Prefix, "", map[string]string{"Origin": ts.URL}, c); r.StatusCode != 200 {
		t.Fatalf("revoke: %d", r.StatusCode)
	}

	// 停用租户后会话失效
	tn, _ := s.P.TenantByEmail("alice@example.com")
	susp := true
	s.P.UpdateTenant(tn.ID, platform.TenantPatch{Suspended: &susp})
	if r := do(t, "GET", ts.URL+"/v1/jobs", "", nil, c2); r.StatusCode != 401 {
		t.Fatalf("suspended session still works: %d", r.StatusCode)
	}

	// 自己删除账户：确认信息不对 → 400；正确 → 删除，会话随之失效
	c4 := login(t, ts, "carol@example.com")
	if r := do(t, "DELETE", ts.URL+"/v1/me", `{"confirm":"x@y.z"}`, map[string]string{"Origin": ts.URL}, c4); r.StatusCode != 400 {
		t.Fatalf("delete with wrong confirm: %d", r.StatusCode)
	}
	if r := do(t, "DELETE", ts.URL+"/v1/me", `{"confirm":"Carol@example.com"}`, map[string]string{"Origin": ts.URL}, c4); r.StatusCode != 200 {
		t.Fatalf("delete me: %d", r.StatusCode)
	}
	if _, ok := s.P.TenantByEmail("carol@example.com"); ok {
		t.Fatal("tenant still exists")
	}
	if r := do(t, "GET", ts.URL+"/v1/jobs", "", nil, c4); r.StatusCode != 401 {
		t.Fatalf("session after delete: %d", r.StatusCode)
	}

	// 登出
	c3 := login(t, ts, "bob@example.com")
	if r := do(t, "POST", ts.URL+"/v1/auth/logout", "", map[string]string{"Origin": ts.URL}, c3); r.StatusCode != 200 {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if r := do(t, "GET", ts.URL+"/v1/jobs", "", nil, c3); r.StatusCode != 401 {
		t.Fatalf("session after logout: %d", r.StatusCode)
	}
}

func TestSignupClosedAndSafeNext(t *testing.T) {
	s, ts := testServer(t)
	st := s.P.Settings()
	st.AllowSignup = false
	s.P.SetSettings(st)
	r := do(t, "POST", ts.URL+"/v1/auth/email", `{"email":"new@example.com"}`, map[string]string{"Origin": ts.URL}, nil)
	if r.StatusCode != 403 {
		t.Fatalf("signup closed should be 403, got %d", r.StatusCode)
	}
	for in, want := range map[string]string{"/index.html?run=x": "/index.html?run=x", "//evil.com": "/console.html",
		"https://evil.com": "/console.html", "/\\evil.com": "/console.html", "": "/console.html"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCFAccessVerify(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	c := &CFAccess{Team: "team", Aud: "aud123", keys: map[string]*rsa.PublicKey{"k1": &key.PublicKey}, fetched: time.Now()}
	sign := func(claims map[string]any, kid string) string {
		h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
		p, _ := json.Marshal(claims)
		in := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
		sum := sha256.Sum256([]byte(in))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		return in + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	ok := map[string]any{"aud": []string{"aud123"}, "iss": "https://team.cloudflareaccess.com", "exp": time.Now().Add(time.Hour).Unix(), "email": "a@b.c"}
	if e, err := c.Verify(context.Background(), sign(ok, "k1")); err != nil || e != "a@b.c" {
		t.Fatalf("valid token rejected: %v", err)
	}
	bad := []map[string]any{
		{"aud": "other", "iss": ok["iss"], "exp": ok["exp"]},
		{"aud": "aud123", "iss": "https://evil.cloudflareaccess.com", "exp": ok["exp"]},
		{"aud": "aud123", "iss": ok["iss"], "exp": time.Now().Add(-time.Minute).Unix()},
	}
	for i, cl := range bad {
		if _, err := c.Verify(context.Background(), sign(cl, "k1")); err == nil {
			t.Errorf("bad token %d accepted", i)
		}
	}
	tok := sign(ok, "k1")
	if _, err := c.Verify(context.Background(), tok[:len(tok)-4]+"AAAA"); err == nil {
		t.Error("tampered signature accepted")
	}
}

func TestRootAndLegacyReplayLinks(t *testing.T) {
	_, ts := testServer(t)
	r, _ := noRedirect().Get(ts.URL + "/?run=20261019-x&key=abc")
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/replay.html?run=20261019-x&key=abc" {
		t.Fatalf("legacy link: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	r, _ = noRedirect().Get(ts.URL + "/")
	if r.StatusCode != 200 {
		t.Fatalf("home: %d", r.StatusCode)
	}
}

func anonSession(t *testing.T, ts *httptest.Server) (*http.Cookie, int) {
	t.Helper()
	r := do(t, "POST", ts.URL+"/v1/auth/anonymous", "", map[string]string{"Origin": ts.URL}, nil)
	for _, c := range r.Cookies() {
		if c.Name == sessionCookie {
			return c, r.StatusCode
		}
	}
	return nil, r.StatusCode
}

func TestAnonymousTrial(t *testing.T) {
	s, ts := testServer(t)
	if _, code := anonSession(t, ts); code != 403 {
		t.Fatalf("anonymous disabled should be 403, got %d", code)
	}
	st := s.P.Settings()
	st.AllowAnonymous, st.AnonPerIPDaily, st.AnonDailyBudget = true, 2, 5
	if err := s.P.SetSettings(st); err != nil {
		t.Fatal(err)
	}
	c, code := anonSession(t, ts)
	if code != 201 || c == nil {
		t.Fatalf("anon session: %d", code)
	}
	var me map[string]any
	json.NewDecoder(do(t, "GET", ts.URL+"/v1/me", "", nil, c).Body).Decode(&me)
	if me["anonymous"] != true || me["plan"].(map[string]any)["name"] != "trial" {
		t.Fatalf("anon me: %v", me)
	}
	anonID := me["id"]
	if _, code := anonSession(t, ts); code != 201 {
		t.Fatalf("second anon: %d", code)
	}
	if _, code := anonSession(t, ts); code != 429 {
		t.Fatalf("per-IP limit should give 429, got %d", code)
	}
	// 全站每日预算：今天已用 + 本次 > 5 → 拒绝
	tn, _ := s.P.Tenant(anonID.(string))
	s.P.Charge(tn.ID, "", "simulate", 4)
	if err := s.P.CheckQuota(tn.ID, 2); err == nil || !strings.Contains(err.Error(), "试用额度") {
		t.Fatalf("daily budget not enforced: %v", err)
	}
	if err := s.P.CheckQuota(tn.ID, 1); err != nil {
		t.Fatalf("within budget rejected: %v", err)
	}
	// 试用账户用邮箱登录 → 转正，账户 ID 不变
	req, _ := http.NewRequest("POST", ts.URL+"/v1/auth/email", strings.NewReader(`{"email":"dora@example.com"}`))
	req.Header.Set("Origin", ts.URL)
	req.AddCookie(c)
	resp, _ := http.DefaultClient.Do(req)
	var out struct{ DevLink string }
	json.NewDecoder(resp.Body).Decode(&out)
	u, _ := url.Parse(out.DevLink)
	cbReq, _ := http.NewRequest("GET", ts.URL+u.RequestURI(), nil)
	cbReq.AddCookie(c)
	cb, _ := noRedirect().Do(cbReq)
	if cb.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback: %d", cb.StatusCode)
	}
	got, ok := s.P.TenantByEmail("dora@example.com")
	if !ok || got.ID != anonID || got.IsAnonymous() || got.Plan != st.SignupPlan {
		t.Fatalf("claim failed: %+v", got)
	}
	// 清理：转正的不清理，未转正的超龄后清理
	ids := s.P.AnonymousToPrune(0)
	for _, id := range ids {
		if id == anonID {
			t.Fatal("claimed tenant would be pruned")
		}
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 stale anonymous tenant, got %v", ids)
	}
}
