import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import { bindRequestContextFields, logRequestStage } from '../../../shared/request-context.js'
import { type GatewayApiKeyRow, type GroupUsageAccessMetadata, type OpenAIAccountsForGroupDiagnostics } from '../../../storage/repositories.js'
import {
  listCachedActiveResponseInspectionPoliciesForAccountsAsync,
  listCachedProviderModelCatalogAsync,
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
  isOpenAIGatewayImageGenerationModel,
  resolveOpenAIGatewayRequestLane,
  type OpenAIGatewayRequestLane
} from '../protocols/openai-v1/request-lane.js'
import {
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
  sendAuthenticatedModelsGatewayResponse,
  type OpenAIModelsResponseUsageContext,
  sendAnthropicModelsGatewayResponse,
  sendGeminiModelsGatewayResponse,
  sendOpenAIModelsGatewayResponse
} from '../response/fixed-responses.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import { gatewayErrorPayload, sendGatewayJsonError } from '../response/responses.js'
import {
  resolveGatewayApiKeyForModelsAsync,
  resolveGatewayRuntimeAsync,
  type GatewayRuntimeRequest
} from './pre-auth.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  geminiInteractionResourceIdFromRequest,
  isGeminiInteractionResourceRequest,
  resolveGeminiInteractionAffinityAsync,
  type GeminiInteractionAffinityBinding
} from '../protocols/gemini-v1beta/interaction-affinity.service.js'
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
import { codexCompactionExpectedForRequest } from '../response/codex-compaction-contract.js'
import { waitForRecoverableUnavailableState } from '../runtime/recoverable-unavailable-wait.js'
import { requestModel } from './metadata.js'
import { resolveGatewayModelsResponseProtocol } from './models-response-protocol.js'
import { gatewayRequestEndpointFamily, openAIRequestEndpointFamily, resolveOpenAIAccountModelMapping } from '../protocols/openai-v1/model-mapping.js'
import {
  consumeAuthenticatedModelsRateLimit,
  type AuthenticatedModelsRateLimitDecision
} from '../runtime/authenticated-models-rate-limit.service.js'
import {
  normalRouteFirstByteDeadlineAppliesToLane,
  normalRouteSpeedFirstAppliesToLane
} from '../policy/speed-first-lane.js'
import type { NormalRouteSpeedFirstRuntimeConfig } from '../runtime/normal-route-latency-degradation.service.js'
import { ServerRetryBudget } from '../runtime/server-retry-budget.js'
import {
  GatewayRequestAttemptTracker,
  GatewayRequestWallBudget,
  RouteCoordinationBudget,
  createGatewayRoutePlanSnapshot,
  type GatewayRouteCoordinatorOwner,
  type GatewayRouteFinalFailure,
  type GatewayRoutePlanSnapshot,
  type RouteCoordinationResult
} from '../routing/route-coordination.js'
import { GatewayDownstreamCommitState } from '../response/downstream-commit-state.js'
import { createGatewaySseWaitHeartbeatObserver } from '../response/sse-wait-heartbeat.js'
import {
  onceGatewayHotQualityExplorationSettlement,
  type GatewayHotQualityExplorationReservation
} from '../runtime/hot-quality-runtime.service.js'
import { gatewayAccountRuntimeKey } from '../runtime/account-runtime-keys.js'
import { defaultNormalRoutingConfig } from '../../../domain/route-strategy.js'
import type { NormalRouteFirstByteRuntimeConfig } from '../routing/normal-route-first-byte-deadline.js'

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
  forwardModelsRequestToUpstream?: boolean
  accountProbeModel?: string
  serverRetryBudget?: ServerRetryBudget
  gatewayRequestWallBudget?: GatewayRequestWallBudget
  routeCoordinationBudget?: RouteCoordinationBudget
  requestAttemptTracker?: GatewayRequestAttemptTracker
  downstreamCommitState?: GatewayDownstreamCommitState
  normalRouteFirstByteConfig?: NormalRouteFirstByteRuntimeConfig
  routePlanSnapshot?: GatewayRoutePlanSnapshot<string>
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
  normalRouteFirstByteConfig?: NormalRouteFirstByteRuntimeConfig
  normalRouteSpeedFirstConfig?: NormalRouteSpeedFirstRuntimeConfig
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
  apiKeyRecord?: GatewayApiKeyRow
  groupFallbackApiKeyRecord?: GatewayApiKeyRow
  hybridRoute?: HybridGatewayRuntimeRoute
  normalRouteLatencyDegradationApplied?: boolean
  codexTurnAccountAvoidanceApplied?: boolean
  codexTurnAvoidedAccountIds?: string[]
  precheckHalfOpenEligible?: boolean
  serverRetryBudget: ServerRetryBudget
  gatewayRequestWallBudget: GatewayRequestWallBudget
  routeCoordinationBudget: RouteCoordinationBudget
  requestAttemptTracker: GatewayRequestAttemptTracker
  downstreamCommitState: GatewayDownstreamCommitState
  routePlanSnapshot: GatewayRoutePlanSnapshot<string>
  interactionResourceAffinity?: GeminiInteractionAffinityBinding
  hotQualityExplorationReservation?: GatewayHotQualityExplorationReservation
  settleHotQualityExplorationAfterDispatch?: (outcome: 'dispatched' | 'not_dispatched') => Promise<void>
  releaseClientIpConcurrency: () => void
}

