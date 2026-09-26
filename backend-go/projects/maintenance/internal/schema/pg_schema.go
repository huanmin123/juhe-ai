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
// All statements are idempotent, so repeated calls are no-ops.
func EnsurePostgres(ctx context.Context, db *sql.DB) (PGResult, error) {
	createdSchemas := make(map[string]bool)
	for i, statement := range postgresSchemaStatements {
		if !createdSchemas[statement.SchemaName] {
			createdSchemas[statement.SchemaName] = true
			createSchema := fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quotePGIdentifier(statement.SchemaName))
			if _, err := db.ExecContext(ctx, createSchema); err != nil {
				return PGResult{}, fmt.Errorf("create postgres schema %s: %w", statement.SchemaName, err)
			}
		}
		execSQL := fmt.Sprintf("SET search_path TO %s, public;\n%s", quotePGIdentifier(statement.SchemaName), statement.SQL)
		if _, err := db.ExecContext(ctx, execSQL); err != nil {
			return PGResult{}, fmt.Errorf("postgres schema statement %d (%s/%s): %w", i, statement.SchemaName, statement.Source, err)
		}
	}
	return PGResult{SchemaCount: len(createdSchemas), StatementCount: len(postgresSchemaStatements)}, nil
}

// quotePGIdentifier mirrors quoteIdentifier in postgres-schema.ts.
func quotePGIdentifier(identifier string) string {
	return "\"" + strings.ReplaceAll(identifier, "\"", "\"\"") + "\""
}

// ---------- seeds (port of postgres-seed-defaults.ts) ----------

// PGSeedResult summarizes EnsurePostgresSeeds.
type PGSeedResult struct {
	StatementCount int
}

// pgSeedSystemAccountsInsert seeds the default super admin account.
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

// pgSeedProviderInsert seeds one providers row.
const pgSeedProviderInsert = `
        INSERT INTO "juhe_business"."providers" (
          id, code, name, description, parent_code, enabled, default_supported_models_json, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        ON CONFLICT DO NOTHING
      `

