import type { SecurityPolicyOptionSummary } from '@/types/domain'
import { rememberSelectOption, selectLabelForValue } from '@/shared/selectLabelCache'

const securityPolicyCacheKey = 'security-policies'

export function rememberSecurityPolicyLabels(options: SecurityPolicyOptionSummary[], cacheKey = securityPolicyCacheKey): void {
  for (const option of options) {
    rememberSelectOption(cacheKey, option.id, option.name)
  }
}

export function rememberSecurityPolicySelectionLabel(
  id: string | null | undefined,
  name: string | undefined,
  cacheKey = securityPolicyCacheKey
): void {
  rememberSelectOption(cacheKey, id ?? undefined, name)
}

export function securityPolicyLabelForId(id: string | undefined, cacheKey = securityPolicyCacheKey): string | undefined {
  return selectLabelForValue(cacheKey, id)
}
