package statreads

// 契约测试：SQLite 内存库上直接驱动 handler（注入 authsys.AuthContext），
// 覆盖权限门（my-stats 403 / usage-records 未过滤 400）、日期参数 400、
// usage-overview 各段读数、account-usage 分页聚合、ai-performance 序列与
// usage-window。表结构仅保留被测 SQL 需要的列。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	_ "modernc.org/sqlite"
)

const testSchema = `
	CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key));
	CREATE TABLE usage_stats_daily (
		system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL, stat_date TEXT NOT NULL,
		request_count REAL NOT NULL DEFAULT 0, success_count REAL NOT NULL DEFAULT 0, error_count REAL NOT NULL DEFAULT 0,
		input_tokens REAL NOT NULL DEFAULT 0, output_tokens REAL NOT NULL DEFAULT 0, cache_read_tokens REAL NOT NULL DEFAULT 0,
		cache_read_cost_usd REAL NOT NULL DEFAULT 0, cache_write_tokens REAL NOT NULL DEFAULT 0,
		cache_write_1h_tokens REAL NOT NULL DEFAULT 0, cache_write_cost_usd REAL NOT NULL DEFAULT 0,
		thinking_tokens REAL NOT NULL DEFAULT 0, input_image_tokens REAL NOT NULL DEFAULT 0,
		output_image_tokens REAL NOT NULL DEFAULT 0, total_cost_usd REAL NOT NULL DEFAULT 0,
		duration_ms_sum REAL NOT NULL DEFAULT 0, duration_ms_count REAL NOT NULL DEFAULT 0,
		first_token_ms_sum REAL NOT NULL DEFAULT 0, first_token_ms_count REAL NOT NULL DEFAULT 0,
		last_used_at TEXT, PRIMARY KEY (system_account_id, scope_type, scope_id, stat_date));
	CREATE TABLE usage_overview_trend_windows (
		system_account_id TEXT NOT NULL, window_key TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL,
		bucket_key TEXT NOT NULL, request_count REAL NOT NULL DEFAULT 0, error_count REAL NOT NULL DEFAULT 0,
		duration_ms_sum REAL NOT NULL DEFAULT 0, duration_ms_count REAL NOT NULL DEFAULT 0,
		PRIMARY KEY (system_account_id, window_key, start_date, end_date, bucket_key));
	CREATE TABLE usage_overview_summary_windows (
		system_account_id TEXT NOT NULL, window_key TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL,
		request_count REAL NOT NULL DEFAULT 0, success_count REAL NOT NULL DEFAULT 0, error_count REAL NOT NULL DEFAULT 0,
		input_tokens REAL NOT NULL DEFAULT 0, output_tokens REAL NOT NULL DEFAULT 0, cache_read_tokens REAL NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0, duration_ms_sum REAL NOT NULL DEFAULT 0, duration_ms_count REAL NOT NULL DEFAULT 0,
		first_token_ms_sum REAL NOT NULL DEFAULT 0, first_token_ms_count REAL NOT NULL DEFAULT 0,
		PRIMARY KEY (system_account_id, window_key, start_date, end_date));
	CREATE TABLE usage_model_rank_windows (
		system_account_id TEXT NOT NULL, window_key TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL,
		rank INTEGER NOT NULL, provider_code TEXT NOT NULL, model TEXT NOT NULL, request_count REAL NOT NULL DEFAULT 0,
		input_tokens REAL NOT NULL DEFAULT 0, output_tokens REAL NOT NULL DEFAULT 0, total_cost_usd REAL NOT NULL DEFAULT 0,
		PRIMARY KEY (system_account_id, window_key, start_date, end_date, rank));
	CREATE TABLE usage_error_rank_windows (
		system_account_id TEXT NOT NULL, window_key TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL,
		rank INTEGER NOT NULL, provider_code TEXT NOT NULL, error_code TEXT, status_code INTEGER, error_message TEXT,
		error_count REAL NOT NULL DEFAULT 0, PRIMARY KEY (system_account_id, window_key, start_date, end_date, rank));
	CREATE TABLE usage_stats_hourly (
		system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL, stat_hour TEXT NOT NULL,
		request_count REAL NOT NULL DEFAULT 0, duration_ms_sum REAL NOT NULL DEFAULT 0, duration_ms_count REAL NOT NULL DEFAULT 0,
		duration_ms_max REAL, first_token_ms_sum REAL NOT NULL DEFAULT 0, first_token_ms_count REAL NOT NULL DEFAULT 0,
		first_token_ms_max REAL, PRIMARY KEY (system_account_id, scope_type, scope_id, stat_hour));
	CREATE TABLE usage_rank_snapshots (
		system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, window_key TEXT NOT NULL, metric TEXT NOT NULL,
		snapshot_at TEXT NOT NULL, rank INTEGER NOT NULL, scope_id TEXT NOT NULL, metric_value REAL NOT NULL DEFAULT 0,
		PRIMARY KEY (system_account_id, scope_type, window_key, metric, snapshot_at, rank));
	CREATE TABLE ai_performance_summary_windows (
		system_account_id TEXT NOT NULL, window_key TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL,
		request_count REAL NOT NULL DEFAULT 0, first_token_ms_sum REAL NOT NULL DEFAULT 0,
		first_token_ms_count REAL NOT NULL DEFAULT 0, first_token_ms_max REAL, duration_ms_sum REAL NOT NULL DEFAULT 0,
		duration_ms_count REAL NOT NULL DEFAULT 0, duration_ms_max REAL,
		PRIMARY KEY (system_account_id, window_key, start_date, end_date));
	CREATE TABLE account_health_hourly (
		account_id TEXT NOT NULL, stat_hour TEXT NOT NULL, status TEXT NOT NULL, last_observed_at TEXT,
		status_code INTEGER, error_code TEXT, error_message TEXT, PRIMARY KEY (account_id, stat_hour));
	CREATE TABLE usage_scope_range_windows (
		system_account_id TEXT NOT NULL, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL,
		start_date TEXT NOT NULL, end_date TEXT NOT NULL, request_count REAL NOT NULL DEFAULT 0,
		input_tokens REAL NOT NULL DEFAULT 0, output_tokens REAL NOT NULL DEFAULT 0,
		cache_read_tokens REAL NOT NULL DEFAULT 0, cache_read_cost_usd REAL NOT NULL DEFAULT 0,
		cache_write_tokens REAL NOT NULL DEFAULT 0, cache_write_1h_tokens REAL NOT NULL DEFAULT 0,
		cache_write_cost_usd REAL NOT NULL DEFAULT 0, thinking_tokens REAL NOT NULL DEFAULT 0,
		input_image_tokens REAL NOT NULL DEFAULT 0, output_image_tokens REAL NOT NULL DEFAULT 0,
		total_cost_usd REAL NOT NULL DEFAULT 0, last_used_at TEXT,
		PRIMARY KEY (system_account_id, scope_type, scope_id, start_date, end_date));
	CREATE TABLE system_metrics_trend_windows (
		window_key TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL, bucket_key TEXT NOT NULL,
		sample_count REAL NOT NULL DEFAULT 0, cpu_percent_sum REAL NOT NULL DEFAULT 0, memory_used_percent_sum REAL NOT NULL DEFAULT 0,
		network_rx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0, network_rx_bytes_per_sec_count REAL NOT NULL DEFAULT 0,
		network_tx_bytes_per_sec_sum REAL NOT NULL DEFAULT 0, network_tx_bytes_per_sec_count REAL NOT NULL DEFAULT 0,
		PRIMARY KEY (window_key, start_date, end_date, bucket_key));
	CREATE TABLE process_event_loop_samples (
		id TEXT PRIMARY KEY, process_role TEXT NOT NULL, process_pid INTEGER, sampled_at TEXT NOT NULL,
		event_loop_lag_ms REAL, process_rss_bytes REAL, process_heap_used_bytes REAL, process_heap_total_bytes REAL);
	CREATE TABLE process_event_loop_trend_windows (
		window_key TEXT NOT NULL, start_date TEXT NOT NULL, end_date TEXT NOT NULL, bucket_key TEXT NOT NULL,
		process_role TEXT NOT NULL, sample_count REAL NOT NULL DEFAULT 0, event_loop_lag_ms_sum REAL NOT NULL DEFAULT 0,
		event_loop_lag_ms_count REAL NOT NULL DEFAULT 0, event_loop_lag_ms_max REAL,
		process_rss_bytes_sum REAL NOT NULL DEFAULT 0, process_rss_bytes_max REAL,
		process_heap_used_bytes_sum REAL NOT NULL DEFAULT 0, process_heap_used_bytes_max REAL,
		process_heap_total_bytes_sum REAL NOT NULL DEFAULT 0, process_heap_total_bytes_max REAL,
		PRIMARY KEY (window_key, start_date, end_date, bucket_key, process_role));
	CREATE TABLE resource_authorizations (
		id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
		owner_system_account_id TEXT NOT NULL, grantee_system_account_id TEXT NOT NULL,
		status TEXT NOT NULL, expires_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
	CREATE TABLE accounts (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL,
		type TEXT NOT NULL, status TEXT NOT NULL, deleted_at TEXT, proxy_profile_id TEXT,
		authorization_instance_source_account_id TEXT, authorization_instance_authorization_id TEXT,
		authorization_instance_owner_system_account_id TEXT, last_used_at TEXT,
		last_health_check_at TEXT, last_health_success_at TEXT, next_health_check_at TEXT);
	CREATE TABLE system_accounts (id TEXT PRIMARY KEY, username TEXT NOT NULL, display_name TEXT);
	CREATE TABLE api_keys (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL);
	CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL);
`

