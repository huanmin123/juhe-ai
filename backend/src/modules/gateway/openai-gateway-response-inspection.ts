import {
  buildGatewayStreamFailureEvent,
  gatewayStreamClientRetryErrorCode,
  gatewayStreamClientRetryMessage
} from './openai-gateway-responses.js'
import {
  parseOpenAISseEventText,
  type ParsedOpenAIStreamEvent
} from './openai-gateway-stream-events.js'
import {
  extractOpenAISseSemanticFrames,
  type OpenAIResponseEndpointFamily,
  type ResponseSemanticFrame
} from './openai-gateway-response-semantics.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'
import {
  responseInspectionPolicyActionRuntime,
  type ResponseInspectionPolicyAccountState,
  type ResponseInspectionPolicyAccountSwitch,
  type ResponseInspectionPolicyDataHandling,
  type ResponseInspectionPolicyExecutionMode,
  type ResponseInspectionPolicyMatch,
  type ResponseInspectionPolicyScopeType,
  type ResponseInspectionPolicySource,
  type ResponseInspectionPolicySummary
} from '../../storage/response-inspection-policy.repository.js'
import { normalizeAccountResponseInspectionRules } from '../accounts/account-response-inspection-policy-validation.js'
import { OPENAI_PROTOCOL_CODE } from '../../domain/provider-protocol.js'

export interface RuntimeResponseInspectionPolicy {
  id: string
  source: ResponseInspectionPolicySource
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: string
  providerCode?: string
  match: ResponseInspectionPolicyMatch
  action: ResponseInspectionPolicySummary['action']
  executionMode: ResponseInspectionPolicyExecutionMode
  dataHandling: ResponseInspectionPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: ResponseInspectionPolicyAccountSwitch
  accountState: ResponseInspectionPolicyAccountState
}

export interface ResponseInspectionMatchResult {
  policy: RuntimeResponseInspectionPolicy
  matchedFrame: ResponseSemanticFrame
  matchedField: string
  matchedValue: string
  snippet?: string
}

export interface ResponseInspectionDecision {
  reason: 'configured_response_policy' | 'before_downstream_write_response_failure' | 'cyber_policy_response_failure'
  action: 'client_retry' | 'discard_event' | 'discard_response' | 'replace_with_failure' | 'dry_run'
  transport: 'json' | 'sse'
  triggerPhase: 'before_downstream_write' | 'after_downstream_write'
  endpointFamily: OpenAIResponseEndpointFamily
  frameType: ResponseSemanticFrame['frameType']
  upstreamEventType?: string
  upstreamErrorCode?: string
  upstreamErrorType?: string
  upstreamErrorMessage?: string
  finishReason?: string
  rewriteErrorCode?: string
  rewriteMessage?: string
  downstreamWritten: boolean
  policyId?: string
  policyName?: string
  policySource?: ResponseInspectionPolicySource
  policyScopeType?: ResponseInspectionPolicyScopeType
  policyProtocolCode?: string
  policyProviderCode?: string
  executionMode?: ResponseInspectionPolicyExecutionMode
  dataHandling?: ResponseInspectionPolicyDataHandling
  retryEnabled?: boolean
  accountSwitch?: string
  accountState?: string
  matchedField?: string
  matchedValue?: string
  matchedSnippet?: string
}

export interface ResponseInspectionResult {
  decision?: ResponseInspectionDecision
  observations?: ResponseInspectionDecision[]
}

export interface ResponseInspectionFailurePayload {
  errorCode: string
  message: string
}

export interface ResponseInspectionSseResult {
  chunks: Buffer[]
  intercepted?: ResponseInspectionDecision
  observations?: ResponseInspectionDecision[]
  parserSkipped: boolean
}

export interface OpenAIResponseInspectionBufferOptions {
  clientRetryEnabled?: boolean
  policies?: RuntimeResponseInspectionPolicy[]
  endpointFamily: OpenAIResponseEndpointFamily
}

const maxBufferedSseEventBytes = 256 * 1024
const textMatchSnippetChars = 160

