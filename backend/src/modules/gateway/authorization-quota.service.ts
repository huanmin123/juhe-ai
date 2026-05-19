import { createAppCache } from '../../shared/cache.js'
import { runtimeConfig } from '../../config/runtime.js'
import type { RequestQuotaLimits } from '../../domain/types.js'
import { getDatabase, getRecordDatabase } from '../../storage/database.js'
import { chunkValues, sqlPlaceholders } from '../../storage/query-utils.js'
import type { GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../storage/repositories.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from '../../storage/request-quota-limits.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from './request-quota-checker.js'

export const AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE = '额度已用完，请联系管理员提升额度'

export interface AuthorizationQuotaDecision {
  allowed: boolean
  message?: string
}

type AuthorizationQuotaCacheEntry = AuthorizationQuotaDecision & {
  checkedAtMs: number
}

interface AuthorizationQuotaRow {
  id: string
  resource_owner_system_account_id: string
  resource_type: 'account' | 'group'
  resource_id: string
  effective_source_team_id: string | null
  limits_json: string | null
}

interface TeamAuthorizationQuotaRow {
  id: string
  resource_owner_system_account_id: string
  resource_type: 'account' | 'group'
  resource_id: string
  limits_json: string | null
}

type AuthorizationQuotaScopeType = 'account_authorization' | 'group_authorization'

interface AuthorizationQuotaScopeRequest {
  authorizationId: string
  scopeType: AuthorizationQuotaScopeType
}

interface AuthorizationQuotaCostCheck {
  cacheKey: string
  limits: RequestQuotaLimits
  costInput: RequestQuotaCostInput
}

type TeamAuthorizationQuotaBatchRow = TeamAuthorizationQuotaRow & {
  authorization_id: string
}

const AUTHORIZATION_QUOTA_CACHE_TTL_MS = 5_000
const authorizationQuotaCache = createAppCache<string, AuthorizationQuotaCacheEntry>({
  name: 'gateway:authorization-quota',
  max: 10000,
  ttlMs: AUTHORIZATION_QUOTA_CACHE_TTL_MS,
  updateAgeOnGet: true
})

export function checkGatewayAuthorizationQuota(input: {
  groupAccess: GroupUsageAccessMetadata
  account?: OpenAIAccountSecret
  now?: Date
}): AuthorizationQuotaDecision {
  return checkGatewayAuthorizationQuotaByIds({
    groupAuthorizationId: input.groupAccess.groupAuthorizationId,
    accountAuthorizationId: input.account?.accountAuthorizationId,
    now: input.now
  })
}

export async function checkGatewayAuthorizationQuotaAsync(input: {
  groupAccess: GroupUsageAccessMetadata
  account?: OpenAIAccountSecret
}): Promise<AuthorizationQuotaDecision> {
  if (!input.groupAccess.groupAuthorizationId && !input.account?.accountAuthorizationId) {
    return { allowed: true }
  }
  return await requestDbService({
    type: 'check_authorization_quota',
    groupAuthorizationId: input.groupAccess.groupAuthorizationId,
    accountAuthorizationId: input.account?.accountAuthorizationId
  })
}

export async function checkGatewayAuthorizationQuotaBatchAsync(input: {
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
}): Promise<Map<string, AuthorizationQuotaDecision>> {
  const hasGroupAuthorizationQuota = Boolean(input.groupAccess.groupAuthorizationId)
  const accountsToCheck = hasGroupAuthorizationQuota
    ? input.accounts
    : input.accounts.filter((account) => Boolean(account.accountAuthorizationId))
  if (!accountsToCheck.length) {
    return new Map(input.accounts.map((account) => [account.id, { allowed: true }]))
  }
  const decisions = await requestDbService({
    type: 'check_authorization_quota_batch',
    groupAuthorizationId: input.groupAccess.groupAuthorizationId,
    accounts: accountsToCheck.map((account) => ({
      accountId: account.id,
      accountAuthorizationId: account.accountAuthorizationId
    }))
  })
  const output = new Map<string, AuthorizationQuotaDecision>()
  accountsToCheck.forEach((account, index) => {
    output.set(account.id, decisions[index] ?? { allowed: true })
  })
  input.accounts.forEach((account) => {
    if (!output.has(account.id)) {
      output.set(account.id, { allowed: true })
    }
  })
  return output
}

export function checkGatewayAuthorizationQuotaByIds(input: {
  groupAuthorizationId?: string
  accountAuthorizationId?: string
  now?: Date
}): AuthorizationQuotaDecision {
  return checkGatewayAuthorizationQuotaBatchByIds({
    groupAuthorizationId: input.groupAuthorizationId,
    accounts: [{
      accountId: input.accountAuthorizationId ?? '',
      accountAuthorizationId: input.accountAuthorizationId
    }],
    now: input.now
  })[0] ?? { allowed: true }
}

export function checkGatewayAuthorizationQuotaBatchByIds(input: {
  groupAuthorizationId?: string
  accounts: Array<{
    accountId: string
    accountAuthorizationId?: string
  }>
  now?: Date
}): AuthorizationQuotaDecision[] {
  assertLocalGatewayDatabaseAccess('checkGatewayAuthorizationQuotaBatchByIds')
  const now = input.now ?? new Date()
  const scopes = uniqueAuthorizationQuotaScopes([
    ...(input.groupAuthorizationId ? [{ authorizationId: input.groupAuthorizationId, scopeType: 'group_authorization' as const }] : []),
    ...input.accounts
      .filter((account) => Boolean(account.accountAuthorizationId))
      .map((account) => ({ authorizationId: account.accountAuthorizationId as string, scopeType: 'account_authorization' as const }))
  ])
  const costChecksByScope = loadAuthorizationQuotaCostChecksByScope(scopes, now)
  const allCostChecks = uniqueAuthorizationQuotaCostChecks([...costChecksByScope.values()].flat())
  const checksByCacheKey = materializeAuthorizationQuotaCostCheckMap(allCostChecks)

  return input.accounts.map((account) => {
    const checks = [
      ...authorizationQuotaChecksForScope(input.groupAuthorizationId, 'group_authorization', costChecksByScope, checksByCacheKey),
      ...authorizationQuotaChecksForScope(account.accountAuthorizationId, 'account_authorization', costChecksByScope, checksByCacheKey)
    ]
    return checks.length ? authorizationQuotaDecisionFromChecks(checks) : { allowed: true }
  })
}

function authorizationQuotaDecisionFromChecks(checks: AuthorizationQuotaCheck[]): AuthorizationQuotaDecision {
  const cacheKey = checks.map((check) => check.cacheKey).join('|')
  const cached = authorizationQuotaCache.get(cacheKey)
  if (cached) {
    return cached
  }
  const allowed = checks.every((check) => !check.exceeded)
  const decision: AuthorizationQuotaCacheEntry = {
    allowed,
    message: allowed ? undefined : AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE,
    checkedAtMs: Date.now()
  }
  authorizationQuotaCache.set(cacheKey, decision)
  return decision
}

export function clearAuthorizationQuotaCache(): void {
  authorizationQuotaCache.clear()
}

interface AuthorizationQuotaCheck {
  cacheKey: string
  exceeded: boolean
}

function authorizationQuotaCostChecksForAuthorizationRow(row: AuthorizationQuotaRow, scopeType: AuthorizationQuotaScopeType, now: Date): AuthorizationQuotaCostCheck[] {
  const limits = parseRequestQuotaLimitsJson(row.limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) return []
  return [{
    cacheKey: `authorization\u0000${row.resource_owner_system_account_id}\u0000${scopeType}\u0000${row.id}\u0000${row.limits_json ?? ''}`,
    limits,
    costInput: {
      systemAccountId: row.resource_owner_system_account_id,
      scopeType,
      scopeId: row.id,
      now,
      hourlyWindowHours: limits.hourly?.hours
    }
  }]
}

function authorizationQuotaCostChecksForTeamRow(row: TeamAuthorizationQuotaRow, scopeType: AuthorizationQuotaScopeType, teamId: string, now: Date): AuthorizationQuotaCostCheck[] {
  const limits = parseRequestQuotaLimitsJson(row.limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) return []
  return [{
    cacheKey: `team_authorization\u0000${row.resource_owner_system_account_id}\u0000${scopeType}\u0000${teamId}\u0000${row.id}\u0000${row.limits_json ?? ''}`,
    limits,
    costInput: {
      systemAccountId: row.resource_owner_system_account_id,
      scopeType: teamAuthorizationScopeType(scopeType),
      scopeId: `${teamAuthorizationResourceId(row)}:${teamId}`,
      now,
      hourlyWindowHours: limits.hourly?.hours
    }
  }]
}

function materializeAuthorizationQuotaCostChecks(costChecks: AuthorizationQuotaCostCheck[]): AuthorizationQuotaCheck[] {
  const costsByKey = loadRequestQuotaCostsBatch(getRecordDatabase(), costChecks.map((check) => check.costInput))
  return costChecks.map((check) => ({
    cacheKey: check.cacheKey,
    exceeded: isRequestQuotaExceeded(check.limits, costsByKey.get(requestQuotaCostKey(check.costInput)) ?? emptyRequestQuotaCosts())
  }))
}

function materializeAuthorizationQuotaCostCheckMap(costChecks: AuthorizationQuotaCostCheck[]): Map<string, AuthorizationQuotaCheck> {
  return new Map(materializeAuthorizationQuotaCostChecks(costChecks).map((check) => [check.cacheKey, check]))
}

function loadAuthorizationQuotaCostChecksByScope(scopes: AuthorizationQuotaScopeRequest[], now: Date): Map<string, AuthorizationQuotaCostCheck[]> {
  const rowsById = loadAuthorizationQuotaRows(scopes.map((scope) => scope.authorizationId))
  const teamRowsByAuthorizationId = loadTeamAuthorizationQuotaRowsByAuthorizationId([...rowsById.values()])
  const output = new Map<string, AuthorizationQuotaCostCheck[]>()
  for (const scope of scopes) {
    const row = rowsById.get(scope.authorizationId)
    const checks = row ? authorizationQuotaCostChecksForAuthorizationRow(row, scope.scopeType, now) : []
    if (row?.effective_source_team_id) {
      const teamRow = teamRowsByAuthorizationId.get(row.id)
      if (teamRow) {
        checks.push(...authorizationQuotaCostChecksForTeamRow(teamRow, scope.scopeType, row.effective_source_team_id, now))
      }
    }
    output.set(authorizationQuotaScopeKey(scope.authorizationId, scope.scopeType), checks)
  }
  return output
}

function loadAuthorizationQuotaRows(authorizationIds: string[]): Map<string, AuthorizationQuotaRow> {
  const ids = [...new Set(authorizationIds.filter(Boolean))]
  if (!ids.length) return new Map()
  const rows: AuthorizationQuotaRow[] = []
  const database = getDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database.prepare(`
      SELECT id, resource_owner_system_account_id, resource_type, resource_id, effective_source_team_id, limits_json
      FROM resource_authorizations
      WHERE status = 'active'
        AND id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as AuthorizationQuotaRow[])
  }
  return new Map(rows.map((row) => [row.id, row]))
}

function loadTeamAuthorizationQuotaRowsByAuthorizationId(rows: AuthorizationQuotaRow[]): Map<string, TeamAuthorizationQuotaRow> {
  const ids = rows.filter((row) => row.effective_source_team_id).map((row) => row.id)
  if (!ids.length) return new Map()
  const teamRows: TeamAuthorizationQuotaBatchRow[] = []
  const database = getDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    teamRows.push(...database.prepare(`
      SELECT ra.id AS authorization_id, grant_rows.id, grant_rows.resource_owner_system_account_id, grant_rows.resource_type, grant_rows.resource_id, grant_rows.limits_json
      FROM resource_authorizations ra
      INNER JOIN resource_authorization_grants grant_rows
        ON grant_rows.resource_type = ra.resource_type
        AND grant_rows.resource_id = ra.resource_id
        AND grant_rows.grantee_type = 'team'
        AND grant_rows.grantee_team_id = ra.effective_source_team_id
        AND grant_rows.status = 'active'
      WHERE ra.status = 'active'
        AND ra.effective_source_team_id IS NOT NULL
        AND ra.id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as TeamAuthorizationQuotaBatchRow[])
  }
  return new Map(teamRows.map((row) => [row.authorization_id, row]))
}

