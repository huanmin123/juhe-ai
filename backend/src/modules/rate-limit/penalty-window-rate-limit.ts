export interface PenaltyWindowRateLimitRule {
  windowSeconds: number
  maxRequests: number
}

export interface PenaltyWindowRateLimitStore {
  name: string
  entries: Map<string, PenaltyWindowRateLimitEntry>
  maxEntries: number
  cleanupIntervalMs: number
  maxIdleMs: number
  maxPenaltyMs: number
  nextCleanupAtMs: number
}

export interface PenaltyWindowRateLimitDecision {
  allowed: boolean
  retryAfterSeconds?: number
  rule?: PenaltyWindowRateLimitRule
  storeName?: string
  limit?: number
}

interface PenaltyWindowRateLimitEntry {
  windowStartedAt: number
  count: number
  penaltyMs: number
  blockedUntilMs?: number
  lastSeenAtMs: number
}

interface InspectPenaltyWindowRateLimitBucket {
  allowed: boolean
  retryAfterSeconds: number
  commit: () => void
  rule: PenaltyWindowRateLimitRule
}

const defaultCleanupIntervalMs = 60_000
const defaultMaxEntries = 20_000
const defaultMaxIdleMs = 86_400_000
const defaultMaxPenaltyMs = 15 * 60_000

export function createPenaltyWindowRateLimitStore(input: {
  name: string
  maxEntries?: number
  cleanupIntervalMs?: number
  maxIdleMs?: number
  maxPenaltyMs?: number
}): PenaltyWindowRateLimitStore {
  return {
    name: input.name,
    entries: new Map(),
    maxEntries: positiveInteger(input.maxEntries, defaultMaxEntries),
    cleanupIntervalMs: positiveInteger(input.cleanupIntervalMs, defaultCleanupIntervalMs),
    maxIdleMs: positiveInteger(input.maxIdleMs, defaultMaxIdleMs),
    maxPenaltyMs: positiveInteger(input.maxPenaltyMs, defaultMaxPenaltyMs),
    nextCleanupAtMs: 0
  }
}

export function consumePenaltyWindowRateLimit(input: {
  store: PenaltyWindowRateLimitStore
  scopeKey: string
  rules: readonly PenaltyWindowRateLimitRule[]
  nowMs?: number
}): PenaltyWindowRateLimitDecision {
  const nowMs = input.nowMs ?? Date.now()
  cleanupPenaltyWindowRateLimitStore(input.store, nowMs)
  const buckets = input.rules
    .filter((rule) => rule.maxRequests > 0 && rule.windowSeconds > 0)
    .map((rule) => inspectPenaltyWindowRateLimitBucket(input.store, input.scopeKey, rule, nowMs))
  const blocked = buckets.find((bucket) => !bucket.allowed)
  if (blocked) {
    return {
      allowed: false,
      retryAfterSeconds: blocked.retryAfterSeconds,
      rule: blocked.rule,
      storeName: input.store.name,
      limit: blocked.rule.maxRequests
    }
  }

  for (const bucket of buckets) {
    bucket.commit()
  }
  return { allowed: true }
}

export function clearPenaltyWindowRateLimitStore(store: PenaltyWindowRateLimitStore): void {
  store.entries.clear()
  store.nextCleanupAtMs = 0
}

function inspectPenaltyWindowRateLimitBucket(
  store: PenaltyWindowRateLimitStore,
  scopeKey: string,
  rule: PenaltyWindowRateLimitRule,
  nowMs: number
): InspectPenaltyWindowRateLimitBucket {
  const windowMs = rule.windowSeconds * 1000
  const windowStartedAt = Math.floor(nowMs / windowMs) * windowMs
  const key = `${scopeKey}:${rule.windowSeconds}:${rule.maxRequests}`
  const current = store.entries.get(key)
  const entry = current && current.windowStartedAt === windowStartedAt
    ? current
    : {
        windowStartedAt,
        count: 0,
        penaltyMs: current?.penaltyMs ?? 0,
        blockedUntilMs: current?.blockedUntilMs,
        lastSeenAtMs: nowMs
      }
  entry.lastSeenAtMs = nowMs

  if (entry.blockedUntilMs && entry.blockedUntilMs > nowMs) {
    openPenaltyBlock(store, entry, windowMs, nowMs)
    store.entries.set(key, entry)
    return blockedBucket(rule, entry.blockedUntilMs - nowMs)
  }

  entry.blockedUntilMs = undefined
  if (entry.count >= rule.maxRequests) {
    openPenaltyBlock(store, entry, windowMs, nowMs)
    store.entries.set(key, entry)
    return blockedBucket(rule, (entry.blockedUntilMs ?? nowMs) - nowMs)
  }

  return {
    allowed: true,
    retryAfterSeconds: 0,
    rule,
    commit: () => {
      entry.count += 1
      entry.lastSeenAtMs = nowMs
      store.entries.set(key, entry)
      trimPenaltyWindowRateLimitStore(store, nowMs)
    }
  }
}

function openPenaltyBlock(
  store: PenaltyWindowRateLimitStore,
  entry: PenaltyWindowRateLimitEntry,
  windowMs: number,
  nowMs: number
): void {
  const maxPenaltyMs = Math.max(windowMs, store.maxPenaltyMs)
  const basePenaltyMs = entry.penaltyMs > 0 ? entry.penaltyMs * 2 : windowMs
  entry.penaltyMs = Math.min(maxPenaltyMs, basePenaltyMs)
  entry.blockedUntilMs = nowMs + entry.penaltyMs
}

function blockedBucket(rule: PenaltyWindowRateLimitRule, retryAfterMs: number): InspectPenaltyWindowRateLimitBucket {
  return {
    allowed: false,
    retryAfterSeconds: Math.max(1, Math.ceil(retryAfterMs / 1000)),
    commit: () => {},
    rule
  }
}

function cleanupPenaltyWindowRateLimitStore(store: PenaltyWindowRateLimitStore, nowMs: number): void {
  if (store.nextCleanupAtMs > nowMs && store.entries.size <= store.maxEntries) {
    return
  }
  for (const [key, entry] of store.entries) {
    if (entry.blockedUntilMs && entry.blockedUntilMs > nowMs) {
      continue
    }
    if (nowMs - entry.lastSeenAtMs > store.maxIdleMs) {
      store.entries.delete(key)
    }
  }
  store.nextCleanupAtMs = nowMs + store.cleanupIntervalMs
  trimPenaltyWindowRateLimitStore(store, nowMs)
}

function trimPenaltyWindowRateLimitStore(store: PenaltyWindowRateLimitStore, nowMs: number): void {
  if (store.entries.size <= store.maxEntries) {
    return
  }
  const targetSize = Math.floor(store.maxEntries * 0.9)
  for (const [key, entry] of store.entries) {
    if ((!entry.blockedUntilMs || entry.blockedUntilMs <= nowMs) || store.entries.size > targetSize) {
      store.entries.delete(key)
    }
    if (store.entries.size <= targetSize) {
      break
    }
  }
}

function positiveInteger(value: number | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : fallback
}
