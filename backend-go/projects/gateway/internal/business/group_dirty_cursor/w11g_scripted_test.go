package groupdirtycursor

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
)

// ---------------------------------------------------------------------------
// 脚本化 database/sql driver（w11g 前缀）：直测 store 各写入路径的错误臂与
// Postgres FOR UPDATE 锁分支，不依赖真实数据库。
// ---------------------------------------------------------------------------

type w11gStep struct {
	contains    string
	cols        []string
	rows        [][]driver.Value
	rowsErr     error
	execErr     error
	affected    int64
	affectedErr error
	beginErr    error
	commitErr   error
	prepareErr  error
}

var w11gDriverSeq int64

type w11gScript struct{ steps []w11gStep }

func (s *w11gScript) next(kind, query string) (*w11gStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w11g script exhausted at %s: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w11g step mismatch: want %q got %s: %s", step.contains, kind, query)
	}
	return step, nil
}

type w11gConn struct{ script *w11gScript }

func (c *w11gConn) Prepare(query string) (driver.Stmt, error) {
	step, err := c.script.next("Prepare", query)
	if err != nil {
		return nil, err
	}
	if step.prepareErr != nil {
		return nil, step.prepareErr
	}
	return &w11gStmt{script: c.script}, nil
}
func (c *w11gConn) Close() error { return nil }
func (c *w11gConn) Begin() (driver.Tx, error) {
	step, err := c.script.next("Begin", "BEGIN")
	if err != nil {
		return nil, err
	}
	if step.beginErr != nil {
		return nil, step.beginErr
	}
	return &w11gTx{script: c.script}, nil
}
func (c *w11gConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"reason"}
	}
	return &w11gRows{cols: cols, values: step.rows}, nil
}
func (c *w11gConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w11gResult{affected: step.affected, err: step.affectedErr}, nil
}

type w11gStmt struct{ script *w11gScript }

func (s *w11gStmt) Close() error  { return nil }
func (s *w11gStmt) NumInput() int { return -1 }
func (s *w11gStmt) Exec(_ []driver.Value) (driver.Result, error) {
	step, err := s.script.next("StmtExec", "stmt")
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return w11gResult{affected: step.affected, err: step.affectedErr}, nil
}
func (s *w11gStmt) Query(_ []driver.Value) (driver.Rows, error) {
	step, err := s.script.next("StmtQuery", "stmt")
	if err != nil {
		return nil, err
	}
	if step.rowsErr != nil {
		return nil, step.rowsErr
	}
	return &w11gRows{cols: []string{"reason"}, values: step.rows}, nil
}

type w11gTx struct{ script *w11gScript }

func (t *w11gTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w11gTx) Rollback() error { return nil }

type w11gResult struct {
	affected int64
	err      error
}

func (r w11gResult) LastInsertId() (int64, error) { return 0, nil }
func (r w11gResult) RowsAffected() (int64, error) { return r.affected, r.err }

type w11gRows struct {
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *w11gRows) Columns() []string { return r.cols }
func (r *w11gRows) Close() error      { return nil }
func (r *w11gRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}
func (r *w11gRows) Err() error { return nil }

type w11gDriver struct{ script *w11gScript }

func (d w11gDriver) Open(string) (driver.Conn, error) { return &w11gConn{script: d.script}, nil }

