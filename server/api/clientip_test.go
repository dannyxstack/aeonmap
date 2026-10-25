package api

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.5:1234", "", "203.0.113.5"},
		{"203.0.113.5:1234", "1.2.3.4", "203.0.113.5"},               // 公网直连：不信任 XFF
		{"127.0.0.1:1234", "198.51.100.7", "198.51.100.7"},           // 本机代理
		{"172.18.0.1:1234", "6.6.6.6, 198.51.100.7", "198.51.100.7"}, // Docker 网关：取最右(代理追加)的一项
		{"127.0.0.1:1234", "not-an-ip", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("remote=%s xff=%q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}
