import type { Request } from 'express'

import type {
  GroupUsageAccessMetadata,
  OpenAIAccountSecret
} from '../../storage/repositories.js'
import { getRequestLogger } from '../../shared/request-context.js'
import { parseErrorPayload } from './account-error-policy.service.js'
import { enqueueUsageRecord } from './usage-record-queue.service.js'
import {
  buildGatewayErrorResponseSnapshot,
  buildUsageResponseSnapshot,
  requestModel,
  type UsageRequestSnapshot
} from './openai-gateway-usage.js'
import type { GatewayErrorPayload } from './openai-gateway-responses.js'

type UpstreamAccount = OpenAIAccountSecret

export type UsageAccessFields = Pick<OpenAIAccountSecret,
  'accountOwnerSystemAccountId'
  | 'groupOwnerSystemAccountId'
  | 'accountAccessType'
  | 'groupAccessType'
  | 'accountAuthorizationId'
  | 'groupAuthorizationId'
>

export interface GatewayUsageContext {
  traceId: string
  clientIp?: string
  systemAccountId: string
  apiKeyId: string
  groupId: string
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
}

export interface GatewayFailureUsageContext extends GatewayUsageContext {
  groupOwnerSystemAccountId?: string
  groupAccessType?: GroupUsageAccessMetadata['groupAccessType']
  groupAuthorizationId?: string
}

export function accountUsageMetadata(account: UpstreamAccount): UsageAccessFields {
  return {
    accountOwnerSystemAccountId: account.accountOwnerSystemAccountId,
    groupOwnerSystemAccountId: account.groupOwnerSystemAccountId,
    accountAccessType: account.accountAccessType,
    groupAccessType: account.groupAccessType,
    accountAuthorizationId: account.accountAuthorizationId,
    groupAuthorizationId: account.groupAuthorizationId
  }
}

export function groupUsageMetadata(groupAccess: GroupUsageAccessMetadata): Pick<UsageAccessFields, 'groupOwnerSystemAccountId' | 'groupAccessType' | 'groupAuthorizationId'> {
  return {
    groupOwnerSystemAccountId: groupAccess.groupOwnerSystemAccountId,
    groupAccessType: groupAccess.groupAccessType,
    groupAuthorizationId: groupAccess.groupAuthorizationId
  }
}

export function recordFailedUpstreamAttempt(
  req: Request,
  usageContext: GatewayUsageContext,
  account: UpstreamAccount,
  input: {
    upstreamUrl: string
    startedAt: number
    statusCode?: number
    headers?: Headers | Record<string, string>
    bodyText?: string
    errorMessage?: string
  }
): void {
  const errorPayload = input.bodyText && input.headers instanceof Headers
    ? parseErrorPayload(input.bodyText, input.headers)
    : {}
  const errorMessage = input.errorMessage
    ?? (typeof errorPayload.message === 'string' ? errorPayload.message : undefined)
    ?? (typeof input.statusCode === 'number' ? `Upstream returned HTTP ${input.statusCode}` : 'Upstream request failed')

  getRequestLogger().warn({
    event: 'gateway_upstream_attempt_failed',
    upstreamUrl: input.upstreamUrl,
    accountId: account.id,
    accountName: account.name,
    statusCode: input.statusCode,
    durationMs: Date.now() - input.startedAt,
    errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
    errorMessage,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    endpoint: usageContext.endpoint
  }, 'Gateway upstream attempt failed')

  enqueueUsageRecord({
    traceId: usageContext.traceId,
    clientIp: usageContext.clientIp,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    accountId: account.id,
    ...accountUsageMetadata(account),
    endpoint: usageContext.endpoint,
    providerCode: 'openai',
    model: requestModel(req),
    stream: req.body?.stream === true,
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
    errorMessage,
    requestSnapshot: usageContext.requestSnapshot,
    responseSnapshot: buildUsageResponseSnapshot({
      upstreamUrl: input.upstreamUrl,
      statusCode: input.statusCode,
      headers: input.headers,
      bodyText: input.bodyText,
      errorMessage
    })
  })
}

export function recordGatewayFailure(
  req: Request,
  usageContext: GatewayFailureUsageContext,
  input: {
    statusCode: number
    startedAt: number
    responsePayload: GatewayErrorPayload
    errorMessage?: string
  }
): void {
  getRequestLogger().warn({
    event: 'gateway_request_failed',
    statusCode: input.statusCode,
    durationMs: Date.now() - input.startedAt,
    errorMessage: input.errorMessage ?? input.responsePayload.error.message,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    endpoint: usageContext.endpoint
  }, 'Gateway request failed')

  enqueueUsageRecord({
    traceId: usageContext.traceId,
    clientIp: usageContext.clientIp,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    groupOwnerSystemAccountId: usageContext.groupOwnerSystemAccountId,
    groupAccessType: usageContext.groupAccessType,
    groupAuthorizationId: usageContext.groupAuthorizationId,
    endpoint: usageContext.endpoint,
    providerCode: 'openai',
    model: requestModel(req),
    stream: req.body?.stream === true,
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorMessage: input.errorMessage ?? input.responsePayload.error.message,
    requestSnapshot: usageContext.requestSnapshot,
    responseSnapshot: buildGatewayErrorResponseSnapshot(input.statusCode, input.responsePayload)
  })
}
