import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { assertOpenAIEndpointModesCompatible } from '../../domain/openai-endpoint-modes.js'
import { assertAnthropicEndpointModesCompatible } from '../../domain/anthropic-endpoint-modes.js'
import { assertGeminiEndpointModesCompatible } from '../../domain/gemini-endpoint-modes.js'
import { isAnthropicProtocolProfile, isGatewaySupportedProtocolProfile, isGeminiProtocolProfile, isHybridProviderCode, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import type { AccountClientCompatibility, AccountModelMapping, AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson
} from '../../storage/account-availability-schedule.js'
import type { AccountTestDraftSnapshot } from '../../storage/account-test-tasks.repository.js'
import { newId } from '../../storage/database.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  assertAccountSupportedModelsRequired,
  findGroupSummary,
  findGroupSummaryAsync,
  listProviders,
  listProvidersAsync,
  normalizeAccountCredentialsForWrite,
  normalizeAccountModelMappingsForProvider,
  normalizeAccountModelMappingsForProviderAsync
} from '../../storage/repositories.js'
import type { RequestAccessScope } from '../auth/request-context.js'
import {
  assertAccountGptRequestOverridesSupported,
  assertAccountGptRequestOverridesSupportedAsync
} from './account-gpt-request-overrides.validation.js'

export interface AccountDraftTestAccountRequest {
  providerCode: string
  providerProtocolProfileId: string
  name: string
  type: string
  credentials?: Record<string, unknown>
  supportedModels?: string[]
  healthCheckModel: string
  modelMappings?: unknown
  concurrencyLimit?: number
  priority?: number
  superPriorityEnabled?: boolean
  fallbackEnabled?: boolean
  proxyProfileId?: string | null
  groupId: string
  accountExpiresAt?: string | null
  availabilitySchedule?: Record<string, unknown> | null
  notes?: string
}

export function savedAccountDraftTestSnapshot(
  account: AccountSummary,
  accountInput: AccountDraftTestAccountRequest,
  requestAccess: RequestAccessScope
): AccountTestDraftSnapshot {
  if (account.accessType === 'authorized') {
    throw new Error('授权账户测试不支持使用未保存表单配置')
  }
  if (accountInput.providerCode !== account.providerCode || accountInput.type !== account.type) {
    throw new Error('账户测试草稿与当前账户不一致')
  }
  const preparedDraft = prepareAccountDraftTestSnapshot({
    accountInput,
    requestAccess,
    draftAccountId: account.id
  })
  if (
    account.providerProtocolProfileId
    && preparedDraft.draftAccount.providerProtocolProfileId
    && preparedDraft.draftAccount.providerProtocolProfileId !== account.providerProtocolProfileId
  ) {
    throw new Error('账户测试草稿与当前账户协议档案不一致')
  }
  return {
    ...preparedDraft.draftAccount,
    stateTargetAccountId: account.id
  }
}

export async function savedAccountDraftTestSnapshotAsync(
  account: AccountSummary,
  accountInput: AccountDraftTestAccountRequest,
  requestAccess: RequestAccessScope
): Promise<AccountTestDraftSnapshot> {
  if (account.accessType === 'authorized') {
    throw new Error('授权账户测试不支持使用未保存表单配置')
  }
  if (accountInput.providerCode !== account.providerCode || accountInput.type !== account.type) {
    throw new Error('账户测试草稿与当前账户不一致')
  }
  const preparedDraft = await prepareAccountDraftTestSnapshotAsync({
    accountInput,
    requestAccess,
    draftAccountId: account.id
  })
  if (
    account.providerProtocolProfileId
    && preparedDraft.draftAccount.providerProtocolProfileId
    && preparedDraft.draftAccount.providerProtocolProfileId !== account.providerProtocolProfileId
  ) {
    throw new Error('账户测试草稿与当前账户协议档案不一致')
  }
  return {
    ...preparedDraft.draftAccount,
    stateTargetAccountId: account.id
  }
}

export function prepareAccountDraftTestSnapshot(input: {
  accountInput: AccountDraftTestAccountRequest
  requestAccess: RequestAccessScope
  draftAccountId?: string
}): { account: AccountSummary; draftAccount: AccountTestDraftSnapshot } {
  const group = findGroupSummary(input.accountInput.groupId, input.requestAccess)
  return prepareAccountDraftTestSnapshotResolved(input, group, listProviders())
}

