import type { Response } from 'express'

import { getRequestLogger } from '../../shared/request-context.js'
import { readStreamChunkWithAbort, UpstreamRequestAbortedError } from './openai-gateway-upstream.js'

export interface NonStreamPipeResult {
  firstByteMs?: number
  capturedBody?: Buffer
  capturedBodyText?: string
  diagnosticBodyText?: string
  usageTailText?: string
  captureTruncated: boolean
  transferredBytes: number
}

export interface LimitedBodyReadResult {
  body: Buffer
  bodyText: string
  diagnosticBodyText: string
  truncated: boolean
  readBytes: number
  firstByteMs?: number
}

export interface ResponseWriteResult {
  bytes: number
  backpressure: boolean
  drainWaitMs?: number
}

export const nonStreamResponseCaptureBytes = 2 * 1024 * 1024
export const nonStreamUsageTailCaptureBytes = 256 * 1024
export const upstreamErrorBodyCaptureBytes = 256 * 1024

export async function pipeNonStreamUpstreamResponse(
  upstreamBody: AsyncIterable<Uint8Array>,
  res: Response,
  input: {
    startedAt: number
    captureBytes?: number
    usageTailBytes?: number
    signal?: AbortSignal
  }
): Promise<NonStreamPipeResult> {
  const iterator = upstreamBody[Symbol.asyncIterator]()
  const capture = new LimitedBufferCapture(input.captureBytes ?? nonStreamResponseCaptureBytes)
  const usageTailCapture = new RollingBufferCapture(input.usageTailBytes ?? nonStreamUsageTailCaptureBytes)
  let transferredBytes = 0
  let firstByteMs: number | undefined
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

      const result = await readStreamChunkWithAbort(iterator, input.signal)
      if (result.done) {
        break
      }

      const buffer = Buffer.from(result.value)
      if (firstByteMs === undefined) {
        firstByteMs = Date.now() - input.startedAt
      }
      transferredBytes += buffer.length
      capture.push(buffer)
      usageTailCapture.push(buffer)
      await writeResponseChunk(res, buffer)
    }
  } catch (error) {
    await closeAsyncIterator(iterator)
    endResponse(res)
    throw error
  } finally {
    res.off('close', closeIterator)
  }

  endResponse(res)
  return {
    firstByteMs,
    capturedBody: capture.completeBuffer(),
    capturedBodyText: capture.toText(),
    diagnosticBodyText: capture.toDiagnosticText(),
    usageTailText: usageTailCapture.toText(),
    captureTruncated: capture.isTruncated(),
    transferredBytes
  }
}

export async function readUpstreamBodyLimited(
  upstreamBody: AsyncIterable<Uint8Array> | null,
  input: {
    maxBytes?: number
    startedAt?: number
    signal?: AbortSignal
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

      const buffer = Buffer.from(result.value)
      if (firstByteMs === undefined && input.startedAt !== undefined) {
        firstByteMs = Date.now() - input.startedAt
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
    throw error
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

export async function writeResponseChunk(res: Response, buffer: Buffer): Promise<ResponseWriteResult> {
  if (res.writableEnded || res.destroyed) {
    throw new UpstreamRequestAbortedError('请求已取消', true)
  }
  if (res.write(buffer)) {
    return { bytes: buffer.length, backpressure: false }
  }
  const drainStartedAt = Date.now()
  getRequestLogger().warn({
    event: 'gateway_response_backpressure_started',
    bytes: buffer.length,
    writableLength: res.writableLength,
    writableHighWaterMark: res.writableHighWaterMark,
    headersSent: res.headersSent,
    writableEnded: res.writableEnded,
    destroyed: res.destroyed
  }, '下游响应写入触发 backpressure，开始等待 drain')
  await waitForResponseDrain(res, drainStartedAt)
  const drainWaitMs = Date.now() - drainStartedAt
  getRequestLogger().info({
    event: 'gateway_response_backpressure_drained',
    bytes: buffer.length,
    drainWaitMs,
    writableLength: res.writableLength,
    writableHighWaterMark: res.writableHighWaterMark,
    headersSent: res.headersSent,
    writableEnded: res.writableEnded,
    destroyed: res.destroyed
  }, '下游响应 drain 已恢复')
  return { bytes: buffer.length, backpressure: true, drainWaitMs }
}

export function endResponse(res: Response): void {
  if (!res.writableEnded && !res.destroyed) {
    res.end()
  }
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
