import { createHash } from 'node:crypto'

import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import {
  SAME_TIER_EXPLORATION_IDENTITY_CAPACITY,
  SAME_TIER_EXPLORATION_POOL_CAPACITY,
  SAME_TIER_EXPLORATION_STATE_TTL_MS,
  cloneSameTierExplorationState,
  emptySameTierExplorationState,
  normalizeSameTierExplorationState,
  type SameTierExplorationState,
  type SameTierExplorationStore
} from './same-tier-exploration-store.js'

export interface RedisSameTierExplorationStoreOptions {
  redisUrl: string
  name?: string
  stateTtlMs?: number
  poolCapacity?: number
  now?: () => number
}

export class RedisSameTierExplorationStore implements SameTierExplorationStore {
  private readonly redisUrl: string
  private readonly prefix: string
  private readonly stateTtlMs: number
  private readonly poolCapacity: number
  private readonly now: () => number

  constructor(options: RedisSameTierExplorationStoreOptions) {
    this.redisUrl = requiredKey(options.redisUrl, 'redisUrl')
    this.prefix = redisSameTierExplorationStoreKeys(options.name ?? 'gateway').prefix
    this.stateTtlMs = positiveInteger(options.stateTtlMs ?? SAME_TIER_EXPLORATION_STATE_TTL_MS, 'stateTtlMs')
    this.poolCapacity = positiveInteger(options.poolCapacity ?? SAME_TIER_EXPLORATION_POOL_CAPACITY, 'poolCapacity')
    this.now = options.now ?? Date.now
  }

  async get(input: { poolKey: string; nowMs?: number }): Promise<SameTierExplorationState> {
    const result = await this.mutate({ operation: 'get', poolKey: input.poolKey, nowMs: normalizedNow(input.nowMs ?? this.now()) })
    return result.state
  }

  async accrue(input: { poolKey: string; accrualToken: string; eligible: boolean; nowMs?: number }): Promise<SameTierExplorationState> {
    const result = await this.mutate({
      operation: 'accrue',
      poolKey: input.poolKey,
      accrualToken: requiredKey(input.accrualToken, 'accrualToken'),
      eligible: input.eligible,
      nowMs: normalizedNow(input.nowMs ?? this.now())
    })
    return result.state
  }

  async reserve(input: {
    poolKey: string
    reservationId: string
    accountRuntimeKey: string
    leaseUntilMs: number
    nowMs?: number
  }): Promise<{
    status: 'reserved' | 'credit_unavailable' | 'pool_busy' | 'target_cooldown' | 'reservation_conflict'
    state: SameTierExplorationState
    reservation?: { reservationId: string; accountRuntimeKey: string; leaseUntilMs: number }
  }> {
    const nowMs = normalizedNow(input.nowMs ?? this.now())
    const leaseUntilMs = normalizedNow(input.leaseUntilMs)
    if (leaseUntilMs <= nowMs) throw new RangeError('leaseUntilMs 必须晚于 nowMs')
    if (leaseUntilMs > nowMs + this.stateTtlMs) throw new RangeError('leaseUntilMs 不得晚于 pool TTL')
    return this.mutate({
      operation: 'reserve',
      poolKey: input.poolKey,
      reservationId: requiredKey(input.reservationId, 'reservationId'),
      accountRuntimeKey: requiredKey(input.accountRuntimeKey, 'accountRuntimeKey'),
      leaseUntilMs,
      nowMs
    }) as Promise<{
      status: 'reserved' | 'credit_unavailable' | 'pool_busy' | 'target_cooldown' | 'reservation_conflict'
      state: SameTierExplorationState
      reservation?: { reservationId: string; accountRuntimeKey: string; leaseUntilMs: number }
    }>
  }

  async settle(input: {
    poolKey: string
    reservationId: string
    accountRuntimeKey: string
    outcome: 'dispatched' | 'not_dispatched'
    nowMs?: number
  }): Promise<{ status: 'applied' | 'idempotent' | 'reservation_not_found' | 'reservation_conflict'; state: SameTierExplorationState }> {
    return this.mutate({
      operation: 'settle',
      poolKey: input.poolKey,
      reservationId: requiredKey(input.reservationId, 'reservationId'),
      accountRuntimeKey: requiredKey(input.accountRuntimeKey, 'accountRuntimeKey'),
      outcome: input.outcome,
      nowMs: normalizedNow(input.nowMs ?? this.now())
    }) as Promise<{ status: 'applied' | 'idempotent' | 'reservation_not_found' | 'reservation_conflict'; state: SameTierExplorationState }>
  }

