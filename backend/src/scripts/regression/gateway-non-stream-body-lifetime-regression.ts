import { strict as assert } from 'node:assert'
import http from 'node:http'
import { PassThrough } from 'node:stream'

import type { Response } from 'express'

import {
  NonStreamUpstreamBodyPipeError,
  UpstreamBodyReadMaxLifetimeError,
  pipeNonStreamUpstreamResponse,
  pipeNonStreamUpstreamResponseForInspection
} from '../../modules/gateway/upstream/body.js'
import { GatewayResponsePrecommitDeadlineError } from '../../modules/gateway/upstream/first-byte-deadline.js'

const realDateNow = Date.now

await assertBufferedBodyUsesAbsoluteLifetime(3_600_000, '图片 lane 一小时上限')
await assertImageBodyCanCrossTextPrecommitWindow()
await assertBufferedFragmentsUseSharedPrecommitDeadline()
await assertDirectBodyKeepsSharedPrecommitDeadlineAfterCommit()
await assertRealHttpDirectBodyClosesSocketAtSharedPrecommitDeadline()
await assertInspectionOverflowKeepsSharedPrecommitDeadlineAfterCommit()
await assertStreamingBodyUsesAbsoluteLifetime(270_000, '文本 lane 270 秒上限')

console.log('gateway non-stream body lifetime regression passed')

async function assertBufferedBodyUsesAbsoluteLifetime(maxLifetimeMs: number, label: string): Promise<void> {
  const startedAt = 10_000
  let now = startedAt
  let iteratorClosed = false
  const response = mockResponse()
  Date.now = () => now
  try {
    await assert.rejects(
      () => pipeNonStreamUpstreamResponseForInspection(mockDrippingBody(() => { iteratorClosed = true }), response, {
        startedAt,
        inspectBytes: 1024,
        firstByteTimeoutMs: 600_000,
        maxLifetimeMs,
        onFirstByte: () => {
          now = startedAt + maxLifetimeMs + 1
        }
      }),
      (error: unknown) => {
        assert(error instanceof UpstreamBodyReadMaxLifetimeError, `${label} 应抛出绝对正文时限错误`)
        assert.equal(error.timeoutMs, maxLifetimeMs)
        return true
      }
    )
    assert.equal(response.readableLength, 0, `${label}：检查缓冲未完成前不得向下游提交半截 JSON`)
    assert.equal(iteratorClosed, true, `${label}：到达绝对上限后必须关闭上游正文 iterator`)
  } finally {
    Date.now = realDateNow
    response.destroy()
  }
}

async function assertStreamingBodyUsesAbsoluteLifetime(maxLifetimeMs: number, label: string): Promise<void> {
  const startedAt = 20_000
  let now = startedAt
  let iteratorClosed = false
  const response = mockResponse()
  Date.now = () => now
  try {
    await assert.rejects(
      () => pipeNonStreamUpstreamResponse(mockDrippingBody(() => { iteratorClosed = true }), response, {
        startedAt,
        firstByteTimeoutMs: 120_000,
        maxLifetimeMs,
        onFirstByte: () => {
          now = startedAt + maxLifetimeMs + 1
        }
      }),
      (error: unknown) => {
        assert(error instanceof NonStreamUpstreamBodyPipeError, `${label} 已提交首块后应以正文中断收口`)
        assert(error.originalError instanceof UpstreamBodyReadMaxLifetimeError, `${label} 的根因必须保留绝对正文时限`)
        assert.equal(error.originalError.timeoutMs, maxLifetimeMs)
        assert.equal(error.partialResult.transferredBytes, 1)
        return true
      }
    )
    assert.equal(iteratorClosed, true, `${label}：到达绝对上限后必须关闭上游正文 iterator`)
  } finally {
    Date.now = realDateNow
    response.destroy()
  }
}

async function assertBufferedFragmentsUseSharedPrecommitDeadline(): Promise<void> {
  const startedAt = 30_000
  const responsePrecommitDeadlineAtMs = startedAt + 255_000
  let now = startedAt
  let iteratorClosed = false
  let fragmentReads = 0
  const response = mockResponse()
  Date.now = () => now
  try {
    await assert.rejects(
      () => pipeNonStreamUpstreamResponseForInspection(mockGradualJsonDrip(
        () => {
          fragmentReads += 1
          now += 40_000
        },
        () => { iteratorClosed = true }
      ), response, {
        startedAt,
        inspectBytes: 1024,
        firstByteTimeoutMs: 600_000,
        maxLifetimeMs: 3_600_000,
        responsePrecommitDeadlineAtMs
      }),
      (error: unknown) => {
        assert(error instanceof GatewayResponsePrecommitDeadlineError, 'JSON 碎片滴流必须归因到共享请求墙钟')
        assert.equal(error.deadlineAtMs, responsePrecommitDeadlineAtMs)
        return true
      }
    )
    assert(fragmentReads > 1, 'JSON 碎片必须经过多次渐进滴流，证明每块活动不会重置绝对墙钟')
    assert.equal(response.readableLength, 0, '共享墙钟到期前缓冲的半截 JSON 不得写入下游')
    assert.equal(iteratorClosed, true, '共享墙钟到期必须关闭 JSON 滴流 iterator')
  } finally {
    Date.now = realDateNow
    response.destroy()
  }
}

