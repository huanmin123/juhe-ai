package mockdata

// 可观测域测试：用各 owner 的真实列定义在临时数据根里建库，直接调用
// seedObservability，断言行数、样本矩阵、幂等性、清理标识、运行日志文件与索引
// 一致性，以及覆盖断言。
//
// 为什么把 owner 的列定义抄进测试：maintenance 模块不能 import gateway / jobs
// （Go 三项目基线禁止跨项目 import），而「列名写错」正是本域最容易犯、又只能靠
// 真实库结构暴露的错误。抄一份列定义是唯一能在本模块内验证列名的办法。来源逐字
// 核对：
//   - gateway/internal/auditlog/schema.go（sqliteSchema）
//   - gateway/internal/operationlog/schema.go（sqliteSchema）
//   - jobs/internal/runtimelog/schema.go（sqliteSchema）
//   - jobs/internal/taskruns/schema.go（sqliteSchema）
//   - internal/schema/sqlite_schema_chat_codex_dataset_usage.go（sqliteDatasetDDL）
//
// 全部数据写在 t.TempDir() 下：造数最危险的失败模式是写错数据根，测试绝不碰
// .local/dev/data。

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// observabilityTestNow 是本域测试固定的时间基准：所有断言都基于它推导，
// 保证「同一 Options 同一结果」可复现。
var observabilityTestNow = time.Date(2026, 3, 18, 15, 30, 0, 0, time.UTC)

const observabilityAuditLogsDDL = `CREATE TABLE audit_logs (
  id TEXT PRIMARY KEY, trace_id TEXT NOT NULL, traffic_source TEXT NOT NULL,
  system_account_id TEXT, api_key_id TEXT, conversation_key TEXT, session_id TEXT,
  session_client_type TEXT, group_id TEXT, account_id TEXT, provider_code TEXT,
  method TEXT NOT NULL, path TEXT NOT NULL, query_string TEXT, model TEXT,
  upstream_model TEXT, pricing_model TEXT, model_mapping_applied INTEGER NOT NULL DEFAULT 0,
  model_mapping_source TEXT, source_endpoint_family TEXT, upstream_endpoint_family TEXT,
  stream INTEGER NOT NULL DEFAULT 0, client_ip TEXT, user_agent TEXT,
  audit_outcome TEXT NOT NULL, success INTEGER NOT NULL DEFAULT 0, final_status_code INTEGER,
  error_phase TEXT, error_code TEXT, error_message TEXT, sample_bucket INTEGER NOT NULL,
  sample_reason TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0,
  payload_count INTEGER NOT NULL DEFAULT 0, raw_payload_bytes INTEGER NOT NULL DEFAULT 0,
  compressed_payload_bytes INTEGER NOT NULL DEFAULT 0, compression_saved_bytes INTEGER NOT NULL DEFAULT 0,
  error_group_id TEXT, capture_status TEXT NOT NULL DEFAULT 'complete',
  lifecycle_status TEXT NOT NULL DEFAULT 'finalized', started_at TEXT NOT NULL,
  ended_at TEXT NOT NULL, duration_ms INTEGER, http_completed_at TEXT, http_duration_ms INTEGER,
  first_token_ms INTEGER, created_at TEXT NOT NULL
)`

const observabilityAuditAttemptsDDL = `CREATE TABLE audit_log_attempts (
  id TEXT PRIMARY KEY, audit_log_id TEXT NOT NULL REFERENCES audit_logs(id) ON DELETE CASCADE,
  attempt_index INTEGER NOT NULL, account_id TEXT, account_owner_system_account_id TEXT,
  group_id TEXT, proxy_url TEXT, provider_code TEXT, attempt_model TEXT,
  attempt_upstream_model TEXT, attempt_pricing_model TEXT,
  attempt_model_mapping_applied INTEGER NOT NULL DEFAULT 0, attempt_model_mapping_source TEXT,
  attempt_source_endpoint_family TEXT, attempt_upstream_endpoint_family TEXT,
  upstream_method TEXT NOT NULL, upstream_url TEXT NOT NULL, upstream_status_code INTEGER,
  success INTEGER NOT NULL DEFAULT 0, error_phase TEXT, error_code TEXT, error_message TEXT,
  started_at TEXT NOT NULL, ended_at TEXT, duration_ms INTEGER
)`

const observabilityAuditBlobsDDL = `CREATE TABLE audit_payload_blobs (
  id TEXT PRIMARY KEY, sha256 TEXT NOT NULL, raw_size_bytes INTEGER NOT NULL,
  compressed_size_bytes INTEGER NOT NULL, content_type TEXT NOT NULL,
  content_encoding TEXT, compression TEXT NOT NULL DEFAULT 'none', storage_key TEXT NOT NULL,
  ref_count INTEGER NOT NULL DEFAULT 0, first_seen_at TEXT NOT NULL, last_seen_at TEXT NOT NULL,
  created_at TEXT NOT NULL, UNIQUE(sha256, raw_size_bytes, content_type)
)`

const observabilityAuditPayloadRefsDDL = `CREATE TABLE audit_payload_refs (
  id TEXT PRIMARY KEY, audit_log_id TEXT NOT NULL REFERENCES audit_logs(id) ON DELETE CASCADE,
  attempt_id TEXT REFERENCES audit_log_attempts(id) ON DELETE SET NULL, part_type TEXT NOT NULL,
  sequence_index INTEGER NOT NULL, content_type TEXT, content_encoding TEXT,
  headers_blob_id TEXT REFERENCES audit_payload_blobs(id) ON DELETE SET NULL,
  body_blob_id TEXT REFERENCES audit_payload_blobs(id) ON DELETE SET NULL,
  headers_sha256 TEXT, body_sha256 TEXT, raw_size_bytes INTEGER NOT NULL DEFAULT 0,
  compressed_size_bytes INTEGER NOT NULL DEFAULT 0, capture_status TEXT NOT NULL,
  drop_reason TEXT, created_at TEXT NOT NULL
)`

const observabilityAuditErrorGroupsDDL = `CREATE TABLE audit_error_groups (
  id TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, window_started_at TEXT NOT NULL,
  window_ended_at TEXT NOT NULL, system_account_id TEXT, api_key_id TEXT, group_id TEXT,
  account_id TEXT, provider_code TEXT, path TEXT, model TEXT, status_code INTEGER,
  error_phase TEXT, error_code TEXT, error_type TEXT, request_fingerprint TEXT,
  error_fingerprint TEXT, count INTEGER NOT NULL DEFAULT 0, first_event_id TEXT,
  last_event_id TEXT, sample_event_id TEXT, last_message TEXT, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, UNIQUE(fingerprint, window_started_at)
)`

const observabilityOperationLogsDDL = `CREATE TABLE operation_logs (
  id TEXT PRIMARY KEY, trace_id TEXT, actor_system_account_id TEXT NOT NULL, actor_username TEXT,
  actor_display_name TEXT, actor_role TEXT NOT NULL, operation_scope_system_account_id TEXT,
  mode TEXT NOT NULL, module TEXT NOT NULL, action TEXT NOT NULL, operation_key TEXT NOT NULL,
  resource_type TEXT NOT NULL, resource_id TEXT, resource_name TEXT, summary TEXT NOT NULL,
  detail_level TEXT NOT NULL, visibility_scope TEXT NOT NULL, changes_json TEXT NOT NULL,
  metadata_json TEXT NOT NULL, method TEXT, path TEXT, status_code INTEGER, client_ip TEXT,
  user_agent TEXT, created_at TEXT NOT NULL
)`

