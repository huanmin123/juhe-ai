import { buildSystemAccountScopeClause, currentSystemAccountId, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { loadAccountNameMap, loadApiKeyNameMap, loadGroupNameMap, loadSystemAccountNameMap } from './repository-lookups.js'
import { optionalString, parseOptionalJsonObject } from './value-utils.js'
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
  model?: string
  startAt?: string
  endAt?: string
}

export interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
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
  inputImageTokens?: number
  outputImageTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: unknown
  responseSnapshot?: unknown
  createdAt?: string
}

type UsageAccessMetadata = {
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
}

type ResourceAuthorizationRow = {
  id: string
  status: string
  expires_at: string | null
  effective_source_type: ResourceAuthorizationSourceType | null
  effective_source_team_id: string | null
}

const usageRecordSortColumns: Record<UsageRecordSortField, string> = {
  createdAt: 'ur.created_at',
  firstTokenMs: 'ur.first_token_ms',
  durationMs: 'ur.duration_ms',
  costUsd: 'ur.cost_usd'
}

type UsageRecordFilterValue = string | number

const usageRecordDefaultPageSize = 50
const usageRecordMaxPageSize = 200

export function listUsageRecords(access?: AccessScope, options?: UsageRecordListOptions): UsageRecordListResult {
  const filters = buildUsageRecordFilters(access, options)
  const listOptions = normalizeUsageRecordListOptions(options)
  const orderClause = buildUsageRecordOrderClause(listOptions)
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields ? loadSystemAccountNameMap() : new Map<string, string>()
  const database = getRecordDatabase()
  const totalRow = database
    .prepare(`
      SELECT COUNT(*) AS total
      FROM usage_records ur
      ${filters.clause}
    `)
    .get(...filters.params) as Record<string, unknown> | undefined
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
    .all(...filters.params, listOptions.pageSize, (listOptions.page - 1) * listOptions.pageSize) as Array<Record<string, unknown>>
  const rowsWithNames = hydrateUsageRecordNames(rows)
  return {
    items: rowsWithNames.map((row) => usageRecordSummaryFromRow(row, shouldIncludeSystemAccountFields, accountNames)),
    total: Number(totalRow?.total ?? 0),
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

export function getUsageRecordDetail(id: string, access?: AccessScope): UsageRecordSummary | undefined {
  const recordId = id.trim()
  if (!recordId) return undefined
  const scope = buildSystemAccountScopeClause(access, 'ur.system_account_id')
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields ? loadSystemAccountNameMap() : new Map<string, string>()
  const row = getRecordDatabase()
    .prepare(`
      SELECT
        ur.*
      FROM usage_records ur
      WHERE ur.id = ?
      ${scope.clause}
      LIMIT 1
    `)
    .get(recordId, ...scope.params) as Record<string, unknown> | undefined
  const namedRow = row ? hydrateUsageRecordNames([row])[0] : undefined
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
  const row = getRecordDatabase()
    .prepare(`
      SELECT endpoint, model, stream, created_at
      FROM usage_records
      WHERE ${clauses.join(' AND ')}
        AND api_key_id IS NOT NULL
        AND provider_code = 'openai'
        AND endpoint IS NOT NULL
        AND TRIM(endpoint) <> ''
        AND (
          LOWER(endpoint) LIKE '%/v1/responses%'
          OR LOWER(endpoint) LIKE '%/v1/chat/completions%'
        )
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(...params) as unknown as { endpoint?: string | null; model?: string | null; stream?: number | null; created_at?: string | null } | undefined
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

export function createUsageRecord(input: UsageRecordInput): void {
  createUsageRecordsBatch([input])
}

export function createUsageRecordsBatch(inputs: UsageRecordInput[]): void {
  if (inputs.length === 0) {
    return
  }

  const database = getRecordDatabase()
  const businessDatabase = getDatabase()
  const insertStatement = database.prepare(`
    INSERT INTO usage_records (
      id, system_account_id, trace_id, client_ip, api_key_id, group_id, account_id, endpoint, provider_code, model, stream,
      status_code, success, first_token_ms, duration_ms, input_tokens, output_tokens, cache_read_tokens, input_image_tokens, output_image_tokens, cost_usd, error_code, error_message,
      request_snapshot_json, response_snapshot_json,
      account_owner_system_account_id, group_owner_system_account_id, account_access_type, group_access_type,
      account_authorization_id, account_authorization_source_type, account_authorization_source_team_id,
      group_authorization_id, group_authorization_source_type, group_authorization_source_team_id,
      created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const updateAccountStatement = businessDatabase.prepare('UPDATE accounts SET last_used_at = ?, updated_at = ? WHERE id = ?')
  const accountLastUsedAt = new Map<string, string>()

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      if (input.apiKeyId && !apiKeyExists(input.apiKeyId)) {
        continue
      }
      const now = input.createdAt ?? nowIso()
      const systemAccountId = input.systemAccountId ?? systemAccountIdForUsage(input)
      const accessMetadata = usageAccessMetadata({ ...input, systemAccountId })
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

function apiKeyExists(apiKeyId: string): boolean {
  const row = getDatabase().prepare('SELECT id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function hydrateUsageRecordNames(rows: Array<Record<string, unknown>>): Array<Record<string, unknown>> {
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

function usageRecordSummaryFromRow(
  row: Record<string, unknown>,
  shouldIncludeSystemAccountFields: boolean,
  accountNames: Map<string, string>,
  includeSnapshots = false
): UsageRecordSummary {
  const requestSnapshot = includeSnapshots ? parseOptionalJsonObject(row.request_snapshot_json) : undefined
  const inputTokens = typeof row.input_tokens === 'number' ? row.input_tokens : undefined
  const outputTokens = typeof row.output_tokens === 'number' ? row.output_tokens : undefined
  const cacheReadTokens = typeof row.cache_read_tokens === 'number' ? row.cache_read_tokens : undefined
  const inputImageTokens = typeof row.input_image_tokens === 'number' ? row.input_image_tokens : undefined
  const outputImageTokens = typeof row.output_image_tokens === 'number' ? row.output_image_tokens : undefined
  const model = optionalString(row.model)
  const stream = row.stream === 1
  const statusCode = typeof row.status_code === 'number' ? row.status_code : undefined
  const success = row.success === 1
  return {
    id: String(row.id),
    systemAccountId: shouldIncludeSystemAccountFields ? optionalString(row.system_account_id) : undefined,
    systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(String(row.system_account_id)) : undefined,
    traceId: String(row.trace_id),
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
    firstTokenMs: typeof row.first_token_ms === 'number' ? row.first_token_ms : undefined,
    durationMs: typeof row.duration_ms === 'number' ? row.duration_ms : undefined,
    inputTokens,
    outputTokens,
    cacheReadTokens,
    inputImageTokens,
    outputImageTokens,
    costUsd: typeof row.cost_usd === 'number' ? row.cost_usd : undefined,
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    requestSnapshot,
    responseSnapshot: includeSnapshots ? parseOptionalJsonObject(row.response_snapshot_json) : undefined,
    createdAt: String(row.created_at)
  }
}

function normalizeUsageRecordListOptions(options?: UsageRecordListOptions): Required<Pick<UsageRecordListOptions, 'page' | 'pageSize' | 'sortBy' | 'sortOrder'>> {
  const sortBy = options?.sortBy && Object.prototype.hasOwnProperty.call(usageRecordSortColumns, options.sortBy)
    ? options.sortBy
    : 'createdAt'
  const sortOrder = options?.sortOrder === 'asc' ? 'asc' : 'desc'
  const rawPage = options?.page
  const rawPageSize = options?.pageSize ?? options?.limit
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(usageRecordMaxPageSize, Math.max(1, rawPageSize))
    : usageRecordDefaultPageSize
  return { page, pageSize, sortBy, sortOrder }
}

function buildUsageRecordOrderClause(options: Required<Pick<UsageRecordListOptions, 'page' | 'pageSize' | 'sortBy' | 'sortOrder'>>): string {
  const direction = options.sortOrder === 'asc' ? 'ASC' : 'DESC'
  if (options.sortBy === 'createdAt') {
    return `ORDER BY ur.created_at ${direction}, ur.id ${direction}`
  }
  return `ORDER BY ${usageRecordSortColumns[options.sortBy]} ${direction}, ur.created_at ${direction}, ur.id ${direction}`
}

function buildUsageRecordFilters(access?: AccessScope, options?: UsageRecordListOptions): { clause: string; params: UsageRecordFilterValue[]; needsAccountJoin: boolean } {
  const clauses: string[] = []
  const params: UsageRecordFilterValue[] = []
  const scope = buildSystemAccountScopeClause(access, 'ur.system_account_id')
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ AND /, ''))
    params.push(...scope.params)
  }
  const accountKeyword = options?.accountKeyword?.trim()
  if (accountKeyword) {
    const matchedAccountIds = accountIdsForKeyword(accountKeyword)
    if (matchedAccountIds.length > 0) {
      clauses.push(`(ur.account_id LIKE ? OR ur.account_id IN (${matchedAccountIds.map(() => '?').join(', ')}))`)
      params.push(`%${accountKeyword}%`, ...matchedAccountIds)
    } else {
      clauses.push('ur.account_id LIKE ?')
      params.push(`%${accountKeyword}%`)
    }
  }
  if (options?.result === 'success') {
    clauses.push('ur.success = 1')
  } else if (options?.result === 'failed') {
    clauses.push('ur.success = 0')
  }
  if (isHttpStatusCode(options?.statusCode)) {
    clauses.push('ur.status_code = ?')
    params.push(options.statusCode)
  }
  const startAt = options?.startAt?.trim()
  if (startAt) {
    clauses.push('ur.created_at >= ?')
    params.push(startAt)
  }
  const endAt = options?.endAt?.trim()
  if (endAt) {
    clauses.push('ur.created_at < ?')
    params.push(endAt)
  }
  const model = options?.model?.trim()
  if (model) {
    clauses.push('ur.model LIKE ?')
    params.push(`%${model}%`)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params,
    needsAccountJoin: false
  }
}

function accountIdsForKeyword(keyword: string): string[] {
  const pattern = `%${keyword}%`
  const rows = getDatabase()
    .prepare('SELECT id FROM accounts WHERE name LIKE ? OR id LIKE ? LIMIT 200')
    .all(pattern, pattern) as unknown as Array<{ id?: string }>
  return rows.map((row) => row.id).filter((id): id is string => Boolean(id))
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}

function endpointFromSnapshot(snapshot?: Record<string, unknown>): string | undefined {
  const method = typeof snapshot?.method === 'string' ? snapshot.method.toUpperCase() : undefined
  const originalUrl = typeof snapshot?.originalUrl === 'string' ? snapshot.originalUrl.split('?')[0] : undefined
  const path = typeof snapshot?.path === 'string' ? snapshot.path : undefined
  const endpoint = originalUrl ?? path
  return endpoint ? `${method ?? 'GET'} ${endpoint}` : undefined
}

function systemAccountIdForUsage(input: { apiKeyId?: string; groupId?: string; accountId?: string }): string {
  const database = getDatabase()
  if (input.apiKeyId) {
    const row = database.prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(input.apiKeyId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  if (input.groupId) {
    const row = database.prepare('SELECT system_account_id FROM groups WHERE id = ?').get(input.groupId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  if (input.accountId) {
    const row = database.prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(input.accountId) as unknown as { system_account_id?: string } | undefined
    if (row?.system_account_id) return row.system_account_id
  }
  return currentSystemAccountId()
}

function usageAccessMetadata(input: {
  systemAccountId: string
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
}): UsageAccessMetadata {
  const groupOwnerSystemAccountId = input.groupOwnerSystemAccountId ?? (input.groupId ? groupOwnerSystemAccountIdForUsage(input.groupId) : undefined)
  const groupAuthorization = input.groupAuthorizationId
    ? undefined
    : input.groupId && groupOwnerSystemAccountId !== input.systemAccountId
      ? activeResourceAuthorization('group', input.groupId, input.systemAccountId)
      : undefined
  const groupAuthorizationId = input.groupAuthorizationId ?? groupAuthorization?.id
  const groupAuthorizationSnapshot = groupAuthorizationId
    ? input.groupAuthorizationId === groupAuthorization?.id
      ? groupAuthorization
      : resourceAuthorizationSnapshot(groupAuthorizationId)
    : undefined
  const groupAccessType = input.groupAccessType
    ?? (groupOwnerSystemAccountId
      ? groupOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAuthorization
          ? 'authorized'
          : undefined
      : undefined)
  const accountOwnerSystemAccountId = input.accountOwnerSystemAccountId ?? (input.accountId ? accountSystemAccountIdForUsage(input.accountId) : undefined)
  const accountAuthorization = input.accountAuthorizationId
    ? undefined
    : input.accountId && accountOwnerSystemAccountId !== input.systemAccountId && groupAccessType !== 'authorized'
      ? activeResourceAuthorization('account', input.accountId, input.systemAccountId)
      : undefined
  const accountAuthorizationId = accountAccessTypeCandidate(input, accountOwnerSystemAccountId, groupAccessType, groupOwnerSystemAccountId, accountAuthorization)
  const accountAuthorizationSnapshot = accountAuthorizationId
    ? input.accountAuthorizationId === accountAuthorization?.id
      ? accountAuthorization
      : resourceAuthorizationSnapshot(accountAuthorizationId)
    : undefined
  const accountAccessType = input.accountAccessType
    ?? (accountOwnerSystemAccountId
      ? accountOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAccessType === 'authorized' && groupOwnerSystemAccountId === accountOwnerSystemAccountId
          ? 'group_authorized'
          : accountAuthorization
            ? 'account_authorized'
            : undefined
      : undefined)
  return {
    accountOwnerSystemAccountId,
    groupOwnerSystemAccountId,
    accountAccessType,
    groupAccessType,
    accountAuthorizationId: accountAccessType === 'account_authorized' ? accountAuthorizationId : undefined,
    accountAuthorizationSourceType: accountAccessType === 'account_authorized'
      ? input.accountAuthorizationSourceType ?? accountAuthorizationSnapshot?.effective_source_type ?? undefined
      : undefined,
    accountAuthorizationSourceTeamId: accountAccessType === 'account_authorized'
      ? input.accountAuthorizationSourceTeamId ?? accountAuthorizationSnapshot?.effective_source_team_id ?? undefined
      : undefined,
    groupAuthorizationId,
    groupAuthorizationSourceType: groupAuthorizationId
      ? input.groupAuthorizationSourceType ?? groupAuthorizationSnapshot?.effective_source_type ?? undefined
      : undefined,
    groupAuthorizationSourceTeamId: groupAuthorizationId
      ? input.groupAuthorizationSourceTeamId ?? groupAuthorizationSnapshot?.effective_source_team_id ?? undefined
      : undefined
  }
}

function accountAccessTypeCandidate(
  input: {
    systemAccountId: string
    accountAccessType?: 'owner' | 'account_authorized' | 'group_authorized'
    accountAuthorizationId?: string
  },
  accountOwnerSystemAccountId: string | undefined,
  groupAccessType: 'owner' | 'authorized' | undefined,
  groupOwnerSystemAccountId: string | undefined,
  accountAuthorization: ResourceAuthorizationRow | undefined
): string | undefined {
  const accountAccessType = input.accountAccessType
    ?? (accountOwnerSystemAccountId
      ? accountOwnerSystemAccountId === input.systemAccountId
        ? 'owner'
        : groupAccessType === 'authorized' && groupOwnerSystemAccountId === accountOwnerSystemAccountId
          ? 'group_authorized'
          : accountAuthorization
            ? 'account_authorized'
            : undefined
      : undefined)
  return accountAccessType === 'account_authorized' ? input.accountAuthorizationId ?? accountAuthorization?.id : undefined
}

function accountSystemAccountIdForUsage(accountId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM accounts WHERE id = ?').get(accountId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function groupOwnerSystemAccountIdForUsage(groupId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function activeResourceAuthorization(resourceType: 'account' | 'group', resourceId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getDatabase()
    .prepare("SELECT id, status, expires_at, effective_source_type, effective_source_team_id FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1")
    .get(resourceType, resourceId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
}

function resourceAuthorizationSnapshot(authorizationId: string): ResourceAuthorizationRow | undefined {
  return getDatabase()
    .prepare('SELECT id, status, expires_at, effective_source_type, effective_source_team_id FROM resource_authorizations WHERE id = ? LIMIT 1')
    .get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
}
