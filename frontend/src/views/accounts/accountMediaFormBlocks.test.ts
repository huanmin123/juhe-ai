import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'
import dayjs from 'dayjs'

import { OPENAI_TTS_FAMILY, OPENAI_VIDEO_GENERATION_FAMILY, VOLCENGINE_OPENAI_V1_PROFILE_ID } from '@/shared/providerProtocol'
import type {
  AccountAdvancedDetail,
  AccountEditBasicDetail,
  AccountSupportedEndpointMode,
  ProviderDefinition,
  ProviderProtocolProfileDefinition
} from '@/types/domain'
import { defaultAccountForm } from './accountFormDefaults'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { defaultEndpointModesForCapabilityDomain } from './accountCapabilityDomains'
import { buildAccountSavePayload } from './accountSavePayload'
import { buildAccountAdvancedUpdatePatch } from './accountEditPatch'
import { buildAccountEditFormLoad } from './accountEditFormLoaders'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import {
  mediaAdvancedCardConfiguredCount,
  mediaAdvancedCardSummary,
  mediaCapabilityModeOptions,
  mediaDomainModelAvailabilityHint
} from './accountMediaFormBlocks'

// v8-B 媒体域表单完整性升级（A 组）契约：
// docs/plans/计划-20261011T141004000Z-AI账户能力域拆分.md §媒体域表单完整性升级（v8-B）。
// 媒体创建请求与对话走同一条派发循环，A 组七项配置对媒体真实生效；
// 本文件证明 payload/装载链路七项全量，且媒体表单渲染契约落位。
// 注：vitest 配置未接入 @vitejs/plugin-vue（依赖也无 @vue/test-utils），
// .vue 文件无法在单测中导入挂载，组件渲染契约按项目回归脚本惯例做源码级断言。

const VIDEO_GROUP: AccountSupportedEndpointMode[] = ['video_create', 'video_get', 'video_content', 'video_cancel']

function mediaProfile(input: {
  id: string
  providerCode: string
  families: string[]
}): ProviderProtocolProfileDefinition {
  return {
    id: input.id,
    providerCode: input.providerCode,
    name: `${input.providerCode} OpenAI v1`,
    enabled: true,
    protocolCode: 'openai',
    protocolVersion: 'v1',
    baseUrl: `https://${input.providerCode}.example/v1`,
    defaultHealthCheckModel: '',
    accountTypes: ['api_key'],
    capabilities: [],
    endpointFamilies: input.families.map((code) => ({ code, name: code }))
  }
}

const openAiMediaProvider: ProviderDefinition = {
  id: 'provider-openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  defaultProtocolProfileId: 'openai-v1',
  protocolCode: 'openai',
  protocolVersion: 'v1',
  baseUrl: 'https://api.openai.com/v1',
  defaultHealthCheckModel: '',
  defaultSupportedModels: [],
  accountTypes: ['api_key'],
  capabilities: [],
  protocolProfiles: [mediaProfile({
    id: 'openai-v1',
    providerCode: 'openai',
    // openai 族通用词表：chat/tts/转写任务族由 families 推导，短转写与
    // realtime_session 为显式可选能力（v8-A 音频域 lane）。
    families: ['chat_completions', OPENAI_TTS_FAMILY, 'audio_transcription', OPENAI_VIDEO_GENERATION_FAMILY]
  })]
}

