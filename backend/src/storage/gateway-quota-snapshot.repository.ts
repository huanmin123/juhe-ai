import type { DatabaseSync } from 'node:sqlite'

import type {
  GatewayAuthorizationQuotaSnapshotEntry,
  GatewayQuotaCostSnapshotEntry,
  GatewayQuotaSnapshot
} from '../modules/gateway/gateway-quota-snapshot-cache.service.js'
import type { RequestQuotaLimits } from '../domain/types.js'
import { getDatabase, getStatsDatabase, nowIso } from './database.js'
import { hasEnabledRequestQuotaLimit, parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { isRequestQuotaExceeded, loadRequestQuotaCostsBatch, requestQuotaCostKey, type RequestQuotaCostInput } from './request-quota-checker.js'

interface ApiKeyQuotaSnapshotRow {
  id: string
  system_account_id: string
  quota_limits_json: string | null
}

interface AuthorizationQuotaSnapshotRow {
  id: string
  resource_owner_system_account_id: string
  resource_type: 'account' | 'group'
  resource_id: string
  effective_source_team_id: string | null
  limits_json: string | null
}

interface TeamAuthorizationQuotaSnapshotRow {
  authorization_id: string
  resource_owner_system_account_id: string
  resource_type: 'account' | 'group'
  resource_id: string
  effective_source_team_id: string
  limits_json: string | null
}

interface QuotaCostCheck {
  key: string
  limits: RequestQuotaLimits
  costInput: RequestQuotaCostInput
}

export function buildGatewayQuotaSnapshot(now = new Date()): GatewayQuotaSnapshot {
  const businessDatabase = getDatabase()
  const statsDatabase = getStatsDatabase()
  const apiKeys = loadApiKeyQuotaSnapshotRows(businessDatabase)
  const authorizations = loadAuthorizationQuotaSnapshotRows(businessDatabase)
  const teamAuthorizations = loadTeamAuthorizationQuotaSnapshotRows(businessDatabase)

  const apiKeyChecks = apiKeys
    .map((row) => apiKeyQuotaCostCheck(row, now))
    .filter((check): check is QuotaCostCheck & { apiKey: ApiKeyQuotaSnapshotRow } => Boolean(check))
  const authorizationChecksById = new Map<string, QuotaCostCheck[]>()
  for (const row of authorizations) {
    const check = authorizationQuotaCostCheck(row, authorizationScopeType(row.resource_type), now)
    if (check) {
      authorizationChecksById.set(row.id, [...(authorizationChecksById.get(row.id) ?? []), check])
    }
  }
  for (const row of teamAuthorizations) {
    const check = teamAuthorizationQuotaCostCheck(row, authorizationScopeType(row.resource_type), now)
    if (check) {
      authorizationChecksById.set(row.authorization_id, [...(authorizationChecksById.get(row.authorization_id) ?? []), check])
    }
  }

  const allCostChecks = uniqueQuotaCostChecks([
    ...apiKeyChecks,
    ...[...authorizationChecksById.values()].flat()
  ])
  const costsByKey = loadRequestQuotaCostsBatch(statsDatabase, allCostChecks.map((check) => check.costInput))
  const costEntries: GatewayQuotaCostSnapshotEntry[] = apiKeyChecks.map((check) => ({
    systemAccountId: check.costInput.systemAccountId,
    scopeType: check.costInput.scopeType,
    scopeId: check.costInput.scopeId,
    hourlyWindowHours: check.costInput.hourlyWindowHours,
    costs: costsByKey.get(requestQuotaCostKey(check.costInput)) ?? emptyRequestQuotaCosts()
  }))
  const authorizationEntries: GatewayAuthorizationQuotaSnapshotEntry[] = []
  for (const row of authorizations) {
    const checks = authorizationChecksById.get(row.id) ?? []
    if (!checks.length) {
      continue
    }
    const allowed = checks.every((check) => {
      const costs = costsByKey.get(requestQuotaCostKey(check.costInput)) ?? emptyRequestQuotaCosts()
      return !isRequestQuotaExceeded(check.limits, costs)
    })
    authorizationEntries.push({
      scopeType: authorizationScopeType(row.resource_type),
      authorizationId: row.id,
      decision: {
        allowed,
        message: allowed ? undefined : '额度已用完，请联系管理员提升额度'
      }
    })
  }

  return {
    generatedAt: nowIso(),
    costEntries,
    authorizationEntries
  }
}

function loadApiKeyQuotaSnapshotRows(database: DatabaseSync): ApiKeyQuotaSnapshotRow[] {
  return database.prepare(`
    SELECT id, system_account_id, quota_limits_json
    FROM api_keys
    WHERE status = 'active'
      AND quota_limits_json IS NOT NULL
  `).all() as unknown as ApiKeyQuotaSnapshotRow[]
}

function loadAuthorizationQuotaSnapshotRows(database: DatabaseSync): AuthorizationQuotaSnapshotRow[] {
  return database.prepare(`
    SELECT id, resource_owner_system_account_id, resource_type, resource_id, effective_source_team_id, limits_json
    FROM resource_authorizations
    WHERE status = 'active'
  `).all() as unknown as AuthorizationQuotaSnapshotRow[]
}

function loadTeamAuthorizationQuotaSnapshotRows(database: DatabaseSync): TeamAuthorizationQuotaSnapshotRow[] {
  return database.prepare(`
    SELECT
      ra.id AS authorization_id,
      grant_rows.resource_owner_system_account_id,
      grant_rows.resource_type,
      grant_rows.resource_id,
      ra.effective_source_team_id,
      grant_rows.limits_json
    FROM resource_authorizations ra
    INNER JOIN resource_authorization_grants grant_rows
      ON grant_rows.resource_type = ra.resource_type
      AND grant_rows.resource_id = ra.resource_id
      AND grant_rows.grantee_type = 'team'
      AND grant_rows.grantee_team_id = ra.effective_source_team_id
      AND grant_rows.status = 'active'
    WHERE ra.status = 'active'
      AND ra.effective_source_team_id IS NOT NULL
  `).all() as unknown as TeamAuthorizationQuotaSnapshotRow[]
}

function apiKeyQuotaCostCheck(row: ApiKeyQuotaSnapshotRow, now: Date): (QuotaCostCheck & { apiKey: ApiKeyQuotaSnapshotRow }) | undefined {
  const limits = parseRequestQuotaLimitsJson(row.quota_limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) {
    return undefined
  }
  return {
    key: `api_key\u0000${row.system_account_id}\u0000${row.id}\u0000${row.quota_limits_json ?? ''}`,
    limits,
    costInput: {
      systemAccountId: row.system_account_id,
      scopeType: 'api_key',
      scopeId: row.id,
      now,
      hourlyWindowHours: limits.hourly?.hours
    },
    apiKey: row
  }
}

function authorizationQuotaCostCheck(
  row: AuthorizationQuotaSnapshotRow,
  scopeType: 'account_authorization' | 'group_authorization',
  now: Date
): QuotaCostCheck | undefined {
  const limits = parseRequestQuotaLimitsJson(row.limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) {
    return undefined
  }
  return {
    key: `authorization\u0000${row.resource_owner_system_account_id}\u0000${scopeType}\u0000${row.id}\u0000${row.limits_json ?? ''}`,
    limits,
    costInput: {
      systemAccountId: row.resource_owner_system_account_id,
      scopeType,
      scopeId: row.id,
      now,
      hourlyWindowHours: limits.hourly?.hours
    }
  }
}

function teamAuthorizationQuotaCostCheck(
  row: TeamAuthorizationQuotaSnapshotRow,
  scopeType: 'account_authorization' | 'group_authorization',
  now: Date
): QuotaCostCheck | undefined {
  const limits = parseRequestQuotaLimitsJson(row.limits_json)
  if (!hasEnabledRequestQuotaLimit(limits)) {
    return undefined
  }
  return {
    key: `team_authorization\u0000${row.resource_owner_system_account_id}\u0000${scopeType}\u0000${row.effective_source_team_id}\u0000${row.resource_id}\u0000${row.limits_json ?? ''}`,
    limits,
    costInput: {
      systemAccountId: row.resource_owner_system_account_id,
      scopeType: scopeType === 'account_authorization' ? 'account_authorization_team' : 'group_authorization_team',
      scopeId: `${row.resource_id}:${row.effective_source_team_id}`,
      now,
      hourlyWindowHours: limits.hourly?.hours
    }
  }
}

function authorizationScopeType(resourceType: 'account' | 'group'): 'account_authorization' | 'group_authorization' {
  return resourceType === 'account' ? 'account_authorization' : 'group_authorization'
}

function uniqueQuotaCostChecks(checks: QuotaCostCheck[]): QuotaCostCheck[] {
  const seen = new Set<string>()
  const output: QuotaCostCheck[] = []
  for (const check of checks) {
    if (seen.has(check.key)) continue
    seen.add(check.key)
    output.push(check)
  }
  return output
}

function emptyRequestQuotaCosts() {
  return { hourly: 0, daily: 0, weekly: 0, monthly: 0, total: 0 }
}
