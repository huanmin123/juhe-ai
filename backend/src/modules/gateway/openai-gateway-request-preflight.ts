import type { Request, Response } from 'express'

import { loadAccountCurrentConcurrencyByIds } from '../../shared/account-concurrency.js'
import { effectiveImageLaneConcurrencyLimit } from '../../domain/group-scheduling.js'
import { logger } from '../../shared/logger.js'
import { bindRequestContextFields } from '../../shared/request-context.js'
import { type GatewayApiKeyRow, type GroupUsageAccessMetadata } from '../../storage/repositories.js'
import { isGatewayApiKeyScheduleInactive } from '../../storage/gateway-api-key.repository.js'
import {
  listCachedOpenAIAccountsForGroupAsync,
  readCachedGatewaySettingsAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from './gateway-runtime-cache.service.js'
import { type GatewaySettings } from './account-error-policy.service.js'
import { API_KEY_QUOTA_EXCEEDED_MESSAGE, checkGatewayApiKeyQuotaAsync } from './api-key-quota.service.js'
import {
  AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE,
  checkGatewayAuthorizationQuotaAsync,
  checkGatewayAuthorizationQuotaBatchAsync
} from './authorization-quota.service.js'
import { type AuditCaptureContext } from './audit-capture.service.js'
import {
  filterLocallySuppressedGatewayAccounts,
  type LocalAccountSuppressionFilterResult
} from './gateway-account-side-effects.service.js'
import {
  createClientIpAccountAvoidanceTracker,
  orderOpenAIAccountsByClientIpAccountAvoidance,
  type ClientIpAccountAvoidanceTracker
} from './openai-gateway-client-ip-account-avoidance.service.js'
import {
  createGatewayRequestBodyState,
  downgradeGatewayAutoImageGenerationTool,
  getGatewayRequestBodyState,
  type GatewayImageGenerationToolDowngradeResult,
  type GatewayRawBodyRequest
} from './openai-gateway-request-body.js'
import { isGatewayJsonWorkerQueueFullError, parseGatewayJsonBodyInWorker } from './openai-gateway-json-parser.js'
import {
  isOpenAIGatewayImageEndpointOrModelRequest,
  type OpenAIGatewayRequestLane
} from './openai-gateway-request-lane.js'
import {
  orderOpenAIAccountsByCodexTurnAvoidance
} from './openai-gateway-codex-turn-retry.service.js'
import {
  filterGatewayAccountsByRequestCapability
} from './openai-gateway-account-capability-filter.js'
import {
  openAIGatewayClientStrategyAuditMetadata,
  resolveOpenAIGatewayClientStrategy,
  type OpenAIGatewayClientStrategyContext
} from './openai-gateway-client-strategy.js'
import { acquireHighConcurrencyClientIpSlot, type ClientIpConcurrencyDecision } from './openai-gateway-client-ip-concurrency.service.js'
import {
  inspectClientIpErrorCircuit,
  recordClientIpErrorCircuitSample,
  recordClientIpErrorCircuitSuccess,
  type GatewayClientIpErrorCircuitReason
} from './openai-gateway-client-ip-error-circuit.service.js'
import {
  orderOpenAIAccountsByGatewayUpstreamBucketHealth
} from './openai-gateway-proxy-health.service.js'
import { waitForHighConcurrencyGroupCapacity } from './openai-gateway-high-concurrency-queue.service.js'
import { finalizeGatewayAuthFailureAudit, sendOpenAIModelsGatewayResponse } from './openai-gateway-fixed-responses.js'
import { sendGatewayFailureResponse, sendQuotaExceededResponse } from './openai-gateway-failure-response.js'
import {
  imageGenerationDisabledCode,
  imageGenerationDisabledMessage,
  isImageGenerationDisabledForApiKey
} from './openai-gateway-image-permission.js'
import { filterGatewayAccountsByRequestedModel, gatewayModelFilterFailureMessage } from './openai-gateway-model-filter.js'
import { gatewayErrorPayload } from './openai-gateway-responses.js'
import { resolveGatewayRuntimeAsync } from './openai-gateway-request.js'
import { isOpenAIModelsRequest, type UpstreamAccount } from './openai-gateway-route-helpers.js'
import {
  areOpenAIHighConcurrencyAccountsBusyForLane,
  orderOpenAIAccountsBySessionAffinity,
  resolveOpenAIGatewaySessionAffinityKey
} from './openai-gateway-session-affinity.service.js'
import { requestModel, type UsageRequestSnapshot } from './openai-gateway-usage.js'
import {
  groupUsageMetadata,
  type GatewayFailureUsageContext
} from './openai-gateway-usage-records.js'
import type { OpenAIGatewayTrafficSource } from './openai-gateway-traffic-source.js'
import type { GroupSchedulingPolicy } from '../../domain/types.js'
import type { StreamInterceptPolicySummary } from '../../storage/stream-intercept-policy.repository.js'

export interface OpenAIGatewayRequestIdentity {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
}

interface OpenAIGatewayRequestPreflightOptions {
  identity?: OpenAIGatewayRequestIdentity
  apiKeyRecord?: GatewayApiKeyRow
  candidateAccounts?: UpstreamAccount[]
  streamInterceptPolicies?: StreamInterceptPolicySummary[]
  disableSessionAffinity?: boolean
  trafficSource?: OpenAIGatewayTrafficSource
  settingsOverride?: Partial<GatewaySettings>
  requestLane?: OpenAIGatewayRequestLane
}

interface PrepareOpenAIGatewayDispatchContextInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  options: OpenAIGatewayRequestPreflightOptions
  startedAt: number
  traceId: string
  clientIp?: string
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
  signal?: AbortSignal
}

