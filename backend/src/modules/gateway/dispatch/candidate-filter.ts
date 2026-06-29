import type { Request, Response } from 'express'

import type { AuditCaptureContext } from '../audit/capture.service.js'
import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import {
  filterGatewayAccountsByRequestCapability
} from './account-capability-filter.js'
import {
  filterGatewayAccountsByRequestedModel,
  type GatewayAccountModelPriority,
  gatewayModelFilterFailureMessage
} from './model-filter.js'
import { sendGatewayFailureResponse } from '../response/failure-response.js'
import { gatewayErrorPayload } from '../response/responses.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { requestModel } from '../request/metadata.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'
import type { GatewayFailureUsageContext } from '../usage/records.js'
import type { OpenAIGatewayDispatchContext } from '../request/preflight.js'

export interface RequestCandidateFallbackResult {
  attempted: boolean
  context?: OpenAIGatewayDispatchContext
}

export type RequestCandidateFilterResult =
  | { outcome: 'accounts'; accounts: UpstreamAccount[]; modelPriority: GatewayAccountModelPriority }
  | { outcome: 'fallback'; context?: OpenAIGatewayDispatchContext }
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
  attemptFallback: (reason: string) => Promise<RequestCandidateFallbackResult>
  recoverUnavailableCandidateAccounts?: () => Promise<UpstreamAccount[] | undefined>
  loadModelAwareCandidateAccounts?: (requestedModel: string, sourceEndpointFamily?: ReturnType<typeof gatewayRequestEndpointFamily>) => Promise<UpstreamAccount[] | undefined>
}): Promise<RequestCandidateFilterResult> {
  const requestedModel = requestModel(input.req)
  const sourceEndpointFamily = gatewayRequestEndpointFamily(input.req)
  let rawCandidateAccounts = input.rawCandidateAccounts
  if (rawCandidateAccounts.length === 0 && requestedModel && input.loadModelAwareCandidateAccounts) {
    rawCandidateAccounts = await input.loadModelAwareCandidateAccounts(requestedModel, sourceEndpointFamily) ?? rawCandidateAccounts
  }
  if (rawCandidateAccounts.length === 0) {
    const fallback = await input.attemptFallback('no_candidate_accounts')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
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
    const fallback = await input.attemptFallback(reason)
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
    }
    const statusCode = 503
    const message = requestCapabilityMismatchMessage(reason)
    const responsePayload = gatewayErrorPayload(message, 'service_unavailable', reason)
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
        errorCode: reason,
        errorMessage: message
      }
    })
    return { outcome: 'completed' }
  }

  let modelFilter = filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, requestedModel, sourceEndpointFamily)
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
            unrestrictedAccountCount: modelAwareModelFilter.unrestrictedAccountCount,
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
        unrestrictedAccountCount: modelFilter.unrestrictedAccountCount,
        directMatchedCount: modelFilter.directMatchedCount,
        mappingMatchedCount: modelFilter.mappingMatchedCount,
        remainingCount: modelFilter.accounts.length,
        reason: modelFilter.reason
      }
    })
  }
  if (capabilityFilter.accounts.length > 0 && modelFilter.accounts.length === 0) {
    const fallback = await input.attemptFallback(modelFilter.reason ?? 'unsupported_model')
    if (fallback.attempted) {
      return { outcome: 'fallback', context: fallback.context }
    }
    const statusCode = 503
    const message = gatewayModelFilterFailureMessage(modelFilter)
    const responsePayload = gatewayErrorPayload(message, 'service_unavailable', modelFilter.reason ?? 'unsupported_model')
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
        errorCode: modelFilter.reason ?? 'unsupported_model',
        errorMessage: message
      }
    })
    return { outcome: 'completed' }
  }

  return {
    outcome: 'accounts',
    accounts: modelFilter.accounts,
    modelPriority: modelFilter.modelPriority
  }
}

function shouldReloadModelAwareCandidates(
  requestedModel: string | undefined,
  modelFilter: {
    limitedAccountCount: number
    directMatchedCount: number
    mappingMatchedCount: number
  },
  loader: ((requestedModel: string, sourceEndpointFamily?: ReturnType<typeof gatewayRequestEndpointFamily>) => Promise<UpstreamAccount[] | undefined>) | undefined
): boolean {
  return Boolean(
    requestedModel
    && loader
    && modelFilter.limitedAccountCount > 0
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
