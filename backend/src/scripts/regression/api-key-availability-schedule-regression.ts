import assert from 'node:assert/strict'

import {
  dueApiKeyAvailabilityScheduleEvent,
  apiKeyAvailabilityScheduleStatus,
  evaluateApiKeyAvailabilitySchedule,
  normalizeApiKeyAvailabilitySchedule
} from '../../storage/api-key-availability-schedule.js'

const dailySchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'Asia/Shanghai',
  mode: 'allow_windows',
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
  apiKeyAvailabilityScheduleStatus(dailySchedule, new Date('2026-05-31T14:30:00.000Z')),
  'active',
  '允许时段内应映射为 API Key 启用状态'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(dailySchedule, new Date('2026-05-31T14:00:00.000Z'))?.status,
  'active',
  '开始边界分钟应触发一次启用事件'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(dailySchedule, new Date('2026-05-31T14:30:00.000Z')),
  undefined,
  '窗口中间不应持续触发启用事件'
)
assert.equal(
  evaluateApiKeyAvailabilitySchedule(dailySchedule, new Date('2026-05-31T15:56:00.000Z')).allowed,
  false,
  '每天 23:55 后应进入时段外'
)
assert.equal(
  apiKeyAvailabilityScheduleStatus(dailySchedule, new Date('2026-05-31T15:56:00.000Z')),
  'disabled',
  '允许时段外应映射为 API Key 停用状态'
)
assert.equal(
  dueApiKeyAvailabilityScheduleEvent(dailySchedule, new Date('2026-05-31T15:55:00.000Z'))?.status,
  'disabled',
  '结束边界分钟应触发一次停用事件'
)

const crossDaySchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'Asia/Shanghai',
  mode: 'allow_windows',
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
  '跨天时段结束后应进入时段外'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
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
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1, 9], start: '22:00', end: '23:55' }
    ]
  }),
  /重复日期无效/,
  '重复日期包含非法星期时应拒绝保存'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ]
  }),
  /模式必须为 allow_windows/,
  '可用时段计划必须显式提交当前模式'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: '',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ]
  }),
  /时区不能为空/,
  '可用时段计划显式空时区不能静默回退默认时区'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    dateRange: null
  }),
  /生效日期范围无效/,
  '可用时段计划显式 null 日期范围不能静默当作未配置'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    exceptions: ''
  }),
  /例外日期无效/,
  '可用时段计划显式空字符串例外日期不能静默当作未配置'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    exceptions: [{ date: '2026-06-01', action: 'allow' }]
  }),
  /允许例外至少需要一个允许时段/,
  '允许例外必须显式配置至少一个允许时段'
)

assert.throws(
  () => normalizeApiKeyAvailabilitySchedule({
    enabled: true,
    timezone: 'Asia/Shanghai',
    mode: 'allow_windows',
    windows: [
      { daysOfWeek: [1], start: '22:00', end: '23:55' }
    ],
    exceptions: [{ date: '2026-06-01', action: 'deny', windows: [{ start: '10:00', end: '11:00' }] }]
  }),
  /拒绝例外不能配置允许时段/,
  '拒绝例外不能携带无效允许时段'
)

const exceptionSchedule = normalizeApiKeyAvailabilitySchedule({
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
  ],
  exceptions: [{ date: '2026-06-01', action: 'allow', windows: [{ start: '10:00', end: '11:00' }] }]
})
assert.equal(
  evaluateApiKeyAvailabilitySchedule(exceptionSchedule, new Date('2026-06-01T10:30:00.000Z')).allowed,
  true,
  '合法允许例外应按例外时段放行'
)

console.log('API Key 可用时段计划回归通过：日常时段、跨天时段、模式、空值、例外和非法参数校验符合预期')


