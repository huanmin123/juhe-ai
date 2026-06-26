import type { Request } from 'express'

import type {
  AccountModelMapping,
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily,
  GatewayRequestEndpointFamily
} from '../../../../domain/types.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY
} from '../../../../domain/provider-protocol.js'
import {
  openAIEndpointFamilyFromPath
} from '../../../../domain/openai-endpoint-modes.js'
import {
  geminiEndpointFamilyFromPath
} from '../../../../domain/gemini-endpoint-modes.js'
import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  type GatewayRawBodyRequest
} from '../../request/body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from '../../request/json-parser.js'
import { requestModel } from '../../request/metadata.js'
import { OpenAIOAuthCodexAdapterError } from '../../adapters/gpt-codex/oauth-adapter.js'
import { splitPathAndQuery } from './route-helpers.js'

export interface OpenAIModelMappingRuntimeAccount {
  modelMappings?: AccountModelMapping[]
  providerProtocolProfileId?: string
}

export interface ResolvedOpenAIModelMapping {
  sourceModel: string
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily
  upstreamModel: string
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
}

export function resolveOpenAIAccountModelMapping(
  account: OpenAIModelMappingRuntimeAccount | undefined,
  requestedModel: string | undefined,
  sourceEndpointFamily: GatewayRequestEndpointFamily | undefined
): ResolvedOpenAIModelMapping | undefined {
  const model = requestedModel?.trim()
  if (!model || !sourceEndpointFamily) return undefined
  if (!isAccountModelMappingSourceEndpointFamily(sourceEndpointFamily)) return undefined
  if (account?.providerProtocolProfileId === GEMINI_OPENAI_CHAT_V1BETA_PROFILE_ID && sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return undefined
  }
  const mapping = (account?.modelMappings ?? []).find((item) => (
    item.enabled !== false
    && item.sourceModel === model
    && item.sourceEndpointFamily === sourceEndpointFamily
  ))
  if (!mapping || (mapping.upstreamModel === mapping.sourceModel && mapping.upstreamEndpointFamily === mapping.sourceEndpointFamily)) return undefined
  return {
    sourceModel: mapping.sourceModel,
    sourceEndpointFamily: mapping.sourceEndpointFamily,
    upstreamModel: mapping.upstreamModel,
    upstreamEndpointFamily: mapping.upstreamEndpointFamily
  }
}

export function resolveOpenAIRequestModelMapping(
  req: Request,
  account: OpenAIModelMappingRuntimeAccount | undefined
): ResolvedOpenAIModelMapping | undefined {
  return resolveOpenAIAccountModelMapping(account, requestModel(req), gatewayRequestEndpointFamily(req))
}

export function gatewayRequestEndpointFamily(req: Request): GatewayRequestEndpointFamily | undefined {
  return openAIRequestEndpointFamily(req) ?? anthropicMessagesRequestEndpointFamily(req) ?? geminiRequestEndpointFamily(req)
}

export function openAIRequestEndpointFamily(req: Request): Extract<AccountModelMappingSourceEndpointFamily, 'chat_completions' | 'responses'> | undefined {
  const endpoint = (req.originalUrl || req.path || '').split('?', 1)[0]
  return openAIEndpointFamilyFromPath(endpoint)
}

export function anthropicMessagesRequestEndpointFamily(req: Request): typeof ANTHROPIC_MESSAGES_FAMILY | undefined {
  if (req.method.toUpperCase() !== 'POST') return undefined
  const endpoint = (req.originalUrl || req.path || '').split('?', 1)[0]
  const normalizedPath = (endpoint.startsWith('/') ? endpoint : `/${endpoint}`).replace(/^\/v1(?=\/|$)/, '') || '/'
  return normalizedPath === '/messages' ? ANTHROPIC_MESSAGES_FAMILY : undefined
}

export function geminiRequestEndpointFamily(req: Request): Exclude<Extract<GatewayRequestEndpointFamily, 'generate_content' | 'stream_generate_content' | 'count_tokens' | 'embed_content'>, never> | undefined {
  if (req.method.toUpperCase() !== 'POST') return undefined
  const endpoint = (req.originalUrl || req.path || '').split('?', 1)[0]
  const family = geminiEndpointFamilyFromPath(endpoint)
  return family === 'models' ? undefined : family
}

