package proberepo

// w12h 补充 arms：precheck/候选扫描/Key 变更的跳过原因与错误传播、
// claim CAS 竞争臂，以及凭据条目去重、Store 默认时钟。
// 本文件补充登记的不可达语句（modernc 驱动层无注入点 / 需真实 PG）：
//   - mutation.go MarkPrecheckTemporaryUnavailable 变更后 changed==0 的
//     stale_dispatch_revision 返回：UPDATE 条件与 loadPrecheckState 门禁一致，
//     仅剩 SELECT/UPDATE 间并发窗口（TOCTOU），单连接 SQLite 无法确定性触发；
//   - mutation.go RecordKeyFailure 的 s.postgres genericGuard 分支（仅 PG 生效）；
//   - RowsAffected / rows.Err 兜底分支（驱动对合法 SQL 不产生错误）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

// precheck：updated_at 非法时间命中 invalid_runtime_state（健康时间臂由 w7c 覆盖）。
func TestW12HPrecheckInvalidUpdatedAtArm(t *testing.T) {
	h := openTestDB(t)
	h.seedPoolAccount(t, "acc-1")
	h.exec(t, `UPDATE accounts SET updated_at='garbage', last_used_at='2020-01-01T00:00:00.000Z' WHERE id='acc-1'`)
	result, err := h.store.MarkPrecheckTemporaryUnavailable(context.Background(), accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err != nil || result.Updated || result.SkippedReason != "invalid_runtime_state" {
		t.Fatalf("非法 updated_at 必须跳过: %+v %v", result, err)
	}
}

// precheck：UPDATE 执行错误必须原样传播。
func TestW12HPrecheckExecErrorArm(t *testing.T) {
	h := openTestDB(t)
	h.seedPoolAccount(t, "acc-1")
	h.exec(t, `CREATE TRIGGER w12h_abort_acc BEFORE UPDATE ON accounts BEGIN SELECT RAISE(ABORT, 'w12h acc abort'); END`)
	_, err := h.store.MarkPrecheckTemporaryUnavailable(context.Background(), accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err == nil || !strings.Contains(err.Error(), "w12h acc abort") {
		t.Fatalf("写中止必须失败: %v", err)
	}
}

// ListDueForProbe：limit 上限钳制、config_revision<1 跳过、扫描错误传播。
// 每个检查独立建库：limit 钳制检查会写入 claim 租约，租约未到期会把行从
// 后续 SELECT 中排除。
func TestW12HListDueForProbeArms(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) *testDB {
		t.Helper()
		h := openTestDB(t)
		key1, _ := h.seedPoolAccount(t, "acc-1")
		fp1 := h.store.FingerprintAPIKey(key1)
		h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
		return h
	}

	// limit>100 收敛到 100，正常返回到期候选。
	h := seed(t)
	candidates, err := h.store.ListDueForProbe(ctx, 500)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("limit 钳制后应返回 1 个候选: %d %v", len(candidates), err)
	}

	// config_revision<1 的候选必须被跳过。
	h = seed(t)
	h.exec(t, `UPDATE accounts SET config_revision=0 WHERE id='acc-1'`)
	if candidates, err = h.store.ListDueForProbe(ctx, 10); err != nil || len(candidates) != 0 {
		t.Fatalf("config_revision<1 必须跳过: %d %v", len(candidates), err)
	}

	// key_index 存入非整数文本命中 Scan 错误传播。
	h = seed(t)
	h.exec(t, `UPDATE account_api_key_runtime_states SET key_index='garbage' WHERE account_id='acc-1'`)
	if _, err = h.store.ListDueForProbe(ctx, 10); err == nil {
		t.Fatal("非法 key_index 必须传播 Scan 错误")
	}
}

