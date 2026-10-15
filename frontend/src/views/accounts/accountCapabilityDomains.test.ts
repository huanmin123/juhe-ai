import { describe, expect, it } from 'vitest'

import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID,
  HYBRID_OPENAI_CHAT_V1_PROFILE_ID,
  MINIMAX_OPENAI_V1_PROFILE_ID,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_RESPONSES_FAMILY,
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  QWEN_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_OPENAI_V1_PROFILE_ID,
  XAI_OPENAI_V1_PROFILE_ID
} from '@/shared/providerProtocol'
import type { AccountSupportedEndpointMode } from '@/types/domain'
import {
  allAccountEndpointModes,
  defaultEndpointModesForAccount,
  type AccountProviderProfileLike
} from './accountProviderCapabilities'
import {
  ACCOUNT_CAPABILITY_DOMAINS,
  ACCOUNT_CAPABILITY_DOMAIN_LABELS,
  ACCOUNT_CAPABILITY_DOMAIN_RULE_VERSION,
  DOMAIN_ENDPOINT_MODES_AUDIO,
  DOMAIN_ENDPOINT_MODES_CHAT,
  DOMAIN_ENDPOINT_MODES_IMAGE,
  DOMAIN_ENDPOINT_MODES_VIDEO,
  DOMAIN_MODEL_MODES,
  accountCapabilityDomainLabel,
  allowedEndpointModesFor,
  capabilityDomainAccountTypes,
  capabilityDomainIsMedia,
  defaultEndpointModesForCapabilityDomain,
  defaultHealthCheckEndpointModeForCapabilityDomain,
  defaultStatusForCapabilityDomain,
  deriveCapabilityDomain,
  mediaDomainModelProtocolTokens
} from './accountCapabilityDomains'
import { FALLBACK_PROVIDERS } from './accountOptions'

describe('能力域枚举与标签', () => {
  it('域枚举固定为四值，顺序与 CHECK 约束一致', () => {
    expect([...ACCOUNT_CAPABILITY_DOMAINS]).toEqual(['chat', 'video', 'audio', 'image'])
  })

  it('中文标签覆盖全部域', () => {
    expect(ACCOUNT_CAPABILITY_DOMAIN_LABELS).toEqual({
      chat: '对话',
      video: '视频',
      audio: '音频',
      image: '图像'
    })
    for (const domain of ACCOUNT_CAPABILITY_DOMAINS) {
      expect(accountCapabilityDomainLabel(domain)).toBe(ACCOUNT_CAPABILITY_DOMAIN_LABELS[domain])
    }
  })

  it('推导规则版本固定为 ruleVersion=1', () => {
    expect(ACCOUNT_CAPABILITY_DOMAIN_RULE_VERSION).toBe(1)
  })
})

