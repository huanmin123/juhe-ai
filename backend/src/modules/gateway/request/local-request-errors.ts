import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import {
  recordClientIpErrorCircuitSampleAsync,
  type GatewayClientIpErrorCircuitReason
} from '../runtime/client-ip-error-circuit.service.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import { gatewayErrorPayload } from '../response/responses.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'

export async function sendInvalidJsonGatewayResponse(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
  endpoint: string
}): Promise<void> {
  const statusCode = 400
  const responsePayload = gatewayErrorPayload('请求体不是合法 JSON', 'invalid_request_error')
  await recordClientIpRequestErrorSample({
    auditCapture: input.auditCapture,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    clientIp: input.clientIp,
    endpoint: input.endpoint,
    reason: 'invalid_json',
    signature: 'invalid_json'
  })
  await sendGatewayFailureResponse({
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
      errorCode: 'invalid_json',
      errorMessage: responsePayload.error.message
    }
  })
}

export async function recordClientIpRequestErrorSample(input: {
  auditCapture: AuditCaptureContext
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
  endpoint: string
  reason: GatewayClientIpErrorCircuitReason
  signature?: string
}): Promise<void> {
  const result = await recordClientIpErrorCircuitSampleAsync(input)
  if (!result.blocked) {
    return
  }
  logger.warn({
    event: 'gateway_client_ip_error_circuit_opened',
    reason: input.reason,
    retryAfterSeconds: result.retryAfterSeconds,
    failureCount: result.failureCount,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    clientIp: input.clientIp
  }, '客户端 IP 级错误熔断已打开')
  input.auditCapture.addGatewayMetadata({
    label: 'client_ip_error_circuit',
    metadata: {
      opened: true,
      reason: input.reason,
      retryAfterSeconds: result.retryAfterSeconds,
      failureCount: result.failureCount
    }
  })
}
