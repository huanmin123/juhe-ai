package accounthealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// projectionBusinessSchema 是投影测试所需的业务库最小 schema（列集与列型对齐
// maintenance sqlite_schema 的 accounts/receipts/cursors/outcome outbox 子集）。
const projectionBusinessSchema = `
CREATE TABLE accounts (
  id TEXT PRIMARY KEY,
  status TEXT NOT NULL DEFAULT '',
  schedulable INTEGER NOT NULL DEFAULT 0,
  config_revision INTEGER NOT NULL,
  dispatch_revision INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  credentials_encrypted TEXT NOT NULL DEFAULT '',
  balance_query_enabled INTEGER NOT NULL DEFAULT 0,
  balance_query_config_json TEXT NOT NULL DEFAULT '{}',
  balance_query_next_refresh_at TEXT,
  availability_schedule_json TEXT,
  availability_schedule_next_check_at TEXT,
  cooldown_until TEXT,
  cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
  cooldown_retest_observation_started_at TEXT,
  cooldown_retest_generation TEXT,
  cooldown_retest_last_at TEXT,
  cooldown_retest_last_status_code INTEGER,
  last_error_code TEXT,
  last_error_message TEXT,
  last_error_trace_id TEXT,
  last_health_check_at TEXT,
  next_health_check_at TEXT,
  last_health_success_at TEXT,
  last_health_check_status_code INTEGER,
  last_health_check_error_code TEXT,
  last_health_check_error_message TEXT,
  last_health_check_trace_id TEXT,
  health_check_failure_count INTEGER NOT NULL DEFAULT 0,
  health_check_failure_started_at TEXT,
  authorization_instance_source_account_id TEXT,
  deleted_at TEXT,
  created_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE account_health_jobs_input_versions (
  account_id TEXT NOT NULL,
  input_version INTEGER NOT NULL,
  current_version INTEGER NOT NULL,
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (account_id, input_version)
);
CREATE TABLE account_health_projection_receipts (
  outcome_id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL,
  input_version INTEGER NOT NULL CHECK (input_version >= 1),
  disposition TEXT NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
  reason TEXT,
  applied_at TEXT NOT NULL
);
CREATE TABLE account_health_projection_cursors (
  consumer_key TEXT PRIMARY KEY,
  observed_at TEXT,
  outcome_id TEXT,
  updated_at TEXT NOT NULL,
  CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
);
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
  updated_at_ms INTEGER NOT NULL,
  UNIQUE (projection_key, dedupe_key)
);
`

var projectionFixtureNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

type projectionFixture struct {
	t         *testing.T
	store     *Store
	business  *sql.DB
	projector *OutcomeProjector
}

func newProjectionFixture(t *testing.T) *projectionFixture {
	t.Helper()
	directory := t.TempDir()
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(directory, "jobs.sqlite3")})
	if err != nil {
		t.Fatalf("打开 jobs store 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("初始化 jobs store schema 失败: %v", err)
	}
	business, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(directory, "business.sqlite3"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开业务库失败: %v", err)
	}
	t.Cleanup(func() { _ = business.Close() })
	if _, err := business.Exec(projectionBusinessSchema); err != nil {
		t.Fatalf("初始化业务库 schema 失败: %v", err)
	}
	handle, err := NewProjectionBusinessDB(business, false)
	if err != nil {
		t.Fatalf("装配业务库句柄失败: %v", err)
	}
	projector, err := NewOutcomeProjector(store, OutcomeProjectorConfig{
		Business:         handle,
		CredentialSecret: "projection-test-secret",
		PollInterval:     DefaultProjectionPollInterval,
		BatchSize:        DefaultProjectionBatchSize,
		Logger:           slog.Default(),
		Now:              func() time.Time { return projectionFixtureNow },
	})
	if err != nil {
		t.Fatalf("装配投影器失败: %v", err)
	}
	return &projectionFixture{t: t, store: store, business: business, projector: projector}
}

func (f *projectionFixture) seedAccount(t *testing.T, values map[string]any) {
	t.Helper()
	row := map[string]any{
		"id":               "acct-1",
		"status":           "active",
		"config_revision":  int64(5),
		"dispatch_revision": int64(7),
		"created_at":       "2026-09-01T00:00:00Z",
		"updated_at":       "2026-09-01T00:00:00Z",
	}
	for column, value := range values {
		row[column] = value
	}
	columns := make([]string, 0, len(row))
	args := make([]any, 0, len(row))
	for column, value := range row {
		columns = append(columns, column)
		args = append(args, value)
	}
	sort.Strings(columns)
	sortedArgs := make([]any, len(columns))
	for index, column := range columns {
		sortedArgs[index] = row[column]
	}
	query := "INSERT INTO accounts (" + columns[0]
	for _, column := range columns[1:] {
		query += ", " + column
	}
	query += ") VALUES (?"
	for range columns[1:] {
		query += ", ?"
	}
	query += ")"
	if _, err := f.business.Exec(query, sortedArgs...); err != nil {
		t.Fatalf("写入测试账户失败: %v", err)
	}
	if _, err := f.business.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, input_version, current_version, updated_at) VALUES ('acct-1', 1, 1, '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatalf("写入测试 input version 失败: %v", err)
	}
}

