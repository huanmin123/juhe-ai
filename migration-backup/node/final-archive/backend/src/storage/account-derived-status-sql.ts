import type { DatabaseSync } from 'node:sqlite'

import { accountApiKeyEntries, isAccountApiKeyPoolIsolationEnabled, type AccountApiKeyRuntimeStatus } from './account-api-key-rotation.js'
import { isAccountAvailabilityScheduleAllowed } from './account-availability-schedule.js'
import { decryptJson } from './crypto.js'

type SqlFunctionDatabase = DatabaseSync & {
  function: (name: string, options: { deterministic?: boolean; varargs?: boolean }, fn: (...args: unknown[]) => unknown) => void
}

const registeredDatabases = new WeakSet<DatabaseSync>()
const stateRowSeparator = String.fromCharCode(31)
const stateFieldSeparator = String.fromCharCode(30)

export function ensureAccountDerivedStatusSqlFunctions(database: DatabaseSync): void {
  if (registeredDatabases.has(database)) return
  const sqlDatabase = database as SqlFunctionDatabase
  sqlDatabase.function('account_schedule_allowed', { deterministic: false }, accountScheduleAllowedForSql)
  sqlDatabase.function('account_api_key_pool_all_unavailable', { deterministic: false }, accountApiKeyPoolAllUnavailableForSql)
  registeredDatabases.add(database)
}

export function accountApiKeyRuntimeStateRowsSql(accountIdSql: string): string {
  return `(SELECT group_concat(states.key_fingerprint || char(30) || states.status, char(31))
    FROM account_api_key_runtime_states states
    WHERE states.account_id = ${accountIdSql})`
}

export function accountResourceCredentialsSql(accountIdSql: string): string {
  return `(SELECT credential_accounts.credentials_encrypted
    FROM accounts credential_accounts
    WHERE credential_accounts.id = ${accountIdSql}
      AND credential_accounts.deleted_at IS NULL
    LIMIT 1)`
}

export function accountApiKeyPoolAllUnavailableSql(input: {
  accountIdSql: string
  providerCodeSql: string
  protocolCodeSql: string
  protocolVersionSql: string
  typeSql: string
}): string {
  return `account_api_key_pool_all_unavailable(
    ${input.providerCodeSql},
    ${input.protocolCodeSql},
    ${input.protocolVersionSql},
    ${input.typeSql},
    ${accountResourceCredentialsSql(input.accountIdSql)},
    ${accountApiKeyRuntimeStateRowsSql(input.accountIdSql)}
  ) = 1`
}

function accountScheduleAllowedForSql(value: unknown): number {
  return isAccountAvailabilityScheduleAllowed(typeof value === 'string' ? value : null, new Date()) ? 1 : 0
}

function accountApiKeyPoolAllUnavailableForSql(
  providerCode: unknown,
  protocolCode: unknown,
  protocolVersion: unknown,
  type: unknown,
  credentialsEncrypted: unknown,
  stateRows: unknown
): number {
  if (typeof credentialsEncrypted !== 'string' || !credentialsEncrypted) return 0
  let credentials: Record<string, unknown>
  try {
    credentials = decryptJson<Record<string, unknown>>(credentialsEncrypted)
  } catch {
    return 0
  }
  if (!isAccountApiKeyPoolIsolationEnabled({
    providerCode,
    protocolCode,
    protocolVersion,
    type,
    credentials
  })) {
    return 0
  }
  const entries = accountApiKeyEntries(credentials)
  if (entries.length < 2) return 0
  const statesByFingerprint = accountApiKeyRuntimeStatusRows(stateRows)
  let activeCount = 0
  for (const entry of entries) {
    const status = statesByFingerprint.get(entry.fingerprint)
    if (!status || status === 'active') {
      activeCount += 1
    }
  }
  return activeCount === 0 ? 1 : 0
}

function accountApiKeyRuntimeStatusRows(value: unknown): Map<string, AccountApiKeyRuntimeStatus> {
  const output = new Map<string, AccountApiKeyRuntimeStatus>()
  if (typeof value !== 'string' || !value) return output
  for (const row of value.split(stateRowSeparator)) {
    const [fingerprint, status] = row.split(stateFieldSeparator)
    if (!fingerprint || !isAccountApiKeyRuntimeStatus(status)) continue
    output.set(fingerprint, status)
  }
  return output
}

function isAccountApiKeyRuntimeStatus(value: unknown): value is AccountApiKeyRuntimeStatus {
  return value === 'active'
    || value === 'unverified'
    || value === 'temporary_unavailable'
    || value === 'rate_limited'
    || value === 'error'
    || value === 'disabled'
}
