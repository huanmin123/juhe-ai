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

export function isAnthropicUsageRecord(record: UsageRecordSummary): boolean {
  return record.usageSemantic === 'anthropic' || record.providerCode === 'anthropic'
}

export function usageRecordTokenParts(record: UsageRecordListItem): string[] {
  return [
    `输入 ${formatTokens(record.inputTokens)}`,
    `输出 ${formatTokens(record.outputTokens)}`,
    `缓存读 ${formatTokens(record.cacheReadTokens)}`
  ]
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

export function usageRecordDisplayCostUsd(record: UsageRecordListItem): number | undefined {
  return record.costUsd
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

export function usageRecordLatencyParts(record: UsageRecordListItem): string[] {
  return [
    `首 token ${formatDuration(record.firstTokenMs)}`,
    `总耗时 ${formatDuration(record.durationMs)}`
  ]
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
  if (typeof record.statusCode === 'number') {
    return !record.success && record.statusCode >= 200 && record.statusCode < 300
      ? `HTTP ${record.statusCode}（非成功终态）`
      : `HTTP ${record.statusCode}`
  }
  return '-'
}

export function usageRecordFailureAttributionText(record: UsageRecordListItem): string | undefined {
  if (record.success) return undefined
  if (record.failureAttribution === 'downstream_unconfirmed') return '归因：下游连接关闭，触发方未知'
  if (record.failureAttribution === 'client_lifecycle') return '归因：下游连接关闭（历史记录，触发方未识别）'
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
    cooldown_retest: '冷却账户复测',
    hybrid_scoring: '混合路由选型',
    hybrid_quality_scoring: '回答质量复核'
  }[record.trafficSource] ?? '网关请求'
}

export function trafficSourceColor(record: UsageRecordListItem): string {
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

export interface UsageRecordCodexGuardStatus {
  label: '已修复' | '协议异常'
  detail: string
}

export function usageRecordCodexGuardStatus(record: UsageRecordSummary): UsageRecordCodexGuardStatus | undefined {
  if (!record.success) return undefined
  const value = record.responseSnapshot?.codexResponsesGuard
  if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
  const guard = value as Record<string, unknown>
  const outcome = typeof guard.outcome === 'string' ? guard.outcome : ''
  if (outcome !== 'repaired_safe' && outcome !== 'repaired_bridge' && outcome !== 'observed_unknown') return undefined
  const codes = Array.isArray(guard.diagnosticCodes)
    ? guard.diagnosticCodes.filter((item): item is string => typeof item === 'string').slice(0, 8)
    : []
  const rules = Array.isArray(guard.repairRuleIds)
    ? guard.repairRuleIds.filter((item): item is string => typeof item === 'string').slice(0, 8)
    : []
  const detail = [
    outcome === 'observed_unknown' ? '检测到未识别的 Responses 类型，已透传并记录' : '响应已在网关复制后完成安全修复',
    rules.length ? `修复规则：${rules.join(', ')}` : '',
    codes.length ? `诊断码：${codes.join(', ')}` : ''
  ].filter(Boolean).join('；')
  return {
    label: outcome === 'observed_unknown' ? '协议异常' : '已修复',
    detail
  }
}

export function usageRecordSystemAccountText(record: UsageRecordListItem): string {
  return systemAccountDisplayText(record)
}
