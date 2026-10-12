package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var errW1SuccessCostInjected = errors.New("w1 success cost injected failure")

type w1SuccessCostFaultConnector struct{ conn *w1SuccessCostFaultConn }

func (c w1SuccessCostFaultConnector) Connect(context.Context) (driver.Conn, error) {
	return c.conn, nil
}
func (c w1SuccessCostFaultConnector) Driver() driver.Driver { return w1SuccessCostFaultDriver{} }

type w1SuccessCostFaultDriver struct{}

func (w1SuccessCostFaultDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("w1 success cost fake requires sql.OpenDB")
}

type w1SuccessCostFaultConn struct {
	fault         string
	cause         error
	markerStatus  string
	markerCursor  string
	markerMissing bool
	emptyBatch    bool
	commits       int
	rollbacks     int
}

func (c *w1SuccessCostFaultConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w1 success cost fake does not support Prepare")
}
func (*w1SuccessCostFaultConn) Close() error { return nil }
func (c *w1SuccessCostFaultConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *w1SuccessCostFaultConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.fault == "begin" {
		return nil, c.cause
	}
	return &w1SuccessCostFaultTx{conn: c}, nil
}
func (c *w1SuccessCostFaultConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "SELECT status, cursor_json"):
		if c.fault == "marker-query" {
			return nil, c.cause
		}
		if c.markerMissing {
			return &w1SuccessCostFaultRows{columns: []string{"status", "cursor_json"}}, nil
		}
		status, cursor := c.markerStatus, c.markerCursor
		if status == "" {
			status = "running"
		}
		if cursor == "" {
			cursor = "{}"
		}
		return &w1SuccessCostFaultRows{
			columns: []string{"status", "cursor_json"},
			values:  [][]driver.Value{{status, cursor}},
		}, nil
	case strings.Contains(query, "SELECT system_account_id"):
		if c.fault == "batch-query" {
			return nil, c.cause
		}
		if c.emptyBatch {
			return &w1SuccessCostFaultRows{columns: []string{"system_account_id", "scope_type", "scope_id"}}, nil
		}
		values := [][]driver.Value{{"w1-account", "system_account", "w1-scope"}}
		var nextErr error
		if c.fault == "batch-scan" {
			values[0][0] = nil
		}
		if c.fault == "batch-iterate" {
			nextErr = c.cause
		}
		return &w1SuccessCostFaultRows{
			columns: []string{"system_account_id", "scope_type", "scope_id"},
			values:  values,
			nextErr: nextErr,
		}, nil
	default:
		return nil, fmt.Errorf("w1 success cost fake: unexpected query %q", query)
	}
}
func (c *w1SuccessCostFaultConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	switch {
	case strings.HasPrefix(query, "INSERT INTO"):
		if c.fault == "marker-insert" {
			return nil, c.cause
		}
		return w1SuccessCostFaultResult{affected: 1}, nil
	case strings.Contains(query, "SET status = 'done'"):
		if c.fault == "done-exec" {
			return nil, c.cause
		}
		if c.fault == "done-rows" {
			return w1SuccessCostFaultResult{affectedErr: c.cause}, nil
		}
		if c.fault == "done-cas" {
			return w1SuccessCostFaultResult{affected: 0}, nil
		}
		return w1SuccessCostFaultResult{affected: 1}, nil
	case strings.Contains(query, "SET success_cost_usd"):
		if c.fault == "batch-update" {
			return nil, c.cause
		}
		return w1SuccessCostFaultResult{affected: 1}, nil
	case strings.Contains(query, "SET cursor_json"):
		if c.fault == "advance-exec" {
			return nil, c.cause
		}
		if c.fault == "advance-rows" {
			return w1SuccessCostFaultResult{affectedErr: c.cause}, nil
		}
		return w1SuccessCostFaultResult{affected: 1}, nil
	default:
		return nil, fmt.Errorf("w1 success cost fake: unexpected exec %q", query)
	}
}

type w1SuccessCostFaultTx struct{ conn *w1SuccessCostFaultConn }

func (tx *w1SuccessCostFaultTx) Commit() error {
	tx.conn.commits++
	if tx.conn.fault == "commit" || tx.conn.fault == "done-commit" {
		return tx.conn.cause
	}
	return nil
}
func (tx *w1SuccessCostFaultTx) Rollback() error {
	tx.conn.rollbacks++
	return nil
}

type w1SuccessCostFaultResult struct {
	affected    int64
	affectedErr error
}

func (w1SuccessCostFaultResult) LastInsertId() (int64, error) { return 0, nil }
func (r w1SuccessCostFaultResult) RowsAffected() (int64, error) {
	return r.affected, r.affectedErr
}

type w1SuccessCostFaultRows struct {
	columns []string
	values  [][]driver.Value
	nextErr error
}

