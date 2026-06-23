import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { resolveProviderProtocolProfileIdFromConnectionType } from '../../domain/provider-connection-type.js'
import { assertOpenAIEndpointModesCompatible } from '../../domain/openai-endpoint-modes.js'
import { assertAnthropicEndpointModesCompatible } from '../../domain/anthropic-endpoint-modes.js'
import { isAnthropicProtocolProfile, isGatewaySupportedProtocolProfile, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import type { AccountClientCompatibility, AccountStatus, AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson
} from '../../storage/account-availability-schedule.js'
import {
  getAccountTestTaskRecord,
  type AccountTestDraftSnapshot
} from '../../storage/account-test-tasks.repository.js'
import { newId } from '../../storage/database.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  findGroupSummary,
  listProviders,
  normalizeAccountCredentialsForWrite,
  normalizeAccountModelMappingsForProvider
} from '../../storage/repositories.js'
import type { RequestAccessScope } from '../auth/request-context.js'
import { hashStableValue } from '../deduplication/deduplication.service.js'

export interface AccountDraftTestAccountRequest {
  providerCode: string
  providerProtocolProfileId?: string
  connectionType?: string
  name: string
  type: string
  credentials?: Record<string, unknown>
  clientCompatibility?: AccountClientCompatibility
  supportedModels?: string[]
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

export interface AccountCreateDraftActivationRequest extends Omit<AccountDraftTestAccountRequest, 'groupId'> {
  groupId?: string | null
  status?: AccountStatus
  activationTestTaskId?: string
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

export function prepareAccountDraftTestSnapshot(input: {
  accountInput: AccountDraftTestAccountRequest
  requestAccess: RequestAccessScope
  draftAccountId?: string
}): { account: AccountSummary; draftAccount: AccountTestDraftSnapshot } {
  const accountInput = input.accountInput
  const group = findGroupSummary(accountInput.groupId, input.requestAccess)
  if (!group || group.providerCode !== accountInput.providerCode || group.permissions?.canManageAccounts === false) {
    throw new Error('账户分组无效')
  }
  const groupProviderProtocolProfileId = group.providerProtocolProfileId?.trim()
  if (!groupProviderProtocolProfileId) {
    throw new Error('账户分组协议档案无效')
  }
  const provider = listProviders().find((item) => item.code === accountInput.providerCode)
  let providerProtocolProfileId: string
  try {
    providerProtocolProfileId = resolveProviderProtocolProfileIdFromConnectionType({
      providerCode: accountInput.providerCode,
      providerProtocolProfileId: accountInput.providerProtocolProfileId,
      connectionType: accountInput.connectionType
    }) ?? groupProviderProtocolProfileId
  } catch (error) {
    throw new Error(error instanceof Error ? error.message : '账户接入类型无效')
  }
  const providerProfile = provider?.protocolProfiles.find((item) => item.id === providerProtocolProfileId)
    ?? provider?.protocolProfiles.find((item) => item.id === provider.defaultProtocolProfileId)
  if (!provider || !providerProfile || !providerProfile.accountTypes.includes(accountInput.type as AccountSummary['type'])) {
    throw new Error(`供应商 ${accountInput.providerCode} 不支持账户类型 ${accountInput.type}`)
  }
  if (!provider.enabled) {
    throw new Error(`供应商已停用：${accountInput.providerCode}`)
  }
  if (group.providerProtocolProfileId !== providerProfile.id || !isGatewaySupportedProtocolProfile(providerProfile)) {
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
        accountInput.clientCompatibility,
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
  assertDraftEndpointModesCompatible(providerProfile, {
    modes: credentials.supported_endpoint_modes as AccountSupportedEndpointMode[],
    accountType: accountInput.type,
    clientCompatibility
  })
  const account = draftTestAccountSummary({
    id: input.draftAccountId,
    account: accountInput,
    availabilitySchedule,
    clientCompatibility,
    credentials,
    groupName: group.name,
    ownerSystemAccountId,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion
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
      modelMappings: account.modelMappings,
      proxyProfileId: account.proxyProfileId,
      accountExpiresAt: account.accountExpiresAt,
      availabilitySchedule,
      availabilityScheduleJson,
      notes: account.notes
    }
  }
}

export function accountCreateStatusFromActivationTest(input: {
  account: AccountCreateDraftActivationRequest
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  group?: ReturnType<typeof findGroupSummary>
  requestAccess?: RequestAccessScope
}): AccountStatus {
  const requestedStatus = input.account.status
  const activationTestTaskId = optionalText(input.account.activationTestTaskId)
  if (!activationTestTaskId) {
    if (requestedStatus === 'active') {
      throw new Error('创建为正常状态需要先完成本次账户草稿测试')
    }
    return requestedStatus ?? 'pending_test'
  }
  if (requestedStatus && requestedStatus !== 'active') {
    throw new Error('带测试任务创建账户时，状态只能为正常或留空')
  }
  assertActivationTestTaskMatchesCreate({
    ...input,
    activationTestTaskId
  })
  return 'active'
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

function assertActivationTestTaskMatchesCreate(input: {
  account: AccountCreateDraftActivationRequest
  activationTestTaskId: string
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  group?: ReturnType<typeof findGroupSummary>
  requestAccess?: RequestAccessScope
}): void {
  if (!input.requestAccess) {
    throw new Error('缺少系统账户上下文，无法确认账户草稿测试结果')
  }
  if (!input.group) {
    throw new Error('账户分组无效，无法确认账户草稿测试结果')
  }
  const task = getAccountTestTaskRecord(input.activationTestTaskId)
  if (!task || !sameAccountTestRequester(task, input.requestAccess)) {
    throw new Error('账户草稿测试任务不存在或不属于当前创建上下文')
  }
  if (task.status !== 'success' || task.result?.success !== true || !task.draftAccount) {
    throw new Error('账户草稿测试尚未成功，不能直接创建为正常状态')
  }
  const ownerSystemAccountId = input.group.ownerSystemAccountId
    ?? input.group.systemAccountId
    ?? input.requestAccess.systemAccountFilterId
    ?? input.requestAccess.systemAccountId
  const expected = accountCreateActivationFingerprintSnapshot({
    account: input.account,
    providerBaseUrl: input.providerBaseUrl,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    ownerSystemAccountId
  })
  const actual = draftActivationFingerprintSnapshot(task.draftAccount)
  if (hashStableValue(expected) !== hashStableValue(actual)) {
    throw new Error('账户草稿测试内容已变化，请重新测试后再创建为正常状态')
  }
}

function sameAccountTestRequester(
  task: NonNullable<ReturnType<typeof getAccountTestTaskRecord>>,
  access: RequestAccessScope
): boolean {
  return task.requestSystemAccountId === access.systemAccountId
    && task.requestRole === access.role
    && (task.requestSystemAccountFilterId ?? undefined) === access.systemAccountFilterId
}

function accountCreateActivationFingerprintSnapshot(input: {
  account: AccountCreateDraftActivationRequest
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  ownerSystemAccountId: string
}): Record<string, unknown> {
  const account = accountDraftRequestFromCreate(input.account)
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    account.providerCode,
    account.type,
    account.clientCompatibility,
    'openai_standard',
    {
      providerCode: account.providerCode,
      providerProtocolProfileId: input.providerProtocolProfileId,
      protocolCode: input.protocolCode,
      protocolVersion: input.protocolVersion
    }
  )
  const credentials = normalizeAccountCredentialsForWrite(account.type, draftAccountCredentials(account, input.providerBaseUrl), {
    providerCode: account.providerCode,
    accountType: account.type,
    clientCompatibility,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  })
  const availabilitySchedule = accountAvailabilityScheduleFromRequest({ availabilitySchedule: account.availabilitySchedule })
  const supportedModels = normalizedTextList(account.supportedModels)
  const modelMappings = normalizeDraftAccountModelMappings(account.modelMappings, account.providerCode, input.ownerSystemAccountId, {
    providerCode: account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  }) ?? []
  assertAccountModelMappingUpstreamsAllowedBySupportedModels(modelMappings, supportedModels)
  return {
    ownerSystemAccountId: input.ownerSystemAccountId,
    groupId: account.groupId,
    providerCode: account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    name: account.name,
    type: account.type,
    credentials,
    concurrencyLimit: account.concurrencyLimit ?? 20,
    priority: account.priority ?? 0,
    superPriorityEnabled: account.superPriorityEnabled ?? false,
    fallbackEnabled: account.fallbackEnabled ?? false,
    clientCompatibility,
    supportedModels,
    modelMappings,
    proxyProfileId: optionalText(account.proxyProfileId),
    accountExpiresAt: optionalText(account.accountExpiresAt),
    availabilityScheduleJson: accountAvailabilityScheduleJson(availabilitySchedule) ?? undefined,
    notes: optionalText(account.notes)
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
    accountType?: string
    clientCompatibility: AccountSummary['clientCompatibility']
  }
): void {
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
      providerCode: providerProfile.providerCode,
      providerProtocolProfileId: providerProfile.providerProtocolProfileId ?? providerProfile.id,
      accountType: input.accountType,
      clientCompatibility: input.clientCompatibility
    })
  }
}

