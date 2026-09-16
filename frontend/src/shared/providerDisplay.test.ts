import { describe, expect, it } from 'vitest'

import { providerDisplayName } from './providerDisplay'

describe('providerDisplayName', () => {
  it('空 code 返回未知供应商', () => {
    expect(providerDisplayName(undefined)).toBe('未知供应商')
    expect(providerDisplayName('')).toBe('未知供应商')
    expect(providerDisplayName('   ')).toBe('未知供应商')
  })

  it('内置供应商 code 映射中文名称', () => {
    expect(providerDisplayName('openai')).toBe('OpenAI 兼容')
    expect(providerDisplayName('gpt')).toBe('GPT')
    expect(providerDisplayName('xai')).toBe('xAI / Grok')
    expect(providerDisplayName('deepseek')).toBe('DeepSeek')
    expect(providerDisplayName('glm')).toBe('智谱 GLM')
    expect(providerDisplayName('anthropic')).toBe('Anthropic')
    expect(providerDisplayName('gemini')).toBe('Google Gemini')
    expect(providerDisplayName('hybrid')).toBe('混合供应商')
  })

  it('未知 code 且无自定义列表时返回未知供应商', () => {
    expect(providerDisplayName('unknown-code')).toBe('未知供应商')
  })

  it('自定义供应商列表按归一化 code 匹配', () => {
    const providers = [
      { code: ' Custom ', name: '自定义供应商' },
      { code: 'other', name: '' }
    ]
    expect(providerDisplayName('custom', providers)).toBe('自定义供应商')
  })

  it('自定义列表名称空白时回退内置映射', () => {
    const providers = [{ code: 'gpt', name: '   ' }]
    expect(providerDisplayName('gpt', providers)).toBe('GPT')
  })

  it('code 大小写与空白归一化后匹配', () => {
    expect(providerDisplayName(' GPT ')).toBe('GPT')
  })
})
