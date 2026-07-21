import type {
  PageDataChangeProjection,
  PageDataConfirmDomainResult,
  PageDataConfirmRequest,
  PageDataConfirmResult,
  PageDataDomain,
  PageDataRevisionToken,
  PageDataViewScope
} from '@/api/domains/pageData'
import type { PageDataActivationHandle, PageDataActivationParticipant } from './pageDataActivationCoordinator'

const PAGE_DATA_CACHE_DATABASE = 'juhe-ai-page-data-cache-v2'
const PAGE_DATA_CACHE_STORE = 'snapshots'
const PAGE_DATA_BROADCAST_CHANNEL = 'juhe-ai-page-data-cache-v2'
const DEFAULT_CONFIRM_INTERVAL_MS = 30_000
const DEFAULT_PEER_TTL_MS = 15_000
const DEFAULT_HEARTBEAT_INTERVAL_MS = 5_000
const DEFAULT_CONFIRM_HANDOFF_TIMEOUT_MS = 250
const DEFAULT_CACHE_TTL_MS = 7 * 24 * 60 * 60_000
const DEFAULT_CACHE_MAX_ENTRIES = 256

export interface PageDataCacheKeyInput {
  scope: string
  route: string
  query: unknown
  version: string | number
  domain?: PageDataDomain
}

export interface PageDataCacheRecord<T> {
  key: string
  domain: PageDataDomain
  scope: string
  route: string
  value: T
  token?: PageDataRevisionToken
  confirmedAt?: string
  writtenAt: string
  expiresAt: string
  lastAccessedAt: string
}

export interface PageDataCacheStorage {
  read<T>(key: string): Promise<PageDataCacheRecord<T> | undefined>
  writeIfCurrent<T>(record: PageDataCacheRecord<T>): Promise<boolean>
  touch(key: string, token: PageDataRevisionToken, confirmedAt: string): Promise<boolean>
  remove(key: string): Promise<void>
  removeDomain(domain: PageDataDomain, scope?: string, route?: string): Promise<void>
}

export interface PageDataTabCoordinator {
  isLeader(): boolean
  requestConfirm(key: string): Promise<boolean>
  notifyUpdated(key: string): void
  notifyInvalidated(key: string): void
  notifyDomainInvalidated(domain: PageDataDomain, scope?: string, route?: string): void
  onConfirmRequested(listener: (key: string) => boolean | void | Promise<boolean | void>): () => void
  onCacheUpdated(listener: (key: string) => void): () => void
  onCacheInvalidated(listener: (key: string) => void): () => void
  onDomainInvalidated(listener: (invalidation: PageDataDomainInvalidation) => void): () => void
}

export interface PageDataDomainInvalidation {
  domain: PageDataDomain
  scope?: string
  route?: string
}

export interface PageDataCacheControllerOptions<T> {
  cacheKey: PageDataCacheKeyInput
  domain: PageDataDomain
  viewScope: PageDataViewScope
  targetSystemAccountId?: string
  storage?: PageDataCacheStorage
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  confirmBatchKey?: string
  loadNetwork: () => Promise<T>
  applyDelta?: (current: T, changes: PageDataChangeProjection[]) => T | undefined
  tabCoordinator?: PageDataTabCoordinator
  now?: () => Date
  maxStabilityAttempts?: number
  cacheTtlMs?: number
  maxStaleMs?: number
  activation?: PageDataActivationHandle
  writeEpoch?: (domain: PageDataDomain) => number
}

export type PageDataRequestCacheDefinition<T> = Omit<PageDataCacheControllerOptions<T>, 'storage' | 'confirm' | 'tabCoordinator' | 'now' | 'activation' | 'writeEpoch'>

export interface PageDataRequestCacheManagerOptions {
  storage?: PageDataCacheStorage
  confirm: (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
  confirmBatchKey?: string
  tabCoordinator?: PageDataTabCoordinator
  now?: () => Date
  activation?: PageDataActivationHandle
  writeEpoch?: (domain: PageDataDomain) => number
}

export interface PageDataLoadResult<T> {
  source: 'cache' | 'network'
  data: T
  confirmed: boolean
  cached: boolean
  superseded: boolean
  confirmation?: Promise<PageDataConfirmOutcome<T>>
}

export type PageDataConfirmOutcome<T> =
  | { state: 'unchanged'; data?: T }
  | { state: 'updated'; data: T }
  | { state: 'unavailable'; data?: T }
  | { state: 'follower'; data?: T }
  | { state: 'superseded'; data?: T }

type ConfirmedPageDataDomain = PageDataConfirmDomainResult & { serverTime: string }
type PageDataConfirm = (request: PageDataConfirmRequest) => Promise<PageDataConfirmResult>
type PendingConfirm = {
  resolve: (result: PageDataConfirmResult) => void
  reject: (error: unknown) => void
}
type PendingConfirmBatch = {
  domains: PageDataConfirmRequest['domains']
  pending: PendingConfirm[]
  started: boolean
}

const pendingConfirmBatchesByFunction = new WeakMap<PageDataConfirm, Map<string, PendingConfirmBatch[]>>()
const pendingConfirmBatchesByKey = new Map<string, Map<string, PendingConfirmBatch[]>>()

export function createPageDataCacheKey(input: PageDataCacheKeyInput): string {
  const scope = requiredKeyPart(input.scope, 'scope')
  const route = requiredKeyPart(input.route, 'route')
  const version = requiredKeyPart(String(input.version), 'version')
  const domain = requiredKeyPart(input.domain ?? 'shared', 'domain')
  return `page-data:${encodeURIComponent(version)}:${encodeURIComponent(domain)}:${encodeURIComponent(scope)}:${encodeURIComponent(route)}:${canonicalValue(input.query)}`
}

export function createPageDataCacheStorage(options: { indexedDB?: IDBFactory; ttlMs?: number; maxEntries?: number; now?: () => Date } = {}): PageDataCacheStorage {
  const now = options.now ?? (() => new Date())
  const maxEntries = boundedMaxEntries(options.maxEntries)
  const memory = createMemoryPageDataCacheStorage({ maxEntries, now })
  const factory = 'indexedDB' in options
    ? options.indexedDB
    : typeof indexedDB === 'undefined' ? undefined : indexedDB
  if (!factory) return memory
  return createResilientStorage(createIndexedDbPageDataCacheStorage(factory, { maxEntries, now }), memory)
}

export function createMemoryPageDataCacheStorage(options: { maxEntries?: number; now?: () => Date } = {}): PageDataCacheStorage {
  const records = new Map<string, PageDataCacheRecord<unknown>>()
  const now = options.now ?? (() => new Date())
  const maxEntries = boundedMaxEntries(options.maxEntries)
  return {
    async read<T>(key: string) {
      const record = records.get(key)
      if (!record) return undefined
      const accessedAt = now().toISOString()
      const expiresAtMs = Date.parse(record.expiresAt)
      if (!Number.isFinite(expiresAtMs) || expiresAtMs <= Date.parse(accessedAt)) {
        records.delete(key)
        return undefined
      }
      const accessed = { ...record, lastAccessedAt: accessedAt }
      records.set(key, accessed)
      return cloneRecord(accessed) as PageDataCacheRecord<T>
    },
    async writeIfCurrent<T>(record: PageDataCacheRecord<T>) {
      const current = records.get(record.key)
      if (!canReplacePageDataCacheRecord(record, current)) return false
      records.set(record.key, cloneRecord(record) as PageDataCacheRecord<unknown>)
      pruneMemoryRecords(records, maxEntries, now())
      return true
    },
    async touch(key, token, confirmedAt) {
      const current = records.get(key)
      if (!current || !sameRevisionToken(current.token, token)) return false
      records.set(key, { ...current, token: { ...token }, confirmedAt })
      return true
    },
    async remove(key) {
      records.delete(key)
    },
    async removeDomain(domain, scope, route) {
      for (const [key, record] of records) {
        if (
          record.domain === domain
          && (!scope || record.scope === scope)
          && (!route || record.route === route)
        ) records.delete(key)
      }
    }
  }
}

export function createPageDataActivationController(options: {
  start: () => void
  stop: () => void
  onActivate?: () => void
}): { mount: () => void; activate: () => void; deactivate: () => void; dispose: () => void } {
  let active = false
  let disposed = false
  const start = (notifyActivation: boolean) => {
    if (active || disposed) return
    active = true
    options.start()
    if (notifyActivation) options.onActivate?.()
  }
  const deactivate = () => {
    if (!active) return
    active = false
    options.stop()
  }
  return {
    mount: () => start(false),
    activate: () => start(true),
    deactivate,
    dispose() {
      if (disposed) return
      deactivate()
      disposed = true
    }
  }
}

export class PageDataCacheController<T> {
  readonly key: string
  private readonly options: PageDataCacheControllerOptions<T>
  private readonly storage: PageDataCacheStorage
  private readonly now: () => Date
  private readonly listeners = new Set<(record: PageDataCacheRecord<T> | undefined) => void>()
  private readonly removeTabListeners: Array<() => void> = []
  private generation = 0
  private confirmInFlight?: Promise<PageDataConfirmOutcome<T>>
  private refreshInFlight?: Promise<PageDataLoadResult<T>>
  private closed = false

