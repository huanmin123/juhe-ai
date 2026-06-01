import type { AccountSummary } from '@/types/domain'
import {
  writeAccountErrorPolicyToCredentials
} from './accountErrorPolicyPayload'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import {
  writeAccountStreamInterceptRulesToCredentials
} from './accountStreamInterceptPolicyPayload'
import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'
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
  streamInterceptRules: AccountStreamInterceptRuleForm[]
  form: AccountFormModel
}): Record<string, unknown> {
  const credentials: Record<string, unknown> = input.form.type === 'api_key'
    ? buildApiKeyCredentials(input.form)
    : buildOAuthCredentials(input.form, input.currentCredentials ?? {})
  writeAccountErrorPolicyToCredentials(credentials, input.errorPolicyRules)
  writeAccountStreamInterceptRulesToCredentials(credentials, input.streamInterceptRules)
  return credentials
}

export function currentAccountCredentials(accounts: AccountSummary[], editingId?: string): Record<string, unknown> {
  if (!editingId) return {}
  return accounts.find((account) => account.id === editingId)?.credentials ?? {}
}

function buildApiKeyCredentials(form: AccountFormModel): Record<string, unknown> {
  return compactAccountCredentials({
    api_key: form.apiKey,
    base_url: form.baseUrl
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
