package j3bmodelcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

const SQLiteBootstrapEnv = "JUHE_AI_MAINTENANCE_J3B_SQLITE_PATH"

const (
	SchemaName    = "juhe_j3b"
	BootstrapEnv  = "JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL"
	bootstrapLock = int64(732_946_110_271_044_016)
)

type Report struct {
	Database       string   `json:"database"`
	Schema         string   `json:"schema"`
	CurrentRole    string   `json:"currentRole"`
	SchemaOwner    string   `json:"schemaOwner"`
	MissingSchema  bool     `json:"missingSchema"`
	OwnerMismatch  bool     `json:"ownerMismatch"`
	MissingTables  []string `json:"missingTables"`
	InvalidTables  []string `json:"invalidTables"`
	MissingIndexes []string `json:"missingIndexes"`
	InvalidIndexes []string `json:"invalidIndexes"`
	Applied        bool     `json:"applied"`
}

func (r Report) Ready() bool {
	return !r.MissingSchema && !r.OwnerMismatch && len(r.MissingTables) == 0 && len(r.InvalidTables) == 0 && len(r.MissingIndexes) == 0 && len(r.InvalidIndexes) == 0
}

func Open(rawURL string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Hostname() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return nil, errors.New("J3b bootstrap 必须提供包含主机、数据库和显式角色的 postgres URL")
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 J3b bootstrap PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// OpenSQLite opens a dedicated J3b file. It never points at the legacy
// Business SQLite path; callers must provide the explicit J3b path.
func OpenSQLite(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("J3b SQLite bootstrap 必须提供专属文件路径")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开 J3b SQLite bootstrap 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// RunSQLite checks or explicitly bootstraps the dedicated J3b schema. Apply
// is intentionally guarded by the command layer's stop/backup confirmations.
func RunSQLite(ctx context.Context, db *sql.DB, apply bool) (SQLiteReport, error) {
	if db == nil {
		return SQLiteReport{}, errors.New("J3b SQLite bootstrap 数据库未初始化")
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		return SQLiteReport{}, err
	}
	report, err := inspectSQLite(ctx, db)
	if err != nil {
		return SQLiteReport{}, err
	}
	if !apply || report.Ready() {
		return report, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SQLiteReport{}, fmt.Errorf("开始 J3b SQLite schema transaction 失败: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range sqliteSchemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return SQLiteReport{}, fmt.Errorf("执行 J3b SQLite schema bootstrap 失败: %w", err)
		}
	}
	if err := ensureSQLiteRunColumns(ctx, tx); err != nil {
		return SQLiteReport{}, fmt.Errorf("升级 J3b SQLite run schema 失败: %w", err)
	}
	if err := ensureSQLiteObservationColumns(ctx, tx); err != nil {
		return SQLiteReport{}, fmt.Errorf("升级 J3b SQLite observation schema 失败: %w", err)
	}
	if err := ensureSQLiteTrustAggregationColumns(ctx, tx); err != nil {
		return SQLiteReport{}, fmt.Errorf("升级 J3b SQLite trust aggregation schema 失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SQLiteReport{}, fmt.Errorf("提交 J3b SQLite schema bootstrap 失败: %w", err)
	}
	report, err = inspectSQLite(ctx, db)
	if err != nil {
		return SQLiteReport{}, err
	}
	if !report.Ready() {
		return SQLiteReport{}, errors.New("J3b SQLite schema bootstrap 后契约仍不完整")
	}
	report.Applied = true
	return report, nil
}

type SQLiteReport struct {
	MissingTables      []string `json:"missingTables"`
	MissingColumns     []string `json:"missingColumns"`
	InvalidPrimaryKeys []string `json:"invalidPrimaryKeys"`
	MissingIndexes     []string `json:"missingIndexes"`
	Applied            bool     `json:"applied"`
}

func (r SQLiteReport) Ready() bool {
	return len(r.MissingTables) == 0 && len(r.MissingColumns) == 0 && len(r.InvalidPrimaryKeys) == 0 && len(r.MissingIndexes) == 0
}

func inspectSQLite(ctx context.Context, db *sql.DB) (SQLiteReport, error) {
	report := SQLiteReport{}
	for _, table := range contracts.J3BModelCheckTables {
		var found string
		err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			report.MissingTables = append(report.MissingTables, table)
			continue
		}
		if err != nil {
			return SQLiteReport{}, err
		}
		rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return SQLiteReport{}, err
		}
		seen := map[string]bool{}
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				return SQLiteReport{}, err
			}
			seen[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return SQLiteReport{}, err
		}
		rows.Close()
		for _, column := range sqliteRequiredColumns[table] {
			if !seen[column] {
				report.MissingColumns = append(report.MissingColumns, table+"."+column)
			}
		}
		primaryKeys, err := sqlitePrimaryKeysDB(ctx, db, table)
		if err != nil {
			return SQLiteReport{}, err
		}
		if expected, ok := sqliteRequiredPrimaryKeys[table]; ok && !sameStringSlice(primaryKeys, expected) {
			report.InvalidPrimaryKeys = append(report.InvalidPrimaryKeys, table)
		}
	}
	sort.Strings(report.MissingTables)
	sort.Strings(report.MissingColumns)
	sort.Strings(report.InvalidPrimaryKeys)
	for table, indexes := range sqliteRequiredIndexes {
		found, err := sqliteIndexes(ctx, db, table)
		if err != nil {
			return SQLiteReport{}, err
		}
		for _, index := range indexes {
			if !found[index] {
				report.MissingIndexes = append(report.MissingIndexes, index)
			}
		}
	}
	sort.Strings(report.MissingIndexes)
	return report, nil
}

