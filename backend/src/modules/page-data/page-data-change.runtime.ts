import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { getRedisClient, type RedisCommandClient } from '../../shared/redis-client.js'
import { redisNamespacedKey } from '../../shared/redis-namespace.js'
import {
  createMemoryPageDataChangeStore,
  createRedisPageDataChangeStore,
  pageDataDomains,
  type PageDataChangeEvent,
  type PageDataChangeStore,
  type PageDataDomain,
  type PageDataRedisClient
} from './page-data-change.service.js'
import { invalidatePageDataReadCacheDomain } from './page-data-read-cache.service.js'

let pageDataChangeStore: PageDataChangeStore | undefined
let pageDataPublishRetryQueue: PageDataPublishRetryQueue | undefined
const pendingPageDataDirtyParentAcks = new Map<string, {
  resolve: () => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
}>()
const maxPendingPageDataDirtyParentAcks = 256

export async function initializePageDataChangeRuntime(): Promise<void> {
  if (runtimeConfig.processRole !== 'db-service') return
  const repository = await import('../../storage/page-data-dirty-domain.repository.js')
  const dirtyState = createPageDataDirtyDomainState({
    initialRows: await repository.listPageDataDirtyDomains(),
    persistence: {
      mark: repository.markPageDataDomainDirty,
      clear: repository.clearPageDataDomainDirty
    }
  })
  const store = createRecoveringPageDataChangeStore(createDriverStore(), { dirtyState, autoRecover: true })
  pageDataChangeStore = store
  try {
    await store.recoverDirtyDomains()
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'page_data_dirty_domain_startup_recovery_failed' }), '页面数据 dirty domain 启动恢复失败，已安排后台重试')
  }
}

export function getPageDataChangeStore(): PageDataChangeStore {
  pageDataChangeStore ??= createRecoveringPageDataChangeStore(createDriverStore())
  return pageDataChangeStore
}

export async function publishPageDataChange(event: PageDataChangeEvent): Promise<void> {
  await invalidatePageDataReadCacheDomain(event.domain)
  pageDataPublishRetryQueue ??= createPageDataPublishRetryQueue({
    deliver: dispatchPageDataChangeNow,
    onFailure: (_error, event) => {
      void recordPageDataDirtyDomain(event.domain).catch((error) => {
        logger.warn(errorLogFields(error, { event: 'page_data_dirty_domain_record_failed', domain: event.domain }), '页面数据 dirty domain 记录失败')
      })
    }
  })
  await pageDataPublishRetryQueue.publish(event)
}

async function dispatchPageDataChangeNow(event: PageDataChangeEvent): Promise<void> {
  await dispatchPageDataChangeForProcess(event, {
    runtimeStateDriver: runtimeConfig.runtimeStateDriver,
    processRole: runtimeConfig.processRole,
    publishLocal: async (publishedEvent) => getPageDataChangeStore().publish(publishedEvent),
    sendToParent: sendPageDataChangeToParent,
    sendToDbService: async (publishedEvent) => {
      const { sendPageDataChangeToDbService } = await import('../db-service/db-service-ipc.js')
      await sendPageDataChangeToDbService(publishedEvent)
    }
  })
}

export async function acceptPageDataChangeFromIpc(
  event: PageDataChangeEvent,
  options: {
    invalidateDomain?: (domain: PageDataDomain) => Promise<void>
    publishLocal?: (event: PageDataChangeEvent) => Promise<void>
  } = {}
): Promise<void> {
  await (options.invalidateDomain ?? invalidatePageDataReadCacheDomain)(event.domain)
  await (options.publishLocal ?? ((acceptedEvent) => getPageDataChangeStore().publish(acceptedEvent)))(event)
}

export async function acceptPageDataDirtyDomainsFromIpc(domains: PageDataDomain[]): Promise<void> {
  const store = getPageDataChangeStore()
  if (!('markDirty' in store) || typeof store.markDirty !== 'function') return
  for (const domain of uniquePageDataDomains(domains)) await store.markDirty(domain)
  if ('recoverDirtyDomains' in store && typeof store.recoverDirtyDomains === 'function') {
    await store.recoverDirtyDomains()
  }
}

async function recordPageDataDirtyDomain(domain: PageDataDomain): Promise<void> {
  const store = getPageDataChangeStore()
  if ('markDirty' in store && typeof store.markDirty === 'function') await store.markDirty(domain)
  await dispatchPageDataDirtyDomainsForProcess([domain], {
    processRole: runtimeConfig.processRole,
    recoverLocal: async () => {
      if ('recoverDirtyDomains' in store && typeof store.recoverDirtyDomains === 'function') await store.recoverDirtyDomains()
    },
    sendToParent: sendPageDataDirtyDomainsToParent,
    sendToDbService: async (domains) => {
      const { sendPageDataDirtyDomainsToDbService } = await import('../db-service/db-service-ipc.js')
      await sendPageDataDirtyDomainsToDbService(domains)
    }
  })
}