export interface OpenAIGatewayRouteAction {
  outcome: 'route_action'
  coordination: Exclude<RouteCoordinationResult<never>, { outcome: 'dispatchable' }>
  failure?: GatewayRouteFinalFailure
  usageContext: GatewayFailureUsageContext
  apiKeyRecord?: GatewayApiKeyRow
  groupFallbackApiKeyRecord?: GatewayApiKeyRow
  requestLane: OpenAIGatewayRequestLane
  clientStrategy: OpenAIGatewayClientStrategyContext
  serverRetryBudget: ServerRetryBudget
  gatewayRequestWallBudget: GatewayRequestWallBudget
  routeCoordinationBudget: RouteCoordinationBudget
  requestAttemptTracker: GatewayRequestAttemptTracker
  downstreamCommitState: GatewayDownstreamCommitState
  routePlanSnapshot: GatewayRoutePlanSnapshot<string>
  interactionResourceAffinity?: GeminiInteractionAffinityBinding
  normalRouteFirstByteConfig?: NormalRouteFirstByteRuntimeConfig
}

export function isOpenAIGatewayRouteAction(
  result: OpenAIGatewayDispatchContext | OpenAIGatewayRouteAction | undefined
): result is OpenAIGatewayRouteAction {
  return Boolean(result && 'outcome' in result && result.outcome === 'route_action')
}

function createOpenAIGatewayRoutePlanSnapshot(input: {
  traceId: string
  startedAt: number
  groupId: string
  apiKeyRecord?: GatewayApiKeyRow
  gatewayRequestWallBudget: GatewayRequestWallBudget
  normalRouteFirstByteConfig?: NormalRouteFirstByteRuntimeConfig
  hybridRoute?: HybridGatewayRuntimeRoute
}): GatewayRoutePlanSnapshot<string> {
  const orderedAllowedTargets = uniqueActiveRouteGroupIds(input.apiKeyRecord)
  if (!orderedAllowedTargets.includes(input.groupId)) orderedAllowedTargets.unshift(input.groupId)
  const cursor = Math.max(0, orderedAllowedTargets.indexOf(input.groupId))
  const selectedBinding = input.apiKeyRecord?.group_bindings?.find((binding) => binding.group_id === input.groupId)
  const hybridScoreDecision = input.hybridRoute
    ? Object.freeze({
        level: input.hybridRoute.scoring.level,
        targetModel: input.hybridRoute.targetModel,
        minLevel: input.hybridRoute.route.minLevel,
        maxLevel: input.hybridRoute.route.maxLevel,
        scoringDefaulted: input.hybridRoute.scoring.defaulted === true
      })
    : undefined
  return createGatewayRoutePlanSnapshot({
    routePlanId: `${input.traceId}:route`,
    mode: input.apiKeyRecord?.route_strategy_mode ?? 'normal',
    requestAcceptedAtMs: input.startedAt,
    gatewayRequestWallBudgetMs: input.gatewayRequestWallBudget.budgetMs,
    firstByteDeadlineMs: input.normalRouteFirstByteConfig?.firstByteDeadlineMs,
    requestPrecommitDeadlineAtMs: input.gatewayRequestWallBudget.deadlineAtMs,
    orderedAllowedTargets,
    cursor,
    weightedDecisionToken: input.apiKeyRecord?.route_strategy_mode === 'weighted'
      ? selectedBinding?.id ?? input.groupId
      : undefined,
    hybridScoreDecision
  })
}

