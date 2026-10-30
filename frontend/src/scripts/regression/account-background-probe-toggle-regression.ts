import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { AccountListItem } from '@/types/domain'
import { defaultAccountForm } from '../../views/accounts/accountFormDefaults'
import { buildAccountSavePayload, buildAccountUpdatePayload, validateAccountSaveForm } from '../../views/accounts/accountSavePayload'
import {
  accountManualModeTag,
  accountManualModeTooltipText,
  accountStatusColor,
  accountStatusText,
  accountStatusTooltipLines
} from '../../views/accounts/accountFormatters'
import { FALLBACK_PROVIDERS } from '../../views/accounts/accountOptions'

const modalSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountEditModal.vue', import.meta.url)), 'utf8')
const statusTagSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountStatusTag.vue', import.meta.url)), 'utf8')
const healthViewSource = readFileSync(fileURLToPath(new URL('../../views/ai-health/AiHealthView.vue', import.meta.url)), 'utf8')
const healthTypesSource = readFileSync(fileURLToPath(new URL('../../types/domain/usage-stats.ts', import.meta.url)), 'utf8')
const menuActionsSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountMenuActions.ts', import.meta.url)), 'utf8')
const mediaFormSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountMediaForm.vue', import.meta.url)), 'utf8')
const balanceSectionSource = readFileSync(fileURLToPath(new URL('../../views/accounts/AccountBalanceQuerySection.vue', import.meta.url)), 'utf8')
const payloadSource = readFileSync(fileURLToPath(new URL('../../views/accounts/accountSavePayload.ts', import.meta.url)), 'utf8')

// ---- 表单默认值与保存载荷（总开关设计 §2/§8）----

const form = defaultAccountForm('gpt', 'api_key')
assert.equal(form.backgroundProbeEnabled, true, '新账户后台自动探测必须默认开启')
assert.equal(form.status, 'pending_test', '开启后台探测的新账户保持默认待检查语义不变')

const savePayload = buildAccountSavePayload({
  accounts: [],
  form,
  errorPolicyRules: [],
  responseInspectionRules: []
})
assert.equal(savePayload.backgroundProbeEnabled, true, '创建 payload 必须携带后台自动探测开启值')
assert.equal('backgroundProbeEnabled' in buildAccountUpdatePayload(savePayload), true, '编辑 payload 构造必须透传后台自动探测（PATCH delta 再按基线差量裁剪）')

const manualForm = defaultAccountForm('gpt', 'api_key')
manualForm.name = '手动模式载荷回归'
manualForm.groupId = 'grp-test'
manualForm.apiKey = 'sk-test'
manualForm.apiKeys = ['sk-test']
manualForm.baseUrl = 'https://api.openai.com/v1'
manualForm.supportedModels = ['gpt-5.4-mini']
manualForm.healthCheckModel = 'gpt-5.4-mini'
manualForm.backgroundProbeEnabled = false
manualForm.status = 'active'
const manualSavePayload = buildAccountSavePayload({
  accounts: [],
  form: manualForm,
  errorPolicyRules: [],
  responseInspectionRules: []
})
assert.equal(manualSavePayload.backgroundProbeEnabled, false, '手动模式创建 payload 必须携带关闭值')
assert.equal(buildAccountUpdatePayload(manualSavePayload).backgroundProbeEnabled, false, '手动模式编辑 payload 必须保留关闭值')

// audio/video 媒体域不适用总开关：载荷不携带该键（总开关设计 §2）。
for (const domain of ['audio', 'video'] as const) {
  const mediaForm = defaultAccountForm('qwen', 'api_key')
  mediaForm.capabilityDomain = domain
  mediaForm.name = '媒体域载荷回归'
  mediaForm.groupId = 'grp-test'
  mediaForm.apiKey = 'sk-media'
  mediaForm.apiKeys = ['sk-media']
  mediaForm.baseUrl = 'https://dashscope.aliyuncs.com'
  mediaForm.supportedModels = ['qwen-tts']
  mediaForm.healthCheckModel = 'qwen-tts'
  mediaForm.backgroundProbeEnabled = false
  const mediaPayload = buildAccountSavePayload({
    accounts: [],
    form: mediaForm,
    errorPolicyRules: [],
    responseInspectionRules: []
  })
  assert.equal('backgroundProbeEnabled' in mediaPayload, false, `${domain} 域创建 payload 不得携带后台自动探测键`)
  assert.equal('backgroundProbeEnabled' in buildAccountUpdatePayload(mediaPayload), false, `${domain} 域编辑 payload 不得携带后台自动探测键`)
}
// image 域适用总开关（2026-10-30 二轮复审裁决：图像账户存在激活/冷却复测等
// 后台探测）：载荷必须携带该键，手动模式 image 账户才可持久化。
for (const domain of ['image'] as const) {
  const imageForm = defaultAccountForm('qwen', 'api_key')
  imageForm.capabilityDomain = domain
  imageForm.name = '图像域载荷回归'
  imageForm.groupId = 'grp-test'
  imageForm.apiKey = 'sk-media'
  imageForm.apiKeys = ['sk-media']
  imageForm.baseUrl = 'https://dashscope.aliyuncs.com'
  imageForm.supportedModels = ['qwen-tts']
  imageForm.healthCheckModel = 'qwen-tts'
  imageForm.backgroundProbeEnabled = false
  const imagePayload = buildAccountSavePayload({
    accounts: [],
    form: imageForm,
    errorPolicyRules: [],
    responseInspectionRules: []
  })
  assert.equal(imagePayload.backgroundProbeEnabled, false, `${domain} 域创建 payload 必须携带后台自动探测键（开关适用）`)
  assert.equal(buildAccountUpdatePayload(imagePayload).backgroundProbeEnabled, false, `${domain} 域编辑 payload 必须保留后台自动探测键`)
}

