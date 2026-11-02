import { describe, expect, it } from 'vitest'

import {
  MINIMAX_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID
} from '@/shared/providerProtocol'
import type { AccountEditBasicDetail, AccountCloneContext } from '@/types/domain'

import { FALLBACK_PROVIDERS } from './accountOptions'
import { buildAccountBasicEditFormLoad, buildAccountCloneFormLoad } from './accountEditFormLoaders'
import { defaultAccountForm } from './accountFormDefaults'

function editDetail(partial: Partial<AccountEditBasicDetail>): AccountEditBasicDetail {
  return {
    id: 'acc_t102',
    configRevision: 1,
    ownerSystemAccountId: 'sys-root',
    providerCode: 'minimax',
    providerProtocolProfileId: '',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: 'T102 派生用例',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 20,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: '',
    supportedModels: ['MiniMax-M3'],
    tags: [],
    healthCheckModel: 'MiniMax-M3',
    healthCheckEndpointMode: 'video_create',
    ...partial
  } as AccountEditBasicDetail
}

function basicCredentials(baseUrl = 'https://api.minimax.chat/v1'): Record<string, unknown> {
  return { base_url: baseUrl }
}

describe('buildAccountBasicEditFormLoad 档案派生（T102）', () => {
  it('minimax 账户：详情档案空 + 连接 preset → 派生供应商与档案身份（原来被「配置不完整」拦死）', () => {
    const defaults = defaultAccountForm('minimax', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountBasicEditFormLoad({
      account: editDetail({ providerCode: 'minimax', providerProtocolProfileId: '', connectionPresetCode: 'minimax_media' }),
      credentials: basicCredentials(),
      defaults
    })
    expect(patch.providerCode).toBe('minimax')
    expect(patch.providerProtocolProfileId).toBe(MINIMAX_OPENAI_V1_PROFILE_ID)
  })

  it('volcengine OpenSpeech 音频账户：派生 OpenSpeech 档案，保存链可正向导出同 preset', () => {
    const defaults = defaultAccountForm('volcengine', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountBasicEditFormLoad({
      account: editDetail({
        providerCode: 'volcengine',
        providerProtocolProfileId: '',
        connectionPresetCode: 'volcengine_openspeech',
        healthCheckEndpointMode: 'audio_speech'
      }),
      credentials: { base_url: 'https://openspeech.bytedance.com', speech_appid: 'app-1', speech_token: 'token-1' },
      defaults
    })
    expect(patch.providerCode).toBe('volcengine')
    expect(patch.providerProtocolProfileId).toBe(VOLCENGINE_OPENSPEECH_OPENAI_V1_PROFILE_ID)
    expect(patch.speechAppId).toBe('app-1')
    expect(patch.speechToken).toBe('token-1')
  })

  it('generic_http 账户：vendor 投影空 providerCode + 探针目录 id → 派生 openai 兼容身份（原来被「请先选择供应商」拦死）', () => {
    const defaults = defaultAccountForm('', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountBasicEditFormLoad({
      account: editDetail({
        providerCode: '',
        providerProtocolProfileId: 'profile_generic_openai_v1',
        connectionPresetCode: 'generic_openai_v1'
      }),
      credentials: { base_url: 'https://relay.example.com/v1', api_key: 'sk-relay' },
      defaults
    })
    expect(patch.providerCode).toBe('openai')
    expect(patch.providerProtocolProfileId).toBe(OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID)
  })

  it('详情档案前端可识别时保持原值（已派生成功的探针表账户零回归）', () => {
    const defaults = defaultAccountForm('deepseek', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountBasicEditFormLoad({
      account: editDetail({
        providerCode: 'deepseek',
        providerProtocolProfileId: 'profile_deepseek_anthropic_v1',
        connectionPresetCode: 'deepseek_anthropic_v1'
      }),
      credentials: { base_url: 'https://api.deepseek.com/anthropic/v1' },
      defaults
    })
    expect(patch.providerCode).toBe('deepseek')
    expect(patch.providerProtocolProfileId).toBe('profile_deepseek_anthropic_v1')
  })

  it('未知 preset 且无供应商身份：fail-closed，providerCode 保持空由保存校验拦下', () => {
    const defaults = defaultAccountForm('', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountBasicEditFormLoad({
      account: editDetail({
        providerCode: '',
        providerProtocolProfileId: '',
        connectionPresetCode: 'future_preset_v9'
      }),
      credentials: { base_url: 'https://relay.example.com/v1' },
      defaults
    })
    expect(patch.providerCode).toBe('')
  })
})

describe('buildAccountCloneFormLoad 档案派生（T102：克隆链同源缺口）', () => {
  function cloneContext(partial: Partial<AccountCloneContext>): AccountCloneContext {
    return {
      id: 'acc_t102',
      configRevision: 1,
      providerCode: 'minimax',
      providerProtocolProfileId: '',
      protocolCode: 'openai',
      protocolVersion: 'v1',
      name: '克隆源',
      type: 'api_key',
      status: 'active',
      credentialOptions: { base_url: 'https://api.minimax.chat/v1' },
      concurrencyLimit: 20,
      priority: 0,
      superPriorityEnabled: false,
      fallbackEnabled: false,
      clientCompatibility: '',
      supportedModels: ['MiniMax-M3'],
      tags: [],
      healthCheckModel: 'MiniMax-M3',
      healthCheckEndpointMode: 'video_create',
      modelMappings: [],
      temporaryUnavailableContinuousProbeEnabled: true,
      backgroundProbeEnabled: true,
      balanceQueryEnabled: false,
      ...partial
    } as AccountCloneContext
  }

  it('minimax 克隆源：详情档案空 → 供应商身份保住，档案落供应商默认（可保存）', () => {
    const defaults = defaultAccountForm('minimax', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountCloneFormLoad({
      account: cloneContext({ providerCode: 'minimax', providerProtocolProfileId: '' }),
      defaults
    })
    expect(patch.providerCode).toBe('minimax')
    expect(patch.providerProtocolProfileId).toBe(MINIMAX_OPENAI_V1_PROFILE_ID)
  })

  it('generic 克隆源（无 preset 字段、vendor 空）：fail-closed 保持 providerCode 空', () => {
    const defaults = defaultAccountForm('', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountCloneFormLoad({
      account: cloneContext({ providerCode: '', providerProtocolProfileId: 'profile_generic_openai_v1' }),
      defaults
    })
    expect(patch.providerCode).toBe('')
  })

  it('OpenSpeech 克隆源：档案空 + vendor 身份在 → 落供应商默认档案（volcengine 默认 Ark）', () => {
    const defaults = defaultAccountForm('volcengine', 'api_key', FALLBACK_PROVIDERS)
    const { patch } = buildAccountCloneFormLoad({
      account: cloneContext({ providerCode: 'volcengine', providerProtocolProfileId: '' }),
      defaults
    })
    expect(patch.providerCode).toBe('volcengine')
    expect(patch.providerProtocolProfileId).toBe('profile_volcengine_openai_v1')
  })
})
