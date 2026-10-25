package helpweb

// pgkit_test.go 是 helpweb 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产 PG 臂（businessauth/authsys 以
// modelcheckauth.Postgres 装配，juhe_business schema 限定 + $n 占位符，与
// gateway 组合根同一装配形态）。
//
// 分组处置（D9/D17）：组A（被测对象有 PG 臂且断言逐字节保持）——w11f 帮助面
// 环境走本文件夹具，断言逐字节保持；夹具 schema 照抄 maintenance 生产 DDL
// （pg_schema_business_tables.go 的 system_accounts / system_sessions 全列、
// NOT NULL 与 UNIQUE），按 groups/modelcheckauth 夹具先例放宽一处：不建
// system_sessions.system_account_id 的 FOREIGN KEY。SPA/静态文件分支不触库，
// 保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// helpWebPGFixtureSchema 是帮助面鉴权链路的生产表形状。列定义照抄
// maintenance 生产 DDL（pg_schema_business_tables.go），唯一放宽是省略
// system_sessions 的 FOREIGN KEY（理由见文件头）。
const helpWebPGFixtureSchema = `
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

// newHelpWebPGDatabase 创建 pgtestkit 隔离 PG 测试库并注入生产表形状，返回
// 连接池供测试体直读写（测试结束自动关池删库）。
func newHelpWebPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, helpWebPGFixtureSchema)
		return err
	})
}
