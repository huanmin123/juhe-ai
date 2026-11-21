package statreads

// 真实 PG 契约：AI 健康列表以 account_health_hourly 为主条带，J1 outcome
// 只补齐缺失小时/最新空档，并为表头单独取每账户最新质量终态；详情只读目标
// 小时。夹具在同一隔离库内建 juhe_jobs schema，与生产读面同形。

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const j1TestSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_jobs;
CREATE TABLE juhe_jobs.account_health_outcomes (
	outcome_id text PRIMARY KEY,
	request_id text NOT NULL UNIQUE,
	account_id text NOT NULL,
	outcome text NOT NULL,
	observed_at timestamptz NOT NULL,
	status_code integer,
	error_code text,
	error_message text,
	next_due_at timestamptz
);
CREATE INDEX idx_account_health_outcomes_read_cover
	ON juhe_jobs.account_health_outcomes (account_id, observed_at)
	INCLUDE (outcome_id, request_id, outcome, status_code, error_code, error_message, next_due_at);
`

func newJ1Fixture(t *testing.T) *testFixture {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, pgTestSchema); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, j1TestSchema)
		return err
	})
	if _, err := testDB.DB.Exec(`INSERT INTO juhe_business.system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed timezone: %v", err)
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	fixture := &testFixture{db: testDB.DB, now: now}
	fixture.deps = &Deps{
		Business:       testDB.DB,
		Stats:          testDB.DB,
		Now:            func() time.Time { return fixture.now },
		Timezone:       NewSystemSettingsTimezoneSource(testDB.DB),
		HealthOutcomes: &HealthOutcomeSource{PostgresURL: testDB.DSN},
	}
	return fixture
}

func TestAiHealthListUsesHourlyPrimaryAndJ1Gap(t *testing.T) {
	fixture := newJ1Fixture(t)
	seed := []string{
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, vendor_code, type, status, last_used_at, last_health_check_at) VALUES
			('acct-a', '账户A', 'sys-user-1', 'openai', 'api_key', 'active', '2026-09-04T11:00:00.000Z', NULL),
			('acct-b', '账户B', 'sys-user-1', 'openai', 'api_key', 'active', '2026-09-04T10:00:00.000Z', NULL),
			('acct-c', '账户C', 'sys-user-1', 'openai', 'api_key', 'active', '2026-09-04T09:00:00.000Z', '2026-09-04T09:30:00.000Z'),
			('acct-e', '账户E', 'sys-user-1', 'openai', 'api_key', 'active', '2026-09-04T08:00:00.000Z', NULL)`,
		`INSERT INTO juhe_stats.account_health_hourly (account_id, system_account_id, provider_code, stat_hour, status, last_observed_at, last_record_id, updated_at) VALUES
			('acct-a', 'sys-user-1', 'openai', '2026-09-04T10', 'success', '2026-09-04T10:30:00.000Z', 'hour-a-10', '2026-09-04T10:31:00.000Z'),
			('acct-a', 'sys-user-1', 'openai', '2026-09-04T11', 'success', '2026-09-04T11:30:00.000Z', 'hour-a-11', '2026-09-04T11:31:00.000Z'),
			('acct-c', 'sys-user-1', 'openai', '2026-09-04T10', 'failure', '2026-09-04T10:10:00.000Z', 'hour-c-10', '2026-09-04T10:11:00.000Z'),
			('acct-c', 'sys-user-1', 'openai', '2026-09-04T11', 'success', '2026-09-04T11:10:00.000Z', 'hour-c-11', '2026-09-04T11:11:00.000Z'),
			('acct-c', 'sys-user-1', 'openai', '2026-09-04T12', 'success', '2026-09-04T12:05:00.000Z', 'hour-c-12', '2026-09-04T12:06:00.000Z'),
			('acct-e', 'sys-user-1', 'openai', '2026-09-04T10', 'success', '2026-09-04T10:10:00.000Z', 'hour-e-10', '2026-09-04T10:11:00.000Z'),
			('acct-e', 'sys-user-1', 'openai', '2026-09-04T11', 'success', '2026-09-04T11:10:00.000Z', 'hour-e-11', '2026-09-04T11:11:00.000Z'),
			('acct-e', 'sys-user-1', 'openai', '2026-09-04T12', 'success', '2026-09-04T12:05:00.000Z', 'hour-e-12', '2026-09-04T12:06:00.000Z')`,
		`INSERT INTO juhe_jobs.account_health_outcomes (outcome_id, request_id, account_id, outcome, observed_at, status_code, error_code, error_message, next_due_at) VALUES
			('j1-a-1140', 'req-a-1140', 'acct-a', 'upstream_http_failure', '2026-09-04T11:40:00Z', 502, 'upstream_http_failure', 'bad gateway', '2026-09-04T12:40:00Z'),
			('j1-a-1150', 'req-a-1150', 'acct-a', 'upstream_failure', '2026-09-04T11:50:00Z', 0, 'upstream_error', 'transport interrupted', '2026-09-04T12:50:00Z'),
			('j1-a-1210', 'req-a-1210', 'acct-a', 'complete_success', '2026-09-04T12:10:00Z', 200, NULL, NULL, '2026-09-04T13:10:00Z'),
			('j1-b-1020', 'req-b-1020', 'acct-b', 'complete_success', '2026-09-04T10:20:00Z', 200, NULL, NULL, '2026-09-04T11:20:00Z'),
			('j1-e-0930', 'req-e-0930', 'acct-e', 'complete_success', '2026-09-04T09:30:00Z', 200, NULL, NULL, '2026-09-04T10:30:00Z'),
			('j1-e-1050', 'req-e-1050', 'acct-e', 'upstream_http_failure', '2026-09-04T10:50:00Z', 502, 'upstream_http_failure', 'bad gateway', '2026-09-04T11:50:00Z')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	recorder := invoke(t, fixture.deps.aiHealthListHandler(false), http.MethodGet,
		"/__aisys__/api/stats/ai-health?hours=3&page=1&pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ai-health not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	items := payload["items"].([]any)
	byID := map[string]map[string]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		byID[item["id"].(string)] = item
	}
	if len(byID) != 4 {
		t.Fatalf("account count wrong: %#v", items)
	}
	assertHealthAccount(t, byID["acct-a"], map[string]any{
		"latestStatus":      "success",
		"successHours":      float64(2),
		"failureHours":      float64(1),
		"unknownHours":      float64(0),
		"healthRate":        float64(66.67),
		"lastHealthCheckAt": "2026-09-04T12:10:00.000000Z",
		"nextHealthCheckAt": "2026-09-04T13:10:00.000000Z",
	})
	assertHealthAccount(t, byID["acct-b"], map[string]any{
		"latestStatus":      "success",
		"successHours":      float64(1),
		"failureHours":      float64(0),
		"unknownHours":      float64(2),
		"healthRate":        float64(100),
		"lastHealthCheckAt": "2026-09-04T10:20:00.000000Z",
		"nextHealthCheckAt": "2026-09-04T11:20:00.000000Z",
	})
	assertHealthAccount(t, byID["acct-c"], map[string]any{
		"latestStatus":      "success",
		"successHours":      float64(2),
		"failureHours":      float64(1),
		"unknownHours":      float64(0),
		"healthRate":        float64(66.67),
		"lastHealthCheckAt": "2026-09-04T09:30:00.000Z",
	})
	assertHealthAccount(t, byID["acct-e"], map[string]any{
		"latestStatus":        "success",
		"successHours":        float64(3),
		"failureHours":        float64(0),
		"unknownHours":        float64(0),
		"healthRate":          float64(100),
		"lastHealthCheckAt":   "2026-09-04T09:30:00.000000Z",
		"lastHealthSuccessAt": "2026-09-04T09:30:00.000000Z",
		"nextHealthCheckAt":   "2026-09-04T10:30:00.000000Z",
	})
}

