import type { AccountSummary } from '../../domain/types.js'
import { sensitiveFingerprint } from '../deduplication/mutation-guard.middleware.js'

export function accountCredentialFingerprint(credentials: unknown): string {
  if (typeof credentials !== 'object' || credentials === null || Array.isArray(credentials)) {
    return ''
  }
  const record = credentials as Record<string, unknown>
  return sensitiveFingerprint(
    apiKeyCredentialFingerprintSource(record)
      ?? record.identity_token
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
  const credentials = { ...account.credentials, ...requested }
  preserveCredentialText(credentials, account.credentials, 'base_url')
  preserveCredentialArray(credentials, account.credentials, 'supported_endpoint_modes')
  if (account.type === 'api_key') {
    const replacesWithSingleKey = hasCredentialText(requested.api_key) && !hasCredentialStringArray(requested.api_keys)
    const replacesWithMultipleKeys = hasCredentialStringArray(requested.api_keys)
    if (replacesWithSingleKey) {
      delete credentials.api_keys
      delete credentials.api_key_strategy
      delete credentials.api_key_weights
    } else if (replacesWithMultipleKeys) {
      delete credentials.api_key
      preserveCredentialText(credentials, account.credentials, 'api_key_strategy')
      preserveCredentialArray(credentials, account.credentials, 'api_key_weights')
    } else {
      preserveCredentialText(credentials, account.credentials, 'api_key')
      preserveCredentialArray(credentials, account.credentials, 'api_keys')
      preserveCredentialText(credentials, account.credentials, 'api_key_strategy')
      preserveCredentialArray(credentials, account.credentials, 'api_key_weights')
    }
  } else if (account.type === 'oauth' || account.type === 'google_oauth') {
    for (const key of account.type === 'google_oauth'
        ? [
            'access_token',
            'refresh_token',
            'expires_at',
            'client_id',
            'client_secret',
            'quota_project_id',
            'oauth_type',
            'project_id',
            'tier_id',
            'scope',
            'token_type',
            'drive_storage_limit',
            'drive_storage_usage',
            'drive_tier_updated_at'
          ]
        : [
            'access_token',
            'refresh_token',
            'expires_at',
            'client_id',
            'id_token',
            'token_type',
            'scope',
            'email',
            'account_id',
            'organization_id',
            'chatgpt_user_id',
            'plan_type',
            'sub',
            'team_id',
            'subscription_tier',
            'entitlement_status'
        ]) {
      preserveCredentialText(credentials, account.credentials, key)
    }
  }
  return credentials
}

export function applyAccountCredentialsPatch(
  current: Record<string, unknown>,
  patch: Record<string, unknown>
): Record<string, unknown> {
  const credentials = { ...current }
  for (const [key, value] of Object.entries(patch)) {
    if (value === null) {
      delete credentials[key]
    } else {
      credentials[key] = value
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

function hasCredentialStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.some(hasCredentialText)
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
