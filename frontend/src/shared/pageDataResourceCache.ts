import type {
  PageDataConfirmRequest,
  PageDataConfirmResult,
  PageDataDomain
} from '@/api/domains/pageData'
import {
  PageDataCacheController,
  createPageDataCacheKey,
  createPageDataCacheStorage,
  getDefaultPageDataTabCoordinator,
  type PageDataCacheControllerOptions,
  type PageDataCacheStorage,
  type PageDataLoadResult,
  type PageDataResourceCacheDefinition,
  type PageDataTabCoordinator
} from './pageDataCache'
import type { PageDataActivationHandle } from './pageDataActivationCoordinator'
import { pageDataDomainRegistry } from './pageDataDomainRegistry'

interface PageDataResourceCacheOptions {
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  confirmBatchKey?: string
  storage?: PageDataCacheStorage
  tabCoordinator?: PageDataTabCoordinator
  now?: () => Date
  maxControllers?: number
  activation?: PageDataActivationHandle
  writeEpoch?: (domain: PageDataDomain) => number
}

interface ResourceEntry {
  controller: PageDataCacheController<unknown>
  domain: PageDataDomain
  scope: string
  route: string
  lastAccessedAt: number
  activation?: PageDataActivationHandle
  writeEpoch?: (domain: PageDataDomain) => number
}

interface ResourceBinding {
  activation?: PageDataActivationHandle
  writeEpoch?: (domain: PageDataDomain) => number
}

export class PageDataResourceCache {
  private readonly confirm: PageDataResourceCacheOptions['confirm']
  private readonly confirmBatchKey?: string
  private readonly storage: PageDataCacheStorage
  private readonly tabCoordinator: PageDataTabCoordinator
  private readonly now?: () => Date
  private readonly maxControllers: number
  private readonly activation?: PageDataActivationHandle
  private readonly writeEpoch?: (domain: PageDataDomain) => number
  private readonly entries = new Map<string, ResourceEntry>()
  private readonly pendingLoads = new Map<string, Promise<PageDataLoadResult<unknown>>>()
  private readonly removeDomainInvalidationListener: () => void
  private closed = false

  constructor(options: PageDataResourceCacheOptions) {
    this.confirm = options.confirm
    this.confirmBatchKey = options.confirmBatchKey
    this.storage = options.storage ?? createPageDataCacheStorage()
    this.tabCoordinator = options.tabCoordinator ?? getDefaultPageDataTabCoordinator()
    this.now = options.now
    this.maxControllers = Math.max(1, Math.min(Math.trunc(options.maxControllers ?? 100), 500))
    this.activation = options.activation
    this.writeEpoch = options.writeEpoch
    this.removeDomainInvalidationListener = this.tabCoordinator.onDomainInvalidated((invalidation) => {
      void this.invalidateLocal(invalidation.domain, invalidation.scope, invalidation.route, false)
    })
  }

  async load<T>(request: PageDataResourceCacheDefinition<T>): Promise<PageDataLoadResult<T>> {
    if (this.closed) throw new Error('页面资源缓存已关闭')
    const key = createPageDataCacheKey({ ...request.cacheKey, domain: request.domain })
    const binding = this.bindingFor(request)
    const entry = this.entryFor(key, request, binding)
    const pending = this.pendingLoads.get(key)
    if (pending) return await pending as PageDataLoadResult<T>

    entry.lastAccessedAt = Date.now()
    const operation = entry.controller.load() as Promise<PageDataLoadResult<unknown>>
    this.pendingLoads.set(key, operation)
    try {
      return await operation as PageDataLoadResult<T>
    } finally {
      if (this.pendingLoads.get(key) === operation) this.pendingLoads.delete(key)
    }
  }

  async invalidate(domain: PageDataDomain, scope?: string, route?: string): Promise<void> {
    await this.invalidateLocal(domain, scope, route, true)
  }

  private async invalidateLocal(domain: PageDataDomain, scope?: string, route?: string, notifyPeers = false): Promise<void> {
    for (const [key, entry] of this.entries) {
      if (
        entry.domain !== domain
        || (scope && entry.scope !== scope)
        || (route && entry.route !== route)
      ) continue
      entry.controller.close()
      this.entries.delete(key)
      this.pendingLoads.delete(key)
      this.tabCoordinator.notifyInvalidated(key)
    }
    await this.storage.removeDomain(domain, scope, route)
    if (notifyPeers) this.tabCoordinator.notifyDomainInvalidated(domain, scope, route)
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    for (const entry of this.entries.values()) entry.controller.close()
    this.entries.clear()
    this.pendingLoads.clear()
    this.removeDomainInvalidationListener()
  }

  private bindingFor<T>(request: PageDataResourceCacheDefinition<T>): ResourceBinding {
    return {
      activation: request.activation ?? this.activation,
      writeEpoch: request.writeEpoch ?? this.writeEpoch
    }
  }

  private entryFor<T>(
    key: string,
    request: PageDataResourceCacheDefinition<T>,
    binding: ResourceBinding
  ): ResourceEntry {
    const current = this.entries.get(key)
    if (
      current
      && current.activation === binding.activation
      && current.writeEpoch === binding.writeEpoch
    ) return current
    if (current) {
      current.controller.close()
      this.entries.delete(key)
      this.pendingLoads.delete(key)
    }
    const domainSpec = pageDataDomainRegistry.find((spec) => spec.domain === request.domain)
    const entry: ResourceEntry = {
      controller: new PageDataCacheController<unknown>({
        ...request,
        maxStaleMs: request.maxStaleMs ?? domainSpec?.maxStaleMs,
        storage: this.storage,
        confirm: this.confirm,
        confirmBatchKey: this.confirmBatchKey,
        tabCoordinator: this.tabCoordinator,
        now: this.now,
        activation: binding.activation,
        writeEpoch: binding.writeEpoch
      } as PageDataCacheControllerOptions<unknown>),
      domain: request.domain,
      scope: request.cacheKey.scope,
      route: request.cacheKey.route,
      lastAccessedAt: Date.now(),
      activation: binding.activation,
      writeEpoch: binding.writeEpoch
    }
    this.entries.set(key, entry)
    this.trimControllers()
    return entry
  }

  private trimControllers(): void {
    if (this.entries.size <= this.maxControllers) return
    const oldest = [...this.entries.entries()]
      .sort((left, right) => left[1].lastAccessedAt - right[1].lastAccessedAt)[0]
    if (!oldest) return
    oldest[1].controller.close()
    this.entries.delete(oldest[0])
    this.pendingLoads.delete(oldest[0])
  }
}

let defaultPageDataResourceCache: PageDataResourceCache | undefined

export function getDefaultPageDataResourceCache(
  confirm: PageDataResourceCacheOptions['confirm']
): PageDataResourceCache {
  defaultPageDataResourceCache ??= new PageDataResourceCache({ confirm })
  return defaultPageDataResourceCache
}

export async function invalidateDefaultPageDataResourceCache(domains: PageDataDomain[]): Promise<void> {
  const uniqueDomains = [...new Set(domains)]
  if (defaultPageDataResourceCache) {
    for (const domain of uniqueDomains) await defaultPageDataResourceCache.invalidate(domain)
    return
  }
  const storage = createPageDataCacheStorage()
  const tabCoordinator = getDefaultPageDataTabCoordinator()
  for (const domain of uniqueDomains) {
    await storage.removeDomain(domain)
    tabCoordinator.notifyDomainInvalidated(domain)
  }
}
