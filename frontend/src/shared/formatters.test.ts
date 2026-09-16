import { afterEach, describe, expect, it, vi } from 'vitest'
import dayjs from 'dayjs'

import {
  compareServerDateTime,
  formatCompactUsageAmount,
  formatDateTime,
  formatInteger,
  formatLocalTime,
  formatMillisecondsAsSeconds,
  formatNumber,
  formatRelativeDateTime,
  formatRequestCountTag,
  formatServerDateTimeInput,
  formatUngroupedInteger,
  formatUsd,
  parseStrictDatePickerValue,
  parseStrictServerDateTime,
  serverDateTimeTimestamp
} from './formatters'

// 断言本地时区格式化结果时，用 Date 本地方法构造期望值，保证任意时区下稳定。
function pad(value: number, width = 2): string {
  return String(value).padStart(width, '0')
}

function localDateTimeText(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} `
    + `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`
}

afterEach(() => {
  vi.useRealTimers()
})

describe('parseStrictServerDateTime', () => {
  it('接受 Z 与 ±HH:MM 时区及 1-9 位小数秒', () => {
    expect(parseStrictServerDateTime('2026-01-02T03:04:05Z')).toEqual(new Date('2026-01-02T03:04:05Z'))
    expect(parseStrictServerDateTime('2026-01-02T03:04:05.123Z')).toEqual(new Date('2026-01-02T03:04:05.123Z'))
    expect(parseStrictServerDateTime('2026-01-02T03:04:05.123456789+08:00')).toEqual(new Date('2026-01-02T03:04:05.123456789+08:00'))
    expect(parseStrictServerDateTime('2026-01-02T03:04:05-05:30')).toEqual(new Date('2026-01-02T03:04:05-05:30'))
  })

  it('缺少时区或格式不符返回 undefined', () => {
    expect(parseStrictServerDateTime('2026-01-02T03:04:05')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-01-02 03:04:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-1-2T03:04:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('')).toBeUndefined()
    expect(parseStrictServerDateTime(undefined)).toBeUndefined()
  })

  it('越界日期时间分量返回 undefined', () => {
    expect(parseStrictServerDateTime('2026-13-02T03:04:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-00-02T03:04:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-02-30T03:04:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-02-00T03:04:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-01-02T24:04:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-01-02T03:60:05Z')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-01-02T03:04:60Z')).toBeUndefined()
  })

  it('闰年日期按当月天数校验', () => {
    expect(parseStrictServerDateTime('2024-02-29T00:00:00Z')).toBeDefined()
    expect(parseStrictServerDateTime('2026-02-29T00:00:00Z')).toBeUndefined()
  })

  it('时区偏移越界返回 undefined', () => {
    expect(parseStrictServerDateTime('2026-01-02T03:04:05+24:00')).toBeUndefined()
    expect(parseStrictServerDateTime('2026-01-02T03:04:05+08:60')).toBeUndefined()
  })
})

describe('formatDateTime', () => {
  it('空值显示占位符', () => {
    expect(formatDateTime(undefined)).toBe('-')
    expect(formatDateTime('')).toBe('-')
  })

  it('非法格式显示异常文案', () => {
    expect(formatDateTime('not-a-date')).toBe('时间格式异常')
  })

  it('按本地时区输出 YYYY-MM-DD HH:mm:ss.SSS', () => {
    const input = '2026-01-02T03:04:05.123+08:00'
    expect(formatDateTime(input)).toBe(localDateTimeText(new Date(input)))
  })
})

describe('formatLocalTime', () => {
  it('非法输入返回占位符', () => {
    expect(formatLocalTime(undefined)).toBe('-')
    expect(formatLocalTime('bad')).toBe('-')
  })

  it('按本地时区输出 HH:mm', () => {
    const input = '2026-01-02T23:59:05Z'
    const date = new Date(input)
    expect(formatLocalTime(input)).toBe(`${pad(date.getHours())}:${pad(date.getMinutes())}`)
  })
})

describe('serverDateTimeTimestamp / compareServerDateTime', () => {
  it('空值或非法值返回 undefined', () => {
    expect(serverDateTimeTimestamp(undefined)).toBeUndefined()
    expect(serverDateTimeTimestamp('bad')).toBeUndefined()
  })

  it('合法值返回毫秒时间戳', () => {
    expect(serverDateTimeTimestamp('2026-01-02T03:04:05Z')).toBe(new Date('2026-01-02T03:04:05Z').getTime())
  })

  it('compareServerDateTime 的空值与顺序语义', () => {
    expect(compareServerDateTime(undefined, undefined)).toBe(0)
    expect(compareServerDateTime(undefined, '2026-01-02T03:04:05Z')).toBe(1)
    expect(compareServerDateTime('2026-01-02T03:04:05Z', undefined)).toBe(-1)
    expect(compareServerDateTime('2026-01-02T03:04:05Z', '2027-01-02T03:04:05Z')).toBeLessThan(0)
    expect(compareServerDateTime('2027-01-02T03:04:05Z', '2026-01-02T03:04:05Z')).toBeGreaterThan(0)
    expect(compareServerDateTime('2026-01-02T03:04:05Z', '2026-01-02T03:04:05Z')).toBe(0)
  })
})

describe('formatRelativeDateTime', () => {
  it('空值与非法值有占位文案', () => {
    expect(formatRelativeDateTime(undefined)).toBe('-')
    expect(formatRelativeDateTime('bad')).toBe('时间格式异常')
  })

  it('输出相对时间与本地日期时间后缀', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 0, 15, 12, 0, 0))
    // 构造“30 秒前”的服务器时间字符串（带本地时区偏移，任意时区下稳定）。
    const target = new Date(new Date(2026, 0, 15, 12, 0, 0).getTime() - 30_000)
    const offsetMinutes = -target.getTimezoneOffset()
    const sign = offsetMinutes >= 0 ? '+' : '-'
    const absOffset = Math.abs(offsetMinutes)
    const input = `${target.getFullYear()}-${pad(target.getMonth() + 1)}-${pad(target.getDate())}`
      + `T${pad(target.getHours())}:${pad(target.getMinutes())}:${pad(target.getSeconds())}`
      + `${sign}${pad(Math.floor(absOffset / 60))}:${pad(absOffset % 60)}`
    expect(formatRelativeDateTime(input)).toBe(`a few seconds ago ${dayjs(target).format('YYYY-MM-DD HH:mm')}`)
  })
})

describe('数字格式化', () => {
  it('formatNumber 按 zh-CN 千分位分组，空值回退 0', () => {
    expect(formatNumber(1234567)).toBe('1,234,567')
    expect(formatNumber(undefined)).toBe('0')
    expect(formatNumber(0)).toBe('0')
  })

  it('formatInteger 四舍五入到整数', () => {
    expect(formatInteger(1234.5)).toBe('1,235')
    expect(formatInteger(undefined)).toBe('0')
    expect(formatInteger(-2.4)).toBe('-2')
  })

  it('formatUngroupedInteger 不分组', () => {
    expect(formatUngroupedInteger(1234567)).toBe('1234567')
    expect(formatUngroupedInteger(undefined)).toBe('0')
  })

  it('formatRequestCountTag 追加 req 后缀', () => {
    expect(formatRequestCountTag(42)).toBe('42req')
    expect(formatRequestCountTag(undefined)).toBe('0req')
  })

  it('formatCompactUsageAmount 按量级压缩', () => {
    expect(formatCompactUsageAmount(999)).toBe('999')
    expect(formatCompactUsageAmount(1_000)).toBe('1.0K')
    expect(formatCompactUsageAmount(12_345)).toBe('12.3K')
    expect(formatCompactUsageAmount(1_000_000)).toBe('1.0M')
    expect(formatCompactUsageAmount(1_500_000_000)).toBe('1.5B')
    expect(formatCompactUsageAmount(undefined)).toBe('0')
    expect(formatCompactUsageAmount(-1_500)).toBe('-1.5K')
  })

  it('formatUsd 保留指定位数', () => {
    expect(formatUsd(3.14159)).toBe('$3.14')
    expect(formatUsd(3.14159, 3)).toBe('$3.142')
    expect(formatUsd(undefined)).toBe('$0.00')
  })
})

describe('formatMillisecondsAsSeconds', () => {
  it('非有限数值返回占位符', () => {
    expect(formatMillisecondsAsSeconds(undefined)).toBe('-')
    expect(formatMillisecondsAsSeconds(null)).toBe('-')
    expect(formatMillisecondsAsSeconds(Number.NaN)).toBe('-')
    expect(formatMillisecondsAsSeconds(Number.POSITIVE_INFINITY)).toBe('-')
  })

  it('按量级选择精度，负数钳制为 0', () => {
    expect(formatMillisecondsAsSeconds(0)).toBe('0s')
    expect(formatMillisecondsAsSeconds(12)).toBe('0.01s')
    expect(formatMillisecondsAsSeconds(500)).toBe('0.50s')
    expect(formatMillisecondsAsSeconds(5_000)).toBe('5.0s')
    expect(formatMillisecondsAsSeconds(15_000)).toBe('15s')
    expect(formatMillisecondsAsSeconds(-100)).toBe('0s')
  })
})

describe('parseStrictDatePickerValue', () => {
  it('空值返回 undefined', () => {
    expect(parseStrictDatePickerValue(undefined)).toBeUndefined()
    expect(parseStrictDatePickerValue('')).toBeUndefined()
  })

  it('非法格式抛出带标签的错误', () => {
    expect(() => parseStrictDatePickerValue('bad')).toThrow('时间格式异常，请清理后再编辑')
    expect(() => parseStrictDatePickerValue('bad', '开始时间')).toThrow('开始时间格式异常，请清理后再编辑')
  })

  it('合法值解析为 Dayjs', () => {
    const parsed = parseStrictDatePickerValue('2026-01-02T03:04:05Z')
    expect(parsed?.toDate()).toEqual(new Date('2026-01-02T03:04:05Z'))
  })
})

describe('formatServerDateTimeInput', () => {
  it('空值返回 null', () => {
    expect(formatServerDateTimeInput(undefined)).toBeNull()
    expect(formatServerDateTimeInput(null)).toBeNull()
  })

  it('输出 ISO 字符串', () => {
    const value = dayjs('2026-01-02T03:04:05+08:00')
    expect(formatServerDateTimeInput(value)).toBe(value.toDate().toISOString())
  })
})
