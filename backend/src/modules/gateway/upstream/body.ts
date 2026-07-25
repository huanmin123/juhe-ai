import type { Response } from 'express'

import { getRequestLogger } from '../../../shared/request-context.js'
import {
  isUpstreamRequestAbortedError,
  readStreamChunkWithAbort,
  UpstreamRequestAbortedError
} from './request.js'
import { GatewayFirstByteTimeoutError } from './first-byte-timeout.js'
import {
  decideFirstByteDeadlineAfterPendingRead,
  GatewayResponsePrecommitDeadlineError,
  observeFirstBytePendingRead,
  type FirstByteDeadlineDecisionResult,
  type FirstByteDeadlineHandler
} from './first-byte-deadline.js'

export interface NonStreamPipeResult {
  firstByteMs?: number
  capturedBody?: Buffer
  capturedBodyText?: string
  diagnosticBodyText?: string
  usageTailText?: string
  captureTruncated: boolean
  transferredBytes: number
}

export interface InspectableNonStreamPipeResult extends NonStreamPipeResult {
  fullyBuffered: boolean
  completeBody?: Buffer
  completeBodyText?: string
}

export interface LimitedBodyReadResult {
  body: Buffer
  bodyText: string
  diagnosticBodyText: string
  truncated: boolean
  readBytes: number
  firstByteMs?: number
}

export interface ReplayableLimitedBodyReadResult extends LimitedBodyReadResult {
  replayBody: AsyncIterable<Uint8Array> | null
  close: () => Promise<void>
}

export class UpstreamBodyReadIncompleteError extends Error {
  readonly code = 'UPSTREAM_BODY_READ_INCOMPLETE'

  constructor(cause: unknown) {
    const timeout = cause instanceof Error && /timeout|timedout|timed out|etimedout|超时/i.test(cause.message)
    super(timeout ? '上游响应正文读取超时' : '上游响应正文读取未完成')
    this.name = 'UpstreamBodyReadIncompleteError'
    ;(this as Error & { cause?: unknown }).cause = cause
  }
}

export class UpstreamBodyReadMaxLifetimeError extends Error {
  readonly code = 'UPSTREAM_BODY_READ_MAX_LIFETIME'

  constructor(readonly timeoutMs: number) {
    super(`上游非流式响应正文读取超时（绝对上限 ${Math.ceil(timeoutMs / 1000)}s）`)
    this.name = 'UpstreamBodyReadMaxLifetimeError'
  }
}

export interface ResponseWriteResult {
  bytes: number
  backpressure: boolean
  drainWaitMs?: number
  logLevel?: 'debug' | 'warn'
}

type NonStreamReadWithDeadlineResult = {
  result: IteratorResult<Uint8Array>
  firstByteDeadlineObserved: boolean
}

export class NonStreamUpstreamBodyPipeError extends Error {
  constructor(
    message: string,
    readonly partialResult: NonStreamPipeResult,
    readonly originalError: unknown
  ) {
    super(message)
    this.name = 'NonStreamUpstreamBodyPipeError'
  }
}

const gatewayForcedDownstreamCloseReasonKey = 'gatewayForcedDownstreamCloseReason'

export const nonStreamResponseCaptureBytes = 2 * 1024 * 1024
export const nonStreamUsageTailCaptureBytes = 256 * 1024
export const upstreamErrorBodyCaptureBytes = 256 * 1024
export const responseBackpressureWarnThresholdMs = 50

