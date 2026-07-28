import {
  ANTHROPIC_MESSAGES_FAMILY,
  ANTHROPIC_PROTOCOL_CODE,
  ANTHROPIC_PROTOCOL_VERSION,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_PROTOCOL_CODE,
  GEMINI_PROTOCOL_VERSION,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE,
  OPENAI_PROTOCOL_VERSION,
  OPENAI_RESPONSES_FAMILY,
  isHybridProviderCode,
  normalizeProviderToken
} from '../domain/provider-protocol.js'
import type { AccountModelMapping, AccountModelMappingEndpointFamily } from '../domain/types.js'
import type { DatabaseClient } from './database-client.js'
import { parseJsonArray } from './value-utils.js'

interface AccountModelValidationRow {
  provider_code: string
  model: string
  mode: string | null
  supported_api_protocols_json: string
  supported_service_tiers_json: string
  supported_reasoning_efforts_json: string
  scope_priority: number | string
  priced: number | boolean | string
}

interface ProtocolProviderRow {
  code: string
  protocol_code: string
  protocol_version: string
}

export interface AccountModelValidationFact {
  providerCode: string
  model: string
  supportedApiProtocols: string[]
  supportedServiceTiers: string[]
  supportedReasoningEfforts: string[]
  priced: boolean
}

export interface AccountModelValidationContext {
  accountModel(model: string): AccountModelValidationFact | undefined
  accountModelIds(): ReadonlySet<string>
  endpointModel(endpointFamily: AccountModelMappingEndpointFamily, model: string): AccountModelValidationFact | undefined
}

export interface AccountModelValidationContextInput {
  providerCode: string
  systemAccountId: string
  models: readonly string[]
  mappings?: readonly AccountModelMapping[]
}

const businessSchemaName = 'juhe_business'

export async function loadAccountModelValidationContextAsync(
  client: DatabaseClient,
  input: AccountModelValidationContextInput
): Promise<AccountModelValidationContext> {
  const providerCode = normalizeProviderToken(input.providerCode) ?? ''
  const models = uniqueTextList([
    ...input.models,
    ...(input.mappings ?? []).flatMap((mapping) => [mapping.sourceModel, mapping.upstreamModel])
  ])
  const endpointFamilies = uniqueTextList((input.mappings ?? []).flatMap((mapping) => [
    mapping.sourceEndpointFamily,
    mapping.upstreamEndpointFamily
  ])) as AccountModelMappingEndpointFamily[]
  if (!providerCode || models.length === 0) return emptyValidationContext()

  const protocolPairs = validationProtocolPairs(providerCode, endpointFamilies)
  const protocolProviders = protocolPairs.length
    ? await loadProtocolProvidersAsync(client, protocolPairs)
    : []
  const providerCodes = validationSourceProviderCodes(providerCode, protocolProviders)
  const builtInProviderCodes = providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE
    ? providerCodes.filter((code) => code !== providerCode)
    : providerCodes
  const rows = await loadValidationRowsAsync(client, {
    providerCodes,
    builtInProviderCodes,
    systemAccountId: input.systemAccountId,
    models
  })
  return buildValidationContext(providerCode, providerCodes, rows, protocolProviders)
}

function validationProtocolPairs(
  providerCode: string,
  endpointFamilies: readonly AccountModelMappingEndpointFamily[]
): Array<{ protocolCode: string; protocolVersion: string }> {
  if (providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE) {
    return [{ protocolCode: OPENAI_PROTOCOL_CODE, protocolVersion: OPENAI_PROTOCOL_VERSION }]
  }
  if (!isHybridProviderCode(providerCode)) return []
  const pairs = new Map<string, { protocolCode: string; protocolVersion: string }>()
  for (const family of endpointFamilies) {
    const pair = endpointFamilyProtocolPair(family)
    pairs.set(`${pair.protocolCode}\n${pair.protocolVersion}`, pair)
  }
  return [...pairs.values()]
}

function endpointFamilyProtocolPair(endpointFamily: AccountModelMappingEndpointFamily): {
  protocolCode: string
  protocolVersion: string
} {
  if (endpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return { protocolCode: ANTHROPIC_PROTOCOL_CODE, protocolVersion: ANTHROPIC_PROTOCOL_VERSION }
  }
  if (endpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || endpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return { protocolCode: GEMINI_PROTOCOL_CODE, protocolVersion: GEMINI_PROTOCOL_VERSION }
  }
  return { protocolCode: OPENAI_PROTOCOL_CODE, protocolVersion: OPENAI_PROTOCOL_VERSION }
}

