import type {
  AccountAvailabilitySchedule,
  AccountClientCompatibility,
  AccountModelMapping,
  AccountStatus,
  AccountType
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { invalidateGatewayRuntimeAfterBusinessWrite } from './account-runtime-mutation-helpers.js'
import { accountAvailabilityScheduleJson, parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { replaceAccountModelMappingsInClientAsync } from './account-model-mappings.repository.js'
import { replaceAccountSupportedModelsInClientAsync } from './account-supported-models.repository.js'
import { replaceAccountTagsAsync } from './account-tags.repository.js'
import { manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { decryptJson, encryptJson } from './crypto.js'
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
  credentialsEncrypted: string
  proxyProfileId?: string
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
  supportedModels: string[]
  modelMappings: AccountModelMapping[]
  tags: string[]
}

export interface AccountBatchUpdatePreparedAccount {
  accountId: string
  expectedConfigRevision: number
  credentials?: Record<string, unknown>
  proxyProfileId?: string
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  status: AccountStatus
  schedulable: boolean
  availabilitySchedule?: AccountAvailabilitySchedule
  availabilityScheduleNextCheckAt: string | null
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
  supportedModels: string[]
  modelMappings: AccountModelMapping[]
  tags: string[]
  supportedModelsChanged: boolean
  modelMappingsChanged: boolean
  tagsChanged: boolean
  dispatchChanged: boolean
  resetHealthCheckState: boolean
}

export interface AccountBatchUpdatePrepareContext {
  client: DatabaseClient
  accounts: AccountBatchUpdateLockedAccount[]
}

export interface AccountBatchUpdateResult {
  batchId: string
  accountIds: string[]
  configRevisions: Record<string, number>
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
  access?: AccessScope
  prepare: (context: AccountBatchUpdatePrepareContext) => Promise<AccountBatchUpdatePreparedAccount[]>
}): Promise<AccountBatchUpdateResult> {
  assertBatchTargets(input.targets)
  const client = await accountBatchDatabaseClientAsync()
  const batchId = newId('account_batch')
  const accountIds = input.targets.map((target) => target.accountId)
  const expectedRevisionByAccountId = new Map(input.targets.map((target) => [target.accountId, target.configRevision]))
  const configRevisions = await client.transaction(async (tx) => {
    const accounts = await loadLockedAccountsAsync(tx, accountIds, input.access)
    assertLockedAccountsMatchTargets(accounts, input.targets, input.access)
    const preparedAccounts = await input.prepare({ client: tx, accounts })
    assertPreparedAccountsMatchTargets(preparedAccounts, input.targets)
    const updatedAt = nowIso()
    const nextRevisions: Record<string, number> = {}

    for (const prepared of preparedAccounts) {
      const current = accounts.find((account) => account.id === prepared.accountId)
      if (!current) {
        throw new AccountBatchUpdateAccessError()
      }
      const expectedRevision = expectedRevisionByAccountId.get(prepared.accountId)
      if (expectedRevision === undefined || expectedRevision !== prepared.expectedConfigRevision) {
        throw new AccountBatchUpdateVersionConflictError(prepared.accountId)
      }
      const result = await tx.execute(`
        UPDATE ${accountBatchTable(tx, 'accounts')}
        SET credentials_encrypted = ?,
            proxy_profile_id = ?,
            concurrency_limit = ?,
            priority = ?,
            super_priority_enabled = ?,
            fallback_enabled = ?,
            status = ?,
            schedulable = ?,
            availability_schedule_json = ?,
            availability_schedule_next_check_at = ?,
            notes = ?,
            account_expires_at = ?,
            cooldown_until = ?,
            last_error_code = ?,
            last_error_message = ?,
            cooldown_retest_failure_count = ?,
            cooldown_retest_observation_started_at = ?,
            cooldown_retest_last_at = ?,
            cooldown_retest_last_status_code = ?,
            health_check_model = ?,
            next_health_check_at = CASE WHEN ? = 1 THEN NULL ELSE next_health_check_at END,
            config_revision = config_revision + 1,
            updated_at = ?
        WHERE id = ?
          AND config_revision = ?
          AND deleted_at IS NULL
          AND authorization_instance_authorization_id IS NULL
          AND authorization_instance_source_account_id IS NULL
      `, [
        prepared.credentials ? encryptJson(prepared.credentials) : current.credentialsEncrypted,
        prepared.proxyProfileId ?? null,
        prepared.concurrencyLimit,
        prepared.priority,
        prepared.superPriorityEnabled ? 1 : 0,
        prepared.fallbackEnabled ? 1 : 0,
        prepared.status,
        prepared.schedulable ? 1 : 0,
        accountAvailabilityScheduleJson(prepared.availabilitySchedule),
        prepared.availabilityScheduleNextCheckAt,
        prepared.notes ?? null,
        prepared.accountExpiresAt ?? null,
        prepared.cooldownUntil ?? null,
        prepared.lastErrorCode ?? null,
        prepared.lastErrorMessage ?? null,
        prepared.cooldownRetestFailureCount,
        prepared.cooldownRetestObservationStartedAt ?? null,
        prepared.cooldownRetestLastAt ?? null,
        prepared.cooldownRetestLastStatusCode ?? null,
        prepared.healthCheckModel,
        prepared.resetHealthCheckState ? 1 : 0,
        updatedAt,
        prepared.accountId,
        prepared.expectedConfigRevision
      ])
      if (result.changes !== 1) {
        throw new AccountBatchUpdateVersionConflictError(prepared.accountId)
      }
      if (prepared.supportedModelsChanged) {
        await replaceAccountSupportedModelsInClientAsync(
          tx,
          prepared.accountId,
          current.providerCode,
          prepared.supportedModels
        )
      }
      if (prepared.modelMappingsChanged) {
        await replaceAccountModelMappingsInClientAsync(
          tx,
          prepared.accountId,
          current.providerCode,
          prepared.modelMappings
        )
      }
      if (prepared.tagsChanged) {
        await replaceAccountTagsAsync(
          tx,
          prepared.accountId,
          current.systemAccountId,
          prepared.tags,
          updatedAt
        )
      }
      if (prepared.dispatchChanged) {
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
          prepared.priority,
          prepared.superPriorityEnabled ? 1 : 0,
          prepared.fallbackEnabled ? 1 : 0,
          updatedAt,
          prepared.accountId,
          current.systemAccountId
        ])
      }
      nextRevisions[prepared.accountId] = prepared.expectedConfigRevision + 1
    }
    return nextRevisions
  })

  for (const accountId of accountIds) {
    invalidateAccountLookupCache(accountId)
  }
  try {
    await refreshGroupAccountStatsAfterWriteAsync({ accountIds, reason: 'account_batch_updated' })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_batch_update_stats_refresh_failed',
      batchId,
      accountCount: accountIds.length
    }), '批量编辑已提交，但分组账户统计脏标记失败')
  }
  try {
    invalidateGatewayRuntimeAfterBusinessWrite('account_batch_updated')
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'account_batch_update_runtime_invalidation_failed',
      batchId,
      accountCount: accountIds.length
    }), '批量编辑已提交，但网关运行时缓存失效失败')
  }
  return { batchId, accountIds, configRevisions }
}

