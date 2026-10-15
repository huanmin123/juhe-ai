import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { capabilityDomainIsMedia } from './accountCapabilityDomains'
import type {
  AccountEditBasicDetail,
  AccountListItem,
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
import { buildAccountCredentials, normalizedAccountApiKeys } from './accountCredentials'
import type { AccountFormModel } from './accountFormTypes'
import {
  buildAccountAvailabilitySchedulePayload,
  validateAccountAvailabilityScheduleForm,
  type AccountAvailabilitySchedulePayload
} from './accountAvailabilitySchedule'
import { validateOpenAICompatibleBaseUrl } from './accountBaseUrlValidation'
import { validateAccountEndpointModes } from './accountEndpointModes'
import { endpointModesForProfile, managedOAuthProviderKind, supportsOAuthAccountType } from './accountProviderCapabilities'
import { FALLBACK_PROVIDERS } from './accountOptions'
import {
  accountModelMappingEndpointFamilyText,
  accountModelMappingProtocolValidationMessage
} from './accountModelMappingProtocolMatrix'
import type { AccountModelMappingProviderProfile } from './accountModelMappingProtocolMatrix'
import {
  isHybridProviderCode,
  isVolcengineProviderCode
} from '@/shared/providerProtocol'
import { accountModelMappingEndpointFamilyProtocol, accountModelMappingSourceModelOptions } from './accountModelMappingModelOptions'
import {
  accountGptRequestOverrideCapabilities,
  isAccountGptReasoningEffortOverrideAvailable,
  isAccountGptServiceTierOverrideAvailable
} from './accountGptRequestOverrides'
import { buildAccountBalancePayload, validateAccountBalanceForm } from './accountBalanceQuery'
import { accountHealthCheckEndpointModeOptions } from './accountHealthCheckEndpointMode'
import { normalizeGrokSsoTokens } from './grokSsoTokens'

export const ACCOUNT_API_KEY_BATCH_CREATE_LIMIT = 10

export function validateAccountLockConfigForm(form: AccountFormModel): string | undefined {
  const timeout = Math.trunc(Number(form.lockDeathTimeoutSeconds))
  if (!Number.isInteger(timeout) || timeout < 30 || timeout > 3600) {
    return '锁死死期必须是 30..3600 秒的整数'
  }
  const interval = Math.trunc(Number(form.lockRetryIntervalSeconds))
  if (!Number.isInteger(interval) || interval < 5 || interval > 30) {
    return '锁死重试间隔必须是 5..30 秒的整数'
  }
  return undefined
}

export type AccountSavePayload = {
  providerCode: AccountFormModel['providerCode']
  providerProtocolProfileId: AccountFormModel['providerProtocolProfileId']
  name?: string
  type: AccountFormModel['type']
  credentials: Record<string, unknown>
  concurrencyLimit: number
  priority: number
  status: AccountFormModel['status']
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  supportedModels: string[]
  healthCheckModel?: string
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
  // M2 能力域契约（D3/D6）：仅创建 payload 携带（域创建时确定、不可更改）；
  // 更新 payload 由 buildAccountUpdatePayload 显式字段构造，天然不携带域字段。
  capabilityDomain?: AccountCapabilityDomain
  cloneSourceId?: string
}

export type AccountUpdatePayload = Omit<AccountSavePayload, 'providerCode' | 'providerProtocolProfileId' | 'type' | 'status'> & {
  status?: AccountSavePayload['status']
}

export type AccountOAuthCreateCommonPayload = {
  providerProtocolProfileId?: string
  capabilityDomain?: AccountCapabilityDomain
  name?: string
  groupId?: string
  concurrencyLimit: number
  priority: number
  status: AccountFormModel['status']
  superPriorityEnabled: boolean
  fallbackEnabled: boolean
  supportedModels: string[]
  healthCheckModel?: string
  healthCheckEndpointMode: AccountFormModel['healthCheckEndpointMode']
  temporaryUnavailableContinuousProbeEnabled: boolean
  modelMappings: AccountFormModel['modelMappings']
  tags: string[]
  proxyProfileId?: string
  accountExpiresAt: string | null
  availabilitySchedule?: AccountAvailabilitySchedulePayload | null
  cloneSourceId?: string
  credentialsPatch?: {
    supported_endpoint_modes?: AccountFormModel['supportedEndpointModes']
    service_tier_override?: Exclude<AccountFormModel['serviceTierOverride'], ''>
    reasoning_effort_override?: Exclude<AccountFormModel['reasoningEffortOverride'], ''>
    error_handling_rules?: unknown
    error_handling_rule_overrides?: unknown
    response_inspection_rules?: unknown
    quota_recovery_policy?: unknown
  }
  notes?: string
}

// 向导步骤归属：创建向导（三步）下校验消息应带回的步骤。名称/供应商/类型/凭证/模型/检查字段
// 都在第 1 步，归属与调度字段在第 2 步、高级配置字段在第 3 步。
// 与 validateAccountSaveForm 的消息文本同文件维护：调整上方消息文案时须同步本表关键词。
const VALIDATION_STEP_HINTS: Array<{ step: 2 | 3; keywords: string[] }> = [
  { step: 2, keywords: ['加入分组', '标签'] },
  { step: 3, keywords: ['时段', '余额', '锁死', '服务等级', '思考级别', '错误处理策略', '响应检查策略', '映射'] }
]

export function accountValidationErrorStep(message: string): 1 | 2 | 3 {
  for (const hint of VALIDATION_STEP_HINTS) {
    if (hint.keywords.some((keyword) => message.includes(keyword))) return hint.step
  }
  return 1
}

export function validateAccountSaveForm(input: {
  editingId?: string
  form: AccountFormModel
  hasAuthSession: boolean
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  providers?: ProviderDefinition[]
  mappingAnthropicSourceModelOptions?: ModelMappingProtocolOption[]
  mappingCurrentProviderSourceModelOptions?: ModelMappingProtocolOption[]
  mappingGeminiSourceModelOptions?: ModelMappingProtocolOption[]
  mappingSourceModelOptions?: ModelMappingProtocolOption[]
  mappingUpstreamModelOptions?: ModelMappingProtocolOption[]
  /** 完整编辑保存基线：映射与基线一致的存量行放行候选窗口校验（后端 PATCH「映射未变不校验」，前端对齐）。 */
  editingAdvancedBaseline?: AccountSavePayload
}): string | undefined {
  const { editingId, form } = input
  if (!form.providerCode) return '请先选择供应商'
  if (!form.providerProtocolProfileId) return '当前供应商配置不完整，请刷新后重试'
  if (!form.type) return '请先选择账户类型'
  if ((editingId || form.type === 'api_key') && !form.name.trim()) return '请填写账户名称'
  if (editingId && !form.groupId) return '请选择加入分组'
  const apiKeyCount = normalizedAccountApiKeys(form).length
  if (form.type === 'api_key' && apiKeyCount === 0) return '请填写 API Key'
  if (form.type === 'api_key' && apiKeyCount > ACCOUNT_API_KEY_BATCH_CREATE_LIMIT) return `单个账户最多配置 ${ACCOUNT_API_KEY_BATCH_CREATE_LIMIT} 个 API Key`
  if (form.type === 'api_key' && !form.baseUrl.trim()) return '请填写 Base URL'
  if (form.type === 'api_key') {
    const baseUrlValidation = validateOpenAICompatibleBaseUrl(form.baseUrl)
    if (baseUrlValidation) return baseUrlValidation
  }
  // 火山方舟音频域双值凭据（D7 清单表定案）：volcengine + audio 域必填
  // speech_appid/speech_token——出站缺一即显式失败，写入面前端先拦。
  if (form.capabilityDomain === 'audio'
    && isVolcengineProviderCode(form.providerCode)
    && (!form.speechAppId.trim() || !form.speechToken.trim())) {
    return '火山方舟音频账户必须填写语音应用 App ID 和 Access Token'
  }
  const formProviderProfile = resolveFormProviderProfile(form, input.providers)
  const managedOAuthProvider = managedOAuthProviderKind(formProviderProfile)
  if (!editingId && form.type === 'google_oauth') {
    if (!form.baseUrl.trim()) return '请填写 Base URL'
    if (form.oauthType === 'ai_studio'
      && form.oauthMode !== 'access_token'
      && (!form.googleClientId.trim() || !form.googleClientSecret.trim())) {
      return 'Gemini AI Studio OAuth 需要 Client ID 和 Client Secret'
    }
    if (form.oauthMode === 'access_token' && !form.accessToken.trim()) return '请填写 Google Access Token'
    if (form.oauthMode === 'access_token' && form.oauthType !== 'ai_studio' && !form.projectId.trim()) {
      return 'Gemini Code Assist / Google One 直接 Token 需要 GCP Project ID'
    }
  }
  if (!editingId && form.type === 'oauth' && !supportsOAuthAccountType(formProviderProfile)) return '当前供应商协议不支持创建 OAuth 账户'
  if (!editingId && form.type === 'oauth' && !form.baseUrl.trim()) return '请填写 Base URL'
  const managedOAuthCreation = !editingId
    && (form.type === 'oauth' || form.type === 'google_oauth')
    && Boolean(managedOAuthProvider)
    && form.oauthMode !== 'access_token'
  // 国外 OAuth 供应商（Grok/GPT/Anthropic/Gemini）的上游必须经代理可达：
  // 创建与 OAuth 授权录入（refresh/SSO 模式）强制绑定代理，与后端拦截一致。
  if (
    (managedOAuthCreation || form.oauthMode === 'refresh_token' || form.oauthMode === 'sso_cookie')
    && ['gpt', 'anthropic', 'gemini', 'xai'].includes(form.providerCode)
    && !form.proxyProfileId?.trim()
  ) {
    return '该供应商的 OAuth 账户必须绑定出海代理：请在下方“代理”中选择后再提交'
  }
  if (managedOAuthCreation && form.oauthMode === 'manual' && !input.hasAuthSession) return '请先生成授权链接'
  if (managedOAuthCreation && form.oauthMode === 'manual' && !form.callbackUrl.trim()) return '请粘贴回调 URL'
  if (managedOAuthCreation && form.oauthMode === 'refresh_token' && !form.refreshToken.trim()) return '请填写 Refresh Token'
  if (managedOAuthProvider === 'grok' && form.oauthMode === 'sso_cookie' && !normalizeGrokSsoTokens(form.ssoTokens).length) {
    return '请填写至少一个 Grok Web SSO key'
  }
  if (!managedOAuthCreation && !editingId && form.type === 'oauth' && !form.accessToken.trim()) {
    return form.providerCode === 'anthropic' ? '请填写 Claude Code OAuth Token' : '请填写 Access Token'
  }
  const supportedModels = normalizeSupportedModels(form.supportedModels)
  // 国外 OAuth 供应商创建时支持模型/检查模型允许留空：后端自动取模型目录
  // 最新的对话模型并默认检查模型（留空项由系统兜底，见 oauthmgmt
  // CreateOAuthAccount）。其余场景保持必填；媒体域无该兜底（D7：支持模型
  // 一律必选），gemini native google_oauth 媒体账户同样必选。
  const oauthDefaultsAllowed = !editingId
    && !capabilityDomainIsMedia(form.capabilityDomain)
    && (form.type === 'oauth' || form.type === 'google_oauth')
    && ['gpt', 'anthropic', 'gemini', 'xai'].includes(form.providerCode)
  if (!supportedModels.length && !oauthDefaultsAllowed) return '请选择支持模型'
  const healthCheckModel = form.healthCheckModel.trim()
  if (!healthCheckModel && !oauthDefaultsAllowed) return '请选择检查模型'
  if (healthCheckModel && !supportedModels.includes(healthCheckModel)) return '检查模型必须从账户支持模型中选择'
  // 音频/视频域账户无后台探针，health_check_endpoint_mode 仅为记录值（域常量，
  // 见 defaultHealthCheckEndpointModeForCapabilityDomain），不参与「已启用能力
  // 交集」校验——qwen 长音频声明集没有任何检查形态词表值，按交集校验会被阻断
  // 保存（R5）；chat/image 域行为不变。
  if (
    form.capabilityDomain !== 'audio'
    && form.capabilityDomain !== 'video'
    && form.healthCheckEndpointMode !== 'images_json'
    && !accountHealthCheckEndpointModeOptions(form.supportedEndpointModes).some((option) => option.value === form.healthCheckEndpointMode)
  ) {
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
      mappingCurrentProviderSourceModelOptions: input.mappingCurrentProviderSourceModelOptions,
      mappingGeminiSourceModelOptions: input.mappingGeminiSourceModelOptions,
      mappingSourceModelOptions: input.mappingSourceModelOptions,
      mappingUpstreamModelOptions: input.mappingUpstreamModelOptions,
      providerCode: form.providerCode,
      baselineMappings: input.editingAdvancedBaseline?.modelMappings
    }
  )
}

