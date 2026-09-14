package main

// 离线迁移命令的单元层覆盖：伪造旧 Node SQLite 源（最小 legacy schema +
// 数据），经 w1CallMain 走 main() 的正常 return 路径。os.Exit(2)/fail()
// 分支不属于进程内可测面，由 w1_main_boot_test.go 的进程级场景覆盖。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// w1LegacyAuditTablesDDL 与 auditlog.legacyAuditTables 的列清单逐列一致；
// 源表列序即复制语句 INSERT INTO ... SELECT 的契约。
const w1LegacyAuditLogsDDL = `CREATE TABLE audit_logs (id TEXT,trace_id TEXT,traffic_source TEXT,system_account_id TEXT,api_key_id TEXT,conversation_key TEXT,session_id TEXT,session_client_type TEXT,group_id TEXT,account_id TEXT,provider_code TEXT,method TEXT,path TEXT,query_string TEXT,model TEXT,upstream_model TEXT,pricing_model TEXT,model_mapping_applied INTEGER,model_mapping_source TEXT,source_endpoint_family TEXT,upstream_endpoint_family TEXT,stream INTEGER,client_ip TEXT,user_agent TEXT,audit_outcome TEXT,success INTEGER,final_status_code INTEGER,error_phase TEXT,error_code TEXT,error_message TEXT,sample_bucket INTEGER,sample_reason TEXT,attempt_count INTEGER,payload_count INTEGER,raw_payload_bytes INTEGER,compressed_payload_bytes INTEGER,compression_saved_bytes INTEGER,error_group_id TEXT,capture_status TEXT,lifecycle_status TEXT,started_at TEXT,ended_at TEXT,duration_ms INTEGER,http_completed_at TEXT,http_duration_ms INTEGER,first_token_ms INTEGER,created_at TEXT)`

const w1LegacyAuditAttemptsDDL = `CREATE TABLE audit_log_attempts (id TEXT,audit_log_id TEXT,attempt_index INTEGER,account_id TEXT,account_owner_system_account_id TEXT,group_id TEXT,proxy_url TEXT,provider_code TEXT,attempt_model TEXT,attempt_upstream_model TEXT,attempt_pricing_model TEXT,attempt_model_mapping_applied INTEGER,attempt_model_mapping_source TEXT,attempt_source_endpoint_family TEXT,attempt_upstream_endpoint_family TEXT,upstream_method TEXT,upstream_url TEXT,upstream_status_code INTEGER,success INTEGER,error_phase TEXT,error_code TEXT,error_message TEXT,started_at TEXT,ended_at TEXT,duration_ms INTEGER)`

const w1LegacyAuditBlobsDDL = `CREATE TABLE audit_payload_blobs (id TEXT,sha256 TEXT,raw_size_bytes INTEGER,compressed_size_bytes INTEGER,content_type TEXT,content_encoding TEXT,compression TEXT,storage_key TEXT,ref_count INTEGER,first_seen_at TEXT,last_seen_at TEXT,created_at TEXT)`

const w1LegacyAuditRefsDDL = `CREATE TABLE audit_payload_refs (id TEXT,audit_log_id TEXT,attempt_id TEXT,part_type TEXT,sequence_index INTEGER,content_type TEXT,content_encoding TEXT,headers_blob_id TEXT,body_blob_id TEXT,headers_sha256 TEXT,body_sha256 TEXT,raw_size_bytes INTEGER,compressed_size_bytes INTEGER,capture_status TEXT,drop_reason TEXT,created_at TEXT)`

const w1LegacyAuditErrorGroupsDDL = `CREATE TABLE audit_error_groups (id TEXT,fingerprint TEXT,window_started_at TEXT,window_ended_at TEXT,system_account_id TEXT,api_key_id TEXT,group_id TEXT,account_id TEXT,provider_code TEXT,path TEXT,model TEXT,status_code INTEGER,error_phase TEXT,error_code TEXT,error_type TEXT,request_fingerprint TEXT,error_fingerprint TEXT,count INTEGER,first_event_id TEXT,last_event_id TEXT,sample_event_id TEXT,last_message TEXT,created_at TEXT,updated_at TEXT)`