func TestAiHealthListLatestQualityProbeSpansCoveredHours(t *testing.T) {
	fixture := newJ1Fixture(t)
	seed := []string{
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, vendor_code, type, status, last_used_at) VALUES
			('acct-f', '账户F', 'sys-user-1', 'openai', 'api_key', 'active', '2026-09-04T11:00:00.000Z')`,
		`INSERT INTO juhe_stats.account_health_hourly (account_id, system_account_id, provider_code, stat_hour, status, last_observed_at, last_record_id, updated_at) VALUES
			('acct-f', 'sys-user-1', 'openai', '2026-09-04T08', 'success', '2026-09-04T08:10:00.000Z', 'hour-f-08', '2026-09-04T08:11:00.000Z'),
			('acct-f', 'sys-user-1', 'openai', '2026-09-04T09', 'failure', '2026-09-04T09:10:00.000Z', 'hour-f-09', '2026-09-04T09:11:00.000Z'),
			('acct-f', 'sys-user-1', 'openai', '2026-09-04T10', 'success', '2026-09-04T10:10:00.000Z', 'hour-f-10', '2026-09-04T10:11:00.000Z'),
			('acct-f', 'sys-user-1', 'openai', '2026-09-04T11', 'success', '2026-09-04T11:10:00.000Z', 'hour-f-11', '2026-09-04T11:11:00.000Z'),
			('acct-f', 'sys-user-1', 'openai', '2026-09-04T12', 'failure', '2026-09-04T12:05:00.000Z', 'hour-f-12', '2026-09-04T12:06:00.000Z')`,
		`INSERT INTO juhe_jobs.account_health_outcomes (outcome_id, request_id, account_id, outcome, observed_at, status_code, error_code, error_message, next_due_at) VALUES
			('j1-f-0720', 'req-f-0720', 'acct-f', 'complete_success', '2026-09-04T07:20:00Z', 200, NULL, NULL, '2026-09-04T08:20:00Z'),
			('j1-f-0930', 'req-f-0930', 'acct-f', 'complete_success', '2026-09-04T09:30:00Z', 200, NULL, NULL, '2026-09-04T10:30:00Z'),
			('j1-f-1140', 'req-f-1140', 'acct-f', 'upstream_http_failure', '2026-09-04T11:40:00Z', 502, 'upstream_http_failure', 'bad gateway', '2026-09-04T12:40:00Z'),
			('j1-f-1205', 'req-f-1205', 'acct-f', 'upstream_http_failure', '2026-09-04T12:05:00Z', 502, 'upstream_http_failure', 'bad gateway', '2026-09-04T13:05:00Z')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	recorder := invoke(t, fixture.deps.aiHealthListHandler(false), http.MethodGet,
		"/__aisys__/api/stats/ai-health?hours=6&page=1&pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ai-health not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	items := dataMap(t, decodeBody(t, recorder))["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("account count wrong: %#v", items)
	}
	account := items[0].(map[string]any)
	// hour 07 is the gap row; hour 09 is a covered hourly failure that the
	// newer 09:30 probe success must override; hour 12 is the hourly failure.
	// The newest quality outcome sits in a covered hour outside the 2h
	// freshness range, so the header and strip both depend on the probe rather
	// than the earlier 07:20 gap row.
	assertHealthAccount(t, account, map[string]any{
		"latestStatus":        "failure",
		"successHours":        float64(5),
		"failureHours":        float64(1),
		"unknownHours":        float64(0),
		"healthRate":          float64(83.33),
		"lastHealthCheckAt":   "2026-09-04T09:30:00.000000Z",
		"lastHealthSuccessAt": "2026-09-04T09:30:00.000000Z",
		"nextHealthCheckAt":   "2026-09-04T10:30:00.000000Z",
	})
}

