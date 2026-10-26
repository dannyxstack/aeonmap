package platform

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 网页登录会话(邮箱魔法链接 / Google)。浏览器持有随机会话令牌(Cookie)，这里只存它的哈希。
// 接口密钥仍用于程序调用；两者都能认证到同一个租户。

const SessionTTL = 30 * 24 * time.Hour

type Session struct {
	Tenant  string `json:"tenant"`
	Created string `json:"created"`
	Expires string `json:"expires"`
	Via     string `json:"via"` // email | google
	IP      string `json:"ip,omitempty"`
}

func (s *Store) sessionsPath() string { return filepath.Join(s.Root, "sessions.json") }

func (s *Store) loadSessions() {
	s.sess = map[string]*Session{}
	b, err := os.ReadFile(s.sessionsPath())
	if err != nil {
		return
	}
	json.Unmarshal(b, &s.sess)
}

func (s *Store) saveSessionsLocked() error {
	now := time.Now().UTC().Format(time.RFC3339)
	for k, v := range s.sess { // 顺便清理过期会话
		if v.Expires < now {
			delete(s.sess, k)
		}
	}
	b, _ := json.Marshal(s.sess)
	tmp := s.sessionsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.sessionsPath())
}

// CreateSession 为租户新建会话，返回给浏览器的令牌(只出现这一次)。
func (s *Store) CreateSession(tenantID, via, ip string) (string, time.Time, error) {
	b := make([]byte, 32)
	rand.Read(b)
	tok := "ses_" + hex.EncodeToString(b)
	exp := time.Now().UTC().Add(SessionTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess[hashKey(tok)] = &Session{Tenant: tenantID, Created: now(), Expires: exp.Format(time.RFC3339), Via: via, IP: ip}
	return tok, exp, s.saveSessionsLocked()
}

// SessionTenant 用会话令牌找到租户；过期、租户停用或已删除时失败。
func (s *Store) SessionTenant(tok string) (*Tenant, bool) {
	if !strings.HasPrefix(tok, "ses_") {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadIfChanged()
	ss, ok := s.sess[hashKey(tok)]
	if !ok || ss.Expires < time.Now().UTC().Format(time.RFC3339) {
		return nil, false
	}
	t, ok := s.ten[ss.Tenant]
	if !ok || t.Suspended {
		return nil, false
	}
	s.touch(t.ID)
	c := *t
	return &c, true
}

func (s *Store) DeleteSession(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sess[hashKey(tok)]; ok {
		delete(s.sess, hashKey(tok))
		s.saveSessionsLocked()
	}
}

// deleteTenantSessionsLocked：删除或停用租户时一并注销其会话。
func (s *Store) deleteTenantSessionsLocked(tenantID string) {
	n := 0
	for k, v := range s.sess {
		if v.Tenant == tenantID {
			delete(s.sess, k)
			n++
		}
	}
	if n > 0 {
		s.saveSessionsLocked()
	}
}

// TenantByEmail 按邮箱(不区分大小写)查找租户。
func (s *Store) TenantByEmail(email string) (*Tenant, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.ten {
		if t.Email != "" && strings.EqualFold(t.Email, email) {
			c := *t
			return &c, true
		}
	}
	return nil, false
}

// ActiveJobs：租户排队中与运行中的任务数(用于每用户并发限制)。
func (s *Store) ActiveJobs(tenant string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, j := range s.jobs {
		if j.Tenant == tenant && (j.Status == "queued" || j.Status == "running") {
			n++
		}
	}
	return n
}
