import { createHash } from 'node:crypto'

import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import { createHotQualitySnapshot, type HotQualityBucketState } from './hot-quality-snapshot.js'
import {
  HOT_QUALITY_KEY_TTL_MS,
  HOT_QUALITY_TERMINAL_TTL_MS,
  cloneHotQualityScope,
  hotQualityScopeKey,
  normalizeHotQualityScope,
  normalizedFirstByteMs,
  protocolHotQualityScope,
  type HotQualityAttemptMutationResult,
  type HotQualityFailureScope,
  type HotQualityScope,
  type HotQualitySnapshot,
  type HotQualityStore,
  type HotQualityStoreStats,
  type HotQualityTerminalMutationResult,
  type HotQualityTerminalOutcomeClass,
  type HotQualityTerminalRecord,
  type HotQualityTerminalSource
} from './hot-quality-store.js'

export interface RedisHotQualityStoreOptions {
  redisUrl: string
  name?: string
  keyCapacity?: number
  attemptCapacity?: number
  keyTtlMs?: number
  terminalTtlMs?: number
  now?: () => number
}

export interface RedisHotQualityStoreKeys {
  prefix: string
  hotRegistry: string
  attemptRegistry: string
  terminalRegistry: string
  metrics: string
}

interface RedisHotQualityEntry {
  scopeKey: string
  scope: HotQualityScope
  buckets: Record<string, HotQualityBucketState>
  expiresAtMs: number
}

type RedisHotQualityOperation = 'record_attempt' | 'record_terminal'

export class RedisHotQualityStore implements HotQualityStore {
  private readonly redisUrl: string
  private readonly keys: RedisHotQualityStoreKeys
  private readonly keyCapacity: number
  private readonly attemptCapacity: number
  private readonly keyTtlMs: number
  private readonly terminalTtlMs: number
  private readonly now: () => number

  constructor(options: RedisHotQualityStoreOptions) {
    this.redisUrl = requiredText(options.redisUrl, 'redisUrl')
    this.keys = redisHotQualityStoreKeys(options.name ?? 'gateway-hot-quality')
    this.keyCapacity = positiveInteger(options.keyCapacity ?? 10_000, 'keyCapacity')
    this.attemptCapacity = positiveInteger(options.attemptCapacity ?? 100_000, 'attemptCapacity')
    this.keyTtlMs = positiveInteger(options.keyTtlMs ?? HOT_QUALITY_KEY_TTL_MS, 'keyTtlMs')
    this.terminalTtlMs = positiveInteger(options.terminalTtlMs ?? HOT_QUALITY_TERMINAL_TTL_MS, 'terminalTtlMs')
    if (this.terminalTtlMs < HOT_QUALITY_TERMINAL_TTL_MS) {
      throw new Error(`terminalTtlMs 不得少于 ${HOT_QUALITY_TERMINAL_TTL_MS}ms`)
    }
    this.now = options.now ?? Date.now
  }

  recordAttempt(input: {
    attemptId: string
    scope: HotQualityScope
    nowMs?: number
  }): Promise<HotQualityAttemptMutationResult> {
    const scope = normalizeHotQualityScope(input.scope)
    return this.mutate('record_attempt', {
      attemptId: boundedIdentity(input.attemptId, 'attemptId'),
      scope,
      nowMs: normalizedNow(input.nowMs ?? this.now())
    }) as Promise<HotQualityAttemptMutationResult>
  }

  recordTerminal(input: {
    attemptId: string
    scope: HotQualityScope
    terminalOutcomeId: string
    outcomeClass: HotQualityTerminalOutcomeClass
    failureScope: HotQualityFailureScope
    source: HotQualityTerminalSource
    firstByteMs?: number
    nowMs?: number
  }): Promise<HotQualityTerminalMutationResult> {
    const scope = normalizeHotQualityScope(input.scope)
    assertOutcomeClass(input.outcomeClass)
    assertFailureScope(input.failureScope)
    assertTerminalSource(input.source)
    return this.mutate('record_terminal', {
      attemptId: boundedIdentity(input.attemptId, 'attemptId'),
      scope,
      terminalOutcomeId: boundedIdentity(input.terminalOutcomeId, 'terminalOutcomeId'),
      outcomeClass: input.outcomeClass,
      failureScope: input.failureScope,
      source: input.source,
      firstByteMs: input.firstByteMs === undefined ? undefined : normalizedFirstByteMs(input.firstByteMs),
      nowMs: normalizedNow(input.nowMs ?? this.now())
    }) as Promise<HotQualityTerminalMutationResult>
  }

