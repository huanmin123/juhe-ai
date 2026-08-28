package accountruntime

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T, gate OwnerGate, deps Dependencies) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/account-runtime.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	ddls := []string{
		`CREATE TABLE system_accounts (id TEXT PRIMARY KEY, status TEXT NOT NULL, image_generation_enabled INTEGER NOT NULL DEFAULT 0, request_limits_json TEXT)`,
		`CREATE TABLE route_strategies (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, mode TEXT NOT NULL, status TEXT NOT NULL, config_json TEXT)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, enabled INTEGER NOT NULL)`,
		`CREATE TABLE route_strategy_groups (id TEXT PRIMARY KEY, route_strategy_id TEXT NOT NULL, system_account_id TEXT NOT NULL, group_id TEXT NOT NULL, priority INTEGER NOT NULL, weight INTEGER NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT, resource_id TEXT, grantee_system_account_id TEXT, status TEXT, expires_at TEXT)`,
		`CREATE TABLE group_authorization_settings (authorization_id TEXT PRIMARY KEY, system_account_id TEXT, group_id TEXT, enabled INTEGER)`,
		`CREATE TABLE api_keys (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, route_strategy_id TEXT NOT NULL, key_hash TEXT NOT NULL, status TEXT NOT NULL, expires_at TEXT, quota_limits_json TEXT, availability_schedule_json TEXT, availability_schedule_next_check_at TEXT, updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, config_revision INTEGER NOT NULL DEFAULT 1, dispatch_revision INTEGER NOT NULL DEFAULT 1, system_account_id TEXT NOT NULL, status TEXT NOT NULL, schedulable INTEGER NOT NULL DEFAULT 1, account_expires_at TEXT, cooldown_until TEXT, last_error_code TEXT, last_error_message TEXT, last_error_trace_id TEXT, cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0, cooldown_retest_observation_started_at TEXT, cooldown_retest_generation TEXT, cooldown_retest_last_at TEXT, cooldown_retest_last_status_code INTEGER, last_health_check_at TEXT, next_health_check_at TEXT, last_health_success_at TEXT, health_check_failure_count INTEGER NOT NULL DEFAULT 0, health_check_failure_started_at TEXT, last_health_check_status_code INTEGER, last_health_check_error_code TEXT, last_health_check_error_message TEXT, last_health_check_trace_id TEXT, stream_failure_count INTEGER NOT NULL DEFAULT 0, stream_failure_window_started_at TEXT, deleted_at TEXT, name TEXT NOT NULL, credentials_encrypted TEXT NOT NULL, type TEXT NOT NULL, provider_code TEXT NOT NULL, protocol_code TEXT NOT NULL, protocol_version TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE account_api_key_runtime_states (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, key_fingerprint TEXT NOT NULL, key_index INTEGER NOT NULL DEFAULT 0, credential_revision TEXT, status TEXT NOT NULL DEFAULT 'active', failure_count INTEGER NOT NULL DEFAULT 0, consecutive_failures INTEGER NOT NULL DEFAULT 0, success_count INTEGER NOT NULL DEFAULT 0, cooldown_until TEXT, next_probe_at TEXT, probe_backoff_seconds INTEGER NOT NULL DEFAULT 0, recovery_started_at TEXT, last_attempt_at TEXT, last_success_at TEXT, last_failure_at TEXT, last_error_code TEXT, last_error_message TEXT, last_trace_id TEXT, last_probe_at TEXT, probe_claim_token TEXT, probe_claimed_until TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, UNIQUE(account_id,key_fingerprint))`,
		`CREATE TABLE account_api_key_pool_probe_cursors (account_id TEXT NOT NULL, purpose TEXT NOT NULL, last_completed_key_fingerprint TEXT, key_set_fingerprint TEXT NOT NULL, config_revision INTEGER NOT NULL, dispatch_revision INTEGER, cooldown_generation TEXT, source_config_revision INTEGER, updated_at TEXT NOT NULL, PRIMARY KEY(account_id,purpose))`,
		`CREATE TABLE api_key_schedule_status_events (event_key TEXT PRIMARY KEY, api_key_id TEXT NOT NULL, status TEXT NOT NULL, executed_at TEXT NOT NULL)`,
	}
	for _, ddl := range ddls {
		if _, err := db.Exec(ddl); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	s, err := New(db, SQLite, "", gate, deps)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
	return s, db
}

