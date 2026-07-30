import { runtimeConfig } from '../config/runtime.js'
import type {
  AccountAvailabilitySchedule,
  AccountCredentials,
  AccountModelMapping,
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily,
  AccountStatus
} from '../domain/types.js'
import { normalizeAccountBalanceConfig } from '../modules/accounts/account-balance-config.js'
import type { AccountBalanceQueryConfig } from '../modules/accounts/account-balance.types.js'
import { parseAccountAvailabilityScheduleJson } from './account-availability-schedule.js'
import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase, nowIso } from './database.js'
import type { DatabaseClient } from './database-client.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { canManageResourceOwner } from './resource-authorization-helpers.js'

const businessSchemaName = 'juhe_business'

const advancedEditableCredentialKeys = [
  'service_tier_override',
  'reasoning_effort_override',
  'error_handling_rules',
  'response_inspection_rules'
] as const

interface AccountAdvancedDetailRow {
  id: string
  config_revision: number
  system_account_id: string
  credentials_encrypted: string | null
  proxy_profile_id: string | null
  availability_schedule_json: string | null
  account_expires_at: string | null
  temporary_unavailable_continuous_probe_enabled: number
  balance_query_enabled: number
  balance_query_config_json: string
  authorization_instance_authorization_id: string | null
  authorization_instance_source_account_id: string | null
  active_authorization_id: string | null
  source_status: AccountStatus | null
  source_schedulable: number | null
  source_proxy_profile_id: string | null
  source_availability_schedule_json: string | null
  source_account_expires_at: string | null
  source_temporary_unavailable_continuous_probe_enabled: number | null
}

interface AccountAdvancedDetailMappingRow {
  source_model: string
  source_endpoint_family: AccountModelMappingSourceEndpointFamily
  upstream_model: string
  upstream_endpoint_family: AccountModelMappingUpstreamEndpointFamily
  enabled: number
}

export interface AccountAdvancedDetail {
  id: string
  configRevision: number
  accessType: 'owner' | 'authorized'
  credentials?: AccountCredentials
  modelMappings: AccountModelMapping[]
  proxyProfileId?: string
  availabilitySchedule?: AccountAvailabilitySchedule
  accountExpiresAt?: string
  temporaryUnavailableContinuousProbeEnabled: boolean
  balanceQueryEnabled: boolean
  balanceQueryConfig?: AccountBalanceQueryConfig
  authorizationInstanceSourceAccountStatus?: AccountStatus
  authorizationInstanceSourceAccountSchedulable?: boolean
}

