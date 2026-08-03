import {
  Router,
  type NextFunction,
  type Request,
  type Response,
} from 'express'

import { createTraceId, getRequestLogger, getTraceId, logRequestStage } from '../../shared/request-context.js'
import { errorLogFields } from '../../shared/logger.js'
import {
  extractClientIp,
  requestEndpoint,
  requestModel,
  requestStream
} from './request/metadata.js'
import {
  buildUsageRequestSnapshot
} from './usage/snapshots.js'
import {
  isEffectiveOpenAIStreamRequest
} from './upstream/request.js'
import {
  gatewayStreamClientRetryErrorCode,
  gatewayStreamClientRetryMessage,
  gatewayErrorPayload,
  sendGatewayErrorResponse,
  shouldHandleOpenAIUpstreamResponseAsStream,
  writeGatewayStreamFailureEvent
} from './response/responses.js'
import { classifyGatewayDispatchExhaustion } from './response/dispatch-exhaustion-classifier.js'
import {
  createAuditCapture,
  observeGatewayHttpCompletion,
  responseHeadersToObject
} from './audit/capture.service.js'
import {
  type UpstreamAccount
} from './protocols/openai-v1/route-helpers.js'
import {
  buildDiagnosticUpstreamError
} from './upstream/error-helpers.js'
import {
  persistOpenAICodexHeadersIfNeeded
} from './runtime/account-effects.js'
import {
  applyHandledUpstreamRoutingEffects,
  finalizeHandledUpstreamResponse,
  handleNonStreamUpstreamResponse,
  handleStreamUpstreamResponse,
  type StreamServerRetryReason
} from './response/finalization.js'
import { observeGatewayRouting } from './observability/routing-observability.service.js'
import { rememberCodexTurnStreamFailureAsync } from './client-profiles/codex-turn-retry.service.js'
import { sendGatewayFailureResponse } from './response/failure-response.js'
import { createGatewaySseWaitHeartbeat } from './response/sse-wait-heartbeat.js'
import type { GatewayDownstreamCommitState } from './response/downstream-commit-state.js'
import { handleUpstreamRequestError } from './response/failure-dispatch.js'
import { handleGatewayRequestKnownErrorResponse } from './request/error-response.js'
import {
  isOpenAIGatewayRouteAction,
  prepareOpenAIGatewayDispatchContext,
  prepareApiKeyGroupFallbackDispatchContext,
  type OpenAIGatewayDispatchContext,
  type OpenAIGatewayRouteAction,
  type OpenAIGatewayRequestIdentity
} from './request/preflight.js'
import { resolveNextHybridGatewayRoute } from './hybrid/routing.service.js'
import { appendHybridQualityRepairInstruction } from './hybrid/quality-repair.service.js'
import {
  fetchFirstAvailableUpstream,
  GatewayRequestWallBudgetExhaustedError,
  NormalRouteFirstByteCutoverError,
  UpstreamAttemptError,
  type GatewayUpstreamRequestCoordinationContext
} from './dispatch/upstream-dispatch.js'
import type { UpstreamAttempt } from './upstream/attempt.js'
import type { ResponseInspectionDecision } from './response/inspection.js'
import type { GatewaySettings } from './policy/account-error-policy.service.js'
import { OpenAIOAuthCodexAdapterError } from './adapters/gpt-codex/oauth-adapter.js'
import { recordClientIpErrorCircuitSampleAsync } from './runtime/client-ip-error-circuit.service.js'
import {
  confirmClientIpAccountAvoidanceAfterFinalFailureAsync,
  transferClientIpAccountPendingFailures
} from './runtime/client-ip-account-avoidance.service.js'
import {
  type GatewayFailureUsageContext
} from './usage/records.js'
import {
  isGatewayForcedDownstreamClose,
  isProvenUpstreamBodyTransportError,
  markGatewayForcedDownstreamClose
} from './upstream/body.js'
import {
  isAccountDiagnosticTrafficSource,
  isAccountProbeTrafficSource,
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from './usage/traffic-source.js'
import { resolveOpenAIGatewayRequestLane } from './protocols/openai-v1/request-lane.js'
import { forgetOpenAIAccountForSessionAsync } from './runtime/session-affinity.service.js'
import { gatewayProtocolClientErrorProtocolForRequest } from './protocols/registry.js'
import { gatewayClientAllowsUpstreamSemanticInterpretation } from './client-profiles/strategy.js'
import { gatewayRequestAbortSource } from './request/abort-attribution.js'
import {
  normalRouteLatencyDegradationScope,
  isNormalRouteAccountLatencyDegradedAsync,
  recordNormalRouteFirstByteSlowAsync,
  recordNormalRouteFirstByteSuccessAsync,
  type NormalRouteLatencySlowResult,
  type NormalRouteSpeedFirstRuntimeConfig
} from './runtime/normal-route-latency-degradation.service.js'
import {
  reserveSpeedFirstCutoverTarget,
  type SpeedFirstCutoverReservation
} from './runtime/speed-first-cutover-reservation.service.js'
import { gatewayAccountConcurrencyAccountId } from './dispatch/account-concurrency-identity.js'
import { gatewayAccountRuntimeKey } from './runtime/account-runtime-keys.js'
import {
  getGatewayAccountCircuitService,
  type GatewayAccountCircuitAttempt,
  type GatewayAccountCircuitConfirmation,
  type GatewayAccountCircuitTransportFailure
} from './runtime/account-circuit.service.js'
import { GeminiInteractionAffinityUnavailableError } from './protocols/gemini-v1beta/interaction-affinity.service.js'
import {
  GatewayRequestAttemptTracker,
  RouteCoordinationBudget,
  defaultGatewayFinalResponseReserveMs,
  advanceGatewayRoutePlanCursor
} from './routing/route-coordination.js'

export const openAIGatewayRouter = Router()

export type { OpenAIGatewayRequestIdentity } from './request/preflight.js'

export interface OpenAIGatewayHandleOptions {
  identity?: OpenAIGatewayRequestIdentity
  candidateAccounts?: UpstreamAccount[]
  disableSessionAffinity?: boolean
  exposeUpstreamDiagnostics?: boolean
  trafficSource?: OpenAIGatewayTrafficSource
  auditCaptureMode?: 'default' | 'metadata_only'
  settingsOverride?: Partial<GatewaySettings>
  disableAccountStateMutation?: boolean
  ignoreAccountRuntimeSuppression?: boolean
  forwardModelsRequestToUpstream?: boolean
  accountProbeModel?: string
  onUpstreamAttemptDiagnostic?: (lastAttempt: UpstreamAttempt) => void
  onUpstreamAttemptStartedDiagnostic?: (account: UpstreamAccount, upstreamUrl: string) => void
}

interface GatewayResponseErrorBoundaryOptions {
  abortController: AbortController
  traceId: string
  endpoint: string
  auditCapture: Pick<ReturnType<typeof createAuditCapture>, 'markDownstreamClosed'>
  clearActiveDownstreamSessionAffinity: () => Promise<void>
  reportError?: (error: unknown, fields: Record<string, unknown>, message: string) => void
}

interface NormalRouteSpeedFirstDecisionOperations {
  isAccountLatencyDegradedAsync: typeof isNormalRouteAccountLatencyDegradedAsync
  recordFirstByteSlowAsync: typeof recordNormalRouteFirstByteSlowAsync
  recordFirstByteSuccessAsync: typeof recordNormalRouteFirstByteSuccessAsync
  reserveCutoverTarget: typeof reserveSpeedFirstCutoverTarget
}

const defaultNormalRouteSpeedFirstDecisionOperations: NormalRouteSpeedFirstDecisionOperations = {
  isAccountLatencyDegradedAsync: isNormalRouteAccountLatencyDegradedAsync,
  recordFirstByteSlowAsync: recordNormalRouteFirstByteSlowAsync,
  recordFirstByteSuccessAsync: recordNormalRouteFirstByteSuccessAsync,
  reserveCutoverTarget: reserveSpeedFirstCutoverTarget
}

let normalRouteSpeedFirstDecisionOperationsForTest: Partial<NormalRouteSpeedFirstDecisionOperations> | undefined

export function setNormalRouteSpeedFirstDecisionOperationsForTest(
  operations?: Partial<NormalRouteSpeedFirstDecisionOperations>
): void {
  normalRouteSpeedFirstDecisionOperationsForTest = operations
}

function normalRouteSpeedFirstDecisionOperations(): NormalRouteSpeedFirstDecisionOperations {
  return normalRouteSpeedFirstDecisionOperationsForTest
    ? { ...defaultNormalRouteSpeedFirstDecisionOperations, ...normalRouteSpeedFirstDecisionOperationsForTest }
    : defaultNormalRouteSpeedFirstDecisionOperations
}

export function handleGatewayDbServiceUnavailable(error: unknown, req: Request, res: Response, next: NextFunction): void {
  const message = dbServiceUnavailableMessage(error)
  if (!message || res.headersSent) {
    next(error)
    return
  }

  getRequestLogger().error(errorLogFields(error, {
    event: 'gateway_db_service_unavailable',
    endpoint: requestEndpoint(req)
  }), '网关 DB service 不可用')

  sendGatewayErrorResponse(res, 503, gatewayErrorPayload(message, 'service_unavailable'), {
    protocol: gatewayProtocolClientErrorProtocolForRequest(req)
  })
}

function dbServiceUnavailableMessage(error: unknown): string | undefined {
  if (!(error instanceof Error)) {
    return undefined
  }
  return /^本地数据库服务(暂时不可用|未就绪|请求超时|已退出)/.test(error.message)
    ? error.message
    : undefined
}

openAIGatewayRouter.all('*', async (req, res, next) => {
  try {
    await handleOpenAIGatewayRequest(req, res)
  } catch (error) {
    handleGatewayDbServiceUnavailable(error, req, res, next)
  }
})

export async function handleOpenAIGatewayRequest(
  req: Request,
  res: Response,
  options: OpenAIGatewayHandleOptions = {}
): Promise<void> {
  const startedAt = Date.now()
  const requestAttemptTracker = new GatewayRequestAttemptTracker()
  const httpCompletion = observeGatewayHttpCompletion(res)
  const abortController = new AbortController()
  const traceId = getTraceId() ?? createTraceId()
  const routeCoordinationBudget = new RouteCoordinationBudget({ requestId: traceId })
  let downstreamLifecycleStartedAt = performance.now()
  let downstreamLifecycleLogged = false
  const logDownstreamLifecycle = (outcome: 'success' | 'aborted') => {
    if (downstreamLifecycleLogged) return
    downstreamLifecycleLogged = true
    logRequestStage('downstream.finish', {
      traceId,
      statusCode: res.statusCode,
      writableEnded: res.writableEnded,
      headersSent: res.headersSent
    }, outcome, downstreamLifecycleStartedAt)
  }
  res.once('finish', () => logDownstreamLifecycle('success'))
  res.once('close', () => {
    if (!res.writableFinished) logDownstreamLifecycle('aborted')
  })
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestLane = resolveOpenAIGatewayRequestLane(req)
  const trafficSource = normalizeOpenAIGatewayTrafficSource(options.trafficSource)
  logRequestStage('request.accepted', {
    traceId,
    method: req.method,
    endpoint,
    requestLane,
    trafficSource,
    model: requestModel(req),
    stream: requestStream(req)
  })
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const auditCapture = createAuditCapture({
    req,
    httpCompletion,
    traceId,
    clientIp,
    startedAtMs: startedAt,
    trafficSource,
    captureMode: options.auditCaptureMode ?? (isAccountProbeTrafficSource(trafficSource) ? 'metadata_only' : 'default')
  })
  let activeDownstreamSessionAffinity: { key: string; accountId: string } | undefined
  let pendingFailedAttemptAccountSlotRelease: (() => void) | undefined
  const releasePendingFailedAttemptAccountSlot = (): void => {
    const release = pendingFailedAttemptAccountSlotRelease
    pendingFailedAttemptAccountSlotRelease = undefined
    release?.()
  }
  const clearActiveDownstreamSessionAffinity = async (): Promise<void> => {
    if (!activeDownstreamSessionAffinity) {
      return
    }
    const binding = activeDownstreamSessionAffinity
    activeDownstreamSessionAffinity = undefined
    await forgetOpenAIAccountForSessionAsync(binding.key, binding.accountId)
  }
  attachGatewayResponseErrorBoundary(res, {
    abortController,
    traceId,
    endpoint,
    auditCapture,
    clearActiveDownstreamSessionAffinity
  })
  req.once('aborted', () => {
    if (isGatewayForcedDownstreamClose(res)) return
    const abortSource = gatewayRequestAbortSource(req)
    if (abortSource === 'server_diagnostic_timeout') {
      auditCapture.markServerDiagnosticTimeout()
      abortController.abort(abortSource)
      clearActiveDownstreamSessionAffinity()
      return
    }
    if (abortSource === 'server_diagnostic_cancel') {
      auditCapture.markServerDiagnosticCancellation()
      abortController.abort(abortSource)
      clearActiveDownstreamSessionAffinity()
      return
    }
    auditCapture.markDownstreamClosed()
    abortController.abort()
    clearActiveDownstreamSessionAffinity()
  })
  res.once('close', () => {
    if (!isGatewayForcedDownstreamClose(res)) {
      if (!res.writableFinished) {
        const abortSource = gatewayRequestAbortSource(req)
        if (abortSource === 'server_diagnostic_timeout') {
          auditCapture.markServerDiagnosticTimeout()
          abortController.abort(abortSource)
        } else if (abortSource === 'server_diagnostic_cancel') {
          auditCapture.markServerDiagnosticCancellation()
          abortController.abort(abortSource)
        } else {
          auditCapture.markDownstreamClosed()
          abortController.abort()
        }
      }
      if (abortController.signal.aborted) {
        clearActiveDownstreamSessionAffinity()
      }
    }
  })

  let preflight: Awaited<ReturnType<typeof prepareOpenAIGatewayDispatchContext>>
  const routeActionVisitedGroupIds = new Set<string>()
  const finalizeRouteAction = async (action: OpenAIGatewayRouteAction): Promise<void> => {
    if (res.writableEnded || res.destroyed) return
    const failure = action.failure
    if (failure) {
      if (failure.retryAfterMs !== undefined && !res.headersSent) {
        res.setHeader('Retry-After', String(Math.max(1, Math.ceil(failure.retryAfterMs / 1000))))
      }
      await sendGatewayFailureResponse({
        req,
        res,
        auditCapture,
        usageContext: action.usageContext,
        startedAt,
        statusCode: failure.statusCode,
        responsePayload: gatewayErrorPayload(failure.message, failure.errorType, failure.errorCode),
        audit: {
          outcome: 'gateway_failed',
          errorPhase: failure.errorPhase,
          errorCode: failure.errorCode,
          errorMessage: failure.message
        },
        failureAttribution: failure.failureAttribution
      })
      return
    }
    if (action.coordination.outcome === 'client_handoff') {
      await sendStreamServerRetryExhaustedResponse({
        req,
        res,
        auditCapture,
        usageContext: action.usageContext,
        startedAt,
        retryReason: 'pre_commit_stream_failure',
        message: '当前路由暂时无法继续派发，请客户端重试并重新选择可用账户',
        errorCode: gatewayStreamClientRetryErrorCode,
        clientStrategy: action.clientStrategy
      })
      return
    }
    const temporarilyBlocked = action.coordination.outcome === 'temporarily_blocked'
    const message = temporarilyBlocked
      ? '当前路由暂时没有可派发账户，请稍后重试'
      : '当前路由没有可用的上游账户'
    await sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: action.usageContext,
      startedAt,
      statusCode: 503,
      responsePayload: gatewayErrorPayload(message, 'service_unavailable', 'upstream_retryable_error'),
      audit: {
        outcome: 'gateway_failed',
        errorPhase: 'dispatch',
        errorCode: 'upstream_retryable_error',
        errorMessage: message
      }
    })
  }
  const resolveRouteAction = async (
    initial: OpenAIGatewayDispatchContext | OpenAIGatewayRouteAction
  ): Promise<OpenAIGatewayDispatchContext | undefined> => {
    let result: OpenAIGatewayDispatchContext | OpenAIGatewayRouteAction = initial
    while (isOpenAIGatewayRouteAction(result)) {
      const action = result
      const groupId = action.usageContext.groupId
      const mayTryFallback = action.coordination.outcome !== 'client_handoff'
        && !action.interactionResourceAffinity
        && !routeActionVisitedGroupIds.has(groupId)
      routeActionVisitedGroupIds.add(groupId)
      if (mayTryFallback) {
        const fallback = await prepareApiKeyGroupFallbackDispatchContext({
          req,
          res,
          auditCapture,
          options: {
            ...options,
            trafficSource,
            requestLane: action.requestLane,
            serverRetryBudget: action.serverRetryBudget,
            gatewayRequestWallBudget: action.gatewayRequestWallBudget,
            routeCoordinationBudget: action.routeCoordinationBudget,
            requestAttemptTracker: action.requestAttemptTracker,
            downstreamCommitState: action.downstreamCommitState,
            normalRouteFirstByteConfig: action.normalRouteFirstByteConfig,
          },
          startedAt,
          traceId,
          clientIp,
          endpoint,
          requestSnapshot,
          signal: abortController.signal,
          routePlanSnapshot: action.routePlanSnapshot,
          reason: action.coordination.reason,
          apiKeyRecord: action.groupFallbackApiKeyRecord ?? action.apiKeyRecord,
          groupFallbackApiKeyRecord: action.groupFallbackApiKeyRecord ?? action.apiKeyRecord,
          systemAccountId: action.usageContext.systemAccountId,
          apiKeyId: action.usageContext.apiKeyId,
          groupId,
          trafficSource: action.usageContext.trafficSource,
          requestLane: action.requestLane,
          requestClientCompatibility: action.clientStrategy.requestClientCompatibility
        })
        if (fallback.attempted && fallback.context) {
          result = fallback.context
          continue
        }
        if (fallback.attempted && !fallback.context) return undefined
      }
      await finalizeRouteAction(action)
      return undefined
    }
    return result
  }
  const preflightStartedAt = performance.now()
  try {
    preflight = await prepareOpenAIGatewayDispatchContext({
      req,
      res,
      auditCapture,
      options: {
        ...options,
        trafficSource,
        requestLane,
        routeCoordinationBudget,
        requestAttemptTracker
      },
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal: abortController.signal
    })
  } catch (error) {
    logRequestStage('preflight.failed', {
      traceId,
      error,
      errorName: error instanceof Error ? error.name : 'NonErrorThrown'
    }, 'unexpected_failure', preflightStartedAt)
    auditCapture.cancel()
    throw error
  }
  if (preflight && isOpenAIGatewayRouteAction(preflight)) {
    preflight = await resolveRouteAction(preflight)
  }
  if (!preflight) {
    logRequestStage('preflight.rejected', {
      traceId,
      failureReason: 'preflight_rejected',
      decisionInputs: { endpoint, requestLane }
    }, 'expected_failure', preflightStartedAt)
    auditCapture.cancel()
    return
  }
  logRequestStage('preflight.completed', {
    traceId,
    groupId: preflight.usageContext.groupId,
    apiKeyId: preflight.usageContext.apiKeyId,
    candidateAccountCount: preflight.accounts.length,
    routeStrategyId: preflight.apiKeyRecord?.route_strategy_id
  }, 'success', preflightStartedAt)
  let currentPreflight = preflight
  const requestExecutionSignal = abortController.signal
  let releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
  let streamServerRetryExcludedAccountIds = new Set<string>()
  let streamServerRetryCount = 0
  let speedFirstByteRetryCount = 0
  let gatewayWallMinimumMeaningfulAttemptMs = 0
  let speedFirstRetryCandidateAccountIds: Set<string> | undefined
  let speedFirstCutoverReservation: SpeedFirstCutoverReservation | undefined
  let activeCompactSseWaitHeartbeat: ReturnType<typeof createGatewaySseWaitHeartbeat> | undefined
  let pendingAccountCircuitConfirmation: GatewayAccountCircuitConfirmation | undefined
  let pendingSemanticRetryId: string | undefined
  let codexTurnAvoidedFallbackEnabled = false
  let fallbackSwitchCount = 0
  const enteredRouteGroupIds = new Set<string>([currentPreflight.usageContext.groupId])
  let forceRecoverableFailureWait = false
  const exhaustedAccountIds = new Set<string>()
  const nonStreamResponseStartedFailedAccountIds = new Set<string>()
  const switchToFallbackGroup = async (
    reason: string
  ): Promise<'none' | 'switched' | 'completed'> => {
    if (currentPreflight.interactionResourceAffinity) {
      return 'none'
    }
    const fallbackApiKeyRecord = reason === 'account_scoped_agent_guidance_exhausted'
      ? currentPreflight.groupFallbackApiKeyRecord ?? currentPreflight.apiKeyRecord
      : currentPreflight.apiKeyRecord
    const groupBindingCount = fallbackApiKeyRecord?.group_bindings?.length ?? 0
    if (groupBindingCount > 0 && fallbackSwitchCount >= groupBindingCount) {
      auditCapture.addGatewayMetadata({
        label: 'api_key_group_route_fallback_skipped',
        metadata: {
          reason,
          groupBindingCount,
          fallbackSwitchCount,
          skippedReason: 'fallback_hop_limit'
        }
      })
      return 'none'
    }
    const gatewayUsageContext = currentPreflight.usageContext
    const fallback = await prepareApiKeyGroupFallbackDispatchContext({
      req,
      res,
      auditCapture,
      options: {
        ...options,
        trafficSource,
        requestLane: currentPreflight.requestLane,
        serverRetryBudget: currentPreflight.serverRetryBudget,
        gatewayRequestWallBudget: currentPreflight.gatewayRequestWallBudget,
        routeCoordinationBudget: currentPreflight.routeCoordinationBudget,
        requestAttemptTracker: currentPreflight.requestAttemptTracker,
        downstreamCommitState: currentPreflight.downstreamCommitState,
        normalRouteFirstByteConfig: currentPreflight.normalRouteFirstByteConfig,
        routePlanSnapshot: currentPreflight.routePlanSnapshot
      },
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal: requestExecutionSignal,
      reason,
      apiKeyRecord: fallbackApiKeyRecord,
      systemAccountId: gatewayUsageContext.systemAccountId,
      apiKeyId: gatewayUsageContext.apiKeyId,
      groupId: gatewayUsageContext.groupId,
      trafficSource: gatewayUsageContext.trafficSource,
      requestLane: currentPreflight.requestLane,
      requestClientCompatibility: currentPreflight.clientStrategy.requestClientCompatibility,
      routePlanSnapshot: currentPreflight.routePlanSnapshot,
      groupFallbackApiKeyRecord: currentPreflight.groupFallbackApiKeyRecord ?? currentPreflight.apiKeyRecord,
      excludedAccountIds: exhaustedAccountIds
    })
    if (!fallback.attempted) {
      return 'none'
    }
    if (!fallback.context) {
      await settleHotQualityExplorationSafely(currentPreflight, 'not_dispatched')
      return 'completed'
    }
    const fallbackContext = await resolveRouteAction(fallback.context)
    if (!fallbackContext) {
      await settleHotQualityExplorationSafely(currentPreflight, 'not_dispatched')
      return 'completed'
    }
    fallbackSwitchCount += 1
    if (enteredRouteGroupIds.has(fallbackContext.usageContext.groupId)) {
      return 'none'
    }
    enteredRouteGroupIds.add(fallbackContext.usageContext.groupId)
    transferClientIpAccountPendingFailures(
      currentPreflight.clientIpAccountAvoidanceTracker,
      fallbackContext.clientIpAccountAvoidanceTracker
    )
    releaseClientIpSlot()
    await settleHotQualityExplorationSafely(currentPreflight, 'not_dispatched')
    currentPreflight = fallbackContext
    releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
    streamServerRetryExcludedAccountIds = new Set<string>()
    streamServerRetryCount = 0
    speedFirstByteRetryCount = 0
    speedFirstRetryCandidateAccountIds = undefined
    speedFirstCutoverReservation?.release()
    speedFirstCutoverReservation = undefined
    codexTurnAvoidedFallbackEnabled = false
    return 'switched'
  }
  const switchToHybridQualityUpgrade = async (
    reason: string
  ): Promise<'none' | 'switched' | 'completed'> => {
    const hybridRoute = currentPreflight.hybridRoute
    if (!hybridRoute) {
      return 'none'
    }
    const remainingHybridGroupIds = new Set(
      currentPreflight.routePlanSnapshot.orderedAllowedTargets.slice(currentPreflight.routePlanSnapshot.cursor + 1)
    )
    const nextRoute = await resolveNextHybridGatewayRoute({
      req,
      apiKeyRecord: {
        ...hybridRoute.apiKeyRecord,
        group_bindings: hybridRoute.apiKeyRecord.group_bindings?.filter((binding) => (
          remainingHybridGroupIds.has(binding.group_id) && !enteredRouteGroupIds.has(binding.group_id)
        ))
      },
      currentRoute: hybridRoute.route,
      requestClientCompatibility: currentPreflight.clientStrategy.requestClientCompatibility,
      signal: requestExecutionSignal
    })
    if (!nextRoute) {
      auditCapture.addGatewayMetadata({
        label: 'hybrid_quality_upgrade_unavailable',
        metadata: {
          reason,
          currentTargetModel: hybridRoute.targetModel,
          currentLevelRange: [hybridRoute.route.minLevel, hybridRoute.route.maxLevel]
        }
      })
      return 'none'
    }
    auditCapture.addGatewayMetadata({
      label: 'hybrid_quality_upgrade_route',
      metadata: {
        reason,
        fromModel: hybridRoute.targetModel,
        toModel: nextRoute.targetModel,
        fromLevelRange: [hybridRoute.route.minLevel, hybridRoute.route.maxLevel],
        toLevelRange: [nextRoute.route.minLevel, nextRoute.route.maxLevel],
        toGroupId: nextRoute.groupId,
        retryCount: hybridRoute.qualityRetryCount + 1
      }
    })
    const context = await prepareOpenAIGatewayDispatchContext({
      req,
      res,
      auditCapture,
      options: {
        ...options,
        serverRetryBudget: currentPreflight.serverRetryBudget,
        gatewayRequestWallBudget: currentPreflight.gatewayRequestWallBudget,
        routeCoordinationBudget: currentPreflight.routeCoordinationBudget,
        requestAttemptTracker: currentPreflight.requestAttemptTracker,
        downstreamCommitState: currentPreflight.downstreamCommitState,
        normalRouteFirstByteConfig: currentPreflight.normalRouteFirstByteConfig,
        routePlanSnapshot: advanceGatewayRoutePlanCursor(
          currentPreflight.routePlanSnapshot,
          currentPreflight.routePlanSnapshot.orderedAllowedTargets.indexOf(nextRoute.groupId)
        ),
        identity: {
          systemAccountId: currentPreflight.usageContext.systemAccountId,
          apiKeyId: currentPreflight.usageContext.apiKeyId,
          groupId: nextRoute.groupId
        },
        apiKeyRecord: nextRoute.apiKeyRecord,
        candidateAccounts: nextRoute.accounts,
        responseInspectionPolicies: nextRoute.responseInspectionPolicies,
        trafficSource,
        requestLane: resolveOpenAIGatewayRequestLane(req)
      },
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal: requestExecutionSignal
    })
    if (!context) {
      await settleHotQualityExplorationSafely(currentPreflight, 'not_dispatched')
      return 'completed'
    }
    const resolvedContext = await resolveRouteAction(context)
    if (!resolvedContext) {
      await settleHotQualityExplorationSafely(currentPreflight, 'not_dispatched')
      return 'completed'
    }
    releaseClientIpSlot()
    await settleHotQualityExplorationSafely(currentPreflight, 'not_dispatched')
    currentPreflight = {
      ...resolvedContext,
      hybridRoute: {
        // nextRoute.apiKeyRecord is filtered to the remaining target groups.
        // Quality scoring and any later upgrade still need the immutable full policy snapshot.
        apiKeyRecord: hybridRoute.apiKeyRecord,
        config: hybridRoute.config,
        scoring: hybridRoute.scoring,
        route: nextRoute.route,
        targetModel: nextRoute.targetModel,
        affinityApplied: false,
        scoringFallbackApplied: hybridRoute.scoringFallbackApplied,
        qualityRetryCount: hybridRoute.qualityRetryCount + 1
      }
    }
    enteredRouteGroupIds.add(currentPreflight.usageContext.groupId)
    releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
    streamServerRetryExcludedAccountIds = new Set<string>()
    streamServerRetryCount = 0
    speedFirstByteRetryCount = 0
    speedFirstRetryCandidateAccountIds = undefined
    speedFirstCutoverReservation?.release()
    speedFirstCutoverReservation = undefined
    codexTurnAvoidedFallbackEnabled = false
    return 'switched'
  }

  try {
    while (true) {
      activeCompactSseWaitHeartbeat?.stop()
      activeCompactSseWaitHeartbeat = undefined
      if (currentPreflight.gatewayRequestWallBudget.handoffRequired({
        finalResponseReserveMs: defaultGatewayFinalResponseReserveMs,
        minimumMeaningfulAttemptMs: gatewayWallMinimumMeaningfulAttemptMs
      })) {
        observeGatewayRouting({ kind: 'budget', outcome: 'wall_exhausted' })
        observeGatewayRouting({ kind: 'budget', outcome: 'client_handoff' })
        if (pendingAccountCircuitConfirmation) {
          await getGatewayAccountCircuitService().completeConfirmation(pendingAccountCircuitConfirmation, 'unknown')
          pendingAccountCircuitConfirmation = undefined
        }
        const wallRemainingMs = currentPreflight.gatewayRequestWallBudget.remainingMs()
        auditCapture.addGatewayMetadata({
          label: 'gateway_request_client_handoff',
          metadata: {
            reason: 'gateway_request_wall_budget_exhausted',
            wallRemainingMs,
            serverRetryRemainingMs: currentPreflight.serverRetryBudget.remainingMs(),
            attempts: currentPreflight.requestAttemptTracker.snapshot()
          }
        })
        await sendStreamServerRetryExhaustedResponse({
          req,
          res,
          auditCapture,
          usageContext: currentPreflight.usageContext,
          startedAt,
          retryReason: 'pre_commit_stream_failure',
          message: '网关请求处理时间已到，请客户端重试并重新选择可用账户',
          errorCode: gatewayStreamClientRetryErrorCode,
          clientStrategy: currentPreflight.clientStrategy,
          downstreamCommitState: currentPreflight.downstreamCommitState
        })
        return
      }
      const {
        activeGatewaySettings,
        usageContext: gatewayUsageContext,
        accounts,
        sessionAffinityKey,
        clientStrategy,
        clientIpAccountAvoidanceTracker,
        modelPriority,
        responseInspectionPolicies,
        apiKeyRecord,
        normalRouteFirstByteConfig,
        normalRouteSpeedFirstConfig,
        normalRouteLatencyDegradationApplied,
        codexTurnAccountAvoidanceApplied,
        codexTurnAvoidedAccountIds,
        precheckHalfOpenEligible
      } = currentPreflight
      const codexTurnAvoidedAccountIdSet = new Set(codexTurnAvoidedAccountIds ?? [])
      let dispatchAccounts = pendingAccountCircuitConfirmation
        ? accounts.filter((account) => gatewayAccountRuntimeKey(account) === pendingAccountCircuitConfirmation?.accountRuntimeKey)
        : streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds)
      if (!pendingAccountCircuitConfirmation && codexTurnAccountAvoidanceApplied && codexTurnAvoidedAccountIdSet.size > 0) {
        dispatchAccounts = dispatchAccounts.filter((account) => (
          codexTurnAvoidedFallbackEnabled
            ? codexTurnAvoidedAccountIdSet.has(account.id)
            : !codexTurnAvoidedAccountIdSet.has(account.id)
        ))
      }
      if (!pendingAccountCircuitConfirmation && speedFirstRetryCandidateAccountIds) {
        dispatchAccounts = dispatchAccounts.filter((account) => speedFirstRetryCandidateAccountIds?.has(account.id))
      }
      if (dispatchAccounts.length === 0) {
        if (
          !codexTurnAvoidedFallbackEnabled
          && codexTurnAccountAvoidanceApplied
          && accounts.some((account) => codexTurnAvoidedAccountIdSet.has(account.id) && !exhaustedAccountIds.has(account.id))
        ) {
          codexTurnAvoidedFallbackEnabled = true
          speedFirstRetryCandidateAccountIds = undefined
          auditCapture.addGatewayMetadata({
            label: 'codex_turn_avoided_accounts_last_resort',
            metadata: {
              avoidedAccountIds: [...codexTurnAvoidedAccountIdSet],
              exhaustedAccountIds: [...exhaustedAccountIds]
            }
          })
          continue
        }
        for (const accountId of streamServerRetryExcludedAccountIds) {
          exhaustedAccountIds.add(accountId)
        }
        const fallbackSwitch = await switchToFallbackGroup('upstream_accounts_exhausted')
        if (fallbackSwitch === 'completed') {
          return
        }
        if (fallbackSwitch === 'switched') {
          continue
        }
        throw new UpstreamAttemptError('没有可用的上游账户')
      }
      const speedFirstRouteOverrideActive = normalRouteLatencyDegradationApplied === true || speedFirstRetryCandidateAccountIds !== undefined
      if (speedFirstRouteOverrideActive) {
        await forgetOpenAIAccountForSessionAsync(sessionAffinityKey)
      }
      const dispatchSessionAffinityKey = speedFirstRouteOverrideActive ? undefined : sessionAffinityKey
      let upstreamResult: Awaited<ReturnType<typeof fetchFirstAvailableUpstream>>
      const dispatchAccountCircuitConfirmation = pendingAccountCircuitConfirmation
      pendingAccountCircuitConfirmation = undefined
      const dispatchCutoverReservation = speedFirstCutoverReservation
      speedFirstCutoverReservation = undefined
      const speedFirstLatencyScope = normalRouteLatencyDegradationScope({
        systemAccountId: gatewayUsageContext.systemAccountId,
        routeStrategyId: apiKeyRecord?.route_strategy_id,
        groupId: gatewayUsageContext.groupId
      })
      let speedFirstSlowObservedForAttempt: NormalRouteLatencySlowResult | undefined
      const onNormalRouteFirstByteDeadline: NonNullable<GatewayUpstreamRequestCoordinationContext['onNormalRouteFirstByteDeadline']> = async ({
        account,
        deadline,
        coordinator
      }) => {
        if (
          deadline.limitingFactor === 'lane_timeout'
          || deadline.limitingFactor === 'uncommitted_attempt'
        ) {
          return 'continue'
        }
        if (deadline.limitingFactor === 'wall_precommit') {
          return 'abort'
        }
        if (!normalRouteSpeedFirstConfig) {
          return 'continue'
        }
        try {
          const decisionOperations = normalRouteSpeedFirstDecisionOperations()
          const alreadyDegraded = await decisionOperations.isAccountLatencyDegradedAsync(account, speedFirstLatencyScope)
          speedFirstSlowObservedForAttempt = await decisionOperations.recordFirstByteSlowAsync(
            account,
            speedFirstLatencyScope,
            normalRouteSpeedFirstConfig,
            `普通路由速度优先首字观察阈值 ${deadline.effectiveDeadlineMs}ms 已到达`
          )
          const nextExcludedAccountIds = new Set(streamServerRetryExcludedAccountIds)
          nextExcludedAccountIds.add(account.id)
          const remainingAccounts = await speedFirstRouteEligibleDispatchAccounts(
            accounts,
            nextExcludedAccountIds,
            speedFirstLatencyScope,
            currentPreflight.requestAttemptTracker
          )
          const remainingCandidateCount = remainingAccounts.length
          const maxRetries = normalRouteSpeedFirstConfig.maxFirstByteRetriesPerRequest
          const degradedForCutover = alreadyDegraded || speedFirstSlowObservedForAttempt?.degraded === true
          const cutoverPreconditionsMet = degradedForCutover
            && speedFirstByteRetryCount < maxRetries
            && remainingCandidateCount > 0
          const reservation = cutoverPreconditionsMet
            ? await decisionOperations.reserveCutoverTarget({
                systemAccountId: gatewayUsageContext.systemAccountId,
                routeStrategyId: apiKeyRecord?.route_strategy_id ?? '',
                groupId: gatewayUsageContext.groupId,
                slowAccountId: gatewayAccountConcurrencyAccountId(account),
                targets: remainingAccounts,
                lane: currentPreflight.requestLane,
                groupSchedulingPolicy: currentPreflight.groupSchedulingPolicy
            })
            : undefined
          const speedFirstCutoverAllowedAtDeadline = reservation !== undefined
            && coordinator.attachReservation(reservation)
          auditCapture.addGatewayMetadata({
            label: 'normal_route_speed_first_slow_observed',
            metadata: {
              accountId: account.id,
              accountName: account.name,
              firstTokenMs: undefined,
              thresholdMs: normalRouteSpeedFirstConfig.firstByteDeadlineMs,
              elapsedMs: Date.now() - (deadline.deadlineAtMs - deadline.effectiveDeadlineMs),
              observedAt: 'first_byte_deadline',
              alreadyDegraded,
              slowCount: speedFirstSlowObservedForAttempt?.slowCount,
              degraded: speedFirstSlowObservedForAttempt?.degraded,
              degradedUntil: speedFirstSlowObservedForAttempt?.degradedUntil,
              nextProbeAt: speedFirstSlowObservedForAttempt?.nextProbeAt,
              cutoverAllowed: speedFirstCutoverAllowedAtDeadline,
              retryBlockedReason: speedFirstCutoverAllowedAtDeadline
                ? undefined
                : remainingCandidateCount <= 0
                    ? 'no_remaining_candidate'
                    : !degradedForCutover
                      ? 'slow_observation_not_degraded'
                    : !cutoverPreconditionsMet
                      ? 'max_retry_exceeded'
                      : 'target_slot_or_cutover_budget_unavailable',
              retryCount: speedFirstByteRetryCount,
              maxRetries,
              remainingCandidateCount,
              remainingCandidateAccountIds: remainingAccounts.map((item) => item.id)
            }
          })
          return speedFirstCutoverAllowedAtDeadline ? 'abort' : 'continue'
        } catch (error) {
          coordinator.releaseReservation()
          getRequestLogger().warn(errorLogFields(error, {
            event: 'normal_route_speed_first_local_decision_failed',
            stage: 'first_byte_cutover',
            accountId: account.id,
            routeStrategyId: apiKeyRecord?.route_strategy_id,
            groupId: gatewayUsageContext.groupId
          }), '普通路由速度优先本地决策失败，继续当前上游')
          auditCapture.addGatewayMetadata({
            label: 'normal_route_speed_first_local_decision_failed',
            metadata: {
              stage: 'first_byte_cutover',
              accountId: account.id
            }
          })
          return 'continue'
        }
      }
      const upstreamDispatchStartedAt = performance.now()
      const semanticRetryId = pendingSemanticRetryId
      pendingSemanticRetryId = undefined
      if (shouldKeepCodexCompactSseAliveDuringUpstreamWait(req, currentPreflight)) {
        activeCompactSseWaitHeartbeat = createGatewaySseWaitHeartbeat({
          res,
          downstreamProtocol: currentPreflight.clientStrategy.downstreamProtocol,
          downstreamCommitState: currentPreflight.downstreamCommitState,
          signal: requestExecutionSignal,
          intervalMs: 10_000,
          emitCodexCompactionKeepalive: true
        })
        activeCompactSseWaitHeartbeat?.start()
      }
      try {
        upstreamResult = await fetchFirstAvailableUpstream(
          req,
          dispatchAccounts,
          activeGatewaySettings,
          gatewayUsageContext,
          auditCapture,
          dispatchSessionAffinityKey,
          requestExecutionSignal,
          clientIpAccountAvoidanceTracker,
          currentPreflight.requestLane,
          currentPreflight.groupSchedulingPolicy,
          options.disableAccountStateMutation !== true,
          currentPreflight.clientStrategy.requestClientCompatibility,
          modelPriority,
          dispatchCutoverReservation,
          precheckHalfOpenEligible === true,
          {
            scope: 'gateway_request',
            timeoutPolicy: currentPreflight.gatewayRequestWallBudget.unbounded
              ? 'codex_compaction_unbounded'
              : undefined,
            serverRetryBudget: currentPreflight.serverRetryBudget,
            gatewayRequestWallBudget: currentPreflight.gatewayRequestWallBudget,
            routeCoordinationBudget: currentPreflight.routeCoordinationBudget,
            requestAttemptTracker: currentPreflight.requestAttemptTracker,
            semanticRetryId,
            normalRouteFirstByteConfig,
            onNormalRouteFirstByteDeadline,
            onUpstreamAttemptStarted: (account, upstreamUrl) => {
              options.onUpstreamAttemptStartedDiagnostic?.(account, upstreamUrl)
            }
          },
          gatewayClientAllowsUpstreamSemanticInterpretation(currentPreflight.clientStrategy),
          forceRecoverableFailureWait
            || (currentPreflight.apiKeyRecord?.group_bindings?.length ?? 0) <= 1
            || fallbackSwitchCount >= (currentPreflight.apiKeyRecord?.group_bindings?.length ?? 0) - 1,
          dispatchAccountCircuitConfirmation
        )
      } catch (error) {
        if (dispatchAccountCircuitConfirmation) {
          await getGatewayAccountCircuitService().completeConfirmation(dispatchAccountCircuitConfirmation, 'unknown')
        }
        if (error instanceof NormalRouteFirstByteCutoverError) {
          const cutoverReservation = error.cutoverReservation
          const targetAccountId = cutoverReservation?.targetAccountId
          speedFirstByteRetryCount += 1
          streamServerRetryExcludedAccountIds.add(error.accountId)
          auditCapture.addGatewayMetadata({
            label: 'normal_route_speed_first_retry_dispatch',
            metadata: {
              accountId: error.accountId,
              responseHeadersReceived: false,
              limitingFactor: error.deadline.limitingFactor,
              retryCount: speedFirstByteRetryCount,
              maxRetries: normalRouteSpeedFirstConfig?.maxFirstByteRetriesPerRequest ?? 0,
              retryAllowed: targetAccountId !== undefined,
              retryBlockedReason: targetAccountId === undefined ? 'cutover_not_confirmed' : undefined,
              targetAccountId
            }
          })
          if (cutoverReservation && targetAccountId) {
            speedFirstCutoverReservation = cutoverReservation
            speedFirstRetryCandidateAccountIds = new Set([targetAccountId])
            continue
          }
          cutoverReservation?.release()
          exhaustedAccountIds.add(error.accountId)
          const fallbackSwitch = await switchToFallbackGroup('normal_route_speed_first_exhausted')
          if (fallbackSwitch === 'completed') return
          if (fallbackSwitch === 'switched') continue
          throw new UpstreamAttemptError(error.message, {
            accountId: error.accountId,
            accountName: error.accountName,
            upstreamUrl: 'gateway:first-byte-deadline',
            message: error.message,
            transportFailureKind: 'timeout'
          }, [error.accountId])
        }
        if (error instanceof GatewayRequestWallBudgetExhaustedError) {
          gatewayWallMinimumMeaningfulAttemptMs = Math.max(
            gatewayWallMinimumMeaningfulAttemptMs,
            error.minimumMeaningfulAttemptMs
          )
          logRequestStage('upstream.dispatch.failed', {
            traceId,
            failureReason: error.code,
            wallRemainingMs: error.wallRemainingMs,
            candidateAccountCount: dispatchAccounts.length
          }, 'expected_failure', upstreamDispatchStartedAt)
          continue
        }
        logRequestStage('upstream.dispatch.failed', {
          traceId,
          error,
          failureReason: error instanceof UpstreamAttemptError ? 'upstream_accounts_exhausted' : 'upstream_dispatch_unexpected',
          candidateAccountCount: dispatchAccounts.length,
          errorName: error instanceof Error ? error.name : 'NonErrorThrown',
          expectedFailure: error instanceof UpstreamAttemptError,
          decisionInputs: {
            candidateAccountCount: dispatchAccounts.length,
            fallbackSwitchCount,
            serverRetryBudgetMs: currentPreflight.serverRetryBudget.remainingMs()
          }
        }, error instanceof UpstreamAttemptError ? 'expected_failure' : 'unexpected_failure', upstreamDispatchStartedAt)
        if (error instanceof UpstreamAttemptError) {
          if (error.terminalUpstreamFailure) throw error
          const recoverableAccountIds = new Set(error.recoverableAccountIds)
          for (const accountId of nonStreamResponseStartedFailedAccountIds) {
            exhaustedAccountIds.add(accountId)
          }
          for (const accountId of error.failedAccountIds) {
            if (!recoverableAccountIds.has(accountId)) {
              exhaustedAccountIds.add(accountId)
            }
          }
          if (speedFirstRetryCandidateAccountIds !== undefined) {
            for (const accountId of error.failedAccountIds) {
              streamServerRetryExcludedAccountIds.add(accountId)
            }
            speedFirstRetryCandidateAccountIds = undefined
            const remainingSpeedFirstAccounts = streamRetryDispatchAccounts(
              accounts,
              streamServerRetryExcludedAccountIds
            )
            auditCapture.addGatewayMetadata({
              label: 'normal_route_speed_first_reserved_target_exhausted',
              metadata: {
                failedAccountIds: error.failedAccountIds,
                recoverableAccountIds: [...recoverableAccountIds],
                remainingCandidateAccountIds: remainingSpeedFirstAccounts.map((account) => account.id)
              }
            })
            if (remainingSpeedFirstAccounts.length > 0) {
              continue
            }
          }
          if (
            !codexTurnAvoidedFallbackEnabled
            && codexTurnAccountAvoidanceApplied
            && accounts.some((account) => codexTurnAvoidedAccountIdSet.has(account.id) && !exhaustedAccountIds.has(account.id))
          ) {
            codexTurnAvoidedFallbackEnabled = true
            speedFirstRetryCandidateAccountIds = undefined
            auditCapture.addGatewayMetadata({
              label: 'codex_turn_avoided_accounts_last_resort',
              metadata: {
                avoidedAccountIds: [...codexTurnAvoidedAccountIdSet],
                exhaustedAccountIds: [...exhaustedAccountIds]
              }
            })
            continue
          }
          const fallbackReason = error.agentGuidanceResponse
            ? 'account_scoped_agent_guidance_exhausted'
            : 'upstream_accounts_exhausted'
          const fallbackSwitch = await switchToFallbackGroup(fallbackReason)
          if (fallbackSwitch === 'completed') {
            return
          }
          if (fallbackSwitch === 'switched') {
            continue
          }
          if (
            recoverableAccountIds.size > 0
            && !currentPreflight.serverRetryBudget.handoffRequired('recoverable_later')
          ) {
            forceRecoverableFailureWait = true
            continue
          }
        }
        throw error
      } finally {
        if (dispatchCutoverReservation && !dispatchCutoverReservation.consumed) {
          dispatchCutoverReservation.release()
        }
      }
      const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, attemptStartedAt, timeoutProfile, releaseConcurrency, markFirstOutput, confirmSameAccountApiKeyFailures, confirmHalfOpenSuccess, releaseHalfOpenLease, accountCircuitAttempt, hotQualityAttempt, normalRouteFirstByteDeadline, responsePrecommitDeadlineAtMs, onFirstByteDeadline, firstByteDeadlineCoordinator } = upstreamResult
      await settleHotQualityExplorationSafely(
        currentPreflight,
        currentPreflight.hotQualityExplorationReservation?.accountRuntimeKey === gatewayAccountRuntimeKey(account)
          ? 'dispatched'
          : 'not_dispatched'
      )
      let firstOutputLogged = false
      const markFirstOutputWithTiming = () => {
        if (!firstOutputLogged) {
          firstOutputLogged = true
          hotQualityAttempt.markFirstByte(Date.now() - attemptStartedAt)
          logRequestStage('upstream.first_output', {
            traceId,
            accountId: account.id,
            providerCode: account.providerCode
          }, 'success', upstreamDispatchStartedAt)
        }
        markFirstOutput()
      }
      logRequestStage('upstream.dispatch.completed', {
        traceId,
        accountId: account.id,
        providerCode: account.providerCode,
        statusCode: upstreamResponse.status,
        upstreamHost: (() => { try { return new URL(upstreamUrl).host } catch { return undefined } })()
      }, 'success', upstreamDispatchStartedAt)
      notifyUpstreamAttemptDiagnostic(options, {
        accountId: account.id,
        accountName: account.name,
        providerCode: account.providerCode,
        providerProtocolProfileId: account.providerProtocolProfileId,
        protocolCode: account.protocolCode,
        protocolVersion: account.protocolVersion,
        upstreamUrl,
        status: upstreamResponse.status
      })
      const releaseAccountSlot = attachAccountSlotRelease(res, releaseConcurrency, {
        deferUntilExplicitRelease: true,
        clientAbortSignal: requestExecutionSignal
      })
      const effectiveFirstByteDeadlineMs = normalRouteFirstByteDeadline?.effectiveDeadlineMs
      let attemptErrorEscaped = false

      try {
        activeDownstreamSessionAffinity = sessionAffinityKey
          ? { key: sessionAffinityKey, accountId: account.id }
          : undefined
        const contentType = upstreamResponse.headers.get('content-type') ?? ''
        // A complete non-2xx is already the terminal upstream response.  Do
        // not interpret a missing/misleading content type as an SSE response
        // and replace the provider's error body with a gateway event.
        const shouldHandleAsStream = upstreamResponse.ok && shouldHandleOpenAIUpstreamResponseAsStream({
          contentType,
          streamRequest: isEffectiveOpenAIStreamRequest(req, account)
        })
        if (options.disableAccountStateMutation !== true) {
          persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, gatewayUsageContext.trafficSource)
        }

        const responseHandlingStartedAt = performance.now()
        downstreamLifecycleStartedAt = responseHandlingStartedAt
        const upstreamBodyStartedAt = performance.now()
        let handledResponse: Awaited<ReturnType<typeof handleStreamUpstreamResponse>>
        if (shouldHandleAsStream) {
          handledResponse = await handleStreamUpstreamResponse({
            req,
            res,
            account,
            upstreamResponse,
            upstreamUrl,
            auditAttemptId,
            auditCapture,
            settings: activeGatewaySettings,
            timeoutProfile,
            usageContext: gatewayUsageContext,
            startedAt: attemptStartedAt,
            signal: requestExecutionSignal,
            firstByteDeadlineMs: effectiveFirstByteDeadlineMs,
            responsePrecommitDeadlineAtMs,
            onFirstByteDeadline,
            onFirstByteDeadlineSuperseded: () => firstByteDeadlineCoordinator?.supersede(),
            sessionAffinityKey,
            clientStrategy,
            responseInspectionPolicies,
            hybridRoute: currentPreflight.hybridRoute,
            markFirstOutput: markFirstOutputWithTiming,
            clientIpAccountAvoidanceTracker,
            accountStateMutationEnabled: options.disableAccountStateMutation !== true,
            automaticAccountStateMutationEnabled: false,
            codexTurnAccountAvoidanceApplied,
            downstreamCommitState: currentPreflight.downstreamCommitState
          })
        } else {
          try {
            handledResponse = await handleNonStreamUpstreamResponse({
              req,
              res,
              account,
              upstreamResponse,
              upstreamUrl,
              auditAttemptId,
              auditCapture,
              settings: activeGatewaySettings,
              timeoutProfile,
              usageContext: gatewayUsageContext,
              startedAt: attemptStartedAt,
              signal: requestExecutionSignal,
              firstByteDeadlineMs: effectiveFirstByteDeadlineMs,
              responsePrecommitDeadlineAtMs,
              onFirstByteDeadline,
              onFirstByteDeadlineSuperseded: () => firstByteDeadlineCoordinator?.supersede(),
              sessionAffinityKey,
              responseInspectionPolicies,
              hybridRoute: currentPreflight.hybridRoute,
              clientStrategy,
              markFirstOutput: markFirstOutputWithTiming,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: options.disableAccountStateMutation !== true,
              automaticAccountStateMutationEnabled: false,
              downstreamCommitState: currentPreflight.downstreamCommitState
            })
          } catch (error) {
            if (error instanceof GeminiInteractionAffinityUnavailableError) {
              await hotQualityAttempt.recordTerminal({ outcomeClass: 'unknown', source: 'request_lifecycle' })
              throw error
            }
            const provenBodyTransportFailure = isProvenUpstreamBodyTransportError(error)
            if (res.headersSent || res.writableEnded || res.destroyed) {
              const accountTransportFailure = provenBodyTransportFailure && !requestExecutionSignal.aborted
              await hotQualityAttempt.recordTerminal({
                outcomeClass: requestExecutionSignal.aborted
                  ? 'client_cancellation'
                  : accountTransportFailure
                    ? 'read_interruption'
                    : 'unknown',
                failureScope: accountTransportFailure ? 'protocol_model' : 'none',
                source: accountTransportFailure ? 'gateway_transport' : 'request_lifecycle'
              })
              if (!accountTransportFailure) await accountCircuitAttempt?.reportUnknown()
              throw error
            }
            if (!provenBodyTransportFailure) {
              await hotQualityAttempt.recordTerminal({
                outcomeClass: 'unknown',
                failureScope: 'none',
                source: 'request_lifecycle'
              })
              auditCapture.addGatewayMetadata({
                label: 'gateway_unproven_upstream_body_transport_failure',
                metadata: {
                  accountId: account.id,
                  endpoint: gatewayUsageContext.endpoint,
                  errorName: error instanceof Error ? error.name : undefined
                }
              })
              await accountCircuitAttempt?.reportUnknown()
              throw error
            }
            const bodyFailure = accountCircuitTransportFailure(error)
            await hotQualityAttempt.recordTerminal({
              outcomeClass: requestExecutionSignal.aborted
                ? 'client_cancellation'
                : bodyFailure.kind === 'timeout'
                  ? 'timeout'
                  : 'transport_failure',
              failureScope: requestExecutionSignal.aborted ? 'none' : 'protocol_model',
              source: requestExecutionSignal.aborted ? 'request_lifecycle' : 'gateway_transport'
            })
            const requestErrorResult = await handleUpstreamRequestError({
              req,
              usageContext: gatewayUsageContext,
              auditCapture,
              auditAttemptId,
              account,
              upstreamUrl,
              attemptStartedAt,
              attemptIndex: 0,
              auditAttemptIndex: 0,
              settings: activeGatewaySettings,
              sessionAffinityKey,
              signal: requestExecutionSignal,
              lastAttempt: {
                accountId: account.id,
                accountName: account.name,
                providerCode: account.providerCode,
                providerProtocolProfileId: account.providerProtocolProfileId,
                protocolCode: account.protocolCode,
                protocolVersion: account.protocolVersion,
                upstreamUrl,
                status: upstreamResponse.status
              },
              failedProxyDispatchKeys: new Map(),
              error,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: false
            })
            nonStreamResponseStartedFailedAccountIds.add(account.id)
            const circuitDecision = !requestExecutionSignal.aborted
              && accountCircuitAttempt
              ? await accountCircuitAttempt.reportTransportFailure(bodyFailure)
              : undefined
            if (circuitDecision?.outcome === 'confirmation_acquired') {
              await getGatewayAccountCircuitService().completeConfirmation(circuitDecision.confirmation, 'unknown')
            }
            if (requestErrorResult.action === 'skip_account') {
              throw new UpstreamAttemptError(
                requestErrorResult.lastAttempt?.message ?? (error instanceof Error ? error.message : '上游响应正文读取失败'),
                requestErrorResult.lastAttempt,
                [account.id],
                undefined,
                [],
                true
              )
            }
            throw error
          }
        }
        const responseRetryUpstream = 'retryUpstream' in handledResponse && handledResponse.retryUpstream
        const responseErrorCode = 'errorCode' in handledResponse ? handledResponse.errorCode : undefined
        const neutralRequestWallTermination = responseErrorCode === 'gateway_request_wall_budget_exhausted'
        const gatewayLocalFailure = 'gatewayLocalFailure' in handledResponse
          && handledResponse.gatewayLocalFailure === true
        const normalRouteFirstByteCutover = responseRetryUpstream
          && 'retryReason' in handledResponse
          && handledResponse.retryReason === 'normal_route_first_byte_timeout'
        const neutralNormalRouteFirstByteCutover = normalRouteFirstByteCutover
          && (
            normalRouteFirstByteDeadline?.limitingFactor === 'configured'
            || normalRouteFirstByteDeadline?.limitingFactor === 'wall_precommit'
          )
        const hardNormalRouteFirstByteCutover = normalRouteFirstByteCutover
          && (
            normalRouteFirstByteDeadline?.limitingFactor === 'lane_timeout'
            || normalRouteFirstByteDeadline?.limitingFactor === 'uncommitted_attempt'
          )
        const neutralSchedulingTermination = neutralRequestWallTermination
          || neutralNormalRouteFirstByteCutover
          || gatewayLocalFailure
        // A configured speed-first deadline is a scheduling decision. The
        // gateway deliberately stopped reading this otherwise-live response,
        // so it is neither transport-failure nor framing-complete evidence.
        const transportFailure = neutralSchedulingTermination
          ? undefined
          : hardNormalRouteFirstByteCutover
          ? ('transportFailure' in handledResponse && handledResponse.transportFailure
              ? handledResponse.transportFailure
              : {
                  kind: 'timeout' as const,
                  reason: 'message' in handledResponse
                    ? handledResponse.message
                    : '普通路由首字硬截止已到达'
                })
          : !neutralNormalRouteFirstByteCutover && 'transportFailure' in handledResponse
            ? handledResponse.transportFailure
            : undefined
        const explicitUserPolicyRetry = responseRetryUpstream
          && 'retryReason' in handledResponse
          && handledResponse.retryReason === 'response_inspection'
          && 'responseInspection' in handledResponse
          && handledResponse.responseInspection?.replayAuthority === 'explicit_user_policy'
        const requestLocalProtocolFailure = responseRetryUpstream
          && 'retryReason' in handledResponse
          && handledResponse.retryReason === 'upstream_protocol_failure'
        const protocolValidatedSuccess = !responseRetryUpstream
          && 'protocolValidatedSuccess' in handledResponse
          && handledResponse.protocolValidatedSuccess === true
        if (transportFailure) {
          notifyUpstreamAttemptDiagnostic(options, {
            accountId: account.id,
            accountName: account.name,
            providerCode: account.providerCode,
            providerProtocolProfileId: account.providerProtocolProfileId,
            protocolCode: account.protocolCode,
            protocolVersion: account.protocolVersion,
            upstreamUrl,
            status: upstreamResponse.status,
            message: transportFailure.reason,
            transportFailureKind: transportFailure.kind
          })
        }
        const diagnosticUpstreamResponse = !transportFailure
          && !neutralSchedulingTermination
          && !requestLocalProtocolFailure
          && (
            !upstreamResponse.ok
            || (responseRetryUpstream && !explicitUserPolicyRetry)
            || (!responseRetryUpstream && !protocolValidatedSuccess)
          )
        const circuitDecision = transportFailure
          && !requestExecutionSignal.aborted
          && responseErrorCode !== 'downstream_connection_closed'
          && accountCircuitAttempt
          ? await accountCircuitAttempt.reportTransportFailure({
              kind: transportFailure.kind,
              reason: transportFailure.reason
            })
          : undefined
        await hotQualityAttempt.recordTerminal({
          // A complete 2xx body with an invalid protocol shape is scoped to the
          // current request. Keep shared quality neutral: it may be a damaged
          // conversation while the same account remains healthy elsewhere.
          outcomeClass: neutralSchedulingTermination || requestLocalProtocolFailure
            ? 'unknown'
            : transportFailure
            ? hotQualityOutcomeForTransportFailure(transportFailure.kind)
            : explicitUserPolicyRetry
              ? 'explicit_policy_failure'
              : diagnosticUpstreamResponse
                ? 'upstream_response_failure'
                : 'completed_response',
          failureScope: neutralSchedulingTermination || requestLocalProtocolFailure ? 'none' : transportFailure ? 'protocol_model' : explicitUserPolicyRetry ? 'account' : 'none',
          source: neutralSchedulingTermination || requestLocalProtocolFailure
            ? 'request_lifecycle'
            : transportFailure
            ? 'gateway_transport'
            : explicitUserPolicyRetry
              ? 'explicit_policy'
              : diagnosticUpstreamResponse
                ? 'upstream_response'
                : 'request_lifecycle',
          firstByteMs: 'firstTokenMs' in handledResponse ? handledResponse.firstTokenMs : undefined
        })
        if (neutralSchedulingTermination) {
          await accountCircuitAttempt?.reportUnknown()
        } else if (!transportFailure) {
          await accountCircuitAttempt?.reportFramingComplete()
        }
        logRequestStage('upstream.body.completed', {
          traceId,
          accountId: account.id,
          stream: shouldHandleAsStream,
          retryUpstream: responseRetryUpstream,
          errorCode: responseErrorCode
        }, responseRetryUpstream ? 'expected_failure' : 'success', upstreamBodyStartedAt)
        logRequestStage('downstream.response.completed', {
          traceId,
          accountId: account.id,
          stream: shouldHandleAsStream,
          retryUpstream: responseRetryUpstream,
          errorCode: responseErrorCode
        }, responseRetryUpstream ? 'expected_failure' : 'success', responseHandlingStartedAt)
        activeDownstreamSessionAffinity = undefined
        if (handledResponse.alreadyFinalized) {
          if (circuitDecision?.outcome === 'confirmation_acquired') {
            await getGatewayAccountCircuitService().completeConfirmation(circuitDecision.confirmation, 'unknown')
          }
          return
        }
        if (handledResponse.retryUpstream) {
          if (circuitDecision?.outcome === 'confirmation_acquired') {
            // Confirmation is intentionally deferred to a later request. The
            // request that observed the failure may rotate to another account,
            // but it must never replay the same physical credential implicitly.
            await getGatewayAccountCircuitService().completeConfirmation(circuitDecision.confirmation, 'unknown')
          }
          if (requestExecutionSignal.aborted || res.destroyed) {
            return
          }
          if (handledResponse.retryReason === 'normal_route_first_byte_timeout') {
            speedFirstByteRetryCount += 1
            streamServerRetryExcludedAccountIds.add(account.id)
            const remainingAccounts = streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds)
            const remainingCandidateCount = remainingAccounts.length
            const maxRetries = normalRouteSpeedFirstConfig?.maxFirstByteRetriesPerRequest ?? 0
            const cutoverReady = firstByteDeadlineCoordinator?.canCutover === true
            const retryAllowed = cutoverReady
              && speedFirstByteRetryCount <= maxRetries
              && remainingCandidateCount > 0
            auditCapture.addGatewayMetadata({
              label: 'normal_route_speed_first_retry_dispatch',
              metadata: {
                retryCount: speedFirstByteRetryCount,
                maxRetries,
                retryAllowed,
                retryBlockedReason: retryAllowed
                  ? undefined
                  : remainingCandidateCount <= 0
                      ? 'no_remaining_candidate'
                      : !cutoverReady
                        ? 'cutover_not_confirmed'
                        : 'max_retry_exceeded',
                accountId: account.id,
                remainingCandidateCount,
                excludedAccountIds: [...streamServerRetryExcludedAccountIds],
                thresholdMs: normalRouteSpeedFirstConfig?.firstByteDeadlineMs,
                slowCount: speedFirstSlowObservedForAttempt?.slowCount,
                degraded: speedFirstSlowObservedForAttempt?.degraded,
                degradedUntil: speedFirstSlowObservedForAttempt?.degradedUntil,
                nextProbeAt: speedFirstSlowObservedForAttempt?.nextProbeAt,
                cutoverAllowedAtDeadline: cutoverReady,
                remainingCandidateAccountIds: remainingAccounts.map((item) => item.id),
                errorCode: handledResponse.errorCode
              }
            })
            if (retryAllowed) {
              const reservation = firstByteDeadlineCoordinator?.transferForCutover()
              const reservedTargetAccountId = reservation?.targetAccountId
              if (reservation && reservedTargetAccountId) {
                speedFirstCutoverReservation = reservation
                speedFirstRetryCandidateAccountIds = new Set([reservedTargetAccountId])
                continue
              }
            }
            if (remainingCandidateCount <= 0) {
              for (const accountId of streamServerRetryExcludedAccountIds) {
                exhaustedAccountIds.add(accountId)
              }
              const fallbackSwitch = await switchToFallbackGroup('normal_route_speed_first_exhausted')
              if (fallbackSwitch !== 'none') {
                if (fallbackSwitch === 'completed') {
                  return
                }
                continue
              }
            }
            await sendStreamServerRetryExhaustedResponse({
              req,
              res,
              auditCapture,
              usageContext: gatewayUsageContext,
              startedAt,
              retryReason: handledResponse.retryReason,
              message: handledResponse.message,
              errorCode: handledResponse.errorCode,
              uncommittedResponseBody: handledResponse.uncommittedResponseBody,
              accountId: account.id,
              clientStrategy,
              downstreamCommitState: currentPreflight.downstreamCommitState
            })
            return
          }
          if (handledResponse.retryReason === 'hybrid_quality') {
            const hybridRoute = currentPreflight.hybridRoute
            const qualityConfig = hybridRoute?.config.qualityInspection
            const retryCount = hybridRoute?.qualityRetryCount ?? 0
            const maxRetries = qualityConfig?.maxRetries ?? 0
            const action = handledResponse.hybridQuality?.actualAction ?? 'return_error'
            auditCapture.addGatewayMetadata({
              label: 'hybrid_quality_retry_dispatch',
              metadata: {
                action,
                retryCount,
                maxRetries,
                accountId: account.id,
                targetModel: hybridRoute?.targetModel,
                errorCode: handledResponse.errorCode,
                failureType: handledResponse.hybridQuality?.result?.failureType,
                score: handledResponse.hybridQuality?.result?.score,
                confidence: handledResponse.hybridQuality?.result?.confidence
              }
            })
            if (action !== 'return_error' && retryCount < maxRetries && hybridRoute) {
              if (action === 'repair_then_upgrade') {
                if (retryCount === 0) {
                  const repairInstructionApplied = await appendHybridQualityRepairInstruction(req, handledResponse.hybridQuality, abortController.signal)
                  auditCapture.addGatewayMetadata({
                    label: 'hybrid_quality_repair_retry',
                    metadata: {
                      applied: repairInstructionApplied,
                      retryCount: retryCount + 1,
                      targetModel: hybridRoute.targetModel,
                      accountId: account.id,
                      errorCode: handledResponse.errorCode,
                      failureType: handledResponse.hybridQuality?.result?.failureType,
                      score: handledResponse.hybridQuality?.result?.score
                    }
                  })
                  if (repairInstructionApplied) {
                    pendingSemanticRetryId = `hybrid_quality_repair:${retryCount + 1}`
                    currentPreflight = {
                      ...currentPreflight,
                      hybridRoute: {
                        ...hybridRoute,
                        qualityRetryCount: retryCount + 1
                      }
                    }
                    continue
                  }
                }
                const qualitySwitch = await switchToHybridQualityUpgrade(handledResponse.errorCode ?? 'hybrid_quality_failed')
                if (qualitySwitch === 'completed') {
                  return
                }
                if (qualitySwitch === 'switched') {
                  continue
                }
              }
              if (action === 'upgrade_next_level') {
                const qualitySwitch = await switchToHybridQualityUpgrade(handledResponse.errorCode ?? 'hybrid_quality_failed')
                if (qualitySwitch === 'completed') {
                  return
                }
                if (qualitySwitch === 'switched') {
                  continue
                }
              }
              if (action === 'retry_same_model') {
                const repairInstructionApplied = await appendHybridQualityRepairInstruction(req, handledResponse.hybridQuality, abortController.signal)
                auditCapture.addGatewayMetadata({
                  label: 'hybrid_quality_repair_retry',
                  metadata: {
                    applied: repairInstructionApplied,
                    retryCount: retryCount + 1,
                    targetModel: hybridRoute.targetModel,
                    accountId: account.id,
                    errorCode: handledResponse.errorCode,
                    failureType: handledResponse.hybridQuality?.result?.failureType,
                    score: handledResponse.hybridQuality?.result?.score
                  }
                })
                if (repairInstructionApplied) {
                  pendingSemanticRetryId = `hybrid_quality_repair:${retryCount + 1}`
                  currentPreflight = {
                    ...currentPreflight,
                    hybridRoute: {
                      ...hybridRoute,
                      qualityRetryCount: retryCount + 1
                    }
                  }
                  continue
                }
              }
            }
            const responsePayload = gatewayErrorPayload(
              handledResponse.message || '混合路由质量评分未通过',
              'upstream_response_error',
              handledResponse.errorCode ?? 'hybrid_quality_failed'
            )
            await sendGatewayFailureResponse({
              req,
              res,
              auditCapture,
              usageContext: gatewayUsageContext,
              startedAt,
              statusCode: handledResponse.statusCode ?? 502,
              responsePayload,
              audit: {
                outcome: 'upstream_failed',
                errorPhase: 'dispatch',
                errorCode: handledResponse.errorCode ?? 'hybrid_quality_failed',
                errorMessage: responsePayload.error.message
              },
              recordUsage: false,
              usageErrorMessage: handledResponse.message
            })
            return
          }
          streamServerRetryCount += 1
          const policyRequestedAccountExclusion = handledResponse.excludeCurrentAccount
            || (handledResponse.responseInspection && shouldExcludeCurrentAccountForStreamRetry(handledResponse.responseInspection))
            || false
          if (policyRequestedAccountExclusion) {
            streamServerRetryExcludedAccountIds.add(account.id)
          }
          auditCapture.addGatewayMetadata({
            label: 'stream_server_retry_dispatch',
            metadata: {
              retryReason: handledResponse.retryReason,
              retryCount: streamServerRetryCount,
              candidateCount: accounts.length,
              remainingCandidateCount: streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds).length,
              noAvailableAccountWaitTimeoutSeconds: activeGatewaySettings.noAvailableAccountWaitTimeoutSeconds,
              elapsedMs: Date.now() - startedAt,
              accountId: account.id,
              excludedAccountIds: [...streamServerRetryExcludedAccountIds],
              excludeCurrentAccount: handledResponse.excludeCurrentAccount,
              currentRequestAccountExcluded: policyRequestedAccountExclusion,
              policyRequestedAccountExclusion,
              policyId: handledResponse.responseInspection?.policyId,
              policyName: handledResponse.responseInspection?.policyName,
              accountSwitch: handledResponse.responseInspection?.accountSwitch,
              errorCode: handledResponse.errorCode
            }
          })
          if (
            handledResponse.retryReason === 'response_inspection'
            && handledResponse.responseInspection
            && !policyRequestedAccountExclusion
          ) {
            await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'response_inspection_no_dispatch_change')
            auditCapture.addGatewayMetadata({
              label: 'response_inspection_server_retry_stopped',
              metadata: {
                reason: 'no_dispatch_change',
                accountId: account.id,
                policyId: handledResponse.responseInspection.policyId,
                policyName: handledResponse.responseInspection.policyName,
                accountSwitch: handledResponse.responseInspection.accountSwitch,
                retryEnabled: handledResponse.responseInspection.retryEnabled,
                errorCode: handledResponse.errorCode
              }
            })
            await sendStreamServerRetryExhaustedResponse({
              req,
              res,
              auditCapture,
              usageContext: gatewayUsageContext,
              startedAt,
              retryReason: handledResponse.retryReason,
              decision: handledResponse.responseInspection,
              message: handledResponse.message,
              errorCode: handledResponse.errorCode,
              uncommittedResponseBody: handledResponse.uncommittedResponseBody,
              accountId: account.id,
              clientStrategy,
              downstreamCommitState: currentPreflight.downstreamCommitState
            })
            return
          }
          if (streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds).length === 0) {
            for (const accountId of streamServerRetryExcludedAccountIds) {
              exhaustedAccountIds.add(accountId)
            }
            const fallbackReason = streamServerRetryFallbackReason(handledResponse.retryReason)
            const fallbackSwitch = await switchToFallbackGroup(fallbackReason)
            if (fallbackSwitch !== 'none') {
              if (fallbackSwitch === 'completed') {
                return
              }
              continue
            }
            await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'stream_server_retry_exhausted')
            await sendStreamServerRetryExhaustedResponse({
              req,
              res,
              auditCapture,
              usageContext: gatewayUsageContext,
              startedAt,
              retryReason: handledResponse.retryReason,
              decision: handledResponse.responseInspection,
              message: handledResponse.message,
              errorCode: handledResponse.errorCode,
              uncommittedResponseBody: handledResponse.uncommittedResponseBody,
              accountId: account.id,
              clientStrategy,
              downstreamCommitState: currentPreflight.downstreamCommitState
            })
            return
          }
          continue
        }
        if (normalRouteSpeedFirstConfig && handledResponse.firstTokenMs !== undefined) {
          try {
            const decisionOperations = normalRouteSpeedFirstDecisionOperations()
            if (handledResponse.firstTokenMs > normalRouteSpeedFirstConfig.firstByteDeadlineMs) {
              if (!speedFirstSlowObservedForAttempt) {
                const slowResult = await decisionOperations.recordFirstByteSlowAsync(
                  account,
                  speedFirstLatencyScope,
                  normalRouteSpeedFirstConfig,
                  `普通路由速度优先首字耗时 ${handledResponse.firstTokenMs}ms 超过阈值 ${normalRouteSpeedFirstConfig.firstByteDeadlineMs}ms`
                )
                auditCapture.addGatewayMetadata({
                  label: 'normal_route_speed_first_slow_observed',
                  metadata: {
                    accountId: account.id,
                    firstTokenMs: handledResponse.firstTokenMs,
                    thresholdMs: normalRouteSpeedFirstConfig.firstByteDeadlineMs,
                    observedAt: 'response_completed',
                    slowCount: slowResult?.slowCount,
                    degraded: slowResult?.degraded,
                    degradedUntil: slowResult?.degradedUntil,
                    nextProbeAt: slowResult?.nextProbeAt
                  }
                })
              }
            } else {
              const recoveryResult = await decisionOperations.recordFirstByteSuccessAsync(
                account,
                speedFirstLatencyScope,
                normalRouteSpeedFirstConfig,
                handledResponse.firstTokenMs
              )
              if (recoveryResult) {
                auditCapture.addGatewayMetadata({
                  label: 'normal_route_speed_first_recovery_observed',
                  metadata: {
                    accountId: account.id,
                    firstTokenMs: handledResponse.firstTokenMs,
                    thresholdMs: normalRouteSpeedFirstConfig.firstByteDeadlineMs,
                    cleared: recoveryResult.cleared,
                    recoverySuccessCount: recoveryResult.recoverySuccessCount,
                    requiredRecoverySuccessCount: recoveryResult.requiredRecoverySuccessCount
                  }
                })
              }
            }
          } catch (error) {
            getRequestLogger().warn(errorLogFields(error, {
              event: 'normal_route_speed_first_local_decision_failed',
              stage: 'response_observation',
              accountId: account.id,
              routeStrategyId: apiKeyRecord?.route_strategy_id,
              groupId: gatewayUsageContext.groupId
            }), '普通路由速度优先响应观测失败，保留已完成上游响应')
            auditCapture.addGatewayMetadata({
              label: 'normal_route_speed_first_local_decision_failed',
              metadata: {
                stage: 'response_observation',
                accountId: account.id
              }
            })
          }
        }
        const handledResponseFinalizationInput = {
          req,
          res,
          account,
          upstreamResponse,
          upstreamUrl,
          auditAttemptId,
          auditCapture,
          settings: activeGatewaySettings,
          timeoutProfile,
          usageContext: gatewayUsageContext,
          startedAt,
          signal: requestExecutionSignal,
          result: handledResponse,
          clientIpAccountAvoidanceTracker,
          accountStateMutationEnabled: options.disableAccountStateMutation !== true,
          automaticAccountStateMutationEnabled: false,
          downstreamCommitState: currentPreflight.downstreamCommitState
        }
        await applyHandledUpstreamRoutingEffects(handledResponseFinalizationInput)
        releaseAccountSlot()
        const httpCompletedAtMs = await httpCompletion.wait()
        await finalizeHandledUpstreamResponse({
          ...handledResponseFinalizationInput,
          completedAtMs: httpCompletedAtMs,
          routingEffectsApplied: true
        })
        if (handledResponse.protocolValidatedSuccess === true) {
          await confirmHalfOpenSuccess()
          await confirmSameAccountApiKeyFailures()
        }
        await releaseHalfOpenLease()
        return
      } catch (error) {
        attemptErrorEscaped = true
        throw error
      } finally {
        try {
          firstByteDeadlineCoordinator?.supersede()
          await settleTransferredAccountCircuitAttemptSafely(accountCircuitAttempt, account.id)
        } finally {
          try {
            await hotQualityAttempt.recordTerminal({
              outcomeClass: requestExecutionSignal.aborted ? 'client_cancellation' : 'unknown',
              source: 'request_lifecycle'
            })
          } finally {
            try {
              await releaseHalfOpenLease()
            } finally {
              if (attemptErrorEscaped && !requestExecutionSignal.aborted) {
                pendingFailedAttemptAccountSlotRelease = releaseAccountSlot
              } else {
                releaseAccountSlot()
              }
            }
          }
        }
      }
    }
  } catch (error) {
    await clearActiveDownstreamSessionAffinity()
    if (error instanceof UpstreamAttemptError) {
      for (const accountId of nonStreamResponseStartedFailedAccountIds) {
        error.failedAccountIds.push(accountId)
      }
    }
    const gatewayUsageContext = currentPreflight.usageContext
    await recordKnownClientIpRequestError(error, gatewayUsageContext, auditCapture)
    if (handleGatewayRequestKnownErrorResponse({
      req,
      res,
      auditCapture,
      error,
      signal: abortController.signal
    })) {
      releasePendingFailedAttemptAccountSlot()
      return
    }
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : '没有可用的上游账户'
    if (error instanceof UpstreamAttemptError) {
      getRequestLogger().warn({
        event: 'gateway_dispatch_exhausted',
        ...classifyGatewayDispatchExhaustion(lastAttempt),
        endpoint: gatewayUsageContext.endpoint,
        apiKeyId: gatewayUsageContext.apiKeyId,
        groupId: gatewayUsageContext.groupId,
        trafficSource: gatewayUsageContext.trafficSource,
        lastAttemptAccountId: lastAttempt?.accountId,
        failedAccountIds: error.failedAccountIds
      }, '网关上游调度已耗尽')
    } else {
      getRequestLogger().error(errorLogFields(error, {
        event: 'gateway_request_unexpected_error',
        endpoint: gatewayUsageContext.endpoint,
        apiKeyId: gatewayUsageContext.apiKeyId,
        groupId: gatewayUsageContext.groupId,
        trafficSource: gatewayUsageContext.trafficSource
      }), '网关请求处理出现未预期异常')
    }
    notifyUpstreamAttemptDiagnostic(options, lastAttempt)
    if (shouldSendDispatchExhaustedProtocolRetry(currentPreflight, error, res)
      || shouldSendTransportCommittedCodexCompactFailure(currentPreflight, res)) {
      await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'dispatch_exhausted_protocol_retry')
      releasePendingFailedAttemptAccountSlot()
      auditCapture.addGatewayMetadata({
        label: 'dispatch_exhausted_protocol_retry',
        metadata: {
          clientProfile: currentPreflight.clientStrategy.clientProfile,
          downstreamProtocol: currentPreflight.clientStrategy.downstreamProtocol,
          lastAttemptAccountId: lastAttempt?.accountId,
          lastAttemptStatus: lastAttempt?.status,
          failedAccountIds: error instanceof UpstreamAttemptError ? error.failedAccountIds : undefined
        }
      })
      await sendPreCommitStreamRetryExhaustedResponse({
        req,
        res,
        auditCapture,
        usageContext: gatewayUsageContext,
        startedAt,
        retryReason: 'pre_commit_stream_failure',
        message: gatewayStreamClientRetryMessage,
        errorCode: gatewayStreamClientRetryErrorCode,
        accountId: lastAttempt?.accountId,
        clientStrategy: currentPreflight.clientStrategy
      })
      return
    }
    const terminalUpstreamFailure = error instanceof UpstreamAttemptError
      && error.terminalUpstreamFailure
    const knownUpstreamHttpFailure = error instanceof UpstreamAttemptError
      && lastAttempt?.status !== undefined
    // Customer gateway traffic must never expose the last account's complete
    // HTTP error after candidate exhaustion.  Keep raw upstream diagnostics
    // for explicit diagnostics/non-gateway callers only.
    const exposeKnownUpstreamHttpFailure = gatewayUsageContext.trafficSource !== 'gateway'
    const diagnosticError = options.exposeUpstreamDiagnostics
      || terminalUpstreamFailure
      || (knownUpstreamHttpFailure && exposeKnownUpstreamHttpFailure)
      ? buildDiagnosticUpstreamError(lastAttempt, message)
      : undefined
    const statusCode = diagnosticError?.statusCode ?? 503
    const responsePayload = diagnosticError?.payload
      ?? (message === '没有可用的上游账户'
        ? gatewayErrorPayload(message, 'service_unavailable', 'no_available_upstream_account')
        : gatewayErrorPayload('上游暂时不可用，请重试', 'service_unavailable', gatewayStreamClientRetryErrorCode))
    await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'gateway_failure_response')
    releasePendingFailedAttemptAccountSlot()
    await sendGatewayFailureResponse({
      req,
      res,
      auditCapture,
      usageContext: gatewayUsageContext,
      startedAt,
      statusCode,
      responsePayload,
      audit: {
        outcome: 'upstream_failed',
        errorPhase: 'dispatch',
        errorCode: responsePayload.error.code ?? 'service_unavailable',
        errorMessage: diagnosticError?.errorMessage ?? message
      },
      recordUsage: !lastAttempt,
      usageErrorMessage: message
    })
  } finally {
    activeCompactSseWaitHeartbeat?.stop()
    releasePendingFailedAttemptAccountSlot()
    await settleHotQualityExplorationSafely(currentPreflight, 'not_dispatched')
    speedFirstCutoverReservation?.release()
    releaseClientIpSlot()
    auditCapture.cancel()
  }
}

