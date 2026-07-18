import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'

import type { Response } from 'express'

import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'
import { createGatewaySseWaitHeartbeatObserver } from '../../modules/gateway/response/sse-wait-heartbeat.js'
import { shouldRetryPreCommitStreamFailureOnServer } from '../../modules/gateway/response/stream-finalization-retry-decision.js'
import type { StreamPipeResult } from '../../modules/gateway/response/stream-result.js'
import { pipeNonStreamUpstreamResponse } from '../../modules/gateway/upstream/body.js'

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

const firstWriteFailureState = new GatewayDownstreamCommitState()
const firstWriteFailureResponse = fakeResponse({ throwOnWrite: true })
await assert.rejects(
  pipeNonStreamUpstreamResponse(singleChunkBody('first chunk'), firstWriteFailureResponse, {
    startedAt: Date.now(),
    prepareDownstream: () => firstWriteFailureState.markTransportCommitted(),
    onChunkWritten: (bytesWritten) => firstWriteFailureState.markSemanticCommitted(bytesWritten)
  }),
  /downstream write failed/
)
assert.equal(firstWriteFailureState.transportCommitted, true, '响应头准备完成后应只标记传输层已提交')
assert.equal(firstWriteFailureState.semanticCommitted, false, '首块正文写失败时不得提前标记语义已提交')
assert.equal(firstWriteFailureState.canRetryUpstream(), true, '首块正文写失败时应允许服务端继续切换账户')

const successfulWriteState = new GatewayDownstreamCommitState()
const successfulWriteResponse = fakeResponse()
await pipeNonStreamUpstreamResponse(singleChunkBody('ok'), successfulWriteResponse, {
  startedAt: Date.now(),
  prepareDownstream: () => successfulWriteState.markTransportCommitted(),
  onChunkWritten: (bytesWritten) => successfulWriteState.markSemanticCommitted(bytesWritten)
})
assert.equal(successfulWriteState.semanticCommitted, true, '首块正文成功写出后应标记语义已提交')
assert.equal(successfulWriteState.downstreamBytesWritten, 2)

const emptyBodyState = new GatewayDownstreamCommitState()
await pipeNonStreamUpstreamResponse(emptyBody(), fakeResponse(), {
  startedAt: Date.now(),
  prepareDownstream: () => emptyBodyState.markTransportCommitted(),
  onChunkWritten: (bytesWritten) => emptyBodyState.markSemanticCommitted(bytesWritten),
  onBodyCompleted: (transferredBytes) => {
    if (transferredBytes === 0) emptyBodyState.markSemanticCommitted()
  }
})
assert.equal(emptyBodyState.semanticCommitted, true, '空 body 成功结束后也应提交完整响应语义')

const heartbeatState = new GatewayDownstreamCommitState()
const heartbeatResponse = fakeResponse({ throwOnWrite: true })
const heartbeatObserver = createGatewaySseWaitHeartbeatObserver({
  res: heartbeatResponse,
  downstreamProtocol: 'chat_completions_sse',
  downstreamCommitState: heartbeatState
})
assert(heartbeatObserver)
assert.doesNotThrow(() => heartbeatObserver.onWaitStarted?.(), '客户端断连竞争导致心跳写失败时不应把异常抛回请求链')
assert.equal(heartbeatState.transportCommitted, false, '失败的心跳写入不得标记传输层已提交')

console.log('gateway downstream commit regression passed')

async function* singleChunkBody(value: string): AsyncGenerator<Uint8Array> {
  yield Buffer.from(value)
}

async function* emptyBody(): AsyncGenerator<Uint8Array> {
  return
}

function fakeResponse(input: { throwOnWrite?: boolean } = {}): Response {
  const response = new EventEmitter() as EventEmitter & Record<string, unknown>
  response.headersSent = false
  response.writableEnded = false
  response.destroyed = false
  response.writableLength = 0
  response.writableHighWaterMark = 16_384
  response.status = function status() {
    return this
  }
  response.setHeader = function setHeader() {
    return this
  }
  response.write = function write() {
    if (input.throwOnWrite) throw new Error('downstream write failed')
    this.headersSent = true
    return true
  }
  response.end = function end() {
    this.headersSent = true
    this.writableEnded = true
    return this
  }
  response.destroy = function destroy() {
    this.destroyed = true
    return this
  }
  response.locals = {}
  return response as unknown as Response
}
