import type { ManagementSettingsSectionKey } from '@/api/domains/settings'
import type { GlobalForm, SystemForm, UpstreamClientVersionKey } from './settingsForm'
import { defaultGlobalSettings, defaultSystemSettings } from './settingsForm'

/**
 * 系统设置「展示行 + 按组编辑」的展示模型（方案 C）：
 * - UI 7 组（品牌与展示 / 请求与限流 / 调度与恢复 / 超时与中断 / 上游版本覆盖 / 数据保留 / 日志与审计）
 *   由后端 8 个 section 契约多对一映射驱动，编辑与保存仍按 section 提交；
 * - 展示行按语义归并（同族字段一行），custom 判定 = 服务端 baseline ≠ 前端默认值。
 */

export type SettingFieldKind = 'number' | 'text' | 'brandIcon'

export interface SettingFieldDef {
  /** 顶层表单键，或 overrides 子键（upstreamClientVersionOverrides.codex）。 */
  key: string
  label: string
  tip?: string
  unit?: string
  kind: SettingFieldKind
  min?: number
  max?: number
  /** 数字输入的小数位数（缺省 0 = 整数）。 */
  precision?: number
  placeholder?: string
}

/** 编辑态子组分隔标题（fields 数组内的分段标记）。 */
export interface SettingFieldDivider {
  divider: string
}

export interface SettingViewRow {
  key: string
  label: string
  value: string
  custom?: boolean
  defaultText?: string
}

export interface SettingViewSubgroup {
  title: string
  rows: SettingViewRow[]
}

/** 展示态组级操作按钮（如审计日志手动清理）。 */
export interface SettingGroupAction {
  key: string
  label: string
  /** a-popconfirm 确认文案。 */
  confirm: string
}

export interface SettingGroupDef {
  key: string
  title: string
  hint: string
  /** 该组依赖的后端 section（决定加载/错误态与保存批次）。 */
  sectionKeys: ManagementSettingsSectionKey[]
  /** 组内可编辑字段总数（页面摘要计数用）。 */
  itemCount: number
  /** 展示态组级操作（编辑按钮之外的独立动作）。 */
  actions?: SettingGroupAction[]
  fields: Array<SettingFieldDef | SettingFieldDivider>
  /** 展示行（归并后）按子组生成。 */
  viewRows: (access: SettingsDisplayAccess) => SettingViewSubgroup[]
}

export interface SettingsDisplayAccess {
  /** 服务端基线值（保存后的生效值）。 */
  baseline: (key: string) => string | number | undefined
  /** 基线是否偏离前端默认值（= 自定义）。 */
  isCustom: (key: string) => boolean
  /** 客户端版本自动层当前值（jobs 跟版任务维护，gateway-core 分区伴随字段下发）。未实现/缺族/未加载视为空。 */
  clientVersionAuto?: (family: UpstreamClientVersionKey) => string
  /** 客户端版本内置基线（编译期常量，gateway-core 分区伴随字段下发）。未实现/未加载视为空。 */
  clientVersionBuiltIn?: (family: UpstreamClientVersionKey) => string
}

const OVERRIDE_PREFIX = 'upstreamClientVersionOverrides.'

export const upstreamOverrideFieldKeys: readonly UpstreamClientVersionKey[] = ['codex', 'claudeCode', 'geminiCLI', 'zcode', 'grokCLI']

export const upstreamOverrideLabels: Record<UpstreamClientVersionKey, string> = {
  codex: 'Codex exec',
  claudeCode: 'Claude Code',
  geminiCLI: 'Gemini CLI',
  zcode: 'ZCode',
  grokCLI: 'Grok CLI'
}

function defaultValueOf(key: string): string | number | undefined {
  if (key === 'appName') return defaultGlobalSettings.appName
  if (key === 'appIcon') return defaultGlobalSettings.appIcon
  if (key.startsWith(OVERRIDE_PREFIX)) {
    return defaultSystemSettings.upstreamClientVersionOverrides[key.slice(OVERRIDE_PREFIX.length) as UpstreamClientVersionKey]
  }
  return (defaultSystemSettings as unknown as Record<string, string | number | undefined>)[key]
}

/** 服务端 baseline 与前端默认值比对；overrides 子键按家族逐个判。 */
export function isCustomValue(key: string, baselineValue: string | number | undefined): boolean {
  const fallback = defaultValueOf(key)
  if (typeof fallback === 'string' || typeof baselineValue === 'string') {
    return String(baselineValue ?? '') !== String(fallback ?? '')
  }
  return baselineValue !== fallback
}

export function defaultCompareText(key: string, format?: (value: string | number) => string): string | undefined {
  const fallback = defaultValueOf(key)
  if (fallback === undefined) return undefined
  const text = format ? format(fallback) : String(fallback)
  return `默认 ${text}`
}

