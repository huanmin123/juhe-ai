import type {
  AccountAvailabilitySchedule,
  AccountClientCompatibility,
  AccountHealthCheckEndpointMode,
  AccountModelMapping,
  AccountStatus,
  AccountType
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { invalidateGatewayRuntimeAfterBusinessWrite } from './account-runtime-mutation-helpers.js'
import { advanceAccountCircuitDispatchRevisionFamilyInTransaction } from './account-circuit-control-plane.repository.js'
import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { replaceAccountModelMappingsInClientAsync } from './account-model-mappings.repository.js'
import { replaceAccountSupportedModelsInClientAsync } from './account-supported-models.repository.js'
import { replaceAccountTagsAsync } from './account-tags.repository.js'
import { canAccessAll, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { refreshGroupAccountStatsAfterWriteAsync } from './group-account-stats-write-invalidation.js'
import { getPostgresPool } from './postgres-client.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'

const businessSchemaName = 'juhe_business'

export interface AccountBatchUpdateTarget {
  accountId: string
  configRevision: number
}

export interface AccountBatchUpdateLockedAccount {
  id: string
  configRevision: number
  systemAccountId: string
  providerCode: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  type: AccountType
  status: AccountStatus
  credentials: Record<string, unknown>
  proxyProfileId?: string
  balanceQueryEnabled: boolean
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  schedulable: boolean
  availabilitySchedule?: AccountAvailabilitySchedule
  accountExpiresAt?: string
  notes?: string
  cooldownUntil?: string
  lastErrorCode?: string
  lastErrorMessage?: string
  cooldownRetestFailureCount: number
  cooldownRetestObservationStartedAt?: string
  cooldownRetestLastAt?: string
  cooldownRetestLastStatusCode?: number
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  supportedModels: string[]
  modelMappings: AccountModelMapping[]
  tags: string[]
}

export interface AccountBatchUpdatePreparedAccount {
  accountId: string
  expectedConfigRevision: number
  changedFields: string[]
  mainColumns: ReadonlyMap<string, unknown>
  supportedModels?: string[]
  modelMappings?: AccountModelMapping[]
  tags?: string[]
  dispatchBinding?: {
    priority: number
    superPriorityEnabled: boolean
    fallbackEnabled: boolean
  }
  dispatchRevisionChanged: boolean
  balanceSnapshotCleanup: boolean
  groupStatsAffected: boolean
  gatewayRuntimeAffected: boolean
}

export interface AccountBatchUpdatePrepareContext {
  client: DatabaseClient
  accounts: AccountBatchUpdateLockedAccount[]
}

export interface AccountBatchUpdateItemResult {
  id: string
  configRevision: number
  changedFields: string[]
}

export interface AccountBatchUpdateResult {
  batchId: string
  ownerSystemAccountId: string
  changedFields: string[]
  items: AccountBatchUpdateItemResult[]
  balanceSnapshotCleanupAccountIds: string[]
}

export class AccountBatchUpdateAccessError extends Error {
  constructor(message = '批量编辑账户不存在、不可编辑或不属于同一作用域') {
    super(message)
    this.name = 'AccountBatchUpdateAccessError'
  }
}

export class AccountBatchUpdateVersionConflictError extends Error {
  constructor(readonly accountId: string) {
    super(`账户配置已发生变化，请刷新后重试：${accountId}`)
    this.name = 'AccountBatchUpdateVersionConflictError'
  }
}

export async function updateAccountsBatchAsync(input: {
  targets: AccountBatchUpdateTarget[]
  requestedFields: string[]
  access?: AccessScope
  prepare: (context: AccountBatchUpdatePrepareContext) => Promise<AccountBatchUpdatePreparedAccount[]>
}): Promise<AccountBatchUpdateResult> {
  assertBatchTargets(input.targets)
  const client = await accountBatchDatabaseClientAsync()
  const batchId = newId('account_batch')
  const accountIds = input.targets.map((target) => target.accountId)
  const expectedRevisionByAccountId = new Map(input.targets.map((target) => [target.accountId, target.configRevision]))
  const transactionResult = await client.transaction(async (tx) => {
    const accounts = await loadLockedAccountsAsync(tx, accountIds, input.requestedFields, input.access)
    assertLockedAccountsMatchTargets(accounts, input.targets)
    const preparedAccounts = await input.prepare({ client: tx, accounts })
    assertPreparedAccountsMatchTargets(preparedAccounts, input.targets)
    const updatedAt = nowIso()
    const items: AccountBatchUpdateItemResult[] = []
    const changedAccountIds: string[] = []
    const statsAccountIds: string[] = []
    const gatewayAccountIds: string[] = []
    const balanceSnapshotCleanupAccountIds: string[] = []

    for (const prepared of preparedAccounts) {
      const current = accounts.find((account) => account.id === prepared.accountId)
      if (!current) throw new AccountBatchUpdateAccessError()
      const expectedRevision = expectedRevisionByAccountId.get(prepared.accountId)
      if (expectedRevision === undefined || expectedRevision !== prepared.expectedConfigRevision) {
        throw new AccountBatchUpdateVersionConflictError(prepared.accountId)
      }
      if (prepared.changedFields.length === 0) {
        items.push({ id: prepared.accountId, configRevision: current.configRevision, changedFields: [] })
        continue
      }

      const result = await executeAccountBatchCasUpdate(tx, prepared, current.systemAccountId, updatedAt)
      if (result !== 1) throw new AccountBatchUpdateVersionConflictError(prepared.accountId)

      if (prepared.supportedModels) {
        await replaceAccountSupportedModelsInClientAsync(tx, prepared.accountId, current.providerCode, prepared.supportedModels)
      }
      if (prepared.modelMappings) {
        await replaceAccountModelMappingsInClientAsync(tx, prepared.accountId, current.providerCode, prepared.modelMappings)
      }
      if (prepared.tags) {
        await replaceAccountTagsAsync(tx, prepared.accountId, current.systemAccountId, prepared.tags, updatedAt)
      }
      if (prepared.dispatchRevisionChanged) {
        await advanceAccountCircuitDispatchRevisionFamilyInTransaction(tx, {
          accountId: prepared.accountId,
          accountRuntimeKey: prepared.accountId,
          transitionId: `${batchId}:${prepared.accountId}`,
          nowMs: Date.parse(updatedAt)
        })
      }
      if (prepared.dispatchBinding) {
        await tx.execute(`
          UPDATE ${accountBatchTable(tx, 'group_accounts')}
          SET local_priority = ?,
              local_super_priority_enabled = ?,
              local_fallback_enabled = ?,
              updated_at = ?
          WHERE account_id = ?
            AND system_account_id = ?
            AND enabled = 1
        `, [
          prepared.dispatchBinding.priority,
          prepared.dispatchBinding.superPriorityEnabled ? 1 : 0,
          prepared.dispatchBinding.fallbackEnabled ? 1 : 0,
          updatedAt,
          prepared.accountId,
          current.systemAccountId
        ])
      }

      changedAccountIds.push(prepared.accountId)
      if (prepared.groupStatsAffected) statsAccountIds.push(prepared.accountId)
      if (prepared.gatewayRuntimeAffected) gatewayAccountIds.push(prepared.accountId)
      if (prepared.balanceSnapshotCleanup) balanceSnapshotCleanupAccountIds.push(prepared.accountId)
      items.push({
        id: prepared.accountId,
        configRevision: prepared.expectedConfigRevision + 1,
        changedFields: [...prepared.changedFields]
      })
    }

    return {
      ownerSystemAccountId: accounts[0]?.systemAccountId ?? '',
      items,
      changedAccountIds,
      statsAccountIds,
      gatewayAccountIds,
      balanceSnapshotCleanupAccountIds
    }
  })

  for (const accountId of transactionResult.changedAccountIds) invalidateAccountLookupCache(accountId)
  if (transactionResult.statsAccountIds.length > 0) {
    try {
      await refreshGroupAccountStatsAfterWriteAsync({
        accountIds: transactionResult.statsAccountIds,
        reason: 'account_batch_updated'
      })
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'account_batch_update_stats_refresh_failed',
        batchId,
        accountCount: transactionResult.statsAccountIds.length
      }), '批量编辑已提交，但分组账户统计脏标记失败')
    }
  }
  if (transactionResult.gatewayAccountIds.length > 0) {
    try {
      invalidateGatewayRuntimeAfterBusinessWrite('account_batch_updated')
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'account_batch_update_runtime_invalidation_failed',
        batchId,
        accountCount: transactionResult.gatewayAccountIds.length
      }), '批量编辑已提交，但网关运行时缓存失效失败')
    }
  }
  return {
    batchId,
    ownerSystemAccountId: transactionResult.ownerSystemAccountId,
    changedFields: [...new Set(transactionResult.items.flatMap((item) => item.changedFields))].sort(),
    items: transactionResult.items,
    balanceSnapshotCleanupAccountIds: transactionResult.balanceSnapshotCleanupAccountIds
  }
}

