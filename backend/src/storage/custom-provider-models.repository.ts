import type { SQLInputValue } from 'node:sqlite'

import type { ProviderModelPricing } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { normalizeServiceTierPrices } from './provider-model-catalog.repository.js'
import { notifyCommittedModelCacheInvalidationAsync } from './model-cache-sync-warning.js'

type CustomProviderModelApiProtocol = ProviderModelPricing['supportedApiProtocols'][number]
type CustomProviderModelServiceTier = string
type CustomProviderModelReasoningEffort = string
export type CustomProviderModelScope = 'global' | 'personal'
export type CustomProviderModelStatus = 'draft' | 'active' | 'disabled'

const customProviderModelApiProtocols = new Set<CustomProviderModelApiProtocol>([
  'chat_completions',
  'responses',
  'messages',
  'message_token_counting',
  'generate_content',
  'stream_generate_content',
  'count_tokens',
  'embed_content',
  'interactions',
  'completions',
  'images',
  'audio',
  'realtime'
])
const customProviderModelCapabilityTokens = {
  has(value: string): boolean {
    return /^[a-z0-9][a-z0-9._-]{0,63}$/i.test(value)
  }
} as ReadonlySet<string>

export interface CustomProviderModelRecord {
  id: string
  providerCode: string
  model: string
  scope: CustomProviderModelScope
  systemAccountId?: string
  status: CustomProviderModelStatus
  catalogVisible: boolean
  mode?: string
  supportedApiProtocols: CustomProviderModelApiProtocol[]
  supportedServiceTiers: CustomProviderModelServiceTier[]
  supportedReasoningEfforts: CustomProviderModelReasoningEffort[]
  defaultReasoningEffort?: CustomProviderModelReasoningEffort
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  serviceTierPrices?: Record<string, import('../domain/types.js').ProviderModelPriceSet>
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
  currency: 'USD'
  pricingNotes?: string
  capabilityNotes?: string
  notes?: string
  createdBy: string
  updatedBy?: string
  createdAt: string
  updatedAt: string
}

export interface CustomProviderModelTestCatalogRecord {
  id: string
  providerCode: string
  model: string
  scope: CustomProviderModelScope
  mode?: string
  releaseDate?: string
  supportedApiProtocols: CustomProviderModelApiProtocol[]
  supportedServiceTiers: string[]
  supportedReasoningEfforts: string[]
  defaultReasoningEffort?: string
}

export interface CustomProviderModelOptionRow {
  id: string
  providerCode: string
  model: string
  scope: CustomProviderModelScope
  releaseDate?: string
}

