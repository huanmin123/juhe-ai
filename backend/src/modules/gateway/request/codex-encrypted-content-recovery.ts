import type { Request } from 'express'

import { isOpenAIProtocolProfile } from '../../../domain/provider-protocol.js'
import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { gatewayJsonBodyInlineParseMaxBytes } from './body.js'
import { parseGatewayJsonBodyInWorker } from './json-parser.js'
import { serializeGatewayJsonObject } from './serialized-json-body.js'

type JsonRecord = Record<string, unknown>

export type CodexEncryptedContentRecoverySignal =
  | 'thinking_signature_invalid'
  | 'invalid_encrypted_content'
  | 'encrypted_content_decryption_failed'

export const codexEncryptedContentRecoveryExhaustedMessage =
  '上游拒绝了加密上下文，网关已尝试一次兼容性清理但仍然失败。请新建会话，或不要携带上一会话的加密 reasoning、工具输出或 compaction 后重新发送请求。'

export interface CodexEncryptedContentRecoveryMetadata {
  strategy: 'codex_encrypted_content_cleanup'
  signal: CodexEncryptedContentRecoverySignal
  removedReasoningEncryptedContentCount: number
  removedFunctionOutputEncryptedContentCount: number
  removedAgentMessageEncryptedContentCount: number
  removedCompactionEncryptedContentCount: number
  removedReasoningItemCount: number
  removedAgentMessageItemCount: number
  removedCompactionItemCount: number
  preservedPreviousResponseId: boolean
  bodyBytesBefore: number
  bodyBytesAfter: number
}

export type CodexEncryptedContentRecoveryResult =
  | {
      action: 'retry_with_body_variant'
      body: Buffer
      semanticRetryId: string
      metadata: CodexEncryptedContentRecoveryMetadata
    }
  | {
      action: 'not_applicable' | 'not_recoverable'
      signal?: CodexEncryptedContentRecoverySignal
      reason?: 'request_body_parse_failed' | 'no_removable_encrypted_content'
    }

/**
 * Retry one pre-commit OpenAI Responses attempt with opaque encrypted state
 * removed only after the upstream explicitly rejects that state.  The caller
 * owns the one-attempt budget; this helper never mutates the client request.
 */
export async function recoverCodexEncryptedContentRequest(input: {
  req: Request
  account: UpstreamAccount
  /** 保留调用兼容性；恢复门槛由协议画像和 endpoint family 决定。 */
  requestClientCompatibility?: ClientCompatibilityCapability
  body: Buffer | string | undefined
  upstreamErrorText: string
  signal?: AbortSignal
}): Promise<CodexEncryptedContentRecoveryResult> {
  if (
    !isOpenAIProtocolProfile(input.account)
    || gatewayRequestEndpointFamily(input.req) !== 'responses'
  ) {
    return { action: 'not_applicable' }
  }

  const signal = classifyCodexEncryptedContentRecoverySignal(input.upstreamErrorText)
  if (!signal || input.body === undefined) {
    return signal ? { action: 'not_recoverable', signal } : { action: 'not_applicable' }
  }

  const parsed = await parseJsonObject(input.body, input.signal)
  if (parsed.status === 'failed') {
    return { action: 'not_recoverable', signal, reason: 'request_body_parse_failed' }
  }

  const sanitized = removeRejectedCodexEncryptedContent(parsed.value)
  if (!sanitized.changed) {
    return { action: 'not_recoverable', signal, reason: 'no_removable_encrypted_content' }
  }

  const body = serializeGatewayJsonObject(sanitized.body)
  return {
    action: 'retry_with_body_variant',
    body,
    semanticRetryId: `codex_encrypted_content_cleanup:${signal}`,
    metadata: {
      strategy: 'codex_encrypted_content_cleanup',
      signal,
      removedReasoningEncryptedContentCount: sanitized.removedReasoningEncryptedContentCount,
      removedFunctionOutputEncryptedContentCount: sanitized.removedFunctionOutputEncryptedContentCount,
      removedAgentMessageEncryptedContentCount: sanitized.removedAgentMessageEncryptedContentCount,
      removedCompactionEncryptedContentCount: sanitized.removedCompactionEncryptedContentCount,
      removedReasoningItemCount: sanitized.removedReasoningItemCount,
      removedAgentMessageItemCount: sanitized.removedAgentMessageItemCount,
      removedCompactionItemCount: sanitized.removedCompactionItemCount,
      preservedPreviousResponseId: typeof sanitized.body.previous_response_id === 'string'
        && sanitized.body.previous_response_id.trim().length > 0,
      bodyBytesBefore: bodyByteLength(input.body),
      bodyBytesAfter: body.byteLength
    }
  }
}

