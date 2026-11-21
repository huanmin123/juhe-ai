import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { removeLocalSelectPreferenceValues } from '@/shared/selectLocalPreferenceCache'
import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import { useRemoteAuthorizationPrincipalOptions } from './useRemoteAuthorizationPrincipalOptions'

vi.mock('@/api/client', () => ({
  api: {
    systemAccounts: {
      options: vi.fn()
    },
    systemTeams: {
      list: vi.fn(),
      detail: vi.fn()
    },
    myTeams: {
      list: vi.fn(),
      detail: vi.fn()
    }
  }
}))

vi.mock('@/composables/useAuth', async () => {
  const { ref } = await import('vue')
  return {
    authState: {
      currentUser: ref(undefined)
    }
  }
})

vi.mock('@/lib/antd', () => ({
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() }
}))

vi.mock('@/shared/selectLocalPreferenceCache', () => ({
  removeLocalSelectPreferenceValues: vi.fn()
}))

function account(id: string): SystemAccountPrincipalSummary {
  return { id, username: `user-${id}`, displayName: `账户-${id}`, status: 'active' } as SystemAccountPrincipalSummary
}

function team(id: string): SystemTeamPrincipalSummary {
  return { id, name: `团队-${id}`, status: 'active' } as SystemTeamPrincipalSummary
}

