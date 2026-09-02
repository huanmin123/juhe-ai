import type { AccountListItem, AccountSummary, PublicAccountRuntimeAvailability } from '../../domain/types.js'
import { publicAccountRuntimeAvailability } from '../../domain/account-runtime-availability-public.js'
import type { AccountApiKeyRuntimeResponse } from './account-api-key-pool-runtime.js'
import type { AccountBalanceSnapshot } from './account-balance.types.js'

const publicCredentialKeys = new Set([
  'base_url',
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'expires_at',
  'client_id',
  'email',
  'account_id',
  'chatgpt_user_id',
  'plan_type'
])

const editBasicCredentialKeys = new Set([
  'api_key',
  'api_keys',
  'api_key_strategy',
  'api_key_weights',
  'base_url',
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'error_handling_rules',
  'response_inspection_rules',
  'quota_recovery_policy'
])

export function sanitizeAccountCredentialsForResponse(credentials: Record<string, unknown> | undefined): Record<string, unknown> {
  if (!credentials) return {}
  const output: Record<string, unknown> = {}
  for (const key of publicCredentialKeys) {
    if (Object.prototype.hasOwnProperty.call(credentials, key)) {
      output[key] = credentials[key]
    }
  }
  return output
}

export function sanitizeAccountResponse<T extends AccountSummary>(account: T): T {
  return {
    ...account,
    credentials: sanitizeAccountCredentialsForResponse(account.credentials),
    balanceSnapshot: accountBalanceSnapshotWithoutKeyDetails(account.balanceSnapshot),
    runtimeAvailability: publicAccountRuntimeAvailability(account.runtimeAvailability)
  } as T
}

function accountBalanceSnapshotWithoutKeyDetails(snapshot: AccountBalanceSnapshot | undefined): AccountBalanceSnapshot | undefined {
  if (!snapshot?.keyBalances) return snapshot
  const { keyBalances: _keyBalances, ...summary } = snapshot
  return summary
}

export function sanitizeAccountEditBasicDetailResponse<T extends AccountSummary>(account: T): T {
  const {
    modelMappings: _modelMappings,
    apiKeyRuntime: _apiKeyRuntime,
    apiKeyRuntimeDetails: _apiKeyRuntimeDetails,
    oauthUsage: _oauthUsage,
    authorizationSources: _authorizationSources,
    ...item
  } = account
  return {
    ...item,
    credentials: sanitizeAccountCredentialsByKeys(account.credentials, editBasicCredentialKeys),
    runtimeAvailability: publicAccountRuntimeAvailability(account.runtimeAvailability),
    supportedModels: [...(account.supportedModels ?? [])]
  } as T
}

export function sanitizeAccountCredentialCarrierResponse<T extends { credentials: Record<string, unknown> }>(value: T): T {
  return {
    ...value,
    credentials: sanitizeAccountCredentialsForResponse(value.credentials)
  }
}

/** @deprecated Management list routes return the exact list DTO directly. */
export function sanitizeAccountListResponse<T extends { items: AccountSummary[] }>(result: T): Omit<T, 'items'> & { items: Array<Partial<AccountListItem>> } {
  return {
    ...result,
    items: result.items.map(projectAccountListItem)
  }
}

/** @deprecated Management list routes return the exact list DTO directly. */
export function projectAccountListItem(account: AccountSummary): Partial<AccountListItem> {
  const {
    credentials: _credentials,
    supportedModels: _supportedModels,
    modelMappings: _modelMappings,
    apiKeyRuntimeDetails: _apiKeyRuntimeDetails,
    balanceQueryConfig: _balanceQueryConfig,
    usage: _usage,
    oauthUsage: _oauthUsage,
    authorizationSources: _authorizationSources,
    authorizationCount: _authorizationCount,
    authorizationTeamCount: _authorizationTeamCount,
    authorizationUsageAvailable: _authorizationUsageAvailable,
    currentConcurrencyAvailable: _currentConcurrencyAvailable,
    ...item
  } = account
  return item as unknown as Partial<AccountListItem>
}

export function sanitizeAccountBasicDetailResponse<T extends AccountSummary>(account: T): T {
  const {
    credentials: _credentials,
    supportedModels: _supportedModels,
    modelMappings: _modelMappings,
    apiKeyRuntimeDetails: _apiKeyRuntimeDetails,
    ...item
  } = account
  return {
    ...item,
    runtimeAvailability: publicAccountRuntimeAvailability(account.runtimeAvailability)
  } as T
}

export function sanitizeAccountRuntimeAvailabilityResponse<T extends { runtimeAvailability?: PublicAccountRuntimeAvailability }>(account: T): T {
  return {
    ...account,
    runtimeAvailability: publicAccountRuntimeAvailability(account.runtimeAvailability)
  }
}

export function sanitizeAccountApiKeyRuntimeResponse(value: AccountApiKeyRuntimeResponse): AccountApiKeyRuntimeResponse {
  return {
    accountId: value.accountId,
    configRevision: value.configRevision,
    items: value.items.map((item) => ({
      keyIndex: item.keyIndex,
      keyFingerprintPrefix: item.keyFingerprintPrefix,
      keySuffix: item.keySuffix,
      weight: item.weight,
      status: item.status,
      failureCount: item.failureCount,
      consecutiveFailures: item.consecutiveFailures,
      successCount: item.successCount,
      cooldownUntil: item.cooldownUntil,
      nextProbeAt: item.nextProbeAt,
      lastAttemptAt: item.lastAttemptAt,
      lastSuccessAt: item.lastSuccessAt,
      lastFailureAt: item.lastFailureAt,
      lastErrorCode: item.lastErrorCode,
      lastErrorMessage: item.lastErrorMessage,
      lastTraceId: item.lastTraceId
    }))
  }
}

function sanitizeAccountCredentialsByKeys(
  credentials: Record<string, unknown> | undefined,
  keys: Set<string>
): Record<string, unknown> {
  if (!credentials) return {}
  const output: Record<string, unknown> = {}
  for (const key of keys) {
    if (Object.prototype.hasOwnProperty.call(credentials, key)) {
      output[key] = credentials[key]
    }
  }
  return output
}

export function sanitizeAccountTrafficMigrationResponse<T extends { sourceAccount: AccountSummary; targetAccount: AccountSummary }>(result: T): T {
  return {
    ...result,
    sourceAccount: sanitizeAccountResponse(result.sourceAccount),
    targetAccount: sanitizeAccountResponse(result.targetAccount)
  }
}
