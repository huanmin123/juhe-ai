package routestrategymigration

// cov 波次：补齐 migration.go 错误分支覆盖。真实 SQLite（无 schema 内存库、
// 已关闭句柄）覆盖查询与事务错误传播；脚本化 fake driver 覆盖 Scan /
// rows.Err / RowsAffected / Commit 等真实驱动下无法稳定注入的分支，
// 参考 internal/businesshandoff/w12g_fake_driver_test.go 的注入模式。
//
// 不可达清单（cov 登记，待用户裁决，不自行删除）：
//   - migration.go:88-90 Open 中 sql.Open("pgx", …) 错误臂：pgx 驱动由
//     migration.go 顶部 blank import（pgx/v5/stdlib）在包初始化时注册，
//     database/sql.Open 仅在驱动未注册时返回错误，测试进程内不可达。
//   - migration.go:247-249 listStrategiesByIDs 空 ids 早退臂：唯一调用点
//     runConfirm 以 len(strategies) > 0 为前置，strategyIDs 每行产出恰好
//     一个 id，该分支从包内全部入口不可达，属未来调用方的防御性护栏。

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

var errCovFake = errors.New("cov fake driver 注入错误")

// covFakeRows 支持空结果、多行结果与行耗尽后的迭代错误（喂给 rows.Err()）。
type covFakeRows struct {
	columns []string
	rows    [][]driver.Value
	pos     int
	nextErr error
}

func (r *covFakeRows) Columns() []string { return r.columns }
func (r *covFakeRows) Close() error      { return nil }
func (r *covFakeRows) Next(dest []driver.Value) error {
	if r.pos >= len(r.rows) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	copy(dest, r.rows[r.pos])
	r.pos++
	return nil
}

type covFakeResult struct {
	affected int64
	affErr   error
}

func (r covFakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r covFakeResult) RowsAffected() (int64, error) {
	if r.affErr != nil {
		return 0, r.affErr
	}
	return r.affected, nil
}

type covFakeTx struct {
	commitErr   error
	rollbackErr error
}

func (t *covFakeTx) Commit() error   { return t.commitErr }
func (t *covFakeTx) Rollback() error { return t.rollbackErr }

var covStrategyColumns = []string{"id", "name", "status", "mode", "bound_api_keys"}

// covFakeConn 按 SQL 片段把 confirm 流程分发到 list / dist / byids / count /
// exec 五个阶段，每个阶段可独立注入错误或行数据。
type covFakeConn struct {
	beginErr   error
	tx         *covFakeTx
	execErr    error
	rowsAffErr error
	queryErr   map[string]error
	listRows   *covFakeRows
	distRows   *covFakeRows
	byidsRows  *covFakeRows
	countRows  *covFakeRows
}

func (c *covFakeConn) phase(query string) string {
	switch {
	case strings.Contains(query, "UPDATE"):
		return "exec"
	case strings.Contains(query, "GROUP BY mode"):
		return "dist"
	case strings.Contains(query, "WHERE r.id IN"):
		return "byids"
	case strings.Contains(query, "WHERE r.mode = ?"):
		return "list"
	default:
		return "count"
	}
}

func (c *covFakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := c.queryErr[c.phase(query)]; err != nil {
		return nil, err
	}
	var rows *covFakeRows
	switch c.phase(query) {
	case "dist":
		rows = c.distRows
		if rows == nil {
			rows = &covFakeRows{columns: []string{"mode", "count"}, rows: [][]driver.Value{{TargetMode, int64(1)}}}
		}
	case "byids":
		rows = c.byidsRows
		if rows == nil {
			rows = &covFakeRows{columns: covStrategyColumns, rows: [][]driver.Value{{"strat-cov-1", "Hybrid", TargetStatus, TargetMode, int64(1)}}}
		}
	case "count":
		rows = c.countRows
		if rows == nil {
			rows = &covFakeRows{columns: []string{"count"}, rows: [][]driver.Value{{int64(0)}}}
		}
	default:
		rows = c.listRows
		if rows == nil {
			rows = &covFakeRows{columns: covStrategyColumns, rows: [][]driver.Value{{"strat-cov-1", "Hybrid", "active", SourceMode, int64(1)}}}
		}
	}
	return rows, nil
}

func (c *covFakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "UPDATE") {
		if c.execErr != nil {
			return nil, c.execErr
		}
		return covFakeResult{affected: 1, affErr: c.rowsAffErr}, nil
	}
	return covFakeResult{}, nil
}

func (c *covFakeConn) Prepare(string) (driver.Stmt, error)      { return nil, errCovFake }
func (c *covFakeConn) Close() error                             { return nil }
func (c *covFakeConn) CheckNamedValue(*driver.NamedValue) error { return nil }
func (c *covFakeConn) Begin() (driver.Tx, error)                { return c.beginTx() }
func (c *covFakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.beginTx()
}

func (c *covFakeConn) beginTx() (driver.Tx, error) {
	if c.beginErr != nil {
		return nil, c.beginErr
	}
	if c.tx == nil {
		c.tx = &covFakeTx{}
	}
	return c.tx, nil
}

type covFakeConnector struct{ conn *covFakeConn }