async function executeAccountBatchCasUpdate(
  client: DatabaseClient,
  prepared: AccountBatchUpdatePreparedAccount,
  ownerSystemAccountId: string,
  updatedAt: string
): Promise<number> {
  const assignments: string[] = []
  const params: unknown[] = []
  for (const [column, value] of prepared.mainColumns) {
    assignments.push(`${client.dialect.quoteIdentifier(column)} = ?`)
    params.push(value)
  }
  assignments.push(`${client.dialect.quoteIdentifier('config_revision')} = ${client.dialect.quoteIdentifier('config_revision')} + 1`)
  assignments.push(`${client.dialect.quoteIdentifier('updated_at')} = ?`)
  params.push(updatedAt, prepared.accountId, prepared.expectedConfigRevision, ownerSystemAccountId)
  const result = await client.execute(`
    UPDATE ${accountBatchTable(client, 'accounts')}
    SET ${assignments.join(', ')}
    WHERE id = ?
      AND config_revision = ?
      AND system_account_id = ?
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
      AND authorization_instance_source_account_id IS NULL
  `, params)
  return result.changes
}

function assertBatchTargets(targets: AccountBatchUpdateTarget[]): void {
  if (targets.length < 2 || targets.length > 100) throw new Error('批量编辑账户数量必须在 2-100 个之间')
  const accountIds = targets.map((target) => target.accountId.trim())
  if (accountIds.some((accountId) => !accountId) || new Set(accountIds).size !== targets.length) {
    throw new Error('批量编辑账户不能为空或重复')
  }
  if (targets.some((target) => !Number.isInteger(target.configRevision) || target.configRevision < 1)) {
    throw new Error('批量编辑账户配置版本无效')
  }
}

