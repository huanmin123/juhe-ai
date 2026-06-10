import type { Request } from 'express'

import {
  gatewayJsonBodyInlineParseMaxBytes
} from './openai-gateway-request-body.js'
import {
  parseGatewayJsonBodyInWorker
} from './openai-gateway-json-parser.js'
import { splitPathAndQuery } from './openai-gateway-route-helpers.js'

export interface OpenAIResponsesRequestRecoveryFeatures {
  endpointFamily: 'responses'
  hasEncryptedReasoning: boolean
  encryptedReasoningItemCount: number
  hasPreviousResponseId: boolean
  hasFunctionCallOutput: boolean
}

export interface OpenAIResponsesEncryptedReasoningRecoveryResult {
  body: Buffer
  features: OpenAIResponsesRequestRecoveryFeatures
  removedEncryptedReasoningItemCount: number
  removedReasoningItemCount: number
  removedPreviousResponseId: boolean
}

export async function recoverOpenAIResponsesEncryptedReasoningBody(
  req: Request,
  body: Buffer | string | undefined,
  signal?: AbortSignal
): Promise<OpenAIResponsesEncryptedReasoningRecoveryResult | undefined> {
  if (!isOpenAIResponsesPostRequest(req)) {
    return undefined
  }
  const parsed = await parseJsonBodyObject(body, signal)
  if (!parsed) {
    return undefined
  }
  const features = inspectOpenAIResponsesRecoveryFeatures(parsed)
  if (!features.hasEncryptedReasoning) {
    return undefined
  }

  const recovery = removeEncryptedReasoningFromInput(parsed.input)
  if (recovery.removedEncryptedReasoningItemCount <= 0) {
    return undefined
  }
  if (recovery.inputChanged) {
    parsed.input = recovery.input
  }

  let removedPreviousResponseId = false
  if (features.hasPreviousResponseId && !features.hasFunctionCallOutput) {
    delete parsed.previous_response_id
    removedPreviousResponseId = true
  }

  return {
    body: Buffer.from(JSON.stringify(parsed), 'utf8'),
    features,
    removedEncryptedReasoningItemCount: recovery.removedEncryptedReasoningItemCount,
    removedReasoningItemCount: recovery.removedReasoningItemCount,
    removedPreviousResponseId
  }
}

export function inspectOpenAIResponsesRecoveryFeatures(body: Record<string, unknown>): OpenAIResponsesRequestRecoveryFeatures {
  const inputItems = inputItemsFromValue(body.input)
  let encryptedReasoningItemCount = 0
  let hasFunctionCallOutput = false
  for (const item of inputItems) {
    if (item.type === 'reasoning' && Object.prototype.hasOwnProperty.call(item, 'encrypted_content')) {
      encryptedReasoningItemCount += 1
    }
    if (item.type === 'function_call_output') {
      hasFunctionCallOutput = true
    }
  }
  return {
    endpointFamily: 'responses',
    hasEncryptedReasoning: encryptedReasoningItemCount > 0,
    encryptedReasoningItemCount,
    hasPreviousResponseId: typeof body.previous_response_id === 'string' && body.previous_response_id.trim().length > 0,
    hasFunctionCallOutput
  }
}

function isOpenAIResponsesPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') {
    return false
  }
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses'
}

async function parseJsonBodyObject(
  body: Buffer | string | undefined,
  signal?: AbortSignal
): Promise<Record<string, unknown> | undefined> {
  if (body === undefined) {
    return undefined
  }
  if (!bodyLooksLikeJsonObject(body)) {
    return undefined
  }
  try {
    const parsed = await parseJsonBody(body, signal)
    return isPlainObject(parsed) ? { ...parsed } : undefined
  } catch {
    return undefined
  }
}

