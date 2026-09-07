package accounts

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// sqlNullInt64 / sqlNullInt64NonValid build the owner-override scan shape for
// the effectiveAiAccountCreationLimit table test.
func sqlNullInt64(value int64) sql.NullInt64 { return sql.NullInt64{Int64: value, Valid: true} }

func sqlNullInt64NonValid() sql.NullInt64 { return sql.NullInt64{} }

// BUG-0174 M-8：账户创建事务后链对齐（Node 归档
// migration-backup/node/final-archive/backend/src/storage/repositories.ts
// createAccountInClientAsync）。覆盖：创建上限断言（:2342,2470-2508）、
// dispatch revision 家族推进（:2412-2417）、J1 snapshot outbox 与
// input_versions 版本行（:2418-2432，探活候选 SQL 硬 JOIN 该表）、提交后
// 三路缓存失效与组统计脏标记（:2440-2443）、事务回滚不残留后链。

// bug0174CreateInput is the minimal health-capable create payload (gpt +
// api_key, the archive's J1 snapshot condition branch).
func bug0174CreateInput(name string) CreateInput {
	return CreateInput{
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "prof-gpt",
		Name:                      name,
		AccountType:               "api_key",
		Credentials:               Credentials{"api_key": "sk-live-secret-1234567890", "base_url": "https://api.openai.com/v1"},
		SupportedModels:           []string{"gpt-4o-mini"},
		Status:                    CreationStatus{Status: "active", SkipInitialHealthCheck: true, Schedulable: true},
	}
}

// seedOwnerWithLimit inserts the owning system_accounts row; nil limit keeps
// the column NULL so the creation limit falls back to the settings port.
func (e *testEnv) seedOwnerWithLimit(t *testing.T, id string, limit *int64) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if limit != nil {
		e.exec(t, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, ai_account_limit, created_at, updated_at)
			VALUES (?, ?, ?, 'user', 'active', 'x', ?, ?, ?)`, id, "u-"+id, id, *limit, now, now)
		return
	}
	e.exec(t, `INSERT INTO system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES (?, ?, ?, 'user', 'active', 'x', ?, ?)`, id, "u-"+id, id, now, now)
}

// TestCreateHealthInputSnapshotAndRevisionAdvance mirrors repositories.ts
// :2412-2432: the dispatch family advance bumps the fresh row to
// dispatch_revision 2 and lands the dispatch_revision_changed outbox row,
// then the J1 snapshot condition reserves input version 1 and writes the
// pending account_created snapshot intent — the version row that makes the
// new account visible to the direct_input_reader candidate join.
func TestCreateHealthInputSnapshotAndRevisionAdvance(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-0174a")
	env.seedOwnerWithLimit(t, "owner-0174a", nil)

	result, err := env.store.Create(context.Background(), bug0174CreateInput("chain-a"), AccessScope{ViewerID: "owner-0174a"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigRevision != 1 || result.DispatchRevision != 2 {
		t.Fatalf("revisions = (%d, %d), want (1, 2) after the family advance", result.ConfigRevision, result.DispatchRevision)
	}
	if got := env.queryCell(t, `SELECT dispatch_revision FROM accounts WHERE id = ?`, result.ID); got != "2" {
		t.Fatalf("persisted dispatch_revision = %q, want 2", got)
	}
	// dispatch_revision_changed circuit outbox row.
	if got := env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox
		WHERE account_id = ? AND event_type = 'dispatch_revision_changed'
			AND dispatch_revision = 2 AND dedupe_key LIKE 'dispatch:%'`, result.ID); got != 1 {
		t.Fatalf("circuit outbox dispatch rows = %d, want 1", got)
	}
	// The probe-candidate version row (direct_input_reader hard JOINs this
	// table — missing it hides the account from probing).
	if got := env.queryCell(t, `SELECT current_version FROM account_health_jobs_input_versions WHERE account_id = ?`, result.ID); got != "1" {
		t.Fatalf("input_versions.current_version = %q, want 1", got)
	}
	// The pending snapshot intent keyed by the reserved version.
	if got := env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox
		WHERE account_id = ? AND event_kind = 'snapshot' AND reason = 'account_created'
			AND status = 'pending' AND input_version = 1
			AND config_revision = 1 AND dispatch_revision = 2`, result.ID); got != 1 {
		t.Fatalf("snapshot outbox rows = %d, want 1", got)
	}
}

// TestCreateSnapshotSkipsNonHealthCapable mirrors the archive condition
// (repositories.ts:2418): only gpt/openai api_key/oauth accounts reserve the
// snapshot; the dispatch family advance still applies to every create. The
// non-health-capable arm rides a google_oauth account (a registered
// credential driver outside the api_key/oauth snapshot condition).
func TestCreateSnapshotSkipsNonHealthCapable(t *testing.T) {
	env := newTestEnv(t)
	owner := "owner-0174b"
	env.seedProviderAndDefaultGroup(t, owner)
	env.seedOwnerWithLimit(t, owner, nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES ('prof-gpt-goog', 'gpt', 'OpenAI Google OAuth', 1, 'openai', 'v1', 'https://api.openai.com/v1',
		'gpt-4o-mini', '["google_oauth"]', '[]', ?, ?)`, now, now)

	input := CreateInput{
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "prof-gpt-goog",
		Name:                      "chain-b",
		AccountType:               "google_oauth",
		Credentials: Credentials{
			"refresh_token": "rt-secret-1234567890",
			"client_id":     "cid-secret-1234567890",
			"client_secret": "cs-secret-1234567890",
			"base_url":      "https://api.openai.com/v1",
		},
		SupportedModels:           []string{"gpt-4o-mini"},
		Status:                    CreationStatus{Status: "active", SkipInitialHealthCheck: true, Schedulable: true},
	}
	result, err := env.store.Create(context.Background(), input, AccessScope{ViewerID: owner})
	if err != nil {
		t.Fatal(err)
	}
	if result.DispatchRevision != 2 {
		t.Fatalf("non-health-capable dispatch revision = %d, want 2 (family advance is unconditional)", result.DispatchRevision)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_versions WHERE account_id = ?`, result.ID); got != 0 {
		t.Fatalf("non-health-capable input_versions rows = %d, want 0", got)
	}
	if got := env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox WHERE account_id = ?`, result.ID); got != 0 {
		t.Fatalf("non-health-capable snapshot rows = %d, want 0", got)
	}
}

