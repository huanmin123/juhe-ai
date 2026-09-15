package modelcheckpolicy

// w7d（modelcheckpolicy 覆盖补齐）：构造器/nil 守卫、参数校验、策略校验失败
// 臂，以及用脚本化 database/sql driver 覆盖 Postgres 专有路径
// （SET LOCAL TRANSACTION READ ONLY、$n 占位符、FALSE 谓词、commit 错误）。
// 不连接真实数据库；进程内确定性。

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
// 脚本化 driver：按步骤次序应答 Begin/Exec/Query/Commit
// ---------------------------------------------------------------------------

var w7dDriverSeq int64

type w7dStep struct {
	contains  string         // 语句必须包含的子串（空 = 不校验）
	cols      []string       // Query 列名
	row       []driver.Value // 单行数据（nil = 零行）
	execErr   error          // Exec 返回的错误
	queryErr  error          // Query 返回的错误
	commitErr error          // Commit 返回的错误
}

type w7dScript struct{ steps []w7dStep }

func (s *w7dScript) next(kind, query string) (*w7dStep, error) {
	if len(s.steps) == 0 {
		return nil, fmt.Errorf("w7d scripted 脚本在 %s 处耗尽: %s", kind, query)
	}
	step := &s.steps[0]
	s.steps = s.steps[1:]
	if step.contains != "" && !strings.Contains(query, step.contains) {
		return nil, fmt.Errorf("w7d scripted 步骤不匹配: want %q got %s %s", step.contains, kind, query)
	}
	return step, nil
}

type w7dConn struct{ script *w7dScript }

func (c *w7dConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w7d: Prepare unsupported")
}
func (c *w7dConn) Close() error              { return nil }
func (c *w7dConn) Begin() (driver.Tx, error) { return &w7dTx{script: c.script}, nil }

// BeginTx 让 database/sql 接受 ReadOnly+RepeatableRead 选项（模拟 PG 驱动）。
func (c *w7dConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}
func (c *w7dConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step, err := c.script.next("Query", query)
	if err != nil {
		return nil, err
	}
	if step.queryErr != nil {
		return nil, step.queryErr
	}
	cols := step.cols
	if cols == nil {
		cols = []string{"revision"}
	}
	rows := &w7dRows{cols: cols}
	if step.row != nil {
		rows.values = [][]driver.Value{step.row}
	}
	return rows, nil
}
func (c *w7dConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step, err := c.script.next("Exec", query)
	if err != nil {
		return nil, err
	}
	if step.execErr != nil {
		return nil, step.execErr
	}
	return driver.RowsAffected(0), nil
}

type w7dTx struct{ script *w7dScript }

func (t *w7dTx) Commit() error {
	step, err := t.script.next("Commit", "COMMIT")
	if err != nil {
		return err
	}
	return step.commitErr
}
func (t *w7dTx) Rollback() error { return nil }

type w7dRows struct {
	cols   []string
	values [][]driver.Value
	index  int
}

func (r *w7dRows) Columns() []string { return r.cols }
func (r *w7dRows) Close() error      { return nil }
func (r *w7dRows) Err() error        { return nil }
func (r *w7dRows) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	return io.EOF
}

type w7dDriver struct{ script *w7dScript }

func (d w7dDriver) Open(string) (driver.Conn, error) { return &w7dConn{script: d.script}, nil }