// w1CreateLegacyAuditSource 构造一份可通过全部迁移校验的旧 F3 审计源：
// 五表各一行、引用闭合、blob 文件 sha256/大小一致（compression=none）。
func w1CreateLegacyAuditSource(t *testing.T, root string) (sourcePath string, sourceBlobs string) {
	t.Helper()
	sourcePath = filepath.Join(root, "legacy-audit.sqlite3")
	sourceBlobs = filepath.Join(root, "legacy-audit-blobs")
	db, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatalf("open legacy audit source = %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, ddl := range []string{w1LegacyAuditLogsDDL, w1LegacyAuditAttemptsDDL, w1LegacyAuditBlobsDDL, w1LegacyAuditRefsDDL, w1LegacyAuditErrorGroupsDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create legacy audit table = %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO audit_logs (id,trace_id,traffic_source,system_account_id,api_key_id,conversation_key,session_id,session_client_type,group_id,account_id,provider_code,method,path,query_string,model,upstream_model,pricing_model,model_mapping_applied,model_mapping_source,source_endpoint_family,upstream_endpoint_family,stream,client_ip,user_agent,audit_outcome,success,final_status_code,error_phase,error_code,error_message,sample_bucket,sample_reason,attempt_count,payload_count,raw_payload_bytes,compressed_payload_bytes,compression_saved_bytes,error_group_id,capture_status,lifecycle_status,started_at,ended_at,duration_ms,http_completed_at,http_duration_ms,first_token_ms,created_at) VALUES ('log-1','trace-1','gateway','sys_owner','key-1','conv-1','sess-1','cli','group_main','acc_1','openai','GET','/v1/test','q=1','gpt-test','gpt-test','gpt-test',0,'','chat','chat',0,'127.0.0.1','w1-agent','gateway_succeeded',1,200,'','','',1,'w1',1,1,10,8,2,'aeg-1','complete','finalized','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z',1000,'2026-01-01T00:00:01Z',1000,50,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy audit_logs = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_log_attempts (id,audit_log_id,attempt_index,account_id,attempt_model_mapping_applied,upstream_method,upstream_url,upstream_status_code,success,started_at,ended_at,duration_ms) VALUES ('alat-1','log-1',0,'acc_1',0,'GET','http://legacy/upstream',200,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:01Z',1000)`); err != nil {
		t.Fatalf("insert legacy audit_log_attempts = %v", err)
	}
	raw := []byte("legacy audit payload for w1")
	digest := sha256.Sum256(raw)
	storageKey := "aa/payload.blob"
	if _, err := db.Exec(`INSERT INTO audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,content_encoding,compression,storage_key,ref_count,first_seen_at,last_seen_at,created_at) VALUES ('blob-1',?,?,?,?,?,?,?,?,?,?,?)`, hex.EncodeToString(digest[:]), len(raw), len(raw), "text/plain", "", "none", storageKey, 1, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("insert legacy audit_payload_blobs = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_payload_refs (id,audit_log_id,attempt_id,part_type,sequence_index,content_type,body_blob_id,raw_size_bytes,compressed_size_bytes,capture_status,created_at) VALUES ('ref-1','log-1','alat-1','gateway_response',0,'text/plain','blob-1',?,?, 'complete','2026-01-01T00:00:00Z')`, len(raw), len(raw)); err != nil {
		t.Fatalf("insert legacy audit_payload_refs = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO audit_error_groups (id,fingerprint,window_started_at,window_ended_at,system_account_id,path,count,sample_event_id,created_at,updated_at) VALUES ('aeg-1','fp-w1','2026-01-01T00:00:00Z','2026-01-01T00:01:00Z','sys_owner','/v1/test',1,'log-1','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy audit_error_groups = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy audit source = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sourceBlobs, "aa"), 0o750); err != nil {
		t.Fatalf("mkdir source blobs = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceBlobs, "aa", "payload.blob"), raw, 0o640); err != nil {
		t.Fatalf("write source blob = %v", err)
	}
	return sourcePath, sourceBlobs
}

func TestW1MainAuditLegacyMigrationCommand(t *testing.T) {
	root := t.TempDir()
	sourcePath, sourceBlobs := w1CreateLegacyAuditSource(t, root)
	targetPath := filepath.Join(root, "target-audit.sqlite3")
	targetBlobs := filepath.Join(root, "target-audit-blobs")
	output := w1CallMain(t,
		"-migrate-audit-log-legacy-sqlite",
		"-source-db="+sourcePath,
		"-target-db="+targetPath,
		"-source-blob-dir="+sourceBlobs,
		"-target-blob-dir="+targetBlobs,
		"-node-stopped", "-go-stopped",
	)
	var result struct {
		NoOp        bool             `json:"noOp"`
		TableCounts map[string]int64 `json:"tableCounts"`
		BlobCount   int64            `json:"blobCount"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		t.Fatalf("audit 迁移结果必须是 JSON = %v\n输出：%s", err, output)
	}
	if result.NoOp {
		t.Fatalf("首次迁移不得是 noOp：%s", output)
	}
	for _, table := range []string{"audit_logs", "audit_log_attempts", "audit_payload_blobs", "audit_payload_refs", "audit_error_groups"} {
		if result.TableCounts[table] != 1 {
			t.Fatalf("表 %s 迁移行数必须为 1 = %+v", table, result.TableCounts)
		}
	}
	if result.BlobCount != 1 {
		t.Fatalf("blobCount = %d", result.BlobCount)
	}
	if _, err := os.Stat(filepath.Join(targetBlobs, "aa", "payload.blob")); err != nil {
		t.Fatalf("目标 blob 文件必须落盘 = %v", err)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("目标审计库必须创建 = %v", err)
	}
}

// w1CreateLegacyOperationSource 构造旧 Node F4 操作日志源：四表 + 一条
// 完整 operation_logs（含 target/viewer 子行），时间戳使用 legacy 契约的
// 纳秒 RFC3339 格式（scanLegacyOperationLog 的 parseStorageTime 可解析）。
func w1CreateLegacyOperationSource(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy operation source = %v", err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE operation_logs (id TEXT PRIMARY KEY,trace_id TEXT,actor_system_account_id TEXT NOT NULL,actor_username TEXT,actor_display_name TEXT,actor_role TEXT NOT NULL,operation_scope_system_account_id TEXT,mode TEXT NOT NULL,module TEXT NOT NULL,action TEXT NOT NULL,operation_key TEXT NOT NULL,resource_type TEXT NOT NULL,resource_id TEXT,resource_name TEXT,summary TEXT NOT NULL,detail_level TEXT NOT NULL,visibility_scope TEXT NOT NULL,changes_json TEXT NOT NULL,metadata_json TEXT NOT NULL,method TEXT,path TEXT,status_code INTEGER,client_ip TEXT,user_agent TEXT,created_at TEXT NOT NULL);
CREATE TABLE operation_log_targets (id TEXT PRIMARY KEY,operation_log_id TEXT NOT NULL,target_type TEXT NOT NULL,target_id TEXT,target_name TEXT,target_owner_system_account_id TEXT,relation TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE operation_log_viewers (operation_log_id TEXT NOT NULL,system_account_id TEXT NOT NULL,visibility_reason TEXT NOT NULL,detail_level TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(operation_log_id,system_account_id,visibility_reason));
CREATE TABLE operation_log_summary_search_terms (operation_log_id TEXT NOT NULL,term TEXT NOT NULL,created_at TEXT NOT NULL,PRIMARY KEY(term,operation_log_id));`)
	if err != nil {
		t.Fatalf("create legacy operation tables = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO operation_logs (id,trace_id,actor_system_account_id,actor_username,actor_display_name,actor_role,operation_scope_system_account_id,mode,module,action,operation_key,resource_type,resource_id,resource_name,summary,detail_level,visibility_scope,changes_json,metadata_json,method,path,status_code,client_ip,user_agent,created_at) VALUES ('oplog-w1','trace-w1','sys_owner','admin','Admin','admin','sys_owner','self','api_keys','update','api_keys.update','api_key','key-1','Key One','legacy summary copy','full','all_users','[]','{}','POST','/__aisys__/api/keys',200,'127.0.0.1','w1-agent','2026-08-13T08:00:01.100000000+08:00')`); err != nil {
		t.Fatalf("insert legacy operation_logs = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO operation_log_targets (id,operation_log_id,target_type,target_id,target_name,target_owner_system_account_id,relation,created_at) VALUES ('tgt-w1','oplog-w1','api_key','key-1','Key One','sys_owner','primary','2026-08-13T08:00:01.200000000+08:00')`); err != nil {
		t.Fatalf("insert legacy operation_log_targets = %v", err)
	}
	if _, err := db.Exec(`INSERT INTO operation_log_viewers (operation_log_id,system_account_id,visibility_reason,detail_level,created_at) VALUES ('oplog-w1','sys_owner','resource_owner','full','2026-08-13T08:00:01.200000000+08:00')`); err != nil {
		t.Fatalf("insert legacy operation_log_viewers = %v", err)
	}
}

// w1CreateBusinessSettingsDB 复刻 F4 store 需要的业务库设置面：保留天数
// 设置 + 系统账户镜像（名字解析兜底查询的数据源）。
func w1CreateBusinessSettingsDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open business settings db = %v", err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE system_settings (system_account_id TEXT NOT NULL,key TEXT NOT NULL,value_json TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(system_account_id,key));
CREATE TABLE system_accounts (id TEXT PRIMARY KEY,username TEXT NOT NULL,display_name TEXT NOT NULL);
INSERT INTO system_accounts VALUES ('sys_owner','sys_owner','System Owner');
INSERT INTO system_settings VALUES ('sys_admin','operationLogRetentionDays','365','2026-08-13T00:00:00Z')`)
	if err != nil {
		t.Fatalf("create business settings schema = %v", err)
	}
}

// w1SetOperationMigrationEnv 设置 F4 CLI 迁移的 LoadConfig 契约：sqlite 模式、
// 目标库/业务设置库/实例 ID/usage shard 根，并把七个隔离路径全部钉进本次
// 临时目录，避免继承环境的 JUHE_AI_* 路径干扰隔离校验。
func w1SetOperationMigrationEnv(t *testing.T, root string) {
	t.Helper()
	t.Setenv("JUHE_AI_OPERATION_LOG_STORE", "sqlite")
	t.Setenv("JUHE_AI_OPERATION_LOG_INSTANCE_ID", "w1-migrate-instance")
	t.Setenv("JUHE_AI_OPERATION_LOG_DATABASE_PATH", filepath.Join(root, "target-operation.sqlite3"))
	t.Setenv("JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH", filepath.Join(root, "business-settings.sqlite3"))
	t.Setenv("JUHE_AI_USAGE_SHARD_ROOT", filepath.Join(root, "usage-shards"))
	for _, key := range []string{"JUHE_AI_DATABASE_PATH", "JUHE_AI_DATASET_DATABASE_PATH", "JUHE_AI_RUNTIME_LOG_DATABASE_PATH", "JUHE_AI_TABLE_MONITOR_DATABASE_PATH", "JUHE_AI_AUDIT_LOG_DATABASE_PATH", "JUHE_AI_USAGE_CATALOG_DATABASE_PATH", "JUHE_AI_STATS_DATABASE_PATH"} {
		t.Setenv(key, filepath.Join(root, "isolation", strings.ToLower(strings.TrimPrefix(key, "JUHE_AI_"))+".sqlite3"))
	}
}

func TestW1MainOperationLegacyMigrationCommand(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "legacy-operation.sqlite3")
	w1CreateLegacyOperationSource(t, sourcePath)
	w1CreateBusinessSettingsDB(t, filepath.Join(root, "business-settings.sqlite3"))
	w1SetOperationMigrationEnv(t, root)
	output := w1CallMain(t,
		"-migrate-operation-log-legacy-sqlite",
		"-operation-log-source-db="+sourcePath,
		"-node-stopped", "-go-stopped", "-backup-confirmed",
	)
	var result struct {
		Mode                  string           `json:"mode"`
		NoOp                  bool             `json:"noOp"`
		SourceCounts          map[string]int64 `json:"sourceCounts"`
		TargetCounts          map[string]int64 `json:"targetCounts"`
		SearchTermsRebuilt    bool             `json:"searchTermsRebuilt"`
		MigratedOperationLogs int64            `json:"migratedOperationLogs"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		t.Fatalf("operation 迁移结果必须是 JSON = %v\n输出：%s", err, output)
	}
	if result.Mode != "sqlite-copy" || result.NoOp || !result.SearchTermsRebuilt {
		t.Fatalf("迁移结果契约不符 = %s", output)
	}
	if result.SourceCounts["operation_logs"] != 1 || result.TargetCounts["operation_logs"] != 1 {
		t.Fatalf("源/目标 operation_logs 行数必须为 1：source=%d target=%d", result.SourceCounts["operation_logs"], result.TargetCounts["operation_logs"])
	}
	if result.MigratedOperationLogs != 1 {
		t.Fatalf("MigratedOperationLogs = %d", result.MigratedOperationLogs)
	}
	if result.SourceCounts["operation_log_targets"] != 1 || result.SourceCounts["operation_log_viewers"] != 1 {
		t.Fatalf("子表源行数必须为 1：%+v", result.SourceCounts)
	}
	if _, err := os.Stat(filepath.Join(root, "target-operation.sqlite3")); err != nil {
		t.Fatalf("目标 F4 库必须创建 = %v", err)
	}
}