func TestAiHealthHourDetailReadsOnlyTargetHour(t *testing.T) {
	fixture := newJ1Fixture(t)
	seed := []string{
		`INSERT INTO juhe_business.accounts (id, name, system_account_id, vendor_code, type, status, last_used_at) VALUES
			('acct-d', '账户D', 'sys-user-1', 'openai', 'api_key', 'active', '2026-09-04T11:00:00.000Z')`,
		`INSERT INTO juhe_stats.account_health_hourly (account_id, system_account_id, provider_code, stat_hour, status, last_observed_at, last_record_id, updated_at) VALUES
			('acct-d', 'sys-user-1', 'openai', '2026-09-04T10', 'failure', '2026-09-04T10:05:00.000Z', 'hour-d-10', '2026-09-04T10:06:00.000Z')`,
		`INSERT INTO juhe_jobs.account_health_outcomes (outcome_id, request_id, account_id, outcome, observed_at, status_code, error_code, error_message, next_due_at) VALUES
			('j1-d-0940', 'req-d-0940', 'acct-d', 'complete_success', '2026-09-04T09:40:00Z', 200, NULL, NULL, '2026-09-04T10:40:00Z'),
			('j1-d-1040', 'req-d-1040', 'acct-d', 'upstream_failure', '2026-09-04T10:40:00Z', 0, 'upstream_error', 'transport interrupted', '2026-09-04T11:40:00Z'),
			('j1-d-1050', 'req-d-1050', 'acct-d', 'upstream_http_failure', '2026-09-04T10:50:00Z', 502, 'upstream_http_failure', 'bad gateway', '2026-09-04T11:50:00Z'),
			('j1-d-1110', 'req-d-1110', 'acct-d', 'complete_success', '2026-09-04T11:10:00Z', 200, NULL, NULL, '2026-09-04T12:10:00Z')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	handler := fixture.deps.aiHealthHourDetailHandler(false)
	for _, test := range []struct {
		statHour       string
		wantStatus     string
		wantObservedAt string
	}{
		{"2026-09-04T09", "success", "2026-09-04T09:40:00.000000Z"},
		{"2026-09-04T10", "failure", "2026-09-04T10:40:00.000000Z"},
		{"2026-09-04T11", "success", "2026-09-04T11:10:00.000000Z"},
	} {
		target := "/__aisys__/api/stats/ai-health/hour-detail?accountId=acct-d&statHour=" + test.statHour
		recorder := invoke(t, handler, http.MethodGet, target, adminAuth(""))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s detail not 200: %d %s", test.statHour, recorder.Code, recorder.Body.String())
		}
		detail := dataMap(t, decodeBody(t, recorder))
		if detail["status"] != test.wantStatus || detail["lastObservedAt"] != test.wantObservedAt {
			t.Fatalf("%s detail wrong: %#v", test.statHour, detail)
		}
	}
}

func assertHealthAccount(t *testing.T, account map[string]any, want map[string]any) {
	t.Helper()
	if account == nil {
		t.Fatalf("missing account row")
	}
	for key, wantValue := range want {
		if account[key] != wantValue {
			t.Fatalf("account %v field %s = %#v, want %#v", account["id"], key, account[key], wantValue)
		}
	}
}
