import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'
import dayjs from 'dayjs'
import { VOLCENGINE_OPENAI_V1_PROFILE_ID } from '@/shared/providerProtocol'
import type {
  AccountAdvancedDetail,
  AccountEditBasicDetail,
  AccountSupportedEndpointMode
} from '@/types/domain'
import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { defaultEndpointModesForCapabilityDomain } from './accountCapabilityDomains'
import { buildAccountSavePayload } from './accountSavePayload'
import { buildAccountAdvancedUpdatePatch } from './accountEditPatch'
import { buildAccountEditFormLoad } from './accountEditFormLoaders'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'

// v8-B 媒体域表单完整性升级（A 组）契约：
// docs/plans/计划-20261011T141004000Z-AI账户能力域拆分.md §媒体域表单完整性升级（v8-B）。
// 媒体创建请求与对话走同一条派发循环，A 组七项配置对媒体真实生效；
// 本文件证明 payload/装载链路七项全量，且媒体表单渲染契约落位。
// 注：vitest 配置未接入 @vitejs/plugin-vue（依赖也无 @vue/test-utils），
// .vue 文件无法在单测中导入挂载，组件渲染契约按项目回归脚本惯例做源码级断言。

const VIDEO_GROUP: AccountSupportedEndpointMode[] = ['video_create', 'video_get', 'video_content', 'video_cancel']

// 五域退役（AI账户上游接口能力设计 §3）：媒体表单不持有声明集，域默认集恒为
// 空集；本 helper 保留 defaultEndpointModesForCapabilityDomain 调用以钉住
// 「五域默认集恒空」契约。
function mediaForm(capabilityDomain: AccountCapabilityDomain, patch: Partial<AccountFormModel> = {}): AccountFormModel {
  const form = defaultAccountForm('gpt', 'api_key')
  form.capabilityDomain = capabilityDomain
  form.supportedEndpointModes = [...defaultEndpointModesForCapabilityDomain({
    capabilityDomain,
    providerCode: form.providerCode,
    type: form.type
  })]
  Object.assign(form, patch)
  return form
}

function buildPayload(form: AccountFormModel, errorPolicyRules: AccountErrorPolicyRuleForm[] = []) {
  return buildAccountSavePayload({
    accounts: [],
    form,
    errorPolicyRules,
    responseInspectionRules: []
  })
}

const rateLimitedRule: AccountErrorPolicyRuleForm = {
  enabled: true,
  name: '429 临时限流',
  priority: 1,
  status_codes: '429',
  error_codes: '',
  error_types: '',
  keywords: '',
  action: 'rate_limited',
  reset_strategy: 'duration',
  duration_hours: 1,
  daily_reset_hour: null,
  weekly_reset_day: null,
  weekly_reset_hour: null,
  description: ''
}

// ---- A组①②③④⑤⑥⑦：创建 payload 七项全量（缺省值与 chat 同源默认） ----

