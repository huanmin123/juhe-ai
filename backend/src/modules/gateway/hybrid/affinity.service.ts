import { createHash } from 'node:crypto'

import type { Request } from 'express'

import type {
  ApiKeyHybridLevelRoute,
  ApiKeyHybridRoutingConfig
} from '../../../domain/types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { createRuntimeStateStore } from '../../../shared/runtime-state-store.js'

interface HybridRouteAffinityBinding {
  route: ApiKeyHybridLevelRoute
  lastLevel: number
  lowCount: number
}

interface MemoryHybridRouteAffinityEntry {
  value: HybridRouteAffinityBinding
  expiresAt: number
}

export interface HybridRouteAffinityDecision {
  route: ApiKeyHybridLevelRoute
  applied: boolean
  reason?: string
  previousModel?: string
  lowCount?: number
}

const hybridRouteAffinityMaxEntries = 10_000
const hybridRouteAffinityMaxTtlMs = 24 * 60 * 60 * 1000

const hybridRouteAffinityMemoryBindings = new Map<string, MemoryHybridRouteAffinityEntry>()
const hybridRouteAffinityStateStore = createRuntimeStateStore('gateway-hybrid-route-affinity')

export function applyHybridRouteAffinity(input: {
  req: Request
  systemAccountId: string
  apiKeyId?: string
  config: ApiKeyHybridRoutingConfig
  level: number
  route: ApiKeyHybridLevelRoute
}): HybridRouteAffinityDecision {
  const sessionKey = hybridRouteAffinityKey(input.req, input.systemAccountId, input.apiKeyId)
  if (!sessionKey || input.config.cacheAffinityEnabled === false || input.config.affinityTtlSeconds <= 0) {
    return { route: input.route, applied: false }
  }
  const previous = getMemoryHybridRouteAffinityBinding(sessionKey)
  const decision = previous
    ? applyAffinityDecision({
      previous,
      level: input.level,
      route: input.route,
      config: input.config
    })
    : { route: input.route, applied: false }
  rememberHybridRouteAffinity(sessionKey, {
    route: decision.route,
    lastLevel: input.level,
    lowCount: decision.lowCount ?? 0
  }, input.config.affinityTtlSeconds)
  return decision
}

export function clearHybridRouteAffinityForTest(): void {
  hybridRouteAffinityMemoryBindings.clear()
}

export async function applyHybridRouteAffinityAsync(input: {
  req: Request
  systemAccountId: string
  apiKeyId?: string
  config: ApiKeyHybridRoutingConfig
  level: number
  route: ApiKeyHybridLevelRoute
}): Promise<HybridRouteAffinityDecision> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return applyHybridRouteAffinity(input)
  }
  const sessionKey = hybridRouteAffinityKey(input.req, input.systemAccountId, input.apiKeyId)
  if (!sessionKey || input.config.cacheAffinityEnabled === false || input.config.affinityTtlSeconds <= 0) {
    return { route: input.route, applied: false }
  }
  const previous = await hybridRouteAffinityStateStore.getJson<HybridRouteAffinityBinding>(hybridRouteAffinityStateKey(sessionKey))
  const decision = previous
    ? applyAffinityDecision({
      previous,
      level: input.level,
      route: input.route,
      config: input.config
    })
    : { route: input.route, applied: false }
  await rememberHybridRouteAffinityAsync(sessionKey, {
    route: decision.route,
    lastLevel: input.level,
    lowCount: decision.lowCount ?? 0
  }, input.config.affinityTtlSeconds)
  return decision
}

function applyAffinityDecision(input: {
  previous: HybridRouteAffinityBinding
  level: number
  route: ApiKeyHybridLevelRoute
  config: ApiKeyHybridRoutingConfig
}): HybridRouteAffinityDecision {
  if (input.previous.route.targetModel === input.route.targetModel) {
    return {
      route: input.route,
      applied: false,
      lowCount: 0
    }
  }
  if (input.route.maxLevel < input.previous.route.minLevel) {
    const lowCount = input.previous.lowCount + 1
    if (lowCount < input.config.downgradeConsecutiveLowCount) {
      return {
        route: input.previous.route,
        applied: true,
        reason: 'downgrade_requires_consecutive_low_scores',
        previousModel: input.previous.route.targetModel,
        lowCount
      }
    }
    return {
      route: input.route,
      applied: false,
      lowCount: 0
    }
  }
  const levelDelta = Math.abs(input.level - input.previous.lastLevel)
  if (levelDelta < input.config.switchMinLevelDelta) {
    return {
      route: input.previous.route,
      applied: true,
      reason: 'level_delta_below_threshold',
      previousModel: input.previous.route.targetModel,
      lowCount: input.previous.lowCount
    }
  }
  return {
    route: input.route,
    applied: false,
    lowCount: 0
  }
}

