import { randomBytes } from 'node:crypto'

import { errorLogFields, logger } from '../../../../shared/logger.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../../../shared/runtime-state-store.js'

export const providerOAuthRefreshLockTtlMs = 90_000
export const providerOAuthRefreshLockWaitMs = 30_000
export const providerOAuthRefreshLockRetryMs = 250

export interface ProviderOAuthRefreshCompatibilityLock {
  lockStore: RuntimeStateStore
  lockKey: string
}

export interface ProviderOAuthRefreshLockOptions {
  signal?: AbortSignal
  lockStore?: RuntimeStateStore
  compatibilityLock?: ProviderOAuthRefreshCompatibilityLock
  lockTtlMs?: number
  waitMs?: number
  retryMs?: number
  failIfLocked?: boolean
  onLockAcquired?: () => void
}

export class ProviderOAuthRefreshLockBusyError extends Error {
  constructor(providerCode: string, accountId: string) {
    super(`${providerCode} OAuth 账户正在其他节点刷新，请稍后重试：${accountId}`)
    this.name = 'ProviderOAuthRefreshLockBusyError'
  }
}

export async function runWithProviderOAuthRefreshLock<T>(
  providerCode: string,
  accountId: string,
  task: (signal: AbortSignal, assertLockOwned: () => Promise<void>) => Promise<T>,
  options: ProviderOAuthRefreshLockOptions = {}
): Promise<T> {
  throwIfAborted(options.signal)
  const lockStore = options.lockStore ?? createRuntimeStateStore('provider-oauth:refresh-locks')
  const lockKey = `${requiredLockPart(providerCode, '供应商')}:${requiredLockPart(accountId, '账户')}`
  const primaryLock = { lockStore, lockKey }
  const compatibilityLock = options.compatibilityLock
    ? { lockStore: options.compatibilityLock.lockStore, lockKey: requiredLockPart(options.compatibilityLock.lockKey, '兼容') }
    : undefined
  // Legacy first is the single acquisition order used by every dual-lock caller during rollout.
  const locks: ProviderOAuthRefreshLockTarget[] = compatibilityLock
    && (compatibilityLock.lockStore !== primaryLock.lockStore || compatibilityLock.lockKey !== primaryLock.lockKey)
    ? [compatibilityLock, primaryLock]
    : [primaryLock]
  const token = randomBytes(16).toString('hex')
  const ttlMs = positiveDuration(options.lockTtlMs, providerOAuthRefreshLockTtlMs)
  const waitMs = positiveDuration(options.waitMs, providerOAuthRefreshLockWaitMs)
  const retryMs = positiveDuration(options.retryMs, providerOAuthRefreshLockRetryMs)
  const deadline = Date.now() + waitMs

  const acquiredLocks: ProviderOAuthRefreshLockTarget[] = []
  try {
    for (const lock of locks) {
      while (!await lock.lockStore.acquireLock(lock.lockKey, { ttlMs, token })) {
        throwIfAborted(options.signal)
        if (options.failIfLocked || Date.now() >= deadline) {
          throw new ProviderOAuthRefreshLockBusyError(providerCode, accountId)
        }
        await abortableDelay(retryMs, options.signal)
      }
      acquiredLocks.push(lock)
    }
  } catch (error) {
    await releaseProviderOAuthRefreshLocks(acquiredLocks, token, providerCode, accountId)
    throw error
  }

  const renewalAbort = new AbortController()
  let lockLostError: Error | undefined
  const loseLock = (error: Error) => {
    if (lockLostError) return
    lockLostError = error
    renewalAbort.abort(error)
  }
  const assertLockOwned = async () => {
    if (lockLostError) throw lockLostError
    try {
      for (const lock of acquiredLocks) {
        const renewed = await lock.lockStore.renewLock(lock.lockKey, { ttlMs, token })
        if (renewed) continue
        const error = providerOAuthRefreshLockLostError(providerCode, accountId)
        loseLock(error)
        throw error
      }
    } catch (error) {
      if (lockLostError) throw lockLostError
      const lockError = new Error(`${providerCode} OAuth 刷新锁持有状态验证失败：${accountId}`, { cause: error })
      loseLock(lockError)
      throw lockError
    }
  }
  const renewal = renewProviderOAuthRefreshLock({
    locks: acquiredLocks,
    token,
    ttlMs,
    providerCode,
    accountId,
    signal: renewalAbort.signal,
    onLost: loseLock
  })
  try {
    throwIfAborted(options.signal)
    options.onLockAcquired?.()
    throwIfAborted(options.signal)
    const result = await task(renewalAbort.signal, assertLockOwned)
    if (lockLostError) throw lockLostError
    return result
  } finally {
    renewalAbort.abort()
    await renewal.catch(() => undefined)
    await releaseProviderOAuthRefreshLocks(acquiredLocks, token, providerCode, accountId)
  }
}

