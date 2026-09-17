package cleanuprepo

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// w13e_pg_arms2_test.go 覆盖 statssubtractpostgres.go、dataretention.go（PG），
// codexcontext.go（PG）与 deleteaccount.go（PG）的剩余错误臂与数据驱动分支。

func w13ePGZone() func(context.Context) (*time.Location, error) {
	return func(context.Context) (*time.Location, error) { return pgTestZone, nil }
}

// w13eFullFamilyRow 构造一条驱动全部扣减家族的失败行：model/errors/
// quality/latency/account 授权 summary（team+user）与授权实例查找。
func w13eFullFamilyRow() statsagg.UsageStatsRecordRow {
	text := func(v string) *string { return &v }
	row := pgTestRow()
	row.Model = text("gpt-w13e")
	row.Success = 0
	row.FailureAttribution = text("account_upstream")
	row.AccountID = text("acc-1")
	row.AccountOwnerSystemAccountID = text("w13e-owner-1")
	// ShouldAggregateUsageStatsRecord 要求：带授权三件套的行必须
	// account_authorized，否则整行拒绝聚合（所有家族语句都不会执行）。
	row.AccountAccessType = text("account_authorized")
	row.AccountAuthorizationID = text("w13e-auth-1")
	row.AccountAuthorizationSourceType = text("team")
	row.AccountAuthorizationSourceTeamID = text("w13e-team-1")
	return row
}

// TestW13ePGStatsSubtractDeleteEmptyArms：扣减后空桶的 delete-empty 失败臂。
// failOn 子串必须对齐 Bind($n 改写)后的生产 SQL 文本；行数据需满足各家族
// 的生成条件（model 非空、失败行带 account_upstream 归因、account 授权
// 三件套 + team 来源），否则对应语句不执行、注入不触发。
func TestW13ePGStatsSubtractDeleteEmptyArms(t *testing.T) {
	stages := []pgStage{
		{"totals upsert", "UPDATE juhe_stats.usage_stats_totals"},
		{"totals delete empty", "DELETE FROM juhe_stats.usage_stats_totals"},
		{"latency delete empty", "AND bucket_upper_bound_ms = $"},
		{"model delete empty", "DELETE FROM juhe_stats.usage_model_"},
		{"error delete empty", "DELETE FROM juhe_stats.usage_error_"},
		{"auth lookup scan", "instance_account_id"},
		{"quality upsert", "UPDATE juhe_stats.account_quality_minute_stats"},
		{"quality dirty", "INSERT INTO juhe_stats.account_quality_dirty_accounts"},
		{"team summary delete empty", "DELETE FROM juhe_stats.authorization_team_usage_summary_daily"},
		{"user summary delete empty", "DELETE FROM juhe_stats.authorization_user_usage_summary_daily"},
		{"team summary upsert", "UPDATE juhe_stats.authorization_team_usage_summary_daily"},
		{"quota windows", "usage_quota_hourly_windows"},
		{"rank snapshots", "usage_rank_snapshots"},
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		rec.script("stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{usageRecordDriverRow(w13eFullFamilyRow())})
		rec.script("SELECT usage_id, shard_key", []string{
			"usage_id", "shard_key", "system_account_id", "api_key_id", "account_id",
		}, [][]driver.Value{{"rec-1", "sk-1", "sys-1", "key-1", "acc-1"}})
		store := &RecordCleanupStore{
			Stats:    w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
			Dataset:  w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{stage.failOn}}),
			Business: openRecorderPG(rec),
			Now:      kitNow,
			Timezone: w13ePGZone(),
		}
		_, err := store.CleanupAPIKeyRelatedPostgres(ctx0(), "key-1", "sys-1")
		return err
	})
}

func ctx0() context.Context { return context.Background() }