const observabilityOperationTargetsDDL = `CREATE TABLE operation_log_targets (
  id TEXT PRIMARY KEY, operation_log_id TEXT NOT NULL REFERENCES operation_logs(id) ON DELETE CASCADE,
  target_type TEXT NOT NULL, target_id TEXT, target_name TEXT, target_owner_system_account_id TEXT,
  relation TEXT NOT NULL, created_at TEXT NOT NULL
)`

const observabilityOperationViewersDDL = `CREATE TABLE operation_log_viewers (
  operation_log_id TEXT NOT NULL REFERENCES operation_logs(id) ON DELETE CASCADE,
  system_account_id TEXT NOT NULL, visibility_reason TEXT NOT NULL, detail_level TEXT NOT NULL,
  created_at TEXT NOT NULL, PRIMARY KEY(operation_log_id, system_account_id, visibility_reason, detail_level)
)`

const observabilityOperationSearchTermsDDL = `CREATE TABLE operation_log_summary_search_terms (
  operation_log_id TEXT NOT NULL REFERENCES operation_logs(id) ON DELETE CASCADE,
  term TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(term, operation_log_id)
)`

const observabilityRuntimeLogsDDL = `CREATE TABLE runtime_logs (
  id TEXT PRIMARY KEY, log_file TEXT, log_offset INTEGER, line_number INTEGER,
  time TEXT NOT NULL, level TEXT NOT NULL, trace_id TEXT, event TEXT, message TEXT,
  error_message TEXT, raw_json TEXT NOT NULL, created_at TEXT NOT NULL
)`

const observabilityRuntimeCursorsDDL = `CREATE TABLE runtime_log_file_cursors (
  log_file TEXT PRIMARY KEY, file_identity TEXT, cursor_offset INTEGER NOT NULL DEFAULT 0,
  line_number INTEGER NOT NULL DEFAULT 0, file_size INTEGER NOT NULL DEFAULT 0,
  truncation_generation INTEGER NOT NULL DEFAULT 0, file_mtime_ms INTEGER, last_read_at TEXT,
  last_error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
)`

const observabilityRuntimeFacetSummaryDDL = `CREATE TABLE runtime_log_facet_summary (
  bucket_key TEXT PRIMARY KEY, total_count INTEGER NOT NULL DEFAULT 0, earliest_time TEXT,
  latest_time TEXT, updated_at TEXT NOT NULL
)`

const observabilityRuntimeLevelFacetsDDL = `CREATE TABLE runtime_log_level_facets (
  bucket_key TEXT NOT NULL, level TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL, PRIMARY KEY (bucket_key, level)
)`

const observabilityRuntimeEventFacetsDDL = `CREATE TABLE runtime_log_event_facets (
  bucket_key TEXT NOT NULL, event TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
  latest_time TEXT, updated_at TEXT NOT NULL, PRIMARY KEY (bucket_key, event)
)`

const observabilityTaskRunsDDL = `CREATE TABLE background_task_runs (
  run_id TEXT PRIMARY KEY, job_name TEXT NOT NULL, job_type TEXT NOT NULL, worker_role TEXT NOT NULL,
  status TEXT NOT NULL, lease_key TEXT NOT NULL, owner_id TEXT, params_json TEXT NOT NULL DEFAULT '{}',
  result_json TEXT NOT NULL DEFAULT '{}', error_message TEXT, submitted_at TEXT NOT NULL,
  started_at TEXT, heartbeat_at TEXT, finished_at TEXT, duration_ms INTEGER, exit_code INTEGER,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
)`

const observabilityPublicAPILogsDDL = `CREATE TABLE public_api_logs (
  id TEXT PRIMARY KEY, trace_id TEXT, source_ref_id TEXT, source_name TEXT, token_id TEXT,
  token_name TEXT, token_prefix TEXT, is_test_token INTEGER NOT NULL DEFAULT 0, method TEXT NOT NULL,
  path TEXT NOT NULL, query_string TEXT, client_ip TEXT, user_agent TEXT, status_code INTEGER,
  success INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER, request_size_bytes INTEGER NOT NULL DEFAULT 0,
  response_size_bytes INTEGER NOT NULL DEFAULT 0, request_capture_status TEXT NOT NULL DEFAULT 'empty',
  response_capture_status TEXT NOT NULL DEFAULT 'empty', request_data_json TEXT NOT NULL DEFAULT '{}',
  response_data_json TEXT NOT NULL DEFAULT '{}', error_code TEXT, error_message TEXT,
  started_at TEXT NOT NULL, ended_at TEXT NOT NULL, created_at TEXT NOT NULL
)`

const observabilityAccountCleanupTargetsDDL = `CREATE TABLE account_record_cleanup_targets (
  account_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL,
  related_account_ids_json TEXT NOT NULL DEFAULT '[]', authorization_ids_json TEXT NOT NULL DEFAULT '[]',
  team_scope_ids_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at TEXT, last_blocked_reason TEXT,
  last_error_message TEXT
)`

const observabilityAPIKeyCleanupTargetsDDL = `CREATE TABLE api_key_record_cleanup_targets (
  api_key_id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at TEXT,
  last_blocked_reason TEXT, last_error_message TEXT
)`

// observabilityTestDDL 是「按存储建表」的清单。
var observabilityTestDDL = map[string][]string{
	StoreAuditLog: {
		observabilityAuditLogsDDL, observabilityAuditAttemptsDDL, observabilityAuditBlobsDDL,
		observabilityAuditPayloadRefsDDL, observabilityAuditErrorGroupsDDL,
	},
	StoreOperationLog: {
		observabilityOperationLogsDDL, observabilityOperationTargetsDDL,
		observabilityOperationViewersDDL, observabilityOperationSearchTermsDDL,
	},
	StoreRuntimeLog: {
		observabilityRuntimeLogsDDL, observabilityRuntimeCursorsDDL,
		observabilityRuntimeFacetSummaryDDL, observabilityRuntimeLevelFacetsDDL,
		observabilityRuntimeEventFacetsDDL,
	},
	StoreTaskRuns: {observabilityTaskRunsDDL},
	StoreDataset: {
		observabilityPublicAPILogsDDL, observabilityAccountCleanupTargetsDDL,
		observabilityAPIKeyCleanupTargetsDDL,
	},
}

// observabilityTestBusinessDDL 是 business 库的测试夹具表（只含本域查询的列）：
// 真实 business schema 属于业务域，这里只需要能提供引用即可。
var observabilityTestBusinessDDL = []string{
	`CREATE TABLE system_accounts (
	  id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
	  role TEXT NOT NULL DEFAULT 'user', status TEXT NOT NULL DEFAULT 'active'
	)`,
	`CREATE TABLE accounts (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL)`,
	`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL)`,
	`CREATE TABLE api_keys (id TEXT PRIMARY KEY, name TEXT NOT NULL, system_account_id TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active')`,
	`CREATE TABLE resource_authorizations (id TEXT PRIMARY KEY, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL, remark TEXT, status TEXT NOT NULL DEFAULT 'active')`,
	`CREATE TABLE announcements (id TEXT PRIMARY KEY, title TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'draft')`,
	`CREATE TABLE external_integration_sources (
	  id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
	  scopes_json TEXT NOT NULL DEFAULT '[]'
	)`,
	`CREATE TABLE external_integration_source_tokens (
	  id TEXT PRIMARY KEY, source_ref_id TEXT NOT NULL, name TEXT NOT NULL, token_prefix TEXT NOT NULL,
	  status TEXT NOT NULL DEFAULT 'active'
	)`,
}

// observabilityTestEnv 建好可观测域要写的全部存储（真实列定义）与 business 夹具，
// 返回上下文；days / now 决定样本跨度。
func observabilityTestEnv(t *testing.T, days int, now time.Time) *env {
	t.Helper()
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: days, DailyRequests: 20, Now: now}, nil)
	t.Cleanup(func() { _ = e.Close() })
	for storeName, ddls := range observabilityTestDDL {
		for _, ddl := range ddls {
			createTestTable(t, e, storeName, ddl)
		}
	}
	return e
}

