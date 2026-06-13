import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { createAppCache } from '../../../shared/cache.js'
import {
  findActiveClientIpPolicyByHash,
  normalizeClientIpForStats,
  recordClientIpPolicyHits,
  type ActiveClientIpPolicy,
  type ClientIpPolicyHitInput
} from '../../../storage/client-ip-stats.repository.js'
import { requestDbService } from '../../db-service/db-service-ipc.js'

export interface ClientIpPolicyDecision {
  blocked: boolean
  normalizedIp?: ReturnType<typeof normalizeClientIpForStats>
  blacklistPolicy?: ActiveClientIpPolicy
}

interface InspectClientIpPolicyOptions {
  cacheOnly?: boolean
}

type ClientIpPolicyLoadResult =
  | { status: 'loaded'; policy: ActiveClientIpPolicy | undefined }
  | { status: 'skipped' }

const clientIpPolicyCacheTtlMs = 30_000
const clientIpPolicyCacheMaxEntries = 5_000
const clientIpPolicyLoadMaxPendingEntries = 1024
const clientIpPolicyHitFlushDelayMs = 1000
const clientIpPolicyHitMaxPendingEntries = 5_000
const clientIpPolicyHitFlushBatchSize = 1000
const policyCache = createAppCache<string, { policy: ActiveClientIpPolicy | undefined }>({
  name: 'gateway:client-ip-policy-by-ip',
  max: clientIpPolicyCacheMaxEntries,
  ttlMs: clientIpPolicyCacheTtlMs
})
const pendingPolicyLoads = new Map<string, Promise<ClientIpPolicyLoadResult>>()
const pendingPolicyHits = new Map<string, ClientIpPolicyHitInput>()
let policyHitFlushTimer: NodeJS.Timeout | undefined
let droppedPolicyLoadCount = 0
let droppedPolicyHitCount = 0

export async function inspectClientIpPolicy(clientIp?: string, options: InspectClientIpPolicyOptions = {}): Promise<ClientIpPolicyDecision> {
  const normalizedIp = normalizeClientIpForStats(clientIp)
  if (!normalizedIp) {
    return { blocked: false }
  }
  const cached = policyCache.get(normalizedIp.ipHash)
  if (cached) {
    return policyDecisionFromCacheEntry(normalizedIp, cached)
  }
  if (options.cacheOnly) {
    return { blocked: false, normalizedIp }
  }
  const loaded = await loadClientIpPolicy(normalizedIp.ipHash)
  if (loaded.status !== 'loaded') {
    return { blocked: false, normalizedIp }
  }
  policyCache.set(normalizedIp.ipHash, { policy: loaded.policy }, {
    ttlMs: clientIpPolicyTtlMs(loaded.policy)
  })
  return policyDecisionFromCacheEntry(normalizedIp, { policy: loaded.policy })
}

export function primeClientIpPolicyCacheLocal(policies: ActiveClientIpPolicy[]): void {
  policyCache.clear()
  for (const policy of policies) {
    policyCache.set(policy.ipHash, { policy }, { ttlMs: clientIpPolicyTtlMs(policy) })
  }
}

export function recordClientIpPolicyHitAsync(policy: ActiveClientIpPolicy): void {
  const key = `${policy.ipHash}:${policy.id}`
  const current = pendingPolicyHits.get(key)
  if (!current && pendingPolicyHits.size >= clientIpPolicyHitMaxPendingEntries) {
    droppedPolicyHitCount += 1
    if (droppedPolicyHitCount <= 10 || droppedPolicyHitCount % 1000 === 0) {
      logger.warn({
        event: 'client_ip_policy_hit_buffer_dropped',
        ipHash: policy.ipHash,
        policyId: policy.id,
        pendingHitCount: pendingPolicyHits.size,
        maxPendingEntries: clientIpPolicyHitMaxPendingEntries,
        droppedPolicyHitCount
      }, 'IP 封禁命中缓冲达到保护上限，已丢弃新的 distinct 命中')
    }
    return
  }
  pendingPolicyHits.set(key, {
    ipHash: policy.ipHash,
    policyId: policy.id,
    hitCount: (current?.hitCount ?? 0) + 1,
    hitAt: new Date().toISOString()
  })
  scheduleClientIpPolicyHitFlush(clientIpPolicyHitFlushDelayMs)
}

export function getClientIpPolicyCacheRuntime(): {
  pendingPolicyLoadCount: number
  maxPendingPolicyLoads: number
  droppedPolicyLoadCount: number
  pendingPolicyHitCount: number
  droppedPolicyHitCount: number
  maxPendingPolicyHits: number
  flushBatchSize: number
} {
  return {
    pendingPolicyLoadCount: pendingPolicyLoads.size,
    maxPendingPolicyLoads: clientIpPolicyLoadMaxPendingEntries,
    droppedPolicyLoadCount,
    pendingPolicyHitCount: pendingPolicyHits.size,
    droppedPolicyHitCount,
    maxPendingPolicyHits: clientIpPolicyHitMaxPendingEntries,
    flushBatchSize: clientIpPolicyHitFlushBatchSize
  }
}

