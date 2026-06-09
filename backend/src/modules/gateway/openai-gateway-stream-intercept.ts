import {
  buildGatewayStreamFailureEvent,
  gatewayStreamClientRetryErrorCode,
  gatewayStreamClientRetryMessage
} from './openai-gateway-responses.js'
import {
  isOpenAIStreamFailureEvent,
  parseOpenAISseEventText,
  type ParsedOpenAIStreamEvent
} from './openai-gateway-stream-events.js'
import {
  matchRuntimeStreamInterceptPolicy,
  type RuntimeStreamInterceptPolicy,
  type StreamInterceptRuntimePhase,
  type StreamInterceptPolicyMatchResult
} from './openai-gateway-stream-policy.js'
import type { StreamInterceptPolicyDataHandling } from '../../storage/stream-intercept-policy.repository.js'

export interface StreamInterceptDecision {
  reason: 'before_downstream_write_stream_failure' | 'cyber_policy_stream_failure' | 'configured_stream_policy'
  action: 'client_retry' | 'discard_event' | 'discard_stream' | 'replace_with_failure' | 'dry_run'
  triggerPhase: StreamInterceptRuntimePhase
  runtimePhase?: StreamInterceptRuntimePhase
  upstreamEventType: string
  upstreamErrorCode?: string
  upstreamErrorMessage?: string
  rewriteErrorCode?: string
  rewriteMessage?: string
  downstreamWritten: boolean
  policyId?: string
  policyName?: string
  policySource?: 'system_default' | 'management' | 'account'
  policyScopeType?: 'protocol' | 'provider'
  policyProtocolCode?: string
  policyProviderCode?: string
  executionMode?: 'intercept' | 'dry_run'
  dataHandling?: StreamInterceptPolicyDataHandling
  retryEnabled?: boolean
  accountSwitch?: string
  accountState?: string
  matchedField?: string
  matchedValue?: string
  matchedSnippet?: string
}

export interface StreamInterceptorResult {
  chunks: Buffer[]
  intercepted?: StreamInterceptDecision
  observations?: StreamInterceptDecision[]
  parserSkipped: boolean
}

export interface OpenAIStreamInterceptBufferOptions {
  clientRetryEnabled?: boolean
  policies?: RuntimeStreamInterceptPolicy[]
}

const maxBufferedSseEventBytes = 256 * 1024

export class OpenAIStreamInterceptBuffer {
  private readonly pendingBuffer = new PendingSseEventBuffer()
  private readonly clientRetryEnabled: boolean
  private readonly policies: RuntimeStreamInterceptPolicy[]
  private readonly deferredLeadingNoopChunks: Buffer[] = []
  private parserSkipped = false
  private downstreamWritten = false

  constructor(options: OpenAIStreamInterceptBufferOptions = {}) {
    this.clientRetryEnabled = options.clientRetryEnabled === true
    this.policies = options.policies ?? []
  }

  markDownstreamWrite(): void {
    if (!this.clientRetryEnabled && this.policies.length === 0) {
      return
    }
    this.downstreamWritten = true
  }

