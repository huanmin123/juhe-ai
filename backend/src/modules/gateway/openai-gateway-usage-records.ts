import type { Request } from 'express'

import type {
  GroupUsageAccessMetadata,
  OpenAIAccountSecret
} from '../../storage/repositories.js'
import { getRequestLogger } from '../../shared/request-context.js'
import { parseErrorPayload } from './account-error-policy.service.js'
import { enqueueUsageRecord } from './usage-record-queue.service.js'
import {
  estimateProviderCacheReadCostUsd,
  estimateProviderCostUsd
} from '../model-pricing/model-pricing.service.js'
import {
  buildGatewayErrorResponseSnapshot,
  buildUsageRequestSnapshot,
  buildUsageResponseSnapshot,
  emptyUsage,
  requestModel,
  requestStream,
  type ParsedUsage,
  type UsageRequestSnapshot
} from './openai-gateway-usage.js'
import type { GatewayErrorPayload } from './openai-gateway-responses.js'
import type { OpenAIGatewayTrafficSource } from './openai-gateway-traffic-source.js'

type UpstreamAccount = OpenAIAccountSecret

export type UsageAccessFields = Pick<OpenAIAccountSecret,
  'accountOwnerSystemAccountId'
  | 'groupOwnerSystemAccountId'
  | 'accountAccessType'
  | 'groupAccessType'
  | 'accountAuthorizationId'
  | 'accountAuthorizationSourceType'
  | 'accountAuthorizationSourceTeamId'
  | 'groupAuthorizationId'
  | 'groupAuthorizationSourceType'
  | 'groupAuthorizationSourceTeamId'
>

export interface GatewayUsageContext {
  traceId: string
  trafficSource: OpenAIGatewayTrafficSource
  clientIp?: string
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
}

export interface GatewayFailureUsageContext extends GatewayUsageContext {
  groupOwnerSystemAccountId?: string
  groupAccessType?: GroupUsageAccessMetadata['groupAccessType']
  groupAuthorizationId?: string
  groupAuthorizationSourceType?: GroupUsageAccessMetadata['groupAuthorizationSourceType']
  groupAuthorizationSourceTeamId?: string
}

export function accountUsageMetadata(account: UpstreamAccount): UsageAccessFields {
  return {
    accountOwnerSystemAccountId: account.accountOwnerSystemAccountId,
    groupOwnerSystemAccountId: account.groupOwnerSystemAccountId,
    accountAccessType: account.accountAccessType,
    groupAccessType: account.groupAccessType,
    accountAuthorizationId: account.accountAuthorizationId,
    accountAuthorizationSourceType: account.accountAuthorizationSourceType,
    accountAuthorizationSourceTeamId: account.accountAuthorizationSourceTeamId,
    groupAuthorizationId: account.groupAuthorizationId,
    groupAuthorizationSourceType: account.groupAuthorizationSourceType,
    groupAuthorizationSourceTeamId: account.groupAuthorizationSourceTeamId
  }
}

