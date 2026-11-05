// 九家真实厂商收口清退命令的 fresh PG 集成测试：账户配置/运行态/测试草稿/
// media/历史 usage/audit/stats 一并删除，真实厂商账户与历史行保留；manifest
// 冻结与幂等重跑；清退后可加回 vendor/driver CHECK。
package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/schema"
	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

func TestPurgeUnsupportedVendorAccounts(t *testing.T) {
	pgtestkit.SkipUnlessExternalAuthorized(t)
	td := pgtestkit.NewTestDatabase(t)
	ctx := context.Background()
	if _, err := schema.EnsurePostgres(ctx, td.DB); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	// 模拟 revision 16 存量库：清退命令执行前 CHECK 尚未添加。
	dropVendorDriverCheckConstraints(t, td.DB)
	seedPurgeFixtures(t, td.DB)

	manifestPath := filepath.Join(t.TempDir(), "purge-manifest.json")
	gate := purgeUnsupportedVendorGate{
		DSN:         td.DSN,
		DryRun:      true,
		ManifestOut: manifestPath,
		BatchSize:   1,
	}
	dryReport, err := runPurgeUnsupportedVendorAccounts(ctx, td.DB, gate, func(string, ...any) {})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dryReport.AccountCount != 2 {
		t.Fatalf("dry-run 目标账户 = %d, want 2（generic + 授权实例）", dryReport.AccountCount)
	}
	if dryReport.RealAccountCount != 1 {
		t.Fatalf("dry-run 真实厂商账户 = %d, want 1", dryReport.RealAccountCount)
	}
	manifest, err := readPurgeManifest(manifestPath)
	if err != nil {
		t.Fatalf("读取 manifest: %v", err)
	}
	if manifest.AccountCount != 2 || !strings.Contains(strings.Join(manifest.AccountIDs, ","), "acc-generic") {
		t.Fatalf("manifest 内容不符: %+v", manifest)
	}
	if !purgeCountPositive(dryReport, "juhe_usage.usage_records") {
		t.Fatalf("dry-run 应报告 usage_records 命中: %+v", dryReport.Tables)
	}
	if !purgeCountPositive(dryReport, "juhe_dataset.audit_logs") {
		t.Fatalf("dry-run 应报告 audit_logs 命中: %+v", dryReport.Tables)
	}

	applyGate := gate
	applyGate.DryRun = false
	applyGate.StopWriteConfirmed = true
	applyGate.BackupConfirmed = true
	applyReport, err := runPurgeUnsupportedVendorAccounts(ctx, td.DB, applyGate, func(string, ...any) {})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applyReport.PostCheckAccounts != 0 {
		t.Fatalf("清退后不支持账户 = %d, want 0", applyReport.PostCheckAccounts)
	}

	// 真实账户与真实历史保留；generic 账户、连接、模式、支持模型、历史全部删除。
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.accounts WHERE id = 'acc-real'`, 1)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.accounts WHERE id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.accounts WHERE id = 'acc-instance'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.account_connections WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.account_endpoint_modes WHERE connection_id = 'conn-acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.account_supported_models WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_usage.usage_records WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_usage.usage_records WHERE account_id = 'acc-real'`, 1)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_dataset.audit_logs WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_dataset.audit_logs WHERE account_id = 'acc-real'`, 1)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_stats.account_quality_scores WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_stats.account_quality_scores WHERE account_id = 'acc-real'`, 1)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_stats.usage_stats_totals WHERE scope_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_jobs.account_health_current_state WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_jobs.account_balance_snapshots WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.account_test_tasks WHERE account_id = 'acc-generic'`, 0)
	assertRowCount(t, td.DB, `SELECT count(*) FROM juhe_business.media_jobs WHERE account_id = 'acc-generic'`, 0)

	// 幂等重跑：live 集合为空时按 manifest 做 no-op 删除。
	replayReport, err := runPurgeUnsupportedVendorAccounts(ctx, td.DB, applyGate, func(string, ...any) {})
	if err != nil {
		t.Fatalf("幂等重跑: %v", err)
	}
	if replayReport.PostCheckAccounts != 0 {
		t.Fatalf("幂等重跑后不支持账户 = %d, want 0", replayReport.PostCheckAccounts)
	}

	// 清退后加回 CHECK：应成功，且 NULL vendor 账户被数据库拒绝。
	if _, err := td.DB.ExecContext(ctx, `ALTER TABLE juhe_business.accounts
		ADD CONSTRAINT accounts_vendor_driver_consistency_check
		CHECK (vendor_code IS NOT NULL AND connection_driver_code = vendor_code)`); err != nil {
		t.Fatalf("清退后加回 CHECK: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := td.DB.ExecContext(ctx, `INSERT INTO juhe_business.accounts
		(id, system_account_id, connection_driver_code, protocol_code, protocol_version, name, type,
		 status, schedulable, credentials_encrypted, created_at, updated_at)
		VALUES ('acc-null-vendor', 'sys-purge', 'generic_http', 'openai', 'v1', 'null-vendor', 'api_key',
		        'active', 1, 'sealed', $1, $1)`, now); err == nil {
		t.Fatal("CHECK 加回后 NULL vendor 账户仍可插入")
	}
}

func dropVendorDriverCheckConstraints(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DO $$
DECLARE c record;
BEGIN
  FOR c IN SELECT conname FROM pg_constraint
    WHERE conrelid = 'juhe_business.accounts'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) LIKE '%vendor_code IS NOT NULL%'
  LOOP
    EXECUTE format('ALTER TABLE juhe_business.accounts DROP CONSTRAINT %I', c.conname);
  END LOOP;
END $$`); err != nil {
		t.Fatalf("drop vendor/driver CHECK: %v", err)
	}
}

func seedPurgeFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO juhe_business.system_accounts
			(id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES ('sys-purge', 'sys-purge', 'purge', 'user', 'active', 'hash', $1, $1)`,
		`INSERT INTO juhe_business.vendors
			(id, code, name, default_supported_models_json, enabled, created_at, updated_at)
			VALUES ('vendor-gpt', 'gpt', 'GPT', '[]', 1, $1, $1)`,
		`INSERT INTO juhe_business.models
			(id, vendor_code, public_model, upstream_model, scope, status, catalog_visible,
			 supported_endpoint_modes_json, supported_service_tiers_json, supported_reasoning_efforts_json,
			 codex_supported_reasoning_levels_json, service_tier_prices_json,
			 long_context_input_token_threshold_inclusive, supports_prompt_caching, currency, created_at, updated_at)
			VALUES ('mdl-gpt', 'gpt', 'gpt-test', 'gpt-test', 'builtin', 'active', true,
			        '["chat_json"]', '[]', '[]', '[]', '{}', false, false, 'USD', $1, $1)`,
		`INSERT INTO juhe_business.accounts
			(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version, name, type,
			 status, schedulable, credentials_encrypted, created_at, updated_at)
			VALUES ('acc-real', 'sys-purge', 'gpt', 'gpt', 'openai', 'v1', 'real', 'api_key', 'active', 1, 'sealed', $1, $1)`,
		`INSERT INTO juhe_business.accounts
			(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version, name, type,
			 status, schedulable, credentials_encrypted, created_at, updated_at)
			VALUES ('acc-generic', 'sys-purge', NULL, 'generic_http', 'openai', 'v1', 'generic', 'api_key', 'active', 1, 'sealed', $1, $1)`,
		`INSERT INTO juhe_business.accounts
			(id, system_account_id, vendor_code, connection_driver_code, protocol_code, protocol_version, name, type,
			 status, schedulable, credentials_encrypted, authorization_instance_source_account_id, created_at, updated_at)
			VALUES ('acc-instance', 'sys-purge', NULL, 'generic_http', 'openai', 'v1', 'instance', 'api_key', 'active', 1, 'sealed', 'acc-generic', $1, $1)`,
		`INSERT INTO juhe_business.account_connections
			(id, account_id, connection_preset_code, protocol_surface_code, base_url, enabled, created_at, updated_at)
			VALUES ('conn-acc-generic', 'acc-generic', 'generic_openai_v1', 'openai_v1', 'https://relay.example/v1', 1, $1, $1)`,
		`INSERT INTO juhe_business.account_endpoint_modes (connection_id, endpoint_mode)
			VALUES ('conn-acc-generic', 'chat_json')`,
		`INSERT INTO juhe_business.account_supported_models (account_id, model_id, created_at)
			VALUES ('acc-real', 'mdl-gpt', $1), ('acc-generic', 'mdl-gpt', $1)`,
		`INSERT INTO juhe_business.account_test_tasks
			(id, account_id, account_name, protocol_code, protocol_version, account_type,
			 request_system_account_id, request_role, queued_at, created_at, updated_at)
			VALUES ('task-generic', 'acc-generic', 'generic', 'openai', 'v1', 'api_key',
			        'sys-purge', 'user', $1, $1, $1)`,
		`INSERT INTO juhe_business.media_jobs
			(id, kind, api_key_id, account_id, upstream_job_id, status, created_at, updated_at)
			VALUES ('media-generic', 'video', 'key-x', 'acc-generic', 'up-x', 'queued', $1, $1)`,
		`CREATE TABLE IF NOT EXISTS juhe_usage.usage_records_test_2026
			PARTITION OF juhe_usage.usage_records FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')`,
		`INSERT INTO juhe_usage.usage_records
			(id, system_account_id, trace_id, traffic_source, account_id, provider_code, created_at)
			VALUES ('usage-generic', 'sys-purge', 'trace-generic', 'gateway', 'acc-generic', 'openai', $1),
			       ('usage-real', 'sys-purge', 'trace-real', 'gateway', 'acc-real', 'gpt', $1)`,
		`INSERT INTO juhe_stats.account_quality_scores
			(account_id, system_account_id, provider_code, window_started_at, window_ended_at, updated_at)
			VALUES ('acc-generic', 'sys-purge', 'openai', $1, $1, $1),
			       ('acc-real', 'sys-purge', 'gpt', $1, $1, $1)`,
		`INSERT INTO juhe_stats.usage_stats_totals
			(system_account_id, scope_type, scope_id, updated_at)
			VALUES ('sys-purge', 'account', 'acc-generic', $1),
			       ('sys-purge', 'account', 'acc-real', $1)`,
		`CREATE SCHEMA IF NOT EXISTS juhe_dataset`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.audit_logs
			(id text PRIMARY KEY, account_id text, provider_code text, started_at timestamptz NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.audit_log_attempts
			(id text PRIMARY KEY, audit_log_id text NOT NULL, account_id text)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.audit_error_groups
			(id text PRIMARY KEY, account_id text, provider_code text)`,
		`CREATE TABLE IF NOT EXISTS juhe_dataset.account_record_cleanup_targets (account_id text PRIMARY KEY)`,
		`INSERT INTO juhe_dataset.audit_logs (id, account_id, provider_code, started_at)
			VALUES ('audit-generic', 'acc-generic', 'openai', $1), ('audit-real', 'acc-real', 'gpt', $1)`,
		`INSERT INTO juhe_dataset.audit_log_attempts (id, audit_log_id, account_id)
			VALUES ('attempt-generic', 'audit-generic', 'acc-generic')`,
		`CREATE SCHEMA IF NOT EXISTS juhe_jobs`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.account_health_current_state
			(account_id text PRIMARY KEY, outcome_id text, outcome text, observed_at timestamptz,
			 input_version bigint, config_revision bigint, dispatch_revision bigint, updated_at timestamptz)`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.account_balance_snapshots
			(account_id text PRIMARY KEY, input_version bigint, config_revision bigint, trigger text,
			 snapshot_json jsonb, updated_at timestamptz)`,
		`INSERT INTO juhe_jobs.account_health_current_state
			(account_id, outcome_id, outcome, observed_at, input_version, config_revision, dispatch_revision, updated_at)
			VALUES ('acc-generic', 'o1', 'success', $1, 1, 1, 1, $1)`,
		`INSERT INTO juhe_jobs.account_balance_snapshots
			(account_id, input_version, config_revision, trigger, snapshot_json, updated_at)
			VALUES ('acc-generic', 1, 1, 'manual', '{}'::jsonb, $1)`,
	}
	for _, statement := range statements {
		var err error
		if strings.Contains(statement, "$1") {
			_, err = db.Exec(statement, now)
		} else {
			_, err = db.Exec(statement)
		}
		if err != nil {
			t.Fatalf("seed fixture: %v\nSQL: %s", err, statement)
		}
	}
}

func assertRowCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	if got != want {
		t.Fatalf("query %q = %d, want %d", query, got, want)
	}
}

func purgeCountPositive(report purgeUnsupportedVendorReport, table string) bool {
	for _, item := range report.Tables {
		if item.Table == table {
			return item.Count > 0
		}
	}
	return false
}
