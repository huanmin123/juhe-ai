package businessauth

// pgkit_test.go 是 businessauth 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产 PG 臂（New(db, modelcheckauth.Postgres, now,
// gate)，juhe_business schema 限定 + $n 占位符，与 gateway 组合根
// businessauth.New(businessConnection.DB, modelcheckauth.Postgres, ...) 同一
// 装配形态）。
//
// 分组处置（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言逐字节保持）：service 直驱面测试走本文件夹
//     具，断言逐字节保持；夹具 schema 照抄 maintenance 生产 DDL
//     （pg_schema_business_tables.go 的 system_accounts / system_sessions
//     全列、NOT NULL 与 UNIQUE），按 groups/modelcheckauth 夹具先例放宽一处：
//     不建 system_sessions.system_account_id 的 FOREIGN KEY。测试体内手写
//     seed/DML 经 fixtureBind 对齐生产 PG 臂（$n），种子补齐生产 NOT NULL 列
//     （created_at/updated_at）。
//   - 非法 mode 构造臂等仅需要非 nil 连接池、不触库的分支走
//     newUnreachablePGDatabase（accounts 包 w13a_patch_arms_test.go 先例）。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// businessAuthPGFixtureSchema 是 businessauth 契约面的生产表形状。列定义照抄
// maintenance 生产 DDL（pg_schema_business_tables.go），唯一放宽是省略
// system_sessions 的 FOREIGN KEY（理由见文件头）；token_hash UNIQUE 与全部
// NOT NULL/CHECK 行为性约束逐字保留。
const businessAuthPGFixtureSchema = `
	CREATE SCHEMA IF NOT EXISTS juhe_business;
	CREATE TABLE IF NOT EXISTS juhe_business.system_accounts (
      id text PRIMARY KEY,
      username text NOT NULL UNIQUE,
      display_name text NOT NULL,
      description text,
      role text NOT NULL DEFAULT 'user',
      status text NOT NULL DEFAULT 'active',
      password_hash text NOT NULL,
      must_change_password integer NOT NULL DEFAULT 0,
      image_generation_enabled integer NOT NULL DEFAULT 0,
      ai_account_limit integer CHECK (ai_account_limit BETWEEN 0 AND 1000000),
      expedited_account_limit integer CHECK (expedited_account_limit BETWEEN 0 AND 100),
      request_limits_json text,
      last_login_at text,
      created_at text NOT NULL,
      updated_at text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.system_sessions (
      id text PRIMARY KEY,
      system_account_id text NOT NULL,
      token_hash text NOT NULL UNIQUE,
      expires_at text NOT NULL,
      created_at text NOT NULL,
      last_seen_at text NOT NULL
    )
`

// newBusinessAuthPGDatabase 创建 pgtestkit 隔离 PG 测试库并注入生产表形状，
// 返回连接池供测试体直读写（测试结束自动关池删库）。
func newBusinessAuthPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, businessAuthPGFixtureSchema)
		return err
	})
}

// newUnreachablePGDatabase 返回一个已打开但底层不可达的 pgx 连接池（accounts
// 包 w13a_patch_arms_test.go 先例）：Open 恒成功，首次使用即连接拒绝，供
// 构造校验在触库前 fail 的高速分支锁定错误语义。
func newUnreachablePGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	pool, err := sql.Open("pgx", "host=127.0.0.1 port=1 sslmode=disable connect_timeout=1")
	if err != nil {
		t.Fatalf("pgtestkit: 打开不可达连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// fixtureBind 把夹具/测试手写 SQL 的 ? 占位符按序改写为 $n（与生产
// Authenticator.bind 同一惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量
// 干扰）。
func fixtureBind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
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