  private async mutate(input: Record<string, unknown> & { operation: string; poolKey: string; nowMs: number }): Promise<{
    status?: string
    state: SameTierExplorationState
    reservation?: { reservationId: string; accountRuntimeKey: string; leaseUntilMs: number }
  }> {
    const poolKey = requiredKey(input.poolKey, 'poolKey')
    const raw = await (await this.client()).eval(sameTierExplorationMutationScript, {
      keys: [this.stateKey(poolKey), `${this.prefix}:registry`],
      arguments: [
        String(input.nowMs),
        String(this.stateTtlMs),
        String(this.poolCapacity),
        String(SAME_TIER_EXPLORATION_IDENTITY_CAPACITY),
        input.operation,
        JSON.stringify(input)
      ]
    })
    const encoded = redisString(raw)
    if (!encoded) throw new Error('Redis 同层探索状态返回值无效')
    const parsed = JSON.parse(encoded) as {
      status?: string
      state?: SameTierExplorationState
      reservation?: { reservationId: string; accountRuntimeKey: string; leaseUntilMs: number }
    }
    if (!parsed.state) throw new Error('Redis 同层探索状态结构无效')
    const state = normalizeSameTierExplorationState({
      ...parsed.state,
      // Older Redis Lua cjson builds encode an empty table as `{}` rather than
      // an array. Normalize that wire representation before applying the
      // shared store invariants.
      reservations: Array.isArray(parsed.state.reservations) ? parsed.state.reservations : [],
      accruedTokens: Array.isArray(parsed.state.accruedTokens) ? parsed.state.accruedTokens : [],
      settledReservationIds: Array.isArray(parsed.state.settledReservationIds) ? parsed.state.settledReservationIds : []
    }, input.nowMs)
    return {
      status: parsed.status,
      state: cloneSameTierExplorationState(state),
      reservation: parsed.reservation ? { ...parsed.reservation } : undefined
    }
  }

  private stateKey(poolKey: string): string {
    return `${this.prefix}:pool:${createHash('sha256').update(poolKey).digest('hex')}`
  }

  private client(): Promise<RedisCommandClient> {
    return getRedisClient(this.redisUrl)
  }
}

export function redisSameTierExplorationStoreKeys(name = 'gateway'): {
  prefix: string
  registry: string
} {
  const prefix = redisNamespacedKey(`juhe-ai:same-tier-exploration:${safeName(name)}`)
  return { prefix, registry: `${prefix}:registry` }
}

