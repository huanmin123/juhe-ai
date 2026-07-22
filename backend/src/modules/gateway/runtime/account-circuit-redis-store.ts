import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import {
  accountCircuitScopeKey,
  cloneAccountCircuitState,
  closedAccountCircuitState,
  type AccountCircuitMutationResult,
  type AccountCircuitScope,
  type AccountCircuitState,
  type AccountCircuitStore,
  type AccountCircuitTransitionIdentity
} from './account-circuit-store.js'

export interface RedisAccountCircuitStoreOptions {
  redisUrl: string
  name?: string
  capacity: number
  closedRetentionMs?: number
  replayLimitPerScope?: number
  now?: () => number
}

interface RedisAccountCircuitKeys {
  states: string
  due: string
  closed: string
}

type RedisAccountCircuitOperation =
  | 'get'
  | 'suspect'
  | 'acquire_confirmation'
  | 'complete_confirmation'
  | 'acquire_canary'
  | 'complete_canary'
  | 'replace_revision'

export class RedisAccountCircuitStore implements AccountCircuitStore {
  private readonly redisUrl: string
  private readonly keys: RedisAccountCircuitKeys
  private readonly capacity: number
  private readonly closedRetentionMs: number
  private readonly replayLimitPerScope: number
  private readonly now: () => number

  constructor(options: RedisAccountCircuitStoreOptions) {
    this.redisUrl = requiredValue(options.redisUrl, 'redisUrl')
    this.keys = redisAccountCircuitStoreKeys(options.name ?? 'gateway-account-circuit')
    this.capacity = positiveInteger(options.capacity, 'capacity')
    this.closedRetentionMs = positiveInteger(options.closedRetentionMs ?? 5 * 60_000, 'closedRetentionMs')
    this.replayLimitPerScope = positiveInteger(options.replayLimitPerScope ?? 64, 'replayLimitPerScope')
    this.now = options.now ?? Date.now
  }

  async get(scope: AccountCircuitScope, nowMs = this.now()): Promise<AccountCircuitState> {
    return (await this.execute('get', scope, { nowMs })).state
  }

  suspect(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    reason: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult> {
    return this.execute('suspect', input.scope, input)
  }

  acquireConfirmationLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
  }): Promise<AccountCircuitMutationResult> {
    return this.execute('acquire_confirmation', input.scope, input)
  }

  completeConfirmation(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
  }): Promise<AccountCircuitMutationResult> {
    return this.execute('complete_confirmation', input.scope, input)
  }

  acquireCanaryLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
  }): Promise<AccountCircuitMutationResult> {
    return this.execute('acquire_canary', input.scope, input)
  }

  completeCanary(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
  }): Promise<AccountCircuitMutationResult> {
    return this.execute('complete_canary', input.scope, input)
  }

  replaceDispatchRevision(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult> {
    return this.execute('replace_revision', input.scope, input)
  }

  async listDue(nowMs: number, limit: number): Promise<AccountCircuitState[]> {
    const now = normalizedNow(nowMs)
    const normalizedLimit = positiveInteger(limit, 'limit')
    const scopeKeys = stringArrayRedisResult(await (await this.client()).sendCommand([
      'ZRANGEBYSCORE',
      this.keys.due,
      '-inf',
      String(now),
      'LIMIT',
      '0',
      String(normalizedLimit)
    ]))
    const states: AccountCircuitState[] = []
    for (const scopeKey of scopeKeys) {
      const raw = await (await this.client()).sendCommand(['HGET', this.keys.states, scopeKey])
      const encoded = typeof raw === 'string' ? raw : Buffer.isBuffer(raw) ? raw.toString('utf8') : undefined
      if (!encoded) {
        await (await this.client()).sendCommand(['ZREM', this.keys.due, scopeKey])
        continue
      }
      const entry = parseRedisEntry(encoded)
      const state = await this.get(entry.state.scope, now)
      if (accountCircuitDueAtMs(state) <= now) states.push(state)
    }
    return states.slice(0, normalizedLimit)
  }

  async size(): Promise<number> {
    const result = await (await this.client()).eval(redisAccountCircuitSizeScript, {
      keys: [this.keys.states, this.keys.due, this.keys.closed],
      arguments: [String(normalizedNow(this.now())), String(this.capacity)]
    })
    return numericRedisResult(result)
  }

  private async execute(
    operation: RedisAccountCircuitOperation,
    scope: AccountCircuitScope,
    input: object
  ): Promise<AccountCircuitMutationResult> {
    const operationInput = input as Record<string, unknown>
    const nowMs = normalizedNow(typeof operationInput.nowMs === 'number' ? operationInput.nowMs : this.now())
    const payload = {
      ...operationInput,
      scope,
      scopeKey: accountCircuitScopeKey(scope),
      nowMs,
      closedState: closedAccountCircuitState(scope),
      operation
    }
    validateOperationPayload(operation, payload)
    const raw = await (await this.client()).eval(redisAccountCircuitTransitionScript, {
      keys: [this.keys.states, this.keys.due, this.keys.closed],
      arguments: [
        JSON.stringify(payload),
        String(this.capacity),
        String(this.closedRetentionMs),
        String(this.replayLimitPerScope)
      ]
    })
    const encoded = typeof raw === 'string' ? raw : Buffer.isBuffer(raw) ? raw.toString('utf8') : ''
    if (!encoded) throw new Error('Redis 账户电路转换返回值无效')
    const parsed = JSON.parse(encoded) as AccountCircuitMutationResult
    if (!parsed?.status || !parsed.state) throw new Error('Redis 账户电路转换结果结构无效')
    return { status: parsed.status, state: cloneAccountCircuitState(parsed.state) }
  }

  private client(): Promise<RedisCommandClient> {
    return getRedisClient(this.redisUrl)
  }
}

