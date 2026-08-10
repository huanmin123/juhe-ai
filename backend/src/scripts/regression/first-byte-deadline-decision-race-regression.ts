import { strict as assert } from 'node:assert'
import { EventEmitter, getEventListeners } from 'node:events'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import type { Socket } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setImmediate as waitForImmediate } from 'node:timers/promises'

import type { Response } from 'express'
import express from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  clearSpeedFirstCutoverReservationsForTest,
  reserveSpeedFirstCutoverTarget,
  speedFirstCutoverBudgetSnapshot,
  type SpeedFirstCutoverReservation
} from '../../modules/gateway/runtime/speed-first-cutover-reservation.service.js'
import { pipeUpstreamStream } from '../../modules/gateway/response/stream.js'
import { pipeNonStreamUpstreamResponse } from '../../modules/gateway/upstream/body.js'
import {
  decideFirstByteDeadlineAfterPendingRead,
  observeFirstBytePendingRead
} from '../../modules/gateway/upstream/first-byte-deadline.js'
import {
  closeGatewayUpstreamAgents,
  isStartedUpstreamTransportError,
  requestUpstream,
  UpstreamRequestAbortedError,
  type GatewayUpstreamResponse
} from '../../modules/gateway/upstream/request.js'
import { GatewayFirstByteTimeoutError } from '../../modules/gateway/upstream/first-byte-timeout.js'
import { clearAccountConcurrency, getAccountCurrentConcurrency } from '../../shared/account-concurrency.js'
import { logger } from '../../shared/logger.js'

type Deferred<T> = {
  promise: Promise<T>
  resolve: (value: T | PromiseLike<T>) => void
  reject: (reason?: unknown) => void
}

const previousAllowPrivateBaseUrls = runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true

try {
  await verifyResponseHeaderDeadlineRace('node')
  await verifyResponseHeaderDeadlineRace('fetch')
  await verifyResponseHeaderFailureProvenance('node')
  await verifyResponseHeaderFailureProvenance('fetch')
  await verifyNodeSseHeartbeatPrecommitCancellationClosesSocket()
  await verifyStreamPrecommitOverflowCancellationReleasesTimers()
  await verifyNonStreamBodyDeadlineRace()
  await verifyStreamBodyDeadlineRace()
  await verifyOpaqueDataSupersedesPendingStreamDeadline()
  await verifyStreamHeartbeatDoesNotSupersedeDeadlineRace()
  await verifyStreamHeartbeatCannotBypassResponsePrecommitDeadline()
  await verifyStreamHeartbeatDecisionCannotOutliveResponsePrecommitDeadline()
  await verifyPendingReadRejectionRemainsAuthoritative()
  await verifyFetchNoBodyDecisionDoesNotOutliveTransportTerminal()
  await verifyNodeAbortDecisionDoesNotRetainTransportResources()
  await verifyGatewayReservationOwnershipForDetachedDeadlineTerminals()
  console.log('first-byte deadline decision race regression passed')
} finally {
  closeGatewayUpstreamAgents()
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = previousAllowPrivateBaseUrls
}

async function verifyResponseHeaderDeadlineRace(transport: 'node' | 'fetch'): Promise<void> {
  const requestObserved = deferred<void>()
  const releaseHeaders = deferred<void>()
  const releaseBody = deferred<void>()
  const deadlineObserved = deferred<void>()
  const deadlineDecision = deferred<'abort'>()
  const deadlineDecisionReturned = deferred<void>()
  const server = http.createServer(async (_req, res) => {
    requestObserved.resolve()
    await releaseHeaders.promise
    res.writeHead(200, { 'content-type': 'text/plain' })
    res.flushHeaders()
    await releaseBody.promise
    res.end(`${transport}-deadline-race-ok`)
  })

  try {
    await listen(server)
    const request = requestUpstream(`http://127.0.0.1:${serverPort(server)}/deadline-race`, {
      method: 'GET',
      headers: new Headers(),
      transport,
      firstByteDeadlineMs: 5,
      onFirstByteDeadline: async () => {
        deadlineObserved.resolve()
        const action = await deadlineDecision.promise
        deadlineDecisionReturned.resolve()
        return action
      }
    })

    await withTimeout(requestObserved.promise, `${transport} transport request was not observed`)
    await withTimeout(deadlineObserved.promise, `${transport} transport deadline was not observed`)
    releaseHeaders.resolve()
    const response = await withTimeout(request, `${transport} transport response headers were not returned`)

    deadlineDecision.resolve('abort')
    await withTimeout(deadlineDecisionReturned.promise, `${transport} transport deadline decision did not return`)
    await waitForImmediate()
    releaseBody.resolve()

    assert.equal(
      await withTimeout(readResponseBody(response), `${transport} transport response body was aborted by a stale decision`),
      `${transport}-deadline-race-ok`,
      `${transport} transport must keep a response whose headers arrived while the async deadline decision was pending`
    )
  } finally {
    releaseHeaders.resolve()
    releaseBody.resolve()
    deadlineDecision.resolve('abort')
    await closeServer(server)
  }
}

async function verifyResponseHeaderFailureProvenance(transport: 'node' | 'fetch'): Promise<void> {
  for (const callbackMode of ['sync_throw', 'async_reject'] as const) {
    const localError = new Error(`${transport}-${callbackMode}-local-decision-secret`)
    const error = await captureResponseHeaderFailure({
      transport,
      onFirstByteDeadline: callbackMode === 'sync_throw'
        ? () => { throw localError }
        : async () => { throw localError }
    })
    assert.equal(error, localError, `${transport}/${callbackMode} 必须保留原本地异常供内部诊断`)
    assert.equal(
      isStartedUpstreamTransportError(error),
      false,
      `${transport}/${callbackMode} 本地首字决策异常不得成为上游 transport evidence`
    )
  }

  const configuredDeadlineError = await captureResponseHeaderFailure({
    transport,
    onFirstByteDeadline: () => 'abort'
  })
  assert(configuredDeadlineError instanceof GatewayFirstByteTimeoutError)
  assert.equal(configuredDeadlineError.source, 'configured_deadline')
  assert.equal(
    isStartedUpstreamTransportError(configuredDeadlineError),
    false,
    `${transport} 配置首字截止是路由决策，不得成为上游 transport evidence`
  )

  const abortController = new AbortController()
  const clientAbortError = await captureResponseHeaderFailure({
    transport,
    signal: abortController.signal,
    afterRequestObserved: () => abortController.abort()
  })
  assert(clientAbortError instanceof UpstreamRequestAbortedError)
  assert.equal(clientAbortError.upstreamRequestStarted, true, `${transport} 客户端取消应保留已派发审计事实`)
  assert.equal(
    isStartedUpstreamTransportError(clientAbortError),
    false,
    `${transport} 客户端取消不得成为上游 transport evidence`
  )

  const transportError = await captureResponseHeaderFailure({
    transport,
    destroyUpstreamSocket: true
  })
  assert.equal(
    isStartedUpstreamTransportError(transportError),
    true,
    `${transport} 真实响应头前 socket reset 必须保留 transport evidence`
  )
}