describe('对话 modes 集合完整性', () => {
  it('锚集合恰为 13 值（v8-A 移出三项）且顺序与计划锚定义一致', () => {
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
})

describe('deriveCapabilityDomain 推导规则（ruleVersion=1）', () => {
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
      name: 'v8-A：对话 + 短转写混合仍归 chat（含对话 modes 即停，遗留机制兜底）',
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

// ---- M2 允许集/默认集镜像（§D6 三集合 + §档案×域清单表定案 v7） ----

const VIDEO_GROUP: AccountSupportedEndpointMode[] = ['video_create', 'video_get', 'video_content', 'video_cancel']
const AUDIO_JOB_GROUP: AccountSupportedEndpointMode[] = ['audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel']

function fallbackProfileById(id: string): AccountProviderProfileLike {
  const provider = FALLBACK_PROVIDERS.find((item) => item.protocolProfiles.some((profile) => profile.id === id))
  const profile = provider?.protocolProfiles.find((item) => item.id === id)
  expect(profile, `FALLBACK_PROVIDERS 缺少档案 ${id}`).toBeTruthy()
  return profile as AccountProviderProfileLike
}

// minimax/volcengine/qwen 不在 FALLBACK_PROVIDERS，用与加载定义同形的最小档案
//（openai 协议 + 媒体 families）代表其写侧词表。
function mediaVendorProfile(input: {
  id: string
  providerCode: string
  families: string[]
}): AccountProviderProfileLike {
  return {
    id: input.id,
    providerCode: input.providerCode,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    endpointFamilies: input.families.map((code) => ({ code }))
  }
}

const gptProfile = fallbackProfileById(GPT_OPENAI_V1_PROFILE_ID)
const openaiCompatibleProfile = fallbackProfileById(OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID)
const xaiProfile = fallbackProfileById(XAI_OPENAI_V1_PROFILE_ID)
const glmProfile = fallbackProfileById(GLM_GENERAL_OPENAI_V1_PROFILE_ID)
const deepseekProfile = fallbackProfileById(DEEPSEEK_OPENAI_V1_PROFILE_ID)
const anthropicProfile = fallbackProfileById(ANTHROPIC_ANTHROPIC_V1_PROFILE_ID)
const geminiNativeProfile = fallbackProfileById(GEMINI_NATIVE_V1BETA_PROFILE_ID)
const geminiOpenAIChatProfile = fallbackProfileById(GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID)
const hybridProfile = fallbackProfileById(HYBRID_OPENAI_CHAT_V1_PROFILE_ID)
const minimaxProfile = mediaVendorProfile({
  id: MINIMAX_OPENAI_V1_PROFILE_ID,
  providerCode: 'minimax',
  families: [OPENAI_VIDEO_GENERATION_FAMILY, OPENAI_TTS_FAMILY]
})
const volcengineProfile = mediaVendorProfile({
  id: VOLCENGINE_OPENAI_V1_PROFILE_ID,
  providerCode: 'volcengine',
  families: [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_VIDEO_GENERATION_FAMILY, OPENAI_TTS_FAMILY]
})
const qwenProfile = mediaVendorProfile({
  id: QWEN_OPENAI_V1_PROFILE_ID,
  providerCode: 'qwen',
  families: [OPENAI_CHAT_COMPLETIONS_FAMILY, OPENAI_VIDEO_GENERATION_FAMILY, 'audio_transcription']
})

describe('allowedEndpointModesFor：chat 域允许集 = 13 值对话锚集合 ∩ 供应商词表（v8-A）', () => {
  it('gpt：恰为 chat×2+responses×2（v8-A 移出 realtime/短转写/images_json）', () => {
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

  it('deepseek / glm / xai / gemini OpenAI Chat：v8-A 后收敛对话族', () => {
    const deepseekAllowed = allowedEndpointModesFor({ capabilityDomain: 'chat', profile: deepseekProfile, accountType: 'api_key' })
    expect(deepseekAllowed).toContain('responses_json')
    expect(deepseekAllowed).not.toContain('images_json')
    expect(deepseekAllowed).not.toContain('audio_transcription_json')
    expect(deepseekAllowed).not.toContain('realtime_session')
    expect(deepseekAllowed).not.toContain('video_create')
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: glmProfile, accountType: 'api_key' })).toEqual([
      'chat_json',
      'chat_sse'
    ])
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: xaiProfile, accountType: 'api_key' })).toContain('responses_sse')
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: geminiOpenAIChatProfile, accountType: 'api_key' })).toEqual([
      'chat_json',
      'chat_sse'
    ])
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

  it('hybrid：13 值对话锚集合全量可用（v8-A 后不再追加 images_json）', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: hybridProfile, accountType: 'api_key' })).toEqual([
      ...DOMAIN_ENDPOINT_MODES_CHAT
    ])
  })

  it('minimax 纯媒体词表与 chat 域求交为空集（无 chat 域）', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'chat', profile: minimaxProfile, accountType: 'api_key' })).toEqual([])
  })

  it('允许集是词表与域集合的子集：任何域结果都不含词表外值', () => {
    const profiles = [gptProfile, xaiProfile, glmProfile, deepseekProfile, anthropicProfile, geminiNativeProfile, geminiOpenAIChatProfile, hybridProfile, minimaxProfile, volcengineProfile, qwenProfile]
    const vocabulary = new Set<string>(allAccountEndpointModes)
    for (const profile of profiles) {
      for (const domain of ACCOUNT_CAPABILITY_DOMAINS) {
        for (const mode of allowedEndpointModesFor({ capabilityDomain: domain, profile, accountType: 'api_key' })) {
          expect(vocabulary.has(mode), `${profile.providerCode}/${profile.id} 域 ${domain} 出现词表外值 ${mode}`).toBe(true)
        }
      }
    }
  })
})

