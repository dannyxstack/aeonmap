package platform

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Pricing：算力点计价系数。
type Pricing struct {
	PrepCellsPerUnit    float64 `json:"prepCellsPerUnit"`    // 预处理：每多少陆地格计 1 点
	SimCellYearsPerUnit float64 `json:"simCellYearsPerUnit"` // 演化：每多少"陆地格×年"计 1 点
	ExportUnits         int     `json:"exportUnits"`         // 每次导出
}

// Settings 是可由管理后台修改的全局配置(持久化在 data/settings.json)。
type Settings struct {
	Plans                map[string]Plan `json:"plans"`
	Pricing              Pricing         `json:"pricing"`
	Currency             string          `json:"currency"`
	AllowSignup          bool            `json:"allowSignup"` // 开放自助注册(POST /v1/signup)
	SignupPlan           string          `json:"signupPlan"`
	AllowPrivateWebhooks bool            `json:"allowPrivateWebhooks"`   // 允许回调到内网地址(仅本地开发)
	FeaturedRuns         []string        `json:"featuredRuns,omitempty"` // 公开示例演化：未登录的访客也能回放
	// 免登录试用：访客第一次计算时自动开匿名试用账户
	AllowAnonymous  bool   `json:"allowAnonymous"`
	AnonPlan        string `json:"anonPlan,omitempty"`        // 试用套餐(为空用自助注册套餐)
	AnonPerIPDaily  int    `json:"anonPerIPDaily,omitempty"`  // 每个 IP 每天最多开几个试用账户；0 = 默认 3
	AnonDailyBudget int    `json:"anonDailyBudget,omitempty"` // 全部试用账户每天合计可用的算力点；0 = 不限
	AnonMaxAgeDays  int    `json:"anonMaxAgeDays,omitempty"`  // 未转正的试用账户保留天数；0 = 默认 30
}

func (st Settings) AnonIPLimit() int {
	if st.AnonPerIPDaily > 0 {
		return st.AnonPerIPDaily
	}
	return 3
}

func (st Settings) AnonMaxAge() int {
	if st.AnonMaxAgeDays > 0 {
		return st.AnonMaxAgeDays
	}
	return 30
}

// IsFeatured：演化是否为公开示例。
func (st Settings) IsFeatured(runID string) bool {
	for _, r := range st.FeaturedRuns {
		if r == runID {
			return true
		}
	}
	return false
}

func DefaultSettings() Settings {
	return Settings{
		Plans: map[string]Plan{
			"free":     {Name: "free", Label: "免费版", MonthlyUnits: 100, MaxYears: 1000, MaxFamilies: 30000, MaxUploadMB: 20, MaxActive: 1},
			"pro":      {Name: "pro", Label: "专业版", MonthlyUnits: 2000, MaxYears: 10000, MaxFamilies: 100000, MaxUploadMB: 100, PriceMonthly: 99, MaxActive: 3},
			"studio":   {Name: "studio", Label: "工作室版", MonthlyUnits: 20000, MaxYears: 10000, MaxFamilies: 200000, MaxUploadMB: 500, PriceMonthly: 999, MaxActive: 5},
			"internal": {Name: "internal", Label: "内部", MonthlyUnits: -1, MaxYears: 100000, MaxFamilies: 200000, MaxUploadMB: 2000, MaxActive: 10},
			"trial":    {Name: "trial", Label: "免登录试用", MonthlyUnits: 30, MaxYears: 500, MaxFamilies: 20000, MaxUploadMB: 10, MaxActive: 1},
		},
		Pricing:    Pricing{PrepCellsPerUnit: 1e4, SimCellYearsPerUnit: 2e6, ExportUnits: 1},
		Currency:   "CNY",
		SignupPlan: "free",
		AnonPlan:   "trial", AnonPerIPDaily: 3, AnonDailyBudget: 1000,
	}
}

func (st *Settings) validate() error {
	if len(st.Plans) == 0 {
		return fmt.Errorf("至少需要一个套餐")
	}
	for k, p := range st.Plans {
		if k == "" || p.Name != k {
			return fmt.Errorf("套餐键与 name 必须一致：%q", k)
		}
		if p.MonthlyUnits < -1 || p.MaxYears < 1 || p.MaxFamilies < 1000 || p.MaxUploadMB < 1 || p.PriceMonthly < 0 || p.MaxActive < 0 {
			return fmt.Errorf("套餐 %s 的数值无效(额度 -1 表示不限；年数≥1；家族≥1000；上传≥1MB；价格≥0)", k)
		}
	}
	if _, ok := st.Plans["internal"]; !ok {
		return fmt.Errorf("不能删除 internal 套餐(公共与本地租户使用)")
	}
	if _, ok := st.Plans[st.AnonPlan]; st.AnonPlan != "" && !ok {
		return fmt.Errorf("试用套餐 %s 不存在", st.AnonPlan)
	}
	if st.AnonPerIPDaily < 0 || st.AnonDailyBudget < 0 || st.AnonMaxAgeDays < 0 {
		return fmt.Errorf("免登录试用的数值不能为负")
	}
	if _, ok := st.Plans[st.SignupPlan]; !ok {
		return fmt.Errorf("自助注册套餐 %s 不存在", st.SignupPlan)
	}
	if st.Pricing.PrepCellsPerUnit <= 0 || st.Pricing.SimCellYearsPerUnit <= 0 || st.Pricing.ExportUnits < 0 {
		return fmt.Errorf("计价系数必须为正")
	}
	return nil
}

