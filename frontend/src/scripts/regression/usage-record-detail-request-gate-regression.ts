import assert from 'node:assert/strict'

import { createUsageRecordDetailRequestGate } from '../../views/usage-records/usageRecordDetailRequestGate'

const gate = createUsageRecordDetailRequestGate()
const first = gate.begin('record-a', 'self|record-a')
const second = gate.begin('record-b', 'self|record-b')
assert.equal(gate.isCurrent(first, 'record-a', 'self|record-a'), false, '较早详情请求不得覆盖后发的另一条记录')
assert.equal(gate.isCurrent(second, 'record-b', 'self|record-b'), true, '最新详情请求应保持可提交')
gate.invalidate()
assert.equal(gate.isCurrent(second, 'record-b', 'self|record-b'), false, '关闭详情抽屉必须使当前请求失效')
const third = gate.begin('record-c', 'admin|owner-c|record-c')
gate.deactivate()
assert.equal(gate.isCurrent(third, 'record-c', 'admin|owner-c|record-c'), false, '页面停用必须使详情请求失效')
gate.activate()
const fourth = gate.begin('record-d', 'self|record-d')
assert.equal(gate.isCurrent(fourth, 'record-d', 'self|record-d'), true, '页面重新激活后应允许新的详情请求')
console.log('使用记录详情请求门禁回归通过：按 ID、作用域、关闭和 KeepAlive 竞态均已隔离')
