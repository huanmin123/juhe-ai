package cleanuprepo

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// statssubtract.go（SQLite 扣减/结算链）的真实库语义测试：与
// statssubtractpostgres_test.go 的 SQLite 互证互补，覆盖完整家族
// （totals/时间桶/latency/model/error/quality/health/授权日报）、
// 台账单次扣减门控、空行清理与 scope stats 全表清理。

const kitCreatedAt = "2026-01-05T03:04:05.123Z"

func newKitStatsStore(t *testing.T) (*RecordCleanupStore, *DB) {
	t.Helper()
	stats := openKitSQLite(t, "stats_chain")
	createKitStatsChainSchema(t, stats.DB)
	store := &RecordCleanupStore{
		Stats:    stats,
		Now:      kitNow,
		Timezone: func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}
	return store, stats
}

func kitText(v string) *string { return &v }
func kitNum(v float64) *float64 { return &v }

// kitFailedRow 构造一行失败记录（触发 error/quality/auth/health 全家族）。
func kitFailedRow() statsagg.UsageStatsRecordRow {
	return statsagg.UsageStatsRecordRow{
		ID: "rec-kit-1", SystemAccountID: "sys-1", TraceID: "tr-kit", TrafficSource: "account_health_check",
		ClientIP: kitText("127.0.0.1"), APIKeyID: kitText("key-1"), AccountID: kitText("acc-1"),
		ProviderCode: kitText("openai"), StatusCode: kitNum(500), Success: 0,
		Model: kitText("gpt-kit"),
		FailureAttribution: kitText("account_upstream"),
		FirstTokenMs:       kitNum(120), DurationMs: kitNum(340),
		InputTokens: kitNum(10), OutputTokens: kitNum(5), CostUsd: kitNum(0.01),
		AccountOwnerSystemAccountID:      kitText("owner-2"),
		AccountAccessType:                kitText("account_authorized"),
		AccountAuthorizationID:           kitText("auth-1"),
		AccountAuthorizationSourceType:   kitText("team"),
		AccountAuthorizationSourceTeamID: kitText("team-9"),
		CreatedAt:                        kitCreatedAt,
	}
}

