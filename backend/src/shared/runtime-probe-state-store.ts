import { runtimeConfig } from '../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'
import { redisNamespacedKey } from './redis-namespace.js'

export interface RuntimeProbeStateStore<TState extends { runtimeKey: string; generation: number; nextProbeAtMs: number }> {
  get(runtimeKey: string): Promise<TState | undefined>
  getMany(runtimeKeys: string[]): Promise<Map<string, TState>>
  set(state: TState, ttlMs: number): Promise<boolean>
  setIfAbsent(state: TState, ttlMs: number): Promise<boolean>
  merge(state: TState, ttlMs: number, options: RuntimeProbeStateMergeOptions): Promise<TState | undefined>
  delete(runtimeKey: string): Promise<void>
  deleteGeneration(runtimeKey: string, generation: number): Promise<boolean>
  acquireGenerationLease(runtimeKey: string, generation: number, leaseId: string, leaseUntilMs: number, ttlMs: number): Promise<TState | undefined>
  releaseGenerationLease(runtimeKey: string, generation: number, leaseId: string, ttlMs: number): Promise<boolean>
  completeGenerationLease(runtimeKey: string, generation: number, leaseId: string): Promise<boolean>
  acquireGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<TState | undefined>
  renewGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<boolean>
  commitGenerationRun(state: TState, runId: string, ttlMs: number): Promise<boolean>
  deleteGenerationRun(runtimeKey: string, generation: number, runId: string): Promise<boolean>
  listDue(nowMs: number, limit: number): Promise<string[]>
  scheduledRuntimeKeys(runtimeKeys: string[]): Promise<Set<string>>
  nextGeneration(runtimeKey: string, ttlMs: number): Promise<number>
}

export interface RuntimeProbeStateMergeOptions {
  preserveCurrentFields?: readonly string[]
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

interface ProbeCoordinationFields {
  phase?: string
  halfOpenLeaseId?: string
  halfOpenLeaseUntilMs?: number
  halfOpenPreviousNextProbeAtMs?: number
  probeRunId?: string
  probeRunUntilMs?: number
  probeRunPreviousNextProbeAtMs?: number
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