describe('allowedEndpointModesFor：媒体域矩阵逐格（域 modes 集 × 供应商写侧词表）', () => {
  it.each([
    // v8-A：音频域允许集补 audio_transcription_json / realtime_session（按
    // 各供应商词表实际支持取舍；qwen/gemini/minimax/volcengine 词表无则不行值）。
    { name: 'gpt', profile: gptProfile, video: VIDEO_GROUP, audio: ['audio_speech', 'audio_transcription_json', 'realtime_session'], image: ['images_json'] },
    { name: 'openai 兼容', profile: openaiCompatibleProfile, video: VIDEO_GROUP, audio: ['audio_speech', 'audio_transcription_json', 'realtime_session'], image: ['images_json'] },
    { name: 'xai', profile: xaiProfile, video: VIDEO_GROUP, audio: [], image: [] },
    { name: 'glm', profile: glmProfile, video: VIDEO_GROUP, audio: ['audio_speech', 'audio_transcription_json'], image: [] },
    { name: 'gemini native', profile: geminiNativeProfile, video: VIDEO_GROUP, audio: ['audio_speech'], image: [] },
    { name: 'hybrid', profile: hybridProfile, video: VIDEO_GROUP, audio: ['audio_speech', 'audio_transcription_json', 'realtime_session'], image: ['images_json'] },
    { name: 'minimax', profile: minimaxProfile, video: VIDEO_GROUP, audio: ['audio_speech'], image: [] },
    { name: 'volcengine', profile: volcengineProfile, video: VIDEO_GROUP, audio: ['audio_speech'], image: [] },
    { name: 'qwen（仅长音频任务四值）', profile: qwenProfile, video: VIDEO_GROUP, audio: AUDIO_JOB_GROUP, image: [] },
    { name: 'deepseek', profile: deepseekProfile, video: [], audio: [], image: [] },
    { name: 'anthropic', profile: anthropicProfile, video: [], audio: [], image: [] },
    { name: 'gemini OpenAI Chat（无媒体行）', profile: geminiOpenAIChatProfile, video: [], audio: [], image: [] }
  ])('$name：域内允许集逐格相等', ({ profile, video, audio, image }) => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile, accountType: 'api_key' })).toEqual(video)
    expect(allowedEndpointModesFor({ capabilityDomain: 'audio', profile, accountType: 'api_key' })).toEqual(audio)
    expect(allowedEndpointModesFor({ capabilityDomain: 'image', profile, accountType: 'api_key' })).toEqual(image)
  })

  it('qwen 音频域 audio_speech 不进允许集（CosyVoice TTS 契约未回填）', () => {
    const qwenAudio = allowedEndpointModesFor({ capabilityDomain: 'audio', profile: qwenProfile, accountType: 'api_key' })
    expect(qwenAudio).not.toContain('audio_speech')
    expect(qwenAudio).not.toContain('audio_transcription_json')
    expect(qwenAudio).not.toContain('realtime_session')
  })

  it('矩阵结果与媒体触发集合一致：视频域允许集恒为视频四值组', () => {
    for (const profile of [gptProfile, glmProfile, geminiNativeProfile, hybridProfile, minimaxProfile, volcengineProfile, qwenProfile]) {
      expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile, accountType: 'api_key' })).toEqual([...DOMAIN_ENDPOINT_MODES_VIDEO])
    }
  })
})

