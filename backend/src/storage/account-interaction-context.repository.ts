import { runtimeConfig } from '../config/runtime.js'
import { GEMINI_PROVIDER_CODE } from '../domain/provider-protocol.js'
import type {
  AccountAvailabilitySchedule,
  AccountClientCompatibility,
  AccountCredentials,
  AccountHealthCheckEndpointMode,
  AccountModelMapping,
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily,
  AccountTagSummary,
  AccountStatus,
  AccountType,
  ProviderCode
} from '../domain/types.js'
import { normalizeAccountBalanceConfig } from '../modules/accounts/account-balance-config.js'
import type { AccountBalanceQueryConfig } from '../modules/accounts/account-balance.types.js'
import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { canManageResourceOwner } from './resource-authorization-helpers.js'

const businessSchemaName = 'juhe_business'

const geminiOAuthMetadataKeys = [
  'oauth_type',
  'client_id',
  'client_secret',
  'quota_project_id',
  'project_id',
  'tier_id',
  'base_url'
] as const

interface AccountInteractionContextRow {
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
  status: AccountStatus
  credentials_encrypted: string
  concurrency_limit: number
  priority: number
  super_priority_enabled: number
  fallback_enabled: number
  client_compatibility: AccountClientCompatibility
  health_check_model: string
  health_check_endpoint_mode: AccountHealthCheckEndpointMode
  proxy_profile_id: string | null
  availability_schedule_json: string | null
  account_expires_at: string | null
  temporary_unavailable_continuous_probe_enabled: number
  balance_query_enabled: number
  balance_query_config_json: string
  authorization_instance_authorization_id: string | null
  authorization_instance_source_account_id: string | null
  bound_group_id: string | null
  bound_group_name: string | null
  bound_group_binding_updated_at: string | null
  bound_group_record_updated_at: string | null
}

interface AccountCloneRelationRow {
  relation_kind: 'mapping' | 'model' | 'tag'
  value_a: string
  value_b: string | null
  value_c: string | null
  value_d: string | null
  enabled: number | null
}

interface AccountCloneRevisionRow {
  config_revision: number
  bound_group_id: string | null
  bound_group_binding_updated_at: string | null
  bound_group_record_updated_at: string | null
}

interface AccountOAuthReauthorizationRow {
  id: string
  config_revision: number
  system_account_id: string
  credentials_encrypted: string
  authorization_instance_authorization_id: string | null
  authorization_instance_source_account_id: string | null
}

export interface AccountOAuthReauthorizationContext {
  id: string
  configRevision: number
  oauthType: 'code_assist' | 'google_one' | 'ai_studio'
  clientId?: string
  clientSecret?: string
  quotaProjectId?: string
  projectId?: string
  tierId?: string
  baseUrl?: string
}

export interface AccountCloneContext {
  id: string
  configRevision: number
  providerCode: ProviderCode
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  name: string
  notes?: string
  type: AccountType
  status: AccountStatus
  credentialOptions: AccountCloneCredentialOptions
  concurrencyLimit: number
  priority: number
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  clientCompatibility: AccountClientCompatibility
  supportedModels: string[]
  tags: Array<Pick<AccountTagSummary, 'id' | 'name'>>
  healthCheckModel: string
  healthCheckEndpointMode: AccountHealthCheckEndpointMode
  boundGroupId?: string
  boundGroupName?: string
  modelMappings: AccountModelMapping[]
  proxyProfileId?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  accountExpiresAt?: string
  temporaryUnavailableContinuousProbeEnabled: boolean
  balanceQueryEnabled: boolean
  balanceQueryConfig?: AccountBalanceQueryConfig
}

export interface AccountCloneCredentialOptions {
  api_key_count?: number
  api_key_strategy?: 'round_robin' | 'weighted_round_robin'
  api_key_weights?: number[]
  base_url?: string
  supported_endpoint_modes?: AccountCredentials['supported_endpoint_modes']
  client_id?: string
  quota_project_id?: string
  oauth_type?: 'code_assist' | 'google_one' | 'ai_studio'
  tier_id?: string
  project_id?: string
  service_tier_override?: string
  reasoning_effort_override?: string
  error_handling_rules?: unknown[]
  response_inspection_rules?: unknown[]
}

