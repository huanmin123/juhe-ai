import assert from 'node:assert/strict'

import {
  acquireKeyModelRecoveryLease,
  capabilityHash,
  createKeyModelOpenState,
  decideKeyModelForegroundAdmission,
  isKeyModelBlocked,
  keyModelBackoffDelayMs,
  keyModelRecoverySuccessMaxGapMs,
  settleKeyModelRecovery,
  matchesMainProbeRoute,
  type CapabilityKey
} from '../../modules/gateway/runtime/key-model-runtime.js'

const base: CapabilityKey = {
  credentialSourceAccountId: 'source-1', keyFingerprint: 'key-1', clientModel: 'B', clientEndpointFamily: 'chat', finalUpstreamModel: 'b-upstream', upstreamEndpointMode: 'chat_json', dispatchRevision: 7
}
const alternativeKey = { ...base, keyFingerprint: 'key-2' }
const alternativeModel = { ...base, clientModel: 'C', finalUpstreamModel: 'c-upstream' }
assert.notEqual(capabilityHash(base), capabilityHash(alternativeKey), '多 Key 必须隔离')
assert.notEqual(capabilityHash(base), capabilityHash(alternativeModel), '多模型必须隔离')
assert.notEqual(capabilityHash(base), capabilityHash({ ...base, clientEndpointFamily: 'responses' }), '不同入口必须隔离')
assert.notEqual(capabilityHash(base), capabilityHash({ ...base, dispatchRevision: 8 }), '不同 revision 必须隔离')
assert.deepEqual([1, 2, 3, 4, 5].map(keyModelBackoffDelayMs), [5_000, 15_000, 60_000, 300_000, 300_000], '退避固定为 5 秒、15 秒、1 分钟、5 分钟封顶')

let now = 1_000
let state = createKeyModelOpenState(base, now)
assert.equal(state.retryAtMs, now + 5_000, '首次失败必须 OPEN 5 秒')
assert.ok(isKeyModelBlocked(state), 'OPEN 必须过滤普通派发')
assert.equal(matchesMainProbeRoute(base, { clientModel: 'B', clientEndpointFamily: 'chat', finalUpstreamModel: 'b-upstream', upstreamEndpointMode: 'chat_json' }), true)
assert.equal(matchesMainProbeRoute(base, { clientModel: 'B', clientEndpointFamily: 'responses', finalUpstreamModel: 'b-upstream', upstreamEndpointMode: 'responses_json' }), false, '主探测必须匹配完整路由')
const tenConcurrentDecisions = await Promise.all([...Array(10)].map(async (_, index) => decideKeyModelForegroundAdmission({ phase: 'CLOSED', activeUncommitted: index })))
assert.equal(tenConcurrentDecisions.filter((decision) => decision === 'admitted').length, 2, '10 并发同一 CapabilityKey 最多两个请求可进入未提交窗口')
assert.equal(decideKeyModelForegroundAdmission({ phase: 'OPEN', activeUncommitted: 0 }), 'blocked')
assert.equal(settleKeyModelRecovery(state, { generation: 1, dispatchRevision: 7, leaseId: 'missing', outcome: 'unknown', nowMs: now }).status, 'lease_mismatch', 'unknown 不得创建或修改恢复结论')

now = state.retryAtMs!
state = acquireKeyModelRecoveryLease(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-1', nowMs: now }).state
state = settleKeyModelRecovery(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-1', outcome: 'complete_success', nowMs: now }).state
assert.equal(state.phase, 'RECOVERING')
assert.equal(state.recoverySuccessCount, 1)
const firstSuccessAt = state.lastRecoverySuccessAtMs

// A 90-second queue delay changes neither recovery count nor the actual success timestamp.
now += 90_000
assert.equal(state.recoverySuccessCount, 1)
assert.equal(state.lastRecoverySuccessAtMs, firstSuccessAt)
state = acquireKeyModelRecoveryLease(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-2', nowMs: now }).state
state = settleKeyModelRecovery(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-2', outcome: 'complete_success', nowMs: now }).state
assert.equal(state.recoverySuccessCount, 2, '两次真实成功间隔不超过两分钟时递增')

now += keyModelRecoverySuccessMaxGapMs + 1
state = acquireKeyModelRecoveryLease(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-3', nowMs: now }).state
state = settleKeyModelRecovery(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-3', outcome: 'complete_success', nowMs: now }).state
assert.equal(state.phase, 'RECOVERING')
assert.equal(state.recoverySuccessCount, 1, '真实成功间隔超过两分钟必须重新从 1 计数')

now = state.retryAtMs!
state = acquireKeyModelRecoveryLease(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-fail', nowMs: now }).state
state = settleKeyModelRecovery(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-fail', outcome: 'upstream_not_complete', nowMs: now }).state
assert.equal(state.phase, 'OPEN')
assert.equal(state.retryAtMs, now + 15_000, '真实恢复失败才进入第二级退避')
assert.equal(state.recoverySuccessCount, 0)
assert.equal(state.lastRecoverySuccessAtMs, undefined)
assert.equal(settleKeyModelRecovery(state, { generation: 0, dispatchRevision: 7, leaseId: 'late', outcome: 'upstream_not_complete', nowMs: now }).status, 'stale', 'CAS 冲突/旧 generation 不得覆盖状态')

now = state.retryAtMs!
state = acquireKeyModelRecoveryLease(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-unknown', nowMs: now }).state
const beforeUnknown = { count: state.recoverySuccessCount, successAt: state.lastRecoverySuccessAtMs, backoffAttempt: state.backoffAttempt }
state = settleKeyModelRecovery(state, { generation: 1, dispatchRevision: 7, leaseId: 'lease-unknown', outcome: 'unknown', nowMs: now }).state
assert.equal(state.phase, 'OPEN', '取消、失租或未知结果不得伪造成功或失败')
assert.equal(state.recoverySuccessCount, beforeUnknown.count)
assert.equal(state.lastRecoverySuccessAtMs, beforeUnknown.successAt)
assert.equal(state.backoffAttempt, beforeUnknown.backoffAttempt)

console.log('key-model-runtime regression passed')