// TestW13ePGStatsSubtractMergeBranches：多行扣减的合并与去重分支。
func TestW13ePGStatsSubtractMergeBranches(t *testing.T) {
	ctx := ctx0()
	rec := newPGRecorder()
	seedKitPGAPIKeyFlow(t, rec, false)
	// 两条同维度行（合并分支）+ 一条带 ErrorCode 的失败行 + 不同日期行
	// （overview min/max 分支）+ 重复授权行（seen 去重）。
	rowA := pgTestRow()
	rowB := pgTestRow()
	rowB.ID = "rec-2"
	rowB.CreatedAt = "2026-01-03T03:04:05.123Z"
	rowC := pgTestRow()
	rowC.ID = "rec-3"
	rowC.Success = 0
	rowC.ErrorCode = strPtrPG("upstream_5xx")
	rowD := pgTestRow()
	rowD.ID = "rec-4"
	rowD.CreatedAt = "2026-01-03T03:04:05.123Z"
	rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), [][]driver.Value{
		usageRecordDriverRow(rowA), usageRecordDriverRow(rowB),
		usageRecordDriverRow(rowC), usageRecordDriverRow(rowD),
	})
	store := &RecordCleanupStore{
		Stats:    openRecorderPG(rec),
		Dataset:  openRecorderPG(rec),
		Business: openRecorderPG(rec),
		Now:      kitNow,
		Timezone: w13ePGZone(),
	}
	if _, err := store.CleanupAPIKeyRelatedPostgres(ctx, "key-1", "sys-1"); err != nil {
		t.Fatalf("多行合并扣减不应报错: %v", err)
	}
}

func strPtrPG(v string) *string { return &v }

