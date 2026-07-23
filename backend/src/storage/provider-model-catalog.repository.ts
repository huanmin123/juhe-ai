import type { SQLInputValue } from 'node:sqlite'

import type { ProviderModelPriceSet, ProviderModelPricing } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { notifyCommittedModelCacheInvalidationAsync } from './model-cache-sync-warning.js'

interface ProviderModelCatalogRow {
  id: string
  provider_code: string
  model: string
  status: 'active' | 'disabled'
  mode?: string | null
  catalog_order?: number | null
  release_date?: string | null
  shutdown_date?: string | null
  supported_api_protocols_json: string
  supported_service_tiers_json: string
  supported_reasoning_efforts_json: string
  default_reasoning_effort?: string | null
  codex_supported_reasoning_levels_json: string
  codex_default_reasoning_level?: string | null
  codex_multi_agent_version?: 'v2' | null
  context_window_tokens?: number | null
  max_input_tokens?: number | null
  max_output_tokens?: number | null
  max_tokens?: number | null
  input_usd_per_1m?: number | null
  output_usd_per_1m?: number | null
  cached_input_usd_per_1m?: number | null
  cache_write_usd_per_1m?: number | null
  cache_write_1h_usd_per_1m?: number | null
  service_tier_prices_json: string
  long_context_input_token_threshold?: number | null
  long_context_input_token_threshold_inclusive: number | boolean
  long_context_input_cost_multiplier?: number | null
  long_context_output_cost_multiplier?: number | null
  image_input_usd_per_1m?: number | null
  image_output_usd_per_1m?: number | null
  audio_input_usd_per_1m?: number | null
  audio_output_usd_per_1m?: number | null
  output_usd_per_image?: number | null
  supports_prompt_caching: number | boolean
  catalog_visible: number | boolean
  source: string
  created_at: string
  updated_at: string
}

export interface BuiltInProviderModelRecord extends ProviderModelPricing {
  id: string
  status: 'active' | 'disabled'
  catalogVisible: boolean
  createdAt: string
  updatedAt: string
  codexSupportedReasoningLevels?: Array<'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'ultra'>
  codexDefaultReasoningLevel?: 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max' | 'ultra'
  codexMultiAgentVersion?: 'v2'
  longContextInputTokenThreshold?: number
  longContextInputTokenThresholdInclusive?: boolean
  longContextInputCostMultiplier?: number
  longContextOutputCostMultiplier?: number
}

export interface ProviderModelTestCatalogRecord {
  id: string
  providerCode: string
  model: string
  mode?: string
  catalogOrder?: number
  releaseDate?: string
  supportedApiProtocols: ProviderModelPricing['supportedApiProtocols']
  supportedServiceTiers: string[]
  supportedReasoningEfforts: string[]
  defaultReasoningEffort?: string
}

export interface ProviderModelOptionRow {
  id: string
  providerCode: string
  model: string
  scope: 'built_in'
  releaseDate?: string
}

