import dayjs, { type Dayjs } from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'

dayjs.extend(relativeTime)

const serverDateTimePattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{3})?Z$/

export function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = strictServerDateTime(value)
  if (!date) return '时间格式异常'
  return date.toLocaleString('zh-CN', { hour12: false })
}

export function serverDateTimeTimestamp(value?: string): number | undefined {
  if (!value) return undefined
  return strictServerDateTime(value)?.getTime()
}

export function formatRelativeDateTime(value?: string): string {
  if (!value) return '-'
  const date = strictServerDateTime(value)
  if (!date) return '时间格式异常'
  const parsed = dayjs(date)
  return `${parsed.fromNow()} ${parsed.format('YYYY-MM-DD HH:mm')}`
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

export function parseStrictDatePickerValue(value?: string, label = '时间'): Dayjs | undefined {
  if (!value) return undefined
  if (!serverDateTimePattern.test(value)) {
    throw new Error(`${label}格式异常，请清理后再编辑`)
  }
  const parsed = dayjs(value)
  const expectedIso = value.includes('.') ? value : value.replace('Z', '.000Z')
  if (!parsed.isValid() || parsed.toDate().toISOString() !== expectedIso) {
    throw new Error(`${label}无效，请清理后再编辑`)
  }
  return parsed
}

export function formatServerDateTimeInput(value?: Dayjs | null): string | null {
  return value ? value.toDate().toISOString() : null
}

function strictServerDateTime(value: string): Date | undefined {
  if (!serverDateTimePattern.test(value)) return undefined
  const date = new Date(value)
  const expectedIso = value.includes('.') ? value : value.replace('Z', '.000Z')
  return Number.isNaN(date.getTime()) || date.toISOString() !== expectedIso ? undefined : date
}
