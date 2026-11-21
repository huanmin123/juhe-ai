import { describe, expect, it } from 'vitest'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID,
  MINIMAX_OPENAI_V1_PROFILE_ID,
  QWEN_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID,
  XAI_OPENAI_V1_PROFILE_ID
} from '@/shared/providerProtocol'
import type { ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import {
  allAccountEndpointModes,
  defaultEndpointModesForAccount,
  endpointModesForProfile,
  type AccountProviderProfileLike
} from './accountProviderCapabilities'
import { providerModelsForProtocolProfile } from './accountEditFormPayload'
import {
  ACCOUNT_CAPABILITY_DOMAINS,
  ACCOUNT_CAPABILITY_DOMAIN_LABELS,
  ACCOUNT_CAPABILITY_DOMAIN_RULE_VERSION,
  DOMAIN_ENDPOINT_MODES_AUDIO,
  DOMAIN_ENDPOINT_MODES_CHAT,
  DOMAIN_ENDPOINT_MODES_EMBEDDING,
  DOMAIN_ENDPOINT_MODES_IMAGE,
  DOMAIN_ENDPOINT_MODES_RERANK,
  DOMAIN_ENDPOINT_MODES_VIDEO,
  DOMAIN_MODEL_MODES,
  accountCapabilityDomainLabel,
  allowedEndpointModesFor,
  capabilityDomainAccountTypes,
  capabilityDomainIsMedia,
  capabilityDomainSupportsDeclaration,
  defaultEndpointModesForCapabilityDomain,
  defaultHealthCheckEndpointModeForCapabilityDomain,
  defaultStatusForCapabilityDomain,
  deriveCapabilityDomain,
  mediaDomainModelProtocolTokens,
  preferredMediaDomainProviderCode
} from './accountCapabilityDomains'
import { FALLBACK_PROVIDERS } from './accountOptions'
import { mergeAccountProviderDefinitions } from './useAccountEditForm'

describe('能力域枚举与标签', () => {
  it('域枚举固定为六值，顺序与 CHECK 约束一致（向量/重排拆分）', () => {
    expect([...ACCOUNT_CAPABILITY_DOMAINS]).toEqual(['chat', 'embedding', 'rerank', 'video', 'audio', 'image'])
  })

  it('中文标签覆盖全部域', () => {
    expect(ACCOUNT_CAPABILITY_DOMAIN_LABELS).toEqual({
      chat: '对话',
      embedding: '向量',
      rerank: '重排',
      video: '视频',
      audio: '音频',
      image: '图像'
    })
    for (const domain of ACCOUNT_CAPABILITY_DOMAINS) {
      expect(accountCapabilityDomainLabel(domain)).toBe(ACCOUNT_CAPABILITY_DOMAIN_LABELS[domain])
    }
  })

  it('推导规则版本固定为 ruleVersion=2（向量/重排拆分）', () => {
    expect(ACCOUNT_CAPABILITY_DOMAIN_RULE_VERSION).toBe(2)
  })
})

describe('对话 modes 集合完整性', () => {
  it('锚集合恰为 13 值（向量/重排拆分移出两 mode）且顺序与设计 lane 表一致', () => {
    expect([...DOMAIN_ENDPOINT_MODES_CHAT]).toEqual([
      'chat_json',
      'chat_sse',
      'responses_json',
      'responses_sse',
      'messages_json',
      'messages_sse',
      'generate_content_json',
      'generate_content_sse',
      'interactions_json',
      'interactions_sse',
      'count_tokens',
      'embed_content',
      'message_token_counting'
    ])
  })

  it('向量/重排独立域 lane 恰为单值', () => {
    expect([...DOMAIN_ENDPOINT_MODES_EMBEDDING]).toEqual(['embeddings_json'])
    expect([...DOMAIN_ENDPOINT_MODES_RERANK]).toEqual(['rerank_json'])
  })

  it('锚集合无重复值', () => {
    expect(new Set(DOMAIN_ENDPOINT_MODES_CHAT).size).toBe(DOMAIN_ENDPOINT_MODES_CHAT.length)
  })

  it('锚集合全部值都在前端端点模式词表内', () => {
    const vocabulary = new Set<string>(allAccountEndpointModes)
    for (const mode of DOMAIN_ENDPOINT_MODES_CHAT) {
      expect(vocabulary.has(mode), `对话 modes 锚集合出现词表外的值：${mode}`).toBe(true)
    }
  })

  it('媒体触发集合取值与顺序固定（v8-A 音频域补短转写与实时会话）', () => {
    expect([...DOMAIN_ENDPOINT_MODES_VIDEO]).toEqual(['video_create', 'video_get', 'video_content', 'video_cancel'])
    expect([...DOMAIN_ENDPOINT_MODES_AUDIO]).toEqual([
      'audio_speech',
      'audio_job_create',
      'audio_job_get',
      'audio_job_content',
      'audio_job_cancel',
      'audio_transcription_json',
      'realtime_session'
    ])
    expect([...DOMAIN_ENDPOINT_MODES_IMAGE]).toEqual(['images_json'])
  })

  it('对话锚集合与媒体触发集合两两不相交', () => {
    const chatModes = new Set<string>(DOMAIN_ENDPOINT_MODES_CHAT)
    for (const mode of [...DOMAIN_ENDPOINT_MODES_VIDEO, ...DOMAIN_ENDPOINT_MODES_AUDIO, ...DOMAIN_ENDPOINT_MODES_IMAGE]) {
      expect(chatModes.has(mode), `媒体触发值 ${mode} 不得出现在对话 modes 锚集合`).toBe(false)
    }
  })

  it('v8-A 对话域纯粹化：被移出三项不得再出现在对话锚集合', () => {
    const chatModes = new Set<string>(DOMAIN_ENDPOINT_MODES_CHAT)
    for (const mode of ['images_json', 'audio_transcription_json', 'realtime_session'] as const) {
      expect(chatModes.has(mode), `v8-A 后 ${mode} 不得再出现在对话锚集合`).toBe(false)
    }
    const audioModes = new Set<string>(DOMAIN_ENDPOINT_MODES_AUDIO)
    expect(audioModes.has('audio_transcription_json')).toBe(true)
    expect(audioModes.has('realtime_session')).toBe(true)
  })

  it('向量/重排拆分：embeddings_json / rerank_json 不得再出现在对话锚集合', () => {
    const chatModes = new Set<string>(DOMAIN_ENDPOINT_MODES_CHAT)
    for (const mode of ['embeddings_json', 'rerank_json'] as const) {
      expect(chatModes.has(mode), `拆分后 ${mode} 不得再出现在对话锚集合`).toBe(false)
    }
  })
})

describe('deriveCapabilityDomain 推导规则（ruleVersion=2）', () => {
  it.each([
    {
      name: '纯对话：chat_json/chat_sse 命中对话 modes，归 chat（规则⑤前置命中）',
      modes: ['chat_json', 'chat_sse'],
      expected: 'chat'
    },
    {
      name: '纯对话：messages_json 单值亦归 chat',
      modes: ['messages_json'],
      expected: 'chat'
    },
    {
      name: '对话辅助 modes：message_token_counting/count_tokens/embed_content 属对话锚集合',
      modes: ['count_tokens', 'embed_content', 'message_token_counting'],
      expected: 'chat'
    },
    {
      name: '向量/重排拆分：纯 embeddings_json 声明落 embedding 域（规则②）',
      modes: ['embeddings_json'],
      expected: 'embedding'
    },
    {
      name: '向量/重排拆分：纯 rerank_json 声明落 rerank 域（规则③）',
      modes: ['rerank_json'],
      expected: 'rerank'
    },
    {
      name: '向量/重排混合声明（无对话/媒体触发）：规则②③互斥，照 v1 落 chat（迁移失败清单人工处理）',
      modes: ['embeddings_json', 'rerank_json'],
      expected: 'chat'
    },
    {
      name: '混合声明：embeddings_json + 对话生成形态不拆域，归 chat（迁移失败清单人工处理）',
      modes: ['embeddings_json', 'chat_json', 'chat_sse'],
      expected: 'chat'
    },
    {
      name: '混合声明：rerank_json + 对话生成形态不拆域，归 chat',
      modes: ['rerank_json', 'responses_sse'],
      expected: 'chat'
    },
    {
      name: '混合声明：embeddings_json + 媒体触发值照 v1 落 chat（媒体分支排除新域 mode，与后端 DeriveDomain 同构）',
      modes: ['embeddings_json', 'images_json'],
      expected: 'chat'
    },
    {
      name: '混合声明：embeddings_json + audio_speech 照 v1 落 chat（媒体分支排除新域 mode）',
      modes: ['embeddings_json', 'audio_speech'],
      expected: 'chat'
    },
    {
      name: '混合声明：rerank_json + video_create 照 v1 落 chat（后端镜像钉值）',
      modes: ['rerank_json', 'video_create'],
      expected: 'chat'
    },
    {
      name: '三向混合（向量+重排+对话锚）落 chat',
      modes: ['embeddings_json', 'rerank_json', 'chat_sse'],
      expected: 'chat'
    },
    {
      name: 'v8-A 对话域纯粹化：只声明短转写/实时会话归音频域（不再是对话锚集成员）',
      modes: ['realtime_session', 'audio_transcription_json'],
      expected: 'audio'
    },
    {
      name: 'v8-A：短转写单值归音频域',
      modes: ['audio_transcription_json'],
      expected: 'audio'
    },
    {
      name: 'v8-A：实时会话单值归音频域',
      modes: ['realtime_session'],
      expected: 'audio'
    },
    {
      name: 'v8-A：对话 + 短转写混合仍归 chat（含对话 modes 即停）',
      modes: ['chat_json', 'audio_transcription_json'],
      expected: 'chat'
    },
    {
      name: 'volcengine 混合：对话 + 视频四值 + audio_speech 同声明，归 chat',
      modes: ['chat_json', 'chat_sse', 'video_create', 'video_get', 'video_content', 'video_cancel', 'audio_speech'],
      expected: 'chat'
    },
    {
      name: 'gemini+veo：generate_content/interactions + 视频四值，归 chat',
      modes: ['generate_content_json', 'generate_content_sse', 'interactions_json', 'interactions_sse', 'video_create', 'video_get', 'video_content', 'video_cancel'],
      expected: 'chat'
    },
    {
      name: '对话 modes 否决一切媒体规则：chat_json + 全量媒体值仍归 chat',
      modes: ['chat_json', 'images_json', 'audio_speech', 'audio_job_create', 'video_create'],
      expected: 'chat'
    },
    {
      name: 'minimax 纯媒体：视频四值 + audio_speech，规则②先于③命中，归 video',
      modes: ['video_create', 'video_get', 'video_content', 'video_cancel', 'audio_speech'],
      expected: 'video'
    },
    {
      name: '纯视频四值，归 video',
      modes: ['video_create', 'video_get', 'video_content', 'video_cancel'],
      expected: 'video'
    },
    {
      name: '长音频四值 + images_json（无对话、无视频触发、无 audio_speech），规则③先于④命中，归 audio',
      modes: ['audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel', 'images_json'],
      expected: 'audio'
    },
    {
      name: 'audio_speech 单值，归 audio',
      modes: ['audio_speech'],
      expected: 'audio'
    },
    {
      name: 'images_json 单值，归 image',
      modes: ['images_json'],
      expected: 'image'
    },
    {
      name: 'images_json + audio_speech（无视频），规则③先于④命中，归 audio',
      modes: ['images_json', 'audio_speech'],
      expected: 'audio'
    },
    {
      name: '空 modes：规则⑤兜底，归 chat',
      modes: [],
      expected: 'chat'
    },
    {
      name: '未知模式单值：不命中任何触发集合、不做通配符推断，归 chat',
      modes: ['totally_unknown_mode'],
      expected: 'chat'
    },
    {
      name: '全部未知模式（含 future_video_* 形态）：不自动视为媒体能力，归 chat',
      modes: ['future_video_export', 'future_audio_job_create'],
      expected: 'chat'
    },
    {
      name: '未知模式与已知视频值并存：已知值命中规则②，归 video',
      modes: ['video_create', 'future_video_export'],
      expected: 'video'
    },
    {
      name: '视频四值 + 长音频四值 + images_json（无对话）：按序命中即停，归 video',
      modes: ['video_create', 'video_get', 'video_content', 'video_cancel', 'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel', 'images_json'],
      expected: 'video'
    }
  ])('$name', ({ modes, expected }) => {
    expect(deriveCapabilityDomain(modes)).toBe(expected)
  })
})

// ---- M2 允许集/默认集镜像（五域退役后允许集/默认集仅 chat 持有；创建墙矩阵由 capabilityDomainAccountTypes 承担） ----

function fallbackProfileById(id: string): AccountProviderProfileLike {
  const provider = FALLBACK_PROVIDERS.find((item) => item.protocolProfiles.some((profile) => profile.id === id))
  const profile = provider?.protocolProfiles.find((item) => item.id === id)
  expect(profile, `FALLBACK_PROVIDERS 缺少档案 ${id}`).toBeTruthy()
  return profile as AccountProviderProfileLike
}

// 三家媒体供应商现在也在 FALLBACK_PROVIDERS，直接复用真实回退档案，
// 避免测试自造档案与生产回退形状漂移（T47）。
const gptProfile = fallbackProfileById(GPT_OPENAI_V1_PROFILE_ID)
const xaiProfile = fallbackProfileById(XAI_OPENAI_V1_PROFILE_ID)
const glmProfile = fallbackProfileById(GLM_GENERAL_OPENAI_V1_PROFILE_ID)
const deepseekProfile = fallbackProfileById(DEEPSEEK_OPENAI_V1_PROFILE_ID)
const anthropicProfile = fallbackProfileById(ANTHROPIC_ANTHROPIC_V1_PROFILE_ID)
const geminiNativeProfile = fallbackProfileById(GEMINI_NATIVE_V1BETA_PROFILE_ID)
const geminiOpenAIChatProfile = fallbackProfileById(GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID)
const minimaxProfile = fallbackProfileById(MINIMAX_OPENAI_V1_PROFILE_ID)
const volcengineProfile = fallbackProfileById(VOLCENGINE_OPENAI_V1_PROFILE_ID)
const volcengineOpenSpeechProfile = fallbackProfileById(VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID)
const qwenProfile = fallbackProfileById(QWEN_OPENAI_V1_PROFILE_ID)

describe('allowedEndpointModesFor：chat 域允许集 = 13 值对话锚集合 ∩ 供应商词表（v8-A + 向量/重排拆分）', () => {
  it('gpt：chat×2+responses×2（embeddings_json 已随向量域拆分退出 chat 域）', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: gptProfile, accountType: 'api_key' })).toEqual([
      'chat_json',
      'chat_sse',
      'responses_json',
      'responses_sse'
    ])
  })

  it('anthropic：仅 messages 对 + token counting，无协议面可选项', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: anthropicProfile, accountType: 'api_key' })).toEqual([
      'messages_json',
      'messages_sse',
      'message_token_counting'
    ])
  })

  it('deepseek / glm / xai / gemini OpenAI Chat：v8-A 后收敛对话族，向量/重排 mode 不再出现在任何 chat 词表', () => {
    const deepseekAllowed = allowedEndpointModesFor({ capabilityDomain: 'chat', profile: deepseekProfile, accountType: 'api_key' })
    expect(deepseekAllowed).toContain('responses_json')
    expect(deepseekAllowed).not.toContain('images_json')
    expect(deepseekAllowed).not.toContain('audio_transcription_json')
    expect(deepseekAllowed).not.toContain('realtime_session')
    expect(deepseekAllowed).not.toContain('video_create')
    expect(deepseekAllowed).not.toContain('embeddings_json')
    expect(deepseekAllowed).not.toContain('rerank_json')
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: glmProfile, accountType: 'api_key' })).toEqual([
      'chat_json',
      'chat_sse'
    ])
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: xaiProfile, accountType: 'api_key' })).toContain('responses_sse')
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: geminiOpenAIChatProfile, accountType: 'api_key' })).toEqual([
      'chat_json',
      'chat_sse'
    ])
    expect(endpointModesForProfile(geminiOpenAIChatProfile)).toEqual(['chat_json', 'chat_sse'])
    // OpenAI/OAuth 与后端 openAIFamilyVocabulary(oauth) 对齐：只允许 responses
    // 对，不得因共用 profile 展示 chat 或 embeddings。
    expect(endpointModesForProfile(gptProfile, 'oauth')).toEqual(['responses_json', 'responses_sse'])
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: gptProfile, accountType: 'oauth' })).toEqual([
      'responses_json',
      'responses_sse'
    ])
    // 拆分后缺省域参数按 chat 收敛：词表不再含 embeddings_json / rerank_json。
    expect(endpointModesForProfile(gptProfile)).not.toContain('embeddings_json')
    expect(endpointModesForProfile(qwenProfile)).not.toContain('embeddings_json')
    expect(endpointModesForProfile(qwenProfile)).not.toContain('rerank_json')
  })

  it('向量/重排独立域：五域退役后允许集恒为空集（声明集仅 chat 持有）', () => {
    // 2026-11-21 五域退役（AI账户上游接口能力设计 §3）：embedding/rerank 域不再
    // 有「允许集」概念，任何供应商×账户类型组合都返回空集；创建墙类型判定由
    // capabilityDomainAccountTypes 承担（见下方用例）。
    for (const profile of [gptProfile, qwenProfile, glmProfile, volcengineProfile, xaiProfile, deepseekProfile, anthropicProfile, geminiNativeProfile, geminiOpenAIChatProfile, minimaxProfile]) {
      expect(allowedEndpointModesFor({ capabilityDomain: 'embedding', profile, accountType: 'api_key' })).toEqual([])
      expect(allowedEndpointModesFor({ capabilityDomain: 'rerank', profile, accountType: 'api_key' })).toEqual([])
      expect(allowedEndpointModesFor({ capabilityDomain: 'embedding', profile, accountType: 'oauth' })).toEqual([])
      expect(allowedEndpointModesFor({ capabilityDomain: 'rerank', profile, accountType: 'oauth' })).toEqual([])
    }
  })

  it('向量/重排域可建账户类型：api_key 按词表求交非空保留，oauth 自然被过滤', () => {
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'embedding', providerCode: 'gpt', profile: gptProfile })).toEqual(['api_key'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'embedding', providerCode: 'qwen', profile: qwenProfile })).toEqual(['api_key'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'rerank', providerCode: 'qwen', profile: qwenProfile })).toEqual(['api_key'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'embedding', providerCode: 'glm', profile: glmProfile })).toEqual([])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'rerank', providerCode: 'gpt', profile: gptProfile })).toEqual([])
  })

  it('gemini native：generateContent/interactions/countTokens/embedContent，images_json 不在词表', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: geminiNativeProfile, accountType: 'api_key' })).toEqual([
      'generate_content_json',
      'generate_content_sse',
      'interactions_json',
      'interactions_sse',
      'count_tokens',
      'embed_content'
    ])
  })

  it('minimax 纯媒体词表与 chat 域求交为空集（无 chat 域）', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: minimaxProfile, accountType: 'api_key' })).toEqual([])
  })

  it('允许集是词表与域集合的子集：任何域结果都不含词表外值', () => {
    const profiles = [gptProfile, xaiProfile, glmProfile, deepseekProfile, anthropicProfile, geminiNativeProfile, geminiOpenAIChatProfile, minimaxProfile, volcengineProfile, qwenProfile]
    const vocabulary = new Set<string>(allAccountEndpointModes)
    for (const profile of profiles) {
      for (const domain of ACCOUNT_CAPABILITY_DOMAINS) {
        for (const mode of allowedEndpointModesFor({ capabilityDomain: domain, profile, accountType: 'api_key' })) {
          expect(vocabulary.has(mode), `${profile.providerCode}/${profile.id} 域 ${domain} 出现词表外值 ${mode}`).toBe(true)
        }
      }
    }
  })

  it('自定义向量/重排模型按域参数化的档案可声明 mode 进入账户模型下拉', () => {
    const models = [
      { label: 'custom-embed', value: 'custom-embed', supportedApiProtocols: ['embeddings' as const] },
      { label: 'custom-rerank', value: 'custom-rerank', supportedApiProtocols: ['rerank' as const] },
      { label: 'custom-chat', value: 'custom-chat', supportedApiProtocols: ['chat_completions' as const] }
    ]
    // chat 域（缺省）：拆分后 chat 词表不含 embeddings_json / rerank_json，
    // embedding/rerank 模型不再进入对话账户模型下拉（域 mode 白名单同步收窄）。
    const gptValues = providerModelsForProtocolProfile(models, gptProfile as unknown as ProviderProtocolProfileDefinition).map((model) => model.value)
    expect(gptValues).toContain('custom-chat')
    expect(gptValues).not.toContain('custom-embed')
    expect(gptValues).not.toContain('custom-rerank')
    // embedding 域：gpt/qwen 词表放行 embeddings 协议；rerank 协议不进 gpt。
    const gptEmbeddingValues = providerModelsForProtocolProfile(
      models,
      gptProfile as unknown as ProviderProtocolProfileDefinition,
      'api_key',
      'embedding'
    ).map((model) => model.value)
    expect(gptEmbeddingValues).toContain('custom-embed')
    expect(gptEmbeddingValues).toContain('custom-chat')
    expect(gptEmbeddingValues).not.toContain('custom-rerank')
    // rerank 域：仅 qwen 词表放行 rerank 协议；embeddings 协议不随 rerank 域放行。
    const qwenRerankValues = providerModelsForProtocolProfile(
      models,
      qwenProfile as unknown as ProviderProtocolProfileDefinition,
      'api_key',
      'rerank'
    ).map((model) => model.value)
    expect(qwenRerankValues).toContain('custom-rerank')
    expect(qwenRerankValues).not.toContain('custom-embed')
    const qwenEmbeddingValues = providerModelsForProtocolProfile(
      models,
      qwenProfile as unknown as ProviderProtocolProfileDefinition,
      'api_key',
      'embedding'
    ).map((model) => model.value)
    expect(qwenEmbeddingValues).toContain('custom-embed')
    expect(qwenEmbeddingValues).not.toContain('custom-rerank')
    // volcengine 现役向量面报 400（词表无 embeddings_json）：embedding 域不放行。
    const volcValues = providerModelsForProtocolProfile(
      models,
      volcengineProfile as unknown as ProviderProtocolProfileDefinition,
      'api_key',
      'embedding'
    ).map((model) => model.value)
    expect(volcValues).not.toContain('custom-embed')
    // glm 的 OpenAI 收窄词表不含 embeddings_json（后端 glmOpenAINarrowedVocabulary），
    // embedding 域也不得把 embedding 模型展示成可绑定。
    const glmValues = providerModelsForProtocolProfile(
      models,
      glmProfile as unknown as ProviderProtocolProfileDefinition,
      'api_key',
      'embedding'
    ).map((model) => model.value)
    expect(glmValues).not.toContain('custom-embed')
    expect(glmValues).not.toContain('custom-rerank')
    // OpenAI OAuth 写侧只保留 Responses 对：模型下拉必须收敛到 responses，
    // 不能展示 chat-only/embedding/rerank 模型（后端 openAIFamilyVocabulary("oauth")）。
    const oauthModels = [
      ...models,
      { label: 'custom-responses', value: 'custom-responses', supportedApiProtocols: ['responses' as const] }
    ]
    const oauthValues = providerModelsForProtocolProfile(
      oauthModels,
      gptProfile as unknown as ProviderProtocolProfileDefinition,
      'oauth'
    ).map((model) => model.value)
    expect(oauthValues).toContain('custom-responses')
    expect(oauthValues).not.toContain('custom-chat')
    expect(oauthValues).not.toContain('custom-embed')
    expect(oauthValues).not.toContain('custom-rerank')
  })
})

