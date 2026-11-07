package main

// worker_balance_detect_pool_test.go 覆盖余额自动探测的池绑定有效目标（代理
// 池设计 §5.2/§10.3 只读入口）：候选代理字段解析为当前租约成员；余额查询身
// 份摘要槽含 proxy_pool_id + 有效成员档案 ID（租约迁移视为不同输入）；无有
// 效租约/池不可用时该候选明确失败走既有 retry 机制，不直连、不直连旧成员。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
)

// wgBalanceSeedPool 在业务库写入池/成员/租约行（balanceFixtureSchema 池表
// 子集；密码封套与 runtime secret 同一 v1 envelope 家族）。
func wgBalanceSeedPool(t *testing.T, rt *balanceDetectRuntime, poolID, memberID string, memberEnabled, poolEnabled bool, leaseKey string) {
	t.Helper()
	sealed, err := accountbalance.EncryptV1Envelope(rt.secret, []byte(`{"password":"pool-pass"}`))
	if err != nil {
		t.Fatal(err)
	}
	memberEnabledArg := memberEnabled
	poolEnabledArg := poolEnabled
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO juhe_business.proxy_profiles (id, type, host, port, username, password_encrypted, enabled, pool_health_status)
		  VALUES (?, 'socks5', 'member.example', 1080, 'pool-user', ?, ?, 'healthy')
		  ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, password_encrypted = excluded.password_encrypted`,
			[]any{memberID, sealed, memberEnabledArg}},
		{`INSERT INTO juhe_business.proxy_pools (id, name, strategy, enabled) VALUES (?, ?, 'round_robin', ?)
		  ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled`,
			[]any{poolID, poolID, poolEnabledArg}},
		{`INSERT INTO juhe_business.proxy_pool_members (pool_id, proxy_profile_id, priority, weight, enabled) VALUES (?, ?, 0, 1, ?)
		  ON CONFLICT (pool_id, proxy_profile_id) DO UPDATE SET enabled = excluded.enabled`,
			[]any{poolID, memberID, memberEnabledArg}},
	}
	for _, statement := range statements {
		if _, err := rt.business.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if leaseKey != "" {
		if _, err := rt.business.db.Exec(`INSERT INTO juhe_business.account_proxy_leases (account_id, pool_id, proxy_profile_id, generation, assignment_reason)
			VALUES (?, ?, ?, 1, 'initial')
			ON CONFLICT (account_id) DO UPDATE SET pool_id = excluded.pool_id, proxy_profile_id = excluded.proxy_profile_id`,
			leaseKey, poolID, memberID); err != nil {
			t.Fatal(err)
		}
	}
}

// wgBalanceSeedPoolAccount 写入一个绑定池的到期候选账户（复用
// wgSeedDueAccount 后补池绑定列）。
func wgBalanceSeedPoolAccount(t *testing.T, rt *balanceDetectRuntime, id, poolID string) {
	t.Helper()
	wgSeedDueAccount(t, rt.business.db, id, "http://127.0.0.1:1", -time.Minute)
	if _, err := rt.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_pool_id = ? WHERE id = ?`, poolID, id); err != nil {
		t.Fatal(err)
	}
}

