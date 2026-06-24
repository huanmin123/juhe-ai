import { createAppCache } from '../../../shared/cache.js'
import { registerAuthorizationQuotaCacheInvalidator } from '../../../shared/gateway-cache-invalidation.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import type { RequestQuotaLimits } from '../../../domain/types.js'
import { getBusinessDatabase, getStatsDatabase } from '../../../storage/database.js'
import { chunkValues, sqlPlaceholders } from '../../../storage/query-utils.js'
import type { GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../../storage/repositories.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from '../../../storage/request-quota-limits.js'
import { gatewayAuthorizationQuotaSnapshotVersion, isGatewayAuthorizationSnapshotIncomplete, readGatewayAuthorizationQuotaSnapshot } from './quota-snapshot-cache.service.js'
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
  grantee_system_account_id: string
  resource_type: 'account' | 'group'
  resource_id: string
  instance_account_id: string | null
  effective_source_team_id: string | null
  limits_json: string | null
}

interface TeamAuthorizationQuotaRow {
  id: string
  resource_owner_system_account_id: string
  authorization_grantee_system_account_id?: string | null
  resource_type: 'account' | 'group'
  resource_id: string
  authorization_instance_account_id?: string | null
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
  updateAgeOnGet: false
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
  const now = new Date()
  const cacheKey = authorizationQuotaRuntimeCacheKey(input.groupAccess.groupAuthorizationId, input.account?.accountAuthorizationId, now)
  const cached = authorizationQuotaCache.get(cacheKey)
  if (cached) {
    return cached
  }
  if (runtimeConfig.processRole === 'server') {
    const snapshotInput = {
      groupAuthorizationId: input.groupAccess.groupAuthorizationId,
      groupAuthorizationQuotaLimited: input.groupAccess.groupAuthorizationQuotaLimited,
      accountAuthorizationId: input.account?.accountAuthorizationId,
      accountAuthorizationQuotaLimited: input.account?.accountAuthorizationQuotaLimited
    }
    if (authorizationQuotaSnapshotNeedsDbFallback(snapshotInput)) {
      try {
        const dbService = await import('../../db-service/db-service-ipc.js')
        const decision = await dbService.requestDbService({
          type: 'check_authorization_quota',
          groupAuthorizationId: input.groupAccess.groupAuthorizationId,
          accountAuthorizationId: input.account?.accountAuthorizationId
        }, { timeoutMs: 1000 })
        authorizationQuotaCache.set(cacheKey, {
          ...decision,
          checkedAtMs: Date.now()
        })
        return decision
      } catch (error) {
        logger.warn(errorLogFields(error, {
          event: 'gateway_authorization_quota_snapshot_fallback_failed',
          groupAuthorizationId: input.groupAccess.groupAuthorizationId,
          accountAuthorizationId: input.account?.accountAuthorizationId
        }), '授权配额快照不完整且 DB service 精确补判失败，按保护策略继续使用快照判定')
      }
    }
    const decision = authorizationQuotaDecisionFromSnapshot(snapshotInput)
    authorizationQuotaCache.set(cacheKey, {
      ...decision,
      checkedAtMs: Date.now()
    })
    return decision
  }
  const decision = checkGatewayAuthorizationQuotaByIds({
    groupAuthorizationId: input.groupAccess.groupAuthorizationId,
    accountAuthorizationId: input.account?.accountAuthorizationId
  })
  authorizationQuotaCache.set(cacheKey, {
    ...decision,
    checkedAtMs: Date.now()
  })
  return decision
}

