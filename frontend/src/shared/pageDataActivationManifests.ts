import type { PageDataDomain } from '@/api/domains/pageData'

export interface PageDataActivationManifest {
  route: string
  domains: readonly PageDataDomain[]
}

export const myAccountsPageDataActivationManifest = Object.freeze({
  route: '/my-accounts',
  domains: Object.freeze([
    'accounts.static',
    'accounts.options',
    'providers.catalog'
  ] as const)
}) satisfies PageDataActivationManifest