export async function prepareOpenAIGatewayDispatchContext(
  input: PrepareOpenAIGatewayDispatchContextInput
): Promise<OpenAIGatewayDispatchContext | OpenAIGatewayRouteAction | undefined> {
  const { req, res, auditCapture, options, startedAt, traceId, clientIp, endpoint, requestSnapshot, signal } = input
  let gatewaySettings: GatewaySettings | undefined
  let apiKeyRecord: GatewayApiKeyRow | undefined = options.apiKeyRecord
  let groupFallbackApiKeyRecord: GatewayApiKeyRow | undefined = options.groupFallbackApiKeyRecord ?? apiKeyRecord
  let runtimeGroupAccess: GroupUsageAccessMetadata | undefined
  let runtimeAccounts: UpstreamAccount[] | undefined
  let runtimeAccountDispatchDiagnostics: OpenAIAccountsForGroupDiagnostics | undefined
  let runtimeResponseInspectionPolicies: ResponseInspectionPolicySummary[] | undefined
  let selectedHybridRoute: HybridGatewayRuntimeRoute | undefined
  const initialModelsResponseProtocol = resolveGatewayModelsResponseProtocol(req)

  if (initialModelsResponseProtocol && !options.identity && !options.forwardModelsRequestToUpstream) {
    const completed = await handleGatewayModelsRequestBeforeRequiredAuth({
      req,
      res,
      auditCapture,
      protocol: initialModelsResponseProtocol,
      startedAt,
      clientIp,
      traceId,
      endpoint
    })
    if (completed) {
      return undefined
    }
  }

  if (isDirectLoopbackDeploymentSmoke(req)) {
    const responsePayload = gatewayErrorPayload(
      '部署 smoke 已在网关本地完成，未派发上游',
      'invalid_request_error',
      'deployment_smoke_no_upstream'
    )
    await sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: buildGatewayUsageContext({
        traceId,
        clientIp,
        identity: {
          systemAccountId: 'deployment-smoke',
          groupId: 'deployment-smoke'
        },
        trafficSource: options.trafficSource ?? 'gateway',
        endpoint,
        requestSnapshot
      }),
      startedAt,
      statusCode: 400,
      responsePayload,
      recordUsage: false,
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'request_validation',
        errorCode: 'deployment_smoke_no_upstream',
        errorMessage: responsePayload.error.message
      }
    })
    return undefined
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
  const compactionTimeoutsDisabled = codexCompactionExpectedForRequest(req)
  let requestLane = options.requestLane ?? 'text'
  const serverRetryBudget = options.serverRetryBudget
    ?? new ServerRetryBudget(activeGatewaySettings.noAvailableAccountWaitTimeoutSeconds * 1000)
  options.serverRetryBudget = serverRetryBudget
  let gatewayRequestWallBudget = options.gatewayRequestWallBudget
    ?? new GatewayRequestWallBudget({
      requestAcceptedAtMs: startedAt,
      unbounded: compactionTimeoutsDisabled,
      budgetMs: requestLane === 'image'
        ? activeGatewaySettings.imageRequestWallTimeoutSeconds * 1000
        : undefined
    })
  if (compactionTimeoutsDisabled) {
    gatewayRequestWallBudget = gatewayRequestWallBudget.withoutLimit()
  }
  if (requestLane === 'image') {
    gatewayRequestWallBudget = gatewayRequestWallBudget.withMinimumBudgetMs(
      activeGatewaySettings.imageRequestWallTimeoutSeconds * 1000
    )
  }
  options.gatewayRequestWallBudget = gatewayRequestWallBudget
  const routeCoordinationBudget = options.routeCoordinationBudget
    ?? new RouteCoordinationBudget({ requestId: traceId })
  options.routeCoordinationBudget = routeCoordinationBudget
  const requestAttemptTracker = options.requestAttemptTracker ?? new GatewayRequestAttemptTracker()
  options.requestAttemptTracker = requestAttemptTracker
  const downstreamCommitState = options.downstreamCommitState ?? new GatewayDownstreamCommitState()
  options.downstreamCommitState = downstreamCommitState
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
  let interactionResourceAffinity: GeminiInteractionAffinityBinding | undefined
  const interactionResourceRequest = isGeminiInteractionResourceRequest(req)
  const interactionResourceId = geminiInteractionResourceIdFromRequest(req)
  if (interactionResourceRequest && !interactionResourceId) {
    await sendInteractionAffinityFailure({
      req,
      res,
      auditCapture,
      usageContext: currentGroupUsageContext(),
      startedAt,
      statusCode: 400,
      code: 'interaction_id_invalid',
      message: 'Interaction 资源 ID 无效'
    })
    return undefined
  }
  if (interactionResourceId) {
    try {
      interactionResourceAffinity = await resolveGeminiInteractionAffinityAsync({
        req,
        scope: { systemAccountId, apiKeyId, groupId }
      })
    } catch (error) {
      logger.warn({
        event: 'gateway_gemini_interaction_affinity_lookup_failed',
        interactionId: interactionResourceId,
        errorMessage: error instanceof Error ? error.message : String(error)
      }, 'Gemini Interaction 账号亲和状态读取失败')
      await sendInteractionAffinityFailure({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext(),
        startedAt,
        statusCode: 503,
        code: 'interaction_affinity_unavailable',
        message: 'Interaction 资源路由状态暂不可用，请稍后重试'
      })
      return undefined
    }
    if (!interactionResourceAffinity) {
      await sendInteractionAffinityFailure({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext(),
        startedAt,
        statusCode: 404,
        code: 'interaction_affinity_not_found',
        message: '未找到该 Interaction 的本地账号路由记录'
      })
      return undefined
    }
    const affinityGroupStillBound = !apiKeyId || !apiKeyRecord || apiKeyRecord.group_bindings?.some((binding) => (
      binding.status === 'active' && binding.group_id === interactionResourceAffinity?.groupId
    ))
    if (!affinityGroupStillBound) {
      await sendInteractionAffinityFailure({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext(),
        startedAt,
        statusCode: 404,
        code: 'interaction_affinity_not_found',
        message: '该 Interaction 所属分组已不再绑定当前 API Key'
      })
      return undefined
    }

    groupId = interactionResourceAffinity.groupId
    identity = { systemAccountId, apiKeyId, groupId }
    runtimeGroupAccess = await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
    const affinityCandidates = options.candidateAccounts
      ?? await listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId)
    runtimeAccounts = affinityCandidates.filter((account) => (
      account.id === interactionResourceAffinity?.accountId
      && account.providerCode === interactionResourceAffinity.providerCode
      && (!interactionResourceAffinity.providerProtocolProfileId
        || account.providerProtocolProfileId === interactionResourceAffinity.providerProtocolProfileId)
    ))
    runtimeAccountDispatchDiagnostics = undefined
    if (runtimeAccounts.length !== 1) {
      await sendInteractionAffinityFailure({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext({ groupId, groupAccess: runtimeGroupAccess }),
        startedAt,
        statusCode: 409,
        code: 'interaction_affinity_account_unavailable',
        message: '创建该 Interaction 的上游账号当前不可用'
      })
      return undefined
    }
    auditCapture.bindContext({ groupId, providerCode: interactionResourceAffinity.providerCode })
    bindRequestContextFields({ systemAccountId, apiKeyId, groupId, trafficSource })
    auditCapture.addGatewayMetadata({
      label: 'gemini_interaction_account_affinity',
      metadata: {
        interactionId: interactionResourceId,
        groupId,
        accountId: interactionResourceAffinity.accountId
      }
    })
  }
  const initialClientStrategy = resolveOpenAIGatewayClientStrategy(req, {
    systemAccountId,
    apiKeyId,
    groupId,
    endpoint
  })
  if (!interactionResourceAffinity && !initialModelsResponseProtocol && !options.identity && trafficSource === 'gateway' && apiKeyRecord && apiKeyRecord.route_strategy_mode !== 'hybrid_smart') {
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
      runtimeResponseInspectionPolicies = undefined
      options.responseInspectionPolicies = undefined
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

  if (!interactionResourceAffinity && !initialModelsResponseProtocol && !options.identity && trafficSource === 'gateway' && apiKeyRecord?.route_strategy_mode === 'hybrid_smart') {
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
      const failureGroupAccess = runtimeGroupAccess
        ?? await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
      await sendGatewayFailureResponse({
        req,
        res,
        auditCapture,
        usageContext: currentGroupUsageContext({ groupId, groupAccess: failureGroupAccess }),
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
      requestLane = resolveOpenAIGatewayRequestLane(req)
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
      runtimeResponseInspectionPolicies = undefined
      options.responseInspectionPolicies = undefined
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

  const groupAccessStartedAt = performance.now()
  const groupAccess = runtimeGroupAccess ?? await resolveCachedGroupUsageAccessMetadataAsync(groupId, systemAccountId)
  logRequestStage('route.group_access', {
    traceId,
    groupId,
    systemAccountId,
    apiKeyId,
    providerCode: groupAccess?.providerCode,
    resolved: Boolean(groupAccess),
    ...(groupAccess ? {} : {
      failureReason: 'group_access_unavailable',
      decisionInputs: { groupId, systemAccountId, apiKeyId }
    })
  }, groupAccess ? 'success' : 'expected_failure', groupAccessStartedAt)
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
  logRequestStage('client.profile', {
    traceId,
    clientProfile: clientStrategy.clientProfile,
    downstreamProtocol: clientStrategy.downstreamProtocol,
    requestClientCompatibility: clientStrategy.requestClientCompatibility
  })
  logRequestStage('protocol.bridge', {
    traceId,
    downstreamProtocol: clientStrategy.downstreamProtocol,
    clientProfile: clientStrategy.clientProfile,
    providerCode: groupAccess?.providerCode,
    bridgeDecisionDeferredToAccount: true
  })
  serverRetryBudget.setWaitObserver(createGatewaySseWaitHeartbeatObserver({
    res,
    downstreamProtocol: clientStrategy.downstreamProtocol,
    downstreamCommitState,
    signal
  }))
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
      await sendAuthenticatedModelsRateLimitFailure({
        req,
        res,
        auditCapture,
        usageContext,
        startedAt,
        decision: authenticatedModelsRateLimitDecision
      })
      return undefined
    }
  }

  if (modelsResponseProtocol && !options.forwardModelsRequestToUpstream) {
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
      providerCodes: gatewayModelsProviderCodes({ apiKeyRecord }),
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
  const accountLoadStartedAt = performance.now()
  const rawCandidateAccounts = options.candidateAccounts ?? runtimeAccounts ?? await listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId)
  logRequestStage('account.load_candidates', {
    traceId,
    groupId,
    source: options.candidateAccounts ? 'provided' : runtimeAccounts ? 'runtime' : 'cache',
    candidateAccountCount: rawCandidateAccounts.length
  }, 'success', accountLoadStartedAt)
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
  let routePlanSnapshot = options.routePlanSnapshot ?? createOpenAIGatewayRoutePlanSnapshot({
    traceId,
    startedAt,
    groupId,
    apiKeyRecord: groupFallbackApiKeyRecord ?? apiKeyRecord,
    gatewayRequestWallBudget,
    normalRouteFirstByteConfig: normalRouteFirstByteDeadlineAppliesToLane(requestLane)
      ? options.normalRouteFirstByteConfig ?? normalRouteFirstByteConfigForApiKey(apiKeyRecord)
      : undefined,
    hybridRoute: selectedHybridRoute
  })
  let pendingRouteReason: string | undefined
  let pendingRouteFailure: GatewayRouteFinalFailure | undefined
  const buildRouteAction = (reason = pendingRouteReason ?? pendingRouteFailure?.errorCode ?? 'route_unavailable'): OpenAIGatewayRouteAction => ({
    outcome: 'route_action',
    coordination: pendingRouteFailure && isTemporarilyBlockedRouteFailure(pendingRouteFailure)
      ? {
          outcome: 'temporarily_blocked',
          reason,
          earliestRetryAtMs: pendingRouteFailure.retryAfterMs === undefined ? undefined : Date.now() + pendingRouteFailure.retryAfterMs,
          confirmationInFlight: false,
          blockedAccountIds: [],
          waitableByCurrentRequest: false,
          foreignLeaseInFlight: false
        }
      : { outcome: 'hard_exhausted', reason },
    failure: pendingRouteFailure,
    usageContext,
    apiKeyRecord,
    groupFallbackApiKeyRecord,
    requestLane,
    clientStrategy,
    serverRetryBudget,
    gatewayRequestWallBudget,
    routeCoordinationBudget,
    requestAttemptTracker,
    downstreamCommitState,
    routePlanSnapshot,
    interactionResourceAffinity,
    normalRouteFirstByteConfig: normalRouteFirstByteDeadlineAppliesToLane(requestLane)
      ? options.normalRouteFirstByteConfig ?? normalRouteFirstByteConfigForApiKey(apiKeyRecord)
      : undefined
  })
  // Candidate and preparation layers only report route actions. The HTTP route
  // loop owns group switching and the eventual terminal response.
  const routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext> = {
    requestFallback: interactionResourceAffinity
      ? async () => ({ attempted: false })
      : async (reason) => {
          // Only advertise fallback when the route plan still has a concrete
          // later target. A previous group is not a fallback candidate: on the
          // last group, capacity waiting must stay in this group so the
          // high-concurrency queue can wake on a released account slot.
          if (!canAttemptApiKeyGroupFallback(apiKeyRecord, groupId, routePlanSnapshot)) {
            return { attempted: false }
          }
          const candidate = await resolveNextApiKeyGroupFallbackCandidate({
            req,
            reason,
            apiKeyRecord,
            systemAccountId,
            groupId,
            requestLane,
            requestClientCompatibility: clientStrategy.requestClientCompatibility,
            routePlanSnapshot
          })
          if (!candidate) {
            return { attempted: false }
          }
          pendingRouteReason = reason
          return { attempted: true }
        },
    async completeFailure(failure) {
      pendingRouteFailure = failure
    }
  }
  const candidateFilterStartedAt = performance.now()
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
    bypassModelFilter: interactionResourceAffinity !== undefined || options.forwardModelsRequestToUpstream,
    requestModelOverride: options.forwardModelsRequestToUpstream ? options.accountProbeModel : undefined,
    loadModelAwareCandidateAccounts: options.candidateAccounts || interactionResourceAffinity
      ? undefined
      : (model, sourceEndpointFamily) => listCachedOpenAIAccountsForGroupAsync(groupId, systemAccountId, { requestedModel: model, requestedEndpointFamily: sourceEndpointFamily }),
    recoverUnavailableCandidateAccounts: options.candidateAccounts || interactionResourceAffinity
      ? undefined
      : () => waitForRecoverableOpenAIGatewayCandidateAccounts({
        req,
        auditCapture,
        systemAccountId,
        apiKeyId,
        groupId,
        startedAt,
        serverRetryBudget,
        routeCoordinationBudget,
        gatewayRequestWallBudget,
        signal
      }),
    routeCoordinator,
  })
  logRequestStage('model.capability_filter', {
    traceId,
    groupId,
    filterOutcome: candidateFilter.outcome,
    candidateAccountCount: candidateFilter.outcome === 'accounts' ? candidateFilter.accounts.length : 0,
    requestedModel: requestModel(req),
    endpointFamily: gatewayRequestEndpointFamily(req),
    ...(candidateFilter.outcome === 'accounts' ? {} : {
      failureReason: `model_capability_filter_${candidateFilter.outcome}`,
      decisionInputs: {
        requestedModel: requestModel(req),
        endpointFamily: gatewayRequestEndpointFamily(req),
        candidateAccountCount: rawCandidateAccounts.length
      }
    })
  }, candidateFilter.outcome === 'accounts' ? 'success' : 'expected_failure', candidateFilterStartedAt)
  if (candidateFilter.outcome === 'fallback') {
    return buildRouteAction(candidateFilter.reason)
  }
  if (candidateFilter.outcome === 'completed') {
    return pendingRouteFailure ? buildRouteAction() : undefined
  }
  if (
    requestLane !== 'image'
    && await accountModelsTargetImage(req, candidateFilter.accounts, systemAccountId)
  ) {
    requestLane = 'image'
  }
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
  if (requestLane === 'image') {
    gatewayRequestWallBudget = gatewayRequestWallBudget.withMinimumBudgetMs(
      activeGatewaySettings.imageRequestWallTimeoutSeconds * 1000
    )
    options.gatewayRequestWallBudget = gatewayRequestWallBudget
    routePlanSnapshot = createGatewayRoutePlanSnapshot({
      ...routePlanSnapshot,
      gatewayRequestWallBudgetMs: gatewayRequestWallBudget.budgetMs,
      firstByteDeadlineMs: undefined,
      requestPrecommitDeadlineAtMs: gatewayRequestWallBudget.deadlineAtMs,
      orderedAllowedTargets: routePlanSnapshot.orderedAllowedTargets
    })
    options.routePlanSnapshot = routePlanSnapshot
  }
  const normalRouteFirstByteConfig = !compactionTimeoutsDisabled
    && normalRouteFirstByteDeadlineAppliesToLane(requestLane)
    ? options.normalRouteFirstByteConfig ?? normalRouteFirstByteConfigForApiKey(apiKeyRecord)
    : undefined
  const normalRouteSpeedFirstConfig = !compactionTimeoutsDisabled
    && normalRouteSpeedFirstAppliesToLane(requestLane)
    ? normalRouteSpeedFirstConfigForApiKey(apiKeyRecord)
    : undefined
  if (compactionTimeoutsDisabled) {
    auditCapture.addGatewayMetadata({
      label: 'codex_compaction_timeouts_disabled',
      metadata: {
        requestLane,
        wallBudgetDisabled: true,
        firstResponseTimeoutsDisabled: true,
        firstOutputTimeoutsDisabled: true,
        attemptLifetimeDisabled: true,
        rawStreamIdleTimeoutRetained: true
      }
    })
  }

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
    serverRetryBudget,
    routeCoordinationBudget,
    gatewayRequestWallBudget,
    signal,
    ignoreAccountRuntimeSuppression: options.ignoreAccountRuntimeSuppression === true,
    routeCoordinator,
  })
  if (dispatchPreparation.outcome === 'fallback') {
    return buildRouteAction(dispatchPreparation.reason)
  }
  if (dispatchPreparation.outcome === 'completed') {
    return pendingRouteFailure ? buildRouteAction() : undefined
  }
  const settleHotQualityExplorationAfterDispatch = dispatchPreparation.settleHotQualityExplorationAfterDispatch
    ? onceGatewayHotQualityExplorationSettlement(dispatchPreparation.settleHotQualityExplorationAfterDispatch)
    : undefined

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
    requestCoordination: {
      scope: 'gateway_request',
      timeoutPolicy: compactionTimeoutsDisabled ? 'codex_compaction_unbounded' : undefined,
      serverRetryBudget,
      gatewayRequestWallBudget,
      routeCoordinationBudget,
      requestAttemptTracker
    },
    onDispatchedAccount: async (account) => settleHotQualityExplorationAfterDispatch?.(
      dispatchPreparation.hotQualityExplorationReservation?.accountRuntimeKey === gatewayAccountRuntimeKey(account)
        ? 'dispatched'
        : 'not_dispatched'
    ),
    signal
  })
  if (codexBridgeCompactPreflight.outcome === 'completed') {
    await settleHotQualityExplorationAfterDispatch?.('not_dispatched')
    dispatchPreparation.releaseClientIpConcurrency()
    return undefined
  }
  try {
    runtimeResponseInspectionPolicies = await listCachedActiveResponseInspectionPoliciesForAccountsAsync(codexBridgeCompactPreflight.accounts)
  } catch (error) {
    await settleHotQualityExplorationAfterDispatch?.('not_dispatched')
    dispatchPreparation.releaseClientIpConcurrency()
    throw error
  }
  options.responseInspectionPolicies = runtimeResponseInspectionPolicies

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
    normalRouteFirstByteConfig,
    normalRouteSpeedFirstConfig,
    responseInspectionPolicies: runtimeResponseInspectionPolicies ?? [],
    apiKeyRecord,
    groupFallbackApiKeyRecord,
    hybridRoute: selectedHybridRoute,
    normalRouteLatencyDegradationApplied: dispatchPreparation.normalRouteLatencyDegradationApplied,
    codexTurnAccountAvoidanceApplied: dispatchPreparation.codexTurnAccountAvoidanceApplied,
    codexTurnAvoidedAccountIds: dispatchPreparation.codexTurnAvoidedAccountIds,
    precheckHalfOpenEligible: dispatchPreparation.precheckHalfOpenEligible,
    serverRetryBudget,
    gatewayRequestWallBudget,
    routeCoordinationBudget,
    requestAttemptTracker,
    downstreamCommitState,
    routePlanSnapshot,
    interactionResourceAffinity,
    hotQualityExplorationReservation: dispatchPreparation.hotQualityExplorationReservation,
    settleHotQualityExplorationAfterDispatch,
    releaseClientIpConcurrency: dispatchPreparation.releaseClientIpConcurrency
  }
}

