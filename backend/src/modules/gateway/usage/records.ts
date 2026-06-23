import type { Request } from 'express'

import type {
  GroupUsageAccessMetadata,
  OpenAIAccountSecret
} from '../../../storage/repositories.js'
import { getRequestLogger, sanitizeUrlCredentialsForLog } from '../../../shared/request-context.js'
import { enqueueUsageRecord } from './record-queue.service.js'
import {
  estimateCatalogCacheReadCostUsd,
  estimateCatalogCacheWriteCostUsd,
  estimateCatalogCostUsd,
  resolveCatalogPricingModel
} from '../../model-pricing/model-catalog.service.js'
import {
  buildGatewayErrorResponseSnapshot,
  buildUsageRequestSnapshot,
  buildUsageResponseSnapshot,
  type UsageRequestSnapshot
} from './snapshots.js'
import {
  emptyUsage,
  type ParsedUsage
} from './types.js'
import {
  requestModel,
  requestStream
} from '../request/metadata.js'
import type { GatewayErrorPayload } from '../response/responses.js'
import { downstreamConnectionClosedMessage } from '../response/client-abort.js'
import type { OpenAIGatewayTrafficSource } from './traffic-source.js'
import { recordGatewayAccountApiKeySuccess } from '../runtime/account-api-key-effects.service.js'
import {
  defaultGatewayUsageProviderCode,
  resolveGatewayUsageModel,
  usageSemanticForProfile
} from '../../providers/drivers/registry.js'
import { parseGatewayProtocolErrorPayload } from '../protocols/registry.js'
import { openAIRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'

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
  providerCode?: string
  providerProtocolProfileId?: string
  protocolCode?: string
  protocolVersion?: string
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

export function groupUsageMetadata(groupAccess: GroupUsageAccessMetadata): Pick<GatewayFailureUsageContext, 'providerCode' | 'providerProtocolProfileId' | 'protocolCode' | 'protocolVersion' | 'groupOwnerSystemAccountId' | 'groupAccessType' | 'groupAuthorizationId' | 'groupAuthorizationSourceType' | 'groupAuthorizationSourceTeamId'> {
  return {
    providerCode: groupAccess.providerCode,
    providerProtocolProfileId: groupAccess.providerProtocolProfileId,
    protocolCode: groupAccess.protocolCode,
    protocolVersion: groupAccess.protocolVersion,
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
  const model = requestModel(req)
  const catalogSystemAccountId = account.accountOwnerSystemAccountId || usageContext.systemAccountId
  const modelAccounting = accountUsageModelAccounting(account, model, catalogSystemAccountId, openAIRequestEndpointFamily(req))
  const errorPayload = input.bodyText && input.headers instanceof Headers
    ? parseGatewayProtocolErrorPayload(account, input.bodyText, input.headers)
    : {}
  const errorCode = sanitizeOptionalDiagnosticMessage(typeof errorPayload.code === 'string' ? errorPayload.code : undefined)
  const errorMessage = input.errorMessage
    ?? (typeof errorPayload.message === 'string' ? errorPayload.message : undefined)
    ?? (typeof input.statusCode === 'number' ? `上游返回 HTTP ${input.statusCode}` : '上游请求失败')

  logGatewayAttemptFailure(usageContext, {
    event: 'gateway_upstream_attempt_failed',
    upstreamUrl: sanitizeUrlCredentialsForLog(input.upstreamUrl) ?? 'unknown',
    accountId: account.id,
    accountName: account.name,
    statusCode: input.statusCode,
    durationMs: Date.now() - input.startedAt,
    errorCode,
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
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    usageSemantic: usageSemanticForProfile(account),
    model,
    upstreamModel: modelAccounting.upstreamModel,
    pricingModel: modelAccounting.pricingModel,
    modelMappingApplied: modelAccounting.modelMappingApplied,
    modelMappingSource: modelAccounting.modelMappingSource,
    stream: requestStream(req),
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorCode,
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
  if (input.success) {
    recordGatewayAccountApiKeySuccess(input.account, 'upstream_attempt_completed')
  }
  const model = requestModel(req)
  const catalogSystemAccountId = input.account.accountOwnerSystemAccountId || input.systemAccountId
  const modelAccounting = accountUsageModelAccounting(input.account, model, catalogSystemAccountId, openAIRequestEndpointFamily(req))
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
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.account.providerProtocolProfileId,
    usageSemantic: usageSemanticForProfile(input.account),
    model,
    upstreamModel: modelAccounting.upstreamModel,
    pricingModel: modelAccounting.pricingModel,
    modelMappingApplied: modelAccounting.modelMappingApplied,
    modelMappingSource: modelAccounting.modelMappingSource,
    stream: input.stream,
    statusCode: input.statusCode,
    success: input.success,
    firstTokenMs: input.firstTokenMs,
    durationMs: Date.now() - input.startedAt,
    inputTokens: input.usage.inputTokens,
    outputTokens: input.usage.outputTokens,
    cacheReadTokens: input.usage.cacheReadTokens,
    cacheWriteTokens: input.usage.cacheWriteTokens,
    cacheWrite1hTokens: input.usage.cacheWrite1hTokens,
    thinkingTokens: input.usage.thinkingTokens,
    inputImageTokens: input.usage.inputImageTokens,
    outputImageTokens: input.usage.outputImageTokens,
    cacheReadCostUsd: estimateCatalogCacheReadCostUsd({
      providerCode: input.account.providerCode,
      systemAccountId: catalogSystemAccountId,
      model: modelAccounting.upstreamModel,
      cacheReadTokens: input.usage.cacheReadTokens
    }),
    cacheWriteCostUsd: estimateCatalogCacheWriteCostUsd({
      providerCode: input.account.providerCode,
      systemAccountId: catalogSystemAccountId,
      model: modelAccounting.upstreamModel,
      cacheWriteTokens: input.usage.cacheWriteTokens,
      cacheWrite1hTokens: input.usage.cacheWrite1hTokens
    }),
    costUsd: estimateCatalogCostUsd({
      providerCode: input.account.providerCode,
      systemAccountId: catalogSystemAccountId,
      model: modelAccounting.upstreamModel,
      inputTokens: input.usage.inputTokens,
      outputTokens: input.usage.outputTokens,
      cacheReadTokens: input.usage.cacheReadTokens,
      cacheWriteTokens: input.usage.cacheWriteTokens,
      cacheWrite1hTokens: input.usage.cacheWrite1hTokens,
      thinkingTokens: input.usage.thinkingTokens,
      inputImageTokens: input.usage.inputImageTokens,
      outputImageTokens: input.usage.outputImageTokens,
      inputAudioTokens: input.usage.inputAudioTokens,
      outputAudioTokens: input.usage.outputAudioTokens,
      outputImageCount: input.usage.outputImageCount
    }),
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    requestSnapshot: usageRecordSnapshot(input, input.requestSnapshot),
    responseSnapshot: usageRecordSnapshot(input, input.responseSnapshot)
  })
}

export function recordHybridScoringAttempt(input: {
  traceId: string
  clientIp?: string
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  account: UpstreamAccount
  endpoint: string
  statusCode?: number
  success: boolean
  startedAt: number
  scoringModel: string
  usage: ParsedUsage
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: unknown
  responseSnapshot?: unknown
  trafficSource?: Extract<OpenAIGatewayTrafficSource, 'hybrid_scoring' | 'hybrid_quality_scoring'>
}): void {
  const catalogSystemAccountId = input.account.accountOwnerSystemAccountId || input.systemAccountId
  const modelAccounting = accountUsageModelAccounting(input.account, input.scoringModel, catalogSystemAccountId, 'chat_completions')
  enqueueUsageRecord({
    traceId: input.traceId,
    trafficSource: input.trafficSource ?? 'hybrid_scoring',
    clientIp: input.clientIp,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    accountId: input.account.id,
    ...accountUsageMetadata(input.account),
    endpoint: input.endpoint,
    providerCode: input.account.providerCode,
    providerProtocolProfileId: input.account.providerProtocolProfileId,
    usageSemantic: usageSemanticForProfile(input.account),
    model: input.scoringModel,
    upstreamModel: modelAccounting.upstreamModel,
    pricingModel: modelAccounting.pricingModel,
    modelMappingApplied: modelAccounting.modelMappingApplied,
    modelMappingSource: modelAccounting.modelMappingSource,
    stream: false,
    statusCode: input.statusCode,
    success: input.success,
    durationMs: Date.now() - input.startedAt,
    inputTokens: input.usage.inputTokens,
    outputTokens: input.usage.outputTokens,
    cacheReadTokens: input.usage.cacheReadTokens,
    cacheWriteTokens: input.usage.cacheWriteTokens,
    cacheWrite1hTokens: input.usage.cacheWrite1hTokens,
    thinkingTokens: input.usage.thinkingTokens,
    inputImageTokens: input.usage.inputImageTokens,
    outputImageTokens: input.usage.outputImageTokens,
    cacheReadCostUsd: estimateCatalogCacheReadCostUsd({
      providerCode: input.account.providerCode,
      systemAccountId: catalogSystemAccountId,
      model: modelAccounting.upstreamModel,
      cacheReadTokens: input.usage.cacheReadTokens
    }),
    cacheWriteCostUsd: estimateCatalogCacheWriteCostUsd({
      providerCode: input.account.providerCode,
      systemAccountId: catalogSystemAccountId,
      model: modelAccounting.upstreamModel,
      cacheWriteTokens: input.usage.cacheWriteTokens,
      cacheWrite1hTokens: input.usage.cacheWrite1hTokens
    }),
    costUsd: estimateCatalogCostUsd({
      providerCode: input.account.providerCode,
      systemAccountId: catalogSystemAccountId,
      model: modelAccounting.upstreamModel,
      inputTokens: input.usage.inputTokens,
      outputTokens: input.usage.outputTokens,
      cacheReadTokens: input.usage.cacheReadTokens,
      cacheWriteTokens: input.usage.cacheWriteTokens,
      cacheWrite1hTokens: input.usage.cacheWrite1hTokens,
      thinkingTokens: input.usage.thinkingTokens,
      inputImageTokens: input.usage.inputImageTokens,
      outputImageTokens: input.usage.outputImageTokens,
      inputAudioTokens: input.usage.inputAudioTokens,
      outputAudioTokens: input.usage.outputAudioTokens,
      outputImageCount: input.usage.outputImageCount
    }),
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    requestSnapshot: input.requestSnapshot,
    responseSnapshot: input.responseSnapshot
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
    errorMessage: downstreamConnectionClosedMessage
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
    errorCode?: string
    responseSnapshot?: ReturnType<typeof buildUsageResponseSnapshot>
  }
): void {
  const errorMessage = input.errorMessage ?? input.responsePayload.error.message
  const errorCode = input.errorCode
    ?? (typeof input.responsePayload.error.code === 'string' ? input.responsePayload.error.code : undefined)
    ?? (typeof input.responsePayload.error.type === 'string' ? input.responsePayload.error.type : undefined)
  logGatewayAttemptFailure(usageContext, {
    event: 'gateway_request_failed',
    statusCode: input.statusCode,
    durationMs: Date.now() - input.startedAt,
    errorMessage,
    errorCode,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    endpoint: usageContext.endpoint
  }, '网关请求失败')
  const providerCode = usageContext.providerCode ?? defaultGatewayUsageProviderCode()
  const providerProtocolProfileId = usageContext.providerProtocolProfileId

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
    providerCode,
    providerProtocolProfileId,
    usageSemantic: usageSemanticForProfile({
      providerCode,
      providerProtocolProfileId,
      protocolCode: usageContext.protocolCode,
      protocolVersion: usageContext.protocolVersion
    }),
    model: requestModel(req),
    stream: requestStream(req),
    statusCode: input.statusCode,
    success: false,
    durationMs: Date.now() - input.startedAt,
    errorCode,
    errorMessage,
    requestSnapshot: usageRecordSnapshot(usageContext, usageContext.requestSnapshot),
    responseSnapshot: usageRecordSnapshot(
      usageContext,
      input.responseSnapshot ?? buildGatewayErrorResponseSnapshot(input.statusCode, input.responsePayload)
    )
  })
}

function usageRecordSnapshot(
  usageContext: Pick<GatewayUsageContext, 'trafficSource'>,
  snapshot: unknown
): unknown {
  return usageContext.trafficSource === 'cooldown_retest' ? undefined : snapshot
}

function sanitizeOptionalDiagnosticMessage(value: string | undefined): string | undefined {
  return value
}

function accountUsageModelAccounting(
  account: UpstreamAccount,
  requestedModel: string | undefined,
  catalogSystemAccountId: string,
  sourceEndpointFamily: ReturnType<typeof openAIRequestEndpointFamily>
): {
  upstreamModel?: string
  pricingModel?: string
  modelMappingApplied: boolean
  modelMappingSource?: string
} {
  const resolved = resolveGatewayUsageModel(account, requestedModel, sourceEndpointFamily)
  const upstreamModel = resolved.upstreamModel ?? requestedModel
  return {
    upstreamModel,
    pricingModel: resolveCatalogPricingModel({
      providerCode: account.providerCode,
      systemAccountId: catalogSystemAccountId,
      model: upstreamModel
    }),
    modelMappingApplied: resolved.modelMappingApplied,
    modelMappingSource: resolved.modelMappingSource
  }
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
