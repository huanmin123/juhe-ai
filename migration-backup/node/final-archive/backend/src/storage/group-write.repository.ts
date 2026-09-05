import type { DatabaseSync, SQLInputValue } from 'node:sqlite'
import { isDeepStrictEqual } from 'node:util'

import type { GroupSchedulingPolicy, GroupSummary, GroupType, ProviderCode } from '../domain/types.js'
import { groupSchedulingPolicyJson, normalizeGroupType, parseGroupSchedulingPolicyJson } from '../domain/group-scheduling.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import { canAccessAll, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { maxRouteStrategyAvailabilityLossCandidates } from './route-strategy-group-binding-limits.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { emptyGroupAccountStats } from './group-account-stats.mapper.js'
import { refreshGroupAccountStatsAfterWrite, refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { invalidateGroupAccountIdsCache } from './group-read-loaders.js'
import { findGroupSummary, findGroupSummaryAsync } from './group-summary.repository.js'
import { getPostgresPool } from './postgres-client.js'
import {
  invalidateGroupLookupCache,
  loadSystemAccountNameMapByIds,
  loadSystemAccountNameMapByIdsAsync
} from './repository-lookups.js'
import {
  assertKnownInputKeys,
  hasOwnInput,
  normalizeNullableTextInput,
  normalizeOptionalBooleanInput,
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
  'expectedUpdatedAt',
  'name',
  'providerCode',
  'description',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

const authorizedGroupSettingsInputKeys = new Set([
  'expectedUpdatedAt',
  'enabled',
  'groupType',
  'schedulingPolicy'
])

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

export interface GroupCreateStorageReceipt {
  group: GroupSummary
  ownerSystemAccountId: string
  updatedAt: string
}

export async function createGroupAsync(input: Record<string, unknown>, access?: AccessScope): Promise<GroupSummary> {
  return (await createGroupWithReceiptAsync(input, access)).group
}

export async function createGroupWithReceiptAsync(input: Record<string, unknown>, access?: AccessScope): Promise<GroupCreateStorageReceipt> {
  const client = await getGroupWriteDatabaseClient()
  return client.transaction(async (tx) => createGroupWithReceiptInClientAsync(tx, input, access))
}

export class GroupPatchConflictError extends Error {
  constructor() {
    super('分组已被其他操作更新，请刷新后重试')
    this.name = 'GroupPatchConflictError'
  }
}

export async function createGroupInClientAsync(client: DatabaseClient, input: Record<string, unknown>, access?: AccessScope): Promise<GroupSummary> {
  return (await createGroupWithReceiptInClientAsync(client, input, access)).group
}

async function createGroupWithReceiptInClientAsync(client: DatabaseClient, input: Record<string, unknown>, access?: AccessScope): Promise<GroupCreateStorageReceipt> {
  assertKnownInputKeys(input, groupCreateInputKeys, '分组创建参数')
  const now = nowIso()
  const systemAccountId = writeSystemAccountId(access)
  const providerCode = requiredTextInput(input.providerCode, '供应商')
  const groupType = normalizeGroupType(input.groupType)
  const schedulingPolicyJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), groupType)
  const name = requiredTextInput(input.name, '分组名称')
  const enabled = normalizeOptionalBooleanInput(input, 'enabled', true, '分组启用状态')
  const systemAccountName = includeSystemAccountFields(access)
    ? (await loadSystemAccountNameMapByIdsAsync(client, [systemAccountId])).get(systemAccountId)
    : undefined
  const group: GroupSummary = {
    id: newId('grp'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName,
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
  return { group, ownerSystemAccountId: systemAccountId, updatedAt: now }
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
  updatedAt: string
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
  updated_at: string
}

interface GroupPatchPlan {
  columns: Map<string, unknown>
  changes: GroupManagementPatchChange[]
  name: string
  updatedAt: string
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
    assertExpectedGroupUpdatedAt(current, input)
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
      if (current.access_type === 'authorized') {
        assertKnownInputKeys(input, authorizedGroupSettingsInputKeys, '授权分组使用配置')
      }
      if (tx.driver === 'postgres') {
        await lockGroupMutationRowAsync(tx, id, current.system_account_id)
        if (current.authorization_id) await lockGroupAuthorizationMutationRowAsync(tx, current.authorization_id)
        current = await findGroupPatchRowAsync(tx, id, input, access)
        if (!current) return undefined
      }
      if (current.access_type === 'owner' && databaseBoolean(current.is_default)) {
        throw new DefaultGroupReadonlyError()
      }
      assertExpectedGroupUpdatedAt(current, input)
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
    if (beforeGroupType !== afterGroupType) {
      const afterJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), afterGroupType)
      columns.set('scheduling_policy_json', afterJson)
      if (hasSchedulingPolicyInput) {
        const beforePolicy = parseGroupSchedulingPolicyJson(current.scheduling_policy_json, beforeGroupType)
        const afterPolicy = parseGroupSchedulingPolicyJson(afterJson, afterGroupType)
        addChange('schedulingPolicy', beforePolicy, afterPolicy)
      }
    } else if (hasSchedulingPolicyInput) {
      const beforePolicy = parseGroupSchedulingPolicyJson(current.scheduling_policy_json, beforeGroupType)
      const afterJson = groupSchedulingPolicyJson(groupSchedulingPolicyInput(input), afterGroupType)
      const afterPolicy = parseGroupSchedulingPolicyJson(afterJson, afterGroupType)
      setColumn('scheduling_policy_json', beforePolicy, afterPolicy, afterJson)
      addChange('schedulingPolicy', beforePolicy, afterPolicy)
    }
  }
  return { columns, changes, name: nextName, updatedAt: current.updated_at }
}