describe('buildAccountSavePayload 媒体创建（五域退役：payload 不携带声明集）', () => {
  it('缺省值分支：凭据不携带 supported_endpoint_modes，调度/到期/计划/余额默认与 chat 同源', () => {
    const form = mediaForm('video', { apiKeys: ['sk-default'] })
    expect(form.supportedEndpointModes).toEqual([])
    const payload = buildPayload(form)
    // ① 上游接口能力：五域凭据归一化不落该键（设计 §3）
    expect(payload.capabilityDomain).toBe('video')
    expect(payload.credentials).not.toHaveProperty('supported_endpoint_modes')
    // 连接载荷同样不携带 endpointModes（连接行不写 account_endpoint_modes）。
    expect(payload.connections?.[0]).not.toHaveProperty('endpointModes')
    // ② 多 Key 策略缺省不产生池键
    expect(payload.credentials).not.toHaveProperty('api_key_strategy')
    // ③ 优先级/超级优先/降级备用（chat 同源默认）
    expect(payload.priority).toBe(0)
    expect(payload.superPriorityEnabled).toBe(false)
    expect(payload.fallbackEnabled).toBe(false)
    // ④ 到期时间
    expect(payload.accountExpiresAt).toBeNull()
    // ⑤ 余额查询（api_key 型至少携带开关默认）
    expect(payload.balanceQueryEnabled).toBe(false)
    // ⑥ 时间计划（默认禁用 → null）
    expect(payload.availabilitySchedule).toBeNull()
    // ⑦ 错误处理策略：默认无专属规则时凭据不携带 error_handling_rules
    expect(payload.credentials).not.toHaveProperty('error_handling_rules')
  })

  it('用户配置分支：表单残留声明集不进 payload，优先级/特权、多 Key 策略权重、到期、余额、时间计划、错误策略全量携带', () => {
    const form = mediaForm('audio', {
      // 五域表单不持有声明集；此处显式注入残留值，钉住「payload 恒剥离」语义。
      supportedEndpointModes: ['audio_speech', 'realtime_session'],
      priority: 5,
      privilege: 'super_priority',
      accountExpiresAt: dayjs('2026-12-31T00:00:00Z'),
      apiKeys: ['sk-a', 'sk-b'],
      apiKeyStrategy: 'weighted_round_robin',
      apiKeyWeights: [3, 1],
      balanceQueryEnabled: true,
      balanceQueryAdapter: 'builtin',
      balanceQueryIntervalMinutes: 10
    })
    form.availabilitySchedule = {
      ...form.availabilitySchedule,
      enabled: true,
      windows: [{ key: 'account_schedule_window-1', daysOfWeek: [1, 2, 3, 4, 5], start: '09:00', end: '18:00' }]
    }
    const payload = buildPayload(form, [rateLimitedRule])
    // ①
    expect(payload.credentials).not.toHaveProperty('supported_endpoint_modes')
    // ②
    expect(payload.credentials.api_keys).toEqual(['sk-a', 'sk-b'])
    expect(payload.credentials.api_key_strategy).toBe('weighted_round_robin')
    expect(payload.credentials.api_key_weights).toEqual([3, 1])
    // ③
    expect(payload.priority).toBe(5)
    expect(payload.superPriorityEnabled).toBe(true)
    expect(payload.fallbackEnabled).toBe(false)
    // ④
    expect(payload.accountExpiresAt).toBe('2026-12-31T00:00:00.000Z')
    // ⑤
    expect(payload.balanceQueryEnabled).toBe(true)
    expect(payload.balanceQueryConfig).toBeDefined()
    // ⑥
    expect(payload.availabilitySchedule?.enabled).toBe(true)
    expect(payload.availabilitySchedule?.windows).toHaveLength(1)
    // ⑦
    expect(Array.isArray(payload.credentials.error_handling_rules)).toBe(true)
    expect(payload.credentials.error_handling_rules).toHaveLength(1)
  })
})

// ---- A组①-⑦：编辑 payload 显式字段差量 + CAS 语义不变 ----

describe('buildAccountAdvancedUpdatePatch 媒体编辑（五域退役：声明集不进差量）', () => {
  it('变更字段进差量、未变更字段省略、expectedConfigRevision 原样携带', () => {
    const baselineForm = mediaForm('video')
    const baseline = buildPayload(baselineForm)
    const currentForm = mediaForm('video', {
      priority: 7,
      privilege: 'fallback',
      accountExpiresAt: dayjs('2027-03-01T12:00:00'),
      balanceQueryEnabled: true,
      balanceQueryAdapter: 'builtin',
      balanceQueryIntervalMinutes: 5,
      apiKeys: ['sk-x', 'sk-y'],
      apiKeyStrategy: 'round_robin'
    })
    currentForm.availabilitySchedule = {
      ...currentForm.availabilitySchedule,
      enabled: true,
      windows: [{ key: 'account_schedule_window-1', daysOfWeek: [6], start: '10:00', end: '12:00' }]
    }
    const current = buildPayload(currentForm, [rateLimitedRule])
    const delta = buildAccountAdvancedUpdatePatch(current, baseline, 9, false)
    expect(delta).toBeDefined()
    expect(delta?.expectedConfigRevision).toBe(9)
    // ③
    expect(delta?.priority).toBe(7)
    expect(delta?.fallbackEnabled).toBe(true)
    expect(delta).not.toHaveProperty('superPriorityEnabled')
    // ④
    expect(delta?.accountExpiresAt).toBeTruthy()
    // ⑤
    expect(delta?.balanceQueryEnabled).toBe(true)
    // ⑥
    expect(delta?.availabilitySchedule).toBeDefined()
    // ⑦（凭据面差量）
    expect(Array.isArray(delta?.credentialsPatch?.error_handling_rules)).toBe(true)
    // ②（凭据面差量：策略）
    expect(delta?.credentialsPatch?.api_key_strategy).toBe('round_robin')
    // ①（声明集已退役，凭据差量恒不含 supported_endpoint_modes）
    expect(delta?.credentialsPatch).not.toHaveProperty('supported_endpoint_modes')
  })

  it('表单残留声明集变化不进凭据差量（五域 payload 恒剥离声明集，D6 退役）', () => {
    const baselineForm = mediaForm('audio')
    baselineForm.supportedEndpointModes = ['audio_speech', 'audio_transcription_json']
    const baseline = buildPayload(baselineForm)
    const currentForm = mediaForm('audio', { supportedEndpointModes: ['audio_speech'] })
    const current = buildPayload(currentForm)
    const delta = buildAccountAdvancedUpdatePatch(current, baseline, 3, false)
    // 声明集恒剥离后残留差异在 payload 层不存在：声明集变化连差量都不产生
    //（delta 为 null 即最强形态），凭据差量自然不含 supported_endpoint_modes。
    expect(delta?.credentialsPatch ?? {}).not.toHaveProperty('supported_endpoint_modes')
  })

  it('与基线完全一致时不产生 patch（未检测到修改语义不变）', () => {
    const baselineForm = mediaForm('image')
    const baseline = buildPayload(baselineForm)
    expect(buildAccountAdvancedUpdatePatch(baseline, baseline, 4, false)).toBeUndefined()
  })
})