export async function pipeNonStreamUpstreamResponse(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  input: {
    startedAt: number
    captureBytes?: number
    usageTailBytes?: number
    captureBody?: boolean
    signal?: AbortSignal
    onFirstByte?: () => void
    firstByteTimeoutMs?: number
    firstByteDeadlineMs?: number
    responsePrecommitDeadlineAtMs?: number
    maxLifetimeMs?: number
    onFirstByteDeadline?: FirstByteDeadlineHandler
    onFirstByteDeadlineSuperseded?: () => void
    prepareDownstream?: () => void
    onChunkWritten?: (bytesWritten: number) => void
    onBodyCompleted?: (transferredBytes: number) => void
  }
): Promise<NonStreamPipeResult> {
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const capture = new LimitedBufferCapture(input.captureBody === false ? -1 : input.captureBytes ?? nonStreamResponseCaptureBytes)
  const usageTailCapture = new RollingBufferCapture(input.usageTailBytes ?? nonStreamUsageTailCaptureBytes)
  const maxLifetimeDeadlineAt = nonStreamBodyMaxLifetimeDeadlineAt(input.startedAt, input.maxLifetimeMs)
  let transferredBytes = 0
  let firstByteMs: number | undefined
  let firstByteDeadlineObserved = false
  let downstreamPrepared = false
  let clientClosed = false
  const closeIterator = () => {
    clientClosed = true
    void closeAsyncIterator(iterator)
  }
  res.once('close', closeIterator)

  try {
    while (true) {
      if (clientClosed || res.destroyed) {
        throw new UpstreamRequestAbortedError('请求已取消', true)
      }

      const readResult: NonStreamReadWithDeadlineResult = firstByteMs === undefined
        ? await readFirstNonStreamChunkWithDeadlines(iterator, input.startedAt, {
          signal: input.signal,
          firstByteTimeoutMs: input.firstByteTimeoutMs,
          firstByteDeadlineMs: input.firstByteDeadlineMs,
          firstByteDeadlineObserved,
          onFirstByteDeadline: input.onFirstByteDeadline,
          onFirstByteDeadlineSuperseded: input.onFirstByteDeadlineSuperseded,
          responsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
          pendingReadSupersedesDeadline: true,
          maxLifetimeDeadlineAt,
          maxLifetimeMs: input.maxLifetimeMs
        })
        : {
            result: await readNonStreamChunkWithAbsoluteDeadline(
              iterator,
              input.signal,
              maxLifetimeDeadlineAt,
              input.maxLifetimeMs,
              input.responsePrecommitDeadlineAtMs
            ),
            firstByteDeadlineObserved
          }
      firstByteDeadlineObserved = readResult.firstByteDeadlineObserved
      const result = readResult.result
      if (result.done) {
        break
      }

      const buffer = bufferFromUint8Array(result.value)
      if (firstByteMs === undefined) {
        firstByteMs = Date.now() - input.startedAt
        if (!downstreamPrepared) {
          downstreamPrepared = true
          input.prepareDownstream?.()
        }
        input.onFirstByte?.()
      }
      transferredBytes += buffer.length
      capture.push(buffer)
      usageTailCapture.push(buffer)
      await writeResponseChunk(res, buffer)
      input.onChunkWritten?.(buffer.length)
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    if (transferredBytes > 0 || res.headersSent) {
      if (isUpstreamRequestAbortedError(error) || input.signal?.aborted) {
        endResponse(res)
      } else {
        const partialResult = buildNonStreamPipeResult(capture, usageTailCapture, firstByteMs, transferredBytes)
        destroyResponseForUpstreamBodyError(res)
        throw new NonStreamUpstreamBodyPipeError(
          error instanceof Error ? error.message : '上游非流式响应正文中断',
          partialResult,
          error
        )
      }
    }
    throw error
  } finally {
    res.off('close', closeIterator)
  }

  if (!downstreamPrepared) {
    downstreamPrepared = true
    input.prepareDownstream?.()
  }
  endResponse(res)
  input.onBodyCompleted?.(transferredBytes)
  return buildNonStreamPipeResult(capture, usageTailCapture, firstByteMs, transferredBytes)
}

export async function pipeNonStreamUpstreamResponseForInspection(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  input: {
    startedAt: number
    inspectBytes: number
    captureBytes?: number
    usageTailBytes?: number
    captureBody?: boolean
    signal?: AbortSignal
    onFirstByte?: () => void
    firstByteTimeoutMs?: number
    firstByteDeadlineMs?: number
    responsePrecommitDeadlineAtMs?: number
    maxLifetimeMs?: number
    onFirstByteDeadline?: FirstByteDeadlineHandler
    onFirstByteDeadlineSuperseded?: () => void
    prepareDownstream?: () => void
    onChunkWritten?: (bytesWritten: number) => void
    beforeDownstreamCommit?: (inspectionBody: Buffer) => Promise<void>
  }
): Promise<InspectableNonStreamPipeResult> {
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const inspectBytes = Math.max(0, input.inspectBytes)
  const capture = new LimitedBufferCapture(input.captureBody === false ? -1 : input.captureBytes ?? nonStreamResponseCaptureBytes)
  const usageTailCapture = new RollingBufferCapture(input.usageTailBytes ?? nonStreamUsageTailCaptureBytes)
  const maxLifetimeDeadlineAt = nonStreamBodyMaxLifetimeDeadlineAt(input.startedAt, input.maxLifetimeMs)
  const inspectionChunks: Buffer[] = []
  let inspectionBytes = 0
  let transferredBytes = 0
  let firstByteMs: number | undefined
  let firstByteDeadlineObserved = false
  let downstreamPrepared = false
  let downstreamWriting = false
  let clientClosed = false
  const closeIterator = () => {
    clientClosed = true
    void closeAsyncIterator(iterator)
  }
  const prepareDownstreamForWrite = () => {
    if (downstreamPrepared) return
    downstreamPrepared = true
    input.prepareDownstream?.()
  }
  const writeBufferedInspectionChunks = async () => {
    if (inspectionChunks.length === 0) return
    prepareDownstreamForWrite()
    for (const chunk of inspectionChunks.splice(0)) {
      await writeResponseChunk(res, chunk)
      input.onChunkWritten?.(chunk.length)
    }
  }
  res.once('close', closeIterator)

  try {
    while (true) {
      if (clientClosed || res.destroyed) {
        throw new UpstreamRequestAbortedError('请求已取消', true)
      }

      const readResult: NonStreamReadWithDeadlineResult = firstByteMs === undefined
        ? await readFirstNonStreamChunkWithDeadlines(iterator, input.startedAt, {
          signal: input.signal,
          firstByteTimeoutMs: input.firstByteTimeoutMs,
          firstByteDeadlineMs: input.firstByteDeadlineMs,
          firstByteDeadlineObserved,
          onFirstByteDeadline: input.onFirstByteDeadline,
          onFirstByteDeadlineSuperseded: input.onFirstByteDeadlineSuperseded,
          responsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
          pendingReadSupersedesDeadline: false,
          maxLifetimeDeadlineAt,
          maxLifetimeMs: input.maxLifetimeMs
        })
        : {
            result: await readNonStreamChunkWithAbsoluteDeadline(
              iterator,
              input.signal,
              maxLifetimeDeadlineAt,
              input.maxLifetimeMs,
              input.responsePrecommitDeadlineAtMs
            ),
            firstByteDeadlineObserved
          }
      firstByteDeadlineObserved = readResult.firstByteDeadlineObserved
      const result = readResult.result
      if (result.done) {
        break
      }

      const buffer = bufferFromUint8Array(result.value)
      if (firstByteMs === undefined) {
        firstByteMs = Date.now() - input.startedAt
        input.onFirstByte?.()
      }
      transferredBytes += buffer.length
      capture.push(buffer)
      usageTailCapture.push(buffer)

      if (!downstreamWriting && inspectionBytes + buffer.length <= inspectBytes) {
        inspectionChunks.push(buffer)
        inspectionBytes += buffer.length
        continue
      }

      if (!downstreamWriting) {
        const inspectionBody = inspectionBytes + buffer.length <= inspectBytes
          ? Buffer.concat([...inspectionChunks, buffer], inspectionBytes + buffer.length)
          : Buffer.concat([
            ...inspectionChunks,
            buffer.subarray(0, Math.max(0, inspectBytes - inspectionBytes))
          ], inspectBytes)
        await input.beforeDownstreamCommit?.(inspectionBody)
        downstreamWriting = true
        await writeBufferedInspectionChunks()
      }
      prepareDownstreamForWrite()
      await writeResponseChunk(res, buffer)
      input.onChunkWritten?.(buffer.length)
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    if (downstreamWriting || res.headersSent) {
      if (isUpstreamRequestAbortedError(error) || input.signal?.aborted) {
        endResponse(res)
      } else {
        const partialResult = buildNonStreamPipeResult(capture, usageTailCapture, firstByteMs, transferredBytes)
        destroyResponseForUpstreamBodyError(res)
        throw new NonStreamUpstreamBodyPipeError(
          error instanceof Error ? error.message : '上游非流式响应正文中断',
          partialResult,
          error
        )
      }
    }
    throw error
  } finally {
    res.off('close', closeIterator)
  }

  if (downstreamWriting) {
    if (!downstreamPrepared) {
      prepareDownstreamForWrite()
    }
    endResponse(res)
    return {
      ...buildNonStreamPipeResult(capture, usageTailCapture, firstByteMs, transferredBytes),
      fullyBuffered: false
    }
  }

  const completeBody = inspectionChunks.length > 0
    ? Buffer.concat(inspectionChunks, inspectionBytes)
    : Buffer.alloc(0)
  const pipeResult = buildNonStreamPipeResult(capture, usageTailCapture, firstByteMs, transferredBytes)
  return {
    ...pipeResult,
    fullyBuffered: true,
    completeBody,
    completeBodyText: completeBody.toString('utf8')
  }
}

async function readFirstNonStreamChunkWithDeadlines(
  iterator: AsyncIterator<Uint8Array>,
  startedAt: number,
  input: {
    signal?: AbortSignal
    firstByteTimeoutMs?: number
    firstByteDeadlineMs?: number
    firstByteDeadlineObserved: boolean
    onFirstByteDeadline?: FirstByteDeadlineHandler
    onFirstByteDeadlineSuperseded?: () => void
    responsePrecommitDeadlineAtMs?: number
    pendingReadSupersedesDeadline: boolean
    maxLifetimeDeadlineAt?: number
    maxLifetimeMs?: number
  }
): Promise<{ result: IteratorResult<Uint8Array>; firstByteDeadlineObserved: boolean }> {
  if (
    input.firstByteTimeoutMs === undefined
    && input.firstByteDeadlineMs === undefined
    && input.responsePrecommitDeadlineAtMs === undefined
    && input.maxLifetimeDeadlineAt === undefined
  ) {
    return {
      result: await readStreamChunkWithAbort(iterator, input.signal),
      firstByteDeadlineObserved: input.firstByteDeadlineObserved
    }
  }

  const pendingRead = observeFirstBytePendingRead(iterator.next())
  const hardDeadlineAt = input.firstByteTimeoutMs === undefined
    ? undefined
    : Date.now() + input.firstByteTimeoutMs
  const softDeadlineAt = input.firstByteDeadlineMs === undefined || input.firstByteDeadlineObserved
    ? undefined
    : startedAt + input.firstByteDeadlineMs
  let firstByteDeadlineObserved = input.firstByteDeadlineObserved

  while (true) {
    const now = Date.now()
    const maxLifetimeRemainingMs = input.maxLifetimeDeadlineAt === undefined
      ? undefined
      : input.maxLifetimeDeadlineAt - now
    const responsePrecommitRemainingMs = input.responsePrecommitDeadlineAtMs === undefined
      ? undefined
      : input.responsePrecommitDeadlineAtMs - now
    if (
      responsePrecommitRemainingMs !== undefined
      && responsePrecommitRemainingMs <= 0
      && (
        maxLifetimeRemainingMs === undefined
        || (input.responsePrecommitDeadlineAtMs ?? Number.POSITIVE_INFINITY) <= (input.maxLifetimeDeadlineAt ?? Number.POSITIVE_INFINITY)
      )
    ) {
      throw new GatewayResponsePrecommitDeadlineError(input.responsePrecommitDeadlineAtMs ?? 0)
    }
    if (maxLifetimeRemainingMs !== undefined && maxLifetimeRemainingMs <= 0) {
      throw new UpstreamBodyReadMaxLifetimeError(input.maxLifetimeMs ?? 0)
    }
    const softRemainingMs = softDeadlineAt === undefined || firstByteDeadlineObserved
      ? undefined
      : softDeadlineAt - now
    if (softRemainingMs !== undefined && softRemainingMs <= 0) {
      firstByteDeadlineObserved = true
      const decision = await decideFirstByteDeadlineAfterPendingRead(pendingRead, input.onFirstByteDeadline, {
        elapsedMs: Date.now() - startedAt,
        timeoutMs: input.firstByteDeadlineMs ?? 0,
        transport: 'non_stream'
      }, {
        responsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
        onResponsePrecommitDeadline: input.onFirstByteDeadlineSuperseded
      })
      if (decision.type === 'response_precommit_deadline') throw decision.error
      if (decision.type === 'read') {
        return firstNonStreamReadAfterDeadlineDecision(decision, firstByteDeadlineObserved, input)
      }
      if (decision.action === 'abort') {
        throw new GatewayFirstByteTimeoutError(`上游非流式响应 ${Math.ceil((input.firstByteDeadlineMs ?? 0) / 1000)}s 后仍未返回首个字节`, input.firstByteDeadlineMs ?? 0, 'configured_deadline')
      }
      continue
    }

    const hardRemainingMs = hardDeadlineAt === undefined ? undefined : hardDeadlineAt - now
    if (hardRemainingMs !== undefined && hardRemainingMs <= 0) {
      throw new GatewayFirstByteTimeoutError(`上游非流式响应 ${Math.ceil((input.firstByteTimeoutMs ?? 0) / 1000)}s 后仍未返回首个字节`, input.firstByteTimeoutMs ?? 0)
    }

    const race = await raceReadWithDeadlines(pendingRead.promise, {
      signal: input.signal,
      softTimeoutMs: softRemainingMs,
      hardTimeoutMs: hardRemainingMs,
      maxLifetimeTimeoutMs: maxLifetimeRemainingMs,
      responsePrecommitTimeoutMs: responsePrecommitRemainingMs
    })
    if (race.type === 'read') {
      if (
        input.responsePrecommitDeadlineAtMs !== undefined
        && (pendingRead.settledAtMs() ?? Date.now()) > input.responsePrecommitDeadlineAtMs
      ) {
        throw new GatewayResponsePrecommitDeadlineError(input.responsePrecommitDeadlineAtMs)
      }
      return { result: race.result, firstByteDeadlineObserved }
    }
    if (race.type === 'abort') {
      throw new UpstreamRequestAbortedError('请求已取消', true)
    }
    if (race.type === 'hard_timeout') {
      throw new GatewayFirstByteTimeoutError(`上游非流式响应 ${Math.ceil((input.firstByteTimeoutMs ?? 0) / 1000)}s 后仍未返回首个字节`, input.firstByteTimeoutMs ?? 0)
    }
    if (race.type === 'max_lifetime_timeout') {
      throw new UpstreamBodyReadMaxLifetimeError(input.maxLifetimeMs ?? 0)
    }
    if (race.type === 'response_precommit_timeout') {
      throw new GatewayResponsePrecommitDeadlineError(input.responsePrecommitDeadlineAtMs ?? 0)
    }

    firstByteDeadlineObserved = true
    const decision = await decideFirstByteDeadlineAfterPendingRead(pendingRead, input.onFirstByteDeadline, {
      elapsedMs: Date.now() - startedAt,
      timeoutMs: input.firstByteDeadlineMs ?? 0,
      transport: 'non_stream'
    }, {
      responsePrecommitDeadlineAtMs: input.responsePrecommitDeadlineAtMs,
      onResponsePrecommitDeadline: input.onFirstByteDeadlineSuperseded
    })
    if (decision.type === 'response_precommit_deadline') throw decision.error
    if (decision.type === 'read') {
      return firstNonStreamReadAfterDeadlineDecision(decision, firstByteDeadlineObserved, input)
    }
    if (decision.action === 'abort') {
      throw new GatewayFirstByteTimeoutError(`上游非流式响应 ${Math.ceil((input.firstByteDeadlineMs ?? 0) / 1000)}s 后仍未返回首个字节`, input.firstByteDeadlineMs ?? 0, 'configured_deadline')
    }
  }
}

function firstNonStreamReadAfterDeadlineDecision(
  decision: Extract<FirstByteDeadlineDecisionResult<IteratorResult<Uint8Array>>, { type: 'read' }>,
  firstByteDeadlineObserved: boolean,
  input: {
    pendingReadSupersedesDeadline: boolean
    onFirstByteDeadlineSuperseded?: () => void
    firstByteDeadlineMs?: number
  }
): NonStreamReadWithDeadlineResult {
  if (input.pendingReadSupersedesDeadline) {
    input.onFirstByteDeadlineSuperseded?.()
    return { result: decision.result, firstByteDeadlineObserved }
  }
  if (decision.decisionError !== undefined) throw decision.decisionError
  if (decision.action === 'abort') {
    throw new GatewayFirstByteTimeoutError(
      `上游非流式响应 ${Math.ceil((input.firstByteDeadlineMs ?? 0) / 1000)}s 后仍未返回完整语义响应`,
      input.firstByteDeadlineMs ?? 0,
      'configured_deadline'
    )
  }
  return { result: decision.result, firstByteDeadlineObserved }
}

async function raceReadWithDeadlines(
  pendingRead: Promise<IteratorResult<Uint8Array>>,
  input: {
    signal?: AbortSignal
    softTimeoutMs?: number
    hardTimeoutMs?: number
    maxLifetimeTimeoutMs?: number
    responsePrecommitTimeoutMs?: number
  }
): Promise<
  | { type: 'read'; result: IteratorResult<Uint8Array> }
  | { type: 'soft_timeout' }
  | { type: 'hard_timeout' }
  | { type: 'max_lifetime_timeout' }
  | { type: 'response_precommit_timeout' }
  | { type: 'abort' }
> {
  let softTimer: NodeJS.Timeout | undefined
  let hardTimer: NodeJS.Timeout | undefined
  let maxLifetimeTimer: NodeJS.Timeout | undefined
  let responsePrecommitTimer: NodeJS.Timeout | undefined
  let abortListener: (() => void) | undefined
  try {
    const races: Array<Promise<
      | { type: 'read'; result: IteratorResult<Uint8Array> }
      | { type: 'soft_timeout' }
      | { type: 'hard_timeout' }
      | { type: 'max_lifetime_timeout' }
      | { type: 'response_precommit_timeout' }
      | { type: 'abort' }
    >> = [pendingRead.then((result) => ({ type: 'read' as const, result }))]
    const softTimeoutMs = input.softTimeoutMs
    if (softTimeoutMs !== undefined) {
      races.push(new Promise((resolve) => {
        softTimer = setTimeout(() => resolve({ type: 'soft_timeout' as const }), Math.max(1, softTimeoutMs))
      }))
    }
    const hardTimeoutMs = input.hardTimeoutMs
    if (hardTimeoutMs !== undefined) {
      races.push(new Promise((resolve) => {
        hardTimer = setTimeout(() => resolve({ type: 'hard_timeout' as const }), Math.max(1, hardTimeoutMs))
      }))
    }
    const responsePrecommitTimeoutMs = input.responsePrecommitTimeoutMs
    if (responsePrecommitTimeoutMs !== undefined) {
      races.push(new Promise((resolve) => {
        responsePrecommitTimer = setTimeout(() => resolve({ type: 'response_precommit_timeout' as const }), Math.max(1, responsePrecommitTimeoutMs))
      }))
    }
    const maxLifetimeTimeoutMs = input.maxLifetimeTimeoutMs
    if (maxLifetimeTimeoutMs !== undefined) {
      races.push(new Promise((resolve) => {
        maxLifetimeTimer = setTimeout(() => resolve({ type: 'max_lifetime_timeout' as const }), Math.max(1, maxLifetimeTimeoutMs))
      }))
    }
    if (input.signal) {
      if (input.signal.aborted) {
        return { type: 'abort' }
      }
      races.push(new Promise((resolve) => {
        abortListener = () => resolve({ type: 'abort' as const })
        input.signal?.addEventListener('abort', abortListener, { once: true })
      }))
    }
    return await Promise.race(races)
  } finally {
    if (softTimer) clearTimeout(softTimer)
    if (hardTimer) clearTimeout(hardTimer)
    if (maxLifetimeTimer) clearTimeout(maxLifetimeTimer)
    if (responsePrecommitTimer) clearTimeout(responsePrecommitTimer)
    if (input.signal && abortListener) {
      input.signal.removeEventListener('abort', abortListener)
    }
  }
}

function nonStreamBodyMaxLifetimeDeadlineAt(startedAt: number, maxLifetimeMs: number | undefined): number | undefined {
  if (maxLifetimeMs === undefined || !Number.isFinite(maxLifetimeMs)) return undefined
  return startedAt + Math.max(1, Math.floor(maxLifetimeMs))
}

async function readNonStreamChunkWithAbsoluteDeadline(
  iterator: AsyncIterator<Uint8Array>,
  signal: AbortSignal | undefined,
  maxLifetimeDeadlineAt: number | undefined,
  maxLifetimeMs: number | undefined,
  responsePrecommitDeadlineAtMs?: number
): Promise<IteratorResult<Uint8Array>> {
  if (maxLifetimeDeadlineAt === undefined && responsePrecommitDeadlineAtMs === undefined) {
    return readStreamChunkWithAbort(iterator, signal)
  }
  const now = Date.now()
  const maxLifetimeRemainingMs = maxLifetimeDeadlineAt === undefined ? undefined : maxLifetimeDeadlineAt - now
  const responsePrecommitRemainingMs = responsePrecommitDeadlineAtMs === undefined
    ? undefined
    : responsePrecommitDeadlineAtMs - now
  if (
    responsePrecommitRemainingMs !== undefined
    && responsePrecommitRemainingMs <= 0
    && (
      maxLifetimeRemainingMs === undefined
      || (responsePrecommitDeadlineAtMs ?? Number.POSITIVE_INFINITY) <= (maxLifetimeDeadlineAt ?? Number.POSITIVE_INFINITY)
    )
  ) {
    throw new GatewayResponsePrecommitDeadlineError(responsePrecommitDeadlineAtMs ?? 0)
  }
  if (maxLifetimeRemainingMs !== undefined && maxLifetimeRemainingMs <= 0) {
    throw new UpstreamBodyReadMaxLifetimeError(maxLifetimeMs ?? 0)
  }
  const pendingRead = observeFirstBytePendingRead(iterator.next())
  const race = await raceReadWithDeadlines(pendingRead.promise, {
    signal,
    maxLifetimeTimeoutMs: maxLifetimeRemainingMs,
    responsePrecommitTimeoutMs: responsePrecommitRemainingMs
  })
  if (race.type === 'read') {
    if (
      responsePrecommitDeadlineAtMs !== undefined
      && (pendingRead.settledAtMs() ?? Date.now()) > responsePrecommitDeadlineAtMs
    ) {
      throw new GatewayResponsePrecommitDeadlineError(responsePrecommitDeadlineAtMs)
    }
    return race.result
  }
  if (race.type === 'abort') throw new UpstreamRequestAbortedError('请求已取消', true)
  if (race.type === 'max_lifetime_timeout') {
    throw new UpstreamBodyReadMaxLifetimeError(maxLifetimeMs ?? 0)
  }
  if (race.type === 'response_precommit_timeout') {
    throw new GatewayResponsePrecommitDeadlineError(responsePrecommitDeadlineAtMs ?? 0)
  }
  throw new UpstreamBodyReadMaxLifetimeError(maxLifetimeMs ?? 0)
}

export async function readUpstreamBodyLimited(
  upstreamBody: AsyncIterable<Uint8Array> | null,
  input: {
    maxBytes?: number
    startedAt?: number
    signal?: AbortSignal
    onFirstByte?: () => void
  } = {}
): Promise<LimitedBodyReadResult> {
  if (!upstreamBody) {
    return emptyLimitedBodyReadResult()
  }

  const maxBytes = Math.max(0, input.maxBytes ?? upstreamErrorBodyCaptureBytes)
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const capture = new LimitedBufferCapture(maxBytes)
  let readBytes = 0
  let firstByteMs: number | undefined
  let truncated = false

  try {
    while (true) {
      const result = await readStreamChunkWithAbort(iterator, input.signal)
      if (result.done) {
        break
      }

      const buffer = bufferFromUint8Array(result.value)
      if (firstByteMs === undefined && input.startedAt !== undefined) {
        firstByteMs = Date.now() - input.startedAt
        input.onFirstByte?.()
      }
      readBytes += buffer.length
      capture.push(buffer)

      if (capture.isTruncated()) {
        truncated = true
        await closeAsyncIterator(iterator)
        break
      }
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    if (isUpstreamRequestAbortedError(error) || input.signal?.aborted) {
      throw error
    }
    throw new UpstreamBodyReadIncompleteError(error)
  }

  const body = capture.buffer()
  const bodyText = body.toString('utf8')
  return {
    body,
    bodyText,
    diagnosticBodyText: truncated ? `${bodyText}\n[truncated]` : bodyText,
    truncated,
    readBytes,
    firstByteMs
  }
}

export async function readUpstreamBodyForPolicyInspection(
  upstreamBody: AsyncIterable<Uint8Array> | null,
  input: {
    maxBytes?: number
    signal?: AbortSignal
  } = {}
): Promise<ReplayableLimitedBodyReadResult> {
  if (!upstreamBody) {
    return {
      ...emptyLimitedBodyReadResult(),
      replayBody: null,
      close: async () => {}
    }
  }

  const maxBytes = Math.max(0, input.maxBytes ?? upstreamErrorBodyCaptureBytes)
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const replayChunks: Buffer[] = []
  const capture = new LimitedBufferCapture(maxBytes)
  let readBytes = 0
  let completed = false
  let closed = false

  const close = async () => {
    if (closed || completed) return
    closed = true
    await closeAsyncIterator(iterator)
  }

  try {
    while (!capture.isTruncated()) {
      const result = await readStreamChunkWithAbort(iterator, input.signal)
      if (result.done) {
        completed = true
        break
      }
      const chunk = bufferFromUint8Array(result.value)
      replayChunks.push(chunk)
      readBytes += chunk.length
      capture.push(chunk)
    }
  } catch (error) {
    await close()
    if (isUpstreamRequestAbortedError(error) || input.signal?.aborted) {
      throw error
    }
    throw new UpstreamBodyReadIncompleteError(error)
  }

  const replayBody = (async function* (): AsyncGenerator<Uint8Array> {
    try {
      for (const chunk of replayChunks) yield chunk
      while (!completed && !closed) {
        const result = await readStreamChunkWithAbort(iterator, input.signal)
        if (result.done) {
          completed = true
          break
        }
        yield bufferFromUint8Array(result.value)
      }
    } finally {
      await close()
    }
  })()
  const body = capture.buffer()
  const bodyText = body.toString('utf8')
  const truncated = capture.isTruncated()
  return {
    body,
    bodyText,
    diagnosticBodyText: truncated ? `${bodyText}\n[truncated]` : bodyText,
    truncated,
    readBytes,
    replayBody,
    close
  }
}

export async function writeResponseChunk(res: Response, buffer: Buffer): Promise<ResponseWriteResult> {
  if (res.writableEnded || res.destroyed) {
    throw new UpstreamRequestAbortedError('请求已取消', true)
  }
  if (res.write(buffer)) {
    return { bytes: buffer.length, backpressure: false }
  }
  const drainStartedAt = Date.now()
  const startedWritableLength = res.writableLength
  const startedWritableHighWaterMark = res.writableHighWaterMark
  const startedHeadersSent = res.headersSent
  const startedWritableEnded = res.writableEnded
  const startedDestroyed = res.destroyed
  await waitForResponseDrain(res, drainStartedAt)
  const drainWaitMs = Date.now() - drainStartedAt
  const logLevel = drainWaitMs >= responseBackpressureWarnThresholdMs ? 'warn' : 'debug'
  getRequestLogger()[logLevel]({
    event: logLevel === 'warn' ? 'gateway_response_backpressure_slow' : 'gateway_response_backpressure_drained',
    bytes: buffer.length,
    drainWaitMs,
    startedWritableLength,
    startedWritableHighWaterMark,
    startedHeadersSent,
    startedWritableEnded,
    startedDestroyed,
    writableLength: res.writableLength,
    writableHighWaterMark: res.writableHighWaterMark,
    headersSent: res.headersSent,
    writableEnded: res.writableEnded,
    destroyed: res.destroyed
  }, logLevel === 'warn' ? '下游响应 backpressure 等待时间过长' : '下游响应短暂 backpressure 已恢复')
  return { bytes: buffer.length, backpressure: true, drainWaitMs, logLevel }
}

export function bufferFromUint8Array(value: Uint8Array): Buffer {
  return Buffer.isBuffer(value)
    ? value
    : Buffer.from(value.buffer, value.byteOffset, value.byteLength)
}

export function endResponse(res: Response): void {
  if (!res.writableEnded && !res.destroyed) {
    res.end()
  }
}

export function destroyResponseForUpstreamBodyError(res: Response): void {
  if (res.writableEnded || res.destroyed) {
    return
  }
  markGatewayForcedDownstreamClose(res, 'upstream_body_interrupted')
  res.destroy()
}

export function isGatewayForcedDownstreamClose(res: Response): boolean {
  return typeof (res.locals as Record<string, unknown>)[gatewayForcedDownstreamCloseReasonKey] === 'string'
}

function markGatewayForcedDownstreamClose(res: Response, reason: string): void {
  const locals = res.locals as Record<string, unknown>
  locals[gatewayForcedDownstreamCloseReasonKey] = reason
}

export async function closeAsyncIterator(iterator: AsyncIterator<Uint8Array>, timeoutMs = 1000): Promise<void> {
  if (!iterator.return) {
    return
  }
  let timer: NodeJS.Timeout | undefined
  try {
    await Promise.race([
      Promise.resolve(iterator.return()),
      new Promise<void>((resolve) => {
        timer = setTimeout(resolve, Math.max(1, timeoutMs))
        timer.unref()
      })
    ])
  } catch {
  } finally {
    if (timer) {
      clearTimeout(timer)
    }
  }
}

export class LimitedBufferCapture {
  private chunks: Buffer[] = []
  private size = 0
  private truncated = false

  constructor(private readonly limitBytes: number) {}

  push(buffer: Buffer): void {
    if (buffer.length === 0 || this.limitBytes < 0) {
      return
    }
    const remaining = this.limitBytes - this.size
    if (remaining <= 0) {
      this.truncated = true
      return
    }
    if (buffer.length > remaining) {
      this.chunks.push(buffer.subarray(0, remaining))
      this.size += remaining
      this.truncated = true
      return
    }
    this.chunks.push(buffer)
    this.size += buffer.length
  }

  isTruncated(): boolean {
    return this.truncated
  }

  buffer(): Buffer {
    return Buffer.concat(this.chunks, this.size)
  }

  completeBuffer(): Buffer | undefined {
    if (this.truncated || this.chunks.length === 0) {
      return undefined
    }
    return this.buffer()
  }

  clear(): void {
    this.chunks = []
    this.size = 0
    this.truncated = false
  }

  toText(): string | undefined {
    if (this.chunks.length === 0) {
      return undefined
    }
    return this.buffer().toString('utf8')
  }

  toDiagnosticText(): string | undefined {
    const text = this.toText()
    if (text === undefined) {
      return undefined
    }
    return this.truncated ? `${text}\n[truncated]` : text
  }
}

class RollingBufferCapture {
  private chunks: Buffer[] = []
  private headIndex = 0
  private size = 0

  constructor(private readonly limitBytes: number) {}

  push(buffer: Buffer): void {
    if (buffer.length === 0 || this.limitBytes <= 0) {
      return
    }
    if (buffer.length >= this.limitBytes) {
      this.chunks = [buffer.subarray(buffer.length - this.limitBytes)]
      this.headIndex = 0
      this.size = this.limitBytes
      return
    }

    this.chunks.push(buffer)
    this.size += buffer.length
    this.trimOverflow()
  }

  toText(): string | undefined {
    if (this.size === 0) {
      return undefined
    }
    return Buffer.concat(this.activeChunks(), this.size).toString('utf8')
  }

  private trimOverflow(): void {
    let overflow = this.size - this.limitBytes
    while (overflow > 0 && this.headIndex < this.chunks.length) {
      const first = this.chunks[this.headIndex]
      if (first.length <= overflow) {
        this.headIndex += 1
        this.size -= first.length
        overflow -= first.length
      } else {
        this.chunks[this.headIndex] = first.subarray(overflow)
        this.size -= overflow
        overflow = 0
      }
    }
    this.compactConsumedChunks()
  }

  private activeChunks(): Buffer[] {
    return this.headIndex === 0 ? this.chunks : this.chunks.slice(this.headIndex)
  }

  private compactConsumedChunks(): void {
    if (this.headIndex === 0) {
      return
    }
    if (this.headIndex >= this.chunks.length) {
      this.chunks = []
      this.headIndex = 0
      return
    }
    if (this.headIndex > 64 && this.headIndex * 2 > this.chunks.length) {
      this.chunks = this.chunks.slice(this.headIndex)
      this.headIndex = 0
    }
  }
}

function buildNonStreamPipeResult(
  capture: LimitedBufferCapture,
  usageTailCapture: RollingBufferCapture,
  firstByteMs: number | undefined,
  transferredBytes: number
): NonStreamPipeResult {
  const capturedBody = capture.completeBuffer()
  const capturedBodyText = capturedBody ? capturedBody.toString('utf8') : capture.toText()
  const captureTruncated = capture.isTruncated()
  return {
    firstByteMs,
    capturedBody,
    capturedBodyText,
    diagnosticBodyText: capturedBodyText === undefined
      ? undefined
      : captureTruncated ? `${capturedBodyText}\n[truncated]` : capturedBodyText,
    usageTailText: usageTailCapture.toText(),
    captureTruncated,
    transferredBytes
  }
}

function waitForResponseDrain(res: Response, startedAt: number): Promise<void> {
  return new Promise((resolve, reject) => {
    const waitingLogTimer = setInterval(() => {
      getRequestLogger().warn({
        event: 'gateway_response_drain_waiting',
        waitMs: Date.now() - startedAt,
        writableLength: res.writableLength,
        writableHighWaterMark: res.writableHighWaterMark,
        headersSent: res.headersSent,
        writableEnded: res.writableEnded,
        destroyed: res.destroyed
      }, '下游响应仍在等待 drain')
    }, 10_000)
    waitingLogTimer.unref()
    const cleanup = () => {
      clearInterval(waitingLogTimer)
      res.off('drain', onDrain)
      res.off('close', onClose)
      res.off('error', onError)
    }
    const onDrain = () => {
      cleanup()
      resolve()
    }
    const onClose = () => {
      cleanup()
      reject(new UpstreamRequestAbortedError('请求已取消', true))
    }
    const onError = (error: Error) => {
      cleanup()
      reject(error)
    }

    res.once('drain', onDrain)
    res.once('close', onClose)
    res.once('error', onError)
  })
}

function emptyLimitedBodyReadResult(): LimitedBodyReadResult {
  return {
    body: Buffer.alloc(0),
    bodyText: '',
    diagnosticBodyText: '',
    truncated: false,
    readBytes: 0
  }
}