function isDirectLoopbackDeploymentSmoke(req: Request): boolean {
  if (req.header('x-juhe-deployment-smoke') !== 'no-upstream' || req.header('x-forwarded-for')) return false
  const remoteAddress = req.socket.remoteAddress
  return remoteAddress === '127.0.0.1' || remoteAddress === '::1' || remoteAddress === '::ffff:127.0.0.1'
}

function normalRouteSpeedFirstConfigForApiKey(apiKeyRecord: GatewayApiKeyRow | undefined): NormalRouteSpeedFirstRuntimeConfig | undefined {
  if (apiKeyRecord?.route_strategy_mode !== 'normal') return undefined
  const normalConfig = apiKeyRecord.normal_routing_config
  if (normalConfig?.schedulingPreference !== 'speed_first') return undefined
  if (!normalConfig.speedFirstConfig) return undefined
  return {
    ...normalConfig.speedFirstConfig,
    firstByteDeadlineMs: normalConfig.firstByteDeadlineMs
  }
}

function normalRouteFirstByteConfigForApiKey(apiKeyRecord: GatewayApiKeyRow | undefined): NormalRouteFirstByteRuntimeConfig | undefined {
  if (apiKeyRecord?.route_strategy_mode !== 'normal') return undefined
  const normalConfig = apiKeyRecord.normal_routing_config ?? defaultNormalRoutingConfig()
  if (normalConfig.schedulingPreference !== 'speed_first') return undefined
  return {
    schedulingPreference: 'speed_first',
    firstByteDeadlineMs: normalConfig.firstByteDeadlineMs
  }
}

