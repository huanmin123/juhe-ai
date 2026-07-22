import type {
  AccountSummary,
  ProviderDefinition,
  ProviderModelApiProtocol,
  ProviderModelReasoningEffort,
  ProviderModelServiceTier
} from '@/types/domain'
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
import { canCreateOAuthAccount, endpointModesForProfile, supportsOAuthAccountType } from './accountProviderCapabilities'
import { FALLBACK_PROVIDERS } from './accountOptions'
import {
  accountModelMappingEndpointFamilyText,
  accountModelMappingProtocolValidationMessage
} from './accountModelMappingProtocolMatrix'
import {
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY,
  isOpenAIProtocolProfile
} from '@/shared/providerProtocol'
import {
  accountGptRequestOverrideCapabilities,
  isAccountGptReasoningEffortOverrideAvailable,
  isAccountGptServiceTierOverrideAvailable
} from './accountGptRequestOverrides'
import { buildAccountBalancePayload, validateAccountBalanceForm } from './accountBalanceQuery'
import { accountHealthCheckEndpointModeOptions } from './accountHealthCheckEndpointMode'

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
  healthCheckModel: string
  healthCheckEndpointMode: AccountFormModel['healthCheckEndpointMode']
  temporaryUnavailableContinuousProbeEnabled: boolean
  modelMappings: AccountFormModel['modelMappings']
  tags: string[]
  proxyProfileId?: string | null
  accountExpiresAt: string | null
  availabilitySchedule?: AccountAvailabilitySchedulePayload | null
  groupId?: string
  notes: string
  balanceQueryEnabled?: boolean
  balanceQueryConfig?: Record<string, unknown>
}

export type AccountUpdatePayload = Omit<AccountSavePayload, 'providerCode' | 'providerProtocolProfileId' | 'type'>

export type AccountOAuthCreateCommonPayload = {
  providerProtocolProfileId?: string
  name?: string
  groupId?: string
  concurrencyLimit: number
  priority: number
  supportedModels: string[]
  healthCheckModel: string
  healthCheckEndpointMode: AccountFormModel['healthCheckEndpointMode']
  temporaryUnavailableContinuousProbeEnabled: boolean
  modelMappings: AccountFormModel['modelMappings']
  tags: string[]
  proxyProfileId?: string
  accountExpiresAt: string | null
  availabilitySchedule?: AccountAvailabilitySchedulePayload | null
  credentialsPatch?: {
    supported_endpoint_modes?: AccountFormModel['supportedEndpointModes']
    service_tier_override?: Exclude<AccountFormModel['serviceTierOverride'], ''>
    reasoning_effort_override?: Exclude<AccountFormModel['reasoningEffortOverride'], ''>
    error_handling_rules?: unknown
    response_inspection_rules?: unknown
  }
  notes?: string
}

