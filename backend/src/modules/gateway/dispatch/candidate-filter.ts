import type { Request, Response } from 'express'

import type { AuditCaptureContext } from '../audit/capture.service.js'
import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import {
  filterGatewayAccountsByRequestCapability
} from './account-capability-filter.js'
import {
  filterGatewayAccountsByRequestedModel,
  gatewayAccountModelPriorityRank,
  type GatewayAccountModelPriority,
  type GatewayModelAccountFilterResult,
  gatewayModelFilterFailureMessage
} from './model-filter.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { requestModel } from '../request/metadata.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'
import type { OpenAIGatewayDispatchContext } from '../request/preflight.js'
import type { GatewayRouteCoordinatorOwner } from '../routing/route-coordination.js'

export interface RequestCandidateFallbackResult {
  attempted: boolean
  context?: OpenAIGatewayDispatchContext
}

export type RequestCandidateFilterResult =
  | { outcome: 'accounts'; accounts: UpstreamAccount[]; modelPriority: GatewayAccountModelPriority }
  | { outcome: 'fallback'; reason: string; context?: OpenAIGatewayDispatchContext }
  | { outcome: 'completed' }

export async function filterOpenAIGatewayRequestCandidateAccounts(input: {
  req: Request
  res: Response
  auditCapture: AuditCaptureContext
  usageContext: GatewayFailureUsageContext
  startedAt: number
  rawCandidateAccounts: UpstreamAccount[]
  clientStrategy: OpenAIGatewayClientStrategyContext
  systemAccountId: string
  apiKeyId?: string
  groupId: string
  clientIp?: string
  endpoint: string
  bypassModelFilter?: boolean
  requestModelOverride?: string
  routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext>
  recoverUnavailableCandidateAccounts?: () => Promise<UpstreamAccount[] | undefined>
  loadModelAwareCandidateAccounts?: (requestedModel: string, sourceEndpointFamily?: ReturnType<typeof gatewayRequestEndpointFamily>) => Promise<UpstreamAccount[] | undefined>
}): Promise<RequestCandidateFilterResult> {
  const requestedModel = input.requestModelOverride?.trim() || requestModel(input.req)
  const sourceEndpointFamily = gatewayRequestEndpointFamily(input.req)
  let rawCandidateAccounts = input.rawCandidateAccounts
  if (rawCandidateAccounts.length === 0 && requestedModel && input.loadModelAwareCandidateAccounts) {
    rawCandidateAccounts = await input.loadModelAwareCandidateAccounts(requestedModel, sourceEndpointFamily) ?? rawCandidateAccounts
  }
  if (rawCandidateAccounts.length === 0) {
    const fallback = await requestRouteFallback(input, 'no_candidate_accounts')
    if (fallback.attempted) {
      return { outcome: 'fallback', reason: 'no_candidate_accounts', context: fallback.context }
    }
    rawCandidateAccounts = await input.recoverUnavailableCandidateAccounts?.() ?? rawCandidateAccounts
  }

  let capabilityFilter = filterGatewayAccountsByRequestCapability(input.req, rawCandidateAccounts, {
    requestClientCompatibility: input.clientStrategy.requestClientCompatibility
  })
  if (capabilityFilter.skippedCount > 0) {
    input.auditCapture.addGatewayMetadata({
      label: 'account_request_capability_filter',
      metadata: {
        skippedCount: capabilityFilter.skippedCount,
        remainingCount: capabilityFilter.accounts.length,
        reason: capabilityFilter.reason,
        requestClientCompatibility: input.clientStrategy.requestClientCompatibility
      }
    })
  }
  if (rawCandidateAccounts.length > 0 && capabilityFilter.accounts.length === 0) {
    const reason = capabilityFilter.reason ?? 'request_capability_mismatch'
    const fallback = await requestRouteFallback(input, reason)
    if (fallback.attempted) {
      return { outcome: 'fallback', reason, context: fallback.context }
    }
    const message = requestCapabilityMismatchMessage(reason)
    await input.routeCoordinator.completeFailure({
      statusCode: 503,
      message,
      errorType: 'service_unavailable',
      errorCode: reason,
      errorPhase: 'dispatch'
    })
    return { outcome: 'completed' }
  }

  let modelFilter = input.bypassModelFilter
    ? bypassGatewayModelFilter(capabilityFilter.accounts, sourceEndpointFamily)
    : filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, requestedModel, sourceEndpointFamily)
  if (shouldReloadModelAwareCandidates(requestedModel, modelFilter, input.loadModelAwareCandidateAccounts)) {
    const modelAwareRawAccounts = await input.loadModelAwareCandidateAccounts!(requestedModel!, sourceEndpointFamily)
    if (modelAwareRawAccounts?.length) {
      const modelAwareCapabilityFilter = filterGatewayAccountsByRequestCapability(input.req, modelAwareRawAccounts, {
        requestClientCompatibility: input.clientStrategy.requestClientCompatibility
      })
      const modelAwareModelFilter = filterGatewayAccountsByRequestedModel(modelAwareCapabilityFilter.accounts, requestedModel, sourceEndpointFamily)
      if (
        modelAwareModelFilter.directMatchedCount > 0
        || modelAwareModelFilter.mappingMatchedCount > 0
        || (modelFilter.accounts.length === 0 && modelAwareModelFilter.accounts.length > 0)
      ) {
        capabilityFilter = modelAwareCapabilityFilter
        modelFilter = modelAwareModelFilter
        input.auditCapture.addGatewayMetadata({
          label: 'account_model_candidate_window',
          metadata: {
            requestedModel: modelAwareModelFilter.requestedModel,
            sourceEndpointFamily: modelAwareModelFilter.sourceEndpointFamily,
            directMatchedCount: modelAwareModelFilter.directMatchedCount,
            mappingMatchedCount: modelAwareModelFilter.mappingMatchedCount,
            invalidModelConstraintCount: modelAwareModelFilter.invalidModelConstraintCount,
            remainingCount: modelAwareModelFilter.accounts.length
          }
        })
      }
    }
  }
  if (modelFilter.skippedCount > 0 || modelFilter.mappingMatchedCount > 0) {
    input.auditCapture.addGatewayMetadata({
      label: 'account_model_filter',
      metadata: {
        requestedModel: modelFilter.requestedModel,
        sourceEndpointFamily: modelFilter.sourceEndpointFamily,
        skippedCount: modelFilter.skippedCount,
        limitedAccountCount: modelFilter.limitedAccountCount,
        invalidModelConstraintCount: modelFilter.invalidModelConstraintCount,
        directMatchedCount: modelFilter.directMatchedCount,
        mappingMatchedCount: modelFilter.mappingMatchedCount,
        remainingCount: modelFilter.accounts.length,
        reason: modelFilter.reason
      }
    })
  }
  if (capabilityFilter.accounts.length > 0 && modelFilter.accounts.length === 0) {
    const fallback = await requestRouteFallback(input, modelFilter.reason ?? 'unsupported_model')
    if (fallback.attempted) {
      return { outcome: 'fallback', reason: modelFilter.reason ?? 'unsupported_model', context: fallback.context }
    }
    const message = gatewayModelFilterFailureMessage(modelFilter)
    await input.routeCoordinator.completeFailure({
      statusCode: 503,
      message,
      errorType: 'service_unavailable',
      errorCode: modelFilter.reason ?? 'unsupported_model',
      errorPhase: 'dispatch'
    })
    return { outcome: 'completed' }
  }

  return {
    outcome: 'accounts',
    accounts: modelFilter.accounts,
    modelPriority: modelFilter.modelPriority
  }
}

