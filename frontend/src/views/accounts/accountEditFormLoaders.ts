import type { AccountAdvancedDetail, AccountCloneContext, AccountCloneCredentialOptions, AccountEditBasicDetail } from '@/types/domain'
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
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
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
  const responseInspectionRules = loadCredentialResponseInspectionRules(credentials, '账户详情响应检查策略')
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
    ...accountGptRequestOverridesForForm(account.providerCode, credentials),
    modelMappings: cloneAccountModelMappings(advanced.modelMappings),
    availabilitySchedule,
    balanceQueryEnabled: advanced.balanceQueryEnabled === true,
    balanceQueryAdapter: advanced.balanceQueryConfig?.adapter ?? 'builtin',
    balanceQueryPreferredBuiltinAdapter: advanced.balanceQueryConfig?.preferredBuiltinAdapter,
    balanceQueryIntervalMinutes: advanced.balanceQueryConfig?.intervalMinutes ?? 5,
    balanceQueryCustomPath: advanced.balanceQueryConfig?.custom?.path ?? '',
    balanceQueryRemainingPointer: advanced.balanceQueryConfig?.custom?.remainingPointer ?? '',
    balanceQueryTotalPointer: advanced.balanceQueryConfig?.custom?.totalPointer ?? '',
    balanceQueryUsedPointer: advanced.balanceQueryConfig?.custom?.usedPointer ?? '',
    balanceQueryDivisor: advanced.balanceQueryConfig?.custom?.divisor ?? ''
  }

  return {
    patch,
    errorPolicyRules,
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
    status: account.status === 'disabled' ? 'disabled' : account.status === 'pending_test' ? 'pending_test' : 'active',
    clientCompatibility: accountClientCompatibilityForForm(account),
    supportedEndpointModes: accountEndpointModesForForm(account, credentials),
    groupId: selectedGroup?.id ?? fallbackGroupId,
    group: selectedGroup,
    apiKey: asString(credentials.api_key) ?? '',
    apiKeys: accountApiKeysForForm(credentials),
    apiKeyStrategy: credentials.api_key_strategy === 'weighted_round_robin' ? 'weighted_round_robin' : 'round_robin',
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
    status: 'pending_test',
    clientCompatibility: accountClientCompatibilityForForm(account),
    supportedEndpointModes: accountEndpointModesForForm(account, credentials),
    proxyProfileId: account.proxyProfileId,
    accountExpiresAt,
    groupId: selectedGroup?.id,
    group: selectedGroup,
    apiKey: '',
    apiKeys: [''],
    apiKeyStrategy: 'round_robin',
    apiKeyWeights: [1],
    baseUrl,
    accessToken: '',
    refreshToken: '',
    callbackUrl: '',
    oauthMode: 'manual',
    ssoTokens: '',
    supportedModels: [...(account.supportedModels ?? [])],
    healthCheckModel: account.healthCheckModel,
    temporaryUnavailableContinuousProbeEnabled: account.temporaryUnavailableContinuousProbeEnabled !== false,
    healthCheckEndpointMode: account.healthCheckEndpointMode,
    ...accountGptRequestOverridesForForm(account.providerCode, credentials),
    modelMappings: cloneAccountModelMappings(account.modelMappings),
    tags: accountTagNames(account.tags),
    availabilitySchedule,
    notes: account.notes ?? ''
  }

  return {
    patch,
    errorPolicyRules,
    responseInspectionRules,
    scheduleFingerprint: accountAvailabilityScheduleFormFingerprint(patch.availabilitySchedule)
  }
}

function accountClientCompatibilityForForm(account: Pick<AccountEditBasicDetail, 'clientCompatibility'>): AccountFormModel['clientCompatibility'] {
  return account.clientCompatibility
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