export function redisAccountCircuitStoreKeys(name: string): RedisAccountCircuitKeys {
  const safeName = name.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'gateway-account-circuit'
  const prefix = redisNamespacedKey(`juhe-ai:account-circuit:${safeName}`)
  return {
    states: `${prefix}:states`,
    due: `${prefix}:due`,
    closed: `${prefix}:closed`
  }
}

// Every transition, including lease-expiry normalization and index maintenance, runs in one Lua call.
export const redisAccountCircuitTransitionScript = String.raw`
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local input = cjson.decode(ARGV[1])
local capacity = tonumber(ARGV[2])
local closed_retention_ms = tonumber(ARGV[3])
local replay_limit = tonumber(ARGV[4])
local scope_key = input['scopeKey']
local now_ms = tonumber(input['nowMs'])

local function remove_scope(key)
  redis.call('HDEL', states_key, key)
  redis.call('ZREM', due_key, key)
  redis.call('ZREM', closed_key, key)
end

local function cleanup_closed()
  local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, capacity)
  for _, key in ipairs(expired) do remove_scope(key) end
end

local function due_at(state)
  if state['phase'] == 'CLOSED' then return nil end
  local lease = state['lease']
  if lease then return tonumber(lease['leaseUntilMs']) end
  if state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING' then
    return tonumber(state['retryAtMs'])
  end
  return tonumber(state['updatedAtMs'])
end

local function persist(entry)
  local state = entry['state']
  redis.call('HSET', states_key, scope_key, cjson.encode(entry))
  local due = due_at(state)
  if due then redis.call('ZADD', due_key, due, scope_key) else redis.call('ZREM', due_key, scope_key) end
  if state['phase'] == 'CLOSED' then
    redis.call('ZADD', closed_key, tonumber(entry['closedExpiresAtMs']), scope_key)
  else
    redis.call('ZREM', closed_key, scope_key)
  end
end

local function normalize(entry)
  if not entry then return nil end
  local state = entry['state']
  if state['phase'] == 'CLOSED' and tonumber(entry['closedExpiresAtMs'] or 0) <= now_ms then
    remove_scope(scope_key)
    return nil
  end
  local lease = state['lease']
  if lease and tonumber(lease['leaseUntilMs']) <= now_ms then
    if lease['kind'] == 'confirmation' then
      state['lease'] = nil
      state['updatedAtMs'] = now_ms
    else
      state['phase'] = state['halfOpenOrigin'] or 'OPEN'
      state['lease'] = nil
      state['halfOpenOrigin'] = nil
      state['retryAtMs'] = now_ms
      state['updatedAtMs'] = now_ms
    end
    persist(entry)
  end
  return entry
end

local function load_entry()
  local raw = redis.call('HGET', states_key, scope_key)
  if not raw then return nil end
  return normalize(cjson.decode(raw))
end

local function closed_state()
  return input['closedState']
end

local function response(status, state)
  return cjson.encode({ status = status, state = state })
end

local function replayed(entry)
  if not entry or not input['transitionId'] then return false end
  for _, transition_id in ipairs(entry['replayOrder'] or {}) do
    if transition_id == input['transitionId'] then return true end
  end
  return false
end

local function remember(entry)
  local order = entry['replayOrder'] or {}
  table.insert(order, input['transitionId'])
  while #order > replay_limit do table.remove(order, 1) end
  entry['replayOrder'] = order
end

local function apply(entry)
  remember(entry)
  persist(entry)
  return response('applied', entry['state'])
end

local function reserve_capacity()
  cleanup_closed()
  if tonumber(redis.call('HLEN', states_key)) < capacity then return true end
  local evict = redis.call('ZRANGE', closed_key, 0, 0)
  if #evict == 0 then return false end
  remove_scope(evict[1])
  return true
end

local function validate_identity(entry)
  if not entry then return response('not_found', closed_state()) end
  local state = entry['state']
  if tonumber(state['generation']) ~= tonumber(input['generation']) then return response('stale_generation', state) end
  if state['dispatchRevision'] ~= input['dispatchRevision'] then return response('stale_dispatch_revision', state) end
  return nil
end

local function clear_optional_state(state)
  state['openedAtMs'] = nil
  state['retryAtMs'] = nil
  state['failureReason'] = nil
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
end

local function close(entry)
  local state = entry['state']
  state['phase'] = 'CLOSED'
  state['transitionId'] = input['transitionId']
  state['backoffAttempt'] = 0
  state['recoverySuccessCount'] = 0
  state['updatedAtMs'] = now_ms
  clear_optional_state(state)
  entry['closedExpiresAtMs'] = now_ms + closed_retention_ms
  return apply(entry)
end

local backoffs = { 3000, 5000, 10000, 30000, 60000 }
local function open(entry)
  local state = entry['state']
  local attempt = tonumber(state['backoffAttempt'] or 0) + 1
  local index = math.min(#backoffs, attempt)
  state['phase'] = 'OPEN'
  state['transitionId'] = input['transitionId']
  state['backoffAttempt'] = attempt
  state['recoverySuccessCount'] = 0
  state['openedAtMs'] = now_ms
  state['retryAtMs'] = now_ms + backoffs[index]
  state['failureReason'] = input['reason'] or state['failureReason']
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['updatedAtMs'] = now_ms
  entry['closedExpiresAtMs'] = nil
  return apply(entry)
end

cleanup_closed()
local entry = load_entry()
local operation = input['operation']

if operation == 'get' then
  return response('applied', entry and entry['state'] or closed_state())
end

if operation == 'suspect' then
  if replayed(entry) then return response('idempotent', entry['state']) end
  if entry and entry['state']['phase'] ~= 'CLOSED' then return response('state_mismatch', entry['state']) end
  if not entry and not reserve_capacity() then return response('capacity_exhausted', closed_state()) end
  local generation = entry and tonumber(entry['state']['generation']) + 1 or 1
  local state = closed_state()
  state['phase'] = 'SUSPECT'
  state['generation'] = generation
  state['dispatchRevision'] = input['dispatchRevision']
  state['transitionId'] = input['transitionId']
  state['failureReason'] = input['reason']
  state['updatedAtMs'] = now_ms
  entry = entry or { replayOrder = {} }
  entry['state'] = state
  entry['closedExpiresAtMs'] = nil
  return apply(entry)
end

if operation == 'replace_revision' then
  if replayed(entry) then return response('idempotent', entry['state']) end
  if not entry and not reserve_capacity() then return response('capacity_exhausted', closed_state()) end
  local generation = entry and tonumber(entry['state']['generation']) + 1 or 1
  local state = closed_state()
  state['generation'] = generation
  state['dispatchRevision'] = input['dispatchRevision']
  state['transitionId'] = input['transitionId']
  state['updatedAtMs'] = now_ms
  entry = entry or { replayOrder = {} }
  entry['state'] = state
  entry['closedExpiresAtMs'] = now_ms + closed_retention_ms
  return apply(entry)
end

local invalid = validate_identity(entry)
if invalid then return invalid end
if replayed(entry) then return response('idempotent', entry['state']) end
local state = entry['state']

if operation == 'acquire_confirmation' then
  if state['phase'] ~= 'SUSPECT' or state['lease'] then return response('state_mismatch', state) end
  state['transitionId'] = input['transitionId']
  state['lease'] = { kind = 'confirmation', leaseId = input['leaseId'], leaseUntilMs = input['leaseUntilMs'] }
  state['updatedAtMs'] = now_ms
  return apply(entry)
end

if operation == 'complete_confirmation' then
  if state['phase'] ~= 'SUSPECT' then return response('state_mismatch', state) end
  local lease = state['lease']
  if not lease or lease['kind'] ~= 'confirmation' or lease['leaseId'] ~= input['leaseId'] then
    return response('lease_mismatch', state)
  end
  if input['outcome'] == 'framing_complete' then return close(entry) end
  if input['outcome'] == 'transport_failure' then return open(entry) end
  state['transitionId'] = input['transitionId']
  state['lease'] = nil
  state['updatedAtMs'] = now_ms
  return apply(entry)
end

if operation == 'acquire_canary' then
  if state['phase'] ~= 'OPEN' and state['phase'] ~= 'RECOVERING' then return response('state_mismatch', state) end
  if state['lease'] then return response('state_mismatch', state) end
  if state['phase'] == 'OPEN' and (not state['retryAtMs'] or tonumber(state['retryAtMs']) > now_ms) then
    return response('not_due', state)
  end
  local origin = state['phase']
  state['phase'] = 'HALF_OPEN'
  state['transitionId'] = input['transitionId']
  state['lease'] = {
    kind = origin == 'OPEN' and 'half_open' or 'recovery',
    leaseId = input['leaseId'],
    leaseUntilMs = input['leaseUntilMs']
  }
  state['halfOpenOrigin'] = origin
  state['updatedAtMs'] = now_ms
  return apply(entry)
end

if operation == 'complete_canary' then
  if state['phase'] ~= 'HALF_OPEN' then return response('state_mismatch', state) end
  local lease = state['lease']
  if not lease or lease['leaseId'] ~= input['leaseId'] then return response('lease_mismatch', state) end
  if input['outcome'] == 'transport_failure' then return open(entry) end
  if input['outcome'] == 'unknown' then
    state['phase'] = state['halfOpenOrigin'] or 'OPEN'
    state['transitionId'] = input['transitionId']
    state['lease'] = nil
    state['halfOpenOrigin'] = nil
    state['retryAtMs'] = now_ms
    state['updatedAtMs'] = now_ms
    return apply(entry)
  end
  local success_count = tonumber(state['recoverySuccessCount'] or 0) + 1
  if success_count >= 3 then return close(entry) end
  state['phase'] = 'RECOVERING'
  state['transitionId'] = input['transitionId']
  state['recoverySuccessCount'] = success_count
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['retryAtMs'] = now_ms
  state['updatedAtMs'] = now_ms
  return apply(entry)
end

return response('state_mismatch', state)
`