describe('allowedEndpointModesFor：账户类型门（清单表：媒体域 = api_key ∪ gemini native google_oauth）', () => {
  it('gpt/xai/anthropic 系 oauth 不可媒体：允许集为空', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile: gptProfile, accountType: 'oauth' })).toEqual([])
    expect(allowedEndpointModesFor({ capabilityDomain: 'audio', profile: xaiProfile, accountType: 'oauth' })).toEqual([])
    expect(allowedEndpointModesFor({ capabilityDomain: 'image', profile: gptProfile, accountType: 'oauth' })).toEqual([])
  })

  it('gemini native google_oauth 在 video/audio 域可用且允许集同 api_key', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile: geminiNativeProfile, accountType: 'google_oauth' })).toEqual([...DOMAIN_ENDPOINT_MODES_VIDEO])
    expect(allowedEndpointModesFor({ capabilityDomain: 'audio', profile: geminiNativeProfile, accountType: 'google_oauth' })).toEqual(['audio_speech'])
  })

  it('google_oauth 非 gemini native（无矩阵行）媒体域为空', () => {
    expect(allowedEndpointModesFor({ capabilityDomain: 'video', profile: gptProfile, accountType: 'google_oauth' })).toEqual([])
  })

  it('capabilityDomainAccountTypes：媒体域 api_key 全量、google_oauth 仅 gemini native；gemini 图像域为空（另行立项）；chat 域返回空（不裁剪现行类型选择）', () => {
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'video', providerCode: 'gemini', profile: geminiNativeProfile })).toEqual(['api_key', 'google_oauth'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'audio', providerCode: 'gemini', profile: geminiNativeProfile })).toEqual(['api_key', 'google_oauth'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'image', providerCode: 'gemini', profile: geminiNativeProfile })).toEqual([])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'video', providerCode: 'volcengine', profile: volcengineProfile })).toEqual(['api_key'])
    expect(capabilityDomainAccountTypes({ capabilityDomain: 'chat', profile: gptProfile })).toEqual([])
  })
})

describe('defaultEndpointModesForCapabilityDomain：默认集按域收窄', () => {
  it('chat 域默认集与现行 defaultEndpointModesForAccount 逐项一致', () => {
    const cases: Array<{ profile: AccountProviderProfileLike; type: 'api_key' | 'oauth' | 'google_oauth' }> = [
      { profile: gptProfile, type: 'api_key' },
      { profile: gptProfile, type: 'oauth' },
      { profile: glmProfile, type: 'api_key' },
      { profile: anthropicProfile, type: 'api_key' },
      { profile: geminiNativeProfile, type: 'api_key' },
      { profile: geminiNativeProfile, type: 'google_oauth' },
      { profile: hybridProfile, type: 'api_key' },
      { profile: volcengineProfile, type: 'api_key' },
      { profile: qwenProfile, type: 'api_key' },
      { profile: minimaxProfile, type: 'api_key' }
    ]
    for (const item of cases) {
      expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'chat', profile: item.profile, type: item.type }))
        .toEqual(defaultEndpointModesForAccount({ profile: item.profile, type: item.type }))
    }
  })

  it.each([
    { name: 'gpt 视频', profile: gptProfile, type: 'api_key' as const, expected: VIDEO_GROUP },
    { name: 'qwen 视频', profile: qwenProfile, type: 'api_key' as const, expected: VIDEO_GROUP },
    { name: 'gemini native google_oauth 视频', profile: geminiNativeProfile, type: 'google_oauth' as const, expected: VIDEO_GROUP }
  ])('$name：video 域默认 = 视频四值组', ({ profile, type, expected }) => {
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'video', profile, type })).toEqual(expected)
  })

  it('audio 域默认按供应商：audio_speech（qwen 除外）与 qwen 长音频四值；v8-A 默认集不含新移入两项', () => {
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'audio', profile: volcengineProfile, type: 'api_key' })).toEqual(['audio_speech'])
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'audio', profile: qwenProfile, type: 'api_key' })).toEqual([...AUDIO_JOB_GROUP])
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'audio', profile: xaiProfile, type: 'api_key' })).toEqual([])
    // v8-A：gpt 词表含 audio_transcription_json / realtime_session，但音频域
    // 默认集维持现状语义只落 audio_speech（默认集不收，显式勾选承担）。
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'audio', profile: gptProfile, type: 'api_key' })).toEqual(['audio_speech'])
  })

  it('image 域默认 = images_json（glm 无 image 域为空）', () => {
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'image', profile: gptProfile, type: 'api_key' })).toEqual(['images_json'])
    expect(defaultEndpointModesForCapabilityDomain({ capabilityDomain: 'image', profile: glmProfile, type: 'api_key' })).toEqual([])
  })
})