// TestW13ePGDataRetentionArms：dataretention.go PG 半区剩余臂。
func TestW13ePGDataRetentionArms(t *testing.T) {
	ctx := ctx0()
	// StatsRetentionStore：PG 删除失败 / HasMore / 非法 cutoff。
	{
		rec := newPGRecorder()
		store := &StatsRetentionStore{DB: w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{"DELETE FROM juhe_stats"}})}
		if _, err := store.CleanupUsageStatsRetention(ctx, retention.UsageStatsRetentionInput{MinuteCutoffMinute: "2026091000"}); err == nil {
			t.Fatalf("PG usage stats 清理失败应透传")
		}
	}
	{
		rec := newPGRecorder()
		store := &StatsRetentionStore{DB: w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{"DELETE FROM juhe_stats.system_metrics_samples"}})}
		if _, err := store.CleanupSystemMetricsRetention(ctx, retention.SystemMetricsRetentionInput{SamplesCutoffIso: kitUpdatedAt}); err == nil {
			t.Fatalf("PG system metrics 清理失败应透传")
		}
	}
	{
		rec := newPGRecorder()
		store := &StatsRetentionStore{DB: w13eOpenDecoratedPG(rec, w13ePGOptions{})}
		counts, err := store.CleanupNonBusinessStatsData(ctx, kitUpdatedAt, 1, pgTestZone)
		if err != nil || !counts.HasMore {
			t.Fatalf("PG 批量上限应 HasMore: %+v %v", counts, err)
		}
	}
	// UsageRecordsStore：无游标 + 有记录 → 阻塞。
	{
		rec := newPGRecorder()
		rec.script("SELECT 1 AS found", []string{"found"}, [][]driver.Value{{int64(1)}})
		store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
		batch, err := store.CleanupProcessedBefore(ctx, kitUpdatedAt, 5)
		if err != nil || batch.BlockedReason == "" {
			t.Fatalf("PG 无游标应阻塞: %+v %v", batch, err)
		}
	}
	// 无游标 + 存在性查询失败。
	{
		rec := newPGRecorder()
		failing := w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{"SELECT 1 AS found"}})
		store := &UsageRecordsStore{Catalog: failing, Stats: failing}
		if _, err := store.CleanupProcessedBefore(ctx, kitUpdatedAt, 5); err == nil {
			t.Fatalf("PG 存在性查询失败应透传")
		}
	}
	// 分区裁剪：pg_inherits 行 + COUNT 失败（ErrNoRows）/ 成功路径 / DETACH / DROP。
	partitionScript := func(rec *pgRecorder) {
		rec.script("FROM pg_inherits inherit", []string{"partition_name", "partition_bound"}, [][]driver.Value{
			{"usage_records_20260101", "FOR VALUES FROM ('2026-01-01') TO ('2026-01-02')"},
			{"usage_records_other", "FOR VALUES FROM ('x') TO ('y')"},
		})
	}
	{
		rec := newPGRecorder()
		rec.script("juhe_stats.stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		partitionScript(rec)
		// COUNT 查询不脚本化 → ErrNoRows → 错误臂。
		store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
		if _, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 5); err == nil {
			t.Fatalf("分区计数失败应透传")
		}
	}
	{
		rec := newPGRecorder()
		rec.script("juhe_stats.stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		partitionScript(rec)
		rec.script("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(7)}})
		store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
		batch, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 5)
		if err != nil || batch.DroppedPartitions != 1 || batch.DeletedRows != 7 {
			t.Fatalf("分区裁剪应成功: %+v %v", batch, err)
		}
	}
	{
		rec := newPGRecorder()
		rec.script("juhe_stats.stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		partitionScript(rec)
		rec.script("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(7)}})
		failing := w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{"DETACH PARTITION"}})
		store := &UsageRecordsStore{Catalog: failing, Stats: openRecorderPG(rec)}
		if _, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 5); err == nil {
			t.Fatalf("DETACH 失败应透传")
		}
	}
	{
		rec := newPGRecorder()
		rec.script("juhe_stats.stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		partitionScript(rec)
		rec.script("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(7)}})
		failing := w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{"DROP TABLE IF EXISTS"}})
		store := &UsageRecordsStore{Catalog: failing, Stats: openRecorderPG(rec)}
		if _, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 5); err == nil {
			t.Fatalf("DROP TABLE 失败应透传")
		}
	}
	{
		rec := newPGRecorder()
		rec.script("juhe_stats.stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"}, [][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
		partitionScript(rec)
		rec.script("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(7)}})
		failing := w13eOpenDecoratedPG(rec, w13ePGOptions{failCommit: true})
		store := &UsageRecordsStore{Catalog: failing, Stats: openRecorderPG(rec)}
		if _, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 5); err == nil {
			t.Fatalf("分区裁剪 Commit 失败应透传")
		}
	}
	// 行删除路径：批超限、scan 失败、空 id 跳过、目录行删除失败、Commit 失败。
	pgRows := func(rec *pgRecorder, rows ...statsagg.UsageStatsRecordRow) {
		values := make([][]driver.Value, 0, len(rows))
		for _, row := range rows {
			values = append(values, usageRecordDriverRow(row))
		}
		rec.script("FROM juhe_usage.usage_records", usageRecordColumns(), values)
	}
	small := pgTestRow()
	small.ID = "rec-1"
	small.CreatedAt = "2026-01-01T00:00:00.000Z"
	// 批超限（3 行、limit 2）。
	{
		rec := newPGRecorder()
		seedKitPGAPIKeyFlow(t, rec, false)
		row2, row3 := small, small
		row2.ID = "rec-2"
		row3.ID = "rec-3"
		pgRows(rec, small, row2, row3)
		store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
		batch, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 2)
		if err != nil || !batch.HasMore || batch.DeletedRows != 2 {
			for i, st := range rec.all() {
				t.Logf("stmt[%d] %s", i, oneLineSQL(st.query))
			}
			t.Fatalf("批超限应 HasMore: %+v %v", batch, err)
		}
	}
	// scan 失败（脚本行集喂不可转换值）。
	{
		rec := newPGRecorder()
		seedKitPGAPIKeyFlow(t, rec, false)
		rec.script("FROM juhe_usage.usage_records", []string{"id", "created_at"}, [][]driver.Value{
			{struct{}{}, "2026-01-01T00:00:00.000Z"},
		})
		store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
		if _, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 2); err == nil {
			t.Fatalf("scan 失败应透传")
		}
	}
	// 空 id 跳过。
	{
		rec := newPGRecorder()
		seedKitPGAPIKeyFlow(t, rec, false)
		empty := small
		empty.ID = " "
		pgRows(rec, empty)
		store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
		batch, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 2)
		if err != nil || batch.DeletedRows != 0 {
			t.Fatalf("空 id 行应跳过: %+v %v", batch, err)
		}
	}
	// 目录条目删除失败。
	{
		rec := newPGRecorder()
		seedKitPGAPIKeyFlow(t, rec, false)
		pgRows(rec, small)
		failing := w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: []string{"DELETE FROM juhe_usage.usage_record_shard_entries"}})
		store := &UsageRecordsStore{Catalog: failing, Stats: openRecorderPG(rec)}
		if _, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 2); err == nil {
			t.Fatalf("目录条目删除失败应透传")
		}
	}
	// usage_records 行删除失败与 Commit 失败。
	for _, tc := range []struct {
		name string
		opts w13ePGOptions
	}{
		{"行删除失败", w13ePGOptions{failOn: []string{"DELETE FROM juhe_usage.usage_records WHERE (created_at"}}},
		{"RowsAffected 失败", w13ePGOptions{rowsAffectedFailOn: "DELETE FROM juhe_usage.usage_records WHERE (created_at"}},
		{"Commit 失败", w13ePGOptions{failCommit: true}},
		{"scope 收缩失败", w13ePGOptions{failOn: []string{"DELETE FROM juhe_usage.usage_record_account_shards scope"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newPGRecorder()
			seedKitPGAPIKeyFlow(t, rec, false)
			pgRows(rec, small)
			failing := w13eOpenDecoratedPG(rec, tc.opts)
			plain := openRecorderPG(rec)
			store := &UsageRecordsStore{Catalog: failing, Stats: plain}
			if _, err := store.CleanupProcessedBefore(ctx, "2026-02-01T00:00:00.000Z", 2); err == nil {
				t.Fatalf("%s 应透传错误", tc.name)
			}
		})
	}
}

