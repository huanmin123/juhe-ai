import { randomBytes } from 'node:crypto'

import { errorLogFields, logger } from '../../../../shared/logger.js'
import { createRuntimeStateStore, type RuntimeStateStore } from '../../../../shared/runtime-state-store.js'

export const providerOAuthRefreshLockTtlMs = 90_000
export const providerOAuthRefreshLockWaitMs = 30_000
export const providerOAuthRefreshLockRetryMs = 250

export interface ProviderOAuthRefreshLockOptions {
  signal?: AbortSignal
  lockStore?: RuntimeStateStore
  lockTtlMs?: number
  waitMs?: number
  retryMs?: number
}

export async function runWithProviderOAuthRefreshLock<T>(
  providerCode: string,
  accountId: string,
  task: () => Promise<T>,
  options: ProviderOAuthRefreshLockOptions = {}
): Promise<T> {
  throwIfAborted(options.signal)
  const lockStore = options.lockStore ?? createRuntimeStateStore('provider-oauth:refresh-locks')
  const lockKey = `${requiredLockPart(providerCode, '供应商')}:${requiredLockPart(accountId, '账户')}`
  const token = randomBytes(16).toString('hex')
  const ttlMs = positiveDuration(options.lockTtlMs, providerOAuthRefreshLockTtlMs)
  const waitMs = positiveDuration(options.waitMs, providerOAuthRefreshLockWaitMs)
  const retryMs = positiveDuration(options.retryMs, providerOAuthRefreshLockRetryMs)
  const deadline = Date.now() + waitMs

  while (!await lockStore.acquireLock(lockKey, { ttlMs, token })) {
    throwIfAborted(options.signal)
    if (Date.now() >= deadline) {
      throw new Error(`${providerCode} OAuth 账户正在其他节点刷新，请稍后重试`)
    }
    await abortableDelay(retryMs, options.signal)
  }

  try {
    throwIfAborted(options.signal)
    return await task()
  } finally {
    await lockStore.releaseLock(lockKey, token).catch((error) => {
      logger.error(errorLogFields(error, {
        event: 'provider_oauth_refresh_lock_release_failed',
        providerCode,
        accountId
      }), '供应商 OAuth 刷新锁释放失败')
    })
  }
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
