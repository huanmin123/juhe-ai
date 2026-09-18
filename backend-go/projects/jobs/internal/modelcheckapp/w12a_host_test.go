package modelcheckapp

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckruntime"
)

// w12a_host_test.go 补齐 OpenHost 的 SQLite 构造器错误臂与 PostgreSQL 装配臂。
// PostgreSQL 臂走 w1cover 门禁覆盖库：durable EnsureSchema 为只读校验，
// dataset EnsureSchema 为幂等 IF NOT EXISTS DDL；连接串永不进入日志或断言。

func w12aConfigWithEmptyCredentialSecret(t *testing.T) modelcheckruntime.RuntimeConfig {
	t.Helper()
	cfg := validSQLiteConfig(t)
	cfg.CredentialSecret = " "
	return cfg
}

func TestW12aOpenHostSQLiteFailsWhenCredentialSecretEmpty(t *testing.T) {
	// 契约：凭据密钥缺失时业务读取器构造必须 fail-closed，并回收已打开的连接。
	cfg := w12aConfigWithEmptyCredentialSecret(t)
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, sqliteBusinessFixtureSchema)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("凭据密钥为空时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "credential secret is required") {
		t.Fatalf("错误应指向凭据密钥缺失，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestW12aOpenHostSQLiteFailsWhenIdentitySecretEmpty(t *testing.T) {
	// 契约：身份密钥缺失同样在业务读取器构造处 fail-closed。
	cfg := validSQLiteConfig(t)
	cfg.IdentitySecret = ""
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, sqliteBusinessFixtureSchema)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("身份密钥为空时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "identity secret is required") {
		t.Fatalf("错误应指向身份密钥缺失，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

// w12aW1CoverPostgresURLs 返回门禁覆盖库的 jobs/business URL；不可用时跳过。
func w12aW1CoverPostgresURLs(t *testing.T) (jobsURL, businessURL string) {
	t.Helper()
	raw, err := os.ReadFile(`F:\sub2api-lite\.local\project-resources\dev\env\shared.env`)
	if err != nil {
		t.Skip("w12a PG gated: shared.env 不可读")
	}
	base := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "JUHE_AI_POSTGRES_URL=") {
			base = strings.TrimPrefix(line, "JUHE_AI_POSTGRES_URL=")
		}
	}
	if base == "" {
		t.Skip("w12a PG gated: shared.env 缺少 JUHE_AI_POSTGRES_URL")
	}
	base = strings.Replace(base, ":6432/", ":5432/", 1)
	base = strings.Replace(base, "/juhe_ai_sub2api_dev?", "/juhe_ai_sub2api_dev_w1cover?", 1)
	return base, base
}

func TestW12aOpenHostPostgresAssembles(t *testing.T) {
	// 契约：PostgreSQL 装配臂按 durable -> dataset -> business reader 的顺序
	// 打开并校验。w1cover 覆盖库的历史遗留全 text 列已按权威 DDL 修复，
	// 业务读取器契约通过，OpenHost 应完整装配（历史前提“契约 fail-closed”
	// 随覆盖库修复作废）。
	jobsURL, businessURL := w12aW1CoverPostgresURLs(t)

	// 覆盖库可能尚未建 durable 四表；这里按维护项目权威 DDL 幂等补建
	// （加法式 CREATE TABLE IF NOT EXISTS，schema 归属 juhe_jobs）。
	boot, err := sqlOpenGuarded(jobsURL)
	if err != nil {
		t.Skip("w12a PG gated: 打开失败")
	}
	defer boot.Close()
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := boot.PingContext(pingCtx); err != nil {
		t.Skip("w12a PG gated: PG 不可达")
	}
	for _, ddl := range w12aDurableFixtureDDL {
		if _, err := boot.Exec(ddl); err != nil {
			t.Skipf("w12a PG gated: durable fixture 建表失败: %v", err)
		}
	}
	for _, ddl := range w12aDatasetFixtureDDL {
		if _, err := boot.Exec(ddl); err != nil {
			t.Skipf("w12a PG gated: dataset fixture 建表失败: %v", err)
		}
	}

	dir := t.TempDir()
	cfg := modelcheckruntime.RuntimeConfig{
		Enabled:              true,
		StoreMode:            "postgres",
		JobsPostgresURL:      jobsURL,
		BusinessPostgresURL:  businessURL,
		DatasetDatabasePath:  filepath.Join(dir, "unused-dataset.sqlite3"),
		BusinessDatabasePath: filepath.Join(dir, "unused-business.sqlite3"),
		CredentialSecret:     "w12a-credential-secret",
		IdentitySecret:       "w12a-identity-secret",
		ProbeSetVersion:      "w12a-probe-v1",
		Deadline:             time.Minute,
		Heartbeat:            time.Second,
	}
	host, err := OpenHost(context.Background(), cfg)
	if err != nil {
		t.Fatalf("覆盖库修复后 OpenHost 应完整装配: %v", err)
	}
	if host == nil || !host.Ready() {
		t.Fatalf("Host 应就绪: %+v", host)
	}
	if host.Handler == nil || host.Service == nil {
		t.Fatalf("Handler/Service 应完成组装: %+v", host)
	}
	if err := host.Close(); err != nil {
		t.Fatalf("Close 应返回 nil: %v", err)
	}
}

// w12aDurableFixtureDDL 与 backend-go/projects/maintenance j3bmodelcheck
// bootstrap 的权威 durable 建表语句同形，仅把 schema 指到 jobs 侧的 juhe_jobs。
var w12aDurableFixtureDDL = []string{
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version BIGINT NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, UNIQUE(identity_key,input_version), UNIQUE(identity_key,input_digest))`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_jobs.model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE)`,
}

