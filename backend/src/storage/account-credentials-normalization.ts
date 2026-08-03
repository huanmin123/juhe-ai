import { normalizeAccountErrorHandlingRules } from '../modules/accounts/account-error-policy-validation.js'
import { normalizeAccountResponseInspectionRules } from '../modules/accounts/account-response-inspection-policy-validation.js'
import { providerAccountCredentialDriverForContext } from '../modules/providers/drivers/account-credentials.registry.js'
import type { ProviderAccountCredentialContext } from '../modules/providers/drivers/_shared/account-credentials.js'
import { GPT_VENDOR_CODE, isAnthropicProtocolProfile } from '../domain/provider-protocol.js'
import { assertSafeUpstreamBaseUrl } from '../shared/upstream-url-policy.js'
import { optionalServerDateTimeIso } from './value-utils.js'

type AccountEndpointModeDefaultContext = ProviderAccountCredentialContext

const apiKeyAccountCredentialKeys = new Set([
  'api_key',
  'api_keys',
  'api_key_strategy',
  'api_key_weights',
  'base_url',
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'error_handling_rules',
  'response_inspection_rules'
])

const oauthAccountCredentialKeys = new Set([
  'access_token',
  'refresh_token',
  'expires_at',
  'client_id',
  'id_token',
  'token_type',
  'scope',
  'email',
  'account_id',
  'chatgpt_account_id',
  'organization_id',
  'chatgpt_user_id',
  'plan_type',
  'sub',
  'team_id',
  'subscription_tier',
  'entitlement_status',
  'base_url',
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'error_handling_rules',
  'response_inspection_rules'
])

const googleOAuthAccountCredentialKeys = new Set([
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
  'drive_tier_updated_at',
  'base_url',
  'supported_endpoint_modes',
  'service_tier_override',
  'reasoning_effort_override',
  'error_handling_rules',
  'response_inspection_rules'
])

const accountCredentialBaseUrlMaxBytes = 2048
const accountCredentialSecretMaxBytes = 16 * 1024
const accountCredentialMetadataMaxBytes = 4096
const accountCredentialsJsonMaxBytes = 32 * 1024
const accountApiKeyListMaxItems = 10
const deprecatedAccountCredentialKeys = new Set([
  'codex_responses_safe_repair_enabled',
  'codex_responses_strict_intercept_enabled'
])

export function normalizeAccountCredentialsForWrite(
  accountType: string,
  value: unknown,
  endpointModeDefaults: AccountEndpointModeDefaultContext = { accountType }
): Record<string, unknown> {
  const input = stripDeprecatedAccountCredentialKeys(accountCredentialsRecord(value))
  assertKnownInputKeys(input, accountCredentialAllowedKeys(accountType), '账户凭据')
  if (accountType === 'api_key') {
    return normalizeApiKeyAccountCredentials(input, endpointModeDefaults)
  }
  if (accountType === 'oauth') {
    return normalizeOAuthAccountCredentials(input, endpointModeDefaults)
  }
  if (accountType === 'google_oauth') {
    return normalizeGoogleOAuthAccountCredentials(input, endpointModeDefaults)
  }
  throw new Error(`账户类型 ${accountType} 不支持凭据写入`)
}

