import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'

export function createBlankAccountStreamInterceptRule(priority = 1): AccountStreamInterceptRuleForm {
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
    notes: ''
  }
}

export function nextStreamInterceptRulePriority(rules: AccountStreamInterceptRuleForm[]): number {
  return nextAvailablePriority(rules.map((rule) => rule.priority))
}

export function normalizeAccountStreamInterceptRulePriorities(rules: AccountStreamInterceptRuleForm[]): AccountStreamInterceptRuleForm[] {
  return rules.map((rule, index) => ({ ...rule, priority: index + 1 }))
}

function nextAvailablePriority(values: Array<number | null>): number {
  const used = new Set(values.filter((value): value is number => Number.isInteger(value) && value > 0 && value <= 9999))
  for (let priority = 1; priority <= 9999; priority += 1) {
    if (!used.has(priority)) return priority
  }
  return 9999
}
