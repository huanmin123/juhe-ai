import type { DatabaseSync } from 'node:sqlite'

import { getBusinessDatabase, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { maxRequestQuotaHourlyWindowHours, parseRequestQuotaLimitsJson } from './request-quota-limits.js'

const businessSchemaName = 'juhe_business'
const statsSchemaName = 'juhe_stats'

export type RequestQuotaHourlyWindowScopeType =
  | 'api_key'
  | 'account_authorization'
  | 'group_authorization'
  | 'account_authorization_team'
  | 'group_authorization_team'

export interface RequestQuotaHourlyWindowScopeBinding {
  systemAccountId: string
  scopeType: RequestQuotaHourlyWindowScopeType
  scopeId: string
  windowHours: number
}

export interface ResourceAuthorizationQuotaBindingGrant {
  id: string
  resourceType: 'account' | 'group'
  resourceId: string
  resourceOwnerSystemAccountId: string
  granteeType: 'system_account' | 'team'
  granteeSystemAccountId?: string | null
  granteeTeamId?: string | null
}

interface StoredScopeBinding extends RequestQuotaHourlyWindowScopeBinding {
  sourceType: 'api_key' | 'resource_authorization_grant'
  sourceId: string
}

interface AuthorizationBindingRow {
  id: string
  resource_type: 'account' | 'group'
  resource_id: string
  resource_owner_system_account_id: string
  grantee_system_account_id: string
  status: string
  limits_json?: string | null
  effective_source_type?: 'manual' | 'team' | null
  effective_source_team_id?: string | null
  effective_grant_id?: string | null
  authorization_instance_account_id?: string | null
}

export function syncApiKeyRequestQuotaHourlyWindowScopeBinding(
  input: {
    apiKeyId: string
    systemAccountId: string
    limitsJson: string | null | undefined
    active: boolean
  },
  database: DatabaseSync = getBusinessDatabase(),
  timestamp: string = nowIso()
): void {
  database.prepare(`
    DELETE FROM request_quota_hourly_window_scope_bindings
    WHERE source_type = 'api_key' AND source_id = ?
  `).run(input.apiKeyId)
  const windowHours = activeRequestQuotaHourlyWindowHours(input.limitsJson, input.active)
  if (windowHours === undefined) return
  insertSqliteScopeBindings(database, [{
    systemAccountId: input.systemAccountId,
    scopeType: 'api_key',
    scopeId: input.apiKeyId,
    sourceType: 'api_key',
    sourceId: input.apiKeyId,
    windowHours
  }], timestamp)
}

export async function syncApiKeyRequestQuotaHourlyWindowScopeBindingAsync(
  client: DatabaseClient,
  input: {
    apiKeyId: string
    systemAccountId: string
    limitsJson: string | null | undefined
    active: boolean
  },
  timestamp: string = nowIso()
): Promise<void> {
  if (client.driver !== 'postgres') return
  await client.execute(`
    DELETE FROM ${businessTable(client, 'request_quota_hourly_window_scope_bindings')}
    WHERE source_type = 'api_key' AND source_id = ?
  `, [input.apiKeyId])
  const windowHours = activeRequestQuotaHourlyWindowHours(input.limitsJson, input.active)
  if (windowHours === undefined) return
  const binding: StoredScopeBinding = {
    systemAccountId: input.systemAccountId,
    scopeType: 'api_key',
    scopeId: input.apiKeyId,
    sourceType: 'api_key',
    sourceId: input.apiKeyId,
    windowHours
  }
  await insertPostgresScopeBindings(client, [binding], timestamp)
  await markPostgresRequestQuotaHourlyWindowDirtyScopes(client, [binding], timestamp)
}

export function syncResourceAuthorizationRequestQuotaHourlyWindowScopeBindings(
  grant: ResourceAuthorizationQuotaBindingGrant,
  database: DatabaseSync = getBusinessDatabase(),
  timestamp: string = nowIso()
): void {
  const previousAuthorizationIds = previousAuthorizationScopeIdsForSource(database, grant.id)
  database.prepare(`
    DELETE FROM request_quota_hourly_window_scope_bindings
    WHERE source_type = 'resource_authorization_grant' AND source_id = ?
  `).run(grant.id)
  const rows = loadAffectedAuthorizationBindingRows(database, grant, previousAuthorizationIds)
  insertSqliteScopeBindings(database, buildAuthorizationScopeBindings(rows), timestamp)
}

export async function syncResourceAuthorizationRequestQuotaHourlyWindowScopeBindingsAsync(
  client: DatabaseClient,
  grant: ResourceAuthorizationQuotaBindingGrant,
  timestamp: string = nowIso()
): Promise<void> {
  if (client.driver !== 'postgres') return
  const previousRows = await client.query<{ scope_id: string; scope_type: string }>(`
    SELECT scope_id, scope_type
    FROM ${businessTable(client, 'request_quota_hourly_window_scope_bindings')}
    WHERE source_type = 'resource_authorization_grant' AND source_id = ?
  `, [grant.id])
  const previousAuthorizationIds = previousRows
    .filter((row) => row.scope_type === 'account_authorization' || row.scope_type === 'group_authorization')
    .map((row) => row.scope_id)
  await client.execute(`
    DELETE FROM ${businessTable(client, 'request_quota_hourly_window_scope_bindings')}
    WHERE source_type = 'resource_authorization_grant' AND source_id = ?
  `, [grant.id])
  const rows = await loadAffectedAuthorizationBindingRowsAsync(client, grant, previousAuthorizationIds)
  const bindings = buildAuthorizationScopeBindings(rows)
  await insertPostgresScopeBindings(client, bindings, timestamp)
  await markPostgresRequestQuotaHourlyWindowDirtyScopes(client, bindings, timestamp)
}

export function listRequestQuotaHourlyWindowScopeBindings(
  database: DatabaseSync = getBusinessDatabase()
): RequestQuotaHourlyWindowScopeBinding[] {
  const rows = database.prepare(`
    SELECT system_account_id, scope_type, scope_id, window_hours
    FROM request_quota_hourly_window_scope_bindings
    ORDER BY window_hours ASC, system_account_id ASC, scope_type ASC, scope_id ASC
  `).all() as unknown as Array<{
    system_account_id: string
    scope_type: RequestQuotaHourlyWindowScopeType
    scope_id: string
    window_hours: number
  }>
  return rows
    .filter((row) => isValidRequestQuotaHourlyWindowHours(row.window_hours))
    .map((row) => ({
      systemAccountId: row.system_account_id,
      scopeType: row.scope_type,
      scopeId: row.scope_id,
      windowHours: row.window_hours
    }))
}

function previousAuthorizationScopeIdsForSource(database: DatabaseSync, grantId: string): string[] {
  return (database.prepare(`
    SELECT scope_id
    FROM request_quota_hourly_window_scope_bindings
    WHERE source_type = 'resource_authorization_grant'
      AND source_id = ?
      AND scope_type IN ('account_authorization', 'group_authorization')
    ORDER BY scope_id ASC
  `).all(grantId) as unknown as Array<{ scope_id: string }>).map((row) => row.scope_id)
}

function loadAffectedAuthorizationBindingRows(
  database: DatabaseSync,
  grant: ResourceAuthorizationQuotaBindingGrant,
  previousAuthorizationIds: string[]
): AuthorizationBindingRow[] {
  const previousClause = previousAuthorizationIds.length > 0
    ? `OR ra.id IN (${previousAuthorizationIds.map(() => '?').join(', ')})`
    : ''
  return database.prepare(`
    SELECT ra.id, ra.resource_type, ra.resource_id, ra.resource_owner_system_account_id,
      ra.grantee_system_account_id, ra.status, ra.limits_json, ra.effective_source_type,
      ra.effective_source_team_id, effective_grant.id AS effective_grant_id,
      instance_accounts.id AS authorization_instance_account_id
    FROM resource_authorizations ra
    LEFT JOIN resource_authorization_grants effective_grant
      ON effective_grant.resource_type = ra.resource_type
      AND effective_grant.resource_id = ra.resource_id
      AND effective_grant.status = 'active'
      AND (
        (ra.effective_source_type = 'manual'
          AND effective_grant.grantee_type = 'system_account'
          AND effective_grant.grantee_system_account_id = ra.grantee_system_account_id)
        OR
        (ra.effective_source_type = 'team'
          AND effective_grant.grantee_type = 'team'
          AND effective_grant.grantee_team_id = ra.effective_source_team_id)
      )
    LEFT JOIN accounts instance_accounts
      ON ra.resource_type = 'account'
      AND instance_accounts.authorization_instance_authorization_id = ra.id
      AND instance_accounts.system_account_id = ra.grantee_system_account_id
      AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
      AND instance_accounts.deleted_at IS NULL
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND (
        (? = 'system_account' AND ra.grantee_system_account_id = ?)
        OR
        (? = 'team' AND EXISTS (
          SELECT 1 FROM resource_authorization_sources ras
          WHERE ras.authorization_id = ra.id
            AND ras.source_type = 'team'
            AND ras.source_team_id = ?
        ))
        ${previousClause}
      )
    ORDER BY ra.id ASC
  `).all(
    grant.resourceType,
    grant.resourceId,
    grant.granteeType,
    grant.granteeSystemAccountId ?? '',
    grant.granteeType,
    grant.granteeTeamId ?? '',
    ...previousAuthorizationIds
  ) as unknown as AuthorizationBindingRow[]
}

async function loadAffectedAuthorizationBindingRowsAsync(
  client: DatabaseClient,
  grant: ResourceAuthorizationQuotaBindingGrant,
  previousAuthorizationIds: string[]
): Promise<AuthorizationBindingRow[]> {
  const previousClause = previousAuthorizationIds.length > 0 ? 'OR ra.id = ANY(?::text[])' : ''
  return client.query<AuthorizationBindingRow>(`
    SELECT ra.id, ra.resource_type, ra.resource_id, ra.resource_owner_system_account_id,
      ra.grantee_system_account_id, ra.status, ra.limits_json, ra.effective_source_type,
      ra.effective_source_team_id, effective_grant.id AS effective_grant_id,
      instance_accounts.id AS authorization_instance_account_id
    FROM ${businessTable(client, 'resource_authorizations')} ra
    LEFT JOIN ${businessTable(client, 'resource_authorization_grants')} effective_grant
      ON effective_grant.resource_type = ra.resource_type
      AND effective_grant.resource_id = ra.resource_id
      AND effective_grant.status = 'active'
      AND (
        (ra.effective_source_type = 'manual'
          AND effective_grant.grantee_type = 'system_account'
          AND effective_grant.grantee_system_account_id = ra.grantee_system_account_id)
        OR
        (ra.effective_source_type = 'team'
          AND effective_grant.grantee_type = 'team'
          AND effective_grant.grantee_team_id = ra.effective_source_team_id)
      )
    LEFT JOIN ${businessTable(client, 'accounts')} instance_accounts
      ON ra.resource_type = 'account'
      AND instance_accounts.authorization_instance_authorization_id = ra.id
      AND instance_accounts.system_account_id = ra.grantee_system_account_id
      AND instance_accounts.authorization_instance_source_account_id = ra.resource_id
      AND instance_accounts.deleted_at IS NULL
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND (
        (? = 'system_account' AND ra.grantee_system_account_id = ?)
        OR
        (? = 'team' AND EXISTS (
          SELECT 1 FROM ${businessTable(client, 'resource_authorization_sources')} ras
          WHERE ras.authorization_id = ra.id
            AND ras.source_type = 'team'
            AND ras.source_team_id = ?
        ))
        ${previousClause}
      )
    ORDER BY ra.id ASC
  `, [
    grant.resourceType,
    grant.resourceId,
    grant.granteeType,
    grant.granteeSystemAccountId ?? '',
    grant.granteeType,
    grant.granteeTeamId ?? '',
    ...(previousAuthorizationIds.length > 0 ? [previousAuthorizationIds] : [])
  ])
}

function buildAuthorizationScopeBindings(rows: AuthorizationBindingRow[]): StoredScopeBinding[] {
  const bindings: StoredScopeBinding[] = []
  for (const row of rows) {
    if (row.status !== 'active' || !row.effective_grant_id) continue
    const windowHours = activeRequestQuotaHourlyWindowHours(row.limits_json, true)
    if (windowHours === undefined) continue
    bindings.push({
      systemAccountId: row.resource_type === 'account'
        ? row.grantee_system_account_id
        : row.resource_owner_system_account_id,
      scopeType: row.resource_type === 'account' ? 'account_authorization' : 'group_authorization',
      scopeId: row.id,
      sourceType: 'resource_authorization_grant',
      sourceId: row.effective_grant_id,
      windowHours
    })
    if (row.effective_source_type !== 'team' || !row.effective_source_team_id) continue
    if (row.resource_type === 'account') {
      if (!row.authorization_instance_account_id) continue
      bindings.push({
        systemAccountId: row.grantee_system_account_id,
        scopeType: 'account_authorization_team',
        scopeId: `${row.authorization_instance_account_id}:${row.effective_source_team_id}`,
        sourceType: 'resource_authorization_grant',
        sourceId: row.effective_grant_id,
        windowHours
      })
    } else {
      bindings.push({
        systemAccountId: row.resource_owner_system_account_id,
        scopeType: 'group_authorization_team',
        scopeId: `${row.resource_id}:${row.effective_source_team_id}`,
        sourceType: 'resource_authorization_grant',
        sourceId: row.effective_grant_id,
        windowHours
      })
    }
  }
  return uniqueScopeBindings(bindings)
}

function insertSqliteScopeBindings(database: DatabaseSync, bindings: StoredScopeBinding[], timestamp: string): void {
  const insert = database.prepare(`
    INSERT INTO request_quota_hourly_window_scope_bindings (
      system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      source_type = excluded.source_type,
      source_id = excluded.source_id,
      window_hours = excluded.window_hours,
      updated_at = excluded.updated_at
  `)
  for (const binding of bindings) {
    insert.run(
      binding.systemAccountId,
      binding.scopeType,
      binding.scopeId,
      binding.sourceType,
      binding.sourceId,
      binding.windowHours,
      timestamp,
      timestamp
    )
  }
}

async function insertPostgresScopeBindings(client: DatabaseClient, bindings: StoredScopeBinding[], timestamp: string): Promise<void> {
  if (bindings.length === 0) return
  const values = bindings.map(() => '(?, ?, ?, ?, ?, ?, ?, ?)').join(', ')
  await client.execute(`
    INSERT INTO ${businessTable(client, 'request_quota_hourly_window_scope_bindings')} (
      system_account_id, scope_type, scope_id, source_type, source_id, window_hours, created_at, updated_at
    ) VALUES ${values}
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      source_type = EXCLUDED.source_type,
      source_id = EXCLUDED.source_id,
      window_hours = EXCLUDED.window_hours,
      updated_at = EXCLUDED.updated_at
  `, bindings.flatMap((binding) => [
    binding.systemAccountId,
    binding.scopeType,
    binding.scopeId,
    binding.sourceType,
    binding.sourceId,
    binding.windowHours,
    timestamp,
    timestamp
  ]))
}

async function markPostgresRequestQuotaHourlyWindowDirtyScopes(
  client: DatabaseClient,
  bindings: RequestQuotaHourlyWindowScopeBinding[],
  timestamp: string
): Promise<void> {
  if (bindings.length === 0) return
  const values = bindings.map(() => '(?, ?, ?, 1, ?, ?)').join(', ')
  await client.execute(`
    INSERT INTO ${statsTable(client, 'usage_quota_hourly_window_dirty_scopes')} (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    ) VALUES ${values}
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `, bindings.flatMap((binding) => [
    binding.systemAccountId,
    binding.scopeType,
    binding.scopeId,
    timestamp,
    timestamp
  ]))
}

function uniqueScopeBindings(bindings: StoredScopeBinding[]): StoredScopeBinding[] {
  const byScope = new Map<string, StoredScopeBinding>()
  for (const binding of bindings) {
    byScope.set(`${binding.systemAccountId}\u0000${binding.scopeType}\u0000${binding.scopeId}`, binding)
  }
  return [...byScope.values()]
}

function activeRequestQuotaHourlyWindowHours(limitsJson: string | null | undefined, active: boolean): number | undefined {
  if (!active) return undefined
  const limits = parseRequestQuotaLimitsJson(limitsJson)
  const hours = limits.hourly?.enabled ? limits.hourly.hours : undefined
  return isValidRequestQuotaHourlyWindowHours(hours) ? hours : undefined
}

function isValidRequestQuotaHourlyWindowHours(value: unknown): value is number {
  return Number.isInteger(value) && typeof value === 'number' && value >= 1 && value <= maxRequestQuotaHourlyWindowHours
}

function businessTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(businessSchemaName, tableName)
}

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable(statsSchemaName, tableName)
}