async function loadProtocolProvidersAsync(
  client: DatabaseClient,
  pairs: readonly { protocolCode: string; protocolVersion: string }[]
): Promise<ProtocolProviderRow[]> {
  if (!pairs.length) return []
  const clauses = pairs.map(() => '(ppp.protocol_code = ? AND ppp.protocol_version = ?)')
  const params = pairs.flatMap((pair) => [pair.protocolCode, pair.protocolVersion])
  return client.query<ProtocolProviderRow>(`
    SELECT DISTINCT p.code, ppp.protocol_code, ppp.protocol_version
    FROM ${validationTable(client, 'providers')} p
    INNER JOIN ${validationTable(client, 'provider_protocol_profiles')} ppp
      ON ppp.provider_code = p.code
    WHERE p.enabled = 1
      AND ppp.enabled = 1
      AND (${clauses.join(' OR ')})
    ORDER BY p.code ASC, ppp.protocol_code ASC, ppp.protocol_version ASC
  `, params)
}

function validationSourceProviderCodes(providerCode: string, rows: readonly ProtocolProviderRow[]): string[] {
  if (providerCode !== OPENAI_COMPATIBLE_PROVIDER_CODE && !isHybridProviderCode(providerCode)) {
    return [providerCode]
  }
  const codes = rows
    .map((row) => normalizeProviderToken(row.code))
    .filter((code): code is string => Boolean(code) && code !== providerCode)
  if (providerCode === OPENAI_COMPATIBLE_PROVIDER_CODE) codes.push(providerCode)
  return [...new Set(codes)]
}

async function loadValidationRowsAsync(
  client: DatabaseClient,
  input: {
    providerCodes: readonly string[]
    builtInProviderCodes: readonly string[]
    systemAccountId: string
    models: readonly string[]
  }
): Promise<AccountModelValidationRow[]> {
  if (!input.providerCodes.length || !input.models.length) return []
  const builtInProviderPredicate = input.builtInProviderCodes.length
    ? `provider_code IN (${client.dialect.bindPlaceholders(input.builtInProviderCodes.length)})`
    : '1 = 0'
  const customProviderPlaceholders = client.dialect.bindPlaceholders(input.providerCodes.length)
  const modelPlaceholders = client.dialect.bindPlaceholders(input.models.length)
  const activeDatePredicate = client.driver === 'postgres'
    ? "(shutdown_date IS NULL OR btrim(shutdown_date) = '' OR shutdown_date > CURRENT_DATE::text)"
    : "(shutdown_date IS NULL OR trim(shutdown_date) = '' OR shutdown_date > date('now'))"
  const visiblePredicate = client.driver === 'postgres' ? 'catalog_visible = TRUE' : 'catalog_visible = 1'
  const pricedExpression = providerModelPricedExpression()
  const builtInTable = validationTable(client, 'provider_model_catalog')
  const customTable = validationTable(client, 'custom_provider_models')
  const params = [
    ...input.builtInProviderCodes,
    ...input.models,
    ...input.providerCodes,
    ...input.models,
    input.systemAccountId
  ]
  return client.query<AccountModelValidationRow>(`
    SELECT provider_code, model, mode, supported_api_protocols_json,
      supported_service_tiers_json, supported_reasoning_efforts_json,
      scope_priority, priced
    FROM (
      SELECT provider_code, model, mode, supported_api_protocols_json,
        supported_service_tiers_json, supported_reasoning_efforts_json,
        1 AS scope_priority,
        CASE WHEN ${pricedExpression} THEN 1 ELSE 0 END AS priced
      FROM ${builtInTable}
      WHERE ${builtInProviderPredicate}
        AND model IN (${modelPlaceholders})
        AND status = 'active'
        AND ${visiblePredicate}
        AND ${activeDatePredicate}
      UNION ALL
      SELECT provider_code, model, mode, supported_api_protocols_json,
        supported_service_tiers_json, supported_reasoning_efforts_json,
        CASE scope WHEN 'personal' THEN 3 ELSE 2 END AS scope_priority,
        CASE WHEN ${pricedExpression} THEN 1 ELSE 0 END AS priced
      FROM ${customTable}
      WHERE provider_code IN (${customProviderPlaceholders})
        AND model IN (${modelPlaceholders})
        AND status = 'active'
        AND ${visiblePredicate}
        AND ${activeDatePredicate}
        AND ((scope = 'global' AND system_account_id IS NULL)
          OR (scope = 'personal' AND system_account_id = ?))
    ) validation_models
    ORDER BY provider_code ASC, model ASC, scope_priority ASC
  `, params)
}

