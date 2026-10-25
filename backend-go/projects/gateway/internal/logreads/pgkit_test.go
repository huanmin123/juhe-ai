package logreads

// pgkit_test.go 是 logreads 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，logreads 测试迁移
// 切片）：pgtestkit 每测试独立库，三族数据集读取器以生产形态
// 挂载（juhe_dataset schema 限定 + mode.bind $n 占位符，与 compose.go 的
// pgDialect 装配同一形态）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：readsTestEnv / w9bBlobDB /
//     routes_test 的 F4 环境走本文件夹具。schema 以生产 SQL 为准：F3 audit
//     数据集五表经 auditlog.OpenStore(ModePostgres) 的生产 EnsureSchema 建
//     （juhe_dataset schema、超级分组补列 route_group_id/member_group_id 与
//     blob GC schema，与生产启动时序一致，不复刻 DDL）；F1 runtime 四表与
//     F5 public_api_logs 属 jobs runtimelog / maintenance pg_schema_dataset
//     跨模块生产 SQL，由本夹具逐字节摘录生产 DDL（timestamptz / bigint /
//     integer / text 列型精确对齐）。测试体手写 SQL 经 fixtureDatasetSQL 对
//     齐生产 PG 方言（juhe_dataset 限定 + $n）；boolean 列种子用 true/false；
//     timestamptz 列回读 time.Time 经 readISOText 输出 canonical UTC 毫秒文
//     本，与 SQLite 文本回读逐字节一致。
//   - 组B（scripted SQLite 故障注入环境）：w11f_faults_test.go 的
//     原 w11fFaultEnv scripted 故障注入族与其 SQLite DDL 夹具已随 sqlite
//     生产臂退役（2026-10-24 存储形态收口），
//     随生产 sqlite 臂删除批次同批退役。
//   - 纯函数矩阵与 stub reader 测试不依赖真库，保持原状。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// logreadsPGFixtureSchema 是 auth 面 + F1 runtime + F5 public 数据集的夹具
// schema。juhe_business 两表照抄 maintenance 生产业务 DDL 的列集与类型（同
// groups 夹具先例）；juhe_dataset 下 F1 四表与 F5 表逐字节摘录生产 SQL：
// F1 摘自 projects/jobs/internal/runtimelog/schema.go 的 postgresSchema（只
// 保留读取器消费的表，读取索引无行为语义故省略），F5 摘自
// projects/maintenance/internal/schema/pg_schema_dataset.go（text 时间列型
// 是 Node F5 管道的生产契约）。F3 audit 五表不在此列——它们走下方生产
// EnsureSchema 路径。
const logreadsPGFixtureSchema = `
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
CREATE TABLE IF NOT EXISTS juhe_business.system_settings (
  system_account_id text NOT NULL,
  key text NOT NULL,
  value_json text NOT NULL,
  updated_at text NOT NULL,
  PRIMARY KEY (system_account_id, key)
);
CREATE SCHEMA IF NOT EXISTS juhe_dataset;
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_logs (
  id text PRIMARY KEY,
  log_file text,
  log_offset bigint,
  line_number integer,
  time timestamptz NOT NULL,
  level text NOT NULL,
  trace_id text,
  event text,
  message text,
  error_message text,
  raw_json text NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_facet_summary (
  bucket_key text PRIMARY KEY,
  total_count integer NOT NULL DEFAULT 0,
  earliest_time timestamptz,
  latest_time timestamptz,
  updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_level_facets (
  bucket_key text NOT NULL,
  level text NOT NULL,
  count integer NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (bucket_key, level)
);
CREATE TABLE IF NOT EXISTS juhe_dataset.runtime_log_event_facets (
  bucket_key text NOT NULL,
  event text NOT NULL,
  count integer NOT NULL DEFAULT 0,
  latest_time timestamptz,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (bucket_key, event)
);
CREATE TABLE IF NOT EXISTS juhe_dataset.public_api_logs (
  id text PRIMARY KEY,
  trace_id text,
  source_ref_id text,
  source_name text,
  token_id text,
  token_name text,
  token_prefix text,
  is_test_token integer NOT NULL DEFAULT 0,
  method text NOT NULL,
  path text NOT NULL,
  query_string text,
  client_ip text,
  user_agent text,
  status_code integer,
  success integer NOT NULL DEFAULT 0,
  duration_ms integer,
  request_size_bytes bigint NOT NULL DEFAULT 0,
  response_size_bytes bigint NOT NULL DEFAULT 0,
  request_capture_status text NOT NULL DEFAULT 'empty',
  response_capture_status text NOT NULL DEFAULT 'empty',
  request_data_json text NOT NULL DEFAULT '{}',
  response_data_json text NOT NULL DEFAULT '{}',
  error_code text,
  error_message text,
  started_at text NOT NULL,
  ended_at text NOT NULL,
  created_at text NOT NULL
)
`