export class AccountInteractionContextForbiddenError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'AccountInteractionContextForbiddenError'
  }
}

export class AccountInteractionContextConflictError extends Error {
  constructor() {
    super('账户配置已发生变化，请重试')
    this.name = 'AccountInteractionContextConflictError'
  }
}

export async function findAccountOAuthReauthorizationContextAsync(
  accountId: string,
  access?: AccessScope
): Promise<AccountOAuthReauthorizationContext | undefined> {
  const id = accountId.trim()
  if (!id) return undefined
  const client = await accountInteractionContextDatabaseClient()
  const ownerScope = buildSystemAccountScopeClause(access, 'accounts.system_account_id')
  const row = await client.one<AccountOAuthReauthorizationRow>(`
    SELECT
      accounts.id,
      accounts.config_revision,
      accounts.system_account_id,
      accounts.credentials_encrypted,
      accounts.authorization_instance_authorization_id,
      accounts.authorization_instance_source_account_id
    FROM ${accountInteractionContextTable(client, 'accounts')} accounts
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      AND accounts.provider_code = '${GEMINI_PROVIDER_CODE}'
      AND accounts.type = 'google_oauth'
      ${ownerScope.clause}
    LIMIT 1
  `, [id, ...ownerScope.params])
  if (!row || !canManageResourceOwner(row.system_account_id, access)) return undefined
  if (row.authorization_instance_authorization_id || row.authorization_instance_source_account_id) {
    throw new AccountInteractionContextForbiddenError('授权实例不能重新授权')
  }
  const credentials = decryptJson<AccountCredentials>(row.credentials_encrypted)
  const metadata = projectCredentialKeys(credentials, geminiOAuthMetadataKeys)
  const oauthType = geminiOAuthContextType(metadata)
  return {
    id: row.id,
    configRevision: Number(row.config_revision ?? 1),
    oauthType,
    ...(oauthType === 'ai_studio' ? {
      ...stringContextField('clientId', metadata.client_id),
      ...stringContextField('clientSecret', metadata.client_secret)
    } : {}),
    ...stringContextField('quotaProjectId', metadata.quota_project_id),
    ...stringContextField('projectId', metadata.project_id),
    ...stringContextField('tierId', metadata.tier_id),
    ...stringContextField('baseUrl', metadata.base_url)
  }
}

function geminiOAuthContextType(credentials: AccountCredentials): 'code_assist' | 'google_one' | 'ai_studio' {
  const explicit = credentialText(credentials.oauth_type)
  if (explicit === 'code_assist' || explicit === 'google_one' || explicit === 'ai_studio') return explicit
  const baseUrl = credentialText(credentials.base_url)
  if (baseUrl.includes('generativelanguage.googleapis.com')) return 'ai_studio'
  if (credentialText(credentials.project_id) || baseUrl.includes('cloudcode-pa.googleapis.com')) return 'code_assist'
  const clientId = credentialText(credentials.client_id)
  return clientId && clientId !== geminiCliOAuthClientId ? 'ai_studio' : 'code_assist'
}

const geminiCliOAuthClientId = '681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com'

