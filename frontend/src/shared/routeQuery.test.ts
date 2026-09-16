import { describe, expect, it, vi } from 'vitest'

import type { RouteLocationNormalizedLoaded, Router } from 'vue-router'

import { hasRouteTraceId, removeRouteTraceIdQuery, singleRouteQueryValue, trimmedRouteQueryValue } from './routeQuery'

function fakeRoute(query: Record<string, unknown>): RouteLocationNormalizedLoaded {
  return { query } as unknown as RouteLocationNormalizedLoaded
}

describe('singleRouteQueryValue', () => {
  it('字符串直接返回', () => {
    expect(singleRouteQueryValue('abc')).toBe('abc')
  })

  it('数组取首个字符串元素', () => {
    expect(singleRouteQueryValue(['a', 'b'])).toBe('a')
  })

  it('数组首元素非字符串返回 undefined', () => {
    expect(singleRouteQueryValue([1, 'a'])).toBeUndefined()
    expect(singleRouteQueryValue([])).toBeUndefined()
  })

  it('非字符串返回 undefined', () => {
    expect(singleRouteQueryValue(undefined)).toBeUndefined()
    expect(singleRouteQueryValue(123)).toBeUndefined()
    expect(singleRouteQueryValue(null)).toBeUndefined()
  })
})

describe('trimmedRouteQueryValue', () => {
  it('去除首尾空白', () => {
    expect(trimmedRouteQueryValue('  x  ')).toBe('x')
  })

  it('空白字符串返回 undefined', () => {
    expect(trimmedRouteQueryValue('   ')).toBeUndefined()
    expect(trimmedRouteQueryValue('')).toBeUndefined()
  })

  it('非字符串输入返回 undefined', () => {
    expect(trimmedRouteQueryValue(undefined)).toBeUndefined()
  })
})

describe('hasRouteTraceId', () => {
  it('存在非空 traceId 返回 true', () => {
    expect(hasRouteTraceId(fakeRoute({ traceId: 't1' }))).toBe(true)
    expect(hasRouteTraceId(fakeRoute({ traceId: ['t1'] }))).toBe(true)
  })

  it('缺失或空白 traceId 返回 false', () => {
    expect(hasRouteTraceId(fakeRoute({}))).toBe(false)
    expect(hasRouteTraceId(fakeRoute({ traceId: '  ' }))).toBe(false)
    expect(hasRouteTraceId(fakeRoute({ traceId: [123] }))).toBe(false)
  })
})

describe('removeRouteTraceIdQuery', () => {
  it('无 traceId 时不触发路由替换', async () => {
    const router = { replace: vi.fn().mockResolvedValue(undefined) } as unknown as Router
    await expect(removeRouteTraceIdQuery(router, fakeRoute({ page: '2' }))).resolves.toBe(false)
    expect(router.replace).not.toHaveBeenCalled()
  })

  it('有 traceId 时移除后替换路由并保留其他参数', async () => {
    const router = { replace: vi.fn().mockResolvedValue(undefined) } as unknown as Router
    const route = fakeRoute({ traceId: 't1', page: '2', tab: ['a'] })
    await expect(removeRouteTraceIdQuery(router, route)).resolves.toBe(true)
    expect(router.replace).toHaveBeenCalledTimes(1)
    expect(router.replace).toHaveBeenCalledWith({ query: { page: '2', tab: ['a'] } })
  })
})
