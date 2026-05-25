import { buildSystemAccountScopeClause, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { pagedTotalUpperBound, takePageRows } from './query-utils.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { buildUsageAccessLookupContext, systemAccountIdForUsage, usageAccessMetadata, usageApiKeyExists } from './usage-record-access-metadata.js'
import { buildUsageRecordFilters, buildUsageRecordOrderClause, type NormalizedUsageRecordListOptions, normalizeUsageRecordListOptions, type UsageRecordFilterResult } from './usage-record-list-query.js'
import { hydrateUsageRecordNames, usageRecordSummaryFromRow, type UsageRecordRow } from './usage-record-mappers.js'
import {
  generateUsageRecordId,
  getUsageRecordShardDatabase,
  listRecentUsageRecordShardLocations,
  listUsageRecordShardLocations,
  queryUsageRecordShardById,
  usageRecordShardLocationForRecord,
  type UsageRecordShardLocation,
  type UsageRecordShardQueryWindow
} from './usage-record-shards.js'
import { optionalString } from './value-utils.js'
import type { ResourceAuthorizationSourceType } from '../domain/types.js'

export interface UsageRecordLogSnapshot {
  [key: string]: unknown
}

export interface UsageRecordSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  traceId: string
  trafficSource: UsageRecordTrafficSource
  clientIp?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  endpoint?: string
  providerCode?: string
  model?: string
  stream: boolean
  statusCode?: number
  success: boolean
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheReadCostUsd?: number
  inputImageTokens?: number
  outputImageTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: UsageRecordLogSnapshot
  responseSnapshot?: UsageRecordLogSnapshot
  createdAt: string
}

export type UsageRecordTrafficSource = 'gateway' | 'manual_account_test' | 'cooldown_retest'
export type UsageRecordSortField = 'createdAt' | 'firstTokenMs' | 'durationMs' | 'costUsd'
export type UsageRecordSortDirection = 'asc' | 'desc'

export interface UsageRecordListOptions {
  page?: number
  pageSize?: number
  sortBy?: UsageRecordSortField
  sortOrder?: UsageRecordSortDirection
  limit?: number
  accountKeyword?: string
  clientIp?: string
  result?: 'success' | 'failed' | 'all'
  statusCode?: number
  groupId?: string
  model?: string
  trafficSource?: UsageRecordTrafficSource
  startAt?: string
  endAt?: string
}

export interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface RecentOpenAIRequestShape {
  endpoint: string
  model?: string
  stream: boolean
  createdAt: string
}

export interface UsageRecordInput {
  id?: string
  systemAccountId?: string
  traceId: string
  trafficSource?: UsageRecordTrafficSource
  clientIp?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  accountOwnerSystemAccountId?: string
  groupOwnerSystemAccountId?: string
  accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
  groupAccessType?: 'owner' | 'authorized'
  accountAuthorizationId?: string
  accountAuthorizationSourceType?: ResourceAuthorizationSourceType
  accountAuthorizationSourceTeamId?: string
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: ResourceAuthorizationSourceType
  groupAuthorizationSourceTeamId?: string
  endpoint?: string
  providerCode?: string
  model?: string
  stream?: boolean
  statusCode?: number
  success: boolean
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheReadCostUsd?: number
  inputImageTokens?: number
  outputImageTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: unknown
  responseSnapshot?: unknown
  createdAt?: string
}

