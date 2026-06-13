import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'

export interface GatewayModelAccountFilterResult {
  accounts: UpstreamAccount[]
  skippedCount: number
  limitedAccountCount: number
  directMatchedCount: number
  mappingMatchedCount: number
  requestedModel?: string
  reason?: 'missing_model' | 'unsupported_model'
}

export function filterGatewayAccountsByRequestedModel(
  accounts: UpstreamAccount[],
  requestedModel?: string
): GatewayModelAccountFilterResult {
  const model = requestedModel?.trim()
  let skippedCount = 0
  let limitedAccountCount = 0
  let directMatchedCount = 0
  let mappingMatchedCount = 0
  const filtered: UpstreamAccount[] = []

  for (const account of accounts) {
    const supportedModels = account.supportedModels ?? []
    if (!supportedModels.length) {
      filtered.push(account)
      continue
    }
    limitedAccountCount += 1
    const match = resolveGatewayAccountModelMatch(account, model, supportedModels)
    if (match === 'direct') {
      directMatchedCount += 1
      filtered.push(account)
      continue
    }
    if (match === 'mapping') {
      mappingMatchedCount += 1
      filtered.push(account)
      continue
    }
    skippedCount += 1
  }

  return {
    accounts: filtered,
    skippedCount,
    limitedAccountCount,
    directMatchedCount,
    mappingMatchedCount,
    requestedModel: model || undefined,
    reason: skippedCount > 0 && filtered.length === 0
      ? model ? 'unsupported_model' : 'missing_model'
      : undefined
  }
}

type GatewayAccountModelMatch = 'direct' | 'mapping' | undefined

function resolveGatewayAccountModelMatch(
  account: UpstreamAccount,
  requestedModel: string | undefined,
  supportedModels: string[]
): GatewayAccountModelMatch {
  if (!requestedModel) return undefined
  if (supportedModels.includes(requestedModel)) {
    return 'direct'
  }
  const mapping = (account.modelMappings ?? []).find((item) =>
    item.enabled !== false
    && item.sourceModel === requestedModel
    && item.upstreamModel !== item.sourceModel
  )
  if (!mapping) return undefined
  return supportedModels.includes(mapping.upstreamModel) ? 'mapping' : undefined
}

export function gatewayModelFilterFailureMessage(result: GatewayModelAccountFilterResult): string {
  if (result.reason === 'missing_model') {
    return '请求缺少 model，当前分组内可用账户均配置了模型限制，无法调度'
  }
  return `当前分组无账户支持请求模型：${result.requestedModel ?? '未知模型'}`
}
