package accounthealth

import (
	"database/sql"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"strings"
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
  availability_schedule_gate integer,
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
CREATE TABLE group_accounts (
  system_account_id TEXT NOT NULL DEFAULT 'sys_admin',
  group_id TEXT NOT NULL,
  account_id TEXT NOT NULL,
  account_authorization_id TEXT,
  enabled INTEGER NOT NULL DEFAULT 1,
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (group_id, account_id)
);
CREATE TABLE group_account_stats_dirty (
  group_id TEXT PRIMARY KEY,
  reason TEXT,
  updated_at TEXT NOT NULL
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

func TestProjectionPostgresQueriesUseDollarPlaceholders(t *testing.T) {
	business := &ProjectionBusinessDB{}
	lockQuery := business.bind("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))")
	if lockQuery != "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))" {
		t.Fatalf("advisory lock query = %q", lockQuery)
	}
	if query := projectionCursorStorageQuery(); strings.Contains(query, "?") || !strings.Contains(query, "$1") {
		t.Fatalf("postgres cursor query must use a dollar placeholder: %q", query)
	}
}

func cooldownSuccessOutcome(observedAt time.Time, fence exactkeyprobe.CooldownFence) Outcome {
	return Outcome{
		OutcomeID:        "outcome-cooldown-success",
		RequestID:        "request-cooldown-success",
		AccountID:        "acct-1",
		Outcome:          exactkeyprobe.OutcomeSuccess,
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
				"last_health_check_at":          observedAt.Format(time.RFC3339Nano),
				"last_health_success_at":        observedAt.Format(time.RFC3339Nano),
				"last_health_check_status_code": 200,
			},
		},
	}
}