function assertBatchTargets(targets: AccountBatchUpdateTarget[]): void {
  if (targets.length < 2 || targets.length > 100) {
    throw new Error('批量编辑账户数量必须在 2-100 个之间')
  }
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
  access?: AccessScope
): Promise<AccountBatchUpdateLockedAccount[]> {
  const placeholders = accountIds.map(() => '?').join(', ')
  const lockClause = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  const rows = await client.query<AccountBatchAccountRow>(`
    SELECT
      id,
      config_revision,
      system_account_id,
      provider_code,
      provider_protocol_profile_id,
      protocol_code,
      protocol_version,
      type,
      status,
      credentials_encrypted,
      proxy_profile_id,
      concurrency_limit,
      priority,
      super_priority_enabled,
      fallback_enabled,
      client_compatibility,
      schedulable,
      availability_schedule_json,
      account_expires_at,
      notes,
      cooldown_until,
      last_error_code,
      last_error_message,
      cooldown_retest_failure_count,
      cooldown_retest_observation_started_at,
      cooldown_retest_last_at,
      cooldown_retest_last_status_code,
      health_check_model,
      authorization_instance_source_account_id,
      authorization_instance_authorization_id
    FROM ${accountBatchTable(client, 'accounts')}
    WHERE id IN (${placeholders})
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
      AND authorization_instance_source_account_id IS NULL
    ORDER BY id ASC${lockClause}
  `, accountIds)
  const related = await loadBatchAccountRelatedConfigAsync(client, rows.map((row) => row.id))
  return rows.map((row) => ({
    id: row.id,
    configRevision: Number(row.config_revision),
    systemAccountId: row.system_account_id,
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    type: row.type,
    status: row.status,
    credentials: decryptJson<Record<string, unknown>>(row.credentials_encrypted),
    credentialsEncrypted: row.credentials_encrypted,
    proxyProfileId: row.proxy_profile_id ?? undefined,
    concurrencyLimit: Number(row.concurrency_limit),
    priority: Number(row.priority),
    superPriorityEnabled: row.super_priority_enabled === 1,
    fallbackEnabled: row.fallback_enabled === 1,
    clientCompatibility: row.client_compatibility,
    schedulable: row.schedulable === 1,
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
    healthCheckModel: row.health_check_model.trim(),
    supportedModels: related.supportedModels.get(row.id) ?? [],
    modelMappings: related.modelMappings.get(row.id) ?? [],
    tags: related.tags.get(row.id) ?? []
  }))
}

