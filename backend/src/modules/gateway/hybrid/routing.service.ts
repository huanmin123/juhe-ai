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
import { filterGatewayAccountsByRequestCapability } from '../dispatch/account-capability-filter.js'
import { filterGatewayAccountsByRequestedModel } from '../dispatch/model-filter.js'
import { orderGatewayApiKeyGroupBindingsForDispatch } from '../routing/api-key-group-route-selector.service.js'
import {
  listCachedActiveResponseInspectionPoliciesAsync,
  listCachedOpenAIAccountsForGroupAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from '../runtime/runtime-cache.service.js'
import { replaceGatewayJsonBodyModel } from '../request/body.js'
import { parseGatewayJsonBodyInWorker } from '../request/json-parser.js'
import { applyHybridRouteAffinity } from './affinity.service.js'
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
    targetModel: string
    affinityApplied: boolean
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
  if (input.apiKeyRecord.route_mode !== 'hybrid' || !config) {
    return { outcome: 'skipped', reason: 'not_hybrid_api_key' }
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
  const initialRoute = targetHybridLevelRouteForLevel(config, scoring.level)
  if (!initialRoute) {
    return { outcome: 'failed', reason: 'hybrid_level_route_missing', scoring }
  }
  const affinity = applyHybridRouteAffinity({
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
      config,
      route: candidateRoute,
      requestClientCompatibility: input.requestClientCompatibility,
      signal: input.signal
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
      targetModel: candidateRoute.targetModel,
      affinityApplied: affinity.applied
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

async function selectHybridTargetGroup(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  config: ApiKeyHybridRoutingConfig
  route: ApiKeyHybridLevelRoute
  requestClientCompatibility?: ClientCompatibilityCapability
  signal?: AbortSignal
}): Promise<{
  groupId: string
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
} | undefined> {
  const bindings = orderGatewayApiKeyGroupBindingsForDispatch(input.apiKeyRecord)
  const seenGroupIds = new Set<string>()
  for (const binding of bindings) {
    if (!binding.group_id || seenGroupIds.has(binding.group_id)) {
      continue
    }
    seenGroupIds.add(binding.group_id)
    const groupAccess = await resolveCachedGroupUsageAccessMetadataAsync(binding.group_id, input.apiKeyRecord.system_account_id)
    if (!groupAccess) {
      continue
    }
    const accounts = await listCachedOpenAIAccountsForGroupAsync(binding.group_id, input.apiKeyRecord.system_account_id)
    if (!accounts.length) {
      continue
    }
    const capabilityFilter = filterGatewayAccountsByRequestCapability(input.req, accounts, {
      requestClientCompatibility: input.requestClientCompatibility
    })
    if (!capabilityFilter.accounts.length) {
      continue
    }
    const modelFilter = filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, input.route.targetModel)
    if (!modelFilter.accounts.length) {
      continue
    }
    const responseInspectionPolicies = await listCachedActiveResponseInspectionPoliciesAsync({
      protocolCode: groupAccess.protocolCode,
      providerCode: groupAccess.providerCode
    })
    return {
      groupId: binding.group_id,
      groupAccess,
      accounts: modelFilter.accounts,
      responseInspectionPolicies
    }
  }
  return undefined
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