export async function listBuiltInProviderModelOptionsAsync(input: {
  providerCodes: string[]
  keyword?: string
  limit: number
  selectedIds?: string[]
}): Promise<ProviderModelOptionRow[]> {
  const providerCodes = [...new Set(input.providerCodes.map((code) => code.trim()).filter(Boolean))]
  if (!providerCodes.length) return []
  const selectedIds = [...new Set((input.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].slice(0, 50)
  const keyword = input.keyword?.trim().toLowerCase()
  const resultLimit = Math.min(100, Math.max(1, input.limit + selectedIds.length))
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : undefined
  const table = client ? 'juhe_business.provider_model_catalog' : 'provider_model_catalog'
  const providerFilter = client
    ? 'provider_code = ANY(?::text[])'
    : `provider_code IN (${providerCodes.map(() => '?').join(', ')})`
  const selectedFilter = selectedIds.length
    ? client ? 'model = ANY(?::text[])' : `model IN (${selectedIds.map(() => '?').join(', ')})`
    : ''
  const keywordFilter = keyword ? 'lower(model) LIKE ?' : ''
  const whereParts = [providerFilter, 'status = \'active\'', client ? 'catalog_visible = TRUE' : 'catalog_visible = 1', client
    ? '(shutdown_date IS NULL OR btrim(shutdown_date) = \'\' OR shutdown_date > CURRENT_DATE::text)'
    : '(shutdown_date IS NULL OR trim(shutdown_date) = \'\' OR shutdown_date > date(\'now\'))']
  if (keywordFilter) {
    whereParts.push(`(${[selectedFilter, keywordFilter].filter(Boolean).join(' OR ')})`)
  }
  const order = selectedIds.length
    ? `CASE WHEN ${client ? 'model = ANY(?::text[])' : `model IN (${selectedIds.map(() => '?').join(', ')})`} THEN 0 ELSE 1 END, `
    : ''
  const sql = `
    WITH ranked_options AS (
      SELECT provider_code, model, id, mode, release_date, supported_api_protocols_json,
        ROW_NUMBER() OVER (PARTITION BY model ORDER BY provider_code ASC, id ASC) AS option_rank
      FROM ${table}
      WHERE ${whereParts.join('\n        AND ')}
    )
    SELECT provider_code, model, mode, release_date, supported_api_protocols_json
    FROM ranked_options
    WHERE option_rank = 1
    ORDER BY ${order}
      CASE WHEN release_date IS NULL OR trim(release_date) = '' THEN 1 ELSE 0 END ASC,
      release_date DESC, lower(model) ASC, provider_code ASC, id ASC
    LIMIT ?
  `
  const params: unknown[] = client ? [providerCodes] : [...providerCodes]
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
    ? await client.query<{ provider_code: string; model: string; mode?: string | null; release_date?: string | null; supported_api_protocols_json?: string | null }>(sql, params)
    : getBusinessDatabase().prepare(sql).all(...params as SQLInputValue[]) as unknown as Array<{
        provider_code: string
        model: string
        mode?: string | null
        release_date?: string | null
        supported_api_protocols_json?: string | null
      }>
  return rows.map((row) => ({
    id: row.model,
    providerCode: row.provider_code,
    model: row.model,
    scope: 'built_in' as const,
    mode: text(row.mode),
    releaseDate: text(row.release_date),
    supportedApiProtocols: parseProviderModelOptionProtocols(row.supported_api_protocols_json)
  }))
}

interface ProviderModelTestCatalogRow {
  id: string
  provider_code: string
  model: string
  mode?: string | null
  catalog_order?: number | null
  release_date?: string | null
  supported_api_protocols_json: string
  supported_service_tiers_json: string
  supported_reasoning_efforts_json: string
  default_reasoning_effort?: string | null
}

export interface ProviderModelPricePatch {
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
}

export interface ProviderModelConfigurationPatch extends ProviderModelPricePatch {
  status?: 'active' | 'disabled'
  catalogVisible?: boolean
  mode?: 'text' | 'image' | 'audio' | null
  supportedApiProtocols?: string[]
  supportedServiceTiers?: string[]
  supportedReasoningEfforts?: string[]
  defaultReasoningEffort?: string | null
  releaseDate?: string | null
  shutdownDate?: string | null
  contextWindowTokens?: number | null
  maxInputTokens?: number | null
  maxOutputTokens?: number | null
}

export function listBuiltInProviderModels(providerCodes: string[]): BuiltInProviderModelRecord[] {
  if (!providerCodes.length) return []
  const placeholders = providerCodes.map(() => '?').join(', ')
  const rows = getBusinessDatabase().prepare(`
    SELECT ${columns()} FROM provider_model_catalog
    WHERE provider_code IN (${placeholders})
      AND status = 'active'
      AND catalog_visible = 1
      AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > date('now'))
    ORDER BY provider_code, catalog_order, model, id
  `).all(...providerCodes as SQLInputValue[]) as unknown as ProviderModelCatalogRow[]
  return rows.map(fromRow)
}

export async function listBuiltInProviderModelsAsync(providerCodes: string[]): Promise<BuiltInProviderModelRecord[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') return listBuiltInProviderModels(providerCodes)
  if (!providerCodes.length) return []
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<ProviderModelCatalogRow>(`
    SELECT ${columns()} FROM juhe_business.provider_model_catalog
    WHERE provider_code = ANY(?::text[])
      AND status = 'active'
      AND catalog_visible = TRUE
      AND (shutdown_date IS NULL OR btrim(shutdown_date) = '' OR shutdown_date > CURRENT_DATE::text)
    ORDER BY provider_code, catalog_order, model, id
  `, [providerCodes])
  return rows.map(fromRow)
}

export async function listBuiltInProviderModelTestCatalogAsync(
  providerCodes: string[]
): Promise<ProviderModelTestCatalogRecord[]> {
  if (!providerCodes.length) return []
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : undefined
  const table = client ? 'juhe_business.provider_model_catalog' : 'provider_model_catalog'
  const providerFilter = client ? 'provider_code = ANY(?::text[])' : `provider_code IN (${providerCodes.map(() => '?').join(', ')})`
  const rows = client
    ? await client.query<ProviderModelTestCatalogRow>(`
        SELECT id, provider_code, model, mode, catalog_order, release_date,
          supported_api_protocols_json, supported_service_tiers_json,
          supported_reasoning_efforts_json, default_reasoning_effort
        FROM ${table}
        WHERE ${providerFilter}
          AND status = 'active'
          AND catalog_visible = TRUE
          AND (shutdown_date IS NULL OR btrim(shutdown_date) = '' OR shutdown_date > CURRENT_DATE::text)
        ORDER BY provider_code ASC, catalog_order ASC, model ASC, id ASC
      `, [providerCodes])
    : getBusinessDatabase().prepare(`
        SELECT id, provider_code, model, mode, catalog_order, release_date,
          supported_api_protocols_json, supported_service_tiers_json,
          supported_reasoning_efforts_json, default_reasoning_effort
        FROM ${table}
        WHERE ${providerFilter}
          AND status = 'active'
          AND catalog_visible = 1
          AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > date('now'))
        ORDER BY provider_code ASC, catalog_order ASC, model ASC, id ASC
      `).all(...providerCodes as SQLInputValue[]) as unknown as ProviderModelTestCatalogRow[]
  return rows.map(providerModelTestCatalogRecordFromRow)
}

export async function findBuiltInProviderModelTestCatalogAsync(
  providerCodes: string[],
  model: string,
  projection: 'test' | 'protocols' = 'test'
): Promise<ProviderModelTestCatalogRecord[]> {
  if (!providerCodes.length || !model.trim()) return []
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : undefined
  const table = client ? 'juhe_business.provider_model_catalog' : 'provider_model_catalog'
  const providerFilter = client ? 'provider_code = ANY(?::text[])' : `provider_code IN (${providerCodes.map(() => '?').join(', ')})`
  const params: unknown[] = client ? [providerCodes, model.trim()] : [...providerCodes, model.trim()]
  const selectedColumns = projection === 'protocols'
    ? 'id, provider_code, model, mode, supported_api_protocols_json'
    : `id, provider_code, model, mode, catalog_order, release_date,
          supported_api_protocols_json, supported_service_tiers_json,
          supported_reasoning_efforts_json, default_reasoning_effort`
  const rows = client
    ? await client.query<ProviderModelTestCatalogRow>(`
        SELECT ${selectedColumns}
        FROM ${table}
        WHERE ${providerFilter}
          AND model = ?
          AND status = 'active'
          AND catalog_visible = TRUE
          AND (shutdown_date IS NULL OR btrim(shutdown_date) = '' OR shutdown_date > CURRENT_DATE::text)
        ORDER BY provider_code ASC, catalog_order ASC, model ASC, id ASC
      `, params)
    : getBusinessDatabase().prepare(`
        SELECT ${selectedColumns}
        FROM ${table}
        WHERE ${providerFilter}
          AND model = ?
          AND status = 'active'
          AND catalog_visible = 1
          AND (shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > date('now'))
        ORDER BY provider_code ASC, catalog_order ASC, model ASC, id ASC
      `).all(...params as SQLInputValue[]) as unknown as ProviderModelTestCatalogRow[]
  return rows.map(providerModelTestCatalogRecordFromRow)
}

export async function findBuiltInProviderModelByIdAsync(id: string): Promise<BuiltInProviderModelRecord | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const row = getBusinessDatabase().prepare(`SELECT ${columns()} FROM provider_model_catalog WHERE id = ? LIMIT 1`)
      .get(id) as unknown as ProviderModelCatalogRow | undefined
    return row ? fromRow(row) : undefined
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const row = await client.one<ProviderModelCatalogRow>(`SELECT ${columns()} FROM juhe_business.provider_model_catalog WHERE id = ? LIMIT 1`, [id])
  return row ? fromRow(row) : undefined
}

export async function updateBuiltInProviderModelPricesAsync(id: string, patch: ProviderModelPricePatch): Promise<BuiltInProviderModelRecord | undefined> {
  return updateBuiltInProviderModelConfigurationAsync(id, patch)
}

export async function updateBuiltInProviderModelConfigurationAsync(id: string, patch: ProviderModelConfigurationPatch): Promise<BuiltInProviderModelRecord | undefined> {
  const { assignments, params } = configurationPatchAssignments(patch)
  if (!assignments.length) return findBuiltInProviderModelByIdAsync(id)
  const marksManualOverride = Object.keys(patch).some((field) => field !== 'status' && field !== 'catalogVisible')
  const sourceAssignment = marksManualOverride
    ? ', source = ?'
    : ", source = CASE WHEN source = 'manual-override' THEN source ELSE ? END"
  const sql = `UPDATE provider_model_catalog SET ${assignments.join(', ')}${sourceAssignment}, updated_at = ? WHERE id = ?`
  const writeParams = [...params, marksManualOverride ? 'manual-override' : 'manual-visibility-override', nowIso(), id]
  if (runtimeConfig.databaseDriver !== 'postgres') {
    getBusinessDatabase().prepare(sql).run(...writeParams as SQLInputValue[])
  } else {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    await client.execute(sql.replace('provider_model_catalog', 'juhe_business.provider_model_catalog'), writeParams)
  }
  const saved = await findBuiltInProviderModelByIdAsync(id)
  if (saved) await notifyCommittedModelCacheInvalidationAsync('provider_model_configuration_updated')
  return saved
}

function configurationPatchAssignments(patch: ProviderModelConfigurationPatch): { assignments: string[]; params: unknown[] } {
  const assignments: string[] = []
  const params: unknown[] = []
  const addValue = (field: keyof ProviderModelConfigurationPatch, column: string, normalize: (value: unknown) => unknown = (value) => value) => {
    if (!Object.prototype.hasOwnProperty.call(patch, field)) return
    assignments.push(`${column} = ?`)
    params.push(normalize(patch[field]))
  }
  addValue('status', 'status')
  addValue('catalogVisible', 'catalog_visible', (value) => value ? 1 : 0)
  addValue('mode', 'mode', nullableText)
  addValue('supportedApiProtocols', 'supported_api_protocols_json', stringListJSON)
  addValue('supportedServiceTiers', 'supported_service_tiers_json', stringListJSON)
  addValue('supportedReasoningEfforts', 'supported_reasoning_efforts_json', stringListJSON)
  addValue('defaultReasoningEffort', 'default_reasoning_effort', nullableText)
  addValue('releaseDate', 'release_date', nullableText)
  addValue('shutdownDate', 'shutdown_date', nullableText)
  addValue('contextWindowTokens', 'context_window_tokens', nullableInteger)
  addValue('maxInputTokens', 'max_input_tokens', nullableInteger)
  addValue('maxOutputTokens', 'max_output_tokens', nullableInteger)
  const addPrice = (field: keyof ProviderModelPricePatch, column: string) => {
    if (!Object.prototype.hasOwnProperty.call(patch, field)) return
    assignments.push(`${column} = ?`)
    params.push(nullablePrice(patch[field]))
  }
  addPrice('inputUsdPer1M', 'input_usd_per_1m')
  addPrice('outputUsdPer1M', 'output_usd_per_1m')
  addPrice('cachedInputUsdPer1M', 'cached_input_usd_per_1m')
  addPrice('cacheWriteUsdPer1M', 'cache_write_usd_per_1m')
  addPrice('cacheWrite1hUsdPer1M', 'cache_write_1h_usd_per_1m')
  if (Object.prototype.hasOwnProperty.call(patch, 'serviceTierPrices')) {
    assignments.push('service_tier_prices_json = ?')
    params.push(JSON.stringify(normalizeServiceTierPrices(patch.serviceTierPrices)))
  }
  addPrice('imageInputUsdPer1M', 'image_input_usd_per_1m')
  addPrice('imageOutputUsdPer1M', 'image_output_usd_per_1m')
  addPrice('audioInputUsdPer1M', 'audio_input_usd_per_1m')
  addPrice('audioOutputUsdPer1M', 'audio_output_usd_per_1m')
  addPrice('outputUsdPerImage', 'output_usd_per_image')
  return { assignments, params }
}

function nullableText(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value.trim() : null
}

function nullableInteger(value: unknown): number | null {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : null
}

function stringListJSON(value: unknown): string {
  return JSON.stringify(Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [])
}

function nullablePrice(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}

export function normalizeServiceTierPrices(value: unknown): Record<string, ProviderModelPriceSet> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  const result: Record<string, ProviderModelPriceSet> = {}
  for (const [rawTier, rawPrices] of Object.entries(value)) {
    const tier = rawTier.trim()
    if (!tier || tier === 'default' || tier === 'standard' || tier.length > 64 || !rawPrices || typeof rawPrices !== 'object' || Array.isArray(rawPrices)) continue
    const prices = rawPrices as Record<string, unknown>
    const normalized: ProviderModelPriceSet = {}
    for (const key of priceKeys) {
      const price = nullablePrice(prices[key])
      if (price !== null) normalized[key] = price
    }
    if (Object.keys(normalized).length) result[tier] = normalized
  }
  return result
}