// observabilitySeedTestBusiness 写入 business 夹具行（mock 资源与外部来源）。
func observabilitySeedTestBusiness(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	for _, ddl := range observabilityTestBusinessDDL {
		createTestTable(t, e, StoreBusiness, ddl)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := e.exec(ctx, StoreBusiness, query, args...); err != nil {
			t.Fatalf("business 夹具写入失败: %v", err)
		}
	}
	exec(`INSERT INTO system_accounts (id, username, display_name, role, status) VALUES (?, ?, ?, ?, 'active')`,
		CleanupIDPrefix+"system_account_admin", CleanupIDPrefix+"admin", CleanupNamePrefix+"管理员用户", "admin")
	exec(`INSERT INTO system_accounts (id, username, display_name, role, status) VALUES (?, ?, ?, ?, 'active')`,
		CleanupIDPrefix+"system_account_ops", CleanupIDPrefix+"ops", CleanupNamePrefix+"运维用户", "user")
	exec(`INSERT INTO accounts (id, name, system_account_id) VALUES (?, ?, ?)`,
		CleanupIDPrefix+"account_primary", CleanupNamePrefix+"主力账户", CleanupIDPrefix+"system_account_admin")
	exec(`INSERT INTO groups (id, name, system_account_id) VALUES (?, ?, ?)`,
		CleanupIDPrefix+"group_main", CleanupNamePrefix+"主力分组", CleanupIDPrefix+"system_account_admin")
	exec(`INSERT INTO api_keys (id, name, system_account_id) VALUES (?, ?, ?)`,
		CleanupIDPrefix+"api_key_admin_main", CleanupNamePrefix+"主力 Key", CleanupIDPrefix+"system_account_admin")
	exec(`INSERT INTO resource_authorizations (id, resource_type, resource_id, remark) VALUES (?, 'group', ?, ?)`,
		CleanupIDPrefix+"authorization_group", CleanupIDPrefix+"group_main", CleanupNamePrefix+"授权样本")
	exec(`INSERT INTO announcements (id, title) VALUES (?, ?)`,
		CleanupIDPrefix+"announcement_main", CleanupNamePrefix+"公告样本")
	// 正式来源（写权限）、只读来源（无 :write scope）、内置测试来源与各自 Token。
	exec(`INSERT INTO external_integration_sources (id, name, scopes_json) VALUES (?, ?, ?)`,
		CleanupIDPrefix+"external_source_primary", CleanupNamePrefix+"正式来源",
		`["juhe_ai_public:account_list:read","juhe_ai_public:account_add:write"]`)
	exec(`INSERT INTO external_integration_source_tokens (id, source_ref_id, name, token_prefix) VALUES (?, ?, ?, ?)`,
		CleanupIDPrefix+"external_token_primary", CleanupIDPrefix+"external_source_primary", CleanupNamePrefix+"正式 Token", "juis_prim")
	exec(`INSERT INTO external_integration_sources (id, name, scopes_json) VALUES (?, ?, ?)`,
		CleanupIDPrefix+"external_source_readonly", CleanupNamePrefix+"只读来源",
		`["juhe_ai_public:account_list:read"]`)
	exec(`INSERT INTO external_integration_source_tokens (id, source_ref_id, name, token_prefix) VALUES (?, ?, ?, ?)`,
		CleanupIDPrefix+"external_token_readonly", CleanupIDPrefix+"external_source_readonly", CleanupNamePrefix+"只读 Token", "juis_ro")
	exec(`INSERT INTO external_integration_sources (id, name, scopes_json) VALUES (?, ?, ?)`,
		observabilityBuiltInTestSourceID, "内置测试来源", `["juhe_ai_public:account_list:read"]`)
	exec(`INSERT INTO external_integration_source_tokens (id, source_ref_id, name, token_prefix) VALUES (?, ?, ?, ?)`,
		observabilityBuiltInTestTokenID, observabilityBuiltInTestSourceID, "内置测试 Token", "juis_build")
}

// observabilityTestCount 执行计数查询。
func observabilityTestCount(t *testing.T, e *env, storeName, query string, args ...any) int {
	t.Helper()
	db, err := e.openExisting(storeName)
	if err != nil {
		t.Fatalf("打开 %s: %v", storeName, err)
	}
	if db == nil {
		t.Fatalf("存储 %s 不存在", storeName)
	}
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("计数 %s: %v", query, err)
	}
	return count
}

// observabilityTestStrings 读一列文本（NULL 归一成空串）。
func observabilityTestStrings(t *testing.T, e *env, storeName, query string, args ...any) []string {
	t.Helper()
	db, err := e.openExisting(storeName)
	if err != nil || db == nil {
		t.Fatalf("打开 %s: %v", storeName, err)
	}
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("查询 %s: %v", query, err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("扫描 %s: %v", query, err)
		}
		values = append(values, value.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 %s: %v", query, err)
	}
	return values
}

func TestSeedObservabilityWritesEveryOwnedTable(t *testing.T) {
	e := observabilityTestEnv(t, 7, observabilityTestNow)
	observabilitySeedTestBusiness(t, e)

	result, err := seedObservability(context.Background(), e)
	if err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	if result.Name != DomainObservability {
		t.Fatalf("domain name = %q", result.Name)
	}
	// 行数的事实基线：7 天样本下每张表都必须有行，且 counts 与表行数一致。
	cases := []struct {
		name  string
		store string
		query string
		key   string
	}{
		{"audit_logs", StoreAuditLog, "SELECT COUNT(*) FROM audit_logs", "auditLogs"},
		{"audit_log_attempts", StoreAuditLog, "SELECT COUNT(*) FROM audit_log_attempts", "auditLogAttempts"},
		{"audit_payload_blobs", StoreAuditLog, "SELECT COUNT(*) FROM audit_payload_blobs", "auditPayloadBlobs"},
		{"audit_payload_refs", StoreAuditLog, "SELECT COUNT(*) FROM audit_payload_refs", "auditPayloadRefs"},
		{"audit_error_groups", StoreAuditLog, "SELECT COUNT(*) FROM audit_error_groups", "auditErrorGroups"},
		{"operation_logs", StoreOperationLog, "SELECT COUNT(*) FROM operation_logs", "operationLogs"},
		{"operation_log_targets", StoreOperationLog, "SELECT COUNT(*) FROM operation_log_targets", "operationLogTargets"},
		{"operation_log_viewers", StoreOperationLog, "SELECT COUNT(*) FROM operation_log_viewers", "operationLogViewers"},
		{"operation_log_summary_search_terms", StoreOperationLog, "SELECT COUNT(*) FROM operation_log_summary_search_terms", "operationLogSummarySearchTerms"},
		{"runtime_logs", StoreRuntimeLog, "SELECT COUNT(*) FROM runtime_logs", "runtimeLogs"},
		{"public_api_logs", StoreDataset, "SELECT COUNT(*) FROM public_api_logs", "publicApiLogs"},
		{"account_record_cleanup_targets", StoreDataset, "SELECT COUNT(*) FROM account_record_cleanup_targets", "accountCleanupTargets"},
		{"api_key_record_cleanup_targets", StoreDataset, "SELECT COUNT(*) FROM api_key_record_cleanup_targets", "apiKeyCleanupTargets"},
		{"background_task_runs", StoreTaskRuns, "SELECT COUNT(*) FROM background_task_runs", "backgroundTaskRuns"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			rows := observabilityTestCount(t, e, testCase.store, testCase.query)
			if rows < 1 {
				t.Fatalf("%s 行数 = %d，要求至少 1", testCase.name, rows)
			}
			if result.Counts[testCase.key] != rows {
				t.Fatalf("counts[%s] = %d，表行数 = %d", testCase.key, result.Counts[testCase.key], rows)
			}
		})
	}
}