export function resolveRuntimeResponseInspectionPolicies(input: {
  account: UpstreamAccount
  managementPolicies?: ResponseInspectionPolicySummary[]
}): RuntimeResponseInspectionPolicy[] {
  const management = (input.managementPolicies ?? [])
    .filter((policy) => policyMatchesAccountScope(policy, input.account))
    .map((policy) => runtimePolicyFromSummary(policy, policy.defaultRule ? 'system_default' : 'management'))
  const accountRules = accountResponseInspectionRules(input.account)
  return [...accountRules, ...management].sort((a, b) =>
    sourceOrder(a.source) - sourceOrder(b.source)
    || scopeOrder(a) - scopeOrder(b)
    || a.priority - b.priority
    || a.id.localeCompare(b.id)
  )
}

export function inspectResponseSemanticFrames(input: {
  frames: ResponseSemanticFrame[]
  policies: RuntimeResponseInspectionPolicy[]
  downstreamWritten: boolean
  transport: 'json' | 'sse'
}): ResponseInspectionResult {
  const observations: ResponseInspectionDecision[] = []
  for (const frame of input.frames) {
    const match = matchRuntimeResponseInspectionPolicy(frame, input.policies)
    if (!match) continue
    const policy = match.policy
    const action = responseInspectionDecisionAction(policy, input.transport)
    const decision = buildPolicyDecision(match, input.downstreamWritten, action)
    if (policy.executionMode === 'dry_run') {
      observations.push(decision)
      continue
    }
    if (action === 'discard_event') {
      return {
        decision,
        observations: [...observations, decision]
      }
    }
    return {
      decision,
      observations: observations.length > 0 ? observations : undefined
    }
  }
  return observations.length > 0 ? { observations } : {}
}

export function responseInspectionFailurePayloadForDecision(
  decision: ResponseInspectionDecision,
  clientRetryEnabled: boolean
): ResponseInspectionFailurePayload {
  const clientRetryCode = clientRetryEnabled
    && decision.retryEnabled === true
    && decision.triggerPhase === 'before_downstream_write'
    ? gatewayStreamClientRetryErrorCode
    : undefined
  const errorCode = clientRetryCode ?? decision.rewriteErrorCode ?? 'response_inspection_matched'
  const message = clientRetryCode === gatewayStreamClientRetryErrorCode
    ? decision.rewriteMessage ?? gatewayStreamClientRetryMessage
    : decision.rewriteMessage ?? '响应命中检查策略'
  return { errorCode, message }
}

export function isCodexRetryableAfterOutputResponseFailureCode(errorCode: string | undefined): boolean {
  return errorCode === 'cyber_policy'
}

export function matchRuntimeResponseInspectionPolicy(
  frame: ResponseSemanticFrame,
  policies: RuntimeResponseInspectionPolicy[]
): ResponseInspectionMatchResult | undefined {
  for (const policy of policies) {
    if (!policy.enabled) continue
    const match = firstPositiveMatch(frame, policy.match)
    if (!match) continue
    if (outputTextExcluded(frame, policy.match)) continue
    return {
      policy,
      matchedFrame: frame,
      ...match
    }
  }
  return undefined
}

export class OpenAIResponseInspectionBuffer {
  private readonly pendingBuffer = new PendingSseEventBuffer()
  private readonly clientRetryEnabled: boolean
  private readonly policies: RuntimeResponseInspectionPolicy[]
  private readonly endpointFamily: OpenAIResponseEndpointFamily
  private readonly deferredLeadingNoopChunks: Buffer[] = []
  private parserSkipped = false
  private downstreamWritten = false

  constructor(options: OpenAIResponseInspectionBufferOptions) {
    this.clientRetryEnabled = options.clientRetryEnabled === true
    this.policies = options.policies ?? []
    this.endpointFamily = options.endpointFamily
  }

  markDownstreamWrite(): void {
    if (!this.clientRetryEnabled && this.policies.length === 0) return
    this.downstreamWritten = true
  }

