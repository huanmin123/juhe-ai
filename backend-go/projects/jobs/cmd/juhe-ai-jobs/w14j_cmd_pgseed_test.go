// 波次 w14j：共享覆盖库（w1cover）PG 门控测试的自愈 seed。
// 该库会被外部流程周期性重置为最小形状；这里集中沉淀加法幂等 DDL
// （CREATE ... IF NOT EXISTS / ADD COLUMN IF NOT EXISTS / ADD CONSTRAINT /
// SET DEFAULT / CREATE OR REPLACE FUNCTION / 种子行 ON CONFLICT DO NOTHING），
// 各 PG 门控测试入口调用即可自愈。连接串绝不写入日志与断言。
package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	capdomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// w14jPGSeedStatements 加法幂等 DDL。来源：
//   - juhe_business.* 生产形状表/列/约束摘自 maintenance/internal/schema/pg_schema.go
//   - juhe_jobs.proxy_latency_* 摘自 maintenance/internal/j3aproxylatency/bootstrap.go
//   - group_account_stats_dirty 与 statsverify 的 SQLite 最小形同构
func w14jPGSeedStatements() []string {
	// M2 启动阶段门禁夹具契约：覆盖库必须自带已封存的阶段记录（记录 JSON
	// 经共享包 Record 序列化，不在测试侧手写字面量）。
	stageRecordJSON, err := json.Marshal(capdomain.Record{
		Stage:       capdomain.StageM2Finalized,
		RuleVersion: capdomain.RuleVersion,
		UpdatedAt:   "2026-10-12T08:00:00Z",
	})
	if err != nil {
		panic("marshal capability domain stage record: " + err.Error())
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS juhe_business.global_settings (
      key text PRIMARY KEY,
      value_json text NOT NULL,
      updated_at text NOT NULL)`,
		`INSERT INTO juhe_business.global_settings (key, value_json, updated_at)
      VALUES ('` + capdomain.MigrationSettingKey + `', '` + string(stageRecordJSON) + `', '2026-10-12T08:00:00Z')
      ON CONFLICT (key) DO NOTHING`,
		// 调度偏好独立实体（PLAN-20261012T033000000Z P1）启动校验夹具契约：
		// jobs 启动只读校验（scheduling_preference_schema.go）要求覆盖库自带
		// scheduling_preferences 表、api_keys 绑定列与合规内置行；旧版外部
		// 重建的共享覆盖库缺三者会直接撞启动门禁。按 maintenance 权威 DDL
		// 加法幂等补齐（表在前——api_keys ALTER 的列级 REFERENCES 需要目标
		// 表先存在；内置行权威值与 maintenance seed 同源，DO NOTHING 幂等，
		// 同 global_settings 阶段记录先例）。
		`CREATE TABLE IF NOT EXISTS juhe_business.scheduling_preferences (
      id text PRIMARY KEY,
      system_account_id text,
      name text NOT NULL,
      chat_mode text NOT NULL CHECK (chat_mode IN ('cost_first', 'speed_first')),
      chat_speed_first_config_json text,
      built_in integer NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1)),
      enabled integer NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
      created_at text NOT NULL,
      updated_at text NOT NULL)`,
		`ALTER TABLE juhe_business.api_keys ADD COLUMN IF NOT EXISTS scheduling_preference_id text REFERENCES juhe_business.scheduling_preferences(id)`,
		`INSERT INTO juhe_business.scheduling_preferences (id, system_account_id, name, chat_mode, built_in, created_at, updated_at)
      VALUES ('schpref-builtin-chat-cost-first', NULL, '默认调度偏好', 'cost_first', 1, '2026-10-12T08:00:00Z', '2026-10-12T08:00:00Z')
      ON CONFLICT (id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS juhe_business.system_teams (
      id text PRIMARY KEY, name text NOT NULL, description text, status text NOT NULL DEFAULT 'active',
      created_by text NOT NULL, created_at text NOT NULL, updated_at text NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.resource_authorization_grants (
      id text PRIMARY KEY, resource_type text NOT NULL, resource_id text NOT NULL,
      resource_owner_system_account_id text NOT NULL, grantee_type text NOT NULL,
      grantee_system_account_id text, grantee_team_id text, scope text NOT NULL DEFAULT 'use',
      status text NOT NULL DEFAULT 'active', remark text, expires_at text, limits_json text,
      created_by text NOT NULL, created_at text NOT NULL, revoked_by text, revoked_at text,
      updated_at text NOT NULL,
      CHECK ((grantee_type = 'system_account' AND grantee_system_account_id IS NOT NULL AND grantee_team_id IS NULL)
        OR (grantee_type = 'team' AND grantee_team_id IS NOT NULL AND grantee_system_account_id IS NULL)),
      FOREIGN KEY (grantee_system_account_id) REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
      FOREIGN KEY (grantee_team_id) REFERENCES juhe_business.system_teams(id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_versions (
      account_id text PRIMARY KEY, current_version integer NOT NULL CHECK (current_version >= 1),
      reserved_at text NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_health_jobs_input_outbox (
      event_id text PRIMARY KEY, account_id text NOT NULL,
      input_version integer NOT NULL CHECK (input_version >= 1),
      event_kind text NOT NULL CHECK (event_kind IN ('snapshot', 'tombstone')), reason text NOT NULL,
      config_revision integer NOT NULL CHECK (config_revision >= 1),
      dispatch_revision bigint NOT NULL CHECK (dispatch_revision >= 1),
      status text NOT NULL CHECK (status IN ('pending', 'leased', 'published', 'failed', 'superseded')),
      claim_token text, claimed_until text, attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
      available_at text NOT NULL, last_error text, created_at text NOT NULL, updated_at text NOT NULL,
      UNIQUE (account_id, input_version),
      CHECK ((status = 'leased' AND claim_token IS NOT NULL AND claimed_until IS NOT NULL)
        OR (status <> 'leased' AND claim_token IS NULL AND claimed_until IS NULL)))`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_health_projection_cursors (
      consumer_key text PRIMARY KEY, observed_at text, outcome_id text, updated_at text NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.account_health_projection_receipts (
      outcome_id text PRIMARY KEY, account_id text NOT NULL, input_version integer NOT NULL,
      disposition text NOT NULL, reason text, applied_at text NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_business.group_account_stats_dirty (
      group_id TEXT PRIMARY KEY, reason TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_owner_leases (
 lease_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_proxy_leases (
 proxy_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, fence_token BIGINT NOT NULL, lease_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_outcomes (
 outcome_id TEXT PRIMARY KEY, request_id TEXT NOT NULL UNIQUE, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL,
 config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL,
 observed_at TIMESTAMPTZ NOT NULL, stored_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL, payload_digest TEXT NOT NULL,
 committed BOOLEAN NOT NULL DEFAULT FALSE)`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_input_versions (
 proxy_id TEXT PRIMARY KEY, next_version BIGINT NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_inputs (
 request_id TEXT PRIMARY KEY, proxy_id TEXT NOT NULL, input_version BIGINT NOT NULL, config_revision TEXT NOT NULL,
 trigger TEXT NOT NULL, issued_at TIMESTAMPTZ NOT NULL, expires_at TIMESTAMPTZ NOT NULL, payload JSONB NOT NULL,
 payload_digest TEXT NOT NULL, UNIQUE(proxy_id, input_version))`,
		`CREATE TABLE IF NOT EXISTS juhe_jobs.proxy_latency_execution_claims (
 request_id TEXT PRIMARY KEY, claim_token TEXT NOT NULL, outcome_id TEXT NOT NULL, proxy_id TEXT NOT NULL,
 input_version BIGINT NOT NULL, config_revision TEXT NOT NULL, trigger TEXT NOT NULL, owner_id TEXT NOT NULL,
 owner_fence_token BIGINT NOT NULL, proxy_fence_token BIGINT NOT NULL, input_digest TEXT NOT NULL,
 claim_until TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_proxy ON juhe_jobs.proxy_latency_outcomes(proxy_id, observed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_proxy_latency_outcomes_cursor ON juhe_jobs.proxy_latency_outcomes(stored_at, outcome_id)`,
		// groups / group_accounts / resource_authorizations / proxy_profiles 最小形状补列。
		`ALTER TABLE juhe_business.groups ADD COLUMN IF NOT EXISTS description text`,
		`ALTER TABLE juhe_business.groups ADD COLUMN IF NOT EXISTS is_default integer NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_business.groups ADD COLUMN IF NOT EXISTS group_type text NOT NULL DEFAULT 'personal'`,
		`ALTER TABLE juhe_business.groups ADD COLUMN IF NOT EXISTS scheduling_policy_json text`,
		`ALTER TABLE juhe_business.groups ADD COLUMN IF NOT EXISTS created_at text`,
		`ALTER TABLE juhe_business.groups ADD COLUMN IF NOT EXISTS updated_at text`,
		`ALTER TABLE juhe_business.group_accounts ADD COLUMN IF NOT EXISTS local_priority integer NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_business.group_accounts ADD COLUMN IF NOT EXISTS local_super_priority_enabled integer NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_business.group_accounts ADD COLUMN IF NOT EXISTS local_fallback_enabled integer NOT NULL DEFAULT 0`,
		`ALTER TABLE juhe_business.group_accounts ADD COLUMN IF NOT EXISTS created_at text`,
		`ALTER TABLE juhe_business.group_accounts ADD COLUMN IF NOT EXISTS updated_at text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS effective_source_type text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS effective_source_team_id text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS activated_at text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS last_source_changed_at text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS remark text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS limits_json text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS created_by text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS created_at text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS revoked_by text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS revoked_at text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS revoked_reason text`,
		`ALTER TABLE juhe_business.resource_authorizations ADD COLUMN IF NOT EXISTS updated_at text`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS system_account_id text`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS name text`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS remark text`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS updated_at timestamptz`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS last_tested_at timestamptz`,
		`ALTER TABLE juhe_business.proxy_profiles ADD COLUMN IF NOT EXISTS created_at timestamptz`,
		`ALTER TABLE juhe_business.providers ADD COLUMN IF NOT EXISTS updated_at timestamptz`,
		`ALTER TABLE juhe_business.providers ADD COLUMN IF NOT EXISTS remark text`,
		`ALTER TABLE juhe_business.provider_protocol_profiles ADD COLUMN IF NOT EXISTS updated_at timestamptz`,
		`ALTER TABLE juhe_business.provider_protocol_profiles ADD COLUMN IF NOT EXISTS remark text`,
		// accounts 生产默认值（只影响后续插入）。
		`ALTER TABLE juhe_business.accounts ALTER COLUMN balance_query_enabled SET DEFAULT 0`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN balance_query_config_json SET DEFAULT '{}'`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN availability_schedule_json SET DEFAULT NULL`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN cooldown_retest_observation_started_at SET DEFAULT NULL`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN cooldown_retest_generation SET DEFAULT NULL`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN account_expires_at SET DEFAULT NULL`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN cooldown_until SET DEFAULT NULL`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN deleted_at SET DEFAULT NULL`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN last_error_code SET DEFAULT NULL`,
		`ALTER TABLE juhe_business.accounts ALTER COLUMN proxy_profile_id SET DEFAULT NULL`,
		// 前波最小形状表补生产主键（空表/无重复数据时安全；已存在则跳过）。
		`ALTER TABLE juhe_business.groups ADD CONSTRAINT groups_pkey PRIMARY KEY (id)`,
		`ALTER TABLE juhe_business.group_accounts ADD CONSTRAINT group_accounts_pkey PRIMARY KEY (group_id, account_id)`,
		`ALTER TABLE juhe_business.resource_authorizations ADD CONSTRAINT resource_authorizations_pkey PRIMARY KEY (id)`,
		`ALTER TABLE juhe_business.proxy_profiles ADD CONSTRAINT proxy_profiles_pkey PRIMARY KEY (id)`,
	}
	return statements
}

// w14jEnsurePGFixture 自愈共享覆盖库的测试形状；逐条应用，失败只记录不中断。
func w14jEnsurePGFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range w14jPGSeedStatements() {
		if _, err := db.Exec(statement); err != nil {
			if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "multiple primary keys") {
				continue
			}
			t.Logf("w14j pg seed 跳过: %v", err)
		}
	}
	w14jSeedSysAdminAccount(t, db)
}

// w14jSeedSysAdminAccount 幂等补齐 canonical sys_admin 系统账户。
func w14jSeedSysAdminAccount(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO juhe_business.system_accounts
		(id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ('sys_admin', 'sys_admin', 'sys_admin', 'admin', 'active', 'w14j-test-seed-not-a-login-hash', $1, $1)
		ON CONFLICT (id) DO NOTHING`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Skipf("w14j: sys_admin seed 失败（共享库形状漂移，跳过）: %v", err)
	}
}

// w14jBusinessColumnsAreTextTyped 探测共享库业务表是否仍是前波 text 桩形状。
// true 时依赖类型化列（integer/boolean 比较）的候选查询必然 42883 失败，
// 相关测试必须 Skip（列类型迁移属破坏性变更，未经授权不得执行）。
func w14jBusinessColumnsAreTextTyped(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var dataType string
	err := db.QueryRow(`SELECT data_type FROM information_schema.columns
		WHERE table_schema='juhe_business' AND table_name='accounts' AND column_name='schedulable'`).Scan(&dataType)
	if err != nil {
		return true
	}
	return dataType == "text"
}
