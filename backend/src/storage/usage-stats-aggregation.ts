import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  type UsageStatsAccumulator,
  type UsageStatsEntry,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

export function usageStatsEntries(row: UsageStatsRecordRow): UsageStatsEntry[] {
  const accumulator = usageStatsAccumulatorFromRecord(row)
  const callerSystemAccountId = row.system_account_id
  const accountOwnerSystemAccountId = row.account_owner_system_account_id ?? callerSystemAccountId
  const groupOwnerSystemAccountId = row.group_owner_system_account_id ?? callerSystemAccountId
  const entries = [
    { systemAccountId: callerSystemAccountId, scopeType: 'system_account', scopeId: callerSystemAccountId, accumulator },
    { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'system_account', scopeId: GLOBAL_STATS_SCOPE_ID, accumulator }
  ]
  if (row.provider_code) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'provider', scopeId: row.provider_code, accumulator })
  if (row.group_id) entries.push({ systemAccountId: groupOwnerSystemAccountId, scopeType: 'group', scopeId: row.group_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: callerSystemAccountId, scopeType: 'caller_account', scopeId: row.account_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: accountOwnerSystemAccountId, scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.account_id) entries.push({ systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'account', scopeId: row.account_id, accumulator })
  if (row.account_authorization_id && accountOwnerSystemAccountId !== callerSystemAccountId) entries.push({ systemAccountId: accountOwnerSystemAccountId, scopeType: 'account_authorization', scopeId: row.account_authorization_id, accumulator })
  if (row.account_id && row.account_authorization_source_team_id && accountOwnerSystemAccountId !== callerSystemAccountId) {
    entries.push({ systemAccountId: accountOwnerSystemAccountId, scopeType: 'account_authorization_team', scopeId: `${row.account_id}:${row.account_authorization_source_team_id}`, accumulator })
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

export function shouldAggregateUsageStatsRecord(row: UsageStatsRecordRow): boolean {
  void row
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
