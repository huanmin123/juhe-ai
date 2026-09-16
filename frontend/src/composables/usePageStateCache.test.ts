import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { authState } from '@/composables/useAuth'
import type { CurrentUserSummary } from '@/types/domain'
import { usePageStateCache } from './usePageStateCache'

const routeState = vi.hoisted(() => ({ route: { path: '/from-route', meta: {} } }))

vi.mock('vue-router', () => ({
  useRoute: () => routeState.route
}))

vi.mock('@/composables/useAuth', () => ({
  authState: {
    currentUser: { value: undefined as CurrentUserSummary | undefined }
  }
}))

const prefix = 'juhe-ai:page-state:'

function createCache(pageKey?: string, options?: Parameters<typeof usePageStateCache<{ keyword: string; page: number }>>[2]) {
  return usePageStateCache(pageKey, () => ({ keyword: '', page: 1 }), options)
}

beforeEach(() => {
  window.localStorage.clear()
  window.sessionStorage.clear()
  authState.currentUser.value = undefined
  routeState.route = { path: '/from-route', meta: {} }
  // 组合函数在组件外调用时 Vue 会输出生命周期告警，静音以保证输出可读。
  vi.spyOn(console, 'warn').mockImplementation(() => {})
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('usePageStateCache.read', () => {
  it('无缓存时返回默认值', () => {
    const cache = createCache('page-a')
    expect(cache.read()).toEqual({ keyword: '', page: 1 })
  })

  it('读取写入的缓存', () => {
    const cache = createCache('page-b')
    cache.write({ keyword: 'abc', page: 3 })
    expect(cache.read()).toEqual({ keyword: 'abc', page: 3 })
  })

  it('版本不匹配时回退默认值', () => {
    const cache = createCache('page-c', { version: 2 })
    window.localStorage.setItem(`${prefix}anonymous:page-c:v2`, JSON.stringify({ version: 1, state: { keyword: 'x', page: 9 } }))
    expect(cache.read()).toEqual({ keyword: '', page: 1 })
  })

  it('损坏 JSON 或结构异常时回退默认值', () => {
    const cache = createCache('page-d')
    window.localStorage.setItem(`${prefix}anonymous:page-d:v1`, 'not-json')
    expect(cache.read()).toEqual({ keyword: '', page: 1 })
    window.localStorage.setItem(`${prefix}anonymous:page-d:v1`, JSON.stringify({ version: 1, state: null }))
    expect(cache.read()).toEqual({ keyword: '', page: 1 })
  })

  it('sanitize 回调接管解析', () => {
    const cache = usePageStateCache('page-e', () => ({ keyword: '', page: 1 }), {
      sanitize: (value, fallback) => ({
        keyword: typeof (value as { keyword?: unknown }).keyword === 'string' ? (value as { keyword: string }).keyword : fallback.keyword,
        page: fallback.page
      })
    })
    window.localStorage.setItem(`${prefix}anonymous:page-e:v1`, JSON.stringify({ version: 1, state: { keyword: 42, page: 9 } }))
    expect(cache.read()).toEqual({ keyword: '', page: 1 })
  })

  it('sessionStorage 存储类型', () => {
    const cache = createCache('page-f', { storage: 'session' })
    cache.write({ keyword: 's', page: 2 })
    expect(window.sessionStorage.length).toBe(1)
    expect(window.localStorage.length).toBe(0)
    expect(cache.read()).toEqual({ keyword: 's', page: 2 })
  })

  it('缓存键包含用户、页面键与版本；无页面键时回退路由路径', () => {
    authState.currentUser.value = { id: 'user-1', username: 'alice', displayName: 'Alice', role: 'user', mustChangePassword: false }
    createCache('page-g').write({ keyword: '', page: 1 })
    createCache(undefined).write({ keyword: '', page: 1 })
    const keys = Object.keys(window.localStorage)
    expect(keys).toContain(`${prefix}user-1:page-g:v1`)
    expect(keys).toContain(`${prefix}user-1:/from-route:v1`)
  })

  it('页面键中的非法字符被归一化', () => {
    const cache = createCache('页 面/键#值')
    cache.write({ keyword: 'x', page: 1 })
    const keys = Object.keys(window.localStorage)
    expect(keys).toContain(`${prefix}anonymous:___/___:v1`)
  })
})

describe('usePageStateCache.write / clear', () => {
  it('write 写入带版本的包裹结构', () => {
    const cache = createCache('page-h', { version: 3 })
    cache.write({ keyword: 'k', page: 5 })
    expect(JSON.parse(window.localStorage.getItem(`${prefix}anonymous:page-h:v3`)!)).toEqual({
      version: 3,
      state: { keyword: 'k', page: 5 }
    })
  })

  it('clear 移除缓存键', () => {
    const cache = createCache('page-i')
    cache.write({ keyword: 'k', page: 5 })
    cache.clear()
    expect(window.localStorage.getItem(`${prefix}anonymous:page-i:v1`)).toBeNull()
    expect(cache.read()).toEqual({ keyword: '', page: 1 })
  })
})

describe('usePageStateCache.scheduleWrite', () => {
  it('按 debounceMs 延迟写入，重复调度重置计时', () => {
    vi.useFakeTimers()
    const cache = createCache('page-j', { debounceMs: 200 })
    cache.scheduleWrite(() => ({ keyword: 'first', page: 1 }))
    cache.scheduleWrite(() => ({ keyword: 'second', page: 2 }))
    vi.advanceTimersByTime(199)
    expect(window.localStorage.getItem(`${prefix}anonymous:page-j:v1`)).toBeNull()
    vi.advanceTimersByTime(1)
    expect(JSON.parse(window.localStorage.getItem(`${prefix}anonymous:page-j:v1`)!)).toEqual({
      version: 1,
      state: { keyword: 'second', page: 2 }
    })
  })

  it('flushPendingWrite 立即写入待写快照', () => {
    const cache = createCache('page-k')
    cache.scheduleWrite(() => ({ keyword: 'flushed', page: 7 }))
    cache.flushPendingWrite()
    expect(cache.read()).toEqual({ keyword: 'flushed', page: 7 })
  })

  it('cancelPendingWrite 取消待写快照', () => {
    vi.useFakeTimers()
    const cache = createCache('page-l')
    cache.scheduleWrite(() => ({ keyword: 'pending', page: 1 }))
    cache.cancelPendingWrite()
    vi.advanceTimersByTime(1_000)
    expect(window.localStorage.getItem(`${prefix}anonymous:page-l:v1`)).toBeNull()
  })

  it('clear 同时取消待写任务', () => {
    vi.useFakeTimers()
    const cache = createCache('page-m')
    cache.write({ keyword: 'old', page: 1 })
    cache.scheduleWrite(() => ({ keyword: 'new', page: 2 }))
    cache.clear()
    vi.advanceTimersByTime(1_000)
    expect(window.localStorage.getItem(`${prefix}anonymous:page-m:v1`)).toBeNull()
  })
})
