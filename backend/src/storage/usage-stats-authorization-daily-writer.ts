import type { DatabaseSync } from 'node:sqlite'

import type { DatabaseClient } from './database-client.js'
import { usageStatsAccumulatorFromRecord } from './usage-stats-aggregation.js'
import { statsParamsTail, statsSubtractParams } from './usage-stats-writer-params.js'
import {
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  type UsageStatsAccumulator,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

type AuthorizationReportResourceType = 'all' | 'account' | 'group'

interface AuthorizationReportRow {
  authorizationId: string
  ownerSystemAccountId: string
  granteeSystemAccountId: string
  resourceType: 'account' | 'group'
  resourceId: string
  hitAccountId: string
  hitAccountOwnerSystemAccountId: string
  sourceType?: string | null
  sourceTeamId?: string | null
}

interface AuthorizationReportResourceFilter {
  resourceFilterType: AuthorizationReportResourceType
  resourceFilterId: string
}

interface AuthorizationReportSummaryKey {
  teamFilterId?: string
  granteeFilterSystemAccountId?: string
  resourceFilterType: AuthorizationReportResourceType
  resourceFilterId: string
}

interface AuthorizationReportContext {
  accountAuthorizationResourceIds?: Map<string, string>
}

const statsSchemaName = 'juhe_stats'

export function upsertAuthorizationUsageReportRows(
  database: DatabaseSync,
  row: UsageStatsRecordRow,
  statDate: string,
  updatedAt: string,
  context?: AuthorizationReportContext
): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const reportRow of authorizationReportRows(row, context)) {
    const reportScopeRows = authorizationReportScopeRows(reportRow)
    const filters = authorizationReportResourceFilters(reportRow)
    for (const scopedReportRow of reportScopeRows) {
      upsertAuthorizationSummaryRows(database, scopedReportRow, filters, stats, statDate, updatedAt)
    }
  }
}

export async function upsertAuthorizationUsageReportRowsAsync(
  client: DatabaseClient,
  row: UsageStatsRecordRow,
  statDate: string,
  updatedAt: string,
  context?: AuthorizationReportContext
): Promise<void> {
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const reportRow of authorizationReportRows(row, context)) {
    const reportScopeRows = authorizationReportScopeRows(reportRow)
    const filters = authorizationReportResourceFilters(reportRow)
    for (const scopedReportRow of reportScopeRows) {
      await upsertAuthorizationSummaryRowsAsync(client, scopedReportRow, filters, stats, statDate, updatedAt)
    }
  }
}

export function subtractAuthorizationUsageReportRows(
  database: DatabaseSync,
  row: UsageStatsRecordRow,
  statDate: string,
  updatedAt: string,
  context?: AuthorizationReportContext
): void {
  const stats = usageStatsAccumulatorFromRecord(row)
  for (const reportRow of authorizationReportRows(row, context)) {
    const reportScopeRows = authorizationReportScopeRows(reportRow)
    const filters = authorizationReportResourceFilters(reportRow)
    for (const scopedReportRow of reportScopeRows) {
      subtractAuthorizationSummaryRows(database, scopedReportRow, filters, stats, statDate, updatedAt)
    }
  }
}

