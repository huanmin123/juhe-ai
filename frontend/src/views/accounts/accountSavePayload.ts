import type { AccountSummary, ProviderDefinition } from '@/types/domain'
import { formatServerDateTimeInput } from './accountFormatters'
import { validateAccountErrorPolicyRules } from './accountErrorPolicyPayload'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import { validateAccountResponseInspectionRules } from './accountResponseInspectionPolicyPayload'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { buildAccountCredentials, currentAccountCredentials, normalizedAccountApiKeys } from './accountCredentials'
import type { AccountFormModel } from './accountFormTypes'
import {
  buildAccountAvailabilitySchedulePayload,
  validateAccountAvailabilityScheduleForm,
  type AccountAvailabilitySchedulePayload
} from './accountAvailabilitySchedule'
import { validateOpenAICompatibleBaseUrl } from './accountBaseUrlValidation'
import { validateAccountEndpointModes } from './accountEndpointModes'
import { canCreateOAuthAccount, endpointModesForProfile } from './accountProviderCapabilities'
import { FALLBACK_PROVIDERS } from './accountOptions'
import {
  accountModelMappingEndpointFamilyText,
  accountModelMappingProtocolValidationMessage
} from './accountModelMappingProtocolMatrix'

export const ACCOUNT_API_KEY_BATCH_CREATE_LIMIT = 50

export type AccountSavePayload = {
  providerCode: AccountFormModel['providerCode']
  providerProtocolProfileId: AccountFormModel['providerProtocolProfileId']
  name?: string
  type: AccountFormModel['type']
  credentials: Record<string, unknown>
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  modelMappings: AccountFormModel['modelMappings']
  tags: string[]
  proxyProfileId?: string | null
  accountExpiresAt: string | null
  availabilitySchedule?: AccountAvailabilitySchedulePayload | null
  groupId?: string
  notes: string
}

export type AccountUpdatePayload = Omit<AccountSavePayload, 'providerCode' | 'providerProtocolProfileId' | 'type'>

export type AccountOAuthCreateCommonPayload = {
  providerProtocolProfileId?: string
  name?: string
  groupId?: string
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  modelMappings: AccountFormModel['modelMappings']
  tags: string[]
  proxyProfileId?: string
  accountExpiresAt: string | null
  availabilitySchedule?: AccountAvailabilitySchedulePayload | null
  credentialsPatch?: {
    supported_endpoint_modes?: AccountFormModel['supportedEndpointModes']
    error_handling_rules?: unknown
    response_inspection_rules?: unknown
  }
  status?: 'active'
  activationTestTaskId?: string
  notes?: string
}

export function validateAccountSaveForm(input: {
  editingId?: string
  form: AccountFormModel
  hasAuthSession: boolean
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  providers?: ProviderDefinition[]
}): string | undefined {
  const { editingId, form } = input
  if (!form.providerCode) return '请先选择供应商'
  if (!form.providerProtocolProfileId) return '当前供应商配置不完整，请刷新后重试'
  if (!form.type) return '请先选择账户类型'
  if ((editingId || form.type === 'api_key') && !form.name.trim()) return '请填写账户名称'
  if (!form.groupId) return '请选择加入分组'
  const apiKeyCount = normalizedAccountApiKeys(form).length
  if (form.type === 'api_key' && apiKeyCount === 0) return '请填写 API Key'
  if (form.type === 'api_key' && apiKeyCount > ACCOUNT_API_KEY_BATCH_CREATE_LIMIT) return `单个账户最多配置 ${ACCOUNT_API_KEY_BATCH_CREATE_LIMIT} 个 API Key`
  if (form.type === 'api_key' && !form.baseUrl.trim()) return '请填写 Base URL'
  if (form.type === 'api_key') {
    const baseUrlValidation = validateOpenAICompatibleBaseUrl(form.baseUrl)
    if (baseUrlValidation) return baseUrlValidation
  }
  const formProviderProfile = resolveFormProviderProfile(form, input.providers)
  if (!editingId && form.type === 'oauth' && !canCreateOAuthAccount(formProviderProfile)) return '当前供应商协议不支持创建 OAuth 账户'
  if (!editingId && form.type === 'oauth' && form.oauthMode === 'manual' && !input.hasAuthSession) return '请先生成授权链接'
  if (!editingId && form.type === 'oauth' && form.oauthMode === 'manual' && !form.callbackUrl.trim()) return '请粘贴回调 URL'
  if (!editingId && form.type === 'oauth' && form.oauthMode === 'refresh_token' && !form.refreshToken.trim()) return '请填写 Refresh Token'
  const supportedModels = normalizeSupportedModels(form.supportedModels)
  if (!supportedModels.length) return '请选择支持模型'
  const tagValidation = validateAccountTags(form.tags)
  if (tagValidation) return tagValidation
  const scheduleValidation = validateAccountAvailabilityScheduleForm(form.availabilitySchedule)
  if (scheduleValidation) return scheduleValidation
  const endpointModeValidation = validateAccountEndpointModes({
    modes: form.supportedEndpointModes,
    type: form.type,
    profile: formProviderProfile.profile ?? formProviderProfile.provider,
    allowedModes: endpointModesForProfile(formProviderProfile.profile ?? formProviderProfile.provider)
  })
  if (endpointModeValidation) return endpointModeValidation
  const accountErrorPolicyValidation = validateAccountErrorPolicyRules(input.errorPolicyRules)
  if (!accountErrorPolicyValidation.valid) return accountErrorPolicyValidation.message || '账户错误处理策略配置不完整'
  const responseInspectionValidation = validateAccountResponseInspectionRules(input.responseInspectionRules)
  if (!responseInspectionValidation.valid) return responseInspectionValidation.message || '账户响应检查策略配置不完整'
  return validateAccountModelMappings(form.modelMappings, supportedModels, form.supportedEndpointModes, formProviderProfile.profile ?? formProviderProfile.provider)
}

