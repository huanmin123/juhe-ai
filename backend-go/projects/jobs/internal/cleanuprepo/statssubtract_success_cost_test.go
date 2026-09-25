package cleanuprepo

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// success_cost_usd 回减口径测试：删除成功/失败混合记录后，total_cost_usd
// 按记录全量成本回减、success_cost_usd 只回减成功记录的成本（失败记录回减
// 0），新列不残留已删记录的成功成本；两列同时归零时行被清理。
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
	if successCost != 2.4 {
		t.Fatalf("success_cost_usd = %v, want 2.4（只回减成功行 0.6，失败行回减 0）", successCost)
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
	if totalCost != 4.0 || successCost != 2.4 {
		t.Fatalf("台账门控失效：(%v,%v), 期望 (4.0,2.4)", totalCost, successCost)
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
