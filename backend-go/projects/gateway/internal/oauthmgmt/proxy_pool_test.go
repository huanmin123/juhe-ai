package oauthmgmt

// OAuth 代理池支持测试（代理池设计 §5.2/§5.3/§6.1/§7.5/§8.3）：建号链
// proxyPoolId 词表与互斥、临时分配、租约固化（pin 同一成员）、池绑定刷新
// 走已固化租约成员、实例 source 覆盖。夹具表见 pgkit_test.go 代理池段。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// seedOAuthProxyPool 种池 + 成员（成员复用 insertProxyProfile），返回池 ID。
func seedOAuthProxyPool(t *testing.T, env *testEnv, poolID string, enabled bool, members map[string]bool) string {
	t.Helper()
	poolEnabled := "TRUE"
	if !enabled {
		poolEnabled = "FALSE"
	}
	_, err := env.db.Exec(fixtureBind(`INSERT INTO proxy_pools (id, system_account_id, name, strategy, enabled, created_at, updated_at)
		VALUES (?, 'root-id', ?, 'round_robin', `+poolEnabled+`, now(), now())`), poolID, poolID)
	if err != nil {
		t.Fatalf("seed proxy pool: %v", err)
	}
	priority := 0
	for profileID, memberEnabled := range members {
		insertProxyProfile(t, env, profileID, "socks5", "127.0.0.1", 1080, "", "", true)
		memberEnabledValue := "TRUE"
		if !memberEnabled {
			memberEnabledValue = "FALSE"
		}
		if _, err := env.db.Exec(fixtureBind(`INSERT INTO proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled, created_at, updated_at)
			VALUES (?, ?, ?, 1, `+memberEnabledValue+`, now(), now())`), poolID, profileID, priority); err != nil {
			t.Fatalf("seed pool member: %v", err)
		}
		priority++
	}
	return poolID
}

// seedOAuthPoolLease 给账户写一条租约行（generation 自共享序列）。
func seedOAuthPoolLease(t *testing.T, env *testEnv, accountID, poolID, memberID string) int64 {
	t.Helper()
	var generation int64
	if err := env.db.QueryRow(fixtureBind(`INSERT INTO account_proxy_leases
		(account_id, pool_id, proxy_profile_id, generation, assignment_reason, assigned_at, updated_at)
		VALUES (?, ?, ?, nextval('juhe_business.account_proxy_lease_generation_seq'), 'initial', now(), now())
		RETURNING generation`), accountID, poolID, memberID).Scan(&generation); err != nil {
		t.Fatalf("seed pool lease: %v", err)
	}
	return generation
}

// oauthPoolLeaseOf 读账户当前租约（account_id, pool_id, member, generation）。
func oauthPoolLeaseOf(t *testing.T, env *testEnv, accountID string) (string, string, int64, bool) {
	t.Helper()
	var poolID, memberID string
	var generation int64
	err := env.db.QueryRow(fixtureBind(`SELECT pool_id, proxy_profile_id, generation
		FROM account_proxy_leases WHERE account_id = ?`), accountID).
		Scan(&poolID, &memberID, &generation)
	if err != nil {
		return "", "", 0, false
	}
	return poolID, memberID, generation, true
}

// TestParseManagedFieldsAcceptsProxyPoolId：proxyPoolId 进词表并按可选串解析
// （空白 = 未提交），与 proxyProfileId 同折叠语义。
func TestParseManagedFieldsAcceptsProxyPoolId(t *testing.T) {
	parsed, ok := parseManagedFields(map[string]any{"proxyPoolId": " pool-1 "})
	if !ok || parsed.ProxyPoolID == nil || *parsed.ProxyPoolID != "pool-1" {
		t.Fatalf("proxyPoolId 解析失败: ok=%v value=%v", ok, parsed.ProxyPoolID)
	}
	blank, ok := parseManagedFields(map[string]any{"proxyPoolId": "   "})
	if !ok || blank.ProxyPoolID != nil {
		t.Fatalf("空白 proxyPoolId 应折叠为未提交: ok=%v value=%v", ok, blank.ProxyPoolID)
	}
	// 非字符串与 proxyProfileId 同语义：bodyString 类型不符 = 未提交
	//（词表校验只拒绝未知键，类型折叠行为两键一致）。
	mistyped, ok := parseManagedFields(map[string]any{"proxyPoolId": 123})
	if !ok || mistyped.ProxyPoolID != nil {
		t.Fatalf("非字符串 proxyPoolId 应与 proxyProfileId 同折叠为未提交: ok=%v value=%v", ok, mistyped.ProxyPoolID)
	}
}