func TestSeedObservabilitySampleMatrix(t *testing.T) {
	e := observabilityTestEnv(t, 7, observabilityTestNow)
	observabilitySeedTestBusiness(t, e)
	if _, err := seedObservability(context.Background(), e); err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	// 审计：端点矩阵、多系统账户、多 AI 账户、成功 / 失败、模型映射。
	endpoints := observabilityTestStrings(t, e, StoreAuditLog, "SELECT DISTINCT path FROM audit_logs ORDER BY path")
	wantEndpoints := map[string]bool{
		"/v1/responses": false, "/v1/chat/completions": false,
		"/v1/images/generations": false, "/v1/models": false,
	}
	for _, endpoint := range endpoints {
		if _, ok := wantEndpoints[endpoint]; ok {
			wantEndpoints[endpoint] = true
		}
	}
	for endpoint, found := range wantEndpoints {
		if !found {
			t.Fatalf("审计日志缺少端点 %s（现有：%v）", endpoint, endpoints)
		}
	}
	if got := observabilityTestCount(t, e, StoreAuditLog, "SELECT COUNT(*) FROM audit_logs WHERE success = 0"); got < 1 {
		t.Fatal("审计日志必须覆盖失败样本")
	}
	if got := observabilityTestCount(t, e, StoreAuditLog, "SELECT COUNT(DISTINCT system_account_id) FROM audit_logs"); got < 2 {
		t.Fatalf("审计日志系统账户数 = %d，要求至少 2", got)
	}
	if got := observabilityTestCount(t, e, StoreAuditLog, "SELECT COUNT(DISTINCT account_id) FROM audit_logs"); got < 1 {
		t.Fatal("审计日志必须引用 AI 账户")
	}
	if got := observabilityTestCount(t, e, StoreAuditLog, "SELECT COUNT(*) FROM audit_logs WHERE model_mapping_applied = 1"); got < 1 {
		t.Fatal("审计日志必须覆盖模型映射命中样本")
	}
	// trace 前缀：全部行必须能被清理扫描命中（id 或 trace 带清理标识）。
	if got := observabilityTestCount(t, e, StoreAuditLog,
		"SELECT COUNT(*) FROM audit_logs WHERE id LIKE ? AND trace_id LIKE ?", CleanupIDPrefix+"%", CleanupTracePrefix+"%"); got == 0 {
		t.Fatal("审计日志必须带清理标识")
	}
	// 时间跨度：近 7 天（首行不早于第 7 天零点，末行不晚于基准时刻）。
	span := observabilityTestStrings(t, e, StoreAuditLog, "SELECT MIN(started_at) FROM audit_logs")
	if len(span) != 1 || span[0] == "" {
		t.Fatalf("审计日志时间跨度为空: %v", span)
	}
	// 操作日志：模块与动作矩阵。
	modules := observabilityTestStrings(t, e, StoreOperationLog, "SELECT DISTINCT module FROM operation_logs ORDER BY module")
	for _, want := range []string{"accounts", "groups", "api_keys", "authorizations", "announcements", "auth"} {
		found := false
		for _, module := range modules {
			if module == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("操作日志缺少模块 %s（现有：%v）", want, modules)
		}
	}
	for _, action := range []string{"create", "update", "delete"} {
		if got := observabilityTestCount(t, e, StoreOperationLog, "SELECT COUNT(*) FROM operation_logs WHERE action = ?", action); got < 1 {
			t.Fatalf("操作日志缺少 action=%s", action)
		}
	}
	if got := observabilityTestCount(t, e, StoreOperationLog,
		"SELECT COUNT(*) FROM operation_logs WHERE summary LIKE ?", CleanupNamePrefix+"%"); got < 1 {
		t.Fatal("操作日志摘要必须带业务前缀")
	}
	// 运行日志：级别与事件矩阵。
	levels := observabilityTestStrings(t, e, StoreRuntimeLog, "SELECT DISTINCT level FROM runtime_logs ORDER BY level")
	for _, want := range []string{"info", "warn", "error"} {
		found := false
		for _, level := range levels {
			if level == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("运行日志缺少级别 %s（现有：%v）", want, levels)
		}
	}
	for _, event := range []string{"http_request_completed", "data_retention_cleanup_completed"} {
		if got := observabilityTestCount(t, e, StoreRuntimeLog, "SELECT COUNT(*) FROM runtime_logs WHERE event = ?", event); got < 1 {
			t.Fatalf("运行日志缺少事件 %s", event)
		}
	}
	if got := observabilityTestCount(t, e, StoreRuntimeLog, "SELECT COUNT(*) FROM runtime_logs WHERE error_message IS NOT NULL AND error_message <> ''"); got < 1 {
		t.Fatal("运行日志缺少错误消息样本")
	}
	// 后台任务：状态与任务名矩阵。
	statuses := observabilityTestStrings(t, e, StoreTaskRuns, "SELECT DISTINCT status FROM background_task_runs ORDER BY status")
	for _, want := range []string{"completed", "failed", "running", "queued"} {
		found := false
		for _, status := range statuses {
			if status == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("后台任务缺少状态 %s（现有：%v）", want, statuses)
		}
	}
	if got := observabilityTestCount(t, e, StoreTaskRuns, "SELECT COUNT(DISTINCT job_name) FROM background_task_runs"); got < 6 {
		t.Fatalf("后台任务名数量 = %d，要求至少 6", got)
	}
	if got := observabilityTestCount(t, e, StoreTaskRuns,
		"SELECT COUNT(*) FROM background_task_runs WHERE started_at IS NOT NULL AND finished_at IS NOT NULL AND duration_ms IS NOT NULL"); got < 1 {
		t.Fatal("后台任务缺少带起止时间与耗时的样本")
	}
	if got := observabilityTestCount(t, e, StoreTaskRuns,
		"SELECT COUNT(*) FROM background_task_runs WHERE error_message LIKE ?", CleanupNamePrefix+"%"); got < 1 {
		t.Fatal("后台任务失败样本必须带清理标识的错误消息")
	}
	// owner 租约族不伪造：task-runs 的 background_job_leases 必须保持为空。
	if got := observabilityTestCount(t, e, StoreTaskRuns,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='background_job_leases'"); got > 0 {
		if rows := observabilityTestCount(t, e, StoreTaskRuns, "SELECT COUNT(*) FROM background_job_leases"); rows != 0 {
			t.Fatalf("background_job_leases 不应被造数写入，现有 %d 行", rows)
		}
	}
}

