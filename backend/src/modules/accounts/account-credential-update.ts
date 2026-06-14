import type { AccountSummary } from '../../domain/types.js'
import { sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'

export function accountCredentialFingerprint(credentials: unknown): string {
  if (typeof credentials !== 'object' || credentials === null || Array.isArray(credentials)) {
    return ''
  }
  const record = credentials as Record<string, unknown>
  return sensitiveFingerprint(
    apiKeyCredentialFingerprintSource(record)
      ?? record.refresh_token
      ?? record.access_token
      ?? record.email
      ?? record.account_id
      ?? ''
  )
}

export function credentialsRecordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

export function mergeAccountCredentialsForUpdate(account: AccountSummary, requested: Record<string, unknown>): Record<string, unknown> {
  const credentials = { ...requested }
  preserveCredentialText(credentials, account.credentials, 'base_url')
  if (account.type === 'api_key') {
    preserveCredentialText(credentials, account.credentials, 'api_key')
    preserveCredentialArray(credentials, account.credentials, 'api_keys')
    preserveCredentialText(credentials, account.credentials, 'api_key_strategy')
    preserveCredentialArray(credentials, account.credentials, 'api_key_weights')
  } else if (account.type === 'oauth') {
    for (const key of [
      'access_token',
      'refresh_token',
      'expires_at',
      'client_id',
      'id_token',
      'email',
      'account_id',
      'chatgpt_user_id',
      'plan_type'
    ]) {
      preserveCredentialText(credentials, account.credentials, key)
    }
  }
  return credentials
}

function preserveCredentialText(output: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  if (hasCredentialText(output[key])) return
  const value = source[key]
  if (hasCredentialText(value)) {
    output[key] = value
  }
}

function preserveCredentialArray(output: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  if (Array.isArray(output[key]) && (output[key] as unknown[]).length > 0) return
  const value = source[key]
  if (Array.isArray(value) && value.length > 0) {
    output[key] = value
  }
}

function hasCredentialText(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function apiKeyCredentialFingerprintSource(record: Record<string, unknown>): string | undefined {
  if (Array.isArray(record.api_keys)) {
    const keys = record.api_keys
      .filter((value): value is string => typeof value === 'string' && value.trim().length > 0)
      .map((value) => value.trim())
    if (keys.length) return keys.join('\n')
  }
  return typeof record.api_key === 'string' ? record.api_key : undefined
}
