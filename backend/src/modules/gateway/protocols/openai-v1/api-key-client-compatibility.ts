import type { Request } from 'express'

import type { ClientCompatibilityCapability } from '../../../../domain/types.js'
import {
  getGatewayRequestBodyState,
  type GatewayRawBodyRequest
} from '../../request/body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayRequestJsonBody
} from '../../request/json-parser.js'
import { splitPathAndQuery } from './route-helpers.js'
import {
  isOpenAICodexClientHeaders,
  normalizeOpenAICodexClientHeaders,
  normalizeOpenAICodexResponsesLiteBody
} from '../../adapters/gpt-codex/client-headers.js'
import { normalizeOpenAICodexBuiltinTools } from '../../adapters/gpt-codex/builtin-tools.js'
import { requestModel } from '../../request/metadata.js'

export interface OpenAIClientCompatibilityOptions {
  modelOverride?: string
  requestClientCompatibility?: ClientCompatibilityCapability
}

export async function buildOpenAIClientCompatibilityBody(
  req: Request,
  signal?: AbortSignal,
  options: OpenAIClientCompatibilityOptions = {}
): Promise<Buffer | undefined> {
  if (!shouldForceOpenAICodexResponsesSse(req, options.requestClientCompatibility)) {
    return undefined
  }
  const body = await parseOpenAIClientCompatibilityJsonBody(req, signal)
  if (options.modelOverride) {
    body.model = options.modelOverride
  }
  applyCodexResponsesCompatibility(body)
  normalizeOpenAICodexResponsesLiteBody(body, stringValue(body.model))
  return Buffer.from(JSON.stringify(body), 'utf8')
}

export function applyOpenAIClientCompatibilityHeaders(
  req: Request,
  headers: Headers,
  options: OpenAIClientCompatibilityOptions = {}
): void {
  if (!shouldForceOpenAICodexResponsesSse(req, options.requestClientCompatibility)) {
    return
  }
  if (isOpenAICodexClientHeaders(req.headers)) {
    return
  }
  headers.set('accept', 'text/event-stream')
  headers.set('content-type', 'application/json')
  normalizeOpenAICodexClientHeaders(
    headers,
    options.modelOverride ?? requestModel(req)
  )
}

export function shouldForceOpenAICodexResponsesSse(
  req: Request,
  requestClientCompatibility?: ClientCompatibilityCapability
): boolean {
  return requestClientCompatibility === 'codex_responses'
    && isOpenAIResponsesPostRequest(req)
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
    throw new Error('Codex Responses 请求形态要求请求体是有效的 JSON 对象')
  }

  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) {
    return {}
  }

  let parsed: unknown
  try {
    parsed = await parseGatewayRequestJsonBody(req, undefined, signal)
  } catch (error) {
    if (isGatewayJsonWorkerQueueFullError(error)) {
      throw new Error('网关请求解析繁忙，请稍后重试')
    }
    throw new Error('Codex Responses 请求形态要求请求体是有效的 JSON 对象')
  }
  if (!isPlainObject(parsed)) {
    throw new Error('Codex Responses 请求形态要求请求体是 JSON 对象')
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

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}