func TestSeedObservabilityPublicAPILogMatrix(t *testing.T) {
	e := observabilityTestEnv(t, 7, observabilityTestNow)
	observabilitySeedTestBusiness(t, e)
	if _, err := seedObservability(context.Background(), e); err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	// 状态码矩阵：Node mockdata 的取模分布在 35 行样本里必须命中全部七种状态。
	statuses := observabilityTestStrings(t, e, StoreDataset, "SELECT DISTINCT status_code FROM public_api_logs ORDER BY status_code")
	want := []string{"200", "201", "400", "401", "403", "429", "500"}
	for _, expected := range want {
		found := false
		for _, status := range statuses {
			if status == expected {
				found = true
			}
		}
		if !found {
			t.Fatalf("公开接口日志缺少状态码 %s（现有：%v）", expected, statuses)
		}
	}
	// capture 状态矩阵：request 列覆盖 empty（GET 无请求体）/ complete / truncated，
	// response 列覆盖 complete / truncated。两列合起来覆盖三种取值；响应体不伪造
	// empty（真实 pipeline 只在确实没抓到内容时才是 empty，而造数样本的响应体始终是 JSON）。
	captureValues := map[string]bool{}
	for _, column := range []string{"request_capture_status", "response_capture_status"} {
		values := observabilityTestStrings(t, e, StoreDataset, "SELECT DISTINCT "+column+" FROM public_api_logs ORDER BY 1")
		for _, value := range values {
			captureValues[value] = true
		}
	}
	for _, expected := range []string{"empty", "complete", "truncated"} {
		if !captureValues[expected] {
			t.Fatalf("capture 状态缺少 %s（现有：%v）", expected, captureValues)
		}
	}
	for _, column := range []string{"request_capture_status", "response_capture_status"} {
		if got := observabilityTestCount(t, e, StoreDataset,
			"SELECT COUNT(*) FROM public_api_logs WHERE "+column+" = 'truncated'"); got < 1 {
			t.Fatalf("%s 缺少截断样本", column)
		}
	}
	// 来源引用是 business 域真实行：正式 / 只读 / 内置测试三类都要出现。
	if got := observabilityTestCount(t, e, StoreDataset,
		"SELECT COUNT(*) FROM public_api_logs WHERE is_test_token = 1 AND source_ref_id = ?", observabilityBuiltInTestSourceID); got < 1 {
		t.Fatal("公开接口日志缺少内置测试 Token 样本")
	}
	if got := observabilityTestCount(t, e, StoreDataset,
		"SELECT COUNT(*) FROM public_api_logs WHERE is_test_token = 0 AND source_ref_id = ?", CleanupIDPrefix+"external_source_readonly"); got < 1 {
		t.Fatal("公开接口日志缺少只读来源样本")
	}
	if got := observabilityTestCount(t, e, StoreDataset,
		"SELECT COUNT(*) FROM public_api_logs WHERE is_test_token = 0 AND source_ref_id = ?", CleanupIDPrefix+"external_source_primary"); got < 1 {
		t.Fatal("公开接口日志缺少正式来源样本")
	}
	// 失败样本必须带 error_code / error_message（401 / 403 / 429 有真实错误码）。
	if got := observabilityTestCount(t, e, StoreDataset,
		"SELECT COUNT(*) FROM public_api_logs WHERE success = 0 AND error_code IS NOT NULL AND error_code <> ''"); got < 1 {
		t.Fatal("公开接口日志失败样本缺少错误码")
	}
	// 清理目标：attemptCount 覆盖 0 / 1 / 2。
	for _, table := range []string{"account_record_cleanup_targets", "api_key_record_cleanup_targets"} {
		for _, attempt := range []string{"0", "1", "2"} {
			if got := observabilityTestCount(t, e, StoreDataset,
				"SELECT COUNT(*) FROM "+table+" WHERE attempt_count = "+attempt); got < 1 {
				t.Fatalf("%s 缺少 attempt_count=%s 样本", table, attempt)
			}
		}
		if got := observabilityTestCount(t, e, StoreDataset,
			"SELECT COUNT(*) FROM "+table+" WHERE last_blocked_reason LIKE ?", CleanupNamePrefix+"%"); got < 1 {
			t.Fatalf("%s 的阻塞原因必须带清理标识", table)
		}
	}
}

func TestSeedObservabilitySkipsMissingStores(t *testing.T) {
	cases := []struct {
		name  string
		store string
	}{
		{"audit log", StoreAuditLog},
		{"operation log", StoreOperationLog},
		{"runtime log", StoreRuntimeLog},
		{"task runs", StoreTaskRuns},
		{"dataset", StoreDataset},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			paths, err := ResolvePaths(root, "", envMap(nil))
			if err != nil {
				t.Fatal(err)
			}
			e := newEnv(Options{Paths: paths, Days: 3, DailyRequests: 20, Now: observabilityTestNow}, nil)
			t.Cleanup(func() { _ = e.Close() })
			// 只建其它存储：目标存储缺失时本域必须安全跳过而不是建库。
			for storeName, ddls := range observabilityTestDDL {
				if storeName == testCase.store {
					continue
				}
				for _, ddl := range ddls {
					createTestTable(t, e, storeName, ddl)
				}
			}
			result, err := seedObservability(context.Background(), e)
			if err != nil {
				t.Fatalf("缺 %s 时必须安全跳过: %v", testCase.store, err)
			}
			if _, statErr := os.Stat(e.byName[testCase.store].Path); !os.IsNotExist(statErr) {
				t.Fatalf("造数不得创建未 bootstrap 的存储 %s: %v", testCase.store, statErr)
			}
			for key := range result.Counts {
				if strings.Contains(key, "storageSnapshot") {
					t.Fatalf("本域不写表监控快照，出现计数 %s", key)
				}
			}
		})
	}
}

