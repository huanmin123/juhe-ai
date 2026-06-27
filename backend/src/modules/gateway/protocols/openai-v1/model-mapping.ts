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
import { requestStream } from '../../request/metadata.js'
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
  runtimeSource?: AccountModelMapping['runtimeSource']
  runtimeRouteRuleId?: string
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
  if (!isOpenAIModelMappingRuntimeConversionSupported(mapping)) return undefined
  return {
    sourceModel: mapping.sourceModel,
    sourceEndpointFamily: mapping.sourceEndpointFamily,
    upstreamModel: mapping.upstreamModel,
    upstreamEndpointFamily: mapping.upstreamEndpointFamily,
    ...(mapping.runtimeSource ? { runtimeSource: mapping.runtimeSource } : {}),
    ...(mapping.runtimeRouteRuleId ? { runtimeRouteRuleId: mapping.runtimeRouteRuleId } : {})
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

export function isGeminiGenerateContentToAnthropicMessagesModelMapping(
  mapping: ResolvedOpenAIModelMapping | undefined
): boolean {
  return (
    mapping?.sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
    || mapping?.sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  ) && mapping.upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
}

export function isOpenAIOrAnthropicToGeminiGenerateContentModelMapping(
  mapping: ResolvedOpenAIModelMapping | undefined
): mapping is ResolvedOpenAIModelMapping {
  return (
    mapping?.sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
    || mapping?.sourceEndpointFamily === OPENAI_RESPONSES_FAMILY
    || mapping?.sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
  ) && mapping.upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
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
  return req.originalUrl || req.path || '/'
}

function isOpenAIModelMappingRuntimeConversionSupported(
  mapping: Pick<AccountModelMapping, 'sourceEndpointFamily' | 'upstreamEndpointFamily' | 'runtimeSource'>
): boolean {
  const { sourceEndpointFamily: source, upstreamEndpointFamily: upstream } = mapping
  if (mapping.runtimeSource !== 'explicit_hybrid_route') {
    return source === upstream
      || (source === GEMINI_STREAM_GENERATE_CONTENT_FAMILY && upstream === GEMINI_GENERATE_CONTENT_FAMILY)
  }
  if (source === upstream) {
    return true
  }
  if (source === GEMINI_STREAM_GENERATE_CONTENT_FAMILY && upstream === GEMINI_GENERATE_CONTENT_FAMILY) {
    return true
  }
  if (source === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return upstream === OPENAI_CHAT_COMPLETIONS_FAMILY
      || upstream === ANTHROPIC_MESSAGES_FAMILY
      || upstream === GEMINI_GENERATE_CONTENT_FAMILY
  }
  if (source === OPENAI_RESPONSES_FAMILY) {
    return upstream === OPENAI_CHAT_COMPLETIONS_FAMILY
      || upstream === OPENAI_RESPONSES_FAMILY
      || upstream === ANTHROPIC_MESSAGES_FAMILY
      || upstream === GEMINI_GENERATE_CONTENT_FAMILY
  }
  if (source === ANTHROPIC_MESSAGES_FAMILY) {
    return upstream === OPENAI_CHAT_COMPLETIONS_FAMILY
      || upstream === GEMINI_GENERATE_CONTENT_FAMILY
  }
  if (source === GEMINI_GENERATE_CONTENT_FAMILY || source === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return upstream === OPENAI_CHAT_COMPLETIONS_FAMILY
      || upstream === ANTHROPIC_MESSAGES_FAMILY
  }
  return false
}

export function geminiGenerateContentToAnthropicMessagesUpstreamPathAndQuery(req: Request): string {
  return `/messages${geminiGenerateContentBridgeQuery(req)}`
}

export function geminiGenerateContentModelMappedUpstreamPathAndQuery(req: Request, mapping: ResolvedOpenAIModelMapping): string {
  const model = geminiModelPathSegment(mapping.upstreamModel)
  const action = requestStream(req) ? 'streamGenerateContent' : 'generateContent'
  return `/v1beta/models/${model}:${action}${requestStream(req) ? '?alt=sse' : ''}`
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
  return geminiGenerateContentBridgeQuery(req)
}

function geminiGenerateContentBridgeQuery(req: Request): string {
  const { query } = splitPathAndQuery(req.originalUrl || req.path || '')
  if (!query) return ''
  const params = new URLSearchParams(query.slice(1))
  params.delete('alt')
  params.delete('key')
  const text = params.toString()
  return text ? `?${text}` : ''
}

function geminiModelPathSegment(model: string): string {
  const normalized = model.startsWith('models/') ? model.slice('models/'.length) : model
  return encodeURIComponent(normalized)
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
