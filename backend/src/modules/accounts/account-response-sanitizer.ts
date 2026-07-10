import type { AccountSummary } from '../../domain/types.js'

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
  'reasoning_effort_override'
])

const batchEditCredentialKeys = new Set([
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'error_handling_rules',
  'response_inspection_rules'
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
    credentials: sanitizeAccountCredentialsForResponse(account.credentials)
  } as T
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
    supportedModels: [...(account.supportedModels ?? [])]
  } as T
}

export function sanitizeAccountBatchEditDetailResponse<T extends AccountSummary>(account: T): T {
  const {
    apiKeyRuntime: _apiKeyRuntime,
    apiKeyRuntimeDetails: _apiKeyRuntimeDetails,
    oauthUsage: _oauthUsage,
    authorizationSources: _authorizationSources,
    ...item
  } = account
  return {
    ...item,
    credentials: sanitizeAccountCredentialsByKeys(account.credentials, batchEditCredentialKeys),
    supportedModels: [...(account.supportedModels ?? [])],
    modelMappings: [...(account.modelMappings ?? [])],
    tags: [...(account.tags ?? [])]
  } as T
}

export function sanitizeAccountCredentialCarrierResponse<T extends { credentials: Record<string, unknown> }>(value: T): T {
  return {
    ...value,
    credentials: sanitizeAccountCredentialsForResponse(value.credentials)
  }
}

export function sanitizeAccountListResponse<T extends { items: AccountSummary[] }>(result: T): T {
  return {
    ...result,
    items: result.items.map(sanitizeAccountBasicDetailResponse)
  }
}

export function sanitizeAccountBasicDetailResponse<T extends AccountSummary>(account: T): T {
  const {
    credentials: _credentials,
    supportedModels: _supportedModels,
    modelMappings: _modelMappings,
    apiKeyRuntimeDetails: _apiKeyRuntimeDetails,
    ...item
  } = account
  return item as T
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
