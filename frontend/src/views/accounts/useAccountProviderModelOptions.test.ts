import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, ref } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelOption } from '@/types/domain'
import { accountModelOptionsTruncated, ACCOUNT_MODEL_OPTIONS_LIMIT, useAccountProviderModelOptions } from './useAccountProviderModelOptions'

vi.mock('@/api/client', () => ({
  api: {
    providers: {
      modelOptions: vi.fn()
    }
  }
}))

vi.mock('@/lib/antd', () => ({
  message: {
    error: vi.fn()
  }
}))

function modelRow(id: string): ProviderModelOption {
  return {
    id,
    name: id,
    supportedApiProtocols: ['chat_completions'],
    supportedServiceTiers: [],
    supportedReasoningEfforts: []
  }
}

function fullWindow() {
  return Array.from({ length: ACCOUNT_MODEL_OPTIONS_LIMIT }, (_item, index) => modelRow(`model-${String(index + 1).padStart(3, '0')}`))
}

function setup() {
  const providerCode = ref('gpt')
  let result!: ReturnType<typeof useAccountProviderModelOptions>
  const scope = effectScope()
  scope.run(() => {
    result = useAccountProviderModelOptions({
      currentProviderCode: () => providerCode.value,
      extractApiErrorMessage: () => '加载供应商模型失败',
      isManagementView: computed(() => false),
      modelScopeParams: computed(() => undefined)
    })
  })
  return { ...result, providerCode }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.providers.modelOptions).mockResolvedValue([])
})

describe('accountModelOptionsTruncated', () => {
  it('返回条数达到 50 条窗口判定为截断，未达到为 false', () => {
    expect(ACCOUNT_MODEL_OPTIONS_LIMIT).toBe(50)
    expect(accountModelOptionsTruncated(50)).toBe(true)
    expect(accountModelOptionsTruncated(67)).toBe(true)
    expect(accountModelOptionsTruncated(49)).toBe(false)
    expect(accountModelOptionsTruncated(0)).toBe(false)
  })
})

describe('useAccountProviderModelOptions 截断标志', () => {
  it('浏览取数（无 keyword、无 selectedIds）返回满窗口 → 截断标志为真', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow())
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(true)
    expect(hook.providerModelOptions.value).toHaveLength(ACCOUNT_MODEL_OPTIONS_LIMIT)
  })

  it('浏览取数未达窗口 → 截断标志为假', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue([modelRow('model-1')])
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
  })

  it('带 keyword 的搜索不参与判定：截断标志保持浏览取数的结果', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow())
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(true)
    vi.mocked(api.providers.modelOptions).mockResolvedValue([modelRow('model-keyword-hit')])
    await hook.loadProviderModelOptions('gpt', { keyword: 'keyword-hit' })
    expect(hook.providerModelOptionsTruncated.value).toBe(true)
  })

  it('selectedIds 补拉批次不参与判定：无浏览取数时标志保持假', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow())
    const hook = setup()
    await hook.loadProviderModelOptions('gpt', { selectedIds: ['model-001'] })
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
  })

  it('reset 与 scope 切换清空截断标志', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow())
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(true)
    hook.resetProviderModelOptions()
    expect(hook.providerModelOptionsTruncated.value).toBe(false)

    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(true)
    hook.providerCode.value = 'anthropic'
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
  })
})