function num(access: SettingsDisplayAccess, key: string): string | number | undefined {
  return access.baseline(key)
}

function withUnit(unit?: string): (value: string | number) => string {
  return (value) => (unit ? `${value} ${unit}` : String(value))
}

/** 数值展示：带单位；0 视为「不限/无」的字段用 zeroAs 语义化；unit 为空时仅显示数值。 */
function displayNumber(value: string | number | undefined, unit: string, zeroAs?: string): string {
  if (value === undefined) return '-'
  if (zeroAs && value === 0) return zeroAs
  return unit ? `${value} ${unit}` : String(value)
}

/**
 * 归并行工厂：同族字段合并为一行展示；任一成员自定义则整行标 custom，
 * defaultText 汇总默认值对照。
 */
function mergedRow(
  key: string,
  label: string,
  members: Array<{ key: string; text: string }>,
  access: SettingsDisplayAccess,
  defaultsSummary?: string
): SettingViewRow {
  const custom = members.some((m) => access.isCustom(m.key))
  return {
    key,
    label,
    value: members.map((m) => m.text).join(' · '),
    custom,
    defaultText: custom ? defaultsSummary : undefined
  }
}

// ---------------------------------------------------------------------------
// 7 组定义
// ---------------------------------------------------------------------------

export const settingGroups: readonly SettingGroupDef[] = [
  {
    key: 'brand',
    title: '品牌与展示',
    hint: '2 项 · 仅管理角色可见',
    sectionKeys: ['brand'],
    itemCount: 2,
    fields: [
      { key: 'appName', label: '系统名称', kind: 'text', tip: '保存后显示到左侧菜单标题、浏览器 tab，并用于登录页标题' },
      { key: 'appIcon', label: '系统图标', kind: 'brandIcon', tip: '默认 /__aisys__/brand-icon.svg；可上传 256KB 以内图片并以 Data URL 保存' }
    ],
    viewRows: (access) => [
      {
        title: '',
        rows: [
          { key: 'appName', label: '系统名称', value: String(access.baseline('appName') ?? '-') },
          {
            key: 'appIcon',
            label: '系统图标',
            value: access.isCustom('appIcon') ? '自定义图标' : '默认图标',
            custom: access.isCustom('appIcon')
          }
        ]
      }
    ]
  },
  {
    key: 'request-limits',
    title: '请求与限流',
    hint: '13 项 · 网关吞吐 / 用户配额 / 后台接口保护',
    sectionKeys: ['gateway-core', 'user-request-limit', 'api-rate-limit'],
    itemCount: 13,
    fields: [
      { divider: '网关请求' },
      { key: 'gatewayTextRawBodyLimitMegabytes', label: '文本请求体上限', kind: 'number', unit: 'MB', min: 1, max: 64, tip: '可设置 1 到 64；调大可承载更长上下文，也会增加单请求内存压力' },
      { key: 'accountCircuitConfirmationFailuresRequired', label: '电路独立确认失败次数', kind: 'number', unit: '次', min: 1, max: 5, tip: '首次 transport 失败进入待确认；默认还需 2 个不同请求的独立失败证据才熔断' },
      { divider: '用户限制 · 0 表示无限 · 可在系统账户编辑中单独覆盖' },
      { key: 'gatewayUserRequestLimitPerMinute', label: '每分钟请求数', kind: 'number', unit: '次/分', min: 0 },
      { key: 'gatewayUserRequestLimitPerDay', label: '每日请求数', kind: 'number', unit: '次/日', min: 0 },
      { key: 'gatewayUserRequestLimitPerWeek', label: '每周请求数', kind: 'number', unit: '次/周', min: 0 },
      { key: 'gatewayUserRequestLimitPerMonth', label: '每月请求数', kind: 'number', unit: '次/月', min: 0 },
      { key: 'userAiAccountLimit', label: 'AI 账户数量限制', kind: 'number', unit: '个', min: 0, tip: '限制每个用户可创建的自有 AI 账户数量，删除账户后释放名额' },
      { divider: '后台接口限流 · 健康检查不受影响' },
      { key: 'systemApiRateLimitIpReadPerMinute', label: 'IP 读请求', kind: 'number', unit: '/分', min: 0, tip: '默认 600；适用于 GET、HEAD 和 OPTIONS' },
      { key: 'systemApiRateLimitIpReadBurstPer10Seconds', label: 'IP 读突发', kind: 'number', unit: '/10秒', min: 0, tip: '默认 120；拦截短时间刷新列表和探测接口' },
      { key: 'systemApiRateLimitIpWritePerMinute', label: 'IP 写请求', kind: 'number', unit: '/分', min: 0, tip: '默认 180；适用于 POST、PATCH、PUT 和 DELETE' },
      { key: 'systemApiRateLimitIpWriteBurstPer10Seconds', label: 'IP 写突发', kind: 'number', unit: '/10秒', min: 0, tip: '默认 40；优先挡住批量提交和暴力探测' },
      { key: 'systemApiRateLimitUserReadPerMinute', label: '登录用户读请求', kind: 'number', unit: '/分', min: 0, tip: '默认 300；同一登录账号的后台读请求保护' },
      { key: 'systemApiRateLimitUserWritePerMinute', label: '登录用户写请求', kind: 'number', unit: '/分', min: 0, tip: '默认 120；对保存、删除、批量操作等写请求再加一层限制' }
    ],
    viewRows: (access) => [
      {
        title: '网关请求',
        rows: [
          { key: 'gatewayTextRawBodyLimitMegabytes', label: '文本请求体上限', value: displayNumber(num(access, 'gatewayTextRawBodyLimitMegabytes'), 'MB'), custom: access.isCustom('gatewayTextRawBodyLimitMegabytes'), defaultText: access.isCustom('gatewayTextRawBodyLimitMegabytes') ? defaultCompareText('gatewayTextRawBodyLimitMegabytes') : undefined },
          { key: 'accountCircuitConfirmationFailuresRequired', label: '电路独立确认失败次数', value: displayNumber(num(access, 'accountCircuitConfirmationFailuresRequired'), '次'), custom: access.isCustom('accountCircuitConfirmationFailuresRequired'), defaultText: access.isCustom('accountCircuitConfirmationFailuresRequired') ? defaultCompareText('accountCircuitConfirmationFailuresRequired') : undefined }
        ]
      },
      {
        title: '用户限制',
        rows: [
          mergedRow('user-rate-limits', '每分钟 / 每日 / 每周 / 每月请求数', [
            { key: 'gatewayUserRequestLimitPerMinute', text: `每分钟 ${displayNumber(num(access, 'gatewayUserRequestLimitPerMinute'), '次', '不限')}` },
            { key: 'gatewayUserRequestLimitPerDay', text: `每日 ${displayNumber(num(access, 'gatewayUserRequestLimitPerDay'), '次', '不限')}` },
            { key: 'gatewayUserRequestLimitPerWeek', text: `每周 ${displayNumber(num(access, 'gatewayUserRequestLimitPerWeek'), '次', '不限')}` },
            { key: 'gatewayUserRequestLimitPerMonth', text: `每月 ${displayNumber(num(access, 'gatewayUserRequestLimitPerMonth'), '次', '不限')}` }
          ], access, '默认均不限'),
          { key: 'userAiAccountLimit', label: 'AI 账户数量限制', value: displayNumber(num(access, 'userAiAccountLimit'), '个', '无限'), custom: access.isCustom('userAiAccountLimit'), defaultText: access.isCustom('userAiAccountLimit') ? defaultCompareText('userAiAccountLimit', withUnit('个')) : undefined }
        ]
      },
      {
        title: '后台接口限流',
        rows: [
          mergedRow('ip-rate-limits', 'IP 读 / 读突发 / 写 / 写突发', [
            { key: 'systemApiRateLimitIpReadPerMinute', text: `读 ${displayNumber(num(access, 'systemApiRateLimitIpReadPerMinute'), '/分')}` },
            { key: 'systemApiRateLimitIpReadBurstPer10Seconds', text: `读突发 ${displayNumber(num(access, 'systemApiRateLimitIpReadBurstPer10Seconds'), '/10秒')}` },
            { key: 'systemApiRateLimitIpWritePerMinute', text: `写 ${displayNumber(num(access, 'systemApiRateLimitIpWritePerMinute'), '/分')}` },
            { key: 'systemApiRateLimitIpWriteBurstPer10Seconds', text: `写突发 ${displayNumber(num(access, 'systemApiRateLimitIpWriteBurstPer10Seconds'), '/10秒')}` }
          ], access, '默认 读 600/分 · 读突发 120/10秒 · 写 180/分 · 写突发 40/10秒'),
          mergedRow('user-api-rate-limits', '登录用户读 / 写', [
            { key: 'systemApiRateLimitUserReadPerMinute', text: `读 ${displayNumber(num(access, 'systemApiRateLimitUserReadPerMinute'), '/分')}` },
            { key: 'systemApiRateLimitUserWritePerMinute', text: `写 ${displayNumber(num(access, 'systemApiRateLimitUserWritePerMinute'), '/分')}` }
          ], access, '默认 读 300/分 · 写 120/分')
        ]
      }
    ]
  },
  {
    key: 'scheduling',
    title: '调度与恢复',
    hint: '7 项 · 健康检测 / 临时不可调用 / 冷却复测',
    sectionKeys: ['account-health', 'gateway-core', 'cooldown-retest'],
    itemCount: 7,
    fields: [
      { divider: '正常账号健康检测 · 只检测长期没有真实成功请求的正常账号' },
      { key: 'accountHealthCheckIntervalHours', label: '检测间隔', kind: 'number', unit: '小时', min: 1, max: 168, tip: '账号近期已有真实成功请求时不再额外探测' },
      { key: 'accountHealthCheckJitterMinutes', label: '错峰窗口', kind: 'number', unit: '分钟', min: 0, max: 1440, tip: '按账号 ID 稳定错峰，避免大量账号同时探测' },
      { key: 'accountHealthCheckFailureThreshold', label: '连续失败阈值', kind: 'number', unit: '次', min: 1, max: 10, tip: '达到阈值后才进入临时不可调用处理，降低网络抖动误杀' },
      { divider: '临时不可调用与安全重试' },
      { key: 'defaultTemporaryUnschedulableMinutes', label: '最大暂停时间', kind: 'number', unit: '分钟', min: 1, max: 1440, tip: '账号进入临时不可调用后先走快速恢复通道：3 秒起步，失败后翻倍；单次等待不超过该上限' },
      { key: 'temporaryUnschedulableRetryIntervalSeconds', label: '安全原地重试间隔', kind: 'number', unit: '秒', min: 0, max: 3600, tip: '仅对可安全重放的文本请求，在主请求已发出且响应头到达前发生传输异常时使用；完整 HTTP、正文中断、配置首字截止和副作用请求不占次数' },
      { key: 'temporaryUnschedulableRetryAttempts', label: '安全原地重试次数', kind: 'number', unit: '次', min: 0, max: 10, tip: '整次请求共享的同账户重试上限；兄弟 Key 会先尝试且不占次数。不按上游状态码或正文判断，也不写账户或 Key 状态' },
      { divider: '冷却账户复测 · 仅复测临时不可调用和限流中的账户' },
      { key: 'cooldownAccountRetestMaxBackoffHours', label: '长期不可用观察阈值', kind: 'number', unit: '小时', min: 1, max: 720, tip: '从进入临时不可调用或限流中开始计时，超过后不转异常，而是显示为长期不可用' }
    ],
    viewRows: (access) => [
      {
        title: '健康检测',
        rows: [
          mergedRow('health-check', '检测间隔 · 错峰窗口 · 连续失败阈值', [
            { key: 'accountHealthCheckIntervalHours', text: displayNumber(num(access, 'accountHealthCheckIntervalHours'), '小时') },
            { key: 'accountHealthCheckJitterMinutes', text: displayNumber(num(access, 'accountHealthCheckJitterMinutes'), '分钟') },
            { key: 'accountHealthCheckFailureThreshold', text: displayNumber(num(access, 'accountHealthCheckFailureThreshold'), '次') }
          ], access, '默认 1 小时 · 10 分钟 · 3 次')
        ]
      },
      {
        title: '临时不可调用与安全重试',
        rows: [
          mergedRow('temporary-unschedulable', '最大暂停 · 重试间隔 · 重试次数', [
            { key: 'defaultTemporaryUnschedulableMinutes', text: displayNumber(num(access, 'defaultTemporaryUnschedulableMinutes'), '分钟') },
            { key: 'temporaryUnschedulableRetryIntervalSeconds', text: displayNumber(num(access, 'temporaryUnschedulableRetryIntervalSeconds'), '秒') },
            { key: 'temporaryUnschedulableRetryAttempts', text: displayNumber(num(access, 'temporaryUnschedulableRetryAttempts'), '次') }
          ], access, '默认 2 分钟 · 3 秒 · 3 次')
        ]
      },
      {
        title: '冷却复测',
        rows: [
          { key: 'cooldownAccountRetestMaxBackoffHours', label: '长期不可用观察阈值', value: displayNumber(num(access, 'cooldownAccountRetestMaxBackoffHours'), '小时'), custom: access.isCustom('cooldownAccountRetestMaxBackoffHours'), defaultText: access.isCustom('cooldownAccountRetestMaxBackoffHours') ? defaultCompareText('cooldownAccountRetestMaxBackoffHours', withUnit('小时')) : undefined }
        ]
      }
    ]
  },
  {
    key: 'timeouts',
    title: '超时与中断',
    hint: '10 项 · 文本 / 图像 lane 独立控制',
    sectionKeys: ['gateway-core'],
    itemCount: 10,
    fields: [
      { divider: '文本 · 流式' },
      { key: 'textFirstResponseTimeoutSeconds', label: '首响应等待', kind: 'number', unit: '秒', min: 10, max: 3600, tip: '当前账号超过该时间仍未返回响应头时，进入未提交接管' },
      { key: 'textStreamIdleTimeoutSeconds', label: '流式停顿', kind: 'number', unit: '秒', min: 1, max: 3600, tip: '收到首段内容后，超过该时间没有任何上游新数据时收口当前尝试' },
      { key: 'textUncommittedAttemptMaxLifetimeSeconds', label: '未提交尝试寿命', kind: 'number', unit: '秒', min: 60, max: 86400, tip: '当前账号尚未产生模型语义输出时的单次尝试最大存活时间' },
      { divider: '文本 · 非流式' },
      { key: 'textNonStreamFirstResponseTimeoutSeconds', label: '非流式响应等待', kind: 'number', unit: '秒', min: 10, max: 3600, tip: '上游需生成完整结果后才返回，超过该等待时间未收到首个响应则终止当前尝试' },
      { divider: '图像' },
      { key: 'imageFirstResponseTimeoutSeconds', label: '首响应等待', kind: 'number', unit: '秒', min: 10, max: 3600, tip: 'image lane 单次 attempt；超时候若下游未提交则按统一候选机制继续切换' },
      { key: 'imageStreamIdleTimeoutSeconds', label: '流式停顿', kind: 'number', unit: '秒', min: 1, max: 3600, tip: '收到首段内容后，超过该时间没有任何上游新数据时收口当前尝试' },
      { key: 'imageUncommittedAttemptMaxLifetimeSeconds', label: '未提交尝试寿命', kind: 'number', unit: '秒', min: 60, max: 86400, tip: '当前账号尚未产生模型语义输出时的单次尝试最大存活时间' },
      { key: 'imageRequestWallTimeoutSeconds', label: '整请求总时限', kind: 'number', unit: '秒', min: 60, max: 86400, tip: '从接收到候选切换决策的总墙钟；失败后只有总墙钟仍有余量才继续后备候选' },
      { key: 'chatImageGenerationTotalTimeoutSeconds', label: 'AI 对话生图总超时', kind: 'number', unit: '秒', min: 60, max: 86400, tip: '一次 generate_image 工具调用的整体时限，包含网关选号、账户切换、上游生成与资产保存' },
      { divider: '等待' },
      { key: 'noAvailableAccountWaitTimeoutSeconds', label: '无可用账号等待', kind: 'number', unit: '秒', min: 10, max: 3600, tip: '只在没有可立即派发账号时累计；当前账号仍在执行或存在可派发候选时不停止服务端接管' }
    ],
    viewRows: (access) => [
      {
        title: '文本 · 流式',
        rows: [
          mergedRow('text-stream', '首响应 · 流式停顿 · 未提交寿命', [
            { key: 'textFirstResponseTimeoutSeconds', text: displayNumber(num(access, 'textFirstResponseTimeoutSeconds'), '秒') },
            { key: 'textStreamIdleTimeoutSeconds', text: displayNumber(num(access, 'textStreamIdleTimeoutSeconds'), '秒') },
            { key: 'textUncommittedAttemptMaxLifetimeSeconds', text: displayNumber(num(access, 'textUncommittedAttemptMaxLifetimeSeconds'), '秒') }
          ], access, '默认 120 秒 · 30 秒 · 1800 秒')
        ]
      },
      {
        title: '文本 · 非流式',
        rows: [
          { key: 'textNonStreamFirstResponseTimeoutSeconds', label: '非流式响应等待', value: displayNumber(num(access, 'textNonStreamFirstResponseTimeoutSeconds'), '秒'), custom: access.isCustom('textNonStreamFirstResponseTimeoutSeconds'), defaultText: access.isCustom('textNonStreamFirstResponseTimeoutSeconds') ? defaultCompareText('textNonStreamFirstResponseTimeoutSeconds', withUnit('秒')) : undefined }
        ]
      },
      {
        title: '图像',
        rows: [
          mergedRow('image-core', '首响应 · 流式停顿 · 未提交寿命', [
            { key: 'imageFirstResponseTimeoutSeconds', text: displayNumber(num(access, 'imageFirstResponseTimeoutSeconds'), '秒') },
            { key: 'imageStreamIdleTimeoutSeconds', text: displayNumber(num(access, 'imageStreamIdleTimeoutSeconds'), '秒') },
            { key: 'imageUncommittedAttemptMaxLifetimeSeconds', text: displayNumber(num(access, 'imageUncommittedAttemptMaxLifetimeSeconds'), '秒') }
          ], access, '默认 600 秒 · 120 秒 · 3600 秒'),
          mergedRow('image-wall', '整请求总时限 · 对话生图总超时', [
            { key: 'imageRequestWallTimeoutSeconds', text: displayNumber(num(access, 'imageRequestWallTimeoutSeconds'), '秒') },
            { key: 'chatImageGenerationTotalTimeoutSeconds', text: displayNumber(num(access, 'chatImageGenerationTotalTimeoutSeconds'), '秒') }
          ], access, '默认 3600 秒 · 900 秒')
        ]
      },
      {
        title: '等待',
        rows: [
          { key: 'noAvailableAccountWaitTimeoutSeconds', label: '无可用账号等待', value: displayNumber(num(access, 'noAvailableAccountWaitTimeoutSeconds'), '秒'), custom: access.isCustom('noAvailableAccountWaitTimeoutSeconds'), defaultText: access.isCustom('noAvailableAccountWaitTimeoutSeconds') ? defaultCompareText('noAvailableAccountWaitTimeoutSeconds', withUnit('秒')) : undefined }
        ]
      }
    ]
  },
  {
    key: 'upstream-versions',
    title: '上游版本覆盖',
    hint: '5 项 · 应急热覆盖 · 生效取手动 > 自动跟版 > 内置 · 保存即生效',
    sectionKeys: ['gateway-core'],
    itemCount: 5,
    fields: [
      { key: `${OVERRIDE_PREFIX}codex`, label: upstreamOverrideLabels.codex, kind: 'text', placeholder: '留空用自动跟版值', tip: '覆盖 GPT/Codex 家族系统请求的 codex exec 画像版本，如 0.162.0' },
      { key: `${OVERRIDE_PREFIX}claudeCode`, label: upstreamOverrideLabels.claudeCode, kind: 'text', placeholder: '留空用自动跟版值', tip: '覆盖 Anthropic 家族系统请求与网关画像补齐的 claude-cli 版本，如 2.1.295' },
      { key: `${OVERRIDE_PREFIX}geminiCLI`, label: upstreamOverrideLabels.geminiCLI, kind: 'text', placeholder: '留空用自动跟版值', tip: '覆盖 Gemini OAuth（code_assist / google_one）系统请求的 GeminiCLI 画像版本，如 0.63.0' },
      { key: `${OVERRIDE_PREFIX}zcode`, label: upstreamOverrideLabels.zcode, kind: 'text', placeholder: '留空用自动跟版值', tip: '覆盖 GLM 家族系统请求的 ZCode 画像版本（UA 与 X-ZCode-App-Version 同步），如 3.14.3' },
      { key: `${OVERRIDE_PREFIX}grokCLI`, label: upstreamOverrideLabels.grokCLI, kind: 'text', placeholder: '留空用自动跟版值', tip: '覆盖 Grok OAuth 上游的 Grok CLI 画像版本（x-grok-client-version 与 UA 同步），如 1.0.45' }
    ],
    viewRows: (access) => {
      const rows = upstreamOverrideFieldKeys.map((subKey) => {
        const manual = String(access.baseline(`${OVERRIDE_PREFIX}${subKey}`) ?? '')
        if (manual) {
          return { key: `${OVERRIDE_PREFIX}${subKey}`, label: upstreamOverrideLabels[subKey], value: `${manual}（手动覆盖）`, custom: true }
        }
        const auto = access.clientVersionAuto?.(subKey) ?? ''
        if (auto) {
          return { key: `${OVERRIDE_PREFIX}${subKey}`, label: upstreamOverrideLabels[subKey], value: `${auto}（自动跟版）` }
        }
        const builtIn = access.clientVersionBuiltIn?.(subKey) ?? ''
        return { key: `${OVERRIDE_PREFIX}${subKey}`, label: upstreamOverrideLabels[subKey], value: builtIn ? `${builtIn}（内置）` : '内置版本' }
      })
      return [{ title: '', rows }]
    }
  },
  {
    key: 'data-retention',
    title: '数据保留',
    hint: '3 项 · 清理前等待统计游标完成',
    sectionKeys: ['data-retention'],
    itemCount: 3,
    fields: [
      { key: 'usageRecordRetentionDays', label: '使用记录保留', kind: 'number', unit: '天', min: 1, max: 180, tip: '最大 180 天；清理前会等待统计游标处理完成，避免破坏聚合' },
      { key: 'runtimeLogIndexRetentionDays', label: '运行日志索引保留', kind: 'number', unit: '天', min: 1, max: 90, tip: '最大 90 天；只影响索引和文件游标清理，不删除原始日志文件' },
      { key: 'publicApiLogRetentionDays', label: '公开接口日志保留', kind: 'number', unit: '天', min: 1, max: 365, tip: '最大 365 天；用于公开接口日志表的后台清理' }
    ],
    viewRows: (access) => [
      {
        title: '',
        rows: [
          mergedRow('retention', '使用记录 · 运行日志索引 · 公开接口日志', [
            { key: 'usageRecordRetentionDays', text: `${displayNumber(num(access, 'usageRecordRetentionDays'), '天')}` },
            { key: 'runtimeLogIndexRetentionDays', text: `${displayNumber(num(access, 'runtimeLogIndexRetentionDays'), '天')}` },
            { key: 'publicApiLogRetentionDays', text: `${displayNumber(num(access, 'publicApiLogRetentionDays'), '天')}` }
          ], access, '默认 30 天 · 14 天 · 30 天')
        ]
      }
    ]
  },
  {
    key: 'log-audit',
    title: '日志与审计',
    hint: '6 项 · 审计请求日志 / 操作日志',
    sectionKeys: ['log-retention'],
    itemCount: 6,
    actions: [
      { key: 'audit-cleanup', label: '立即清理审计日志', confirm: '立即按当前保留策略执行一轮审计日志清理？' }
    ],
    fields: [
      { divider: '审计请求日志 · 保留策略热更新（下一轮自动清理 ≤1 分钟生效）' },
      { key: 'auditLogSuccessRetentionDays', label: '成功保留', kind: 'number', unit: '天', min: 0, max: 3650, tip: '成功请求审计行的保留天数；0 = 只保留热窗，须与采样率 0 成对使用' },
      { key: 'auditLogProblemRetentionDays', label: '问题保留', kind: 'number', unit: '天', min: 1, max: 3650, tip: '失败或问题请求审计行的保留天数，不小于成功保留' },
      { key: 'auditLogSuccessHotRetentionHours', label: '成功热窗', kind: 'number', unit: '小时', min: 0, max: 168, tip: '成功行热窗；热窗外正文降级为 metadata_only，仅保留元数据' },
      { key: 'auditLogSuccessSampleRate', label: '成功采样率', kind: 'number', min: 0, max: 1, precision: 4, tip: '成功行正文保留采样率，0 到 1 之间且最多 4 位小数（如 0.1 = 只保留 10% 成功请求正文）；采样率 0 须与成功保留 0 天成对' },
      { divider: '操作日志' },
      { key: 'operationLogRetentionDays', label: '保留天数', kind: 'number', unit: '天', min: 1, max: 3650, tip: '操作日志记录的保留天数，由后台任务自动清理' },
      { key: 'operationLogMaxChangesPerRecord', label: '每条变更记录上限', kind: 'number', unit: '条', min: 1, max: 500, tip: '单条操作日志记录的变更字段数量上限，超出部分截断' }
    ],
    viewRows: (access) => [
      {
        title: '审计请求日志',
        rows: [
          mergedRow('audit-retention', '成功保留 · 问题保留', [
            { key: 'auditLogSuccessRetentionDays', text: displayNumber(num(access, 'auditLogSuccessRetentionDays'), '天', '仅热窗') },
            { key: 'auditLogProblemRetentionDays', text: displayNumber(num(access, 'auditLogProblemRetentionDays'), '天') }
          ], access, '默认 3 天 · 7 天'),
          mergedRow('audit-hot-sample', '成功热窗 · 成功采样率', [
            { key: 'auditLogSuccessHotRetentionHours', text: displayNumber(num(access, 'auditLogSuccessHotRetentionHours'), '小时') },
            { key: 'auditLogSuccessSampleRate', text: displayNumber(num(access, 'auditLogSuccessSampleRate'), '') }
          ], access, '默认 1 小时 · 1')
        ]
      },
      {
        title: '操作日志',
        rows: [
          mergedRow('operation-log', '保留天数 · 每条变更记录上限', [
            { key: 'operationLogRetentionDays', text: displayNumber(num(access, 'operationLogRetentionDays'), '天') },
            { key: 'operationLogMaxChangesPerRecord', text: displayNumber(num(access, 'operationLogMaxChangesPerRecord'), '条') }
          ], access, '默认 365 天 · 100 条')
        ]
      }
    ]
  }
]

