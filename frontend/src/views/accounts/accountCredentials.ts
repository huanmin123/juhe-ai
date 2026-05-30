import type { AccountSummary } from '@/types/domain'
import {
  writeAccountErrorPolicyToCredentials,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicy'
import {
  writeAccountStreamInterceptRulesToCredentials
} from './accountStreamInterceptPolicyPayload'
import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import { compactAccountCredentials } from './accountFormDefaults'

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
    ...currentCredentials,
    access_token: form.accessToken,
    refresh_token: form.refreshToken,
    expires_at: currentCredentials.expires_at,
    base_url: currentCredentials.base_url ?? 'https://api.openai.com/v1'
  })
}
