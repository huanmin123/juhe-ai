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
// w9d：补齐 w7a 脚本未触达的事务中段失败臂（w7a base 步骤缺 affected=1，
// 导致 grant UPDATE 之后的中段臂实际以 ErrCAS 提前返回）。
// ---------------------------------------------------------------------------

var w9dErrBoom = errors.New("w9d scripted failure")

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

// BeginTx 支持 ReadOnly 选项（CheckQuotaBatch 使用只读事务），脚本仍走 Begin 步骤。
func (c *w9dConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
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

// HasNextResultSet 在注入 closeErr 时保持 Rows 打开，避免 database/sql 在
// EOF 时自动关闭并吞掉 Close 错误。
func (r *w9dRows) HasNextResultSet() bool { return r.closeErr != nil }
func (r *w9dRows) NextResultSet() error   { return io.EOF }

type w9dDriver struct{ script *w9dScript }

func (d w9dDriver) Open(string) (driver.Conn, error) { return &w9dConn{script: d.script}, nil }

var w9dDriverSeq int64

func w9dOpen(t *testing.T, steps []w9dStep) *Store {
	t.Helper()
	name := fmt.Sprintf("w9d-authz-scripted-%d", atomic.AddInt64(&w9dDriverSeq, 1))
	sql.Register(name, w9dDriver{script: &w9dScript{steps: steps}})
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
	store.now = func() time.Time { return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC) }
	return store
}

func w9dBlockedStore(t *testing.T) *Store {
	t.Helper()
	// nil db 触发 requireOwner 的 ErrOwnerGate 臂。
	return &Store{gate: OwnerGate{Confirmed: true, SchemaReady: true}}
}

func TestW9DCheckQuotaBatchArms(t *testing.T) {
	ctx := context.Background()
	// 半开门禁拦截（requireOwner 臂）。
	if _, err := w9dBlockedStore(t).CheckQuotaBatch(ctx, QuotaRequest{}); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("blocked quota err=%v", err)
	}
	// limits 查询失败（非 NoRows）。
	qry := w9dOpen(t, []w9dStep{{beginErr: nil}, {rowsErr: w9dErrBoom}})
	if _, err := qry.CheckQuota(ctx, "g", "a"); err == nil {
		t.Fatal("limits query failure must fail")
	}
	// 提交失败。
	commit := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		{cols: []string{"limits", "status", "expires_at"}, rows: [][]driver.Value{{"", "active", nil}}},
		{commitErr: w9dErrBoom},
	})
	if _, err := commit.CheckQuota(ctx, "g", "a"); err == nil {
		t.Fatal("commit failure must fail")
	}
}

func TestW9DExpireDueScriptedFailureArms(t *testing.T) {
	ctx := context.Background()
	grantCols := []string{"id", "updated_at"}
	grantRows := [][]driver.Value{{"g1", "u1"}}
	grantsQuery := func() w9dStep { return w9dStep{cols: grantCols, rows: grantRows} }
	grantUpdate := func() w9dStep {
		return w9dStep{contains: "UPDATE resource_authorization_grants SET status='expired'", affected: 1}
	}
	cases := []struct {
		name  string
		steps []w9dStep
	}{
		{"grants close", []w9dStep{{beginErr: nil}, {cols: grantCols, rows: grantRows, closeErr: w9dErrBoom}}},
		{"grant update exec", []w9dStep{{beginErr: nil}, grantsQuery(), {contains: "UPDATE resource_authorization_grants SET status='expired'", execErr: w9dErrBoom}}},
		{"authorizations update", []w9dStep{{beginErr: nil}, grantsQuery(), grantUpdate(), {contains: "UPDATE resource_authorizations SET status='expired'", execErr: w9dErrBoom}}},
		{"group accounts delete", []w9dStep{{beginErr: nil}, grantsQuery(), grantUpdate(), {contains: "UPDATE resource_authorizations SET status='expired'", affected: 1}, {contains: "DELETE FROM group_accounts", execErr: w9dErrBoom}}},
		{"settings delete", []w9dStep{{beginErr: nil}, grantsQuery(), grantUpdate(), {contains: "UPDATE resource_authorizations SET status='expired'", affected: 1}, {contains: "DELETE FROM group_accounts", affected: 1}, {contains: "DELETE FROM group_authorization_settings", execErr: w9dErrBoom}}},
		{"commit", []w9dStep{{beginErr: nil}, grantsQuery(), grantUpdate(), {contains: "UPDATE resource_authorizations SET status='expired'", affected: 1}, {contains: "DELETE FROM group_accounts", affected: 1}, {contains: "DELETE FROM group_authorization_settings", affected: 1}, {commitErr: w9dErrBoom}}},
	}
	for _, tc := range cases {
		store := w9dOpen(t, tc.steps)
		if _, err := store.ExpireDue(ctx, 10); err == nil {
			t.Fatalf("expire %s must fail", tc.name)
		}
	}
}

func TestW9DSyncAvailabilityScriptedFailureArms(t *testing.T) {
	ctx := context.Background()
	accCols := []string{"id", "status", "availability_schedule_json", "availability_schedule_next_check_at", "updated_at"}
	accRow := []driver.Value{"a1", "active", "{}", nil, "u"}
	accountsQuery := func() w9dStep { return w9dStep{cols: accCols, rows: [][]driver.Value{accRow}} }
	insertEvent := func(affected int64) w9dStep {
		return w9dStep{contains: "INSERT INTO account_schedule_status_events", affected: affected}
	}
	// 半开门禁拦截。
	if _, err := w9dBlockedStore(t).SyncAvailability(ctx, nil, 10); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("blocked sync err=%v", err)
	}
	// rows.Close 失败（evaluator 不能为 nil：nil 校验先于事务）。
	closeFail := w9dOpen(t, []w9dStep{{beginErr: nil}, {cols: accCols, rows: [][]driver.Value{accRow}, closeErr: w9dErrBoom}})
	if _, err := closeFail.SyncAvailability(ctx, evaluator{decision: ScheduleDecision{}}, 10); err == nil {
		t.Fatal("rows close failure must fail")
	}
	next := time.Date(2026, 9, 10, 13, 0, 0, 0, time.UTC)
	eval := evaluator{decision: ScheduleDecision{NextCheckAt: &next, EventKey: "k", Status: "disabled"}}
	// 去重跳过路径中 updateNext 失败。
	skipped := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		accountsQuery(),
		insertEvent(0),
		{contains: "UPDATE accounts SET availability_schedule_next_check_at=?", execErr: w9dErrBoom},
	})
	if _, err := skipped.SyncAvailability(ctx, eval, 10); err == nil {
		t.Fatal("skipped updateNext failure must fail")
	}
	// 不变路径中 updateNext 失败。
	quiet := evaluator{decision: ScheduleDecision{}}
	unchanged := w9dOpen(t, []w9dStep{
		{beginErr: nil},
		accountsQuery(),
		{contains: "UPDATE accounts SET availability_schedule_next_check_at=?", execErr: w9dErrBoom},
	})
	if _, err := unchanged.SyncAvailability(ctx, quiet, 10); err == nil {
		t.Fatal("unchanged updateNext failure must fail")
	}
}
