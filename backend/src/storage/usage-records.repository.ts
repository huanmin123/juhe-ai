import { buildSystemAccountScopeClause, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, getDatasetDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { pagedTotalUpperBound, takePageRows } from './query-utils.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { buildUsageAccessLookupContext, systemAccountIdForUsage, usageAccessMetadata, usageApiKeyExists } from './usage-record-access-metadata.js'
import { buildUsageRecordFilters, buildUsageRecordOrderClause, normalizeUsageRecordListOptions } from './usage-record-list-query.js'
import { hydrateUsageRecordNames, usageRecordSummaryFromRow, type UsageRecordRow } from './usage-record-mappers.js'
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

export type UsageRecordSortField = 'createdAt' | 'firstTokenMs' | 'durationMs' | 'costUsd'
export type UsageRecordSortDirection = 'asc' | 'desc'

export interface UsageRecordListOptions {
  page?: number
  pageSize?: number
  sortBy?: UsageRecordSortField
  sortOrder?: UsageRecordSortDirection
  limit?: number
  accountKeyword?: string
  result?: 'success' | 'failed' | 'all'
  statusCode?: number
  groupId?: string
  model?: string
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
  const database = getDatasetDatabase()
  const offset = (listOptions.page - 1) * listOptions.pageSize
  const rows = database
    .prepare(`
      SELECT
        ur.id,
        ur.system_account_id,
        ur.trace_id,
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
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, listOptions.pageSize + 1, offset) as UsageRecordRow[]
  const pageRows = takePageRows(rows, listOptions.pageSize)
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
  const row = getDatasetDatabase()
    .prepare(`
      SELECT
        ur.*
      FROM usage_records ur
      WHERE ur.id = ?
      ${scope.clause}
      LIMIT 1
    `)
    .get(recordId, ...scope.params) as UsageRecordRow | undefined
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
  const row = getDatasetDatabase()
    .prepare(`
      SELECT endpoint, model, stream, created_at
      FROM usage_records
      WHERE ${clauses.join(' AND ')}
        AND api_key_id IS NOT NULL
        AND provider_code = 'openai'
        AND endpoint IS NOT NULL
        AND TRIM(endpoint) <> ''
        AND (${endpointFilter.clause})
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(...params, ...endpointFilter.params) as unknown as { endpoint?: string | null; model?: string | null; stream?: number | null; created_at?: string | null } | undefined
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

  const database = getDatasetDatabase()
  const businessDatabase = getDatabase()
  const insertStatement = database.prepare(`
    INSERT INTO usage_records (
      id, system_account_id, trace_id, client_ip, api_key_id, group_id, account_id, endpoint, provider_code, model, stream,
      status_code, success, first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, cache_read_cost_usd, input_image_tokens, output_image_tokens, cost_usd, error_code, error_message,
      request_snapshot_json, response_snapshot_json,
      account_owner_system_account_id, group_owner_system_account_id, account_access_type, group_access_type,
      account_authorization_id, account_authorization_source_type, account_authorization_source_team_id,
      group_authorization_id, group_authorization_source_type, group_authorization_source_team_id,
      created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const updateAccountStatement = businessDatabase.prepare('UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE id = ?')
  const accountLastUsedAt = new Map<string, string>()
  const accessLookupContext = buildUsageAccessLookupContext(inputs)

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      if (input.apiKeyId && !usageApiKeyExists(input.apiKeyId, accessLookupContext)) {
        continue
      }
      const now = input.createdAt ?? nowIso()
      const systemAccountId = input.systemAccountId ?? systemAccountIdForUsage(input, accessLookupContext)
      const accessMetadata = usageAccessMetadata({ ...input, systemAccountId }, accessLookupContext)
      const result = insertStatement.run(
        input.id ?? newId('usage'),
        systemAccountId,
        input.traceId,
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
      )

      if (Number(result.changes ?? 0) === 0) {
        continue
      }

      if (input.accountId) {
        const previous = accountLastUsedAt.get(input.accountId)
        if (!previous || now > previous) {
          accountLastUsedAt.set(input.accountId, now)
        }
      }
    }

    for (const [accountId, lastUsedAt] of accountLastUsedAt) {
      updateAccountStatement.run(lastUsedAt, lastUsedAt, accountId)
    }

    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