export const settingsTotalItemCount = settingGroups.reduce((sum, group) => sum + group.itemCount, 0)

/**
 * section 提交字段契约（与后端 GET/PATCH /settings/sections/<key> 的键一一对应）。
 * 保存差异 payload 按 section 生成；overrides 以序列化对象整体提交。
 */
export const settingsSectionFields: Record<ManagementSettingsSectionKey, readonly string[]> = {
  brand: ['appName', 'appIcon'],
  'gateway-core': ['gatewayTextRawBodyLimitMegabytes', 'accountCircuitConfirmationFailuresRequired', 'defaultTemporaryUnschedulableMinutes', 'temporaryUnschedulableRetryIntervalSeconds', 'temporaryUnschedulableRetryAttempts', 'textFirstResponseTimeoutSeconds', 'textNonStreamFirstResponseTimeoutSeconds', 'textStreamIdleTimeoutSeconds', 'textUncommittedAttemptMaxLifetimeSeconds', 'imageFirstResponseTimeoutSeconds', 'imageStreamIdleTimeoutSeconds', 'imageUncommittedAttemptMaxLifetimeSeconds', 'imageRequestWallTimeoutSeconds', 'chatImageGenerationTotalTimeoutSeconds', 'noAvailableAccountWaitTimeoutSeconds', 'upstreamClientVersionOverrides'],
  'user-request-limit': ['gatewayUserRequestLimitPerMinute', 'gatewayUserRequestLimitPerDay', 'gatewayUserRequestLimitPerWeek', 'gatewayUserRequestLimitPerMonth', 'userAiAccountLimit'],
  'account-health': ['accountHealthCheckIntervalHours', 'accountHealthCheckJitterMinutes', 'accountHealthCheckFailureThreshold'],
  'api-rate-limit': ['systemApiRateLimitIpReadPerMinute', 'systemApiRateLimitIpReadBurstPer10Seconds', 'systemApiRateLimitIpWritePerMinute', 'systemApiRateLimitIpWriteBurstPer10Seconds', 'systemApiRateLimitUserReadPerMinute', 'systemApiRateLimitUserWritePerMinute'],
  'cooldown-retest': ['cooldownAccountRetestMaxBackoffHours'],
  'data-retention': ['usageRecordRetentionDays', 'runtimeLogIndexRetentionDays', 'publicApiLogRetentionDays'],
  'log-retention': ['auditLogSuccessRetentionDays', 'auditLogProblemRetentionDays', 'auditLogSuccessHotRetentionHours', 'auditLogSuccessSampleRate', 'operationLogRetentionDays', 'operationLogMaxChangesPerRecord']
}

