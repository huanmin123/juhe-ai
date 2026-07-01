import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { createAppCache, createSharedJsonCache, throwIfRedisCacheIsRequired } from '../../../shared/cache.js'
import {
  listActiveClientIpPoliciesAsync,
  normalizeClientIpForStats,
  recordClientIpPolicyHitsAsync,
  type ActiveClientIpPolicy,
  type ClientIpPolicyHitInput
} from '../../../storage/client-ip-stats.repository.js'
import { requestStatsWriter } from '../../background/background-stats-writer.js'

export interface ClientIpPolicyDecision {
  blocked: boolean
  normalizedIp?: ReturnType<typeof normalizeClientIpForStats>
  blacklistPolicy?: ActiveClientIpPolicy
}

interface InspectClientIpPolicyOptions {
  cacheOnly?: boolean
}

interface ClientIpPolicySnapshotCacheEntry {
  loadedAt: string
  policies: ActiveClientIpPolicy[]
}

const clientIpPolicyCacheTtlMs = 30_000
const clientIpPolicyCacheMaxEntries = 5_000
const clientIpPolicyHitFlushDelayMs = 1000
const clientIpPolicyHitMaxPendingEntries = 5_000
const clientIpPolicyHitFlushBatchSize = 1000
const activePolicySnapshotSharedCacheKey = 'active'
const policyCache = createAppCache<string, { policy: ActiveClientIpPolicy | undefined }>({
  name: 'gateway:client-ip-policy-by-ip',
  max: clientIpPolicyCacheMaxEntries,
  ttlMs: clientIpPolicyCacheTtlMs
})
const activePolicySnapshotSharedCache = createSharedJsonCache<ClientIpPolicySnapshotCacheEntry>({
  name: 'gateway:client-ip-policy-snapshot',
  max: 1,
  ttlMs: clientIpPolicyCacheTtlMs
})
const activePolicySnapshot = new Map<string, ActiveClientIpPolicy>()
const pendingPolicyHits = new Map<string, ClientIpPolicyHitInput>()
let activePolicySnapshotLoadedAt: string | undefined
let policyHitFlushTimer: NodeJS.Timeout | undefined
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
  const snapshotPolicy = activePolicySnapshot.get(normalizedIp.ipHash)
  if (!snapshotPolicy && options.cacheOnly && !activePolicySnapshotLoadedAt) {
    return { blocked: false, normalizedIp }
  }
  const entry = { policy: snapshotPolicy }
  policyCache.set(normalizedIp.ipHash, entry, {
    ttlMs: clientIpPolicyTtlMs(snapshotPolicy)
  })
  return policyDecisionFromCacheEntry(normalizedIp, entry)
}

export function primeClientIpPolicyCacheLocal(policies: ActiveClientIpPolicy[]): void {
  replaceClientIpPolicyCacheLocal(policies)
}

export function replaceClientIpPolicyCacheLocal(policies: ActiveClientIpPolicy[], options: { skipSharedCache?: boolean } = {}): void {
  policyCache.clear()
  activePolicySnapshot.clear()
  for (const policy of policies) {
    const cloned = cloneActiveClientIpPolicy(policy)
    if (!activePolicySnapshot.has(cloned.ipHash)) {
      activePolicySnapshot.set(cloned.ipHash, cloned)
    }
  }
  activePolicySnapshotLoadedAt = new Date().toISOString()
  if (!options.skipSharedCache) {
    setActivePolicySnapshotSharedCacheEntry({
      loadedAt: activePolicySnapshotLoadedAt,
      policies: [...activePolicySnapshot.values()].map(cloneActiveClientIpPolicy)
    })
  }
}