type testFixture struct {
	deps *Deps
	db   *sql.DB
	now  time.Time
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO system_settings (system_account_id, key, value_json, updated_at) VALUES ('sys_admin', 'usageStatsTimezone', '"UTC"', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("seed timezone: %v", err)
	}
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	fixture := &testFixture{db: db, now: now}
	fixture.deps = &Deps{
		Business:  db,
		Stats:     db,
		PGDialect: false,
		Now:       func() time.Time { return fixture.now },
		Timezone:  NewSystemSettingsTimezoneSource(db, false),
	}
	return fixture
}

// invoke runs a wrapped handler with an injected auth context (the authsys
// session wrappers are exercised by their own package tests).
func invoke(t *testing.T, handler http.HandlerFunc, method, target string, auth *authsys.AuthContext) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	if auth != nil {
		request = request.WithContext(authsys.WithAuthContext(request.Context(), auth))
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func adminAuth(role string) *authsys.AuthContext {
	if role == "" {
		role = "admin"
	}
	return &authsys.AuthContext{SystemAccountID: "sys-admin-1", Username: "admin", DisplayName: "Admin", Role: role, SessionID: "s1"}
}

func userAuth() *authsys.AuthContext {
	return &authsys.AuthContext{SystemAccountID: "sys-user-1", Username: "user", DisplayName: "User", Role: "user", SessionID: "s2"}
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func dataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("payload has no data object: %#v", payload)
	}
	return data
}

