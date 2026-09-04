package statsagg

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// 端到端 job 对账测试：SQLite 承载测试库（双模之一），SQL 语义与 PG 生产
// 路径由 Dialect 单一来源生成。golden 期望值从 Node 源码逻辑推导并在断言处
// 标注推导依据。

type testEnv struct {
	t       *testing.T
	db      *sql.DB
	dialect Dialect
	now     time.Time
	zone    *time.Location
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := OpenSQLiteTestDB(filepath.Join(t.TempDir(), "stats.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	zone, err := LoadStatsTimezone("UTC")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)
	return &testEnv{t: t, db: db, dialect: Dialect{Postgres: false}, now: now, zone: zone}
}

func (e *testEnv) aggregator() *Aggregator {
	// Now 读取 env.now，测试中改 env.now 可模拟日期翻转。
	return &Aggregator{DB: e.db, Dialect: e.dialect, Clock: StaticTimezoneSource{e.zone}, Now: func() time.Time { return e.now }}
}

func (e *testEnv) refresher() *WindowRefresher {
	return &WindowRefresher{DB: e.db, Dialect: e.dialect, Clock: StaticTimezoneSource{e.zone}, Now: func() time.Time { return e.now }}
}

func (e *testEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.Exec(query, args...); err != nil {
		e.t.Fatalf("exec %s: %v", query, err)
	}
}

// queryFloats 执行返回单列数值行集的查询。
func (e *testEnv) queryFloats(query string, args ...any) []float64 {
	e.t.Helper()
	rows, err := e.db.Query(query, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var result []float64
	for rows.Next() {
		var value float64
		if err := rows.Scan(&value); err != nil {
			e.t.Fatal(err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return result
}

// queryRowFloats 扫描单行多数值列。
func (e *testEnv) queryRowFloats(query string, args ...any) []float64 {
	e.t.Helper()
	rows, err := e.db.Query(query, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil
	}
	columns, _ := rows.Columns()
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	if err := rows.Scan(pointers...); err != nil {
		e.t.Fatal(err)
	}
	result := make([]float64, 0, len(columns))
	for _, value := range values {
		switch typed := value.(type) {
		case int64:
			result = append(result, float64(typed))
		case float64:
			result = append(result, typed)
		default:
			e.t.Fatalf("non-numeric column value %v (%T)", value, value)
		}
	}
	return result
}

func (e *testEnv) queryString(query string, args ...any) []string {
	e.t.Helper()
	rows, err := e.db.Query(query, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			e.t.Fatal(err)
		}
		result = append(result, value.String)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return result
}

func (e *testEnv) assertFloats(label string, got []float64, want ...float64) {
	e.t.Helper()
	if len(got) != len(want) {
		e.t.Fatalf("%s: got %v want %v", label, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			e.t.Fatalf("%s: got %v want %v", label, got, want)
		}
	}
}

func (e *testEnv) seedUsageRecord(record UsageStatsRecordRow) {
	e.t.Helper()
	e.exec(`INSERT INTO usage_records (
		id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, group_id, account_id, endpoint,
		provider_code, provider_protocol_profile_id, model, status_code, success, failure_attribution,
		first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
		cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens,
		output_image_tokens, cost_usd, error_code, error_message,
		account_owner_system_account_id, group_owner_system_account_id, account_access_type, group_access_type,
		account_authorization_id, account_authorization_source_type, account_authorization_source_team_id,
		group_authorization_id, group_authorization_source_type, group_authorization_source_team_id, created_at
	) VALUES (`+placeholders(41)+`)`,
		record.ID, record.SystemAccountID, record.TraceID, record.TrafficSource, record.ClientIP, record.APIKeyID,
		record.GroupID, record.AccountID, record.Endpoint, record.ProviderCode, record.ProviderProtocolProfileID,
		record.Model, record.StatusCode, record.Success, record.FailureAttribution,
		record.FirstTokenMs, record.DurationMs, record.InputTokens, record.OutputTokens,
		record.CacheReadTokens, record.CacheReadCostUsd, record.CacheWriteTokens, record.CacheWrite1hTokens,
		record.CacheWriteCostUsd, record.ThinkingTokens, record.InputImageTokens, record.OutputImageTokens,
		record.CostUsd, record.ErrorCode, record.ErrorMessage,
		record.AccountOwnerSystemAccountID, record.GroupOwnerSystemAccountID, record.AccountAccessType, record.GroupAccessType,
		record.AccountAuthorizationID, record.AccountAuthorizationSourceType, record.AccountAuthorizationSourceTeamID,
		record.GroupAuthorizationID, record.GroupAuthorizationSourceType, record.GroupAuthorizationSourceTeamID,
		record.CreatedAt)
}

// seedGoldenPair 写入对账基线两条 usage_records。
func (e *testEnv) seedGoldenPair() {
	e.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-1", SystemAccountID: "alice", TraceID: "tr-1", TrafficSource: "gateway",
		APIKeyID: strPtr("key-1"), Endpoint: strPtr("/v1/chat/completions"),
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
		Success:    1,
		DurationMs: f64Ptr(1200), FirstTokenMs: f64Ptr(250),
		InputTokens: f64Ptr(100), OutputTokens: f64Ptr(50),
		CacheReadTokens: f64Ptr(10), CacheReadCostUsd: f64Ptr(0.001),
		CostUsd: f64Ptr(0.02), CreatedAt: "2026-04-18T10:15:00.000Z",
		AccountID: strPtr("acc-1"), AccountOwnerSystemAccountID: strPtr("alice"), AccountAccessType: strPtr("owner"),
		GroupID: strPtr("grp-1"), GroupOwnerSystemAccountID: strPtr("alice"), GroupAccessType: strPtr("owner"),
	})
	e.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-2", SystemAccountID: "bob", TraceID: "tr-2", TrafficSource: "gateway",
		ProviderCode: strPtr("openai"), Model: strPtr("gpt-5"),
		Success: 0, StatusCode: f64Ptr(429), ErrorCode: strPtr("rate_limited"),
		InputTokens: f64Ptr(5), CostUsd: f64Ptr(0), CreatedAt: "2026-04-18T10:20:00.000Z",
	})
}

// TestAggregateUsageStatsBatchGolden：usage-stats-aggregation job 主对账。
// 记录时间键（UTC）：r1 minute=2026-04-18T10:15 / hour=T10 / date=2026-04-18 /
// week=2026-04-13（04-18 周六 → 周一起始 04-13）/ month=2026-04。
func TestAggregateUsageStatsBatchGolden(t *testing.T) {
	env := newTestEnv(t)
	env.seedGoldenPair()
	aggregator := env.aggregator()
	processed, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if processed != 2 {
		t.Fatalf("processed = %d want 2", processed)
	}

	// usage_stats_totals：r1 扇出 10 个 scope、r2 扇出 3 个 scope（system_account
	// caller/global、provider、model——r2 无 account/group/key/endpoint）。
	// 推导（usageStatsAccumulatorFromRecord + aggregatePostgresUsageStatsRows）：
	// (alice,system_account,alice) 仅 r1 → request 1 success 1。
	env.assertFloats("alice totals",
		env.queryRowFloats(`SELECT request_count, success_count, error_count, input_tokens, output_tokens, total_cost_usd,
			duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max
			FROM usage_stats_totals WHERE system_account_id='alice' AND scope_type='system_account' AND scope_id='alice'`),
		1, 1, 0, 100, 50, 0.02, 1200, 1, 1200, 250, 1, 250)
	// global scope 聚合两条：request 2、input 105=100+5、cost 0.02。
	env.assertFloats("global totals",
		env.queryRowFloats(`SELECT request_count, success_count, error_count, input_tokens, output_tokens, total_cost_usd
			FROM usage_stats_totals WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global'`),
		2, 1, 1, 105, 50, 0.02)
	if got := env.queryString(`SELECT last_used_at FROM usage_stats_totals WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global'`); len(got) != 1 || got[0] != "2026-04-18T10:20:00.000Z" {
		t.Fatalf("global last_used_at = %v want r2 created_at", got)
	}
	if got := env.queryString(`SELECT last_error_at FROM usage_stats_totals WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global'`); len(got) != 1 || got[0] != "2026-04-18T10:20:00.000Z" {
		t.Fatalf("global last_error_at = %v want r2 created_at", got)
	}
	// group/account/provider/api_key/model/endpoint scope = r1 值（owner 均为 alice）。
	for _, scope := range []struct{ scopeType, scopeID string }{
		{"group", "grp-1"}, {"account", "acc-1"}, {"caller_account", "acc-1"},
		{"provider", "openai"}, {"api_key", "key-1"}, {"model", "gpt-5"}, {"endpoint", "/v1/chat/completions"},
	} {
		env.assertFloats(fmt.Sprintf("alice %s/%s", scope.scopeType, scope.scopeID),
			env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd
				FROM usage_stats_totals WHERE system_account_id='alice' AND scope_type=? AND scope_id=?`,
				scope.scopeType, scope.scopeID),
			1, 100, 0.02)
	}
	env.assertFloats("global account",
		env.queryRowFloats(`SELECT request_count FROM usage_stats_totals WHERE system_account_id='global' AND scope_type='account' AND scope_id='acc-1'`),
		1)

	// 5 个时间桶逐桶锁定。
	env.assertFloats("daily global",
		env.queryRowFloats(`SELECT request_count, error_count FROM usage_stats_daily
			WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global' AND stat_date='2026-04-18'`),
		2, 1)
	env.assertFloats("hourly global",
		env.queryRowFloats(`SELECT request_count, input_tokens FROM usage_stats_hourly
			WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global' AND stat_hour='2026-04-18T10'`),
		2, 105)
	env.assertFloats("weekly alice",
		env.queryRowFloats(`SELECT request_count FROM usage_stats_weekly
			WHERE system_account_id='alice' AND scope_type='system_account' AND scope_id='alice' AND stat_week='2026-04-13'`),
		1)
	env.assertFloats("monthly global",
		env.queryRowFloats(`SELECT request_count FROM usage_stats_monthly
			WHERE system_account_id='global' AND scope_type='system_account' AND scope_id='global' AND stat_month='2026-04'`),
		2)
	env.assertFloats("minute alice",
		env.queryRowFloats(`SELECT request_count FROM usage_stats_minute
			WHERE system_account_id='alice' AND scope_type='system_account' AND scope_id='alice' AND stat_minute='2026-04-18T10:15'`),
		1)

	// usage_model_daily：caller + global 两条模型行。
	env.assertFloats("model alice",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd FROM usage_model_daily
			WHERE system_account_id='alice' AND stat_date='2026-04-18' AND provider_code='openai' AND model='gpt-5'`),
		1, 100, 0.02)
	env.assertFloats("model global",
		env.queryRowFloats(`SELECT request_count, input_tokens FROM usage_model_daily
			WHERE system_account_id='global' AND stat_date='2026-04-18' AND provider_code='openai' AND model='gpt-5'`),
		2, 105)

	// usage_error_daily：仅失败行；error_group/provider_code = provider_code ||
	// 'unknown'，error_code = error_code || String(status_code) || 'unknown'；
	// caller(bob) + global 两行。
	env.assertFloats("error rows",
		env.queryRowFloats(`SELECT COUNT(*), COALESCE(SUM(error_count), 0), COALESCE(SUM(request_count), 0) FROM usage_error_daily
			WHERE error_group='openai' AND provider_code='openai' AND error_code='rate_limited' AND status_code=429`),
		2, 2, 2)

	// usage_latency_daily：r1 duration 1200 → 桶 2000（上界数组第一个 >= 值），
	// first_token 250 → 桶 250；r1 的 10 个 scope 各 1 个样本。r2 无
	// duration/first_token → 不产生样本。
	env.assertFloats("latency duration buckets",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_latency_daily WHERE metric_type='duration_ms' AND bucket_upper_bound_ms=2000 AND sample_count=1`),
		10)
	env.assertFloats("latency first token buckets",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_latency_daily WHERE metric_type='first_token_ms' AND bucket_upper_bound_ms=250 AND sample_count=1`),
		10)

	// stats_job_state 游标推进到最后一条（created_at, id）。
	if got := env.queryString(`SELECT cursor_created_at || '|' || cursor_id FROM stats_job_state WHERE scope_type='global' AND job_name='usage_stats_aggregation'`); len(got) != 1 || got[0] != "2026-04-18T10:20:00.000Z|rec-2" {
		t.Fatalf("cursor = %v", got)
	}
	// 派生窗口脏标记：daily system_account → overview dirty；daily account →
	// ai performance dirty；hourly quota scope 无 → quota dirty 空。
	env.assertFloats("overview dirty",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_overview_dirty_scopes WHERE system_account_id IN ('alice','global') AND min_changed_date='2026-04-18'`),
		2)
	env.assertFloats("ai dirty",
		env.queryRowFloats(`SELECT COUNT(*) FROM ai_performance_summary_dirty_system_accounts WHERE system_account_id IN ('alice','global') AND min_stat_date='2026-04-18' AND max_stat_date='2026-04-18'`),
		2)
	// quota dirty：rec-1 的 api_key scope 进入 usage_stats_hourly → 标记
	// 1 条（api_key 属 quota scope types）。
	env.assertFloats("quota dirty api_key",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_quota_hourly_window_dirty_scopes WHERE scope_type='api_key'`),
		1)
}

// TestAggregateBatchEmptyIdempotentAndCursor：空输入、重复游标（幂等）、批量
// 分页、乱序到达与 safeCreatedBefore 边界。
func TestAggregateBatchEmptyIdempotentAndCursor(t *testing.T) {
	env := newTestEnv(t)
	aggregator := env.aggregator()

	// 空输入：processed=0，仍写 last_success_at（Node rows.length===0 分支）。
	processed, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
	if err != nil || processed != 0 {
		t.Fatalf("empty run processed=%d err=%v", processed, err)
	}
	if got := env.queryString(`SELECT last_success_at FROM stats_job_state WHERE job_name='usage_stats_aggregation'`); len(got) != 1 || got[0] != "2026-04-18T12:00:00.000Z" {
		t.Fatalf("empty run last_success_at = %v want fixed now", got)
	}

	env.seedGoldenPair()
	// safeCreatedBefore 边界：只收 r1（10:15 <= 10:16 < 10:20）。
	processed, err = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T10:16:00.000Z"})
	if err != nil || processed != 1 {
		t.Fatalf("bounded run processed=%d err=%v", processed, err)
	}
	env.assertFloats("safe boundary totals",
		env.queryRowFloats(`SELECT COALESCE(SUM(request_count), 0) FROM usage_stats_totals`),
		10) // 仅 r1 的 10 个 scope
	// 重复游标：同参数重跑 → processed=0、聚合结果不变（游标幂等）。
	processed, err = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T10:16:00.000Z"})
	if err != nil || processed != 0 {
		t.Fatalf("duplicate cursor run processed=%d err=%v", processed, err)
	}
	env.assertFloats("idempotent totals",
		env.queryRowFloats(`SELECT COALESCE(SUM(request_count), 0) FROM usage_stats_totals`),
		10)
	// 批量分页：batch=1，两次跑完剩余 r2。
	processed, _ = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 1, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
	if processed != 1 {
		t.Fatalf("paged run 1 processed=%d", processed)
	}
	processed, _ = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 1, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
	if processed != 0 {
		t.Fatalf("paged run 2 should be empty, processed=%d", processed)
	}
	env.assertFloats("after pagination totals",
		env.queryRowFloats(`SELECT COALESCE(SUM(request_count), 0) FROM usage_stats_totals WHERE system_account_id='global' AND scope_type='system_account'`),
		2)
	// 乱序到达（迟到记录 created_at 早于游标）：游标单调推进，迟于游标的
	// 记录不回补——与 Node 游标 WHERE 语义一致（ingest 保证写入序）。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-late", SystemAccountID: "alice", TrafficSource: "gateway", ProviderCode: strPtr("openai"),
		Success: 1, CreatedAt: "2026-04-18T10:14:00.000Z",
	})
	processed, _ = aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
	if processed != 0 {
		t.Fatalf("late record must not be aggregated behind cursor, processed=%d", processed)
	}
	// 新库中记录按 created_at 升序取批（ORDER BY created_at ASC, id ASC），
	// 游标落在最晚一条。
	env2 := newTestEnv(t)
	env2.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-b", SystemAccountID: "alice", TrafficSource: "gateway", ProviderCode: strPtr("openai"),
		Success: 1, CreatedAt: "2026-04-18T10:20:00.000Z",
	})
	env2.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-a", SystemAccountID: "alice", TrafficSource: "gateway", ProviderCode: strPtr("openai"),
		Success: 1, CreatedAt: "2026-04-18T10:10:00.000Z",
	})
	processed, err = env2.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
	if err != nil || processed != 2 {
		t.Fatalf("ordered fetch processed=%d err=%v", processed, err)
	}
	if got := env2.queryString(`SELECT cursor_id FROM stats_job_state WHERE job_name='usage_stats_aggregation'`); len(got) != 1 || got[0] != "rec-b" {
		t.Fatalf("cursor id = %v want rec-b", got)
	}
}

// TestFilterAndAuthorizationDailyGolden：不可聚合行被跳过；带授权字段的行写入
// 授权摘要日报（authorization-usage-range-windows job 的源表）。
func TestFilterAndAuthorizationDailyGolden(t *testing.T) {
	env := newTestEnv(t)
	// 非法 access_type → shouldAggregate=false → 无聚合行。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-bad", SystemAccountID: "alice", TrafficSource: "gateway",
		AccountID: strPtr("acc-1"), AccountAccessType: strPtr("friend"), AccountOwnerSystemAccountID: strPtr("alice"),
		Success: 1, CreatedAt: "2026-04-18T10:15:00.000Z",
	})
	processed, err := env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
	if err != nil || processed != 1 {
		t.Fatalf("processed=%d err=%v (row fetched but filtered)", processed, err)
	}
	env.assertFloats("filtered rows",
		env.queryRowFloats(`SELECT COUNT(*) FROM usage_stats_totals`), 0)

	// 授权行：account 授权（team 来源）→ owner + global 双维度摘要。
	// 推导（usage-stats-authorization-daily-writer.ts）：authorizationReportRows
	// 要求 owner≠caller；scopeRows = [owner, global]；键展开 = 3 个资源过滤器 ×
	// [user 全体、user 授权者] + team 来源追加 3 × [team 全体、team 团队、
	// team×user 全体、team×user 授权者]。
	// team 摘要行 = 3 过滤器 × 2 键 × 2 维度 = 12；user 摘要行 = (3+3) 键 × 2 维度 = 24。
	env.seedUsageRecord(UsageStatsRecordRow{
		ID: "rec-auth", SystemAccountID: "grantee-1", TrafficSource: "gateway",
		Success: 1, InputTokens: f64Ptr(20), CostUsd: f64Ptr(0.1),
		CreatedAt: "2026-04-18T10:16:00.000Z",
		AccountID: strPtr("acc-7"), AccountOwnerSystemAccountID: strPtr("owner-1"), AccountAccessType: strPtr("account_authorized"),
		AccountAuthorizationID: strPtr("auth-2"), AccountAuthorizationSourceType: strPtr("team"), AccountAuthorizationSourceTeamID: strPtr("team-5"),
	})
	processed, err = env.aggregator().AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
	if err != nil || processed != 1 {
		t.Fatalf("auth run processed=%d err=%v", processed, err)
	}
	env.assertFloats("team summary rows",
		env.queryRowFloats(`SELECT COUNT(*) FROM authorization_team_usage_summary_daily`),
		12)
	env.assertFloats("user summary rows",
		env.queryRowFloats(`SELECT COUNT(*) FROM authorization_user_usage_summary_daily`),
		24)
	// 抽样对账：owner-1 全体键。
	env.assertFloats("team summary all",
		env.queryRowFloats(`SELECT request_count FROM authorization_team_usage_summary_daily
			WHERE system_account_id='owner-1' AND stat_date='2026-04-18' AND team_filter_id='' AND resource_filter_type='all' AND resource_filter_id=''`),
		1)
	env.assertFloats("team summary team-5",
		env.queryRowFloats(`SELECT request_count FROM authorization_team_usage_summary_daily
			WHERE system_account_id='owner-1' AND stat_date='2026-04-18' AND team_filter_id='team-5' AND resource_filter_type='all' AND resource_filter_id=''`),
		1)
	env.assertFloats("user summary grantee",
		env.queryRowFloats(`SELECT request_count FROM authorization_user_usage_summary_daily
			WHERE system_account_id='owner-1' AND grantee_filter_system_account_id='grantee-1' AND resource_filter_type='all' AND resource_filter_id=''`),
		1)
	// resource_authorizations 命中时 account_authorization_team scopeId 使用
	// instance account（Node accountAuthorizationInstanceAccountIds 查找）。
	env.exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, grantee_system_account_id)
		VALUES ('auth-2', 'account', 'acc-7', 'grantee-1')`)
	env.exec(`INSERT INTO accounts (id, system_account_id, authorization_instance_authorization_id)
		VALUES ('instance-1', 'grantee-1', 'auth-2')`)
}

// TestWindowStagesGolden：排行快照、overview、scope range、authorization range、
// ai performance 等窗口 stage 的聚合对账。
func TestWindowStagesGolden(t *testing.T) {
	env := newTestEnv(t)
	env.exec(`INSERT INTO usage_stats_totals (system_account_id, scope_type, scope_id, request_count, updated_at)
		VALUES ('alice','system_account','alice',3,'2026-04-18T09:00:00.000Z')`)
	insertDaily := func(system, scopeType, scopeID, statDate string, request, input, cost float64) {
		env.exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date,
			request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max,
			first_token_ms_sum, first_token_ms_count, first_token_ms_max, last_used_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			system, scopeType, scopeID, statDate, request, input, cost, 100*request, 1, 100, 50, 1, 50,
			"2026-04-18T09:00:00.000Z", "2026-04-18T09:00:00.000Z")
	}
	insertDaily("alice", "system_account", "alice", "2026-04-17", 2, 10, 0.3)
	insertDaily("alice", "system_account", "alice", "2026-04-18", 1, 7, 0.2)
	insertDaily("alice", "account", "acc-1", "2026-04-18", 4, 40, 0.4)
	insertDaily("bob", "system_account", "bob", "2026-04-18", 5, 50, 0.5)
	insertMonthly := func(system, scopeType, scopeID, statMonth string, request, cost float64, lastUsed string) {
		env.exec(`INSERT INTO usage_stats_monthly (system_account_id, scope_type, scope_id, stat_month,
			request_count, total_cost_usd, last_used_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			system, scopeType, scopeID, statMonth, request, cost, lastUsed, "2026-04-18T09:00:00.000Z")
	}
	// 排行对账种子：metric（cost）并列时 last_used_at DESC 决定次序。
	insertMonthly("alice", "api_key", "key-b", "2026-04", 3, 1.0, "2026-04-18T08:00:00.000Z")
	insertMonthly("alice", "api_key", "key-a", "2026-04", 3, 1.0, "2026-04-18T09:00:00.000Z")
	insertMonthly("alice", "api_key", "key-c", "2026-04", 1, 0.1, "2026-04-18T07:00:00.000Z")
	insertMonthly("bob", "api_key", "key-d", "2026-03", 9, 9.0, "2026-03-31T00:00:00.000Z")
	refresher := env.refresher()

	// usage-rank-snapshots-refresh（api_key current_month 排行）。
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageApiKeyCurrentMonthCostRank}, RefreshOptions{JobName: "usage-rank-snapshots-refresh-test"}); err != nil {
		t.Fatal(err)
	}
	// 推导（usage-stats-snapshot-helpers.ts refreshUsageRankSnapshotFromStats）：
	// stat_month = 当月（2026-04）桶只有 alice 三行；ROW_NUMBER ORDER BY
	// metric_value DESC, last_used_at DESC, scope_id ASC → key-a、key-b、key-c；
	// bob 只有 03 桶，不进本月排行。
	if got := env.queryString(`SELECT scope_id FROM usage_rank_snapshots WHERE metric='total_cost_usd' AND window_key='current_month' ORDER BY rank ASC`); len(got) != 3 || got[0] != "key-a" || got[1] != "key-b" || got[2] != "key-c" {
		t.Fatalf("rank order = %v", got)
	}
	// metric_value 是 cost 列的 SUM：key-a/key-b 并列 1.0、key-c 0.1。
	env.assertFloats("rank values",
		env.queryFloats(`SELECT metric_value FROM usage_rank_snapshots WHERE metric='total_cost_usd' AND window_key='current_month' ORDER BY rank ASC`),
		1.0, 1.0, 0.1)
	// last7d 排行（usage_stats_daily，stat_date >= today-6 = 2026-04-12）。
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageAccountLast7dRequestRank}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	// metric_value = SUM(request_count)：last7d 排行只读 scope_type='account'
	//（Node refreshAccountLast7dRequestRankSnapshot）→ 种子中仅
	// alice/account/acc-1（4）命中；bob 的 system_account 行不参与。
	env.assertFloats("account last7d rank",
		env.queryFloats(`SELECT metric_value FROM usage_rank_snapshots WHERE metric='request_count' AND window_key='last7d' ORDER BY rank ASC`),
		4)

	// usage-overview-windows-refresh（summary/trend/model rank/error rank）。
	env.exec(`INSERT INTO usage_stats_hourly (system_account_id, scope_type, scope_id, stat_hour,
		request_count, error_count, input_tokens, output_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, updated_at)
		VALUES ('alice','system_account','alice','2026-04-18T08',1,0,7,3,0.2,300,1,'2026-04-18T09:00:00.000Z'),
		       ('alice','system_account','alice','2026-04-18T09',1,0,4,2,0.1,100,1,'2026-04-18T09:00:00.000Z')`)
	env.exec(`INSERT INTO usage_model_daily (system_account_id, stat_date, provider_code, model, request_count, total_cost_usd, updated_at)
		VALUES ('alice','2026-04-18','openai','gpt-5',5,0.5,'2026-04-18T09:00:00.000Z'),
		       ('alice','2026-04-17','openai','gpt-4.1',3,0.4,'2026-04-18T09:00:00.000Z')`)
	env.exec(`INSERT INTO usage_error_daily (system_account_id, stat_date, error_group, provider_code, error_code, status_code, error_count, updated_at)
		VALUES ('alice','2026-04-18','openai','openai','rate_limited',429,2,'2026-04-18T09:00:00.000Z'),
		       ('alice','2026-04-17','openai','openai','timeout',504,5,'2026-04-18T09:00:00.000Z')`)
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageOverviewWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	// summary today：仅 04-18 行。
	env.assertFloats("overview summary today",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd FROM usage_overview_summary_windows
			WHERE system_account_id='alice' AND window_key='2026-04-18:2026-04-18'`),
		1, 7, 0.2)
	// summary last7d：04-17（duration 200/1）+ 04-18（100/1）→ sum 300、
	// count 2。Node 的 summary 窗口表没有 duration_ms_max 列（仅 sum/count）。
	env.assertFloats("overview summary last7d",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd, duration_ms_sum, duration_ms_count
			FROM usage_overview_summary_windows WHERE system_account_id='alice' AND window_key='2026-04-12:2026-04-18'`),
		3, 17, 0.5, 300, 2)
	// trend today：days=1 → 1h 桶，两桶。
	env.assertFloats("overview trend today buckets",
		env.queryRowFloats(`SELECT COUNT(*), COALESCE(SUM(request_count), 0) FROM usage_overview_trend_windows
			WHERE system_account_id='alice' AND window_key='2026-04-18:2026-04-18'`),
		2, 2)
	// trend last7d：days=7 → 24h 桶合并为 "2026-04-18"。
	env.assertFloats("overview trend last7d bucket",
		env.queryRowFloats(`SELECT request_count, input_tokens FROM usage_overview_trend_windows
			WHERE system_account_id='alice' AND window_key='2026-04-12:2026-04-18' AND bucket_key='2026-04-18'`),
		2, 11)
	// model rank last7d：request DESC → gpt-5(5) rank1、gpt-4.1(3) rank2。
	if got := env.queryString(`SELECT model FROM usage_model_rank_windows WHERE window_key='2026-04-12:2026-04-18' AND rank IN (1,2) ORDER BY rank ASC`); len(got) != 2 || got[0] != "gpt-5" || got[1] != "gpt-4.1" {
		t.Fatalf("model rank = %v", got)
	}
	// error rank last7d：error_count DESC → timeout(5) rank1、rate_limited(2) rank2。
	if got := env.queryString(`SELECT error_code FROM usage_error_rank_windows WHERE window_key='2026-04-12:2026-04-18' AND rank IN (1,2) ORDER BY rank ASC`); len(got) != 2 || got[0] != "timeout" || got[1] != "rate_limited" {
		t.Fatalf("error rank = %v", got)
	}

	// usage-scope-range-windows-refresh：全 scope 聚合 + HAVING 过滤全零 scope
	//（usage-range-windows.repository.ts）。
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageScopeRangeWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	env.assertFloats("scope range alice system 7d",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd, active_days FROM usage_scope_range_windows
			WHERE system_account_id='alice' AND scope_type='system_account' AND scope_id='alice' AND start_date='2026-04-12' AND end_date='2026-04-18'`),
		3, 17, 0.5, 2)
	env.assertFloats("scope range alice account today",
		env.queryRowFloats(`SELECT request_count, active_days FROM usage_scope_range_windows
			WHERE system_account_id='alice' AND scope_type='account' AND start_date='2026-04-18' AND end_date='2026-04-18'`),
		4, 1)

	// authorization-usage-range-windows-refresh。
	env.exec(`INSERT INTO authorization_team_usage_summary_daily (system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id,
		request_count, input_tokens, total_cost_usd, last_used_at, updated_at)
		VALUES ('owner-1','2026-04-18','','all','',6,60,0.6,'2026-04-18T09:00:00.000Z','2026-04-18T09:00:00.000Z'),
		       ('owner-1','2026-04-18','team-7','account','acc-3',2,20,0.2,'2026-04-18T09:00:00.000Z','2026-04-18T09:00:00.000Z'),
		       ('owner-1','2026-04-17','','all','',1,10,0.1,'2026-04-17T09:00:00.000Z','2026-04-18T09:00:00.000Z')`)
	env.exec(`INSERT INTO authorization_user_usage_summary_daily (system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
		request_count, input_tokens, total_cost_usd, last_used_at, updated_at)
		VALUES ('owner-1','2026-04-18','','grantee-9','all','',3,30,0.3,'2026-04-18T09:00:00.000Z','2026-04-18T09:00:00.000Z')`)
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageAuthorizationUsageRangeWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	env.assertFloats("auth team range 7d",
		env.queryRowFloats(`SELECT request_count, input_tokens, total_cost_usd FROM authorization_team_usage_range_windows
			WHERE system_account_id='owner-1' AND start_date='2026-04-12' AND end_date='2026-04-18' AND team_filter_id='' AND resource_filter_type='all'`),
		7, 70, 0.7)
	env.assertFloats("auth team range today rows",
		env.queryRowFloats(`SELECT COUNT(*) FROM authorization_team_usage_range_windows WHERE start_date='2026-04-18' AND end_date='2026-04-18'`),
		2)
	env.assertFloats("auth user range 7d",
		env.queryRowFloats(`SELECT request_count FROM authorization_user_usage_range_windows
			WHERE system_account_id='owner-1' AND start_date='2026-04-12' AND end_date='2026-04-18' AND grantee_filter_system_account_id='grantee-9'`),
		3)

	// ai-performance-summary-windows-refresh（scope_type='account' 聚合）。
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageAiPerformanceSummaryWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	// alice account scope 04-18 一行：request 4、duration 400/4、first_token 200/4。
	env.assertFloats("ai performance alice 7d",
		env.queryRowFloats(`SELECT request_count, duration_ms_sum, duration_ms_count, first_token_ms_sum FROM ai_performance_summary_windows
			WHERE system_account_id='alice' AND window_key='2026-04-12:2026-04-18'`),
		4, 400, 4, 200)
	// global 只聚合 scope_type='account' 的行（Node 源查询 WHERE scope_type='account'）。
	env.assertFloats("ai performance global 7d",
		env.queryRowFloats(`SELECT request_count FROM ai_performance_summary_windows
			WHERE system_account_id='global' AND window_key='2026-04-12:2026-04-18'`),
		4)
}

