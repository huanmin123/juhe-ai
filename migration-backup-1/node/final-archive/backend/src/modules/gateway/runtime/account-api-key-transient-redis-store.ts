import { createHash, randomUUID } from 'node:crypto'

import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import type { AccountApiKeyRuntimeStatus } from '../../../storage/account-api-key-rotation.js'

type FailureStatus = Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>

export interface AccountApiKeyTransientTarget {
  accountId: string
  keyFingerprint: string
  keyIndex?: number
}

export interface AccountApiKeyTransientState {
  schemaVersion: 1
  accountId: string
  keyFingerprint: string
  keyIndex?: number
  generation: string
  lastObservedAtMs: number
  observationKind: 'failure' | 'success'
  failureCount: number
  status?: FailureStatus
  suppressUntilMs?: number
}

export interface AccountApiKeyTransientMutationResult {
  applied: boolean
  reason: 'applied' | 'stale_generation' | 'missing_state'
  state?: AccountApiKeyTransientState
}

export interface AccountApiKeyTransientDispatchState {
  state: AccountApiKeyTransientState
  suppressed: boolean
}

export interface AccountApiKeyTransientStateStore {
  recordFailure(input: {
    target: AccountApiKeyTransientTarget
    status: FailureStatus
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult>
  recordSuccess(input: {
    target: AccountApiKeyTransientTarget
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult>
  loadMany(accountId: string, keyFingerprints: string[]): Promise<AccountApiKeyTransientDispatchState[]>
}

export interface RedisAccountApiKeyTransientStateStoreOptions {
  redisUrl: string
  name?: string
  stateTtlMs?: number
  suppressionDelayMs?: readonly number[]
  failureCounterWindowMs?: number
  allowUnsafeShortStateTtlForTest?: boolean
}

const minimumStateTtlMs = 25 * 60 * 60_000
const defaultStateTtlMs = 48 * 60 * 60_000
const defaultSuppressionDelayMs = [3_000, 5_000, 10_000] as const
const defaultFailureCounterWindowMs = 10 * 60_000

export class RedisAccountApiKeyTransientStateStore implements AccountApiKeyTransientStateStore {
  private readonly redisUrl: string
  private readonly keyPrefix: string
  private readonly stateTtlMs: number
  private readonly suppressionDelayMs: readonly number[]
  private readonly failureCounterWindowMs: number

  constructor(options: RedisAccountApiKeyTransientStateStoreOptions) {
    this.redisUrl = requiredText(options.redisUrl, 'redisUrl')
    const name = sanitizeRedisKeyPart(options.name ?? 'gateway-account-api-key-transient-avoidance')
    this.keyPrefix = redisNamespacedKey(`juhe-ai:state:${name}:state:`)
    this.stateTtlMs = positiveInteger(options.stateTtlMs ?? defaultStateTtlMs, 'stateTtlMs')
    this.suppressionDelayMs = normalizeSuppressionDelays(options.suppressionDelayMs ?? defaultSuppressionDelayMs)
    this.failureCounterWindowMs = positiveInteger(options.failureCounterWindowMs ?? defaultFailureCounterWindowMs, 'failureCounterWindowMs')
    if (!options.allowUnsafeShortStateTtlForTest && this.stateTtlMs < minimumStateTtlMs) {
      throw new Error(`stateTtlMs 不得少于 ${minimumStateTtlMs}ms，必须覆盖网关最大在途请求`)
    }
    if (this.stateTtlMs < Math.max(...this.suppressionDelayMs)) {
      throw new Error('stateTtlMs 不得短于最大 suppression delay')
    }
  }

  recordFailure(input: {
    target: AccountApiKeyTransientTarget
    status: FailureStatus
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult> {
    return this.mutate({
      operation: 'failure',
      target: normalizeTarget(input.target),
      status: input.status,
      expectedGeneration: requiredText(input.expectedGeneration, 'expectedGeneration')
    })
  }

  recordSuccess(input: {
    target: AccountApiKeyTransientTarget
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult> {
    return this.mutate({
      operation: 'success',
      target: normalizeTarget(input.target),
      expectedGeneration: requiredText(input.expectedGeneration, 'expectedGeneration')
    })
  }

  async loadMany(accountIdInput: string, keyFingerprintsInput: string[]): Promise<AccountApiKeyTransientDispatchState[]> {
    const accountId = requiredText(accountIdInput, 'accountId')
    const keyFingerprints = [...new Set(keyFingerprintsInput.map((value) => value.trim()).filter(Boolean))]
    if (!keyFingerprints.length) return []
    const redisKeys = keyFingerprints.map((keyFingerprint) => this.stateKey({ accountId, keyFingerprint }))
    const raw = await (await this.client()).eval(redisAccountApiKeyTransientLoadScript, {
      keys: redisKeys,
      arguments: [
        String(this.stateTtlMs),
        JSON.stringify(keyFingerprints.map((keyFingerprint) => ({
          accountId,
          keyFingerprint,
          generation: randomUUID()
        })))
      ]
    })
    if (typeof raw !== 'string') throw new Error('Redis API Key transient load 返回值无效')
    const parsed = JSON.parse(raw) as { states?: unknown }
    if (!Array.isArray(parsed.states)) throw new Error('Redis API Key transient load 结构无效')
    const states: AccountApiKeyTransientDispatchState[] = []
    for (const item of parsed.states) {
      if (!item || typeof item !== 'object') continue
      const candidate = item as { state?: unknown; suppressed?: unknown }
      const state = parseStateValue(candidate.state)
      if (
        !state
        || state.accountId !== accountId
        || !keyFingerprints.includes(state.keyFingerprint)
        || typeof candidate.suppressed !== 'boolean'
      ) {
        continue
      }
      states.push({ state, suppressed: candidate.suppressed })
    }
    return states
  }

  private async mutate(input: {
    operation: 'failure' | 'success'
    target: AccountApiKeyTransientTarget
    status?: FailureStatus
    expectedGeneration: string
  }): Promise<AccountApiKeyTransientMutationResult> {
    const raw = await (await this.client()).eval(redisAccountApiKeyTransientMutationScript, {
      keys: [this.stateKey(input.target)],
      arguments: [
        input.operation,
        input.target.accountId,
        input.target.keyFingerprint,
        input.target.keyIndex === undefined ? '' : String(input.target.keyIndex),
        input.status ?? '',
        input.expectedGeneration,
        randomUUID(),
        String(this.stateTtlMs),
        JSON.stringify(this.suppressionDelayMs),
        String(this.failureCounterWindowMs)
      ]
    })
    if (typeof raw !== 'string') throw new Error('Redis API Key transient mutation 返回值无效')
    const parsed = JSON.parse(raw) as { applied?: unknown; reason?: unknown; state?: unknown }
    const state = parsed.state === undefined ? undefined : parseStateValue(parsed.state)
    if (
      typeof parsed.applied !== 'boolean'
      || (parsed.reason !== 'applied' && parsed.reason !== 'stale_generation' && parsed.reason !== 'missing_state')
      || (parsed.state !== undefined && !state)
    ) {
      throw new Error('Redis API Key transient mutation 结构无效')
    }
    return { applied: parsed.applied, reason: parsed.reason, state }
  }

  async deleteManyForTest(targets: AccountApiKeyTransientTarget[]): Promise<void> {
    const keys = targets.map((target) => this.stateKey(normalizeTarget(target)))
    if (keys.length) await (await this.client()).sendCommand(['DEL', ...keys])
  }

  async setRawStateForTest(target: AccountApiKeyTransientTarget, rawValue: string): Promise<void> {
    await (await this.client()).set(
      this.stateKey(normalizeTarget(target)),
      rawValue,
      { PX: this.stateTtlMs }
    )
  }

  private stateKey(target: Pick<AccountApiKeyTransientTarget, 'accountId' | 'keyFingerprint'>): string {
    const identity = `${target.accountId}\u0000${target.keyFingerprint}`
    return `${this.keyPrefix}${createHash('sha256').update(identity).digest('hex')}`
  }

  private client(): Promise<RedisCommandClient> {
    return getRedisClient(this.redisUrl)
  }
}

// Failure and success share one key and one mutation. Success advances the generation;
// delayed observations from an older dispatch snapshot cannot cross that tombstone.
export const redisAccountApiKeyTransientMutationScript = String.raw`
local key = KEYS[1]
local operation = ARGV[1]
local account_id = ARGV[2]
local key_fingerprint = ARGV[3]
local key_index = ARGV[4]
local status = ARGV[5]
local expected_generation = ARGV[6]
local next_generation = ARGV[7]
local state_ttl_ms = tonumber(ARGV[8])
local suppression_delays = cjson.decode(ARGV[9])
local failure_counter_window_ms = tonumber(ARGV[10])
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local max_safe_integer = 9007199254740991

local function non_negative_safe_integer(value)
  if type(value) ~= 'number' then return false end
  local number = tonumber(value)
  return number and number >= 0 and number <= max_safe_integer and number == math.floor(number)
end

local current = nil
local current_raw = redis.call('GET', key)
if current_raw then
  local decoded, value = pcall(cjson.decode, current_raw)
  if decoded and type(value) == 'table'
    and value['schemaVersion'] == 1
    and value['accountId'] == account_id
    and value['keyFingerprint'] == key_fingerprint
    and type(value['generation']) == 'string'
    and string.len(value['generation']) > 0
    and non_negative_safe_integer(value['lastObservedAtMs'])
    and non_negative_safe_integer(value['failureCount'])
    and (value['keyIndex'] == nil or non_negative_safe_integer(value['keyIndex']))
    and (
      value['observationKind'] == 'success'
      or (
        value['observationKind'] == 'failure'
        and (value['status'] == 'temporary_unavailable' or value['status'] == 'rate_limited' or value['status'] == 'error')
        and non_negative_safe_integer(value['suppressUntilMs'])
      )
    ) then
    current = value
  end
end

if not current then
  return cjson.encode({ applied = false, reason = 'missing_state' })
end
local current_generation = tostring(current['generation'] or '')
if expected_generation ~= current_generation then
  return cjson.encode({ applied = false, reason = 'stale_generation', state = current })
end

local generation = current_generation
if operation == 'success' then
  generation = next_generation
end

local state = {
  schemaVersion = 1,
  accountId = account_id,
  keyFingerprint = key_fingerprint,
  generation = generation,
  lastObservedAtMs = now_ms,
  observationKind = operation,
  failureCount = 0
}
if key_index ~= '' then state['keyIndex'] = tonumber(key_index) end

if operation == 'failure' then
  local failure_count = 1
  if current and current['observationKind'] == 'failure'
    and tonumber(current['lastObservedAtMs'])
    and now_ms - tonumber(current['lastObservedAtMs']) <= failure_counter_window_ms then
    failure_count = math.min(#suppression_delays, tonumber(current['failureCount'] or 0) + 1)
  end
  local delay_ms = tonumber(suppression_delays[failure_count])
  state['failureCount'] = failure_count
  state['status'] = status
  state['suppressUntilMs'] = now_ms + delay_ms
end

redis.call('SET', key, cjson.encode(state), 'PX', state_ttl_ms)
return cjson.encode({ applied = true, reason = 'applied', state = state })
`

export const redisAccountApiKeyTransientLoadScript = String.raw`
local state_ttl_ms = tonumber(ARGV[1])
local identities = cjson.decode(ARGV[2])
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local max_safe_integer = 9007199254740991

local function non_negative_safe_integer(value)
  if type(value) ~= 'number' then return false end
  local number = tonumber(value)
  return number and number >= 0 and number <= max_safe_integer and number == math.floor(number)
end
local states = {}
for index, key in ipairs(KEYS) do
  local identity = identities[index]
  local raw = redis.call('GET', key)
  local state = nil
  if raw then
    local decoded, decoded_state = pcall(cjson.decode, raw)
    if decoded and type(decoded_state) == 'table'
      and decoded_state['schemaVersion'] == 1
      and decoded_state['accountId'] == identity['accountId']
      and decoded_state['keyFingerprint'] == identity['keyFingerprint']
      and type(decoded_state['generation']) == 'string'
      and string.len(decoded_state['generation']) > 0
      and non_negative_safe_integer(decoded_state['lastObservedAtMs'])
      and non_negative_safe_integer(decoded_state['failureCount'])
      and (decoded_state['keyIndex'] == nil or non_negative_safe_integer(decoded_state['keyIndex']))
      and (
        decoded_state['observationKind'] == 'success'
        or (
          decoded_state['observationKind'] == 'failure'
          and (decoded_state['status'] == 'temporary_unavailable' or decoded_state['status'] == 'rate_limited' or decoded_state['status'] == 'error')
          and non_negative_safe_integer(decoded_state['suppressUntilMs'])
        )
      ) then
      state = decoded_state
    end
  end
  if not state then
    state = {
      schemaVersion = 1,
      accountId = identity['accountId'],
      keyFingerprint = identity['keyFingerprint'],
      generation = identity['generation'],
      lastObservedAtMs = now_ms,
      observationKind = 'success',
      failureCount = 0
    }
    redis.call('SET', key, cjson.encode(state), 'PX', state_ttl_ms)
  end
  local suppressed = state['observationKind'] == 'failure'
    and tonumber(state['suppressUntilMs'])
    and tonumber(state['suppressUntilMs']) > now_ms
  table.insert(states, { state = state, suppressed = suppressed and true or false })
end
return cjson.encode({ nowMs = now_ms, states = states })
`

function parseStateValue(value: unknown): AccountApiKeyTransientState | undefined {
  if (!value || typeof value !== 'object') return undefined
  const candidate = value as Partial<AccountApiKeyTransientState>
  if (
    candidate.schemaVersion !== 1
    || typeof candidate.accountId !== 'string' || !candidate.accountId
    || typeof candidate.keyFingerprint !== 'string' || !candidate.keyFingerprint
    || typeof candidate.generation !== 'string' || !candidate.generation
    || !Number.isSafeInteger(candidate.lastObservedAtMs) || candidate.lastObservedAtMs! < 0
    || (candidate.observationKind !== 'failure' && candidate.observationKind !== 'success')
    || !Number.isSafeInteger(candidate.failureCount) || candidate.failureCount! < 0
    || (candidate.keyIndex !== undefined && (!Number.isSafeInteger(candidate.keyIndex) || candidate.keyIndex < 0))
  ) {
    return undefined
  }
  if (candidate.observationKind === 'failure') {
    if (
      (candidate.status !== 'temporary_unavailable' && candidate.status !== 'rate_limited' && candidate.status !== 'error')
      || !Number.isSafeInteger(candidate.suppressUntilMs) || candidate.suppressUntilMs! < 0
    ) {
      return undefined
    }
  }
  return candidate as AccountApiKeyTransientState
}

function normalizeTarget(target: AccountApiKeyTransientTarget): AccountApiKeyTransientTarget {
  return {
    accountId: requiredText(target.accountId, 'accountId'),
    keyFingerprint: requiredText(target.keyFingerprint, 'keyFingerprint'),
    keyIndex: target.keyIndex === undefined ? undefined : nonNegativeInteger(target.keyIndex, 'keyIndex')
  }
}

function normalizeSuppressionDelays(values: readonly number[]): readonly number[] {
  if (!values.length) throw new Error('suppressionDelayMs 不能为空')
  return values.map((value, index) => positiveInteger(value, `suppressionDelayMs[${index}]`))
}

function requiredText(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`${name} 不能为空`)
  return normalized
}

function sanitizeRedisKeyPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'default'
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new Error(`${name} 必须是正整数`)
  return value
}

function nonNegativeInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new Error(`${name} 必须是非负整数`)
  return value
}