async function requestRouteFallback(
  input: {
    routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext>
  },
  reason: string
): Promise<RequestCandidateFallbackResult> {
  return input.routeCoordinator.requestFallback(reason)
}

function bypassGatewayModelFilter(
  accounts: UpstreamAccount[],
  sourceEndpointFamily: ReturnType<typeof gatewayRequestEndpointFamily>
): GatewayModelAccountFilterResult {
  return {
    accounts,
    skippedCount: 0,
    limitedAccountCount: accounts.filter((account) => (account.supportedModels?.length ?? 0) > 0).length,
    invalidModelConstraintCount: 0,
    directMatchedCount: accounts.length,
    mappingMatchedCount: 0,
    sourceEndpointFamily,
    modelPriority: {
      sourceEndpointFamily,
      rankByAccountId: new Map(accounts.map((account) => [account.id, gatewayAccountModelPriorityRank.direct]))
    }
  }
}

function shouldReloadModelAwareCandidates(
  requestedModel: string | undefined,
  modelFilter: {
    directMatchedCount: number
    mappingMatchedCount: number
  },
  loader: ((requestedModel: string, sourceEndpointFamily?: ReturnType<typeof gatewayRequestEndpointFamily>) => Promise<UpstreamAccount[] | undefined>) | undefined
): boolean {
  return Boolean(
    requestedModel
    && loader
    && modelFilter.directMatchedCount === 0
    && modelFilter.mappingMatchedCount === 0
  )
}

function requestCapabilityMismatchMessage(reason: string): string {
  if (reason === 'anthropic_native_group_openai_compatible_request') {
    return '当前 API Key 绑定的是 Anthropic 原生分组，不兼容 Codex / OpenAI 请求路径；请改用 Anthropic /v1/messages 客户端，或绑定支持 OpenAI Responses / Chat Completions 的分组'
  }
  return '当前分组无账户支持请求路径或客户端协议'
}
