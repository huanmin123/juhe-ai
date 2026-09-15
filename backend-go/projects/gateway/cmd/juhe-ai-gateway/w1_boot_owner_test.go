//go:build windows

package main

// w1_boot_owner_test.go —— juhe-ai-gateway owner 模式完整启动的二进制级覆盖
// 测试（TestW1BOwnerFullBoot）：复用 w1_boot_cover_test.go 的插桩二进制构建
// 与覆盖目录登记帮手（w1bBuildCoverBinary / w1bCoverageDir /
// w1bScenarioEnv / w1bAppendCoverageManifest / w1bFreePort /
// w1bSetNewProcessGroup / w1bSendCtrlBreak / w1bWriteCutoverEvidence），把
// main.go 的 owner 分支（loadRuntimeConfig → business owner gate → 双份
// cutover 证据 → J3b owner 装配 → F3/F4 store + lease → composeSystemAPI →
// supervisor 组件族 → /health 就绪）跑到真实就绪，再以
// CTRL_BREAK_EVENT 触发优雅关闭并断言 exit 0。
//
// 场景 fixture（全部位于 t.TempDir()）：
//
//   - 业务库 business.sqlite：maintenance bootstrap 的
//     EnsureSQLiteSchema(SQLiteSchemaBusiness) + SeedSQLiteBusiness（X05
//     同款 ensure+seed，表达式索引误报已由 business_schema.go
//     errSQLiteIndexExpression 修复，无需再 DROP 索引）。该文件同时充当
//     JUHE_AI_DATABASE_PATH / JUHE_AI_BUSINESS_DATABASE_PATH /
//     JUHE_AI_J3B_BUSINESS_DATABASE_PATH / F4 只读镜像与 F3 只读 settings
//     镜像（部署契约允许镜像路径指向业务库本体）。
//   - J3b 专属库 j3b-dedicated.sqlite：复刻 maintenance
//     internal/j3bmodelcheck 的 sqliteSchemaStatements + ensure 升级列
//     （internal 包不可跨模块导入，DDL 按原语句顺序复制）。
//   - cutover 证据两份（business 与 J3b，owner epoch 一致、freshness 在
//     有效期），构造方式与 K5 场景相同。
//   - miniredis 两实例：cache 与 state 各一；J3b circuit runtime 共用 state
//     实例。state 实例预写 runtime-index-meta
//     （juhe-ai:w1v2:account-circuit:gateway-account-circuit:runtime-index-meta，
//     HSET version=1 / status=ready / ownerMode=go-runtime-state-v1，与
//     circuitruntime.Store.CheckReady 的 HMGet 契约一致）。
//   - 六库角色路径两两不同（physical identity gate D-44）；runtime-log 文件
//     预建为空文件（F1 jobs 拥有 schema，读面缺失降级，不阻塞启动）。
//
// 就绪信号：/health 返回 200 且 body ready=true（main.go healthHandler 的
// ready = audit && operation && j3b && sessionRetention && circuitRuntime
// 全组件 running）。就绪后额外断言管理面 /__aisys__/api/health 200。
// 关闭：CTRL_BREAK_EVENT（同 J 场景的 CREATE_NEW_PROCESS_GROUP 路径），
// 断言 exit 0、无 fail 文案输出。全程有界：30s 就绪轮询 + 30s 退出等待。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/bootstrap"
	goredis "github.com/redis/go-redis/v9"

	_ "modernc.org/sqlite"
)

const (
	w1v2OwnerEpoch       = "epoch-w1v2"
	w1v2J3bRedisNS       = "w1v2"
	w1v2RuntimeSecret    = "w1v2-owner-full-boot-secret"
	w1v2BootPollTimeout  = 30 * time.Second
	w1v2BootStopTimeout  = 30 * time.Second
	w1v2BootTotalTimeout = 85 * time.Second
)

