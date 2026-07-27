import type { Request } from 'express'

import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../request/body.js'
import type { ParsedOpenAIStreamEvent } from '../protocols/openai-v1/stream-events.js'
import type { ResponseSemanticFrame } from '../protocols/openai-v1/response-semantics.js'

export const codexCompactionContractMismatchErrorCode = 'codex_compaction_contract_mismatch'

export interface CodexCompactionContractCounts {
  outputItemCount: number
  compactionItemCount: number
}

export interface CodexCompactionContractMismatchInput extends CodexCompactionContractCounts {
  transport: 'json' | 'sse'
  eventType?: string
  force?: boolean
  message?: string
}

export function codexCompactionExpectedForRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const normalizedPath = normalizedOpenAIRequestPath(req)
  if (normalizedPath === '/responses/compact') return true
  if (normalizedPath === '/responses' && getGatewayRequestBodyState(req)?.compactionTrigger === true) return true
  return normalizedPath === '/responses' && requestBodyHasCompactionTrigger(req)
}

export function codexCompactionContractMismatchFrame(
  input: CodexCompactionContractMismatchInput
): ResponseSemanticFrame | undefined {
  if (!input.force && input.compactionItemCount === 1) return undefined
  return {
    frameType: 'error',
    protocol: 'openai_v1',
    endpointFamily: 'responses',
    transport: input.transport,
    errorCode: codexCompactionContractMismatchErrorCode,
    errorType: 'invalid_response_contract',
    errorMessage: input.message ?? `Codex Remote Compaction V2 响应结构无效：期望恰好 1 个 compaction output item，实际 ${input.compactionItemCount} 个，output item 总数 ${input.outputItemCount} 个`,
    rawJsonPaths: ['output'],
    rawText: input.transport === 'sse' ? input.eventType : undefined,
    eventType: input.eventType,
    provenance: 'gateway_protocol_contract'
  }
}

export function countCodexCompactionOutputItemsFromJson(value: unknown): CodexCompactionContractCounts | undefined {
  const root = objectValue(value)
  if (!root) return undefined
  const output = Array.isArray(root.output) ? root.output : undefined
  if (!output) return undefined
  return countCodexCompactionOutputItems(output)
}

export function countCodexCompactionOutputItemsFromStreamEvent(event: ParsedOpenAIStreamEvent): CodexCompactionContractCounts | undefined {
  if (event.eventType !== 'response.output_item.done' && event.eventName !== 'response.output_item.done') {
    return undefined
  }
  const item = objectValue(event.data?.item)
  return {
    outputItemCount: 1,
    compactionItemCount: isCodexDeserializableCompactionItem(item) ? 1 : 0
  }
}

function countCodexCompactionOutputItems(output: unknown[]): CodexCompactionContractCounts {
  let compactionItemCount = 0
  for (const item of output) {
    if (isCodexDeserializableCompactionItem(objectValue(item))) {
      compactionItemCount += 1
    }
  }
  return {
    outputItemCount: output.length,
    compactionItemCount
  }
}

function isCodexDeserializableCompactionItem(item: Record<string, unknown> | undefined): boolean {
  if (!item) return false
  if (item.type !== 'compaction' && item.type !== 'compaction_summary') return false
  return typeof item.encrypted_content === 'string'
}

function requestBodyHasCompactionTrigger(req: Request): boolean {
  const parsedBody = parsedJsonObjectBody(req)
  if (parsedBody && responsesInputHasCompactionTrigger(parsedBody)) {
    return true
  }
  return false
}

function parsedJsonObjectBody(req: Request): Record<string, unknown> | undefined {
  const request = req as GatewayRawBodyRequest
  if (objectValue(req.body)) {
    return req.body as Record<string, unknown>
  }
  if (request.gatewayParsedJsonBodyAvailable && objectValue(request.gatewayParsedJsonBody)) {
    return request.gatewayParsedJsonBody as Record<string, unknown>
  }
  return undefined
}

function responsesInputHasCompactionTrigger(body: Record<string, unknown>): boolean {
  const input = dataDescriptorValue(body, 'input')
  if (!input.exists || !Array.isArray(input.value)) return false
  const length = input.value.length
  for (let index = 0; index < length; index += 1) {
    const item = dataDescriptorValue(input.value, String(index))
    if (!item.exists) continue
    const record = objectValue(item.value)
    if (!record) continue
    const type = dataDescriptorValue(record, 'type')
    if (type.exists && type.value === 'compaction_trigger') return true
  }
  return false
}

function dataDescriptorValue(
  value: object,
  key: string
): { exists: true; value: unknown } | { exists: false } {
  const descriptor = Object.getOwnPropertyDescriptor(value, key)
  if (!descriptor || !('value' in descriptor)) {
    return { exists: false }
  }
  return { exists: true, value: descriptor.value }
}

function normalizedOpenAIRequestPath(req: Request): string {
  const rawPath = (req.originalUrl || req.path || '').split('?', 1)[0] || '/'
  const path = rawPath.startsWith('/') ? rawPath : `/${rawPath}`
  return path.replace(/^\/v1(?=\/|$)/, '') || '/'
}

function objectValue(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}