func TestUsageOverviewSummaryReadsTodayDailyBucket(t *testing.T) {
	fixture := newFixture(t)
	_, err := fixture.db.Exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
		request_count, success_count, error_count, input_tokens, output_tokens, total_cost_usd,
		duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count)
		VALUES ('global', 'system_account', 'global', '2026-09-04', 10, 8, 2, 100, 50, 1.25, 4000, 10, 900, 9)`)
	if err != nil {
		t.Fatalf("seed daily: %v", err)
	}
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/summary?startDate=2026-09-04&endDate=2026-09-04", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("summary not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	data := dataMap(t, decodeBody(t, recorder))
	summary := data["summary"].(map[string]any)
	if summary["requestCount"] != float64(10) || summary["errorCount"] != float64(2) {
		t.Fatalf("summary counters wrong: %#v", summary)
	}
	if summary["errorRate"] != 0.2 || summary["totalTokens"] != float64(150) {
		t.Fatalf("summary ratios wrong: %#v", summary)
	}
	if summary["averageDurationMs"] != float64(400) || summary["averageFirstTokenMs"] != float64(100) {
		t.Fatalf("summary averages wrong: %#v", summary)
	}
	rangeData, ok := data["range"].(map[string]any)
	if !ok || rangeData["maxDays"] != float64(31) {
		t.Fatalf("summary range wrong: %#v", data["range"])
	}
}

func TestUsageOverviewSummaryHistoricalReadsSummaryWindow(t *testing.T) {
	fixture := newFixture(t)
	yesterday := "2026-09-01"
	_, err := fixture.db.Exec(`INSERT INTO usage_overview_summary_windows (system_account_id, window_key, start_date, end_date,
		request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd,
		duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count)
		VALUES ('global', '2026-09-01:2026-09-01', '2026-09-01', '2026-09-01', 4, 4, 0, 10, 5, 1, 0.5, 80, 4, 20, 2)`)
	if err != nil {
		t.Fatalf("seed window: %v", err)
	}
	handler := fixture.deps.overviewSectionHandler(false)
	target := "/__aisys__/api/stats/usage-overview/summary?startDate=" + yesterday + "&endDate=" + yesterday
	recorder := invoke(t, handler, http.MethodGet, target, adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("historical summary not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	summary := dataMap(t, decodeBody(t, recorder))["summary"].(map[string]any)
	if summary["requestCount"] != float64(4) || summary["averageDurationMs"] != float64(20) {
		t.Fatalf("historical summary wrong: %#v", summary)
	}
}

func TestUsageOverviewDateContract(t *testing.T) {
	fixture := newFixture(t)
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/summary?startDate=20260901", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid date not 400: %d", recorder.Code)
	}
	if got := decodeBody(t, recorder)["message"]; got != "开始日期格式应为 YYYY-MM-DD" {
		t.Fatalf("invalid date message wrong: %#v", got)
	}
	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/unknown", adminAuth(""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown section not 404: %d", recorder.Code)
	}
}

func TestUsageOverviewDailyTrendFillsRange(t *testing.T) {
	fixture := newFixture(t)
	_, err := fixture.db.Exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
		input_tokens, output_tokens, total_cost_usd) VALUES ('global', 'system_account', 'global', '2026-09-02', 7, 3, 2.5)`)
	if err != nil {
		t.Fatalf("seed daily: %v", err)
	}
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/daily-trend?startDate=2026-08-30&endDate=2026-09-03", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("daily trend not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	trend, ok := dataMap(t, decodeBody(t, recorder))["dailyTrend"].([]any)
	if !ok || len(trend) != 5 {
		t.Fatalf("daily trend length wrong: %#v", dataMap(t, decodeBody(t, recorder))["dailyTrend"])
	}
	first := trend[0].(map[string]any)
	seeded := trend[3].(map[string]any)
	if seeded["statDate"] != "2026-09-02" || first["totalTokens"] != float64(0) || seeded["totalTokens"] != float64(10) || seeded["totalCost"] != 2.5 {
		t.Fatalf("daily trend values wrong: %#v / %#v", first, seeded)
	}
}

