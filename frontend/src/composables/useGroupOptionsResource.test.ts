import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { RouteStrategyGroupOption } from '@/types/domain'
import { loadGroupOptionsResource } from './useGroupOptionsResource'

function group(id: string, name: string): RouteStrategyGroupOption {
  return { id, name, providerCode: 'openai', enabled: true }
}

function createApi() {
  return {
    routeStrategyOptions: vi.fn()
  }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('loadGroupOptionsResource', () => {
  it('按窗口参数加载分组选项并应用', async () => {
    const api = createApi()
    api.routeStrategyOptions.mockResolvedValue([group('g1', '分组一')])
    const apply = vi.fn()
    const result = await loadGroupOptionsResource({
      api,
      apply,
      isManagementView: true,
      systemAccountId: 'u1'
    })
    expect(result).toEqual([group('g1', '分组一')])
    expect(apply).toHaveBeenCalledWith(result)
    expect(api.routeStrategyOptions).toHaveBeenCalledTimes(1)
    expect(api.routeStrategyOptions).toHaveBeenCalledWith({ keyword: undefined, limit: 50, systemAccountId: 'u1' })
  })

  it('keyword 空白归一化为 undefined', async () => {
    const api = createApi()
    api.routeStrategyOptions.mockResolvedValue([])
    await loadGroupOptionsResource({ api, apply: vi.fn(), isManagementView: true, keyword: '   ' })
    expect(api.routeStrategyOptions).toHaveBeenCalledWith({ keyword: undefined, limit: 50, systemAccountId: undefined })
  })

  it('keyword 保留有效检索词', async () => {
    const api = createApi()
    api.routeStrategyOptions.mockResolvedValue([])
    await loadGroupOptionsResource({ api, apply: vi.fn(), isManagementView: true, keyword: ' 关键词 ' })
    expect(api.routeStrategyOptions).toHaveBeenCalledWith({ keyword: '关键词', limit: 50, systemAccountId: undefined })
  })

  it('窗口内缺失的选中项触发按 ids 的补拉并合并', async () => {
    const api = createApi()
    api.routeStrategyOptions.mockResolvedValueOnce([group('g1', '分组一')])
      .mockResolvedValueOnce([group('g2', '分组二'), group('g3', '分组三')])
    const result = await loadGroupOptionsResource({
      api,
      apply: vi.fn(),
      isManagementView: true,
      selectedIds: ['g2', ' g3 ', 'g9', '', '  ', 'g2'],
      selectedOptions: [group('g9', '已知选中'), group('g8', '不在 selectedIds 中的选中项')]
    })
    expect(api.routeStrategyOptions).toHaveBeenCalledTimes(2)
    expect(api.routeStrategyOptions).toHaveBeenLastCalledWith({ ids: ['g2', 'g3'], limit: 2, systemAccountId: undefined })
    // 补拉结果在前、已知选中居中、窗口项最后；未列入 selectedIds 的选中项被排除。
    expect(result.map((item) => item.id)).toEqual(['g2', 'g3', 'g9', 'g1'])
  })

  it('选中项全部在窗口内时不发起补拉', async () => {
    const api = createApi()
    api.routeStrategyOptions.mockResolvedValue([group('g1', '分组一'), group('g2', '分组二')])
    const result = await loadGroupOptionsResource({
      api,
      apply: vi.fn(),
      isManagementView: true,
      selectedIds: ['g1']
    })
    expect(api.routeStrategyOptions).toHaveBeenCalledTimes(1)
    expect(result.map((item) => item.id)).toEqual(['g1', 'g2'])
  })

  it('isCurrent 返回 false 时不应用结果但仍返回数据', async () => {
    const api = createApi()
    api.routeStrategyOptions.mockResolvedValue([group('g1', '分组一')])
    const apply = vi.fn()
    const result = await loadGroupOptionsResource({
      api,
      apply,
      isCurrent: () => false,
      isManagementView: true
    })
    expect(result).toEqual([group('g1', '分组一')])
    expect(apply).not.toHaveBeenCalled()
  })
})