export const redisAccountCircuitSizeScript = String.raw`
local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
for _, scope_key in ipairs(expired) do
  redis.call('HDEL', KEYS[1], scope_key)
  redis.call('ZREM', KEYS[2], scope_key)
  redis.call('ZREM', KEYS[3], scope_key)
end
return redis.call('HLEN', KEYS[1])
`

function validateOperationPayload(operation: RedisAccountCircuitOperation, input: Record<string, unknown>): void {
  if (operation !== 'get') {
    requiredValue(input.transitionId, 'transitionId')
  }
  if (operation === 'suspect' || operation === 'replace_revision') {
    requiredValue(input.dispatchRevision, 'dispatchRevision')
  }
  if (operation === 'acquire_confirmation' || operation === 'acquire_canary') {
    requiredValue(input.leaseId, 'leaseId')
    const nowMs = normalizedNow(Number(input.nowMs))
    if (normalizedNow(Number(input.leaseUntilMs)) <= nowMs) {
      throw new Error('账户电路租约截止时间必须晚于当前时间')
    }
  }
  if (operation === 'complete_confirmation' || operation === 'complete_canary') {
    requiredValue(input.leaseId, 'leaseId')
    if (!['framing_complete', 'transport_failure', 'unknown'].includes(String(input.outcome))) {
      throw new Error('账户电路结果类型无效')
    }
  }
}