// TestAllocateOAuthPoolMemberURLBranches：临时分配三臂——健康成员返回成员
// URL（socks5→socks5h）；池停用/无成员/成员健康全 dead 明确 400 失败。
func TestAllocateOAuthPoolMemberURLBranches(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// 停用池 → 明确失败。
	seedOAuthProxyPool(t, env, "pool-disabled", false, map[string]bool{"p-a": true})
	if _, _, err := env.store.allocateOAuthPoolMemberURL(ctx, "pool-disabled"); err == nil {
		t.Fatal("disabled pool: want error")
	} else {
		requireValidationError(t, err, "已停用")
	}

	// 池不存在 → 明确失败。
	if _, _, err := env.store.allocateOAuthPoolMemberURL(ctx, "pool-missing"); err == nil {
		t.Fatal("missing pool: want error")
	} else {
		requireValidationError(t, err, "不存在或已停用")
	}

	// 池有 enabled 成员但健康全 dead（§6.2 不可分配）→ 明确失败。
	seedOAuthProxyPool(t, env, "pool-dead", true, map[string]bool{"p-dead": true})
	if _, err := env.db.Exec(fixtureBind(`UPDATE proxy_profiles SET pool_health_status = 'dead' WHERE id = 'p-dead'`)); err != nil {
		t.Fatalf("mark member dead: %v", err)
	}
	if _, _, err := env.store.allocateOAuthPoolMemberURL(ctx, "pool-dead"); err == nil {
		t.Fatal("all-dead pool: want error")
	} else {
		requireValidationError(t, err, "没有可分配成员")
	}

	// 健康成员 → 返回成员 id + 出站 URL（socks5h 归一）。
	seedOAuthProxyPool(t, env, "pool-ok", true, map[string]bool{"p-ok": true})
	memberID, memberURL, err := env.store.allocateOAuthPoolMemberURL(ctx, "pool-ok")
	if err != nil {
		t.Fatalf("healthy pool: unexpected error %v", err)
	}
	if memberID != "p-ok" || !strings.HasPrefix(memberURL, "socks5h://127.0.0.1:1080") {
		t.Fatalf("member = %q url = %q, want p-ok + socks5h://127.0.0.1:1080…", memberID, memberURL)
	}
}

// TestPinOAuthPoolLease 同一成员固化两臂：可分配成员固化为租约（generation
// 取共享序列、reason=initial）；成员停用后重新固化明确失败（不静默换成员）。
func TestPinOAuthPoolLease(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedOAuthProxyPool(t, env, "pool-pin", true, map[string]bool{"p-pin": true})
	// 账户行：无代理绑定（池建号落库后的形态）。
	if _, err := env.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, vendor_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acc-pin', 'root-id', 'gpt', 'openai', 'v1', 'pin', 'oauth', 'active', 'x', now()::text, now()::text)`)); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	if err := env.store.pinOAuthPoolLease(ctx, "acc-pin", "pool-pin", "p-pin"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	poolID, memberID, generation, ok := oauthPoolLeaseOf(t, env, "acc-pin")
	if !ok || poolID != "pool-pin" || memberID != "p-pin" || generation <= 0 {
		t.Fatalf("lease = %v/%v/%v ok=%v, want pool-pin/p-pin/generation>0", poolID, memberID, generation, ok)
	}
	var reason string
	if err := env.db.QueryRow(fixtureBind(`SELECT assignment_reason FROM account_proxy_leases WHERE account_id = ?`), "acc-pin").Scan(&reason); err != nil || reason != "initial" {
		t.Fatalf("assignment_reason = %q err=%v, want initial", reason, err)
	}
	// 固化后账户行池绑定已写（写面职责），此处只断言租约语义；pin 幂等：
	// 同一成员重复固化成功且不回退。
	if err := env.store.pinOAuthPoolLease(ctx, "acc-pin", "pool-pin", "p-pin"); err != nil {
		t.Fatalf("pin idempotent: %v", err)
	}

	// 成员停用 → 重新固化（新账户）明确失败，不静默换出口。
	if _, err := env.db.Exec(fixtureBind(`UPDATE proxy_pool_members SET enabled = FALSE WHERE pool_id = 'pool-pin' AND proxy_profile_id = 'p-pin'`)); err != nil {
		t.Fatalf("disable member: %v", err)
	}
	if _, err := env.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, vendor_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acc-pin-2', 'root-id', 'gpt', 'openai', 'v1', 'pin2', 'oauth', 'active', 'x', now()::text, now()::text)`)); err != nil {
		t.Fatalf("seed account 2: %v", err)
	}
	if err := env.store.pinOAuthPoolLease(ctx, "acc-pin-2", "pool-pin", "p-pin"); err == nil {
		t.Fatal("disabled member pin: want explicit error")
	} else {
		requireValidationError(t, err, "不换出口")
	}
}

