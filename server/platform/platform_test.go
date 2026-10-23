package platform

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeysQuotaAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tn, key, err := s.CreateTenant("测试", "free")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Authenticate(key); !ok || got.ID != tn.ID {
		t.Fatal("新密钥应能认证")
	}
	if _, ok := s.Authenticate("msk_wrong"); ok {
		t.Fatal("错误密钥不应通过")
	}
	// 额度：免费 100 点；已用 60 + 排队中预估 30，再要 20 应被拒绝
	s.Charge(tn.ID, "j1", "simulate", 60)
	s.PutJob(&Job{ID: "j2", Tenant: tn.ID, Status: "queued", Estimate: 30, Created: now()})
	if err := s.CheckQuota(tn.ID, 10); err != nil {
		t.Fatalf("100 点以内应允许: %v", err)
	}
	if err := s.CheckQuota(tn.ID, 20); err == nil {
		t.Fatal("超出额度应拒绝(含排队任务的预估)")
	}
	// 用量在重启后保留
	s2, _ := Open(dir)
	if u := s2.UsedThisMonth(tn.ID); u != 60 {
		t.Fatalf("重启后用量应为 60，得到 %d", u)
	}
	// 另一个进程(命令行)吊销密钥后，服务端无需重启即生效
	time.Sleep(20 * time.Millisecond)
	if err := s2.RevokeKey(tn.ID, key[:10]); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	os.Chtimes(filepath.Join(dir, "tenants.json"), future, future)
	if _, ok := s.Authenticate(key); ok {
		t.Fatal("吊销后的密钥应立即失效")
	}
	// 回调签名稳定
	if SignWebhook("sec", []byte("1.{}")) != SignWebhook("sec", []byte("1.{}")) {
		t.Fatal("签名应确定")
	}
}
