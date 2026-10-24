// Complete port of Node backend/src/storage/postgres-seed-defaults.ts
// (seedPostgresDefaults and its private helpers), converged to the 供应商与
// 上游协议面重构 target schema: vendors（9 真实厂商，无 parent_code）、
// groups（无供应商维度）、models（provider_model_catalog/custom_provider_
// models 的唯一物理表，目录快照经 modelSeedTargetRows 投影后 bulk upsert，
// 过期行按 ShutdownDate 写 'disabled'）。provider_protocol_profiles 及其
// family 绑定 seed 随旧表退役（§1.1 连接器注册表接管）；旧 providers /
// profiles / family 绑定种子与旧 provider_model_catalog 目录种子的迁移窗口
// shim 已按 A 路线一次性收敛撤除（设计 §9 第 11 条：删除所有旧字段和旧
// seed，不提供兼容分支）。custom 模型同名清理随 custom_provider_models 表
// 退役（同名冲突改由 models 身份唯一索引与应用层写事务承接）。pg_schema.go
// already carried the portable subset (EnsurePostgresSeeds); this file
// completes it with the pieces the
// pg_schema.go header lists as deliberately omitted:
//
//   - the models bulk upsert (212 rows x 40 parameters, the static rows are
//     generated into model_catalog_data_gen.go from the gateway pricing
//     catalog) and the stale built-in model disable with reference guards,
//   - default route strategies / default API keys for the single built-in
//     default group (createApiKey + hashSecret + encryptJson); the admin chat
//     API key port was removed with the 2026-10-07 默认资源收口 (the gateway
//     chat-session ensure chain provisions it on demand),
//   - the external integration source token create/update.
//
// Statement order and idempotency mirror Node exactly: every INSERT uses ON
// CONFLICT DO NOTHING, every UPDATE is the same guarded repair Node re-applies
// on every run, so with a pinned SeedOptions.Now repeated calls leave
// identical rows. Unlike EnsurePostgresSeeds this function is the full seed
// and takes the injected clock.
//
// SeedPostgresDefaults targets a database where applyPostgresSchema
// (EnsurePostgres) already ran, mirroring the Node init script
// backend/src/scripts/maintenance/init-postgres-schema.ts
// (applyPostgresSchema -> seedPostgresDefaults).

package schema

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
	"github.com/huanminabc/juhe-ai/backend-go-platform/schedprefdomain"
)

// postgresSeedClient is the SQL surface seedPostgresDefaults needs; *sql.DB
// satisfies it and tests can capture statements instead of opening a server.
type postgresSeedClient interface {
	seedExecutor
	seedRowQuerier
}

// SeedPostgresDefaults ports Node seedPostgresDefaults in full. See the file
// header for the relationship with EnsurePostgresSeeds (the portable subset).
func SeedPostgresDefaults(ctx context.Context, db *sql.DB, options SeedOptions) (PGSeedResult, error) {
	return seedPostgresDefaults(ctx, db, options)
}