export const sameTierExplorationMutationScript = String.raw`
local now_ms = tonumber(ARGV[1])
local ttl_ms = tonumber(ARGV[2])
local pool_capacity = tonumber(ARGV[3])
local identity_capacity = tonumber(ARGV[4])
local operation = ARGV[5]
local input = cjson.decode(ARGV[6])
local function empty_array()
  return {}
end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
local raw = redis.call('GET', KEYS[1])
local state
if raw then
  local decoded = cjson.decode(raw)
  if tonumber(decoded['expiresAtMs'] or 0) > now_ms then state = decoded end
end
if not state then
  redis.call('ZREM', KEYS[2], KEYS[1])
  state = {
    poolKey = input['poolKey'],
    credit = 0,
    cursor = 0,
    reservations = empty_array(),
    cooldownUntilMsByRuntimeKey = {},
    accruedTokens = empty_array(),
    settledReservationIds = empty_array(),
    expiresAtMs = now_ms + ttl_ms
  }
  if tonumber(redis.call('ZCARD', KEYS[2])) >= pool_capacity then
    local capacity_status = operation == 'reserve' and 'credit_unavailable'
      or operation == 'settle' and 'reservation_not_found'
      or 'capacity_exhausted'
    return cjson.encode({ status = capacity_status, state = state })
  end
end
state['poolKey'] = input['poolKey']
state['reservations'] = state['reservations'] or empty_array()
state['cooldownUntilMsByRuntimeKey'] = state['cooldownUntilMsByRuntimeKey'] or {}
state['accruedTokens'] = state['accruedTokens'] or empty_array()
state['settledReservationIds'] = state['settledReservationIds'] or empty_array()
local function has_value(values, target)
  for _, value in ipairs(values or {}) do
    if value == target then return true end
  end
  return false
end
local active_reservations = {}
for _, reservation in ipairs(state['reservations'] or {}) do
  if tonumber(reservation['leaseUntilMs'] or 0) > now_ms then
    table.insert(active_reservations, reservation)
  elseif not has_value(state['settledReservationIds'], reservation['reservationId']) then
    table.insert(state['settledReservationIds'], reservation['reservationId'])
  end
end
state['reservations'] = #active_reservations == 0 and empty_array() or active_reservations
while #state['settledReservationIds'] > identity_capacity do table.remove(state['settledReservationIds'], 1) end
for runtime_key, until_ms in pairs(state['cooldownUntilMsByRuntimeKey'] or {}) do
  if tonumber(until_ms or 0) <= now_ms then
    state['cooldownUntilMsByRuntimeKey'][runtime_key] = nil
  end
end
local status = nil
local reservation = nil
if operation == 'accrue' then
  local token = input['accrualToken']
  if input['eligible'] and not has_value(state['accruedTokens'], token)
      and #(state['accruedTokens'] or {}) < identity_capacity then
    state['credit'] = math.min(1, tonumber(state['credit'] or 0) + 0.05)
    table.insert(state['accruedTokens'], token)
  end
elseif operation == 'reserve' then
  local reservation_id = input['reservationId']
  local runtime_key = input['accountRuntimeKey']
  for _, existing in ipairs(state['reservations']) do
    if existing['reservationId'] == reservation_id then
      status = existing['accountRuntimeKey'] == runtime_key and 'reserved' or 'reservation_conflict'
      reservation = existing
    end
  end
  if not status and has_value(state['settledReservationIds'], reservation_id) then
    status = 'reservation_conflict'
  end
  if not status then
    if tonumber(state['credit'] or 0) < 1 then
      status = 'credit_unavailable'
    elseif #state['reservations'] > 0 then
      status = 'pool_busy'
    else
      if not status and tonumber(state['cooldownUntilMsByRuntimeKey'][runtime_key] or 0) > now_ms then
        status = 'target_cooldown'
      end
      if not status and state['cooldownUntilMsByRuntimeKey'][runtime_key] == nil then
        local cooldown_count = 0
        for _ in pairs(state['cooldownUntilMsByRuntimeKey'] or {}) do cooldown_count = cooldown_count + 1 end
        if cooldown_count >= identity_capacity then status = 'target_cooldown' end
      end
      if not status and tonumber(input['leaseUntilMs'] or 0) <= now_ms then
        status = 'reservation_conflict'
      end
      if not status and tonumber(input['leaseUntilMs'] or 0) > now_ms + ttl_ms then
        status = 'reservation_conflict'
      end
      if not status then
        reservation = { reservationId = reservation_id, accountRuntimeKey = runtime_key, leaseUntilMs = tonumber(input['leaseUntilMs']) }
        table.insert(state['reservations'], reservation)
        status = 'reserved'
      end
    end
  end
elseif operation == 'settle' then
  local reservation_id = input['reservationId']
  local runtime_key = input['accountRuntimeKey']
  if has_value(state['settledReservationIds'], reservation_id) then
    status = 'idempotent'
  else
    local found = nil
    for index, existing in ipairs(state['reservations']) do
      if existing['reservationId'] == reservation_id then found = { index = index, value = existing } end
    end
    if not found then
      status = 'reservation_not_found'
    elseif found.value['accountRuntimeKey'] ~= runtime_key then
      status = 'reservation_conflict'
    else
      table.remove(state['reservations'], found.index)
      table.insert(state['settledReservationIds'], reservation_id)
      while #state['settledReservationIds'] > identity_capacity do table.remove(state['settledReservationIds'], 1) end
      if input['outcome'] == 'dispatched' then
        state['credit'] = math.max(0, tonumber(state['credit'] or 0) - 1)
        state['cursor'] = tonumber(state['cursor'] or 0) >= 9007199254740991 and 0 or tonumber(state['cursor'] or 0) + 1
        state['cooldownUntilMsByRuntimeKey'][runtime_key] = now_ms + 60000
      end
      status = 'applied'
    end
  end
elseif operation == 'get' then
  status = 'read'
end
state['expiresAtMs'] = now_ms + ttl_ms
redis.call('SET', KEYS[1], cjson.encode(state), 'PX', ttl_ms)
redis.call('ZADD', KEYS[2], state['expiresAtMs'], KEYS[1])
return cjson.encode({ status = status, state = state, reservation = reservation })
`

function redisString(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  if (Buffer.isBuffer(value)) return value.toString('utf8')
  return undefined
}

function safeName(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'gateway'
}

function requiredKey(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized || normalized.length > 512) throw new RangeError(`${name} 必须是 1 到 512 字符`)
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isSafeInteger(value) || value < 0) throw new RangeError('时间必须是非负安全整数')
  return value
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value <= 0) throw new RangeError(`${name} 必须是正整数`)
  return value
}