async function loadLockedAccountsAsync(
  client: DatabaseClient,
  accountIds: string[],
  requestedFields: string[],
  access?: AccessScope
): Promise<AccountBatchUpdateLockedAccount[]> {
  const scopedOwnerId = manageableSystemAccountId(access)
  if (!scopedOwnerId && !canAccessAll(access)) return []
  const placeholders = accountIds.map(() => '?').join(', ')
  const lockClause = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  const columns = accountBatchProjection(requestedFields)
    .map((column) => client.dialect.quoteIdentifier(column))
    .join(', ')
  const ownerScopeClause = scopedOwnerId
    ? ` AND ${client.dialect.quoteIdentifier('system_account_id')} = ?`
    : ''
  const params = scopedOwnerId ? [...accountIds, scopedOwnerId] : accountIds
  const rows = await client.query<AccountBatchAccountRow>(`
    SELECT ${columns}
    FROM ${accountBatchTable(client, 'accounts')}
    WHERE id IN (${placeholders})
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
      AND authorization_instance_source_account_id IS NULL${ownerScopeClause}
    ORDER BY id ASC${lockClause}
  `, params)
  const related = await loadBatchAccountRelatedConfigAsync(client, rows.map((row) => row.id), requestedFields)
  return rows.map((row) => ({
    id: row.id,
    configRevision: Number(row.config_revision),
    systemAccountId: row.system_account_id,
    providerCode: row.provider_code ?? '',
    providerProtocolProfileId: row.provider_protocol_profile_id ?? '',
    protocolCode: row.protocol_code ?? '',
    protocolVersion: row.protocol_version ?? '',
    type: row.type ?? 'api_key',
    status: row.status ?? 'disabled',
    credentials: row.credentials_encrypted ? decryptJson<Record<string, unknown>>(row.credentials_encrypted) : {},
    proxyProfileId: row.proxy_profile_id ?? undefined,
    balanceQueryEnabled: databaseBoolean(row.balance_query_enabled),
    concurrencyLimit: Number(row.concurrency_limit ?? 0),
    priority: Number(row.priority ?? 0),
    superPriorityEnabled: databaseBoolean(row.super_priority_enabled),
    fallbackEnabled: databaseBoolean(row.fallback_enabled),
    clientCompatibility: row.client_compatibility ?? 'openai_standard',
    schedulable: databaseBoolean(row.schedulable),
    availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
    accountExpiresAt: row.account_expires_at ?? undefined,
    notes: row.notes ?? undefined,
    cooldownUntil: row.cooldown_until ?? undefined,
    lastErrorCode: row.last_error_code ?? undefined,
    lastErrorMessage: row.last_error_message ?? undefined,
    cooldownRetestFailureCount: Number(row.cooldown_retest_failure_count ?? 0),
    cooldownRetestObservationStartedAt: row.cooldown_retest_observation_started_at ?? undefined,
    cooldownRetestLastAt: row.cooldown_retest_last_at ?? undefined,
    cooldownRetestLastStatusCode: optionalNumber(row.cooldown_retest_last_status_code),
    healthCheckModel: row.health_check_model?.trim() ?? '',
    healthCheckEndpointMode: row.health_check_endpoint_mode ?? 'chat_json',
    supportedModels: related.supportedModels.get(row.id) ?? [],
    modelMappings: related.modelMappings.get(row.id) ?? [],
    tags: related.tags.get(row.id) ?? []
  }))
}

