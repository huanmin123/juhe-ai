import { describe, expect, it } from 'vitest'

import { normalizeFrontendBuildId } from './frontendBuildId'

describe('normalizeFrontendBuildId', () => {
  it('接受 40 位小写十六进制', () => {
    const value = '0123456789abcdef0123456789abcdef01234567'
    expect(normalizeFrontendBuildId(value)).toBe(value)
  })

  it('大写输入归一化为小写', () => {
    const value = 'ABCDEF0123456789ABCDEF0123456789ABCDEF01'
    expect(normalizeFrontendBuildId(value)).toBe(value.toLowerCase())
  })

  it('去除首尾空白', () => {
    const value = '  0123456789abcdef0123456789abcdef01234567  '
    expect(normalizeFrontendBuildId(value)).toBe(value.trim())
  })

  it('拒绝非字符串输入', () => {
    expect(normalizeFrontendBuildId(undefined)).toBeUndefined()
    expect(normalizeFrontendBuildId(null)).toBeUndefined()
    expect(normalizeFrontendBuildId(123)).toBeUndefined()
  })

  it('拒绝长度不符的输入', () => {
    expect(normalizeFrontendBuildId('')).toBeUndefined()
    expect(normalizeFrontendBuildId('abc')).toBeUndefined()
    expect(normalizeFrontendBuildId('0123456789abcdef0123456789abcdef0123456789')).toBeUndefined()
  })

  it('拒绝含非十六进制字符的输入', () => {
    expect(normalizeFrontendBuildId('z123456789abcdef0123456789abcdef0123456')).toBeUndefined()
    expect(normalizeFrontendBuildId('g123456789abcdef0123456789abcdef0123456'.slice(0, 40).padEnd(40, '0'))).toBeUndefined()
  })
})
