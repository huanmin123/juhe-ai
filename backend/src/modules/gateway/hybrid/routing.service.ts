import { channel } from 'node:diagnostics_channel'

import type { Request } from 'express'

import {
  higherHybridLevelRoutes,
  targetHybridLevelRouteForLevel
} from '../../../domain/api-key-hybrid-routing.js'
import type {
  ApiKeyHybridLevelRoute,
  ApiKeyHybridRoutingConfig,
  ClientCompatibilityCapability
} from '../../../domain/types.js'
import type {
  GatewayApiKeyRow,
  GroupUsageAccessMetadata,
  OpenAIAccountSecret
} from '../../../storage/repositories.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import { orderGatewayApiKeyGroupBindingsForDispatchAsync } from '../routing/api-key-group-route-selector.service.js'
import { selectGatewayModelTargetGroup } from '../routing/model-target-group-selector.js'
import { replaceGatewayJsonBodyModel } from '../request/body.js'
import { parseGatewayJsonBodyInWorker } from '../request/json-parser.js'
import { applyHybridRouteAffinityAsync } from './affinity.service.js'
import { scoreHybridGatewayRequest, type HybridScoringResult } from './scoring.service.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'
import type { GatewayRawBodyRequest } from '../request/body.js'

const hybridRouteDiagnosticsChannel = channel('juhe-ai:hybrid-route-decision')

export type HybridGatewayRouteResult =
  | {
    outcome: 'selected'
    apiKeyRecord: GatewayApiKeyRow
    groupId: string
    groupAccess: GroupUsageAccessMetadata
    accounts: OpenAIAccountSecret[]
    responseInspectionPolicies: ResponseInspectionPolicySummary[]
    scoring: HybridScoringResult
    route: ApiKeyHybridLevelRoute
    config: ApiKeyHybridRoutingConfig
    targetModel: string
    affinityApplied: boolean
    scoringFallbackApplied: boolean
  }
  | {
    outcome: 'skipped'
    reason: string
  }
  | {
    outcome: 'failed'
    reason: string
    scoring?: HybridScoringResult
    targetModel?: string
  }

export interface HybridGatewayRuntimeRoute {
  apiKeyRecord: GatewayApiKeyRow
  config: ApiKeyHybridRoutingConfig
  scoring: HybridScoringResult
  route: ApiKeyHybridLevelRoute
  targetModel: string
  affinityApplied: boolean
  scoringFallbackApplied: boolean
  qualityRetryCount: number
}

export interface HybridGatewayTargetRoute {
  apiKeyRecord: GatewayApiKeyRow
  groupId: string
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
  route: ApiKeyHybridLevelRoute
  targetModel: string
}

