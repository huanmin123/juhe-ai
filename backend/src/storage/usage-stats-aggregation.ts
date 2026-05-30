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
  const accountOwnerSystemAccountId = row.account_owner_system_account_id ?? callerSystemAccountId
  const accountStatsSystemAccountId = row.account_access_type === 'account_authorized'
    ? callerSystemAccountId
    : accountOwnerSystemAccountId
  const groupOwnerSystemAccountId = row.group_owner_system_account_id ?? callerSystemAccountId
  const entries = [
    { systemAccountId: callerSystemAccountId, scopeType: 'system_account', scopeId: callerSystemAccountId, accumulator },
    { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'system_account', scopeId: GLOBAL_STATS_SCOPE_ID, accumulator }
  ]
  if (row.provider_code) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'provider', scopeId: row.provider_code, accumulator })
  if (row.group_id) entries.push({ systemAccountId: groupOwnerSystemAccountId, scopeType: 'group', scopeId: row.group_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'caller_account', scopeId: row.account_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: accountStatsSystemAccountId, scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.account_authorization_id && accountOwnerSystemAccountId !== callerSystemAccountId) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'account_authorization', scopeId: row.account_authorization_id, accumulator })
  if (row.account_id && row.account_authorization_source_team_id && accountOwnerSystemAccountId !== callerSystemAccountId) {
    entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'account_authorization_team', scopeId: `${accountAuthorizationTeamAccountId(row, lookup)}:${row.account_authorization_source_team_id}`, accumulator })
  }
  if (row.group_authorization_id && groupOwnerSystemAccountId !== callerSystemAccountId) entries.push({ systemAccountId: groupOwnerSystemAccountId, scopeType: 'group_authorization', scopeId: row.group_authorization_id, accumulator })
  if (row.group_id && row.group_authorization_source_team_id && groupOwnerSystemAccountId !== callerSystemAccountId) {
    entries.push({ systemAccountId: groupOwnerSystemAccountId, scopeType: 'group_authorization_team', scopeId: `${row.group_id}:${row.group_authorization_source_team_id}`, accumulator })
  }
  if (row.api_key_id) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'api_key', scopeId: row.api_key_id, accumulator })
  if (row.model) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'model', scopeId: row.model, accumulator })
  if (row.endpoint) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'endpoint', scopeId: row.endpoint, accumulator })
  return entries
}

function accountAuthorizationTeamAccountId(row: UsageStatsRecordRow, lookup?: UsageStatsAuthorizationLookup): string {
  if (row.account_authorization_id) {
    const instanceAccountId = lookup?.accountAuthorizationInstanceAccountIds?.get(row.account_authorization_id)
    if (instanceAccountId) return instanceAccountId
  }
  return row.account_id ?? ''
}

export function shouldAggregateUsageStatsRecord(row: UsageStatsRecordRow): boolean {
  return (row.traffic_source ?? 'gateway') !== 'cooldown_retest'
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