function resolveFormProviderProfile(form: AccountFormModel, providers: ProviderDefinition[] = FALLBACK_PROVIDERS): {
  provider?: ProviderDefinition
  profile?: ProviderDefinition['protocolProfiles'][number]
} {
  const provider = providers.find((item) => item.code === form.providerCode)
  const profile = provider?.protocolProfiles.find((item) => item.id === form.providerProtocolProfileId)
    ?? provider?.protocolProfiles.find((item) => item.id === provider.defaultProtocolProfileId)
    ?? provider?.protocolProfiles.find((item) => item.enabled)
    ?? provider?.protocolProfiles[0]
  return { provider, profile }
}

export function buildAccountSavePayload(input: {
  accounts: AccountSummary[]
  accountDetail?: AccountSummary
  editingId?: string
  form: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
}): AccountSavePayload {
  return {
    providerCode: input.form.providerCode,
    providerProtocolProfileId: input.form.providerProtocolProfileId,
    name: input.form.name.trim() || undefined,
    type: input.form.type,
    credentials: accountCredentials(input),
    concurrencyLimit: input.form.concurrencyLimit,
    priority: input.form.priority,
    supportedModels: normalizeSupportedModels(input.form.supportedModels),
    modelMappings: normalizeAccountModelMappings(input.form.modelMappings),
    tags: normalizeAccountTags(input.form.tags),
    proxyProfileId: saveProxyProfileId(input.form.proxyProfileId, Boolean(input.editingId)),
    accountExpiresAt: formatServerDateTimeInput(input.form.accountExpiresAt),
    availabilitySchedule: buildAccountAvailabilitySchedulePayload(input.form.availabilitySchedule),
    groupId: input.form.groupId,
    notes: input.form.notes
  }
}

export function buildAccountUpdatePayload(payload: AccountSavePayload): AccountUpdatePayload {
  return {
    name: payload.name,
    credentials: payload.credentials,
    concurrencyLimit: payload.concurrencyLimit,
    priority: payload.priority,
    supportedModels: payload.supportedModels,
    modelMappings: payload.modelMappings,
    tags: payload.tags,
    proxyProfileId: payload.proxyProfileId,
    accountExpiresAt: payload.accountExpiresAt,
    availabilitySchedule: payload.availabilitySchedule,
    groupId: payload.groupId,
    notes: payload.notes
  }
}