async function captureResponseHeaderFailure(input: {
  transport: 'node' | 'fetch'
  onFirstByteDeadline?: () => 'abort' | Promise<'abort'>
  signal?: AbortSignal
  afterRequestObserved?: () => void
  destroyUpstreamSocket?: boolean
}): Promise<unknown> {
  const requestObserved = deferred<void>()
  const sockets = new Set<Socket>()
  const server = http.createServer((req) => {
    req.resume()
    requestObserved.resolve()
    if (input.destroyUpstreamSocket) req.socket.destroy()
  })
  server.on('connection', (socket) => {
    sockets.add(socket)
    socket.once('close', () => sockets.delete(socket))
  })
  try {
    await listen(server)
    const failure = requestUpstream(`http://127.0.0.1:${serverPort(server)}/provenance`, {
      method: 'GET',
      headers: new Headers(),
      transport: input.transport,
      firstByteDeadlineMs: input.onFirstByteDeadline ? 5 : undefined,
      onFirstByteDeadline: input.onFirstByteDeadline,
      signal: input.signal
    }).then(
      () => Promise.reject(new Error(`${input.transport} 夹具必须失败`)),
      (error: unknown) => error
    )
    await withTimeout(requestObserved.promise, `${input.transport} provenance 夹具未命中上游`)
    input.afterRequestObserved?.()
    return await withTimeout(failure, `${input.transport} provenance 夹具未按预期失败`)
  } finally {
    for (const socket of sockets) socket.destroy()
    await closeServer(server)
  }
}

async function verifyNodeSseHeartbeatPrecommitCancellationClosesSocket(): Promise<void> {
  const responseClosed = deferred<void>()
  let serverResponse: http.ServerResponse | undefined
  const server = http.createServer((_req, res) => {
    serverResponse = res
    res.once('close', () => responseClosed.resolve())
    res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
    res.flushHeaders()
    res.write(': keep-alive\n\n')
  })

  try {
    await listen(server)
    const upstreamResponse = await requestUpstream(`http://127.0.0.1:${serverPort(server)}/sse-heartbeat`, {
      method: 'GET',
      headers: new Headers(),
      transport: 'node'
    })
    assert(upstreamResponse.body, 'node SSE cancellation test requires a response body')
    const timerTracker = trackGlobalTimeouts()
    try {
      const startedAt = Date.now()
      const downstream = fakeResponse()
      const result = await withTimeout(pipeUpstreamStream(
        upstreamResponse.body,
        downstream,
        {
          firstResponseTimeoutMs: 2_000,
          firstByteTimeoutMs: 2_000,
          idleTimeoutMs: 2_000,
          uncommittedAttemptMaxLifetimeMs: 10_000,
          noAvailableAccountWaitMs: 2_000
        },
        startedAt,
        async () => {},
        undefined,
        {
          responseProtocol: 'openai_v1',
          endpointFamily: 'chat_completions',
          retryBeforeDownstreamWriteUntilOutput: true,
          responsePrecommitDeadlineAtMs: startedAt + 80
        }
      ), 'node SSE heartbeat did not stop at the response precommit deadline')

      assert.equal(result.errorCode, 'gateway_request_wall_budget_exhausted')
      assert.equal(result.transportFailure, undefined)
      assert.equal(result.semanticCommitted, false)
      assert.equal(downstream.writtenText(), '')
      await withTimeout(responseClosed.promise, 'node SSE body cancellation did not close the upstream socket')
      await waitForImmediate()
      assert.equal(timerTracker.activeCount(), 0, 'SSE 墙钟结束后 read/close deadline timers 必须全部清零')
    } finally {
      timerTracker.restore()
    }
  } finally {
    serverResponse?.destroy()
    await closeServer(server)
  }
}

async function verifyStreamPrecommitOverflowCancellationReleasesTimers(): Promise<void> {
  const pendingRead = deferred<IteratorResult<Uint8Array>>()
  let readCount = 0
  let iteratorClosed = false
  const body: AsyncIterable<Uint8Array> = {
    [Symbol.asyncIterator](): AsyncIterator<Uint8Array> {
      return {
        next: () => {
          if (readCount++ === 0) {
            return Promise.resolve({ done: false, value: Buffer.alloc(300 * 1024, 0x78) })
          }
          return pendingRead.promise
        },
        return: () => {
          iteratorClosed = true
          return Promise.resolve({ done: true, value: undefined })
        }
      }
    }
  }
  const timerTracker = trackGlobalTimeouts()
  try {
    const startedAt = Date.now()
    const downstream = fakeResponse()
    const result = await withTimeout(pipeUpstreamStream(
      body,
      downstream,
      {
        firstResponseTimeoutMs: 2_000,
        firstByteTimeoutMs: 2_000,
        idleTimeoutMs: 2_000,
        uncommittedAttemptMaxLifetimeMs: 10_000,
        noAvailableAccountWaitMs: 2_000
      },
      startedAt,
      async () => {},
      undefined,
      {
        responseProtocol: 'openai_v1',
        endpointFamily: 'chat_completions',
        retryBeforeDownstreamWriteUntilOutput: true,
        responsePrecommitDeadlineAtMs: startedAt + 80
      }
    ), 'oversized precommit stream did not stop at the safety buffer limit')

    assert.equal(result.errorCode, 'stream_precommit_buffer_exceeded')
    assert.equal(result.transportFailure, undefined)
    assert.equal(result.gatewayLocalFailure, true)
    assert.equal(result.semanticCommitted, false)
    assert.equal(result.upstreamResponseBytesWritten, 0)
    assert.equal(downstream.writtenText(), '')
    assert.equal(downstream.destroyed, false, '预提交失败必须把未提交下游留给上层写入稳定网关错误')
    assert.equal(iteratorClosed, true)
    await waitForImmediate()
    assert.equal(timerTracker.activeCount(), 0, '流式预提交缓冲溢出收口后，所有 deadline timers 必须清零')
  } finally {
    timerTracker.restore()
  }
}

