import { describe, expect, it } from 'vitest'

import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import {
  CREDENTIAL_CIPHER_PLACEHOLDER,
  applyRevealedAccountCredentials,
  buildAccountCredentials,
  isCredentialCipherPlaceholder,
  normalizedAccountApiKeys
} from './accountCredentials'
import { buildAccountBasicEditSnapshot } from './accountEditPatch'

const PH = CREDENTIAL_CIPHER_PLACEHOLDER

function apiKeysForm(apiKeys: string[], weights?: number[], strategy: AccountFormModel['apiKeyStrategy'] = 'weighted_round_robin'): AccountFormModel {
  const form = defaultAccountForm('gpt', 'api_key')
  form.apiKey = apiKeys[0] ?? ''
  form.apiKeys = [...apiKeys]
  form.apiKeyStrategy = strategy
  form.apiKeyWeights = [...(weights ?? apiKeys.map(() => 1))]
  form.baseUrl = 'https://api.openai.com/v1'
  return form
}

describe('isCredentialCipherPlaceholder', () => {
  it('识别契约占位符字面量（含首尾空白）', () => {
    expect(isCredentialCipherPlaceholder(PH)).toBe(true)
    expect(isCredentialCipherPlaceholder(`  ${PH}  `)).toBe(true)
    expect(isCredentialCipherPlaceholder('sk-real')).toBe(false)
    expect(isCredentialCipherPlaceholder('')).toBe(false)
    expect(isCredentialCipherPlaceholder(null)).toBe(false)
  })
})

describe('normalizedAccountApiKeys 占位行语义', () => {
  it('占位行各占一行，不去重（维持池行数/权重列语义）', () => {
    expect(normalizedAccountApiKeys(apiKeysForm([PH, PH, PH]))).toEqual([PH, PH, PH])
  })

  it('真实键值仍按既有规则去重', () => {
    expect(normalizedAccountApiKeys(apiKeysForm(['sk-a', 'sk-a', 'sk-b']))).toEqual(['sk-a', 'sk-b'])
  })

  it('混合占位与真实值时保持行序', () => {
    expect(normalizedAccountApiKeys(apiKeysForm([PH, 'sk-a', PH]))).toEqual([PH, 'sk-a', PH])
  })
})

describe('buildAccountCredentials 保存占位过滤', () => {
  it('api_key：全部占位时省略 api_key/api_keys/api_key_weights', () => {
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeysForm([PH, PH, PH], [1, 2, 3])
    })
    expect(credentials).not.toHaveProperty('api_key')
    expect(credentials).not.toHaveProperty('api_keys')
    expect(credentials).not.toHaveProperty('api_key_strategy')
    expect(credentials).not.toHaveProperty('api_key_weights')
    expect(credentials.base_url).toBe('https://api.openai.com/v1')
  })

  it('api_key：滤除占位项后提交剩余键与对应权重', () => {
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeysForm([PH, 'sk-a', 'sk-b'], [9, 2, 3])
    })
    expect(credentials.api_key).toBe('sk-a')
    expect(credentials.api_keys).toEqual(['sk-a', 'sk-b'])
    expect(credentials.api_key_weights).toEqual([2, 3])
  })

  it('api_key：用户实际输入（非占位）照常提交', () => {
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form: apiKeysForm(['sk-typed'], [1])
    })
    expect(credentials.api_key).toBe('sk-typed')
    expect(credentials).not.toHaveProperty('api_keys')
  })

  it('oauth：access_token/refresh_token 占位时省略，元数据键保留', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.baseUrl = 'https://chatgpt.com/backend-api/codex'
    form.accessToken = PH
    form.refreshToken = PH
    const credentials = buildAccountCredentials({
      currentCredentials: { client_id: 'cid-1', expires_at: '2026-10-01T00:00:00Z' },
      errorPolicyRules: [],
      responseInspectionRules: [],
      form
    })
    expect(credentials).not.toHaveProperty('access_token')
    expect(credentials).not.toHaveProperty('refresh_token')
    expect(credentials.client_id).toBe('cid-1')
    expect(credentials.base_url).toBe('https://chatgpt.com/backend-api/codex')
  })

  it('google_oauth：client_secret 占位时省略，client_id 原样提交', () => {
    const form = defaultAccountForm('gemini', 'google_oauth')
    form.baseUrl = 'https://generativelanguage.googleapis.com/v1beta'
    form.accessToken = PH
    form.refreshToken = ''
    form.googleClientId = 'client-id-1'
    form.googleClientSecret = PH
    const credentials = buildAccountCredentials({
      errorPolicyRules: [],
      responseInspectionRules: [],
      form
    })
    expect(credentials).not.toHaveProperty('access_token')
    expect(credentials).not.toHaveProperty('refresh_token')
    expect(credentials).not.toHaveProperty('client_secret')
    expect(credentials.client_id).toBe('client-id-1')
  })
})