export interface OpenAIGatewayDispatchContext {
  activeGatewaySettings: GatewaySettings
  usageContext: GatewayFailureUsageContext
  accounts: UpstreamAccount[]
  sessionAffinityKey?: string
  clientStrategy: OpenAIGatewayClientStrategyContext
  clientIpAccountAvoidanceTracker: ClientIpAccountAvoidanceTracker
  requestLane: OpenAIGatewayRequestLane
  groupSchedulingPolicy?: GroupSchedulingPolicy
  streamInterceptPolicies: StreamInterceptPolicySummary[]
  apiKeyRecord?: GatewayApiKeyRow
  releaseClientIpConcurrency: () => void
}

export async function prepareOpenAIGatewayDispatchContext(
  input: PrepareOpenAIGatewayDispatchContextInput
): Promise<OpenAIGatewayDispatchContext | undefined> {
  const { req, res, auditCapture, options, startedAt, traceId, clientIp, endpoint, requestSnapshot, signal } = input
  let gatewaySettings: GatewaySettings | undefined
  let apiKeyRecord: GatewayApiKeyRow | undefined = options.apiKeyRecord
  let runtimeGroupAccess: GroupUsageAccessMetadata | undefined
  let runtimeAccounts: UpstreamAccount[] | undefined
  let runtimeStreamInterceptPolicies: StreamInterceptPolicySummary[] | undefined = options.streamInterceptPolicies

  const identity = options.identity ?? await (async () => {
    const runtime = await resolveGatewayRuntimeAsync(req, res)
    if (!runtime?.apiKey) {
      finalizeGatewayAuthFailureAudit(req, res, auditCapture)
      return undefined
    }
    gatewaySettings = runtime.settings
    apiKeyRecord = runtime.apiKey
    runtimeGroupAccess = runtime.groupAccess
    runtimeAccounts = runtime.accounts
    runtimeStreamInterceptPolicies = runtime.streamInterceptPolicies
    options.streamInterceptPolicies = runtime.streamInterceptPolicies
    return {
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      groupId: runtime.apiKey.selected_group_id
    }
  })()
  if (!identity) {
    return undefined
  }

  const activeGatewaySettings = mergeGatewaySettings(
    gatewaySettings ?? await readCachedGatewaySettingsAsync(),
    options.settingsOverride
  )
  let requestLane = options.requestLane ?? 'text'
  const trafficSource = options.trafficSource ?? 'gateway'
  const gatewayClientIp = trafficSource === 'gateway' ? clientIp : undefined
  const { systemAccountId, apiKeyId, groupId } = identity
  const apiKeyUnavailable = Boolean(apiKeyId && !apiKeyRecord)
  auditCapture.bindContext({
    systemAccountId,
    apiKeyId,
    groupId,
    providerCode: 'openai',
    trafficSource
  })
  bindRequestContextFields({
    systemAccountId,
    apiKeyId,
    groupId,
    trafficSource
  })

  const baseUsageContext = buildGatewayUsageContext({
    traceId,
    clientIp,
    identity,
    trafficSource,
    endpoint,
    requestSnapshot
  })
  const clientIpErrorCircuit = inspectClientIpErrorCircuit({
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp,
    endpoint
  })
  if (clientIpErrorCircuit.blocked) {
    const statusCode = 429
    const responsePayload = gatewayErrorPayload('当前来源短时间错误过多，请稍后重试', 'rate_limit_exceeded', 'client_ip_error_circuit_open')
    if (clientIpErrorCircuit.retryAfterSeconds && !res.headersSent) {
      res.setHeader('Retry-After', String(clientIpErrorCircuit.retryAfterSeconds))
    }
    logger.warn({
      event: 'gateway_client_ip_error_circuit_blocked',
      reason: clientIpErrorCircuit.reason,
      retryAfterSeconds: clientIpErrorCircuit.retryAfterSeconds,
      failureCount: clientIpErrorCircuit.failureCount,
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp
    }, '客户端 IP 级错误熔断已短路请求')
    auditCapture.addGatewayMetadata({
      label: 'client_ip_error_circuit',
      metadata: {
        blocked: true,
        reason: clientIpErrorCircuit.reason,
        retryAfterSeconds: clientIpErrorCircuit.retryAfterSeconds,
        failureCount: clientIpErrorCircuit.failureCount
      }
    })
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'security',
        errorCode: 'client_ip_error_circuit_open',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }
  if (apiKeyUnavailable) {
    const statusCode = 401
    const responsePayload = gatewayErrorPayload('API Key 不可用或已过期', 'invalid_api_key')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'authorization',
        errorCode: 'invalid_api_key',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }
  if (isGatewayApiKeyScheduleInactive(apiKeyRecord)) {
    const statusCode = 403
    const responsePayload = gatewayErrorPayload('API Key 当前不在允许使用时段', 'forbidden', 'api_key_schedule_inactive')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'authorization',
        errorCode: 'api_key_schedule_inactive',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }
  const initialBodyState = getGatewayRequestBodyState(req)
  if (initialBodyState?.jsonParseStatus === 'invalid_json') {
    sendInvalidJsonGatewayResponse({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint
    })
    return undefined
  }
  if (isImageGenerationDisabledForApiKey(apiKeyRecord, requestLane)) {
    const downgrade = isOpenAIGatewayImageEndpointOrModelRequest(req)
      ? { downgraded: false, removedToolCount: 0, reason: 'image_endpoint_or_model' as const }
      : await downgradeGatewayAutoImageGenerationToolForPermission(req, signal)
    if (downgrade.downgraded) {
      requestLane = 'text'
      logger.warn({
        event: 'gateway_image_generation_tool_downgraded',
        removedToolCount: downgrade.removedToolCount,
        systemAccountId,
        apiKeyId,
        groupId
      }, '系统账户未开启图像生成，已移除 Responses auto 图像生成工具并按文本请求继续')
      auditCapture.addGatewayMetadata({
        label: 'system_account_image_generation_permission',
        metadata: {
          allowed: false,
          downgraded: true,
          removedToolCount: downgrade.removedToolCount,
          reason: downgrade.reason
        }
      })
    } else if (downgrade.reason === 'invalid_json') {
      sendInvalidJsonGatewayResponse({
        req,
        res,
        auditCapture,
        usageContext: baseUsageContext,
        startedAt,
        systemAccountId,
        apiKeyId,
        groupId,
        clientIp: gatewayClientIp,
        endpoint
      })
      return undefined
    } else if (downgrade.reason === 'json_worker_overloaded') {
      const statusCode = 503
      const responsePayload = gatewayErrorPayload('网关请求解析繁忙，请稍后重试', 'server_overloaded', 'server_overloaded')
      sendGatewayFailureResponse({
        req,
        res,
        auditCapture,
        usageContext: baseUsageContext,
        startedAt,
        statusCode,
        responsePayload,
        audit: {
          outcome: 'gateway_failed',
          errorPhase: 'request_validation',
          errorCode: 'server_overloaded',
          errorMessage: responsePayload.error.message
        }
      })
      return undefined
    } else {
      const statusCode = 403
      const responsePayload = gatewayErrorPayload(imageGenerationDisabledMessage, 'forbidden', imageGenerationDisabledCode)
      auditCapture.addGatewayMetadata({
        label: 'system_account_image_generation_permission',
        metadata: {
          allowed: false,
          downgraded: false,
          reason: downgrade.reason
        }
      })
      sendGatewayFailureResponse({
        req,
        res,
        auditCapture,
        usageContext: baseUsageContext,
        startedAt,
        statusCode,
        responsePayload,
        audit: {
          outcome: 'gateway_failed',
          errorPhase: 'authorization',
          errorCode: imageGenerationDisabledCode,
          errorMessage: responsePayload.error.message
        }
      })
      return undefined
    }
  }

  const groupAccess = runtimeGroupAccess ?? await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
  const clientStrategy = resolveOpenAIGatewayClientStrategy(req, {
    systemAccountId,
    apiKeyId,
    groupId,
    endpoint
  })
  const clientIpAccountAvoidanceTracker = createClientIpAccountAvoidanceTracker({
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp
  })
  if (clientStrategy.clientProfile === 'codex') {
    auditCapture.addGatewayMetadata({
      label: 'client_strategy',
      metadata: openAIGatewayClientStrategyAuditMetadata(clientStrategy)
    })
  }
  if (!groupAccess) {
    const statusCode = 403
    const responsePayload = gatewayErrorPayload('API Key 绑定的分组授权不可用', 'forbidden')
    sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'authorization',
        errorCode: 'forbidden',
        errorMessage: 'API Key 绑定的分组授权不可用'
      }
    })
    return undefined
  }
  const groupUsageFields = groupUsageMetadata(groupAccess)
  const usageContext = buildGatewayUsageContext({
    traceId,
    clientIp,
    identity,
    trafficSource,
    groupUsageFields,
    endpoint,
    requestSnapshot
  })

  const bodyState = getGatewayRequestBodyState(req)
  if (bodyState?.jsonParseStatus === 'invalid_json') {
    sendInvalidJsonGatewayResponse({
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint
    })
    return undefined
  }

  const quotaDecision = apiKeyRecord ? await checkGatewayApiKeyQuotaAsync(apiKeyRecord) : { allowed: true }
  if (!quotaDecision.allowed) {
    const statusCode = 429
    const responsePayload = gatewayErrorPayload(quotaDecision.message ?? API_KEY_QUOTA_EXCEEDED_MESSAGE, 'rate_limit_exceeded')
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
    return undefined
  }

  const groupAuthorizationQuotaDecision = await checkGatewayAuthorizationQuotaAsync({ groupAccess })
  if (!groupAuthorizationQuotaDecision.allowed) {
    sendQuotaExceededResponse(
      req,
      res,
      auditCapture,
      usageContext,
      startedAt,
      groupAuthorizationQuotaDecision.message ?? AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE
    )
    return undefined
  }

  if (isOpenAIModelsRequest(req)) {
    recordClientIpErrorCircuitSuccess({
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint
    })
    sendOpenAIModelsGatewayResponse({
      res,
      auditCapture,
      usageContext,
      startedAt
    })
    return undefined
  }

  const rawSessionAffinityKey = resolveOpenAIGatewaySessionAffinityKey(req, {
    systemAccountId,
    apiKeyId,
    groupId
  })
  const sessionAffinityKey = options.disableSessionAffinity ? undefined : rawSessionAffinityKey
  const rawCandidateAccounts = options.candidateAccounts ?? runtimeAccounts ?? await listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId)
  const dispatchOrderingOptions = {
    groupType: groupAccess.groupType,
    schedulingPolicy: groupAccess.schedulingPolicy
  }
  let releaseClientIpConcurrency = noop
  const capabilityFilter = filterGatewayAccountsByRequestCapability(req, rawCandidateAccounts)
  if (capabilityFilter.skippedCount > 0) {
    auditCapture.addGatewayMetadata({
      label: 'account_request_capability_filter',
      metadata: {
        skippedCount: capabilityFilter.skippedCount,
        remainingCount: capabilityFilter.accounts.length,
        reason: capabilityFilter.reason
      }
    })
  }
  if (rawCandidateAccounts.length > 0 && capabilityFilter.accounts.length === 0) {
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options,
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal,
      reason: 'request_capability_mismatch',
      apiKeyRecord,
      systemAccountId,
      apiKeyId,
      groupId,
      trafficSource,
      requestLane
    })
    if (fallback.attempted) {
      return fallback.context
    }
    const statusCode = 400
    const message = '当前分组无账户支持请求路径或客户端协议'
    const responsePayload = gatewayErrorPayload(message, 'invalid_request_error', 'request_capability_mismatch')
    recordClientIpRequestErrorSample({
      auditCapture,
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint,
      reason: 'request_capability_mismatch',
      signature: `${req.method.toUpperCase()} ${req.path || req.originalUrl.split('?')[0] || '/'}`
    })
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
        errorPhase: 'request_validation',
        errorCode: 'request_capability_mismatch',
        errorMessage: message
      }
    })
    return undefined
  }
  const modelFilter = filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, requestModel(req))
  if (modelFilter.skippedCount > 0) {
    auditCapture.addGatewayMetadata({
      label: 'account_model_filter',
      metadata: {
        requestedModel: modelFilter.requestedModel,
        skippedCount: modelFilter.skippedCount,
        limitedAccountCount: modelFilter.limitedAccountCount,
        remainingCount: modelFilter.accounts.length,
        reason: modelFilter.reason
      }
    })
  }
  if (capabilityFilter.accounts.length > 0 && modelFilter.accounts.length === 0) {
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options,
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal,
      reason: modelFilter.reason ?? 'unsupported_model',
      apiKeyRecord,
      systemAccountId,
      apiKeyId,
      groupId,
      trafficSource,
      requestLane
    })
    if (fallback.attempted) {
      return fallback.context
    }
    const statusCode = 400
    const message = gatewayModelFilterFailureMessage(modelFilter)
    const responsePayload = gatewayErrorPayload(message, 'invalid_request_error')
    recordClientIpRequestErrorSample({
      auditCapture,
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint,
      reason: 'unsupported_model',
      signature: modelFilter.reason ?? modelFilter.requestedModel ?? 'unsupported_model'
    })
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
        errorPhase: 'request_validation',
        errorCode: modelFilter.reason ?? 'unsupported_model',
        errorMessage: message
      }
    })
    return undefined
  }

  const orderedCandidateAccounts = orderOpenAIAccountsBySessionAffinity(
    modelFilter.accounts,
    sessionAffinityKey,
    dispatchOrderingOptions
  )
  const initialLocalSuppressionFilter = filterLocallySuppressedGatewayAccounts(orderedCandidateAccounts)
  if (initialLocalSuppressionFilter.allSuppressed) {
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options,
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal,
      reason: 'local_account_suppressed',
      apiKeyRecord,
      systemAccountId,
      apiKeyId,
      groupId,
      trafficSource,
      requestLane
    })
    if (fallback.attempted) {
      logger.warn({
        event: 'gateway_local_account_suppression_fallback',
        suppressedCount: initialLocalSuppressionFilter.suppressedCount,
        suppressedAccountIds: initialLocalSuppressionFilter.suppressedAccountIds,
        nextRetryAfterMs: initialLocalSuppressionFilter.nextRetryAfterMs,
        groupId,
        systemAccountId,
        apiKeyId
      }, '当前号池候选账号均处于本地短期屏蔽，已在派发前尝试后备号池')
      auditCapture.addGatewayMetadata({
        label: 'local_account_suppression',
        metadata: {
          suppressedCount: initialLocalSuppressionFilter.suppressedCount,
          suppressedAccountIds: initialLocalSuppressionFilter.suppressedAccountIds,
          allSuppressed: true,
          nextRetryAfterMs: initialLocalSuppressionFilter.nextRetryAfterMs,
          fallbackAttempted: true
        }
      })
      return fallback.context
    }
  }
  const localSuppressionFilter = await resolveLocalSuppressionFilter({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    accounts: orderedCandidateAccounts,
    systemAccountId,
    apiKeyId,
    groupId,
    signal
  })
  if (!localSuppressionFilter) {
    return undefined
  }

  const proxyHealthOrder = orderOpenAIAccountsByGatewayUpstreamBucketHealth(localSuppressionFilter.accounts)
  if (proxyHealthOrder.applied || proxyHealthOrder.bypassedAllAvoided) {
    logger.warn({
      event: proxyHealthOrder.applied
        ? 'gateway_upstream_bucket_health_avoidance_applied'
        : 'gateway_upstream_bucket_health_avoidance_bypassed',
      applied: proxyHealthOrder.applied,
      avoidedBucketKeys: proxyHealthOrder.avoidedBucketKeys,
      avoidedProxyKeys: proxyHealthOrder.avoidedProxyKeys,
      avoidedAccountIds: proxyHealthOrder.avoidedAccountIds,
      halfOpenBucketKeys: proxyHealthOrder.halfOpenBucketKeys,
      halfOpenAccountIds: proxyHealthOrder.halfOpenAccountIds,
      bypassedAllAvoided: proxyHealthOrder.bypassedAllAvoided,
      groupId,
      systemAccountId,
      apiKeyId
    }, proxyHealthOrder.applied
      ? '上游桶运行态避让已应用到候选列表'
      : '上游桶运行态避让无可用备选，保持原候选列表')
    auditCapture.addGatewayMetadata({
      label: 'upstream_bucket_health_avoidance',
      metadata: {
        applied: proxyHealthOrder.applied,
        avoidedBucketKeys: proxyHealthOrder.avoidedBucketKeys,
        avoidedProxyKeys: proxyHealthOrder.avoidedProxyKeys,
        avoidedAccountIds: proxyHealthOrder.avoidedAccountIds,
        halfOpenBucketKeys: proxyHealthOrder.halfOpenBucketKeys,
        halfOpenAccountIds: proxyHealthOrder.halfOpenAccountIds,
        bypassedAllAvoided: proxyHealthOrder.bypassedAllAvoided
      }
    })
  }

  const clientIpAccountAvoidance = orderOpenAIAccountsByClientIpAccountAvoidance(proxyHealthOrder.accounts, {
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp
  })
  if (clientIpAccountAvoidance.applied || clientIpAccountAvoidance.bypassedAllAvoided) {
    logger.warn({
      event: clientIpAccountAvoidance.applied
        ? 'gateway_client_ip_account_avoidance_applied'
        : 'gateway_client_ip_account_avoidance_bypassed',
      applied: clientIpAccountAvoidance.applied,
      avoidedAccountIds: clientIpAccountAvoidance.avoidedAccountIds,
      bypassedAllAvoided: clientIpAccountAvoidance.bypassedAllAvoided,
      groupId,
      systemAccountId,
      apiKeyId,
      clientIp: gatewayClientIp
    }, clientIpAccountAvoidance.applied
      ? '客户端 IP 级账号回避已应用到候选列表'
      : '客户端 IP 级账号回避无可用备选，保持原候选列表')
    auditCapture.addGatewayMetadata({
      label: 'client_ip_account_avoidance',
      metadata: {
        applied: clientIpAccountAvoidance.applied,
        avoidedAccountIds: clientIpAccountAvoidance.avoidedAccountIds,
        bypassedAllAvoided: clientIpAccountAvoidance.bypassedAllAvoided
      }
    })
  }

  const codexTurnAvoidance = orderOpenAIAccountsByCodexTurnAvoidance(clientIpAccountAvoidance.accounts, clientStrategy)
  if (codexTurnAvoidance.applied || codexTurnAvoidance.bypassedAllAvoided) {
    logger.warn({
      event: 'gateway_codex_turn_account_avoidance',
      applied: codexTurnAvoidance.applied,
      failureCount: codexTurnAvoidance.failureCount,
      avoidedAccountIds: codexTurnAvoidance.avoidedAccountIds,
      bypassedAllAvoided: codexTurnAvoidance.bypassedAllAvoided,
      groupId,
      systemAccountId
    }, codexTurnAvoidance.applied
      ? 'Codex turn 级失败账号避让已应用到候选列表'
      : 'Codex turn 级失败账号避让无可用备选，保持原候选列表')
    auditCapture.addGatewayMetadata({
      label: 'codex_turn_account_avoidance',
      metadata: {
        applied: codexTurnAvoidance.applied,
        failureCount: codexTurnAvoidance.failureCount,
        avoidedAccountIds: codexTurnAvoidance.avoidedAccountIds,
        bypassedAllAvoided: codexTurnAvoidance.bypassedAllAvoided
      }
    })
  }

  const candidateAccounts = codexTurnAvoidance.accounts
  let authorizationQuotaDeniedAccountCount = 0
  let accounts: UpstreamAccount[] = []
  const accountQuotaDecisions = await checkGatewayAuthorizationQuotaBatchAsync({ groupAccess, accounts: candidateAccounts })
  for (const account of candidateAccounts) {
    const decision = accountQuotaDecisions.get(account.id) ?? { allowed: true }
    if (!decision.allowed) {
      authorizationQuotaDeniedAccountCount += 1
      continue
    }
    accounts.push(account)
  }
  if (dispatchOrderingOptions.groupType === 'high_concurrency') {
    accounts = refreshGatewayAccountCurrentConcurrency(accounts)
  }

  if (accounts.length === 0) {
    if (authorizationQuotaDeniedAccountCount > 0) {
      const fallback = await prepareApiKeyGroupFallbackDispatchContext({
        req,
        res,
        auditCapture,
        options,
        startedAt,
        traceId,
        clientIp,
        endpoint,
        requestSnapshot,
        signal,
        reason: 'authorization_quota_exceeded',
        apiKeyRecord,
        systemAccountId,
        apiKeyId,
        groupId,
        trafficSource,
        requestLane
      })
      if (fallback.attempted) {
        return fallback.context
      }
      sendQuotaExceededResponse(req, res, auditCapture, usageContext, startedAt, AUTHORIZATION_QUOTA_EXCEEDED_MESSAGE)
      return undefined
    }
    const statusCode = 503
    const responsePayload = gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
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
        errorPhase: 'dispatch',
        errorCode: 'service_unavailable',
        errorMessage: '没有可用的上游账户'
      }
    })
    return undefined
  }

  const laneAwareDispatchOrderingOptions = {
    ...dispatchOrderingOptions,
    requestLane
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    accounts = orderOpenAIAccountsBySessionAffinity(
      refreshGatewayAccountCurrentConcurrency(accounts),
      sessionAffinityKey,
      dispatchOrderingOptions
    )
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options,
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal,
      reason: 'high_concurrency_group_busy',
      apiKeyRecord,
      systemAccountId,
      apiKeyId,
      groupId,
      trafficSource,
      requestLane
    })
    if (fallback.attempted) {
      return fallback.context
    }
  }

  if (dispatchOrderingOptions.groupType !== 'high_concurrency'
    && areGatewayAccountsCapacityBusyForLane(accounts, requestLane, groupAccess.schedulingPolicy)) {
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options,
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal,
      reason: 'group_capacity_busy',
      apiKeyRecord,
      systemAccountId,
      apiKeyId,
      groupId,
      trafficSource,
      requestLane
    })
    if (fallback.attempted) {
      return fallback.context
    }
  }

  if (dispatchOrderingOptions.groupType === 'high_concurrency') {
    const clientIpConcurrency = await acquireHighConcurrencyClientIpSlot({
      systemAccountId,
      groupId,
      apiKeyId,
      clientIp: gatewayClientIp,
      policy: groupAccess.schedulingPolicy,
      signal
    })
    if (clientIpConcurrency.enabled) {
      auditCapture.addGatewayMetadata({
        label: 'high_concurrency_client_ip',
        metadata: clientIpConcurrencyAuditMetadata(clientIpConcurrency)
      })
    }
    if (!clientIpConcurrency.acquired) {
      if (signal?.aborted || res.writableEnded) {
        return undefined
      }
      const statusCode = 429
      const responsePayload = gatewayErrorPayload(clientIpConcurrencyFailureMessage(clientIpConcurrency), 'rate_limit_exceeded')
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
          errorPhase: 'dispatch',
          errorCode: 'rate_limit_exceeded',
          errorMessage: responsePayload.error.message
        }
      })
      return undefined
    }
    releaseClientIpConcurrency = clientIpConcurrency.release
    if (signal?.aborted || res.writableEnded) {
      releaseClientIpConcurrency()
      return undefined
    }
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    const queueWait = await waitForHighConcurrencyGroupCapacity({
      systemAccountId,
      groupId,
      apiKeyId,
      accountIds: accounts.map((account) => account.id),
      accountConcurrencyLimits: Object.fromEntries(accounts.map((account) => [account.id, account.concurrencyLimit])),
      lane: requestLane,
      policy: groupAccess.schedulingPolicy,
      signal
    })
    auditCapture.addGatewayMetadata({
      label: 'high_concurrency_group_queue',
      metadata: {
        ...queueWait,
        lane: requestLane
      }
    })
    if (signal?.aborted || res.writableEnded) {
      releaseClientIpConcurrency()
      return undefined
    }
    accounts = orderOpenAIAccountsBySessionAffinity(
      refreshGatewayAccountCurrentConcurrency(accounts),
      sessionAffinityKey,
      dispatchOrderingOptions
    )
  }

  if (areOpenAIHighConcurrencyAccountsBusyForLane(accounts, laneAwareDispatchOrderingOptions)) {
    const statusCode = 429
    const responsePayload = gatewayErrorPayload('分组繁忙，请稍后重试', 'rate_limit_exceeded')
    releaseClientIpConcurrency()
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
        errorPhase: 'dispatch',
        errorCode: 'rate_limit_exceeded',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
  }

  return {
    activeGatewaySettings,
    usageContext,
    accounts,
    sessionAffinityKey,
    clientStrategy,
    clientIpAccountAvoidanceTracker,
    requestLane,
    groupSchedulingPolicy: groupAccess.schedulingPolicy,
    streamInterceptPolicies: runtimeStreamInterceptPolicies ?? [],
    apiKeyRecord,
    releaseClientIpConcurrency
  }
}