func w7dOpenDB(t *testing.T, steps []w7dStep) *sql.DB {
	t.Helper()
	name := "w7d-policy-scripted-" + fmt.Sprint(atomic.AddInt64(&w7dDriverSeq, 1))
	sql.Register(name, w7dDriver{script: &w7dScript{steps: steps}})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// 构造器与参数守卫
// ---------------------------------------------------------------------------

func TestW7DNewReaderRejectsNilDB(t *testing.T) {
	if _, err := NewPostgresReader(nil); err == nil {
		t.Fatal("nil db 的 Postgres reader 必须拒绝")
	}
	if _, err := NewSQLiteReader(nil); err == nil {
		t.Fatal("nil db 的 SQLite reader 必须拒绝")
	}
}

func TestW7DUninitializedReaderMethodsFailClosed(t *testing.T) {
	var nilReader *Reader
	if err := nilReader.CheckContract(context.Background()); err == nil {
		t.Fatal("nil reader CheckContract 必须失败")
	}
	if _, err := nilReader.Load(context.Background(), "sys"); err == nil {
		t.Fatal("nil reader Load 必须失败")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader := &Reader{db: nil}
	if err := reader.CheckContract(context.Background()); err == nil {
		t.Fatal("nil db CheckContract 必须失败")
	}
	if _, err := reader.Load(context.Background(), "sys"); err == nil {
		t.Fatal("nil db Load 必须失败")
	}
	// 委托 falsePredicate/placeholder 的 nil 守卫分支。
	if got := nilReader.falsePredicate(); got != "0" {
		t.Fatalf("nil reader falsePredicate = %q", got)
	}
	if got := nilReader.placeholder(1); got != "?" {
		t.Fatalf("nil reader placeholder = %q", got)
	}
	_ = db
}

func TestW7DLoadRejectsBlankSystemAccount(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Load(context.Background(), "   "); err == nil {
		t.Fatal("空白 systemAccountID 必须拒绝")
	}
}

// ---------------------------------------------------------------------------
// SQLite 校验失败臂
// ---------------------------------------------------------------------------

func TestW7DSQLiteLoadReportsInvalidPolicyRow(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE model_quality_policies(system_account_id TEXT PRIMARY KEY,revision INTEGER NOT NULL,profile TEXT NOT NULL,manual_enforcement_enabled INTEGER NOT NULL,penalty_threshold INTEGER NOT NULL,penalty_action TEXT NOT NULL,recovery_interval_minutes INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_quality_policies VALUES('sys-1', 1, 'bogus-profile', 0, 70, 'fallback', 10)`); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Load(context.Background(), "sys-1"); err == nil {
		t.Fatal("非法 profile 行必须在校验层失败")
	}
}

func TestW7DPostgresReaderOverSQLiteFailsAtReadOnlyGuard(t *testing.T) {
	// Postgres reader 的 SET LOCAL TRANSACTION READ ONLY 在 SQLite 上必然失败：
	// 覆盖 beginReadOnly 的 read-only 设置错误臂。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reader, err := NewPostgresReader(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Load(context.Background(), "sys-1"); err == nil {
		t.Fatal("SQLite 后端执行 SET LOCAL 必须失败")
	}
	if err := reader.CheckContract(context.Background()); err == nil {
		t.Fatal("SQLite 后端 CheckContract 必须失败")
	}
}

// ---------------------------------------------------------------------------
// 脚本化 Postgres 路径：占位符/只读事务/commit 错误臂
// ---------------------------------------------------------------------------

func TestW7DPostgresHappyPathsUsePlaceholdersAndReadOnlyTx(t *testing.T) {
	// CheckContract：SET LOCAL → FALSE 谓词查询（零行）→ COMMIT。
	db := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "WHERE FALSE"},
		{},
	})
	reader, err := NewPostgresReader(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.CheckContract(context.Background()); err != nil {
		t.Fatalf("PG CheckContract happy path: %v", err)
	}

	// Load 命中策略行：$1 占位符 + COMMIT。
	db2 := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "system_account_id=$1", cols: []string{"revision", "profile", "manual", "threshold", "action", "recovery"}, row: []driver.Value{"7", "quick", "true", "70", "fallback", "15"}},
		{},
	})
	reader2, err := NewPostgresReader(db2)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := reader2.Load(context.Background(), "sys-7")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != "7" || policy.RecoveryIntervalMinutes != 15 {
		t.Fatalf("PG Load 结果不正确: %#v", policy)
	}

	// Load 未命中 → 默认策略 + COMMIT。
	db3 := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "system_account_id=$1"},
		{},
	})
	reader3, err := NewPostgresReader(db3)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := reader3.Load(context.Background(), "sys-none")
	if err != nil {
		t.Fatal(err)
	}
	if fallback.Revision != "0" || fallback.Profile != "quick" {
		t.Fatalf("默认策略不正确: %#v", fallback)
	}
}

func TestW7DPostgresErrorArmsSurface(t *testing.T) {
	// CheckContract 查询错误。
	db := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "WHERE FALSE", queryErr: errors.New("w7d: contract query boom")},
	})
	reader, _ := NewPostgresReader(db)
	if err := reader.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "verify model check policy reader contract") {
		t.Fatalf("CheckContract 查询错误必须暴露: %v", err)
	}

	// CheckContract COMMIT 错误。
	db2 := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "WHERE FALSE"},
		{commitErr: errors.New("w7d: commit boom")},
	})
	reader2, _ := NewPostgresReader(db2)
	if err := reader2.CheckContract(context.Background()); err == nil || !strings.Contains(err.Error(), "commit model check policy reader contract") {
		t.Fatalf("CheckContract commit 错误必须暴露: %v", err)
	}

	// Load 非 NoRows 查询错误。
	db3 := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "system_account_id=$1", queryErr: errors.New("w7d: load boom")},
	})
	reader3, _ := NewPostgresReader(db3)
	if _, err := reader3.Load(context.Background(), "sys-1"); err == nil || !strings.Contains(err.Error(), "read model check policy") {
		t.Fatalf("Load 查询错误必须暴露: %v", err)
	}

	// 默认策略路径 COMMIT 错误。
	db4 := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "system_account_id=$1"},
		{commitErr: errors.New("w7d: default commit boom")},
	})
	reader4, _ := NewPostgresReader(db4)
	if _, err := reader4.Load(context.Background(), "sys-1"); err == nil || !strings.Contains(err.Error(), "commit default model check policy read") {
		t.Fatalf("默认策略 commit 错误必须暴露: %v", err)
	}

	// 命中策略路径 COMMIT 错误。
	db5 := w7dOpenDB(t, []w7dStep{
		{contains: "SET LOCAL TRANSACTION READ ONLY"},
		{contains: "system_account_id=$1", cols: []string{"a", "b", "c", "d", "e", "f"}, row: []driver.Value{"7", "quick", "true", "70", "fallback", "15"}},
		{commitErr: errors.New("w7d: hit commit boom")},
	})
	reader5, _ := NewPostgresReader(db5)
	if _, err := reader5.Load(context.Background(), "sys-1"); err == nil || !strings.Contains(err.Error(), "commit model check policy read") {
		t.Fatalf("命中策略 commit 错误必须暴露: %v", err)
	}
}