export function classifyCodexEncryptedContentRecoverySignal(
  upstreamErrorText: string
): CodexEncryptedContentRecoverySignal | undefined {
  const exactSignal = signalForExactErrorCode(upstreamErrorText)
  if (exactSignal) return exactSignal

  for (const payload of structuredErrorPayloads(upstreamErrorText)) {
    const signal = signalForStructuredErrorPayload(payload)
    if (signal) return signal
  }
  if (looksLikeHttpErrorWrapper(upstreamErrorText) && looksLikeEncryptedContentDecryptionFailure(upstreamErrorText)) {
    return 'encrypted_content_decryption_failed'
  }
  return undefined
}

function signalForExactErrorCode(value: string): CodexEncryptedContentRecoverySignal | undefined {
  switch (value.trim().toLowerCase()) {
    case 'thinking_signature_invalid':
      return 'thinking_signature_invalid'
    case 'invalid_encrypted_content':
      return 'invalid_encrypted_content'
    case 'encrypted_content_decryption_failed':
      return 'encrypted_content_decryption_failed'
    default:
      return undefined
  }
}

function structuredErrorPayloads(value: string): JsonRecord[] {
  const payloads: JsonRecord[] = []
  const direct = parseJsonRecord(value)
  if (direct) payloads.push(direct)

  let eventDataLines: string[] = []
  const appendEventPayload = (): void => {
    if (eventDataLines.length === 0) return
    const payload = parseJsonRecord(eventDataLines.join('\n'))
    if (payload) payloads.push(payload)
    eventDataLines = []
  }
  for (const line of value.split(/\r?\n/)) {
    if (line.length === 0) {
      appendEventPayload()
      continue
    }
    if (line.startsWith('data:')) {
      eventDataLines.push(line.slice('data:'.length).trimStart())
    }
  }
  appendEventPayload()
  return payloads
}

function signalForStructuredErrorPayload(payload: JsonRecord): CodexEncryptedContentRecoverySignal | undefined {
  const nestedError = isPlainObject(payload.error) ? payload.error : undefined
  const candidates = nestedError ? [payload, nestedError] : [payload]
  for (const candidate of candidates) {
    const code = typeof candidate.code === 'string'
      ? signalForExactErrorCode(candidate.code)
      : undefined
    if (code) return code

    const errorPayload = candidate === nestedError
      || payload.type === 'error'
      || nestedError !== undefined
    if (errorPayload && typeof candidate.message === 'string' && looksLikeEncryptedContentDecryptionFailure(candidate.message)) {
      return 'encrypted_content_decryption_failed'
    }
  }
  return undefined
}

function looksLikeEncryptedContentDecryptionFailure(value: string): boolean {
  const normalized = value.toLowerCase()
  return normalized.includes('encrypted')
    && (
      normalized.includes('could not be decrypted')
      || normalized.includes('could not be decoded')
      || normalized.includes('could not be verified')
      || normalized.includes('could not be parsed')
    )
}

function looksLikeHttpErrorWrapper(value: string): boolean {
  return /^\s*http\s+4\d{2}\b[\s;,:-]*cause\s*:/i.test(value)
}

function parseJsonRecord(value: string): JsonRecord | undefined {
  try {
    const parsed = JSON.parse(value) as unknown
    return isPlainObject(parsed) ? parsed : undefined
  } catch {
    return undefined
  }
}

async function parseJsonObject(body: Buffer | string, signal?: AbortSignal): Promise<
  | { status: 'valid'; value: JsonRecord }
  | { status: 'failed' }
> {
  try {
    const bytes = Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
    const parsed = bytes.byteLength > gatewayJsonBodyInlineParseMaxBytes
      ? await parseGatewayJsonBodyInWorker(bytes, undefined, signal)
      : JSON.parse(bytes.toString('utf8')) as unknown
    return isPlainObject(parsed)
      ? { status: 'valid', value: { ...parsed } }
      : { status: 'failed' }
  } catch {
    return { status: 'failed' }
  }
}

