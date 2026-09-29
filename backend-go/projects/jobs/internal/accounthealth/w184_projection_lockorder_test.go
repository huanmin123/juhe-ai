package accounthealth

// w184_projection_lockorder_test.go 用录制驱动（参照 proxylatency wfRecorder
// 模式，不依赖真实 PostgreSQL）锁定 projectOutcome 事务的双 advisory 锁顺序
// 契约（问题-0237，2026-09-30 生产三方死锁环；锁序约定出处见问题-0184/0192）：
//
//	首语句  pg_advisory_xact_lock($1)              → 7001001（advisorylock.AccountListDirty，dirty 写全局串行化）
//	次语句  pg_advisory_xact_lock(hashtextextended($1, 0)) → juhe-ai:account-health-projection:v1（投影互斥）
//	之后    才允许 receipts 查询 / accounts、input_versions 行锁。
//
// 若顺序倒置（health 键在前），UPDATE accounts 的库端触发器在触发器内等
// 7001001，与首锁 7001001 的合规方交叉持锁形成 40P01 死锁环。同时锁定
// SQLite 方言 no-op（不产生任何 pg_advisory 语句）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 录制驱动（仅记录语句与参数，不执行） ----

type w184RecordedStatement struct {
	query string
	args  []driver.Value
}

type w184Recorder struct {
	mu         sync.Mutex
	statements []w184RecordedStatement
}

func (r *w184Recorder) record(query string, args []driver.NamedValue) {
	values := make([]driver.Value, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, w184RecordedStatement{query: query, args: values})
}

func (r *w184Recorder) snapshot() []w184RecordedStatement {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]w184RecordedStatement, len(r.statements))
	copy(copied, r.statements)
	return copied
}

type w184Rows struct{}

func (w184Rows) Columns() []string         { return []string{"ignored"} }
func (w184Rows) Close() error              { return nil }
func (w184Rows) Next([]driver.Value) error { return io.EOF }

type w184Tx struct{}

func (w184Tx) Commit() error   { return nil }
func (w184Tx) Rollback() error { return nil }

type w184Conn struct {
	recorder *w184Recorder
}

func (c *w184Conn) Prepare(query string) (driver.Stmt, error) {
	// database/sql 优先走 ExecerContext/QueryerContext；Prepare 兜底记录并不再执行。
	return &w184Stmt{conn: c, query: query}, nil
}

type w184Stmt struct {
	conn  *w184Conn
	query string
}

func (s *w184Stmt) Close() error  { return nil }
func (s *w184Stmt) NumInput() int { return -1 }

func (s *w184Stmt) Exec(args []driver.Value) (driver.Result, error) {
	named := make([]driver.NamedValue, 0, len(args))
	for index, arg := range args {
		named = append(named, driver.NamedValue{Ordinal: index + 1, Value: arg})
	}
	s.conn.recorder.record(s.query, named)
	return driver.RowsAffected(1), nil
}

func (s *w184Stmt) Query(args []driver.Value) (driver.Rows, error) {
	named := make([]driver.NamedValue, 0, len(args))
	for index, arg := range args {
		named = append(named, driver.NamedValue{Ordinal: index + 1, Value: arg})
	}
	s.conn.recorder.record(s.query, named)
	return w184Rows{}, nil
}

func (c *w184Conn) Close() error { return nil }

func (c *w184Conn) Begin() (driver.Tx, error) { return w184Tx{}, nil }

func (c *w184Conn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.recorder.record(query, args)
	return driver.RowsAffected(1), nil
}

func (c *w184Conn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.recorder.record(query, args)
	return w184Rows{}, nil
}

type w184Connector struct {
	recorder *w184Recorder
}

func (c w184Connector) Connect(context.Context) (driver.Conn, error) {
	return &w184Conn{recorder: c.recorder}, nil
}
func (c w184Connector) Driver() driver.Driver { return w184Driver{} }

