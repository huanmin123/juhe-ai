import type { DatabaseSync } from 'node:sqlite'

import type {
  AuthorizationStatus,
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceStatus,
  ResourceAuthorizationSourceType
} from '../domain/types.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, newId, nowIso } from './database.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { clearResourceAuthorizationLookupCaches } from './authorization-read-loaders.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { loadRuntimeAuthorizationForUserGrant } from './resource-authorization-read.repository.js'
import { normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import type {
  ResourceAuthorizationGrantRow,
  ResourceAuthorizationRow,
  ResourceAuthorizationSourceRow,
  SystemTeamMemberRow
} from './repository-row-types.js'
import { markAllGroupAccountStatsDirty } from './usage-stats.repository.js'
import { jsonObjectOrNull, parseOptionalJsonObject } from './value-utils.js'

interface RefreshEffectiveSourceOptions {
  noActiveSourceReason?: string
  preserveExpiredWhenNoActiveSource?: boolean
  terminalStatus?: 'revoked' | 'returned'
}

export function expireDueResourceAuthorizations(): number {
  const now = nowIso()
  const database = getDatabase()
  const dueGrants = database
    .prepare("SELECT * FROM resource_authorization_grants WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL AND expires_at <= ?")
    .all(now) as unknown as ResourceAuthorizationGrantRow[]
  const grantResult = database
    .prepare(`
      UPDATE resource_authorization_grants
      SET status = 'expired',
          revoked_at = COALESCE(revoked_at, ?),
          updated_at = ?
      WHERE status IN ('active', 'paused')
        AND expires_at IS NOT NULL
        AND expires_at <= ?
    `)
    .run(now, now, now)
  for (const grant of dueGrants) {
    syncResourceAuthorizationGrantRuntime({ ...grant, status: 'expired', revoked_at: grant.revoked_at ?? now, updated_at: now }, grant.revoked_by ?? grant.created_by, database, now)
  }
  const changed = Number(grantResult.changes ?? 0)
  if (changed > 0) {
    cleanupInactiveAuthorizationBindings(database)
    invalidateAuthorizationLookupCaches()
    markAllGroupAccountStatsDirty('authorization_expired')
    notifyGatewayRuntimeCacheInvalidation('authorization_expired')
    notifyAuthorizationQuotaCacheInvalidation('authorization_expired')
  }
  return changed
}

export function activeTeamMemberRows(teamId: string, database = getDatabase()): SystemTeamMemberRow[] {
  return database.prepare(`
    SELECT system_team_members.*
    FROM system_team_members
    INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id
    WHERE system_team_members.team_id = ?
      AND system_team_members.status = 'active'
      AND system_accounts.status = 'active'
    ORDER BY system_team_members.joined_at ASC, system_team_members.id ASC
  `).all(teamId) as unknown as SystemTeamMemberRow[]
}

export function upsertResourceAuthorizationForUser(input: { resourceType: ResourceAuthorizationResourceType; resourceId: string; ownerSystemAccountId: string; granteeSystemAccountId: string; sourceType: ResourceAuthorizationSourceType; sourceTeamId?: string; targetGroupId?: string; remark?: string; expiresAt?: string | null; limits?: unknown; modelPolicy?: unknown; actor: string; now: string; database: DatabaseSync }): ResourceAuthorizationRow {
  if (input.granteeSystemAccountId === input.ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
  const existing = input.database.prepare('SELECT * FROM resource_authorizations WHERE resource_type = ? AND resource_id = ? AND grantee_system_account_id = ? LIMIT 1').get(input.resourceType, input.resourceId, input.granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  const authorizationId = existing?.id ?? newId('rauth')
  const isTeamSource = input.sourceType === 'team'
  const hasActiveTeamSource = existing ? hasActiveTeamAuthorizationSource(input.database, authorizationId) : false
  const nextEffectiveSourceType = isTeamSource || hasActiveTeamSource ? 'team' : 'manual'
  const nextEffectiveSourceTeamId = isTeamSource ? input.sourceTeamId ?? null : firstActiveTeamSourceId(input.database, authorizationId)
  const nextExpiresAt = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
    ? input.expiresAt ?? null
    : existing?.expires_at ?? null
  const existingStatus = existing?.status
  const nextStatus: AuthorizationStatus = isResourceAuthorizationExpired(nextExpiresAt)
    ? 'expired'
    : existingStatus === 'paused' && !isTeamSource
      ? 'paused'
      : 'active'
  const nextLimitsJson = !isTeamSource && hasActiveTeamSource
    ? existing?.limits_json ?? null
    : requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
  const nextRevokedBy = nextStatus === 'expired' ? existing?.revoked_by ?? input.actor : null
  const nextRevokedAt = nextStatus === 'expired' ? existing?.revoked_at ?? input.now : null
  const nextRevokedReason = nextStatus === 'expired' ? 'authorization_expired' : null
  if (existing) {
    input.database.prepare(`
      UPDATE resource_authorizations
      SET resource_owner_system_account_id = ?,
          status = ?,
          effective_source_type = COALESCE(?, effective_source_type),
          effective_source_team_id = ?,
          activated_at = COALESCE(activated_at, ?),
          last_source_changed_at = ?,
          remark = COALESCE(?, remark),
          expires_at = ?,
          limits_json = ?,
          model_policy_json = COALESCE(?, model_policy_json),
          revoked_by = ?,
          revoked_at = ?,
          revoked_reason = ?,
          updated_at = ?
      WHERE id = ?
    `).run(input.ownerSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, nextExpiresAt, nextLimitsJson, jsonObjectOrNull(input.modelPolicy), nextRevokedBy, nextRevokedAt, nextRevokedReason, input.now, authorizationId)
  } else {
    input.database.prepare(`
      INSERT INTO resource_authorizations (
        id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
        effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
        remark, expires_at, limits_json, model_policy_json,
        created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'use', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(authorizationId, input.resourceType, input.resourceId, input.ownerSystemAccountId, input.granteeSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, nextExpiresAt, nextLimitsJson, jsonObjectOrNull(input.modelPolicy), input.actor, input.now, nextRevokedBy, nextRevokedAt, nextRevokedReason, input.now)
  }
  upsertResourceAuthorizationSource(input.database, authorizationId, input.sourceType, input.sourceTeamId, input.actor, input.now, isTeamSource ? 'active' : hasActiveTeamSource ? 'superseded' : 'active')
  if (isTeamSource) {
    input.database.prepare(`
      UPDATE resource_authorization_sources
      SET status = 'superseded',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'covered_by_team'),
          updated_at = ?
      WHERE authorization_id = ? AND source_type = 'manual' AND status = 'active'
    `).run(input.now, input.now, authorizationId)
  }
  refreshResourceAuthorizationEffectiveSource(authorizationId, input.actor, input.now, input.database)
  const row = input.database.prepare('SELECT * FROM resource_authorizations WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationRow | undefined
  if (!row) throw new Error('创建资源授权失败')
  bindActiveAccountAuthorizationToGranteeGroup(input.database, row, input.now, input.targetGroupId)
  return row
}

function bindActiveAccountAuthorizationToGranteeGroup(database: DatabaseSync, authorization: ResourceAuthorizationRow, now: string, targetGroupId?: string): void {
  if (authorization.resource_type !== 'account') return
  if (authorization.status !== 'active' || isResourceAuthorizationExpired(authorization.expires_at, Date.parse(now))) return
  const account = database
    .prepare('SELECT provider_code, system_account_id FROM accounts WHERE id = ?')
    .get(authorization.resource_id) as unknown as { provider_code?: string; system_account_id?: string } | undefined
  if (!account?.provider_code || account.system_account_id === authorization.grantee_system_account_id) return
  const requestedGroupId = targetGroupId?.trim()
  const existingBinding = database
    .prepare(`
      SELECT group_id
      FROM group_accounts
      WHERE account_id = ?
        AND system_account_id = ?
        AND enabled = 1
      ORDER BY updated_at DESC, group_id ASC, account_id ASC
      LIMIT 1
    `)
    .get(authorization.resource_id, authorization.grantee_system_account_id) as unknown as { group_id?: string } | undefined
  if (existingBinding?.group_id && (!requestedGroupId || existingBinding.group_id === requestedGroupId)) return
  const bindGroupId = groupIdForAuthorizationBinding(database, account.provider_code, authorization.grantee_system_account_id, now, requestedGroupId)
  if (!bindGroupId) return
  if (existingBinding?.group_id && existingBinding.group_id !== bindGroupId) {
    database
      .prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ?')
      .run(authorization.resource_id, authorization.grantee_system_account_id)
    invalidateGroupAccountIdsCache(existingBinding.group_id)
  }
  database
    .prepare(`
      INSERT INTO group_accounts (system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at)
      VALUES (?, ?, ?, ?, 1, ?, ?)
      ON CONFLICT(group_id, account_id) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        account_authorization_id = excluded.account_authorization_id,
        enabled = 1,
        updated_at = excluded.updated_at
    `)
    .run(authorization.grantee_system_account_id, bindGroupId, authorization.resource_id, authorization.id, now, now)
  invalidateGroupAccountIdsCache(bindGroupId)
}

function groupIdForAuthorizationBinding(database: DatabaseSync, providerCode: string, systemAccountId: string, now: string, targetGroupId?: string): string | undefined {
  if (targetGroupId) {
    const group = database
      .prepare('SELECT id, system_account_id, provider_code, enabled FROM groups WHERE id = ? LIMIT 1')
      .get(targetGroupId) as unknown as { id?: string; system_account_id?: string; provider_code?: string; enabled?: number } | undefined
    if (!group?.id || group.system_account_id !== systemAccountId) {
      throw new Error('目标分组不存在或不属于被授权用户')
    }
    if (group.provider_code !== providerCode) {
      throw new Error('目标分组供应商与授权账户不一致')
    }
    if (group.enabled !== 1) {
      throw new Error('目标分组已停用，请选择启用分组')
    }
    return group.id
  }
  return defaultGroupIdForAuthorizationBinding(database, providerCode, systemAccountId, now)
}

function defaultGroupIdForAuthorizationBinding(database: DatabaseSync, providerCode: string, systemAccountId: string, now: string): string | undefined {
  const existing = database
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? ORDER BY is_default DESC, updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, providerCode) as unknown as { id?: string } | undefined
  if (existing?.id) return existing.id
  if (providerCode !== 'openai') return undefined
  const id = newId('grp')
  database
    .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)')
    .run(id, systemAccountId, '默认 OpenAI 分组', 'openai', '', now, now)
  return id
}

function hasActiveTeamAuthorizationSource(database: DatabaseSync, authorizationId: string): boolean {
  const row = database
    .prepare(`
      SELECT ras.id
      FROM resource_authorization_sources ras
      INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
      INNER JOIN resource_authorization_grants trg
        ON trg.resource_type = ra.resource_type
        AND trg.resource_id = ra.resource_id
        AND trg.grantee_type = 'team'
        AND trg.grantee_team_id = ras.source_team_id
        AND trg.status = 'active'
        AND (trg.expires_at IS NULL OR trg.expires_at > ?)
      WHERE ras.authorization_id = ?
        AND ras.source_type = 'team'
        AND ras.status = 'active'
      LIMIT 1
    `)
    .get(nowIso(), authorizationId) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

function firstActiveTeamSourceId(database: DatabaseSync, authorizationId: string): string | null {
  const row = database
    .prepare(`
      SELECT ras.source_team_id
      FROM resource_authorization_sources ras
      INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
      INNER JOIN resource_authorization_grants trg
        ON trg.resource_type = ra.resource_type
        AND trg.resource_id = ra.resource_id
        AND trg.grantee_type = 'team'
        AND trg.grantee_team_id = ras.source_team_id
        AND trg.status = 'active'
        AND (trg.expires_at IS NULL OR trg.expires_at > ?)
      WHERE ras.authorization_id = ?
        AND ras.source_type = 'team'
        AND ras.status = 'active'
      ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
      LIMIT 1
    `)
    .get(nowIso(), authorizationId) as unknown as { source_team_id?: string | null } | undefined
  return row?.source_team_id ?? null
}

function upsertResourceAuthorizationSource(database: DatabaseSync, authorizationId: string, sourceType: ResourceAuthorizationSourceType, sourceTeamId: string | undefined, actor: string, now: string, requestedStatus: ResourceAuthorizationSourceStatus): void {
  const existing = database.prepare("SELECT * FROM resource_authorization_sources WHERE authorization_id = ? AND source_type = ? AND COALESCE(source_team_id, '') = COALESCE(?, '') ORDER BY created_at DESC, id DESC LIMIT 1").get(authorizationId, sourceType, sourceTeamId ?? null) as unknown as ResourceAuthorizationSourceRow | undefined
  if (existing) {
    database.prepare(`
      UPDATE resource_authorization_sources
      SET status = ?,
          activated_at = COALESCE(activated_at, ?),
          ended_at = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_at, ?) END,
          ended_reason = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_reason, ?) END,
          revoked_by = CASE WHEN ? = 'active' THEN NULL ELSE revoked_by END,
          revoked_at = CASE WHEN ? = 'active' THEN NULL ELSE revoked_at END,
          updated_at = ?
      WHERE id = ?
    `).run(requestedStatus, now, requestedStatus, now, requestedStatus, requestedStatus === 'superseded' ? 'covered_by_team' : null, requestedStatus, requestedStatus, now, existing.id)
    return
  }
  database.prepare(`
    INSERT INTO resource_authorization_sources (
      id, authorization_id, source_type, source_team_id, status, activated_at, ended_at, ended_reason,
      created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?)
  `).run(newId('rauthsrc'), authorizationId, sourceType, sourceTeamId ?? null, requestedStatus, now, requestedStatus === 'active' ? null : now, requestedStatus === 'superseded' ? 'covered_by_team' : null, actor, now, now)
}

function refreshResourceAuthorizationEffectiveSource(
  authorizationId: string,
  actor: string,
  now: string,
  database = getDatabase(),
  options: RefreshEffectiveSourceOptions = {}
): void {
  invalidateAuthorizationLookupCaches()
  const activeTeamSource = database.prepare(`
    SELECT ras.source_team_id
    FROM resource_authorization_sources ras
    INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
    INNER JOIN resource_authorization_grants trg
      ON trg.resource_type = ra.resource_type
      AND trg.resource_id = ra.resource_id
      AND trg.grantee_type = 'team'
      AND trg.grantee_team_id = ras.source_team_id
      AND trg.status = 'active'
      AND (trg.expires_at IS NULL OR trg.expires_at > ?)
    WHERE ras.authorization_id = ?
      AND ras.source_type = 'team'
      AND ras.status = 'active'
    ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
    LIMIT 1
  `).get(now, authorizationId) as unknown as { source_team_id?: string | null } | undefined

  if (activeTeamSource?.source_team_id) {
    database.prepare(`
      UPDATE resource_authorizations
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            WHEN status = 'paused' THEN 'paused'
            ELSE 'active'
          END,
          effective_source_type = 'team',
          effective_source_team_id = ?,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `).run(now, activeTeamSource.source_team_id, now, actor, now, now, now, now, now, authorizationId)
    return
  }

  const pausedTeamSource = database.prepare(`
    SELECT ras.source_team_id
    FROM resource_authorization_sources ras
    INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
    INNER JOIN resource_authorization_grants trg
      ON trg.resource_type = ra.resource_type
      AND trg.resource_id = ra.resource_id
      AND trg.grantee_type = 'team'
      AND trg.grantee_team_id = ras.source_team_id
      AND trg.status = 'paused'
    WHERE ras.authorization_id = ?
      AND ras.source_type = 'team'
      AND ras.status = 'active'
    ORDER BY ras.activated_at ASC, ras.created_at ASC, ras.id ASC
    LIMIT 1
  `).get(authorizationId) as unknown as { source_team_id?: string | null } | undefined

  if (pausedTeamSource?.source_team_id) {
    database.prepare(`
      UPDATE resource_authorizations
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            ELSE 'paused'
          END,
          effective_source_type = 'team',
          effective_source_team_id = ?,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE 'authorization_paused' END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `).run(now, pausedTeamSource.source_team_id, now, actor, now, now, now, now, now, authorizationId)
    cleanupInactiveAuthorizationBindings(database, [authorizationId])
    return
  }

  const activeManualSource = database.prepare(`
    SELECT id
    FROM resource_authorization_sources
    WHERE authorization_id = ? AND source_type = 'manual' AND status = 'active'
    ORDER BY activated_at ASC, created_at ASC, id ASC
    LIMIT 1
  `).get(authorizationId) as unknown as { id?: string } | undefined

  if (activeManualSource?.id) {
    database.prepare(`
      UPDATE resource_authorizations
      SET status = CASE
            WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'expired'
            WHEN status = 'paused' THEN 'paused'
            ELSE 'active'
          END,
          effective_source_type = 'manual',
          effective_source_team_id = NULL,
          revoked_by = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired' ELSE NULL END,
          last_source_changed_at = ?,
          updated_at = ?
      WHERE id = ?
    `).run(now, now, actor, now, now, now, now, now, authorizationId)
    return
  }

  const preserveExpiredWhenNoActiveSource = options.preserveExpiredWhenNoActiveSource === false ? 0 : 1
  const noActiveSourceReason = options.noActiveSourceReason ?? null
  const terminalStatus = options.terminalStatus ?? 'revoked'
  database.prepare(`
    UPDATE resource_authorizations
    SET status = CASE WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'expired' ELSE ? END,
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = CASE WHEN ? IS NOT NULL THEN ? ELSE COALESCE(revoked_by, ?) END,
        revoked_at = CASE WHEN ? IS NOT NULL THEN ? ELSE COALESCE(revoked_at, ?) END,
        revoked_reason = CASE
          WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired'
          WHEN ? IS NOT NULL THEN ?
          ELSE COALESCE(revoked_reason, 'no_active_source')
        END,
        last_source_changed_at = ?,
        updated_at = ?
    WHERE id = ?
  `).run(
    preserveExpiredWhenNoActiveSource,
    now,
    terminalStatus,
    noActiveSourceReason,
    actor,
    actor,
    noActiveSourceReason,
    now,
    now,
    preserveExpiredWhenNoActiveSource,
    now,
    noActiveSourceReason,
    noActiveSourceReason,
    now,
    now,
    authorizationId
  )
  cleanupInactiveAuthorizationBindings(database, [authorizationId])
}

export function cleanupInactiveAuthorizationBindings(database = getDatabase(), authorizationIds?: string[]): void {
  void authorizationIds
  void database
  clearGatewayApiKeyValidationCache()
  invalidateAuthorizationLookupCaches()
}

export function upsertResourceAuthorizationGrant(input: { resourceType: ResourceAuthorizationResourceType; resourceId: string; ownerSystemAccountId: string; granteeType: 'system_account' | 'team'; granteeId: string; remark?: string; expiresAt?: string | null; limits?: unknown; modelPolicy?: unknown; actor: string; now: string; database: DatabaseSync }): ResourceAuthorizationGrantRow {
  if (input.granteeType === 'system_account' && input.granteeId === input.ownerSystemAccountId) {
    throw new Error('不能授权给资源所有者自己')
  }
  const active = input.database.prepare(`
    SELECT id
    FROM resource_authorization_grants
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_type = ?
      AND COALESCE(grantee_system_account_id, '') = COALESCE(?, '')
      AND COALESCE(grantee_team_id, '') = COALESCE(?, '')
      AND status = 'active'
    LIMIT 1
  `).get(
    input.resourceType,
    input.resourceId,
    input.granteeType,
    input.granteeType === 'system_account' ? input.granteeId : null,
    input.granteeType === 'team' ? input.granteeId : null
  ) as unknown as { id?: string } | undefined
  if (active?.id) {
    throw new Error(input.granteeType === 'team' ? '该资源已授权给该团队，请勿重复授权' : '该资源已授权给该用户，请勿重复授权')
  }
  const existing = input.database.prepare(`
    SELECT *
    FROM resource_authorization_grants
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_type = ?
      AND COALESCE(grantee_system_account_id, '') = COALESCE(?, '')
      AND COALESCE(grantee_team_id, '') = COALESCE(?, '')
      AND status IN ('paused', 'expired', 'revoked', 'returned')
    ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 WHEN 'expired' THEN 2 WHEN 'revoked' THEN 3 WHEN 'returned' THEN 4 ELSE 5 END,
      created_at ASC,
      id ASC
    LIMIT 1
  `).get(
    input.resourceType,
    input.resourceId,
    input.granteeType,
    input.granteeType === 'system_account' ? input.granteeId : null,
    input.granteeType === 'team' ? input.granteeId : null
  ) as unknown as ResourceAuthorizationGrantRow | undefined
  const id = existing?.id ?? newId('rauthgrant')
  const nextExpiresAt = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
    ? input.expiresAt ?? null
    : existing?.expires_at ?? null
  if (existing) {
    input.database.prepare("UPDATE resource_authorization_grants SET status = 'active', remark = COALESCE(?, remark), expires_at = ?, limits_json = ?, model_policy_json = COALESCE(?, model_policy_json), revoked_by = NULL, revoked_at = NULL, updated_at = ? WHERE id = ?")
      .run(input.remark ?? null, nextExpiresAt, requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits)), jsonObjectOrNull(input.modelPolicy), input.now, id)
  } else {
    input.database.prepare("INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at, limits_json, model_policy_json, created_by, created_at, revoked_by, revoked_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, ?, NULL, NULL, ?)")
      .run(
        id,
        input.resourceType,
        input.resourceId,
        input.ownerSystemAccountId,
        input.granteeType,
        input.granteeType === 'system_account' ? input.granteeId : null,
        input.granteeType === 'team' ? input.granteeId : null,
        input.remark ?? null,
        nextExpiresAt,
        requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits)),
        jsonObjectOrNull(input.modelPolicy),
        input.actor,
        input.now,
        input.now
      )
  }
  const row = input.database.prepare('SELECT * FROM resource_authorization_grants WHERE id = ?').get(id) as unknown as ResourceAuthorizationGrantRow | undefined
  if (!row) throw new Error('创建资源授权失败')
  invalidateAuthorizationLookupCaches()
  return row
}

export function revokeResourceAuthorizationGrant(grant: ResourceAuthorizationGrantRow, actor: string, database: DatabaseSync, now: string): void {
  database
    .prepare("UPDATE resource_authorization_grants SET status = 'revoked', revoked_by = ?, revoked_at = ?, updated_at = ? WHERE id = ?")
    .run(actor, now, now, grant.id)
  syncResourceAuthorizationGrantRuntime({ ...grant, status: 'revoked', revoked_by: actor, revoked_at: now, updated_at: now }, actor, database, now)
  cleanupInactiveAuthorizationBindings(database)
}

export function returnResourceAuthorizationGrant(grant: ResourceAuthorizationGrantRow, actor: string, database: DatabaseSync, now: string): void {
  database
    .prepare("UPDATE resource_authorization_grants SET status = 'returned', revoked_by = ?, revoked_at = ?, updated_at = ? WHERE id = ?")
    .run(actor, now, now, grant.id)
  syncResourceAuthorizationGrantRuntime({ ...grant, status: 'returned', revoked_by: actor, revoked_at: now, updated_at: now }, actor, database, now)
  cleanupInactiveAuthorizationBindings(database)
}

export function syncResourceAuthorizationGrantRuntime(grant: ResourceAuthorizationGrantRow, actor: string, database: DatabaseSync, now: string): void {
  if (grant.grantee_type === 'system_account') {
    syncUserGrantRuntime(grant, actor, database, now)
    return
  }
  syncTeamGrantMemberAuthorizations(grant, actor, database, now)
}

function syncUserGrantRuntime(grant: ResourceAuthorizationGrantRow, actor: string, database: DatabaseSync, now: string): void {
  if (!grant.grantee_system_account_id) return
  const runtime = loadRuntimeAuthorizationForUserGrant(grant, database)
  if (grant.status === 'active') {
    upsertResourceAuthorizationForUser({
      resourceType: grant.resource_type,
      resourceId: grant.resource_id,
      ownerSystemAccountId: grant.resource_owner_system_account_id,
      granteeSystemAccountId: grant.grantee_system_account_id,
      sourceType: 'manual',
      remark: grant.remark ?? undefined,
      expiresAt: grant.expires_at,
      limits: parseRequestQuotaLimitsJson(grant.limits_json),
      modelPolicy: parseOptionalJsonObject(grant.model_policy_json ?? undefined),
      actor,
      now,
      database
    })
    return
  }
  if (!runtime) return
  if (grant.status === 'paused' || grant.status === 'expired') {
    database.prepare(`
      UPDATE resource_authorizations
      SET status = ?,
          expires_at = ?,
          limits_json = ?,
          revoked_by = CASE WHEN ? = 'expired' THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN ? = 'expired' THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN ? = 'expired' THEN 'authorization_expired' ELSE 'authorization_paused' END,
          updated_at = ?
      WHERE id = ?
    `).run(grant.status, grant.expires_at, grant.limits_json, grant.status, actor, grant.status, now, grant.status, now, runtime.id)
    refreshResourceAuthorizationEffectiveSource(runtime.id, actor, now, database)
    return
  }
  database.prepare(`
    UPDATE resource_authorization_sources
    SET status = 'revoked',
        ended_at = COALESCE(ended_at, ?),
        ended_reason = COALESCE(ended_reason, ?),
        revoked_by = ?,
        revoked_at = ?,
        updated_at = ?
    WHERE authorization_id = ? AND source_type = 'manual' AND status IN ('active', 'superseded')
  `).run(now, grant.status === 'returned' ? 'grantee_returned' : 'authorization_revoked', actor, now, now, runtime.id)
  refreshResourceAuthorizationEffectiveSource(
    runtime.id,
    actor,
    now,
    database,
    grant.status === 'revoked' || grant.status === 'returned'
      ? {
        noActiveSourceReason: grant.status === 'returned' ? 'grantee_returned' : 'authorization_revoked',
        preserveExpiredWhenNoActiveSource: false,
        terminalStatus: grant.status === 'returned' ? 'returned' : 'revoked'
      }
      : undefined
  )
}

function syncTeamGrantMemberAuthorizations(grant: ResourceAuthorizationGrantRow, actor: string, database: DatabaseSync, now: string): void {
  const teamId = grant.grantee_team_id
  if (!teamId) return
  if (grant.status === 'revoked' || grant.status === 'returned') {
    revokeTeamGrantSources(grant.resource_type, grant.resource_id, teamId, actor, database, now)
    return
  }
  if (grant.status === 'paused' || grant.status === 'expired') {
    const sourceRows = database.prepare(`
      SELECT ras.authorization_id
      FROM resource_authorization_sources ras
      INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
      WHERE ra.resource_type = ?
        AND ra.resource_id = ?
        AND ras.source_type = 'team'
        AND ras.source_team_id = ?
        AND ras.status = 'active'
    `).all(grant.resource_type, grant.resource_id, teamId) as unknown as Array<{ authorization_id?: string }>
    for (const sourceRow of sourceRows) {
      if (!sourceRow.authorization_id) continue
      database.prepare(`
        UPDATE resource_authorizations
        SET expires_at = ?,
            revoked_by = CASE WHEN ? = 'expired' THEN COALESCE(revoked_by, ?) ELSE NULL END,
            revoked_at = CASE WHEN ? = 'expired' THEN COALESCE(revoked_at, ?) ELSE NULL END,
            revoked_reason = CASE WHEN ? = 'expired' THEN 'authorization_expired' WHEN ? = 'paused' THEN 'authorization_paused' ELSE revoked_reason END,
            limits_json = ?,
            updated_at = ?
        WHERE id = ?
      `).run(grant.expires_at, grant.status, actor, grant.status, now, grant.status, grant.status, grant.limits_json, now, sourceRow.authorization_id)
      refreshResourceAuthorizationEffectiveSource(sourceRow.authorization_id, actor, now, database)
    }
    return
  }
  if (grant.status === 'active') {
    const members = activeTeamMemberRows(teamId, database).filter((member) => member.system_account_id !== grant.resource_owner_system_account_id)
    for (const member of members) {
      upsertResourceAuthorizationForUser({ resourceType: grant.resource_type, resourceId: grant.resource_id, ownerSystemAccountId: grant.resource_owner_system_account_id, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: teamId, remark: grant.remark ?? undefined, expiresAt: grant.expires_at, limits: parseRequestQuotaLimitsJson(grant.limits_json), modelPolicy: parseOptionalJsonObject(grant.model_policy_json ?? undefined), actor, now, database })
    }
  }
  const rows = database.prepare(`
    SELECT ra.id
    FROM resource_authorizations ra
    INNER JOIN resource_authorization_sources ras ON ras.authorization_id = ra.id
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND ras.source_type = 'team'
      AND ras.source_team_id = ?
      AND ras.status = 'active'
  `).all(grant.resource_type, grant.resource_id, teamId) as unknown as Array<{ id?: string }>
  for (const row of rows) {
    if (!row.id) continue
    const otherActiveTeam = database.prepare(`
      SELECT ras.id
      FROM resource_authorization_sources ras
      INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
      INNER JOIN resource_authorization_grants trg
        ON trg.resource_type = ra.resource_type
        AND trg.resource_id = ra.resource_id
        AND trg.grantee_type = 'team'
        AND trg.grantee_team_id = ras.source_team_id
        AND trg.id <> ?
        AND trg.status = 'active'
        AND (trg.expires_at IS NULL OR trg.expires_at > ?)
      WHERE ras.authorization_id = ?
        AND ras.source_type = 'team'
        AND ras.status = 'active'
      LIMIT 1
    `).get(grant.id, now, row.id) as unknown as { id?: string } | undefined
    if (!otherActiveTeam?.id) {
      database.prepare(`
        UPDATE resource_authorizations
        SET expires_at = ?,
            revoked_by = CASE WHEN ? IN ('active', 'paused') THEN NULL ELSE ? END,
            revoked_at = CASE WHEN ? IN ('active', 'paused') THEN NULL ELSE ? END,
            revoked_reason = CASE WHEN ? = 'expired' THEN 'authorization_expired' WHEN ? = 'paused' THEN 'authorization_paused' ELSE NULL END,
            limits_json = ?,
            updated_at = ?
        WHERE id = ?
      `).run(grant.expires_at, grant.status, grant.revoked_by, grant.status, grant.revoked_at, grant.status, grant.status, grant.limits_json, now, row.id)
    }
    refreshResourceAuthorizationEffectiveSource(row.id, actor, now, database)
  }
}

export function applyActiveTeamGrantsToMember(teamId: string, systemAccountId: string, access: AccessScope | undefined, database: DatabaseSync, now: string): void {
  const grants = database.prepare("SELECT * FROM resource_authorization_grants WHERE grantee_type = 'team' AND grantee_team_id = ? AND status = 'active'").all(teamId) as unknown as ResourceAuthorizationGrantRow[]
  const actor = currentSystemAccountId(access)
  for (const grant of grants) {
    if (grant.resource_owner_system_account_id === systemAccountId) continue
    upsertResourceAuthorizationForUser({ resourceType: grant.resource_type, resourceId: grant.resource_id, ownerSystemAccountId: grant.resource_owner_system_account_id, granteeSystemAccountId: systemAccountId, sourceType: 'team', sourceTeamId: teamId, remark: grant.remark ?? undefined, expiresAt: grant.expires_at, limits: parseRequestQuotaLimitsJson(grant.limits_json), modelPolicy: parseOptionalJsonObject(grant.model_policy_json ?? undefined), actor, now, database })
  }
}

export function revokeTeamSourcesForMember(teamId: string, systemAccountId: string, actor: string, database: DatabaseSync, now: string): void {
  const rows = database.prepare("SELECT ras.authorization_id FROM resource_authorization_sources ras INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id WHERE ras.source_type = 'team' AND ras.source_team_id = ? AND ras.status = 'active' AND ra.grantee_system_account_id = ?").all(teamId, systemAccountId) as unknown as Array<{ authorization_id: string }>
  for (const row of rows) {
    database.prepare("UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'member_removed'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'").run(now, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database)
  }
}

function revokeTeamGrantSources(resourceType: ResourceAuthorizationResourceType, resourceId: string, teamId: string, actor: string, database: DatabaseSync, now: string): void {
  const rows = database.prepare("SELECT ras.authorization_id FROM resource_authorization_sources ras INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id WHERE ras.source_type = 'team' AND ras.source_team_id = ? AND ras.status = 'active' AND ra.resource_type = ? AND ra.resource_id = ?").all(teamId, resourceType, resourceId) as unknown as Array<{ authorization_id: string }>
  for (const row of rows) {
    database.prepare("UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'team_revoked'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'").run(now, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database, {
      noActiveSourceReason: 'authorization_revoked',
      preserveExpiredWhenNoActiveSource: false
    })
  }
}

export function revokeAllTeamSources(teamId: string, actor: string, database: DatabaseSync, now: string, reason: string): void {
  const rows = database.prepare("SELECT DISTINCT authorization_id FROM resource_authorization_sources WHERE source_type = 'team' AND source_team_id = ? AND status = 'active'").all(teamId) as unknown as Array<{ authorization_id: string }>
  for (const row of rows) {
    database.prepare(`
      UPDATE resource_authorization_sources
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, ?),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'
    `).run(now, reason, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database)
  }
}

export function reactivateTeamGrantSources(teamId: string, access: AccessScope | undefined, database: DatabaseSync, now: string): void {
  const memberRows = activeTeamMemberRows(teamId, database)
  for (const member of memberRows) {
    applyActiveTeamGrantsToMember(teamId, member.system_account_id, access, database, now)
  }
}

export function deactivateAuthorizationIfNoActiveSources(authorizationId: string, actor: string, now: string, database = getDatabase()): void {
  refreshResourceAuthorizationEffectiveSource(authorizationId, actor, now, database)
}

function invalidateAuthorizationLookupCaches(): void {
  clearResourceAuthorizationLookupCaches()
  invalidateGroupAccountIdsCache()
}