  async get(scopeInput: HotQualityScope, nowMs = this.now()): Promise<HotQualitySnapshot | undefined> {
    const scope = normalizeHotQualityScope(scopeInput)
    const scopeKey = hotQualityScopeKey(scope)
    const raw = await (await this.client()).eval(redisHotQualityReadEntryScript, {
      keys: [redisHotQualityEntryKey(this.keys, scopeKey), this.keys.hotRegistry],
      arguments: [String(normalizedNow(nowMs))]
    })
    const encoded = redisString(raw)
    if (!encoded) return undefined
    const entry = parseRedisHotQualityEntry(encoded)
    return createHotQualitySnapshot({
      scopeKey: entry.scopeKey,
      scope: entry.scope,
      buckets: Object.values(entry.buckets),
      expiresAtMs: entry.expiresAtMs
    }, normalizedNow(nowMs))
  }

  async getTerminal(attemptIdInput: string, nowMs = this.now()): Promise<HotQualityTerminalRecord | undefined> {
    const attemptId = boundedIdentity(attemptIdInput, 'attemptId')
    const raw = await (await this.client()).eval(redisHotQualityReadTerminalScript, {
      keys: [redisHotQualityAttemptKey(this.keys, attemptId), this.keys.attemptRegistry],
      arguments: [String(normalizedNow(nowMs))]
    })
    const encoded = redisString(raw)
    return encoded ? parseTerminalRecord(encoded) : undefined
  }

  async stats(nowMs = this.now()): Promise<HotQualityStoreStats> {
    const raw = await (await this.client()).eval(redisHotQualityStatsScript, {
      keys: [this.keys.hotRegistry, this.keys.attemptRegistry, this.keys.terminalRegistry, this.keys.metrics],
      arguments: [String(normalizedNow(nowMs))]
    })
    const encoded = redisString(raw)
    if (!encoded) throw new Error('Redis 热质量统计返回值无效')
    const parsed = JSON.parse(encoded) as Partial<HotQualityStoreStats>
    return {
      keyCount: numericValue(parsed.keyCount, 'keyCount'),
      attemptIdentityCount: numericValue(parsed.attemptIdentityCount, 'attemptIdentityCount'),
      terminalIdentityCount: numericValue(parsed.terminalIdentityCount, 'terminalIdentityCount'),
      keyCreationRefusals: numericValue(parsed.keyCreationRefusals ?? 0, 'keyCreationRefusals'),
      highCardinalityDegradations: numericValue(parsed.highCardinalityDegradations ?? 0, 'highCardinalityDegradations'),
      attemptCapacityRefusals: numericValue(parsed.attemptCapacityRefusals ?? 0, 'attemptCapacityRefusals'),
      terminalQualityKeyMisses: numericValue(parsed.terminalQualityKeyMisses ?? 0, 'terminalQualityKeyMisses')
    }
  }

  private async mutate(
    operation: RedisHotQualityOperation,
    input: { attemptId: string; scope: HotQualityScope; nowMs: number } & Record<string, unknown>
  ): Promise<HotQualityAttemptMutationResult | HotQualityTerminalMutationResult> {
    const requestedScope = input.scope
    const requestedScopeKey = hotQualityScopeKey(requestedScope)
    const fallbackScope = protocolHotQualityScope(requestedScope)
    const fallbackScopeKey = hotQualityScopeKey(fallbackScope)
    const terminalOutcomeId = typeof input.terminalOutcomeId === 'string' ? input.terminalOutcomeId : `attempt-${input.attemptId}`
    const payload = {
      ...input,
      operation,
      requestedScope,
      requestedScopeKey,
      fallbackScope,
      fallbackScopeKey
    }
    const raw = await (await this.client()).eval(redisHotQualityMutationScript, {
      keys: [
        redisHotQualityEntryKey(this.keys, requestedScopeKey),
        redisHotQualityEntryKey(this.keys, fallbackScopeKey),
        redisHotQualityAttemptKey(this.keys, input.attemptId),
        redisHotQualityTerminalKey(this.keys, terminalOutcomeId),
        this.keys.hotRegistry,
        this.keys.attemptRegistry,
        this.keys.terminalRegistry,
        this.keys.metrics
      ],
      arguments: [
        JSON.stringify(payload),
        String(this.keyCapacity),
        String(this.attemptCapacity),
        String(this.keyTtlMs),
        String(this.terminalTtlMs)
      ]
    })
    const encoded = redisString(raw)
    if (!encoded) throw new Error('Redis 热质量 mutation 返回值无效')
    const result = JSON.parse(encoded) as HotQualityAttemptMutationResult | HotQualityTerminalMutationResult
    if (!result?.status) throw new Error('Redis 热质量 mutation 结构无效')
    if ('requestedScope' in result && result.requestedScope) result.requestedScope = cloneHotQualityScope(result.requestedScope)
    if (result.effectiveScope) result.effectiveScope = cloneHotQualityScope(result.effectiveScope)
    if ('terminal' in result && result.terminal) result.terminal = { ...result.terminal }
    return result
  }