describe('allowedEndpointModesFor：媒体域五域退役（允许集恒空，矩阵语义由创建墙承担）', () => {
  // 2026-11-21 五域退役（AI账户上游接口能力设计 §3）：图像/音频/视频域不再有
  // 「允许集」；域×供应商矩阵只服务创建墙（capabilityDomainAccountTypes）与
  // 域默认检查形态，声明面恒为空集。
  const mediaProfiles = [gptProfile, xaiProfile, glmProfile, geminiNativeProfile, minimaxProfile, volcengineProfile, volcengineOpenSpeechProfile, qwenProfile, deepseekProfile, anthropicProfile, geminiOpenAIChatProfile]

  it('媒体域（video/audio/image）任何供应商×账户类型组合都返回空集', () => {
    for (const profile of mediaProfiles) {
      for (const accountType of ['api_key', 'oauth', 'google_oauth'] as const) {
        expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile, accountType })).toEqual([])
        expect(allowedEndpointModesFor({ capabilityDomain: 'audio', profile, accountType })).toEqual([])
        expect(allowedEndpointModesFor({ capabilityDomain: 'image', profile, accountType })).toEqual([])
      }
    }
  })

  it('volcengine 档案×音频域创建墙：Ark 无可创建类型，OpenSpeech 可建 api_key（声明集仍为空）', () => {
    expect(capabilityDomainAccountTypes({
      capabilityDomain: 'audio',
      providerCode: 'volcengine',
      profile: volcengineProfile
    })).toEqual([])
    expect(allowedEndpointModesFor({
      capabilityDomain: 'audio',
      profile: volcengineProfile,
      accountType: 'api_key'
    })).toEqual([])
    expect(capabilityDomainAccountTypes({
      capabilityDomain: 'audio',
      providerCode: 'volcengine',
      profile: volcengineOpenSpeechProfile
    })).toEqual(['api_key'])
    expect(allowedEndpointModesFor({
      capabilityDomain: 'audio',
      profile: volcengineOpenSpeechProfile,
      accountType: 'api_key'
    })).toEqual([])
  })
})

