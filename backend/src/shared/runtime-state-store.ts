import { runtimeConfig } from '../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'
import { redisNamespacedKey } from './redis-namespace.js'

export type RuntimeStateKey = string

export interface RuntimeStateStore {
  getJson<T>(key: RuntimeStateKey): Promise<T | undefined>
  getJsonMany<T>(keys: RuntimeStateKey[]): Promise<Array<T | undefined>>
  getDeleteJson<T>(key: RuntimeStateKey): Promise<T | undefined>
  setJson<T>(key: RuntimeStateKey, value: T, ttlMs: number): Promise<void>
  compareSetJson<T>(
    key: RuntimeStateKey,
    expectedValue: T | undefined,
    nextValue: T,
    ttlMs: number
  ): Promise<boolean>
  compareDeleteJson<T>(key: RuntimeStateKey, expectedValue: T): Promise<boolean>
  delete(key: RuntimeStateKey): Promise<void>
  incr(key: RuntimeStateKey, options: { ttlMs: number; max?: number }): Promise<number>
  acquireLock(key: RuntimeStateKey, options: { ttlMs: number; token: string }): Promise<boolean>
  releaseLock(key: RuntimeStateKey, token: string): Promise<void>
}

interface RuntimeStateEntry {
  value: unknown
  expiresAt: number
}

const memoryStores = new Map<string, MemoryRuntimeStateStore>()

export function createRuntimeStateStore(name: string): RuntimeStateStore {
  if (runtimeConfig.runtimeStateDriver === 'redis') return new RedisRuntimeStateStore(name)
  const existing = memoryStores.get(name)
  if (existing) return existing
  const store = new MemoryRuntimeStateStore()
  memoryStores.set(name, store)
  return store
}

class MemoryRuntimeStateStore implements RuntimeStateStore {
  private readonly entries = new Map<RuntimeStateKey, RuntimeStateEntry>()

  async getJson<T>(key: RuntimeStateKey): Promise<T | undefined> {
    const entry = this.getFreshEntry(key)
    return entry?.value as T | undefined
  }

  async getJsonMany<T>(keys: RuntimeStateKey[]): Promise<Array<T | undefined>> {
    return keys.map((key) => this.getFreshEntry(key)?.value as T | undefined)
  }

  async getDeleteJson<T>(key: RuntimeStateKey): Promise<T | undefined> {
    const entry = this.getFreshEntry(key)
    this.entries.delete(key)
    return entry?.value as T | undefined
  }

  async setJson<T>(key: RuntimeStateKey, value: T, ttlMs: number): Promise<void> {
    this.entries.set(key, {
      value,
      expiresAt: expiresAtFromTtl(ttlMs)
    })
  }

  async compareSetJson<T>(
    key: RuntimeStateKey,
    expectedValue: T | undefined,
    nextValue: T,
    ttlMs: number
  ): Promise<boolean> {
    const current = this.getFreshEntry(key)
    if (expectedValue === undefined) {
      if (current) return false
    } else if (!current || JSON.stringify(current.value) !== JSON.stringify(expectedValue)) {
      return false
    }
    this.entries.set(key, {
      value: nextValue,
      expiresAt: expiresAtFromTtl(ttlMs)
    })
    return true
  }

  async compareDeleteJson<T>(key: RuntimeStateKey, expectedValue: T): Promise<boolean> {
    const current = this.getFreshEntry(key)
    if (!current || JSON.stringify(current.value) !== JSON.stringify(expectedValue)) {
      return false
    }
    this.entries.delete(key)
    return true
  }

  async delete(key: RuntimeStateKey): Promise<void> {
    this.entries.delete(key)
  }

  async incr(key: RuntimeStateKey, options: { ttlMs: number; max?: number }): Promise<number> {
    const entry = this.getFreshEntry(key)
    const current = typeof entry?.value === 'number' && Number.isFinite(entry.value) ? entry.value : 0
    const next = current + 1
    if (options.max !== undefined && next > options.max) {
      return next
    }
    this.entries.set(key, {
      value: next,
      expiresAt: entry?.expiresAt ?? expiresAtFromTtl(options.ttlMs)
    })
    return next
  }

  async acquireLock(key: RuntimeStateKey, options: { ttlMs: number; token: string }): Promise<boolean> {
    if (this.getFreshEntry(key)) {
      return false
    }
    this.entries.set(key, {
      value: options.token,
      expiresAt: expiresAtFromTtl(options.ttlMs)
    })
    return true
  }

  async releaseLock(key: RuntimeStateKey, token: string): Promise<void> {
    const entry = this.getFreshEntry(key)
    if (entry?.value === token) {
      this.entries.delete(key)
    }
  }

  private getFreshEntry(key: RuntimeStateKey): RuntimeStateEntry | undefined {
    const entry = this.entries.get(key)
    if (!entry) return undefined
    if (entry.expiresAt <= Date.now()) {
      this.entries.delete(key)
      return undefined
    }
    return entry
  }
}

function expiresAtFromTtl(ttlMs: number): number {
  const normalizedTtlMs = Number.isFinite(ttlMs) ? Math.max(1, Math.trunc(ttlMs)) : 1
  return Date.now() + normalizedTtlMs
}

