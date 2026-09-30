import { describe, expect, it } from 'vitest'

import type { AccountEditBasicDetail } from '@/types/domain'
import { FALLBACK_PROVIDERS } from './accountOptions'
import { CREDENTIAL_CIPHER_PLACEHOLDER } from './accountCredentials'
import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import {
  buildAccountDraftTestPayload,
  validateAccountDraftTestForm
} from './accountDraftTestPayload'

const PH = CREDENTIAL_CIPHER_PLACEHOLDER

function editDetail(credentials: Record<string, unknown>, type: 'api_key' | 'oauth'): AccountEditBasicDetail {
  return {
    id: 'acc-1',
    configRevision: 3,
    ownerSystemAccountId: 'sys-1',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile-gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '掩码账户',
    type,
    credentials,
    credentialsMasked: true,
    status: 'active',
    concurrencyLimit: 5,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-5.6-sol'],
    tags: [],
    healthCheckModel: 'gpt-5.6-sol',
    healthCheckEndpointMode: 'chat_json'
  }
}

function apiKeyDraftForm(apiKeys: string[]): AccountFormModel {
  const form = defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
  form.name = '草稿测试账户'
  form.groupId = 'grp-1'
  form.baseUrl = 'https://api.openai.com/v1'
  form.supportedModels = ['gpt-5.6-sol']
  form.healthCheckModel = 'gpt-5.6-sol'
  form.apiKeys = [...apiKeys]
  form.apiKey = apiKeys[0] ?? ''
  return form
}

function oauthDraftForm(tokens: { accessToken?: string; refreshToken?: string } = {}): AccountFormModel {
  const form = defaultAccountForm('gpt', 'oauth', FALLBACK_PROVIDERS)
  form.name = '草稿测试账户'
  form.groupId = 'grp-1'
  form.baseUrl = 'https://chatgpt.com/backend-api/codex'
  form.supportedModels = ['gpt-5.6-sol']
  form.healthCheckModel = 'gpt-5.6-sol'
  form.accessToken = tokens.accessToken ?? ''
  form.refreshToken = tokens.refreshToken ?? ''
  return form
}

describe('buildAccountDraftTestPayload 占位回填（BUG-0243）', () => {
  it('api_key：已存 api_key 为占位时跳过回填，载荷不含占位符', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({
        api_key: PH,
        api_keys: [PH, PH],
        api_key_strategy: 'round_robin',
        api_key_weights: [1, 1],
        base_url: 'https://api.openai.com/v1'
      }, 'api_key'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeyDraftForm([PH, PH]),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials).not.toHaveProperty('api_key')
    expect(payload.credentials).not.toHaveProperty('api_keys')
    expect(payload.credentials.base_url).toBe('https://api.openai.com/v1')
    expect(JSON.stringify(payload.credentials)).not.toContain(PH)
  })

  it('api_key：已存值为真实明文时保留回填（非占位行为不变）', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({ api_key: 'sk-real-single', base_url: 'https://api.openai.com/v1' }, 'api_key'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeyDraftForm(['']),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials.api_key).toBe('sk-real-single')
  })

  it('oauth：已存 token 为占位时跳过回填，元数据键保留', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({
        access_token: PH,
        refresh_token: PH,
        client_id: 'cid-1',
        base_url: 'https://chatgpt.com/backend-api/codex'
      }, 'oauth'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: oauthDraftForm(),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials).not.toHaveProperty('access_token')
    expect(payload.credentials).not.toHaveProperty('refresh_token')
    expect(payload.credentials.client_id).toBe('cid-1')
    expect(JSON.stringify(payload.credentials)).not.toContain(PH)
  })

  it('oauth：已存 token 为真实明文时保留回填（非占位行为不变）', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({ access_token: 'at-real', refresh_token: PH }, 'oauth'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: oauthDraftForm(),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials.access_token).toBe('at-real')
    expect(payload.credentials).not.toHaveProperty('refresh_token')
  })

  it('oauth：表单值本身为占位（未 reveal 的输入框内容）时不得作为凭据发出', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({ access_token: PH, refresh_token: PH }, 'oauth'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: oauthDraftForm({ accessToken: PH, refreshToken: PH }),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials).not.toHaveProperty('access_token')
    expect(payload.credentials).not.toHaveProperty('refresh_token')
    expect(JSON.stringify(payload.credentials)).not.toContain(PH)
  })
})

describe('validateAccountDraftTestForm OAuth 测试凭据占位语义（BUG-0243）', () => {
  it('表单为空且已存 token 为占位：视同缺失，要求填写 Token', () => {
    const message = validateAccountDraftTestForm({
      accounts: [],
      accountDetail: editDetail({ access_token: PH, refresh_token: PH }, 'oauth'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: oauthDraftForm(),
      hasAuthSession: false,
      providers: FALLBACK_PROVIDERS
    })
    expect(message).toBe('请填写 Access Token 或 Refresh Token 后再测试')
  })

  it('已存 token 为真实明文：不再要求填写 Token', () => {
    const message = validateAccountDraftTestForm({
      accounts: [],
      accountDetail: editDetail({ access_token: 'at-real', refresh_token: 'rt-real' }, 'oauth'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: oauthDraftForm(),
      hasAuthSession: false,
      providers: FALLBACK_PROVIDERS
    })
    expect(message).toBeUndefined()
  })
})