// w1v2J3bSQLiteSchemaStatements 复刻 maintenance internal/j3bmodelcheck
// bootstrap.go 的 sqliteSchemaStatements（J3b 专属 SQLite 专属库 DDL）。
var w1v2J3bSQLiteSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS model_check_input_versions (identity_key TEXT PRIMARY KEY,next_version INTEGER NOT NULL,updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS model_check_inputs (input_id TEXT PRIMARY KEY,identity_key TEXT NOT NULL,input_version INTEGER NOT NULL,input_digest TEXT NOT NULL,target_id TEXT NOT NULL,config_revision TEXT NOT NULL,policy_revision TEXT NOT NULL,trigger TEXT NOT NULL,issued_at TEXT NOT NULL,expires_at TEXT NOT NULL,payload BLOB NOT NULL,UNIQUE(identity_key,input_version),UNIQUE(identity_key,input_digest))`,
	`CREATE TABLE IF NOT EXISTS model_check_execution_claims (input_id TEXT PRIMARY KEY,claim_token TEXT NOT NULL,outcome_id TEXT NOT NULL,owner_id TEXT NOT NULL,fence_token INTEGER NOT NULL,claim_until TEXT NOT NULL,updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS model_check_outcomes (outcome_id TEXT PRIMARY KEY,input_id TEXT NOT NULL UNIQUE,input_digest TEXT NOT NULL,fence_token INTEGER NOT NULL,observed_at TEXT NOT NULL,stored_at TEXT NOT NULL,payload BLOB NOT NULL,payload_digest TEXT NOT NULL,committed INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS model_check_runs (id TEXT PRIMARY KEY,system_account_id TEXT NOT NULL,actor_system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT NOT NULL,account_id TEXT,model TEXT NOT NULL,profile TEXT NOT NULL,trigger_kind TEXT NOT NULL,schedule_id TEXT,status TEXT NOT NULL,level TEXT NOT NULL,score INTEGER NOT NULL,max_score INTEGER NOT NULL,message TEXT NOT NULL,request_summary_json TEXT NOT NULL,result_summary_json TEXT NOT NULL,policy_snapshot_json TEXT NOT NULL,quality_decision_json TEXT NOT NULL,probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1',started_at TEXT NOT NULL,trace_id TEXT,quality_health_sync_status TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,finished_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS model_check_items (id TEXT PRIMARY KEY,run_id TEXT NOT NULL,item_key TEXT NOT NULL,item_type TEXT NOT NULL,status TEXT NOT NULL,score INTEGER NOT NULL,max_score INTEGER NOT NULL,duration_ms INTEGER,trace_id TEXT,evidence_summary_json TEXT NOT NULL,error_code TEXT,error_message TEXT,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS model_check_observations (id TEXT PRIMARY KEY,run_id TEXT NOT NULL,system_account_id TEXT NOT NULL,account_id TEXT NOT NULL,provider_code TEXT NOT NULL,provider_protocol_profile_id TEXT NOT NULL DEFAULT 'unknown',endpoint_family TEXT NOT NULL DEFAULT 'unknown',requested_model TEXT NOT NULL,mapped_upstream_model TEXT NOT NULL,observed_model TEXT,mapping_applied INTEGER NOT NULL DEFAULT 0,upstream_bucket_hmac TEXT NOT NULL DEFAULT '',cohort_key_hmac TEXT NOT NULL DEFAULT '',population_key_hmac TEXT NOT NULL DEFAULT '',probe_key_hmac TEXT NOT NULL DEFAULT '',system_fingerprint_hmac TEXT,probe_family TEXT NOT NULL,probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1',tokenizer_version TEXT NOT NULL DEFAULT 'unavailable',feature_version TEXT NOT NULL DEFAULT 'none',round_index INTEGER NOT NULL DEFAULT 0,padding_tokens INTEGER NOT NULL DEFAULT 0,local_input_tokens INTEGER NOT NULL DEFAULT 0,reported_input_tokens INTEGER,cached_input_tokens INTEGER,constraint_passed INTEGER,feature_1 REAL,feature_2 REAL,feature_3 REAL,feature_4 REAL,feature_5 REAL,feature_6 REAL,feature_7 REAL,feature_8 REAL,observation_status TEXT NOT NULL,identity_status TEXT NOT NULL,mapping_status TEXT NOT NULL,protocol_status TEXT NOT NULL,evidence_coverage INTEGER NOT NULL,trace_id TEXT,created_at TEXT NOT NULL,aggregation_completed_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS account_quality_health_hourly (account_id TEXT NOT NULL,system_account_id TEXT NOT NULL,provider_code TEXT NOT NULL,stat_hour TEXT NOT NULL,observed_at TEXT NOT NULL,model_check_run_id TEXT NOT NULL,model TEXT NOT NULL,profile TEXT NOT NULL,score INTEGER NOT NULL,threshold INTEGER NOT NULL,level TEXT NOT NULL,error_code TEXT,error_message TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(account_id,stat_hour))`,
	`CREATE TABLE IF NOT EXISTS model_check_scheduler_tasks (id TEXT PRIMARY KEY,kind TEXT NOT NULL,due_at TEXT NOT NULL,claim_owner TEXT,claim_until TEXT,fence_token INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',last_error TEXT,completed_at TEXT,payload BLOB NOT NULL,updated_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_scheduler_tasks_due ON model_check_scheduler_tasks(kind,due_at,claim_until,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_retry ON model_check_runs(quality_health_sync_status,updated_at,id)`,
	`CREATE TABLE IF NOT EXISTS model_token_intercept_baseline_versions (cohort_key_hmac TEXT NOT NULL,requested_model TEXT NOT NULL,tokenizer_version TEXT NOT NULL,probe_set_version TEXT NOT NULL,baseline_version INTEGER NOT NULL,version_status TEXT NOT NULL DEFAULT 'calibration_pending',evidence_status TEXT NOT NULL DEFAULT 'insufficient',independent_source_count INTEGER NOT NULL DEFAULT 0,retained_source_count INTEGER NOT NULL DEFAULT 0,excluded_source_count INTEGER NOT NULL DEFAULT 0,median_intercept REAL,mad_intercept REAL,q10_intercept REAL,q90_intercept REAL,strong_threshold_intercept REAL,strong_gate_enabled INTEGER NOT NULL DEFAULT 0,calibration_note TEXT,first_observed_at TEXT NOT NULL,last_observed_at TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,baseline_version))`,
	`CREATE INDEX IF NOT EXISTS idx_model_token_intercept_baseline_active ON model_token_intercept_baseline_versions(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,version_status,baseline_version)`,
	`CREATE TABLE IF NOT EXISTS model_account_trust_results (system_account_id TEXT NOT NULL,account_id TEXT NOT NULL,requested_model TEXT NOT NULL,identity_status TEXT NOT NULL DEFAULT 'insufficient_evidence',mapping_status TEXT NOT NULL DEFAULT 'unknown',usage_integrity_status TEXT NOT NULL DEFAULT 'insufficient_evidence',protocol_status TEXT NOT NULL DEFAULT 'insufficient_evidence',evidence_status TEXT NOT NULL DEFAULT 'insufficient',evidence_coverage INTEGER NOT NULL DEFAULT 0,observation_count INTEGER NOT NULL DEFAULT 0,round_count INTEGER NOT NULL DEFAULT 0,independent_source_count INTEGER NOT NULL DEFAULT 0,identity_observation_count INTEGER NOT NULL DEFAULT 0,paired_probe_count INTEGER NOT NULL DEFAULT 0,slope REAL,intercept REAL,intercept_baseline_median REAL,intercept_baseline_mad REAL,intercept_baseline_version INTEGER,intercept_baseline_status TEXT,intercept_strong_gate_enabled INTEGER NOT NULL DEFAULT 0,identity_distance REAL,paired_distance REAL,paired_baseline_median REAL,paired_baseline_mad REAL,baseline_version INTEGER,baseline_version_status TEXT,feature_version TEXT,tokenizer_version TEXT,probe_set_version TEXT,reason_codes_json TEXT NOT NULL DEFAULT '[]',last_observed_id TEXT,last_observed_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,account_id,requested_model))`,
	`CREATE TABLE IF NOT EXISTS model_trust_latest_dirty_accounts (system_account_id TEXT NOT NULL,account_id TEXT NOT NULL,requested_model TEXT NOT NULL,dirty_reason TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,account_id,requested_model))`,
	`CREATE TABLE IF NOT EXISTS model_trust_observation_receipts (observation_id TEXT PRIMARY KEY,observation_created_at TEXT NOT NULL,processed_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS model_trust_aggregation_state (scope_key TEXT PRIMARY KEY,cursor_created_at TEXT,cursor_id TEXT,last_success_at TEXT,last_error_message TEXT,lag_seconds INTEGER,updated_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_model_account_trust_results_updated ON model_account_trust_results(updated_at,account_id,requested_model)`,
	`CREATE INDEX IF NOT EXISTS idx_model_trust_latest_dirty_updated ON model_trust_latest_dirty_accounts(updated_at,system_account_id,account_id,requested_model)`,
	`CREATE INDEX IF NOT EXISTS idx_model_trust_observation_receipts_processed ON model_trust_observation_receipts(processed_at,observation_id)`,
}

// w1v2J3bSQLiteUpgradeColumns 复刻 ensureSQLiteRunColumns 为旧文件追加的
// run 投影列（fresh 建库按原始 DDL + 这些 ALTER 与 maintenance bootstrap
// 的最终形态一致；observations/aggregation 的 ensure 列已并入上方 CREATE）。
var w1v2J3bSQLiteUpgradeColumns = []string{
	`ALTER TABLE model_check_runs ADD COLUMN target_name TEXT`,
	`ALTER TABLE model_check_runs ADD COLUMN target_owner_system_account_id TEXT`,
	`ALTER TABLE model_check_runs ADD COLUMN group_id TEXT`,
	`ALTER TABLE model_check_runs ADD COLUMN api_key_id TEXT`,
	`ALTER TABLE model_check_runs ADD COLUMN trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE model_check_runs ADD COLUMN trusted_comparison_available INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE model_check_runs ADD COLUMN duration_ms INTEGER`,
	`ALTER TABLE model_check_runs ADD COLUMN error_code TEXT`,
	`ALTER TABLE model_check_runs ADD COLUMN error_message TEXT`,
}

// w1v2PrepareBusinessSQLite 用 maintenance bootstrap 同款 ensure+seed 建
// 业务库（K 系 fail-fast 场景不会打开它；owner 完整启动的 J3b 连接、F3/F4
// 只读镜像与 composeSystemAPI preflight 都消费这一个文件）。
func w1v2PrepareBusinessSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := bootstrap.OpenSQLiteFile(path)
	if err != nil {
		t.Fatalf("打开业务库失败: %v", err)
	}
	defer db.Close()
	if _, err := bootstrap.EnsureSQLiteSchema(context.Background(), bootstrap.SQLiteSchemaBusiness, db); err != nil {
		t.Fatalf("应用真实 maintenance business DDL 失败: %v", err)
	}
	if _, err := bootstrap.SeedSQLiteBusiness(context.Background(), db, bootstrap.SeedOptions{Now: func() time.Time { return time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC) }, Secret: w1v2RuntimeSecret}); err != nil {
		t.Fatalf("seed 业务库默认数据失败: %v", err)
	}
}

// w1v2PrepareJ3bSQLite 建立 J3b 专属库（gateway 侧 modelcheckowner.Store
// 以 mode=rw 打开，文件必须预先存在且满足 CheckSchema 的表/列契约）。
func w1v2PrepareJ3bSQLite(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开 J3b 专属库失败: %v", err)
	}
	defer db.Close()
	for _, statement := range append(append([]string{}, w1v2J3bSQLiteSchemaStatements...), w1v2J3bSQLiteUpgradeColumns...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("执行 J3b 专属库 DDL 失败: %v, statement=%s", err, statement)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 J3b 专属库失败: %v", err)
	}
}

// w1v2SeedCircuitRuntimeIndex 按 circuitruntime.Store.CheckReady 的契约向
// state redis 写 runtime-index-meta（HSET version/status/ownerMode）。
func w1v2SeedCircuitRuntimeIndex(t *testing.T, addr, namespace string) {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := fmt.Sprintf("juhe-ai:%s:account-circuit:gateway-account-circuit:runtime-index-meta", namespace)
	if err := client.HSet(ctx, key,
		"version", "1",
		"status", "ready",
		"ownerMode", "go-runtime-state-v1",
	).Err(); err != nil {
		t.Fatalf("写入 account-circuit runtime-index-meta 失败: %v", err)
	}
	stored, err := client.HMGet(ctx, key, "version", "status", "ownerMode").Result()
	if err != nil || len(stored) != 3 || fmt.Sprint(stored[0]) != "1" || fmt.Sprint(stored[1]) != "ready" || fmt.Sprint(stored[2]) != "go-runtime-state-v1" {
		t.Fatalf("runtime-index-meta 回读不符: stored=%v err=%v", stored, err)
	}
}

// w1v2OwnerFailMarkers 是 main.go owner 分支 fail() 输出的特征文案采样；
// 优雅路径的 combined 输出不得包含其中任何一项。
var w1v2OwnerFailMarkers = []string{
	"load gateway runtime config",
	"verify business owner gates",
	"cutover evidence",
	"load J3b gateway owner config",
	"J3b Gateway authenticator",
	"J3b Gateway session retention",
	"J3b Gateway circuit control-plane",
	"J3b Gateway circuit runtime",
	"J3b Gateway key-model runtime",
	"J3b Gateway circuit projector",
	"J3b Gateway enforcement owner",
	"J3b Gateway recovery owner",
	"J3b Gateway quality manager",
	"J3b Gateway scheduler contract",
	"J3b Gateway tokenizer",
	"J3b Gateway model-limit source",
	"J3b Gateway usage stats timezone",
	"open J3b Gateway owner host",
	"mount J3b Gateway",
	"listen J3b Gateway management endpoint",
	"load F3 audit-log config",
	"open F3 audit-log store",
	"initialize F3 audit-log schema",
	"acquire F3 audit owner lease",
	"F3 audit owner lease held",
	"load F4 operation-log config",
	"open F4 operation-log store",
	"initialize F4 operation-log schema",
	"acquire F4 operation-log owner lease",
	"F4 operation log owner lease held",
	"compose gateway system api",
	"listen gateway system api endpoint",
	"listen gateway health endpoint",
	"create internal gateway registry",
	"shutdown gateway system api endpoint",
	"gateway system api endpoint stopped",
	"J3b management endpoint stopped",
	"gateway component supervisor stopped",
	"shutdown gateway health endpoint",
	"gateway health endpoint stopped",
	"sqlite storage preflight",
	"Gateway SQLite schema",
	"指向同一个 SQLite 物理文件",
}

// TestW1BOwnerFullBoot 场景 W1V2：owner 模式完整启动 → /health ready →
// 管理面探活 → CTRL_BREAK 优雅关闭（exit 0、无 fail 输出）。
func TestW1BOwnerFullBoot(t *testing.T) {
	exe := w1bBuildCoverBinary(t)
	root := t.TempDir()

	// ---- fixture：业务库 + J3b 专属库 ----
	businessPath := filepath.Join(root, "business.sqlite")
	w1v2PrepareBusinessSQLite(t, businessPath)
	j3bPath := filepath.Join(root, "j3b-dedicated.sqlite")
	w1v2PrepareJ3bSQLite(t, j3bPath)

	// ---- fixture：business 与 J3b 两份有效 cutover 证据（同 epoch） ----
	for _, evidenceDir := range []string{
		filepath.Join(root, "business-evidence"),
		filepath.Join(root, "j3b-evidence"),
	} {
		if err := os.MkdirAll(evidenceDir, 0o750); err != nil {
			t.Fatalf("创建 cutover evidence 目录 %s 失败: %v", evidenceDir, err)
		}
	}
	businessEvidence := w1bWriteCutoverEvidence(t, filepath.Join(root, "business-evidence"), w1v2OwnerEpoch)
	j3bEvidence := w1bWriteCutoverEvidence(t, filepath.Join(root, "j3b-evidence"), w1v2OwnerEpoch)

	// ---- fixture：miniredis cache/state 两实例 + runtime-index meta ----
	cacheRedis := miniredis.RunT(t)
	stateRedis := miniredis.RunT(t)
	w1v2SeedCircuitRuntimeIndex(t, stateRedis.Addr(), w1v2J3bRedisNS)

	// ---- fixture：运行时目录与辅助文件 ----
	for _, dir := range []string{
		filepath.Join(root, "codex-shards"),
		filepath.Join(root, "usage-shards"),
		filepath.Join(root, "audit-blobs"),
		filepath.Join(root, "audit-hot"),
		filepath.Join(root, "usage-spool"),
		filepath.Join(root, "logs"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("创建运行时目录 %s 失败: %v", dir, err)
		}
	}
	runtimeLogPath := filepath.Join(root, "runtime-log.sqlite")
	if err := os.WriteFile(runtimeLogPath, nil, 0o644); err != nil {
		t.Fatalf("预建 runtime-log 空文件失败: %v", err)
	}

	// ---- 监听端口（health / 主入口 / J3b 管理） ----
	healthPort := w1bFreePort(t)
	mainPort := w1bFreePort(t)
	j3bPort := w1bFreePort(t)
	healthAddress := fmt.Sprintf("127.0.0.1:%d", healthPort)
	mainAddress := fmt.Sprintf("127.0.0.1:%d", mainPort)
	j3bAddress := fmt.Sprintf("127.0.0.1:%d", j3bPort)

	coverageDir := w1bCoverageDir(t, "W1V2-owner-full-boot")
	env := w1bScenarioEnv(t, coverageDir,
		// runtime 组合根（loadRuntimeConfig）
		"JUHE_AI_RUNTIME_MODE=performance",
		"JUHE_AI_DATABASE_DRIVER=sqlite",
		"JUHE_AI_CACHE_DRIVER=redis",
		"JUHE_AI_RUNTIME_STATE_DRIVER=redis",
		"JUHE_AI_REDIS_CACHE_URL=redis://"+cacheRedis.Addr(),
		"JUHE_AI_REDIS_STATE_URL=redis://"+stateRedis.Addr(),
		"JUHE_AI_REDIS_NAMESPACE=juhe-ai:"+w1v2J3bRedisNS,
		"JUHE_AI_SECRET="+w1v2RuntimeSecret,
		"JUHE_AI_HOST=127.0.0.1",
		fmt.Sprintf("JUHE_AI_PORT=%d", mainPort),
		"JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS="+healthAddress,
		"JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true",
		"JUHE_AI_AUTH_CAPTCHA_DISABLED=true",
		// 六库角色路径（两两不同物理文件；业务库复用 business.sqlite）
		"JUHE_AI_DATABASE_PATH="+businessPath,
		"JUHE_AI_CHAT_DATABASE_PATH="+filepath.Join(root, "chat.sqlite"),
		"JUHE_AI_DATASET_DATABASE_PATH="+filepath.Join(root, "dataset.sqlite"),
		"JUHE_AI_USAGE_CATALOG_DATABASE_PATH="+filepath.Join(root, "usage-catalog.sqlite"),
		"JUHE_AI_STATS_DATABASE_PATH="+filepath.Join(root, "stats.sqlite"),
		"JUHE_AI_TABLE_MONITOR_DATABASE_PATH="+filepath.Join(root, "table-monitor.sqlite"),
		"JUHE_AI_RUNTIME_LOG_DATABASE_PATH="+runtimeLogPath,
		"JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT="+filepath.Join(root, "codex-shards"),
		// business owner handoff（businessOwnerGate + 证据）
		"JUHE_AI_BUSINESS_OWNER=gateway",
		"JUHE_AI_BUSINESS_DATABASE_PATH="+businessPath,
		"JUHE_AI_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_BUSINESS_NODE_WRITER_STOPPED=true",
		"JUHE_AI_BUSINESS_SCHEMA_READY=true",
		"JUHE_AI_BUSINESS_OWNER_EPOCH="+w1v2OwnerEpoch,
		"JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH="+businessEvidence,
		// J3b owner（modelcheckowner.LoadConfig + OpenBusinessTargetConnection）
		"JUHE_AI_J3B_ENABLED=true",
		"JUHE_AI_J3B_OWNER=gateway",
		"JUHE_AI_J3B_INSTANCE_ID=w1v2-owner-boot",
		"JUHE_AI_J3B_STORE=sqlite",
		"JUHE_AI_J3B_DATABASE_PATH="+j3bPath,
		"JUHE_AI_J3B_BUSINESS_DATABASE_PATH="+businessPath,
		"JUHE_AI_J3B_CREDENTIAL_SECRET=w1v2-credential-secret-value",
		"JUHE_AI_J3B_IDENTITY_SECRET=w1v2-identity-secret-value",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED=true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED=true",
		"JUHE_AI_J3B_OWNER_EPOCH="+w1v2OwnerEpoch,
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH="+j3bEvidence,
		"JUHE_AI_J3B_SCHEMA_READY=true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY=true",
		"JUHE_AI_J3B_RUNTIME_READY=true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL=redis://"+stateRedis.Addr(),
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE="+w1v2J3bRedisNS,
		"JUHE_AI_J3B_MANAGEMENT_LISTEN_ADDRESS="+j3bAddress,
		// F3 审计（auditlog.LoadConfig sqlite 模式 + 专库隔离校验）
		"JUHE_AI_AUDIT_LOG_STORE=sqlite",
		"JUHE_AI_AUDIT_LOG_DATABASE_PATH="+filepath.Join(root, "audit.sqlite"),
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY="+filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY="+filepath.Join(root, "audit-hot"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_PATH="+businessPath,
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID=w1v2-owner-boot",
		// F4 操作日志（operationlog.LoadConfig sqlite 模式）
		"JUHE_AI_OPERATION_LOG_STORE=sqlite",
		"JUHE_AI_OPERATION_LOG_DATABASE_PATH="+filepath.Join(root, "operation.sqlite"),
		"JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH="+businessPath,
		"JUHE_AI_OPERATION_LOG_INSTANCE_ID=w1v2-owner-boot",
		"JUHE_AI_USAGE_SHARD_ROOT="+filepath.Join(root, "usage-shards"),
		// chain 协作面（spool / 日志 grep 目录）
		"JUHE_AI_USAGE_SPOOL_DIRECTORY="+filepath.Join(root, "usage-spool"),
		"JUHE_AI_LOG_DIR="+filepath.Join(root, "logs"),
	)

	// ---- 运行插桩二进制（owner 分支完整启动） ----
	ctx, cancel := context.WithTimeout(context.Background(), w1v2BootTotalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Env = env
	w1bSetNewProcessGroup(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if startErr := cmd.Start(); startErr != nil {
		cancel()
		t.Fatalf("场景 W1V2 启动 owner 网关失败: %v", startErr)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// ---- 就绪轮询：/health 200 且 ready=true（30s 有界，超时即杀并 Fatal） ----
	client := &http.Client{Timeout: 2 * time.Second}
	healthReady := false
	pollDeadline := time.Now().Add(w1v2BootPollTimeout)
	for time.Now().Before(pollDeadline) {
		response, err := client.Get("http://" + healthAddress + "/health")
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err == nil && readErr == nil && response.StatusCode == http.StatusOK {
				var payload map[string]any
				if json.Unmarshal(body, &payload) == nil && payload["ready"] == true {
					healthReady = true
					break
				}
			}
		}
		select {
		case waitErr := <-done:
			cancel()
			t.Fatalf("场景 W1V2 owner 网关在健康就绪前退出: %v\nstdout=%s\nstderr=%s", waitErr, stdout.String(), stderr.String())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !healthReady {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("场景 W1V2 健康端点 %s 在 %s 内未就绪（ready=true）\nstdout=%s\nstderr=%s", healthAddress, w1v2BootPollTimeout, stdout.String(), stderr.String())
	}

	// ---- 就绪探针断言：owner 组件族全部 running + 管理面 200 ----
	response, err := client.Get("http://" + healthAddress + "/health")
	if err != nil {
		t.Fatalf("场景 W1V2 就绪后 /health 复读失败: %v", err)
	}
	healthBody, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("场景 W1V2 就绪后 /health 期望 200，实际 %d（body=%s err=%v）", response.StatusCode, string(healthBody), readErr)
	}
	var healthPayload map[string]any
	if err := json.Unmarshal(healthBody, &healthPayload); err != nil {
		t.Fatalf("场景 W1V2 /health body 不是合法 JSON: %v, body=%s", err, string(healthBody))
	}
	for _, field := range []string{"ready", "ownerReady", "auditLogReady", "operationLogReady", "j3bReady", "sessionRetentionReady", "accountCircuitRuntimeReady"} {
		if healthPayload[field] != true {
			t.Fatalf("场景 W1V2 /health 字段 %s 期望 true，实际: %v, body=%s", field, healthPayload[field], string(healthBody))
		}
	}
	managementResponse, managementErr := client.Get("http://" + mainAddress + "/__aisys__/api/health")
	if managementErr != nil {
		t.Fatalf("场景 W1V2 管理面 /__aisys__/api/health 探活失败: %v", managementErr)
	}
	_, _ = io.Copy(io.Discard, managementResponse.Body)
	_ = managementResponse.Body.Close()
	if managementResponse.StatusCode != http.StatusOK {
		t.Fatalf("场景 W1V2 管理面 /__aisys__/api/health 期望 200，实际 %d", managementResponse.StatusCode)
	}

	// ---- 优雅关闭：CTRL_BREAK_EVENT（同 J 场景进程组路径） ----
	if err := w1bSendCtrlBreak(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("场景 W1V2 GenerateConsoleCtrlEvent(CTRL_BREAK) 失败: %v", err)
	}
	select {
	case waitErr := <-done:
		// 超时判定必须在 cancel 之前取值（同 J 场景注释）。
		ctxErr := ctx.Err()
		cancel()
		if ctxErr != nil {
			t.Fatalf("场景 W1V2 超过 %s 有界等待，进程已被终止", w1v2BootTotalTimeout)
		}
		exitCode := 0
		if waitErr != nil {
			exitErr, ok := waitErr.(*exec.ExitError)
			if !ok {
				t.Fatalf("场景 W1V2 等待进程退出失败: %v", waitErr)
			}
			exitCode = exitErr.ExitCode()
		}
		if exitCode != 0 {
			t.Fatalf("场景 W1V2 优雅关闭期望 exit 0，实际 %d\nstdout=%s\nstderr=%s", exitCode, stdout.String(), stderr.String())
		}
	case <-time.After(w1v2BootStopTimeout):
		_ = cmd.Process.Kill()
		<-done
		cancel()
		t.Fatalf("场景 W1V2 CTRL_BREAK 后 %s 内未退出，已强杀\nstdout=%s\nstderr=%s", w1v2BootStopTimeout, stdout.String(), stderr.String())
	}

	// ---- 输出契约：启动日志 + 无 fail 文案 ----
	combined := stdout.String() + stderr.String()
	for _, marker := range w1v2OwnerFailMarkers {
		if bytes.Contains([]byte(combined), []byte(marker)) {
			t.Fatalf("场景 W1V2 owner 启动路径出现 fail 输出 %q\nstdout=%s\nstderr=%s", marker, stdout.String(), stderr.String())
		}
	}
	for _, expected := range []string{"gateway system api composed", "juhe-ai-gateway started"} {
		if !bytes.Contains([]byte(stdout.String()), []byte(expected)) {
			t.Fatalf("场景 W1V2 stdout 缺少启动日志 %q\nstdout=%s", expected, stdout.String())
		}
	}

	w1bAppendCoverageManifest(t, coverageDir)
	t.Logf("场景 W1V2 owner 完整启动→就绪→优雅关闭通过: health=%s main=%s j3b=%s", healthAddress, mainAddress, j3bAddress)
}
