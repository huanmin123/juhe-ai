import { normalizeOpenAIAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import { assertOpenAIEndpointModesCompatible } from '../../domain/openai-endpoint-modes.js'
import { assertAnthropicEndpointModesCompatible } from '../../domain/anthropic-endpoint-modes.js'
import { assertGeminiEndpointModesCompatible } from '../../domain/gemini-endpoint-modes.js'
import { isAnthropicProtocolProfile, isGatewaySupportedProtocolProfile, isGeminiProtocolProfile, isHybridProviderCode, isOpenAIProtocolProfile } from '../../domain/provider-protocol.js'
import type { AccountClientCompatibility, AccountModelMapping, AccountStatus, AccountSummary, AccountSupportedEndpointMode } from '../../domain/types.js'
import {
  accountAvailabilityScheduleFromRequest,
  accountAvailabilityScheduleJson
} from '../../storage/account-availability-schedule.js'
import {
  getAccountTestTaskRecord,
  getAccountTestTaskRecordAsync,
  type AccountTestTaskRecord,
  type AccountTestDraftSnapshot
} from '../../storage/account-test-tasks.repository.js'
import { newId } from '../../storage/database.js'
import {
  assertAccountModelMappingUpstreamsAllowedBySupportedModels,
  assertAccountSupportedModelsRequired,
  findProviderDefaultSupportedModels,
  findProviderDefaultSupportedModelsAsync,
  findGroupSummary,
  findGroupSummaryAsync,
  listProviders,
  listProvidersAsync,
  normalizeAccountCredentialsForWrite,
  normalizeAccountModelMappingsForProvider,
  normalizeAccountModelMappingsForProviderAsync
} from '../../storage/repositories.js'
import type { RequestAccessScope } from '../auth/request-context.js'
import { hashStableValue } from '../deduplication/deduplication.service.js'

export interface AccountDraftTestAccountRequest {
  providerCode: string
  providerProtocolProfileId: string
  name: string
  type: string
  credentials?: Record<string, unknown>
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
  defaultTestModel?: string
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

export async function accountCreateStatusFromActivationTestAsync(input: {
  account: AccountCreateDraftActivationRequest
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  group?: ReturnType<typeof findGroupSummary>
  requestAccess?: RequestAccessScope
}): Promise<AccountStatus> {
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
  await assertActivationTestTaskMatchesCreateAsync({
    ...input,
    activationTestTaskId
  })
  return 'active'
}

export async function assertAccountUpdateActivationTestMatchesAsync(input: {
  currentAccount: AccountSummary
  update: Record<string, unknown>
  activationTestTaskId: string
  requestAccess?: RequestAccessScope
}): Promise<void> {
  if (!input.requestAccess) {
    throw new Error('缺少系统账户上下文，无法确认账户测试结果')
  }
  const task = await getAccountTestTaskRecordAsync(input.activationTestTaskId)
  if (!task || !sameAccountTestRequester(task, input.requestAccess)) {
    throw new Error('账户测试任务不存在或不属于当前编辑上下文')
  }
  if (task.status !== 'success' || task.result?.success !== true || !task.draftAccount) {
    throw new Error('账户测试尚未成功，不能保存新的 API Key')
  }
  assertSuccessfulTestModelMatchesRequestedDefault(input.update.defaultTestModel, task)
  if (task.draftAccount.stateTargetAccountId !== input.currentAccount.id) {
    throw new Error('账户测试任务不是当前账户的编辑测试，请重新测试')
  }
  const provider = (await listProvidersAsync()).find((item) => item.code === input.currentAccount.providerCode)
  const providerProfile = provider?.protocolProfiles.find((item) => item.id === input.currentAccount.providerProtocolProfileId)
  if (!provider || !providerProfile) {
    throw new Error('账户供应商协议档案无效，无法确认账户测试结果')
  }
  const groupId = accountUpdateGroupId(input.currentAccount, input.update)
  if (!groupId) {
    throw new Error('账户分组无效，无法确认账户测试结果')
  }
  const group = await findGroupSummaryAsync(groupId, input.requestAccess)
  if (!group || group.providerCode !== input.currentAccount.providerCode) {
    throw new Error('账户分组无效，无法确认账户测试结果')
  }
  const ownerSystemAccountId = group.ownerSystemAccountId
    ?? group.systemAccountId
    ?? input.currentAccount.ownerSystemAccountId
    ?? input.currentAccount.systemAccountId
    ?? input.requestAccess.systemAccountFilterId
    ?? input.requestAccess.systemAccountId
  if (!ownerSystemAccountId) {
    throw new Error('账户分组缺少归属用户，无法确认账户测试结果')
  }
  const expected = accountUpdateActivationFingerprintSnapshot({
    currentAccount: input.currentAccount,
    update: input.update,
    providerBaseUrl: providerProfile.baseUrl,
    providerProtocolProfileId: providerProfile.id,
    protocolCode: providerProfile.protocolCode,
    protocolVersion: providerProfile.protocolVersion,
    groupId,
    ownerSystemAccountId
  })
  const actual = draftUpdateActivationFingerprintSnapshot(task.draftAccount)
  if (hashStableValue(expected) !== hashStableValue(actual)) {
    throw new Error('账户测试内容已变化，请重新测试后再保存')
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
  assertSuccessfulTestModelMatchesRequestedDefault(input.account.defaultTestModel, task)
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
    ownerSystemAccountId,
    defaultSupportedModels: findProviderDefaultSupportedModels(input.account.providerCode)
  })
  const actual = draftActivationFingerprintSnapshot(task.draftAccount)
  if (hashStableValue(expected) !== hashStableValue(actual)) {
    throw new Error('账户草稿测试内容已变化，请重新测试后再创建为正常状态')
  }
}

async function assertActivationTestTaskMatchesCreateAsync(input: {
  account: AccountCreateDraftActivationRequest
  activationTestTaskId: string
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  group?: ReturnType<typeof findGroupSummary>
  requestAccess?: RequestAccessScope
}): Promise<void> {
  if (!input.requestAccess) {
    throw new Error('缺少系统账户上下文，无法确认账户草稿测试结果')
  }
  if (!input.group) {
    throw new Error('账户分组无效，无法确认账户草稿测试结果')
  }
  const task = await getAccountTestTaskRecordAsync(input.activationTestTaskId)
  if (!task || !sameAccountTestRequester(task, input.requestAccess)) {
    throw new Error('账户草稿测试任务不存在或不属于当前创建上下文')
  }
  if (task.status !== 'success' || task.result?.success !== true || !task.draftAccount) {
    throw new Error('账户草稿测试尚未成功，不能直接创建为正常状态')
  }
  assertSuccessfulTestModelMatchesRequestedDefault(input.account.defaultTestModel, task)
  const ownerSystemAccountId = input.group.ownerSystemAccountId
    ?? input.group.systemAccountId
    ?? input.requestAccess.systemAccountFilterId
    ?? input.requestAccess.systemAccountId
  const expected = await accountCreateActivationFingerprintSnapshotAsync({
    account: input.account,
    providerBaseUrl: input.providerBaseUrl,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    ownerSystemAccountId,
    defaultSupportedModels: await findProviderDefaultSupportedModelsAsync(input.account.providerCode)
  })
  const actual = draftActivationFingerprintSnapshot(task.draftAccount)
  if (hashStableValue(expected) !== hashStableValue(actual)) {
    throw new Error('账户草稿测试内容已变化，请重新测试后再创建为正常状态')
  }
}

async function accountCreateActivationFingerprintSnapshotAsync(input: {
  account: AccountCreateDraftActivationRequest
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  ownerSystemAccountId: string
  defaultSupportedModels?: string[]
}): Promise<Record<string, unknown>> {
  const account = accountDraftRequestFromCreate(input.account)
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    account.providerCode,
    account.type,
    undefined,
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
  const supportedModels = draftSupportedModels(
    account.providerCode,
    account.supportedModels,
    input.defaultSupportedModels ?? await findProviderDefaultSupportedModelsAsync(account.providerCode)
  )
  const modelMappings = await normalizeDraftAccountModelMappingsAsync(account.modelMappings, account.providerCode, input.ownerSystemAccountId, {
    providerCode: account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  }, credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]) ?? []
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

function sameAccountTestRequester(
  task: AccountTestTaskRecord,
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
  defaultSupportedModels?: string[]
}): Record<string, unknown> {
  const account = accountDraftRequestFromCreate(input.account)
  const clientCompatibility = normalizeOpenAIAccountClientCompatibility(
    account.providerCode,
    account.type,
    undefined,
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
  const supportedModels = draftSupportedModels(
    account.providerCode,
    account.supportedModels,
    input.defaultSupportedModels ?? findProviderDefaultSupportedModels(account.providerCode)
  )
  const modelMappings = normalizeDraftAccountModelMappings(account.modelMappings, account.providerCode, input.ownerSystemAccountId, {
    providerCode: account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  }, credentials.supported_endpoint_modes as AccountSupportedEndpointMode[]) ?? []
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

function accountUpdateActivationFingerprintSnapshot(input: {
  currentAccount: AccountSummary
  update: Record<string, unknown>
  providerBaseUrl: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  groupId: string
  ownerSystemAccountId: string
}): Record<string, unknown> {
  const account = accountDraftRequestFromUpdate(input.currentAccount, input.update, input.groupId)
  const clientCompatibility = isAnthropicProtocolProfile({
    providerCode: account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion
  })
    ? 'openai_standard' as const
    : normalizeOpenAIAccountClientCompatibility(
        account.providerCode,
        account.type,
        undefined,
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
  return {
    ownerSystemAccountId: input.ownerSystemAccountId,
    groupId: account.groupId,
    providerCode: account.providerCode,
    providerProtocolProfileId: input.providerProtocolProfileId,
    protocolCode: input.protocolCode,
    protocolVersion: input.protocolVersion,
    type: account.type,
    credentials,
    proxyProfileId: optionalText(account.proxyProfileId)
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

function assertSuccessfulTestModelMatchesRequestedDefault(
  requestedDefaultTestModel: unknown,
  task: Pick<AccountTestTaskRecord, 'result'>
): void {
  const testedModel = optionalText(task.result?.model)
  const requestedModel = optionalText(requestedDefaultTestModel)
  if (!testedModel || requestedModel !== testedModel) {
    throw new Error('账户默认测试模型必须使用本次成功测试的模型')
  }
}

function draftUpdateActivationFingerprintSnapshot(draft: AccountTestDraftSnapshot): Record<string, unknown> {
  return {
    ownerSystemAccountId: draft.ownerSystemAccountId,
    groupId: draft.groupId,
    providerCode: draft.providerCode,
    providerProtocolProfileId: draft.providerProtocolProfileId,
    protocolCode: draft.protocolCode,
    protocolVersion: draft.protocolVersion,
    type: draft.type,
    credentials: draft.credentials,
    proxyProfileId: optionalText(draft.proxyProfileId)
  }
}

function accountDraftRequestFromUpdate(
  account: AccountSummary,
  update: Record<string, unknown>,
  groupId: string
): AccountDraftTestAccountRequest {
  return {
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId ?? '',
    name: hasOwnRecordKey(update, 'name') ? optionalText(update.name) ?? account.name : account.name,
    type: account.type,
    credentials: hasOwnRecordKey(update, 'credentials')
      ? credentialsRecordValue(update.credentials) ?? {}
      : account.credentials,
    supportedModels: hasOwnRecordKey(update, 'supportedModels')
      ? normalizedTextList(Array.isArray(update.supportedModels) ? update.supportedModels.filter((item): item is string => typeof item === 'string') : undefined)
      : normalizedTextList(account.supportedModels),
    modelMappings: hasOwnRecordKey(update, 'modelMappings') ? update.modelMappings : account.modelMappings,
    concurrencyLimit: hasOwnRecordKey(update, 'concurrencyLimit') && typeof update.concurrencyLimit === 'number'
      ? update.concurrencyLimit
      : account.concurrencyLimit,
    priority: hasOwnRecordKey(update, 'priority') && typeof update.priority === 'number'
      ? update.priority
      : account.priority,
    superPriorityEnabled: hasOwnRecordKey(update, 'superPriorityEnabled') && typeof update.superPriorityEnabled === 'boolean'
      ? update.superPriorityEnabled
      : account.superPriorityEnabled,
    fallbackEnabled: hasOwnRecordKey(update, 'fallbackEnabled') && typeof update.fallbackEnabled === 'boolean'
      ? update.fallbackEnabled
      : account.fallbackEnabled,
    proxyProfileId: hasOwnRecordKey(update, 'proxyProfileId') ? optionalText(update.proxyProfileId) : optionalText(account.proxyProfileId),
    groupId,
    accountExpiresAt: hasOwnRecordKey(update, 'accountExpiresAt') ? optionalText(update.accountExpiresAt) : optionalText(account.accountExpiresAt),
    availabilitySchedule: hasOwnRecordKey(update, 'availabilitySchedule')
      ? credentialsRecordValue(update.availabilitySchedule) ?? null
      : account.availabilitySchedule as unknown as Record<string, unknown> | undefined,
    notes: hasOwnRecordKey(update, 'notes') ? optionalText(update.notes) : optionalText(account.notes)
  }
}

function accountUpdateGroupId(account: AccountSummary, update: Record<string, unknown>): string | undefined {
  return hasOwnRecordKey(update, 'groupId') ? optionalText(update.groupId) : optionalText(account.boundGroupId)
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