  private client(): Promise<RedisCommandClient> {
    return getRedisClient(this.redisUrl)
  }
}

export function redisHotQualityStoreKeys(name: string): RedisHotQualityStoreKeys {
  const safeName = name.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'gateway-hot-quality'
  const prefix = redisNamespacedKey(`juhe-ai:hot-quality:${safeName}`)
  return {
    prefix,
    hotRegistry: `${prefix}:registry:hot`,
    attemptRegistry: `${prefix}:registry:attempt`,
    terminalRegistry: `${prefix}:registry:terminal`,
    metrics: `${prefix}:metrics`
  }
}

// Both real dispatch accounting and terminal projection are committed by this one Lua script.
export const redisHotQualityMutationScript = String.raw`
local requested_entry_key = KEYS[1]
local fallback_entry_key = KEYS[2]
local attempt_key = KEYS[3]
local terminal_outcome_key = KEYS[4]
local hot_registry_key = KEYS[5]
local attempt_registry_key = KEYS[6]
local terminal_registry_key = KEYS[7]
local metrics_key = KEYS[8]
local input = cjson.decode(ARGV[1])
local key_capacity = tonumber(ARGV[2])
local attempt_capacity = tonumber(ARGV[3])
local key_ttl_ms = tonumber(ARGV[4])
local terminal_ttl_ms = tonumber(ARGV[5])
local now_ms = tonumber(input['nowMs'])
local current_minute = math.floor(now_ms / 60000)
local max_safe_integer = 9007199254740991

local function response(status, requested_scope, effective_scope, terminal)
  local result = { status = status }
  if requested_scope then result['requestedScope'] = requested_scope end
  if effective_scope then result['effectiveScope'] = effective_scope end
  if terminal then result['terminal'] = terminal end
  return cjson.encode(result)
end

local function metric(name)
  redis.call('HINCRBY', metrics_key, name, 1)
end

local function increment(value)
  return math.min(max_safe_integer, tonumber(value or 0) + 1)
end

local function add(value, amount)
  return math.min(max_safe_integer, tonumber(value or 0) + tonumber(amount or 0))
end

local function cleanup_registries()
  redis.call('ZREMRANGEBYSCORE', hot_registry_key, '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', attempt_registry_key, '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', terminal_registry_key, '-inf', now_ms)
end

local function load_expiring(key, registry_key)
  local raw = redis.call('GET', key)
  if not raw then return nil end
  local value = cjson.decode(raw)
  if tonumber(value['expiresAtMs'] or 0) <= now_ms then
    redis.call('DEL', key)
    redis.call('ZREM', registry_key, key)
    return nil
  end
  return value
end

local function empty_bucket()
  return {
    minuteStartedAtMs = current_minute * 60000,
    attempts = 0,
    completedResponses = 0,
    localTransportFailures = 0,
    timeouts = 0,
    readInterruptions = 0,
    incompleteResponses = 0,
    explicitPolicyFailures = 0,
    unknownOutcomes = 0,
    clientCancellations = 0,
    firstByteSampleCount = 0,
    firstByteSumMs = 0,
    firstByteHistogram = { 0, 0, 0, 0, 0, 0, 0, 0 }
  }
end

local function current_bucket(entry)
  local buckets = entry['buckets'] or {}
  for bucket_key, _ in pairs(buckets) do
    local bucket_minute = tonumber(bucket_key)
    if not bucket_minute or bucket_minute <= current_minute - 30 or bucket_minute > current_minute then
      buckets[bucket_key] = nil
    end
  end
  local minute_key = tostring(current_minute)
  local bucket = buckets[minute_key]
  if not bucket then
    bucket = empty_bucket()
    buckets[minute_key] = bucket
  end
  entry['buckets'] = buckets
  return bucket
end

local function new_entry(scope_key, scope)
  return { scopeKey = scope_key, scope = scope, buckets = {}, expiresAtMs = now_ms + key_ttl_ms }
end

local function persist_entry(key, entry)
  entry['expiresAtMs'] = now_ms + key_ttl_ms
  redis.call('SET', key, cjson.encode(entry), 'PX', key_ttl_ms)
  redis.call('ZADD', hot_registry_key, entry['expiresAtMs'], key)
end

local function persist_attempt(attempt)
  attempt['expiresAtMs'] = now_ms + terminal_ttl_ms
  redis.call('SET', attempt_key, cjson.encode(attempt), 'PX', terminal_ttl_ms)
  redis.call('ZADD', attempt_registry_key, attempt['expiresAtMs'], attempt_key)
end

local function same_terminal(terminal)
  return terminal['terminalOutcomeId'] == input['terminalOutcomeId']
    and terminal['outcomeClass'] == input['outcomeClass']
    and terminal['failureScope'] == input['failureScope']
    and terminal['source'] == input['source']
end

local function histogram_index(sample)
  if sample <= 1000 then return 1 end
  if sample <= 2000 then return 2 end
  if sample <= 5000 then return 3 end
  if sample <= 10000 then return 4 end
  if sample <= 20000 then return 5 end
  if sample <= 30000 then return 6 end
  if sample <= 60000 then return 7 end
  return 8
end

local function apply_terminal(bucket)
  local outcome = input['outcomeClass']
  if outcome == 'completed_response' then
    bucket['completedResponses'] = increment(bucket['completedResponses'])
    bucket['lastCompletedAtMs'] = math.max(tonumber(bucket['lastCompletedAtMs'] or 0), now_ms)
  elseif outcome == 'explicit_policy_failure' then
    bucket['explicitPolicyFailures'] = increment(bucket['explicitPolicyFailures'])
    bucket['lastFailureAtMs'] = math.max(tonumber(bucket['lastFailureAtMs'] or 0), now_ms)
  elseif outcome == 'transport_failure' then
    bucket['localTransportFailures'] = increment(bucket['localTransportFailures'])
    bucket['lastFailureAtMs'] = math.max(tonumber(bucket['lastFailureAtMs'] or 0), now_ms)
  elseif outcome == 'timeout' then
    bucket['localTransportFailures'] = increment(bucket['localTransportFailures'])
    bucket['timeouts'] = increment(bucket['timeouts'])
    bucket['lastFailureAtMs'] = math.max(tonumber(bucket['lastFailureAtMs'] or 0), now_ms)
  elseif outcome == 'read_interruption' then
    bucket['localTransportFailures'] = increment(bucket['localTransportFailures'])
    bucket['readInterruptions'] = increment(bucket['readInterruptions'])
    bucket['lastFailureAtMs'] = math.max(tonumber(bucket['lastFailureAtMs'] or 0), now_ms)
  elseif outcome == 'incomplete_response' then
    bucket['localTransportFailures'] = increment(bucket['localTransportFailures'])
    bucket['incompleteResponses'] = increment(bucket['incompleteResponses'])
    bucket['lastFailureAtMs'] = math.max(tonumber(bucket['lastFailureAtMs'] or 0), now_ms)
  elseif outcome == 'unknown' then
    bucket['unknownOutcomes'] = increment(bucket['unknownOutcomes'])
  elseif outcome == 'client_cancellation' then
    bucket['clientCancellations'] = increment(bucket['clientCancellations'])
  end
  if input['firstByteMs'] ~= nil and outcome ~= 'unknown' and outcome ~= 'client_cancellation' then
    local sample = tonumber(input['firstByteMs'])
    bucket['firstByteSampleCount'] = increment(bucket['firstByteSampleCount'])
    bucket['firstByteSumMs'] = add(bucket['firstByteSumMs'], sample)
    local index = histogram_index(sample)
    bucket['firstByteHistogram'][index] = increment(bucket['firstByteHistogram'][index])
  end
end

cleanup_registries()
local operation = input['operation']
local attempt = load_expiring(attempt_key, attempt_registry_key)

if operation == 'record_attempt' then
  if attempt then
    local status = attempt['requestedScopeKey'] == input['requestedScopeKey'] and 'idempotent' or 'attempt_conflict'
    return response(status, input['requestedScope'], attempt['effectiveScope'])
  end
  if tonumber(redis.call('ZCARD', attempt_registry_key)) >= attempt_capacity then
    metric('attemptCapacityRefusals')
    return response('attempt_capacity_exhausted', input['requestedScope'], input['requestedScope'])
  end

  local entry = load_expiring(requested_entry_key, hot_registry_key)
  local effective_entry_key = requested_entry_key
  local effective_scope = input['requestedScope']
  local effective_scope_key = input['requestedScopeKey']
  local effective_kind = 'requested'
  local status = 'applied'
  if not entry then
    if tonumber(redis.call('ZCARD', hot_registry_key)) < key_capacity then
      entry = new_entry(input['requestedScopeKey'], input['requestedScope'])
    else
      local fallback_entry = load_expiring(fallback_entry_key, hot_registry_key)
      if not fallback_entry then
        metric('keyCreationRefusals')
        return response('key_capacity_exhausted', input['requestedScope'], input['requestedScope'])
      end
      entry = fallback_entry
      effective_entry_key = fallback_entry_key
      effective_scope = input['fallbackScope']
      effective_scope_key = input['fallbackScopeKey']
      effective_kind = 'fallback'
      status = 'degraded_to_protocol'
      metric('highCardinalityDegradations')
    end
  end
  local bucket = current_bucket(entry)
  bucket['attempts'] = increment(bucket['attempts'])
  persist_entry(effective_entry_key, entry)
  persist_attempt({
    attemptId = input['attemptId'],
    requestedScopeKey = input['requestedScopeKey'],
    effectiveScopeKey = effective_scope_key,
    effectiveScope = effective_scope,
    effectiveKind = effective_kind
  })
  return response(status, input['requestedScope'], effective_scope)
end

if not attempt then return response('attempt_not_found') end
if attempt['requestedScopeKey'] ~= input['requestedScopeKey'] then
  return response('attempt_conflict', nil, attempt['effectiveScope'])
end
if attempt['terminal'] then
  local status = same_terminal(attempt['terminal']) and 'idempotent' or 'terminal_conflict'
  return response(status, nil, attempt['effectiveScope'], attempt['terminal'])
end

local terminal_owner = load_expiring(terminal_outcome_key, terminal_registry_key)
if terminal_owner and terminal_owner['attemptId'] ~= input['attemptId'] then
  return response('terminal_outcome_conflict', nil, attempt['effectiveScope'])
end

local effective_entry_key = attempt['effectiveKind'] == 'fallback' and fallback_entry_key or requested_entry_key
local entry = load_expiring(effective_entry_key, hot_registry_key)
if not entry then
  if tonumber(redis.call('ZCARD', hot_registry_key)) >= key_capacity then
    metric('terminalQualityKeyMisses')
    return response('quality_key_unavailable', nil, attempt['effectiveScope'])
  end
  entry = new_entry(attempt['effectiveScopeKey'], attempt['effectiveScope'])
end

local terminal = {
  terminalOutcomeId = input['terminalOutcomeId'],
  outcomeClass = input['outcomeClass'],
  failureScope = input['failureScope'],
  source = input['source'],
  createdAtMs = now_ms
}
local bucket = current_bucket(entry)
apply_terminal(bucket)
attempt['terminal'] = terminal
persist_entry(effective_entry_key, entry)
persist_attempt(attempt)
local terminal_owner_record = { attemptId = input['attemptId'], expiresAtMs = now_ms + terminal_ttl_ms }
redis.call('SET', terminal_outcome_key, cjson.encode(terminal_owner_record), 'PX', terminal_ttl_ms)
redis.call('ZADD', terminal_registry_key, terminal_owner_record['expiresAtMs'], terminal_outcome_key)
return response('applied', nil, attempt['effectiveScope'], terminal)
`

