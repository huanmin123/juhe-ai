package modelcheckowner

// pgkit_test.go 是 modelcheckowner 测试迁真实 PG 的共享基建
// （PLAN-20261024T051925945Z SQLite 与内存模式彻底退役，modelcheckowner 切片）：
// pgtestkit 每测试独立库 + J3b/Business 生产 PG 臂（Store mode=postgres +
// juhe_j3b schema 限定 + $n 占位符，与生产 PG 装配同一方言）。
//
// 三组分类（D9/D17）：
//   - 组A（被测对象有 PG 臂且断言可逐字节保持）：Store/QualityProjector/
//     HealthSyncRetryExecutor/Runtime/SchedulerStore/TrustStore/Business 面
//     测试走本文件 newJ3BPGDatabase/newJ3BPGTestDatabase 夹具；测试体断言与
//     种子值逐字节保持，夹具 DDL 与 maintenance 生产 DDL
//     （j3bmodelcheck/bootstrap.go juhe_j3b 段）按列对齐类型，测试体内手写
//     SQL 经 j3bQ 对齐生产 PG 臂（juhe_j3b 限定 + $n）。夹具按测试语义保留
//     刻意缺表/缺列的漂移臂，不复制生产 CHECK/外键（groups 夹具同先例）。
//   - 组B（SQLite-only 生产臂为被测对象，随生产 sqlite 臂删除批次同批退役，
//     不提前迁移也不提前删除）：CheckBusinessSQLiteSchema 直测族
//     （business_schema_test.go / business_schema_exprindex_test.go）与
//     w14m_schema_helpers_test.go 的 SQLite 臂。
//   - scripted 假驱动/内存 fake（w13g2 failpoint 驱动族、内存 fake 端口）与
//     纯函数测试不依赖真库，保持原状；w14m 的 pgx 驱动包装注入臂（inner 已
//     是 pgx）与 w11e/w15b/postgres_smoke 既有 PG 门禁测试不在迁移面。
//
// 连接串由 pgtestkit 自 .local dev env 解析，内容不进入任何文件、日志或断言。

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// j3bTestSchema 是 J3b 生产 PG 臂的固定 schema（store.go OpenStore postgres
// 臂同名常量语义），夹具表与测试手写 SQL 都限定在该 schema 内。
const j3bTestSchema = "juhe_j3b"

// j3bTablePattern 是测试手写 seed/校验 SQL 的裸表名改写（与生产 PG 臂
// s.table 的 juhe_j3b. 限定、Business 面的 juhe_business. 限定同形式）。
// 同前缀族按长名优先排列。
var j3bTablePattern = regexp.MustCompile(`\b(FROM|INTO|UPDATE|JOIN)\s+(model_check_input_versions|model_check_execution_claims|model_check_observations|model_check_scheduler_tasks|model_token_intercept_baseline_versions|model_trust_latest_dirty_accounts|model_trust_observation_receipts|model_trust_aggregation_state|model_account_trust_results|account_quality_health_hourly|model_check_inputs|model_check_outcomes|model_check_runs|model_check_items|model_quality_schedules|model_quality_policies|account_quality_enforcements|system_settings|accounts|models)\b`)

