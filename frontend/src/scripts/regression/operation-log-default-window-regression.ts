import assert from 'node:assert/strict'
import dayjs from 'dayjs'

import { operationLogListParams } from '@/views/operation-logs/operationLogFilters'
import {
  defaultManagementOperationLogDateRange,
  defaultOperationLogsPageState,
  operationLogPageStateForTrace
} from '@/views/operation-logs/operationLogPageState'

const pageSize = 20
const now = dayjs('2026-06-22T12:30:00.000Z')
const defaultRange = defaultManagementOperationLogDateRange(now)
assert.deepEqual(defaultRange, [
  now.subtract(30, 'day').startOf('day').toISOString(),
  now.endOf('day').toISOString()
])

const managementState = defaultOperationLogsPageState(pageSize, true)
assert(managementState.createdAtRange, '管理页面默认必须显示日期窗口')
const managementParams = operationLogListParams({
  ...managementState,
  createdAtRange: [dayjs(defaultRange[0]), dayjs(defaultRange[1])]
}, managementState.pagination, true)
assert.equal(managementParams.startAt, defaultRange[0])
assert.equal(managementParams.endAt, defaultRange[1])

const selfState = defaultOperationLogsPageState(pageSize)
assert.equal(selfState.createdAtRange, undefined, '个人页面默认不得被管理窗口限制')
const selfParams = operationLogListParams({
  ...selfState,
  createdAtRange: undefined
}, selfState.pagination, false)
assert.equal(selfParams.startAt, undefined)
assert.equal(selfParams.endAt, undefined)

const exactTraceState = operationLogPageStateForTrace(
  pageSize,
  true,
  '019f81b6-f427-7c63-88bc-de491c9350eb'
)
assert.equal(exactTraceState.createdAtRange, undefined, '完整 UUID trace 深链不得被默认窗口限制')

const tracePrefixState = operationLogPageStateForTrace(pageSize, true, '019f81b6')
assert(tracePrefixState.createdAtRange, 'trace 前缀深链必须保留管理默认窗口')

console.log('操作日志前端默认日期窗口回归通过')