export async function findAccountAdvancedDetailAsync(
  accountId: string,
  access?: AccessScope
): Promise<AccountAdvancedDetail | undefined> {
  const id = accountId.trim()
  if (!id) return undefined
  const client = await accountAdvancedDetailDatabaseClient()
  const ownerScope = buildSystemAccountScopeClause(access, 'accounts.system_account_id')
  const row = await client.one<AccountAdvancedDetailRow>(`
    SELECT
      accounts.id,
      accounts.config_revision,
      accounts.system_account_id,
      CASE
        WHEN accounts.authorization_instance_authorization_id IS NULL
          AND accounts.authorization_instance_source_account_id IS NULL
        THEN accounts.credentials_encrypted
        ELSE NULL
      END AS credentials_encrypted,
      accounts.proxy_profile_id,
      accounts.availability_schedule_json,
      accounts.account_expires_at,
      accounts.temporary_unavailable_continuous_probe_enabled,
      accounts.balance_query_enabled,
      accounts.balance_query_config_json,
      accounts.authorization_instance_authorization_id,
      accounts.authorization_instance_source_account_id,
      active_authorizations.id AS active_authorization_id,
      source_accounts.status AS source_status,
      source_accounts.schedulable AS source_schedulable,
      source_accounts.proxy_profile_id AS source_proxy_profile_id,
      source_accounts.availability_schedule_json AS source_availability_schedule_json,
      source_accounts.account_expires_at AS source_account_expires_at,
      source_accounts.temporary_unavailable_continuous_probe_enabled AS source_temporary_unavailable_continuous_probe_enabled
    FROM ${accountAdvancedDetailTable(client, 'accounts')} accounts
    LEFT JOIN ${accountAdvancedDetailTable(client, 'resource_authorizations')} active_authorizations
      ON active_authorizations.id = accounts.authorization_instance_authorization_id
      AND active_authorizations.resource_type = 'account'
      AND active_authorizations.resource_id = accounts.authorization_instance_source_account_id
      AND active_authorizations.resource_owner_system_account_id = accounts.authorization_instance_owner_system_account_id
      AND active_authorizations.grantee_system_account_id = accounts.system_account_id
      AND active_authorizations.status = 'active'
      AND active_authorizations.effective_source_type IN ('manual', 'team')
      AND (active_authorizations.expires_at IS NULL OR active_authorizations.expires_at > ?)
    LEFT JOIN ${accountAdvancedDetailTable(client, 'accounts')} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.system_account_id = active_authorizations.resource_owner_system_account_id
      AND source_accounts.deleted_at IS NULL
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      ${ownerScope.clause}
    LIMIT 1
  `, [nowIso(), id, ...ownerScope.params])
  if (!row || !canManageResourceOwner(row.system_account_id, access)) return undefined
  const authorized = Boolean(row.authorization_instance_authorization_id || row.authorization_instance_source_account_id)
  if (authorized && !row.active_authorization_id) return undefined
  const factAccountId = authorized ? row.authorization_instance_source_account_id! : row.id

  const mappingRows = await client.query<AccountAdvancedDetailMappingRow>(`
    SELECT source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
    FROM ${accountAdvancedDetailTable(client, 'account_model_mappings')}
    WHERE account_id = ?
    ORDER BY source_model ASC, source_endpoint_family ASC
  `, [factAccountId])
  const advancedCredentials = !authorized && row.credentials_encrypted
    ? projectAdvancedEditableCredentials(decryptJson<AccountCredentials>(row.credentials_encrypted))
    : undefined

  return {
    id: row.id,
    configRevision: Number(row.config_revision ?? 1),
    accessType: authorized ? 'authorized' : 'owner',
    ...(advancedCredentials && Object.keys(advancedCredentials).length > 0
      ? { credentials: advancedCredentials }
      : {}),
    modelMappings: mappingRows.map(accountAdvancedDetailMappingFromRow),
    proxyProfileId: (authorized ? row.source_proxy_profile_id : row.proxy_profile_id) ?? undefined,
    availabilitySchedule: parseAccountAvailabilityScheduleJson(authorized
      ? row.source_availability_schedule_json
      : row.availability_schedule_json),
    accountExpiresAt: (authorized ? row.source_account_expires_at : row.account_expires_at) ?? undefined,
    temporaryUnavailableContinuousProbeEnabled: Number(authorized
      ? row.source_temporary_unavailable_continuous_probe_enabled
      : row.temporary_unavailable_continuous_probe_enabled) === 1,
    balanceQueryEnabled: authorized ? false : Number(row.balance_query_enabled) === 1,
    balanceQueryConfig: authorized ? undefined : parseAccountAdvancedBalanceConfig(row.balance_query_config_json),
    ...(authorized
      ? {
          authorizationInstanceSourceAccountStatus: row.source_status ?? undefined,
          authorizationInstanceSourceAccountSchedulable: row.source_schedulable !== null
            ? Number(row.source_schedulable) === 1
            : undefined
        }
      : {})
  }
}

function projectAdvancedEditableCredentials(credentials: AccountCredentials): AccountCredentials {
  const output: Record<string, unknown> = {}
  for (const key of advancedEditableCredentialKeys) {
    if (Object.prototype.hasOwnProperty.call(credentials, key)) output[key] = credentials[key]
  }
  return output
}

function accountAdvancedDetailMappingFromRow(row: AccountAdvancedDetailMappingRow): AccountModelMapping {
  return {
    sourceModel: row.source_model,
    sourceEndpointFamily: row.source_endpoint_family,
    upstreamModel: row.upstream_model,
    upstreamEndpointFamily: row.upstream_endpoint_family,
    enabled: Number(row.enabled) === 1
  }
}

function parseAccountAdvancedBalanceConfig(value: string): AccountBalanceQueryConfig | undefined {
  try {
    const parsed = JSON.parse(value) as unknown
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed) || Object.keys(parsed).length === 0) {
      return undefined
    }
    return normalizeAccountBalanceConfig(parsed)
  } catch {
    return undefined
  }
}

async function accountAdvancedDetailDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function accountAdvancedDetailTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