  constructor(options: PageDataCacheControllerOptions<T>) {
    this.options = options
    this.key = createPageDataCacheKey({ ...options.cacheKey, domain: options.domain })
    this.storage = options.storage ?? createPageDataCacheStorage()
    this.now = options.now ?? (() => new Date())
    if (options.tabCoordinator) {
      this.removeTabListeners.push(options.tabCoordinator.onConfirmRequested(async (key) => {
        if (key !== this.key || !options.tabCoordinator?.isLeader()) return false
        await this.confirmNow()
        return true
      }))
      this.removeTabListeners.push(options.tabCoordinator.onCacheUpdated((key) => {
        if (key !== this.key) return
        void this.storage.read<T>(this.key).then((record) => {
          if (record && !this.closed) this.emit(record)
        })
      }))
      this.removeTabListeners.push(options.tabCoordinator.onCacheInvalidated((key) => {
        if (key !== this.key) return
        void this.storage.remove(this.key).then(() => {
          if (!this.closed) this.emit(undefined)
        })
      }))
    }
  }

  async load(): Promise<PageDataLoadResult<T>> {
    const cached = await this.storage.read<T>(this.key)
    if (
      cached
      && this.isUsableCachedRecord(cached)
      && (!this.options.activation || this.isActivationHotCachedRecord(cached))
    ) {
      return {
        source: 'cache',
        data: cached.value,
        confirmed: false,
        cached: true,
        superseded: false,
        ...(this.options.activation ? {} : { confirmation: this.requestConfirm() })
      }
    }
    return this.refreshCurrent(false)
  }

  refresh(): Promise<PageDataLoadResult<T>> {
    const operation = (this.confirmInFlight
      ? this.refreshAfterPendingConfirm(this.confirmInFlight)
      : this.refreshCurrent(true)).finally(() => {
      if (this.refreshInFlight === operation) this.refreshInFlight = undefined
    })
    this.refreshInFlight = operation
    return operation
  }

  private async refreshAfterPendingConfirm(
    pendingConfirm: Promise<PageDataConfirmOutcome<T>>
  ): Promise<PageDataLoadResult<T>> {
    const outcome = await pendingConfirm.catch((): PageDataConfirmOutcome<T> => ({ state: 'unavailable' }))
    if (this.closed) return { source: 'cache', data: outcome.data as T, confirmed: false, cached: false, superseded: true }
    if (outcome.state === 'updated' && outcome.data !== undefined) {
      return { source: 'cache', data: outcome.data, confirmed: true, cached: true, superseded: false }
    }
    const generation = ++this.generation
    const operationWriteEpoch = this.currentWriteEpoch()
    const data = await this.options.loadNetwork()
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
    if (outcome.state === 'unchanged') {
      const current = await this.storage.read<T>(this.key)
      if (current?.token && this.isOperationCurrent(generation, operationWriteEpoch)) {
        const record = this.record(data, current.token, current.confirmedAt)
        if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
        const written = await this.storage.writeIfCurrent(record)
        if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
        if (written) this.publish(record)
        return { source: 'network', data, confirmed: written, cached: written, superseded: false }
      }
    }
    return { source: 'network', data, confirmed: false, cached: false, superseded: false }
  }