// TestCreateFromRefreshTokenWithProxyPool 建号链端到端：proxyPoolId 建号 →
// token 交换固定使用临时分配成员 URL → 账户落库 proxy_pool_id + 租约固化在
// 同一成员；proxyProfileId 与 proxyPoolId 同时提交 400 互斥。
func TestCreateFromRefreshTokenWithProxyPool(t *testing.T) {
	env := newTestEnv(t)
	seedOAuthProxyPool(t, env, "pool-create", true, map[string]bool{"p-create": true})
	env.login(t, "root", "root-pass", "super_admin")

	// 互斥：两列同时非空 → 400 proxy_target_conflict 文案。
	env.exchanger.respond = staticToken(openAITokenPayload("should-not-happen"))
	baseline := len(env.exchanger.recorded())
	code, body := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","proxyProfileId":"p-create","proxyPoolId":"pool-create"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("conflict create: %d %v", code, body)
	}
	if message, _ := body["message"].(string); !strings.Contains(message, "不能同时选择") {
		t.Fatalf("conflict message = %v", body)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0（互斥在兑换前拒绝）", got)
	}

	// 池建号：交换走成员 URL，落库池绑定 + 租约同一成员。
	env.exchanger.respond = staticToken(openAITokenPayload("pool-create-token"))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","name":"池建号","proxyPoolId":"pool-create"}`)
	if code != http.StatusCreated {
		t.Fatalf("pool create: %d %v", code, created)
	}
	calls := env.exchanger.recorded()
	if len(calls) == 0 || !strings.HasPrefix(calls[len(calls)-1].ProxyURL, "socks5h://127.0.0.1:1080") {
		t.Fatalf("token exchange ProxyURL = %+v, want socks5h member URL", calls[len(calls)-1].ProxyURL)
	}
	accountID := dataMap(t, created)["id"].(string)
	var ownPool, ownProfile string
	if err := env.db.QueryRow(fixtureBind(`SELECT COALESCE(proxy_pool_id, ''), COALESCE(proxy_profile_id, '') FROM accounts WHERE id = ?`), accountID).
		Scan(&ownPool, &ownProfile); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if ownPool != "pool-create" || ownProfile != "" {
		t.Fatalf("account binding = pool:%q profile:%q, want pool-create/空（互斥落库）", ownPool, ownProfile)
	}
	leasePool, leaseMember, _, ok := oauthPoolLeaseOf(t, env, accountID)
	if !ok || leasePool != "pool-create" || leaseMember != "p-create" {
		t.Fatalf("lease = %v/%v ok=%v, want pool-create/p-create（同一成员固化）", leasePool, leaseMember, ok)
	}
}

