import assert from 'node:assert/strict'

import dayjs, { type Dayjs } from 'dayjs'

import type { RuntimeLogGrepRuntime } from '@/types/domain'
import {
  defaultGrepRange,
  isGrepDateDisabled,
  normalizeGrepRange,
  parseStoredGrepRangeWithoutRuntime
} from '../../views/runtime-logs/runtimeLogTimeRanges'

const DAY_MS = 24 * 60 * 60 * 1000
const TOLERANCE_MS = 2000

function makeRuntime(overrides: Partial<RuntimeLogGrepRuntime>): RuntimeLogGrepRuntime {
  const now = dayjs()
  return {
    earliestFileTime: now.toISOString(),
    defaultStartAt: now.subtract(3, 'day').toISOString(),
    defaultEndAt: now.toISOString(),
    defaultRangeDays: 3,
    maxRangeDays: 7,
    fileRetentionDays: 30,
    activeSearchCount: 0,
    maxConcurrentSearches: 4,
    ...overrides
  }
}

function assertCloseTo(actual: Dayjs, expected: Dayjs, message: string): void {
  assert.ok(Math.abs(actual.diff(expected, 'millisecond')) <= TOLERANCE_MS, message)
}

const now = dayjs()

// 无 runtime 时默认范围 = [now-3天, now]
const defaults = defaultGrepRange()
const defaultWidthMs = defaults[1].diff(defaults[0], 'millisecond')
assert.ok(
  Math.abs(defaultWidthMs - 3 * DAY_MS) <= TOLERANCE_MS,
  `默认 grep 范围宽度应约为 3 天，实际 ${defaultWidthMs}ms`
)
assertCloseTo(defaults[1], dayjs(), '默认 grep 范围 end 应约为当前时间')

// earliestFileTime≈now（单活跃日志文件场景）不再钳制 start：maxRangeDays 放宽为 60，30 天前的 start 原样保留
const earliestNowRuntime = makeRuntime({ earliestFileTime: now.toISOString(), maxRangeDays: 60 })
const wide = normalizeGrepRange([now.subtract(30, 'day'), now], earliestNowRuntime)
assertCloseTo(wide[0], now.subtract(30, 'day'), 'earliest≈now 时 start 不应被钳到 earliest，应保留传入的 30 天前')
assertCloseTo(wide[1], now, 'earliest≈now 时 end 应保留传入值')

// 窗口超过 maxRangeDays 时 start 截到 end-maxRangeDays
const cappedThirty = normalizeGrepRange(
  [now.subtract(30, 'day'), now],
  makeRuntime({ defaultRangeDays: 3, maxRangeDays: 7 })
)
assertCloseTo(cappedThirty[0], now.subtract(7, 'day'), '30 天窗口在 maxRangeDays=7 下 start 应截到 end-7 天')
const cappedTen = normalizeGrepRange(
  [now.subtract(10, 'day'), now],
  makeRuntime({ defaultRangeDays: 3, maxRangeDays: 7 })
)
assertCloseTo(cappedTen[0], now.subtract(7, 'day'), '10 天窗口在 maxRangeDays=7 下 start 应截到 end-7 天')

// start>end 倒置修正为 start=end-defaultRangeDays（无 runtime 走默认 3 天）
const inverted = normalizeGrepRange([now.add(1, 'day'), now.subtract(1, 'day')])
assertCloseTo(inverted[0], now.subtract(4, 'day'), 'start>end 时 start 应修正为 end-3 天')
assertCloseTo(inverted[1], now.subtract(1, 'day'), 'start>end 修正时 end 不应变')

// end 为未来时间截到 now，合法 start 不受影响
const future = normalizeGrepRange([now.subtract(1, 'day'), now.add(2, 'day')])
assertCloseTo(future[1], dayjs(), '未来 end 应截到当前时间')
assert.ok(!future[1].isAfter(dayjs()), 'end 不应晚于当前时间')
assertCloseTo(future[0], now.subtract(1, 'day'), 'end 截断不应影响合法 start')

// value 缺省时 end=now、start=end-3 天
const fallback = normalizeGrepRange(undefined)
assertCloseTo(fallback[1], dayjs(), '缺省 end 应取当前时间')
assertCloseTo(fallback[0], fallback[1].subtract(3, 'day'), '缺省 start 应为 end-3 天')

// 日期禁用下界来自 fileRetentionDays 而非 earliestFileTime
assert.equal(
  isGrepDateDisabled(dayjs().subtract(1, 'day'), makeRuntime({ earliestFileTime: now.toISOString(), fileRetentionDays: 30 })),
  false,
  'earliest≈now 时昨天不应被禁用（旧逻辑 earliest≈now 会禁掉昨天）'
)
assert.equal(
  isGrepDateDisabled(dayjs().subtract(31, 'day'), makeRuntime({ earliestFileTime: now.toISOString(), fileRetentionDays: 30 })),
  true,
  'now-31 天超出 fileRetentionDays=30 下界应被禁用'
)

// 存储的 [start,end] ISO 字符串往返不丢
const stored: [string, string] = [now.subtract(2, 'day').toISOString(), now.toISOString()]
const restored = parseStoredGrepRangeWithoutRuntime(stored)
if (restored === undefined) throw new Error('存储的 grep 范围应能往返解析')
assert.equal(restored[0].valueOf(), dayjs(stored[0]).valueOf(), 'start ISO 往返不丢')
assert.equal(restored[1].valueOf(), dayjs(stored[1]).valueOf(), 'end ISO 往返不丢')

console.log('运行日志 grep 时间范围回归通过')