export async function checkGatewayAuthorizationQuotaBatchAsync(input: {
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
}): Promise<Map<string, AuthorizationQuotaDecision>> {
  const hasGroupAuthorizationQuota = Boolean(input.groupAccess.groupAuthorizationId)
  const now = new Date()
  const accountsToCheck = hasGroupAuthorizationQuota
    ? input.accounts
    : input.accounts.filter((account) => Boolean(account.accountAuthorizationId))
  if (!accountsToCheck.length) {
    return new Map(input.accounts.map((account) => [account.id, { allowed: true }]))
  }
  const cachedDecisionsByAccountId = new Map<string, AuthorizationQuotaDecision>()
  const missingAccounts: OpenAIAccountSecret[] = []
  const missingCacheKeys = new Map<string, string>()
  const requestedMissingCacheKeys = new Set<string>()
  for (const account of accountsToCheck) {
    const cacheKey = authorizationQuotaRuntimeCacheKey(input.groupAccess.groupAuthorizationId, account.accountAuthorizationId, now)
    const cached = authorizationQuotaCache.get(cacheKey)
    if (cached) {
      cachedDecisionsByAccountId.set(account.id, cached)
      continue
    }
    missingCacheKeys.set(account.id, cacheKey)
    if (requestedMissingCacheKeys.has(cacheKey)) {
      continue
    }
    requestedMissingCacheKeys.add(cacheKey)
    missingAccounts.push(account)
  }
  if (!missingAccounts.length) {
    const output = new Map<string, AuthorizationQuotaDecision>()
    input.accounts.forEach((account) => {
      output.set(account.id, cachedDecisionsByAccountId.get(account.id) ?? { allowed: true })
    })
    return output
  }
  if (runtimeConfig.processRole === 'server') {
    const output = new Map<string, AuthorizationQuotaDecision>()
    for (const [accountId, decision] of cachedDecisionsByAccountId.entries()) {
      output.set(accountId, decision)
    }
    if (authorizationQuotaBatchSnapshotNeedsDbFallback(input.groupAccess, missingAccounts)) {
      try {
        const dbService = await import('../../db-service/db-service-ipc.js')
        const decisions = await dbService.requestDbService({
          type: 'check_authorization_quota_batch',
          groupAuthorizationId: input.groupAccess.groupAuthorizationId,
          accounts: missingAccounts.map((account) => ({
            accountId: account.id,
            accountAuthorizationId: account.accountAuthorizationId
          }))
        }, { timeoutMs: 1000 })
        const missingDecisionsByCacheKey = new Map<string, AuthorizationQuotaDecision>()
        missingAccounts.forEach((account, index) => {
          const decision = decisions[index] ?? { allowed: true }
          const cacheKey = missingCacheKeys.get(account.id)
          if (cacheKey) {
            missingDecisionsByCacheKey.set(cacheKey, decision)
            authorizationQuotaCache.set(cacheKey, {
              ...decision,
              checkedAtMs: Date.now()
            })
          }
        })
        for (const [accountId, cacheKey] of missingCacheKeys.entries()) {
          output.set(accountId, missingDecisionsByCacheKey.get(cacheKey) ?? { allowed: true })
        }
        input.accounts.forEach((account) => {
          if (!output.has(account.id)) {
            output.set(account.id, { allowed: true })
          }
        })
        return output
      } catch (error) {
        logger.warn(errorLogFields(error, {
          event: 'gateway_authorization_quota_batch_snapshot_fallback_failed',
          groupAuthorizationId: input.groupAccess.groupAuthorizationId,
          accountCount: missingAccounts.length
        }), '授权配额快照不完整且 DB service 批量补判失败，按保护策略继续使用快照判定')
      }
    }
    for (const account of missingAccounts) {
      const cacheKey = missingCacheKeys.get(account.id)
      const decision = authorizationQuotaDecisionFromSnapshot({
        groupAuthorizationId: input.groupAccess.groupAuthorizationId,
        groupAuthorizationQuotaLimited: input.groupAccess.groupAuthorizationQuotaLimited,
        accountAuthorizationId: account.accountAuthorizationId,
        accountAuthorizationQuotaLimited: account.accountAuthorizationQuotaLimited
      })
      output.set(account.id, decision)
      if (cacheKey) {
        authorizationQuotaCache.set(cacheKey, {
          ...decision,
          checkedAtMs: Date.now()
        })
      }
    }
    for (const [accountId, cacheKey] of missingCacheKeys.entries()) {
      if (!output.has(accountId)) {
        const account = accountsToCheck.find((item) => item.id === accountId)
        const decision = authorizationQuotaDecisionFromSnapshot({
          groupAuthorizationId: input.groupAccess.groupAuthorizationId,
          groupAuthorizationQuotaLimited: input.groupAccess.groupAuthorizationQuotaLimited,
          accountAuthorizationId: account?.accountAuthorizationId,
          accountAuthorizationQuotaLimited: account?.accountAuthorizationQuotaLimited
        })
        output.set(accountId, decision)
        authorizationQuotaCache.set(cacheKey, {
          ...decision,
          checkedAtMs: Date.now()
        })
      }
    }
    input.accounts.forEach((account) => {
      if (!output.has(account.id)) {
        output.set(account.id, { allowed: true })
      }
    })
    return output
  }
  const decisions = checkGatewayAuthorizationQuotaBatchByIds({
    groupAuthorizationId: input.groupAccess.groupAuthorizationId,
    accounts: missingAccounts.map((account) => ({
      accountId: account.id,
      accountAuthorizationId: account.accountAuthorizationId
    }))
  })
  const output = new Map<string, AuthorizationQuotaDecision>()
  for (const [accountId, decision] of cachedDecisionsByAccountId.entries()) {
    output.set(accountId, decision)
  }
  const missingDecisionsByCacheKey = new Map<string, AuthorizationQuotaDecision>()
  missingAccounts.forEach((account, index) => {
    const decision = decisions[index] ?? { allowed: true }
    const cacheKey = missingCacheKeys.get(account.id)
    if (cacheKey) {
      missingDecisionsByCacheKey.set(cacheKey, decision)
      authorizationQuotaCache.set(cacheKey, {
        ...decision,
        checkedAtMs: Date.now()
      })
    }
  })
  for (const [accountId, cacheKey] of missingCacheKeys.entries()) {
    output.set(accountId, missingDecisionsByCacheKey.get(cacheKey) ?? { allowed: true })
  }
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
  const systemAccountId = authorizationQuotaStatsSystemAccountId(row, scopeType)
  const costInput = {
    systemAccountId,
    scopeType,
    scopeId: row.id,
    now,
    hourlyWindowHours: limits.hourly?.hours
  }
  return [{
    cacheKey: `authorization\u0000${systemAccountId}\u0000${scopeType}\u0000${row.id}\u0000${requestQuotaCostKey(costInput)}\u0000${row.limits_json ?? ''}`,
    limits,
    costInput
  }]
}

