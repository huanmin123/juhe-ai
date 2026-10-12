package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func w1SuccessCostWALReadMarkers(t *testing.T, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) map[string][2]string {
	t.Helper()
	rows, err := queryer.QueryContext(context.Background(), "SELECT table_name, status, cursor_json FROM backfill_progress")
	if err != nil {
		t.Fatalf("read backfill markers: %v", err)
	}
	defer rows.Close()
	markers := map[string][2]string{}
	for rows.Next() {
		var table, status, cursor string
		if err := rows.Scan(&table, &status, &cursor); err != nil {
			t.Fatalf("scan backfill marker: %v", err)
		}
		markers[table] = [2]string{status, cursor}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate backfill markers: %v", err)
	}
	return markers
}

func w1SuccessCostWALReadTotal(t *testing.T, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (errorCount int, totalCost, successCost float64) {
	t.Helper()
	if err := queryer.QueryRowContext(context.Background(), `SELECT error_count, total_cost_usd, success_cost_usd
		FROM usage_stats_totals WHERE system_account_id = 'wal-u1' AND scope_type = 'system_account' AND scope_id = 'wal-u1'`).
		Scan(&errorCount, &totalCost, &successCost); err != nil {
		t.Fatalf("read WAL fixture fact: %v", err)
	}
	return errorCount, totalCost, successCost
}

func w1SuccessCostWALReadJournalMode(t *testing.T, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) string {
	t.Helper()
	var mode string
	if err := queryer.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read SQLite journal_mode: %v", err)
	}
	return strings.ToLower(mode)
}

func w1SuccessCostWALAssertBytesUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SQLite file %s after read-only access: %v", path, err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("read-only access changed bytes in %s", path)
	}
}

func TestW1SuccessCostWALReadOnlyAfterCleanClose(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	backfillInsertTotal(t, db, "wal-u1", "system_account", "wal-u1", 0, 10, 0)
	for _, target := range successCostBackfillTargets {
		status := "done"
		if target.name == "usage_stats_totals" {
			status = "running"
		}
		backfillPresetMarker(t, db, target.name, status)
	}
	markersBefore := backfillMarkerRows(t, db)
	if mode := w1SuccessCostWALReadJournalMode(t, db); mode != "wal" {
		t.Fatalf("bootstrap fixture journal_mode=%q, want wal", mode)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close clean WAL fixture: %v", err)
	}

	for _, sidecar := range []string{statsPath + "-wal", statsPath + "-shm"} {
		if _, err := os.Stat(sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("clean close should leave no sidecar before read-only open: %s, err=%v", sidecar, err)
		}
	}
	mainBefore, err := os.ReadFile(statsPath)
	if err != nil {
		t.Fatalf("read cleanly closed stats database: %v", err)
	}

	pending, present, err := pendingBackfillTablesSQLite(context.Background(), statsPath)
	if err != nil {
		t.Fatalf("read-only pending probe of clean WAL: %v", err)
	}
	if !present || !reflect.DeepEqual(pending, []string{"usage_stats_totals"}) {
		t.Fatalf("clean WAL pending probe=%v present=%v, want totals only/present", pending, present)
	}
	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-dry-run", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("clean WAL dry-run exit=%d, stderr=%s", code, stderr)
	}
	if got := report.Tables["usage_stats_totals"]; got.Status != "dry-run" || got.MatchedRows != 1 || got.Batches != 0 {
		t.Fatalf("clean WAL dry-run totals=%+v, want dry-run/1/0", got)
	}

	ro, err := openReadOnlySuccessCostSQLite(statsPath)
	if err != nil {
		t.Fatalf("reopen clean WAL read-only: %v", err)
	}
	if mode := w1SuccessCostWALReadJournalMode(t, ro); mode != "wal" {
		t.Fatalf("read-only access changed clean WAL journal_mode to %q", mode)
	}
	var queryOnly int
	if err := ro.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		t.Fatalf("query_only=%d err=%v, want 1", queryOnly, err)
	}
	if errorCount, total, success := w1SuccessCostWALReadTotal(t, ro); errorCount != 0 || total != 10 || success != 0 {
		t.Fatalf("clean WAL fact=(%d,%v,%v), want unchanged (0,10,0)", errorCount, total, success)
	}
	if markersAfter := w1SuccessCostWALReadMarkers(t, ro); !reflect.DeepEqual(markersAfter, markersBefore) {
		t.Fatalf("read-only probe/dry-run changed markers: before=%v after=%v", markersBefore, markersAfter)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("close read-only clean WAL handle: %v", err)
	}
	w1SuccessCostWALAssertBytesUnchanged(t, statsPath, mainBefore)
}

