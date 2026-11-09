package usagewriter

// TestEnsurePostgresUsageRecordPartitionsAgainstRealPG 在 pgtestkit 隔离
// PostgreSQL 测试库上运行分区确保链（PLAN-20261024T051925945Z 测试迁移切片；
// 原臂经 JUHE_AI_USAGE_WRITER_PARTITION_PG_URL 门禁直连 dev 库，现改为每
// 测试独立库：分区母表由 usagewriter_pgkit_test.go 按生产形状装配，PG 不可
// 达时显式失败而非 skip）。DDL 为加法幂等（CREATE TABLE IF NOT EXISTS ...
// PARTITION OF），断言与原臂逐字节一致。
import (
	"context"
	"testing"
	"time"
)

func TestEnsurePostgresUsageRecordPartitionsAgainstRealPG(t *testing.T) {
	db := openKitPostgresDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The partitioned parent must exist in the test database.
	var parentRelKind string
	err := db.QueryRowContext(ctx,
		`SELECT relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'juhe_usage' AND c.relname = 'usage_records'`).Scan(&parentRelKind)
	if err != nil {
		t.Fatalf("usage_records parent table lookup: %v", err)
	}
	if parentRelKind != "p" {
		t.Fatalf("usage_records relkind = %q, want 'p' (partitioned)", parentRelKind)
	}

	ensured := &ensuredPartitionDateKeys{}
	today := time.Now().UTC().Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	createdAts := []string{today + "T00:00:00.000Z", today + "T12:00:00.000Z", tomorrow + "T06:30:00.000Z"}

	// 首次确保：创建（或幂等命中已存在的）今日/明日分区。
	if err := ensurePostgresUsageRecordPartitions(ctx, db, ensured, createdAts); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	// 重复确保：进程内备忘 + IF NOT EXISTS 幂等。
	if err := ensurePostgresUsageRecordPartitions(ctx, db, ensured, createdAts); err != nil {
		t.Fatalf("repeat ensure must be idempotent: %v", err)
	}

	for _, dateKey := range []string{
		today[0:4] + today[5:7] + today[8:10],
		tomorrow[0:4] + tomorrow[5:7] + tomorrow[8:10],
	} {
		partitionName := "usage_records_" + dateKey
		var exists bool
		err = db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'juhe_usage' AND c.relname = $1 AND c.relispartition)`, partitionName).Scan(&exists)
		if err != nil {
			t.Fatalf("partition %s lookup: %v", partitionName, err)
		}
		if !exists {
			t.Fatalf("partition juhe_usage.%s must exist after ensure", partitionName)
		}
	}
}
