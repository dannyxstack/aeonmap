package platform

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// 免登录试用：未登录的访客第一次需要计算(上传、演化、识图)时，自动建一个匿名试用账户(Source=anonymous)，
// 以会话 Cookie 绑定到浏览器。额度按"试用套餐"限制，另有全站每日总预算与每 IP 每日开户数上限。
// 之后用邮箱/Google 登录时，若该邮箱还没有账户，试用账户直接转正(作品保留)。

const AnonSource = "anonymous"

func (t *Tenant) IsAnonymous() bool { return t != nil && t.Source == AnonSource }

// AnonPlanName：试用套餐；未设置或已被删除时用自助注册套餐。
func (st Settings) AnonPlanName() string {
	if _, ok := st.Plans[st.AnonPlan]; ok && st.AnonPlan != "" {
		return st.AnonPlan
	}
	return st.SignupPlan
}

// CreateAnonymousTenant 新建匿名试用账户(没有邮箱、没有接口密钥)。
func (s *Store) CreateAnonymousTenant() (*Tenant, error) {
	b := make([]byte, 3)
	rand.Read(b)
	set := s.Settings()
	return s.CreateTenantNoKey("试用-"+hex.EncodeToString(b), "", set.AnonPlanName(), AnonSource)
}

// ClaimAnonymous：试用账户绑定邮箱转正，套餐换成自助注册套餐(若仍是试用套餐)。
func (s *Store) ClaimAnonymous(tenantID, email, via string) (*Tenant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.ten[tenantID]
	if !ok || t.Source != AnonSource {
		return nil, ErrNotFound
	}
	for _, o := range s.ten {
		if strings.EqualFold(o.Email, email) {
			return nil, fmt.Errorf("邮箱 %s 已注册", email)
		}
	}
	t.Email, t.Source = email, via
	if i := strings.IndexByte(email, '@'); i > 0 {
		t.Name = email[:i]
	}
	if t.Plan == s.set.AnonPlanName() {
		t.Plan = s.set.SignupPlan
	}
	c := *t
	return &c, s.saveTenantsLocked()
}

// anonUsedToday：今天(UTC)所有试用账户已用与进行中预计的算力点合计。
func (s *Store) anonUsedToday() int {
	day := time.Now().UTC().Format("2006-01-02")
	s.mu.Lock()
	anon := map[string]bool{}
	for id, t := range s.ten {
		if t.Source == AnonSource {
			anon[id] = true
		}
	}
	pending := 0
	for _, j := range s.jobs {
		if anon[j.Tenant] && (j.Status == "queued" || j.Status == "running") {
			pending += j.Estimate - j.Units
		}
	}
	s.mu.Unlock()
	used := 0
	for _, u := range s.UsageRows(1) {
		if anon[u.Tenant] && strings.HasPrefix(u.Time, day) {
			used += u.Units
		}
	}
	return used + pending
}

// AnonUsedToday：供管理后台显示。
func (s *Store) AnonUsedToday() int { return s.anonUsedToday() }

// checkAnonBudget：试用账户还要受全站每日总预算限制。
func (s *Store) checkAnonBudget(t *Tenant, units int) error {
	if !t.IsAnonymous() {
		return nil
	}
	b := s.Settings().AnonDailyBudget
	if b <= 0 {
		return nil
	}
	if used := s.anonUsedToday(); used+units > b {
		return fmt.Errorf("今天的免登录试用额度已用完，登录后可继续使用")
	}
	return nil
}

// AnonymousToPrune：创建超过 maxAge 仍未转正、且没有进行中任务的试用账户。
func (s *Store) AnonymousToPrune(maxAge time.Duration) []string {
	cut := time.Now().UTC().Add(-maxAge).Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id, t := range s.ten {
		if t.Source != AnonSource || t.Created > cut {
			continue
		}
		busy := false
		for _, j := range s.jobs {
			if j.Tenant == id && (j.Status == "queued" || j.Status == "running") {
				busy = true
			}
		}
		if !busy {
			out = append(out, id)
		}
	}
	return out
}
