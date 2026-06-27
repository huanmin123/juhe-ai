import type { Request } from 'express'

import type { AccountModelMapping, ApiKeyExplicitHybridRouteRule } from '../../../domain/types.js'
import type { GatewayApiKeyRow, GroupUsageAccessMetadata, OpenAIAccountSecret } from '../../../storage/repositories.js'
import type { ResponseInspectionPolicySummary } from '../../../storage/response-inspection-policy.repository.js'
import { requestModel } from '../request/metadata.js'
import { gatewayRequestEndpointFamily } from '../protocols/openai-v1/model-mapping.js'
import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import {
  listCachedActiveResponseInspectionPoliciesAsync,
  listCachedOpenAIAccountsForGroupAsync,
  resolveCachedGroupUsageAccessMetadataAsync
} from '../runtime/runtime-cache.service.js'

export type ExplicitHybridGatewayRouteResult =
  | {
      outcome: 'selected'
      rule: ApiKeyExplicitHybridRouteRule
      apiKeyRecord: GatewayApiKeyRow
      groupId: string
      groupAccess: GroupUsageAccessMetadata
      accounts: OpenAIAccountSecret[]
      responseInspectionPolicies: ResponseInspectionPolicySummary[]
      requestedModel: string
    }
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
      ruleId?: string
    }

export async function resolveExplicitHybridGatewayRoute(input: {
  req: Request
  apiKeyRecord: GatewayApiKeyRow
  clientStrategy: OpenAIGatewayClientStrategyContext
}): Promise<ExplicitHybridGatewayRouteResult> {
  const rules = input.apiKeyRecord.explicit_hybrid_route_rules ?? []
  if (!rules.length) {
    return { outcome: 'skipped', reason: 'empty_explicit_hybrid_route_rules' }
  }
  const requestedModel = requestModel(input.req)?.trim()
  if (!requestedModel) {
    return { outcome: 'skipped', reason: 'missing_requested_model' }
  }
  const sourceEndpointFamily = gatewayRequestEndpointFamily(input.req)
  if (!sourceEndpointFamily || !isModelMappingSourceEndpointFamily(sourceEndpointFamily)) {
    return { outcome: 'skipped', reason: 'unsupported_source_endpoint_family', requestedModel }
  }

  const activeBindings = new Map(
    (input.apiKeyRecord.group_bindings ?? [])
      .filter((binding) => binding.status === 'active' && binding.group_enabled !== 0)
      .map((binding) => [binding.group_id, binding])
  )

  for (const rule of rules) {
    if (rule.enabled === false) continue
    if (rule.sourceEndpointFamily !== sourceEndpointFamily) continue
    if (rule.sourceClientProfile !== 'auto' && rule.sourceClientProfile !== input.clientStrategy.clientProfile) continue
    if (rule.sourceModel && rule.sourceModel.trim().toLowerCase() !== requestedModel.toLowerCase()) continue
    const binding = activeBindings.get(rule.targetGroupId)
    if (!binding) {
      return failedRoute(rule, requestedModel, 'explicit_hybrid_target_group_not_bound', `显式混合路由目标分组未绑定或未启用：${rule.targetGroupId}`)
    }
    if (rule.targetProviderProtocolProfileId && rule.targetProviderProtocolProfileId !== binding.provider_protocol_profile_id) {
      return failedRoute(rule, requestedModel, 'explicit_hybrid_target_profile_mismatch', `显式混合路由目标协议档案与绑定分组不一致：${rule.id}`)
    }
    const groupAccess = await resolveCachedGroupUsageAccessMetadataAsync(rule.targetGroupId, input.apiKeyRecord.system_account_id)
    if (!groupAccess) {
      return failedRoute(rule, requestedModel, 'explicit_hybrid_target_group_unavailable', `显式混合路由目标分组不可用：${rule.targetGroupId}`)
    }
    let accounts = await listCachedOpenAIAccountsForGroupAsync(rule.targetGroupId, input.apiKeyRecord.system_account_id, {
      requestedModel: rule.upstreamModel,
      requestedEndpointFamily: rule.upstreamEndpointFamily
    })
    if (rule.targetAccountId) {
      accounts = accounts.filter((account) => account.id === rule.targetAccountId)
      if (!accounts.length) {
        return failedRoute(rule, requestedModel, 'explicit_hybrid_target_account_unavailable', `显式混合路由目标账号不可用或不在目标分组中：${rule.targetAccountId}`)
      }
    }
    const accountsWithRule = accounts.map((account) => ({
      ...account,
      modelMappings: mergeExplicitHybridRouteMappings(
        account.modelMappings,
        explicitHybridRouteRuntimeMappingsForAccount({
          rules,
          selectedRule: rule,
          requestedModel,
          account,
          clientProfile: input.clientStrategy.clientProfile,
          targetProviderProtocolProfileId: binding.provider_protocol_profile_id
        })
      )
    }))
    const responseInspectionPolicies = await listCachedActiveResponseInspectionPoliciesAsync({
      protocolCode: groupAccess.protocolCode,
      providerCode: groupAccess.providerCode
    })
    const selectedBindings = [...activeBindings.values()]
      .filter((candidate) => candidate.provider_protocol_profile_id === binding.provider_protocol_profile_id)
      .map((candidate) => ({ ...candidate }))
    return {
      outcome: 'selected',
      rule,
      apiKeyRecord: {
        ...input.apiKeyRecord,
        selected_group_id: rule.targetGroupId,
        group_bindings: selectedBindings
      },
      groupId: rule.targetGroupId,
      groupAccess,
      accounts: accountsWithRule,
      responseInspectionPolicies,
      requestedModel
    }
  }

  return { outcome: 'skipped', reason: 'no_explicit_hybrid_route_matched', requestedModel }
}