async function downgradeGatewayAutoImageGenerationToolForPermission(
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
    const parsedBody = await parseGatewayJsonBodyInWorker(rawBody, undefined, signal)
    request.gatewayParsedJsonBodyAvailable = true
    request.gatewayParsedJsonBody = parsedBody
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
  request.gatewayUpstreamBodyCache = undefined
  request.gatewayRequestBody = createGatewayRequestBodyState({
    rawBody,
    contentType: previousState?.contentType ?? req.headers['content-type'] ?? 'application/json',
    jsonParseStatus: 'invalid_json',
    model: previousState?.model,
    stream: previousState?.stream,
    imageGeneration: previousState?.imageGeneration,
    imageGenerationForced: previousState?.imageGenerationForced
  })
}

function sendInvalidJsonGatewayResponse(input: {
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
}): void {
  const statusCode = 400
  const responsePayload = gatewayErrorPayload('请求体不是合法 JSON', 'invalid_request_error')
  recordClientIpRequestErrorSample({
    auditCapture: input.auditCapture,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    clientIp: input.clientIp,
    endpoint: input.endpoint,
    reason: 'invalid_json',
    signature: 'invalid_json'
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
      errorPhase: 'request_validation',
      errorCode: 'invalid_json',
      errorMessage: responsePayload.error.message
    }
  })
}