export async function prepareAccountDraftTestSnapshotAsync(input: {
  accountInput: AccountDraftTestAccountRequest
  requestAccess: RequestAccessScope
  draftAccountId?: string
}): Promise<{ account: AccountSummary; draftAccount: AccountTestDraftSnapshot }> {
  const group = await findGroupSummaryAsync(input.accountInput.groupId, input.requestAccess)
  return await prepareAccountDraftTestSnapshotResolvedAsync(input, group, await listProvidersAsync())
}

function prepareAccountDraftTestSnapshotResolved(
  input: {
    accountInput: AccountDraftTestAccountRequest
    requestAccess: RequestAccessScope
    draftAccountId?: string
  },
  group: ReturnType<typeof findGroupSummary>,
  providers: ReturnType<typeof listProviders>
): { account: AccountSummary; draftAccount: AccountTestDraftSnapshot } {
  const accountInput = input.accountInput
  if (!group || group.providerCode !== accountInput.providerCode || group.permissions?.canManageAccounts === false) {
    throw new Error('账户分组无效')
  }
  const provider = providers.find((item) => item.code === accountInput.providerCode)
  const providerProtocolProfileId = optionalText(accountInput.providerProtocolProfileId)
  if (!providerProtocolProfileId) {
    throw new Error('账户 providerProtocolProfileId 不能为空')
  }
  const providerProfile = provider?.protocolProfiles.find((item) => item.id === providerProtocolProfileId)
  if (!provider || !providerProfile || !providerProfile.accountTypes.includes(accountInput.type as AccountSummary['type'])) {
    throw new Error(`供应商 ${accountInput.providerCode} 不支持账户类型 ${accountInput.type}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${accountInput.providerCode}`)
  }
  if (!isGatewaySupportedProtocolProfile(providerProfile)) {
    throw new Error('当前仅支持测试 OpenAI 或 Anthropic 协议账户')
  }
  const ownerSystemAccountId = group.ownerSystemAccountId
    ?? group.systemAccountId
    ?? input.requestAccess.systemAccountFilterId
    ?? input.requestAccess.systemAccountId
  if (!ownerSystemAccountId) {
    throw new Error('账户分组缺少归属用户，无法测试')
  }
  const clientCompatibility = isAnthropicProtocolProfile(providerProfile)
    ? 'openai_standard' as const
    : normalizeOpenAIAccountClientCompatibility(
        accountInput.providerCode,
        accountInput.type,
        undefined,
        'openai_standard',
        providerProfile
      )
  const credentials = normalizeAccountCredentialsForWrite(accountInput.type, draftAccountCredentials(accountInput, providerProfile.baseUrl), {
    providerCode: accountInput.providerCode,
    accountType: accountInput.type,
    clientCompatibility,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
  })
  const availabilitySchedule = accountAvailabilityScheduleFromRequest({ availabilitySchedule: accountInput.availabilitySchedule })
  const availabilityScheduleJson = accountAvailabilityScheduleJson(availabilitySchedule) ?? undefined
  const account = draftTestAccountSummary({
    id: input.draftAccountId,
    account: accountInput,
    availabilitySchedule,
    clientCompatibility,
    credentials,
    groupName: group.name,
    ownerSystemAccountId,
    defaultSupportedModels: provider.defaultSupportedModels,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
  })
  assertAccountGptRequestOverridesSupported({
    providerCode: account.providerCode,
    accountType: account.type,
    credentials: account.credentials,
    supportedModels: account.supportedModels ?? [],
    systemAccountId: ownerSystemAccountId
  })
  assertDraftEndpointModesCompatible(providerProfile, {
    modes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
    modelMappings: account.modelMappings,
    accountType: accountInput.type,
    clientCompatibility
  })
  return {
    account,
    draftAccount: {
      id: account.id,
      ownerSystemAccountId,
      groupId: accountInput.groupId,
      groupName: group.name,
      providerCode: accountInput.providerCode,
      providerProtocolProfileId: providerProfile.id,
      protocolCode: providerProfile.protocolCode,
      protocolVersion: providerProfile.protocolVersion,
      name: account.name,
      type: accountInput.type,
      credentials,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      superPriorityEnabled: account.superPriorityEnabled,
      fallbackEnabled: account.fallbackEnabled,
      clientCompatibility,
      supportedModels: account.supportedModels,
      healthCheckModel: account.healthCheckModel,
      modelMappings: account.modelMappings,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: account.accountExpiresAt,
      availabilitySchedule,
      availabilityScheduleJson,
      notes: account.notes
    }
  }
}