export function validateAccountSaveForm(input: {
  editingId?: string
  form: AccountFormModel
  hasAuthSession: boolean
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  providers?: ProviderDefinition[]
  mappingAnthropicSourceModelOptions?: ModelMappingProtocolOption[]
  mappingGeminiSourceModelOptions?: ModelMappingProtocolOption[]
  mappingSourceModelOptions?: ModelMappingProtocolOption[]
  mappingUpstreamModelOptions?: ModelMappingProtocolOption[]
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
  if (!editingId && form.type === 'google_oauth') {
    if (!form.baseUrl.trim()) return '请填写 Base URL'
    if (!form.accessToken.trim() && !form.refreshToken.trim()) return '请填写 Google Access Token 或 Refresh Token'
    if (form.refreshToken.trim() && (!form.googleClientId.trim() || !form.googleClientSecret.trim())) return 'Refresh Token 需要 Client ID 和 Client Secret'
  }
  const formProviderProfile = resolveFormProviderProfile(form, input.providers)
  if (!editingId && form.type === 'oauth' && !supportsOAuthAccountType(formProviderProfile)) return '当前供应商协议不支持创建 OAuth 账户'
  if (!editingId && form.type === 'oauth' && canCreateOAuthAccount(formProviderProfile) && form.oauthMode === 'manual' && !input.hasAuthSession) return '请先生成授权链接'
  if (!editingId && form.type === 'oauth' && canCreateOAuthAccount(formProviderProfile) && form.oauthMode === 'manual' && !form.callbackUrl.trim()) return '请粘贴回调 URL'
  if (!editingId && form.type === 'oauth' && canCreateOAuthAccount(formProviderProfile) && form.oauthMode === 'refresh_token' && !form.refreshToken.trim()) return '请填写 Refresh Token'
  if (form.type === 'oauth' && !canCreateOAuthAccount(formProviderProfile) && !form.baseUrl.trim()) return '请填写 Base URL'
  if (form.type === 'oauth' && !canCreateOAuthAccount(formProviderProfile) && !form.accessToken.trim()) {
    return form.providerCode === 'anthropic'
      ? '请填写 Claude Code OAuth Token'
      : '请填写 Access Token'
  }
  const supportedModels = normalizeSupportedModels(form.supportedModels)
  if (!supportedModels.length) return '请选择支持模型'
  const healthCheckModel = form.healthCheckModel.trim()
  if (!healthCheckModel) return '请选择检查模型'
  if (!supportedModels.includes(healthCheckModel)) return '检查模型必须从账户支持模型中选择'
  if (!accountHealthCheckEndpointModeOptions(form.supportedEndpointModes).some((option) => option.value === form.healthCheckEndpointMode)) {
    return '检查请求形态必须选择已启用的 JSON 或流式上游能力'
  }
  const requestOverrideValidation = validateAccountGptRequestOverrides(
    form,
    supportedModels,
    input.mappingUpstreamModelOptions ?? []
  )
  if (requestOverrideValidation) return requestOverrideValidation
  const tagValidation = validateAccountTags(form.tags)
  if (tagValidation) return tagValidation
  const scheduleValidation = validateAccountAvailabilityScheduleForm(form.availabilitySchedule)
  if (scheduleValidation) return scheduleValidation
  const balanceValidation = validateAccountBalanceForm(form)
  if (balanceValidation) return balanceValidation
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
  return validateAccountModelMappings(
    form.modelMappings,
    supportedModels,
    form.supportedEndpointModes,
    formProviderProfile.profile ?? formProviderProfile.provider,
    {
      mappingAnthropicSourceModelOptions: input.mappingAnthropicSourceModelOptions,
      mappingGeminiSourceModelOptions: input.mappingGeminiSourceModelOptions,
      mappingSourceModelOptions: input.mappingSourceModelOptions,
      mappingUpstreamModelOptions: input.mappingUpstreamModelOptions
    }
  )
}

type ModelMappingProtocolOption = {
  value: string
  supportedApiProtocols?: ProviderModelApiProtocol[]
  supportedServiceTiers?: ProviderModelServiceTier[]
  supportedReasoningEfforts?: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort | null
}

export function resolveFormProviderProfile(form: AccountFormModel, providers: ProviderDefinition[] = FALLBACK_PROVIDERS): {
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
  const balancePayload = buildAccountBalancePayload(input.form)
  return {
    providerCode: input.form.providerCode,
    providerProtocolProfileId: input.form.providerProtocolProfileId,
    name: input.form.name.trim() || undefined,
    type: input.form.type,
    credentials: accountCredentials(input),
    concurrencyLimit: input.form.concurrencyLimit,
    priority: input.form.priority,
    supportedModels: normalizeSupportedModels(input.form.supportedModels),
    healthCheckModel: input.form.healthCheckModel.trim(),
    healthCheckEndpointMode: input.form.healthCheckEndpointMode,
    temporaryUnavailableContinuousProbeEnabled: input.form.temporaryUnavailableContinuousProbeEnabled,
    modelMappings: normalizeAccountModelMappings(input.form.modelMappings),
    tags: normalizeAccountTags(input.form.tags),
    proxyProfileId: saveProxyProfileId(input.form.proxyProfileId, Boolean(input.editingId)),
    accountExpiresAt: formatServerDateTimeInput(input.form.accountExpiresAt),
    availabilitySchedule: buildAccountAvailabilitySchedulePayload(input.form.availabilitySchedule),
    groupId: input.form.groupId,
    notes: input.form.notes,
    ...balancePayload
  }
}

export function buildAccountUpdatePayload(payload: AccountSavePayload): AccountUpdatePayload {
  return {
    name: payload.name,
    credentials: payload.credentials,
    concurrencyLimit: payload.concurrencyLimit,
    priority: payload.priority,
    supportedModels: payload.supportedModels,
    healthCheckModel: payload.healthCheckModel,
    healthCheckEndpointMode: payload.healthCheckEndpointMode,
    temporaryUnavailableContinuousProbeEnabled: payload.temporaryUnavailableContinuousProbeEnabled,
    modelMappings: payload.modelMappings,
    tags: payload.tags,
    proxyProfileId: payload.proxyProfileId,
    accountExpiresAt: payload.accountExpiresAt,
    availabilitySchedule: payload.availabilitySchedule,
    groupId: payload.groupId,
    notes: payload.notes,
    balanceQueryEnabled: payload.balanceQueryEnabled,
    balanceQueryConfig: payload.balanceQueryConfig
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
    healthCheckModel: input.form.healthCheckModel.trim(),
    healthCheckEndpointMode: input.form.healthCheckEndpointMode,
    temporaryUnavailableContinuousProbeEnabled: input.form.temporaryUnavailableContinuousProbeEnabled,
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
  if (typeof credentials.service_tier_override === 'string') {
    credentialsPatch.service_tier_override = credentials.service_tier_override as Exclude<AccountFormModel['serviceTierOverride'], ''>
  }
  if (typeof credentials.reasoning_effort_override === 'string') {
    credentialsPatch.reasoning_effort_override = credentials.reasoning_effort_override as Exclude<AccountFormModel['reasoningEffortOverride'], ''>
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
    const key = name
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
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push(model)
  }
  return output
}

function validateAccountGptRequestOverrides(
  form: AccountFormModel,
  supportedModels: string[],
  modelOptions: ModelMappingProtocolOption[]
): string | undefined {
  const capabilities = accountGptRequestOverrideCapabilities({
    providerCode: form.providerCode,
    accountType: form.type,
    modelOptions,
    supportedModels,
    supportedEndpointModes: form.supportedEndpointModes
  })
  if (!isAccountGptServiceTierOverrideAvailable(form.serviceTierOverride, capabilities)) {
    return '所选支持模型中没有模型支持当前服务等级覆盖'
  }
  if (!isAccountGptReasoningEffortOverrideAvailable(form.reasoningEffortOverride, capabilities)) {
    return '所选支持模型中没有模型支持当前思考级别覆盖'
  }
  return undefined
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
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel}`
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
  providerProfile?: ProviderDefinition | ProviderDefinition['protocolProfiles'][number],
  options: {
    mappingAnthropicSourceModelOptions?: ModelMappingProtocolOption[]
    mappingGeminiSourceModelOptions?: ModelMappingProtocolOption[]
    mappingSourceModelOptions?: ModelMappingProtocolOption[]
    mappingUpstreamModelOptions?: ModelMappingProtocolOption[]
  } = {}
): string | undefined {
  const seenSources = new Set<string>()
  const supportedModelSet = new Set(supportedModels.map((model) => model.trim()).filter(Boolean))
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
      enabled: item.enabled,
      context: { providerProfile, supportedEndpointModes }
    })
    if (protocolValidation) return protocolValidation
    if (sourceModel === upstreamModel && sourceEndpointFamily === upstreamEndpointFamily) {
      return '模型映射的下游模型和上游模型不能完全相同'
    }
    const sourceModelOptions = sourceModelOptionsForEndpointFamily(sourceEndpointFamily, options)
    const sourceModelProtocolAllowed = isOpenAIResponsesToChatMapping(item, providerProfile)
      ? (
          modelOptionSupportsProtocol(sourceModel, OPENAI_RESPONSES_FAMILY, sourceModelOptions)
          || modelOptionSupportsProtocol(sourceModel, OPENAI_CHAT_COMPLETIONS_FAMILY, sourceModelOptions)
        )
      : modelOptionSupportsProtocol(sourceModel, sourceEndpointFamily, sourceModelOptions)
    if (!sourceModelProtocolAllowed) {
      return `下游模型 ${sourceModel} 不支持 ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 协议，请先选择协议支持的模型`
    }
    if (!modelOptionSupportsProtocol(upstreamModel, upstreamEndpointFamily, options.mappingUpstreamModelOptions)) {
      return `上游模型 ${upstreamModel} 不支持 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 协议，请先选择协议支持的模型`
    }
    if (!supportedModelSet.has(upstreamModel)) {
      return `模型映射的上游模型必须从支持模型中选择：${upstreamModel}`
    }
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel}`
    if (seenSources.has(sourceKey)) {
      return `下游模型 ${sourceModel} / ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 已重复配置映射`
    }
    seenSources.add(sourceKey)
  }
  return undefined
}

