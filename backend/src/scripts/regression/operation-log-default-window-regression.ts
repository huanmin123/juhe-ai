import assert from 'node:assert/strict'

import {
  defaultManagementOperationLogDateRange,
  parseOperationLogListOptions
} from '../../modules/operation-logs/operation-log-list-options.js'

const fixedNow = new Date(Date.UTC(2026, 5, 22, 12, 30, 0, 0))
const range = defaultManagementOperationLogDateRange(fixedNow)
const start = new Date(range.startAt)
const end = new Date(range.endAt)

assert.equal(start.getUTCFullYear(), 2026)
assert.equal(start.getUTCMonth(), 4)
assert.equal(start.getUTCDate(), 23)
assert.equal(start.getUTCHours(), 0)
assert.equal(end.getUTCDate(), 22)
assert.equal(end.getUTCHours(), 23)
assert.equal(end.getUTCMinutes(), 59)
assert.equal(range.startAt, '2026-05-23T00:00:00.000Z', '默认窗口必须使用 UTC 自然日，而非后端进程本地时区')
assert.equal(range.endAt, '2026-06-22T23:59:59.999Z', '默认窗口结束必须使用 UTC 自然日')

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
