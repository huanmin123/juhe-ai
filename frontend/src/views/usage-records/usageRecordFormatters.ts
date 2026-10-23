import type { UsageRecordListItem, UsageRecordSummary } from '@/types/domain'
import { displayGroupName } from '@/shared/groupLabelCache'
import { formatMillisecondsAsSeconds } from '@/shared/formatters'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'

export { formatDateTime } from '@/shared/formatters'

export function displayName(name?: string, id?: string): string {
  if (name) return name
  return id ? '已删除或未知' : '-'
}

export function displayUsageRecordGroupName(name?: string, id?: string): string {
  return displayGroupName(name, id)
}

export function accountDisplayText(record: UsageRecordListItem): string {
  if (record.accountName) return record.accountName
  if (record.accountId) return '已删除或未知'
  if (!record.success) return '未分配账号'
  return '-'
}

export function formatTokens(value?: unknown): string {
  return new Intl.NumberFormat('zh-CN').format(numberValue(value) ?? 0)
}

/** 缓存读缩写：≥1M 显示 X.XM、≥1K 显示 X.XK，避免大缓存数字撑宽用量列。 */
export function formatCacheTokens(tokens?: unknown): string {
  const value = numberValue(tokens) ?? 0
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return formatTokens(value)
}

export function isAnthropicUsageRecord(record: UsageRecordSummary): boolean {
  return record.usageSemantic === 'anthropic' || record.providerCode === 'anthropic'
}

export function usageRecordTokenParts(record: Pick<UsageRecordListItem,
  'inputTokens' | 'outputTokens' | 'cacheReadTokens'>): string[] {
  return [
    `输入 ${formatTokens(record.inputTokens)}`,
    `输出 ${formatTokens(record.outputTokens)}`,
    `缓存读 ${formatTokens(record.cacheReadTokens)}`
  ]
}

/** 媒体秒数一位小数（M4a 媒体计量口径）。 */
export function formatMediaSeconds(value?: unknown): string {
  const numericValue = numberValue(value)
  if (!numericValue) return '0.0'
  return numericValue.toFixed(1)
}

/** 媒体计量部件：仅展示非零维度（音频 token 千分位、TTS 字符千分位、秒一位小数）。 */
export function usageRecordMediaParts(record: Pick<UsageRecordListItem,
  'inputAudioTokens' | 'outputAudioTokens' | 'ttsInputChars' | 'audioInputSeconds' | 'outputVideoSeconds'>): string[] {
  const parts: string[] = []
  const inputAudioTokens = numberValue(record.inputAudioTokens) ?? 0
  const outputAudioTokens = numberValue(record.outputAudioTokens) ?? 0
  const ttsInputChars = numberValue(record.ttsInputChars) ?? 0
  const audioInputSeconds = numberValue(record.audioInputSeconds) ?? 0
  const outputVideoSeconds = numberValue(record.outputVideoSeconds) ?? 0
  if (inputAudioTokens > 0) parts.push(`音入 ${formatTokens(inputAudioTokens)}`)
  if (outputAudioTokens > 0) parts.push(`音出 ${formatTokens(outputAudioTokens)}`)
  if (ttsInputChars > 0) parts.push(`字符 ${formatTokens(ttsInputChars)}`)
  if (audioInputSeconds > 0) parts.push(`音秒 ${formatMediaSeconds(audioInputSeconds)}`)
  if (outputVideoSeconds > 0) parts.push(`视频秒 ${formatMediaSeconds(outputVideoSeconds)}`)
  return parts
}

/** 记录是否存在任一媒体计量（全零或缺失时表格显示占位符）。 */
export function usageRecordHasMediaMetering(record: Pick<UsageRecordListItem,
  'inputAudioTokens' | 'outputAudioTokens' | 'ttsInputChars' | 'audioInputSeconds' | 'outputVideoSeconds'>): boolean {
  return usageRecordMediaParts(record).length > 0
}

export function formatRecordTokens(record: UsageRecordListItem): string {
  return usageRecordTokenParts(record).join(' / ')
}

export function formatEndpoint(value?: string): string {
  return value ?? '-'
}

export function formatCost(value?: unknown): string {
  const numericValue = numberValue(value)
  if (!numericValue) return '$0.000000'
  return `$${numericValue.toFixed(6)}`
}

export function usageRecordDisplayCostUsd(record: Pick<UsageRecordSummary, 'costUsd' | 'costBreakdown'>): number | undefined {
  return record.costUsd ?? record.costBreakdown?.accountChargeUsd
}

export function formatUnitPrice(value?: unknown): string {
  const numericValue = numberValue(value)
  return numericValue === undefined ? '-' : `$${numericValue.toFixed(4)} / 1M Token`
}

export function formatCacheRate(record: UsageRecordSummary): string {
  const inputTokens = numberValue(record.inputTokens) ?? 0
  const cacheReadTokens = numberValue(record.cacheReadTokens) ?? 0
  const cacheWriteTokens = numberValue(record.cacheWriteTokens) ?? 0
  const denominator = isAnthropicUsageRecord(record) || cacheReadTokens > inputTokens
    ? inputTokens + cacheReadTokens + cacheWriteTokens
    : inputTokens
  if (denominator <= 0) return '0.0%'
  return `${((cacheReadTokens / denominator) * 100).toFixed(1)}%`
}