  pushChunk(chunk: Buffer): StreamInterceptorResult {
    if (!this.clientRetryEnabled && this.policies.length === 0) {
      return {
        chunks: [chunk],
        parserSkipped: false
      }
    }
    if (this.parserSkipped) {
      return {
        chunks: [...this.drainDeferredLeadingNoopChunks(), chunk],
        parserSkipped: this.parserSkipped
      }
    }

    this.pendingBuffer.push(chunk)
    if (this.pendingBuffer.length > maxBufferedSseEventBytes) {
      const buffered = this.pendingBuffer.drain()
      this.parserSkipped = true
      return {
        chunks: [...this.drainDeferredLeadingNoopChunks(), buffered],
        parserSkipped: true
      }
    }

    const chunks: Buffer[] = []
    const observations: StreamInterceptDecision[] = []

    while (true) {
      const rawBuffer = this.pendingBuffer.shiftEvent()
      if (!rawBuffer) break
      const rawText = rawBuffer.toString('utf8')
      const event = parseOpenAISseEventText(rawText)
      if (!this.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event)) {
        this.deferredLeadingNoopChunks.push(rawBuffer)
        continue
      }
      const policyDecision = this.buildPolicyDecision(event)
      if (policyDecision) {
        if (policyDecision.observation) {
          observations.push(policyDecision.observation)
        }
        if (policyDecision.action === 'discard_event') {
          continue
        }
        if ('decision' in policyDecision) {
          this.clearDeferredLeadingNoopChunks()
          if (policyDecision.failureEvent) {
            if (!this.downstreamWritten) {
              chunks.length = 0
            }
            chunks.push(policyDecision.failureEvent)
          }
          return {
            chunks,
            intercepted: policyDecision.decision,
            observations: observations.length ? observations : undefined,
            parserSkipped: this.parserSkipped
          }
        }
      }
      const decision = this.clientRetryEnabled ? buildStreamFailureDecision(event, this.downstreamWritten) : undefined
      if (decision) {
        this.clearDeferredLeadingNoopChunks()
        if (!this.downstreamWritten) {
          chunks.length = 0
        }
        chunks.push(buildGatewayStreamFailureEvent(decision.rewriteMessage ?? gatewayStreamClientRetryMessage, decision.rewriteErrorCode))
        return {
          chunks,
          intercepted: decision,
          observations: observations.length ? observations : undefined,
          parserSkipped: this.parserSkipped
        }
      }
      chunks.push(...this.drainDeferredLeadingNoopChunks())
      chunks.push(rawBuffer)
    }

    return {
      chunks,
      observations: observations.length ? observations : undefined,
      parserSkipped: this.parserSkipped
    }
  }

  flushPendingOnEof(): StreamInterceptorResult {
    if (!this.clientRetryEnabled && this.policies.length === 0) {
      return {
        chunks: [],
        parserSkipped: false
      }
    }
    if (this.parserSkipped || this.pendingBuffer.length === 0) {
      return {
        chunks: this.parserSkipped ? this.drainDeferredLeadingNoopChunks() : [],
        parserSkipped: this.parserSkipped
      }
    }

    const rawBuffer = this.pendingBuffer.drainEnsuringBoundary()
    const event = parseOpenAISseEventText(rawBuffer.toString('utf8'))
    if (!this.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event)) {
      this.deferredLeadingNoopChunks.push(rawBuffer)
      this.clearDeferredLeadingNoopChunks()
      return {
        chunks: [],
        parserSkipped: this.parserSkipped
      }
    }
    const policyDecision = this.buildPolicyDecision(event)
    if (policyDecision) {
      if (policyDecision.action === 'discard_event') {
        return {
          chunks: [],
          observations: policyDecision.observation ? [policyDecision.observation] : undefined,
          parserSkipped: this.parserSkipped
        }
      }
      if ('decision' in policyDecision) {
        this.clearDeferredLeadingNoopChunks()
        return {
          chunks: policyDecision.failureEvent ? [policyDecision.failureEvent] : [],
          intercepted: policyDecision.decision,
          observations: policyDecision.observation ? [policyDecision.observation] : undefined,
          parserSkipped: this.parserSkipped
        }
      }
      if (policyDecision.observation) {
        return {
          chunks: [rawBuffer],
          observations: [policyDecision.observation],
          parserSkipped: this.parserSkipped
        }
      }
    }
    const decision = this.clientRetryEnabled ? buildStreamFailureDecision(event, this.downstreamWritten) : undefined
    if (decision) {
      this.clearDeferredLeadingNoopChunks()
      return {
        chunks: [buildGatewayStreamFailureEvent(decision.rewriteMessage ?? gatewayStreamClientRetryMessage, decision.rewriteErrorCode)],
        intercepted: decision,
        parserSkipped: this.parserSkipped
      }
    }
    return {
      chunks: [...this.drainDeferredLeadingNoopChunks(), rawBuffer],
      parserSkipped: this.parserSkipped
    }
  }

  private drainDeferredLeadingNoopChunks(): Buffer[] {
    if (this.deferredLeadingNoopChunks.length === 0) {
      return []
    }
    const chunks = [...this.deferredLeadingNoopChunks]
    this.deferredLeadingNoopChunks.length = 0
    return chunks
  }

  private clearDeferredLeadingNoopChunks(): void {
    this.deferredLeadingNoopChunks.length = 0
  }

  private buildPolicyDecision(event: ParsedOpenAIStreamEvent): {
    action: 'dry_run'
    observation: StreamInterceptDecision
  } | {
    action: 'discard_event'
    observation: StreamInterceptDecision
  } | {
    action: 'discard_stream' | 'replace_with_failure'
    decision: StreamInterceptDecision
    observation?: StreamInterceptDecision
    failureEvent?: Buffer
  } | undefined {
    if (this.policies.length === 0) {
      return undefined
    }
    const phase = this.currentPhase()
    const match = matchRuntimeStreamInterceptPolicy(event, this.policies, phase)
    if (!match) {
      return undefined
    }
    const policy = match.policy
    if (policy.executionMode === 'dry_run') {
      return {
        action: 'dry_run',
        observation: buildConfiguredPolicyDecision(event, match, this.downstreamWritten, 'dry_run')
      }
    }
    if (policy.dataHandling === 'discard_event') {
      return {
        action: 'discard_event',
        observation: buildConfiguredPolicyDecision(event, match, this.downstreamWritten, 'discard_event')
      }
    }
    const errorCode = policy.retryEnabled && this.clientRetryEnabled
      ? gatewayStreamClientRetryErrorCode
      : 'stream_intercepted'
    const message = event.errorMessage || `流式响应命中拦截策略：${policy.name}`
    const decision = buildConfiguredPolicyDecision(event, match, this.downstreamWritten, policy.dataHandling === 'replace_with_failure' ? 'replace_with_failure' : 'discard_stream', errorCode, message)
    return {
      action: policy.dataHandling,
      decision,
      failureEvent: policy.dataHandling === 'replace_with_failure'
        ? buildGatewayStreamFailureEvent(message, errorCode)
        : undefined
    }
  }

  private currentPhase(): StreamInterceptRuntimePhase {
    return this.downstreamWritten ? 'after_downstream_write' : 'before_downstream_write'
  }
}

