import { describe, expect, it } from 'vitest'

import type { AccountListItem } from '@/types/domain'
import { buildAccountTestPayload, connectivityEndpointModeForDomain } from './accountTestFlow'

function account(overrides: Partial<AccountListItem> = {}): AccountListItem {
  return {
    id: 'acc-1',
    providerCode: 'gpt',
    name: 'test',
    type: 'api_key',
    capabilityDomain: 'chat',
    status: 'active',
    concurrencyLimit: 1,
    rpmLimit: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    healthCheckModel: 'gpt-test',
    healthCheckEndpointMode: 'chat_sse',
    ...overrides
  } as AccountListItem
}

describe('buildAccountTestPayload 场景分流', () => {
  it('对话账户保留模型/协议选择', () => {
    const payload = buildAccountTestPayload({ model: 'gpt-test', testEndpointMode: 'chat_sse' }, account())
    expect(payload).toEqual({ model: 'gpt-test', testEndpointMode: 'chat_sse' })
  })

  it('非对话账户只发 connectivity，不带模型/协议', () => {
    const payload = buildAccountTestPayload(
      { model: 'should-not-send', testEndpointMode: 'chat_sse' },
      account({ capabilityDomain: 'video' })
    )
    expect(payload).toEqual({ kind: 'connectivity' })
  })

  it('草稿账户按 capabilityDomain 走 connectivity', () => {
    const payload = buildAccountTestPayload(
      { model: '', testEndpointMode: 'account_default' },
      undefined,
      {
        providerCode: 'qwen',
        name: 'draft',
        type: 'api_key',
        capabilityDomain: 'image',
        credentials: {},
        concurrencyLimit: 1,
        priority: 0,
        supportedModels: ['qwen-image'],
        healthCheckModel: 'qwen-image',
        healthCheckEndpointMode: 'images_json',
        modelMappings: [],
        groupId: 'g-1'
      }
    )
    expect(payload).toEqual({ kind: 'connectivity' })
  })

  it('连通性展示 mode 按场景映射（含 B7 工具场景）', () => {
    expect(connectivityEndpointModeForDomain('video')).toBe('video_create')
    expect(connectivityEndpointModeForDomain('image')).toBe('images_json')
    expect(connectivityEndpointModeForDomain('audio')).toBe('audio_speech')
    expect(connectivityEndpointModeForDomain('embedding')).toBe('embed_content')
    expect(connectivityEndpointModeForDomain('rerank')).toBe('rerank_json')
    expect(connectivityEndpointModeForDomain('moderation')).toBe('moderation_json')
  })
})
