import type { AccountSummary } from '@/types/domain'
import {
  writeAccountErrorPolicyToCredentials
} from './accountErrorPolicyPayload'
import {
  writeAccountResponseInspectionRulesToCredentials
} from './accountResponseInspectionPolicyPayload'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { asString } from './accountBasicFormatters'
import { accountApiKeysForForm } from './accountEditFormPayload'
import type { AccountFormModel } from './accountFormTypes'
import { loadAccountQuotaRecoveryPolicy } from './accountQuotaRecoveryPolicyTypes'
import { compactAccountCredentials } from './accountFormDefaults'
import { writeAccountGptRequestOverrides } from './accountGptRequestOverrides'

/**
 * BUG-0238 凭据占位符（前后端契约常量）：编辑明细接口把敏感凭据键替换为该占位内容，
 * 保存 payload 中占位值一律视为"未修改"，不得提交（后端另有同规则兜底）。
 */
export const CREDENTIAL_CIPHER_PLACEHOLDER = '__ENCRYPTED__'

export function isCredentialCipherPlaceholder(value: unknown): boolean {
  return typeof value === 'string' && value.trim() === CREDENTIAL_CIPHER_PLACEHOLDER
}

/** 敏感单键的 payload 值：占位符转为空串（交由 compact 省略），其余原样。 */
function credentialPayloadText(value: string): string {
  return isCredentialCipherPlaceholder(value) ? '' : value
}

const oauthCredentialMetadataKeys = [
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
  'entitlement_status',
  'base_url',
  'supported_endpoint_modes'
] as const

export function buildAccountCredentials(input: {
  currentCredentials?: Record<string, unknown>
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  form: AccountFormModel
}): Record<string, unknown> {
  const credentials: Record<string, unknown> = input.form.type === 'api_key'
    ? buildApiKeyCredentials(input.form)
    : input.form.type === 'google_oauth'
      ? buildGoogleOAuthCredentials(input.form)
      : buildOAuthCredentials(input.form, input.currentCredentials ?? {})
  writeAccountGptRequestOverrides(credentials, input.form)
  writeAccountErrorPolicyToCredentials(credentials, input.errorPolicyRules)
  if (input.form.errorHandlingRuleOverrides?.length) {
    credentials.error_handling_rule_overrides = input.form.errorHandlingRuleOverrides
  }
  writeAccountResponseInspectionRulesToCredentials(credentials, input.responseInspectionRules)
  if (input.form.quotaRecoveryPolicy && Object.keys(input.form.quotaRecoveryPolicy).length) {
    credentials.quota_recovery_policy = loadAccountQuotaRecoveryPolicy(input.form.quotaRecoveryPolicy)
  }
  return credentials
}

export function currentAccountCredentials(accounts: AccountSummary[], editingId?: string): Record<string, unknown> {
  if (!editingId) return {}
  return accounts.find((account) => account.id === editingId)?.credentials ?? {}
}

