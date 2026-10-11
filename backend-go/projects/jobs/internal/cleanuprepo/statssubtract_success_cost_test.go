package cleanuprepo

import (
	"context"
	"fmt"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// success_cost_usd 回减与扣减补正测试：删除成功/失败混合记录后，total_cost_usd
// 按记录全量成本回减、success_cost_usd 只回减成功记录的成本（失败记录回减
// 0）；删除失败记录使 error_count 扣减后归零且两列不一致时，success_cost_usd
// 在同一扣减事务内补正为扣减后的 total_cost_usd（CASE 补正，计划
// PLAN-20261011T225418 §3.4）；混合行不补正；两列同时归零时行被清理。
func TestCleanupAPIKeyRecordStatsDataSuccessCostSubtract(t *testing.T) {
	store, stats := newKitStatsStore(t)
	ctx := context.Background()

	successRow := statsagg.UsageStatsRecordRow{
		ID: "rec-success", SystemAccountID: "sys-1", TraceID: "tr-1", TrafficSource: "api",
		APIKeyID: kitText("key-1"), Model: kitText("gpt-kit"), Success: 1,
		CostUsd: kitNum(0.6), CreatedAt: kitCreatedAt,
	}
	failedRow := statsagg.UsageStatsRecordRow{
		ID: "rec-failed", SystemAccountID: "sys-1", TraceID: "tr-2", TrafficSource: "api",
		APIKeyID: kitText("key-1"), Model: kitText("gpt-kit"), Success: 0,
		StatusCode: kitNum(500), CostUsd: kitNum(0.4), CreatedAt: kitCreatedAt,
	}
	// 初始化 total = 5.0 / success_cost = 3.0：
	// 成功行回减 (total 0.6, success 0.6)，失败行回减 (total 0.4, success 0)。
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals (
		system_account_id, scope_type, scope_id, request_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('sys-1', 'api_key', 'key-1', 10, 5.0, 3.0, '2026-01-01T00:00:00.000Z')`)

	rows := []map[string]any{
		statsaggRowToMap(successRow, "shard-sc"),
		statsaggRowToMap(failedRow, "shard-sc"),
	}
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("CleanupAPIKeyRecordStatsData: %v", err)
	}

	var totalCost, successCost float64
	if err := stats.QueryRowContext(ctx, `SELECT total_cost_usd, success_cost_usd FROM usage_stats_totals
		WHERE system_account_id = 'sys-1' AND scope_type = 'api_key' AND scope_id = 'key-1'`).
		Scan(&totalCost, &successCost); err != nil {
		t.Fatalf("read totals: %v", err)
	}
	if totalCost != 4.0 {
		t.Fatalf("total_cost_usd = %v, want 4.0（成功 0.6 + 失败 0.4 全量回减）", totalCost)
	}
	// CASE 补正：删失败记录使 error_count 扣减后归零且 total≠success，
	// success_cost_usd 补正为扣减后的 total_cost_usd——删失败记录后该行成为
	// 全成功行，success=total（这正是治理目标，不是测试放松）。
	if successCost != 4.0 {
		t.Fatalf("success_cost_usd = %v, want 4.0（CASE 补正：删失败记录后全成功行 success=total）", successCost)
	}

	// 二次结算：台账门控，不重复回减。
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
	if err := stats.QueryRowContext(ctx, `SELECT total_cost_usd, success_cost_usd FROM usage_stats_totals
		WHERE system_account_id = 'sys-1' AND scope_type = 'api_key' AND scope_id = 'key-1'`).
		Scan(&totalCost, &successCost); err != nil {
		t.Fatalf("re-read totals: %v", err)
	}
	if totalCost != 4.0 || successCost != 4.0 {
		t.Fatalf("台账门控失效：(%v,%v), 期望 (4.0,4.0)", totalCost, successCost)
	}
}

// TestCleanupAPIKeyRecordStatsDataSuccessCostZeroRowCleanup：全部行等量扣减后
// 两列同时归零，空行清理条件含 success_cost_usd = 0。
func TestCleanupAPIKeyRecordStatsDataSuccessCostZeroRowCleanup(t *testing.T) {
	store, stats := newKitStatsStore(t)
	ctx := context.Background()

	row := statsagg.UsageStatsRecordRow{
		ID: "rec-only", SystemAccountID: "sys-1", TraceID: "tr-1", TrafficSource: "api",
		APIKeyID: kitText("key-1"), Model: kitText("gpt-kit"), Success: 1,
		CostUsd: kitNum(1.5), CreatedAt: kitCreatedAt,
	}
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals (
		system_account_id, scope_type, scope_id, request_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('sys-1', 'api_key', 'key-1', 1, 1.5, 1.5, '2026-01-01T00:00:00.000Z')`)

	rows := []map[string]any{statsaggRowToMap(row, "shard-sc")}
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("CleanupAPIKeyRecordStatsData: %v", err)
	}
	if got := mustQueryCountKit(t, stats, `SELECT COUNT(*) FROM usage_stats_totals
		WHERE system_account_id = 'sys-1' AND scope_type = 'api_key' AND scope_id = 'key-1'`); got != 0 {
		t.Fatalf("两列归零的空行应被清理，剩余 %d 行", got)
	}
}

