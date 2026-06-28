import type { AuthorizationStatus, ResourceAuthorizationResourceType, ResourceAuthorizationSourceStatus, ResourceAuthorizationSourceType, ResourceAuthorizationSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, type AccessScope } from './access-scope.js'
import { replaceAccountNameSearchTermsAsync } from './account-name-search.repository.js'
import { clearResourceAuthorizationLookupCaches } from './authorization-read-loaders.js'
import { encryptJson } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { clearGatewayApiKeyValidationCache } from './gateway-api-key.repository.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { getPostgresPool } from './postgres-client.js'
import { groupOwnerAndProvider, canManageResourceOwner, isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { normalizeResourceType } from './resource-authorization-list-helpers.js'
import { findResourceAuthorizationSummary, findResourceAuthorizationSummaryAsync } from './resource-authorization-read.repository.js'
import {
  activeTeamMemberRows,
  assertActiveTeamGrantFanoutWithinLimit,
  cleanupInactiveAuthorizationBindings,
  expireDueResourceAuthorizations,
  revokeResourceAuthorizationGrant,
  syncResourceAuthorizationGrantRuntime,
  upsertResourceAuthorizationForUser,
  upsertResourceAuthorizationGrant
} from './resource-authorization-write-state.repository.js'
import { assertKnownInputKeys } from './repository-input-normalization.js'
import type { AccountRow, ResourceAuthorizationGrantRow, ResourceAuthorizationRow, ResourceAuthorizationSourceRow, SystemTeamMemberRow, SystemTeamRow } from './repository-row-types.js'
import { maxRequestQuotaHourlyWindowHours, normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import { findSystemAccountById } from './system-accounts.repository.js'
import { maxSystemTeamActiveGrantCount, maxSystemTeamMembersPerTeam } from './system-team-limits.js'
import { optionalNullableServerDateTimeIso } from './value-utils.js'

const resourceAuthorizationCreateInputKeys = new Set(['resourceType', 'resourceId', 'granteeType', 'granteeId', 'targetGroupId', 'remark', 'expiresAt', 'limits'])
const resourceAuthorizationUpdateInputKeys = new Set(['status', 'expiresAt', 'limits'])
const businessSchemaName = 'juhe_business'

export function createResourceAuthorization(input: Record<string, unknown>, access?: AccessScope): ResourceAuthorizationSummary {
  assertKnownInputKeys(input, resourceAuthorizationCreateInputKeys, '资源授权')
  const resourceType = normalizeResourceType(input.resourceType)
  const resourceId = normalizeRequiredTextInput(input.resourceId, '授权资源')
  if (!resourceType || !resourceId) throw new Error('请选择授权资源')
  const ownerSystemAccountId = resourceOwnerSystemAccountId(resourceType, resourceId)
  if (!ownerSystemAccountId || !canManageResourceOwner(ownerSystemAccountId, access)) throw new Error('授权资源不存在')
  const granteeType = normalizeResourceAuthorizationGranteeType(input.granteeType)
  const granteeId = normalizeRequiredTextInput(input.granteeId, '被授权对象')
  if (!granteeId) throw new Error('请选择被授权对象')
  const database = getBusinessDatabase()
  const now = nowIso()
  const expiresAt = normalizeResourceAuthorizationExpiresAtInput(input.expiresAt)
  validateResourceAuthorizationExpiresAt(resourceType, resourceId, expiresAt, Date.parse(now))
  const actor = currentSystemAccountId(access)
  const targetGroupId = normalizeOptionalTextInput(input.targetGroupId, '目标分组')
  const remark = normalizeOptionalTextInput(input.remark, '授权备注', { allowBlank: true })
  if (!targetGroupId && resourceType === 'account' && granteeType === 'system_account') {
    throw new Error('授权 AI 账户给个人时必须选择目标分组')
  }
  if (targetGroupId && (resourceType !== 'account' || granteeType !== 'system_account')) {
    throw new Error('只有授权 AI 账户给个人时可以指定目标分组')
  }
  let createdGrantId: string | undefined
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    if (granteeType === 'team') {
      const team = database.prepare("SELECT * FROM system_teams WHERE id = ? AND status = 'active'").get(granteeId) as unknown as SystemTeamRow | undefined
      if (!team) throw new Error('团队不存在或已停用')
      const members = activeTeamMemberRows(granteeId, database).filter((member) => member.system_account_id !== ownerSystemAccountId)
      if (!members.length) throw new Error('团队暂无可授权成员，请先添加非归属人成员后再授权')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark, expiresAt, limits: input.limits, actor, now, database })
      assertActiveTeamGrantFanoutWithinLimit(granteeId, database)
      createdGrantId = grant.id
      for (const member of members) {
        upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: granteeId, remark, expiresAt, limits: input.limits, actor, now, database })
      }
    } else {
      const grantee = findSystemAccountById(granteeId)
      if (!grantee || grantee.status !== 'active') throw new Error('被授权用户不存在或已停用')
      if (granteeId === ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
      const grant = upsertResourceAuthorizationGrant({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark, expiresAt, limits: input.limits, actor, now, database })
      createdGrantId = grant.id
      upsertResourceAuthorizationForUser({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: granteeId, sourceType: 'manual', targetGroupId, remark, expiresAt, limits: input.limits, actor, now, database })
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_created' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_created')
  const created = createdGrantId ? findResourceAuthorizationAfterWrite(createdGrantId, access) : undefined
  if (created) return created
  throw new Error('创建资源授权失败')
}

export async function createResourceAuthorizationAsync(input: Record<string, unknown>, access?: AccessScope): Promise<ResourceAuthorizationSummary> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return createResourceAuthorization(input, access)
  }
  assertKnownInputKeys(input, resourceAuthorizationCreateInputKeys, '资源授权')
  const resourceType = normalizeResourceType(input.resourceType)
  const resourceId = normalizeRequiredTextInput(input.resourceId, '授权资源')
  if (!resourceType || !resourceId) throw new Error('请选择授权资源')
  const client = await getResourceAuthorizationWriteClient()
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const expiresAt = normalizeResourceAuthorizationExpiresAtInput(input.expiresAt)
  const granteeType = normalizeResourceAuthorizationGranteeType(input.granteeType)
  const granteeId = normalizeRequiredTextInput(input.granteeId, '被授权对象')
  if (!granteeId) throw new Error('请选择被授权对象')
  const targetGroupId = normalizeOptionalTextInput(input.targetGroupId, '目标分组')
  const remark = normalizeOptionalTextInput(input.remark, '授权备注', { allowBlank: true })
  if (!targetGroupId && resourceType === 'account' && granteeType === 'system_account') {
    throw new Error('授权 AI 账户给个人时必须选择目标分组')
  }
  if (targetGroupId && (resourceType !== 'account' || granteeType !== 'system_account')) {
    throw new Error('只有授权 AI 账户给个人时可以指定目标分组')
  }

  let createdGrantId: string | undefined
  await client.transaction(async (tx) => {
    const ownerSystemAccountId = await resourceOwnerSystemAccountIdAsync(tx, resourceType, resourceId)
    if (!ownerSystemAccountId || !canManageResourceOwner(ownerSystemAccountId, access)) throw new Error('授权资源不存在')
    await validateResourceAuthorizationExpiresAtAsync(tx, resourceType, resourceId, expiresAt, Date.parse(now))
    if (granteeType === 'team') {
      const team = await tx.one<SystemTeamRow>(`
        SELECT *
        FROM ${resourceAuthorizationWriteTable(tx, 'system_teams')}
        WHERE id = ?
          AND status = 'active'
        LIMIT 1
      `, [granteeId])
      if (!team) throw new Error('团队不存在或已停用')
      const members = (await activeTeamMemberRowsAsync(granteeId, tx)).filter((member) => member.system_account_id !== ownerSystemAccountId)
      if (!members.length) throw new Error('团队暂无可授权成员，请先添加非归属人成员后再授权')
      const grant = await upsertResourceAuthorizationGrantAsync({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark, expiresAt, limits: input.limits, actor, now, client: tx })
      await assertActiveTeamGrantFanoutWithinLimitAsync(granteeId, tx)
      createdGrantId = grant.id
      for (const member of members) {
        await upsertResourceAuthorizationForUserAsync({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: member.system_account_id, sourceType: 'team', sourceTeamId: granteeId, remark, expiresAt, limits: input.limits, actor, now, client: tx })
      }
      return
    }
    const grantee = await tx.one<{ id?: string; status?: string }>(`
      SELECT id, status
      FROM ${resourceAuthorizationWriteTable(tx, 'system_accounts')}
      WHERE id = ?
      LIMIT 1
    `, [granteeId])
    if (!grantee || grantee.status !== 'active') throw new Error('被授权用户不存在或已停用')
    if (granteeId === ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
    const grant = await upsertResourceAuthorizationGrantAsync({ resourceType, resourceId, ownerSystemAccountId, granteeType, granteeId, remark, expiresAt, limits: input.limits, actor, now, client: tx })
    createdGrantId = grant.id
    await upsertResourceAuthorizationForUserAsync({ resourceType, resourceId, ownerSystemAccountId, granteeSystemAccountId: granteeId, sourceType: 'manual', targetGroupId, remark, expiresAt, limits: input.limits, actor, now, client: tx })
  })

  refreshAfterResourceAuthorizationBusinessWrite('resource_authorization_created')
  const created = createdGrantId ? await findResourceAuthorizationAfterWriteAsync(createdGrantId, access) : undefined
  if (created) return created
  throw new Error('创建资源授权失败')
}