// ---- 手动模式提交拦截（总开关设计 §4 二态状态门禁）----

const pendingManualForm = defaultAccountForm('gpt', 'api_key')
pendingManualForm.name = '手动模式待检查拦截回归'
pendingManualForm.groupId = 'grp-test'
pendingManualForm.apiKey = 'sk-test'
pendingManualForm.apiKeys = ['sk-test']
pendingManualForm.baseUrl = 'https://api.openai.com/v1'
pendingManualForm.supportedModels = ['gpt-5.4-mini']
pendingManualForm.healthCheckModel = 'gpt-5.4-mini'
pendingManualForm.backgroundProbeEnabled = false
pendingManualForm.status = 'pending_test'
pendingManualForm.statusSelectionExplicit = true
assert.equal(
  validateAccountSaveForm({
    form: pendingManualForm,
    hasAuthSession: false,
    errorPolicyRules: [],
    responseInspectionRules: [],
    providers: FALLBACK_PROVIDERS
  }),
  '手动模式不适用待检查状态：请选择可调度或停用',
  '手动模式显式选择待检查必须在提交前被拦截'
)
// 非显式选择（异常账户装载映射）不拦截：PATCH 不携带 status，行为零变化。
pendingManualForm.statusSelectionExplicit = false
assert.equal(
  validateAccountSaveForm({
    form: pendingManualForm,
    hasAuthSession: false,
    errorPolicyRules: [],
    responseInspectionRules: [],
    providers: FALLBACK_PROVIDERS
  }),
  undefined,
  '手动模式下未显式选择状态的过程态账户必须允许保存其他字段'
)
pendingManualForm.status = 'active'
assert.equal(
  validateAccountSaveForm({
    form: pendingManualForm,
    hasAuthSession: false,
    errorPolicyRules: [],
    responseInspectionRules: [],
    providers: FALLBACK_PROVIDERS
  }),
  undefined,
  '手动模式可调度状态应放行'
)

// ---- 二态状态映射（总开关设计 §4/§9）----

assert.equal(accountStatusText(manualAccountFixture({ status: 'active' })), '正常', '手动模式正常账户必须显示「正常」')
assert.equal(accountStatusColor(manualAccountFixture({ status: 'active' })), 'green', '手动模式正常账户必须显示绿色')
assert.equal(accountStatusText(manualAccountFixture({ status: 'error' })), '异常', '手动模式异常账户必须显示「异常」')
assert.equal(accountStatusColor(manualAccountFixture({ status: 'error' })), 'red', '手动模式异常账户必须显示红色')
assert.equal(accountStatusText(manualAccountFixture({ status: 'disabled' })), '停用', '手动模式停用账户必须显示「停用」')
assert.equal(accountStatusColor(manualAccountFixture({ status: 'disabled' })), 'default', '手动模式停用账户必须显示灰色')
// 过程态抑制：手动模式不得展示待检查/临时不可调用/限流中/质量隔离等过程态标签。
for (const status of ['pending_test', 'temporary_unavailable', 'rate_limited', 'quality_isolated'] as const) {
  assert.notEqual(accountStatusText(manualAccountFixture({ status })), '待检查', `手动模式 ${status} 不得显示待检查过程态`)
}
// 正交状态（授权/到期/额度）保持现有展示（总开关设计 §9）。
assert.equal(
  accountStatusText(manualAccountFixture({ status: 'active', authorizationExpiresAt: '2020-01-01T00:00:00.000Z', authorizationStatus: 'expired', accessType: 'authorized' })),
  '授权到期',
  '手动模式授权到期必须保留现有展示'
)
assert.equal(
  accountStatusText(manualAccountFixture({ status: 'active', accountExpiresAt: '2020-01-01T00:00:00.000Z' })),
  '账户到期',
  '手动模式账户到期必须保留现有展示'
)
// 开关缺省（旧数据未返回）按开启处理，行为零变化。
assert.equal(accountStatusText(accountFixture({ status: 'temporary_unavailable' })), '临时不可调用', '缺省开关的临时不可调用账户保持现有文案')
assert.equal(accountStatusText(accountFixture({ status: 'active' })), '可调度', '缺省开关的正常账户保持「可调度」文案')
// audio/video 媒体域不适用：开关关闭也不套用二态映射。
const mediaAccount = accountFixture({ status: 'active', backgroundProbeEnabled: false, capabilityDomain: 'audio' })
assert.equal(accountStatusText(mediaAccount), '可调度', '媒体域账户不适用手动模式二态映射')
// image 域适用（二轮复审裁决）：开关关闭套用二态映射并显示「手动」标签。
const imageAccount = accountFixture({ status: 'active', backgroundProbeEnabled: false, capabilityDomain: 'image' })
assert.equal(accountStatusText(imageAccount), '正常', 'image 域手动账户必须套用手动模式二态映射')
const imageManualTag = accountManualModeTag(imageAccount)
assert.equal(imageManualTag?.label, '手动', 'image 域手动账户必须显示「手动」标签')
assert.equal(accountManualModeTag(accountFixture({ status: 'active', backgroundProbeEnabled: false, capabilityDomain: 'video' })), undefined, 'video 域账户不得显示手动标签')