export type ModelMappingProtocolOption = {
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
  accounts: AccountListItem[]
  accountDetail?: AccountEditBasicDetail | AccountSummary
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
    status: input.form.status,
    superPriorityEnabled: input.form.privilege === 'super_priority',
    fallbackEnabled: input.form.privilege === 'fallback',
    supportedModels: normalizeSupportedModels(input.form.supportedModels),
    ...(input.form.healthCheckModel.trim() ? { healthCheckModel: input.form.healthCheckModel.trim() } : {}),
    healthCheckEndpointMode: input.form.healthCheckEndpointMode,
    temporaryUnavailableContinuousProbeEnabled: input.form.temporaryUnavailableContinuousProbeEnabled,
    modelMappings: normalizeAccountModelMappings(input.form.modelMappings),
    tags: normalizeAccountTags(input.form.tags),
    proxyProfileId: saveProxyProfileId(input.form.proxyProfileId, Boolean(input.editingId)),
    accountExpiresAt: formatServerDateTimeInput(input.form.accountExpiresAt),
    availabilitySchedule: buildAccountAvailabilitySchedulePayload(input.form.availabilitySchedule),
    groupId: input.form.groupId,
    notes: input.form.notes,
    ...balancePayload,
    // M2 能力域（D3）：创建携带当前域；编辑场景该字段被 buildAccountUpdatePayload
    // 显式字段构造排除（域不可变，更新 payload 不携带）。
    capabilityDomain: input.form.capabilityDomain
  }
}

