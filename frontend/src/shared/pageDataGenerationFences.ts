import type { PageDataDomain } from '@/api/domains/pageData'

const writeEpochs = new Map<PageDataDomain, number>()
let writeGeneration = 0
let securityGeneration = 0
let sessionGeneration = 0
let permissionGeneration = 0

export interface PageDataSecurityContextGeneration {
  generation: number
  sessionGeneration: number
  permissionGeneration: number
}

export function currentPageDataWriteEpoch(domain: PageDataDomain): number {
  return writeEpochs.get(domain) ?? 0
}

export function advancePageDataWriteEpoch(domains: readonly PageDataDomain[]): void {
  const uniqueDomains = new Set(domains)
  if (uniqueDomains.size > 0) writeGeneration += 1
  for (const domain of uniqueDomains) {
    writeEpochs.set(domain, currentPageDataWriteEpoch(domain) + 1)
  }
}

export function currentPageDataWriteGeneration(): number {
  return writeGeneration
}

export function currentPageDataSecurityGeneration(): number {
  return securityGeneration
}

export function currentPageDataSecurityContext(): PageDataSecurityContextGeneration {
  return { generation: securityGeneration, sessionGeneration, permissionGeneration }
}

export function advancePageDataSessionGeneration(): void {
  sessionGeneration += 1
  securityGeneration += 1
}

export function advancePageDataPermissionGeneration(): void {
  permissionGeneration += 1
  securityGeneration += 1
}

export function advancePageDataAuthenticationGeneration(): void {
  sessionGeneration += 1
  permissionGeneration += 1
  securityGeneration += 1
}
