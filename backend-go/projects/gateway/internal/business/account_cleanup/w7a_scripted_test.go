package accountcleanup

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
// 脚本化 database/sql driver（w7a 前缀）：直测内部查询函数的错误防御臂。
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

func w7aOpenDB(t *testing.T, steps []w7aStep) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("w7a-cleanup-scripted-%d", atomic.AddInt64(&w7aDriverSeq, 1))
	sql.Register(name, w7aDriver{script: &w7aScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func w7aOpen(t *testing.T, steps []w7aStep) *Store {
	t.Helper()
	store, err := New(w7aOpenDB(t, steps), SQLite, "", OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW7AScriptedQueryHelperArms(t *testing.T) {
	ctx := context.Background()
	// queryAccounts：查询错误 / NULL 扫描错误 / 行迭代错误。
	for _, tc := range []struct {
		name  string
		steps []w7aStep
	}{
		{"query", []w7aStep{{rowsErr: w7aErrBoom}}},
		{"scan", []w7aStep{{cols: []string{"a", "b", "c", "d", "e", "f"}, rows: [][]driver.Value{make([]driver.Value, 6)}}}},
		{"iter", []w7aStep{{cols: []string{"a", "b", "c", "d", "e", "f"}, eofErr: w7aErrBoom}}},
	} {
		store := w7aOpen(t, tc.steps)
		if _, err := store.queryAccounts(ctx, "SELECT 1"); err == nil {
			t.Fatalf("queryAccounts %s must fail", tc.name)
		}
	}
	// queryRelated。
	for _, tc := range []struct {
		name  string
		steps []w7aStep
	}{
		{"query", []w7aStep{{rowsErr: w7aErrBoom}}},
		{"scan", []w7aStep{{cols: []string{"a", "b", "c", "d", "e"}, rows: [][]driver.Value{make([]driver.Value, 5)}}}},
		{"iter", []w7aStep{{cols: []string{"a", "b", "c", "d", "e"}, eofErr: w7aErrBoom}}},
	} {
		store := w7aOpen(t, tc.steps)
		if _, err := store.queryRelated(ctx, "SELECT 1"); err == nil {
			t.Fatalf("queryRelated %s must fail", tc.name)
		}
	}
	// queryAuthorizationRows。
	for _, tc := range []struct {
		name  string
		steps []w7aStep
	}{
		{"query", []w7aStep{{rowsErr: w7aErrBoom}}},
		{"scan", []w7aStep{{cols: []string{"a", "b", "c"}, rows: [][]driver.Value{{nil, nil, nil}}}}},
		{"iter", []w7aStep{{cols: []string{"a", "b", "c"}, eofErr: w7aErrBoom}}},
	} {
		store := w7aOpen(t, tc.steps)
		if _, err := store.queryAuthorizationRows(ctx, "SELECT 1"); err == nil {
			t.Fatalf("queryAuthorizationRows %s must fail", tc.name)
		}
	}
	// queryIDs。
	for _, tc := range []struct {
		name  string
		steps []w7aStep
	}{
		{"query", []w7aStep{{rowsErr: w7aErrBoom}}},
		{"scan", []w7aStep{{cols: []string{"id"}, rows: [][]driver.Value{{nil}}}}},
		{"iter", []w7aStep{{cols: []string{"id"}, eofErr: w7aErrBoom}}},
	} {
		store := w7aOpen(t, tc.steps)
		if _, err := store.queryIDs(ctx, "SELECT 1"); err == nil {
			t.Fatalf("queryIDs %s must fail", tc.name)
		}
	}
	// queryTeamSources：扫描错误与排序分支。
	teamStore := w7aOpen(t, []w7aStep{{cols: []string{"authorization_id", "source_team_id"}, rows: [][]driver.Value{{"b", "t2"}, {"a", "t1"}, {"a", "t0"}}}})
	teams, err := teamStore.queryTeamSources(ctx, []string{"a", "b"})
	if err != nil || len(teams) != 3 || teams[0].AuthorizationID != "a" || teams[0].SourceTeamID != "t0" || teams[2].AuthorizationID != "b" {
		t.Fatalf("teams=%+v err=%v", teams, err)
	}
	teamErr := w7aOpen(t, []w7aStep{{cols: []string{"authorization_id", "source_team_id"}, rows: [][]driver.Value{{nil, nil}}}})
	if _, err := teamErr.queryTeamSources(ctx, []string{"a"}); err == nil {
		t.Fatal("team scan error must fail")
	}
}

func TestW7AScriptedActiveAuthorizationInstanceArms(t *testing.T) {
	ctx := context.Background()
	ok := w7aOpen(t, []w7aStep{{cols: []string{"authorization_instance_authorization_id"}, rows: [][]driver.Value{{"auth-1"}, {nil}}}})
	active, err := ok.activeAuthorizationInstances(ctx, []string{"auth-1", "auth-2"}, true)
	if err != nil || len(active) != 1 || !active["auth-1"] {
		t.Fatalf("active=%v err=%v", active, err)
	}
	// 空入参直接返回空映射。
	empty := w7aOpen(t, nil)
	if active, err := empty.activeAuthorizationInstances(ctx, nil, true); err != nil || len(active) != 0 {
		t.Fatalf("empty active=%v err=%v", active, err)
	}
	qerr := w7aOpen(t, []w7aStep{{rowsErr: w7aErrBoom}})
	if _, err := qerr.activeAuthorizationInstances(ctx, []string{"a"}, true); err == nil {
		t.Fatal("query error must fail")
	}
	serr := w7aOpen(t, []w7aStep{{cols: []string{"x"}, rows: [][]driver.Value{{nil}}}})
	// DISTINCT id 列为 NULL 时跳过（有效扫描）。
	if _, err := serr.activeAuthorizationInstances(ctx, []string{"a"}, true); err != nil {
		t.Fatalf("null distinct id err=%v", err)
	}
}

func TestW7AScriptedListCandidatesArms(t *testing.T) {
	ctx := context.Background()
	rootCols := []string{"id", "system_account_id", "authorization_instance_authorization_id", "authorization_instance_source_account_id", "deleted_at", "updated_at"}
	rootRow := []driver.Value{"root", "sys", nil, nil, "2026-01-05T00:00:00.000Z", "2026-01-05T00:00:00.000Z"}
	// 实例分支查询失败。
	store := w7aOpen(t, []w7aStep{
		{cols: rootCols, rows: [][]driver.Value{rootRow}},
		{rowsErr: w7aErrBoom},
	})
	if _, err := store.listCandidates(ctx, "2026-02-01T00:00:00.000Z", 5); err == nil {
		t.Fatal("instances query failure must fail")
	}
	// 根候选填满 limit：实例分支被跳过。
	full := w7aOpen(t, []w7aStep{{cols: rootCols, rows: [][]driver.Value{rootRow, rootRow}}})
	rows, err := full.listCandidates(ctx, "2026-02-01T00:00:00.000Z", 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	// queryAccounts 扫描错误经 listCandidates 上抛。
	scanErr := w7aOpen(t, []w7aStep{
		{cols: rootCols, rows: [][]driver.Value{{nil, nil, nil, nil, nil, nil}}},
		{cols: rootCols, rows: nil},
	})
	if _, err := scanErr.listCandidates(ctx, "2026-02-01T00:00:00.000Z", 5); err == nil {
		t.Fatal("root scan failure must fail")
	}
}

func TestW7AScriptedSoftDeleteOrphanAndDeleteBusinessArms(t *testing.T) {
	ctx := context.Background()
	// Begin 失败。
	begin := w7aOpen(t, []w7aStep{{beginErr: w7aErrBoom}})
	if _, err := begin.softDeleteOrphan(ctx, accountRow{ID: "a", SystemAccountID: "s", AuthorizationInstanceAuthID: nullStringV("x")}); err == nil {
		t.Fatal("begin failure must fail")
	}
	// deleteBusiness：重读失败 / 根行身份变化 / 行数缺失 / 事务失败。
	cand := candidate{
		row:        accountRow{ID: "root", SystemAccountID: "sys", DeletedAt: nullStringV("d"), UpdatedAt: nullStringV("u")},
		AccountIDs: []string{"root"},
	}
	accCols := []string{"id", "system_account_id", "authorization_instance_authorization_id", "authorization_instance_source_account_id", "deleted_at", "updated_at"}
	rootRow := []driver.Value{"root", "sys", nil, nil, "d", "u"}
	rereadErr := w7aOpen(t, []w7aStep{{beginErr: nil}, {rowsErr: w7aErrBoom}})
	if _, err := rereadErr.deleteBusiness(ctx, cand, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("re-read failure must fail")
	}
	rootCAS := w7aOpen(t, []w7aStep{{beginErr: nil}, {cols: accCols, rows: [][]driver.Value{append([]driver.Value{"root", "sys", nil, nil, "d"}, []driver.Value{"changed"}...)}}})
	if _, err := rootCAS.deleteBusiness(ctx, cand, "2026-02-01T00:00:00.000Z"); !errors.Is(err, ErrCAS) {
		t.Fatalf("root identity CAS=%v", err)
	}
	missingRow := w7aOpen(t, []w7aStep{{beginErr: nil}, {cols: accCols, rows: nil}})
	if _, err := missingRow.deleteBusiness(ctx, cand, "2026-02-01T00:00:00.000Z"); !errors.Is(err, ErrCAS) {
		t.Fatalf("missing row CAS=%v", err)
	}
	// 通过（含提交失败）：验证授权删除计数与 addChanged 的 RowsAffected 错误臂。
	okSteps := []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{rootRow}},
		{affected: 2},         // group_accounts
		{affected: 1},         // account_supported_models
		{affected: 1},         // account_model_mappings
		{affected: 1},         // account_tag_bindings
		{affected: 1},         // group_accounts(auth)
		{affected: 1},         // sources
		{affected: 1},         // quota(auth)
		{execErr: w7aErrBoom}, // quota(grants) 触发业务删除失败
	}
	quotaFail := w7aOpen(t, okSteps)
	if _, err := quotaFail.deleteBusiness(ctx, cand, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("grants quota delete failure must fail")
	}
	commitFail := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{rootRow}},
		{affected: 0},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 0},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{affected: 1},
		{commitErr: w7aErrBoom},
	})
	if _, err := commitFail.deleteBusiness(ctx, cand, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("commit failure must fail")
	}
	rowsAffectedErr := w7aOpen(t, []w7aStep{
		{beginErr: nil},
		{cols: accCols, rows: [][]driver.Value{rootRow}},
		{affectedErr: w7aErrBoom},
	})
	if _, err := rowsAffectedErr.deleteBusiness(ctx, cand, "2026-02-01T00:00:00.000Z"); err == nil {
		t.Fatal("RowsAffected failure must fail")
	}
}