export async function dispatchPageDataDirtyDomainsForProcess(
  input: PageDataDomain[],
  options: {
    processRole: string
    recoverLocal: () => Promise<void>
    sendToParent: (domains: PageDataDomain[]) => Promise<void>
    sendToDbService: (domains: PageDataDomain[]) => Promise<void>
  }
): Promise<void> {
  const domains = uniquePageDataDomains(input)
  if (domains.length === 0) return
  if (options.processRole === 'db-service') {
    await options.recoverLocal()
  } else if (options.processRole === 'worker') {
    await options.sendToParent(domains)
  } else if (options.processRole === 'server') {
    await options.sendToDbService(domains)
  }
}

export async function dispatchPageDataChangeForProcess(
  event: PageDataChangeEvent,
  options: {
    runtimeStateDriver: string
    processRole: string
    publishLocal: (event: PageDataChangeEvent) => Promise<void>
    sendToParent: (event: PageDataChangeEvent) => Promise<void>
    sendToDbService: (event: PageDataChangeEvent) => Promise<void>
  }
): Promise<void> {
  if (options.runtimeStateDriver === 'redis') {
    try {
      await options.publishLocal(event)
    } catch (error) {
      if (options.processRole !== 'worker') throw error
      await options.sendToParent(event)
    }
    return
  }
  if (options.processRole === 'db-service') {
    await options.publishLocal(event)
    return
  }
  if (options.processRole === 'worker') {
    await options.sendToParent(event)
    return
  }
  if (options.processRole === 'server') {
    await options.publishLocal(event)
    await options.sendToDbService(event)
    return
  }
  await options.publishLocal(event)
}

export interface RecoveringPageDataChangeStore extends PageDataChangeStore {
  markDirty(domain: PageDataDomain): Promise<number>
  recoverDirtyDomains(): Promise<void>
}

export interface PageDataDirtyDomainPersistence {
  mark(domain: PageDataDomain): Promise<number>
  clear(domain: PageDataDomain, generation: number): Promise<boolean>
}

export interface PageDataDirtyDomainState {
  markDirty(domain: PageDataDomain): Promise<number>
  isDirty(domain: PageDataDomain): boolean
  recover(publishReset: (event: PageDataChangeEvent) => Promise<void>): Promise<void>
}

export function createPageDataDirtyDomainState(options: {
  initialRows?: Array<{ domain: string; generation: number }>
  persistence?: PageDataDirtyDomainPersistence
  now?: () => Date
} = {}): PageDataDirtyDomainState {
  const generations = new Map<PageDataDomain, number>()
  const domainLocks = new Map<PageDataDomain, Promise<void>>()
  const registeredDomains = new Set<string>(pageDataDomains)
  for (const row of options.initialRows ?? []) {
    if (!registeredDomains.has(row.domain) || !Number.isSafeInteger(row.generation) || row.generation < 1) continue
    generations.set(row.domain as PageDataDomain, row.generation)
  }
  const now = options.now ?? (() => new Date())
  const withDomainLock = async <T>(domain: PageDataDomain, operation: () => Promise<T>): Promise<T> => {
    const previous = domainLocks.get(domain) ?? Promise.resolve()
    let release: (() => void) | undefined
    const current = new Promise<void>((resolve) => { release = resolve })
    domainLocks.set(domain, current)
    await previous
    try {
      return await operation()
    } finally {
      release?.()
      if (domainLocks.get(domain) === current) domainLocks.delete(domain)
    }
  }
  return {
    async markDirty(domain) {
      return await withDomainLock(domain, async () => {
        const generation = options.persistence
          ? await options.persistence.mark(domain)
          : (generations.get(domain) ?? 0) + 1
        generations.set(domain, generation)
        return generation
      })
    },
    isDirty: (domain) => generations.has(domain),
    async recover(publishReset) {
      for (const [domain, generation] of [...generations]) {
        await publishReset({
          eventId: `dirty-recovery-${domain}-${generation}-${randomUUID()}`,
          domain,
          operation: 'range_reset',
          fieldMask: [],
          ownerSystemAccountIds: [],
          membershipChanged: true,
          orderChanged: true,
          filterChanged: true,
          pageChanged: true,
          occurredAt: now().toISOString(),
          allScopes: true
        })
        await withDomainLock(domain, async () => {
          if (generations.get(domain) !== generation) return
          if (options.persistence && !await options.persistence.clear(domain, generation)) return
          if (generations.get(domain) === generation) generations.delete(domain)
        })
      }
    }
  }
}