interface ProviderOAuthRefreshLockTarget {
  lockStore: RuntimeStateStore
  lockKey: string
}

interface ProviderOAuthRefreshRenewalInput {
  locks: readonly ProviderOAuthRefreshLockTarget[]
  token: string
  ttlMs: number
  providerCode: string
  accountId: string
  signal: AbortSignal
  onLost(error: Error): void
}

async function renewProviderOAuthRefreshLock(input: ProviderOAuthRefreshRenewalInput): Promise<void> {
  const intervalMs = Math.max(25, Math.min(Math.floor(input.ttlMs / 3), input.ttlMs - 1))
  const lastSuccessAt = new Map(input.locks.map((lock) => [lock, Date.now()]))
  let delayMs = intervalMs
  while (!input.signal.aborted) {
    await abortableDelay(delayMs, input.signal).catch(() => undefined)
    if (input.signal.aborted) return
    for (const lock of input.locks) {
      try {
        const renewed = await lock.lockStore.renewLock(lock.lockKey, { ttlMs: input.ttlMs, token: input.token })
        if (!renewed) {
          const error = providerOAuthRefreshLockLostError(input.providerCode, input.accountId)
          input.onLost(error)
          return
        }
        lastSuccessAt.set(lock, Date.now())
        delayMs = intervalMs
      } catch (error) {
        if (Date.now() - (lastSuccessAt.get(lock) ?? 0) >= input.ttlMs) {
          const lockError = new Error(`${input.providerCode} OAuth 刷新锁续租失败：${input.accountId}`, { cause: error })
          input.onLost(lockError)
          return
        }
        delayMs = Math.min(1_000, intervalMs)
        logger.warn(errorLogFields(error, {
          event: 'provider_oauth_refresh_lock_renew_failed',
          providerCode: input.providerCode,
          accountId: input.accountId
        }), '供应商 OAuth 刷新锁续租暂时失败')
      }
    }
  }
}

async function releaseProviderOAuthRefreshLocks(
  locks: readonly ProviderOAuthRefreshLockTarget[],
  token: string,
  providerCode: string,
  accountId: string
): Promise<void> {
  for (const lock of [...locks].reverse()) {
    await lock.lockStore.releaseLock(lock.lockKey, token).catch((error) => {
      logger.error(errorLogFields(error, {
        event: 'provider_oauth_refresh_lock_release_failed',
        providerCode,
        accountId
      }), '供应商 OAuth 刷新锁释放失败')
    })
  }
}

function providerOAuthRefreshLockLostError(providerCode: string, accountId: string): Error {
  return new Error(`${providerCode} OAuth 刷新锁已丢失：${accountId}`)
}

function requiredLockPart(value: string, label: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`${label}刷新锁标识不能为空`)
  return normalized
}

function positiveDuration(value: number | undefined, fallback: number): number {
  return Number.isFinite(value) && value! > 0 ? Math.trunc(value!) : fallback
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) {
    throw signal.reason instanceof Error ? signal.reason : new Error('请求已取消')
  }
}

async function abortableDelay(delayMs: number, signal?: AbortSignal): Promise<void> {
  if (!signal) {
    await new Promise<void>((resolve) => setTimeout(resolve, delayMs))
    return
  }
  throwIfAborted(signal)
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort)
      resolve()
    }, delayMs)
    const abort = () => {
      clearTimeout(timer)
      reject(signal.reason instanceof Error ? signal.reason : new Error('请求已取消'))
    }
    signal.addEventListener('abort', abort, { once: true })
  })
}
