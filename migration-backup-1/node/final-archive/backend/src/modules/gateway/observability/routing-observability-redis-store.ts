import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import {
  gatewayRoutingObservationMetricKey,
  gatewayRoutingObservabilityMetricCapacity,
  type GatewayRoutingObservation,
  type GatewayRoutingObservationBatchEntry,
  type GatewayRoutingObservabilitySnapshot,
  type GatewayRoutingObservabilityStore
} from './routing-observability-store.js'

export class RedisGatewayRoutingObservabilityStore implements GatewayRoutingObservabilityStore {
  private readonly key: string

  constructor(private readonly redisUrl: string, name = 'gateway-routing-observability') {
    if (!redisUrl.trim()) throw new Error('performance routing observability 缺少 Redis URL')
    this.key = redisNamespacedKey(`juhe-ai:${safeName(name)}:v1`)
  }

  async record(observation: GatewayRoutingObservation, nowMs = Date.now()): Promise<void> {
    await this.recordBatch([{ observation, count: 1 }], nowMs)
  }

  async recordBatch(entries: readonly GatewayRoutingObservationBatchEntry[], nowMs = Date.now()): Promise<void> {
    if (entries.length === 0) return
    const now = normalizedNow(nowMs)
    const argumentsList = [String(now), String(gatewayRoutingObservabilityMetricCapacity)]
    const counts = new Map<string, number>()
    for (const entry of entries) {
      const key = gatewayRoutingObservationMetricKey(entry.observation)
      const count = positiveCount(entry.count)
      counts.set(key, Math.min(Number.MAX_SAFE_INTEGER, (counts.get(key) ?? 0) + count))
    }
    for (const [key, count] of counts) {
      argumentsList.push(key, String(count))
    }
    await (await this.client()).eval(redisGatewayRoutingObservabilityRecordScript, {
      keys: [this.key],
      arguments: argumentsList
    })
  }

  async snapshot(): Promise<GatewayRoutingObservabilitySnapshot> {
    const raw = redisHash(await (await this.client()).sendCommand(['HGETALL', this.key]))
    const counters: Record<string, number> = {}
    for (const [key, value] of Object.entries(raw)) {
      if (!key.startsWith('metric:')) continue
      counters[key.slice('metric:'.length)] = finiteCount(value)
    }
    return {
      version: 1,
      recordedEvents: finiteCount(raw.recordedEvents),
      updatedAtMs: finiteCount(raw.updatedAtMs),
      counters
    }
  }

  private client(): Promise<RedisCommandClient> {
    // Performance mode deliberately has no memory fallback: an unavailable
    // state Redis produces a rejected write/snapshot instead of false local data.
    return getRedisClient(this.redisUrl)
  }
}

export const redisGatewayRoutingObservabilityRecordScript = String.raw`
local key = KEYS[1]
local now_ms = ARGV[1]
local capacity = tonumber(ARGV[2])
local new_fields = 0
for index = 3, #ARGV, 2 do
  if redis.call('HEXISTS', key, 'metric:' .. ARGV[index]) == 0 then new_fields = new_fields + 1 end
end
local existing_metric_fields = math.max(0, redis.call('HLEN', key) - 3)
if existing_metric_fields + new_fields > capacity then return redis.error_reply('routing observability metric capacity exhausted') end
local max_safe_integer = 9007199254740991
local function increment_saturated(field, increment)
  local current = tonumber(redis.call('HGET', key, field) or '0')
  redis.call('HSET', key, field, tostring(math.min(max_safe_integer, current + increment)))
end
local recorded = 0
for index = 3, #ARGV, 2 do
  local count = tonumber(ARGV[index + 1])
  increment_saturated('metric:' .. ARGV[index], count)
  recorded = math.min(max_safe_integer, recorded + count)
end
increment_saturated('recordedEvents', recorded)
local previous_updated_at_ms = tonumber(redis.call('HGET', key, 'updatedAtMs') or '0')
redis.call('HSET', key, 'updatedAtMs', tostring(math.max(previous_updated_at_ms, tonumber(now_ms))), 'version', '1')
return 1
`

function safeName(value: string): string {
  const normalized = value.trim().toLowerCase()
  if (!/^[a-z0-9_-]{1,64}$/.test(normalized)) throw new Error('routing observability name 非法')
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new RangeError('routing observability nowMs 必须是非负安全整数')
  return value
}

function positiveCount(value: number): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new RangeError('routing observability count 必须是正安全整数')
  return value
}

function finiteCount(value: string | undefined): number {
  const parsed = Number(value ?? 0)
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : 0
}

function redisHash(value: unknown): Record<string, string> {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    return Object.fromEntries(Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))
  }
  if (!Array.isArray(value)) return {}
  const result: Record<string, string> = {}
  for (let index = 0; index + 1 < value.length; index += 2) {
    const key = value[index]
    const fieldValue = value[index + 1]
    if (typeof key === 'string' && typeof fieldValue === 'string') result[key] = fieldValue
  }
  return result
}