function authorizationQuotaCostChecksForTeamRow(row: TeamAuthorizationQuotaRow, scopeType: AuthorizationQuotaScopeType, teamId: string, now: Date): AuthorizationQuotaCostCheck[] {
  const limits = parseRequestQuotaLimitsJson(row.limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) return []
  const systemAccountId = teamAuthorizationQuotaStatsSystemAccountId(row, scopeType)
  const resourceId = teamAuthorizationResourceId(row, scopeType)
  if (!resourceId) return []
  const scopeId = `${resourceId}:${teamId}`
  const costInput = {
    systemAccountId,
    scopeType: teamAuthorizationScopeType(scopeType),
    scopeId,
    now,
    hourlyWindowHours: limits.hourly?.hours
  }
  return [{
    cacheKey: `team_authorization\u0000${systemAccountId}\u0000${scopeType}\u0000${teamId}\u0000${row.id}\u0000${requestQuotaCostKey(costInput)}\u0000${row.limits_json ?? ''}`,
    limits,
    costInput
  }]
}

function materializeAuthorizationQuotaCostChecks(costChecks: AuthorizationQuotaCostCheck[]): AuthorizationQuotaCheck[] {
  const costsByKey = loadRequestQuotaCostsBatch(getStatsDatabase(), costChecks.map((check) => check.costInput))
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
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    rows.push(...database.prepare(`
      SELECT ra.id, ra.resource_owner_system_account_id, ra.grantee_system_account_id, ra.resource_type, ra.resource_id,
        instance_accounts.id AS instance_account_id,
        ra.effective_source_team_id, ra.limits_json
      FROM resource_authorizations ra
      LEFT JOIN accounts instance_accounts
        ON ra.resource_type = 'account'
        AND instance_accounts.authorization_instance_authorization_id = ra.id
        AND instance_accounts.system_account_id = ra.grantee_system_account_id
        AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
      WHERE ra.status = 'active'
        AND ra.id IN (${sqlPlaceholders(chunk.length)})
    `).all(...chunk) as unknown as AuthorizationQuotaRow[])
  }
  return new Map(rows.map((row) => [row.id, row]))
}

