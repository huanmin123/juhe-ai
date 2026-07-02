import { runtimeConfig } from '../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'

export interface RuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }> {
  get(runtimeKey: string): Promise<TState | undefined>
  set(state: TState, ttlMs: number): Promise<void>
  delete(runtimeKey: string): Promise<void>
  listDue(nowMs: number, limit: number): Promise<string[]>
  nextGeneration(runtimeKey: string, ttlMs: number): Promise<number>
  acquireLock(runtimeKey: string, token: string, ttlMs: number): Promise<boolean>
  releaseLock(runtimeKey: string, token: string): Promise<void>
}

interface MemoryProbeEntry<TState> {
  value: TState
  expiresAtMs: number
}

const memoryProbeStores = new Map<string, MemoryRuntimeProbeStateStore<never>>()

export function createRuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }>(
  name: string
): RuntimeProbeStateStore<TState> {
  if (runtimeConfig.runtimeStateDriver === 'redis') return new RedisRuntimeProbeStateStore<TState>(name)
  const existing = memoryProbeStores.get(name) as MemoryRuntimeProbeStateStore<TState> | undefined
  if (existing) return existing
  const store = new MemoryRuntimeProbeStateStore<TState>()
  memoryProbeStores.set(name, store as MemoryRuntimeProbeStateStore<never>)
  return store
}

class MemoryRuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }>
implements RuntimeProbeStateStore<TState> {
  private readonly entries = new Map<string, MemoryProbeEntry<TState>>()
  private readonly locks = new Map<string, { token: string; expiresAtMs: number }>()
  private readonly generations = new Map<string, number>()

  async get(runtimeKey: string): Promise<TState | undefined> {
    return this.freshEntry(runtimeKey)?.value
  }

  async set(state: TState, ttlMs: number): Promise<void> {
    this.entries.set(state.runtimeKey, {
      value: state,
      expiresAtMs: Date.now() + normalizedTtlMs(ttlMs)
    })
  }

  async delete(runtimeKey: string): Promise<void> {
    this.entries.delete(runtimeKey)
    this.locks.delete(runtimeKey)
  }

  async listDue(nowMs: number, limit: number): Promise<string[]> {
    const output: string[] = []
    for (const [runtimeKey, entry] of this.entries) {
      if (output.length >= Math.max(1, Math.trunc(limit))) break
      if (!this.freshEntry(runtimeKey)) continue
      if (entry.value.nextProbeAtMs <= nowMs) output.push(runtimeKey)
    }
    return output
  }

  async nextGeneration(runtimeKey: string, _ttlMs: number): Promise<number> {
    const next = (this.generations.get(runtimeKey) ?? 0) + 1
    this.generations.set(runtimeKey, next)
    return next
  }

  async acquireLock(runtimeKey: string, token: string, ttlMs: number): Promise<boolean> {
    this.clearExpiredLock(runtimeKey)
    if (this.locks.has(runtimeKey)) return false
    this.locks.set(runtimeKey, {
      token,
      expiresAtMs: Date.now() + normalizedTtlMs(ttlMs)
    })
    return true
  }

  async releaseLock(runtimeKey: string, token: string): Promise<void> {
    this.clearExpiredLock(runtimeKey)
    const lock = this.locks.get(runtimeKey)
    if (lock?.token === token) {
      this.locks.delete(runtimeKey)
    }
  }

  private freshEntry(runtimeKey: string): MemoryProbeEntry<TState> | undefined {
    const entry = this.entries.get(runtimeKey)
    if (!entry) return undefined
    if (entry.expiresAtMs <= Date.now()) {
      this.entries.delete(runtimeKey)
      return undefined
    }
    return entry
  }

  private clearExpiredLock(runtimeKey: string): void {
    const lock = this.locks.get(runtimeKey)
    if (lock && lock.expiresAtMs <= Date.now()) {
      this.locks.delete(runtimeKey)
    }
  }
}

class RedisRuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }>
implements RuntimeProbeStateStore<TState> {
  private readonly statePrefix: string
  private readonly generationPrefix: string
  private readonly lockPrefix: string
  private readonly dueKey: string

  constructor(name: string) {
    const safeName = sanitizeRedisKeyPart(name)
    this.statePrefix = `juhe-ai:probe:${safeName}:state:`
    this.generationPrefix = `juhe-ai:probe:${safeName}:generation:`
    this.lockPrefix = `juhe-ai:probe:${safeName}:lock:`
    this.dueKey = `juhe-ai:probe:${safeName}:due`
  }

  async get(runtimeKey: string): Promise<TState | undefined> {
    const rawValue = await (await this.client()).get(this.stateKey(runtimeKey))
    if (rawValue === null) return undefined
    try {
      return JSON.parse(rawValue) as TState
    } catch {
      await this.delete(runtimeKey)
      return undefined
    }
  }

  async set(state: TState, ttlMs: number): Promise<void> {
    await (await this.client()).eval(redisSetProbeStateScript, {
      keys: [this.stateKey(state.runtimeKey), this.dueKey],
      arguments: [
        JSON.stringify(state),
        String(normalizedTtlMs(ttlMs)),
        String(Math.max(0, Math.trunc(state.nextProbeAtMs))),
        state.runtimeKey
      ]
    })
  }

  async delete(runtimeKey: string): Promise<void> {
    await (await this.client()).eval(redisDeleteProbeStateScript, {
      keys: [this.stateKey(runtimeKey), this.lockKey(runtimeKey), this.dueKey],
      arguments: [runtimeKey]
    })
  }

  async listDue(nowMs: number, limit: number): Promise<string[]> {
    const result = await (await this.client()).eval(redisListDueProbeStatesScript, {
      keys: [this.dueKey],
      arguments: [
        String(Math.max(0, Math.trunc(nowMs))),
        String(Math.max(1, Math.trunc(limit)))
      ]
    })
    return stringArrayRedisResult(result)
  }

  async nextGeneration(runtimeKey: string, ttlMs: number): Promise<number> {
    const result = await (await this.client()).eval(redisNextProbeGenerationScript, {
      keys: [this.generationKey(runtimeKey)],
      arguments: [String(normalizedTtlMs(ttlMs))]
    })
    return typeof result === 'number' && Number.isFinite(result) ? Math.max(1, Math.trunc(result)) : 1
  }

  async acquireLock(runtimeKey: string, token: string, ttlMs: number): Promise<boolean> {
    const result = await (await this.client()).set(this.lockKey(runtimeKey), token, {
      PX: normalizedTtlMs(ttlMs),
      NX: true
    })
    return result === 'OK'
  }

  async releaseLock(runtimeKey: string, token: string): Promise<void> {
    await (await this.client()).eval(redisReleaseProbeLockScript, {
      keys: [this.lockKey(runtimeKey)],
      arguments: [token]
    })
  }

  private client(): Promise<RedisCommandClient> {
    const redisUrl = runtimeConfig.redis.stateUrl
    if (!redisUrl) {
      throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
    }
    return getRedisClient(redisUrl)
  }

  private stateKey(runtimeKey: string): string {
    return `${this.statePrefix}${sanitizeRedisKeyPart(runtimeKey)}`
  }

  private generationKey(runtimeKey: string): string {
    return `${this.generationPrefix}${sanitizeRedisKeyPart(runtimeKey)}`
  }

  private lockKey(runtimeKey: string): string {
    return `${this.lockPrefix}${sanitizeRedisKeyPart(runtimeKey)}`
  }
}

const redisSetProbeStateScript = `
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`

const redisDeleteProbeStateScript = `
redis.call('DEL', KEYS[1])
redis.call('DEL', KEYS[2])
redis.call('ZREM', KEYS[3], ARGV[1])
return 1
`

const redisListDueProbeStatesScript = `
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', 0)
return redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
`

const redisNextProbeGenerationScript = `
local generation = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[1])
return generation
`

const redisReleaseProbeLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

function normalizedTtlMs(ttlMs: number): number {
  return Number.isFinite(ttlMs) ? Math.max(1, Math.trunc(ttlMs)) : 1
}

function sanitizeRedisKeyPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'default'
}

function stringArrayRedisResult(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((item) => typeof item === 'string' && item.trim() ? [item] : [])
}