// TestW13ePGCodexArms：codexcontext.go PG 半区剩余臂。
func TestW13ePGCodexArms(t *testing.T) {
	ctx := ctx0()
	newStore := func(t *testing.T, rec *pgRecorder, failOn ...string) *CodexContextStore {
		return &CodexContextStore{
			Postgres:    true,
			PG:          w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: failOn}),
			Now:         kitNow,
			RetryJitter: func(int64) int64 { return 0 },
		}
	}
	pgCodexSeed := func(rec *pgRecorder) {
		rec.script("FROM juhe_codex_context.codex_context_sessions\n      WHERE expires_at < ?",
			[]string{"id", "expires_at"}, [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}})
		rec.script("SELECT storage_key\n      FROM juhe_codex_context.codex_context_responses",
			[]string{"storage_key"}, [][]driver.Value{{"k-1"}})
		rec.script("SELECT storage_key\n      FROM juhe_codex_context.codex_context_compacts",
			[]string{"storage_key"}, [][]driver.Value{})
		rec.script("FROM juhe_codex_context.codex_context_responses\n        WHERE session_id IN",
			[]string{"session_id", "expires_at"}, [][]driver.Value{{"s-1", "2027-01-01T00:00:00.000Z"}})
		rec.script("FROM juhe_codex_context.codex_context_compacts\n        WHERE session_id IN",
			[]string{"session_id", "expires_at"}, [][]driver.Value{})
		rec.script("FROM juhe_codex_context.codex_context_storage_cleanup_queue\n      WHERE next_attempt_at <= ?",
			[]string{"storage_key"}, [][]driver.Value{})
		rec.script("SELECT DISTINCT storage_key\n        FROM juhe_codex_context.codex_context_responses",
			[]string{"storage_key"}, [][]driver.Value{})
		rec.script("SELECT DISTINCT storage_key\n        FROM juhe_codex_context.codex_context_compacts",
			[]string{"storage_key"}, [][]driver.Value{})
	}
	stages := []pgStage{
		{"sessions select", "FROM juhe_codex_context.codex_context_sessions"},
		{"responses delete", "DELETE FROM juhe_codex_context.codex_context_responses"},
		{"compacts delete", "DELETE FROM juhe_codex_context.codex_context_compacts"},
		{"remaining select", "GROUP BY session_id"},
		{"session refresh", "SET updated_at = ?, expires_at = ?"},
		{"session delete", "DELETE FROM juhe_codex_context.codex_context_sessions WHERE id = ?"},
		{"queue select", "FROM juhe_codex_context.codex_context_storage_cleanup_queue\n      WHERE next_attempt_at <= ?"},
		{"distinct responses", "SELECT DISTINCT storage_key\n        FROM juhe_codex_context.codex_context_responses"},
		{"distinct compacts", "SELECT DISTINCT storage_key\n        FROM juhe_codex_context.codex_context_compacts"},
		{"queue delete", "DELETE FROM juhe_codex_context.codex_context_storage_cleanup_queue WHERE storage_key IN"},
		{"attempt select", "SELECT attempt_count\n      FROM juhe_codex_context.codex_context_storage_cleanup_queue"},
		{"attempt update", "UPDATE juhe_codex_context.codex_context_storage_cleanup_queue"},
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		pgCodexSeed(rec)
		store := newStore(t, rec, stage.failOn)
		_, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10)
		if err == nil {
			_, err = store.SettleStorageCleanup(ctx, Settlement{
				SucceededStorageKeys: []string{"k-9"},
				Failures:             []SettlementFailure{{StorageKey: "k-1", Error: "boom"}},
				Now:                  kitUpdatedAt,
			})
		}
		return err
	})
	// BeginTx / Commit / RowsAffected 失败。
	for _, tc := range []struct {
		name string
		opts w13ePGOptions
	}{
		{"Begin 失败", w13ePGOptions{failBegin: true}},
		{"Commit 失败", w13ePGOptions{failCommit: true}},
		{"RowsAffected 失败", w13ePGOptions{rowsAffectedFailOn: "DELETE FROM juhe_codex_context.codex_context_sessions WHERE id = ?"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newPGRecorder()
			pgCodexSeed(rec)
			store := &CodexContextStore{Postgres: true, PG: w13eOpenDecoratedPG(rec, tc.opts), Now: kitNow}
			if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil {
				t.Fatalf("%s 应透传错误", tc.name)
			}
		})
	}
	// session scan 失败与 rows.Err（脚本行集）。
	for _, tc := range []struct {
		name   string
		script w13eRowsScript
	}{
		{"scan 失败", w13eRowsScript{match: "FROM juhe_codex_context.codex_context_sessions", columns: []string{"id", "expires_at"},
			values: [][]driver.Value{{struct{}{}, "x"}}, errAfterRows: -1}},
		{"迭代错误", w13eRowsScript{match: "FROM juhe_codex_context.codex_context_sessions", columns: []string{"id", "expires_at"},
			values: [][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}}, errAfterRows: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newPGRecorder()
			rec.script("FROM juhe_codex_context.codex_context_sessions\n      WHERE expires_at < ?", nil, nil)
			rec.reset()
			pgCodexSeed(rec)
			// 覆盖 sessions 脚本：重新登记到队首。
			rec2 := newPGRecorder()
			rec2.script("FROM juhe_codex_context.codex_context_sessions\n      WHERE expires_at < ?",
				tc.script.columns, tc.script.values)
			if tc.script.errAfterRows >= 0 {
				rec2 = newPGRecorder()
				rec2.script("FROM juhe_codex_context.codex_context_sessions\n      WHERE expires_at < ?",
					[]string{"id", "expires_at"},
					[][]driver.Value{{"s-1", "2026-01-01T00:00:00.000Z"}, {"s-2", "2026-01-01T00:00:00.000Z"}})
			}
			pgCodexSeed(rec2)
			store := newStore(t, rec2)
			if _, err := store.CleanupExpiredStates(ctx, "2026-09-10T00:00:00.000Z", 10); err == nil {
				t.Fatalf("脚本注入应产生错误")
			}
		})
	}
}

