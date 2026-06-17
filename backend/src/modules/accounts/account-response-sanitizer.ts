import type { AccountSummary } from '../../domain/types.js'

const publicCredentialKeys = new Set([
  'base_url',
  'supported_endpoint_modes',
  'expires_at',
  'client_id',
  'email',
  'account_id',
  'chatgpt_user_id',
  'plan_type'
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

export function sanitizeAccountCredentialCarrierResponse<T extends { credentials: Record<string, unknown> }>(value: T): T {
  return {
    ...value,
    credentials: sanitizeAccountCredentialsForResponse(value.credentials)
  }
}

export function sanitizeAccountListResponse<T extends { items: AccountSummary[] }>(result: T): T {
  return {
    ...result,
    items: result.items.map(sanitizeAccountResponse)
  }
}

export function sanitizeAccountTrafficMigrationResponse<T extends { sourceAccount: AccountSummary; targetAccount: AccountSummary }>(result: T): T {
  return {
    ...result,
    sourceAccount: sanitizeAccountResponse(result.sourceAccount),
    targetAccount: sanitizeAccountResponse(result.targetAccount)
  }
}
