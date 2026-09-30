import { computed } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { GPT_OPENAI_V1_PROFILE_ID, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '@/shared/providerProtocol'
import type {
  AccountAdvancedDetail,
  AccountCredentials,
  AccountEditBasicDetail,
  AccountListItem
} from '@/types/domain'

import { CREDENTIAL_CIPHER_PLACEHOLDER } from './accountCredentials'
import { useAccountEditForm } from './useAccountEditForm'

vi.mock('@/api/client', () => ({
  api: {
    myAccounts: {
      editBasicDetail: vi.fn(),
      advancedDetail: vi.fn(),
      revealCredentials: vi.fn(),
      apiKeyRuntime: vi.fn(),
      update: vi.fn()
    }
  }
}))

vi.mock('@/lib/antd', () => ({
  message: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn()
  }
}))

const messageInfo = vi.mocked(message.info)
const messageWarning = vi.mocked(message.warning)

const ACCOUNT_ID = 'acct-race-1'
const PH = CREDENTIAL_CIPHER_PLACEHOLDER

const listItem: AccountListItem = {
  id: ACCOUNT_ID,
  providerCode: 'gpt',
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  name: 'reveal 竞态账户',
  type: 'api_key',
  status: 'active',
  concurrencyLimit: 20,
  currentConcurrency: 0,
  priority: 50,
  superPriorityEnabled: false,
  fallbackEnabled: false,
  clientCompatibility: 'openai_standard',
  healthCheckModel: 'gpt-5.5',
  healthCheckEndpointMode: 'chat_json',
  schedulable: true,
  todayUsage: { requestCount: 0, totalTokens: 0, totalCost: 0 }
}

const maskedBasicDetail: AccountEditBasicDetail = {
  id: ACCOUNT_ID,
  configRevision: 5,
  ownerSystemAccountId: 'sys-1',
  providerCode: 'gpt',
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  protocolCode: OPENAI_PROTOCOL_CODE,
  protocolVersion: OPENAI_PROTOCOL_VERSION,
  name: 'reveal 竞态账户',
  type: 'api_key',
  credentials: {
    api_key: PH,
    base_url: 'https://api.openai.com/v1',
    supported_endpoint_modes: ['chat_json', 'chat_sse']
  },
  credentialsMasked: true,
  status: 'active',
  concurrencyLimit: 20,
  priority: 50,
  superPriorityEnabled: false,
  fallbackEnabled: false,
  clientCompatibility: 'openai_standard',
  supportedModels: ['gpt-5.5'],
  tags: [],
  healthCheckModel: 'gpt-5.5',
  healthCheckEndpointMode: 'chat_json',
  boundGroupId: 'grp-1',
  boundGroupName: '默认分组'
}

const ownerAdvancedDetail: AccountAdvancedDetail = {
  id: ACCOUNT_ID,
  configRevision: 5,
  accessType: 'owner',
  modelMappings: [],
  temporaryUnavailableContinuousProbeEnabled: true,
  balanceQueryEnabled: false
}

const revealedCredentials: AccountCredentials = {
  api_key: 'sk-real-single',
  base_url: 'https://api.openai.com/v1',
  supported_endpoint_modes: ['chat_json', 'chat_sse']
}

function createEditForm() {
  return useAccountEditForm({
    accountScopeParams: computed(() => undefined),
    accounts: { value: [listItem] },
    extractApiErrorMessage: (_error: unknown, fallback: string) => fallback,
    groupIdForAccount: () => undefined,
    groups: { value: [] },
    isManagementView: computed(() => false),
    ensureProviderDefinition: vi.fn(async () => undefined),
    loadGroupOptions: vi.fn(async () => {}),
    loadData: vi.fn(async () => {}),
    refreshAccountMutationRows: vi.fn(async () => {}),
    providerDefinitions: { value: [] },
    providers: { value: [] },
    systemAccounts: { value: [] }
  })
}

/** 走完"reveal 先返回、高级配置后返回"的交错序列，返回竞态完成后的编辑表单。 */
async function openWithRevealBeforeAdvanced() {
  vi.mocked(api.myAccounts.editBasicDetail).mockResolvedValue(maskedBasicDetail)
  vi.mocked(api.myAccounts.revealCredentials).mockResolvedValue({
    id: ACCOUNT_ID,
    configRevision: 5,
    credentials: revealedCredentials
  })
  let resolveAdvanced!: (value: AccountAdvancedDetail) => void
  const advancedResponse = new Promise<AccountAdvancedDetail>((resolve) => {
    resolveAdvanced = resolve
  })
  vi.mocked(api.myAccounts.advancedDetail).mockReturnValue(advancedResponse)

  const editForm = createEditForm()
  await editForm.openEdit(listItem)
  expect(editForm.form.apiKey).toBe(PH)

  // 高级配置加载挂起时 reveal 先完成
  const advancedLoad = editForm.loadAdvancedAccountDetail()
  await editForm.revealAccountCredentials()
  expect(editForm.form.apiKey).toBe('sk-real-single')

  resolveAdvanced(ownerAdvancedDetail)
  await expect(advancedLoad).resolves.toBe(true)
  return editForm
}

describe('useAccountEditForm reveal/高级配置加载竞态（BUG-0243 问题 5a）', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('reveal 先返回、高级配置后返回：表单保留明文，基线含真实 Key，未修改保存不重提交明文', async () => {
    const editForm = await openWithRevealBeforeAdvanced()

    // 高级配置加载（preserve）保留已 reveal 的明文，不重置回占位态
    expect(editForm.form.apiKey).toBe('sk-real-single')
    expect(editForm.credentialsRevealed.value).toBe(true)

    await editForm.saveAccount()

    // 基线由当前表单（含真实 Key）重建：未修改保存走"未检测到账户修改"，
    // 不发起更新请求，真实凭据不出站（缺陷形态下 update 会携带明文 api_key）
    expect(api.myAccounts.update).not.toHaveBeenCalled()
    expect(messageInfo).toHaveBeenCalledWith('未检测到账户修改')
    expect(messageWarning).not.toHaveBeenCalled()
  })

  it('竞态后仅修改非凭据字段：更新 diff 只含该字段，不含明文凭据', async () => {
    const editForm = await openWithRevealBeforeAdvanced()

    editForm.form.notes = 'race-adjusted-notes'
    vi.mocked(api.myAccounts.update).mockResolvedValue({
      id: ACCOUNT_ID,
      configRevision: 6,
      changedFields: ['notes']
    })

    await editForm.saveAccount()

    expect(api.myAccounts.update).toHaveBeenCalledTimes(1)
    const updatePayload = vi.mocked(api.myAccounts.update).mock.calls[0]?.[1] as
      | { notes?: string; credentialsPatch?: Record<string, unknown> }
      | undefined
    expect(updatePayload).toBeDefined()
    // 基线凭据部分与表单一致：非凭据修改不携带 credentialsPatch，明文 Key 不出站
    expect(updatePayload?.credentialsPatch).toBeUndefined()
    expect(JSON.stringify(updatePayload)).not.toContain('sk-real-single')
    expect(updatePayload?.notes).toBe('race-adjusted-notes')
  })
})
