// BUG-0297 回归：PG 单键设置查询的占位符必须与实参顺序
// QueryRowContext(ctx, query, SystemSettingsAccountID, key) 对齐——
// $1 = system_account_id、$2 = key。曾写反为 $2/$1，PG 下查询条件互换、
// 恒 ErrNoRows 回退默认值，SQLite（? 按位置绑定）测试全绿未能暴露。
//
// 本文件用脚本化 driver 捕获实际下发的 SQL 与实参，断言：
//   - PG 查询串的占位符映射为 system_account_id=$1、key=$2（字符串钉桩）；
//   - 实参顺序为 (SystemSettingsAccountID, key)（行为钉桩，防实参被调换
//     而字符串断言仍绿）；
//   - 两条读取路径（readValue 经 Number、readValueStrict 经
//     UpstreamClientVersionOverrides）共用同一查询串。
package jobssettings

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// pgPlaceholderQuery 是 BUG-0297 修复后的 PG 查询串（与包级常量逐字对齐）。
const pgPlaceholderQuery = `SELECT value_json FROM juhe_business.system_settings WHERE system_account_id = $1 AND key = $2 LIMIT 1`

var pgPlaceholderDriverSeq atomic.Int64

// pgPlaceholderCapture 记录 driver 实际收到的查询与实参。
type pgPlaceholderCapture struct {
	query string
	args  []driver.NamedValue
}

type pgPlaceholderConn struct{ capture *pgPlaceholderCapture }

func (c *pgPlaceholderConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("pg placeholder driver: Prepare 不应被调用")
}
func (c *pgPlaceholderConn) Close() error { return nil }
func (c *pgPlaceholderConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("pg placeholder driver: Begin 不应被调用")
}

func (c *pgPlaceholderConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.capture.query = query
	c.capture.args = append([]driver.NamedValue(nil), args...)
	// 零行：读取路径按缺行回退默认，本测试只钉桩查询形态。
	return &pgPlaceholderRows{}, nil
}

type pgPlaceholderRows struct{ consumed bool }

func (r *pgPlaceholderRows) Columns() []string { return []string{"value_json"} }
func (r *pgPlaceholderRows) Close() error      { return nil }
func (r *pgPlaceholderRows) Next([]driver.Value) error {
	if r.consumed {
		return io.EOF
	}
	r.consumed = true
	return io.EOF
}

type pgPlaceholderDriver struct{ capture *pgPlaceholderCapture }

func (d pgPlaceholderDriver) Open(string) (driver.Conn, error) {
	return &pgPlaceholderConn{capture: d.capture}, nil
}

func newPGPlaceholderDB(t *testing.T) (*sql.DB, *pgPlaceholderCapture) {
	t.Helper()
	capture := &pgPlaceholderCapture{}
	name := fmt.Sprintf("jobssettings-pg-placeholder-%d", pgPlaceholderDriverSeq.Add(1))
	sql.Register(name, pgPlaceholderDriver{capture: capture})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, capture
}

// assertPGPlaceholderOrder 断言一次 PG 读取实际下发的查询与实参。
func assertPGPlaceholderOrder(t *testing.T, capture *pgPlaceholderCapture, key string) {
	t.Helper()
	if capture.query != pgPlaceholderQuery {
		t.Fatalf("PG 查询串占位符错配:\n got: %s\nwant: %s", capture.query, pgPlaceholderQuery)
	}
	if !strings.Contains(capture.query, "system_account_id = $1") || !strings.Contains(capture.query, "key = $2") {
		t.Fatalf("PG 占位符映射必须是 system_account_id=$1、key=$2: %s", capture.query)
	}
	if strings.Contains(capture.query, "$3") || strings.Contains(capture.query, "system_account_id = $2") {
		t.Fatalf("PG 查询串出现反向或多余占位符: %s", capture.query)
	}
	if len(capture.args) != 2 {
		t.Fatalf("实参数量 = %d，期望 2", len(capture.args))
	}
	if got, _ := capture.args[0].Value.(string); got != SystemSettingsAccountID {
		t.Fatalf("实参 $1 必须是 system_account_id（%s），实际 %v", SystemSettingsAccountID, capture.args[0].Value)
	}
	if got, _ := capture.args[1].Value.(string); got != key {
		t.Fatalf("实参 $2 必须是 key（%s），实际 %v", key, capture.args[1].Value)
	}
}

// TestPostgresSettingSelectPlaceholderOrder 锁定两条读取路径的 PG 占位符
// 顺序（BUG-0297 回归）。
func TestPostgresSettingSelectPlaceholderOrder(t *testing.T) {
	cases := []struct {
		name string
		key  string
		read func(*Source) error
	}{
		{
			name: "readValue 经 Number",
			key:  "statsAggregationBatchSize",
			read: func(source *Source) error {
				_, err := source.Number(context.Background(), "statsAggregationBatchSize", 100, 10000)
				return err
			},
		},
		{
			name: "readValueStrict 经 UpstreamClientVersionOverrides",
			key:  "upstreamClientVersionOverrides",
			read: func(source *Source) error {
				_, err := source.UpstreamClientVersionOverrides(context.Background())
				return err
			},
		},
		{
			name: "readValueStrict 经 UpstreamClientVersionAutoOverrides",
			key:  "upstreamClientVersionAutoOverrides",
			read: func(source *Source) error {
				_, err := source.UpstreamClientVersionAutoOverrides(context.Background())
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, capture := newPGPlaceholderDB(t)
			source := NewSource(Options{DB: db, Mode: Postgres, Now: func() time.Time { return time.Unix(0, 0) }})
			if err := tc.read(source); err != nil {
				t.Fatalf("缺行回退默认不应报错: %v", err)
			}
			assertPGPlaceholderOrder(t, capture, tc.key)
		})
	}
}

// TestSystemSettingSelectQueryConstants 直接钉桩包级查询常量：PG 占位符
// 映射与 SQLite 位置参数形状，防止调用点绕过 systemSettingSelectQuery。
func TestSystemSettingSelectQueryConstants(t *testing.T) {
	if got := systemSettingSelectQuery(Postgres); got != pgPlaceholderQuery {
		t.Fatalf("Postgres 查询常量漂移: %s", got)
	}
	sqliteQuery := systemSettingSelectQuery(SQLite)
	if strings.Contains(sqliteQuery, "$") {
		t.Fatalf("SQLite 查询不得含 PG 占位符: %s", sqliteQuery)
	}
	if !strings.Contains(sqliteQuery, "system_account_id = ? AND key = ?") {
		t.Fatalf("SQLite 查询占位符顺序漂移: %s", sqliteQuery)
	}
	if systemSettingSelectQuery(Mode(9)) != sqliteQuery {
		t.Fatal("未知模式必须回落 SQLite 查询（保持既有行为）")
	}
}