async function verifyNonStreamBodyDeadlineRace(): Promise<void> {
  const pendingRead = controlledUpstreamBody(Buffer.from('non-stream-deadline-race-ok'))
  const deadlineObserved = deferred<void>()
  const deadlineDecision = deferred<'abort'>()
  const response = fakeResponse()
  let supersededCount = 0
  const pipe = pipeNonStreamUpstreamResponse(pendingRead.body, response, {
    startedAt: Date.now(),
    firstByteTimeoutMs: 2_000,
    firstByteDeadlineMs: 5,
    onFirstByteDeadline: async () => {
      deadlineObserved.resolve()
      return deadlineDecision.promise
    },
    onFirstByteDeadlineSuperseded: () => { supersededCount += 1 }
  })

  await withTimeout(deadlineObserved.promise, 'non-stream body deadline was not observed')
  pendingRead.resolveFirstChunk()
  await waitForImmediate()
  deadlineDecision.reject(new Error('stale deadline decision failure'))
  await withTimeout(pipe, 'non-stream body was aborted by a stale decision')

  assert.equal(
    response.writtenText(),
    'non-stream-deadline-race-ok',
    'non-stream pending read must win when it completes during the async deadline decision'
  )
  assert.equal(supersededCount, 1, 'non-stream read winner must release the stale cutover reservation exactly once')
}

async function verifyPendingReadRejectionRemainsAuthoritative(): Promise<void> {
  const read = deferred<number>()
  const decision = deferred<'abort'>()
  const expectedReadError = new Error('pending read failed during deadline decision')
  const result = decideFirstByteDeadlineAfterPendingRead(
    observeFirstBytePendingRead(read.promise),
    () => decision.promise,
    { elapsedMs: 5, timeoutMs: 5, transport: 'stream' }
  )

  read.reject(expectedReadError)
  await waitForImmediate()
  decision.resolve('abort')
  await assert.rejects(
    withTimeout(result, 'pending read rejection was lost behind the deadline decision'),
    (error: unknown) => error === expectedReadError,
    'a settled pending-read error must win over a stale deadline abort without becoming unhandled'
  )
}

async function verifyStreamBodyDeadlineRace(): Promise<void> {
  const streamChunk = Buffer.from([
    'data: {"id":"chatcmpl_race","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}',
    '',
    'data: {"id":"chatcmpl_race","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}',
    '',
    'data: [DONE]',
    '',
    ''
  ].join('\n'))
  const pendingRead = controlledUpstreamBody(streamChunk)
  const deadlineObserved = deferred<void>()
  const deadlineDecision = deferred<'abort'>()
  const response = fakeResponse()
  let streamFailure: string | undefined
  let supersededCount = 0
  const pipe = pipeUpstreamStream(
    pendingRead.body,
    response,
    {
      firstResponseTimeoutMs: 2_000,
      firstByteTimeoutMs: 2_000,
      idleTimeoutMs: 2_000,
      uncommittedAttemptMaxLifetimeMs: 10_000,
      noAvailableAccountWaitMs: 2_000
    },
    Date.now(),
    async (reason) => {
      streamFailure = reason
    },
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'chat_completions',
      firstByteDeadlineMs: 5,
      onFirstByteDeadline: async () => {
        deadlineObserved.resolve()
        return deadlineDecision.promise
      },
      onFirstByteDeadlineSuperseded: () => { supersededCount += 1 }
    }
  )

  await withTimeout(deadlineObserved.promise, 'stream body deadline was not observed')
  pendingRead.resolveFirstChunk()
  await waitForImmediate()
  deadlineDecision.resolve('abort')
  const result = await withTimeout(pipe, 'stream body was aborted by a stale decision')

  assert.equal(streamFailure, undefined, `stream body should not fail after its pending read completed: ${streamFailure ?? ''}`)
  assert.equal(result.completed, true, 'stream body should complete after its pending read wins the decision race')
  assert.equal(supersededCount, 1, 'stream read winner must release the stale cutover reservation exactly once')
  assert.match(response.writtenText(), /chatcmpl_race/)
}

async function verifyOpaqueDataSupersedesPendingStreamDeadline(): Promise<void> {
  const pendingRead = controlledUpstreamBody(Buffer.from('opaque raw read'))
  const deadlineObserved = deferred<void>()
  const deadlineDecision = deferred<'abort'>()
  const response = fakeResponse()
  let supersededCount = 0
  const pipe = pipeUpstreamStream(
    pendingRead.body,
    response,
    {
      firstResponseTimeoutMs: 2_000,
      firstByteTimeoutMs: 2_000,
      idleTimeoutMs: 2_000,
      uncommittedAttemptMaxLifetimeMs: 10_000,
      noAvailableAccountWaitMs: 2_000
    },
    Date.now(),
    async () => {},
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'chat_completions',
      interpretProtocolFailures: false,
      retryBeforeDownstreamWriteUntilOutput: true,
      firstByteDeadlineMs: 5,
      onFirstByteDeadline: async () => {
        deadlineObserved.resolve()
        return deadlineDecision.promise
      },
      onFirstByteDeadlineSuperseded: () => { supersededCount += 1 },
      transformUpstreamChunk: () => [
        Buffer.from('event: vendor.progress\n'),
        Buffer.from('data: opaque-visible\n\n')
      ]
    }
  )

  await withTimeout(deadlineObserved.promise, 'opaque stream deadline was not observed')
  pendingRead.resolveFirstChunk()
  await waitForImmediate()
  deadlineDecision.resolve('abort')
  const result = await withTimeout(pipe, 'opaque data lost to a stale first-byte decision')

  assert.equal(result.completed, true, '非空 opaque data 是客户端可见语义，必须胜过迟到首字切换')
  assert.equal(result.semanticCommitted, true, 'opaque data 写出后必须锁定不可重放')
  assert.equal(result.errorCode, undefined)
  assert.equal(result.transportFailure, undefined)
  assert.equal(supersededCount, 1, 'opaque data 必须释放迟到的切号 reservation')
  assert.equal(
    response.writtenText(),
    'event: vendor.progress\ndata: opaque-visible\n\n',
    '同一 raw read 的 transformed fragments 必须按原 framing 提交'
  )
}

