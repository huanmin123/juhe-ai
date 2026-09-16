import { beforeEach, describe, expect, it, vi } from 'vitest'

import { authState } from '@/composables/useAuth'
import type { CurrentUserSummary } from '@/types/domain'
import { useScopedMenuView } from './useScopedMenuView'

// vue-router 的 useRoute 用可变路由对象模拟；authState 用可控 mock。
const routeState = vi.hoisted(() => ({ route: { path: '/x', meta: {} } as { path: string; meta: Record<string, unknown> } }))

vi.mock('vue-router', () => ({
  useRoute: () => routeState.route
}))

vi.mock('@/composables/useAuth', async () => {
  const { computed, ref } = await import('vue')
  const currentUser = ref<CurrentUserSummary>()
  return {
    authState: {
      currentUser,
      isAdmin: computed(() => currentUser.value?.role === 'admin' || currentUser.value?.role === 'super_admin')
    }
  }
})

function user(partial: Partial<CurrentUserSummary> = {}): CurrentUserSummary {
  return { id: 'u1', username: 'alice', displayName: 'Alice', role: 'admin', mustChangePassword: false, ...partial }
}

beforeEach(() => {
  routeState.route = { path: '/x', meta: {} }
  authState.currentUser.value = undefined
})

describe('useScopedMenuView', () => {
  it('管理员且路由 viewScope 为 admin 时进入管理视图', () => {
    authState.currentUser.value = user()
    routeState.route = { path: '/admin/x', meta: { viewScope: 'admin' } }
    const view = useScopedMenuView()
    expect(view.isManagementView.value).toBe(true)
  })

  it('管理员但路由非 admin 作用域时不是管理视图', () => {
    authState.currentUser.value = user()
    routeState.route = { path: '/my-accounts', meta: {} }
    expect(useScopedMenuView().isManagementView.value).toBe(false)
  })

  it('普通用户即使路由为 admin 也不是管理视图', () => {
    authState.currentUser.value = user({ role: 'user' })
    routeState.route = { path: '/admin/x', meta: { viewScope: 'admin' } }
    expect(useScopedMenuView().isManagementView.value).toBe(false)
  })

  it('管理视图下 scopedSystemAccountId 委托系统账户筛选（all 与空白返回 undefined）', () => {
    authState.currentUser.value = user()
    routeState.route = { path: '/admin/x', meta: { viewScope: 'admin' } }
    const view = useScopedMenuView()
    expect(view.scopedSystemAccountId()).toBeUndefined()
    expect(view.scopedSystemAccountId('all')).toBeUndefined()
    expect(view.scopedSystemAccountId('  ')).toBeUndefined()
    expect(view.scopedSystemAccountId(' target ')).toBe('target')
  })

  it('非管理视图下 scopedSystemAccountId 返回当前用户 id', () => {
    authState.currentUser.value = user({ id: 'me-1' })
    routeState.route = { path: '/my-accounts', meta: {} }
    const view = useScopedMenuView()
    expect(view.scopedSystemAccountId()).toBe('me-1')
    expect(view.scopedSystemAccountId('other')).toBe('me-1')
  })
})
