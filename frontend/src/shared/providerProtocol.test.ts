import { describe, expect, it } from 'vitest'

import {
  GPT_OPENAI_V1_PROFILE_ID,
  HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID,
  HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID,
  MINIMAX_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  QWEN_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID,
  connectionPresetCodeForProfile,
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
  preferredDefaultProviderCode,
  providerIdentityForConnectionPreset,
  resolveEditAccountProviderIdentity
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

describe('connectionPresetCodeForProfile 正向导出（T102 openspeech 补条目）', () => {
  it('volcengine_openspeech 档案正向导出 OpenSpeech 连接 preset', () => {
    expect(connectionPresetCodeForProfile(VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID)).toBe('volcengine_openspeech')
  })

  it('既有媒体档案正向导出不回归', () => {
    expect(connectionPresetCodeForProfile(MINIMAX_OPENAI_V1_PROFILE_ID)).toBe('minimax_media')
    expect(connectionPresetCodeForProfile(VOLCENGINE_OPENAI_V1_PROFILE_ID)).toBe('volcengine_ark')
    expect(connectionPresetCodeForProfile(QWEN_OPENAI_V1_PROFILE_ID)).toBe('qwen_dashscope')
  })
})

describe('providerIdentityForConnectionPreset 反查（T102 媒体四档 + generic）', () => {
  it('四个媒体 preset 各自派生出正确的供应商与档案身份', () => {
    expect(providerIdentityForConnectionPreset('minimax_media')).toEqual({
      providerCode: 'minimax',
      providerProtocolProfileId: MINIMAX_OPENAI_V1_PROFILE_ID
    })
    expect(providerIdentityForConnectionPreset('volcengine_ark')).toEqual({
      providerCode: 'volcengine',
      providerProtocolProfileId: VOLCENGINE_OPENAI_V1_PROFILE_ID
    })
    expect(providerIdentityForConnectionPreset('volcengine_openspeech')).toEqual({
      providerCode: 'volcengine',
      providerProtocolProfileId: VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID
    })
    expect(providerIdentityForConnectionPreset('qwen_dashscope')).toEqual({
      providerCode: 'qwen',
      providerProtocolProfileId: QWEN_OPENAI_V1_PROFILE_ID
    })
  })

  it('generic_http 账户（generic_openai_v1）派生出非空 openai 兼容身份', () => {
    expect(providerIdentityForConnectionPreset('generic_openai_v1')).toEqual({
      providerCode: 'openai',
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID
    })
  })

  it('未知/空白 preset 保持 fail-closed（返回 undefined）', () => {
    expect(providerIdentityForConnectionPreset('volcengine_imagex')).toBeUndefined()
    expect(providerIdentityForConnectionPreset('future_preset_v9')).toBeUndefined()
    expect(providerIdentityForConnectionPreset('')).toBeUndefined()
    expect(providerIdentityForConnectionPreset(undefined)).toBeUndefined()
    expect(providerIdentityForConnectionPreset(null)).toBeUndefined()
  })

  it('反查表内除 hybrid 两条外逐条经正向表往返一致（双射护栏）', () => {
    // mapping.go legacyProfilePresets 双射逆向：反查 → 档案 → 正向导出必须回到原 preset。
    // hybrid 两档案（generic_anthropic/generic_gemini 的落地档案）无正向 preset 条目，
    // 保存链不携带 connections，属登记过的例外。
    const hybridOnlyProfiles = new Set([HYBRID_ANTHROPIC_MESSAGES_V1_PROFILE_ID, HYBRID_GEMINI_NATIVE_V1BETA_PROFILE_ID])
    for (const [preset, identity] of Object.entries({
      generic_openai_v1: providerIdentityForConnectionPreset('generic_openai_v1'),
      gpt_openai_v1: providerIdentityForConnectionPreset('gpt_openai_v1'),
      xai_openai_v1: providerIdentityForConnectionPreset('xai_openai_v1'),
      deepseek_openai_v1: providerIdentityForConnectionPreset('deepseek_openai_v1'),
      deepseek_anthropic_v1: providerIdentityForConnectionPreset('deepseek_anthropic_v1'),
      anthropic_v1: providerIdentityForConnectionPreset('anthropic_v1'),
      glm_general_openai_v1: providerIdentityForConnectionPreset('glm_general_openai_v1'),
      glm_coding_openai_v1: providerIdentityForConnectionPreset('glm_coding_openai_v1'),
      glm_coding_anthropic_v1: providerIdentityForConnectionPreset('glm_coding_anthropic_v1'),
      gemini_native_v1beta: providerIdentityForConnectionPreset('gemini_native_v1beta'),
      gemini_openai_chat_v1beta: providerIdentityForConnectionPreset('gemini_openai_chat_v1beta'),
      minimax_media: providerIdentityForConnectionPreset('minimax_media'),
      volcengine_ark: providerIdentityForConnectionPreset('volcengine_ark'),
      volcengine_openspeech: providerIdentityForConnectionPreset('volcengine_openspeech'),
      qwen_dashscope: providerIdentityForConnectionPreset('qwen_dashscope')
    })) {
      expect(identity, preset).toBeDefined()
      if (!identity) continue
      if (hybridOnlyProfiles.has(identity.providerProtocolProfileId)) continue
      expect(connectionPresetCodeForProfile(identity.providerProtocolProfileId), preset).toBe(preset)
    }
  })
})

describe('resolveEditAccountProviderIdentity 编辑装载派生', () => {
  it('媒体账户：详情档案为空 + 连接 preset 命中 → 按反查表派生', () => {
    expect(resolveEditAccountProviderIdentity({
      providerCode: 'minimax',
      providerProtocolProfileId: '',
      connectionPresetCode: 'minimax_media'
    })).toEqual({ providerCode: 'minimax', providerProtocolProfileId: MINIMAX_OPENAI_V1_PROFILE_ID })
  })

  it('OpenSpeech 音频账户：派生 OpenSpeech 档案（保存链正向导出同 preset，改 Base URL 闭环）', () => {
    const identity = resolveEditAccountProviderIdentity({
      providerCode: 'volcengine',
      providerProtocolProfileId: '',
      connectionPresetCode: 'volcengine_openspeech'
    })
    expect(identity).toEqual({
      providerCode: 'volcengine',
      providerProtocolProfileId: VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID
    })
    expect(connectionPresetCodeForProfile(identity.providerProtocolProfileId)).toBe('volcengine_openspeech')
  })

  it('generic_http 账户：探针目录 id（profile_generic_*）经 preset 反查为前端档案且供应商非空', () => {
    expect(resolveEditAccountProviderIdentity({
      providerCode: '',
      providerProtocolProfileId: 'profile_generic_openai_v1',
      connectionPresetCode: 'generic_openai_v1'
    })).toEqual({ providerCode: 'openai', providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID })
  })

  it('详情档案前端可识别时原样保留（不因反查表改写既有身份）', () => {
    expect(resolveEditAccountProviderIdentity({
      providerCode: 'gpt',
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      connectionPresetCode: 'gpt_openai_v1'
    })).toEqual({ providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID })
  })

  it('未知 preset：账户行 providerCode 直接使用（vendor 账户兜底）', () => {
    expect(resolveEditAccountProviderIdentity({
      providerCode: 'minimax',
      providerProtocolProfileId: '',
      connectionPresetCode: 'future_preset_v9'
    })).toEqual({ providerCode: 'minimax', providerProtocolProfileId: '' })
  })

  it('未知 preset 且无供应商身份：保持 fail-closed（双空，由保存校验给出可操作文案）', () => {
    expect(resolveEditAccountProviderIdentity({
      providerCode: '',
      providerProtocolProfileId: '',
      connectionPresetCode: 'future_preset_v9'
    })).toEqual({ providerCode: '', providerProtocolProfileId: '' })
  })
})