async function handleGatewayModelsRequestBeforeRequiredAuth(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  protocol: ResponseProtocolCode
  startedAt: number
  clientIp?: string
  traceId: string
  endpoint: string
}): Promise<boolean> {
  const apiKey = await resolveGatewayApiKeyForModelsAsync(input.req as GatewayRuntimeRequest, input.res, {
    inspectClientIpPolicyAfterRuntime: false
  })
  if (!apiKey) {
    finalizeGatewayAuthFailureAudit(input.req, input.res, input.auditCapture)
    return true
  }
  const usageContext: OpenAIModelsResponseUsageContext = {
    traceId: input.traceId,
    trafficSource: 'gateway',
    clientIp: input.clientIp,
    systemAccountId: apiKey.system_account_id,
    apiKeyId: apiKey.id,
    endpoint: input.endpoint
  }
  input.auditCapture.bindContext({
    systemAccountId: apiKey.system_account_id,
    apiKeyId: apiKey.id,
    groupId: apiKey.selected_group_id,
    trafficSource: 'gateway'
  })
  const rateLimitDecision = await consumeAuthenticatedModelsRateLimit({
    apiKeyId: apiKey.id,
    clientIp: input.clientIp
  })
  if (!rateLimitDecision.allowed) {
    await sendAuthenticatedModelsRateLimitFailure({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext,
      startedAt: input.startedAt,
      decision: rateLimitDecision
    })
    return true
  }
  await sendAuthenticatedModelsGatewayResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext,
    providerCodes: gatewayModelsProviderCodes({ apiKeyRecord: apiKey }),
    protocol: modelsResponseKind(input.protocol),
    startedAt: input.startedAt
  })
  return true
}

