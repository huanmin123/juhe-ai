package accountkeystates

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// accounts RuntimeResetEffects 端到端测试：真实 accounts.Store 走完整
// runtime-reset 流程，经本包 store 触发 RevalidateAccountAPIKeyRuntimePool /
// APIKeyPoolAllUnavailable 两个端口，断言 account_api_key_runtime_states 的
// 真实副作用。
//
// resetEffectsAdapter 是 cmd/juhe-ai-gateway/compose_accounts_reset.go 生产
// 桥接的测试孪生：两个已装配端口直连本包，其余方法保持生产桥接的登记语义
// （DispatchAccountHealthCheck 登记为 nil 跳过）。生产装配本身由
// compose_accounts_reset_test.go 覆盖。

type resetEffectsAdapter struct {
	keyStates *Store
}

func (a *resetEffectsAdapter) ClearAccountRuntimeAvailability(_ context.Context, _ accounts.RuntimeAvailabilityClearInput) (accounts.RuntimeAvailabilityClearResult, error) {
	// 生产桥接经 K5 bus 投影后恒报 cleared；测试孪生保持同语义。
	return accounts.RuntimeAvailabilityClearResult{Cleared: true}, nil
}

func (a *resetEffectsAdapter) ClearNormalRouteLatencyDegradation(context.Context, string, string) (int64, error) {
	return 0, nil
}

func (a *resetEffectsAdapter) RevalidateAccountAPIKeyRuntimePool(ctx context.Context, accountID string, expectedConfigRevision int64) (accounts.AccountAPIKeyRuntimeRevalidation, error) {
	result, err := a.keyStates.RevalidatePool(ctx, accountID, expectedConfigRevision)
	if err != nil {
		return accounts.AccountAPIKeyRuntimeRevalidation{}, err
	}
	return accounts.AccountAPIKeyRuntimeRevalidation{Eligible: result.Eligible, Changed: result.Changed, Reason: result.Reason}, nil
}

func (a *resetEffectsAdapter) LoadAPIKeyTransientStates(context.Context, string, []string) ([]accounts.AccountAPIKeyTransientSelectionState, error) {
	return []accounts.AccountAPIKeyTransientSelectionState{}, nil
}

func (a *resetEffectsAdapter) ClearAPIKeyFailureGuard(string, string, *int64) bool { return false }

func (a *resetEffectsAdapter) ClearAPIKeyTransientFailure(context.Context, string, string, *int64) (bool, error) {
	return false, nil
}

func (a *resetEffectsAdapter) DispatchAccountHealthCheck(accountID, reason string) {
	// 生产装配：REGISTERED NIL（健康检查派发器未迁移），登记跳过。
	_ = accountID
	_ = reason
}

func (a *resetEffectsAdapter) AuthorizationQuotaExceeded(context.Context, accounts.AuthorizationQuotaCheckInput) (bool, error) {
	return false, nil
}

func (a *resetEffectsAdapter) APIKeyPoolAllUnavailable(ctx context.Context, accountID string) (bool, error) {
	return a.keyStates.AllUnavailable(ctx, accountID)
}