describe('allowedEndpointModesFor：账户类型门与五域退役空集（清单表：媒体域 = api_key ∪ gemini native google_oauth）', () => {
  it('gpt/xai/anthropic 系 oauth 不可媒体：允许集为空（五域退役后全域空集）', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile: gptProfile, accountType: 'oauth' })).toEqual([])
    expect(allowedEndpointModesFor({ capabilityDomain: 'audio', profile: xaiProfile, accountType: 'oauth' })).toEqual([])
    expect(allowedEndpointModesFor({ capabilityDomain: 'image', profile: gptProfile, accountType: 'oauth' })).toEqual([])
  })

  it('gemini native google_oauth 媒体域允许集同为空集（声明面退役），类型判定仍放行', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile: geminiNativeProfile, accountType: 'google_oauth' })).toEqual([])
    expect(allowedEndpointModesFor({ capabilityDomain: 'audio', profile: geminiNativeProfile, accountType: 'google_oauth' })).toEqual([])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'video', providerCode: 'gemini', profile: geminiNativeProfile })).toEqual(['api_key', 'google_oauth'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'audio', providerCode: 'gemini', profile: geminiNativeProfile })).toEqual(['api_key', 'google_oauth'])
  })

  it('google_oauth 非 gemini native（无矩阵行）媒体域为空', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile: gptProfile, accountType: 'google_oauth' })).toEqual([])
  })

  it('capabilityDomainAccountTypes：媒体域 api_key 全量、google_oauth 仅 gemini native；gemini 图像域为空（另行立项）；chat 域返回空（不裁剪现行类型选择）', () => {
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'image', providerCode: 'gemini', profile: geminiNativeProfile })).toEqual([])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'video', providerCode: 'volcengine', profile: volcengineProfile })).toEqual(['api_key'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'audio', providerCode: 'volcengine', profile: volcengineProfile })).toEqual([])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'audio', providerCode: 'volcengine', profile: volcengineOpenSpeechProfile })).toEqual(['api_key'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'chat', profile: gptProfile })).toEqual([])
  })
})