function applyOwnedGroupPatch(database: DatabaseSync, current: GroupPatchRow, plan: GroupPatchPlan): void {
  if (plan.columns.has('provider_code')) {
    const providerCode = requiredPatchText(plan.columns.get('provider_code'), '供应商')
    assertGroupPatchProviderAvailable(database, providerCode)
    if (ownedGroupHasAccounts(database, current.id)) {
      throw new Error('已有账户的分组不允许修改供应商')
    }
  }
  if (plan.columns.get('enabled') === 0 && databaseBoolean(current.enabled)) {
    assertRouteStrategiesCanLoseGroupAvailability(database, current.id, current.name, '停用分组')
  }
  const assignments = [...plan.columns.keys()].map((column) => `${column} = ?`)
  const updatedAt = nextGroupUpdatedAt(current.updated_at)
  const update = database.prepare(`UPDATE groups SET ${assignments.join(', ')}, updated_at = ? WHERE id = ? AND system_account_id = ? AND updated_at = ?`)
    .run(...sqlitePatchValues(plan.columns.values()), updatedAt, current.id, current.system_account_id, current.updated_at)
  assertGroupPatchApplied(update.changes)
  plan.updatedAt = updatedAt
}

async function applyOwnedGroupPatchAsync(client: DatabaseClient, current: GroupPatchRow, plan: GroupPatchPlan): Promise<void> {
  if (plan.columns.has('provider_code')) {
    const providerCode = requiredPatchText(plan.columns.get('provider_code'), '供应商')
    await assertGroupPatchProviderAvailableAsync(client, providerCode)
    if (await ownedGroupHasAccountsAsync(client, current.id)) {
      throw new Error('已有账户的分组不允许修改供应商')
    }
  }
  if (plan.columns.get('enabled') === 0 && databaseBoolean(current.enabled)) {
    await assertRouteStrategiesCanLoseGroupAvailabilityAsync(client, current.id, current.name, '停用分组')
  }
  const assignments = [...plan.columns.keys()].map((column) => `${client.dialect.quoteIdentifier(column)} = ?`)
  const updatedAt = nextGroupUpdatedAt(current.updated_at)
  const update = await client.execute(`
    UPDATE ${groupWriteTable(client, 'groups')}
    SET ${assignments.join(', ')}, updated_at = ?
    WHERE id = ? AND system_account_id = ? AND updated_at = ?
  `, [...plan.columns.values(), updatedAt, current.id, current.system_account_id, current.updated_at])
  assertGroupPatchApplied(update.changes)
  plan.updatedAt = updatedAt
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
    const updatedAt = nextGroupUpdatedAt(current.updated_at)
    const update = database.prepare(`UPDATE group_authorization_settings SET ${assignments.join(', ')}, updated_at = ? WHERE authorization_id = ? AND system_account_id = ? AND group_id = ? AND updated_at = ?`)
      .run(...sqlitePatchValues(plan.columns.values()), updatedAt, authorizationId, granteeSystemAccountId, current.id, current.updated_at)
    assertGroupPatchApplied(update.changes)
    plan.updatedAt = updatedAt
    return
  }
  const initial = authorizedGroupSettingsInsertValues(database, current, plan)
  database.prepare(`
    INSERT INTO group_authorization_settings (
      authorization_id, system_account_id, group_id, enabled, group_type,
      scheduling_policy_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `).run(authorizationId, granteeSystemAccountId, current.id, initial.enabled, initial.groupType, initial.schedulingPolicyJson, initial.now, initial.now)
  plan.updatedAt = initial.now
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
    const updatedAt = nextGroupUpdatedAt(current.updated_at)
    const update = await client.execute(`
      UPDATE ${groupWriteTable(client, 'group_authorization_settings')}
      SET ${assignments.join(', ')}, updated_at = ?
      WHERE authorization_id = ? AND system_account_id = ? AND group_id = ? AND updated_at = ?
    `, [...plan.columns.values(), updatedAt, authorizationId, granteeSystemAccountId, current.id, current.updated_at])
    assertGroupPatchApplied(update.changes)
    plan.updatedAt = updatedAt
    return
  }
  const initial = await authorizedGroupSettingsInsertValuesAsync(client, current, plan)
  await client.execute(`
    INSERT INTO ${groupWriteTable(client, 'group_authorization_settings')} (
      authorization_id, system_account_id, group_id, enabled, group_type,
      scheduling_policy_json, created_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
  `, [authorizationId, granteeSystemAccountId, current.id, initial.enabled, initial.groupType, initial.schedulingPolicyJson, initial.now, initial.now])
  plan.updatedAt = initial.now
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
): { enabled: number; groupType: GroupType; schedulingPolicyJson: string | null; now: string } {
  if (!base) throw new Error('授权分组归属数据异常，请刷新后重试')
  const groupTypeValue = plan.columns.has('group_type') ? plan.columns.get('group_type') : normalizeGroupType(base.group_type)
  if (groupTypeValue !== 'personal' && groupTypeValue !== 'high_concurrency') throw new Error('授权分组类型数据异常，请刷新后重试')
  const schedulingPolicyJson = plan.columns.has('scheduling_policy_json')
    ? nullablePatchText(plan.columns.get('scheduling_policy_json'))
    : groupTypeValue === 'high_concurrency'
      ? groupSchedulingPolicyJson(writableSchedulingPolicyInput(parseGroupSchedulingPolicyJson(base.scheduling_policy_json, groupTypeValue)), groupTypeValue)
      : null
  const enabledValue = plan.columns.has('enabled') ? plan.columns.get('enabled') : 1
  if (enabledValue !== 0 && enabledValue !== 1) throw new Error('授权分组启用状态数据异常，请刷新后重试')
  return {
    enabled: enabledValue,
    groupType: groupTypeValue,
    schedulingPolicyJson,
    now: nextGroupUpdatedAt(current.updated_at)
  }
}

