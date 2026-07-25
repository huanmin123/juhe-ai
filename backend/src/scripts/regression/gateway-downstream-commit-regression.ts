import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'

import type { Response } from 'express'

import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'
import { createGatewaySseWaitHeartbeatObserver } from '../../modules/gateway/response/sse-wait-heartbeat.js'
import { streamPreCommitBufferMaxBytes } from '../../modules/gateway/response/stream-pre-commit-buffer.js'
import { shouldRetryPreCommitStreamFailureOnServer } from '../../modules/gateway/response/stream-finalization-retry-decision.js'
import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'
import type { StreamPipeResult } from '../../modules/gateway/response/stream-result.js'
import { pipeNonStreamUpstreamResponse } from '../../modules/gateway/upstream/body.js'

const streamTimeoutProfile = {
  firstResponseTimeoutMs: 5_000,
  firstByteTimeoutMs: 5_000,
  idleTimeoutMs: 5_000,
  uncommittedAttemptMaxLifetimeMs: 30_000,
  noAvailableAccountWaitMs: 5_000
}

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

const keepAliveOnlyState = new GatewayDownstreamCommitState()
const keepAliveOnlyResponse = fakeResponse()
const keepAliveOnlyResult = await pipeUpstreamStream(
  singleChunkBody(': keep-alive\n\n'),
  keepAliveOnlyResponse,
  streamTimeoutProfile,
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'chat_completions',
    interpretProtocolFailures: false,
    retryBeforeDownstreamWriteUntilOutput: true,
    downstreamCommitState: keepAliveOnlyState
  }
)
assert.equal(keepAliveOnlyResult.completed, false, '只有 SSE keep-alive 的干净 EOF 不得伪装成协议完成')
assert.equal(keepAliveOnlyResult.transportCommitted, false, '上游 keep-alive 必须留在预提交缓冲，不得提交下游传输')
assert.equal(keepAliveOnlyResult.semanticCommitted, false)
assert.equal(keepAliveOnlyResult.downstreamBytesWritten, 0)
assert.equal(keepAliveOnlyResult.upstreamResponseBytesWritten, 0)
assert.equal(keepAliveOnlyResponse.headersSent, false, '上游 keep-alive 不得提前写出下游响应头')
assert.equal(keepAliveOnlyResponse.writableEnded, false, '建流前失败必须交回上层接管 HTTP 错误或切号')
assert.equal(keepAliveOnlyResult.uncommittedResponseBody, undefined, '纯 SSE comment 不属于语义正文，不应占用预提交缓冲')
assert.equal(shouldRetryPreCommitStreamFailureOnServer(keepAliveOnlyResult, keepAliveOnlyResponse), true)

await assertTransportOnlySseRemainsPreCommit(
  '单个超过预提交上限的 SSE comment',
  singleChunkBody(`: ${'x'.repeat(300 * 1024)}\n\n`)
)
await assertTransportOnlySseRemainsPreCommit(
  '累计超过预提交上限的 SSE comments',
  chunkSequenceBody(Array.from({ length: 20 }, (_, index) => `: ${index}-${'x'.repeat(20 * 1024)}\n\n`))
)
await assertTransportOnlySseRemainsPreCommit(
  '跨 chunk 和 CRLF 分片的 SSE comment',
  chunkSequenceBody([': keep', '-alive\r', '\n\r', '\n'])
)
await assertTransportOnlySseRemainsPreCommit(
  '只有 event 字段但没有 data 的 SSE 帧',
  singleChunkBody('event: vendor.progress\n\n')
)
await assertTransportOnlySseRemainsPreCommit(
  '只有空 data 字段的 SSE 帧',
  chunkSequenceBody(['data:', '\r\n', '\r\n'])
)
await assertTransportOnlySseRemainsPreCommit(
  'EOF 前未终止的 event 字段残片',
  singleChunkBody('event: vendor.progress')
)
await assertTransportOnlySseRemainsPreCommit(
  'EOF 前未终止的 id 字段残片',
  singleChunkBody('id: vendor-id')
)
await assertTransportOnlySseRemainsPreCommit(
  'EOF 前未终止的未知字段残片',
  singleChunkBody('vendor-meta: pending')
)
await assertTransportOnlySseRemainsPreCommit(
  'EOF 前未终止的空 data 字段残片',
  singleChunkBody('data:')
)
await assertOversizedUncommittedSseRejected(
  '超大未终止 event 字段',
  singleChunkBody(`event: ${'x'.repeat(300 * 1024)}`)
)
await assertOversizedUncommittedSseRejected(
  '超大未终止 id 字段',
  singleChunkBody(`id: ${'x'.repeat(300 * 1024)}`)
)
await assertOversizedUncommittedSseRejected(
  '超大未终止未知字段',
  singleChunkBody(`vendor-meta: ${'x'.repeat(300 * 1024)}`)
)
await assertOversizedUncommittedSseRejected(
  '同一事件累计空 data 字段超过上限',
  chunkSequenceBody(Array.from({ length: 50_000 }, () => 'data:\n'))
)
await assertCompletedNonSemanticFrameAtLimitIsCleared()

