import { describe, expect, it } from 'vitest'

import type { AccountEditBasicDetail } from '@/types/domain'
import { FALLBACK_PROVIDERS } from './accountOptions'
import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import {
  buildAccountDraftTestPayload,
  validateAccountDraftTestForm
} from './accountDraftTestPayload'

function editDetail(credentials: Record<string, unknown>, type: 'api_key' | 'oauth'): AccountEditBasicDetail {
  return {
    id: 'acc-1',
    configRevision: 3,
    ownerSystemAccountId: 'sys-1',
    providerCode: 'gpt',
    providerProtocolProfileId: 'profile-gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '明文账户',
    type,
    credentials,
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

describe('buildAccountDraftTestPayload 已存明文回填', () => {
  it('api_key：已存值为真实明文时保留回填', () => {
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

  it('api_key：编辑态已存明文 api_key 直接进测试载荷', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({ api_key: 'sk-stored', base_url: 'https://api.openai.com/v1' }, 'api_key'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeyDraftForm(['sk-stored']),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials.api_key).toBe('sk-stored')
    expect(payload.credentials.base_url).toBe('https://api.openai.com/v1')
  })

  it('oauth：已存 token 为真实明文时保留回填', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({ access_token: 'at-real', refresh_token: 'rt-real', base_url: 'https://chatgpt.com/backend-api/codex' }, 'oauth'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: oauthDraftForm(),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials.access_token).toBe('at-real')
    expect(payload.credentials.refresh_token).toBe('rt-real')
  })

  it('oauth：表单直录的明文 token 优先进入测试载荷', () => {
    const payload = buildAccountDraftTestPayload({
      accounts: [],
      accountDetail: editDetail({ access_token: 'at-stored', base_url: 'https://chatgpt.com/backend-api/codex' }, 'oauth'),
      editingId: 'acc-1',
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: oauthDraftForm({ accessToken: 'at-typed' }),
      providers: FALLBACK_PROVIDERS
    })
    expect(payload.credentials.access_token).toBe('at-typed')
  })
})

describe('validateAccountDraftTestForm OAuth 测试凭据', () => {
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