export function listUsageRecords(access?: AccessScope, options?: UsageRecordListOptions): UsageRecordListResult {
  const filters = buildUsageRecordFilters(access, options)
  const listOptions = normalizeUsageRecordListOptions(options)
  const orderClause = buildUsageRecordOrderClause(listOptions)
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const offset = (listOptions.page - 1) * listOptions.pageSize
  const rows = listUsageRecordRowsFromShards(
    filters,
    listOptions,
    orderClause,
    offset + listOptions.pageSize + 1,
    usageRecordShardQueryWindowFromOptions(options)
  )
  const pageRows = takePageRows(rows.slice(offset), listOptions.pageSize)
  const rowsWithNames = hydrateUsageRecordNames(pageRows.rows)
  const accountNames = shouldIncludeSystemAccountFields
    ? loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
    : new Map<string, string>()
  const items = rowsWithNames.map((row) => usageRecordSummaryFromRow(row, shouldIncludeSystemAccountFields, accountNames))
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export function getUsageRecordDetail(id: string, access?: AccessScope): UsageRecordSummary | undefined {
  const recordId = id.trim()
  if (!recordId) return undefined
  const scope = buildSystemAccountScopeClause(access, 'ur.system_account_id')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const row = queryUsageRecordShardById<UsageRecordRow>(recordId, `
      SELECT
        ur.*
      FROM usage_records ur
      WHERE ur.id = ?
      ${scope.clause}
      LIMIT 1
    `, [recordId, ...scope.params])
  const namedRow = row ? hydrateUsageRecordNames([row])[0] : undefined
  const accountNames = shouldIncludeSystemAccountFields
    ? loadSystemAccountNameMapByIds([optionalString(namedRow?.system_account_id)])
    : new Map<string, string>()
  return namedRow ? usageRecordSummaryFromRow(namedRow, shouldIncludeSystemAccountFields, accountNames, true) : undefined
}

export function findRecentOpenAIRequestShapeForAccount(accountId: string, groupId?: string): RecentOpenAIRequestShape | undefined {
  const normalizedAccountId = accountId.trim()
  const normalizedGroupId = groupId?.trim()
  if (!normalizedAccountId) return undefined
  const accountShape = findRecentOpenAIRequestShape({ accountId: normalizedAccountId, groupId: normalizedGroupId })
  return accountShape ?? (normalizedGroupId ? findRecentOpenAIRequestShape({ groupId: normalizedGroupId }) : undefined)
}

function findRecentOpenAIRequestShape(input: { accountId?: string; groupId?: string }): RecentOpenAIRequestShape | undefined {
  const clauses: string[] = []
  const params: string[] = []
  if (input.accountId) {
    clauses.push('account_id = ?')
    params.push(input.accountId)
  }
  if (input.groupId) {
    clauses.push('group_id = ?')
    params.push(input.groupId)
  }
  if (clauses.length === 0) return undefined
  const endpointFilter = recentOpenAIEndpointFilter()
  let currentBucketDateKey = ''
  let currentRows: RecentOpenAIRequestShapeRow[] = []
  for (const location of listRecentUsageRecordShardLocations(recentOpenAIRequestShapeLookbackDays)) {
    if (currentBucketDateKey && location.bucketDateKey !== currentBucketDateKey) {
      const shape = recentOpenAIRequestShapeFromRows(currentRows)
      if (shape) return shape
      currentRows = []
    }
    currentBucketDateKey = location.bucketDateKey
    const row = getUsageRecordShardDatabase(location)
      .prepare(`
      SELECT id, endpoint, model, stream, created_at
      FROM usage_records
      WHERE ${clauses.join(' AND ')}
        AND api_key_id IS NOT NULL
        AND COALESCE(traffic_source, 'gateway') = 'gateway'
        AND provider_code = 'openai'
        AND endpoint IS NOT NULL
        AND TRIM(endpoint) <> ''
        AND (${endpointFilter.clause})
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
      .get(...params, ...endpointFilter.params) as unknown as RecentOpenAIRequestShapeRow | undefined
    if (row) {
      currentRows.push(row)
    }
  }
  return recentOpenAIRequestShapeFromRows(currentRows)
}

function recentOpenAIRequestShapeFromRows(rows: RecentOpenAIRequestShapeRow[]): RecentOpenAIRequestShape | undefined {
  rows.sort((left, right) => {
    const byCreatedAt = String(right.created_at ?? '').localeCompare(String(left.created_at ?? ''))
    return byCreatedAt || String(right.id ?? '').localeCompare(String(left.id ?? ''))
  })
  const row = rows[0]
  const endpoint = optionalString(row?.endpoint)
  const createdAt = optionalString(row?.created_at)
  if (!endpoint || !createdAt) return undefined
  return {
    endpoint,
    model: optionalString(row?.model),
    stream: row?.stream === 1,
    createdAt
  }
}

interface RecentOpenAIRequestShapeRow {
  id?: string | null
  endpoint?: string | null
  model?: string | null
  stream?: number | null
  created_at?: string | null
}

function recentOpenAIEndpointFilter(): { clause: string; params: string[] } {
  const endpoints = ['/v1/responses', '/v1/chat/completions']
  const prefixes = endpoints.flatMap((endpoint) => [`post ${endpoint}`, endpoint])
  return {
    clause: prefixes.map(() => `
      ${recentOpenAIEndpointExpression} = ?
      OR (${recentOpenAIEndpointExpression} >= ? AND ${recentOpenAIEndpointExpression} < ?)
    `).join(' OR '),
    params: prefixes.flatMap((prefix) => {
      const childPrefix = `${prefix}/`
      return [prefix, childPrefix, `${childPrefix}\uffff`]
    })
  }
}

const recentOpenAIEndpointExpression = 'LOWER(TRIM(endpoint))'

export function createUsageRecord(input: UsageRecordInput): void {
  createUsageRecordsBatch([input])
}

export function createUsageRecordsBatch(inputs: UsageRecordInput[]): void {
  if (inputs.length === 0) {
    return
  }

  const businessDatabase = getDatabase()
  const insertSql = `
    INSERT INTO usage_records (
      id, system_account_id, trace_id, traffic_source, client_ip, api_key_id, group_id, account_id, endpoint, provider_code, model, stream,
      status_code, success, first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, input_image_tokens, output_image_tokens, cost_usd, error_code, error_message,
      request_snapshot_json, response_snapshot_json,
      account_owner_system_account_id, group_owner_system_account_id, account_access_type, group_access_type,
      account_authorization_id, account_authorization_source_type, account_authorization_source_team_id,
      group_authorization_id, group_authorization_source_type, group_authorization_source_team_id,
      created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `
  const accountLastUsedAt = new Map<string, string>()
  const accessLookupContext = buildUsageAccessLookupContext(inputs)
  const rowsByShard = new Map<string, { location: UsageRecordShardLocation; rows: UsageRecordInsertRow[] }>()
  let accountLastUsedFlushed = false

  try {
    for (const input of inputs) {
      if (input.apiKeyId && !usageApiKeyExists(input.apiKeyId, accessLookupContext)) {
        continue
      }
      const now = input.createdAt ?? nowIso()
      const id = input.id ?? generateUsageRecordId(now, newId('usage'))
      const systemAccountId = input.systemAccountId ?? systemAccountIdForUsage(input, accessLookupContext)
      const accessMetadata = usageAccessMetadata({ ...input, systemAccountId }, accessLookupContext)
      const trafficSource = normalizeUsageRecordTrafficSource(input.trafficSource)
      const row: UsageRecordInsertRow = {
        params: [
          id,
          systemAccountId,
          input.traceId,
          trafficSource,
          input.clientIp ?? null,
          input.apiKeyId ?? null,
          input.groupId ?? null,
          input.accountId ?? null,
          input.endpoint ?? null,
          input.providerCode ?? null,
          input.model ?? null,
          input.stream ? 1 : 0,
          input.statusCode ?? null,
          input.success ? 1 : 0,
          input.firstTokenMs ?? null,
          input.durationMs ?? null,
          input.inputTokens ?? null,
          input.outputTokens ?? null,
          input.cacheReadTokens ?? null,
          input.cacheReadCostUsd ?? null,
          input.inputImageTokens ?? null,
          input.outputImageTokens ?? null,
          input.costUsd ?? null,
          input.errorCode ?? null,
          input.errorMessage ?? null,
          input.requestSnapshot ? JSON.stringify(input.requestSnapshot) : null,
          input.responseSnapshot ? JSON.stringify(input.responseSnapshot) : null,
          accessMetadata.accountOwnerSystemAccountId ?? null,
          accessMetadata.groupOwnerSystemAccountId ?? null,
          accessMetadata.accountAccessType ?? null,
          accessMetadata.groupAccessType ?? null,
          accessMetadata.accountAuthorizationId ?? null,
          accessMetadata.accountAuthorizationSourceType ?? null,
          accessMetadata.accountAuthorizationSourceTeamId ?? null,
          accessMetadata.groupAuthorizationId ?? null,
          accessMetadata.groupAuthorizationSourceType ?? null,
          accessMetadata.groupAuthorizationSourceTeamId ?? null,
          now
        ],
        accountId: input.accountId,
        accountLastUsedAt: trafficSource !== 'cooldown_retest' ? now : undefined
      }

      const location = usageRecordShardLocationForRecord(id, now)
      const shardRows = rowsByShard.get(location.shardKey) ?? { location, rows: [] }
      shardRows.rows.push(row)
      rowsByShard.set(location.shardKey, shardRows)
    }

    for (const shardRows of rowsByShard.values()) {
      const shardDatabase = getUsageRecordShardDatabase(shardRows.location)
      const insertStatement = shardDatabase.prepare(insertSql)
      const transactionStarted = beginDatabaseTransaction(shardDatabase)
      const shardAccountLastUsedAt = new Map<string, string>()
      try {
        for (const row of shardRows.rows) {
          const result = insertStatement.run(...row.params)
          if (Number(result.changes ?? 0) === 0 || !row.accountId || !row.accountLastUsedAt) {
            continue
          }
          const previous = shardAccountLastUsedAt.get(row.accountId)
          if (!previous || row.accountLastUsedAt > previous) {
            shardAccountLastUsedAt.set(row.accountId, row.accountLastUsedAt)
          }
        }
        commitDatabaseTransaction(shardDatabase, transactionStarted)
        mergeAccountLastUsedAt(accountLastUsedAt, shardAccountLastUsedAt)
      } catch (error) {
        rollbackDatabaseTransaction(shardDatabase, transactionStarted)
        throw error
      }
    }

    updateAccountLastUsedAt(accountLastUsedAt, businessDatabase)
    accountLastUsedFlushed = true
  } catch (error) {
    if (!accountLastUsedFlushed && accountLastUsedAt.size > 0) {
      try {
        updateAccountLastUsedAt(accountLastUsedAt, businessDatabase)
      } catch {
      }
    }
    throw error
  }
}

interface UsageRecordInsertRow {
  params: Array<string | number | null>
  accountId?: string
  accountLastUsedAt?: string
}

function listUsageRecordRowsFromShards(
  filters: UsageRecordFilterResult,
  listOptions: NormalizedUsageRecordListOptions,
  orderClause: string,
  perShardLimit: number,
  window: UsageRecordShardQueryWindow = {}
): UsageRecordRow[] {
  const locations = listUsageRecordShardLocations(window)
  const rows: UsageRecordRow[] = []
  for (const location of locations) {
    rows.push(...getUsageRecordShardDatabase(location)
      .prepare(`
        SELECT
          ur.id,
          ur.system_account_id,
          ur.trace_id,
          ur.traffic_source,
          ur.client_ip,
          ur.api_key_id,
          ur.group_id,
          ur.account_id,
          ur.endpoint,
          ur.provider_code,
          ur.model,
          ur.stream,
          ur.status_code,
          ur.success,
          ur.first_token_ms,
          ur.duration_ms,
          ur.input_tokens,
          ur.output_tokens,
          ur.cache_read_tokens,
          ur.cache_read_cost_usd,
          ur.input_image_tokens,
          ur.output_image_tokens,
          ur.cost_usd,
          ur.error_code,
          ur.error_message,
          ur.created_at
        FROM usage_records ur
        ${filters.clause}
        ${orderClause}
        LIMIT ?
      `)
      .all(...filters.params, perShardLimit) as UsageRecordRow[])
  }
  return rows.sort((left, right) => compareUsageRecordRows(left, right, listOptions)).slice(0, perShardLimit)
}

function usageRecordShardQueryWindowFromOptions(options?: UsageRecordListOptions): UsageRecordShardQueryWindow {
  return {
    startAt: options?.startAt,
    endAt: options?.endAt
  }
}

function compareUsageRecordRows(left: UsageRecordRow, right: UsageRecordRow, options: NormalizedUsageRecordListOptions): number {
  const direction = options.sortOrder === 'asc' ? 1 : -1
  if (options.sortBy !== 'createdAt') {
    const byRequestedField = compareNullableValues(
      usageRecordSortValue(left, options.sortBy),
      usageRecordSortValue(right, options.sortBy),
      direction
    )
    if (byRequestedField !== 0) return byRequestedField
  }
  const byCreatedAt = String(left.created_at ?? '').localeCompare(String(right.created_at ?? '')) * direction
  if (byCreatedAt !== 0) return byCreatedAt
  return String(left.id ?? '').localeCompare(String(right.id ?? '')) * direction
}

function usageRecordSortValue(row: UsageRecordRow, sortBy: UsageRecordSortField): string | number | null | undefined {
  if (sortBy === 'firstTokenMs') return sortableUsageRecordValue(row.first_token_ms)
  if (sortBy === 'durationMs') return sortableUsageRecordValue(row.duration_ms)
  if (sortBy === 'costUsd') return sortableUsageRecordValue(row.cost_usd)
  return sortableUsageRecordValue(row.created_at)
}

function sortableUsageRecordValue(value: unknown): string | number | null | undefined {
  if (value === null || value === undefined) return value
  if (typeof value === 'number' || typeof value === 'string') return value
  return undefined
}

function compareNullableValues(left: string | number | null | undefined, right: string | number | null | undefined, direction: 1 | -1): number {
  const leftMissing = left === null || left === undefined
  const rightMissing = right === null || right === undefined
  if (leftMissing && rightMissing) return 0
  if (leftMissing) return -1 * direction
  if (rightMissing) return 1 * direction
  if (typeof left === 'number' && typeof right === 'number') {
    return left === right ? 0 : left > right ? direction : -direction
  }
  return String(left).localeCompare(String(right)) * direction
}

function mergeAccountLastUsedAt(target: Map<string, string>, source: Map<string, string>): void {
  for (const [accountId, lastUsedAt] of source) {
    const previous = target.get(accountId)
    if (!previous || lastUsedAt > previous) {
      target.set(accountId, lastUsedAt)
    }
  }
}

function updateAccountLastUsedAt(accountLastUsedAt: Map<string, string>, database: ReturnType<typeof getDatabase>): void {
  if (accountLastUsedAt.size === 0) return
  const updateAccountStatement = database.prepare(`
    UPDATE accounts
    SET last_used_at = ?, updated_at = ?
    WHERE id = ?
      AND (last_used_at IS NULL OR last_used_at < ?)
  `)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const [accountId, lastUsedAt] of accountLastUsedAt) {
      const previousFlushAt = accountLastUsedWriteCache.get(accountId)
      if (previousFlushAt && Date.parse(lastUsedAt) - Date.parse(previousFlushAt) < accountLastUsedThrottleMs) {
        continue
      }
      updateAccountStatement.run(lastUsedAt, lastUsedAt, accountId, lastUsedAt)
      accountLastUsedWriteCache.set(accountId, lastUsedAt)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

const accountLastUsedThrottleMs = 30_000
const accountLastUsedWriteCache = new Map<string, string>()
const recentOpenAIRequestShapeLookbackDays = 7

function normalizeUsageRecordTrafficSource(value: unknown): UsageRecordTrafficSource {
  return value === 'manual_account_test' || value === 'cooldown_retest' ? value : 'gateway'
}