  private async refreshCurrent(forceNetwork: boolean): Promise<PageDataLoadResult<T>> {
    const generation = ++this.generation
    const operationWriteEpoch = this.currentWriteEpoch()
    const cached = await this.storage.read<T>(this.key)
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) {
      if (cached) return await this.supersededLoadResult(cached.value)
      const data = await this.options.loadNetwork()
      return await this.supersededLoadResult(data)
    }
    if (forceNetwork && cached?.token) return this.refreshFromToken(generation, operationWriteEpoch, cached)
    let baseline: ConfirmedPageDataDomain | undefined
    try {
      baseline = await this.confirmDomain(cached?.token, generation, operationWriteEpoch)
    } catch {
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) {
        if (cached) return await this.supersededLoadResult(cached.value)
        const data = await this.options.loadNetwork()
        return await this.supersededLoadResult(data)
      }
      if (cached && !this.isUsableCachedRecord(cached)) await this.invalidateCache()
      const data = await this.options.loadNetwork()
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) {
        return await this.supersededLoadResult(data)
      }
      if (this.options.activation) {
        return { source: 'network', data, confirmed: false, cached: false, superseded: false }
      }
      const record = this.record(data)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
      const written = await this.storage.writeIfCurrent(record)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
      if (written) this.publish(record)
      return { source: 'network', data, confirmed: false, cached: written, superseded: false }
    }
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) {
      if (cached) return await this.supersededLoadResult(cached.value)
      const data = await this.options.loadNetwork()
      return await this.supersededLoadResult(data)
    }
    if (cached && baseline.action === 'unchanged' && sameRevisionToken(baseline.token, cached.token)) {
      const touched = this.isOperationCurrent(generation, operationWriteEpoch)
        && await this.storage.touch(this.key, baseline.token, baseline.serverTime)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(cached.value)
      if (touched) {
        return {
          source: 'cache',
          data: cached.value,
          confirmed: true,
          cached: true,
          superseded: false
        }
      }
    }
    const stable = await this.reloadStable(generation, operationWriteEpoch, baseline)
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(stable.data)
    return {
      source: 'network',
      data: stable.data,
      confirmed: stable.confirmed,
      cached: stable.cached,
      superseded: false
    }
  }

  async requestConfirm(): Promise<PageDataConfirmOutcome<T>> {
    if (this.closed) return Promise.resolve({ state: 'superseded' })
    if (this.refreshInFlight) return this.confirmNow()
    const tab = this.options.tabCoordinator
    if (tab && !tab.isLeader()) {
      const handled = await tab.requestConfirm(this.key)
      if (!handled) return this.confirmNow()
      const record = await this.storage.read<T>(this.key)
      if (record && this.isUsableCachedRecord(record)) return { state: 'follower', data: record.value }
      if (record) return this.confirmNow()
      return { state: 'unavailable' }
    }
    return this.confirmNow()
  }

  confirmNow(): Promise<PageDataConfirmOutcome<T>> {
    if (this.refreshInFlight) {
      return this.refreshInFlight.then(
        (result) => result.superseded
          ? { state: 'superseded', data: result.data }
          : { state: 'updated', data: result.data },
        () => ({ state: 'unavailable' })
      )
    }
    if (this.confirmInFlight) return this.confirmInFlight
    const operation = this.confirmCurrent().finally(() => {
      if (this.confirmInFlight === operation) this.confirmInFlight = undefined
    })
    this.confirmInFlight = operation
    return operation
  }

  subscribe(listener: (record: PageDataCacheRecord<T> | undefined) => void): () => void {
    if (!this.closed) this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    this.generation += 1
    this.listeners.clear()
    for (const remove of this.removeTabListeners.splice(0)) remove()
  }

  private async confirmCurrent(): Promise<PageDataConfirmOutcome<T>> {
    const generation = ++this.generation
    const operationWriteEpoch = this.currentWriteEpoch()
    const current = await this.storage.read<T>(this.key)
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: current?.value }
    let result: ConfirmedPageDataDomain
    try {
      result = await this.confirmDomain(current?.token, generation, operationWriteEpoch)
    } catch {
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: current?.value }
      if (current && !this.isUsableCachedRecord(current)) {
        await this.invalidateCache()
        try {
          const data = await this.options.loadNetwork()
          if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data }
          if (this.options.activation) return { state: 'unavailable', data }
          const record = this.record(data)
          if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data }
          const written = await this.storage.writeIfCurrent(record)
          if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data }
          if (written) this.publish(record)
          return { state: 'updated', data }
        } catch {
          return { state: 'unavailable' }
        }
      }
      return { state: 'unavailable', data: current?.value }
    }
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: current?.value }
    if (result.action === 'unchanged' && current && sameRevisionToken(result.token, current.token)) {
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: current.value }
      const touched = await this.storage.touch(this.key, result.token, result.serverTime)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: current.value }
      return touched ? { state: 'unchanged', data: current.value } : { state: 'superseded', data: current.value }
    }
    if (result.action === 'delta' && current && this.options.applyDelta) {
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: current.value }
      const next = this.options.applyDelta(current.value, result.changes ?? [])
      if (next !== undefined) {
        const record = this.record(next, result.token, result.serverTime)
        const written = this.isOperationCurrent(generation, operationWriteEpoch)
          && await this.storage.writeIfCurrent(record)
        if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: current.value }
        if (written) this.publish(record)
        return written ? { state: 'updated', data: next } : { state: 'superseded', data: current.value }
      }
    }
    if (result.action === 'reset' && this.isOperationCurrent(generation, operationWriteEpoch)) await this.invalidateCache()
    const stable = await this.reloadStable(generation, operationWriteEpoch, result)
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { state: 'superseded', data: stable.data }
    return this.options.activation && !stable.confirmed
      ? { state: 'unavailable', data: stable.data }
      : { state: 'updated', data: stable.data }
  }

  private async refreshFromToken(
    generation: number,
    operationWriteEpoch: number,
    cached: PageDataCacheRecord<T>
  ): Promise<PageDataLoadResult<T>> {
    const data = await this.options.loadNetwork()
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
    let after: ConfirmedPageDataDomain
    try {
      after = this.options.activation
        ? await this.stabilizeDomain(cached.token!, generation, operationWriteEpoch)
        : await this.confirmDomain(cached.token, generation, operationWriteEpoch)
    } catch {
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
      return { source: 'network', data, confirmed: false, cached: false, superseded: false }
    }
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
    if (after.action === 'unchanged' && sameRevisionToken(after.token, cached.token)) {
      const record = this.record(data, after.token, after.serverTime)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
      const written = await this.storage.writeIfCurrent(record)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(data)
      if (written) this.publish(record)
      return { source: 'network', data, confirmed: written, cached: written, superseded: false }
    }
    if (this.options.activation) return { source: 'network', data, confirmed: false, cached: false, superseded: false }
    if (after.action === 'reset' && this.isOperationCurrent(generation, operationWriteEpoch)) await this.invalidateCache()
    const stable = await this.reloadStable(generation, operationWriteEpoch, after)
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return await this.supersededLoadResult(stable.data)
    return { source: 'network', data: stable.data, confirmed: stable.confirmed, cached: stable.cached, superseded: false }
  }

  private async reloadStable(
    generation: number,
    operationWriteEpoch: number,
    initial: ConfirmedPageDataDomain
  ): Promise<{ data: T; confirmed: boolean; cached: boolean }> {
    let baseline = initial
    let latest = await this.options.loadNetwork()
    if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { data: latest, confirmed: false, cached: false }
    if (this.options.activation) {
      let after: ConfirmedPageDataDomain
      try {
        after = await this.stabilizeDomain(baseline.token, generation, operationWriteEpoch)
      } catch {
        return { data: latest, confirmed: false, cached: false }
      }
      if (
        !this.isOperationCurrent(generation, operationWriteEpoch)
        || after.action !== 'unchanged'
        || !sameRevisionToken(after.token, baseline.token)
      ) return { data: latest, confirmed: false, cached: false }
      const record = this.record(latest, after.token, after.serverTime)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { data: latest, confirmed: false, cached: false }
      const written = await this.storage.writeIfCurrent(record)
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { data: latest, confirmed: false, cached: false }
      if (written) this.publish(record)
      return { data: latest, confirmed: written, cached: written }
    }
    const attempts = Math.max(1, Math.min(Math.trunc(this.options.maxStabilityAttempts ?? 3), 5))
    for (let attempt = 0; attempt < attempts; attempt += 1) {
      if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { data: latest, confirmed: false, cached: false }
      let after: ConfirmedPageDataDomain
      try {
        after = await this.confirmDomain(baseline.token, generation, operationWriteEpoch)
      } catch {
        return { data: latest, confirmed: false, cached: false }
      }
      if (after.action === 'unchanged' && sameRevisionToken(after.token, baseline.token)) {
        const record = this.record(latest, after.token, after.serverTime)
        const written = this.isOperationCurrent(generation, operationWriteEpoch)
          && await this.storage.writeIfCurrent(record)
        if (!this.isOperationCurrent(generation, operationWriteEpoch)) return { data: latest, confirmed: false, cached: false }
        if (written) this.publish(record)
        return { data: latest, confirmed: written, cached: written }
      }
      baseline = after
      if (attempt + 1 < attempts) latest = await this.options.loadNetwork()
    }
    return { data: latest, confirmed: false, cached: false }
  }

  private async confirmDomain(
    token: PageDataRevisionToken | undefined,
    generation: number,
    operationWriteEpoch: number
  ): Promise<ConfirmedPageDataDomain> {
    if (this.options.activation) {
      const decision = await this.options.activation.register(this.participant(generation, operationWriteEpoch, token))
      if (decision.state !== 'confirmed' || decision.phase !== 'pre') throw new Error('页面级前置确认不可用')
      return this.activationResult(decision.result)
    }
    const result = await requestBatchedConfirm(this.options.confirm, this.options.confirmBatchKey, {
      viewScope: this.options.viewScope,
      ...(this.options.targetSystemAccountId ? { targetSystemAccountId: this.options.targetSystemAccountId } : {}),
      domains: { [this.options.domain]: token ?? null }
    })
    const domain = result.domains[this.options.domain]
    if (!domain || domain.token.domain !== this.options.domain) throw new Error('页面数据确认响应缺少目标数据域')
    return { ...domain, serverTime: result.serverTime }
  }

  private async stabilizeDomain(
    baseline: PageDataRevisionToken,
    generation: number,
    operationWriteEpoch: number
  ): Promise<ConfirmedPageDataDomain> {
    const activation = this.options.activation
    if (!activation) return this.confirmDomain(baseline, generation, operationWriteEpoch)
    const decision = await activation.stabilize({
      ...this.participant(generation, operationWriteEpoch, baseline),
      baseline: { ...baseline }
    })
    if (decision.state !== 'confirmed' || decision.phase !== 'post') throw new Error('页面级后置确认不可用')
    return this.activationResult(decision.result)
  }

  private activationResult(result: ConfirmedPageDataDomain): ConfirmedPageDataDomain {
    if (result.token.domain !== this.options.domain) throw new Error('页面级确认响应缺少目标数据域')
    return {
      ...result,
      token: { ...result.token },
      ...(result.changes ? { changes: result.changes.map((change) => ({ ...change, fieldMask: [...change.fieldMask] })) } : {})
    }
  }

  private participant(
    generation: number,
    operationWriteEpoch: number,
    token?: PageDataRevisionToken
  ): PageDataActivationParticipant {
    return {
      resourceKey: this.key,
      domain: this.options.domain,
      ...(token ? { token: { ...token } } : {}),
      generation,
      writeEpoch: operationWriteEpoch
    }
  }

  private currentWriteEpoch(): number {
    return this.options.writeEpoch?.(this.options.domain) ?? 0
  }

  private isOperationCurrent(generation: number, operationWriteEpoch: number): boolean {
    return !this.closed
      && generation === this.generation
      && operationWriteEpoch === this.currentWriteEpoch()
  }

  private record(value: T, token?: PageDataRevisionToken, confirmedAt?: string): PageDataCacheRecord<T> {
    const nowDate = this.now()
    const now = nowDate.toISOString()
    const ttlMs = Math.max(60_000, Math.min(Math.trunc(this.options.cacheTtlMs ?? DEFAULT_CACHE_TTL_MS), DEFAULT_CACHE_TTL_MS))
    return {
      key: this.key,
      domain: this.options.domain,
      scope: this.options.cacheKey.scope,
      route: this.options.cacheKey.route,
      value,
      ...(token ? { token: { ...token }, confirmedAt: confirmedAt ?? now } : {}),
      writtenAt: now,
      lastAccessedAt: now,
      expiresAt: new Date(nowDate.getTime() + ttlMs).toISOString()
    }
  }

  private isUsableCachedRecord(record: PageDataCacheRecord<T>): boolean {
    if (this.options.maxStaleMs === undefined) return true
    const maxStaleMs = Math.max(1_000, Math.trunc(this.options.maxStaleMs))
    const confirmedAtMs = Date.parse(record.confirmedAt ?? record.writtenAt)
    return Number.isFinite(confirmedAtMs) && this.now().getTime() - confirmedAtMs <= maxStaleMs
  }

  private isActivationHotCachedRecord(record: PageDataCacheRecord<T>): boolean {
    const confirmedAtMs = Date.parse(record.confirmedAt ?? record.writtenAt)
    return Number.isFinite(confirmedAtMs)
      && this.now().getTime() - confirmedAtMs <= DEFAULT_CONFIRM_INTERVAL_MS
  }

  private async supersededLoadResult(fallback: T): Promise<PageDataLoadResult<T>> {
    const current = await this.storage.read<T>(this.key)
    return current
      ? { source: 'cache', data: current.value, confirmed: Boolean(current.token), cached: true, superseded: true }
      : { source: 'network', data: fallback, confirmed: false, cached: false, superseded: true }
  }

  private publish(record: PageDataCacheRecord<T>): void {
    this.emit(record)
    this.options.tabCoordinator?.notifyUpdated(this.key)
  }

  private async invalidateCache(): Promise<void> {
    await this.storage.remove(this.key)
    if (this.closed) return
    this.options.tabCoordinator?.notifyInvalidated(this.key)
  }

  private emit(record: PageDataCacheRecord<T> | undefined): void {
    for (const listener of this.listeners) {
      try { listener(record ? cloneRecord(record) as PageDataCacheRecord<T> : undefined) } catch { /* listeners are isolated */ }
    }
  }
}

