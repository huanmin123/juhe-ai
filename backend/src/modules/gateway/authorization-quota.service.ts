import { createAppCache } from '../../shared/cache.js'
import { getDatabase } from '../../storage/database.js'
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

export function checkGatewayAuthorizationQuotaByIds(input: {
  groupAuthorizationId?: string
  accountAuthorizationId?: string
  now?: Date
}): AuthorizationQuotaDecision {
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
      SELECT id, resource_owner_system_account_id, resource_type, limits_json
      FROM team_resource_authorization_grants
      WHERE resource_type = ? AND resource_id = (
          SELECT resource_id FROM resource_authorizations WHERE id = ?
        )
        AND team_id = ?
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
  const costs = loadRequestQuotaCosts(getDatabase(), {
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
  const memberScopeIds = teamAuthorizationMemberScopeIds(row.id, scopeType)
  if (!memberScopeIds.length) return []
  const costs = sumMemberQuotaCosts(row.resource_owner_system_account_id, scopeType, memberScopeIds, now, limits.hourly?.hours)
  return [{
    cacheKey: `team_authorization\u0000${row.resource_owner_system_account_id}\u0000${scopeType}\u0000${teamId}\u0000${row.id}\u0000${row.limits_json ?? ''}`,
    exceeded: isRequestQuotaExceeded(limits, costs)
  }]
}

function teamAuthorizationMemberScopeIds(teamGrantId: string, scopeType: 'account_authorization' | 'group_authorization'): string[] {
  const resourceType = scopeType === 'account_authorization' ? 'account' : 'group'
  const rows = getDatabase().prepare(`
    SELECT ra.id
    FROM team_resource_authorization_grants grant
    INNER JOIN resource_authorizations ra
      ON ra.resource_type = grant.resource_type
      AND ra.resource_id = grant.resource_id
    INNER JOIN resource_authorization_sources source
      ON source.authorization_id = ra.id
      AND source.source_type = 'team'
      AND source.source_team_id = grant.team_id
      AND source.status = 'active'
    WHERE grant.id = ?
      AND grant.resource_type = ?
      AND grant.status = 'active'
      AND ra.status = 'active'
  `).all(teamGrantId, resourceType) as unknown as Array<{ id?: string }>
  return rows.map((row) => row.id).filter((id): id is string => Boolean(id))
}

function sumMemberQuotaCosts(systemAccountId: string, scopeType: string, scopeIds: string[], now: Date, hourlyWindowHours?: number) {
  return scopeIds.reduce((summary, scopeId) => {
    const costs = loadRequestQuotaCosts(getDatabase(), {
      systemAccountId,
      scopeType,
      scopeId,
      now,
      hourlyWindowHours
    })
    return {
      hourly: summary.hourly + costs.hourly,
      daily: summary.daily + costs.daily,
      weekly: summary.weekly + costs.weekly,
      monthly: summary.monthly + costs.monthly,
      total: summary.total + costs.total
    }
  }, { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 })
}