function mediaForm(capabilityDomain: AccountCapabilityDomain, patch: Partial<AccountFormModel> = {}): AccountFormModel {
  const form = defaultAccountForm('openai', 'api_key')
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

// ---- 上游接口能力域内勾选（A组①）：v8-A 音频域允许集 + 供应商词表求交 ----

describe('mediaCapabilityModeOptions（域允许集 × 供应商词表）', () => {
  it('openai 音频域：audio_speech + 短转写 + realtime_session（v8-A 移入项可勾选）', () => {
    const options = mediaCapabilityModeOptions({
      capabilityDomain: 'audio',
      providerCode: 'openai',
      profile: openAiMediaProvider.protocolProfiles[0],
      accountType: 'api_key'
    })
    expect(options.map((option) => option.value)).toEqual([
      'audio_speech',
      'audio_transcription_json',
      'realtime_session'
    ])
  })

  it('openai 视频域：视频四值组；图像域：images_json', () => {
    const videoOptions = mediaCapabilityModeOptions({
      capabilityDomain: 'video',
      providerCode: 'openai',
      profile: openAiMediaProvider.protocolProfiles[0],
      accountType: 'api_key'
    })
    expect(videoOptions.map((option) => option.value)).toEqual(VIDEO_GROUP)
    const imageOptions = mediaCapabilityModeOptions({
      capabilityDomain: 'image',
      providerCode: 'openai',
      profile: openAiMediaProvider.protocolProfiles[0],
      accountType: 'api_key'
    })
    expect(imageOptions.map((option) => option.value)).toEqual(['images_json'])
  })
})

// ---- 高级配置折叠卡头部摘要（A组④-⑦） ----

describe('mediaAdvancedCardSummary / mediaAdvancedCardConfiguredCount', () => {
  const emptyState = {
    accountExpiresAt: undefined as AccountFormModel['accountExpiresAt'],
    balanceQueryEnabled: false,
    scheduleEnabled: false,
    scheduleWindowCount: 0,
    errorPolicyRuleCount: 0
  }

  it('全默认：摘要标注未设置到期/关/未启用/0 条，计数 0', () => {
    expect(mediaAdvancedCardSummary(emptyState)).toBe('未设置到期 · 余额查询关 · 计划未启用 · 错误规则 0 条')
    expect(mediaAdvancedCardConfiguredCount(emptyState)).toBe(0)
  })

  it('已配置项计数与到期摘要', () => {
    const state = {
      accountExpiresAt: dayjs('2026-12-31T00:00:00Z'),
      balanceQueryEnabled: true,
      scheduleEnabled: true,
      scheduleWindowCount: 2,
      errorPolicyRuleCount: 3
    }
    const summary = mediaAdvancedCardSummary(state)
    expect(summary).toContain('到期 ')
    expect(summary).not.toContain('时间格式异常')
    expect(summary).toContain('2026')
    expect(summary).toContain('08:00') // 本地时区渲染后的时分（UTC+8 为 08:00）
    expect(summary).toContain('余额查询开')
    expect(summary).toContain('计划已启用 2 个时段')
    expect(summary).toContain('错误规则 3 条')
    expect(mediaAdvancedCardConfiguredCount(state)).toBe(4)
  })
})

// ---- A组①②③④⑤⑥⑦：创建 payload 七项全量（缺省值与 chat 同源默认） ----

describe('buildAccountSavePayload 媒体创建（七项全量）', () => {
  it('缺省值分支：域默认集 + chat 同源调度/到期/计划/余额默认', () => {
    const form = mediaForm('video', { apiKeys: ['sk-default'] })
    expect(form.supportedEndpointModes).toEqual(VIDEO_GROUP)
    const payload = buildPayload(form)
    // ① 上游接口能力（域默认集）
    expect(payload.capabilityDomain).toBe('video')
    expect(payload.credentials.supported_endpoint_modes).toEqual(VIDEO_GROUP)
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

  it('用户配置分支：勾选收窄、优先级/特权、多 Key 策略权重、到期、余额、时间计划、错误策略全量携带', () => {
    const form = mediaForm('audio', {
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
    expect(payload.credentials.supported_endpoint_modes).toEqual(['audio_speech', 'realtime_session'])
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

describe('buildAccountAdvancedUpdatePatch 媒体编辑（七项差量 + CAS）', () => {
  it('变更字段进差量、未变更字段省略、expectedConfigRevision 原样携带', () => {
    const baselineForm = mediaForm('video')
    const baseline = buildPayload(baselineForm)
    const currentForm = mediaForm('video', {
      supportedEndpointModes: [...VIDEO_GROUP],
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
    // ①（modes 未变则不出现在凭据差量）
    expect(delta?.credentialsPatch).not.toHaveProperty('supported_endpoint_modes')
  })

  it('modes 勾选变化经凭据差量提交完整声明集（D6）', () => {
    const baselineForm = mediaForm('audio')
    baselineForm.supportedEndpointModes = ['audio_speech', 'audio_transcription_json']
    const baseline = buildPayload(baselineForm)
    const currentForm = mediaForm('audio', { supportedEndpointModes: ['audio_speech'] })
    const current = buildPayload(currentForm)
    const delta = buildAccountAdvancedUpdatePatch(current, baseline, 3, false)
    expect(delta?.credentialsPatch?.supported_endpoint_modes).toEqual(['audio_speech'])
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
      balanceQueryEnabled: true,
      balanceQueryConfig: { adapter: 'builtin', intervalMinutes: 10 },
      effectiveErrorHandlingRules: [],
      expeditedRecoveryEnabled: false,
      expeditedOverLimit: false
    }
    return { account, advanced }
  }

  it('优先级/特权/能力 modes/多 Key 策略权重/到期/余额/时间计划/错误策略全量装载', () => {
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
      defaults: defaultAccountForm('volcengine', 'api_key')
    })
    // ③ 优先级/特权
    expect(load.patch.priority).toBe(3)
    expect(load.patch.privilege).toBe('super_priority')
    expect(load.patch.concurrencyLimit).toBe(4)
    // ① 能力 modes（编辑装载不裁剪）
    expect(load.patch.supportedEndpointModes).toEqual(VIDEO_GROUP)
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

// ---- 组件渲染契约（源码级断言，UsageSummaryTags.test.ts 同惯例） ----

describe('AccountMediaForm.vue v8-B 渲染契约（源码级）', () => {
  const componentSource = readFileSync(path.resolve(process.cwd(), 'src/views/accounts/AccountMediaForm.vue'), 'utf8')
  const templateSource = componentSource.slice(componentSource.indexOf('<template>'), componentSource.lastIndexOf('</template>'))

  it('段2：上游接口能力域内勾选块位于支持模型之后，勾选绑定域允许集选项', () => {
    const supportedModelsIndex = templateSource.indexOf('支持模型</span>')
    const capabilityIndex = templateSource.indexOf('上游接口能力</span>')
    expect(capabilityIndex, '段2 缺少上游接口能力勾选块').toBeGreaterThan(supportedModelsIndex)
    expect(templateSource).toContain('v-for="option in mediaEndpointModeOptions"')
    expect(templateSource).toContain(':checked="form.supportedEndpointModes.includes(option.value)"')
    expect(templateSource).toContain(':disabled="capabilityModesDisabled"')
  })

  it('段3：优先级与特权控件位于并发上限旁，绑定 chat 同源表单字段', () => {
    const concurrencyIndex = templateSource.indexOf('label="并发上限"')
    const priorityIndex = templateSource.indexOf('label="优先级"')
    const privilegeIndex = templateSource.indexOf('label="特权"')
    expect(priorityIndex, '段3 缺少优先级字段').toBeGreaterThan(concurrencyIndex)
    expect(privilegeIndex, '段3 缺少特权字段').toBeGreaterThan(priorityIndex)
    expect(templateSource).toContain('v-model:value="form.priority"')
    expect(templateSource).toContain('v-model:value="form.privilege"')
    expect(templateSource).toContain('<a-select-option value="super_priority">超级优先</a-select-option>')
    expect(templateSource).toContain('<a-select-option value="fallback">降级备用</a-select-option>')
  })

  it('段3 末尾：高级配置折叠卡复用 AccountAdvancedCard 壳，四块齐备', () => {
    const attributionIndex = templateSource.indexOf('media-attribution-grid')
    const advancedCardIndex = templateSource.indexOf('<AccountAdvancedCard')
    expect(advancedCardIndex, '高级配置折叠卡缺失').toBeGreaterThan(attributionIndex)
    expect(templateSource).toContain('name="高级配置"')
    expect(templateSource).toContain('<AccountExtraInfoSection') // ④ 到期时间
    expect(templateSource).toContain('<AccountBalanceQuerySection') // ⑤ 余额查询
    expect(templateSource).toContain('<AccountAvailabilityScheduleSection') // ⑥ 时间计划
    expect(templateSource).toContain('<AccountErrorPolicyCard') // ⑦ 错误处理策略
    // 授权实例只读化：四块统一 authorizedEditing 只读
    const advancedBody = templateSource.slice(advancedCardIndex)
    expect(advancedBody.match(/:readonly="authorizedEditing"/g)?.length).toBeGreaterThanOrEqual(4)
    // 编辑态高级详情加载分支（chat 同规）
    expect(advancedBody).toContain('v-else-if="advancedPending"')
    expect(advancedBody).toContain('@click="$emit(\'advanced-open\')"')
  })

  it('C 组四项（请求覆盖/响应检查/周期探活/锁死）不渲染', () => {
    expect(templateSource).not.toContain('AccountGptRequestOverridesSection')
    expect(templateSource).not.toContain('AccountResponseInspectionPolicyCard')
    expect(templateSource).not.toContain('temporaryUnavailableContinuousProbeEnabled')
    expect(templateSource).not.toContain('lockDeathTimeoutSeconds')
    // chat 稳定性卡的功能标记类名（探活开关行/锁死配置块）不得出现在媒体表单
    expect(templateSource).not.toContain('probe-toggle')
    expect(templateSource).not.toContain('lock-config-fields')
  })

  it('授权实例只读态：能力勾选与调度字段沿用 authorizedEditing 门控', () => {
    expect(componentSource).toContain('capabilityModesDisabled = computed(() => props.authorizedEditing || props.form.type === \'google_oauth\')')
    expect(componentSource.match(/:disabled="authorizedEditing"/g)?.length).toBeGreaterThanOrEqual(5)
  })
})

describe('AccountEditModal.vue 媒体分支接线契约（源码级）', () => {
  const modalSource = readFileSync(path.resolve(process.cwd(), 'src/views/accounts/AccountEditModal.vue'), 'utf8')
  const mediaBranch = modalSource.slice(
    modalSource.indexOf('<AccountMediaForm'),
    modalSource.indexOf('/>', modalSource.indexOf('<AccountMediaForm'))
  )

  it('高级详情/余额/错误策略 props 与事件透传齐备（chat 同一来源）', () => {
    expect(mediaBranch).toContain(':advanced-loaded="advancedLoaded"')
    expect(mediaBranch).toContain(':advanced-loading="advancedLoading"')
    expect(mediaBranch).toContain(':balance-query-can-run="balanceQueryCanRun"')
    expect(mediaBranch).toContain(':balance-query-loading="balanceQueryLoading"')
    expect(mediaBranch).toContain('v-model:error-policy-rules="errorPolicyRules"')
    expect(mediaBranch).toContain(':inherited-error-policy-rules="inheritedErrorPolicyRules"')
    expect(mediaBranch).toContain('@advanced-open="$emit(\'advanced-open\')"')
    expect(mediaBranch).toContain('@balance-query="$emit(\'balance-query\')"')
  })
})

describe('mediaDomainModelAvailabilityHint：域内空目录模型提示（v8-D）', () => {
  const base = {
    capabilityDomain: 'video' as AccountCapabilityDomain,
    optionCount: 0,
    loading: false,
    settled: true,
    hybridMappingDomain: false
  }

  it('候选加载后确认为空（settled 且非加载中）时提示；有候选或加载中不提示', () => {
    expect(mediaDomainModelAvailabilityHint(base)).toContain('暂无可用目录模型')
    expect(mediaDomainModelAvailabilityHint({ ...base, optionCount: 2 })).toBeUndefined()
    expect(mediaDomainModelAvailabilityHint({ ...base, loading: true })).toBeUndefined()
  })

  it('懒加载下未加载的空不是真没有货：settled 前不提示', () => {
    expect(mediaDomainModelAvailabilityHint({ ...base, settled: false })).toBeUndefined()
  })

  it('hybrid 映射域（上游模型来自映射面）与对话域不提示', () => {
    expect(mediaDomainModelAvailabilityHint({ ...base, hybridMappingDomain: true })).toBeUndefined()
    expect(mediaDomainModelAvailabilityHint({ ...base, capabilityDomain: 'chat' })).toBeUndefined()
  })
})
