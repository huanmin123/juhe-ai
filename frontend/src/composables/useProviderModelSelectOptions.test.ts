import { beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, effectScope } from 'vue'

import { api } from '@/api/client'
import type { ProviderModelOption } from '@/types/domain'
import { filterModelOption, useProviderModelSelectOptions, type ProviderModelSelectOption } from './useProviderModelSelectOptions'

vi.mock('@/api/client', () => ({
  api: {
    providers: {
      modelOptions: vi.fn()
    }
  }
}))

function modelOption(partial: Partial<ProviderModelOption> & { id: string; name: string }): ProviderModelOption {
  return {
    providerCode: 'openai',
    supportedApiProtocols: ['chat_completions'],
    supportedServiceTiers: [],
    supportedReasoningEfforts: [],
    ...partial
  } as ProviderModelOption
}

function setup(options: Parameters<typeof useProviderModelSelectOptions>[0] = {}) {
  let result!: ReturnType<typeof useProviderModelSelectOptions>
  effectScope().run(() => {
    result = useProviderModelSelectOptions(options)
  })
  return result
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.providers.modelOptions).mockResolvedValue([])
})

describe('selectOptions 聚合', () => {
  it('按模型 id 分组并合并供应商与协议，按名称排序', async () => {
    const list = setup()
    list.providerModelOptions.value = [
      modelOption({ id: 'm-b', name: 'Model B', providerCode: 'gpt', supportedApiProtocols: ['chat_completions'] }),
      modelOption({ id: 'm-a', name: 'Model A', providerCode: 'openai', supportedApiProtocols: ['chat_completions'] }),
      modelOption({ id: 'm-a', name: 'Model A', providerCode: 'glm', supportedApiProtocols: ['chat_completions', 'messages'] })
    ]
    expect(list.selectOptions.value).toEqual([
      {
        label: 'Model A（glm、openai）',
        value: 'm-a',
        providerCodes: ['glm', 'openai'],
        supportedApiProtocols: ['chat_completions', 'messages']
      },
      {
        label: 'Model B（gpt）',
        value: 'm-b',
        providerCodes: ['gpt'],
        supportedApiProtocols: ['chat_completions']
      }
    ])
  })

  it('无供应商信息的模型直接用名称作标签', async () => {
    const list = setup()
    list.providerModelOptions.value = [modelOption({ id: 'm-x', name: 'Model X', providerCode: '  ' })]
    expect(list.selectOptions.value).toEqual([
      { label: 'Model X', value: 'm-x', providerCodes: [], supportedApiProtocols: ['chat_completions'] }
    ])
  })

  it('空白 id 或名称的条目被过滤', async () => {
    const list = setup()
    list.providerModelOptions.value = [
      modelOption({ id: ' ', name: 'X' }),
      modelOption({ id: 'y', name: '  ' })
    ]
    expect(list.selectOptions.value).toEqual([])
    expect(list.optionValues.value.size).toBe(0)
  })

  it('hasModel 匹配去除空白后的值', async () => {
    const list = setup()
    list.providerModelOptions.value = [modelOption({ id: 'm-1', name: 'One' })]
    expect(list.hasModel(' m-1 ')).toBe(true)
    expect(list.hasModel('m-2')).toBe(false)
  })
})

describe('filterModelOption', () => {
  const option: ProviderModelSelectOption = {
    label: 'Model A（glm、openai）',
    value: 'm-a',
    providerCodes: ['glm', 'openai'],
    supportedApiProtocols: ['chat_completions']
  }

  it('空关键字匹配全部', () => {
    expect(filterModelOption('', option)).toBe(true)
    expect(filterModelOption('   ', undefined)).toBe(true)
  })

  it('按标签、值与供应商代码大小写不敏感匹配', () => {
    expect(filterModelOption('model', option)).toBe(true)
    expect(filterModelOption('M-A', option)).toBe(true)
    expect(filterModelOption('GLM', option)).toBe(true)
    expect(filterModelOption('anthropic', option)).toBe(false)
  })
})

