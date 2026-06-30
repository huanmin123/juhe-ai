import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  type UsageStatsAccumulator,
  type UsageStatsEntry,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

export interface UsageStatsAuthorizationLookup {
  accountAuthorizationInstanceAccountIds?: Map<string, string>
}

export function usageStatsEntries(row: UsageStatsRecordRow, lookup?: UsageStatsAuthorizationLookup): UsageStatsEntry[] {
  const accumulator = usageStatsAccumulatorFromRecord(row)
  const callerSystemAccountId = row.system_account_id
  const accountMetadata = usageStatsAccountMetadata(row)
  const groupMetadata = usageStatsGroupMetadata(row)
  const accountStatsSystemAccountId = accountMetadata?.accessType === 'account_authorized'
    ? callerSystemAccountId
    : accountMetadata?.ownerSystemAccountId
  const skipOwnerAccountStats = accountMetadata?.accessType !== 'account_authorized'
    && groupMetadata?.accessType === 'authorized'
    && accountMetadata?.ownerSystemAccountId !== callerSystemAccountId
  const skipOwnerGroupStats = groupMetadata?.accessType === 'authorized'
    && groupMetadata.ownerSystemAccountId !== callerSystemAccountId
  const entries = [
    { systemAccountId: callerSystemAccountId, scopeType: 'system_account', scopeId: callerSystemAccountId, accumulator },
    { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'system_account', scopeId: GLOBAL_STATS_SCOPE_ID, accumulator }
  ]
  if (row.provider_code) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'provider', scopeId: row.provider_code, accumulator })
  if (row.provider_protocol_profile_id) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'provider_protocol_profile', scopeId: row.provider_protocol_profile_id, accumulator })
  if (row.group_id && groupMetadata && !skipOwnerGroupStats) entries.push({ systemAccountId: groupMetadata.ownerSystemAccountId, scopeType: 'group', scopeId: row.group_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'caller_account', scopeId: row.account_id, accumulator })
  if (row.account_id && accountStatsSystemAccountId && !skipOwnerAccountStats) entries.push({ systemAccountId: accountStatsSystemAccountId, scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.account_authorization_id && accountMetadata?.ownerSystemAccountId !== callerSystemAccountId) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'account_authorization', scopeId: row.account_authorization_id, accumulator })
  if (row.account_id && row.account_authorization_source_team_id && accountMetadata?.ownerSystemAccountId !== callerSystemAccountId) {
    entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'account_authorization_team', scopeId: `${accountAuthorizationTeamAccountId(row, lookup)}:${row.account_authorization_source_team_id}`, accumulator })
  }
  if (row.group_authorization_id && groupMetadata && groupMetadata.ownerSystemAccountId !== callerSystemAccountId) entries.push({ systemAccountId: groupMetadata.ownerSystemAccountId, scopeType: 'group_authorization', scopeId: row.group_authorization_id, accumulator })
  if (row.group_id && row.group_authorization_source_team_id && groupMetadata && groupMetadata.ownerSystemAccountId !== callerSystemAccountId) {
    entries.push({ systemAccountId: groupMetadata.ownerSystemAccountId, scopeType: 'group_authorization_team', scopeId: `${row.group_id}:${row.group_authorization_source_team_id}`, accumulator })
  }
  if (row.api_key_id) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'api_key', scopeId: row.api_key_id, accumulator })
  if (row.model) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'model', scopeId: row.model, accumulator })
  if (row.endpoint) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'endpoint', scopeId: row.endpoint, accumulator })
  return entries
}

function usageStatsAccountMetadata(row: UsageStatsRecordRow): { ownerSystemAccountId: string; accessType: string } | undefined {
  if (!row.account_id) return undefined
  if (!row.account_owner_system_account_id) {
    throw new Error(`使用记录 ${row.id} 缺少账户归属字段 account_owner_system_account_id`)
  }
  if (!row.account_access_type) {
    throw new Error(`使用记录 ${row.id} 缺少账户访问类型字段 account_access_type`)
  }
  return {
    ownerSystemAccountId: row.account_owner_system_account_id,
    accessType: row.account_access_type
  }
}

function usageStatsGroupMetadata(row: UsageStatsRecordRow): { ownerSystemAccountId: string; accessType: string } | undefined {
  if (!row.group_id) return undefined
  if (!row.group_owner_system_account_id) {
    throw new Error(`使用记录 ${row.id} 缺少分组归属字段 group_owner_system_account_id`)
  }
  if (!row.group_access_type) {
    throw new Error(`使用记录 ${row.id} 缺少分组访问类型字段 group_access_type`)
  }
  return {
    ownerSystemAccountId: row.group_owner_system_account_id,
    accessType: row.group_access_type
  }
}

function accountAuthorizationTeamAccountId(row: UsageStatsRecordRow, lookup?: UsageStatsAuthorizationLookup): string {
  if (row.account_authorization_id) {
    const instanceAccountId = lookup?.accountAuthorizationInstanceAccountIds?.get(row.account_authorization_id)
    if (instanceAccountId) return instanceAccountId
  }
  return row.account_id ?? ''
}

export function shouldAggregateUsageStatsRecord(_row: UsageStatsRecordRow): boolean {
  return true
}

export function usageStatsAccumulatorFromRecord(row: UsageStatsRecordRow): UsageStatsAccumulator {
  const success = row.success === 1
  const durationMs = row.duration_ms === null ? 0 : Math.max(0, Number(row.duration_ms ?? 0))
  const firstTokenMs = row.first_token_ms === null ? 0 : Math.max(0, Number(row.first_token_ms ?? 0))
  return {
    requestCount: 1,
    successCount: success ? 1 : 0,
    errorCount: success ? 0 : 1,
    inputTokens: Math.max(0, Number(row.input_tokens ?? 0)),
    outputTokens: Math.max(0, Number(row.output_tokens ?? 0)),
    cacheReadTokens: Math.max(0, Number(row.cache_read_tokens ?? 0)),
    cacheReadCostUsd: Math.max(0, Number(row.cache_read_cost_usd ?? 0)),
    cacheWriteTokens: Math.max(0, Number(row.cache_write_tokens ?? 0)),
    cacheWrite1hTokens: Math.max(0, Number(row.cache_write_1h_tokens ?? 0)),
    cacheWriteCostUsd: Math.max(0, Number(row.cache_write_cost_usd ?? 0)),
    thinkingTokens: Math.max(0, Number(row.thinking_tokens ?? 0)),
    inputImageTokens: Math.max(0, Number(row.input_image_tokens ?? 0)),
    outputImageTokens: Math.max(0, Number(row.output_image_tokens ?? 0)),
    totalCostUsd: Math.max(0, Number(row.cost_usd ?? 0)),
    durationMsSum: durationMs,
    durationMsCount: row.duration_ms === null ? 0 : 1,
    durationMsMax: row.duration_ms === null ? 0 : durationMs,
    firstTokenMsSum: firstTokenMs,
    firstTokenMsCount: row.first_token_ms === null ? 0 : 1,
    firstTokenMsMax: row.first_token_ms === null ? 0 : firstTokenMs,
    lastUsedAt: row.created_at,
    lastErrorAt: success ? undefined : row.created_at
  }
}
