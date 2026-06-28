import type { DatabaseSync } from 'node:sqlite'

import type { GroupSchedulingPolicy, GroupSummary, ProviderCode } from '../domain/types.js'
import { groupSchedulingPolicyJson, normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { maxRouteStrategyAvailabilityLossCandidates } from './route-strategy-group-binding-limits.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { emptyGroupAccountStats } from './group-account-stats.mapper.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { findGroupSummary, findGroupSummaryAsync } from './group-summary.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { requireEnabledProviderProtocolProfile, requireEnabledProviderProtocolProfileAsync } from './provider.repository.js'
import { sqlPlaceholders } from './query-utils.js'
import {
  activeResourceAuthorization,
  activeResourceAuthorizationById,
  canManageResourceOwner as canManageAuthorizedResourceOwner,
  groupOwnerAndProvider,
  resourceAuthorizationSelectColumns
} from './resource-authorization-helpers.js'
import type { ResourceAuthorizationRow } from './repository-row-types.js'
import {
  invalidateGroupLookupCache,
  loadSystemAccountNameMapByIds
} from './repository-lookups.js'
import {
  assertKnownInputKeys,
  hasOwnInput,
  normalizeNullableTextInput,
  normalizeOptionalBooleanInput,
  normalizeOptionalRequiredTextInput,
  requiredTextInput
} from './repository-input-normalization.js'
import {
  assertAffectedRouteStrategiesCanLoseGroupAvailability,
  assertAffectedRouteStrategiesCanLoseGroupAvailabilityAsync,
  assertRouteStrategiesCanLoseGroupAvailability,
  assertRouteStrategiesCanLoseGroupAvailabilityAsync,
  type RouteStrategyGroupAvailabilityLossCandidate
} from './route-strategy-availability-guard.js'

export class DefaultGroupReadonlyError extends Error {
  constructor() {
    super('默认分组不允许修改')
    this.name = 'DefaultGroupReadonlyError'
  }
}

function hasGroupSchedulingPolicyInput(input: Record<string, unknown>): boolean {
  return hasOwnInput(input, 'schedulingPolicy')
}

function groupSchedulingPolicyInput(input: Record<string, unknown>): unknown {
  return input.schedulingPolicy
}

function writableSchedulingPolicyInput(policy: GroupSchedulingPolicy | undefined): Record<string, unknown> | undefined {
  if (!policy) {
    return undefined
  }
  return {
    defaultSoftConcurrency: policy.defaultSoftConcurrency,
    maxQueueWaitMs: policy.maxQueueWaitMs,
    clientIpConcurrencyLimit: policy.clientIpConcurrencyLimit,
    clientIpConcurrencyOverflowMode: policy.clientIpConcurrencyOverflowMode,
    imageLaneMaxConcurrency: policy.imageLaneMaxConcurrency
  }
}

const groupCreateInputKeys = new Set([
  'name',
  'providerCode',
  'providerProtocolProfileId',
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const groupUpdateInputKeys = new Set([
  'name',
  'providerCode',
  'providerProtocolProfileId',
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const authorizedGroupSettingsInputKeys = new Set([
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const maxDeletedGroupAffectedApiKeyRouteSamples = 500

export function createGroup(input: Record<string, unknown>, access?: AccessScope): GroupSummary {
  assertKnownInputKeys(input, groupCreateInputKeys, '分组创建参数')
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = requiredTextInput(input.providerCode, '供应商')
  const providerProfile = requireEnabledProviderProtocolProfile(providerCode, input.providerProtocolProfileId)
  const groupType = normalizeGroupType(input.groupType)
  const schedulingPolicyJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), groupType)
  const name = requiredTextInput(input.name, '分组名称')
  const enabled = normalizeOptionalBooleanInput(input, 'enabled', true, '分组启用状态')
  assertGroupNameAvailable(systemAccountId, providerCode, name)
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name,
    providerCode,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion,
    description: normalizeNullableTextInput(input.description, '分组说明'),
    enabled,
    isDefault: false,
    groupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(schedulingPolicyJson, groupType),
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  try {
    getBusinessDatabase()
      .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)')
      .run(group.id, systemAccountId, group.name, group.providerCode, providerProfile.id, providerProfile.protocolCode, providerProfile.protocolVersion, group.description ?? null, group.enabled ? 1 : 0, group.groupType, schedulingPolicyJson, now, now)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${group.name}`)
    }
    throw error
  }
  invalidateGroupLookupCache(group.id)
  invalidateGatewayRuntimeAfterBusinessWrite('group_created')
  return group
}

export async function createGroupAsync(input: Record<string, unknown>, access?: AccessScope): Promise<GroupSummary> {
  assertKnownInputKeys(input, groupCreateInputKeys, '分组创建参数')
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = requiredTextInput(input.providerCode, '供应商')
  const providerProfile = await requireEnabledProviderProtocolProfileAsync(providerCode, input.providerProtocolProfileId)
  const groupType = normalizeGroupType(input.groupType)
  const schedulingPolicyJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), groupType)
  const name = requiredTextInput(input.name, '分组名称')
  const enabled = normalizeOptionalBooleanInput(input, 'enabled', true, '分组启用状态')
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: undefined,
    name,
    providerCode,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion,
    description: normalizeNullableTextInput(input.description, '分组说明'),
    enabled,
    isDefault: false,
    groupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(schedulingPolicyJson, groupType),
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  const client = await getGroupWriteDatabaseClient()
  try {
    await client.transaction(async (tx) => {
      await assertGroupNameAvailableAsync(tx, systemAccountId, providerCode, name)
      await tx.execute(`
        INSERT INTO ${groupWriteTable(tx, 'groups')} (
          id, system_account_id, name, provider_code, provider_protocol_profile_id, protocol_code,
          protocol_version, description, enabled, is_default, group_type, scheduling_policy_json,
          created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)
      `, [
        group.id,
        systemAccountId,
        group.name,
        group.providerCode,
        providerProfile.id,
        providerProfile.protocolCode,
        providerProfile.protocolVersion,
        group.description ?? null,
        group.enabled ? 1 : 0,
        group.groupType,
        schedulingPolicyJson,
        now,
        now
      ])
    })
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${group.name}`)
    }
    throw error
  }
  invalidateGroupLookupCache(group.id)
  invalidateGatewayRuntimeAfterBusinessWrite('group_created')
  return group
}

export function updateGroup(id: string, input: Record<string, unknown>, access?: AccessScope): GroupSummary | undefined {
  assertKnownInputKeys(input, groupUpdateInputKeys, '分组更新参数')
  const current = findGroupSummary(id, access)
  if (!current) {
    return undefined
  }
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  if (current.accessType === 'authorized' && current.ownerSystemAccountId !== viewerSystemAccountId) {
    return updateAuthorizedGroupSettings(id, input, current, access)
  }
  if (current.isDefault) {
    throw new DefaultGroupReadonlyError()
  }
  const systemAccountId = groupOwnerAndProvider(id)?.systemAccountId
  if (!systemAccountId) {
    throw new Error('分组归属数据异常，请清理后再编辑')
  }
  if (!canManageAuthorizedResourceOwner(systemAccountId, access)) {
    return undefined
  }
  const hasDescriptionInput = hasOwnInput(input, 'description')
  const hasProviderCodeInput = hasOwnInput(input, 'providerCode')
  const hasProviderProtocolProfileInput = hasOwnInput(input, 'providerProtocolProfileId')
  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextProviderCode = hasProviderCodeInput
    ? normalizeOptionalRequiredTextInput(input, 'providerCode', current.providerCode, '供应商')
    : current.providerCode
  const nextProviderProtocolProfileId = hasProviderProtocolProfileInput
    ? normalizeOptionalRequiredTextInput(input, 'providerProtocolProfileId', current.providerProtocolProfileId ?? '', '供应商协议档案')
    : nextProviderCode === current.providerCode
      ? current.providerProtocolProfileId
      : undefined
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : writableSchedulingPolicyInput(current.schedulingPolicy)
  const next: GroupSummary = {
    ...current,
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '分组名称'),
    providerCode: nextProviderCode,
    providerProtocolProfileId: nextProviderProtocolProfileId,
    description: hasDescriptionInput ? normalizeNullableTextInput(input.description, '分组说明') : current.description,
    enabled: normalizeOptionalBooleanInput(input, 'enabled', current.enabled, '分组启用状态'),
    groupType: nextGroupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nextGroupType)
  }
  if (next.providerCode !== current.providerCode && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  const providerProfile = requireEnabledProviderProtocolProfile(next.providerCode, next.providerProtocolProfileId)
  next.providerProtocolProfileId = providerProfile.id
  next.protocolCode = providerProfile.protocolCode
  next.protocolVersion = providerProfile.protocolVersion
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    assertGroupNameAvailable(systemAccountId, next.providerCode, next.name, id)
    if (current.enabled && !next.enabled) {
      assertRouteStrategiesCanLoseGroupAvailability(database, id, current.name, '停用分组')
    }
    database
      .prepare('UPDATE groups SET name = ?, provider_code = ?, provider_protocol_profile_id = ?, protocol_code = ?, protocol_version = ?, description = ?, enabled = ?, group_type = ?, scheduling_policy_json = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
      .run(next.name, next.providerCode, next.providerProtocolProfileId, next.protocolCode, next.protocolVersion, next.description ?? null, next.enabled ? 1 : 0, next.groupType, groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nowIso(), id, systemAccountId)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${next.name}`)
    }
    throw error
  }
  invalidateGroupLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('group_updated')
  return findGroupSummary(id, access)
}

export async function updateGroupAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<GroupSummary | undefined> {
  assertKnownInputKeys(input, groupUpdateInputKeys, '分组更新参数')
  const current = await findGroupSummaryAsync(id, access)
  if (!current) {
    return undefined
  }
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  if (current.accessType === 'authorized' && current.ownerSystemAccountId !== viewerSystemAccountId) {
    return updateAuthorizedGroupSettingsAsync(id, input, current, access)
  }
  if (current.isDefault) {
    throw new DefaultGroupReadonlyError()
  }
  const owner = await groupOwnerAndProviderAsync(id)
  if (!owner) {
    throw new Error('分组归属数据异常，请清理后再编辑')
  }
  if (!canManageAuthorizedResourceOwner(owner.systemAccountId, access)) {
    return undefined
  }
  const hasDescriptionInput = hasOwnInput(input, 'description')
  const hasProviderCodeInput = hasOwnInput(input, 'providerCode')
  const hasProviderProtocolProfileInput = hasOwnInput(input, 'providerProtocolProfileId')
  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextProviderCode = hasProviderCodeInput
    ? normalizeOptionalRequiredTextInput(input, 'providerCode', current.providerCode, '供应商')
    : current.providerCode
  const nextProviderProtocolProfileId = hasProviderProtocolProfileInput
    ? normalizeOptionalRequiredTextInput(input, 'providerProtocolProfileId', current.providerProtocolProfileId ?? '', '供应商协议档案')
    : nextProviderCode === current.providerCode
      ? current.providerProtocolProfileId
      : undefined
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : writableSchedulingPolicyInput(current.schedulingPolicy)
  const next: GroupSummary = {
    ...current,
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '分组名称'),
    providerCode: nextProviderCode,
    providerProtocolProfileId: nextProviderProtocolProfileId,
    description: hasDescriptionInput ? normalizeNullableTextInput(input.description, '分组说明') : current.description,
    enabled: normalizeOptionalBooleanInput(input, 'enabled', current.enabled, '分组启用状态'),
    groupType: nextGroupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nextGroupType)
  }
  if (next.providerCode !== current.providerCode && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  const providerProfile = await requireEnabledProviderProtocolProfileAsync(next.providerCode, next.providerProtocolProfileId)
  next.providerProtocolProfileId = providerProfile.id
  next.protocolCode = providerProfile.protocolCode
  next.protocolVersion = providerProfile.protocolVersion
  const client = await getGroupWriteDatabaseClient()
  await client.transaction(async (tx) => {
    await lockGroupMutationRowAsync(tx, id, owner.systemAccountId)
    await assertGroupNameAvailableAsync(tx, owner.systemAccountId, next.providerCode, next.name, id)
    if (current.enabled && !next.enabled) {
      await assertRouteStrategiesCanLoseGroupAvailabilityAsync(tx, id, current.name, '停用分组')
    }
    await tx.execute(`
      UPDATE ${groupWriteTable(tx, 'groups')}
      SET name = ?, provider_code = ?, provider_protocol_profile_id = ?, protocol_code = ?, protocol_version = ?,
          description = ?, enabled = ?, group_type = ?, scheduling_policy_json = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ?
    `, [
      next.name,
      next.providerCode,
      next.providerProtocolProfileId,
      next.protocolCode,
      next.protocolVersion,
      next.description ?? null,
      next.enabled ? 1 : 0,
      next.groupType,
      groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType),
      nowIso(),
      id,
      owner.systemAccountId
    ])
  })
  invalidateGroupLookupCache(id)
  invalidateGatewayRuntimeAfterBusinessWrite('group_updated')
  return await findGroupSummaryAsync(id, access)
}

function updateAuthorizedGroupSettings(
  id: string,
  input: Record<string, unknown>,
  current: GroupSummary,
  access?: AccessScope
): GroupSummary | undefined {
  assertKnownInputKeys(input, authorizedGroupSettingsInputKeys, '授权分组使用配置')
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId || !current.groupAuthorizationId) {
    return undefined
  }
  const database = getBusinessDatabase()
  const authorization = database
    .prepare(`
      SELECT ${resourceAuthorizationSelectColumns()}
      FROM resource_authorizations
      WHERE id = ?
        AND resource_type = 'group'
        AND resource_id = ?
        AND grantee_system_account_id = ?
        AND status IN ('active', 'paused', 'expired')
      LIMIT 1
    `)
    .get(current.groupAuthorizationId, id, granteeSystemAccountId) as unknown as ResourceAuthorizationRow | undefined
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  const existing = database
    .prepare('SELECT enabled FROM group_authorization_settings WHERE authorization_id = ? LIMIT 1')
    .get(authorization.id) as unknown as { enabled?: number } | undefined
  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : writableSchedulingPolicyInput(current.schedulingPolicy)
  const nextSchedulingPolicyJson = groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType)
  const nextEnabled = normalizeOptionalBooleanInput(input, 'enabled', existing?.enabled === 0 ? false : true, '授权分组启用状态')
  const now = nowIso()
  if (current.enabled && !nextEnabled) {
    assertRouteStrategiesCanLoseGroupAvailability(database, id, current.name, '停用授权分组', granteeSystemAccountId)
  }
  database
    .prepare(`
      INSERT INTO group_authorization_settings (
        authorization_id, system_account_id, group_id, enabled, group_type,
        scheduling_policy_json, created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(authorization_id) DO UPDATE SET
        system_account_id = excluded.system_account_id,
        group_id = excluded.group_id,
        enabled = excluded.enabled,
        group_type = excluded.group_type,
        scheduling_policy_json = excluded.scheduling_policy_json,
        updated_at = excluded.updated_at
    `)
    .run(
      authorization.id,
      granteeSystemAccountId,
      id,
      nextEnabled ? 1 : 0,
      nextGroupType,
      nextSchedulingPolicyJson,
      now,
      now
    )
  invalidateGatewayRuntimeAfterBusinessWrite('group_authorization_settings_updated')
  return findGroupSummary(id, access)
}

async function updateAuthorizedGroupSettingsAsync(
  id: string,
  input: Record<string, unknown>,
  current: GroupSummary,
  access?: AccessScope
): Promise<GroupSummary | undefined> {
  assertKnownInputKeys(input, authorizedGroupSettingsInputKeys, '授权分组使用配置')
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!granteeSystemAccountId || !current.groupAuthorizationId) {
    return undefined
  }
  const client = await getGroupWriteDatabaseClient()
  const authorization = await client.one<ResourceAuthorizationRow>(`
    SELECT ${resourceAuthorizationSelectColumns()}
    FROM ${groupWriteTable(client, 'resource_authorizations')}
    WHERE id = ?
      AND resource_type = 'group'
      AND resource_id = ?
      AND grantee_system_account_id = ?
      AND status IN ('active', 'paused', 'expired')
    LIMIT 1
  `, [current.groupAuthorizationId, id, granteeSystemAccountId])
  if (!authorization || authorization.resource_owner_system_account_id === granteeSystemAccountId) {
    return undefined
  }
  const existing = await client.one<{ enabled?: number }>(`
    SELECT enabled
    FROM ${groupWriteTable(client, 'group_authorization_settings')}
    WHERE authorization_id = ?
    LIMIT 1
  `, [authorization.id])
  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  const nextGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : current.groupType
  const nextSchedulingPolicyInput = hasSchedulingPolicyInput ? groupSchedulingPolicyInput(input) : writableSchedulingPolicyInput(current.schedulingPolicy)
  const nextSchedulingPolicyJson = groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType)
  const nextEnabled = normalizeOptionalBooleanInput(input, 'enabled', Number(existing?.enabled ?? 1) === 0 ? false : true, '授权分组启用状态')
  const now = nowIso()
  if (current.enabled && !nextEnabled) {
    await assertRouteStrategiesCanLoseGroupAvailabilityAsync(client, id, current.name, '停用授权分组', granteeSystemAccountId)
  }
  await client.execute(`
    INSERT INTO ${groupWriteTable(client, 'group_authorization_settings')} (
      authorization_id, system_account_id, group_id, enabled, group_type,
      scheduling_policy_json, created_at, updated_at
    )
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(authorization_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      group_id = excluded.group_id,
      enabled = excluded.enabled,
      group_type = excluded.group_type,
      scheduling_policy_json = excluded.scheduling_policy_json,
      updated_at = excluded.updated_at
  `, [
    authorization.id,
    granteeSystemAccountId,
    id,
    nextEnabled ? 1 : 0,
    nextGroupType,
    nextSchedulingPolicyJson,
    now,
    now
  ])
  invalidateGatewayRuntimeAfterBusinessWrite('group_authorization_settings_updated')
  return await findGroupSummaryAsync(id, access)
}

export interface DeletedGroupRouteStrategyChange {
  routeStrategyId: string
  routeStrategyName: string
  removedGroupId: string
  removedGroupName?: string
  removedBindingStatus?: string
}

export interface DeletedGroupApiKeyRouteChange extends DeletedGroupRouteStrategyChange {
  apiKeyId: string
}

export interface DeleteGroupResult {
  deleted: boolean
  affectedRouteStrategies: DeletedGroupRouteStrategyChange[]
  affectedApiKeyRoutes: DeletedGroupApiKeyRouteChange[]
  affectedApiKeyRouteCount: number
  affectedApiKeyRoutesTruncated: boolean
}

export function deleteGroup(id: string, access?: AccessScope): DeleteGroupResult {
  const current = findGroupSummary(id, access)
  if (current?.isDefault) {
    throw new Error('默认分组不能删除')
  }
  const owner = groupOwnerAndProvider(id)
  if (!owner || !canManageAuthorizedResourceOwner(owner.systemAccountId, access)) {
    return emptyDeleteGroupResult()
  }
  const database = getBusinessDatabase()
  let deleted = false
  let affectedRouteStrategies: DeletedGroupRouteStrategyChange[] = []
  let affectedApiKeyRouteChanges = emptyDeletedGroupApiKeyRouteChanges()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    affectedRouteStrategies = preserveRouteStrategiesBeforeGroupDelete(database, id, current?.name)
    affectedApiKeyRouteChanges = loadDeletedGroupApiKeyRouteChanges(database, affectedRouteStrategies)
    const result = database.prepare('DELETE FROM groups WHERE id = ? AND system_account_id = ?').run(id, owner.systemAccountId)
    deleted = Number(result.changes ?? 0) > 0
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (deleted) {
    refreshGroupAccountStatsAfterWrite({ groupIds: [id], reason: 'group_deleted' })
    invalidateGroupLookupCache(id)
    invalidateGroupAccountIdsCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('group_deleted')
  }
  return {
    deleted,
    affectedRouteStrategies: deleted ? affectedRouteStrategies : [],
    affectedApiKeyRoutes: deleted ? affectedApiKeyRouteChanges.items : [],
    affectedApiKeyRouteCount: deleted ? affectedApiKeyRouteChanges.total : 0,
    affectedApiKeyRoutesTruncated: deleted ? affectedApiKeyRouteChanges.truncated : false
  }
}

export async function deleteGroupAsync(id: string, access?: AccessScope): Promise<DeleteGroupResult> {
  const current = await findGroupSummaryAsync(id, access)
  if (current?.isDefault) {
    throw new Error('默认分组不能删除')
  }
  const owner = await groupOwnerAndProviderAsync(id)
  if (!owner || !canManageAuthorizedResourceOwner(owner.systemAccountId, access)) {
    return emptyDeleteGroupResult()
  }
  const client = await getGroupWriteDatabaseClient()
  let deleted = false
  let affectedRouteStrategies: DeletedGroupRouteStrategyChange[] = []
  let affectedApiKeyRouteChanges = emptyDeletedGroupApiKeyRouteChanges()
  await client.transaction(async (tx) => {
    await lockGroupMutationRowAsync(tx, id, owner.systemAccountId)
    affectedRouteStrategies = await preserveRouteStrategiesBeforeGroupDeleteAsync(tx, id, current?.name)
    affectedApiKeyRouteChanges = await loadDeletedGroupApiKeyRouteChangesAsync(tx, affectedRouteStrategies)
    const result = await tx.execute(`
      DELETE FROM ${groupWriteTable(tx, 'groups')}
      WHERE id = ? AND system_account_id = ?
    `, [id, owner.systemAccountId])
    deleted = Number(result.changes ?? 0) > 0
  })
  if (deleted) {
    refreshGroupAccountStatsAfterWriteForCurrentDriver({ groupIds: [id], reason: 'group_deleted' })
    invalidateGroupLookupCache(id)
    invalidateGroupAccountIdsCache(id)
    invalidateGatewayRuntimeAfterBusinessWrite('group_deleted')
  }
  return {
    deleted,
    affectedRouteStrategies: deleted ? affectedRouteStrategies : [],
    affectedApiKeyRoutes: deleted ? affectedApiKeyRouteChanges.items : [],
    affectedApiKeyRouteCount: deleted ? affectedApiKeyRouteChanges.total : 0,
    affectedApiKeyRoutesTruncated: deleted ? affectedApiKeyRouteChanges.truncated : false
  }
}

function preserveRouteStrategiesBeforeGroupDelete(
  database: DatabaseSync,
  groupId: string,
  groupName?: string
): DeletedGroupRouteStrategyChange[] {
  const affectedRouteStrategies = database
    .prepare(`
      SELECT
        route_strategy_groups.route_strategy_id AS id,
        route_strategies.name,
        route_strategies.system_account_id AS systemAccountId,
        route_strategy_groups.status AS targetBindingStatus
      FROM route_strategy_groups
      INNER JOIN route_strategies
        ON route_strategies.id = route_strategy_groups.route_strategy_id
        AND route_strategies.system_account_id = route_strategy_groups.system_account_id
        AND route_strategies.status = 'active'
      WHERE route_strategy_groups.group_id = ?
      ORDER BY route_strategy_groups.route_strategy_id ASC
      LIMIT ?
    `)
    .all(groupId, maxRouteStrategyAvailabilityLossCandidates + 1) as unknown as RouteStrategyGroupAvailabilityLossCandidate[]
  if (!affectedRouteStrategies.length) return []
  if (affectedRouteStrategies.length > maxRouteStrategyAvailabilityLossCandidates) {
    throw new Error(`该分组关联的策略路由超过 ${maxRouteStrategyAvailabilityLossCandidates} 个，请先分批解除绑定后再删除分组`)
  }

  assertAffectedRouteStrategiesCanLoseGroupAvailability(database, groupId, affectedRouteStrategies, '删除分组', groupName)

  return affectedRouteStrategies.map((routeStrategy) => {
    return {
      routeStrategyId: routeStrategy.id,
      routeStrategyName: routeStrategy.name,
      removedGroupId: groupId,
      removedGroupName: groupName,
      removedBindingStatus: routeStrategy.targetBindingStatus ?? undefined
    }
  })
}

function loadDeletedGroupApiKeyRouteChanges(
  database: DatabaseSync,
  routeChanges: DeletedGroupRouteStrategyChange[]
): { items: DeletedGroupApiKeyRouteChange[]; total: number; truncated: boolean } {
  const routeIds = [...new Set(routeChanges.map((change) => change.routeStrategyId).filter(Boolean))]
  if (!routeIds.length) return emptyDeletedGroupApiKeyRouteChanges()
  const changeByRouteStrategyId = new Map(routeChanges.map((change) => [change.routeStrategyId, change]))
  const countRow = database
    .prepare(`
      SELECT COUNT(*) AS total
      FROM api_keys
      WHERE route_strategy_id IN (${sqlPlaceholders(routeIds.length)})
    `)
    .get(...routeIds) as { total?: number } | undefined
  const total = Number(countRow?.total ?? 0)
  const rows = database
    .prepare(`
      SELECT id AS apiKeyId, route_strategy_id AS routeStrategyId
      FROM api_keys
      WHERE route_strategy_id IN (${sqlPlaceholders(routeIds.length)})
      ORDER BY id ASC
      LIMIT ?
    `)
    .all(...routeIds, maxDeletedGroupAffectedApiKeyRouteSamples) as Array<{ apiKeyId: string; routeStrategyId: string }>
  const items = rows.flatMap((row) => {
    const change = changeByRouteStrategyId.get(row.routeStrategyId)
    return change ? [{ ...change, apiKeyId: row.apiKeyId }] : []
  })
  return {
    items,
    total,
    truncated: total > items.length
  }
}

async function preserveRouteStrategiesBeforeGroupDeleteAsync(
  client: DatabaseClient,
  groupId: string,
  groupName?: string
): Promise<DeletedGroupRouteStrategyChange[]> {
  const affectedRouteStrategies = await client.query<RouteStrategyGroupAvailabilityLossCandidate>(`
    SELECT
      route_strategy_groups.route_strategy_id AS id,
      route_strategies.name,
      route_strategies.system_account_id AS "systemAccountId",
      route_strategy_groups.status AS "targetBindingStatus"
    FROM ${groupWriteTable(client, 'route_strategy_groups')} route_strategy_groups
    INNER JOIN ${groupWriteTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = route_strategy_groups.route_strategy_id
      AND route_strategies.system_account_id = route_strategy_groups.system_account_id
      AND route_strategies.status = 'active'
    WHERE route_strategy_groups.group_id = ?
    ORDER BY route_strategy_groups.route_strategy_id ASC
    LIMIT ?
  `, [groupId, maxRouteStrategyAvailabilityLossCandidates + 1])
  if (!affectedRouteStrategies.length) return []
  if (affectedRouteStrategies.length > maxRouteStrategyAvailabilityLossCandidates) {
    throw new Error(`该分组关联的策略路由超过 ${maxRouteStrategyAvailabilityLossCandidates} 个，请先分批解除绑定后再删除分组`)
  }

  await assertAffectedRouteStrategiesCanLoseGroupAvailabilityAsync(client, groupId, affectedRouteStrategies, '删除分组', groupName)

  return affectedRouteStrategies.map((routeStrategy) => {
    return {
      routeStrategyId: routeStrategy.id,
      routeStrategyName: routeStrategy.name,
      removedGroupId: groupId,
      removedGroupName: groupName,
      removedBindingStatus: routeStrategy.targetBindingStatus ?? undefined
    }
  })
}

async function loadDeletedGroupApiKeyRouteChangesAsync(
  client: DatabaseClient,
  routeChanges: DeletedGroupRouteStrategyChange[]
): Promise<{ items: DeletedGroupApiKeyRouteChange[]; total: number; truncated: boolean }> {
  const routeIds = [...new Set(routeChanges.map((change) => change.routeStrategyId).filter(Boolean))]
  if (!routeIds.length) return emptyDeletedGroupApiKeyRouteChanges()
  const changeByRouteStrategyId = new Map(routeChanges.map((change) => [change.routeStrategyId, change]))
  const countRow = await client.one<{ total?: number | string }>(`
    SELECT COUNT(*) AS total
    FROM ${groupWriteTable(client, 'api_keys')}
    WHERE route_strategy_id IN (${client.dialect.bindPlaceholders(routeIds.length)})
  `, routeIds)
  const total = Number(countRow?.total ?? 0)
  const rows = await client.query<{ apiKeyId: string; routeStrategyId: string }>(`
    SELECT id AS "apiKeyId", route_strategy_id AS "routeStrategyId"
    FROM ${groupWriteTable(client, 'api_keys')}
    WHERE route_strategy_id IN (${client.dialect.bindPlaceholders(routeIds.length)})
    ORDER BY id ASC
    LIMIT ?
  `, [...routeIds, maxDeletedGroupAffectedApiKeyRouteSamples])
  const items = rows.flatMap((row) => {
    const change = changeByRouteStrategyId.get(row.routeStrategyId)
    return change ? [{ ...change, apiKeyId: row.apiKeyId }] : []
  })
  return {
    items,
    total,
    truncated: total > items.length
  }
}

function assertGroupNameAvailable(systemAccountId: string, providerCode: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, providerCode, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getBusinessDatabase()
    .prepare(`SELECT id FROM groups WHERE system_account_id = ? AND provider_code = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`同一供应商下分组名称已存在：${name}`)
  }
}

async function assertGroupNameAvailableAsync(client: DatabaseClient, systemAccountId: string, providerCode: string, name: string, excludeId?: string): Promise<void> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${groupWriteTable(client, 'groups')}
    WHERE system_account_id = ?
      AND provider_code = ?
      AND lower(name) = lower(?)
      AND id <> ?
    LIMIT 1
  `, [systemAccountId, providerCode, name, excludeId ?? ''])
  if (row?.id) {
    throw new Error(`同一供应商下分组名称已存在：${name}`)
  }
}

function emptyDeleteGroupResult(): DeleteGroupResult {
  return {
    deleted: false,
    affectedRouteStrategies: [],
    affectedApiKeyRoutes: [],
    affectedApiKeyRouteCount: 0,
    affectedApiKeyRoutesTruncated: false
  }
}

function emptyDeletedGroupApiKeyRouteChanges(): { items: DeletedGroupApiKeyRouteChange[]; total: number; truncated: boolean } {
  return {
    items: [],
    total: 0,
    truncated: false
  }
}

async function lockGroupMutationRowAsync(client: DatabaseClient, groupId: string, systemAccountId: string): Promise<void> {
  const lockClause = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  await client.one<{ id?: string }>(`
    SELECT id
    FROM ${groupWriteTable(client, 'groups')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1${lockClause}
  `, [groupId, systemAccountId])
}

function isDuplicateGroupNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_groups_owner_provider_name_unique_lower')
    || error.message.includes('idx_groups_owner_protocol_profile_name_unique_lower')
}

function writeSystemAccountId(access?: AccessScope): string {
  return manageableSystemAccountId(access) ?? currentSystemAccountId(access)
}

async function groupOwnerAndProviderAsync(groupId: string): Promise<{ systemAccountId: string; providerCode: ProviderCode; providerProtocolProfileId: string; protocolCode: string; protocolVersion: string; name?: string } | undefined> {
  const client = await getGroupWriteDatabaseClient()
  const row = await client.one<{ system_account_id?: string; provider_code?: ProviderCode; provider_protocol_profile_id?: string; protocol_code?: string; protocol_version?: string; name?: string }>(`
    SELECT system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name
    FROM ${groupWriteTable(client, 'groups')}
    WHERE id = ?
  `, [groupId])
  return row?.system_account_id && row.provider_code && row.provider_protocol_profile_id && row.protocol_code && row.protocol_version
    ? {
        systemAccountId: row.system_account_id,
        providerCode: row.provider_code,
        providerProtocolProfileId: row.provider_protocol_profile_id,
        protocolCode: row.protocol_code,
        protocolVersion: row.protocol_version,
        name: row.name
      }
    : undefined
}

async function getGroupWriteDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function groupWriteTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function refreshGroupAccountStatsAfterWriteForCurrentDriver(input: Parameters<typeof refreshGroupAccountStatsAfterWrite>[0]): void {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return
  }
  refreshGroupAccountStatsAfterWrite(input)
}

function invalidateGatewayRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
}
