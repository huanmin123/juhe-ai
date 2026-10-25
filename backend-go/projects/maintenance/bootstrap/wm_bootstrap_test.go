package bootstrap

// 本文件覆盖受控导出面 bootstrap 的 PostgreSQL 包装（录制驱动：全部执行成功、
// 查询返回空行，验证 EnsurePostgres/SeedPostgres 的语句计数与错误传播）。
// 六库 SQLite ensure+seed 导出面与 OpenSQLiteFile 随 SQLite 存储形态退役删除，
// 对应测试同批退役（PLAN-20261024T051925945Z B4）。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// ---- PostgreSQL 录制驱动 ----

type wmPGRecorder struct {
	mu sync.Mutex
	// stageRecordValue 模拟 juhe_business.global_settings 的
	// account_capability_domain_migration 行现值（空串=无记录）：SeedPostgres
	// 种子后的全新空库自动封存会经条件更新写入阶段记录，后续读取必须看到。
	stageRecordValue string
	execs            []string
	fail             bool
}

func (r *wmPGRecorder) record(query string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("wm bootstrap fake: 注入执行失败")
	}
	r.execs = append(r.execs, query)
	return nil
}

type wmPGConnector struct{ rec *wmPGRecorder }

func (c wmPGConnector) Connect(context.Context) (driver.Conn, error) {
	return &wmPGConn{rec: c.rec}, nil
}

func (c wmPGConnector) Driver() driver.Driver { return wmPGFakeDriver{} }

type wmPGFakeDriver struct{}

func (wmPGFakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("wm bootstrap fake: 请使用 sql.OpenDB")
}

type wmPGConn struct{ rec *wmPGRecorder }

func (c *wmPGConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("wm bootstrap fake: Prepare 不应被调用")
}
func (c *wmPGConn) Close() error { return nil }
func (c *wmPGConn) Begin() (driver.Tx, error) {
	// 全新空库自动封存（SeedPostgres 种子后挂点）会驱动迁移执行器的事务面；
	// 事务内执行沿用连接的 Exec/Query 录制语义，提交/回滚均为空操作。
	return &wmPGTx{rec: c.rec}, nil
}

type wmPGTx struct{ rec *wmPGRecorder }

func (t *wmPGTx) Commit() error   { return nil }
func (t *wmPGTx) Rollback() error { return nil }

func (c *wmPGConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case nil, bool, int64, float64, []byte, string, []string:
		return nil
	case int:
		value.Value = int64(typed)
		return nil
	default:
		return fmt.Errorf("wm bootstrap fake: 不支持的参数类型 %T", value.Value)
	}
}

func (c *wmPGConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := c.rec.record(query); err != nil {
		return nil, err
	}
	// 阶段记录写入捕获，供后续阶段读取查询应答，模拟真实库的持久化。
	// INSERT 领取：VALUES ($1=key, $2=value)；UPDATE 跃迁：
	// SET value_json=$1, updated_at=$2 WHERE key=$3。
	if strings.Contains(query, `"juhe_business"."global_settings"`) {
		var key, value string
		if len(args) >= 3 {
			key, _ = args[2].Value.(string)
			value, _ = args[0].Value.(string)
		} else if len(args) >= 2 {
			key, _ = args[0].Value.(string)
			value, _ = args[1].Value.(string)
		}
		if key == "account_capability_domain_migration" && value != "" {
			c.rec.mu.Lock()
			c.rec.stageRecordValue = value
			c.rec.mu.Unlock()
		}
	}
	return driver.RowsAffected(1), nil
}

func (c *wmPGConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := c.rec.record(query); err != nil {
		return nil, err
	}
	// 全新空库自动封存（capabilitydomain.SealFreshInstall，SeedPostgres 种子
	// 后挂点）的 accounts/api_keys 计数探针按空库应答（0 行）；其余查询保持
	// 零行结果（QueryRow().Scan() 语义等价 sql.ErrNoRows）。
	if strings.Contains(query, "COUNT(*)") &&
		(strings.Contains(query, `juhe_business"."accounts`) || strings.Contains(query, `juhe_business"."api_keys`)) {
		return &wmPGRows{columns: []string{"c0"}, values: []driver.Value{int64(0)}}, nil
	}
	// 阶段记录读取：按捕获的写入值应答（未写过=零行，等价 sql.ErrNoRows）。
	if strings.Contains(query, `"juhe_business"."global_settings"`) {
		c.rec.mu.Lock()
		value := c.rec.stageRecordValue
		c.rec.mu.Unlock()
		if value != "" {
			return &wmPGRows{columns: []string{"c0"}, values: []driver.Value{value}}, nil
		}
	}
	// 零行结果：QueryRow().Scan() 语义等价 sql.ErrNoRows。
	return &wmPGRows{columns: []string{"c0"}}, nil
}

type wmPGRows struct {
	columns []string
	values  []driver.Value
	pos     int
}

func (r *wmPGRows) Columns() []string { return r.columns }
func (r *wmPGRows) Close() error      { return nil }
func (r *wmPGRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}
	dest[0] = r.values[r.pos]
	r.pos++
	return nil
}

func openWMBootstrapPG(rec *wmPGRecorder) *sql.DB {
	return sql.OpenDB(wmPGConnector{rec: rec})
}

func TestWMEnsurePostgresWrapperMirrorsSchemaCounts(t *testing.T) {
	rec := &wmPGRecorder{}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	count, err := EnsurePostgres(context.Background(), db)
	if err != nil {
		t.Fatalf("EnsurePostgres: %v", err)
	}
	if count <= 0 {
		t.Fatalf("语句计数必须为正: %d", count)
	}
	// +6 个 CREATE SCHEMA 之外还有 1 条能力域形状只读校验查询（M1 起
	// EnsurePostgres 末尾追加 VerifyPostgresBusinessCapabilityDomainShape，
	// 982ed8f2f），录制器按执行计数一并收录。
	if len(rec.execs) != count+7 {
		t.Fatalf("执行数应等于语句数 + 6 个 CREATE SCHEMA + 1 个能力域形状校验: %d vs %d", len(rec.execs), count)
	}
	rec.fail = true
	if _, err := EnsurePostgres(context.Background(), db); err == nil {
		t.Fatal("执行失败必须上抛")
	}
}

func TestWMSeedPostgresWrapperMirrorsSchemaSeed(t *testing.T) {
	rec := &wmPGRecorder{}
	db := openWMBootstrapPG(rec)
	defer db.Close()
	result, err := SeedPostgres(context.Background(), db, SeedOptions{
		Now:    func() time.Time { return time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC) },
		Secret: "wm-secret",
	})
	if err != nil {
		t.Fatalf("SeedPostgres: %v", err)
	}
	if result.StatementCount <= 0 {
		t.Fatalf("语句计数必须为正: %d", result.StatementCount)
	}
}