export function buildOAuthCreateCommonPayload(input: {
  accounts: AccountSummary[]
  editingId?: string
  form: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
}): AccountOAuthCreateCommonPayload {
  const credentials = accountCredentials(input)
  const payload: AccountOAuthCreateCommonPayload = {
    name: input.form.name.trim() || undefined,
    providerProtocolProfileId: input.form.providerProtocolProfileId,
    groupId: input.form.groupId,
    concurrencyLimit: input.form.concurrencyLimit,
    priority: input.form.priority,
    supportedModels: normalizeSupportedModels(input.form.supportedModels),
    modelMappings: normalizeAccountModelMappings(input.form.modelMappings),
    tags: normalizeAccountTags(input.form.tags),
    proxyProfileId: input.form.proxyProfileId,
    accountExpiresAt: formatServerDateTimeInput(input.form.accountExpiresAt),
    availabilitySchedule: buildAccountAvailabilitySchedulePayload(input.form.availabilitySchedule),
    notes: input.form.notes || undefined
  }
  const credentialsPatch: NonNullable<AccountOAuthCreateCommonPayload['credentialsPatch']> = {}
  if (Array.isArray(credentials.supported_endpoint_modes)) {
    credentialsPatch.supported_endpoint_modes = [...credentials.supported_endpoint_modes] as AccountFormModel['supportedEndpointModes']
  }
  if (Object.prototype.hasOwnProperty.call(credentials, 'error_handling_rules')) {
    credentialsPatch.error_handling_rules = credentials.error_handling_rules
  }
  if (Object.prototype.hasOwnProperty.call(credentials, 'response_inspection_rules')) {
    credentialsPatch.response_inspection_rules = credentials.response_inspection_rules
  }
  if (Object.keys(credentialsPatch).length) {
    payload.credentialsPatch = credentialsPatch
  }
  return payload
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
  responseInspectionRules: AccountResponseInspectionRuleForm[]
}): Record<string, unknown> {
  return buildAccountCredentials({
    currentCredentials: input.accountDetail?.credentials ?? currentAccountCredentials(input.accounts, input.editingId),
    errorPolicyRules: input.errorPolicyRules,
    responseInspectionRules: input.responseInspectionRules,
    form: input.form
  })
}

function normalizeAccountTags(value: AccountFormModel['tags']): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of value ?? []) {
    const name = item.replace(/\s+/g, ' ').trim()
    if (!name) continue
    const key = name.toLocaleLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    output.push(name)
  }
  return output
}

function normalizeSupportedModels(value: AccountFormModel['supportedModels']): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of value ?? []) {
    const model = item.trim()
    const key = model.toLowerCase()
    if (!model || seen.has(key)) continue
    seen.add(key)
    output.push(model)
  }
  return output
}

function validateAccountTags(value: AccountFormModel['tags']): string | undefined {
  const normalized = normalizeAccountTags(value)
  if (normalized.length > 24) return '单个账户最多配置 24 个标签'
  if (normalized.some((item) => item.length > 40)) return '账户标签不能超过 40 个字符'
  return undefined
}

function normalizeAccountModelMappings(value: AccountFormModel['modelMappings']): AccountFormModel['modelMappings'] {
  const output: AccountFormModel['modelMappings'] = []
  const seenSources = new Set<string>()
  for (const item of value ?? []) {
    const sourceModel = item.sourceModel.trim()
    const upstreamModel = item.upstreamModel.trim()
    const sourceEndpointFamily = item.sourceEndpointFamily
    const upstreamEndpointFamily = item.upstreamEndpointFamily
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel.toLowerCase()}`
    if (!sourceModel || !upstreamModel || (sourceModel === upstreamModel && sourceEndpointFamily === upstreamEndpointFamily) || seenSources.has(sourceKey)) {
      continue
    }
    seenSources.add(sourceKey)
    output.push({
      sourceModel,
      sourceEndpointFamily,
      upstreamModel,
      upstreamEndpointFamily,
      enabled: item.enabled !== false
    })
  }
  return output
}

function validateAccountModelMappings(
  value: AccountFormModel['modelMappings'],
  supportedModels: string[],
  supportedEndpointModes: AccountFormModel['supportedEndpointModes'],
  providerProfile?: ProviderDefinition | ProviderDefinition['protocolProfiles'][number]
): string | undefined {
  const seenSources = new Set<string>()
  const supportedModelSet = new Set(supportedModels.map((model) => model.toLowerCase()))
  for (const item of value ?? []) {
    const sourceModel = item.sourceModel.trim()
    const upstreamModel = item.upstreamModel.trim()
    const sourceEndpointFamily = item.sourceEndpointFamily
    const upstreamEndpointFamily = item.upstreamEndpointFamily
    if (!sourceModel && !upstreamModel) {
      continue
    }
    if (!sourceModel || !upstreamModel) {
      return '模型映射需要同时选择下游模型和上游模型'
    }
    const protocolValidation = accountModelMappingProtocolValidationMessage({
      sourceEndpointFamily,
      upstreamEndpointFamily,
      context: { providerProfile, supportedEndpointModes }
    })
    if (protocolValidation) return protocolValidation
    if (sourceModel === upstreamModel && sourceEndpointFamily === upstreamEndpointFamily) {
      return '模型映射的下游模型和上游模型不能完全相同'
    }
    if (!supportedModelSet.has(upstreamModel.toLowerCase())) {
      return `模型映射的上游模型必须从支持模型中选择：${upstreamModel}`
    }
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel.toLowerCase()}`
    if (seenSources.has(sourceKey)) {
      return `下游模型 ${sourceModel} / ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 已重复配置映射`
    }
    seenSources.add(sourceKey)
  }
  return undefined
}