describe('defaultEndpointModesForCapabilityDomain：默认集按域收窄', () => {
  it('chat 域默认集：现行默认本就域内的供应商与 defaultEndpointModesForAccount 逐项一致（BUG-0323 根治 I3 新口径）', () => {
    const cases: Array<{ profile: AccountProviderProfileLike; type: 'api_key' | 'oauth' | 'google_oauth' }> = [
      { profile: gptProfile, type: 'api_key' },
      { profile: gptProfile, type: 'oauth' },
      { profile: glmProfile, type: 'api_key' },
      { profile: anthropicProfile, type: 'api_key' },
      { profile: geminiNativeProfile, type: 'api_key' },
      { profile: geminiNativeProfile, type: 'google_oauth' },
      { profile: minimaxProfile, type: 'api_key' }
    ]
    for (const item of cases) {
      expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'chat', profile: item.profile, type: item.type }))
        .toEqual(defaultEndpointModesForAccount({ profile: item.profile, type: item.type }))
    }
  })

  it('chat 域默认集：现行默认不含对话值的供应商落词表内基础对话对', () => {
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'chat', profile: volcengineProfile, type: 'api_key' }))
      .toEqual(['chat_json', 'chat_sse'])
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'chat', profile: qwenProfile, type: 'api_key' }))
      .toEqual(['chat_json', 'chat_sse'])
  })

  it('五域退役：五域默认集恒为空集（创建不播种声明集）', () => {
    // 2026-11-21 五域退役（AI账户上游接口能力设计 §3）：任何供应商×类型组合
    // 都不播种默认集。
    for (const profile of [gptProfile, qwenProfile, glmProfile, volcengineProfile, volcengineOpenSpeechProfile, xaiProfile, geminiNativeProfile, minimaxProfile]) {
      for (const domain of ['video', 'audio', 'image', 'embedding', 'rerank'] as const) {
        expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: domain, profile, type: 'api_key' })).toEqual([])
      }
    }
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'video', profile: geminiNativeProfile, type: 'google_oauth' })).toEqual([])
  })
})

