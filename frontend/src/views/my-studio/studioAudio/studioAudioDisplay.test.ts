import { describe, expect, it } from 'vitest'

import type { StudioModelOption } from '@/types/domain'
import {
  filterStudioModelsByEndpointMode,
  formatAudioDuration,
  splitStudioSpeechSegments,
  studioTtsCostEstimate,
  studioTtsParamsSummary,
  transcriptionHasSrt
} from './studioAudioDisplay'

function audioModel(overrides: Partial<StudioModelOption> = {}): StudioModelOption {
  return {
    model: 'tts-1',
    providerCode: 'openai',
    domain: 'audio',
    endpointModes: ['audio_speech'],
    capabilities: {},
    ...overrides
  }
}

describe('studioAudioDisplay 端点模式过滤', () => {
  it('按 endpointModes 过滤页签候选，未声明该模式的模型不进入', () => {
    const models = [
      audioModel({ model: 'tts-1', endpointModes: ['audio_speech'] }),
      audioModel({ model: 'whisper-1', endpointModes: ['audio_transcription'] }),
      audioModel({ model: 'glm-voice', endpointModes: [] })
    ]
    expect(filterStudioModelsByEndpointMode(models, 'audio_speech').map((model) => model.model)).toEqual(['tts-1'])
    expect(filterStudioModelsByEndpointMode(models, 'audio_transcription').map((model) => model.model)).toEqual(['whisper-1'])
  })
})

describe('studioAudioDisplay 长文本分段', () => {
  it('按空行拆段并剔除空白段', () => {
    expect(splitStudioSpeechSegments('第一段。\n\n第二段。\n\n\n   \n第三段。')).toEqual(['第一段。', '第二段。', '第三段。'])
  })

  it('无空行文本为单段；空文本为空', () => {
    expect(splitStudioSpeechSegments('单段文本')).toEqual(['单段文本'])
    expect(splitStudioSpeechSegments('   \n\n  ')).toEqual([])
  })
})

describe('studioAudioDisplay TTS 参数摘要', () => {
  it('拼接 voice/speed/emotion/responseFormat，缺项回落 -', () => {
    expect(studioTtsParamsSummary({ voice: 'alloy', speed: 1.2, emotion: ' cheerful ', responseFormat: 'mp3' }))
      .toBe('音色 alloy · 语速 1.2 · 情感  cheerful  · mp3')
    expect(studioTtsParamsSummary({ voice: 'alloy' })).toBe('音色 alloy')
    expect(studioTtsParamsSummary({})).toBe('-')
    expect(studioTtsParamsSummary(undefined)).toBe('-')
  })
})

describe('studioAudioDisplay SRT 可用性与时长', () => {
  it('srt 缺失或空白不可用，不从文本虚构时间轴', () => {
    expect(transcriptionHasSrt({ text: '内容' })).toBe(false)
    expect(transcriptionHasSrt({ text: '内容', srt: '   ' })).toBe(false)
    expect(transcriptionHasSrt({ text: '内容', srt: '1\n00:00:01,000 --> 00:00:02,000\n内容' })).toBe(true)
    expect(transcriptionHasSrt(undefined)).toBe(false)
  })

  it('时长毫秒格式化，来源缺失回落 -', () => {
    expect(formatAudioDuration(65_000)).toBe('1:05')
    expect(formatAudioDuration(null)).toBe('-')
  })
})

describe('studioAudioDisplay 成本预估', () => {
  it('目录未声明单价显式"无法估算"，不编造数字', () => {
    expect(studioTtsCostEstimate(undefined)).toBe('无法估算')
    expect(studioTtsCostEstimate(audioModel({ capabilities: {} }))).toBe('无法估算')
    expect(studioTtsCostEstimate(audioModel({ capabilities: { unitPrice: 0.015 } }))).toBe('$0.015000')
  })
})
