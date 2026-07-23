import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import {
  accountCircuitScopeKey,
  cloneAccountCircuitState,
  closedAccountCircuitState,
  type AccountCircuitEscalationResult,
  type AccountCircuitMutationResult,
  type AccountCircuitProtocolModelOpenEvidenceInput,
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
  escalation: string
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
    evidenceScopeKey?: string
  }): Promise<AccountCircuitMutationResult> {
    return this.execute('complete_canary', input.scope, input)
  }

  async recordProtocolModelOpenEvidence(
    input: AccountCircuitProtocolModelOpenEvidenceInput
  ): Promise<AccountCircuitEscalationResult> {
    const nowMs = normalizedNow(input.nowMs ?? this.now())
    positiveInteger(input.confirmedFailureCount, 'confirmedFailureCount')
    positiveInteger(input.windowMs, 'windowMs')
    positiveInteger(input.maxProtocolScopes, 'maxProtocolScopes')
    requiredValue(input.evidenceId, 'evidenceId')
    requiredValue(input.accountTransitionId, 'accountTransitionId')
    requiredValue(input.reason, 'reason')
    const accountScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: input.scope.accountRuntimeKey }
    const payload = {
      ...input,
      nowMs,
      scopeKey: accountCircuitScopeKey(input.scope),
      accountScope,
      accountScopeKey: accountCircuitScopeKey(accountScope),
      closedAccountState: closedAccountCircuitState(accountScope, input.dispatchRevision)
    }
    const raw = await (await this.client()).eval(redisAccountCircuitEscalationScript, {
      keys: [this.keys.states, this.keys.due, this.keys.closed, this.keys.escalation],
      arguments: [JSON.stringify(payload), String(this.capacity), String(this.closedRetentionMs)]
    })
    const encoded = typeof raw === 'string' ? raw : Buffer.isBuffer(raw) ? raw.toString('utf8') : ''
    if (!encoded) throw new Error('Redis 账户电路作用域升级返回值无效')
    const parsed = JSON.parse(encoded) as AccountCircuitEscalationResult
    if (!parsed?.status || !parsed.accountState) throw new Error('Redis 账户电路作用域升级结果结构无效')
    return { ...parsed, accountState: cloneAccountCircuitState(parsed.accountState) }
  }

  async clearAccountEscalationEvidence(input: {
    accountRuntimeKey: string
    dispatchRevision: string
    evidenceId: string
    nowMs?: number
  }): Promise<boolean> {
    const raw = await (await this.client()).eval(redisAccountCircuitClearEscalationScript, {
      keys: [this.keys.escalation],
      arguments: [
        requiredValue(input.accountRuntimeKey, 'accountRuntimeKey'),
        requiredValue(input.dispatchRevision, 'dispatchRevision'),
        requiredValue(input.evidenceId, 'evidenceId'),
        String(normalizedNow(input.nowMs ?? this.now()))
      ]
    })
    return numericRedisResult(raw) === 1
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

  async restore(rawState: AccountCircuitState, nowMs = this.now()): Promise<AccountCircuitMutationResult> {
    const state = cloneAccountCircuitState(rawState)
    const raw = await (await this.client()).eval(redisAccountCircuitRestoreScript, {
      keys: [this.keys.states, this.keys.due, this.keys.closed],
      arguments: [JSON.stringify(state), String(normalizedNow(nowMs)), String(this.closedRetentionMs)]
    })
    const encoded = typeof raw === 'string' ? raw : Buffer.isBuffer(raw) ? raw.toString('utf8') : ''
    if (!encoded) throw new Error('Redis 账户电路重建返回值无效')
    const parsed = JSON.parse(encoded) as AccountCircuitMutationResult
    return { status: parsed.status, state: cloneAccountCircuitState(parsed.state) }
  }

  async replaceAccountDispatchRevision(input: {
    accountRuntimeKey: string
    dispatchRevision: string
    transitionId: string
    nowMs?: number
  }): Promise<number> {
    const raw = await (await this.client()).eval(redisAccountCircuitAccountRevisionScript, {
      keys: [this.keys.states, this.keys.due, this.keys.closed, this.keys.escalation],
      arguments: [
        input.accountRuntimeKey,
        input.dispatchRevision,
        input.transitionId,
        String(normalizedNow(input.nowMs ?? this.now())),
        String(this.closedRetentionMs)
      ]
    })
    return numericRedisResult(raw)
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
      keys: [this.keys.states, this.keys.due, this.keys.closed, this.keys.escalation],
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
    closed: `${prefix}:closed`,
    escalation: `${prefix}:escalation`
  }
}

