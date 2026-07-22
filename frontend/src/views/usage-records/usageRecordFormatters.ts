import type { UsageRecordSummary } from '@/types/domain'
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

export function accountDisplayText(record: UsageRecordSummary): string {
  if (record.accountName) return record.accountName
  if (record.accountId) return '已删除或未知'
  if (!record.success) return '未分配账号'
  return '-'
}

export function formatTokens(value?: unknown): string {
  return new Intl.NumberFormat('zh-CN').format(numberValue(value) ?? 0)
}

export function isAnthropicUsageRecord(record: UsageRecordSummary): boolean {
  return record.usageSemantic === 'anthropic' || record.providerCode === 'anthropic'
}

export function usageRecordTokenParts(record: UsageRecordSummary): string[] {
  return [
    `输入 ${formatTokens(record.inputTokens)}`,
    `输出 ${formatTokens(record.outputTokens)}`,
    `缓存读 ${formatTokens(record.cacheReadTokens)}`
  ]
}

export function formatRecordTokens(record: UsageRecordSummary): string {
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

export function usageRecordDisplayCostUsd(record: UsageRecordSummary): number | undefined {
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

export function usageRecordLatencyParts(record: UsageRecordSummary): string[] {
  return [
    `首 token ${formatDuration(record.firstTokenMs)}`,
    `总耗时 ${formatDuration(record.durationMs)}`
  ]
}

export function usageRecordServiceTierText(record: UsageRecordSummary): string | undefined {
  if (record.billedServiceTier === 'priority') return 'Priority'
  if (record.billedServiceTier === 'flex') return 'Flex'
  return undefined
}

export function usageRecordReasoningEffortText(record: UsageRecordSummary): string | undefined {
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

export function statusCodeColor(record: UsageRecordSummary): string {
  const value = record.statusCode
  if (!value) return 'default'
  if (value >= 200 && value < 300) return 'green'
  if (value >= 400 && value < 500) return 'orange'
  if (value >= 500) return 'red'
  return 'blue'
}

export function statusCodeText(record: UsageRecordSummary): string {
  if (typeof record.statusCode === 'number') return String(record.statusCode)
  return '-'
}

function numberValue(value: unknown): number | undefined {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : undefined
}

export function trafficSourceText(record: UsageRecordSummary): string {
  return {
    gateway: '网关请求',
    manual_account_test: '账号测试',
    account_health_check: '健康检查',
    runtime_recovery_probe: '运行态恢复探针',
    cooldown_retest: '恢复探活',
    hybrid_scoring: '混合评分',
    hybrid_quality_scoring: '混合质量评分'
  }[record.trafficSource] ?? '网关请求'
}

export function trafficSourceColor(record: UsageRecordSummary): string {
  if (record.trafficSource === 'hybrid_quality_scoring') return 'purple'
  if (record.trafficSource === 'hybrid_scoring') return 'blue'
  if (record.trafficSource === 'runtime_recovery_probe') return 'orange'
  if (record.trafficSource === 'account_health_check') return 'green'
  if (record.trafficSource === 'cooldown_retest') return 'gold'
  if (record.trafficSource === 'manual_account_test') return 'cyan'
  return 'default'
}

export function errorText(record: UsageRecordSummary): string {
  if (record.errorMessage) return record.errorMessage
  if (record.responseSnapshot) return JSON.stringify(record.responseSnapshot, null, 2)
  if (!record.accountId && !record.success) return '没有可调度的上游账号'
  return '-'
}

export function usageRecordSystemAccountText(record: UsageRecordSummary): string {
  return systemAccountDisplayText(record)
}