describe('loadModelOptions', () => {
  it('默认参数请求模型选项', async () => {
    const data = [modelOption({ id: 'm-1', name: 'One' })]
    vi.mocked(api.providers.modelOptions).mockResolvedValue(data)
    const list = setup()
    await list.loadModelOptions()
    expect(api.providers.modelOptions).toHaveBeenCalledWith({ limit: 50 })
    expect(list.providerModelOptions.value).toEqual(data)
    expect(list.loading.value).toBe(false)
    expect(list.loadFailed.value).toBe(false)
  })

  it('同 scope 重复加载命中缓存不再请求', async () => {
    const list = setup()
    await list.loadModelOptions()
    await list.loadModelOptions()
    expect(api.providers.modelOptions).toHaveBeenCalledTimes(1)
  })

  it('force 强制重新加载', async () => {
    const list = setup()
    await list.loadModelOptions()
    await list.loadModelOptions(true)
    expect(api.providers.modelOptions).toHaveBeenCalledTimes(2)
  })

  it('keyword / limit / protocol 组装查询参数并钳制 limit', async () => {
    const list = setup()
    await list.loadModelOptions({ keyword: ' gpt-4 ', limit: 999, selectedIds: ['s1'] })
    expect(api.providers.modelOptions).toHaveBeenCalledWith({ keyword: 'gpt-4', limit: 50, selectedIds: ['s1'] })
    const scoped = setup({ protocol: 'anthropic' })
    await scoped.loadModelOptions({ limit: 0 })
    expect(api.providers.modelOptions).toHaveBeenLastCalledWith({ protocol: 'anthropic', limit: 1 })
  })

  it('scopeParams 合并且单一供应商下沉为 providerCode', async () => {
    const providerCodes = computed(() => [' GLM '])
    const scopeParams = computed(() => ({ systemAccountId: 'u1' }))
    const list = setup({ scopeParams, providerCodes })
    await list.loadModelOptions()
    expect(api.providers.modelOptions).toHaveBeenCalledTimes(1)
    expect(api.providers.modelOptions).toHaveBeenCalledWith({
      systemAccountId: 'u1',
      providerCode: 'GLM',
      limit: 50
    })
  })

  it('多个供应商代码分别请求并按 id 去重（后到供应商覆盖同 id 项）', async () => {
    const providerCodes = computed(() => ['gpt', 'glm', 'gpt'])
    const list = setup({ providerCodes })
    vi.mocked(api.providers.modelOptions).mockImplementation(async (params) => {
      const code = (params as { providerCode?: string }).providerCode
      return code === 'gpt'
        ? [modelOption({ id: 'shared', name: 'Shared', providerCode: 'gpt' })]
        : [modelOption({ id: 'shared', name: 'Shared', providerCode: 'glm' }), modelOption({ id: 'glm-only', name: 'GLM Only', providerCode: 'glm' })]
    })
    await list.loadModelOptions()
    const calls = vi.mocked(api.providers.modelOptions).mock.calls.map((call) => (call[0] as { providerCode?: string }).providerCode)
    expect(calls).toEqual(['glm', 'gpt'])
    expect(list.providerModelOptions.value.map((item) => item.id)).toEqual(['shared', 'glm-only'])
    expect(list.providerModelOptions.value[0].providerCode).toBe('gpt')
  })

  it('selectedIds 超过 50 个时分批请求', async () => {
    const selectedIds = computed(() => Array.from({ length: 101 }, (_, index) => `s-${index}`))
    const list = setup({ selectedIds })
    await list.loadModelOptions()
    const batches = vi.mocked(api.providers.modelOptions).mock.calls.map(
      (call) => (call[0] as { selectedIds?: string[] }).selectedIds?.length ?? 0
    )
    expect(batches).toEqual([50, 50, 1])
  })

  it('请求失败标记 loadFailed 并回调 onLoadError', async () => {
    const onLoadError = vi.fn()
    const failure = new Error('加载失败')
    vi.mocked(api.providers.modelOptions).mockRejectedValue(failure)
    const list = setup({ onLoadError })
    await list.loadModelOptions()
    expect(list.loadFailed.value).toBe(true)
    expect(onLoadError).toHaveBeenCalledWith(failure)
    expect(list.loading.value).toBe(false)
  })

  it('resetModelOptions 清空状态并允许重新加载', async () => {
    const list = setup()
    await list.loadModelOptions()
    expect(list.providerModelOptions.value).toHaveLength(0)
    list.resetModelOptions()
    await list.loadModelOptions()
    expect(api.providers.modelOptions).toHaveBeenCalledTimes(2)
    expect(list.loadFailed.value).toBe(false)
  })
})