async function upsertAuthorizationSummaryRowsAsync(
  client: DatabaseClient,
  row: AuthorizationReportRow,
  filters: AuthorizationReportResourceFilter[],
  stats: UsageStatsAccumulator,
  statDate: string,
  updatedAt: string
): Promise<void> {
  const userSummaryKeys: AuthorizationReportSummaryKey[] = []
  const teamSummaryKeys: AuthorizationReportSummaryKey[] = []
  for (const filter of filters) {
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: '', ...filter })
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    if (row.sourceType === 'team' && row.sourceTeamId) {
      teamSummaryKeys.push({ teamFilterId: '', ...filter })
      teamSummaryKeys.push({ teamFilterId: row.sourceTeamId, ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: '', ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    }
  }
  for (const key of teamSummaryKeys) {
    await upsertAuthorizationTeamUsageSummaryRowAsync(client, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
  for (const key of userSummaryKeys) {
    await upsertAuthorizationUserUsageSummaryRowAsync(client, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
}

function authorizationReportRows(row: UsageStatsRecordRow, context?: AuthorizationReportContext): AuthorizationReportRow[] {
  const rows: AuthorizationReportRow[] = []
  const seen = new Set<string>()
  if (row.account_authorization_id && row.account_id && row.account_owner_system_account_id && row.account_owner_system_account_id !== row.system_account_id) {
    const resourceId = context?.accountAuthorizationResourceIds?.get(row.account_authorization_id) ?? row.account_id
    addAuthorizationReportRow(rows, seen, {
      authorizationId: `account:${row.account_authorization_id}`,
      ownerSystemAccountId: row.account_owner_system_account_id,
      granteeSystemAccountId: row.system_account_id,
      resourceType: 'account',
      resourceId,
      hitAccountId: row.account_id,
      hitAccountOwnerSystemAccountId: row.account_owner_system_account_id,
      sourceType: row.account_authorization_source_type,
      sourceTeamId: row.account_authorization_source_team_id
    })
  }
  if (row.group_authorization_id && row.group_id && row.group_owner_system_account_id && row.group_owner_system_account_id !== row.system_account_id) {
    addAuthorizationReportRow(rows, seen, {
      authorizationId: `group:${row.group_authorization_id}`,
      ownerSystemAccountId: row.group_owner_system_account_id,
      granteeSystemAccountId: row.system_account_id,
      resourceType: 'group',
      resourceId: row.group_id,
      hitAccountId: row.account_id ?? '',
      hitAccountOwnerSystemAccountId: row.account_owner_system_account_id ?? row.group_owner_system_account_id,
      sourceType: row.group_authorization_source_type,
      sourceTeamId: row.group_authorization_source_team_id
    })
  }
  return rows
}

function addAuthorizationReportRow(rows: AuthorizationReportRow[], seen: Set<string>, row: AuthorizationReportRow): void {
  const key = row.authorizationId
  if (seen.has(key)) return
  seen.add(key)
  rows.push(row)
}

function authorizationReportScopeRows(row: AuthorizationReportRow): AuthorizationReportRow[] {
  return row.ownerSystemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID
    ? [row]
    : [row, { ...row, ownerSystemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID }]
}

function authorizationReportResourceFilters(row: AuthorizationReportRow): AuthorizationReportResourceFilter[] {
  return [
    { resourceFilterType: 'all', resourceFilterId: '' },
    { resourceFilterType: row.resourceType, resourceFilterId: '' },
    { resourceFilterType: row.resourceType, resourceFilterId: row.resourceId }
  ]
}

function upsertAuthorizationSummaryRows(
  database: DatabaseSync,
  row: AuthorizationReportRow,
  filters: AuthorizationReportResourceFilter[],
  stats: UsageStatsAccumulator,
  statDate: string,
  updatedAt: string
): void {
  const userSummaryKeys: AuthorizationReportSummaryKey[] = []
  const teamSummaryKeys: AuthorizationReportSummaryKey[] = []
  for (const filter of filters) {
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: '', ...filter })
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    if (row.sourceType === 'team' && row.sourceTeamId) {
      teamSummaryKeys.push({ teamFilterId: '', ...filter })
      teamSummaryKeys.push({ teamFilterId: row.sourceTeamId, ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: '', ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    }
  }
  for (const key of teamSummaryKeys) {
    upsertAuthorizationTeamUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
  for (const key of userSummaryKeys) {
    upsertAuthorizationUserUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
}

function subtractAuthorizationSummaryRows(
  database: DatabaseSync,
  row: AuthorizationReportRow,
  filters: AuthorizationReportResourceFilter[],
  stats: UsageStatsAccumulator,
  statDate: string,
  updatedAt: string
): void {
  const userSummaryKeys: AuthorizationReportSummaryKey[] = []
  const teamSummaryKeys: AuthorizationReportSummaryKey[] = []
  for (const filter of filters) {
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: '', ...filter })
    userSummaryKeys.push({ teamFilterId: '', granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    if (row.sourceType === 'team' && row.sourceTeamId) {
      teamSummaryKeys.push({ teamFilterId: '', ...filter })
      teamSummaryKeys.push({ teamFilterId: row.sourceTeamId, ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: '', ...filter })
      userSummaryKeys.push({ teamFilterId: row.sourceTeamId, granteeFilterSystemAccountId: row.granteeSystemAccountId, ...filter })
    }
  }
  for (const key of teamSummaryKeys) {
    subtractAuthorizationTeamUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
  for (const key of userSummaryKeys) {
    subtractAuthorizationUserUsageSummaryRow(database, row.ownerSystemAccountId, statDate, key, stats, updatedAt)
  }
}

async function upsertAuthorizationTeamUsageSummaryRowAsync(client: DatabaseClient, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): Promise<void> {
  await client.execute(`
    INSERT INTO ${authorizationStatsTable(client, 'authorization_team_usage_summary_daily')} AS current_row (
      system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id, row_count,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id) DO UPDATE SET
      request_count = current_row.request_count + EXCLUDED.request_count,
      success_count = current_row.success_count + EXCLUDED.success_count,
      error_count = current_row.error_count + EXCLUDED.error_count,
      input_tokens = current_row.input_tokens + EXCLUDED.input_tokens,
      output_tokens = current_row.output_tokens + EXCLUDED.output_tokens,
      cache_read_tokens = current_row.cache_read_tokens + EXCLUDED.cache_read_tokens,
      cache_read_cost_usd = current_row.cache_read_cost_usd + EXCLUDED.cache_read_cost_usd,
      cache_write_tokens = current_row.cache_write_tokens + EXCLUDED.cache_write_tokens,
      cache_write_1h_tokens = current_row.cache_write_1h_tokens + EXCLUDED.cache_write_1h_tokens,
      cache_write_cost_usd = current_row.cache_write_cost_usd + EXCLUDED.cache_write_cost_usd,
      thinking_tokens = current_row.thinking_tokens + EXCLUDED.thinking_tokens,
      input_image_tokens = current_row.input_image_tokens + EXCLUDED.input_image_tokens,
      output_image_tokens = current_row.output_image_tokens + EXCLUDED.output_image_tokens,
      total_cost_usd = current_row.total_cost_usd + EXCLUDED.total_cost_usd,
      duration_ms_sum = current_row.duration_ms_sum + EXCLUDED.duration_ms_sum,
      duration_ms_count = current_row.duration_ms_count + EXCLUDED.duration_ms_count,
      duration_ms_max = CASE WHEN current_row.duration_ms_max > EXCLUDED.duration_ms_max THEN current_row.duration_ms_max ELSE EXCLUDED.duration_ms_max END,
      first_token_ms_sum = current_row.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
      first_token_ms_count = current_row.first_token_ms_count + EXCLUDED.first_token_ms_count,
      first_token_ms_max = CASE WHEN current_row.first_token_ms_max > EXCLUDED.first_token_ms_max THEN current_row.first_token_ms_max ELSE EXCLUDED.first_token_ms_max END,
      last_used_at = CASE WHEN EXCLUDED.last_used_at IS NULL THEN current_row.last_used_at WHEN current_row.last_used_at IS NULL OR EXCLUDED.last_used_at > current_row.last_used_at THEN EXCLUDED.last_used_at ELSE current_row.last_used_at END,
      last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN current_row.last_error_at WHEN current_row.last_error_at IS NULL OR EXCLUDED.last_error_at > current_row.last_error_at THEN EXCLUDED.last_error_at ELSE current_row.last_error_at END,
      updated_at = EXCLUDED.updated_at
  `, [systemAccountId, statDate, key.teamFilterId ?? '', key.resourceFilterType, key.resourceFilterId, ...statsParamsTail(stats, updatedAt)])
}

async function upsertAuthorizationUserUsageSummaryRowAsync(client: DatabaseClient, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): Promise<void> {
  await client.execute(`
    INSERT INTO ${authorizationStatsTable(client, 'authorization_user_usage_summary_daily')} AS current_row (
      system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id, row_count,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id) DO UPDATE SET
      request_count = current_row.request_count + EXCLUDED.request_count,
      success_count = current_row.success_count + EXCLUDED.success_count,
      error_count = current_row.error_count + EXCLUDED.error_count,
      input_tokens = current_row.input_tokens + EXCLUDED.input_tokens,
      output_tokens = current_row.output_tokens + EXCLUDED.output_tokens,
      cache_read_tokens = current_row.cache_read_tokens + EXCLUDED.cache_read_tokens,
      cache_read_cost_usd = current_row.cache_read_cost_usd + EXCLUDED.cache_read_cost_usd,
      cache_write_tokens = current_row.cache_write_tokens + EXCLUDED.cache_write_tokens,
      cache_write_1h_tokens = current_row.cache_write_1h_tokens + EXCLUDED.cache_write_1h_tokens,
      cache_write_cost_usd = current_row.cache_write_cost_usd + EXCLUDED.cache_write_cost_usd,
      thinking_tokens = current_row.thinking_tokens + EXCLUDED.thinking_tokens,
      input_image_tokens = current_row.input_image_tokens + EXCLUDED.input_image_tokens,
      output_image_tokens = current_row.output_image_tokens + EXCLUDED.output_image_tokens,
      total_cost_usd = current_row.total_cost_usd + EXCLUDED.total_cost_usd,
      duration_ms_sum = current_row.duration_ms_sum + EXCLUDED.duration_ms_sum,
      duration_ms_count = current_row.duration_ms_count + EXCLUDED.duration_ms_count,
      duration_ms_max = CASE WHEN current_row.duration_ms_max > EXCLUDED.duration_ms_max THEN current_row.duration_ms_max ELSE EXCLUDED.duration_ms_max END,
      first_token_ms_sum = current_row.first_token_ms_sum + EXCLUDED.first_token_ms_sum,
      first_token_ms_count = current_row.first_token_ms_count + EXCLUDED.first_token_ms_count,
      first_token_ms_max = CASE WHEN current_row.first_token_ms_max > EXCLUDED.first_token_ms_max THEN current_row.first_token_ms_max ELSE EXCLUDED.first_token_ms_max END,
      last_used_at = CASE WHEN EXCLUDED.last_used_at IS NULL THEN current_row.last_used_at WHEN current_row.last_used_at IS NULL OR EXCLUDED.last_used_at > current_row.last_used_at THEN EXCLUDED.last_used_at ELSE current_row.last_used_at END,
      last_error_at = CASE WHEN EXCLUDED.last_error_at IS NULL THEN current_row.last_error_at WHEN current_row.last_error_at IS NULL OR EXCLUDED.last_error_at > current_row.last_error_at THEN EXCLUDED.last_error_at ELSE current_row.last_error_at END,
      updated_at = EXCLUDED.updated_at
  `, [systemAccountId, statDate, key.teamFilterId ?? '', key.granteeFilterSystemAccountId ?? '', key.resourceFilterType, key.resourceFilterId, ...statsParamsTail(stats, updatedAt)])
}

function authorizationStatsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(statsSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function upsertAuthorizationTeamUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO authorization_team_usage_summary_daily (
      system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id, row_count,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
      cache_write_1h_tokens = cache_write_1h_tokens + excluded.cache_write_1h_tokens,
      cache_write_cost_usd = cache_write_cost_usd + excluded.cache_write_cost_usd,
      thinking_tokens = thinking_tokens + excluded.thinking_tokens,
      input_image_tokens = input_image_tokens + excluded.input_image_tokens,
      output_image_tokens = output_image_tokens + excluded.output_image_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      first_token_ms_max = MAX(first_token_ms_max, excluded.first_token_ms_max),
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN authorization_team_usage_summary_daily.last_used_at WHEN authorization_team_usage_summary_daily.last_used_at IS NULL OR excluded.last_used_at > authorization_team_usage_summary_daily.last_used_at THEN excluded.last_used_at ELSE authorization_team_usage_summary_daily.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN authorization_team_usage_summary_daily.last_error_at WHEN authorization_team_usage_summary_daily.last_error_at IS NULL OR excluded.last_error_at > authorization_team_usage_summary_daily.last_error_at THEN excluded.last_error_at ELSE authorization_team_usage_summary_daily.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.resourceFilterType, key.resourceFilterId, ...statsParamsTail(stats, updatedAt))
}

function subtractAuthorizationTeamUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE authorization_team_usage_summary_daily
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        cache_write_tokens = MAX(0, cache_write_tokens - ?),
        cache_write_1h_tokens = MAX(0, cache_write_1h_tokens - ?),
        cache_write_cost_usd = MAX(0, cache_write_cost_usd - ?),
        thinking_tokens = MAX(0, thinking_tokens - ?),
        input_image_tokens = MAX(0, input_image_tokens - ?),
        output_image_tokens = MAX(0, output_image_tokens - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
  `).run(...statsSubtractParams(stats), updatedAt, systemAccountId, statDate, key.teamFilterId ?? '', key.resourceFilterType, key.resourceFilterId)
  deleteEmptyAuthorizationTeamUsageSummaryRow(database, systemAccountId, statDate, key)
}

function upsertAuthorizationUserUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    INSERT INTO authorization_user_usage_summary_daily (
      system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id, row_count,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd,
      cache_write_tokens, cache_write_1h_tokens, cache_write_cost_usd, thinking_tokens, input_image_tokens, output_image_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max,
      last_used_at, last_error_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id) DO UPDATE SET
      request_count = request_count + excluded.request_count,
      success_count = success_count + excluded.success_count,
      error_count = error_count + excluded.error_count,
      input_tokens = input_tokens + excluded.input_tokens,
      output_tokens = output_tokens + excluded.output_tokens,
      cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
      cache_read_cost_usd = cache_read_cost_usd + excluded.cache_read_cost_usd,
      cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
      cache_write_1h_tokens = cache_write_1h_tokens + excluded.cache_write_1h_tokens,
      cache_write_cost_usd = cache_write_cost_usd + excluded.cache_write_cost_usd,
      thinking_tokens = thinking_tokens + excluded.thinking_tokens,
      input_image_tokens = input_image_tokens + excluded.input_image_tokens,
      output_image_tokens = output_image_tokens + excluded.output_image_tokens,
      total_cost_usd = total_cost_usd + excluded.total_cost_usd,
      duration_ms_sum = duration_ms_sum + excluded.duration_ms_sum,
      duration_ms_count = duration_ms_count + excluded.duration_ms_count,
      duration_ms_max = MAX(duration_ms_max, excluded.duration_ms_max),
      first_token_ms_sum = first_token_ms_sum + excluded.first_token_ms_sum,
      first_token_ms_count = first_token_ms_count + excluded.first_token_ms_count,
      first_token_ms_max = MAX(first_token_ms_max, excluded.first_token_ms_max),
      last_used_at = CASE WHEN excluded.last_used_at IS NULL THEN authorization_user_usage_summary_daily.last_used_at WHEN authorization_user_usage_summary_daily.last_used_at IS NULL OR excluded.last_used_at > authorization_user_usage_summary_daily.last_used_at THEN excluded.last_used_at ELSE authorization_user_usage_summary_daily.last_used_at END,
      last_error_at = CASE WHEN excluded.last_error_at IS NULL THEN authorization_user_usage_summary_daily.last_error_at WHEN authorization_user_usage_summary_daily.last_error_at IS NULL OR excluded.last_error_at > authorization_user_usage_summary_daily.last_error_at THEN excluded.last_error_at ELSE authorization_user_usage_summary_daily.last_error_at END,
      updated_at = excluded.updated_at
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.granteeFilterSystemAccountId ?? '', key.resourceFilterType, key.resourceFilterId, ...statsParamsTail(stats, updatedAt))
}

function subtractAuthorizationUserUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey, stats: UsageStatsAccumulator, updatedAt: string): void {
  database.prepare(`
    UPDATE authorization_user_usage_summary_daily
    SET request_count = MAX(0, request_count - ?),
        success_count = MAX(0, success_count - ?),
        error_count = MAX(0, error_count - ?),
        input_tokens = MAX(0, input_tokens - ?),
        output_tokens = MAX(0, output_tokens - ?),
        cache_read_tokens = MAX(0, cache_read_tokens - ?),
        cache_read_cost_usd = MAX(0, cache_read_cost_usd - ?),
        cache_write_tokens = MAX(0, cache_write_tokens - ?),
        cache_write_1h_tokens = MAX(0, cache_write_1h_tokens - ?),
        cache_write_cost_usd = MAX(0, cache_write_cost_usd - ?),
        thinking_tokens = MAX(0, thinking_tokens - ?),
        input_image_tokens = MAX(0, input_image_tokens - ?),
        output_image_tokens = MAX(0, output_image_tokens - ?),
        total_cost_usd = MAX(0, total_cost_usd - ?),
        duration_ms_sum = MAX(0, duration_ms_sum - ?),
        duration_ms_count = MAX(0, duration_ms_count - ?),
        duration_ms_max = CASE WHEN duration_ms_count <= ? THEN 0 ELSE duration_ms_max END,
        first_token_ms_sum = MAX(0, first_token_ms_sum - ?),
        first_token_ms_count = MAX(0, first_token_ms_count - ?),
        first_token_ms_max = CASE WHEN first_token_ms_count <= ? THEN 0 ELSE first_token_ms_max END,
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        last_error_at = CASE WHEN error_count <= ? THEN NULL ELSE last_error_at END,
        updated_at = ?
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND grantee_filter_system_account_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
  `).run(...statsSubtractParams(stats), updatedAt, systemAccountId, statDate, key.teamFilterId ?? '', key.granteeFilterSystemAccountId ?? '', key.resourceFilterType, key.resourceFilterId)
  deleteEmptyAuthorizationUserUsageSummaryRow(database, systemAccountId, statDate, key)
}

function deleteEmptyAuthorizationTeamUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey): void {
  database.prepare(`
    DELETE FROM authorization_team_usage_summary_daily
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.resourceFilterType, key.resourceFilterId)
}

function deleteEmptyAuthorizationUserUsageSummaryRow(database: DatabaseSync, systemAccountId: string, statDate: string, key: AuthorizationReportSummaryKey): void {
  database.prepare(`
    DELETE FROM authorization_user_usage_summary_daily
    WHERE system_account_id = ? AND stat_date = ? AND team_filter_id = ? AND grantee_filter_system_account_id = ? AND resource_filter_type = ? AND resource_filter_id = ?
      AND request_count = 0 AND success_count = 0 AND error_count = 0
      AND input_tokens = 0 AND output_tokens = 0 AND cache_read_tokens = 0 AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0 AND cache_write_1h_tokens = 0 AND cache_write_cost_usd = 0
      AND thinking_tokens = 0 AND input_image_tokens = 0 AND output_image_tokens = 0 AND total_cost_usd = 0
  `).run(systemAccountId, statDate, key.teamFilterId ?? '', key.granteeFilterSystemAccountId ?? '', key.resourceFilterType, key.resourceFilterId)
}