// claimProbeCandidates 直调：时间解析守卫、limit=0 立即截断、CAS 竞争失败丢弃、写中止传播。
func TestW12HClaimProbeCandidatesArms(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	now := nowMillisText()

	// 内部 now 格式固定，直调坏时间命中解析守卫。
	if _, err := h.store.claimProbeCandidates(ctx, nil, 1, "garbage"); err == nil {
		t.Fatal("坏时间必须失败")
	}
	// limit=0：首个候选即截断。
	claimed, err := h.store.claimProbeCandidates(ctx,
		[]accountquality.CooldownProbeCandidate{{AccountID: "acc-1", KeyFingerprint: fp1}}, 0, now)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("limit=0 必须为空: %d %v", len(claimed), err)
	}
	// CAS 不命中（指纹不存在）→ changed==0 → 丢弃。
	claimed, err = h.store.claimProbeCandidates(ctx,
		[]accountquality.CooldownProbeCandidate{{AccountID: "acc-1", KeyFingerprint: "fp-none", Status: "unverified", NextProbeAt: plusMillis(-1000)}}, 5, now)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("CAS 未命中必须丢弃: %d %v", len(claimed), err)
	}
	// 写中止必须传播。
	h.exec(t, `CREATE TRIGGER w12h_abort_claim BEFORE UPDATE ON account_api_key_runtime_states BEGIN SELECT RAISE(ABORT, 'w12h claim abort'); END`)
	_, err = h.store.claimProbeCandidates(ctx,
		[]accountquality.CooldownProbeCandidate{{AccountID: "acc-1", KeyFingerprint: fp1, Status: "temporary_unavailable", NextProbeAt: plusMillis(-1000)}}, 5, now)
	if err == nil || !strings.Contains(err.Error(), "w12h claim abort") {
		t.Fatalf("claim 写中止必须失败: %v", err)
	}
}

// Key 变更：UPDATE 触发器中止各写路径，INSERT 触发器中止失败插入路径。
func TestW12HKeyMutationExecErrorArms(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	h.exec(t, `CREATE TRIGGER w12h_abort_rs BEFORE UPDATE ON account_api_key_runtime_states BEGIN SELECT RAISE(ABORT, 'w12h rs abort'); END`)

	// 成功 + 围栏 → UPDATE 命中 → 中止。
	if _, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{Status: "temporary_unavailable", NextProbeAt: plusMillis(-1000), StateUpdatedAt: plusMillis(0)},
	}); err == nil || !strings.Contains(err.Error(), "w12h rs abort") {
		t.Fatalf("成功写中止必须失败: %v", err)
	}
	// 成功无围栏 → upsert DO UPDATE → 中止。
	if _, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
	}); err == nil || !strings.Contains(err.Error(), "w12h rs abort") {
		t.Fatalf("upsert 写中止必须失败: %v", err)
	}
	// 失败已有行 → UPDATE 命中 → 中止。
	if _, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		StatusCode: 500, ObservedAt: nowMillisText(),
	}); err == nil || !strings.Contains(err.Error(), "w12h rs abort") {
		t.Fatalf("失败写中止必须失败: %v", err)
	}
	// defer → UPDATE 命中 → 中止。
	if _, err := h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		DelaySeconds: 60, ObservedAt: nowMillisText(),
		Expected: accountquality.KeyMutationExpected{NextProbeAt: plusMillis(-1000)},
	}); err == nil || !strings.Contains(err.Error(), "w12h rs abort") {
		t.Fatalf("defer 写中止必须失败: %v", err)
	}

	// 新指纹 + BEFORE INSERT 触发器 → 失败插入路径中止。
	h.exec(t, `CREATE TRIGGER w12h_abort_ri BEFORE INSERT ON account_api_key_runtime_states BEGIN SELECT RAISE(ABORT, 'w12h ri abort'); END`)
	freshKey := "sk-w12h-fresh"
	h.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-1'`,
		h.sealCredentials(t, map[string]any{"base_url": "https://upstream.example.com/v1", "api_keys": []any{key1, freshKey}}))
	if _, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: h.store.FingerprintAPIKey(freshKey), TrafficSource: "cooldown_retest",
		StatusCode: 503, ObservedAt: nowMillisText(),
	}); err == nil || !strings.Contains(err.Error(), "w12h ri abort") {
		t.Fatalf("插入写中止必须失败: %v", err)
	}
}