export function revokeResourceAuthorization(authorizationId: string, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  const database = getBusinessDatabase()
  const grant = database.prepare('SELECT * FROM resource_authorization_grants WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationGrantRow | undefined
  if (!grant || !canManageResourceOwner(grant.resource_owner_system_account_id, access)) return undefined
  const now = nowIso()
  const actor = currentSystemAccountId(access)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    revokeResourceAuthorizationGrant(grant, actor, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_revoked' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_revoked')
  return findResourceAuthorizationAfterWrite(authorizationId, access)
}

export async function revokeResourceAuthorizationAsync(authorizationId: string, access?: AccessScope): Promise<ResourceAuthorizationSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return revokeResourceAuthorization(authorizationId, access)
  }
  const client = await getResourceAuthorizationWriteClient()
  const actor = currentSystemAccountId(access)
  const now = nowIso()
  const revoked = await client.transaction(async (tx) => {
    const grant = await tx.one<ResourceAuthorizationGrantRow>(`
      SELECT *
      FROM ${resourceAuthorizationWriteTable(tx, 'resource_authorization_grants')}
      WHERE id = ?
      LIMIT 1
    `, [authorizationId])
    if (!grant || !canManageResourceOwner(grant.resource_owner_system_account_id, access)) return false
    await revokeResourceAuthorizationGrantAsync(grant, actor, tx, now)
    return true
  })
  if (!revoked) return undefined
  refreshAfterResourceAuthorizationBusinessWrite('resource_authorization_revoked')
  return findResourceAuthorizationAfterWriteAsync(authorizationId, access)
}