// ---- 状态 tooltip（总开关设计 §9）----

const manualTooltip = accountStatusTooltipLines(manualAccountFixture({
  status: 'active',
  availabilityPresentation: {
    status: 'available',
    label: '可调度',
    probe: {
      kind: 'health_check',
      lastObservation: { observationId: 'manual-tooltip', attemptedAt: '2026-10-30T01:00:00.000Z', result: 'success' },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-10-30T02:00:00.000Z' }
    }
  }
}))
assertTrue(manualTooltip.some((line) => line.includes('主动探测：已关闭（仅真实流量，手动恢复）')), '手动模式 tooltip 必须显示主动探测关闭提示')
assertTrue(manualTooltip.some((line) => line.includes('（关闭前）')), '手动模式历史检查时间必须标注关闭前')
assertTrue(!manualTooltip.some((line) => line.includes('下次检查')), '手动模式 tooltip 不得输出下次检查')
const enabledTooltip = accountStatusTooltipLines(accountFixture({
  availabilityPresentation: {
    status: 'available',
    label: '可调度',
    probe: {
      kind: 'health_check',
      lastObservation: { observationId: 'enabled-tooltip', attemptedAt: '2026-10-30T01:00:00.000Z', result: 'success' },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-10-30T02:00:00.000Z' }
    }
  }
}))
assertTrue(enabledTooltip.some((line) => line.includes('下次检查')), '开启探测的账户 tooltip 保持下次检查展示')
assertTrue(!enabledTooltip.some((line) => line.includes('（关闭前）')), '开启探测的账户不得标注关闭前')

// ---- 列表「手动」标签（总开关设计 §9）----

const manualTag = accountManualModeTag(manualAccountFixture({ status: 'active' }))
assert.equal(manualTag?.color, 'default', '手动标签必须使用灰色 default')
assert.equal(manualTag?.label, '手动', '手动标签文案必须为「手动」')
assert.equal(accountManualModeTag(accountFixture({ status: 'active' })), undefined, '开启探测的账户不得显示手动标签')
assert.equal(accountManualModeTag(mediaAccount), undefined, 'audio/video 媒体域账户不得显示手动标签')
assertTrue(accountManualModeTooltipText(manualAccountFixture({ status: 'active' })).includes('异常需手动处理'), '手动标签 tooltip 必须说明异常需手动处理')

// ---- 表单模板断言（总开关设计 §4/§7，两段模板分支都要）----

