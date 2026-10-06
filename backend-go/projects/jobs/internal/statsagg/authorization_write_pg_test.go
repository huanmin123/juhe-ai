// authorization_write_pg_test.go —— 授权日摘要写侧的 PG 方言验证（写入执行代理
// 交付物 2）。真实 upsertAuthorizationTeamUsageSummaryRow /
// upsertAuthorizationUserUsageSummaryRow 在真实 PG 上执行：
//   - 门控：环境变量 JUHE_AI_DUALMODE_PG_DSN 未设置时 t.Skip；设置了但连不上
//     或缺表时 t.Fatal 大声失败（共享 dev 库前置检查）。
//   - 零副作用：全部 upsert 在一个事务内执行，末尾 ROLLBACK；首次 upsert 断言
//     行存在与数值正确，同键二次 upsert 断言 ON CONFLICT 增量累加正确。
//   - owner 用带 awp+6hex- 前缀的假 ID，永不触碰共享真实键；聚合游标任务
//     （AggregateUsageStatsBatch）涉及共享 stats_job_state，禁止在共享 dev 库
//     跑全量游标（SQLite 臂已有既有测试覆盖）。
//   - DSN 永不进入日志、断言或错误信息。
package statsagg

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestAuthorizationWritePGDailyUpserts 验证授权 team/user 日摘要 upsert 的 PG
// 方言、冲突目标与增量合并语义。
func TestAuthorizationWritePGDailyUpserts(t *testing.T) {
	dsn := os.Getenv("JUHE_AI_DUALMODE_PG_DSN")
	if dsn == "" {
		t.Skip("JUHE_AI_DUALMODE_PG_DSN 未设置，跳过 PG 写侧验证")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("PG sql.Open 失败（DSN 不打印）: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("PG 连接失败（共享 dev 库不可达，DSN 不打印）: %v", err)
	}
	for _, table := range []string{
		"juhe_stats.authorization_team_usage_summary_daily",
		"juhe_stats.authorization_user_usage_summary_daily",
	} {
		var qualified sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)`, table).Scan(&qualified); err != nil {
			t.Fatalf("PG 前置检查失败：to_regclass(%s) 查询错误: %v", table, err)
		}
		if !qualified.Valid || qualified.String == "" {
			t.Fatalf("PG 前置检查失败：共享 dev 库缺少表 %s", table)
		}
	}

	prefix := func() string {
		buf := make([]byte, 3)
		if _, err := crand.Read(buf); err != nil {
			t.Fatal(err)
		}
		return "awp" + hex.EncodeToString(buf) + "-"
	}()
	ownerID := prefix + "owner"
	statDate := "2026-10-01"
	updatedAt := "2026-10-01T12:00:00.000Z"

	agg := &Aggregator{DB: db, Dialect: Dialect{Postgres: true}}
	teamKey := authorizationSummaryKey{
		teamFilterID:       prefix + "team1",
		resourceFilterType: "account",
		resourceFilterID:   prefix + "acc1",
	}
	userKey := authorizationSummaryKey{
		granteeFilterSystemAccountID: prefix + "grantee1",
		resourceFilterType:           "group",
		resourceFilterID:             prefix + "grp1",
	}
	first := UsageStatsAccumulator{
		RequestCount:     2,
		SuccessCount:     2,
		InputTokens:      100,
		OutputTokens:     50,
		CacheWriteTokens: 10,
		ThinkingTokens:   5,
		TotalCostUsd:     1.5,
		DurationMsSum:    300,
		DurationMsCount:  2,
		DurationMsMax:    200,
		FirstTokenMsSum:  80,
		FirstTokenMsMax:  50,
		LastUsedAt:       "2026-10-01T01:00:00.000Z",
	}
	second := UsageStatsAccumulator{
		RequestCount:     3,
		SuccessCount:     1,
		ErrorCount:       2,
		InputTokens:      30,
		OutputTokens:     20,
		CacheWriteTokens: 5,
		ThinkingTokens:   1,
		TotalCostUsd:     0.7,
		DurationMsSum:    500,
		DurationMsCount:  3,
		DurationMsMax:    250,
		FirstTokenMsMax:  90,
		LastUsedAt:       "2026-10-01T02:00:00.000Z",
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// 首次 upsert：team + user 各一行。
	if err := agg.upsertAuthorizationTeamUsageSummaryRow(ctx, tx, ownerID, statDate, teamKey, first, updatedAt); err != nil {
		t.Fatalf("team 日摘要首次 upsert 失败: %v", err)
	}
	if err := agg.upsertAuthorizationUserUsageSummaryRow(ctx, tx, ownerID, statDate, userKey, first, updatedAt); err != nil {
		t.Fatalf("user 日摘要首次 upsert 失败: %v", err)
	}
	var teamRequests, teamRow float64
	var teamLastUsed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT request_count, row_count, last_used_at
		FROM juhe_stats.authorization_team_usage_summary_daily
		WHERE system_account_id = $1 AND stat_date = $2 AND team_filter_id = $3
			AND resource_filter_type = $4 AND resource_filter_id = $5`,
		ownerID, statDate, teamKey.teamFilterID, teamKey.resourceFilterType, teamKey.resourceFilterID).
		Scan(&teamRequests, &teamRow, &teamLastUsed); err != nil {
		t.Fatalf("team 首次 upsert 后行应存在: %v", err)
	}
	if teamRequests != 2 || teamRow != 1 {
		t.Fatalf("team 首次 upsert 后 (request_count, row_count) = (%v, %v)，want (2, 1)", teamRequests, teamRow)
	}
	if !teamLastUsed.Valid || teamLastUsed.String != first.LastUsedAt {
		t.Fatalf("team 首次 upsert 后 last_used_at = %v，want %s", teamLastUsed, first.LastUsedAt)
	}
	var userRequests float64
	if err := tx.QueryRowContext(ctx, `SELECT request_count
		FROM juhe_stats.authorization_user_usage_summary_daily
		WHERE system_account_id = $1 AND stat_date = $2 AND team_filter_id = ''
			AND grantee_filter_system_account_id = $3 AND resource_filter_type = $4 AND resource_filter_id = $5`,
		ownerID, statDate, userKey.granteeFilterSystemAccountID, userKey.resourceFilterType, userKey.resourceFilterID).
		Scan(&userRequests); err != nil {
		t.Fatalf("user 首次 upsert 后行应存在: %v", err)
	}
	if userRequests != 2 {
		t.Fatalf("user 首次 upsert 后 request_count = %v，want 2", userRequests)
	}

	// 二次 upsert 同键不同值：ON CONFLICT 增量累加 + last_used_at 取最大。
	if err := agg.upsertAuthorizationTeamUsageSummaryRow(ctx, tx, ownerID, statDate, teamKey, second, updatedAt); err != nil {
		t.Fatalf("team 日摘要二次 upsert 失败: %v", err)
	}
	if err := agg.upsertAuthorizationUserUsageSummaryRow(ctx, tx, ownerID, statDate, userKey, second, updatedAt); err != nil {
		t.Fatalf("user 日摘要二次 upsert 失败: %v", err)
	}
	var tReq, tInput, tOut, tCacheWrite, tThink, tCost, tDurSum, tDurCnt, tDurMax, tFtMax float64
	var tLastUsed, tLastError sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT request_count, input_tokens, output_tokens, cache_write_tokens,
			thinking_tokens, total_cost_usd, duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_max,
			last_used_at, last_error_at
		FROM juhe_stats.authorization_team_usage_summary_daily
		WHERE system_account_id = $1 AND stat_date = $2 AND team_filter_id = $3
			AND resource_filter_type = $4 AND resource_filter_id = $5`,
		ownerID, statDate, teamKey.teamFilterID, teamKey.resourceFilterType, teamKey.resourceFilterID).
		Scan(&tReq, &tInput, &tOut, &tCacheWrite, &tThink, &tCost, &tDurSum, &tDurCnt, &tDurMax, &tFtMax, &tLastUsed, &tLastError); err != nil {
		t.Fatalf("team 二次 upsert 后读取失败: %v", err)
	}
	if tReq != 5 || tInput != 130 || tOut != 70 || tCacheWrite != 15 || tThink != 6 {
		t.Fatalf("team 增量累加异常：requests=%v input=%v output=%v cacheWrite=%v thinking=%v，want (5, 130, 70, 15, 6)",
			tReq, tInput, tOut, tCacheWrite, tThink)
	}
	if tCost != 2.2 || tDurSum != 800 || tDurCnt != 5 || tDurMax != 250 || tFtMax != 90 {
		t.Fatalf("team 增量/取大异常：cost=%v durSum=%v durCnt=%v durMax=%v ftMax=%v，want (2.2, 800, 5, 250, 90)",
			tCost, tDurSum, tDurCnt, tDurMax, tFtMax)
	}
	if !tLastUsed.Valid || tLastUsed.String != second.LastUsedAt {
		t.Fatalf("team last_used_at 应取最大 = %v，want %s", tLastUsed, second.LastUsedAt)
	}
	if tLastError.Valid {
		t.Fatalf("team last_error_at 应保持 NULL: %v", tLastError)
	}
	var uReq, uCost float64
	if err := tx.QueryRowContext(ctx, `SELECT request_count, total_cost_usd
		FROM juhe_stats.authorization_user_usage_summary_daily
		WHERE system_account_id = $1 AND stat_date = $2 AND team_filter_id = ''
			AND grantee_filter_system_account_id = $3 AND resource_filter_type = $4 AND resource_filter_id = $5`,
		ownerID, statDate, userKey.granteeFilterSystemAccountID, userKey.resourceFilterType, userKey.resourceFilterID).
		Scan(&uReq, &uCost); err != nil {
		t.Fatalf("user 二次 upsert 后读取失败: %v", err)
	}
	if uReq != 5 || uCost != 2.2 {
		t.Fatalf("user 增量累加异常：(request_count, total_cost_usd) = (%v, %v)，want (5, 2.2)", uReq, uCost)
	}

	// ROLLBACK：defer 已执行，共享 dev 库零副作用。
	if err := tx.Rollback(); err != nil {
		t.Fatalf("事务 ROLLBACK 失败: %v", err)
	}
}