export function updateResourceAuthorization(authorizationId: string, input: Record<string, unknown> = {}, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  assertKnownInputKeys(input, resourceAuthorizationUpdateInputKeys, '资源授权')
  expireDueResourceAuthorizations()
  const database = getBusinessDatabase()
  const grant = database.prepare('SELECT * FROM resource_authorization_grants WHERE id = ?').get(authorizationId) as unknown as ResourceAuthorizationGrantRow | undefined
  if (!grant || !canManageResourceOwner(grant.resource_owner_system_account_id, access)) return undefined
  const now = nowIso()
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
  const hasLimitsInput = Object.prototype.hasOwnProperty.call(input, 'limits')
  const nextExpiresAt = hasExpiresAtInput
    ? normalizeResourceAuthorizationExpiresAtInput(input.expiresAt)
    : grant.expires_at
  const nextLimits = hasLimitsInput
    ? requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
    : grant.limits_json
  const requestedStatus = Object.prototype.hasOwnProperty.call(input, 'status')
    ? normalizeResourceAuthorizationStatus(input.status)
    : undefined
  validateResourceAuthorizationExpiresAt(grant.resource_type, grant.resource_id, nextExpiresAt, Date.parse(now), { allowExpired: requestedStatus === 'expired' })
  if (grant.status === 'expired' && requestedStatus === 'active' && !hasExpiresAtInput) {
    throw new Error('到期授权恢复时请同时调整过期时间')
  }
  const expiredByTime = isResourceAuthorizationExpired(nextExpiresAt)
  const nextStatus: AuthorizationStatus = expiredByTime
    ? 'expired'
    : requestedStatus === 'active' || requestedStatus === 'paused' || requestedStatus === 'revoked' || requestedStatus === 'returned'
      ? requestedStatus
      : grant.status === 'expired' && hasExpiresAtInput
        ? 'active'
        : grant.status === 'paused'
          ? 'paused'
        : grant.status
  const nextRevokedAt = nextStatus === 'active' || nextStatus === 'paused' ? null : grant.revoked_at ?? now
  const nextRevokedBy = nextStatus === 'active' || nextStatus === 'paused' ? null : grant.revoked_by ?? currentSystemAccountId(access)

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        UPDATE resource_authorization_grants
        SET status = ?,
            expires_at = ?,
            revoked_by = ?,
            revoked_at = ?,
            limits_json = ?,
            updated_at = ?
        WHERE id = ?
      `)
      .run(nextStatus, nextExpiresAt, nextRevokedBy, nextRevokedAt, nextLimits, now, authorizationId)
    syncResourceAuthorizationGrantRuntime({ ...grant, status: nextStatus, expires_at: nextExpiresAt, limits_json: nextLimits, revoked_by: nextRevokedBy, revoked_at: nextRevokedAt, updated_at: now }, currentSystemAccountId(access), database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  cleanupInactiveAuthorizationBindings(database)
  refreshGroupAccountStatsAfterWrite({ all: true, reason: 'resource_authorization_updated' })
  invalidateAuthorizationRuntimeAfterBusinessWrite('resource_authorization_updated')
  return findResourceAuthorizationAfterWrite(authorizationId, access)
}

export async function updateResourceAuthorizationAsync(authorizationId: string, input: Record<string, unknown> = {}, access?: AccessScope): Promise<ResourceAuthorizationSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return updateResourceAuthorization(authorizationId, input, access)
  }
  assertKnownInputKeys(input, resourceAuthorizationUpdateInputKeys, '资源授权')
  const client = await getResourceAuthorizationWriteClient()
  const actor = currentSystemAccountId(access)
  const now = nowIso()
  const updated = await client.transaction(async (tx) => {
    const grant = await tx.one<ResourceAuthorizationGrantRow>(`
      SELECT *
      FROM ${resourceAuthorizationWriteTable(tx, 'resource_authorization_grants')}
      WHERE id = ?
      LIMIT 1
    `, [authorizationId])
    if (!grant || !canManageResourceOwner(grant.resource_owner_system_account_id, access)) return false
    const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
    const hasLimitsInput = Object.prototype.hasOwnProperty.call(input, 'limits')
    const nextExpiresAt = hasExpiresAtInput
      ? normalizeResourceAuthorizationExpiresAtInput(input.expiresAt)
      : grant.expires_at
    const nextLimits = hasLimitsInput
      ? requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
      : grant.limits_json
    const requestedStatus = Object.prototype.hasOwnProperty.call(input, 'status')
      ? normalizeResourceAuthorizationStatus(input.status)
      : undefined
    await validateResourceAuthorizationExpiresAtAsync(tx, grant.resource_type, grant.resource_id, nextExpiresAt, Date.parse(now), { allowExpired: requestedStatus === 'expired' })
    if (grant.status === 'expired' && requestedStatus === 'active' && !hasExpiresAtInput) {
      throw new Error('到期授权恢复时请同时调整过期时间')
    }
    const expiredByTime = isResourceAuthorizationExpired(nextExpiresAt)
    const nextStatus: AuthorizationStatus = expiredByTime
      ? 'expired'
      : requestedStatus === 'active' || requestedStatus === 'paused' || requestedStatus === 'revoked' || requestedStatus === 'returned'
        ? requestedStatus
        : grant.status === 'expired' && hasExpiresAtInput
          ? 'active'
          : grant.status === 'paused'
            ? 'paused'
            : grant.status
    const nextRevokedAt = nextStatus === 'active' || nextStatus === 'paused' ? null : grant.revoked_at ?? now
    const nextRevokedBy = nextStatus === 'active' || nextStatus === 'paused' ? null : grant.revoked_by ?? actor
    await tx.execute(`
      UPDATE ${resourceAuthorizationWriteTable(tx, 'resource_authorization_grants')}
      SET status = ?,
          expires_at = ?,
          revoked_by = ?,
          revoked_at = ?,
          limits_json = ?,
          updated_at = ?
      WHERE id = ?
    `, [nextStatus, nextExpiresAt, nextRevokedBy, nextRevokedAt, nextLimits, now, authorizationId])
    await syncResourceAuthorizationGrantRuntimeAsync({
      ...grant,
      status: nextStatus,
      expires_at: nextExpiresAt,
      limits_json: nextLimits,
      revoked_by: nextRevokedBy,
      revoked_at: nextRevokedAt,
      updated_at: now
    }, actor, tx, now)
    return true
  })
  if (!updated) return undefined
  refreshAfterResourceAuthorizationBusinessWrite('resource_authorization_updated')
  return findResourceAuthorizationAfterWriteAsync(authorizationId, access)
}

function findResourceAuthorizationAfterWrite(authorizationId: string, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  expireDueResourceAuthorizations()
  return findResourceAuthorizationSummary(authorizationId, access)
}

async function findResourceAuthorizationAfterWriteAsync(authorizationId: string, access?: AccessScope): Promise<ResourceAuthorizationSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findResourceAuthorizationAfterWrite(authorizationId, access)
  }
  return findResourceAuthorizationSummaryAsync(authorizationId, access, { includeUsage: false })
}

async function revokeResourceAuthorizationGrantAsync(grant: ResourceAuthorizationGrantRow, actor: string, client: DatabaseClient, now: string): Promise<void> {
  await client.execute(`
    UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorization_grants')}
    SET status = 'revoked',
        revoked_by = ?,
        revoked_at = ?,
        updated_at = ?
    WHERE id = ?
  `, [actor, now, now, grant.id])
  await syncResourceAuthorizationGrantRuntimeAsync({ ...grant, status: 'revoked', revoked_by: actor, revoked_at: now, updated_at: now }, actor, client, now)
}

async function syncResourceAuthorizationGrantRuntimeAsync(grant: ResourceAuthorizationGrantRow, actor: string, client: DatabaseClient, now: string): Promise<void> {
  await rememberRequestQuotaHourlyWindowsFromJsonAsync(client, grant.limits_json, now)
  if (grant.grantee_type === 'system_account') {
    await syncUserGrantRuntimeAsync(grant, actor, client, now)
    return
  }
  await syncTeamGrantMemberAuthorizationsAsync(grant, actor, client, now)
}

async function upsertResourceAuthorizationGrantAsync(input: {
  resourceType: ResourceAuthorizationResourceType
  resourceId: string
  ownerSystemAccountId: string
  granteeType: 'system_account' | 'team'
  granteeId: string
  remark?: string
  expiresAt?: string | null
  limits?: unknown
  actor: string
  now: string
  client: DatabaseClient
}): Promise<ResourceAuthorizationGrantRow> {
  if (input.granteeType === 'system_account' && input.granteeId === input.ownerSystemAccountId) {
    throw new Error('不能授权给资源所有者自己')
  }
  const active = await input.client.one<{ id?: string }>(`
    SELECT id
    FROM ${resourceAuthorizationWriteTable(input.client, 'resource_authorization_grants')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_type = ?
      AND COALESCE(grantee_system_account_id, '') = COALESCE(?, '')
      AND COALESCE(grantee_team_id, '') = COALESCE(?, '')
      AND status = 'active'
    LIMIT 1
  `, [
    input.resourceType,
    input.resourceId,
    input.granteeType,
    input.granteeType === 'system_account' ? input.granteeId : null,
    input.granteeType === 'team' ? input.granteeId : null
  ])
  if (active?.id) {
    throw new Error(input.granteeType === 'team' ? '该资源已授权给该团队，请勿重复授权' : '该资源已授权给该用户，请勿重复授权')
  }
  const existing = await input.client.one<ResourceAuthorizationGrantRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(input.client, 'resource_authorization_grants')}
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
  `, [
    input.resourceType,
    input.resourceId,
    input.granteeType,
    input.granteeType === 'system_account' ? input.granteeId : null,
    input.granteeType === 'team' ? input.granteeId : null
  ])
  const id = existing?.id ?? newId('rauthgrant')
  const nextExpiresAt = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
    ? input.expiresAt ?? null
    : existing?.expires_at ?? null
  const nextLimitsJson = requestQuotaLimitsJson(normalizeRequestQuotaLimits(input.limits))
  if (existing) {
    await input.client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(input.client, 'resource_authorization_grants')}
      SET status = 'active',
          remark = COALESCE(?, remark),
          expires_at = ?,
          limits_json = ?,
          revoked_by = NULL,
          revoked_at = NULL,
          updated_at = ?
      WHERE id = ?
    `, [input.remark ?? null, nextExpiresAt, nextLimitsJson, input.now, id])
  } else {
    await input.client.execute(`
      INSERT INTO ${resourceAuthorizationWriteTable(input.client, 'resource_authorization_grants')} (
        id, resource_type, resource_id, resource_owner_system_account_id, grantee_type,
        grantee_system_account_id, grantee_team_id, scope, status, remark, expires_at,
        limits_json, created_by, created_at, revoked_by, revoked_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, 'use', 'active', ?, ?, ?, ?, ?, NULL, NULL, ?)
    `, [
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
    ])
  }
  await rememberRequestQuotaHourlyWindowsFromJsonAsync(input.client, nextLimitsJson, input.now)
  return {
    id,
    resource_type: input.resourceType,
    resource_id: input.resourceId,
    resource_owner_system_account_id: input.ownerSystemAccountId,
    grantee_type: input.granteeType,
    grantee_system_account_id: input.granteeType === 'system_account' ? input.granteeId : null,
    grantee_team_id: input.granteeType === 'team' ? input.granteeId : null,
    scope: 'use',
    status: 'active',
    remark: input.remark ?? existing?.remark ?? null,
    expires_at: nextExpiresAt,
    limits_json: nextLimitsJson,
    created_by: existing?.created_by ?? input.actor,
    created_at: existing?.created_at ?? input.now,
    revoked_by: null,
    revoked_at: null,
    updated_at: input.now
  }
}

async function upsertResourceAuthorizationForUserAsync(input: {
  resourceType: ResourceAuthorizationResourceType
  resourceId: string
  ownerSystemAccountId: string
  granteeSystemAccountId: string
  sourceType: ResourceAuthorizationSourceType
  sourceTeamId?: string
  targetGroupId?: string
  remark?: string
  expiresAt?: string | null
  limits?: unknown
  actor: string
  now: string
  client: DatabaseClient
}): Promise<ResourceAuthorizationRow> {
  if (input.granteeSystemAccountId === input.ownerSystemAccountId) throw new Error('不能授权给资源所有者自己')
  const existing = await input.client.one<ResourceAuthorizationRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(input.client, 'resource_authorizations')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `, [input.resourceType, input.resourceId, input.granteeSystemAccountId])
  const authorizationId = existing?.id ?? newId('rauth')
  const isTeamSource = input.sourceType === 'team'
  const hasActiveTeamSource = existing ? await hasActiveTeamAuthorizationSourceAsync(input.client, authorizationId, input.now) : false
  const nextEffectiveSourceType = isTeamSource || hasActiveTeamSource ? 'team' : 'manual'
  const nextEffectiveSourceTeamId = isTeamSource ? input.sourceTeamId ?? null : await firstActiveTeamSourceIdAsync(input.client, authorizationId, input.now)
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
    await input.client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(input.client, 'resource_authorizations')}
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
    `, [
      input.ownerSystemAccountId,
      nextStatus,
      nextEffectiveSourceType,
      nextEffectiveSourceTeamId,
      input.now,
      input.now,
      input.remark ?? null,
      nextExpiresAt,
      nextLimitsJson,
      nextRevokedBy,
      nextRevokedAt,
      nextRevokedReason,
      input.now,
      authorizationId
    ])
  } else {
    await input.client.execute(`
      INSERT INTO ${resourceAuthorizationWriteTable(input.client, 'resource_authorizations')} (
        id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id, scope, status,
        effective_source_type, effective_source_team_id, activated_at, last_source_changed_at,
        remark, expires_at, limits_json,
        created_by, created_at, revoked_by, revoked_at, revoked_reason, updated_at
      ) VALUES (?, ?, ?, ?, ?, 'use', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [
      authorizationId,
      input.resourceType,
      input.resourceId,
      input.ownerSystemAccountId,
      input.granteeSystemAccountId,
      nextStatus,
      nextEffectiveSourceType,
      nextEffectiveSourceTeamId,
      input.now,
      input.now,
      input.remark ?? null,
      nextExpiresAt,
      nextLimitsJson,
      input.actor,
      input.now,
      nextRevokedBy,
      nextRevokedAt,
      nextRevokedReason,
      input.now
    ])
  }
  await rememberRequestQuotaHourlyWindowsFromJsonAsync(input.client, nextLimitsJson, input.now)
  await upsertResourceAuthorizationSourceAsync(input.client, authorizationId, input.sourceType, input.sourceTeamId, input.actor, input.now, isTeamSource ? 'active' : hasActiveTeamSource ? 'superseded' : 'active')
  if (isTeamSource) {
    await input.client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(input.client, 'resource_authorization_sources')}
      SET status = 'superseded',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'covered_by_team'),
          updated_at = ?
      WHERE authorization_id = ?
        AND source_type = 'manual'
        AND status = 'active'
    `, [input.now, input.now, authorizationId])
  }
  await refreshResourceAuthorizationEffectiveSourceAsync(authorizationId, input.actor, input.now, input.client)
  const row = await input.client.one<ResourceAuthorizationRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(input.client, 'resource_authorizations')}
    WHERE id = ?
    LIMIT 1
  `, [authorizationId])
  if (!row) throw new Error('创建资源授权失败')
  await bindActiveAccountAuthorizationToGranteeGroupAsync(input.client, row, input.now, input.targetGroupId)
  return row
}