async function verifyStreamHeartbeatDoesNotSupersedeDeadlineRace(): Promise<void> {
  const pendingRead = controlledUpstreamBody(Buffer.from(': keep-alive\n\n'))
  const deadlineObserved = deferred<void>()
  const deadlineDecision = deferred<'abort'>()
  const response = fakeResponse()
  let streamFailure: string | undefined
  let supersededCount = 0
  const pipe = pipeUpstreamStream(
    pendingRead.body,
    response,
    {
      firstResponseTimeoutMs: 2_000,
      firstByteTimeoutMs: 2_000,
      idleTimeoutMs: 2_000,
      uncommittedAttemptMaxLifetimeMs: 10_000,
      noAvailableAccountWaitMs: 2_000
    },
    Date.now(),
    async (reason) => {
      streamFailure = reason
    },
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'chat_completions',
      firstByteDeadlineMs: 5,
      onFirstByteDeadline: async () => {
        deadlineObserved.resolve()
        return deadlineDecision.promise
      },
      onFirstByteDeadlineSuperseded: () => { supersededCount += 1 }
    }
  )

  await withTimeout(deadlineObserved.promise, 'stream heartbeat deadline was not observed')
  pendingRead.resolveFirstChunk()
  await waitForImmediate()
  deadlineDecision.resolve('abort')
  const result = await withTimeout(pipe, 'stream heartbeat did not settle the deadline decision')

  assert.equal(result.completed, false, 'SSE heartbeat must not turn an expired attempt into a completed stream')
  assert.equal(result.errorCode, 'first_byte_timeout', 'SSE heartbeat must preserve the configured first-output timeout')
  assert.match(streamFailure ?? '', /首个有效输出/, 'SSE heartbeat timeout must reach stream failure accounting')
  assert.equal(supersededCount, 0, 'SSE heartbeat must not release a cutover reservation as semantic output')
  assert.equal(response.writtenText(), '', 'SSE heartbeat must remain private when the attempt loses the deadline race')
}

async function verifyStreamHeartbeatCannotBypassResponsePrecommitDeadline(): Promise<void> {
  const realDateNow = Date.now
  const startedAt = 50_000
  const responsePrecommitDeadlineAtMs = startedAt + 255_000
  let now = startedAt
  let readCount = 0
  let iteratorClosed = false
  const response = fakeResponse()
  const body: AsyncIterable<Uint8Array> = {
    [Symbol.asyncIterator](): AsyncIterator<Uint8Array> {
      return {
        next: () => {
          readCount += 1
          if (readCount <= 5) {
            now += 50_000
            const comment = readCount % 2 === 0 ? ': upstream-comment\n\n' : ': keep-alive\n\n'
            return Promise.resolve({ done: false, value: Buffer.from(comment) })
          }
          now = responsePrecommitDeadlineAtMs + 1
          return new Promise<IteratorResult<Uint8Array>>(() => {})
        },
        return: () => {
          iteratorClosed = true
          return Promise.resolve({ done: true, value: undefined })
        }
      }
    }
  }
  Date.now = () => now
  try {
    const result = await withTimeout(pipeUpstreamStream(
      body,
      response,
      {
        firstResponseTimeoutMs: 600_000,
        firstByteTimeoutMs: 600_000,
        idleTimeoutMs: 600_000,
        uncommittedAttemptMaxLifetimeMs: 3_600_000,
        noAvailableAccountWaitMs: 270_000
      },
      startedAt,
      async () => {},
      undefined,
      {
        responseProtocol: 'openai_v1',
        endpointFamily: 'chat_completions',
        retryBeforeDownstreamWriteUntilOutput: true,
        responsePrecommitDeadlineAtMs
      }
    ), 'stream heartbeat bypassed the response precommit deadline')

    assert.equal(result.completed, false)
    assert.equal(result.errorCode, 'gateway_request_wall_budget_exhausted')
    assert.equal(result.transportFailure, undefined, '请求墙钟属于网关调度归因，不能伪装成账户 transport failure')
    assert.equal(result.semanticCommitted, false)
    assert.equal(result.outputReceived, false)
    assert(readCount > 2, '多次 SSE 心跳/注释活动不得重置绝对 precommit deadline')
    assert.equal(response.writtenText(), '', '请求墙钟到期时不得把 heartbeat 当作正文提交')
    assert.equal(iteratorClosed, true, '请求墙钟到期必须关闭 heartbeat 滴流 iterator')
  } finally {
    Date.now = realDateNow
  }
}

