import {
  Router,
  type NextFunction,
  type Request,
  type Response,
} from 'express'

import { createTraceId, getRequestLogger, getTraceId } from '../../shared/request-context.js'
import { errorLogFields } from '../../shared/logger.js'
import {
  extractClientIp,
  requestEndpoint
} from './request/metadata.js'
import {
  buildUsageRequestSnapshot,
  buildUsageResponseSnapshot
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
import { createAuditCapture, responseHeadersToObject } from './audit/capture.service.js'
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
  probeCodexSwitchCandidateAccount,
  type CodexSwitchProbeResult
} from './client-profiles/codex-switch-probe.js'
import {
  finalizeHandledUpstreamResponse,
  handleNonStreamUpstreamResponse,
  handleStreamUpstreamResponse,
  type StreamServerRetryReason
} from './response/finalization.js'
import { rememberCodexTurnStreamFailureAsync } from './client-profiles/codex-turn-retry.service.js'
import { sendGatewayFailureResponse } from './response/failure-response.js'
import { handleUpstreamRequestError } from './response/failure-dispatch.js'
import { handleGatewayRequestKnownErrorResponse } from './request/error-response.js'
import {
  prepareOpenAIGatewayDispatchContext,
  prepareApiKeyGroupFallbackDispatchContext,
  type OpenAIGatewayDispatchContext,
  type OpenAIGatewayRequestIdentity
} from './request/preflight.js'
import { resolveNextHybridGatewayRoute } from './hybrid/routing.service.js'
import { appendHybridQualityRepairInstruction } from './hybrid/quality-repair.service.js'
import {
  fetchFirstAvailableUpstream,
  UpstreamAttemptError
} from './dispatch/upstream-dispatch.js'
import type { UpstreamAttempt } from './upstream/attempt.js'
import type { ResponseInspectionDecision } from './response/inspection.js'
import type { GatewaySettings } from './policy/account-error-policy.service.js'
import type { RouteStrategySpeedFirstConfig } from '../../domain/types.js'
import { OpenAIOAuthCodexAdapterError } from './adapters/gpt-codex/oauth-adapter.js'
import { recordClientIpErrorCircuitSampleAsync } from './runtime/client-ip-error-circuit.service.js'
import {
  confirmClientIpAccountAvoidanceAfterFinalFailureAsync,
  transferClientIpAccountPendingFailures
} from './runtime/client-ip-account-avoidance.service.js'
import {
  recordGatewayFailure,
  type GatewayFailureUsageContext
} from './usage/records.js'
import { isGatewayForcedDownstreamClose } from './upstream/body.js'
import {
  isAccountProbeTrafficSource,
  normalizeOpenAIGatewayTrafficSource,
  type OpenAIGatewayTrafficSource
} from './usage/traffic-source.js'
import { resolveOpenAIGatewayRequestLane } from './protocols/openai-v1/request-lane.js'
import { forgetOpenAIAccountForSession } from './runtime/session-affinity.service.js'
import { gatewayProtocolClientErrorProtocolForRequest } from './protocols/registry.js'
import {
  normalRouteLatencyDegradationScope,
  isNormalRouteAccountLatencyDegradedAsync,
  recordNormalRouteFirstByteSlowAsync,
  recordNormalRouteFirstByteSuccessAsync,
  type NormalRouteLatencySlowResult
} from './runtime/normal-route-latency-degradation.service.js'

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
  onUpstreamAttemptDiagnostic?: (lastAttempt: UpstreamAttempt) => void
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
  const abortController = new AbortController()
  const traceId = getTraceId() ?? createTraceId()
  const clientIp = extractClientIp(req)
  const endpoint = requestEndpoint(req)
  const requestLane = resolveOpenAIGatewayRequestLane(req)
  const trafficSource = normalizeOpenAIGatewayTrafficSource(options.trafficSource)
  const requestSnapshot = buildUsageRequestSnapshot(req, traceId, clientIp)
  const auditCapture = createAuditCapture({
    req,
    traceId,
    clientIp,
    startedAtMs: startedAt,
    trafficSource,
    captureMode: options.auditCaptureMode ?? (isAccountProbeTrafficSource(trafficSource) ? 'metadata_only' : 'default')
  })
  let activeDownstreamSessionAffinity: { key: string; accountId: string } | undefined
  const clearActiveDownstreamSessionAffinity = () => {
    if (!activeDownstreamSessionAffinity) {
      return
    }
    const binding = activeDownstreamSessionAffinity
    activeDownstreamSessionAffinity = undefined
    forgetOpenAIAccountForSession(binding.key, binding.accountId)
  }
  req.once('aborted', () => {
    auditCapture.markClientAborted()
    abortController.abort()
    clearActiveDownstreamSessionAffinity()
  })
  res.once('close', () => {
    if (!isGatewayForcedDownstreamClose(res)) {
      if (!res.writableEnded) {
        auditCapture.markClientAborted()
        abortController.abort()
      }
      if (abortController.signal.aborted) {
        clearActiveDownstreamSessionAffinity()
      }
    }
  })

  const preflight = await prepareOpenAIGatewayDispatchContext({
    req,
    res,
    auditCapture,
    options: { ...options, trafficSource, requestLane },
    startedAt,
    traceId,
    clientIp,
    endpoint,
    requestSnapshot,
    signal: abortController.signal
  })
  if (!preflight) {
    return
  }
  let currentPreflight = preflight
  let releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
  let streamServerRetryExcludedAccountIds = new Set<string>()
  let streamServerRetryCount = 0
  let speedFirstByteRetryCount = 0
  let speedFirstRetryCandidateAccountIds: Set<string> | undefined
  let fallbackSwitchCount = 0
  const exhaustedAccountIds = new Set<string>()
  const nonStreamResponseStartedFailedAccountIds = new Set<string>()
  const switchToFallbackGroup = async (
    reason: string,
    input: { allowCandidateWrap?: boolean } = {}
  ): Promise<'none' | 'switched' | 'completed'> => {
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
        requestLane: currentPreflight.requestLane
      },
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal: abortController.signal,
      reason,
      apiKeyRecord: fallbackApiKeyRecord,
      systemAccountId: gatewayUsageContext.systemAccountId,
      apiKeyId: gatewayUsageContext.apiKeyId,
      groupId: gatewayUsageContext.groupId,
      trafficSource: gatewayUsageContext.trafficSource,
      requestLane: currentPreflight.requestLane,
      requestClientCompatibility: currentPreflight.clientStrategy.requestClientCompatibility,
      groupFallbackApiKeyRecord: currentPreflight.groupFallbackApiKeyRecord ?? currentPreflight.apiKeyRecord,
      excludedAccountIds: exhaustedAccountIds,
      allowCandidateWrap: input.allowCandidateWrap
    })
    if (!fallback.attempted) {
      return 'none'
    }
    if (!fallback.context) {
      return 'completed'
    }
    fallbackSwitchCount += 1
    transferClientIpAccountPendingFailures(
      currentPreflight.clientIpAccountAvoidanceTracker,
      fallback.context.clientIpAccountAvoidanceTracker
    )
    releaseClientIpSlot()
    currentPreflight = fallback.context
    releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
    streamServerRetryExcludedAccountIds = new Set<string>()
    streamServerRetryCount = 0
    speedFirstByteRetryCount = 0
    speedFirstRetryCandidateAccountIds = undefined
    return 'switched'
  }
  const switchToHybridQualityUpgrade = async (
    reason: string
  ): Promise<'none' | 'switched' | 'completed'> => {
    const hybridRoute = currentPreflight.hybridRoute
    if (!hybridRoute) {
      return 'none'
    }
    const nextRoute = await resolveNextHybridGatewayRoute({
      req,
      apiKeyRecord: hybridRoute.apiKeyRecord,
      currentRoute: hybridRoute.route,
      requestClientCompatibility: currentPreflight.clientStrategy.requestClientCompatibility,
      signal: abortController.signal
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
        identity: {
          systemAccountId: currentPreflight.usageContext.systemAccountId,
          apiKeyId: currentPreflight.usageContext.apiKeyId,
          groupId: nextRoute.groupId
        },
        apiKeyRecord: nextRoute.apiKeyRecord,
        candidateAccounts: nextRoute.accounts,
        responseInspectionPolicies: nextRoute.responseInspectionPolicies,
        trafficSource,
        requestLane: currentPreflight.requestLane
      },
      startedAt,
      traceId,
      clientIp,
      endpoint,
      requestSnapshot,
      signal: abortController.signal
    })
    if (!context) {
      return 'completed'
    }
    releaseClientIpSlot()
    currentPreflight = {
      ...context,
      hybridRoute: {
        apiKeyRecord: nextRoute.apiKeyRecord,
        config: hybridRoute.config,
        scoring: hybridRoute.scoring,
        route: nextRoute.route,
        targetModel: nextRoute.targetModel,
        affinityApplied: false,
        scoringFallbackApplied: hybridRoute.scoringFallbackApplied,
        qualityRetryCount: hybridRoute.qualityRetryCount + 1
      }
    }
    releaseClientIpSlot = attachClientIpSlotRelease(res, currentPreflight)
    streamServerRetryExcludedAccountIds = new Set<string>()
    streamServerRetryCount = 0
    speedFirstByteRetryCount = 0
    speedFirstRetryCandidateAccountIds = undefined
    return 'switched'
  }

  try {
    while (true) {
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
        normalRouteSpeedFirstConfig,
        normalRouteLatencyDegradationApplied,
        codexTurnAccountAvoidanceApplied,
        codexTurnAvoidedAccountIds
      } = currentPreflight
      let dispatchAccounts = streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds)
      if (speedFirstRetryCandidateAccountIds) {
        dispatchAccounts = dispatchAccounts.filter((account) => speedFirstRetryCandidateAccountIds?.has(account.id))
      }
      if (dispatchAccounts.length === 0) {
        for (const accountId of streamServerRetryExcludedAccountIds) {
          exhaustedAccountIds.add(accountId)
        }
        const fallbackSwitch = await switchToFallbackGroup('upstream_accounts_exhausted', { allowCandidateWrap: true })
        if (fallbackSwitch === 'completed') {
          return
        }
        if (fallbackSwitch === 'switched') {
          continue
        }
        throw new UpstreamAttemptError('没有可用的上游账户')
      }
      if (codexTurnAccountAvoidanceApplied) {
        const probeSelection = await selectCodexProbeVerifiedDispatchAccount({
          accounts: dispatchAccounts,
          avoidedAccountIds: new Set(codexTurnAvoidedAccountIds ?? []),
          req,
          systemAccountId: gatewayUsageContext.systemAccountId,
          groupId: gatewayUsageContext.groupId,
          auditCapture,
          signal: abortController.signal
        })
        for (const probe of probeSelection.probes) {
          if (!probe.success) {
            streamServerRetryExcludedAccountIds.add(probe.accountId)
          }
        }
        auditCapture.addGatewayMetadata({
          label: 'codex_switch_probe',
          metadata: {
            selectedAccountId: probeSelection.account?.id,
            selectedAccountName: probeSelection.account?.name,
            probeCount: probeSelection.probes.length,
            probes: probeSelection.probes.map(codexSwitchProbeAuditMetadata)
          }
        })
        if (!probeSelection.account) {
          for (const accountId of streamServerRetryExcludedAccountIds) {
            exhaustedAccountIds.add(accountId)
          }
          const fallbackSwitch = await switchToFallbackGroup('codex_switch_probe_failed', { allowCandidateWrap: true })
          if (fallbackSwitch === 'completed') {
            return
          }
          if (fallbackSwitch === 'switched') {
            continue
          }
          await sendCodexSwitchProbeFailedResponse({
            req,
            res,
            auditCapture,
            usageContext: gatewayUsageContext,
            startedAt,
            probes: probeSelection.probes
          })
          return
        }
        dispatchAccounts = [probeSelection.account]
      }
      const speedFirstRouteOverrideActive = normalRouteLatencyDegradationApplied === true || speedFirstRetryCandidateAccountIds !== undefined
      if (speedFirstRouteOverrideActive) {
        forgetOpenAIAccountForSession(sessionAffinityKey)
      }
      const dispatchSessionAffinityKey = speedFirstRouteOverrideActive ? undefined : sessionAffinityKey
      let upstreamResult: Awaited<ReturnType<typeof fetchFirstAvailableUpstream>>
      try {
        upstreamResult = await fetchFirstAvailableUpstream(
          req,
          dispatchAccounts,
          activeGatewaySettings,
          gatewayUsageContext,
          auditCapture,
          dispatchSessionAffinityKey,
          abortController.signal,
          clientIpAccountAvoidanceTracker,
          currentPreflight.requestLane,
          currentPreflight.groupSchedulingPolicy,
          options.disableAccountStateMutation !== true,
          currentPreflight.clientStrategy.requestClientCompatibility,
          modelPriority
        )
      } catch (error) {
        if (error instanceof UpstreamAttemptError) {
          for (const accountId of nonStreamResponseStartedFailedAccountIds) {
            exhaustedAccountIds.add(accountId)
          }
          for (const accountId of error.failedAccountIds) {
            exhaustedAccountIds.add(accountId)
            if (codexTurnAccountAvoidanceApplied) {
              streamServerRetryExcludedAccountIds.add(accountId)
            }
          }
          if (codexTurnAccountAvoidanceApplied) {
            continue
          }
          const fallbackReason = error.agentGuidanceResponse
            ? 'account_scoped_agent_guidance_exhausted'
            : 'upstream_accounts_exhausted'
          const fallbackSwitch = await switchToFallbackGroup(fallbackReason, { allowCandidateWrap: true })
          if (fallbackSwitch === 'completed') {
            return
          }
          if (fallbackSwitch === 'switched') {
            continue
          }
        }
        throw error
      }
      const { account, response: upstreamResponse, upstreamUrl, auditAttemptId, attemptStartedAt, releaseConcurrency, markFirstOutput, confirmSameAccountApiKeyFailures } = upstreamResult
      const speedFirstFirstByteDeadlineMs = normalRouteSpeedFirstByteDeadlineMs(normalRouteSpeedFirstConfig)
      const speedFirstLatencyScope = normalRouteLatencyDegradationScope({
        systemAccountId: gatewayUsageContext.systemAccountId,
        routeStrategyId: apiKeyRecord?.route_strategy_id,
        groupId: gatewayUsageContext.groupId
      })
      let speedFirstSlowObservedForAttempt: NormalRouteLatencySlowResult | undefined
      let speedFirstCutoverAllowedAtDeadline = false
      const onSpeedFirstFirstByteDeadline = speedFirstFirstByteDeadlineMs !== undefined && normalRouteSpeedFirstConfig
        ? async () => {
            const alreadyDegraded = await isNormalRouteAccountLatencyDegradedAsync(account, speedFirstLatencyScope)
            speedFirstSlowObservedForAttempt = await recordNormalRouteFirstByteSlowAsync(
              account,
              speedFirstLatencyScope,
              normalRouteSpeedFirstConfig,
              `普通路由速度优先首字观察阈值 ${speedFirstFirstByteDeadlineMs}ms 已到达`
            )
            const nextExcludedAccountIds = new Set(streamServerRetryExcludedAccountIds)
            nextExcludedAccountIds.add(account.id)
            const remainingAccounts = await speedFirstRouteEligibleDispatchAccounts(accounts, nextExcludedAccountIds, speedFirstLatencyScope)
            const remainingCandidateCount = remainingAccounts.length
            const maxRetries = normalRouteSpeedFirstConfig.maxFirstByteRetriesPerRequest
            const totalWaitTimedOut = streamClientTotalWaitTimedOut(activeGatewaySettings, startedAt)
            const degradedForCutover = alreadyDegraded || speedFirstSlowObservedForAttempt?.degraded === true
            speedFirstCutoverAllowedAtDeadline = degradedForCutover
              && speedFirstByteRetryCount < maxRetries
              && remainingCandidateCount > 0
              && !totalWaitTimedOut
            auditCapture.addGatewayMetadata({
              label: 'normal_route_speed_first_slow_observed',
              metadata: {
                accountId: account.id,
                accountName: account.name,
                firstTokenMs: undefined,
                thresholdMs: normalRouteSpeedFirstConfig.firstByteThresholdMs,
                elapsedMs: Date.now() - attemptStartedAt,
                observedAt: 'first_byte_deadline',
                alreadyDegraded,
                slowCount: speedFirstSlowObservedForAttempt?.slowCount,
                degraded: speedFirstSlowObservedForAttempt?.degraded,
                degradedUntil: speedFirstSlowObservedForAttempt?.degradedUntil,
                nextProbeAt: speedFirstSlowObservedForAttempt?.nextProbeAt,
                cutoverAllowed: speedFirstCutoverAllowedAtDeadline,
                retryBlockedReason: speedFirstCutoverAllowedAtDeadline
                  ? undefined
                  : totalWaitTimedOut
                    ? 'client_total_wait_timeout'
                    : remainingCandidateCount <= 0
                      ? 'no_remaining_candidate'
                      : !degradedForCutover
                        ? 'slow_observation_not_degraded'
                        : 'max_retry_exceeded',
                retryCount: speedFirstByteRetryCount,
                maxRetries,
                remainingCandidateCount,
                remainingCandidateAccountIds: remainingAccounts.map((item) => item.id)
              }
            })
            return speedFirstCutoverAllowedAtDeadline ? 'abort' as const : 'continue' as const
          }
        : undefined

      try {
        activeDownstreamSessionAffinity = sessionAffinityKey
          ? { key: sessionAffinityKey, accountId: account.id }
          : undefined
        const contentType = upstreamResponse.headers.get('content-type') ?? ''
        const shouldHandleAsStream = shouldHandleOpenAIUpstreamResponseAsStream({
          contentType,
          streamRequest: isEffectiveOpenAIStreamRequest(req, account)
        })
        persistOpenAICodexHeadersIfNeeded(account, upstreamResponse.headers, gatewayUsageContext.trafficSource)

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
            usageContext: gatewayUsageContext,
            startedAt: attemptStartedAt,
            signal: abortController.signal,
            firstByteDeadlineMs: speedFirstFirstByteDeadlineMs,
            onFirstByteDeadline: onSpeedFirstFirstByteDeadline,
            sessionAffinityKey,
            clientStrategy,
            responseInspectionPolicies,
            hybridRoute: currentPreflight.hybridRoute,
            markFirstOutput,
            clientIpAccountAvoidanceTracker,
            accountStateMutationEnabled: options.disableAccountStateMutation !== true,
            codexTurnAccountAvoidanceApplied
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
              usageContext: gatewayUsageContext,
              startedAt: attemptStartedAt,
              signal: abortController.signal,
              firstByteDeadlineMs: speedFirstFirstByteDeadlineMs,
              onFirstByteDeadline: onSpeedFirstFirstByteDeadline,
              sessionAffinityKey,
              responseInspectionPolicies,
              hybridRoute: currentPreflight.hybridRoute,
              clientStrategy,
              markFirstOutput,
              clientIpAccountAvoidanceTracker,
              accountStateMutationEnabled: options.disableAccountStateMutation !== true
            })
          } catch (error) {
            if (res.headersSent || res.writableEnded || res.destroyed) {
              throw error
            }
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
              signal: abortController.signal,
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
              accountStateMutationEnabled: options.disableAccountStateMutation !== true
            })
            nonStreamResponseStartedFailedAccountIds.add(account.id)
            if (requestErrorResult.action === 'skip_account') {
              streamServerRetryExcludedAccountIds.add(account.id)
              continue
            }
            throw error
          }
        }
        activeDownstreamSessionAffinity = undefined
        if (handledResponse.alreadyFinalized) {
          return
        }
        if (handledResponse.retryUpstream) {
          if (handledResponse.retryReason === 'speed_first_first_byte_timeout') {
            speedFirstByteRetryCount += 1
            streamServerRetryExcludedAccountIds.add(account.id)
            const remainingAccounts = await speedFirstRouteEligibleDispatchAccounts(accounts, streamServerRetryExcludedAccountIds, speedFirstLatencyScope)
            const remainingCandidateCount = remainingAccounts.length
            const maxRetries = normalRouteSpeedFirstConfig?.maxFirstByteRetriesPerRequest ?? 0
            const totalWaitTimedOut = streamClientTotalWaitTimedOut(activeGatewaySettings, startedAt)
            const retryAllowed = speedFirstCutoverAllowedAtDeadline
              && speedFirstByteRetryCount <= maxRetries
              && remainingCandidateCount > 0
              && !totalWaitTimedOut
            auditCapture.addGatewayMetadata({
              label: 'normal_route_speed_first_retry_dispatch',
              metadata: {
                retryCount: speedFirstByteRetryCount,
                maxRetries,
                retryAllowed,
                retryBlockedReason: retryAllowed
                  ? undefined
                  : totalWaitTimedOut
                    ? 'client_total_wait_timeout'
                    : remainingCandidateCount <= 0
                      ? 'no_remaining_candidate'
                      : !speedFirstCutoverAllowedAtDeadline
                        ? 'cutover_not_confirmed'
                        : 'max_retry_exceeded',
                accountId: account.id,
                remainingCandidateCount,
                excludedAccountIds: [...streamServerRetryExcludedAccountIds],
                thresholdMs: normalRouteSpeedFirstConfig?.firstByteThresholdMs,
                slowCount: speedFirstSlowObservedForAttempt?.slowCount,
                degraded: speedFirstSlowObservedForAttempt?.degraded,
                degradedUntil: speedFirstSlowObservedForAttempt?.degradedUntil,
                nextProbeAt: speedFirstSlowObservedForAttempt?.nextProbeAt,
                cutoverAllowedAtDeadline: speedFirstCutoverAllowedAtDeadline,
                remainingCandidateAccountIds: remainingAccounts.map((item) => item.id),
                errorCode: handledResponse.errorCode
              }
            })
            if (retryAllowed) {
              speedFirstRetryCandidateAccountIds = new Set(remainingAccounts.map((item) => item.id))
              continue
            }
            if (remainingCandidateCount <= 0) {
              for (const accountId of streamServerRetryExcludedAccountIds) {
                exhaustedAccountIds.add(accountId)
              }
              const fallbackSwitch = await switchToFallbackGroup('normal_route_speed_first_exhausted', { allowCandidateWrap: true })
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
              message: totalWaitTimedOut ? streamClientTotalWaitTimeoutMessage(activeGatewaySettings) : handledResponse.message,
              errorCode: handledResponse.errorCode,
              uncommittedResponseBody: handledResponse.uncommittedResponseBody,
              accountId: account.id,
              clientStrategy
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
              clientTotalWaitTimeoutSeconds: activeGatewaySettings.streamClientTotalWaitTimeoutSeconds,
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
              clientStrategy
            })
            return
          }
          if (streamRetryDispatchAccounts(accounts, streamServerRetryExcludedAccountIds).length === 0) {
            for (const accountId of streamServerRetryExcludedAccountIds) {
              exhaustedAccountIds.add(accountId)
            }
            const fallbackReason = streamServerRetryFallbackReason(handledResponse.retryReason)
            const fallbackSwitch = await switchToFallbackGroup(fallbackReason, { allowCandidateWrap: true })
            if (fallbackSwitch !== 'none') {
              if (fallbackSwitch === 'completed') {
                return
              }
              continue
            }
            const totalWaitTimedOut = streamClientTotalWaitTimedOut(activeGatewaySettings, startedAt)
            await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, totalWaitTimedOut ? 'stream_client_total_wait_timeout' : 'stream_server_retry_exhausted')
            await sendStreamServerRetryExhaustedResponse({
              req,
              res,
              auditCapture,
              usageContext: gatewayUsageContext,
              startedAt,
              retryReason: handledResponse.retryReason,
              decision: handledResponse.responseInspection,
              message: totalWaitTimedOut ? streamClientTotalWaitTimeoutMessage(activeGatewaySettings) : handledResponse.message,
              errorCode: handledResponse.errorCode,
              uncommittedResponseBody: handledResponse.uncommittedResponseBody,
              accountId: account.id,
              clientStrategy
            })
            return
          }
          continue
        }
        if (normalRouteSpeedFirstConfig && handledResponse.firstTokenMs !== undefined) {
          if (handledResponse.firstTokenMs > normalRouteSpeedFirstConfig.firstByteThresholdMs) {
            if (!speedFirstSlowObservedForAttempt) {
              const slowResult = await recordNormalRouteFirstByteSlowAsync(
                account,
                speedFirstLatencyScope,
                normalRouteSpeedFirstConfig,
                `普通路由速度优先首字耗时 ${handledResponse.firstTokenMs}ms 超过阈值 ${normalRouteSpeedFirstConfig.firstByteThresholdMs}ms`
              )
              auditCapture.addGatewayMetadata({
                label: 'normal_route_speed_first_slow_observed',
                metadata: {
                  accountId: account.id,
                  firstTokenMs: handledResponse.firstTokenMs,
                  thresholdMs: normalRouteSpeedFirstConfig.firstByteThresholdMs,
                  observedAt: 'response_completed',
                  slowCount: slowResult?.slowCount,
                  degraded: slowResult?.degraded,
                  degradedUntil: slowResult?.degradedUntil,
                  nextProbeAt: slowResult?.nextProbeAt
                }
              })
            }
          } else if (upstreamResponse.ok) {
            const recoveryResult = await recordNormalRouteFirstByteSuccessAsync(
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
                  thresholdMs: normalRouteSpeedFirstConfig.firstByteThresholdMs,
                  cleared: recoveryResult.cleared,
                  recoverySuccessCount: recoveryResult.recoverySuccessCount,
                  requiredRecoverySuccessCount: recoveryResult.requiredRecoverySuccessCount
                }
              })
            }
          }
        }
        await finalizeHandledUpstreamResponse({
          req,
          res,
          account,
          upstreamResponse,
          upstreamUrl,
          auditAttemptId,
          auditCapture,
          settings: activeGatewaySettings,
          usageContext: gatewayUsageContext,
          startedAt,
          signal: abortController.signal,
          result: handledResponse,
          clientIpAccountAvoidanceTracker,
          accountStateMutationEnabled: options.disableAccountStateMutation !== true
        })
        await confirmSameAccountApiKeyFailures()
        return
      } finally {
        releaseConcurrency()
      }
    }
  } catch (error) {
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
      return
    }
    const lastAttempt = error instanceof UpstreamAttemptError ? error.lastAttempt : undefined
    const message = error instanceof Error ? error.message : '没有可用的上游账户'
    getRequestLogger().error(errorLogFields(error, {
      event: 'gateway_request_unexpected_error',
      endpoint: gatewayUsageContext.endpoint,
      apiKeyId: gatewayUsageContext.apiKeyId,
      groupId: gatewayUsageContext.groupId,
      trafficSource: gatewayUsageContext.trafficSource
    }), '网关请求处理出现未预期异常')
    notifyUpstreamAttemptDiagnostic(options, lastAttempt)
    if (shouldSendCodexDispatchExhaustedStreamRetry(currentPreflight, error, res)) {
      await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'codex_dispatch_exhausted_retryable_sse')
      auditCapture.addGatewayMetadata({
        label: 'codex_dispatch_exhausted_retryable_sse',
        metadata: {
          lastAttemptAccountId: lastAttempt?.accountId,
          lastAttemptStatus: lastAttempt?.status,
          failedAccountIds: error.failedAccountIds
        }
      })
      await sendPreCommitStreamRetryExhaustedResponse({
        req,
        res,
        auditCapture,
        usageContext: gatewayUsageContext,
        startedAt,
        retryReason: 'codex_pre_commit_stream_failure',
        message: gatewayStreamClientRetryMessage,
        errorCode: gatewayStreamClientRetryErrorCode,
        accountId: lastAttempt?.accountId,
        clientStrategy: currentPreflight.clientStrategy
      })
      return
    }
    const diagnosticError = options.exposeUpstreamDiagnostics
      ? buildDiagnosticUpstreamError(lastAttempt, message)
      : undefined
    const statusCode = diagnosticError?.statusCode ?? 503
    const responsePayload = diagnosticError?.payload ?? gatewayErrorPayload('没有可用的上游账户', 'service_unavailable')
    await confirmCurrentClientIpAccountAvoidanceAfterFinalFailure(currentPreflight, auditCapture, 'gateway_failure_response')
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
        errorCode: 'service_unavailable',
        errorMessage: diagnosticError?.errorMessage ?? message
      },
      recordUsage: !lastAttempt,
      usageErrorMessage: message
    })
  } finally {
    releaseClientIpSlot()
  }
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