// TestCreateAiAccountLimitAssertion mirrors repositories.ts:2342,2470-2508:
// the owner override gates the live base-account count inside the create
// transaction, 0 means unlimited, and the over-limit copy is exact. Each arm
// is a subtest because the fixture database is named after the test.
func TestCreateAiAccountLimitAssertion(t *testing.T) {
	owner := "owner-0174c"

	t.Run("over limit leaves no post chain", func(t *testing.T) {
		one := int64(1)
		env := newTestEnv(t)
		env.seedProviderAndDefaultGroup(t, owner)
		env.seedOwnerWithLimit(t, owner, &one)
		env.seedAccount(t, "acc-0174-c1", owner, "existing", "active")
		if _, err := env.store.Create(context.Background(), bug0174CreateInput("chain-c"), AccessScope{ViewerID: owner}); err == nil {
			t.Fatal("create over the limit must fail")
		} else {
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Message != "AI 账户数量已达到限制（1）" {
				t.Fatalf("over-limit error = %v, want AI 账户数量已达到限制（1）", err)
			}
		}
		if got := env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_versions`); got != 0 {
			t.Fatalf("rolled-back create left input_versions rows: %d", got)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM account_health_jobs_input_outbox`); got != 0 {
			t.Fatalf("rolled-back create left snapshot rows: %d", got)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM account_circuit_outbox`); got != 0 {
			t.Fatalf("rolled-back create left circuit outbox rows: %d", got)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM group_account_stats_dirty`); got != 0 {
			t.Fatalf("rolled-back create left stats dirty rows: %d", got)
		}
		if got := env.count(t, `SELECT COUNT(*) FROM accounts WHERE name = 'chain-c'`); got != 0 {
			t.Fatalf("rolled-back create persisted the account row: %d", got)
		}
	})

	t.Run("within limit", func(t *testing.T) {
		two := int64(2)
		env := newTestEnv(t)
		env.seedProviderAndDefaultGroup(t, owner)
		env.seedOwnerWithLimit(t, owner, &two)
		env.seedAccount(t, "acc-0174-c2", owner, "existing", "active")
		if _, err := env.store.Create(context.Background(), bug0174CreateInput("chain-c2"), AccessScope{ViewerID: owner}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unlimited zero", func(t *testing.T) {
		zero := int64(0)
		env := newTestEnv(t)
		env.seedProviderAndDefaultGroup(t, owner)
		env.seedOwnerWithLimit(t, owner, &zero)
		for _, name := range []string{"chain-c3a", "chain-c3b", "chain-c3c"} {
			if _, err := env.store.Create(context.Background(), bug0174CreateInput(name), AccessScope{ViewerID: owner}); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("settings fallback", func(t *testing.T) {
		env := newTestEnv(t)
		env.seedProviderAndDefaultGroup(t, owner)
		env.seedOwnerWithLimit(t, owner, nil)
		env.seedAccount(t, "acc-0174-c3", owner, "existing", "active")
		env.store.SetAiAccountLimitSettings(&fakeAiAccountLimitSettings{limit: 1})
		if _, err := env.store.Create(context.Background(), bug0174CreateInput("chain-c4"), AccessScope{ViewerID: owner}); err == nil {
			t.Fatal("settings-driven limit must gate the create")
		} else {
			var validation *ValidationError
			if !errors.As(err, &validation) || validation.Message != "AI 账户数量已达到限制（1）" {
				t.Fatalf("settings fallback error = %v, want AI 账户数量已达到限制（1）", err)
			}
		}
	})
}

// fakeAiAccountLimitSettings is the settings port test double.
type fakeAiAccountLimitSettings struct {
	limit int64
}

func (f *fakeAiAccountLimitSettings) UserAiAccountLimit(ctx context.Context) (int64, error) {
	return f.limit, nil
}

// TestEffectiveAiAccountCreationLimit table-drives the archived fallback and
// validation semantics (repositories.ts:2493-2499): owner override wins, the
// setting is the fallback, the valid range is the integer [0, 1000000], and
// the unwired port keeps the schema default 100.
func TestEffectiveAiAccountCreationLimit(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	limit, err := env.store.effectiveAiAccountCreationLimit(ctx, sqlNullInt64(3))
	if err != nil || limit != 3 {
		t.Fatalf("owner override = (%d, %v), want (3, nil)", limit, err)
	}
	limit, err = env.store.effectiveAiAccountCreationLimit(ctx, sqlNullInt64NonValid())
	if err != nil || limit != defaultUserAiAccountLimit {
		t.Fatalf("unwired fallback = (%d, %v), want (%d, nil)", limit, err, defaultUserAccountLimitValue())
	}
	store := env.store
	store.SetAiAccountLimitSettings(&fakeAiAccountLimitSettings{limit: 7})
	limit, err = store.effectiveAiAccountCreationLimit(ctx, sqlNullInt64NonValid())
	if err != nil || limit != 7 {
		t.Fatalf("settings fallback = (%d, %v), want (7, nil)", limit, err)
	}
	invalid := int64(1_000_001)
	if _, err := store.effectiveAiAccountCreationLimit(ctx, sqlNullInt64(invalid)); err == nil {
		t.Fatal("out-of-range owner override must fail")
	} else {
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Message != "AI 账户数量限制配置无效" {
			t.Fatalf("invalid limit error = %v, want AI 账户数量限制配置无效", err)
		}
	}
}

func defaultUserAccountLimitValue() int64 { return defaultUserAiAccountLimit }

// TestCreateInvalidationChannels mirrors repositories.ts:2440-2443: one
// lookup flush for the new account, one group-account-ids flush and one
// gateway runtime invalidation with the 'account_created' reason; the delete-
// only channels (resource-authorization lookups, quota) stay silent.
func TestCreateInvalidationChannels(t *testing.T) {
	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-0174d")
	env.seedOwnerWithLimit(t, "owner-0174d", nil)
	invalidator := &recordingInvalidator{}
	env.store.SetCacheInvalidator(invalidator)

	result, err := env.store.Create(context.Background(), bug0174CreateInput("chain-d"), AccessScope{ViewerID: "owner-0174d"})
	if err != nil {
		t.Fatal(err)
	}
	lookups, reasons := invalidator.snapshot()
	if len(lookups) != 1 || lookups[0] != result.ID {
		t.Fatalf("lookup flushes = %v, want [%s]", lookups, result.ID)
	}
	if len(reasons) != 1 || reasons[0] != "account_created" {
		t.Fatalf("runtime reasons = %v, want [account_created]", reasons)
	}
	groupIdsFlushes, authzLookups, quotaReasons := invalidator.deleteSnapshot()
	if groupIdsFlushes != 1 {
		t.Fatalf("group-account-ids flushes = %d, want 1", groupIdsFlushes)
	}
	if authzLookups != 0 || len(quotaReasons) != 0 {
		t.Fatalf("create must not touch the delete-only channels: authz=%d quota=%v", authzLookups, quotaReasons)
	}
	// The bound-group dirty marker (SQLite arm, refreshGroupAccountStatsAfterWrite
	// groupIds branch).
	if got := env.queryCell(t, `SELECT reason FROM group_account_stats_dirty WHERE group_id = ?`, result.GroupID); got != "account_created" {
		t.Fatalf("group dirty reason = %q, want account_created", got)
	}
	if result.GroupID != "grp-default-owner-0174d" {
		t.Fatalf("CreateResult.GroupID = %q, want the seeded default group", result.GroupID)
	}
}

// TestCreateBalanceNextRefreshSeeded mirrors the G2-m5 archive check
// (repositories.ts:2330,2388 → accountBalanceWriteValues): an enabled balance
// query seeds balance_query_next_refresh_at at the creation instant; the
// disabled default leaves it NULL, and stream_failure_count starts at 0 with
// no window in both arms.
func TestCreateBalanceNextRefreshSeeded(t *testing.T) {
	enabled := bug0174CreateInput("chain-e1")
	enabled.BalanceQueryEnabled = true
	canonical := `{"type":"ech0"}`
	enabled.BalanceQueryConfigCanonical = &canonical

	env := newTestEnv(t)
	env.seedProviderAndDefaultGroup(t, "owner-0174e")
	env.seedOwnerWithLimit(t, "owner-0174e", nil)
	result, err := env.store.Create(context.Background(), enabled, AccessScope{ViewerID: "owner-0174e"})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.queryCell(t, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`, result.ID); got == "" {
		t.Fatal("enabled balance query must seed balance_query_next_refresh_at at creation")
	}
	if got := env.queryCell(t, `SELECT CAST(stream_failure_count AS TEXT) FROM accounts WHERE id = ?`, result.ID); got != "0" {
		t.Fatalf("stream_failure_count = %q, want 0", got)
	}
	if got := env.queryCell(t, `SELECT stream_failure_window_started_at FROM accounts WHERE id = ?`, result.ID); got != "" {
		t.Fatalf("stream_failure_window_started_at = %q, want NULL", got)
	}

	env2 := newTestEnv(t)
	env2.seedProviderAndDefaultGroup(t, "owner-0174f")
	env2.seedOwnerWithLimit(t, "owner-0174f", nil)
	disabled, err := env2.store.Create(context.Background(), bug0174CreateInput("chain-f"), AccessScope{ViewerID: "owner-0174f"})
	if err != nil {
		t.Fatal(err)
	}
	if got := env2.queryCell(t, `SELECT balance_query_next_refresh_at FROM accounts WHERE id = ?`, disabled.ID); got != "" {
		t.Fatalf("disabled balance query refresh-at = %q, want NULL", got)
	}
}
