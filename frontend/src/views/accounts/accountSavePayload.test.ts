import { describe, expect, it } from 'vitest'

import { buildAccountSavePayload, buildAccountUpdatePayload, validateAccountModelMappings, validateAccountSaveForm } from './accountSavePayload'
import {
  buildAccountAdvancedUpdatePatch,
  buildAccountBasicEditSnapshot,
  buildAccountBasicUpdatePatch
} from './accountEditPatch'
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

describe('backgroundProbeEnabled 手动模式（AI账户后台自动探测总开关设计 §2/§4）', () => {
  function manualModeForm(): AccountFormModel {
    const form = defaultAccountForm('gpt', 'api_key')
    form.name = '手动模式单测账户'
    form.groupId = 'group-manual-probe'
    form.apiKey = 'sk-manual-probe'
    form.apiKeys = ['sk-manual-probe']
    form.baseUrl = 'https://api.openai.com/v1'
    form.supportedModels = ['gpt-5.4-mini']
    form.healthCheckModel = 'gpt-5.4-mini'
    form.backgroundProbeEnabled = false
    return form
  }

  it('创建 payload 携带总开关关闭值，编辑 payload 构造保留该键供 PATCH delta 差量', () => {
    const payload = buildAccountSavePayload({
      accounts: [],
      form: manualModeForm(),
      errorPolicyRules: [],
      responseInspectionRules: []
    })
    expect(payload.backgroundProbeEnabled).toBe(false)
    expect(buildAccountUpdatePayload(payload).backgroundProbeEnabled).toBe(false)
  })

  it('媒体域创建/编辑 payload 均不携带总开关键', () => {
    const form = manualModeForm()
    form.capabilityDomain = 'audio'
    const payload = buildAccountSavePayload({
      accounts: [],
      form,
      errorPolicyRules: [],
      responseInspectionRules: []
    })
    expect(payload).not.toHaveProperty('backgroundProbeEnabled')
    expect(buildAccountUpdatePayload(payload)).not.toHaveProperty('backgroundProbeEnabled')
  })

  it('手动模式显式选择待检查在提交前被拦截', () => {
    const form = manualModeForm()
    form.status = 'pending_test'
    form.statusSelectionExplicit = true
    expect(validateAccountSaveForm({
      form,
      hasAuthSession: false,
      errorPolicyRules: [],
      responseInspectionRules: [],
      providers: FALLBACK_PROVIDERS
    })).toBe('手动模式不适用待检查状态：请选择可调度或停用')
  })

  it('非显式选择的过程态账户（异常装载映射）放行，PATCH 不携带 status 行为零变化', () => {
    const form = manualModeForm()
    form.status = 'pending_test'
    form.statusSelectionExplicit = false
    expect(validateAccountSaveForm({
      form,
      hasAuthSession: false,
      errorPolicyRules: [],
      responseInspectionRules: [],
      providers: FALLBACK_PROVIDERS
    })).toBeUndefined()
  })
})

