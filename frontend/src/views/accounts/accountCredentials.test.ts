import { describe, expect, it } from 'vitest'

import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import {
  CREDENTIAL_CIPHER_PLACEHOLDER,
  accountApiKeyPoolBaseline,
  applyRevealedAccountCredentials,
  buildAccountCredentials,
  isCredentialCipherPlaceholder,
  normalizedAccountApiKeys,
  validateAccountApiKeyCipherRows
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

  // BUG-0243 问题 5b：reveal 保留占位态下用户已输入的新值。
  it('api_key：占位态新输入的行保留，占位行按序填入服务端真实值（BUG-0243 问题 5b）', () => {
    const form = apiKeysForm([PH, PH, 'sk-user-new'], [1, 2, 3])
    applyRevealedAccountCredentials(form, { api_keys: ['sk-real-1', 'sk-real-2'] })
    expect(form.apiKeys).toEqual(['sk-real-1', 'sk-real-2', 'sk-user-new'])
    expect(form.apiKey).toBe('sk-real-1')
  })

  it('api_key：服务端未被占位行消费的真实 Key 追加行尾，不静默丢弃（BUG-0243 问题 5b）', () => {
    const form = apiKeysForm(['sk-user-new', PH], [1, 2])
    applyRevealedAccountCredentials(form, { api_keys: ['sk-real-1', 'sk-real-2'] })
    expect(form.apiKeys).toEqual(['sk-user-new', 'sk-real-1', 'sk-real-2'])
  })

  it('api_key：与服务端真实值重复的用户输入按既有规则去重（BUG-0243 问题 5b）', () => {
    const form = apiKeysForm([PH, 'sk-real-1'], [1, 2])
    applyRevealedAccountCredentials(form, { api_keys: ['sk-real-1', 'sk-real-2'] })
    expect(form.apiKeys).toEqual(['sk-real-1', 'sk-real-2'])
  })

  it('api_key：占位行多于服务端池时多余占位行剔除，不留占位符（BUG-0243 问题 5b）', () => {
    const form = apiKeysForm([PH, PH, PH], [1, 2, 3])
    applyRevealedAccountCredentials(form, { api_keys: ['sk-real-1'] })
    expect(form.apiKeys).toEqual(['sk-real-1'])
    expect(form.apiKeys.some((key) => isCredentialCipherPlaceholder(key))).toBe(false)
  })

  it('oauth：用户已输入的新 token 保留，占位/空字段由服务端真实值替换（BUG-0243 问题 5b）', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.accessToken = 'typed-access-token'
    form.refreshToken = ''
    applyRevealedAccountCredentials(form, { access_token: 'at-real', refresh_token: 'rt-real' })
    expect(form.accessToken).toBe('typed-access-token')
    expect(form.refreshToken).toBe('rt-real')
  })

  it('google_oauth：用户已输入的 client_secret 保留，占位 token 字段替换（BUG-0243 问题 5b）', () => {
    const form = defaultAccountForm('gemini', 'google_oauth')
    form.accessToken = PH
    form.googleClientSecret = 'typed-secret'
    applyRevealedAccountCredentials(form, {
      access_token: 'at-real',
      refresh_token: 'rt-real',
      client_secret: 'secret-real'
    })
    expect(form.accessToken).toBe('at-real')
    expect(form.googleClientSecret).toBe('typed-secret')
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

describe('accountApiKeyPoolBaseline 掩码池基线提取（BUG-0243）', () => {
  it('掩码 api_keys 按行计数，策略与权重保留真实语义', () => {
    const baseline = accountApiKeyPoolBaseline({
      api_keys: [PH, PH, PH],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [1, 2, 3]
    })
    expect(baseline).toEqual({ poolCount: 3, strategy: 'weighted_round_robin', weights: [1, 2, 3] })
  })

  it('单键账户按 api_key 计数，无策略/权重', () => {
    expect(accountApiKeyPoolBaseline({ api_key: PH })).toEqual({ poolCount: 1 })
  })

  it('无已存凭据时返回 undefined（创建路径无基线）', () => {
    expect(accountApiKeyPoolBaseline(undefined)).toBeUndefined()
    expect(accountApiKeyPoolBaseline({ base_url: 'https://api.openai.com/v1' })).toBeUndefined()
  })
})

describe('validateAccountApiKeyCipherRows 占位行保存守卫（BUG-0243）', () => {
  it('混合占位与真实行：阻断并提示先获取明文（P1 池截断场景）', () => {
    const message = validateAccountApiKeyCipherRows(apiKeysForm([PH, 'sk-new', PH], [1, 1, 1]))
    expect(message).toBe('部分密钥仍为密文占位，请先点击眼睛获取明文，或清空占位行后再保存')
  })

  it('混合行无需基线即阻断（无已存凭据的路径同样适用）', () => {
    expect(validateAccountApiKeyCipherRows(apiKeysForm(['sk-new', PH]), undefined)).toBeTruthy()
  })

  it('纯占位池：策略相对基线变化时提示需先获取明文（P3）', () => {
    const form = apiKeysForm([PH, PH], [1, 1], 'round_robin')
    const baseline = accountApiKeyPoolBaseline({
      api_keys: [PH, PH],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [1, 1]
    })
    expect(validateAccountApiKeyCipherRows(form, baseline)).toBe('密钥仍为密文占位，修改密钥行、策略或权重前请先点击眼睛获取明文')
  })

  it('纯占位池：权重相对基线变化时提示需先获取明文（P3）', () => {
    const form = apiKeysForm([PH, PH], [1, 5], 'weighted_round_robin')
    const baseline = accountApiKeyPoolBaseline({
      api_keys: [PH, PH],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [1, 2]
    })
    expect(validateAccountApiKeyCipherRows(form, baseline)).toBeTruthy()
  })

  it('纯占位池：删除占位行时提示需先获取明文（P3）', () => {
    const form = apiKeysForm([PH], [1], 'round_robin')
    const baseline = accountApiKeyPoolBaseline({
      api_keys: [PH, PH],
      api_key_strategy: 'round_robin',
      api_key_weights: [1, 1]
    })
    expect(validateAccountApiKeyCipherRows(form, baseline)).toBeTruthy()
  })

  it('纯占位池且行数/策略/权重未变：放行，保持既有"未检测到修改"路径', () => {
    const form = apiKeysForm([PH, PH], [1, 2], 'weighted_round_robin')
    const baseline = accountApiKeyPoolBaseline({
      api_keys: [PH, PH],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [1, 2]
    })
    expect(validateAccountApiKeyCipherRows(form, baseline)).toBeUndefined()
  })

  it('全部真实值：不误伤正常保存（含 reveal 后重建的池）', () => {
    const baseline = accountApiKeyPoolBaseline({
      api_keys: [PH, PH],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [1, 2]
    })
    expect(validateAccountApiKeyCipherRows(apiKeysForm(['sk-a', 'sk-b'], [1, 2]), baseline)).toBeUndefined()
    expect(validateAccountApiKeyCipherRows(apiKeysForm(['sk-single']), undefined)).toBeUndefined()
  })

  it('非 api_key 表单不触发守卫', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    expect(validateAccountApiKeyCipherRows(form, accountApiKeyPoolBaseline({ access_token: PH }))).toBeUndefined()
  })
})
