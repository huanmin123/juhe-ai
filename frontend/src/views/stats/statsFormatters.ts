import { formatMillisecondsAsSeconds } from '@/shared/formatters'

export function formatInteger(value?: unknown) {
  return new Intl.NumberFormat('zh-CN').format(Math.round(numberValue(value) ?? 0))
}

export function formatCompactInteger(value?: unknown) {
  return compactNumber(Math.round(numberValue(value) ?? 0))
}

export function formatCost(value?: unknown) {
  return `$${(numberValue(value) ?? 0).toFixed(4)}`
}

export function formatPercent(value?: unknown) {
  const numericValue = numberValue(value)
  if (numericValue === undefined) return '-'
  return `${numericValue.toFixed(1)}%`
}

export function formatDuration(value?: unknown) {
  return formatMillisecondsAsSeconds(numberValue(value))
}

export function formatDurationSeconds(value?: unknown) {
  return formatMillisecondsAsSeconds(numberValue(value))
}

export function bytesPerSecondToMbps(value?: unknown) {
  const numericValue = numberValue(value)
  return numericValue === undefined ? null : (numericValue * 8) / 1_000_000
}

export function bytesToMiB(value?: unknown) {
  const numericValue = numberValue(value)
  return numericValue === undefined ? null : numericValue / 1024 / 1024
}

export function formatBytesMiB(value?: unknown) {
  const mib = bytesToMiB(value)
  if (mib === null) return '-'
  return `${mib.toFixed(mib >= 100 ? 0 : 1)} MB`
}

export function formatNetworkRateFromMbps(value?: unknown) {
  const numericValue = numberValue(value)
  return numericValue === undefined ? '-' : `${numericValue.toFixed(numericValue >= 10 ? 1 : 2)} Mbps`
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

function numberValue(value: unknown): number | undefined {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : undefined
}
