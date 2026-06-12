import { formatMillisecondsAsSeconds } from '@/shared/formatters'

export function formatInteger(value?: number) {
  return new Intl.NumberFormat('zh-CN').format(Math.round(value ?? 0))
}

export function formatCompactInteger(value?: number) {
  return compactNumber(Math.round(value ?? 0))
}

export function formatCost(value?: number) {
  return `$${(value ?? 0).toFixed(4)}`
}

export function formatPercent(value?: number) {
  if (value === undefined) return '-'
  return `${value.toFixed(1)}%`
}

export function formatDuration(value?: number) {
  return formatMillisecondsAsSeconds(value)
}

export function formatDurationSeconds(value?: number) {
  return formatMillisecondsAsSeconds(value)
}

export function formatSeconds(value?: number) {
  return value === undefined ? '未知' : `${Math.round(value)} 秒`
}

export function bytesPerSecondToMbps(value?: number) {
  return value === undefined ? null : (value * 8) / 1_000_000
}

export function formatNetworkRateFromMbps(value?: number) {
  return value === undefined ? '-' : `${value.toFixed(value >= 10 ? 1 : 2)} Mbps`
}

export function formatHourLabel(value: string) {
  const dateMatch = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (dateMatch) return `${dateMatch[2]}-${dateMatch[3]}`
  const minuteMatch = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value)
  if (minuteMatch) {
    return minuteMatch[4] === '00' && minuteMatch[5] === '00'
      ? `${minuteMatch[2]}-${minuteMatch[3]} 00:00`
      : `${minuteMatch[4]}:${minuteMatch[5]}`
  }
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2})$/.exec(value)
  if (!match) return value
  return match[4] === '00' ? `${match[2]}-${match[3]} 00:00` : `${match[4]}:00`
}

export function axisNumberLabel(value: number) {
  return compactNumber(value)
}

export function compactNumber(value: number) {
  const absolute = Math.abs(value)
  if (absolute >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (absolute >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (absolute >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return `${Math.round(value)}`
}
