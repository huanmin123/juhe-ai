import type { Request, Response } from 'express'

import { logger } from '../../shared/logger.js'
import type { GatewayApiKeyRow } from '../../storage/repositories.js'
import type { AuditCaptureContext } from './audit-capture.service.js'
import { sendGatewayFailureResponse } from './openai-gateway-failure-response.js'
import {
  imageGenerationDisabledCode,
  imageGenerationDisabledMessage,
  isImageGenerationDisabledForApiKey
} from './openai-gateway-image-permission.js'
import { downgradeGatewayAutoImageGenerationToolForPermission } from './openai-gateway-image-permission-downgrade.js'
import {
  gatewayTextRawBodyLimitBytes,
  getGatewayRequestBodyState,
  releaseGatewayRequestBodyInFlightBytes,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import {
  isOpenAIGatewayImageEndpointOrModelRequest,
  type OpenAIGatewayRequestLane
} from './openai-gateway-request-lane.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import type { GatewayFailureUsageContext } from './openai-gateway-usage-records.js'
import { sendInvalidJsonGatewayResponse } from './openai-gateway-local-request-errors.js'

export type OpenAIGatewayImagePermissionPreflightResult =
  | { outcome: 'continue'; requestLane: OpenAIGatewayRequestLane }
  | { outcome: 'completed' }

export async function applyOpenAIGatewayImagePermissionPreflight(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  apiKeyRecord?: GatewayApiKeyRow
  requestLane: OpenAIGatewayRequestLane
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
  endpoint: string
  gatewayTextRawBodyLimitMegabytes?: number
  signal?: AbortSignal
}): Promise<OpenAIGatewayImagePermissionPreflightResult> {
  if (!isImageGenerationDisabledForApiKey(input.apiKeyRecord, input.requestLane)) {
    return { outcome: 'continue', requestLane: input.requestLane }
  }

  const imageEndpointOrModel = isOpenAIGatewayImageEndpointOrModelRequest(input.req)
  if (!imageEndpointOrModel && rejectOversizedAutoImageGenerationTextDowngrade(input)) {
    return { outcome: 'completed' }
  }

  const downgrade = imageEndpointOrModel
    ? { downgraded: false, removedToolCount: 0, reason: 'image_endpoint_or_model' as const }
    : await downgradeGatewayAutoImageGenerationToolForPermission(input.req, input.signal)
  if (downgrade.downgraded) {
    logger.warn({
      event: 'gateway_image_generation_tool_downgraded',
      removedToolCount: downgrade.removedToolCount,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId,
      groupId: input.groupId
    }, '系统账户未开启图像生成，已移除 Responses auto 图像生成工具并按文本请求继续')
    input.auditCapture.addGatewayMetadata({
      label: 'system_account_image_generation_permission',
      metadata: {
        allowed: false,
        downgraded: true,
        removedToolCount: downgrade.removedToolCount,
        reason: downgrade.reason
      }
    })
    return { outcome: 'continue', requestLane: 'text' }
  }

  if (downgrade.reason === 'invalid_json') {
    sendInvalidJsonGatewayResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId,
      groupId: input.groupId,
      clientIp: input.clientIp,
      endpoint: input.endpoint
    })
    return { outcome: 'completed' }
  }

  if (downgrade.reason === 'json_worker_overloaded') {
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('网关请求解析繁忙，请稍后重试', 'server_overloaded', 'server_overloaded')
    sendGatewayFailureResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'request_validation',
        errorCode: 'server_overloaded',
        errorMessage: responsePayload.error.message
      }
    })
    return { outcome: 'completed' }
  }

  const statusCode = 403
  const responsePayload = gatewayErrorPayload(imageGenerationDisabledMessage, 'forbidden', imageGenerationDisabledCode)
  input.auditCapture.addGatewayMetadata({
    label: 'system_account_image_generation_permission',
    metadata: {
      allowed: false,
      downgraded: false,
      reason: downgrade.reason
    }
  })
  sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode,
    responsePayload,
    audit: {
      outcome: 'gateway_failed',
      errorPhase: 'authorization',
      errorCode: imageGenerationDisabledCode,
      errorMessage: responsePayload.error.message
    }
  })
  return { outcome: 'completed' }
}

function rejectOversizedAutoImageGenerationTextDowngrade(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  gatewayTextRawBodyLimitMegabytes?: number
}): boolean {
  const state = getGatewayRequestBodyState(input.req)
  const req = input.req as GatewayRawBodyRequest
  const rawBody = req.rawBody
  if (
    !rawBody
    || state?.jsonParseStatus !== 'deferred_large_json'
    || !state.imageGeneration
    || state.imageGenerationForced
  ) {
    return false
  }

  const textRawBodyLimitBytes = gatewayTextRawBodyLimitBytes(input.gatewayTextRawBodyLimitMegabytes)
  if (rawBody.length <= textRawBodyLimitBytes) {
    return false
  }

  logger.warn({
    event: 'gateway_image_generation_downgrade_text_body_too_large',
    rawBodyBytes: rawBody.length,
    textRawBodyLimitBytes,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId
  }, '系统账户未开启图像生成，auto image_generation 大请求降级后超过文本请求体上限，已拒绝')
  input.auditCapture.addGatewayMetadata({
    label: 'system_account_image_generation_permission',
    metadata: {
      allowed: false,
      downgraded: false,
      reason: 'auto_image_generation_text_body_too_large',
      rawBodyBytes: rawBody.length,
      textRawBodyLimitBytes
    }
  })

  req.rawBody = undefined
  req.body = undefined
  releaseGatewayRequestBodyInFlightBytes(req)

  const statusCode = 413
  const responsePayload = gatewayErrorPayload('请求体过大', 'request_too_large', 'request_body_too_large')
  sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode,
    responsePayload,
    audit: {
      outcome: 'gateway_failed',
      errorPhase: 'request_validation',
      errorCode: 'request_body_too_large',
      errorMessage: responsePayload.error.message
    }
  })
  return true
}