function providerModelPricedExpression(): string {
  return `(
    input_usd_per_1m IS NOT NULL
    OR output_usd_per_1m IS NOT NULL
    OR cached_input_usd_per_1m IS NOT NULL
    OR cache_write_usd_per_1m IS NOT NULL
    OR cache_write_1h_usd_per_1m IS NOT NULL
    OR cache_storage_usd_per_1m_per_hour IS NOT NULL
    OR image_input_usd_per_1m IS NOT NULL
    OR image_output_usd_per_1m IS NOT NULL
    OR audio_input_usd_per_1m IS NOT NULL
    OR audio_output_usd_per_1m IS NOT NULL
    OR output_usd_per_image IS NOT NULL
    OR service_tier_prices_json <> '{}'
  )`
}

function buildValidationContext(
  providerCode: string,
  sourceProviderCodes: readonly string[],
  rows: readonly AccountModelValidationRow[],
  protocolProviders: readonly ProtocolProviderRow[]
): AccountModelValidationContext {
  const factsByProviderAndModel = new Map<string, { fact: AccountModelValidationFact; priority: number }>()
  const accountFacts = new Map<string, { fact: AccountModelValidationFact; priority: number; sourceRank: number }>()
  const sourceRanks = new Map(sourceProviderCodes.map((code, index) => [code, index]))
  for (const row of rows) {
    const fact = validationFactFromRow(row)
    if (!isSupportedValidationModel(row.mode, fact.model, fact.supportedApiProtocols)) continue
    const priority = Number(row.scope_priority)
    const providerKey = `${fact.providerCode}\n${fact.model}`
    const providerPrevious = factsByProviderAndModel.get(providerKey)
    if (!providerPrevious || priority >= providerPrevious.priority) {
      factsByProviderAndModel.set(providerKey, { fact, priority })
    }
    if (!isHybridProviderCode(providerCode)) {
      const accountPrevious = accountFacts.get(fact.model)
      const sourceRank = sourceRanks.get(fact.providerCode) ?? -1
      if (!accountPrevious || priority > accountPrevious.priority
        || (priority === accountPrevious.priority && sourceRank >= accountPrevious.sourceRank)) {
        accountFacts.set(fact.model, { fact, priority, sourceRank })
      }
    }
  }
  const providerCodesByProtocol = new Map<string, Set<string>>()
  for (const row of protocolProviders) {
    const key = `${row.protocol_code}\n${row.protocol_version}`
    const codes = providerCodesByProtocol.get(key) ?? new Set<string>()
    codes.add(row.code)
    providerCodesByProtocol.set(key, codes)
  }
  const accountModelIds = new Set(accountFacts.keys())
  return {
    accountModel(model) {
      return accountFacts.get(model.trim())?.fact
    },
    accountModelIds() {
      return accountModelIds
    },
    endpointModel(endpointFamily, model) {
      if (!isHybridProviderCode(providerCode)) {
        const fact = accountFacts.get(model.trim())?.fact
        return fact?.supportedApiProtocols.includes(endpointFamily) ? fact : undefined
      }
      const pair = endpointFamilyProtocolPair(endpointFamily)
      const providerCodes = providerCodesByProtocol.get(`${pair.protocolCode}\n${pair.protocolVersion}`) ?? new Set<string>()
      for (const sourceProviderCode of providerCodes) {
        const fact = factsByProviderAndModel.get(`${sourceProviderCode}\n${model.trim()}`)?.fact
        if (fact?.supportedApiProtocols.includes(endpointFamily)) return fact
      }
      return undefined
    }
  }
}

function emptyValidationContext(): AccountModelValidationContext {
  const ids = new Set<string>()
  return {
    accountModel: () => undefined,
    accountModelIds: () => ids,
    endpointModel: () => undefined
  }
}

function validationFactFromRow(row: AccountModelValidationRow): AccountModelValidationFact {
  return {
    providerCode: row.provider_code,
    model: row.model.trim(),
    supportedApiProtocols: uniqueTextList(parseJsonArray(row.supported_api_protocols_json)),
    supportedServiceTiers: uniqueTextList(parseJsonArray(row.supported_service_tiers_json)),
    supportedReasoningEfforts: uniqueTextList(parseJsonArray(row.supported_reasoning_efforts_json)),
    priced: databaseBoolean(row.priced)
  }
}

function isSupportedValidationModel(mode: string | null, model: string, protocols: readonly string[]): boolean {
  const normalizedMode = mode?.trim().toLowerCase()
  if (normalizedMode === 'audio' || normalizedMode === 'audio_speech' || normalizedMode === 'audio_transcription') return false
  if (protocols.includes('realtime')) return false
  if (protocols.length === 1 && protocols[0] === 'audio') return false
  return !/(?:^|[-_.])(audio|realtime|transcribe|tts|whisper)(?:$|[-_.])/.test(model.trim().toLowerCase())
}

function validationTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function uniqueTextList(values: readonly string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const normalized = value.trim()
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    output.push(normalized)
  }
  return output
}

function databaseBoolean(value: number | boolean | string): boolean {
  return value === true || value === 1 || value === '1' || value === 'true'
}
