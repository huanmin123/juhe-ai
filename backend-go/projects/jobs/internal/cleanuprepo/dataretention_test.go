package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// dataretention.go 的语义测试：public_api_logs / system_sessions 保留清理、
// stats+metrics 保留、非业务数据硬清理（stats/dataset/usage-catalog 三半区）、
// 使用记录安全游标清理（SQLite 真库 + PG 录制）。

func createKitMinimalTable(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" ("%s" TEXT)`, table, column))
}

// TestPublicApiLogsAndSessionsCleanup：rowid 批删语义（严格早于 cutoff、
// 最旧优先、limit 上限）。
func TestPublicApiLogsAndSessionsCleanup(t *testing.T) {
	db := openKitSQLite(t, "public_logs")
	execKitSchema(t, db.DB,
		`CREATE TABLE public_api_logs (id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`,
		`CREATE TABLE system_sessions (id TEXT PRIMARY KEY, expires_at TEXT NOT NULL)`)
	for _, id := range []string{"old-2", "old-1", "edge", "new"} {
		createdAt := map[string]string{
			"old-1": "2026-09-01T00:00:00.000Z", "old-2": "2026-08-01T00:00:00.000Z",
			"edge": "2026-09-05T00:00:00.000Z", "new": "2026-09-20T00:00:00.000Z",
		}[id]
		mustExecKit(t, db, `INSERT INTO public_api_logs (id, created_at) VALUES (?, ?)`, id, createdAt)
		mustExecKit(t, db, `INSERT INTO system_sessions (id, expires_at) VALUES (?, ?)`, id, createdAt)
	}
	logs := &PublicApiLogsStore{DB: db}
	// limit=1：只删最旧的一行（old-2）。
	deleted, err := logs.CleanupBefore(context.Background(), "2026-09-05T00:00:00.000Z", 1)
	if err != nil || deleted != 1 {
		t.Fatalf("limited deleted = %d, %v", deleted, err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM public_api_logs WHERE id = 'old-2'`); got != 0 {
		t.Fatalf("最旧行应先被删除")
	}
	// 放开 limit：删除剩余严格早于 cutoff 的行（old-1）；边界行（等于 cutoff）保留。
	deleted, err = logs.CleanupBefore(context.Background(), "2026-09-05T00:00:00.000Z", 10)
	if err != nil || deleted != 1 {
		t.Fatalf("logs deleted = %d, %v", deleted, err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM public_api_logs WHERE id IN ('edge','new')`); got != 2 {
		t.Fatalf("边界行不应被删除")
	}
	sessions := &SystemSessionsStore{DB: db}
	deleted, err = sessions.CleanupExpired(context.Background(), "2026-09-05T00:00:00.000Z", 10)
	if err != nil || deleted != 2 {
		t.Fatalf("sessions deleted = %d, %v", deleted, err)
	}

	// PG 语句形状（录制驱动）：ctid 子查询 + $n 占位。
	rec := newPGRecorder()
	pgLogs := &PublicApiLogsStore{DB: openRecorderPG(rec)}
	if _, err := pgLogs.CleanupBefore(context.Background(), "2026-09-05T00:00:00.000Z", 7); err != nil {
		t.Fatalf("pg cleanup: %v", err)
	}
	want := bindTestPG(`
      DELETE FROM juhe_dataset.public_api_logs
      WHERE ctid IN (
        SELECT ctid FROM juhe_dataset.public_api_logs
        WHERE created_at < ?
        ORDER BY created_at ASC, ctid ASC
        LIMIT ?
      )
	`)
	if got, wantN := rec.all()[0].query, normalizeKitSQL(want); normalizeKitSQL(got) != wantN {
		t.Fatalf("PG public_api_logs 语句不匹配：\n%s", got)
	}
}

// TestDeleteRowsBeforeAndQuoting：rowid/ctid 批删的引用与方言差异。
func TestDeleteRowsBeforeAndQuoting(t *testing.T) {
	db := openKitSQLite(t, "rows_before")
	createKitMinimalTable(t, db.DB, "usage_stats_minute", "stat_minute")
	mustExecKit(t, db, `INSERT INTO "usage_stats_minute" ("stat_minute") VALUES ('a'), ('b'), ('c')`)
	// 严格早于 'b'：只删 'a'（'b' 自身为边界保留）。
	deleted, err := deleteRowsBefore(context.Background(), db, "", "usage_stats_minute", "stat_minute", "b", 10)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted = %d, %v", deleted, err)
	}
	if got := mustQueryCountKit(t, db, `SELECT COUNT(*) FROM "usage_stats_minute"`); got != 2 {
		t.Fatalf("残余 = %d", got)
	}
	if got := quoteIdentifierTable(db, "t"); got != `"t"` {
		t.Fatalf("SQLite 引用 = %q", got)
	}
	pg := &DB{Postgres: true}
	if got := quoteIdentifierTable(pg, "juhe_stats.t"); got != "juhe_stats.t" {
		t.Fatalf("PG 表引用 = %q", got)
	}
	if got := quoteIdentifierColumn(pg, "col"); got != `"col"` {
		t.Fatalf("PG 列引用 = %q", got)
	}

	rec := newPGRecorder()
	if _, err := deleteRowsBefore(context.Background(), openRecorderPG(rec), "juhe_stats", "usage_stats_minute", "stat_minute", "2026-01-01T00:00", 7); err != nil {
		t.Fatalf("pg deleteRowsBefore: %v", err)
	}
	want := bindTestPG(fmt.Sprintf(`
      DELETE FROM %s
      WHERE ctid IN (
        SELECT ctid FROM %s
        WHERE %s < ?
        ORDER BY %s ASC, ctid ASC
        LIMIT ?
      )
	`, "juhe_stats.usage_stats_minute", "juhe_stats.usage_stats_minute", `"stat_minute"`, `"stat_minute"`))
	if got, wantN := rec.all()[0].query, normalizeKitSQL(want); normalizeKitSQL(got) != wantN {
		t.Fatalf("PG 批删语句不匹配：\n%s", got)
	}
}

