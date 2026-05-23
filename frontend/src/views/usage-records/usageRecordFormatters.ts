import type { UsageRecordSummary } from '@/types/domain'
import { displayGroupName } from '@/shared/groupLabelCache'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'

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

export function formatTokens(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

export function formatRecordTokens(record: UsageRecordSummary): string {
  const base = `${formatTokens(record.inputTokens)} / ${formatTokens(record.outputTokens)} / ${formatTokens(record.cacheReadTokens)}`
  const imageTokens = (record.inputImageTokens ?? 0) + (record.outputImageTokens ?? 0)
  return imageTokens > 0 ? `${base} / 图片 ${formatTokens(imageTokens)}` : base
}

export function formatEndpoint(value?: string): string {
  return value ?? '-'
}

export function formatCost(value?: number): string {
  if (!value) return '$0.000000'
  return `$${value.toFixed(6)}`
}

export function formatUnitPrice(value?: number): string {
  return typeof value === 'number' ? `$${value.toFixed(4)} / 1M Token` : '-'
}

export function formatCacheRate(record: UsageRecordSummary): string {
  const inputTokens = record.inputTokens ?? 0
  if (inputTokens <= 0) return '0.0%'
  return `${(((record.cacheReadTokens ?? 0) / inputTokens) * 100).toFixed(1)}%`
}

export function formatDuration(value?: number): string {
  return typeof value === 'number' ? `${(value / 1000).toFixed(2)} s` : '-'
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

export function trafficSourceText(record: UsageRecordSummary): string {
  return {
    gateway: '网关请求',
    manual_account_test: '账号测试',
    cooldown_retest: '恢复探活'
  }[record.trafficSource] ?? '网关请求'
}

export function trafficSourceColor(record: UsageRecordSummary): string {
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

export function formatDateTime(value?: string): string {
  if (!value) return '-'
  return new Date(value).toLocaleString('zh-CN', { hour12: false })
}

export function usageRecordSystemAccountText(record: UsageRecordSummary): string {
  return systemAccountDisplayText(record)
}
