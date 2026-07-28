import type { DatabaseSync } from 'node:sqlite'
import { isDeepStrictEqual } from 'node:util'

import type { GroupSchedulingPolicy, GroupSummary, GroupType, ProviderCode } from '../domain/types.js'
import { groupSchedulingPolicyJson, normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { maxRouteStrategyAvailabilityLossCandidates } from './route-strategy-group-binding-limits.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { emptyGroupAccountStats } from './group-account-stats.mapper.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { findGroupSummary, findGroupSummaryAsync } from './group-summary.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { sqlPlaceholders } from './query-utils.js'
import {
  canManageResourceOwner as canManageAuthorizedResourceOwner,
  groupOwnerAndProvider
} from './resource-authorization-helpers.js'
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
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const groupUpdateInputKeys = new Set([
  'name',
  'providerCode',
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
  const groupType = normalizeGroupType(input.groupType)
  const schedulingPolicyJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), groupType)
  const name = requiredTextInput(input.name, '分组名称')
  const enabled = normalizeOptionalBooleanInput(input, 'enabled', true, '分组启用状态')
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name,
    providerCode,
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
      .prepare('INSERT INTO groups (id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)')
      .run(group.id, systemAccountId, group.name, group.providerCode, group.description ?? null, group.enabled ? 1 : 0, group.groupType, schedulingPolicyJson, now, now)
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
  const client = await getGroupWriteDatabaseClient()
  return client.transaction(async (tx) => createGroupInClientAsync(tx, input, access))
}

export async function createGroupInClientAsync(client: DatabaseClient, input: Record<string, unknown>, access?: AccessScope): Promise<GroupSummary> {
  assertKnownInputKeys(input, groupCreateInputKeys, '分组创建参数')
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = requiredTextInput(input.providerCode, '供应商')
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
    description: normalizeNullableTextInput(input.description, '分组说明'),
    enabled,
    isDefault: false,
    groupType,
    schedulingPolicy: parseGroupSchedulingPolicyJson(schedulingPolicyJson, groupType),
    accountIds: [],
    accountStats: emptyGroupAccountStats()
  }
  try {
    await client.execute(`
        INSERT INTO ${groupWriteTable(client, 'groups')} (
          id, system_account_id, name, provider_code, description, enabled, is_default, group_type, scheduling_policy_json,
          created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)
      `, [
        group.id,
        systemAccountId,
        group.name,
        group.providerCode,
        group.description ?? null,
        group.enabled ? 1 : 0,
        group.groupType,
        schedulingPolicyJson,
        now,
        now
      ])
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
  const mutation = patchGroup(id, input, access)
  return mutation ? findGroupSummary(id, access) : undefined
}

export async function updateGroupAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<GroupSummary | undefined> {
  const mutation = await patchGroupAsync(id, input, access)
  return mutation ? await findGroupSummaryAsync(id, access) : undefined
}

export interface GroupManagementPatchChange {
  field: string
  before: unknown
  after: unknown
}

export interface GroupManagementPatchResult {
  id: string
  name: string
  ownerSystemAccountId: string
  accessType: 'owner' | 'authorized'
  changedFields: string[]
  changes: GroupManagementPatchChange[]
}

interface GroupPatchRow {
  id: string
  system_account_id: string
  name: string
  is_default: number | boolean | string
  access_type: 'owner' | 'authorized'
  authorization_id?: string | null
  settings_exists: number | boolean | string
  provider_code?: ProviderCode | null
  description?: string | null
  enabled?: number | boolean | string | null
  source_enabled?: number | boolean | string | null
  group_type?: GroupType | null
  scheduling_policy_json?: string | null
}

interface GroupPatchPlan {
  columns: Map<string, unknown>
  changes: GroupManagementPatchChange[]
  name: string
}

