import {
  buildGatewayStreamFailureEvent,
  gatewayStreamClientRetryErrorCode,
  gatewayStreamClientRetryMessage
} from './openai-gateway-responses.js'
import { openAIStreamEventHasVisibleOutput } from './openai-gateway-usage.js'

export interface StreamInterceptDecision {
  reason: 'before_output_stream_failure'
  action: 'client_retry'
  triggerPhase: 'before_output'
  upstreamEventType: string
  upstreamErrorCode?: string
  upstreamErrorMessage?: string
  rewriteErrorCode: string
  rewriteMessage: string
  outputSeen: boolean
}

export interface StreamInterceptorResult {
  chunks: Buffer[]
  intercepted?: StreamInterceptDecision
  parserSkipped: boolean
}

interface ParsedSseEvent {
  rawText: string
  eventName: string
  dataText: string
  data?: Record<string, unknown>
  dataParseError: boolean
  eventType: string
  errorCode?: string
  errorMessage?: string
}

const maxBufferedSseEventBytes = 256 * 1024

export class OpenAIStreamInterceptBuffer {
  private readonly pendingBuffer = new PendingSseEventBuffer()
  private parserSkipped = false
  private outputSeen = false

  pushChunk(chunk: Buffer): StreamInterceptorResult {
    if (this.parserSkipped) {
      return {
        chunks: [chunk],
        parserSkipped: this.parserSkipped
      }
    }

    this.pendingBuffer.push(chunk)
    if (this.pendingBuffer.length > maxBufferedSseEventBytes) {
      const buffered = this.pendingBuffer.drain()
      this.parserSkipped = true
      return {
        chunks: [buffered],
        parserSkipped: true
      }
    }

    const chunks: Buffer[] = []

    while (true) {
      const rawBuffer = this.pendingBuffer.shiftEvent()
      if (!rawBuffer) break
      const rawText = rawBuffer.toString('utf8')
      const event = parseSseEvent(rawText)
      const decision = buildBeforeOutputFailureDecision(event, this.outputSeen)
      if (decision) {
        chunks.push(buildGatewayStreamFailureEvent(decision.rewriteMessage, decision.rewriteErrorCode))
        return {
          chunks,
          intercepted: decision,
          parserSkipped: this.parserSkipped
        }
      }
      chunks.push(rawBuffer)
      if (isVisibleOutputEvent(event)) {
        this.outputSeen = true
      }
    }

    return {
      chunks,
      parserSkipped: this.parserSkipped
    }
  }

  flushPendingOnEof(): StreamInterceptorResult {
    if (this.parserSkipped || this.pendingBuffer.length === 0) {
      return {
        chunks: [],
        parserSkipped: this.parserSkipped
      }
    }

    const rawBuffer = this.pendingBuffer.drainEnsuringBoundary()
    const event = parseSseEvent(rawBuffer.toString('utf8'))
    const decision = buildBeforeOutputFailureDecision(event, this.outputSeen)
    if (decision) {
      return {
        chunks: [buildGatewayStreamFailureEvent(decision.rewriteMessage, decision.rewriteErrorCode)],
        intercepted: decision,
        parserSkipped: this.parserSkipped
      }
    }
    if (isVisibleOutputEvent(event)) {
      this.outputSeen = true
    }
    return {
      chunks: [rawBuffer],
      parserSkipped: this.parserSkipped
    }
  }
}

function buildBeforeOutputFailureDecision(event: ParsedSseEvent, outputSeen: boolean): StreamInterceptDecision | undefined {
  if (outputSeen || !isStreamFailureEvent(event)) {
    return undefined
  }
  return {
    reason: 'before_output_stream_failure',
    action: 'client_retry',
    triggerPhase: 'before_output',
    upstreamEventType: event.eventType || event.eventName || 'message',
    upstreamErrorCode: event.errorCode,
    upstreamErrorMessage: event.errorMessage,
    rewriteErrorCode: gatewayStreamClientRetryErrorCode,
    rewriteMessage: event.errorMessage || gatewayStreamClientRetryMessage,
    outputSeen
  }
}

function isStreamFailureEvent(event: ParsedSseEvent): boolean {
  if (event.eventType === 'response.failed' || event.eventName === 'response.failed') return true
  if (event.eventType === 'error' || event.eventName === 'error') return true
  return Boolean(hasExplicitErrorObject(event))
}

function hasExplicitErrorObject(event: ParsedSseEvent): boolean {
  const data = event.data
  if (!data) return false
  const response = objectValue(data.response)
  return Boolean(objectValue(response?.error) || objectValue(data.error))
}

