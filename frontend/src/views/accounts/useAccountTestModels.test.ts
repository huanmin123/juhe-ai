import { computed, reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AccountListItem } from '@/types/domain'
import type { AccountDraftTestPayload } from '@/api/client'
import { api } from '@/api/client'
import type { AccountTestOptions } from '@/api/domains/accounts'
import type { AccountTestForm } from './accountTestFlow'
import { useAccountTestModels } from './useAccountTestModels'

vi.mock('@/api/client', () => ({
  api: {
    accounts: {
      testOptions: vi.fn(),
      testDraftOptions: vi.fn()
    },
    myAccounts: {
      testOptions: vi.fn(),
      testDraftOptions: vi.fn()
    }
  }
}))

const accountsTestDraftOptions = vi.mocked(api.accounts.testDraftOptions)
const myAccountsTestDraftOptions = vi.mocked(api.myAccounts.testDraftOptions)

function draftAccountListItem(overrides: Partial<AccountListItem> = {}): AccountListItem {
  return {
    id: 'draft:1700000000000',
    providerCode: 'gpt',
    name: '草稿测试账户',
    type: 'api_key',
    status: 'active',
    systemAccountId: 'sys-owner',
    healthCheckModel: 'gpt-5.6-sol',
    healthCheckEndpointMode: 'chat_json',
    ...overrides
  } as AccountListItem
}

function draftPayloadFixture(): AccountDraftTestPayload['account'] {
  return {
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile-openai-v1',
    name: '草稿测试账户',
    type: 'api_key',
    credentials: {},
    concurrencyLimit: 5,
    priority: 0,
    supportedModels: ['gpt-5.6-sol'],
    healthCheckModel: 'gpt-5.6-sol',
    healthCheckEndpointMode: 'chat_json',
    modelMappings: [],
    groupId: 'grp-1'
  }
}

