package settings

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

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// w9d：补齐 put/list/replace 的事务中途失败臂。
// ---------------------------------------------------------------------------

var w9dErrBoom = errors.New("w9d scripted failure")

func TestW9DPrefixedTableArm(t *testing.T) {
	bare := &Store{mode: Postgres, schema: "juhe_business"}
	if got := bare.table("global_settings"); got != "juhe_business.global_settings" {
		t.Fatalf("prefixed table=%q", got)
	}
}

func TestW9DPutGlobalInsertConflict(t *testing.T) {
	store, db := testStore(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := store.PutGlobal(ctx, "k", `1`, ""); err != nil {
		t.Fatal(err)
	}
	// 已存在键且无期望版本 → 全局 INSERT DO NOTHING 影响 0 行 → ErrCAS。
	if _, err := store.PutGlobal(ctx, "k", `2`, ""); !errors.Is(err, ErrCAS) {
		t.Fatalf("global insert conflict err=%v", err)
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
		cols = []string{"x"}
	}
	return &w9dRows{cols: cols, values: step.rows}, nil
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
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *w9dRows) Columns() []string { return r.cols }
func (r *w9dRows) Close() error      { return nil }
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
	name := fmt.Sprintf("w9d-settings-scripted-%d", atomic.AddInt64(&w9dDriverSeq, 1))
	sql.Register(name, w9dDriver{script: &w9dScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db, SQLite, "", OwnerGate{true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW9DPutScriptedFailureArms(t *testing.T) {
	ctx := context.Background()
	del := "INSERT INTO global_settings"
	upd := "UPDATE global_settings SET value_json=?,updated_at=? WHERE key=? AND updated_at=?"
	cases := []struct {
		name  string
		steps []w9dStep
	}{
		{"global insert", []w9dStep{{beginErr: nil}, {contains: del, execErr: w9dErrBoom}}},
		{"global update", []w9dStep{{beginErr: nil}, {contains: upd, execErr: w9dErrBoom}}},
		{"system insert", []w9dStep{{beginErr: nil}, {contains: "INSERT INTO system_settings", execErr: w9dErrBoom}}},
		{"system update", []w9dStep{{beginErr: nil}, {contains: "UPDATE system_settings", execErr: w9dErrBoom}}},
		{"commit", []w9dStep{{beginErr: nil}, {contains: del, affected: 1}, {commitErr: w9dErrBoom}}},
	}
	for _, tc := range cases {
		store := w9dOpen(t, tc.steps)
		var err error
		switch tc.name {
		case "global update":
			_, err = store.PutGlobal(ctx, "k", `1`, "rev")
		case "system insert":
			_, err = store.PutSystem(ctx, "sys", "k", `1`, "")
		case "system update":
			_, err = store.PutSystem(ctx, "sys", "k", `1`, "rev")
		default:
			_, err = store.PutGlobal(ctx, "k", `1`, "")
		}
		if err == nil {
			t.Fatalf("%s must fail", tc.name)
		}
	}
}

func TestW9DListAndReplaceScriptedFailureArms(t *testing.T) {
	ctx := context.Background()
	// 列表查询失败。
	list := w9dOpen(t, []w9dStep{{rowsErr: w9dErrBoom}})
	if _, err := list.ListProviderModels(ctx, "openai", false); err == nil {
		t.Fatal("list query failure must fail")
	}
	// 列表 Scan 失败（driver 值类型不兼容）。
	scan := w9dOpen(t, []w9dStep{{cols: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, rows: [][]driver.Value{{"openai", "m", "active", "", "bad-int", "[]", nil, nil, nil}}}})
	if _, err := scan.ListProviderModels(ctx, "openai", false); err == nil {
		t.Fatal("list scan failure must fail")
	}
	// 目录替换：CAS UPDATE 失败 / DELETE 失败 / INSERT 失败 / COMMIT 失败。
	casUpd := "UPDATE providers SET updated_at=?"
	del := "DELETE FROM provider_model_catalog"
	ins := "INSERT INTO provider_model_catalog"
	model := CatalogModel{ID: "i", Model: "m", Status: "active", Source: "s", SupportedAPIProtocolsJSON: "[]"}
	cases := []struct {
		name  string
		steps []w9dStep
	}{
		{"cas update", []w9dStep{{beginErr: nil}, {contains: casUpd, execErr: w9dErrBoom}}},
		{"cas mismatch", []w9dStep{{beginErr: nil}, {contains: casUpd, affected: 0}}},
		{"delete", []w9dStep{{beginErr: nil}, {contains: casUpd, affected: 1}, {contains: del, execErr: w9dErrBoom}}},
		{"insert", []w9dStep{{beginErr: nil}, {contains: casUpd, affected: 1}, {contains: del}, {contains: ins, execErr: w9dErrBoom}}},
		{"commit", []w9dStep{{beginErr: nil}, {contains: casUpd, affected: 1}, {contains: del}, {contains: ins}, {commitErr: w9dErrBoom}}},
	}
	for _, tc := range cases {
		store := w9dOpen(t, tc.steps)
		if err := store.ReplaceProviderCatalog(ctx, CatalogReplacement{ProviderCode: "openai", ExpectedProviderUpdatedAt: "r", Models: []CatalogModel{model}}); err == nil {
			t.Fatalf("%s must fail", tc.name)
		}
	}
}