func seedPostgresDefaults(ctx context.Context, client postgresSeedClient, options SeedOptions) (PGSeedResult, error) {
	now := seedTimestamp(options.nowTime())
	var result PGSeedResult
	exec := func(query string, args ...any) error {
		if _, err := client.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("postgres seed statement %d: %w", result.StatementCount+1, err)
		}
		result.StatementCount++
		return nil
	}

	adminPasswordHash, err := hashSeedPassword("admin")
	if err != nil {
		return PGSeedResult{}, fmt.Errorf("hash seed admin password: %w", err)
	}
	// must_change_password = 1: BUG-0224, see the adjudication note on
	// pgSeedSystemAccountsInsert in pg_schema.go.
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

	// Node (SQLite seed-defaults.ts): strip codex-auto-review from the GPT
	// vendor default model list. PG 老库同样可能残留该已退役模型：上面的
	// pgSeedProviderInsert 是 ON CONFLICT DO NOTHING，老行原样保留，而
	// pgSeedProviderDefaultModelsRepair 只修复空列表。清洗语句与执行顺序
	// 对齐 SQLite 种子（vendor 循环后、models 目录 upsert 之前）。
	if err := seedPostgresGPTVendorCodexAutoReviewRemoval(ctx, client, exec, now); err != nil {
		return PGSeedResult{}, err
	}

	// 内置模型目录：models bulk upsert（含 ShutdownDate 已过的 disabled 行）
	// 加受引用守卫保护的 stale builtin disable。
	if err := seedPostgresModelCatalog(ctx, client, exec, options, now, &result); err != nil {
		return PGSeedResult{}, err
	}

	// 系统内置题库题（模型检测设计 §5.7 系统内置题）：固定 id 幂等预置。
	if err := seedPostgresBuiltinModelCheckQuestions(ctx, exec, now, &result); err != nil {
		return PGSeedResult{}, err
	}

	// 内置调度偏好（调度偏好独立实体 PLAN-20261012T033000000Z）：固定 id
	// 恢复式幂等预置（与 --ensure-schema 补种臂共用
	// pgSeedBuiltinSchedulingPreferenceUpsert，权威值单源于
	// scheduling_preference.go）。
	if err := exec(pgSeedBuiltinSchedulingPreferenceUpsert,
		BuiltinSchedulingPreferenceID, BuiltinSchedulingPreferenceName, now, now); err != nil {
		return PGSeedResult{}, err
	}

	// 全新空库自动写调度偏好迁移完成记录（调度偏好独立实体 P2 设计门裁决②）：
	// 与 SQLite 臂同语义——迁移记录不存在且 api_keys 无行（本时点在默认路由
	// 策略/Key 预置之前）时原子写入 completed 记录（counts 全零）；记录已存在
	// 或 api_keys 非空的库一律跳过（存量库由 --migrate-scheduling-preferences
	// 负责，见 internal/schedprefmigration）。
	sealed, err := schedprefdomain.SealFreshInstall(ctx, client, capabilitydomain.DialectPostgres)
	if err != nil {
		return PGSeedResult{}, fmt.Errorf("postgres seed seal scheduling preferences fresh install: %w", err)
	}
	result.SchedulingPreferenceMigrationSealed = sealed

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
		if err := exec(pgSeedGroupInsert, group.SystemAccountID, group.ID, "grp_default_gpt_", group.Name, group.Name, group.Description, now, now); err != nil {
			return PGSeedResult{}, err
		}
	}

	// Node: seedAdminDefaultRouteStrategiesAndApiKeys, seedBuiltInExternalIntegrationTestToken
	// (with the token-free source row insert/update at its head). The admin
	// chat API key seeding was removed with the 2026-10-07 默认资源收口 —
	// AI chat sessions provision it on demand.
	if err := seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx, client, exec, options, now); err != nil {
		return PGSeedResult{}, err
	}
	if err := seedPostgresExternalIntegrationTestToken(ctx, client, exec, options, now); err != nil {
		return PGSeedResult{}, err
	}

	for _, setting := range pgSeedSystemSettings {
		if err := exec(pgSeedSystemSettingInsert, "sys_admin", setting.Key, setting.ValueJSON, now); err != nil {
			return PGSeedResult{}, err
		}
	}
	return result, nil
}

// pgSeedGPTVendorCodexAutoReviewRemovalSelect reads the GPT vendor default
// model list for the codex-auto-review repair.
const pgSeedGPTVendorCodexAutoReviewRemovalSelect = `
        SELECT default_supported_models_json
        FROM "juhe_business"."vendors"
        WHERE code = $1
        LIMIT 1
      `

// pgSeedGPTVendorCodexAutoReviewRemovalUpdate writes the repaired default
// model list back to the GPT vendor row.
const pgSeedGPTVendorCodexAutoReviewRemovalUpdate = `
        UPDATE "juhe_business"."vendors"
        SET default_supported_models_json = $1, updated_at = $2
        WHERE code = $3
      `

// pgSeedBuiltinQuestionUpsert 预置一道系统内置题：固定 id，冲突时仅在
// 目标行仍是内置行时把题面字段恢复为 seed 权威值——应用层禁止编辑内置
// 题，因此该 upsert 要么插入新行、要么把被人工改动的内置行拉回版本值，
// 题面修订得以随版本到达存量环境；updated_at 不在更新列内，重复 seed
// 对已一致的行零写入。
const pgSeedBuiltinQuestionUpsert = `
        INSERT INTO "juhe_business"."model_check_question_bank"
          (id,title,title_norm,question_text,reference_answer,key_points_json,status,reject_reason,created_by,created_scope,is_builtin,reviewed_by,reviewed_at,created_at,updated_at)
        VALUES ($1,$2,$3,$4,$5,$6,'approved',NULL,'system','system',1,NULL,NULL,$7,$7)
        ON CONFLICT (id) DO UPDATE SET
          title = EXCLUDED.title,
          title_norm = EXCLUDED.title_norm,
          question_text = EXCLUDED.question_text,
          reference_answer = EXCLUDED.reference_answer,
          key_points_json = EXCLUDED.key_points_json
          WHERE "juhe_business"."model_check_question_bank".is_builtin = 1
      `

