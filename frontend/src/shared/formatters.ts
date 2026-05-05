import dayjs, { type Dayjs } from 'dayjs'

export function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

export function formatNumber(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

export function formatInteger(value?: number): string {
  return formatNumber(Math.round(value ?? 0))
}

export function formatCompactUsageAmount(value?: number): string {
  const amount = value ?? 0
  const absoluteValue = Math.abs(amount)
  if (absoluteValue >= 1_000_000_000) {
    return `${(amount / 1_000_000_000).toFixed(1)}B`
  }
  if (absoluteValue >= 1_000_000) {
    return `${(amount / 1_000_000).toFixed(1)}M`
  }
  if (absoluteValue >= 1_000) {
    return `${(amount / 1_000).toFixed(1)}K`
  }
  return formatNumber(amount)
}

export function formatUsd(value?: number, digits = 2): string {
  return `$${(value ?? 0).toFixed(digits)}`
}

export function parseDatePickerValue(value?: string): Dayjs | undefined {
  if (!value) return undefined
  const parsed = dayjs(value)
  return parsed.isValid() ? parsed : undefined
}

export function formatServerDateTimeInput(value?: Dayjs | null): string | null {
  return value ? value.format('YYYY-MM-DDTHH:mm:ss') : null
}