export async function resolveHybridGatewayRoute(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  traceId: string
  clientIp?: string
  endpoint: string
  auditCapture: AuditCaptureContext
  requestClientCompatibility?: ClientCompatibilityCapability
  signal?: AbortSignal
}): Promise<HybridGatewayRouteResult> {
  const config = input.apiKeyRecord.hybrid_routing_config
  if (input.apiKeyRecord.route_strategy_mode !== 'hybrid_smart' || !config) {
    return { outcome: 'skipped', reason: 'not_hybrid_route_strategy' }
  }
  if (!isHybridRoutableRequest(input.req)) {
    return { outcome: 'skipped', reason: 'not_json_post_request' }
  }
  const scoring = await scoreHybridGatewayRequest({
    req: input.req,
    apiKeyRecord: input.apiKeyRecord,
    config,
    traceId: input.traceId,
    clientIp: input.clientIp,
    endpoint: input.endpoint,
    signal: input.signal
  })
  if (scoring.failed) {
    const fallbackTarget = await selectHybridScoringFallbackTarget({
      req: input.req,
      apiKeyRecord: input.apiKeyRecord,
      config,
      requestClientCompatibility: input.requestClientCompatibility
    })
    if (fallbackTarget) {
      const routeDiagnostics = {
        traceId: input.traceId,
        apiKeyId: input.apiKeyRecord.id,
        sessionId: input.req.get?.('x-session-id'),
        clientRequestId: input.req.get?.('x-client-request-id'),
        endpoint: input.endpoint,
        outcome: 'selected',
        level: scoring.level,
        confidence: scoring.confidence,
        scoringDefaulted: scoring.defaulted,
        scoringCacheHit: scoring.cacheHit === true,
        scoringAccountId: scoring.scoringAccountId,
        scoringErrorCode: scoring.errorCode,
        scoringErrorMessage: scoring.errorMessage,
        scoringFactors: scoring.factors,
        scoringReason: scoring.reason,
        targetModel: fallbackTarget.route.targetModel,
        targetGroupId: fallbackTarget.groupId,
        levelRange: [fallbackTarget.route.minLevel, fallbackTarget.route.maxLevel],
        scoringFallbackApplied: true,
        scoringFallbackReason: scoring.errorCode ?? 'hybrid_scoring_failed',
        scoringFallbackMaxLevel: config.scoringFallbackMaxLevel,
        affinityApplied: false
      }
      input.auditCapture.addGatewayMetadata({
        label: 'hybrid_route',
        metadata: routeDiagnostics
      })
      hybridRouteDiagnosticsChannel.publish(routeDiagnostics)
      await rewriteHybridRequestModel(input.req, fallbackTarget.route.targetModel, input.signal)
      return {
        outcome: 'selected',
        apiKeyRecord: {
          ...input.apiKeyRecord,
          selected_group_id: fallbackTarget.groupId
        },
        groupId: fallbackTarget.groupId,
        groupAccess: fallbackTarget.groupAccess,
        accounts: fallbackTarget.accounts,
        responseInspectionPolicies: fallbackTarget.responseInspectionPolicies,
        scoring,
        route: fallbackTarget.route,
        config,
        targetModel: fallbackTarget.route.targetModel,
        affinityApplied: false,
        scoringFallbackApplied: true
      }
    }
    const reason = 'hybrid_scoring_fallback_unavailable'
    hybridRouteDiagnosticsChannel.publish({
      traceId: input.traceId,
      apiKeyId: input.apiKeyRecord.id,
      sessionId: input.req.get?.('x-session-id'),
      clientRequestId: input.req.get?.('x-client-request-id'),
      endpoint: input.endpoint,
      outcome: 'failed',
      reason,
      scoringFallbackMaxLevel: config.scoringFallbackMaxLevel,
      scoringDefaulted: scoring.defaulted,
      scoringCacheHit: scoring.cacheHit === true,
      scoringAccountId: scoring.scoringAccountId,
      scoringErrorCode: scoring.errorCode,
      scoringErrorMessage: scoring.errorMessage
    })
    return { outcome: 'failed', reason, scoring }
  }
  const initialRoute = targetHybridLevelRouteForLevel(config, scoring.level)
  if (!initialRoute) {
    return { outcome: 'failed', reason: 'hybrid_level_route_missing', scoring }
  }
  const affinity = await applyHybridRouteAffinityAsync({
    req: input.req,
    systemAccountId: input.apiKeyRecord.system_account_id,
    apiKeyId: input.apiKeyRecord.id,
    config,
    level: scoring.level,
    route: initialRoute
  })
  const route = affinity.route
  const candidates = [route, ...higherHybridLevelRoutes(config, route)]
  for (const candidateRoute of candidates) {
    const target = await selectHybridTargetGroup({
      req: input.req,
      apiKeyRecord: input.apiKeyRecord,
      route: candidateRoute,
      requestClientCompatibility: input.requestClientCompatibility
    })
    if (!target) {
      continue
    }
    const routeDiagnostics = {
      traceId: input.traceId,
      apiKeyId: input.apiKeyRecord.id,
      sessionId: input.req.get?.('x-session-id'),
      clientRequestId: input.req.get?.('x-client-request-id'),
      endpoint: input.endpoint,
      outcome: 'selected',
      level: scoring.level,
      confidence: scoring.confidence,
      scoringDefaulted: scoring.defaulted,
      scoringCacheHit: scoring.cacheHit === true,
      scoringAccountId: scoring.scoringAccountId,
      scoringErrorCode: scoring.errorCode,
      scoringErrorMessage: scoring.errorMessage,
      scoringFactors: scoring.factors,
      scoringReason: scoring.reason,
      targetModel: candidateRoute.targetModel,
      targetGroupId: target.groupId,
      levelRange: [candidateRoute.minLevel, candidateRoute.maxLevel],
      upgradedFromModel: candidateRoute.targetModel !== route.targetModel ? route.targetModel : undefined,
      affinityApplied: affinity.applied,
      affinityReason: affinity.reason,
      previousModel: affinity.previousModel,
      lowCount: affinity.lowCount
    }
    input.auditCapture.addGatewayMetadata({
      label: 'hybrid_route',
      metadata: routeDiagnostics
    })
    hybridRouteDiagnosticsChannel.publish(routeDiagnostics)
    await rewriteHybridRequestModel(input.req, candidateRoute.targetModel, input.signal)
    return {
      outcome: 'selected',
      apiKeyRecord: {
        ...input.apiKeyRecord,
        selected_group_id: target.groupId
      },
      groupId: target.groupId,
      groupAccess: target.groupAccess,
      accounts: target.accounts,
      responseInspectionPolicies: target.responseInspectionPolicies,
      scoring,
      route: candidateRoute,
      config,
      targetModel: candidateRoute.targetModel,
      affinityApplied: affinity.applied,
      scoringFallbackApplied: false
    }
  }
  hybridRouteDiagnosticsChannel.publish({
    traceId: input.traceId,
    apiKeyId: input.apiKeyRecord.id,
    sessionId: input.req.get?.('x-session-id'),
    clientRequestId: input.req.get?.('x-client-request-id'),
    endpoint: input.endpoint,
    outcome: 'failed',
    reason: 'hybrid_target_group_unavailable',
    level: scoring.level,
    confidence: scoring.confidence,
    scoringDefaulted: scoring.defaulted,
    scoringCacheHit: scoring.cacheHit === true,
    scoringErrorCode: scoring.errorCode,
    scoringErrorMessage: scoring.errorMessage,
    scoringFactors: scoring.factors,
    scoringReason: scoring.reason,
    targetModel: route.targetModel
  })
  return {
    outcome: 'failed',
    reason: 'hybrid_target_group_unavailable',
    scoring,
    targetModel: route.targetModel
  }
}

