import type { Request, Response } from 'express'

import { responseHeadersToObject, type AuditCaptureContext } from './audit-capture.service.js'
import { gatewayErrorPayload, sendGatewayErrorResponse, type GatewayErrorPayload } from './openai-gateway-responses.js'
import {
  recordGatewayFailure,
  type GatewayFailureUsageContext
} from './openai-gateway-usage-records.js'

interface SendGatewayFailureResponseInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  statusCode: number
  responsePayload: GatewayErrorPayload
  audit: {
    outcome: 'gateway_failed' | 'upstream_failed'
    errorPhase: 'authorization' | 'quota' | 'dispatch' | 'request_validation' | 'security'
    errorCode?: string
    errorMessage?: string
  }
  recordUsage?: boolean
  usageErrorMessage?: string
}

export function sendGatewayFailureResponse(input: SendGatewayFailureResponseInput): void {
  const {
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    statusCode,
    responsePayload,
    audit,
    recordUsage = true,
    usageErrorMessage
  } = input

  if (recordUsage) {
    recordGatewayFailure(req, usageContext, {
      statusCode,
      startedAt,
      responsePayload,
      errorMessage: usageErrorMessage
    })
  }
  sendGatewayErrorResponse(res, statusCode, responsePayload)
  auditCapture.finalize({
    outcome: audit.outcome,
    success: false,
    statusCode,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(responsePayload),
    responsePartType: 'gateway_error',
    errorPhase: audit.errorPhase,
    errorCode: audit.errorCode,
    errorMessage: audit.errorMessage ?? responsePayload.error.message
  })
}

export function sendQuotaExceededResponse(
  req: Request,
  res: Response,
  auditCapture: AuditCaptureContext,
  usageContext: GatewayFailureUsageContext,
  startedAt: number,
  message: string
): void {
  const statusCode = 429
  const responsePayload = gatewayErrorPayload(message, 'rate_limit_exceeded')
  sendGatewayFailureResponse({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    statusCode,
    responsePayload,
    audit: {
      outcome: 'gateway_failed',
      errorPhase: 'quota',
      errorCode: 'rate_limit_exceeded',
      errorMessage: responsePayload.error.message
    }
  })
}
