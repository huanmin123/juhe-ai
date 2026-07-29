import { isDeepStrictEqual } from 'node:util'

import type { AccountStatus, AccountType, ProviderCode } from '../domain/types.js'
import { accountCircuitCredentialOwnerIdentity } from '../domain/account-circuit-owner.js'
import { invalidateGatewayRuntimeAfterBusinessWrite } from './account-runtime-mutation-helpers.js'
import { normalizeAccountCredentialsForWrite, requiredAccountCredentialSource } from './account-credentials-normalization.js'
import { advanceAccountCircuitDispatchRevisionFamilyInTransaction } from './account-circuit-control-plane.repository.js'
import { accountCredentialFingerprint } from './account-identity.js'
import { manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { decryptJson, encryptJson, maskSecret } from './crypto.js'
import { getBusinessDatabase, newId, nowIso } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { invalidateAccountLookupCache } from './repository-lookups.js'
import { runtimeConfig } from '../config/runtime.js'
import { AccountConfigRevisionConflictError } from './account-config-revision.js'

const businessSchemaName = 'juhe_business'

export interface OAuthCredentialRotationAccount {
  id: string
  systemAccountId: string
  providerCode: ProviderCode
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  name: string
  type: AccountType
  status: AccountStatus
  lastErrorCode?: string
  proxyProfileId?: string
  credentials: Record<string, unknown>
  credentialFingerprint?: string
  configRevision: number
  updatedAt: string
}

export interface OAuthCredentialRotationResult {
  id: string
  configRevision: number
  updatedAt: string
  changed: boolean
  credentials: Record<string, unknown>
}

export async function findOAuthCredentialRotationAccountAsync(
  accountId: string,
  access?: AccessScope
): Promise<OAuthCredentialRotationAccount | undefined> {
  const client = await databaseClient()
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const row = await client.one<OAuthCredentialRotationRow>(`
    SELECT id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, last_error_code,
      proxy_profile_id, credentials_encrypted, credential_fingerprint,
      config_revision, updated_at
    FROM ${tableName(client, 'accounts')}
    WHERE id = ?
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
      ${ownerSystemAccountId ? 'AND system_account_id = ?' : ''}
    LIMIT 1
  `, ownerSystemAccountId ? [accountId, ownerSystemAccountId] : [accountId])
  return row ? mapRotationAccount(row) : undefined
}

export async function rotateOAuthCredentialsAsync(input: {
  accountId: string
  expectedConfigRevision: number
  expectedProviderCode: ProviderCode
  expectedAccountType: 'oauth' | 'google_oauth'
  expectedProviderProtocolProfileId: string
  credentials: Record<string, unknown>
  access?: AccessScope
}): Promise<OAuthCredentialRotationResult | undefined> {
  if (!Number.isInteger(input.expectedConfigRevision) || input.expectedConfigRevision < 1) {
    throw new Error('账户配置版本无效')
  }
  const client = await databaseClient()
  const ownerSystemAccountId = manageableSystemAccountId(input.access)
  const result = await client.transaction(async (tx) => {
    const row = await tx.one<OAuthCredentialRotationRow>(`
      SELECT id, system_account_id, provider_code, provider_protocol_profile_id,
        protocol_code, protocol_version, name, type, status, last_error_code,
        proxy_profile_id, credentials_encrypted, credential_fingerprint,
        config_revision, updated_at
      FROM ${tableName(tx, 'accounts')}
      WHERE id = ?
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL
        ${ownerSystemAccountId ? 'AND system_account_id = ?' : ''}
      ${tx.driver === 'postgres' ? 'FOR UPDATE' : ''}
    `, ownerSystemAccountId ? [input.accountId, ownerSystemAccountId] : [input.accountId])
    if (!row) return undefined
    const current = mapRotationAccount(row)
    if (current.providerCode !== input.expectedProviderCode
      || current.type !== input.expectedAccountType
      || current.providerProtocolProfileId !== input.expectedProviderProtocolProfileId) {
      return undefined
    }
    if (current.configRevision !== input.expectedConfigRevision) {
      throw new AccountConfigRevisionConflictError(input.accountId, input.expectedConfigRevision, current.configRevision)
    }
    const credentials = normalizeAccountCredentialsForWrite(current.type, input.credentials, {
      providerCode: current.providerCode,
      accountType: current.type,
      providerProtocolProfileId: current.providerProtocolProfileId,
      protocolCode: current.protocolCode,
      protocolVersion: current.protocolVersion
    })
    if (isDeepStrictEqual(current.credentials, credentials)) {
      return {
        id: current.id,
        configRevision: current.configRevision,
        updatedAt: current.updatedAt,
        changed: false,
        credentials
      }
    }
    const credentialSource = requiredAccountCredentialSource(current.type, credentials)
    const updatedAt = nowIso()
    const update = await tx.execute(`
      UPDATE ${tableName(tx, 'accounts')}
      SET credentials_encrypted = ?,
          credential_fingerprint = ?,
          credential_mask = ?,
          oauth_access_token_expires_at = ?,
          oauth_refresh_token_present = ?,
          config_revision = config_revision + 1,
          updated_at = ?
      WHERE id = ?
        AND system_account_id = ?
        AND provider_code = ?
        AND provider_protocol_profile_id = ?
        AND type = ?
        AND config_revision = ?
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL
    `, [
      encryptJson(credentials),
      accountCredentialFingerprint(credentialSource),
      maskSecret(credentialSource),
      optionalIso(credentials.expires_at),
      typeof credentials.refresh_token === 'string' && credentials.refresh_token.trim() ? 1 : 0,
      updatedAt,
      current.id,
      current.systemAccountId,
      current.providerCode,
      current.providerProtocolProfileId,
      current.type,
      current.configRevision
    ])
    if (update.changes !== 1) {
      throw new AccountConfigRevisionConflictError(input.accountId, input.expectedConfigRevision)
    }
    if (!isDeepStrictEqual(
      accountCircuitCredentialOwnerIdentity(current.credentials),
      accountCircuitCredentialOwnerIdentity(credentials)
    )) {
      await advanceAccountCircuitDispatchRevisionFamilyInTransaction(tx, {
        accountId: current.id,
        accountRuntimeKey: current.id,
        transitionId: newId('dispatch'),
        nowMs: Date.parse(updatedAt)
      })
    }
    return {
      id: current.id,
      configRevision: current.configRevision + 1,
      updatedAt,
      changed: true,
      credentials
    }
  })
  if (result?.changed) {
    invalidateAccountLookupCache(result.id)
    invalidateGatewayRuntimeAfterBusinessWrite('oauth_credentials_rotated')
  }
  return result
}

interface OAuthCredentialRotationRow {
  id: string
  system_account_id: string
  provider_code: ProviderCode
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string
  type: AccountType
  status: AccountStatus
  last_error_code: string | null
  proxy_profile_id: string | null
  credentials_encrypted: string
  credential_fingerprint: string | null
  config_revision: number
  updated_at: string
}

function mapRotationAccount(row: OAuthCredentialRotationRow): OAuthCredentialRotationAccount {
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    providerCode: row.provider_code,
    providerProtocolProfileId: row.provider_protocol_profile_id,
    protocolCode: row.protocol_code,
    protocolVersion: row.protocol_version,
    name: row.name,
    type: row.type,
    status: row.status,
    lastErrorCode: row.last_error_code ?? undefined,
    proxyProfileId: row.proxy_profile_id ?? undefined,
    credentials: decryptJson<Record<string, unknown>>(row.credentials_encrypted),
    credentialFingerprint: row.credential_fingerprint ?? undefined,
    configRevision: Math.max(1, Number(row.config_revision) || 1),
    updatedAt: row.updated_at
  }
}

async function databaseClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

function tableName(client: DatabaseClient, name: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, name)
    : client.dialect.quoteIdentifier(name)
}

function optionalIso(value: unknown): string | null {
  if (typeof value !== 'string' || !value.trim()) return null
  const timestamp = Date.parse(value)
  return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : null
}
