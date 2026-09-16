package sessionretention

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// w9d：直接参数臂 + 脚本化 driver 注入事务中途失败。
// ---------------------------------------------------------------------------

var w9dErrBoom = errors.New("w9d scripted failure")

func TestW9DNewRejectsNilDBAndDefaultsSchema(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{}); err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("nil db err=%v", err)
	}
	db := w9dSQLite(t)
	// Postgres 空 schema 时注入默认 schema。
	store, err := New(db, Postgres, "  ", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	if store.schema != "juhe_business" {
		t.Fatalf("default schema=%q", store.schema)
	}
	if got := store.table(); got != "juhe_business.system_sessions" {
		t.Fatalf("table=%q", got)
	}
	if got := store.deleteSQL(); !strings.Contains(got, "juhe_business.system_sessions") {
		t.Fatalf("deleteSQL=%q", got)
	}
}

func TestW9DCheckContractArms(t *testing.T) {
	ctx := context.Background()
	// 空 store 直接拒绝。
	if err := (&Store{}).CheckContract(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("empty store contract err=%v", err)
	}
	// 查询失败（缺少关系）。
	bare, err := sql.Open("sqlite", "file:w9d-retention-bare?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	store, err := New(bare, SQLite, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckContract(ctx); err == nil || !strings.Contains(err.Error(), "session retention contract") {
		t.Fatalf("missing relation err=%v", err)
	}
}

func TestW9DExpiryZeroTimeRejected(t *testing.T) {
	db := w9dSQLite(t)
	store, err := New(db, SQLite, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.expiry("0001-01-01T00:00:00Z"); !errors.Is(err, ErrInvalidExpiry) {
		t.Fatalf("zero time err=%v", err)
	}
}

func TestW9DCleanupLimitNormalizationAndAlias(t *testing.T) {
	db := w9dSQLite(t)
	store, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if normalizeLimit(0) != 1 || normalizeLimit(-5) != 1 || normalizeLimit(3) != 3 {
		t.Fatal("normalizeLimit arms")
	}
	insertSession(t, db, "old", "2020-01-01T00:00:00.000Z")
	// Limit 0 归一化为 1，Alias 方法透传。
	deleted, err := store.CleanupExpiredSessions(context.Background(), "2021-01-01T00:00:00.000Z", 0)
	if err != nil || deleted != 1 {
		t.Fatalf("alias cleanup deleted=%d err=%v", deleted, err)
	}
	// 失败透传臂。
	blocked, err := New(db, SQLite, "", OwnerGate{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.CleanupExpiredSystemSessions(context.Background(), "2021-01-01T00:00:00.000Z", 1); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("blocked alias err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// 脚本化 driver
// ---------------------------------------------------------------------------

type w9dStep struct {
	contains    string
	cols        []string
	rows        [][]driver.Value
	rowsErr     error
	execErr     error
	affected    int64
	affectedErr error
	beginErr    error
	commitErr   error
	closeErr    error
}

type w9dScript struct{ steps []w9dStep }

func (s *w9dScript) next(kind, query string) (*w9dStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w9d script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w9d step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w9dConn struct{ script *w9dScript }

func (c *w9dConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w9d: prepare unsupported")
}
func (c *w9dConn) Close() error { return nil }
func (c *w9dConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w9dTx{script: c.script}, nil
}
func (c *w9dConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"id"}
	}
	return &w9dRows{cols: cols, values: step.rows, closeErr: step.closeErr}, nil
}
func (c *w9dConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w9dResult{affected: step.affected, err: step.affectedErr}, nil
}

type w9dTx struct{ script *w9dScript }

func (t *w9dTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w9dTx) Rollback() error { return nil }

type w9dResult struct {
	affected int64
	err      error
}

func (r w9dResult) LastInsertId() (int64, error) { return 0, nil }
func (r w9dResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w9dRows struct {
	cols     []string
	values   [][]driver.Value
	closeErr error
	index    int
}

func (r *w9dRows) Columns() []string { return r.cols }
func (r *w9dRows) Close() error      { return r.closeErr }
func (r *w9dRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}
func (r *w9dRows) Err() error { return nil }

type w9dDriver struct{ script *w9dScript }

func (d w9dDriver) Open(string) (driver.Conn, error) { return &w9dConn{script: d.script}, nil }

var w9dDriverSeq int64

func w9dOpen(t *testing.T, steps []w9dStep) *Store {
	t.Helper()
	name := fmt.Sprintf("w9d-retention-scripted-%d", atomic.AddInt64(&w9dDriverSeq, 1))
	sql.Register(name, w9dDriver{script: &w9dScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW9DCleanupScriptedFailureArms(t *testing.T) {
	cases := []struct {
		name  string
		steps []w9dStep
	}{
		{"begin", []w9dStep{{beginErr: w9dErrBoom}}},
		{"exec", []w9dStep{{beginErr: nil}, {contains: "DELETE FROM system_sessions", execErr: w9dErrBoom}}},
		{"rows affected", []w9dStep{{beginErr: nil}, {contains: "DELETE FROM system_sessions", affectedErr: w9dErrBoom}}},
		{"commit", []w9dStep{{beginErr: nil}, {contains: "DELETE FROM system_sessions"}, {commitErr: w9dErrBoom}}},
	}
	for _, tc := range cases {
		store := w9dOpen(t, tc.steps)
		if _, err := store.Cleanup(context.Background(), CleanupInput{ExpiredBefore: "2021-01-01T00:00:00.000Z", Limit: 1}); err == nil {
			t.Fatalf("%s must fail", tc.name)
		}
	}
	// CheckContract 的查询失败与 rows.Close 失败臂。
	contract := w9dOpen(t, []w9dStep{{rowsErr: w9dErrBoom}})
	if err := contract.CheckContract(context.Background()); err == nil {
		t.Fatal("query failure must fail contract")
	}
	closeFail := w9dOpen(t, []w9dStep{{cols: []string{"id"}, closeErr: w9dErrBoom}})
	if err := closeFail.CheckContract(context.Background()); err == nil {
		t.Fatal("rows close failure must fail contract")
	}
}

func w9dSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:w9d-retention-ctx?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE system_sessions (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, token_hash TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

var _ = time.Now
