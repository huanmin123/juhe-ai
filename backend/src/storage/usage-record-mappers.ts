import type { UsageRecordSummary } from './usage-records.repository.js'
import { loadAccountNameMap, loadAccountNameMapAsync, loadApiKeyNameMap, loadApiKeyNameMapAsync, loadGroupNameMap, loadGroupNameMapAsync } from './repository-lookups.js'
import type { DatabaseClient } from './database-client.js'
import { optionalString, parseOptionalJsonObject } from './value-utils.js'
import { normalizeUsageReasoningEffort } from '../modules/gateway/usage/reasoning-effort.js'
import { normalizeOptionalUsageServiceTier } from '../modules/gateway/usage/service-tier.js'

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

export async function hydrateUsageRecordNamesAsync(client: DatabaseClient, rows: UsageRecordRow[]): Promise<UsageRecordRow[]> {
  if (!rows.length) return rows
  const [apiKeyNames, groupNames, recordAccountNames] = await Promise.all([
    loadApiKeyNameMapAsync(client, rows.map((row) => optionalString(row.api_key_id) ?? '')),
    loadGroupNameMapAsync(client, rows.map((row) => optionalString(row.group_id) ?? '')),
    loadAccountNameMapAsync(client, rows.map((row) => optionalString(row.account_id) ?? ''))
  ])
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
  const cacheWriteTokens = numberValue(row.cache_write_tokens)
  const cacheWrite1hTokens = numberValue(row.cache_write_1h_tokens)
  const cacheWriteCostUsd = numberValue(row.cache_write_cost_usd)
  const thinkingTokens = numberValue(row.thinking_tokens)
  const inputImageTokens = numberValue(row.input_image_tokens)
  const outputImageTokens = numberValue(row.output_image_tokens)
  const inputAudioTokens = numberValue(row.input_audio_tokens)
  const outputAudioTokens = numberValue(row.output_audio_tokens)
  const outputImageCount = numberValue(row.output_image_count)
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
    providerProtocolProfileId: optionalString(row.provider_protocol_profile_id),
    usageSemantic: optionalString(row.usage_semantic),
    model,
    upstreamModel: optionalString(row.upstream_model),
    pricingModel: optionalString(row.pricing_model),
    requestedServiceTier: usageServiceTier(row.requested_service_tier),
    effectiveServiceTier: usageServiceTier(row.effective_service_tier),
    reportedServiceTier: usageServiceTier(row.reported_service_tier),
    billedServiceTier: usageServiceTier(row.billed_service_tier),
    requestedReasoningEffort: usageReasoningEffort(row.requested_reasoning_effort),
    effectiveReasoningEffort: usageReasoningEffort(row.effective_reasoning_effort),
    pricingSnapshot: parseOptionalJsonObject(row.cost_breakdown_snapshot_json) as UsageRecordSummary['pricingSnapshot'],
    modelMappingApplied: row.model_mapping_applied === 1,
    modelMappingSource: optionalString(row.model_mapping_source),
    sourceEndpointFamily: optionalString(row.source_endpoint_family),
    upstreamEndpointFamily: optionalString(row.upstream_endpoint_family),
    stream,
    statusCode,
    success,
    failureAttribution: usageFailureAttribution(row.failure_attribution),
    firstTokenMs: numberValue(row.first_token_ms),
    durationMs: numberValue(row.duration_ms),
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheReadCostUsd,
    cacheWriteTokens,
    cacheWrite1hTokens,
    cacheWriteCostUsd,
    thinkingTokens,
    inputImageTokens,
    outputImageTokens,
    inputAudioTokens,
    outputAudioTokens,
    outputImageCount,
    costUsd: numberValue(row.cost_usd),
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    requestSnapshot,
    responseSnapshot: includeSnapshots ? parseOptionalJsonObject(row.response_snapshot_json) : undefined,
    createdAt: String(row.created_at)
  }
}

function usageReasoningEffort(value: unknown): UsageRecordSummary['effectiveReasoningEffort'] {
  return normalizeUsageReasoningEffort(value)
}

function usageServiceTier(value: unknown): UsageRecordSummary['billedServiceTier'] {
  return normalizeOptionalUsageServiceTier(value)
}

function usageRecordTrafficSource(value: unknown): UsageRecordSummary['trafficSource'] {
  if (
    value === 'gateway'
    || value === 'manual_account_test'
    || value === 'account_health_check'
    || value === 'runtime_recovery_probe'
    || value === 'cooldown_retest'
    || value === 'hybrid_scoring'
    || value === 'hybrid_quality_scoring'
  ) {
    return value
  }
  throw new Error('使用记录来源无效')
}

function usageFailureAttribution(value: unknown): UsageRecordSummary['failureAttribution'] {
  if (
    value === 'account_upstream'
    || value === 'account_dependency'
    || value === 'opaque_upstream'
    || value === 'gateway_capacity'
    || value === 'gateway_policy'
    || value === 'client_lifecycle'
  ) {
    return value
  }
  return undefined
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
