package cleanuprepo

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// stats 库 usage_records 镜像（usagewriter mirrorStatsUsageRecords 的写入侧）
// 保留清理的契约测试：删除必须同时满足「早于保留期 cutoff」且「落在
// usage_stats_aggregation + client_ip_stats_aggregation 双 global 聚合游标的
// floor 之前」；未聚合行（游标未覆盖）必须保留。全部经公开入口
// CleanupProcessedBefore 驱动（catalog 为空 → 分片半区空转，镜像半区工作）。

func newStatsMirrorFixture(t *testing.T) (*UsageRecordsStore, *DB) {
	t.Helper()
	stats := openKitSQLite(t, "stats_mirror")
	createKitStatsChainSchema(t, stats.DB)
	catalog := newKitCatalog(t)
	shards := newKitShardStore(t, filepath.Join(t.TempDir(), "shards"))
	store := &UsageRecordsStore{Catalog: catalog, Stats: stats, Shards: shards}
	return store, stats
}

func seedStatsMirrorRecord(t *testing.T, stats *DB, id, createdAt string) {
	t.Helper()
	mustExecKit(t, stats, `INSERT INTO usage_records (id, system_account_id, trace_id, traffic_source, success, created_at)
      VALUES (?, 'sys-1', 'trace-1', 'api', 1, ?)`, id, createdAt)
}

func seedStatsMirrorGlobalCursor(t *testing.T, stats *DB, jobName, createdAt, id string) {
	t.Helper()
	mustExecKit(t, stats, `INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id)
      VALUES ('global', '', ?, ?, ?)`, jobName, createdAt, id)
}