interface ApiKeyGroupFallbackDispatchInput {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  options: OpenAIGatewayRequestPreflightOptions
  startedAt: number
  traceId: string
  clientIp?: string
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
  signal?: AbortSignal
  reason: string
  apiKeyRecord?: GatewayApiKeyRow
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  trafficSource: OpenAIGatewayTrafficSource
  requestLane: OpenAIGatewayRequestLane
  streamInterceptPolicies?: StreamInterceptPolicySummary[]
  excludedAccountIds?: Iterable<string>
  allowCandidateWrap?: boolean
}

interface ApiKeyGroupFallbackDispatchResult {
  attempted: boolean
  context?: OpenAIGatewayDispatchContext
}

export async function prepareApiKeyGroupFallbackDispatchContext(
  input: ApiKeyGroupFallbackDispatchInput
): Promise<ApiKeyGroupFallbackDispatchResult> {
  if (!canAttemptApiKeyGroupFallback(input.apiKeyRecord, input.groupId, input.allowCandidateWrap === true)) {
    return { attempted: false }
  }
  const candidate = await resolveNextApiKeyGroupFallbackCandidate(input)
  if (!candidate) {
    return { attempted: false }
  }
  input.auditCapture.addGatewayMetadata({
    label: 'api_key_group_route_fallback',
    metadata: {
      reason: input.reason,
      fromGroupId: input.groupId,
      toGroupId: candidate.groupId
    }
  })
  const context = await prepareOpenAIGatewayDispatchContext({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    options: {
      ...input.options,
      identity: {
        systemAccountId: input.systemAccountId,
        apiKeyId: input.apiKeyId,
        groupId: candidate.groupId
      },
      apiKeyRecord: input.apiKeyRecord,
      candidateAccounts: candidate.accounts,
      streamInterceptPolicies: input.streamInterceptPolicies ?? input.options.streamInterceptPolicies,
      trafficSource: input.trafficSource,
      requestLane: input.requestLane
    },
    startedAt: input.startedAt,
    traceId: input.traceId,
    clientIp: input.clientIp,
    endpoint: input.endpoint,
    requestSnapshot: input.requestSnapshot,
    signal: input.signal
  })
  return { attempted: true, context }
}