// seedPostgresBuiltinModelCheckQuestions 预置系统内置题库题（模型检测设计
// §5.7）：approved + is_builtin=1，供用户直接选用。
func seedPostgresBuiltinModelCheckQuestions(ctx context.Context, exec func(string, ...any) error, now string, result *PGSeedResult) error {
	for _, question := range builtinModelCheckQuestions {
		keyPointsJSON, err := builtinQuestionKeyPointsJSON(question)
		if err != nil {
			return err
		}
		if err := exec(pgSeedBuiltinQuestionUpsert,
			question.ID,
			question.Title,
			builtinQuestionTitleNorm(question.Title),
			question.QuestionText,
			question.ReferenceAnswer,
			keyPointsJSON,
			now,
		); err != nil {
			return fmt.Errorf("postgres seed statement %d (builtin question %s): %w", result.StatementCount+1, question.ID, err)
		}
		result.BuiltinQuestionRows++
	}
	return nil
}

// seedPostgresGPTVendorCodexAutoReviewRemoval ports the SQLite
// sqSeedGPTVendorCodexAutoReviewRemoval repair (Node seed-defaults.ts) to the
// PG seed path: old databases may carry the retired model because
// pgSeedProviderInsert is ON CONFLICT DO NOTHING and
// pgSeedProviderDefaultModelsRepair only refreshes empty lists.
// default_supported_models_json is a text column, so the SQLite guards
// (json_valid + json_type='array') translate into a read-parse-filter round
// trip: a stored value that is not a JSON string array skips the repair
// (parse error ⇔ json_valid=false / type mismatch) instead of risking an
// unguarded JSON cast inside SQL. Unlike the SQLite statement (which rewrites
// the row on every run), a list without the retired model is left untouched,
// keeping repeated seeds value-stable without updated_at churn.
func seedPostgresGPTVendorCodexAutoReviewRemoval(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, now string) error {
	var rawList sql.NullString
	err := client.QueryRowContext(ctx, pgSeedGPTVendorCodexAutoReviewRemovalSelect, gptVendorCode).Scan(&rawList)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("postgres seed select gpt default models: %w", err)
	}
	if !rawList.Valid {
		return nil
	}
	models, err := parseSeedStringArray(rawList.String)
	if err != nil {
		// json_valid=false / json_type<>'array' 等价：无法解析成字符串数组的
		// 老值不修，避免把未知内容改写成种子清单。
		return nil
	}
	kept := make([]string, 0, len(models))
	removed := false
	for _, model := range models {
		if model == retiredCodexAutoReviewModel {
			removed = true
			continue
		}
		kept = append(kept, model)
	}
	if !removed {
		return nil
	}
	return exec(pgSeedGPTVendorCodexAutoReviewRemovalUpdate, seedStringify(kept), now, gptVendorCode)
}

// pgSeedModelsColumns lists the models bulk upsert target columns: 48 列，与
// buildPostgresModelSeedUpsert 每行渲染的 40 参数 + 8 字面量一一对应；
// scope='builtin'、system_account_id=NULL、pricing_notes/capability_notes/
// notes/created_by/updated_by 五个备注/操作者 NULL 列、currency='USD' 是语句
// 内字面量（设计 §3 列集）。
const pgSeedModelsColumns = `
        INSERT INTO "juhe_business"."models" (
          id, vendor_code, public_model, upstream_model, scope, system_account_id, status,
          catalog_visible, catalog_order, release_date, shutdown_date,
          supported_endpoint_modes_json, supported_service_tiers_json, supported_reasoning_efforts_json,
          default_reasoning_effort, codex_supported_reasoning_levels_json, codex_default_reasoning_level,
          codex_multi_agent_version, context_window_tokens, max_input_tokens, max_output_tokens, max_tokens,
          supports_prompt_caching, source, pricing_notes, capability_notes, notes, created_by, updated_by,
          created_at, updated_at,
          input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
          cache_write_1h_usd_per_1m, cache_storage_usd_per_1m_per_hour, service_tier_prices_json,
          long_context_input_token_threshold, long_context_input_token_threshold_inclusive,
          long_context_input_cost_multiplier, long_context_output_cost_multiplier,
          image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
          output_usd_per_image, currency
        ) VALUES
          `