func statsMirrorRemainingIDs(t *testing.T, stats *DB) []string {
	t.Helper()
	rows, err := stats.QueryContext(context.Background(), `SELECT id FROM usage_records ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("query mirror ids: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan mirror id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	sort.Strings(ids)
	return ids
}

func stringsJoinIDs(ids []string) string { return strings.Join(ids, ",") }

// TestStatsMirrorRetentionDeletesOnlyExpiredAggregatedRows：双门控删除语义。
// floor 取两游标更早者（client_ip 的 2026-01-01T00:00:00.000Z/rec-100），
// cutoff 晚于 floor，使「游标边界」与「保留期」两个门控各自可见。
func TestStatsMirrorRetentionDeletesOnlyExpiredAggregatedRows(t *testing.T) {
	store, stats := newStatsMirrorFixture(t)
	ctx := context.Background()
	seedStatsMirrorGlobalCursor(t, stats, "usage_stats_aggregation", "2026-01-05T03:00:00.000Z", "rec-500")
	seedStatsMirrorGlobalCursor(t, stats, "client_ip_stats_aggregation", "2026-01-01T00:00:00.000Z", "rec-100")
	seedStatsMirrorRecord(t, stats, "m-before-floor", "2025-12-31T00:00:00.000Z") // 早于 floor 与 cutoff → 删
	seedStatsMirrorRecord(t, stats, "m-floor-id-le", "2026-01-01T00:00:00.000Z")  // == floor 且 id <= floor.id → 删
	seedStatsMirrorRecord(t, stats, "zz-floor-id-gt", "2026-01-01T00:00:00.000Z") // == floor 但 id > floor.id → 留（未聚合）
	seedStatsMirrorRecord(t, stats, "m-floor-after", "2026-01-01T00:00:00.001Z")  // 晚于 floor → 留（未聚合）
	seedStatsMirrorRecord(t, stats, "m-after-floor", "2026-01-03T00:00:00.000Z")  // 晚于 floor → 留
	seedStatsMirrorRecord(t, stats, "m-fresh", "2026-09-01T00:00:00.000Z")        // 新鲜行 → 留

	batch, err := store.CleanupProcessedBefore(ctx, "2026-01-02T00:00:00.000Z", 100)
	if err != nil {
		t.Fatalf("CleanupProcessedBefore: %v", err)
	}
	if batch.DeletedRows != 2 || batch.HasMore || batch.BlockedReason != "" {
		t.Fatalf("batch = %+v", batch)
	}
	if got := stringsJoinIDs(statsMirrorRemainingIDs(t, stats)); got != "m-after-floor,m-floor-after,m-fresh,zz-floor-id-gt" {
		t.Fatalf("残余行 = %s（未聚合行必须保留）", got)
	}

	// 幂等：重跑不再删除。
	batch, err = store.CleanupProcessedBefore(ctx, "2026-01-02T00:00:00.000Z", 100)
	if err != nil || batch.DeletedRows != 0 || batch.BlockedReason != "" {
		t.Fatalf("重跑 batch = %+v, %v", batch, err)
	}
	if got := stringsJoinIDs(statsMirrorRemainingIDs(t, stats)); got != "m-after-floor,m-floor-after,m-fresh,zz-floor-id-gt" {
		t.Fatalf("重跑后残余行 = %s", got)
	}
}

// TestStatsMirrorRetentionBlockedUntilBothGlobalCursors：任一 global 游标缺失
// 时阻塞且不删除；补齐后同批数据放行（可重试收敛）。
func TestStatsMirrorRetentionBlockedUntilBothGlobalCursors(t *testing.T) {
	store, stats := newStatsMirrorFixture(t)
	ctx := context.Background()
	seedStatsMirrorRecord(t, stats, "m-old-1", "2026-01-01T00:00:00.000Z")
	seedStatsMirrorRecord(t, stats, "m-old-2", "2026-01-02T00:00:00.000Z")
	seedStatsMirrorGlobalCursor(t, stats, "usage_stats_aggregation", "2026-01-05T00:00:00.000Z", "rec-1")

	batch, err := store.CleanupProcessedBefore(ctx, "2026-01-05T00:00:00.000Z", 100)
	if err != nil {
		t.Fatalf("CleanupProcessedBefore: %v", err)
	}
	if batch.DeletedRows != 0 || !strings.Contains(batch.BlockedReason, "聚合安全游标尚未建立") {
		t.Fatalf("单游标应阻塞: %+v", batch)
	}
	if got := len(statsMirrorRemainingIDs(t, stats)); got != 2 {
		t.Fatalf("阻塞时不得删除，残余 = %d", got)
	}

	seedStatsMirrorGlobalCursor(t, stats, "client_ip_stats_aggregation", "2026-01-03T00:00:00.000Z", "rec-0")
	batch, err = store.CleanupProcessedBefore(ctx, "2026-01-05T00:00:00.000Z", 100)
	if err != nil || batch.DeletedRows != 2 || batch.BlockedReason != "" {
		t.Fatalf("补齐游标后应放行: %+v, %v", batch, err)
	}
	if got := len(statsMirrorRemainingIDs(t, stats)); got != 0 {
		t.Fatalf("放行后应清空，残余 = %d", got)
	}
}

// TestStatsMirrorRetentionNoBlockerWithoutExpiredRows：游标未建立但不存在
// 超期行（新装/全量新鲜）→ 空批直通，不误报阻塞。
func TestStatsMirrorRetentionNoBlockerWithoutExpiredRows(t *testing.T) {
	store, stats := newStatsMirrorFixture(t)
	ctx := context.Background()
	seedStatsMirrorRecord(t, stats, "m-fresh", "2026-09-01T00:00:00.000Z")

	batch, err := store.CleanupProcessedBefore(ctx, "2026-01-02T00:00:00.000Z", 100)
	if err != nil || batch.DeletedRows != 0 || batch.BlockedReason != "" || batch.HasMore {
		t.Fatalf("无超期行应直通: %+v, %v", batch, err)
	}
	if got := len(statsMirrorRemainingIDs(t, stats)); got != 1 {
		t.Fatalf("新鲜行必须保留，残余 = %d", got)
	}
}

// TestStatsMirrorRetentionBatchHasMore：批上限截断 + HasMore 驱动多批收敛
// （DataRetentionJob 的 maxBatches 循环语义依赖该信号）。
func TestStatsMirrorRetentionBatchHasMore(t *testing.T) {
	store, stats := newStatsMirrorFixture(t)
	ctx := context.Background()
	seedStatsMirrorGlobalCursor(t, stats, "usage_stats_aggregation", "2026-02-01T00:00:00.000Z", "zz")
	seedStatsMirrorGlobalCursor(t, stats, "client_ip_stats_aggregation", "2026-02-01T00:00:00.000Z", "zz")
	for _, id := range []string{"m-a", "m-b", "m-c"} {
		seedStatsMirrorRecord(t, stats, id, "2026-01-01T00:00:00.000Z")
	}

	var total int64
	for round := 0; round < 3; round++ {
		batch, err := store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 1)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if batch.DeletedRows != 1 {
			t.Fatalf("round %d DeletedRows = %d, 期望 1", round, batch.DeletedRows)
		}
		total += batch.DeletedRows
		if want := round < 2; batch.HasMore != want {
			t.Fatalf("round %d HasMore = %v, 期望 %v", round, batch.HasMore, want)
		}
	}
	if total != 3 {
		t.Fatalf("total = %d", total)
	}
	if got := len(statsMirrorRemainingIDs(t, stats)); got != 0 {
		t.Fatalf("多批后应清空，残余 = %d", got)
	}
}

// TestUsageRecordsCleanupSQLiteMergesShardAndMirrorHalves：global 双游标齐备
// 时分片半区与镜像半区同一批各自删除，DeletedRows 相加、HasMore 取或。
func TestUsageRecordsCleanupSQLiteMergesShardAndMirrorHalves(t *testing.T) {
	f := newKitRecordFixture(t)
	store := &UsageRecordsStore{Catalog: f.catalog, Stats: f.stats, Shards: f.shards}
	ctx := context.Background()
	seedStatsMirrorGlobalCursor(t, f.stats, "usage_stats_aggregation", "2026-02-01T00:00:00.000Z", "zz")
	seedStatsMirrorGlobalCursor(t, f.stats, "client_ip_stats_aggregation", "2026-02-01T00:00:00.000Z", "zz")
	// 分片半区候选：一条超期分片行；镜像半区候选：两条超期镜像行。
	f.addKitShard(t, "sk-merge", "2026-01-05", 1, "key-1", "sys-1", "acc-1", false, []statsagg.UsageStatsRecordRow{
		kitUsageRecord("rec-shard", "sys-1", "key-1", "acc-1", "2026-01-01T00:00:00.000Z"),
	})
	seedStatsMirrorRecord(t, f.stats, "m-1", "2026-01-01T00:00:00.000Z")
	seedStatsMirrorRecord(t, f.stats, "m-2", "2026-01-02T00:00:00.000Z")

	// batch=1：分片半区删 1（HasMore=false），镜像半区删 1（HasMore=true）。
	batch, err := store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 1)
	if err != nil {
		t.Fatalf("CleanupProcessedBefore: %v", err)
	}
	if batch.DeletedRows != 2 || !batch.HasMore || batch.BlockedReason != "" {
		t.Fatalf("合并批次 = %+v", batch)
	}
	// 第二批清空镜像剩余行，HasMore 归零。
	batch, err = store.CleanupProcessedBefore(ctx, "2026-09-10T00:00:00.000Z", 1)
	if err != nil || batch.DeletedRows != 1 || batch.HasMore || batch.BlockedReason != "" {
		t.Fatalf("收尾批次 = %+v, %v", batch, err)
	}
	if got := mustQueryCountKit(t, f.stats, `SELECT COUNT(*) FROM usage_records`); got != 0 {
		t.Fatalf("镜像应清空，残余 = %d", got)
	}
	if got := mustQueryCountKit(t, f.catalog, `SELECT COUNT(*) FROM usage_record_shard_entries`); got != 0 {
		t.Fatalf("分片目录条目应清空，残余 = %d", got)
	}
}
