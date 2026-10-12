import { describe, expect, it } from 'vitest'

import {
  OPENAI_AUDIO_TRANSCRIPTION_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  MINIMAX_OPENAI_V1_PROFILE_ID,
  QWEN_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_OPENAI_V1_PROFILE_ID
} from '@/shared/providerProtocol'
import type { ProviderProtocolProfileDefinition } from '@/types/domain'
import { applyMediaCreateDefaults, type MediaCreateDefaultsForm } from './accountMediaCreateDefaults'

function mediaProfile(input: {
  id: string
  providerCode: string
  baseUrl: string
  families: Array<{ code: string; name: string }>
}): ProviderProtocolProfileDefinition {
  return {
    id: input.id,
    providerCode: input.providerCode,
    name: `${input.providerCode} OpenAI v1`,
    enabled: true,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: input.baseUrl,
    defaultHealthCheckModel: '',
    accountTypes: ['api_key'],
    capabilities: [],
    endpointFamilies: input.families
  }
}

const volcengineProfile = mediaProfile({
  id: VOLCENGINE_OPENAI_V1_PROFILE_ID,
  providerCode: 'volcengine',
  baseUrl: 'https://ark.cn-beijing.volces.com/api/v3',
  families: [
    { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
    { code: OPENAI_VIDEO_GENERATION_FAMILY, name: 'Video Generation' },
    { code: OPENAI_TTS_FAMILY, name: 'TTS' }
  ]
})

const minimaxProfile = mediaProfile({
  id: MINIMAX_OPENAI_V1_PROFILE_ID,
  providerCode: 'minimax',
  baseUrl: 'https://api.minimaxi.chat/v1',
  families: [
    { code: OPENAI_VIDEO_GENERATION_FAMILY, name: 'Video Generation' },
    { code: OPENAI_TTS_FAMILY, name: 'TTS' }
  ]
})

const qwenProfile = mediaProfile({
  id: QWEN_OPENAI_V1_PROFILE_ID,
  providerCode: 'qwen',
  baseUrl: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
  families: [
    { code: OPENAI_CHAT_COMPLETIONS_FAMILY, name: 'Chat Completions' },
    { code: OPENAI_VIDEO_GENERATION_FAMILY, name: 'Video Generation' },
    { code: OPENAI_AUDIO_TRANSCRIPTION_FAMILY, name: 'Audio Transcription' }
  ]
})

// 第二次选择同供应商时定义已入缓存：resetForm 直接解析出完整默认
// （accountFormDefaults 硬编码 status:'pending_test'），声明集非空且全部落在
// 域允许集内——B1 修复前此处提前 return，跳过 status=active 预选。
function cachedResetFormState(overrides: Partial<MediaCreateDefaultsForm> = {}): MediaCreateDefaultsForm {
  return {
    supportedEndpointModes: [],
    healthCheckEndpointMode: 'chat_json',
    status: 'pending_test',
    statusSelectionExplicit: false,
    ...overrides
  }
}

describe('applyMediaCreateDefaults（B1：status 预选不受声明集早退分支影响）', () => {
  it('volcengine×video：声明集已落域内（legacy video_get）时仍预选 active，检查形态收敛为域默认 video_create', () => {
    const form = cachedResetFormState({
      supportedEndpointModes: ['video_get'],
      healthCheckEndpointMode: 'chat_json'
    })
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'video',
      providerCode: 'volcengine',
      profile: volcengineProfile,
      type: 'api_key'
    })
    expect(form.status).toBe('active')
    expect(form.statusSelectionExplicit).toBe(false)
    expect(form.healthCheckEndpointMode).toBe('video_create')
    expect(form.supportedEndpointModes).toEqual(['video_get'])
  })

  it('minimax×audio：声明集 audio_speech 已落域内时仍预选 active，检查形态保持域默认 audio_speech', () => {
    const form = cachedResetFormState({
      supportedEndpointModes: ['audio_speech'],
      healthCheckEndpointMode: 'chat_json'
    })
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'audio',
      providerCode: 'minimax',
      profile: minimaxProfile,
      type: 'api_key'
    })
    expect(form.status).toBe('active')
    expect(form.statusSelectionExplicit).toBe(false)
    expect(form.healthCheckEndpointMode).toBe('audio_speech')
    expect(form.supportedEndpointModes).toEqual(['audio_speech'])
  })

  it('qwen×video：声明集已落域内时仍预选 active，检查形态收敛为域默认 video_create', () => {
    const form = cachedResetFormState({
      supportedEndpointModes: ['video_get', 'video_content'],
      healthCheckEndpointMode: 'chat_json'
    })
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'video',
      providerCode: 'qwen',
      profile: qwenProfile,
      type: 'api_key'
    })
    expect(form.status).toBe('active')
    expect(form.statusSelectionExplicit).toBe(false)
    expect(form.healthCheckEndpointMode).toBe('video_create')
    expect(form.supportedEndpointModes).toEqual(['video_get', 'video_content'])
  })

  it('首次选择（声明集为空）：落域默认集 + active 预选（既有 else 分支行为不变）', () => {
    const form = cachedResetFormState()
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'video',
      providerCode: 'volcengine',
      profile: volcengineProfile,
      type: 'api_key'
    })
    expect(form.supportedEndpointModes).toEqual(['video_create', 'video_get', 'video_content', 'video_cancel'])
    expect(form.healthCheckEndpointMode).toBe('video_create')
    expect(form.status).toBe('active')
    expect(form.statusSelectionExplicit).toBe(false)
  })

  it('声明集含域外值：整体重置为域默认集 + active 预选（既有 else 分支行为不变）', () => {
    const form = cachedResetFormState({
      supportedEndpointModes: ['chat_json'],
      healthCheckEndpointMode: 'chat_json'
    })
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'video',
      providerCode: 'volcengine',
      profile: volcengineProfile,
      type: 'api_key'
    })
    expect(form.supportedEndpointModes).toEqual(['video_create', 'video_get', 'video_content', 'video_cancel'])
    expect(form.healthCheckEndpointMode).toBe('video_create')
    expect(form.status).toBe('active')
  })

  it('对话域（chat）status 预选保持 pending_test（对话域行为零变化）', () => {
    const form = cachedResetFormState()
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'chat',
      providerCode: 'gpt',
      profile: undefined,
      type: 'api_key'
    })
    expect(form.status).toBe('pending_test')
    expect(form.statusSelectionExplicit).toBe(false)
  })
})
