package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
)

// dataretention.go 的语义测试：public_api_logs / system_sessions 保留清理、
// stats+metrics 保留、非业务数据硬清理（stats/dataset/usage-catalog 三半区）、
// 使用记录安全游标清理（SQLite 真库 + PG 录制）。

func createKitMinimalTable(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	execKitSchema(t, db, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS "%s" ("%s" TEXT)`, table, column))
}

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

// TestUsageRecordPartitionDayStart：分区名到 UTC 日的解析与非法日期拒绝。
func TestUsageRecordPartitionDayStart(t *testing.T) {
	start, ok := usageRecordPartitionDayStart("usage_records_20260105")
	if !ok || start.Format("2006-01-02") != "2026-01-05" {
		t.Fatalf("start = %v, ok = %v", start, ok)
	}
	for _, value := range []string{"", "abc", "usage_records_20260230", "usage_records_20261301", "usage_records_other"} {
		if _, ok := usageRecordPartitionDayStart(value); ok {
			t.Fatalf("usageRecordPartitionDayStart(%q) 应拒绝", value)
		}
	}
}

func TestDropEligiblePartitions(t *testing.T) {
	rec := newPGRecorder()
	store := &UsageRecordsStore{Catalog: openRecorderPG(rec), Stats: openRecorderPG(rec)}
	rec.script("FROM pg_inherits inherit", []string{"partition_name", "partition_bound"},
		[][]driver.Value{
			{"usage_records_20260101", "FOR VALUES FROM ('2025-12-31') TO ('2026-01-01')"}, // 名字与数据不一致
			{"usage_records_20260102", "FOR VALUES FROM ('2026-01-02') TO ('2026-01-03')"},
			{"usage_records_20260103", "FOR VALUES FROM ('2026-01-03') TO ('2026-01-04')"},
			{"usage_records_other", "FOR VALUES FROM ('2026-01-03') TO ('2026-01-04')"}, // 命名不匹配
			{"usage_records_20260999", "FOR VALUES FROM ('2026-09-01') TO ('2026-09-02')"},
		})
	// 20260101 的数据守卫失败：min 落在名字日之前，跳过并告警。
	rec.script(`FROM juhe_usage."usage_records_20260101"`,
		[]string{"total", "pending", "min_created", "max_created"},
		[][]driver.Value{{int64(1), int64(0), "2025-12-31T23:59:59.000Z", "2026-01-01T00:00:00.000Z"}})
	// 20260102 通过守卫，成为目标。
	rec.script(`FROM juhe_usage."usage_records_20260102"`,
		[]string{"total", "pending", "min_created", "max_created"},
		[][]driver.Value{{int64(5), int64(0), "2026-01-02T00:00:00.000Z", "2026-01-02T23:59:59.000Z"}})

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
	if !strings.Contains(allText, `ALTER TABLE juhe_usage.usage_records DETACH PARTITION juhe_usage."usage_records_20260102"`) {
		t.Fatalf("缺少 DETACH 语句：\n%s", allText)
	}
	if !strings.Contains(allText, `DROP TABLE IF EXISTS juhe_usage."usage_records_20260102"`) {
		t.Fatalf("缺少 DROP 语句")
	}
	if !strings.Contains(allText, `SET LOCAL lock_timeout = '2s'`) {
		t.Fatalf("缺少 lock_timeout")
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
