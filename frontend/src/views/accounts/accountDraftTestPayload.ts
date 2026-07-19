import type { AccountDraftTestAccountPayload } from '@/api/client'
import { groupLabelForId } from '@/shared/groupLabelCache'
import type { AccountSummary, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import { buildAccountSavePayload, validateAccountSaveForm } from './accountSavePayload'
import type { AccountModelSelectOption } from './accountEditFormPayload'

interface AccountDraftTestPayloadInput {
  accountDetail?: AccountSummary
  accounts: AccountSummary[]
  editingId?: string
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  form: AccountFormModel
  mappingAnthropicSourceModelOptions?: AccountModelSelectOption[]
  mappingGeminiSourceModelOptions?: AccountModelSelectOption[]
  mappingSourceModelOptions?: AccountModelSelectOption[]
  mappingUpstreamModelOptions?: AccountModelSelectOption[]
  providers?: ProviderDefinition[]
}

interface AccountDraftTestSummaryInput {
  accountDetail?: AccountSummary
  draftPayload: AccountDraftTestAccountPayload
  protocolProfile?: ProviderProtocolProfileDefinition
  scopeSystemAccountId?: string
}

export function validateAccountDraftTestForm(input: AccountDraftTestPayloadInput & { hasAuthSession: boolean }): string | undefined {
  if (!input.form.providerProtocolProfileId) return '当前供应商配置不完整，请刷新后重试'
  if (!input.editingId && input.form.type === 'oauth' && input.form.oauthMode === 'manual') {
    return '手动授权创建需先保存账户，完成换取 Token 后再测试'
  }
  const validationMessage = validateAccountSaveForm(input)
  if (validationMessage) return validationMessage
  if (input.form.type === 'oauth' && !hasOAuthTestCredential(input)) {
    return '请填写 Access Token 或 Refresh Token 后再测试'
  }
  return undefined
}

export function buildAccountDraftTestPayload(input: AccountDraftTestPayloadInput): AccountDraftTestAccountPayload {
  const payload = buildAccountSavePayload(input)
  const credentials = accountDraftTestCredentials(payload.credentials, input.accountDetail)
  return {
    providerCode: payload.providerCode,
    providerProtocolProfileId: payload.providerProtocolProfileId,
    name: payload.name ?? input.accountDetail?.name ?? input.form.name.trim(),
    type: payload.type,
    credentials,
    concurrencyLimit: payload.concurrencyLimit,
    priority: payload.priority,
    supportedModels: payload.supportedModels,
    healthCheckModel: payload.healthCheckModel,
    healthCheckEndpointMode: payload.healthCheckEndpointMode,
    modelMappings: payload.modelMappings,
    proxyProfileId: payload.proxyProfileId,
    groupId: payload.groupId ?? '',
    accountExpiresAt: payload.accountExpiresAt,
    availabilitySchedule: payload.availabilitySchedule as AccountDraftTestAccountPayload['availabilitySchedule'],
    notes: payload.notes
  }
}

export function buildAccountDraftTestSummary(input: AccountDraftTestSummaryInput): AccountSummary {
  const usage = emptyAccountUsageSummary()
  const ownerSystemAccountId = input.accountDetail?.ownerSystemAccountId ?? input.accountDetail?.systemAccountId ?? input.scopeSystemAccountId
  return {
    id: input.accountDetail?.id ?? `draft:${Date.now()}`,
    systemAccountId: ownerSystemAccountId,
    ownerSystemAccountId,
    providerCode: input.draftPayload.providerCode,
    providerProtocolProfileId: input.draftPayload.providerProtocolProfileId,
    protocolCode: input.accountDetail?.protocolCode ?? input.protocolProfile?.protocolCode,
    protocolVersion: input.accountDetail?.protocolVersion ?? input.protocolProfile?.protocolVersion,
    name: input.draftPayload.name || input.accountDetail?.name || '未命名账户',
    notes: input.draftPayload.notes,
    type: input.draftPayload.type,
    credentials: input.draftPayload.credentials,
    status: 'active',
    concurrencyLimit: input.draftPayload.concurrencyLimit,
    currentConcurrency: 0,
    priority: input.draftPayload.priority,
    superPriorityEnabled: input.accountDetail?.superPriorityEnabled ?? false,
    fallbackEnabled: input.accountDetail?.fallbackEnabled ?? false,
    clientCompatibility: input.accountDetail?.clientCompatibility ?? (input.draftPayload.type === 'oauth' ? 'codex_responses' : 'openai_standard'),
    supportedModels: input.draftPayload.supportedModels,
    healthCheckModel: input.draftPayload.healthCheckModel,
    healthCheckEndpointMode: input.draftPayload.healthCheckEndpointMode,
    modelMappings: input.draftPayload.modelMappings,
    proxyProfileId: input.draftPayload.proxyProfileId ?? undefined,
    schedulable: true,
    accountExpiresAt: input.draftPayload.accountExpiresAt ?? undefined,
    todayUsage: usage,
    usage,
    accessType: 'owner',
    boundGroupId: input.draftPayload.groupId,
    boundGroupName: input.accountDetail?.boundGroupName ?? groupLabelForId(input.draftPayload.groupId),
    groupBindStatus: 'bound',
    permissions: {
      canUse: true,
      canEdit: true,
      canDelete: true,
      canAuthorize: false,
      canViewCredentials: true,
      canManageAccounts: true,
      canBindToApiKey: true
    },
    effectiveAvailability: {
      available: true,
      status: 'available',
      label: '草稿测试',
      color: 'blue'
    }
  }
}

function accountDraftTestCredentials(credentials: Record<string, unknown>, accountDetail?: AccountSummary): Record<string, unknown> {
  if (!accountDetail) return credentials
  const output = { ...credentials }
  preserveCredentialText(output, accountDetail.credentials, 'base_url')
  if (accountDetail.type === 'api_key') {
    preserveCredentialText(output, accountDetail.credentials, 'api_key')
    return output
  }
  for (const key of accountDetail.type === 'google_oauth'
    ? ['access_token', 'refresh_token', 'expires_at', 'client_id', 'client_secret', 'quota_project_id']
    : [
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
    preserveCredentialText(output, accountDetail.credentials, key)
  }
  return output
}

function hasOAuthTestCredential(input: AccountDraftTestPayloadInput): boolean {
  if (input.form.accessToken.trim() || input.form.refreshToken.trim()) return true
  const credentials = input.accountDetail?.credentials
  return hasCredentialText(credentials?.access_token) || hasCredentialText(credentials?.refresh_token)
}

function preserveCredentialText(output: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  if (hasCredentialText(output[key])) return
  const value = source[key]
  if (hasCredentialText(value)) {
    output[key] = value
  }
}

function hasCredentialText(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function emptyAccountUsageSummary(): AccountSummary['usage'] {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}