function requestBatchedConfirm(confirm: PageDataConfirm, batchKey: string | undefined, request: PageDataConfirmRequest): Promise<PageDataConfirmResult> {
  const contextKey = `${request.viewScope}:${request.targetSystemAccountId ?? ''}`
  const contexts = confirmBatchContexts(confirm, batchKey)
  let batches = contexts.get(contextKey)
  if (!batches) {
    batches = []
    contexts.set(contextKey, batches)
  }
  let batch = batches.find((candidate) => confirmDomainsCompatible(candidate, request.domains))
  let created = false
  if (!batch) {
    batch = { domains: {}, pending: [], started: false }
    batches.push(batch)
    created = true
  }
  Object.assign(batch.domains, request.domains)
  const promise = new Promise<PageDataConfirmResult>((resolve, reject) => {
    batch!.pending.push({ resolve, reject })
  })
  if (created) {
    queueMicrotask(() => flushConfirmBatches(confirm, batchKey, contextKey, request))
  }
  return promise
}

function confirmDomainsCompatible(
  batch: PendingConfirmBatch,
  right: PageDataConfirmRequest['domains']
): boolean {
  return Object.entries(right).every(([domain, token]) => {
    if (!(domain in batch.domains)) return !batch.started
    const existing = batch.domains[domain as PageDataDomain]
    return existing === null ? token === null : token !== null && sameRevisionToken(existing, token)
  })
}