function accountBatchProjection(requestedFields: string[]): string[] {
  const fields = new Set(requestedFields)
  const columns = new Set(['id', 'config_revision', 'system_account_id'])
  const add = (...values: string[]): void => { for (const value of values) columns.add(value) }
  const addProviderContext = (): void => add(
    'provider_code', 'provider_protocol_profile_id', 'protocol_code', 'protocol_version', 'type', 'client_compatibility'
  )
  const addRuntimeState = (): void => add(
    'status', 'schedulable', 'cooldown_until', 'last_error_code', 'last_error_message',
    'cooldown_retest_failure_count', 'cooldown_retest_observation_started_at',
    'cooldown_retest_last_at', 'cooldown_retest_last_status_code'
  )
  if (fields.has('notes')) add('notes')
  if (fields.has('proxyProfileId')) add('proxy_profile_id', 'balance_query_enabled', 'status')
  if (fields.has('concurrencyLimit')) add('concurrency_limit')
  if (['priority', 'superPriorityEnabled', 'fallbackEnabled'].some((field) => fields.has(field))) {
    add('priority', 'super_priority_enabled', 'fallback_enabled')
  }
  if (fields.has('accountExpiresAt')) {
    add('account_expires_at')
    addRuntimeState()
  }
  if (fields.has('availabilitySchedule')) {
    add('availability_schedule_json')
    addRuntimeState()
  }
  const credentialFields = ['errorHandlingRules', 'responseInspectionRules', 'supportedEndpointModes', 'serviceTierOverride', 'reasoningEffortOverride']
  if (credentialFields.some((field) => fields.has(field))) {
    addProviderContext()
    add('credentials_encrypted')
  }
  const modelFields = ['supportedModels', 'healthCheckModel', 'healthCheckEndpointMode', 'modelMappings', 'supportedEndpointModes', 'serviceTierOverride', 'reasoningEffortOverride']
  if (modelFields.some((field) => fields.has(field))) {
    addProviderContext()
    add('status')
  }
  if (['supportedModels', 'healthCheckEndpointMode', 'modelMappings', 'supportedEndpointModes', 'serviceTierOverride', 'reasoningEffortOverride']
    .some((field) => fields.has(field))) {
    add('credentials_encrypted')
  }
  if (fields.has('supportedModels') || fields.has('healthCheckModel')) add('health_check_model')
  if (fields.has('healthCheckEndpointMode') || fields.has('supportedEndpointModes')) add('health_check_endpoint_mode')
  return [...columns]
}

function assertLockedAccountsMatchTargets(
  accounts: AccountBatchUpdateLockedAccount[],
  targets: AccountBatchUpdateTarget[]
): void {
  if (accounts.length !== targets.length) throw new AccountBatchUpdateAccessError()
  const targetById = new Map(targets.map((target) => [target.accountId, target]))
  const owners = new Set<string>()
  for (const account of accounts) {
    const target = targetById.get(account.id)
    if (!target) throw new AccountBatchUpdateAccessError()
    if (account.configRevision !== target.configRevision) throw new AccountBatchUpdateVersionConflictError(account.id)
    owners.add(account.systemAccountId)
  }
  if (owners.size !== 1) throw new AccountBatchUpdateAccessError('批量编辑账户必须属于同一系统账户作用域')
}

function assertPreparedAccountsMatchTargets(
  preparedAccounts: AccountBatchUpdatePreparedAccount[],
  targets: AccountBatchUpdateTarget[]
): void {
  if (preparedAccounts.length !== targets.length) throw new Error('批量编辑最终配置数量不匹配')
  const preparedIds = new Set(preparedAccounts.map((account) => account.accountId))
  if (preparedIds.size !== targets.length || targets.some((target) => !preparedIds.has(target.accountId))) {
    throw new Error('批量编辑最终配置目标不匹配')
  }
}

