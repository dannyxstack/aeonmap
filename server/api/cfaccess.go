package api

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Cloudflare Access：在 Cloudflare Zero Trust 里给 /admin* 加访问策略后，Cloudflare 会在转发的请求里带上
// Cf-Access-Jwt-Assertion(RS256 签名的身份令牌)。服务端校验它，即使有人绕过 Cloudflare 直连源站也进不了管理后台。
//   MAPSIM_CF_ACCESS_TEAM  团队域名前缀，如 novakite(即 https://novakite.cloudflareaccess.com)
//   MAPSIM_CF_ACCESS_AUD   该 Access 应用的 Application Audience (AUD) Tag
// 两者都设置时启用；管理员令牌仍然需要。

type CFAccess struct {
	Team, Aud string
	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetched   time.Time
}

func CFAccessFromEnv() *CFAccess {
	team, aud := strings.TrimSpace(os.Getenv("MAPSIM_CF_ACCESS_TEAM")), strings.TrimSpace(os.Getenv("MAPSIM_CF_ACCESS_AUD"))
	if team == "" || aud == "" {
		return nil
	}
	team = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(team, "https://"), "http://"), ".cloudflareaccess.com")
	return &CFAccess{Team: team, Aud: aud}
}

func (c *CFAccess) issuer() string { return "https://" + c.Team + ".cloudflareaccess.com" }

// Verify 校验令牌，返回其中的邮箱。
func (c *CFAccess) Verify(ctx context.Context, tok string) (string, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("缺少 Cloudflare Access 令牌")
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	pb, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", fmt.Errorf("令牌编码错误")
	}
	var h struct{ Alg, Kid string }
	if json.Unmarshal(hb, &h) != nil || h.Alg != "RS256" {
		return "", fmt.Errorf("不支持的签名算法")
	}
	key, err := c.key(ctx, h.Kid)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return "", fmt.Errorf("签名无效")
	}
	var cl struct {
		Aud   any    `json:"aud"`
		Iss   string `json:"iss"`
		Exp   int64  `json:"exp"`
		Email string `json:"email"`
	}
	if json.Unmarshal(pb, &cl) != nil {
		return "", fmt.Errorf("令牌内容无效")
	}
	audOK := false
	switch a := cl.Aud.(type) {
	case string:
		audOK = a == c.Aud
	case []any:
		for _, v := range a {
			audOK = audOK || v == c.Aud
		}
	}
	if !audOK || cl.Iss != c.issuer() || time.Now().Unix() > cl.Exp {
		return "", fmt.Errorf("令牌的 aud/iss 不匹配或已过期")
	}
	return cl.Email, nil
}

// key：按 kid 取公钥；本地缓存 1 小时，遇到未知 kid(Cloudflare 轮换密钥)时重新拉取。
func (c *CFAccess) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if k, ok := c.keys[kid]; ok && time.Since(c.fetched) < time.Hour {
		return k, nil
	}
	if time.Since(c.fetched) < 10*time.Second && c.keys != nil {
		return nil, fmt.Errorf("未知的签名密钥")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.issuer()+"/cdn-cgi/access/certs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取 Cloudflare Access 公钥失败: %w", err)
	}
	defer resp.Body.Close()
	var jwks struct {
		Keys []struct{ Kid, Kty, N, E string } `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("Cloudflare Access 公钥格式错误: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(k.N)
		eb, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(new(big.Int).SetBytes(eb).Int64())}
	}
	c.keys, c.fetched = keys, time.Now()
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("未知的签名密钥")
}
