import type { UsageRecordSummary } from '@/types/domain'
import { systemAccountDisplayText } from '@/utils/systemAccountFilter'

export function displayName(name?: string, id?: string): string {
  if (name) return name
  return id ? '已删除或未知' : '-'
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
  return `${formatTokens(record.inputTokens)} / ${formatTokens(record.outputTokens)} / ${formatTokens(record.cacheReadTokens)}`
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
  return record.success ? '-' : '网络异常'
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