function canAttemptApiKeyGroupFallback(apiKeyRecord: GatewayApiKeyRow | undefined, groupId: string, allowCandidateWrap: boolean): boolean {
  const bindings = apiKeyRecord?.group_bindings ?? []
  if (bindings.length <= 1) {
    return false
  }
  const currentIndex = bindings.findIndex((binding) => binding.group_id === groupId)
  return currentIndex >= 0 && (allowCandidateWrap || currentIndex < bindings.length - 1)
}

async function resolveNextApiKeyGroupFallbackCandidate(input: ApiKeyGroupFallbackDispatchInput): Promise<{
  groupId: string
  accounts: UpstreamAccount[]
} | undefined> {
  const bindings = input.apiKeyRecord?.group_bindings ?? []
  const currentIndex = bindings.findIndex((binding) => binding.group_id === input.groupId)
  const candidateBindings = currentIndex >= 0
    ? input.allowCandidateWrap
      ? [...bindings.slice(currentIndex + 1), ...bindings.slice(0, currentIndex + 1)]
      : bindings.slice(currentIndex + 1)
    : bindings.filter((binding) => binding.group_id !== input.groupId)
  const requestedModel = requestModel(input.req)
  const excludedAccountIds = new Set(input.excludedAccountIds ?? [])
  const seenGroupIds = new Set<string>()
  for (const binding of candidateBindings) {
    if (!binding.group_id || seenGroupIds.has(binding.group_id)) {
      continue
    }
    seenGroupIds.add(binding.group_id)
    const groupAccess = await resolveCachedGroupUsageAccessMetadataAsync(binding.group_id, input.systemAccountId)
    if (!groupAccess) {
      continue
    }
    const accounts = (await listCachedOpenAIAccountsForGroupAsync(binding.group_id, input.systemAccountId))
      .filter((account) => !excludedAccountIds.has(account.id))
    if (!accounts.length) {
      continue
    }
    const capabilityFilter = filterGatewayAccountsByRequestCapability(input.req, accounts)
    if (!capabilityFilter.accounts.length) {
      continue
    }
    const modelFilter = filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, requestedModel)
    if (!modelFilter.accounts.length) {
      continue
    }
    const accountQuotaDecisions = await checkGatewayAuthorizationQuotaBatchAsync({ groupAccess, accounts: modelFilter.accounts })
    const quotaAllowedAccounts = modelFilter.accounts.filter((account) => {
      const decision = accountQuotaDecisions.get(account.id) ?? { allowed: true }
      return decision.allowed
    })
    if (!quotaAllowedAccounts.length) {
      continue
    }
    if ((input.reason === 'high_concurrency_group_busy' || input.reason === 'group_capacity_busy')
      && areGatewayAccountsCapacityBusyForLane(quotaAllowedAccounts, input.requestLane, groupAccess.schedulingPolicy)) {
      continue
    }
    if ((input.reason === 'local_account_suppressed' || input.reason === 'upstream_accounts_exhausted' || input.reason === 'stream_intercept_server_retry_exhausted')
      && filterLocallySuppressedGatewayAccounts(quotaAllowedAccounts).allSuppressed) {
      continue
    }
    return {
      groupId: binding.group_id,
      accounts: quotaAllowedAccounts
    }
  }
  return undefined
}