func TestUsageOverviewHourlyAndErrorsWindows(t *testing.T) {
	fixture := newFixture(t)
	seed := []string{
		`INSERT INTO usage_overview_trend_windows (system_account_id, window_key, start_date, end_date, bucket_key, request_count, error_count, duration_ms_sum, duration_ms_count)
			VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', '2026-09-04T00', 6, 1, 3000, 6)`,
		`INSERT INTO usage_error_rank_windows (system_account_id, window_key, start_date, end_date, rank, provider_code, error_code, status_code, error_message, error_count)
			VALUES ('global', '2026-09-04:2026-09-04', '2026-09-04', '2026-09-04', 1, 'openai', 'rate_limit_exceeded', 429, 'slow down', 3)`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	handler := fixture.deps.overviewSectionHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/hourly-trend?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("hourly trend not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	hourly := dataMap(t, decodeBody(t, recorder))["hourlyTrend"].([]any)
	if len(hourly) != 1 {
		t.Fatalf("hourly trend rows wrong: %#v", hourly)
	}
	point := hourly[0].(map[string]any)
	if point["statHour"] != "2026-09-04T00" || point["requestCount"] != float64(6) || point["averageDurationMs"] != float64(500) {
		t.Fatalf("hourly point wrong: %#v", point)
	}
	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/usage-overview/errors?startDate=2026-09-04&endDate=2026-09-04", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("errors not 200: %d", recorder.Code)
	}
	errorsList := dataMap(t, decodeBody(t, recorder))["errors"].([]any)
	if len(errorsList) != 1 {
		t.Fatalf("errors rows wrong: %#v", errorsList)
	}
	errorPoint := errorsList[0].(map[string]any)
	if errorPoint["errorCode"] != "rate_limit_exceeded" || errorPoint["statusCode"] != float64(429) || errorPoint["errorCount"] != float64(3) {
		t.Fatalf("error point wrong: %#v", errorPoint)
	}
}

func TestUsageWindowContract(t *testing.T) {
	fixture := newFixture(t)
	recorder := invoke(t, fixture.deps.usageWindowHandler, http.MethodGet, "/__aisys__/api/stats/usage-window", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("usage-window not 200: %d", recorder.Code)
	}
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["timezone"] != "UTC" || payload["endDate"] != "2026-09-04" || payload["days"] != float64(31) || payload["maxDays"] != float64(31) {
		t.Fatalf("usage-window payload wrong: %#v", payload)
	}
	if payload["startDate"] != "2026-08-05" {
		t.Fatalf("usage-window start wrong: %#v", payload)
	}
}

func TestMyStatsPinsScopeAndBlocksAdminRoutes(t *testing.T) {
	fixture := newFixture(t)
	handler := fixture.deps.overviewSectionHandler(true)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/my-stats/usage-overview/summary?systemAccountId=sys-admin-1", adminAuth("super_admin"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("my summary not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	// forceSelfAccessScope downgrades the role, so the router-internal
	// requireAdmin answers 403 on /my-stats/system-metrics/* (the composed
	// self wrapper downgrades before the internal gate runs).
	gate := fixture.deps.selfScopeSession()(fixture.deps.requireAdminInternal(fixture.deps.systemMetricsTrendHandler))
	recorder = invoke(t, gate.ServeHTTP, http.MethodGet, "/__aisys__/api/my-stats/system-metrics/trend", adminAuth("super_admin"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("my-stats system-metrics not 403: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := decodeBody(t, recorder)["message"]; got != "需要管理员权限" {
		t.Fatalf("403 message wrong: %#v", got)
	}
	// The admin surface still passes the internal gate.
	adminGate := fixture.deps.requireAdminInternal(fixture.deps.systemMetricsTrendHandler)
	recorder = invoke(t, adminGate.ServeHTTP, http.MethodGet, "/__aisys__/api/stats/system-metrics/trend", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin system-metrics trend not 200: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestUsageRecordsRejectsUnscopedAdminFilters(t *testing.T) {
	fixture := newFixture(t)
	handler := fixture.deps.usageRecordsListHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/usage-records?model=gpt-5", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unscoped admin filter not 400: %d", recorder.Code)
	}
	if got := decodeBody(t, recorder)["message"]; got != "请先选择系统账户后筛选" {
		t.Fatalf("guard message wrong: %#v", got)
	}
	// my-usage-records pins the caller so the same filter is accepted.
	selfHandler := fixture.deps.usageRecordsListHandler(true)
	recorder = invoke(t, selfHandler, http.MethodGet, "/__aisys__/api/my-usage-records?model=gpt-5", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("self usage-records not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["page"] != float64(1) || payload["pageSize"] != float64(50) || payload["total"] != float64(0) {
		t.Fatalf("usage-records page contract wrong: %#v", payload)
	}
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("usage-records items wrong: %#v", payload["items"])
	}
}

func TestAccountUsagePageAggregatesAndPaginates(t *testing.T) {
	fixture := newFixture(t)
	seed := []string{
		// Owner account scope rows (global admin view): acct-b ranks first.
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd, last_used_at)
			VALUES ('global', 'account', 'acct-b', '2026-09-04', 30, 300, 200, 3.5, '2026-09-04T11:00:00.000Z')`,
		`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
			VALUES ('global', 'account', 'acct-a', '2026-09-04', 10, 100, 50, 1)`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-b', '账户B', 'sys-owner-2', 'openai', 'api_key', 'active')`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-a', '账户A', 'sys-owner-1', 'openai', 'api_key', 'active')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-owner-1', 'owner1', 'Owner One')`,
		`INSERT INTO system_accounts (id, username, display_name) VALUES ('sys-owner-2', 'owner2', 'Owner Two')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	handler := fixture.deps.accountUsageHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/account-usage?pageSize=1", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("account-usage not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	if payload["total"] != float64(2) || payload["hasMore"] != true || payload["pageSize"] != float64(1) {
		t.Fatalf("account-usage paging wrong: %#v", payload)
	}
	rows, ok := payload["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("account-usage rows wrong: %#v", payload["rows"])
	}
	top := rows[0].(map[string]any)
	if top["id"] != "acct-b" || top["ownerSystemAccountId"] != "sys-owner-2" || top["systemAccountName"] != "Owner Two" {
		t.Fatalf("account-usage top row wrong: %#v", top)
	}
	rangeUsage := top["rangeUsage"].(map[string]any)
	if rangeUsage["requestCount"] != float64(30) || rangeUsage["totalCost"] != 3.5 || rangeUsage["totalTokens"] != float64(500) {
		t.Fatalf("range usage wrong: %#v", rangeUsage)
	}
}

func TestAccountUsageSummaryScopedCaller(t *testing.T) {
	fixture := newFixture(t)
	if _, err := fixture.db.Exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, output_tokens, total_cost_usd)
		VALUES ('sys-user-1', 'system_account', 'sys-user-1', '2026-09-04', 5, 10, 5, 0.5)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	handler := fixture.deps.accountUsageSummaryHandler(true)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/my-stats/account-usage/summary", userAuth())
	if recorder.Code != http.StatusOK {
		t.Fatalf("my summary not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	summary := dataMap(t, decodeBody(t, recorder))["summary"].(map[string]any)
	if summary["requestCount"] != float64(5) || summary["totalCost"] != 0.5 {
		t.Fatalf("scoped summary wrong: %#v", summary)
	}
}

func TestAiHealthListReadsHourlyStrip(t *testing.T) {
	fixture := newFixture(t)
	seed := []string{
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status, last_used_at)
			VALUES ('acct-a', '账户A', 'sys-user-1', 'openai', 'api_key', 'active', '2026-09-04T10:00:00.000Z')`,
		`INSERT INTO account_health_hourly (account_id, stat_hour, status, last_observed_at)
			VALUES ('acct-a', '2026-09-04T10', 'success', '2026-09-04T10:30:00.000Z')`,
		`INSERT INTO account_health_hourly (account_id, stat_hour, status, last_observed_at)
			VALUES ('acct-a', '2026-09-04T09', 'failure', '2026-09-04T09:30:00.000Z')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	handler := fixture.deps.aiHealthListHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-health?hours=24&page=1&pageSize=10", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ai-health not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	items := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("ai-health items wrong: %#v", items)
	}
	account := items[0].(map[string]any)
	if account["id"] != "acct-a" || account["latestStatus"] != "success" || account["successHours"] != float64(1) || account["failureHours"] != float64(1) {
		t.Fatalf("ai-health account wrong: %#v", account)
	}
	if account["healthRate"] != float64(50) {
		t.Fatalf("ai-health rate wrong: %#v", account["healthRate"])
	}
	hours := account["hours"].([]any)
	if len(hours) != 24 {
		t.Fatalf("ai-health hour strip wrong: %d", len(hours))
	}
}

func TestAiHealthHourDetailContract(t *testing.T) {
	fixture := newFixture(t)
	handler := fixture.deps.aiHealthHourDetailHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-health/hour-detail?accountId=acct-a&statHour=2026-09-04T25", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid statHour not 400: %d", recorder.Code)
	}
	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-health/hour-detail?accountId=ghost&statHour=2026-09-04T10", adminAuth(""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("invisible account not 404: %d", recorder.Code)
	}
}

