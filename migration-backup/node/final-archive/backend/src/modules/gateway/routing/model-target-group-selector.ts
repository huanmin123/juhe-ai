import type { Request } from 'express'

import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import type {
  GatewayApiKeyRow,
  GroupUsageAccessMetadata,
  OpenAIAccountSecret
} from '../../../storage/repositories.js'
import type { GatewayApiKeyGroupBindingRow } from '../../../storage/gateway-api-key.repository.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import { filterGatewayAccountsByRequestCapability } from '../dispatch/account-capability-filter.js'
import {
  filterGatewayAccountsByRequestedModel,
  type GatewayModelAccountFilterResult
} from '../dispatch/model-filter.js'
import {
  listCachedOpenAIAccountsForGroupAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from '../runtime/runtime-cache.service.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'

export interface GatewayModelTargetGroupCandidate {
  binding: GatewayApiKeyGroupBindingRow
  groupAccess: GroupUsageAccessMetadata
  accounts: OpenAIAccountSecret[]
  modelFilter: GatewayModelAccountFilterResult
}

export interface GatewayModelTargetGroupSelection extends GatewayModelTargetGroupCandidate {
  groupId: string
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
}

export async function selectGatewayModelTargetGroup(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  bindings: GatewayApiKeyGroupBindingRow[]
  targetModel: string
  requestClientCompatibility?: ClientCompatibilityCapability
  acceptCandidate?: (candidate: GatewayModelTargetGroupCandidate) => boolean
  candidatePriority?: (candidate: GatewayModelTargetGroupCandidate) => number
}): Promise<GatewayModelTargetGroupSelection | undefined> {
  const sourceEndpointFamily = gatewayRequestEndpointFamily(input.req)
  let selected: GatewayModelTargetGroupSelection | undefined
  let selectedPriority = Number.NEGATIVE_INFINITY
  for (const binding of uniqueGatewayGroupBindings(input.bindings)) {
    const groupAccess = await resolveCachedGroupUsageAccessMetadataAsync(binding.group_id, input.apiKeyRecord.system_account_id)
    if (!groupAccess) {
      continue
    }
    const accounts = await listCachedOpenAIAccountsForGroupAsync(binding.group_id, input.apiKeyRecord.system_account_id, {
      requestedModel: input.targetModel,
      requestedEndpointFamily: sourceEndpointFamily
    })
    if (!accounts.length) {
      continue
    }
    const capabilityFilter = filterGatewayAccountsByRequestCapability(input.req, accounts, {
      requestClientCompatibility: input.requestClientCompatibility,
      requestModelOverride: input.targetModel
    })
    if (!capabilityFilter.accounts.length) {
      continue
    }
    const modelFilter = filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, input.targetModel, sourceEndpointFamily)
    if (!modelFilter.accounts.length) {
      continue
    }
    const candidate = {
      binding,
      groupAccess,
      accounts: modelFilter.accounts,
      modelFilter
    }
    if (input.acceptCandidate && !input.acceptCandidate(candidate)) {
      continue
    }
    const selection = {
      ...candidate,
      groupId: binding.group_id,
      responseInspectionPolicies: []
    }
    if (!input.candidatePriority) return selection
    const priority = input.candidatePriority(candidate)
    if (priority > selectedPriority) {
      selected = selection
      selectedPriority = priority
    }
  }
  return selected
}

function uniqueGatewayGroupBindings(bindings: GatewayApiKeyGroupBindingRow[]): GatewayApiKeyGroupBindingRow[] {
  const seenGroupIds = new Set<string>()
  const uniqueBindings: GatewayApiKeyGroupBindingRow[] = []
  for (const binding of bindings) {
    if (!binding.group_id || seenGroupIds.has(binding.group_id)) {
      continue
    }
    seenGroupIds.add(binding.group_id)
    uniqueBindings.push(binding)
  }
  return uniqueBindings
}
