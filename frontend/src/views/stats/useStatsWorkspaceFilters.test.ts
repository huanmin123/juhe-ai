import { effectScope } from 'vue'
import { createWebHistory } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { authState } from '@/composables/useAuth'

import { useStatsWorkspaceFilters } from './useStatsWorkspaceFilters'

const navigation = vi.hoisted(() => ({
  route: { path: '/stats', fullPath: '/stats', meta: { viewScope: 'admin' } },
  history: {
    location: '/stats',
    state: {} as Record<string, unknown>,
    replace: vi.fn((location: string, state: Record<string, unknown>) => {
      navigation.history.location = location
      navigation.history.state = { ...navigation.history.state, ...state }
    })
  }
}))

vi.mock('vue-router', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-router')>(),
  useRoute: () => navigation.route,
  useRouter: () => ({
    options: { history: navigation.history },
    resolve: (path: string) => ({ meta: { viewScope: path.startsWith('/my-') ? 'self' : 'admin' } })
  })
}))

vi.mock('@/composables/useAuth', () => ({
  authState: { currentUser: { value: { id: 'admin-1' } } }
}))

const filters = {
  startDate: '2026-11-14',
  endDate: '2026-11-20',
  systemAccountId: 'user-1',
  systemAccount: { id: 'user-1', name: '用户一', kind: 'system_account' as const }
}

