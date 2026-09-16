import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { removeLocalSelectPreferenceValues } from '@/shared/selectLocalPreferenceCache'
import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import { useRemoteAuthorizationPrincipalOptions } from './useRemoteAuthorizationPrincipalOptions'

vi.mock('@/api/client', () => ({
  api: {
    authorizationOptions: {
      granteeAccounts: vi.fn(),
      granteeTeams: vi.fn()
    },
    myAuthorizationOptions: {
      granteeAccounts: vi.fn(),
      granteeTeams: vi.fn()
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

function team(id: string): SystemTeamPrincipalSummary {
  return { id, name: `团队-${id}`, status: 'active' } as SystemTeamPrincipalSummary
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
  vi.spyOn(console, 'error').mockImplementation(() => {})
  vi.spyOn(console, 'warn').mockImplementation(() => {})
  vi.mocked(api.authorizationOptions.granteeAccounts).mockResolvedValue([])
  vi.mocked(api.authorizationOptions.granteeTeams).mockResolvedValue([])
  vi.mocked(api.myAuthorizationOptions.granteeAccounts).mockResolvedValue([])
  vi.mocked(api.myAuthorizationOptions.granteeTeams).mockResolvedValue([])
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('useRemoteAuthorizationPrincipalOptions.load', () => {
  it('账户类型 + 管理视图走授权候选接口', async () => {
    vi.mocked(api.authorizationOptions.granteeAccounts).mockResolvedValue([account('a1')])
    const list = setup({ kind: 'account', isManagementView: () => true })
    await list.load('关键词')
    expect(api.authorizationOptions.granteeAccounts).toHaveBeenCalledWith({ keyword: '关键词', limit: 50 })
    expect(list.options.value.map((option) => option.id)).toEqual(['a1'])
    expect(list.loading.value).toBe(false)
  })

  it('团队类型 + 个人视图走我的授权候选接口', async () => {
    vi.mocked(api.myAuthorizationOptions.granteeTeams).mockResolvedValue([team('t1')])
    const list = setup({ kind: 'team', isManagementView: () => false })
    await list.load()
    expect(api.myAuthorizationOptions.granteeTeams).toHaveBeenCalledWith({ keyword: undefined, limit: 50 })
    expect(list.options.value.map((option) => option.id)).toEqual(['t1'])
  })

  it('keyword 空白归一化为 undefined，limit 钳制到 1-50', async () => {
    const list = setup({ kind: 'account', isManagementView: () => true, limit: 999 })
    await list.load('   ')
    expect(api.authorizationOptions.granteeAccounts).toHaveBeenCalledWith({ keyword: undefined, limit: 50 })

    const minList = setup({ kind: 'account', isManagementView: () => true, limit: 0 })
    await minList.load()
    expect(api.authorizationOptions.granteeAccounts).toHaveBeenLastCalledWith({ keyword: undefined, limit: 1 })
  })

  it('enabled 返回 false 时清空选项且不发请求', async () => {
    const list = setup({ kind: 'account', isManagementView: () => true, enabled: () => false })
    list.options.value = [account('a1')]
    await list.load()
    expect(api.authorizationOptions.granteeAccounts).not.toHaveBeenCalled()
    expect(list.options.value).toEqual([])
  })

  it('请求失败提示错误文案', async () => {
    vi.mocked(api.authorizationOptions.granteeAccounts).mockRejectedValue(new Error('boom'))
    const list = setup({ kind: 'account', isManagementView: () => true, errorMessage: '自定义失败文案' })
    await list.load()
    expect(message.error).toHaveBeenCalledWith('自定义失败文案')
    expect(list.loading.value).toBe(false)
  })

  it('默认失败文案兜底', async () => {
    vi.mocked(api.myAuthorizationOptions.granteeTeams).mockRejectedValue(new Error('boom'))
    const list = setup({ kind: 'team', isManagementView: () => false })
    await list.load()
    expect(message.error).toHaveBeenCalledWith('加载授权候选项失败')
  })
})

describe('选中项补全与失效处理', () => {
  it('窗口结果缺失的选中项触发按 ids 补拉并合并', async () => {
    vi.mocked(api.authorizationOptions.granteeAccounts)
      .mockResolvedValueOnce([account('a1')])
      .mockResolvedValueOnce([account('a2'), account('a3')])
    // selectedIds 归一化会 trim 并丢弃空白项、undefined 与 all 哨兵值。
    const list = setup({ kind: 'account', isManagementView: () => true, selectedIds: () => ['a2', 'a3', 'all', undefined, '  '] })
    await list.load()
    expect(api.authorizationOptions.granteeAccounts).toHaveBeenLastCalledWith({ ids: ['a2', 'a3'], limit: 50 })
    expect(list.options.value.map((option) => option.id)).toEqual(['a2', 'a3', 'a1'])
  })

  it('补拉仍找不到的选中项被清理并仅提示一次', async () => {
    vi.mocked(api.authorizationOptions.granteeAccounts)
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
    vi.mocked(api.authorizationOptions.granteeAccounts)
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce([])
    await list.load()
    expect(message.warning).toHaveBeenCalledTimes(1)
  })

  it('补拉失败时保留窗口结果', async () => {
    vi.mocked(api.authorizationOptions.granteeAccounts)
      .mockResolvedValueOnce([account('a1')])
      .mockRejectedValueOnce(new Error('ids fetch failed'))
    const list = setup({ kind: 'account', isManagementView: () => true, selectedIds: () => ['missing'] })
    await list.load()
    expect(list.options.value.map((option) => option.id)).toEqual(['a1'])
  })

  it('选中项全部在窗口内时不补拉', async () => {
    vi.mocked(api.authorizationOptions.granteeAccounts).mockResolvedValue([account('a1')])
    const list = setup({ kind: 'account', isManagementView: () => true, selectedIds: () => ['a1'] })
    await list.load()
    expect(api.authorizationOptions.granteeAccounts).toHaveBeenCalledTimes(1)
  })
})

describe('搜索与失效', () => {
  it('handleSearch 防抖后按最新关键词加载', async () => {
    vi.useFakeTimers()
    const list = setup({ kind: 'account', isManagementView: () => true, searchDelayMs: 250 })
    list.handleSearch(' 关键 ')
    expect(api.authorizationOptions.granteeAccounts).not.toHaveBeenCalled()
    list.handleSearch('关键词2')
    vi.advanceTimersByTime(249)
    expect(api.authorizationOptions.granteeAccounts).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    await vi.advanceTimersByTimeAsync(0)
    expect(api.authorizationOptions.granteeAccounts).toHaveBeenCalledTimes(1)
    expect(api.authorizationOptions.granteeAccounts).toHaveBeenCalledWith({ keyword: '关键词2', limit: 50 })
    expect(list.keyword.value).toBe('关键词2')
  })

  it('resetSearch 清空关键词并取消计时器', async () => {
    vi.useFakeTimers()
    const list = setup({ kind: 'account', isManagementView: () => true })
    list.handleSearch('abc')
    list.resetSearch()
    vi.advanceTimersByTime(1_000)
    expect(api.authorizationOptions.granteeAccounts).not.toHaveBeenCalled()
    expect(list.keyword.value).toBe('')
  })

  it('handleDropdown 打开时触发加载', async () => {
    const list = setup({ kind: 'team', isManagementView: () => true })
    list.handleDropdown(true)
    await Promise.resolve()
    expect(api.authorizationOptions.granteeTeams).toHaveBeenCalledTimes(1)
    list.handleDropdown(false)
    await Promise.resolve()
    expect(api.authorizationOptions.granteeTeams).toHaveBeenCalledTimes(1)
  })

  it('invalidate 清空状态与选项', async () => {
    vi.mocked(api.authorizationOptions.granteeAccounts).mockResolvedValue([account('a1')])
    const list = setup({ kind: 'account', isManagementView: () => true })
    await list.load()
    list.invalidate()
    expect(list.options.value).toEqual([])
    expect(list.loading.value).toBe(false)
    expect(list.keyword.value).toBe('')
  })
})