func TestW1SuccessCostWALReadOnlySeesUncheckpointedCommit(t *testing.T) {
	statsPath, pathsFlag := backfillFixturePaths(t)
	writer := openRebuildSQLite(t, statsPath)
	defer writer.Close()
	writerConn, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire active SQLite writer connection: %v", err)
	}
	defer writerConn.Close()
	if mode := w1SuccessCostWALReadJournalMode(t, writerConn); mode != "wal" {
		t.Fatalf("writer journal_mode=%q, want wal", mode)
	}
	if _, err := writerConn.ExecContext(context.Background(), "PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatalf("disable WAL auto-checkpoint on fixture writer: %v", err)
	}
	var autocheckpoint int
	if err := writerConn.QueryRowContext(context.Background(), "PRAGMA wal_autocheckpoint").Scan(&autocheckpoint); err != nil || autocheckpoint != 0 {
		t.Fatalf("wal_autocheckpoint=%d err=%v, want 0", autocheckpoint, err)
	}
	mainBeforeCommit, err := os.ReadFile(statsPath)
	if err != nil {
		t.Fatalf("snapshot main database before commit: %v", err)
	}
	walPath := statsPath + "-wal"
	walBeforeCommit, err := os.ReadFile(walPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot WAL before commit: %v", err)
	}

	tx, err := writerConn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin uncheckpointed fixture transaction: %v", err)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO usage_stats_totals
		(system_account_id, scope_type, scope_id, error_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('wal-u1', 'system_account', 'wal-u1', 0, 10, 0, '2026-10-12T00:00:00.000Z')`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert uncheckpointed fact: %v", err)
	}
	for _, target := range successCostBackfillTargets {
		status := "done"
		if target.name == "usage_stats_totals" {
			status = "running"
		}
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO backfill_progress
			(table_name, status, cursor_json, updated_at) VALUES (?, ?, '{}', '2026-10-12T00:00:00.000Z')`, target.name, status); err != nil {
			_ = tx.Rollback()
			t.Fatalf("insert uncheckpointed marker %s: %v", target.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit uncheckpointed fixture rows: %v", err)
	}

	mainAfterCommit, err := os.ReadFile(statsPath)
	if err != nil {
		t.Fatalf("snapshot main database after WAL commit: %v", err)
	}
	if !reflect.DeepEqual(mainAfterCommit, mainBeforeCommit) {
		t.Fatal("fixture commit was checkpointed into the main database despite wal_autocheckpoint=0")
	}
	walBefore, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("read committed WAL: %v", err)
	}
	if len(walBefore) <= 32 {
		t.Fatalf("committed WAL size=%d, want header plus uncheckpointed frames", len(walBefore))
	}
	if len(walBefore) <= len(walBeforeCommit) {
		t.Fatalf("WAL did not grow for committed fixture rows: before=%d after=%d", len(walBeforeCommit), len(walBefore))
	}
	if len(walBeforeCommit) > 0 && !reflect.DeepEqual(walBefore[:len(walBeforeCommit)], walBeforeCommit) {
		t.Fatal("fixture commit rewrote the WAL prefix instead of appending uncheckpointed frames")
	}
	markersBefore := w1SuccessCostWALReadMarkers(t, writerConn)
	factErrBefore, factTotalBefore, factSuccessBefore := w1SuccessCostWALReadTotal(t, writerConn)

	pending, present, err := pendingBackfillTablesSQLite(context.Background(), statsPath)
	if err != nil {
		t.Fatalf("read-only pending probe with active WAL writer: %v", err)
	}
	if !present || !reflect.DeepEqual(pending, []string{"usage_stats_totals"}) {
		t.Fatalf("active WAL pending probe=%v present=%v, want totals only/present", pending, present)
	}
	code, report, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-dry-run", "-driver", "sqlite", "-paths", pathsFlag)
	if code != 0 {
		t.Fatalf("active WAL dry-run exit=%d, stderr=%s", code, stderr)
	}
	for _, target := range successCostBackfillTargets {
		want := 0
		if target.name == "usage_stats_totals" {
			want = 1
		}
		if got := report.Tables[target.name]; got.Status != "dry-run" || got.MatchedRows != want || got.Batches != 0 {
			t.Fatalf("active WAL dry-run %s=%+v, want dry-run/%d/0", target.name, got, want)
		}
	}

	ro, err := openReadOnlySuccessCostSQLite(statsPath)
	if err != nil {
		t.Fatalf("open active WAL read-only handle: %v", err)
	}
	if mode := w1SuccessCostWALReadJournalMode(t, ro); mode != "wal" {
		t.Fatalf("read-only access changed active WAL journal_mode to %q", mode)
	}
	if errorCount, total, success := w1SuccessCostWALReadTotal(t, ro); errorCount != 0 || total != 10 || success != 0 {
		t.Fatalf("read-only handle did not see committed WAL fact: (%d,%v,%v)", errorCount, total, success)
	}
	if markersAfter := w1SuccessCostWALReadMarkers(t, ro); !reflect.DeepEqual(markersAfter, markersBefore) {
		t.Fatalf("read-only probe/dry-run changed committed markers: before=%v after=%v", markersBefore, markersAfter)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("close active WAL read-only handle: %v", err)
	}
	if errCount, total, success := w1SuccessCostWALReadTotal(t, writerConn); errCount != factErrBefore || total != factTotalBefore || success != factSuccessBefore {
		t.Fatalf("read-only access changed fact: before=(%d,%v,%v) after=(%d,%v,%v)", factErrBefore, factTotalBefore, factSuccessBefore, errCount, total, success)
	}
	if markersAfter := w1SuccessCostWALReadMarkers(t, writerConn); !reflect.DeepEqual(markersAfter, markersBefore) {
		t.Fatalf("read-only access changed writer-visible markers: before=%v after=%v", markersBefore, markersAfter)
	}
	w1SuccessCostWALAssertBytesUnchanged(t, statsPath, mainAfterCommit)
	w1SuccessCostWALAssertBytesUnchanged(t, walPath, walBefore)
	// SQLite may create or update the shared-memory lock sidecar while coordinating
	// a read-only connection with the active WAL writer; it is outside byte invariants.
}

func TestW1SuccessCostWALReadOnlyProbeDoesNotCreateMissingPath(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "not-created")
	statsPath := filepath.Join(missingDir, "stats.sqlite3")
	_, present, err := pendingBackfillTablesSQLite(context.Background(), statsPath)
	if err == nil || present {
		t.Fatalf("missing read-only probe should fail with present=false, present=%v err=%v", present, err)
	}
	if _, err := os.Stat(statsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read-only probe created database, stat err=%v", err)
	}
	if _, err := os.Stat(missingDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read-only probe created parent directory, stat err=%v", err)
	}
}