describe('connections 编辑链透传（T94：编辑 Base URL 必须同步连接域）', () => {
  function apiKeyForm(): AccountFormModel {
    const form = defaultAccountForm('gpt', 'api_key')
    form.name = '连接域同步单测账户'
    form.groupId = 'group-conn-sync'
    form.apiKey = 'sk-conn-sync'
    form.apiKeys = ['sk-conn-sync']
    form.baseUrl = 'https://old.example.com/v1'
    form.supportedModels = ['gpt-5.4-mini']
    form.healthCheckModel = 'gpt-5.4-mini'
    return form
  }

  it('编辑 payload 构造与创建链同构携带连接集合', () => {
    const payload = buildAccountSavePayload({
      accounts: [],
      form: apiKeyForm(),
      errorPolicyRules: [],
      responseInspectionRules: []
    })
    expect(payload.connections).toHaveLength(1)
    expect(buildAccountUpdatePayload(payload).connections).toEqual(payload.connections)
  })

  it('修改 Base URL 后编辑 payload 的连接集合携带新地址（网关拨号事实源随之更新）', () => {
    const form = apiKeyForm()
    form.baseUrl = 'https://new.example.com/v1'
    const payload = buildAccountSavePayload({
      accounts: [],
      form,
      errorPolicyRules: [],
      responseInspectionRules: []
    })
    expect(buildAccountUpdatePayload(payload).connections?.[0]?.baseUrl).toBe('https://new.example.com/v1')
  })

  it('OAuth 清空 Base URL 时编辑 payload 不携带连接集合（不触发空集合整替换）', () => {
    const form = defaultAccountForm('gpt', 'oauth')
    form.name = 'OAuth 无连接单测'
    form.groupId = 'group-oauth-conn'
    form.baseUrl = ''
    form.accessToken = 'at-conn-sync'
    form.supportedModels = ['gpt-5.4-mini']
    form.healthCheckModel = 'gpt-5.4-mini'
    const payload = buildAccountSavePayload({
      accounts: [],
      form,
      errorPolicyRules: [],
      responseInspectionRules: []
    })
    expect(payload.connections).toBeUndefined()
    expect(buildAccountUpdatePayload(payload)).not.toHaveProperty('connections')
  })
})

// T94 差量口径：编辑链真正提交的是 buildAccountAdvancedUpdatePatch /
// buildAccountBasicUpdatePatch 产出的 PATCH delta，而不是整份 payload。
// 「只提交相对基线变化的字段」契约要求 baseUrl 未变时不出现 connections，
// 变化时整集合提交且与凭据域 base_url 同源同值。
describe('connections 编辑差量（T94：仅 Base URL/请求形态变化时进入 PATCH）', () => {
  function editForm(): AccountFormModel {
    const form = defaultAccountForm('gpt', 'api_key')
    form.name = '连接差量单测账户'
    form.groupId = 'group-conn-delta'
    form.apiKey = 'sk-conn-delta'
    form.apiKeys = ['sk-conn-delta']
    form.baseUrl = 'https://old.example.com/v1'
    form.supportedModels = ['gpt-5.4-mini']
    form.healthCheckModel = 'gpt-5.4-mini'
    form.healthCheckEndpointMode = 'chat_json'
    return form
  }

  function payloadFor(form: AccountFormModel) {
    return buildAccountSavePayload({
      accounts: [],
      form,
      errorPolicyRules: [],
      responseInspectionRules: []
    })
  }

  it('高级编辑 Base URL 变化时 delta 携带同构 connections 且 baseUrl 为新值', () => {
    const baseline = payloadFor(editForm())
    const currentForm = editForm()
    currentForm.baseUrl = 'https://new.example.com/v1'
    const current = payloadFor(currentForm)

    const delta = buildAccountAdvancedUpdatePatch(current, baseline, 12)

    expect(delta?.expectedConfigRevision).toBe(12)
    expect(delta?.connections).toEqual([{
      connectionPresetCode: 'gpt_openai_v1',
      baseUrl: 'https://new.example.com/v1',
      endpointModes: currentForm.supportedEndpointModes,
      enabled: true
    }])
    // 凭据域与连接域同源同值：两个副本一起对齐，避免再次分叉。
    expect(delta?.credentialsPatch?.base_url).toBe('https://new.example.com/v1')
  })

  it('高级编辑 Base URL 未变时 delta 不含 connections（只提交变化字段）', () => {
    const baseline = payloadFor(editForm())
    const currentForm = editForm()
    currentForm.priority += 3
    const current = payloadFor(currentForm)

    const delta = buildAccountAdvancedUpdatePatch(current, baseline, 12)

    expect(delta).toEqual({ priority: currentForm.priority, expectedConfigRevision: 12 })
    expect(delta).not.toHaveProperty('connections')
  })

  it('仅请求形态变化时 delta 携带 connections（连接域 endpointModes 随编辑同步）', () => {
    const baselineForm = editForm()
    baselineForm.supportedEndpointModes = ['chat_json', 'chat_sse']
    const baseline = payloadFor(baselineForm)
    const currentForm = editForm()
    currentForm.supportedEndpointModes = ['chat_json']
    const current = payloadFor(currentForm)

    const delta = buildAccountAdvancedUpdatePatch(current, baseline, 12)

    expect(delta?.connections).toEqual([{
      connectionPresetCode: 'gpt_openai_v1',
      baseUrl: 'https://old.example.com/v1',
      endpointModes: ['chat_json'],
      enabled: true
    }])
    expect(delta?.credentialsPatch?.supported_endpoint_modes).toEqual(['chat_json'])
  })

  it('基础编辑 Base URL 变化时 delta 携带 connections；未变时不含 connections', () => {
    const form = editForm()
    const baseline = buildAccountBasicEditSnapshot(form, { api_key: 'sk-conn-delta' })
    const changed = editForm()
    changed.baseUrl = 'https://basic-new.example.com/v1'
    const changedSnapshot = buildAccountBasicEditSnapshot(changed, { api_key: 'sk-conn-delta' })

    const changedDelta = buildAccountBasicUpdatePatch(changedSnapshot, baseline, 5)
    expect(changedDelta?.connections).toEqual([{
      connectionPresetCode: 'gpt_openai_v1',
      baseUrl: 'https://basic-new.example.com/v1',
      endpointModes: changed.supportedEndpointModes,
      enabled: true
    }])
    expect(changedDelta?.credentialsPatch?.base_url).toBe('https://basic-new.example.com/v1')

    const unchanged = editForm()
    unchanged.priority += 1
    const unchangedDelta = buildAccountBasicUpdatePatch(
      buildAccountBasicEditSnapshot(unchanged, { api_key: 'sk-conn-delta' }),
      baseline,
      5
    )
    expect(unchangedDelta).not.toHaveProperty('connections')
  })

  it('创建链形状不回归：connections 仍为单条默认连接并与健康检查 preset 一致', () => {
    const form = editForm()
    const payload = payloadFor(form)

    expect(payload.connections).toEqual([{
      connectionPresetCode: 'gpt_openai_v1',
      baseUrl: 'https://old.example.com/v1',
      endpointModes: form.supportedEndpointModes,
      enabled: true
    }])
    expect(payload.healthCheckConnectionPresetCode).toBe('gpt_openai_v1')
  })
})

