import { runtimeConfig } from '../config/runtime.js'
import type {
  AccountClientCompatibility,
  AccountCredentials,
  AccountEditBasicDetail,
  AccountHealthCheckEndpointMode,
  AccountStatus,
  AccountType,
  ProviderCode
} from '../domain/types.js'
import { canManageResourceOwner } from './resource-authorization-helpers.js'
import { includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

const businessSchemaName = 'juhe_business'

const basicEditableCredentialKeys = [
  'base_url',
  'supported_endpoint_modes'
] as const

const editableCredentialKeysByAccountType: Record<string, readonly string[]> = {
  api_key: [
    'api_key',
    'api_keys',
    'api_key_strategy',
    'api_key_weights'
  ],
  oauth: [
    'access_token',
    'refresh_token'
  ],
  google_oauth: [
    'access_token',
    'refresh_token',
    'client_id',
    'client_secret',
    'quota_project_id',
    'oauth_type',
    'project_id',
    'tier_id'
  ]
}

interface AccountEditBasicRow {
  id: string
  config_revision: number
  system_account_id: string
  provider_code: ProviderCode
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  notes: string | null
  type: AccountType
  credentials_encrypted: string
  status: AccountStatus
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  client_compatibility: AccountClientCompatibility
  health_check_model: string
  health_check_endpoint_mode: AccountHealthCheckEndpointMode
  authorization_instance_authorization_id: string | null
  authorization_instance_source_account_id: string | null
  bound_group_id: string | null
  bound_group_name: string | null
}

interface AccountEditBasicTagRow {
  id: string
  name: string
}

interface AccountEditBasicModelRow {
  model: string
}

export class AccountEditBasicForbiddenError extends Error {
  constructor() {
    super('无权查看账户凭据')
    this.name = 'AccountEditBasicForbiddenError'
  }
}

export async function findAccountEditBasicDetailAsync(
  accountId: string,
  access?: AccessScope
): Promise<AccountEditBasicDetail | undefined> {
  const id = accountId.trim()
  if (!id) return undefined
  const client = await accountEditBasicDatabaseClient()
  const row = await client.one<AccountEditBasicRow>(`
    SELECT
      accounts.id,
      accounts.config_revision,
      accounts.system_account_id,
      accounts.provider_code,
      accounts.provider_protocol_profile_id,
      accounts.protocol_code,
      accounts.protocol_version,
      accounts.name,
      accounts.notes,
      accounts.type,
      accounts.credentials_encrypted,
      accounts.status,
      accounts.concurrency_limit,
      accounts.priority,
      accounts.super_priority_enabled,
      accounts.fallback_enabled,
      accounts.client_compatibility,
      accounts.health_check_model,
      accounts.health_check_endpoint_mode,
      accounts.authorization_instance_authorization_id,
      accounts.authorization_instance_source_account_id,
      (
        SELECT group_accounts.group_id
        FROM ${accountEditBasicTable(client, 'group_accounts')} group_accounts
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = accounts.system_account_id
          AND group_accounts.enabled = 1
        ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC
        LIMIT 1
      ) AS bound_group_id,
      (
        SELECT groups.name
        FROM ${accountEditBasicTable(client, 'group_accounts')} group_accounts
        INNER JOIN ${accountEditBasicTable(client, 'groups')} groups
          ON groups.id = group_accounts.group_id
        WHERE group_accounts.account_id = accounts.id
          AND group_accounts.system_account_id = accounts.system_account_id
          AND group_accounts.enabled = 1
        ORDER BY group_accounts.updated_at DESC, group_accounts.group_id ASC
        LIMIT 1
      ) AS bound_group_name
    FROM ${accountEditBasicTable(client, 'accounts')} accounts
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
    LIMIT 1
  `, [id])
  if (!row || !canManageResourceOwner(row.system_account_id, access)) return undefined
  if (row.authorization_instance_authorization_id || row.authorization_instance_source_account_id) {
    throw new AccountEditBasicForbiddenError()
  }

  const [modelRows, tagRows] = await Promise.all([
    client.query<AccountEditBasicModelRow>(`
      SELECT model
      FROM ${accountEditBasicTable(client, 'account_supported_models')}
      WHERE account_id = ?
      ORDER BY model ASC
    `, [row.id]),
    client.query<AccountEditBasicTagRow>(`
      SELECT account_tags.id, account_tags.name
      FROM ${accountEditBasicTable(client, 'account_tag_bindings')} account_tag_bindings
      INNER JOIN ${accountEditBasicTable(client, 'account_tags')} account_tags
        ON account_tags.id = account_tag_bindings.tag_id
      WHERE account_tag_bindings.account_id = ?
      ORDER BY account_tags.name ASC, account_tags.id ASC
    `, [row.id])
  ])
  const credentials = decryptJson<Record<string, unknown>>(row.credentials_encrypted)
  return {
    id: row.id,
    configRevision: Number(row.config_revision ?? 1),
    systemAccountId: includeSystemAccountFields(access) ? row.system_account_id : undefined,
    ownerSystemAccountId: row.system_account_id,
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    name: row.name,
    notes: row.notes ?? undefined,
    type: row.type,
    credentials: projectEditableCredentials(row.type, credentials),
    status: row.status,
    concurrencyLimit: Number(row.concurrency_limit),
    priority: Number(row.priority),
    superPriorityEnabled: row.super_priority_enabled === 1,
    fallbackEnabled: row.fallback_enabled === 1,
    clientCompatibility: row.client_compatibility,
    supportedModels: modelRows.map((item) => item.model),
    tags: tagRows.map((item) => ({ id: item.id, name: item.name })),
    healthCheckModel: row.health_check_model.trim(),
    healthCheckEndpointMode: row.health_check_endpoint_mode,
    boundGroupId: row.bound_group_id ?? undefined,
    boundGroupName: row.bound_group_name ?? undefined
  }
}

export function projectEditableCredentials(
  accountType: AccountType,
  credentials: Record<string, unknown>
): AccountCredentials {
  const output: AccountCredentials = {}
  for (const key of [...basicEditableCredentialKeys, ...(editableCredentialKeysByAccountType[accountType] ?? [])]) {
    if (Object.prototype.hasOwnProperty.call(credentials, key)) {
      output[key] = credentials[key]
    }
  }
  return output
}

async function accountEditBasicDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountEditBasicTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
