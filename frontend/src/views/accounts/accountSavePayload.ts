import type { AccountSummary } from '@/types/domain'
import { formatServerDateTimeInput } from './accountFormatters'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicy'
import { validateAccountErrorPolicyRules } from './accountErrorPolicy'
import { buildAccountCredentials, currentAccountCredentials } from './accountCredentials'
import type { AccountFormModel } from './accountFormTypes'

export type AccountSavePayload = {
  providerCode: AccountFormModel['providerCode']
  name?: string
  type: AccountFormModel['type']
  credentials: Record<string, unknown>
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  proxyProfileId?: string | null
  accountExpiresAt: string | null
  groupId?: string
  notes: string
}

export type AccountOAuthCreateCommonPayload = {
  name?: string
  groupId?: string
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  proxyProfileId?: string
  accountExpiresAt: string | null
  credentialsPatch: { error_handling_rules?: unknown }
  notes?: string
}

export function validateAccountSaveForm(input: {
  editingId?: string
  form: AccountFormModel
  hasAuthSession: boolean
  errorPolicyRules: AccountErrorPolicyRuleForm[]
}): string | undefined {
  const { editingId, form } = input
  if (!form.providerCode) return '请先选择供应商'
  if (!form.type) return '请先选择账户类型'
  if ((editingId || form.type === 'api_key') && !form.name.trim()) return '请填写账户名称'
  if (!form.groupId) return '请选择加入分组'
  if (form.type === 'api_key' && !form.apiKey.trim()) return '请填写 API Key'
  if (form.type === 'api_key' && !form.baseUrl.trim()) return '请填写 Base URL'
  if (editingId && form.type === 'oauth' && !form.accessToken.trim() && !form.refreshToken.trim()) return '请至少填写 Access Token 或 Refresh Token'
  if (!editingId && form.type === 'oauth' && form.providerCode !== 'openai') return '当前只支持创建 OpenAI OAuth 账户'
  if (!editingId && form.type === 'oauth' && form.oauthMode === 'manual' && !input.hasAuthSession) return '请先生成授权链接'
  if (!editingId && form.type === 'oauth' && form.oauthMode === 'manual' && !form.callbackUrl.trim()) return '请粘贴回调 URL'
  if (!editingId && form.type === 'oauth' && form.oauthMode === 'refresh_token' && !form.refreshToken.trim()) return '请填写 Refresh Token'
  const errorPolicyValidation = validateAccountErrorPolicyRules(input.errorPolicyRules)
  return errorPolicyValidation.valid ? undefined : errorPolicyValidation.message || '错误处理策略配置不完整'
}

export function buildAccountSavePayload(input: {
  accounts: AccountSummary[]
  accountDetail?: AccountSummary
  editingId?: string
  form: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
}): AccountSavePayload {
  return {
    providerCode: input.form.providerCode,
    name: input.form.name.trim() || undefined,
    type: input.form.type,
    credentials: accountCredentials(input),
    concurrencyLimit: input.form.concurrencyLimit,
    priority: input.form.priority,
    supportedModels: [...(input.form.supportedModels ?? [])],
    proxyProfileId: saveProxyProfileId(input.form.proxyProfileId, Boolean(input.editingId)),
    accountExpiresAt: formatServerDateTimeInput(input.form.accountExpiresAt),
    groupId: input.form.groupId,
    notes: input.form.notes
  }
}

export function buildOAuthCreateCommonPayload(input: {
  accounts: AccountSummary[]
  editingId?: string
  form: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
}): AccountOAuthCreateCommonPayload {
  return {
    name: input.form.name.trim() || undefined,
    groupId: input.form.groupId,
    concurrencyLimit: input.form.concurrencyLimit,
    priority: input.form.priority,
    supportedModels: [...(input.form.supportedModels ?? [])],
    proxyProfileId: input.form.proxyProfileId,
    accountExpiresAt: formatServerDateTimeInput(input.form.accountExpiresAt),
    credentialsPatch: { error_handling_rules: accountCredentials(input).error_handling_rules },
    notes: input.form.notes || undefined
  }
}

function saveProxyProfileId(proxyProfileId: string | undefined, editing: boolean): string | null | undefined {
  if (proxyProfileId) return proxyProfileId
  return editing ? null : undefined
}

function accountCredentials(input: {
  accounts: AccountSummary[]
  accountDetail?: AccountSummary
  editingId?: string
  form: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
}): Record<string, unknown> {
  return buildAccountCredentials({
    currentCredentials: input.accountDetail?.credentials ?? currentAccountCredentials(input.accounts, input.editingId),
    errorPolicyRules: input.errorPolicyRules,
    form: input.form
  })
}