export const redisHotQualityReadEntryScript = String.raw`
local raw = redis.call('GET', KEYS[1])
if not raw then return nil end
local entry = cjson.decode(raw)
if tonumber(entry['expiresAtMs'] or 0) <= tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  redis.call('ZREM', KEYS[2], KEYS[1])
  return nil
end
return raw
`

export const redisHotQualityReadTerminalScript = String.raw`
local raw = redis.call('GET', KEYS[1])
if not raw then return nil end
local attempt = cjson.decode(raw)
if tonumber(attempt['expiresAtMs'] or 0) <= tonumber(ARGV[1]) then
  redis.call('DEL', KEYS[1])
  redis.call('ZREM', KEYS[2], KEYS[1])
  return nil
end
if not attempt['terminal'] then return nil end
return cjson.encode(attempt['terminal'])
`

export const redisHotQualityStatsScript = String.raw`
local now_ms = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now_ms)
local function metric(name)
  return tonumber(redis.call('HGET', KEYS[4], name) or 0)
end
return cjson.encode({
  keyCount = redis.call('ZCARD', KEYS[1]),
  attemptIdentityCount = redis.call('ZCARD', KEYS[2]),
  terminalIdentityCount = redis.call('ZCARD', KEYS[3]),
  keyCreationRefusals = metric('keyCreationRefusals'),
  highCardinalityDegradations = metric('highCardinalityDegradations'),
  attemptCapacityRefusals = metric('attemptCapacityRefusals'),
  terminalQualityKeyMisses = metric('terminalQualityKeyMisses')
})
`