async function syncUserGrantRuntimeAsync(grant: ResourceAuthorizationGrantRow, actor: string, client: DatabaseClient, now: string): Promise<void> {
  const granteeSystemAccountId = grant.grantee_system_account_id
  if (!granteeSystemAccountId) return
  const runtime = await loadRuntimeAuthorizationForUserGrantAsync(grant, client)
  if (grant.status === 'active') {
    const authorizationId = runtime?.id ?? newId('rauth')
    if (runtime) {
      await client.execute(`
        UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
        SET resource_owner_system_account_id = ?,
            status = 'active',
            activated_at = COALESCE(activated_at, ?),
            last_source_changed_at = ?,
            remark = COALESCE(?, remark),
            expires_at = ?,
            limits_json = ?,
            revoked_by = NULL,
            revoked_at = NULL,
            revoked_reason = NULL,
            updated_at = ?
        WHERE id = ?
      `, [grant.resource_owner_system_account_id, now, now, grant.remark, grant.expires_at, grant.limits_json, now, authorizationId])
    } else {
      await client.execute(`
        INSERT INTO ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} (
          id, resource_type, resource_id, resource_owner_system_account_id, grantee_system_account_id,
          scope, status, effective_source_type, effective_source_team_id, activated_at,
          last_source_changed_at, remark, expires_at, limits_json, created_by, created_at,
          revoked_by, revoked_at, revoked_reason, updated_at
        ) VALUES (?, ?, ?, ?, ?, 'use', 'active', 'manual', NULL, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?)
      `, [
        authorizationId,
        grant.resource_type,
        grant.resource_id,
        grant.resource_owner_system_account_id,
        granteeSystemAccountId,
        now,
        now,
        grant.remark,
        grant.expires_at,
        grant.limits_json,
        actor,
        now,
        now
      ])
    }
    await upsertResourceAuthorizationSourceAsync(client, authorizationId, 'manual', undefined, actor, now, 'active')
    await refreshResourceAuthorizationEffectiveSourceAsync(authorizationId, actor, now, client)
    return
  }
  if (!runtime) return
  if (grant.status === 'paused' || grant.status === 'expired') {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
      SET status = ?,
          expires_at = ?,
          limits_json = ?,
          revoked_by = CASE WHEN ? = 'expired' THEN COALESCE(revoked_by, ?) ELSE NULL END,
          revoked_at = CASE WHEN ? = 'expired' THEN COALESCE(revoked_at, ?) ELSE NULL END,
          revoked_reason = CASE WHEN ? = 'expired' THEN 'authorization_expired' ELSE 'authorization_paused' END,
          updated_at = ?
      WHERE id = ?
    `, [grant.status, grant.expires_at, grant.limits_json, grant.status, actor, grant.status, now, grant.status, now, runtime.id])
    await refreshResourceAuthorizationEffectiveSourceAsync(runtime.id, actor, now, client)
    return
  }
  await client.execute(`
    UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
    SET status = 'revoked',
        ended_at = COALESCE(ended_at, ?),
        ended_reason = COALESCE(ended_reason, ?),
        revoked_by = ?,
        revoked_at = ?,
        updated_at = ?
    WHERE authorization_id = ?
      AND source_type = 'manual'
      AND status IN ('active', 'superseded')
  `, [now, grant.status === 'returned' ? 'grantee_returned' : 'authorization_revoked', actor, now, now, runtime.id])
  await refreshResourceAuthorizationEffectiveSourceAsync(runtime.id, actor, now, client, {
    noActiveSourceReason: grant.status === 'returned' ? 'grantee_returned' : 'authorization_revoked',
    preserveExpiredWhenNoActiveSource: false,
    terminalStatus: grant.status === 'returned' ? 'returned' : 'revoked'
  })
}

async function syncTeamGrantMemberAuthorizationsAsync(grant: ResourceAuthorizationGrantRow, actor: string, client: DatabaseClient, now: string): Promise<void> {
  const teamId = grant.grantee_team_id
  if (!teamId) return
  if (grant.status === 'revoked' || grant.status === 'returned') {
    await revokeTeamGrantSourcesAsync(grant.resource_type, grant.resource_id, teamId, actor, client, now)
    return
  }
  const rows = await client.query<{ id?: string }>(`
    SELECT ra.id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} ra
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} ras ON ras.authorization_id = ra.id
    WHERE ra.resource_type = ?
      AND ra.resource_id = ?
      AND ras.source_type = 'team'
      AND ras.source_team_id = ?
      AND ras.status = 'active'
    ORDER BY ra.id ASC
    LIMIT ?
  `, [grant.resource_type, grant.resource_id, teamId, 1001])
  if (rows.length > 1000) {
    throw new Error('授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权')
  }
  for (const row of rows) {
    if (!row.id) continue
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
      SET expires_at = ?,
          revoked_by = CASE WHEN ? IN ('active', 'paused') THEN NULL ELSE ? END,
          revoked_at = CASE WHEN ? IN ('active', 'paused') THEN NULL ELSE ? END,
          revoked_reason = CASE WHEN ? = 'expired' THEN 'authorization_expired' WHEN ? = 'paused' THEN 'authorization_paused' ELSE NULL END,
          limits_json = ?,
          updated_at = ?
      WHERE id = ?
    `, [grant.expires_at, grant.status, grant.revoked_by, grant.status, grant.revoked_at, grant.status, grant.status, grant.limits_json, now, row.id])
    await refreshResourceAuthorizationEffectiveSourceAsync(row.id, actor, now, client)
  }
}