async function settleTransferredAccountCircuitAttemptSafely(
  attempt: GatewayAccountCircuitAttempt | undefined,
  accountId: string
): Promise<void> {
  if (!attempt?.isConfirmation) return
  let lastError: unknown
  for (let retry = 0; retry < 2; retry += 1) {
    try {
      // The attempt pins its first terminal intent. If an earlier failure or
      // framing settlement lost its store reply, reportUnknown retries that
      // exact intent instead of replacing it with a contradictory outcome.
      await attempt.reportUnknown()
      return
    } catch (error) {
      lastError = error
    }
  }
  getRequestLogger().warn(errorLogFields(lastError, {
    event: 'gateway_account_circuit_transferred_confirmation_settlement_failed',
    accountId
  }), '账户 confirmation 在上游处理结束后结算失败，保留原终态意图并等待租约到期')
}

function notifyUpstreamAttemptDiagnostic(
  options: OpenAIGatewayHandleOptions,
  lastAttempt: UpstreamAttempt | undefined
): void {
  if (!lastAttempt || !options.onUpstreamAttemptDiagnostic) {
    return
  }
  try {
    options.onUpstreamAttemptDiagnostic(lastAttempt)
  } catch (error) {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'gateway_upstream_attempt_diagnostic_callback_failed',
      accountId: lastAttempt.accountId,
      upstreamStatus: lastAttempt.status
    }), '网关上游尝试诊断回调失败')
  }
}