const priceKeys = [
  'inputUsdPer1M', 'outputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M', 'cacheWrite1hUsdPer1M',
  'imageInputUsdPer1M', 'imageOutputUsdPer1M', 'audioInputUsdPer1M', 'audioOutputUsdPer1M', 'outputUsdPerImage'
] as const

function columns(): string {
  return `id, provider_code, model, status, mode, catalog_order, release_date, shutdown_date,
    supported_api_protocols_json, supported_service_tiers_json, supported_reasoning_efforts_json,
    default_reasoning_effort, codex_supported_reasoning_levels_json, codex_default_reasoning_level,
    codex_multi_agent_version, context_window_tokens, max_input_tokens, max_output_tokens, max_tokens,
    input_usd_per_1m, output_usd_per_1m, cached_input_usd_per_1m, cache_write_usd_per_1m,
    cache_write_1h_usd_per_1m, service_tier_prices_json, long_context_input_token_threshold,
    long_context_input_token_threshold_inclusive,
    long_context_input_cost_multiplier, long_context_output_cost_multiplier, image_input_usd_per_1m,
    image_output_usd_per_1m, audio_input_usd_per_1m, audio_output_usd_per_1m, output_usd_per_image,
    supports_prompt_caching, catalog_visible, source, created_at, updated_at`
}