// readKitScopeCostTuple 读 api_key scope 统计行的 (request, error, total,
// success) 四元组；timeColumn 非空时追加对应时间桶谓词。行不存在即 fail
// （用于断言补正后的非零行不被全零 DELETE 误删）。
func readKitScopeCostTuple(t *testing.T, stats *DB, table, timeColumn string, timeValue any) [4]float64 {
	t.Helper()
	query := fmt.Sprintf(`SELECT request_count, error_count, total_cost_usd, success_cost_usd FROM %s
		WHERE system_account_id = 'sys-1' AND scope_type = 'api_key' AND scope_id = 'key-1'`, table)
	args := []any{}
	if timeColumn != "" {
		query += " AND " + timeColumn + " = ?"
		args = append(args, timeValue)
	}
	rows, err := stats.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("read %s tuple: %v", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("%s 元组行不存在（被误删或未命中）", table)
	}
	var request, errors, total, success float64
	if err := rows.Scan(&request, &errors, &total, &success); err != nil {
		t.Fatalf("scan %s tuple: %v", table, err)
	}
	return [4]float64{request, errors, total, success}
}

// TestCleanupAPIKeyRecordStatsDataSuccessCostCaseCorrection：计划 §5 元组
// 夹具（SQLite 真库，totals 与时间桶同一表达式各跑一遍）——预置
// (error 1, total 10, success 0) 删失败记录 → CASE 补正为 (0, 8, 8)；补正后
// 行非零不被全零 DELETE 误删；再删一条失败记录保持一致 (0, 7, 7)；重试
// （台账门控重复结算）幂等。
func TestCleanupAPIKeyRecordStatsDataSuccessCostCaseCorrection(t *testing.T) {
	store, stats := newKitStatsStore(t)
	ctx := context.Background()
	timeKeys, err := statsagg.UsageStatsTimeKeysFor(kitCreatedAt, kitZone())
	if err != nil {
		t.Fatalf("timeKeys: %v", err)
	}

	failed := statsagg.UsageStatsRecordRow{
		ID: "rec-case-failed", SystemAccountID: "sys-1", TraceID: "tr-case-1", TrafficSource: "api",
		APIKeyID: kitText("key-1"), Success: 0, StatusCode: kitNum(500),
		CostUsd: kitNum(2.0), CreatedAt: kitCreatedAt,
	}
	// 元组 (error 1, total 10, success 0)：历史混合错口径行。
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals (
		system_account_id, scope_type, scope_id, request_count, error_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('sys-1', 'api_key', 'key-1', 2, 1, 10.0, 0.0, '2026-01-01T00:00:00.000Z')`)
	mustExecKit(t, stats, `INSERT INTO usage_stats_daily (
		system_account_id, scope_type, scope_id, stat_date, request_count, error_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('sys-1', 'api_key', 'key-1', ?, 2, 1, 10.0, 0.0, '2026-01-01T00:00:00.000Z')`, timeKeys.StatDate)

	rows := []map[string]any{statsaggRowToMap(failed, "shard-case")}
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("CleanupAPIKeyRecordStatsData: %v", err)
	}

	// 删失败记录（total -2.0, success -0, error -1）：error 扣减后归零且
	// 8 ≠ 0 → CASE 补正 success 为扣减后 total：(0, 8, 8)。
	// 行能被读出即未被全零 DELETE 误删（补正后 total=8 非零）。
	totalsTuple := readKitScopeCostTuple(t, stats, "usage_stats_totals", "", nil)
	if totalsTuple != [4]float64{1, 0, 8, 8} {
		t.Fatalf("totals 元组 = %v, 期望 [1 0 8 8]（CASE 补正 (0,8,8)）", totalsTuple)
	}
	dailyTuple := readKitScopeCostTuple(t, stats, "usage_stats_daily", "stat_date", timeKeys.StatDate)
	if dailyTuple != [4]float64{1, 0, 8, 8} {
		t.Fatalf("daily 元组 = %v, 期望 [1 0 8 8]（时间桶同表达式补正）", dailyTuple)
	}

	// 再删一条失败记录（cost 1.0）：error 保持 0、7 = 7 → 走 ELSE 分支，
	// 补正语义自洽收敛：(0, 7, 7)。
	failed2 := failed
	failed2.ID = "rec-case-failed-2"
	failed2.TraceID = "tr-case-2"
	failed2.CostUsd = kitNum(1.0)
	rows2 := []map[string]any{statsaggRowToMap(failed2, "shard-case")}
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows2, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("second failed cleanup: %v", err)
	}
	totalsTuple = readKitScopeCostTuple(t, stats, "usage_stats_totals", "", nil)
	if totalsTuple != [4]float64{0, 0, 7, 7} {
		t.Fatalf("再次删失败记录后 totals 元组 = %v, 期望 [0 0 7 7]", totalsTuple)
	}

	// 重试幂等：重复结算全部已删行，台账门控不再扣减。
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		append(rows, rows2...), kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	totalsTuple = readKitScopeCostTuple(t, stats, "usage_stats_totals", "", nil)
	if totalsTuple != [4]float64{0, 0, 7, 7} {
		t.Fatalf("重试后 totals 元组 = %v, 期望 [0 0 7 7]（台账门控幂等）", totalsTuple)
	}
}

// TestCleanupAPIKeyRecordStatsDataSuccessCostMixedRowNoCorrection：计划 §5
// 混合行不补正——预置 (error 1, total 10, success 6) 删一条成功记录后
// error_count 扣减后仍 >0（仍混合），success_cost_usd 维持自身扣减结果、
// 不补正为 total_cost_usd。
func TestCleanupAPIKeyRecordStatsDataSuccessCostMixedRowNoCorrection(t *testing.T) {
	store, stats := newKitStatsStore(t)
	ctx := context.Background()

	success := statsagg.UsageStatsRecordRow{
		ID: "rec-mixed-success", SystemAccountID: "sys-1", TraceID: "tr-mixed", TrafficSource: "api",
		APIKeyID: kitText("key-1"), Success: 1,
		CostUsd: kitNum(2.0), CreatedAt: kitCreatedAt,
	}
	// 元组 (error 1, total 10, success 6)：混合行。
	mustExecKit(t, stats, `INSERT INTO usage_stats_totals (
		system_account_id, scope_type, scope_id, request_count, error_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('sys-1', 'api_key', 'key-1', 3, 1, 10.0, 6.0, '2026-01-01T00:00:00.000Z')`)

	rows := []map[string]any{statsaggRowToMap(success, "shard-mixed")}
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("CleanupAPIKeyRecordStatsData: %v", err)
	}

	// 删成功记录（total -2.0, success -2.0, error -0）：error 扣减后仍为 1
	// （混合行）→ 不补正，success 维持扣减结果 4.0：(2, 1, 8, 4)。
	tuple := readKitScopeCostTuple(t, stats, "usage_stats_totals", "", nil)
	if tuple != [4]float64{2, 1, 8, 4} {
		t.Fatalf("混合行元组 = %v, 期望 [2 1 8 4]（仍混合，不补正）", tuple)
	}

	// 重试幂等：台账门控，不重复扣减、不触发补正。
	if err := store.CleanupAPIKeyRecordStatsData(ctx,
		retention.APIKeyCleanupTarget{APIKeyID: "key-1", SystemAccountID: "sys-1"},
		rows, kitUpdatedAt, false, kitZone()); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	tuple = readKitScopeCostTuple(t, stats, "usage_stats_totals", "", nil)
	if tuple != [4]float64{2, 1, 8, 4} {
		t.Fatalf("重试后混合行元组 = %v, 期望 [2 1 8 4]", tuple)
	}
}
