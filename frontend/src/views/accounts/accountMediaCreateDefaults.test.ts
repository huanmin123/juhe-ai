import { describe, expect, it } from 'vitest'

import { applyMediaCreateDefaults, type MediaCreateDefaultsForm } from './accountMediaCreateDefaults'

// 2026-11-21 五域退役（AI账户上游接口能力设计 §3/§5）修订后的新建态域默认值契约：
// 五域不播种「上游接口能力」声明集，检查请求形态按域默认常量落值，
// status 预选统一 pending_test。创建墙类型判定（capabilityDomainAccountTypes）
// 与本函数无关，不再在此钉媒体供应商矩阵。

function cachedResetFormState(overrides: Partial<MediaCreateDefaultsForm> = {}): MediaCreateDefaultsForm {
  return {
    supportedEndpointModes: [],
    healthCheckEndpointMode: 'chat_json',
    status: 'pending_test',
    statusSelectionExplicit: false,
    ...overrides
  }
}

describe('applyMediaCreateDefaults（五域退役：声明集清空 + 域默认检查形态 + pending_test 预选）', () => {
  it('video 域：清空装载残留的声明集，检查形态落域默认 video_create', () => {
    const form = cachedResetFormState({
      supportedEndpointModes: ['video_create', 'video_get'],
      healthCheckEndpointMode: 'chat_json'
    })
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'video',
      providerCode: 'volcengine',
      type: 'api_key'
    })
    expect(form.supportedEndpointModes).toEqual([])
    expect(form.healthCheckEndpointMode).toBe('video_create')
    expect(form.status).toBe('pending_test')
    expect(form.statusSelectionExplicit).toBe(false)
  })

  it('audio 域：检查形态落域默认 audio_speech（域常量，不再按声明/preset 收敛）', () => {
    const form = cachedResetFormState({
      supportedEndpointModes: ['audio_job_create', 'audio_job_get'],
      healthCheckEndpointMode: 'audio_job_create'
    })
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'audio',
      providerCode: 'qwen',
      type: 'api_key'
    })
    expect(form.supportedEndpointModes).toEqual([])
    expect(form.healthCheckEndpointMode).toBe('audio_speech')
    expect(form.status).toBe('pending_test')
    expect(form.statusSelectionExplicit).toBe(false)
  })

  it('image/embedding/rerank 域：声明集清空，检查形态落各自域 lane 单值', () => {
    for (const [domain, expectedMode] of [
      ['image', 'images_json'],
      ['embedding', 'embeddings_json'],
      ['rerank', 'rerank_json']
    ] as const) {
      const form = cachedResetFormState({ healthCheckEndpointMode: 'chat_json' })
      applyMediaCreateDefaults(form, {
        capabilityDomain: domain,
        providerCode: 'gpt',
        type: 'api_key'
      })
      expect(form.supportedEndpointModes).toEqual([])
      expect(form.healthCheckEndpointMode).toBe(expectedMode)
      expect(form.status).toBe('pending_test')
    }
  })

  it('chat 域不适用本函数（统一向导只对五域触发）：任何字段都不改写', () => {
    const form = cachedResetFormState({
      supportedEndpointModes: ['chat_json', 'chat_sse'],
      healthCheckEndpointMode: 'responses_sse'
    })
    applyMediaCreateDefaults(form, {
      capabilityDomain: 'chat',
      providerCode: 'gpt',
      type: 'api_key'
    })
    expect(form.supportedEndpointModes).toEqual(['chat_json', 'chat_sse'])
    expect(form.healthCheckEndpointMode).toBe('responses_sse')
    expect(form.status).toBe('pending_test')
    expect(form.statusSelectionExplicit).toBe(false)
  })
})