func (f *projectionFixture) insertOutcome(observedAt time.Time, outcome Outcome) {
	f.t.Helper()
	outcome.ObservedAt = observedAt
	payload, err := json.Marshal(outcome)
	if err != nil {
		f.t.Fatalf("编码测试 outcome 失败: %v", err)
	}
	_, err = f.store.db.Exec(`INSERT INTO account_health_outcomes (outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, status_code, error_code, error_message, payload)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		outcome.OutcomeID, outcome.RequestID, outcome.AccountID, outcome.Outcome, observedAt.UTC().Format(time.RFC3339Nano), outcome.InputVersion, outcome.ConfigRevision, outcome.DispatchRevision, nil, nil, nil, string(payload))
	if err != nil {
		f.t.Fatalf("写入测试 outcome 行失败: %v", err)
	}
}

func (f *projectionFixture) drain(t *testing.T) ProjectionDrainResult {
	t.Helper()
	result, err := f.projector.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("drain 失败: %v", err)
	}
	return result
}

func (f *projectionFixture) accountRow(t *testing.T) map[string]any {
	t.Helper()
	row := f.business.QueryRowContext(context.Background(), `SELECT status, schedulable, dispatch_revision, config_revision, availability_schedule_next_check_at, balance_query_next_refresh_at, cooldown_until, cooldown_retest_failure_count, cooldown_retest_generation, last_error_code, last_health_check_at, next_health_check_at, last_health_success_at, health_check_failure_count, last_health_check_status_code FROM accounts WHERE id = 'acct-1'`)
	values := map[string]any{}
	var status string
	var schedulable, dispatchRevision, configRevision, cooldownRetestFailureCount, healthCheckFailureCount int64
	var scheduleNextCheckAt, balanceNextRefreshAt, cooldownUntil, cooldownGeneration, lastErrorCode, lastHealthCheckAt, nextHealthCheckAt, lastHealthSuccessAt sql.NullString
	var lastHealthCheckStatusCode sql.NullInt64
	if err := row.Scan(&status, &schedulable, &dispatchRevision, &configRevision, &scheduleNextCheckAt, &balanceNextRefreshAt, &cooldownUntil, &cooldownRetestFailureCount, &cooldownGeneration, &lastErrorCode, &lastHealthCheckAt, &nextHealthCheckAt, &lastHealthSuccessAt, &healthCheckFailureCount, &lastHealthCheckStatusCode); err != nil {
		t.Fatalf("读取账户行失败: %v", err)
	}
	values["status"] = status
	values["schedulable"] = schedulable
	values["dispatch_revision"] = dispatchRevision
	values["config_revision"] = configRevision
	values["availability_schedule_next_check_at"] = scheduleNextCheckAt.String
	values["balance_query_next_refresh_at"] = balanceNextRefreshAt.String
	values["cooldown_until"] = cooldownUntil.String
	values["cooldown_retest_failure_count"] = cooldownRetestFailureCount
	values["cooldown_retest_generation"] = cooldownGeneration.String
	values["last_error_code"] = lastErrorCode.String
	values["last_health_check_at"] = lastHealthCheckAt.String
	values["next_health_check_at"] = nextHealthCheckAt.String
	values["last_health_success_at"] = lastHealthSuccessAt.String
	values["health_check_failure_count"] = healthCheckFailureCount
	values["last_health_check_status_code"] = lastHealthCheckStatusCode.Int64
	return values
}

func (f *projectionFixture) receipt(t *testing.T, outcomeID string) (string, string) {
	t.Helper()
	var disposition, reason sql.NullString
	err := f.business.QueryRowContext(context.Background(), `SELECT disposition, reason FROM account_health_projection_receipts WHERE outcome_id = ?`, outcomeID).Scan(&disposition, &reason)
	if err != nil {
		t.Fatalf("读取 receipt 失败: %v", err)
	}
	return disposition.String, reason.String
}

func (f *projectionFixture) receiptCount(t *testing.T, outcomeID string) int {
	t.Helper()
	var count int
	if err := f.business.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM account_health_projection_receipts WHERE outcome_id = ?`, outcomeID).Scan(&count); err != nil {
		t.Fatalf("统计 receipt 失败: %v", err)
	}
	return count
}