async function sendAuthenticatedModelsRateLimitFailure(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext | OpenAIModelsResponseUsageContext
  startedAt: number
  decision: AuthenticatedModelsRateLimitDecision
}): Promise<void> {
  const limiterUnavailable = input.decision.unavailable === true
  const statusCode = limiterUnavailable ? 503 : 429
  const errorCode = limiterUnavailable
    ? 'authenticated_models_rate_limit_unavailable'
    : 'authenticated_models_rate_limited'
  const retryAfterSeconds = input.decision.retryAfterSeconds ?? (limiterUnavailable ? 5 : 1)
  if (!input.res.headersSent) input.res.setHeader('Retry-After', String(retryAfterSeconds))
  input.auditCapture.addGatewayMetadata({
    label: 'authenticated_models_rate_limit',
    metadata: {
      scope: input.decision.scope,
      limit: input.decision.limit,
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
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext as GatewayFailureUsageContext,
    startedAt: input.startedAt,
    statusCode,
    responsePayload,
    audit: {
      outcome: 'gateway_failed',
      errorPhase: limiterUnavailable ? 'security' : 'request_validation',
      errorCode,
      errorMessage: responsePayload.error.message
    }
  })
}

function gatewayModelsProviderCodes(input: {
  apiKeyRecord?: GatewayApiKeyRow
}): string[] {
  const providerCodes = new Set<string>()
  const bindings = input.apiKeyRecord?.group_bindings?.filter((binding) => binding.status === 'active') ?? []
  for (const binding of bindings) {
    const providerCode = binding.provider_code?.trim()
    if (providerCode) providerCodes.add(providerCode)
  }
  return [...providerCodes]
}

function modelsResponseKind(protocol: ResponseProtocolCode): 'openai' | 'anthropic' | 'gemini' {
  if (protocol === 'anthropic_v1') return 'anthropic'
  if (protocol === 'gemini_v1beta') return 'gemini'
  return 'openai'
}

async function sendInteractionAffinityFailure(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  statusCode: number
  code: string
  message: string
}): Promise<void> {
  const responsePayload = gatewayErrorPayload(input.message, 'invalid_request_error', input.code)
  await sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode: input.statusCode,
    responsePayload,
    audit: {
      outcome: 'gateway_failed',
      errorPhase: input.statusCode >= 500 ? 'dispatch' : 'request_validation',
      errorCode: input.code,
      errorMessage: input.message
    }
  })
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
  serverRetryBudget: ServerRetryBudget
  routeCoordinationBudget: RouteCoordinationBudget
  gatewayRequestWallBudget: GatewayRequestWallBudget
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
    windowMs: input.serverRetryBudget.remainingMs()
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
  const waitStartedAtMs = Date.now()
  const deadlineAtMs = input.serverRetryBudget.deadlineAtMs(waitStartedAtMs)
  try {
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
      maxWaitMs: input.serverRetryBudget.remainingMs(waitStartedAtMs),
      requestStartedAtMs: waitStartedAtMs,
      deadlineAtMs,
      routeCoordinationBudget: input.routeCoordinationBudget,
      gatewayRequestWallBudget: input.gatewayRequestWallBudget,
      signal: input.signal
    })
    return wait.state.accounts
  } finally {
    input.serverRetryBudget.pauseNoAvailableWait()
  }
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
  routePlanSnapshot: GatewayRoutePlanSnapshot<string>
}