describe('媒体生命周期定案（v7）辅助语义', () => {
  it('health_check_endpoint_mode 默认落值为纯域常量：image→images_json、audio→audio_speech（含 qwen 长音频）、video→video_create、embedding→embeddings_json、rerank→rerank_json、chat→chat_sse（流式优先）', () => {
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'image' })).toBe('images_json')
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'audio' })).toBe('audio_speech')
    // 2026-11-21 白名单扩展后 audio_job_create 已是合法记录值，但域默认仍是
    // audio_speech；存量合法记录值（如旧 qwen 音频 audio_job_create）由编辑
    // 装载原样保留，不在前端强制归位（五域退役设计 §5）。
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'audio' })).not.toBe('audio_job_create')
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'video' })).toBe('video_create')
    // 向量/重排域默认检查形态 = 域 lane 单值（设计 §7）。
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'embedding' })).toBe('embeddings_json')
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'rerank' })).toBe('rerank_json')
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'chat' })).toBe('chat_sse')
  })

  it('五域退役：声明集配置面判定仅 chat 为真，undefined 视作 chat', () => {
    expect(capabilityDomainSupportsDeclaration('chat')).toBe(true)
    expect(capabilityDomainSupportsDeclaration(undefined)).toBe(true)
    for (const domain of ['video', 'audio', 'image', 'embedding', 'rerank'] as const) {
      expect(capabilityDomainSupportsDeclaration(domain)).toBe(false)
    }
  })

  it('新建表单默认 status：2026-11-21 起全部能力域统一 pending_test', () => {
    for (const domain of ACCOUNT_CAPABILITY_DOMAINS) {
      expect(defaultStatusForCapabilityDomain(domain)).toBe('pending_test')
    }
  })

  it('媒体域判定与目录协议 token 词表（向量/重排为文本域，非媒体）', () => {
    expect(capabilityDomainIsMedia('chat')).toBe(false)
    expect(capabilityDomainIsMedia('embedding')).toBe(false)
    expect(capabilityDomainIsMedia('rerank')).toBe(false)
    expect(capabilityDomainIsMedia(undefined)).toBe(false)
    for (const domain of ['video', 'audio', 'image'] as const) {
      expect(capabilityDomainIsMedia(domain)).toBe(true)
    }
    expect(mediaDomainModelProtocolTokens('video')).toEqual(['video'])
    expect(mediaDomainModelProtocolTokens('audio')).toEqual(['audio_speech', 'audio_transcription', 'realtime'])
    expect(mediaDomainModelProtocolTokens('image')).toEqual(['images'])
    expect(mediaDomainModelProtocolTokens('embedding')).toEqual(['embeddings'])
    expect(mediaDomainModelProtocolTokens('rerank')).toEqual(['rerank'])
    expect(mediaDomainModelProtocolTokens('chat')).toEqual([])
  })

  it('v8-C 域→目录 mode 白名单：六域取值与目录词表七值对齐，向量/重排独立成域', () => {
    expect(DOMAIN_MODEL_MODES.chat).toEqual(['chat', 'responses'])
    expect(DOMAIN_MODEL_MODES.embedding).toEqual(['embedding'])
    expect(DOMAIN_MODEL_MODES.rerank).toEqual(['rerank'])
    expect(DOMAIN_MODEL_MODES.video).toEqual(['video'])
    expect(DOMAIN_MODEL_MODES.audio).toEqual(['audio'])
    expect(DOMAIN_MODEL_MODES.image).toEqual(['image_generation'])
    // 六域 mode 值互不相交，且全部落在目录 mode 词表内（embedding/rerank 已随
    // 2026-11-21 拆分移出对话域，各自独立白名单）；词表与服务端
    // ProviderCatalogModeValues 同源。
    const domainModes = ACCOUNT_CAPABILITY_DOMAINS.flatMap((domain) => [...DOMAIN_MODEL_MODES[domain]])
    expect(new Set(domainModes).size).toBe(domainModes.length)
    const catalogModeValues = ['chat', 'responses', 'embedding', 'rerank', 'audio', 'video', 'image_generation']
    for (const mode of domainModes) {
      expect(catalogModeValues).toContain(mode)
    }
  })
})

