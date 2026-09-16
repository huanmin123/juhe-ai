import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { removeLocalSelectPreferenceValues } from '@/shared/selectLocalPreferenceCache'
import type { SystemAccountPrincipalSummary } from '@/types/domain'
import { useRemoteSystemAccountOptions } from './useRemoteSystemAccountOptions'

vi.mock('@/api/client', () => ({
  api: {
    systemAccounts: {
      options: vi.fn()
    }
  }
}))

vi.mock('@/lib/antd', () => ({
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() }
}))

vi.mock('@/shared/selectLocalPreferenceCache', () => ({
  removeLocalSelectPreferenceValues: vi.fn()
}))

function account(id: string): SystemAccountPrincipalSummary {
  return { id, username: `user-${id}`, displayName: `账户-${id}`, status: 'active' } as SystemAccountPrincipalSummary
}

function setup(config: Parameters<typeof useRemoteSystemAccountOptions>[0] = {}) {
  let result!: ReturnType<typeof useRemoteSystemAccountOptions>
  effectScope().run(() => {
    result = useRemoteSystemAccountOptions(config)
  })
  return result
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  vi.mocked(api.systemAccounts.options).mockResolvedValue([])
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('useRemoteSystemAccountOptions.load', () => {
  it('加载系统账户选项并处理关键词', async () => {
    vi.mocked(api.systemAccounts.options).mockResolvedValue([account('a1')])
    const list = setup()
    await list.load(' 关键词 ')
    expect(api.systemAccounts.options).toHaveBeenCalledWith({ keyword: '关键词', limit: 50 })
    expect(list.systemAccounts.value.map((option) => option.id)).toEqual(['a1'])
    expect(list.loading.value).toBe(false)
  })

  it('limit 钳制到 1-50', async () => {
    const list = setup({ limit: 0 })
    await list.load()
    expect(api.systemAccounts.options).toHaveBeenCalledWith({ keyword: undefined, limit: 1 })
    const big = setup({ limit: 100 })
    await big.load()
    expect(api.systemAccounts.options).toHaveBeenLastCalledWith({ keyword: undefined, limit: 50 })
  })

  it('enabled 返回 false 时清空且不发请求', async () => {
    const list = setup({ enabled: () => false })
    list.systemAccounts.value = [account('a1')]
    await list.load()
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    expect(list.systemAccounts.value).toEqual([])
  })

  it('请求失败提示错误文案', async () => {
    vi.mocked(api.systemAccounts.options).mockRejectedValue(new Error('boom'))
    const list = setup({ errorMessage: '自定义失败文案' })
    await list.load()
    expect(message.error).toHaveBeenCalledWith('自定义失败文案')

    const fallback = setup()
    await fallback.load()
    expect(message.error).toHaveBeenLastCalledWith('加载系统账户筛选项失败')
  })
})

describe('选中项补全与失效处理', () => {
  it('窗口缺失的选中项按 ids 补拉并合并', async () => {
    vi.mocked(api.systemAccounts.options)
      .mockResolvedValueOnce([account('a1')])
      .mockResolvedValueOnce([account('a2')])
    // selectedIds 归一化会 trim 并丢弃空白项、undefined 与 all 哨兵值。
    const list = setup({ selectedIds: () => ['a2', 'all', undefined, '  '] })
    await list.load()
    expect(api.systemAccounts.options).toHaveBeenLastCalledWith({ ids: ['a2'], limit: 50 })
    expect(list.systemAccounts.value.map((option) => option.id)).toEqual(['a2', 'a1'])
  })

  it('补拉仍找不到的选中项被清理并仅提示一次', async () => {
    vi.mocked(api.systemAccounts.options)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([])
    const onMissingSelectedIds = vi.fn()
    const list = setup({ selectedIds: () => ['ghost'], onMissingSelectedIds, preferenceKeys: () => ['sys-pref-key'] })
    await list.load()
    expect(removeLocalSelectPreferenceValues).toHaveBeenCalledWith('sys-pref-key', ['ghost'])
    expect(onMissingSelectedIds).toHaveBeenCalledWith(['ghost'])
    expect(message.warning).toHaveBeenCalledTimes(1)
    expect(message.warning).toHaveBeenCalledWith('已移除不存在或无权访问的系统账户，请重新选择')

    vi.mocked(api.systemAccounts.options)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([])
    await list.load()
    expect(message.warning).toHaveBeenCalledTimes(1)
  })

  it('补拉失败时保留窗口结果', async () => {
    vi.mocked(api.systemAccounts.options)
      .mockResolvedValueOnce([account('a1')])
      .mockRejectedValueOnce(new Error('ids fetch failed'))
    const list = setup({ selectedIds: () => ['missing'] })
    await list.load()
    expect(list.systemAccounts.value.map((option) => option.id)).toEqual(['a1'])
  })

  it('选中项全部在窗口内时不补拉', async () => {
    vi.mocked(api.systemAccounts.options).mockResolvedValue([account('a1')])
    const list = setup({ selectedIds: () => ['a1'] })
    await list.load()
    expect(api.systemAccounts.options).toHaveBeenCalledTimes(1)
  })
})

describe('搜索与失效', () => {
  it('handleSearch 防抖后按最新关键词加载', async () => {
    vi.useFakeTimers()
    const list = setup({ searchDelayMs: 250 })
    list.handleSearch('ab')
    list.handleSearch('abc')
    vi.advanceTimersByTime(249)
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    await vi.advanceTimersByTimeAsync(0)
    expect(api.systemAccounts.options).toHaveBeenCalledTimes(1)
    expect(api.systemAccounts.options).toHaveBeenCalledWith({ keyword: 'abc', limit: 50 })
  })

  it('resetSearch 清空关键词并取消计时器', async () => {
    vi.useFakeTimers()
    const list = setup()
    list.handleSearch('abc')
    list.resetSearch()
    vi.advanceTimersByTime(1_000)
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    expect(list.keyword.value).toBe('')
  })

  it('handleDropdown 打开时触发加载', async () => {
    const list = setup()
    list.handleDropdown(true)
    await Promise.resolve()
    expect(api.systemAccounts.options).toHaveBeenCalledTimes(1)
    list.handleDropdown(false)
    await Promise.resolve()
    expect(api.systemAccounts.options).toHaveBeenCalledTimes(1)
  })

  it('invalidate 清空加载状态', async () => {
    vi.mocked(api.systemAccounts.options).mockResolvedValue([account('a1')])
    const list = setup()
    await list.load()
    list.invalidate()
    expect(list.loading.value).toBe(false)
    expect(list.keyword.value).toBe('')
  })
})
