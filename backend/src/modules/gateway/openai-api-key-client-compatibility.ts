import type { Request } from 'express'

import { normalizeAccountClientCompatibility } from '../../domain/account-client-compatibility.js'
import type { AccountClientCompatibility } from '../../domain/types.js'
import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from './openai-gateway-json-parser.js'
import { splitPathAndQuery } from './openai-gateway-route-helpers.js'
import {
  openAICodexOriginator,
  openAICodexResponsesBetaHeader,
  openAICodexUserAgent,
  openAICodexVersion
} from './openai-codex-client-headers.js'
import { normalizeOpenAICodexBuiltinTools } from './openai-oauth-codex-normalizer.js'

export interface OpenAIClientCompatibilityAccount {
  clientCompatibility?: AccountClientCompatibility
}

export async function buildOpenAIClientCompatibilityBody(
  req: Request,
  account: OpenAIClientCompatibilityAccount,
  signal?: AbortSignal
): Promise<Buffer | undefined> {
  if (normalizeAccountClientCompatibility(account.clientCompatibility) !== 'codex_responses') {
    return undefined
  }
  if (!isOpenAIResponsesPostRequest(req)) {
    return undefined
  }
  const body = await parseOpenAIClientCompatibilityJsonBody(req, signal)
  applyCodexResponsesCompatibility(body)
  return Buffer.from(JSON.stringify(body), 'utf8')
}

export function applyOpenAIClientCompatibilityHeaders(
  req: Request,
  account: OpenAIClientCompatibilityAccount,
  headers: Headers
): void {
  if (normalizeAccountClientCompatibility(account.clientCompatibility) !== 'codex_responses') {
    return
  }
  if (!isOpenAIResponsesPostRequest(req)) {
    return
  }
  headers.set('accept', 'text/event-stream')
  headers.set('content-type', 'application/json')
  setHeaderIfMissing(headers, 'originator', openAICodexOriginator)
  setHeaderIfMissing(headers, 'user-agent', openAICodexUserAgent)
  setHeaderIfMissing(headers, 'version', openAICodexVersion)
  if (!headers.get('openai-beta')?.toLowerCase().includes('responses')) {
    headers.set('openai-beta', openAICodexResponsesBetaHeader)
  }
}

function isOpenAIResponsesPostRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const { path } = splitPathAndQuery(req.originalUrl || req.path || '')
  return (path.replace(/^\/v1(?=\/|$)/, '') || '/') === '/responses'
}

async function parseOpenAIClientCompatibilityJsonBody(req: Request, signal?: AbortSignal): Promise<Record<string, unknown>> {
  const body = req.body
  if (isPlainObject(body)) {
    return { ...body }
  }

  const requestWithBody = req as GatewayRawBodyRequest
  if (requestWithBody.gatewayParsedJsonBodyAvailable && isPlainObject(requestWithBody.gatewayParsedJsonBody)) {
    return { ...requestWithBody.gatewayParsedJsonBody }
  }

  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    throw new Error('Codex Responses 兼容模式要求请求体是有效的 JSON 对象')
  }

  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) {
    return {}
  }

  let parsed: unknown
  try {
    parsed = rawBody.length > gatewayJsonBodyInlineParseMaxBytes
      ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
      : JSON.parse(rawBody.toString('utf8')) as unknown
  } catch (error) {
    if (isGatewayJsonWorkerQueueFullError(error)) {
      throw new Error('网关请求解析繁忙，请稍后重试')
    }
    throw new Error('Codex Responses 兼容模式要求请求体是有效的 JSON 对象')
  }
  if (!isPlainObject(parsed)) {
    throw new Error('Codex Responses 兼容模式要求请求体是 JSON 对象')
  }
  return { ...parsed }
}

function applyCodexResponsesCompatibility(body: Record<string, unknown>): void {
  if (typeof body.input === 'string') {
    body.input = [
      {
        type: 'message',
        role: 'user',
        content: [
          {
            type: 'input_text',
            text: body.input
          }
        ]
      }
    ]
  } else if (Array.isArray(body.input)) {
    body.input = normalizeCodexResponsesInputItems(body.input)
  }
  if (!Object.prototype.hasOwnProperty.call(body, 'instructions')) {
    body.instructions = ''
  }
  if (!Array.isArray(body.tools)) {
    body.tools = []
  }
  if (typeof body.tool_choice !== 'string' && !isPlainObject(body.tool_choice)) {
    body.tool_choice = 'auto'
  }
  normalizeOpenAICodexBuiltinTools(body)
  if (typeof body.parallel_tool_calls !== 'boolean') {
    body.parallel_tool_calls = true
  }
  body.stream = true
  body.store = false
  body.include = ensureReasoningEncryptedContent(body.include)
  delete body.max_output_tokens
  delete body.max_completion_tokens
  delete body.temperature
  delete body.top_p
  delete body.context_management
  delete body.truncation
  delete body.user
}

function ensureReasoningEncryptedContent(value: unknown): string[] {
  const include = Array.isArray(value)
    ? value.filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
    : []
  return include.includes('reasoning.encrypted_content')
    ? include
    : [...include, 'reasoning.encrypted_content']
}

function normalizeCodexResponsesInputItems(input: unknown[]): unknown[] {
  return input.map((item) => {
    if (!isPlainObject(item) || item.role !== 'system') {
      return item
    }
    return {
      ...item,
      role: 'developer'
    }
  })
}

function setHeaderIfMissing(headers: Headers, name: string, value: string): void {
  if (!headers.get(name)) {
    headers.set(name, value)
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
