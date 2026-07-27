import type { Request } from 'express'

import { logger } from '../../../shared/logger.js'
import {
  createGatewayRequestBodyState,
  downgradeGatewayAutoImageGenerationTool,
  getGatewayRequestBodyState,
  type GatewayImageGenerationToolDowngradeResult,
  type GatewayRawBodyRequest
} from './body.js'
import {
  isGatewayJsonWorkerQueueFullError,
  parseGatewayRequestJsonBody
} from './json-parser.js'

export async function downgradeGatewayAutoImageGenerationToolForPermission(
  req: Request,
  signal?: AbortSignal
): Promise<GatewayImageGenerationToolDowngradeResult> {
  const directDowngrade = downgradeGatewayAutoImageGenerationTool(req)
  if (directDowngrade.reason !== 'not_json_object' || !shouldParseLargeJsonForImageToolDowngrade(req)) {
    return directDowngrade
  }

  const request = req as GatewayRawBodyRequest
  const rawBody = request.rawBody
  if (!rawBody || rawBody.length === 0) {
    return directDowngrade
  }

  try {
    const parsedBody = await parseGatewayRequestJsonBody(req, undefined, signal)
    const previousState = getGatewayRequestBodyState(req)
    request.gatewayRequestBody = createGatewayRequestBodyState({
      rawBody,
      contentType: previousState?.contentType ?? req.headers['content-type'] ?? 'application/json',
      jsonParseStatus: previousState?.jsonParseStatus ?? 'parsed',
      parsedBody
    })
    logger.warn({
      event: 'gateway_auto_image_generation_tool_parse_for_downgrade',
      rawBodyBytes: rawBody.length,
      jsonParseStatus: request.gatewayRequestBody.jsonParseStatus
    }, '系统账户未开启图像生成，大 JSON 请求按需完整解析以移除 optional image_generation 工具')
    return downgradeGatewayAutoImageGenerationTool(req)
  } catch (error) {
    if (isGatewayJsonWorkerQueueFullError(error)) {
      return { downgraded: false, removedToolCount: 0, reason: 'json_worker_overloaded' }
    }
    markGatewayJsonBodyInvalid(req)
    return { downgraded: false, removedToolCount: 0, reason: 'invalid_json' }
  }
}

function shouldParseLargeJsonForImageToolDowngrade(req: Request): boolean {
  const request = req as GatewayRawBodyRequest
  const state = getGatewayRequestBodyState(req)
  return Boolean(
    request.rawBody
    && request.rawBody.length > 0
    && state?.jsonParseStatus === 'deferred_large_json'
    && state.imageGeneration
    && !state.imageGenerationForced
  )
}

function markGatewayJsonBodyInvalid(req: Request): void {
  const request = req as GatewayRawBodyRequest
  const previousState = getGatewayRequestBodyState(req)
  const rawBody = request.rawBody ?? Buffer.alloc(0)
  request.gatewayParsedJsonBodyAvailable = false
  request.gatewayParsedJsonBody = undefined
  request.gatewayParsedJsonBodyPromise = undefined
  request.gatewayUpstreamBodyCache = undefined
  request.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody,
    contentType: previousState?.contentType ?? req.headers['content-type'] ?? 'application/json',
    jsonParseStatus: 'invalid_json',
    model: previousState?.model,
    stream: previousState?.stream,
    serviceTier: previousState?.serviceTier,
    reasoningEffort: previousState?.reasoningEffort,
    imageGeneration: previousState?.imageGeneration,
    imageGenerationForced: previousState?.imageGenerationForced
  })
}