/** 字段 → 提交 section 的索引（overrides 子键归 gateway-core）。 */
export const fieldSectionIndex: Readonly<Record<string, ManagementSettingsSectionKey>> = (() => {
  const index: Record<string, ManagementSettingsSectionKey> = {}
  for (const [sectionKey, keys] of Object.entries(settingsSectionFields) as Array<[ManagementSettingsSectionKey, readonly string[]]>) {
    for (const key of keys) {
      if (key === 'upstreamClientVersionOverrides') continue
      index[key] = sectionKey
    }
  }
  for (const subKey of upstreamOverrideFieldKeys) index[`${OVERRIDE_PREFIX}${subKey}`] = 'gateway-core'
  return index
})()

/** 字段的出厂默认值（取消编辑回退与「恢复默认值」共用）。 */
export function settingDefaultValue(key: string): string | number {
  return defaultValueOf(key) ?? ''
}

/** 组内全部可编辑字段 key（含 overrides 子键展开）。 */
export function groupFieldKeys(group: SettingGroupDef): string[] {
  return group.fields
    .filter((entry): entry is SettingFieldDef => 'key' in entry)
    .map((entry) => entry.key)
}

/** 组自定义项计数（基于服务端 baseline）。 */
export function groupCustomCount(group: SettingGroupDef, access: SettingsDisplayAccess): number {
  return groupFieldKeys(group).filter((key) => access.isCustom(key)).length
}

/** 表单值读写（SettingsView 侧的 form 绑定辅助）。 */
export function getFormValue(globalForm: GlobalForm, systemForm: SystemForm, key: string): string | number {
  if (key === 'appName') return globalForm.appName
  if (key === 'appIcon') return globalForm.appIcon
  if (key.startsWith(OVERRIDE_PREFIX)) {
    return systemForm.upstreamClientVersionOverrides[key.slice(OVERRIDE_PREFIX.length) as UpstreamClientVersionKey] ?? ''
  }
  return (systemForm as unknown as Record<string, string | number>)[key] ?? 0
}

export function setFormValue(globalForm: GlobalForm, systemForm: SystemForm, key: string, value: string | number): void {
  if (key === 'appName') {
    globalForm.appName = String(value)
    return
  }
  if (key === 'appIcon') {
    globalForm.appIcon = String(value)
    return
  }
  if (key.startsWith(OVERRIDE_PREFIX)) {
    systemForm.upstreamClientVersionOverrides[key.slice(OVERRIDE_PREFIX.length) as UpstreamClientVersionKey] = String(value)
    return
  }
  ;(systemForm as unknown as Record<string, string | number>)[key] = value
}