function groupPatchResult(current: GroupPatchRow, plan: GroupPatchPlan): GroupManagementPatchResult {
  return {
    id: current.id,
    name: plan.name,
    ownerSystemAccountId: current.system_account_id,
    accessType: current.access_type,
    changedFields: plan.changes.map((change) => change.field).sort(),
    changes: plan.changes,
    updatedAt: plan.updatedAt
  }
}

function finalizeGroupPatch(result: GroupManagementPatchResult | undefined): void {
  if (!result?.changedFields.length) return
  const changedFields = new Set(result.changedFields)
  if (result.accessType === 'owner' && changedFields.has('name')) {
    invalidateGroupLookupCache(result.id)
  }
  const gatewayRuntimeChanged = result.accessType === 'authorized'
    || ['providerCode', 'enabled', 'groupType', 'schedulingPolicy'].some((field) => changedFields.has(field))
  if (gatewayRuntimeChanged) {
    invalidateGatewayRuntimeAfterBusinessWrite(result.accessType === 'authorized' ? 'group_authorization_settings_updated' : 'group_updated')
  }
}

interface GroupPatchTables {
  groups: string
  resourceAuthorizations: string
  authorizationSettings: string
}

interface GroupPatchQuery {
  sql: string
  params: string[]
}

function findGroupPatchRow(
  database: DatabaseSync,
  id: string,
  input: Record<string, unknown>,
  access?: AccessScope
): GroupPatchRow | undefined {
  const query = groupPatchRowQuery(id, input, access, {
    groups: 'groups',
    resourceAuthorizations: 'resource_authorizations',
    authorizationSettings: 'group_authorization_settings'
  })
  return database.prepare(query.sql).get(...query.params) as unknown as GroupPatchRow | undefined
}

