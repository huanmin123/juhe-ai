import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { RouteStrategyOptionSummary } from '@/types/domain'
import { loadRouteStrategyOptionsResource } from './useRouteStrategyOptionsResource'

function strategy(id: string, name: string): RouteStrategyOptionSummary {
  return { id, name, mode: 'normal', status: 'active', isDefault: false }
}

function createApi() {
  return { options: vi.fn() }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('loadRouteStrategyOptionsResource', () => {
  it('按窗口参数加载策略选项并应用', async () => {
    const api = createApi()
    api.options.mockResolvedValue([strategy('s1', '策略一')])
    const apply = vi.fn()
    const result = await loadRouteStrategyOptionsResource({
      api,
      apply,
      isManagementView: true,
      systemAccountId: 'u2',
      keyword: ' 关键词 '
    })
    expect(result).toEqual([strategy('s1', '策略一')])
    expect(apply).toHaveBeenCalledWith(result)
    expect(api.options).toHaveBeenCalledWith({ keyword: '关键词', limit: 50, activeOnly: false, systemAccountId: 'u2' })
  })

  it('keyword 空白归一化为 undefined', async () => {
    const api = createApi()
    api.options.mockResolvedValue([])
    await loadRouteStrategyOptionsResource({ api, apply: vi.fn(), isManagementView: true, keyword: '   ' })
    expect(api.options).toHaveBeenCalledWith({ keyword: undefined, limit: 50, activeOnly: false, systemAccountId: undefined })
  })

  it('窗口缺失的选中项按 ids 补拉并合并（补拉结果在前）', async () => {
    const api = createApi()
    api.options.mockResolvedValueOnce([strategy('s1', '策略一')])
      .mockResolvedValueOnce([strategy('s2', '策略二')])
    const result = await loadRouteStrategyOptionsResource({
      api,
      apply: vi.fn(),
      isManagementView: true,
      selectedIds: [' s2 ', '', 's2', '   ']
    })
    expect(api.options).toHaveBeenCalledTimes(2)
    expect(api.options).toHaveBeenLastCalledWith({ ids: ['s2'], limit: 1, activeOnly: false, systemAccountId: undefined })
    expect(result.map((item) => item.id)).toEqual(['s2', 's1'])
  })

  it('选中项全部命中窗口时不补拉', async () => {
    const api = createApi()
    api.options.mockResolvedValue([strategy('s1', '策略一')])
    await loadRouteStrategyOptionsResource({ api, apply: vi.fn(), isManagementView: true, selectedIds: ['s1'] })
    expect(api.options).toHaveBeenCalledTimes(1)
  })

  it('isCurrent 返回 false 时不应用结果', async () => {
    const api = createApi()
    api.options.mockResolvedValue([strategy('s1', '策略一')])
    const apply = vi.fn()
    const result = await loadRouteStrategyOptionsResource({
      api,
      apply,
      isCurrent: () => false,
      isManagementView: true
    })
    expect(result).toEqual([strategy('s1', '策略一')])
    expect(apply).not.toHaveBeenCalled()
  })
})
