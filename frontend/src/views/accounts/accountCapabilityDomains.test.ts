import { describe, expect, it } from 'vitest'

import { allAccountEndpointModes } from './accountProviderCapabilities'
import {
  ACCOUNT_CAPABILITY_DOMAINS,
  ACCOUNT_CAPABILITY_DOMAIN_LABELS,
  ACCOUNT_CAPABILITY_DOMAIN_RULE_VERSION,
  CHAT_DOMAIN_PROTOCOL_FACE_OPTIONAL_MODES,
  DOMAIN_ENDPOINT_MODES_AUDIO,
  DOMAIN_ENDPOINT_MODES_CHAT,
  DOMAIN_ENDPOINT_MODES_IMAGE,
  DOMAIN_ENDPOINT_MODES_VIDEO,
  accountCapabilityDomainLabel,
  deriveCapabilityDomain
} from './accountCapabilityDomains'

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
  it('锚集合恰为 15 值且顺序与计划锚定义一致', () => {
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
      'message_token_counting',
      'realtime_session',
      'audio_transcription_json'
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

  it('媒体触发集合取值与顺序固定', () => {
    expect([...DOMAIN_ENDPOINT_MODES_VIDEO]).toEqual(['video_create', 'video_get', 'video_content', 'video_cancel'])
    expect([...DOMAIN_ENDPOINT_MODES_AUDIO]).toEqual(['audio_speech', 'audio_job_create', 'audio_job_get', 'audio_job_content', 'audio_job_cancel'])
    expect([...DOMAIN_ENDPOINT_MODES_IMAGE]).toEqual(['images_json'])
  })

  it('对话锚集合与媒体触发集合两两不相交', () => {
    const chatModes = new Set<string>(DOMAIN_ENDPOINT_MODES_CHAT)
    for (const mode of [...DOMAIN_ENDPOINT_MODES_VIDEO, ...DOMAIN_ENDPOINT_MODES_AUDIO, ...DOMAIN_ENDPOINT_MODES_IMAGE]) {
      expect(chatModes.has(mode), `媒体触发值 ${mode} 不得出现在对话 modes 锚集合`).toBe(false)
    }
  })

  it('对话域协议面可选项恰为 images_json、audio_transcription_json、realtime_session', () => {
    expect([...CHAT_DOMAIN_PROTOCOL_FACE_OPTIONAL_MODES]).toEqual(['images_json', 'audio_transcription_json', 'realtime_session'])
    const chatModes = new Set<string>(DOMAIN_ENDPOINT_MODES_CHAT)
    expect(chatModes.has('images_json')).toBe(false)
    expect(chatModes.has('audio_transcription_json')).toBe(true)
    expect(chatModes.has('realtime_session')).toBe(true)
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
      name: '对话协议面：realtime_session 与 audio_transcription_json 均在对话锚集合内',
      modes: ['realtime_session', 'audio_transcription_json'],
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