func wgBalanceLeaseCount(t *testing.T, rt *balanceDetectRuntime) int {
	t.Helper()
	var count int
	if err := rt.business.db.QueryRow(`SELECT COUNT(*) FROM juhe_business.account_proxy_leases`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestBalanceDetectPoolCandidateResolvesLeaseMember 池绑定候选的有效目标是
// 租约成员：出站信封与身份摘要槽都指向成员；解析全程只读。
func TestBalanceDetectPoolCandidateResolvesLeaseMember(t *testing.T) {
	runtime, _, _ := wgNewBalanceRuntime(t, "", false)
	wgBalanceSeedPool(t, runtime, "w-pool-ok", "w-pool-ok-member", true, true, "w-pool-acc")
	wgBalanceSeedPoolAccount(t, runtime, "w-pool-acc", "w-pool-ok")
	before := wgBalanceLeaseCount(t, runtime)

	ctx := context.Background()
	selected, err := runtime.ListDueCandidates(ctx, 5)
	if err != nil || len(selected) != 1 {
		t.Fatalf("候选准备: %v %v", selected, err)
	}
	candidate := selected[0]
	if candidate.ProxyPoolID != "w-pool-ok" || candidate.ProxyMemberProfileID != "w-pool-ok-member" {
		t.Fatalf("候选有效目标必须解析到租约成员: %+v", candidate)
	}
	config := opsjobs.BalanceQueryConfig{Adapter: "builtin", IntervalMinutes: 5}
	input, err := runtime.buildQueryInput(ctx, candidate, config)
	if err != nil {
		t.Fatalf("buildQueryInput: %v", err)
	}
	if input.ProxyProfileID != "pool:w-pool-ok:w-pool-ok-member" {
		t.Fatalf("身份摘要槽必须含池 ID + 有效成员档案 ID，得到 %q", input.ProxyProfileID)
	}
	if input.Proxy == nil {
		t.Fatal("池绑定候选必须携带成员出站信封")
	}
	plaintext, err := accountbalance.DecryptV1Envelope(runtime.secret, input.Proxy.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envelope.URL, "socks5h://pool-user:pool-pass@member.example:1080") {
		t.Fatalf("出站信封必须是租约成员 URL，得到 %s", envelope.URL)
	}
	if after := wgBalanceLeaseCount(t, runtime); after != before {
		t.Fatalf("解析不得补租约：before=%d after=%d", before, after)
	}
}

// TestBalanceDetectPoolUnavailableFailsCandidate 无租约/池停用时候选保留池
// 绑定但成员为空，buildQueryInput 明确失败（既有 retry 机制），不直连。
func TestBalanceDetectPoolUnavailableFailsCandidate(t *testing.T) {
	runtime, _, _ := wgNewBalanceRuntime(t, "", false)
	wgBalanceSeedPool(t, runtime, "w-pool-nl", "w-pool-nl-member", true, true, "")
	wgBalanceSeedPool(t, runtime, "w-pool-off", "w-pool-off-member", true, false, "w-pool-acc-off")
	wgBalanceSeedPoolAccount(t, runtime, "w-pool-acc-nl", "w-pool-nl")
	wgBalanceSeedPoolAccount(t, runtime, "w-pool-acc-off", "w-pool-off")

	ctx := context.Background()
	selected, err := runtime.ListDueCandidates(ctx, 5)
	if err != nil {
		t.Fatalf("候选准备: %v", err)
	}
	byID := map[string]opsjobs.BalanceDetectionCandidate{}
	for _, candidate := range selected {
		byID[candidate.ID] = candidate
	}
	for _, id := range []string{"w-pool-acc-nl", "w-pool-acc-off"} {
		candidate, ok := byID[id]
		if !ok {
			t.Fatalf("池不可用候选 %s 必须保留在候选列表（走 retry 链路）: %#v", id, selected)
		}
		if candidate.ProxyPoolID == "" || candidate.ProxyMemberProfileID != "" {
			t.Fatalf("池不可用候选 %s 必须为池绑定且成员为空: %+v", id, candidate)
		}
		if _, err := runtime.buildQueryInput(ctx, candidate, opsjobs.BalanceQueryConfig{Adapter: "builtin"}); err == nil ||
			!strings.Contains(err.Error(), "proxy_pool_unavailable") {
			t.Fatalf("池不可用候选 %s 必须明确失败: %v", id, err)
		}
	}
	if after := wgBalanceLeaseCount(t, runtime); after != 1 {
		t.Fatalf("只读解析不得写租约，租约行数=%d", after)
	}
}

// TestBalanceDetectDirectProfileIdentityUnchanged 直接代理候选的身份摘要槽
// 保持配置档案 ID（既有语义），出站信封按档案行构造。
func TestBalanceDetectDirectProfileIdentityUnchanged(t *testing.T) {
	runtime, _, _ := wgNewBalanceRuntime(t, "", false)
	sealed, err := accountbalance.EncryptV1Envelope(runtime.secret, []byte(`{"password":"direct-pass"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.business.db.Exec(`INSERT INTO juhe_business.proxy_profiles (id, type, host, port, username, password_encrypted, enabled)
		VALUES ('w-direct-profile', 'http', 'direct.example', 8080, 'direct-user', ?, TRUE)
		ON CONFLICT (id) DO NOTHING`, sealed); err != nil {
		t.Fatal(err)
	}
	wgSeedDueAccount(t, runtime.business.db, "w-direct-acc", "http://127.0.0.1:1", -time.Minute)
	if _, err := runtime.business.db.Exec(`UPDATE juhe_business.accounts SET proxy_profile_id = 'w-direct-profile' WHERE id = 'w-direct-acc'`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	selected, err := runtime.ListDueCandidates(ctx, 5)
	if err != nil || len(selected) != 1 {
		t.Fatalf("候选准备: %v %v", selected, err)
	}
	candidate := selected[0]
	if candidate.ProxyPoolID != "" || candidate.ProxyMemberProfileID != "" || candidate.ProxyProfileID != "w-direct-profile" {
		t.Fatalf("直接代理候选字段必须保持既有语义: %+v", candidate)
	}
	input, err := runtime.buildQueryInput(ctx, candidate, opsjobs.BalanceQueryConfig{Adapter: "builtin"})
	if err != nil {
		t.Fatalf("buildQueryInput: %v", err)
	}
	if input.ProxyProfileID != "w-direct-profile" {
		t.Fatalf("直接代理身份摘要槽必须保持配置档案 ID，得到 %q", input.ProxyProfileID)
	}
	if input.Proxy == nil {
		t.Fatal("直接代理候选必须保持既有出站信封")
	}
	plaintext, err := accountbalance.DecryptV1Envelope(runtime.secret, input.Proxy.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envelope.URL, "http://direct-user:direct-pass@direct.example:8080") {
		t.Fatalf("直接代理出站信封必须保持既有语义，得到 %s", envelope.URL)
	}
}
