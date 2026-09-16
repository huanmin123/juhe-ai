import { describe, expect, it } from 'vitest'

import {
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  defaultProviderProtocolProfileId,
  isAnthropicProtocolProfile,
  isDeepSeekProviderCode,
  isGeminiProviderCode,
  isGeminiProtocolProfile,
  isGatewaySupportedProtocolProfile,
  isGlmProviderCode,
  isGptVendorCode,
  isHybridProviderCode,
  isOpenAICompatibleProviderCode,
  isOpenAIProtocolProfile,
  isOpenAIProtocolProvider,
  isXaiProviderCode,
  normalizeProviderToken,
  preferredDefaultProvider,
  preferredDefaultProviderCode
} from './providerProtocol'
import type { ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'

function provider(partial: Partial<ProviderDefinition>): ProviderDefinition {
  return {
    id: partial.id ?? 'p1',
    code: partial.code ?? 'openai',
    name: partial.name ?? 'OpenAI',
    enabled: partial.enabled ?? true,
    defaultProtocolProfileId: partial.defaultProtocolProfileId ?? '',
    protocolCode: partial.protocolCode ?? 'openai',
    protocolVersion: partial.protocolVersion ?? 'v1',
    protocolProfiles: partial.protocolProfiles ?? [],
    baseUrl: partial.baseUrl ?? '',
    accountTypes: [],
    capabilities: [],
    ...partial
  } as ProviderDefinition
}

describe('normalizeProviderToken', () => {
  it('统一为小写并去除空白', () => {
    expect(normalizeProviderToken('  OpenAI ')).toBe('openai')
    expect(normalizeProviderToken('GLM')).toBe('glm')
  })

  it('空值与非法值归一化为空字符串', () => {
    expect(normalizeProviderToken(undefined)).toBe('')
    expect(normalizeProviderToken(null)).toBe('')
    expect(normalizeProviderToken(123)).toBe('123')
    expect(normalizeProviderToken('   ')).toBe('')
  })
})

describe('协议判定函数', () => {
  it('isOpenAIProtocolProvider 校验 code 与 version', () => {
    expect(isOpenAIProtocolProvider({ protocolCode: 'OpenAI', protocolVersion: 'V1' })).toBe(true)
    expect(isOpenAIProtocolProvider({ protocolCode: 'openai', protocolVersion: 'v1beta' })).toBe(false)
    expect(isOpenAIProtocolProvider(undefined)).toBe(false)
  })

  it('isOpenAIProtocolProfile / isAnthropicProtocolProfile / isGeminiProtocolProfile', () => {
    expect(isOpenAIProtocolProfile({ protocolCode: 'openai', protocolVersion: 'v1' })).toBe(true)
    expect(isAnthropicProtocolProfile({ protocolCode: 'anthropic', protocolVersion: 'v1' })).toBe(true)
    expect(isGeminiProtocolProfile({ protocolCode: 'gemini', protocolVersion: 'v1beta' })).toBe(true)
    expect(isAnthropicProtocolProfile({ protocolCode: 'gemini', protocolVersion: 'v1beta' })).toBe(false)
    expect(isGeminiProtocolProfile({ protocolCode: 'openai', protocolVersion: 'v1' })).toBe(false)
    expect(isOpenAIProtocolProfile(undefined)).toBe(false)
  })

  it('isGatewaySupportedProtocolProfile 覆盖三种网关协议', () => {
    expect(isGatewaySupportedProtocolProfile({ protocolCode: 'openai', protocolVersion: 'v1' })).toBe(true)
    expect(isGatewaySupportedProtocolProfile({ protocolCode: 'anthropic', protocolVersion: 'v1' })).toBe(true)
    expect(isGatewaySupportedProtocolProfile({ protocolCode: 'gemini', protocolVersion: 'v1beta' })).toBe(true)
    expect(isGatewaySupportedProtocolProfile({ protocolCode: 'other', protocolVersion: 'v1' })).toBe(false)
  })
})

describe('供应商 code 判定函数', () => {
  it('大小写与空白归一后判定', () => {
    expect(isGptVendorCode(' GPT ')).toBe(true)
    expect(isGptVendorCode('gpt-x')).toBe(false)
    expect(isXaiProviderCode('XAI')).toBe(true)
    expect(isDeepSeekProviderCode(' deepseek ')).toBe(true)
    expect(isGlmProviderCode('GLM')).toBe(true)
    expect(isGeminiProviderCode('Gemini')).toBe(true)
    expect(isHybridProviderCode('Hybrid')).toBe(true)
    expect(isGptVendorCode(undefined)).toBe(false)
  })

  it('isOpenAICompatibleProviderCode 接受 openai 与 gpt', () => {
    expect(isOpenAICompatibleProviderCode('openai')).toBe(true)
    expect(isOpenAICompatibleProviderCode('gpt')).toBe(true)
    expect(isOpenAICompatibleProviderCode('glm')).toBe(false)
    expect(isOpenAICompatibleProviderCode('')).toBe(false)
  })
})

describe('preferredDefaultProvider', () => {
  it('优先选择启用的 gpt 供应商', () => {
    const gpt = provider({ id: 'g1', code: 'gpt' })
    const glm = provider({ id: 'g2', code: 'glm' })
    expect(preferredDefaultProvider([glm, gpt])).toBe(gpt)
  })

  it('无 gpt 时选择第一个启用供应商', () => {
    const disabled = provider({ id: 'd1', code: 'gpt', enabled: false })
    const glm = provider({ id: 'g2', code: 'glm' })
    const deepseek = provider({ id: 'g3', code: 'deepseek' })
    expect(preferredDefaultProvider([disabled, glm, deepseek])).toBe(glm)
  })

  it('全部未启用或为空时返回 undefined', () => {
    expect(preferredDefaultProvider([provider({ enabled: false })])).toBeUndefined()
    expect(preferredDefaultProvider([])).toBeUndefined()
  })

  it('preferredDefaultProviderCode 无默认时返回空字符串', () => {
    expect(preferredDefaultProviderCode([provider({ code: 'gpt' })])).toBe('gpt')
    expect(preferredDefaultProviderCode([])).toBe('')
  })
})

describe('defaultProviderProtocolProfileId', () => {
  const profiles = [
    { id: 'profile_a', providerCode: 'openai', name: 'A', enabled: false, protocolCode: 'openai', protocolVersion: 'v1', baseUrl: '' },
    { id: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID, providerCode: 'openai', name: 'B', enabled: true, protocolCode: 'openai', protocolVersion: 'v1', baseUrl: '' }
  ] as unknown as ProviderProtocolProfileDefinition[]

  it('优先返回 defaultProtocolProfileId 命中的 profile', () => {
    expect(defaultProviderProtocolProfileId({ defaultProtocolProfileId: 'profile_a', protocolProfiles: profiles })).toBe('profile_a')
  })

  it('default 未命中时返回第一个启用的 profile', () => {
    expect(defaultProviderProtocolProfileId({ defaultProtocolProfileId: 'missing', protocolProfiles: profiles })).toBe(OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID)
  })

  it('无启用 profile 时回退第一个 profile', () => {
    const allDisabled = [profiles[0]]
    expect(defaultProviderProtocolProfileId({ defaultProtocolProfileId: 'missing', protocolProfiles: allDisabled })).toBe('profile_a')
  })

  it('profile 列表为空时回退 defaultProtocolProfileId 原值', () => {
    expect(defaultProviderProtocolProfileId({ defaultProtocolProfileId: 'legacy', protocolProfiles: [] })).toBe('legacy')
    expect(defaultProviderProtocolProfileId({ defaultProtocolProfileId: '', protocolProfiles: [] })).toBe('')
  })

  it('供应商为空时返回空字符串', () => {
    expect(defaultProviderProtocolProfileId(undefined)).toBe('')
  })
})