async function confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(
  preflight: OpenAIGatewayDispatchContext,
  auditCapture: ReturnType<typeof createAuditCapture>,
  reason: string
): Promise<void> {
  if (isAccountDiagnosticTrafficSource(preflight.usageContext.trafficSource)) {
    return
  }
  const result = await confirmClientIpAccountAvoidanceAfterFinalFailureAsync(
    preflight.clientIpAccountAvoidanceTracker,
    preflight.activeGatewaySettings
  )
  if (result.confirmedAccountIds.length === 0) {
    return
  }
  getRequestLogger().warn({
    event: 'gateway_client_ip_account_failure_confirmed_after_final_failure',
    reason,
    confirmedAccountIds: result.confirmedAccountIds,
    systemAccountId: preflight.usageContext.systemAccountId,
    apiKeyId: preflight.usageContext.apiKeyId,
    groupId: preflight.usageContext.groupId,
    clientIp: preflight.usageContext.clientIp
  }, '请求失败已返回客户端，客户端 IP 级账号回避状态已立即确认')
  auditCapture.addGatewayMetadata({
    label: 'client_ip_account_avoidance_update',
    metadata: {
      reason,
      confirmedAccountIds: result.confirmedAccountIds
    }
  })
}

function once(callback: () => void): () => void {
  let called = false
  return () => {
    if (called) return
    called = true
    callback()
  }
}

