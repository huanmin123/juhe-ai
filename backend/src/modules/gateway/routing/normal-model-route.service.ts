import type { Request } from 'express'

import { requestModel } from '../request/metadata.js'
import {
  resolveCachedProviderModelRouteAsync
} from '../runtime/runtime-cache.service.js'
import { selectGatewayModelTargetGroup } from './model-target-group-selector.js'
import type {
  ClientCompatibilityCapability
} from '../../../domain/types.js'
import type {
  GatewayApiKeyRow,
  GroupUsageAccessMetadata,
  OpenAIAccountSecret
} from '../../../storage/repositories.js'
import type { GatewayApiKeyGroupBindingRow } from '../../../storage/gateway-api-key.repository.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'

export type NormalGatewayModelRouteSource = 'account_mapping' | 'catalog_provider'

export interface SelectedNormalGatewayModelRouteResult {
  outcome: 'selected'
  apiKeyRecord: GatewayApiKeyRow
  groupId: string
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
  requestedModel: string
  routeSource: NormalGatewayModelRouteSource
  matchedProviderCode?: string
}

export type NormalGatewayModelRouteResult =
  | SelectedNormalGatewayModelRouteResult
  | {
      outcome: 'skipped'
      reason: string
      requestedModel?: string
    }
  | {
      outcome: 'failed'
      statusCode: number
      type: string
      code: string
      message: string
      requestedModel: string
      matchedProviderCodes?: string[]
    }

export interface ResolveNormalGatewayModelRouteInput {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  requestClientCompatibility?: ClientCompatibilityCapability
}

interface CatalogProviderRoute {
  providerCode: string
  providerProtocolProfileIds: Set<string>
  matchedProviderCodes: string[]
}

export async function resolveNormalGatewayModelRoute(
  input: ResolveNormalGatewayModelRouteInput
): Promise<NormalGatewayModelRouteResult> {
  const { apiKeyRecord, req, requestClientCompatibility } = input
  if (apiKeyRecord.route_mode !== 'normal') {
    return { outcome: 'skipped', reason: 'route_mode_not_normal' }
  }

  const requestedModel = requestModel(req)?.trim()
  if (!requestedModel) {
    return { outcome: 'skipped', reason: 'missing_requested_model' }
  }

  const bindings = activeGatewayApiKeyGroupBindings(apiKeyRecord)
  if (!bindings.length) {
    return { outcome: 'skipped', reason: 'empty_binding', requestedModel }
  }

  const mappingTarget = await selectGatewayModelTargetGroup({
    req,
    apiKeyRecord,
    bindings,
    targetModel: requestedModel,
    requestClientCompatibility,
    acceptCandidate: (candidate) => candidate.modelFilter.mappingMatchedCount > 0
  })
  if (mappingTarget) {
    const selectedProfileBindings = bindings
      .filter(candidate => candidate.provider_protocol_profile_id === mappingTarget.binding.provider_protocol_profile_id)
      .map(copyGroupBinding)
    return {
      outcome: 'selected',
      apiKeyRecord: {
        ...apiKeyRecord,
        selected_group_id: mappingTarget.groupId,
        group_bindings: selectedProfileBindings
      },
      groupId: mappingTarget.groupId,
      groupAccess: mappingTarget.groupAccess,
      accounts: mappingTarget.accounts,
      responseInspectionPolicies: mappingTarget.responseInspectionPolicies,
      requestedModel,
      routeSource: 'account_mapping',
      matchedProviderCode: mappingTarget.groupAccess.providerCode
    }
  }

  const catalogRoute = await resolveCatalogProviderRoute({
    bindings,
    requestedModel,
    systemAccountId: apiKeyRecord.system_account_id
  })
  if (catalogRoute.outcome === 'missing') {
    return {
      outcome: 'failed',
      statusCode: 400,
      type: 'invalid_request_error',
      code: 'model_not_routable_for_api_key',
      message: `当前 API Key 绑定的供应商中没有可路由模型：${requestedModel}`,
      requestedModel,
      matchedProviderCodes: catalogRoute.matchedProviderCodes
    }
  }
  if (catalogRoute.outcome === 'ambiguous') {
    return {
      outcome: 'failed',
      statusCode: 400,
      type: 'invalid_request_error',
      code: 'model_route_ambiguous',
      message: `请求模型在多个供应商中同时存在，无法确定目标号池：${requestedModel}`,
      requestedModel,
      matchedProviderCodes: catalogRoute.matchedProviderCodes
    }
  }
  if (catalogRoute.outcome !== 'matched') {
    return {
      outcome: 'failed',
      statusCode: 400,
      type: 'invalid_request_error',
      code: 'model_route_unavailable',
      message: `请求模型无法确定目标号池：${requestedModel}`,
      requestedModel
    }
  }
  const matchedRoute = catalogRoute.route
  const activeProviderProtocolProfileIds = new Set(bindings.map(binding => binding.provider_protocol_profile_id))
  if (
    activeProviderProtocolProfileIds.size === 1
    && matchedRoute.providerProtocolProfileIds.size === 1
    && matchedRoute.providerProtocolProfileIds.has(bindings[0]!.provider_protocol_profile_id)
  ) {
    return { outcome: 'skipped', reason: 'single_provider_protocol_profile', requestedModel }
  }
  const candidateBindings = bindings
    .filter(binding => matchedRoute.providerProtocolProfileIds.has(binding.provider_protocol_profile_id))

  if (!candidateBindings.length) {
    return {
      outcome: 'failed',
      statusCode: 400,
      type: 'invalid_request_error',
      code: 'model_target_group_not_bound',
      message: `当前 API Key 未绑定请求模型对应的供应商分组：${requestedModel}`,
      requestedModel,
      matchedProviderCodes: matchedRoute.matchedProviderCodes
    }
  }

  const target = await selectGatewayModelTargetGroup({
    req,
    apiKeyRecord,
    bindings: candidateBindings,
    targetModel: requestedModel,
    requestClientCompatibility
  })
  if (!target) {
    return {
      outcome: 'failed',
      statusCode: 503,
      type: 'service_unavailable',
      code: 'model_target_group_unavailable',
      message: `请求模型对应的供应商分组当前没有可用账号：${requestedModel}`,
      requestedModel,
      matchedProviderCodes: matchedRoute.matchedProviderCodes
    }
  }

  const selectedProfileBindings = bindings
    .filter(candidate => candidate.provider_protocol_profile_id === target.binding.provider_protocol_profile_id)
    .map(copyGroupBinding)
  return {
    outcome: 'selected',
    apiKeyRecord: {
      ...apiKeyRecord,
      selected_group_id: target.groupId,
      group_bindings: selectedProfileBindings
    },
    groupId: target.groupId,
    groupAccess: target.groupAccess,
    accounts: target.accounts,
    responseInspectionPolicies: target.responseInspectionPolicies,
    requestedModel,
    routeSource: 'catalog_provider',
    matchedProviderCode: matchedRoute.providerCode
  }
}

