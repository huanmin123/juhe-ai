import { describe, expect, it } from 'vitest'

import { systemAccountAccountLimitsText, systemAccountRequestLimitsText } from './systemAccountDisplay'

describe('systemAccountRequestLimitsText', () => {
  it('无覆盖或空覆盖显示继承全局', () => {
    expect(systemAccountRequestLimitsText(undefined)).toBe('继承全局')
    expect(systemAccountRequestLimitsText({})).toBe('继承全局')
  })

  it('只拼接已设置窗口，0 渲染为不限', () => {
    expect(systemAccountRequestLimitsText({ perMinute: 10, perDay: 0, perMonth: 2000 })).toBe('10/分 不限/日 2000/月')
    expect(systemAccountRequestLimitsText({ perDay: 100 })).toBe('100/日')
  })

  it('携带到期日时追加有效期', () => {
    expect(systemAccountRequestLimitsText({ perDay: 100, expiresOn: '2026-11-01' })).toBe('100/日（至 2026-11-01）')
  })
})

describe('systemAccountAccountLimitsText', () => {
  it('未覆盖显示继承与默认', () => {
    expect(systemAccountAccountLimitsText({})).toBe('AI 继承 · 特供 默认')
    expect(systemAccountAccountLimitsText({ expeditedAccountLimit: null })).toBe('AI 继承 · 特供 默认')
  })

  it('0 值显示不限与禁止', () => {
    expect(systemAccountAccountLimitsText({ aiAccountLimit: 0, expeditedAccountLimit: 0 })).toBe('AI 不限 · 特供 禁止')
  })

  it('显式数值原样展示', () => {
    expect(systemAccountAccountLimitsText({ aiAccountLimit: 5, expeditedAccountLimit: 2 })).toBe('AI 5 · 特供 2')
  })
})