function parseRedisEntry(value: string): { state: AccountCircuitState } {
  const parsed = JSON.parse(value) as { state?: AccountCircuitState }
  if (!parsed.state) throw new Error('Redis 账户电路状态结构无效')
  return { state: parsed.state }
}

function accountCircuitDueAtMs(state: AccountCircuitState): number {
  if (state.phase === 'CLOSED') return Number.POSITIVE_INFINITY
  if (state.lease) return state.lease.leaseUntilMs
  if (state.phase === 'OPEN' || state.phase === 'RECOVERING') return state.retryAtMs ?? Number.POSITIVE_INFINITY
  return state.updatedAtMs
}

function requiredValue(value: unknown, name: string): string {
  const normalized = typeof value === 'string' ? value.trim() : ''
  if (!normalized) throw new Error(`账户电路操作缺少 ${name}`)
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isFinite(value)) throw new Error('账户电路时间必须是有限数值')
  return Math.max(0, Math.trunc(value))
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isFinite(value) || value < 1) throw new Error(`账户电路 ${name} 必须是正整数`)
  return Math.trunc(value)
}

function numericRedisResult(value: unknown): number {
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number)) throw new Error('Redis 账户电路数值返回无效')
  return number
}

function stringArrayRedisResult(value: unknown): string[] {
  if (!Array.isArray(value)) throw new Error('Redis 账户电路索引返回无效')
  return value.map((item) => Buffer.isBuffer(item) ? item.toString('utf8') : String(item))
}
