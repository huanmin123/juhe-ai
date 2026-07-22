import assert from 'node:assert/strict'

import {
  defaultManagementOperationLogDateRange,
  parseOperationLogListOptions
} from '../../modules/operation-logs/operation-log-list-options.js'

const fixedNow = new Date(2026, 5, 22, 12, 30, 0, 0)
const range = defaultManagementOperationLogDateRange(fixedNow)
const start = new Date(range.startAt)
const end = new Date(range.endAt)

assert.equal(start.getFullYear(), 2026)
assert.equal(start.getMonth(), 4)
assert.equal(start.getDate(), 23)
assert.equal(start.getHours(), 0)
assert.equal(end.getDate(), 22)
assert.equal(end.getHours(), 23)
assert.equal(end.getMinutes(), 59)

const managementDefault = parseOperationLogListOptions({}, true, fixedNow)
assert.deepEqual(
  { startAt: managementDefault.startAt, endAt: managementDefault.endAt },
  range,
  '管理列表未传日期时必须使用最近 31 个自然日'
)

const selfDefault = parseOperationLogListOptions({}, false)
assert.equal(selfDefault.startAt, undefined, '个人列表不得被管理默认窗口限制')
assert.equal(selfDefault.endAt, undefined, '个人列表不得被管理默认窗口限制')

const traceQuery = parseOperationLogListOptions({ traceId: '019f81b6-f427-7c63-88bc-de491c9350eb' }, true, fixedNow)
assert.equal(traceQuery.startAt, undefined, '精确 trace 追溯不得被默认窗口限制')
assert.equal(traceQuery.endAt, undefined, '精确 trace 追溯不得被默认窗口限制')

const tracePrefixQuery = parseOperationLogListOptions({ traceId: '019f81b6' }, true, fixedNow)
assert.deepEqual(
  { startAt: tracePrefixQuery.startAt, endAt: tracePrefixQuery.endAt },
  range,
  'trace 前缀搜索不得绕过默认窗口'
)

const explicitRange = parseOperationLogListOptions({
  startAt: '2026-01-01T00:00:00.000Z',
  endAt: '2026-02-01T00:00:00.000Z'
}, true)
assert.equal(explicitRange.startAt, '2026-01-01T00:00:00.000Z')
assert.equal(explicitRange.endAt, '2026-02-01T00:00:00.000Z')

const partialRange = parseOperationLogListOptions({ startAt: '2026-01-01T00:00:00.000Z' }, true)
assert.equal(partialRange.startAt, '2026-01-01T00:00:00.000Z')
assert.equal(partialRange.endAt, undefined, '单侧显式时间不得被默认窗口补写')

console.log('操作日志管理默认日期窗口回归通过')