function loadTeamAuthorizationQuotaRowsByAuthorizationId(rows: AuthorizationQuotaRow[]): Map<string, TeamAuthorizationQuotaRow> {
  const ids = rows.filter((row) => row.effective_source_team_id).map((row) => row.id)
  if (!ids.length) return new Map()
  const teamRows: TeamAuthorizationQuotaBatchRow[] = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    teamRows.push(...database.prepare(`
      SELECT ra.id AS authorization_id, grant_rows.id, grant_rows.resource_owner_system_account_id,
        ra.grantee_system_account_id AS authorization_grantee_system_account_id,
        instance_accounts.id AS authorization_instance_account_id,
        grant_rows.resource_type, grant_rows.resource_id, grant_rows.limits_json
      FROM resource_authorizations ra
      INNER JOIN resource_authorization_grants grant_rows
        ON grant_rows.resource_type = ra.resource_type
        AND grant_rows.resource_id = ra.resource_id
        AND grant_rows.grantee_type = 'team'
        AND grant_rows.grantee_team_id = ra.effective_source_team_id
        AND grant_rows.status = 'active'
      LEFT JOIN accounts instance_accounts
        ON ra.resource_type = 'account'
        AND instance_accounts.authorization_instance_authorization_id = ra.id
        AND instance_accounts.system_account_id = ra.grantee_system_account_id
        AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
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

function authorizationQuotaRuntimeCacheKey(groupAuthorizationId?: string, accountAuthorizationId?: string, now = new Date()): string {
  return `runtime_authorization_quota\u0000${groupAuthorizationId ?? ''}\u0000${accountAuthorizationId ?? ''}\u0000${requestQuotaCostKey({
    systemAccountId: '',
    scopeType: 'authorization_runtime',
    scopeId: '',
    now
  })}\u0000${runtimeConfig.processRole === 'server' ? gatewayAuthorizationQuotaSnapshotVersion() : 0}`
}

function authorizationQuotaBatchSnapshotNeedsDbFallback(
  groupAccess: GroupUsageAccessMetadata,
  accounts: OpenAIAccountSecret[]
): boolean {
  if (!isGatewayAuthorizationSnapshotIncomplete()) {
    return false
  }
  if (
    groupAccess.groupAuthorizationId
    && groupAccess.groupAuthorizationQuotaLimited
    && !readGatewayAuthorizationQuotaSnapshot('group_authorization', groupAccess.groupAuthorizationId)
  ) {
    return true
  }
  return accounts.some((account) => authorizationQuotaSnapshotNeedsDbFallback({
    groupAuthorizationId: groupAccess.groupAuthorizationId,
    groupAuthorizationQuotaLimited: groupAccess.groupAuthorizationQuotaLimited,
    accountAuthorizationId: account.accountAuthorizationId,
    accountAuthorizationQuotaLimited: account.accountAuthorizationQuotaLimited
  }))
}

function authorizationQuotaSnapshotNeedsDbFallback(input: {
  groupAuthorizationId?: string
  groupAuthorizationQuotaLimited?: boolean
  accountAuthorizationId?: string
  accountAuthorizationQuotaLimited?: boolean
}): boolean {
  if (!isGatewayAuthorizationSnapshotIncomplete()) {
    return false
  }
  if (
    input.groupAuthorizationId
    && input.groupAuthorizationQuotaLimited
    && !readGatewayAuthorizationQuotaSnapshot('group_authorization', input.groupAuthorizationId)
  ) {
    return true
  }
  return Boolean(
    input.accountAuthorizationId
    && input.accountAuthorizationQuotaLimited
    && !readGatewayAuthorizationQuotaSnapshot('account_authorization', input.accountAuthorizationId)
  )
}

function authorizationQuotaDecisionFromSnapshot(input: {
  groupAuthorizationId?: string
  groupAuthorizationQuotaLimited?: boolean
  accountAuthorizationId?: string
  accountAuthorizationQuotaLimited?: boolean
}): AuthorizationQuotaDecision {
  const groupDecision = readGatewayAuthorizationQuotaSnapshot('group_authorization', input.groupAuthorizationId)
  if (groupDecision && !groupDecision.allowed) {
    return groupDecision
  }
  if (input.groupAuthorizationId && input.groupAuthorizationQuotaLimited && !groupDecision && isGatewayAuthorizationSnapshotIncomplete()) {
    return { allowed: false, message: AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE }
  }
  const accountDecision = readGatewayAuthorizationQuotaSnapshot('account_authorization', input.accountAuthorizationId)
  if (accountDecision && !accountDecision.allowed) {
    return accountDecision
  }
  if (input.accountAuthorizationId && input.accountAuthorizationQuotaLimited && !accountDecision && isGatewayAuthorizationSnapshotIncomplete()) {
    return { allowed: false, message: AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE }
  }
  return { allowed: true }
}

function emptyRequestQuotaCosts() {
  return { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 }
}

function teamAuthorizationScopeType(scopeType: 'account_authorization' | 'group_authorization'): 'account_authorization_team' | 'group_authorization_team' {
  return scopeType === 'account_authorization' ? 'account_authorization_team' : 'group_authorization_team'
}

function authorizationQuotaStatsSystemAccountId(row: AuthorizationQuotaRow, scopeType: AuthorizationQuotaScopeType): string {
  return scopeType === 'account_authorization'
    ? row.grantee_system_account_id
    : row.resource_owner_system_account_id
}

function teamAuthorizationQuotaStatsSystemAccountId(row: TeamAuthorizationQuotaRow, scopeType: AuthorizationQuotaScopeType): string {
  return scopeType === 'account_authorization'
    ? row.authorization_grantee_system_account_id ?? row.resource_owner_system_account_id
    : row.resource_owner_system_account_id
}

function teamAuthorizationResourceId(row: TeamAuthorizationQuotaRow, scopeType: AuthorizationQuotaScopeType): string | undefined {
  if (scopeType === 'account_authorization') {
    return row.authorization_instance_account_id ?? undefined
  }
  return row.resource_id
}

function assertLocalGatewayDatabaseAccess(operation: string): void {
  if (runtimeConfig.processRole === 'server') {
    throw new Error(`server 角色禁止直接同步读取 SQLite：${operation} 必须通过 DB service`)
  }
}

registerAuthorizationQuotaCacheInvalidator(clearAuthorizationQuotaCache)
