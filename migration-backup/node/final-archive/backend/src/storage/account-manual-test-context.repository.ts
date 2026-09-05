import type {
  AccountClientCompatibility,
  AccountHealthCheckEndpointMode,
  AccountModelMapping,
  AccountSupportedEndpointMode,
  AccountType,
  ProviderCode
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { scopedSystemAccountId, type AccessScope } from './access-scope.js'
import {
  loadModelMappingsByAccountIdsAsync,
  loadModelMappingsForAccountModel,
  loadModelMappingsForAccountModelAsync
} from './account-model-mappings.repository.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

export interface AccountManualTestListContext {
  id: string
  factAccountId: string
  ownerSystemAccountId: string
  providerCode: ProviderCode
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
  type: AccountType
  clientCompatibility: AccountClientCompatibility
  healthCheckModel: string
}

export interface AccountManualTestCapabilitiesContext extends AccountManualTestListContext {
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  supportedEndpointModes: AccountSupportedEndpointMode[]
  modelMappings: AccountModelMapping[]
}

/**
 * 仅供测试模型 options 生成使用的受控账户上下文。凭据只用于在服务端
 * 解析 endpoint mode，绝不能作为 HTTP 响应的一部分返回。
 */
export interface AccountManualTestOptionsContext extends AccountManualTestCapabilitiesContext {}

interface AccountManualTestContextRow {
  view_account_id: string
  fact_account_id: string
  owner_system_account_id: string
  provider_code: ProviderCode
  provider_protocol_profile_id?: string | null
  protocol_code?: string | null
  protocol_version?: string | null
  type: AccountType
  client_compatibility: AccountClientCompatibility
  health_check_model: string
  health_check_endpoint_mode: AccountHealthCheckEndpointMode
  credentials_encrypted?: string | null
}

export async function findAccountManualTestListContextAsync(
  accountId: string,
  access?: AccessScope
): Promise<AccountManualTestListContext | undefined> {
  const row = await findVisibleAccountManualTestContextRowAsync(accountId, access, false)
  return row ? accountManualTestListContextFromRow(row) : undefined
}

export async function findAccountManualTestCapabilitiesContextAsync(
  accountId: string,
  modelId: string,
  access?: AccessScope
): Promise<AccountManualTestCapabilitiesContext | undefined> {
  const row = await findVisibleAccountManualTestContextRowAsync(accountId, access, true)
  if (!row?.credentials_encrypted) return undefined
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  const modelMappings = runtimeConfig.databaseDriver === 'postgres'
    ? await loadModelMappingsForAccountModelAsync(row.fact_account_id, modelId)
    : loadModelMappingsForAccountModel(row.fact_account_id, modelId)
  return {
    ...accountManualTestListContextFromRow(row),
    healthCheckEndpointMode: row.health_check_endpoint_mode,
    supportedEndpointModes: accountSupportedEndpointModes(credentials.supported_endpoint_modes),
    modelMappings
  }
}

export async function findAccountManualTestOptionsContextAsync(
  accountId: string,
  access?: AccessScope
): Promise<AccountManualTestOptionsContext | undefined> {
  const row = await findVisibleAccountManualTestContextRowAsync(accountId, access, true)
  if (!row?.credentials_encrypted) return undefined
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  const modelMappings = (await loadModelMappingsByAccountIdsAsync([row.fact_account_id])).get(row.fact_account_id) ?? []
  return {
    ...accountManualTestListContextFromRow(row),
    healthCheckEndpointMode: row.health_check_endpoint_mode,
    supportedEndpointModes: accountSupportedEndpointModes(credentials.supported_endpoint_modes),
    modelMappings
  }
}

async function findVisibleAccountManualTestContextRowAsync(
  accountIdInput: string,
  access: AccessScope | undefined,
  includeCredentials: boolean
): Promise<AccountManualTestContextRow | undefined> {
  const accountId = accountIdInput.trim()
  if (!accountId) return undefined
  const scopedOwnerId = scopedSystemAccountId(access)
  if (runtimeConfig.databaseDriver !== 'postgres') {
    const client = createSqliteDatabaseClient(getBusinessDatabase())
    return client.one<AccountManualTestContextRow>(
      accountManualTestContextSql(client, includeCredentials, Boolean(scopedOwnerId)),
      accountManualTestContextParams(accountId, scopedOwnerId)
    )
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  return client.one<AccountManualTestContextRow>(
    accountManualTestContextSql(client, includeCredentials, Boolean(scopedOwnerId)),
    accountManualTestContextParams(accountId, scopedOwnerId)
  )
}

function accountManualTestContextSql(
  client: Pick<DatabaseClient, 'dialect'>,
  includeCredentials: boolean,
  restrictOwner: boolean
): string {
  const accounts = client.dialect.qualifyTable('juhe_business', 'accounts')
  const authorizations = client.dialect.qualifyTable('juhe_business', 'resource_authorizations')
  return `
    SELECT
      accounts.id AS view_account_id,
      COALESCE(source_accounts.id, accounts.id) AS fact_account_id,
      COALESCE(source_accounts.system_account_id, accounts.system_account_id) AS owner_system_account_id,
      COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
      COALESCE(source_accounts.provider_protocol_profile_id, accounts.provider_protocol_profile_id) AS provider_protocol_profile_id,
      COALESCE(source_accounts.protocol_code, accounts.protocol_code) AS protocol_code,
      COALESCE(source_accounts.protocol_version, accounts.protocol_version) AS protocol_version,
      COALESCE(source_accounts.type, accounts.type) AS type,
      COALESCE(source_accounts.client_compatibility, accounts.client_compatibility) AS client_compatibility,
      COALESCE(source_accounts.health_check_model, accounts.health_check_model) AS health_check_model,
      COALESCE(source_accounts.health_check_endpoint_mode, accounts.health_check_endpoint_mode) AS health_check_endpoint_mode
      ${includeCredentials ? ', COALESCE(source_accounts.credentials_encrypted, accounts.credentials_encrypted) AS credentials_encrypted' : ''}
    FROM ${accounts} accounts
    LEFT JOIN ${accounts} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN ${authorizations} authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR (
          authorizations.id IS NOT NULL
          AND authorizations.status IN ('active', 'paused', 'expired')
          AND source_accounts.id IS NOT NULL
        )
      )
      ${restrictOwner ? 'AND accounts.system_account_id = ?' : ''}
    LIMIT 1
  `
}

function accountManualTestContextParams(accountId: string, scopedOwnerId: string | undefined): string[] {
  return scopedOwnerId ? [accountId, scopedOwnerId] : [accountId]
}

function accountManualTestListContextFromRow(row: AccountManualTestContextRow): AccountManualTestListContext {
  return {
    id: row.view_account_id,
    factAccountId: row.fact_account_id,
    ownerSystemAccountId: row.owner_system_account_id,
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id ?? undefined,
    protocolCode: row.protocol_code ?? undefined,
    protocolVersion: row.protocol_version ?? undefined,
    type: row.type,
    clientCompatibility: row.client_compatibility,
    healthCheckModel: row.health_check_model
  }
}

function accountSupportedEndpointModes(value: unknown): AccountSupportedEndpointMode[] {
  return Array.isArray(value)
    ? value.filter((item): item is AccountSupportedEndpointMode => typeof item === 'string')
    : []
}
