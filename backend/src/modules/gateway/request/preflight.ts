import type { Request, Response } from 'express'

import { logger } from '../../../shared/logger.js'
import { bindRequestContextFields } from '../../../shared/request-context.js'
import { type GatewayApiKeyRow, type GroupUsageAccessMetadata, type OpenAIAccountsForGroupDiagnostics } from '../../../storage/repositories.js'
import {
  listCachedOpenAIAccountsForGroupAsync,
  readCachedGatewaySettingsAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from '../runtime/runtime-cache.service.js'
import { type GatewaySettings } from '../policy/account-error-policy.service.js'
import { type AuditCaptureContext } from '../audit/capture.service.js'
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
  openAIGatewayClientStrategyAuditMetadata,
  resolveOpenAIGatewayClientStrategy,
  type OpenAIGatewayClientStrategyContext
} from '../client-profiles/strategy.js'
import {
  inspectClientIpErrorCircuit,
  recordClientIpErrorCircuitSuccess
} from '../runtime/client-ip-error-circuit.service.js'
import { finalizeGatewayAuthFailureAudit, sendAnthropicModelsGatewayResponse, sendOpenAIModelsGatewayResponse } from '../response/fixed-responses.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import { gatewayErrorPayload } from '../response/responses.js'
import { resolveGatewayRuntimeAsync } from './pre-auth.js'
import { type UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import {
  gatewayProtocolResponseProtocolForProfile,
  isGatewayProtocolModelsRequest
} from '../protocols/registry.js'
import {
  resolveOpenAIGatewaySessionAffinityKey
} from '../runtime/session-affinity.service.js'
import { type UsageRequestSnapshot } from '../usage/snapshots.js'
import {
  groupUsageMetadata,
  type GatewayFailureUsageContext
} from '../usage/records.js'
import type { OpenAIGatewayTrafficSource } from '../usage/traffic-source.js'
import type { ClientCompatibilityCapability, GroupSchedulingPolicy } from '../../../domain/types.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import { OPENAI_PROTOCOL_CODE } from '../../../domain/provider-protocol.js'
import {
  canAttemptApiKeyGroupFallback,
  resolveNextApiKeyGroupFallbackCandidate
} from '../dispatch/api-key-group-fallback-candidate.js'
import {
  sendInvalidJsonGatewayResponse
} from './local-request-errors.js'
import { filterOpenAIGatewayRequestCandidateAccounts } from '../dispatch/candidate-filter.js'
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
import { applyCodexResponsesChatBridgeStatePreflight } from '../codex-responses/chat-bridge-state.js'
import { applyCodexResponsesChatBridgeCompactPreflight } from '../codex-responses/compact-preflight.js'

export interface OpenAIGatewayRequestIdentity {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
}

interface OpenAIGatewayRequestPreflightOptions {
  identity?: OpenAIGatewayRequestIdentity
  apiKeyRecord?: GatewayApiKeyRow
  candidateAccounts?: UpstreamAccount[]
  responseInspectionPolicies?: ResponseInspectionPolicySummary[]
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
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
  apiKeyRecord?: GatewayApiKeyRow
  hybridRoute?: HybridGatewayRuntimeRoute
  codexTurnAccountAvoidanceApplied?: boolean
  codexTurnAvoidedAccountIds?: string[]
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
  let runtimeAccountDispatchDiagnostics: OpenAIAccountsForGroupDiagnostics | undefined
  let runtimeResponseInspectionPolicies: ResponseInspectionPolicySummary[] | undefined = options.responseInspectionPolicies
  let selectedHybridRoute: HybridGatewayRuntimeRoute | undefined

  let identity = options.identity ?? await (async () => {
    const runtime = await resolveGatewayRuntimeAsync(req, res)
    if (!runtime?.apiKey) {
      finalizeGatewayAuthFailureAudit(req, res, auditCapture)
      return undefined
    }
    gatewaySettings = runtime.settings
    apiKeyRecord = runtime.apiKey
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

  const activeGatewaySettings = mergeGatewaySettings(
    gatewaySettings ?? await readCachedGatewaySettingsAsync(),
    options.settingsOverride
  )
  let requestLane = options.requestLane ?? 'text'
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
  const clientIpErrorCircuit = inspectClientIpErrorCircuit({
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp,
    endpoint
  })
  if (sendClientIpErrorCircuitGatewayResponse({
    req,
    res,
    auditCapture,
    usageContext: baseUsageContext,
    startedAt,
    circuit: clientIpErrorCircuit,
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp
  })) {
    return undefined
  }
  if (rejectUnavailableGatewayApiKey({
    req,
    res,
    auditCapture,
    usageContext: baseUsageContext,
    startedAt,
    apiKeyUnavailable
  })) {
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
  const imagePermissionPreflight = await applyOpenAIGatewayImagePermissionPreflight({
    req,
    res,
    auditCapture,
    usageContext: baseUsageContext,
    startedAt,
    apiKeyRecord,
    requestLane,
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp,
    endpoint,
    gatewayTextRawBodyLimitMegabytes: activeGatewaySettings.gatewayTextRawBodyLimitMegabytes,
    signal
  })
  if (imagePermissionPreflight.outcome === 'completed') {
    return undefined
  }
  requestLane = imagePermissionPreflight.requestLane

  const initialClientStrategy = resolveOpenAIGatewayClientStrategy(req, {
    systemAccountId,
    apiKeyId,
    groupId,
    endpoint
  })
  if (!options.identity && trafficSource === 'gateway' && apiKeyRecord?.route_mode === 'normal') {
    const previousGroupId = groupId
    const previousBindingCount = apiKeyRecord.group_bindings?.length ?? 0
    const normalRoute = await resolveNormalGatewayModelRoute({
      req,
      apiKeyRecord,
      requestClientCompatibility: initialClientStrategy.requestClientCompatibility
    })
    if (normalRoute.outcome === 'selected') {
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
      const targetClientIpErrorCircuit = inspectClientIpErrorCircuit({
        systemAccountId,
        apiKeyId,
        groupId,
        clientIp: gatewayClientIp,
        endpoint
      })
      if (sendClientIpErrorCircuitGatewayResponse({
        req,
        res,
        auditCapture,
        usageContext: { ...baseUsageContext, groupId },
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

  if (!options.identity && trafficSource === 'gateway' && apiKeyRecord?.route_mode === 'hybrid') {
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
          level: hybridRoute.scoring?.level,
          scoringDefaulted: hybridRoute.scoring?.defaulted,
          scoringErrorCode: hybridRoute.scoring?.errorCode,
          scoringErrorMessage: hybridRoute.scoring?.errorMessage
        }
      })
      const statusCode = 503
      const responsePayload = gatewayErrorPayload('混合路由目标分组暂不可用', 'service_unavailable', hybridRoute.reason)
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
        qualityRetryCount: 0
      }
      auditCapture.bindContext({ groupId })
      bindRequestContextFields({
        systemAccountId,
        apiKeyId,
        groupId,
        trafficSource
      })
      const targetClientIpErrorCircuit = inspectClientIpErrorCircuit({
        systemAccountId,
        apiKeyId,
        groupId,
        clientIp: gatewayClientIp,
        endpoint
      })
      if (sendClientIpErrorCircuitGatewayResponse({
        req,
        res,
        auditCapture,
        usageContext: { ...baseUsageContext, groupId },
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
    providerCode: groupAccess?.providerCode,
    providerProtocolProfileId: groupAccess?.providerProtocolProfileId,
    protocolCode: groupAccess?.protocolCode,
    protocolVersion: groupAccess?.protocolVersion
  })
  const clientIpAccountAvoidanceTracker = createClientIpAccountAvoidanceTracker({
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp
  })
  if (clientStrategy.clientProfile === 'codex' || clientStrategy.clientProfile === 'claude_code') {
    auditCapture.addGatewayMetadata({
      label: 'client_strategy',
      metadata: openAIGatewayClientStrategyAuditMetadata(clientStrategy)
    })
  }
  if (!groupAccess) {
    rejectMissingGatewayGroupAccess({
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

  if (isGatewayModelsRequest(req, groupAccess)) {
    recordClientIpErrorCircuitSuccess({
      systemAccountId,
      apiKeyId,
      groupId,
      clientIp: gatewayClientIp,
      endpoint
    })
    const sender = gatewayProtocolResponseProtocolForProfile(groupAccess) === 'anthropic_v1'
      ? sendAnthropicModelsGatewayResponse
      : sendOpenAIModelsGatewayResponse
    await sender({
      req,
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
  if (!options.candidateAccounts && runtimeAccountDispatchDiagnostics) {
    auditCapture.addGatewayMetadata({
      label: 'account_dispatch_candidate_window',
      metadata: {
        ...runtimeAccountDispatchDiagnostics,
        returnedCandidateCount: rawCandidateAccounts.length
      }
    })
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
    rawCandidateAccounts,
    activeGatewaySettings,
    clientIpAccountAvoidanceTracker,
    requestLane,
    groupSchedulingPolicy: groupAccess.schedulingPolicy,
    signal
  })
  if (codexBridgeCompactPreflight === 'completed') {
    return undefined
  }
  const codexBridgeStatePreflight = await applyCodexResponsesChatBridgeStatePreflight({
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
    rawCandidateAccounts,
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

  const dispatchPreparation = await prepareOpenAIGatewayDispatchAccounts({
    req,
    res,
    auditCapture,
    usageContext,
    startedAt,
    candidateAccounts: candidateFilter.accounts,
    sessionAffinityKey,
    groupAccess,
    systemAccountId,
    apiKeyId,
    groupId,
    clientIp: gatewayClientIp,
    clientStrategy,
    requestLane,
    signal,
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

  return {
    activeGatewaySettings,
    usageContext,
    accounts: dispatchPreparation.accounts,
    sessionAffinityKey,
    clientStrategy,
    clientIpAccountAvoidanceTracker,
    requestLane,
    groupSchedulingPolicy: groupAccess.schedulingPolicy,
    responseInspectionPolicies: runtimeResponseInspectionPolicies ?? [],
    apiKeyRecord,
    hybridRoute: selectedHybridRoute,
    codexTurnAccountAvoidanceApplied: dispatchPreparation.codexTurnAccountAvoidanceApplied,
    codexTurnAvoidedAccountIds: dispatchPreparation.codexTurnAvoidedAccountIds,
    releaseClientIpConcurrency: dispatchPreparation.releaseClientIpConcurrency
  }
}

function isGatewayModelsRequest(req: Request, groupAccess: GroupUsageAccessMetadata): boolean {
  return isGatewayProtocolModelsRequest(req, groupAccess)
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
    streamCircuitBreakerEnabled: override.streamCircuitBreakerEnabled ?? base.streamCircuitBreakerEnabled,
    streamRequestTimeoutSeconds: override.streamRequestTimeoutSeconds ?? base.streamRequestTimeoutSeconds,
    streamIdleTimeoutSeconds: override.streamIdleTimeoutSeconds ?? base.streamIdleTimeoutSeconds,
    streamFailureThresholdCount: override.streamFailureThresholdCount ?? base.streamFailureThresholdCount,
    streamFailureThresholdWindowMinutes: override.streamFailureThresholdWindowMinutes ?? base.streamFailureThresholdWindowMinutes
  }
}

function sendClientIpErrorCircuitGatewayResponse(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  circuit: ReturnType<typeof inspectClientIpErrorCircuit>
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
}): boolean {
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
      errorPhase: 'security',
      errorCode: 'client_ip_error_circuit_open',
      errorMessage: responsePayload.error.message
    }
  })
  return true
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
    requestSnapshot
  }
}