// TestSystemMetricsJobsGolden：system-metrics-sample 写入与
// system-metrics-trend-windows-refresh 聚合对账。
func TestSystemMetricsJobsGolden(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	cpu10, cpu30 := 10.0, 30.0
	lag12 := 12.0
	rx100, tx200 := 100.0, 200.0
	sampleA := SystemMetricsSampleInput{
		SampledAt: "2026-04-18T10:00:30.000Z", CPUPercent: &cpu10, MemoryUsedPercent: &cpu30,
		ProcessRssBytes: f64Ptr(1000), ProcessHeapUsedBytes: f64Ptr(500),
		EventLoopLagMs: &lag12, NetworkRxBytesPerSecond: &rx100, NetworkTxBytesPerSecond: &tx200,
		DBFileBytes: f64Ptr(4000), StatsLagSeconds: f64Ptr(30),
	}
	sampleB := SystemMetricsSampleInput{
		SampledAt: "2026-04-18T10:30:00.000Z", CPUPercent: &cpu30, MemoryUsedPercent: &cpu10,
		ProcessRssBytes: f64Ptr(3000), ProcessHeapUsedBytes: f64Ptr(700),
		DBFileBytes: f64Ptr(6000), StatsLagSeconds: f64Ptr(20),
	}
	if err := refresher.InsertSystemMetricsSampleBatch(context.Background(), sampleA, nil); err != nil {
		t.Fatal(err)
	}
	if err := refresher.InsertSystemMetricsSampleBatch(context.Background(), sampleB, nil); err != nil {
		t.Fatal(err)
	}
	// hourly 对账（upsertSystemMetricsHourly）：sum 累加、max NULL 感知 CASE、
	// sample_count +1/次、count 列 0/1 累加。
	env.assertFloats("hourly aggregates",
		env.queryRowFloats(`SELECT sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum, memory_used_percent_max,
			process_rss_bytes_sum, process_rss_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_count,
			network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_count, db_file_bytes_max, stats_lag_seconds_max
			FROM system_metrics_hourly WHERE stat_hour='2026-04-18T10'`),
		2, 40, 30, 40, 30, 4000, 3000, 12, 1, 100, 1, 6000, 30)
	// 采样行两条。
	env.assertFloats("samples rows",
		env.queryRowFloats(`SELECT COUNT(*) FROM system_metrics_samples WHERE sampled_at LIKE '2026-04-18T10%'`),
		2)
	// 事件循环样本：全空指标被丢弃（normalizedProcessEventLoopSample）。
	if err := refresher.InsertSystemMetricsSampleBatch(context.Background(), SystemMetricsSampleInput{SampledAt: "2026-04-18T10:40:00.000Z"},
		[]ProcessEventLoopSampleInput{
			{ProcessRole: "server", SampledAt: "2026-04-18T10:40:00.000Z"},
			{ProcessRole: "stats-worker", SampledAt: "2026-04-18T10:40:00.000Z", EventLoopLagMs: f64Ptr(5), ProcessRssBytes: f64Ptr(800)},
		}); err != nil {
		t.Fatal(err)
	}
	env.assertFloats("process hourly rows",
		env.queryRowFloats(`SELECT COUNT(*), COALESCE(SUM(sample_count), 0) FROM process_event_loop_hourly WHERE stat_hour='2026-04-18T10'`),
		1, 1)
	env.assertFloats("process hourly values",
		env.queryRowFloats(`SELECT event_loop_lag_ms_sum, event_loop_lag_ms_count FROM process_event_loop_hourly WHERE process_role='stats-worker'`),
		5, 1)

	// trend windows：today（1 天 → 1h 桶）与 7d（24h 桶）。
	if _, err := refresher.RunStages(context.Background(), []WindowStageName{StageSystemMetricsTrendWindows}, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	// 主采样行无条件写入（Node 仅过滤全空的 process 事件循环样本）：
	// 第三次插入（10:40）没有指标但主 sample_count 仍 +1 → 3。
	env.assertFloats("trend today hourly bucket",
		env.queryRowFloats(`SELECT sample_count, cpu_percent_sum, cpu_percent_max FROM system_metrics_trend_windows
			WHERE window_key='2026-04-18:2026-04-18' AND bucket_key='2026-04-18T10'`),
		3, 40, 30)
	env.assertFloats("trend last7d daily bucket",
		env.queryRowFloats(`SELECT sample_count, network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_count, process_rss_bytes_max
			FROM system_metrics_trend_windows WHERE window_key='2026-04-12:2026-04-18' AND bucket_key='2026-04-18'`),
		3, 100, 1, 3000)
	env.assertFloats("process trend bucket",
		env.queryRowFloats(`SELECT sample_count, event_loop_lag_ms_sum FROM process_event_loop_trend_windows
			WHERE window_key='2026-04-18:2026-04-18' AND bucket_key='2026-04-18T10' AND process_role='stats-worker'`),
		1, 5)

	// sourceVersion：同输入稳定、v2:64hex 格式、行值变化时变化。
	version1, err := systemMetricsTrendSourceVersion(context.Background(), env.db, env.dialect)
	if err != nil {
		t.Fatal(err)
	}
	version2, err := systemMetricsTrendSourceVersion(context.Background(), env.db, env.dialect)
	if err != nil {
		t.Fatal(err)
	}
	if version1 != version2 || len(version1) != 67 || version1[:3] != "v2:" {
		t.Fatalf("sourceVersion mismatch: %s vs %s", version1, version2)
	}
	if err := requireSystemMetricsTrendSourceVersion(version1); err != nil {
		t.Fatalf("sourceVersion pattern rejected: %v", err)
	}
	env.exec(`UPDATE system_metrics_hourly SET cpu_percent_max = 99 WHERE stat_hour='2026-04-18T10'`)
	version3, err := systemMetricsTrendSourceVersion(context.Background(), env.db, env.dialect)
	if err != nil {
		t.Fatal(err)
	}
	if version3 == version1 {
		t.Fatal("sourceVersion must change when watermark rows change")
	}
}

// TestWatermarkSkipSemantics：窗口 job 的 source watermark 跳过与日期翻转。
func TestWatermarkSkipSemantics(t *testing.T) {
	env := newTestEnv(t)
	refresher := env.refresher()
	env.exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, updated_at)
		VALUES ('alice','system_account','alice','2026-04-18',1,'2026-04-18T09:00:00.000Z')`)
	options := RefreshOptions{SkipIfUnchanged: true, JobName: "usage-scope-range-windows-refresh"}
	// 第一次：无历史状态 → 执行。
	result, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageScopeRangeWindows}, options)
	if err != nil || result.Skipped {
		t.Fatalf("first run skipped=%v err=%v result=%+v", result.Skipped, err, result)
	}
	// 状态行：cursor_created_at = watermark、cursor_id = todayKey
	//（Node updateUsageRankSnapshotRefreshJobState）。
	if got := env.queryString(`SELECT cursor_created_at || '|' || cursor_id FROM stats_job_state WHERE job_name='usage-scope-range-windows-refresh'`); len(got) != 1 || got[0] != "2026-04-18T09:00:00.000Z|2026-04-18" {
		t.Fatalf("job state = %v", got)
	}
	// 第二次：watermark 与 refreshDate 均未变 → 跳过。
	result, err = refresher.RunStages(context.Background(), []WindowStageName{StageUsageScopeRangeWindows}, options)
	if err != nil || !result.Skipped || result.SkipReason != "source_watermark_unchanged" {
		t.Fatalf("second run = %+v err=%v", result, err)
	}
	// 源表更新 → watermark 推进 → 执行。
	env.exec(`UPDATE usage_stats_daily SET updated_at='2026-04-18T10:30:00.000Z'`)
	result, err = refresher.RunStages(context.Background(), []WindowStageName{StageUsageScopeRangeWindows}, options)
	if err != nil || result.Skipped {
		t.Fatalf("third run should execute: %+v err=%v", result, err)
	}
	// 日期翻转（refreshDate 变化）→ 即使 watermark 相同也执行。
	env.now = env.now.Add(24 * time.Hour)
	result, err = refresher.RunStages(context.Background(), []WindowStageName{StageUsageScopeRangeWindows}, options)
	if err != nil || result.Skipped {
		t.Fatalf("date rollover run should execute: %+v err=%v", result, err)
	}
	if got := env.queryString(`SELECT cursor_id FROM stats_job_state WHERE job_name='usage-scope-range-windows-refresh'`); len(got) != 1 || got[0] != "2026-04-19" {
		t.Fatalf("after rollover cursor_id = %v want 2026-04-19", got)
	}
	// hot window refresh = overview + scope range 两个固定阶段。
	hotResult, err := refresher.RunHotWindows(context.Background(), RefreshOptions{SkipIfUnchanged: true, JobName: "usage_hot_window_refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hotResult.Stages) != 2 {
		t.Fatalf("hot window stages = %+v", hotResult.Stages)
	}
}

// TestConcurrentWindowRefreshRace：并发窗口竞争。SQLite 单连接串行化镜像
// 生产 stats-writer 单写者；断言并发下无重复行、终态一致（-race 检测竞争）。
func TestConcurrentWindowRefreshRace(t *testing.T) {
	env := newTestEnv(t)
	env.exec(`INSERT INTO usage_stats_daily (system_account_id, scope_type, scope_id, stat_date, request_count, input_tokens, total_cost_usd, updated_at)
		VALUES ('alice','system_account','alice','2026-04-18',2,10,0.2,'2026-04-18T09:00:00.000Z')`)
	refresher := env.refresher()
	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := refresher.RunStages(context.Background(), []WindowStageName{StageUsageScopeRangeWindows}, RefreshOptions{})
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent refresh error: %v", err)
		}
	}
	// 4 次全量重建后行数仍为唯一窗口行集：alice 只有 04-18 数据，覆盖它的
	// 热窗口为 [04-18]、[04-12..04-18]、[04-01..04-18]、[03-19..04-18]。
	rows := env.queryFloats(`SELECT COUNT(*) FROM usage_scope_range_windows WHERE system_account_id='alice'`)
	if len(rows) != 1 || rows[0] != 4 {
		t.Fatalf("concurrent final rows = %v want 4", rows)
	}
	// 并发 usage-stats-aggregation：游标保证每条记录恰好聚合一次。
	seedEnv := newTestEnv(t)
	seedEnv.seedGoldenPair()
	aggregator := seedEnv.aggregator()
	var aggWg sync.WaitGroup
	aggErrs := make(chan error, 4)
	processedCh := make(chan int, 4)
	for worker := 0; worker < 4; worker++ {
		aggWg.Add(1)
		go func() {
			defer aggWg.Done()
			processed, err := aggregator.AggregateUsageStatsBatch(context.Background(), AggregateOptions{BatchSize: 10, SafeCreatedBefore: "2026-04-18T11:00:00.000Z"})
			aggErrs <- err
			processedCh <- processed
		}()
	}
	aggWg.Wait()
	close(aggErrs)
	for err := range aggErrs {
		if err != nil {
			t.Fatalf("concurrent aggregate error: %v", err)
		}
	}
	totalProcessed := 0
	for index := 0; index < 4; index++ {
		totalProcessed += <-processedCh
	}
	if totalProcessed != 2 {
		t.Fatalf("concurrent aggregate processed total = %d want 2 (游标保证恰好一次)", totalProcessed)
	}
	seedEnv.assertFloats("concurrent totals exactly once",
		seedEnv.queryRowFloats(`SELECT COALESCE(SUM(request_count), 0) FROM usage_stats_totals WHERE system_account_id='global' AND scope_type='system_account'`),
		2)
}