// Every transition, including lease-expiry normalization and index maintenance, runs in one Lua call.
export const redisAccountCircuitTransitionScript = String.raw`
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
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
  state['shadowedByIncidentId'] = nil
  state['childIncidentIds'] = nil
  state['childScopeKeys'] = nil
  state['requiredRecoveryScopeKeys'] = nil
  state['recoveryEvidenceScopeKeys'] = nil
end

local function close(entry)
  local state = entry['state']
  local parent_incident_id = state['incidentId']
  for _, child_scope_key in ipairs(state['childScopeKeys'] or {}) do
    local child_raw = redis.call('HGET', states_key, child_scope_key)
    if child_raw then
      local child_entry = cjson.decode(child_raw)
      if child_entry['state']['shadowedByIncidentId'] == parent_incident_id then
        child_entry['state']['shadowedByIncidentId'] = nil
        child_entry['state']['updatedAtMs'] = now_ms
        redis.call('HSET', states_key, child_scope_key, cjson.encode(child_entry))
      end
    end
  end
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
  state['recoveryEvidenceScopeKeys'] = {}
  state['openedAtMs'] = now_ms
  state['retryAtMs'] = now_ms + backoffs[index]
  state['failureReason'] = input['reason'] or state['failureReason']
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['updatedAtMs'] = now_ms
  entry['closedExpiresAtMs'] = nil
  return apply(entry)
end

local function enter_recovering(entry)
  local state = entry['state']
  state['phase'] = 'RECOVERING'
  state['transitionId'] = input['transitionId']
  state['recoverySuccessCount'] = 0
  state['recoveryEvidenceScopeKeys'] = {}
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['retryAtMs'] = now_ms + 3000
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
  state['incidentId'] = input['transitionId']
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
  redis.call('HDEL', escalation_key, input['scope']['accountRuntimeKey'])
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
  if input['outcome'] == 'framing_complete' then return enter_recovering(entry) end
  if input['outcome'] == 'transport_failure' then return open(entry) end
  state['transitionId'] = input['transitionId']
  state['lease'] = nil
  state['updatedAtMs'] = now_ms
  return apply(entry)
end

if operation == 'acquire_canary' then
  if state['phase'] ~= 'OPEN' and state['phase'] ~= 'RECOVERING' then return response('state_mismatch', state) end
  if state['lease'] then return response('state_mismatch', state) end
  if (state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING') and (not state['retryAtMs'] or tonumber(state['retryAtMs']) > now_ms) then
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
  if state['halfOpenOrigin'] == 'OPEN' then return enter_recovering(entry) end
  local success_count = tonumber(state['recoverySuccessCount'] or 0) + 1
  local recovery_evidence = state['recoveryEvidenceScopeKeys'] or {}
  local required_evidence = state['requiredRecoveryScopeKeys'] or {}
  if state['scope']['kind'] == 'account' and #required_evidence > 0 then
    local evidence_scope_key = input['evidenceScopeKey']
    local required = false
    for _, key in ipairs(required_evidence) do
      if key == evidence_scope_key then required = true end
    end
    if not required then return response('state_mismatch', state) end
    local seen = false
    for _, key in ipairs(recovery_evidence) do
      if key == evidence_scope_key then seen = true end
    end
    if not seen then table.insert(recovery_evidence, evidence_scope_key) end
  end
  local covered = true
  for _, required_key in ipairs(required_evidence) do
    local seen = false
    for _, evidence_key in ipairs(recovery_evidence) do
      if evidence_key == required_key then seen = true end
    end
    if not seen then covered = false end
  end
  if success_count >= 3 and covered then return close(entry) end
  state['phase'] = 'RECOVERING'
  state['transitionId'] = input['transitionId']
  state['recoverySuccessCount'] = success_count
  state['recoveryEvidenceScopeKeys'] = recovery_evidence
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['retryAtMs'] = now_ms
  state['updatedAtMs'] = now_ms
  return apply(entry)
end

return response('state_mismatch', state)
`