// Key 变更跳过与解析臂：非池内指纹、generic 恢复码、config 围栏、runtime 行读取错误。
func TestW12HKeyMutationSkipArms(t *testing.T) {
	ctx := context.Background()
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))

	// 指纹不在当前凭据池 → 无目标。
	result, err := h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: "fp-not-in-pool", TrafficSource: "cooldown_retest", StatusCode: 500,
	})
	if err != nil || result.Changed {
		t.Fatalf("池外指纹必须无目标: %+v %v", result, err)
	}
	result, err = h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{
		AccountID: "acc-1", KeyFingerprint: "fp-not-in-pool", TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{NextProbeAt: plusMillis(-1000)},
	})
	if err != nil || result.Changed {
		t.Fatalf("池外指纹 defer 必须无目标: %+v %v", result, err)
	}

	// generic 恢复模式且未给错误码 → 采用通用配额错误码。
	result, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		QuotaRecoveryMode: "generic", StatusCode: 429, ObservedAt: nowMillisText(),
	})
	if err != nil || !result.Changed {
		t.Fatalf("generic 失败必须生效: %+v %v", result, err)
	}
	var code string
	if err := h.db.QueryRow(`SELECT last_error_code FROM account_api_key_runtime_states WHERE key_fingerprint = ?`,
		fp1).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if code != accountquality.QuotaRecoveryGenericErrorCode {
		t.Fatalf("generic 错误码不符: %s", code)
	}

	// config_revision 围栏 ≥1 → fence.provided 置位；版本不匹配 → 未变更。
	result, err = h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest",
		Expected: accountquality.KeyMutationExpected{AccountConfigRevision: 7},
	})
	if err != nil || result.Changed {
		t.Fatalf("config 围栏不匹配必须未变更: %+v %v", result, err)
	}

	// runtime 表缺失 → loadRuntimeRow 错误原样传播。
	h.exec(t, `DROP TABLE account_api_key_runtime_states`)
	if _, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID: "acc-1", KeyFingerprint: fp1, TrafficSource: "cooldown_retest", StatusCode: 500,
	}); err == nil {
		t.Fatal("runtime 表缺失必须失败")
	}
}

// 抖动偏移零值臂：window=1 时 offset∈{-1,0,1}，offset==0 的兜底以约 1/3
// 概率命中，循环调用即可确定性覆盖。
func TestW12HJitterZeroOffsetArms(t *testing.T) {
	// passiveScheduleDelayMS：interval=2ms → window=1。
	for i := 0; i < 300; i++ {
		delay := passiveScheduleDelayMS(2)
		if delay != 1 && delay != 3 {
			t.Fatalf("interval=2ms 的 delay 只能是 1 或 3: %d", delay)
		}
	}
	// passiveProbeNotBeforeAt：deadline=now+2ms → window=1。
	for i := 0; i < 300; i++ {
		got := passiveProbeNotBeforeAt(plusMillis(2), func() time.Time { return testNow })
		want := plusMillis(2)
		if got != want && got != plusMillis(3) && got != plusMillis(4) {
			t.Fatalf("deadline+2ms 的返回只能是 +1ms/+2ms/原值: %q", got)
		}
	}
}

// 凭据条目：空白与重复 key 去重；Store 缺省时钟。
func TestW12HCredentialEntryDedupAndDefaultClock(t *testing.T) {
	h := openTestDB(t)
	entries := h.store.AccountAPIKeyEntries(map[string]any{
		"api_keys": []any{"  ", "dup", "dup"},
	})
	if len(entries) != 1 || entries[0].Key != "dup" {
		t.Fatalf("空白与重复 key 必须去重: %+v", entries)
	}
	// NewStore 未提供 Now 时回退系统时钟。
	store, err := NewStore(Config{DB: h.db, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.now(); got.Before(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("缺省时钟必须为当前系统时间: %v", got)
	}
}