func w11gOpenScripted(t *testing.T, mode Mode, steps []w11gStep) *Store {
	t.Helper()
	name := fmt.Sprintf("w11g-gdc-scripted-%d", atomic.AddInt64(&w11gDriverSeq, 1))
	sql.Register(name, w11gDriver{script: &w11gScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db, mode, "w11g_schema", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW11GMarkAllTransactionErrorArms(t *testing.T) {
	ctx := context.Background()
	beginErr := errors.New("w11g begin failed")
	s := w11gOpenScripted(t, SQLite, []w11gStep{{beginErr: beginErr}})
	if err := s.MarkAll(ctx, "reason"); err == nil {
		t.Fatal("begin error must propagate")
	}
	execErr := errors.New("w11g exec failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{{}, {execErr: execErr}})
	if err := s.MarkAll(ctx, "reason"); err == nil {
		t.Fatal("exec error must propagate")
	}
}

func TestW11GMarkAllAliasMethodsAndDefaultReason(t *testing.T) {
	s, db := testStore(t, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	ctx := context.Background()
	if err := s.MarkAllGroupAccountStatsDirty(ctx, "  "); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := db.QueryRow(`SELECT reason FROM group_account_stats_dirty WHERE group_id=?`, GroupAccountStatsDirtyAll).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "write" {
		t.Fatalf("default reason=%q want write", reason)
	}
	if err := s.MarkAllDirty(ctx, "alias"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT reason FROM group_account_stats_dirty WHERE group_id=?`, GroupAccountStatsDirtyAll).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "alias" {
		t.Fatalf("alias reason=%q", reason)
	}
}

func TestW11GDeleteRowsTransactionErrorArms(t *testing.T) {
	ctx := context.Background()
	rows := []DirtyRow{{GroupID: "w11g-g1", UpdatedAt: "u1"}}
	beginErr := errors.New("w11g begin failed")
	s := w11gOpenScripted(t, SQLite, []w11gStep{{beginErr: beginErr}})
	if _, err := s.DeleteRows(ctx, rows); err == nil {
		t.Fatal("begin error must propagate")
	}
	prepareErr := errors.New("w11g prepare failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{{}, {prepareErr: prepareErr}})
	if _, err := s.DeleteRows(ctx, rows); err == nil {
		t.Fatal("prepare error must propagate")
	}
	execErr := errors.New("w11g stmt exec failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{{}, {}, {execErr: execErr}})
	if _, err := s.DeleteRows(ctx, rows); err == nil {
		t.Fatal("stmt exec error must propagate")
	}
	affectedErr := errors.New("w11g rows-affected failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{{}, {}, {affectedErr: affectedErr}})
	if _, err := s.DeleteRows(ctx, rows); err == nil {
		t.Fatal("rows-affected error must propagate")
	}
	commitErr := errors.New("w11g commit failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{{}, {}, {affected: 1}, {commitErr: commitErr}})
	if _, err := s.DeleteRows(ctx, rows); err == nil {
		t.Fatal("commit error must propagate")
	}
	s = w11gOpenScripted(t, SQLite, []w11gStep{{}, {}, {affected: 1}, {}})
	if err := s.DeleteGroupAccountStatsDirtyRows(ctx, rows); err != nil {
		t.Fatalf("manifest alias err=%v", err)
	}
}

func TestW11GUpdateAllCursorPostgresLockAndErrorArms(t *testing.T) {
	ctx := context.Background()
	// Postgres 路径：FOR UPDATE 锁定 + 相等 cursor 幂等提交。
	s := w11gOpenScripted(t, Postgres, []w11gStep{
		{},
		{contains: "FOR UPDATE", cols: []string{"reason"}, rows: [][]driver.Value{{"all_cursor:w11g-g1"}}},
		{contains: "CASE WHEN", cols: []string{"c"}, rows: [][]driver.Value{{int64(0)}}},
		{contains: "CASE WHEN", cols: []string{"c"}, rows: [][]driver.Value{{int64(1)}}},
		{},
	})
	if err := s.UpdateGroupAccountStatsAllCursor(ctx, "w11g-g1"); err != nil {
		t.Fatal(err)
	}
	// 空 cursor：先拒绝。
	s = w11gOpenScripted(t, Postgres, nil)
	if err := s.UpdateAllCursor(ctx, "   "); !errors.Is(err, ErrCursorMalformed) {
		t.Fatalf("blank cursor err=%v", err)
	}
	beginErr := errors.New("w11g begin failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{{beginErr: beginErr}})
	if err := s.UpdateAllCursor(ctx, "w11g-g1"); err == nil {
		t.Fatal("begin error must propagate")
	}
	queryErr := errors.New("w11g select failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{{}, {rowsErr: queryErr}})
	if err := s.UpdateAllCursor(ctx, "w11g-g1"); err == nil {
		t.Fatal("select error must propagate")
	}
	// reason 为 NULL（初始非 cursor 状态）时直接推进 UPDATE。
	s = w11gOpenScripted(t, SQLite, []w11gStep{
		{},
		{cols: []string{"reason"}, rows: [][]driver.Value{{nil}}},
		{contains: "UPDATE", affected: 1},
		{},
	})
	if err := s.UpdateAllCursor(ctx, "w11g-g2"); err != nil {
		t.Fatal(err)
	}
	regressErr := errors.New("w11g regress query failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{
		{},
		{cols: []string{"reason"}, rows: [][]driver.Value{{AllCursorPrefix + "w11g-g1"}}},
		{rowsErr: regressErr},
	})
	if err := s.UpdateAllCursor(ctx, "w11g-g2"); err == nil {
		t.Fatal("regress query error must propagate")
	}
	equalErr := errors.New("w11g equal query failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{
		{},
		{cols: []string{"reason"}, rows: [][]driver.Value{{AllCursorPrefix + "w11g-g1"}}},
		{cols: []string{"c"}, rows: [][]driver.Value{{int64(0)}}},
		{rowsErr: equalErr},
	})
	if err := s.UpdateAllCursor(ctx, "w11g-g2"); err == nil {
		t.Fatal("equal query error must propagate")
	}
	execErr := errors.New("w11g update failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{
		{},
		{cols: []string{"reason"}, rows: [][]driver.Value{{nil}}},
		{contains: "UPDATE", execErr: execErr},
	})
	if err := s.UpdateAllCursor(ctx, "w11g-g2"); err == nil {
		t.Fatal("update error must propagate")
	}
	affectedErr := errors.New("w11g affected failed")
	s = w11gOpenScripted(t, SQLite, []w11gStep{
		{},
		{cols: []string{"reason"}, rows: [][]driver.Value{{nil}}},
		{contains: "UPDATE", affectedErr: affectedErr},
	})
	if err := s.UpdateAllCursor(ctx, "w11g-g2"); err == nil {
		t.Fatal("affected error must propagate")
	}
	// 影响行数非 1：视为 marker 丢失，失败关闭。
	s = w11gOpenScripted(t, SQLite, []w11gStep{
		{},
		{cols: []string{"reason"}, rows: [][]driver.Value{{nil}}},
		{contains: "UPDATE", affected: 0},
	})
	if err := s.UpdateAllCursor(ctx, "w11g-g2"); !errors.Is(err, ErrDirtyRowMissing) {
		t.Fatalf("affected=0 err=%v", err)
	}
}

func TestW11GConstructorAndContractGuards(t *testing.T) {
	if _, err := New(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("nil db must fail")
	}
	s, db := testStore(t, OwnerGate{})
	if _, err := New(db, Mode("bogus"), "", OwnerGate{}); err == nil {
		t.Fatal("invalid mode must fail")
	}
	if _, err := NewStore(db, SQLite, "", OwnerGate{}); err != nil {
		t.Fatalf("NewStore err=%v", err)
	}
	if _, err := NewStore(nil, SQLite, "", OwnerGate{}); err == nil {
		t.Fatal("NewStore nil db must fail")
	}
	ctx := context.Background()
	if err := s.CheckContract(ctx); err != nil {
		t.Fatalf("contract check err=%v", err)
	}
	var nilStore *Store
	if err := nilStore.CheckContract(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil store err=%v", err)
	}
	if err := (&Store{}).CheckContract(ctx); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("nil db err=%v", err)
	}
	if _, err := db.Exec(`DROP TABLE group_account_stats_dirty`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckContract(ctx); err == nil {
		t.Fatal("missing relation must fail contract check")
	}
}