export const redisAccountCircuitEscalationScript = String.raw`
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local evidence_key = KEYS[4]
local input = cjson.decode(ARGV[1])
local capacity = tonumber(ARGV[2])
local closed_retention_ms = tonumber(ARGV[3])
local now_ms = tonumber(input['nowMs'])
local protocol_scope_key = input['scopeKey']
local account_scope_key = input['accountScopeKey']
local runtime_key = input['scope']['accountRuntimeKey']

local function response(status, state, scope_count, failure_count)
  return cjson.encode({
    status = status,
    accountState = state,
    protocolScopeCount = scope_count,
    confirmedFailureCount = failure_count
  })
end

local function remove_scope(key)
  redis.call('HDEL', states_key, key)
  redis.call('ZREM', due_key, key)
  redis.call('ZREM', closed_key, key)
end

local function reserve_capacity()
  local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, capacity)
  for _, key in ipairs(expired) do remove_scope(key) end
  if tonumber(redis.call('HLEN', states_key)) < capacity then return true end
  local evict = redis.call('ZRANGE', closed_key, 0, 0)
  if #evict == 0 then return false end
  remove_scope(evict[1])
  return true
end

local child_raw = redis.call('HGET', states_key, protocol_scope_key)
if not child_raw then return response('not_found', input['closedAccountState'], 0, 0) end
local child_entry = cjson.decode(child_raw)
local child_state = child_entry['state']
if tonumber(child_state['generation']) ~= tonumber(input['generation']) then
  return response('stale_generation', input['closedAccountState'], 0, 0)
end
if child_state['dispatchRevision'] ~= input['dispatchRevision'] then
  return response('stale_dispatch_revision', input['closedAccountState'], 0, 0)
end
if child_state['phase'] ~= 'OPEN' then
  return response('state_mismatch', input['closedAccountState'], 0, 0)
end

local evidence_raw = redis.call('HGET', evidence_key, runtime_key)
local evidence = evidence_raw and cjson.decode(evidence_raw) or {
  dispatchRevision = input['dispatchRevision'],
  scopes = {}
}
if evidence['dispatchRevision'] ~= input['dispatchRevision'] then
  evidence = { dispatchRevision = input['dispatchRevision'], scopes = {} }
end
local cutoff = now_ms - tonumber(input['windowMs'])
local scopes = {}
local duplicate = false
for _, item in ipairs(evidence['scopes'] or {}) do
  if tonumber(item['observedAtMs']) >= cutoff then
    if item['evidenceId'] == input['evidenceId'] then duplicate = true end
    table.insert(scopes, item)
  end
end
if duplicate then
  local total = 0
  for _, item in ipairs(scopes) do total = total + tonumber(item['confirmedFailureCount'] or 0) end
  local account_raw = redis.call('HGET', states_key, account_scope_key)
  local account_state = account_raw and cjson.decode(account_raw)['state'] or input['closedAccountState']
  return response('idempotent', account_state, #scopes, total)
end

local retained = {}
for _, item in ipairs(scopes) do
  if item['scopeKey'] ~= protocol_scope_key then table.insert(retained, item) end
end
scopes = retained

local child_incident_id = child_state['incidentId'] or (protocol_scope_key .. '@' .. tostring(child_state['generation']))
table.insert(scopes, {
  scopeKey = protocol_scope_key,
  incidentId = child_incident_id,
  evidenceId = input['evidenceId'],
  confirmedFailureCount = tonumber(input['confirmedFailureCount']),
  observedAtMs = now_ms
})
while #scopes > tonumber(input['maxProtocolScopes']) do
  local oldest_index = 1
  for index = 2, #scopes do
    if tonumber(scopes[index]['observedAtMs']) < tonumber(scopes[oldest_index]['observedAtMs']) then oldest_index = index end
  end
  table.remove(scopes, oldest_index)
end
evidence['scopes'] = scopes
redis.call('HSET', evidence_key, runtime_key, cjson.encode(evidence))

local failure_total = 0
for _, item in ipairs(scopes) do failure_total = failure_total + tonumber(item['confirmedFailureCount'] or 0) end
local account_raw = redis.call('HGET', states_key, account_scope_key)
local account_entry = account_raw and cjson.decode(account_raw) or nil
if account_entry and account_entry['state']['phase'] == 'CLOSED'
  and tonumber(account_entry['closedExpiresAtMs'] or 0) <= now_ms then
  remove_scope(account_scope_key)
  account_entry = nil
end
local account_state = account_entry and account_entry['state'] or input['closedAccountState']
if #scopes < 2 or failure_total < 3 then
  return response('recorded', account_state, #scopes, failure_total)
end

local child_scope_keys = {}
local child_incident_ids = {}
for _, item in ipairs(scopes) do
  table.insert(child_scope_keys, item['scopeKey'])
  table.insert(child_incident_ids, item['incidentId'])
end

local function contains(values, target)
  for _, value in ipairs(values or {}) do if value == target then return true end end
  return false
end

local function merge_unique(current, additions)
  local result = current or {}
  for _, value in ipairs(additions) do if not contains(result, value) then table.insert(result, value) end end
  return result
end

local function shadow_children(parent_incident_id, scope_keys)
  for _, key in ipairs(scope_keys) do
    local raw = redis.call('HGET', states_key, key)
    if raw then
      local entry = cjson.decode(raw)
      if entry['state']['phase'] ~= 'CLOSED' and entry['state']['dispatchRevision'] == input['dispatchRevision'] then
        entry['state']['shadowedByIncidentId'] = parent_incident_id
        entry['state']['updatedAtMs'] = now_ms
        redis.call('HSET', states_key, key, cjson.encode(entry))
      end
    end
  end
end

if account_entry and account_state['phase'] ~= 'CLOSED' then
  if account_state['dispatchRevision'] ~= input['dispatchRevision'] then
    return response('stale_dispatch_revision', account_state, #scopes, failure_total)
  end
  local incident_id = account_state['incidentId'] or account_state['transitionId']
  account_state['incidentId'] = incident_id
  account_state['childScopeKeys'] = merge_unique(account_state['childScopeKeys'], child_scope_keys)
  account_state['childIncidentIds'] = merge_unique(account_state['childIncidentIds'], child_incident_ids)
  account_state['requiredRecoveryScopeKeys'] = merge_unique(account_state['requiredRecoveryScopeKeys'], child_scope_keys)
  account_state['updatedAtMs'] = now_ms
  account_entry['state'] = account_state
  redis.call('HSET', states_key, account_scope_key, cjson.encode(account_entry))
  shadow_children(incident_id, child_scope_keys)
  return response('already_active', account_state, #scopes, failure_total)
end

if not account_entry and not reserve_capacity() then
  return response('capacity_exhausted', input['closedAccountState'], #scopes, failure_total)
end
local generation = account_entry and tonumber(account_state['generation'] or 0) + 1 or 1
local incident_id = input['accountTransitionId']
account_state = input['closedAccountState']
account_state['phase'] = 'OPEN'
account_state['generation'] = generation
account_state['dispatchRevision'] = input['dispatchRevision']
account_state['transitionId'] = incident_id
account_state['incidentId'] = incident_id
account_state['backoffAttempt'] = 1
account_state['recoverySuccessCount'] = 0
account_state['openedAtMs'] = now_ms
account_state['retryAtMs'] = now_ms + 3000
account_state['failureReason'] = input['reason']
account_state['childScopeKeys'] = child_scope_keys
account_state['childIncidentIds'] = child_incident_ids
account_state['requiredRecoveryScopeKeys'] = child_scope_keys
account_state['recoveryEvidenceScopeKeys'] = {}
account_state['updatedAtMs'] = now_ms
account_entry = { state = account_state, replayOrder = { incident_id } }
redis.call('HSET', states_key, account_scope_key, cjson.encode(account_entry))
redis.call('ZADD', due_key, account_state['retryAtMs'], account_scope_key)
redis.call('ZREM', closed_key, account_scope_key)
shadow_children(incident_id, child_scope_keys)
return response('escalated', account_state, #scopes, failure_total)
`

