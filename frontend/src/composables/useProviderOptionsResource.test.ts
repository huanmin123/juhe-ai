import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import type { ProviderDefinition, ProviderListItem, ProviderOption } from '@/types/domain'
import { loadProviderOptionsResource } from './useProviderOptionsResource'

vi.mock('@/api/client', () => ({
  api: {
    providers: {
      list: vi.fn(),
      listItems: vi.fn(),
      options: vi.fn(),
      definitions: vi.fn()
    }
  }
}))

function definition(partial: Partial<ProviderDefinition>): ProviderDefinition {
  return {
    id: 'p1',
    code: 'gpt',
    name: 'GPT',
    enabled: true,
    defaultProtocolProfileId: 'profile',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    protocolProfiles: [],
    baseUrl: '',
    ...partial
  } as ProviderDefinition
}

function listItem(partial: Partial<ProviderListItem> = {}): ProviderListItem {
  return {
    id: 'p1',
    code: 'gpt',
    name: 'GPT',
    enabled: true,
    protocolCode: 'openai',
    baseUrl: 'https://base',
    ...partial
  } as ProviderListItem
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('loadProviderOptionsResource 分支选择', () => {
  it('默认走 options 接口并转换为定义结构', async () => {
    const option: ProviderOption = { id: 'p1', code: 'gpt', name: 'GPT', enabled: true }
    vi.mocked(api.providers.options).mockResolvedValue([option])
    const result = await loadProviderOptionsResource({ isManagementView: false })
    expect(api.providers.options).toHaveBeenCalledWith(undefined)
    expect(result).toEqual({
      state: 'ready',
      data: [{
        id: 'p1',
        code: 'gpt',
        name: 'GPT',
        enabled: true,
        defaultProtocolProfileId: '',
        protocolCode: '',
        protocolVersion: '',
        baseUrl: '',
        defaultHealthCheckModel: '',
        defaultSupportedModels: [],
        accountTypes: [],
        capabilities: [],
        protocolProfiles: []
      }]
    })
  })

  it('includeDefinitions 时走 definitions 接口', async () => {
    const data = [definition({ id: 'p2' })]
    vi.mocked(api.providers.definitions).mockResolvedValue(data)
    await loadProviderOptionsResource({ isManagementView: false, includeDefinitions: true })
    expect(api.providers.definitions).toHaveBeenCalledWith(undefined)
    expect(api.providers.options).not.toHaveBeenCalled()
  })

  it('listItemsOnly 时走 listItems 接口并补齐空协议字段', async () => {
    vi.mocked(api.providers.listItems).mockResolvedValue([listItem()])
    const result = await loadProviderOptionsResource({ isManagementView: false, listItemsOnly: true })
    expect(result.data[0]).toMatchObject({
      id: 'p1',
      code: 'gpt',
      defaultProtocolProfileId: '',
      protocolVersion: '',
      protocolProfiles: []
    })
  })

  it('管理视图且 includeDisabled 时走 list 接口', async () => {
    const data = [definition({ id: 'p3', enabled: false })]
    vi.mocked(api.providers.list).mockResolvedValue(data)
    const result = await loadProviderOptionsResource({ isManagementView: true, includeDisabled: true })
    expect(api.providers.list).toHaveBeenCalledWith(undefined)
    expect(result.data).toEqual(data)
  })

  it('管理视图、includeDisabled 且 listItemsOnly 时走 listItems 接口', async () => {
    vi.mocked(api.providers.listItems).mockResolvedValue([listItem({ enabled: false })])
    const result = await loadProviderOptionsResource({ isManagementView: true, includeDisabled: true, listItemsOnly: true })
    expect(api.providers.listItems).toHaveBeenCalledWith(undefined)
    expect(result.data[0].enabled).toBe(false)
  })

  it('非管理视图即使 includeDisabled 也不走 list 接口', async () => {
    vi.mocked(api.providers.options).mockResolvedValue([])
    await loadProviderOptionsResource({ isManagementView: false, includeDisabled: true })
    expect(api.providers.list).not.toHaveBeenCalled()
    expect(api.providers.options).toHaveBeenCalled()
  })

  it('systemAccountId / viewScope 组装请求参数', async () => {
    vi.mocked(api.providers.options).mockResolvedValue([])
    await loadProviderOptionsResource({ isManagementView: true, systemAccountId: 'u1', viewScope: 'admin' })
    expect(api.providers.options).toHaveBeenCalledWith({ systemAccountId: 'u1', viewScope: 'admin' })
  })
})

describe('loadProviderOptionsResource 应用回调', () => {
  it('默认调用 apply 传入结果', async () => {
    vi.mocked(api.providers.options).mockResolvedValue([])
    const apply = vi.fn()
    await loadProviderOptionsResource({ isManagementView: false, apply })
    expect(apply).toHaveBeenCalledWith([])
  })

  it('isCurrent 返回 false 时不调用 apply', async () => {
    vi.mocked(api.providers.options).mockResolvedValue([])
    const apply = vi.fn()
    const result = await loadProviderOptionsResource({ isManagementView: false, apply, isCurrent: () => false })
    expect(result.state).toBe('ready')
    expect(apply).not.toHaveBeenCalled()
  })
})
