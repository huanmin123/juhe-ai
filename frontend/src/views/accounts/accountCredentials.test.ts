import { describe, expect, it } from 'vitest'

import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import {
  buildAccountCredentials,
  normalizedAccountApiKeys
} from './accountCredentials'
import { buildAccountBasicEditSnapshot } from './accountEditPatch'

function apiKeysForm(apiKeys: string[], weights?: number[], strategy: AccountFormModel['apiKeyStrategy'] = 'weighted_round_robin'): AccountFormModel {
  const form = defaultAccountForm('gpt', 'api_key')
  form.apiKey = apiKeys[0] ?? ''
  form.apiKeys = [...apiKeys]
  form.apiKeyStrategy = strategy
  form.apiKeyWeights = [...(weights ?? apiKeys.map(() => 1))]
  form.baseUrl = 'https://api.openai.com/v1'
  return form
}

describe('normalizedAccountApiKeys', () => {
  it('空行跳过，重复键值去重并保持行序', () => {
    expect(normalizedAccountApiKeys(apiKeysForm(['sk-a', 'sk-a', 'sk-b', '']))).toEqual(['sk-a', 'sk-b'])
  })

  it('apiKeys 为空时回退 apiKey 单值', () => {
    const form = apiKeysForm([])
    form.apiKey = 'sk-single'
    form.apiKeys = []
    expect(normalizedAccountApiKeys(form)).toEqual(['sk-single'])
  })

  it('首尾空白在归一化时去除', () => {
    expect(normalizedAccountApiKeys(apiKeysForm(['  sk-a  ', 'sk-a']))).toEqual(['sk-a'])
  })
})

describe('buildAccountCredentials 明文直提', () => {
  it('api_key：单行直提 api_key，不产生池键', () => {
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeysForm(['sk-single'], [1])
    })
    expect(credentials.api_key).toBe('sk-single')
    expect(credentials).not.toHaveProperty('api_keys')
    expect(credentials).not.toHaveProperty('api_key_strategy')
    expect(credentials).not.toHaveProperty('api_key_weights')
    expect(credentials.base_url).toBe('https://api.openai.com/v1')
  })

  it('api_key：多行全量提交并带 strategy 与对应权重', () => {
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeysForm(['sk-a', 'sk-b', 'sk-c'], [9, 2, 3])
    })
    expect(credentials.api_key).toBe('sk-a')
    expect(credentials.api_keys).toEqual(['sk-a', 'sk-b', 'sk-c'])
    expect(credentials.api_key_strategy).toBe('weighted_round_robin')
    expect(credentials.api_key_weights).toEqual([9, 2, 3])
  })

  it('api_key：非权重策略不带 api_key_weights', () => {
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeysForm(['sk-a', 'sk-b'], [1, 1], 'round_robin')
    })
    expect(credentials.api_keys).toEqual(['sk-a', 'sk-b'])
    expect(credentials.api_key_strategy).toBe('round_robin')
    expect(credentials).not.toHaveProperty('api_key_weights')
  })

  it('api_key：全部为空行时省略凭据键', () => {
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeysForm(['', ''])
    })
    expect(credentials).not.toHaveProperty('api_key')
    expect(credentials).not.toHaveProperty('api_keys')
    expect(credentials.base_url).toBe('https://api.openai.com/v1')
  })

  it('oauth：token 直提表单值，元数据键保留', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.baseUrl = 'https://chatgpt.com/backend-api/codex'
    form.accessToken = 'at-typed'
    form.refreshToken = 'rt-typed'
    const credentials = buildAccountCredentials({
      currentCredentials: { client_id: 'cid-1', expires_at: '2026-10-01T00:00:00Z' },
      errorPolicyRules: [],
      responseInspectionRules: [],
      form
    })
    expect(credentials.access_token).toBe('at-typed')
    expect(credentials.refresh_token).toBe('rt-typed')
    expect(credentials.client_id).toBe('cid-1')
    expect(credentials.base_url).toBe('https://chatgpt.com/backend-api/codex')
  })

  it('oauth：token 为空时省略该键', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.baseUrl = 'https://chatgpt.com/backend-api/codex'
    form.accessToken = ''
    form.refreshToken = ''
    const credentials = buildAccountCredentials({
      currentCredentials: { client_id: 'cid-1' },
      errorPolicyRules: [],
      responseInspectionRules: [],
      form
    })
    expect(credentials).not.toHaveProperty('access_token')
    expect(credentials).not.toHaveProperty('refresh_token')
    expect(credentials.client_id).toBe('cid-1')
  })

  it('google_oauth：access/refresh/client_secret 直提表单值', () => {
    const form = defaultAccountForm('gemini', 'google_oauth')
    form.baseUrl = 'https://generativelanguage.googleapis.com/v1beta'
    form.accessToken = 'at-typed'
    form.refreshToken = 'rt-typed'
    form.googleClientId = 'client-id-1'
    form.googleClientSecret = 'secret-typed'
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form
    })
    expect(credentials.access_token).toBe('at-typed')
    expect(credentials.refresh_token).toBe('rt-typed')
    expect(credentials.client_id).toBe('client-id-1')
    expect(credentials.client_secret).toBe('secret-typed')
  })

  it('google_oauth：client_secret 为空时省略，client_id 原样提交', () => {
    const form = defaultAccountForm('gemini', 'google_oauth')
    form.baseUrl = 'https://generativelanguage.googleapis.com/v1beta'
    form.googleClientId = 'client-id-1'
    form.googleClientSecret = ''
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form
    })
    expect(credentials).not.toHaveProperty('client_secret')
    expect(credentials.client_id).toBe('client-id-1')
  })
})

describe('buildAccountBasicEditSnapshot 明文直提', () => {
  it('api_key：全行提交，与已存明文基线一致（保存无差异）', () => {
    const form = apiKeysForm(['sk-a', 'sk-b'], [1, 2])
    const current = buildAccountBasicEditSnapshot(form, {
      api_key: 'sk-a',
      api_keys: ['sk-a', 'sk-b'],
      api_key_weights: [1, 2],
      api_key_strategy: 'weighted_round_robin',
      base_url: 'https://api.openai.com/v1'
    })
    expect(current.credentials.api_key).toBe('sk-a')
    expect(current.credentials.api_keys).toEqual(['sk-a', 'sk-b'])
    expect(current.credentials.api_key_weights).toEqual([1, 2])
    expect(current.credentials.base_url).toBe('https://api.openai.com/v1')
  })

  it('oauth：表单值直提；空值回退已存明文', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.baseUrl = 'https://chatgpt.com/backend-api/codex'
    form.accessToken = 'at-typed'
    form.refreshToken = ''
    const current = buildAccountBasicEditSnapshot(form, { access_token: 'at-stored', refresh_token: 'rt-stored' })
    expect(current.credentials.access_token).toBe('at-typed')
    expect(current.credentials.refresh_token).toBe('rt-stored')
  })

  it('oauth：表单与已存均无值时省略该键', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.baseUrl = 'https://chatgpt.com/backend-api/codex'
    form.accessToken = ''
    form.refreshToken = ''
    const current = buildAccountBasicEditSnapshot(form, {})
    expect(current.credentials).not.toHaveProperty('access_token')
    expect(current.credentials).not.toHaveProperty('refresh_token')
  })
})
