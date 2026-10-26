package mediajobsadmin

// pgkit_test.go 是 mediajobsadmin 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，测试尾巴批次）：
// pgtestkit 每测试独立库 + 生产 PG 臂（gatewaymedia.NewMediaJobsRepo(db, true,
// now)，juhe_business schema 限定 + $n 占位符，与 gateway 组合根恒 PG 的装配
// 同一方言）。
//
// 分组处置（D9/D17）：组A（被测对象有 PG 臂且断言逐字节保持）——HTTP/repo 级
// 测试走本文件夹具，断言逐字节保持；夹具 schema 列集与原 sqlite 夹具同一
// 子集，media_jobs 类型/CHECK 照抄 maintenance 生产 DDL
// （pg_schema_business_tables.go），FK 按 groups 夹具先例放宽。
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

// mediaJobsAdminPGFixtureSchema 是 mediajobsadmin 面测试的 PG schema。
const mediaJobsAdminPGFixtureSchema = `
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
    );
	CREATE TABLE IF NOT EXISTS juhe_business.api_keys (
      id text PRIMARY KEY,
      system_account_id text NOT NULL
    );
	CREATE TABLE IF NOT EXISTS juhe_business.accounts (
      id text PRIMARY KEY,
      vendor_code text
    );
	CREATE TABLE IF NOT EXISTS juhe_business.media_jobs (
      id text PRIMARY KEY,
      kind text NOT NULL CHECK (kind IN ('video', 'audio_transcription', 'audio_speech')),
      api_key_id text NOT NULL,
      owner_system_account_id text NOT NULL DEFAULT '',
      account_id text NOT NULL,
      upstream_job_id text NOT NULL,
      status text NOT NULL CHECK (status IN ('queued', 'in_progress', 'completed', 'failed', 'cancelled', 'expired')),
      request_snapshot_json text NOT NULL DEFAULT '{}',
      artifact_json text NOT NULL DEFAULT '{}',
      error_json text NOT NULL DEFAULT '{}',
      usage_json text NOT NULL DEFAULT '{}',
      cost_usd double precision NOT NULL DEFAULT 0,
      params_applied_json text NOT NULL DEFAULT '[]',
      params_ignored_json text NOT NULL DEFAULT '[]',
      created_at text NOT NULL,
      updated_at text NOT NULL
    )
`

// newMediaJobsAdminPGDatabase 创建 pgtestkit 隔离 PG 测试库、注入夹具 schema，
// 并返回带 juhe_business search_path 的连接池（夹具/测试手写 SQL 的裸表名由
// search_path 解析；生产 repo 的 PG 臂本就 table() 全限定）。
func newMediaJobsAdminPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, mediaJobsAdminPGFixtureSchema)
		return err
	})
	_ = testDB.DB.Close()
	pool, err := sql.Open("pgx", testDB.DSN+"&search_path=juhe_business")
	if err != nil {
		t.Fatalf("pgtestkit: 打开夹具连接池失败: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// fixtureBind 把夹具/测试手写 SQL 的 ? 占位符按序改写为 $n（与生产 repo.bind
// 同一惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
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