describe('applyRevealedAccountCredentials reveal 写回', () => {
  it('api_key：以服务端真实池重建全部行并同步 apiKey', () => {
    const form = apiKeysForm([PH, PH], [1, 2])
    applyRevealedAccountCredentials(form, { api_keys: ['sk-real-1', 'sk-real-2', 'sk-real-3'] })
    expect(form.apiKeys).toEqual(['sk-real-1', 'sk-real-2', 'sk-real-3'])
    expect(form.apiKey).toBe('sk-real-1')
    expect(form.apiKeyWeights).toEqual([1, 2])
  })

  it('api_key：单键账户按 api_key 投影重建', () => {
    const form = apiKeysForm([PH])
    applyRevealedAccountCredentials(form, { api_key: 'sk-single' })
    expect(form.apiKeys).toEqual(['sk-single'])
    expect(form.apiKey).toBe('sk-single')
  })

  it('oauth：替换 token 字段，不动 googleClientSecret', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.accessToken = PH
    form.refreshToken = PH
    form.googleClientSecret = 'typed-secret'
    applyRevealedAccountCredentials(form, { access_token: 'at-real', refresh_token: 'rt-real' })
    expect(form.accessToken).toBe('at-real')
    expect(form.refreshToken).toBe('rt-real')
    expect(form.googleClientSecret).toBe('typed-secret')
  })

  it('google_oauth：替换 access/refresh/client_secret', () => {
    const form = defaultAccountForm('gemini', 'google_oauth')
    form.accessToken = PH
    form.refreshToken = PH
    form.googleClientSecret = PH
    applyRevealedAccountCredentials(form, {
      access_token: 'at-real',
      refresh_token: 'rt-real',
      client_secret: 'secret-real'
    })
    expect(form.accessToken).toBe('at-real')
    expect(form.refreshToken).toBe('rt-real')
    expect(form.googleClientSecret).toBe('secret-real')
  })
})

describe('buildAccountBasicEditSnapshot 基础编辑占位过滤', () => {
  it('api_key：占位池不产生凭据键，与加载基线一致（保存无差异）', () => {
    const form = apiKeysForm([PH, PH], [1, 2])
    const current = buildAccountBasicEditSnapshot(form, {
      api_key: PH,
      api_keys: [PH, PH],
      api_key_weights: [1, 2],
      api_key_strategy: 'weighted_round_robin',
      base_url: 'https://api.openai.com/v1'
    })
    expect(current.credentials).not.toHaveProperty('api_key')
    expect(current.credentials).not.toHaveProperty('api_keys')
    expect(current.credentials).not.toHaveProperty('api_key_weights')
    expect(current.credentials.base_url).toBe('https://api.openai.com/v1')
  })

  it('oauth：表单占位且已存值占位时省略；用户输入真实值时提交', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.baseUrl = 'https://chatgpt.com/backend-api/codex'
    form.accessToken = PH
    form.refreshToken = PH
    const masked = buildAccountBasicEditSnapshot(form, { access_token: PH, refresh_token: PH })
    expect(masked.credentials).not.toHaveProperty('access_token')
    expect(masked.credentials).not.toHaveProperty('refresh_token')

    form.refreshToken = 'rt-typed'
    const edited = buildAccountBasicEditSnapshot(form, { access_token: PH, refresh_token: PH })
    expect(edited.credentials).not.toHaveProperty('access_token')
    expect(edited.credentials.refresh_token).toBe('rt-typed')
  })

  it('oauth：清空字段回退已存占位时同样省略（不得回写占位符）', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.baseUrl = 'https://chatgpt.com/backend-api/codex'
    form.accessToken = ''
    form.refreshToken = ''
    const cleared = buildAccountBasicEditSnapshot(form, { access_token: PH, refresh_token: PH })
    expect(cleared.credentials).not.toHaveProperty('access_token')
    expect(cleared.credentials).not.toHaveProperty('refresh_token')
  })
})
