import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { AccountModelMappingEndpointFamily } from '../../../domain/types.js'
import { resolveOpenAIAccountModelMapping } from '../protocols/openai-v1/model-mapping.js'

export interface GatewayModelAccountFilterResult {
  accounts: UpstreamAccount[]
  skippedCount: number
  limitedAccountCount: number
  unrestrictedAccountCount: number
  directMatchedCount: number
  mappingMatchedCount: number
  requestedModel?: string
  sourceEndpointFamily?: AccountModelMappingEndpointFamily
  modelPriority: GatewayAccountModelPriority
  reason?: 'missing_model' | 'unsupported_model'
}

export interface GatewayAccountModelPriority {
  requestedModel?: string
  sourceEndpointFamily?: AccountModelMappingEndpointFamily
  rankByAccountId: ReadonlyMap<string, number>
}

export const gatewayAccountModelPriorityRank = {
  direct: 0,
  mapping: 1,
  unrestricted: 2,
  unsupported: 3
} as const

export function filterGatewayAccountsByRequestedModel(
  accounts: UpstreamAccount[],
  requestedModel?: string,
  sourceEndpointFamily?: AccountModelMappingEndpointFamily
): GatewayModelAccountFilterResult {
  const model = requestedModel?.trim()
  let skippedCount = 0
  let limitedAccountCount = 0
  let unrestrictedAccountCount = 0
  let directMatchedCount = 0
  let mappingMatchedCount = 0
  const directMatchedAccounts: UpstreamAccount[] = []
  const mappingMatchedAccounts: UpstreamAccount[] = []
  const unrestrictedAccounts: UpstreamAccount[] = []
  const rankByAccountId = new Map<string, number>()

  for (const account of accounts) {
    const supportedModels = account.supportedModels ?? []
    const mapping = resolveOpenAIAccountModelMapping(account, model, sourceEndpointFamily)
    if (mapping && isMappingAllowedBySupportedModels(mapping.upstreamModel, supportedModels)) {
      if (supportedModels.length) {
        limitedAccountCount += 1
      } else {
        unrestrictedAccountCount += 1
      }
      mappingMatchedCount += 1
      rankByAccountId.set(account.id, gatewayAccountModelPriorityRank.mapping)
      mappingMatchedAccounts.push(account)
      continue
    }
    if (!supportedModels.length) {
      unrestrictedAccountCount += 1
      rankByAccountId.set(account.id, gatewayAccountModelPriorityRank.unrestricted)
      unrestrictedAccounts.push(account)
      continue
    }
    limitedAccountCount += 1
    const match = resolveGatewayAccountModelMatch(model, supportedModels)
    if (match === 'direct') {
      directMatchedCount += 1
      rankByAccountId.set(account.id, gatewayAccountModelPriorityRank.direct)
      directMatchedAccounts.push(account)
      continue
    }
    skippedCount += 1
    rankByAccountId.set(account.id, gatewayAccountModelPriorityRank.unsupported)
  }
  const filtered = [
    ...directMatchedAccounts,
    ...mappingMatchedAccounts,
    ...unrestrictedAccounts
  ]

  return {
    accounts: filtered,
    skippedCount,
    limitedAccountCount,
    unrestrictedAccountCount,
    directMatchedCount,
    mappingMatchedCount,
    requestedModel: model || undefined,
    sourceEndpointFamily,
    modelPriority: {
      requestedModel: model || undefined,
      sourceEndpointFamily,
      rankByAccountId
    },
    reason: skippedCount > 0 && filtered.length === 0
      ? model ? 'unsupported_model' : 'missing_model'
      : undefined
  }
}

function isMappingAllowedBySupportedModels(upstreamModel: string, supportedModels: string[]): boolean {
  if (!supportedModels.length) return true
  return supportedModels.some((model) => model === upstreamModel)
}

type GatewayAccountModelMatch = 'direct' | undefined

function resolveGatewayAccountModelMatch(
  requestedModel: string | undefined,
  supportedModels: string[]
): GatewayAccountModelMatch {
  if (!requestedModel) return undefined
  if (supportedModels.includes(requestedModel)) {
    return 'direct'
  }
  return undefined
}

export function gatewayModelFilterFailureMessage(result: GatewayModelAccountFilterResult): string {
  if (result.reason === 'missing_model') {
    return '请求缺少 model，当前分组内可用账户均配置了模型限制，无法调度'
  }
  return `当前分组无账户支持请求模型：${result.requestedModel ?? '未知模型'}`
}

export function compareGatewayAccountModelPriority(
  left: Pick<UpstreamAccount, 'id'>,
  right: Pick<UpstreamAccount, 'id'>,
  priority?: GatewayAccountModelPriority
): number {
  return gatewayAccountModelPriority(left, priority) - gatewayAccountModelPriority(right, priority)
}

export function gatewayAccountModelPriority(
  account: Pick<UpstreamAccount, 'id'>,
  priority?: GatewayAccountModelPriority
): number {
  if (!priority) {
    return gatewayAccountModelPriorityRank.direct
  }
  return priority.rankByAccountId.get(account.id) ?? gatewayAccountModelPriorityRank.unsupported
}
