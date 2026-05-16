import { openAIUpstreamErrorFeatureRules, type UpstreamErrorFeatureRule } from './openai-gateway-upstream-error-rules.js'

export interface UpstreamErrorFeatureRuleCatalogItem {
  id: string
  enabled: boolean
  name: string
  description?: string
  rationale?: string
  source?: string
  provider: string
  endpoint: string
  action: string
  accountPolicy: string
  rule: UpstreamErrorFeatureRule
}

export function listUpstreamErrorFeatureRuleCatalog(): UpstreamErrorFeatureRuleCatalogItem[] {
  return openAIUpstreamErrorFeatureRules.map((rule) => ({
    id: rule.id,
    enabled: rule.enabled,
    name: rule.name,
    description: rule.description,
    rationale: rule.rationale,
    source: rule.source,
    provider: rule.provider,
    endpoint: rule.endpoint,
    action: rule.action,
    accountPolicy: rule.accountPolicy,
    rule
  }))
}