// TestOAuthProxyRequestURLPoolLease 有效目标解析：池绑定账户走租约成员 URL；
// 无租约明确失败不直连；实例行 source 池覆盖（实例自身 proxy_profile_id 与
// proxy_pool_id 都不参与）。
func TestOAuthProxyRequestURLPoolLease(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedOAuthProxyPool(t, env, "pool-resolve", true, map[string]bool{"p-a": true, "p-b": true})
	seedOAuthProxyPool(t, env, "pool-src", true, map[string]bool{"p-src": true})

	newAccount := func(t *testing.T, id string) {
		t.Helper()
		if _, err := env.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, vendor_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, created_at, updated_at)
			VALUES (?, 'root-id', 'gpt', 'openai', 'v1', ?, 'oauth', 'active', 'x', now()::text, now()::text)`), id, id); err != nil {
			t.Fatalf("seed account %s: %v", id, err)
		}
	}

	// 池绑定 + 租约 → 租约成员 URL。
	newAccount(t, "acc-pool")
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET proxy_pool_id = 'pool-resolve' WHERE id = 'acc-pool'`)); err != nil {
		t.Fatalf("bind pool: %v", err)
	}
	seedOAuthPoolLease(t, env, "acc-pool", "pool-resolve", "p-b")
	proxyURL, err := env.store.oauthProxyRequestURL(ctx, "acc-pool", "")
	if err != nil || !strings.HasPrefix(proxyURL, "socks5h://127.0.0.1:1080") {
		t.Fatalf("pool lease url = %q err=%v, want socks5h member URL", proxyURL, err)
	}

	// 池绑定无租约 → 明确失败（只读进程不重分配），不直连。
	newAccount(t, "acc-pool-nolease")
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET proxy_pool_id = 'pool-resolve' WHERE id = 'acc-pool-nolease'`)); err != nil {
		t.Fatalf("bind pool: %v", err)
	}
	if _, err := env.store.oauthProxyRequestURL(ctx, "acc-pool-nolease", ""); err == nil {
		t.Fatal("pool without lease: want explicit error")
	} else {
		requireValidationError(t, err, "租约")
	}

	// 实例行：source 池覆盖——实例自身 proxy_profile_id/proxy_pool_id 都不
	// 参与解析（§5.2），租约键 = source account。
	newAccount(t, "acc-src")
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET proxy_pool_id = 'pool-src' WHERE id = 'acc-src'`)); err != nil {
		t.Fatalf("bind source pool: %v", err)
	}
	seedOAuthPoolLease(t, env, "acc-src", "pool-src", "p-src")
	if _, err := env.db.Exec(fixtureBind(`INSERT INTO accounts (id, system_account_id, vendor_code, protocol_code, protocol_version, name, type, status, credentials_encrypted, proxy_profile_id, proxy_pool_id, authorization_instance_source_account_id, created_at, updated_at)
		VALUES ('acc-inst', 'root-id', 'gpt', 'openai', 'v1', 'acc-inst', 'oauth', 'active', 'x', 'ghost-profile', 'ghost-pool', 'acc-src', now()::text, now()::text)`)); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	proxyURL, err = env.store.oauthProxyRequestURL(ctx, "acc-inst", "")
	if err != nil || !strings.HasPrefix(proxyURL, "socks5h://127.0.0.1:1080") {
		t.Fatalf("instance source override url = %q err=%v, want source lease member URL", proxyURL, err)
	}

	// 无绑定 → ""（直连既有语义）。
	newAccount(t, "acc-plain")
	proxyURL, err = env.store.oauthProxyRequestURL(ctx, "acc-plain", "")
	if err != nil || proxyURL != "" {
		t.Fatalf("unbound url = %q err=%v, want empty", proxyURL, err)
	}
}

// TestAutoRefreshPoolBoundUsesLease 池绑定账户自动刷新：已固化租约 → token
// 交换走租约成员 URL 且轮换成功；租约缺失 → 本地明确失败，token 端点零调用。
func TestAutoRefreshPoolBoundUsesLease(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	seedOAuthProxyPool(t, env, "pool-refresh", true, map[string]bool{"p-refresh": true})
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = staticToken(openAITokenPayload("create-token"))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","name":"池刷新"}`)
	if code != http.StatusCreated {
		t.Fatalf("建号: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	// 建号后再绑池并写租约（模拟管理面改绑 + 租约固化）。
	if _, err := env.db.Exec(fixtureBind(`UPDATE accounts SET proxy_pool_id = 'pool-refresh' WHERE id = ?`), accountID); err != nil {
		t.Fatalf("bind pool: %v", err)
	}
	seedOAuthPoolLease(t, env, accountID, "pool-refresh", "p-refresh")

	baseline := len(env.exchanger.recorded())
	env.exchanger.respond = staticToken(openAITokenPayload("pool-refreshed-token"))
	result, err := env.store.RotateAccountTokensFromStored(ctx, accountID)
	if err != nil || result == nil || !result.Rotated {
		t.Fatalf("pool auto refresh: result=%v err=%v", result, err)
	}
	calls := env.exchanger.recorded()
	if len(calls)-baseline != 1 || !strings.HasPrefix(calls[baseline].ProxyURL, "socks5h://127.0.0.1:1080") {
		t.Fatalf("refresh ProxyURL calls=%d last=%+v, want 租约成员 socks5h URL 一次", len(calls)-baseline, calls[len(calls)-1])
	}

	// 租约缺失 → 明确失败，不直连、零上游调用。
	if _, err := env.db.Exec(fixtureBind(`DELETE FROM account_proxy_leases WHERE account_id = ?`), accountID); err != nil {
		t.Fatalf("drop lease: %v", err)
	}
	baseline = len(env.exchanger.recorded())
	_, err = env.store.RotateAccountTokensFromStored(ctx, accountID)
	if err == nil {
		t.Fatal("pool without lease refresh: want explicit error")
	}
	var upstream *AutoRefreshUpstreamError
	if errors.As(err, &upstream) {
		t.Fatalf("lease-missing failure is local, not upstream: %v", err)
	}
	if got := len(env.exchanger.recorded()) - baseline; got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0", got)
	}
}