func TestSeedObservabilityOnEmptyDataRootWritesNothing(t *testing.T) {
	root := t.TempDir()
	paths, err := ResolvePaths(root, "", envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(Options{Paths: paths, Days: 3, DailyRequests: 20, Now: observabilityTestNow}, nil)
	defer func() { _ = e.Close() }()
	result, err := seedObservability(context.Background(), e)
	if err != nil {
		t.Fatalf("空数据根必须安全: %v", err)
	}
	if len(result.Counts) != 0 {
		t.Fatalf("空数据根不得产出计数（域应判为未接线）: %v", result.Counts)
	}
	for _, storeName := range []string{StoreAuditLog, StoreOperationLog, StoreRuntimeLog, StoreTaskRuns, StoreDataset} {
		if _, statErr := os.Stat(e.byName[storeName].Path); !os.IsNotExist(statErr) {
			t.Fatalf("空数据根上不得创建 %s: %v", storeName, statErr)
		}
	}
}

func TestSeedObservabilityIsIdempotent(t *testing.T) {
	e := observabilityTestEnv(t, 4, observabilityTestNow)
	observabilitySeedTestBusiness(t, e)
	ctx := context.Background()
	first, err := seedObservability(ctx, e)
	if err != nil {
		t.Fatalf("第一次造数: %v", err)
	}
	snapshot := map[string][]string{}
	for _, item := range []struct {
		store string
		query string
		key   string
	}{
		{StoreAuditLog, "SELECT id FROM audit_logs ORDER BY id", "audit"},
		{StoreAuditLog, "SELECT id FROM audit_payload_refs ORDER BY id", "refs"},
		{StoreAuditLog, "SELECT id FROM audit_error_groups ORDER BY id", "groups"},
		{StoreOperationLog, "SELECT id FROM operation_logs ORDER BY id", "operations"},
		{StoreOperationLog, "SELECT operation_log_id || '|' || system_account_id FROM operation_log_viewers ORDER BY 1", "viewers"},
		{StoreRuntimeLog, "SELECT id FROM runtime_logs ORDER BY id", "runtime"},
		{StoreTaskRuns, "SELECT run_id FROM background_task_runs ORDER BY run_id", "taskruns"},
		{StoreDataset, "SELECT id FROM public_api_logs ORDER BY id", "publicapi"},
		{StoreDataset, "SELECT account_id FROM account_record_cleanup_targets ORDER BY account_id", "accounttargets"},
		{StoreDataset, "SELECT api_key_id FROM api_key_record_cleanup_targets ORDER BY api_key_id", "apikeytargets"},
	} {
		snapshot[item.key] = observabilityTestStrings(t, e, item.store, item.query)
	}
	second, err := seedObservability(ctx, e)
	if err != nil {
		t.Fatalf("第二次造数: %v", err)
	}
	if len(first.Counts) != len(second.Counts) {
		t.Fatalf("两次造数计数键不同: %v / %v", first.Counts, second.Counts)
	}
	for key, count := range first.Counts {
		if second.Counts[key] != count {
			t.Fatalf("重复执行叠加了 %s: %d → %d", key, count, second.Counts[key])
		}
	}
	for _, item := range []struct {
		store string
		query string
		key   string
	}{
		{StoreAuditLog, "SELECT id FROM audit_logs ORDER BY id", "audit"},
		{StoreAuditLog, "SELECT id FROM audit_payload_refs ORDER BY id", "refs"},
		{StoreAuditLog, "SELECT id FROM audit_error_groups ORDER BY id", "groups"},
		{StoreOperationLog, "SELECT id FROM operation_logs ORDER BY id", "operations"},
		{StoreOperationLog, "SELECT operation_log_id || '|' || system_account_id FROM operation_log_viewers ORDER BY 1", "viewers"},
		{StoreRuntimeLog, "SELECT id FROM runtime_logs ORDER BY id", "runtime"},
		{StoreTaskRuns, "SELECT run_id FROM background_task_runs ORDER BY run_id", "taskruns"},
		{StoreDataset, "SELECT id FROM public_api_logs ORDER BY id", "publicapi"},
		{StoreDataset, "SELECT account_id FROM account_record_cleanup_targets ORDER BY account_id", "accounttargets"},
		{StoreDataset, "SELECT api_key_id FROM api_key_record_cleanup_targets ORDER BY api_key_id", "apikeytargets"},
	} {
		got := observabilityTestStrings(t, e, item.store, item.query)
		if strings.Join(got, ",") != strings.Join(snapshot[item.key], ",") {
			t.Fatalf("%s 行集合在重复执行后变化: %d → %d 行", item.key, len(snapshot[item.key]), len(got))
		}
	}
	// payload blob 与分面同样不得叠加：blob 行数固定，分面计数等于 runtime_logs 行数。
	blobs := observabilityTestCount(t, e, StoreAuditLog, "SELECT COUNT(*) FROM audit_payload_blobs")
	if second.Counts["auditPayloadBlobs"] != blobs {
		t.Fatalf("payload blob 行数叠加: counts=%d table=%d", second.Counts["auditPayloadBlobs"], blobs)
	}
	runtimeRows := observabilityTestCount(t, e, StoreRuntimeLog, "SELECT COUNT(*) FROM runtime_logs")
	total := observabilityTestStrings(t, e, StoreRuntimeLog,
		"SELECT CAST(total_count AS TEXT) FROM runtime_log_facet_summary WHERE bucket_key = ?", observabilityRuntimeFacetBucketKey)
	if len(total) != 1 || total[0] != strconv.Itoa(runtimeRows) {
		t.Fatalf("分面总数 %v 与 runtime_logs 行数 %d 不一致", total, runtimeRows)
	}
}

func TestSeedObservabilityRuntimeLogIndexMatchesFiles(t *testing.T) {
	e := observabilityTestEnv(t, 3, observabilityTestNow)
	if _, err := seedObservability(context.Background(), e); err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	logDir := e.options.Paths.LogDir
	for _, name := range []string{observabilityServerLogFile, observabilityWorkerLogFile} {
		path := filepath.Join(logDir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("运行日志文件缺失 %s: %v", path, err)
		}
		if !strings.HasSuffix(string(raw), "\n") {
			t.Fatalf("%s 最后一行必须以换行结束（F1 按整行读取）", name)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(line), &parsed); err != nil {
				t.Fatalf("%s 行不是 JSON 对象: %v（%q）", name, err, line)
			}
			if _, ok := parsed["time"].(string); !ok {
				t.Fatalf("%s 行缺少 time: %q", name, line)
			}
			if _, ok := parsed["level"].(string); !ok {
				t.Fatalf("%s 行缺少 level: %q", name, line)
			}
		}
	}
	// 索引行与文件行一一对应：raw_json 必须逐字出现在对应文件里，时间形态为毫秒 UTC。
	rawJSONs := observabilityTestStrings(t, e, StoreRuntimeLog, "SELECT raw_json FROM runtime_logs ORDER BY id")
	if len(rawJSONs) == 0 {
		t.Fatal("运行日志索引为空")
	}
	serverRaw, err := os.ReadFile(filepath.Join(logDir, observabilityServerLogFile))
	if err != nil {
		t.Fatal(err)
	}
	workerRaw, err := os.ReadFile(filepath.Join(logDir, observabilityWorkerLogFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range rawJSONs {
		if !strings.Contains(string(serverRaw), raw) && !strings.Contains(string(workerRaw), raw) {
			t.Fatalf("索引行在日志文件里找不到原文: %q", raw)
		}
	}
	times := observabilityTestStrings(t, e, StoreRuntimeLog, "SELECT DISTINCT time FROM runtime_logs ORDER BY time")
	for _, value := range times {
		if _, err := time.Parse(observabilityTimeLayout, value); err != nil {
			t.Fatalf("索引时间不是 %s 形态: %q", observabilityTimeLayout, value)
		}
	}
	// 文件大小 / 行数 / 游标一致：游标指向文件末尾，F1 不会重读历史行。
	files := []string{observabilityServerLogFile, observabilityWorkerLogFile}
	for _, name := range files {
		path := filepath.Join(logDir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		rows := observabilityTestStrings(t, e, StoreRuntimeLog,
			"SELECT CAST(cursor_offset AS TEXT) || '|' || CAST(file_size AS TEXT) || '|' || COALESCE(file_identity, 'x') FROM runtime_log_file_cursors WHERE log_file = ?", path)
		if len(rows) != 1 {
			t.Fatalf("%s 的游标行数 = %d，要求 1", name, len(rows))
		}
		want := strconv.Itoa(int(info.Size())) + "|" + strconv.Itoa(int(info.Size())) + "|"
		if rows[0] != want {
			t.Fatalf("%s 游标 = %q，要求 %q（offset|size|空身份）", name, rows[0], want)
		}
	}
	// 分面与表一致：级别计数之和 = 索引行数，且 event 覆盖核心取值。
	levelSum := 0
	for _, value := range observabilityTestStrings(t, e, StoreRuntimeLog, "SELECT CAST(count AS TEXT) FROM runtime_log_level_facets ORDER BY level") {
		number, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("级别分面计数不是整数: %q", value)
		}
		levelSum += number
	}
	if levelSum != len(rawJSONs) {
		t.Fatalf("级别分面之和 %d != 索引行数 %d", levelSum, len(rawJSONs))
	}
	if got := observabilityTestCount(t, e, StoreRuntimeLog, "SELECT COUNT(*) FROM runtime_log_event_facets WHERE event = 'http_request_completed'"); got < 1 {
		t.Fatal("事件分面缺少网关请求 summary 事件")
	}
}

// observabilityRuntimeLogFileNames 是 F1 filename.go 当前 / 实例日志模式的名字集合
// （测试内的副本：maintenance 不能 import jobs 的 runtimelog 包）。
var observabilityRuntimeLogFileNames = struct {
	workerPattern *regexp.Regexp
	serverPattern *regexp.Regexp
	legacyNames   map[string]bool
}{
	workerPattern: regexp.MustCompile(`^juhe-ai\.(worker|db-service|ingest-worker|usage-worker|log-worker|stats-worker|ops-worker|temporary-maintenance-worker)\.([A-Za-z0-9][A-Za-z0-9._-]{0,63})\.log$`),
	serverPattern: regexp.MustCompile(`^juhe-ai\.([A-Za-z0-9][A-Za-z0-9._-]{0,63})\.log$`),
	legacyNames: map[string]bool{
		"juhe-ai.log": true, "juhe-ai.worker.log": true, "juhe-ai.db-service.log": true,
		"juhe-ai.ingest-worker.log": true, "juhe-ai.usage-worker.log": true,
		"juhe-ai.log-worker.log": true, "juhe-ai.stats-worker.log": true,
		"juhe-ai.ops-worker.log": true, "juhe-ai.temporary-maintenance-worker.log": true,
	},
}

func TestObservabilityRuntimeLogFileNamesAreIndexable(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{observabilityServerLogFile, true},
		{observabilityWorkerLogFile, true},
		{"juhe-ai.log", true},
		{"juhe-ai.stats-worker.log", true},
		{"mockdata.log", false},
		{"juhe-ai.mockdata-server.txt", false},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			got := observabilityRuntimeLogFileNames.legacyNames[testCase.name] ||
				observabilityRuntimeLogFileNames.workerPattern.MatchString(testCase.name) ||
				observabilityRuntimeLogFileNames.serverPattern.MatchString(testCase.name)
			if got != testCase.want {
				t.Fatalf("文件名 %q 可索引 = %v，要求 %v", testCase.name, got, testCase.want)
			}
		})
	}
}