describe('统计工作区筛选传递', () => {
  beforeEach(() => {
    navigation.route.path = '/stats'
    navigation.route.fullPath = '/stats'
    navigation.route.meta.viewScope = 'admin'
    navigation.history.location = '/stats'
    navigation.history.state = {}
    navigation.history.replace.mockClear()
  })

  it('进入账户健康并返回请求性能时保留选中的用户和日期', () => {
    const owner = effectScope()
    try {
      const overview = owner.run(() => {
        const workspace = useStatsWorkspaceFilters()
        workspace.register(() => filters)
        return workspace
      })!
      expect(overview.link('/my-ai-performance')).toBe('/my-ai-performance')
      overview.persist('recent7d')
      const healthLocation = overview.link('/ai-health')
      expect(healthLocation).toMatchObject({ path: '/ai-health', state: { statsSystemAccountId: 'user-1' } })
      if (typeof healthLocation === 'string' || !healthLocation.state) throw new Error('缺少筛选状态')
      navigation.history.state = healthLocation.state
      navigation.route.path = '/ai-health'
      const health = useStatsWorkspaceFilters()
      const performanceLocation = health.link('/ai-performance')
      expect(performanceLocation).toMatchObject({ path: '/ai-performance', state: healthLocation.state })
      expect(health.incoming()).toEqual({ ...filters, overviewRangeMode: 'recent7d' })
      expect(health.link('/stats')).toMatchObject({ state: { statsOverviewRangeMode: 'recent7d' } })
    } finally {
      owner.stop()
    }
  })

  it('忽略跨用户、跨模式和非法日期的历史状态', () => {
    const workspace = useStatsWorkspaceFilters()
    const state = {
      statsWorkspaceUser: authState.currentUser.value?.id,
      statsWorkspaceScope: 'admin',
      statsStartDate: filters.startDate,
      statsEndDate: filters.endDate,
      statsSystemAccountId: filters.systemAccountId
    }
    navigation.history.state = { ...state, statsWorkspaceUser: 'admin-2' }
    expect(workspace.incoming()).toBeUndefined()
    navigation.history.state = { ...state, statsWorkspaceScope: 'self' }
    expect(workspace.incoming()).toBeUndefined()
    navigation.history.state = { ...state, statsStartDate: '2026-02-31' }
    expect(workspace.incoming()).toBeUndefined()
  })

  it('用户模式仅传递业务日期', () => {
    navigation.route.path = '/my-ai-performance'
    navigation.route.meta.viewScope = 'self'
    navigation.history.state = {
      statsWorkspaceUser: authState.currentUser.value?.id,
      statsWorkspaceScope: 'self',
      statsStartDate: filters.startDate,
      statsEndDate: filters.endDate,
      statsSystemAccountId: 'other-user',
      statsSystemAccountName: '其他用户'
    }
    expect(useStatsWorkspaceFilters().incoming()).toEqual({
      startDate: filters.startDate,
      endDate: filters.endDate,
      systemAccountId: 'all',
      systemAccount: undefined
    })
  })

  it('将当前筛选写入 history entry 并保留路由状态', () => {
    navigation.history.state = { back: '/home', current: '/stats', forward: null, position: 3, custom: 'keep' }
    const scope = effectScope()
    try {
      const workspace = scope.run(() => {
        const value = useStatsWorkspaceFilters()
        value.register(() => filters)
        return value
      })!
      workspace.persist()
      expect(navigation.history.replace).toHaveBeenCalledWith('/stats', expect.objectContaining({
        back: '/home',
        current: '/stats',
        position: 3,
        custom: 'keep',
        statsStartDate: filters.startDate,
        statsEndDate: filters.endDate,
        statsSystemAccountId: filters.systemAccountId
      }))
    } finally {
      scope.stop()
    }
  })

  it('persist 使用 reader 的最新筛选，clear 只移除工作区状态', () => {
    const scope = effectScope()
    try {
      let current: Omit<typeof filters, 'systemAccount'> & { systemAccount?: typeof filters.systemAccount } = filters
      const workspace = scope.run(() => {
        const value = useStatsWorkspaceFilters()
        value.register(() => current)
        return value
      })!
      current = { ...filters, startDate: '2026-11-10', endDate: '2026-11-16', systemAccountId: 'all', systemAccount: undefined }
      navigation.history.state = { position: 2, statsWorkspaceUser: 'admin-1', statsStartDate: filters.startDate }
      workspace.persist()
      expect(navigation.history.state).toMatchObject({
        position: 2,
        statsStartDate: current.startDate,
        statsEndDate: current.endDate,
        statsSystemAccountId: 'all'
      })
      workspace.persist('recent7d')
      expect(workspace.incoming()?.overviewRangeMode).toBe('recent7d')
      const destination = workspace.link('/ai-performance')
      expect(destination).toMatchObject({ state: { statsOverviewRangeMode: 'recent7d' } })
      current = { ...current, startDate: '2026-11-15' }
      expect(workspace.link('/ai-performance')).toMatchObject({ state: { statsOverviewRangeMode: null } })
      workspace.clear()
      expect(navigation.history.state).toMatchObject({ position: 2, statsWorkspaceUser: null, statsStartDate: null, statsEndDate: null, statsSystemAccountId: null })
      expect(workspace.incoming()).toBeUndefined()
    } finally {
      scope.stop()
    }
  })

  it('路由切换期间不覆盖其他页面的 history entry', () => {
    const scope = effectScope()
    try {
      const workspace = scope.run(() => {
        const value = useStatsWorkspaceFilters()
        value.register(() => filters)
        return value
      })!
      navigation.history.location = '/ai-performance'
      workspace.persist()
      workspace.clear()
      expect(navigation.history.replace).not.toHaveBeenCalled()
    } finally {
      scope.stop()
    }
  })

  it('真实 web history 的刷新读取、后退和前进保留修改或重置后的筛选', async () => {
    const history = createWebHistory()
    const navigate = (delta: number) => new Promise<void>((resolve) => {
      const stop = history.listen(() => {
        stop()
        resolve()
      })
      history.go(delta)
    })
    try {
      history.push('/stats', { statsStartDate: '2026-11-14', custom: 'keep' })
      history.replace('/stats', { statsStartDate: '2026-11-18' })
      expect(window.history.state).toMatchObject({ statsStartDate: '2026-11-18', custom: 'keep' })
      history.push('/ai-performance', { statsStartDate: '2026-11-18' })
      history.replace('/ai-performance', { statsStartDate: null })
      await navigate(-1)
      expect(history.state).toMatchObject({ statsStartDate: '2026-11-18', custom: 'keep' })
      await navigate(1)
      expect(history.state).toHaveProperty('statsStartDate', null)
    } finally {
      history.destroy()
    }
  })
})
