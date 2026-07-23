import type { Request } from 'express'

import type { GatewayApiKeyRow } from '../../../storage/repositories.js'
import { checkGatewayAuthorizationQuotaBatchAsync } from '../quota/authorization-quota.service.js'
import {
  listCachedOpenAIAccountsForGroupAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from '../runtime/runtime-cache.service.js'
import {
  orderGatewayAccountsByRuntimeDegradation
} from '../runtime/account-side-effects.service.js'
import {
  filterGatewayAccountsByRequestCapability
} from './account-capability-filter.js'
import {
  filterGatewayAccountsByRequestedModel
} from './model-filter.js'
import { requestModel } from '../request/metadata.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'
import type { ClientCompatibilityCapability } from '../../../domain/types.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import { areGatewayAccountsCapacityBusyForLaneAsync } from './capacity.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import {
  advanceGatewayRoutePlanCursor,
  type GatewayRoutePlanSnapshot
} from '../routing/route-coordination.js'

export interface ApiKeyGroupFallbackCandidateInput {
  req: Request
  reason: string
  apiKeyRecord?: GatewayApiKeyRow
  systemAccountId: string
  groupId: string
  requestLane: OpenAIGatewayRequestLane
  requestClientCompatibility?: ClientCompatibilityCapability
  excludedAccountIds?: Iterable<string>
  routePlanSnapshot?: GatewayRoutePlanSnapshot<string>
}

export interface ApiKeyGroupFallbackCandidate {
  groupId: string
  accounts: UpstreamAccount[]
  responseInspectionPolicies: ResponseInspectionPolicySummary[]
  routePlanSnapshot?: GatewayRoutePlanSnapshot<string>
}

export function canAttemptApiKeyGroupFallback(
  apiKeyRecord: GatewayApiKeyRow | undefined,
  groupId: string,
  routePlanSnapshot?: GatewayRoutePlanSnapshot<string>
): boolean {
  if (routePlanSnapshot) {
    return routePlanSnapshot.cursor < routePlanSnapshot.orderedAllowedTargets.length - 1
  }
  const bindings = apiKeyRecord?.group_bindings ?? []
  if (bindings.length <= 1) {
    return false
  }
  const currentIndex = bindings.findIndex((binding) => binding.group_id === groupId)
  return currentIndex >= 0 && currentIndex < bindings.length - 1
}

export async function resolveNextApiKeyGroupFallbackCandidate(
  input: ApiKeyGroupFallbackCandidateInput
): Promise<ApiKeyGroupFallbackCandidate | undefined> {
  const bindings = input.apiKeyRecord?.group_bindings ?? []
  const routePlanSnapshot = input.routePlanSnapshot
  const currentIndex = bindings.findIndex((binding) => binding.group_id === input.groupId)
  const allowedBindingIds = new Set(bindings.filter((binding) => binding.status === 'active' && binding.group_enabled !== 0).map((binding) => binding.group_id))
  const candidateGroupIds = routePlanSnapshot
    ? routePlanSnapshot.orderedAllowedTargets.slice(routePlanSnapshot.cursor + 1).filter((groupId) => allowedBindingIds.has(groupId))
    : (currentIndex >= 0
      ? bindings.slice(currentIndex + 1).map((binding) => binding.group_id)
      : bindings.filter((binding) => binding.group_id !== input.groupId).map((binding) => binding.group_id))
  const requestedModel = requestModel(input.req)
  const sourceEndpointFamily = gatewayRequestEndpointFamily(input.req)
  const excludedAccountIds = new Set(input.excludedAccountIds ?? [])
  const seenGroupIds = new Set<string>()
  for (const candidateGroupId of candidateGroupIds) {
    if (!candidateGroupId || seenGroupIds.has(candidateGroupId)) {
      continue
    }
    seenGroupIds.add(candidateGroupId)
    const groupAccess = await resolveCachedGroupUsageAccessMetadataAsync(candidateGroupId, input.systemAccountId)
    if (!groupAccess) {
      continue
    }
    const accounts = (await listCachedOpenAIAccountsForGroupAsync(candidateGroupId, input.systemAccountId, {
      requestedModel,
      requestedEndpointFamily: sourceEndpointFamily
    }))
      .filter((account) => !excludedAccountIds.has(account.id))
    if (!accounts.length) {
      continue
    }
    const capabilityFilter = filterGatewayAccountsByRequestCapability(input.req, accounts, {
      requestClientCompatibility: input.requestClientCompatibility
    })
    if (!capabilityFilter.accounts.length) {
      continue
    }
    const modelFilter = filterGatewayAccountsByRequestedModel(capabilityFilter.accounts, requestedModel, sourceEndpointFamily)
    if (!modelFilter.accounts.length) {
      continue
    }
    const accountQuotaDecisions = await checkGatewayAuthorizationQuotaBatchAsync({ groupAccess, accounts: modelFilter.accounts })
    const quotaAllowedAccounts = modelFilter.accounts.filter((account) => {
      const decision = accountQuotaDecisions.get(account.id) ?? { allowed: true }
      return decision.allowed
    })
    if (!quotaAllowedAccounts.length) {
      continue
    }
    const runtimeDegradationOrder = orderGatewayAccountsByRuntimeDegradation(quotaAllowedAccounts, {
      modelRankByAccountId: modelFilter.modelPriority.rankByAccountId
    })
    if (input.reason === 'runtime_degraded' && runtimeDegradationOrder.bypassedAllDegraded) {
      continue
    }
    const orderedQuotaAllowedAccounts = runtimeDegradationOrder.accounts
    if ((input.reason === 'high_concurrency_group_busy' || input.reason === 'group_capacity_busy')
      && await areGatewayAccountsCapacityBusyForLaneAsync(orderedQuotaAllowedAccounts, input.requestLane, groupAccess.schedulingPolicy)) {
      continue
    }
    return {
      groupId: candidateGroupId,
      accounts: orderedQuotaAllowedAccounts,
      responseInspectionPolicies: [],
      routePlanSnapshot: routePlanSnapshot
        ? advanceGatewayRoutePlanCursor(routePlanSnapshot, routePlanSnapshot.orderedAllowedTargets.indexOf(candidateGroupId))
        : undefined
    }
  }
  return undefined
}