export function createRecoveringPageDataChangeStore(
  delegate: PageDataChangeStore,
  options: {
    dirtyState?: PageDataDirtyDomainState
    autoRecover?: boolean
    scheduleRecovery?: RetrySchedule
    cancelRecovery?: RetryCancel
  } = {}
): RecoveringPageDataChangeStore {
  const dirtyState = options.dirtyState ?? createPageDataDirtyDomainState()
  const schedule = options.scheduleRecovery ?? ((callback, delayMs) => {
    const timer = setTimeout(() => void callback(), delayMs)
    timer.unref?.()
    return timer
  })
  const cancel = options.cancelRecovery ?? ((handle) => clearTimeout(handle as NodeJS.Timeout))
  let recoveryTimer: unknown
  let recoveryRound = 0
  let recoveryInFlight: Promise<void> | undefined
  let recoveryRequested = false

  const scheduleRecovery = (): void => {
    if (options.autoRecover !== true || recoveryTimer !== undefined) return
    const delayMs = Math.min(30_000, 1_000 * (2 ** Math.min(recoveryRound, 5)))
    recoveryTimer = schedule(async () => {
      recoveryTimer = undefined
      try {
        await recoverDirtyDomains()
      } catch (error) {
        logger.warn(errorLogFields(error, { event: 'page_data_dirty_domain_recovery_retry_failed' }), '页面数据 dirty domain 后台恢复失败')
      }
    }, delayMs)
  }
  const recoverDirtyDomains = async (): Promise<void> => {
    if (recoveryInFlight) {
      await recoveryInFlight
      return
    }
    if (recoveryTimer !== undefined) {
      cancel(recoveryTimer)
      recoveryTimer = undefined
    }
    const recovery = (async () => {
      try {
        do {
          recoveryRequested = false
          await dirtyState.recover((event) => delegate.publish(event))
        } while (recoveryRequested)
        recoveryRound = 0
        if (recoveryTimer !== undefined) {
          cancel(recoveryTimer)
          recoveryTimer = undefined
        }
      } catch (error) {
        recoveryRound += 1
        scheduleRecovery()
        throw error
      }
    })()
    recoveryInFlight = recovery
    try {
      await recovery
    } finally {
      if (recoveryInFlight === recovery) recoveryInFlight = undefined
    }
  }
  return {
    async markDirty(domain) {
      const generation = await dirtyState.markDirty(domain)
      if (recoveryInFlight) recoveryRequested = true
      scheduleRecovery()
      return generation
    },
    recoverDirtyDomains,
    async confirm(scope, domains) {
      try {
        const result = await delegate.confirm(scope, domains)
        let shouldRecover = false
        for (const domain of Object.keys(domains)) {
          const typedDomain = domain as keyof typeof result.domains
          const current = result.domains[typedDomain]
          if (current && dirtyState.isDirty(typedDomain)) {
            result.domains[typedDomain] = { ...current, action: 'reset', changes: undefined }
            shouldRecover = true
          }
        }
        if (shouldRecover) scheduleRecovery()
        return result
      } catch (error) {
        // A read-only confirm failure is not a publish failure. Marking every
        // domain dirty here turns a Redis outage into a PostgreSQL write storm.
        throw error
      }
    },
    publish: (event) => delegate.publish(event)
  }
}

type RetrySchedule = (callback: () => Promise<void>, delayMs: number) => unknown
type RetryCancel = (handle: unknown) => void

export interface PageDataPublishRetryQueue {
  readonly pendingCount: number
  publish(event: PageDataChangeEvent): Promise<void>
  flush(): Promise<void>
}