async function verifyStreamHeartbeatDecisionCannotOutliveResponsePrecommitDeadline(): Promise<void> {
  const startedAt = Date.now()
  const responsePrecommitDeadlineAtMs = startedAt + 200
  const deadlineObserved = deferred<void>()
  const deadlineDecision = deferred<'abort'>()
  const firstRead = deferred<IteratorResult<Uint8Array>>()
  let readCount = 0
  let iteratorClosed = false
  let reservationActive = false
  let lateReservationReleaseCount = 0
  let streamFailureCalled = false
  const response = fakeResponse()
  const body: AsyncIterable<Uint8Array> = {
    [Symbol.asyncIterator](): AsyncIterator<Uint8Array> {
      return {
        next: () => readCount++ === 0
          ? firstRead.promise
          : new Promise<IteratorResult<Uint8Array>>(() => {}),
        return: () => {
          iteratorClosed = true
          return Promise.resolve({ done: true, value: undefined })
        }
      }
    }
  }
  const pipe = pipeUpstreamStream(
    body,
    response,
    {
      firstResponseTimeoutMs: 2_000,
      firstByteTimeoutMs: 2_000,
      idleTimeoutMs: 2_000,
      uncommittedAttemptMaxLifetimeMs: 10_000,
      noAvailableAccountWaitMs: 2_000
    },
    startedAt,
    async () => { streamFailureCalled = true },
    undefined,
    {
      responseProtocol: 'openai_v1',
      endpointFamily: 'chat_completions',
      retryBeforeDownstreamWriteUntilOutput: true,
      firstByteDeadlineMs: 10,
      responsePrecommitDeadlineAtMs,
      onFirstByteDeadline: async () => {
        deadlineObserved.resolve()
        await deadlineDecision.promise
        reservationActive = true
        return 'abort' as const
      },
      onFirstByteDeadlineSuperseded: () => {
        if (!reservationActive) return
        reservationActive = false
        lateReservationReleaseCount += 1
      }
    }
  )

  await withTimeout(deadlineObserved.promise, 'stream async first-byte decision was not observed')
  firstRead.resolve({ done: false, value: Buffer.from(': keep-alive\n\n') })
  const result = await withTimeout(pipe, 'async first-byte decision bypassed the response precommit deadline')

  assert.equal(result.errorCode, 'gateway_request_wall_budget_exhausted')
  assert.equal(result.transportFailure, undefined)
  assert.equal(result.semanticCommitted, false)
  assert.equal(streamFailureCalled, false, '墙钟决策不能写成账户 stream failure')
  assert.equal(iteratorClosed, true, '决策迟滞越过墙钟后必须关闭上游 reader')
  assert.equal(response.writtenText(), '', '决策迟滞期间收到的 heartbeat 不得泄露到客户端')

  deadlineDecision.resolve('abort')
  await waitForImmediate()
  await waitForImmediate()
  assert.equal(reservationActive, false, '墙钟到期后迟到创建的切号 reservation 必须再次释放')
  assert.equal(lateReservationReleaseCount, 1, '迟到 reservation 只应被有效释放一次')
}

/**
 * requestUpstream stops owning a deadline callback as soon as headers, a
 * transport error, or a client abort settles the transport. The route still
 * owns any reservation created by that callback, so exercise those detached
 * terminals through the real router rather than a callback-only unit test.
 */
