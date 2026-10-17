// Code generated from the Node PostgreSQL storage sources listed below. The
// statements are the exact output of collectPostgresSchemaStatements() in
// postgres-schema.ts (dumped with tsx and ported verbatim), executed in the
// same order as applyPostgresSchema(). Do not hand-edit the SQL constants;
// regenerate or re-verify against the Node sources when they change.
//
// Node sources (juhe-ai backend/src/storage):
//   - postgres-schema.ts          collectPostgresSchemaStatements / applyPostgresSchema
//   - postgres-seed-defaults.ts   seedPostgresDefaults
//   - schema-defaults.ts          DEFAULT_* seed constants
//   - request-quota-limits.ts     defaultRequestQuotaHourlyWindowHours
//
// Execution model (mirrors applyPostgresSchema):
//   - Before the first statement of each schema the runner executes
//     CREATE SCHEMA IF NOT EXISTS "<schema>".
//   - Every statement is sent as one batch, prefixed with
//     SET search_path TO "<schema>", public; so unqualified names resolve
//     exactly like Node. pgx uses the simple query protocol for
//     zero-argument Exec calls, which allows these multi-statement batches.
//   - Every statement carries its own idempotency guard (IF NOT EXISTS,
//     DROP TRIGGER IF EXISTS + CREATE TRIGGER, guarded DO $$ blocks), so
//     repeated EnsurePostgres calls are no-ops.
//
// Seeds (EnsurePostgresSeeds) port the statements of seedPostgresDefaults
// whose data is static or derived deterministically. Deliberately NOT ported
// (they need human review / application ports first):
//   - the provider_model_catalog bulk upsert and the stale built-in model
//     disable (Node listProviderModelPricing pricing catalog: 105 models x
//     39 parameters),
//   - default route strategy / API key and admin chat API key seeding
//     (Node createApiKey + hashSecret + encryptJson secret material),
//   - the external integration source token creation/update (random token +
//     encryptJson); the token-free source row seeding is ported.
//   - repairBuiltInProviderProfileAccountTypes IS ported (pure read-merge-update).
//
// BUG-0167/0168 follow-up (2026-09-04): the omitted pieces above ARE ported
// now — SeedPostgresDefaults in pg_seed.go is the complete seedPostgresDefaults
// port (bulk upsert + guarded stale disable over the generated pricing
// snapshot in model_catalog_data.go, default route strategies / default API
// keys / admin chat API key with the Node crypto envelopes, and the external
// integration source token). EnsurePostgresSeeds stays unchanged as the
// portable subset for the existing golden test; new callers must use
// SeedPostgresDefaults. The catalog snapshot is data-driven: the header's
// "105 models" reflected the dump date — the 2026-09-04 Node dump yields 106
// rows (see model_catalog_data.go).

package schema

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// PGStatement mirrors PostgresSchemaStatement in postgres-schema.ts.
type PGStatement struct {
	SchemaName string
	Source     string
	SQL        string
}

// postgresSchemaStatements holds every DDL statement of
// collectPostgresSchemaStatements() in execution order (per-schema table
// dependency order, then ALTER/DO phase, then CREATE FUNCTION blocks, then
// indexes; schema groups in Node first-seen order: juhe_business, juhe_chat,
// juhe_dataset, juhe_usage, juhe_stats, juhe_codex_context). The group
// slices live in the pg_schema_<group>.go data files and are concatenated
// below in that same Node first-seen order; the concatenation order is the
// execution order and the only aggregation point.
var postgresSchemaStatements = slices.Concat(
	postgresSchemaBusinessTables,
	postgresSchemaBusinessIndexes,
	postgresSchemaChat,
	postgresSchemaDataset,
	postgresSchemaUsage,
	postgresSchemaStats,
	postgresSchemaCodexContext,
)

// Statements returns the raw PostgreSQL statement list in execution order.
// goldenPostgresSchemaStatementCount in pg_schema_test.go pins its length to
// the Node source output.
func Statements() []string {
	statements := make([]string, len(postgresSchemaStatements))
	for i, statement := range postgresSchemaStatements {
		statements[i] = statement.SQL
	}
	return statements
}

// PGResult summarizes EnsurePostgres, mirroring the Node
// applyPostgresSchema return value.
type PGResult struct {
	SchemaCount    int
	StatementCount int
}

// EnsurePostgres applies the full PostgreSQL schema (business, chat, dataset,
// usage, stats and codex context) to db, executing CREATE SCHEMA once per
// schema group and every DDL statement in the Node applyPostgresSchema order.
// All statements are idempotent, so repeated calls are no-ops. Transient lock
// conflicts (deadlock_detected 40P01 / lock_not_available 55P03 / statement
// timeout 57014, e.g. against concurrently running jobs projections) are
// retried per statement with bounded backoff — see pg_lock_retry.go.
func EnsurePostgres(ctx context.Context, db *sql.DB) (PGResult, error) {
	createdSchemas := make(map[string]bool)
	for i, statement := range postgresSchemaStatements {
		if !createdSchemas[statement.SchemaName] {
			createdSchemas[statement.SchemaName] = true
			createSchema := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quotePGIdentifier(statement.SchemaName))
			if err := execPostgresStatementWithLockRetry(ctx, db, createSchema, createSchema); err != nil {
				return PGResult{}, fmt.Errorf("create postgres schema %s: %w", statement.SchemaName, err)
			}
		}
		execSQL := fmt.Sprintf("SET search_path TO %s, public;\n%s", quotePGIdentifier(statement.SchemaName), statement.SQL)
		if err := execPostgresStatementWithLockRetry(ctx, db, execSQL, statement.SQL); err != nil {
			return PGResult{}, fmt.Errorf("postgres schema statement %d (%s/%s): %w", i, statement.SchemaName, statement.Source, err)
		}
	}
	// AI 账户能力域拆分（PLAN-20261011T141004000Z）：ADD COLUMN IF NOT EXISTS
	// 只保证列存在，不以命中替代形状检查——全部语句执行完后对既存列做四值
	// 形状校验（类型/NOT NULL/DEFAULT/CHECK），不符原样报错停止。
	if err := VerifyPostgresBusinessCapabilityDomainShape(ctx, db); err != nil {
		return PGResult{}, fmt.Errorf("verify accounts.capability_domain shape: %w", err)
	}
	return PGResult{SchemaCount: len(createdSchemas), StatementCount: len(postgresSchemaStatements)}, nil
}