var sqliteRequiredPrimaryKeys = map[string][]string{
	"model_check_input_versions":              {"identity_key"},
	"model_check_inputs":                      {"input_id"},
	"model_check_execution_claims":            {"input_id"},
	"model_check_outcomes":                    {"outcome_id"},
	"model_check_runs":                        {"id"},
	"model_check_items":                       {"id"},
	"model_check_observations":                {"id"},
	"account_quality_health_hourly":           {"account_id", "stat_hour"},
	"model_check_scheduler_tasks":             {"id"},
	"model_token_intercept_baseline_versions": {"cohort_key_hmac", "requested_model", "tokenizer_version", "probe_set_version", "baseline_version"},
	"model_account_trust_results":             {"system_account_id", "account_id", "requested_model"},
	"model_trust_latest_dirty_accounts":       {"system_account_id", "account_id", "requested_model"},
	"model_trust_observation_receipts":        {"observation_id"},
	"model_trust_aggregation_state":           {"scope_key"},
}

var sqliteRequiredIndexes = map[string][]string{
	"model_check_scheduler_tasks":             {"idx_model_check_scheduler_tasks_due"},
	"model_check_runs":                        {"idx_model_check_runs_quality_health_sync_retry"},
	"model_token_intercept_baseline_versions": {"idx_model_token_intercept_baseline_active"},
	"model_account_trust_results":             {"idx_model_account_trust_results_updated"},
	"model_trust_latest_dirty_accounts":       {"idx_model_trust_latest_dirty_updated"},
	"model_trust_observation_receipts":        {"idx_model_trust_observation_receipts_processed"},
}

