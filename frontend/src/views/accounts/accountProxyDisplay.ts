import type { AccountListItem, ProxyProfileOptionSummary } from '@/types/domain'

export function accountProxyDisplay(
  account: AccountListItem,
  fallback?: ProxyProfileOptionSummary
): ProxyProfileOptionSummary | undefined {
  if (account.proxyProfileName !== undefined || account.proxyProfileType !== undefined || account.proxyProfileEnabled !== undefined) {
    return {
      id: account.proxyProfileId ?? '',
      name: account.proxyProfileName ?? '',
      type: account.proxyProfileType ?? '',
      enabled: account.proxyProfileEnabled ?? true
    }
  }
  if (account.proxyProfileUnavailable) return undefined
  return fallback
}
