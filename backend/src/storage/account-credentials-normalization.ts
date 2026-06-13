import { normalizeAccountErrorHandlingRules } from '../modules/accounts/account-error-policy-validation.js'
import { normalizeAccountResponseInspectionRules } from '../modules/accounts/account-response-inspection-policy-validation.js'
import { assertSafeUpstreamBaseUrl } from '../shared/upstream-url-policy.js'
import { optionalServerDateTimeIso } from './value-utils.js'

const apiKeyAccountCredentialKeys = new Set([
  'api_key',
  'base_url',
  'error_handling_rules',
  'response_inspection_rules'
])

const oauthAccountCredentialKeys = new Set([
  'access_token',
  'refresh_token',
  'expires_at',
  'client_id',
  'id_token',
  'email',
  'account_id',
  'chatgpt_user_id',
  'plan_type',
  'base_url',
  'error_handling_rules',
  'response_inspection_rules'
])

const accountCredentialBaseUrlMaxBytes = 2048
const accountCredentialSecretMaxBytes = 16 * 1024
const accountCredentialMetadataMaxBytes = 4096
const accountCredentialsJsonMaxBytes = 32 * 1024

export function normalizeAccountCredentialsForWrite(accountType: string, value: unknown): Record<string, unknown> {
  const input = accountCredentialsRecord(value)
  assertKnownInputKeys(input, accountCredentialAllowedKeys(accountType), '账户凭据')
  if (accountType === 'api_key') {
    return normalizeApiKeyAccountCredentials(input)
  }
  if (accountType === 'oauth') {
    return normalizeOAuthAccountCredentials(input)
  }
  throw new Error(`账户类型 ${accountType} 不支持凭据写入`)
}

export function requiredAccountCredentialSource(accountType: string, credentials: Record<string, unknown>): string {
  if (accountType === 'oauth') {
    return requiredTextInput(credentials.refresh_token ?? credentials.access_token, 'OAuth 凭据')
  }
  if (accountType === 'api_key') {
    return requiredTextInput(credentials.api_key, 'API Key')
  }
  return requiredTextInput(credentials.api_key ?? credentials.refresh_token ?? credentials.access_token, '账户凭据')
}

function accountCredentialsRecord(value: unknown): Record<string, unknown> {
  if (value === undefined) return {}
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('账户凭据必须是对象')
  }
  return value as Record<string, unknown>
}

function accountCredentialAllowedKeys(accountType: string): ReadonlySet<string> {
  if (accountType === 'api_key') return apiKeyAccountCredentialKeys
  if (accountType === 'oauth') return oauthAccountCredentialKeys
  throw new Error(`账户类型 ${accountType} 不支持凭据写入`)
}

function normalizeApiKeyAccountCredentials(input: Record<string, unknown>): Record<string, unknown> {
  const baseUrl = requiredCredentialTextInput(input.base_url, 'Base URL', accountCredentialBaseUrlMaxBytes)
  assertSafeUpstreamBaseUrl(baseUrl)
  const credentials: Record<string, unknown> = {
    api_key: requiredCredentialTextInput(input.api_key, 'API Key', accountCredentialSecretMaxBytes),
    base_url: baseUrl
  }
  normalizeAccountCredentialPolicies(input, credentials)
  assertAccountCredentialsJsonSize(credentials)
  return credentials
}

function normalizeOAuthAccountCredentials(input: Record<string, unknown>): Record<string, unknown> {
  const accessToken = optionalCredentialText(input.access_token, 'Access Token', accountCredentialSecretMaxBytes)
  const refreshToken = optionalCredentialText(input.refresh_token, 'Refresh Token', accountCredentialSecretMaxBytes)
  if (!refreshToken && !accessToken) {
    throw new Error('OAuth 凭据不能为空')
  }

  const credentials: Record<string, unknown> = {
    base_url: requiredCredentialTextInput(input.base_url, 'Base URL', accountCredentialBaseUrlMaxBytes)
  }
  assertSafeUpstreamBaseUrl(String(credentials.base_url))
  if (accessToken) credentials.access_token = accessToken
  if (refreshToken) credentials.refresh_token = refreshToken
  const expiresAt = optionalCredentialDateTime(input.expires_at, 'Access Token 到期时间')
  if (expiresAt) credentials.expires_at = expiresAt
  copyOptionalCredentialText(input, credentials, 'client_id', 'OAuth client_id', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'id_token', 'OAuth id_token', accountCredentialSecretMaxBytes)
  copyOptionalCredentialText(input, credentials, 'email', 'OAuth email', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'account_id', 'OpenAI account_id', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'chatgpt_user_id', 'OpenAI chatgpt_user_id', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'plan_type', 'OpenAI plan_type', accountCredentialMetadataMaxBytes)
  normalizeAccountCredentialPolicies(input, credentials)
  assertAccountCredentialsJsonSize(credentials)
  return credentials
}

function normalizeAccountCredentialPolicies(input: Record<string, unknown>, credentials: Record<string, unknown>): void {
  if (Object.prototype.hasOwnProperty.call(input, 'error_handling_rules')) {
    credentials.error_handling_rules = normalizeAccountErrorHandlingRules(input.error_handling_rules)
  }
  if (Object.prototype.hasOwnProperty.call(input, 'response_inspection_rules')) {
    credentials.response_inspection_rules = normalizeAccountResponseInspectionRules(input.response_inspection_rules)
  }
}

function requiredCredentialTextInput(value: unknown, label: string, maxBytes: number): string {
  const text = requiredTextInput(value, label)
  assertCredentialTextByteLength(text, label, maxBytes)
  return text
}

function optionalCredentialText(value: unknown, label: string, maxBytes: number): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  const text = value.trim()
  assertCredentialTextByteLength(text, label, maxBytes)
  return text
}

function optionalCredentialDateTime(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  const normalized = optionalServerDateTimeIso(value)
  if (!normalized) {
    throw new Error(`${label}必须是有效时间字符串`)
  }
  return normalized
}

function copyOptionalCredentialText(input: Record<string, unknown>, output: Record<string, unknown>, key: string, label: string, maxBytes: number): void {
  const value = optionalCredentialText(input[key], label, maxBytes)
  if (value) output[key] = value
}

function assertCredentialTextByteLength(value: string, label: string, maxBytes: number): void {
  if (Buffer.byteLength(value, 'utf8') > maxBytes) {
    throw new Error(`${label}不能超过 ${maxBytes} 字节`)
  }
}

function assertAccountCredentialsJsonSize(credentials: Record<string, unknown>): void {
  const bytes = Buffer.byteLength(JSON.stringify(credentials), 'utf8')
  if (bytes > accountCredentialsJsonMaxBytes) {
    throw new Error(`账户凭据整体大小不能超过 ${accountCredentialsJsonMaxBytes} 字节`)
  }
}

function assertKnownInputKeys(input: Record<string, unknown>, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含不支持的字段：${unknownKeys.join(', ')}`)
  }
}

function requiredTextInput(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}