// TestCleanupUsageStatsRetentionSQLite：specs → 结果字段映射 + 批删 + checkpoint。
func TestCleanupUsageStatsRetentionSQLite(t *testing.T) {
	db := openKitSQLite(t, "stats_retention")
	for _, spec := range usageStatsRetentionSpecs {
		createKitMinimalTable(t, db.DB, spec.TableName, spec.Column)
	}
	// 种子：minute 桶 2 行、daily 1 行、rank snapshots 1 行。
	mustExecKit(t, db, `INSERT INTO "usage_stats_minute" ("stat_minute") VALUES ('2020-01-01T00:00'), ('2020-02-01T00:00')`)
	mustExecKit(t, db, `INSERT INTO "usage_stats_daily" ("stat_date") VALUES ('2020-01-01')`)
	mustExecKit(t, db, `INSERT INTO "usage_rank_snapshots" ("snapshot_at") VALUES ('2020-01-01T00:00:00.000Z')`)
	checkpoints := 0
	store := &StatsRetentionStore{DB: db, Checkpoint: func(context.Context) error { checkpoints++; return nil }}

	counts, err := store.CleanupUsageStatsRetention(context.Background(), retention.UsageStatsRetentionInput{
		MinuteCutoffMinute:    "2026-01-01T00:00",
		DailyCutoffDate:       "2026-01-01",
		RankSnapshotCutoffIso: "2026-01-01T00:00:00.000Z",
		Limit:                 10,
	})
	if err != nil {
		t.Fatalf("CleanupUsageStatsRetention: %v", err)
	}
	if counts.UsageStatsMinute != 2 || counts.UsageStatsDaily != 1 || counts.UsageRankSnapshots != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoint 调用 = %d", checkpoints)
	}
	// 全 cutoff 缺省 → 全表批删为 0，不报错（字段映射完整性）。
	empty, err := store.CleanupUsageStatsRetention(context.Background(), retention.UsageStatsRetentionInput{Limit: 1})
	if err != nil {
		t.Fatalf("全缺省 cutoffs: %v", err)
	}
	if empty.AccountHealthHourly != 0 || empty.AuthorizationUserUsageRangeWindows != 0 {
		t.Fatalf("空 counts = %+v", empty)
	}
}