// j3bBind 把测试手写 SQL 的 ? 占位符按序改写为 $n（与生产 Store.bind 同一
// 惯例；夹具 SQL 的字符串字面量不含 ?，计数不受字面量干扰）。
func j3bBind(query string) string {
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

// j3bBusinessTablePattern 前缀族改写目标 schema：Business 面表归 juhe_business。
var j3bBusinessTables = map[string]bool{
	"accounts": true, "model_quality_schedules": true, "model_quality_policies": true,
	"account_quality_enforcements": true, "system_settings": true, "models": true,
}

// j3bQ 把测试手写 SQL 对齐生产 PG 臂方言（juhe_j3b/juhe_business 限定 + $n）。
func j3bQ(query string) string {
	qualified := j3bTablePattern.ReplaceAllStringFunc(query, func(match string) string {
		parts := j3bTablePattern.FindStringSubmatch(match)
		if j3bBusinessTables[parts[2]] {
			return parts[1] + " juhe_business." + parts[2]
		}
		return parts[1] + " " + j3bTestSchema + "." + parts[2]
	})
	return j3bBind(qualified)
}

// newJ3BPGTestDatabase 创建 pgtestkit 隔离 PG 测试库、注入 juhe_j3b schema
// 与调用方给出的夹具 DDL（PG 方言，测试按语义自带建表/漂移变体），并返回
// 完整句柄（DSN 供 OpenStore postgres 配置使用）。
func newJ3BPGTestDatabase(t *testing.T, ddl ...string) *pgtestkit.TestDatabase {
	t.Helper()
	return pgtestkit.NewTestDatabase(t, func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+j3bTestSchema); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS juhe_business`); err != nil {
			return err
		}
		for _, statement := range ddl {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("执行 J3b 夹具 DDL 失败: %w", err)
			}
		}
		return nil
	})
}

// newJ3BPGDatabase 等价于 newJ3BPGTestDatabase(...).DB。
func newJ3BPGDatabase(t *testing.T, ddl ...string) *sql.DB {
	t.Helper()
	return newJ3BPGTestDatabase(t, ddl...).DB
}

// j3bPGConfig 构造 OpenStore 的生产 postgres 配置（门控全开，与
// testSQLiteConfig 同形态）。
func j3bPGConfig(dsn string) Config {
	return Config{Enabled: true, StoreMode: "postgres", PostgresURL: dsn, BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true}
}

// testSQLiteConfig 构造 OpenStore 的 sqlite 配置（门控全开）。仅剩尚未迁移
// 的组A测试与无真实建库的门控测试在用；迁移完成后随最后一个调用方删除。
func testSQLiteConfig(path string) Config {
	return Config{Enabled: true, StoreMode: "sqlite", DatabasePath: path, BusinessHandoffConfirmed: true, NodeWriterStopped: true, SchemaReady: true, HealthBoundaryReady: true, RuntimeReady: true}
}

// j3bNewStore 以生产 PG 臂形态直构 Store（与 &Store{db: db, mode: "sqlite"}
// 同构，schema 固定 juhe_j3b）。
func j3bNewStore(db *sql.DB) *Store {
	return &Store{db: db, mode: "postgres", schema: j3bTestSchema}
}

// j3bRetryRunsFixtureDDL 是健康重试扫描测试族的 16 列最小 model_check_runs
// 夹具（列型与生产 bootstrap 对齐，无生产 CHECK/外键）。
func j3bRetryRunsFixtureDDL() string {
	return `CREATE TABLE ` + j3bTestSchema + `.model_check_runs (id TEXT PRIMARY KEY,status TEXT,account_id TEXT,system_account_id TEXT,provider_code TEXT,model TEXT,profile TEXT,level TEXT,score INTEGER,schedule_id TEXT,policy_snapshot_json TEXT,quality_decision_json TEXT,request_summary_json TEXT,finished_at TEXT,quality_health_sync_status TEXT,updated_at TEXT)`
}

// j3bHealthHourlyDDL 是健康投影测试族的 account_quality_health_hourly 夹具
// （列集与类型照抄生产 bootstrap DDL，去生产 CHECK/外键）。
func j3bHealthHourlyDDL() string {
	return `CREATE TABLE ` + j3bTestSchema + `.account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`
}

// j3bRunsSyncTinyDDL / j3bRunsSyncDecisionDDL 是质量投影测试的 run 行最小
// 夹具（后者含 quality_decision_json 合并面）。
const (
	j3bRunsSyncTinyDDL     = `CREATE TABLE ` + j3bTestSchema + `.model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,updated_at TEXT)`
	j3bRunsSyncDecisionDDL = `CREATE TABLE ` + j3bTestSchema + `.model_check_runs (id TEXT PRIMARY KEY,quality_health_sync_status TEXT,quality_decision_json TEXT,updated_at TEXT)`
)

// j3bRuntimeFixtureDDLs 是 runtimeTestDDL()（SQLite 13 表夹具，供 failpoint/
// scripted 臂继续使用）的 PG 方言对齐版：列集逐列一致，时间列与 payload/
// committed 列型对齐 maintenance 生产 DDL（j3bmodelcheck/bootstrap.go）。
// outcomes.payload 按生产 JSONB；需要原始字节摘要回读语义的测试自带 text
// 变体（见 store_test.go TestLoadInputAndListCommittedOutcomesVerifyDigests）。
func j3bRuntimeFixtureDDLs() []string {
	s := j3bTestSchema + "."
	return []string{
		`CREATE TABLE ` + s + `model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE ` + s + `model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version BIGINT NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL)`,
		`CREATE TABLE ` + s + `model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE ` + s + `model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL)`,
		`CREATE TABLE ` + s + `model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL, trigger_kind TEXT NOT NULL, schedule_id TEXT, trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0, trusted_comparison_available INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, request_summary_json TEXT NOT NULL, result_summary_json TEXT NOT NULL, policy_snapshot_json TEXT NOT NULL, quality_decision_json TEXT NOT NULL, probe_set_version TEXT NOT NULL, started_at TEXT NOT NULL, trace_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, level TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, message TEXT NOT NULL, finished_at TEXT, duration_ms INTEGER, error_code TEXT, error_message TEXT, quality_health_sync_status TEXT)`,
		`CREATE TABLE ` + s + `model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL, error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE ` + s + `model_check_observations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, provider_code TEXT NOT NULL, requested_model TEXT NOT NULL, mapped_upstream_model TEXT NOT NULL, probe_family TEXT NOT NULL, observation_status TEXT NOT NULL, identity_status TEXT NOT NULL, mapping_status TEXT NOT NULL, protocol_status TEXT NOT NULL, evidence_coverage INTEGER NOT NULL, created_at TEXT NOT NULL, aggregation_completed_at TEXT)`,
		`CREATE TABLE ` + s + `account_quality_health_hourly (account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, stat_hour TEXT NOT NULL, observed_at TEXT NOT NULL, model_check_run_id TEXT NOT NULL, model TEXT NOT NULL, profile TEXT NOT NULL, score INTEGER NOT NULL, threshold INTEGER NOT NULL, level TEXT NOT NULL, error_code TEXT, error_message TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(account_id,stat_hour))`,
		`CREATE TABLE ` + s + `model_account_trust_results (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, identity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', mapping_status TEXT NOT NULL DEFAULT 'unknown', usage_integrity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', protocol_status TEXT NOT NULL DEFAULT 'insufficient_evidence', evidence_status TEXT NOT NULL DEFAULT 'insufficient', evidence_coverage INTEGER NOT NULL DEFAULT 0, observation_count INTEGER NOT NULL DEFAULT 0, reason_codes_json TEXT NOT NULL DEFAULT '[]', last_observed_id TEXT, last_observed_at TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(system_account_id,account_id,requested_model))`,
		`CREATE TABLE ` + s + `model_trust_latest_dirty_accounts (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, dirty_reason TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(system_account_id,account_id,requested_model))`,
		`CREATE TABLE ` + s + `model_trust_observation_receipts (observation_id TEXT PRIMARY KEY, observation_created_at TEXT NOT NULL, processed_at TEXT NOT NULL)`,
		`CREATE TABLE ` + s + `model_trust_aggregation_state (scope_key TEXT PRIMARY KEY, cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, updated_at TEXT NOT NULL)`,
	}
}

// j3bSchedulerTasksProdDDL 是 model_check_scheduler_tasks 的生产对齐夹具
// （bootstrap DDL 同型，去生产 CHECK；payload JSONB）。
func j3bSchedulerTasksProdDDL() string {
	return `CREATE TABLE ` + j3bTestSchema + `.model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TIMESTAMPTZ NOT NULL,claim_owner TEXT,claim_until TIMESTAMPTZ,fence_token BIGINT NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TIMESTAMPTZ,payload JSONB NOT NULL DEFAULT '{}'::jsonb,updated_at TIMESTAMPTZ NOT NULL)`
}

// j3bSchedulerTasksTextPayloadDDL 是 SQLSchedulerSource 字节精确 payload 回读
// 断言用的 text payload 变体（生产 JSONB 规范化渲染会改写 JSON 文本字节）。
func j3bSchedulerTasksTextPayloadDDL() string {
	return `CREATE TABLE ` + j3bTestSchema + `.model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TIMESTAMPTZ NOT NULL,claim_owner TEXT,claim_until TIMESTAMPTZ,fence_token BIGINT NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TIMESTAMPTZ,payload TEXT NOT NULL,updated_at TIMESTAMPTZ NOT NULL)`
}

// runtimeTestDDL 是 SQLite 方言的 13 表运行时夹具（组A 已由
// j3bRuntimeFixtureDDLs 承接）；现仅剩 scripted failpoint 驱动与内存库消费方
// （w13g2/wb_*/w14m 家族），随其退役批次一并删除。
func runtimeTestDDL() []string {
	return []string{
		`CREATE TABLE model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version INTEGER NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TEXT NOT NULL, expires_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE TABLE model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token INTEGER NOT NULL, claim_until TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token INTEGER NOT NULL, observed_at TEXT NOT NULL, stored_at TEXT NOT NULL, payload BLOB NOT NULL, payload_digest TEXT NOT NULL, committed INTEGER NOT NULL)`,
		`CREATE TABLE model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL, trigger_kind TEXT NOT NULL, schedule_id TEXT, trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0, trusted_comparison_available INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, request_summary_json TEXT NOT NULL, result_summary_json TEXT NOT NULL, policy_snapshot_json TEXT NOT NULL, quality_decision_json TEXT NOT NULL, probe_set_version TEXT NOT NULL, started_at TEXT NOT NULL, trace_id TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, level TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, message TEXT NOT NULL, finished_at TEXT, duration_ms INTEGER, error_code TEXT, error_message TEXT, quality_health_sync_status TEXT)`,
		`CREATE TABLE model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL, score INTEGER NOT NULL, max_score INTEGER NOT NULL, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL, error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE model_check_observations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, provider_code TEXT NOT NULL, requested_model TEXT NOT NULL, mapped_upstream_model TEXT NOT NULL, probe_family TEXT NOT NULL, observation_status TEXT NOT NULL, identity_status TEXT NOT NULL, mapping_status TEXT NOT NULL, protocol_status TEXT NOT NULL, evidence_coverage INTEGER NOT NULL, created_at TEXT NOT NULL, aggregation_completed_at TEXT)`,
		`CREATE TABLE account_quality_health_hourly (account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, stat_hour TEXT NOT NULL, observed_at TEXT NOT NULL, model_check_run_id TEXT NOT NULL, model TEXT NOT NULL, profile TEXT NOT NULL, score INTEGER NOT NULL, threshold INTEGER NOT NULL, level TEXT NOT NULL, error_code TEXT, error_message TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(account_id,stat_hour))`,
		`CREATE TABLE model_account_trust_results (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, identity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', mapping_status TEXT NOT NULL DEFAULT 'unknown', usage_integrity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', protocol_status TEXT NOT NULL DEFAULT 'insufficient_evidence', evidence_status TEXT NOT NULL DEFAULT 'insufficient', evidence_coverage INTEGER NOT NULL DEFAULT 0, observation_count INTEGER NOT NULL DEFAULT 0, reason_codes_json TEXT NOT NULL DEFAULT '[]', last_observed_id TEXT, last_observed_at TEXT, updated_at TEXT NOT NULL, PRIMARY KEY(system_account_id,account_id,requested_model))`,
		`CREATE TABLE model_trust_latest_dirty_accounts (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, dirty_reason TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(system_account_id,account_id,requested_model))`,
		`CREATE TABLE model_trust_observation_receipts (observation_id TEXT PRIMARY KEY, observation_created_at TEXT NOT NULL, processed_at TEXT NOT NULL)`,
		`CREATE TABLE model_trust_aggregation_state (scope_key TEXT PRIMARY KEY, cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, updated_at TEXT NOT NULL)`,
	}
}
