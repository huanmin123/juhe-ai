import type { AccountSummary } from '@/types/domain'
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
import { isAuthorizedAccount, parseStrictDatePickerValue } from './accountFormatters'
import { defaultAccountClientCompatibility } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import {
  defaultAccountEndpointModes,
  normalizeAccountEndpointModes
} from './accountEndpointModes'
import { effectiveAccountTestClientCompatibility } from './accountProviderCapabilities'
import { accountGptRequestOverridesForForm } from './accountGptRequestOverrides'

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

interface AccountFormLoadInput {
  account: AccountSummary
  credentials: Record<string, unknown>
  defaults: AccountFormModel
  fallbackGroupId?: string
  selectedGroup?: GroupSelection
}

export function buildAccountEditFormLoad(input: AccountFormLoadInput): AccountEditFormLoadResult {
  const { account, credentials, defaults, fallbackGroupId, selectedGroup } = input
  const baseUrl = credentialBaseUrlForForm(credentials, '账户详情凭据')
  const errorPolicyRules = loadCredentialErrorPolicyRules(credentials, '账户详情错误处理策略')
  const responseInspectionRules = loadCredentialResponseInspectionRules(credentials, '账户详情响应检查策略')
  const { accountExpiresAt, availabilitySchedule } = parseAccountScheduleFields(
    account.accountExpiresAt,
    accountAvailabilityScheduleForForm(account),
    '账户数据结构异常，请清理后再编辑'
  )
  const patch: AccountFormModel = {
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
    codexResponsesSafeRepairEnabled: credentials.codex_responses_safe_repair_enabled !== false,
    codexResponsesStrictInterceptEnabled: credentials.codex_responses_strict_intercept_enabled === true,
    supportedEndpointModes: accountEndpointModesForForm(account, credentials),
    proxyProfileId: account.proxyProfileId,
    accountExpiresAt,
    groupId: selectedGroup?.id ?? fallbackGroupId,
    group: selectedGroup,
    apiKey: asString(credentials.api_key) ?? '',
    apiKeys: accountApiKeysForForm(credentials),
    apiKeyStrategy: credentials.api_key_strategy === 'weighted_round_robin' ? 'weighted_round_robin' : 'round_robin',
    apiKeyWeights: accountApiKeyWeightsForForm(credentials),
    baseUrl,
    accessToken: asString(credentials.access_token) ?? '',
    refreshToken: asString(credentials.refresh_token) ?? '',
    googleClientId: asString(credentials.client_id) ?? '',
    googleClientSecret: asString(credentials.client_secret) ?? '',
    googleQuotaProjectId: asString(credentials.quota_project_id) ?? '',
    oauthType: googleOAuthType(credentials.oauth_type),
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
    notes: account.notes ?? '',
    balanceQueryEnabled: account.balanceQueryEnabled === true,
    balanceQueryAdapter: account.balanceQueryConfig?.adapter ?? 'builtin',
    balanceQueryPreferredBuiltinAdapter: account.balanceQueryConfig?.preferredBuiltinAdapter,
    balanceQueryIntervalMinutes: account.balanceQueryConfig?.intervalMinutes ?? 5,
    balanceQueryCustomPath: account.balanceQueryConfig?.custom?.path ?? '',
    balanceQueryRemainingPointer: account.balanceQueryConfig?.custom?.remainingPointer ?? '',
    balanceQueryTotalPointer: account.balanceQueryConfig?.custom?.totalPointer ?? '',
    balanceQueryUsedPointer: account.balanceQueryConfig?.custom?.usedPointer ?? '',
    balanceQueryDivisor: account.balanceQueryConfig?.custom?.divisor ?? ''
  }

  return {
    patch,
    errorPolicyRules,
    responseInspectionRules,
    scheduleFingerprint: accountAvailabilityScheduleFormFingerprint(patch.availabilitySchedule)
  }
}

function googleOAuthType(value: unknown): AccountFormModel['oauthType'] {
  if (value === 'code_assist' || value === 'google_one' || value === 'ai_studio') return value
  return 'code_assist'
}

export function buildAccountCloneFormLoad(input: AccountFormLoadInput): AccountEditFormLoadResult {
  const { account, credentials, defaults, fallbackGroupId, selectedGroup } = input
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
    codexResponsesSafeRepairEnabled: credentials.codex_responses_safe_repair_enabled !== false,
    codexResponsesStrictInterceptEnabled: credentials.codex_responses_strict_intercept_enabled === true,
    supportedEndpointModes: accountEndpointModesForForm(account, credentials),
    proxyProfileId: account.proxyProfileId,
    accountExpiresAt,
    groupId: selectedGroup?.id ?? fallbackGroupId,
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

function accountAvailabilityScheduleForForm(account: AccountSummary) {
  return isAuthorizedAccount(account)
    ? account.authorizationInstanceSourceAccountAvailabilitySchedule
    : account.availabilitySchedule
}

function accountClientCompatibilityForForm(account: AccountSummary): AccountFormModel['clientCompatibility'] {
  return effectiveAccountTestClientCompatibility(account, 'account_default')
    ?? defaultAccountClientCompatibility(account.providerCode)
}

function accountEndpointModesForForm(
  account: AccountSummary,
  credentials: Record<string, unknown>
): AccountFormModel['supportedEndpointModes'] {
  return normalizeAccountEndpointModes(
    credentials.supported_endpoint_modes,
    defaultAccountEndpointModes(account.providerCode, account.type, undefined, {
      protocolProfile: account
    })
  )
}

function credentialBaseUrlForForm(credentials: Record<string, unknown>, label: string): string {
  const baseUrl = asString(credentials.base_url)
  if (!baseUrl) {
    throw new AccountEditFormLoadError(`${label}缺少 Base URL，请先修正账户凭据`)
  }
  return baseUrl
}

function loadCredentialErrorPolicyRules(credentials: Record<string, unknown>, label: string): AccountErrorPolicyRuleForm[] {
  try {
    return loadAccountErrorPolicyRules(credentials)
  } catch (error) {
    throw new AccountEditFormLoadError(`${label}配置异常，请先修正已保存的账户凭据`, {
      cause: error,
      log: true
    })
  }
}

function loadCredentialResponseInspectionRules(credentials: Record<string, unknown>, label: string): AccountResponseInspectionRuleForm[] {
  try {
    return loadAccountResponseInspectionRules(credentials)
  } catch (error) {
    throw new AccountEditFormLoadError(`${label}配置异常，请先修正已保存的账户凭据`, {
      cause: error,
      log: true
    })
  }
}

function parseAccountScheduleFields(
  accountExpiresAtSource: AccountSummary['accountExpiresAt'],
  availabilityScheduleSource: AccountSummary['availabilitySchedule'],
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