describe('媒体生命周期定案（v7）辅助语义', () => {
  it('health_check_endpoint_mode 默认落值为纯域常量：image→images_json、audio→audio_speech（含 qwen 长音频）、video→video_create、chat→chat_json', () => {
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'image' })).toBe('images_json')
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'audio' })).toBe('audio_speech')
    // qwen 长音频默认集首值是 audio_job_create（非检查形态词表值），不得作为记录值兜底。
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'audio' })).not.toBe('audio_job_create')
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'video' })).toBe('video_create')
    expect(defaultHealthCheckEndpointModeForCapabilityDomain({ capabilityDomain: 'chat' })).toBe('chat_json')
  })

  it('新建表单默认 status：chat=pending_test（现行），媒体域=active', () => {
    expect(defaultStatusForCapabilityDomain('chat')).toBe('pending_test')
    expect(defaultStatusForCapabilityDomain('video')).toBe('active')
    expect(defaultStatusForCapabilityDomain('audio')).toBe('active')
    expect(defaultStatusForCapabilityDomain('image')).toBe('active')
  })

  it('媒体域判定与目录协议 token 词表', () => {
    expect(capabilityDomainIsMedia('chat')).toBe(false)
    expect(capabilityDomainIsMedia(undefined)).toBe(false)
    for (const domain of ['video', 'audio', 'image'] as const) {
      expect(capabilityDomainIsMedia(domain)).toBe(true)
    }
    expect(mediaDomainModelProtocolTokens('video')).toEqual(['video'])
    expect(mediaDomainModelProtocolTokens('audio')).toEqual(['audio_speech', 'audio_transcription', 'realtime'])
    expect(mediaDomainModelProtocolTokens('image')).toEqual(['images'])
    expect(mediaDomainModelProtocolTokens('chat')).toEqual([])
  })

  it('v8-C 域→目录 mode 白名单：四域取值与目录词表六值对齐，embedding 不属于任何域', () => {
    expect(DOMAIN_MODEL_MODES.chat).toEqual(['chat', 'responses'])
    expect(DOMAIN_MODEL_MODES.video).toEqual(['video'])
    expect(DOMAIN_MODEL_MODES.audio).toEqual(['audio'])
    expect(DOMAIN_MODEL_MODES.image).toEqual(['image_generation'])
    // 四域 mode 值互不相交，且全部落在目录 mode 词表内（embedding 仅目录存在，
    // 不放行给任何账户域）；词表与服务端 ProviderCatalogModeValues 同源。
    const domainModes = ACCOUNT_CAPABILITY_DOMAINS.flatMap((domain) => [...DOMAIN_MODEL_MODES[domain]])
    expect(new Set(domainModes).size).toBe(domainModes.length)
    const catalogModeValues = ['chat', 'responses', 'embedding', 'audio', 'video', 'image_generation']
    for (const mode of domainModes) {
      expect(catalogModeValues).toContain(mode)
    }
  })
})