// ---- A组①-⑦：编辑装载（applyLoadedAccountDetailToEditForm 的装载核心） ----

describe('buildAccountEditFormLoad 媒体编辑装载（七项落表单）', () => {
  function mediaEditAccount(input: { credentials: Record<string, unknown> }): { account: AccountEditBasicDetail; advanced: AccountAdvancedDetail } {
    const account: AccountEditBasicDetail = {
      id: 'acct-media-1',
      configRevision: 5,
      ownerSystemAccountId: 'sys-1',
      providerCode: 'volcengine',
      providerProtocolProfileId: VOLCENGINE_OPENAI_V1_PROFILE_ID,
      protocolCode: 'openai',
      protocolVersion: 'v1',
      name: 'volcengine 媒体户',
      type: 'api_key',
      credentials: input.credentials,
      status: 'active',
      concurrencyLimit: 4,
      rpmLimit: 0,
      priority: 3,
      superPriorityEnabled: true,
      fallbackEnabled: false,
      clientCompatibility: 'openai_standard',
      supportedModels: ['doubao-seedance-1-0-lite-t2v-250428'],
      tags: [],
      healthCheckModel: 'doubao-seedance-1-0-lite-t2v-250428',
      healthCheckEndpointMode: 'video_create',
      boundGroupId: 'group-1',
      boundGroupName: '视频组'
    }
    const advanced: AccountAdvancedDetail = {
      id: 'acct-media-1',
      configRevision: 5,
      accessType: 'owner',
      credentials: input.credentials,
      modelMappings: [],
      proxyProfileId: undefined,
      availabilitySchedule: {
        enabled: true,
        timezone: 'Asia/Shanghai',
        mode: 'allow_windows',
        windows: [{ daysOfWeek: [1, 2, 3, 4, 5], start: '09:00', end: '18:00' }]
      },
      accountExpiresAt: '2026-12-31T00:00:00Z',
      temporaryUnavailableContinuousProbeEnabled: true,
      backgroundProbeEnabled: true,
      balanceQueryEnabled: true,
      balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10 },
      effectiveErrorHandlingRules: [],
      expeditedRecoveryEnabled: false,
      expeditedOverLimit: false
    }
    return { account, advanced }
  }

  it('优先级/特权/多 Key 策略权重/到期/余额/时间计划/错误策略全量装载；声明集不进表单（五域退役）', () => {
    const credentials = {
      api_key: 'vk-1',
      api_keys: ['vk-1', 'vk-2'],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [2, 1],
      base_url: 'https://ark.cn-beijing.volces.com/api/v3',
      supported_endpoint_modes: VIDEO_GROUP,
      error_handling_rules: [{
        enabled: true,
        name: '5xx 临时避让',
        priority: 2,
        status_codes: [500, 502],
        action: 'temp_unschedulable',
        reset_strategy: 'duration',
        duration_hours: 2
      }]
    }
    const { account, advanced } = mediaEditAccount({ credentials })
    const load = buildAccountEditFormLoad({
      account,
      advanced,
      credentials: { ...account.credentials, ...advanced.credentials },
      defaults: defaultAccountForm('volcengine', 'api_key'),
      // 五域退役：按路由域裁剪——凭据残留键（revision 30 前存量）不读入表单。
      capabilityDomain: 'video'
    })
    // ③ 优先级/特权
    expect(load.patch.priority).toBe(3)
    expect(load.patch.privilege).toBe('super_priority')
    expect(load.patch.concurrencyLimit).toBe(4)
    expect(load.patch.rpmLimit).toBe(0)
    // ① 声明集不装载（设计 §3：五域表单不持有「上游接口能力」）
    expect(load.patch.supportedEndpointModes).toEqual([])
    // ② 多 Key 策略与权重
    expect(load.patch.apiKeys).toEqual(['vk-1', 'vk-2'])
    expect(load.patch.apiKeyStrategy).toBe('weighted_round_robin')
    expect(load.patch.apiKeyWeights).toEqual([2, 1])
    // ④ 到期时间
    expect(load.patch.accountExpiresAt).toBeDefined()
    expect(load.patch.accountExpiresAt?.year()).toBe(2026)
    // ⑤ 余额查询
    expect(load.patch.balanceQueryEnabled).toBe(true)
    expect(load.patch.balanceQueryIntervalMinutes).toBe(10)
    // ⑥ 时间计划
    expect(load.patch.availabilitySchedule.enabled).toBe(true)
    expect(load.patch.availabilitySchedule.windows).toHaveLength(1)
    // ⑦ 错误处理策略
    expect(load.errorPolicyRules).toHaveLength(1)
    expect(load.errorPolicyRules[0].action).toBe('temp_unschedulable')
  })
})

