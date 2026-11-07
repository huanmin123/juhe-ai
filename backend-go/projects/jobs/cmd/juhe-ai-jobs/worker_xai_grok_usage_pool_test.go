package main

// worker_xai_grok_usage_pool_test.go 覆盖 xAI 用量刷新的池绑定有效目标（代理
// 池设计 §5.2/§5.3 只读入口）：池绑定（授权实例按 source 池、租约键 =
// source account，实例自身池绑定不参与）解析当前租约成员出站；无有效租约/
// 池不可用明确失败且在上游调用前失败（不直连）；直接代理与无绑定保持既有
// xaiGrokProxyRequestURL 语义。夹具复用 balanceFixtureSchema（含池表与
// proxy_pool_id / source 列）。

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// xaiGrokNewPoolRuntime 构造接 balanceFixtureSchema 业务库的运行态。
func xaiGrokNewPoolRuntime(t *testing.T) *xaiGrokUsageRuntime {
	t.Helper()
	biz, _ := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		return balanceFixtureSchema(ctx, db, db)
	})
	return &xaiGrokUsageRuntime{
		business: &businessDB{db: biz},
		secret:   wgBalanceSecret,
		nowFunc:  func() time.Time { return time.Now().UTC() },
	}
}

// xaiGrokSeedAccount 写入一条 xai oauth 候选账户（凭据用明文 JSON，
// decryptXAIGrokCredentials 直接接受）。
func xaiGrokSeedAccount(t *testing.T, rt *xaiGrokUsageRuntime, id string) {
	t.Helper()
	if _, err := rt.business.db.Exec(`INSERT INTO juhe_business.accounts
		(id, system_account_id, vendor_code, connection_driver_code, type, status, schedulable, credentials_encrypted, balance_query_enabled, balance_query_config_json, created_at, updated_at)
		VALUES (?, ?, 'xai', 'xai', 'oauth', 'active', 1, '{"access_token":"at","base_url":"http://127.0.0.1:1"}', 0, '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, "sys-xai"); err != nil {
		t.Fatal(err)
	}
}

// xaiGrokSeedPool 写入池/成员/租约夹具（fixture 池表 enabled 为 BOOLEAN，
// 与共享解析器扫描契约一致；密码封套与 runtime secret 同一 v1 envelope 家族）。
func xaiGrokSeedPool(t *testing.T, rt *xaiGrokUsageRuntime, poolID, memberID string, poolEnabled bool, leaseKey string) {
	t.Helper()
	sealed, err := accountbalance.EncryptV1Envelope(rt.secret, []byte(`{"password":"pool-pass"}`))
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO juhe_business.proxy_profiles (id, type, host, port, username, password_encrypted, enabled, pool_health_status)
		  VALUES (?, 'socks5', 'member.example', 1080, 'pool-user', ?, true, 'healthy')
		  ON CONFLICT (id) DO NOTHING`, []any{memberID, sealed}},
		{`INSERT INTO juhe_business.proxy_pools (id, name, strategy, enabled) VALUES (?, ?, 'round_robin', ?)
		  ON CONFLICT (id) DO NOTHING`, []any{poolID, poolID, poolEnabled}},
		{`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled)
		  VALUES (?, ?, 0, 1, true) ON CONFLICT (pool_id, proxy_profile_id) DO NOTHING`, []any{poolID, memberID}},
	}
	for _, statement := range statements {
		if _, err := rt.business.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if leaseKey != "" {
		if _, err := rt.business.db.Exec(`INSERT INTO juhe_business.account_proxy_leases (account_id, pool_id, proxy_profile_id, generation, assignment_reason)
			VALUES (?, ?, ?, 1, 'initial') ON CONFLICT (account_id) DO NOTHING`, leaseKey, poolID, memberID); err != nil {
			t.Fatal(err)
		}
	}
}

