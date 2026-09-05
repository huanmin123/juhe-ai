import type { Request, Response } from 'express'

import {
  observeGatewayHttpCompletion,
  responseHeadersToObject,
  type AuditCaptureContext
} from '../audit/capture.service.js'
import {
  gatewayErrorPayload,
  gatewayErrorPayloadForProtocol,
  localizedGatewayErrorPayload,
  sendGatewayErrorResponse,
  type GatewayErrorPayload
} from './responses.js'
import { buildGatewayErrorResponseSnapshot } from '../usage/snapshots.js'
import {
  recordGatewayFailure,
  type GatewayFailureUsageContext
} from '../usage/records.js'
import { gatewayProtocolClientErrorProtocolForRequest } from '../protocols/registry.js'
import type { UsageFailureAttribution } from '../../../storage/repositories.js'
import {
  getRequestLogger,
  markRequestHttpMetricFailureScope
} from '../../../shared/request-context.js'
import type { HttpMetricFailureScope } from '../../../shared/prometheus-metrics.js'
import { trackGatewayFailureUsageFinalization } from '../usage/failure-finalization.service.js'

interface SendGatewayFailureResponseInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  statusCode: number
  responsePayload: GatewayErrorPayload
  audit: {
    outcome: 'gateway_failed' | 'upstream_failed' | 'stream_failed'
    errorPhase: 'authorization' | 'quota' | 'dispatch' | 'request_validation' | 'security' | 'stream'
    errorCode?: string
    errorMessage?: string
  }
  recordUsage?: boolean
  usageErrorMessage?: string
  failureAttribution?: UsageFailureAttribution
  failureScope?: Exclude<HttpMetricFailureScope, 'none'>
  preserveUpstreamErrorMessage?: boolean
}

export async function sendGatewayFailureResponse(input: SendGatewayFailureResponseInput): Promise<void> {
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
    usageErrorMessage,
    preserveUpstreamErrorMessage = false
  } = input
  const protocol = gatewayErrorProtocolForRequest(req)
  const deliveredPayload = preserveUpstreamErrorMessage
    ? responsePayload
    : localizedGatewayErrorPayload(responsePayload, statusCode)
  const clientPayload = gatewayErrorPayloadForProtocol(deliveredPayload, protocol)
  const httpCompletion = observeGatewayHttpCompletion(res)
  const requestLogger = getRequestLogger()
  const failureScope = input.failureScope ?? inferGatewayFailureScope(audit.outcome, input.failureAttribution)
  if (failureScope === 'upstream') {
    markRequestHttpMetricFailureScope(failureScope)
  }

  sendGatewayErrorResponse(res, statusCode, deliveredPayload, { protocol, preserveUpstreamErrorMessage })
  auditCapture.finalize({
    outcome: audit.outcome,
    success: false,
    statusCode,
    responseHeaders: responseHeadersToObject(res),
    responseBody: JSON.stringify(clientPayload),
    responsePartType: 'gateway_error',
    errorPhase: audit.errorPhase,
    errorCode: audit.errorCode,
    errorMessage: audit.errorMessage ?? deliveredPayload.error.message
  })
  if (recordUsage) {
    const usageFinalization = httpCompletion.wait().then(async (completedAtMs) => {
      await recordGatewayFailure(req, usageContext, {
        statusCode,
        startedAt,
        completedAtMs,
        responsePayload: deliveredPayload,
        errorMessage: usageErrorMessage,
        failureAttribution: input.failureAttribution,
        responseSnapshot: buildGatewayErrorResponseSnapshot(statusCode, clientPayload)
      })
    }).catch((error) => {
      requestLogger.warn({
        event: 'gateway_failure_usage_finalize_failed',
        traceId: usageContext.traceId,
        statusCode,
        errorMessage: error instanceof Error ? error.message : String(error)
      }, '网关错误响应已返回客户端，但使用记录异步收尾失败')
    })
    trackGatewayFailureUsageFinalization(usageFinalization)
  }
}

function inferGatewayFailureScope(
  outcome: SendGatewayFailureResponseInput['audit']['outcome'],
  attribution: UsageFailureAttribution | undefined
): Exclude<HttpMetricFailureScope, 'none'> | undefined {
  if (outcome === 'upstream_failed') return 'upstream'
  return attribution === 'account_upstream'
    || attribution === 'account_dependency'
    || attribution === 'opaque_upstream'
    ? 'upstream'
    : undefined
}

function gatewayErrorProtocolForRequest(req: Request) {
  return gatewayProtocolClientErrorProtocolForRequest(req)
}

export async function sendQuotaExceededResponse(
  req: Request,
  res: Response,
  auditCapture: AuditCaptureContext,
  usageContext: GatewayFailureUsageContext,
  startedAt: number,
  message: string
): Promise<void> {
  const statusCode = 429
  const responsePayload = gatewayErrorPayload(message, 'rate_limit_exceeded')
  await sendGatewayFailureResponse({
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
