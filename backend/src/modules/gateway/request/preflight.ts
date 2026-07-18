import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import { bindRequestContextFields } from '../../../shared/request-context.js'
import { type GatewayApiKeyRow, type GroupUsageAccessMetadata, type OpenAIAccountsForGroupDiagnostics } from '../../../storage/repositories.js'
import {
  listCachedActiveResponseInspectionPoliciesForAccountsAsync,
  listFreshOpenAIAccountsForGroupAsync,
  listCachedOpenAIAccountsForGroupAsync,
  listRecoverableUnavailableOpenAIAccountsForGroupAsync,
  readCachedGatewaySettingsAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from '../runtime/runtime-cache.service.js'
import { type GatewaySettings } from '../policy/account-error-policy.service.js'
import { responseHeadersToObject, type AuditCaptureContext } from '../audit/capture.service.js'
import {
  createClientIpAccountAvoidanceTracker,
  type ClientIpAccountAvoidanceTracker
} from '../runtime/client-ip-account-avoidance.service.js'
import {
  getGatewayRequestBodyState
} from './body.js'
import {
  type OpenAIGatewayRequestLane
} from '../protocols/openai-v1/request-lane.js'
import {
  gatewayClientProfileHeader,
  openAIGatewayClientStrategyAuditMetadata,
  resolveOpenAIGatewayClientStrategy,
  type OpenAIGatewayClientStrategyContext
} from '../client-profiles/strategy.js'
import {
  type GatewayCircuitDecision,
  inspectClientIpErrorCircuitAsync,
  recordClientIpErrorCircuitSuccessAsync
} from '../runtime/client-ip-error-circuit.service.js'
import {
  finalizeGatewayAuthFailureAudit,
  sendAnthropicModelsGatewayResponse,
  sendGeminiModelsGatewayResponse,
  sendOpenAIModelsGatewayResponse,
  sendPublicModelsGatewayResponse
} from '../response/fixed-responses.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import { gatewayErrorPayload, sendGatewayJsonError } from '../response/responses.js'
import { extractGatewayApiKey, resolveGatewayRuntimeAsync, type GatewayRuntimeRequest } from './pre-auth.js'
import {
  isOpenAIModelsRequest,
  type UpstreamAccount
} from '../protocols/openai-v1/route-helpers.js'
import { isAnthropicModelsRequest } from '../protocols/anthropic-v1/route-helpers.js'
import { isGeminiModelsRequest } from '../protocols/gemini-v1beta/route-helpers.js'
import type { ResponseProtocolCode } from '../protocols/openai-v1/response-semantics.js'
import {
  resolveOpenAIGatewaySessionAffinityKey
} from '../runtime/session-affinity.service.js'
import { type UsageRequestSnapshot } from '../usage/snapshots.js'
import {
  groupUsageMetadata,
  type GatewayFailureUsageContext
} from '../usage/records.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import type { ClientCompatibilityCapability, GroupSchedulingPolicy, RouteStrategySpeedFirstConfig } from '../../../domain/types.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_PROTOCOL_CODE,
  OPENAI_RESPONSES_FAMILY,
  isAnthropicProtocolProfile
} from '../../../domain/provider-protocol.js'
import {
  canAttemptApiKeyGroupFallback,
  resolveNextApiKeyGroupFallbackCandidate
} from '../dispatch/api-key-group-fallback-candidate.js'
import {
  sendInvalidJsonGatewayResponse
} from './local-request-errors.js'
import { filterOpenAIGatewayRequestCandidateAccounts } from '../dispatch/candidate-filter.js'
import type { GatewayAccountModelPriority } from '../dispatch/model-filter.js'
import { prepareOpenAIGatewayDispatchAccounts } from '../dispatch/preparation.js'
import { applyOpenAIGatewayImagePermissionPreflight } from './image-permission-preflight.js'
import {
  rejectGatewayApiKeyQuotaIfExceeded,
  rejectGatewayAuthorizationQuotaIfExceeded,
  rejectMissingGatewayGroupAccess,
  rejectUnavailableGatewayApiKey
} from './authorization-preflight.js'
import { resolveHybridGatewayRoute, type HybridGatewayRuntimeRoute } from '../hybrid/routing.service.js'
import { resolveNormalGatewayModelRoute } from '../routing/normal-model-route.service.js'
import { applyCodexResponsesContextStatePreflight } from '../codex-responses/chat-bridge-state.js'
import { applyCodexResponsesChatBridgeCompactPreflight } from '../codex-responses/compact-preflight.js'
import {
  recoverableUnavailableMaxWaitMs,
  waitForRecoverableUnavailableState
} from '../runtime/recoverable-unavailable-wait.js'
import { requestModel } from './metadata.js'
import { gatewayRequestEndpointFamily, openAIRequestEndpointFamily, resolveOpenAIAccountModelMapping } from '../protocols/openai-v1/model-mapping.js'
import { consumePublicModelsRateLimit } from '../runtime/public-models-rate-limit.service.js'
import { consumeAuthenticatedModelsRateLimit } from '../runtime/authenticated-models-rate-limit.service.js'
import {
  createSameAccountRetryBudget,
  type SameAccountRetryBudget
} from '../dispatch/upstream-dispatch.js'
import { gatewayRequestAbsoluteDeadlineAtMs } from '../runtime/gateway-request-deadline.js'

export interface OpenAIGatewayRequestIdentity {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
}