async function prepareAccountDraftTestSnapshotResolvedAsync(
  input: {
    accountInput: AccountDraftTestAccountRequest
    requestAccess: RequestAccessScope
    draftAccountId?: string
  },
  group: ReturnType<typeof findGroupSummary>,
  providers: ReturnType<typeof listProviders>
): Promise<{ account: AccountSummary; draftAccount: AccountTestDraftSnapshot }> {
  const accountInput = input.accountInput
  if (!group || group.providerCode !== accountInput.providerCode || group.permissions?.canManageAccounts === false) {
    throw new Error('账户分组无效')
  }
  const provider = providers.find((item) => item.code === accountInput.providerCode)
  const providerProtocolProfileId = optionalText(accountInput.providerProtocolProfileId)
  if (!providerProtocolProfileId) {
    throw new Error('账户 providerProtocolProfileId 不能为空')
  }
  const providerProfile = provider?.protocolProfiles.find((item) => item.id === providerProtocolProfileId)
  if (!provider || !providerProfile || !providerProfile.accountTypes.includes(accountInput.type as AccountSummary['type'])) {
    throw new Error(`供应商 ${accountInput.providerCode} 不支持账户类型 ${accountInput.type}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${accountInput.providerCode}`)
  }
  if (!isGatewaySupportedProtocolProfile(providerProfile)) {
    throw new Error('当前仅支持测试 OpenAI 或 Anthropic 协议账户')
  }
  const ownerSystemAccountId = group.ownerSystemAccountId
    ?? group.systemAccountId
    ?? input.requestAccess.systemAccountFilterId
    ?? input.requestAccess.systemAccountId
  if (!ownerSystemAccountId) {
    throw new Error('账户分组缺少归属用户，无法测试')
  }
  const clientCompatibility = isAnthropicProtocolProfile(providerProfile)
    ? 'openai_standard' as const
    : normalizeOpenAIAccountClientCompatibility(
        accountInput.providerCode,
        accountInput.type,
        undefined,
        'openai_standard',
        providerProfile
      )
  const credentials = normalizeAccountCredentialsForWrite(accountInput.type, draftAccountCredentials(accountInput, providerProfile.baseUrl), {
    providerCode: accountInput.providerCode,
    accountType: accountInput.type,
    clientCompatibility,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
  })
  const availabilitySchedule = accountAvailabilityScheduleFromRequest({ availabilitySchedule: accountInput.availabilitySchedule })
  const availabilityScheduleJson = accountAvailabilityScheduleJson(availabilitySchedule) ?? undefined
  const account = await draftTestAccountSummaryAsync({
    id: input.draftAccountId,
    account: accountInput,
    availabilitySchedule,
    clientCompatibility,
    credentials,
    groupName: group.name,
    ownerSystemAccountId,
    defaultSupportedModels: provider.defaultSupportedModels,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
  })
  await assertAccountGptRequestOverridesSupportedAsync({
    providerCode: account.providerCode,
    accountType: account.type,
    credentials: account.credentials,
    supportedModels: account.supportedModels ?? [],
    systemAccountId: ownerSystemAccountId
  })
  assertDraftEndpointModesCompatible(providerProfile, {
    modes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
    modelMappings: account.modelMappings,
    accountType: accountInput.type,
    clientCompatibility
  })
  return {
    account,
    draftAccount: {
      id: account.id,
      ownerSystemAccountId,
      groupId: accountInput.groupId,
      groupName: group.name,
      providerCode: accountInput.providerCode,
      providerProtocolProfileId: providerProfile.id,
      protocolCode: providerProfile.protocolCode,
      protocolVersion: providerProfile.protocolVersion,
      name: account.name,
      type: accountInput.type,
      credentials,
      concurrencyLimit: account.concurrencyLimit,
      priority: account.priority,
      superPriorityEnabled: account.superPriorityEnabled,
      fallbackEnabled: account.fallbackEnabled,
      clientCompatibility,
      supportedModels: account.supportedModels,
      healthCheckModel: account.healthCheckModel,
      modelMappings: account.modelMappings,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: account.accountExpiresAt,
      availabilitySchedule,
      availabilityScheduleJson,
      notes: account.notes
    }
  }
}

