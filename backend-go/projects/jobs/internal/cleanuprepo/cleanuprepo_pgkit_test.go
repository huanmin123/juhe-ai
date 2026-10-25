package cleanuprepo

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/retention"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// cleanuprepo_pgkit_test.go 是真实建库测试迁 PG 的共享基建
// （PLAN-20261024T051925945Z B1 POC#2）：对「双方言单入口」的测试
// （deleteRowsBefore 家族 / execChanged 家族 / UpsertAccountUsageSnapshots 等，
// 生产代码按 DB.Postgres 内部分派），建库改走 pgtestkit 隔离 PG 测试库，
// DDL/种子/断言 SQL 与生产 PG 臂精确对齐（schema 限定 juhe_*、$n 占位符、
// bigint/double precision/text 列类型，对照 maintenance pg_schema_stats.go）。
// 被测对象为 SQLite-only 生产臂的测试（分片/镜像半区/codex 分片/SQLite 结算
// 链/SQLite 错误注入臂）不在本迁移面，其 sqlite 建库设施保持原状，随
// PLAN B3/B6 删生产 sqlite 臂批次一并退役。
//
// scripted 假驱动（pgRecorder / kitFailingPGConn / w13e 装饰句柄）不依赖真库，
// 保持原状（PLAN §3 非目标）。

const kitPGSchemaStats = "juhe_stats"

// openKitPostgres 打开 pgtestkit 隔离 PG 测试库并返回 PG 方言句柄；prepare
// 回调内建该测试所需的最小 schema（生产 PG 臂按 schema 限定访问，DDL 与
// 生产 schema 名一一对应）。测试结束自动删库。
func openKitPostgres(t *testing.T, prepare func(ctx context.Context, db *sql.DB) error) *DB {
	t.Helper()
	return &DB{DB: pgtestkit.New(t, prepare), Postgres: true}
}

// kitPGSchemaStats 是 stats 家族表的生产 PG schema（deleteRowsBefore /
// schemaForStats 的 PG 方言值；与组合根 openDual 的 "juhe_stats" 一致）。

type kitFailingPGConn struct {
	*recorderConn
	failOn []string
}

func (c *kitFailingPGConn) shouldFail(query string) bool {
	for _, needle := range c.failOn {
		if strings.Contains(query, needle) {
			return true
		}
	}
	return false
}

func (c *kitFailingPGConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.shouldFail(query) {
		return nil, fmt.Errorf("kitFailingPGConn: 注入失败：%s", oneLineSQL(query))
	}
	return c.recorderConn.ExecContext(ctx, query, args)
}

func (c *kitFailingPGConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.shouldFail(query) {
		return nil, fmt.Errorf("kitFailingPGConn: 注入失败：%s", oneLineSQL(query))
	}
	return c.recorderConn.QueryContext(ctx, query, args)
}

type kitFailingPGConnector struct {
	rec    *pgRecorder
	failOn []string
}

func (c kitFailingPGConnector) Connect(context.Context) (driver.Conn, error) {
	return &kitFailingPGConn{recorderConn: &recorderConn{rec: c.rec}, failOn: c.failOn}, nil
}

func (c kitFailingPGConnector) Driver() driver.Driver { return recorderDriver{rec: c.rec} }

// openKitFailingRecorderPG 打开命中 failOn 子串即报错的 PG 录制句柄。
func openKitFailingRecorderPG(rec *pgRecorder, failOn ...string) *DB {
	return &DB{DB: sql.OpenDB(kitFailingPGConnector{rec: rec, failOn: failOn}), Postgres: true}
}

// ---- retention.StatsWriter / DerivedWindowRefresher fake ----

type kitAPIKeyStatsCall struct {
	Target       retention.APIKeyCleanupTarget
	Rows         []map[string]any
	UpdatedAt    string
	ShardDeleted bool
}

type kitAccountStatsCall struct {
	Target       retention.ExpiredDeletedAccountTarget
	Rows         []map[string]any
	UpdatedAt    string
	ShardDeleted bool
}

// kitStatsWriter 记录 CleanupDeletedApiKeyRecordStats / CleanupDeletedAccountRecordStats
// 调用，并可脚本化返回错误（Node stats cleanup callback 的 fake）。
type kitStatsWriter struct {
	mu           sync.Mutex
	apiKeyCalls  []kitAPIKeyStatsCall
	accountCalls []kitAccountStatsCall
	apiKeyErr    error
	accountErr   error
}

func oneLineSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

// normalizeKitSQL 逐行 trim 后拼接，用于对生产 raw string SQL 做空白无关
// 的逐字符内容比对（保留 token 序列）。

func execKitSchema(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema %q: %v", oneLineSQL(statement), err)
		}
	}
}

func normalizeKitSQL(query string) string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(query, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

type kitFailingResult struct{}

func (kitFailingResult) LastInsertId() (int64, error) { return 0, nil }
func (kitFailingResult) RowsAffected() (int64, error) {
	return 0, errors.New("kitFailingResult: RowsAffected 注入失败")
}
