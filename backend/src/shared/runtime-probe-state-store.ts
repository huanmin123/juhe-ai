import { runtimeConfig } from '../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'

export interface RuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }> {
  get(runtimeKey: string): Promise<TState | undefined>
  set(state: TState, ttlMs: number): Promise<boolean>
  merge(state: TState, ttlMs: number, options: RuntimeProbeStateMergeOptions): Promise<TState | undefined>
  delete(runtimeKey: string): Promise<void>
  deleteGeneration(runtimeKey: string, generation: number): Promise<boolean>
  listDue(nowMs: number, limit: number): Promise<string[]>
  nextGeneration(runtimeKey: string, ttlMs: number): Promise<number>
}

export interface RuntimeProbeStateMergeOptions {
  incrementFields?: readonly string[]
  maxFields?: readonly string[]
  minFields?: readonly string[]
  booleanOrFields?: readonly string[]
  unionArrayFields?: readonly RuntimeProbeStateUnionArrayField[]
}

export interface RuntimeProbeStateUnionArrayField {
  field: string
  countField?: string
  maxItems?: number
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

  async merge(state: TState, ttlMs: number, options: RuntimeProbeStateMergeOptions): Promise<TState | undefined> {
    const current = this.freshEntry(state.runtimeKey)?.value
    const merged = mergeProbeStateValues(current, state, options)
    this.entries.set(state.runtimeKey, {
      value: merged,
      expiresAtMs: Date.now() + normalizedTtlMs(ttlMs)
    })
    return merged
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

  async merge(state: TState, ttlMs: number, options: RuntimeProbeStateMergeOptions): Promise<TState | undefined> {
    const result = await (await this.client()).eval(redisMergeProbeStateScript, {
      keys: [this.stateKey(state.runtimeKey), this.dueKey],
      arguments: [
        JSON.stringify(state),
        String(normalizedTtlMs(ttlMs)),
        String(Math.max(0, Math.trunc(state.nextProbeAtMs))),
        state.runtimeKey,
        String(Math.max(0, Math.trunc(state.generation))),
        JSON.stringify(options)
      ]
    })
    const rawValue = typeof result === 'string'
      ? result
      : Buffer.isBuffer(result)
        ? result.toString('utf8')
        : ''
    if (!rawValue.trim()) return undefined
    try {
      return JSON.parse(rawValue) as TState
    } catch {
      return undefined
    }
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

const redisMergeProbeStateScript = `
local incoming_ok, incoming = pcall(cjson.decode, ARGV[1])
if not incoming_ok or type(incoming) ~= 'table' then
  return ''
end
local options_ok, options = pcall(cjson.decode, ARGV[6])
if not options_ok or type(options) ~= 'table' then
  options = {}
end

local current = redis.call('GET', KEYS[1])
local decoded = nil
if current then
  local ok, current_decoded = pcall(cjson.decode, current)
    if ok and type(current_decoded) == 'table' then
      decoded = current_decoded
    end
end
if not decoded then
  decoded = {}
end

local function number_value(value)
  local parsed = tonumber(value)
  if parsed then return parsed end
  return 0
end

local base = decoded
local merged = {}
for key, value in pairs(base) do
  merged[key] = value
end
for key, value in pairs(incoming) do
  merged[key] = value
end

local current_generation = tonumber(base['generation'])
local incoming_generation = tonumber(incoming['generation'])
if current_generation and incoming_generation then
  if current_generation > incoming_generation then
    merged['generation'] = current_generation
  else
    merged['generation'] = incoming_generation
  end
end

if type(options['incrementFields']) == 'table' then
  for _, field in ipairs(options['incrementFields']) do
    merged[field] = number_value(base[field]) + number_value(incoming[field])
  end
end

if type(options['maxFields']) == 'table' then
  for _, field in ipairs(options['maxFields']) do
    local current_value = number_value(base[field])
    local incoming_value = number_value(incoming[field])
    if current_value > incoming_value then
      merged[field] = current_value
    else
      merged[field] = incoming_value
    end
  end
end

if type(options['minFields']) == 'table' then
  for _, field in ipairs(options['minFields']) do
    local current_value = tonumber(base[field])
    local incoming_value = tonumber(incoming[field])
    if current_value and incoming_value then
      if current_value < incoming_value then
        merged[field] = current_value
      else
        merged[field] = incoming_value
      end
    elseif incoming_value then
      merged[field] = incoming_value
    end
  end
end

if type(options['booleanOrFields']) == 'table' then
  for _, field in ipairs(options['booleanOrFields']) do
    merged[field] = base[field] == true or incoming[field] == true
  end
end

if type(options['unionArrayFields']) == 'table' then
  for _, entry in ipairs(options['unionArrayFields']) do
    if type(entry) == 'table' then
      local field = entry['field']
      if type(field) == 'string' and field ~= '' then
        local max_items = tonumber(entry['maxItems']) or 128
        if max_items < 1 then max_items = 1 end
        local seen = {}
        local output = {}
        local function append_values(values)
          if type(values) ~= 'table' then return end
          for _, item in ipairs(values) do
            local value = tostring(item)
            if value ~= '' and not seen[value] and #output < max_items then
              seen[value] = true
              output[#output + 1] = value
            end
          end
        end
        append_values(base[field])
        append_values(incoming[field])
        merged[field] = output
        local count_field = entry['countField']
        if type(count_field) == 'string' and count_field ~= '' then
          local current_count = number_value(base[count_field])
          local incoming_count = number_value(incoming[count_field])
          if #output > current_count then
            merged[count_field] = #output
          elseif incoming_count > current_count then
            merged[count_field] = incoming_count
          else
            merged[count_field] = current_count
          end
        end
      end
    end
  end
end

local encoded = cjson.encode(merged)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[2])
local merged_next_probe_at = tonumber(merged['nextProbeAtMs']) or tonumber(ARGV[3]) or 0
redis.call('ZADD', KEYS[2], merged_next_probe_at, ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return encoded
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

function mergeProbeStateValues<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }>(
  current: TState | undefined,
  incoming: TState,
  options: RuntimeProbeStateMergeOptions
): TState {
  const merged = {
    ...(current as Record<string, unknown> | undefined),
    ...(incoming as Record<string, unknown>)
  } as Record<string, unknown>
  const currentRecord = current as Record<string, unknown> | undefined
  const incomingRecord = incoming as Record<string, unknown>
  const currentGeneration = finiteNumber(currentRecord?.generation)
  const incomingGeneration = finiteNumber(incomingRecord.generation)
  if (currentGeneration !== undefined && incomingGeneration !== undefined) {
    merged.generation = Math.max(currentGeneration, incomingGeneration)
  }
  for (const field of options.incrementFields ?? []) {
    merged[field] = numberValue(currentRecord?.[field]) + numberValue(incomingRecord[field])
  }
  for (const field of options.maxFields ?? []) {
    merged[field] = Math.max(numberValue(currentRecord?.[field]), numberValue(incomingRecord[field]))
  }
  for (const field of options.minFields ?? []) {
    const currentValue = finiteNumber(currentRecord?.[field])
    const incomingValue = finiteNumber(incomingRecord[field])
    if (currentValue !== undefined && incomingValue !== undefined) {
      merged[field] = Math.min(currentValue, incomingValue)
    } else if (incomingValue !== undefined) {
      merged[field] = incomingValue
    }
  }
  for (const field of options.booleanOrFields ?? []) {
    merged[field] = currentRecord?.[field] === true || incomingRecord[field] === true
  }
  for (const entry of options.unionArrayFields ?? []) {
    const output = unionStringArrays(
      currentRecord?.[entry.field],
      incomingRecord[entry.field],
      entry.maxItems
    )
    merged[entry.field] = output
    if (entry.countField) {
      merged[entry.countField] = Math.max(
        numberValue(currentRecord?.[entry.countField]),
        numberValue(incomingRecord[entry.countField]),
        output.length
      )
    }
  }
  return merged as TState
}

function unionStringArrays(first: unknown, second: unknown, maxItems = 128): string[] {
  const limit = Math.max(1, Math.trunc(maxItems))
  const output: string[] = []
  const seen = new Set<string>()
  for (const values of [first, second]) {
    if (!Array.isArray(values)) continue
    for (const item of values) {
      const value = typeof item === 'string' ? item.trim() : String(item ?? '').trim()
      if (!value || seen.has(value)) continue
      seen.add(value)
      output.push(value)
      if (output.length >= limit) return output
    }
  }
  return output
}

function numberValue(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function finiteNumber(value: unknown): number | undefined {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : undefined
}