// TestCleanupSystemMetricsRetention：六表映射。
func TestCleanupSystemMetricsRetention(t *testing.T) {
	db := openKitSQLite(t, "metrics_retention")
	for _, spec := range []struct{ table, column string }{
		{"system_metrics_samples", "sampled_at"}, {"system_metrics_hourly", "stat_hour"},
		{"system_metrics_trend_windows", "end_date"}, {"process_event_loop_samples", "sampled_at"},
		{"process_event_loop_hourly", "stat_hour"}, {"process_event_loop_trend_windows", "end_date"},
	} {
		createKitMinimalTable(t, db.DB, spec.table, spec.column)
	}
	mustExecKit(t, db, `INSERT INTO "system_metrics_samples" ("sampled_at") VALUES ('2020-01-01T00:00:00.000Z'), ('2020-06-01T00:00:00.000Z')`)
	store := &StatsRetentionStore{DB: db}
	counts, err := store.CleanupSystemMetricsRetention(context.Background(), retention.SystemMetricsRetentionInput{
		SamplesCutoffIso: "2026-01-01T00:00:00.000Z", Limit: 10,
	})
	if err != nil {
		t.Fatalf("CleanupSystemMetricsRetention: %v", err)
	}
	if counts.SystemMetricsSamples != 2 || counts.SystemMetricsHourly != 0 || counts.ProcessEventLoopSamples != 0 {
		t.Fatalf("counts = %+v", counts)
	}
	store.AfterDelete(context.Background()) // nil Checkpoint 不应 panic
}

// TestCleanupNonBusinessStatsData：cutoff 键计算 + 全表批删 + HasMore 语义。
func TestCleanupNonBusinessStatsData(t *testing.T) {
	db := openKitSQLite(t, "non_business_stats")
	for _, rule := range nonBusinessStatsCleanupTables {
		createKitMinimalTable(t, db.DB, rule.TableName, rule.TimeColumnName)
	}
	mustExecKit(t, db, `INSERT INTO "usage_stats_totals" ("updated_at") VALUES ('2026-01-01T00:00:00.000Z'), ('2026-02-01T00:00:00.000Z')`)
	mustExecKit(t, db, `INSERT INTO "system_metrics_samples" ("sampled_at") VALUES ('2026-01-01T00:00:00.000Z')`)
	store := &StatsRetentionStore{DB: db}

	counts, err := store.CleanupNonBusinessStatsData(context.Background(), kitUpdatedAt, 10, kitZone())
	if err != nil {
		t.Fatalf("CleanupNonBusinessStatsData: %v", err)
	}
	if counts.DeletedRows != 3 {
		t.Fatalf("DeletedRows = %d", counts.DeletedRows)
	}
	if counts.TableRows["juhe_stats.usage_stats_totals"] != 2 || counts.TableRows["juhe_stats.system_metrics_samples"] != 1 {
		t.Fatalf("TableRows = %v", counts.TableRows)
	}
	if counts.HasMore {
		t.Fatalf("未触批上限不应 HasMore")
	}
	if _, err := store.CleanupNonBusinessStatsData(context.Background(), "bad-time", 10, kitZone()); err == nil {
		t.Fatalf("非法 cutoff 应报错")
	}
}

// TestHardCleanupCutoffs：iso 与业务时区五键。
func TestHardCleanupCutoffs(t *testing.T) {
	cutoffs, err := hardCleanupCutoffs("2026-01-05T20:30:00.000Z", kitZone())
	if err != nil {
		t.Fatalf("hardCleanupCutoffs: %v", err)
	}
	// iso 原样透传（仅 trim）；minute/hour/date/week/month 按业务时区（UTC+8）。
	if cutoffs["iso"] != "2026-01-05T20:30:00.000Z" || cutoffs["minute"] != "2026-01-06T04:30" ||
		cutoffs["hour"] != "2026-01-06T04" || cutoffs["date"] != "2026-01-06" || cutoffs["month"] != "2026-01" {
		t.Fatalf("cutoffs = %v", cutoffs)
	}
	if cutoffs["week"] != "2026-01-05" {
		t.Fatalf("week 键 = %q", cutoffs["week"])
	}
	if _, err := hardCleanupCutoffs("nope", kitZone()); err == nil {
		t.Fatalf("非法时间应报错")
	}
}

// TestIsoDatePrefix：合法日期校验（含 2026-02-30 拒绝）。
func TestIsoDatePrefix(t *testing.T) {
	if got := isoDatePrefix(" 2026-01-05T03:04:05.000Z"); got != "2026-01-05" {
		t.Fatalf("isoDatePrefix = %q", got)
	}
	for _, value := range []string{"", "abc", "2026-02-30T00:00:00.000Z", "2026-13-01"} {
		if got := isoDatePrefix(value); got != "" {
			t.Fatalf("isoDatePrefix(%q) = %q, 期望空", value, got)
		}
	}
}