class PendingSseEventBuffer {
  private chunks: Buffer[] = []
  private headIndex = 0
  private size = 0
  private nextBoundaryEndIndex: number | undefined

  get length(): number {
    return this.size
  }

  push(chunk: Buffer): void {
    if (chunk.length === 0) {
      return
    }
    const previousSize = this.size
    const previousTail = this.tail(3)
    this.chunks.push(chunk)
    this.size += chunk.length
    if (this.nextBoundaryEndIndex === undefined) {
      this.nextBoundaryEndIndex = findBoundaryEndAfterAppend(previousSize, previousTail, chunk)
    }
  }

  shiftEvent(): Buffer | undefined {
    if (this.nextBoundaryEndIndex === undefined) {
      return undefined
    }
    const event = this.consumePrefix(this.nextBoundaryEndIndex)
    this.nextBoundaryEndIndex = this.findBoundaryEndFromStart()
    return event
  }

  drain(): Buffer {
    const buffered = this.consumePrefix(this.size)
    this.nextBoundaryEndIndex = undefined
    return buffered
  }

  drainEnsuringBoundary(): Buffer {
    const hasBoundary = this.endsWithBoundary()
    const buffered = this.drain()
    return hasBoundary
      ? buffered
      : Buffer.concat([buffered, sseEventBoundarySuffix], buffered.length + sseEventBoundarySuffix.length)
  }

  private consumePrefix(length: number): Buffer {
    if (length <= 0 || this.size === 0) {
      return Buffer.alloc(0)
    }

    const boundedLength = Math.min(length, this.size)
    const first = this.chunks[this.headIndex]
    if (first && boundedLength < first.length) {
      const output = first.subarray(0, boundedLength)
      this.chunks[this.headIndex] = first.subarray(boundedLength)
      this.size -= boundedLength
      return output
    }
    if (first && boundedLength === first.length) {
      this.headIndex += 1
      this.size -= boundedLength
      this.compactConsumedChunks()
      return first
    }

    const parts: Buffer[] = []
    let remaining = boundedLength
    while (remaining > 0) {
      const current = this.chunks[this.headIndex]
      if (!current) {
        break
      }
      if (current.length <= remaining) {
        parts.push(current)
        remaining -= current.length
        this.headIndex += 1
      } else {
        parts.push(current.subarray(0, remaining))
        this.chunks[this.headIndex] = current.subarray(remaining)
        remaining = 0
      }
    }

    this.size -= boundedLength - remaining
    this.compactConsumedChunks()
    return parts.length === 1 ? parts[0] : Buffer.concat(parts, boundedLength - remaining)
  }

  private findBoundaryEndFromStart(): number | undefined {
    let offset = 0
    let tail: Buffer = Buffer.alloc(0)
    for (let index = this.headIndex; index < this.chunks.length; index += 1) {
      const chunk = this.chunks[index]
      const boundary = findBoundaryEndInChunk(offset, tail, chunk)
      if (boundary !== undefined) {
        return boundary
      }
      offset += chunk.length
      tail = trailingBytes(tail, chunk, 3)
    }
    return undefined
  }

  private tail(length: number): Buffer {
    if (length <= 0 || this.size === 0) {
      return Buffer.alloc(0)
    }
    const parts: Buffer[] = []
    const targetLength = Math.min(length, this.size)
    let remaining = targetLength
    for (let index = this.chunks.length - 1; index >= this.headIndex && remaining > 0; index -= 1) {
      const chunk = this.chunks[index]
      const partLength = Math.min(chunk.length, remaining)
      parts.unshift(chunk.subarray(chunk.length - partLength))
      remaining -= partLength
    }
    return parts.length === 1 ? parts[0] : Buffer.concat(parts, targetLength - remaining)
  }