export function formatDuration(value?: unknown): string {
  return formatMillisecondsAsSeconds(numberValue(value))
}

/**
 * 压缩请求识别：以 `compactionExpected` 字段为主判定（后端单点判定后全链路透传，
 * 覆盖显式压缩路径与 `/responses` 内联压缩触发器形态）；endpoint 推断仅用于回退
 * 识别字段上线前的存量行（POST 且路径以 `/responses/compact` 结尾）。
 */
export function usageRecordIsCompaction(record: Pick<UsageRecordListItem, 'compactionExpected' | 'endpoint'>): boolean {
  if (record.compactionExpected === true) return true
  return /^POST \S*\/responses\/compact\/?$/.test(record.endpoint?.trim() ?? '')
}

/**
 * 输出 TPS = 输出 Token ÷ 完整耗时（含首字等待，口径偏保守且与 AI 性能监控一致）。
 * 媒体计量、输出 ≤ 0 或耗时缺失/非正的记录显示占位符。
 */
export function formatUsageOutputTps(record: Pick<UsageRecordListItem,
  'outputTokens' | 'durationMs' |
  'inputAudioTokens' | 'outputAudioTokens' | 'ttsInputChars' | 'audioInputSeconds' | 'outputVideoSeconds'>): string {
  if (usageRecordHasMediaMetering(record)) return '—'
  const outputTokens = numberValue(record.outputTokens)
  const durationMs = numberValue(record.durationMs)
  if (!outputTokens || outputTokens <= 0 || !durationMs || durationMs <= 0) return '—'
  return `${(outputTokens * 1000 / durationMs).toFixed(1)} t/s`
}

/** 延迟摘要行：首字 / 总耗时恒有两行，TPS 仅在可计算时追加第三行。 */
export function usageRecordLatencySummaryParts(record: UsageRecordListItem): string[] {
  const parts = [
    `首字 ${formatDuration(record.firstTokenMs)}`,
    `总耗时 ${formatDuration(record.durationMs)}`
  ]
  const tps = formatUsageOutputTps(record)
  if (tps !== '—') parts.push(`TPS ${tps}`)
  return parts
}

export function usageRecordServiceTierText(record: UsageRecordListItem): string | undefined {
  if (record.billedServiceTier === 'priority') return 'Priority'
  if (record.billedServiceTier === 'flex') return 'Flex'
  return undefined
}

export function usageRecordReasoningEffortText(record: UsageRecordListItem): string | undefined {
  const effort = record.effectiveReasoningEffort
  if (!effort) return undefined
  return {
    none: '不思考',
    minimal: '极低',
    low: '低',
    medium: '中',
    high: '高',
    xhigh: '超高',
    max: '最大'
  }[effort]
}

export function statusCodeColor(record: UsageRecordListItem): string {
  const value = record.statusCode
  if (!value) return 'default'
  if (!record.success && value >= 200 && value < 300) return 'orange'
  if (value >= 200 && value < 300) return 'green'
  if (value >= 400 && value < 500) return 'orange'
  if (value >= 500) return 'red'
  return 'blue'
}

export function statusCodeText(record: UsageRecordListItem): string {
  if (typeof record.statusCode === 'number') return String(record.statusCode)
  return '-'
}

export function usageRecordFailureAttributionText(record: UsageRecordListItem): string | undefined {
  if (record.success) return undefined
  if (record.failureAttribution === 'downstream_closed') return '下游连接关闭'
  if (record.failureAttribution === 'account_upstream') return '归因：上游账户'
  if (record.failureAttribution === 'account_dependency') return '归因：账户依赖'
  if (record.failureAttribution === 'opaque_upstream') return '归因：上游未识别失败'
  if (record.failureAttribution === 'gateway_capacity') return '归因：网关容量'
  if (record.failureAttribution === 'gateway_policy') return '归因：网关策略'
  return undefined
}

function numberValue(value: unknown): number | undefined {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : undefined
}

export function trafficSourceText(record: UsageRecordListItem): string {
  return {
    gateway: '网关请求',
    manual_account_test: '账号测试',
    account_health_check: '健康检查',
    runtime_recovery_probe: '快速恢复检测',
    cooldown_retest: '冷却账户复测'
  }[record.trafficSource] ?? '网关请求'
}

export function trafficSourceColor(record: UsageRecordListItem): string {
  if (record.trafficSource === 'runtime_recovery_probe') return 'orange'
  if (record.trafficSource === 'account_health_check') return 'green'
  if (record.trafficSource === 'cooldown_retest') return 'gold'
  if (record.trafficSource === 'manual_account_test') return 'cyan'
  return 'default'
}

export function errorText(record: UsageRecordSummary): string {
  if (record.errorMessage) {
    return record.errorMessage
  }
  if (record.responseSnapshot) return JSON.stringify(record.responseSnapshot, null, 2)
  if (!record.accountId && !record.success) return '没有可调度的上游账号'
  return '-'
}

export function usageRecordSystemAccountText(record: UsageRecordListItem): string {
  return systemAccountDisplayText(record)
}