async function revokeTeamGrantSourcesAsync(
  resourceType: ResourceAuthorizationResourceType,
  resourceId: string,
  teamId: string,
  actor: string,
  client: DatabaseClient,
  now: string
): Promise<void> {
  const rows = await client.query<{ authorization_id: string }>(`
    SELECT ras.authorization_id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    WHERE ras.source_type = 'team'
      AND ras.source_team_id = ?
      AND ras.status = 'active'
      AND ra.resource_type = ?
      AND ra.resource_id = ?
    ORDER BY ras.authorization_id ASC
    LIMIT ?
  `, [teamId, resourceType, resourceId, 1001])
  if (rows.length > 1000) {
    throw new Error('授权团队最多支持 1000 个成员，请先移除部分成员后再继续')
  }
  for (const row of rows) {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'team_revoked'),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ?
        AND source_type = 'team'
        AND source_team_id = ?
        AND status = 'active'
    `, [now, actor, now, now, row.authorization_id, teamId])
    await refreshResourceAuthorizationEffectiveSourceAsync(row.authorization_id, actor, now, client, {
      noActiveSourceReason: 'authorization_revoked',
      preserveExpiredWhenNoActiveSource: false
    })
  }
}

async function loadRuntimeAuthorizationForUserGrantAsync(grant: ResourceAuthorizationGrantRow, client: DatabaseClient): Promise<ResourceAuthorizationRow | undefined> {
  if (!grant.grantee_system_account_id) return undefined
  return client.one<ResourceAuthorizationRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `, [grant.resource_type, grant.resource_id, grant.grantee_system_account_id])
}

async function upsertResourceAuthorizationSourceAsync(
  client: DatabaseClient,
  authorizationId: string,
  sourceType: 'manual' | 'team',
  sourceTeamId: string | undefined,
  actor: string,
  now: string,
  requestedStatus: 'active' | 'superseded' | 'revoked'
): Promise<void> {
  const existing = await client.one<ResourceAuthorizationSourceRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
    WHERE authorization_id = ?
      AND source_type = ?
      AND COALESCE(source_team_id, '') = COALESCE(?, '')
    ORDER BY created_at DESC, id DESC
    LIMIT 1
  `, [authorizationId, sourceType, sourceTeamId ?? null])
  if (existing) {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
      SET status = ?,
          activated_at = COALESCE(activated_at, ?),
          ended_at = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_at, ?) END,
          ended_reason = CASE WHEN ? = 'active' THEN NULL ELSE COALESCE(ended_reason, ?) END,
          revoked_by = CASE WHEN ? = 'active' THEN NULL ELSE revoked_by END,
          revoked_at = CASE WHEN ? = 'active' THEN NULL ELSE revoked_at END,
          updated_at = ?
      WHERE id = ?
    `, [
      requestedStatus,
      now,
      requestedStatus,
      now,
      requestedStatus,
      requestedStatus === 'superseded' ? 'covered_by_team' : null,
      requestedStatus,
      requestedStatus,
      now,
      existing.id
    ])
    return
  }
  await client.execute(`
    INSERT INTO ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} (
      id, authorization_id, source_type, source_team_id, status, activated_at, ended_at,
      ended_reason, created_by, created_at, revoked_by, revoked_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?)
  `, [
    newId('rauthsrc'),
    authorizationId,
    sourceType,
    sourceTeamId ?? null,
    requestedStatus,
    now,
    requestedStatus === 'active' ? null : now,
    requestedStatus === 'superseded' ? 'covered_by_team' : null,
    actor,
    now,
    now
  ])
}

async function resourceOwnerSystemAccountIdAsync(client: DatabaseClient, resourceType: ResourceAuthorizationResourceType, resourceId: string): Promise<string | undefined> {
  if (resourceType !== 'account') {
    const row = await client.one<{ system_account_id?: string | null }>(`
      SELECT system_account_id
      FROM ${resourceAuthorizationWriteTable(client, 'groups')}
      WHERE id = ?
      LIMIT 1
    `, [resourceId])
    return row?.system_account_id ?? undefined
  }
  const row = await client.one<{ system_account_id?: string | null; authorization_instance_authorization_id?: string | null }>(`
    SELECT system_account_id, authorization_instance_authorization_id
    FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
    WHERE id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [resourceId])
  if (!row?.system_account_id || row.authorization_instance_authorization_id) return undefined
  return row.system_account_id
}

async function activeTeamMemberRowsAsync(teamId: string, client: DatabaseClient): Promise<SystemTeamMemberRow[]> {
  const rows = await client.query<SystemTeamMemberRow>(`
    SELECT system_team_members.*
    FROM ${resourceAuthorizationWriteTable(client, 'system_team_members')} system_team_members
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = system_team_members.system_account_id
    WHERE system_team_members.team_id = ?
      AND system_team_members.status = 'active'
      AND system_accounts.status = 'active'
    ORDER BY system_team_members.joined_at ASC, system_team_members.id ASC
    LIMIT ?
  `, [teamId, maxSystemTeamMembersPerTeam + 1])
  if (rows.length > maxSystemTeamMembersPerTeam) {
    throw new Error(`授权团队最多支持 ${maxSystemTeamMembersPerTeam} 个成员，请先移除部分成员后再继续`)
  }
  return rows
}

async function activeTeamGrantRowsAsync(teamId: string, client: DatabaseClient): Promise<ResourceAuthorizationGrantRow[]> {
  const rows = await client.query<ResourceAuthorizationGrantRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_grants')}
    WHERE grantee_type = 'team'
      AND grantee_team_id = ?
      AND status = 'active'
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [teamId, maxSystemTeamActiveGrantCount + 1])
  if (rows.length > maxSystemTeamActiveGrantCount) {
    throw new Error(`单个授权团队最多支持 ${maxSystemTeamActiveGrantCount} 条有效授权，请先回收或停用部分授权`)
  }
  return rows
}

export async function applyActiveTeamGrantsToMemberAsync(teamId: string, systemAccountId: string, access: AccessScope | undefined, client: DatabaseClient, now: string): Promise<void> {
  const grants = await activeTeamGrantRowsAsync(teamId, client)
  const actor = currentSystemAccountId(access)
  for (const grant of grants) {
    if (grant.resource_owner_system_account_id === systemAccountId) continue
    await upsertResourceAuthorizationForUserAsync({
      resourceType: grant.resource_type,
      resourceId: grant.resource_id,
      ownerSystemAccountId: grant.resource_owner_system_account_id,
      granteeSystemAccountId: systemAccountId,
      sourceType: 'team',
      sourceTeamId: teamId,
      remark: grant.remark ?? undefined,
      expiresAt: grant.expires_at,
      limits: parseRequestQuotaLimitsJson(grant.limits_json),
      actor,
      now,
      client
    })
  }
}

export async function revokeTeamSourcesForMemberAsync(teamId: string, systemAccountId: string, actor: string, client: DatabaseClient, now: string): Promise<void> {
  const rows = await client.query<{ authorization_id: string }>(`
    SELECT ras.authorization_id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    WHERE ras.source_type = 'team'
      AND ras.source_team_id = ?
      AND ras.status = 'active'
      AND ra.grantee_system_account_id = ?
    ORDER BY ras.authorization_id ASC
    LIMIT ?
  `, [teamId, systemAccountId, maxSystemTeamActiveGrantCount + 1])
  if (rows.length > maxSystemTeamActiveGrantCount) {
    throw new Error(`单个授权团队最多支持 ${maxSystemTeamActiveGrantCount} 条有效授权，请先回收或停用部分授权`)
  }
  for (const row of rows) {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, 'member_removed'),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ?
        AND source_type = 'team'
        AND source_team_id = ?
        AND status = 'active'
    `, [now, actor, now, now, row.authorization_id, teamId])
    await refreshResourceAuthorizationEffectiveSourceAsync(row.authorization_id, actor, now, client)
  }
}