// TestW13ePGDeletedAccountArms：deleteaccount.go PG 半区剩余臂。
func TestW13ePGDeletedAccountArms(t *testing.T) {
	ctx := ctx0()
	newStore := func(t *testing.T, rec *pgRecorder, failOn ...string) *DeletedAccountStore {
		// 所有句柄共享同一注入配置（failOn 作用于装饰句柄）。
		decorated := w13eOpenDecoratedPG(rec, w13ePGOptions{failOn: failOn})
		return &DeletedAccountStore{
			Business:           decorated,
			Dataset:            decorated,
			Records:            &RecordCleanupStore{Stats: decorated, Dataset: decorated, Business: decorated, Now: kitNow},
			Now:                kitNow,
			OrphanSweepEnabled: true,
		}
	}
	pgAccountSeed := func(rec *pgRecorder) {
		// match 只命中 listCandidates 根查询（孤儿扫尾查询无此子串）。
		rec.script("authorization_instance_authorization_id IS NULL", []string{
			"id", "system_account_id", "authorization_instance_authorization_id",
			"authorization_instance_source_account_id", "deleted_at", "updated_at",
		}, [][]driver.Value{{"acc-1", "sys-1", nil, nil, "2026-06-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"}})
	}
	stages := []pgStage{
		{"orphan sweep", "LEFT JOIN"},
		{"physically delete pg tables", "DELETE FROM account_name_search_terms"},
		{"tombstone outbox", "INSERT INTO account_health_jobs_input_outbox"},
		{"related targets", "FROM juhe_dataset.account_record_cleanup_targets"},
		{"related usage", "FROM juhe_usage.usage_records WHERE account_id = ANY"},
		{"related usage auth", "FROM juhe_usage.usage_records WHERE account_authorization_id = ANY"},
		{"related usage team", "FROM juhe_usage.usage_records WHERE group_authorization_id = ANY"},
		{"related audit", "FROM juhe_dataset.audit_logs"},
		{"related quality", "FROM juhe_stats.account_quality_scores"},
		{"related snapshots", "FROM juhe_stats.account_usage_snapshots"},
	}
	runPGStages(t, stages, func(t *testing.T, stage pgStage) error {
		rec := newPGRecorder()
		pgAccountSeed(rec)
		store := newStore(t, rec, stage.failOn)
		_, err := store.CleanupExpired(ctx)
		return err
	})
	// hasRelatedRecordDataPostgres：record cleanup 目标未清 → true。
	{
		rec := newPGRecorder()
		rec.script("FROM juhe_dataset.account_record_cleanup_targets", []string{"found"}, [][]driver.Value{{int64(1)}})
		store := newStore(t, rec)
		store.OrphanSweepEnabled = false
		ok, err := store.hasRelatedRecordData(ctx, &cleanupTarget{AccountID: "acc-1", SystemAccountID: "sys-1"})
		if err != nil || !ok {
			t.Fatalf("目标未清应返回 true: %v %v", ok, err)
		}
	}
	// logicallyDeleteAccountsTx 的 PG 专有表分支（直接经 physicallyDelete）。
	{
		rec := newPGRecorder()
		pgAccountSeed(rec)
		store := newStore(t, rec, "account_name_search_terms")
		summary, err := store.CleanupExpired(ctx)
		if err != nil && !strings.Contains(err.Error(), "account_name_search_terms") {
			t.Fatalf("PG 专有表删除失败应透传: %v", err)
		}
		if summary != nil && summary.Failed == 1 {
			return
		}
		if summary != nil && summary.Failed == 0 && err == nil {
			t.Fatalf("PG 专有表删除应失败")
		}
	}
}