async function findGroupPatchRowAsync(
  client: DatabaseClient,
  id: string,
  input: Record<string, unknown>,
  access?: AccessScope
): Promise<GroupPatchRow | undefined> {
  const query = groupPatchRowQuery(id, input, access, {
    groups: groupWriteTable(client, 'groups'),
    resourceAuthorizations: groupWriteTable(client, 'resource_authorizations'),
    authorizationSettings: groupWriteTable(client, 'group_authorization_settings')
  })
  return client.one<GroupPatchRow>(query.sql, query.params)
}

function groupPatchRowQuery(
  id: string,
  input: Record<string, unknown>,
  access: AccessScope | undefined,
  tables: GroupPatchTables
): GroupPatchQuery {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const columnPairs = groupPatchColumnPairs(input)
  const ownerColumns = columnPairs.map((column) => `${column.owner} AS ${column.alias}`).join(', ')
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return {
      sql: `SELECT ${ownerColumns} FROM ${tables.groups} groups WHERE groups.id = ? LIMIT 1`,
      params: [id]
    }
  }
  if (!viewerSystemAccountId) throw new Error('缺少系统账户上下文')
  const authorizedColumns = columnPairs.map((column) => `${column.authorized} AS ${column.alias}`).join(', ')
  const aliases = columnPairs.map((column) => column.alias).join(', ')
  const directOwnerSystemAccountId = ownerSystemAccountId ?? viewerSystemAccountId
  return {
    sql: `
      SELECT ${aliases}
      FROM (
        SELECT ${ownerColumns}
        FROM ${tables.groups} groups
        WHERE groups.id = ? AND groups.system_account_id = ?
        UNION ALL
        SELECT ${authorizedColumns}
        FROM ${tables.resourceAuthorizations} authorization_rows
        INNER JOIN ${tables.groups} groups ON groups.id = authorization_rows.resource_id
        LEFT JOIN ${tables.authorizationSettings} authorization_settings
          ON authorization_settings.authorization_id = authorization_rows.id
          AND authorization_settings.system_account_id = authorization_rows.grantee_system_account_id
          AND authorization_settings.group_id = authorization_rows.resource_id
        WHERE groups.id = ?
          AND authorization_rows.resource_type = 'group'
          AND authorization_rows.grantee_system_account_id = ?
          AND authorization_rows.status IN ('active', 'paused', 'expired')
          AND groups.system_account_id <> ?
      ) group_patch_target
      LIMIT 1
    `,
    params: [id, directOwnerSystemAccountId, id, viewerSystemAccountId, directOwnerSystemAccountId]
  }
}

function groupPatchColumnPairs(input: Record<string, unknown>): Array<{ alias: string; owner: string; authorized: string }> {
  const columns = [
    { alias: 'id', owner: 'groups.id', authorized: 'groups.id' },
    { alias: 'system_account_id', owner: 'groups.system_account_id', authorized: 'groups.system_account_id' },
    { alias: 'name', owner: 'groups.name', authorized: 'groups.name' },
    { alias: 'is_default', owner: 'groups.is_default', authorized: '0' },
    { alias: 'access_type', owner: "'owner'", authorized: "'authorized'" },
    { alias: 'authorization_id', owner: 'NULL', authorized: 'authorization_rows.id' },
    { alias: 'settings_exists', owner: '0', authorized: 'CASE WHEN authorization_settings.authorization_id IS NULL THEN 0 ELSE 1 END' },
    { alias: 'updated_at', owner: 'groups.updated_at', authorized: 'COALESCE(authorization_settings.updated_at, groups.updated_at)' }
  ]
  if (hasOwnInput(input, 'providerCode')) {
    columns.push({ alias: 'provider_code', owner: 'groups.provider_code', authorized: 'groups.provider_code' })
  }
  if (hasOwnInput(input, 'description')) {
    columns.push({ alias: 'description', owner: 'groups.description', authorized: 'groups.description' })
  }
  if (hasOwnInput(input, 'enabled')) {
    columns.push(
      { alias: 'enabled', owner: 'groups.enabled', authorized: 'COALESCE(authorization_settings.enabled, 1)' },
      { alias: 'source_enabled', owner: 'groups.enabled', authorized: 'groups.enabled' }
    )
  }
  if (hasOwnInput(input, 'groupType') || hasGroupSchedulingPolicyInput(input)) {
    columns.push({
      alias: 'group_type',
      owner: 'groups.group_type',
      authorized: 'COALESCE(authorization_settings.group_type, groups.group_type)'
    })
  }
  if (hasGroupSchedulingPolicyInput(input)) {
    const localGroupType = 'COALESCE(authorization_settings.group_type, groups.group_type)'
    columns.push({
      alias: 'scheduling_policy_json',
      owner: 'groups.scheduling_policy_json',
      authorized: `CASE WHEN ${localGroupType} = 'high_concurrency' THEN COALESCE(authorization_settings.scheduling_policy_json, groups.scheduling_policy_json) ELSE NULL END`
    })
  }
  return columns
}