export async function revokeAllTeamSourcesAsync(teamId: string, actor: string, client: DatabaseClient, now: string, reason: string): Promise<void> {
  const rows = await client.query<{ authorization_id: string }>(`
    SELECT DISTINCT authorization_id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
    WHERE source_type = 'team'
      AND source_team_id = ?
      AND status = 'active'
    ORDER BY authorization_id ASC
    LIMIT ?
  `, [teamId, maxSystemTeamMembersPerTeam * maxSystemTeamActiveGrantCount + 1])
  if (rows.length > maxSystemTeamMembersPerTeam * maxSystemTeamActiveGrantCount) {
    throw new Error(`授权团队来源展开超过当前系统上限，请先拆分团队或回收部分授权`)
  }
  for (const row of rows) {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
      SET status = 'revoked',
          ended_at = COALESCE(ended_at, ?),
          ended_reason = COALESCE(ended_reason, ?),
          revoked_by = ?,
          revoked_at = ?,
          updated_at = ?
      WHERE authorization_id = ?
        AND source_type = 'team'
        AND source_team_id = ?
        AND status = 'active'
    `, [now, reason, actor, now, now, row.authorization_id, teamId])
    await refreshResourceAuthorizationEffectiveSourceAsync(row.authorization_id, actor, now, client)
  }
}

export async function reactivateTeamGrantSourcesAsync(teamId: string, access: AccessScope | undefined, client: DatabaseClient, now: string): Promise<void> {
  const memberRows = await activeTeamMemberRowsAsync(teamId, client)
  for (const member of memberRows) {
    await applyActiveTeamGrantsToMemberAsync(teamId, member.system_account_id, access, client, now)
  }
}

async function assertActiveTeamGrantFanoutWithinLimitAsync(teamId: string, client: DatabaseClient): Promise<void> {
  const rows = await client.query<{ id?: string }>(`
    SELECT id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_grants')}
    WHERE grantee_type = 'team'
      AND grantee_team_id = ?
      AND status = 'active'
    ORDER BY created_at ASC, id ASC
    LIMIT ?
  `, [teamId, maxSystemTeamActiveGrantCount + 1])
  if (rows.length > maxSystemTeamActiveGrantCount) {
    throw new Error(`单个授权团队最多支持 ${maxSystemTeamActiveGrantCount} 条有效授权，请先回收或停用部分授权`)
  }
}

async function hasActiveTeamAuthorizationSourceAsync(client: DatabaseClient, authorizationId: string, now: string): Promise<boolean> {
  const row = await client.one<{ id?: string }>(`
    SELECT ras.id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorization_grants')} trg
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
  `, [now, authorizationId])
  return Boolean(row?.id)
}

async function firstActiveTeamSourceIdAsync(client: DatabaseClient, authorizationId: string, now: string): Promise<string | null> {
  const row = await client.one<{ source_team_id?: string | null }>(`
    SELECT ras.source_team_id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorization_grants')} trg
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
  `, [now, authorizationId])
  return row?.source_team_id ?? null
}

async function bindActiveAccountAuthorizationToGranteeGroupAsync(client: DatabaseClient, authorization: ResourceAuthorizationRow, now: string, targetGroupId?: string): Promise<void> {
  if (authorization.resource_type !== 'account') return
  if (authorization.status !== 'active' || isResourceAuthorizationExpired(authorization.expires_at, Date.parse(now))) return
  const instance = await ensureAccountAuthorizationInstanceAsync(client, authorization, now)
  if (!instance?.id || !instance.provider_protocol_profile_id) return
  const requestedGroupId = targetGroupId?.trim()
  const existingBinding = await client.one<{ group_id?: string | null }>(`
    SELECT group_id
    FROM ${resourceAuthorizationWriteTable(client, 'group_accounts')}
    WHERE account_id = ?
      AND system_account_id = ?
      AND account_authorization_id = ?
      AND enabled = 1
    ORDER BY updated_at DESC, group_id ASC, account_id ASC
    LIMIT 1
  `, [instance.id, authorization.grantee_system_account_id, authorization.id])
  if (existingBinding?.group_id && (!requestedGroupId || existingBinding.group_id === requestedGroupId)) return
  const bindGroupId = await groupIdForAuthorizationBindingAsync(client, instance.provider_protocol_profile_id, authorization.grantee_system_account_id, requestedGroupId)
  if (!bindGroupId) return
  if (existingBinding?.group_id && existingBinding.group_id !== bindGroupId) {
    await client.execute(`
      DELETE FROM ${resourceAuthorizationWriteTable(client, 'group_accounts')}
      WHERE account_id = ?
        AND system_account_id = ?
        AND account_authorization_id = ?
    `, [instance.id, authorization.grantee_system_account_id, authorization.id])
    invalidateGroupAccountIdsCache(existingBinding.group_id)
  }
  await client.execute(`
    INSERT INTO ${resourceAuthorizationWriteTable(client, 'group_accounts')} (
      system_account_id, group_id, account_id, account_authorization_id, enabled, created_at, updated_at
    ) VALUES (?, ?, ?, ?, 1, ?, ?)
    ON CONFLICT(group_id, account_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      account_authorization_id = excluded.account_authorization_id,
      enabled = 1,
      updated_at = excluded.updated_at
  `, [authorization.grantee_system_account_id, bindGroupId, instance.id, authorization.id, now, now])
  invalidateGroupAccountIdsCache(bindGroupId)
}

async function ensureAccountAuthorizationInstanceAsync(client: DatabaseClient, authorization: ResourceAuthorizationRow, now: string): Promise<AccountRow | undefined> {
  const existing = await client.one<AccountRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
    WHERE authorization_instance_authorization_id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [authorization.id])
  if (existing) return existing
  const source = await client.one<AccountRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
    WHERE id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [authorization.resource_id])
  if (!source || source.system_account_id === authorization.grantee_system_account_id) return undefined
  const deletedExisting = await client.one<AccountRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
    WHERE authorization_instance_authorization_id = ?
      AND deleted_at IS NOT NULL
    ORDER BY deleted_at DESC, updated_at DESC, id ASC
    LIMIT 1
  `, [authorization.id])
  if (deletedExisting?.id) {
    const restoredName = await uniqueAuthorizedAccountInstanceNameAsync(client, source.name, authorization.grantee_system_account_id, authorization.id, deletedExisting.id)
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'accounts')}
      SET provider_code = ?,
          provider_protocol_profile_id = ?,
          protocol_code = ?,
          protocol_version = ?,
          name = ?,
          type = ?,
          status = 'active',
          schedulable = 1,
          cooldown_until = NULL,
          last_error_code = NULL,
          last_error_message = NULL,
          cooldown_retest_failure_count = 0,
          cooldown_retest_observation_started_at = NULL,
          cooldown_retest_last_at = NULL,
          cooldown_retest_last_status_code = NULL,
          stream_failure_count = 0,
          stream_failure_window_started_at = NULL,
          authorization_instance_source_account_id = ?,
          authorization_instance_owner_system_account_id = ?,
          deleted_at = NULL,
          deleted_by = NULL,
          updated_at = ?
      WHERE id = ?
    `, [
      source.provider_code,
      source.provider_protocol_profile_id,
      source.protocol_code,
      source.protocol_version,
      restoredName,
      source.type,
      authorization.resource_id,
      authorization.resource_owner_system_account_id,
      now,
      deletedExisting.id
    ])
    await replaceAccountNameSearchTermsAsync(client, deletedExisting.id, authorization.grantee_system_account_id, restoredName, now)
    return client.one<AccountRow>(`
      SELECT *
      FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
      WHERE id = ?
        AND deleted_at IS NULL
      LIMIT 1
    `, [deletedExisting.id])
  }
  const id = newId('acc')
  const name = await uniqueAuthorizedAccountInstanceNameAsync(client, source.name, authorization.grantee_system_account_id, authorization.id)
  await client.execute(`
    INSERT INTO ${resourceAuthorizationWriteTable(client, 'accounts')} (
      id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
      name, type, status, credentials_encrypted, credential_fingerprint, credential_mask,
      proxy_profile_id, concurrency_limit,
      priority, super_priority_enabled, fallback_enabled, schedulable, notes, account_expires_at,
      cooldown_until, last_error_code, last_error_message,
      cooldown_retest_observation_started_at, stream_failure_count, stream_failure_window_started_at,
      authorization_instance_source_account_id, authorization_instance_authorization_id, authorization_instance_owner_system_account_id,
      created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, NULL, ?, ?, ?, ?, ?, ?, 1, NULL, NULL, NULL, NULL, NULL, NULL, 0, NULL, ?, ?, ?, ?, ?)
  `, [
    id,
    authorization.grantee_system_account_id,
    source.provider_code,
    source.provider_protocol_profile_id,
    source.protocol_code,
    source.protocol_version,
    name,
    source.type,
    encryptJson({}),
    '',
    null,
    source.concurrency_limit,
    0,
    0,
    0,
    authorization.resource_id,
    authorization.id,
    authorization.resource_owner_system_account_id,
    now,
    now
  ])
  await replaceAccountNameSearchTermsAsync(client, id, authorization.grantee_system_account_id, name, now)
  return client.one<AccountRow>(`
    SELECT *
    FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
    WHERE id = ?
    LIMIT 1
  `, [id])
}

async function uniqueAuthorizedAccountInstanceNameAsync(client: DatabaseClient, sourceName: string, systemAccountId: string, authorizationId: string, exceptAccountId?: string): Promise<string> {
  const baseName = sourceName.trim() || '授权账户'
  const shortId = authorizationId.split('_').pop()?.slice(0, 6) || authorizationId.slice(-6)
  const candidates = [
    baseName,
    `${baseName}-${shortId}`
  ]
  for (const candidate of candidates) {
    if (await isAccountNameAvailableAsync(client, systemAccountId, candidate, exceptAccountId)) return candidate
  }
  for (let index = 2; index <= 1000; index += 1) {
    const candidate = `${baseName}-${shortId}-${index}`
    if (await isAccountNameAvailableAsync(client, systemAccountId, candidate, exceptAccountId)) return candidate
  }
  return `${baseName}-${shortId}-${Date.now()}`
}

async function isAccountNameAvailableAsync(client: DatabaseClient, systemAccountId: string, name: string, exceptAccountId?: string): Promise<boolean> {
  const params: string[] = [systemAccountId, name]
  const exceptClause = exceptAccountId ? ' AND id <> ?' : ''
  if (exceptAccountId) params.push(exceptAccountId)
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
    WHERE system_account_id = ?
      AND lower(name) = lower(?)
      AND deleted_at IS NULL${exceptClause}
    LIMIT 1
  `, params)
  return !row?.id
}

