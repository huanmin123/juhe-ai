package authorization

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
)

// ---------------------------------------------------------------------------
// 脚本化 database/sql driver（w7a 前缀）：注入事务中途失败防御臂。
// ---------------------------------------------------------------------------

type w7aStep struct {
	contains    string
	cols        []string
	rows        [][]driver.Value
	noRows      bool
	rowsErr     error
	eofErr      error
	execErr     error
	affected    int64
	affectedErr error
	beginErr    error
	commitErr   error
}

var w7aDriverSeq int64

type w7aScript struct{ steps []w7aStep }

func (s *w7aScript) next(kind, query string) (*w7aStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w7a script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w7a step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w7aConn struct{ script *w7aScript }

func (c *w7aConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w7a: prepare unsupported")
}
func (c *w7aConn) Close() error { return nil }
func (c *w7aConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w7aTx{script: c.script}, nil
}
func (c *w7aConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	if step.noRows || (len(step.rows) == 0 && step.cols == nil) {
		return &w7aRows{cols: []string{"x"}}, nil
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"id"}
	}
	return &w7aRows{cols: cols, eofErr: step.eofErr, values: step.rows}, nil
}
func (c *w7aConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w7aResult{affected: step.affected, err: step.affectedErr}, nil
}

type w7aTx struct{ script *w7aScript }

func (t *w7aTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w7aTx) Rollback() error { return nil }

type w7aResult struct {
	affected int64
	err      error
}

func (r w7aResult) LastInsertId() (int64, error) { return 0, nil }
func (r w7aResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w7aRows struct {
	cols   []string
	values [][]driver.Value
	eofErr error
	index  int
}

func (r *w7aRows) Columns() []string { return r.cols }
func (r *w7aRows) Close() error      { return nil }
func (r *w7aRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.eofErr != nil {
		return r.eofErr
	}
	return io.EOF
}
func (r *w7aRows) Err() error { return nil }

type w7aDriver struct{ script *w7aScript }

func (d w7aDriver) Open(string) (driver.Conn, error) { return &w7aConn{script: d.script}, nil }

func w7aOpen(t *testing.T, steps []w7aStep) *Store {
	t.Helper()
	name := fmt.Sprintf("w7a-authz-scripted-%d", atomic.AddInt64(&w7aDriverSeq, 1))
	sql.Register(name, w7aDriver{script: &w7aScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db, SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, usage(0))
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC) }
	return store
}

func TestW7AScriptedExpireDueArms(t *testing.T) {
	ctx := context.Background()
	grantCols := []string{"id", "updated_at"}
	grantRows := [][]driver.Value{{"g1", "u1"}}
	base := func() []w7aStep {
		return []w7aStep{
			{beginErr: nil},
			{cols: grantCols, rows: grantRows},
			{contains: "UPDATE resource_authorization_grants SET status='expired'", affected: 1},
			{contains: "UPDATE resource_authorizations SET status='expired'", affected: 1},
			{contains: "DELETE FROM group_accounts WHERE account_authorization_id", affected: 1},
			{contains: "DELETE FROM group_authorization_settings", affected: 1},
		}
	}
	cases := []struct {
		name  string
		steps []w7aStep
	}{
		{"begin", []w7aStep{{beginErr: w7aErrBoom}}},
		{"grants query", []w7aStep{{beginErr: nil}, {rowsErr: w7aErrBoom}}},
		{"grants scan", []w7aStep{{beginErr: nil}, {cols: grantCols, rows: [][]driver.Value{{nil, nil}}}}},
		{"grants cas", append([]w7aStep{{beginErr: nil}, {cols: grantCols, rows: grantRows}}, w7aStep{contains: "UPDATE resource_authorization_grants SET status='expired'", affected: 0})},
		{"grants rowsAffected", []w7aStep{{beginErr: nil}, {cols: grantCols, rows: grantRows}, {contains: "UPDATE resource_authorization_grants SET status='expired'", affectedErr: w7aErrBoom}}},
		{"authorizations update", []w7aStep{{beginErr: nil}, {cols: grantCols, rows: grantRows}, {contains: "UPDATE resource_authorization_grants SET status='expired'", affected: 1}, {contains: "UPDATE resource_authorizations SET status='expired'", execErr: w7aErrBoom}}},
		{"group accounts delete", []w7aStep{{beginErr: nil}, {cols: grantCols, rows: grantRows}, {contains: "UPDATE resource_authorization_grants SET status='expired'", affected: 1}, {contains: "UPDATE resource_authorizations SET status='expired'", affected: 1}, {contains: "DELETE FROM group_accounts WHERE account_authorization_id", execErr: w7aErrBoom}}},
		{"settings delete", func() []w7aStep {
			steps := base()
			steps[4] = w7aStep{contains: "DELETE FROM group_accounts WHERE account_authorization_id", affected: 1}
			return append(steps, w7aStep{contains: "DELETE FROM group_authorization_settings", execErr: w7aErrBoom})
		}()},
		{"commit", append(base(), w7aStep{commitErr: w7aErrBoom})},
	}
	for _, tc := range cases {
		store := w7aOpen(t, tc.steps)
		if _, err := store.ExpireDue(ctx, 10); err == nil {
			t.Fatalf("expire %s must fail", tc.name)
		}
	}
}