func activationSuccessOutcome(observedAt time.Time) Outcome {
	return Outcome{
		OutcomeID:        "outcome-activation-success",
		RequestID:        "request-activation-success",
		AccountID:        "acct-1",
		Outcome:          exactkeyprobe.OutcomeSuccess,
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

// TestShouldScheduleBalanceAutoDetectionFirstSuccess 钉住 T110 语义：写余额
// 探测意图的判据是「首次成功健康检查」。activation_success 保持归档原语义
// （不要求此前从未成功）；health_success / cooldown_success 只在投影前
// last_health_success_at 为 NULL/空（账户从未成功健康检查）时写一次。
func TestShouldScheduleBalanceAutoDetectionFirstSuccess(t *testing.T) {
	const secret = "t110-unit-credential-secret"
	envelope, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_key":"sk-t110-unit"}`))
	if err != nil {
		t.Fatalf("构造凭据 envelope: %v", err)
	}
	noKeyEnvelope, err := exactkeyprobe.EncryptV1Envelope(secret, []byte(`{"api_keys":["","   "],"api_key":"  "}`))
	if err != nil {
		t.Fatalf("构造无 Key 凭据 envelope: %v", err)
	}
	projector := &OutcomeProjector{secret: secret}
	observedAt := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	outcomeFor := func(transition string) Outcome {
		return Outcome{
			OutcomeID: "t110-unit-outcome", RequestID: "t110-unit-request", AccountID: "t110-unit-account",
			Outcome: exactkeyprobe.OutcomeSuccess, ObservedAt: observedAt,
			InputVersion: 1, ConfigRevision: 2, DispatchRevision: 1, StatusCode: 200,
			Projection: &Projection{
				TargetAccountID: "t110-unit-account", TransitionKind: transition, InputVersion: 1,
				ConfigRevision: 2, DispatchRevision: 1, ExpectedAccountStatus: "active",
			},
		}
	}
	neverSucceeded := sql.NullString{}
	succeeded := sql.NullString{String: "2026-11-03T11:00:00Z", Valid: true}
	blankSuccess := sql.NullString{String: "   ", Valid: true}

	cases := []struct {
		name        string
		transition  string
		lastSuccess sql.NullString
		accountType string
		enabled     int64
		configJSON  string
		credentials string
		planStatus  string
		sourceID    sql.NullString
		authID      sql.NullString
		want        bool
	}{
		{name: "activation_success + 从未成功", transition: "activation_success", lastSuccess: neverSucceeded, want: true},
		{name: "activation_success + 已有成功记录（原语义不要求为空）", transition: "activation_success", lastSuccess: succeeded, want: true},
		{name: "health_success + 从未成功", transition: "health_success", lastSuccess: neverSucceeded, want: true},
		{name: "health_success + 成功记录为空白文本", transition: "health_success", lastSuccess: blankSuccess, want: true},
		{name: "health_success + 已有成功记录", transition: "health_success", lastSuccess: succeeded, want: false},
		{name: "cooldown_success + 从未成功", transition: "cooldown_success", lastSuccess: neverSucceeded, want: true},
		{name: "cooldown_success + 已有成功记录", transition: "cooldown_success", lastSuccess: succeeded, want: false},
		{name: "health_failure + 从未成功", transition: "health_failure", lastSuccess: neverSucceeded, want: false},
		{name: "health_success + 从未成功但余额查询已开启", transition: "health_success", lastSuccess: neverSucceeded, enabled: 1, want: false},
		{name: "health_success + 从未成功但配置非空", transition: "health_success", lastSuccess: neverSucceeded, configJSON: `{"adapter":"builtin"}`, want: false},
		{name: "health_success + 从未成功但类型非 api_key", transition: "health_success", lastSuccess: neverSucceeded, accountType: "oauth", want: false},
		{name: "health_success + 从未成功但凭据无有效 Key", transition: "health_success", lastSuccess: neverSucceeded, credentials: noKeyEnvelope, want: false},
		{name: "health_success + 从未成功但计划未判 active", transition: "health_success", lastSuccess: neverSucceeded, planStatus: "temporary_unavailable", want: false},
		// B1：授权实例的 credentials_encrypted 生产为占位 "{}"，必须在解密前跳过，
		// 否则 health_success 首次成功路径会抛 envelope 格式错误并毒化 drain。
		{
			name: "health_success + 从未成功但为授权实例", transition: "health_success", lastSuccess: neverSucceeded,
			credentials: "{}", sourceID: sql.NullString{String: "t110-source", Valid: true}, want: false,
		},
		// 历史/人工数据可能只写 authorization_instance_authorization_id；守卫
		// 同时检查任一实例标记，避免仅 auth ID 有值时漏过解密。
		{
			name: "health_success + 从未成功但仅有授权 ID", transition: "health_success", lastSuccess: neverSucceeded,
			credentials: "{}", authID: sql.NullString{String: "t110-auth", Valid: true}, want: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			accountType := testCase.accountType
			if accountType == "" {
				accountType = "api_key"
			}
			configJSON := testCase.configJSON
			if configJSON == "" {
				configJSON = "{}"
			}
			credentials := testCase.credentials
			if credentials == "" {
				credentials = envelope
			}
			planStatus := testCase.planStatus
			if planStatus == "" {
				planStatus = "active"
			}
			account := &projectionAccountFence{
				accountType:                   accountType,
				credentialsEncrypted:          credentials,
				balanceQueryEnabled:           sql.NullInt64{Int64: testCase.enabled, Valid: true},
				balanceQueryConfigJSON:        configJSON,
				lastHealthSuccessAt:           testCase.lastSuccess,
				authorizationInstanceSourceID: testCase.sourceID,
				authorizationInstanceAuthID:   testCase.authID,
			}
			got, err := projector.shouldScheduleBalanceAutoDetection(account, outcomeFor(testCase.transition), activationProjectionPlan{status: planStatus})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("shouldScheduleBalanceAutoDetection=%t want %t", got, testCase.want)
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

// TestActivationPlanForProjectionGateSemantics 钉住《时间计划闸门与账户状态
// 解耦设计》§4.3：activation_success / cooldown_success 的 status 恒为 active
// （健康事实恢复），时间计划只写独立闸门；计划解析失败 fail-closed 关闸，但
// status 仍恢复为 active。
func TestActivationPlanForProjectionGateSemantics(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC) // Monday
	projector := &OutcomeProjector{now: func() time.Time { return now }}
	const insideSchedule = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"09:00","end":"18:00"}]}`
	const outsideSchedule = `{"enabled":true,"timezone":"UTC","mode":"allow_windows","windows":[{"daysOfWeek":[1,2,3,4,5,6,7],"start":"12:00","end":"18:00"}]}`
	fence := exactkeyprobe.CooldownFence{}
	gate := func(value int) *int { return &value }

	cases := []struct {
		name         string
		schedule     sql.NullString
		outcome      Outcome
		wantStatus   string
		wantGate     *int
		wantNext     string
		wantRestores bool
	}{
		{
			name:         "activation_success 无计划（闸门 NULL 恒开）",
			schedule:     sql.NullString{},
			outcome:      activationSuccessOutcome(now),
			wantStatus:   "active",
			wantGate:     nil,
			wantRestores: true,
		},
		{
			name:         "activation_success 时段内（闸门 1）",
			schedule:     sql.NullString{String: insideSchedule, Valid: true},
			outcome:      activationSuccessOutcome(now),
			wantStatus:   "active",
			wantGate:     gate(1),
			wantNext:     "2026-09-07T18:00:00.000Z",
			wantRestores: true,
		},
		{
			name:         "cooldown_success 时段外（status 仍 active、闸门 0）",
			schedule:     sql.NullString{String: outsideSchedule, Valid: true},
			outcome:      cooldownSuccessOutcome(now, fence),
			wantStatus:   "active",
			wantGate:     gate(0),
			wantNext:     "2026-09-07T12:00:00.000Z",
			wantRestores: false,
		},
		{
			name:         "activation_success 计划解析失败（fail-closed 关闸）",
			schedule:     sql.NullString{String: "{bad", Valid: true},
			outcome:      activationSuccessOutcome(now),
			wantStatus:   "active",
			wantGate:     gate(0),
			wantRestores: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			account := &projectionAccountFence{availabilityScheduleJSON: testCase.schedule}
			plan, err := projector.activationPlanForProjection(account, testCase.outcome)
			if err != nil {
				t.Fatal(err)
			}
			if plan.status != testCase.wantStatus {
				t.Fatalf("status=%q want %q（计划不得改写 status）", plan.status, testCase.wantStatus)
			}
			if testCase.wantGate == nil {
				if plan.scheduleGate != nil {
					t.Fatalf("gate=%d want NULL（无计划）", *plan.scheduleGate)
				}
			} else if plan.scheduleGate == nil || *plan.scheduleGate != *testCase.wantGate {
				t.Fatalf("gate=%+v want %d", plan.scheduleGate, *testCase.wantGate)
			}
			if plan.nextCheckAt != testCase.wantNext {
				t.Fatalf("nextCheckAt=%q want %q", plan.nextCheckAt, testCase.wantNext)
			}
			if plan.restoresDispatch != testCase.wantRestores {
				t.Fatalf("restoresDispatch=%t want %t", plan.restoresDispatch, testCase.wantRestores)
			}
		})
	}
}
