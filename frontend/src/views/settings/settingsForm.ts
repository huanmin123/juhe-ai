import type { GlobalSettings, SystemSettings, SystemSettingsPatch } from '@/types/domain'
import { defaultAppBrand } from '@/composables/useAppBrand'

export interface GlobalForm {
  appName: string
  appIcon: string
}

export type UpstreamClientVersionKey = 'codex' | 'claudeCode' | 'geminiCLI' | 'zcode' | 'grokCLI'

export type UpstreamClientVersionOverridesForm = Record<UpstreamClientVersionKey, string>

export interface SystemForm {
  gatewayTextRawBodyLimitMegabytes: number
  accountCircuitConfirmationFailuresRequired: number
  gatewayUserRequestLimitPerMinute: number
  gatewayUserRequestLimitPerDay: number
  gatewayUserRequestLimitPerWeek: number
  gatewayUserRequestLimitPerMonth: number
  userAiAccountLimit: number
  systemApiRateLimitIpReadPerMinute: number
  systemApiRateLimitIpReadBurstPer10Seconds: number
  systemApiRateLimitIpWritePerMinute: number
  systemApiRateLimitIpWriteBurstPer10Seconds: number
  systemApiRateLimitUserReadPerMinute: number
  systemApiRateLimitUserWritePerMinute: number
  defaultTemporaryUnschedulableMinutes: number
  temporaryUnschedulableRetryIntervalSeconds: number
  temporaryUnschedulableRetryAttempts: number
  textFirstResponseTimeoutSeconds: number
  textNonStreamFirstResponseTimeoutSeconds: number
  textStreamIdleTimeoutSeconds: number
  textUncommittedAttemptMaxLifetimeSeconds: number
  imageFirstResponseTimeoutSeconds: number
  imageStreamIdleTimeoutSeconds: number
  imageUncommittedAttemptMaxLifetimeSeconds: number
  imageRequestWallTimeoutSeconds: number
  chatImageGenerationTotalTimeoutSeconds: number
  noAvailableAccountWaitTimeoutSeconds: number
  streamFailureThresholdCount: number
  streamFailureThresholdWindowMinutes: number
  accountHealthCheckIntervalHours: number
  accountHealthCheckJitterMinutes: number
  accountHealthCheckFailureThreshold: number
  runtimeLogIndexRetentionDays: number
  publicApiLogRetentionDays: number
  usageRecordRetentionDays: number
  cooldownAccountRetestMaxBackoffHours: number
  operationLogRetentionDays: number
  operationLogMaxChangesPerRecord: number
  auditLogSuccessRetentionDays: number
  auditLogProblemRetentionDays: number
  auditLogSuccessHotRetentionHours: number
  auditLogSuccessSampleRate: number
  upstreamClientVersionOverrides: UpstreamClientVersionOverridesForm
}

export const defaultGlobalSettings: GlobalForm = {
  appName: defaultAppBrand.appName,
  appIcon: defaultAppBrand.appIcon
}