async function settleHotQualityExplorationSafely(
  preflight: OpenAIGatewayDispatchContext,
  outcome: 'dispatched' | 'not_dispatched'
): Promise<void> {
  try {
    await preflight.settleHotQualityExplorationAfterDispatch?.(outcome)
  } catch (error) {
    getRequestLogger().warn(errorLogFields(error, {
      event: 'gateway_hot_quality_exploration_settlement_failed',
      outcome,
      accountRuntimeKey: preflight.hotQualityExplorationReservation?.accountRuntimeKey
    }), '结算热质量同层探索失败')
  }
}

function hotQualityOutcomeForTransportFailure(kind: 'timeout' | 'read_incomplete'): 'timeout' | 'read_interruption' {
  return kind === 'timeout' ? 'timeout' : 'read_interruption'
}

function attachClientIpSlotRelease(res: Response, preflight: OpenAIGatewayDispatchContext): () => void {
  const releaseClientIpSlot = once(preflight.releaseClientIpConcurrency)
  res.once('finish', releaseClientIpSlot)
  res.once('close', releaseClientIpSlot)
  return releaseClientIpSlot
}

export function attachGatewayResponseErrorBoundary(
  res: Response,
  options: GatewayResponseErrorBoundaryOptions
): void {
  const reportError = options.reportError ?? ((error: unknown, fields: Record<string, unknown>, message: string) => {
    getRequestLogger().error(errorLogFields(error, fields), message)
  })
  res.on('error', (error: Error) => {
    const errorCode = nodeErrorProperty(error, 'code')
    const downstreamDisconnected = isKnownDownstreamResponseDisconnect(errorCode)
    const writableFinished = res.writableFinished
    const gatewayForcedDownstreamClose = isGatewayForcedDownstreamClose(res)
    const shouldHandleAsDownstreamFailure = !writableFinished && !gatewayForcedDownstreamClose
    if (shouldHandleAsDownstreamFailure && downstreamDisconnected) {
      options.auditCapture.markDownstreamClosed()
    }
    if (shouldHandleAsDownstreamFailure && !options.abortController.signal.aborted) {
      options.abortController.abort('downstream_response_error')
    }
    reportError(error, {
      event: 'gateway_downstream_response_error',
      traceId: options.traceId,
      endpoint: options.endpoint,
      downstreamDisconnected,
      handledAsDownstreamFailure: shouldHandleAsDownstreamFailure,
      errorCode,
      errorSyscall: nodeErrorProperty(error, 'syscall'),
      statusCode: res.statusCode,
      headersSent: res.headersSent,
      writableEnded: res.writableEnded,
      writableFinished,
      gatewayForcedDownstreamClose,
      destroyed: res.destroyed
    }, downstreamDisconnected ? '网关下游响应连接已关闭' : '网关下游响应发生未预期错误')
    if (!shouldHandleAsDownstreamFailure) return
    void options.clearActiveDownstreamSessionAffinity().catch((cleanupError: unknown) => {
      reportError(cleanupError, {
        event: 'gateway_downstream_session_affinity_clear_failed',
        traceId: options.traceId,
        endpoint: options.endpoint,
        responseErrorCode: errorCode
      }, '下游响应错误后清理会话亲和性失败')
    })
  })
}

