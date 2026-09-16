import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { CurrentUserSummary, UsageStatsWindow } from '@/types/domain'
import { clearUsageStatsWindowCache, didUsageStatsWindowLoadFail, useUsageStatsWindow } from './useUsageStatsWindow'

vi.mock('@/api/client', () => ({
  api: {
    stats: { usageWindow: vi.fn() },
    myStats: { usageWindow: vi.fn() }
  }
}))

vi.mock('@/composables/useAuth', () => ({
  authState: {
    currentUser: { value: undefined as CurrentUserSummary | undefined },
    revision: { value: 0 }
  }
}))

function window30Days(): UsageStatsWindow {
  return { timezone: 'UTC', startDate: '2026-01-01', endDate: '2026-01-30', days: 30, maxDays: 30 }
}

function user(partial: Partial<CurrentUserSummary> = {}): CurrentUserSummary {
  return { id: 'u1', username: 'alice', displayName: 'Alice', role: 'admin', mustChangePassword: false, ...partial }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date(2026, 0, 15, 12, 0, 0))
  vi.clearAllMocks()
  authState.currentUser.value = undefined
  authState.revision.value = 0
  clearUsageStatsWindowCache()
})

afterEach(() => {
  vi.useRealTimers()
})

// loadUsageStatsWindow 未单独导出，经 useUsageStatsWindow() 返回对象获取。
const { loadUsageStatsWindow } = useUsageStatsWindow()

describe('loadUsageStatsWindow', () => {
  it('默认 self 作用域请求个人统计窗口', async () => {
    const statsWindow = window30Days()
    vi.mocked(api.myStats.usageWindow).mockResolvedValue(statsWindow)
    await expect(loadUsageStatsWindow()).resolves.toBe(statsWindow)
    expect(api.myStats.usageWindow).toHaveBeenCalledTimes(1)
    expect(api.stats.usageWindow).not.toHaveBeenCalled()
  })

  it('admin 作用域请求全局统计窗口', async () => {
    const statsWindow = window30Days()
    vi.mocked(api.stats.usageWindow).mockResolvedValue(statsWindow)
    await expect(loadUsageStatsWindow({ viewScope: 'admin' })).resolves.toBe(statsWindow)
    expect(api.stats.usageWindow).toHaveBeenCalledTimes(1)
  })

  it('TTL 内重复加载使用缓存', async () => {
    vi.mocked(api.myStats.usageWindow).mockResolvedValue(window30Days())
    await loadUsageStatsWindow()
    await loadUsageStatsWindow()
    expect(api.myStats.usageWindow).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(60_001)
    await loadUsageStatsWindow()
    expect(api.myStats.usageWindow).toHaveBeenCalledTimes(2)
  })

  it('force 绕过缓存强制刷新', async () => {
    vi.mocked(api.myStats.usageWindow).mockResolvedValue(window30Days())
    await loadUsageStatsWindow()
    await loadUsageStatsWindow({ force: true })
    expect(api.myStats.usageWindow).toHaveBeenCalledTimes(2)
  })

  it('请求失败时返回 31 天回退窗口并标记失败', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.myStats.usageWindow).mockRejectedValue(new Error('network down'))
    const fallback = await loadUsageStatsWindow()
    expect(fallback.maxDays).toBe(31)
    expect(fallback.days).toBe(31)
    expect(fallback.endDate).toBe('2026-01-15')
    expect(didUsageStatsWindowLoadFail('self')).toBe(true)
    expect(didUsageStatsWindowLoadFail('admin')).toBe(false)
  })

  it('身份变化后缓存失效重新请求', async () => {
    vi.mocked(api.myStats.usageWindow).mockResolvedValue(window30Days())
    await loadUsageStatsWindow()
    authState.currentUser.value = user()
    await loadUsageStatsWindow()
    expect(api.myStats.usageWindow).toHaveBeenCalledTimes(2)
  })

  it('clearUsageStatsWindowCache 后重新请求', async () => {
    vi.mocked(api.myStats.usageWindow).mockResolvedValue(window30Days())
    await loadUsageStatsWindow()
    clearUsageStatsWindowCache()
    await loadUsageStatsWindow()
    expect(api.myStats.usageWindow).toHaveBeenCalledTimes(2)
  })

  it('请求失败后再次加载会重试', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    vi.mocked(api.myStats.usageWindow)
      .mockRejectedValueOnce(new Error('network down'))
      .mockResolvedValueOnce(window30Days())
    await loadUsageStatsWindow()
    await loadUsageStatsWindow()
    expect(didUsageStatsWindowLoadFail('self')).toBe(false)
  })
})

describe('useUsageStatsWindow', () => {
  it('computed 反映已加载窗口与回退默认', async () => {
    const { usageStatsWindow, usageStatsWindowEndDate, usageStatsWindowMaxDays, loadUsageStatsWindow: load } = useUsageStatsWindow()
    expect(usageStatsWindow.value).toBeUndefined()
    expect(usageStatsWindowEndDate.value).toBeUndefined()
    expect(usageStatsWindowMaxDays.value).toBe(31)

    const statsWindow = { ...window30Days(), endDate: '2026-01-30', maxDays: 45 }
    vi.mocked(api.myStats.usageWindow).mockResolvedValue(statsWindow)
    await load()
    expect(usageStatsWindow.value).toEqual(statsWindow)
    expect(usageStatsWindowEndDate.value?.format('YYYY-MM-DD')).toBe('2026-01-30')
    expect(usageStatsWindowMaxDays.value).toBe(45)
  })
})