  private endsWithBoundary(): boolean {
    const suffix = this.tail(4)
    return bufferEndsWith(suffix, crlfcrlfBoundary)
      || bufferEndsWith(suffix, lflfBoundary)
      || bufferEndsWith(suffix, crcrBoundary)
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

const crlfcrlfBoundary = Buffer.from('\r\n\r\n', 'utf8')
const lflfBoundary = Buffer.from('\n\n', 'utf8')
const crcrBoundary = Buffer.from('\r\r', 'utf8')
const sseEventBoundarySuffix = lflfBoundary

function findBoundaryEndAfterAppend(previousSize: number, previousTail: Buffer, chunk: Buffer): number | undefined {
  return findBoundaryEndInChunk(previousSize, previousTail, chunk)
}

function findBoundaryEndInChunk(chunkOffset: number, previousTail: Buffer, chunk: Buffer): number | undefined {
  const crossBoundary = findCrossChunkBoundaryEnd(chunkOffset, previousTail, chunk)
  const inChunkBoundary = findSseEventBoundary(chunk)
  const inChunkBoundaryEnd = inChunkBoundary ? chunkOffset + inChunkBoundary.endIndex : undefined
  if (crossBoundary === undefined) return inChunkBoundaryEnd
  if (inChunkBoundaryEnd === undefined) return crossBoundary
  return Math.min(crossBoundary, inChunkBoundaryEnd)
}

function findCrossChunkBoundaryEnd(chunkOffset: number, previousTail: Buffer, chunk: Buffer): number | undefined {
  if (previousTail.length === 0 || chunk.length === 0) {
    return undefined
  }
  const prefix = chunk.subarray(0, Math.min(3, chunk.length))
  const combined = Buffer.concat([previousTail, prefix], previousTail.length + prefix.length)
  const boundary = findSseEventBoundary(combined)
  if (!boundary || boundary.index >= previousTail.length || boundary.endIndex <= previousTail.length) {
    return undefined
  }
  return chunkOffset - previousTail.length + boundary.endIndex
}

function findSseEventBoundary(buffer: Buffer): { index: number; endIndex: number } | undefined {
  const candidates = [
    boundaryCandidate(buffer, '\r\n\r\n'),
    boundaryCandidate(buffer, '\n\n'),
    boundaryCandidate(buffer, '\r\r')
  ].filter((item): item is { index: number; length: number } => Boolean(item))
  if (!candidates.length) return undefined
  const first = candidates.sort((left, right) => left.index - right.index || right.length - left.length)[0]
  return { index: first.index, endIndex: first.index + first.length }
}

function boundaryCandidate(buffer: Buffer, token: string): { index: number; length: number } | undefined {
  const tokenBuffer = Buffer.from(token, 'utf8')
  const index = buffer.indexOf(tokenBuffer)
  return index >= 0 ? { index, length: token.length } : undefined
}

function trailingBytes(previousTail: Buffer, chunk: Buffer, length: number): Buffer {
  if (chunk.length >= length) {
    return chunk.subarray(chunk.length - length)
  }
  const combinedLength = Math.min(length, previousTail.length + chunk.length)
  return Buffer.concat([previousTail, chunk], previousTail.length + chunk.length).subarray(previousTail.length + chunk.length - combinedLength)
}

function bufferEndsWith(buffer: Buffer, suffix: Buffer): boolean {
  return buffer.length >= suffix.length && buffer.subarray(buffer.length - suffix.length).equals(suffix)
}

function parseSseEvent(rawText: string): ParsedSseEvent {
  let eventName = ''
  const dataLines: string[] = []
  for (const line of rawText.split(/\r?\n|\r/)) {
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart())
    }
  }
  const dataText = dataLines.join('\n').trim()
  if (!dataText || dataText === '[DONE]') {
    return {
      rawText,
      eventName,
      dataText,
      dataParseError: false,
      eventType: dataText === '[DONE]' ? '[DONE]' : eventName
    }
  }
  try {
    const data = JSON.parse(dataText) as Record<string, unknown>
    const eventType = typeof data.type === 'string' ? data.type : eventName
    const error = extractEventError(data)
    return {
      rawText,
      eventName,
      dataText,
      data,
      dataParseError: false,
      eventType,
      errorCode: typeof error?.code === 'string' ? error.code : undefined,
      errorMessage: typeof error?.message === 'string' ? error.message : undefined
    }
  } catch {
    return {
      rawText,
      eventName,
      dataText,
      dataParseError: true,
      eventType: eventName
    }
  }
}

function extractEventError(data: Record<string, unknown>): Record<string, unknown> | undefined {
  const response = objectValue(data.response)
  const responseError = objectValue(response?.error)
  if (responseError) return responseError
  const error = objectValue(data.error)
  if (error) return error
  if (typeof data.type === 'string' && data.type === 'error' && (typeof data.code === 'string' || typeof data.message === 'string')) {
    return data
  }
  return undefined
}

function isVisibleOutputEvent(event: ParsedSseEvent): boolean {
  const data = event.data
  if (!data) return false
  return openAIStreamEventHasVisibleOutput(data, event.eventType)
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
