import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope, ref } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelOption } from '@/types/domain'
import type { ProviderModelOptionList } from '@/api/domains/providers'
import { ACCOUNT_MODEL_OPTIONS_LIMIT, useAccountProviderModelOptions } from './useAccountProviderModelOptions'

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

function fullWindow(hasMore?: boolean): ProviderModelOptionList {
  const list = Array.from({ length: ACCOUNT_MODEL_OPTIONS_LIMIT }, (_item, index) => modelRow(`model-${String(index + 1).padStart(3, '0')}`)) as ProviderModelOptionList
  if (hasMore !== undefined) list.hasMore = hasMore
  return list
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

describe('useAccountProviderModelOptions 截断标志（T25 服务端 hasMore 元数据）', () => {
  it('浏览取数携带 hasMore=true → 截断标志为真', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow(true))
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(true)
    expect(hook.providerModelOptions.value).toHaveLength(ACCOUNT_MODEL_OPTIONS_LIMIT)
  })

  it('浏览取数携带 hasMore=false → 截断标志为假', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow(false))
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
  })

  it('元数据缺失（旧响应/纯数组 mock）→ 按无法判定处理，标志保持假', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow())
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
  })

  it('编辑态 selectedIds 单批次补拉（无 keyword）以服务端判定为准', async () => {
    vi.mocked(api.providers.modelOptions).mockImplementation(async () => {
      const list = [modelRow('model-selected')] as ProviderModelOptionList
      list.hasMore = true
      return list
    })
    const hook = setup()
    await hook.loadProviderModelOptions('gpt', { selectedIds: ['model-selected'] })
    expect(hook.providerModelOptionsTruncated.value).toBe(true)
  })

  it('带 keyword 的搜索不参与判定：标志保持最近一次非搜索取数的结果', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow(false))
    const hook = setup()
    await hook.loadProviderModelOptions('gpt')
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
    vi.mocked(api.providers.modelOptions).mockImplementation(async () => {
      const list = [modelRow('model-keyword-hit')] as ProviderModelOptionList
      list.hasMore = true
      return list
    })
    await hook.loadProviderModelOptions('gpt', { keyword: 'keyword-hit' })
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
  })

  it('selectedIds 超窗口切多批次时合并结果无元数据，不改写标志', async () => {
    const selectedIds = Array.from({ length: ACCOUNT_MODEL_OPTIONS_LIMIT + 10 }, (_item, index) => `model-${String(index + 1).padStart(3, '0')}`)
    vi.mocked(api.providers.modelOptions).mockImplementation(async () => {
      const list = [modelRow('model-batch')] as ProviderModelOptionList
      list.hasMore = true
      return list
    })
    const hook = setup()
    await hook.loadProviderModelOptions('gpt', { selectedIds })
    expect(vi.mocked(api.providers.modelOptions)).toHaveBeenCalledTimes(2)
    expect(hook.providerModelOptionsTruncated.value).toBe(false)
  })

  it('reset 与 scope 切换清空截断标志', async () => {
    vi.mocked(api.providers.modelOptions).mockResolvedValue(fullWindow(true))
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