function fromRow(row: ProviderModelCatalogRow): BuiltInProviderModelRecord {
  const serviceTiers = stringArray(row.supported_service_tiers_json)
  return {
    id: row.id, providerCode: row.provider_code, model: row.model, status: row.status,
    mode: text(row.mode), catalogOrder: integer(row.catalog_order), releaseDate: text(row.release_date), shutdownDate: text(row.shutdown_date),
    supportedApiProtocols: stringArray(row.supported_api_protocols_json) as ProviderModelPricing['supportedApiProtocols'],
    supportedServiceTiers: serviceTiers as ProviderModelPricing['supportedServiceTiers'],
    supportedReasoningEfforts: stringArray(row.supported_reasoning_efforts_json) as ProviderModelPricing['supportedReasoningEfforts'],
    defaultReasoningEffort: text(row.default_reasoning_effort) as ProviderModelPricing['defaultReasoningEffort'],
    codexSupportedReasoningLevels: stringArray(row.codex_supported_reasoning_levels_json) as BuiltInProviderModelRecord['codexSupportedReasoningLevels'],
    codexDefaultReasoningLevel: text(row.codex_default_reasoning_level) as BuiltInProviderModelRecord['codexDefaultReasoningLevel'],
    codexMultiAgentVersion: text(row.codex_multi_agent_version) as 'v2' | undefined,
    inputUsdPer1M: number(row.input_usd_per_1m), outputUsdPer1M: number(row.output_usd_per_1m),
    cachedInputUsdPer1M: number(row.cached_input_usd_per_1m), cacheWriteUsdPer1M: number(row.cache_write_usd_per_1m),
    cacheWrite1hUsdPer1M: number(row.cache_write_1h_usd_per_1m), serviceTierPrices: normalizeServiceTierPrices(json(row.service_tier_prices_json)),
    imageInputUsdPer1M: number(row.image_input_usd_per_1m), imageOutputUsdPer1M: number(row.image_output_usd_per_1m),
    audioInputUsdPer1M: number(row.audio_input_usd_per_1m), audioOutputUsdPer1M: number(row.audio_output_usd_per_1m),
    outputUsdPerImage: number(row.output_usd_per_image), contextWindowTokens: integer(row.context_window_tokens),
    maxInputTokens: integer(row.max_input_tokens), maxOutputTokens: integer(row.max_output_tokens), maxTokens: integer(row.max_tokens),
    longContextInputTokenThreshold: integer(row.long_context_input_token_threshold),
    longContextInputTokenThresholdInclusive: Boolean(row.long_context_input_token_threshold_inclusive),
    longContextInputCostMultiplier: number(row.long_context_input_cost_multiplier),
    longContextOutputCostMultiplier: number(row.long_context_output_cost_multiplier),
    supportsPromptCaching: Boolean(row.supports_prompt_caching), supportsServiceTier: serviceTiers.length > 0,
    catalogVisible: Boolean(row.catalog_visible), source: row.source, createdAt: row.created_at, updatedAt: row.updated_at
  }
}