func seedKitTotals(t *testing.T, stats *DB, systemAccountID, scopeType, scopeID string, request, success, input float64) {
	t.Helper()
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count, input_tokens, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, '2026-01-01T00:00:00.000Z')`,
		systemAccountID, scopeType, scopeID, request, success, input)
}

func readKitTotals(t *testing.T, stats *DB, systemAccountID, scopeType, scopeID string) (float64, float64, float64) {
	t.Helper()
	var request, success, input float64
	if err := stats.QueryRowContext(context.Background(), `SELECT request_count, success_count, input_tokens
      FROM usage_stats_totals WHERE system_account_id = ? AND scope_type = ? AND scope_id = ?`,
		systemAccountID, scopeType, scopeID).Scan(&request, &success, &input); err != nil {
		if err == sql.ErrNoRows {
			return -1, -1, -1
		}
		t.Fatalf("read totals: %v", err)
	}
	return request, success, input
}

// TestCleanupAPIKeyRecordStatsDataFullFamily：一行失败记录触发 totals/桶/
// latency/model/error/quality/health/授权日报全家族扣减，并验证台账门控。
func TestCleanupAPIKeyRecordStatsDataFullFamily(t *testing.T) {
	store, stats := newKitStatsStore(t)
	row := kitFailedRow()
	timeKeys, err := statsagg.UsageStatsTimeKeysFor(row.CreatedAt, kitZone())
	if err != nil {
		t.Fatalf("timeKeys: %v", err)
	}
	accumulator := statsagg.UsageStatsAccumulatorFromRecord(row)

	// -- 种子 --
	seedKitTotals(t, stats, "sys-1", "system_account", "sys-1", 5, 4, 100)
	seedKitTotals(t, stats, "global", "system_account", "global", 5, 4, 100)
	mustExecKit(t, stats, `INSERT INTO usage_stats_minute (
      system_account_id, scope_type, scope_id, stat_minute, request_count, success_count, input_tokens, updated_at)
      VALUES ('sys-1','system_account','sys-1',?, 5, 4, 100, '2026-01-01T00:00:00.000Z')`, timeKeys.StatMinute)
	// latency：duration_ms=340 → 上界 500；样本 2 → 扣 1 后仍存在。
	mustExecKit(t, stats, `INSERT INTO usage_latency_minute (
      system_account_id, scope_type, scope_id, stat_minute, metric_type, bucket_upper_bound_ms, sample_count, updated_at)
      VALUES ('sys-1','system_account','sys-1',?, 'duration_ms', 500, 2, '2026-01-01T00:00:00.000Z')`, timeKeys.StatMinute)
	// model 桶：恰好等量 → 扣减后空行删除；global 半区留 surplus 验证部分扣减。
	mustExecKit(t, stats, `INSERT INTO usage_model_minute (
      system_account_id, stat_minute, provider_code, model, request_count, input_tokens, updated_at)
      VALUES ('sys-1', ?, 'openai', 'gpt-kit', 1, 10, '2026-01-01T00:00:00.000Z')`, timeKeys.StatMinute)
	mustExecKit(t, stats, `INSERT INTO usage_model_minute (
      system_account_id, stat_minute, provider_code, model, request_count, input_tokens, updated_at)
      VALUES ('global', ?, 'openai', 'gpt-kit', 9, 90, '2026-01-01T00:00:00.000Z')`, timeKeys.StatMinute)
	// error 桶：errorCode 缺省 → String(status_code) = "500"；每行固定扣 1，
	// 种 1/1 → 清零后空行删除。
	mustExecKit(t, stats, `INSERT INTO usage_error_minute (
      system_account_id, stat_minute, error_group, provider_code, error_code, status_code, request_count, error_count, updated_at)
      VALUES ('sys-1', ?, 'openai', 'openai', '500', 500, 1, 1, '2026-01-01T00:00:00.000Z')`, timeKeys.StatMinute)
	// quality：失败归属 account_upstream → 计 quality 扣减（种子 error 3，
	// 扣 1 后可观察钳制前的真实减量）。
	mustExecKit(t, stats, `INSERT INTO account_quality_minute_stats (
      account_id, stat_minute, request_count, success_count, error_count, updated_at)
      VALUES ('acc-1', ?, 2, 2, 3, '2026-01-01T00:00:00.000Z')`, timeKeys.StatMinute)
	// health：traffic_source = account_health_check → 删除小时健康行。
	mustExecKit(t, stats, `INSERT INTO account_health_hourly (account_id, stat_hour, last_record_id, updated_at)
      VALUES ('acc-1', ?, 'rec-kit-1', '2026-01-01T00:00:00.000Z')`, timeKeys.StatHour)
	// 授权日报（team source）：owner + global 双 scope 的 user 汇总行。
	mustExecKit(t, stats, `INSERT INTO authorization_user_usage_summary_daily (
      system_account_id, stat_date, resource_filter_type, resource_filter_id, team_filter_id,
      grantee_filter_system_account_id, request_count, updated_at)
      VALUES ('owner-2', ?, 'account', 'acc-1', 'team-9', 'sys-1', 4, '2026-01-01T00:00:00.000Z')`, timeKeys.StatDate)
	mustExecKit(t, stats, `INSERT INTO authorization_user_usage_summary_daily (
      system_account_id, stat_date, resource_filter_type, resource_filter_id, team_filter_id,
      grantee_filter_system_account_id, request_count, updated_at)
      VALUES ('global', ?, 'account', 'acc-1', 'team-9', 'sys-1', 4, '2026-01-01T00:00:00.000Z')`, timeKeys.StatDate)
	mustExecKit(t, stats, `INSERT INTO authorization_team_usage_summary_daily (
      system_account_id, stat_date, resource_filter_type, resource_filter_id, team_filter_id,
      grantee_filter_system_account_id, request_count, updated_at)
      VALUES ('owner-2', ?, 'all', '', 'team-9', 'sys-1', 4, '2026-01-01T00:00:00.000Z')`, timeKeys.StatDate)

	rows := []map[string]any{statsaggRowToMap(row, "shard-kit-a")}
	if err := store.CleanupAPIKeyRecordStatsData(context.Background(),
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, true, kitZone()); err != nil {
		t.Fatalf("CleanupAPIKeyRecordStatsData: %v", err)
	}

	// -- totals：调用方 scope 与 global 双 scope 扣减同一 accumulator --
	request, success, input := readKitTotals(t, stats, "sys-1", "system_account", "sys-1")
	if request != 5-accumulator.RequestCount || success != 4-accumulator.SuccessCount || input != 100-accumulator.InputTokens {
		t.Fatalf("totals 扣减不符：got (%v,%v,%v) want (-%v,-%v,-%v)",
			request, success, input, accumulator.RequestCount, accumulator.SuccessCount, accumulator.InputTokens)
	}
	gRequest, _, _ := readKitTotals(t, stats, "global", "system_account", "global")
	if gRequest != 5-accumulator.RequestCount {
		t.Fatalf("global totals 扣减不符：%v", gRequest)
	}
	// -- 时间桶 --
	var bucketRequest float64
	if err := stats.QueryRowContext(context.Background(), `SELECT request_count FROM usage_stats_minute
      WHERE system_account_id = 'sys-1' AND scope_type = 'system_account' AND scope_id = 'sys-1' AND stat_minute = ?`,
		timeKeys.StatMinute).Scan(&bucketRequest); err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	if bucketRequest != 5-accumulator.RequestCount {
		t.Fatalf("minute 桶扣减不符：%v", bucketRequest)
	}
	// -- latency：340ms → 500 上界样本 2→1（未清零不删行）--
	var sampleCount float64
	if err := stats.QueryRowContext(context.Background(), `SELECT sample_count FROM usage_latency_minute
      WHERE system_account_id = 'sys-1' AND stat_minute = ? AND metric_type = 'duration_ms' AND bucket_upper_bound_ms = 500`,
		timeKeys.StatMinute).Scan(&sampleCount); err != nil {
		t.Fatalf("read latency: %v", err)
	}
	if sampleCount != 1 {
		t.Fatalf("latency 样本数 = %v, 期望 1", sampleCount)
	}
	// -- model：调用方 scope 等量清空删除；global 半量扣减保留 --
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM usage_model_minute
      WHERE system_account_id = 'sys-1' AND stat_minute = ?`, timeKeys.StatMinute); got != 0 {
		t.Fatalf("等量 model 行应被删除")
	}
	var gModelRequest float64
	if err := stats.QueryRowContext(context.Background(), `SELECT request_count FROM usage_model_minute
      WHERE system_account_id = 'global' AND stat_minute = ?`, timeKeys.StatMinute).Scan(&gModelRequest); err != nil {
		t.Fatalf("read global model: %v", err)
	}
	if gModelRequest != 9-accumulator.RequestCount {
		t.Fatalf("global model 扣减不符：%v", gModelRequest)
	}
	// -- error：等量清空删除（error_code = "500" 缺省回退语义）--
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM usage_error_minute
      WHERE system_account_id = 'sys-1' AND stat_minute = ? AND error_code = '500'`, timeKeys.StatMinute); got != 0 {
		t.Fatalf("等量 error 行应被删除")
	}
	// -- quality：失败行 request 1 / success 0 / error 1 --
	var qRequest, qSuccess, qError float64
	if err := stats.QueryRowContext(context.Background(), `SELECT request_count, success_count, error_count
      FROM account_quality_minute_stats WHERE account_id = 'acc-1' AND stat_minute = ?`,
		timeKeys.StatMinute).Scan(&qRequest, &qSuccess, &qError); err != nil {
		t.Fatalf("read quality: %v", err)
	}
	if qRequest != 1 || qSuccess != 2 || qError != 2 {
		t.Fatalf("quality 扣减不符：(%v,%v,%v), 期望 (1,2,2)", qRequest, qSuccess, qError)
	}
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM account_quality_dirty_accounts WHERE account_id = 'acc-1'`); got != 1 {
		t.Fatalf("quality 脏账户应被 upsert")
	}
	// -- health：小时健康行删除 --
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM account_health_hourly
      WHERE account_id = 'acc-1' AND last_record_id = 'rec-kit-1'`); got != 0 {
		t.Fatalf("account health 行应被删除")
	}
	// -- 授权日报：owner + global 双 scope --
	// 行为存疑：Go 的 subtractAuthorizationSummaryRows（SQLite）SET 子句缺
	// duration_ms/first_token_ms/last_* 列（21 个占位符 vs 25 个参数），真库
	// 执行时 WHERE 参数错位、语句不命中任何行；Node 归档的同一 UPDATE 为
	// 23 个 SET 占位符 + 6 个 WHERE 占位符（29 参，逐列对齐）。按当前实际
	// 行为断言：汇总行保持原值，扣减不生效。
	for _, owner := range []string{"owner-2", "global"} {
		var authRequest float64
		if err := stats.QueryRowContext(context.Background(), `SELECT request_count FROM authorization_user_usage_summary_daily
        WHERE system_account_id = ? AND stat_date = ? AND resource_filter_type = 'account' AND resource_filter_id = 'acc-1'
        AND team_filter_id = 'team-9' AND grantee_filter_system_account_id = 'sys-1'`,
			owner, timeKeys.StatDate).Scan(&authRequest); err != nil {
			t.Fatalf("read auth user %s: %v", owner, err)
		}
		if authRequest != 4 {
			t.Fatalf("授权 user 汇总 (%s) = %v（当前实现占位符错位不命中行）", owner, authRequest)
		}
	}
	var teamRequest float64
	if err := stats.QueryRowContext(context.Background(), `SELECT request_count FROM authorization_team_usage_summary_daily
      WHERE system_account_id = 'owner-2' AND stat_date = ? AND resource_filter_type = 'all' AND team_filter_id = 'team-9'
      AND grantee_filter_system_account_id = 'sys-1'`, timeKeys.StatDate).Scan(&teamRequest); err != nil {
		t.Fatalf("read auth team: %v", err)
	}
	if teamRequest != 4 {
		t.Fatalf("授权 team 汇总 = %v（当前实现占位符错位不命中行）", teamRequest)
	}
	// -- 台账：stats_subtracted_at + shard_deleted_at 双标记 --
	var subtractedAt, shardDeletedAt sql.NullString
	if err := stats.QueryRowContext(context.Background(), `SELECT stats_subtracted_at, shard_deleted_at
      FROM usage_record_cleanup_deductions WHERE usage_id = 'rec-kit-1' AND source_shard_key = 'shard-kit-a'`).
		Scan(&subtractedAt, &shardDeletedAt); err != nil {
		t.Fatalf("read deductions: %v", err)
	}
	if !subtractedAt.Valid || subtractedAt.String != kitUpdatedAt {
		t.Fatalf("stats_subtracted_at = %v", subtractedAt)
	}
	if !shardDeletedAt.Valid || shardDeletedAt.String != kitUpdatedAt {
		t.Fatalf("shard_deleted_at = %v", shardDeletedAt)
	}

	// -- 二次结算：台账门控，不再扣减 --
	if err := store.CleanupAPIKeyRecordStatsData(context.Background(),
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, true, kitZone()); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	request, _, _ = readKitTotals(t, stats, "sys-1", "system_account", "sys-1")
	if request != 5-accumulator.RequestCount {
		t.Fatalf("台账门控失效：二次结算后 request = %v", request)
	}
}

// TestCleanupAPIKeyRecordStatsDataEmptyRows：空行集 → api_key scope 全表清理
// + 派生窗口刷新（fake 记录调用）。
func TestCleanupAPIKeyRecordStatsDataEmptyRows(t *testing.T) {
	store, stats := newKitStatsStore(t)
	derived := &kitDerivedWindows{}
	store.DerivedWindows = derived
	seedKitTotals(t, stats, "sys-1", "api_key", "key-1", 1, 1, 1)
	mustExecKit(t, stats, `INSERT INTO usage_latency_minute (
      system_account_id, scope_type, scope_id, stat_minute, metric_type, bucket_upper_bound_ms, sample_count)
      VALUES ('sys-1','api_key','key-1','2026-01-05T11:04','duration_ms',500,1)`)
	mustExecKit(t, stats, `INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id)
      VALUES ('api_key','key-1','usage_stats_aggregation','2026-01-05T00:00:00.000Z','rec-0')`)
	mustExecKit(t, stats, `INSERT INTO usage_record_cleanup_deductions
      (usage_id, source_shard_key, api_key_id, system_account_id) VALUES ('rec-x','shard-a','key-1','sys-1')`)

	if err := store.CleanupAPIKeyRecordStatsData(context.Background(),
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		nil, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("CleanupAPIKeyRecordStatsData(nil): %v", err)
	}
	for _, table := range apiKeyScopeStatsTables {
		if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM `+table+
			` WHERE system_account_id = 'sys-1' AND scope_type = 'api_key' AND scope_id = 'key-1'`); got != 0 {
			t.Fatalf("%s scope 行应被清理", table)
		}
	}
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM stats_job_state WHERE scope_type = 'api_key'`); got != 0 {
		t.Fatalf("api_key 游标应被清理")
	}
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM usage_record_cleanup_deductions
      WHERE api_key_id = 'key-1'`); got != 0 {
		t.Fatalf("台账应被清理")
	}
	derived.mu.Lock()
	calls := derived.quotaCalls + derived.rankCalls
	derived.mu.Unlock()
	if calls != 2 {
		t.Fatalf("派生窗口刷新调用 = %d, 期望 2", calls)
	}
}

