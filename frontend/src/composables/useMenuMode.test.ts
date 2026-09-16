import { beforeEach, describe, expect, it } from 'vitest'

import type { CurrentUserSummary } from '@/types/domain'
import { appMenuMode, getDefaultPathForMenuMode, getPreferredEntryPath, readMenuModePreference, saveMenuModePreference, setMenuModeFromRoute, syncMenuModeWithUser } from './useMenuMode'

function user(partial: Partial<CurrentUserSummary> = {}): CurrentUserSummary {
  return { id: 'u1', username: 'alice', displayName: 'Alice', role: 'admin', mustChangePassword: false, ...partial }
}

beforeEach(() => {
  window.localStorage.clear()
  appMenuMode.value = 'self'
})

describe('readMenuModePreference', () => {
  it('非管理员始终返回 self（即使存储中有 admin）', () => {
    window.localStorage.setItem('juhe-ai:menu-mode:u1', 'admin')
    expect(readMenuModePreference(user({ role: 'user' }))).toBe('self')
    expect(readMenuModePreference(undefined)).toBe('self')
  })

  it('管理员读取存储偏好并归一化非法值', () => {
    expect(readMenuModePreference(user())).toBe('self')
    window.localStorage.setItem('juhe-ai:menu-mode:u1', 'admin')
    expect(readMenuModePreference(user())).toBe('admin')
    window.localStorage.setItem('juhe-ai:menu-mode:u1', 'something-else')
    expect(readMenuModePreference(user())).toBe('self')
  })

  it('存储键优先使用用户 id，id 为空时回退 username', () => {
    window.localStorage.setItem('juhe-ai:menu-mode:bob', 'admin')
    expect(readMenuModePreference(user({ id: '', username: 'bob', role: 'super_admin' }))).toBe('admin')
  })
})

describe('syncMenuModeWithUser / setMenuModeFromRoute', () => {
  it('同步存储偏好到全局菜单模式', () => {
    window.localStorage.setItem('juhe-ai:menu-mode:u1', 'admin')
    syncMenuModeWithUser(user())
    expect(appMenuMode.value).toBe('admin')
    syncMenuModeWithUser(user({ role: 'user' }))
    expect(appMenuMode.value).toBe('self')
  })

  it('管理员可从路由切换菜单模式，普通用户被强制 self', () => {
    setMenuModeFromRoute(user(), 'admin')
    expect(appMenuMode.value).toBe('admin')
    setMenuModeFromRoute(user({ role: 'user' }), 'admin')
    expect(appMenuMode.value).toBe('self')
  })
})

describe('saveMenuModePreference', () => {
  it('管理员保存偏好到存储并同步全局状态', () => {
    const result = saveMenuModePreference(user(), 'admin')
    expect(result).toBe('admin')
    expect(appMenuMode.value).toBe('admin')
    expect(window.localStorage.getItem('juhe-ai:menu-mode:u1')).toBe('admin')
  })

  it('普通用户不保存且强制 self', () => {
    const result = saveMenuModePreference(user({ role: 'user' }), 'admin')
    expect(result).toBe('self')
    expect(appMenuMode.value).toBe('self')
    expect(window.localStorage.getItem('juhe-ai:menu-mode:u1')).toBeNull()
  })

  it('未登录用户不写存储', () => {
    expect(saveMenuModePreference(undefined, 'admin')).toBe('self')
    expect(window.localStorage.length).toBe(0)
  })
})

describe('默认路径', () => {
  it('getDefaultPathForMenuMode 按模式返回入口', () => {
    expect(getDefaultPathForMenuMode('admin')).toBe('/stats')
    expect(getDefaultPathForMenuMode('self')).toBe('/my-accounts')
  })

  it('getPreferredEntryPath 依据用户偏好返回入口', () => {
    expect(getPreferredEntryPath(undefined)).toBe('/my-accounts')
    window.localStorage.setItem('juhe-ai:menu-mode:u1', 'admin')
    expect(getPreferredEntryPath(user())).toBe('/stats')
  })
})