async function resolveLocalSuppressionFilter(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  accounts: UpstreamAccount[]
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  signal?: AbortSignal
}): Promise<LocalAccountSuppressionFilterResult<UpstreamAccount> | undefined> {
  const filter = filterLocallySuppressedGatewayAccounts(input.accounts)
  if (filter.suppressedCount > 0) {
    logger.warn({
      event: filter.allSuppressed
        ? 'gateway_local_account_suppression_exhausted'
        : 'gateway_local_account_suppression_applied',
      suppressedCount: filter.suppressedCount,
      suppressedAccountIds: filter.suppressedAccountIds,
      allSuppressed: filter.allSuppressed,
      nextRetryAfterMs: filter.nextRetryAfterMs,
      groupId: input.groupId,
      systemAccountId: input.systemAccountId,
      apiKeyId: input.apiKeyId
    }, filter.allSuppressed
      ? '候选上游账号均处于本地短期屏蔽，立即返回'
      : '网关本地短期屏蔽账号已应用到候选列表')
    input.auditCapture.addGatewayMetadata({
      label: 'local_account_suppression',
      metadata: {
        suppressedCount: filter.suppressedCount,
        suppressedAccountIds: filter.suppressedAccountIds,
        allSuppressed: filter.allSuppressed,
        nextRetryAfterMs: filter.nextRetryAfterMs
      }
    })
  }

  if (!filter.allSuppressed) {
    return filter
  }

  if (input.signal?.aborted || input.res.writableEnded) {
    return undefined
  }

  const statusCode = 503
  const responsePayload = gatewayErrorPayload('所有上游账户正在临时隔离，请稍后重试', 'service_unavailable')
  if (!input.res.headersSent && filter.nextRetryAfterMs !== undefined) {
    input.res.setHeader('Retry-After', String(Math.max(1, Math.ceil(filter.nextRetryAfterMs / 1000))))
  }
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
      errorPhase: 'dispatch',
      errorCode: 'service_unavailable',
      errorMessage: responsePayload.error.message
    }
  })
  return undefined
}