const opaqueDataState = new GatewayDownstreamCommitState()
const opaqueDataChunks: Buffer[] = []
const opaqueDataBody = 'event: vendor.progress\ndata: not-json-but-client-visible\n\n'
const opaqueDataResponse = fakeResponse({ writtenChunks: opaqueDataChunks })
const opaqueDataResult = await pipeUpstreamStream(
  singleChunkBody(opaqueDataBody),
  opaqueDataResponse,
  streamTimeoutProfile,
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'chat_completions',
    interpretProtocolFailures: false,
    retryBeforeDownstreamWriteUntilOutput: true,
    downstreamCommitState: opaqueDataState
  }
)
assert.equal(opaqueDataResult.completed, true, 'generic 客户端的普通不透明 data 事件仍应在干净 EOF 后成功透传')
assert.equal(opaqueDataResult.transportCommitted, true)
assert.equal(opaqueDataResult.semanticCommitted, true, '真实不透明 data 事件一旦写出必须锁定语义提交')
assert.equal(opaqueDataState.canRetryUpstream(), false, '普通不透明 data 事件写出后不得拼接第二个账户')
assert.equal(opaqueDataResponse.writableEnded, true)
assert.equal(Buffer.concat(opaqueDataChunks).toString('utf8'), opaqueDataBody, '普通不透明 data 事件正文必须保持原样')

const oversizedOpaqueDataChunks: Buffer[] = []
const oversizedOpaqueDataBody = `data: ${'v'.repeat(300 * 1024)}\n\n`
const oversizedOpaqueDataResult = await pipeUpstreamStream(
  singleChunkBody(oversizedOpaqueDataBody),
  fakeResponse({ writtenChunks: oversizedOpaqueDataChunks }),
  streamTimeoutProfile,
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'chat_completions',
    interpretProtocolFailures: false,
    retryBeforeDownstreamWriteUntilOutput: true
  }
)
assert.equal(oversizedOpaqueDataResult.completed, true, '超大但非空的 opaque data 仍是客户端语义，不得被 metadata 上限拦截')
assert.equal(oversizedOpaqueDataResult.semanticCommitted, true)
assert.equal(Buffer.concat(oversizedOpaqueDataChunks).toString('utf8'), oversizedOpaqueDataBody)

const visibleOutputState = new GatewayDownstreamCommitState()
const visibleOutputChunks: Buffer[] = []
const visibleOutputResponse = fakeResponse({ writtenChunks: visibleOutputChunks })
const visibleOutputResult = await pipeUpstreamStream(
  visibleOutputThenFailureBody(),
  visibleOutputResponse,
  streamTimeoutProfile,
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'chat_completions',
    interpretProtocolFailures: false,
    retryBeforeDownstreamWriteUntilOutput: true,
    downstreamCommitState: visibleOutputState
  }
)
assert.equal(visibleOutputResult.completed, false)
assert.equal(visibleOutputResult.semanticCommitted, true, '真实 Chat delta 写出后必须进入不可重放语义提交态')
assert.equal(visibleOutputState.canRetryUpstream(), false, '已可见语义后不得切号拼接第二条流')
assert.equal(shouldRetryPreCommitStreamFailureOnServer(visibleOutputResult, visibleOutputResponse), false)
assert.equal(visibleOutputResult.transportFailure, undefined, '无上游 transport provenance 的本地 iterable 异常不得污染传输电路')
assert.doesNotMatch(Buffer.concat(visibleOutputChunks).toString('utf8'), /vendor-secret-code/, '本地异常原文不得进入客户端流')

const localTransformResponse = fakeResponse()
const localTransformResult = await pipeUpstreamStream(
  singleChunkBody('data: {"choices":[{"delta":{"content":"hidden"}}]}\n\n'),
  localTransformResponse,
  streamTimeoutProfile,
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'chat_completions',
    interpretProtocolFailures: false,
    retryBeforeDownstreamWriteUntilOutput: true,
    transformUpstreamChunk: () => {
      throw new Error('vendor-secret-code local transform failed')
    }
  }
)
assert.equal(localTransformResult.completed, false)
assert.equal(localTransformResult.transportFailure, undefined, '本地 transform 异常不得伪装成上游读取中断')
assert.equal(localTransformResult.gatewayLocalFailure, true, '本地 transform 异常必须显式保持 unknown，不能伪装 framing complete')
assert.equal(localTransformResponse.headersSent, false)
assert.equal(localTransformResponse.writableEnded, false)
assert.doesNotMatch(localTransformResult.message, /vendor-secret-code/, '公开失败摘要不得包含本地异常原文')

const downstreamWriteResponse = fakeResponse({ throwOnWrite: true })
const downstreamWriteResult = await pipeUpstreamStream(
  singleChunkBody('data: {"choices":[{"delta":{"content":"write"}}]}\n\n'),
  downstreamWriteResponse,
  streamTimeoutProfile,
  Date.now(),
  async () => {},
  undefined,
  {
    responseProtocol: 'openai_v1',
    endpointFamily: 'chat_completions',
    interpretProtocolFailures: false,
    retryBeforeDownstreamWriteUntilOutput: true
  }
)
assert.equal(downstreamWriteResult.completed, false)
assert.equal(downstreamWriteResult.transportFailure, undefined, '下游 write 异常不得伪装成上游读取中断')
assert.equal(downstreamWriteResult.gatewayLocalFailure, true, '下游 write 异常必须显式保持 unknown，不能恢复账户电路')
assert.doesNotMatch(downstreamWriteResult.message, /downstream write failed/, '公开失败摘要不得包含下游异常原文')