class RedisRuntimeStateStore implements RuntimeStateStore {
  private readonly keyPrefix: string

  constructor(name: string) {
    const redisUrl = runtimeConfig.redis.stateUrl
    if (!redisUrl) {
      throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
    }
    this.keyPrefix = redisNamespacedKey(`juhe-ai:state:${sanitizeRedisKeyPart(name)}:`)
  }

  async getJson<T>(key: RuntimeStateKey): Promise<T | undefined> {
    const rawValue = await (await this.client()).get(this.redisKey(key))
    if (rawValue === null) return undefined
    try {
      return JSON.parse(rawValue) as T
    } catch {
      await this.delete(key)
      return undefined
    }
  }

  async getJsonMany<T>(keys: RuntimeStateKey[]): Promise<Array<T | undefined>> {
    if (!keys.length) return []
    const redisKeys = keys.map((key) => this.redisKey(key))
    const rawValues = await (await this.client()).sendCommand(['MGET', ...redisKeys])
    const values = Array.isArray(rawValues) ? rawValues : []
    const malformedKeys: string[] = []
    const output = redisKeys.map((redisKey, index) => {
      const rawValue = values[index]
      if (typeof rawValue !== 'string') return undefined
      try {
        return JSON.parse(rawValue) as T
      } catch {
        malformedKeys.push(redisKey)
        return undefined
      }
    })
    if (malformedKeys.length) {
      await (await this.client()).sendCommand(['DEL', ...malformedKeys])
    }
    return output
  }

  async getDeleteJson<T>(key: RuntimeStateKey): Promise<T | undefined> {
    const rawValue = await (await this.client()).sendCommand(['GETDEL', this.redisKey(key)])
    if (typeof rawValue !== 'string') return undefined
    try {
      return JSON.parse(rawValue) as T
    } catch {
      return undefined
    }
  }

  async setJson<T>(key: RuntimeStateKey, value: T, ttlMs: number): Promise<void> {
    await (await this.client()).set(
      this.redisKey(key),
      JSON.stringify(value),
      { PX: normalizeTtlMs(ttlMs) }
    )
  }

  async compareSetJson<T>(
    key: RuntimeStateKey,
    expectedValue: T | undefined,
    nextValue: T,
    ttlMs: number
  ): Promise<boolean> {
    const result = await (await this.client()).eval(compareSetJsonScript, {
      keys: [this.redisKey(key)],
      arguments: [
        expectedValue === undefined ? '' : JSON.stringify(expectedValue),
        JSON.stringify(nextValue),
        String(normalizeTtlMs(ttlMs))
      ]
    })
    return numericRedisResult(result) === 1
  }

  async compareDeleteJson<T>(key: RuntimeStateKey, expectedValue: T): Promise<boolean> {
    const result = await (await this.client()).eval(compareDeleteJsonScript, {
      keys: [this.redisKey(key)],
      arguments: [JSON.stringify(expectedValue)]
    })
    return numericRedisResult(result) === 1
  }

  async delete(key: RuntimeStateKey): Promise<void> {
    await (await this.client()).del(this.redisKey(key))
  }

  async incr(key: RuntimeStateKey, options: { ttlMs: number; max?: number }): Promise<number> {
    const result = await (await this.client()).eval(incrWithMaxScript, {
      keys: [this.redisKey(key)],
      arguments: [
        String(normalizeTtlMs(options.ttlMs)),
        options.max === undefined ? '' : String(normalizeNonNegativeInteger(options.max))
      ]
    })
    return numericRedisResult(result)
  }

  async acquireLock(key: RuntimeStateKey, options: { ttlMs: number; token: string }): Promise<boolean> {
    const result = await (await this.client()).set(
      this.redisKey(key),
      options.token,
      { PX: normalizeTtlMs(options.ttlMs), NX: true }
    )
    return result === 'OK'
  }

  async releaseLock(key: RuntimeStateKey, token: string): Promise<void> {
    await (await this.client()).eval(releaseLockScript, {
      keys: [this.redisKey(key)],
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

  private redisKey(key: RuntimeStateKey): string {
    return `${this.keyPrefix}${key}`
  }
}

const incrWithMaxScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0') or 0
local next_value = current + 1
if ARGV[2] ~= '' and next_value > tonumber(ARGV[2]) then
  return next_value
end
if current == 0 then
  redis.call('SET', KEYS[1], tostring(next_value), 'PX', ARGV[1])
else
  redis.call('INCR', KEYS[1])
  if redis.call('PTTL', KEYS[1]) < 0 then
    redis.call('PEXPIRE', KEYS[1], ARGV[1])
  end
end
return next_value
`

const compareSetJsonScript = `
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '' then
  if current then
    return 0
  end
elseif current ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1
`

const compareDeleteJsonScript = `
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`

const releaseLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

function sanitizeRedisKeyPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'default'
}

function normalizeTtlMs(ttlMs: number): number {
  return Number.isFinite(ttlMs) ? Math.max(1, Math.trunc(ttlMs)) : 1
}

function normalizeNonNegativeInteger(value: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}
