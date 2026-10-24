package groupdirtycursor

// pgkit_test.go 是 group_dirty_cursor 建库测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役；本切片只迁测试，
// 不删除任何生产 sqlite 臂）：pgtestkit 每测试独立库 + 生产 store 的 PG 臂
// （New Postgres + juhe_business schema 限定 + $n 占位符，与
// cmd/juhe-ai-gateway/compose.go 的 groupStatsDirtyMarker 生产装配同一形态）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：store_test.go 全部用例与
//     w11g_scripted_test.go 中经 testStore 建库的两个用例走本文件夹具；测试
//     体断言逐字节保持；夹具手写 seed/校验 SQL 经 fixtureSQL 对齐生产 PG 臂
//     （juhe_business 限定 + $n）。schema 与 maintenance 生产 DDL 精确对齐
//     （pg_schema_business_tables.go group_account_stats_dirty 三列 +
//     pg_schema_business_indexes.go idx_group_account_stats_dirty_updated；
//     生产该表本就无外键链，夹具内显式 schema 限定）。
//   - 组B（SQLite-only 生产臂为被测对象）：无建库用例残留；该臂行为已全部由
//     组A夹具或 scripted driver 覆盖，postgres_contract_test.go 的纯 SQL 形态
//     断言（不建库）中 SQLite 占位符分支随生产 sqlite 臂删除批次同批退役。
//   - scripted 假驱动（w11g driver 族）不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// groupDirtyCursorPGFixtureSchema 与 maintenance 生产 DDL 精确对齐
// （pg_schema_business_tables.go group_account_stats_dirty 三列 +
// pg_schema_business_indexes.go idx_group_account_stats_dirty_updated）。
const groupDirtyCursorPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
      group_id text PRIMARY KEY,
      reason text,
      updated_at text NOT NULL
    );
	CREATE INDEX IF NOT EXISTS idx_group_account_stats_dirty_updated
      ON juhe_business.group_account_stats_dirty(updated_at);
`

// fixtureTablePattern 是夹具手写 seed/校验 SQL 的裸表名改写（与生产 PG 臂
// s.table 的 juhe_business. 限定同形式；TABLE 关键字覆盖 DROP TABLE 臂）。
var fixtureTablePattern = regexp.MustCompile(`\b(FROM|INTO|UPDATE|JOIN|TABLE)\s+(group_account_stats_dirty)\b`)

// fixtureBind 把夹具手写 SQL 的 ? 占位符按序改写为 $n（与生产 Store.bind
// 同一惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
func fixtureBind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + strconv.Itoa(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// fixtureSQL 把夹具手写 SQL 对齐生产 PG 臂方言（juhe_business 限定 + $n）。
func fixtureSQL(query string) string {
	return fixtureBind(fixtureTablePattern.ReplaceAllString(query, "$1 juhe_business.$2"))
}

// newGroupDirtyCursorPGDatabase 创建 pgtestkit 隔离 PG 测试库并注入夹具 schema。
func newGroupDirtyCursorPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, groupDirtyCursorPGFixtureSchema)
		return err
	})
}
