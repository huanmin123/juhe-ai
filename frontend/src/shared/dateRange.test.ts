import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import dayjs from 'dayjs'

import { formatDateKey, formatDateLabel, formatDateShortLabel, isDateKey, isMonthKey, isRecentWindowDateDisabled, normalizeDateRangeKeys, normalizeDayjsDateRange, parseDateKey, parseDateRangeKeys, recentDateRange, todayDateRange } from './dateRange'

// 固定“今天”为 2026-01-15，避免测试随真实时间漂移。
const fixedNow = new Date(2026, 0, 15, 12, 30, 0)

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(fixedNow)
})

afterEach(() => {
  vi.useRealTimers()
})

function d(value: string): dayjs.Dayjs {
  return dayjs(value)
}

describe('todayDateRange', () => {
  it('返回今天的零点起止', () => {
    const [start, end] = todayDateRange()
    expect(start.format('YYYY-MM-DD HH:mm:ss')).toBe('2026-01-15 00:00:00')
    expect(end.isSame(start, 'day')).toBe(true)
  })
})

describe('recentDateRange', () => {
  it('按天数向过去展开并含今天', () => {
    const [start, end] = recentDateRange(7)
    expect(formatDateKey(start)).toBe('2026-01-09')
    expect(formatDateKey(end)).toBe('2026-01-15')
  })

  it('天数为 1 时仅今天', () => {
    const [start, end] = recentDateRange(1)
    expect(formatDateKey(start)).toBe('2026-01-15')
    expect(formatDateKey(end)).toBe('2026-01-15')
  })

  it('非正数与小数被钳制', () => {
    expect(formatDateKey(recentDateRange(0)[0])).toBe('2026-01-15')
    expect(formatDateKey(recentDateRange(-5)[0])).toBe('2026-01-15')
    expect(formatDateKey(recentDateRange(3.9)[0])).toBe('2026-01-13')
  })
})

describe('isDateKey / isMonthKey', () => {
  it('接受 YYYY-MM-DD', () => {
    expect(isDateKey('2026-01-15')).toBe(true)
    expect(isDateKey('2026-1-5')).toBe(false)
    expect(isDateKey('2026-01')).toBe(false)
    expect(isDateKey(undefined)).toBe(false)
    expect(isDateKey('')).toBe(false)
  })

  it('接受 YYYY-MM', () => {
    expect(isMonthKey('2026-01')).toBe(true)
    expect(isMonthKey('2026-1')).toBe(false)
    expect(isMonthKey('2026-01-15')).toBe(false)
    expect(isMonthKey(undefined)).toBe(false)
  })
})

describe('parseDateKey', () => {
  it('解析合法日期键为零点 Dayjs', () => {
    const parsed = parseDateKey('2026-01-15')
    expect(parsed?.format('YYYY-MM-DD HH:mm')).toBe('2026-01-15 00:00')
  })

  it('拒绝格式不符的输入', () => {
    expect(parseDateKey('2026/01/15')).toBeUndefined()
    expect(parseDateKey('2026-13-01')).toBeUndefined()
    expect(parseDateKey(undefined)).toBeUndefined()
  })

  it('拒绝实际不存在的日期（月份天数溢出）', () => {
    expect(parseDateKey('2026-02-30')).toBeUndefined()
    expect(parseDateKey('2026-02-29')).toBeUndefined()
    expect(parseDateKey('2024-02-29')).toBeDefined()
  })
})

describe('格式化函数', () => {
  it('formatDateKey 输出 YYYY-MM-DD', () => {
    expect(formatDateKey(d('2026-02-03'))).toBe('2026-02-03')
  })

  it('formatDateLabel 输出 M月D日，非法输入原样返回', () => {
    expect(formatDateLabel('2026-02-03')).toBe('2月3日')
    expect(formatDateLabel('2026-12-25')).toBe('12月25日')
    expect(formatDateLabel('bad')).toBe('bad')
  })

  it('formatDateShortLabel 输出 MM-DD，非法输入原样返回', () => {
    expect(formatDateShortLabel('2026-02-03')).toBe('02-03')
    expect(formatDateShortLabel('bad')).toBe('bad')
  })
})

