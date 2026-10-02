import { describe, expect, it } from 'vitest'

import {
  createDefaultSystemForm,
  parseUpstreamClientVersionOverrides,
  serializeUpstreamClientVersionOverrides,
} from './settingsForm'

describe('parseUpstreamClientVersionOverrides', () => {
  it('合法对象解析到五字段，非法项忽略', () => {
    const form = parseUpstreamClientVersionOverrides({
      codex: '0.160.0',
      grokCLI: '1.0.14',
      other: '1.0.0',
      zcode: 'bad-version',
    })
    expect(form.codex).toBe('0.160.0')
    expect(form.grokCLI).toBe('1.0.14')
    expect(form.claudeCode).toBe('')
    expect(form.geminiCLI).toBe('')
    expect(form.zcode).toBe('')
  })

  it('undefined / 非对象输入落到全空表单', () => {
    expect(parseUpstreamClientVersionOverrides(undefined)).toEqual(createDefaultSystemForm().upstreamClientVersionOverrides)
    expect(parseUpstreamClientVersionOverrides('codex')).toEqual(createDefaultSystemForm().upstreamClientVersionOverrides)
  })
})

describe('serializeUpstreamClientVersionOverrides', () => {
  it('只序列化非空字段', () => {
    const form = { codex: '0.160.0', claudeCode: '', geminiCLI: '', zcode: '3.15.0', grokCLI: '' }
    expect(serializeUpstreamClientVersionOverrides(form)).toEqual({ codex: '0.160.0', zcode: '3.15.0' })
  })

  it('全空表单序列化为空对象（= 全部使用内置）', () => {
    const form = createDefaultSystemForm().upstreamClientVersionOverrides
    expect(serializeUpstreamClientVersionOverrides(form)).toEqual({})
  })

  it('非法 semver 抛错（保存校验兜底）', () => {
    const form = { codex: '0.160', claudeCode: '', geminiCLI: '', zcode: '', grokCLI: '' }
    expect(() => serializeUpstreamClientVersionOverrides(form)).toThrow()
  })

  it('parse/serialize 往返保持一致', () => {
    const stored = { claudeCode: '2.1.300', geminiCLI: '0.62.0' }
    const round = serializeUpstreamClientVersionOverrides(parseUpstreamClientVersionOverrides(stored))
    expect(round).toEqual(stored)
  })
})
