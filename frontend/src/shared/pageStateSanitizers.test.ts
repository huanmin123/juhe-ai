import { describe, expect, it } from 'vitest'

import { positiveIntegerOrFallback, sanitizePaginationState, stringOrFallback, stringUnionOrFallback } from './pageStateSanitizers'

describe('positiveIntegerOrFallback', () => {
  it('接受范围内的正整数', () => {
    expect(positiveIntegerOrFallback(1, 10)).toBe(1)
    expect(positiveIntegerOrFallback(20, 10, 20)).toBe(20)
  })

  it('字符串数字被转换为数字判断', () => {
    expect(positiveIntegerOrFallback('5', 10)).toBe(5)
  })

  it('零、负数、小数回退', () => {
    expect(positiveIntegerOrFallback(0, 10)).toBe(10)
    expect(positiveIntegerOrFallback(-3, 10)).toBe(10)
    expect(positiveIntegerOrFallback(1.5, 10)).toBe(10)
  })

  it('超过上限回退', () => {
    expect(positiveIntegerOrFallback(101, 10, 100)).toBe(10)
  })

  it('非法输入回退', () => {
    expect(positiveIntegerOrFallback(undefined, 7)).toBe(7)
    expect(positiveIntegerOrFallback(null, 7)).toBe(7)
    expect(positiveIntegerOrFallback('abc', 7)).toBe(7)
    expect(positiveIntegerOrFallback(Number.NaN, 7)).toBe(7)
  })
})

describe('sanitizePaginationState', () => {
  it('合法分页状态原样保留', () => {
    expect(sanitizePaginationState({ current: 2, pageSize: 50 }, { current: 1, pageSize: 20 }))
      .toEqual({ current: 2, pageSize: 50 })
  })

  it('非法字段逐项回退', () => {
    expect(sanitizePaginationState({ current: 0, pageSize: 999 }, { current: 1, pageSize: 20 }))
      .toEqual({ current: 1, pageSize: 20 })
  })

  it('pageSize 超过 maxPageSize 回退', () => {
    expect(sanitizePaginationState({ current: 1, pageSize: 300 }, { current: 1, pageSize: 20 }, 200))
      .toEqual({ current: 1, pageSize: 20 })
  })

  it('非对象输入整体回退', () => {
    expect(sanitizePaginationState(null, { current: 3, pageSize: 40 })).toEqual({ current: 3, pageSize: 40 })
    expect(sanitizePaginationState('x', { current: 3, pageSize: 40 })).toEqual({ current: 3, pageSize: 40 })
  })

  it('部分字段缺失时缺失项回退', () => {
    expect(sanitizePaginationState({ current: 2 }, { current: 1, pageSize: 30 }))
      .toEqual({ current: 2, pageSize: 30 })
  })
})

describe('stringOrFallback', () => {
  it('字符串原样返回', () => {
    expect(stringOrFallback('abc')).toBe('abc')
    expect(stringOrFallback('abc', 'x')).toBe('abc')
  })

  it('非字符串回退默认值', () => {
    expect(stringOrFallback(undefined, 'fb')).toBe('fb')
    expect(stringOrFallback(123, 'fb')).toBe('fb')
    expect(stringOrFallback(null, 'fb')).toBe('fb')
    expect(stringOrFallback(undefined)).toBe('')
  })
})

describe('stringUnionOrFallback', () => {
  const allowed = ['alpha', 'beta'] as const

  it('在允许列表中的值原样返回', () => {
    expect(stringUnionOrFallback('beta', allowed, 'alpha')).toBe('beta')
  })

  it('不在允许列表中的值回退', () => {
    expect(stringUnionOrFallback('gamma', allowed, 'alpha')).toBe('alpha')
  })

  it('非字符串回退', () => {
    expect(stringUnionOrFallback(undefined, allowed, 'alpha')).toBe('alpha')
    expect(stringUnionOrFallback(42, allowed, 'alpha')).toBe('alpha')
  })
})
