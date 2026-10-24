package sessionretention

// pgkit_test.go 是 session_retention 建库测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役；本切片只迁测试，
// 不删除任何生产 sqlite 臂）：pgtestkit 每测试独立库 + 生产 store 的 PG 臂
// （New Postgres + juhe_business schema 限定 + $n 占位符 + ctid 有界删除，
// 与 cmd/juhe-ai-gateway/main.go 的 retentionStore 生产装配同一形态）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：retention_test.go 全部用例
//     与 w9d_retention_extra_test.go 的直参数用例走本文件夹具；测试体断言
//     逐字节保持；夹具手写校验 SQL 经 fixtureSQL 对齐生产 PG 臂（juhe_business
//     限定 + $n）。原 SQLite rowid 系校验按 PG 形态改写为 ctid/ORDER BY
//     （tid 无 MIN 聚合，改写后语义与生产 deleteSQL 同序），见
//     TestCleanupPostgresStrictCutoffBoundedAndReplayable。
//   - 组B（SQLite-only 生产臂为被测对象）：SQLite 臂 rowid 删除 SQL 的行为
//     面随本迁移退出建库测试覆盖（生产已 PG-only，该臂为退役前遗留代码），
//     随生产 sqlite 臂删除批次同批退役；scripted driver 对 SQLite 臂 SQL
//     形态的断言按 scripted 假驱动原样保留。
//   - scripted 假驱动（w9d driver 族）不依赖真库，保持原状。
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

// sessionRetentionPGFixtureSchema 与 maintenance 生产 DDL 精确对齐
// （pg_schema_business_tables.go system_sessions 六列 + token_hash UNIQUE +
// pg_schema_business_indexes.go idx_system_sessions_expires_at）；按
// groups/statreads 夹具先例放宽一处：不建 FOREIGN KEY（生产外键指向
// system_accounts，本包夹不含该表列集）。
const sessionRetentionPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE TABLE IF NOT EXISTS juhe_business.system_sessions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      token_hash text NOT NULL UNIQUE,
      expires_at text NOT NULL,
      created_at text NOT NULL,
      last_seen_at text NOT NULL
    );
	CREATE INDEX IF NOT EXISTS idx_system_sessions_expires_at
      ON juhe_business.system_sessions(expires_at);
`

// fixtureTablePattern 是夹具手写校验 SQL 的裸表名改写（与生产 PG 臂
// s.table 的 juhe_business. 限定同形式）。
var fixtureTablePattern = regexp.MustCompile(`\b(FROM|INTO|UPDATE|JOIN)\s+(system_sessions)\b`)

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

// openPG 创建带 system_sessions 夹具的隔离 PG 测试库。
func openPG(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, sessionRetentionPGFixtureSchema)
		return err
	})
}

// openBarePG 创建无任何关系的隔离 PG 测试库（missing-relation 错误臂专用）。
func openBarePG(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t)
}

// insertSession 预置一行 system_sessions（fixtureSQL 对齐生产 PG 方言）。
func insertSession(t *testing.T, db *sql.DB, id, expires string) {
	t.Helper()
	_, err := db.Exec(fixtureSQL(`INSERT INTO system_sessions
		(id,system_account_id,token_hash,expires_at,created_at,last_seen_at)
		VALUES (?,?,?,?,?,?)`), id, "account", id+"-hash", expires, expires, expires)
	if err != nil {
		t.Fatal(err)
	}
}
