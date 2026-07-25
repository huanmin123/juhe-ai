import { strict as assert } from 'node:assert'
import { EventEmitter } from 'node:events'

import type { Response } from 'express'

import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'

const timeoutProfile = {
  firstResponseTimeoutMs: 5_000,
  firstByteTimeoutMs: 5_000,
  idleTimeoutMs: 5_000,
  uncommittedAttemptMaxLifetimeMs: 30_000,
  noAvailableAccountWaitMs: 5_000
}

await assertPreciseCommittedThrowIsSanitized()
await assertGenericCommittedThrowDisconnects()
await assertFailureAfterWrittenTerminalDisconnects()
await assertEofFlushFailureIsSanitized()
await assertTerminalWriteFailureCannotBecomeSuccess()
await assertFailureSignalWriteFailureDisconnects()

console.log('gateway stream committed failure regression passed')

async function assertPreciseCommittedThrowIsSanitized(): Promise<void> {
  const response = fakeResponse()
  const result = await pipe(upstreamOutputThenThrow(), response, {
    committedFailureSignal: 'protocol_error_event'
  })
  const body = writtenText(response)
  assert.equal(result.completed, false)
  assert.equal(result.transportFailure, undefined, '无 transport provenance 的 iterable 异常必须保持请求级中性')
  assert.equal(result.gatewayLocalFailure, true, '无 provenance 的异常必须显式报告 gateway local unknown')
  assert.equal(count(body, 'event: response.failed'), 1, '精确客户端必须且只能收到一个受控失败终态')
  assert.match(body, /upstream_stream_interrupted/)
  assert.match(body, /上游流式响应在输出后中断/)
  assert.doesNotMatch(body, /vendor-secret-code|vendor-private-message/)
  assert.equal(response.endCalls, 1)
  assert.equal(response.destroyCalls, 0)
}

async function assertGenericCommittedThrowDisconnects(): Promise<void> {
  const response = fakeResponse()
  const result = await pipe(upstreamOutputThenThrow(), response, {
    committedFailureSignal: 'disconnect'
  })
  const body = writtenText(response)
  assert.equal(result.completed, false)
  assert.equal(count(body, 'event: response.failed'), 0, '通用客户端不得被拼接网关协议事件')
  assert.doesNotMatch(body, /vendor-secret-code|vendor-private-message/)
  assert.equal(response.endCalls, 0, '通用客户端不能用正常 EOF 伪装成功')
  assert.equal(response.destroyCalls, 1)
}

async function assertFailureAfterWrittenTerminalDisconnects(): Promise<void> {
  for (const committedFailureSignal of ['protocol_error_event', 'disconnect'] as const) {
    const response = fakeResponse()
    const result = await pipe(successTerminalThenFailure(), response, { committedFailureSignal })
    const body = writtenText(response)
    assert.equal(result.completed, false)
    assert.equal(count(body, 'event: response.completed'), 1, `${committedFailureSignal} 必须保留已写成功终态`)
    assert.equal(count(body, 'event: response.failed'), 0, `${committedFailureSignal} 不得追加矛盾的第二终态`)
    assert.doesNotMatch(body, /vendor-secret-code|vendor-private-message/)
    assert.equal(response.endCalls, 0, `${committedFailureSignal} 不得在矛盾终态后正常结束`)
    assert.equal(response.destroyCalls, 1, `${committedFailureSignal} 必须中断矛盾流`)
  }
}

async function assertEofFlushFailureIsSanitized(): Promise<void> {
  const response = fakeResponse()
  const result = await pipe(singleChunkBody(outputEvent()), response, {
    committedFailureSignal: 'protocol_error_event',
    transformUpstreamChunk: (chunk) => [chunk],
    flushTransformedUpstreamChunks: () => [failureEvent()]
  })
  const body = writtenText(response)
  assert.equal(result.completed, false)
  assert.equal(count(body, 'event: response.failed'), 1, 'EOF flush 失败必须只产生一个受控失败终态')
  assert.match(body, /upstream_stream_interrupted/)
  assert.doesNotMatch(body, /vendor-secret-code|vendor-private-message/)
  assert.equal(response.endCalls, 1)
  assert.equal(response.destroyCalls, 0)
}

async function assertTerminalWriteFailureCannotBecomeSuccess(): Promise<void> {
  const response = fakeResponse({
    throwOnWrite: (chunk) => chunk.includes('event: response.completed')
  })
  const result = await pipe(singleChunkBody(completedEvent()), response, {
    committedFailureSignal: 'protocol_error_event'
  })
  const body = writtenText(response)
  assert.equal(result.completed, false, '解析到终态但写入失败时不得误报成功')
  assert.equal(result.transportFailure, undefined, '下游 write 失败不得污染上游传输电路')
  assert.equal(result.gatewayLocalFailure, true, '下游 write 失败不得被误报为 framing complete')
  assert.equal(count(body, 'event: response.completed'), 0)
  assert.equal(count(body, 'event: response.failed'), 1, '终态写失败后仍可写时只能输出受控失败终态')
  assert.doesNotMatch(body, /vendor-secret-code|downstream terminal write failed/)
  assert.equal(response.endCalls, 1)
  assert.equal(response.destroyCalls, 0)
}

