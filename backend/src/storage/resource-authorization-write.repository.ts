import type { AuthorizationStatus, ResourceAuthorizationResourceType, ResourceAuthorizationSummary } from '../domain/types.js'
import { notifyAuthorizationQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { groupOwnerAndProvider, canManageResourceOwner, isResourceAuthorizationExpired } from './resource-authorization-helpers.js'
import { normalizeResourceType } from './resource-authorization-list-helpers.js'
import { findResourceAuthorizationSummary } from './resource-authorization-read.repository.js'
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
import type { ResourceAuthorizationGrantRow, SystemTeamRow } from './repository-row-types.js'
import { normalizeRequestQuotaLimits, requestQuotaLimitsJson } from './request-quota-limits.js'
import { findSystemAccountById } from './system-accounts.repository.js'
import { optionalNullableServerDateTimeIso } from './value-utils.js'

const resourceAuthorizationCreateInputKeys = new Set(['resourceType', 'resourceId', 'granteeType', 'granteeId', 'targetGroupId', 'remark', 'expiresAt', 'limits'])
const resourceAuthorizationUpdateInputKeys = new Set(['status', 'expiresAt', 'limits'])

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

function findResourceAuthorizationAfterWrite(authorizationId: string, access?: AccessScope): ResourceAuthorizationSummary | undefined {
  expireDueResourceAuthorizations()
  return findResourceAuthorizationSummary(authorizationId, access)
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

function invalidateAuthorizationRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
  notifyAuthorizationQuotaCacheInvalidation(reason)
}