func TestW7AScriptedSyncAvailabilityArms(t *testing.T) {
	ctx := context.Background()
	accCols := []string{"id", "status", "availability_schedule_json", "availability_schedule_next_check_at", "updated_at"}
	accRow := []driver.Value{"a1", "active", "{}", nil, "u"}
	next := time.Date(2026, 8, 28, 13, 0, 0, 0, time.UTC)
	eval := evaluator{decision: ScheduleDecision{NextCheckAt: &next, EventKey: "k", Status: "disabled"}}
	base := func() []w7aStep {
		return []w7aStep{
			{beginErr: nil},
			{cols: accCols, rows: [][]driver.Value{accRow}},
			{contains: "INSERT INTO account_schedule_status_events", affected: 1},
			{contains: "UPDATE accounts SET status=?", affected: 1},
			{contains: "DELETE FROM group_authorization_settings", affected: 1},
		}
	}
	cases := []struct {
		name  string
		steps []w7aStep
	}{
		{"begin", []w7aStep{{beginErr: w7aErrBoom}}},
		{"accounts query", []w7aStep{{beginErr: nil}, {rowsErr: w7aErrBoom}}},
		{"accounts scan", []w7aStep{{beginErr: nil}, {cols: accCols, rows: [][]driver.Value{{nil, nil, nil, nil, nil}}}}},
		{"event insert", append([]w7aStep{{beginErr: nil}, {cols: accCols, rows: [][]driver.Value{accRow}}}, w7aStep{contains: "INSERT INTO account_schedule_status_events", execErr: w7aErrBoom})},
		{"account update", append(base()[:3], w7aStep{contains: "UPDATE accounts SET status=?", execErr: w7aErrBoom})},
		{"account cas", append(base()[:3], w7aStep{contains: "UPDATE accounts SET status=?", affected: 0})},
		{"commit", append(base(), w7aStep{commitErr: w7aErrBoom})},
	}
	for _, tc := range cases {
		store := w7aOpen(t, tc.steps)
		if _, err := store.SyncAvailability(ctx, eval, 10); err == nil {
			t.Fatalf("sync %s must fail", tc.name)
		}
	}
	// 事件去重分支：INSERT 影响 0 行 → Skipped + updateNext。
	dedup := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{accRow}},
		{contains: "INSERT INTO account_schedule_status_events", affected: 0},
		{contains: "UPDATE accounts SET availability_schedule_next_check_at=?", affected: 1},
		{commitErr: w7aErrBoom},
	})
	result, err := dedup.SyncAvailability(ctx, eval, 10)
	if err == nil || result.Skipped != 1 {
		t.Fatalf("dedup result=%+v err=%v", result, err)
	}
}
