package retention

// media_jobs retention 真实 PG 门控测试：expired 行按 updated_at 再保留一个
// TTL（AC-022：终态、请求快照与审计信息不在终态化同轮被删除），其余终态行
// 仍按 created_at 超期删除。默认跳过；设置 JUHE_AI_TEST_EXTERNAL=1 执行。

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const mediaJobsRetentionSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE juhe_business.media_jobs (
  id text PRIMARY KEY,
  status text NOT NULL,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
`

func TestMediaJobsSQLStorePreservesFreshlyExpiredRows(t *testing.T) {
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, mediaJobsRetentionSchema)
		return err
	})
	for _, statement := range []string{
		`INSERT INTO juhe_business.media_jobs (id, status, created_at, updated_at) VALUES
		 ('old-completed', 'completed', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z'),
		 ('old-failed', 'failed', '2026-11-02T00:00:00.000Z', '2026-11-02T00:00:00.000Z'),
		 ('old-expired', 'expired', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z'),
		 ('fresh-expired', 'expired', '2026-11-12T00:00:00.000Z', '2026-11-19T00:00:00.000Z'),
		 ('old-queued', 'queued', '2026-11-01T00:00:00.000Z', '2026-11-01T00:00:00.000Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed media_jobs: %v", err)
		}
	}

	store := &MediaJobsSQLStore{DB: db, Postgres: true}
	cutoff := "2026-11-13T00:00:00.000Z"
	expired, err := store.ExpireNonTerminalBefore(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("expire non-terminal: %v", err)
	}
	if expired != 1 {
		t.Fatalf("expired non-terminal = %d, want 1", expired)
	}
	deleted, err := store.DeleteExpiredBefore(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("delete expired: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("deleted = %d, want 3（old-completed/old-failed/old-expired）", deleted)
	}

	rows, err := db.Query(`SELECT id, status FROM juhe_business.media_jobs ORDER BY id`)
	if err != nil {
		t.Fatalf("query remaining rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var remaining []string
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		remaining = append(remaining, id+":"+status)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rows: %v", err)
	}
	sort.Strings(remaining)
	want := []string{"fresh-expired:expired", "old-queued:expired"}
	if len(remaining) != len(want) {
		t.Fatalf("remaining = %v, want %v", remaining, want)
	}
	for index := range want {
		if remaining[index] != want[index] {
			t.Fatalf("remaining = %v, want %v", remaining, want)
		}
	}
}
