package manualtestrepo

// pgkit_test.go 是手动测试任务仓储测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，manualtestrepo 测试
// 切片）：pgtestkit 每测试独立库 + 生产 PG 方言（New(Config{Postgres: true})，
// bindSQL $n 改写、juhe_business 限定、原生 boolean/timestamptz 与生产装配
// 同方言）。表 DDL 与 maintenance 权威生产 DDL（pg_schema_business_tables.go
// 的 account_test_tasks / account_test_sessions / account_test_session_tasks）
// 精确对齐；按 groups 夹具先例放宽一处：不建 FOREIGN KEY（seed 行不满足生产
// 外键链，如先插 session_tasks 后补 task 行）。
//
// 三组分类（D9/D17）：
//   - 组A（bind() 方言共享 SQL，无独立 sqlite 结构臂）：仓储行为/生命周期/
//     维护/错误注入族走本文件夹具，断言逐字节保持；PG 无同构前提的
//     SQLite 弱类型臂（cancel_requested 存非法整数）保留在
//     w12h_repo_scan_arms_test.go 的 newTestRepoSQLite（组B），随生产
//     sqlite 方言分支删除批次同批退役。
//   - 故障注入经 pgAbortTrigger 转写为 PG 原生触发器函数（RAISE EXCEPTION
//     文案逐字保留），断言不变。
//   - 纯函数/方言 helper 测试（bindSQL 改写、bool/instant 参数、文案）不依赖
//     真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// manualTestBusinessSchema 是仓储契约的三张业务表（与 maintenance 权威生产
// DDL 逐列对齐；timestamptz + 原生 boolean + sessions 状态 CHECK）。
const manualTestBusinessSchema = `
CREATE SCHEMA IF NOT EXISTS juhe_business;
CREATE TABLE IF NOT EXISTS juhe_business.account_test_tasks (
  id text PRIMARY KEY,
  account_id text NOT NULL,
  account_name text NOT NULL,
  provider_protocol_profile_id text NOT NULL,
  protocol_code text NOT NULL,
  protocol_version text NOT NULL,
  account_type text NOT NULL,
  request_system_account_id text NOT NULL,
  request_role text NOT NULL,
  request_system_account_filter_id text,
  diagnostics text NOT NULL DEFAULT 'full',
  model text,
  test_endpoint_mode text,
  draft_account_encrypted text,
  status text NOT NULL DEFAULT 'queued',
  status_message text,
  result_json text,
  error_message text,
  cancel_requested boolean NOT NULL DEFAULT false,
  queued_at timestamptz NOT NULL,
  queued_deadline_at timestamptz,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_business.account_test_sessions (
  id text PRIMARY KEY,
  request_system_account_id text NOT NULL,
  request_role text NOT NULL,
  request_system_account_filter_id text,
  status text NOT NULL DEFAULT 'running',
  cancel_reason text,
  last_heartbeat_at timestamptz NOT NULL,
  cancel_requested_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CHECK (status IN ('running', 'canceled', 'expired', 'completed'))
);
CREATE TABLE IF NOT EXISTS juhe_business.account_test_session_tasks (
  session_id text NOT NULL,
  task_id text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (session_id, task_id)
)`

// openTestRepo 在 pgtestkit 隔离 PG 测试库上构建生产 PG 方言仓储
// （返回形状与旧 SQLite 夹具一致：句柄供测试侧断言查询与注入）。
func openTestRepo(t *testing.T) (*Repo, *sql.DB, func()) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, manualTestBusinessSchema)
		return err
	})
	current := time.Now().UTC()
	repo, err := New(Config{DB: db, Postgres: true, Now: func() time.Time { return current }})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() { _ = db.Close() }
	t.Cleanup(cleanup)
	return repo, db, cleanup
}

// seedTask 注入一条任务行（cancelRequested 以 0/1 表达 false/true，与
// timestamptz 列经原生时间绑定；account_name/protocol 族为生产 DDL 的
// NOT NULL 无默认列，种子统一补齐）。
func seedTask(t *testing.T, db *sql.DB, id string, status string, cancelRequested int, queuedAt time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO juhe_business.account_test_tasks (
			id, account_id, account_name, provider_protocol_profile_id, protocol_code,
			protocol_version, account_type, request_system_account_id, request_role, diagnostics,
			status, status_message, cancel_requested, queued_at, created_at, updated_at
		) VALUES ($1, 'acc-1', 'w12h-seed', '', 'chat_completions', 'v1', 'api_key', 'viewer-1', 'user', 'limited', $2, '等待后台测试', $3, $4, $4, $4)`,
		id, status, cancelRequested != 0, queuedAt.UTC()); err != nil {
		t.Fatal(err)
	}
}

// pgAbortTrigger 在 PG 上等价 SQLite 的 RAISE(ABORT) 注入：创建触发器函数与
// BEFORE <event> 触发器，命中时抛出 message（错误文案逐字保留）。每测试独立
// 库 + 函数名含事件，无需全局去重。
func pgAbortTrigger(t *testing.T, db *sql.DB, name, event, table, message string) {
	t.Helper()
	function := name + "_" + event + "_fn"
	_, err := db.Exec(`CREATE FUNCTION ` + function + `() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN RAISE EXCEPTION '` + message + `'; END $fn$;
CREATE TRIGGER ` + name + ` BEFORE ` + event + ` ON juhe_business.` + table + ` FOR EACH ROW EXECUTE FUNCTION ` + function + `()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS ` + name + ` ON juhe_business.` + table)
		_, _ = db.Exec(`DROP FUNCTION IF EXISTS ` + function + `()`)
	})
}
