import type { AccountSummary } from '@/types/domain'
import {
  writeAccountErrorPolicyToCredentials
} from './accountErrorPolicyPayload'
import {
  writeAccountResponseInspectionRulesToCredentials
} from './accountResponseInspectionPolicyPayload'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import { compactAccountCredentials } from './accountFormDefaults'
import { writeAccountGptRequestOverrides } from './accountGptRequestOverrides'

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
  writeAccountResponseInspectionRulesToCredentials(credentials, input.responseInspectionRules)
  return credentials
}

export function currentAccountCredentials(accounts: AccountSummary[], editingId?: string): Record<string, unknown> {
  if (!editingId) return {}
  return accounts.find((account) => account.id === editingId)?.credentials ?? {}
}

function buildApiKeyCredentials(form: AccountFormModel): Record<string, unknown> {
  const apiKeys = normalizedAccountApiKeys(form)
  const apiKey = apiKeys[0] ?? form.apiKey
  const credentials = compactAccountCredentials({
    api_key: apiKey,
    base_url: form.baseUrl,
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
  if (apiKeys.length > 1) {
    credentials.api_keys = apiKeys
    credentials.api_key_strategy = form.apiKeyStrategy === 'weighted_round_robin'
      ? 'weighted_round_robin'
      : 'round_robin'
    if (credentials.api_key_strategy === 'weighted_round_robin') {
      credentials.api_key_weights = normalizedAccountApiKeyWeights(form, apiKeys.length)
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
    if (!key || seen.has(key)) continue
    seen.add(key)
    output.push(key)
  }
  return output
}

export function accountFormApiKeysChanged(form: AccountFormModel, account?: AccountSummary): boolean {
  if (form.type !== 'api_key') return false
  const nextKeys = normalizedAccountApiKeys(form)
  if (!nextKeys.length) return false
  const currentKeys = normalizedCredentialApiKeys(account?.credentials)
  if (!currentKeys.length) return true
  return stableStringListKey(nextKeys) !== stableStringListKey(currentKeys)
}

export function accountFormApiKeyRuntimeChanged(form: AccountFormModel, account?: AccountSummary): boolean {
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

function accountFormBaseUrlChanged(form: AccountFormModel, account?: AccountSummary): boolean {
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
    access_token: form.accessToken,
    refresh_token: form.refreshToken,
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
}

function buildGoogleOAuthCredentials(form: AccountFormModel): Record<string, unknown> {
  return compactAccountCredentials({
    access_token: form.accessToken,
    refresh_token: form.refreshToken,
    client_id: form.googleClientId,
    client_secret: form.googleClientSecret,
    quota_project_id: form.googleQuotaProjectId,
    oauth_type: form.oauthType,
    tier_id: form.tierId,
    project_id: form.projectId,
    base_url: form.baseUrl,
    supported_endpoint_modes: [...form.supportedEndpointModes]
  })
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