export async function reloadClientIpPolicyCacheLocal(): Promise<void> {
  const sharedSnapshot = await getActivePolicySnapshotSharedCacheEntry()
  if (sharedSnapshot) {
    replaceClientIpPolicyCacheLocal(sharedSnapshot.policies, { skipSharedCache: true })
    activePolicySnapshotLoadedAt = sharedSnapshot.loadedAt
    return
  }
  const policies = shouldUseStatsWriterBridge()
    ? await requestStatsWriter({ type: 'list_active_client_ip_policies' }, 1000)
    : await listActiveClientIpPoliciesAsync()
  replaceClientIpPolicyCacheLocal(policies)
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
  snapshotLoadedAt?: string
  snapshotPolicyCount: number
  pendingPolicyHitCount: number
  droppedPolicyHitCount: number
  maxPendingPolicyHits: number
  flushBatchSize: number
} {
  return {
    snapshotLoadedAt: activePolicySnapshotLoadedAt,
    snapshotPolicyCount: activePolicySnapshot.size,
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
  activePolicySnapshot.clear()
  activePolicySnapshotLoadedAt = undefined
  clearActivePolicySnapshotSharedCache()
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

function cloneActiveClientIpPolicy(policy: ActiveClientIpPolicy): ActiveClientIpPolicy {
  return {
    id: policy.id,
    ipHash: policy.ipHash,
    aggregateIpKey: policy.aggregateIpKey,
    clientIp: policy.clientIp,
    reason: policy.reason,
    expiresAt: policy.expiresAt
  }
}

async function getActivePolicySnapshotSharedCacheEntry(): Promise<ClientIpPolicySnapshotCacheEntry | undefined> {
  try {
    const cached = await activePolicySnapshotSharedCache.get(activePolicySnapshotSharedCacheKey)
    if (!cached || !Array.isArray(cached.policies)) return undefined
    return {
      loadedAt: typeof cached.loadedAt === 'string' ? cached.loadedAt : new Date().toISOString(),
      policies: cached.policies
        .filter(isActiveClientIpPolicy)
        .map(cloneActiveClientIpPolicy)
    }
  } catch (error) {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'client_ip_policy_snapshot_shared_cache_read_failed'
    }), '读取 IP 封禁策略 Redis shared cache 失败')
    return undefined
  }
}

function setActivePolicySnapshotSharedCacheEntry(entry: ClientIpPolicySnapshotCacheEntry): void {
  void activePolicySnapshotSharedCache.set(activePolicySnapshotSharedCacheKey, {
    loadedAt: entry.loadedAt,
    policies: entry.policies.map(cloneActiveClientIpPolicy)
  }, { ttlMs: clientIpPolicyCacheTtlMs }).catch((error) => {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'client_ip_policy_snapshot_shared_cache_write_failed',
      policyCount: entry.policies.length
    }), '写入 IP 封禁策略 Redis shared cache 失败')
  })
}

function clearActivePolicySnapshotSharedCache(): void {
  void activePolicySnapshotSharedCache.clear().catch((error) => {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'client_ip_policy_snapshot_shared_cache_clear_failed'
    }), '清理 IP 封禁策略 Redis shared cache 失败')
  })
}

function isActiveClientIpPolicy(value: unknown): value is ActiveClientIpPolicy {
  if (!value || typeof value !== 'object') return false
  const record = value as Record<string, unknown>
  return typeof record.id === 'string'
    && typeof record.ipHash === 'string'
    && typeof record.aggregateIpKey === 'string'
    && typeof record.clientIp === 'string'
    && (record.reason === undefined || typeof record.reason === 'string')
    && (record.expiresAt === undefined || typeof record.expiresAt === 'string')
}

async function flushClientIpPolicyHits(): Promise<void> {
  if (pendingPolicyHits.size === 0) return
  const entries = [...pendingPolicyHits.entries()].slice(0, clientIpPolicyHitFlushBatchSize)
  for (const [key] of entries) {
    pendingPolicyHits.delete(key)
  }
  const hits = entries.map(([, hit]) => hit)
  try {
    if (shouldUseStatsWriterBridge()) {
      await requestStatsWriter({ type: 'record_client_ip_policy_hits', hits }, 1000)
    } else {
      await recordClientIpPolicyHitsAsync(hits)
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

function shouldUseStatsWriterBridge(): boolean {
  return runtimeConfig.processRole === 'server'
    || (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole !== 'stats-worker')
}