async function verifyGatewayReservationOwnershipForDetachedDeadlineTerminals(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-first-byte-reservation-owner-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  const previousRuntimeConfig = {
    databasePath: runtimeConfig.databasePath,
    datasetDatabasePath: runtimeConfig.datasetDatabasePath,
    usageCatalogDatabasePath: runtimeConfig.usageCatalogDatabasePath,
    statsDatabasePath: runtimeConfig.statsDatabasePath,
    secret: runtimeConfig.secret,
    processRole: runtimeConfig.processRole,
    runtimeStateDriver: runtimeConfig.runtimeStateDriver,
    allowPrivateBaseUrls: runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls,
    consoleEnabled: runtimeConfig.log.consoleEnabled,
    fileEnabled: runtimeConfig.log.fileEnabled,
    loggerLevel: logger.level
  }
  runtimeConfig.databasePath = join(tempRoot, 'gateway.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.secret = 'first-byte-reservation-owner-regression-secret'
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.runtimeStateDriver = 'memory'
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  logger.level = 'silent'
  mkdirSync(tempRoot, { recursive: true })

  const [
    { openAIGatewayRouter, setNormalRouteSpeedFirstDecisionOperationsForTest },
    { requestContextMiddleware },
    databaseModule,
    repositories,
    settingsRepository,
    gatewayCache,
    accountSideEffects,
    usageRecordQueue,
    accountCircuit,
    latencyDegradation,
    readWorkerPool
  ] = await Promise.all([
    import('../../modules/gateway/routes.js'),
    import('../../shared/request-context.js'),
    import('../../storage/database.js'),
    import('../../storage/repositories.js'),
    import('../../storage/settings.repository.js'),
    import('../../modules/gateway/runtime/runtime-cache.service.js'),
    import('../../modules/gateway/runtime/account-side-effects.service.js'),
    import('../../modules/gateway/usage/record-queue.service.js'),
    import('../../modules/gateway/runtime/account-circuit.service.js'),
    import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js'),
    import('../../storage/sqlite-read-worker-pool.js')
  ])

  type TerminalKind = 'node_204' | 'node_preheader_error'
  type ActiveTerminal = {
    kind: TerminalKind
    primaryKey: string
    requestObserved: Deferred<void>
    decisionObserved: Deferred<void>
    allowUpstreamTerminal: Deferred<void>
    upstreamTerminalObserved: Deferred<void>
  }
  let activeTerminal: ActiveTerminal | undefined
  let upstreamServer: http.Server | undefined
  let gatewayServer: http.Server | undefined
  const timerAccelerator = accelerateConfiguredFirstByteTimers()
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    settingsRepository.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      textFirstResponseTimeoutSeconds: 30,
      noAvailableAccountWaitTimeoutSeconds: 30
    })
    gatewayCache.clearGatewayRuntimeCache()
    clearAccountConcurrency()
    clearSpeedFirstCutoverReservationsForTest()

    upstreamServer = http.createServer(async (req, res) => {
      const active = activeTerminal
      const key = upstreamCredential(req)
      if (active && key === active.primaryKey) {
        active.requestObserved.resolve()
        await active.allowUpstreamTerminal.promise
        if (active.kind === 'node_204') {
          res.writeHead(204)
          res.end()
        } else {
          res.destroy(new Error(`first-byte reservation ownership ${active.kind}`))
        }
        active.upstreamTerminalObserved.resolve()
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify(openAIJsonBody(`unexpected fallback ${key}`)))
    })
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`
    const group = repositories.createGroup({
      name: '首字 deadline reservation 所有权回归分组',
      providerCode: GPT_VENDOR_CODE,
      enabled: true
    }, access)
    const primary = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '首字 deadline reservation 主账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-first-byte-owner-primary',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['responses_sse', 'chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      concurrencyLimit: 1,
      priority: 0
    }, access)
    const secondary = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '首字 deadline reservation 目标账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-first-byte-owner-secondary',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['responses_sse', 'chat_json', 'chat_sse']
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: ['gpt-5.5'],
      healthCheckModel: 'gpt-5.5',
      concurrencyLimit: 1,
      priority: 1
    }, access)
    for (const account of [primary, secondary]) {
      assert(repositories.recordAccountHealthCheckSuccess(account.id, {
        intervalHours: 24,
        jitterMinutes: 0,
        failureThreshold: 3,
        statusCode: 200
      }), `fixture account ${account.id} must become dispatchable`)
    }
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '首字 deadline reservation 所有权 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      normalRoutingConfig: {
        schedulingPreference: 'speed_first',
        firstByteDeadlineMs: 10_000,
        speedFirstConfig: {
          slowTriggerCount: 2,
          slowWindowSeconds: 60,
          recoverySuccessCount: 3,
          probeIntervalSeconds: 10,
          degradedTtlSeconds: 60,
          maxFirstByteRetriesPerRequest: 1
        }
      },
      status: 'active'
    }, access)
    assert(apiKey.key, 'reservation ownership fixture must expose its local API key')
    assert(apiKey.routeStrategyId, 'reservation ownership fixture must create its route strategy')

    const app = express()
    app.use(requestContextMiddleware)
    app.use('/v1', express.raw({ type: () => true, limit: '1mb' }), captureGatewayRawBody, openAIGatewayRouter)
    gatewayServer = http.createServer(app)
    await listen(gatewayServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`

    const failures: Error[] = []
    for (const kind of ['node_204', 'node_preheader_error'] as const) {
      accountCircuit.resetGatewayAccountCircuitStoreForTest()
      accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
      await latencyDegradation.clearNormalRouteLatencyDegradationForRouteStrategyAsync(apiKey.routeStrategyId)
      clearAccountConcurrency()
      clearSpeedFirstCutoverReservationsForTest()
      let observedReservation: ObservedReservation | undefined
      let releaseReservationCreation: Deferred<void> | undefined
      const terminal: ActiveTerminal = {
        kind,
        primaryKey: 'sk-first-byte-owner-primary',
        requestObserved: deferred<void>(),
        decisionObserved: deferred<void>(),
        allowUpstreamTerminal: deferred<void>(),
        upstreamTerminalObserved: deferred<void>()
      }
      activeTerminal = terminal
      setNormalRouteSpeedFirstDecisionOperationsForTest({
        isAccountLatencyDegradedAsync: async (account) => account.id === primary.id,
        reserveCutoverTarget: async (input) => {
          terminal.decisionObserved.resolve()
          releaseReservationCreation = deferred<void>()
          await releaseReservationCreation.promise
          const actual = await reserveSpeedFirstCutoverTarget(input)
          if (!actual) return undefined
          observedReservation = observeReservation(actual)
          return observedReservation.reservation
        }
      })
      try {
        const clientRequest = postGatewayChat(
          gatewayBaseUrl,
          apiKey.key,
          `late reservation ${kind}`
        )
        void clientRequest.catch(() => undefined)
        await withTimeout(terminal.requestObserved.promise, `${kind} upstream request was not observed`)
        await withTimeout(terminal.decisionObserved.promise, `${kind} configured deadline did not enter reservation creation`)
        terminal.allowUpstreamTerminal.resolve()
        const response = await withTimeout(clientRequest, `${kind} gateway terminal did not reach the client`)
        if (kind === 'node_204') {
          assert.equal(response.status, 204, '204 upstream terminal must remain a 204 client response')
        }
        await withTimeout(terminal.upstreamTerminalObserved.promise, `${kind} upstream terminal was not emitted`)
        await waitForImmediate()
        releaseReservationCreation?.resolve()
        await waitForReservation(() => observedReservation, `${kind} delayed decision did not acquire its reservation`)
        await waitForImmediate()
        await waitForImmediate()

        assert.equal(observedReservation?.releaseCalls, 1, `${kind} delayed reservation must be released exactly once`)
        assert.deepEqual(speedFirstCutoverBudgetSnapshot(), [], `${kind} delayed reservation must not retain cutover budget`)
        assert.equal(getAccountCurrentConcurrency(secondary.id), 0, `${kind} delayed reservation must not retain target concurrency`)
        assert.equal(timerAccelerator.activeCount(), 0, `${kind} configured deadline timers must be cleared after transport terminal`)
      } catch (error) {
        failures.push(error instanceof Error ? error : new Error(String(error)))
      } finally {
        releaseReservationCreation?.resolve()
        observedReservation?.reservation.release()
        setNormalRouteSpeedFirstDecisionOperationsForTest(undefined)
        activeTerminal = undefined
        clearSpeedFirstCutoverReservationsForTest()
        clearAccountConcurrency()
        await waitForImmediate()
      }
    }
    if (failures.length > 0) {
      throw new AggregateError(failures, 'gateway detached first-byte deadline reservation ownership regression failed')
    }
  } finally {
    activeTerminal?.allowUpstreamTerminal.resolve()
    if (gatewayServer) await closeServer(gatewayServer)
    if (upstreamServer) await closeServer(upstreamServer)
    setNormalRouteSpeedFirstDecisionOperationsForTest(undefined)
    clearSpeedFirstCutoverReservationsForTest()
    clearAccountConcurrency()
    accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
    usageRecordQueue.clearUsageRecordQueueForTest()
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
    databaseModule.closeStorageDatabases()
    timerAccelerator.restore()
    runtimeConfig.databasePath = previousRuntimeConfig.databasePath
    runtimeConfig.datasetDatabasePath = previousRuntimeConfig.datasetDatabasePath
    runtimeConfig.usageCatalogDatabasePath = previousRuntimeConfig.usageCatalogDatabasePath
    runtimeConfig.statsDatabasePath = previousRuntimeConfig.statsDatabasePath
    runtimeConfig.secret = previousRuntimeConfig.secret
    runtimeConfig.processRole = previousRuntimeConfig.processRole
    runtimeConfig.runtimeStateDriver = previousRuntimeConfig.runtimeStateDriver
    runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = previousRuntimeConfig.allowPrivateBaseUrls
    runtimeConfig.log.consoleEnabled = previousRuntimeConfig.consoleEnabled
    runtimeConfig.log.fileEnabled = previousRuntimeConfig.fileEnabled
    logger.level = previousRuntimeConfig.loggerLevel
    await removeTempRootWithRetry(tempRoot)
  }
}