export function requiredAccountCredentialSource(accountType: string, credentials: Record<string, unknown>): string {
  if (accountType === 'oauth') {
    return requiredTextInput(credentials.refresh_token ?? credentials.access_token, 'OAuth 凭据')
  }
  if (accountType === 'api_key') {
    return requiredTextInput(accountApiKeys(credentials)[0], 'API Key')
  }
  if (accountType === 'google_oauth') {
    return requiredTextInput(credentials.refresh_token ?? credentials.access_token, 'Google OAuth 凭据')
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

function stripDeprecatedAccountCredentialKeys(input: Record<string, unknown>): Record<string, unknown> {
  if (!Object.keys(input).some((key) => deprecatedAccountCredentialKeys.has(key))) return input
  const sanitized = { ...input }
  for (const key of deprecatedAccountCredentialKeys) delete sanitized[key]
  return sanitized
}

function accountCredentialAllowedKeys(accountType: string): ReadonlySet<string> {
  if (accountType === 'api_key') return apiKeyAccountCredentialKeys
  if (accountType === 'oauth') return oauthAccountCredentialKeys
  if (accountType === 'google_oauth') return googleOAuthAccountCredentialKeys
  throw new Error(`账户类型 ${accountType} 不支持凭据写入`)
}

function accountApiKeys(credentials: Record<string, unknown>): string[] {
  if (Array.isArray(credentials.api_keys)) {
    return credentials.api_keys.filter((value): value is string => typeof value === 'string' && value.trim().length > 0)
  }
  return typeof credentials.api_key === 'string' ? [credentials.api_key] : []
}

function normalizeApiKeyAccountCredentials(
  input: Record<string, unknown>,
  endpointModeDefaults: AccountEndpointModeDefaultContext
): Record<string, unknown> {
  const baseUrl = requiredCredentialTextInput(input.base_url, 'Base URL', accountCredentialBaseUrlMaxBytes)
  assertSafeUpstreamBaseUrl(baseUrl)
  const apiKeys = normalizeApiKeyCredentialList(input)
  const credentials: Record<string, unknown> = {
    api_key: apiKeys[0],
    base_url: baseUrl,
    supported_endpoint_modes: normalizeApiKeyEndpointModesForWrite(input.supported_endpoint_modes, endpointModeDefaults)
  }
  if (apiKeys.length > 1) {
    credentials.api_keys = apiKeys
    credentials.api_key_strategy = normalizeApiKeyStrategy(input.api_key_strategy)
    if (credentials.api_key_strategy === 'weighted_round_robin') {
      credentials.api_key_weights = normalizeApiKeyWeights(input.api_key_weights, apiKeys.length)
    }
  }
  normalizeAccountCredentialPolicies(input, credentials)
  normalizeGptAccountRequestOverrides(input, credentials, endpointModeDefaults)
  assertAccountCredentialsJsonSize(credentials)
  return credentials
}

function normalizeApiKeyEndpointModesForWrite(value: unknown, endpointModeDefaults: AccountEndpointModeDefaultContext): string[] {
  return normalizeEndpointModesForWrite(value, {
    ...endpointModeDefaults,
    accountType: 'api_key'
  })
}

function normalizeApiKeyCredentialList(input: Record<string, unknown>): string[] {
  const sourceValues = Array.isArray(input.api_keys) && input.api_keys.length
    ? input.api_keys
    : [input.api_key]
  if (sourceValues.length > accountApiKeyListMaxItems) {
    throw new Error(`单个账户最多配置 ${accountApiKeyListMaxItems} 个 API Key`)
  }
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of sourceValues) {
    const key = requiredCredentialTextInput(value, 'API Key', accountCredentialSecretMaxBytes)
    if (seen.has(key)) continue
    seen.add(key)
    output.push(key)
  }
  if (!output.length) {
    throw new Error('API Key不能为空')
  }
  return output
}

function normalizeApiKeyStrategy(value: unknown): 'round_robin' | 'weighted_round_robin' | 'failover' {
  if (value === 'failover') return 'failover'
  return value === 'weighted_round_robin' ? 'weighted_round_robin' : value === 'round_robin' ? 'round_robin' : 'failover'
}

function normalizeApiKeyWeights(value: unknown, count: number): number[] {
  const input = Array.isArray(value) ? value : []
  return Array.from({ length: count }, (_, index) => normalizeApiKeyWeight(input[index]))
}

function normalizeApiKeyWeight(value: unknown): number {
  if (value === undefined) return 1
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 1 || value > 100) {
    throw new Error('API Key 权重必须是 1-100 之间的整数')
  }
  return value
}