// xaiGrokListAccountByID 经 listCandidates 取回单账户投影。
func xaiGrokListAccountByID(t *testing.T, rt *xaiGrokUsageRuntime, id string) (xaiGrokUsageAccount, bool) {
	t.Helper()
	accounts, err := rt.listCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.id == id {
			return account, true
		}
	}
	return xaiGrokUsageAccount{}, false
}

// TestXAIGrokEffectiveProxyURLPoolArms 逐分支覆盖池绑定的只读解析语义。
func TestXAIGrokEffectiveProxyURLPoolArms(t *testing.T) {
	rt := xaiGrokNewPoolRuntime(t)
	ctx := context.Background()

	// 无绑定：保持既有直连语义（空串）。
	xaiGrokSeedAccount(t, rt, "xai-direct")
	direct, ok := xaiGrokListAccountByID(t, rt, "xai-direct")
	if !ok {
		t.Fatal("无绑定候选必须在候选列表")
	}
	url, err := rt.xaiGrokEffectiveProxyURL(ctx, direct)
	if err != nil || url != "" {
		t.Fatalf("无绑定 url=%q err=%v", url, err)
	}

	// 有租约：解析到租约成员 URL（socks5h + 认证）。
	xaiGrokSeedAccount(t, rt, "xai-pool")
	xaiGrokSeedPool(t, rt, "xai-pool-1", "xai-member", true, "xai-pool")
	if _, err := rt.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = ? WHERE id = ?`, "xai-pool-1", "xai-pool"); err != nil {
		t.Fatal(err)
	}
	pooled, ok := xaiGrokListAccountByID(t, rt, "xai-pool")
	if !ok || pooled.proxyPoolID.String != "xai-pool-1" {
		t.Fatalf("池列必须进入候选投影: %+v ok=%v", pooled, ok)
	}
	url, err = rt.xaiGrokEffectiveProxyURL(ctx, pooled)
	if err != nil || url != "socks5h://pool-user:pool-pass@member.example:1080" {
		t.Fatalf("池成员 url=%q err=%v", url, err)
	}

	// 无租约：明确失败（proxy_pool_unavailable），不直连。
	if _, err := rt.business.db.Exec(`DELETE FROM juhe_business.account_proxy_leases WHERE account_id = ?`, "xai-pool"); err != nil {
		t.Fatal(err)
	}
	url, err = rt.xaiGrokEffectiveProxyURL(ctx, pooled)
	if err == nil || url != "" || !strings.Contains(err.Error(), "proxy_pool_unavailable") {
		t.Fatalf("无租约 url=%q err=%v", url, err)
	}

	// 池停用：明确失败。
	xaiGrokSeedPool(t, rt, "xai-pool-off", "xai-member-off", false, "xai-pool")
	if _, err := rt.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'xai-pool-off' WHERE id = 'xai-pool'`); err != nil {
		t.Fatal(err)
	}
	if pooled, ok = xaiGrokListAccountByID(t, rt, "xai-pool"); !ok {
		t.Fatal("池停用候选必须在候选列表")
	}
	if _, err := rt.xaiGrokEffectiveProxyURL(ctx, pooled); err == nil || !strings.Contains(err.Error(), "proxy_pool_unavailable") {
		t.Fatalf("池停用 err=%v", err)
	}

	// 直接代理（配置档案）保持既有语义。
	xaiGrokSeedAccount(t, rt, "xai-profile")
	directSealed, err := accountbalance.EncryptV1Envelope(rt.secret, []byte(`{"password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.business.db.Exec(`INSERT INTO juhe_business.proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
		VALUES ('xai-profile-pp', 'socks5', 'direct.example', 1080, 'alice', ?, true)
		ON CONFLICT (id) DO NOTHING`, directSealed); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_profile_id = 'xai-profile-pp' WHERE id = 'xai-profile'`); err != nil {
		t.Fatal(err)
	}
	profiled, ok := xaiGrokListAccountByID(t, rt, "xai-profile")
	if !ok {
		t.Fatal("直接代理候选必须在候选列表")
	}
	url, err = rt.xaiGrokEffectiveProxyURL(ctx, profiled)
	if err != nil || url != "socks5h://alice:secret@direct.example:1080" {
		t.Fatalf("直接代理 url=%q err=%v", url, err)
	}
}

