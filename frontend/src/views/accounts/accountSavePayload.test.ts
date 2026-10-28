import { describe, expect, it } from 'vitest'

import { validateAccountModelMappings, validateAccountSaveForm } from './accountSavePayload'
import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import { FALLBACK_PROVIDERS } from './accountOptions'

const mapping: AccountFormModel['modelMappings'][number] = {
  sourceModel: 'legacy-client-model',
  sourceEndpointFamily: 'chat_completions',
  upstreamModel: 'legacy-upstream-model',
  upstreamEndpointFamily: 'chat_completions',
  enabled: true
}
const openAIProfile = FALLBACK_PROVIDERS.find((provider) => provider.code === 'openai')!

describe('validateAccountModelMappings baseline changes', () => {
  it('revalidates an enabled-only change against current model candidates', () => {
    const changed = { ...mapping, enabled: false }

    const error = validateAccountModelMappings(
      [changed],
      [],
      ['chat_json'],
      openAIProfile,
      {
        baselineMappings: [mapping],
        mappingCurrentProviderSourceModelOptions: [],
        mappingUpstreamModelOptions: []
      }
    )

    expect(error).toContain('模型映射的上游模型必须从支持模型中选择：legacy-upstream-model')
  })

  it('keeps an unchanged baseline row exempt from the current candidate window', () => {
    expect(validateAccountModelMappings([mapping], [], ['chat_json'], openAIProfile, { baselineMappings: [mapping] })).toBeUndefined()
  })
})

describe('validateAccountSaveForm oauthDefaultsAllowed（access_token 直录无派生兜底）', () => {
  it('四家托管供应商 access_token 直录创建空支持模型在提交前被拦截', () => {
    const directTokenForm = defaultAccountForm('gpt', 'oauth', FALLBACK_PROVIDERS)
    directTokenForm.oauthMode = 'access_token'
    directTokenForm.name = '直录 Token 账户'
    directTokenForm.groupId = 'group-direct-token'
    directTokenForm.group = { id: directTokenForm.groupId, name: '直录 Token 分组' }
    directTokenForm.baseUrl = 'https://api.openai.com/v1'
    directTokenForm.accessToken = 'at-direct-token'
    directTokenForm.supportedModels = []
    expect(validateAccountSaveForm({
      form: directTokenForm,
      hasAuthSession: false,
      errorPolicyRules: [],
      responseInspectionRules: [],
      providers: FALLBACK_PROVIDERS
    })).toBe('请选择支持模型')
  })

  it('托管授权形态（refresh_token）空支持模型仍放行，由后端 CreateOAuthAccount 派生', () => {
    const managedForm = defaultAccountForm('gpt', 'oauth', FALLBACK_PROVIDERS)
    managedForm.oauthMode = 'refresh_token'
    managedForm.name = '托管创建账户'
    managedForm.groupId = 'group-managed-create'
    managedForm.group = { id: managedForm.groupId, name: '托管创建分组' }
    managedForm.baseUrl = 'https://api.openai.com/v1'
    managedForm.refreshToken = 'rt-managed'
    managedForm.proxyProfileId = 'proxy-managed'
    managedForm.supportedModels = []
    expect(validateAccountSaveForm({
      form: managedForm,
      hasAuthSession: false,
      errorPolicyRules: [],
      responseInspectionRules: [],
      providers: FALLBACK_PROVIDERS
    })).toBeUndefined()
  })
})
