package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AIProviderConf 是某个 AI 服务商的接入参数。APIKey 只保存在 data/ai.json(仅本机可读)。
type AIProviderConf struct {
	BaseURL string `json:"baseURL,omitempty"` // 为空时用服务商默认地址
	Model   string `json:"model,omitempty"`   // 为空时用服务商默认模型
	APIKey  string `json:"apiKey,omitempty"`
}

// AIConfig：AI 识图配置。可为多家服务商分别填写参数，Active 指定当前使用哪一家。
type AIConfig struct {
	Enabled      bool                      `json:"enabled"`
	Active       string                    `json:"active"`
	Providers    map[string]AIProviderConf `json:"providers"`
	TimeoutSec   int                       `json:"timeoutSec"`   // 单次请求超时
	MaxImagePx   int                       `json:"maxImagePx"`   // 发送前把长边缩到不超过此像素
	UnitsPerCall int                       `json:"unitsPerCall"` // 每次识图扣的算力点
}

func DefaultAIConfig() AIConfig {
	return AIConfig{Providers: map[string]AIProviderConf{}, TimeoutSec: 120, MaxImagePx: 1568, UnitsPerCall: 1}
}

func (c *AIConfig) validate() error {
	if c.TimeoutSec < 10 || c.TimeoutSec > 600 {
		return fmt.Errorf("超时需在 10–600 秒之间")
	}
	if c.MaxImagePx < 256 || c.MaxImagePx > 4096 {
		return fmt.Errorf("图片长边上限需在 256–4096 像素之间")
	}
	if c.UnitsPerCall < 0 {
		return fmt.Errorf("每次识图的算力点不能为负")
	}
	for k, p := range c.Providers {
		if p.BaseURL != "" && !strings.HasPrefix(p.BaseURL, "https://") && !strings.HasPrefix(p.BaseURL, "http://") {
			return fmt.Errorf("%s 的接口地址必须以 http:// 或 https:// 开头", k)
		}
	}
	if c.Enabled && c.Active == "" {
		return fmt.Errorf("启用 AI 识图时需要选择服务商")
	}
	return nil
}

func (s *Store) aiPath() string { return filepath.Join(s.Root, "ai.json") }

// AIConfig 返回当前 AI 配置(含密钥，仅供服务端内部使用)。
func (s *Store) AIConfig() AIConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := DefaultAIConfig()
	if b, err := os.ReadFile(s.aiPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if c.Providers == nil {
		c.Providers = map[string]AIProviderConf{}
	}
	return c
}

// SetAIConfig 保存配置。某服务商的 APIKey 为空时沿用原有密钥(管理后台不回显密钥)；
// 要清除密钥，传 ClearKeys 中的服务商标识。
func (s *Store) SetAIConfig(c AIConfig, clearKeys []string) (AIConfig, error) {
	if c.Providers == nil {
		c.Providers = map[string]AIProviderConf{}
	}
	if err := c.validate(); err != nil {
		return c, err
	}
	old := s.AIConfig()
	for k, p := range c.Providers {
		p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		p.Model = strings.TrimSpace(p.Model)
		p.APIKey = strings.TrimSpace(p.APIKey)
		if p.APIKey == "" {
			p.APIKey = old.Providers[k].APIKey
		}
		c.Providers[k] = p
	}
	for _, k := range clearKeys {
		if p, ok := c.Providers[k]; ok {
			p.APIKey = ""
			c.Providers[k] = p
		}
	}
	b, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return c, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := s.aiPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return c, err
	}
	return c, os.Rename(tmp, s.aiPath())
}

// MaskKey 把密钥显示成 "sk-a…9f3c" 的形式。
func MaskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 10 {
		return "已设置"
	}
	return k[:4] + "…" + k[len(k)-4:]
}
