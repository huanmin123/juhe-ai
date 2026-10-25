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
)

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
	if !apply {
		if err := tx.Commit(); err != nil {
			return Report{}, err
		}
		return report, nil
	}
	// Ready 的存量库不会执行 postgresSchema（下方短路），而 trigger_kind 的
	// CHECK 收窄是唯一无法通过列/索引检查发现的漂移，因此迁移块独立于
	// postgresSchema，在 apply 且通过 schema/owner 门禁后始终执行。块本身
	// 幂等：表不存在或约束已含 schedule_now 时不写任何内容。
	if report.Ready() {
		if err := migrateTriggerKindCheck(ctx, tx); err != nil {
			return Report{}, err
		}
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
	if err := migrateTriggerKindCheck(ctx, tx); err != nil {
		return Report{}, err
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

// migrateTriggerKindCheck executes postgresTriggerKindCheckMigration inside
// the caller's transaction. Callers must already hold the apply-side schema
// and owner gates (or a Ready report, which implies them).
func migrateTriggerKindCheck(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, postgresTriggerKindCheckMigration); err != nil {
		return fmt.Errorf("执行 J3b model_check_runs trigger_kind CHECK 迁移失败: %w", err)
	}
	return nil
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
CREATE TABLE IF NOT EXISTS juhe_j3b.model_check_runs (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, actor_system_account_id TEXT NOT NULL, provider_code TEXT NOT NULL, target_type TEXT NOT NULL, target_id TEXT NOT NULL, target_name TEXT, target_owner_system_account_id TEXT, account_id TEXT, group_id TEXT, api_key_id TEXT, model TEXT NOT NULL, profile TEXT NOT NULL DEFAULT 'quick', trigger_kind TEXT NOT NULL DEFAULT 'manual' CHECK (trigger_kind IN ('manual','scheduled','quality_recovery','schedule_now')), schedule_id TEXT, trusted_comparison_enabled INTEGER NOT NULL DEFAULT 0, trusted_comparison_available INTEGER NOT NULL DEFAULT 0, level TEXT NOT NULL DEFAULT 'unavailable', score INTEGER NOT NULL DEFAULT 0, max_score INTEGER NOT NULL DEFAULT 100, status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','completed','failed','canceled')), message TEXT NOT NULL DEFAULT '', trace_id TEXT, probe_set_version TEXT NOT NULL DEFAULT 'openai-model-check-v1', started_at TEXT NOT NULL, finished_at TEXT, duration_ms INTEGER, request_summary_json TEXT NOT NULL DEFAULT '{}', result_summary_json TEXT NOT NULL DEFAULT '{}', policy_snapshot_json TEXT NOT NULL DEFAULT '{}', quality_decision_json TEXT NOT NULL DEFAULT '{}', quality_health_sync_status TEXT CHECK (quality_health_sync_status IS NULL OR quality_health_sync_status IN ('applied','pending_retry','failed')), error_code TEXT, error_message TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
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

// postgresTriggerKindCheckMigration widens the durable model_check_runs
// trigger_kind CHECK from the three-value contract to the four-value contract
// ('schedule_now' added for on-demand schedule execution). It lives outside
// postgresSchema on purpose: ready databases skip the bootstrap DDL entirely,
// so the migration is executed by Run on every apply. The block is idempotent:
// it only replaces a trigger_kind constraint whose normalized definition still
// lacks 'schedule_now' (pg_get_constraintdef renders the inline CHECK as
// (trigger_kind = ANY (ARRAY[...])), and the conkey guard pins the constraint
// to the trigger_kind column), then adds the same constraint name fresh
// databases get from the inline CHECK. Empty schemas (table absent) exit
// before writing anything.
const postgresTriggerKindCheckMigration = `DO $$
DECLARE
  legacy_constraint text;
BEGIN
  IF to_regclass('juhe_j3b.model_check_runs') IS NOT NULL THEN
    SELECT c.conname INTO legacy_constraint
    FROM pg_constraint AS c
    JOIN pg_class AS relation ON relation.oid = c.conrelid
    JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
    WHERE namespace.nspname = 'juhe_j3b'
      AND relation.relname = 'model_check_runs'
      AND c.contype = 'c'
      AND pg_get_constraintdef(c.oid) LIKE '%trigger_kind%'
      AND pg_get_constraintdef(c.oid) NOT LIKE '%schedule_now%'
      AND EXISTS (
        SELECT 1 FROM pg_attribute AS a
        WHERE a.attrelid = c.conrelid
          AND a.attname = 'trigger_kind'
          AND a.attnum = ANY(c.conkey)
      );
    IF legacy_constraint IS NOT NULL THEN
      EXECUTE format('ALTER TABLE juhe_j3b.model_check_runs DROP CONSTRAINT %I', legacy_constraint);
    END IF;
    IF NOT EXISTS (
      SELECT 1 FROM pg_constraint AS c
      JOIN pg_class AS relation ON relation.oid = c.conrelid
      JOIN pg_namespace AS namespace ON namespace.oid = relation.relnamespace
      WHERE namespace.nspname = 'juhe_j3b'
        AND relation.relname = 'model_check_runs'
        AND c.conname = 'model_check_runs_trigger_kind_check'
    ) THEN
      EXECUTE 'ALTER TABLE juhe_j3b.model_check_runs ADD CONSTRAINT model_check_runs_trigger_kind_check CHECK (trigger_kind IN (''manual'',''scheduled'',''quality_recovery'',''schedule_now''))';
    END IF;
  END IF;
END
$$`