func (r *w1SuccessCostFaultRows) Columns() []string { return r.columns }
func (*w1SuccessCostFaultRows) Close() error        { return nil }
func (r *w1SuccessCostFaultRows) Next(dest []driver.Value) error {
	if len(r.values) > 0 {
		copy(dest, r.values[0])
		r.values = r.values[1:]
		return nil
	}
	if r.nextErr != nil {
		err := r.nextErr
		r.nextErr = nil
		return err
	}
	return io.EOF
}

func w1SuccessCostFaultDB(conn *w1SuccessCostFaultConn) *sql.DB {
	db := sql.OpenDB(w1SuccessCostFaultConnector{conn: conn})
	db.SetMaxOpenConns(1)
	return db
}

func w1SuccessCostTarget() successCostBackfillTarget {
	return successCostBackfillTarget{
		name:      "usage_stats_totals",
		pkColumns: []string{"system_account_id", "scope_type", "scope_id"},
	}
}

func TestW1BackfillSuccessCostBatchFaultsPreserveErrorsAndRollback(t *testing.T) {
	tests := []struct {
		name         string
		fault        string
		emptyBatch   bool
		wantContains string
		wantCommit   int
		wantRollback int
		wantCause    bool
	}{
		{name: "begin", fault: "begin", wantCause: true},
		{name: "query", fault: "batch-query", wantCause: true, wantRollback: 1},
		{name: "scan", fault: "batch-scan", wantContains: "scan usage_stats_totals batch row", wantRollback: 1},
		{name: "iterate", fault: "batch-iterate", wantCause: true, wantRollback: 1},
		{name: "update", fault: "batch-update", wantCause: true, wantRollback: 1},
		{name: "advance exec", fault: "advance-exec", wantCause: true, wantRollback: 1},
		{name: "advance rows affected", fault: "advance-rows", wantCause: true, wantRollback: 1},
		{name: "commit", fault: "commit", wantCause: true, wantCommit: 1},
		{name: "done exec", fault: "done-exec", emptyBatch: true, wantCause: true, wantRollback: 1},
		{name: "done rows affected", fault: "done-rows", emptyBatch: true, wantCause: true, wantRollback: 1},
		{name: "done CAS", fault: "done-cas", emptyBatch: true, wantContains: "cursor CAS conflict while marking", wantRollback: 1},
		{name: "done commit", fault: "done-commit", emptyBatch: true, wantCause: true, wantCommit: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn := &w1SuccessCostFaultConn{
				fault:        tc.fault,
				cause:        errW1SuccessCostInjected,
				markerStatus: "running",
				markerCursor: "{}",
				emptyBatch:   tc.emptyBatch,
			}
			db := w1SuccessCostFaultDB(conn)
			defer db.Close()

			_, _, _, err := backfillSuccessCostBatch(context.Background(), db, backfillDialect{}, w1SuccessCostTarget(), nil, "{}", "2026-10-12T00:00:00.000Z")
			if err == nil {
				t.Fatal("预期批事务返回错误")
			}
			if tc.wantCause && !errors.Is(err, errW1SuccessCostInjected) {
				t.Fatalf("原始注入错误未保留: %v", err)
			}
			if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
				t.Fatalf("错误 %q 不含 %q", err, tc.wantContains)
			}
			if conn.commits != tc.wantCommit || conn.rollbacks != tc.wantRollback {
				t.Fatalf("commit/rollback = %d/%d, want %d/%d", conn.commits, conn.rollbacks, tc.wantCommit, tc.wantRollback)
			}
		})
	}
}

func TestW1BackfillSuccessCostTableMarkerFaultsAndInvalidStates(t *testing.T) {
	tests := []struct {
		name          string
		fault         string
		markerStatus  string
		markerCursor  string
		markerMissing bool
		wantCause     bool
		wantContains  string
	}{
		{name: "marker query", fault: "marker-query", wantCause: true},
		{name: "marker insert", fault: "marker-insert", markerMissing: true, wantCause: true},
		{name: "unknown status", markerStatus: "paused", wantContains: `unknown status "paused"`},
		{name: "malformed cursor", markerStatus: "running", markerCursor: "{broken", wantContains: "decode cursor"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conn := &w1SuccessCostFaultConn{
				fault:         tc.fault,
				cause:         errW1SuccessCostInjected,
				markerStatus:  tc.markerStatus,
				markerCursor:  tc.markerCursor,
				markerMissing: tc.markerMissing,
			}
			db := w1SuccessCostFaultDB(conn)
			defer db.Close()

			_, err := backfillSuccessCostTable(context.Background(), db, backfillDialect{}, w1SuccessCostTarget())
			if err == nil {
				t.Fatal("预期 marker 错误或非法状态被拒绝")
			}
			if tc.wantCause && !errors.Is(err, errW1SuccessCostInjected) {
				t.Fatalf("原始注入错误未保留: %v", err)
			}
			if tc.wantContains != "" && !strings.Contains(err.Error(), tc.wantContains) {
				t.Fatalf("错误 %q 不含 %q", err, tc.wantContains)
			}
			if conn.commits != 0 || conn.rollbacks != 0 {
				t.Fatalf("marker 前置失败不应启动批事务: commit/rollback=%d/%d", conn.commits, conn.rollbacks)
			}
		})
	}
}