function assertLockedAccountsMatchTargets(
  accounts: AccountBatchUpdateLockedAccount[],
  targets: AccountBatchUpdateTarget[],
  access?: AccessScope
): void {
  if (accounts.length !== targets.length) {
    throw new AccountBatchUpdateAccessError()
  }
  const targetById = new Map(targets.map((target) => [target.accountId, target]))
  const owners = new Set<string>()
  const requiredOwnerSystemAccountId = manageableSystemAccountId(access)
  for (const account of accounts) {
    const target = targetById.get(account.id)
    if (!target) {
      throw new AccountBatchUpdateAccessError()
    }
    if (account.configRevision !== target.configRevision) {
      throw new AccountBatchUpdateVersionConflictError(account.id)
    }
    if (requiredOwnerSystemAccountId && account.systemAccountId !== requiredOwnerSystemAccountId) {
      throw new AccountBatchUpdateAccessError()
    }
    owners.add(account.systemAccountId)
  }
  if (owners.size !== 1) {
    throw new AccountBatchUpdateAccessError('批量编辑账户必须属于同一系统账户作用域')
  }
}

function assertPreparedAccountsMatchTargets(
  preparedAccounts: AccountBatchUpdatePreparedAccount[],
  targets: AccountBatchUpdateTarget[]
): void {
  if (preparedAccounts.length !== targets.length) {
    throw new Error('批量编辑最终配置数量不匹配')
  }
  const preparedIds = new Set(preparedAccounts.map((account) => account.accountId))
  if (preparedIds.size !== targets.length || targets.some((target) => !preparedIds.has(target.accountId))) {
    throw new Error('批量编辑最终配置目标不匹配')
  }
}

async function loadBatchAccountRelatedConfigAsync(client: DatabaseClient, accountIds: string[]): Promise<{
  supportedModels: Map<string, string[]>
  modelMappings: Map<string, AccountModelMapping[]>
  tags: Map<string, string[]>
}> {
  if (!accountIds.length) {
    return {
      supportedModels: new Map(),
      modelMappings: new Map(),
      tags: new Map()
    }
  }
  const placeholders = accountIds.map(() => '?').join(', ')
  const [supportedModelRows, mappingRows, tagRows] = await Promise.all([
    client.query<{ account_id: string; model: string }>(`
      SELECT account_id, model
      FROM ${accountBatchTable(client, 'account_supported_models')}
      WHERE account_id IN (${placeholders})
      ORDER BY account_id ASC, model ASC
    `, accountIds),
    client.query<AccountBatchModelMappingRow>(`
      SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
      FROM ${accountBatchTable(client, 'account_model_mappings')}
      WHERE account_id IN (${placeholders})
      ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC
    `, accountIds),
    client.query<{ account_id: string; name: string }>(`
      SELECT account_tag_bindings.account_id, account_tags.name
      FROM ${accountBatchTable(client, 'account_tag_bindings')} account_tag_bindings
      INNER JOIN ${accountBatchTable(client, 'account_tags')} account_tags
        ON account_tags.id = account_tag_bindings.tag_id
      WHERE account_tag_bindings.account_id IN (${placeholders})
      ORDER BY account_tag_bindings.account_id ASC, account_tags.name ASC
    `, accountIds)
  ])
  const supportedModels = new Map<string, string[]>()
  for (const row of supportedModelRows) {
    appendMapValue(supportedModels, row.account_id, row.model)
  }
  const modelMappings = new Map<string, AccountModelMapping[]>()
  for (const row of mappingRows) {
    appendMapValue(modelMappings, row.account_id, {
      sourceModel: row.source_model,
      sourceEndpointFamily: row.source_endpoint_family,
      upstreamModel: row.upstream_model,
      upstreamEndpointFamily: row.upstream_endpoint_family,
      enabled: row.enabled === 1
    })
  }
  const tags = new Map<string, string[]>()
  for (const row of tagRows) {
    appendMapValue(tags, row.account_id, row.name)
  }
  return { supportedModels, modelMappings, tags }
}

function appendMapValue<T>(target: Map<string, T[]>, key: string, value: T): void {
  const values = target.get(key)
  if (values) {
    values.push(value)
  } else {
    target.set(key, [value])
  }
}

async function accountBatchDatabaseClientAsync(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountBatchTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function optionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

interface AccountBatchAccountRow {
  id: string
  config_revision: number
  system_account_id: string
  provider_code: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  type: AccountType
  status: AccountStatus
  credentials_encrypted: string
  proxy_profile_id: string | null
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  client_compatibility: AccountClientCompatibility
  schedulable: number
  availability_schedule_json: string | null
  account_expires_at: string | null
  notes: string | null
  cooldown_until: string | null
  last_error_code: string | null
  last_error_message: string | null
  cooldown_retest_failure_count: number
  cooldown_retest_observation_started_at: string | null
  cooldown_retest_last_at: string | null
  cooldown_retest_last_status_code: number | null
  health_check_model: string
  authorization_instance_source_account_id: string | null
  authorization_instance_authorization_id: string | null
}

interface AccountBatchModelMappingRow {
  account_id: string
  source_model: string
  source_endpoint_family: AccountModelMapping['sourceEndpointFamily']
  upstream_model: string
  upstream_endpoint_family: AccountModelMapping['upstreamEndpointFamily']
  enabled: number
}
