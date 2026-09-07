import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { requiredRfc3339Instant, rfc3339InstantMilliseconds } from '../../../shared/rfc3339.js'
import { createAppCache, createSharedJsonCache } from '../../../shared/cache.js'
import {
  findActiveClientIpPolicyByHashAsync,
  listActiveClientIpPoliciesAsync,
  normalizeClientIpForStats,
  recordClientIpPolicyHitsAsync,
  type ActiveClientIpPolicy,
  type ClientIpPolicyHitInput
} from '../../../storage/client-ip-stats.repository.js'
import { requestStatsWriter } from '../../background/background-stats-writer.js'

export interface ClientIpPolicyDecision {
  blocked: boolean
  allowlisted: boolean
  normalizedIp?: ReturnType<typeof normalizeClientIpForStats>
  blacklistPolicy?: ActiveClientIpPolicy
  allowlistPolicy?: ActiveClientIpPolicy
}

interface InspectClientIpPolicyOptions {
  cacheOnly?: boolean
  ensureSnapshotLoaded?: boolean
}

interface ClientIpPolicySnapshotCacheEntry {
  loadedAt: string
  policies: ActiveClientIpPolicy[]
}

interface ClientIpPolicyByIpCacheEntry {
  loadedAt: string
  policy?: ActiveClientIpPolicy
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
const policyByIpSharedCache = createSharedJsonCache<ClientIpPolicyByIpCacheEntry>({
  name: 'gateway:client-ip-policy-by-ip',
  max: clientIpPolicyCacheMaxEntries,
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
    return { blocked: false, allowlisted: false }
  }
  if (runtimeConfig.cacheDriver === 'redis') {
    const entry = options.cacheOnly
      ? await getClientIpPolicyByIpSharedCacheEntry(normalizedIp.ipHash)
      : await loadClientIpPolicyByHashFromSharedCacheOrDatabase(normalizedIp.ipHash)
    return policyDecisionFromCacheEntry(normalizedIp, { policy: entry?.policy })
  }
  const cached = policyCache.get(normalizedIp.ipHash)
  if (cached) {
    return policyDecisionFromCacheEntry(normalizedIp, cached)
  }
  if (!activePolicySnapshotLoadedAt && options.ensureSnapshotLoaded) {
    await reloadClientIpPolicyCacheLocal()
  }
  const snapshotPolicy = activePolicySnapshot.get(normalizedIp.ipHash)
  if (!snapshotPolicy && options.cacheOnly && !activePolicySnapshotLoadedAt) {
    return { blocked: false, allowlisted: false, normalizedIp }
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
  if (runtimeConfig.cacheDriver === 'redis') {
    activePolicySnapshotLoadedAt = undefined
    if (!options.skipSharedCache) {
      throw new Error('高性能模式禁止同步写入 Client-IP 策略 Redis shared cache，必须使用异步刷新入口')
    }
    return
  }
  for (const policy of policies) {
    const cloned = cloneActiveClientIpPolicy(policy)
    if (!activePolicySnapshot.has(cloned.ipHash)) {
      activePolicySnapshot.set(cloned.ipHash, cloned)
    }
  }
  activePolicySnapshotLoadedAt = new Date().toISOString()
}

export async function reloadClientIpPolicyCacheLocal(options: { bypassSharedCache?: boolean } = {}): Promise<void> {
  if (runtimeConfig.cacheDriver === 'redis') {
    activePolicySnapshot.clear()
    activePolicySnapshotLoadedAt = undefined
    if (options.bypassSharedCache) {
      await clearActivePolicySnapshotSharedCache()
      await clearPolicyByIpSharedCache()
      return
    }
    await clearPolicyByIpSharedCache()
    return
  }
  const sharedSnapshot = options.bypassSharedCache ? undefined : await getActivePolicySnapshotSharedCacheEntry()
  if (sharedSnapshot) {
    replaceClientIpPolicyCacheLocal(sharedSnapshot.policies, { skipSharedCache: true })
    activePolicySnapshotLoadedAt = sharedSnapshot.loadedAt
    return
  }
  const snapshot = await loadClientIpPolicySnapshotFromDatabase()
  replaceClientIpPolicyCacheLocal(snapshot.policies, { skipSharedCache: true })
  activePolicySnapshotLoadedAt = snapshot.loadedAt
}

export async function replaceClientIpPolicySharedSnapshotAsync(policies: ActiveClientIpPolicy[]): Promise<void> {
  policyCache.clear()
  activePolicySnapshot.clear()
  if (runtimeConfig.cacheDriver !== 'redis') {
    replaceClientIpPolicyCacheLocal(policies)
    return
  }
  activePolicySnapshotLoadedAt = undefined
  await clearActivePolicySnapshotSharedCache()
  await clearPolicyByIpSharedCache()
  const loadedAt = new Date().toISOString()
  for (const policy of policies) {
    await setClientIpPolicyByIpSharedCacheEntry(policy.ipHash, policy, loadedAt)
  }
}

export async function recordClientIpPolicyHitAsync(policy: ActiveClientIpPolicy): Promise<void> {
  if (policy.policyType !== 'blacklist') return
  const hit = clientIpPolicyHitInput(policy, 1)
  if (runtimeConfig.cacheDriver === 'redis' || runtimeConfig.runtimeMode === 'performance') {
    await writeClientIpPolicyHits([hit])
    return
  }
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
    ...hit,
    hitCount: (current?.hitCount ?? 0) + 1
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
    snapshotPolicyCount: runtimeConfig.cacheDriver === 'redis' ? 0 : activePolicySnapshot.size,
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

export async function clearClientIpPolicyCacheLocal(): Promise<void> {
  policyCache.clear()
  activePolicySnapshot.clear()
  activePolicySnapshotLoadedAt = undefined
  await clearActivePolicySnapshotSharedCache()
  await clearPolicyByIpSharedCache()
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
  const blacklistPolicy = policy?.policyType === 'blacklist' ? policy : undefined
  const allowlistPolicy = policy?.policyType === 'allowlist' ? policy : undefined
  return {
    blocked: Boolean(blacklistPolicy),
    allowlisted: Boolean(allowlistPolicy),
    normalizedIp,
    blacklistPolicy,
    allowlistPolicy
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
  if (policy.expiresAt === undefined) return undefined
  const expiresAtMs = rfc3339InstantMilliseconds(policy.expiresAt)
  if (expiresAtMs === undefined) {
    throw new Error('Client-IP 策略 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间')
  }
  return expiresAtMs
}

function cloneActiveClientIpPolicy(policy: ActiveClientIpPolicy): ActiveClientIpPolicy {
  return {
    id: policy.id,
    ipHash: policy.ipHash,
    policyType: policy.policyType,
    aggregateIpKey: policy.aggregateIpKey,
    clientIp: policy.clientIp,
    reason: policy.reason,
    expiresAt: policy.expiresAt === undefined
      ? undefined
      : requiredRfc3339Instant(policy.expiresAt, 'Client-IP 策略 expiresAt')
  }
}

function clientIpPolicyHitInput(policy: ActiveClientIpPolicy, hitCount: number): ClientIpPolicyHitInput {
  return {
    ipHash: policy.ipHash,
    policyId: policy.id,
    hitCount,
    hitAt: new Date().toISOString()
  }
}

async function getActivePolicySnapshotSharedCacheEntry(): Promise<ClientIpPolicySnapshotCacheEntry | undefined> {
  const cached = await activePolicySnapshotSharedCache.get(activePolicySnapshotSharedCacheKey)
  if (!cached || !Array.isArray(cached.policies)) return undefined
  return {
    loadedAt: typeof cached.loadedAt === 'string' ? cached.loadedAt : new Date().toISOString(),
    policies: cached.policies
      .filter(isActiveClientIpPolicy)
      .map(cloneActiveClientIpPolicy)
  }
}

async function setActivePolicySnapshotSharedCacheEntry(entry: ClientIpPolicySnapshotCacheEntry): Promise<void> {
  await activePolicySnapshotSharedCache.set(activePolicySnapshotSharedCacheKey, {
    loadedAt: entry.loadedAt,
    policies: entry.policies.map(cloneActiveClientIpPolicy)
  }, { ttlMs: clientIpPolicyCacheTtlMs })
}

async function loadClientIpPolicySnapshotFromSharedCacheOrDatabase(): Promise<ClientIpPolicySnapshotCacheEntry> {
  const sharedSnapshot = await getActivePolicySnapshotSharedCacheEntry()
  if (sharedSnapshot) {
    return sharedSnapshot
  }
  return await loadClientIpPolicySnapshotFromDatabase()
}

async function loadClientIpPolicySnapshotFromDatabase(): Promise<ClientIpPolicySnapshotCacheEntry> {
  const policies = shouldUseStatsWriterBridge()
    ? await requestStatsWriter({ type: 'list_active_client_ip_policies' }, 1000)
    : await listActiveClientIpPoliciesAsync()
  const snapshot = snapshotCacheEntryFromPolicies(policies)
  await setActivePolicySnapshotSharedCacheEntry(snapshot)
  return snapshot
}

async function getClientIpPolicyByIpSharedCacheEntry(ipHash: string): Promise<ClientIpPolicyByIpCacheEntry | undefined> {
  const cached = await policyByIpSharedCache.get(ipHash)
  if (!cached) return undefined
  const policy = cached.policy && isActiveClientIpPolicy(cached.policy)
    ? cloneActiveClientIpPolicy(cached.policy)
    : undefined
  return {
    loadedAt: typeof cached.loadedAt === 'string' ? cached.loadedAt : new Date().toISOString(),
    ...(policy ? { policy } : {})
  }
}

async function setClientIpPolicyByIpSharedCacheEntry(
  ipHash: string,
  policy: ActiveClientIpPolicy | undefined,
  loadedAt = new Date().toISOString()
): Promise<void> {
  await policyByIpSharedCache.set(ipHash, {
    loadedAt,
    ...(policy ? { policy: cloneActiveClientIpPolicy(policy) } : {})
  }, { ttlMs: clientIpPolicyTtlMs(policy) })
}

async function loadClientIpPolicyByHashFromSharedCacheOrDatabase(ipHash: string): Promise<ClientIpPolicyByIpCacheEntry> {
  const sharedEntry = await getClientIpPolicyByIpSharedCacheEntry(ipHash)
  if (sharedEntry) {
    return sharedEntry
  }
  const policy = await loadClientIpPolicyByHashFromDatabase(ipHash)
  const loadedAt = new Date().toISOString()
  await setClientIpPolicyByIpSharedCacheEntry(ipHash, policy, loadedAt)
  return {
    loadedAt,
    ...(policy ? { policy } : {})
  }
}

async function loadClientIpPolicyByHashFromDatabase(ipHash: string): Promise<ActiveClientIpPolicy | undefined> {
  return shouldUseStatsWriterBridge()
    ? await requestStatsWriter({ type: 'find_active_client_ip_policy_by_hash', ipHash }, 1000)
    : await findActiveClientIpPolicyByHashAsync(ipHash)
}

function snapshotCacheEntryFromPolicies(policies: ActiveClientIpPolicy[]): ClientIpPolicySnapshotCacheEntry {
  return {
    loadedAt: new Date().toISOString(),
    policies: policies.map(cloneActiveClientIpPolicy)
  }
}

async function clearActivePolicySnapshotSharedCache(): Promise<void> {
  await activePolicySnapshotSharedCache.clear()
}

async function clearPolicyByIpSharedCache(): Promise<void> {
  await policyByIpSharedCache.clear()
}

function isActiveClientIpPolicy(value: unknown): value is ActiveClientIpPolicy {
  if (!value || typeof value !== 'object') return false
  const record = value as Record<string, unknown>
  return typeof record.id === 'string'
    && typeof record.ipHash === 'string'
    && (record.policyType === 'blacklist' || record.policyType === 'allowlist')
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
    await writeClientIpPolicyHits(hits)
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

async function writeClientIpPolicyHits(hits: ClientIpPolicyHitInput[]): Promise<void> {
  if (shouldUseStatsWriterBridge()) {
    await requestStatsWriter({ type: 'record_client_ip_policy_hits', hits }, 1000)
    return
  }
  await recordClientIpPolicyHitsAsync(hits)
}

function shouldUseStatsWriterBridge(): boolean {
  return runtimeConfig.processRole === 'server'
    || (runtimeConfig.processRole === 'worker' && runtimeConfig.workerRole !== 'stats-worker')
}
