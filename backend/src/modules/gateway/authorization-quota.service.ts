import { createAppCache } from '../../shared/cache.js'
import { runtimeConfig } from '../../config/runtime.js'
import { getDatabase, getRecordDatabase } from '../../storage/database.js'
import type { GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../storage/repositories.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from '../../storage/request-quota-limits.js'
import { requestDbService } from '../db-service/db-service-ipc.js'
import { isRequestQuotaExceeded, loadRequestQuotaCosts } from './request-quota-checker.js'

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
  assertLocalGatewayDatabaseAccess('checkGatewayAuthorizationQuota')
  const now = input.now ?? new Date()
  const checks: AuthorizationQuotaCheck[] = []
  if (input.groupAccess.groupAuthorizationId) {
    checks.push(...authorizationQuotaChecks(input.groupAccess.groupAuthorizationId, 'group_authorization', now))
  }
  if (input.account?.accountAuthorizationId) {
    checks.push(...authorizationQuotaChecks(input.account.accountAuthorizationId, 'account_authorization', now))
  }
  if (!checks.length) {
    return { allowed: true }
  }
  return authorizationQuotaDecisionFromChecks(checks)
}

export async function checkGatewayAuthorizationQuotaAsync(input: {
  groupAccess: GroupUsageAccessMetadata
  account?: OpenAIAccountSecret
}): Promise<AuthorizationQuotaDecision> {
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
  const decisions = await requestDbService({
    type: 'check_authorization_quota_batch',
    groupAuthorizationId: input.groupAccess.groupAuthorizationId,
    accounts: input.accounts.map((account) => ({
      accountId: account.id,
      accountAuthorizationId: account.accountAuthorizationId
    }))
  })
  const output = new Map<string, AuthorizationQuotaDecision>()
  input.accounts.forEach((account, index) => {
    output.set(account.id, decisions[index] ?? { allowed: true })
  })
  return output
}

export function checkGatewayAuthorizationQuotaByIds(input: {
  groupAuthorizationId?: string
  accountAuthorizationId?: string
  now?: Date
}): AuthorizationQuotaDecision {
  assertLocalGatewayDatabaseAccess('checkGatewayAuthorizationQuotaByIds')
  const now = input.now ?? new Date()
  const checks: AuthorizationQuotaCheck[] = []
  if (input.groupAuthorizationId) {
    checks.push(...authorizationQuotaChecks(input.groupAuthorizationId, 'group_authorization', now))
  }
  if (input.accountAuthorizationId) {
    checks.push(...authorizationQuotaChecks(input.accountAuthorizationId, 'account_authorization', now))
  }
  if (!checks.length) {
    return { allowed: true }
  }
  return authorizationQuotaDecisionFromChecks(checks)
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

function authorizationQuotaChecks(authorizationId: string, scopeType: 'account_authorization' | 'group_authorization', now: Date): AuthorizationQuotaCheck[] {
  const database = getDatabase()
  const row = database.prepare(`
    SELECT id, resource_owner_system_account_id, resource_type, effective_source_team_id, limits_json
    FROM resource_authorizations
    WHERE id = ? AND status = 'active'
  `).get(authorizationId) as unknown as AuthorizationQuotaRow | undefined
  if (!row) return []

  const checks = quotaCheckForAuthorizationRow(row, scopeType, now)
  if (row.effective_source_team_id) {
    const teamRow = database.prepare(`
      SELECT id, resource_owner_system_account_id, resource_type, resource_id, limits_json
      FROM resource_authorization_grants
      WHERE resource_type = ? AND resource_id = (
          SELECT resource_id FROM resource_authorizations WHERE id = ?
        )
        AND grantee_type = 'team'
        AND grantee_team_id = ?
        AND status = 'active'
      LIMIT 1
    `).get(row.resource_type, authorizationId, row.effective_source_team_id) as unknown as TeamAuthorizationQuotaRow | undefined
    if (teamRow) {
      checks.push(...quotaCheckForTeamRow(teamRow, scopeType, row.effective_source_team_id, now))
    }
  }
  return checks
}

function quotaCheckForAuthorizationRow(row: AuthorizationQuotaRow, scopeType: 'account_authorization' | 'group_authorization', now: Date): AuthorizationQuotaCheck[] {
  const limits = parseRequestQuotaLimitsJson(row.limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) return []
  const costs = loadRequestQuotaCosts(getRecordDatabase(), {
    systemAccountId: row.resource_owner_system_account_id,
    scopeType,
    scopeId: row.id,
    now,
    hourlyWindowHours: limits.hourly?.hours
  })
  return [{
    cacheKey: `authorization\u0000${row.resource_owner_system_account_id}\u0000${scopeType}\u0000${row.id}\u0000${row.limits_json ?? ''}`,
    exceeded: isRequestQuotaExceeded(limits, costs)
  }]
}

function quotaCheckForTeamRow(row: TeamAuthorizationQuotaRow, scopeType: 'account_authorization' | 'group_authorization', teamId: string, now: Date): AuthorizationQuotaCheck[] {
  const limits = parseRequestQuotaLimitsJson(row.limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) return []
  const costs = loadRequestQuotaCosts(getRecordDatabase(), {
    systemAccountId: row.resource_owner_system_account_id,
    scopeType: teamAuthorizationScopeType(scopeType),
    scopeId: `${teamAuthorizationResourceId(row)}:${teamId}`,
    now,
    hourlyWindowHours: limits.hourly?.hours
  })
  return [{
    cacheKey: `team_authorization\u0000${row.resource_owner_system_account_id}\u0000${scopeType}\u0000${teamId}\u0000${row.id}\u0000${row.limits_json ?? ''}`,
    exceeded: isRequestQuotaExceeded(limits, costs)
  }]
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
