import type { DatabaseSync } from 'node:sqlite'

import type { GroupSchedulingPolicy, GroupSummary, ProviderCode } from '../domain/types.js'
import { groupSchedulingPolicyJson, normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { maxGroupDeleteAffectedApiKeyRoutes } from './api-key-group-binding-limits.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { emptyGroupAccountStats } from './group-account-stats.mapper.js'
import { refreshGroupAccountStatsAfterWrite } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { findGroupSummary, findGroupSummaryAsync } from './group-summary.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { requireEnabledProviderProtocolProfile, requireEnabledProviderProtocolProfileAsync } from './provider.repository.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
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
  assertGroupNameAvailable(systemAccountId, providerProfile.id, name)
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
      throw new Error(`同一协议档案下分组名称已存在：${group.name}`)
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
      await assertGroupNameAvailableAsync(tx, systemAccountId, providerProfile.id, name)
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
      throw new Error(`同一协议档案下分组名称已存在：${group.name}`)
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
  if ((next.providerCode !== current.providerCode || next.providerProtocolProfileId !== current.providerProtocolProfileId) && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商或协议档案')
  }
  const providerProfile = requireEnabledProviderProtocolProfile(next.providerCode, next.providerProtocolProfileId)
  next.providerProtocolProfileId = providerProfile.id
  next.protocolCode = providerProfile.protocolCode
  next.protocolVersion = providerProfile.protocolVersion
  assertGroupNameAvailable(systemAccountId, providerProfile.id, next.name, id)
  const database = getBusinessDatabase()
  try {
    database
      .prepare('UPDATE groups SET name = ?, provider_code = ?, provider_protocol_profile_id = ?, protocol_code = ?, protocol_version = ?, description = ?, enabled = ?, group_type = ?, scheduling_policy_json = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
      .run(next.name, next.providerCode, next.providerProtocolProfileId, next.protocolCode, next.protocolVersion, next.description ?? null, next.enabled ? 1 : 0, next.groupType, groupSchedulingPolicyJson(nextSchedulingPolicyInput, nextGroupType), nowIso(), id, systemAccountId)
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一协议档案下分组名称已存在：${next.name}`)
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
  if ((next.providerCode !== current.providerCode || next.providerProtocolProfileId !== current.providerProtocolProfileId) && current.accountStats.total > 0) {
    throw new Error('已有账户的分组不允许修改供应商或协议档案')
  }
  const providerProfile = await requireEnabledProviderProtocolProfileAsync(next.providerCode, next.providerProtocolProfileId)
  next.providerProtocolProfileId = providerProfile.id
  next.protocolCode = providerProfile.protocolCode
  next.protocolVersion = providerProfile.protocolVersion
  await assertGroupNameAvailableAsync(await getGroupWriteDatabaseClient(), owner.systemAccountId, providerProfile.id, next.name, id)
  const client = await getGroupWriteDatabaseClient()
  await client.transaction(async (tx) => {
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

export interface DeletedGroupApiKeyRouteChange {
  apiKeyId: string
  apiKeyName: string
  removedGroupId: string
  removedGroupName?: string
  removedBindingStatus?: string
}

export interface DeleteGroupResult {
  deleted: boolean
  affectedApiKeyRoutes: DeletedGroupApiKeyRouteChange[]
}

export function deleteGroup(id: string, access?: AccessScope): DeleteGroupResult {
  const current = findGroupSummary(id, access)
  if (current?.isDefault) {
    throw new Error('默认分组不能删除')
  }
  const owner = groupOwnerAndProvider(id)
  if (!owner || !canManageAuthorizedResourceOwner(owner.systemAccountId, access)) {
    return { deleted: false, affectedApiKeyRoutes: [] }
  }
  const database = getBusinessDatabase()
  let deleted = false
  const affectedApiKeyRoutes = preserveApiKeyRoutesBeforeGroupDelete(database, id, owner.systemAccountId, current?.name)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM api_key_group_bindings WHERE group_id = ?').run(id)
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
  return { deleted, affectedApiKeyRoutes: deleted ? affectedApiKeyRoutes : [] }
}

export async function deleteGroupAsync(id: string, access?: AccessScope): Promise<DeleteGroupResult> {
  const current = await findGroupSummaryAsync(id, access)
  if (current?.isDefault) {
    throw new Error('默认分组不能删除')
  }
  const owner = await groupOwnerAndProviderAsync(id)
  if (!owner || !canManageAuthorizedResourceOwner(owner.systemAccountId, access)) {
    return { deleted: false, affectedApiKeyRoutes: [] }
  }
  const client = await getGroupWriteDatabaseClient()
  const affectedApiKeyRoutes = await preserveApiKeyRoutesBeforeGroupDeleteAsync(client, id, owner.systemAccountId, current?.name)
  let deleted = false
  await client.transaction(async (tx) => {
    await tx.execute(`
      DELETE FROM ${groupWriteTable(tx, 'api_key_group_bindings')}
      WHERE group_id = ?
    `, [id])
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
  return { deleted, affectedApiKeyRoutes: deleted ? affectedApiKeyRoutes : [] }
}

type ApiKeyAffectedByGroupDeleteRow = {
  id: string
  name: string
  systemAccountId: string
  targetBindingStatus?: string | null
}

function preserveApiKeyRoutesBeforeGroupDelete(
  database: DatabaseSync,
  groupId: string,
  systemAccountId: string,
  groupName?: string
): DeletedGroupApiKeyRouteChange[] {
  const affectedApiKeys = database
    .prepare(`
      SELECT
        api_key_group_bindings.api_key_id AS id,
        api_keys.name,
        api_keys.system_account_id AS systemAccountId,
        api_key_group_bindings.status AS targetBindingStatus
      FROM api_key_group_bindings
      INNER JOIN api_keys
        ON api_keys.id = api_key_group_bindings.api_key_id
        AND api_keys.system_account_id = api_key_group_bindings.system_account_id
      WHERE api_key_group_bindings.group_id = ?
      ORDER BY api_key_group_bindings.api_key_id ASC
      LIMIT ?
    `)
    .all(groupId, maxGroupDeleteAffectedApiKeyRoutes + 1) as unknown as ApiKeyAffectedByGroupDeleteRow[]
  if (!affectedApiKeys.length) return []
  if (affectedApiKeys.length > maxGroupDeleteAffectedApiKeyRoutes) {
    throw new Error(`该分组关联的 API Key 超过 ${maxGroupDeleteAffectedApiKeyRoutes} 个，请先分批解除绑定后再删除分组`)
  }

  const activeBindingCountByApiKeyId = loadActiveApiKeyGroupCountExcludingGroup(
    database,
    groupId,
    affectedApiKeys.map((apiKey) => apiKey.id)
  )
  const blockers = affectedApiKeys.filter((apiKey) => {
    if (apiKey.systemAccountId !== systemAccountId) return false
    if (apiKey.targetBindingStatus !== 'active') return false
    return (activeBindingCountByApiKeyId.get(apiKey.id) ?? 0) === 0
  })
  if (blockers.length) {
    const names = blockers.slice(0, 3).map((apiKey) => apiKey.name).join('、')
    const suffix = blockers.length > 3 ? ` 等 ${blockers.length} 个` : ''
    throw new Error(`无法删除分组：该分组仍是以下 API Key 的唯一启用号池：${names}${suffix}。请先到 API Key 管理中为这些 Key 新增并启用其他分组，或删除这些 API Key 后再删除分组。`)
  }

  return affectedApiKeys.map((apiKey) => {
    return {
      apiKeyId: apiKey.id,
      apiKeyName: apiKey.name,
      removedGroupId: groupId,
      removedGroupName: groupName,
      removedBindingStatus: apiKey.targetBindingStatus ?? undefined
    }
  })
}

async function preserveApiKeyRoutesBeforeGroupDeleteAsync(
  client: DatabaseClient,
  groupId: string,
  systemAccountId: string,
  groupName?: string
): Promise<DeletedGroupApiKeyRouteChange[]> {
  const affectedApiKeys = await client.query<ApiKeyAffectedByGroupDeleteRow>(`
    SELECT
      api_key_group_bindings.api_key_id AS id,
      api_keys.name,
      api_keys.system_account_id AS "systemAccountId",
      api_key_group_bindings.status AS "targetBindingStatus"
    FROM ${groupWriteTable(client, 'api_key_group_bindings')} api_key_group_bindings
    INNER JOIN ${groupWriteTable(client, 'api_keys')} api_keys
      ON api_keys.id = api_key_group_bindings.api_key_id
      AND api_keys.system_account_id = api_key_group_bindings.system_account_id
    WHERE api_key_group_bindings.group_id = ?
    ORDER BY api_key_group_bindings.api_key_id ASC
    LIMIT ?
  `, [groupId, maxGroupDeleteAffectedApiKeyRoutes + 1])
  if (!affectedApiKeys.length) return []
  if (affectedApiKeys.length > maxGroupDeleteAffectedApiKeyRoutes) {
    throw new Error(`该分组关联的 API Key 超过 ${maxGroupDeleteAffectedApiKeyRoutes} 个，请先分批解除绑定后再删除分组`)
  }

  const activeBindingCountByApiKeyId = await loadActiveApiKeyGroupCountExcludingGroupAsync(
    client,
    groupId,
    affectedApiKeys.map((apiKey) => apiKey.id)
  )
  const blockers = affectedApiKeys.filter((apiKey) => {
    if (apiKey.systemAccountId !== systemAccountId) return false
    if (apiKey.targetBindingStatus !== 'active') return false
    return (activeBindingCountByApiKeyId.get(apiKey.id) ?? 0) === 0
  })
  if (blockers.length) {
    const names = blockers.slice(0, 3).map((apiKey) => apiKey.name).join('、')
    const suffix = blockers.length > 3 ? ` 等 ${blockers.length} 个` : ''
    throw new Error(`无法删除分组：该分组仍是以下 API Key 的唯一启用号池：${names}${suffix}。请先到 API Key 管理中为这些 Key 新增并启用其他分组，或删除这些 API Key 后再删除分组。`)
  }

  return affectedApiKeys.map((apiKey) => {
    return {
      apiKeyId: apiKey.id,
      apiKeyName: apiKey.name,
      removedGroupId: groupId,
      removedGroupName: groupName,
      removedBindingStatus: apiKey.targetBindingStatus ?? undefined
    }
  })
}

function loadActiveApiKeyGroupCountExcludingGroup(
  database: DatabaseSync,
  groupId: string,
  apiKeyIds: string[]
): Map<string, number> {
  const result = new Map<string, number>()
  const uniqueIds = [...new Set(apiKeyIds.filter(Boolean))]
  const now = nowIso()
  for (const chunk of chunkValues(uniqueIds, 500)) {
    const rows = database
      .prepare(`
        SELECT
          api_key_group_bindings.api_key_id AS apiKeyId,
          COUNT(*) AS activeBindingCount
        FROM api_key_group_bindings
        INNER JOIN groups
          ON groups.id = api_key_group_bindings.group_id
          AND groups.enabled = 1
        LEFT JOIN resource_authorizations group_authorization
          ON group_authorization.resource_type = 'group'
          AND group_authorization.resource_id = groups.id
          AND group_authorization.grantee_system_account_id = api_key_group_bindings.system_account_id
          AND group_authorization.status = 'active'
          AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
        LEFT JOIN group_authorization_settings
          ON group_authorization_settings.authorization_id = group_authorization.id
          AND group_authorization_settings.system_account_id = api_key_group_bindings.system_account_id
          AND group_authorization_settings.group_id = groups.id
        WHERE api_key_group_bindings.status = 'active'
          AND (
            groups.system_account_id = api_key_group_bindings.system_account_id
            OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
          )
          AND api_key_group_bindings.group_id <> ?
          AND api_key_group_bindings.api_key_id IN (${sqlPlaceholders(chunk.length)})
        GROUP BY api_key_group_bindings.api_key_id
    `)
      .all(now, groupId, ...chunk) as unknown as Array<{ apiKeyId: string; activeBindingCount: number }>
    for (const row of rows) {
      result.set(row.apiKeyId, Number(row.activeBindingCount) || 0)
    }
  }
  return result
}

async function loadActiveApiKeyGroupCountExcludingGroupAsync(
  client: DatabaseClient,
  groupId: string,
  apiKeyIds: string[]
): Promise<Map<string, number>> {
  const result = new Map<string, number>()
  const uniqueIds = [...new Set(apiKeyIds.filter(Boolean))]
  const now = nowIso()
  for (const chunk of chunkValues(uniqueIds, 500)) {
    const rows = await client.query<{ apiKeyId: string; activeBindingCount: number | string }>(`
      SELECT
        api_key_group_bindings.api_key_id AS "apiKeyId",
        COUNT(*) AS "activeBindingCount"
      FROM ${groupWriteTable(client, 'api_key_group_bindings')} api_key_group_bindings
      INNER JOIN ${groupWriteTable(client, 'groups')} groups
        ON groups.id = api_key_group_bindings.group_id
        AND groups.enabled = 1
      LEFT JOIN ${groupWriteTable(client, 'resource_authorizations')} group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = groups.id
        AND group_authorization.grantee_system_account_id = api_key_group_bindings.system_account_id
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      LEFT JOIN ${groupWriteTable(client, 'group_authorization_settings')} group_authorization_settings
        ON group_authorization_settings.authorization_id = group_authorization.id
        AND group_authorization_settings.system_account_id = api_key_group_bindings.system_account_id
        AND group_authorization_settings.group_id = groups.id
      WHERE api_key_group_bindings.status = 'active'
        AND (
          groups.system_account_id = api_key_group_bindings.system_account_id
          OR (group_authorization.id IS NOT NULL AND COALESCE(group_authorization_settings.enabled, 1) = 1)
        )
        AND api_key_group_bindings.group_id <> ?
        AND api_key_group_bindings.api_key_id IN (${client.dialect.bindPlaceholders(chunk.length)})
      GROUP BY api_key_group_bindings.api_key_id
    `, [now, groupId, ...chunk])
    for (const row of rows) {
      result.set(row.apiKeyId, Number(row.activeBindingCount) || 0)
    }
  }
  return result
}

function assertGroupNameAvailable(systemAccountId: string, providerProtocolProfileId: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, providerProtocolProfileId, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getBusinessDatabase()
    .prepare(`SELECT id FROM groups WHERE system_account_id = ? AND provider_protocol_profile_id = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`同一协议档案下分组名称已存在：${name}`)
  }
}

async function assertGroupNameAvailableAsync(client: DatabaseClient, systemAccountId: string, providerProtocolProfileId: string, name: string, excludeId?: string): Promise<void> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${groupWriteTable(client, 'groups')}
    WHERE system_account_id = ?
      AND provider_protocol_profile_id = ?
      AND lower(name) = lower(?)
      AND id <> ?
    LIMIT 1
  `, [systemAccountId, providerProtocolProfileId, name, excludeId ?? ''])
  if (row?.id) {
    throw new Error(`同一协议档案下分组名称已存在：${name}`)
  }
}

function isDuplicateGroupNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_groups_owner_protocol_profile_name_unique_lower')
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
