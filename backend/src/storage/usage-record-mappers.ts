import type { UsageRecordSummary } from './usage-records.repository.js'
import { loadAccountNameMap, loadApiKeyNameMap, loadGroupNameMap } from './repository-lookups.js'
import { optionalString, parseOptionalJsonObject } from './value-utils.js'

export type UsageRecordRow = Record<string, unknown>

export function hydrateUsageRecordNames(rows: UsageRecordRow[]): UsageRecordRow[] {
  if (!rows.length) return rows
  const apiKeyNames = loadApiKeyNameMap(rows.map((row) => optionalString(row.api_key_id) ?? ''))
  const groupNames = loadGroupNameMap(rows.map((row) => optionalString(row.group_id) ?? ''))
  const recordAccountNames = loadAccountNameMap(rows.map((row) => optionalString(row.account_id) ?? ''))
  return rows.map((row) => ({
    ...row,
    api_key_name: optionalString(row.api_key_name) ?? (row.api_key_id ? apiKeyNames.get(String(row.api_key_id)) : undefined),
    group_name: optionalString(row.group_name) ?? (row.group_id ? groupNames.get(String(row.group_id)) : undefined),
    account_name: optionalString(row.account_name) ?? (row.account_id ? recordAccountNames.get(String(row.account_id)) : undefined)
  }))
}

export function usageRecordSummaryFromRow(
  row: UsageRecordRow,
  shouldIncludeSystemAccountFields: boolean,
  accountNames: Map<string, string>,
  includeSnapshots = false
): UsageRecordSummary {
  const requestSnapshot = includeSnapshots ? parseOptionalJsonObject(row.request_snapshot_json) : undefined
  const inputTokens = numberValue(row.input_tokens)
  const outputTokens = numberValue(row.output_tokens)
  const cacheReadTokens = numberValue(row.cache_read_tokens)
  const cacheReadCostUsd = numberValue(row.cache_read_cost_usd)
  const inputImageTokens = numberValue(row.input_image_tokens)
  const outputImageTokens = numberValue(row.output_image_tokens)
  const model = optionalString(row.model)
  const stream = row.stream === 1
  const statusCode = numberValue(row.status_code)
  const success = row.success === 1
  return {
    id: String(row.id),
    systemAccountId: shouldIncludeSystemAccountFields ? optionalString(row.system_account_id) : undefined,
    systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(String(row.system_account_id)) : undefined,
    traceId: String(row.trace_id),
    trafficSource: usageRecordTrafficSource(row.traffic_source),
    clientIp: optionalString(row.client_ip),
    apiKeyId: optionalString(row.api_key_id),
    apiKeyName: optionalString(row.api_key_name),
    groupId: optionalString(row.group_id),
    groupName: optionalString(row.group_name),
    accountId: optionalString(row.account_id),
    accountName: optionalString(row.account_name),
    endpoint: optionalString(row.endpoint) ?? endpointFromSnapshot(requestSnapshot),
    providerCode: optionalString(row.provider_code),
    model,
    stream,
    statusCode,
    success,
    firstTokenMs: numberValue(row.first_token_ms),
    durationMs: numberValue(row.duration_ms),
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheReadCostUsd,
    inputImageTokens,
    outputImageTokens,
    costUsd: numberValue(row.cost_usd),
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    requestSnapshot,
    responseSnapshot: includeSnapshots ? parseOptionalJsonObject(row.response_snapshot_json) : undefined,
    createdAt: String(row.created_at)
  }
}

function usageRecordTrafficSource(value: unknown): UsageRecordSummary['trafficSource'] {
  if (value === 'gateway' || value === 'manual_account_test' || value === 'cooldown_retest') {
    return value
  }
  throw new Error('使用记录来源无效')
}

function endpointFromSnapshot(snapshot?: Record<string, unknown>): string | undefined {
  const method = typeof snapshot?.method === 'string' ? snapshot.method.toUpperCase() : undefined
  const originalUrl = typeof snapshot?.originalUrl === 'string' ? snapshot.originalUrl.split('?')[0] : undefined
  const path = typeof snapshot?.path === 'string' ? snapshot.path : undefined
  const endpoint = originalUrl ?? path
  return endpoint ? `${method ?? 'GET'} ${endpoint}` : undefined
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