console.log('gateway downstream commit regression passed')

async function* singleChunkBody(value: string): AsyncGenerator<Uint8Array> {
  yield Buffer.from(value)
}

async function* emptyBody(): AsyncGenerator<Uint8Array> {
  return
}

async function* chunkSequenceBody(chunks: string[]): AsyncGenerator<Uint8Array> {
  for (const chunk of chunks) yield Buffer.from(chunk)
}

async function* visibleOutputThenFailureBody(): AsyncGenerator<Uint8Array> {
  yield Buffer.from(': keep-alive\n\n')
  yield Buffer.from('data: {"choices":[{"index":0,"delta":{"content":"visible"},"finish_reason":null}]}\n\n')
  throw new Error('vendor-secret-code semantic stream interrupted')
}

async function assertTransportOnlySseRemainsPreCommit(
  label: string,
  body: AsyncIterable<Uint8Array>,
  expectedUncommittedBody?: string
): Promise<void> {
  const commitState = new GatewayDownstreamCommitState()
  const response = fakeResponse()
  const result = await pipeUpstreamStream(
    body,
    response,
    streamTimeoutProfile,
    Date.now(),
    async () => {},
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'chat_completions',
      interpretProtocolFailures: false,
      retryBeforeDownstreamWriteUntilOutput: true,
      downstreamCommitState: commitState
    }
  )
  assert.equal(result.completed, false, `${label} 不得伪装成协议完成`)
  assert.equal(result.transportCommitted, false, `${label} 不得提交下游传输`)
  assert.equal(result.semanticCommitted, false, `${label} 不得提交下游语义`)
  assert.equal(response.headersSent, false, `${label} 不得提前写出响应头`)
  assert.equal(response.writableEnded, false, `${label} 必须交回上层接管`)
  assert.equal(result.uncommittedResponseBody?.toString('utf8'), expectedUncommittedBody)
  assert.equal(shouldRetryPreCommitStreamFailureOnServer(result, response), true, `${label} 应允许服务端安全切号`)
}

async function assertOversizedUncommittedSseRejected(
  label: string,
  body: AsyncIterable<Uint8Array>
): Promise<void> {
  const response = fakeResponse()
  const result = await pipeUpstreamStream(
    body,
    response,
    streamTimeoutProfile,
    Date.now(),
    async () => {},
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'chat_completions',
      interpretProtocolFailures: false,
      retryBeforeDownstreamWriteUntilOutput: true
    }
  )
  assert.equal(result.completed, false, `${label} 不得伪装成成功流`)
  assert.equal(result.errorCode, 'stream_precommit_buffer_exceeded', `${label} 应返回稳定的本地安全上限错误码`)
  assert.equal(result.transportFailure, undefined, `${label} 属于本地 framing 防护，不得进入账户传输电路`)
  assert.equal(result.transportCommitted, false, `${label} 不得提交下游传输`)
  assert.equal(result.semanticCommitted, false, `${label} 不得提交下游语义`)
  assert.equal(result.uncommittedResponseBody, undefined, `${label} 不得把超大不完整 framing 交给最终客户端响应`)
  assert.equal(response.headersSent, false, `${label} 不得写出 HTTP 头`)
  assert.equal(response.writableEnded, false, `${label} 必须交回上层做有界切号或稳定失败`)
}

async function assertCompletedNonSemanticFrameAtLimitIsCleared(): Promise<void> {
  const prefix = 'event: '
  const visibleEvent = 'data: opaque-visible\n\n'
  const responseChunks: Buffer[] = []
  const response = fakeResponse({ writtenChunks: responseChunks })
  const result = await pipeUpstreamStream(
    chunkSequenceBody([
      `${prefix}${'x'.repeat(streamPreCommitBufferMaxBytes - Buffer.byteLength(prefix))}`,
      '\n\n',
      visibleEvent
    ]),
    response,
    streamTimeoutProfile,
    Date.now(),
    async () => {},
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'chat_completions',
      interpretProtocolFailures: false,
      retryBeforeDownstreamWriteUntilOutput: true
    }
  )
  assert.equal(result.completed, true, '上限内已闭合的非 data 事件后仍应允许后续真实 data 完成')
  assert.equal(Buffer.concat(responseChunks).toString('utf8'), visibleEvent, '被丢弃的元数据事件不得破坏后续 SSE framing')
  assert.equal(result.semanticCommitted, true)
}

function fakeResponse(input: { throwOnWrite?: boolean; writtenChunks?: Buffer[] } = {}): Response {
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
  response.write = function write(chunk?: Buffer | string) {
    if (input.throwOnWrite) throw new Error('downstream write failed')
    if (chunk !== undefined) input.writtenChunks?.push(Buffer.from(chunk))
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
