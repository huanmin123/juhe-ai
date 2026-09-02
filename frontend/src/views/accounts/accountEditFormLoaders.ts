import type { AccountAdvancedDetail, AccountCloneContext, AccountCloneCredentialOptions, AccountEditBasicDetail, AccountEffectiveErrorHandlingRule } from '@/types/domain'
import type { GroupSelection } from '@/shared/groupLabelCache'

import { accountAvailabilityScheduleFormFingerprint, createAccountAvailabilityScheduleForm } from './accountAvailabilitySchedule'
import {
  accountApiKeysForForm,
  accountApiKeyWeightsForForm,
  accountTagNames,
  cloneAccountModelMappings,
  cloneAccountName
} from './accountEditFormPayload'
import { loadAccountErrorPolicyRules } from './accountErrorPolicyPayload'
import {
  systemInheritedErrorPolicyRulesPreview,
  type AccountErrorPolicyInheritedRule,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicyTypes'
import { loadAccountResponseInspectionRules } from './accountResponseInspectionPolicyPayload'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import { asString } from './accountBasicFormatters'
import { parseStrictDatePickerValue } from './accountFormatters'
import type { AccountFormModel } from './accountFormTypes'
import {
  defaultAccountEndpointModes,
  normalizeAccountEndpointModes
} from './accountEndpointModes'
import { accountGptRequestOverridesForForm } from './accountGptRequestOverrides'
import { inferGeminiOAuthType } from './geminiOAuthType'
import { loadAccountQuotaRecoveryPolicy } from './accountQuotaRecoveryPolicyTypes'

export class AccountEditFormLoadError extends Error {
  readonly cause: unknown
  readonly fallbackMessage?: string
  readonly log: boolean

  constructor(message: string, options: { cause?: unknown; fallbackMessage?: string; log?: boolean } = {}) {
    super(message)
    this.name = 'AccountEditFormLoadError'
    this.cause = options.cause
    this.fallbackMessage = options.fallbackMessage
    this.log = options.log ?? false
  }
}

export interface AccountEditFormLoadResult {
  patch: AccountFormModel
  errorPolicyRules: AccountErrorPolicyRuleForm[]
  inheritedErrorPolicyRules: AccountErrorPolicyInheritedRule[]
  responseInspectionRules: AccountResponseInspectionRuleForm[]
  scheduleFingerprint: string
}

export interface AccountBasicEditFormLoadResult {
  patch: AccountFormModel
}

interface AccountBasicFormLoadInput {
  account: AccountEditBasicDetail
  credentials: Record<string, unknown>
  defaults: AccountFormModel
  fallbackGroupId?: string
  selectedGroup?: GroupSelection
  allowMissingBaseUrl?: boolean
}

interface AccountFormLoadInput extends AccountBasicFormLoadInput {
  advanced: AccountAdvancedDetail
}

interface AccountCloneFormLoadInput {
  account: AccountCloneContext
  defaults: AccountFormModel
  selectedGroup?: GroupSelection
}

export function buildAccountEditFormLoad(input: AccountFormLoadInput): AccountEditFormLoadResult {
  const { account, advanced, credentials } = input
  const basicPatch = buildAccountBasicFormPatch(input)
  const errorPolicyRules = loadCredentialErrorPolicyRules(credentials, '账户详情错误处理策略')
  const inheritedErrorPolicyRules = loadInheritedErrorPolicyRules(advanced.effectiveErrorHandlingRules)
  const responseInspectionRules = loadCredentialResponseInspectionRules(credentials, '账户详情响应检查策略')
  // Keep the form override separate from the effective system/default policy.
  // The latter is passed to the inline rule editor for display only; loading it
  // here would make an inherited account look locally overridden on save.
  const quotaRecoveryPolicy = loadAccountQuotaRecoveryPolicy(credentials.quota_recovery_policy)
  const { accountExpiresAt, availabilitySchedule } = parseAccountScheduleFields(
    advanced.accountExpiresAt,
    advanced.availabilitySchedule,
    '账户数据结构异常，请清理后再编辑'
  )
  const patch: AccountFormModel = {
    ...basicPatch,
    proxyProfileId: advanced.proxyProfileId,
    accountExpiresAt,
    temporaryUnavailableContinuousProbeEnabled: advanced.temporaryUnavailableContinuousProbeEnabled !== false,
    lockDeathTimeoutSeconds: advanced.lockDeathTimeoutSeconds ?? 300,
    lockRetryIntervalSeconds: advanced.lockRetryIntervalSeconds ?? 5,
    ...accountGptRequestOverridesForForm(account.providerCode, credentials),
    modelMappings: cloneAccountModelMappings(advanced.modelMappings),
    availabilitySchedule,
    ...accountBalanceFormFields(advanced),
    quotaRecoveryPolicy,
    errorHandlingRuleOverrides: Array.isArray(credentials.error_handling_rule_overrides) ? credentials.error_handling_rule_overrides as AccountFormModel['errorHandlingRuleOverrides'] : []
  }

  return {
    patch,
    errorPolicyRules,
    inheritedErrorPolicyRules,
    responseInspectionRules,
    scheduleFingerprint: accountAvailabilityScheduleFormFingerprint(patch.availabilitySchedule)
  }
}

export function buildAccountBasicEditFormLoad(input: AccountBasicFormLoadInput): AccountBasicEditFormLoadResult {
  return { patch: buildAccountBasicFormPatch(input) }
}

function buildAccountBasicFormPatch(input: AccountBasicFormLoadInput): AccountFormModel {
  const { account, credentials, defaults, fallbackGroupId, selectedGroup } = input
  return {
    ...defaults,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId ?? defaults.providerProtocolProfileId,
    name: account.name,
    type: account.type,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    privilege: account.superPriorityEnabled ? 'super_priority' : account.fallbackEnabled ? 'fallback' : 'normal',
    status: account.status === 'active' ? 'active' : account.status === 'disabled' ? 'disabled' : 'pending_test',
    clientCompatibility: accountClientCompatibilityForForm(account),
    supportedEndpointModes: accountEndpointModesForForm(account, credentials),
    groupId: selectedGroup?.id ?? fallbackGroupId,
    group: selectedGroup,
    apiKey: asString(credentials.api_key) ?? '',
    apiKeys: accountApiKeysForForm(credentials),
    apiKeyStrategy: parseAccountApiKeyStrategy(credentials),
    apiKeyWeights: accountApiKeyWeightsForForm(credentials),
    baseUrl: credentialBaseUrlForForm(credentials, '账户详情凭据', input.allowMissingBaseUrl),
    accessToken: asString(credentials.access_token) ?? '',
    refreshToken: asString(credentials.refresh_token) ?? '',
    googleClientId: asString(credentials.client_id) ?? '',
    googleClientSecret: asString(credentials.client_secret) ?? '',
    googleQuotaProjectId: asString(credentials.quota_project_id) ?? '',
    oauthType: inferGeminiOAuthType(credentials),
    tierId: asString(credentials.tier_id) ?? '',
    projectId: asString(credentials.project_id) ?? '',
    supportedModels: [...(account.supportedModels ?? [])],
    healthCheckModel: account.healthCheckModel,
    healthCheckEndpointMode: account.healthCheckEndpointMode,
    tags: accountTagNames(account.tags),
    notes: account.notes ?? ''
  }
}

export function buildAccountCloneFormLoad(input: AccountCloneFormLoadInput): AccountEditFormLoadResult {
  const { account, defaults, selectedGroup } = input
  const credentials = account.credentialOptions
  const errorPolicyRules = loadCredentialErrorPolicyRules(credentials, '克隆来源错误处理策略')
  const responseInspectionRules = loadCredentialResponseInspectionRules(credentials, '克隆来源响应检查策略')
  // A clone starts as a new account: do not carry the source account's
  // account-level quota override. It will display the effective global policy
  // and only fork a local override after the user edits it.
  const quotaRecoveryPolicy = loadAccountQuotaRecoveryPolicy(undefined)
  const baseUrl = credentialBaseUrlForForm(credentials, '克隆来源凭据')
  const { accountExpiresAt, availabilitySchedule } = parseAccountScheduleFields(
    account.accountExpiresAt,
    account.availabilitySchedule,
    '克隆来源账户数据结构异常，请清理后再克隆'
  )
  const patch: AccountFormModel = {
    ...defaults,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId ?? defaults.providerProtocolProfileId,
    name: cloneAccountName(account.name),
    type: account.type,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    privilege: account.superPriorityEnabled ? 'super_priority' : account.fallbackEnabled ? 'fallback' : 'normal',
    status: accountCloneStatusForForm(account.status),
    statusSelectionExplicit: true,
    clientCompatibility: accountClientCompatibilityForForm(account),
    supportedEndpointModes: accountEndpointModesForForm(account, credentials),
    proxyProfileId: account.proxyProfileId,
    accountExpiresAt,
    groupId: selectedGroup?.id,
    group: selectedGroup,
    apiKey: '',
    apiKeys: cloneAccountApiKeySlots(credentials),
    apiKeyStrategy: parseAccountApiKeyStrategy(credentials),
    apiKeyWeights: cloneAccountApiKeyWeights(credentials),
    baseUrl,
    accessToken: '',
    refreshToken: '',
    googleClientId: asString(credentials.client_id) ?? '',
    callbackUrl: '',
    oauthMode: 'manual',
    ssoTokens: '',
    googleQuotaProjectId: asString(credentials.quota_project_id) ?? '',
    oauthType: inferGeminiOAuthType(credentials),
    tierId: asString(credentials.tier_id) ?? '',
    projectId: asString(credentials.project_id) ?? '',
    supportedModels: [...(account.supportedModels ?? [])],
    healthCheckModel: account.healthCheckModel,
    temporaryUnavailableContinuousProbeEnabled: account.temporaryUnavailableContinuousProbeEnabled !== false,
    healthCheckEndpointMode: account.healthCheckEndpointMode,
    ...accountGptRequestOverridesForForm(account.providerCode, credentials),
    modelMappings: cloneAccountModelMappings(account.modelMappings),
    tags: accountTagNames(account.tags),
    availabilitySchedule,
    ...accountBalanceFormFields(account),
    notes: account.notes ?? '',
    quotaRecoveryPolicy,
    errorHandlingRuleOverrides: []
  }

  return {
    patch,
    errorPolicyRules,
    inheritedErrorPolicyRules: systemInheritedErrorPolicyRulesPreview(),
    responseInspectionRules,
    scheduleFingerprint: accountAvailabilityScheduleFormFingerprint(patch.availabilitySchedule)
  }
}

function loadInheritedErrorPolicyRules(
  rules: AccountAdvancedDetail['effectiveErrorHandlingRules']
): AccountErrorPolicyInheritedRule[] {
  return (rules ?? [])
    .filter((rule) => rule.source === 'system' && rule.inherited === true && rule.editable === false)
    .map(toInheritedErrorPolicyRule)
}

function toInheritedErrorPolicyRule(rule: AccountEffectiveErrorHandlingRule): AccountErrorPolicyInheritedRule {
  const actions = ['retry_next', 'rate_limited', 'temp_unschedulable', 'error_disabled'] as const
  if (!actions.includes(rule.action)) {
    throw new Error(`系统错误处理策略动作无效：${String(rule.action)}`)
  }
  return {
    id: rule.id,
    source: 'system',
    inherited: true,
    editable: false,
    enabled: rule.enabled !== false,
    name: rule.name,
    priority: rule.priority,
    status_codes: listToText(rule.status_codes),
    error_codes: listToText(rule.error_codes),
    error_types: listToText(rule.error_types),
    keywords: listToText(rule.keywords),
    action: rule.action,
    reset_strategy: rule.reset_strategy ?? 'duration',
    duration_hours: rule.duration_hours ?? null,
    daily_reset_hour: rule.daily_reset_hour ?? null,
    weekly_reset_day: rule.weekly_reset_day ?? null,
    weekly_reset_hour: rule.weekly_reset_hour ?? null,
    description: rule.description ?? ''
  }
}

function listToText(value: unknown): string {
  if (!Array.isArray(value)) return ''
  return value.map((item) => String(item).trim()).filter(Boolean).join(', ')
}

function accountClientCompatibilityForForm(account: Pick<AccountEditBasicDetail, 'clientCompatibility'>): AccountFormModel['clientCompatibility'] {
  return account.clientCompatibility
}

function parseAccountApiKeyStrategy(credentials: { api_key_strategy?: unknown }): AccountFormModel['apiKeyStrategy'] {
  const strategy = credentials.api_key_strategy
  if (strategy === undefined || strategy === null || strategy === '') return 'failover'
  if (strategy === 'round_robin' || strategy === 'weighted_round_robin' || strategy === 'failover') return strategy
  throw new AccountEditFormLoadError('账户凭据 API Key 策略异常，请先修正已保存的账户凭据')
}

function accountEndpointModesForForm(
  account: Pick<AccountEditBasicDetail, 'providerCode' | 'providerProtocolProfileId' | 'type'>
    & Partial<Pick<AccountEditBasicDetail, 'protocolCode' | 'protocolVersion'>>,
  credentials: Pick<AccountCloneCredentialOptions, 'supported_endpoint_modes'>
): AccountFormModel['supportedEndpointModes'] {
  return normalizeAccountEndpointModes(
    credentials.supported_endpoint_modes,
    defaultAccountEndpointModes(account.providerCode, account.type, undefined, {
      protocolProfile: account
    })
  )
}

function credentialBaseUrlForForm(credentials: { base_url?: unknown }, label: string, allowMissing = false): string {
  const baseUrl = asString(credentials.base_url)
  if (!baseUrl && allowMissing) return ''
  if (!baseUrl) {
    throw new AccountEditFormLoadError(`${label}缺少 Base URL，请先修正账户凭据`)
  }
  return baseUrl
}

function accountCloneStatusForForm(status: AccountCloneContext['status']): AccountFormModel['status'] {
  return status === 'active' || status === 'disabled' || status === 'pending_test' ? status : 'pending_test'
}

function cloneAccountApiKeySlots(credentials: AccountCloneCredentialOptions): string[] {
  const count = Number(credentials.api_key_count ?? 1)
  const slotCount = Number.isInteger(count) ? Math.min(Math.max(count, 1), 50) : 1
  return Array.from({ length: slotCount }, () => '')
}

function cloneAccountApiKeyWeights(credentials: AccountCloneCredentialOptions): number[] {
  const slots = cloneAccountApiKeySlots(credentials)
  const sourceWeights = Array.isArray(credentials.api_key_weights) ? credentials.api_key_weights : []
  return slots.map((_, index) => {
    const value = Number(sourceWeights[index] ?? 1)
    return Number.isInteger(value) ? Math.min(Math.max(value, 1), 100) : 1
  })
}

function accountBalanceFormFields(input: {
  balanceQueryEnabled: boolean
  balanceQueryConfig?: AccountAdvancedDetail['balanceQueryConfig']
}): Pick<AccountFormModel,
  | 'balanceQueryEnabled'
  | 'balanceQueryAdapter'
  | 'balanceQueryPreferredBuiltinAdapter'
  | 'balanceQueryIntervalMinutes'
  | 'balanceQueryCustomPath'
  | 'balanceQueryRemainingPointer'
  | 'balanceQueryTotalPointer'
  | 'balanceQueryUsedPointer'
  | 'balanceQueryDivisor'
> {
  return {
    balanceQueryEnabled: input.balanceQueryEnabled === true,
    balanceQueryAdapter: input.balanceQueryConfig?.adapter === 'custom' ? 'custom' : 'builtin',
    balanceQueryPreferredBuiltinAdapter: input.balanceQueryConfig?.preferredBuiltinAdapter,
    balanceQueryIntervalMinutes: input.balanceQueryConfig?.intervalMinutes ?? 5,
    balanceQueryCustomPath: input.balanceQueryConfig?.custom?.path ?? '',
    balanceQueryRemainingPointer: input.balanceQueryConfig?.custom?.remainingPointer ?? '',
    balanceQueryTotalPointer: input.balanceQueryConfig?.custom?.totalPointer ?? '',
    balanceQueryUsedPointer: input.balanceQueryConfig?.custom?.usedPointer ?? '',
    balanceQueryDivisor: input.balanceQueryConfig?.custom?.divisor ?? ''
  }
}

function loadCredentialErrorPolicyRules(credentials: { error_handling_rules?: unknown }, label: string): AccountErrorPolicyRuleForm[] {
  try {
    return loadAccountErrorPolicyRules(credentials as Record<string, unknown>)
  } catch (error) {
    throw new AccountEditFormLoadError(`${label}配置异常，请先修正已保存的账户凭据`, {
      cause: error,
      log: true
    })
  }
}

function loadCredentialResponseInspectionRules(credentials: { response_inspection_rules?: unknown }, label: string): AccountResponseInspectionRuleForm[] {
  try {
    return loadAccountResponseInspectionRules(credentials as Record<string, unknown>)
  } catch (error) {
    throw new AccountEditFormLoadError(`${label}配置异常，请先修正已保存的账户凭据`, {
      cause: error,
      log: true
    })
  }
}

function parseAccountScheduleFields(
  accountExpiresAtSource: AccountAdvancedDetail['accountExpiresAt'],
  availabilityScheduleSource: AccountAdvancedDetail['availabilitySchedule'],
  fallbackMessage: string
): Pick<AccountFormModel, 'accountExpiresAt' | 'availabilitySchedule'> {
  try {
    return {
      accountExpiresAt: parseStrictDatePickerValue(accountExpiresAtSource, '账户过期时间'),
      availabilitySchedule: createAccountAvailabilityScheduleForm(availabilityScheduleSource)
    }
  } catch (error) {
    throw new AccountEditFormLoadError(fallbackMessage, {
      cause: error,
      fallbackMessage,
      log: true
    })
  }
}
