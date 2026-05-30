import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'
import { defaultAvoidanceTtlSeconds } from '../stream-intercept-policies/streamInterceptActionTemplates'

export function createBlankAccountStreamInterceptRule(priority = 100): AccountStreamInterceptRuleForm {
  return {
    enabled: true,
    name: '中转流污染拦截',
    priority,
    eventTypes: '',
    dataTypes: '',
    errorCodes: '',
    errorTypes: '',
    textIncludes: '',
    textExcludes: '',
    jsonPathsExists: '',
    action: 'avoid_account_ttl',
    avoidanceTtlSeconds: defaultAvoidanceTtlSeconds,
    notes: ''
  }
}

export function nextStreamInterceptRulePriority(rules: AccountStreamInterceptRuleForm[]): number {
  const max = Math.max(0, ...rules.map((rule) => Number(rule.priority ?? 0)).filter(Number.isFinite))
  return Math.min(9999, max + 10 || 100)
}