// pgSeedModelsUpsertSuffix 是身份唯一索引 idx_models_identity_unique 上的
// manual-override 感知 ON CONFLICT 子句（目标形态承接旧 provider_model_catalog
// upsert 语义：manual-override / manual-visibility-override 行保留人工状态与
// 目录可见性，其余列按 seed 权威值重放收敛）。
const pgSeedModelsUpsertSuffix = `
        ON CONFLICT (coalesce(vendor_code, ''), scope, coalesce(system_account_id, ''), upstream_model) DO UPDATE SET
          status = CASE
            WHEN "juhe_business"."models".source IN ('manual-override', 'manual-visibility-override')
            THEN "juhe_business"."models".status
            ELSE excluded.status
          END,
          public_model = excluded.public_model,
          catalog_visible = CASE
            WHEN "juhe_business"."models".source IN ('manual-override', 'manual-visibility-override')
            THEN "juhe_business"."models".catalog_visible AND excluded.catalog_visible
            ELSE excluded.catalog_visible
          END,
          catalog_order = excluded.catalog_order,
          release_date = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".release_date ELSE excluded.release_date END,
          shutdown_date = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".shutdown_date ELSE excluded.shutdown_date END,
          supported_endpoint_modes_json = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".supported_endpoint_modes_json ELSE excluded.supported_endpoint_modes_json END,
          supported_service_tiers_json = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".supported_service_tiers_json ELSE excluded.supported_service_tiers_json END,
          supported_reasoning_efforts_json = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".supported_reasoning_efforts_json ELSE excluded.supported_reasoning_efforts_json END,
          default_reasoning_effort = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".default_reasoning_effort ELSE excluded.default_reasoning_effort END,
          codex_supported_reasoning_levels_json = excluded.codex_supported_reasoning_levels_json,
          codex_default_reasoning_level = excluded.codex_default_reasoning_level,
          codex_multi_agent_version = excluded.codex_multi_agent_version,
          context_window_tokens = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".context_window_tokens ELSE excluded.context_window_tokens END,
          max_input_tokens = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".max_input_tokens ELSE excluded.max_input_tokens END,
          max_output_tokens = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".max_output_tokens ELSE excluded.max_output_tokens END,
          max_tokens = excluded.max_tokens,
          input_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".input_usd_per_1m ELSE excluded.input_usd_per_1m END,
          output_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".output_usd_per_1m ELSE excluded.output_usd_per_1m END,
          cached_input_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".cached_input_usd_per_1m ELSE excluded.cached_input_usd_per_1m END,
          cache_write_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".cache_write_usd_per_1m ELSE excluded.cache_write_usd_per_1m END,
          cache_write_1h_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".cache_write_1h_usd_per_1m ELSE excluded.cache_write_1h_usd_per_1m END,
          cache_storage_usd_per_1m_per_hour = CASE
            WHEN "juhe_business"."models".source = 'manual-override'
            THEN coalesce("juhe_business"."models".cache_storage_usd_per_1m_per_hour, excluded.cache_storage_usd_per_1m_per_hour)
            ELSE excluded.cache_storage_usd_per_1m_per_hour
          END,
          service_tier_prices_json = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".service_tier_prices_json ELSE excluded.service_tier_prices_json END,
          long_context_input_token_threshold = excluded.long_context_input_token_threshold,
          long_context_input_token_threshold_inclusive = excluded.long_context_input_token_threshold_inclusive,
          long_context_input_cost_multiplier = excluded.long_context_input_cost_multiplier,
          long_context_output_cost_multiplier = excluded.long_context_output_cost_multiplier,
          image_input_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".image_input_usd_per_1m ELSE excluded.image_input_usd_per_1m END,
          image_output_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".image_output_usd_per_1m ELSE excluded.image_output_usd_per_1m END,
          audio_input_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".audio_input_usd_per_1m ELSE excluded.audio_input_usd_per_1m END,
          audio_output_usd_per_1m = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".audio_output_usd_per_1m ELSE excluded.audio_output_usd_per_1m END,
          output_usd_per_image = CASE WHEN "juhe_business"."models".source = 'manual-override' THEN "juhe_business"."models".output_usd_per_image ELSE excluded.output_usd_per_image END,
          supports_prompt_caching = excluded.supports_prompt_caching,
          source = CASE
            WHEN "juhe_business"."models".source IN ('manual-override', 'manual-visibility-override')
            THEN "juhe_business"."models".source
            ELSE excluded.source
          END,
          updated_at = excluded.updated_at
      `