async function parseJsonBody(body: Buffer | string, signal?: AbortSignal): Promise<unknown> {
  if (Buffer.isBuffer(body)) {
    return body.length > gatewayJsonBodyInlineParseMaxBytes
      ? await parseGatewayJsonBodyInWorker(body, undefined, signal)
      : JSON.parse(body.toString('utf8')) as unknown
  }

  const bodyBytes = Buffer.byteLength(body, 'utf8')
  return bodyBytes > gatewayJsonBodyInlineParseMaxBytes
    ? await parseGatewayJsonBodyInWorker(Buffer.from(body, 'utf8'), undefined, signal)
    : JSON.parse(body) as unknown
}

function bodyLooksLikeJsonObject(body: Buffer | string): boolean {
  if (Buffer.isBuffer(body)) {
    const index = firstNonWhitespaceBufferIndex(body)
    return index >= 0 && body[index] === jsonObjectOpenByte
  }
  const index = firstNonWhitespaceStringIndex(body)
  return index >= 0 && body.charCodeAt(index) === jsonObjectOpenByte
}

function firstNonWhitespaceBufferIndex(body: Buffer): number {
  for (let index = 0; index < body.length; index += 1) {
    if (!isJsonWhitespaceByte(body[index] ?? 0)) {
      return index
    }
  }
  return -1
}

function firstNonWhitespaceStringIndex(body: string): number {
  for (let index = 0; index < body.length; index += 1) {
    if (!isJsonWhitespaceCode(body.charCodeAt(index))) {
      return index
    }
  }
  return -1
}

function removeEncryptedReasoningFromInput(input: unknown): {
  input: unknown
  inputChanged: boolean
  removedEncryptedReasoningItemCount: number
  removedReasoningItemCount: number
} {
  if (Array.isArray(input)) {
    let inputChanged = false
    let removedEncryptedReasoningItemCount = 0
    let removedReasoningItemCount = 0
    const output: unknown[] = []
    for (const item of input) {
      if (!isPlainObject(item) || item.type !== 'reasoning' || !Object.prototype.hasOwnProperty.call(item, 'encrypted_content')) {
        output.push(item)
        continue
      }
      const nextItem = { ...item }
      delete nextItem.encrypted_content
      removedEncryptedReasoningItemCount += 1
      inputChanged = true
      if (shouldDropEmptyReasoningItem(nextItem)) {
        removedReasoningItemCount += 1
        continue
      }
      output.push(nextItem)
    }
    return {
      input: output,
      inputChanged,
      removedEncryptedReasoningItemCount,
      removedReasoningItemCount
    }
  }

  if (!isPlainObject(input) || input.type !== 'reasoning' || !Object.prototype.hasOwnProperty.call(input, 'encrypted_content')) {
    return {
      input,
      inputChanged: false,
      removedEncryptedReasoningItemCount: 0,
      removedReasoningItemCount: 0
    }
  }

  const nextInput = { ...input }
  delete nextInput.encrypted_content
  return {
    input: nextInput,
    inputChanged: true,
    removedEncryptedReasoningItemCount: 1,
    removedReasoningItemCount: 0
  }
}

function inputItemsFromValue(value: unknown): Array<Record<string, unknown>> {
  if (Array.isArray(value)) {
    return value.filter(isPlainObject)
  }
  return isPlainObject(value) ? [value] : []
}

function shouldDropEmptyReasoningItem(item: Record<string, unknown>): boolean {
  for (const [key, value] of Object.entries(item)) {
    if (key === 'type') {
      continue
    }
    if (key === 'id' || key === 'status') {
      continue
    }
    if (value === null || value === undefined) {
      continue
    }
    if (Array.isArray(value) && value.length === 0) {
      continue
    }
    if (typeof value === 'string' && value.trim().length === 0) {
      continue
    }
    return false
  }
  return true
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isJsonWhitespaceByte(byte: number): boolean {
  return byte === 0x20 || byte === 0x0a || byte === 0x0d || byte === 0x09
}

function isJsonWhitespaceCode(code: number): boolean {
  return code === 0x20 || code === 0x0a || code === 0x0d || code === 0x09
}

const jsonObjectOpenByte = 0x7b