func (s *Store) loadSettings() error {
	st := DefaultSettings()
	if b, err := os.ReadFile(filepath.Join(s.Root, "settings.json")); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return fmt.Errorf("settings.json: %w", err)
		}
	}
	s.set = st
	return nil
}

func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.set
	c.Plans = map[string]Plan{}
	for k, v := range s.set.Plans {
		c.Plans[k] = v
	}
	return c
}

func (s *Store) SetSettings(st Settings) error {
	if err := st.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.ten {
		if _, ok := st.Plans[t.Plan]; !ok {
			return fmt.Errorf("租户 %s 正在使用套餐 %s，不能删除", t.ID, t.Plan)
		}
	}
	s.set = st
	return writeJSON(filepath.Join(s.Root, "settings.json"), st)
}

// PlanOf 返回租户实际生效的套餐(含单独设置的额度)。
func (s *Store) PlanOf(t *Tenant) Plan {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.set.Plans[t.Plan]
	if !ok {
		p = s.set.Plans[s.set.SignupPlan]
	}
	if t.QuotaOverride != nil {
		p.MonthlyUnits = *t.QuotaOverride
	}
	return p
}

// ---------------- 管理员令牌 ----------------

// AdminToken 读取或生成管理员令牌(data/admin.key，仅本机可读)；环境变量 MAPSIM_ADMIN_TOKEN 优先。
func (s *Store) AdminToken() (string, bool, error) {
	if v := strings.TrimSpace(os.Getenv("MAPSIM_ADMIN_TOKEN")); v != "" {
		return v, false, nil
	}
	path := filepath.Join(s.Root, "admin.key")
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 20 {
		return strings.TrimSpace(string(b)), false, nil
	}
	b := make([]byte, 24)
	rand.Read(b)
	tok := "mad_" + hex.EncodeToString(b)
	return tok, true, os.WriteFile(path, []byte(tok+"\n"), 0o600)
}

func TokenEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---------------- 统计 ----------------

// UsageRows 读取最近 days 天的全部用量记录(所有租户)。
func (s *Store) UsageRows(days int) []UsageRec {
	since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	ents, _ := os.ReadDir(filepath.Join(s.Root, "usage"))
	var out []UsageRec
	for _, e := range ents {
		f, err := os.Open(filepath.Join(s.Root, "usage", e.Name()))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var u UsageRec
			if json.Unmarshal(sc.Bytes(), &u) == nil && u.Tenant != "" && u.Time >= since {
				out = append(out, u)
			}
		}
		f.Close()
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Time < out[b].Time })
	return out
}

// UsageByMonth 返回租户各月用量(月份 → 点数)。
func (s *Store) UsageByMonth(tenant string) map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.used {
		if strings.HasPrefix(k, tenant+"-") {
			out[strings.TrimPrefix(k, tenant+"-")] = v
		}
	}
	return out
}

func (s *Store) AllJobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created > out[b].Created })
	return out
}

func (s *Store) AllWorlds() []World {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]World, 0, len(s.wld))
	for _, w := range s.wld {
		out = append(out, *w)
	}
	return out
}

// Touch 记录租户最近一次调用接口的时间(内存中)。
func (s *Store) touch(tenant string) { s.seen[tenant] = time.Now().UTC().Format(time.RFC3339) }

func (s *Store) LastSeen(tenant string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[tenant]
}

// ---------------- 审计 ----------------

type AuditRec struct {
	Time   string         `json:"time"`
	Action string         `json:"action"`
	Target string         `json:"target,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
	IP     string         `json:"ip,omitempty"`
}

func (s *Store) Audit(action, target, ip string, detail map[string]any) {
	b, _ := json.Marshal(AuditRec{Time: now(), Action: action, Target: target, Detail: detail, IP: ip})
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.Root, "audit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func (s *Store) AuditLog(limit int) []AuditRec {
	b, _ := os.ReadFile(filepath.Join(s.Root, "audit.jsonl"))
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var out []AuditRec
	for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
		var a AuditRec
		if json.Unmarshal([]byte(lines[i]), &a) == nil {
			out = append(out, a)
		}
	}
	return out
}

// DirSize 统计目录占用(字节)。
func DirSize(dir string) int64 {
	var n int64
	filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}
