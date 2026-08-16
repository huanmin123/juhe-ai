import type { NextFunction, Request, Response } from 'express'

const shanghaiTimeZone = 'Asia/Shanghai'
const rfc3339Pattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/
const shanghaiDisplayTimePattern = /^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?$/

const shanghaiDateTimeFormatter = new Intl.DateTimeFormat('en-CA', {
  timeZone: shanghaiTimeZone,
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hourCycle: 'h23'
})

export function formatShanghaiTime(value: Date | number | string): string {
  const date = value instanceof Date ? value : new Date(value)
  if (Number.isNaN(date.getTime())) throw new Error('无效时间值')
  const parts = Object.fromEntries(shanghaiDateTimeFormatter.formatToParts(date)
    .filter((part) => part.type !== 'literal')
    .map((part) => [part.type, part.value]))
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}.${String(date.getMilliseconds()).padStart(3, '0')}`
}

export function formatShanghaiNow(): string {
  return formatShanghaiTime(new Date())
}

export function displayTimeResponseMiddleware(_req: Request, res: Response, next: NextFunction): void {
  const originalJson = res.json.bind(res)
  res.json = ((body: unknown) => originalJson(formatResponseTimes(body))) as Response['json']
  next()
}

export function normalizeDisplayTimeRequestMiddleware(req: Request, _res: Response, next: NextFunction): void {
  if (req.body && typeof req.body === 'object') {
    req.body = normalizeRequestTimes(req.body)
  }
  next()
}

export function formatResponseTimes(value: unknown): unknown {
  if (value instanceof Date) return formatShanghaiTime(value)
  if (typeof value === 'string') {
    return rfc3339Pattern.test(value) ? formatShanghaiTime(value) : value
  }
  if (Array.isArray(value)) return value.map(formatResponseTimes)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, formatResponseTimes(item)]))
}

export function normalizeRequestTimes(value: unknown): unknown {
  if (typeof value === 'string') {
    const date = parseShanghaiDisplayTime(value)
    return date ? date.toISOString() : value
  }
  if (Array.isArray(value)) return value.map(normalizeRequestTimes)
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, normalizeRequestTimes(item)]))
}

export function parseShanghaiDisplayTime(value: string): Date | undefined {
  const match = shanghaiDisplayTimePattern.exec(value)
  if (!match) return undefined
  const [, yearText, monthText, dayText, hourText, minuteText, secondText, fractionText] = match
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  const hour = Number(hourText)
  const minute = Number(minuteText)
  const second = Number(secondText)
  if (
    month < 1 || month > 12
    || day < 1 || day > new Date(Date.UTC(year, month, 0)).getUTCDate()
    || hour > 23 || minute > 59 || second > 59
  ) return undefined
  const millisecond = Number((fractionText ?? '').padEnd(3, '0').slice(0, 3))
  return new Date(Date.UTC(year, month - 1, day, hour - 8, minute, second, millisecond))
}