function buildStreamFailureDecision(event: ParsedOpenAIStreamEvent, downstreamWritten: boolean): StreamInterceptDecision | undefined {
  if (!isOpenAIStreamFailureEvent(event)) {
    return undefined
  }
  const retryableAfterOutput = isCodexRetryableAfterOutputStreamFailureCode(event.errorCode)
  if (downstreamWritten && !retryableAfterOutput) {
    return undefined
  }
  return {
    reason: downstreamWritten ? 'cyber_policy_stream_failure' : 'before_downstream_write_stream_failure',
    action: 'client_retry',
    triggerPhase: downstreamWritten ? 'after_downstream_write' : 'before_downstream_write',
    upstreamEventType: event.eventType || event.eventName || 'message',
    upstreamErrorCode: event.errorCode,
    upstreamErrorMessage: event.errorMessage,
    rewriteErrorCode: gatewayStreamClientRetryErrorCode,
    rewriteMessage: event.errorMessage || gatewayStreamClientRetryMessage,
    downstreamWritten
  }
}

export function isCodexRetryableAfterOutputStreamFailureCode(errorCode: string | undefined): boolean {
  // 维护者注意：cyber_policy 是生产里确认过的 GPT / Codex 200 + SSE 流内异常，
  // 输出后也需要改写为客户端可重试错误，否则客户端会表现为半截断开、持续重连。
  // 这不是可随意扩散的通用错误码白名单；改动或删除前必须先告知用户并同步回归用例。
  return errorCode === 'cyber_policy'
}