function assertGroupPatchProviderAvailable(database: DatabaseSync, providerCode: string): void {
  const provider = database.prepare('SELECT enabled FROM providers WHERE code = ? LIMIT 1')
    .get(providerCode) as unknown as { enabled?: number | boolean | string } | undefined
  assertGroupPatchProviderRow(provider, providerCode)
}

async function assertGroupPatchProviderAvailableAsync(client: DatabaseClient, providerCode: string): Promise<void> {
  const provider = await client.one<{ enabled?: number | boolean | string }>(`
    SELECT enabled
    FROM ${groupWriteTable(client, 'providers')}
    WHERE code = ?
    LIMIT 1
  `, [providerCode])
  assertGroupPatchProviderRow(provider, providerCode)
}

function assertGroupPatchProviderRow(provider: { enabled?: number | boolean | string } | undefined, providerCode: string): void {
  if (!provider) throw new Error(`不支持的供应商：${providerCode}`)
  if (!databaseBoolean(provider.enabled)) throw new Error(`供应商已停用：${providerCode}`)
}

function ownedGroupHasAccounts(database: DatabaseSync, groupId: string): boolean {
  return Boolean(database.prepare(`
    SELECT 1
    FROM group_accounts
    INNER JOIN accounts ON accounts.id = group_accounts.account_id
    WHERE group_accounts.group_id = ?
      AND group_accounts.enabled = 1
      AND accounts.deleted_at IS NULL
    LIMIT 1
  `).get(groupId))
}

async function ownedGroupHasAccountsAsync(client: DatabaseClient, groupId: string): Promise<boolean> {
  const row = await client.one<{ found?: number }>(`
    SELECT 1 AS found
    FROM ${groupWriteTable(client, 'group_accounts')} group_accounts
    INNER JOIN ${groupWriteTable(client, 'accounts')} accounts ON accounts.id = group_accounts.account_id
    WHERE group_accounts.group_id = ?
      AND group_accounts.enabled = 1
      AND accounts.deleted_at IS NULL
    LIMIT 1
  `, [groupId])
  return Boolean(row)
}

async function lockGroupAuthorizationMutationRowAsync(client: DatabaseClient, authorizationId: string): Promise<void> {
  const lockClause = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  await client.one<{ id?: string }>(`
    SELECT id
    FROM ${groupWriteTable(client, 'resource_authorizations')}
    WHERE id = ?
    LIMIT 1${lockClause}
  `, [authorizationId])
}

function databaseBoolean(value: unknown): boolean {
  return value === true || value === 1 || value === '1' || value === 'true'
}

function assertExpectedGroupUpdatedAt(current: GroupPatchRow, input: Record<string, unknown>): void {
  if (!hasOwnInput(input, 'expectedUpdatedAt')) return
  const expectedUpdatedAt = requiredTextInput(input.expectedUpdatedAt, '分组版本')
  if (expectedUpdatedAt !== current.updated_at) throw new GroupPatchConflictError()
}

function assertGroupPatchApplied(changes: number | bigint | undefined): void {
  if (Number(changes ?? 0) !== 1) throw new GroupPatchConflictError()
}

function nextGroupUpdatedAt(currentUpdatedAt: string): string {
  const currentMs = rfc3339InstantMilliseconds(currentUpdatedAt)
  if (currentMs === undefined) throw new Error(`分组 updatedAt 必须是带 Z 或数值 offset 的 RFC3339 时间：${currentUpdatedAt}`)
  return new Date(Math.max(Date.now(), currentMs + 1)).toISOString()
}