  async getMany(runtimeKeys: string[]): Promise<Map<string, TState>> {
    const result = new Map<string, TState>()
    for (const runtimeKey of new Set(runtimeKeys.filter(Boolean))) {
      const value = this.freshEntry(runtimeKey)?.value
      if (value) result.set(runtimeKey, value)
    }
    return result
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

  async setIfAbsent(state: TState, ttlMs: number): Promise<boolean> {
    if (this.freshEntry(state.runtimeKey)) return false
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

  async acquireGenerationLease(runtimeKey: string, generation: number, leaseId: string, leaseUntilMs: number, ttlMs: number): Promise<TState | undefined> {
    const current = this.freshEntry(runtimeKey)?.value
    if (!current || current.generation !== generation) return undefined
    const record = current as TState & ProbeCoordinationFields
    if (record.phase !== 'precheck_pending') return undefined
    const now = Date.now()
    if (record.halfOpenLeaseId && (record.halfOpenLeaseUntilMs ?? 0) > now) return undefined
    if (record.probeRunId && (record.probeRunUntilMs ?? 0) > now) return undefined
    const previousNextProbeAtMs = record.probeRunPreviousNextProbeAtMs ?? current.nextProbeAtMs
    const leased = {
      ...current,
      halfOpenLeaseId: leaseId,
      halfOpenLeaseUntilMs: leaseUntilMs,
      halfOpenPreviousNextProbeAtMs: previousNextProbeAtMs,
      nextProbeAtMs: Math.max(previousNextProbeAtMs, leaseUntilMs),
      probeRunId: undefined,
      probeRunUntilMs: undefined,
      probeRunPreviousNextProbeAtMs: undefined
    } as TState & ProbeCoordinationFields
    delete leased.probeRunId
    delete leased.probeRunUntilMs
    delete leased.probeRunPreviousNextProbeAtMs
    this.entries.set(runtimeKey, { value: leased, expiresAtMs: Date.now() + normalizedTtlMs(ttlMs) })
    return leased
  }

  async releaseGenerationLease(runtimeKey: string, generation: number, leaseId: string, ttlMs: number): Promise<boolean> {
    const current = this.freshEntry(runtimeKey)?.value
    if (!current || current.generation !== generation) return false
    const record = current as TState & { halfOpenLeaseId?: string; halfOpenLeaseUntilMs?: number; halfOpenPreviousNextProbeAtMs?: number }
    if (record.halfOpenLeaseId !== leaseId) return false
    const restored = { ...current } as TState & { halfOpenLeaseId?: string; halfOpenLeaseUntilMs?: number; halfOpenPreviousNextProbeAtMs?: number }
    restored.nextProbeAtMs = record.halfOpenPreviousNextProbeAtMs ?? restored.nextProbeAtMs
    delete restored.halfOpenLeaseId
    delete restored.halfOpenLeaseUntilMs
    delete restored.halfOpenPreviousNextProbeAtMs
    this.entries.set(runtimeKey, { value: restored, expiresAtMs: Date.now() + normalizedTtlMs(ttlMs) })
    return true
  }

  async completeGenerationLease(runtimeKey: string, generation: number, leaseId: string): Promise<boolean> {
    const current = this.freshEntry(runtimeKey)?.value as (TState & { halfOpenLeaseId?: string }) | undefined
    if (!current || current.generation !== generation || current.halfOpenLeaseId !== leaseId) return false
    await this.delete(runtimeKey)
    return true
  }

  async acquireGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<TState | undefined> {
    const current = this.freshEntry(runtimeKey)?.value
    if (!current || current.generation !== generation) return undefined
    const record = current as TState & ProbeCoordinationFields
    const now = Date.now()
    if (record.halfOpenLeaseId && (record.halfOpenLeaseUntilMs ?? 0) > now) return undefined
    if (record.probeRunId && record.probeRunId !== runId && (record.probeRunUntilMs ?? 0) > now) return undefined
    const previousNextProbeAtMs = record.halfOpenPreviousNextProbeAtMs
      ?? record.probeRunPreviousNextProbeAtMs
      ?? current.nextProbeAtMs
    const running = {
      ...current,
      nextProbeAtMs: Math.max(previousNextProbeAtMs, runUntilMs),
      probeRunId: runId,
      probeRunUntilMs: runUntilMs,
      probeRunPreviousNextProbeAtMs: previousNextProbeAtMs,
      halfOpenLeaseId: undefined,
      halfOpenLeaseUntilMs: undefined,
      halfOpenPreviousNextProbeAtMs: undefined
    } as TState & ProbeCoordinationFields
    delete running.halfOpenLeaseId
    delete running.halfOpenLeaseUntilMs
    delete running.halfOpenPreviousNextProbeAtMs
    this.entries.set(runtimeKey, { value: running, expiresAtMs: now + normalizedTtlMs(ttlMs) })
    return running
  }

  async renewGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<boolean> {
    const current = this.freshEntry(runtimeKey)?.value as (TState & ProbeCoordinationFields) | undefined
    if (!current || current.generation !== generation || current.probeRunId !== runId) return false
    const renewed = {
      ...current,
      nextProbeAtMs: Math.max(current.nextProbeAtMs, runUntilMs),
      probeRunUntilMs: runUntilMs
    } as TState & ProbeCoordinationFields
    this.entries.set(runtimeKey, { value: renewed, expiresAtMs: Date.now() + normalizedTtlMs(ttlMs) })
    return true
  }

  async commitGenerationRun(state: TState, runId: string, ttlMs: number): Promise<boolean> {
    const current = this.freshEntry(state.runtimeKey)?.value as (TState & ProbeCoordinationFields) | undefined
    if (!current || current.generation !== state.generation || current.probeRunId !== runId) return false
    const committed = { ...state } as TState & ProbeCoordinationFields
    delete committed.probeRunId
    delete committed.probeRunUntilMs
    delete committed.probeRunPreviousNextProbeAtMs
    this.entries.set(state.runtimeKey, { value: committed, expiresAtMs: Date.now() + normalizedTtlMs(ttlMs) })
    return true
  }

  async deleteGenerationRun(runtimeKey: string, generation: number, runId: string): Promise<boolean> {
    const current = this.freshEntry(runtimeKey)?.value as (TState & ProbeCoordinationFields) | undefined
    if (!current || current.generation !== generation || current.probeRunId !== runId) return false
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

  async scheduledRuntimeKeys(runtimeKeys: string[]): Promise<Set<string>> {
    const result = new Set<string>()
    for (const runtimeKey of [...new Set(runtimeKeys.filter(Boolean))].slice(0, 100)) {
      if (this.freshEntry(runtimeKey)) result.add(runtimeKey)
    }
    return result
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
    this.statePrefix = redisNamespacedKey(`juhe-ai:probe:${safeName}:state:`)
    this.generationPrefix = redisNamespacedKey(`juhe-ai:probe:${safeName}:generation:`)
    this.dueKey = redisNamespacedKey(`juhe-ai:probe:${safeName}:due`)
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

  async getMany(runtimeKeys: string[]): Promise<Map<string, TState>> {
    const keys = [...new Set(runtimeKeys.filter(Boolean))].slice(0, 100)
    if (keys.length === 0) return new Map<string, TState>()
    const rawValues = await (await this.client()).sendCommand(['MGET', ...keys.map((runtimeKey) => this.stateKey(runtimeKey))])
    if (!Array.isArray(rawValues)) throw new Error('Redis probe state MGET 返回值无效')
    const result = new Map<string, TState>()
    for (let index = 0; index < keys.length; index += 1) {
      const raw = rawValues[index]
      if (raw === null || raw === undefined) continue
      const encoded = Buffer.isBuffer(raw) ? raw.toString('utf8') : String(raw)
      try {
        result.set(keys[index]!, JSON.parse(encoded) as TState)
      } catch {
        await this.delete(keys[index]!)
        throw new Error(`Redis probe state 内容损坏：${keys[index]}`)
      }
    }
    return result
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

  async acquireGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<TState | undefined> {
    const result = await (await this.client()).eval(redisAcquireProbeGenerationRunScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [
        runtimeKey,
        String(Math.max(0, Math.trunc(generation))),
        runId,
        String(Math.max(0, Math.trunc(runUntilMs))),
        String(Date.now()),
        String(normalizedTtlMs(ttlMs))
      ]
    })
    const encoded = typeof result === 'string' ? result : Buffer.isBuffer(result) ? result.toString('utf8') : ''
    if (!encoded) return undefined
    try {
      return JSON.parse(encoded) as TState
    } catch {
      return undefined
    }
  }

  async renewGenerationRun(runtimeKey: string, generation: number, runId: string, runUntilMs: number, ttlMs: number): Promise<boolean> {
    const result = await (await this.client()).eval(redisRenewProbeGenerationRunScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [
        runtimeKey,
        String(Math.max(0, Math.trunc(generation))),
        runId,
        String(Math.max(0, Math.trunc(runUntilMs))),
        String(normalizedTtlMs(ttlMs))
      ]
    })
    return numericRedisResult(result) === 1
  }

  async commitGenerationRun(state: TState, runId: string, ttlMs: number): Promise<boolean> {
    const result = await (await this.client()).eval(redisCommitProbeGenerationRunScript, {
      keys: [this.stateKey(state.runtimeKey), this.dueKey],
      arguments: [
        JSON.stringify(state),
        String(normalizedTtlMs(ttlMs)),
        String(Math.max(0, Math.trunc(state.nextProbeAtMs))),
        state.runtimeKey,
        String(Math.max(0, Math.trunc(state.generation))),
        runId
      ]
    })
    return numericRedisResult(result) === 1
  }

  async deleteGenerationRun(runtimeKey: string, generation: number, runId: string): Promise<boolean> {
    const result = await (await this.client()).eval(redisDeleteProbeStateGenerationRunScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [runtimeKey, String(Math.max(0, Math.trunc(generation))), runId]
    })
    return numericRedisResult(result) === 1
  }

  async setIfAbsent(state: TState, ttlMs: number): Promise<boolean> {
    const result = await (await this.client()).eval(redisSetProbeStateIfAbsentScript, {
      keys: [this.stateKey(state.runtimeKey), this.dueKey],
      arguments: [
        JSON.stringify(state),
        String(normalizedTtlMs(ttlMs)),
        String(Math.max(0, Math.trunc(state.nextProbeAtMs))),
        state.runtimeKey
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

  async acquireGenerationLease(runtimeKey: string, generation: number, leaseId: string, leaseUntilMs: number, ttlMs: number): Promise<TState | undefined> {
    const result = await (await this.client()).eval(redisAcquireProbeGenerationLeaseScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [
        runtimeKey,
        String(Math.max(0, Math.trunc(generation))),
        leaseId,
        String(Math.max(0, Math.trunc(leaseUntilMs))),
        String(Date.now()),
        String(normalizedTtlMs(ttlMs))
      ]
    })
    const encoded = typeof result === 'string' ? result : Buffer.isBuffer(result) ? result.toString('utf8') : ''
    if (!encoded) return undefined
    try {
      return JSON.parse(encoded) as TState
    } catch {
      return undefined
    }
  }

  async releaseGenerationLease(runtimeKey: string, generation: number, leaseId: string, ttlMs: number): Promise<boolean> {
    const result = await (await this.client()).eval(redisReleaseProbeGenerationLeaseScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [runtimeKey, String(Math.max(0, Math.trunc(generation))), leaseId, String(normalizedTtlMs(ttlMs))]
    })
    return numericRedisResult(result) === 1
  }

  async completeGenerationLease(runtimeKey: string, generation: number, leaseId: string): Promise<boolean> {
    const result = await (await this.client()).eval(redisCompleteProbeGenerationLeaseScript, {
      keys: [this.stateKey(runtimeKey), this.dueKey],
      arguments: [runtimeKey, String(Math.max(0, Math.trunc(generation))), leaseId]
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

  async scheduledRuntimeKeys(runtimeKeys: string[]): Promise<Set<string>> {
    const keys = [...new Set(runtimeKeys.filter(Boolean))].slice(0, 100)
    if (keys.length === 0) return new Set<string>()
    const result = await (await this.client()).sendCommand(['ZMSCORE', this.dueKey, ...keys])
    if (!Array.isArray(result)) throw new Error('Redis probe due membership ZMSCORE 返回值无效')
    const scheduled = new Set<string>()
    for (let index = 0; index < keys.length; index += 1) {
      if (result[index] !== null && result[index] !== undefined) scheduled.add(keys[index]!)
    }
    return scheduled
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

const redisSetProbeStateIfAbsentScript = `
if redis.call('EXISTS', KEYS[1]) == 1 then
  return 0
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
if current_generation then
  merged['generation'] = current_generation
elseif incoming_generation then
  merged['generation'] = incoming_generation
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

if type(options['preserveCurrentFields']) == 'table' then
  for _, field in ipairs(options['preserveCurrentFields']) do
    if base[field] ~= nil then
      merged[field] = base[field]
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

const redisAcquireProbeGenerationLeaseScript = `
local current = redis.call('GET', KEYS[1])
if not current then return '' end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return '' end
if tonumber(decoded['generation']) ~= tonumber(ARGV[2]) then return '' end
if decoded['phase'] ~= 'precheck_pending' then return '' end
local lease_id = decoded['halfOpenLeaseId']
local lease_until = tonumber(decoded['halfOpenLeaseUntilMs']) or 0
if lease_id and lease_id ~= cjson.null and lease_until > tonumber(ARGV[5]) then return '' end
local run_id = decoded['probeRunId']
local run_until = tonumber(decoded['probeRunUntilMs']) or 0
if run_id and run_id ~= cjson.null and run_until > tonumber(ARGV[5]) then return '' end
local previous_run_next = tonumber(decoded['probeRunPreviousNextProbeAtMs'])
if previous_run_next then decoded['nextProbeAtMs'] = previous_run_next end
decoded['halfOpenLeaseId'] = ARGV[3]
decoded['halfOpenLeaseUntilMs'] = tonumber(ARGV[4])
decoded['halfOpenPreviousNextProbeAtMs'] = tonumber(decoded['nextProbeAtMs']) or 0
decoded['nextProbeAtMs'] = math.max(tonumber(decoded['nextProbeAtMs']) or 0, tonumber(ARGV[4]))
decoded['probeRunId'] = nil
decoded['probeRunUntilMs'] = nil
decoded['probeRunPreviousNextProbeAtMs'] = nil
local encoded = cjson.encode(decoded)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[6])
redis.call('ZADD', KEYS[2], tonumber(decoded['nextProbeAtMs']) or 0, ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[6])
return encoded
`

const redisReleaseProbeGenerationLeaseScript = `
local current = redis.call('GET', KEYS[1])
if not current then return 0 end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return 0 end
if tonumber(decoded['generation']) ~= tonumber(ARGV[2]) then return 0 end
if decoded['halfOpenLeaseId'] ~= ARGV[3] then return 0 end
decoded['nextProbeAtMs'] = tonumber(decoded['halfOpenPreviousNextProbeAtMs']) or tonumber(decoded['nextProbeAtMs']) or 0
decoded['halfOpenLeaseId'] = nil
decoded['halfOpenLeaseUntilMs'] = nil
decoded['halfOpenPreviousNextProbeAtMs'] = nil
local encoded = cjson.encode(decoded)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[4])
redis.call('ZADD', KEYS[2], tonumber(decoded['nextProbeAtMs']) or 0, ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[4])
return 1
`

const redisAcquireProbeGenerationRunScript = `
local current = redis.call('GET', KEYS[1])
if not current then return '' end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return '' end
if tonumber(decoded['generation']) ~= tonumber(ARGV[2]) then return '' end
local lease_id = decoded['halfOpenLeaseId']
local lease_until = tonumber(decoded['halfOpenLeaseUntilMs']) or 0
if lease_id and lease_id ~= cjson.null and lease_until > tonumber(ARGV[5]) then return '' end
local current_run_id = decoded['probeRunId']
local current_run_until = tonumber(decoded['probeRunUntilMs']) or 0
if current_run_id and current_run_id ~= ARGV[3] and current_run_until > tonumber(ARGV[5]) then return '' end
local previous_lease_next = tonumber(decoded['halfOpenPreviousNextProbeAtMs'])
local previous_run_next = tonumber(decoded['probeRunPreviousNextProbeAtMs'])
local previous_next = previous_lease_next or previous_run_next or tonumber(decoded['nextProbeAtMs']) or 0
decoded['nextProbeAtMs'] = math.max(previous_next, tonumber(ARGV[4]))
decoded['probeRunId'] = ARGV[3]
decoded['probeRunUntilMs'] = tonumber(ARGV[4])
decoded['probeRunPreviousNextProbeAtMs'] = previous_next
decoded['halfOpenLeaseId'] = nil
decoded['halfOpenLeaseUntilMs'] = nil
decoded['halfOpenPreviousNextProbeAtMs'] = nil
local encoded = cjson.encode(decoded)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[6])
redis.call('ZADD', KEYS[2], tonumber(decoded['nextProbeAtMs']) or 0, ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[6])
return encoded
`

const redisCommitProbeGenerationRunScript = `
local current = redis.call('GET', KEYS[1])
if not current then return 0 end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return 0 end
if tonumber(decoded['generation']) ~= tonumber(ARGV[5]) then return 0 end
if decoded['probeRunId'] ~= ARGV[6] then return 0 end
local incoming_ok, incoming = pcall(cjson.decode, ARGV[1])
if not incoming_ok or type(incoming) ~= 'table' then return 0 end
if tonumber(incoming['generation']) ~= tonumber(ARGV[5]) then return 0 end
incoming['probeRunId'] = nil
incoming['probeRunUntilMs'] = nil
incoming['probeRunPreviousNextProbeAtMs'] = nil
local encoded = cjson.encode(incoming)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], tonumber(incoming['nextProbeAtMs']) or tonumber(ARGV[3]) or 0, ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return 1
`

const redisRenewProbeGenerationRunScript = `
local current = redis.call('GET', KEYS[1])
if not current then return 0 end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return 0 end
if tonumber(decoded['generation']) ~= tonumber(ARGV[2]) then return 0 end
if decoded['probeRunId'] ~= ARGV[3] then return 0 end
decoded['probeRunUntilMs'] = tonumber(ARGV[4])
decoded['nextProbeAtMs'] = math.max(tonumber(decoded['nextProbeAtMs']) or 0, tonumber(ARGV[4]))
local encoded = cjson.encode(decoded)
redis.call('SET', KEYS[1], encoded, 'PX', ARGV[5])
redis.call('ZADD', KEYS[2], tonumber(decoded['nextProbeAtMs']) or 0, ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[5])
return 1
`

const redisDeleteProbeStateGenerationRunScript = `
local current = redis.call('GET', KEYS[1])
if not current then return 0 end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return 0 end
if tonumber(decoded['generation']) ~= tonumber(ARGV[2]) then return 0 end
if decoded['probeRunId'] ~= ARGV[3] then return 0 end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return 1
`

const redisCompleteProbeGenerationLeaseScript = `
local current = redis.call('GET', KEYS[1])
if not current then return 0 end
local ok, decoded = pcall(cjson.decode, current)
if not ok or type(decoded) ~= 'table' then return 0 end
if tonumber(decoded['generation']) ~= tonumber(ARGV[2]) then return 0 end
if decoded['halfOpenLeaseId'] ~= ARGV[3] then return 0 end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
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
  if (currentGeneration !== undefined) {
    merged.generation = currentGeneration
  } else if (incomingGeneration !== undefined) {
    merged.generation = incomingGeneration
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
  for (const field of options.preserveCurrentFields ?? []) {
    if (currentRecord?.[field] !== undefined) {
      merged[field] = currentRecord[field]
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