function flushConfirmBatches(confirm: PageDataConfirm, batchKey: string | undefined, contextKey: string, request: PageDataConfirmRequest): void {
  const batches = confirmBatchContexts(confirm, batchKey).get(contextKey)
  if (!batches) return
  for (const batch of batches) {
    if (batch.started) continue
    batch.started = true
    void Promise.resolve().then(() => confirm({
      viewScope: request.viewScope,
      ...(request.targetSystemAccountId ? { targetSystemAccountId: request.targetSystemAccountId } : {}),
      domains: batch.domains
    })).then(
      (result) => settleConfirmBatch(confirm, batchKey, contextKey, batch, (pending) => pending.resolve(result)),
      (error) => settleConfirmBatch(confirm, batchKey, contextKey, batch, (pending) => pending.reject(error))
    )
  }
}

function settleConfirmBatch(
  confirm: PageDataConfirm,
  batchKey: string | undefined,
  contextKey: string,
  batch: PendingConfirmBatch,
  settle: (pending: PendingConfirm) => void
): void {
  const contexts = confirmBatchContexts(confirm, batchKey)
  const batches = contexts.get(contextKey)
  if (batches) {
    const index = batches.indexOf(batch)
    if (index >= 0) batches.splice(index, 1)
    if (batches.length === 0) contexts.delete(contextKey)
  }
  batch.pending.forEach(settle)
}

function confirmBatchContexts(confirm: PageDataConfirm, batchKey?: string): Map<string, PendingConfirmBatch[]> {
  if (batchKey) {
    let contexts = pendingConfirmBatchesByKey.get(batchKey)
    if (!contexts) {
      contexts = new Map()
      pendingConfirmBatchesByKey.set(batchKey, contexts)
    }
    return contexts
  }
  let contexts = pendingConfirmBatchesByFunction.get(confirm)
  if (!contexts) {
    contexts = new Map()
    pendingConfirmBatchesByFunction.set(confirm, contexts)
  }
  return contexts
}

export class PageDataRequestCacheManager<T> {
  private readonly options: PageDataRequestCacheManagerOptions
  private readonly storage: PageDataCacheStorage
  private readonly listeners = new Set<(record: PageDataCacheRecord<T> | undefined) => void>()
  private active?: { key: string; controller: PageDataCacheController<T>; removeSubscription: () => void }
  private activationGeneration = 0
  private closed = false

  constructor(options: PageDataRequestCacheManagerOptions) {
    this.options = options
    this.storage = options.storage ?? createPageDataCacheStorage()
  }

  get currentKey(): string | undefined {
    return this.active?.key
  }

  async load(request: PageDataRequestCacheDefinition<T>): Promise<PageDataLoadResult<T>> {
    const active = this.activate(request)
    const generation = this.activationGeneration
    const result = await active.controller.load()
    return generation === this.activationGeneration && active === this.active
      ? result
      : { ...result, superseded: true }
  }

  async forceRefresh(request: PageDataRequestCacheDefinition<T>): Promise<PageDataLoadResult<T>> {
    const active = this.activate(request)
    const generation = this.activationGeneration
    const result = await active.controller.refresh()
    return generation === this.activationGeneration && active === this.active
      ? result
      : { ...result, superseded: true }
  }

  confirmCurrent(): Promise<PageDataConfirmOutcome<T>> {
    return this.active?.controller.requestConfirm() ?? Promise.resolve({ state: 'superseded' })
  }

  subscribe(listener: (record: PageDataCacheRecord<T> | undefined) => void): () => void {
    if (!this.closed) this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  close(): void {
    if (this.closed) return
    this.closed = true
    this.activationGeneration += 1
    this.disposeActive()
    this.listeners.clear()
  }

  private activate(request: PageDataRequestCacheDefinition<T>): NonNullable<PageDataRequestCacheManager<T>['active']> {
    if (this.closed) throw new Error('页面请求缓存 manager 已关闭')
    const key = createPageDataCacheKey({ ...request.cacheKey, domain: request.domain })
    if (this.active?.key === key) return this.active
    this.activationGeneration += 1
    this.disposeActive()
    const controller = new PageDataCacheController<T>({
      ...request,
      storage: this.storage,
      confirm: this.options.confirm,
      confirmBatchKey: this.options.confirmBatchKey,
      tabCoordinator: this.options.tabCoordinator,
      now: this.options.now,
      activation: this.options.activation,
      writeEpoch: this.options.writeEpoch
    })
    const active = {
      key,
      controller,
      removeSubscription: controller.subscribe((record) => {
        if (this.active?.controller !== controller || this.closed) return
        for (const listener of this.listeners) {
          try { listener(record) } catch { /* listeners are isolated */ }
        }
      })
    }
    this.active = active
    return active
  }

  private disposeActive(): void {
    const active = this.active
    this.active = undefined
    if (!active) return
    active.removeSubscription()
    active.controller.close()
  }
}

export class PageDataVisibleConfirmScheduler {
  private readonly confirm: () => void
  private readonly isVisible: () => boolean
  private readonly intervalMs: number
  private readonly addFocusListener: (listener: () => void) => () => void
  private timer?: ReturnType<typeof setInterval>
  private removeFocus?: () => void

  constructor(options: {
    confirm: () => void
    intervalMs?: number
    isVisible?: () => boolean
    addFocusListener?: (listener: () => void) => () => void
  }) {
    this.confirm = options.confirm
    this.intervalMs = Math.max(1_000, Math.trunc(options.intervalMs ?? DEFAULT_CONFIRM_INTERVAL_MS))
    this.isVisible = options.isVisible ?? (() => typeof document === 'undefined' || document.visibilityState === 'visible')
    this.addFocusListener = options.addFocusListener ?? ((listener) => {
      if (typeof window === 'undefined') return () => undefined
      window.addEventListener('focus', listener)
      return () => window.removeEventListener('focus', listener)
    })
  }