  pushChunk(chunk: Buffer): ResponseInspectionSseResult {
    if (!this.clientRetryEnabled && this.policies.length === 0) {
      return { chunks: [chunk], parserSkipped: false }
    }
    if (this.parserSkipped) {
      return { chunks: [...this.drainDeferredLeadingNoopChunks(), chunk], parserSkipped: true }
    }

    this.pendingBuffer.push(chunk)
    if (this.pendingBuffer.length > maxBufferedSseEventBytes) {
      const buffered = this.pendingBuffer.drain()
      this.parserSkipped = true
      return { chunks: [...this.drainDeferredLeadingNoopChunks(), buffered], parserSkipped: true }
    }

    const chunks: Buffer[] = []
    const observations: ResponseInspectionDecision[] = []

    while (true) {
      const rawBuffer = this.pendingBuffer.shiftEvent()
      if (!rawBuffer) break
      const event = parseOpenAISseEventText(rawBuffer.toString('utf8'))
      if (!this.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event)) {
        this.deferredLeadingNoopChunks.push(rawBuffer)
        continue
      }
      const frames = extractOpenAISseSemanticFrames(event, this.endpointFamily)
      const inspection = inspectResponseSemanticFrames({
        frames,
        policies: this.policies,
        downstreamWritten: this.downstreamWritten,
        transport: 'sse'
      })
      if (inspection.observations) observations.push(...inspection.observations)
      if (inspection.decision) {
        const decision = inspection.decision
        if (decision.action === 'discard_event') {
          continue
        }
        this.clearDeferredLeadingNoopChunks()
        if (!this.downstreamWritten) {
          chunks.length = 0
        }
        const failureEvent = failureEventForDecision(decision, this.clientRetryEnabled)
        if (failureEvent) {
          chunks.push(failureEvent)
        }
        return {
          chunks,
          intercepted: decision,
          observations: observations.length > 0 ? observations : undefined,
          parserSkipped: this.parserSkipped
        }
      }
      chunks.push(...this.drainDeferredLeadingNoopChunks())
      chunks.push(rawBuffer)
    }

    return {
      chunks,
      observations: observations.length > 0 ? observations : undefined,
      parserSkipped: this.parserSkipped
    }
  }

  flushPendingOnEof(): ResponseInspectionSseResult {
    if (!this.clientRetryEnabled && this.policies.length === 0) {
      return { chunks: [], parserSkipped: false }
    }
    if (this.parserSkipped || this.pendingBuffer.length === 0) {
      return {
        chunks: this.parserSkipped ? this.drainDeferredLeadingNoopChunks() : [],
        parserSkipped: this.parserSkipped
      }
    }
    const rawBuffer = this.pendingBuffer.drainEnsuringBoundary()
    return this.inspectRawEventBuffer(rawBuffer, true)
  }

  private inspectRawEventBuffer(rawBuffer: Buffer, eofPendingFlush = false): ResponseInspectionSseResult {
    const event = parseOpenAISseEventText(rawBuffer.toString('utf8'))
    if (!this.downstreamWritten && isDeferrableLeadingChatCompletionNoopEvent(event)) {
      this.deferredLeadingNoopChunks.push(rawBuffer)
      this.clearDeferredLeadingNoopChunks()
      return { chunks: [], parserSkipped: this.parserSkipped }
    }
    const frames = extractOpenAISseSemanticFrames(event, this.endpointFamily)
    const inspection = inspectResponseSemanticFrames({
      frames,
      policies: this.policies,
      downstreamWritten: this.downstreamWritten,
      transport: 'sse'
    })
    if (!inspection.decision) {
      return {
        chunks: [...this.drainDeferredLeadingNoopChunks(), rawBuffer],
        observations: inspection.observations,
        parserSkipped: this.parserSkipped
      }
    }
    const decision = inspection.decision
    if (decision.action === 'discard_event') {
      return {
        chunks: [],
        observations: inspection.observations,
        parserSkipped: this.parserSkipped
      }
    }
    this.clearDeferredLeadingNoopChunks()
    const failureEvent = failureEventForDecision(decision, this.clientRetryEnabled)
    return {
      chunks: failureEvent ? [failureEvent] : [],
      intercepted: decision,
      observations: inspection.observations,
      parserSkipped: this.parserSkipped
    }
  }

  private drainDeferredLeadingNoopChunks(): Buffer[] {
    if (this.deferredLeadingNoopChunks.length === 0) return []
    const chunks = [...this.deferredLeadingNoopChunks]
    this.deferredLeadingNoopChunks.length = 0
    return chunks
  }

  private clearDeferredLeadingNoopChunks(): void {
    this.deferredLeadingNoopChunks.length = 0
  }
}