export function patchGroup(id: string, input: Record<string, unknown>, access?: AccessScope): GroupManagementPatchResult | undefined {
  assertKnownInputKeys(input, groupUpdateInputKeys, '分组更新参数')
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  let result: GroupManagementPatchResult | undefined
  try {
    const current = findGroupPatchRow(database, id, input, access)
    if (!current) {
      commitDatabaseTransaction(database, transactionStarted)
      return undefined
    }
    if (current.access_type === 'owner' && databaseBoolean(current.is_default)) {
      throw new DefaultGroupReadonlyError()
    }
    if (current.access_type === 'authorized') {
      assertKnownInputKeys(input, authorizedGroupSettingsInputKeys, '授权分组使用配置')
    }
    const plan = buildGroupPatchPlan(current, input)
    if (plan.columns.size > 0) {
      if (current.access_type === 'authorized') {
        applyAuthorizedGroupPatch(database, current, plan, access)
      } else {
        applyOwnedGroupPatch(database, current, plan)
      }
    }
    result = groupPatchResult(current, plan)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${normalizedPatchGroupName(input)}`)
    }
    throw error
  }
  finalizeGroupPatch(result)
  return result
}

export async function patchGroupAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<GroupManagementPatchResult | undefined> {
  assertKnownInputKeys(input, groupUpdateInputKeys, '分组更新参数')
  const client = await getGroupWriteDatabaseClient()
  let result: GroupManagementPatchResult | undefined
  try {
    result = await client.transaction(async (tx) => {
      let current = await findGroupPatchRowAsync(tx, id, input, access)
      if (!current) return undefined
      if (tx.driver === 'postgres') {
        await lockGroupMutationRowAsync(tx, id, current.system_account_id)
        if (current.authorization_id) await lockGroupAuthorizationMutationRowAsync(tx, current.authorization_id)
        current = await findGroupPatchRowAsync(tx, id, input, access)
        if (!current) return undefined
      }
      if (current.access_type === 'owner' && databaseBoolean(current.is_default)) {
        throw new DefaultGroupReadonlyError()
      }
      if (current.access_type === 'authorized') {
        assertKnownInputKeys(input, authorizedGroupSettingsInputKeys, '授权分组使用配置')
      }
      const plan = buildGroupPatchPlan(current, input)
      if (plan.columns.size > 0) {
        if (current.access_type === 'authorized') {
          await applyAuthorizedGroupPatchAsync(tx, current, plan, access)
        } else {
          await applyOwnedGroupPatchAsync(tx, current, plan)
        }
      }
      return groupPatchResult(current, plan)
    })
  } catch (error) {
    if (isDuplicateGroupNameError(error)) {
      throw new Error(`同一供应商下分组名称已存在：${normalizedPatchGroupName(input)}`)
    }
    throw error
  }
  finalizeGroupPatch(result)
  return result
}

function buildGroupPatchPlan(current: GroupPatchRow, input: Record<string, unknown>): GroupPatchPlan {
  const columns = new Map<string, unknown>()
  const changes: GroupManagementPatchChange[] = []
  const addChange = (field: string, before: unknown, after: unknown): void => {
    if (isDeepStrictEqual(before, after)) return
    changes.push({ field, before, after })
  }
  const setColumn = (column: string, before: unknown, after: unknown, storedValue = after): void => {
    if (!isDeepStrictEqual(before, after)) columns.set(column, storedValue)
  }

  let nextName = current.name
  if (hasOwnInput(input, 'name')) {
    nextName = requiredTextInput(input.name, '分组名称')
    setColumn('name', current.name, nextName)
    addChange('name', current.name, nextName)
  }
  if (hasOwnInput(input, 'providerCode')) {
    const before = requiredPatchText(current.provider_code, '供应商')
    const after = requiredTextInput(input.providerCode, '供应商')
    setColumn('provider_code', before, after)
    addChange('providerCode', before, after)
  }
  if (hasOwnInput(input, 'description')) {
    const before = current.description ?? undefined
    const after = normalizeNullableTextInput(input.description, '分组说明')
    setColumn('description', before, after, after ?? null)
    addChange('description', before, after)
  }
  if (hasOwnInput(input, 'enabled')) {
    const before = databaseBoolean(current.enabled)
    const after = normalizeOptionalBooleanInput(input, 'enabled', before, current.access_type === 'authorized' ? '授权分组启用状态' : '分组启用状态')
    setColumn('enabled', before, after, after ? 1 : 0)
    addChange('enabled', before, after)
  }

  const hasGroupTypeInput = hasOwnInput(input, 'groupType')
  const hasSchedulingPolicyInput = hasGroupSchedulingPolicyInput(input)
  if (hasGroupTypeInput || hasSchedulingPolicyInput) {
    const beforeGroupType = normalizeGroupType(current.group_type)
    const afterGroupType = hasGroupTypeInput ? normalizeGroupType(input.groupType) : beforeGroupType
    if (hasGroupTypeInput) {
      setColumn('group_type', beforeGroupType, afterGroupType)
      addChange('groupType', beforeGroupType, afterGroupType)
    }
    if (hasSchedulingPolicyInput) {
      const afterJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), afterGroupType)
      const beforePolicy = beforeGroupType === afterGroupType
        ? parseGroupSchedulingPolicyJson(current.scheduling_policy_json, beforeGroupType)
        : undefined
      const afterPolicy = parseGroupSchedulingPolicyJson(afterJson, afterGroupType)
      setColumn('scheduling_policy_json', beforePolicy, afterPolicy, afterJson)
      addChange('schedulingPolicy', beforePolicy, afterPolicy)
    } else if (beforeGroupType !== afterGroupType) {
      columns.set('scheduling_policy_json', groupSchedulingPolicyJson(undefined, afterGroupType))
    }
  }
  return { columns, changes, name: nextName }
}

function applyOwnedGroupPatch(database: DatabaseSync, current: GroupPatchRow, plan: GroupPatchPlan): void {
  if (plan.columns.has('provider_code') && ownedGroupHasAccounts(database, current.id)) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  if (plan.columns.get('enabled') === 0 && databaseBoolean(current.enabled)) {
    assertRouteStrategiesCanLoseGroupAvailability(database, current.id, current.name, '停用分组')
  }
  const assignments = [...plan.columns.keys()].map((column) => `${column} = ?`)
  database.prepare(`UPDATE groups SET ${assignments.join(', ')}, updated_at = ? WHERE id = ? AND system_account_id = ?`)
    .run(...plan.columns.values(), nowIso(), current.id, current.system_account_id)
}

async function applyOwnedGroupPatchAsync(client: DatabaseClient, current: GroupPatchRow, plan: GroupPatchPlan): Promise<void> {
  if (plan.columns.has('provider_code') && await ownedGroupHasAccountsAsync(client, current.id)) {
    throw new Error('已有账户的分组不允许修改供应商')
  }
  if (plan.columns.get('enabled') === 0 && databaseBoolean(current.enabled)) {
    await assertRouteStrategiesCanLoseGroupAvailabilityAsync(client, current.id, current.name, '停用分组')
  }
  const assignments = [...plan.columns.keys()].map((column) => `${client.dialect.quoteIdentifier(column)} = ?`)
  await client.execute(`
    UPDATE ${groupWriteTable(client, 'groups')}
    SET ${assignments.join(', ')}, updated_at = ?
    WHERE id = ? AND system_account_id = ?
  `, [...plan.columns.values(), nowIso(), current.id, current.system_account_id])
}

function applyAuthorizedGroupPatch(database: DatabaseSync, current: GroupPatchRow, plan: GroupPatchPlan, access?: AccessScope): void {
  const authorizationId = current.authorization_id
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!authorizationId || !granteeSystemAccountId) throw new Error('授权分组归属数据异常，请刷新后重试')
  if (plan.columns.get('enabled') === 0 && databaseBoolean(current.enabled) && databaseBoolean(current.source_enabled)) {
    assertRouteStrategiesCanLoseGroupAvailability(database, current.id, current.name, '停用授权分组', granteeSystemAccountId)
  }
  if (databaseBoolean(current.settings_exists)) {
    const assignments = [...plan.columns.keys()].map((column) => `${column} = ?`)
    database.prepare(`UPDATE group_authorization_settings SET ${assignments.join(', ')}, updated_at = ? WHERE authorization_id = ? AND system_account_id = ? AND group_id = ?`)
      .run(...plan.columns.values(), nowIso(), authorizationId, granteeSystemAccountId, current.id)
    return
  }
  const initial = authorizedGroupSettingsInsertValues(database, current, plan)
  database.prepare(`
    INSERT INTO group_authorization_settings (
      authorization_id, system_account_id, group_id, enabled, group_type,
      scheduling_policy_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `).run(authorizationId, granteeSystemAccountId, current.id, initial.enabled, initial.groupType, initial.schedulingPolicyJson, initial.now, initial.now)
}

async function applyAuthorizedGroupPatchAsync(client: DatabaseClient, current: GroupPatchRow, plan: GroupPatchPlan, access?: AccessScope): Promise<void> {
  const authorizationId = current.authorization_id
  const granteeSystemAccountId = userVisibleSystemAccountId(access)
  if (!authorizationId || !granteeSystemAccountId) throw new Error('授权分组归属数据异常，请刷新后重试')
  if (plan.columns.get('enabled') === 0 && databaseBoolean(current.enabled) && databaseBoolean(current.source_enabled)) {
    await assertRouteStrategiesCanLoseGroupAvailabilityAsync(client, current.id, current.name, '停用授权分组', granteeSystemAccountId)
  }
  if (databaseBoolean(current.settings_exists)) {
    const assignments = [...plan.columns.keys()].map((column) => `${client.dialect.quoteIdentifier(column)} = ?`)
    await client.execute(`
      UPDATE ${groupWriteTable(client, 'group_authorization_settings')}
      SET ${assignments.join(', ')}, updated_at = ?
      WHERE authorization_id = ? AND system_account_id = ? AND group_id = ?
    `, [...plan.columns.values(), nowIso(), authorizationId, granteeSystemAccountId, current.id])
    return
  }
  const initial = await authorizedGroupSettingsInsertValuesAsync(client, current, plan)
  await client.execute(`
    INSERT INTO ${groupWriteTable(client, 'group_authorization_settings')} (
      authorization_id, system_account_id, group_id, enabled, group_type,
      scheduling_policy_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `, [authorizationId, granteeSystemAccountId, current.id, initial.enabled, initial.groupType, initial.schedulingPolicyJson, initial.now, initial.now])
}

function authorizedGroupSettingsInsertValues(database: DatabaseSync, current: GroupPatchRow, plan: GroupPatchPlan) {
  const base = database.prepare('SELECT group_type, scheduling_policy_json FROM groups WHERE id = ? AND system_account_id = ? LIMIT 1')
    .get(current.id, current.system_account_id) as unknown as { group_type?: GroupType | null; scheduling_policy_json?: string | null } | undefined
  return authorizedGroupSettingsValues(current, plan, base)
}

async function authorizedGroupSettingsInsertValuesAsync(client: DatabaseClient, current: GroupPatchRow, plan: GroupPatchPlan) {
  const base = await client.one<{ group_type?: GroupType | null; scheduling_policy_json?: string | null }>(`
    SELECT group_type, scheduling_policy_json
    FROM ${groupWriteTable(client, 'groups')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1
  `, [current.id, current.system_account_id])
  return authorizedGroupSettingsValues(current, plan, base)
}

function authorizedGroupSettingsValues(
  current: GroupPatchRow,
  plan: GroupPatchPlan,
  base: { group_type?: GroupType | null; scheduling_policy_json?: string | null } | undefined
) {
  if (!base) throw new Error('授权分组归属数据异常，请刷新后重试')
  const groupType = (plan.columns.get('group_type') ?? normalizeGroupType(base.group_type)) as GroupType
  const sourceSchedulingPolicyJson = groupType === 'high_concurrency'
    ? groupSchedulingPolicyJson(writableSchedulingPolicyInput(parseGroupSchedulingPolicyJson(base.scheduling_policy_json, groupType)), groupType)
    : null
  return {
    enabled: plan.columns.get('enabled') ?? 1,
    groupType,
    schedulingPolicyJson: plan.columns.has('scheduling_policy_json') ? plan.columns.get('scheduling_policy_json') ?? null : sourceSchedulingPolicyJson,
    now: nowIso()
  }
}

function groupPatchResult(current: GroupPatchRow, plan: GroupPatchPlan): GroupManagementPatchResult {
  return {
    id: current.id,
    name: plan.name,
    ownerSystemAccountId: current.system_account_id,
    accessType: current.access_type,
    changedFields: plan.changes.map((change) => change.field).sort(),
    changes: plan.changes
  }
}

function finalizeGroupPatch(result: GroupManagementPatchResult | undefined): void {
  if (!result?.changedFields.length) return
  if (result.accessType === 'owner') invalidateGroupLookupCache(result.id)
  invalidateGatewayRuntimeAfterBusinessWrite(result.accessType === 'authorized' ? 'group_authorization_settings_updated' : 'group_updated')
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
    await refreshGroupAccountStatsAfterWriteAsync({ groupIds: [id], reason: 'group_deleted' })
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
  return error.message.includes('idx_groups_owner_provider_name_unique')
    || error.message.includes('idx_groups_owner_provider_name_unique_lower')
    || error.message.includes('UNIQUE constraint failed: groups.system_account_id, groups.provider_code, groups.name')
}

function writeSystemAccountId(access?: AccessScope): string {
  return manageableSystemAccountId(access) ?? currentSystemAccountId(access)
}

async function groupOwnerAndProviderAsync(groupId: string): Promise<{ systemAccountId: string; providerCode: ProviderCode; name?: string } | undefined> {
  const client = await getGroupWriteDatabaseClient()
  const row = await client.one<{ system_account_id?: string; provider_code?: ProviderCode; name?: string }>(`
    SELECT system_account_id, provider_code, name
    FROM ${groupWriteTable(client, 'groups')}
    WHERE id = ?
  `, [groupId])
  return row?.system_account_id && row.provider_code
    ? {
        systemAccountId: row.system_account_id,
        providerCode: row.provider_code,
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

function invalidateGatewayRuntimeAfterBusinessWrite(reason: string): void {
  notifyGatewayRuntimeCacheInvalidation(reason)
}