// T102：编辑态供应商身份由连接 preset 反查派生，派生仍为空属 fail-closed——
// 刷新不可解，文案必须可操作；创建态保留选择引导原文案。
describe('validateAccountSaveForm 编辑态 fail-closed 文案（T102）', () => {
  function editingFormBase(): AccountFormModel {
    return defaultAccountForm('gpt', 'api_key', FALLBACK_PROVIDERS)
  }

  it('编辑态缺供应商身份：返回可操作文案而非「请先选择供应商」', () => {
    const form = editingFormBase()
    form.providerCode = ''
    form.providerProtocolProfileId = ''
    expect(validateAccountSaveForm({
      editingId: 'acc_t102',
      form,
      hasAuthSession: false,
      errorPolicyRules: [],
      responseInspectionRules: []
    })).toBe('该账户没有可编辑的供应商身份（自定义协议连接），暂不支持表单编辑：请重建账户或联系管理员')
  })

  it('编辑态缺协议档案：返回可操作文案而非「请刷新后重试」', () => {
    const form = editingFormBase()
    form.providerProtocolProfileId = ''
    expect(validateAccountSaveForm({
      editingId: 'acc_t102',
      form,
      hasAuthSession: false,
      errorPolicyRules: [],
      responseInspectionRules: []
    })).toBe('该账户的连接类型没有可用的协议档案，暂不支持表单编辑：请重建账户或联系管理员')
  })

  it('创建态文案不回归：仍为选择引导原文案', () => {
    const form = editingFormBase()
    form.providerCode = ''
    form.providerProtocolProfileId = ''
    expect(validateAccountSaveForm({
      form,
      hasAuthSession: false,
      errorPolicyRules: [],
      responseInspectionRules: []
    })).toBe('请先选择供应商')
  })
})
