package runtimelog

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// w12d_sqlite_cleanup_test.go 覆盖 SQLite Cleanup 的轮转游标清理与 facet
// 减计路径（本地临时库）。

func TestW12dSQLiteCleanupRotatedCursorsAndFacets(t *testing.T) {
	store, config := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	oldTime := "2026-08-01T00:00:00.000Z"
	// 旧 runtime_logs 行 + facet 汇总行。
	statements := []string{
		`INSERT INTO runtime_logs (id, log_file, log_offset, line_number, time, level, trace_id, event, message, error_message, raw_json, created_at)
			VALUES ('w12d-clean-1', 'juhe-ai.20260801T000000Z.a1b2.log', 5, 1, '` + oldTime + `', 'info', '', 'w12d-clean', 'm', '', '{}', '` + oldTime + `')`,
		`INSERT INTO runtime_log_facet_summary (bucket_key, total_count, earliest_time, latest_time, updated_at)
			VALUES ('current', 1, '` + oldTime + `', '` + oldTime + `', '` + oldTime + `')`,
		`INSERT INTO runtime_log_level_facets (bucket_key, level, count, updated_at)
			VALUES ('current', 'info', 1, '` + oldTime + `')`,
		`INSERT INTO runtime_log_event_facets (bucket_key, event, count, latest_time, updated_at)
			VALUES ('current', 'w12d-clean', 1, '` + oldTime + `', '` + oldTime + `')`,
		// 已读完且过期的轮转文件游标（会被清理）。
		`INSERT INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at)
			VALUES ('` + filepath.Join(config.LogDirectory, "juhe-ai.20260801T000000Z.a1b2.log") + `', 'ident-1', 100, 1, 100, 0, 0, '` + oldTime + `', NULL, '` + oldTime + `', '2026-08-01T00:00:00.000Z')`,
		// 未读完的轮转游标（保留）。
		`INSERT INTO runtime_log_file_cursors (log_file, file_identity, cursor_offset, line_number, file_size, truncation_generation, file_mtime_ms, last_read_at, last_error_message, created_at, updated_at)
			VALUES ('` + filepath.Join(config.LogDirectory, "juhe-ai.20260801T000100Z.c2d3.log") + `', 'ident-2', 10, 1, 100, 0, 0, '` + oldTime + `', NULL, '` + oldTime + `', '2026-08-01T00:00:00.000Z')`,
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Cleanup(ctx, lease, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 10, 10)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if result.RuntimeLogs != 1 {
		t.Fatalf("runtime logs deleted=%d", result.RuntimeLogs)
	}
	if result.RuntimeLogCursors != 1 {
		t.Fatalf("rotated cursors deleted=%d", result.RuntimeLogCursors)
	}
	// facet 已随行删除而减计到 0。
	var facetCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runtime_log_level_facets WHERE level = 'info'`).Scan(&facetCount); err != nil {
		t.Fatal(err)
	}
	if facetCount != 0 {
		t.Fatalf("level facets remain=%d", facetCount)
	}
}

func TestW12dSQLiteCleanupEmptyStore(t *testing.T) {
	store, _ := openTestSQLiteStore(t)
	if err := EnsureSchema(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	ctx := testOwnerContext(t, store)
	lease := testOwnerLease(t, ctx)
	result, err := store.Cleanup(ctx, lease, time.Now().UTC(), 10, 3)
	if err != nil {
		t.Fatalf("empty cleanup: %v", err)
	}
	if result.RuntimeLogs != 0 || result.RuntimeLogCursors != 0 {
		t.Fatalf("result=%+v", result)
	}
}