function buildApiKeyCredentials(form: AccountFormModel): Record<string, unknown> {
  const apiKeys = normalizedAccountApiKeys(form)
  // BUG-0238：占位行不代表用户输入，保存前按行剔除；权重随行同步过滤。
  const weights = normalizedAccountApiKeyWeights(form, apiKeys.length)
  const keptRows = apiKeys
    .map((key, index) => ({ key, weight: weights[index] }))
    .filter((row) => !isCredentialCipherPlaceholder(row.key))
  const keptKeys = keptRows.map((row) => row.key)
  const apiKey = keptKeys[0] ?? ''
  const credentials = compactAccountCredentials({
    api_key: apiKey,
    base_url: form.baseUrl,
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
  if (keptKeys.length > 1) {
    credentials.api_keys = keptKeys
    credentials.api_key_strategy = form.apiKeyStrategy
    if (credentials.api_key_strategy === 'weighted_round_robin') {
      credentials.api_key_weights = keptRows.map((row) => row.weight)
    }
  }
  return credentials
}

export function normalizedAccountApiKeys(form: AccountFormModel): string[] {
  const values = form.apiKeys?.length ? form.apiKeys : [form.apiKey]
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    const key = value.trim()
    if (!key) continue
    // BUG-0238：占位行各占一行（保持池行数/权重列/运行状态语义），不参与去重；
    // 真实键值仍按既有规则去重。
    if (isCredentialCipherPlaceholder(key)) {
      output.push(key)
      continue
    }
    if (seen.has(key)) continue
    seen.add(key)
    output.push(key)
  }
  return output
}

type AccountCredentialBaseline = Pick<AccountSummary, 'type' | 'credentials'>

export function accountFormApiKeysChanged(form: AccountFormModel, account?: AccountCredentialBaseline): boolean {
  if (form.type !== 'api_key') return false
  const nextKeys = normalizedAccountApiKeys(form)
  if (!nextKeys.length) return false
  const currentKeys = normalizedCredentialApiKeys(account?.credentials)
  if (!currentKeys.length) return true
  return stableStringListKey(nextKeys) !== stableStringListKey(currentKeys)
}

export function accountFormApiKeyRuntimeChanged(form: AccountFormModel, account?: AccountCredentialBaseline): boolean {
  return accountFormApiKeysChanged(form, account) || accountFormBaseUrlChanged(form, account)
}

export function normalizedAccountApiKeyWeights(form: AccountFormModel, count = normalizedAccountApiKeys(form).length): number[] {
  return Array.from({ length: count }, (_, index) => {
    const value = Number(form.apiKeyWeights?.[index] ?? 1)
    return Number.isInteger(value) ? Math.min(100, Math.max(1, value)) : 1
  })
}

function normalizedCredentialApiKeys(credentials: Record<string, unknown> | undefined): string[] {
  const values = Array.isArray(credentials?.api_keys) && credentials.api_keys.length
    ? credentials.api_keys
    : [credentials?.api_key]
  const output: string[] = []
  const seen = new Set<string>()
  for (const value of values) {
    if (typeof value !== 'string') continue
    const key = value.trim()
    if (!key || seen.has(key)) continue
    seen.add(key)
    output.push(key)
  }
  return output
}

function accountFormBaseUrlChanged(form: AccountFormModel, account?: AccountCredentialBaseline): boolean {
  if (form.type !== 'api_key') return false
  return normalizeCredentialText(form.baseUrl) !== normalizeCredentialText(account?.credentials?.base_url)
}

function normalizeCredentialText(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function stableStringListKey(value: string[]): string {
  return value.join('\n')
}

function buildOAuthCredentials(form: AccountFormModel, currentCredentials: Record<string, unknown>): Record<string, unknown> {
  return compactAccountCredentials({
    ...pickOAuthCredentialMetadata(currentCredentials),
    base_url: form.baseUrl || normalizeCredentialText(currentCredentials.base_url),
    access_token: credentialPayloadText(form.accessToken),
    refresh_token: credentialPayloadText(form.refreshToken),
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
}

function buildGoogleOAuthCredentials(form: AccountFormModel): Record<string, unknown> {
  return compactAccountCredentials({
    access_token: credentialPayloadText(form.accessToken),
    refresh_token: credentialPayloadText(form.refreshToken),
    client_id: form.googleClientId,
    client_secret: credentialPayloadText(form.googleClientSecret),
    quota_project_id: form.googleQuotaProjectId,
    oauth_type: form.oauthType,
    tier_id: form.tierId,
    project_id: form.projectId,
    base_url: form.baseUrl,
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
}

/**
 * BUG-0238：把 reveal 接口取回的明文凭据写回编辑表单（按账户类型存在的敏感字段全量替换）。
 * api_keys 池以服务端真实池为准重建行，维持行数与真实值一致。
 */
export function applyRevealedAccountCredentials(form: AccountFormModel, credentials: Record<string, unknown>): void {
  if (form.type === 'api_key') {
    const apiKeys = accountApiKeysForForm(credentials)
    form.apiKey = apiKeys[0] ?? ''
    form.apiKeys = apiKeys
    return
  }
  form.accessToken = asString(credentials.access_token)
  form.refreshToken = asString(credentials.refresh_token)
  if (form.type === 'google_oauth') {
    form.googleClientSecret = asString(credentials.client_secret)
  }
}

function pickOAuthCredentialMetadata(currentCredentials: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const key of oauthCredentialMetadataKeys) {
    if (Object.prototype.hasOwnProperty.call(currentCredentials, key)) {
      output[key] = currentCredentials[key]
    }
  }
  return output
}