type w184Driver struct{}

func (w184Driver) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }

// w184RecordedProjector 用录制驱动装配 postgres 方言投影器并执行一次
// projectOutcome（outcome 无 Projection，走 terminal ignored 早退路径：两把
// advisory 锁 → receipt 查询 → receipt 写入 → 提交）。
func w184RecordedProjector(t *testing.T, postgres bool) []w184RecordedStatement {
	t.Helper()
	recorder := &w184Recorder{}
	db := sql.OpenDB(w184Connector{recorder: recorder})
	t.Cleanup(func() { _ = db.Close() })
	business, err := NewProjectionBusinessDB(db, postgres)
	if err != nil {
		t.Fatalf("装配录制业务库失败: %v", err)
	}
	projector, err := NewOutcomeProjector(&Store{}, OutcomeProjectorConfig{
		Business:         business,
		CredentialSecret: "w184-lock-order-secret",
		Logger:           nil,
		Now:              func() time.Time { return time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("装配录制投影器失败: %v", err)
	}
	outcome := Outcome{OutcomeID: "w184-outcome", AccountID: "w184-acct", InputVersion: 1}
	result, err := projector.projectOutcome(context.Background(), outcome)
	if err != nil {
		t.Fatalf("projectOutcome 执行失败: %v", err)
	}
	if result.Disposition != ProjectionIgnored {
		t.Fatalf("录制路径应走 terminal ignored，实际 %s", result.Disposition)
	}
	return recorder.snapshot()
}

// TestW184ProjectionDualAdvisoryLockOrderContract 断言 PG 方言下 projectOutcome
// 事务的前两条语句分别是 7001001 与 health-projection 键（顺序不可倒置），
// 且任何表访问语句不得先于两把锁。
func TestW184ProjectionDualAdvisoryLockOrderContract(t *testing.T) {
	statements := w184RecordedProjector(t, true)
	if len(statements) < 4 {
		t.Fatalf("录制语句过少: %+v", statements)
	}
	if !strings.Contains(statements[0].query, "pg_advisory_xact_lock($1)") || strings.Contains(statements[0].query, "hashtextextended") {
		t.Fatalf("首语句必须是 pg_advisory_xact_lock($1)（7001001），实际: %q", statements[0].query)
	}
	if len(statements[0].args) != 1 || statements[0].args[0] != int64(7001001) {
		t.Fatalf("首语句参数必须是 7001001，实际: %v", statements[0].args)
	}
	if !strings.Contains(statements[1].query, "pg_advisory_xact_lock(hashtextextended($1, 0))") {
		t.Fatalf("次语句必须是 health-projection 键锁，实际: %q", statements[1].query)
	}
	if len(statements[1].args) != 1 || statements[1].args[0] != accountHealthProjectionAdvisoryLockKey {
		t.Fatalf("次语句参数必须是 %q，实际: %v", accountHealthProjectionAdvisoryLockKey, statements[1].args)
	}
	for index, statement := range statements[2:] {
		if strings.Contains(statement.query, "pg_advisory_xact_lock") {
			t.Fatalf("两把锁之后不应再出现 advisory 语句，第 %d 条: %q", index+2, statement.query)
		}
		if index == 0 && !strings.Contains(statement.query, "account_health_projection_receipts") {
			t.Fatalf("第三条语句应是 receipt 幂等查询，实际: %q", statement.query)
		}
	}
}

// TestW184ProjectionAdvisoryLocksPostgresOnly 断言非 PG 方言不产生任何
// pg_advisory 语句（SQLite 单 writer no-op 契约）。
func TestW184ProjectionAdvisoryLocksPostgresOnly(t *testing.T) {
	for _, statement := range w184RecordedProjector(t, false) {
		if strings.Contains(statement.query, "pg_advisory") {
			t.Fatalf("SQLite 方言不应执行 pg_advisory 语句: %q", statement.query)
		}
	}
}