// EnsurePostgresBusinessCapabilityDomainColumn 把 accounts.capability_domain
// 补齐到既有 PostgreSQL 业务库并做四值形状校验，供能力域迁移命令使用：缺列
// 则执行与 schema 语句清单同文的 ADD COLUMN IF NOT EXISTS（存量行落
// DEFAULT 'chat' 满足列级 CHECK）；列已存在时仅做形状校验。只承载这一列，
// 不执行其余 schema 语句（迁移窗口内不改函数/索引/触发器定义）。调用面是
// maintenance 一次性命令，不在请求路径反复执行（BUG-0307）。
func EnsurePostgresBusinessCapabilityDomainColumn(ctx context.Context, db *sql.DB) error {
	const alter = `ALTER TABLE "juhe_business"."accounts" ADD COLUMN IF NOT EXISTS capability_domain text NOT NULL DEFAULT 'chat' CHECK (capability_domain IN ('chat','video','audio','image'))`
	if _, err := db.ExecContext(ctx, alter); err != nil {
		return fmt.Errorf("add accounts.capability_domain: %w", err)
	}
	return VerifyPostgresBusinessCapabilityDomainShape(ctx, db)
}

// VerifyPostgresBusinessCapabilityDomainShape 校验既存
// "juhe_business"."accounts".capability_domain 列的四值形状。列不存在返回
// nil（调用方决定缺列是否可接受：EnsurePostgres 的 ALTER 已先行补列，只有
// 显式 ALTER 失败才会走到这里缺列）。任何一项形状不符都原样报错停止，不做
// 静默修补。
func VerifyPostgresBusinessCapabilityDomainShape(ctx context.Context, db *sql.DB) error {
	const query = `SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = 'juhe_business' AND table_name = 'accounts' AND column_name = 'capability_domain'`
	var dataType, isNullable string
	var columnDefault sql.NullString
	err := db.QueryRowContext(ctx, query).Scan(&dataType, &isNullable, &columnDefault)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if dataType != "text" {
		return fmt.Errorf("accounts.capability_domain 形状不符：列类型为 %q，期望 text", dataType)
	}
	if isNullable != "NO" {
		return fmt.Errorf("accounts.capability_domain 形状不符：列可空，期望 NOT NULL")
	}
	// PostgreSQL 对 text 列 DEFAULT 的规范化渲染是 '<literal>::text'（实测 PG
	// information_schema 不产出 ::character varying 后缀；gateway/jobs 启动侧
	// 同口径按 ::text 前缀判据）。
	if !columnDefault.Valid || !strings.HasPrefix(columnDefault.String, "'chat'::text") {
		return fmt.Errorf("accounts.capability_domain 形状不符：默认值为 %s，期望 'chat'::text", pgShapeDefaultText(columnDefault))
	}
	const checkQuery = `SELECT cc.check_clause
		FROM information_schema.table_constraints tc
		JOIN information_schema.check_constraints cc
		  ON tc.constraint_schema = cc.constraint_schema AND tc.constraint_name = cc.constraint_name
		WHERE tc.table_schema = 'juhe_business' AND tc.table_name = 'accounts'
		  AND tc.constraint_type = 'CHECK' AND cc.check_clause LIKE '%capability_domain%'`
	rows, err := db.QueryContext(ctx, checkQuery)
	if err != nil {
		return err
	}
	defer rows.Close()
	clauses := []string{}
	for rows.Next() {
		var clause string
		if err := rows.Scan(&clause); err != nil {
			return err
		}
		clauses = append(clauses, clause)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// 归一化后的 CHECK 文本可能把 IN 重写为 = ANY (ARRAY[...])，不锚定具体
	// 文法；判据是四值词表与列名同时出现在同一 CHECK 子句中，且四值齐全。
	for _, clause := range clauses {
		if pgCapabilityDomainCheckComplete(clause) {
			return nil
		}
	}
	if len(clauses) == 0 {
		return fmt.Errorf("accounts.capability_domain 形状不符：未找到该列的 CHECK 约束")
	}
	return fmt.Errorf("accounts.capability_domain 形状不符：CHECK 约束缺少四值枚举（chat/video/audio/image），实际：%s", strings.Join(clauses, " | "))
}

// pgCapabilityDomainCheckComplete 判定一段归一化 CHECK 文本是否覆盖
// capability_domain 的四值枚举。
func pgCapabilityDomainCheckComplete(clause string) bool {
	for _, value := range []string{"chat", "video", "audio", "image"} {
		if !strings.Contains(clause, "'"+value+"'") {
			return false
		}
	}
	return strings.Contains(clause, "capability_domain")
}

// pgShapeDefaultText renders an information_schema column_default for error messages.
func pgShapeDefaultText(dflt sql.NullString) string {
	if !dflt.Valid {
		return "NULL（无默认值）"
	}
	return dflt.String
}

// quotePGIdentifier mirrors quoteIdentifier in postgres-schema.ts.
func quotePGIdentifier(identifier string) string {
	return "\"" + strings.ReplaceAll(identifier, "\"", "\"\"") + "\""
}

// ---------- seeds (port of postgres-seed-defaults.ts) ----------

// PGSeedResult summarizes EnsurePostgresSeeds.
type PGSeedResult struct {
	StatementCount int
	// BuiltinQuestionRows 是本次 seed 尝试写入的系统内置题库题行数
	//（固定 8 题；冲突时按内置行恢复 seed 权威题面，详见
	// pgSeedBuiltinQuestionUpsert）。
	BuiltinQuestionRows int
	// SchedulingPreferenceMigrationSealed 是本次 seed 触发「全新空库自动写调
	// 度偏好迁移完成记录」的结果（调度偏好独立实体 P2 设计门裁决②；仅记录不
	// 存在且 api_keys 无行时为 true）。
	SchedulingPreferenceMigrationSealed bool
}

// pgSeedSystemAccountsInsert seeds the default super admin account with
// must_change_password = 1 (BUG-0224: the seeded super admin authenticates
// with the well-known default password admin/admin, so the first login must
// hit the must_change_password 403 gate instead of relying on a manual
// rename step). Contract adjudication 2026-09-28: the Node archive
// (Node archive moved out of the repo,
// retrievable from git history) carries no seed source at all — the
// postgres-seed-defaults.ts this port's header cites does not exist there —
// so there is no Node seed value to mirror. The Node generic create path
// defaults mustChangePassword to true but
// normalizeSystemAccountMustChangePassword (system-accounts.repository.ts:1500)
// short-circuits to false for super_admin/admin; that semantics belongs to
// admin-console account creation (password chosen by the creator), not to a
// seed row created with a public default password. The runtime 403 gate
// (Node auth.middleware.ts:51-52, Go authsys middleware) checks the column
// value with no role exemption, so a seeded 1 takes effect for super_admin.
// The deploy guide's standing "首次登录立即改密" step states the intent;
// seeding 1 turns it into the enforced gate. Both PG call sites
// (EnsurePostgresSeeds and seedPostgresDefaults) share this constant and
// stay ON CONFLICT DO NOTHING, so existing databases keep their stored rows
// untouched.
const pgSeedSystemAccountsInsert = `
      INSERT INTO "juhe_business"."system_accounts" (
        id, username, display_name, description, role, status, password_hash, must_change_password, image_generation_enabled, created_at, updated_at
      ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
      ON CONFLICT DO NOTHING
    `

// pgSeedGlobalSettingsInsert seeds one global_settings row.
const pgSeedGlobalSettingsInsert = `
        INSERT INTO "juhe_business"."global_settings" (key, value_json, updated_at)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
      `

// pgSeedWindowConfigInsert seeds one request_quota_hourly_window_configs row.
const pgSeedWindowConfigInsert = `
        INSERT INTO "juhe_business"."request_quota_hourly_window_configs" (window_hours, created_at, updated_at)
        VALUES ($1, $2, $3)
        ON CONFLICT DO NOTHING
      `

// pgSeedQuotaScopeBindingsFromAPIKeysCTE backfills hourly quota scope bindings from active API keys (parameter-free, idempotent).
const pgSeedQuotaScopeBindingsFromAPIKeysCTE = `
    WITH inserted AS (
      INSERT INTO "juhe_business"."request_quota_hourly_window_scope_bindings" (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT system_account_id, 'api_key', id, 'api_key', id,
        (quota_limits_json::jsonb #>> '{hourly,hours}')::integer, created_at, updated_at
      FROM "juhe_business"."api_keys"
      WHERE status = 'active'
        AND quota_limits_json IS NOT NULL
        AND quota_limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND quota_limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (quota_limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `

// pgSeedQuotaScopeBindingsFromAuthorizationsCTE backfills quota scope bindings from resource authorizations (parameter-free, idempotent).
const pgSeedQuotaScopeBindingsFromAuthorizationsCTE = `
    WITH inserted AS (
      INSERT INTO "juhe_business"."request_quota_hourly_window_scope_bindings" (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END,
        CASE WHEN ra.resource_type = 'account' THEN 'account_authorization' ELSE 'group_authorization' END,
        ra.id, 'resource_authorization_grant', grants.id,
        (ra.limits_json::jsonb #>> '{hourly,hours}')::integer, ra.created_at, ra.updated_at
      FROM "juhe_business"."resource_authorizations" ra
      INNER JOIN "juhe_business"."resource_authorization_grants" grants
        ON grants.resource_type = ra.resource_type
        AND grants.resource_id = ra.resource_id
        AND grants.status = 'active'
        AND (
          (ra.effective_source_type = 'manual' AND grants.grantee_type = 'system_account' AND grants.grantee_system_account_id = ra.grantee_system_account_id)
          OR
          (ra.effective_source_type = 'team' AND grants.grantee_type = 'team' AND grants.grantee_team_id = ra.effective_source_team_id)
        )
      WHERE ra.status = 'active'
        AND ra.limits_json IS NOT NULL
        AND ra.limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND ra.limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (ra.limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `

// pgSeedQuotaScopeBindingsFromTeamGrantsCTE backfills team quota scope bindings from authorization grants (parameter-free, idempotent).
const pgSeedQuotaScopeBindingsFromTeamGrantsCTE = `
    WITH candidates AS (
      SELECT DISTINCT
        CASE WHEN ra.resource_type = 'account' THEN ra.grantee_system_account_id ELSE ra.resource_owner_system_account_id END AS system_account_id,
        CASE WHEN ra.resource_type = 'account' THEN 'account_authorization_team' ELSE 'group_authorization_team' END AS scope_type,
        CASE WHEN ra.resource_type = 'account' THEN instance_accounts.id || ':' || ra.effective_source_team_id ELSE ra.resource_id || ':' || ra.effective_source_team_id END AS scope_id,
        grants.id AS source_id,
        (ra.limits_json::jsonb #>> '{hourly,hours}')::integer AS window_hours,
        ra.created_at,
        ra.updated_at
      FROM "juhe_business"."resource_authorizations" ra
      INNER JOIN "juhe_business"."resource_authorization_grants" grants
        ON grants.resource_type = ra.resource_type
        AND grants.resource_id = ra.resource_id
        AND grants.grantee_type = 'team'
        AND grants.grantee_team_id = ra.effective_source_team_id
        AND grants.status = 'active'
      LEFT JOIN "juhe_business"."accounts" instance_accounts
        ON ra.resource_type = 'account'
        AND instance_accounts.authorization_instance_authorization_id = ra.id
        AND instance_accounts.system_account_id = ra.grantee_system_account_id
        AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
        AND instance_accounts.deleted_at IS NULL
      WHERE ra.status = 'active'
        AND ra.effective_source_type = 'team'
        AND (ra.resource_type = 'group' OR instance_accounts.id IS NOT NULL)
        AND ra.limits_json IS NOT NULL
        AND ra.limits_json::jsonb #>> '{hourly,enabled}' = 'true'
        AND ra.limits_json::jsonb #>> '{hourly,hours}' ~ '^[0-9]+$'
        AND (ra.limits_json::jsonb #>> '{hourly,hours}')::integer BETWEEN 1 AND 720
    ), inserted AS (
      INSERT INTO "juhe_business"."request_quota_hourly_window_scope_bindings" (
        system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
      )
      SELECT system_account_id, scope_type, scope_id, 'resource_authorization_grant', source_id,
        window_hours, created_at, updated_at
      FROM candidates
      WHERE true
      ON CONFLICT(system_account_id, scope_type, scope_id) DO NOTHING
      RETURNING system_account_id, scope_type, scope_id, created_at, updated_at
    )
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, 1, created_at, updated_at FROM inserted
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `

// pgSeedProviderInsert seeds one vendors row（设计 §9：providers 由 vendors
// 取代，无 parent_code 列）。
const pgSeedProviderInsert = `
        INSERT INTO "juhe_business"."vendors" (
          id, code, name, description, enabled, default_supported_models_json, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT DO NOTHING
      `

// pgSeedProviderDefaultModelsRepair refreshes empty vendor default model lists.
const pgSeedProviderDefaultModelsRepair = `
        UPDATE "juhe_business"."vendors"
        SET default_supported_models_json = $1, updated_at = $2
        WHERE code = $3
          AND (default_supported_models_json IS NULL OR btrim(default_supported_models_json) = '' OR default_supported_models_json = '[]')
      `

// pgSeedProtocolInsert seeds one protocols row.
const pgSeedProtocolInsert = `
        INSERT INTO "juhe_business"."protocols" (
          id, code, version, name, description, enabled, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        ON CONFLICT DO NOTHING
      `

// pgSeedEndpointFamilyInsert seeds one protocol_endpoint_families row.
const pgSeedEndpointFamilyInsert = `
        INSERT INTO "juhe_business"."protocol_endpoint_families" (
          id, protocol_code, protocol_version, family_code, name, description, enabled, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT DO NOTHING
      `

// provider_protocol_profiles seed（pgSeedProfileInsert/
// pgSeedProfileAccountTypesSelect/pgSeedProfileAccountTypesUpdate/
// pgSeedProfileFamilyInsert 与 pgSeedProfiles 数据）随供应商与上游协议面重构
// 批退役：目标架构中协议档案由 §1.1 连接器注册表（connection preset）承担，
// DDL 已无 provider_protocol_profiles / provider_protocol_profile_families
// 两表，seed 不再写任何 profile 行。protocols / protocol_endpoint_families
// 仍为目标表，其 seed 保留。

// pgSeedGroupDefaultRepair marks the built-in default group when its owner has
// no default group yet（设计 §8：每 system_account_id 至多一个 is_default=1，
// 唯一索引 idx_groups_owner_default_unique；判定只按 owner，无供应商维度）。
const pgSeedGroupDefaultRepair = `
        UPDATE "juhe_business"."groups" AS candidate
        SET is_default = 1
        WHERE candidate.is_default = 0
          AND candidate.system_account_id = $1
          AND candidate.id = $2
          AND NOT EXISTS (
            SELECT 1
            FROM "juhe_business"."groups" AS existing_default
            WHERE existing_default.system_account_id = candidate.system_account_id
              AND existing_default.is_default = 1
          )
      `

// pgSeedGroupInsert inserts the built-in default group for every system account
// that lacks one（缺组判定只按 owner 的 is_default=1，无供应商维度；唯一约束
// (system_account_id, name) 下的重名回退命名逻辑保持原形态）。
const pgSeedGroupInsert = `
        INSERT INTO "juhe_business"."groups" (
          id, system_account_id, name,
          description, enabled, is_default, created_at, updated_at
        )
        SELECT
          CASE WHEN system_accounts.id = $1 THEN $2 ELSE $3 || system_accounts.id END,
          system_accounts.id,
          CASE
            WHEN EXISTS (
              SELECT 1
              FROM "juhe_business"."groups" AS same_name
              WHERE same_name.system_account_id = system_accounts.id
                AND lower(same_name.name) = lower($4)
          ) THEN $5 || '（系统默认：' || system_accounts.id || CASE
            WHEN candidate_suffix.suffix = 0 THEN ''
            ELSE ' #' || candidate_suffix.suffix
          END || '）'
          ELSE $4
        END,
          $6, 1, 1, $7, $8
        FROM "juhe_business"."system_accounts" AS system_accounts
        LEFT JOIN LATERAL (
          SELECT candidate_suffix.suffix
          FROM generate_series(
            0,
            (
              SELECT COUNT(*)
              FROM "juhe_business"."groups" AS fallback_name
              WHERE fallback_name.system_account_id = system_accounts.id
                AND lower(fallback_name.name) LIKE lower($5) || '（系统默认：%）'
            )
          ) AS candidate_suffix(suffix)
          WHERE NOT EXISTS (
            SELECT 1
            FROM "juhe_business"."groups" AS existing_fallback_name
            WHERE existing_fallback_name.system_account_id = system_accounts.id
              AND lower(existing_fallback_name.name) = lower(
                $5 || '（系统默认：' || system_accounts.id || CASE
                  WHEN candidate_suffix.suffix = 0 THEN ''
                  ELSE ' #' || candidate_suffix.suffix
                END || '）'
              )
          )
          ORDER BY candidate_suffix.suffix
          LIMIT 1
        ) AS candidate_suffix ON true
        WHERE NOT EXISTS (
          SELECT 1
          FROM "juhe_business"."groups" AS existing_default
          WHERE existing_default.system_account_id = system_accounts.id
            AND existing_default.is_default = 1
        )
        ON CONFLICT DO NOTHING
      `

// pgSeedExternalIntegrationSourceInsert seeds the built-in external integration test source row (the secret token part is intentionally not ported).
const pgSeedExternalIntegrationSourceInsert = `
      INSERT INTO "juhe_business"."external_integration_sources" (
        id, name, status, scopes_json, rate_limits_json, expires_at, notes, created_at, updated_at
      ) VALUES ($1, $2, 'active', $3, $4, NULL, $5, $6, $7)
      ON CONFLICT DO NOTHING
    `

// pgSeedExternalIntegrationSourceUpdate repairs the built-in external integration test source row.
const pgSeedExternalIntegrationSourceUpdate = `
      UPDATE "juhe_business"."external_integration_sources"
      SET name = $1,
          scopes_json = $2,
          rate_limits_json = $3,
          expires_at = NULL,
          notes = $4,
          updated_at = $5
      WHERE id = $6
    `

// pgSeedSystemSettingInsert seeds one system_settings row for sys_admin.
const pgSeedSystemSettingInsert = `
        INSERT INTO "juhe_business"."system_settings" (system_account_id, key, value_json, updated_at)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT DO NOTHING
      `

// pgSeedPasswordIterations mirrors passwordIterations in Node src/storage/crypto.ts.
const pgSeedPasswordIterations = 120000

// pgSeedGlobalSettings mirrors DEFAULT_GLOBAL_SETTINGS (value_json already JSON-encoded).
var pgSeedGlobalSettings = []pgSeedKeyValue{
	{Key: "appName", ValueJSON: "\"聚合 AI\""},
	{Key: "appIcon", ValueJSON: "\"/__aisys__/brand-icon.svg\""},
}

var pgSeedRequestQuotaHourlyWindowHours = []int{1, 3, 6, 12, 24, 72, 168, 720}

// pgSeedProvider mirrors the §1.1 vendor registry seed (设计 §9 迁移第 1 步:
// providers 表由 vendors 取代，只保留 9 个真实厂商；parent_code 列与 openai/
// hybrid 行随旧表退役)。
type pgSeedProvider struct {
	ID                         string
	Code                       string
	Name                       string
	Description                string
	Enabled                    int
	DefaultSupportedModelsJSON string
}

// pgSeedProviders 恰为设计 §1.1 的 9 个真实厂商；generic_http 连接不建
// vendor 行（账户 vendor_code 为 NULL）。openai（通用兼容）与 hybrid（混合）
// 两行随旧供应商维度删除，分组无供应商维度由 groups seed 收敛承担。
var pgSeedProviders = []pgSeedProvider{
	{
		ID:                         "gpt",
		Code:                       "gpt",
		Name:                       "GPT",
		Description:                "GPT 官方供应商，继承通用 OpenAI-compatible 能力，并启用 OAuth、Codex Responses 等 GPT 专属能力",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gpt-6.1-sol\",\"gpt-6-sol\",\"gpt-6-luna\",\"gpt-6-astra\",\"gpt-5.6-terra\",\"gpt-5.6-sol\"]",
	},
	{
		ID:                         "xai",
		Code:                       "xai",
		Name:                       "xAI / Grok",
		Description:                "xAI 官方供应商，支持 API Key 与 Grok OAuth 接入 OpenAI v1 文本协议；媒体面支持 Grok Imagine Video 视频（契约 §6.1）",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"grok-4.7\",\"grok-4.6\",\"grok-4.5\",\"grok-imagine-video-1.5\"]",
	},
	{
		ID:                         "deepseek",
		Code:                       "deepseek",
		Name:                       "DeepSeek",
		Description:                "DeepSeek 官方供应商，支持 OpenAI-compatible v1 Chat Completions 与 Responses 直连，也支持 Anthropic v1 Messages 档案兼容 Claude Code",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"deepseek-flash\",\"deepseek-v4.1-flash\",\"deepseek-v4-pro\"]",
	},
	{
		ID:                         "anthropic",
		Code:                       "anthropic",
		Name:                       "Anthropic",
		Description:                "Anthropic 官方供应商，支持 API Key 或 OAuth Access Token（Bearer）接入 Anthropic Messages 原生协议",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"claude-opus-5-5\",\"claude-fable-5-1\",\"claude-opus-5\",\"claude-sonnet-5\"]",
	},
	{
		ID:                         "gemini",
		Code:                       "gemini",
		Name:                       "Gemini",
		Description:                "Google Gemini 官方供应商，支持 API Key 或 Google OAuth 接入 Gemini v1beta Generate Content 与 Interactions；OpenAI 客户端可使用兼容档案",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gemini-3.8-flash\",\"gemini-3.7-flash\",\"gemini-3.6-flash\",\"gemini-3.5-flash-lite\"]",
	},
	{
		ID:                         "glm",
		Code:                       "glm",
		Name:                       "智谱 GLM",
		Description:                "智谱 GLM 官方供应商，支持通用 GLM API Key、GLM Coding Plan OpenAI Chat 档案，以及 GLM Coding Anthropic v1 Messages 档案",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"glm-5.3\",\"glm-5.2\",\"glm-5.1\",\"glm-5\",\"glm-5-turbo\",\"glm-4.7-flashx\",\"glm-4.7-flash\"]",
	},
	{
		// M3 媒体新增供应商（媒体设计 §9/契约 §8）：MiniMax 仅声明媒体能力
		//（Hailuo 视频生成 + speech 系 TTS），不承接聊天流量——MiniMax 聊天
		// 端点非 /v1/chat/completions，目录亦无 chat 模型行。
		ID:                         "minimax",
		Code:                       "minimax",
		Name:                       "MiniMax",
		Description:                "MiniMax 官方供应商，支持 API Key 接入视频生成（Hailuo 系 video_generation）与语音合成（speech 系 t2a_v2）媒体能力",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"MiniMax-Hailuo-2.3\",\"speech-2.8-turbo\"]",
	},
	{
		// M3 媒体新增供应商（媒体设计 §9/契约 §9）：火山方舟声明视频能力
		//（豆包 Seedance 系 contents/generations/tasks）；M6 补 TTS（豆包
		// openspeech /api/v3/tts/create，2026-10-17 TTS 迁移批自旧端点 /api/v3/tts 迁移）；对话批补聊天（ark OpenAI 兼容端点
		// /api/v3/chat/completions，豆包 doubao 对话模型面）；2026-10-17 补
		// 图像生成（豆包 seedream 系 OpenAI 兼容图像端点
		// /api/v3/images/generations，实测出图成功；Agent Plan 专属端点
		// /api/plan/v3/images/generations 实测可用）；同日语音识别承接批补
		// 长转写（豆包录音文件识别 2.0 openspeech /api/v3/auc/bigmodel/
		// submit|query 异步两段，`/v1/audio/jobs` 统一面，契约 §9.2）。
		//
		// DefaultSupportedModelsJSON 换为现役视频模型
		// doubao-seedance-2-0-mini-260615（既有 doubao-seedance-1-0-pro-250528
		// 官方「即将下线」），语音识别承接批追加 volc.seedasr.auc（上游
		// X-Api-Resource-Id 真实标识）。注意 seed upsert 只在新建库生效
		//（既有库不回填默认支持模型，属预期；运营可按需在管理面调整）。
		ID:                         "volcengine",
		Code:                       "volcengine",
		Name:                       "火山方舟",
		Description:                "火山方舟官方供应商，支持 API Key 接入对话（豆包 doubao 系 OpenAI 兼容端点）、视频生成（豆包 Seedance 系 contents/generations/tasks）、语音合成（豆包 TTS openspeech /api/v3/tts/create）、图像生成（豆包 seedream 系 images/generations）与语音识别长转写（豆包录音文件识别 2.0 /api/v3/auc/bigmodel/submit|query，语音应用凭据）能力",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"doubao-seedance-2-0-mini-260615\",\"volc.seedasr.auc\"]",
	},
	{
		// M3 媒体新增供应商（媒体设计 §9/契约 §10）：通义百炼声明视频能力
		//（万相 wan 系 DashScope 异步任务）；M3f 补长转写（paraformer）；
		// 对话批补聊天（DashScope OpenAI 兼容模式 /compatible-mode/v1/chat/
		// completions，qwen 对话模型面）。CosyVoice TTS 面仍未回填。
		ID:                         "qwen",
		Code:                       "qwen",
		Name:                       "通义百炼",
		Description:                "通义百炼（阿里云 DashScope）官方供应商，支持 API Key 接入对话（qwen 系 OpenAI 兼容模式）、视频生成（万相 wan 系 video-synthesis 异步任务）与长音频转写（paraformer 系录音文件识别）能力",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"wan2.2-t2v-plus\"]",
	},
}

// pgSeedProtocol mirrors DEFAULT_PROTOCOL_SEEDS.
type pgSeedProtocol struct {
	ID, Code, Version, Name, Description string
	Enabled                              int
}

var pgSeedProtocols = []pgSeedProtocol{
	{ID: "openai_v1", Code: "openai", Version: "v1", Name: "OpenAI v1", Description: "OpenAI-compatible v1 协议；接口族包含 Chat Completions 与 Responses", Enabled: 1},
	{ID: "anthropic_v1", Code: "anthropic", Version: "v1", Name: "Anthropic v1", Description: "Anthropic 官方 v1 协议；接口族包含 Messages、Models 与 Message Token Counting", Enabled: 1},
	{ID: "gemini_v1beta", Code: "gemini", Version: "v1beta", Name: "Gemini v1beta", Description: "Google Gemini v1beta 原生协议；接口族包含 Models、generateContent、streamGenerateContent、countTokens 与 embedContent", Enabled: 1},
}

// pgSeedEndpointFamily mirrors DEFAULT_PROTOCOL_ENDPOINT_FAMILY_SEEDS.
type pgSeedEndpointFamily struct {
	ID, ProtocolCode, ProtocolVersion, Code, Name, Description string
	Enabled                                                    int
}

var pgSeedEndpointFamilies = []pgSeedEndpointFamily{
	{ID: "openai_v1_chat_completions", ProtocolCode: "openai", ProtocolVersion: "v1", Code: "chat_completions", Name: "Chat Completions", Description: "OpenAI v1 /chat/completions 接口族", Enabled: 1},
	{ID: "openai_v1_responses", ProtocolCode: "openai", ProtocolVersion: "v1", Code: "responses", Name: "Responses", Description: "OpenAI v1 /responses 接口族", Enabled: 1},
	{ID: "anthropic_v1_messages", ProtocolCode: "anthropic", ProtocolVersion: "v1", Code: "messages", Name: "Messages", Description: "Anthropic v1 /messages 接口族", Enabled: 1},
	{ID: "anthropic_v1_models", ProtocolCode: "anthropic", ProtocolVersion: "v1", Code: "models", Name: "Models", Description: "Anthropic v1 /models 接口族", Enabled: 1},
	{ID: "anthropic_v1_message_token_counting", ProtocolCode: "anthropic", ProtocolVersion: "v1", Code: "message_token_counting", Name: "Message Token Counting", Description: "Anthropic v1 /messages/count_tokens 接口族", Enabled: 1},
	{ID: "gemini_v1beta_models", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "models", Name: "Models", Description: "Gemini v1beta /models 接口族", Enabled: 1},
	{ID: "gemini_v1beta_generate_content", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "generate_content", Name: "generateContent", Description: "Gemini v1beta :generateContent 接口族", Enabled: 1},
	{ID: "gemini_v1beta_stream_generate_content", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "stream_generate_content", Name: "streamGenerateContent", Description: "Gemini v1beta :streamGenerateContent SSE 接口族", Enabled: 1},
	{ID: "gemini_v1beta_count_tokens", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "count_tokens", Name: "countTokens", Description: "Gemini v1beta :countTokens 接口族", Enabled: 1},
	{ID: "gemini_v1beta_embed_content", ProtocolCode: "gemini", ProtocolVersion: "v1beta", Code: "embed_content", Name: "embedContent", Description: "Gemini v1beta :embedContent 接口族", Enabled: 1},
	// M3 媒体新增（媒体设计 §9/§11.2）：openai 协议族的厂商原生媒体端点族——
	// 统一 /v1/videos、/v1/audio/speech 面承载，厂商原生报文经媒体 adapter
	// 改写（minimax video_generation / t2a_v2，契约 §8）。
	{ID: "openai_v1_video_generation", ProtocolCode: "openai", ProtocolVersion: "v1", Code: "video_generation", Name: "Video Generation", Description: "OpenAI v1 统一 /videos 面承载的厂商原生视频生成任务形态", Enabled: 1},
	{ID: "openai_v1_tts", ProtocolCode: "openai", ProtocolVersion: "v1", Code: "tts", Name: "TTS", Description: "OpenAI v1 统一 /audio/speech 面承载的厂商原生语音合成形态", Enabled: 1},
	// M3f 长音频任务族（媒体设计 §4.2，契约 §10.2）：openai 协议族的厂商
	// 原生长转写端点族——统一 /v1/audio/jobs 面承载，厂商原生报文经长转写
	// adapter 改写（首个上游 qwen paraformer 录音文件识别；沿 video_generation
	// family 先例）。
	{ID: "openai_v1_audio_transcription", ProtocolCode: "openai", ProtocolVersion: "v1", Code: "audio_transcription", Name: "Audio Transcription", Description: "OpenAI v1 统一 /audio/jobs 面承载的厂商原生长音频转写任务形态", Enabled: 1},
}

// pgSeedGroup mirrors the narrowed default-resource family (2026-10-07 默认
// 资源收口): sys_admin seeds exactly one built-in default group. 供应商与上游
// 协议面重构批（设计 §8）去掉组上的供应商维度：组名收敛为「默认分组」，
// 行内不再有 provider_code；ID 保留 grp_default_gpt_sys_admin 字面值以维持
// 既有库/网关派生 ID（route_strategy_/key_default_ 前缀替换链）稳定。
// Other groups are created explicitly by the operator; the sys_admin chat key
// is not seeded here either (the chat-session ensure chain provisions it on
// demand).
type pgSeedGroup struct {
	ID, SystemAccountID, Name, Description string
}

var pgSeedGroups = []pgSeedGroup{
	{ID: "grp_default_gpt_sys_admin", SystemAccountID: "sys_admin", Name: "默认分组", Description: ""},
}

// pgSeedExternalIntegrationSource mirrors the built-in external integration
// test source constants recorded from the Node seed run.
var pgSeedExternalIntegrationSource = struct {
	ID, Name, ScopesJSON, RateLimitsJSON, Notes string
}{
	ID:             "extsrc_builtin_test",
	Name:           "内置测试来源",
	ScopesJSON:     "[\"juhe_ai_public:account_add:write\",\"juhe_ai_public:account_delete:write\",\"juhe_ai_public:account_list:read\",\"juhe_ai_public:account_update:write\",\"juhe_ai_public:api_key_add:write\",\"juhe_ai_public:api_key_delete:write\",\"juhe_ai_public:api_key_list:read\",\"juhe_ai_public:api_key_update:write\",\"juhe_ai_public:group_add:write\",\"juhe_ai_public:group_delete:write\",\"juhe_ai_public:group_list:read\",\"juhe_ai_public:group_update:write\",\"juhe_ai_public:route_strategy_add:write\",\"juhe_ai_public:route_strategy_delete:write\",\"juhe_ai_public:route_strategy_list:read\",\"juhe_ai_public:route_strategy_update:write\"]",
	RateLimitsJSON: "[{\"windowSeconds\":60,\"maxRequests\":10}]",
	Notes:          "系统内置测试 Token，只返回 mock 数据；可停用或重置，不支持编辑或删除。",
}

// pgSeedSystemSettings mirrors DEFAULT_SYSTEM_SETTINGS (value_json already JSON-encoded).
var pgSeedSystemSettings = []pgSeedKeyValue{
	{Key: "gatewayTextRawBodyLimitMegabytes", ValueJSON: "16"},
	{Key: "accountCircuitConfirmationFailuresRequired", ValueJSON: "2"},
	{Key: "gatewayUserRequestLimitPerMinute", ValueJSON: "0"},
	{Key: "gatewayUserRequestLimitPerDay", ValueJSON: "0"},
	{Key: "gatewayUserRequestLimitPerWeek", ValueJSON: "0"},
	{Key: "gatewayUserRequestLimitPerMonth", ValueJSON: "0"},
	{Key: "userAiAccountLimit", ValueJSON: "100"},
	{Key: "systemApiRateLimitIpReadPerMinute", ValueJSON: "600"},
	{Key: "systemApiRateLimitIpReadBurstPer10Seconds", ValueJSON: "120"},
	{Key: "systemApiRateLimitIpWritePerMinute", ValueJSON: "180"},
	{Key: "systemApiRateLimitIpWriteBurstPer10Seconds", ValueJSON: "40"},
	{Key: "systemApiRateLimitUserReadPerMinute", ValueJSON: "300"},
	{Key: "systemApiRateLimitUserWritePerMinute", ValueJSON: "120"},
	{Key: "defaultTemporaryUnschedulableMinutes", ValueJSON: "2"},
	{Key: "temporaryUnschedulableRetryIntervalSeconds", ValueJSON: "3"},
	{Key: "temporaryUnschedulableRetryAttempts", ValueJSON: "2"},
	{Key: "textFirstResponseTimeoutSeconds", ValueJSON: "120"},
	{Key: "textNonStreamFirstResponseTimeoutSeconds", ValueJSON: "600"},
	{Key: "textStreamIdleTimeoutSeconds", ValueJSON: "30"},
	{Key: "textUncommittedAttemptMaxLifetimeSeconds", ValueJSON: "1800"},
	{Key: "imageFirstResponseTimeoutSeconds", ValueJSON: "600"},
	{Key: "imageStreamIdleTimeoutSeconds", ValueJSON: "120"},
	{Key: "imageUncommittedAttemptMaxLifetimeSeconds", ValueJSON: "3600"},
	{Key: "imageRequestWallTimeoutSeconds", ValueJSON: "3600"},
	// 媒体车道独立超时档位（音频视频模型接入设计 §3 车道独立超时）：audio
	// 同步端点首响 120s；video 创建请求应快速返回 job 对象，60s。
	{Key: "audioFirstResponseTimeoutSeconds", ValueJSON: "120"},
	{Key: "videoCreateTimeoutSeconds", ValueJSON: "60"},
	// M5b realtime 会话生命周期键（Realtime 设计 §3：空闲超时=双向均无帧
	// 120s；最大会话时长 1800s；每 API Key 并发 WS 连接上限 5——消费面随
	// M5b2 WS 桥接 handler 生效，键位三处同源先行落地）。
	{Key: "realtimeIdleTimeoutSeconds", ValueJSON: "120"},
	{Key: "realtimeMaxSessionSeconds", ValueJSON: "1800"},
	{Key: "realtimeMaxConnectionsPerApiKey", ValueJSON: "5"},
	{Key: "noAvailableAccountWaitTimeoutSeconds", ValueJSON: "270"},
	{Key: "streamFailureThresholdCount", ValueJSON: "3"},
	{Key: "streamFailureThresholdWindowMinutes", ValueJSON: "5"},
	{Key: "operationLogRetentionDays", ValueJSON: "365"},
	{Key: "operationLogMaxChangesPerRecord", ValueJSON: "100"},
	// 2026-10-02 日志与审计设置：审计 retention 四键（与 settings 包
	// compatibleSystemSettingDefaults、auditlog LoadConfig 代码默认一致）。
	{Key: "auditLogSuccessRetentionDays", ValueJSON: "3"},
	{Key: "auditLogProblemRetentionDays", ValueJSON: "7"},
	{Key: "auditLogSuccessHotRetentionHours", ValueJSON: "1"},
	{Key: "auditLogSuccessSampleRate", ValueJSON: "1.0"},
	{Key: "statsAggregationIntervalSeconds", ValueJSON: "60"},
	{Key: "statsAggregationBatchSize", ValueJSON: "2000"},
	{Key: "statsAggregationMaxBatchesPerRun", ValueJSON: "5"},
	{Key: "usageHotWindowRefreshIntervalSeconds", ValueJSON: "600"},
	{Key: "groupAccountStatsRefreshIntervalSeconds", ValueJSON: "60"},
	{Key: "accountQualityRefreshIntervalSeconds", ValueJSON: "600"},
	{Key: "accountQualityWindowMinutes", ValueJSON: "10"},
	{Key: "accountHealthCheckIntervalHours", ValueJSON: "1"},
	{Key: "accountHealthCheckJitterMinutes", ValueJSON: "10"},
	{Key: "accountHealthCheckFailureThreshold", ValueJSON: "3"},
	{Key: "cooldownAccountRetestIntervalSeconds", ValueJSON: "3"},
	{Key: "cooldownAccountRetestMaxBackoffHours", ValueJSON: "12"},
	{Key: "oauthAccessTokenRefreshIntervalSeconds", ValueJSON: "60"},
	{Key: "oauthAccessTokenRefreshLeadSeconds", ValueJSON: "300"},
	{Key: "oauthAccessTokenRefreshBatchSize", ValueJSON: "20"},
	{Key: "oauthAccessTokenRefreshRetryBackoffSeconds", ValueJSON: "300"},
	{Key: "modelCheckRetentionDays", ValueJSON: "30"},
	{Key: "runtimeLogIndexRetentionDays", ValueJSON: "14"},
	{Key: "publicApiLogRetentionDays", ValueJSON: "30"},
	{Key: "usageRecordRetentionDays", ValueJSON: "30"},
	{Key: "usageStatsTimezone", ValueJSON: "\"Asia/Shanghai\""},
	{Key: "usageStatsMinuteRetentionHours", ValueJSON: "48"},
	{Key: "usageStatsHourlyRetentionDays", ValueJSON: "60"},
	{Key: "usageStatsDailyRetentionDays", ValueJSON: "400"},
	{Key: "usageStatsWeeklyRetentionWeeks", ValueJSON: "104"},
	{Key: "usageStatsMonthlyRetentionMonths", ValueJSON: "24"},
	{Key: "usageRankSnapshotRetentionDays", ValueJSON: "30"},
	{Key: "upstreamClientVersionOverrides", ValueJSON: "{}"},
	// 客户端版本自动层键（客户端版本自动跟版设计 §3）：与手动键同结构同默认，
	// 由 jobs 跟版任务独占写入、gateway/jobs 只读消费。
	{Key: "upstreamClientVersionAutoOverrides", ValueJSON: "{}"},
}

// pgSeedKeyValue is one key -> JSON-encoded value pair.
type pgSeedKeyValue struct {
	Key       string
	ValueJSON string
}

// EnsurePostgresSeeds applies the portable subset of seedPostgresDefaults in
// Node statement order: default admin account, global settings, quota window
// configs, quota scope binding backfills, vendors, protocols, endpoint
// families, built-in groups, the external integration source row and system
// settings. Every INSERT uses ON CONFLICT DO NOTHING and every UPDATE is a
// guarded repair, so repeated calls are idempotent. See the file header for
// the seed statements that are intentionally not ported yet.
func EnsurePostgresSeeds(ctx context.Context, db *sql.DB) (PGSeedResult, error) {
	var result PGSeedResult
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
	exec := func(query string, args ...any) error {
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("postgres seed statement %d: %w", result.StatementCount+1, err)
		}
		result.StatementCount++
		return nil
	}

	adminPasswordHash, err := hashSeedPassword("admin")
	if err != nil {
		return PGSeedResult{}, fmt.Errorf("hash seed admin password: %w", err)
	}
	if err := exec(pgSeedSystemAccountsInsert, "sys_admin", "admin", "超级管理员", "系统默认超级管理员账户", "super_admin", "active", adminPasswordHash, 1, 0, now, now); err != nil {
		return PGSeedResult{}, err
	}
	for _, setting := range pgSeedGlobalSettings {
		if err := exec(pgSeedGlobalSettingsInsert, setting.Key, setting.ValueJSON, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, hours := range pgSeedRequestQuotaHourlyWindowHours {
		if err := exec(pgSeedWindowConfigInsert, hours, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, cte := range []string{pgSeedQuotaScopeBindingsFromAPIKeysCTE, pgSeedQuotaScopeBindingsFromAuthorizationsCTE, pgSeedQuotaScopeBindingsFromTeamGrantsCTE} {
		if err := exec(cte); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, provider := range pgSeedProviders {
		if err := exec(pgSeedProviderInsert, provider.ID, provider.Code, provider.Name, provider.Description, provider.Enabled, provider.DefaultSupportedModelsJSON, now, now); err != nil {
			return PGSeedResult{}, err
		}
		if err := exec(pgSeedProviderDefaultModelsRepair, provider.DefaultSupportedModelsJSON, now, provider.Code); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, protocol := range pgSeedProtocols {
		if err := exec(pgSeedProtocolInsert, protocol.ID, protocol.Code, protocol.Version, protocol.Name, protocol.Description, protocol.Enabled, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, family := range pgSeedEndpointFamilies {
		if err := exec(pgSeedEndpointFamilyInsert, family.ID, family.ProtocolCode, family.ProtocolVersion, family.Code, family.Name, family.Description, family.Enabled, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, group := range pgSeedGroups {
		if err := exec(pgSeedGroupDefaultRepair, group.SystemAccountID, group.ID); err != nil {
			return PGSeedResult{}, err
		}
		defaultGroupIDPrefix := "grp_default_gpt_"
		if err := exec(pgSeedGroupInsert, group.SystemAccountID, group.ID, defaultGroupIDPrefix, group.Name, group.Name, group.Description, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	source := pgSeedExternalIntegrationSource
	if err := exec(pgSeedExternalIntegrationSourceInsert, source.ID, source.Name, source.ScopesJSON, source.RateLimitsJSON, source.Notes, now, now); err != nil {
		return PGSeedResult{}, err
	}
	if err := exec(pgSeedExternalIntegrationSourceUpdate, source.Name, source.ScopesJSON, source.RateLimitsJSON, source.Notes, now, source.ID); err != nil {
		return PGSeedResult{}, err
	}
	for _, setting := range pgSeedSystemSettings {
		if err := exec(pgSeedSystemSettingInsert, "sys_admin", setting.Key, setting.ValueJSON, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	return result, nil
}

// hashSeedPassword mirrors hashPassword in Node src/storage/crypto.ts
// (pbkdf2-sha512, 120000 iterations, base64url salt and digest). Node passes
// the base64url salt TEXT itself to pbkdf2Sync, so the derivation input is
// the UTF-8 bytes of the encoded salt — never the decoded raw bytes. The
// Go gateway verify path (modelcheckauth verifyNodePBKDF2Password) mirrors
// the same semantics.
func hashSeedPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	saltText := base64.RawURLEncoding.EncodeToString(salt)
	derived, err := pbkdf2.Key(sha512.New, password, []byte(saltText), pgSeedPasswordIterations, 32)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		"pbkdf2",
		"sha512",
		strconv.Itoa(pgSeedPasswordIterations),
		saltText,
		base64.RawURLEncoding.EncodeToString(derived),
	}, "$"), nil
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal seed json: %v", err))
	}
	return string(encoded)
}

// parseSeedStringArray decodes one JSON string array value.
func parseSeedStringArray(value string) ([]string, error) {
	var items []string
	if err := json.Unmarshal([]byte(value), &items); err != nil {
		return nil, err
	}
	return items, nil
}

// mergeSeedDistinct appends missing additions while preserving order.
func mergeSeedDistinct(current, additions []string) []string {
	seen := make(map[string]bool, len(current)+len(additions))
	merged := make([]string, 0, len(current)+len(additions))
	for _, item := range current {
		if !seen[item] {
			seen[item] = true
			merged = append(merged, item)
		}
	}
	for _, item := range additions {
		if !seen[item] {
			seen[item] = true
			merged = append(merged, item)
		}
	}
	return merged
}

func seedStringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