function refreshGatewayAccountCurrentConcurrency(accounts: UpstreamAccount[]): UpstreamAccount[] {
  const concurrency = loadAccountCurrentConcurrencyByIds(accounts.map((account) => account.id))
  return accounts.map((account) => ({
    ...account,
    currentConcurrency: concurrency.get(account.id) ?? 0
  }))
}

function areGatewayAccountsCapacityBusyForLane(
  accounts: UpstreamAccount[],
  requestLane: OpenAIGatewayRequestLane,
  schedulingPolicy?: GroupSchedulingPolicy
): boolean {
  if (accounts.length === 0) {
    return false
  }
  const accountIds = accounts.map((account) => account.id)
  const currentConcurrency = loadAccountCurrentConcurrencyByIds(accountIds)
  const imageLaneConcurrency = requestLane === 'image'
    ? loadAccountCurrentConcurrencyByIds(accountIds, 'image')
    : undefined
  return accounts.every((account) => {
    const hardLimit = accountHardConcurrencyLimit(account)
    if ((currentConcurrency.get(account.id) ?? 0) >= hardLimit) {
      return true
    }
    if (requestLane !== 'image') {
      return false
    }
    return (imageLaneConcurrency?.get(account.id) ?? 0) >= effectiveImageLaneConcurrencyLimit({
      accountConcurrencyLimit: hardLimit,
      policy: schedulingPolicy
    })
  })
}

