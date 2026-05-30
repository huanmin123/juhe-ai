import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import {
  listActiveClientIpPolicies,
  normalizeClientIpForStats,
  recordClientIpPolicyHits,
  type ActiveClientIpPolicy,
  type ClientIpPolicyHitInput
} from '../../storage/client-ip-stats.repository.js'
import { requestDbService } from '../db-service/db-service-ipc.js'

export interface ClientIpPolicyDecision {
  blocked: boolean
  normalizedIp?: ReturnType<typeof normalizeClientIpForStats>
  blacklistPolicy?: ActiveClientIpPolicy
}

interface InspectClientIpPolicyOptions {
  cacheOnly?: boolean
}

const clientIpPolicyCacheTtlMs = 30_000
const clientIpPolicyHitFlushDelayMs = 1000
let policyCache: {
  expiresAtMs: number
  policiesByIpHash: Map<string, ActiveClientIpPolicy[]>
} | undefined
let pendingPolicyLoad: Promise<Map<string, ActiveClientIpPolicy[]>> | undefined
const pendingPolicyHits = new Map<string, ClientIpPolicyHitInput>()
let policyHitFlushTimer: NodeJS.Timeout | undefined

export async function inspectClientIpPolicy(clientIp?: string, options: InspectClientIpPolicyOptions = {}): Promise<ClientIpPolicyDecision> {
  const normalizedIp = normalizeClientIpForStats(clientIp)
  if (!normalizedIp) {
    return { blocked: false }
  }
  const policies = (await readClientIpPolicyMap(options)).get(normalizedIp.ipHash) ?? []
  const blacklistPolicy = policies.find((policy) => isPolicyActiveAt(policy, Date.now()))
  return {
    blocked: Boolean(blacklistPolicy),
    normalizedIp,
    blacklistPolicy
  }
}

export function primeClientIpPolicyCacheLocal(policies: ActiveClientIpPolicy[]): void {
  policyCache = buildClientIpPolicyCache(policies)
}

export async function refreshClientIpPolicyCacheLocal(): Promise<void> {
  await loadClientIpPolicyMap()
}

export function recordClientIpPolicyHitAsync(policy: ActiveClientIpPolicy): void {
  const key = `${policy.ipHash}:${policy.id}`
  const current = pendingPolicyHits.get(key)
  pendingPolicyHits.set(key, {
    ipHash: policy.ipHash,
    policyId: policy.id,
    hitCount: (current?.hitCount ?? 0) + 1,
    hitAt: new Date().toISOString()
  })
  if (policyHitFlushTimer) return
  policyHitFlushTimer = setTimeout(() => {
    policyHitFlushTimer = undefined
    void flushClientIpPolicyHits()
  }, clientIpPolicyHitFlushDelayMs)
  policyHitFlushTimer.unref?.()
}

export function clearClientIpPolicyCacheLocal(): void {
  policyCache = undefined
  pendingPolicyLoad = undefined
}

async function readClientIpPolicyMap(options: InspectClientIpPolicyOptions = {}): Promise<Map<string, ActiveClientIpPolicy[]>> {
  const nowMs = Date.now()
  if (policyCache && policyCache.expiresAtMs > nowMs) {
    return policyCache.policiesByIpHash
  }
  if (options.cacheOnly) {
    return new Map()
  }
  if (pendingPolicyLoad) {
    return pendingPolicyLoad
  }
  pendingPolicyLoad = loadClientIpPolicyMap()
  try {
    return await pendingPolicyLoad
  } finally {
    pendingPolicyLoad = undefined
  }
}

async function loadClientIpPolicyMap(): Promise<Map<string, ActiveClientIpPolicy[]>> {
  try {
    const policies = runtimeConfig.processRole === 'server'
      ? await requestDbService({ type: 'list_active_client_ip_policies' }, { timeoutMs: 1000 })
      : listActiveClientIpPolicies()
    policyCache = buildClientIpPolicyCache(policies)
    return policyCache.policiesByIpHash
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'client_ip_policy_cache_load_failed'
    }), 'IP 策略缓存加载失败')
    if (policyCache) {
      return policyCache.policiesByIpHash
    }
    return new Map()
  }
}

function isPolicyActiveAt(policy: ActiveClientIpPolicy, nowMs: number): boolean {
  const expiresAtMs = policyExpiresAtTime(policy)
  return expiresAtMs === undefined || expiresAtMs > nowMs
}

function buildClientIpPolicyCache(policies: ActiveClientIpPolicy[]): NonNullable<typeof policyCache> {
  const policiesByIpHash = new Map<string, ActiveClientIpPolicy[]>()
  const now = Date.now()
  let expiresAtMs = now + clientIpPolicyCacheTtlMs
  for (const policy of policies) {
    const policyExpiresAtMs = policyExpiresAtTime(policy)
    if (policyExpiresAtMs !== undefined) {
      if (policyExpiresAtMs <= now) continue
      expiresAtMs = Math.min(expiresAtMs, policyExpiresAtMs)
    }
    const rows = policiesByIpHash.get(policy.ipHash)
    if (rows) {
      rows.push(policy)
    } else {
      policiesByIpHash.set(policy.ipHash, [policy])
    }
  }
  return {
    expiresAtMs,
    policiesByIpHash
  }
}

function policyExpiresAtTime(policy: ActiveClientIpPolicy): number | undefined {
  if (!policy.expiresAt) return undefined
  const expiresAtMs = Date.parse(policy.expiresAt)
  return Number.isFinite(expiresAtMs) ? expiresAtMs : undefined
}

async function flushClientIpPolicyHits(): Promise<void> {
  if (pendingPolicyHits.size === 0) return
  const hits = [...pendingPolicyHits.values()]
  pendingPolicyHits.clear()
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
  }
}