function removeRejectedCodexEncryptedContent(body: JsonRecord): {
  body: JsonRecord
  changed: boolean
  removedReasoningEncryptedContentCount: number
  removedFunctionOutputEncryptedContentCount: number
  removedAgentMessageEncryptedContentCount: number
  removedCompactionEncryptedContentCount: number
  removedReasoningItemCount: number
  removedAgentMessageItemCount: number
  removedCompactionItemCount: number
} {
  const inputItems = Array.isArray(body.input)
    ? body.input
    : isPlainObject(body.input)
      ? [body.input]
      : undefined
  if (!inputItems) {
    return {
      body,
      changed: false,
      removedReasoningEncryptedContentCount: 0,
      removedFunctionOutputEncryptedContentCount: 0,
      removedAgentMessageEncryptedContentCount: 0,
      removedCompactionEncryptedContentCount: 0,
      removedReasoningItemCount: 0,
      removedAgentMessageItemCount: 0,
      removedCompactionItemCount: 0
    }
  }

  let changed = false
  let removedReasoningEncryptedContentCount = 0
  let removedFunctionOutputEncryptedContentCount = 0
  let removedAgentMessageEncryptedContentCount = 0
  let removedCompactionEncryptedContentCount = 0
  let removedReasoningItemCount = 0
  let removedAgentMessageItemCount = 0
  let removedCompactionItemCount = 0
  const input: unknown[] = []

  for (const item of inputItems) {
    if (!isPlainObject(item)) {
      input.push(item)
      continue
    }

    if (item.type === 'reasoning' && typeof item.encrypted_content === 'string') {
      const copy = { ...item }
      delete copy.encrypted_content
      changed = true
      removedReasoningEncryptedContentCount += 1
      if (isEmptyReasoningItem(copy)) {
        removedReasoningItemCount += 1
        continue
      }
      input.push(copy)
      continue
    }

    if (isCodexCompactionItemWithEncryptedContent(item)) {
      changed = true
      removedCompactionEncryptedContentCount += 1
      removedCompactionItemCount += 1
      continue
    }

    if (item.type === 'function_call_output' || item.type === 'custom_tool_call_output') {
      const output = stripEncryptedContentItems(item.output)
      if (output.changed) {
        input.push({ ...item, output: output.output })
        changed = true
        removedFunctionOutputEncryptedContentCount += output.removedCount
        continue
      }
    }

    if (item.type === 'agent_message') {
      const content = stripEncryptedContentItems(item.content)
      if (content.changed) {
        changed = true
        removedAgentMessageEncryptedContentCount += content.removedCount
        if (Array.isArray(content.output) && content.output.length === 0) {
          removedAgentMessageItemCount += 1
          continue
        }
        input.push({ ...item, content: content.output })
        continue
      }
    }
    input.push(item)
  }

  return {
    body: changed
      ? { ...body, input: Array.isArray(body.input) ? input : input[0] ?? [] }
      : body,
    changed,
    removedReasoningEncryptedContentCount,
    removedFunctionOutputEncryptedContentCount,
    removedAgentMessageEncryptedContentCount,
    removedCompactionEncryptedContentCount,
    removedReasoningItemCount,
    removedAgentMessageItemCount,
    removedCompactionItemCount
  }
}

function isCodexCompactionItemWithEncryptedContent(item: JsonRecord): boolean {
  return (
    (item.type === 'compaction'
      || item.type === 'compaction_summary'
      || item.type === 'context_compaction')
    && typeof item.encrypted_content === 'string'
  )
}

function stripEncryptedContentItems(value: unknown): {
  output: unknown
  changed: boolean
  removedCount: number
} {
  if (Array.isArray(value)) {
    let removedCount = 0
    const output = value.filter((item) => {
      const encrypted = isPlainObject(item)
        && item.type === 'encrypted_content'
        && typeof item.encrypted_content === 'string'
      if (encrypted) removedCount += 1
      return !encrypted
    })
    return {
      output,
      changed: removedCount > 0,
      removedCount
    }
  }
  if (
    isPlainObject(value)
    && value.type === 'encrypted_content'
    && typeof value.encrypted_content === 'string'
  ) {
    return {
      output: [],
      changed: true,
      removedCount: 1
    }
  }
  return { output: value, changed: false, removedCount: 0 }
}

function isEmptyReasoningItem(item: JsonRecord): boolean {
  for (const [key, value] of Object.entries(item)) {
    if (key === 'type' || key === 'id' || key === 'status') continue
    if (value === null || value === undefined) continue
    if (Array.isArray(value) && value.length === 0) continue
    if (typeof value === 'string' && value.trim().length === 0) continue
    return false
  }
  return true
}

function bodyByteLength(body: Buffer | string): number {
  return Buffer.isBuffer(body) ? body.byteLength : Buffer.byteLength(body, 'utf8')
}

function isPlainObject(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