describe('preferredMediaDomainProviderCode：媒体域新建默认供应商收敛（v8-D）', () => {
  const providerByCode = (code: string, overrides: Partial<ProviderDefinition> = {}): ProviderDefinition => {
    const provider = FALLBACK_PROVIDERS.find((item) => item.code === code)
    expect(provider, `FALLBACK_PROVIDERS 缺少供应商 ${code}`).toBeTruthy()
    return { ...provider!, ...overrides }
  }

  it('video 域：清单序里首个「启用 + 域矩阵放行 + 有在售目录」的供应商胜出', () => {
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'video',
      providers: [providerByCode('gpt'), providerByCode('glm')],
      providerCodesWithDomainModels: new Set(['gpt', 'glm'])
    })).toBe('gpt')
    // gpt 无货（sora-2/sora-2-pro 已下线场景）→ 收敛到 glm。
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'video',
      providers: [providerByCode('gpt'), providerByCode('glm')],
      providerCodesWithDomainModels: new Set(['glm'])
    })).toBe('glm')
  })

  it('禁用供应商跳过；全无货返回 undefined（调用方保持既有默认值，不做异步改选）', () => {
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'video',
      providers: [providerByCode('gpt', { enabled: false }), providerByCode('glm')],
      providerCodesWithDomainModels: new Set(['gpt', 'glm'])
    })).toBe('glm')
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'video',
      providers: [providerByCode('gpt')],
      providerCodesWithDomainModels: new Set(['glm', 'qwen'])
    })).toBeUndefined()
  })

  it('chat 与向量/重排文本域不适用（文本域供应商清单同 chat 路径，不做媒体收敛）；gemini 仅 native 档案放行', () => {
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'chat',
      providers: [providerByCode('gpt')],
      providerCodesWithDomainModels: new Set(['gpt'])
    })).toBeUndefined()
    // 向量/重排域供应商清单不过滤（§8.2 文本域同 chat 路径）。
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'embedding',
      providers: [providerByCode('gpt')],
      providerCodesWithDomainModels: new Set(['gpt'])
    })).toBeUndefined()
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'rerank',
      providers: [providerByCode('qwen')],
      providerCodesWithDomainModels: new Set(['qwen'])
    })).toBeUndefined()
    // gemini OpenAI Chat 档案无媒体域（矩阵无行）：不参与媒体域收敛。
    const geminiOpenAIChat = providerByCode('gemini', { protocolProfiles: [geminiOpenAIChatProfile as never] })
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'video',
      providers: [geminiOpenAIChat],
      providerCodesWithDomainModels: new Set(['gemini'])
    })).toBeUndefined()
    // native 档案放行（Veo）。
    const geminiNative = providerByCode('gemini', { protocolProfiles: [geminiNativeProfile as never] })
    expect(preferredMediaDomainProviderCode({
      capabilityDomain: 'video',
      providers: [geminiNative],
      providerCodesWithDomainModels: new Set(['gemini'])
    })).toBe('gemini')
  })
})