// ---- 统一账户表单渲染契约（源码级断言，UsageSummaryTags.test.ts 同惯例） ----

describe('AccountEditModal.vue 统一三步向导（源码级）', () => {
  const modalSource = readFileSync(path.resolve(process.cwd(), 'src/views/accounts/AccountEditModal.vue'), 'utf8')
  const templateSource = modalSource.slice(modalSource.indexOf('<template>'), modalSource.lastIndexOf('</template>'))

  it('媒体域不再有独立表单分支，所有域共用 AccountFormSelector', () => {
    expect(modalSource).not.toContain('<AccountMediaForm')
    expect(modalSource).not.toContain('AccountMediaForm.vue')
    expect(templateSource).toContain('<AccountFormSelector')
    expect(templateSource).toContain('wizardProviders')
    expect(templateSource).toContain('wizardAccountTypeChoices')
  })

  it('支持模型、模型映射、优先级、时间计划、余额与错误策略对媒体域开放', () => {
    expect(templateSource).toContain('<AccountStrategySection')
    expect(templateSource).toContain('<AccountExtraInfoSection')
    expect(templateSource).toContain('<AccountBalanceQuerySection')
    expect(templateSource).toContain('<AccountAvailabilityScheduleSection')
    expect(templateSource).toContain('<AccountErrorPolicyCard')
    expect(templateSource).toContain('label="优先级"')
    expect(templateSource).toContain('label="并发上限"')
    expect(templateSource).toContain('label="每分钟请求上限"')
  })

  it('仅对话专属项按域隐藏：请求覆盖、响应检查、持续恢复探活、账户锁死', () => {
    expect(templateSource).toContain('v-if="!isMediaDomain"')
    expect(templateSource.match(/<AccountGptRequestOverridesSection\s+v-if="!isMediaDomain"/g)).toHaveLength(2)
    expect(templateSource).toMatch(/<AccountResponseInspectionPolicyCard\s+v-if="!isMediaDomain"/)
    expect(templateSource).toContain('v-if="!isMediaDomain" class="form-section probe-toggle-row"')
    expect(templateSource).toContain('v-if="!isMediaDomain" class="form-section lock-config-row"')
  })

  it('媒体域创建默认值仍由统一向导 watcher 重放', () => {
    expect(modalSource).toContain('applyMediaCreateDefaults(props.form')
    expect(modalSource).toContain('capabilityDomainIsMedia(domain)')
  })
})