function draftActivationFingerprintSnapshot(draft: AccountTestDraftSnapshot): Record<string, unknown> {
  return {
    ownerSystemAccountId: draft.ownerSystemAccountId,
    groupId: draft.groupId,
    providerCode: draft.providerCode,
    providerProtocolProfileId: draft.providerProtocolProfileId,
    protocolCode: draft.protocolCode,
    protocolVersion: draft.protocolVersion,
    name: draft.name,
    type: draft.type,
    credentials: draft.credentials,
    concurrencyLimit: draft.concurrencyLimit,
    priority: draft.priority,
    superPriorityEnabled: draft.superPriorityEnabled,
    fallbackEnabled: draft.fallbackEnabled,
    clientCompatibility: draft.clientCompatibility,
    supportedModels: normalizedTextList(draft.supportedModels),
    modelMappings: draft.modelMappings ?? [],
    proxyProfileId: optionalText(draft.proxyProfileId),
    accountExpiresAt: optionalText(draft.accountExpiresAt),
    availabilityScheduleJson: optionalText(draft.availabilityScheduleJson),
    notes: optionalText(draft.notes)
  }
}

function accountDraftRequestFromCreate(account: AccountCreateDraftActivationRequest): AccountDraftTestAccountRequest {
  return {
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    connectionType: account.connectionType,
    clientCompatibility: account.clientCompatibility,
    name: account.name,
    type: account.type,
    credentials: account.credentials,
    supportedModels: account.supportedModels,
    modelMappings: account.modelMappings,
    concurrencyLimit: account.concurrencyLimit,
    priority: account.priority,
    superPriorityEnabled: account.superPriorityEnabled,
    fallbackEnabled: account.fallbackEnabled,
    proxyProfileId: account.proxyProfileId,
    groupId: typeof account.groupId === 'string' ? account.groupId : '',
    accountExpiresAt: account.accountExpiresAt,
    availabilitySchedule: account.availabilitySchedule,
    notes: account.notes
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
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
}): AccountSummary {
  const usage = emptyAccountUsageSummary()
  const supportedModels = normalizedTextList(input.account.supportedModels)
  const modelMappings = normalizeDraftAccountModelMappings(input.account.modelMappings, input.account.providerCode, input.ownerSystemAccountId, {
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  }) ?? []
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
    modelMappings,
    proxyProfileId: optionalText(input.account.proxyProfileId),
    schedulable: true,
    availabilitySchedule: input.availabilitySchedule,
    availabilityScheduleActive: true,
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
    totalTokens: 0,
    totalCost: 0
  }
}

function normalizedTextList(value: string[] | undefined): string[] {
  return [...new Set((value ?? []).map((item) => item.trim()).filter(Boolean))]
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
  }
): AccountSummary['modelMappings'] {
  return normalizeAccountModelMappingsForProvider(value ?? [], providerCode, ownerSystemAccountId, providerProfile) ?? []
}

function optionalText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}

function hasCredentialText(value: unknown): boolean {
  return typeof value === 'string' && value.trim().length > 0
}

function credentialsRecordValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