// TestXAIGrokPoolInstanceUsesSourceLeaseKey 授权实例取 source 池且租约键 =
// source account；实例自身池绑定不参与解析。
func TestXAIGrokPoolInstanceUsesSourceLeaseKey(t *testing.T) {
	rt := xaiGrokNewPoolRuntime(t)
	xaiGrokSeedAccount(t, rt, "xai-src")
	xaiGrokSeedAccount(t, rt, "xai-inst")
	xaiGrokSeedPool(t, rt, "xai-src-pool", "xai-src-member", true, "xai-src")
	xaiGrokSeedPool(t, rt, "xai-own-pool", "xai-own-member", true, "xai-inst")
	if _, err := rt.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'xai-src-pool' WHERE id = 'xai-src'`); err != nil {
		t.Fatal(err)
	}
	// 实例行误写自身池绑定：按 §5.2 不参与解析。
	if _, err := rt.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'xai-own-pool',
		authorization_instance_source_account_id = 'xai-src' WHERE id = 'xai-inst'`); err != nil {
		t.Fatal(err)
	}
	instance, ok := xaiGrokListAccountByID(t, rt, "xai-inst")
	if !ok || instance.sourceAccountID.String != "xai-src" {
		t.Fatalf("实例候选必须带 source 列: %+v ok=%v", instance, ok)
	}
	url, err := rt.xaiGrokEffectiveProxyURL(context.Background(), instance)
	if err != nil || url != "socks5h://pool-user:pool-pass@member.example:1080" {
		t.Fatalf("source 池成员 url=%q err=%v", url, err)
	}

	// source 租约缺失：明确失败，不回落实例自身池。
	if _, err := rt.business.db.Exec(`DELETE FROM juhe_business.account_proxy_leases WHERE account_id = ?`, "xai-src"); err != nil {
		t.Fatal(err)
	}
	url, err = rt.xaiGrokEffectiveProxyURL(context.Background(), instance)
	if err == nil || url != "" || !strings.Contains(err.Error(), "proxy_pool_unavailable") {
		t.Fatalf("source 租约缺失 url=%q err=%v", url, err)
	}
}

// TestXAIGrokAttemptAccountFailsBeforeDialWhenPoolUnavailable：池不可用时
// attemptAccount 在任何出站前失败（不直连、不用旧成员）。
func TestXAIGrokAttemptAccountFailsBeforeDialWhenPoolUnavailable(t *testing.T) {
	rt := xaiGrokNewPoolRuntime(t)
	xaiGrokSeedAccount(t, rt, "xai-pool-nolease")
	xaiGrokSeedPool(t, rt, "xai-pool-nl", "xai-member-nl", true, "")
	if _, err := rt.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = 'xai-pool-nl' WHERE id = 'xai-pool-nolease'`); err != nil {
		t.Fatal(err)
	}
	account, ok := xaiGrokListAccountByID(t, rt, "xai-pool-nolease")
	if !ok {
		t.Fatal("池候选必须在候选列表")
	}
	factoryCalled := false
	rt.clientFactory = func(proxyURL string) (*http.Client, error) {
		factoryCalled = true
		return &http.Client{}, nil
	}
	_, err := rt.attemptAccount(context.Background(), account)
	if err == nil || !strings.Contains(err.Error(), "proxy_pool_unavailable") {
		t.Fatalf("池不可用必须明确失败: %v", err)
	}
	if factoryCalled {
		t.Fatal("池不可用必须在任何出站前失败（不直连）")
	}
}