interface OpenAIGatewayRequestPreflightOptions {
  identity?: OpenAIGatewayRequestIdentity
  apiKeyRecord?: GatewayApiKeyRow
  groupFallbackApiKeyRecord?: GatewayApiKeyRow
  candidateAccounts?: UpstreamAccount[]
  responseInspectionPolicies?: ResponseInspectionPolicySummary[]
  disableSessionAffinity?: boolean
  trafficSource?: OpenAIGatewayTrafficSource
  settingsOverride?: Partial<GatewaySettings>
  requestLane?: OpenAIGatewayRequestLane
  ignoreAccountRuntimeSuppression?: boolean
  sameAccountRetryBudget?: SameAccountRetryBudget
  requestDeadlineAtMs?: number
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
  modelPriority: GatewayAccountModelPriority
  requestLane: OpenAIGatewayRequestLane
  groupSchedulingPolicy?: GroupSchedulingPolicy
  normalRouteSpeedFirstConfig?: RouteStrategySpeedFirstConfig
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
  apiKeyRecord?: GatewayApiKeyRow
  groupFallbackApiKeyRecord?: GatewayApiKeyRow
  hybridRoute?: HybridGatewayRuntimeRoute
  normalRouteLatencyDegradationApplied?: boolean
  codexTurnAccountAvoidanceApplied?: boolean
  codexTurnAvoidedAccountIds?: string[]
  precheckHalfOpenEligible?: boolean
  requestDeadlineAtMs: number
  sameAccountRetryBudget: SameAccountRetryBudget
  releaseClientIpConcurrency: () => void
}

