import { openAIStreamInterceptRules, type StreamInterceptRule } from './openai-gateway-stream-rules.js'

export interface StreamInterceptRuleCatalogItem {
  id: string
  enabled: boolean
  name: string
  description?: string
  rationale?: string
  source?: string
  provider: string
  endpoint: string
  action: string
  triggerPhase: string
  accountPolicy: string
  rule: StreamInterceptRule
}

export function listStreamInterceptRuleCatalog(): StreamInterceptRuleCatalogItem[] {
  return openAIStreamInterceptRules.map((rule) => ({
    id: rule.id,
    enabled: rule.enabled,
    name: rule.name,
    description: rule.description,
    rationale: rule.rationale,
    source: rule.source,
    provider: rule.provider,
    endpoint: rule.endpoint,
    action: rule.action,
    triggerPhase: rule.triggerPhase,
    accountPolicy: rule.accountPolicy,
    rule
  }))
}