// pgSeedStaleBuiltInModelsDisable 是内置过期模型回收（沿用原 provider_model_
// catalog stale disable 语义，守卫换算到 models 目标形态）：快照外的 builtin
// 行停用并隐藏，manual 覆盖行与仍被支持关系 / 入口映射 / 账户健康检查引用的
// 行不动。accounts.health_check_model 本批保留文本列（账户形态第 3 步切片才
// 切 model_id），按 upstream_model 名匹配保持旧守卫语义。
const pgSeedStaleBuiltInModelsDisable = `
    UPDATE "juhe_business"."models" AS stale_model
    SET status = 'disabled', catalog_visible = false, updated_at = $1
    WHERE stale_model.scope = 'builtin'
      AND stale_model.source NOT IN ('manual-override', 'manual-visibility-override')
      AND NOT EXISTS (
        SELECT 1
        FROM jsonb_to_recordset($2::jsonb) AS built_in(vendor_code text, upstream_model text)
        WHERE built_in.vendor_code = stale_model.vendor_code
          AND built_in.upstream_model = stale_model.upstream_model
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."account_supported_models" AS supported_model
        WHERE supported_model.model_id = stale_model.id
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."account_entry_mappings" AS entry_mapping
        WHERE entry_mapping.target_model_id = stale_model.id
      )
      AND NOT EXISTS (
        SELECT 1
        FROM "juhe_business"."accounts" AS account
        WHERE account.health_check_model = stale_model.upstream_model
      )
  `

// pgSeedAdminRouteStrategyInsert seeds one admin default route strategy.
const pgSeedAdminRouteStrategyInsert = `
        INSERT INTO "juhe_business"."route_strategies" (
          id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
        ) VALUES ($1, 'sys_admin', $2, $3, 'normal', 'active', 1, NULL, $4, $5)
        ON CONFLICT DO NOTHING
      `

// pgSeedAdminRouteStrategySelect re-reads the strategy after the insert.
const pgSeedAdminRouteStrategySelect = `
        SELECT id
        FROM "juhe_business"."route_strategies"
        WHERE id = $1
        LIMIT 1
      `

// pgSeedAdminRouteStrategyGroupBindingInsert seeds one strategy-group binding.
const pgSeedAdminRouteStrategyGroupBindingInsert = `
        INSERT INTO "juhe_business"."route_strategy_groups" (
          id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
        ) VALUES ($1, $2, 'sys_admin', $3, 1, 1, 'active', $4, $5)
        ON CONFLICT DO NOTHING
      `

// pgSeedAdminExistingDefaultAPIKeySelect finds an existing default API key.
const pgSeedAdminExistingDefaultAPIKeySelect = `
        SELECT id
        FROM "juhe_business"."api_keys"
        WHERE route_strategy_id = $1 AND is_default = 1
        LIMIT 1
      `

// pgSeedAdminDefaultAPIKeyInsert seeds one admin default API key. 调度偏好独立
// 实体 P1 起普通 Key 创建缺省持久化内置固定 ID（接口契约「调度偏好」章五写
// 入口矩阵），seed 预置的默认 Key 与该契约同形——内置行由本函数更早的
// pgSeedBuiltinSchedulingPreferenceUpsert 预置；P1 之前已存在的默认 Key 不受
// 影响（ON CONFLICT DO NOTHING 跳过，存量 NULL 由
// --migrate-scheduling-preferences 回填）。
const pgSeedAdminDefaultAPIKeyInsert = `
        INSERT INTO "juhe_business"."api_keys" (
          id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
          key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
          availability_schedule_next_check_at, scheduling_preference_id, created_at, updated_at
        ) VALUES ($1, 'sys_admin', $2, $3, $4, $5, $6, $7, $8, 'active', 1, NULL, NULL, NULL, NULL, 'schpref-builtin-chat-cost-first', $9, $10)
        ON CONFLICT DO NOTHING
      `