async function loadBatchAccountRelatedConfigAsync(
  client: DatabaseClient,
  accountIds: string[],
  requestedFields: string[]
): Promise<{
  supportedModels: Map<string, string[]>
  modelMappings: Map<string, AccountModelMapping[]>
  tags: Map<string, string[]>
}> {
  const output = {
    supportedModels: new Map<string, string[]>(),
    modelMappings: new Map<string, AccountModelMapping[]>(),
    tags: new Map<string, string[]>()
  }
  if (!accountIds.length) return output
  const fields = new Set(requestedFields)
  const placeholders = accountIds.map(() => '?').join(', ')
  if (['supportedModels', 'healthCheckModel', 'modelMappings', 'supportedEndpointModes', 'serviceTierOverride', 'reasoningEffortOverride']
    .some((field) => fields.has(field))) {
    const rows = await client.query<{ account_id: string; model: string }>(`
      SELECT account_id, model
      FROM ${accountBatchTable(client, 'account_supported_models')}
      WHERE account_id IN (${placeholders})
      ORDER BY account_id ASC, model ASC
    `, accountIds)
    for (const row of rows) appendMapValue(output.supportedModels, row.account_id, row.model)
  }
  if (fields.has('modelMappings') || fields.has('supportedModels') || fields.has('supportedEndpointModes')) {
    const rows = await client.query<AccountBatchModelMappingRow>(`
      SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
      FROM ${accountBatchTable(client, 'account_model_mappings')}
      WHERE account_id IN (${placeholders})
      ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC
    `, accountIds)
    for (const row of rows) {
      appendMapValue(output.modelMappings, row.account_id, {
        sourceModel: row.source_model,
        sourceEndpointFamily: row.source_endpoint_family,
        upstreamModel: row.upstream_model,
        upstreamEndpointFamily: row.upstream_endpoint_family,
        enabled: row.enabled === 1
      })
    }
  }
  if (fields.has('tags')) {
    const rows = await client.query<{ account_id: string; name: string }>(`
      SELECT account_tag_bindings.account_id, account_tags.name
      FROM ${accountBatchTable(client, 'account_tag_bindings')} account_tag_bindings
      INNER JOIN ${accountBatchTable(client, 'account_tags')} account_tags
        ON account_tags.id = account_tag_bindings.tag_id
      WHERE account_tag_bindings.account_id IN (${placeholders})
      ORDER BY account_tag_bindings.account_id ASC, account_tags.name ASC
    `, accountIds)
    for (const row of rows) appendMapValue(output.tags, row.account_id, row.name)
  }
  return output
}

function appendMapValue<T>(target: Map<string, T[]>, key: string, value: T): void {
  const values = target.get(key)
  if (values) values.push(value)
  else target.set(key, [value])
}

async function accountBatchDatabaseClientAsync(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') return createPostgresDatabaseClient(await getPostgresPool())
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountBatchTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function databaseBoolean(value: unknown): boolean {
  return value === true || value === 1 || value === '1'
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

interface AccountBatchAccountRow {
  id: string
  config_revision: number
  system_account_id: string
  provider_code?: string
  provider_protocol_profile_id?: string
  protocol_code?: string
  protocol_version?: string
  type?: AccountType
  status?: AccountStatus
  credentials_encrypted?: string
  proxy_profile_id?: string | null
  balance_query_enabled?: number | boolean | string
  concurrency_limit?: number
  priority?: number
  super_priority_enabled?: number | boolean | string
  fallback_enabled?: number | boolean | string
  client_compatibility?: AccountClientCompatibility
  schedulable?: number | boolean | string
  availability_schedule_json?: string | null
  account_expires_at?: string | null
  notes?: string | null
  cooldown_until?: string | null
  last_error_code?: string | null
  last_error_message?: string | null
  cooldown_retest_failure_count?: number
  cooldown_retest_observation_started_at?: string | null
  cooldown_retest_last_at?: string | null
  cooldown_retest_last_status_code?: number | null
  health_check_model?: string
  health_check_endpoint_mode?: AccountHealthCheckEndpointMode
}

interface AccountBatchModelMappingRow {
  account_id: string
  source_model: string
  source_endpoint_family: AccountModelMapping['sourceEndpointFamily']
  upstream_model: string
  upstream_endpoint_family: AccountModelMapping['upstreamEndpointFamily']
  enabled: number
}