function draftAccountCredentials(account: AccountDraftTestAccountRequest, providerBaseUrl: string): Record<string, unknown> {
  const credentials = credentialsRecordValue(account.credentials) ?? {}
  if (account.type !== 'oauth' || hasCredentialText(credentials.base_url)) {
    return credentials
  }
  return {
    ...credentials,
    base_url: providerBaseUrl || 'https://api.openai.com/v1'
  }
}

function assertDraftEndpointModesCompatible(
  providerProfile: {
    id?: string
    providerCode?: string
    providerProtocolProfileId?: string
    protocolCode?: string
    protocolVersion?: string
  },
  input: {
    modes: readonly AccountSupportedEndpointMode[]
    modelMappings?: readonly AccountModelMapping[]
    accountType?: string
    clientCompatibility: AccountSummary['clientCompatibility']
  }
): void {
  if (isHybridProviderCode(providerProfile.providerCode)) {
    return
  }
  if (isAnthropicProtocolProfile(providerProfile)) {
    assertAnthropicEndpointModesCompatible({
      modes: input.modes,
      accountType: input.accountType
    })
    return
  }
  if (isOpenAIProtocolProfile(providerProfile)) {
    assertOpenAIEndpointModesCompatible({
      modes: input.modes,
      modelMappings: input.modelMappings,
      providerCode: providerProfile.providerCode,
      providerProtocolProfileId: providerProfile.providerProtocolProfileId ?? providerProfile.id,
      accountType: input.accountType,
      clientCompatibility: input.clientCompatibility
    })
    return
  }
  if (isGeminiProtocolProfile(providerProfile)) {
    assertGeminiEndpointModesCompatible({
      modes: input.modes,
      accountType: input.accountType
    })
  }
}

