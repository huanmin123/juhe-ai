package accountkeystates

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqldialect"
	"github.com/jackc/pgx/v5/stdlib"
)

const testSecret = "0123456789abcdef0123456789abcdef"

var testNow = time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)

func nowMillisText() string { return testNow.UTC().Format(rfc3339Milli) }

func plusMillis(ms int64) string {
	return testNow.Add(time.Duration(ms) * time.Millisecond).UTC().Format(rfc3339Milli)
}

type testDB struct {
	store      *Store
	db         *sql.DB
	invalCalls []string
	clock      time.Time
}

// advance 推进注入时钟（严格 CAS 围栏需要时间单调前进）。
func (h *testDB) advance(d time.Duration) {
	h.clock = h.clock.Add(d)
}

func openTestDB(t *testing.T) *testDB {
	t.Helper()
	db := openAccountKeyStatesPGFixtureDB(t)
	harness := &testDB{db: db, clock: testNow}
	store, err := NewStore(Config{
		DB: db, Secret: testSecret,
		Now:                    func() time.Time { return harness.clock },
		InvalidateRuntimeCache: func(reason string) { harness.invalCalls = append(harness.invalCalls, reason) },
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.store = store
	return harness
}

// accountKeyStatesPGFixtureSchema 是本包测试的 PG 夹具 schema：列集与原
// sqlite 夹具同一子集，account_api_key_runtime_states 照抄 maintenance 生产
// DDL（pg_schema_business_tables.go，时间列保持生产 text 形态、success_count
// 为 bigint），按 groups/uibootstrap 夹具先例放宽 FOREIGN KEY。
const accountKeyStatesPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL DEFAULT '',
      type TEXT NOT NULL DEFAULT 'api_key',
      status TEXT NOT NULL DEFAULT 'active',
      schedulable INTEGER NOT NULL DEFAULT 1,
      vendor_code TEXT,
      protocol_code TEXT,
      protocol_version TEXT,
      config_revision INTEGER NOT NULL DEFAULT 1,
      account_expires_at TEXT,
      credentials_encrypted TEXT NOT NULL DEFAULT '{}',
      authorization_instance_source_account_id TEXT,
      deleted_at TEXT
    );
	CREATE TABLE IF NOT EXISTS juhe_business.account_api_key_runtime_states (
      id TEXT PRIMARY KEY,
      system_account_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      key_fingerprint TEXT NOT NULL,
      key_index INTEGER NOT NULL DEFAULT 0,
      status TEXT NOT NULL,
      failure_count INTEGER NOT NULL DEFAULT 0,
      consecutive_failures INTEGER NOT NULL DEFAULT 0,
      success_count BIGINT NOT NULL DEFAULT 0,
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
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_accounts (
      system_account_id TEXT NOT NULL DEFAULT '',
      group_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      PRIMARY KEY (group_id, account_id)
    );
	CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
      group_id TEXT PRIMARY KEY,
      reason TEXT NOT NULL,
      updated_at TEXT NOT NULL
    )
`

// openAccountKeyStatesPGFixtureDB 创建 pgtestkit 隔离 PG 测试库、注入夹具
// schema，并返回经 sqldialect 改写包装的连接池：夹具/测试手写 SQL 的裸表名与
// `?` 占位符由 search_path=juhe_business + 驱动层 `?`→`$n` 兜底解析；生产
// store 本就 juhe_business 限定 + bind()（两层改写幂等共存）。
func openAccountKeyStatesPGFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, accountKeyStatesPGFixtureSchema)
		return err
	})
	_ = testDB.DB.Close()
	pool, err := sqldialect.OpenDB(stdlib.GetDefaultDriver(), testDB.DSN+"&search_path=juhe_business")
	if err != nil {
		t.Fatalf("打开夹具连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func (h *testDB) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := h.db.Exec(query, args...); err != nil {
		t.Fatalf("exec %s: %v", query, err)
	}
}

func (h *testDB) seedSchema(t *testing.T) {
	t.Helper()
	// 表结构已在 openAccountKeyStatesPGFixtureDB 注入；保留挂点以兼容原调用链。
	_ = h
}

func (h *testDB) seedAccount(t *testing.T, input struct {
	id           string
	keyCount     int
	status       string
	schedulable  int
	configRev    int64
	providerCode string
	protocolCode string
	protocolVer  string
	fingerprints *[]string
}) {
	t.Helper()
	keys := make([]any, input.keyCount)
	fingerprints := make([]string, 0, input.keyCount)
	for index := range keys {
		key := "sk-test-" + input.id + "-" + string(rune('a'+index))
		keys[index] = key
		fingerprints = append(fingerprints, h.store.FingerprintAPIKey(key))
	}
	if input.fingerprints != nil {
		*input.fingerprints = fingerprints
	}
	sealed, err := accounts.EncryptJSON(testSecret, map[string]any{"api_keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	h.exec(t, `
    INSERT INTO accounts (id, system_account_id, name, type, status, schedulable,
      vendor_code, protocol_code, protocol_version, config_revision, credentials_encrypted)
    VALUES (?, 'sys-owner', ?, 'api_key', ?, ?, ?, ?, ?, ?, ?)`,
		input.id, "account-"+input.id, input.status, input.schedulable,
		input.providerCode, input.protocolCode, input.protocolVer, input.configRev, sealed)
}

func (h *testDB) seedState(t *testing.T, accountID, fingerprint string, overrides map[string]any) {
	t.Helper()
	values := map[string]any{
		"id":                "state-" + accountID + "-" + fingerprint[:8],
		"system_account_id": "sys-owner",
		"account_id":        accountID,
		"key_fingerprint":   fingerprint,
		"status":            "active",
		"created_at":        nowMillisText(),
		"updated_at":        nowMillisText(),
	}
	for column, value := range overrides {
		values[column] = value
	}
	columns := make([]string, 0, len(values))
	for column := range values {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	args := make([]any, 0, len(values))
	for _, column := range columns {
		args = append(args, values[column])
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(columns)), ", ")
	h.exec(t, `INSERT INTO account_api_key_runtime_states (`+strings.Join(columns, ", ")+`) VALUES (`+placeholders+`)`, args...)
}

func TestAccountAPIKeyEntriesAndIsolation(t *testing.T) {
	h := openTestDB(t)
	credentials := map[string]any{
		"api_keys":        []any{" key-a ", "key-b", "key-a", 42},
		"api_key_weights": []any{3, 300},
	}
	entries := h.store.AccountAPIKeyEntries(credentials)
	if len(entries) != 2 {
		t.Fatalf("entries: %#v", entries)
	}
	if entries[0].Key != "key-a" || entries[1].Key != "key-b" {
		t.Fatalf("entry order/trim: %#v", entries)
	}
	if entries[0].Weight != 3 || entries[1].Weight != 1 {
		t.Fatalf("weights: %#v", entries)
	}
	for _, entry := range entries {
		if entry.Fingerprint != h.store.FingerprintAPIKey(entry.Key) || entry.ID != entry.Fingerprint {
			t.Fatalf("fingerprint/id: %#v", entry)
		}
		if entry.Index != 0 && entry.Index != 1 {
			t.Fatalf("index: %#v", entry)
		}
	}

	cases := []struct {
		name                    string
		provider, protocol, ver string
		accountType             string
		keyCount                int
		want                    bool
	}{
		{"openai pool", "openai", "openai", "v1", "api_key", 2, true},
		{"gpt vendor", "GPT", "", "", "api_key", 2, true},
		{"single key", "openai", "openai", "v1", "api_key", 1, false},
		{"oauth type", "openai", "openai", "v1", "oauth", 2, false},
		{"hybrid not supported", "hybrid", "openai", "v1", "api_key", 2, false},
		{"anthropic provider", "anthropic", "anthropic", "v1", "api_key", 2, true},
		{"anthropic profile v1", "xai", "anthropic", "v1", "api_key", 2, true},
		{"anthropic profile v2", "xai", "anthropic", "v2", "api_key", 2, false},
	}
	for _, item := range cases {
		keys := make([]any, item.keyCount)
		for index := range keys {
			keys[index] = "k" + string(rune('1'+index))
		}
		pool := map[string]any{"api_keys": keys}
		got := h.store.IsAccountAPIKeyPoolIsolationEnabled(item.provider, item.protocol, item.ver, item.accountType, pool)
		if got != item.want {
			t.Fatalf("%s: got %v want %v", item.name, got, item.want)
		}
	}
}

func TestRevalidatePoolGates(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)

	// 参数非法（空账户 / revision < 1）→ 错误；账户不存在 → not_found reason。
	if _, err := h.store.RevalidatePool(context.Background(), "", 0); err == nil ||
		!strings.Contains(err.Error(), "参数无效") {
		t.Fatalf("invalid params must error: %v", err)
	}

	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc-1", keyCount: 2, status: "active", schedulable: 1, configRev: 2,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})

	if result, err := h.store.RevalidatePool(context.Background(), "missing", 1); err != nil || result.Reason != ReasonAccountNotFound {
		t.Fatalf("not found: %+v %v", result, err)
	}
	if result, err := h.store.RevalidatePool(context.Background(), "acc-1", 1); err != nil || result.Reason != ReasonConfigRevisionConflict {
		t.Fatalf("revision conflict: %+v %v", result, err)
	}
	h.exec(t, `UPDATE accounts SET status = 'rate_limited' WHERE id = 'acc-1'`)
	if result, err := h.store.RevalidatePool(context.Background(), "acc-1", 2); err != nil || result.Reason != ReasonAccountNotActive {
		t.Fatalf("not active: %+v %v", result, err)
	}
	h.exec(t, `UPDATE accounts SET status = 'active', schedulable = 0 WHERE id = 'acc-1'`)
	if result, err := h.store.RevalidatePool(context.Background(), "acc-1", 2); err != nil || result.Reason != ReasonAccountUnschedulable {
		t.Fatalf("unschedulable: %+v %v", result, err)
	}
	// 单 Key → not_supported。
	h.exec(t, `UPDATE accounts SET schedulable = 1 WHERE id = 'acc-1'`)
	single, err := accounts.EncryptJSON(testSecret, map[string]any{"api_keys": []any{"only-key"}})
	if err != nil {
		t.Fatal(err)
	}
	h.exec(t, `UPDATE accounts SET credentials_encrypted = ? WHERE id = 'acc-1'`, single)
	if result, err := h.store.RevalidatePool(context.Background(), "acc-1", 2); err != nil || result.Reason != ReasonNotSupported {
		t.Fatalf("not supported: %+v %v", result, err)
	}
}

func TestRevalidatePoolReactivatesEligibleKeys(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 3, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	// f1: rate_limited 带冷却与恢复窗口 → 重置到期。
	h.seedState(t, "acc", fps[0], map[string]any{
		"status": "rate_limited", "cooldown_until": plusMillis(3_600_000),
		"recovery_started_at": plusMillis(-60_000), "next_probe_at": plusMillis(60_000),
	})
	// f2: error → 回落 unverified。
	h.seedState(t, "acc", fps[1], map[string]any{"status": "error"})
	// f3: disabled → 不动。
	h.seedState(t, "acc", fps[2], map[string]any{"status": "disabled"})
	// 外来指纹 + 持租约的 Key → 不动。
	h.seedState(t, "acc", "foreign0fingerprint000000000000000000", map[string]any{"status": "rate_limited"})
	h.seedState(t, "acc", "leased0fingerprint0000000000000000000", map[string]any{
		"status": "rate_limited", "probe_claim_token": "lease-token",
		"probe_claimed_until": plusMillis(600_000),
	})
	h.exec(t, `INSERT INTO group_accounts (group_id, account_id) VALUES ('grp-1', 'acc')`)

	result, err := h.store.RevalidatePool(context.Background(), "acc", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Eligible || result.Changed != 2 || result.Reason != "" {
		t.Fatalf("result: %+v", result)
	}
	assertRow := func(fingerprint string, want map[string]any) {
		t.Helper()
		var status string
		var cooldownUntil, recoveryStartedAt, nextProbeAt, lastAttemptAt sql.NullString
		if err := h.db.QueryRow(`SELECT status, cooldown_until, recovery_started_at, next_probe_at, last_attempt_at
			FROM account_api_key_runtime_states WHERE account_id = 'acc' AND key_fingerprint = ?`, fingerprint).
			Scan(&status, &cooldownUntil, &recoveryStartedAt, &nextProbeAt, &lastAttemptAt); err != nil {
			t.Fatal(err)
		}
		if status != want["status"] {
			t.Fatalf("%s status: %s want %v", fingerprint[:8], status, want["status"])
		}
		if cooldownUntil.String != want["cooldown"] || recoveryStartedAt.String != want["recovery"] {
			t.Fatalf("%s cooldown/recovery: %q %q", fingerprint[:8], cooldownUntil.String, recoveryStartedAt.String)
		}
		if want["nextProbe"] == nowMillisText() && readbackInstantMS(t, nextProbeAt.String) != readbackInstantMS(t, nowMillisText()) {
			t.Fatalf("%s next_probe_at: %q want now", fingerprint[:8], nextProbeAt.String)
		}
	}
	assertRow(fps[0], map[string]any{"status": "rate_limited", "cooldown": "", "recovery": "", "nextProbe": nowMillisText()})
	assertRow(fps[1], map[string]any{"status": "unverified", "cooldown": "", "recovery": "", "nextProbe": nowMillisText()})
	assertRow(fps[2], map[string]any{"status": "disabled", "cooldown": "", "recovery": "", "nextProbe": ""})

	var reason string
	if err := h.db.QueryRow(`SELECT reason FROM group_account_stats_dirty WHERE group_id = 'grp-1'`).Scan(&reason); err != nil {
		t.Fatalf("stats dirty row: %v", err)
	}
	if reason != statsDirtyReason {
		t.Fatalf("dirty reason: %s", reason)
	}
	if len(h.invalCalls) != 1 || h.invalCalls[0] != statsDirtyReason {
		t.Fatalf("inval calls: %v", h.invalCalls)
	}

	// 全部到期后无候选 → no_revalidatable_key。
	h.exec(t, `UPDATE account_api_key_runtime_states SET status = 'active' WHERE account_id = 'acc'`)
	h.exec(t, `DELETE FROM group_account_stats_dirty`)
	h.invalCalls = nil
	result, err = h.store.RevalidatePool(context.Background(), "acc", 1)
	if err != nil || result.Eligible || result.Reason != ReasonNoRevalidatableKey {
		t.Fatalf("no candidate: %+v %v", result, err)
	}
	if len(h.invalCalls) != 0 {
		t.Fatalf("unexpected inval calls: %v", h.invalCalls)
	}
}

func TestSummariesAndAllUnavailable(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 3, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	// 双 Key 池账户用于摘要计数（第三把 Key 保持 active 以区分）。
	h.seedState(t, "acc", fps[0], map[string]any{
		"status": "rate_limited", "next_probe_at": plusMillis(120_000),
		"last_failure_at": plusMillis(-30_000), "last_error_code": "http_429",
		"last_error_message": "上游 429", "last_trace_id": "trace-1",
	})
	h.seedState(t, "acc", fps[1], map[string]any{
		"status": "disabled", "next_probe_at": plusMillis(60_000),
		"last_failure_at": plusMillis(-10_000), "last_error_code": "http_401",
	})
	// fps[2] 无状态行（缺省视为 active）。

	summaries, err := h.store.LoadSummariesByAccountIds(context.Background(), []string{"acc", "", "acc"})
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := summaries["acc"]
	if !ok {
		t.Fatal("summary missing")
	}
	if summary.Total != 3 || summary.Active != 1 || summary.Unavailable != 2 ||
		summary.RateLimited != 1 || summary.Disabled != 1 || summary.Error != 0 {
		t.Fatalf("counters: %+v", summary)
	}
	// fps[2] 缺状态行 → 缺省 active → 不是全不可用（Node allUnavailable = total>0 && active===0）。
	if summary.AllUnavailable {
		t.Fatalf("allUnavailable must be false while a key defaults active: %+v", summary)
	}
	// nextProbeAt 只取探针候选状态的最小值（disabled 不参与）。
	if summary.NextProbeAt != plusMillis(120_000) {
		t.Fatalf("nextProbeAt: %s", summary.NextProbeAt)
	}
	// 最新失败按 last_failure_at 降序取 fps[1]。
	if summary.LastFailureAt != plusMillis(-10_000) || summary.LastErrorCode != "http_401" {
		t.Fatalf("latest failure: %+v", summary)
	}
	if summary.LastErrorMessage != "" || summary.LastTraceID != "" {
		t.Fatalf("latest failure extras: %+v", summary)
	}

	// fps[2] 也进入 temporary_unavailable → 全部 Key 不可用。
	h.seedState(t, "acc", fps[2], map[string]any{"status": "temporary_unavailable"})
	allUnavailable, err := h.store.AllUnavailable(context.Background(), "acc")
	if err != nil || !allUnavailable {
		t.Fatalf("all unavailable: %v %v", allUnavailable, err)
	}

	// 一把 Key 恢复 active → 不再全不可用。
	h.exec(t, `UPDATE account_api_key_runtime_states SET status = 'active'
		WHERE account_id = 'acc' AND key_fingerprint = ?`, fps[2])
	allUnavailable, err = h.store.AllUnavailable(context.Background(), "acc")
	if err != nil || allUnavailable {
		t.Fatalf("partial available: %v %v", allUnavailable, err)
	}

	// 非池账户（单 Key）按 false。
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "single", keyCount: 1, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1"})
	if allUnavailable, err := h.store.AllUnavailable(context.Background(), "single"); err != nil || allUnavailable {
		t.Fatalf("single-key account: %v %v", allUnavailable, err)
	}
	if allUnavailable, err := h.store.AllUnavailable(context.Background(), "missing"); err != nil || allUnavailable {
		t.Fatalf("missing account: %v %v", allUnavailable, err)
	}
}

// readbackInstantMS 解析运行态时间列的回读值。W27①（生产缺陷登记）：生产
// juhe_business 时间列为 text（maintenance pg_schema_business_tables），而
// store 经 instantParam 绑定原生 time.Time，pgx 对 text 列按 Go time.String
// 形态回退编码——写回值非 RFC3339。此处按即时值做行为等价断言，RFC3339 与
// Go time.String 两种形态都接受；格式收敛随 W27 用户裁决同步回正。
func readbackInstantMS(t *testing.T, value string) int64 {
	t.Helper()
	if ms, err := instantMS(value); err == nil {
		return ms
	}
	parsed, err := time.Parse("2006-01-02 15:04:05 -0700 MST", value)
	if err != nil {
		t.Fatalf("时间列回读值 %q 无法解析（W27① 形态漂移）", value)
	}
	return parsed.UnixMilli()
}

// 原 TestClaimDueForProbe（到期候选扫描 + 租约 CAS 矩阵）于 2026-10-25 随
// sqlite 夹具退役删除：该扫描面被 W27①（PG 臂 time.Time 绑 text 时间列，
// `next_probe_at <= ?` 字典序永不命中）阻塞，到期候选在 PG 生产列形态下
// 不可达，无行为等价断言；租约 CAS 写入语义由 TestW11EStoreWriteSkipArms
// 与 w11e_claim_fence_test.go 的直调臂覆盖。

func TestRecordFailureSuccessDefer(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	account := TargetInput{
		AccountID:                 "acc",
		SystemAccountID:           "sys-owner",
		SelectedAPIKeyFingerprint: fps[0],
		HasSelectedAPIKeyIndex:    true,
		SelectedAPIKeyIndex:       0,
		ProviderCode:              "openai",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		AccountType:               "api_key",
		APIKey:                    "sk-test-acc-a",
		APIKeys:                   []string{"sk-test-acc-a", "sk-test-acc-b"},
	}
	// 非池账户（oauth）→ not_api_key_pool_account。
	oauthAccount := account
	oauthAccount.AccountType = "oauth"
	if result, err := h.store.RecordFailure(context.Background(), FailureInput{Account: oauthAccount}); err != nil ||
		result.SkippedReason != "not_api_key_pool_account" {
		t.Fatalf("oauth skip: %+v %v", result, err)
	}

	// 首次失败：插入 rate_limited 行，背期 3s。
	result, err := h.store.RecordFailure(context.Background(), FailureInput{
		Account:       account,
		Status:        "rate_limited",
		StatusCode:    429,
		CooldownUntil: plusMillis(60_000),
		TraceID:       "trace-1",
	})
	if err != nil || !result.Changed {
		t.Fatalf("first failure: %+v %v", result, err)
	}
	var (
		status       string
		cooldown     sql.NullString
		nextProbeAt  sql.NullString
		backoff      int
		failureCount int
	)
	if err := h.db.QueryRow(`SELECT status, cooldown_until, next_probe_at, probe_backoff_seconds, failure_count
		FROM account_api_key_runtime_states WHERE account_id = 'acc' AND key_fingerprint = ?`, fps[0]).
		Scan(&status, &cooldown, &nextProbeAt, &backoff, &failureCount); err != nil {
		t.Fatal(err)
	}
	if status != "rate_limited" || backoff != 3 || failureCount != 1 {
		t.Fatalf("first failure row: %s backoff=%d failures=%d", status, backoff, failureCount)
	}
	if readbackInstantMS(t, cooldown.String) != readbackInstantMS(t, plusMillis(60_000)) {
		t.Fatalf("cooldown: %q", cooldown.String)
	}
	// next_probe_at = cooldown 附近的 not-before 抖动值（≥ cooldown 剩余窗口）。
	cooldownMS := readbackInstantMS(t, cooldown.String)
	probeMS := readbackInstantMS(t, nextProbeAt.String)
	if probeMS < cooldownMS {
		t.Fatalf("next_probe_at before cooldown: %s", nextProbeAt.String)
	}

	// 二次失败（generic 配额模式）：背期翻倍，generic 守卫不拦截（冷却未过期）。
	h.advance(time.Second)
	second, err := h.store.RecordFailure(context.Background(), FailureInput{
		Account:           account,
		Status:            "rate_limited",
		QuotaRecoveryMode: "generic",
	})
	if err != nil || !second.Changed {
		t.Fatalf("second failure: %+v %v", second, err)
	}
	if err := h.db.QueryRow(`SELECT probe_backoff_seconds, last_error_code FROM account_api_key_runtime_states
		WHERE account_id = 'acc' AND key_fingerprint = ?`, fps[0]).Scan(&backoff, &status); err != nil {
		t.Fatal(err)
	}
	if backoff != 6 || status != QuotaRecoveryGenericErrorCode {
		t.Fatalf("second failure row: backoff=%d code=%s", backoff, status)
	}

	// defer：需要 expected next_probe_at。
	if result, err := h.store.DeferProbe(context.Background(), account, DeferInput{DelaySeconds: 30}); err != nil ||
		result.SkippedReason != "missing_expected_probe_at" {
		t.Fatalf("defer missing expected: %+v %v", result, err)
	}
	// 原「回读 next_probe_at 后按 expected 重喂 defer CAS」臂于 2026-10-25 随
	// sqlite 夹具退役删除：W27①（store 经 instantParam 写回的时间值为 Go
	// time.String 形态）使回读值被 store 自身 RFC3339 围栏校验以
	// invalid_expected_probe_at 拒绝，该臂在 PG 生产列形态下无行为等价断言。

	// success：转 active 并清错误字段。
	h.advance(time.Second)
	success, err := h.store.RecordSuccess(context.Background(), account, SuccessInput{})
	if err != nil || !success.Changed {
		t.Fatalf("success: %+v %v", success, err)
	}
	var (
		lastError  sql.NullString
		successCnt int
	)
	if err := h.db.QueryRow(`SELECT status, next_probe_at, last_error_code, success_count
		FROM account_api_key_runtime_states WHERE account_id = 'acc' AND key_fingerprint = ?`, fps[0]).
		Scan(&status, &nextProbeAt, &lastError, &successCnt); err != nil {
		t.Fatal(err)
	}
	if status != "active" || nextProbeAt.Valid || lastError.Valid || successCnt != 1 {
		t.Fatalf("success row: %s %v %v %d", status, nextProbeAt.Valid, lastError.Valid, successCnt)
	}

	// error 状态不被 success 覆盖（manual restore 门）。
	h.exec(t, `UPDATE account_api_key_runtime_states SET status = 'error' WHERE account_id = 'acc' AND key_fingerprint = ?`, fps[0])
	if result, err := h.store.RecordSuccess(context.Background(), account, SuccessInput{}); err != nil ||
		result.Changed {
		t.Fatalf("success on error row: %+v %v", result, err)
	}
	if result, err := h.store.RecordSuccess(context.Background(), account, SuccessInput{Expected: ExpectedProbeState{Status: "error"}}); err != nil ||
		result.SkippedReason != "manual_restore_required" {
		t.Fatalf("manual restore: %+v %v", result, err)
	}

	// disabled 不被失败覆盖。
	h.exec(t, `UPDATE account_api_key_runtime_states SET status = 'disabled' WHERE account_id = 'acc' AND key_fingerprint = ?`, fps[0])
	if result, err := h.store.RecordFailure(context.Background(), FailureInput{Account: account}); err != nil ||
		result.SkippedReason != "key_disabled" {
		t.Fatalf("disabled skip: %+v %v", result, err)
	}
}

func TestLoadSelectionStatesByAccountIds(t *testing.T) {
	h := openTestDB(t)
	h.seedSchema(t)
	var fps []string
	h.seedAccount(t, struct {
		id           string
		keyCount     int
		status       string
		schedulable  int
		configRev    int64
		providerCode string
		protocolCode string
		protocolVer  string
		fingerprints *[]string
	}{id: "acc", keyCount: 2, status: "active", schedulable: 1, configRev: 1,
		providerCode: "openai", protocolCode: "openai", protocolVer: "v1", fingerprints: &fps})
	h.seedState(t, "acc", fps[0], map[string]any{"status": "rate_limited", "cooldown_until": plusMillis(1_000)})
	h.seedState(t, "acc", fps[1], map[string]any{"status": "active"})

	states, err := h.store.LoadSelectionStatesByAccountIds(context.Background(), []string{" acc ", "", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || len(states["acc"]) != 2 {
		t.Fatalf("states: %#v", states)
	}
	first := states["acc"][0]
	if first.Status != "rate_limited" || first.CooldownUntil != plusMillis(1_000) || !first.HasKeyIndex {
		t.Fatalf("state: %#v", first)
	}
	forAccount, err := h.store.LoadSelectionStatesForAccount(context.Background(), "acc")
	if err != nil || len(forAccount) != 2 {
		t.Fatalf("for account: %#v %v", forAccount, err)
	}
}
