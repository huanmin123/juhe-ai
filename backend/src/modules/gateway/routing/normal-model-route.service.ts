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

export type NormalGatewayModelRouteSource = 'catalog_provider' | 'account_model'

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

export interface ResolveNormalGatewayModelRouteInput {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  requestClientCompatibility?: ClientCompatibilityCapability
}

interface CatalogProviderRoute {
  providerCode: string
  providerProtocolProfileIds: Set<string>
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
  if (bindings.length <= 1) {
    return { outcome: 'skipped', reason: 'single_or_empty_binding', requestedModel }
  }

  const selectedBinding = bindings.find(binding => binding.group_id === apiKeyRecord.selected_group_id)
  const selectedProviderProtocolProfileId = selectedBinding?.provider_protocol_profile_id
  const catalogRoute = await resolveCatalogProviderRoute({
    bindings,
    requestedModel,
    systemAccountId: apiKeyRecord.system_account_id
  })
  const candidateBindings = catalogRoute
    ? bindings.filter(binding => catalogRoute.providerProtocolProfileIds.has(binding.provider_protocol_profile_id))
    : bindings

  if (!candidateBindings.length) {
    return { outcome: 'skipped', reason: 'empty_candidate_bindings', requestedModel }
  }

  const target = await selectGatewayModelTargetGroup({
    req,
    apiKeyRecord,
    bindings: candidateBindings,
    targetModel: requestedModel,
    requestClientCompatibility,
    acceptCandidate: ({ binding, modelFilter }) => {
      const explicitModelMatchCount = modelFilter.directMatchedCount + modelFilter.mappingMatchedCount
      const isCrossProfileCandidate =
        !selectedProviderProtocolProfileId ||
        binding.provider_protocol_profile_id !== selectedProviderProtocolProfileId
      return Boolean(catalogRoute || (isCrossProfileCandidate && explicitModelMatchCount > 0))
    }
  })
  if (!target) {
    return { outcome: 'skipped', reason: 'no_matching_group', requestedModel }
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
    routeSource: catalogRoute ? 'catalog_provider' : 'account_model',
    matchedProviderCode: catalogRoute?.providerCode
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
}): Promise<CatalogProviderRoute | undefined> {
  const providerCodes = [...new Set(input.bindings.map(binding => binding.provider_code))]
  if (providerCodes.length <= 1) {
    return undefined
  }

  const route = await resolveCachedProviderModelRouteAsync({
    model: input.requestedModel,
    providerCodes,
    systemAccountId: input.systemAccountId,
    includeUnpriced: true
  })
  if (route.outcome !== 'matched') {
    return undefined
  }

  const providerCode = route.providerCode
  const providerProtocolProfileIds = new Set(
    input.bindings
      .filter(binding => binding.provider_code === providerCode)
      .map(binding => binding.provider_protocol_profile_id)
  )
  if (!providerProtocolProfileIds.size) {
    return undefined
  }

  return { providerCode, providerProtocolProfileIds }
}
