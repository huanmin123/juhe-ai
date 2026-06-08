import type { Request, Response } from 'express'

import type { GatewayApiKeyRow, GroupUsageAccessMetadata } from '../../storage/repositories.js'
import { isGatewayApiKeyScheduleInactive } from '../../storage/gateway-api-key.repository.js'
import { API_KEY_QUOTA_EXCEEDED_MESSAGE, checkGatewayApiKeyQuotaAsync } from './api-key-quota.service.js'
import {
  AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE,
  checkGatewayAuthorizationQuotaAsync
} from './authorization-quota.service.js'
import type { AuditCaptureContext } from './audit-capture.service.js'
import { sendGatewayFailureResponse, sendQuotaExceededResponse } from './openai-gateway-failure-response.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import type { GatewayFailureUsageContext } from './openai-gateway-usage-records.js'

export function rejectUnavailableGatewayApiKey(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  apiKeyUnavailable: boolean
}): boolean {
  if (!input.apiKeyUnavailable) return false
  const statusCode = 401
  const responsePayload = gatewayErrorPayload('API Key 不可用或已过期', 'invalid_api_key')
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
      errorCode: 'invalid_api_key',
      errorMessage: responsePayload.error.message
    }
  })
  return true
}

export function rejectInactiveGatewayApiKeySchedule(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  apiKeyRecord?: GatewayApiKeyRow
}): boolean {
  if (!isGatewayApiKeyScheduleInactive(input.apiKeyRecord)) return false
  const statusCode = 403
  const responsePayload = gatewayErrorPayload('API Key 当前不在允许使用时段', 'forbidden', 'api_key_schedule_inactive')
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
      errorCode: 'api_key_schedule_inactive',
      errorMessage: responsePayload.error.message
    }
  })
  return true
}

export function rejectMissingGatewayGroupAccess(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  groupAccess?: GroupUsageAccessMetadata
}): boolean {
  if (input.groupAccess) return false
  const statusCode = 403
  const responsePayload = gatewayErrorPayload('API Key 绑定的分组授权不可用', 'forbidden')
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
      errorCode: 'forbidden',
      errorMessage: 'API Key 绑定的分组授权不可用'
    }
  })
  return true
}

export async function rejectGatewayApiKeyQuotaIfExceeded(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  apiKeyRecord?: GatewayApiKeyRow
}): Promise<boolean> {
  const quotaDecision = input.apiKeyRecord ? await checkGatewayApiKeyQuotaAsync(input.apiKeyRecord) : { allowed: true }
  if (quotaDecision.allowed) return false
  const statusCode = 429
  const responsePayload = gatewayErrorPayload(quotaDecision.message ?? API_KEY_QUOTA_EXCEEDED_MESSAGE, 'rate_limit_exceeded')
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
      errorPhase: 'quota',
      errorCode: 'rate_limit_exceeded',
      errorMessage: responsePayload.error.message
    }
  })
  return true
}

export async function rejectGatewayAuthorizationQuotaIfExceeded(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  groupAccess: GroupUsageAccessMetadata
}): Promise<boolean> {
  const groupAuthorizationQuotaDecision = await checkGatewayAuthorizationQuotaAsync({ groupAccess: input.groupAccess })
  if (groupAuthorizationQuotaDecision.allowed) return false
  sendQuotaExceededResponse(
    input.req,
    input.res,
    input.auditCapture,
    input.usageContext,
    input.startedAt,
    groupAuthorizationQuotaDecision.message ?? AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE
  )
  return true
}