// fixtureDatasetTablePattern 是夹具手写 seed/校验 SQL 的裸表名改写（与生产
// 读取器 s.mode.table 的 "juhe_dataset". 限定同形式）。同前缀族按长名优先排列。
var fixtureDatasetTablePattern = regexp.MustCompile(`\b(FROM|INTO|UPDATE|JOIN)\s+(audit_error_groups|audit_log_attempts|audit_payload_blobs|audit_payload_refs|audit_logs|runtime_log_event_facets|runtime_log_facet_summary|runtime_log_level_facets|runtime_logs|public_api_logs)\b`)

// fixtureDatasetBind 把夹具手写 SQL 的 ? 占位符按序改写为 $n（与生产读取器
// mode.bind 同一惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
func fixtureDatasetBind(query string) string {
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

// fixtureDatasetSQL 把夹具手写 SQL 对齐生产 PG 方言（juhe_dataset 限定 + $n）。
func fixtureDatasetSQL(query string) string {
	return fixtureDatasetBind(fixtureDatasetTablePattern.ReplaceAllString(query, `$1 "juhe_dataset"."$2"`))
}

// newLogreadsPGTestDatabase 创建 pgtestkit 隔离 PG 测试库、注入夹具 schema，
// 并经生产 auditlog store 的 EnsureSchema 建 F3 audit 数据集（生产建表路径，
// 含超级分组补列与 blob GC schema）；建表后即关池，表留在测试库中。F3 表的
// 外键按 groups 夹具先例放宽（读取器 SELECT-only，FK 无读取语义，而夹具
// seed 行的插入顺序不满足生产外键链）；UNIQUE 等行为性约束逐字保留。
func newLogreadsPGTestDatabase(t *testing.T) *pgtestkit.TestDatabase {
	t.Helper()
	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, logreadsPGFixtureSchema)
		return err
	})
	// 池上限与 LoadConfig 缺省一致（20/10，满足 sqlpool.ValidatePoolLimits）；
	// 零值会让 OpenStore 落到 1000/1000 而被池校验拒绝。
	store, err := auditlog.OpenStore(auditlog.Config{
		InstanceID:           "logreads-pgkit",
		PostgresURL:          testDB.DSN,
		PostgresMaxOpenConns: 20,
		PostgresMaxIdleConns: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// F3 外键放宽（groups 夹具先例）：读取器 SELECT-only，FK 无读取语义，而
	// 夹具 seed 行的插入顺序不满足生产外键链；UNIQUE 等行为性约束逐字保留。
	if _, err := testDB.DB.ExecContext(context.Background(), `ALTER TABLE juhe_dataset.audit_log_attempts DROP CONSTRAINT IF EXISTS audit_log_attempts_audit_log_id_fkey;
ALTER TABLE juhe_dataset.audit_payload_refs DROP CONSTRAINT IF EXISTS audit_payload_refs_audit_log_id_fkey;
ALTER TABLE juhe_dataset.audit_payload_refs DROP CONSTRAINT IF EXISTS audit_payload_refs_attempt_id_fkey;
ALTER TABLE juhe_dataset.audit_payload_refs DROP CONSTRAINT IF EXISTS audit_payload_refs_headers_blob_id_fkey;
ALTER TABLE juhe_dataset.audit_payload_refs DROP CONSTRAINT IF EXISTS audit_payload_refs_body_blob_id_fkey`); err != nil {
		t.Fatal(err)
	}
	return testDB
}

// newLogreadsPGDatabase 是组A数据集读取器测试的入口：返回已建齐 F1/F3/F5
// 生产 schema 的连接池。
func newLogreadsPGDatabase(t *testing.T) *sql.DB {
	t.Helper()
	return newLogreadsPGTestDatabase(t).DB
}