// pgSeedAdminDefaultGroupSelect picks the owner default group per built-in
// group seed (PostgreSQL orders by updated_at DESC, unlike the SQLite variant);
// 判定只按 system_account_id + is_default=1，无供应商维度.
const pgSeedAdminDefaultGroupSelect = `
        SELECT id, name
        FROM "juhe_business"."groups"
        WHERE system_account_id = $1
          AND is_default = 1
        ORDER BY updated_at DESC, id ASC
        LIMIT 1
      `

// pgSeedExternalIntegrationTokenSelect reads the built-in token id.
const pgSeedExternalIntegrationTokenSelect = `
      SELECT id
      FROM "juhe_business"."external_integration_source_tokens"
      WHERE id = $1
    `

// pgSeedExternalIntegrationTokenInsert creates the built-in token.
const pgSeedExternalIntegrationTokenInsert = `
        INSERT INTO "juhe_business"."external_integration_source_tokens" (
          id, source_ref_id, name, token_hash, token_secret_encrypted, token_prefix, token_suffix, status, scopes_json, expires_at, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, 'active', $8, NULL, $9, $10)
      `

// pgSeedExternalIntegrationTokenUpdate repairs the built-in token metadata.
const pgSeedExternalIntegrationTokenUpdate = `
      UPDATE "juhe_business"."external_integration_source_tokens"
      SET source_ref_id = $1,
          name = $2,
          scopes_json = $3,
          expires_at = NULL,
          updated_at = $4
      WHERE id = $5
    `

// pgSeedBuiltInModelKey mirrors one jsonb_to_recordset row of the stale
// disable parameter（models 目标形态键：vendor_code + upstream_model，即设计
// §3 身份唯一索引的 builtin 投影）。
type pgSeedBuiltInModelKey struct {
	VendorCode    string `json:"vendor_code"`
	UpstreamModel string `json:"upstream_model"`
}

// 旧 pgSeedCustomModelOverlapCleanup（目录 upsert 后删除与内置行同名的
// custom_provider_models 存量行）随 custom_provider_models 表退役删除：目标
// 形态下自定义模型落在 models 的 global/personal scope，与 builtin 行的身份
// 唯一索引天然不冲突；「自定义模型不得与内置同名」的运行时约束由应用层写
// 事务（HTTP 409 account_public_model_conflict 等，设计 §3/§4）承接，seed
// 不删除用户自定义数据。

// seedPostgresModelCatalog renders the models bulk upsert（全量目录行，含
// ShutdownDate 已过写 'disabled' 的行）plus the guarded stale disable,
// returning those statement counts.
func seedPostgresModelCatalog(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, options SeedOptions, now string, result *PGSeedResult) error {
	asOfUTCDate := seedTimestamp(options.nowTime())[:10]
	rows, err := modelSeedTargetRows(asOfUTCDate)
	if err != nil {
		return fmt.Errorf("postgres seed model catalog projection: %w", err)
	}
	if len(rows) > 0 {
		query, args := buildPostgresModelSeedUpsert(rows, now)
		if _, err := client.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("postgres seed statement %d (models bulk upsert): %w", result.StatementCount+1, err)
		}
		result.StatementCount++
	}
	builtIn := make([]pgSeedBuiltInModelKey, 0, len(rows))
	for _, row := range rows {
		builtIn = append(builtIn, pgSeedBuiltInModelKey{VendorCode: row.VendorCode, UpstreamModel: row.UpstreamModel})
	}
	builtInJSON, err := seedJSONStringify(builtIn)
	if err != nil {
		return err
	}
	staleResult, err := client.ExecContext(ctx, pgSeedStaleBuiltInModelsDisable, now, string(builtInJSON))
	if err != nil {
		return fmt.Errorf("postgres seed statement %d (stale built-in model disable): %w", result.StatementCount+1, err)
	}
	// Node statementCount += staleBuiltInModels.changes.
	changes, err := staleResult.RowsAffected()
	if err != nil {
		return fmt.Errorf("postgres seed stale disable rows affected: %w", err)
	}
	result.StatementCount += int(changes)
	return nil
}

