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
import { mediaDomainModelAvailabilityHint } from './accountMediaFormBlocks'

// 媒体域表单契约（2026-11-11 统一）：
// - 视频/音频/图像账户与对话账户共用 AccountEditModal 的三步向导 + 编辑工作台；
// - 媒体账户没有「上游接口能力」开关，能力集恒等于该供应商该域允许集全量；
// - 本文件证明 payload/装载链路字段全量，且表单壳与域裁剪落位。
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
  code: 'gpt',
  name: 'GPT',
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
    providerCode: 'gpt',
    families: ['chat_completions', OPENAI_TTS_FAMILY, 'audio_transcription', OPENAI_VIDEO_GENERATION_FAMILY]
  })]
}

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

// ---- 媒体域默认能力集：恒等于域允许集全量（2026-11-11） ----

describe('媒体域默认能力集（无用户勾选）', () => {
  it('video/image：直接等于域允许集', () => {
    expect(defaultEndpointModesForCapabilityDomain({
      capabilityDomain: 'video',
      providerCode: 'gpt',
      profile: openAiMediaProvider.protocolProfiles[0],
      type: 'api_key'
    })).toEqual(VIDEO_GROUP)
    expect(defaultEndpointModesForCapabilityDomain({
      capabilityDomain: 'image',
      providerCode: 'gpt',
      profile: openAiMediaProvider.protocolProfiles[0],
      type: 'api_key'
    })).toEqual(['images_json'])
  })

  it('audio：语音 + 长音频任务 + 短转写 + realtime_session 全量，不再只落 audio_speech', () => {
    expect(defaultEndpointModesForCapabilityDomain({
      capabilityDomain: 'audio',
      providerCode: 'gpt',
      profile: openAiMediaProvider.protocolProfiles[0],
      type: 'api_key'
    })).toEqual([
      'audio_speech',
      'audio_job_create',
      'audio_job_get',
      'audio_job_content',
      'audio_job_cancel',
      'audio_transcription_json',
      'realtime_session'
    ])
  })
})

// ---- 创建 payload：字段全量 + 能力集全量 ----

describe('buildAccountSavePayload 媒体创建（字段全量）', () => {
  it('缺省值分支：域全量能力集 + chat 同源调度/到期/计划/余额默认', () => {
    const form = mediaForm('video', { apiKeys: ['sk-default'] })
    expect(form.supportedEndpointModes).toEqual(VIDEO_GROUP)
    const payload = buildPayload(form)
    expect(payload.capabilityDomain).toBe('video')
    expect(payload.credentials.supported_endpoint_modes).toEqual(VIDEO_GROUP)
    expect(payload.credentials).not.toHaveProperty('api_key_strategy')
    expect(payload.priority).toBe(0)
    expect(payload.superPriorityEnabled).toBe(false)
    expect(payload.fallbackEnabled).toBe(false)
    expect(payload.accountExpiresAt).toBeNull()
    expect(payload.balanceQueryEnabled).toBe(false)
    expect(payload.availabilitySchedule).toBeNull()
    expect(payload.credentials).not.toHaveProperty('error_handling_rules')
  })

  it('用户配置分支：优先级/特权、多 Key 策略权重、到期、余额、时间计划、错误策略全量携带', () => {
    const form = mediaForm('audio', {
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
    expect(payload.credentials.supported_endpoint_modes).toEqual([
      'audio_speech',
      'audio_job_create',
      'audio_job_get',
      'audio_job_content',
      'audio_job_cancel',
      'audio_transcription_json',
      'realtime_session'
    ])
    expect(payload.credentials.api_keys).toEqual(['sk-a', 'sk-b'])
    expect(payload.credentials.api_key_strategy).toBe('weighted_round_robin')
    expect(payload.credentials.api_key_weights).toEqual([3, 1])
    expect(payload.priority).toBe(5)
    expect(payload.superPriorityEnabled).toBe(true)
    expect(payload.fallbackEnabled).toBe(false)
    expect(payload.accountExpiresAt).toBe('2026-12-31T00:00:00.000Z')
    expect(payload.balanceQueryEnabled).toBe(true)
    expect(payload.balanceQueryConfig).toBeDefined()
    expect(payload.availabilitySchedule?.enabled).toBe(true)
    expect(payload.availabilitySchedule?.windows).toHaveLength(1)
    expect(Array.isArray(payload.credentials.error_handling_rules)).toBe(true)
    expect(payload.credentials.error_handling_rules).toHaveLength(1)
  })
})

// ---- 编辑 payload：显式字段差量 + CAS；能力集不再产生用户差量 ----

describe('buildAccountAdvancedUpdatePatch 媒体编辑（差量 + CAS）', () => {
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
    expect(delta?.priority).toBe(7)
    expect(delta?.fallbackEnabled).toBe(true)
    expect(delta).not.toHaveProperty('superPriorityEnabled')
    expect(delta?.accountExpiresAt).toBeTruthy()
    expect(delta?.balanceQueryEnabled).toBe(true)
    expect(delta?.availabilitySchedule).toBeDefined()
    expect(Array.isArray(delta?.credentialsPatch?.error_handling_rules)).toBe(true)
    expect(delta?.credentialsPatch?.api_key_strategy).toBe('round_robin')
    // 能力集恒全量，不因用户操作产生差量。
    expect(delta?.credentialsPatch).not.toHaveProperty('supported_endpoint_modes')
  })

  it('与基线完全一致时不产生 patch', () => {
    const baselineForm = mediaForm('image')
    const baseline = buildPayload(baselineForm)
    expect(buildAccountAdvancedUpdatePatch(baseline, baseline, 4, false)).toBeUndefined()
  })
})