// e2eResetStore 组装 accounts 测试所需的表子集（accounts 测试切片同款列 +
// 本域表 + reset 触点表）。
func e2eResetStore(t *testing.T) (*Store, *accounts.Store, *sql.DB, []string, func(reason string)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "e2e-business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatalf("exec %s: %v", query, err)
		}
	}
	exec(`
    CREATE TABLE accounts (
      id TEXT PRIMARY KEY,
      config_revision INTEGER NOT NULL DEFAULT 1,
      dispatch_revision INTEGER NOT NULL DEFAULT 1,
      system_account_id TEXT NOT NULL,
      name TEXT NOT NULL DEFAULT '',
      type TEXT NOT NULL DEFAULT 'api_key',
      status TEXT NOT NULL DEFAULT 'active',
      credentials_encrypted TEXT NOT NULL DEFAULT '{}',
      client_compatibility TEXT NOT NULL DEFAULT 'openai_standard',
      schedulable INTEGER NOT NULL DEFAULT 1,
      provider_code TEXT NOT NULL DEFAULT 'openai',
      provider_protocol_profile_id TEXT NOT NULL DEFAULT 'profile_openai_openai_v1',
      protocol_code TEXT NOT NULL DEFAULT 'openai',
      protocol_version TEXT NOT NULL DEFAULT 'v1',
      account_expires_at TEXT,
      cooldown_until TEXT,
      last_error_code TEXT,
      last_error_message TEXT,
      last_error_trace_id TEXT,
      last_health_check_at TEXT,
      last_health_check_error_code TEXT,
      last_health_check_error_message TEXT,
      cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
      cooldown_retest_observation_started_at TEXT,
      cooldown_retest_generation TEXT,
      cooldown_retest_last_at TEXT,
      health_check_failure_count INTEGER NOT NULL DEFAULT 0,
      health_check_failure_started_at TEXT,
      stream_failure_count INTEGER NOT NULL DEFAULT 0,
      stream_failure_window_started_at TEXT,
      authorization_instance_authorization_id TEXT,
      authorization_instance_source_account_id TEXT,
      created_at TEXT NOT NULL DEFAULT '',
      updated_at TEXT NOT NULL DEFAULT '',
      deleted_at TEXT
    )`)
	exec(`
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
	exec(`
    CREATE TABLE group_accounts (
      system_account_id TEXT NOT NULL DEFAULT '',
      group_id TEXT NOT NULL,
      account_id TEXT NOT NULL,
      enabled INTEGER NOT NULL DEFAULT 1,
      PRIMARY KEY (group_id, account_id)
    )`)
	exec(`
    CREATE TABLE group_account_stats_dirty (
      group_id TEXT PRIMARY KEY,
      reason TEXT NOT NULL,
      updated_at TEXT NOT NULL
    )`)
	exec(`
    CREATE TABLE account_lock_states (
      account_id TEXT PRIMARY KEY,
      enabled INTEGER NOT NULL DEFAULT 0,
      lock_state TEXT NOT NULL DEFAULT 'UNLOCKED'
    )`)
	exec(`
    CREATE TABLE account_circuit_outbox (
      event_id TEXT PRIMARY KEY,
      projection_key TEXT NOT NULL,
      dedupe_key TEXT NOT NULL,
      event_type TEXT NOT NULL,
      account_id TEXT NOT NULL,
      account_runtime_key TEXT NOT NULL,
      circuit_scope_key TEXT,
      incident_id TEXT,
      transition_id TEXT NOT NULL,
      dispatch_revision INTEGER NOT NULL,
      generation INTEGER,
      ledger_revision INTEGER,
      status TEXT NOT NULL DEFAULT 'pending',
      available_at_ms INTEGER NOT NULL,
      attempt_count INTEGER NOT NULL DEFAULT 0,
      created_at_ms INTEGER NOT NULL,
      updated_at_ms INTEGER NOT NULL
    )`)

	invalCalls := []string{}
	keyStates, err := NewStore(Config{
		DB: db, Postgres: false, Secret: testSecret,
		Now:                    func() time.Time { return testNow },
		InvalidateRuntimeCache: func(reason string) { invalCalls = append(invalCalls, reason) },
	})
	if err != nil {
		t.Fatal(err)
	}
	accountStore, err := accounts.NewStore(db, false, testSecret, func() time.Time { return testNow }, nil)
	if err != nil {
		t.Fatal(err)
	}
	accountStore.SetRuntimeResetEffects(&resetEffectsAdapter{keyStates: keyStates})

	// 种子：三把 Key 的 openai api_key 池账户。
	keys := []any{"sk-e2e-a", "sk-e2e-b", "sk-e2e-c"}
	fingerprints := make([]string, 0, len(keys))
	for _, key := range keys {
		fingerprints = append(fingerprints, keyStates.FingerprintAPIKey(key.(string)))
	}
	sealed, err := accounts.EncryptJSON(testSecret, map[string]any{"api_keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	nowISO := nowMillisText()
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, name, type, status, credentials_encrypted, created_at, updated_at)
		VALUES ('acc-e2e', 'sys-owner', 'reset-e2e', 'api_key', 'active', ?, ?, ?)`, sealed, nowISO, nowISO); err != nil {
		t.Fatal(err)
	}
	seed := func(fingerprint, status string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO account_api_key_runtime_states
			(id, system_account_id, account_id, key_fingerprint, status, created_at, updated_at)
			VALUES (?, 'sys-owner', 'acc-e2e', ?, ?, ?, ?)`,
			"state-"+fingerprint[:12], fingerprint, status, nowISO, nowISO); err != nil {
			t.Fatal(err)
		}
	}
	seed(fingerprints[0], "rate_limited")
	seed(fingerprints[1], "error")
	seed(fingerprints[2], "disabled")
	exec(`INSERT INTO group_accounts (group_id, account_id) VALUES ('grp-e2e', 'acc-e2e')`)
	return keyStates, accountStore, db, fingerprints, func(reason string) { invalCalls = append(invalCalls, reason) }
}

func TestRuntimeResetThroughAccountsStoreEndToEnd(t *testing.T) {
	keyStates, accountStore, db, fingerprints, _ := e2eResetStore(t)
	_ = keyStates

	outcome, err := accountStore.ResetAccountRuntimeState(context.Background(), "acc-e2e", 1, accounts.AccessScope{IsAdmin: true})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	result := outcome.Result
	// rate_limited + error 两行被重校验；disabled 不动。
	if result.APIKeyRuntimeRevalidated != 2 {
		t.Fatalf("apiKeyRuntimeRevalidated: %d", result.APIKeyRuntimeRevalidated)
	}
	cleared := map[string]bool{}
	for _, item := range result.Cleared {
		cleared[item] = true
	}
	if !cleared["api_key_runtime"] {
		t.Fatalf("cleared set: %v", result.Cleared)
	}
	if result.GatewayRuntime != "cleared" {
		t.Fatalf("gatewayRuntime: %s", result.GatewayRuntime)
	}

	var status string
	var cooldownUntil, nextProbeAt sql.NullString
	if err := db.QueryRow(`SELECT status, cooldown_until, next_probe_at FROM account_api_key_runtime_states
		WHERE account_id = 'acc-e2e' AND key_fingerprint = ?`, fingerprints[1]).
		Scan(&status, &cooldownUntil, &nextProbeAt); err != nil {
		t.Fatal(err)
	}
	if status != "unverified" || cooldownUntil.Valid || !nextProbeAt.Valid || nextProbeAt.String != nowMillisText() {
		t.Fatalf("error key row: %s %v %q", status, cooldownUntil.Valid, nextProbeAt.String)
	}
	if err := db.QueryRow(`SELECT status FROM account_api_key_runtime_states
		WHERE account_id = 'acc-e2e' AND key_fingerprint = ?`, fingerprints[2]).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "disabled" {
		t.Fatalf("disabled key must stay untouched: %s", status)
	}
	var reason string
	if err := db.QueryRow(`SELECT reason FROM group_account_stats_dirty WHERE group_id = 'grp-e2e'`).Scan(&reason); err != nil {
		t.Fatalf("stats dirty row: %v", err)
	}
	if reason != statsDirtyReason {
		t.Fatalf("dirty reason: %s", reason)
	}

	// 重校验后整池不可用（rate_limited/unverified/disabled）→ 端口判定生效，
	// 派发可用性关闭。
	if result.DispatchEligible {
		t.Fatalf("dispatchEligible must be false while the whole pool is unavailable: %+v", result)
	}
	allUnavailable, err := keyStates.AllUnavailable(context.Background(), "acc-e2e")
	if err != nil || !allUnavailable {
		t.Fatalf("allUnavailable: %v %v", allUnavailable, err)
	}
}