function normalizeOAuthAccountCredentials(
  input: Record<string, unknown>,
  endpointModeDefaults: AccountEndpointModeDefaultContext
): Record<string, unknown> {
  const accessToken = optionalCredentialText(input.access_token, 'Access Token', accountCredentialSecretMaxBytes)
  const refreshToken = optionalCredentialText(input.refresh_token, 'Refresh Token', accountCredentialSecretMaxBytes)
  if (isAnthropicProtocolProfile(endpointModeDefaults) && !accessToken) {
    throw new Error('Anthropic OAuth Access Token 不能为空')
  }
  if (!isAnthropicProtocolProfile(endpointModeDefaults) && !refreshToken && !accessToken) {
    throw new Error('OAuth 凭据不能为空')
  }

  const credentials: Record<string, unknown> = {
    base_url: requiredCredentialTextInput(input.base_url, 'Base URL', accountCredentialBaseUrlMaxBytes),
    supported_endpoint_modes: normalizeEndpointModesForWrite(input.supported_endpoint_modes, {
      ...endpointModeDefaults,
      accountType: 'oauth'
    })
  }
  assertSafeUpstreamBaseUrl(String(credentials.base_url))
  if (accessToken) credentials.access_token = accessToken
  if (refreshToken) credentials.refresh_token = refreshToken
  const expiresAt = optionalCredentialDateTime(input.expires_at, 'Access Token 到期时间')
  if (expiresAt) credentials.expires_at = expiresAt
  copyOptionalCredentialText(input, credentials, 'client_id', 'OAuth client_id', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'id_token', 'OAuth id_token', accountCredentialSecretMaxBytes)
  copyOptionalCredentialText(input, credentials, 'token_type', 'OAuth token_type', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'scope', 'OAuth scope', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'email', 'OAuth email', accountCredentialMetadataMaxBytes)
  const openAIAccountId = optionalCredentialText(input.account_id, 'OpenAI account_id', accountCredentialMetadataMaxBytes)
    || optionalCredentialText(input.chatgpt_account_id, 'OpenAI chatgpt_account_id', accountCredentialMetadataMaxBytes)
  if (endpointModeDefaults.providerCode === GPT_VENDOR_CODE && accessToken && !openAIAccountId) {
    throw new Error('OpenAI OAuth Access Token 缺少 account_id')
  }
  if (openAIAccountId) credentials.account_id = openAIAccountId
  copyOptionalCredentialText(input, credentials, 'organization_id', 'Anthropic organization_id', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'chatgpt_user_id', 'OpenAI chatgpt_user_id', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'plan_type', 'OpenAI plan_type', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'sub', 'xAI subject', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'team_id', 'xAI team_id', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'subscription_tier', 'xAI subscription_tier', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'entitlement_status', 'xAI entitlement_status', accountCredentialMetadataMaxBytes)
  normalizeAccountCredentialPolicies(input, credentials)
  normalizeGptAccountRequestOverrides(input, credentials, endpointModeDefaults)
  assertAccountCredentialsJsonSize(credentials)
  return credentials
}