function credentialText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export async function findAccountCloneContextAsync(
  accountId: string,
  access?: AccessScope,
  clientOverride?: DatabaseClient
): Promise<AccountCloneContext | undefined> {
  const id = accountId.trim()
  if (!id) return undefined
  const client = clientOverride ?? await accountInteractionContextDatabaseClient()
  const trueLiteral = accountInteractionContextTrueLiteral(client.driver)
  for (let attempt = 0; attempt < 2; attempt += 1) {
    const ownerScope = buildSystemAccountScopeClause(access, 'accounts.system_account_id')
    const row = await client.one<AccountInteractionContextRow>(`
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
        accounts.status,
        accounts.credentials_encrypted,
        accounts.concurrency_limit,
        accounts.priority,
        accounts.super_priority_enabled,
        accounts.fallback_enabled,
        accounts.client_compatibility,
        accounts.health_check_model,
        accounts.health_check_endpoint_mode,
        accounts.proxy_profile_id,
        accounts.availability_schedule_json,
        accounts.account_expires_at,
        accounts.temporary_unavailable_continuous_probe_enabled,
        accounts.balance_query_enabled,
        accounts.balance_query_config_json,
        accounts.authorization_instance_authorization_id,
        accounts.authorization_instance_source_account_id,
        ${accountInteractionContextCloneGroupProjection(client, trueLiteral, 'group_accounts.group_id')} AS bound_group_id,
        ${accountInteractionContextCloneGroupProjection(client, trueLiteral, 'groups.name')} AS bound_group_name,
        (
          SELECT group_accounts.updated_at
          FROM ${accountInteractionContextTable(client, 'group_accounts')} group_accounts
          INNER JOIN ${accountInteractionContextTable(client, 'groups')} groups
            ON groups.id = group_accounts.group_id
            AND groups.system_account_id = accounts.system_account_id
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = accounts.system_account_id
          ORDER BY CASE WHEN group_accounts.enabled = ${trueLiteral} THEN 0 ELSE 1 END,
            group_accounts.updated_at DESC, group_accounts.group_id ASC
          LIMIT 1
        ) AS bound_group_binding_updated_at,
        ${accountInteractionContextCloneGroupProjection(client, trueLiteral, 'groups.updated_at')} AS bound_group_record_updated_at
      FROM ${accountInteractionContextTable(client, 'accounts')} accounts
      WHERE accounts.id = ?
        AND accounts.deleted_at IS NULL
        ${ownerScope.clause}
      LIMIT 1
    `, [id, ...ownerScope.params])
    if (!row || !canManageResourceOwner(row.system_account_id, access)) return undefined
    if (row.authorization_instance_authorization_id || row.authorization_instance_source_account_id) {
      throw new AccountInteractionContextForbiddenError('授权实例不能克隆')
    }

    const relationRows = await client.query<AccountCloneRelationRow>(`
      WITH scoped_account AS (
        SELECT id, system_account_id
        FROM ${accountInteractionContextTable(client, 'accounts')}
        WHERE id = ?
          AND system_account_id = ?
          AND deleted_at IS NULL
      )
      SELECT
        'model' AS relation_kind,
        model AS value_a,
        NULL AS value_b,
        NULL AS value_c,
        NULL AS value_d,
        NULL AS enabled
      FROM ${accountInteractionContextTable(client, 'account_supported_models')} account_supported_models
      INNER JOIN scoped_account
        ON scoped_account.id = account_supported_models.account_id
      UNION ALL
      SELECT
        'tag' AS relation_kind,
        account_tags.id AS value_a,
        account_tags.name AS value_b,
        NULL AS value_c,
        NULL AS value_d,
        NULL AS enabled
      FROM ${accountInteractionContextTable(client, 'account_tag_bindings')} account_tag_bindings
      INNER JOIN scoped_account
        ON scoped_account.id = account_tag_bindings.account_id
        AND scoped_account.system_account_id = account_tag_bindings.system_account_id
      INNER JOIN ${accountInteractionContextTable(client, 'account_tags')} account_tags
        ON account_tags.id = account_tag_bindings.tag_id
        AND account_tags.system_account_id = scoped_account.system_account_id
      UNION ALL
      SELECT
        'mapping' AS relation_kind,
        source_model AS value_a,
        source_endpoint_family AS value_b,
        upstream_model AS value_c,
        upstream_endpoint_family AS value_d,
        enabled
      FROM ${accountInteractionContextTable(client, 'account_model_mappings')} account_model_mappings
      INNER JOIN scoped_account
        ON scoped_account.id = account_model_mappings.account_id
      ORDER BY relation_kind ASC, value_a ASC, value_b ASC
    `, [row.id, row.system_account_id])
    const revision = await client.one<AccountCloneRevisionRow>(`
      SELECT
        accounts.config_revision,
        ${accountInteractionContextCloneGroupProjection(client, trueLiteral, 'group_accounts.group_id')} AS bound_group_id,
        (
          SELECT group_accounts.updated_at
          FROM ${accountInteractionContextTable(client, 'group_accounts')} group_accounts
          INNER JOIN ${accountInteractionContextTable(client, 'groups')} groups
            ON groups.id = group_accounts.group_id
            AND groups.system_account_id = accounts.system_account_id
          WHERE group_accounts.account_id = accounts.id
            AND group_accounts.system_account_id = accounts.system_account_id
          ORDER BY CASE WHEN group_accounts.enabled = ${trueLiteral} THEN 0 ELSE 1 END,
            group_accounts.updated_at DESC, group_accounts.group_id ASC
          LIMIT 1
        ) AS bound_group_binding_updated_at,
        ${accountInteractionContextCloneGroupProjection(client, trueLiteral, 'groups.updated_at')} AS bound_group_record_updated_at
      FROM ${accountInteractionContextTable(client, 'accounts')} accounts
      WHERE accounts.id = ?
        AND accounts.deleted_at IS NULL
        ${ownerScope.clause}
      LIMIT 1
    `, [id, ...ownerScope.params])
    if (!revision
      || Number(revision.config_revision) !== Number(row.config_revision)
      || revision.bound_group_id !== row.bound_group_id
      || revision.bound_group_binding_updated_at !== row.bound_group_binding_updated_at
      || revision.bound_group_record_updated_at !== row.bound_group_record_updated_at) continue

    const modelRows = relationRows.filter((item) => item.relation_kind === 'model')
    const tagRows = relationRows.filter((item) => item.relation_kind === 'tag')
    const mappingRows = relationRows.filter((item) => item.relation_kind === 'mapping')
    const credentialOptions = projectCloneCredentialOptions(
      decryptJson<AccountCredentials>(row.credentials_encrypted),
    )
    return {
      id: row.id,
      configRevision: Number(row.config_revision ?? 1),
      providerCode: row.provider_code,
      providerProtocolProfileId: row.provider_protocol_profile_id,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      name: row.name,
      notes: row.notes ?? undefined,
      type: row.type,
      status: row.status,
      credentialOptions,
      concurrencyLimit: Number(row.concurrency_limit),
      priority: Number(row.priority),
      superPriorityEnabled: Number(row.super_priority_enabled) === 1,
      fallbackEnabled: Number(row.fallback_enabled) === 1,
      clientCompatibility: row.client_compatibility,
      supportedModels: modelRows.map((item) => item.value_a),
      tags: tagRows.map((item) => ({ id: item.value_a, name: item.value_b ?? '' })),
      healthCheckModel: row.health_check_model.trim(),
      healthCheckEndpointMode: row.health_check_endpoint_mode,
      boundGroupId: row.bound_group_id ?? undefined,
      boundGroupName: row.bound_group_name ?? undefined,
      modelMappings: mappingRows.map((item) => ({
        sourceModel: item.value_a,
        sourceEndpointFamily: item.value_b as AccountModelMappingSourceEndpointFamily,
        upstreamModel: item.value_c ?? '',
        upstreamEndpointFamily: item.value_d as AccountModelMappingUpstreamEndpointFamily,
        enabled: Number(item.enabled) === 1
      })),
      proxyProfileId: row.proxy_profile_id ?? undefined,
      availabilitySchedule: parseAccountAvailabilityScheduleJson(row.availability_schedule_json),
      accountExpiresAt: row.account_expires_at ?? undefined,
      temporaryUnavailableContinuousProbeEnabled: Number(row.temporary_unavailable_continuous_probe_enabled) === 1,
      balanceQueryEnabled: Number(row.balance_query_enabled) === 1,
      balanceQueryConfig: parseAccountCloneBalanceConfig(row.balance_query_config_json)
    }
  }
  throw new AccountInteractionContextConflictError()
}