// ---- 编辑装载 ----

describe('buildAccountEditFormLoad 媒体编辑装载（字段全量）', () => {
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

  it('优先级/特权/多 Key 策略权重/到期/余额/时间计划/错误策略全量装载', () => {
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
    expect(load.patch.priority).toBe(3)
    expect(load.patch.privilege).toBe('super_priority')
    expect(load.patch.concurrencyLimit).toBe(4)
    expect(load.patch.rpmLimit).toBe(0)
    expect(load.patch.supportedEndpointModes).toEqual(VIDEO_GROUP)
    expect(load.patch.apiKeys).toEqual(['vk-1', 'vk-2'])
    expect(load.patch.apiKeyStrategy).toBe('weighted_round_robin')
    expect(load.patch.apiKeyWeights).toEqual([2, 1])
    expect(load.patch.accountExpiresAt).toBeDefined()
    expect(load.patch.accountExpiresAt?.year()).toBe(2026)
    expect(load.patch.balanceQueryEnabled).toBe(true)
    expect(load.patch.balanceQueryIntervalMinutes).toBe(10)
    expect(load.patch.availabilitySchedule.enabled).toBe(true)
    expect(load.patch.availabilitySchedule.windows).toHaveLength(1)
    expect(load.errorPolicyRules).toHaveLength(1)
    expect(load.errorPolicyRules[0].action).toBe('temp_unschedulable')
  })
})

// ---- 表单壳渲染契约（源码级断言，UsageSummaryTags.test.ts 同惯例） ----

describe('AccountEditModal.vue 媒体域渲染契约（源码级）', () => {
  const modalSource = readFileSync(path.resolve(process.cwd(), 'src/views/accounts/AccountEditModal.vue'), 'utf8')
  const templateSource = modalSource.slice(modalSource.indexOf('<template>'), modalSource.lastIndexOf('</template>'))

  it('媒体域与对话域共用同一向导/工作台：不再存在独立媒体表单组件', () => {
    expect(modalSource).not.toContain('AccountMediaForm')
    expect(templateSource).toContain('wizard-steps')
    expect(templateSource).toContain('bench-section-name">常用配置')
    expect(templateSource).toContain('bench-section-name">高级配置')
    // 新建三步向导的下一步/上一步对媒体同样渲染。
    expect(templateSource).toContain('<template v-if="!editing">')
  })

  it('媒体账户没有上游接口能力勾选块', () => {
    expect(templateSource).not.toContain('上游接口能力</span>')
    expect(templateSource).not.toContain('mediaEndpointModeOptions')
    expect(templateSource).not.toContain('capabilityModesDisabled')
  })

  it('媒体凭据区：支持模型与火山语音双值字段由域条件分支承接', () => {
    expect(templateSource).toContain('<AccountMediaModelField')
    expect(templateSource).toContain('v-if="isMediaDomain && isApiKeyForm && !authorizedEditing"')
    expect(templateSource).toContain('<AccountMediaSpeechCredentials')
    expect(templateSource).toContain('v-if="isMediaDomain && isVolcengineAudio && !authorizedEditing"')
    expect(modalSource).toContain('applyMediaCreateDefaults(props.form')
  })

  it('高级折叠卡按域裁剪：对话专属卡不渲染，image 才有后台探测卡', () => {
    expect(templateSource).toContain('v-if="!isMediaDomain"')
    expect(templateSource).toContain('v-if="isMediaDomain && capabilityDomain === \'image\'"')
    expect(templateSource).toContain('name="后台自动探测"')
    // 媒体域错误卡只保留错误处理策略，不渲染响应检查。
    expect(templateSource).toContain(':name="isMediaDomain ? \'错误处理策略\' : \'错误与响应规则\'"')
    expect(templateSource).toContain('v-if="!isMediaDomain"\n                        v-model:rules="responseInspectionRules"')
  })

  it('授权实例只读态：媒体域不展示上游接口能力条目', () => {
    expect(modalSource).toContain('...(isMediaDomain.value')
    expect(modalSource).toContain("'supported_endpoint_modes'")
  })
})

describe('mediaDomainModelAvailabilityHint：域内空目录模型提示（v8-D）', () => {
  const base = {
    capabilityDomain: 'video' as AccountCapabilityDomain,
    optionCount: 0,
    loading: false,
    settled: true
  }

  it('候选加载后确认为空（settled 且非加载中）时提示；有候选或加载中不提示', () => {
    expect(mediaDomainModelAvailabilityHint(base)).toContain('暂无可用目录模型')
    expect(mediaDomainModelAvailabilityHint({ ...base, optionCount: 2 })).toBeUndefined()
    expect(mediaDomainModelAvailabilityHint({ ...base, loading: true })).toBeUndefined()
  })

  it('懒加载下未加载的空不是真没有货：settled 前不提示', () => {
    expect(mediaDomainModelAvailabilityHint({ ...base, settled: false })).toBeUndefined()
  })

  it('对话域不提示', () => {
    expect(mediaDomainModelAvailabilityHint({ ...base, capabilityDomain: 'chat' })).toBeUndefined()
  })
})