function firstPositiveMatch(frame: ResponseSemanticFrame, match: ResponseInspectionPolicyMatch): Pick<ResponseInspectionMatchResult, 'matchedField' | 'matchedValue' | 'snippet'> | undefined {
  const matched: Array<Pick<ResponseInspectionMatchResult, 'matchedField' | 'matchedValue' | 'snippet'>> = []

  if (match.outputTextIncludes?.length) {
    if (!frame.text || frame.visibleOutput === false) return undefined
    const outputTextMatch = firstSubstringMatch(frame.text, match.outputTextIncludes)
    if (!outputTextMatch) return undefined
    matched.push({ matchedField: 'outputTextIncludes', matchedValue: outputTextMatch, snippet: snippetAround(frame.text, outputTextMatch) })
  }

  if (match.errorCodes?.length) {
    const errorCode = firstExactMatch(frame.errorCode, match.errorCodes)
    if (!errorCode) return undefined
    matched.push({ matchedField: 'errorCodes', matchedValue: errorCode, snippet: frame.errorCode })
  }

  if (match.errorTypes?.length) {
    const errorType = firstExactMatch(frame.errorType, match.errorTypes)
    if (!errorType) return undefined
    matched.push({ matchedField: 'errorTypes', matchedValue: errorType, snippet: frame.errorType })
  }

  if (match.errorMessageIncludes?.length) {
    if (!frame.errorMessage) return undefined
    const errorMessageMatch = firstSubstringMatch(frame.errorMessage, match.errorMessageIncludes)
    if (!errorMessageMatch) return undefined
    matched.push({ matchedField: 'errorMessageIncludes', matchedValue: errorMessageMatch, snippet: snippetAround(frame.errorMessage, errorMessageMatch) })
  }

  if (match.finishReasons?.length) {
    const finishReason = firstExactMatch(frame.finishReason ?? frame.status, match.finishReasons)
    if (!finishReason) return undefined
    matched.push({ matchedField: 'finishReasons', matchedValue: finishReason, snippet: frame.finishReason ?? frame.status })
  }

  if (match.jsonPathsExists?.length) {
    const jsonPath = firstJsonPathMatch(frame, match.jsonPathsExists)
    if (!jsonPath) return undefined
    matched.push({ matchedField: 'jsonPathsExists', matchedValue: jsonPath, snippet: jsonPath })
  }

  if (match.rawTextIncludes?.length) {
    if (!frame.rawText) return undefined
    const rawTextMatch = firstSubstringMatch(frame.rawText, match.rawTextIncludes)
    if (!rawTextMatch) return undefined
    matched.push({ matchedField: 'rawTextIncludes', matchedValue: rawTextMatch, snippet: snippetAround(frame.rawText, rawTextMatch) })
  }

  return matched.find((item) => item.snippet) ?? matched[0]
}

function outputTextExcluded(frame: ResponseSemanticFrame, match: ResponseInspectionPolicyMatch): boolean {
  if (!frame.text || frame.visibleOutput === false || !match.outputTextIncludes?.length || !match.outputTextExcludes?.length) return false
  return Boolean(firstSubstringMatch(frame.text, match.outputTextExcludes))
}