describe('parseDateRangeKeys', () => {
  const defaultRange = (): [dayjs.Dayjs, dayjs.Dayjs] => [d('2026-01-10'), d('2026-01-15')]

  it('合法输入按输入解析', () => {
    const [start, end] = parseDateRangeKeys({ startDate: '2026-01-12', endDate: '2026-01-14' }, { defaultRange })
    expect(formatDateKey(start)).toBe('2026-01-12')
    expect(formatDateKey(end)).toBe('2026-01-14')
  })

  it('非法或缺失输入回退默认区间', () => {
    const [start, end] = parseDateRangeKeys({ startDate: 'bad' }, { defaultRange })
    expect(formatDateKey(start)).toBe('2026-01-10')
    expect(formatDateKey(end)).toBe('2026-01-15')
  })

  it('defaultRange 支持函数形式', () => {
    const [start, end] = parseDateRangeKeys(undefined, { defaultRange: () => [d('2026-01-01'), d('2026-01-02')] })
    expect(formatDateKey(start)).toBe('2026-01-01')
    expect(formatDateKey(end)).toBe('2026-01-02')
  })

  it('maxDays 钳制超宽区间', () => {
    const [start, end] = parseDateRangeKeys({ startDate: '2025-12-01', endDate: '2026-01-15' }, { defaultRange, maxDays: 7 })
    expect(formatDateKey(start)).toBe('2026-01-09')
    expect(formatDateKey(end)).toBe('2026-01-15')
  })
})

describe('normalizeDateRangeKeys', () => {
  const defaultRange: [dayjs.Dayjs, dayjs.Dayjs] = [d('2026-01-10'), d('2026-01-15')]

  it('正常区间归一为零点日期键', () => {
    expect(normalizeDateRangeKeys([d('2026-01-12 08:00'), d('2026-01-14 23:00')], { defaultRange })).toEqual(['2026-01-12', '2026-01-14'])
  })

  it('起始晚于结束时将起始钳制为结束日', () => {
    expect(normalizeDateRangeKeys([d('2026-01-20'), d('2026-01-14')], { defaultRange })).toEqual(['2026-01-14', '2026-01-14'])
  })

  it('maxDays 超宽时把起始拉回到结束前 maxDays-1 天', () => {
    expect(normalizeDateRangeKeys([d('2026-01-01'), d('2026-01-15')], { defaultRange, maxDays: 3 })).toEqual(['2026-01-13', '2026-01-15'])
  })

  it('缺失端点回退默认区间', () => {
    expect(normalizeDateRangeKeys([undefined as unknown as dayjs.Dayjs, d('2026-01-14')], { defaultRange })).toEqual(['2026-01-10', '2026-01-14'])
  })
})

describe('normalizeDayjsDateRange', () => {
  it('有效区间归一为零点并保持顺序', () => {
    const [start, end] = normalizeDayjsDateRange([d('2026-01-14 10:00'), d('2026-01-12 05:00')])!
    expect(formatDateKey(start)).toBe('2026-01-12')
    expect(formatDateKey(end)).toBe('2026-01-14')
  })

  it('无效端点返回 undefined', () => {
    expect(normalizeDayjsDateRange(undefined)).toBeUndefined()
    expect(normalizeDayjsDateRange([dayjs('invalid-date'), d('2026-01-12')])).toBeUndefined()
    expect(normalizeDayjsDateRange([d('2026-01-12'), undefined as unknown as dayjs.Dayjs])).toBeUndefined()
  })
})

describe('isRecentWindowDateDisabled', () => {
  const maxDays = 7

  it('current 为空直接可用', () => {
    expect(isRecentWindowDateDisabled(null, [null, null], maxDays)).toBe(false)
  })

  it('晚于今天或早于窗口起点时禁用', () => {
    expect(isRecentWindowDateDisabled(d('2026-01-16'), [d('2026-01-14'), null], maxDays)).toBe(true)
    expect(isRecentWindowDateDisabled(d('2026-01-08'), [d('2026-01-14'), null], maxDays)).toBe(true)
  })

  it('窗口内且与锚点距离不超过 maxDays-1 时可用', () => {
    expect(isRecentWindowDateDisabled(d('2026-01-12'), [d('2026-01-14'), null], maxDays)).toBe(false)
    expect(isRecentWindowDateDisabled(d('2026-01-15'), [d('2026-01-14'), null], maxDays)).toBe(false)
  })

  it('与锚点距离超限时禁用', () => {
    expect(isRecentWindowDateDisabled(d('2026-01-12'), [d('2026-01-01'), null], maxDays)).toBe(true)
  })

  it('无锚点时不按锚点距离禁用', () => {
    expect(isRecentWindowDateDisabled(d('2026-01-09'), [null, null], maxDays)).toBe(false)
  })

  it('锚点取第一个非空端', () => {
    expect(isRecentWindowDateDisabled(d('2026-01-12'), [null, d('2026-01-14')], maxDays)).toBe(false)
  })

  it('referenceEndDate 覆盖“今天”基准', () => {
    // 基准日改为 2026-01-11：窗口为 01-05 至 01-11。
    expect(isRecentWindowDateDisabled(d('2026-01-10'), [null, null], maxDays, d('2026-01-11'))).toBe(false)
    expect(isRecentWindowDateDisabled(d('2026-01-12'), [null, null], maxDays, d('2026-01-11'))).toBe(true)
    expect(isRecentWindowDateDisabled(d('2026-01-04'), [null, null], maxDays, d('2026-01-11'))).toBe(true)
  })
})