  start(): void {
    if (this.timer) return
    this.timer = setInterval(() => this.tick(), this.intervalMs)
    this.removeFocus = this.addFocusListener(() => this.tick())
  }

  tick(): void {
    if (this.isVisible()) this.confirm()
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer)
    this.timer = undefined
    this.removeFocus?.()
    this.removeFocus = undefined
  }
}

type PageDataChannelLike = Pick<BroadcastChannel, 'postMessage' | 'close' | 'onmessage'>
type PageDataChannelFactory = (name: string) => PageDataChannelLike
type PageDataBroadcastMessage = {
  protocolVersion: 1
  sender: string
  sentAt: number
  type: 'hello' | 'heartbeat' | 'bye' | 'confirm-request' | 'confirm-response' | 'cache-updated' | 'cache-invalidated' | 'domain-invalidated'
  key?: string
  domain?: PageDataDomain
  scope?: string
  route?: string
  requestId?: string
  handled?: boolean
}

export class BrowserPageDataTabCoordinator implements PageDataTabCoordinator {
  private readonly id: string
  private readonly now: () => number
  private readonly peerTtlMs: number
  private readonly channel?: PageDataChannelLike
  private readonly peers = new Map<string, number>()
  private readonly confirmListeners = new Set<(key: string) => boolean | void | Promise<boolean | void>>()
  private readonly updateListeners = new Set<(key: string) => void>()
  private readonly invalidationListeners = new Set<(key: string) => void>()
  private readonly domainInvalidationListeners = new Set<(invalidation: PageDataDomainInvalidation) => void>()
  private readonly pendingConfirmRequests = new Map<string, { resolve: (handled: boolean) => void; timer: ReturnType<typeof setTimeout> }>()
  private heartbeat?: ReturnType<typeof setInterval>
  private closed = false

  constructor(options: {
    tabId?: string
    channelName?: string
    channelFactory?: PageDataChannelFactory
    now?: () => number
    heartbeatIntervalMs?: number
    peerTtlMs?: number
  } = {}) {
    this.id = options.tabId?.trim() || randomTabId()
    this.now = options.now ?? (() => Date.now())
    this.peerTtlMs = Math.max(1_000, Math.trunc(options.peerTtlMs ?? DEFAULT_PEER_TTL_MS))
    const factory = 'channelFactory' in options
      ? options.channelFactory
      : typeof window !== 'undefined' && typeof BroadcastChannel === 'function'
        ? (name: string) => new BroadcastChannel(name)
        : undefined
    try {
      this.channel = factory?.(options.channelName ?? PAGE_DATA_BROADCAST_CHANNEL)
      if (this.channel) {
        ;(this.channel as PageDataChannelLike & { unref?: () => void }).unref?.()
        this.channel.onmessage = (event) => this.receive(event.data)
        this.send('hello')
        const heartbeat = setInterval(() => this.send('heartbeat'), Math.max(1_000, Math.trunc(options.heartbeatIntervalMs ?? DEFAULT_HEARTBEAT_INTERVAL_MS)))
        ;(heartbeat as { unref?: () => void }).unref?.()
        this.heartbeat = heartbeat
      }
    } catch {
      this.channel = undefined
    }
  }

  isLeader(): boolean {
    if (!this.channel || this.closed) return !this.closed
    this.prunePeers()
    return [this.id, ...this.peers.keys()].sort()[0] === this.id
  }

  requestConfirm(key: string): Promise<boolean> {
    if (!this.channel || this.closed) return Promise.resolve(false)
    const requestId = randomTabId()
    return new Promise<boolean>((resolve) => {
      const timer = setTimeout(() => this.resolveConfirmRequest(requestId, false), DEFAULT_CONFIRM_HANDOFF_TIMEOUT_MS)
      this.pendingConfirmRequests.set(requestId, { resolve, timer })
      this.send('confirm-request', key, { requestId })
    })
  }

  notifyUpdated(key: string): void {
    this.send('cache-updated', key)
  }

  notifyInvalidated(key: string): void {
    this.send('cache-invalidated', key)
  }

  notifyDomainInvalidated(domain: PageDataDomain, scope?: string, route?: string): void {
    this.send('domain-invalidated', undefined, { domain, scope, route })
  }

  onConfirmRequested(listener: (key: string) => boolean | void | Promise<boolean | void>): () => void {
    if (!this.closed) this.confirmListeners.add(listener)
    return () => this.confirmListeners.delete(listener)
  }

  onCacheUpdated(listener: (key: string) => void): () => void {
    if (!this.closed) this.updateListeners.add(listener)
    return () => this.updateListeners.delete(listener)
  }

  onCacheInvalidated(listener: (key: string) => void): () => void {
    if (!this.closed) this.invalidationListeners.add(listener)
    return () => this.invalidationListeners.delete(listener)
  }

  onDomainInvalidated(listener: (invalidation: PageDataDomainInvalidation) => void): () => void {
    if (!this.closed) this.domainInvalidationListeners.add(listener)
    return () => this.domainInvalidationListeners.delete(listener)
  }

  close(): void {
    if (this.closed) return
    this.send('bye')
    this.closed = true
    if (this.heartbeat) clearInterval(this.heartbeat)
    this.heartbeat = undefined
    this.confirmListeners.clear()
    this.updateListeners.clear()
    this.invalidationListeners.clear()
    this.domainInvalidationListeners.clear()
    for (const requestId of [...this.pendingConfirmRequests.keys()]) this.resolveConfirmRequest(requestId, false)
    if (this.channel) {
      this.channel.onmessage = null
      try { this.channel.close() } catch { /* channel teardown is best effort */ }
    }
  }

  private receive(value: unknown): void {
    if (this.closed || !isPageDataBroadcastMessage(value) || value.sender === this.id) return
    if (value.type === 'bye') {
      this.peers.delete(value.sender)
      return
    }
    this.peers.set(value.sender, value.sentAt)
    if (value.type === 'hello') this.send('heartbeat')
    if (value.type === 'confirm-request' && value.key && value.requestId && this.isLeader()) {
      void this.respondToConfirmRequest(value.key, value.requestId)
    }
    if (value.type === 'confirm-response' && value.requestId) {
      this.resolveConfirmRequest(value.requestId, value.handled === true)
    }
    if (value.type === 'cache-updated' && value.key) {
      for (const listener of this.updateListeners) listener(value.key)
    }
    if (value.type === 'cache-invalidated' && value.key) {
      for (const listener of this.invalidationListeners) listener(value.key)
    }
    if (value.type === 'domain-invalidated' && value.domain) {
      const invalidation: PageDataDomainInvalidation = {
        domain: value.domain,
        ...(value.scope ? { scope: value.scope } : {}),
        ...(value.route ? { route: value.route } : {})
      }
      for (const listener of this.domainInvalidationListeners) listener(invalidation)
    }
  }