export async function prepareOpenAIGatewayDispatchContext(
  input: PrepareOpenAIGatewayDispatchContextInput
): Promise<OpenAIGatewayDispatchContext | undefined> {
  const { req, res, auditCapture, options, startedAt, traceId, clientIp, endpoint, requestSnapshot, signal } = input
  let gatewaySettings: GatewaySettings | undefined
  let apiKeyRecord: GatewayApiKeyRow | undefined = options.apiKeyRecord
  let groupFallbackApiKeyRecord: GatewayApiKeyRow | undefined = options.groupFallbackApiKeyRecord ?? apiKeyRecord
  let runtimeGroupAccess: GroupUsageAccessMetadata | undefined
  let runtimeAccounts: UpstreamAccount[] | undefined
  let runtimeAccountDispatchDiagnostics: OpenAIAccountsForGroupDiagnostics | undefined
  let runtimeResponseInspectionPolicies: ResponseInspectionPolicySummary[] | undefined = options.responseInspectionPolicies
  let selectedHybridRoute: HybridGatewayRuntimeRoute | undefined
  const initialModelsResponseProtocol = resolveGatewayModelsResponseProtocol(req)

  if (initialModelsResponseProtocol && !options.identity) {
    const completed = await handleGatewayModelsRequestBeforeRequiredAuth({
      req,
      res,
      auditCapture,
      protocol: initialModelsResponseProtocol,
      startedAt,
      clientIp
    })
    if (completed) {
      return undefined
    }
  }

  let identity = options.identity ?? await (async () => {
    const runtime = await resolveGatewayRuntimeAsync(req, res)
    if (!runtime?.apiKey) {
      finalizeGatewayAuthFailureAudit(req, res, auditCapture)
      return undefined
    }
    gatewaySettings = runtime.settings
    apiKeyRecord = runtime.apiKey
    groupFallbackApiKeyRecord ??= runtime.apiKey
    runtimeGroupAccess = runtime.groupAccess
    runtimeAccounts = runtime.accounts
    runtimeAccountDispatchDiagnostics = runtime.accountDispatchDiagnostics
    runtimeResponseInspectionPolicies = runtime.responseInspectionPolicies
    options.responseInspectionPolicies = runtime.responseInspectionPolicies
    return {
      systemAccountId: runtime.apiKey.system_account_id,
      apiKeyId: runtime.apiKey.id,
      groupId: runtime.apiKey.selected_group_id
    }
  })()
  if (!identity) {
    return undefined
  }

  const trafficSource = options.trafficSource ?? 'gateway'
  const gatewayClientIp = trafficSource === 'gateway' ? clientIp : undefined
  let { systemAccountId, apiKeyId, groupId } = identity
  const apiKeyUnavailable = Boolean(apiKeyId && !apiKeyRecord)
  auditCapture.bindContext({
    systemAccountId,
    apiKeyId,
    groupId,
    providerCode: OPENAI_PROTOCOL_CODE,
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
  const activeGatewaySettings = mergeGatewaySettings(
    gatewaySettings ?? await readCachedGatewaySettingsAsync(),
    options.settingsOverride
  )
  const requestDeadlineAtMs = options.requestDeadlineAtMs
    ?? gatewayRequestAbsoluteDeadlineAtMs(startedAt, activeGatewaySettings.noAvailableAccountWaitTimeoutSeconds)
  options.requestDeadlineAtMs = requestDeadlineAtMs
  const sameAccountRetryBudget = options.sameAccountRetryBudget
    ?? createSameAccountRetryBudget(activeGatewaySettings.temporaryUnschedulableRetryAttempts)
  options.sameAccountRetryBudget = sameAccountRetryBudget
  let requestLane = options.requestLane ?? 'text'
  const currentGroupUsageContext = (input: { groupId?: string; groupAccess?: GroupUsageAccessMetadata } = {}): GatewayFailureUsageContext => buildGatewayUsageContext({
    traceId,
    clientIp,
    identity: {
      systemAccountId,
      apiKeyId,
      groupId: input.groupId ?? groupId
    },
    trafficSource,
    groupUsageFields: input.groupAccess
      ? groupUsageMetadata(input.groupAccess)
      : runtimeGroupAccess
        ? groupUsageMetadata(runtimeGroupAccess)
        : undefined,
    endpoint,
    requestSnapshot
  })
  const clientIpErrorCircuit = await inspectClientIpErrorCircuitAsync({
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp,
    endpoint
  })
  if (await sendClientIpErrorCircuitGatewayResponse({
    req,
    res,
    auditCapture,
    usageContext: currentGroupUsageContext(),
    startedAt,
    circuit: clientIpErrorCircuit,
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp
  })) {
    return undefined
  }
  if (await rejectUnavailableGatewayApiKey({
    req,
    res,
    auditCapture,
    usageContext: currentGroupUsageContext(),
    startedAt,
    apiKeyUnavailable
  })) {
    return undefined
  }
  const initialBodyState = getGatewayRequestBodyState(req)
  if (initialBodyState?.jsonParseStatus === 'invalid_json') {
    await sendInvalidJsonGatewayResponse({
      req,
      res,
      auditCapture,
      usageContext: currentGroupUsageContext(),
      startedAt,
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint
    })
    return undefined
  }
  const initialClientStrategy = resolveOpenAIGatewayClientStrategy(req, {
    systemAccountId,
    apiKeyId,
    groupId,
    endpoint
  })
  if (!initialModelsResponseProtocol && !options.identity && trafficSource === 'gateway' && apiKeyRecord && apiKeyRecord.route_strategy_mode !== 'hybrid_smart') {
    const previousGroupId = groupId
    const previousBindingCount = apiKeyRecord.group_bindings?.length ?? 0
    const normalRoute = await resolveNormalGatewayModelRoute({
      req,
      apiKeyRecord,
      requestClientCompatibility: initialClientStrategy.requestClientCompatibility
    })
    if (normalRoute.outcome === 'selected') {
      groupFallbackApiKeyRecord ??= apiKeyRecord
      apiKeyRecord = normalRoute.apiKeyRecord
      groupId = normalRoute.groupId
      identity = {
        systemAccountId,
        apiKeyId,
        groupId
      }
      runtimeGroupAccess = normalRoute.groupAccess
      runtimeAccounts = normalRoute.accounts
      runtimeAccountDispatchDiagnostics = undefined
      runtimeResponseInspectionPolicies = normalRoute.responseInspectionPolicies
      options.responseInspectionPolicies = normalRoute.responseInspectionPolicies
      auditCapture.addGatewayMetadata({
        label: 'normal_model_route',
        metadata: {
          requestedModel: normalRoute.requestedModel,
          fromGroupId: previousGroupId,
          toGroupId: normalRoute.groupId,
          routeSource: normalRoute.routeSource,
          matchedProviderCode: normalRoute.matchedProviderCode,
          sourceBindingCount: previousBindingCount,
          candidateBindingCount: normalRoute.apiKeyRecord.group_bindings?.length ?? 0
        }
      })
      auditCapture.bindContext({ groupId })
      bindRequestContextFields({
        systemAccountId,
        apiKeyId,
        groupId,
        trafficSource
      })
      const targetClientIpErrorCircuit = await inspectClientIpErrorCircuitAsync({
        systemAccountId,
        apiKeyId,
        groupId,
        clientIp: gatewayClientIp,
        endpoint
      })
      if (await sendClientIpErrorCircuitGatewayResponse({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext({ groupId, groupAccess: runtimeGroupAccess }),
        startedAt,
        circuit: targetClientIpErrorCircuit,
        systemAccountId,
        apiKeyId,
        groupId,
        clientIp: gatewayClientIp
      })) {
        return undefined
      }
    }
    if (normalRoute.outcome === 'failed') {
      auditCapture.addGatewayMetadata({
        label: 'normal_model_route_failed',
        metadata: {
          requestedModel: normalRoute.requestedModel,
          reason: normalRoute.code,
          matchedProviderCodes: normalRoute.matchedProviderCodes,
          sourceBindingCount: previousBindingCount
        }
      })
      const responsePayload = gatewayErrorPayload(normalRoute.message, normalRoute.type, normalRoute.code)
      await sendGatewayFailureResponse({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext(),
        startedAt,
        statusCode: normalRoute.statusCode,
        responsePayload,
        audit: {
          outcome: 'gateway_failed',
          errorPhase: normalRoute.statusCode >= 500 ? 'dispatch' : 'request_validation',
          errorCode: normalRoute.code,
          errorMessage: normalRoute.message
        }
      })
      return undefined
    }
  }

  if (!initialModelsResponseProtocol && !options.identity && trafficSource === 'gateway' && apiKeyRecord?.route_strategy_mode === 'hybrid_smart') {
    const hybridRoute = await resolveHybridGatewayRoute({
      req,
      apiKeyRecord,
      traceId,
      clientIp,
      endpoint,
      auditCapture,
      requestClientCompatibility: initialClientStrategy.requestClientCompatibility,
      signal
    })
    if (hybridRoute.outcome === 'failed') {
      auditCapture.addGatewayMetadata({
        label: 'hybrid_route',
        metadata: {
          failed: true,
          reason: hybridRoute.reason,
          targetModel: hybridRoute.targetModel,
          level: hybridRoute.scoring?.failed ? undefined : hybridRoute.scoring?.level,
          scoringDefaulted: hybridRoute.scoring?.defaulted,
          scoringErrorCode: hybridRoute.scoring?.errorCode,
          scoringErrorMessage: hybridRoute.scoring?.errorMessage,
          scoringFallbackMaxLevel: apiKeyRecord.hybrid_routing_config?.scoringFallbackMaxLevel
        }
      })
      const statusCode = hybridRouteFailureStatusCode(hybridRoute.reason)
      const responsePayload = gatewayErrorPayload(
        hybridRouteFailureMessage(hybridRoute.reason),
        statusCode === 503 ? 'service_unavailable' : 'upstream_response_error',
        hybridRoute.reason
      )
      await sendGatewayFailureResponse({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext(),
        startedAt,
        statusCode,
        responsePayload,
        audit: {
          outcome: 'gateway_failed',
          errorPhase: 'dispatch',
          errorCode: hybridRoute.reason,
          errorMessage: responsePayload.error.message
        }
      })
      return undefined
    }
    if (hybridRoute.outcome === 'selected') {
      apiKeyRecord = hybridRoute.apiKeyRecord
      groupId = hybridRoute.groupId
      identity = {
        systemAccountId,
        apiKeyId,
        groupId
      }
      runtimeGroupAccess = hybridRoute.groupAccess
      runtimeAccounts = hybridRoute.accounts
      runtimeAccountDispatchDiagnostics = undefined
      runtimeResponseInspectionPolicies = hybridRoute.responseInspectionPolicies
      options.responseInspectionPolicies = hybridRoute.responseInspectionPolicies
      selectedHybridRoute = {
        apiKeyRecord,
        config: hybridRoute.config,
        scoring: hybridRoute.scoring,
        route: hybridRoute.route,
        targetModel: hybridRoute.targetModel,
        affinityApplied: hybridRoute.affinityApplied,
        scoringFallbackApplied: hybridRoute.scoringFallbackApplied,
        qualityRetryCount: 0
      }
      auditCapture.bindContext({ groupId })
      bindRequestContextFields({
        systemAccountId,
        apiKeyId,
        groupId,
        trafficSource
      })
      const targetClientIpErrorCircuit = await inspectClientIpErrorCircuitAsync({
        systemAccountId,
        apiKeyId,
        groupId,
        clientIp: gatewayClientIp,
        endpoint
      })
      if (await sendClientIpErrorCircuitGatewayResponse({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext({ groupId, groupAccess: runtimeGroupAccess }),
        startedAt,
        circuit: targetClientIpErrorCircuit,
        systemAccountId,
        apiKeyId,
        groupId,
        clientIp: gatewayClientIp
      })) {
        return undefined
      }
    }
  }

  const groupAccess = runtimeGroupAccess ?? await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
  if (groupAccess) {
    auditCapture.bindContext({ providerCode: groupAccess.providerCode })
  }
  const clientStrategy = resolveOpenAIGatewayClientStrategy(req, {
    systemAccountId,
    apiKeyId,
    groupId,
    endpoint,
    providerCode: groupAccess?.providerCode
  })
  const clientIpAccountAvoidanceTracker = createClientIpAccountAvoidanceTracker({
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp
  })
  if (clientStrategy.clientProfile === 'codex' || clientStrategy.clientProfile === 'claude_code' || clientStrategy.clientProfile === 'gemini_cli') {
    auditCapture.addGatewayMetadata({
      label: 'client_strategy',
      metadata: openAIGatewayClientStrategyAuditMetadata(clientStrategy)
    })
  }
  if (!groupAccess) {
    await rejectMissingGatewayGroupAccess({
      req,
      res,
      auditCapture,
      usageContext: baseUsageContext,
      startedAt,
      groupAccess
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
    await sendInvalidJsonGatewayResponse({
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

  if (await rejectGatewayApiKeyQuotaIfExceeded({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    apiKeyRecord
  })) {
    return undefined
  }

  if (await rejectGatewayAuthorizationQuotaIfExceeded({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    groupAccess
  })) {
    return undefined
  }

  const modelsResponseProtocol = initialModelsResponseProtocol
  if (modelsResponseProtocol && trafficSource === 'gateway' && apiKeyId) {
    const authenticatedModelsRateLimitDecision = await consumeAuthenticatedModelsRateLimit({
      apiKeyId,
      clientIp: gatewayClientIp
    })
    if (!authenticatedModelsRateLimitDecision.allowed) {
      const limiterUnavailable = authenticatedModelsRateLimitDecision.unavailable === true
      const statusCode = limiterUnavailable ? 503 : 429
      const errorCode = limiterUnavailable
        ? 'authenticated_models_rate_limit_unavailable'
        : 'authenticated_models_rate_limited'
      const retryAfterSeconds = authenticatedModelsRateLimitDecision.retryAfterSeconds ?? (limiterUnavailable ? 5 : 1)
      if (!res.headersSent) {
        res.setHeader('Retry-After', String(retryAfterSeconds))
      }
      auditCapture.addGatewayMetadata({
        label: 'authenticated_models_rate_limit',
        metadata: {
          scope: authenticatedModelsRateLimitDecision.scope,
          limit: authenticatedModelsRateLimitDecision.limit,
          retryAfterSeconds,
          unavailable: limiterUnavailable
        }
      })
      const responsePayload = gatewayErrorPayload(
        limiterUnavailable ? '模型列表限流服务暂不可用，请稍后重试' : '模型列表请求过于频繁，请稍后重试',
        limiterUnavailable ? 'service_unavailable' : 'rate_limit_exceeded',
        errorCode
      )
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
          errorPhase: limiterUnavailable ? 'security' : 'request_validation',
          errorCode,
          errorMessage: responsePayload.error.message
        }
      })
      return undefined
    }
  }

  if (modelsResponseProtocol) {
    await recordClientIpErrorCircuitSuccessAsync({
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint
    })
    const sender = modelsResponseProtocol === 'anthropic_v1'
      ? sendAnthropicModelsGatewayResponse
      : modelsResponseProtocol === 'gemini_v1beta'
        ? sendGeminiModelsGatewayResponse
        : sendOpenAIModelsGatewayResponse
    await sender({
      req,
      res,
      auditCapture,
      usageContext,
      providerCodes: gatewayModelsProviderCodes({
        apiKeyRecord,
        fallbackProviderCode: groupAccess.providerCode
      }),
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
  if (!options.candidateAccounts && runtimeAccountDispatchDiagnostics) {
    auditCapture.addGatewayMetadata({
      label: 'account_dispatch_candidate_window',
      metadata: {
        ...runtimeAccountDispatchDiagnostics,
        returnedCandidateCount: rawCandidateAccounts.length
      }
    })
  }
  const codexBridgeStatePreflight = await applyCodexResponsesContextStatePreflight({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    systemAccountId,
    apiKeyId,
    groupId,
    groupAccess,
    signal
  })
  if (codexBridgeStatePreflight === 'completed') {
    return undefined
  }
  const candidateFilter = await filterOpenAIGatewayRequestCandidateAccounts({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    rawCandidateAccounts,
    clientStrategy,
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp,
    endpoint,
    loadModelAwareCandidateAccounts: options.candidateAccounts
      ? undefined
      : (model, sourceEndpointFamily) => listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId, { requestedModel: model, requestedEndpointFamily: sourceEndpointFamily }),
    recoverUnavailableCandidateAccounts: options.candidateAccounts
      ? undefined
      : () => waitForRecoverableOpenAIGatewayCandidateAccounts({
        req,
        auditCapture,
        systemAccountId,
        apiKeyId,
        groupId,
        startedAt,
        requestDeadlineAtMs,
        signal
      }),
    attemptFallback: (reason) => prepareApiKeyGroupFallbackDispatchContext({
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
      reason,
      apiKeyRecord,
      systemAccountId,
      apiKeyId,
      groupId,
      trafficSource,
      requestLane,
      requestClientCompatibility: clientStrategy.requestClientCompatibility
    })
  })
  if (candidateFilter.outcome === 'fallback') {
    return candidateFilter.context
  }
  if (candidateFilter.outcome === 'completed') {
    return undefined
  }
  runtimeResponseInspectionPolicies = await listCachedActiveResponseInspectionPoliciesForAccountsAsync(candidateFilter.accounts)
  options.responseInspectionPolicies = runtimeResponseInspectionPolicies

  const imagePermissionPreflight = await applyOpenAIGatewayImagePermissionPreflight({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    apiKeyRecord,
    requestLane,
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp,
    endpoint,
    gatewayTextRawBodyLimitMegabytes: activeGatewaySettings.gatewayTextRawBodyLimitMegabytes,
    deferForcedImageGenerationTool: shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge({
      req,
      accounts: candidateFilter.accounts,
      requestClientCompatibility: clientStrategy.requestClientCompatibility
    }),
    signal
  })
  if (imagePermissionPreflight.outcome === 'completed') {
    return undefined
  }
  requestLane = imagePermissionPreflight.requestLane
  const normalRouteSpeedFirstConfig = normalRouteSpeedFirstConfigForApiKey(apiKeyRecord)

  const dispatchPreparation = await prepareOpenAIGatewayDispatchAccounts({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    candidateAccounts: candidateFilter.accounts,
    modelPriority: candidateFilter.modelPriority,
    sessionAffinityKey,
    groupAccess,
    systemAccountId,
    apiKeyId,
    groupId,
    routeStrategyId: apiKeyRecord?.route_strategy_id,
    normalRouteSpeedFirstConfig,
    clientIp: gatewayClientIp,
    clientStrategy,
    requestLane,
    requestDeadlineAtMs,
    signal,
    ignoreAccountRuntimeSuppression: options.ignoreAccountRuntimeSuppression === true,
    attemptFallback: (reason) => prepareApiKeyGroupFallbackDispatchContext({
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
      reason,
      apiKeyRecord,
      systemAccountId,
      apiKeyId,
      groupId,
      trafficSource,
      requestLane,
      requestClientCompatibility: clientStrategy.requestClientCompatibility
    })
  })
  if (dispatchPreparation.outcome === 'fallback') {
    return dispatchPreparation.context
  }
  if (dispatchPreparation.outcome === 'completed') {
    return undefined
  }

  const codexBridgeCompactPreflight = await applyCodexResponsesChatBridgeCompactPreflight({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    systemAccountId,
    apiKeyId,
    groupId,
    groupAccess,
    requestClientCompatibility: clientStrategy.requestClientCompatibility,
    dispatchAccounts: dispatchPreparation.accounts,
    activeGatewaySettings,
    clientIpAccountAvoidanceTracker,
    modelPriority: candidateFilter.modelPriority,
    requestLane,
    groupSchedulingPolicy: groupAccess.schedulingPolicy,
    sameAccountRetryBudget,
    signal
  })
  if (codexBridgeCompactPreflight.outcome === 'completed') {
    dispatchPreparation.releaseClientIpConcurrency()
    return undefined
  }

  return {
    activeGatewaySettings,
    usageContext,
    accounts: codexBridgeCompactPreflight.accounts,
    sessionAffinityKey,
    clientStrategy,
    clientIpAccountAvoidanceTracker,
    modelPriority: candidateFilter.modelPriority,
    requestLane,
    groupSchedulingPolicy: groupAccess.schedulingPolicy,
    normalRouteSpeedFirstConfig,
    responseInspectionPolicies: runtimeResponseInspectionPolicies ?? [],
    apiKeyRecord,
    groupFallbackApiKeyRecord,
    hybridRoute: selectedHybridRoute,
    normalRouteLatencyDegradationApplied: dispatchPreparation.normalRouteLatencyDegradationApplied,
    codexTurnAccountAvoidanceApplied: dispatchPreparation.codexTurnAccountAvoidanceApplied,
    codexTurnAvoidedAccountIds: dispatchPreparation.codexTurnAvoidedAccountIds,
    precheckHalfOpenEligible: dispatchPreparation.precheckHalfOpenEligible,
    requestDeadlineAtMs,
    sameAccountRetryBudget,
    releaseClientIpConcurrency: dispatchPreparation.releaseClientIpConcurrency
  }
}

function normalRouteSpeedFirstConfigForApiKey(apiKeyRecord: GatewayApiKeyRow | undefined): RouteStrategySpeedFirstConfig | undefined {
  if (apiKeyRecord?.route_strategy_mode !== 'normal') return undefined
  const normalConfig = apiKeyRecord.normal_routing_config
  if (normalConfig?.schedulingPreference !== 'speed_first') return undefined
  return normalConfig.speedFirstConfig
}

async function handleGatewayModelsRequestBeforeRequiredAuth(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  protocol: ResponseProtocolCode
  startedAt: number
  clientIp?: string
}): Promise<boolean> {
  if (gatewayModelsRequestHasAuthCredential(input.req)) {
    const runtime = await resolveGatewayRuntimeAsync(input.req as GatewayRuntimeRequest, input.res)
    if (!runtime?.apiKey) {
      finalizeGatewayAuthFailureAudit(input.req, input.res, input.auditCapture)
      return true
    }
    const gatewayRequest = input.req as GatewayRuntimeRequest
    gatewayRequest.gatewayRuntime = runtime
    return false
  }

  const rateLimit = await consumePublicModelsRateLimit({ clientIp: input.clientIp })
  if (!rateLimit.allowed) {
    sendPublicModelsRateLimitedResponse(input, rateLimit.retryAfterSeconds ?? 1)
    return true
  }

  await sendPublicModelsGatewayResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    protocol: modelsResponseKind(input.protocol),
    startedAt: input.startedAt
  })
  return true
}

function gatewayModelsProviderCodes(input: {
  apiKeyRecord?: GatewayApiKeyRow
  fallbackProviderCode?: string
}): string[] {
  const providerCodes = new Set<string>()
  const bindings = input.apiKeyRecord?.group_bindings?.filter((binding) => binding.status === 'active') ?? []
  for (const binding of bindings) {
    const providerCode = binding.provider_code?.trim()
    if (providerCode) providerCodes.add(providerCode)
  }
  const fallbackProviderCode = input.fallbackProviderCode?.trim()
  if (!providerCodes.size && fallbackProviderCode) providerCodes.add(fallbackProviderCode)
  return [...providerCodes]
}

function gatewayModelsRequestHasAuthCredential(req: Request): boolean {
  return Boolean(
    extractGatewayApiKey(req, req.header('authorization'))
      || nonEmptyHeader(req, 'authorization')
      || nonEmptyHeader(req, 'x-api-key')
      || nonEmptyHeader(req, 'x-goog-api-key')
  )
}

function nonEmptyHeader(req: Request, name: string): boolean {
  const value = req.header(name)
  return typeof value === 'string' && value.trim().length > 0
}

function sendPublicModelsRateLimitedResponse(
  input: {
    req: Request
    res: Response
    auditCapture: AuditCaptureContext
    protocol: ResponseProtocolCode
    startedAt: number
  },
  retryAfterSeconds: number
): void {
  if (!input.res.headersSent) {
    input.res.setHeader('Retry-After', String(retryAfterSeconds))
  }
  const responsePayload = gatewayErrorPayload('模型列表请求过于频繁，请稍后重试', 'rate_limit_exceeded', 'public_models_rate_limited')
  sendGatewayJsonError(input.res, 429, responsePayload, {
    protocol: modelsResponseKind(input.protocol)
  })
  input.auditCapture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: 429,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody: JSON.stringify(responsePayload),
    responsePartType: 'gateway_error',
    errorPhase: 'request_validation',
    errorCode: 'public_models_rate_limited',
    errorMessage: responsePayload.error.message,
    firstTokenMs: Date.now() - input.startedAt
  })
}

function resolveGatewayModelsResponseProtocol(req: Request): ResponseProtocolCode | undefined {
  if (isGeminiModelsRequest(req)) {
    return 'gemini_v1beta'
  }
  if (isAnthropicModelsRequest(req) && isExplicitAnthropicModelsClient(req)) {
    return 'anthropic_v1'
  }
  if (isOpenAIModelsRequest(req) || isAnthropicModelsRequest(req)) {
    return 'openai_v1'
  }
  return undefined
}

function modelsResponseKind(protocol: ResponseProtocolCode): 'openai' | 'anthropic' | 'gemini' {
  if (protocol === 'anthropic_v1') return 'anthropic'
  if (protocol === 'gemini_v1beta') return 'gemini'
  return 'openai'
}

function isExplicitAnthropicModelsClient(req: Request): boolean {
  const profile = normalizedHeaderToken(req.header(gatewayClientProfileHeader))
  if (profile === 'anthropic' || profile === 'generic_anthropic' || profile === 'claude_code') {
    return true
  }
  return Boolean(
    normalizedHeaderToken(req.header('anthropic-version'))
      || normalizedHeaderToken(req.header('anthropic-beta'))
      || normalizedHeaderToken(req.header('x-claude-code-session-id'))
      || normalizedHeaderToken(req.header('x-claude-code-agent-id'))
      || claudeCodeUserAgent(req)
  )
}

function claudeCodeUserAgent(req: Request): boolean {
  const userAgent = lowerHeaderToken(req.header('user-agent'))
  return Boolean(userAgent && (userAgent.startsWith('claude-cli/') || userAgent.includes(' claude-cli/')))
}

function normalizedHeaderToken(value: unknown): string | undefined {
  return lowerHeaderToken(value)?.replace(/[-\s]+/g, '_')
}

function lowerHeaderToken(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim()
    ? value.trim().toLowerCase()
    : undefined
}

interface RecoverableOpenAICandidateState {
  accounts: UpstreamAccount[]
  recoverableAccounts: UpstreamAccount[]
}

async function waitForRecoverableOpenAIGatewayCandidateAccounts(input: {
  req: Request
  auditCapture: AuditCaptureContext
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  startedAt: number
  requestDeadlineAtMs: number
  signal?: AbortSignal
}): Promise<UpstreamAccount[]> {
  const requestedModel = requestModel(input.req)
  const requestedEndpointFamily = gatewayRequestEndpointFamily(input.req)
  const loadActiveAccounts = () => listFreshOpenAIAccountsForGroupAsync(input.groupId, input.systemAccountId, {
    requestedModel,
    requestedEndpointFamily
  })
  const loadRecoverableAccounts = () => listRecoverableUnavailableOpenAIAccountsForGroupAsync(input.groupId, input.systemAccountId, {
    requestedModel,
    requestedEndpointFamily,
    windowMs: recoverableUnavailableMaxWaitMs
  })
  const activeAccounts = await loadActiveAccounts()
  if (activeAccounts.length > 0) {
    return activeAccounts
  }
  const initialState: RecoverableOpenAICandidateState = {
    accounts: [],
    recoverableAccounts: await loadRecoverableAccounts()
  }
  if (initialState.recoverableAccounts.length === 0) {
    return []
  }
  const wait = await waitForRecoverableUnavailableState({
    scopeKey: recoverableCandidateScopeKey(input.systemAccountId, input.apiKeyId, input.groupId, requestedModel, requestedEndpointFamily),
    reason: 'account_cooldown_recoverable',
    initialState,
    refresh: async () => {
      const accounts = await loadActiveAccounts()
      return {
        accounts,
        recoverableAccounts: accounts.length > 0 ? [] : await loadRecoverableAccounts()
      }
    },
    isReady: (state) => state.accounts.length > 0,
    nextRetryAfterMs: (state) => nextRecoverableAccountRetryAfterMs(state.recoverableAccounts),
    auditCapture: input.auditCapture,
    requestStartedAtMs: input.startedAt,
    deadlineAtMs: input.requestDeadlineAtMs,
    signal: input.signal
  })
  return wait.state.accounts
}

function nextRecoverableAccountRetryAfterMs(accounts: UpstreamAccount[]): number | undefined {
  const now = Date.now()
  let nextRetryAfterMs: number | undefined
  for (const account of accounts) {
    if (!account.cooldownUntil) {
      continue
    }
    const cooldownUntilMs = Date.parse(account.cooldownUntil)
    if (!Number.isFinite(cooldownUntilMs)) {
      continue
    }
    const retryAfterMs = Math.max(0, cooldownUntilMs - now)
    nextRetryAfterMs = nextRetryAfterMs === undefined
      ? retryAfterMs
      : Math.min(nextRetryAfterMs, retryAfterMs)
  }
  return nextRetryAfterMs
}

function recoverableCandidateScopeKey(
  systemAccountId: string,
  apiKeyId: string | undefined,
  groupId: string,
  requestedModel: string | undefined,
  requestedEndpointFamily: ReturnType<typeof gatewayRequestEndpointFamily>
): string {
  return [systemAccountId, apiKeyId ?? '', groupId, requestedModel ?? '', requestedEndpointFamily ?? ''].join(':')
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
  groupFallbackApiKeyRecord?: GatewayApiKeyRow
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  trafficSource: OpenAIGatewayTrafficSource
  requestLane: OpenAIGatewayRequestLane
  requestClientCompatibility?: ClientCompatibilityCapability
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
      groupFallbackApiKeyRecord: input.groupFallbackApiKeyRecord ?? input.apiKeyRecord,
      candidateAccounts: candidate.accounts,
      responseInspectionPolicies: candidate.responseInspectionPolicies,
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

function mergeGatewaySettings(base: GatewaySettings, override?: Partial<GatewaySettings>): GatewaySettings {
  if (!override) return base
  return {
    gatewayTextRawBodyLimitMegabytes: override.gatewayTextRawBodyLimitMegabytes ?? base.gatewayTextRawBodyLimitMegabytes,
    defaultTemporaryUnschedulableMinutes: override.defaultTemporaryUnschedulableMinutes ?? base.defaultTemporaryUnschedulableMinutes,
    temporaryUnschedulableRetryIntervalSeconds: override.temporaryUnschedulableRetryIntervalSeconds ?? base.temporaryUnschedulableRetryIntervalSeconds,
    temporaryUnschedulableRetryAttempts: override.temporaryUnschedulableRetryAttempts ?? base.temporaryUnschedulableRetryAttempts,
    streamCircuitBreakerEnabled: true,
    textFirstResponseTimeoutSeconds: override.textFirstResponseTimeoutSeconds ?? base.textFirstResponseTimeoutSeconds,
    textStreamIdleTimeoutSeconds: override.textStreamIdleTimeoutSeconds ?? base.textStreamIdleTimeoutSeconds,
    textUncommittedAttemptMaxLifetimeSeconds: override.textUncommittedAttemptMaxLifetimeSeconds ?? base.textUncommittedAttemptMaxLifetimeSeconds,
    imageFirstResponseTimeoutSeconds: override.imageFirstResponseTimeoutSeconds ?? base.imageFirstResponseTimeoutSeconds,
    imageStreamIdleTimeoutSeconds: override.imageStreamIdleTimeoutSeconds ?? base.imageStreamIdleTimeoutSeconds,
    imageUncommittedAttemptMaxLifetimeSeconds: override.imageUncommittedAttemptMaxLifetimeSeconds ?? base.imageUncommittedAttemptMaxLifetimeSeconds,
    noAvailableAccountWaitTimeoutSeconds: override.noAvailableAccountWaitTimeoutSeconds ?? base.noAvailableAccountWaitTimeoutSeconds,
    streamFailureThresholdCount: override.streamFailureThresholdCount ?? base.streamFailureThresholdCount,
    streamFailureThresholdWindowMinutes: override.streamFailureThresholdWindowMinutes ?? base.streamFailureThresholdWindowMinutes
  }
}

async function sendClientIpErrorCircuitGatewayResponse(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  circuit: GatewayCircuitDecision
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
}): Promise<boolean> {
  if (!input.circuit.blocked) {
    return false
  }
  const statusCode = 429
  const responsePayload = gatewayErrorPayload('当前来源短时间错误过多，请稍后重试', 'rate_limit_exceeded', 'client_ip_error_circuit_open')
  if (input.circuit.retryAfterSeconds && !input.res.headersSent) {
    input.res.setHeader('Retry-After', String(input.circuit.retryAfterSeconds))
  }
  logger.warn({
    event: 'gateway_client_ip_error_circuit_blocked',
    reason: input.circuit.reason,
    retryAfterSeconds: input.circuit.retryAfterSeconds,
    failureCount: input.circuit.failureCount,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    clientIp: input.clientIp
  }, '客户端 IP 级错误熔断已短路请求')
  input.auditCapture.addGatewayMetadata({
    label: 'client_ip_error_circuit',
    metadata: {
      blocked: true,
      reason: input.circuit.reason,
      retryAfterSeconds: input.circuit.retryAfterSeconds,
      failureCount: input.circuit.failureCount
    }
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
      errorPhase: 'security',
      errorCode: 'client_ip_error_circuit_open',
      errorMessage: responsePayload.error.message
    }
  })
  return true
}