func sqlOpenGuarded(dsn string) (*sql.DB, error) {
	return sql.Open("pgx", dsn)
}

// w12aDatasetFixtureDDL 按 modelcheckstore 权威列集在 juhe_dataset 幂等补建
// dataset 三表，供装配链路的只读 EnsureSchema 校验通过。
var w12aDatasetFixtureDDL = []string{
	`CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL DEFAULT 'quick', trigger_kind TEXT NOT NULL DEFAULT 'manual', schedule_id TEXT, trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0, trusted_comparison_available INTEGER NOT NULL DEFAULT 0, level TEXT NOT NULL DEFAULT 'unavailable', score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 100, status TEXT NOT NULL DEFAULT 'running', message TEXT NOT NULL DEFAULT '', trace_id TEXT, probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1', started_at TEXT NOT NULL, finished_at TEXT, duration_ms INTEGER, request_summary_json TEXT NOT NULL DEFAULT '{}', result_summary_json TEXT NOT NULL DEFAULT '{}', policy_snapshot_json TEXT NOT NULL DEFAULT '{}', quality_decision_json TEXT NOT NULL DEFAULT '{}', quality_health_sync_status TEXT, error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL, score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL DEFAULT '{}', error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS juhe_dataset.model_check_observations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, provider_code TEXT NOT NULL, provider_protocol_profile_id TEXT NOT NULL DEFAULT 'unknown', endpoint_family TEXT NOT NULL DEFAULT 'unknown', requested_model TEXT NOT NULL, mapped_upstream_model TEXT NOT NULL, observed_model TEXT, mapping_applied INTEGER NOT NULL DEFAULT 0, upstream_bucket_hmac TEXT NOT NULL DEFAULT '', cohort_key_hmac TEXT NOT NULL DEFAULT '', population_key_hmac TEXT NOT NULL DEFAULT '', probe_key_hmac TEXT NOT NULL DEFAULT '', system_fingerprint_hmac TEXT, probe_family TEXT NOT NULL, probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1', tokenizer_version TEXT NOT NULL DEFAULT 'unavailable', feature_version TEXT NOT NULL DEFAULT 'none', round_index INTEGER NOT NULL DEFAULT 0, padding_tokens INTEGER NOT NULL DEFAULT 0, local_input_tokens INTEGER NOT NULL DEFAULT 0, reported_input_tokens INTEGER, cached_input_tokens INTEGER, constraint_passed INTEGER, feature_1 DOUBLE PRECISION, feature_2 DOUBLE PRECISION, feature_3 DOUBLE PRECISION, feature_4 DOUBLE PRECISION, feature_5 DOUBLE PRECISION, feature_6 DOUBLE PRECISION, feature_7 DOUBLE PRECISION, feature_8 DOUBLE PRECISION, observation_status TEXT NOT NULL, identity_status TEXT NOT NULL, mapping_status TEXT NOT NULL, protocol_status TEXT NOT NULL, evidence_coverage INTEGER NOT NULL DEFAULT 0, trace_id TEXT, created_at TEXT NOT NULL, aggregation_completed_at TEXT)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_created ON juhe_dataset.model_check_runs(created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_created ON juhe_dataset.model_check_runs(system_account_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_actor_created ON juhe_dataset.model_check_runs(actor_system_account_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_model_created ON juhe_dataset.model_check_runs(model,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_level_created ON juhe_dataset.model_check_runs(level,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_status_created ON juhe_dataset.model_check_runs(status,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_target_created ON juhe_dataset.model_check_runs(target_type,target_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_account_created ON juhe_dataset.model_check_runs(account_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_trigger_created ON juhe_dataset.model_check_runs(trigger_kind,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_retry ON juhe_dataset.model_check_runs(quality_health_sync_status,updated_at,id) WHERE quality_health_sync_status='failed'`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_model_created ON juhe_dataset.model_check_runs(system_account_id,model,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_level_created ON juhe_dataset.model_check_runs(system_account_id,level,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_status_created ON juhe_dataset.model_check_runs(system_account_id,status,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_runs_system_account_target_created ON juhe_dataset.model_check_runs(system_account_id,target_type,target_id,created_at DESC,id DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_items_run_order ON juhe_dataset.model_check_items(run_id,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_items_run_key ON juhe_dataset.model_check_items(run_id,item_key,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_items_run_status ON juhe_dataset.model_check_items(run_id,status,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_cursor ON juhe_dataset.model_check_observations(created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_pending_aggregation ON juhe_dataset.model_check_observations(created_at,id) WHERE aggregation_completed_at IS NULL`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_account_model ON juhe_dataset.model_check_observations(system_account_id,account_id,requested_model,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_cohort ON juhe_dataset.model_check_observations(cohort_key_hmac,mapped_upstream_model,created_at,id)`,
	`CREATE INDEX IF NOT EXISTS idx_model_check_observations_population ON juhe_dataset.model_check_observations(population_key_hmac,requested_model,probe_family,created_at,id)`,
}

func TestW12aOpenHostPostgresFailsWhenJobsURLBlank(t *testing.T) {
	// 契约：PostgreSQL 模式下 jobs URL 为空必须在 durable 打开处 fail-closed。
	cfg := validSQLiteConfig(t)
	cfg.StoreMode = "postgres"
	cfg.JobsPostgresURL = " "
	cfg.BusinessPostgresURL = "postgres://w12a.invalid/db"
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("jobs URL 为空时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "model check durable PostgreSQL URL is required") {
		t.Fatalf("错误应指向 durable URL 缺失，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestW12aOpenHostPostgresFailsWhenCredentialSecretBlank(t *testing.T) {
	// 契约：PostgreSQL 模式下凭据密钥为空在业务读取器构造处 fail-closed；
	// sql.Open 惰性连接，此处不需要真实数据库。
	cfg := validSQLiteConfig(t)
	cfg.StoreMode = "postgres"
	cfg.JobsPostgresURL = "postgres://w12a.invalid/juhe_jobs"
	cfg.BusinessPostgresURL = "postgres://w12a.invalid/juhe_business"
	cfg.CredentialSecret = " "
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("凭据密钥为空时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "credential secret is required") {
		t.Fatalf("错误应指向凭据密钥缺失，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}

func TestW12aOpenHostSQLiteFailsWhenProbeSetVersionBlank(t *testing.T) {
	// 契约：探针集版本为空时命令构建器必须 fail-closed。
	cfg := validSQLiteConfig(t)
	cfg.ProbeSetVersion = " "
	prepareSQLiteBusinessDB(t, cfg.BusinessDatabasePath, sqliteBusinessFixtureSchema)
	host, err := OpenHost(context.Background(), cfg)
	if err == nil {
		_ = host.Close()
		t.Fatalf("探针集版本为空时 OpenHost 应报错")
	}
	if !strings.Contains(err.Error(), "probe set snapshot is required") {
		t.Fatalf("错误应指向探针集版本缺失，实际: %v", err)
	}
	if host != nil {
		t.Fatalf("失败路径应返回 nil Host")
	}
}