// TestUsageRecordsCleanupProcessedBeforeSQLite：安全游标门控 + 分片行删除。
func TestUsageRecordsCleanupProcessedBeforeSQLite(t *testing.T) {
	f := newKitRecordFixture(t)
	store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: f.shards}
	ctx := context.Background()

	// 无游标 + 无记录 → 空批次。
	batch, err := store.CleanupProcessedBefore(ctx, kitUpdatedAt, 5)
	if err != nil || batch.DeletedRows != 0 || batch.BlockedReason != "" || batch.HasMore {
		t.Fatalf("空批次 = %+v, %v", batch, err)
	}
	// 无游标 + 有记录 → 阻塞。
	f.addKitShard(t, "sk-blocked", "2026-01-05", 1, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-1", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
	})
	batch, err = store.CleanupProcessedBefore(ctx, kitUpdatedAt, 5)
	if err != nil || batch.DeletedRows != 0 || !strings.Contains(batch.BlockedReason, "游标尚未建立") {
		t.Fatalf("阻塞批次 = %+v, %v", batch, err)
	}
	// 移除阻塞分片后：双游标齐备 → 批删 + 目录收缩 + 安全游标回显。
	mustExecKit(t, f.catalog, `DELETE FROM usage_record_shard_entries WHERE shard_key = 'sk-blocked'`)
	mustExecKit(t, f.catalog, `DELETE FROM usage_record_shards WHERE shard_key = 'sk-blocked'`)
	f.addKitShard(t, "sk-2", "2026-01-06", 2, "key-1", "sys-1", "acc-1", true, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-2", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
		kitUsageRecord("rec-3", "sys-1", "key-1", "acc-1", "2026-01-05T02:00:00.000Z"),
	})
	batch, err = store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 1)
	if err != nil {
		t.Fatalf("CleanupProcessedBefore: %v", err)
	}
	if batch.DeletedRows != 1 || !batch.HasMore {
		t.Fatalf("批次 = %+v", batch)
	}
	if batch.SafetyCursorCreatedAt != "2026-01-05T03:00:00.000Z" || batch.SafetyCursorID != "rec-000" {
		t.Fatalf("安全游标 = %+v", batch)
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries WHERE shard_key = 'sk-2'`); got != 1 {
		t.Fatalf("已删行的目录条目应收缩，残余 = %d", got)
	}
}

// TestNonBusinessDatasetCleanupBefore：dataset 半区（usage records 阻塞时
// 跳过 usage-catalog 清理）。
func TestNonBusinessDatasetCleanupBefore(t *testing.T) {
	f := newKitRecordFixture(t)
	for _, rule := range nonBusinessDatasetCleanupTables {
		createKitMinimalTable(t, f.dataset.DB, rule.TableName, rule.TimeColumnName)
	}
	for _, rule := range nonBusinessUsageCatalogCleanupTables {
		createKitMinimalTable(t, f.catalog.DB, rule.TableName, rule.TimeColumnName)
	}
	mustExecKit(t, f.dataset, `INSERT INTO "public_api_logs" ("created_at") VALUES ('2026-01-01T00:00:00.000Z')`)
	mustExecKit(t, f.dataset, `INSERT INTO "api_key_record_cleanup_targets" ("api_key_id", "system_account_id", "updated_at")
    VALUES ('key-x', 'sys-1', '2026-01-01T00:00:00.000Z')`)
	mustExecKit(t, f.catalog, `INSERT INTO "usage_record_account_shards" ("account_id", "shard_key", "last_seen_at")
    VALUES ('acc-orphan', 'sk-orphan', '2026-01-01T00:00:00.000Z')`)
	f.addKitShard(t, "sk-nb", "2026-01-05", 1, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-nb", "sys-1", "key-1", "acc-1", "2026-01-05T01:00:00.000Z"),
	})
	store := &NonBusinessDatasetStore{
		Dataset:      f.dataset,
		UsageCatalog: f.catalog,
		Stats:        f.stats,
		UsageRecords: &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: f.shards},
		Timezone:     func(context.Context) (*time.Location, error) { return kitZone(), nil },
	}

	// usage records 被安全游标阻塞 → HasMore，且 usage-catalog 半区跳过。
	counts, err := store.CleanupBefore(context.Background(), kitUpdatedAt, 10)
	if err != nil {
		t.Fatalf("CleanupBefore: %v", err)
	}
	if !counts.HasMore {
		t.Fatalf("阻塞时应 HasMore")
	}
	if counts.TableRows["dataset.public_api_logs"] != 1 || counts.TableRows["dataset.api_key_record_cleanup_targets"] != 1 {
		t.Fatalf("dataset 半区 = %v", counts.TableRows)
	}
	if _, ok := counts.TableRows["usage-catalog.usage_record_account_shards"]; ok {
		t.Fatalf("阻塞时不应清理 usage-catalog：%v", counts.TableRows)
	}

	// 清掉分片记录后（无记录可清）→ catalog 半区恢复。
	mustExecKit(t, f.catalog, `DELETE FROM usage_record_shard_entries`)
	mustExecKit(t, f.catalog, `DELETE FROM usage_record_shards`)
	counts, err = store.CleanupBefore(context.Background(), kitUpdatedAt, 10)
	if err != nil {
		t.Fatalf("second CleanupBefore: %v", err)
	}
	// 阻塞解除后 catalog 半区恢复。仅孤儿行（last_seen_at 有值）被删除；
	// addKitShard 建的行 last_seen_at 为 NULL，`NULL < cutoff` 不命中而保留
	// （与 SQL 三值语义一致）。
	if counts.TableRows["usage-catalog.usage_record_account_shards"] != 1 {
		t.Fatalf("catalog 半区 = %v", counts.TableRows)
	}
	// dataset 半区行已在首轮清理，本轮只有 catalog 的孤儿 account_shards 行。
	if counts.DeletedRows != 1 {
		t.Fatalf("DeletedRows = %d", counts.DeletedRows)
	}
}

// TestDropEligiblePartitions：PG 分区探测（最早分区 DETACH + DROP，剩余 → HasMore）。
func TestDropEligiblePartitions(t *testing.T) {
	rec := newPGRecorder()
	store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
	rec.script("FROM pg_inherits inherit", []string{"partition_name", "partition_bound"},
		[][]driver.Value{
			{"usage_records_20260101", "FOR VALUES FROM ('2026-01-01') TO ('2026-01-02')"},
			{"usage_records_20260102", "FOR VALUES FROM ('2026-01-02') TO ('2026-01-03')"},
			{"usage_records_other", "FOR VALUES FROM ('2026-01-03') TO ('2026-01-04')"}, // 命名不匹配
			{"usage_records_20260103", "FOR VALUES FROM INHERIT ('x')"},                 // bound 不匹配
			{"usage_records_20260999", "FOR VALUES FROM ('2026-09-01') TO ('2026-09-02')"},
		})
	rec.script("SELECT COUNT(*) AS total", []string{"total"}, [][]driver.Value{{int64(5)}})

	outcome, err := store.dropEligiblePartitions(context.Background(),
		"2026-09-10T00:00:00.000Z", cleanupCursor{CreatedAt: "2026-01-05T00:00:00.000Z", ID: "rec-0"})
	if err != nil {
		t.Fatalf("dropEligiblePartitions: %v", err)
	}
	if outcome.DeletedRows != 5 || outcome.DroppedPartitions != 1 || !outcome.HasMore {
		t.Fatalf("outcome = %+v", outcome)
	}
	joined := make([]string, 0, 4)
	for _, statement := range rec.all() {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	if !strings.Contains(allText, `ALTER TABLE juhe_usage.usage_records DETACH PARTITION juhe_usage."usage_records_20260101"`) {
		t.Fatalf("缺少 DETACH 语句：\n%s", allText)
	}
	if !strings.Contains(allText, `DROP TABLE IF EXISTS juhe_usage."usage_records_20260101"`) {
		t.Fatalf("缺少 DROP 语句")
	}
	// 非法日期 cursor → 空结果。
	outcome, err = store.dropEligiblePartitions(context.Background(), "2026-09-10T00:00:00.000Z", cleanupCursor{CreatedAt: "bad"})
	if err != nil || outcome.DroppedPartitions != 0 {
		t.Fatalf("非法 cursor = %+v, %v", outcome, err)
	}
}

// TestCleanupProcessedBeforePostgres：PG 批删链（目录收缩 + 分区键删除）。
func TestCleanupProcessedBeforePostgres(t *testing.T) {
	rec := newPGRecorder()
	store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
	rec.script("juhe_stats.stats_job_state", []string{"job_name", "cursor_created_at", "cursor_id"},
		[][]driver.Value{
			{"usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
			{"client_ip_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-0"},
		})
	// 无过期分区 → 走行级删除。
	rec.script("FROM pg_inherits inherit", []string{"partition_name", "partition_bound"}, nil)
	rec.script("SELECT id, created_at", []string{"id", "created_at"}, [][]driver.Value{
		{"rec-1", "2026-01-05T01:00:00.000Z"}, {"rec-2", "2026-01-05T02:00:00.000Z"},
	})
	rec.script("SELECT usage_id, shard_key", []string{
		"usage_id", "shard_key", "system_account_id", "api_key_id", "account_id",
	}, [][]driver.Value{{"rec-1", "sk-1", "sys-1", "key-1", "acc-1"}, {"rec-2", "sk-1", "sys-1", "key-1", ""}})

	batch, err := store.CleanupProcessedBefore(context.Background(), "2026-09-10T00:00:00.000Z", 10)
	if err != nil {
		t.Fatalf("CleanupProcessedBefore: %v", err)
	}
	if batch.SafetyCursorCreatedAt != "2026-01-05T03:00:00.000Z" || batch.SafetyCursorID != "rec-0" {
		t.Fatalf("batch = %+v", batch)
	}
	joined := make([]string, 0, 8)
	for _, statement := range rec.all() {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	for _, needle := range []string{
		"DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY",
		"DELETE FROM juhe_usage.usage_record_account_shards",
		"DELETE FROM juhe_usage.usage_record_api_key_shards",
		"DELETE FROM juhe_usage.usage_records WHERE (created_at, id) IN",
	} {
		if !strings.Contains(allText, needle) {
			t.Fatalf("缺少 PG 语句：%s", needle)
		}
	}

	// 双游标缺一 → cursor nil：无记录时返回空批次。
	emptyRec := newPGRecorder()
	emptyStore := &UsageRecordsStore{Catalog: openRecorderPG(emptyRec), Stats: openRecorderPG(emptyRec)}
	batch, err = emptyStore.CleanupProcessedBefore(context.Background(), "2026-09-10T00:00:00.000Z", 10)
	if err != nil || batch.BlockedReason != "" || batch.DeletedRows != 0 {
		t.Fatalf("无游标空批次 = %+v, %v", batch, err)
	}
	// 无游标 + 有记录 → 阻塞。
	emptyRec.script("FROM juhe_usage.usage_records", []string{"found"}, [][]driver.Value{{int64(1)}})
	batch, err = emptyStore.CleanupProcessedBefore(context.Background(), "2026-09-10T00:00:00.000Z", 10)
	if err != nil || !strings.Contains(batch.BlockedReason, "游标尚未建立") {
		t.Fatalf("阻塞批次 = %+v, %v", batch, err)
	}
}

// TestPostgresUsageRecordCatalogHelpers：目录条目删除触发 scope catalog
// 收缩语句（录制驱动，逐语句核对家族）。
func TestPostgresUsageRecordCatalogHelpers(t *testing.T) {
	rec := newPGRecorder()
	store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
	rec.script("SELECT usage_id, shard_key", []string{
		"usage_id", "shard_key", "system_account_id", "api_key_id", "account_id",
	}, [][]driver.Value{{"rec-1", "sk-1", "sys-1", "key-1", "acc-1"}})
	tx, err := store.Catalog.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := deletePostgresUsageRecordCatalogRowsByUsageIds(context.Background(), store, tx, []string{"rec-1"}); err != nil {
		t.Fatalf("deletePostgresUsageRecordCatalogRowsByUsageIds: %v", err)
	}
	joined := make([]string, 0, 4)
	for _, statement := range rec.all() {
		joined = append(joined, statement.query)
	}
	allText := strings.Join(joined, "\n")
	for _, needle := range []string{
		"DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY",
		"DELETE FROM juhe_usage.usage_record_account_shards scope",
		"DELETE FROM juhe_usage.usage_record_api_key_shards scope",
		"NOT EXISTS",
	} {
		if !strings.Contains(allText, needle) {
			t.Fatalf("缺少 scope 收缩语句：%s", needle)
		}
	}
	// 空 ids 直接返回。
	if err := deletePostgresUsageRecordCatalogRowsByUsageIds(context.Background(), store, tx, nil); err != nil {
		t.Fatalf("空 ids 应直通：%v", err)
	}
}