  private send(
    type: PageDataBroadcastMessage['type'],
    key?: string,
    details: Pick<PageDataBroadcastMessage, 'requestId' | 'handled' | 'domain' | 'scope' | 'route'> = {}
  ): void {
    if (!this.channel || this.closed) return
    try {
      this.channel.postMessage({
        protocolVersion: 1,
        sender: this.id,
        sentAt: this.now(),
        type,
        ...(key ? { key } : {}),
        ...(details.requestId ? { requestId: details.requestId } : {}),
        ...(details.handled !== undefined ? { handled: details.handled } : {}),
        ...(details.domain ? { domain: details.domain } : {}),
        ...(details.scope ? { scope: details.scope } : {}),
        ...(details.route ? { route: details.route } : {})
      } satisfies PageDataBroadcastMessage)
    } catch { /* BroadcastChannel failure falls back to per-tab confirms */ }
  }

  private async respondToConfirmRequest(key: string, requestId: string): Promise<void> {
    let handled = false
    for (const listener of this.confirmListeners) {
      try {
        if (await listener(key) !== false) handled = true
      } catch { /* a failed owner still handled the request and applied its fallback */ }
    }
    this.send('confirm-response', key, { requestId, handled })
  }

  private resolveConfirmRequest(requestId: string, handled: boolean): void {
    const pending = this.pendingConfirmRequests.get(requestId)
    if (!pending) return
    this.pendingConfirmRequests.delete(requestId)
    clearTimeout(pending.timer)
    pending.resolve(handled)
  }

  private prunePeers(): void {
    const cutoff = this.now() - this.peerTtlMs
    for (const [id, seenAt] of this.peers) {
      if (seenAt < cutoff) this.peers.delete(id)
    }
  }
}

let defaultPageDataTabCoordinator: BrowserPageDataTabCoordinator | undefined

export function getDefaultPageDataTabCoordinator(): PageDataTabCoordinator {
  defaultPageDataTabCoordinator ??= new BrowserPageDataTabCoordinator()
  return defaultPageDataTabCoordinator
}

function createIndexedDbPageDataCacheStorage(factory: IDBFactory, options: { maxEntries: number; now: () => Date }): PageDataCacheStorage {
  let databasePromise: Promise<IDBDatabase> | undefined
  const database = () => {
    databasePromise ??= openPageDataCacheDatabase(factory).catch((error) => {
      databasePromise = undefined
      throw error
    })
    return databasePromise
  }
  return {
    async read<T>(key: string) {
      const db = await database()
      return await new Promise<PageDataCacheRecord<T> | undefined>((resolve, reject) => {
        const transaction = db.transaction(PAGE_DATA_CACHE_STORE, 'readwrite')
        const store = transaction.objectStore(PAGE_DATA_CACHE_STORE)
        let result: PageDataCacheRecord<T> | undefined
        const request = store.get(key)
        request.onerror = () => reject(request.error ?? new Error('读取页面缓存失败'))
        request.onsuccess = () => {
          const current = request.result as PageDataCacheRecord<T> | undefined
          if (!current) return
          const accessedAt = options.now().toISOString()
          const expiresAtMs = Date.parse(current.expiresAt)
          if (!Number.isFinite(expiresAtMs) || expiresAtMs <= Date.parse(accessedAt)) {
            store.delete(key)
            return
          }
          result = { ...current, lastAccessedAt: accessedAt }
          store.put(result)
        }
        transaction.oncomplete = () => resolve(result)
        transaction.onerror = () => reject(transaction.error ?? new Error('读取页面缓存失败'))
        transaction.onabort = () => reject(transaction.error ?? new Error('读取页面缓存已中止'))
      })
    },
    async writeIfCurrent<T>(record: PageDataCacheRecord<T>) {
      const db = await database()
      return await new Promise<boolean>((resolve, reject) => {
        const transaction = db.transaction(PAGE_DATA_CACHE_STORE, 'readwrite')
        const store = transaction.objectStore(PAGE_DATA_CACHE_STORE)
        let written = false
        const request = store.get(record.key)
        request.onerror = () => reject(request.error ?? new Error('读取页面缓存失败'))
        request.onsuccess = () => {
          if (!canReplacePageDataCacheRecord(record, request.result as PageDataCacheRecord<unknown> | undefined)) return
          written = true
          store.put(record)
        }
        transaction.oncomplete = () => {
          resolve(written)
          if (written) void pruneIndexedDbRecords(db, options.maxEntries, options.now()).catch(() => undefined)
        }
        transaction.onerror = () => reject(transaction.error ?? new Error('写入页面缓存失败'))
        transaction.onabort = () => reject(transaction.error ?? new Error('写入页面缓存已中止'))
      })
    },
    async touch(key, token, confirmedAt) {
      const db = await database()
      return await new Promise<boolean>((resolve, reject) => {
        const transaction = db.transaction(PAGE_DATA_CACHE_STORE, 'readwrite')
        const store = transaction.objectStore(PAGE_DATA_CACHE_STORE)
        let touched = false
        const request = store.get(key)
        request.onerror = () => reject(request.error ?? new Error('读取页面缓存失败'))
        request.onsuccess = () => {
          const current = request.result as PageDataCacheRecord<unknown> | undefined
          if (!current || !sameRevisionToken(current.token, token)) return
          touched = true
          store.put({ ...current, token, confirmedAt })
        }
        transaction.oncomplete = () => resolve(touched)
        transaction.onerror = () => reject(transaction.error ?? new Error('更新页面缓存确认时间失败'))
        transaction.onabort = () => reject(transaction.error ?? new Error('更新页面缓存确认时间已中止'))
      })
    },
    async remove(key) {
      const db = await database()
      const transaction = db.transaction(PAGE_DATA_CACHE_STORE, 'readwrite')
      transaction.objectStore(PAGE_DATA_CACHE_STORE).delete(key)
      await idbTransaction(transaction)
    },
    async removeDomain(domain, scope, route) {
      const db = await database()
      const transaction = db.transaction(PAGE_DATA_CACHE_STORE, 'readwrite')
      const store = transaction.objectStore(PAGE_DATA_CACHE_STORE)
      const request = store.getAll()
      request.onsuccess = () => {
        for (const record of request.result as PageDataCacheRecord<unknown>[]) {
          if (
            record.domain === domain
            && (!scope || record.scope === scope)
            && (!route || record.route === route)
          ) store.delete(record.key)
        }
      }
      await idbTransaction(transaction)
    }
  }
}

