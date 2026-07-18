import { strict as assert } from 'node:assert'

import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'
import { shouldRetryPreCommitStreamFailureOnServer } from '../../modules/gateway/response/stream-finalization-retry-decision.js'
import type { StreamPipeResult } from '../../modules/gateway/response/stream-result.js'

const state = new GatewayDownstreamCommitState()
assert.equal(state.transportCommitted, false)
assert.equal(state.semanticCommitted, false)
assert.equal(state.canRetryUpstream(), true)

state.markTransportCommitted(18)
assert.equal(state.transportCommitted, true, 'SSE 注释心跳应只提交传输层')
assert.equal(state.semanticCommitted, false)
assert.equal(state.downstreamBytesWritten, 18)
assert.equal(state.canRetryUpstream(), true, '只提交传输层时仍应允许在同一 SSE 连接切换账号')

state.markSemanticCommitted(42)
assert.equal(state.semanticCommitted, true)
assert.equal(state.downstreamBytesWritten, 60)
assert.equal(state.canRetryUpstream(), false, '真实协议事件写出后不得透明拼接第二个账号')

const transportOnlyFailure = {
  completed: false,
  message: 'upstream transport closed',
  errorCode: 'upstream_transport_closed',
  usage: {},
  outputReceived: false,
  imageOutputReceived: false,
  downstreamBytesWritten: 18,
  transportCommitted: true,
  semanticCommitted: false
} as StreamPipeResult
assert.equal(shouldRetryPreCommitStreamFailureOnServer(transportOnlyFailure, {
  headersSent: true,
  writableEnded: false,
  destroyed: false
}), true, 'SSE 心跳已经写出但语义未提交时仍应服务端重试')

console.log('gateway downstream commit regression passed')