async function groupIdForAuthorizationBindingAsync(client: DatabaseClient, providerProtocolProfileId: string, systemAccountId: string, targetGroupId?: string): Promise<string> {
  if (targetGroupId) {
    const group = await client.one<{ id?: string; system_account_id?: string | null; provider_protocol_profile_id?: string | null; enabled?: number | boolean | null }>(`
      SELECT id, system_account_id, provider_protocol_profile_id, enabled
      FROM ${resourceAuthorizationWriteTable(client, 'groups')}
      WHERE id = ?
      LIMIT 1
    `, [targetGroupId])
    if (!group?.id || group.system_account_id !== systemAccountId) {
      throw new Error('目标分组不存在或不属于被授权用户')
    }
    if (group.provider_protocol_profile_id !== providerProtocolProfileId) {
      throw new Error('目标分组协议档案与授权账户不一致')
    }
    if (Number(group.enabled) !== 1) {
      throw new Error('目标分组已停用，请选择启用分组')
    }
    return group.id
  }
  return defaultGroupIdForAuthorizationBindingAsync(client, providerProtocolProfileId, systemAccountId)
}

async function defaultGroupIdForAuthorizationBindingAsync(client: DatabaseClient, providerProtocolProfileId: string, systemAccountId: string): Promise<string> {
  const existing = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${resourceAuthorizationWriteTable(client, 'groups')}
    WHERE system_account_id = ?
      AND provider_protocol_profile_id = ?
      AND is_default = 1
      AND enabled = 1
    ORDER BY updated_at DESC, id ASC
    LIMIT 1
  `, [systemAccountId, providerProtocolProfileId])
  if (existing?.id) return existing.id
  throw new Error('目标用户缺少启用的默认分组，请按当前数据契约修复目标用户分组后再授权')
}

async function refreshResourceAuthorizationEffectiveSourceAsync(
  authorizationId: string,
  actor: string,
  now: string,
  client: DatabaseClient,
  options: {
    noActiveSourceReason?: string
    preserveExpiredWhenNoActiveSource?: boolean
    terminalStatus?: 'revoked' | 'returned'
  } = {}
): Promise<void> {
  clearResourceAuthorizationLookupCaches()
  const activeTeamSource = await client.one<{ source_team_id?: string | null }>(`
    SELECT ras.source_team_id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorization_grants')} trg
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
  `, [now, authorizationId])
  if (activeTeamSource?.source_team_id) {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
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
    `, [now, activeTeamSource.source_team_id, now, actor, now, now, now, now, now, authorizationId])
    return
  }
  const pausedTeamSource = await client.one<{ source_team_id?: string | null }>(`
    SELECT ras.source_team_id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')} ras
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorizations')} ra ON ra.id = ras.authorization_id
    INNER JOIN ${resourceAuthorizationWriteTable(client, 'resource_authorization_grants')} trg
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
  `, [authorizationId])
  if (pausedTeamSource?.source_team_id) {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
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
    `, [now, pausedTeamSource.source_team_id, now, actor, now, now, now, now, now, authorizationId])
    return
  }
  const activeManualSource = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${resourceAuthorizationWriteTable(client, 'resource_authorization_sources')}
    WHERE authorization_id = ?
      AND source_type = 'manual'
      AND status = 'active'
    ORDER BY activated_at ASC, created_at ASC, id ASC
    LIMIT 1
  `, [authorizationId])
  if (activeManualSource?.id) {
    await client.execute(`
      UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
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
    `, [now, now, actor, now, now, now, now, now, authorizationId])
    return
  }
  const preserveExpiredWhenNoActiveSource = options.preserveExpiredWhenNoActiveSource === false ? 0 : 1
  const noActiveSourceReason = options.noActiveSourceReason ?? null
  const hasNoActiveSourceReason = noActiveSourceReason ? 1 : 0
  const terminalStatus = options.terminalStatus ?? 'revoked'
  await client.execute(`
    UPDATE ${resourceAuthorizationWriteTable(client, 'resource_authorizations')}
    SET status = CASE WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'expired' ELSE ? END,
        effective_source_type = NULL,
        effective_source_team_id = NULL,
        revoked_by = CASE WHEN ? = 1 THEN ? ELSE COALESCE(revoked_by, ?) END,
        revoked_at = CASE WHEN ? = 1 THEN ? ELSE COALESCE(revoked_at, ?) END,
        revoked_reason = CASE
          WHEN ? = 1 AND expires_at IS NOT NULL AND expires_at <= ? THEN 'authorization_expired'
          WHEN ? = 1 THEN CAST(? AS text)
          ELSE COALESCE(revoked_reason, 'no_active_source')
        END,
        last_source_changed_at = ?,
        updated_at = ?
    WHERE id = ?
  `, [
    preserveExpiredWhenNoActiveSource,
    now,
    terminalStatus,
    hasNoActiveSourceReason,
    actor,
    actor,
    hasNoActiveSourceReason,
    now,
    now,
    preserveExpiredWhenNoActiveSource,
    now,
    hasNoActiveSourceReason,
    noActiveSourceReason,
    now,
    now,
    authorizationId
  ])
}