// buildPostgresModelSeedUpsert renders the models bulk upsert: one statement
// with 40 bound parameters per row (status included) using global $n
// placeholders. 每行 VALUES 渲染 40 参数 + 8 字面量 = 48 值，与
// pgSeedModelsColumns 的 48 个目标列一一对应（与 SQLite sqSeedModelsUpsert 的
// 行模板同构）：scope='builtin'、system_account_id=NULL、pricing_notes/
// capability_notes/notes/created_by/updated_by 五列为 NULL、currency='USD'，
// 全部是语句内字面量。
func buildPostgresModelSeedUpsert(rows []modelSeedTargetRow, now string) (string, []any) {
	args := make([]any, 0, len(rows)*40)
	rowTemplates := make([]string, 0, len(rows))
	for _, model := range rows {
		first := len(args) + 1
		args = append(args,
			model.ID,
			model.VendorCode,
			model.PublicModel,
			model.UpstreamModel,
			model.Status,
			model.CatalogVisible,
			seedNullableInt64(model.CatalogOrder),
			seedNullableString(model.ReleaseDate),
			seedNullableString(model.ShutdownDate),
			model.SupportedEndpointModesJSON,
			model.SupportedServiceTiersJSON,
			model.SupportedReasoningEffortsJSON,
			seedNullableString(model.DefaultReasoningEffort),
			model.CodexSupportedReasoningLevelsJSON,
			seedNullableString(model.CodexDefaultReasoningLevel),
			seedNullableString(model.CodexMultiAgentVersion),
			seedNullableInt64(model.ContextWindowTokens),
			seedNullableInt64(model.MaxInputTokens),
			seedNullableInt64(model.MaxOutputTokens),
			seedNullableInt64(model.MaxTokens),
			model.SupportsPromptCaching,
			model.Source,
			now,
			now,
			seedNullableFloat64(model.InputUsdPer1M),
			seedNullableFloat64(model.OutputUsdPer1M),
			seedNullableFloat64(model.CachedInputUsdPer1M),
			seedNullableFloat64(model.CacheWriteUsdPer1M),
			seedNullableFloat64(model.CacheWrite1HUsdPer1M),
			seedNullableFloat64(model.CacheStorageUsdPer1MPerHour),
			model.ServiceTierPricesJSON,
			seedNullableInt64(model.LongContextInputTokenThreshold),
			model.LongContextInputTokenThresholdInclusive,
			seedNullableFloat64(model.LongContextInputCostMultiplier),
			seedNullableFloat64(model.LongContextOutputCostMultiplier),
			seedNullableFloat64(model.ImageInputUsdPer1M),
			seedNullableFloat64(model.ImageOutputUsdPer1M),
			seedNullableFloat64(model.AudioInputUsdPer1M),
			seedNullableFloat64(model.AudioOutputUsdPer1M),
			seedNullableFloat64(model.OutputUsdPerImage),
		)
		// 列序（与 pgSeedModelsColumns 的 48 列一一对齐）：
		//   1-4  = $first..$first+3（id/vendor_code/public_model/upstream_model）
		//   5-6  = 'builtin' / NULL 字面量（scope/system_account_id）
		//   7-24 = $first+4..$first+21（status..source）
		//   25-29 = NULL 字面量（pricing_notes/capability_notes/notes/
		//          created_by/updated_by）
		//   30-47 = $first+22..$first+39（created_at..output_usd_per_image）
		//   48   = 'USD' 字面量（currency）
		row := "($" + strconv.Itoa(first) + ", $" + strconv.Itoa(first+1) + ", $" + strconv.Itoa(first+2) +
			", $" + strconv.Itoa(first+3) + ", 'builtin', NULL, $" + strconv.Itoa(first+4)
		for index := 5; index < 22; index++ {
			row += ", $" + strconv.Itoa(first+index)
		}
		row += ", NULL, NULL, NULL, NULL, NULL, $" + strconv.Itoa(first+22) + ", $" + strconv.Itoa(first+23)
		for index := 24; index < 40; index++ {
			row += ", $" + strconv.Itoa(first+index)
		}
		row += ", 'USD')"
		rowTemplates = append(rowTemplates, row)
	}
	return pgSeedModelsColumns + strings.Join(rowTemplates, ",\n          ") + pgSeedModelsUpsertSuffix, args
}