function normalizeGoogleOAuthAccountCredentials(
  input: Record<string, unknown>,
  endpointModeDefaults: AccountEndpointModeDefaultContext
): Record<string, unknown> {
  const accessToken = optionalCredentialText(input.access_token, 'Google Access Token', accountCredentialSecretMaxBytes)
  const refreshToken = optionalCredentialText(input.refresh_token, 'Google Refresh Token', accountCredentialSecretMaxBytes)
  if (!accessToken && !refreshToken) {
    throw new Error('Google OAuth 凭据不能为空')
  }
  const clientId = optionalCredentialText(input.client_id, 'Google OAuth Client ID', accountCredentialMetadataMaxBytes)
  const clientSecret = optionalCredentialText(input.client_secret, 'Google OAuth Client Secret', accountCredentialSecretMaxBytes)
  if (refreshToken && (!clientId || !clientSecret)) {
    throw new Error('Google Refresh Token 需要同时配置 Client ID 和 Client Secret')
  }
  const credentials: Record<string, unknown> = {
    base_url: requiredCredentialTextInput(input.base_url, 'Base URL', accountCredentialBaseUrlMaxBytes),
    supported_endpoint_modes: normalizeEndpointModesForWrite(input.supported_endpoint_modes, {
      ...endpointModeDefaults,
      accountType: 'google_oauth'
    })
  }
  assertSafeUpstreamBaseUrl(String(credentials.base_url))
  if (accessToken) credentials.access_token = accessToken
  if (refreshToken) credentials.refresh_token = refreshToken
  if (clientId) credentials.client_id = clientId
  if (clientSecret) credentials.client_secret = clientSecret
  const expiresAt = optionalCredentialDateTime(input.expires_at, 'Google Access Token 到期时间')
  if (expiresAt) credentials.expires_at = expiresAt
  copyOptionalCredentialText(input, credentials, 'quota_project_id', 'Google Quota Project ID', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'oauth_type', 'Gemini OAuth 类型', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'project_id', 'Gemini Project ID', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'tier_id', 'Gemini Tier ID', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'scope', 'Google OAuth scope', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialText(input, credentials, 'token_type', 'Google OAuth token_type', accountCredentialMetadataMaxBytes)
  copyOptionalCredentialNonNegativeInteger(input, credentials, 'drive_storage_limit', 'Google Drive 存储上限')
  copyOptionalCredentialNonNegativeInteger(input, credentials, 'drive_storage_usage', 'Google Drive 已用存储')
  copyOptionalCredentialText(input, credentials, 'drive_tier_updated_at', 'Google Drive tier 更新时间', accountCredentialMetadataMaxBytes)
  normalizeAccountCredentialPolicies(input, credentials)
  normalizeGptAccountRequestOverrides(input, credentials, endpointModeDefaults)
  assertAccountCredentialsJsonSize(credentials)
  return credentials
}

function normalizeEndpointModesForWrite(value: unknown, context: AccountEndpointModeDefaultContext): string[] {
  const driver = providerAccountCredentialDriverForContext(context)
  if (!driver) {
    throw new Error(`供应商协议档案未注册接口能力归一化：${context.providerCode ?? 'unknown'}`)
  }
  return driver.normalizeEndpointModesForWrite(value, context)
}

function normalizeAccountCredentialPolicies(input: Record<string, unknown>, credentials: Record<string, unknown>): void {
  if (Object.prototype.hasOwnProperty.call(input, 'error_handling_rules')) {
    credentials.error_handling_rules = normalizeAccountErrorHandlingRules(input.error_handling_rules)
  }
  if (Object.prototype.hasOwnProperty.call(input, 'response_inspection_rules')) {
    credentials.response_inspection_rules = normalizeAccountResponseInspectionRules(input.response_inspection_rules)
  }
}

function normalizeGptAccountRequestOverrides(
  input: Record<string, unknown>,
  credentials: Record<string, unknown>,
  context: AccountEndpointModeDefaultContext
): void {
  const hasServiceTier = Object.prototype.hasOwnProperty.call(input, 'service_tier_override')
  const hasReasoningEffort = Object.prototype.hasOwnProperty.call(input, 'reasoning_effort_override')
  if (!hasServiceTier && !hasReasoningEffort) return
  if (context.providerCode === 'gemini'
    && Array.isArray(credentials.supported_endpoint_modes)
    && !credentials.supported_endpoint_modes.some((mode) => mode === 'generate_content_json' || mode === 'generate_content_sse')) {
    return
  }
  const serviceTier = optionalCredentialToken(
    input.service_tier_override,
    '服务等级覆盖'
  )
  const reasoningEffort = optionalCredentialToken(
    input.reasoning_effort_override,
    '思考级别覆盖'
  )
  if (context.providerCode === 'gpt') {
    if (serviceTier && !new Set(['default', 'priority', 'flex']).has(serviceTier)) {
      throw new Error('服务等级覆盖无效')
    }
    if (reasoningEffort && !new Set(['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']).has(reasoningEffort)) {
      throw new Error('思考级别覆盖无效')
    }
  }
  if (serviceTier) credentials.service_tier_override = serviceTier
  if (reasoningEffort) credentials.reasoning_effort_override = reasoningEffort
}

function optionalCredentialToken(
  value: unknown,
  label: string
): string | undefined {
  if (value === undefined || value === null || value === '') return undefined
  if (typeof value !== 'string' || value !== value.trim() || !/^[a-z0-9][a-z0-9._-]{0,63}$/i.test(value)) {
    throw new Error(`${label}无效`)
  }
  return value
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

function copyOptionalCredentialNonNegativeInteger(
  input: Record<string, unknown>,
  output: Record<string, unknown>,
  key: string,
  label: string
): void {
  const value = input[key]
  if (value === undefined || value === null || value === '') return
  if (typeof value === 'number' && Number.isSafeInteger(value) && value >= 0) {
    output[key] = value
    return
  }
  if (typeof value === 'string' && /^\d+$/u.test(value.trim())) {
    output[key] = value.trim()
    return
  }
  throw new Error(`${label}必须是非负整数`)
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