function createResilientStorage(primary: PageDataCacheStorage, fallback: PageDataCacheStorage): PageDataCacheStorage {
  const use = async <T>(primaryCall: () => Promise<T>, fallbackCall: () => Promise<T>): Promise<T> => {
    try {
      return await primaryCall()
    } catch {
      return fallbackCall()
    }
  }
  const removeBoth = async (primaryCall: () => Promise<void>, fallbackCall: () => Promise<void>): Promise<void> => {
    const [primaryResult, fallbackResult] = await Promise.allSettled([primaryCall(), fallbackCall()])
    if (primaryResult.status === 'rejected' && fallbackResult.status === 'rejected') throw primaryResult.reason
  }
  return {
    read: <T>(key: string) => use(() => primary.read<T>(key), () => fallback.read<T>(key)),
    writeIfCurrent: <T>(record: PageDataCacheRecord<T>) => use(() => primary.writeIfCurrent(record), () => fallback.writeIfCurrent(record)),
    touch: (key, token, confirmedAt) => use(() => primary.touch(key, token, confirmedAt), () => fallback.touch(key, token, confirmedAt)),
    remove: (key) => removeBoth(() => primary.remove(key), () => fallback.remove(key)),
    removeDomain: (domain, scope, route) => removeBoth(
      () => primary.removeDomain(domain, scope, route),
      () => fallback.removeDomain(domain, scope, route)
    )
  }
}

function openPageDataCacheDatabase(factory: IDBFactory): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = factory.open(PAGE_DATA_CACHE_DATABASE, 1)
    request.onupgradeneeded = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(PAGE_DATA_CACHE_STORE)) db.createObjectStore(PAGE_DATA_CACHE_STORE, { keyPath: 'key' })
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('打开页面缓存失败'))
    request.onblocked = () => reject(new Error('页面缓存数据库升级被阻塞'))
  })
}

function idbTransaction(transaction: IDBTransaction): Promise<void> {
  return new Promise((resolve, reject) => {
    transaction.oncomplete = () => resolve()
    transaction.onerror = () => reject(transaction.error ?? new Error('页面缓存事务失败'))
    transaction.onabort = () => reject(transaction.error ?? new Error('页面缓存事务已中止'))
  })
}

function canReplacePageDataCacheRecord(candidate: PageDataCacheRecord<unknown>, current?: PageDataCacheRecord<unknown>): boolean {
  if (!current) return true
  if (candidate.token && !current.token) return true
  if (!candidate.token && current.token) return false
  if (candidate.token && current.token) {
    if (sameRevisionLineage(candidate.token, current.token)) {
      if (candidate.token.sequence !== current.token.sequence) return candidate.token.sequence > current.token.sequence
    } else {
      const candidateConfirmed = Date.parse(candidate.confirmedAt ?? candidate.writtenAt)
      const currentConfirmed = Date.parse(current.confirmedAt ?? current.writtenAt)
      if (candidateConfirmed !== currentConfirmed) return candidateConfirmed > currentConfirmed
    }
  }
  return Date.parse(candidate.writtenAt) >= Date.parse(current.writtenAt)
}

async function pruneIndexedDbRecords(database: IDBDatabase, maxEntries: number, now: Date): Promise<void> {
  const transaction = database.transaction(PAGE_DATA_CACHE_STORE, 'readwrite')
  const store = transaction.objectStore(PAGE_DATA_CACHE_STORE)
  const request = store.getAll()
  request.onsuccess = () => {
    const nowMs = now.getTime()
    const active = (request.result as PageDataCacheRecord<unknown>[]).filter((record) => {
      const expiresAtMs = Date.parse(record.expiresAt)
      if (Number.isFinite(expiresAtMs) && expiresAtMs > nowMs) return true
      store.delete(record.key)
      return false
    })
    active.sort((left, right) => Date.parse(left.lastAccessedAt) - Date.parse(right.lastAccessedAt))
    for (const record of active.slice(0, Math.max(0, active.length - maxEntries))) store.delete(record.key)
  }
  await idbTransaction(transaction)
}

function pruneMemoryRecords(records: Map<string, PageDataCacheRecord<unknown>>, maxEntries: number, now: Date): void {
  const nowMs = now.getTime()
  for (const [key, record] of records) {
    const expiresAtMs = Date.parse(record.expiresAt)
    if (!Number.isFinite(expiresAtMs) || expiresAtMs <= nowMs) records.delete(key)
  }
  if (records.size <= maxEntries) return
  const oldest = [...records.values()].sort((left, right) => Date.parse(left.lastAccessedAt) - Date.parse(right.lastAccessedAt))
  for (const record of oldest.slice(0, records.size - maxEntries)) records.delete(record.key)
}

function boundedMaxEntries(value?: number): number {
  return Math.max(1, Math.min(Math.trunc(value ?? DEFAULT_CACHE_MAX_ENTRIES), 10_000))
}

function sameRevisionLineage(left: PageDataRevisionToken, right: PageDataRevisionToken): boolean {
  return left.protocolVersion === right.protocolVersion
    && left.epoch === right.epoch
    && left.scope === right.scope
    && left.domain === right.domain
    && left.resetSequence === right.resetSequence
}

function sameRevisionToken(left?: PageDataRevisionToken, right?: PageDataRevisionToken): boolean {
  return Boolean(left && right && sameRevisionLineage(left, right) && left.sequence === right.sequence)
}

function canonicalValue(value: unknown): string {
  if (value === null) return 'null'
  if (value === undefined) return 'undefined'
  if (typeof value === 'string' || typeof value === 'boolean') return JSON.stringify(value)
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : JSON.stringify(String(value))
  if (Array.isArray(value)) return `[${value.map(canonicalValue).join(',')}]`
  if (typeof value === 'object') {
    const record = value as Record<string, unknown>
    return `{${Object.keys(record).sort().map((key) => `${JSON.stringify(key)}:${canonicalValue(record[key])}`).join(',')}}`
  }
  return JSON.stringify(String(value))
}

function requiredKeyPart(value: string, label: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`页面缓存 ${label} 不能为空`)
  return normalized
}

function cloneRecord<T>(record?: PageDataCacheRecord<T>): PageDataCacheRecord<T> | undefined {
  if (!record) return undefined
  if (typeof structuredClone === 'function') return structuredClone(record)
  return JSON.parse(JSON.stringify(record)) as PageDataCacheRecord<T>
}

function randomTabId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
}

function isPageDataBroadcastMessage(value: unknown): value is PageDataBroadcastMessage {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const message = value as Partial<PageDataBroadcastMessage>
  return message.protocolVersion === 1
    && typeof message.sender === 'string'
    && Boolean(message.sender)
    && typeof message.sentAt === 'number'
    && Number.isFinite(message.sentAt)
    && ['hello', 'heartbeat', 'bye', 'confirm-request', 'confirm-response', 'cache-updated', 'cache-invalidated', 'domain-invalidated'].includes(message.type ?? '')
    && (message.key === undefined || typeof message.key === 'string')
    && (message.requestId === undefined || typeof message.requestId === 'string')
    && (message.handled === undefined || typeof message.handled === 'boolean')
    && (message.domain === undefined || typeof message.domain === 'string')
    && (message.scope === undefined || typeof message.scope === 'string')
    && (message.route === undefined || typeof message.route === 'string')
}