export async function listCustomProviderModelOptionsAsync(input: {
  providerCodes: string[]
  systemAccountId?: string
  keyword?: string
  limit: number
  selectedIds?: string[]
}): Promise<CustomProviderModelOptionRow[]> {
  const providerCodes = [...new Set(input.providerCodes.map((code) => code.trim()).filter(Boolean))]
  if (!providerCodes.length) return []
  const selectedIds = [...new Set((input.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].slice(0, 50)
  const keyword = input.keyword?.trim().toLowerCase()
  const resultLimit = Math.min(100, Math.max(1, input.limit + selectedIds.length))
  const { clause, params: scopeParams } = customProviderModelTestCatalogScope(input)
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? await getCustomProviderModelsDatabaseClient()
    : undefined
  const table = client ? customProviderModelsTable(client) : 'custom_provider_models'
  const providerFilter = client
    ? 'provider_code = ANY(?::text[])'
    : `provider_code IN (${providerCodes.map(() => '?').join(', ')})`
  const selectedFilter = selectedIds.length
    ? client ? 'model = ANY(?::text[])' : `model IN (${selectedIds.map(() => '?').join(', ')})`
    : ''
  const keywordFilter = keyword ? 'lower(model) LIKE ?' : ''
  const whereParts = [
    providerFilter,
    "status = 'active'",
    client ? 'catalog_visible = TRUE' : 'catalog_visible = 1',
    client
      ? "(shutdown_date IS NULL OR btrim(shutdown_date) = '' OR shutdown_date > CURRENT_DATE::text)"
      : "(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > date('now'))",
    clause.replace(/^AND\s+/i, '')
  ]
  if (keywordFilter) {
    whereParts.push(`(${[selectedFilter, keywordFilter].filter(Boolean).join(' OR ')})`)
  }
  const order = selectedIds.length
    ? `CASE WHEN ${client ? 'model = ANY(?::text[])' : `model IN (${selectedIds.map(() => '?').join(', ')})`} THEN 0 ELSE 1 END, `
    : ''
  const sql = `
    WITH ranked_options AS (
      SELECT id, provider_code, model, scope, mode, release_date, supported_api_protocols_json,
        ROW_NUMBER() OVER (
          PARTITION BY model
          ORDER BY CASE scope WHEN 'personal' THEN 0 ELSE 1 END, provider_code ASC, id ASC
        ) AS option_rank
      FROM ${table}
      WHERE ${whereParts.join('\n        AND ')}
    )
    SELECT id, provider_code, model, scope, mode, release_date, supported_api_protocols_json
    FROM ranked_options
    WHERE option_rank = 1
    ORDER BY ${order}
      CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
      release_date DESC, lower(model) ASC, provider_code ASC, scope ASC, id ASC
    LIMIT ?
  `
  const params: unknown[] = client ? [providerCodes] : [...providerCodes]
  params.push(...scopeParams)
  if (selectedFilter && keywordFilter) {
    if (client) params.push(selectedIds)
    else params.push(...selectedIds)
  }
  if (keywordFilter) params.push(`%${keyword}%`)
  if (selectedIds.length) {
    if (client) params.push(selectedIds)
    else params.push(...selectedIds)
  }
  params.push(resultLimit)
  const rows = client
    ? await client.query<{
        id: string
        provider_code: string
        model: string
        scope: CustomProviderModelScope
        mode?: string | null
        release_date?: string | null
        supported_api_protocols_json?: string | null
      }>(sql, params)
    : getBusinessDatabase().prepare(sql).all(...params as SQLInputValue[]) as unknown as Array<{
        id: string
         provider_code: string
         model: string
         scope: CustomProviderModelScope
         mode?: string | null
         release_date?: string | null
         supported_api_protocols_json?: string | null
       }>
  return rows.map((row) => ({
    id: row.id,
    providerCode: row.provider_code,
    model: row.model,
    scope: row.scope,
    mode: testCatalogOptionalText(row.mode),
    releaseDate: testCatalogOptionalText(row.release_date),
    supportedApiProtocols: testCatalogStringList(row.supported_api_protocols_json)
  }))
}

interface CustomProviderModelTestCatalogRow {
  id: string
  provider_code: string
  model: string
  scope: CustomProviderModelScope
  mode?: string | null
  release_date?: string | null
  supported_api_protocols_json?: string | null
  supported_service_tiers_json?: string | null
  supported_reasoning_efforts_json?: string | null
  default_reasoning_effort?: string | null
}

export interface CustomProviderModelAccountBindingSummary {
  supportedModelAccountCount: number
  mappingSourceAccountCount: number
  mappingUpstreamAccountCount: number
  totalAccountCount: number
}

export interface UpsertCustomProviderModelInput {
  id?: string
  providerCode: string
  model: string
  scope?: CustomProviderModelScope
  systemAccountId?: string
  status?: CustomProviderModelStatus
  catalogVisible?: boolean
  mode?: string | null
  supportedApiProtocols?: string[] | null
  supportedServiceTiers?: string[] | null
  supportedReasoningEfforts?: string[] | null
  defaultReasoningEffort?: string | null
  releaseDate?: string | null
  shutdownDate?: string | null
  contextWindowTokens?: number | null
  maxInputTokens?: number | null
  maxOutputTokens?: number | null
  inputUsdPer1M?: number | null
  outputUsdPer1M?: number | null
  cachedInputUsdPer1M?: number | null
  cacheWriteUsdPer1M?: number | null
  cacheWrite1hUsdPer1M?: number | null
  serviceTierPrices?: unknown
  imageInputUsdPer1M?: number | null
  imageOutputUsdPer1M?: number | null
  audioInputUsdPer1M?: number | null
  audioOutputUsdPer1M?: number | null
  outputUsdPerImage?: number | null
  pricingNotes?: string | null
  capabilityNotes?: string | null
  notes?: string | null
  actorSystemAccountId: string
}

interface CustomProviderModelRow {
  id: string
  provider_code: string
  model: string
  scope: CustomProviderModelScope
  system_account_id?: string | null
  status: CustomProviderModelStatus
  catalog_visible: number | boolean
  mode?: string | null
  supported_api_protocols_json?: string | null
  supported_service_tiers_json?: string | null
  supported_reasoning_efforts_json?: string | null
  default_reasoning_effort?: string | null
  release_date?: string | null
  shutdown_date?: string | null
  context_window_tokens?: number | null
  max_input_tokens?: number | null
  max_output_tokens?: number | null
  input_usd_per_1m?: number | null
  output_usd_per_1m?: number | null
  cached_input_usd_per_1m?: number | null
  cache_write_usd_per_1m?: number | null
  cache_write_1h_usd_per_1m?: number | null
  service_tier_prices_json?: string | null
  image_input_usd_per_1m?: number | null
  image_output_usd_per_1m?: number | null
  audio_input_usd_per_1m?: number | null
  audio_output_usd_per_1m?: number | null
  output_usd_per_image?: number | null
  currency?: string | null
  pricing_notes?: string | null
  capability_notes?: string | null
  notes?: string | null
  created_by: string
  updated_by?: string | null
  created_at: string
  updated_at: string
}

export function listCustomProviderModelsForCatalog(input: {
  providerCode: string
  systemAccountId?: string
  includeInactive?: boolean
}): CustomProviderModelRecord[] {
  const clauses = ['provider_code = ?']
  const params: SQLInputValue[] = [input.providerCode]
  if (!input.includeInactive) {
    clauses.push("status = 'active'")
  }
  if (input.systemAccountId) {
    clauses.push("((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
    params.push(input.systemAccountId)
  } else {
    clauses.push("scope = 'global' AND system_account_id IS NULL")
  }

  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${customProviderModelColumns()}
      FROM custom_provider_models
      WHERE ${clauses.join(' AND ')}
      ORDER BY scope ASC, model COLLATE NOCASE ASC, id ASC
    `)
    .all(...params) as unknown as CustomProviderModelRow[]
  return rows.map(customProviderModelFromRow)
}

export async function listCustomProviderModelsForCatalogAsync(input: {
  providerCode: string
  systemAccountId?: string
  includeInactive?: boolean
}): Promise<CustomProviderModelRecord[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listCustomProviderModelsForCatalog(input)
  }
  const clauses = ['provider_code = ?']
  const params: unknown[] = [input.providerCode]
  if (!input.includeInactive) {
    clauses.push("status = 'active'")
  }
  if (input.systemAccountId) {
    clauses.push("((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))")
    params.push(input.systemAccountId)
  } else {
    clauses.push("scope = 'global' AND system_account_id IS NULL")
  }

  const client = await getCustomProviderModelsDatabaseClient()
  const rows = await client.query<CustomProviderModelRow>(`
    SELECT ${customProviderModelColumns()}
    FROM ${customProviderModelsTable(client)}
    WHERE ${clauses.join(' AND ')}
    ORDER BY scope ASC, lower(model) ASC, id ASC
  `, params)
  return rows.map(customProviderModelFromRow)
}

export async function listCustomProviderModelTestCatalogAsync(input: {
  providerCodes: string[]
  systemAccountId?: string
}): Promise<CustomProviderModelTestCatalogRecord[]> {
  const providerCodes = [...new Set(input.providerCodes.map((code) => code.trim()).filter(Boolean))]
  if (!providerCodes.length) return []
  const { clause, params } = customProviderModelTestCatalogScope(input)
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const placeholders = providerCodes.map(() => '?').join(', ')
    const rows = getBusinessDatabase().prepare(`
      SELECT id, provider_code, model, scope, mode, release_date, supported_api_protocols_json,
        supported_service_tiers_json, supported_reasoning_efforts_json, default_reasoning_effort
      FROM custom_provider_models
      WHERE provider_code IN (${placeholders})
        AND status = 'active'
        ${clause}
      ORDER BY provider_code ASC, scope ASC, model COLLATE NOCASE ASC, id ASC
    `).all(...providerCodes, ...params as SQLInputValue[]) as unknown as CustomProviderModelTestCatalogRow[]
    return rows.map(customProviderModelTestCatalogRecordFromRow)
  }
  const client = await getCustomProviderModelsDatabaseClient()
  const rows = await client.query<CustomProviderModelTestCatalogRow>(`
    SELECT id, provider_code, model, scope, mode, release_date, supported_api_protocols_json,
      supported_service_tiers_json, supported_reasoning_efforts_json, default_reasoning_effort
    FROM ${customProviderModelsTable(client)}
    WHERE provider_code = ANY(?::text[])
      AND status = 'active'
      ${clause}
    ORDER BY provider_code ASC, scope ASC, lower(model) ASC, id ASC
  `, [providerCodes, ...params])
  return rows.map(customProviderModelTestCatalogRecordFromRow)
}

export async function findCustomProviderModelTestCatalogAsync(input: {
  providerCodes: string[]
  systemAccountId?: string
  model: string
  projection?: 'test' | 'protocols'
}): Promise<CustomProviderModelTestCatalogRecord[]> {
  const model = input.model.trim()
  const providerCodes = [...new Set(input.providerCodes.map((code) => code.trim()).filter(Boolean))]
  if (!model || !providerCodes.length) return []
  const { clause, params } = customProviderModelTestCatalogScope(input)
  const selectedColumns = input.projection === 'protocols'
    ? 'id, provider_code, model, scope, mode, supported_api_protocols_json'
    : `id, provider_code, model, scope, mode, release_date, supported_api_protocols_json,
        supported_service_tiers_json, supported_reasoning_efforts_json, default_reasoning_effort`
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const placeholders = providerCodes.map(() => '?').join(', ')
    const rows = getBusinessDatabase().prepare(`
      SELECT ${selectedColumns}
      FROM custom_provider_models
      WHERE provider_code IN (${placeholders})
        AND model = ?
        AND status = 'active'
        ${clause}
      ORDER BY provider_code ASC, scope ASC, id ASC
    `).all(...providerCodes, model, ...params as SQLInputValue[]) as unknown as CustomProviderModelTestCatalogRow[]
    return rows.map(customProviderModelTestCatalogRecordFromRow)
  }
  const client = await getCustomProviderModelsDatabaseClient()
  const rows = await client.query<CustomProviderModelTestCatalogRow>(`
    SELECT ${selectedColumns}
    FROM ${customProviderModelsTable(client)}
    WHERE provider_code = ANY(?::text[])
      AND model = ?
      AND status = 'active'
      ${clause}
    ORDER BY provider_code ASC, scope ASC, id ASC
  `, [providerCodes, model, ...params])
  return rows.map(customProviderModelTestCatalogRecordFromRow)
}

export function findCustomProviderModelById(id: string): CustomProviderModelRecord | undefined {
  const row = getBusinessDatabase()
    .prepare(`SELECT ${customProviderModelColumns()} FROM custom_provider_models WHERE id = ? LIMIT 1`)
    .get(id) as unknown as CustomProviderModelRow | undefined
  return row ? customProviderModelFromRow(row) : undefined
}

export async function findCustomProviderModelByIdAsync(id: string): Promise<CustomProviderModelRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findCustomProviderModelById(id)
  }
  const client = await getCustomProviderModelsDatabaseClient()
  const row = await client.one<CustomProviderModelRow>(`
    SELECT ${customProviderModelColumns()}
    FROM ${customProviderModelsTable(client)}
    WHERE id = ?
    LIMIT 1
  `, [id])
  return row ? customProviderModelFromRow(row) : undefined
}

export function upsertCustomProviderModel(
  input: UpsertCustomProviderModelInput,
  options: { notifyCache?: boolean } = {}
): CustomProviderModelRecord {
  const providerCode = requiredText(input.providerCode, '供应商代码不能为空')
  const model = requiredText(input.model, '模型 ID 不能为空')
  const scope: CustomProviderModelScope = input.scope === 'global' ? 'global' : 'personal'
  const systemAccountId = scope === 'global'
    ? undefined
    : requiredText(input.systemAccountId ?? input.actorSystemAccountId, '个人模型必须归属系统账户')
  const status = input.status ?? 'active'
  const now = nowIso()
  const existing = input.id
    ? findCustomProviderModelById(input.id)
    : findCustomProviderModelByScope(providerCode, scope, systemAccountId, model)
  const catalogVisible = input.catalogVisible ?? existing?.catalogVisible ?? true
  if (existing && existing.model.trim() !== model) {
    throw new Error('模型 ID 创建后不能修改')
  }
  const id = existing?.id ?? input.id ?? newId('custom_model')
  const capabilities = normalizeCustomProviderModelCapabilities(providerCode, input)

  getBusinessDatabase()
    .prepare(`
      INSERT INTO custom_provider_models (
        id, provider_code, model, scope, system_account_id, status, catalog_visible,
        mode, supported_api_protocols_json, supported_service_tiers_json,
        supported_reasoning_efforts_json, default_reasoning_effort,
        release_date, shutdown_date, context_window_tokens, max_input_tokens, max_output_tokens,
        input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m, cache_write_1h_usd_per_1m, service_tier_prices_json,
        image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
        output_usd_per_image, currency, pricing_notes, capability_notes, notes,
        created_by, updated_by, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'USD', ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(id) DO UPDATE SET
        provider_code = excluded.provider_code,
        model = excluded.model,
        scope = excluded.scope,
        system_account_id = excluded.system_account_id,
        status = excluded.status,
        catalog_visible = excluded.catalog_visible,
        mode = excluded.mode,
        supported_api_protocols_json = excluded.supported_api_protocols_json,
        supported_service_tiers_json = excluded.supported_service_tiers_json,
        supported_reasoning_efforts_json = excluded.supported_reasoning_efforts_json,
        default_reasoning_effort = excluded.default_reasoning_effort,
        release_date = excluded.release_date,
        shutdown_date = excluded.shutdown_date,
        context_window_tokens = excluded.context_window_tokens,
        max_input_tokens = excluded.max_input_tokens,
        max_output_tokens = excluded.max_output_tokens,
        input_usd_per_1m = excluded.input_usd_per_1m,
        output_usd_per_1m = excluded.output_usd_per_1m,
        cached_input_usd_per_1m = excluded.cached_input_usd_per_1m,
        cache_write_usd_per_1m = excluded.cache_write_usd_per_1m,
        cache_write_1h_usd_per_1m = excluded.cache_write_1h_usd_per_1m,
        service_tier_prices_json = excluded.service_tier_prices_json,
        image_input_usd_per_1m = excluded.image_input_usd_per_1m,
        image_output_usd_per_1m = excluded.image_output_usd_per_1m,
        audio_input_usd_per_1m = excluded.audio_input_usd_per_1m,
        audio_output_usd_per_1m = excluded.audio_output_usd_per_1m,
        output_usd_per_image = excluded.output_usd_per_image,
        pricing_notes = excluded.pricing_notes,
        capability_notes = excluded.capability_notes,
        notes = excluded.notes,
        updated_by = excluded.updated_by,
        updated_at = excluded.updated_at
    `)
    .run(
      id,
      providerCode,
      model,
      scope,
      systemAccountId ?? null,
      status,
      catalogVisible ? 1 : 0,
      optionalText(input.mode) ?? null,
      JSON.stringify(normalizeProtocols(input.supportedApiProtocols)),
      JSON.stringify(capabilities.supportedServiceTiers),
      JSON.stringify(capabilities.supportedReasoningEfforts),
      capabilities.defaultReasoningEffort ?? null,
      optionalDate(input.releaseDate) ?? null,
      optionalDate(input.shutdownDate) ?? null,
      optionalInteger(input.contextWindowTokens) ?? null,
      optionalInteger(input.maxInputTokens) ?? null,
      optionalInteger(input.maxOutputTokens) ?? null,
      optionalNumber(input.inputUsdPer1M) ?? null,
      optionalNumber(input.outputUsdPer1M) ?? null,
      optionalNumber(input.cachedInputUsdPer1M) ?? null,
      optionalNumber(input.cacheWriteUsdPer1M) ?? null,
      optionalNumber(input.cacheWrite1hUsdPer1M) ?? null,
      JSON.stringify(normalizeServiceTierPrices(input.serviceTierPrices)),
      optionalNumber(input.imageInputUsdPer1M) ?? null,
      optionalNumber(input.imageOutputUsdPer1M) ?? null,
      optionalNumber(input.audioInputUsdPer1M) ?? null,
      optionalNumber(input.audioOutputUsdPer1M) ?? null,
      optionalNumber(input.outputUsdPerImage) ?? null,
      optionalText(input.pricingNotes) ?? null,
      optionalText(input.capabilityNotes) ?? null,
      optionalText(input.notes) ?? null,
      existing?.createdBy ?? input.actorSystemAccountId,
      input.actorSystemAccountId,
      existing?.createdAt ?? now,
      now
    )

  const saved = findCustomProviderModelById(id)
  if (!saved) {
    throw new Error('自定义模型保存失败')
  }
  if (options.notifyCache !== false) {
    notifyGatewayRuntimeCacheInvalidation('custom_provider_model_saved')
  }
  return saved
}

export async function upsertCustomProviderModelAsync(input: UpsertCustomProviderModelInput): Promise<CustomProviderModelRecord> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const saved = upsertCustomProviderModel(input, { notifyCache: false })
    await notifyCommittedModelCacheInvalidationAsync('custom_provider_model_saved')
    return saved
  }
  const providerCode = requiredText(input.providerCode, '供应商代码不能为空')
  const model = requiredText(input.model, '模型 ID 不能为空')
  const scope: CustomProviderModelScope = input.scope === 'global' ? 'global' : 'personal'
  const systemAccountId = scope === 'global'
    ? undefined
    : requiredText(input.systemAccountId ?? input.actorSystemAccountId, '个人模型必须归属系统账户')
  const status = input.status ?? 'active'
  const now = nowIso()
  const existing = input.id
    ? await findCustomProviderModelByIdAsync(input.id)
    : await findCustomProviderModelByScopeAsync(providerCode, scope, systemAccountId, model)
  const catalogVisible = input.catalogVisible ?? existing?.catalogVisible ?? true
  if (existing && existing.model.trim() !== model) {
    throw new Error('模型 ID 创建后不能修改')
  }
  const id = existing?.id ?? input.id ?? newId('custom_model')
  const capabilities = normalizeCustomProviderModelCapabilities(providerCode, input)

  const client = await getCustomProviderModelsDatabaseClient()
  await client.execute(`
    INSERT INTO ${customProviderModelsTable(client)} (
      id, provider_code, model, scope, system_account_id, status, catalog_visible,
      mode, supported_api_protocols_json, supported_service_tiers_json,
      supported_reasoning_efforts_json, default_reasoning_effort,
      release_date, shutdown_date, context_window_tokens, max_input_tokens, max_output_tokens,
      input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m, cache_write_1h_usd_per_1m, service_tier_prices_json,
      image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
      output_usd_per_image, currency, pricing_notes, capability_notes, notes,
      created_by, updated_by, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'USD', ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO UPDATE SET
      provider_code = excluded.provider_code,
      model = excluded.model,
      scope = excluded.scope,
      system_account_id = excluded.system_account_id,
      status = excluded.status,
      catalog_visible = excluded.catalog_visible,
      mode = excluded.mode,
      supported_api_protocols_json = excluded.supported_api_protocols_json,
      supported_service_tiers_json = excluded.supported_service_tiers_json,
      supported_reasoning_efforts_json = excluded.supported_reasoning_efforts_json,
      default_reasoning_effort = excluded.default_reasoning_effort,
      release_date = excluded.release_date,
      shutdown_date = excluded.shutdown_date,
      context_window_tokens = excluded.context_window_tokens,
      max_input_tokens = excluded.max_input_tokens,
      max_output_tokens = excluded.max_output_tokens,
      input_usd_per_1m = excluded.input_usd_per_1m,
      output_usd_per_1m = excluded.output_usd_per_1m,
      cached_input_usd_per_1m = excluded.cached_input_usd_per_1m,
      cache_write_usd_per_1m = excluded.cache_write_usd_per_1m,
      cache_write_1h_usd_per_1m = excluded.cache_write_1h_usd_per_1m,
      service_tier_prices_json = excluded.service_tier_prices_json,
      image_input_usd_per_1m = excluded.image_input_usd_per_1m,
      image_output_usd_per_1m = excluded.image_output_usd_per_1m,
      audio_input_usd_per_1m = excluded.audio_input_usd_per_1m,
      audio_output_usd_per_1m = excluded.audio_output_usd_per_1m,
      output_usd_per_image = excluded.output_usd_per_image,
      pricing_notes = excluded.pricing_notes,
      capability_notes = excluded.capability_notes,
      notes = excluded.notes,
      updated_by = excluded.updated_by,
      updated_at = excluded.updated_at
  `, [
    id,
    providerCode,
    model,
    scope,
    systemAccountId ?? null,
    status,
    runtimeConfig.databaseDriver === 'postgres' ? Boolean(catalogVisible) : (catalogVisible ? 1 : 0),
    optionalText(input.mode) ?? null,
    JSON.stringify(normalizeProtocols(input.supportedApiProtocols)),
    JSON.stringify(capabilities.supportedServiceTiers),
    JSON.stringify(capabilities.supportedReasoningEfforts),
    capabilities.defaultReasoningEffort ?? null,
    optionalDate(input.releaseDate) ?? null,
    optionalDate(input.shutdownDate) ?? null,
    optionalInteger(input.contextWindowTokens) ?? null,
    optionalInteger(input.maxInputTokens) ?? null,
    optionalInteger(input.maxOutputTokens) ?? null,
    optionalNumber(input.inputUsdPer1M) ?? null,
    optionalNumber(input.outputUsdPer1M) ?? null,
    optionalNumber(input.cachedInputUsdPer1M) ?? null,
    optionalNumber(input.cacheWriteUsdPer1M) ?? null,
    optionalNumber(input.cacheWrite1hUsdPer1M) ?? null,
    JSON.stringify(normalizeServiceTierPrices(input.serviceTierPrices)),
    optionalNumber(input.imageInputUsdPer1M) ?? null,
    optionalNumber(input.imageOutputUsdPer1M) ?? null,
    optionalNumber(input.audioInputUsdPer1M) ?? null,
    optionalNumber(input.audioOutputUsdPer1M) ?? null,
    optionalNumber(input.outputUsdPerImage) ?? null,
    optionalText(input.pricingNotes) ?? null,
    optionalText(input.capabilityNotes) ?? null,
    optionalText(input.notes) ?? null,
    existing?.createdBy ?? input.actorSystemAccountId,
    input.actorSystemAccountId,
    existing?.createdAt ?? now,
    now
  ])

  const saved = await findCustomProviderModelByIdAsync(id)
  if (!saved) {
    throw new Error('自定义模型保存失败')
  }
  await notifyCommittedModelCacheInvalidationAsync('custom_provider_model_saved')
  return saved
}

export function deleteCustomProviderModel(
  id: string,
  options: { notifyCache?: boolean } = {}
): boolean {
  const result = getBusinessDatabase()
    .prepare('DELETE FROM custom_provider_models WHERE id = ?')
    .run(id)
  if (result.changes > 0 && options.notifyCache !== false) {
    void notifyCommittedModelCacheInvalidationAsync('custom_provider_model_deleted')
  }
  return result.changes > 0
}

export async function deleteCustomProviderModelAsync(id: string): Promise<boolean> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const deleted = deleteCustomProviderModel(id, { notifyCache: false })
    if (deleted) {
      await notifyCommittedModelCacheInvalidationAsync('custom_provider_model_deleted')
    }
    return deleted
  }
  const client = await getCustomProviderModelsDatabaseClient()
  const result = await client.execute(`DELETE FROM ${customProviderModelsTable(client)} WHERE id = ?`, [id])
  if (result.changes > 0) {
    await notifyCommittedModelCacheInvalidationAsync('custom_provider_model_deleted')
  }
  return result.changes > 0
}

export function customProviderModelAccountBindingSummary(input: {
  providerCode: string
  model: string
  scope: CustomProviderModelScope
  systemAccountId?: string
}): CustomProviderModelAccountBindingSummary {
  const providerCode = requiredText(input.providerCode, '供应商代码不能为空')
  const model = requiredText(input.model, '模型 ID 不能为空')
  const accountOwnerScope = accountBindingOwnerScope(input.scope, input.systemAccountId)
  const supportedModelSql = `
    SELECT account_supported_models.account_id
    FROM account_supported_models
    INNER JOIN accounts
      ON accounts.id = account_supported_models.account_id
      AND accounts.deleted_at IS NULL
    WHERE account_supported_models.provider_code = ?
      AND account_supported_models.model = ?
      ${accountOwnerScope.sql}
  `
  const supportedModelParams: SQLInputValue[] = [providerCode, model, ...accountOwnerScope.params]
  const mappingSourceSql = `
    SELECT account_model_mappings.account_id
    FROM account_model_mappings
    INNER JOIN accounts
      ON accounts.id = account_model_mappings.account_id
      AND accounts.deleted_at IS NULL
    WHERE account_model_mappings.source_model = ?
      ${accountOwnerScope.sql}
  `
  const mappingSourceParams: SQLInputValue[] = [model, ...accountOwnerScope.params]
  const mappingUpstreamSql = `
    SELECT account_model_mappings.account_id
    FROM account_model_mappings
    INNER JOIN accounts
      ON accounts.id = account_model_mappings.account_id
      AND accounts.deleted_at IS NULL
    WHERE account_model_mappings.upstream_model = ?
      ${accountOwnerScope.sql}
  `
  const mappingUpstreamParams: SQLInputValue[] = [model, ...accountOwnerScope.params]
  const supportedModelAccountCount = countDistinctBoundAccounts(supportedModelSql, ...supportedModelParams)
  const mappingSourceAccountCount = countDistinctBoundAccounts(mappingSourceSql, ...mappingSourceParams)
  const mappingUpstreamAccountCount = countDistinctBoundAccounts(mappingUpstreamSql, ...mappingUpstreamParams)
  const totalAccountCount = countDistinctBoundAccounts(`
    SELECT account_id FROM (
      ${supportedModelSql}
      UNION
      ${mappingSourceSql}
      UNION
      ${mappingUpstreamSql}
    )
  `, ...supportedModelParams, ...mappingSourceParams, ...mappingUpstreamParams)

  return {
    supportedModelAccountCount,
    mappingSourceAccountCount,
    mappingUpstreamAccountCount,
    totalAccountCount
  }
}

export async function customProviderModelAccountBindingSummaryAsync(input: {
  providerCode: string
  model: string
  scope: CustomProviderModelScope
  systemAccountId?: string
}): Promise<CustomProviderModelAccountBindingSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return customProviderModelAccountBindingSummary(input)
  }
  const providerCode = requiredText(input.providerCode, '供应商代码不能为空')
  const model = requiredText(input.model, '模型 ID 不能为空')
  const client = await getCustomProviderModelsDatabaseClient()
  const accountOwnerScope = accountBindingOwnerScope(input.scope, input.systemAccountId)
  const accountsTable = businessTable(client, 'accounts')
  const supportedModelsTable = businessTable(client, 'account_supported_models')
  const modelMappingsTable = businessTable(client, 'account_model_mappings')
  const supportedModelSql = `
    SELECT account_supported_models.account_id
    FROM ${supportedModelsTable} account_supported_models
    INNER JOIN ${accountsTable} accounts
      ON accounts.id = account_supported_models.account_id
      AND accounts.deleted_at IS NULL
    WHERE account_supported_models.provider_code = ?
      AND account_supported_models.model = ?
      ${accountOwnerScope.sql}
  `
  const supportedModelParams: SQLInputValue[] = [providerCode, model, ...accountOwnerScope.params]
  const mappingSourceSql = `
    SELECT account_model_mappings.account_id
    FROM ${modelMappingsTable} account_model_mappings
    INNER JOIN ${accountsTable} accounts
      ON accounts.id = account_model_mappings.account_id
      AND accounts.deleted_at IS NULL
    WHERE account_model_mappings.source_model = ?
      ${accountOwnerScope.sql}
  `
  const mappingSourceParams: SQLInputValue[] = [model, ...accountOwnerScope.params]
  const mappingUpstreamSql = `
    SELECT account_model_mappings.account_id
    FROM ${modelMappingsTable} account_model_mappings
    INNER JOIN ${accountsTable} accounts
      ON accounts.id = account_model_mappings.account_id
      AND accounts.deleted_at IS NULL
    WHERE account_model_mappings.upstream_model = ?
      ${accountOwnerScope.sql}
  `
  const mappingUpstreamParams: SQLInputValue[] = [model, ...accountOwnerScope.params]
  const supportedModelAccountCount = await countDistinctBoundAccountsAsync(client, supportedModelSql, supportedModelParams)
  const mappingSourceAccountCount = await countDistinctBoundAccountsAsync(client, mappingSourceSql, mappingSourceParams)
  const mappingUpstreamAccountCount = await countDistinctBoundAccountsAsync(client, mappingUpstreamSql, mappingUpstreamParams)
  const totalAccountCount = await countDistinctBoundAccountsAsync(client, `
    SELECT account_id FROM (
      ${supportedModelSql}
      UNION
      ${mappingSourceSql}
      UNION
      ${mappingUpstreamSql}
    ) bound_account_union
  `, [...supportedModelParams, ...mappingSourceParams, ...mappingUpstreamParams])

  return {
    supportedModelAccountCount,
    mappingSourceAccountCount,
    mappingUpstreamAccountCount,
    totalAccountCount
  }
}

function accountBindingOwnerScope(scope: CustomProviderModelScope, systemAccountId?: string): { sql: string; params: SQLInputValue[] } {
  if (scope === 'global') {
    return { sql: '', params: [] }
  }
  return {
    sql: 'AND accounts.system_account_id = ?',
    params: [requiredText(systemAccountId, '个人模型必须归属系统账户')]
  }
}

function findCustomProviderModelByScope(
  providerCode: string,
  scope: CustomProviderModelScope,
  systemAccountId: string | undefined,
  model: string
): CustomProviderModelRecord | undefined {
  const row = scope === 'global'
    ? getBusinessDatabase()
      .prepare(`SELECT ${customProviderModelColumns()} FROM custom_provider_models WHERE provider_code = ? AND scope = 'global' AND system_account_id IS NULL AND model = ? LIMIT 1`)
      .get(providerCode, model)
    : getBusinessDatabase()
      .prepare(`SELECT ${customProviderModelColumns()} FROM custom_provider_models WHERE provider_code = ? AND scope = 'personal' AND system_account_id = ? AND model = ? LIMIT 1`)
      .get(providerCode, systemAccountId ?? '', model)
  return row ? customProviderModelFromRow(row as unknown as CustomProviderModelRow) : undefined
}

async function findCustomProviderModelByScopeAsync(
  providerCode: string,
  scope: CustomProviderModelScope,
  systemAccountId: string | undefined,
  model: string
): Promise<CustomProviderModelRecord | undefined> {
  const client = await getCustomProviderModelsDatabaseClient()
  if (scope === 'global') {
    const row = await client.one<CustomProviderModelRow>(`
      SELECT ${customProviderModelColumns()}
      FROM ${customProviderModelsTable(client)}
      WHERE provider_code = ?
        AND scope = 'global'
        AND system_account_id IS NULL
        AND model = ?
      LIMIT 1
    `, [providerCode, model])
    return row ? customProviderModelFromRow(row) : undefined
  }
  const row = await client.one<CustomProviderModelRow>(`
    SELECT ${customProviderModelColumns()}
    FROM ${customProviderModelsTable(client)}
    WHERE provider_code = ?
      AND scope = 'personal'
      AND system_account_id = ?
      AND model = ?
    LIMIT 1
  `, [providerCode, systemAccountId ?? '', model])
  return row ? customProviderModelFromRow(row) : undefined
}

function countDistinctBoundAccounts(sql: string, ...params: SQLInputValue[]): number {
  const row = getBusinessDatabase()
    .prepare(`SELECT COUNT(DISTINCT account_id) AS count FROM (${sql})`)
    .get(...params) as { count?: number } | undefined
  return typeof row?.count === 'number' ? row.count : 0
}

async function countDistinctBoundAccountsAsync(client: DatabaseClient, sql: string, params: readonly unknown[]): Promise<number> {
  const row = await client.one<{ count?: number | string | bigint }>(`SELECT COUNT(DISTINCT account_id) AS count FROM (${sql}) bound_accounts`, params)
  const count = Number(row?.count ?? 0)
  return Number.isFinite(count) ? count : 0
}

function customProviderModelColumns(): string {
  return `
    id, provider_code, model, scope, system_account_id, status, catalog_visible,
    mode, supported_api_protocols_json, supported_service_tiers_json,
    supported_reasoning_efforts_json, default_reasoning_effort,
    release_date, shutdown_date, context_window_tokens, max_input_tokens, max_output_tokens,
    input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m, cache_write_1h_usd_per_1m, service_tier_prices_json,
    image_input_usd_per_1m, image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m,
    output_usd_per_image, currency, pricing_notes, capability_notes, notes,
    created_by, updated_by, created_at, updated_at
  `
}

function customProviderModelTestCatalogScope(input: { systemAccountId?: string }): { clause: string; params: string[] } {
  const systemAccountId = input.systemAccountId?.trim()
  return systemAccountId
    ? {
        clause: "AND ((scope = 'global' AND system_account_id IS NULL) OR (scope = 'personal' AND system_account_id = ?))",
        params: [systemAccountId]
      }
    : {
        clause: "AND scope = 'global' AND system_account_id IS NULL",
        params: []
      }
}

function customProviderModelTestCatalogRecordFromRow(
  row: CustomProviderModelTestCatalogRow
): CustomProviderModelTestCatalogRecord {
  return {
    id: row.id,
    providerCode: row.provider_code,
    model: row.model,
    scope: row.scope,
    mode: testCatalogOptionalText(row.mode),
    releaseDate: testCatalogOptionalText(row.release_date),
    supportedApiProtocols: testCatalogStringList(row.supported_api_protocols_json) as CustomProviderModelApiProtocol[],
    supportedServiceTiers: testCatalogStringList(row.supported_service_tiers_json),
    supportedReasoningEfforts: testCatalogStringList(row.supported_reasoning_efforts_json),
    defaultReasoningEffort: testCatalogOptionalText(row.default_reasoning_effort)
  }
}

function testCatalogOptionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function testCatalogStringList(value: unknown): string[] {
  if (typeof value !== 'string') return []
  try {
    const parsed = JSON.parse(value) as unknown
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string') : []
  } catch {
    return []
  }
}

async function getCustomProviderModelsDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  throw new Error('custom provider models async client is only available in PostgreSQL mode')
}

function customProviderModelsTable(client: DatabaseClient): string {
  return businessTable(client, 'custom_provider_models')
}

function businessTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function customProviderModelFromRow(row: CustomProviderModelRow): CustomProviderModelRecord {
  return {
    id: row.id,
    providerCode: row.provider_code,
    model: row.model,
    scope: row.scope,
    systemAccountId: optionalText(row.system_account_id),
    status: row.status,
    catalogVisible: Boolean(row.catalog_visible),
    mode: optionalText(row.mode),
    supportedApiProtocols: parseStringArray(row.supported_api_protocols_json),
    supportedServiceTiers: parseEnumArray(row.supported_service_tiers_json, customProviderModelCapabilityTokens),
    supportedReasoningEfforts: parseEnumArray(row.supported_reasoning_efforts_json, customProviderModelCapabilityTokens),
    defaultReasoningEffort: optionalEnum(row.default_reasoning_effort, customProviderModelCapabilityTokens),
    releaseDate: optionalText(row.release_date),
    shutdownDate: optionalText(row.shutdown_date),
    contextWindowTokens: optionalInteger(row.context_window_tokens),
    maxInputTokens: optionalInteger(row.max_input_tokens),
    maxOutputTokens: optionalInteger(row.max_output_tokens),
    inputUsdPer1M: optionalNumber(row.input_usd_per_1m),
    outputUsdPer1M: optionalNumber(row.output_usd_per_1m),
    cachedInputUsdPer1M: optionalNumber(row.cached_input_usd_per_1m),
    cacheWriteUsdPer1M: optionalNumber(row.cache_write_usd_per_1m),
    cacheWrite1hUsdPer1M: optionalNumber(row.cache_write_1h_usd_per_1m),
    serviceTierPrices: normalizeServiceTierPrices(parseJsonObject(row.service_tier_prices_json)),
    imageInputUsdPer1M: optionalNumber(row.image_input_usd_per_1m),
    imageOutputUsdPer1M: optionalNumber(row.image_output_usd_per_1m),
    audioInputUsdPer1M: optionalNumber(row.audio_input_usd_per_1m),
    audioOutputUsdPer1M: optionalNumber(row.audio_output_usd_per_1m),
    outputUsdPerImage: optionalNumber(row.output_usd_per_image),
    currency: 'USD',
    pricingNotes: optionalText(row.pricing_notes),
    capabilityNotes: optionalText(row.capability_notes),
    notes: optionalText(row.notes),
    createdBy: row.created_by,
    updatedBy: optionalText(row.updated_by),
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function normalizeProtocols(values: string[] | null | undefined): CustomProviderModelApiProtocol[] {
  return [...new Set((values ?? [])
    .map((value) => value.trim())
    .filter((value): value is CustomProviderModelApiProtocol => customProviderModelApiProtocols.has(value as CustomProviderModelApiProtocol)))]
}

function parseStringArray(raw: string | null | undefined): CustomProviderModelApiProtocol[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as unknown
    return Array.isArray(parsed) ? parsed.filter((item): item is CustomProviderModelApiProtocol => typeof item === 'string' && customProviderModelApiProtocols.has(item as CustomProviderModelApiProtocol)) : []
  } catch {
    return []
  }
}

function parseJsonObject(raw: string | null | undefined): unknown {
  if (!raw) return undefined
  try {
    return JSON.parse(raw)
  } catch {
    return undefined
  }
}

function normalizeCustomProviderModelCapabilities(
  providerCode: string,
  input: Pick<UpsertCustomProviderModelInput, 'mode' | 'supportedServiceTiers' | 'supportedReasoningEfforts' | 'defaultReasoningEffort' | 'serviceTierPrices'>
): {
  supportedServiceTiers: CustomProviderModelServiceTier[]
  supportedReasoningEfforts: CustomProviderModelReasoningEffort[]
  defaultReasoningEffort?: CustomProviderModelReasoningEffort
} {
  const supportedServiceTiers = normalizeEnumArray(
    input.supportedServiceTiers,
    customProviderModelCapabilityTokens,
    '服务等级'
  )
  const supportedReasoningEfforts = normalizeEnumArray(
    input.supportedReasoningEfforts,
    customProviderModelCapabilityTokens,
    '思考级别'
  )
  if (providerCode === 'gpt') {
    const gptServiceTiers = new Set(['priority', 'flex'])
    const gptReasoningEfforts = new Set(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'])
    if ((input.supportedServiceTiers?.length ?? 0) > 2
      || (input.supportedReasoningEfforts?.length ?? 0) > 7
      || supportedServiceTiers.some((value) => !gptServiceTiers.has(value))
      || supportedReasoningEfforts.some((value) => !gptReasoningEfforts.has(value))) {
      throw new Error('自定义模型参数无效')
    }
  }
  const isTextModel = (optionalText(input.mode) ?? 'text') === 'text'
  const serviceTierPriceKeys = Object.keys(normalizeServiceTierPrices(input.serviceTierPrices))
  if (!isTextModel && (supportedServiceTiers.length || supportedReasoningEfforts.length)) {
    throw new Error('只有文本自定义模型支持服务等级和思考能力配置')
  }
  if (!isTextModel && serviceTierPriceKeys.length) {
    throw new Error('只有文本自定义模型支持服务档位价格')
  }
  if (serviceTierPriceKeys.some((tier) => !supportedServiceTiers.includes(tier))) {
    throw new Error('服务档位价格必须属于模型支持的服务等级')
  }
  return {
    supportedServiceTiers,
    supportedReasoningEfforts,
    defaultReasoningEffort: undefined
  }
}

function normalizeEnumArray<TValue extends string>(
  values: string[] | null | undefined,
  allowedValues: ReadonlySet<TValue>,
  label: string
): TValue[] {
  const output: TValue[] = []
  const seen = new Set<TValue>()
  for (const raw of values ?? []) {
    const value = typeof raw === 'string' ? raw.trim() : ''
    if (!allowedValues.has(value as TValue)) {
      throw new Error(`${label}包含不支持的值：${value || String(raw)}`)
    }
    if (seen.has(value as TValue)) continue
    seen.add(value as TValue)
    output.push(value as TValue)
  }
  return output
}

function parseEnumArray<TValue extends string>(
  raw: string | null | undefined,
  allowedValues: ReadonlySet<TValue>
): TValue[] {
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw) as unknown
    return Array.isArray(parsed)
      ? parsed.filter((item): item is TValue => typeof item === 'string' && allowedValues.has(item as TValue))
      : []
  } catch {
    return []
  }
}

function optionalEnum<TValue extends string>(
  value: unknown,
  allowedValues: ReadonlySet<TValue>
): TValue | undefined {
  return typeof value === 'string' && allowedValues.has(value as TValue)
    ? value as TValue
    : undefined
}

function requiredText(value: unknown, message: string): string {
  const text = optionalText(value)
  if (!text) throw new Error(message)
  return text
}

function optionalText(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function optionalDate(value: unknown): string | undefined {
  const text = optionalText(value)
  return text && /^\d{4}-\d{2}-\d{2}$/.test(text) ? text : undefined
}

function optionalInteger(value: unknown): number | undefined {
  if (value === undefined || value === null || value === '') return undefined
  const number = Number(value)
  return Number.isInteger(number) && number >= 0 ? number : undefined
}

function optionalNumber(value: unknown): number | undefined {
  if (value === undefined || value === null || value === '') return undefined
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : undefined
}
