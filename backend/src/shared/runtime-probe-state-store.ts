import { runtimeConfig } from '../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'

export interface RuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }> {
  get(runtimeKey: string): Promise<TState | undefined>
  set(state: TState, ttlMs: number): Promise<boolean>
  delete(runtimeKey: string): Promise<void>
  deleteGeneration(runtimeKey: string, generation: number): Promise<boolean>
  listDue(nowMs: number, limit: number): Promise<string[]>
  nextGeneration(runtimeKey: string, ttlMs: number): Promise<number>
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
  private readonly generations = new Map<string, number>()

  async get(runtimeKey: string): Promise<TState | undefined> {
    return this.freshEntry(runtimeKey)?.value
  }

  async set(state: TState, ttlMs: number): Promise<boolean> {
    const current = this.freshEntry(state.runtimeKey)?.value
    if (current && current.generation > state.generation) {
      return false
    }
    this.entries.set(state.runtimeKey, {
      value: state,
      expiresAtMs: Date.now() + normalizedTtlMs(ttlMs)
    })
    return true
  }

  async delete(runtimeKey: string): Promise<void> {
    this.entries.delete(runtimeKey)
  }

  async deleteGeneration(runtimeKey: string, generation: number): Promise<boolean> {
    const entry = this.freshEntry(runtimeKey)
    if (!entry || entry.value.generation !== generation) {
      return false
    }
    await this.delete(runtimeKey)
    return true
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

  private freshEntry(runtimeKey: string): MemoryProbeEntry<TState> | undefined {
    const entry = this.entries.get(runtimeKey)
    if (!entry) return undefined
    if (entry.expiresAtMs <= Date.now()) {
      this.entries.delete(runtimeKey)
      return undefined
    }
    return entry
  }
}

class RedisRuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }>
implements RuntimeProbeStateStore<TState> {
  private readonly statePrefix: string
  private readonly generationPrefix: string
  private readonly dueKey: string

  constructor(name: string) {
    const safeName = sanitizeRedisKeyPart(name)
    this.statePrefix = `juhe-ai:probe:${safeName}:state:`
    this.generationPrefix = `juhe-ai:probe:${safeName}:generation:`
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

  async set(state: TState, ttlMs: number): Promise<boolean> {
    const result = await (await this.client()).eval(redisSetProbeStateScript, {
      keys: [this.stateKey(state.runtimeKey), this.dueKey],
      arguments: [
        JSON.stringify(state),
        String(normalizedTtlMs(ttlMs)),
        String(Math.max(0, Math.trunc(state.nextProbeAtMs))),
        state.runtimeKey,
        String(Math.max(0, Math.trunc(state.generation)))
      ]
    })
    return numericRedisResult(result) === 1
  }

  async delete(runtimeKey: string): Promise<void> {
    await (await this.client()).eval(redisDeleteProbeStateScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [runtimeKey]
    })
  }

  async deleteGeneration(runtimeKey: string, generation: number): Promise<boolean> {
    const result = await (await this.client()).eval(redisDeleteProbeStateGenerationScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [
        runtimeKey,
        String(Math.max(0, Math.trunc(generation)))
      ]
    })
    return numericRedisResult(result) === 1
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
    return Math.max(1, numericRedisResult(result))
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
}

const redisSetProbeStateScript = `
local current = redis.call('GET', KEYS[1])
if current then
  local ok, decoded = pcall(cjson.decode, current)
  if ok and type(decoded) == 'table' then
    local current_generation = tonumber(decoded['generation'])
    local incoming_generation = tonumber(ARGV[5])
    if current_generation and incoming_generation and current_generation > incoming_generation then
      return 0
    end
  end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`

const redisDeleteProbeStateScript = `
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return 1
`

const redisDeleteProbeStateGenerationScript = `
local current = redis.call('GET', KEYS[1])
if not current then
  redis.call('ZREM', KEYS[2], ARGV[1])
  return 0
end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then
  redis.call('DEL', KEYS[1])
  redis.call('ZREM', KEYS[2], ARGV[1])
  return 1
end
local current_generation = tonumber(decoded['generation'])
local target_generation = tonumber(ARGV[2])
if current_generation and target_generation and current_generation == target_generation then
  redis.call('DEL', KEYS[1])
  redis.call('ZREM', KEYS[2], ARGV[1])
  return 1
end
return 0
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

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.trunc(value)
  if (typeof value === 'bigint') return Number(value)
  if (typeof value === 'string') {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? Math.trunc(parsed) : 0
  }
  return 0
}