func TestAiPerformanceSeriesReadsHourly(t *testing.T) {
	fixture := newFixture(t)
	seed := []string{
		`INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour, request_count, duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max)
			VALUES ('global', 'account', 'acct-a', '2026-09-04T10', 4, 400, 4, 150, 800, 4, 300)`,
		`INSERT INTO accounts (id, name, system_account_id, provider_code, type, status)
			VALUES ('acct-a', '账户A', 'sys-owner-1', 'openai', 'api_key', 'active')`,
	}
	for _, statement := range seed {
		if _, err := fixture.db.Exec(statement); err != nil {
			t.Fatalf("seed %v", err)
		}
	}
	handler := fixture.deps.aiPerformanceSeriesHandler(false)
	recorder := invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-performance/series?startDate=2026-09-04&endDate=2026-09-04&accountIds=acct-a", adminAuth(""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("series not 200: %d %s", recorder.Code, recorder.Body.String())
	}
	payload := dataMap(t, decodeBody(t, recorder))
	series := payload["hourlySeries"].([]any)
	if len(series) != 1 {
		t.Fatalf("series wrong: %#v", series)
	}
	points := series[0].(map[string]any)["points"].([]any)
	if len(points) != 24 {
		t.Fatalf("series points wrong: %d", len(points))
	}
	point := points[10].(map[string]any)
	if point["statHour"] != "2026-09-04T10" || point["requestCount"] != float64(4) || point["maxDurationMs"] != float64(150) || point["averageFirstTokenMs"] != float64(200) {
		t.Fatalf("series point wrong: %#v", point)
	}
	// CSV rejection keeps the repeated-parameter contract.
	recorder = invoke(t, handler, http.MethodGet, "/__aisys__/api/stats/ai-performance/series?accountIds=acct-a,acct-b", adminAuth(""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("csv accountIds not 400: %d", recorder.Code)
	}
	if got := decodeBody(t, recorder)["message"]; got != "accountIds 不接受 CSV，必须使用重复参数" {
		t.Fatalf("csv message wrong: %#v", got)
	}
}

func TestTimezoneMissingFailsClosed(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, key))`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	deps := &Deps{Business: db, Stats: db, Now: func() time.Time { return time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC) }, Timezone: NewSystemSettingsTimezoneSource(db, false)}
	recorder := invoke(t, deps.usageWindowHandler, http.MethodGet, "/__aisys__/api/stats/usage-window", adminAuth(""))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("missing timezone not 500: %d", recorder.Code)
	}
}

var _ = errors.New
var _ = fmt.Sprintf
var _ = context.Background
var _ = url.Values{}