function scheduleClientIpPolicyHitFlush(delayMs: number): void {
  if (policyHitFlushTimer) return
  policyHitFlushTimer = setTimeout(() => {
    policyHitFlushTimer = undefined
    void flushClientIpPolicyHits()
  }, delayMs)
  policyHitFlushTimer.unref?.()
}

export function clearClientIpPolicyCacheLocal(): void {
  policyCache.clear()
  pendingPolicyLoads.clear()
}

async function loadClientIpPolicy(ipHash: string): Promise<ClientIpPolicyLoadResult> {
  const pending = pendingPolicyLoads.get(ipHash)
  if (pending) {
    return pending
  }
  if (pendingPolicyLoads.size >= clientIpPolicyLoadMaxPendingEntries) {
    droppedPolicyLoadCount += 1
    if (droppedPolicyLoadCount <= 10 || droppedPolicyLoadCount % 1000 === 0) {
      logger.warn({
        event: 'client_ip_policy_load_dropped',
        ipHash,
        pendingPolicyLoadCount: pendingPolicyLoads.size,
        maxPendingPolicyLoads: clientIpPolicyLoadMaxPendingEntries,
        droppedPolicyLoadCount
      }, 'IP 封禁策略查询达到保护上限，已跳过新的来源查询')
    }
    return { status: 'skipped' }
  }
  const promise = findClientIpPolicy(ipHash).then((policy) => ({ status: 'loaded' as const, policy }))
  pendingPolicyLoads.set(ipHash, promise)
  try {
    return await promise
  } finally {
    pendingPolicyLoads.delete(ipHash)
  }
}

async function findClientIpPolicy(ipHash: string): Promise<ActiveClientIpPolicy | undefined> {
  try {
    return runtimeConfig.processRole === 'server'
      ? await requestDbService({ type: 'find_active_client_ip_policy', ipHash }, { timeoutMs: 1000 })
      : findActiveClientIpPolicyByHash(ipHash)
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'client_ip_policy_lookup_failed',
      ipHash
    }), 'IP 策略按来源查询失败')
    return undefined
  }
}

function isPolicyActiveAt(policy: ActiveClientIpPolicy, nowMs: number): boolean {
  const expiresAtMs = policyExpiresAtTime(policy)
  return expiresAtMs === undefined || expiresAtMs > nowMs
}

function policyDecisionFromCacheEntry(
  normalizedIp: NonNullable<ReturnType<typeof normalizeClientIpForStats>>,
  entry: { policy: ActiveClientIpPolicy | undefined }
): ClientIpPolicyDecision {
  const policy = entry.policy && isPolicyActiveAt(entry.policy, Date.now()) ? entry.policy : undefined
  return {
    blocked: Boolean(policy),
    normalizedIp,
    blacklistPolicy: policy
  }
}

function clientIpPolicyTtlMs(policy: ActiveClientIpPolicy | undefined): number {
  const expiresAtMs = policy ? policyExpiresAtTime(policy) : undefined
  if (expiresAtMs === undefined) {
    return clientIpPolicyCacheTtlMs
  }
  return Math.max(1, Math.min(clientIpPolicyCacheTtlMs, expiresAtMs - Date.now()))
}

function policyExpiresAtTime(policy: ActiveClientIpPolicy): number | undefined {
  if (!policy.expiresAt) return undefined
  const expiresAtMs = Date.parse(policy.expiresAt)
  return Number.isFinite(expiresAtMs) ? expiresAtMs : undefined
}

async function flushClientIpPolicyHits(): Promise<void> {
  if (pendingPolicyHits.size === 0) return
  const entries = [...pendingPolicyHits.entries()].slice(0, clientIpPolicyHitFlushBatchSize)
  for (const [key] of entries) {
    pendingPolicyHits.delete(key)
  }
  const hits = entries.map(([, hit]) => hit)
  try {
    if (runtimeConfig.processRole === 'server') {
      await requestDbService({ type: 'record_client_ip_policy_hits', hits }, { timeoutMs: 1000 })
    } else {
      recordClientIpPolicyHits(hits)
    }
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'client_ip_policy_hits_flush_failed',
      hitCount: hits.reduce((sum, hit) => sum + Number(hit.hitCount ?? 0), 0)
    }), 'IP 封禁命中记录写入失败')
  } finally {
    if (pendingPolicyHits.size > 0) {
      scheduleClientIpPolicyHitFlush(0)
    }
  }
}
