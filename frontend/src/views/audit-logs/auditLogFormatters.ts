import type { AuditOutcome, AuditPayloadPartType, AuditTrafficSource } from '@/types/domain'
import { displayGroupName } from '@/shared/groupLabelCache'
import { formatMillisecondsAsSeconds } from '@/shared/formatters'

export { formatDateTime } from '@/shared/formatters'

export function outcomeText(value: AuditOutcome): string {
  return {
    success: '成功',
    success_after_retry: '重试后成功',
    gateway_failed: '网关失败',
    upstream_failed: '上游失败',
    stream_failed: '流式失败',
    client_aborted: '客户端断开'
  }[value]
}

export function outcomeColor(value: AuditOutcome): string {
  if (value === 'success') return 'green'
  if (value === 'success_after_retry') return 'blue'
  if (value === 'client_aborted') return 'orange'
  return 'red'
}

export function payloadPartText(value: AuditPayloadPartType): string {
  return {
    client_request: '客户端请求',
    upstream_request: '上游请求',
    upstream_response: '上游响应',
    gateway_response: '返回客户端',
    gateway_error: '网关错误',
    gateway_metadata: '网关元信息'
  }[value]
}

export function trafficSourceText(value: AuditTrafficSource): string {
  return {
    gateway: '网关请求',
    manual_account_test: 'AI账户测试',
    cooldown_retest: '恢复探活'
  }[value] ?? '网关请求'
}

export function trafficSourceColor(value: AuditTrafficSource): string {
  if (value === 'cooldown_retest') return 'gold'
  if (value === 'manual_account_test') return 'cyan'
  return 'default'
}

export function statusColor(statusCode: number | undefined, success: boolean): string {
  if (success) return 'green'
  if (!statusCode) return 'default'
  return statusCode >= 500 ? 'red' : 'orange'
}

export function displayName(name?: string, _id?: string): string {
  return name || '-'
}

export function displayAuditGroupName(name?: string, id?: string): string {
  return displayGroupName(name, id)
}

export function formatDuration(value?: number): string {
  return formatMillisecondsAsSeconds(value)
}

export function formatBytes(value: number): string {
  if (value >= 1024 * 1024) return `${(value / 1024 / 1024).toFixed(2)} MB`
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${value} B`
}

export function prettyJson(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

export function formatHashPreview(value?: string): string {
  if (!value) return '-'
  return value.length > 8 ? `${value.slice(0, 4)}....${value.slice(-4)}` : value
}

export function compressionText(rawBytes: number, compressedBytes: number): string {
  if (!rawBytes) return '-'
  if (!compressedBytes || compressedBytes >= rawBytes) return '未压缩'
  const ratio = Math.max(0, Math.round((1 - compressedBytes / rawBytes) * 100))
  return `${formatBytes(compressedBytes)} / 节省 ${ratio}%`
}

export function captureStatusText(value?: string): string {
  return {
    complete: '完整',
    summary_only: '仅摘要',
    hash_only: '仅 Hash',
    expired: '已过期',
    overflow: '超限丢弃',
    dropped: '已丢弃'
  }[value || ''] ?? (value || '-')
}

export function normalizedStatusCode(value: string): number | undefined {
  const text = value.trim()
  if (!text) return undefined
  const statusCode = Number(text)
  return Number.isInteger(statusCode) && statusCode >= 100 && statusCode <= 599 ? statusCode : undefined
}
