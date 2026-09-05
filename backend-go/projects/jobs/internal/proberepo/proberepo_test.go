package proberepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
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

func openTestDB(t *testing.T) *testDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(Config{DB: db, Postgres: false, Secret: testSecret, Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return &testDB{store: store, db: db}
}

func (h *testDB) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.db.Exec(query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func (h *testDB) seedSchema(t *testing.T) {
	t.Helper()
	h.exec(t, `
    CREATE TABLE accounts (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL DEFAULT '',
      type TEXT NOT NULL,
      status TEXT NOT NULL,
      schedulable INTEGER NOT NULL DEFAULT 1,
      provider_code TEXT NOT NULL DEFAULT 'openai',
      provider_protocol_profile_id TEXT,
      protocol_code TEXT,
      protocol_version TEXT,
      client_compatibility TEXT,
      health_check_model TEXT NOT NULL DEFAULT '',
      health_check_endpoint_mode TEXT NOT NULL DEFAULT '',
      account_expires_at TEXT,
      cooldown_until TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      credentials_encrypted TEXT NOT NULL DEFAULT '{}',
      authorization_instance_authorization_id TEXT,
      authorization_instance_source_account_id TEXT,
      authorization_instance_owner_system_account_id TEXT,
      config_revision INTEGER NOT NULL DEFAULT 1,
      dispatch_revision INTEGER NOT NULL DEFAULT 1,
      last_health_success_at TEXT,
      last_used_at TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_generation TEXT,
      cooldown_retest_last_at TEXT,
      cooldown_retest_last_status_code INTEGER,
      stream_failure_count INTEGER NOT NULL DEFAULT 0,
      stream_failure_window_started_at TEXT,
      deleted_at TEXT,
      updated_at TEXT NOT NULL DEFAULT ''
    )`)
	h.exec(t, `
    CREATE TABLE account_supported_models (
      account_id TEXT NOT NULL,
      provider_code TEXT,
      model TEXT NOT NULL,
      created_at TEXT NOT NULL DEFAULT ''
    )`)
	h.exec(t, `
    CREATE TABLE account_api_key_runtime_states (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      key_fingerprint TEXT NOT NULL,
      key_index INTEGER NOT NULL DEFAULT 0,
      status TEXT NOT NULL,
      failure_count INTEGER NOT NULL DEFAULT 0,
      consecutive_failures INTEGER NOT NULL DEFAULT 0,
      success_count INTEGER NOT NULL DEFAULT 0,
      cooldown_until TEXT,
      next_probe_at TEXT,
      probe_backoff_seconds INTEGER NOT NULL DEFAULT 0,
      recovery_started_at TEXT,
      last_attempt_at TEXT,
      last_success_at TEXT,
      last_failure_at TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      last_trace_id TEXT,
      last_probe_at TEXT,
      probe_claim_token TEXT,
      probe_claimed_until TEXT,
      created_at TEXT NOT NULL DEFAULT '',
      updated_at TEXT NOT NULL DEFAULT '',
      UNIQUE(account_id, key_fingerprint)
    )`)
	h.exec(t, `
    CREATE TABLE groups (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      provider_code TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      group_type TEXT,
      scheduling_policy_json TEXT
    )`)
	h.exec(t, `
    CREATE TABLE group_accounts (
      group_id TEXT NOT NULL,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_authorization_id TEXT,
      local_priority INTEGER NOT NULL DEFAULT 0,
      local_super_priority_enabled INTEGER NOT NULL DEFAULT 0,
      local_fallback_enabled INTEGER NOT NULL DEFAULT 0,
      enabled INTEGER NOT NULL DEFAULT 1,
      updated_at TEXT NOT NULL DEFAULT ''
    )`)
	h.exec(t, `
    CREATE TABLE resource_authorizations (
      id TEXT PRIMARY KEY,
      resource_type TEXT NOT NULL,
      resource_id TEXT NOT NULL,
      grantee_system_account_id TEXT NOT NULL,
      resource_owner_system_account_id TEXT NOT NULL,
      status TEXT NOT NULL,
      expires_at TEXT,
      limits_json TEXT,
      effective_source_type TEXT,
      effective_source_team_id TEXT
    )`)
}

func (h *testDB) sealCredentials(t *testing.T, credentials map[string]any) string {
	t.Helper()
	envelope, err := oauthrefresh.EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// seedPoolAccount 写入一个双 Key 池账户（openai api_key、active、可调度、绑定分组）。
func (h *testDB) seedPoolAccount(t *testing.T, accountID string) (key1, key2 string) {
	t.Helper()
	h.seedSchema(t)
	key1, key2 = "sk-key-one", "sk-key-two"
	credentials := map[string]any{
		"base_url":              "https://upstream.example.com/v1",
		"api_keys":              []any{key1, key2},
		"quota_recovery_policy": map[string]any{"windowHours": float64(24)},
	}
	h.exec(t, `
    INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, health_check_model, health_check_endpoint_mode, credentials_encrypted, dispatch_revision)
    VALUES (?, 'sys-1', '池账户', 'api_key', 'active', 1, 'gpt-test', 'chat_json', ?, 3)`,
		accountID, h.sealCredentials(t, credentials))
	h.exec(t, `INSERT INTO account_supported_models (account_id, model) VALUES (?, 'gpt-test')`, accountID)
	h.exec(t, `INSERT INTO groups (id, system_account_id, provider_code, enabled) VALUES ('group-1', 'sys-1', 'openai', 1)`)
	h.exec(t, `INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled) VALUES ('group-1', 'sys-1', ?, 1)`, accountID)
	return key1, key2
}

func (h *testDB) seedRuntimeState(t *testing.T, accountID, fingerprint string, keyIndex int, status, nextProbeAt string) {
	t.Helper()
	h.exec(t, `
    INSERT INTO account_api_key_runtime_states (
      id, system_account_id, account_id, key_fingerprint, key_index, status, next_probe_at, updated_at
    ) VALUES (?, 'sys-1', ?, ?, ?, ?, ?, ?)`,
		"state-"+fingerprint, accountID, fingerprint, keyIndex, status, nextProbeAt, plusMillis(0), nowMillisText())
}

// TestListDueForProbeClaimsAndSkipsFreshLeases 验证到期候选 claim：命中后写入
// token/租约，未到期的候选不得返回。
func TestListDueForProbeClaimsAndSkipsFreshLeases(t *testing.T) {
	h := openTestDB(t)
	key1, _ := h.seedPoolAccount(t, "acc-1")
	fp1 := h.store.FingerprintAPIKey(key1)
	h.seedRuntimeState(t, "acc-1", fp1, 0, "temporary_unavailable", plusMillis(-1000))

	candidates, err := h.store.ListDueForProbe(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates=%d", len(candidates))
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
	h := openTestDB(t)
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
	if err := h.db.QueryRow(`SELECT status FROM account_api_key_runtime_states WHERE account_id='acc-1' AND key_fingerprint=?`, fp1).Scan(&status); err != nil {
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
      FROM account_api_key_runtime_states WHERE account_id='acc-1' AND key_fingerprint=?`, fp1).
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
      SELECT recovery_started_at FROM account_api_key_runtime_states
      WHERE account_id='acc-1' AND key_fingerprint=?`, fp1).Scan(&recoveryStarted); err != nil {
		t.Fatal(err)
	}
	if recoveryStarted.Valid && recoveryStarted.String != "" {
		t.Fatalf("breakWindow 必须清空 recovery_started_at: %v", recoveryStarted)
	}
}

// TestMarkPrecheckTemporaryUnavailableFenceOrder 验证 skipReason 的判定顺序。
func TestMarkPrecheckTemporaryUnavailableFenceOrder(t *testing.T) {
	h := openTestDB(t)
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
	h.exec(t, `UPDATE accounts SET last_health_success_at = ? WHERE id='acc-1'`, plusMillis(5000))
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
	h.exec(t, `UPDATE accounts SET last_health_success_at = NULL WHERE id='acc-1'`)
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
	if err := h.db.QueryRow(`SELECT dispatch_revision FROM accounts WHERE id='acc-1'`).Scan(&dispatchRevision); err != nil {
		t.Fatal(err)
	}
	if dispatchRevision != 4 {
		t.Fatalf("dispatchRevision=%d", dispatchRevision)
	}
}

// TestFindAccountForTestAvailability 验证 effectiveAvailability 的 DB 分支。
func TestFindAccountForTestAvailability(t *testing.T) {
	h := openTestDB(t)
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
	h.exec(t, `UPDATE accounts SET cooldown_until = ? WHERE id='acc-1'`, plusMillis(60_000))
	view, err = h.store.LoadAccountForTest(context.Background(), "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.EffectiveAvailable {
		t.Fatal("冷却中必须不可用")
	}
	h.exec(t, `UPDATE accounts SET cooldown_until = NULL WHERE id='acc-1'`)

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
	h := openTestDB(t)
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
	h := openTestDB(t)
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
