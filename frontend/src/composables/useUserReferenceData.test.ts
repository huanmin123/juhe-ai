import { beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import type { CurrentUserSummary, UserReferenceData } from '@/types/domain'
import {
  clearUserReferenceDataCache,
  createUserReferenceDataResource,
  getCachedUserReferenceData,
  invalidateUserReferenceData,
  loadUserReferenceData,
  prewarmSelfUserReferenceData,
  syncUserReferenceDataAuthState,
  userReferenceDataScopeKey,
  type UserReferenceDataRequestScope
} from './useUserReferenceData'

vi.mock('@/api/client', () => ({
  api: {
    uiBootstrap: { options: vi.fn() },
    myUiBootstrap: { options: vi.fn() }
  }
}))

vi.mock('@/composables/useAuth', () => ({
  authState: {
    currentUser: { value: undefined as CurrentUserSummary | undefined },
    revision: { value: 0 }
  }
}))

function user(partial: Partial<CurrentUserSummary> = {}): CurrentUserSummary {
  return { id: 'u1', username: 'alice', displayName: 'Alice', role: 'admin', mustChangePassword: false, ...partial }
}

function referenceData(systemAccountId: string): UserReferenceData {
  return { systemAccountId, providerDefaults: [] }
}

function scope(partial: Partial<UserReferenceDataRequestScope> = {}): UserReferenceDataRequestScope {
  return {
    viewerSystemAccountId: 'u1',
    authRevision: 0,
    viewScope: 'self',
    ownerSystemAccountId: 'u1',
    ...partial
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  authState.currentUser.value = undefined
  authState.revision.value = 0
  clearUserReferenceDataCache()
})

describe('userReferenceDataScopeKey', () => {
  it('拼接作用域各段', () => {
    expect(userReferenceDataScopeKey(scope({ viewerSystemAccountId: 'v', authRevision: 2, viewScope: 'admin', ownerSystemAccountId: 'o' })))
      .toBe('v:2:admin:o')
  })
})

describe('createUserReferenceDataResource', () => {
  it('load 拉取并缓存结果，get 直接读取', async () => {
    const data = referenceData('u1')
    const fetch = vi.fn().mockResolvedValue(data)
    const resource = createUserReferenceDataResource({ fetch })
    resource.syncAuthSession('u1', 0)
    await expect(resource.load(scope())).resolves.toBe(data)
    expect(resource.get(scope())).toBe(data)
    await resource.load(scope())
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('并发 load 共享 in-flight 请求', async () => {
    let resolveFetch!: (value: UserReferenceData) => void
    const fetch = vi.fn(() => new Promise<UserReferenceData>((done) => { resolveFetch = done }))
    const resource = createUserReferenceDataResource({ fetch })
    resource.syncAuthSession('u1', 0)
    const first = resource.load(scope())
    const second = resource.load(scope())
    resolveFetch(referenceData('u1'))
    await expect(first).resolves.toBeDefined()
    await expect(second).resolves.toBeDefined()
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('响应与请求作用域不一致时抛出错误', async () => {
    const fetch = vi.fn().mockResolvedValue(referenceData('other-owner'))
    const resource = createUserReferenceDataResource({ fetch })
    resource.syncAuthSession('u1', 0)
    await expect(resource.load(scope({ ownerSystemAccountId: 'owner-1' }))).rejects.toThrow('用户默认资源响应与请求作用域不一致')
  })

  it('syncAuthSession 变更后旧会话数据不再可见', async () => {
    const data = referenceData('u1')
    const fetch = vi.fn().mockResolvedValue(data)
    const resource = createUserReferenceDataResource({ fetch })
    resource.syncAuthSession('u1', 0)
    await resource.load(scope())
    resource.syncAuthSession('u1', 1)
    expect(resource.get(scope())).toBeUndefined()
    // 旧会话作用域的 load 不再发起请求。
    await expect(resource.load(scope())).resolves.toBeUndefined()
    expect(fetch).toHaveBeenCalledTimes(1)
    // 新会话作用域重新拉取。
    await resource.load(scope({ authRevision: 1 }))
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('invalidate 后重新拉取', async () => {
    const fetch = vi.fn().mockResolvedValue(referenceData('u1'))
    const resource = createUserReferenceDataResource({ fetch })
    resource.syncAuthSession('u1', 0)
    await resource.load(scope())
    resource.invalidate(scope())
    expect(resource.get(scope())).toBeUndefined()
    await resource.load(scope())
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('clear 清空所有缓存', async () => {
    const fetch = vi.fn().mockResolvedValue(referenceData('u1'))
    const resource = createUserReferenceDataResource({ fetch })
    resource.syncAuthSession('u1', 0)
    await resource.load(scope())
    resource.clear()
    expect(resource.get(scope())).toBeUndefined()
  })

  it('非当前会话的 get/load 直接返回 undefined', async () => {
    const fetch = vi.fn().mockResolvedValue(referenceData('u1'))
    const resource = createUserReferenceDataResource({ fetch })
    // 未 syncAuthSession：会话键不匹配。
    await expect(resource.load(scope())).resolves.toBeUndefined()
    expect(resource.get(scope())).toBeUndefined()
    expect(fetch).not.toHaveBeenCalled()
  })
})

describe('顶层资源函数', () => {
  it('未登录时 load/get/invalidate 均为空操作', async () => {
    await expect(loadUserReferenceData()).resolves.toBeUndefined()
    expect(getCachedUserReferenceData()).toBeUndefined()
    expect(() => invalidateUserReferenceData()).not.toThrow()
    expect(api.myUiBootstrap.options).not.toHaveBeenCalled()
  })

  it('普通用户请求 admin 作用域返回 undefined', async () => {
    authState.currentUser.value = user({ role: 'user' })
    await expect(loadUserReferenceData({ viewScope: 'admin', systemAccountId: 'o1' })).resolves.toBeUndefined()
    expect(api.uiBootstrap.options).not.toHaveBeenCalled()
  })

  it('self 作用域走 myUiBootstrap 并缓存', async () => {
    authState.currentUser.value = user({ role: 'user', id: 'me-1' })
    const data = referenceData('me-1')
    vi.mocked(api.myUiBootstrap.options).mockResolvedValue(data)
    await expect(loadUserReferenceData()).resolves.toBe(data)
    expect(getCachedUserReferenceData()).toBe(data)
    // 再次 load 命中缓存。
    await loadUserReferenceData()
    expect(api.myUiBootstrap.options).toHaveBeenCalledTimes(1)
  })

  it('admin 作用域带 systemAccountId 请求 uiBootstrap', async () => {
    authState.currentUser.value = user()
    const data = referenceData('owner-1')
    vi.mocked(api.uiBootstrap.options).mockResolvedValue(data)
    await expect(loadUserReferenceData({ viewScope: 'admin', systemAccountId: ' owner-1 ' })).resolves.toBe(data)
    expect(api.uiBootstrap.options).toHaveBeenCalledWith({ systemAccountId: 'owner-1' })
  })

  it('admin 作用域缺 systemAccountId 返回 undefined', async () => {
    authState.currentUser.value = user()
    await expect(loadUserReferenceData({ viewScope: 'admin' })).resolves.toBeUndefined()
    expect(api.uiBootstrap.options).not.toHaveBeenCalled()
  })

  it('invalidate 后重新拉取', async () => {
    authState.currentUser.value = user({ role: 'user', id: 'me-2' })
    vi.mocked(api.myUiBootstrap.options).mockResolvedValue(referenceData('me-2'))
    await loadUserReferenceData()
    invalidateUserReferenceData()
    expect(getCachedUserReferenceData()).toBeUndefined()
    await loadUserReferenceData()
    expect(api.myUiBootstrap.options).toHaveBeenCalledTimes(2)
  })

  it('prewarmSelfUserReferenceData 吞掉加载错误', async () => {
    authState.currentUser.value = user({ role: 'user', id: 'me-3' })
    vi.mocked(api.myUiBootstrap.options).mockRejectedValue(new Error('boom'))
    await expect(prewarmSelfUserReferenceData()).resolves.toBeUndefined()
  })

  it('authRevision 变化触发会话键更新并重新拉取', async () => {
    authState.currentUser.value = user({ role: 'user', id: 'me-4' })
    vi.mocked(api.myUiBootstrap.options).mockResolvedValue(referenceData('me-4'))
    await loadUserReferenceData()
    authState.revision.value = 5
    expect(getCachedUserReferenceData()).toBeUndefined()
    syncUserReferenceDataAuthState()
    await loadUserReferenceData()
    expect(api.myUiBootstrap.options).toHaveBeenCalledTimes(2)
  })
})