export async function resolveNextHybridGatewayRoute(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  currentRoute: ApiKeyHybridLevelRoute
  requestClientCompatibility?: ClientCompatibilityCapability
  signal?: AbortSignal
}): Promise<HybridGatewayTargetRoute | undefined> {
  const config = input.apiKeyRecord.hybrid_routing_config
  if (input.apiKeyRecord.route_strategy_mode !== 'hybrid_smart' || !config) {
    return undefined
  }
  for (const candidateRoute of higherHybridLevelRoutes(config, input.currentRoute)) {
    const target = await selectHybridTargetGroup({
      req: input.req,
      apiKeyRecord: input.apiKeyRecord,
      route: candidateRoute,
      requestClientCompatibility: input.requestClientCompatibility
    })
    if (!target) continue
    await rewriteHybridRequestModel(input.req, candidateRoute.targetModel, input.signal)
    return {
      apiKeyRecord: {
        ...input.apiKeyRecord,
        selected_group_id: target.groupId
      },
      groupId: target.groupId,
      groupAccess: target.groupAccess,
      accounts: target.accounts,
      responseInspectionPolicies: target.responseInspectionPolicies,
      route: candidateRoute,
      targetModel: candidateRoute.targetModel
    }
  }
  return undefined
}

async function selectHybridTargetGroup(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  route: ApiKeyHybridLevelRoute
  requestClientCompatibility?: ClientCompatibilityCapability
}): Promise<{
  groupId: string
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
} | undefined> {
  return selectGatewayModelTargetGroup({
    req: input.req,
    apiKeyRecord: input.apiKeyRecord,
    bindings: await orderGatewayApiKeyGroupBindingsForDispatchAsync(input.apiKeyRecord),
    targetModel: input.route.targetModel,
    requestClientCompatibility: input.requestClientCompatibility
  })
}

async function selectHybridScoringFallbackTarget(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  config: ApiKeyHybridRoutingConfig
  requestClientCompatibility?: ClientCompatibilityCapability
}): Promise<{
  groupId: string
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
  route: ApiKeyHybridLevelRoute
} | undefined> {
  for (const route of hybridScoringFallbackRoutes(input.config)) {
    const target = await selectHybridTargetGroup({
      req: input.req,
      apiKeyRecord: input.apiKeyRecord,
      route,
      requestClientCompatibility: input.requestClientCompatibility
    })
    if (!target) continue
    return {
      ...target,
      route
    }
  }
  return undefined
}

function hybridScoringFallbackRoutes(config: ApiKeyHybridRoutingConfig): ApiKeyHybridLevelRoute[] {
  return config.levelRoutes
    .filter((route) => route.enabled && route.minLevel <= config.scoringFallbackMaxLevel)
    .sort((left, right) => left.minLevel - right.minLevel || left.maxLevel - right.maxLevel)
}

async function rewriteHybridRequestModel(req: Request, targetModel: string, signal?: AbortSignal): Promise<void> {
  if (replaceGatewayJsonBodyModel(req, targetModel)) {
    return
  }
  const request = req as GatewayRawBodyRequest
  if (!request.rawBody?.length) {
    throw new Error('混合路由无法改写空请求体')
  }
  const parsed = await parseGatewayJsonBodyInWorker(request.rawBody, 30000, signal)
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error('混合路由请求体必须是 JSON 对象')
  }
  if (!replaceGatewayJsonBodyModel(req, targetModel, parsed as Record<string, unknown>)) {
    throw new Error('混合路由模型改写失败')
  }
}

function isHybridRoutableRequest(req: Request): boolean {
  if (req.method.toUpperCase() !== 'POST') {
    return false
  }
  const contentType = String(req.headers['content-type'] ?? '').toLowerCase()
  if (!contentType.includes('json')) {
    return false
  }
  return Boolean((req as GatewayRawBodyRequest).rawBody?.length || req.body)
}
