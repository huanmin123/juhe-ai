import type { DatabaseSync } from 'node:sqlite'

import type {
  AuthorizationStatus,
  ResourceAuthorizationResourceType,
  ResourceAuthorizationSourceStatus,
  ResourceAuthorizationSourceType
} from '../domain/types.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { encryptJson } from './crypto.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { clearResourceAuthorizationLookupCaches } from './authorization-read-loaders.js'
import { isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { loadRuntimeAuthorizationForUserGrant } from './resource-authorization-read.repository.js'
import { maxAuthorizationExpirySweepBatchSize } from './authorization-sweep-limits.js'
import { rememberRequestQuotaHourlyWindowsFromJson } from './request-quota-hourly-windows.repository.js'
import { normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import { maxSystemTeamActiveGrantCount, maxSystemTeamMembersPerTeam } from './system-team-limits.js'
import type {
  AccountRow,
  ResourceAuthorizationGrantRow,
  ResourceAuthorizationRow,
  ResourceAuthorizationSourceRow,
  SystemTeamMemberRow
} from './repository-row-types.js'
import { markAllGroupAccountStatsDirty } from './usage-stats.repository.js'

interface RefreshEffectiveSourceOptions {
  noActiveSourceReason?: string
  preserveExpiredWhenNoActiveSource?: boolean
  terminalStatus?: 'revoked' | 'returned'
}

export function expireDueResourceAuthorizations(limit = maxAuthorizationExpirySweepBatchSize): number {
  const now = nowIso()
  const database = getBusinessDatabase()
  const batchSize = Math.max(1, Math.trunc(limit))
  const dueGrants = database
    .prepare(`
      SELECT *
      FROM resource_authorization_grants
      WHERE status IN ('active', 'paused')
        AND expires_at IS NOT NULL
        AND expires_at <= ?
      ORDER BY expires_at ASC, updated_at ASC, id ASC
      LIMIT ?
    `)
    .all(now, batchSize) as unknown as ResourceAuthorizationGrantRow[]
  if (!dueGrants.length) return 0
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const grant of dueGrants) {
      database.prepare(`
        UPDATE resource_authorization_grants
        SET status = 'expired',
            revoked_at = COALESCE(revoked_at, ?),
            updated_at = ?
        WHERE id = ?
      `).run(now, now, grant.id)
      syncResourceAuthorizationGrantRuntime({ ...grant, status: 'expired', revoked_at: grant.revoked_at ?? now, updated_at: now }, grant.revoked_by ?? grant.created_by, database, now)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  cleanupInactiveAuthorizationBindings(database)
  invalidateAuthorizationLookupCaches()
  markAllGroupAccountStatsDirty('authorization_expired')
  notifyGatewayRuntimeCacheInvalidation('authorization_expired')
  notifyAuthorizationQuotaCacheInvalidation('authorization_expired')
  return dueGrants.length
}

export function activeTeamMemberRows(teamId: string, database = getBusinessDatabase()): SystemTeamMemberRow[] {
  const rows = database.prepare(`
    SELECT system_team_members.*
    FROM system_team_members
    INNER JOIN system_accounts ON system_accounts.id = system_team_members.system_account_id
    WHERE system_team_members.team_id = ?
      AND system_team_members.status = 'active'
      AND system_accounts.status = 'active'
    ORDER BY system_team_members.joined_at ASC, system_team_members.id ASC
    LIMIT ?
  `).all(teamId, maxSystemTeamMembersPerTeam + 1) as unknown as SystemTeamMemberRow[]
  if (rows.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再继续`)
  }
  return rows
}

export function assertActiveTeamGrantFanoutWithinLimit(teamId: string, database = getBusinessDatabase()): void {
  void activeTeamGrantRows(teamId, database)
}

function activeTeamGrantRows(teamId: string, database = getBusinessDatabase()): ResourceAuthorizationGrantRow[] {
  const rows = database.prepare(`
    SELECT *
    FROM resource_authorization_grants
    WHERE grantee_type = 'team'
      AND grantee_team_id = ?
      AND status = 'active'
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `).all(teamId, maxSystemTeamActiveGrantCount + 1) as unknown as ResourceAuthorizationGrantRow[]
  if (rows.length > maxSystemTeamActiveGrantCount) {
    throw new Error(`单个授权团队最多支持 ${maxSystemTeamActiveGrantCount} 条有效授权，请先回收或停用部分授权`)
  }
  return rows
}

export function upsertResourceAuthorizationForUser(input: { resourceType: ResourceAuthorizationResourceType; resourceId: string; ownerSystemAccountId: string; granteeSystemAccountId: string; sourceType: ResourceAuthorizationSourceType; sourceTeamId?: string; targetGroupId?: string; remark?: string; expiresAt?: string | null; limits?: unknown; actor: string; now: string; database: DatabaseSync }): ResourceAuthorizationRow {
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
  const nextStatus: AuthorizationStatus = isResourceAuthorizationExpired(nextExpiresAt) ? 'expired' : 'active'
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
           revoked_by = ?,
          revoked_at = ?,
          revoked_reason = ?,
          updated_at = ?
      WHERE id = ?
    `).run(input.ownerSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, nextExpiresAt, nextLimitsJson, nextRevokedBy, nextRevokedAt, nextRevokedReason, input.now, authorizationId)
  } else {
    input.database.prepare(`
      INSERT INTO resource_authorizations (
        id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
        effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
        remark, expires_at, limits_json,
        created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'use', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).run(authorizationId, input.resourceType, input.resourceId, input.ownerSystemAccountId, input.granteeSystemAccountId, nextStatus, nextEffectiveSourceType, nextEffectiveSourceTeamId, input.now, input.now, input.remark ?? null, nextExpiresAt, nextLimitsJson, input.actor, input.now, nextRevokedBy, nextRevokedAt, nextRevokedReason, input.now)
  }
  rememberRequestQuotaHourlyWindowsFromJson(nextLimitsJson, input.database, input.now)
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
  const instance = ensureAccountAuthorizationInstance(database, authorization, now)
  if (!instance?.id || !instance.provider_code) return
  const requestedGroupId = targetGroupId?.trim()
  const existingBinding = database
    .prepare(`
      SELECT group_id
      FROM group_accounts
      WHERE account_id = ?
        AND system_account_id = ?
        AND account_authorization_id = ?
        AND enabled = 1
      ORDER BY updated_at DESC, group_id ASC, account_id ASC
      LIMIT 1
    `)
    .get(instance.id, authorization.grantee_system_account_id, authorization.id) as unknown as { group_id?: string } | undefined
  if (existingBinding?.group_id && (!requestedGroupId || existingBinding.group_id === requestedGroupId)) return
  const bindGroupId = groupIdForAuthorizationBinding(database, instance.provider_code, authorization.grantee_system_account_id, requestedGroupId)
  if (!bindGroupId) return
  if (existingBinding?.group_id && existingBinding.group_id !== bindGroupId) {
    database
      .prepare('DELETE FROM group_accounts WHERE account_id = ? AND system_account_id = ? AND account_authorization_id = ?')
      .run(instance.id, authorization.grantee_system_account_id, authorization.id)
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
    .run(authorization.grantee_system_account_id, bindGroupId, instance.id, authorization.id, now, now)
  invalidateGroupAccountIdsCache(bindGroupId)
}

export function syncAccountAuthorizationInstanceNamesForSourceAccount(database: DatabaseSync, sourceAccountId: string, sourceName: string, providerCode: string, now = nowIso()): string[] {
  const rows = database
    .prepare(`
      SELECT id, system_account_id, authorization_instance_authorization_id, name
      FROM accounts
      WHERE authorization_instance_source_account_id = ?
      ORDER BY system_account_id ASC, id ASC
    `)
    .all(sourceAccountId) as unknown as Array<{
      id?: string
      system_account_id?: string
      authorization_instance_authorization_id?: string | null
      name?: string
    }>
  const changedIds: string[] = []
  const updateName = database.prepare('UPDATE accounts SET name = ?, updated_at = ? WHERE id = ?')
  for (const row of rows) {
    if (!row.id || !row.system_account_id || !row.authorization_instance_authorization_id) continue
    const nextName = uniqueAuthorizedAccountInstanceName(
      database,
      sourceName,
      row.system_account_id,
      providerCode,
      row.authorization_instance_authorization_id,
      row.id
    )
    if (row.name === nextName) continue
    updateName.run(nextName, now, row.id)
    changedIds.push(row.id)
  }
  return changedIds
}

function ensureAccountAuthorizationInstance(database: DatabaseSync, authorization: ResourceAuthorizationRow, now: string): AccountRow | undefined {
  const existing = database
    .prepare('SELECT * FROM accounts WHERE authorization_instance_authorization_id = ? LIMIT 1')
    .get(authorization.id) as unknown as AccountRow | undefined
  if (existing) {
    return existing
  }
  const source = database
    .prepare('SELECT * FROM accounts WHERE id = ? LIMIT 1')
    .get(authorization.resource_id) as unknown as AccountRow | undefined
  if (!source || source.system_account_id === authorization.grantee_system_account_id) return undefined
  const id = newId('acc')
  const name = uniqueAuthorizedAccountInstanceName(database, source.name, authorization.grantee_system_account_id, source.provider_code, authorization.id)
  database
    .prepare(`
      INSERT INTO accounts (
        id, system_account_id, provider_code, name, type, status, credentials_encrypted, credential_fingerprint, account_identity_fingerprint, credential_mask,
        proxy_profile_id, concurrency_limit, error_policy_id,
        priority, super_priority_enabled, fallback_enabled, schedulable, notes, account_expires_at, cooldown_until, last_error_code, last_error_message,
        cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at,
        authorization_instance_source_account_id, authorization_instance_authorization_id, authorization_instance_owner_system_account_id,
        created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'active', ?, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, 1, NULL, NULL, NULL, NULL, NULL, NULL, 0, NULL, ?, ?, ?, ?, ?)
    `)
    .run(
      id,
      authorization.grantee_system_account_id,
      source.provider_code,
      name,
      source.type,
      encryptJson({}),
      '',
      null,
      source.concurrency_limit,
      null,
      0,
      0,
      0,
      authorization.resource_id,
      authorization.id,
      authorization.resource_owner_system_account_id,
      now,
      now
    )
  return database.prepare('SELECT * FROM accounts WHERE id = ? LIMIT 1').get(id) as unknown as AccountRow | undefined
}

function uniqueAuthorizedAccountInstanceName(database: DatabaseSync, sourceName: string, systemAccountId: string, providerCode: string, authorizationId: string, exceptAccountId?: string): string {
  const baseName = sourceName.trim() || '授权账户'
  const shortId = authorizationId.split('_').pop()?.slice(0, 6) || authorizationId.slice(-6)
  const candidates = [
    baseName,
    `${baseName}（授权）`,
    `${baseName}（授权 ${shortId}）`
  ]
  for (const candidate of candidates) {
    if (isAccountNameAvailable(database, systemAccountId, providerCode, candidate, exceptAccountId)) return candidate
  }
  for (let index = 2; index <= 1000; index += 1) {
    const candidate = `${baseName}（授权 ${shortId}-${index}）`
    if (isAccountNameAvailable(database, systemAccountId, providerCode, candidate, exceptAccountId)) return candidate
  }
  return `${baseName}（授权 ${shortId}-${Date.now()}）`
}

function isAccountNameAvailable(database: DatabaseSync, systemAccountId: string, providerCode: string, name: string, exceptAccountId?: string): boolean {
  const row = database
    .prepare('SELECT id FROM accounts WHERE system_account_id = ? AND provider_code = ? AND lower(name) = lower(?) LIMIT 1')
    .get(systemAccountId, providerCode, name) as unknown as { id?: string } | undefined
  return !row?.id || row.id === exceptAccountId
}

function groupIdForAuthorizationBinding(database: DatabaseSync, providerCode: string, systemAccountId: string, targetGroupId?: string): string {
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
  return defaultGroupIdForAuthorizationBinding(database, providerCode, systemAccountId)
}

function defaultGroupIdForAuthorizationBinding(database: DatabaseSync, providerCode: string, systemAccountId: string): string {
  const existing = database
    .prepare('SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 AND enabled = 1 ORDER BY updated_at DESC, id ASC LIMIT 1')
    .get(systemAccountId, providerCode) as unknown as { id?: string } | undefined
  if (existing?.id) return existing.id
  throw new Error('目标用户缺少启用的默认分组，请按当前数据契约修复目标用户分组后再授权')
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
  database = getBusinessDatabase(),
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

export function cleanupInactiveAuthorizationBindings(database = getBusinessDatabase(), authorizationIds?: string[]): void {
  void authorizationIds
  void database
  clearGatewayApiKeyValidationCache()
  invalidateAuthorizationLookupCaches()
}

export function upsertResourceAuthorizationGrant(input: { resourceType: ResourceAuthorizationResourceType; resourceId: string; ownerSystemAccountId: string; granteeType: 'system_account' | 'team'; granteeId: string; remark?: string; expiresAt?: string | null; limits?: unknown; actor: string; now: string; database: DatabaseSync }): ResourceAuthorizationGrantRow {
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
  const nextLimitsJson = requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
  if (existing) {
    input.database.prepare("UPDATE resource_authorization_grants SET status = 'active', remark = COALESCE(?, remark), expires_at = ?, limits_json = ?, revoked_by = NULL, revoked_at = NULL, updated_at = ? WHERE id = ?")
      .run(input.remark ?? null, nextExpiresAt, nextLimitsJson, input.now, id)
  } else {
    input.database.prepare("INSERT INTO resource_authorization_grants (id, resource_type, resource_id, resource_owner_system_account_id, grantee_type, grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at, limits_json, created_by, created_at, revoked_by, revoked_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, NULL, NULL, ?)")
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
        nextLimitsJson,
        input.actor,
        input.now,
        input.now
      )
  }
  rememberRequestQuotaHourlyWindowsFromJson(nextLimitsJson, input.database, input.now)
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
  rememberRequestQuotaHourlyWindowsFromJson(grant.limits_json, database, now)
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
      ORDER BY ras.authorization_id ASC
      LIMIT ?
    `).all(grant.resource_type, grant.resource_id, teamId, maxSystemTeamMembersPerTeam + 1) as unknown as Array<{ authorization_id?: string }>
    if (sourceRows.length > maxSystemTeamMembersPerTeam) {
      throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再继续`)
    }
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
      upsertResourceAuthorizationForUser({ resourceType: grant.resource_type, resourceId: grant.resource_id, ownerSystemAccountId: grant.resource_owner_system_account_id, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: teamId, remark: grant.remark ?? undefined, expiresAt: grant.expires_at, limits: parseRequestQuotaLimitsJson(grant.limits_json), actor, now, database })
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
    ORDER BY ra.id ASC
    LIMIT ?
  `).all(grant.resource_type, grant.resource_id, teamId, maxSystemTeamMembersPerTeam + 1) as unknown as Array<{ id?: string }>
  if (rows.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再继续`)
  }
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
  const grants = activeTeamGrantRows(teamId, database)
  const actor = currentSystemAccountId(access)
  for (const grant of grants) {
    if (grant.resource_owner_system_account_id === systemAccountId) continue
    upsertResourceAuthorizationForUser({ resourceType: grant.resource_type, resourceId: grant.resource_id, ownerSystemAccountId: grant.resource_owner_system_account_id, granteeSystemAccountId: systemAccountId, sourceType: 'team', sourceTeamId: teamId, remark: grant.remark ?? undefined, expiresAt: grant.expires_at, limits: parseRequestQuotaLimitsJson(grant.limits_json), actor, now, database })
  }
}

export function revokeTeamSourcesForMember(teamId: string, systemAccountId: string, actor: string, database: DatabaseSync, now: string): void {
  const rows = database.prepare(`
    SELECT ras.authorization_id
    FROM resource_authorization_sources ras
    INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
    WHERE ras.source_type = 'team'
      AND ras.source_team_id = ?
      AND ras.status = 'active'
      AND ra.grantee_system_account_id = ?
    ORDER BY ras.authorization_id ASC
    LIMIT ?
  `).all(teamId, systemAccountId, maxSystemTeamActiveGrantCount + 1) as unknown as Array<{ authorization_id: string }>
  if (rows.length > maxSystemTeamActiveGrantCount) {
    throw new Error(`单个授权团队最多支持 ${maxSystemTeamActiveGrantCount} 条有效授权，请先回收或停用部分授权`)
  }
  for (const row of rows) {
    database.prepare("UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'member_removed'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'").run(now, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database)
  }
}

function revokeTeamGrantSources(resourceType: ResourceAuthorizationResourceType, resourceId: string, teamId: string, actor: string, database: DatabaseSync, now: string): void {
  const rows = database.prepare(`
    SELECT ras.authorization_id
    FROM resource_authorization_sources ras
    INNER JOIN resource_authorizations ra ON ra.id = ras.authorization_id
    WHERE ras.source_type = 'team'
      AND ras.source_team_id = ?
      AND ras.status = 'active'
      AND ra.resource_type = ?
      AND ra.resource_id = ?
    ORDER BY ras.authorization_id ASC
    LIMIT ?
  `).all(teamId, resourceType, resourceId, maxSystemTeamMembersPerTeam + 1) as unknown as Array<{ authorization_id: string }>
  if (rows.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再继续`)
  }
  for (const row of rows) {
    database.prepare("UPDATE resource_authorization_sources SET status = 'revoked', ended_at = COALESCE(ended_at, ?), ended_reason = COALESCE(ended_reason, 'team_revoked'), revoked_by = ?, revoked_at = ?, updated_at = ? WHERE authorization_id = ? AND source_type = 'team' AND source_team_id = ? AND status = 'active'").run(now, actor, now, now, row.authorization_id, teamId)
    refreshResourceAuthorizationEffectiveSource(row.authorization_id, actor, now, database, {
      noActiveSourceReason: 'authorization_revoked',
      preserveExpiredWhenNoActiveSource: false
    })
  }
}

export function revokeAllTeamSources(teamId: string, actor: string, database: DatabaseSync, now: string, reason: string): void {
  const rows = database.prepare(`
    SELECT DISTINCT authorization_id
    FROM resource_authorization_sources
    WHERE source_type = 'team'
      AND source_team_id = ?
      AND status = 'active'
    ORDER BY authorization_id ASC
    LIMIT ?
  `).all(teamId, maxSystemTeamMembersPerTeam * maxSystemTeamActiveGrantCount + 1) as unknown as Array<{ authorization_id: string }>
  if (rows.length > maxSystemTeamMembersPerTeam * maxSystemTeamActiveGrantCount) {
    throw new Error(`授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权`)
  }
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

export function deactivateAuthorizationIfNoActiveSources(authorizationId: string, actor: string, now: string, database = getBusinessDatabase()): void {
  refreshResourceAuthorizationEffectiveSource(authorizationId, actor, now, database)
}

function invalidateAuthorizationLookupCaches(): void {
  clearResourceAuthorizationLookupCaches()
  invalidateGroupAccountIdsCache()
}