function hybridRouteFailureMessage(reason: string): string {
  if (reason === 'no_scoring_account') {
    return '混合路由评分模型暂不可用：绑定分组池没有可用评分账户'
  }
  if (reason === 'scoring_account_busy') {
    return '混合路由评分模型暂不可用：评分账户并发已满'
  }
  if (reason === 'hybrid_scoring_failed' || reason === 'hybrid_scoring_http_error') {
    return '混合路由评分模型调用失败'
  }
  if (reason === 'hybrid_level_route_missing') {
    return '混合路由等级配置不可用'
  }
  if (reason === 'hybrid_scoring_fallback_unavailable') {
    return '混合路由评分模型不可用，且低档兜底范围内没有可用目标模型'
  }
  if (reason === 'hybrid_target_group_unavailable') {
    return '混合路由目标分组暂不可用'
  }
  return '混合路由暂不可用'
}

function hybridRouteFailureStatusCode(reason: string): number {
  if (reason === 'hybrid_scoring_failed' || reason === 'hybrid_scoring_http_error') {
    return 502
  }
  return 503
}

function shouldDeferForcedImageGenerationToolPermissionToAnthropicBridge(input: {
  req: Request
  accounts?: UpstreamAccount[]
  requestClientCompatibility: ClientCompatibilityCapability
}): boolean {
  const sourceEndpointFamily = openAIRequestEndpointFamily(input.req)
  if (sourceEndpointFamily !== OPENAI_CHAT_COMPLETIONS_FAMILY && sourceEndpointFamily !== OPENAI_RESPONSES_FAMILY) {
    return false
  }
  const accounts = input.accounts ?? []
  if (!accounts.length) return false
  return accounts.every((account) => {
    if (!isAnthropicProtocolProfile(account)) return false
    const mapping = resolveOpenAIAccountModelMapping(account, requestModel(input.req), sourceEndpointFamily)
    return mapping?.upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
  })
}

export function buildGatewayUsageContext(input: {
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
    requestSnapshot,
    requestedServiceTier: requestSnapshot.requestedServiceTier ?? 'default',
    effectiveServiceTier: requestSnapshot.requestedServiceTier ?? 'default',
    requestedReasoningEffort: requestSnapshot.requestedReasoningEffort,
    effectiveReasoningEffort: requestSnapshot.requestedReasoningEffort
  }
}