function authorizationQuotaChecksForScope(
  authorizationId: string | undefined,
  scopeType: AuthorizationQuotaScopeType,
  costChecksByScope: Map<string, AuthorizationQuotaCostCheck[]>,
  checksByCacheKey: Map<string, AuthorizationQuotaCheck>
): AuthorizationQuotaCheck[] {
  if (!authorizationId) return []
  return (costChecksByScope.get(authorizationQuotaScopeKey(authorizationId, scopeType)) ?? [])
    .map((check) => checksByCacheKey.get(check.cacheKey))
    .filter((check): check is AuthorizationQuotaCheck => Boolean(check))
}

function uniqueAuthorizationQuotaScopes(scopes: AuthorizationQuotaScopeRequest[]): AuthorizationQuotaScopeRequest[] {
  const seen = new Set<string>()
  const output: AuthorizationQuotaScopeRequest[] = []
  for (const scope of scopes) {
    const key = authorizationQuotaScopeKey(scope.authorizationId, scope.scopeType)
    if (seen.has(key)) continue
    seen.add(key)
    output.push(scope)
  }
  return output
}

function uniqueAuthorizationQuotaCostChecks(checks: AuthorizationQuotaCostCheck[]): AuthorizationQuotaCostCheck[] {
  const seen = new Set<string>()
  const output: AuthorizationQuotaCostCheck[] = []
  for (const check of checks) {
    if (seen.has(check.cacheKey)) continue
    seen.add(check.cacheKey)
    output.push(check)
  }
  return output
}

function authorizationQuotaScopeKey(authorizationId: string, scopeType: AuthorizationQuotaScopeType): string {
  return `${scopeType}\u0000${authorizationId}`
}

function emptyRequestQuotaCosts() {
  return { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 }
}

function teamAuthorizationScopeType(scopeType: 'account_authorization' | 'group_authorization'): 'account_authorization_team' | 'group_authorization_team' {
  return scopeType === 'account_authorization' ? 'account_authorization_team' : 'group_authorization_team'
}

function teamAuthorizationResourceId(row: TeamAuthorizationQuotaRow): string {
  return row.resource_id
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步读取 SQLite：${operation} 必须通过 DB service`)
  }
}
