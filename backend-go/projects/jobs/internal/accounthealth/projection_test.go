package accounthealth

import (
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

func splitDispositionExpectation(value string) []string {
	for index := 0; index < len(value); index++ {
		if value[index] == ':' {
			return []string{value[:index], value[index+1:]}
		}
	}
	return []string{value, ""}
}