async function assertImageBodyCanCrossTextPrecommitWindow(): Promise<void> {
  const startedAt = 40_000
  let now = startedAt
  const response = mockResponse()
  const body: AsyncIterable<Uint8Array> = {
    [Symbol.asyncIterator]() {
      let readCount = 0
      return {
        next: async (): Promise<IteratorResult<Uint8Array>> => {
          if (readCount++ === 0) {
            now = startedAt + 270_001
            return { done: false, value: Buffer.from('{"data":[]}') }
          }
          return { done: true, value: undefined }
        }
      }
    }
  }
  Date.now = () => now
  try {
    const result = await pipeNonStreamUpstreamResponseForInspection(body, response, {
      startedAt,
      inspectBytes: 1024,
      firstByteTimeoutMs: 600_000,
      maxLifetimeMs: 3_600_000
    })
    assert.equal(result.fullyBuffered, true)
    assert.equal(result.completeBodyText, '{"data":[]}', '图片 lane 未传共享文本墙钟时应允许跨过 270 秒')
  } finally {
    Date.now = realDateNow
    response.destroy()
  }
}

async function assertDirectBodyKeepsSharedPrecommitDeadlineAfterCommit(): Promise<void> {
  const startedAt = 50_000
  const responsePrecommitDeadlineAtMs = startedAt + 255_000
  let now = startedAt
  let iteratorClosed = false
  const response = mockResponse()
  Date.now = () => now
  try {
    await assert.rejects(
      () => pipeNonStreamUpstreamResponse(twoChunkBody({
        first: '{',
        second: '"late":true}',
        beforeSecond: () => { now = responsePrecommitDeadlineAtMs + 1 },
        onClose: () => { iteratorClosed = true }
      }), response, {
        startedAt,
        maxLifetimeMs: 3_600_000,
        responsePrecommitDeadlineAtMs
      }),
      (error: unknown) => {
        assert(error instanceof NonStreamUpstreamBodyPipeError, 'raw JSON 首块已提交后应以已提交正文中断收口')
        assert(error.originalError instanceof GatewayResponsePrecommitDeadlineError, 'raw JSON 子读取必须保留共享请求墙钟归因')
        assert.equal(error.originalError.deadlineAtMs, responsePrecommitDeadlineAtMs)
        assert.equal(error.partialResult.transferredBytes, 1, '越过墙钟的第二块不得再写给客户端')
        return true
      }
    )
    assert.equal(iteratorClosed, true, 'raw JSON 墙钟到期必须关闭上游 iterator')
  } finally {
    Date.now = realDateNow
    response.destroy()
  }
}

async function assertInspectionOverflowKeepsSharedPrecommitDeadlineAfterCommit(): Promise<void> {
  const startedAt = 60_000
  const responsePrecommitDeadlineAtMs = startedAt + 255_000
  let now = startedAt
  let iteratorClosed = false
  const firstChunk = '{"oversized":'
  const response = mockResponse()
  Date.now = () => now
  try {
    await assert.rejects(
      () => pipeNonStreamUpstreamResponseForInspection(twoChunkBody({
        first: firstChunk,
        second: '"late"}',
        beforeSecond: () => { now = responsePrecommitDeadlineAtMs + 1 },
        onClose: () => { iteratorClosed = true }
      }), response, {
        startedAt,
        inspectBytes: 4,
        maxLifetimeMs: 3_600_000,
        responsePrecommitDeadlineAtMs
      }),
      (error: unknown) => {
        assert(error instanceof NonStreamUpstreamBodyPipeError, '检查缓冲溢出并开始透传后应以已提交正文中断收口')
        assert(error.originalError instanceof GatewayResponsePrecommitDeadlineError, '检查降级透传不得丢失文本墙钟')
        assert.equal(error.originalError.deadlineAtMs, responsePrecommitDeadlineAtMs)
        assert.equal(error.partialResult.transferredBytes, Buffer.byteLength(firstChunk), '检查降级后越墙钟的后续块不得写入下游')
        return true
      }
    )
    assert.equal(iteratorClosed, true, '检查降级透传越墙钟后必须关闭上游 iterator')
  } finally {
    Date.now = realDateNow
    response.destroy()
  }
}

