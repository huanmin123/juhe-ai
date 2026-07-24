import assert from 'node:assert/strict'

import { eventText } from '../../views/runtime-logs/runtimeLogFormatters'

assert.equal(eventText('db_service.request.start'), 'DB Service 请求开始')
assert.equal(eventText('db_service.request.complete'), 'DB Service 请求完成')
assert.equal(eventText('db_service.request.failed'), 'DB Service 请求失败')
assert.equal(eventText('gateway.request.stage'), '网关请求阶段')
assert.equal(eventText('gateway.request.failure'), '网关请求阶段失败')
assert.equal(eventText('gateway.request.timing_summary'), '网关请求耗时汇总')
assert.equal(eventText('gateway.future_event'), '网关未映射事件', 'dotted 网关事件必须落入有领域语义的兜底')
assert.equal(eventText('db_service.future_event'), 'DB Service 未映射事件', 'dotted DB Service 事件必须落入有领域语义的兜底')

console.log('运行日志事件映射回归通过')