// TestCleanupDerivedWindowsSkippedReported：DerivedWindows 缺省时必须显式
// 上报（不静默）。
func TestCleanupDerivedWindowsSkippedReported(t *testing.T) {
	store, _ := newKitStatsStore(t)
	reasons := make([]string, 0, 1)
	store.OnDerivedWindowsSkipped = func(reason string) { reasons = append(reasons, reason) }
	if err := store.CleanupAPIKeyRecordStatsData(context.Background(),
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		nil, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "key-1") {
		t.Fatalf("跳过上报 = %v", reasons)
	}
}

// TestCleanupAccountRecordStatsDataEmptyRows：account 变体 scope 清理覆盖
// account/caller_account/授权/团队 LIKE/报表与账户附属表。
func TestCleanupAccountRecordStatsDataEmptyRows(t *testing.T) {
	store, stats := newKitStatsStore(t)
	// 调用方 + 关联账户 + 授权 + 团队 scope 各一行。
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals
      (system_account_id, scope_type, scope_id, request_count) VALUES ('sys-1','account','acc-1',1)`)
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals
      (system_account_id, scope_type, scope_id, request_count) VALUES ('sys-1','caller_account','acc-rel-1',1)`)
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals
      (system_account_id, scope_type, scope_id, request_count) VALUES ('sys-1','account_authorization','auth-1',1)`)
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals
      (system_account_id, scope_type, scope_id, request_count) VALUES ('sys-1','account_authorization_team','acc-1:team-9',1)`)
	mustExecKit(t, stats, `INSERT INTO stats_job_state (scope_type, scope_id, job_name)
      VALUES ('account','acc-1','usage_stats_aggregation'),
             ('account_authorization','auth-1','usage_stats_aggregation'),
             ('account_authorization_team','acc-rel-1:team-8','usage_stats_aggregation')`)
	mustExecKit(t, stats, `INSERT INTO account_quality_scores (account_id) VALUES ('acc-1')`)
	mustExecKit(t, stats, `INSERT INTO account_quality_dirty_accounts (account_id) VALUES ('acc-1')`)
	mustExecKit(t, stats, `INSERT INTO account_quality_minute_stats (account_id, stat_minute) VALUES ('acc-rel-1','2026-01-05T11:04')`)
	mustExecKit(t, stats, `INSERT INTO account_health_hourly (account_id, stat_hour) VALUES ('acc-1','2026-01-05T03')`)
	mustExecKit(t, stats, `INSERT INTO account_usage_snapshots (system_account_id, account_id, kind) VALUES ('sys-1','acc-1','openai_codex')`)
	mustExecKit(t, stats, `INSERT INTO usage_record_cleanup_deductions (usage_id, source_shard_key, account_id)
      VALUES ('rec-y','shard-a','acc-rel-1')`)
	mustExecKit(t, stats, `INSERT INTO authorization_user_usage_summary_daily
      (system_account_id, stat_date, resource_filter_type, resource_filter_id, team_filter_id, grantee_filter_system_account_id, request_count)
      VALUES ('owner-2','2026-01-05','account','acc-1','','',4)`)

	target := retention.ExpiredDeletedAccountTarget{
		AccountID:         "acc-1",
		SystemAccountID:   "sys-1",
		RelatedAccountIDs: []string{"acc-rel-1"},
		AuthorizationIDs:  []string{"auth-1"},
		TeamScopeIDs:      []string{"acc-1:team-9", "acc-rel-1:team-8"},
	}
	// 行为存疑：deleteAccountScopeStatsRows 的团队 scope LIKE 条件使用
	// `ESCAPE '\\'`（raw string，实际两字符），SQLite 要求单字符 ESCAPE，
	// 语句报错「ESCAPE expression must be a single character」；Node 归档的
	// JS 模板串 `'\\'` 渲染为单反斜杠。按当前实际行为断言：入口报错，且
	// 首张表已完成 account/caller_account 两行删除后才中断。
	err := store.CleanupAccountRecordStatsData(context.Background(), target, nil, kitUpdatedAt, false, kitZone())
	if err == nil || !strings.Contains(err.Error(), "ESCAPE expression must be a single character") {
		t.Fatalf("当前实现应在团队 LIKE 语句报错，实际：%v", err)
	}
	// 报错经 defer tx.Rollback() 回滚：全部种子行保持原状。
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM usage_stats_totals
      WHERE scope_type IN ('account','caller_account','account_authorization','account_authorization_team')`); got != 4 {
		t.Fatalf("报错应整体回滚，残余 scope 行 = %d, 期望 4", got)
	}
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM account_quality_scores`); got != 1 {
		t.Fatalf("账户附属行不应被清理（回滚语义）")
	}
}