export const redisAccountCircuitClearEscalationScript = String.raw`
local evidence_key = KEYS[1]
local runtime_key = ARGV[1]
local dispatch_revision = ARGV[2]
local raw = redis.call('HGET', evidence_key, runtime_key)
if not raw then return 0 end
local evidence = cjson.decode(raw)
if evidence['dispatchRevision'] ~= dispatch_revision then return 0 end
redis.call('HDEL', evidence_key, runtime_key)
return 1
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

export const redisAccountCircuitRestoreScript = String.raw`
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local state = cjson.decode(ARGV[1])
local now_ms = tonumber(ARGV[2])
local closed_retention_ms = tonumber(ARGV[3])
local scope_key = state['scopeKey']
local existing_raw = redis.call('HGET', states_key, scope_key)
if existing_raw then
  local existing = cjson.decode(existing_raw)['state']
  local existing_generation = tonumber(existing['generation'] or 0)
  local incoming_generation = tonumber(state['generation'] or 0)
  if existing_generation > incoming_generation
    or (existing_generation == incoming_generation and tonumber(existing['updatedAtMs'] or 0) >= tonumber(state['updatedAtMs'] or 0)) then
    return cjson.encode({ status = 'idempotent', state = existing })
  end
end
local entry = { state = state, replayIds = { state['transitionId'] }, replayOrder = { state['transitionId'] } }
redis.call('HSET', states_key, scope_key, cjson.encode(entry))
redis.call('ZREM', due_key, scope_key)
redis.call('ZREM', closed_key, scope_key)
if state['phase'] == 'CLOSED' then
  local expires_at = now_ms + closed_retention_ms
  entry['closedExpiresAtMs'] = expires_at
  redis.call('HSET', states_key, scope_key, cjson.encode(entry))
  redis.call('ZADD', closed_key, expires_at, scope_key)
