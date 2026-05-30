import assert from 'node:assert/strict'

import {
  evaluateApiKeyAvailabilitySchedule,
  normalizeApiKeyAvailabilitySchedule
} from '../../storage/api-key-availability-schedule.js'

const dailySchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'Asia/Shanghai',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
  ]
})

assert.equal(
  evaluateApiKeyAvailabilitySchedule(dailySchedule, new Date('2026-05-31T14:30:00.000Z')).allowed,
  true,
  '每天 22:00-23:55 内应允许调用'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(dailySchedule, new Date('2026-05-31T15:56:00.000Z')).allowed,
  false,
  '每天 23:55 后应进入计划停用'
)

const crossDaySchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'Asia/Shanghai',
  windows: [
    { daysOfWeek: [7], start: '22:00', end: '02:00' }
  ]
})

assert.equal(
  evaluateApiKeyAvailabilitySchedule(crossDaySchedule, new Date('2026-05-31T17:30:00.000Z')).allowed,
  true,
  '周日 22:00-次日 02:00 的次日凌晨部分应允许调用'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(crossDaySchedule, new Date('2026-05-31T19:00:00.000Z')).allowed,
  false,
  '跨天时段结束后应进入计划停用'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '22:00' }
    ]
  }),
  /开始时间和停止时间不能相同/,
  '开始时间和停止时间相同时应拒绝保存'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    windows: [
      { daysOfWeek: [1, 9], start: '22:00', end: '23:55' }
    ]
  }),
  /重复日期无效/,
  '重复日期包含非法星期时应拒绝保存'
)

console.log('API Key 自动启停计划回归通过：日常时段、跨天时段、非法时段和非法星期校验符合预期')