function attachClientIpSlotRelease(res: Response, preflight: OpenAIGatewayDispatchContext): () => void {
  const releaseClientIpSlot = once(preflight.releaseClientIpConcurrency)
  res.once('finish', releaseClientIpSlot)
  res.once('close', releaseClientIpSlot)
  return releaseClientIpSlot
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

function normalRouteSpeedFirstByteDeadlineMs(config?: RouteStrategySpeedFirstConfig): number | undefined {
  return config?.firstByteThresholdMs
}

async function speedFirstRouteEligibleDispatchAccounts(
  accounts: UpstreamAccount[],
  excludedAccountIds: Set<string>,
  latencyScope: ReturnType<typeof normalRouteLatencyDegradationScope>
): Promise<UpstreamAccount[]> {
  const remainingAccounts = streamRetryDispatchAccounts(accounts, excludedAccountIds)
  if (!latencyScope || remainingAccounts.length === 0) {
    return remainingAccounts
  }
  const states = await Promise.all(remainingAccounts.map(async (account) => ({
    account,
    degraded: await isNormalRouteAccountLatencyDegradedAsync(account, latencyScope)
  })))
  return states
    .filter((item) => !item.degraded)
    .map((item) => item.account)
}

function streamClientTotalWaitTimedOut(settings: GatewaySettings, startedAt: number): boolean {
  return Date.now() - startedAt >= streamClientTotalWaitTimeoutMs(settings)
}

function streamClientTotalWaitTimeoutMs(settings: GatewaySettings): number {
  return Math.max(10, settings.streamClientTotalWaitTimeoutSeconds) * 1000
}

function streamClientTotalWaitTimeoutMessage(settings: GatewaySettings): string {
  return `客户端总等待时长 ${Math.max(10, settings.streamClientTotalWaitTimeoutSeconds)} 秒已到达，停止服务端隐藏重试`
}

function shouldSendCodexDispatchExhaustedStreamRetry(
  preflight: OpenAIGatewayDispatchContext,
  error: unknown,
  res: Response
): error is UpstreamAttemptError {
  return error instanceof UpstreamAttemptError
    && !error.agentGuidanceResponse
    && preflight.clientStrategy.allowCodexStreamClientRetry
    && preflight.clientStrategy.downstreamProtocol === 'responses_sse'
    && !res.headersSent
    && !res.writableEnded
    && !res.destroyed
}

async function selectCodexProbeVerifiedDispatchAccount(input: {
  accounts: UpstreamAccount[]
  avoidedAccountIds: Set<string>
  req: Request
  systemAccountId: string
  groupId: string
  auditCapture: ReturnType<typeof createAuditCapture>
  signal?: AbortSignal
}): Promise<{ account?: UpstreamAccount; probes: CodexSwitchProbeResult[] }> {
  const probes: CodexSwitchProbeResult[] = []
  const candidates = input.accounts.filter((account) => !input.avoidedAccountIds.has(account.id))
  for (const account of candidates) {
    const probe = await probeCodexSwitchCandidateAccount(account, {
      req: input.req,
      systemAccountId: input.systemAccountId,
      groupId: input.groupId,
      signal: input.signal
    })
    probes.push(probe)
    if (probe.success) {
      return { account, probes }
    }
  }
  return { probes }
}

function codexSwitchProbeAuditMetadata(probe: CodexSwitchProbeResult): Record<string, unknown> {
  return {
    accountId: probe.accountId,
    accountName: probe.accountName,
    success: probe.success,
    statusCode: probe.statusCode,
    durationMs: probe.durationMs,
    errorCode: probe.errorCode,
    traceId: probe.traceId,
    model: probe.model,
    message: truncateProbeMessage(probe.message)
  }
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
}): Promise<void> {
  const message = input.message || '服务端流式重试未找到可用账号'
  if (input.retryReason === 'codex_pre_commit_stream_failure') {
    await sendPreCommitStreamRetryExhaustedResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      retryReason: input.retryReason,
      message,
      errorCode: input.errorCode,
      uncommittedResponseBody: input.uncommittedResponseBody,
      accountId: input.accountId,
      clientStrategy: input.clientStrategy
    })
    return
  }
  if (input.retryReason === 'pre_commit_stream_failure') {
    if (gatewayProtocolClientErrorProtocolForRequest(input.req) === 'anthropic' || !isOpenAIChatCompletionsRequest(input.req)) {
      await sendPreCommitStreamRetryExhaustedResponse({
        req: input.req,
        res: input.res,
        auditCapture: input.auditCapture,
        usageContext: input.usageContext,
        startedAt: input.startedAt,
        retryReason: input.retryReason,
        message,
        errorCode: input.errorCode,
        uncommittedResponseBody: input.uncommittedResponseBody,
        accountId: input.accountId,
        clientStrategy: input.clientStrategy
      })
      return
    }
    const responsePayload = gatewayErrorPayload(message, 'service_unavailable', input.errorCode ?? 'stream_server_retry_exhausted')
    input.auditCapture.addGatewayMetadata({
      label: 'stream_server_retry_exhausted',
      metadata: {
        retryReason: input.retryReason,
        errorCode: input.errorCode,
        responseMode: 'pre_commit_http_error'
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
        errorCode: input.errorCode ?? 'stream_server_retry_exhausted',
        errorMessage: message
      },
      recordUsage: false,
      usageErrorMessage: message
    })
    return
  }
  if (input.retryReason === 'upstream_protocol_failure') {
    const responsePayload = gatewayErrorPayload(message, 'upstream_response_error', input.errorCode ?? 'upstream_protocol_error')
    input.auditCapture.addGatewayMetadata({
      label: 'upstream_protocol_server_retry_exhausted',
      metadata: {
        retryReason: input.retryReason,
        errorCode: input.errorCode,
        responseMode: 'protocol_failure_http_error'
      }
    })
    await sendGatewayFailureResponse({
      req: input.req,
      res: input.res,
      auditCapture: input.auditCapture,
      usageContext: input.usageContext,
      startedAt: input.startedAt,
      statusCode: 502,
      responsePayload,
      audit: {
        outcome: 'upstream_failed',
        errorPhase: 'dispatch',
        errorCode: input.errorCode ?? 'upstream_protocol_error',
        errorMessage: message
      },
      recordUsage: false,
      usageErrorMessage: message
    })
    return
  }
  const responsePayload = gatewayErrorPayload(message, 'service_unavailable', 'stream_server_retry_exhausted')
  input.auditCapture.addGatewayMetadata({
    label: 'stream_server_retry_exhausted',
    metadata: {
      retryReason: input.retryReason,
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
      errorCode: 'stream_server_retry_exhausted',
      errorMessage: message
    },
    recordUsage: false,
    usageErrorMessage: message
  })
}

function isOpenAIChatCompletionsRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') return false
  const path = (req.originalUrl || req.path || '').split('?', 1)[0] ?? ''
  const requestPath = path.startsWith('/') ? path : `/${path}`
  const normalizedPath = requestPath.replace(/^\/v1(?=\/|$)/, '') || '/'
  return normalizedPath === '/chat/completions'
}

async function sendPreCommitStreamRetryExhaustedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  retryReason: 'pre_commit_stream_failure' | 'codex_pre_commit_stream_failure'
  message: string
  errorCode?: string
  uncommittedResponseBody?: Buffer
  accountId?: string
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
}): Promise<void> {
  const protocol = gatewayProtocolClientErrorProtocolForRequest(input.req)
  const clientVisibleMessage = clientVisiblePreCommitStreamRetryMessage(input)
  const failureEvent = writeGatewayStreamFailureEvent(input.res, clientVisibleMessage, input.errorCode, protocol)
  const responseBody = input.uncommittedResponseBody
    ? Buffer.concat([input.uncommittedResponseBody, failureEvent ?? Buffer.alloc(0)])
    : failureEvent
  input.auditCapture.addGatewayMetadata({
    label: 'stream_server_retry_exhausted',
    metadata: {
      retryReason: input.retryReason,
      errorCode: input.errorCode,
      responseMode: protocol === 'anthropic'
        ? 'anthropic_stream_failure_sse'
        : input.errorCode === gatewayStreamClientRetryErrorCode ? 'codex_retryable_sse' : 'openai_stream_failure_sse'
    }
  })
  await rememberCodexTurnFailureWhenClientRetryIsVisible(input)
  if (!input.res.headersSent) {
    input.res.status(200)
    input.res.setHeader('content-type', 'text/event-stream; charset=utf-8')
    input.res.setHeader('cache-control', 'no-cache, no-transform')
    input.res.setHeader('x-accel-buffering', 'no')
  }
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
    statusCode: 200,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody,
    responsePartType: 'gateway_response',
    errorPhase: 'stream',
    errorCode: input.errorCode,
    errorMessage: clientVisibleMessage,
    accountId: input.accountId
  })
}