function activeGatewayApiKeyGroupBindings(apiKeyRecord: GatewayApiKeyRow): GatewayApiKeyGroupBindingRow[] {
  return (apiKeyRecord.group_bindings ?? [])
    .filter(binding => binding.status === 'active')
    .map(copyGroupBinding)
}

function copyGroupBinding(binding: GatewayApiKeyGroupBindingRow): GatewayApiKeyGroupBindingRow {
  return { ...binding }
}

async function resolveCatalogProviderRoute(input: {
  bindings: GatewayApiKeyGroupBindingRow[]
  requestedModel: string
  systemAccountId: string
}): Promise<
  | { outcome: 'matched'; route: CatalogProviderRoute }
  | { outcome: 'missing' | 'ambiguous'; matchedProviderCodes: string[] }
> {
  const providerCodes = [...new Set(input.bindings.map(binding => binding.provider_code))]
  const route = await resolveCachedProviderModelRouteAsync({
    model: input.requestedModel,
    providerCodes,
    systemAccountId: input.systemAccountId,
    includeUnpriced: true
  })
  if (route.outcome !== 'matched') {
    return {
      outcome: route.outcome,
      matchedProviderCodes: route.matchedProviderCodes
    }
  }

  const providerCode = route.providerCode
  const providerProtocolProfileIds = new Set(
    input.bindings
      .filter(binding => binding.provider_code === providerCode)
      .map(binding => binding.provider_protocol_profile_id)
  )
  if (!providerProtocolProfileIds.size) {
    return { outcome: 'missing', matchedProviderCodes: route.matchedProviderCodes }
  }

  return {
    outcome: 'matched',
    route: {
      providerCode,
      providerProtocolProfileIds,
      matchedProviderCodes: route.matchedProviderCodes
    }
  }
}