function buildConfiguredPolicyDecision(
  event: ParsedOpenAIStreamEvent,
  match: StreamInterceptPolicyMatchResult,
  downstreamWritten: boolean,
  action: StreamInterceptDecision['action'],
  rewriteErrorCode?: string,
  rewriteMessage?: string
): StreamInterceptDecision {
  const policy = match.policy
  return {
    reason: configuredPolicyReason(policy, event, downstreamWritten),
    action,
    triggerPhase: downstreamWritten ? 'after_downstream_write' : 'before_downstream_write',
    runtimePhase: match.phase,
    upstreamEventType: event.eventType || event.eventName || 'message',
    upstreamErrorCode: event.errorCode,
    upstreamErrorMessage: event.errorMessage,
    rewriteErrorCode,
    rewriteMessage,
    downstreamWritten,
    policyId: policy.id,
    policyName: policy.name,
    policySource: policy.source,
    policyScopeType: policy.scopeType,
    policyProtocolCode: policy.protocolCode,
    policyProviderCode: policy.providerCode,
    executionMode: policy.executionMode,
    dataHandling: policy.dataHandling,
    retryEnabled: policy.retryEnabled,
    accountSwitch: policy.accountSwitch,
    accountState: policy.accountState,
    matchedField: match.matchedField,
    matchedValue: match.matchedValue,
    matchedSnippet: match.snippet
  }
}

function configuredPolicyReason(policy: RuntimeStreamInterceptPolicy, event: ParsedOpenAIStreamEvent, downstreamWritten: boolean): StreamInterceptDecision['reason'] {
  if (policy.source !== 'system_default') {
    return 'configured_stream_policy'
  }
  return downstreamWritten && isCodexRetryableAfterOutputStreamFailureCode(event.errorCode)
    ? 'cyber_policy_stream_failure'
    : 'before_downstream_write_stream_failure'
}

function isDeferrableLeadingChatCompletionNoopEvent(event: ParsedOpenAIStreamEvent): boolean {
  const data = event.data
  if (!data || data.object !== 'chat.completion.chunk') {
    return false
  }
  if (data.error !== undefined || data.usage !== undefined) {
    return false
  }
  const choices = Array.isArray(data.choices) ? data.choices : []
  if (choices.length === 0) {
    return false
  }
  return choices.every(isNoopChatCompletionChoice)
}

function isNoopChatCompletionChoice(value: unknown): boolean {
  const choice = objectValue(value)
  if (!choice) return false
  if (choice.finish_reason !== undefined && choice.finish_reason !== null) return false
  if (hasNonEmptyString(choice.text)) return false
  if (choice.message !== undefined) return false
  const delta = objectValue(choice.delta)
  if (!delta) return false
  for (const key of Object.keys(delta)) {
    const value = delta[key]
    if (key === 'role' && typeof value === 'string') continue
    if (key === 'content' && (value === '' || value === null || value === undefined)) continue
    return false
  }
  return true
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function hasNonEmptyString(value: unknown): boolean {
  return typeof value === 'string' && value.length > 0
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
  const first = earliestBoundaryCandidate(
    boundaryCandidate(buffer, sseCrLfBoundary),
    boundaryCandidate(buffer, sseLfBoundary),
    boundaryCandidate(buffer, sseCrBoundary)
  )
  if (!first) return undefined
  return { index: first.index, endIndex: first.index + first.length }
}

function earliestBoundaryCandidate(
  ...candidates: Array<{ index: number; length: number } | undefined>
): { index: number; length: number } | undefined {
  let first: { index: number; length: number } | undefined
  for (const candidate of candidates) {
    if (!candidate) continue
    if (!first || candidate.index < first.index || (candidate.index === first.index && candidate.length < first.length)) {
      first = candidate
    }
  }
  return first
}

function boundaryCandidate(buffer: Buffer, tokenBuffer: Buffer): { index: number; length: number } | undefined {
  const index = buffer.indexOf(tokenBuffer)
  return index >= 0 ? { index, length: tokenBuffer.length } : undefined
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

const sseCrLfBoundary = Buffer.from('\r\n\r\n', 'utf8')
const sseLfBoundary = Buffer.from('\n\n', 'utf8')
const sseCrBoundary = Buffer.from('\r\r', 'utf8')