function providerModelTestCatalogRecordFromRow(row: ProviderModelTestCatalogRow): ProviderModelTestCatalogRecord {
  return {
    id: row.id,
    providerCode: row.provider_code,
    model: row.model,
    mode: text(row.mode),
    catalogOrder: integer(row.catalog_order),
    releaseDate: text(row.release_date),
    supportedApiProtocols: stringArray(row.supported_api_protocols_json) as ProviderModelPricing['supportedApiProtocols'],
    supportedServiceTiers: stringArray(row.supported_service_tiers_json),
    supportedReasoningEfforts: stringArray(row.supported_reasoning_efforts_json),
    defaultReasoningEffort: text(row.default_reasoning_effort)
  }
}

function parseProviderModelOptionProtocols(value: unknown): string[] {
  if (typeof value !== 'string') return []
  const parsed = json(value)
  return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string') : []
}

function json(raw: string): unknown { try { return JSON.parse(raw) } catch { return undefined } }
function stringArray(raw: string): string[] { const value = json(raw); return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [] }
function text(value: unknown): string | undefined { return typeof value === 'string' && value.trim() ? value.trim() : undefined }
function number(value: unknown): number | undefined { return typeof value === 'number' && Number.isFinite(value) ? value : undefined }
function integer(value: unknown): number | undefined { const valueNumber = number(value); return valueNumber === undefined ? undefined : Math.trunc(valueNumber) }