function projectCloneCredentialOptions(credentials: AccountCredentials): AccountCloneCredentialOptions {
  const output: AccountCloneCredentialOptions = {}
  const apiKeyCount = cloneCredentialApiKeyCount(credentials)
  if (apiKeyCount > 0) output.api_key_count = apiKeyCount
  if (credentials.api_key_strategy === 'round_robin' || credentials.api_key_strategy === 'weighted_round_robin') {
    output.api_key_strategy = credentials.api_key_strategy
  }
  if (Array.isArray(credentials.api_key_weights)) {
    output.api_key_weights = credentials.api_key_weights
      .map((value) => Number(value))
      .filter((value) => Number.isInteger(value) && value >= 1 && value <= 100)
  }
  if (typeof credentials.base_url === 'string') output.base_url = credentials.base_url
  if (Array.isArray(credentials.supported_endpoint_modes)) output.supported_endpoint_modes = credentials.supported_endpoint_modes
  if (typeof credentials.client_id === 'string') output.client_id = credentials.client_id
  if (typeof credentials.quota_project_id === 'string') output.quota_project_id = credentials.quota_project_id
  if (credentials.oauth_type === 'code_assist' || credentials.oauth_type === 'google_one' || credentials.oauth_type === 'ai_studio') {
    output.oauth_type = credentials.oauth_type
  }
  if (typeof credentials.tier_id === 'string') output.tier_id = credentials.tier_id
  if (typeof credentials.project_id === 'string') output.project_id = credentials.project_id
  if (typeof credentials.service_tier_override === 'string') output.service_tier_override = credentials.service_tier_override
  if (typeof credentials.reasoning_effort_override === 'string') output.reasoning_effort_override = credentials.reasoning_effort_override
  if (Array.isArray(credentials.error_handling_rules)) output.error_handling_rules = credentials.error_handling_rules
  if (Array.isArray(credentials.response_inspection_rules)) output.response_inspection_rules = credentials.response_inspection_rules
  return output
}