export function buildAccountUpdatePayload(payload: AccountSavePayload, statusSelectionExplicit = false): AccountUpdatePayload {
  return {
    name: payload.name,
    credentials: payload.credentials,
    concurrencyLimit: payload.concurrencyLimit,
    priority: payload.priority,
    ...(statusSelectionExplicit ? { status: payload.status } : {}),
    superPriorityEnabled: payload.superPriorityEnabled,
    fallbackEnabled: payload.fallbackEnabled,
    supportedModels: payload.supportedModels,
    ...(payload.healthCheckModel ? { healthCheckModel: payload.healthCheckModel } : {}),
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
  accounts: AccountListItem[]
  cloningSourceId?: string
  editingId?: string
  form: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
}): AccountOAuthCreateCommonPayload {
  const credentials = accountCredentials(input)
  const payload: AccountOAuthCreateCommonPayload = {
    name: input.form.name.trim() || undefined,
    providerProtocolProfileId: input.form.providerProtocolProfileId,
    // M2 能力域（D3）：OAuth 创建同样携带当前域（媒体域 gemini native google_oauth
    // 走此通道；chat 域显式携带 'chat'）。
    capabilityDomain: input.form.capabilityDomain,
    ...(input.cloningSourceId && !input.editingId ? { cloneSourceId: input.cloningSourceId } : {}),
    groupId: input.form.groupId,
    concurrencyLimit: input.form.concurrencyLimit,
    priority: input.form.priority,
    status: input.form.status,
    superPriorityEnabled: input.form.privilege === 'super_priority',
    fallbackEnabled: input.form.privilege === 'fallback',
    supportedModels: normalizeSupportedModels(input.form.supportedModels),
    ...(input.form.healthCheckModel.trim() ? { healthCheckModel: input.form.healthCheckModel.trim() } : {}),
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
  if (Object.prototype.hasOwnProperty.call(credentials, 'error_handling_rule_overrides')) {
    credentialsPatch.error_handling_rule_overrides = credentials.error_handling_rule_overrides
  }
  if (Object.prototype.hasOwnProperty.call(credentials, 'response_inspection_rules')) {
    credentialsPatch.response_inspection_rules = credentials.response_inspection_rules
  }
  if (Object.prototype.hasOwnProperty.call(credentials, 'quota_recovery_policy')) {
    credentialsPatch.quota_recovery_policy = credentials.quota_recovery_policy
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
  accounts: AccountListItem[]
  accountDetail?: AccountEditBasicDetail | AccountSummary
  editingId?: string
  form: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
}): Record<string, unknown> {
  return buildAccountCredentials({
    currentCredentials: input.accountDetail?.credentials,
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

export function validateAccountModelMappings(
  value: AccountFormModel['modelMappings'],
  supportedModels: string[],
  supportedEndpointModes: AccountFormModel['supportedEndpointModes'],
  providerProfile?: AccountModelMappingProviderProfile,
  options: {
    mappingAnthropicSourceModelOptions?: ModelMappingProtocolOption[]
    mappingCurrentProviderSourceModelOptions?: ModelMappingProtocolOption[]
    mappingGeminiSourceModelOptions?: ModelMappingProtocolOption[]
    mappingSourceModelOptions?: ModelMappingProtocolOption[]
    mappingUpstreamModelOptions?: ModelMappingProtocolOption[]
    providerCode?: string
    /** 编辑保存基线中的存量映射：行键与上游都未变的原样行放行，仅「基线中不存在或已变更」的行做候选窗口与支持模型校验（R6）。 */
    baselineMappings?: AccountFormModel['modelMappings']
  } = {}
): string | undefined {
  const seenSources = new Set<string>()
  const supportedModelSet = new Set(supportedModels.map((model) => model.trim()).filter(Boolean))
  // 存量基线放行（R6）：加载基线里未变化的行不再拿当前候选窗口当完整事实拒绝，
  // 只有新增/变更行按候选窗口与支持模型集合校验；新增域外模型仍走完整校验被拒。
  const baselineRowsByKey = new Map<string, AccountFormModel['modelMappings'][number]>()
  for (const baselineItem of options.baselineMappings ?? []) {
    const baselineSourceModel = baselineItem.sourceModel.trim()
    if (!baselineSourceModel) continue
    baselineRowsByKey.set(`${baselineItem.sourceEndpointFamily}\n${baselineSourceModel}`, baselineItem)
  }
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
    if (!isPristineBaselineMappingRow(item, baselineRowsByKey)) {
      const providerCode = options.providerCode ?? formProviderCode(providerProfile)
      const sourceModelOptions = accountModelMappingSourceModelOptions({
        providerCode,
        sourceEndpointFamily,
        currentProviderOptions: options.mappingCurrentProviderSourceModelOptions ?? [],
        openAIProtocolOptions: options.mappingSourceModelOptions ?? [],
        anthropicProtocolOptions: options.mappingAnthropicSourceModelOptions ?? [],
        geminiProtocolOptions: options.mappingGeminiSourceModelOptions ?? []
      })
      if (!modelExistsInOptions(sourceModel, sourceModelOptions)) {
        return `客户端模型 ${sourceModel} 不在当前可选模型目录中`
      }
      if (
        isHybridProviderCode(providerCode)
        && !modelOptionSupportsProtocol(sourceModel, sourceEndpointFamily, sourceModelOptions)
      ) {
        return `客户端模型 ${sourceModel} 不支持 ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 协议，请先选择协议支持的模型`
      }
      if (!modelOptionSupportsProtocol(upstreamModel, upstreamEndpointFamily, options.mappingUpstreamModelOptions)) {
        return `上游模型 ${upstreamModel} 不支持 ${accountModelMappingEndpointFamilyText(upstreamEndpointFamily)} 协议，请先选择协议支持的模型`
      }
      if (!supportedModelSet.has(upstreamModel)) {
        return `模型映射的上游模型必须从支持模型中选择：${upstreamModel}`
      }
    }
    const sourceKey = `${sourceEndpointFamily}\n${sourceModel}`
    if (seenSources.has(sourceKey)) {
      return `下游模型 ${sourceModel} / ${accountModelMappingEndpointFamilyText(sourceEndpointFamily)} 已重复配置映射`
    }
    seenSources.add(sourceKey)
  }
  return undefined
}

/**
 * 行与编辑基线一致的判定（R6）：行键（下游端点族+下游模型）在基线中存在且上游
 * 模型、上游端点族也未变，才视为「基线原样行」放行候选窗口校验；新增行、只换
 * 上游行与键变化行都不属于基线原样。
 */
function isPristineBaselineMappingRow(
  item: AccountFormModel['modelMappings'][number],
  baselineRowsByKey: Map<string, AccountFormModel['modelMappings'][number]>
): boolean {
  const baseline = baselineRowsByKey.get(`${item.sourceEndpointFamily}\n${item.sourceModel.trim()}`)
  if (!baseline) return false
  return baseline.upstreamModel.trim() === item.upstreamModel.trim()
    && baseline.upstreamEndpointFamily === item.upstreamEndpointFamily
    && baseline.enabled === item.enabled
}

function modelOptionSupportsProtocol(
  model: string,
  endpointFamily: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'] | AccountFormModel['modelMappings'][number]['upstreamEndpointFamily'],
  options?: ModelMappingProtocolOption[]
): boolean {
  if (!options?.length) return true
  const item = options.find((option) => option.value === model)
  // 媒体族端点族码与目录协议 token 不同名（video_generation→video、
  // tts→audio_speech），必须经协议换算后再对目录声明做包含判断。
  const protocol = accountModelMappingEndpointFamilyProtocol(endpointFamily)
  return Boolean(item?.supportedApiProtocols?.includes(protocol))
}

function modelExistsInOptions(model: string, options: ModelMappingProtocolOption[] | undefined): boolean {
  if (!options?.length) return true
  return options.some((option) => option.value === model)
}

function formProviderCode(providerProfile?: AccountModelMappingProviderProfile): string | undefined {
  if (!providerProfile) return undefined
  if ('providerCode' in providerProfile && typeof providerProfile.providerCode === 'string') {
    return providerProfile.providerCode
  }
  return 'code' in providerProfile ? providerProfile.code : undefined
}
