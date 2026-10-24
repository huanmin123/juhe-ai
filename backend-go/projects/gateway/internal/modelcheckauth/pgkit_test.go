package modelcheckauth

// pgkit_test.go 是 modelcheckauth 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，modelcheckauth 测试迁移
// 切片）：pgtestkit 每测试独立库 + Authenticator 生产 PG 臂（New(db, Postgres,
// now)，juhe_business schema 限定 + $n 占位符，与 gateway 组合根
// modelcheckauth.New(businessConnection.DB, modelcheckauth.Postgres, ...) 同一
// 装配形态）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：直驱 / HTTP / 错误臂测试走
//     本文件夹具，测试体断言逐字节保持；夹具 schema 照抄 maintenance 生产
//     DDL（pg_schema_business_tables.go 的 system_accounts / system_sessions
//     全列、NOT NULL 与 UNIQUE），按 groups 夹具先例放宽一处：不建
//     system_sessions.system_account_id 的 FOREIGN KEY——w7b HTTP 矩阵在会话
//     存活中途 DELETE 后重插账户行，生产 ON DELETE CASCADE 会连带删会话改变
//     可观察行为（SQLite 夹具等价于外键未启用）；测试体内手写 DDL/DML 经
//     juhe_business. 限定 + $n 占位符对齐生产 PG 臂，列集裁剪的
//     CheckContract 契约测试保留内联变形表能力。测试名均不含「SQLite」。
//   - 组B（SQLite-only 生产臂语义断言）：w7b 直驱面末尾的 SQLite
//     table()/bind() 方言渲染断言、w14f scripted 驱动的 SQLite 模式臂——按
//     D17 不提前迁移也不提前删除，随生产 sqlite 臂删除批次同批退役。
//   - scripted 假驱动（w14f 全文件）不依赖真库，保持原状；进程内
//     captcha/loginGuard memory 臂测试（不触库）保持原状，memory↔redis 双臂
//     删除属组合根批次，不在本切片。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// modelcheckauthPGFixtureSchema 是本包契约面的生产表形状。列定义照抄
// maintenance 生产 DDL（pg_schema_business_tables.go），唯一放宽是省略
// system_sessions 的 FOREIGN KEY（理由见文件头）；token_hash UNIQUE 与全部
// NOT NULL/CHECK 行为性约束逐字保留。
const modelcheckauthPGFixtureSchema = `
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

// newModelCheckAuthPGDatabase 创建 pgtestkit 隔离 PG 测试库并注入生产表形状，
// 返回连接池供测试体直读写（测试结束自动关池删库）。
func newModelCheckAuthPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, modelcheckauthPGFixtureSchema)
		return err
	})
}
