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

const oauthCredentialMetadataKeys = [
  'expires_at',
  'client_id',
  'email',
  'account_id',
  'chatgpt_user_id',
  'plan_type',
  'base_url'
] as const

export function buildAccountCredentials(input: {
  currentCredentials?: Record<string, unknown>
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  form: AccountFormModel
}): Record<string, unknown> {
  const credentials: Record<string, unknown> = input.form.type === 'api_key'
    ? buildApiKeyCredentials(input.form)
    : buildOAuthCredentials(input.form, input.currentCredentials ?? {})
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
    base_url: form.baseUrl
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

export function normalizedAccountApiKeyWeights(form: AccountFormModel, count = normalizedAccountApiKeys(form).length): number[] {
  return Array.from({ length: count }, (_, index) => {
    const value = Number(form.apiKeyWeights?.[index] ?? 1)
    return Number.isInteger(value) ? Math.min(100, Math.max(1, value)) : 1
  })
}

function buildOAuthCredentials(form: AccountFormModel, currentCredentials: Record<string, unknown>): Record<string, unknown> {
  return compactAccountCredentials({
    ...pickOAuthCredentialMetadata(currentCredentials),
    access_token: form.accessToken,
    refresh_token: form.refreshToken
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
