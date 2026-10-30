package gatewaycodex

// pgkit_test.go 是 gatewaycodex context 行存储测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，gatewaycodex 切片）：
// pgtestkit 每测试独立库 + 生产 PostgresContextStateStore（sqlpool 注入句柄，
// juhe_codex_context schema 限定与生产装配同方言）。
//
// 三组分类（D9/D17）：
//   - 组A（PostgresContextStateStore 生产臂）：fixture 夹具 DDL 与 maintenance
//     权威生产 DDL（pg_schema_codex_context.go）逐语句对齐（fresh 库含
//     provider_code 退役幂等 DROP，与生产执行顺序一致）；行契约/链读/compact
//     读正测试走本文件 newPostgresContextStateStore，断言与分片臂既有矩阵
//     逐字节一致。
//   - 原「组B」（SQLiteShardContextStateStore 分片生产臂及其夹具族）已随
//     sqlite 生产臂同批退役（2026-10-24 存储形态收口）；服务层测试夹具改为
//     生产 PostgresContextStateStore。
//   - scripted/内存实现与纯函数测试不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
	"github.com/huanminabc/juhe-ai/backend-go-platform/sqlpool"
)

// codexContextPostgresSchema 与 maintenance postgresSchemaCodexContext 的语句
// 序列一致（建表 → 索引 → 队列索引；B08 后无 provider_code 退役 DROP）。
const codexContextPostgresSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_codex_context;
CREATE TABLE IF NOT EXISTS juhe_codex_context.codex_context_sessions (
  id text PRIMARY KEY,
  system_account_id text NOT NULL,
  api_key_id text,
  group_id text NOT NULL,
  source_response_id text,
  latest_response_id text,
  latest_compact_id text,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  last_used_at text NOT NULL,
  expires_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_codex_context.codex_context_responses (
  response_id text PRIMARY KEY,
  session_id text NOT NULL,
  previous_response_id text,
  system_account_id text NOT NULL,
  api_key_id text,
  group_id text NOT NULL,
  upstream_account_id text,
  model text,
  upstream_model text,
  storage_key text NOT NULL,
  storage_offset_bytes bigint NOT NULL,
  sha256 text NOT NULL,
  raw_size_bytes bigint NOT NULL,
  compressed_size_bytes bigint NOT NULL,
  compression text NOT NULL DEFAULT 'gzip',
  schema_version integer NOT NULL DEFAULT 1,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  last_used_at text NOT NULL,
  expires_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_codex_context.codex_context_compacts (
  compact_id text PRIMARY KEY,
  session_id text NOT NULL,
  source_response_id text,
  summary_digest text NOT NULL,
  system_account_id text NOT NULL,
  api_key_id text,
  group_id text NOT NULL,
  upstream_account_id text,
  model text,
  upstream_model text,
  storage_key text NOT NULL,
  storage_offset_bytes bigint NOT NULL,
  sha256 text NOT NULL,
  raw_size_bytes bigint NOT NULL,
  compressed_size_bytes bigint NOT NULL,
  compression text NOT NULL DEFAULT 'gzip',
  schema_version integer NOT NULL DEFAULT 1,
  created_at text NOT NULL,
  updated_at text NOT NULL,
  last_used_at text NOT NULL,
  expires_at text NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_codex_context.codex_context_storage_cleanup_queue (
  storage_key text PRIMARY KEY,
  enqueued_at text NOT NULL,
  updated_at text NOT NULL,
  next_attempt_at text NOT NULL,
  attempt_count integer NOT NULL DEFAULT 0,
  last_error text
);
CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_expires ON juhe_codex_context.codex_context_sessions(expires_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_codex_context_sessions_last_used ON juhe_codex_context.codex_context_sessions(last_used_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_codex_context_responses_session ON juhe_codex_context.codex_context_responses(session_id, created_at ASC, response_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_responses_previous ON juhe_codex_context.codex_context_responses(previous_response_id) WHERE previous_response_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_codex_context_responses_expires ON juhe_codex_context.codex_context_responses(expires_at ASC, response_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_session ON juhe_codex_context.codex_context_compacts(session_id, created_at ASC, compact_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_source_response ON juhe_codex_context.codex_context_compacts(source_response_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_compacts_expires ON juhe_codex_context.codex_context_compacts(expires_at ASC, compact_id);
CREATE INDEX IF NOT EXISTS idx_codex_context_storage_cleanup_due ON juhe_codex_context.codex_context_storage_cleanup_queue(next_attempt_at ASC, enqueued_at ASC, storage_key ASC)`

// newPostgresContextStateStore 在 pgtestkit 隔离 PG 测试库上构建生产
// PostgresContextStateStore（sqlpool 句柄注入，与生产装配同一打开路径）。
// URL 标识用固定标签，不携带连接串内容。
func newPostgresContextStateStore(t *testing.T) *PostgresContextStateStore {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, codexContextPostgresSchema)
		return err
	})
	registry := sqlpool.NewRegistry()
	handle, err := registry.Acquire(
		func() (*sql.DB, error) { return db, nil },
		"pgtestkit://juhe_codex_context", "test", 1, 1,
	)
	if err != nil {
		t.Fatalf("sqlpool Acquire 失败: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	store, err := NewPostgresContextStateStore(handle)
	if err != nil {
		t.Fatalf("NewPostgresContextStateStore 失败: %v", err)
	}
	return store
}
