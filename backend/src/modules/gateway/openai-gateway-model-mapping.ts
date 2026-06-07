import type { Request } from 'express'

import type { AccountModelMapping } from '../../domain/types.js'
import {
  getGatewayRequestBodyState,
  gatewayJsonBodyInlineParseMaxBytes,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayJsonBodyInWorker
} from './openai-gateway-json-parser.js'
import { requestModel } from './openai-gateway-usage.js'
import { OpenAIOAuthCodexAdapterError } from './openai-oauth-codex-adapter.js'

export interface OpenAIModelMappingRuntimeAccount {
  modelMappings?: AccountModelMapping[]
}

export interface ResolvedOpenAIModelMapping {
  sourceModel: string
  upstreamModel: string
}

export function resolveOpenAIAccountModelMapping(
  account: OpenAIModelMappingRuntimeAccount | undefined,
  requestedModel: string | undefined
): ResolvedOpenAIModelMapping | undefined {
  const model = requestedModel?.trim()
  if (!model) return undefined
  const mapping = (account?.modelMappings ?? []).find((item) => item.enabled !== false && item.sourceModel === model)
  if (!mapping || mapping.upstreamModel === mapping.sourceModel) return undefined
  return {
    sourceModel: mapping.sourceModel,
    upstreamModel: mapping.upstreamModel
  }
}

export function resolveOpenAIRequestModelMapping(
  req: Request,
  account: OpenAIModelMappingRuntimeAccount | undefined
): ResolvedOpenAIModelMapping | undefined {
  return resolveOpenAIAccountModelMapping(account, requestModel(req))
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

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