export function isOpenAIResponsesToChatCompletionsModelMapping(
  mapping: ResolvedOpenAIModelMapping | undefined
): boolean {
  return mapping?.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    && mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
}

export function isOpenAIChatCompletionsToResponsesModelMapping(
  mapping: ResolvedOpenAIModelMapping | undefined
): boolean {
  return mapping?.sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
    && mapping.upstreamEndpointFamily === OPENAI_RESPONSES_FAMILY
}

export function isAnthropicMessagesToChatCompletionsModelMapping(
  mapping: ResolvedOpenAIModelMapping | undefined
): boolean {
  return mapping?.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
    && mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
}

export function isGeminiGenerateContentToChatCompletionsModelMapping(
  mapping: ResolvedOpenAIModelMapping | undefined
): boolean {
  return (
    mapping?.sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
    || mapping?.sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  ) && mapping.upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
}

export function openAIModelMappedUpstreamPathAndQuery(req: Request, mapping: ResolvedOpenAIModelMapping): string {
  const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
  if (isOpenAIResponsesToChatCompletionsModelMapping(mapping)) {
    return `/chat/completions${query}`
  }
  if (isAnthropicMessagesToChatCompletionsModelMapping(mapping)) {
    return `/chat/completions${query}`
  }
  if (isGeminiGenerateContentToChatCompletionsModelMapping(mapping)) {
    return `/chat/completions${geminiGenerateContentToChatCompletionsQuery(req)}`
  }
  if (isOpenAIChatCompletionsToResponsesModelMapping(mapping)) {
    return `/responses${query}`
  }
  return req.originalUrl || req.path || '/'
}

export async function buildOpenAIModelMappedJsonBody(
  req: Request,
  upstreamModel: string,
  signal?: AbortSignal
): Promise<Buffer> {
  const body = await parseOpenAIModelMappingJsonObjectBody(req, signal)
  return Buffer.from(JSON.stringify({
    ...body,
    model: upstreamModel
  }), 'utf8')
}

async function parseOpenAIModelMappingJsonObjectBody(req: Request, signal?: AbortSignal): Promise<Record<string, unknown>> {
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
    throw modelMappingRequestError('账号模型映射要求请求体是有效的 JSON 对象')
  }

  const rawBody = requestWithBody.rawBody
  if (!rawBody || rawBody.length === 0) {
    throw modelMappingRequestError('账号模型映射要求请求体是 JSON 对象')
  }

  let parsed: unknown
  try {
    parsed = rawBody.length > gatewayJsonBodyInlineParseMaxBytes
      ? await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
      : JSON.parse(rawBody.toString('utf8')) as unknown
  } catch (error) {
    if (isGatewayJsonWorkerQueueFullError(error)) {
      throw new OpenAIOAuthCodexAdapterError('网关请求解析繁忙，请稍后重试', 'server_overloaded', {
        statusCode: 503,
        type: 'server_overloaded'
      })
    }
    throw modelMappingRequestError('账号模型映射要求请求体是有效的 JSON 对象')
  }

  if (!isPlainObject(parsed)) {
    throw modelMappingRequestError('账号模型映射要求请求体是 JSON 对象')
  }
  return { ...parsed }
}

function modelMappingRequestError(message: string): OpenAIOAuthCodexAdapterError {
  return new OpenAIOAuthCodexAdapterError(message, 'account_model_mapping_request_invalid', {
    statusCode: 400,
    type: 'invalid_request_error'
  })
}

function geminiGenerateContentToChatCompletionsQuery(req: Request): string {
  const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
  if (!query) return ''
  const params = new URLSearchParams(query.slice(1))
  params.delete('alt')
  params.delete('key')
  const text = params.toString()
  return text ? `?${text}` : ''
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isAccountModelMappingSourceEndpointFamily(value: GatewayRequestEndpointFamily): value is AccountModelMappingSourceEndpointFamily {
  return value === OPENAI_CHAT_COMPLETIONS_FAMILY
    || value === OPENAI_RESPONSES_FAMILY
    || value === ANTHROPIC_MESSAGES_FAMILY
    || value === GEMINI_GENERATE_CONTENT_FAMILY
    || value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
}
