import type { UpstreamAccount } from './openai-gateway-route-helpers.js'

export interface GatewayModelAccountFilterResult {
  accounts: UpstreamAccount[]
  skippedCount: number
  limitedAccountCount: number
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
  const filtered: UpstreamAccount[] = []

  for (const account of accounts) {
    const supportedModels = account.supportedModels ?? []
    if (!supportedModels.length) {
      filtered.push(account)
      continue
    }
    limitedAccountCount += 1
    if (model && supportedModels.includes(model)) {
      filtered.push(account)
      continue
    }
    skippedCount += 1
  }

  return {
    accounts: filtered,
    skippedCount,
    limitedAccountCount,
    requestedModel: model || undefined,
    reason: skippedCount > 0 && filtered.length === 0
      ? model ? 'unsupported_model' : 'missing_model'
      : undefined
  }
}

export function gatewayModelFilterFailureMessage(result: GatewayModelAccountFilterResult): string {
  if (result.reason === 'missing_model') {
    return '请求缺少 model，当前分组内可用账户均配置了模型限制，无法调度'
  }
  return `当前分组无账户支持请求模型：${result.requestedModel ?? '未知模型'}`
}