export function createPageDataPublishRetryQueue(options: {
  deliver: (event: PageDataChangeEvent) => Promise<void>
  onFailure?: (error: unknown, event: PageDataChangeEvent) => void
  schedule?: RetrySchedule
  cancel?: RetryCancel
  maxPending?: number
  now?: () => Date
}): PageDataPublishRetryQueue {
  const pending = new Map<string, PageDataChangeEvent>()
  const maxPending = Math.max(4, Math.min(Math.trunc(options.maxPending ?? 256), 4096))
  const schedule = options.schedule ?? ((callback, delayMs) => {
    const timer = setTimeout(() => void callback(), delayMs)
    timer.unref?.()
    return timer
  })
  const cancel = options.cancel ?? ((handle) => clearTimeout(handle as NodeJS.Timeout))
  const now = options.now ?? (() => new Date())
  let timer: unknown
  let retryRound = 0

  const scheduleRetry = (): void => {
    if (timer !== undefined || pending.size === 0) return
    const delayMs = Math.min(30_000, 1_000 * (2 ** Math.min(retryRound, 5)))
    timer = schedule(async () => {
      timer = undefined
      await api.flush()
    }, delayMs)
  }

  const enqueue = (event: PageDataChangeEvent): void => {
    pending.set(event.eventId, event)
    if (pending.size <= maxPending) return
    const domains = [...new Set([...pending.values()].map((item) => item.domain))]
    pending.clear()
    for (const domain of domains) {
      const dirtyEvent: PageDataChangeEvent = {
        eventId: `dirty-${domain}-${randomUUID()}`,
        domain,
        operation: 'range_reset',
        fieldMask: [],
        ownerSystemAccountIds: [],
        membershipChanged: true,
        orderChanged: true,
        filterChanged: true,
        pageChanged: true,
        occurredAt: now().toISOString(),
        allScopes: true
      }
      pending.set(dirtyEvent.eventId, dirtyEvent)
    }
  }

  const api: PageDataPublishRetryQueue = {
    get pendingCount() { return pending.size },
    async publish(event) {
      try {
        await options.deliver(event)
      } catch (error) {
        options.onFailure?.(error, event)
        enqueue(event)
        scheduleRetry()
        throw error
      }
    },
    async flush() {
      if (timer !== undefined) {
        cancel(timer)
        timer = undefined
      }
      let failed = false
      for (const [eventId, event] of [...pending]) {
        try {
          await options.deliver(event)
          pending.delete(eventId)
        } catch (error) {
          failed = true
          options.onFailure?.(error, event)
        }
      }
      retryRound = failed ? retryRound + 1 : 0
      scheduleRetry()
    }
  }
  return api
}

function createDriverStore(): PageDataChangeStore {
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return createMemoryPageDataChangeStore()
  }
  const stateUrl = runtimeConfig.redis.stateUrl
  if (!stateUrl) throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  return createRedisPageDataChangeStore({
    client: lazyRedisClient(() => getRedisClient(stateUrl)),
    keyPrefix: redisNamespacedKey('juhe-ai:page-data-change')
  })
}

function lazyRedisClient(load: () => Promise<RedisCommandClient>): PageDataRedisClient {
  return {
    get: async (key) => (await load()).get(key),
    set: async (key, value, options) => (await load()).set(key, value, options),
    eval: async (script, options) => (await load()).eval(script, options),
    sendCommand: async (command) => (await load()).sendCommand(command)
  }
}

async function sendPageDataChangeToParent(event: PageDataChangeEvent): Promise<void> {
  if (typeof process.send !== 'function') {
    throw new Error('当前 worker 没有可用的父进程 IPC')
  }
  await new Promise<void>((resolve, reject) => {
    try {
      process.send?.({ type: 'page_data_change_publish', event }, (error) => error ? reject(error) : resolve())
    } catch (error) {
      reject(error)
    }
  })
}

export async function sendPageDataDirtyDomainsToParent(domains: PageDataDomain[], timeoutMs = 5_000): Promise<void> {
  if (typeof process.send !== 'function') throw new Error('当前 worker 没有可用的父进程 IPC')
  if (pendingPageDataDirtyParentAcks.size >= maxPendingPageDataDirtyParentAcks) {
    throw new Error('页面数据 dirty domain 父进程 ACK 队列已满')
  }
  const requestId = randomUUID()
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => {
      const pending = pendingPageDataDirtyParentAcks.get(requestId)
      if (!pending) return
      pendingPageDataDirtyParentAcks.delete(requestId)
      pending.reject(new Error('页面数据 dirty domain 父进程持久化 ACK 超时'))
    }, Math.max(1, Math.trunc(timeoutMs)))
    pendingPageDataDirtyParentAcks.set(requestId, { resolve, reject, timeout })
    const fail = (error: unknown): void => {
      const pending = pendingPageDataDirtyParentAcks.get(requestId)
      if (!pending) return
      clearTimeout(pending.timeout)
      pendingPageDataDirtyParentAcks.delete(requestId)
      pending.reject(error instanceof Error ? error : new Error(String(error)))
    }
    try {
      process.send?.({ type: 'page_data_change_dirty', requestId, domains: uniquePageDataDomains(domains) }, (error) => {
        if (error) fail(error)
      })
    } catch (error) {
      fail(error)
    }
  })
}

export function acceptPageDataDirtyDomainsParentAck(requestId: string, ok: boolean, errorMessage?: string): void {
  const pending = pendingPageDataDirtyParentAcks.get(requestId)
  if (!pending) return
  clearTimeout(pending.timeout)
  pendingPageDataDirtyParentAcks.delete(requestId)
  if (ok) pending.resolve()
  else pending.reject(new Error(errorMessage?.trim() || '页面数据 dirty domain 父进程持久化失败'))
}

function uniquePageDataDomains(domains: readonly unknown[]): PageDataDomain[] {
  const registered = new Set<string>(pageDataDomains)
  return [...new Set(domains.filter((domain): domain is PageDataDomain => typeof domain === 'string' && registered.has(domain)))]
}