function isKnownDownstreamResponseDisconnect(errorCode: string | undefined): boolean {
  return errorCode === 'EPIPE'
    || errorCode === 'ECONNRESET'
    || errorCode === 'ECONNABORTED'
    || errorCode === 'ERR_STREAM_DESTROYED'
}

function nodeErrorProperty(error: unknown, property: 'code' | 'syscall'): string | undefined {
  if (!error || typeof error !== 'object') return undefined
  const value = (error as Record<string, unknown>)[property]
  return typeof value === 'string' ? value : undefined
}

export function attachAccountSlotRelease(
  res: Response,
  releaseConcurrency: () => void,
  options: {
    deferUntilExplicitRelease?: boolean
    clientAbortSignal?: AbortSignal
  } = {}
): () => void {
  const onHttpComplete = () => {
    if (options.deferUntilExplicitRelease !== true) release()
  }
  const onClientAbort = () => release()
  const release = once(() => {
    res.off('finish', onHttpComplete)
    res.off('close', onHttpComplete)
    options.clientAbortSignal?.removeEventListener('abort', onClientAbort)
    releaseConcurrency()
  })
  res.once('finish', onHttpComplete)
  res.once('close', onHttpComplete)
  options.clientAbortSignal?.addEventListener('abort', onClientAbort, { once: true })
  if (options.clientAbortSignal?.aborted) release()
  return release
}