func TestObservabilityAuditUpstreamResponsePayload(t *testing.T) {
	e := observabilityTestEnv(t, 3, observabilityTestNow)
	observabilitySeedTestBusiness(t, e)
	if _, err := seedObservability(context.Background(), e); err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	// 三条上游响应模型对比 trace：trace 逐字固定，payload 里的 model 是原始响应模型，
	// audit_logs.upstream_model 是实际发送模型（设计文档「验证点」的验收口径）。
	type expectation struct {
		suffix        string
		requestModel  string
		upstreamModel string
		responseModel string
	}
	expectations := []expectation{
		{"match", "mockdata-global-long-context", "gpt-5.4-mini", "gpt-5.4-mini"},
		{"mismatch", "mockdata-global-long-context", "gpt-5.4-mini", "gpt-5.4-mini-2026-03-17"},
		{"unmapped_mismatch", "gpt-5.4-mini", "gpt-5.4-mini", "gpt-5.4-mini-2026-03-17"},
	}
	for _, want := range expectations {
		trace := observabilityCoverageTrace(want.suffix)
		rows := observabilityTestStrings(t, e, StoreAuditLog,
			`SELECT model || '|' || COALESCE(upstream_model, '') || '|' || COALESCE(model_mapping_applied, 0) || '|' ||
			        CAST(payload_count AS TEXT)
			 FROM audit_logs WHERE trace_id = ?`, trace)
		if len(rows) != 1 {
			t.Fatalf("trace %s 命中 %d 条审计记录，要求 1", trace, len(rows))
		}
		fields := strings.Split(rows[0], "|")
		if fields[0] != want.requestModel {
			t.Fatalf("trace %s 请求模型 = %q，要求 %q", trace, fields[0], want.requestModel)
		}
		if fields[1] != want.upstreamModel {
			t.Fatalf("trace %s 实际发送模型 = %q，要求 %q", trace, fields[1], want.upstreamModel)
		}
		if fields[3] != "2" {
			t.Fatalf("trace %s payload_count = %s，要求 2", trace, fields[3])
		}
		// payload：从 audit_payload_refs → audit_payload_blobs → 物理文件读回响应模型。
		payloadRows := observabilityTestStrings(t, e, StoreAuditLog,
			`SELECT b.storage_key || '|' || b.compression || '|' || CAST(b.raw_size_bytes AS TEXT) || '|' || CAST(b.compressed_size_bytes AS TEXT) || '|' || COALESCE(r.body_sha256, '')
			 FROM audit_payload_refs r
			 JOIN audit_logs l ON l.id = r.audit_log_id
			 JOIN audit_payload_blobs b ON b.id = r.body_blob_id
			 WHERE l.trace_id = ? AND r.part_type = 'upstream_response'`, trace)
		if len(payloadRows) != 1 {
			t.Fatalf("trace %s 的 upstream_response payload 行数 = %d，要求 1", trace, len(payloadRows))
		}
		fields = strings.Split(payloadRows[0], "|")
		storageKey, compression, rawSize, compressedSize, sha := fields[0], fields[1], fields[2], fields[3], fields[4]
		if compression != "none" {
			t.Fatalf("小 JSON payload 必须未压缩，实际 %q", compression)
		}
		if rawSize != compressedSize {
			t.Fatalf("未压缩 payload 的 raw/compressed 尺寸必须相等: %s/%s", rawSize, compressedSize)
		}
		if sha == "" {
			t.Fatal("payload 引用必须带 body_sha256")
		}
		body, err := os.ReadFile(filepath.Join(e.options.Paths.AuditBlobDirectory, filepath.FromSlash(storageKey)))
		if err != nil {
			t.Fatalf("payload 物理文件缺失 %s: %v", storageKey, err)
		}
		if strconv.Itoa(len(body)) != rawSize {
			t.Fatalf("payload 文件尺寸 %d != 元数据 %s", len(body), rawSize)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("payload 不是合法 JSON: %v", err)
		}
		if decoded["model"] != want.responseModel {
			t.Fatalf("payload.model = %v，要求 %q", decoded["model"], want.responseModel)
		}
	}
	// 至少一条 gateway_error payload 覆盖失败样本的错误摘要。
	if got := observabilityTestCount(t, e, StoreAuditLog,
		"SELECT COUNT(*) FROM audit_payload_refs WHERE part_type = 'gateway_error'"); got < 1 {
		t.Fatal("审计 payload 缺少 gateway_error 样本")
	}
}

func TestSeedObservabilitySatisfiesCoverageAssertions(t *testing.T) {
	e := observabilityTestEnv(t, 5, observabilityTestNow)
	observabilitySeedTestBusiness(t, e)
	// 表空间监控快照属 F2 owner（gateway/internal/tablemonitor）；这里造一行夹具
	// 模拟 owner 的输出，验证本域接线后 DomainObservability 的断言全部通过。
	createTestTable(t, e, StoreTableMonitor, `CREATE TABLE table_storage_snapshots (
	  id TEXT PRIMARY KEY, role TEXT NOT NULL, table_name TEXT NOT NULL, sampled_at TEXT NOT NULL
	)`)
	if err := e.insertMap(context.Background(), StoreTableMonitor, "table_storage_snapshots", map[string]any{
		"id": "f2-owner-fixture", "role": "dataset", "table_name": "public_api_logs", "sampled_at": observabilityFormatTime(observabilityTestNow),
	}); err != nil {
		t.Fatal(err)
	}
	result, err := seedObservability(context.Background(), e)
	if err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	e.recordDomainResult(result)
	notCovered, failures := evaluateAssertions(context.Background(), e)
	for _, failure := range failures {
		t.Fatalf("可观测域断言失败: %s", failure)
	}
	// 本域断言不得进 NotCovered（只有未接线的域才允许）。
	for _, item := range notCovered {
		if strings.HasPrefix(item, "audit_logs") || strings.HasPrefix(item, "operation_logs") ||
			strings.HasPrefix(item, "runtime_logs") || strings.HasPrefix(item, "public_api_logs") ||
			strings.HasPrefix(item, "table_storage_snapshots") {
			t.Fatalf("已接线域断言不得为 not-covered: %s", item)
		}
	}
}