func seedAccount(t *testing.T, db *sql.DB, status string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO system_accounts(id,status) VALUES('sys-1','active')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO accounts(id,system_account_id,status,schedulable,name,credentials_encrypted,type,provider_code,protocol_code,protocol_version) VALUES('acct-1','sys-1',?,1,'Account','opaque','api_key','openai','openai','v1')`, status)
	if err != nil {
		t.Fatal(err)
	}
}

func poolAccount() Account {
	return Account{ID: "acct-1", SystemAccountID: "sys-1", ConfigRevision: 1, DispatchRevision: 1, Type: "api_key", SelectedKeyFingerprint: hashKey("sk-one"), SelectedKeyIndex: 0, APIKeys: []APIKeyEntry{{Key: "sk-one", Fingerprint: hashKey("sk-one"), Index: 0}, {Key: "sk-two", Fingerprint: hashKey("sk-two"), Index: 1}}}
}

func TestRuntimeCASAndOwnerGate(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true}, Dependencies{})
	defer db.Close()
	seedAccount(t, db, "active")
	ctx := context.Background()
	if _, err := s.RecordAccountAPIKeyFailure(ctx, poolAccount(), FailureInput{Status: RuntimeTemporaryUnavailable}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("partial owner gate err=%v", err)
	}
	s.gate.NodeWriterStopped = true
	result, err := s.RecordAccountAPIKeyFailure(ctx, poolAccount(), FailureInput{StatusCode: 429, ErrorMessage: "rate limited", ObservedAt: "2030-01-01T00:00:00Z"})
	if err != nil || !result.Changed {
		t.Fatalf("failure result=%+v err=%v", result, err)
	}
	var status string
	var failures int
	if err := db.QueryRow(`SELECT status,failure_count FROM account_api_key_runtime_states WHERE account_id='acct-1'`).Scan(&status, &failures); err != nil {
		t.Fatal(err)
	}
	if status != string(RuntimeTemporaryUnavailable) || failures != 1 {
		t.Fatalf("state status=%s failures=%d", status, failures)
	}
	result, err = s.RecordAccountAPIKeySuccess(ctx, poolAccount(), SuccessInput{ExpectedStatus: RuntimeTemporaryUnavailable, ExpectedStateUpdatedAt: "2000-01-01T00:00:00Z"})
	if err != nil || result.Changed {
		t.Fatalf("stale success result=%+v err=%v", result, err)
	}
	result, err = s.RecordAccountAPIKeySuccess(ctx, poolAccount(), SuccessInput{ObservedAt: "2030-01-01T00:00:01Z"})
	if err != nil || !result.Changed {
		t.Fatalf("success result=%+v err=%v", result, err)
	}
	result, err = s.DeferAccountAPIKeyProbe(ctx, poolAccount(), ProbeDeferInput{ExpectedStatus: RuntimeTemporaryUnavailable, DelaySeconds: 4, ObservedAt: "2030-01-01T00:00:02Z"})
	if err != nil || result.Changed {
		t.Fatalf("defer active state result=%+v err=%v", result, err)
	}
}

func TestProbeCursorAndLeaseFailClosed(t *testing.T) {
	resolver := CredentialResolverFunc(func(context.Context, Account) ([]APIKeyEntry, error) {
		return []APIKeyEntry{{Key: "sk-one", Fingerprint: hashKey("sk-one"), Index: 0}, {Key: "sk-two", Fingerprint: hashKey("sk-two"), Index: 1}}, nil
	})
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{Credentials: resolver})
	defer db.Close()
	seedAccount(t, db, "active")
	if _, err := db.Exec(`INSERT INTO account_api_key_runtime_states(id,system_account_id,account_id,key_fingerprint,key_index,status,next_probe_at,created_at,updated_at) VALUES('state-1','sys-1','acct-1',?,0,'temporary_unavailable','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z','2029-01-01T00:00:00Z')`, hashKey("sk-one")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccountAPIKeyPoolProbeCursor(context.Background(), ProbeCursor{AccountID: "acct-1", Purpose: HealthCheck, KeySetFingerprint: "set-1", ConfigRevision: 1}, "save"); err != nil {
		t.Fatal(err)
	}
	cur, err := s.ReadAccountAPIKeyPoolProbeCursor(context.Background(), "acct-1", HealthCheck)
	if err != nil || cur.KeySetFingerprint != "set-1" {
		t.Fatalf("cursor=%+v err=%v", cur, err)
	}
	candidates, err := s.ListAccountAPIKeyRuntimeStatesDueForProbe(context.Background(), 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	if candidates[0].APIKey != "sk-one" || candidates[0].ProbeClaimToken == "" {
		t.Fatalf("candidate=%+v", candidates[0])
	}
	second, err := s.ListAccountAPIKeyRuntimeStatesDueForProbe(context.Background(), 10)
	if err != nil || len(second) != 0 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	noResolver, _ := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{})
	defer noResolver.db.Close()
	seedAccount(t, noResolver.db, "active")
	if _, err := noResolver.ListAccountAPIKeyRuntimeStatesDueForProbe(context.Background(), 1); !errors.Is(err, ErrOutstandingCredentialResolver) {
		t.Fatalf("resolver fail closed err=%v", err)
	}
}

func TestGatewayValidationAndQuotaOwner(t *testing.T) {
	s, db := testStore(t, OwnerGate{}, Dependencies{})
	defer db.Close()
	seedAccount(t, db, "active")
	if _, err := db.Exec(`INSERT INTO route_strategies(id,system_account_id,mode,status) VALUES('route-1','sys-1','normal','active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,provider_code,enabled) VALUES('group-1','sys-1','openai',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO route_strategy_groups(id,route_strategy_id,system_account_id,group_id,priority,weight,status,created_at) VALUES('binding-1','route-1','sys-1','group-1',1,1,'active','2029-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,quota_limits_json) VALUES('key-1','sys-1','route-1',?,'active','{"total":{"enabled":true,"limit":1}}')`, hashKey("sk-valid")); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) }
	row, err := s.ValidateGatewayAPIKey(context.Background(), "sk-valid")
	if err != nil || row.SelectedGroupID != "group-1" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	if _, err := s.CheckAPIKeyQuota(context.Background(), row); !errors.Is(err, ErrOutstandingStatsOwner) {
		t.Fatalf("quota fail closed err=%v", err)
	}
	if _, err := s.ValidateGatewayAPIKey(context.Background(), "not-a-key"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid key err=%v", err)
	}
}

func TestAccountTransitionsAndPorts(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, Dependencies{
		QuotaUsage: QuotaUsageFunc(func(context.Context, string, string, time.Time, *int) (QuotaCosts, error) {
			return QuotaCosts{Total: 3}, nil
		}),
		Schedule: ScheduleEvaluatorFunc(func(context.Context, string, time.Time) (ScheduleDecision, error) {
			return ScheduleDecision{Status: "disabled", EventKey: "close", NextCheckAt: "2030-01-01T00:01:00Z"}, nil
		}),
	})
	defer db.Close()
	seedAccount(t, db, "active")
	if _, err := db.Exec(`INSERT INTO api_keys(id,system_account_id,route_strategy_id,key_hash,status,availability_schedule_json,availability_schedule_next_check_at) VALUES('sched-key','sys-1','route-missing',?,'active','{}','2029-01-01T00:00:00Z')`, hashKey("sk-schedule")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkAccountTemporaryUnavailable(context.Background(), TemporaryUnavailableInput{Account: Account{ID: "acct-1"}, Reason: "transport"}); err != nil {
		t.Fatal(err)
	}
	result, err := s.ClearAccountFailureState(context.Background(), ClearFailureInput{AccountID: "acct-1"})
	if err != nil || !result.Changed {
		t.Fatalf("clear result=%+v err=%v", result, err)
	}
	result, err = s.RecordAccountStreamFailure(context.Background(), StreamFailureInput{AccountID: "acct-1", ThresholdCount: 2, ThresholdWindowMinutes: 10, Action: "disable", Reason: "stream closed"})
	if err != nil || result.Count != 1 || result.Triggered {
		t.Fatalf("stream first=%+v err=%v", result, err)
	}
	result, err = s.RecordAccountStreamFailure(context.Background(), StreamFailureInput{AccountID: "acct-1", ThresholdCount: 2, ThresholdWindowMinutes: 10, Action: "disable", Reason: "stream closed"})
	if err != nil || result.Count != 2 || !result.Triggered {
		t.Fatalf("stream second=%+v err=%v", result, err)
	}
	if _, err := s.ClearAccountStreamFailureState(context.Background(), "acct-1"); err != nil {
		t.Fatal(err)
	}
	limitsKey := GatewayAPIKey{ID: "k", SystemAccountID: "sys", QuotaLimitsJSON: `{"total":{"enabled":true,"limit":2}}`}
	decision, err := s.CheckAPIKeyQuota(context.Background(), limitsKey)
	if err != nil || decision.Allowed {
		t.Fatalf("quota decision=%+v err=%v", decision, err)
	}
	result, err = s.SyncAPIKeyAvailabilityScheduleStatuses(context.Background())
	if err != nil || !result.Changed {
		t.Fatalf("schedule result=%+v err=%v", result, err)
	}
}

func TestPostgresQualificationAndPlaceholder(t *testing.T) {
	s, db := testStore(t, OwnerGate{}, Dependencies{})
	defer db.Close()
	s.mode = Postgres
	s.schema = "juhe_business"
	if got := s.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("table=%q", got)
	}
	if got := s.bind("SELECT * FROM accounts WHERE id=? AND status=?"); got != "SELECT * FROM accounts WHERE id=$1 AND status=$2" {
		t.Fatalf("bind=%q", got)
	}
}