elseif state['lease'] then
  redis.call('ZADD', due_key, tonumber(state['lease']['leaseUntilMs']), scope_key)
elseif state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING' then
  redis.call('ZADD', due_key, tonumber(state['retryAtMs'] or state['updatedAtMs']), scope_key)
else
  redis.call('ZADD', due_key, tonumber(state['updatedAtMs']), scope_key)
end
return cjson.encode({ status = 'applied', state = state })
`

export const redisAccountCircuitAccountRevisionScript = String.raw`
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
local runtime_key = ARGV[1]
local dispatch_revision = ARGV[2]
local transition_id = ARGV[3]
local now_ms = tonumber(ARGV[4])
local retention_ms = tonumber(ARGV[5])
local values = redis.call('HGETALL', states_key)
local family_prefix = runtime_key .. ':authorized:'
local function matches_runtime_key(candidate)
  return candidate == runtime_key or (
    not string.find(runtime_key, ':authorized:', 1, true)
    and string.sub(candidate, 1, string.len(family_prefix)) == family_prefix
  )
end
local function is_older_revision(current_revision)
  local incoming_number = tonumber(dispatch_revision)
  local current_number = tonumber(current_revision)
  return incoming_number and current_number and current_number > incoming_number
end
local changed = 0
for index = 1, #values, 2 do
  local scope_key = values[index]
  local entry = cjson.decode(values[index + 1])
  local state = entry['state']
  local state_runtime_key = state['scope']['accountRuntimeKey']
  if matches_runtime_key(state_runtime_key)
    and state['dispatchRevision'] ~= dispatch_revision
    and not is_older_revision(state['dispatchRevision']) then
    state['phase'] = 'CLOSED'
    state['generation'] = tonumber(state['generation'] or 0) + 1
    state['dispatchRevision'] = dispatch_revision
    state['transitionId'] = transition_id
    state['backoffAttempt'] = 0
    state['recoverySuccessCount'] = 0
    state['openedAtMs'] = nil
    state['retryAtMs'] = nil
    state['failureReason'] = nil
    state['lease'] = nil
    state['halfOpenOrigin'] = nil
    state['incidentId'] = nil
    state['shadowedByIncidentId'] = nil
    state['childIncidentIds'] = nil
    state['childScopeKeys'] = nil
    state['requiredRecoveryScopeKeys'] = nil
    state['recoveryEvidenceScopeKeys'] = nil
    state['updatedAtMs'] = now_ms
    entry['closedExpiresAtMs'] = now_ms + retention_ms
    entry['replayIds'] = { transition_id }
    entry['replayOrder'] = { transition_id }
    redis.call('HSET', states_key, scope_key, cjson.encode(entry))
    redis.call('ZREM', due_key, scope_key)
    redis.call('ZADD', closed_key, now_ms + retention_ms, scope_key)
    redis.call('HDEL', escalation_key, state_runtime_key)
    changed = changed + 1
  end
end
local evidence_values = redis.call('HGETALL', escalation_key)
for index = 1, #evidence_values, 2 do
  local evidence_runtime_key = evidence_values[index]
  local evidence = cjson.decode(evidence_values[index + 1])
  if matches_runtime_key(evidence_runtime_key)
    and evidence['dispatchRevision'] ~= dispatch_revision
    and not is_older_revision(evidence['dispatchRevision']) then
    redis.call('HDEL', escalation_key, evidence_runtime_key)
  end
end
return changed
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
