import type { DatabaseClient } from './database-client.js'

export interface ProviderModelDefaultReferenceCleanupTarget {
  providerCode: string
  sourceProviderCodes: string[]
}

export interface ProviderModelDefaultReferenceCleanupInput {
  model: string
  targets: ProviderModelDefaultReferenceCleanupTarget[]
  systemAccountId?: string
  clearSystemDefault: boolean
}

export async function clearUnavailableProviderModelDefaultReferencesInTransaction(
  client: DatabaseClient,
  input: ProviderModelDefaultReferenceCleanupInput
): Promise<string[]> {
  const model = input.model.trim()
  if (!model) return []
  const clearedProviderCodes = new Set<string>()
  for (const target of normalizeTargets(input.targets)) {
    const personalChanges = await clearPersonalDefaultReferences(client, {
      ...input,
      model,
      target
    })
    const systemChanges = input.clearSystemDefault
      ? await clearSystemDefaultReference(client, { model, target })
      : 0
    if (personalChanges + systemChanges > 0) clearedProviderCodes.add(target.providerCode)
  }
  return [...clearedProviderCodes]
}

async function clearPersonalDefaultReferences(
  client: DatabaseClient,
  input: ProviderModelDefaultReferenceCleanupInput & {
    model: string
    target: ProviderModelDefaultReferenceCleanupTarget
  }
): Promise<number> {
  const preferenceTable = client.dialect.qualifyTable('juhe_business', 'provider_default_health_check_models')
  const ownerPredicate = input.systemAccountId?.trim() ? 'AND preference.system_account_id = ?' : ''
  const availability = availableModelExistsSql(client, input.target.sourceProviderCodes, 'preference.system_account_id')
  const result = await client.execute(`
    DELETE FROM ${preferenceTable} AS preference
    WHERE preference.provider_code = ?
      AND preference.model = ?
      ${ownerPredicate}
      AND NOT (${availability.sql})
  `, [
    input.target.providerCode,
    input.model,
    ...(input.systemAccountId?.trim() ? [input.systemAccountId.trim()] : []),
    ...availability.params(input.model)
  ])
  return Number(result.changes ?? 0)
}

async function clearSystemDefaultReference(
  client: DatabaseClient,
  input: { model: string; target: ProviderModelDefaultReferenceCleanupTarget }
): Promise<number> {
  const systemDefaultTable = client.dialect.qualifyTable('juhe_business', 'provider_system_default_health_check_models')
  const availability = availableModelExistsSql(client, input.target.sourceProviderCodes)
  const result = await client.execute(`
    DELETE FROM ${systemDefaultTable} AS system_default
    WHERE system_default.provider_code = ?
      AND system_default.model = ?
      AND NOT (${availability.sql})
  `, [input.target.providerCode, input.model, ...availability.params(input.model)])
  return Number(result.changes ?? 0)
}

function availableModelExistsSql(
  client: DatabaseClient,
  sourceProviderCodes: string[],
  personalOwnerExpression?: string
): { sql: string; params: (model: string) => unknown[] } {
  const sourceCodes = [...new Set(sourceProviderCodes.map((code) => code.trim()).filter(Boolean))]
  if (!sourceCodes.length) return { sql: '0 = 1', params: () => [] }
  const builtInTable = client.dialect.qualifyTable('juhe_business', 'provider_model_catalog')
  const customTable = client.dialect.qualifyTable('juhe_business', 'custom_provider_models')
  const builtInProviderPredicate = providerCodesPredicate(client, 'built_in.provider_code', sourceCodes)
  const customProviderPredicate = providerCodesPredicate(client, 'custom.provider_code', sourceCodes)
  const todayExpression = client.driver === 'postgres' ? 'CURRENT_DATE::text' : "date('now')"
  const trim = client.driver === 'postgres' ? 'btrim' : 'trim'
  const customScopePredicate = personalOwnerExpression
    ? `(custom.scope = 'global' AND custom.system_account_id IS NULL)
          OR (custom.scope = 'personal' AND custom.system_account_id = ${personalOwnerExpression})`
    : "custom.scope = 'global' AND custom.system_account_id IS NULL"
  return {
    sql: `
      EXISTS (
        SELECT 1
        FROM ${builtInTable} AS built_in
        WHERE ${builtInProviderPredicate.sql}
          AND built_in.model = ?
          AND built_in.status = 'active'
          AND ${client.driver === 'postgres' ? 'built_in.catalog_visible = TRUE' : 'built_in.catalog_visible = 1'}
          AND (built_in.shutdown_date IS NULL OR ${trim}(built_in.shutdown_date) = '' OR built_in.shutdown_date > ${todayExpression})
          AND (built_in.mode IS NULL OR lower(${trim}(built_in.mode)) NOT IN ('image', 'audio'))
          AND ${textProtocolPredicate(client, 'built_in.supported_api_protocols_json')}
      )
      OR EXISTS (
        SELECT 1
        FROM ${customTable} AS custom
        WHERE ${customProviderPredicate.sql}
          AND custom.model = ?
          AND custom.status = 'active'
          AND (custom.shutdown_date IS NULL OR ${trim}(custom.shutdown_date) = '' OR custom.shutdown_date > ${todayExpression})
          AND (custom.mode IS NULL OR lower(${trim}(custom.mode)) NOT IN ('image', 'audio'))
          AND ${textProtocolPredicate(client, 'custom.supported_api_protocols_json')}
          AND (${customScopePredicate})
      )
    `,
    params: (model: string) => [
      ...builtInProviderPredicate.params,
      model,
      ...customProviderPredicate.params,
      model
    ]
  }
}

function providerCodesPredicate(
  client: DatabaseClient,
  column: string,
  sourceProviderCodes: string[]
): { sql: string; params: unknown[] } {
  return client.driver === 'postgres'
    ? { sql: `${column} = ANY(?::text[])`, params: [sourceProviderCodes] }
    : {
        sql: `${column} IN (${client.dialect.bindPlaceholders(sourceProviderCodes.length)})`,
        params: sourceProviderCodes
      }
}

function textProtocolPredicate(client: DatabaseClient, column: string): string {
  if (client.driver === 'postgres') {
    return `(jsonb_array_length(COALESCE(${column}::jsonb, '[]'::jsonb)) = 0
      OR COALESCE(${column}::jsonb, '[]'::jsonb) ?| ARRAY['chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content'])`
  }
  return `(json_array_length(COALESCE(${column}, '[]')) = 0
    OR EXISTS (
      SELECT 1
      FROM json_each(COALESCE(${column}, '[]')) AS protocol
      WHERE protocol.value IN ('chat_completions', 'responses', 'messages', 'generate_content', 'stream_generate_content')
    ))`
}

function normalizeTargets(targets: ProviderModelDefaultReferenceCleanupTarget[]): ProviderModelDefaultReferenceCleanupTarget[] {
  const normalized = new Map<string, Set<string>>()
  for (const target of targets) {
    const providerCode = target.providerCode.trim()
    if (!providerCode) continue
    const sourceCodes = normalized.get(providerCode) ?? new Set<string>()
    for (const sourceProviderCode of target.sourceProviderCodes) {
      const normalizedSourceCode = sourceProviderCode.trim()
      if (normalizedSourceCode) sourceCodes.add(normalizedSourceCode)
    }
    normalized.set(providerCode, sourceCodes)
  }
  return [...normalized].map(([providerCode, sourceProviderCodes]) => ({
    providerCode,
    sourceProviderCodes: [...sourceProviderCodes]
  }))
}