function sourceModelOptionsForEndpointFamily(
  endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'],
  options: {
    mappingAnthropicSourceModelOptions?: ModelMappingProtocolOption[]
    mappingGeminiSourceModelOptions?: ModelMappingProtocolOption[]
    mappingSourceModelOptions?: ModelMappingProtocolOption[]
  }
): ModelMappingProtocolOption[] | undefined {
  if (endpointFamily === 'messages') return options.mappingAnthropicSourceModelOptions
  if (endpointFamily === 'generate_content' || endpointFamily === 'stream_generate_content') return options.mappingGeminiSourceModelOptions
  return options.mappingSourceModelOptions
}

function modelOptionSupportsProtocol(
  model: string,
  endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'] | AccountFormModel['modelMappings'][number]['upstreamEndpointFamily'],
  options?: ModelMappingProtocolOption[]
): boolean {
  if (!options?.length) return true
  const item = options.find((option) => option.value === model)
  if (!item) return true
  if (!item.supportedApiProtocols?.length) return true
  return item.supportedApiProtocols.includes(endpointFamily as ProviderModelApiProtocol)
}

function isOpenAIResponsesToChatMapping(
  mapping: AccountFormModel['modelMappings'][number],
  providerProfile?: ProviderDefinition | ProviderDefinition['protocolProfiles'][number]
): boolean {
  return mapping.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    && mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
    && isOpenAIProtocolProfile(providerProfile)
}
