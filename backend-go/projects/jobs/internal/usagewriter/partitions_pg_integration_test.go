package usagewriter

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestEnsurePostgresUsageRecordPartitionsAgainstRealPG runs the partition
// ensure chain against a live PostgreSQL (dev juhe_ai_sub2api_dev). Gated on
// JUHE_AI_USAGE_WRITER_PARTITION_PG_URL; without it the test skips. The DDL
// is additive and idempotent (CREATE TABLE IF NOT EXISTS ... PARTITION OF),
// and the test only touches usage_records daily partitions.
func TestEnsurePostgresUsageRecordPartitionsAgainstRealPG(t *testing.T) {
	postgresURL := os.Getenv("JUHE_AI_USAGE_WRITER_PARTITION_PG_URL")
	if postgresURL == "" {
		t.Skip("requires JUHE_AI_USAGE_WRITER_PARTITION_PG_URL (isolated/dev PG)")
	}
	db, err := sql.Open("pgx", postgresURL)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping pg: %v", err)
	}

	// The partitioned parent must exist in the dev database.
	var parentRelKind string
	err = db.QueryRowContext(ctx,
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
	fmt.Printf("partition ensure verified against live PG for %s / %s\n", today, tomorrow)
}