export const defaultSystemSettings: SystemForm = {
  gatewayTextRawBodyLimitMegabytes: 16,
  accountCircuitConfirmationFailuresRequired: 2,
  gatewayUserRequestLimitPerMinute: 0,
  gatewayUserRequestLimitPerDay: 0,
  gatewayUserRequestLimitPerWeek: 0,
  gatewayUserRequestLimitPerMonth: 0,
  userAiAccountLimit: 100,
  systemApiRateLimitIpReadPerMinute: 600,
  systemApiRateLimitIpReadBurstPer10Seconds: 120,
  systemApiRateLimitIpWritePerMinute: 180,
  systemApiRateLimitIpWriteBurstPer10Seconds: 40,
  systemApiRateLimitUserReadPerMinute: 300,
  systemApiRateLimitUserWritePerMinute: 120,
  defaultTemporaryUnschedulableMinutes: 2,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  textFirstResponseTimeoutSeconds: 120,
  textNonStreamFirstResponseTimeoutSeconds: 600,
  textStreamIdleTimeoutSeconds: 30,
  textUncommittedAttemptMaxLifetimeSeconds: 1800,
  imageFirstResponseTimeoutSeconds: 600,
  imageStreamIdleTimeoutSeconds: 120,
  imageUncommittedAttemptMaxLifetimeSeconds: 3600,
  imageRequestWallTimeoutSeconds: 3600,
  chatImageGenerationTotalTimeoutSeconds: 900,
  noAvailableAccountWaitTimeoutSeconds: 270,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5,
  accountHealthCheckIntervalHours: 1,
  accountHealthCheckJitterMinutes: 10,
  accountHealthCheckFailureThreshold: 3,
  runtimeLogIndexRetentionDays: 14,
  publicApiLogRetentionDays: 30,
  usageRecordRetentionDays: 30,
  cooldownAccountRetestMaxBackoffHours: 12,
  operationLogRetentionDays: 365,
  operationLogMaxChangesPerRecord: 100,
  auditLogSuccessRetentionDays: 3,
  auditLogProblemRetentionDays: 7,
  auditLogSuccessHotRetentionHours: 1,
  auditLogSuccessSampleRate: 1,
  upstreamClientVersionOverrides: { codex: '', claudeCode: '', geminiCLI: '', zcode: '', grokCLI: '' }
}

/** 返回嵌套对象不共享引用的默认 SystemForm，供 reactive 初始化与重置使用。 */
export function createDefaultSystemForm(): SystemForm {
  return { ...defaultSystemSettings, upstreamClientVersionOverrides: { ...defaultSystemSettings.upstreamClientVersionOverrides } }
}

export function normalizeGlobalSettings(settings: GlobalSettings | GlobalForm): GlobalForm {
  return {
    appName: requiredStringValue(settings.appName, '系统名称'),
    appIcon: requiredStringValue(settings.appIcon, '系统图标路径')
  }
}