function redisHotQualityEntryKey(keys: RedisHotQualityStoreKeys, scopeKey: string): string {
  return `${keys.prefix}:entry:${redisIdentityHash(scopeKey)}`
}

function redisHotQualityAttemptKey(keys: RedisHotQualityStoreKeys, attemptId: string): string {
  return `${keys.prefix}:attempt:${redisIdentityHash(attemptId)}`
}

function redisHotQualityTerminalKey(keys: RedisHotQualityStoreKeys, terminalOutcomeId: string): string {
  return `${keys.prefix}:terminal:${redisIdentityHash(terminalOutcomeId)}`
}

function redisIdentityHash(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function parseRedisHotQualityEntry(value: string): RedisHotQualityEntry {
  const parsed = JSON.parse(value) as Partial<RedisHotQualityEntry>
  if (!parsed.scopeKey || !parsed.scope || !parsed.buckets || typeof parsed.expiresAtMs !== 'number') {
    throw new Error('Redis 热质量 entry 结构无效')
  }
  return {
    scopeKey: parsed.scopeKey,
    scope: normalizeHotQualityScope(parsed.scope),
    buckets: parsed.buckets,
    expiresAtMs: parsed.expiresAtMs
  }
}

function parseTerminalRecord(value: string): HotQualityTerminalRecord {
  const parsed = JSON.parse(value) as Partial<HotQualityTerminalRecord>
  if (!parsed.terminalOutcomeId || !parsed.outcomeClass || !parsed.failureScope || !parsed.source || typeof parsed.createdAtMs !== 'number') {
    throw new Error('Redis 热质量终态结构无效')
  }
  return parsed as HotQualityTerminalRecord
}

function redisString(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  if (Buffer.isBuffer(value)) return value.toString('utf8')
  return undefined
}

function numericValue(value: unknown, name: string): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(numeric) || numeric < 0) throw new Error(`Redis 热质量 ${name} 返回值无效`)
  return numeric
}

function requiredText(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`Redis 热质量缺少 ${name}`)
  return normalized
}

function boundedIdentity(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized || normalized.length > 256) throw new Error(`${name} 必须是 1 到 256 字符`)
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new Error('nowMs 必须是非负安全整数')
  return value
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${name} 必须是正整数`)
  return value
}

function assertOutcomeClass(value: HotQualityTerminalOutcomeClass): void {
  if (!['completed_response', 'explicit_policy_failure', 'transport_failure', 'timeout', 'read_interruption', 'incomplete_response', 'unknown', 'client_cancellation'].includes(value)) {
    throw new Error('热质量 outcomeClass 非法')
  }
}

function assertFailureScope(value: HotQualityFailureScope): void {
  if (!['none', 'key', 'protocol_model', 'account', 'upstream_bucket'].includes(value)) {
    throw new Error('热质量 failureScope 非法')
  }
}

function assertTerminalSource(value: HotQualityTerminalSource): void {
  if (!['gateway_transport', 'explicit_policy', 'request_lifecycle'].includes(value)) {
    throw new Error('热质量 terminal source 非法')
  }
}
