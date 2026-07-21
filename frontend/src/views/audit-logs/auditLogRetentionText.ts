import type { AuditLogRuntime } from '@/types/domain'

type AuditRetentionSettings = AuditLogRuntime['settings']

export function auditLogEmptyDescription(settings?: AuditRetentionSettings): string {
  if (!settings) return '暂无审计日志。'
  if (!settings.enabled) return '原始审计已通过部署配置临时关闭。'
  const samplePercent = settings.successSampleRate * 100
  const keepsLongTermSamples = samplePercent > 0 && settings.successRetentionDays > 0
  const sampleText = keepsLongTermSamples
    ? `按 ${formatPercentage(samplePercent)}% 稳定采样长期保留`
    : '不长期保留成功样本'
  if (settings.successHotRetentionHours > 0) {
    return `暂无审计日志。问题请求会全量记录，成功请求最近 ${settings.successHotRetentionHours} 小时全量保留，热窗口结束后${sampleText}。`
  }
  if (!keepsLongTermSamples) {
    return '暂无审计日志。问题请求会全量记录，成功请求当前不记录。'
  }
  return `暂无审计日志。问题请求会全量记录，成功请求${sampleText}。`
}

function formatPercentage(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(2).replace(/0+$/, '').replace(/\.$/, '')
}