export function normalizeSystemSettings(settings: SystemSettings | SystemForm): SystemForm {
  return {
    gatewayTextRawBodyLimitMegabytes: integerValue(settings.gatewayTextRawBodyLimitMegabytes, '文本请求体上限', 1, 64),
    accountCircuitConfirmationFailuresRequired: integerValue(settings.accountCircuitConfirmationFailuresRequired, '账户电路独立确认失败次数', 1, 5),
    gatewayUserRequestLimitPerMinute: integerValue(settings.gatewayUserRequestLimitPerMinute, '用户每分钟请求上限', 0, 1_000_000_000),
    gatewayUserRequestLimitPerDay: integerValue(settings.gatewayUserRequestLimitPerDay, '用户每日请求上限', 0, 1_000_000_000),
    gatewayUserRequestLimitPerWeek: integerValue(settings.gatewayUserRequestLimitPerWeek, '用户每周请求上限', 0, 1_000_000_000),
    gatewayUserRequestLimitPerMonth: integerValue(settings.gatewayUserRequestLimitPerMonth, '用户每月请求上限', 0, 1_000_000_000),
    userAiAccountLimit: integerValue(settings.userAiAccountLimit, '用户 AI 账户数量上限', 0, 1_000_000),
    systemApiRateLimitIpReadPerMinute: integerValue(settings.systemApiRateLimitIpReadPerMinute, 'IP 读请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitIpReadBurstPer10Seconds: integerValue(settings.systemApiRateLimitIpReadBurstPer10Seconds, 'IP 读请求突发上限', 0, 1_000_000),
    systemApiRateLimitIpWritePerMinute: integerValue(settings.systemApiRateLimitIpWritePerMinute, 'IP 写请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitIpWriteBurstPer10Seconds: integerValue(settings.systemApiRateLimitIpWriteBurstPer10Seconds, 'IP 写请求突发上限', 0, 1_000_000),
    systemApiRateLimitUserReadPerMinute: integerValue(settings.systemApiRateLimitUserReadPerMinute, '登录用户读请求每分钟上限', 0, 1_000_000),
    systemApiRateLimitUserWritePerMinute: integerValue(settings.systemApiRateLimitUserWritePerMinute, '登录用户写请求每分钟上限', 0, 1_000_000),
    defaultTemporaryUnschedulableMinutes: integerValue(settings.defaultTemporaryUnschedulableMinutes, '临时不可调用最大暂停时间', 1, 1440),
    temporaryUnschedulableRetryIntervalSeconds: integerValue(settings.temporaryUnschedulableRetryIntervalSeconds, '安全原地重试间隔', 0, 3600),
    temporaryUnschedulableRetryAttempts: integerValue(settings.temporaryUnschedulableRetryAttempts, '安全原地重试次数', 0, 10),
    textFirstResponseTimeoutSeconds: integerValue(settings.textFirstResponseTimeoutSeconds, '文本首响应等待上限', 10, 3600),
    textNonStreamFirstResponseTimeoutSeconds: integerValue(settings.textNonStreamFirstResponseTimeoutSeconds, '非流式响应等待上限', 10, 3600),
    textStreamIdleTimeoutSeconds: integerValue(settings.textStreamIdleTimeoutSeconds, '文本流式停顿上限', 1, 3600),
    textUncommittedAttemptMaxLifetimeSeconds: integerValue(settings.textUncommittedAttemptMaxLifetimeSeconds, '文本未提交尝试最大存活时间', 60, 86400),
    imageFirstResponseTimeoutSeconds: integerValue(settings.imageFirstResponseTimeoutSeconds, '图像首响应等待上限', 10, 3600),
    imageStreamIdleTimeoutSeconds: integerValue(settings.imageStreamIdleTimeoutSeconds, '图像流式停顿上限', 1, 3600),
    imageUncommittedAttemptMaxLifetimeSeconds: integerValue(settings.imageUncommittedAttemptMaxLifetimeSeconds, '图像未提交尝试最大存活时间', 60, 86400),
    imageRequestWallTimeoutSeconds: integerValue(settings.imageRequestWallTimeoutSeconds, '图像整请求总时限', 60, 86400),
    chatImageGenerationTotalTimeoutSeconds: integerValue(settings.chatImageGenerationTotalTimeoutSeconds, 'AI 对话生图总超时', 60, 86400),
    noAvailableAccountWaitTimeoutSeconds: integerValue(settings.noAvailableAccountWaitTimeoutSeconds, '无可用账号等待上限', 10, 3600),
    streamFailureThresholdCount: integerValue(settings.streamFailureThresholdCount, '流失败诊断计数', 1, 100),
    streamFailureThresholdWindowMinutes: integerValue(settings.streamFailureThresholdWindowMinutes, '流失败诊断窗口', 1, 1440),
    accountHealthCheckIntervalHours: integerValue(settings.accountHealthCheckIntervalHours, '正常账号健康检测间隔', 1, 168),
    accountHealthCheckJitterMinutes: integerValue(settings.accountHealthCheckJitterMinutes, '健康检测错峰窗口', 0, 1440),
    accountHealthCheckFailureThreshold: integerValue(settings.accountHealthCheckFailureThreshold, '健康检测连续失败阈值', 1, 10),
    runtimeLogIndexRetentionDays: integerValue(settings.runtimeLogIndexRetentionDays, '运行日志索引保留天数', 1, 90),
    publicApiLogRetentionDays: integerValue(settings.publicApiLogRetentionDays, '公开接口日志保留天数', 1, 365),
    usageRecordRetentionDays: integerValue(settings.usageRecordRetentionDays, '使用记录保留天数', 1, 180),
    cooldownAccountRetestMaxBackoffHours: integerValue(settings.cooldownAccountRetestMaxBackoffHours, '长期不可用观察阈值', 1, 720),
    operationLogRetentionDays: integerValue(settings.operationLogRetentionDays, '操作日志保留天数', 1, 3650),
    operationLogMaxChangesPerRecord: integerValue(settings.operationLogMaxChangesPerRecord, '操作日志每条变更记录上限', 1, 500),
    auditLogSuccessRetentionDays: integerValue(settings.auditLogSuccessRetentionDays, '审计成功保留天数', 0, 3650),
    auditLogProblemRetentionDays: integerValue(settings.auditLogProblemRetentionDays, '审计问题保留天数', 1, 3650),
    auditLogSuccessHotRetentionHours: integerValue(settings.auditLogSuccessHotRetentionHours, '审计成功热窗', 0, 168),
    auditLogSuccessSampleRate: decimalValue(settings.auditLogSuccessSampleRate, '审计成功采样率必须是 0 到 1 之间且最多 4 位小数', 0, 1, 4),
    upstreamClientVersionOverrides: parseUpstreamClientVersionOverrides(settings.upstreamClientVersionOverrides)
  }
}

export function buildGlobalSettingsPayload(form: GlobalForm): GlobalSettings {
  return normalizeGlobalSettings(form)
}

export function buildSystemSettingsPayload(form: SystemForm): SystemSettingsPatch {
  const normalized = normalizeSystemSettings(form)
  return { ...normalized, upstreamClientVersionOverrides: serializeUpstreamClientVersionOverrides(normalized.upstreamClientVersionOverrides) }
}

const upstreamClientVersionKeys: readonly UpstreamClientVersionKey[] = ['codex', 'claudeCode', 'geminiCLI', 'zcode', 'grokCLI']

const upstreamClientVersionLabels: Record<UpstreamClientVersionKey, string> = {
  codex: 'Codex Desktop 版本覆盖',
  claudeCode: 'Claude Code 版本覆盖',
  geminiCLI: 'Gemini CLI 版本覆盖',
  zcode: 'ZCode 版本覆盖',
  grokCLI: 'Grok CLI 版本覆盖'
}

const semverPattern = /^\d+\.\d+\.\d+$/

/** 解析（加载方向）：五个家族键只取合法 semver 或空，其余输入整体回退全空。 */
export function parseUpstreamClientVersionOverrides(value: unknown): UpstreamClientVersionOverridesForm {
  const result: UpstreamClientVersionOverridesForm = { codex: '', claudeCode: '', geminiCLI: '', zcode: '', grokCLI: '' }
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return result
  const source = value as Record<string, unknown>
  for (const key of upstreamClientVersionKeys) {
    const raw = source[key]
    if (typeof raw !== 'string') continue
    const trimmed = raw.trim()
    result[key] = semverPattern.test(trimmed) ? trimmed : ''
  }
  return result
}

/** 序列化（保存方向）：非空字段必须是合法 semver，否则抛出中文错误；空字段剔除，全空存储 {}。 */
export function serializeUpstreamClientVersionOverrides(form: UpstreamClientVersionOverridesForm): Record<string, string> {
  const result: Record<string, string> = {}
  for (const key of upstreamClientVersionKeys) {
    const trimmed = String(form[key] ?? '').trim()
    if (!trimmed) continue
    if (!semverPattern.test(trimmed)) {
      throw new Error(`${upstreamClientVersionLabels[key]}必须是 x.y.z 格式的三段版本号`)
    }
    result[key] = trimmed
  }
  return result
}

function integerValue(value: unknown, label: string, min: number, max: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`${label}必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`${label}必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

/** 小数数值校验：范围内且小数位数不超过 maxFractionDigits；任何非法输入都抛出给定完整文案。 */
function decimalValue(value: unknown, errorMessage: string, min: number, max: number, maxFractionDigits: number): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) throw new Error(errorMessage)
  if (value < min || value > max) throw new Error(errorMessage)
  if (Math.round(value * 10 ** maxFractionDigits) / 10 ** maxFractionDigits !== value) throw new Error(errorMessage)
  return value
}

function requiredStringValue(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}