function clientVisiblePreCommitStreamRetryMessage(input: {
  retryReason: 'pre_commit_stream_failure' | 'codex_pre_commit_stream_failure'
  errorCode?: string
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
  message: string
}): string {
  if (
    input.retryReason === 'codex_pre_commit_stream_failure'
    || (
      input.errorCode === gatewayStreamClientRetryErrorCode
      && input.clientStrategy?.allowCodexStreamClientRetry === true
    )
  ) {
    return gatewayStreamClientRetryMessage
  }
  return input.message
}

async function rememberCodexTurnFailureWhenClientRetryIsVisible(input: {
  auditCapture: ReturnType<typeof createAuditCapture>
  clientStrategy?: OpenAIGatewayDispatchContext['clientStrategy']
  accountId?: string
  errorCode?: string
  message: string
}): Promise<void> {
  if (
    input.errorCode !== gatewayStreamClientRetryErrorCode
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

async function sendCodexSwitchProbeFailedResponse(input: {
  req: Request
  res: Response
  auditCapture: ReturnType<typeof createAuditCapture>
  usageContext: GatewayFailureUsageContext
  startedAt: number
  probes: CodexSwitchProbeResult[]
}): Promise<void> {
  const diagnosticMessage = codexSwitchProbeFailedMessage(input.probes)
  const message = gatewayStreamClientRetryMessage
  const failureEvent = writeGatewayStreamFailureEvent(input.res, message, gatewayStreamClientRetryErrorCode)
  await recordGatewayFailure(input.req, input.usageContext, {
    statusCode: 200,
    startedAt: input.startedAt,
    responsePayload: gatewayErrorPayload(message, 'server_error', gatewayStreamClientRetryErrorCode),
    errorMessage: message,
    errorCode: gatewayStreamClientRetryErrorCode,
    responseSnapshot: buildUsageResponseSnapshot({
      statusCode: 200,
      headers: {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache, no-transform',
        'x-accel-buffering': 'no'
      },
      bodyText: failureEvent?.toString('utf8'),
      errorMessage: message,
      generatedBy: 'gateway'
    })
  })
  input.auditCapture.addGatewayMetadata({
    label: 'codex_switch_probe_failed',
    metadata: {
      message: diagnosticMessage,
      probeCount: input.probes.length,
      probes: input.probes.map(codexSwitchProbeAuditMetadata)
    }
  })
  if (!input.res.headersSent) {
    input.res.status(200)
    input.res.setHeader('content-type', 'text/event-stream; charset=utf-8')
    input.res.setHeader('cache-control', 'no-cache, no-transform')
    input.res.setHeader('x-accel-buffering', 'no')
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
    statusCode: 200,
    responseHeaders: responseHeadersToObject(input.res),
    responseBody: failureEvent,
    responsePartType: 'gateway_response',
    errorPhase: 'stream',
    errorCode: gatewayStreamClientRetryErrorCode,
    errorMessage: message
  })
}

function codexSwitchProbeFailedMessage(probes: CodexSwitchProbeResult[]): string {
  if (probes.length === 0) {
    return 'Codex 切号失败：没有可探测的备用账号'
  }
  const summaries = probes
    .slice(0, 5)
    .map((probe) => `${probe.accountName || probe.accountId}: ${probe.errorCode ?? probe.statusCode ?? 'probe_failed'} ${probe.message}`)
  const suffix = probes.length > 5 ? `；另有 ${probes.length - 5} 个账号探针失败` : ''
  return `Codex 切号失败：所有备用账号探针均未通过。${summaries.join('；')}${suffix}`
}

function truncateProbeMessage(message: string): string {
  return message.length > 300 ? `${message.slice(0, 300)}...` : message
}

async function recordKnownClientIpRequestError(
  error: unknown,
  usageContext: GatewayFailureUsageContext,
  auditCapture: ReturnType<typeof createAuditCapture>
): Promise<void> {
  const sample = clientIpRequestErrorSample(error)
  if (!sample) {
    return
  }
  const result = await recordClientIpErrorCircuitSampleAsync({
    systemAccountId: usageContext.systemAccountId,
    apiKeyId: usageContext.apiKeyId,
    groupId: usageContext.groupId,
    clientIp: usageContext.clientIp,
    endpoint: usageContext.endpoint,
    reason: sample.reason,
    signature: sample.signature
  })
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