// pgSeedProviderDefaultModelsRepair refreshes empty provider default model lists.
const pgSeedProviderDefaultModelsRepair = `
        UPDATE "juhe_business"."providers"
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

// pgSeedProfileInsert seeds one provider_protocol_profiles row.
const pgSeedProfileInsert = `
        INSERT INTO "juhe_business"."provider_protocol_profiles" (
          id, provider_code, name, description, enabled, protocol_code, protocol_version,
          base_url, default_health_check_model, account_types_json, capabilities_json, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
        ON CONFLICT DO NOTHING
      `

// pgSeedProfileAccountTypesSelect reads one profile account_types_json for the repair step.
const pgSeedProfileAccountTypesSelect = `
        SELECT account_types_json
        FROM "juhe_business"."provider_protocol_profiles"
        WHERE id = $1
      `

// pgSeedProfileAccountTypesUpdate merges missing built-in account types into one profile (from repairBuiltInProviderProfileAccountTypes).
const pgSeedProfileAccountTypesUpdate = `
        UPDATE "juhe_business"."provider_protocol_profiles"
        SET account_types_json = $1, updated_at = $2
        WHERE id = $3
      `

// pgSeedProfileFamilyInsert seeds one provider_protocol_profile_families row.
const pgSeedProfileFamilyInsert = `
          INSERT INTO "juhe_business"."provider_protocol_profile_families" (
            profile_id, family_code, enabled, capabilities_json, created_at, updated_at
          ) VALUES ($1, $2, 1, '[]', $3, $4)
          ON CONFLICT DO NOTHING
        `

// pgSeedGroupDefaultRepair marks the built-in group as default when no default exists.
const pgSeedGroupDefaultRepair = `
        UPDATE "juhe_business"."groups" AS candidate
        SET is_default = 1
        WHERE candidate.provider_code = $1
          AND candidate.is_default = 0
          AND candidate.system_account_id = $2
          AND candidate.id = $3
          AND NOT EXISTS (
            SELECT 1
            FROM "juhe_business"."groups" AS existing_default
            WHERE existing_default.system_account_id = candidate.system_account_id
              AND existing_default.provider_code = candidate.provider_code
              AND existing_default.is_default = 1
          )
      `

// pgSeedGroupInsert inserts the built-in default group for every system account that lacks it.
const pgSeedGroupInsert = `
        INSERT INTO "juhe_business"."groups" (
          id, system_account_id, name, provider_code,
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
                AND same_name.provider_code = $4
                AND lower(same_name.name) = lower($5)
          ) THEN $6 || '（系统默认：' || system_accounts.id || CASE
            WHEN candidate_suffix.suffix = 0 THEN ''
            ELSE ' #' || candidate_suffix.suffix
          END || '）'
          ELSE $5
        END,
          $4, $7, 1, 1, $8, $9
        FROM "juhe_business"."system_accounts" AS system_accounts
        LEFT JOIN LATERAL (
          SELECT candidate_suffix.suffix
          FROM generate_series(
            0,
            (
              SELECT COUNT(*)
              FROM "juhe_business"."groups" AS fallback_name
              WHERE fallback_name.system_account_id = system_accounts.id
                AND fallback_name.provider_code = $4
                AND lower(fallback_name.name) LIKE lower($6) || '（系统默认：%）'
            )
          ) AS candidate_suffix(suffix)
          WHERE NOT EXISTS (
            SELECT 1
            FROM "juhe_business"."groups" AS existing_fallback_name
            WHERE existing_fallback_name.system_account_id = system_accounts.id
              AND existing_fallback_name.provider_code = $4
              AND lower(existing_fallback_name.name) = lower(
                $6 || '（系统默认：' || system_accounts.id || CASE
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
            AND existing_default.provider_code = $4
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

// pgSeedProvider mirrors DEFAULT_PROVIDER_SEEDS.
type pgSeedProvider struct {
	ID                         string
	Code                       string
	Name                       string
	Description                string
	ParentCode                 string
	Enabled                    int
	DefaultSupportedModelsJSON string
}

var pgSeedProviders = []pgSeedProvider{
	{
		ID:                         "openai",
		Code:                       "openai",
		Name:                       "OpenAI 兼容",
		Description:                "通用 OpenAI-compatible 供应商，用于接入兼容 OpenAI v1 协议的上游服务，默认只提供 API Key 透传能力",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gpt-6-sol\",\"gpt-6-luna\",\"gpt-6-astra\",\"gpt-5.6-terra\",\"gpt-5.6-sol\"]",
	},
	{
		ID:                         "gpt",
		Code:                       "gpt",
		Name:                       "GPT",
		Description:                "GPT 官方供应商，继承通用 OpenAI-compatible 能力，并启用 OAuth、Codex Responses 等 GPT 专属能力",
		ParentCode:                 "openai",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gpt-6-sol\",\"gpt-6-luna\",\"gpt-6-astra\",\"gpt-5.6-terra\",\"gpt-5.6-sol\"]",
	},
	{
		ID:                         "xai",
		Code:                       "xai",
		Name:                       "xAI / Grok",
		Description:                "xAI 官方供应商，支持 API Key 与 Grok OAuth 接入 OpenAI v1 文本协议",
		ParentCode:                 "openai",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"grok-4.7\",\"grok-4.6\",\"grok-4.5\"]",
	},
	{
		ID:                         "deepseek",
		Code:                       "deepseek",
		Name:                       "DeepSeek",
		Description:                "DeepSeek 官方供应商，支持 OpenAI-compatible v1 Chat Completions 与 Responses 直连，也支持 Anthropic v1 Messages 档案兼容 Claude Code",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"deepseek-flash\",\"deepseek-v4.1-flash\",\"deepseek-v4-pro\"]",
	},
	{
		ID:                         "anthropic",
		Code:                       "anthropic",
		Name:                       "Anthropic",
		Description:                "Anthropic 官方供应商，支持 API Key 或 OAuth Access Token（Bearer）接入 Anthropic Messages 原生协议",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"claude-opus-5-5\",\"claude-fable-5-1\",\"claude-opus-5\",\"claude-sonnet-5\"]",
	},
	{
		ID:                         "gemini",
		Code:                       "gemini",
		Name:                       "Gemini",
		Description:                "Google Gemini 官方供应商，支持 API Key 或 Google OAuth 接入 Gemini v1beta Generate Content 与 Interactions；OpenAI 客户端可使用兼容档案",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gemini-3.8-flash\",\"gemini-3.7-flash\",\"gemini-3.6-flash\",\"gemini-3.5-flash-lite\"]",
	},
	{
		ID:                         "glm",
		Code:                       "glm",
		Name:                       "智谱 GLM",
		Description:                "智谱 GLM 官方供应商，支持通用 GLM API Key、GLM Coding Plan OpenAI Chat 档案，以及 GLM Coding Anthropic v1 Messages 档案",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"glm-5.3\",\"glm-5.2\",\"glm-5.1\",\"glm-5\",\"glm-5-turbo\",\"glm-4.7-flashx\",\"glm-4.7-flash\"]",
	},
	{
		ID:                         "hybrid",
		Code:                       "hybrid",
		Name:                       "混合供应商",
		Description:                "混合供应商账户用于创建真实上游账户，并在账户内配置允许的下游协议入口和上游模型映射；不指向其他账户、分组或 API Key",
		ParentCode:                 "",
		Enabled:                    1,
		DefaultSupportedModelsJSON: "[\"gpt-6-sol\",\"claude-opus-5-5\",\"gemini-3.8-flash\",\"glm-5.3\"]",
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
}

// pgSeedProfile mirrors DEFAULT_PROVIDER_PROTOCOL_PROFILE_SEEDS. AccountTypes
// and Capabilities are kept as slices; the seed parameters are JSON-encoded
// from them exactly like the Node JSON.stringify calls.
type pgSeedProfile struct {
	ID                      string
	ProviderCode            string
	Name                    string
	Description             string
	Enabled                 int
	ProtocolCode            string
	ProtocolVersion         string
	BaseURL                 string
	DefaultHealthCheckModel string
	AccountTypes            []string
	Capabilities            []string
	EndpointFamilies        []string
}

var pgSeedProfiles = []pgSeedProfile{
	{
		ID:                      "profile_openai_openai_v1",
		ProviderCode:            "openai",
		Name:                    "OpenAI 兼容 / OpenAI v1",
		Description:             "通用 OpenAI-compatible 供应商的 OpenAI v1 协议档案，仅承载 API Key 透传、模型目录和通用协议策略",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.openai.com/v1",
		DefaultHealthCheckModel: "gpt-6-sol",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"responses", "chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_gpt_openai_v1",
		ProviderCode:            "gpt",
		Name:                    "GPT / OpenAI v1",
		Description:             "GPT 供应商的 OpenAI v1 协议档案，支持 OAuth 与 API Key 两种账户接入方式",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.openai.com/v1",
		DefaultHealthCheckModel: "gpt-6-sol",
		AccountTypes:            []string{"oauth", "api_key"},
		Capabilities:            []string{"responses", "chat"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_xai_openai_v1",
		ProviderCode:            "xai",
		Name:                    "xAI / OpenAI v1",
		Description:             "xAI 官方协议档案，支持 API Key 与 Grok OAuth，承载 OpenAI v1 Chat Completions 与 Responses 文本接口",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.x.ai/v1",
		DefaultHealthCheckModel: "grok-4.7",
		AccountTypes:            []string{"api_key", "oauth"},
		Capabilities:            []string{"responses", "chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_deepseek_anthropic_v1",
		ProviderCode:            "deepseek",
		Name:                    "DeepSeek / Anthropic v1",
		Description:             "DeepSeek 供应商的 Anthropic v1 Messages 协议档案，承载 Claude Code 使用的 /v1/messages 与 /v1/models 直连",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.deepseek.com/anthropic",
		DefaultHealthCheckModel: "deepseek-flash",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"messages", "models", "passthrough"},
		EndpointFamilies:        []string{"messages", "models"},
	},
	{
		ID:                      "profile_deepseek_openai_v1",
		ProviderCode:            "deepseek",
		Name:                    "DeepSeek / OpenAI v1",
		Description:             "DeepSeek 供应商的 OpenAI-compatible v1 协议档案，承载 API Key、Chat Completions、原生 Responses、DeepSeek 响应扩展字段与 Codex Responses 桥接",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.deepseek.com",
		DefaultHealthCheckModel: "deepseek-flash",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "responses", "passthrough"},
		EndpointFamilies:        []string{"chat_completions", "responses"},
	},
	{
		ID:                      "profile_anthropic_anthropic_v1",
		ProviderCode:            "anthropic",
		Name:                    "Anthropic / Anthropic v1",
		Description:             "Anthropic 官方协议档案，支持 API Key 或 OAuth Access Token，承载 anthropic-version 与 Messages 原生协议",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://api.anthropic.com/v1",
		DefaultHealthCheckModel: "claude-opus-5-5",
		AccountTypes:            []string{"api_key", "oauth"},
		Capabilities:            []string{"messages", "models", "count_tokens", "passthrough"},
		EndpointFamilies:        []string{"messages", "models", "message_token_counting"},
	},
	{
		ID:                      "profile_gemini_openai_chat_v1beta",
		ProviderCode:            "gemini",
		Name:                    "Gemini / OpenAI Chat",
		Description:             "Gemini 官方 OpenAI Chat Completions 兼容档案，仅用于 OpenAI Chat 直连和 Codex Responses 显式模型映射，不承载 Gemini 原生协议",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://generativelanguage.googleapis.com/v1beta/openai",
		DefaultHealthCheckModel: "gemini-3.8-flash",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions"},
	},
	{
		ID:                      "profile_gemini_native_v1beta",
		ProviderCode:            "gemini",
		Name:                    "Gemini / Gemini v1beta",
		Description:             "Gemini 官方 API Key 协议档案，承载 x-goog-api-key 与 Gemini v1beta 原生协议直连",
		Enabled:                 1,
		ProtocolCode:            "gemini",
		ProtocolVersion:         "v1beta",
		BaseURL:                 "https://generativelanguage.googleapis.com",
		DefaultHealthCheckModel: "gemini-3.8-flash",
		AccountTypes:            []string{"api_key", "google_oauth"},
		Capabilities:            []string{"generate_content", "stream_generate_content", "count_tokens", "embed_content", "interactions", "models", "passthrough"},
		EndpointFamilies:        []string{"models", "generate_content", "stream_generate_content", "count_tokens", "embed_content", "interactions"},
	},
	{
		ID:                      "profile_glm_coding_openai_v1",
		ProviderCode:            "glm",
		Name:                    "智谱 GLM Coding / OpenAI Chat",
		Description:             "智谱 GLM Coding Plan Key 协议档案，使用 Coding Plan OpenAI Chat Completions 兼容端点",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://open.bigmodel.cn/api/coding/paas/v4",
		DefaultHealthCheckModel: "glm-5.3",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions"},
	},
	{
		ID:                      "profile_glm_coding_anthropic_v1",
		ProviderCode:            "glm",
		Name:                    "智谱 GLM Coding / Anthropic v1",
		Description:             "智谱 GLM Coding Plan Key 的 Anthropic v1 Messages 协议档案，面向 Anthropic Messages 客户端直连",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://open.bigmodel.cn/api/anthropic",
		DefaultHealthCheckModel: "glm-5.3",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"messages", "models", "passthrough"},
		EndpointFamilies:        []string{"messages", "models"},
	},
	{
		ID:                      "profile_glm_general_openai_v1",
		ProviderCode:            "glm",
		Name:                    "智谱 GLM 通用 / OpenAI Chat",
		Description:             "智谱通用 GLM API Key 协议档案，使用智谱 OpenAI Chat Completions 兼容端点",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "https://open.bigmodel.cn/api/paas/v4/",
		DefaultHealthCheckModel: "glm-5.3",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "passthrough"},
		EndpointFamilies:        []string{"chat_completions"},
	},
	{
		ID:                      "profile_hybrid_openai_chat_v1",
		ProviderCode:            "hybrid",
		Name:                    "混合供应商",
		Description:             "混合供应商通用 API Key 档案；真实上游 Base URL 和目标协议由账户模型映射显式声明",
		Enabled:                 1,
		ProtocolCode:            "openai",
		ProtocolVersion:         "v1",
		BaseURL:                 "",
		DefaultHealthCheckModel: "",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"chat", "responses", "messages", "generate_content", "stream_generate_content", "bridge"},
		EndpointFamilies:        []string{"chat_completions", "responses", "messages", "generate_content", "stream_generate_content"},
	},
	{
		ID:                      "profile_hybrid_anthropic_messages_v1",
		ProviderCode:            "hybrid",
		Name:                    "混合供应商 Anthropic Messages",
		Description:             "混合供应商 Anthropic Messages API Key 档案；下游协议由账户模型映射显式声明",
		Enabled:                 1,
		ProtocolCode:            "anthropic",
		ProtocolVersion:         "v1",
		BaseURL:                 "",
		DefaultHealthCheckModel: "",
		AccountTypes:            []string{"api_key"},
		Capabilities:            []string{"messages", "bridge"},
		EndpointFamilies:        []string{"messages"},
	},
}

// pgSeedGroup mirrors DEFAULT_BUILT_IN_GROUPS.
type pgSeedGroup struct {
	ID, SystemAccountID, Name, ProviderCode, Description string
}

var pgSeedGroups = []pgSeedGroup{
	{ID: "grp_default_openai_sys_admin", SystemAccountID: "sys_admin", Name: "默认 OpenAI 兼容分组", ProviderCode: "openai", Description: ""},
	{ID: "grp_default_gpt_sys_admin", SystemAccountID: "sys_admin", Name: "默认 GPT 分组", ProviderCode: "gpt", Description: ""},
	{ID: "grp_default_xai_sys_admin", SystemAccountID: "sys_admin", Name: "默认 xAI 分组", ProviderCode: "xai", Description: ""},
	{ID: "grp_default_deepseek_sys_admin", SystemAccountID: "sys_admin", Name: "默认 DeepSeek 分组", ProviderCode: "deepseek", Description: ""},
	{ID: "grp_default_anthropic_sys_admin", SystemAccountID: "sys_admin", Name: "默认 Anthropic 分组", ProviderCode: "anthropic", Description: ""},
	{ID: "grp_default_gemini_sys_admin", SystemAccountID: "sys_admin", Name: "默认 Gemini 分组", ProviderCode: "gemini", Description: ""},
	{ID: "grp_default_glm_sys_admin", SystemAccountID: "sys_admin", Name: "默认 GLM 分组", ProviderCode: "glm", Description: ""},
	{ID: "grp_default_hybrid_openai_chat_sys_admin", SystemAccountID: "sys_admin", Name: "默认混合供应商分组", ProviderCode: "hybrid", Description: "混合供应商账户保存真实上游凭据和 Base URL，允许账户内配置跨协议入口映射"},
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
	{Key: "chatImageGenerationTotalTimeoutSeconds", ValueJSON: "900"},
	{Key: "noAvailableAccountWaitTimeoutSeconds", ValueJSON: "270"},
	{Key: "streamFailureThresholdCount", ValueJSON: "3"},
	{Key: "streamFailureThresholdWindowMinutes", ValueJSON: "5"},
	{Key: "operationLogRetentionDays", ValueJSON: "365"},
	{Key: "operationLogMaxChangesPerRecord", ValueJSON: "100"},
	{Key: "statsAggregationIntervalSeconds", ValueJSON: "60"},
	{Key: "statsAggregationBatchSize", ValueJSON: "2000"},
	{Key: "statsAggregationMaxBatchesPerRun", ValueJSON: "5"},
	{Key: "usageHotWindowRefreshIntervalSeconds", ValueJSON: "600"},
	{Key: "groupAccountStatsRefreshIntervalSeconds", ValueJSON: "60"},
	{Key: "systemMetricsSampleIntervalSeconds", ValueJSON: "30"},
	{Key: "tableMonitorMaxTablesPerRun", ValueJSON: "4"},
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
	{Key: "systemMetricsRetentionDays", ValueJSON: "7"},
	{Key: "systemMetricsHourlyRetentionDays", ValueJSON: "30"},
}

// pgSeedKeyValue is one key -> JSON-encoded value pair.
type pgSeedKeyValue struct {
	Key       string
	ValueJSON string
}

// EnsurePostgresSeeds applies the portable subset of seedPostgresDefaults in
// Node statement order: default admin account, global settings, quota window
// configs, quota scope binding backfills, providers, protocols, endpoint
// families, provider protocol profiles (with account-type repair), profile
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
	if err := exec(pgSeedSystemAccountsInsert, "sys_admin", "admin", "超级管理员", "系统默认超级管理员账户", "super_admin", "active", adminPasswordHash, 0, 0, now, now); err != nil {
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
		if err := exec(pgSeedProviderInsert, provider.ID, provider.Code, provider.Name, provider.Description, pgNullableText(provider.ParentCode), provider.Enabled, provider.DefaultSupportedModelsJSON, now, now); err != nil {
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
	nowTime, err := time.Parse("2006-01-02T15:04:05.000Z07:00", now)
	if err != nil {
		return PGSeedResult{}, fmt.Errorf("parse seed timestamp: %w", err)
	}
	for index, profile := range pgSeedProfiles {
		profileUpdatedAt := nowTime.Add(time.Duration(index) * time.Millisecond).UTC().Format("2006-01-02T15:04:05.000Z07:00")
		if err := exec(pgSeedProfileInsert, profile.ID, profile.ProviderCode, profile.Name, profile.Description, profile.Enabled, profile.ProtocolCode, profile.ProtocolVersion, profile.BaseURL, profile.DefaultHealthCheckModel, mustJSON(profile.AccountTypes), mustJSON(profile.Capabilities), now, profileUpdatedAt); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, profile := range pgSeedProfiles {
		var accountTypesJSON string
		err := db.QueryRowContext(ctx, pgSeedProfileAccountTypesSelect, profile.ID).Scan(&accountTypesJSON)
		if err != nil {
			continue // missing row or non-text value mirrors the Node repair skip
		}
		current, err := parseSeedStringArray(accountTypesJSON)
		if err != nil {
			continue
		}
		merged := mergeSeedDistinct(current, profile.AccountTypes)
		if seedStringSlicesEqual(merged, current) {
			continue
		}
		if err := exec(pgSeedProfileAccountTypesUpdate, mustJSON(merged), now, profile.ID); err != nil {
			return PGSeedResult{}, err
		}
	}
	for _, profile := range pgSeedProfiles {
		for _, familyCode := range profile.EndpointFamilies {
			if err := exec(pgSeedProfileFamilyInsert, profile.ID, familyCode, now, now); err != nil {
				return PGSeedResult{}, err
			}
		}
	}
	for _, group := range pgSeedGroups {
		if err := exec(pgSeedGroupDefaultRepair, group.ProviderCode, group.SystemAccountID, group.ID); err != nil {
			return PGSeedResult{}, err
		}
		defaultGroupIDPrefix := "grp_default_" + group.ProviderCode + "_"
		if err := exec(pgSeedGroupInsert, group.SystemAccountID, group.ID, defaultGroupIDPrefix, group.ProviderCode, group.Name, group.Name, group.Description, now, now); err != nil {
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

// pgNullableText maps the empty string to a SQL NULL like the Node seeds do
// for optional provider parent codes.
func pgNullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
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