function buildPolicyDecision(
  match: ResponseInspectionMatchResult,
  downstreamWritten: boolean,
  action: ResponseInspectionDecision['action']
): ResponseInspectionDecision {
  const policy = match.policy
  const frame = match.matchedFrame
  return {
    reason: configuredPolicyReason(policy, frame, downstreamWritten),
    action,
    transport: frame.transport,
    triggerPhase: downstreamWritten ? 'after_downstream_write' : 'before_downstream_write',
    endpointFamily: frame.endpointFamily,
    frameType: frame.frameType,
    upstreamEventType: frame.eventType,
    upstreamErrorCode: frame.errorCode,
    upstreamErrorType: frame.errorType,
    upstreamErrorMessage: frame.errorMessage,
    finishReason: frame.finishReason ?? frame.status,
    rewriteErrorCode: rewriteErrorCode(policy, frame),
    rewriteMessage: frame.errorMessage ?? `响应命中检查策略：${policy.name}`,
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

function configuredPolicyReason(policy: RuntimeResponseInspectionPolicy, frame: ResponseSemanticFrame, downstreamWritten: boolean): ResponseInspectionDecision['reason'] {
  if (policy.source !== 'system_default') return 'configured_response_policy'
  return downstreamWritten && frame.errorCode === 'cyber_policy'
    ? 'cyber_policy_response_failure'
    : 'before_downstream_write_response_failure'
}

function responseInspectionDecisionAction(
  policy: RuntimeResponseInspectionPolicy,
  transport: 'json' | 'sse'
): ResponseInspectionDecision['action'] {
  if (policy.executionMode === 'dry_run') return 'dry_run'
  if (policy.dataHandling === 'discard_event' && transport === 'sse') return 'discard_event'
  if (policy.dataHandling === 'discard_event') return 'dry_run'
  if (policy.dataHandling === 'discard_response') return 'discard_response'
  return 'replace_with_failure'
}

function rewriteErrorCode(policy: RuntimeResponseInspectionPolicy, frame: ResponseSemanticFrame): string {
  if (policy.retryEnabled && frame.transport === 'sse' && frame.errorCode === 'cyber_policy') {
    return gatewayStreamClientRetryErrorCode
  }
  return frame.errorCode ?? 'response_inspection_matched'
}

function failureEventForDecision(decision: ResponseInspectionDecision, clientRetryEnabled: boolean): Buffer | undefined {
  if (decision.action === 'discard_event' || decision.action === 'dry_run') return undefined
  const { errorCode, message } = responseInspectionFailurePayloadForDecision(decision, clientRetryEnabled)
  return buildGatewayStreamFailureEvent(message, errorCode)
}

function policyMatchesAccountScope(policy: ResponseInspectionPolicySummary, account: UpstreamAccount): boolean {
  if (!policy.enabled) return false
  if (policy.scopeType === 'protocol') return policy.protocolCode === account.protocolCode && policy.providerCode === undefined
  return policy.protocolCode === account.protocolCode && policy.providerCode === account.providerCode
}

function runtimePolicyFromSummary(policy: ResponseInspectionPolicySummary, source: ResponseInspectionPolicySource): RuntimeResponseInspectionPolicy {
  const runtime = responseInspectionPolicyActionRuntime(policy.action)
  return {
    id: policy.id,
    source,
    name: policy.name,
    enabled: policy.enabled,
    priority: policy.priority,
    scopeType: policy.scopeType,
    protocolCode: policy.protocolCode,
    providerCode: policy.providerCode,
    match: policy.match,
    action: policy.action,
    ...runtime
  }
}

function sourceOrder(source: ResponseInspectionPolicySource): number {
  if (source === 'account') return 0
  if (source === 'management') return 1
  return 2
}

function scopeOrder(policy: RuntimeResponseInspectionPolicy): number {
  if (policy.source === 'account') return 0
  return policy.scopeType === 'provider' ? 0 : 1
}

function firstExactMatch(value: string | undefined, needles: string[] | undefined): string | undefined {
  if (!value || !needles?.length) return undefined
  const normalized = value.toLowerCase()
  return needles.find((needle) => needle.toLowerCase() === normalized)
}

function firstSubstringMatch(value: string, needles: string[] | undefined): string | undefined {
  if (!needles?.length) return undefined
  const normalized = value.toLowerCase()
  return needles.find((needle) => normalized.includes(needle.toLowerCase()))
}

function firstJsonPathMatch(frame: ResponseSemanticFrame, needles: string[] | undefined): string | undefined {
  if (!needles?.length) return undefined
  return needles.find((needle) =>
    (frame.rawJson !== undefined && jsonPathExists(frame.rawJson, needle))
    || Boolean(frame.rawJsonPaths?.includes(needle))
  )
}

function jsonPathExists(value: unknown, path: string): boolean {
  const parts = path.split('.').map((part) => part.trim()).filter(Boolean)
  if (!parts.length) return false
  let current: unknown = value
  for (const part of parts) {
    if (Array.isArray(current)) {
      const index = Number(part)
      if (!Number.isInteger(index) || index < 0 || index >= current.length) return false
      current = current[index]
      continue
    }
    if (typeof current !== 'object' || current === null) return false
    if (!Object.prototype.hasOwnProperty.call(current, part)) return false
    current = (current as Record<string, unknown>)[part]
  }
  return hasJsonPathMeaningfulValue(current)
}

function hasJsonPathMeaningfulValue(value: unknown): boolean {
  if (value === null || value === undefined || value === false) return false
  if (typeof value === 'string') return value.trim().length > 0
  if (Array.isArray(value)) return value.length > 0
  if (typeof value === 'object') return hasOwnEnumerableKey(value as Record<string, unknown>)
  return true
}

function hasOwnEnumerableKey(value: Record<string, unknown>): boolean {
  for (const key in value) {
    if (Object.prototype.hasOwnProperty.call(value, key)) return true
  }
  return false
}

function accountResponseInspectionRules(account: UpstreamAccount): RuntimeResponseInspectionPolicy[] {
  const rules = normalizeAccountResponseInspectionRules(account.credentials.response_inspection_rules)
  return rules.map((rule, index) => {
    const runtime = responseInspectionPolicyActionRuntime(rule.action)
    return {
      id: `account_rule_${index + 1}`,
      source: 'account',
      name: rule.name,
      enabled: rule.enabled,
      priority: rule.priority,
      scopeType: 'provider',
      protocolCode: account.protocolCode || OPENAI_PROTOCOL_CODE,
      providerCode: account.providerCode,
      match: rule.match,
      action: rule.action,
      ...runtime
    }
  })
}

function snippetAround(value: string, needle: string): string {
  const index = value.toLowerCase().indexOf(needle.toLowerCase())
  if (index < 0) return value.slice(0, textMatchSnippetChars)
  const start = Math.max(0, index - 40)
  const end = Math.min(value.length, index + needle.length + 80)
  return value.slice(start, end)
}

function isDeferrableLeadingChatCompletionNoopEvent(event: ParsedOpenAIStreamEvent): boolean {
  const data = event.data
  if (!data || data.object !== 'chat.completion.chunk') return false
  if (data.error !== undefined || data.usage !== undefined) return false
  const choices = Array.isArray(data.choices) ? data.choices : []
  if (choices.length === 0) return false
  return choices.every(isNoopChatCompletionChoice)
}

function isNoopChatCompletionChoice(value: unknown): boolean {
  const choice = objectValue(value)
  if (!choice) return false
  if (choice.finish_reason !== undefined && choice.finish_reason !== null) return false
  if (typeof choice.text === 'string' && choice.text.length > 0) return false
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

class PendingSseEventBuffer {
  private chunks: Buffer[] = []
  private headIndex = 0
  private size = 0
  private nextBoundaryEndIndex: number | undefined

  get length(): number {
    return this.size
  }

  push(chunk: Buffer): void {
    if (chunk.length === 0) return
    const previousSize = this.size
    const previousTail = this.tail(3)
    this.chunks.push(chunk)
    this.size += chunk.length
    if (this.nextBoundaryEndIndex === undefined) {
      this.nextBoundaryEndIndex = findBoundaryEndAfterAppend(previousSize, previousTail, chunk)
    }
  }

  shiftEvent(): Buffer | undefined {
    if (this.nextBoundaryEndIndex === undefined) return undefined
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
    if (this.size === 0) return Buffer.alloc(0)
    const hasBoundary = this.endsWithBoundary()
    const drained = this.drain()
    return hasBoundary
      ? drained
      : Buffer.concat([drained, sseEventBoundarySuffix], drained.length + sseEventBoundarySuffix.length)
  }

  private consumePrefix(length: number): Buffer {
    if (length <= 0 || this.size === 0) return Buffer.alloc(0)
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
      if (!current) break
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
      if (boundary !== undefined) return boundary
      offset += chunk.length
      tail = trailingBytes(tail, chunk, 3)
    }
    return undefined
  }

  private tail(length: number): Buffer {
    if (length <= 0 || this.size === 0) return Buffer.alloc(0)
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
    if (this.headIndex === 0) return
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
  if (previousTail.length === 0 || chunk.length === 0) return undefined
  const prefix = chunk.subarray(0, Math.min(3, chunk.length))
  const combined = Buffer.concat([previousTail, prefix], previousTail.length + prefix.length)
  const boundary = findSseEventBoundary(combined)
  if (!boundary || boundary.index >= previousTail.length || boundary.endIndex <= previousTail.length) return undefined
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
  if (chunk.length >= length) return chunk.subarray(chunk.length - length)
  const combinedLength = Math.min(length, previousTail.length + chunk.length)
  return Buffer.concat([previousTail, chunk], previousTail.length + chunk.length).subarray(previousTail.length + chunk.length - combinedLength)
}

function bufferEndsWith(buffer: Buffer, suffix: Buffer): boolean {
  return buffer.length >= suffix.length && buffer.subarray(buffer.length - suffix.length).equals(suffix)
}

const sseCrLfBoundary = Buffer.from('\r\n\r\n', 'utf8')
const sseLfBoundary = Buffer.from('\n\n', 'utf8')
const sseCrBoundary = Buffer.from('\r\r', 'utf8')