func TestW1BackfillSuccessCostTableSkipsDoneMarker(t *testing.T) {
	conn := &w1SuccessCostFaultConn{markerStatus: "done", markerCursor: "not-decoded-for-done"}
	db := w1SuccessCostFaultDB(conn)
	defer db.Close()

	stat, err := backfillSuccessCostTable(context.Background(), db, backfillDialect{}, w1SuccessCostTarget())
	if err != nil {
		t.Fatalf("done marker should skip: %v", err)
	}
	if stat.Status != "done" || stat.Batches != 0 || stat.MatchedRows != 0 {
		t.Fatalf("done marker stat=%+v, want done with no work", stat)
	}
	if conn.commits != 0 || conn.rollbacks != 0 {
		t.Fatalf("done marker must not start a batch transaction: commit/rollback=%d/%d", conn.commits, conn.rollbacks)
	}
}

func TestW1BackfillSuccessCostResumesAfterSecondBatchRollback(t *testing.T) {
	statsPath, _ := backfillFixturePaths(t)
	db := openRebuildSQLite(t, statsPath)
	defer db.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin fixture insert: %v", err)
	}
	insert, err := tx.Prepare(`INSERT INTO usage_stats_totals
		(system_account_id, scope_type, scope_id, error_count, total_cost_usd, success_cost_usd, updated_at)
		VALUES ('same-account', 'system_account', ?, 0, 10, 0, '2026-10-12T00:00:00.000Z')`)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("prepare fixture insert: %v", err)
	}
	for i := 0; i <= successCostBackfillBatchSize; i++ {
		if _, err := insert.Exec(fmt.Sprintf("scope-%05d", i)); err != nil {
			_ = insert.Close()
			_ = tx.Rollback()
			t.Fatalf("insert fixture row %d: %v", i, err)
		}
	}
	if err := insert.Close(); err != nil {
		_ = tx.Rollback()
		t.Fatalf("close fixture insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixture rows: %v", err)
	}

	if _, err := db.Exec(`CREATE TRIGGER w1_fail_second_success_cost_batch
		BEFORE UPDATE OF success_cost_usd ON usage_stats_totals
		WHEN OLD.scope_id = 'scope-05000'
		BEGIN
			SELECT RAISE(ABORT, 'w1 injected second batch failure');
		END`); err != nil {
		t.Fatalf("create second-batch failure trigger: %v", err)
	}

	stat, err := backfillSuccessCostTable(context.Background(), db, backfillDialect{}, w1SuccessCostTarget())
	if err == nil || !strings.Contains(err.Error(), "w1 injected second batch failure") {
		t.Fatalf("second batch 应返回 trigger 原始故障，stat=%+v err=%v", stat, err)
	}
	if stat.Batches != 1 || stat.MatchedRows != successCostBackfillBatchSize {
		t.Fatalf("失败后已提交批次统计=%+v, want one batch and %d rows", stat, successCostBackfillBatchSize)
	}
	markers := backfillMarkerRows(t, db)
	wantCursor := `[` + `"same-account","system_account","scope-04999"` + `]`
	if markers["usage_stats_totals"] != [2]string{"running", wantCursor} {
		t.Fatalf("第二批失败后 marker=%v, want running cursor %s", markers["usage_stats_totals"], wantCursor)
	}
	var updated int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_stats_totals WHERE success_cost_usd = total_cost_usd`).Scan(&updated); err != nil {
		t.Fatalf("count first batch rows: %v", err)
	}
	if updated != successCostBackfillBatchSize {
		t.Fatalf("第一批提交行数=%d, want %d", updated, successCostBackfillBatchSize)
	}
	var lastSuccess float64
	if err := db.QueryRow(`SELECT success_cost_usd FROM usage_stats_totals WHERE scope_id = 'scope-05000'`).Scan(&lastSuccess); err != nil {
		t.Fatalf("read second batch row: %v", err)
	}
	if lastSuccess != 0 {
		t.Fatalf("第二批失败后末行 success_cost_usd=%v, want 0", lastSuccess)
	}

	if _, err := db.Exec(`DROP TRIGGER w1_fail_second_success_cost_batch`); err != nil {
		t.Fatalf("drop second-batch failure trigger: %v", err)
	}
	stat, err = backfillSuccessCostTable(context.Background(), db, backfillDialect{}, w1SuccessCostTarget())
	if err != nil {
		t.Fatalf("resume from committed cursor: %v", err)
	}
	if stat.Status != "done" || stat.Batches != 1 || stat.MatchedRows != 1 {
		t.Fatalf("恢复统计=%+v, want done/1/1", stat)
	}
	if markers := backfillMarkerRows(t, db); markers["usage_stats_totals"] != [2]string{"done", "{}"} {
		t.Fatalf("恢复后 marker=%v, want done/{}", markers["usage_stats_totals"])
	}
	if err := db.QueryRow(`SELECT success_cost_usd FROM usage_stats_totals WHERE scope_id = 'scope-05000'`).Scan(&lastSuccess); err != nil {
		t.Fatalf("read resumed row: %v", err)
	}
	if lastSuccess != 10 {
		t.Fatalf("恢复后末行 success_cost_usd=%v, want 10", lastSuccess)
	}
}

func TestW1ApplySuccessCostBackfillCLIPathAndDatabaseFailures(t *testing.T) {
	t.Run("bad paths", func(t *testing.T) {
		code, _, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", "stats=")
		if code != 2 || !strings.Contains(stderr, "必须是 key=value") {
			t.Fatalf("bad paths exit/stderr=%d/%q, want exit 2 and key=value error", code, stderr)
		}
	})

	t.Run("read-only open error", func(t *testing.T) {
		statsPath := filepath.Join(t.TempDir(), "invalid.sqlite3")
		content := []byte("not a sqlite database")
		if err := os.WriteFile(statsPath, content, 0o600); err != nil {
			t.Fatalf("write invalid SQLite input: %v", err)
		}
		code, _, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-dry-run", "-driver", "sqlite", "-paths", "stats="+filepath.ToSlash(statsPath))
		if code != 1 || !strings.Contains(stderr, "open read-only sqlite stats") {
			t.Fatalf("read-only open failure exit/stderr=%d/%q", code, stderr)
		}
		got, err := os.ReadFile(statsPath)
		if err != nil {
			t.Fatalf("read original invalid SQLite input: %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("read-only open modified input: %q", got)
		}
	})

	t.Run("pending count query error", func(t *testing.T) {
		statsPath := filepath.Join(t.TempDir(), "stats.sqlite3")
		db := openRebuildSQLite(t, statsPath)
		if _, err := db.Exec(`CREATE TABLE unrelated (id INTEGER PRIMARY KEY)`); err != nil {
			db.Close()
			t.Fatalf("create minimal SQLite file: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close minimal SQLite file: %v", err)
		}
		code, _, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-dry-run", "-driver", "sqlite", "-paths", "stats="+filepath.ToSlash(statsPath))
		if code != 1 || !strings.Contains(stderr, "count pending rows of usage_stats_totals") || !strings.Contains(strings.ToLower(stderr), "no such table") {
			t.Fatalf("pending count failure exit/stderr=%d/%q", code, stderr)
		}
	})

	t.Run("apply query failure reports driver error", func(t *testing.T) {
		statsPath := filepath.Join(t.TempDir(), "stats.sqlite3")
		db := openRebuildSQLite(t, statsPath)
		if _, err := db.Exec(`CREATE TABLE backfill_progress (
			table_name TEXT PRIMARY KEY, status TEXT NOT NULL,
			cursor_json TEXT NOT NULL DEFAULT '{}', updated_at TEXT NOT NULL
		)`); err != nil {
			db.Close()
			t.Fatalf("create marker-only SQLite file: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close marker-only SQLite file: %v", err)
		}
		code, _, stderr := runApplySuccessCostBackfill(t, "-apply-success-cost-backfill", "-driver", "sqlite", "-paths", "stats="+filepath.ToSlash(statsPath))
		if code != 1 || !strings.Contains(stderr, "成功成本回填失败（表 usage_stats_totals）") || !strings.Contains(strings.ToLower(stderr), "no such table") {
			t.Fatalf("apply query failure exit/stderr=%d/%q", code, stderr)
		}
	})
}

func TestW1ApplySuccessCostBackfillReportWriterFailure(t *testing.T) {
	_, pathsFlag := backfillFixturePaths(t)
	closedTemp, err := os.CreateTemp(t.TempDir(), "closed-stdout-*.json")
	if err != nil {
		t.Fatalf("create temporary report file: %v", err)
	}
	if err := closedTemp.Close(); err != nil {
		t.Fatalf("close temporary report file: %v", err)
	}

	previousStdout := os.Stdout
	os.Stdout = closedTemp
	t.Cleanup(func() { os.Stdout = previousStdout })
	code := applySuccessCostBackfillResult("sqlite", pathsFlag, "", false)
	os.Stdout = previousStdout
	if code != 1 {
		t.Fatalf("report writer failure exit=%d, want 1", code)
	}
}