function streamServerRetryFallbackReason(retryReason: StreamServerRetryReason): string {
  if (retryReason === 'response_inspection') {
    return 'response_inspection_server_retry_exhausted'
  }
  if (retryReason === 'upstream_protocol_failure') {
    return 'upstream_protocol_server_retry_exhausted'
  }
  return 'stream_server_retry_exhausted'
}

function streamRetryDispatchAccounts(accounts: UpstreamAccount[], excludedAccountIds: Set<string>): UpstreamAccount[] {
  if (excludedAccountIds.size === 0) {
    return accounts
  }
  return accounts.filter((account) => !excludedAccountIds.has(account.id))
}

function normalRouteSpeedFirstByteDeadlineMs(config?: NormalRouteSpeedFirstRuntimeConfig): number | undefined {
  return config?.firstByteDeadlineMs
}

async function speedFirstRouteEligibleDispatchAccounts(
  accounts: UpstreamAccount[],
  excludedAccountIds: Set<string>,
  latencyScope: ReturnType<typeof normalRouteLatencyDegradationScope>,
  requestAttemptTracker: GatewayRequestAttemptTracker
): Promise<UpstreamAccount[]> {
  const remainingAccounts = streamRetryDispatchAccounts(accounts, excludedAccountIds)
    .filter((account) => requestAttemptTracker.canAttemptAccount({
      accountRuntimeKey: gatewayAccountRuntimeKey(account),
      physicalCredentialKey: account.credentialSourceAccountId?.trim() || account.id
    }).allowed)
  if (!latencyScope || remainingAccounts.length === 0) {
    return remainingAccounts
  }
  const decisionOperations = normalRouteSpeedFirstDecisionOperations()
  const states = await Promise.all(remainingAccounts.map(async (account) => ({
    account,
    degraded: await decisionOperations.isAccountLatencyDegradedAsync(account, latencyScope)
  })))
  return states
    .filter((item) => !item.degraded)
    .map((item) => item.account)
}