async function verifyFetchNoBodyDecisionDoesNotOutliveTransportTerminal(): Promise<void> {
  const requestObserved = deferred<void>()
  const allowNoContent = deferred<void>()
  const decisionObserved = deferred<void>()
  const decision = deferred<'continue'>()
  const controller = new AbortController()
  const timerTracker = trackTimersWithDelay(5)
  const server = http.createServer(async (_req, res) => {
    requestObserved.resolve()
    await allowNoContent.promise
    res.writeHead(204)
    res.end()
  })
  try {
    await listen(server)
    const request = requestUpstream(`http://127.0.0.1:${serverPort(server)}/fetch-no-content`, {
      method: 'GET',
      headers: new Headers(),
      transport: 'fetch',
      signal: controller.signal,
      firstByteDeadlineMs: 5,
      onFirstByteDeadline: async () => {
        decisionObserved.resolve()
        return decision.promise
      }
    })
    await withTimeout(requestObserved.promise, 'fetch no-body request was not observed')
    await withTimeout(decisionObserved.promise, 'fetch no-body deadline was not observed')
    allowNoContent.resolve()
    const response = await withTimeout(request, 'fetch 204 response was blocked by pending deadline decision')
    assert.equal(response.status, 204)
    assert.equal(response.body, null, 'Fetch 204 must expose a no-body terminal')
    decision.resolve('continue')
    await waitForImmediate()
    assert.equal(timerTracker.activeCount(), 0, 'Fetch 204 terminal must clear deadline timers')
    assert.equal(getAbortListenerCount(controller.signal), 0, 'Fetch 204 terminal must remove its abort listener')
  } finally {
    allowNoContent.resolve()
    decision.resolve('continue')
    timerTracker.restore()
    await closeServer(server)
  }
}

async function verifyNodeAbortDecisionDoesNotRetainTransportResources(): Promise<void> {
  const requestObserved = deferred<void>()
  const decisionObserved = deferred<void>()
  const decision = deferred<'continue'>()
  const controller = new AbortController()
  const timerTracker = trackTimersWithDelay(5)
  const server = http.createServer((_req, _res) => {
    requestObserved.resolve()
  })
  try {
    await listen(server)
    const request = requestUpstream(`http://127.0.0.1:${serverPort(server)}/node-abort`, {
      method: 'GET',
      headers: new Headers(),
      transport: 'node',
      signal: controller.signal,
      firstByteDeadlineMs: 5,
      onFirstByteDeadline: async () => {
        decisionObserved.resolve()
        return decision.promise
      }
    })
    void request.catch(() => undefined)
    await withTimeout(requestObserved.promise, 'node abort request was not observed')
    await withTimeout(decisionObserved.promise, 'node abort deadline was not observed')
    controller.abort()
    await assert.rejects(request, /请求已取消/, 'client abort must settle the Node transport before a delayed decision returns')
    decision.resolve('continue')
    await waitForImmediate()
    assert.equal(timerTracker.activeCount(), 0, 'Node client abort must clear deadline timers')
    assert.equal(getAbortListenerCount(controller.signal), 0, 'Node client abort must remove its abort listener')
  } finally {
    decision.resolve('continue')
    timerTracker.restore()
    await closeServer(server)
  }
}

interface ObservedReservation {
  reservation: SpeedFirstCutoverReservation
  releaseCalls: number
  takeCalls: number
}

function observeReservation(actual: SpeedFirstCutoverReservation): ObservedReservation {
  let releaseCalls = 0
  let takeCalls = 0
  const release = () => {
    releaseCalls += 1
    actual.release()
  }
  const reservation: SpeedFirstCutoverReservation = {
    targetAccountId: actual.targetAccountId,
    get consumed() {
      return actual.consumed
    },
    takeForAccount(account) {
      takeCalls += 1
      const slot = actual.takeForAccount(account)
      return slot ? { ...slot, release } : undefined
    },
    release
  }
  return {
    reservation,
    get releaseCalls() {
      return releaseCalls
    },
    get takeCalls() {
      return takeCalls
    }
  }
}

async function waitForReservation(
  getReservation: () => ObservedReservation | undefined,
  message: string
): Promise<ObservedReservation> {
  const startedAt = Date.now()
  while (!getReservation()) {
    if (Date.now() - startedAt > 2_000) throw new Error(message)
    await waitForImmediate()
  }
  return getReservation()!
}

async function postGatewayChat(
  baseUrl: string,
  apiKey: string,
  content: string,
  signal?: AbortSignal
): Promise<{ status: number; body: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    signal,
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'gpt-5.5',
      messages: [{ role: 'user', content }],
      stream: false,
      max_tokens: 8
    })
  })
  return { status: response.status, body: await response.text() }
}

function openAIJsonBody(content: string): Record<string, unknown> {
  return {
    id: 'chatcmpl_first_byte_reservation_owner',
    object: 'chat.completion',
    model: 'gpt-5.5',
    choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 }
  }
}

function upstreamCredential(req: http.IncomingMessage): string {
  const authorization = String(req.headers.authorization ?? '')
  if (authorization.toLowerCase().startsWith('bearer ')) return authorization.slice(7).trim()
  return String(req.headers['x-api-key'] ?? '')
}

function accelerateConfiguredFirstByteTimers(): {
  activeCount: () => number
  restore: () => void
} {
  const originalSetTimeout = globalThis.setTimeout.bind(globalThis)
  const originalClearTimeout = globalThis.clearTimeout.bind(globalThis)
  const active = new Set<ReturnType<typeof globalThis.setTimeout>>()
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delay?: number, ...args: unknown[]) => {
    const configuredDeadlineTimer = typeof delay === 'number' && delay >= 8_000 && delay <= 10_100
    let timer: ReturnType<typeof globalThis.setTimeout>
    timer = originalSetTimeout((...callbackArgs: unknown[]) => {
      active.delete(timer)
      callback(...callbackArgs)
    }, configuredDeadlineTimer ? 8 : delay, ...args)
    if (configuredDeadlineTimer) active.add(timer)
    return timer
  }) as typeof globalThis.setTimeout
  globalThis.clearTimeout = ((timer: Parameters<typeof globalThis.clearTimeout>[0]) => {
    active.delete(timer as ReturnType<typeof globalThis.setTimeout>)
    originalClearTimeout(timer)
  }) as typeof globalThis.clearTimeout
  return {
    activeCount: () => active.size,
    restore: () => {
      globalThis.setTimeout = originalSetTimeout
      globalThis.clearTimeout = originalClearTimeout
    }
  }
}