async function assertFailureSignalWriteFailureDisconnects(): Promise<void> {
  const response = fakeResponse({
    throwOnWrite: (chunk) => chunk.includes('event: response.failed')
  })
  const result = await pipe(upstreamOutputThenThrow(), response, {
    committedFailureSignal: 'protocol_error_event'
  })
  const body = writtenText(response)
  assert.equal(result.completed, false)
  assert.equal(count(body, 'event: response.failed'), 0, '写失败的受控终态不能计为已发送')
  assert.equal(response.endCalls, 0, '受控终态写失败后不能正常 EOF')
  assert.equal(response.destroyCalls, 1, '受控终态写失败必须降级为断连')
}

interface PipeOptions {
  committedFailureSignal: 'protocol_error_event' | 'disconnect'
  transformUpstreamChunk?: (chunk: Buffer) => Buffer[]
  flushTransformedUpstreamChunks?: () => Buffer[]
}

async function pipe(
  upstreamBody: AsyncIterable<Uint8Array>,
  response: FakeResponse,
  options: PipeOptions
) {
  return pipeUpstreamStream(
    upstreamBody,
    response as unknown as Response,
    timeoutProfile,
    Date.now(),
    async () => {},
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'responses',
      downstreamProtocol: 'responses_sse',
      interpretProtocolFailures: true,
      retryBeforeDownstreamWriteUntilOutput: true,
      committedFailureSignal: options.committedFailureSignal,
      transformUpstreamChunk: options.transformUpstreamChunk,
      flushTransformedUpstreamChunks: options.flushTransformedUpstreamChunks
    }
  )
}

async function* upstreamOutputThenThrow(): AsyncGenerator<Uint8Array> {
  yield outputEvent()
  throw new Error('vendor-secret-code vendor-private-message')
}

async function* successTerminalThenFailure(): AsyncGenerator<Uint8Array> {
  yield completedEvent()
  yield failureEvent()
}

async function* singleChunkBody(chunk: Buffer): AsyncGenerator<Uint8Array> {
  yield chunk
}

function outputEvent(): Buffer {
  return sseEvent('response.output_text.delta', {
    type: 'response.output_text.delta',
    delta: 'visible'
  })
}

function completedEvent(): Buffer {
  return sseEvent('response.completed', {
    type: 'response.completed',
    response: { status: 'completed' }
  })
}

function failureEvent(): Buffer {
  return sseEvent('response.failed', {
    type: 'response.failed',
    response: {
      status: 'failed',
      error: {
        code: 'vendor-secret-code',
        message: 'vendor-private-message'
      }
    }
  })
}

function sseEvent(type: string, payload: unknown): Buffer {
  return Buffer.from(`event: ${type}\ndata: ${JSON.stringify(payload)}\n\n`, 'utf8')
}

interface FakeResponse extends EventEmitter {
  headersSent: boolean
  writableEnded: boolean
  destroyed: boolean
  writableLength: number
  writableHighWaterMark: number
  writtenChunks: Buffer[]
  endCalls: number
  destroyCalls: number
  locals: Record<string, unknown>
  status: () => FakeResponse
  setHeader: () => FakeResponse
  hasHeader: () => boolean
  write: (chunk?: Buffer | string) => boolean
  end: () => FakeResponse
  destroy: () => FakeResponse
}

function fakeResponse(input: { throwOnWrite?: (chunk: string) => boolean } = {}): FakeResponse {
  const response = new EventEmitter() as FakeResponse
  response.headersSent = false
  response.writableEnded = false
  response.destroyed = false
  response.writableLength = 0
  response.writableHighWaterMark = 16_384
  response.writtenChunks = []
  response.endCalls = 0
  response.destroyCalls = 0
  response.locals = {}
  response.status = () => response
  response.setHeader = () => response
  response.hasHeader = () => false
  response.write = (chunk?: Buffer | string) => {
    const buffer = Buffer.from(chunk ?? '')
    const text = buffer.toString('utf8')
    if (input.throwOnWrite?.(text)) throw new Error('downstream terminal write failed vendor-secret-code')
    response.writtenChunks.push(buffer)
    response.headersSent = true
    return true
  }
  response.end = () => {
    response.endCalls += 1
    response.headersSent = true
    response.writableEnded = true
    return response
  }
  response.destroy = () => {
    response.destroyCalls += 1
    response.destroyed = true
    return response
  }
  return response
}

function writtenText(response: FakeResponse): string {
  return Buffer.concat(response.writtenChunks).toString('utf8')
}

function count(value: string, needle: string): number {
  return value.split(needle).length - 1
}