// seedPostgresAdminDefaultRouteStrategiesAndAPIKeys ports
// seedAdminDefaultRouteStrategiesAndApiKeys (PostgreSQL variant: iterate the
// built-in group seeds, pick each owner default group by updated_at DESC).
func seedPostgresAdminDefaultRouteStrategiesAndAPIKeys(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, options SeedOptions, now string) error {
	for _, groupSeed := range pgSeedGroups {
		if groupSeed.SystemAccountID != "sys_admin" {
			continue
		}
		var groupID, groupName string
		err := client.QueryRowContext(ctx, pgSeedAdminDefaultGroupSelect, groupSeed.SystemAccountID).Scan(&groupID, &groupName)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return fmt.Errorf("postgres seed select admin default group: %w", err)
		}
		routeStrategyName := defaultRouteStrategyNameForGroup(groupName)
		routeStrategyID := defaultRouteStrategyIDForGroup(groupID)
		if err := exec(pgSeedAdminRouteStrategyInsert, routeStrategyID, routeStrategyName, "系统默认普通路由，绑定"+groupName+"。", now, now); err != nil {
			return err
		}
		var strategyID string
		err = client.QueryRowContext(ctx, pgSeedAdminRouteStrategySelect, routeStrategyID).Scan(&strategyID)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return fmt.Errorf("postgres seed select admin route strategy: %w", err)
		}
		if err := exec(pgSeedAdminRouteStrategyGroupBindingInsert, defaultRouteStrategyGroupBindingIDForGroup(groupID), routeStrategyID, groupID, now, now); err != nil {
			return err
		}
		var existingAPIKeyID string
		err = client.QueryRowContext(ctx, pgSeedAdminExistingDefaultAPIKeySelect, routeStrategyID).Scan(&existingAPIKeyID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("postgres seed select existing default api key: %w", err)
		}
		if err == nil {
			continue
		}
		apiKey, err := seedCreateAPIKey()
		if err != nil {
			return err
		}
		keySecretEncrypted, err := seedEncryptJSONWithOptions(options, map[string]string{"key": apiKey})
		if err != nil {
			return err
		}
		if err := exec(pgSeedAdminDefaultAPIKeyInsert,
			defaultAPIKeyIDForRouteStrategy(routeStrategyID),
			routeStrategyID,
			defaultAPIKeyNameForRouteStrategy(routeStrategyName),
			"系统默认 API Key，绑定"+routeStrategyName+"。",
			seedHashSecret(apiKey),
			seedKeyPrefix(apiKey),
			seedKeySuffix(apiKey),
			keySecretEncrypted,
			now,
			now,
		); err != nil {
			return err
		}
	}
	return nil
}

// seedPostgresAdminChatAPIKey was removed with the 2026-10-07 默认资源收口:
// the sys_admin chat API key is no longer seeded. 2026-10-24 免 Key 直调改造后
// AI 问答也不再创建任何 API Key（gateway EnsureChatAPIKey 链已删除），chat
// 会话派发走内部身份通道。

// seedPostgresExternalIntegrationTestToken ports
// seedBuiltInExternalIntegrationTestToken (PostgreSQL variant, including the
// token-free source row insert/update at its head).
func seedPostgresExternalIntegrationTestToken(ctx context.Context, client postgresSeedClient, exec func(string, ...any) error, options SeedOptions, now string) error {
	source := pgSeedExternalIntegrationSource
	scopesJSON := source.ScopesJSON
	if err := exec(pgSeedExternalIntegrationSourceInsert, source.ID, source.Name, scopesJSON, source.RateLimitsJSON, source.Notes, now, now); err != nil {
		return err
	}
	if err := exec(pgSeedExternalIntegrationSourceUpdate, source.Name, scopesJSON, source.RateLimitsJSON, source.Notes, now, source.ID); err != nil {
		return err
	}
	var existingTokenID string
	err := client.QueryRowContext(ctx, pgSeedExternalIntegrationTokenSelect, externalIntegrationTestTokenID).Scan(&existingTokenID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("postgres seed select external integration token: %w", err)
	}
	if err == sql.ErrNoRows {
		token, err := seedCreateExternalIntegrationToken()
		if err != nil {
			return err
		}
		tokenSecretEncrypted, err := seedEncryptJSONWithOptions(options, map[string]string{"token": token})
		if err != nil {
			return err
		}
		return exec(pgSeedExternalIntegrationTokenInsert,
			externalIntegrationTestTokenID,
			source.ID,
			externalIntegrationTestTokenName,
			seedHashExternalIntegrationToken(token),
			tokenSecretEncrypted,
			seedKeyPrefix(token),
			seedKeySuffix(token),
			scopesJSON,
			now,
			now,
		)
	}
	return exec(pgSeedExternalIntegrationTokenUpdate, source.ID, externalIntegrationTestTokenName, scopesJSON, now, externalIntegrationTestTokenID)
}