function draftTestAccountSummary(input: {
  id?: string
  account: AccountDraftTestAccountRequest
  availabilitySchedule: ReturnType<typeof accountAvailabilityScheduleFromRequest>
  clientCompatibility: AccountSummary['clientCompatibility']
  credentials: Record<string, unknown>
  groupName?: string
  ownerSystemAccountId: string
  defaultSupportedModels: string[]
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
}): AccountSummary {
  const usage = emptyAccountUsageSummary()
  const supportedModels = draftSupportedModels(input.account.providerCode, input.account.supportedModels, input.defaultSupportedModels)
  const healthCheckModel = requiredDraftHealthCheckModel(input.account.healthCheckModel, supportedModels)
  const modelMappings = normalizeDraftAccountModelMappings(input.account.modelMappings, input.account.providerCode, input.ownerSystemAccountId, {
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  }, input.credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]) ?? []
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(modelMappings, supportedModels)
  return {
    id: input.id ?? newId('acctdraft'),
    systemAccountId: input.ownerSystemAccountId,
    ownerSystemAccountId: input.ownerSystemAccountId,
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    name: input.account.name,
    notes: optionalText(input.account.notes),
    type: input.account.type,
    credentials: input.credentials,
    status: 'active',
    concurrencyLimit: input.account.concurrencyLimit ?? 20,
    currentConcurrency: 0,
    priority: input.account.priority ?? 0,
    superPriorityEnabled: input.account.superPriorityEnabled ?? false,
    fallbackEnabled: input.account.fallbackEnabled ?? false,
    clientCompatibility: input.clientCompatibility,
    supportedModels,
    healthCheckModel,
    modelMappings,
    proxyProfileId: optionalText(input.account.proxyProfileId),
    schedulable: true,
    availabilitySchedule: input.availabilitySchedule,
    accountExpiresAt: optionalText(input.account.accountExpiresAt),
    todayUsage: usage,
    usage,
    accessType: 'owner',
    boundGroupId: input.account.groupId,
    boundGroupName: input.groupName,
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

async function draftTestAccountSummaryAsync(input: {
  id?: string
  account: AccountDraftTestAccountRequest
  availabilitySchedule: ReturnType<typeof accountAvailabilityScheduleFromRequest>
  clientCompatibility: AccountSummary['clientCompatibility']
  credentials: Record<string, unknown>
  groupName?: string
  ownerSystemAccountId: string
  defaultSupportedModels: string[]
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
}): Promise<AccountSummary> {
  const usage = emptyAccountUsageSummary()
  const supportedModels = draftSupportedModels(input.account.providerCode, input.account.supportedModels, input.defaultSupportedModels)
  const healthCheckModel = requiredDraftHealthCheckModel(input.account.healthCheckModel, supportedModels)
  const modelMappings = await normalizeDraftAccountModelMappingsAsync(input.account.modelMappings, input.account.providerCode, input.ownerSystemAccountId, {
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  }, input.credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]) ?? []
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(modelMappings, supportedModels)
  return {
    id: input.id ?? newId('acctdraft'),
    systemAccountId: input.ownerSystemAccountId,
    ownerSystemAccountId: input.ownerSystemAccountId,
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    name: input.account.name,
    notes: optionalText(input.account.notes),
    type: input.account.type,
    credentials: input.credentials,
    status: 'active',
    concurrencyLimit: input.account.concurrencyLimit ?? 20,
    currentConcurrency: 0,
    priority: input.account.priority ?? 0,
    superPriorityEnabled: input.account.superPriorityEnabled ?? false,
    fallbackEnabled: input.account.fallbackEnabled ?? false,
    clientCompatibility: input.clientCompatibility,
    supportedModels,
    healthCheckModel,
    modelMappings,
    proxyProfileId: optionalText(input.account.proxyProfileId),
    schedulable: true,
    availabilitySchedule: input.availabilitySchedule,
    accountExpiresAt: optionalText(input.account.accountExpiresAt),
    todayUsage: usage,
    usage,
    accessType: 'owner',
    boundGroupId: input.account.groupId,
    boundGroupName: input.groupName,
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

function normalizedTextList(value: string[] | undefined): string[] {
  return [...new Set((value ?? []).map((item) => item.trim()).filter(Boolean))]
}

function draftSupportedModels(providerCode: string, value: string[] | undefined, defaultSupportedModels: readonly string[]): string[] {
  void providerCode
  const supportedModels = normalizedTextList(value)
  const result = supportedModels.length ? supportedModels : normalizedTextList([...defaultSupportedModels])
  assertAccountSupportedModelsRequired(result)
  return result
}

function requiredDraftHealthCheckModel(value: unknown, supportedModels: readonly string[]): string {
  const model = optionalText(value)
  if (!model) {
    throw new Error('账户检查模型不能为空')
  }
  if (!supportedModels.includes(model)) {
    throw new Error('账户检查模型必须属于账户支持模型')
  }
  return model
}

function normalizeDraftAccountModelMappings(
  value: unknown,
  providerCode: string,
  ownerSystemAccountId: string,
  providerProfile: {
    providerCode?: string
    providerProtocolProfileId?: string
    protocolCode?: string
    protocolVersion?: string
  },
  supportedEndpointModes: readonly AccountSupportedEndpointMode[] | undefined
): AccountSummary['modelMappings'] {
  return normalizeAccountModelMappingsForProvider(value ?? [], providerCode, ownerSystemAccountId, providerProfile, {
    supportedEndpointModes
  }) ?? []
}

async function normalizeDraftAccountModelMappingsAsync(
  value: unknown,
  providerCode: string,
  ownerSystemAccountId: string,
  providerProfile: {
    providerCode?: string
    providerProtocolProfileId?: string
    protocolCode?: string
    protocolVersion?: string
  },
  supportedEndpointModes: readonly AccountSupportedEndpointMode[] | undefined
): Promise<AccountSummary['modelMappings']> {
  return await normalizeAccountModelMappingsForProviderAsync(value ?? [], providerCode, ownerSystemAccountId, providerProfile, {
    supportedEndpointModes
  }) ?? []
}

function optionalText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}

function hasOwnRecordKey(value: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(value, key)
}

function hasCredentialText(value: unknown): boolean {
  return typeof value === 'string' && value.trim().length > 0
}

function credentialsRecordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