function normalizeRequiredTextInput(value: unknown, label: string): string {
  if (typeof value !== 'string') {
    throw new Error(`${label}不能为空`)
  }
  const text = value.trim()
  if (!text) {
    throw new Error(`${label}不能为空`)
  }
  return text
}

function normalizeOptionalTextInput(value: unknown, label: string, options: { allowBlank?: boolean } = {}): string | undefined {
  if (value === undefined) {
    return undefined
  }
  if (value === null) {
    throw new Error(`${label}不能为空`)
  }
  if (typeof value !== 'string') {
    throw new Error(`${label}必须是字符串`)
  }
  const text = value.trim()
  if (!text) {
    if (options.allowBlank) return undefined
    throw new Error(`${label}不能为空`)
  }
  return text
}

function normalizeResourceAuthorizationGranteeType(value: unknown): 'team' | 'system_account' {
  if (value === 'team' || value === 'system_account') {
    return value
  }
  throw new Error('被授权对象类型无效')
}

function normalizeResourceAuthorizationStatus(value: unknown): AuthorizationStatus {
  if (value === 'active' || value === 'paused' || value === 'expired' || value === 'revoked' || value === 'returned') {
    return value
  }
  throw new Error('授权状态无效')
}

function normalizeResourceAuthorizationExpiresAtInput(value: unknown): string | null {
  if (value === undefined || value === null) {
    return null
  }
  if (typeof value !== 'string') {
    throw new Error('过期时间格式不正确')
  }
  const text = value.trim()
  if (!text) {
    throw new Error('过期时间格式不正确')
  }
  const normalized = optionalNullableServerDateTimeIso(text)
  if (!normalized) {
    throw new Error('过期时间格式不正确')
  }
  return normalized
}

function resourceOwnerSystemAccountId(resourceType: ResourceAuthorizationResourceType, resourceId: string): string | undefined {
  if (resourceType !== 'account') return groupOwnerAndProvider(resourceId)?.systemAccountId
  const row = getBusinessDatabase()
    .prepare('SELECT system_account_id, authorization_instance_authorization_id FROM accounts WHERE id = ? AND deleted_at IS NULL LIMIT 1')
    .get(resourceId) as unknown as { system_account_id?: string; authorization_instance_authorization_id?: string | null } | undefined
  if (!row?.system_account_id || row.authorization_instance_authorization_id) return undefined
  return row.system_account_id
}

function validateResourceAuthorizationExpiresAt(
  resourceType: ResourceAuthorizationResourceType,
  resourceId: string,
  expiresAt: string | null,
  now = Date.now(),
  options: { allowExpired?: boolean } = {}
): void {
  if (!expiresAt) return
  const expiresAtMs = Date.parse(expiresAt)
  if (!Number.isFinite(expiresAtMs)) throw new Error('授权到期时间格式不正确')
  if (!options.allowExpired && expiresAtMs <= now) throw new Error('授权到期时间不能早于当前时间')
  if (resourceType !== 'account') return
  const account = getBusinessDatabase()
    .prepare('SELECT account_expires_at FROM accounts WHERE id = ? AND deleted_at IS NULL')
    .get(resourceId) as unknown as { account_expires_at?: string | null } | undefined
  if (!account?.account_expires_at) return
  const accountExpiresAtMs = Date.parse(account.account_expires_at)
  if (Number.isFinite(accountExpiresAtMs) && expiresAtMs > accountExpiresAtMs) {
    throw new Error('授权到期时间不能晚于账户到期时间')
  }
}

async function validateResourceAuthorizationExpiresAtAsync(
  client: DatabaseClient,
  resourceType: ResourceAuthorizationResourceType,
  resourceId: string,
  expiresAt: string | null,
  now = Date.now(),
  options: { allowExpired?: boolean } = {}
): Promise<void> {
  if (!expiresAt) return
  const expiresAtMs = Date.parse(expiresAt)
  if (!Number.isFinite(expiresAtMs)) throw new Error('授权到期时间格式不正确')
  if (!options.allowExpired && expiresAtMs <= now) throw new Error('授权到期时间不能早于当前时间')
  if (resourceType !== 'account') return
  const account = await client.one<{ account_expires_at?: string | null }>(`
    SELECT account_expires_at
    FROM ${resourceAuthorizationWriteTable(client, 'accounts')}
    WHERE id = ?
      AND deleted_at IS NULL
    LIMIT 1
  `, [resourceId])
  if (!account?.account_expires_at) return
  const accountExpiresAtMs = Date.parse(account.account_expires_at)
  if (Number.isFinite(accountExpiresAtMs) && expiresAtMs > accountExpiresAtMs) {
    throw new Error('授权到期时间不能晚于账户到期时间')
  }
}

async function rememberRequestQuotaHourlyWindowsFromJsonAsync(client: DatabaseClient, limitsJson: string | null | undefined, timestamp: string): Promise<void> {
  const limits = parseRequestQuotaLimitsJson(limitsJson)
  const hours = limits.hourly?.enabled ? limits.hourly.hours : undefined
  if (!Number.isInteger(hours) || typeof hours !== 'number' || hours < 1 || hours > maxRequestQuotaHourlyWindowHours) {
    return
  }
  await client.execute(`
    INSERT INTO ${resourceAuthorizationWriteTable(client, 'request_quota_hourly_window_configs')} (window_hours, created_at, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(window_hours) DO UPDATE SET updated_at = excluded.updated_at
  `, [hours, timestamp, timestamp])
}

async function getResourceAuthorizationWriteClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('高性能授权写入客户端仅支持 PostgreSQL 模式')
  }
  return createPostgresDatabaseClient(await getPostgresPool())
}

function resourceAuthorizationWriteTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function refreshAfterResourceAuthorizationBusinessWrite(reason: string): void {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    refreshGroupAccountStatsAfterWrite({ all: true, reason })
  }
  clearGatewayApiKeyValidationCache()
  invalidateGroupAccountIdsCache()
  clearResourceAuthorizationLookupCaches()
  invalidateAuthorizationRuntimeAfterBusinessWrite(reason)
}

function invalidateAuthorizationRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
  notifyAuthorizationQuotaCacheInvalidation(reason)
}