function shouldSendDispatchExhaustedProtocolRetry(
  preflight: OpenAIGatewayDispatchContext,
  error: unknown,
  res: Response
): error is UpstreamAttemptError {
  return error instanceof UpstreamAttemptError
    && !error.terminalUpstreamFailure
    && (
      error.lastAttempt?.status === undefined
      || (
        error.lastAttempt.status >= 200
        && error.lastAttempt.status < 300
      )
    )
    && !error.agentGuidanceResponse
    && preflight.clientStrategy.retryCoordination.preCommitFailureSignal === 'protocol_error_event'
    && !preflight.downstreamCommitState.semanticCommitted
    && !res.writableEnded
    && !res.destroyed
}

function shouldKeepCodexCompactSseAliveDuringUpstreamWait(
  req: Request,
  preflight: OpenAIGatewayDispatchContext
): boolean {
  const strategy = preflight.clientStrategy
  return requestStream(req)
    && strategy.clientProfile === 'codex'
    && strategy.codexCompactionExpected === true
    && strategy.downstreamProtocol === 'responses_sse'
}

function shouldSendTransportCommittedCodexCompactFailure(
  preflight: OpenAIGatewayDispatchContext,
  res: Response
): boolean {
  const strategy = preflight.clientStrategy
  return res.headersSent
    && !res.writableEnded
    && !res.destroyed
    && preflight.downstreamCommitState.transportCommitted
    && preflight.downstreamCommitState.downstreamBytesWritten > 0
    && !preflight.downstreamCommitState.semanticCommitted
    && strategy.clientProfile === 'codex'
    && strategy.codexCompactionExpected === true
    && strategy.downstreamProtocol === 'responses_sse'
}