function accountHardConcurrencyLimit(account: UpstreamAccount): number {
  return Number.isFinite(account.concurrencyLimit) ? Math.max(1, Math.trunc(account.concurrencyLimit)) : 1
}

function mergeGatewaySettings(base: GatewaySettings, override?: Partial<GatewaySettings>): GatewaySettings {
  if (!override) return base
  return {
    defaultTemporaryUnschedulableMinutes: override.defaultTemporaryUnschedulableMinutes ?? base.defaultTemporaryUnschedulableMinutes,
    temporaryUnschedulableRetryIntervalSeconds: override.temporaryUnschedulableRetryIntervalSeconds ?? base.temporaryUnschedulableRetryIntervalSeconds,
    temporaryUnschedulableRetryAttempts: override.temporaryUnschedulableRetryAttempts ?? base.temporaryUnschedulableRetryAttempts,
    streamCircuitBreakerEnabled: override.streamCircuitBreakerEnabled ?? base.streamCircuitBreakerEnabled,
    streamRequestTimeoutSeconds: override.streamRequestTimeoutSeconds ?? base.streamRequestTimeoutSeconds,
    streamIdleTimeoutSeconds: override.streamIdleTimeoutSeconds ?? base.streamIdleTimeoutSeconds,
    streamFailureThresholdCount: override.streamFailureThresholdCount ?? base.streamFailureThresholdCount,
    streamFailureThresholdWindowMinutes: override.streamFailureThresholdWindowMinutes ?? base.streamFailureThresholdWindowMinutes
  }
}

function clientIpConcurrencyAuditMetadata(decision: ClientIpConcurrencyDecision): Record<string, unknown> {
  if (!decision.enabled) {
    return { enabled: false }
  }
  if (decision.acquired) {
    return {
      enabled: true,
      acquired: true,
      current: decision.current,
      limit: decision.limit,
      waitedMs: decision.waitedMs,
      queued: decision.queued,
      queueSizeBeforeAcquire: decision.queueSizeBeforeAcquire
    }
  }
  return {
    enabled: true,
    acquired: false,
    reason: decision.reason,
    current: decision.current,
    limit: decision.limit,
    waitedMs: decision.waitedMs,
    queueSize: decision.queueSize
  }
}

function clientIpConcurrencyFailureMessage(decision: ClientIpConcurrencyDecision): string {
  if (decision.enabled && !decision.acquired && decision.reason === 'timeout') {
    return '当前 IP 并发排队等待超时，请稍后重试'
  }
  return '当前 IP 并发已达到分组限制，请稍后重试'
}

function recordClientIpRequestErrorSample(input: {
  auditCapture: AuditCaptureContext
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
  endpoint: string
  reason: GatewayClientIpErrorCircuitReason
  signature?: string
}): void {
  const result = recordClientIpErrorCircuitSample(input)
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

function buildGatewayUsageContext(input: {
  traceId: string
  clientIp?: string
  identity: OpenAIGatewayRequestIdentity
  trafficSource: OpenAIGatewayTrafficSource
  groupUsageFields?: ReturnType<typeof groupUsageMetadata>
  endpoint: string
  requestSnapshot: UsageRequestSnapshot
}): GatewayFailureUsageContext {
  const { traceId, clientIp, identity, trafficSource, groupUsageFields, endpoint, requestSnapshot } = input
  return {
    traceId,
    trafficSource,
    clientIp,
    systemAccountId: identity.systemAccountId,
    apiKeyId: identity.apiKeyId,
    groupId: identity.groupId,
    ...groupUsageFields,
    endpoint,
    requestSnapshot
  }
}

function noop(): void {}