describe('媒体域三家供应商回退定义（T47）', () => {
  it.each([
    ['minimax', MINIMAX_OPENAI_V1_PROFILE_ID, ['video', 'audio']],
    // volcengine 的媒体域按档案拆分：Ark 承接 chat/video/image，OpenSpeech 承接 audio。
    ['volcengine', VOLCENGINE_OPENAI_V1_PROFILE_ID, ['video', 'image']],
    ['volcengine', VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID, ['audio']],
    ['qwen', QWEN_OPENAI_V1_PROFILE_ID, ['video', 'audio']]
  ] as const)('%s/%s 在 FALLBACK_PROVIDERS 且按档案×域可建户', (code, profileId, domains) => {
    const provider = FALLBACK_PROVIDERS.find((item) => item.code === code)
    expect(provider, `FALLBACK_PROVIDERS 缺少媒体供应商 ${code}`).toBeTruthy()
    const profile = provider!.protocolProfiles.find((item) => item.id === profileId)
    expect(profile, `FALLBACK_PROVIDERS 缺少供应商 ${code} 的档案 ${profileId}`).toBeTruthy()
    for (const domain of ['video', 'audio', 'image'] as const) {
      expect(capabilityDomainAccountTypes({
        capabilityDomain: domain,
        providerCode: code,
        profile: profile!
      }), `${code}/${profileId} 域 ${domain} 可建户判定不符`).toEqual((domains as readonly string[]).includes(domain) ? ['api_key'] : [])
    }
  })

  it('媒体表单合并逻辑保留三家供应商（服务端空档案不覆盖内置协议形状）', () => {
    const merged = mergeAccountProviderDefinitions(FALLBACK_PROVIDERS, [])
    for (const code of ['minimax', 'volcengine', 'qwen']) {
      const provider = merged.find((item) => item.code === code)
      expect(provider, `合并后缺少 ${code}`).toBeTruthy()
      expect(provider!.protocolProfiles.length).toBeGreaterThan(0)
    }
  })
})
