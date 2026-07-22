import type { PageDataDomain } from '@/api/domains/pageData'
import { advancePageDataWriteEpoch } from './pageDataGenerationFences'
import { invalidateDefaultPageDataResourceCache } from './pageDataResourceCache'

const providerMutationDomains: PageDataDomain[] = ['accounts.options', 'providers.catalog']
const statsMutationDomains: PageDataDomain[] = ['stats.overview', 'stats.accountUsage', 'stats.aiPerformance']
const accountMutationDomains: PageDataDomain[] = ['accounts.static', 'accounts.options', 'groups.static', ...statsMutationDomains]
const systemPrincipalMutationDomains: PageDataDomain[] = ['accounts.static', 'accounts.options', 'systemAccounts.options', 'teams.options', ...statsMutationDomains]
const teamMutationDomains: PageDataDomain[] = ['accounts.static', 'accounts.options', 'groups.static', 'systemAccounts.options', 'teams.options', ...statsMutationDomains]
const authorizationMutationDomains: PageDataDomain[] = ['accounts.static', 'accounts.options', 'groups.static', 'systemAccounts.options', 'teams.options', ...statsMutationDomains]

export function pageDataDomainsForMutation(method: string | undefined, url: string | undefined): PageDataDomain[] {
  const normalizedMethod = method?.trim().toLowerCase()
  if (!normalizedMethod || ['get', 'head', 'options'].includes(normalizedMethod)) return []
  const path = requestPath(url)
  if (!path || path === '/data-changes/confirm') return []

  if (/^\/providers\/[^/]+\/(?:models(?:\/[^/]+)?|default-health-check-model)$/.test(path)) {
    return [...providerMutationDomains]
  }
  if (/^\/(?:my-)?openai-oauth\/(?:create-from-(?:code|refresh-token)|accounts\/[^/]+\/(?:refresh-token|reauthorize-from-(?:code|refresh-token)))$/.test(path)) {
    return [...accountMutationDomains]
  }
  if (/^\/(?:my-)?accounts(?:\/|$)/.test(path) && !isAccountCommandPath(path)) {
    return [...accountMutationDomains]
  }
  if (/^\/(?:my-)?groups(?:\/|$)/.test(path)) return ['groups.static', 'routeStrategies.options', ...statsMutationDomains]
  if (/^\/(?:my-)?route-strategies(?:\/|$)/.test(path)) return ['routeStrategies.options']
  if (/^\/system-accounts(?:\/|$)/.test(path)) return [...systemPrincipalMutationDomains]
  if (/^\/(?:my-)?system-teams(?:\/|$)/.test(path)) return [...teamMutationDomains]
  if (/^\/(?:my-)?authorizations(?:\/|$)/.test(path)) return [...authorizationMutationDomains]
  return []
}

export async function invalidatePageDataForSuccessfulMutation(method: string | undefined, url: string | undefined): Promise<void> {
  const domains = pageDataDomainsForMutation(method, url)
  if (!domains.length) return
  try {
    await invalidateDefaultPageDataResourceCache(domains)
  } catch (error) {
    console.error('页面数据写后缓存失效失败', error)
  }
}

export function beginPageDataMutation(method: string | undefined, url: string | undefined): PageDataDomain[] {
  const domains = pageDataDomainsForMutation(method, url)
  advancePageDataWriteEpoch(domains)
  return domains
}

function requestPath(url: string | undefined): string {
  const value = url?.trim()
  if (!value) return ''
  try {
    const path = new URL(value, 'http://local.invalid').pathname
    const apiPrefix = '/__aisys__/api'
    return path.startsWith(apiPrefix) ? path.slice(apiPrefix.length) || '/' : path
  } catch {
    return value.split('?')[0] ?? ''
  }
}

function isAccountCommandPath(path: string): boolean {
  return (
    /^\/(?:my-)?accounts\/(?:test-draft|test-sessions|test-tasks)(?:\/|$)/.test(path)
    || /^\/(?:my-)?accounts\/(?:export|import\/preview|batch-edit-context|balance\/test-draft)(?:\/|$)/.test(path)
    || /\/(?:test|test-options|test-status|cancel-test|restore-test|balance-query)(?:\/|$)/.test(path)
  )
}