function explicitHybridRouteRuntimeMappingsForAccount(input: {
  rules: ApiKeyExplicitHybridRouteRule[]
  selectedRule: ApiKeyExplicitHybridRouteRule
  requestedModel: string
  account: OpenAIAccountSecret
  clientProfile: OpenAIGatewayClientStrategyContext['clientProfile']
  targetProviderProtocolProfileId?: string
}): AccountModelMapping[] {
  const selectedSourceModel = input.selectedRule.sourceModel?.trim() || input.requestedModel
  const selectedMapping = explicitHybridRouteRuntimeMapping(input.selectedRule, selectedSourceModel)
  const companionMappings = input.rules
    .filter((rule) => rule.id !== input.selectedRule.id)
    .filter((rule) => rule.enabled !== false)
    .filter((rule) => rule.targetGroupId === input.selectedRule.targetGroupId)
    .filter((rule) => !rule.targetProviderProtocolProfileId || rule.targetProviderProtocolProfileId === input.targetProviderProtocolProfileId)
    .filter((rule) => !rule.targetAccountId || rule.targetAccountId === input.account.id)
    .filter((rule) => rule.sourceClientProfile === 'auto' || rule.sourceClientProfile === input.clientProfile)
    .filter((rule) => (rule.sourceModel?.trim() || input.requestedModel).toLowerCase() === selectedSourceModel.toLowerCase())
    .filter((rule) => accountCanUseExplicitHybridRuntimeMapping(input.account, rule))
    .map((rule) => explicitHybridRouteRuntimeMapping(rule, rule.sourceModel?.trim() || input.requestedModel))
  return [selectedMapping, ...companionMappings].filter((mapping) => !isExactIdentityModelMapping(mapping))
}

function explicitHybridRouteRuntimeMapping(
  rule: ApiKeyExplicitHybridRouteRule,
  sourceModel: string
): AccountModelMapping {
  return {
    sourceModel,
    sourceEndpointFamily: rule.sourceEndpointFamily,
    upstreamModel: rule.upstreamModel,
    upstreamEndpointFamily: rule.upstreamEndpointFamily,
    enabled: true,
    runtimeSource: 'explicit_hybrid_route',
    runtimeRouteRuleId: rule.id
  }
}

function mergeExplicitHybridRouteMappings(
  mappings: AccountModelMapping[] | undefined,
  runtimeMappings: AccountModelMapping[]
): AccountModelMapping[] {
  if (!runtimeMappings.length) return mappings ?? []
  const runtimeMappingKeys = new Set(runtimeMappings.map((mapping) => modelMappingSourceKey(mapping)))
  const withoutSameSource = (mappings ?? []).filter((item) => !runtimeMappingKeys.has(modelMappingSourceKey(item)))
  return [...runtimeMappings, ...withoutSameSource]
}

function modelMappingSourceKey(mapping: AccountModelMapping): string {
  return `${mapping.sourceEndpointFamily}:${mapping.sourceModel.toLowerCase()}`
}

function isExactIdentityModelMapping(mapping: AccountModelMapping): boolean {
  return mapping.sourceModel === mapping.upstreamModel
    && mapping.sourceEndpointFamily === mapping.upstreamEndpointFamily
}

function accountCanUseExplicitHybridRuntimeMapping(
  account: OpenAIAccountSecret,
  rule: ApiKeyExplicitHybridRouteRule
): boolean {
  const supportedModels = account.supportedModels ?? []
  if (supportedModels.length && !supportedModels.some((model) => model === rule.upstreamModel)) {
    return false
  }
  const modes = account.supportedEndpointModes ?? []
  if (!modes.length) return true
  if (rule.upstreamEndpointFamily === 'chat_completions') {
    return modes.includes('chat_json') || modes.includes('chat_sse')
  }
  if (rule.upstreamEndpointFamily === 'responses') {
    return modes.includes('responses_json') || modes.includes('responses_sse')
  }
  if (rule.upstreamEndpointFamily === 'messages') {
    return modes.includes('messages_json') || modes.includes('messages_sse')
  }
  if (rule.upstreamEndpointFamily === 'generate_content') {
    return modes.includes('generate_content_json') || modes.includes('generate_content_sse')
  }
  return false
}

function failedRoute(
  rule: ApiKeyExplicitHybridRouteRule,
  requestedModel: string,
  code: string,
  message: string
): ExplicitHybridGatewayRouteResult {
  return {
    outcome: 'failed',
    statusCode: code.endsWith('_not_bound') || code.endsWith('_mismatch') ? 400 : 503,
    type: code.endsWith('_not_bound') || code.endsWith('_mismatch') ? 'invalid_request_error' : 'service_unavailable',
    code,
    message,
    requestedModel,
    ruleId: rule.id
  }
}

function isModelMappingSourceEndpointFamily(value: string): value is AccountModelMapping['sourceEndpointFamily'] {
  return value === 'chat_completions'
    || value === 'responses'
    || value === 'messages'
    || value === 'generate_content'
    || value === 'stream_generate_content'
}