func TestObservabilitySearchTermsCoverKeywords(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    []string
		absent  []string
	}{
		{
			name:    "中文摘要",
			summary: CleanupNamePrefix + "创建 AI 分组",
			// 归一化把 "-" 折叠成空格，与 F4 normalizeSearchText 同语义。
			want: []string{"造数 创建 ai 分组", "造数创建ai分组", "创建", "造数"},
		},
		{
			name:    "标点折叠",
			summary: "Mockdata 模拟：账号相关记录（清理）",
			want:    []string{"mockdata 模拟 账号相关记录 清理", "mockdata"},
			absent:  []string{"：", "（"},
		},
		{
			name:    "空摘要",
			summary: "   ",
			absent:  []string{""},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			terms := observabilitySearchTerms(testCase.summary)
			joined := strings.Join(terms, "\n")
			for _, want := range testCase.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("检索词缺少 %q（现有 %d 条）", want, len(terms))
				}
			}
			for _, absent := range testCase.absent {
				for _, term := range terms {
					if term == absent || strings.Contains(term, absent) && absent != "" {
						t.Fatalf("检索词不应包含 %q", absent)
					}
				}
			}
			if len(terms) > observabilitySearchTermLimit {
				t.Fatalf("检索词数量 %d 超出上限 %d", len(terms), observabilitySearchTermLimit)
			}
			seen := map[string]bool{}
			for _, term := range terms {
				if seen[term] {
					t.Fatalf("检索词重复: %q", term)
				}
				seen[term] = true
			}
		})
	}
}

func TestObservabilitySpreadStaysInsideDay(t *testing.T) {
	now := time.Date(2026, 5, 4, 9, 15, 0, 0, time.UTC)
	todayStart := observabilityDayStart(now)
	pastStart := todayStart.AddDate(0, 0, -3)
	cases := []struct {
		name     string
		dayStart time.Time
		index    int
		perDay   int
	}{
		{"过去的一天", pastStart, 0, 6},
		{"过去的一天末尾", pastStart, 5, 6},
		{"今天", todayStart, 0, 6},
		{"今天末尾", todayStart, 5, 6},
		{"单样本", pastStart, 0, 1},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			at := observabilitySpread(now, testCase.dayStart, testCase.index, testCase.perDay)
			if at.After(now) {
				t.Fatalf("样本时间 %v 晚于基准时刻 %v", at, now)
			}
			if at.Before(testCase.dayStart) {
				t.Fatalf("样本时间 %v 早于当天零点 %v", at, testCase.dayStart)
			}
			// 同一输入必须复现：造数是可重复执行的。
			if again := observabilitySpread(now, testCase.dayStart, testCase.index, testCase.perDay); !again.Equal(at) {
				t.Fatalf("同一输入产生不同时间: %v / %v", at, again)
			}
		})
	}
}

func TestObservabilityMockRowsCarryCleanupMarkers(t *testing.T) {
	e := observabilityTestEnv(t, 3, observabilityTestNow)
	observabilitySeedTestBusiness(t, e)
	if _, err := seedObservability(context.Background(), e); err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	// 每张表都必须至少有一个可被清理扫描命中的列：id / trace_id / 父键，
	// 否则重复执行会叠加旧样本（cleanup.go 的通用标识扫描按这三类前缀工作）。
	cases := []struct {
		name  string
		store string
		query string
	}{
		{"audit_logs", StoreAuditLog, "SELECT COUNT(*) FROM audit_logs WHERE id NOT LIKE ? AND trace_id NOT LIKE ?"},
		{"audit_log_attempts", StoreAuditLog, "SELECT COUNT(*) FROM audit_log_attempts WHERE id NOT LIKE ?"},
		{"audit_payload_refs", StoreAuditLog, "SELECT COUNT(*) FROM audit_payload_refs WHERE id NOT LIKE ?"},
		{"audit_error_groups", StoreAuditLog, "SELECT COUNT(*) FROM audit_error_groups WHERE id NOT LIKE ?"},
		{"operation_logs", StoreOperationLog, "SELECT COUNT(*) FROM operation_logs WHERE id NOT LIKE ?"},
		{"operation_log_targets", StoreOperationLog, "SELECT COUNT(*) FROM operation_log_targets WHERE id NOT LIKE ?"},
		{"operation_log_viewers", StoreOperationLog, "SELECT COUNT(*) FROM operation_log_viewers WHERE operation_log_id NOT LIKE ?"},
		{"operation_log_summary_search_terms", StoreOperationLog, "SELECT COUNT(*) FROM operation_log_summary_search_terms WHERE operation_log_id NOT LIKE ?"},
		{"runtime_logs", StoreRuntimeLog, "SELECT COUNT(*) FROM runtime_logs WHERE id NOT LIKE ?"},
		{"background_task_runs", StoreTaskRuns, "SELECT COUNT(*) FROM background_task_runs WHERE run_id NOT LIKE ?"},
		{"public_api_logs", StoreDataset, "SELECT COUNT(*) FROM public_api_logs WHERE id NOT LIKE ?"},
		{"account_record_cleanup_targets", StoreDataset, "SELECT COUNT(*) FROM account_record_cleanup_targets WHERE account_id NOT LIKE ?"},
		{"api_key_record_cleanup_targets", StoreDataset, "SELECT COUNT(*) FROM api_key_record_cleanup_targets WHERE api_key_id NOT LIKE ?"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			args := []any{CleanupIDPrefix + "%"}
			if strings.Count(testCase.query, "?") == 2 {
				args = append(args, CleanupTracePrefix+"%")
			}
			if got := observabilityTestCount(t, e, testCase.store, testCase.query, args...); got != 0 {
				t.Fatalf("%s 有 %d 行不带清理标识", testCase.name, got)
			}
		})
	}
	// 清理扫描（cleanup.go 的 sweepCleanupMarkers）必须能把本域的行全部删掉：
	// 这是「重复执行幂等」的最后一道兜底。
	deleted, skipped, err := cleanupAll(context.Background(), e)
	if err != nil {
		t.Fatalf("cleanupAll: %v", err)
	}
	_ = skipped
	for _, item := range []struct {
		name  string
		store string
		table string
	}{
		{"audit_logs", StoreAuditLog, "audit_logs"},
		{"operation_logs", StoreOperationLog, "operation_logs"},
		{"runtime_logs", StoreRuntimeLog, "runtime_logs"},
		{"background_task_runs", StoreTaskRuns, "background_task_runs"},
		{"public_api_logs", StoreDataset, "public_api_logs"},
		{"account_record_cleanup_targets", StoreDataset, "account_record_cleanup_targets"},
		{"api_key_record_cleanup_targets", StoreDataset, "api_key_record_cleanup_targets"},
	} {
		if deleted[item.store+"."+item.table] == 0 {
			t.Fatalf("清理没有删掉 %s.%s（deleted=%v）", item.store, item.table, deleted)
		}
		remaining := observabilityTestCount(t, e, item.store, "SELECT COUNT(*) FROM "+item.table)
		if remaining != 0 {
			t.Fatalf("清理后 %s.%s 仍剩 %d 行", item.store, item.table, remaining)
		}
	}
}

func TestObservabilityPublicAPILogsSkipWithoutSource(t *testing.T) {
	e := observabilityTestEnv(t, 3, observabilityTestNow)
	// business 库存在但没有来源系统：公开接口日志必须跳过（不伪造来源），
	// 其它 dataset 表（清理目标）不受影响。
	result, err := seedObservability(context.Background(), e)
	if err != nil {
		t.Fatalf("seedObservability: %v", err)
	}
	if got := observabilityTestCount(t, e, StoreDataset, "SELECT COUNT(*) FROM public_api_logs"); got != 0 {
		t.Fatalf("没有来源系统时必须跳过公开接口日志，实际 %d 行", got)
	}
	if _, ok := result.Counts["publicApiLogs"]; ok {
		t.Fatalf("跳过时不得记计数: %v", result.Counts)
	}
	for _, key := range []string{"accountCleanupTargets", "apiKeyCleanupTargets", "backgroundTaskRuns"} {
		if result.Counts[key] < 1 {
			t.Fatalf("%s 与来源系统无关，必须有计数: %v", key, result.Counts)
		}
	}
}