func sqliteIndexes(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA index_list("+quoteIdent(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexes := map[string]bool{}
	for rows.Next() {
		var seq, unique, partial int
		var origin string
		var name string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return nil, err
		}
		indexes[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return indexes, nil
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var sqliteRequiredColumns = map[string][]string{
	"model_check_input_versions":              {"identity_key", "next_version", "updated_at"},
	"model_check_inputs":                      {"input_id", "identity_key", "input_version", "input_digest", "target_id", "config_revision", "policy_revision", "trigger", "issued_at", "expires_at", "payload"},
	"model_check_execution_claims":            {"input_id", "claim_token", "outcome_id", "owner_id", "fence_token", "claim_until", "updated_at"},
	"model_check_outcomes":                    {"outcome_id", "input_id", "input_digest", "fence_token", "observed_at", "stored_at", "payload", "payload_digest", "committed"},
	"model_check_runs":                        {"id", "system_account_id", "actor_system_account_id", "provider_code", "target_type", "target_id", "target_name", "target_owner_system_account_id", "account_id", "group_id", "api_key_id", "model", "profile", "trigger_kind", "schedule_id", "trusted_comparison_enabled", "trusted_comparison_available", "status", "level", "score", "max_score", "message", "request_summary_json", "result_summary_json", "policy_snapshot_json", "quality_decision_json", "probe_set_version", "started_at", "trace_id", "quality_health_sync_status", "created_at", "updated_at", "finished_at", "duration_ms", "error_code", "error_message"},
	"model_check_items":                       {"id", "run_id", "item_key", "item_type", "status", "score", "max_score", "duration_ms", "trace_id", "evidence_summary_json", "error_code", "error_message", "created_at", "updated_at"},
	"model_check_observations":                {"id", "run_id", "system_account_id", "account_id", "provider_code", "provider_protocol_profile_id", "endpoint_family", "requested_model", "mapped_upstream_model", "observed_model", "mapping_applied", "upstream_bucket_hmac", "cohort_key_hmac", "population_key_hmac", "probe_key_hmac", "system_fingerprint_hmac", "probe_family", "probe_set_version", "tokenizer_version", "feature_version", "round_index", "padding_tokens", "local_input_tokens", "reported_input_tokens", "cached_input_tokens", "constraint_passed", "feature_1", "feature_2", "feature_3", "feature_4", "feature_5", "feature_6", "feature_7", "feature_8", "observation_status", "identity_status", "mapping_status", "protocol_status", "evidence_coverage", "trace_id", "created_at", "aggregation_completed_at"},
	"account_quality_health_hourly":           {"account_id", "system_account_id", "provider_code", "stat_hour", "observed_at", "model_check_run_id", "model", "profile", "score", "threshold", "level", "error_code", "error_message", "updated_at"},
	"model_check_scheduler_tasks":             {"id", "kind", "due_at", "claim_owner", "claim_until", "fence_token", "state", "last_error", "completed_at", "payload", "updated_at"},
	"model_token_intercept_baseline_versions": {"cohort_key_hmac", "requested_model", "tokenizer_version", "probe_set_version", "baseline_version", "version_status", "evidence_status", "independent_source_count", "retained_source_count", "excluded_source_count", "median_intercept", "mad_intercept", "q10_intercept", "q90_intercept", "strong_threshold_intercept", "strong_gate_enabled", "calibration_note", "first_observed_at", "last_observed_at", "updated_at"},
	"model_account_trust_results":             {"system_account_id", "account_id", "requested_model", "identity_status", "mapping_status", "usage_integrity_status", "protocol_status", "evidence_status", "evidence_coverage", "observation_count", "round_count", "independent_source_count", "identity_observation_count", "paired_probe_count", "slope", "intercept", "intercept_baseline_median", "intercept_baseline_mad", "intercept_baseline_version", "intercept_baseline_status", "intercept_strong_gate_enabled", "identity_distance", "paired_distance", "paired_baseline_median", "paired_baseline_mad", "baseline_version", "baseline_version_status", "feature_version", "tokenizer_version", "probe_set_version", "reason_codes_json", "last_observed_id", "last_observed_at", "updated_at"},
	"model_trust_latest_dirty_accounts":       {"system_account_id", "account_id", "requested_model", "dirty_reason", "updated_at"},
	"model_trust_observation_receipts":        {"observation_id", "observation_created_at", "processed_at"},
	"model_trust_aggregation_state":           {"scope_key", "cursor_created_at", "cursor_id", "last_success_at", "last_error_message", "lag_seconds", "updated_at"},
}

var sqliteSchemaStatements = []string{
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

// ensureSQLiteRunColumns is an explicit forward-only bootstrap migration for
// dedicated files created by an earlier J3b contract. Runtime never performs
// this DDL; the maintenance transaction either upgrades the complete durable
// run projection or rolls the entire bootstrap back.
func ensureSQLiteRunColumns(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(model_check_runs)")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, addition := range []struct{ name, statement string }{
		{"target_name", "ALTER TABLE model_check_runs ADD COLUMN target_name TEXT"},
		{"target_owner_system_account_id", "ALTER TABLE model_check_runs ADD COLUMN target_owner_system_account_id TEXT"},
		{"group_id", "ALTER TABLE model_check_runs ADD COLUMN group_id TEXT"},
		{"api_key_id", "ALTER TABLE model_check_runs ADD COLUMN api_key_id TEXT"},
		{"schedule_id", "ALTER TABLE model_check_runs ADD COLUMN schedule_id TEXT"},
		{"trusted_comparison_enabled", "ALTER TABLE model_check_runs ADD COLUMN trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0"},
		{"trusted_comparison_available", "ALTER TABLE model_check_runs ADD COLUMN trusted_comparison_available INTEGER NOT NULL DEFAULT 0"},
		{"probe_set_version", "ALTER TABLE model_check_runs ADD COLUMN probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1'"},
		{"started_at", "ALTER TABLE model_check_runs ADD COLUMN started_at TEXT"},
		{"trace_id", "ALTER TABLE model_check_runs ADD COLUMN trace_id TEXT"},
		{"duration_ms", "ALTER TABLE model_check_runs ADD COLUMN duration_ms INTEGER"},
		{"error_code", "ALTER TABLE model_check_runs ADD COLUMN error_code TEXT"},
		{"error_message", "ALTER TABLE model_check_runs ADD COLUMN error_message TEXT"},
	} {
		if !found[addition.name] {
			if _, err := tx.ExecContext(ctx, addition.statement); err != nil {
				return err
			}
		}
	}
	if !found["started_at"] {
		if _, err := tx.ExecContext(ctx, "UPDATE model_check_runs SET started_at=created_at WHERE started_at IS NULL"); err != nil {
			return err
		}
	}
	return nil
}

func ensureSQLiteObservationColumns(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(model_check_observations)")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, addition := range []struct{ name, statement string }{
		{"provider_protocol_profile_id", "ALTER TABLE model_check_observations ADD COLUMN provider_protocol_profile_id TEXT NOT NULL DEFAULT 'unknown'"},
		{"endpoint_family", "ALTER TABLE model_check_observations ADD COLUMN endpoint_family TEXT NOT NULL DEFAULT 'unknown'"},
		{"observed_model", "ALTER TABLE model_check_observations ADD COLUMN observed_model TEXT"},
		{"mapping_applied", "ALTER TABLE model_check_observations ADD COLUMN mapping_applied INTEGER NOT NULL DEFAULT 0"},
		{"upstream_bucket_hmac", "ALTER TABLE model_check_observations ADD COLUMN upstream_bucket_hmac TEXT NOT NULL DEFAULT ''"},
		{"cohort_key_hmac", "ALTER TABLE model_check_observations ADD COLUMN cohort_key_hmac TEXT NOT NULL DEFAULT ''"},
		{"population_key_hmac", "ALTER TABLE model_check_observations ADD COLUMN population_key_hmac TEXT NOT NULL DEFAULT ''"},
		{"probe_key_hmac", "ALTER TABLE model_check_observations ADD COLUMN probe_key_hmac TEXT NOT NULL DEFAULT ''"},
		{"system_fingerprint_hmac", "ALTER TABLE model_check_observations ADD COLUMN system_fingerprint_hmac TEXT"},
		{"probe_set_version", "ALTER TABLE model_check_observations ADD COLUMN probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1'"},
		{"tokenizer_version", "ALTER TABLE model_check_observations ADD COLUMN tokenizer_version TEXT NOT NULL DEFAULT 'unavailable'"},
		{"feature_version", "ALTER TABLE model_check_observations ADD COLUMN feature_version TEXT NOT NULL DEFAULT 'none'"},
		{"round_index", "ALTER TABLE model_check_observations ADD COLUMN round_index INTEGER NOT NULL DEFAULT 0"},
		{"padding_tokens", "ALTER TABLE model_check_observations ADD COLUMN padding_tokens INTEGER NOT NULL DEFAULT 0"},
		{"local_input_tokens", "ALTER TABLE model_check_observations ADD COLUMN local_input_tokens INTEGER NOT NULL DEFAULT 0"},
		{"reported_input_tokens", "ALTER TABLE model_check_observations ADD COLUMN reported_input_tokens INTEGER"},
		{"cached_input_tokens", "ALTER TABLE model_check_observations ADD COLUMN cached_input_tokens INTEGER"},
		{"constraint_passed", "ALTER TABLE model_check_observations ADD COLUMN constraint_passed INTEGER"},
		{"feature_1", "ALTER TABLE model_check_observations ADD COLUMN feature_1 REAL"},
		{"feature_2", "ALTER TABLE model_check_observations ADD COLUMN feature_2 REAL"},
		{"feature_3", "ALTER TABLE model_check_observations ADD COLUMN feature_3 REAL"},
		{"feature_4", "ALTER TABLE model_check_observations ADD COLUMN feature_4 REAL"},
		{"feature_5", "ALTER TABLE model_check_observations ADD COLUMN feature_5 REAL"},
		{"feature_6", "ALTER TABLE model_check_observations ADD COLUMN feature_6 REAL"},
		{"feature_7", "ALTER TABLE model_check_observations ADD COLUMN feature_7 REAL"},
		{"feature_8", "ALTER TABLE model_check_observations ADD COLUMN feature_8 REAL"},
		{"trace_id", "ALTER TABLE model_check_observations ADD COLUMN trace_id TEXT"},
		{"aggregation_completed_at", "ALTER TABLE model_check_observations ADD COLUMN aggregation_completed_at TEXT"},
	} {
		if !found[addition.name] {
			if _, err := tx.ExecContext(ctx, addition.statement); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureSQLiteTrustAggregationColumns preserves the scoped Node cursor's
// diagnostic state when an older dedicated J3b file is upgraded. Runtime
// never runs this DDL; it is only part of the explicit maintenance bootstrap.
func ensureSQLiteTrustAggregationColumns(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info(model_trust_aggregation_state)")
	if err != nil {
		return err
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, addition := range []struct{ name, statement string }{
		{"last_error_message", "ALTER TABLE model_trust_aggregation_state ADD COLUMN last_error_message TEXT"},
		{"lag_seconds", "ALTER TABLE model_trust_aggregation_state ADD COLUMN lag_seconds INTEGER"},
	} {
		if !found[addition.name] {
			if _, err := tx.ExecContext(ctx, addition.statement); err != nil {
				return err
			}
		}
	}
	return nil
}

func Run(ctx context.Context, db *sql.DB, apply bool) (Report, error) {
	if db == nil {
		return Report{}, errors.New("J3b bootstrap 数据库未初始化")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !apply})
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL statement_timeout = '30s'; SET LOCAL lock_timeout = '5s'; SET LOCAL idle_in_transaction_session_timeout = '30s'"); err != nil {
		return Report{}, err
	}
	if apply {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", bootstrapLock); err != nil {
			return Report{}, err
		}
	}
	report, err := inspectTx(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	if !apply || report.Ready() {
		if err := tx.Commit(); err != nil {
			return Report{}, err
		}
		return report, nil
	}
	if report.MissingSchema {
		return Report{}, errors.New("J3b bootstrap 拒绝创建 juhe_j3b schema；必须由受控数据库流程预置")
	}
	if report.SchemaOwner != report.CurrentRole {
		return Report{}, fmt.Errorf("J3b bootstrap 拒绝跨角色修改 juhe_j3b schema: owner=%s current=%s", report.SchemaOwner, report.CurrentRole)
	}
	if _, err := tx.ExecContext(ctx, postgresSchema); err != nil {
		return Report{}, fmt.Errorf("执行 J3b PostgreSQL juhe_j3b schema bootstrap 失败: %w", err)
	}
	report, err = inspectTx(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	if !report.Ready() {
		return Report{}, fmt.Errorf("J3b PostgreSQL juhe_j3b schema bootstrap 后契约仍不完整: missingSchema=%t ownerMismatch=%t missingTables=%v invalidTables=%v missingIndexes=%v invalidIndexes=%v", report.MissingSchema, report.OwnerMismatch, report.MissingTables, report.InvalidTables, report.MissingIndexes, report.InvalidIndexes)
	}
	report.Applied = true
	if err := tx.Commit(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func inspectTx(ctx context.Context, tx *sql.Tx) (Report, error) {
	report := Report{Schema: SchemaName}
	if err := tx.QueryRowContext(ctx, "SELECT current_database(), current_user").Scan(&report.Database, &report.CurrentRole); err != nil {
		return Report{}, err
	}
	var owner sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=$1", SchemaName).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		report.MissingSchema = true
		return report, nil
	}
	if err != nil {
		return Report{}, err
	}
	report.SchemaOwner, report.OwnerMismatch = owner.String, owner.String != report.CurrentRole
	rows, err := tx.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema=$1 AND table_name = ANY($2)", SchemaName, contracts.J3BModelCheckTables)
	if err != nil {
		return Report{}, err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return Report{}, err
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Report{}, err
	}
	rows.Close()
	for _, name := range contracts.J3BModelCheckTables {
		if !seen[name] {
			report.MissingTables = append(report.MissingTables, name)
		}
	}
	if len(report.MissingTables) == 0 {
		if err := inspectColumns(ctx, tx, &report); err != nil {
			return Report{}, err
		}
		if err := inspectConstraints(ctx, tx, &report); err != nil {
			return Report{}, err
		}
	}
	indexRows, err := tx.QueryContext(ctx, "SELECT indexname,indexdef FROM pg_indexes WHERE schemaname=$1 AND indexname = ANY($2)", SchemaName, requiredIndexNames())
	if err != nil {
		return Report{}, err
	}
	indexes := map[string]string{}
	for indexRows.Next() {
		var name, definition string
		if err := indexRows.Scan(&name, &definition); err != nil {
			indexRows.Close()
			return Report{}, err
		}
		indexes[name] = strings.ToLower(strings.Join(strings.Fields(definition), " "))
	}
	if err := indexRows.Err(); err != nil {
		indexRows.Close()
		return Report{}, err
	}
	indexRows.Close()
	for name, expected := range contracts.J3BModelCheckIndexes {
		actual, ok := indexes[name]
		if !ok {
			report.MissingIndexes = append(report.MissingIndexes, name)
			continue
		}
		if !strings.Contains(actual, expected) {
			report.InvalidIndexes = append(report.InvalidIndexes, name)
		}
	}
	sort.Strings(report.MissingTables)
	sort.Strings(report.InvalidTables)
	sort.Strings(report.MissingIndexes)
	sort.Strings(report.InvalidIndexes)
	return report, nil
}

func inspectColumns(ctx context.Context, tx *sql.Tx, report *Report) error {
	rows, err := tx.QueryContext(ctx, `SELECT table_name,column_name,data_type,udt_name,is_nullable FROM information_schema.columns WHERE table_schema=$1 AND table_name = ANY($2)`, SchemaName, contracts.J3BModelCheckTables)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]map[string]contracts.PostgresColumnSpec{}
	for rows.Next() {
		var table, column, dataType, udtName, nullable string
		if err := rows.Scan(&table, &column, &dataType, &udtName, &nullable); err != nil {
			return err
		}
		if seen[table] == nil {
			seen[table] = map[string]contracts.PostgresColumnSpec{}
		}
		seen[table][column] = contracts.PostgresColumnSpec{DataType: dataType, UdtName: udtName, Nullable: nullable == "YES"}
	}
	for table, columns := range contracts.J3BModelCheckColumns {
		for column, expected := range columns {
			actual, ok := seen[table][column]
			if !ok || actual.DataType != expected.DataType || actual.UdtName != expected.UdtName || actual.Nullable != expected.Nullable {
				report.InvalidTables = append(report.InvalidTables, table+"."+column)
			}
		}
	}
	return rows.Err()
}

func inspectConstraints(ctx context.Context, tx *sql.Tx, report *Report) error {
	rows, err := tx.QueryContext(ctx, `SELECT relation.relname,pg_get_constraintdef(c.oid) FROM pg_constraint AS c JOIN pg_class AS relation ON relation.oid=c.conrelid JOIN pg_namespace AS namespace ON namespace.oid=relation.relnamespace WHERE namespace.nspname=$1 AND relation.relname = ANY($2) AND c.contype IN ('p','u')`, SchemaName, contracts.J3BModelCheckTables)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string][]string{}
	for rows.Next() {
		var table, definition string
		if err := rows.Scan(&table, &definition); err != nil {
			return err
		}
		seen[table] = append(seen[table], strings.ToLower(strings.Join(strings.Fields(definition), " ")))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for table, expectedList := range contracts.J3BModelCheckConstraints {
		for _, expected := range expectedList {
			found := false
			for _, actual := range seen[table] {
				if strings.Contains(actual, expected) {
					found = true
					break
				}
			}
			if !found {
				report.InvalidTables = append(report.InvalidTables, table+":constraint="+expected)
			}
		}
	}
	return nil
}

func requiredIndexNames() []string {
	names := make([]string, 0, len(contracts.J3BModelCheckIndexes))
	for name := range contracts.J3BModelCheckIndexes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const postgresSchema = `
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_input_versions (identity_key TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_inputs (input_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL, input_version BIGINT NOT NULL, input_digest TEXT NOT NULL, target_id TEXT NOT NULL, config_revision TEXT NOT NULL, policy_revision TEXT NOT NULL, trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, UNIQUE(identity_key,input_version), UNIQUE(identity_key,input_digest));
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_execution_claims (input_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_outcomes (outcome_id TEXT PRIMARY KEY, input_id TEXT NOT NULL UNIQUE, input_digest TEXT NOT NULL, fence_token BIGINT NOT NULL, observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL, committed BOOLEAN NOT NULL DEFAULT FALSE);
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL DEFAULT 'quick', trigger_kind TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_kind IN ('manual','scheduled','quality_recovery')), schedule_id TEXT, trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0, trusted_comparison_available INTEGER NOT NULL DEFAULT 0, level TEXT NOT NULL DEFAULT 'unavailable', score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 100, status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed','canceled')), message TEXT NOT NULL DEFAULT '', trace_id TEXT, probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1', started_at TEXT NOT NULL, finished_at TEXT, duration_ms INTEGER, request_summary_json TEXT NOT NULL DEFAULT '{}', result_summary_json TEXT NOT NULL DEFAULT '{}', policy_snapshot_json TEXT NOT NULL DEFAULT '{}', quality_decision_json TEXT NOT NULL DEFAULT '{}', quality_health_sync_status TEXT CHECK (quality_health_sync_status IS NULL OR quality_health_sync_status IN ('applied','pending_retry','failed')), error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS schedule_id TEXT;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS target_name TEXT;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS target_owner_system_account_id TEXT;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS group_id TEXT;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS api_key_id TEXT;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS trusted_comparison_available INTEGER NOT NULL DEFAULT 0;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1';
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS started_at TEXT;
UPDATE juhe_j3b.model_check_runs SET started_at=created_at WHERE started_at IS NULL;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS trace_id TEXT;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS duration_ms INTEGER;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS error_code TEXT;
ALTER TABLE juhe_j3b.model_check_runs ADD COLUMN IF NOT EXISTS error_message TEXT;
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_items (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES juhe_j3b.model_check_runs(id) ON DELETE CASCADE, item_key TEXT NOT NULL, item_type TEXT NOT NULL, status TEXT NOT NULL CHECK (status IN ('passed','warning','failed','skipped')), score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 0, duration_ms INTEGER, trace_id TEXT, evidence_summary_json TEXT NOT NULL DEFAULT '{}', error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_observations (id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES juhe_j3b.model_check_runs(id) ON DELETE CASCADE, system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, provider_code TEXT NOT NULL, provider_protocol_profile_id TEXT NOT NULL DEFAULT 'unknown', endpoint_family TEXT NOT NULL DEFAULT 'unknown', requested_model TEXT NOT NULL, mapped_upstream_model TEXT NOT NULL, observed_model TEXT, mapping_applied INTEGER NOT NULL DEFAULT 0, upstream_bucket_hmac TEXT NOT NULL DEFAULT '', cohort_key_hmac TEXT NOT NULL DEFAULT '', population_key_hmac TEXT NOT NULL DEFAULT '', probe_key_hmac TEXT NOT NULL DEFAULT '', system_fingerprint_hmac TEXT, probe_family TEXT NOT NULL, probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1', tokenizer_version TEXT NOT NULL DEFAULT 'unavailable', feature_version TEXT NOT NULL DEFAULT 'none', round_index INTEGER NOT NULL DEFAULT 0, padding_tokens INTEGER NOT NULL DEFAULT 0, local_input_tokens INTEGER NOT NULL DEFAULT 0, reported_input_tokens INTEGER, cached_input_tokens INTEGER, constraint_passed INTEGER, feature_1 DOUBLE PRECISION, feature_2 DOUBLE PRECISION, feature_3 DOUBLE PRECISION, feature_4 DOUBLE PRECISION, feature_5 DOUBLE PRECISION, feature_6 DOUBLE PRECISION, feature_7 DOUBLE PRECISION, feature_8 DOUBLE PRECISION, observation_status TEXT NOT NULL, identity_status TEXT NOT NULL, mapping_status TEXT NOT NULL, protocol_status TEXT NOT NULL, evidence_coverage INTEGER NOT NULL DEFAULT 0, trace_id TEXT, created_at TEXT NOT NULL, aggregation_completed_at TEXT);
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN provider_protocol_profile_id SET DEFAULT 'unknown';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN endpoint_family SET DEFAULT 'unknown';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN upstream_bucket_hmac SET DEFAULT '';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN cohort_key_hmac SET DEFAULT '';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN population_key_hmac SET DEFAULT '';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN probe_key_hmac SET DEFAULT '';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN probe_set_version SET DEFAULT 'openai-model-check-v1';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN tokenizer_version SET DEFAULT 'unavailable';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN feature_version SET DEFAULT 'none';
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN round_index SET DEFAULT 0;
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN padding_tokens SET DEFAULT 0;
ALTER TABLE juhe_j3b.model_check_observations ALTER COLUMN local_input_tokens SET DEFAULT 0;
ALTER TABLE juhe_j3b.model_check_observations ADD COLUMN IF NOT EXISTS aggregation_completed_at TEXT;
CREATE TABLE IF NOT EXISTS juhe_j3b.account_quality_health_hourly (account_id TEXT NOT NULL, system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, stat_hour TEXT NOT NULL, observed_at TEXT NOT NULL, model_check_run_id TEXT NOT NULL, model TEXT NOT NULL, profile TEXT NOT NULL CHECK (profile IN ('quick','full')), score INTEGER NOT NULL, threshold INTEGER NOT NULL CHECK (threshold BETWEEN 40 AND 100), level TEXT NOT NULL, error_code TEXT, error_message TEXT, updated_at TEXT NOT NULL, PRIMARY KEY (account_id, stat_hour));
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_scheduler_tasks (id TEXT PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('scheduled','quality_recovery','health_sync_retry')), due_at TIMESTAMPTZ NOT NULL, claim_owner TEXT, claim_until TIMESTAMPTZ, fence_token BIGINT NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','failed','completed')), last_error TEXT, completed_at TIMESTAMPTZ, payload JSONB NOT NULL DEFAULT '{}'::jsonb, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_j3b.model_token_intercept_baseline_versions (cohort_key_hmac TEXT NOT NULL, requested_model TEXT NOT NULL, tokenizer_version TEXT NOT NULL, probe_set_version TEXT NOT NULL, baseline_version INTEGER NOT NULL, version_status TEXT NOT NULL DEFAULT 'calibration_pending', evidence_status TEXT NOT NULL DEFAULT 'insufficient', independent_source_count INTEGER NOT NULL DEFAULT 0, retained_source_count INTEGER NOT NULL DEFAULT 0, excluded_source_count INTEGER NOT NULL DEFAULT 0, median_intercept DOUBLE PRECISION, mad_intercept DOUBLE PRECISION, q10_intercept DOUBLE PRECISION, q90_intercept DOUBLE PRECISION, strong_threshold_intercept DOUBLE PRECISION, strong_gate_enabled INTEGER NOT NULL DEFAULT 0, calibration_note TEXT, first_observed_at TEXT NOT NULL, last_observed_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, baseline_version));
CREATE TABLE IF NOT EXISTS juhe_j3b.model_account_trust_results (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, identity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', mapping_status TEXT NOT NULL DEFAULT 'unknown', usage_integrity_status TEXT NOT NULL DEFAULT 'insufficient_evidence', protocol_status TEXT NOT NULL DEFAULT 'insufficient_evidence', evidence_status TEXT NOT NULL DEFAULT 'insufficient', evidence_coverage INTEGER NOT NULL DEFAULT 0, observation_count INTEGER NOT NULL DEFAULT 0, round_count INTEGER NOT NULL DEFAULT 0, independent_source_count INTEGER NOT NULL DEFAULT 0, identity_observation_count INTEGER NOT NULL DEFAULT 0, paired_probe_count INTEGER NOT NULL DEFAULT 0, slope DOUBLE PRECISION, intercept DOUBLE PRECISION, intercept_baseline_median DOUBLE PRECISION, intercept_baseline_mad DOUBLE PRECISION, intercept_baseline_version INTEGER, intercept_baseline_status TEXT, intercept_strong_gate_enabled INTEGER NOT NULL DEFAULT 0, identity_distance DOUBLE PRECISION, paired_distance DOUBLE PRECISION, paired_baseline_median DOUBLE PRECISION, paired_baseline_mad DOUBLE PRECISION, baseline_version INTEGER, baseline_version_status TEXT, feature_version TEXT, tokenizer_version TEXT, probe_set_version TEXT, reason_codes_json TEXT NOT NULL DEFAULT '[]', last_observed_id TEXT, last_observed_at TEXT, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, account_id, requested_model));
ALTER TABLE juhe_j3b.model_account_trust_results ADD COLUMN IF NOT EXISTS last_observed_id TEXT;
CREATE TABLE IF NOT EXISTS juhe_j3b.model_trust_latest_dirty_accounts (system_account_id TEXT NOT NULL, account_id TEXT NOT NULL, requested_model TEXT NOT NULL, dirty_reason TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (system_account_id, account_id, requested_model));
CREATE TABLE IF NOT EXISTS juhe_j3b.model_trust_observation_receipts (observation_id TEXT PRIMARY KEY, observation_created_at TEXT NOT NULL, processed_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS juhe_j3b.model_trust_aggregation_state (scope_key TEXT PRIMARY KEY, cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, last_error_message TEXT, lag_seconds INTEGER, updated_at TEXT NOT NULL);
ALTER TABLE juhe_j3b.model_trust_aggregation_state ADD COLUMN IF NOT EXISTS last_error_message TEXT;
ALTER TABLE juhe_j3b.model_trust_aggregation_state ADD COLUMN IF NOT EXISTS lag_seconds INTEGER;
CREATE INDEX IF NOT EXISTS idx_model_check_scheduler_tasks_due ON juhe_j3b.model_check_scheduler_tasks(kind,due_at,claim_until,id);
CREATE INDEX IF NOT EXISTS idx_model_token_intercept_baseline_active ON juhe_j3b.model_token_intercept_baseline_versions(cohort_key_hmac,requested_model,tokenizer_version,probe_set_version,version_status,baseline_version);
CREATE INDEX IF NOT EXISTS idx_model_account_trust_results_updated ON juhe_j3b.model_account_trust_results(updated_at,account_id,requested_model);
CREATE INDEX IF NOT EXISTS idx_model_trust_latest_dirty_updated ON juhe_j3b.model_trust_latest_dirty_accounts(updated_at,system_account_id,account_id,requested_model);
CREATE INDEX IF NOT EXISTS idx_model_trust_observation_receipts_processed ON juhe_j3b.model_trust_observation_receipts(processed_at,observation_id);
CREATE INDEX IF NOT EXISTS idx_model_check_outcomes_cursor ON juhe_j3b.model_check_outcomes(stored_at,outcome_id);
CREATE INDEX IF NOT EXISTS idx_model_check_inputs_target ON juhe_j3b.model_check_inputs(target_id,issued_at);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_created ON juhe_j3b.model_check_runs(created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS idx_model_check_runs_quality_health_sync_retry ON juhe_j3b.model_check_runs(quality_health_sync_status,updated_at,id) WHERE quality_health_sync_status='failed';
CREATE INDEX IF NOT EXISTS idx_model_check_items_run_order ON juhe_j3b.model_check_items(run_id,created_at,id);
CREATE INDEX IF NOT EXISTS idx_model_check_items_run_key ON juhe_j3b.model_check_items(run_id,item_key,id);
CREATE INDEX IF NOT EXISTS idx_model_check_observations_cursor ON juhe_j3b.model_check_observations(created_at,id);
CREATE INDEX IF NOT EXISTS idx_model_check_observations_pending_aggregation ON juhe_j3b.model_check_observations(created_at,id) WHERE aggregation_completed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_account_quality_health_hourly_scope ON juhe_j3b.account_quality_health_hourly(system_account_id,stat_hour,account_id);
`
