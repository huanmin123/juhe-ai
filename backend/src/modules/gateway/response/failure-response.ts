import type { Request, Response } from 'express'

import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import {
  gatewayErrorPayload,
  gatewayErrorPayloadForProtocol,
  sendGatewayErrorResponse,
  type GatewayErrorPayload
} from './responses.js'
import { buildGatewayErrorResponseSnapshot } from '../usage/snapshots.js'
import {
  recordGatewayFailure,
  type GatewayFailureUsageContext
} from '../usage/records.js'
import { gatewayProtocolClientErrorProtocolForRequest } from '../protocols/registry.js'

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
  const protocol = gatewayErrorProtocolForRequest(req)
  const clientPayload = gatewayErrorPayloadForProtocol(responsePayload, protocol)

  if (recordUsage) {
    recordGatewayFailure(req, usageContext, {
      statusCode,
      startedAt,
      responsePayload,
      errorMessage: usageErrorMessage,
      responseSnapshot: buildGatewayErrorResponseSnapshot(statusCode, clientPayload)
    })
  }
  sendGatewayErrorResponse(res, statusCode, responsePayload, { protocol })
  auditCapture.finalize({
    outcome: audit.outcome,
    success: false,
    statusCode,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(clientPayload),
    responsePartType: 'gateway_error',
    errorPhase: audit.errorPhase,
    errorCode: audit.errorCode,
    errorMessage: audit.errorMessage ?? responsePayload.error.message
  })
}

function gatewayErrorProtocolForRequest(req: Request) {
  return gatewayProtocolClientErrorProtocolForRequest(req)
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