func (f *projectionFixture) outboxCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.business.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM account_circuit_outbox WHERE projection_key = 'account_circuit_runtime_v1'`).Scan(&count); err != nil {
		t.Fatalf("统计 circuit outbox 失败: %v", err)
	}
	return count
}

// cooldownSuccessOutcome 构造一条 Go scheduler 冷却复测成功的真实投影形态。
func cooldownSuccessOutcome(observedAt time.Time, fence CooldownFence) Outcome {
	return Outcome{
		OutcomeID:        "outcome-cooldown-success",
		RequestID:        "request-cooldown-success",
		AccountID:        "acct-1",
		Outcome:          OutcomeSuccess,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
		StatusCode:       200,
		NextDueAt:        ptrTime(observedAt.Add(time.Hour)),
		Projection: &Projection{
			TargetAccountID:       "acct-1",
			TransitionKind:        "cooldown_success",
			InputVersion:          1,
			ConfigRevision:        5,
			DispatchRevision:      7,
			ExpectedAccountStatus: "temporary_unavailable",
			ExpectedCooldownFence: &fence,
			Values: map[string]any{
				"last_health_check_at":           observedAt.Format(time.RFC3339Nano),
				"last_health_success_at":         observedAt.Format(time.RFC3339Nano),
				"last_health_check_status_code":  200,
			},
		},
	}
}

// TestProjectionCooldownSuccessRestoresActive：cooldown_success 恢复 active、
// 清冷却、写健康列、推进 dispatch revision（57535d3ad）并落 applied receipt；
// 重放 drain 幂等。
func TestProjectionCooldownSuccessRestoresActive(t *testing.T) {
	fixture := newProjectionFixture(t)
	observation := projectionFixtureNow.Add(-2 * time.Hour)
	fixture.seedAccount(t, map[string]any{
		"status":                                   "temporary_unavailable",
		"schedulable":                              1,
		"cooldown_until":                           projectionFixtureNow.Add(time.Hour).Format(time.RFC3339Nano),
		"cooldown_retest_failure_count":            3,
		"cooldown_retest_observation_started_at":   fenceGuardText(observation),
		"cooldown_retest_generation":               "gen-1",
		"cooldown_retest_last_at":                  observation.Format(time.RFC3339Nano),
		"cooldown_retest_last_status_code":         503,
		"last_error_code":                          "upstream_5xx",
		"last_error_message":                       "上游失败",
		"health_check_failure_count":               2,
	})
	observed := projectionFixtureNow.Add(-time.Minute)
	fixture.insertOutcome(observed, cooldownSuccessOutcome(observed, CooldownFence{ObservationStartedAt: observation, Generation: "gen-1"}))

	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("Processed = %d, 期望 1", result.Processed)
	}
	row := fixture.accountRow(t)
	if row["status"] != "active" {
		t.Fatalf("status = %v, 期望 active", row["status"])
	}
	if row["schedulable"].(int64) != 1 {
		t.Fatalf("schedulable = %v, 期望 1", row["schedulable"])
	}
	if row["cooldown_until"] != "" || row["cooldown_retest_generation"] != "" || row["cooldown_retest_failure_count"].(int64) != 0 {
		t.Fatalf("冷却列未清空: %v", row)
	}
	if row["last_error_code"] != "" {
		t.Fatalf("last_error_code 未清空: %v", row["last_error_code"])
	}
	if row["last_health_check_at"] == "" || row["last_health_success_at"] == "" || row["next_health_check_at"] == "" {
		t.Fatalf("健康列未写入: %v", row)
	}
	if row["health_check_failure_count"].(int64) != 0 {
		t.Fatalf("health_check_failure_count = %v, 期望 0", row["health_check_failure_count"])
	}
	// restoresDispatch：无授权源 → 家族推进（本例家族即自身）。
	if row["dispatch_revision"].(int64) != 8 {
		t.Fatalf("dispatch_revision = %v, 期望 8", row["dispatch_revision"])
	}
	if row["config_revision"].(int64) != 5 {
		t.Fatalf("config_revision 不得变化: %v", row["config_revision"])
	}
	if fixture.outboxCount(t) != 1 {
		t.Fatalf("circuit outbox 行数 = %d, 期望 1", fixture.outboxCount(t))
	}
	disposition, reason := fixture.receipt(t, "outcome-cooldown-success")
	if disposition != "applied" || reason != "" {
		t.Fatalf("receipt = %s/%s, 期望 applied", disposition, reason)
	}

	// 幂等重放：游标已推进，不再产生任何变更。
	replay := fixture.drain(t)
	if replay.Processed != 0 {
		t.Fatalf("重放 Processed = %d, 期望 0", replay.Processed)
	}
	if fixture.receiptCount(t, "outcome-cooldown-success") != 1 {
		t.Fatalf("重放后 receipt 数量不为 1")
	}
	if row := fixture.accountRow(t); row["dispatch_revision"].(int64) != 8 {
		t.Fatalf("重放改变了 dispatch_revision: %v", row["dispatch_revision"])
	}
}

// activationSuccessOutcome 构造一条 activation_success 投影。
func activationSuccessOutcome(observedAt time.Time) Outcome {
	return Outcome{
		OutcomeID:        "outcome-activation-success",
		RequestID:        "request-activation-success",
		AccountID:        "acct-1",
		Outcome:          OutcomeSuccess,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
		StatusCode:       200,
		NextDueAt:        ptrTime(observedAt.Add(time.Hour)),
		Projection: &Projection{
			TargetAccountID:       "acct-1",
			TransitionKind:        "activation_success",
			InputVersion:          1,
			ConfigRevision:        5,
			DispatchRevision:      7,
			ExpectedAccountStatus: "pending_test",
			Values: map[string]any{
				"last_health_check_at":          observedAt.Format(time.RFC3339Nano),
				"last_health_success_at":        observedAt.Format(time.RFC3339Nano),
				"last_health_check_status_code": 200,
			},
		},
	}
}

// TestProjectionActivationSuccessSchedulesBalance：pending_test → active，
// 无余额配置的 api_key 账户安排余额探测，dispatch revision 推进。
func TestProjectionActivationSuccessSchedulesBalance(t *testing.T) {
	fixture := newProjectionFixture(t)
	credentials, err := EncryptV1Envelope("projection-test-secret", []byte(`{"api_keys":["sk-test-1"]}`))
	if err != nil {
		t.Fatalf("加密测试凭据失败: %v", err)
	}
	fixture.seedAccount(t, map[string]any{
		"status":                  "pending_test",
		"schedulable":             0,
		"type":                    "api_key",
		"credentials_encrypted":   credentials,
		"balance_query_enabled":   0,
		"balance_query_config_json": "{}",
	})
	observed := projectionFixtureNow.Add(-time.Minute)
	fixture.insertOutcome(observed, activationSuccessOutcome(observed))

	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("Processed = %d, 期望 1", result.Processed)
	}
	row := fixture.accountRow(t)
	if row["status"] != "active" {
		t.Fatalf("status = %v, 期望 active", row["status"])
	}
	if row["schedulable"].(int64) != 1 {
		t.Fatalf("schedulable = %v, 期望 1", row["schedulable"])
	}
	if row["balance_query_next_refresh_at"] == "" {
		t.Fatalf("balance_query_next_refresh_at 未安排")
	}
	if row["dispatch_revision"].(int64) != 8 {
		t.Fatalf("dispatch_revision = %v, 期望 8", row["dispatch_revision"])
	}
	if row["availability_schedule_next_check_at"] != "" {
		t.Fatalf("无计划时 availability_schedule_next_check_at 应为 NULL: %v", row["availability_schedule_next_check_at"])
	}
	disposition, _ := fixture.receipt(t, "outcome-activation-success")
	if disposition != "applied" {
		t.Fatalf("receipt = %s, 期望 applied", disposition)
	}
}

// TestProjectionActivationSuccessOutsideScheduleWindow：账户处于计划外时段时
// 不恢复 active/dispatch（57535d3ad），但保留 schedulable 并写下一次计划检查。
func TestProjectionActivationSuccessOutsideScheduleWindow(t *testing.T) {
	fixture := newProjectionFixture(t)
	schedule := `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"01:00","end":"02:00"}]}`
	credentials, err := EncryptV1Envelope("projection-test-secret", []byte(`{"api_keys":["sk-test-1"]}`))
	if err != nil {
		t.Fatalf("加密测试凭据失败: %v", err)
	}
	fixture.seedAccount(t, map[string]any{
		"status":                    "pending_test",
		"schedulable":               0,
		"type":                      "api_key",
		"credentials_encrypted":     credentials,
		"balance_query_enabled":     0,
		"balance_query_config_json": "{}",
		"availability_schedule_json": schedule,
	})
	observed := projectionFixtureNow.Add(-time.Minute)
	fixture.insertOutcome(observed, activationSuccessOutcome(observed))

	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("Processed = %d, 期望 1", result.Processed)
	}
	row := fixture.accountRow(t)
	if row["status"] != "disabled" {
		t.Fatalf("status = %v, 期望 disabled（计划外时段）", row["status"])
	}
	if row["schedulable"].(int64) != 1 {
		t.Fatalf("schedulable = %v, 期望 1", row["schedulable"])
	}
	if row["availability_schedule_next_check_at"] == "" {
		t.Fatalf("availability_schedule_next_check_at 未写入")
	}
	nextCheck, err := time.Parse(time.RFC3339, row["availability_schedule_next_check_at"].(string))
	if err != nil {
		t.Fatalf("解析 next check 失败: %v", err)
	}
	if !nextCheck.After(projectionFixtureNow) {
		t.Fatalf("next check %v 应晚于当前时刻", nextCheck)
	}
	if row["dispatch_revision"].(int64) != 7 {
		t.Fatalf("计划外时段不得推进 dispatch_revision: %v", row["dispatch_revision"])
	}
	if fixture.outboxCount(t) != 0 {
		t.Fatalf("计划外时段不得写 circuit outbox")
	}
	if row["balance_query_next_refresh_at"] != "" {
		t.Fatalf("计划外时段不得安排余额探测")
	}
	disposition, _ := fixture.receipt(t, "outcome-activation-success")
	if disposition != "applied" {
		t.Fatalf("receipt = %s, 期望 applied", disposition)
	}
}

// TestProjectionCASStaleSkipsAndStaysIdempotent：expected status 与业务行不一致
// 时 CAS stale 终态跳过；receipt 幂等且账户行不变。
func TestProjectionCASStaleSkipsAndStaysIdempotent(t *testing.T) {
	fixture := newProjectionFixture(t)
	// 账户实际是 active，投影 expected pending_test → expected_account_status_stale。
	fixture.seedAccount(t, map[string]any{"status": "active", "schedulable": 1})
	observed := projectionFixtureNow.Add(-time.Minute)
	fixture.insertOutcome(observed, activationSuccessOutcome(observed))

	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("stale 也推进游标：Processed = %d, 期望 1", result.Processed)
	}
	disposition, reason := fixture.receipt(t, "outcome-activation-success")
	if disposition != "stale" || reason != "expected_account_status_stale" {
		t.Fatalf("receipt = %s/%s, 期望 stale/expected_account_status_stale", disposition, reason)
	}
	row := fixture.accountRow(t)
	if row["status"] != "active" || row["dispatch_revision"].(int64) != 7 {
		t.Fatalf("stale 不得改变账户行: %v", row)
	}
	if fixture.outboxCount(t) != 0 {
		t.Fatalf("stale 不得写 circuit outbox")
	}
	// 幂等重放：receipt 命中，不产生第二条 receipt。
	replay := fixture.drain(t)
	if replay.Processed != 0 {
		t.Fatalf("重放 Processed = %d, 期望 0", replay.Processed)
	}
	if fixture.receiptCount(t, "outcome-activation-success") != 1 {
		t.Fatalf("重放后 receipt 数量不为 1")
	}
}

// TestProjectionHealthFailureKeepsActive：health_failure 只更新失败与健康元
// 数据，状态保持 active、不进冷却、不推进 dispatch。
func TestProjectionHealthFailureKeepsActive(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"status": "active", "schedulable": 1})
	observed := projectionFixtureNow.Add(-time.Minute)
	started := observed.Add(-30 * time.Minute)
	outcome := Outcome{
		OutcomeID:        "outcome-health-failure",
		RequestID:        "request-health-failure",
		AccountID:        "acct-1",
		Outcome:          OutcomeUpstreamFailed,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
		StatusCode:       503,
		ErrorCode:        "upstream_5xx",
		ErrorMessage:     "上游失败",
		NextDueAt:        ptrTime(observed.Add(5 * time.Minute)),
		FailureCount:     3,
		FailureStartedAt: &started,
		Projection: &Projection{
			TargetAccountID:       "acct-1",
			TransitionKind:        "health_failure",
			InputVersion:          1,
			ConfigRevision:        5,
			DispatchRevision:      7,
			ExpectedAccountStatus: "active",
			Values: map[string]any{
				"last_health_check_at":            observed.Format(time.RFC3339Nano),
				"last_health_check_status_code":   503,
				"last_health_check_error_code":    "upstream_5xx",
				"last_health_check_error_message": "上游失败",
				"health_check_failure_count":      3,
			},
		},
	}
	fixture.insertOutcome(observed, outcome)

	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("Processed = %d, 期望 1", result.Processed)
	}
	row := fixture.accountRow(t)
	if row["status"] != "active" {
		t.Fatalf("status = %v, 期望保持 active", row["status"])
	}
	if row["health_check_failure_count"].(int64) != 3 {
		t.Fatalf("health_check_failure_count = %v, 期望 3", row["health_check_failure_count"])
	}
	if row["next_health_check_at"] == "" || row["last_health_check_at"] == "" {
		t.Fatalf("健康元数据未更新: %v", row)
	}
	if row["cooldown_until"] != "" || row["cooldown_retest_generation"] != "" {
		t.Fatalf("health_failure 不得进入冷却: %v", row)
	}
	if row["dispatch_revision"].(int64) != 7 {
		t.Fatalf("health_failure 不得推进 dispatch_revision: %v", row["dispatch_revision"])
	}
	disposition, _ := fixture.receipt(t, "outcome-health-failure")
	if disposition != "applied" {
		t.Fatalf("receipt = %s, 期望 applied", disposition)
	}
}

// TestProjectionIgnoresOutcomesWithoutProjection：审计行（无 projection）落
// ignored receipt 并推进游标。
func TestProjectionIgnoresOutcomesWithoutProjection(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, nil)
	observed := projectionFixtureNow.Add(-time.Minute)
	fixture.insertOutcome(observed, Outcome{
		OutcomeID:        "outcome-stale-audit",
		RequestID:        "request-stale-audit",
		AccountID:        "acct-1",
		Outcome:          OutcomeStale,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
	})
	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("Processed = %d, 期望 1", result.Processed)
	}
	disposition, reason := fixture.receipt(t, "outcome-stale-audit")
	if disposition != "ignored" || reason != "outcome_has_no_account_projection" {
		t.Fatalf("receipt = %s/%s, 期望 ignored/outcome_has_no_account_projection", disposition, reason)
	}
}

// TestProjectionRejectsTransitionOutcomeMismatch：transition 与 outcome 形态
// 不符时落 rejected receipt（outcomeMatchesTransition 真值表抽查）。
func TestProjectionRejectsTransitionOutcomeMismatch(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"status": "temporary_unavailable", "schedulable": 1})
	observation := projectionFixtureNow.Add(-2 * time.Hour)
	outcome := cooldownSuccessOutcome(projectionFixtureNow.Add(-time.Minute), CooldownFence{ObservationStartedAt: observation, Generation: "gen-1"})
	outcome.Outcome = OutcomeUpstreamFailed
	fixture.insertOutcome(projectionFixtureNow.Add(-time.Minute), outcome)

	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("Processed = %d, 期望 1", result.Processed)
	}
	disposition, reason := fixture.receipt(t, "outcome-cooldown-success")
	if disposition != "rejected" || reason != "projection_outcome_transition_mismatch" {
		t.Fatalf("receipt = %s/%s, 期望 rejected/projection_outcome_transition_mismatch", disposition, reason)
	}
	row := fixture.accountRow(t)
	if row["status"] != "temporary_unavailable" {
		t.Fatalf("rejected 不得改变状态: %v", row["status"])
	}
}

// TestValidateProjectionTruthTable：拒绝真值表的纯函数级对照（归档
// validateProjection 的关键分支）。
func TestValidateProjectionTruthTable(t *testing.T) {
	observed := projectionFixtureNow
	fence := CooldownFence{ObservationStartedAt: observed.Add(-time.Hour), Generation: "gen-1"}
	base := Outcome{
		OutcomeID: "o", RequestID: "r", AccountID: "acct-1", Outcome: OutcomeSuccess,
		ObservedAt: observed, InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
		NextDueAt: ptrTime(observed.Add(time.Hour)),
	}
	projection := func(mutate func(p *Projection)) Outcome {
		outcome := base
		outcome.Projection = &Projection{
			TargetAccountID: "acct-1", TransitionKind: "health_success",
			InputVersion: 1, ConfigRevision: 5, DispatchRevision: 7,
			ExpectedAccountStatus: "active",
		}
		mutate(outcome.Projection)
		return outcome
	}
	cases := []struct {
		name   string
		build  func() Outcome
		expect string // 空 = 可投影
	}{
		{
			name:  "health_success 可投影",
			build: func() Outcome { return projection(func(p *Projection) {}) },
		},
		{
			name: "无 projection → ignored",
			build: func() Outcome {
				outcome := base
				return outcome
			},
			expect: "ignored:outcome_has_no_account_projection",
		},
		{
			name: "transition 不在白名单",
			build: func() Outcome {
				return projection(func(p *Projection) { p.TransitionKind = "unknown" })
			},
			expect: "rejected:projection_transition_not_allowed",
		},
		{
			name: "顶层 fence 不一致",
			build: func() Outcome {
				return projection(func(p *Projection) { p.ConfigRevision = 6 })
			},
			expect: "rejected:projection_top_level_fence_mismatch",
		},
		{
			name: "activation_success 期望状态必须是 pending_test",
			build: func() Outcome {
				return projection(func(p *Projection) { p.TransitionKind = "activation_success" })
			},
			expect: "rejected:projection_activation_expected_status_invalid",
		},
		{
			name: "health_success 期望状态必须是 active",
			build: func() Outcome {
				return projection(func(p *Projection) { p.ExpectedAccountStatus = "pending_test" })
			},
			expect: "rejected:projection_active_expected_status_invalid",
		},
		{
			name: "health_failure 允许 active/pending_test 之外拒绝",
			build: func() Outcome {
				return projection(func(p *Projection) {
					p.TransitionKind = "health_failure"
					p.ExpectedAccountStatus = "temporary_unavailable"
				})
			},
			expect: "rejected:projection_health_failure_expected_status_invalid",
		},
		{
			name: "cooldown 族期望状态必须是 temporary_unavailable/rate_limited",
			build: func() Outcome {
				return projection(func(p *Projection) { p.TransitionKind = "cooldown_success" })
			},
			expect: "rejected:projection_cooldown_expected_status_invalid",
		},
		{
			name: "缺 next_due",
			build: func() Outcome {
				outcome := projection(func(p *Projection) {})
				outcome.NextDueAt = nil
				return outcome
			},
			expect: "rejected:projection_next_due_missing",
		},
		{
			name: "temporary_unavailable 缺输出 fence",
			build: func() Outcome {
				return projection(func(p *Projection) {
					p.TransitionKind = "temporary_unavailable"
					p.CooldownFence = nil
				})
			},
			expect: "rejected:projection_output_cooldown_fence_missing",
		},
		{
			name: "cooldown_success 缺 expected fence",
			build: func() Outcome {
				outcome := projection(func(p *Projection) {
					p.TransitionKind = "cooldown_success"
					p.ExpectedAccountStatus = "temporary_unavailable"
				})
				return outcome
			},
			expect: "rejected:projection_expected_cooldown_fence_missing",
		},
		{
			name: "cooldown_defer expected/output fence 不一致",
			build: func() Outcome {
				return projection(func(p *Projection) {
					p.TransitionKind = "cooldown_defer"
					p.ExpectedAccountStatus = "temporary_unavailable"
					p.ExpectedCooldownFence = &fence
					p.CooldownFence = &CooldownFence{ObservationStartedAt: fence.ObservationStartedAt, Generation: "gen-2"}
				})
			},
			expect: "rejected:projection_cooldown_fence_mismatch",
		},
		{
			name: "cooldown_success outcome 必须是 complete_success",
			build: func() Outcome {
				outcome := projection(func(p *Projection) {
					p.TransitionKind = "cooldown_success"
					p.ExpectedAccountStatus = "temporary_unavailable"
					p.ExpectedCooldownFence = &fence
				})
				outcome.Outcome = OutcomeUpstreamFailed
				return outcome
			},
			expect: "rejected:projection_outcome_transition_mismatch",
		},
		{
			name: "cooldown_defer 接受 probe_task_failure",
			build: func() Outcome {
				outcome := projection(func(p *Projection) {
					p.TransitionKind = "cooldown_defer"
					p.ExpectedAccountStatus = "temporary_unavailable"
					p.ExpectedCooldownFence = &fence
					p.CooldownFence = &fence
				})
				outcome.Outcome = OutcomeTaskFailed
				return outcome
			},
		},
		{
			name: "value 键不在白名单",
			build: func() Outcome {
				return projection(func(p *Projection) { p.Values = map[string]any{"rogue": 1} })
			},
			expect: "rejected:projection_value_not_allowed:rogue",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			validation := validateProjection(testCase.build())
			if testCase.expect == "" {
				if validation.terminal {
					t.Fatalf("期望可投影，实际 %s/%s", validation.disposition, validation.reason)
				}
				return
			}
			expectedParts := splitDispositionExpectation(testCase.expect)
			if !validation.terminal || string(validation.disposition) != expectedParts[0] || validation.reason != expectedParts[1] {
				t.Fatalf("期望 %s，实际 terminal=%v %s/%s", testCase.expect, validation.terminal, validation.disposition, validation.reason)
			}
		})
	}
}

func splitDispositionExpectation(value string) []string {
	for index := 0; index < len(value); index++ {
		if value[index] == ':' {
			return []string{value[:index], value[index+1:]}
		}
	}
	return []string{value, ""}
}

// TestProjectionCursorPagination：批量上限分页与游标单调推进。
func TestProjectionCursorPagination(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, nil)
	first := cooldownSuccessOutcome(projectionFixtureNow.Add(-2*time.Minute), CooldownFence{ObservationStartedAt: projectionFixtureNow.Add(-3 * time.Hour), Generation: "gen-0"})
	first.OutcomeID = "outcome-first"
	first.RequestID = "request-first"
	second := activationSuccessOutcome(projectionFixtureNow.Add(-time.Minute))
	second.OutcomeID = "outcome-second"
	second.RequestID = "request-second"
	fixture.insertOutcome(projectionFixtureNow.Add(-2*time.Minute), first)
	fixture.insertOutcome(projectionFixtureNow.Add(-time.Minute), second)

	batchOne, err := fixture.projector.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("drain 失败: %v", err)
	}
	_ = batchOne
	// 第一轮全量处理后游标应停在第二条。
	var cursorOutcomeID string
	if err := fixture.business.QueryRowContext(context.Background(), `SELECT outcome_id FROM account_health_projection_cursors WHERE consumer_key = ?`, DefaultProjectionConsumerKey).Scan(&cursorOutcomeID); err != nil {
		t.Fatalf("读取游标失败: %v", err)
	}
	if cursorOutcomeID != "outcome-second" {
		t.Fatalf("游标 = %s, 期望 outcome-second", cursorOutcomeID)
	}
	if fixture.receiptCount(t, "outcome-first") != 1 || fixture.receiptCount(t, "outcome-second") != 1 {
		t.Fatalf("两条 outcome 都应有 receipt")
	}
}

// TestProjectionCredentialDecryptFailureAborts：凭据解密失败按归档语义中止
// 投影（错误上抛、游标保留），不静默降级。
func TestProjectionCredentialDecryptFailureAborts(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{
		"status":                    "pending_test",
		"schedulable":               0,
		"type":                      "api_key",
		"credentials_encrypted":     "v1:not-a-valid-envelope",
		"balance_query_enabled":     0,
		"balance_query_config_json": "{}",
	})
	observed := projectionFixtureNow.Add(-time.Minute)
	fixture.insertOutcome(observed, activationSuccessOutcome(observed))

	_, err := fixture.projector.DrainOnce(context.Background())
	if err == nil {
		t.Fatal("凭据解密失败必须上抛")
	}
	var receiptCount int
	if err := fixture.business.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM account_health_projection_receipts`).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 0 {
		t.Fatalf("中止的投影不得落 receipt，实际 %d", receiptCount)
	}
	var cursorCount int
	if err := fixture.business.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM account_health_projection_cursors`).Scan(&cursorCount); err != nil {
		t.Fatal(err)
	}
	if cursorCount != 0 {
		t.Fatalf("中止的投影不得推进游标，实际 %d", cursorCount)
	}
}

// TestProjectionDispatchFamilyAdvance：源账户恢复推进整个家族（根 + 授权实例）。
func TestProjectionDispatchFamilyAdvance(t *testing.T) {
	fixture := newProjectionFixture(t)
	observation := projectionFixtureNow.Add(-2 * time.Hour)
	fixture.seedAccount(t, map[string]any{
		"status":                                 "temporary_unavailable",
		"schedulable":                            1,
		"cooldown_retest_observation_started_at": fenceGuardText(observation),
		"cooldown_retest_generation":             "gen-1",
	})
	// 授权实例行。
	if _, err := fixture.business.Exec(`INSERT INTO accounts (id, status, schedulable, config_revision, dispatch_revision, authorization_instance_source_account_id, created_at, updated_at) VALUES ('acct-2', 'temporary_unavailable', 1, 5, 7, 'acct-1', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.business.Exec(`INSERT INTO account_health_jobs_input_versions (account_id, input_version, current_version, updated_at) VALUES ('acct-2', 1, 1, '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	fixture.insertOutcome(projectionFixtureNow.Add(-time.Minute), cooldownSuccessOutcome(observation, CooldownFence{ObservationStartedAt: observation, Generation: "gen-1"}))

	result := fixture.drain(t)
	if result.Processed != 1 {
		t.Fatalf("Processed = %d, 期望 1", result.Processed)
	}
	disposition, reason := fixture.receipt(t, "outcome-cooldown-success")
	if disposition != "applied" {
		t.Fatalf("receipt = %s/%s, 期望 applied", disposition, reason)
	}
	var rootRevision, instanceRevision int64
	if err := fixture.business.QueryRowContext(context.Background(), `SELECT dispatch_revision FROM accounts WHERE id = 'acct-1'`).Scan(&rootRevision); err != nil {
		t.Fatal(err)
	}
	if err := fixture.business.QueryRowContext(context.Background(), `SELECT dispatch_revision FROM accounts WHERE id = 'acct-2'`).Scan(&instanceRevision); err != nil {
		t.Fatal(err)
	}
	if rootRevision != 8 || instanceRevision != 8 {
		t.Fatalf("家族推进失败：root=%d instance=%d，期望 8/8", rootRevision, instanceRevision)
	}
	if count := fixture.outboxCount(t); count != 2 {
		t.Fatalf("circuit outbox 行数 = %d, 期望 2（根 + 实例）", count)
	}
	var childTransitionID string
	if err := fixture.business.QueryRowContext(context.Background(), `SELECT transition_id FROM account_circuit_outbox WHERE account_id = 'acct-2'`).Scan(&childTransitionID); err != nil {
		t.Fatal(err)
	}
	var rootTransitionID string
	if err := fixture.business.QueryRowContext(context.Background(), `SELECT transition_id FROM account_circuit_outbox WHERE account_id = 'acct-1'`).Scan(&rootTransitionID); err != nil {
		t.Fatal(err)
	}
	if childTransitionID != familyDispatchTransitionID(rootTransitionID, "acct-2") {
		t.Fatalf("子实例 transitionId 派生不符: %s", childTransitionID)
	}
}

// TestRunStopsOnContextCancel：Run 循环随 ctx 取消退出。
func TestRunStopsOnContextCancel(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fixture.projector.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, 期望 context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未随 ctx 退出")
	}
}