export function groupUsageMetadata(groupAccess: GroupUsageAccessMetadata): Pick<UsageAccessFields, 'groupOwnerSystemAccountId' | 'groupAccessType' | 'groupAuthorizationId' | 'groupAuthorizationSourceType' | 'groupAuthorizationSourceTeamId'> {
  return {
    groupOwnerSystemAccountId: groupAccess.groupOwnerSystemAccountId,
    groupAccessType: groupAccess.groupAccessType,
    groupAuthorizationId: groupAccess.groupAuthorizationId,
    groupAuthorizationSourceType: groupAccess.groupAuthorizationSourceType,
    groupAuthorizationSourceTeamId: groupAccess.groupAuthorizationSourceTeamId
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
    ?? (typeof input.statusCode === 'number' ? `上游返回 HTTP ${input.statusCode}` : '上游请求失败')

  logGatewayAttemptFailure(usageContext, {
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
  }, '网关上游尝试失败')

  enqueueUsageRecord({
    traceId: usageContext.traceId,
    trafficSource: usageContext.trafficSource,
    clientIp: usageContext.clientIp,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    accountId: account.id,
    ...accountUsageMetadata(account),
    endpoint: usageContext.endpoint,
    providerCode: 'openai',
    model: requestModel(req),
    stream: requestStream(req),
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorCode: typeof errorPayload.code === 'string' ? errorPayload.code : undefined,
    errorMessage,
    requestSnapshot: usageRecordSnapshot(usageContext, usageContext.requestSnapshot),
    responseSnapshot: usageRecordSnapshot(usageContext, buildUsageResponseSnapshot({
      upstreamUrl: input.upstreamUrl,
      statusCode: input.statusCode,
      headers: input.headers,
      bodyText: input.bodyText,
      errorMessage
    }))
  })
}

export function recordCompletedUpstreamAttempt(
  req: Request,
  input: {
    traceId: string
    trafficSource: OpenAIGatewayTrafficSource
    clientIp?: string
    systemAccountId: string
    apiKeyId?: string
    groupId: string
    account: UpstreamAccount
    endpoint: string
    statusCode?: number
    success: boolean
    stream: boolean
    firstTokenMs?: number
    startedAt: number
    usage: ParsedUsage
    errorCode?: string
    errorMessage?: string
    requestSnapshot?: ReturnType<typeof buildUsageRequestSnapshot>
    responseSnapshot?: ReturnType<typeof buildUsageResponseSnapshot>
  }
): void {
  const model = requestModel(req)
  enqueueUsageRecord({
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    clientIp: input.clientIp,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    accountId: input.account.id,
    ...accountUsageMetadata(input.account),
    endpoint: input.endpoint,
    providerCode: 'openai',
    model,
    stream: input.stream,
    statusCode: input.statusCode,
    success: input.success,
    firstTokenMs: input.firstTokenMs,
    durationMs: Date.now() - input.startedAt,
    inputTokens: input.usage.inputTokens,
    outputTokens: input.usage.outputTokens,
    cacheReadTokens: input.usage.cacheReadTokens,
    inputImageTokens: input.usage.inputImageTokens,
    outputImageTokens: input.usage.outputImageTokens,
    cacheReadCostUsd: estimateProviderCacheReadCostUsd({
      providerCode: 'openai',
      model,
      cacheReadTokens: input.usage.cacheReadTokens
    }),
    costUsd: estimateProviderCostUsd({
      providerCode: 'openai',
      model,
      inputTokens: input.usage.inputTokens,
      outputTokens: input.usage.outputTokens,
      cacheReadTokens: input.usage.cacheReadTokens,
      inputImageTokens: input.usage.inputImageTokens,
      outputImageTokens: input.usage.outputImageTokens
    }),
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    requestSnapshot: usageRecordSnapshot(input, input.requestSnapshot),
    responseSnapshot: usageRecordSnapshot(input, input.responseSnapshot)
  })
}

export function recordClientAbortedUpstreamAttempt(
  req: Request,
  input: {
    traceId: string
    trafficSource: OpenAIGatewayTrafficSource
    clientIp?: string
    systemAccountId: string
    apiKeyId?: string
    groupId: string
    account: UpstreamAccount
    endpoint: string
    statusCode?: number
    stream: boolean
    firstTokenMs?: number
    startedAt: number
    requestSnapshot?: ReturnType<typeof buildUsageRequestSnapshot>
    responseSnapshot?: ReturnType<typeof buildUsageResponseSnapshot>
  }
): void {
  recordCompletedUpstreamAttempt(req, {
    ...input,
    success: false,
    usage: emptyUsage(),
    errorCode: 'client_aborted',
    errorMessage: '请求已取消'
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
  logGatewayAttemptFailure(usageContext, {
    event: 'gateway_request_failed',
    statusCode: input.statusCode,
    durationMs: Date.now() - input.startedAt,
    errorMessage: input.errorMessage ?? input.responsePayload.error.message,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    endpoint: usageContext.endpoint
  }, '网关请求失败')

  enqueueUsageRecord({
    traceId: usageContext.traceId,
    trafficSource: usageContext.trafficSource,
    clientIp: usageContext.clientIp,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    groupOwnerSystemAccountId: usageContext.groupOwnerSystemAccountId,
    groupAccessType: usageContext.groupAccessType,
    groupAuthorizationId: usageContext.groupAuthorizationId,
    groupAuthorizationSourceType: usageContext.groupAuthorizationSourceType,
    groupAuthorizationSourceTeamId: usageContext.groupAuthorizationSourceTeamId,
    endpoint: usageContext.endpoint,
    providerCode: 'openai',
    model: requestModel(req),
    stream: requestStream(req),
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorMessage: input.errorMessage ?? input.responsePayload.error.message,
    requestSnapshot: usageRecordSnapshot(usageContext, usageContext.requestSnapshot),
    responseSnapshot: usageRecordSnapshot(usageContext, buildGatewayErrorResponseSnapshot(input.statusCode, input.responsePayload))
  })
}

function usageRecordSnapshot(
  usageContext: Pick<GatewayUsageContext, 'trafficSource'>,
  snapshot: unknown
): unknown {
  return usageContext.trafficSource === 'cooldown_retest' ? undefined : snapshot
}

function logGatewayAttemptFailure(
  usageContext: GatewayUsageContext,
  fields: Record<string, unknown>,
  message: string
): void {
  const logger = getRequestLogger()
  const enrichedFields = {
    ...fields,
    trafficSource: usageContext.trafficSource
  }
  if (usageContext.trafficSource === 'cooldown_retest') {
    logger.debug(enrichedFields, message)
    return
  }
  logger.warn(enrichedFields, message)
}