interface ApiKeyGroupFallbackDispatchResult {
  attempted: boolean
  context?: OpenAIGatewayDispatchContext | OpenAIGatewayRouteAction
}

export async function prepareApiKeyGroupFallbackDispatchContext(
  input: ApiKeyGroupFallbackDispatchInput
): Promise<ApiKeyGroupFallbackDispatchResult> {
  if (!canAttemptApiKeyGroupFallback(input.apiKeyRecord, input.groupId, input.routePlanSnapshot)) {
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
      requestLane: input.requestLane,
      routePlanSnapshot: candidate.routePlanSnapshot ?? input.routePlanSnapshot
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

function uniqueActiveRouteGroupIds(apiKeyRecord: GatewayApiKeyRow | undefined): string[] {
  const result: string[] = []
  const seen = new Set<string>()
  for (const binding of apiKeyRecord?.group_bindings ?? []) {
    const groupId = binding.group_id?.trim()
    if (!groupId || binding.status !== 'active' || binding.group_enabled === 0 || seen.has(groupId)) continue
    seen.add(groupId)
    result.push(groupId)
  }
  return result
}

function mergeGatewaySettings(base: GatewaySettings, override?: Partial<GatewaySettings>): GatewaySettings {
  if (!override) return base
  return {
    gatewayTextRawBodyLimitMegabytes: override.gatewayTextRawBodyLimitMegabytes ?? base.gatewayTextRawBodyLimitMegabytes,
    accountCircuitConfirmationFailuresRequired: override.accountCircuitConfirmationFailuresRequired ?? base.accountCircuitConfirmationFailuresRequired,
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
    imageRequestWallTimeoutSeconds: override.imageRequestWallTimeoutSeconds ?? base.imageRequestWallTimeoutSeconds,
    noAvailableAccountWaitTimeoutSeconds: override.noAvailableAccountWaitTimeoutSeconds ?? base.noAvailableAccountWaitTimeoutSeconds,
    streamFailureThresholdCount: override.streamFailureThresholdCount ?? base.streamFailureThresholdCount,
    streamFailureThresholdWindowMinutes: override.streamFailureThresholdWindowMinutes ?? base.streamFailureThresholdWindowMinutes
  }
}

function isTemporarilyBlockedRouteFailure(failure: GatewayRouteFinalFailure): boolean {
  return failure.retryAfterMs !== undefined
    || failure.failureAttribution === 'gateway_capacity'
    || failure.statusCode === 429
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

async function accountModelsTargetImage(
  req: Request,
  accounts: UpstreamAccount[],
  systemAccountId: string
): Promise<boolean> {
  const sourceEndpointFamily = gatewayRequestEndpointFamily(req)
  const requestedModel = requestModel(req)
  const mappedModelCatalogScopes = new Map<string, {
    providerCode: string
    systemAccountId: string
    models: Set<string>
  }>()
  for (const account of accounts) {
    const upstreamModel = resolveOpenAIAccountModelMapping(account, requestedModel, sourceEndpointFamily)?.upstreamModel
      ?? requestedModel
    if (!upstreamModel) continue
    if (isOpenAIGatewayImageGenerationModel(upstreamModel)) return true
    const providerCode = account.providerCode.trim()
    if (!providerCode) continue
    const catalogOwnerSystemAccountId = account.accountOwnerSystemAccountId?.trim() || systemAccountId
    const catalogScopeKey = `${providerCode}\u0000${catalogOwnerSystemAccountId}`
    const scope = mappedModelCatalogScopes.get(catalogScopeKey) ?? {
      providerCode,
      systemAccountId: catalogOwnerSystemAccountId,
      models: new Set<string>()
    }
    scope.models.add(upstreamModel.trim())
    mappedModelCatalogScopes.set(catalogScopeKey, scope)
  }
  if (mappedModelCatalogScopes.size === 0) return false

  const catalogs = await Promise.all([...mappedModelCatalogScopes.values()].map(async ({ providerCode, systemAccountId, models }) => ({
    models,
    items: await listCachedProviderModelCatalogAsync({
      providerCode,
      systemAccountId,
      includeUnpriced: true
    })
  })))
  return catalogs.some(({ models, items }) => items.some((item) => (
    models.has(item.model.trim())
    && (
      item.supportedApiProtocols.includes('images')
      || item.outputModalities?.includes('image') === true
      || item.imageOutputUsdPer1M !== undefined
      || item.outputUsdPerImage !== undefined
    )
  )))
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