function cloneCredentialApiKeyCount(credentials: AccountCredentials): number {
  if (Array.isArray(credentials.api_keys)) {
    const count = credentials.api_keys.filter((value) => typeof value === 'string' && value.trim()).length
    return Math.min(count, 50)
  }
  return typeof credentials.api_key === 'string' && credentials.api_key.trim() ? 1 : 0
}

function parseAccountCloneBalanceConfig(value: string): AccountBalanceQueryConfig | undefined {
  try {
    const parsed = JSON.parse(value) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed) || Object.keys(parsed).length === 0) return undefined
    return normalizeAccountBalanceConfig(parsed)
  } catch {
    return undefined
  }
}

function projectCredentialKeys(
  credentials: AccountCredentials,
  keys: readonly string[]
): AccountCredentials {
  const output: AccountCredentials = {}
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(credentials, key)) output[key] = credentials[key]
  }
  return output
}

function stringContextField<Key extends string>(key: Key, value: unknown): Partial<Record<Key, string>> {
  return typeof value === 'string' && value.trim() ? { [key]: value.trim() } as Record<Key, string> : {}
}

async function accountInteractionContextDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountInteractionContextTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

export function accountInteractionContextTrueLiteral(driver: DatabaseClient['driver']): 'TRUE' | '1' {
  return driver === 'postgres' ? 'TRUE' : '1'
}

function accountInteractionContextCloneGroupProjection(
  client: DatabaseClient,
  trueLiteral: 'TRUE' | '1',
  column: 'group_accounts.group_id' | 'groups.name' | 'groups.updated_at'
): string {
  return `(
    SELECT ${column}
    FROM ${accountInteractionContextTable(client, 'group_accounts')} group_accounts
    INNER JOIN ${accountInteractionContextTable(client, 'groups')} groups
      ON groups.id = group_accounts.group_id
      AND groups.system_account_id = accounts.system_account_id
    WHERE group_accounts.account_id = accounts.id
      AND group_accounts.system_account_id = accounts.system_account_id
    ORDER BY CASE WHEN group_accounts.enabled = ${trueLiteral} THEN 0 ELSE 1 END,
      group_accounts.updated_at DESC, group_accounts.group_id ASC
    LIMIT 1
  )`
}