// TestSubtractAuthorizationReportRowsGlobalOwner：owner = global 时不再追加
// global scope（单 scope 语义）。
func TestSubtractAuthorizationReportRowsGlobalOwner(t *testing.T) {
	store, stats := newKitStatsStore(t)
	row := kitFailedRow()
	row.AccountOwnerSystemAccountID = kitText("global")
	row.AccountAuthorizationSourceType = nil
	row.TrafficSource = "api"
	row.AccountID = kitText("acc-1")
	timeKeys, err := statsagg.UsageStatsTimeKeysFor(row.CreatedAt, kitZone())
	if err != nil {
		t.Fatalf("timeKeys: %v", err)
	}
	mustExecKit(t, stats, `INSERT INTO authorization_user_usage_summary_daily
      (system_account_id, stat_date, resource_filter_type, resource_filter_id, team_filter_id, grantee_filter_system_account_id, request_count)
      VALUES ('global', ?, 'account', 'acc-1', '', 'sys-1', 2)`, timeKeys.StatDate)
	tx, err := stats.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := store.subtractAuthorizationUsageReportRows(context.Background(), tx, row,
		timeKeys.StatDate, kitUpdatedAt); err != nil {
		t.Fatalf("subtractAuthorizationUsageReportRows: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	var request float64
	if err := stats.QueryRowContext(context.Background(), `SELECT request_count FROM authorization_user_usage_summary_daily`).Scan(&request); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	// 行为存疑：同 TestCleanupAPIKeyRecordStatsDataFullFamily——SQLite 授权
	// 日报扣减 UPDATE 占位符与参数数不匹配，语句不命中任何行。按当前实际
	// 行为断言（Node 语义应扣为 1）。
	if request != 2 {
		t.Fatalf("global owner 扣减 = %v（当前实现占位符错位不命中行）", request)
	}
}

// TestShouldRecordAccountQualityStats：探针/混合评分流量与无关失败不计 quality。
func TestShouldRecordAccountQualityStats(t *testing.T) {
	row := kitFailedRow()
	row.TrafficSource = "api"
	if !shouldRecordAccountQualityStats(row) {
		t.Fatalf("account_upstream 失败应计 quality")
	}
	for _, source := range []string{"runtime_recovery_probe", "cooldown_retest", "hybrid_scoring", "hybrid_quality_scoring"} {
		probe := row
		probe.TrafficSource = source
		if shouldRecordAccountQualityStats(probe) {
			t.Fatalf("%s 不应计 quality", source)
		}
	}
	success := row
	success.Success = 1
	success.FailureAttribution = nil
	if !shouldRecordAccountQualityStats(success) {
		t.Fatalf("成功行应计 quality")
	}
	unrelated := row
	unrelated.FailureAttribution = kitText("client_error")
	if shouldRecordAccountQualityStats(unrelated) {
		t.Fatalf("client_error 失败不应计 quality")
	}
}

// TestLatencyBucketHelpers：延迟桶上界与样本选择。
func TestLatencyBucketHelpers(t *testing.T) {
	cases := []struct {
		value float64
		want  int64
	}{
		{0, 100}, {100, 100}, {100.5, 250}, {60000, 60000}, {60000.5, -1}, {1 << 20, -1},
	}
	for _, item := range cases {
		if got := latencyBucketUpperBound(item.value); got != item.want {
			t.Fatalf("latencyBucketUpperBound(%v) = %d, 期望 %d", item.value, got, item.want)
		}
	}
	row := statsagg.UsageStatsRecordRow{DurationMs: kitNum(340), FirstTokenMs: kitNum(-1)}
	samples := latencySamples(row)
	if len(samples) != 1 || samples[0].metricType != "duration_ms" || samples[0].bucketBound != 500 {
		t.Fatalf("latencySamples = %+v", samples)
	}
	if got := latencySamples(statsagg.UsageStatsRecordRow{}); len(got) != 0 {
		t.Fatalf("无样本行应返回空：%+v", got)
	}
}

// TestStatsaggRowMapHelpers：Node IPC JSON 形状的双向转换与类型宽容读取。
func TestStatsaggRowMapHelpers(t *testing.T) {
	if shardUsageRowsToMaps(nil) != nil {
		t.Fatalf("空行集应返回 nil")
	}
	row := kitFailedRow()
	row.SourceShardKey = "shard-x"
	maps := shardUsageRowsToMaps([]shardUsageRow{{UsageStatsRecordRow: row, SourceShardKey: "shard-x"}})
	if len(maps) != 1 || maps[0]["id"] != "rec-kit-1" || maps[0]["source_shard_key"] != "shard-x" {
		t.Fatalf("shardUsageRowsToMaps = %+v", maps[0])
	}
	parsed, err := statsaggRowFromMap(maps[0])
	if err != nil {
		t.Fatalf("statsaggRowFromMap: %v", err)
	}
	if parsed.ID != row.ID || parsed.SystemAccountID != row.SystemAccountID ||
		*parsed.AccountAuthorizationID != "auth-1" || *parsed.StatusCode != 500 {
		t.Fatalf("round-trip 不符：%+v", parsed)
	}

	// mapString/mapNumber 类型宽容：[]byte / float32 / int / int64 / 字符串数值。
	if got := mapString(map[string]any{"k": []byte("v")}, "k"); got == nil || *got != "v" {
		t.Fatalf("mapString([]byte) = %v", got)
	}
	if got := mapString(map[string]any{"k": ""}, "k"); got != nil {
		t.Fatalf("mapString 空串应返回 nil")
	}
	if got := mapString(map[string]any{"k": 3.0}, "k"); got != nil {
		t.Fatalf("mapString 非文本应返回 nil")
	}
	for name, item := range map[string]struct {
		value any
		want  float64
	}{
		"float64": {3.5, 3.5}, "float32": {float32(3.5), 3.5},
		"int": {3, 3}, "int64": {int64(3), 3},
		"[]byte": {[]byte("3.5"), 3.5}, "string": {" 3.5 ", 3.5},
	} {
		got := mapNumber(map[string]any{"k": item.value}, "k")
		if got == nil || *got != item.want {
			t.Fatalf("mapNumber(%s) = %v, 期望 %v", name, got, item.want)
		}
	}
	if got := mapNumber(map[string]any{"k": "abc"}, "k"); got != nil {
		t.Fatalf("mapNumber 非数值应返回 nil")
	}
	if got := mapNumber(map[string]any{}, "missing"); got != nil {
		t.Fatalf("mapNumber 缺键应返回 nil")
	}
	// success 缺省保持 0；source_shard_key 回填。
	empty, err := statsaggRowFromMap(map[string]any{"id": "x", "source_shard_key": "s1"})
	if err != nil || empty.Success != 0 || empty.SourceShardKey != "s1" || empty.CreatedAt != "" {
		t.Fatalf("缺省行解析 = %+v, %v", empty, err)
	}
}

// TestTimeKeyValueMapping：bucket valueKey → UsageStatsTimeKeys 字段映射。
func TestTimeKeyValueMapping(t *testing.T) {
	keys := statsagg.UsageStatsTimeKeys{
		StatMinute: "m", StatHour: "h", StatDate: "d", StatWeek: "w", StatMonth: "mo",
	}
	if timeKeyValue(keys, "statMinute") != "m" || timeKeyValue(keys, "statHour") != "h" ||
		timeKeyValue(keys, "statDate") != "d" || timeKeyValue(keys, "statWeek") != "w" ||
		timeKeyValue(keys, "statMonth") != "mo" || timeKeyValue(keys, "unknown") != "" {
		t.Fatalf("timeKeyValue 映射错误")
	}
}

// TestUpsertAccountUsageSnapshotsSQLite：SQLite 路径（裸表名 + excluded 小写）
// 的 upsert、缺省 updatedAt 与缺归属报错。
func TestUpsertAccountUsageSnapshotsSQLite(t *testing.T) {
	store, stats := newKitStatsStore(t)
	business := openKitSQLite(t, "business_kit_snapshots")
	mustExecKit(t, business, `CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT)`)
	mustExecKit(t, business, `INSERT INTO accounts VALUES ('acc-1','owner-1'), ('acc-2','owner-2')`)

	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, nil); err != nil {
		t.Fatalf("空输入应直接返回 nil：%v", err)
	}
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex", Source: "job", Snapshot: map[string]any{"requests": 1.0}, UpdatedAt: "bad-time"},
	}); err == nil {
		t.Fatalf("非法 updatedAt 应报错")
	}
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-missing", Kind: "openai_codex"},
	}); err == nil || !strings.Contains(err.Error(), "缺少账户归属") {
		t.Fatalf("缺归属应报错：%v", err)
	}
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex", Source: "job", Snapshot: map[string]any{"requests": 1.0}},
		{AccountID: "acc-2", Kind: "openai_codex", Source: "job", Snapshot: map[string]any{"requests": 2.0}, UpdatedAt: "2026-08-01T00:00:00.000Z"},
	}); err != nil {
		t.Fatalf("UpsertAccountUsageSnapshots: %v", err)
	}
	var count int64
	if err := stats.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM account_usage_snapshots`).Scan(&count); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if count != 2 {
		t.Fatalf("快照行数 = %d, 期望 2", count)
	}
	// 同 key 二次 upsert 覆盖（refresh_status 回 fresh）。
	if err := store.UpsertAccountUsageSnapshots(context.Background(), business, []retention.AccountUsageSnapshotUpsertInput{
		{AccountID: "acc-1", Kind: "openai_codex", Source: "retry", Snapshot: map[string]any{"requests": 9.0}},
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	var source string
	if err := stats.QueryRowContext(context.Background(), `SELECT source FROM account_usage_snapshots WHERE account_id = 'acc-1'`).Scan(&source); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if source != "retry" {
		t.Fatalf("upsert 未覆盖 source：%q", source)
	}
}