function requiredPatchText(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label}数据异常，请清理后再编辑`)
  return value.trim()
}

function normalizedPatchGroupName(input: Record<string, unknown>): string {
  return typeof input.name === 'string' && input.name.trim() ? input.name.trim() : '当前分组名称'
}

function sqlitePatchValues(values: Iterable<unknown>): SQLInputValue[] {
  return [...values].map((value) => {
    if (value === null || typeof value === 'string' || typeof value === 'number') return value
    throw new Error('分组写入值类型无效')
  })
}

function nullablePatchText(value: unknown): string | null {
  if (value === null || typeof value === 'string') return value
  throw new Error('分组调度策略数据异常，请刷新后重试')
}

export interface DeletedGroupRouteStrategyChange {
  routeStrategyId: string
  routeStrategyName: string
  removedGroupId: string
  removedGroupName?: string
  removedBindingStatus?: string
}

export interface DeleteGroupResult {
  deleted: boolean
  ownerSystemAccountId?: string
  name?: string
  affectedRouteStrategies: DeletedGroupRouteStrategyChange[]
}

interface GroupDeleteLocatorRow {
  id: string
  system_account_id: string
  name: string
  is_default: number | boolean | string
}

export function deleteGroup(id: string, access?: AccessScope): DeleteGroupResult {
  const database = getBusinessDatabase()
  let deleted = false
  let current: GroupDeleteLocatorRow | undefined
  let affectedRouteStrategies: DeletedGroupRouteStrategyChange[] = []
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    current = findGroupDeleteLocator(database, id, access)
    if (!current) {
      commitDatabaseTransaction(database, transactionStarted)
      return emptyDeleteGroupResult()
    }
    if (Number(current.is_default) === 1) throw new Error('默认分组不能删除')
    affectedRouteStrategies = preserveRouteStrategiesBeforeGroupDelete(database, id, current.name)
    const result = database.prepare('DELETE FROM groups WHERE id = ? AND system_account_id = ?').run(id, current.system_account_id)
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
    ownerSystemAccountId: deleted ? current?.system_account_id : undefined,
    name: deleted ? current?.name : undefined,
    affectedRouteStrategies: deleted ? affectedRouteStrategies : []
  }
}

export async function deleteGroupAsync(id: string, access?: AccessScope): Promise<DeleteGroupResult> {
  const client = await getGroupWriteDatabaseClient()
  let deleted = false
  let current: GroupDeleteLocatorRow | undefined
  let affectedRouteStrategies: DeletedGroupRouteStrategyChange[] = []
  await client.transaction(async (tx) => {
    current = await findGroupDeleteLocatorAsync(tx, id, access)
    if (!current) return
    if (Number(current.is_default) === 1) throw new Error('默认分组不能删除')
    affectedRouteStrategies = await preserveRouteStrategiesBeforeGroupDeleteAsync(tx, id, current.name)
    const result = await tx.execute(`
      DELETE FROM ${groupWriteTable(tx, 'groups')}
      WHERE id = ? AND system_account_id = ?
    `, [id, current.system_account_id])
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
    ownerSystemAccountId: deleted ? current?.system_account_id : undefined,
    name: deleted ? current?.name : undefined,
    affectedRouteStrategies: deleted ? affectedRouteStrategies : []
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

function emptyDeleteGroupResult(): DeleteGroupResult {
  return {
    deleted: false,
    affectedRouteStrategies: []
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

function findGroupDeleteLocator(database: DatabaseSync, groupId: string, access?: AccessScope): GroupDeleteLocatorRow | undefined {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && !canAccessAll(access)) return undefined
  const ownerClause = ownerSystemAccountId ? ' AND system_account_id = ?' : ''
  const params: SQLInputValue[] = ownerSystemAccountId ? [groupId, ownerSystemAccountId] : [groupId]
  return database
    .prepare(`
      SELECT id, system_account_id, name, is_default
      FROM groups
      WHERE id = ?${ownerClause}
      LIMIT 1
    `)
    .get(...params) as unknown as GroupDeleteLocatorRow | undefined
}

async function findGroupDeleteLocatorAsync(client: DatabaseClient, groupId: string, access?: AccessScope): Promise<GroupDeleteLocatorRow | undefined> {
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && !canAccessAll(access)) return undefined
  const ownerClause = ownerSystemAccountId ? ' AND system_account_id = ?' : ''
  const lockClause = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  return client.one<GroupDeleteLocatorRow>(`
    SELECT id, system_account_id, name, is_default
    FROM ${groupWriteTable(client, 'groups')}
    WHERE id = ?${ownerClause}
    LIMIT 1${lockClause}
  `, ownerSystemAccountId ? [groupId, ownerSystemAccountId] : [groupId])
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