function teamListItem(id: string) {
  return { id, name: `团队-${id}`, status: 'active' as const, memberCount: 1, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' }
}

function setCurrentUser(id?: string) {
  authState.currentUser.value = id
    ? { id, username: `user-${id}`, displayName: `账户-${id}`, role: 'user' as const, mustChangePassword: false }
    : undefined
}

function setup(config: Partial<Parameters<typeof useRemoteAuthorizationPrincipalOptions>[0]> = {}) {
  let result!: ReturnType<typeof useRemoteAuthorizationPrincipalOptions<SystemAccountPrincipalSummary>>
  effectScope().run(() => {
    result = useRemoteAuthorizationPrincipalOptions<SystemAccountPrincipalSummary>({
      kind: 'account',
      isManagementView: () => true,
      ...config
    })
  })
  return result
}

beforeEach(() => {
  vi.clearAllMocks()
  setCurrentUser()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  vi.mocked(api.systemAccounts.options).mockResolvedValue([])
  vi.mocked(api.systemTeams.list).mockResolvedValue({ items: [], total: 0, hasMore: false, page: 1, pageSize: 50 })
  vi.mocked(api.myTeams.list).mockResolvedValue({ items: [], total: 0, hasMore: false, page: 1, pageSize: 50 })
  vi.mocked(api.systemTeams.detail).mockRejectedValue(new Error('not found'))
  vi.mocked(api.myTeams.detail).mockRejectedValue(new Error('not found'))
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('useRemoteAuthorizationPrincipalOptions.load', () => {
  it('账户类型 + 管理视图走系统账户 options', async () => {
    vi.mocked(api.systemAccounts.options).mockResolvedValue([account('a1')])
    const list = setup({ kind: 'account', isManagementView: () => true })
    await list.load('关键词')
    expect(api.systemAccounts.options).toHaveBeenCalledWith({ ids: undefined, keyword: '关键词', limit: 50 })
    expect(list.options.value.map((option) => option.id)).toEqual(['a1'])
    expect(list.loading.value).toBe(false)
  })

  it('团队类型 + 个人视图走我的团队列表', async () => {
    vi.mocked(api.myTeams.list).mockResolvedValue({ items: [teamListItem('t1')], total: 1, hasMore: false, page: 1, pageSize: 50 })
    const list = setup({ kind: 'team', isManagementView: () => false })
    await list.load()
    expect(api.myTeams.list).toHaveBeenCalledWith({ keyword: undefined, page: 1, pageSize: 50 })
    expect(list.options.value.map((option) => option.id)).toEqual(['t1'])
  })

  it('团队类型 + 管理视图走系统团队列表', async () => {
    vi.mocked(api.systemTeams.list).mockResolvedValue({ items: [teamListItem('t2')], total: 1, hasMore: false, page: 1, pageSize: 20 })
    const list = setup({ kind: 'team', isManagementView: () => true, limit: 20 })
    await list.load('团队')
    expect(api.systemTeams.list).toHaveBeenCalledWith({ keyword: '团队', page: 1, pageSize: 20 })
    expect(list.options.value.map((option) => option.id)).toEqual(['t2'])
  })

  it('账户类型 + 个人视图由当前登录用户本地构造候选项', async () => {
    setCurrentUser('self-1')
    const list = setup({ kind: 'account', isManagementView: () => false })
    await list.load()
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    expect(list.options.value.map((option) => option.id)).toEqual(['self-1'])
  })

  it('个人视图关键词不匹配时返回空', async () => {
    setCurrentUser('self-1')
    const list = setup({ kind: 'account', isManagementView: () => false })
    await list.load('别人')
    expect(list.options.value).toEqual([])
  })

  it('keyword 空白归一化为 undefined，limit 钳制到 1-50', async () => {
    const list = setup({ kind: 'account', isManagementView: () => true, limit: 999 })
    await list.load('   ')
    expect(api.systemAccounts.options).toHaveBeenCalledWith({ ids: undefined, keyword: undefined, limit: 50 })

    const minList = setup({ kind: 'account', isManagementView: () => true, limit: 0 })
    await minList.load()
    expect(api.systemAccounts.options).toHaveBeenLastCalledWith({ ids: undefined, keyword: undefined, limit: 1 })
  })

  it('enabled 返回 false 时清空选项且不发请求', async () => {
    const list = setup({ kind: 'account', isManagementView: () => true, enabled: () => false })
    list.options.value = [account('a1')]
    await list.load()
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    expect(list.options.value).toEqual([])
  })

  it('请求失败提示错误文案', async () => {
    vi.mocked(api.systemAccounts.options).mockRejectedValue(new Error('boom'))
    const list = setup({ kind: 'account', isManagementView: () => true, errorMessage: '自定义失败文案' })
    await list.load()
    expect(message.error).toHaveBeenCalledWith('自定义失败文案')
    expect(list.loading.value).toBe(false)
  })

  it('默认失败文案兜底', async () => {
    vi.mocked(api.myTeams.list).mockRejectedValue(new Error('boom'))
    const list = setup({ kind: 'team', isManagementView: () => false })
    await list.load()
    expect(message.error).toHaveBeenCalledWith('加载授权候选项失败')
  })
})

describe('选中项补全与失效处理', () => {
  it('窗口结果缺失的选中项触发按 ids 补拉并合并', async () => {
    vi.mocked(api.systemAccounts.options)
      .mockResolvedValueOnce([account('a1')])
      .mockResolvedValueOnce([account('a2'), account('a3')])
    // selectedIds 归一化会 trim 并丢弃空白项、undefined 与 all 哨兵值。
    const list = setup({ kind: 'account', isManagementView: () => true, selectedIds: () => ['a2', 'a3', 'all', undefined, '  '] })
    await list.load()
    expect(api.systemAccounts.options).toHaveBeenLastCalledWith({ ids: ['a2', 'a3'], keyword: undefined, limit: 50 })
    expect(list.options.value.map((option) => option.id)).toEqual(['a2', 'a3', 'a1'])
  })

  it('团队选中项缺失时按 detail 逐个补拉', async () => {
    vi.mocked(api.systemTeams.list).mockResolvedValue({ items: [teamListItem('t1')], total: 1, hasMore: false, page: 1, pageSize: 50 })
    vi.mocked(api.systemTeams.detail).mockResolvedValue({ ...team('t2'), memberCount: 1, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' })
    const list = setup({ kind: 'team', isManagementView: () => true, selectedIds: () => ['t2'] })
    await list.load()
    expect(api.systemTeams.detail).toHaveBeenCalledWith('t2')
    expect(list.options.value.map((option) => option.id)).toEqual(['t2', 't1'])
  })

  it('补拉仍找不到的选中项被清理并仅提示一次', async () => {
    vi.mocked(api.systemAccounts.options)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([account('a1')])
    const onMissingSelectedIds = vi.fn()
    const list = setup({
      kind: 'account',
      isManagementView: () => true,
      selectedIds: () => ['ghost-1', 'ghost-2'],
      onMissingSelectedIds,
      preferenceKeys: () => ['auth-pref-key']
    })
    await list.load()
    expect(removeLocalSelectPreferenceValues).toHaveBeenCalledWith('auth-pref-key', ['ghost-1', 'ghost-2'])
    expect(onMissingSelectedIds).toHaveBeenCalledWith(['ghost-1', 'ghost-2'])
    expect(message.warning).toHaveBeenCalledTimes(1)
    expect(message.warning).toHaveBeenCalledWith('已移除不存在或无权访问的授权对象，请重新选择')

    // 相同缺失集合再次出现不再重复提示。
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
    const list = setup({ kind: 'account', isManagementView: () => true, selectedIds: () => ['missing'] })
    await list.load()
    expect(list.options.value.map((option) => option.id)).toEqual(['a1'])
  })

  it('选中项全部在窗口内时不补拉', async () => {
    vi.mocked(api.systemAccounts.options).mockResolvedValue([account('a1')])
    const list = setup({ kind: 'account', isManagementView: () => true, selectedIds: () => ['a1'] })
    await list.load()
    expect(api.systemAccounts.options).toHaveBeenCalledTimes(1)
  })
})

describe('搜索与失效', () => {
  it('handleSearch 防抖后按最新关键词加载', async () => {
    vi.useFakeTimers()
    const list = setup({ kind: 'account', isManagementView: () => true, searchDelayMs: 250 })
    list.handleSearch(' 关键 ')
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    list.handleSearch('关键词2')
    vi.advanceTimersByTime(249)
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    await vi.advanceTimersByTimeAsync(0)
    expect(api.systemAccounts.options).toHaveBeenCalledTimes(1)
    expect(api.systemAccounts.options).toHaveBeenCalledWith({ ids: undefined, keyword: '关键词2', limit: 50 })
    expect(list.keyword.value).toBe('关键词2')
  })

  it('resetSearch 清空关键词并取消计时器', async () => {
    vi.useFakeTimers()
    const list = setup({ kind: 'account', isManagementView: () => true })
    list.handleSearch('abc')
    list.resetSearch()
    vi.advanceTimersByTime(1_000)
    expect(api.systemAccounts.options).not.toHaveBeenCalled()
    expect(list.keyword.value).toBe('')
  })

  it('handleDropdown 打开时触发加载', async () => {
    const list = setup({ kind: 'team', isManagementView: () => true })
    list.handleDropdown(true)
    await Promise.resolve()
    expect(api.systemTeams.list).toHaveBeenCalledTimes(1)
    list.handleDropdown(false)
    await Promise.resolve()
    expect(api.systemTeams.list).toHaveBeenCalledTimes(1)
  })

  it('invalidate 清空状态与选项', async () => {
    vi.mocked(api.systemAccounts.options).mockResolvedValue([account('a1')])
    const list = setup({ kind: 'account', isManagementView: () => true })
    await list.load()
    list.invalidate()
    expect(list.options.value).toEqual([])
    expect(list.loading.value).toBe(false)
    expect(list.keyword.value).toBe('')
  })
})