async function assertRealHttpDirectBodyClosesSocketAtSharedPrecommitDeadline(): Promise<void> {
  const startedAt = 70_000
  const responsePrecommitDeadlineAtMs = startedAt + 255_000
  let now = startedAt
  let releaseTail: (() => void) | undefined
  let upstreamSocketClosed = false
  let released = false
  const upstream = http.createServer((_, res) => {
    const tail = '"late":true}'
    res.once('close', () => { upstreamSocketClosed = true })
    res.writeHead(200, {
      'content-type': 'application/json; charset=utf-8',
      'content-length': String(Buffer.byteLength(`{${tail}`))
    })
    res.flushHeaders()
    res.write('{')
    releaseTail = () => {
      now = responsePrecommitDeadlineAtMs + 1
      if (res.destroyed || res.writableEnded) return
      res.write(tail)
      setTimeout(() => {
        if (!res.destroyed && !res.writableEnded) res.end()
      }, 25).unref()
    }
  })
  await listen(upstream)
  const address = upstream.address()
  assert(typeof address === 'object' && address !== null)
  const upstreamResponse = await fetch(`http://127.0.0.1:${address.port}/slow-json`)
  assert(upstreamResponse.body, '真实 HTTP 上游必须返回可读 body')
  const downstream = mockResponse()
  const written: Buffer[] = []
  downstream.on('data', (chunk: Buffer) => {
    written.push(Buffer.from(chunk))
    if (released) return
    released = true
    const release = releaseTail
    assert(release, '首个 { 到达下游后必须可释放真实 HTTP 后续块')
    release()
  })
  Date.now = () => now
  try {
    await assert.rejects(
      () => pipeNonStreamUpstreamResponse(upstreamResponse.body as AsyncIterable<Uint8Array>, downstream, {
        startedAt,
        maxLifetimeMs: 3_600_000,
        responsePrecommitDeadlineAtMs
      }),
      (error: unknown) => {
        assert(error instanceof NonStreamUpstreamBodyPipeError, '真实 HTTP raw JSON 已提交后应以正文中断收口')
        assert(error.originalError instanceof GatewayResponsePrecommitDeadlineError, '真实 HTTP raw JSON 必须保留墙钟归因')
        return true
      }
    )
    await waitFor(() => upstreamSocketClosed, '真实 HTTP raw JSON 墙钟必须关闭上游 socket')
    assert.equal(Buffer.concat(written).toString('utf8'), '{', '真实 HTTP raw JSON 只能保留墙钟前已提交的首个 {')
  } finally {
    Date.now = realDateNow
    downstream.destroy()
    await closeServer(upstream)
  }
}

function mockGradualJsonDrip(onRead: () => void, onClose: () => void): AsyncIterable<Uint8Array> {
  const fragments = ['{"choices":[', '{"message":', '"still', '-not', '-complete', '"', ',']
  let index = 0
  return {
    [Symbol.asyncIterator]() {
      return {
        async next(): Promise<IteratorResult<Uint8Array>> {
          onRead()
          const value = fragments[index % fragments.length]!
          index += 1
          return { done: false, value: Buffer.from(value) }
        },
        async return(): Promise<IteratorResult<Uint8Array>> {
          onClose()
          return { done: true, value: undefined }
        }
      }
    }
  }
}

function mockDrippingBody(onClose: () => void): AsyncIterable<Uint8Array> {
  let first = true
  return {
    [Symbol.asyncIterator]() {
      return {
        async next(): Promise<IteratorResult<Uint8Array>> {
          if (first) {
            first = false
            return { done: false, value: Buffer.from('x') }
          }
          return new Promise<IteratorResult<Uint8Array>>(() => {})
        },
        async return(): Promise<IteratorResult<Uint8Array>> {
          onClose()
          return { done: true, value: undefined }
        }
      }
    }
  }
}

function twoChunkBody(input: {
  first: string
  second: string
  beforeSecond: () => void
  onClose: () => void
}): AsyncIterable<Uint8Array> {
  let index = 0
  return {
    [Symbol.asyncIterator]() {
      return {
        async next(): Promise<IteratorResult<Uint8Array>> {
          index += 1
          if (index === 1) return { done: false, value: Buffer.from(input.first) }
          if (index === 2) {
            input.beforeSecond()
            return { done: false, value: Buffer.from(input.second) }
          }
          return { done: true, value: undefined }
        },
        async return(): Promise<IteratorResult<Uint8Array>> {
          input.onClose()
          return { done: true, value: undefined }
        }
      }
    }
  }
}

function mockResponse(): PassThrough & Response {
  const response = new PassThrough() as PassThrough & Response
  response.locals = {}
  return response
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1')
  })
}

function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

async function waitFor(predicate: () => boolean, message: string): Promise<void> {
  const deadline = realDateNow() + 1_000
  while (!predicate() && realDateNow() < deadline) {
    await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 5))
  }
  assert.equal(predicate(), true, message)
}