function rememberHybridRouteAffinity(
  key: string,
  binding: HybridRouteAffinityBinding,
  ttlSeconds: number
): void {
  const ttlMs = Math.min(hybridRouteAffinityMaxTtlMs, Math.max(1, Math.trunc(ttlSeconds)) * 1000)
  setMemoryHybridRouteAffinityBinding(key, binding, ttlMs)
}

async function rememberHybridRouteAffinityAsync(
  key: string,
  binding: HybridRouteAffinityBinding,
  ttlSeconds: number
): Promise<void> {
  const ttlMs = Math.min(hybridRouteAffinityMaxTtlMs, Math.max(1, Math.trunc(ttlSeconds)) * 1000)
  await hybridRouteAffinityStateStore.setJson(hybridRouteAffinityStateKey(key), binding, ttlMs)
}

function getMemoryHybridRouteAffinityBinding(key: string): HybridRouteAffinityBinding | undefined {
  const entry = hybridRouteAffinityMemoryBindings.get(key)
  if (!entry) {
    return undefined
  }
  if (entry.expiresAt <= Date.now()) {
    hybridRouteAffinityMemoryBindings.delete(key)
    return undefined
  }
  return entry.value
}

function setMemoryHybridRouteAffinityBinding(key: string, value: HybridRouteAffinityBinding, ttlMs: number): void {
  hybridRouteAffinityMemoryBindings.set(key, {
    value,
    expiresAt: Date.now() + Math.max(1, Math.trunc(ttlMs))
  })
  while (hybridRouteAffinityMemoryBindings.size > hybridRouteAffinityMaxEntries) {
    const oldestKey = hybridRouteAffinityMemoryBindings.keys().next().value
    if (typeof oldestKey !== 'string') {
      return
    }
    hybridRouteAffinityMemoryBindings.delete(oldestKey)
  }
}

function hybridRouteAffinityStateKey(key: string): string {
  return `session:${key}`
}

function hybridRouteAffinityKey(req: Request, systemAccountId: string, apiKeyId?: string): string | undefined {
  const session = extractHybridSessionIdentity(req)
  if (!session) {
    return undefined
  }
  return createHash('sha256')
    .update(JSON.stringify({
      systemAccountId,
      apiKeyId: apiKeyId ?? 'internal',
      session
    }))
    .digest('hex')
}

function extractHybridSessionIdentity(req: Request): { source: string; value: string } | undefined {
  for (const name of sessionHeaderNames) {
    const value = stringValue(req.header(name))
    if (value) {
      return { source: `header:${name.toLowerCase()}`, value }
    }
  }
  for (const path of sessionBodyPaths) {
    const value = stringValue(valueAtPath(req.body, path))
    if (value) {
      return { source: `body:${path.join('.')}`, value }
    }
  }
  return undefined
}

function valueAtPath(value: unknown, path: string[]): unknown {
  let current = value
  for (const key of path) {
    if (typeof current !== 'object' || current === null) {
      return undefined
    }
    current = (current as Record<string, unknown>)[key]
  }
  return current
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

const sessionHeaderNames = [
  'session_id',
  'session-id',
  'x-session-id',
  'conversation_id',
  'conversation-id',
  'x-conversation-id',
  'prompt_cache_key',
  'x-prompt-cache-key',
  'previous_response_id',
  'previous-response-id',
  'x-previous-response-id',
  'x-client-request-id'
]

const sessionBodyPaths = [
  ['previous_response_id'],
  ['session_id'],
  ['conversation_id'],
  ['prompt_cache_key'],
  ['metadata', 'session_id'],
  ['metadata', 'conversation_id'],
  ['metadata', 'user_id']
]