func (c covFakeConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c covFakeConnector) Driver() driver.Driver                        { return covFakeDriver{} }

type covFakeDriver struct{}

func (covFakeDriver) Open(string) (driver.Conn, error) { return nil, errCovFake }

func covFakeDB(t *testing.T, conn *covFakeConn) *sql.DB {
	t.Helper()
	db := sql.OpenDB(covFakeConnector{conn: conn})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestCovSchemalessSQLitePropagatesQueryFailure(t *testing.T) {
	// :memory: 库未 ensure 业务 schema：dry-run 与 confirm 的清单查询都必须失败，
	// 覆盖 runDryRun / runConfirm / listStrategies 的查询错误上抛分支。
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	if _, err := Run(ctx, db, DialectSQLite, false, now); err == nil || !strings.Contains(err.Error(), "读取 hybrid_smart route strategies 失败") {
		t.Fatalf("无 schema dry-run 必须上抛查询错误: %v", err)
	}
	if _, err := Run(ctx, db, DialectSQLite, true, now); err == nil || !strings.Contains(err.Error(), "读取 hybrid_smart route strategies 失败") {
		t.Fatalf("无 schema confirm 必须上抛查询错误: %v", err)
	}
}

func TestCovConfirmOnClosedDBFailsAtBeginTx(t *testing.T) {
	db := newBusinessSQLite(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), db, DialectSQLite, true, time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "开启 hybrid_smart strategy migration 事务失败") {
		t.Fatalf("已关闭句柄必须在 BeginTx 失败: %v", err)
	}
}

func TestCovConfirmErrorInjectionMatrix(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	strategyColumns := covStrategyColumns
	cases := []struct {
		name    string
		mutate  func(*covFakeConn)
		wantErr string
	}{
		{"update exec failure", func(c *covFakeConn) { c.execErr = errCovFake }, "迁移 hybrid_smart route strategies 失败"},
		{"rows affected failure", func(c *covFakeConn) { c.rowsAffErr = errCovFake }, "读取 hybrid_smart strategy migration affected rows 失败"},
		{"distribution query failure", func(c *covFakeConn) { c.queryErr["dist"] = errCovFake }, "读取 route strategies mode 分布失败"},
		{"migrated listing query failure", func(c *covFakeConn) { c.queryErr["byids"] = errCovFake }, "读取 hybrid_smart route strategies 失败"},
		{"remaining count query failure", func(c *covFakeConn) { c.queryErr["count"] = errCovFake }, "统计残留 hybrid_smart route strategies 失败"},
		{"commit failure", func(c *covFakeConn) { c.tx = &covFakeTx{commitErr: errCovFake} }, "提交 hybrid_smart strategy migration 事务失败"},
		{"strategy row scan failure", func(c *covFakeConn) {
			// id 列为 NULL：Scan 进 string 必须失败。
			c.listRows = &covFakeRows{columns: strategyColumns, rows: [][]driver.Value{{nil, "Hybrid", "active", SourceMode, int64(1)}}}
		}, "读取 hybrid_smart route strategy 行失败"},
		{"strategy rows iteration failure", func(c *covFakeConn) {
			c.listRows = &covFakeRows{columns: strategyColumns, rows: [][]driver.Value{{"strat-cov-1", "Hybrid", "active", SourceMode, int64(1)}}, nextErr: errCovFake}
		}, "遍历 hybrid_smart route strategies 失败"},
		{"distribution row scan failure", func(c *covFakeConn) {
			// mode 列为 NULL：Scan 进 string 必须失败。
			c.distRows = &covFakeRows{columns: []string{"mode", "count"}, rows: [][]driver.Value{{nil, int64(1)}}}
		}, "读取 route strategies mode 分布行失败"},
		{"distribution rows iteration failure", func(c *covFakeConn) {
			c.distRows = &covFakeRows{columns: []string{"mode", "count"}, rows: [][]driver.Value{{TargetMode, int64(1)}}, nextErr: errCovFake}
		}, "遍历 route strategies mode 分布失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := &covFakeConn{queryErr: map[string]error{}}
			tc.mutate(conn)
			report, err := Run(ctx, covFakeDB(t, conn), DialectSQLite, true, now)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望错误 %q，实际: %v", tc.wantErr, err)
			}
			if report.Confirm || report.DryRun || report.Driver != "" {
				// 失败路径返回零值 Report，不得声称已确认或已执行。
				t.Fatalf("失败路径必须返回零值 Report: %+v", report)
			}
		})
	}
	t.Run("no injection completes confirm", func(t *testing.T) {
		conn := &covFakeConn{queryErr: map[string]error{}}
		report, err := Run(ctx, covFakeDB(t, conn), DialectSQLite, true, now)
		if err != nil {
			t.Fatalf("无注入 confirm 必须成功: %v", err)
		}
		if report.MigratedRows != 1 || report.RemainingHybridSmart != 0 || !report.Ready() {
			t.Fatalf("confirm 报告异常: migrated=%d remaining=%d ready=%v", report.MigratedRows, report.RemainingHybridSmart, report.Ready())
		}
		if len(report.Migrated) != 1 || report.Migrated[0].ID != "strat-cov-1" {
			t.Fatalf("migrated 清单异常: %+v", report.Migrated)
		}
	})
}