const toggleBindings = modalSource.match(/v-model:checked="form\.backgroundProbeEnabled"/g) ?? []
assert.equal(toggleBindings.length, 2, '编辑态与创建态两段稳定性卡都必须渲染后台自动探测总开关')
assert.match(modalSource, /v-model:checked="form\.backgroundProbeEnabled"/, '总开关必须绑定 form.backgroundProbeEnabled')
assert.match(modalSource, /manualProbeMode/, '手动模式判定必须由表单总开关驱动')
assert.match(modalSource, /:disabled="authorizedEditing \|\| manualProbeMode"/, '持续恢复探活子开关必须在手动模式下置灰')
assert.match(modalSource, /仅在后台自动探测开启时生效/, '子开关置灰必须提示仅后台自动探测开启时生效')
assert.match(modalSource, /手动模式不适用锁死/, '手动模式必须显示锁死不适用提示')
assert.doesNotMatch(modalSource, /v-if="manualProbeMode" class="lock-config-fields"/, '手动模式不得渲染锁死配置字段')
assert.match(modalSource, /手动模式 · 探活/, '稳定性卡摘要必须以手动模式前缀展示')
assert.match(modalSource, /将清除待检查\/冷却\/限流状态；质量隔离转为异常；自动取消特供并释放名额；自动解除锁死/, '关闭总开关确认框必须列出归一化后果')
assert.match(modalSource, /重新开启后将安排一次即时健康检查/, '重新开启总开关必须提示即时健康检查')
assert.match(modalSource, /onCancel[\s\S]*backgroundProbeEnabled = true/, '取消确认必须回弹总开关')
assert.match(modalSource, /statusSegmentedOptions/, '编辑态标题状态选项必须按手动模式收敛')
assert.match(modalSource, /label: '正常', value: 'active'/, '手动模式状态选项必须提供正常')
assert.match(modalSource, /disabled: true/, '异常状态必须以只读禁用项回显')

// image 域表单渲染总开关（二轮复审裁决）：开关区仅 image 域出现，经
// background-probe-toggle 事件走 modal 同款关闭确认；audio/video 仍不渲染。
assert.match(mediaFormSource, /capabilityDomain === 'image'/, '媒体域表单必须仅在 image 域渲染后台自动探测开关')
assert.match(mediaFormSource, /background-probe-toggle/, 'image 域开关必须经 background-probe-toggle 事件走 modal 关闭确认')
assert.match(
  mediaFormSource,
  /v-model:checked="form\.backgroundProbeEnabled"/,
  'image 域开关必须以 v-model 写回 form.backgroundProbeEnabled（确认后写值，取消回弹 true；单向 :checked 不落值）'
)
// 余额查询卡片不纳入本开关（总开关设计 §12）。
assert.doesNotMatch(balanceSectionSource, /backgroundProbe/, '余额查询组件不得引用后台自动探测开关')
assert.match(payloadSource, /capabilityDomainWithoutBackgroundProbe\(input\.form\.capabilityDomain\)/, '保存载荷必须仅对 audio/video 媒体域排除总开关键（image 适用）')

// ---- 列表状态标签组件断言 ----

assert.match(statusTagSource, /accountManualModeTag/, '状态标签组件必须渲染手动标签')
assert.match(statusTagSource, /v-if="account\.expeditedRecoveryEnabled && !manualModeTag"/, '手动模式必须隐藏特供徽章')

// ---- 健康监控页断言（总开关设计 §9）----

assert.match(healthTypesSource, /backgroundProbeEnabled\?: boolean/, '健康监控行类型必须携带后台自动探测字段')
assert.match(healthViewSource, /probeDisabled\(account\)/, '健康页必须按后台自动探测字段判定手动模式')
assert.match(healthViewSource, /<a-tag v-if="probeDisabled\(account\)" color="default">手动<\/a-tag>/, '健康页账户卡片必须显示灰色手动标签')
assert.match(healthViewSource, /最近独立检查（关闭前）/, '健康页历史检查必须标注关闭前')
assert.match(healthViewSource, /主动探测：已关闭/, '健康页手动账户必须显示主动探测已关闭')
assert.match(healthViewSource, /if \(status === 'active'\) return '正常'/, '健康页手动账户状态必须显示正常')

// ---- 列表操作断言（总开关设计 §7 功能可用性）----

assert.match(menuActionsSource, /手动模式不适用锁死/, '手动账户锁死操作必须提示不适用')
assert.match(menuActionsSource, /手动模式不适用特供恢复/, '手动账户特供操作必须提示不适用')
assert.match(menuActionsSource, /accountBackgroundProbeDisabled\(account\) \|\| isAuthorizedAccount\(account\) \|\| !isPendingHealthCheckFailed\(account\)/, '手动账户重新检查入口必须拦截')

console.log('后台自动探测总开关前端回归通过：默认值、保存载荷、媒体域排除（audio/video）、image 域适用、手动模式提交拦截、二态状态映射、tooltip、手动标签、表单两段分支、健康页展示、列表操作拦截')

// ---- fixtures 与断言助手 ----

function accountFixture(overrides: Partial<AccountListItem> = {}): AccountListItem {
  return {
    id: 'account_background_probe_regression',
    providerCode: 'gpt',
    name: '总开关回归账户',
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    todayUsage: { requestCount: 0, totalTokens: 0, totalCost: 0 },
    ...overrides
  }
}

function manualAccountFixture(overrides: Partial<AccountListItem> = {}): AccountListItem {
  return accountFixture({ backgroundProbeEnabled: false, ...overrides })
}

function assertTrue(value: boolean, message: string): void {
  if (!value) throw new Error(message)
}