function setupTestModels(options: {
  isManagementView?: boolean
  scopeParams?: { systemAccountId: string }
} = {}) {
  const testForm = reactive<AccountTestForm>({ model: '', testEndpointMode: 'account_default' })
  const hook = useAccountTestModels({
    accountScopeParams: computed(() => options.scopeParams),
    isManagementView: computed(() => options.isManagementView ?? true),
    testForm
  })
  return { hook, testForm }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('useAccountTestModels 草稿测试选项', () => {
  it('initializeDraftTestOptions 播种默认模型与请求形态，且不置只读', () => {
    const { hook, testForm } = setupTestModels()
    hook.initializeDraftTestOptions(
      draftAccountListItem(),
      draftPayloadFixture(),
      ' gpt-5.6-sol ',
      ['chat_sse', 'chat_json', 'chat_sse'],
      'create'
    )
    expect(hook.testModelOptions.value).toEqual([
      { label: 'gpt-5.6-sol', value: 'gpt-5.6-sol', testEndpointModes: ['chat_sse', 'chat_json'] }
    ])
    expect(hook.testEndpointModes.value).toEqual(['chat_sse', 'chat_json'])
    expect(testForm.model).toBe('gpt-5.6-sol')
    expect(testForm.testEndpointMode).toBe('chat_sse')
    expect(hook.testModelsReady.value).toBe(true)
  })

  it('loadDraftTestModelOptions 合并远程选项，默认模型置顶并合并请求形态', async () => {
    accountsTestDraftOptions.mockResolvedValueOnce([
      { id: 'gpt-5.6-mini', name: 'Mini', testEndpointModes: ['chat_json'] },
      { id: 'gpt-5.6-sol', name: 'GPT 5.6 Sol', testEndpointModes: ['chat_sse', 'responses_sse'] }
    ])
    const scopeParams = { systemAccountId: 'sys-list' }
    const { hook, testForm } = setupTestModels({ scopeParams })
    const payload = draftPayloadFixture()
    hook.initializeDraftTestOptions(
      draftAccountListItem(),
      payload,
      'gpt-5.6-sol',
      ['chat_json'],
      'create'
    )
    await hook.loadDraftTestModelOptions()
    expect(accountsTestDraftOptions).toHaveBeenCalledTimes(1)
    expect(accountsTestDraftOptions).toHaveBeenCalledWith(
      { account: payload, keyword: undefined, limit: 50, selectedIds: ['gpt-5.6-sol'] },
      scopeParams,
      { signal: expect.any(AbortSignal) }
    )
    expect(hook.testModelOptions.value).toEqual([
      {
        label: 'gpt-5.6-sol',
        value: 'gpt-5.6-sol',
        testEndpointModes: ['chat_json', 'chat_sse', 'responses_sse']
      },
      { label: 'Mini', value: 'gpt-5.6-mini', testEndpointModes: ['chat_json'] }
    ])
    expect(testForm.model).toBe('gpt-5.6-sol')
    expect(testForm.testEndpointMode).toBe('chat_json')
    expect(hook.testModelsError.value).toBe('')
    expect(hook.testModelsReady.value).toBe(true)
  })

  it('草稿账户可通过 updateSelectableTestModel 切换模型并联动请求形态', async () => {
    accountsTestDraftOptions.mockResolvedValueOnce([
      { id: 'gpt-5.6-mini', name: 'Mini', testEndpointModes: ['responses_json'] }
    ])
    const { hook, testForm } = setupTestModels()
    hook.initializeDraftTestOptions(
      draftAccountListItem(),
      draftPayloadFixture(),
      'gpt-5.6-sol',
      ['chat_json'],
      'create'
    )
    await hook.loadDraftTestModelOptions()
    hook.updateSelectableTestModel('gpt-5.6-mini')
    expect(testForm.model).toBe('gpt-5.6-mini')
    expect(testForm.testEndpointMode).toBe('responses_json')
    expect(hook.testEndpointModes.value).toEqual(['responses_json'])
  })

  it('saved 草稿按账户所属系统账号计算管理面作用域', async () => {
    accountsTestDraftOptions.mockResolvedValueOnce([])
    const { hook } = setupTestModels({ scopeParams: { systemAccountId: 'sys-list' } })
    hook.initializeDraftTestOptions(
      draftAccountListItem({ systemAccountId: 'sys-owner' }),
      draftPayloadFixture(),
      'gpt-5.6-sol',
      ['chat_json'],
      'saved'
    )
    await hook.loadDraftTestModelOptions()
    expect(accountsTestDraftOptions).toHaveBeenCalledWith(
      expect.anything(),
      { systemAccountId: 'sys-owner' },
      expect.anything()
    )
  })

  it('个人视角走 myAccounts.testDraftOptions 并携带关键字', async () => {
    myAccountsTestDraftOptions.mockResolvedValueOnce([])
    const { hook } = setupTestModels({ isManagementView: false })
    const payload = draftPayloadFixture()
    hook.initializeDraftTestOptions(
      draftAccountListItem(),
      payload,
      'gpt-5.6-sol',
      ['chat_json'],
      'create'
    )
    await hook.loadDraftTestModelOptions(' mini ')
    expect(myAccountsTestDraftOptions).toHaveBeenCalledWith(
      { account: payload, keyword: 'mini', limit: 50, selectedIds: ['gpt-5.6-sol'] },
      { signal: expect.any(AbortSignal) }
    )
    expect(accountsTestDraftOptions).not.toHaveBeenCalled()
  })

  it('选项加载失败时保留播种的默认模型并记录错误', async () => {
    accountsTestDraftOptions.mockRejectedValueOnce(new Error('上游目录不可用'))
    const { hook } = setupTestModels()
    hook.initializeDraftTestOptions(
      draftAccountListItem(),
      draftPayloadFixture(),
      'gpt-5.6-sol',
      ['chat_json'],
      'create'
    )
    await expect(hook.loadDraftTestModelOptions()).rejects.toThrow('上游目录不可用')
    expect(hook.testModelsError.value).toBe('上游目录不可用')
    expect(hook.testModelOptions.value).toEqual([
      { label: 'gpt-5.6-sol', value: 'gpt-5.6-sol', testEndpointModes: ['chat_json'] }
    ])
  })

  it('相同关键字重复加载被去重，不再发起请求', async () => {
    accountsTestDraftOptions.mockResolvedValue([
      { id: 'gpt-5.6-sol', name: 'GPT 5.6 Sol', testEndpointModes: ['chat_json'] }
    ])
    const { hook } = setupTestModels()
    hook.initializeDraftTestOptions(
      draftAccountListItem(),
      draftPayloadFixture(),
      'gpt-5.6-sol',
      ['chat_json'],
      'create'
    )
    await hook.loadDraftTestModelOptions()
    await hook.loadDraftTestModelOptions()
    expect(accountsTestDraftOptions).toHaveBeenCalledTimes(1)
  })

  it('重新初始化草稿后，旧请求的响应不再写入当前选项', async () => {
    let resolveFirst: (value: AccountTestOptions) => void = () => undefined
    accountsTestDraftOptions.mockImplementationOnce(
      () => new Promise<AccountTestOptions>((resolve) => {
        resolveFirst = resolve
      })
    )
    const { hook, testForm } = setupTestModels()
    const payload = draftPayloadFixture()
    hook.initializeDraftTestOptions(
      draftAccountListItem({ id: 'draft:1' }),
      payload,
      'gpt-5.6-sol',
      ['chat_json'],
      'create'
    )
    const pending = hook.loadDraftTestModelOptions()
    hook.initializeDraftTestOptions(
      draftAccountListItem({ id: 'draft:2' }),
      payload,
      'claude-opus-4',
      ['messages_json'],
      'create'
    )
    resolveFirst([{ id: 'gpt-5.6-sol', name: 'GPT 5.6 Sol', testEndpointModes: ['chat_json'] }])
    await pending
    expect(hook.testModelOptions.value).toEqual([
      { label: 'claude-opus-4', value: 'claude-opus-4', testEndpointModes: ['messages_json'] }
    ])
    expect(testForm.model).toBe('claude-opus-4')
  })
})