function getAbortListenerCount(signal: AbortSignal): number {
  return getEventListeners(signal, 'abort').length
}

function controlledUpstreamBody(firstChunk: Buffer): {
  body: AsyncIterable<Uint8Array>
  resolveFirstChunk: () => void
} {
  const firstRead = deferred<IteratorResult<Uint8Array>>()
  let readCount = 0
  return {
    body: {
      [Symbol.asyncIterator](): AsyncIterator<Uint8Array> {
        return {
          next: () => {
            if (readCount++ === 0) return firstRead.promise
            return Promise.resolve({ done: true, value: undefined })
          },
          return: () => Promise.resolve({ done: true, value: undefined })
        }
      }
    },
    resolveFirstChunk: () => firstRead.resolve({ done: false, value: firstChunk })
  }
}

function fakeResponse(): Response & { writtenText: () => string } {
  const chunks: Buffer[] = []
  const response = new EventEmitter() as EventEmitter & Record<string, unknown>
  response.headersSent = false
  response.writableEnded = false
  response.destroyed = false
  response.writableLength = 0
  response.writableHighWaterMark = 16_384
  response.locals = {}
  response.hasHeader = () => false
  response.setHeader = function setHeader() { return this }
  response.status = function status() { return this }
  response.write = function write(chunk: Uint8Array | string) {
    this.headersSent = true
    chunks.push(Buffer.isBuffer(chunk) ? Buffer.from(chunk) : Buffer.from(chunk))
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
  response.writtenText = () => Buffer.concat(chunks).toString('utf8')
  return response as unknown as Response & { writtenText: () => string }
}

async function readResponseBody(response: GatewayUpstreamResponse): Promise<string> {
  assert(response.body, 'upstream response body is required')
  const chunks: Buffer[] = []
  for await (const chunk of response.body) {
    chunks.push(Buffer.from(chunk))
  }
  return Buffer.concat(chunks).toString('utf8')
}

function deferred<T>(): Deferred<T> {
  let resolvePromise!: Deferred<T>['resolve']
  let rejectPromise!: Deferred<T>['reject']
  const promise = new Promise<T>((resolve, reject) => {
    resolvePromise = resolve
    rejectPromise = reject
  })
  return { promise, resolve: resolvePromise, reject: rejectPromise }
}

function trackGlobalTimeouts(): { activeCount: () => number; restore: () => void } {
  const originalSetTimeout = globalThis.setTimeout.bind(globalThis)
  const originalClearTimeout = globalThis.clearTimeout.bind(globalThis)
  const active = new Set<ReturnType<typeof globalThis.setTimeout>>()
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delay?: number, ...args: unknown[]) => {
    let timer: ReturnType<typeof globalThis.setTimeout>
    timer = originalSetTimeout((...callbackArgs: unknown[]) => {
      active.delete(timer)
      callback(...callbackArgs)
    }, delay, ...args)
    active.add(timer)
    return timer
  }) as typeof globalThis.setTimeout
  globalThis.clearTimeout = ((timer: Parameters<typeof globalThis.clearTimeout>[0]) => {
    active.delete(timer as ReturnType<typeof globalThis.setTimeout>)
    originalClearTimeout(timer)
  }) as typeof globalThis.clearTimeout
  return {
    activeCount: () => active.size,
    restore: () => {
      globalThis.setTimeout = originalSetTimeout
      globalThis.clearTimeout = originalClearTimeout
    }
  }
}

function trackTimersWithDelay(expectedDelayMs: number): { activeCount: () => number; restore: () => void } {
  const originalSetTimeout = globalThis.setTimeout.bind(globalThis)
  const originalClearTimeout = globalThis.clearTimeout.bind(globalThis)
  const active = new Set<ReturnType<typeof globalThis.setTimeout>>()
  globalThis.setTimeout = ((callback: (...args: unknown[]) => void, delay?: number, ...args: unknown[]) => {
    const tracked = delay === expectedDelayMs
    let timer: ReturnType<typeof globalThis.setTimeout>
    timer = originalSetTimeout((...callbackArgs: unknown[]) => {
      active.delete(timer)
      callback(...callbackArgs)
    }, delay, ...args)
    if (tracked) active.add(timer)
    return timer
  }) as typeof globalThis.setTimeout
  globalThis.clearTimeout = ((timer: Parameters<typeof globalThis.clearTimeout>[0]) => {
    active.delete(timer as ReturnType<typeof globalThis.setTimeout>)
    originalClearTimeout(timer)
  }) as typeof globalThis.clearTimeout
  return {
    activeCount: () => active.size,
    restore: () => {
      globalThis.setTimeout = originalSetTimeout
      globalThis.clearTimeout = originalClearTimeout
    }
  }
}

async function withTimeout<T>(promise: Promise<T>, message: string, timeoutMs = 2_000): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs)
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

async function listen(server: http.Server): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', () => {
      server.off('error', reject)
      resolve()
    })
  })
}

async function closeServer(server: http.Server): Promise<void> {
  if (!server.listening) return
  await new Promise<void>((resolve) => {
    server.closeIdleConnections?.()
    server.closeAllConnections?.()
    server.close(() => resolve())
  })
}

async function removeTempRootWithRetry(path: string): Promise<void> {
  for (let attempt = 0; attempt < 8; attempt += 1) {
    try {
      rmSync(path, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message) || attempt === 7) return
      await new Promise<void>((resolvePromise) => setTimeout(resolvePromise, 100 + attempt * 100))
    }
  }
}

function serverPort(server: http.Server): number {
  const address = server.address()
  assert(address && typeof address === 'object', 'server address unavailable')
  return address.port
}