function shouldExcludeCurrentAccountForStreamRetry(decision: ResponseInspectionDecision): boolean {
  return decision.accountSwitch === 'request_next_account'
    || decision.accountSwitch === 'avoid_account_ttl'
    || decision.accountSwitch === 'avoid_upstream_bucket_ttl'
    || decision.accountState === 'runtime_avoidance'
}

async function sendStreamServerRetryExhaustedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  retryReason: StreamServerRetryReason
  decision?: ResponseInspectionDecision
  message: string
  errorCode?: string
  uncommittedResponseBody?: Buffer
  accountId?: string
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
  downstreamCommitState?: GatewayDownstreamCommitState
}): Promise<void> {
  const message = input.message || '服务端流式重试未找到可用账号'
  const downstreamCommitted = input.downstreamCommitState?.transportCommitted === true
    && input.downstreamCommitState.downstreamBytesWritten > 0
  const failureSignal = downstreamCommitted
    ? input.clientStrategy?.retryCoordination.committedFailureSignal
    : input.clientStrategy?.retryCoordination.preCommitFailureSignal
  if (
    failureSignal === 'protocol_error_event'
    && !input.res.writableEnded
    && !input.res.destroyed
  ) {
    await sendPreCommitStreamRetryExhaustedResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      retryReason: input.retryReason,
      message,
      errorCode: gatewayStreamClientRetryErrorCode,
      uncommittedResponseBody: input.uncommittedResponseBody,
      accountId: input.accountId,
      clientStrategy: input.clientStrategy,
      downstreamCommitState: input.downstreamCommitState
    })
    return
  }
  if (downstreamCommitted) {
    input.auditCapture.addGatewayMetadata({
      label: 'stream_server_retry_exhausted',
      metadata: {
        retryReason: input.retryReason,
        upstreamErrorCode: input.errorCode,
        responseMode: 'committed_disconnect',
        clientProfile: input.clientStrategy?.clientProfile,
        downstreamProtocol: input.clientStrategy?.downstreamProtocol
      }
    })
    if (!input.res.writableEnded && !input.res.destroyed) {
      markGatewayForcedDownstreamClose(input.res, 'stream_retry_exhausted')
      input.res.destroy()
    }
    input.auditCapture.finalize({
      outcome: 'stream_failed',
      success: false,
      statusCode: input.res.statusCode,
      responseHeaders: responseHeadersToObject(input.res),
      responsePartType: 'gateway_response',
      errorPhase: 'stream',
      errorCode: gatewayStreamClientRetryErrorCode,
      errorMessage: message,
      accountId: input.accountId
    })
    return
  }
  const responsePayload = gatewayErrorPayload(
    '上游暂时不可用，请重试',
    'service_unavailable',
    gatewayStreamClientRetryErrorCode
  )
  input.auditCapture.addGatewayMetadata({
    label: 'stream_server_retry_exhausted',
    metadata: {
      retryReason: input.retryReason,
      upstreamErrorCode: input.errorCode,
      responseMode: 'pre_commit_http_error',
      policyId: input.decision?.policyId,
      policyName: input.decision?.policyName,
      accountSwitch: input.decision?.accountSwitch,
      retryEnabled: input.decision?.retryEnabled,
      matchedField: input.decision?.matchedField,
      matchedValue: input.decision?.matchedValue
    }
  })
  await sendGatewayFailureResponse({
    req: input.req,
    res: input.res,
    auditCapture: input.auditCapture,
    usageContext: input.usageContext,
    startedAt: input.startedAt,
    statusCode: 503,
    responsePayload,
    audit: {
      outcome: 'upstream_failed',
      errorPhase: 'dispatch',
      errorCode: gatewayStreamClientRetryErrorCode,
      errorMessage: message
    },
    recordUsage: false,
    usageErrorMessage: message
  })
}

async function sendPreCommitStreamRetryExhaustedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  retryReason: StreamServerRetryReason
  message: string
  errorCode?: string
  uncommittedResponseBody?: Buffer
  accountId?: string
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
  downstreamCommitState?: GatewayDownstreamCommitState
}): Promise<void> {
  const clientVisibleMessage = gatewayStreamClientRetryMessage
  input.auditCapture.addGatewayMetadata({
    label: 'stream_server_retry_exhausted',
    metadata: {
      retryReason: input.retryReason,
      errorCode: input.errorCode,
      clientProfile: input.clientStrategy?.clientProfile,
      downstreamProtocol: input.clientStrategy?.downstreamProtocol,
      responseMode: input.downstreamCommitState?.transportCommitted === true
        && input.downstreamCommitState.downstreamBytesWritten > 0
        ? 'committed_retryable_sse'
        : 'pre_commit_http_error'
    }
  })
  await rememberCodexTurnFailureWhenClientRetryIsVisible(input)
  if (input.clientStrategy?.retryCoordination.preCommitFailureSignal !== 'protocol_error_event') {
    const responsePayload = gatewayErrorPayload(
      clientVisibleMessage,
      'service_unavailable',
      input.errorCode ?? gatewayStreamClientRetryErrorCode
    )
    await sendGatewayFailureResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      statusCode: 503,
      responsePayload,
      audit: {
        outcome: 'stream_failed',
        errorPhase: 'stream',
        errorCode: input.errorCode ?? gatewayStreamClientRetryErrorCode,
        errorMessage: clientVisibleMessage
      },
      recordUsage: false,
      usageErrorMessage: clientVisibleMessage
    })
    return
  }
  if (!input.res.headersSent) {
    input.res.status(200)
    input.res.setHeader('content-type', 'text/event-stream; charset=utf-8')
    input.res.setHeader('cache-control', 'no-cache, no-transform')
    input.res.setHeader('x-accel-buffering', 'no')
  }
  const failureEvent = writeGatewayStreamFailureEvent(
    input.res,
    clientVisibleMessage,
    input.errorCode,
    gatewayProtocolClientErrorProtocolForRequest(input.req),
    input.clientStrategy?.downstreamProtocol
  )
  const responseBody = input.uncommittedResponseBody
    ? Buffer.concat([input.uncommittedResponseBody, failureEvent ?? Buffer.alloc(0)])
    : failureEvent
  if (!input.res.writableEnded && !input.res.destroyed && input.uncommittedResponseBody?.length) {
    input.res.write(input.uncommittedResponseBody)
  }
  if (!input.res.writableEnded && !input.res.destroyed && failureEvent) {
    input.res.write(failureEvent)
  }
  if (!input.res.writableEnded && !input.res.destroyed) {
    input.res.end()
  }
  input.auditCapture.finalize({
    outcome: 'stream_failed',
    success: false,
    statusCode: input.res.statusCode,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody,
    responsePartType: 'gateway_response',
    errorPhase: 'stream',
    errorCode: input.errorCode,
    errorMessage: clientVisibleMessage,
    accountId: input.accountId
  })
}

async function rememberCodexTurnFailureWhenClientRetryIsVisible(input: {
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
  accountId?: string
  errorCode?: string
  message: string
}): Promise<void> {
  if (
    isAccountDiagnosticTrafficSource(input.usageContext.trafficSource)
    || input.errorCode !== gatewayStreamClientRetryErrorCode
    || !input.accountId
    || input.clientStrategy?.allowCodexTurnAccountAvoidance !== true
  ) {
    return
  }
  const codexTurnFailure = await rememberCodexTurnStreamFailureAsync(input.clientStrategy, input.accountId, {
    errorCode: input.errorCode,
    message: input.message
  })
  if (!codexTurnFailure) {
    return
  }
  input.auditCapture.addGatewayMetadata({
    label: 'codex_turn_stream_failure',
    metadata: {
      stateKey: codexTurnFailure.stateKey,
      failureCount: codexTurnFailure.failureCount,
      failedAccountIds: codexTurnFailure.failedAccountIds,
      accountId: input.accountId
    }
  })
}

async function recordKnownClientIpRequestError(
  error: unknown,
  usageContext: GatewayFailureUsageContext,
  auditCapture: ReturnType<typeof createAuditCapture>
): Promise<void> {
  if (isAccountDiagnosticTrafficSource(usageContext.trafficSource)) {
    return
  }
  const sample = clientIpRequestErrorSample(error)
  if (!sample) {
    return
  }
  let result: Awaited<ReturnType<typeof recordClientIpErrorCircuitSampleAsync>>
  try {
    result = await recordClientIpErrorCircuitSampleAsync({
      systemAccountId: usageContext.systemAccountId,
      apiKeyId: usageContext.apiKeyId,
      groupId: usageContext.groupId,
      clientIp: usageContext.clientIp,
      endpoint: usageContext.endpoint,
      reason: sample.reason,
      signature: sample.signature
    })
  } catch (stateError) {
    getRequestLogger().warn(errorLogFields(stateError, {
      event: 'gateway_client_ip_error_circuit_record_failed',
      systemAccountId: usageContext.systemAccountId,
      apiKeyId: usageContext.apiKeyId,
      groupId: usageContext.groupId,
      clientIp: usageContext.clientIp,
      reason: sample.reason
    }), '客户端 IP 错误电路写入失败，保留原始请求错误响应')
    auditCapture.addGatewayMetadata({
      label: 'client_ip_error_circuit_state_failure',
      metadata: {
        operation: 'record_failure',
        reason: sample.reason
      }
    })
    return
  }
  if (!result.blocked) {
    return
  }
  getRequestLogger().warn({
    event: 'gateway_client_ip_error_circuit_opened',
    reason: sample.reason,
    retryAfterSeconds: result.retryAfterSeconds,
    failureCount: result.failureCount,
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp
  }, '客户端 IP 级错误熔断已打开')
  auditCapture.addGatewayMetadata({
    label: 'client_ip_error_circuit',
    metadata: {
      opened: true,
      reason: sample.reason,
      retryAfterSeconds: result.retryAfterSeconds,
      failureCount: result.failureCount
    }
  })
}

function clientIpRequestErrorSample(error: unknown): { reason: 'adapter_request_validation'; signature: string } | undefined {
  if (error instanceof OpenAIOAuthCodexAdapterError) {
    return {
      reason: 'adapter_request_validation',
      signature: [error.type, error.code].filter(Boolean).join('|') || error.message
    }
  }
  return undefined
}

function accountCircuitTransportFailure(error: unknown): GatewayAccountCircuitTransportFailure {
  const reason = error instanceof Error && error.message.trim()
    ? error.message.trim()
    : '上游响应正文读取未完成'
  const diagnostic = [
    error instanceof Error ? error.name : '',
    typeof error === 'object' && error !== null && 'code' in error ? String(error.code) : '',
    reason
  ].join(' ').toLowerCase()
  return {
    kind: /timeout|timedout|timed out|etimedout|超时/.test(diagnostic) ? 'timeout' : 'read_incomplete',
    reason
  }
}
