package proberepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
)

const testSecret = "0123456789abcdef0123456789abcdef"

var testNow = time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)

func nowMillisText() string { return testNow.UTC().Format(rfc3339Milli) }

func plusMillis(ms int64) string {
	return testNow.Add(time.Duration(ms) * time.Millisecond).UTC().Format(rfc3339Milli)
}

type testDB struct {
	store *Store
	db    *sql.DB
}

// openTestDB（SQLite 夹具）与其专属 seedSchema DDL 已于 2026-10-25 随 sqlite
// 方言退役整体删除；全部存储行为测试统一走 openTestDBPostgres（pgtestkit 真库
// + 生产 PG 方言）。seed/校验 helper 按生产方言生成 SQL（juhe_business 表限定
// + bind $n），时间列断言一律按 RFC3339 文本契约（store.go timeParam 口径）。

func (h *testDB) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.db.Exec(query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

// seedModel 写入一条全局模型并绑定到账户支持模型集合，返回模型 ID。
func (h *testDB) seedModel(t *testing.T, accountID, name string) string {
	t.Helper()
	modelID := "model-" + name
	h.exec(t, h.store.bind(`INSERT INTO `+h.store.table("models")+` (id, vendor_code, public_model, upstream_model, scope, status, created_at)
	    VALUES (?, 'gpt', ?, ?, 'global', 'active', ?) ON CONFLICT (id) DO NOTHING`),
		modelID, name, name, nowMillisText())
	h.exec(t, h.store.bind(`INSERT INTO `+h.store.table("account_supported_models")+` (account_id, model_id, created_at) VALUES (?, ?, ?)
	    ON CONFLICT (account_id, model_id) DO NOTHING`), accountID, modelID, nowMillisText())
	return modelID
}

// seedProbeConnection 写入账户的探针连接面（health-check 优先语义下的唯一
// 连接行）并声明 endpoint modes，同时补齐账户 health_check_connection_id。
func (h *testDB) seedProbeConnection(t *testing.T, accountID, preset, baseURL string, modes ...string) string {
	t.Helper()
	connectionID := "conn-" + accountID
	h.exec(t, h.store.bind(`INSERT INTO `+h.store.table("account_connections")+` (id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
	    VALUES (?, ?, ?, 'openai_v1', ?, 1, ?, ?) ON CONFLICT (id) DO NOTHING`),
		connectionID, accountID, preset, baseURL, nowMillisText(), nowMillisText())
	for _, mode := range modes {
		h.exec(t, h.store.bind(`INSERT INTO `+h.store.table("account_endpoint_modes")+` (connection_id, endpoint_mode) VALUES (?, ?)
		    ON CONFLICT (connection_id, endpoint_mode) DO NOTHING`), connectionID, mode)
	}
	h.exec(t, h.store.bind(`UPDATE `+h.store.table("accounts")+` SET health_check_connection_id = ? WHERE id = ?`), connectionID, accountID)
	return connectionID
}

func (h *testDB) sealCredentials(t *testing.T, credentials map[string]any) string {
	t.Helper()
	envelope, err := accountcrypto.EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// seedPoolAccount 写入一个双 Key 池账户（gpt api_key、active、可调度、绑定
// 分组，连接面 gpt_openai_v1 + chat 双模式）。connection_driver_code/
// protocol_code/protocol_version/created_at 为生产 NOT NULL 无默认列，两种
// 夹具统一补齐。
func (h *testDB) seedPoolAccount(t *testing.T, accountID string) (key1, key2 string) {
	t.Helper()
	key1, key2 = "sk-key-one", "sk-key-two"
	credentials := map[string]any{
		"api_keys":              []any{key1, key2},
		"quota_recovery_policy": map[string]any{"windowHours": float64(24)},
	}
	h.exec(t, h.store.bind(`
    INSERT INTO `+h.store.table("accounts")+` (id, system_account_id, name, type, status, schedulable, vendor_code, connection_driver_code, protocol_code, protocol_version, health_check_model, health_check_endpoint_mode, credentials_encrypted, dispatch_revision, created_at, updated_at)
    VALUES (?, 'sys-1', '池账户', 'api_key', 'active', 1, 'gpt', 'gpt', 'openai', 'v1', 'gpt-test', 'chat_json', ?, 3, ?, ?)`),
		accountID, h.sealCredentials(t, credentials), nowMillisText(), nowMillisText())
	h.seedModel(t, accountID, "gpt-test")
	h.seedProbeConnection(t, accountID, "gpt_openai_v1", "https://upstream.example.com/v1", "chat_json", "chat_sse")
	h.exec(t, h.store.bind(`INSERT INTO `+h.store.table("groups")+` (id, system_account_id, name, enabled, created_at, updated_at) VALUES ('group-1', 'sys-1', 'g', 1, ?, ?)`), nowMillisText(), nowMillisText())
	h.exec(t, h.store.bind(`INSERT INTO `+h.store.table("group_accounts")+` (group_id, system_account_id, account_id, enabled, created_at, updated_at) VALUES ('group-1', 'sys-1', ?, 1, ?, ?)`), accountID, nowMillisText(), nowMillisText())
	return key1, key2
}

func (h *testDB) seedRuntimeState(t *testing.T, accountID, fingerprint string, keyIndex int, status, nextProbeAt string) {
	t.Helper()
	h.exec(t, h.store.bind(`
    INSERT INTO `+h.store.table("account_api_key_runtime_states")+` (
      id, system_account_id, account_id, key_fingerprint, key_index, status, next_probe_at, created_at, updated_at
    ) VALUES (?, 'sys-1', ?, ?, ?, ?, ?, ?, ?)`),
		"state-"+fingerprint, accountID, fingerprint, keyIndex, status, nextProbeAt, nowMillisText(), nowMillisText())
}

// TestListDueForProbeClaimsAndSkipsFreshLeases 验证到期候选 claim（W27① 修复
// 后的 PG 生产行为）：到期候选非空且命中后写入 claim 租约；未到期候选与租约
// 未过期的候选不得返回。
func TestListDueForProbeClaimsAndSkipsFreshLeases(t *testing.T) {
	h := openTestDBPostgres(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	// 未到期候选（next_probe_at 在未来）：不得入选。
	fpFuture := h.store.FingerprintAPIKey("sk-key-future")
	h.seedRuntimeState(t, "acc-1", fpFuture, 1, "unverified", plusMillis(60_000))

	candidates, err := h.store.ListDueForProbe(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("到期候选必须恰为 1（未到期不入选）: %d %+v", len(candidates), candidates)
	}
	candidate := candidates[0]
	if candidate.AccountID != "acc-1" || candidate.APIKey != key1 || candidate.KeyFingerprint != fp1 {
		t.Fatalf("candidate=%+v", candidate)
	}
	if candidate.ProbeClaimToken == "" || candidate.ProbeClaimedUntil == "" {
		t.Fatalf("claim 未写入: %+v", candidate)
	}
	if candidate.AccountConfigRevision != 1 {
		t.Fatalf("configRevision=%d", candidate.AccountConfigRevision)
	}

	// 已领取且租约未到期的候选，下一轮扫描不得重复返回。
	repeat, err := h.store.ListDueForProbe(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat) != 0 {
		t.Fatalf("fresh lease 必须被排除: %+v", repeat)
	}
}

// TestCooldownMutationsFlow 验证 record success / failure / defer 的 CAS 与
// 状态迁移语义。
func TestCooldownMutationsFlow(t *testing.T) {
	h := openTestDBPostgres(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	ctx := context.Background()

	// 错误的 expected status → stale_probe_state。
	result, err := h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID:      "acc-1",
		KeyFingerprint: fp1,
		KeyIndex:       0,
		TrafficSource:  "cooldown_retest",
		ProbeOutcome:   "complete_success",
		ObservedAt:     nowMillisText(),
		Expected: accountquality.KeyMutationExpected{
			Status: "rate_limited", NextProbeAt: plusMillis(-1000), StateUpdatedAt: plusMillis(0), ProbeClaimToken: "t",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("fence 不匹配不得 changed")
	}

	// 命中 fence → 恢复 active。
	result, err = h.store.RecordKeySuccess(ctx, accountquality.KeySuccessInput{
		AccountID:      "acc-1",
		KeyFingerprint: fp1,
		KeyIndex:       0,
		TrafficSource:  "cooldown_retest",
		ProbeOutcome:   "complete_success",
		ObservedAt:     nowMillisText(),
		Expected: accountquality.KeyMutationExpected{
			Status: "temporary_unavailable", NextProbeAt: plusMillis(-1000), StateUpdatedAt: plusMillis(0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("恢复写入失败: %+v", result)
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM juhe_business.account_api_key_runtime_states WHERE account_id='acc-1' AND key_fingerprint=$1`, fp1).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("status=%q", status)
	}

	// 推进时钟：last_attempt_at 幂等围栏要求观测时间严格晚于上次写入。
	testNow = testNow.Add(2 * time.Second)

	// 失败：先落一次普通上游失败（last_error_code=http_403），避免 Node 同款
	// generic 守卫在 last_error_code 为 NULL 时的三值逻辑过滤。
	result, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID:      "acc-1",
		KeyFingerprint: fp1,
		KeyIndex:       0,
		TrafficSource:  "cooldown_retest",
		ProbeOutcome:   "upstream_failure",
		Status:         "temporary_unavailable",
		StatusCode:     403,
		ObservedAt:     nowMillisText(),
		Expected: accountquality.KeyMutationExpected{
			Status: "active", StateUpdatedAt: plusMillis(-2000),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("普通失败写入未生效: %+v", result)
	}

	// 再次推进时钟（幂等围栏要求严格晚于上次 last_attempt_at）。
	testNow = testNow.Add(2 * time.Second)

	// 失败：generic 额度模式 → rate_limited + cooldown + recovery_started_at。
	result, err = h.store.RecordKeyFailure(ctx, accountquality.KeyFailureInput{
		AccountID:         "acc-1",
		KeyFingerprint:    fp1,
		KeyIndex:          0,
		TrafficSource:     "cooldown_retest",
		ProbeOutcome:      "upstream_failure",
		QuotaRecoveryMode: "generic",
		Status:            "rate_limited",
		StatusCode:        403,
		ErrorCode:         "insufficient_quota",
		ErrorMessage:      "上游额度不足",
		CooldownUntil:     plusMillis(3600_000),
		ObservedAt:        nowMillisText(),
		Expected: accountquality.KeyMutationExpected{
			Status: "temporary_unavailable", StateUpdatedAt: plusMillis(-2000),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("失败写入未生效: %+v", result)
	}
	var (
		failureStatus   string
		cooldownUntil   sql.NullString
		recoveryStarted sql.NullString
		nextProbeAt     sql.NullString
		backoffSeconds  int
	)
	if err := h.db.QueryRow(`
      SELECT status, cooldown_until, recovery_started_at, next_probe_at, probe_backoff_seconds
      FROM juhe_business.account_api_key_runtime_states WHERE account_id='acc-1' AND key_fingerprint=$1`, fp1).
		Scan(&failureStatus, &cooldownUntil, &recoveryStarted, &nextProbeAt, &backoffSeconds); err != nil {
		t.Fatal(err)
	}
	if failureStatus != "rate_limited" {
		t.Fatalf("failure status=%q", failureStatus)
	}
	if !cooldownUntil.Valid || cooldownUntil.String != plusMillis(3600_000) {
		t.Fatalf("cooldown_until=%v", cooldownUntil)
	}
	if !recoveryStarted.Valid || recoveryStarted.String != nowMillisText() {
		t.Fatalf("recovery_started_at=%v", recoveryStarted)
	}
	if !nextProbeAt.Valid || nextProbeAt.String == "" {
		t.Fatal("next_probe_at 必须写入")
	}
	// 第一次普通失败已把 backoff 提到 3，generic 失败翻倍为 6（min(3600, 3*2)）。
	if backoffSeconds != 2*initialProbeBackoffSeconds {
		t.Fatalf("backoff=%d", backoffSeconds)
	}

	// 再次推进时钟，然后 defer：breakQuotaRecoveryWindow 清空 recovery_started_at。
	testNow = testNow.Add(2 * time.Second)
	result, err = h.store.DeferKeyProbe(ctx, accountquality.KeyDeferInput{
		AccountID:                "acc-1",
		KeyFingerprint:           fp1,
		KeyIndex:                 0,
		TrafficSource:            "cooldown_retest",
		ProbeOutcome:             "upstream_failure",
		DelaySeconds:             60,
		BreakQuotaRecoveryWindow: true,
		ObservedAt:               nowMillisText(),
		Expected: accountquality.KeyMutationExpected{
			Status: failureStatus, NextProbeAt: nextProbeAt.String, StateUpdatedAt: plusMillis(-2000),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("defer 未生效: %+v", result)
	}
	if err := h.db.QueryRow(`
      SELECT recovery_started_at FROM juhe_business.account_api_key_runtime_states
      WHERE account_id='acc-1' AND key_fingerprint=$1`, fp1).Scan(&recoveryStarted); err != nil {
		t.Fatal(err)
	}
	if recoveryStarted.Valid && recoveryStarted.String != "" {
		t.Fatalf("breakWindow 必须清空 recovery_started_at: %v", recoveryStarted)
	}
}

// TestMarkPrecheckTemporaryUnavailableFenceOrder 验证 skipReason 的判定顺序。
func TestMarkPrecheckTemporaryUnavailableFenceOrder(t *testing.T) {
	h := openTestDBPostgres(t)
	h.seedPoolAccount(t, "acc-1")
	ctx := context.Background()

	// dispatch_revision 不匹配。
	result, err := h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 99, ExpectedStatus: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SkippedReason != "stale_dispatch_revision" {
		t.Fatalf("reason=%q", result.SkippedReason)
	}

	// status 不匹配。
	result, err = h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "rate_limited",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SkippedReason != "stale_account_status" {
		t.Fatalf("reason=%q", result.SkippedReason)
	}

	// 更新的健康成功 → newer_health_success。
	h.exec(t, `UPDATE juhe_business.accounts SET last_health_success_at = $1 WHERE id='acc-1'`, plusMillis(5000))
	result, err = h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SkippedReason != "newer_health_success" {
		t.Fatalf("reason=%q", result.SkippedReason)
	}

	// fence 全部满足 → 更新成功且 dispatch_revision 前移。
	// 先把 updated_at 钉到过去值，成功标记后必须被重写为 RFC3339 文本
	// （W27②：text 时间列写入 Go time.String() 形态会让后续 instantMS
	// 解析报 invalid_runtime_state）。
	h.exec(t, `UPDATE juhe_business.accounts SET last_health_success_at = NULL, updated_at = $1 WHERE id='acc-1'`, plusMillis(-5000))
	result, err = h.store.MarkPrecheckTemporaryUnavailable(ctx, accountquality.PrecheckMutationInput{
		AccountID: "acc-1", Reason: "r", PrecheckStartedAt: nowMillisText(),
		ExpectedDispatchRevision: 3, ExpectedStatus: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated {
		t.Fatalf("expected updated: %+v", result)
	}
	var dispatchRevision int64
	if err := h.db.QueryRow(`SELECT dispatch_revision FROM juhe_business.accounts WHERE id='acc-1'`).Scan(&dispatchRevision); err != nil {
		t.Fatal(err)
	}
	if dispatchRevision != 4 {
		t.Fatalf("dispatchRevision=%d", dispatchRevision)
	}
	// updated_at 必须为注入时钟的 RFC3339 毫秒文本（text 时间列写入口径）。
	var updatedAt string
	if err := h.db.QueryRow(`SELECT updated_at FROM juhe_business.accounts WHERE id='acc-1'`).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		t.Fatalf("updated_at 必须是 RFC3339 文本: %q %v", updatedAt, err)
	}
	if updatedAt != nowMillisText() {
		t.Fatalf("updated_at=%q（必须为注入时钟的 RFC3339 毫秒形态）", updatedAt)
	}

	// 成功标记必须写入冷却恢复锚点（BUG-0282）：缺失会把 temporary_unavailable
	// 账户永久隔离在 J1 候选之外，无法自动恢复。
	var cooldownUntil, observationStartedAt, generation, lastTraceID sql.NullString
	var schedulable int
	if err := h.db.QueryRow(`SELECT cooldown_until, cooldown_retest_observation_started_at, cooldown_retest_generation, last_error_trace_id, schedulable FROM juhe_business.accounts WHERE id='acc-1'`).
		Scan(&cooldownUntil, &observationStartedAt, &generation, &lastTraceID, &schedulable); err != nil {
		t.Fatal(err)
	}
	if !cooldownUntil.Valid || cooldownUntil.String == "" {
		t.Fatal("标记后必须写入 cooldown_until 冷却锚点")
	}
	if !observationStartedAt.Valid || observationStartedAt.String == "" {
		t.Fatal("标记后必须写入冷却复测观察起点")
	}
	if !generation.Valid || !strings.HasPrefix(generation.String, "cooldown:") {
		t.Fatalf("标记后必须写入冷却复测 generation: %q", generation.String)
	}
	if lastTraceID.Valid && lastTraceID.String != "" {
		t.Fatalf("precheck 无 trace，必须清空 last_error_trace_id: %q", lastTraceID.String)
	}
	if schedulable != 1 {
		t.Fatalf("标记后 schedulable 必须保持可调度: %d", schedulable)
	}
}

// TestFindAccountForTestAvailability 验证 effectiveAvailability 的 DB 分支。
func TestFindAccountForTestAvailability(t *testing.T) {
	h := openTestDBPostgres(t)
	key1, key2 := h.seedPoolAccount(t, "acc-1")
	view, err := h.store.LoadAccountForTest(context.Background(), "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || !view.EffectiveAvailable {
		t.Fatalf("active 账户必须可用: %+v", view)
	}
	if view.BoundGroupID != "group-1" || view.OwnerSystemAccountID != "sys-1" {
		t.Fatalf("绑定/归属错误: %+v", view.AccountForTest)
	}
	if len(view.SupportedModels) != 1 || view.SupportedModels[0] != "gpt-test" {
		t.Fatalf("supportedModels=%v", view.SupportedModels)
	}

	// 冷却中 → 不可用。
	h.exec(t, `UPDATE juhe_business.accounts SET cooldown_until = $1 WHERE id='acc-1'`, plusMillis(60_000))
	view, err = h.store.LoadAccountForTest(context.Background(), "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.EffectiveAvailable {
		t.Fatal("冷却中必须不可用")
	}
	h.exec(t, `UPDATE juhe_business.accounts SET cooldown_until = NULL WHERE id='acc-1'`)

	// 全部 Key 不可用 → api_key_pool_unavailable。
	fp1 := h.store.FingerprintAPIKey(key1)
	fp2 := h.store.FingerprintAPIKey(key2)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))
	h.seedRuntimeState(t, "acc-1", fp2, 1, "rate_limited", plusMillis(-1000))
	view, err = h.store.LoadAccountForTest(context.Background(), "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.EffectiveAvailable || view.EffectiveAvailabilityStatus != "api_key_pool_unavailable" {
		t.Fatalf("pool 不可用必须阻断: %+v", view.EffectiveAvailabilityStatus)
	}
}

// TestFindAccountForGroupAndHasAPIKeyEntry 验证分组候选解析与 Key 池校验。
func TestFindAccountForGroupAndHasAPIKeyEntry(t *testing.T) {
	h := openTestDBPostgres(t)
	key1, key2 := h.seedPoolAccount(t, "acc-1")
	ctx := context.Background()
	candidate, err := h.store.FindAccountForGroup(ctx, "group-1", "acc-1", "sys-1")
	if err != nil {
		t.Fatal(err)
	}
	if candidate == nil {
		t.Fatal("候选不得为空")
	}
	if candidate.Type != "api_key" || candidate.DispatchRevision != 3 || !candidate.HasDispatchRevision {
		t.Fatalf("candidate=%+v", candidate)
	}

	full, err := h.store.LoadAccountForGroup(ctx, "group-1", "acc-1", "sys-1")
	if err != nil {
		t.Fatal(err)
	}
	if full.BaseURL != "https://upstream.example.com/v1" {
		t.Fatalf("base_url=%q", full.BaseURL)
	}
	if len(full.APIKeyEntries) != 2 || full.APIKeyEntries[0].Key != key1 || full.APIKeyEntries[1].Key != key2 {
		t.Fatalf("entries=%+v", full.APIKeyEntries)
	}

	has, err := h.store.HasAPIKeyEntry(ctx, candidate, h.store.FingerprintAPIKey(key1), key1)
	if err != nil || !has {
		t.Fatalf("HasAPIKeyEntry=%v err=%v", has, err)
	}
	has, err = h.store.HasAPIKeyEntry(ctx, candidate, h.store.FingerprintAPIKey("sk-rotated"), "sk-rotated")
	if err != nil || has {
		t.Fatalf("轮换后的 Key 不得命中: %v %v", has, err)
	}

	// 未绑定该分组的访问者 → nil。
	missing, err := h.store.FindAccountForGroup(ctx, "group-1", "acc-1", "sys-other")
	if err != nil || missing != nil {
		t.Fatalf("非授权访问者必须为 nil: %v %v", missing, err)
	}
}

// TestLoadProbeViewFixedKey 验证探针视图组装与固定 Key 注入。
func TestLoadProbeViewFixedKey(t *testing.T) {
	h := openTestDBPostgres(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	view, err := h.store.LoadProbeView(context.Background(), accountquality.ProbeRequest{
		AccountID: "acc-1", GroupID: "group-1", SystemAccountID: "sys-1",
		TrafficSource: "cooldown_retest", Full: false,
		FixedAPIKey: key1, FixedKeyFingerprint: fp1, FixedKeyIndex: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view == nil {
		t.Fatal("view 不得为空")
	}
	if view.FixedKey == nil || view.FixedKey.Key != key1 || view.HealthCheckModel != "gpt-test" {
		t.Fatalf("view=%+v", view)
	}
	if view.BaseURL == "" || len(view.APIKeyEntries) != 2 {
		t.Fatalf("view 凭据缺失: %+v", view)
	}
	if !view.NormalizeEndpointModes[accountprobe.ModeChatJSON] {
		t.Fatalf("endpoint modes=%v", view.NormalizeEndpointModes)
	}
	policyJSON, _ := json.Marshal(view.QuotaRecoveryPolicy)
	if !strings.Contains(string(policyJSON), "windowHours") {
		t.Fatalf("quota policy=%s", policyJSON)
	}
}

// TestLoadProbeViewConnectionSurface 钉住供应商解耦后的探针视图连接面事实：
// BaseURL 来自资源账户健康检查连接行（凭据 JSON 不再承载）、profile 由
// connection_preset_code 派生、endpoint modes 来自 account_endpoint_modes。
func TestLoadProbeViewConnectionSurface(t *testing.T) {
	h := openTestDBPostgres(t)
	h.seedPoolAccount(t, "conn-1")
	view, err := h.store.LoadProbeView(context.Background(), accountquality.ProbeRequest{
		AccountID: "conn-1", GroupID: "group-1", SystemAccountID: "sys-1", Full: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view == nil {
		t.Fatal("view 不得为空")
	}
	if view.BaseURL != "https://upstream.example.com/v1" {
		t.Fatalf("base_url=%q（必须来自连接行）", view.BaseURL)
	}
	if view.ProviderProtocolProfileID != "profile_gpt_openai_v1" {
		t.Fatalf("profile=%q（必须由 preset 派生）", view.ProviderProtocolProfileID)
	}
	if view.ProviderCode != "gpt" {
		t.Fatalf("provider=%q（必须投影 vendor_code）", view.ProviderCode)
	}
	if !view.NormalizeEndpointModes[accountprobe.ModeChatJSON] || !view.NormalizeEndpointModes[accountprobe.ModeChatSSE] {
		t.Fatalf("endpoint modes=%v（必须来自 account_endpoint_modes）", view.NormalizeEndpointModes)
	}
}
