package circuitruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	DefaultAccountCircuitRuntimeReplayLimit = GatewayAccountCircuitRuntimeMaxReplayIDs
	DefaultAccountCircuitRuntimeMaxLease    = 15 * time.Minute
)

// accountCircuitRuntimeMutationLua is the Go-only runtime owner. It deliberately
// refuses an index in building mode: a legacy Node writer cannot maintain the
// reverse index atomically, so accepting writes before the backfill publication
// would create an unsafe split-brain state.
const accountCircuitRuntimeMutationLua = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
local revisions_key = KEYS[5]
local scope_runtime_key = KEYS[6]
local runtime_scopes_key = KEYS[7]
local account_runtimes_key = KEYS[8]
local runtime_accounts_key = KEYS[9]
local index_meta_key = KEYS[10]
local ledger_revisions_key = KEYS[11]

local input = cjson.decode(ARGV[1])
local capacity = tonumber(ARGV[2])
local retention_ms = tonumber(ARGV[3])
local replay_limit = tonumber(ARGV[4])
local max_scope_members = tonumber(ARGV[5])
local now_ms = tonumber(input['nowMs'])
local account_id = input['accountId']
local scope = input['scope']
local scope_key = input['scopeKey']
local runtime_key = scope and scope['accountRuntimeKey'] or nil

local function require_type(key, expected)
  local actual = redis.call('TYPE', key)['ok']
  return actual == 'none' or actual == expected
end

if not require_type(states_key, 'hash') or not require_type(due_key, 'zset')
  or not require_type(closed_key, 'zset') or not require_type(escalation_key, 'hash')
  or not require_type(revisions_key, 'hash') or not require_type(scope_runtime_key, 'hash')
  or not require_type(runtime_scopes_key, 'hash') or not require_type(account_runtimes_key, 'hash')
  or not require_type(runtime_accounts_key, 'hash') or not require_type(index_meta_key, 'hash')
  or not require_type(ledger_revisions_key, 'hash') then
  return redis.error_reply('invalid account circuit Redis key type')
end
if redis.call('HGET', index_meta_key, 'version') ~= '1'
  or redis.call('HGET', index_meta_key, 'status') ~= 'ready'
  or redis.call('HGET', index_meta_key, 'ownerMode') ~= 'go-runtime-state-v1' then
  return redis.error_reply('account circuit runtime index is not ready')
end
if not now_ms or now_ms < 0 or not capacity or capacity < 1 or not replay_limit or replay_limit < 1
  or not max_scope_members or max_scope_members < 1 then
  return redis.error_reply('invalid account circuit runtime mutation input')
end

local function response(status, state)
  return cjson.encode({ status = status, state = state })
end

local function closed_state(dispatch_revision, generation, transition_id)
  return {
    scopeKey = scope_key,
    scope = scope,
    phase = 'CLOSED',
    generation = generation or 0,
    dispatchRevision = tostring(dispatch_revision or 0),
    transitionId = transition_id or '',
    backoffAttempt = 0,
    recoverySuccessCount = 0,
    updatedAtMs = now_ms
  }
end

local function numeric_revision(raw)
  local value = tonumber(raw)
  if not value or value < 0 or value ~= math.floor(value) then return nil end
  return value
end

local function incoming_revision()
  local value = numeric_revision(input['dispatchRevision'])
  if not value or value < 1 then return nil end
  return value
end

local function decode_array(raw)
  if not raw then return {} end
  local values = cjson.decode(raw)
  if type(values) ~= 'table' or #values > max_scope_members then return nil end
  local count = 0
  for key, value in pairs(values) do
    if type(key) ~= 'number' or key < 1 or key ~= math.floor(key) or type(value) ~= 'string' then return nil end
    count = count + 1
  end
  if count ~= #values then return nil end
  return values
end

local function canonical(values)
  local unique = {}
  local result = {}
  for _, value in ipairs(values or {}) do
    if type(value) ~= 'string' or value == '' then return nil end
    if not unique[value] then unique[value] = true; table.insert(result, value) end
  end
  table.sort(result)
  if #result > max_scope_members then return nil end
  return result
end

local function add_value(values, target)
  table.insert(values, target)
  return canonical(values)
end

local function remove_value(values, target)
  local result = {}
  for _, value in ipairs(values or {}) do if value ~= target then table.insert(result, value) end end
  return canonical(result)
end

local function contains(values, target)
  for _, value in ipairs(values or {}) do if value == target then return true end end
  return false
end

local function persist_array(hash_key, field, values)
  if not values or #values == 0 then redis.call('HDEL', hash_key, field)
  else redis.call('HSET', hash_key, field, cjson.encode(values)) end
end

local function validate_runtime_index(target_scope, target_runtime, target_account, require_state)
  local indexed_runtime = redis.call('HGET', scope_runtime_key, target_scope)
  if require_state and indexed_runtime ~= target_runtime then return false end
  if indexed_runtime and indexed_runtime ~= target_runtime then return false end
  local indexed_account = redis.call('HGET', runtime_accounts_key, target_runtime)
  if indexed_account ~= target_account then return false end
  local scopes = decode_array(redis.call('HGET', runtime_scopes_key, target_runtime))
  local runtimes = decode_array(redis.call('HGET', account_runtimes_key, target_account))
  if not scopes or not runtimes then return false end
  if require_state and (not contains(scopes, target_scope) or not contains(runtimes, target_runtime)) then return false end
  return true
end

local function validate_new_runtime_index(target_runtime, target_account)
  local indexed_account = redis.call('HGET', runtime_accounts_key, target_runtime)
  if indexed_account and indexed_account ~= target_account then return false end
  local scopes = decode_array(redis.call('HGET', runtime_scopes_key, target_runtime))
  local runtimes = decode_array(redis.call('HGET', account_runtimes_key, target_account))
  return scopes ~= nil and runtimes ~= nil
end

local function register_scope(target_scope, target_runtime, target_account)
  if not validate_new_runtime_index(target_runtime, target_account) then return false end
  local indexed_runtime = redis.call('HGET', scope_runtime_key, target_scope)
  if indexed_runtime and indexed_runtime ~= target_runtime then return false end
  local scopes = decode_array(redis.call('HGET', runtime_scopes_key, target_runtime))
  local runtimes = decode_array(redis.call('HGET', account_runtimes_key, target_account))
  local next_scopes = add_value(scopes, target_scope)
  local next_runtimes = add_value(runtimes, target_runtime)
  if not next_scopes or not next_runtimes then return false end
  redis.call('HSET', scope_runtime_key, target_scope, target_runtime)
  redis.call('HSET', runtime_accounts_key, target_runtime, target_account)
  persist_array(runtime_scopes_key, target_runtime, next_scopes)
  persist_array(account_runtimes_key, target_account, next_runtimes)
  return true
end

local function remove_runtime_if_empty(target_runtime)
  local scopes = decode_array(redis.call('HGET', runtime_scopes_key, target_runtime))
  if not scopes then return false end
  if #scopes > 0 or redis.call('HGET', escalation_key, target_runtime) then return true end
  local target_account = redis.call('HGET', runtime_accounts_key, target_runtime)
  if not target_account then return false end
  local runtimes = decode_array(redis.call('HGET', account_runtimes_key, target_account))
  if not runtimes then return false end
  local remaining = remove_value(runtimes, target_runtime)
  if not remaining then return false end
  redis.call('HDEL', runtime_scopes_key, target_runtime)
  redis.call('HDEL', runtime_accounts_key, target_runtime)
  persist_array(account_runtimes_key, target_account, remaining)
  return true
end

local function remove_scope(target_scope)
  local target_runtime = redis.call('HGET', scope_runtime_key, target_scope)
  if not target_runtime then return false end
  local target_account = redis.call('HGET', runtime_accounts_key, target_runtime)
  if not target_account then return false end
  local scopes = decode_array(redis.call('HGET', runtime_scopes_key, target_runtime))
  if not scopes or not contains(scopes, target_scope) then return false end
  local remaining = remove_value(scopes, target_scope)
  if not remaining then return false end
  redis.call('HDEL', states_key, target_scope)
  redis.call('ZREM', due_key, target_scope)
  redis.call('ZREM', closed_key, target_scope)
  redis.call('HDEL', scope_runtime_key, target_scope)
  persist_array(runtime_scopes_key, target_runtime, remaining)
  return remove_runtime_if_empty(target_runtime)
end

local function validate_entry(entry, expected_scope, expected_runtime)
  if type(entry) ~= 'table' or type(entry['state']) ~= 'table' then return false end
  local state = entry['state']
  if state['scopeKey'] ~= expected_scope or type(state['scope']) ~= 'table'
    or state['scope']['accountRuntimeKey'] ~= expected_runtime then return false end
  local revision = numeric_revision(state['dispatchRevision'])
  local generation = tonumber(state['generation'])
  if not revision or not generation or generation < 0 or generation ~= math.floor(generation) then return false end
  if state['ledgerRevision'] ~= nil and not numeric_revision(state['ledgerRevision']) then return false end
  return true
end

local function due_at(state)
  if state['phase'] == 'CLOSED' then return nil end
  if state['lease'] then return tonumber(state['lease']['leaseUntilMs']) end
  if state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING' then return tonumber(state['retryAtMs']) end
  -- SUSPECT requires a request-bound confirmation; it never occupies canary due.
  return nil
end

local function persist_entry(target_scope, entry)
  local state = entry['state']
	if state['phase'] == 'CLOSED' and not tonumber(entry['closedExpiresAtMs']) then return false end
  redis.call('HSET', states_key, target_scope, cjson.encode(entry))
  local due = due_at(state)
  if due then redis.call('ZADD', due_key, due, target_scope) else redis.call('ZREM', due_key, target_scope) end
  if state['phase'] == 'CLOSED' then
    redis.call('ZADD', closed_key, tonumber(entry['closedExpiresAtMs']), target_scope)
  else
    redis.call('ZREM', closed_key, target_scope)
  end
  return true
end

local function normalize_entry(target_scope, entry)
  if not entry then return nil, false end
  local state = entry['state']
  if state['phase'] == 'CLOSED' and tonumber(entry['closedExpiresAtMs'] or 0) <= now_ms then
    if not remove_scope(target_scope) then return nil, nil end
    return nil, true
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
    if not persist_entry(target_scope, entry) then return nil, nil end
  end
  return entry, false
end

local function load_entry(target_scope, target_runtime, target_account)
  local raw = redis.call('HGET', states_key, target_scope)
  if not raw then
    if redis.call('HGET', scope_runtime_key, target_scope) then return nil, nil end
    return nil, false
  end
  local entry = cjson.decode(raw)
  if not validate_entry(entry, target_scope, target_runtime) or not validate_runtime_index(target_scope, target_runtime, target_account, true) then
    return nil, nil
  end
  local normalized, removed = normalize_entry(target_scope, entry)
  if normalized == nil and removed == nil then return nil, nil end
  return normalized, removed
end

local function cleanup_closed()
  local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, capacity)
  for _, target_scope in ipairs(expired) do
    local target_runtime = redis.call('HGET', scope_runtime_key, target_scope)
    if not target_runtime or not validate_runtime_index(target_scope, target_runtime, redis.call('HGET', runtime_accounts_key, target_runtime), true) then
      return false
    end
  end
  for _, target_scope in ipairs(expired) do if not remove_scope(target_scope) then return false end end
  return true
end

local function reserve_capacity()
  if not cleanup_closed() then return nil end
  if tonumber(redis.call('HLEN', states_key)) < capacity then return true end
  local evict = redis.call('ZRANGE', closed_key, 0, 0)
  if #evict == 0 then return false end
  if not remove_scope(evict[1]) then return nil end
  return true
end

local function replayed(entry, transition_id)
  if not entry or not transition_id then return false end
  for _, existing in ipairs(entry['replayOrder'] or {}) do if existing == transition_id then return true end end
  return false
end

local function remember(entry, transition_id)
  local order = entry['replayOrder'] or {}
  table.insert(order, transition_id)
  while #order > replay_limit do table.remove(order, 1) end
  entry['replayOrder'] = order
end

local function apply(target_scope, entry, transition_id)
  remember(entry, transition_id)
  if not persist_entry(target_scope, entry) then return nil end
  return response('applied', entry['state'])
end

local function dispatch_tombstone(incoming)
  local raw = redis.call('HGET', revisions_key, account_id)
  if not raw then
    if incoming ~= 1 then return false end
    redis.call('HSETNX', revisions_key, account_id, '1')
    return nil
  end
  local current = numeric_revision(raw)
  if current == nil then return false end
  if current ~= incoming then return current end
  return nil
end

local function identity_error(entry, incoming, generation)
  local fenced = dispatch_tombstone(incoming)
  if fenced == false then return 'invalid' end
  if fenced then return response('stale_dispatch_revision', entry and entry['state'] or closed_state(incoming, 0, '')) end
  if not entry then return response('not_found', closed_state(incoming, 0, '')) end
  local state = entry['state']
  local current_revision = numeric_revision(state['dispatchRevision'])
  if current_revision > incoming then return response('stale_dispatch_revision', state) end
  if tonumber(state['generation']) ~= generation then return response('stale_generation', state) end
  if current_revision ~= incoming then return response('stale_dispatch_revision', state) end
  return nil
end

if type(scope) ~= 'table' or type(scope_key) ~= 'string' or type(runtime_key) ~= 'string' or type(account_id) ~= 'string' then
  return redis.error_reply('invalid account circuit runtime identity')
end
if not cleanup_closed() then return redis.error_reply('invalid account circuit runtime index') end

local operation = input['operation']
if operation == 'get' then
  local entry, removed = load_entry(scope_key, runtime_key, account_id)
  if removed == nil then return redis.error_reply('invalid account circuit runtime state') end
  return response('applied', entry and entry['state'] or closed_state(0, 0, ''))
end

local incoming = incoming_revision()
if not incoming then return redis.error_reply('invalid account circuit dispatch revision') end
local entry, removed = load_entry(scope_key, runtime_key, account_id)
if removed == nil then return redis.error_reply('invalid account circuit runtime state') end

if operation == 'suspect' then
  local fenced = dispatch_tombstone(incoming)
  if fenced == false then return redis.error_reply('invalid revision tombstone') end
  if fenced then return response('stale_dispatch_revision', entry and entry['state'] or closed_state(incoming, 0, '')) end
  if entry and numeric_revision(entry['state']['dispatchRevision']) > incoming then return response('stale_dispatch_revision', entry['state']) end
  if replayed(entry, input['transitionId']) then return response('idempotent', entry['state']) end
  if entry and entry['state']['phase'] ~= 'CLOSED' then return response('state_mismatch', entry['state']) end
  if not entry then
    local reserved = reserve_capacity()
    if reserved == nil then return redis.error_reply('invalid account circuit runtime index') end
    if not reserved then return response('capacity_exhausted', closed_state(incoming, 0, '')) end
  end
  local generation = entry and tonumber(entry['state']['generation']) + 1 or 1
  local state = closed_state(incoming, generation, input['transitionId'])
  state['phase'] = 'SUSPECT'
  state['failureReason'] = input['reason']
  state['incidentId'] = input['transitionId']
  entry = entry or { replayOrder = {} }
  entry['state'] = state
  entry['closedExpiresAtMs'] = nil
  if not register_scope(scope_key, runtime_key, account_id) then return redis.error_reply('invalid account circuit runtime index') end
  local applied = apply(scope_key, entry, input['transitionId'])
  if not applied then return redis.error_reply('invalid account circuit runtime state') end
  return applied
end

if operation == 'replace_revision' then
  local fenced = dispatch_tombstone(incoming)
  if fenced == false then return redis.error_reply('invalid revision tombstone') end
  if fenced then return response('stale_dispatch_revision', entry and entry['state'] or closed_state(incoming, 0, '')) end
  if entry and numeric_revision(entry['state']['dispatchRevision']) > incoming then return response('stale_dispatch_revision', entry['state']) end
  if replayed(entry, input['transitionId']) then return response('idempotent', entry['state']) end
  if entry and numeric_revision(entry['state']['dispatchRevision']) == incoming then return response('idempotent', entry['state']) end
  if not entry then
    local reserved = reserve_capacity()
    if reserved == nil then return redis.error_reply('invalid account circuit runtime index') end
    if not reserved then return response('capacity_exhausted', closed_state(incoming, 0, '')) end
  end
  local generation = entry and tonumber(entry['state']['generation']) + 1 or 1
  entry = entry or { replayOrder = {} }
  entry['state'] = closed_state(incoming, generation, input['transitionId'])
  entry['closedExpiresAtMs'] = now_ms + retention_ms
  if not register_scope(scope_key, runtime_key, account_id) then return redis.error_reply('invalid account circuit runtime index') end
  redis.call('HDEL', escalation_key, runtime_key)
  local applied = apply(scope_key, entry, input['transitionId'])
  if not applied then return redis.error_reply('invalid account circuit runtime state') end
  return applied
end

if operation == 'restore' then
  local restored = input['state']
  if type(restored) ~= 'table' or restored['scopeKey'] ~= scope_key or type(restored['scope']) ~= 'table'
    or restored['scope']['accountRuntimeKey'] ~= runtime_key then
    return redis.error_reply('invalid account circuit restore state')
  end
  local restored_generation = tonumber(restored['generation'])
  local restored_updated_at = tonumber(restored['updatedAtMs'])
  local restored_ledger = numeric_revision(restored['ledgerRevision'] or '0')
  if not restored_generation or restored_generation < 0 or restored_generation ~= math.floor(restored_generation)
    or not restored_updated_at or restored_updated_at < 0 or not restored_ledger then
    return redis.error_reply('invalid account circuit restore state')
  end
  local fenced = dispatch_tombstone(incoming)
  if fenced == false then return redis.error_reply('invalid revision tombstone') end
  if fenced then return response('stale_dispatch_revision', entry and entry['state'] or closed_state(incoming, 0, '')) end
  local ledger_raw = redis.call('HGET', ledger_revisions_key, scope_key)
  if ledger_raw then
    local ledger = cjson.decode(ledger_raw)
    local ledger_revision = ledger and tonumber(ledger['ledgerRevision']) or nil
    local ledger_dispatch = ledger and tonumber(ledger['dispatchRevision']) or nil
    if not ledger_revision or not ledger_dispatch then return redis.error_reply('invalid account circuit ledger tombstone') end
    if ledger_dispatch > incoming then return response('stale_dispatch_revision', entry and entry['state'] or closed_state(incoming, 0, '')) end
    if ledger_dispatch == incoming and ledger_revision > restored_ledger then return response('stale_generation', entry and entry['state'] or closed_state(incoming, 0, '')) end
  end
  if entry then
    local existing = entry['state']
    local existing_revision = numeric_revision(existing['dispatchRevision'])
    local existing_generation = tonumber(existing['generation'])
    local existing_ledger = numeric_revision(existing['ledgerRevision'] or '0')
    local existing_updated_at = tonumber(existing['updatedAtMs'])
    if not existing_revision or not existing_generation or not existing_ledger or not existing_updated_at then return redis.error_reply('invalid account circuit runtime state') end
    if existing_revision > incoming then return response('stale_dispatch_revision', existing) end
    if existing_revision == incoming and existing_generation > restored_generation then return response('stale_generation', existing) end
    if existing_revision == incoming and existing_ledger > restored_ledger then return response('stale_generation', existing) end
    if existing_revision == incoming and existing_generation == restored_generation and existing_ledger == restored_ledger and existing_updated_at >= restored_updated_at then
      if not persist_entry(scope_key, entry) then return redis.error_reply('invalid account circuit runtime state') end
      return response('idempotent', existing)
    end
  end
  if restored['phase'] == 'CLOSED' then
    local retained_until = tonumber(input['retainedUntilMs'] or 0)
    if retained_until > 0 and retained_until <= now_ms then
      if entry and not remove_scope(scope_key) then return redis.error_reply('invalid account circuit runtime index') end
      if restored_ledger > 0 then redis.call('HSET', ledger_revisions_key, scope_key, cjson.encode({ dispatchRevision = incoming, ledgerRevision = restored_ledger, transitionId = restored['transitionId'] })) end
      return response('applied', restored)
    end
	if not entry then
	  local reserved = reserve_capacity()
	  if reserved == nil then return redis.error_reply('invalid account circuit runtime index') end
	  if not reserved then return response('capacity_exhausted', restored) end
	end
    entry = entry or { replayOrder = {} }
    entry['state'] = restored
    entry['closedExpiresAtMs'] = retained_until > 0 and retained_until or (now_ms + retention_ms)
  else
	if not entry then
	  local reserved = reserve_capacity()
	  if reserved == nil then return redis.error_reply('invalid account circuit runtime index') end
	  if not reserved then return response('capacity_exhausted', restored) end
	end
    entry = entry or { replayOrder = {} }
    entry['state'] = restored
    entry['closedExpiresAtMs'] = nil
  end
  if not register_scope(scope_key, runtime_key, account_id) then return redis.error_reply('invalid account circuit runtime index') end
  if not persist_entry(scope_key, entry) then return redis.error_reply('invalid account circuit runtime state') end
  if restored_ledger > 0 then redis.call('HSET', ledger_revisions_key, scope_key, cjson.encode({ dispatchRevision = incoming, ledgerRevision = restored_ledger, transitionId = restored['transitionId'] })) end
  return response('applied', restored)
end

local expected_generation = tonumber(input['generation'])
if not expected_generation or expected_generation < 0 or expected_generation ~= math.floor(expected_generation) then
  return redis.error_reply('invalid account circuit generation')
end
local invalid = identity_error(entry, incoming, expected_generation)
if invalid == 'invalid' then return redis.error_reply('invalid revision tombstone') end
if invalid then return invalid end
if replayed(entry, input['transitionId']) then return response('idempotent', entry['state']) end
local state = entry['state']

if operation == 'acquire_confirmation' then
  if state['phase'] ~= 'SUSPECT' or state['lease'] then return response('state_mismatch', state) end
  local until_ms = tonumber(input['leaseUntilMs'])
  if not until_ms or until_ms <= now_ms then return redis.error_reply('invalid account circuit lease deadline') end
  state['transitionId'] = input['transitionId']
  state['lease'] = { kind = 'confirmation', leaseId = input['leaseId'], leaseUntilMs = until_ms }
  state['updatedAtMs'] = now_ms
  local applied = apply(scope_key, entry, input['transitionId'])
  if not applied then return redis.error_reply('invalid account circuit runtime state') end
  return applied
end

local function open(entry, transition_id, reason)
  local target = entry['state']
  local attempts = tonumber(target['backoffAttempt'] or 0) + 1
  local backoffs = { 3000, 5000, 10000, 30000, 60000 }
  local delay = backoffs[math.min(#backoffs, attempts)]
  target['phase'] = 'OPEN'
  target['transitionId'] = transition_id
  target['backoffAttempt'] = attempts
  target['recoverySuccessCount'] = 0
  target['recoveryEvidenceScopeKeys'] = {}
  target['openedAtMs'] = now_ms
  target['retryAtMs'] = now_ms + delay
  target['failureReason'] = reason or target['failureReason']
  target['lease'] = nil
  target['halfOpenOrigin'] = nil
  entry['closedExpiresAtMs'] = nil
  target['updatedAtMs'] = now_ms
  return apply(scope_key, entry, transition_id)
end

local function enter_recovering(entry, transition_id)
  local target = entry['state']
  target['phase'] = 'RECOVERING'
  target['transitionId'] = transition_id
  target['recoverySuccessCount'] = 0
  target['recoveryEvidenceScopeKeys'] = {}
  target['lease'] = nil
  target['halfOpenOrigin'] = nil
  target['retryAtMs'] = now_ms + 3000
  target['updatedAtMs'] = now_ms
  entry['closedExpiresAtMs'] = nil
  return apply(scope_key, entry, transition_id)
end

if operation == 'complete_confirmation' then
  if state['phase'] ~= 'SUSPECT' then return response('state_mismatch', state) end
  local lease = state['lease']
  if not lease or lease['kind'] ~= 'confirmation' or lease['leaseId'] ~= input['leaseId'] then return response('lease_mismatch', state) end
  if input['outcome'] == 'framing_complete' then
    local applied = enter_recovering(entry, input['transitionId'])
    if not applied then return redis.error_reply('invalid account circuit runtime state') end
    return applied
  end
  if input['outcome'] == 'transport_failure' then
    local applied = open(entry, input['transitionId'], input['reason'])
    if not applied then return redis.error_reply('invalid account circuit runtime state') end
    return applied
  end
  state['transitionId'] = input['transitionId']
  state['lease'] = nil
  state['updatedAtMs'] = now_ms
  local applied = apply(scope_key, entry, input['transitionId'])
  if not applied then return redis.error_reply('invalid account circuit runtime state') end
  return applied
end

if operation == 'acquire_canary' then
  if state['phase'] ~= 'OPEN' and state['phase'] ~= 'RECOVERING' then return response('state_mismatch', state) end
  if state['lease'] then return response('state_mismatch', state) end
  if not state['retryAtMs'] or tonumber(state['retryAtMs']) > now_ms then return response('not_due', state) end
  local until_ms = tonumber(input['leaseUntilMs'])
  if not until_ms or until_ms <= now_ms then return redis.error_reply('invalid account circuit lease deadline') end
  local origin = state['phase']
  state['phase'] = 'HALF_OPEN'
  state['transitionId'] = input['transitionId']
  state['lease'] = { kind = origin == 'OPEN' and 'half_open' or 'recovery', leaseId = input['leaseId'], leaseUntilMs = until_ms }
  state['halfOpenOrigin'] = origin
  state['updatedAtMs'] = now_ms
  local applied = apply(scope_key, entry, input['transitionId'])
  if not applied then return redis.error_reply('invalid account circuit runtime state') end
  return applied
end

local function canonical_state_array(state, field)
  local values = canonical(state[field] or {})
  if not values then return nil end
  state[field] = values
  return values
end

local function close(entry, transition_id)
  local target = entry['state']
  local incident_id = target['incidentId']
  local child_scopes = canonical_state_array(target, 'childScopeKeys')
  if not child_scopes then return nil end
  local children = {}
  for _, child_scope in ipairs(child_scopes) do
    local child_raw = redis.call('HGET', states_key, child_scope)
    if child_raw then
      local child = cjson.decode(child_raw)
      if type(child) ~= 'table' or type(child['state']) ~= 'table' then return nil end
      table.insert(children, { scope = child_scope, entry = child })
    end
  end
  for _, child in ipairs(children) do
    if child['entry']['state']['shadowedByIncidentId'] == incident_id then
      child['entry']['state']['shadowedByIncidentId'] = nil
      child['entry']['state']['updatedAtMs'] = now_ms
      if not persist_entry(child['scope'], child['entry']) then return nil end
    end
  end
  local generation = target['generation']
  local revision = target['dispatchRevision']
  target = closed_state(numeric_revision(revision), generation, transition_id)
  target['incidentId'] = incident_id
  entry['state'] = target
  entry['closedExpiresAtMs'] = now_ms + retention_ms
  return apply(scope_key, entry, transition_id)
end

if operation == 'complete_canary' then
  if state['phase'] ~= 'HALF_OPEN' then return response('state_mismatch', state) end
  local lease = state['lease']
  if not lease or lease['leaseId'] ~= input['leaseId'] then return response('lease_mismatch', state) end
  if input['outcome'] == 'transport_failure' then
    local applied = open(entry, input['transitionId'], input['reason'])
    if not applied then return redis.error_reply('invalid account circuit runtime state') end
    return applied
  end
  if input['outcome'] == 'unknown' then
    state['phase'] = state['halfOpenOrigin'] or 'OPEN'
    state['transitionId'] = input['transitionId']
    state['lease'] = nil
    state['halfOpenOrigin'] = nil
    state['retryAtMs'] = now_ms
    state['updatedAtMs'] = now_ms
    local applied = apply(scope_key, entry, input['transitionId'])
    if not applied then return redis.error_reply('invalid account circuit runtime state') end
    return applied
  end
  if state['halfOpenOrigin'] == 'OPEN' then
    local applied = enter_recovering(entry, input['transitionId'])
    if not applied then return redis.error_reply('invalid account circuit runtime state') end
    return applied
  end
  local required = canonical_state_array(state, 'requiredRecoveryScopeKeys')
  local recovered = canonical_state_array(state, 'recoveryEvidenceScopeKeys')
  if not required or not recovered then return redis.error_reply('invalid account circuit recovery evidence') end
  if state['scope']['kind'] == 'account' and #required > 0 then
    if not input['evidenceScopeKey'] or not contains(required, input['evidenceScopeKey']) then return response('state_mismatch', state) end
    recovered = add_value(recovered, input['evidenceScopeKey'])
    if not recovered then return redis.error_reply('invalid account circuit recovery evidence') end
    state['recoveryEvidenceScopeKeys'] = recovered
  end
  local covered = true
  for _, required_scope in ipairs(required) do if not contains(recovered, required_scope) then covered = false end end
  local successes = tonumber(state['recoverySuccessCount'] or 0) + 1
  if successes >= 3 and covered then
    local applied = close(entry, input['transitionId'])
    if not applied then return redis.error_reply('invalid account circuit runtime state') end
    return applied
  end
  state['phase'] = 'RECOVERING'
  state['transitionId'] = input['transitionId']
  state['recoverySuccessCount'] = successes
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['retryAtMs'] = now_ms
  state['updatedAtMs'] = now_ms
  local applied = apply(scope_key, entry, input['transitionId'])
  if not applied then return redis.error_reply('invalid account circuit runtime state') end
  return applied
end

return response('state_mismatch', state)
`

const accountCircuitRuntimeEscalationLua = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
local revisions_key = KEYS[5]
local scope_runtime_key = KEYS[6]
local runtime_scopes_key = KEYS[7]
local account_runtimes_key = KEYS[8]
local runtime_accounts_key = KEYS[9]
local index_meta_key = KEYS[10]
local input = cjson.decode(ARGV[1])
local capacity = tonumber(ARGV[2])
local retention_ms = tonumber(ARGV[3])
local max_scope_members = tonumber(ARGV[4])
local now_ms = tonumber(input['nowMs'])
local account_id = input['accountId']
local scope = input['scope']
local scope_key = input['scopeKey']
local runtime_key = scope and scope['accountRuntimeKey'] or nil

local function require_type(key, expected)
  local actual = redis.call('TYPE', key)['ok']
  return actual == 'none' or actual == expected
end
if not require_type(states_key, 'hash') or not require_type(due_key, 'zset') or not require_type(closed_key, 'zset')
  or not require_type(escalation_key, 'hash') or not require_type(revisions_key, 'hash') or not require_type(scope_runtime_key, 'hash')
  or not require_type(runtime_scopes_key, 'hash') or not require_type(account_runtimes_key, 'hash') or not require_type(runtime_accounts_key, 'hash')
  or not require_type(index_meta_key, 'hash') then return redis.error_reply('invalid account circuit Redis key type') end
if redis.call('HGET', index_meta_key, 'version') ~= '1' or redis.call('HGET', index_meta_key, 'status') ~= 'ready'
  or redis.call('HGET', index_meta_key, 'ownerMode') ~= 'go-runtime-state-v1' then return redis.error_reply('account circuit runtime index is not ready') end
if not scope or scope['kind'] ~= 'protocol_model' or not scope_key or not runtime_key or not account_id or not now_ms or not capacity or capacity < 1 then
  return redis.error_reply('invalid account circuit escalation input')
end
local incoming = tonumber(input['dispatchRevision'])
if not incoming or incoming < 1 or incoming ~= math.floor(incoming) then return redis.error_reply('invalid account circuit dispatch revision') end
local function response(status, state, count, failures)
  return cjson.encode({ status = status, accountState = state, protocolScopeCount = count, confirmedFailureCount = failures })
end
local function closed_state(scope_value)
  return { scopeKey = input['accountScopeKey'], scope = scope_value, phase = 'CLOSED', generation = 0,
    dispatchRevision = tostring(incoming), transitionId = '', backoffAttempt = 0, recoverySuccessCount = 0, updatedAtMs = now_ms }
end
local function array(raw)
  if not raw then return {} end
  local values = cjson.decode(raw)
  if type(values) ~= 'table' or #values > max_scope_members then return nil end
  local count = 0
  for key, value in pairs(values) do if type(key) ~= 'number' or key < 1 or key ~= math.floor(key) or type(value) ~= 'string' then return nil end; count = count + 1 end
  if count ~= #values then return nil end
  return values
end
local function contains(values, target) for _, value in ipairs(values or {}) do if value == target then return true end end return false end
local function canonical(values)
  local set, out = {}, {}
  for _, value in ipairs(values or {}) do if type(value) ~= 'string' or value == '' then return nil end; if not set[value] then set[value] = true; table.insert(out, value) end end
  table.sort(out); if #out > max_scope_members then return nil end; return out
end
local function add(values, target) table.insert(values, target); return canonical(values) end
local function persist_array(key, field, values) if not values or #values == 0 then redis.call('HDEL', key, field) else redis.call('HSET', key, field, cjson.encode(values)) end end
local function due_at(state)
  if state['phase'] == 'CLOSED' then return nil end
  if state['lease'] then return tonumber(state['lease']['leaseUntilMs']) end
  if state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING' then return tonumber(state['retryAtMs']) end
  return nil
end
local function persist(scope_value, entry)
  redis.call('HSET', states_key, scope_value, cjson.encode(entry))
  local due = due_at(entry['state'])
  if due then redis.call('ZADD', due_key, due, scope_value) else redis.call('ZREM', due_key, scope_value) end
  if entry['state']['phase'] == 'CLOSED' then redis.call('ZADD', closed_key, tonumber(entry['closedExpiresAtMs']), scope_value) else redis.call('ZREM', closed_key, scope_value) end
end
local function validate_runtime(scope_value, runtime, account, require_scope)
  local actual_runtime = redis.call('HGET', scope_runtime_key, scope_value)
  if require_scope and actual_runtime ~= runtime then return false end
  if actual_runtime and actual_runtime ~= runtime then return false end
  if redis.call('HGET', runtime_accounts_key, runtime) ~= account then return false end
  local scopes = array(redis.call('HGET', runtime_scopes_key, runtime))
  local runtimes = array(redis.call('HGET', account_runtimes_key, account))
  if not scopes or not runtimes then return false end
  return not require_scope or (contains(scopes, scope_value) and contains(runtimes, runtime))
end
local function register(scope_value, runtime, account)
  local old_runtime = redis.call('HGET', scope_runtime_key, scope_value)
  local old_account = redis.call('HGET', runtime_accounts_key, runtime)
  if old_runtime and old_runtime ~= runtime or old_account and old_account ~= account then return false end
  local scopes = array(redis.call('HGET', runtime_scopes_key, runtime)); local runtimes = array(redis.call('HGET', account_runtimes_key, account))
  if not scopes or not runtimes then return false end
  scopes = add(scopes, scope_value); runtimes = add(runtimes, runtime); if not scopes or not runtimes then return false end
  redis.call('HSET', scope_runtime_key, scope_value, runtime); redis.call('HSET', runtime_accounts_key, runtime, account)
  persist_array(runtime_scopes_key, runtime, scopes); persist_array(account_runtimes_key, account, runtimes); return true
end
local function remove_value(values, target)
  local result = {}; for _, value in ipairs(values or {}) do if value ~= target then table.insert(result, value) end end; return canonical(result)
end
local function validate_removal(target_scope)
  local target_runtime = redis.call('HGET', scope_runtime_key, target_scope); if not target_runtime then return false end
  local target_account = redis.call('HGET', runtime_accounts_key, target_runtime); if not target_account then return false end
  local scopes = array(redis.call('HGET', runtime_scopes_key, target_runtime)); local runtimes = array(redis.call('HGET', account_runtimes_key, target_account))
  return scopes and runtimes and contains(scopes, target_scope) and contains(runtimes, target_runtime)
end
local function remove_scope(target_scope)
  if not validate_removal(target_scope) then return false end
  local target_runtime = redis.call('HGET', scope_runtime_key, target_scope); local target_account = redis.call('HGET', runtime_accounts_key, target_runtime)
  local scopes = array(redis.call('HGET', runtime_scopes_key, target_runtime)); local runtimes = array(redis.call('HGET', account_runtimes_key, target_account))
  local remaining_scopes = remove_value(scopes, target_scope); if not remaining_scopes then return false end
  redis.call('HDEL', states_key, target_scope); redis.call('ZREM', due_key, target_scope); redis.call('ZREM', closed_key, target_scope); redis.call('HDEL', scope_runtime_key, target_scope)
  persist_array(runtime_scopes_key, target_runtime, remaining_scopes)
  if #remaining_scopes == 0 and not redis.call('HGET', escalation_key, target_runtime) then
    local remaining_runtimes = remove_value(runtimes, target_runtime); if not remaining_runtimes then return false end
    redis.call('HDEL', runtime_accounts_key, target_runtime); persist_array(account_runtimes_key, target_account, remaining_runtimes)
  end
  return true
end
local function cleanup_closed()
  local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, capacity)
  for _, target_scope in ipairs(expired) do if not validate_removal(target_scope) then return false end end
  for _, target_scope in ipairs(expired) do if not remove_scope(target_scope) then return false end end
  return true
end
local function reserve_capacity()
  if not cleanup_closed() then return nil end
  if tonumber(redis.call('HLEN', states_key)) < capacity then return true end
  local evict = redis.call('ZRANGE', closed_key, 0, 0); if #evict == 0 then return false end
  if not validate_removal(evict[1]) or not remove_scope(evict[1]) then return nil end
  return true
end
if not cleanup_closed() then return redis.error_reply('invalid account circuit runtime index') end
local tombstone_raw = redis.call('HGET', revisions_key, account_id)
if not tombstone_raw and incoming == 1 then redis.call('HSETNX', revisions_key, account_id, '1'); tombstone_raw = '1' end
if not tombstone_raw or not tonumber(tombstone_raw) or tonumber(tombstone_raw) ~= incoming then
  local account_scope = { kind = 'account', accountRuntimeKey = runtime_key }
  return response('stale_dispatch_revision', closed_state(account_scope), 0, 0)
end
local child_raw = redis.call('HGET', states_key, scope_key)
local account_scope = { kind = 'account', accountRuntimeKey = runtime_key }
local fallback = closed_state(account_scope)
if not child_raw then return response('not_found', fallback, 0, 0) end
local child_entry = cjson.decode(child_raw)
if type(child_entry) ~= 'table' or type(child_entry['state']) ~= 'table' or not validate_runtime(scope_key, runtime_key, account_id, true) then return redis.error_reply('invalid account circuit runtime state') end
local child = child_entry['state']
if tonumber(child['generation']) ~= tonumber(input['generation']) then return response('stale_generation', fallback, 0, 0) end
if tonumber(child['dispatchRevision']) ~= incoming then return response('stale_dispatch_revision', fallback, 0, 0) end
if child['phase'] ~= 'OPEN' then return response('state_mismatch', fallback, 0, 0) end
local evidence_raw = redis.call('HGET', escalation_key, runtime_key)
local evidence = evidence_raw and cjson.decode(evidence_raw) or { dispatchRevision = tostring(incoming), scopes = {} }
if type(evidence) ~= 'table' or type(evidence['scopes']) ~= 'table' then return redis.error_reply('invalid account circuit escalation evidence') end
local evidence_revision = tonumber(evidence['dispatchRevision'])
if evidence_revision and evidence_revision > incoming then return response('stale_dispatch_revision', fallback, 0, 0) end
if evidence_revision ~= incoming then evidence = { dispatchRevision = tostring(incoming), scopes = {} } end
local cutoff = now_ms - tonumber(input['windowMs'])
local kept, duplicate = {}, false
for _, item in ipairs(evidence['scopes']) do
  if type(item) ~= 'table' then return redis.error_reply('invalid account circuit escalation evidence') end
  if tonumber(item['observedAtMs']) >= cutoff then
    if item['evidenceId'] == input['evidenceId'] then duplicate = true end
    if item['scopeKey'] ~= scope_key then table.insert(kept, item) end
  end
end
if duplicate then
  local total = 0; for _, item in ipairs(kept) do total = total + tonumber(item['confirmedFailureCount'] or 0) end
  local account_raw = redis.call('HGET', states_key, input['accountScopeKey'])
  local account_state = account_raw and cjson.decode(account_raw)['state'] or fallback
  return response('idempotent', account_state, #kept, total)
end
table.insert(kept, { scopeKey = scope_key, incidentId = child['incidentId'] or child['transitionId'], evidenceId = input['evidenceId'], confirmedFailureCount = tonumber(input['confirmedFailureCount']), observedAtMs = now_ms })
table.sort(kept, function(left, right) if tonumber(left['observedAtMs']) == tonumber(right['observedAtMs']) then return left['scopeKey'] < right['scopeKey'] end return tonumber(left['observedAtMs']) < tonumber(right['observedAtMs']) end)
while #kept > tonumber(input['maxProtocolScopes']) do table.remove(kept, 1) end
evidence['scopes'] = kept
local account_raw = redis.call('HGET', states_key, input['accountScopeKey'])
local account_entry = account_raw and cjson.decode(account_raw) or nil
if account_entry and (type(account_entry) ~= 'table' or type(account_entry['state']) ~= 'table') then return redis.error_reply('invalid account circuit runtime state') end
local total = 0; for _, item in ipairs(kept) do total = total + tonumber(item['confirmedFailureCount'] or 0) end
if #kept < 2 or total < 3 then redis.call('HSET', escalation_key, runtime_key, cjson.encode(evidence)); return response('recorded', account_entry and account_entry['state'] or fallback, #kept, total) end
local child_scopes, child_incidents = {}, {}
for _, item in ipairs(kept) do table.insert(child_scopes, item['scopeKey']); table.insert(child_incidents, item['incidentId']) end
child_scopes = canonical(child_scopes); child_incidents = canonical(child_incidents); if not child_scopes or not child_incidents then return redis.error_reply('invalid account circuit escalation evidence') end
local shadow_entries = {}
for _, child_scope in ipairs(child_scopes) do
  local raw = redis.call('HGET', states_key, child_scope); if not raw then return redis.error_reply('account circuit escalation child state is missing') end
  local item = cjson.decode(raw); local item_state = item and item['state'] or nil
  if not item_state or item_state['phase'] == 'CLOSED' or tonumber(item_state['dispatchRevision']) ~= incoming
    or not validate_runtime(child_scope, runtime_key, account_id, true) then return redis.error_reply('invalid account circuit escalation child state') end
  table.insert(shadow_entries, { scope = child_scope, entry = item })
end
if account_entry and account_entry['state']['phase'] ~= 'CLOSED' then
  local account_state = account_entry['state']
  if tonumber(account_state['dispatchRevision']) ~= incoming then return response('stale_dispatch_revision', account_state, #kept, total) end
  local incident_id = account_state['incidentId'] or account_state['transitionId']
  local function merge(field, additions) local values = canonical(account_state[field] or {}); if not values then return false end; for _, value in ipairs(additions) do table.insert(values, value) end; values = canonical(values); if not values then return false end; account_state[field] = values; return true end
  if not merge('childScopeKeys', child_scopes) or not merge('childIncidentIds', child_incidents) or not merge('requiredRecoveryScopeKeys', child_scopes) then return redis.error_reply('invalid account circuit parent state') end
  account_state['incidentId'] = incident_id; account_state['updatedAtMs'] = now_ms; persist(input['accountScopeKey'], account_entry)
  for _, child_item in ipairs(shadow_entries) do child_item['entry']['state']['shadowedByIncidentId'] = incident_id; child_item['entry']['state']['updatedAtMs'] = now_ms; persist(child_item['scope'], child_item['entry']) end
  redis.call('HSET', escalation_key, runtime_key, cjson.encode(evidence)); return response('already_active', account_state, #kept, total)
end
if not account_entry then
  local reserved = reserve_capacity()
  if reserved == nil then return redis.error_reply('invalid account circuit runtime index') end
  if not reserved then return response('capacity_exhausted', fallback, #kept, total) end
end
local generation = account_entry and tonumber(account_entry['state']['generation']) + 1 or 1
local parent = fallback
parent['phase'] = 'OPEN'; parent['generation'] = generation; parent['transitionId'] = input['accountTransitionId']; parent['incidentId'] = input['accountTransitionId']; parent['backoffAttempt'] = 1; parent['openedAtMs'] = now_ms; parent['retryAtMs'] = now_ms + 3000; parent['failureReason'] = input['reason']; parent['childScopeKeys'] = child_scopes; parent['childIncidentIds'] = child_incidents; parent['requiredRecoveryScopeKeys'] = child_scopes; parent['recoveryEvidenceScopeKeys'] = {}; parent['updatedAtMs'] = now_ms
account_entry = { state = parent, replayOrder = { input['accountTransitionId'] } }
if not register(input['accountScopeKey'], runtime_key, account_id) then return redis.error_reply('invalid account circuit runtime index') end
persist(input['accountScopeKey'], account_entry)
for _, child_item in ipairs(shadow_entries) do child_item['entry']['state']['shadowedByIncidentId'] = parent['incidentId']; child_item['entry']['state']['updatedAtMs'] = now_ms; persist(child_item['scope'], child_item['entry']) end
redis.call('HSET', escalation_key, runtime_key, cjson.encode(evidence)); return response('escalated', parent, #kept, total)
`

const accountCircuitRuntimeListDueLua = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local revisions_key = KEYS[4]
local scope_runtime_key = KEYS[5]
local runtime_scopes_key = KEYS[6]
local runtime_accounts_key = KEYS[7]
local account_runtimes_key = KEYS[8]
local index_meta_key = KEYS[9]
local now_ms = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local scan_limit = tonumber(ARGV[3])
local max_scope_members = tonumber(ARGV[4])
local function require_type(key, expected) local actual = redis.call('TYPE', key)['ok']; return actual == 'none' or actual == expected end
local function array(raw)
  if not raw then return {} end
  local values = cjson.decode(raw); if type(values) ~= 'table' or #values > max_scope_members then return nil end
  local count = 0; for key, value in pairs(values) do if type(key) ~= 'number' or key < 1 or key ~= math.floor(key) or type(value) ~= 'string' then return nil end; count = count + 1 end
  if count ~= #values then return nil end; return values
end
local function contains(values, target) for _, value in ipairs(values or {}) do if value == target then return true end end return false end
if not require_type(states_key, 'hash') or not require_type(due_key, 'zset') or not require_type(closed_key, 'zset') or not require_type(revisions_key, 'hash') or not require_type(scope_runtime_key, 'hash') or not require_type(runtime_scopes_key, 'hash') or not require_type(runtime_accounts_key, 'hash') or not require_type(account_runtimes_key, 'hash') or not require_type(index_meta_key, 'hash') then return redis.error_reply('invalid account circuit Redis key type') end
if redis.call('HGET', index_meta_key, 'version') ~= '1' or redis.call('HGET', index_meta_key, 'status') ~= 'ready' or redis.call('HGET', index_meta_key, 'ownerMode') ~= 'go-runtime-state-v1' then return redis.error_reply('account circuit runtime index is not ready') end
if not now_ms or now_ms < 0 or not limit or limit < 1 or not scan_limit or scan_limit < limit then return redis.error_reply('invalid account circuit due input') end
local candidates = redis.call('ZRANGEBYSCORE', due_key, '-inf', now_ms, 'LIMIT', 0, scan_limit)
local parsed, orphaned = {}, {}
for _, scope_key in ipairs(candidates) do
  local raw = redis.call('HGET', states_key, scope_key)
  if not raw then table.insert(orphaned, scope_key) else
    local entry = cjson.decode(raw)
    if type(entry) ~= 'table' or type(entry['state']) ~= 'table' then return redis.error_reply('invalid account circuit due state') end
    local state = entry['state']; local runtime = state['scope'] and state['scope']['accountRuntimeKey'] or nil
    if state['scopeKey'] ~= scope_key or not runtime or redis.call('HGET', scope_runtime_key, scope_key) ~= runtime then return redis.error_reply('invalid account circuit due index') end
    local account = redis.call('HGET', runtime_accounts_key, runtime); if not account then return redis.error_reply('invalid account circuit due index') end
    local scopes = array(redis.call('HGET', runtime_scopes_key, runtime)); local runtimes = array(redis.call('HGET', account_runtimes_key, account))
    if not scopes or not runtimes or not contains(scopes, scope_key) or not contains(runtimes, runtime) then return redis.error_reply('invalid account circuit due index') end
    local tombstone_raw = redis.call('HGET', revisions_key, account); local state_revision = tonumber(state['dispatchRevision'])
    if not tombstone_raw and state_revision == 1 then redis.call('HSETNX', revisions_key, account, '1'); tombstone_raw = '1' end
    local tombstone = tonumber(tombstone_raw)
    if not tombstone or not state_revision or state_revision ~= tombstone then return redis.error_reply('stale account circuit due revision') end
    table.insert(parsed, { scope = scope_key, entry = entry })
  end
end
for _, scope_key in ipairs(orphaned) do redis.call('ZREM', due_key, scope_key) end
local result = {}
for _, item in ipairs(parsed) do
  local state = item['entry']['state']; local due = nil
  if state['lease'] and tonumber(state['lease']['leaseUntilMs']) <= now_ms then
    if state['lease']['kind'] == 'confirmation' then state['lease'] = nil; state['updatedAtMs'] = now_ms else state['phase'] = state['halfOpenOrigin'] or 'OPEN'; state['lease'] = nil; state['halfOpenOrigin'] = nil; state['retryAtMs'] = now_ms; state['updatedAtMs'] = now_ms end
  end
  if state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING' then due = tonumber(state['retryAtMs']) end
  if state['lease'] then due = tonumber(state['lease']['leaseUntilMs']) end
  redis.call('HSET', states_key, item['scope'], cjson.encode(item['entry']))
  if due then redis.call('ZADD', due_key, due, item['scope']) else redis.call('ZREM', due_key, item['scope']) end
  if due and due <= now_ms and (state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING') and #result < limit then table.insert(result, state) end
end
return cjson.encode({ states = result })
`

const accountCircuitRuntimeClearEscalationLua = `
local escalation_key = KEYS[1]
local runtime_scopes_key = KEYS[2]
local runtime_accounts_key = KEYS[3]
local account_runtimes_key = KEYS[4]
local index_meta_key = KEYS[5]
local runtime_key = ARGV[1]
local account_id = ARGV[2]
local dispatch_revision = tonumber(ARGV[3])
local evidence_id = ARGV[4]
local function array(raw)
  if not raw then return {} end
  local values = cjson.decode(raw); if type(values) ~= 'table' then return nil end
  for _, value in ipairs(values) do if type(value) ~= 'string' then return nil end end
  return values
end
if redis.call('TYPE', escalation_key)['ok'] ~= 'none' and redis.call('TYPE', escalation_key)['ok'] ~= 'hash' then return redis.error_reply('invalid account circuit Redis key type') end
if redis.call('HGET', index_meta_key, 'version') ~= '1' or redis.call('HGET', index_meta_key, 'status') ~= 'ready' or redis.call('HGET', index_meta_key, 'ownerMode') ~= 'go-runtime-state-v1' then return redis.error_reply('account circuit runtime index is not ready') end
if redis.call('HGET', runtime_accounts_key, runtime_key) ~= account_id then return redis.error_reply('invalid account circuit runtime index') end
local raw = redis.call('HGET', escalation_key, runtime_key); if not raw then return 0 end
local evidence = cjson.decode(raw); if type(evidence) ~= 'table' or tonumber(evidence['dispatchRevision']) ~= dispatch_revision or type(evidence['scopes']) ~= 'table' then return 0 end
local found = false; for _, item in ipairs(evidence['scopes']) do if item['evidenceId'] == evidence_id then found = true end end
if not found then return 0 end
local scopes = array(redis.call('HGET', runtime_scopes_key, runtime_key)); if not scopes then return redis.error_reply('invalid account circuit runtime index') end
local remaining = nil
if #scopes == 0 then
  local runtimes = array(redis.call('HGET', account_runtimes_key, account_id)); if not runtimes then return redis.error_reply('invalid account circuit runtime index') end
  remaining = {}; for _, value in ipairs(runtimes) do if value ~= runtime_key then table.insert(remaining, value) end end
end
redis.call('HDEL', escalation_key, runtime_key)
if #scopes == 0 then
  redis.call('HDEL', runtime_scopes_key, runtime_key); redis.call('HDEL', runtime_accounts_key, runtime_key)
  if #remaining == 0 then redis.call('HDEL', account_runtimes_key, account_id) else table.sort(remaining); redis.call('HSET', account_runtimes_key, account_id, cjson.encode(remaining)) end
end
return 1
`

const accountCircuitRuntimeReplaceAccountRevisionLua = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
local revisions_key = KEYS[5]
local scope_runtime_key = KEYS[6]
local runtime_scopes_key = KEYS[7]
local account_runtimes_key = KEYS[8]
local runtime_accounts_key = KEYS[9]
local index_meta_key = KEYS[10]
local account_id = ARGV[1]
local incoming = tonumber(ARGV[2])
local transition_id = ARGV[3]
local now_ms = tonumber(ARGV[4])
local retention_ms = tonumber(ARGV[5])
local max_scope_members = tonumber(ARGV[6])
local function require_type(key, expected) local actual = redis.call('TYPE', key)['ok']; return actual == 'none' or actual == expected end
if not require_type(states_key, 'hash') or not require_type(due_key, 'zset') or not require_type(closed_key, 'zset') or not require_type(escalation_key, 'hash') or not require_type(revisions_key, 'hash') or not require_type(scope_runtime_key, 'hash') or not require_type(runtime_scopes_key, 'hash') or not require_type(account_runtimes_key, 'hash') or not require_type(runtime_accounts_key, 'hash') or not require_type(index_meta_key, 'hash') then return redis.error_reply('invalid account circuit Redis key type') end
if redis.call('HGET', index_meta_key, 'version') ~= '1' or redis.call('HGET', index_meta_key, 'status') ~= 'ready' or redis.call('HGET', index_meta_key, 'ownerMode') ~= 'go-runtime-state-v1' then return redis.error_reply('account circuit runtime index is not ready') end
if not incoming or incoming < 1 or incoming ~= math.floor(incoming) or not now_ms then return redis.error_reply('invalid account circuit dispatch revision') end
local function array(raw)
  if not raw then return {} end
  local values = cjson.decode(raw); if type(values) ~= 'table' or #values > max_scope_members then return nil end
  local count = 0; for key, value in pairs(values) do if type(key) ~= 'number' or key < 1 or key ~= math.floor(key) or type(value) ~= 'string' then return nil end; count = count + 1 end; if count ~= #values then return nil end; return values
end
local function remove_value(values, target)
  local result = {}; for _, value in ipairs(values or {}) do if value ~= target then table.insert(result, value) end end; table.sort(result); return result
end
local current_raw = redis.call('HGET', revisions_key, account_id); local current = current_raw and tonumber(current_raw) or 0
if current_raw and (not current or current < 1 or current ~= math.floor(current)) then return redis.error_reply('invalid revision tombstone') end
if current > incoming then return cjson.encode({ status = 'stale_dispatch_revision', currentDispatchRevision = current, closedScopeCount = 0 }) end
local runtimes = array(redis.call('HGET', account_runtimes_key, account_id)); if not runtimes then return redis.error_reply('invalid account circuit runtime index') end
local changes, evidence_deletes, max_seen = {}, {}, current
for _, runtime_key in ipairs(runtimes) do
  if redis.call('HGET', runtime_accounts_key, runtime_key) ~= account_id then return redis.error_reply('invalid account circuit runtime index') end
  local scopes = array(redis.call('HGET', runtime_scopes_key, runtime_key)); if not scopes then return redis.error_reply('invalid account circuit runtime index') end
  for _, scope_key in ipairs(scopes) do
    if redis.call('HGET', scope_runtime_key, scope_key) ~= runtime_key then return redis.error_reply('invalid account circuit runtime index') end
    local raw = redis.call('HGET', states_key, scope_key); if not raw then return redis.error_reply('invalid account circuit runtime index') end
    local entry = cjson.decode(raw); local state = entry and entry['state']; local revision = state and tonumber(state['dispatchRevision']) or nil; local generation = state and tonumber(state['generation']) or nil
    if not revision or revision < 0 or revision ~= math.floor(revision) or not generation or generation < 0 or generation ~= math.floor(generation) then return redis.error_reply('invalid account circuit runtime state') end
    if revision > max_seen then max_seen = revision end
    if revision < incoming then table.insert(changes, { scope = scope_key, entry = entry }) end
  end
  local evidence_raw = redis.call('HGET', escalation_key, runtime_key)
  if evidence_raw then local evidence = cjson.decode(evidence_raw); local revision = evidence and tonumber(evidence['dispatchRevision']) or nil; if not revision or revision < 0 or revision ~= math.floor(revision) then return redis.error_reply('invalid account circuit escalation evidence') end; if revision > max_seen then max_seen = revision end; if revision < incoming then table.insert(evidence_deletes, runtime_key) end end
end
if max_seen > incoming then return cjson.encode({ status = 'stale_dispatch_revision', currentDispatchRevision = max_seen, closedScopeCount = 0 }) end
for _, change in ipairs(changes) do
  local state = change['entry']['state']; state['phase'] = 'CLOSED'; state['generation'] = tonumber(state['generation']) + 1; state['dispatchRevision'] = tostring(incoming); state['ledgerRevision'] = nil; state['transitionId'] = transition_id; state['backoffAttempt'] = 0; state['recoverySuccessCount'] = 0; state['openedAtMs'] = nil; state['retryAtMs'] = nil; state['failureReason'] = nil; state['lease'] = nil; state['halfOpenOrigin'] = nil; state['incidentId'] = nil; state['shadowedByIncidentId'] = nil; state['childIncidentIds'] = nil; state['childScopeKeys'] = nil; state['requiredRecoveryScopeKeys'] = nil; state['recoveryEvidenceScopeKeys'] = nil; state['updatedAtMs'] = now_ms; change['entry']['closedExpiresAtMs'] = now_ms + retention_ms; change['entry']['replayOrder'] = { transition_id }; redis.call('HSET', states_key, change['scope'], cjson.encode(change['entry'])); redis.call('ZREM', due_key, change['scope']); redis.call('ZADD', closed_key, now_ms + retention_ms, change['scope'])
end
for _, runtime_key in ipairs(evidence_deletes) do
  redis.call('HDEL', escalation_key, runtime_key)
  local scopes = array(redis.call('HGET', runtime_scopes_key, runtime_key))
  if not scopes then return redis.error_reply('invalid account circuit runtime index') end
  if #scopes == 0 then
    local current_runtimes = array(redis.call('HGET', account_runtimes_key, account_id))
    if not current_runtimes then return redis.error_reply('invalid account circuit runtime index') end
    local remaining = remove_value(current_runtimes, runtime_key)
    redis.call('HDEL', runtime_scopes_key, runtime_key)
    redis.call('HDEL', runtime_accounts_key, runtime_key)
    if #remaining == 0 then redis.call('HDEL', account_runtimes_key, account_id) else redis.call('HSET', account_runtimes_key, account_id, cjson.encode(remaining)) end
  end
end
redis.call('HSET', revisions_key, account_id, tostring(incoming))
return cjson.encode({ status = current == incoming and 'idempotent' or 'applied', currentDispatchRevision = incoming, closedScopeCount = #changes })
`

var (
	accountCircuitRuntimeMutationScript               = goredis.NewScript(accountCircuitRuntimeMutationLua)
	accountCircuitRuntimeEscalationScript             = goredis.NewScript(accountCircuitRuntimeEscalationLua)
	accountCircuitRuntimeListDueScript                = goredis.NewScript(accountCircuitRuntimeListDueLua)
	accountCircuitRuntimeClearEscalationScript        = goredis.NewScript(accountCircuitRuntimeClearEscalationLua)
	accountCircuitRuntimeReplaceAccountRevisionScript = goredis.NewScript(accountCircuitRuntimeReplaceAccountRevisionLua)
)

type accountCircuitRuntimeScopeWire struct {
	Kind              string `json:"kind"`
	AccountRuntimeKey string `json:"accountRuntimeKey"`
	KeyFingerprint    string `json:"keyFingerprint,omitempty"`
	ProtocolProfile   string `json:"protocolProfile,omitempty"`
	RequestLane       string `json:"requestLane,omitempty"`
	ModelBucket       string `json:"modelBucket,omitempty"`
}

type accountCircuitRuntimeLeaseWire struct {
	Kind         string `json:"kind"`
	LeaseID      string `json:"leaseId"`
	LeaseUntilMS int64  `json:"leaseUntilMs"`
}

type accountCircuitRuntimeStateWire struct {
	ScopeKey                  string                          `json:"scopeKey"`
	Scope                     accountCircuitRuntimeScopeWire  `json:"scope"`
	Phase                     string                          `json:"phase"`
	Generation                int                             `json:"generation"`
	DispatchRevision          string                          `json:"dispatchRevision"`
	LedgerRevision            string                          `json:"ledgerRevision,omitempty"`
	TransitionID              string                          `json:"transitionId"`
	BackoffAttempt            int                             `json:"backoffAttempt"`
	RecoverySuccessCount      int                             `json:"recoverySuccessCount"`
	OpenedAtMS                *int64                          `json:"openedAtMs,omitempty"`
	RetryAtMS                 *int64                          `json:"retryAtMs,omitempty"`
	FailureReason             string                          `json:"failureReason,omitempty"`
	Lease                     *accountCircuitRuntimeLeaseWire `json:"lease,omitempty"`
	HalfOpenOrigin            string                          `json:"halfOpenOrigin,omitempty"`
	IncidentID                string                          `json:"incidentId,omitempty"`
	ShadowedByIncidentID      string                          `json:"shadowedByIncidentId,omitempty"`
	ChildIncidentIDs          []string                        `json:"childIncidentIds,omitempty"`
	ChildScopeKeys            []string                        `json:"childScopeKeys,omitempty"`
	RequiredRecoveryScopeKeys []string                        `json:"requiredRecoveryScopeKeys,omitempty"`
	RecoveryEvidenceScopeKeys []string                        `json:"recoveryEvidenceScopeKeys,omitempty"`
	UpdatedAtMS               int64                           `json:"updatedAtMs"`
}

type accountCircuitRuntimeMutationWire struct {
	Operation        string                          `json:"operation"`
	AccountID        string                          `json:"accountId"`
	Scope            accountCircuitRuntimeScopeWire  `json:"scope"`
	ScopeKey         string                          `json:"scopeKey"`
	DispatchRevision int64                           `json:"dispatchRevision,omitempty"`
	Generation       int                             `json:"generation,omitempty"`
	TransitionID     string                          `json:"transitionId,omitempty"`
	LeaseID          string                          `json:"leaseId,omitempty"`
	LeaseUntilMS     int64                           `json:"leaseUntilMs,omitempty"`
	Outcome          string                          `json:"outcome,omitempty"`
	Reason           string                          `json:"reason,omitempty"`
	EvidenceScopeKey string                          `json:"evidenceScopeKey,omitempty"`
	State            *accountCircuitRuntimeStateWire `json:"state,omitempty"`
	RetainedUntilMS  int64                           `json:"retainedUntilMs,omitempty"`
	NowMS            int64                           `json:"nowMs"`
}

type accountCircuitRuntimeMutationResponseWire struct {
	Status string                         `json:"status"`
	State  accountCircuitRuntimeStateWire `json:"state"`
}

type accountCircuitRuntimeEscalationResponseWire struct {
	Status                string                         `json:"status"`
	AccountState          accountCircuitRuntimeStateWire `json:"accountState"`
	ProtocolScopeCount    int                            `json:"protocolScopeCount"`
	ConfirmedFailureCount int                            `json:"confirmedFailureCount"`
}

type accountCircuitRuntimeDueResponseWire struct {
	States []accountCircuitRuntimeStateWire `json:"states"`
}

type accountCircuitRuntimeAccountRevisionResponseWire struct {
	Status                  string `json:"status"`
	CurrentDispatchRevision int64  `json:"currentDispatchRevision"`
	ClosedScopeCount        int    `json:"closedScopeCount"`
}

type AccountCircuitRuntimeStore struct {
	client      *Client
	keys        accountCircuitRevisionKeys
	retention   time.Duration
	capacity    int
	replayLimit int
	maxLease    time.Duration
	now         func() time.Time
}

func NewAccountCircuitRuntimeStore(client *Client, retention time.Duration, capacity int) (*AccountCircuitRuntimeStore, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("Redis state client is required")
	}
	if retention == 0 {
		retention = DefaultAccountCircuitClosedRetention
	}
	if retention <= 0 || retention > 24*time.Hour {
		return nil, fmt.Errorf("account circuit closed retention is invalid")
	}
	if capacity == 0 {
		capacity = 100000
	}
	if capacity < 1 || capacity > 1000000 {
		return nil, fmt.Errorf("account circuit runtime capacity is invalid")
	}
	keys, err := accountCircuitRevisionRedisKeys(client.namespace, AccountCircuitRedisStoreName)
	if err != nil {
		return nil, err
	}
	return &AccountCircuitRuntimeStore{
		client: client, keys: keys, retention: retention, capacity: capacity,
		replayLimit: DefaultAccountCircuitRuntimeReplayLimit, maxLease: DefaultAccountCircuitRuntimeMaxLease, now: time.Now,
	}, nil
}

func (s *AccountCircuitRuntimeStore) WithNow(now func() time.Time) *AccountCircuitRuntimeStore {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *AccountCircuitRuntimeStore) WithMaxLease(maxLease time.Duration) *AccountCircuitRuntimeStore {
	if maxLease > 0 {
		s.maxLease = maxLease
	}
	return s
}

func (s *AccountCircuitRuntimeStore) GetGatewayAccountCircuit(ctx context.Context, input GatewayAccountCircuitGetInput) (GatewayAccountCircuitState, error) {
	if err := s.validateIdentity(input.AccountID, input.Scope); err != nil {
		return GatewayAccountCircuitState{}, err
	}
	result, err := s.runMutation(ctx, accountCircuitRuntimeMutationWire{Operation: "get", AccountID: input.AccountID, Scope: runtimeScopeWire(input.Scope), ScopeKey: mustGatewayAccountCircuitScopeKey(input.Scope), NowMS: s.resolveNow(input.Now).UnixMilli()})
	if err != nil {
		return GatewayAccountCircuitState{}, err
	}
	return result.State, nil
}

func (s *AccountCircuitRuntimeStore) SuspectGatewayAccountCircuit(ctx context.Context, input GatewayAccountCircuitSuspectInput) (GatewayAccountCircuitMutationResult, error) {
	if err := s.validateMutationIdentity(input.AccountID, input.Scope, input.DispatchRevision, input.TransitionID); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	if err := validateRuntimeText(input.Reason, 1024, "reason"); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return s.runMutation(ctx, accountCircuitRuntimeMutationWire{Operation: "suspect", AccountID: input.AccountID, Scope: runtimeScopeWire(input.Scope), ScopeKey: mustGatewayAccountCircuitScopeKey(input.Scope), DispatchRevision: input.DispatchRevision, TransitionID: input.TransitionID, Reason: input.Reason, NowMS: s.resolveNow(input.Now).UnixMilli()})
}

func (s *AccountCircuitRuntimeStore) AcquireGatewayAccountCircuitConfirmationLease(ctx context.Context, input GatewayAccountCircuitAcquireConfirmationLeaseInput) (GatewayAccountCircuitMutationResult, error) {
	if err := s.validateLeaseIdentity(input.GatewayAccountCircuitTransitionIdentity, input.LeaseID, input.LeaseUntil); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return s.runTransition(ctx, "acquire_confirmation", input.GatewayAccountCircuitTransitionIdentity, input.LeaseID, input.LeaseUntil, "", "", "")
}

func (s *AccountCircuitRuntimeStore) CompleteGatewayAccountCircuitConfirmation(ctx context.Context, input GatewayAccountCircuitCompleteConfirmationInput) (GatewayAccountCircuitMutationResult, error) {
	if err := s.validateTransitionIdentity(input.GatewayAccountCircuitTransitionIdentity); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	if err := validateRuntimeText(input.LeaseID, 256, "lease id"); err != nil || !validRuntimeOutcome(input.Outcome) {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("account circuit confirmation completion is invalid")
	}
	if input.Reason != "" {
		if err := validateRuntimeText(input.Reason, 1024, "reason"); err != nil {
			return GatewayAccountCircuitMutationResult{}, err
		}
	}
	return s.runTransition(ctx, "complete_confirmation", input.GatewayAccountCircuitTransitionIdentity, input.LeaseID, time.Time{}, input.Outcome, input.Reason, "")
}

func (s *AccountCircuitRuntimeStore) AcquireGatewayAccountCircuitCanaryLease(ctx context.Context, input GatewayAccountCircuitAcquireCanaryLeaseInput) (GatewayAccountCircuitMutationResult, error) {
	if err := s.validateLeaseIdentity(input.GatewayAccountCircuitTransitionIdentity, input.LeaseID, input.LeaseUntil); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return s.runTransition(ctx, "acquire_canary", input.GatewayAccountCircuitTransitionIdentity, input.LeaseID, input.LeaseUntil, "", "", "")
}

func (s *AccountCircuitRuntimeStore) CompleteGatewayAccountCircuitCanary(ctx context.Context, input GatewayAccountCircuitCompleteCanaryInput) (GatewayAccountCircuitMutationResult, error) {
	if err := s.validateTransitionIdentity(input.GatewayAccountCircuitTransitionIdentity); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	if err := validateRuntimeText(input.LeaseID, 256, "lease id"); err != nil || !validRuntimeOutcome(input.Outcome) {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("account circuit canary completion is invalid")
	}
	if input.Reason != "" {
		if err := validateRuntimeText(input.Reason, 1024, "reason"); err != nil {
			return GatewayAccountCircuitMutationResult{}, err
		}
	}
	if input.EvidenceScopeKey != "" {
		if err := validateRuntimeText(input.EvidenceScopeKey, 2048, "evidence scope key"); err != nil {
			return GatewayAccountCircuitMutationResult{}, err
		}
	}
	return s.runTransition(ctx, "complete_canary", input.GatewayAccountCircuitTransitionIdentity, input.LeaseID, time.Time{}, input.Outcome, input.Reason, input.EvidenceScopeKey)
}

func (s *AccountCircuitRuntimeStore) ReplaceGatewayAccountCircuitDispatchRevision(ctx context.Context, input GatewayAccountCircuitReplaceDispatchRevisionInput) (GatewayAccountCircuitMutationResult, error) {
	if err := s.validateMutationIdentity(input.AccountID, input.Scope, input.DispatchRevision, input.TransitionID); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	return s.runMutation(ctx, accountCircuitRuntimeMutationWire{Operation: "replace_revision", AccountID: input.AccountID, Scope: runtimeScopeWire(input.Scope), ScopeKey: mustGatewayAccountCircuitScopeKey(input.Scope), DispatchRevision: input.DispatchRevision, TransitionID: input.TransitionID, NowMS: s.resolveNow(input.Now).UnixMilli()})
}

func (s *AccountCircuitRuntimeStore) RestoreGatewayAccountCircuit(ctx context.Context, input GatewayAccountCircuitRestoreInput) (GatewayAccountCircuitMutationResult, error) {
	if err := s.validateIdentity(input.AccountID, input.State.Scope); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	if err := ValidateGatewayAccountCircuitState(input.State); err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	if input.State.DispatchRevision < 1 {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("account circuit restore dispatch revision is invalid")
	}
	state, err := runtimeStateToWire(input.State)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	retainedUntilMS := int64(0)
	if input.RetainedUntil != nil {
		retainedUntilMS = input.RetainedUntil.UTC().UnixMilli()
	}
	return s.runMutation(ctx, accountCircuitRuntimeMutationWire{Operation: "restore", AccountID: input.AccountID, Scope: runtimeScopeWire(input.State.Scope), ScopeKey: input.State.ScopeKey, DispatchRevision: input.State.DispatchRevision, State: &state, RetainedUntilMS: retainedUntilMS, NowMS: s.resolveNow(input.Now).UnixMilli()})
}

func (s *AccountCircuitRuntimeStore) RecordGatewayAccountCircuitProtocolModelOpenEvidence(ctx context.Context, input GatewayAccountCircuitProtocolModelOpenEvidenceInput) (GatewayAccountCircuitEscalationResult, error) {
	if input.Scope.Kind != GatewayAccountCircuitScopeProtocolModel || input.Generation < 0 || input.DispatchRevision < 1 || input.ConfirmedFailureCount < 1 || input.MaxProtocolScopes < 1 || input.MaxProtocolScopes > GatewayAccountCircuitRuntimeMaxEvidenceScopes || input.Window <= 0 || input.Window > GatewayAccountCircuitRuntimeMaxEvidenceWindow {
		return GatewayAccountCircuitEscalationResult{}, fmt.Errorf("account circuit escalation input is invalid")
	}
	if err := s.validateIdentity(input.AccountID, input.Scope); err != nil {
		return GatewayAccountCircuitEscalationResult{}, err
	}
	for _, value := range []struct{ value, name string }{{input.EvidenceID, "evidence id"}, {input.AccountTransitionID, "account transition id"}, {input.Reason, "reason"}} {
		if err := validateRuntimeText(value.value, 1024, value.name); err != nil {
			return GatewayAccountCircuitEscalationResult{}, err
		}
	}
	accountScope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeAccount, AccountRuntimeKey: input.Scope.AccountRuntimeKey}
	accountScopeKey, err := GatewayAccountCircuitScopeKey(accountScope)
	if err != nil {
		return GatewayAccountCircuitEscalationResult{}, err
	}
	payload := map[string]any{
		"accountId": input.AccountID, "scope": runtimeScopeWire(input.Scope), "scopeKey": mustGatewayAccountCircuitScopeKey(input.Scope), "accountScopeKey": accountScopeKey,
		"generation": input.Generation, "dispatchRevision": input.DispatchRevision, "evidenceId": input.EvidenceID, "accountTransitionId": input.AccountTransitionID,
		"reason": input.Reason, "confirmedFailureCount": input.ConfirmedFailureCount, "windowMs": input.Window.Milliseconds(), "maxProtocolScopes": input.MaxProtocolScopes, "nowMs": s.resolveNow(input.Now).UnixMilli(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return GatewayAccountCircuitEscalationResult{}, fmt.Errorf("encode account circuit escalation: %w", err)
	}
	value, err := accountCircuitRuntimeEscalationScript.Run(ctx, s.client.client, s.runtimeKeys(), string(raw), strconv.Itoa(s.capacity), strconv.FormatInt(s.retention.Milliseconds(), 10), strconv.Itoa(s.capacity)).Result()
	if err != nil {
		return GatewayAccountCircuitEscalationResult{}, fmt.Errorf("record account circuit escalation: %w", err)
	}
	encoded, err := runtimeRedisBytes(value)
	if err != nil {
		return GatewayAccountCircuitEscalationResult{}, err
	}
	var response accountCircuitRuntimeEscalationResponseWire
	if err := json.Unmarshal(encoded, &response); err != nil {
		return GatewayAccountCircuitEscalationResult{}, fmt.Errorf("decode account circuit escalation: %w", err)
	}
	state, err := runtimeStateFromWire(response.AccountState)
	if err != nil {
		return GatewayAccountCircuitEscalationResult{}, err
	}
	status := GatewayAccountCircuitEscalationStatus(response.Status)
	switch status {
	case GatewayAccountCircuitEscalationRecorded, GatewayAccountCircuitEscalationEscalated, GatewayAccountCircuitEscalationAlreadyActive, GatewayAccountCircuitEscalationIdempotent, GatewayAccountCircuitEscalationNotFound, GatewayAccountCircuitEscalationStateMismatch, GatewayAccountCircuitEscalationStaleGeneration, GatewayAccountCircuitEscalationStaleDispatchRevision, GatewayAccountCircuitEscalationCapacityExhausted:
	default:
		return GatewayAccountCircuitEscalationResult{}, fmt.Errorf("account circuit escalation status is invalid")
	}
	if response.ProtocolScopeCount < 0 || response.ConfirmedFailureCount < 0 {
		return GatewayAccountCircuitEscalationResult{}, fmt.Errorf("account circuit escalation result is invalid")
	}
	return GatewayAccountCircuitEscalationResult{Status: status, AccountState: state, ProtocolScopeCount: response.ProtocolScopeCount, ConfirmedFailureCount: response.ConfirmedFailureCount}, nil
}

func (s *AccountCircuitRuntimeStore) ClearGatewayAccountCircuitEscalationEvidence(ctx context.Context, input GatewayAccountCircuitClearAccountEscalationEvidenceInput) (bool, error) {
	if !validAccountCircuitRevisionText(input.AccountID, 256) || !validAccountCircuitRevisionText(input.AccountRuntimeKey, 1024) || accountCircuitRuntimeAccountID(input.AccountRuntimeKey) != input.AccountID || input.DispatchRevision < 1 || validateRuntimeText(input.EvidenceID, 1024, "evidence id") != nil {
		return false, fmt.Errorf("account circuit escalation clear input is invalid")
	}
	value, err := accountCircuitRuntimeClearEscalationScript.Run(ctx, s.client.client, []string{s.keys.escalation, s.keys.runtimeScopes, s.keys.runtimeAccounts, s.keys.accountRuntimes, s.keys.indexMeta}, input.AccountRuntimeKey, input.AccountID, strconv.FormatInt(input.DispatchRevision, 10), input.EvidenceID).Result()
	if err != nil {
		return false, fmt.Errorf("clear account circuit escalation evidence: %w", err)
	}
	parsed, err := redisInt64(value)
	if err != nil || (parsed != 0 && parsed != 1) {
		return false, fmt.Errorf("decode account circuit escalation clear result: %w", err)
	}
	return parsed == 1, nil
}

func (s *AccountCircuitRuntimeStore) ReplaceGatewayAccountCircuitAccountDispatchRevision(ctx context.Context, input GatewayAccountCircuitReplaceAccountDispatchRevisionInput) (GatewayAccountCircuitAccountRevisionResult, error) {
	if !validAccountCircuitRevisionText(input.AccountID, 256) || input.DispatchRevision < 1 || validateRuntimeText(input.TransitionID, 256, "transition id") != nil {
		return GatewayAccountCircuitAccountRevisionResult{}, fmt.Errorf("account circuit account revision input is invalid")
	}
	value, err := accountCircuitRuntimeReplaceAccountRevisionScript.Run(ctx, s.client.client, s.runtimeKeys(), input.AccountID, strconv.FormatInt(input.DispatchRevision, 10), input.TransitionID, strconv.FormatInt(s.resolveNow(input.Now).UnixMilli(), 10), strconv.FormatInt(s.retention.Milliseconds(), 10), strconv.Itoa(s.capacity)).Result()
	if err != nil {
		return GatewayAccountCircuitAccountRevisionResult{}, fmt.Errorf("replace account circuit dispatch revision: %w", err)
	}
	encoded, err := runtimeRedisBytes(value)
	if err != nil {
		return GatewayAccountCircuitAccountRevisionResult{}, err
	}
	var response accountCircuitRuntimeAccountRevisionResponseWire
	if err := json.Unmarshal(encoded, &response); err != nil {
		return GatewayAccountCircuitAccountRevisionResult{}, fmt.Errorf("decode account circuit account revision: %w", err)
	}
	status := GatewayAccountCircuitMutationStatus(response.Status)
	if status != GatewayAccountCircuitMutationApplied && status != GatewayAccountCircuitMutationIdempotent && status != GatewayAccountCircuitMutationStaleDispatchRevision || response.CurrentDispatchRevision < 1 || response.ClosedScopeCount < 0 {
		return GatewayAccountCircuitAccountRevisionResult{}, fmt.Errorf("account circuit account revision result is invalid")
	}
	return GatewayAccountCircuitAccountRevisionResult{Status: status, CurrentDispatchRevision: response.CurrentDispatchRevision, ClosedScopeCount: response.ClosedScopeCount}, nil
}

func (s *AccountCircuitRuntimeStore) ListDueGatewayAccountCircuits(ctx context.Context, input GatewayAccountCircuitListDueInput) ([]GatewayAccountCircuitState, error) {
	if input.Limit < 1 || input.Limit > GatewayAccountCircuitRuntimeMaxDuePage {
		return nil, fmt.Errorf("account circuit due limit is invalid")
	}
	value, err := accountCircuitRuntimeListDueScript.Run(ctx, s.client.client, []string{s.keys.states, s.keys.due, s.keys.closed, s.keys.revisions, s.keys.scopeRuntime, s.keys.runtimeScopes, s.keys.runtimeAccounts, s.keys.accountRuntimes, s.keys.indexMeta}, strconv.FormatInt(s.resolveNow(input.Now).UnixMilli(), 10), strconv.Itoa(input.Limit), strconv.Itoa(min(GatewayAccountCircuitRuntimeMaxDuePage*4, input.Limit*4)), strconv.Itoa(s.capacity)).Result()
	if err != nil {
		return nil, fmt.Errorf("list due account circuits: %w", err)
	}
	encoded, err := runtimeRedisBytes(value)
	if err != nil {
		return nil, err
	}
	var response accountCircuitRuntimeDueResponseWire
	if err := json.Unmarshal(encoded, &response); err != nil || len(response.States) > input.Limit {
		return nil, fmt.Errorf("decode due account circuits: %w", err)
	}
	states := make([]GatewayAccountCircuitState, 0, len(response.States))
	for _, value := range response.States {
		state, err := runtimeStateFromWire(value)
		if err != nil {
			return nil, err
		}
		if state.Phase != GatewayAccountCircuitPhaseOpen && state.Phase != GatewayAccountCircuitPhaseRecovering {
			return nil, fmt.Errorf("account circuit due state is not canary eligible")
		}
		states = append(states, state)
	}
	return states, nil
}

func (s *AccountCircuitRuntimeStore) runTransition(ctx context.Context, operation string, identity GatewayAccountCircuitTransitionIdentity, leaseID string, leaseUntil time.Time, outcome GatewayAccountCircuitCompletionOutcome, reason, evidenceScopeKey string) (GatewayAccountCircuitMutationResult, error) {
	payload := accountCircuitRuntimeMutationWire{Operation: operation, AccountID: identity.AccountID, Scope: runtimeScopeWire(identity.Scope), ScopeKey: mustGatewayAccountCircuitScopeKey(identity.Scope), DispatchRevision: identity.DispatchRevision, Generation: identity.Generation, TransitionID: identity.TransitionID, LeaseID: leaseID, Outcome: string(outcome), Reason: reason, EvidenceScopeKey: evidenceScopeKey, NowMS: s.resolveNow(identity.Now).UnixMilli()}
	if !leaseUntil.IsZero() {
		payload.LeaseUntilMS = leaseUntil.UTC().UnixMilli()
	}
	return s.runMutation(ctx, payload)
}

func (s *AccountCircuitRuntimeStore) runMutation(ctx context.Context, payload accountCircuitRuntimeMutationWire) (GatewayAccountCircuitMutationResult, error) {
	if s == nil || s.client == nil || s.client.client == nil {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("account circuit runtime store is required")
	}
	if ctx == nil {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("account circuit runtime context is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("encode account circuit runtime mutation: %w", err)
	}
	value, err := accountCircuitRuntimeMutationScript.Run(ctx, s.client.client, s.runtimeKeys(), string(raw), strconv.Itoa(s.capacity), strconv.FormatInt(s.retention.Milliseconds(), 10), strconv.Itoa(s.replayLimit), strconv.Itoa(s.capacity)).Result()
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("mutate account circuit runtime: %w", err)
	}
	encoded, err := runtimeRedisBytes(value)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	var response accountCircuitRuntimeMutationResponseWire
	if err := json.Unmarshal(encoded, &response); err != nil {
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("decode account circuit runtime mutation: %w", err)
	}
	state, err := runtimeStateFromWire(response.State)
	if err != nil {
		return GatewayAccountCircuitMutationResult{}, err
	}
	status := GatewayAccountCircuitMutationStatus(response.Status)
	switch status {
	case GatewayAccountCircuitMutationApplied, GatewayAccountCircuitMutationIdempotent, GatewayAccountCircuitMutationNotFound, GatewayAccountCircuitMutationStateMismatch, GatewayAccountCircuitMutationStaleGeneration, GatewayAccountCircuitMutationStaleDispatchRevision, GatewayAccountCircuitMutationLeaseMismatch, GatewayAccountCircuitMutationNotDue, GatewayAccountCircuitMutationCapacityExhausted:
	default:
		return GatewayAccountCircuitMutationResult{}, fmt.Errorf("account circuit runtime mutation status is invalid")
	}
	return GatewayAccountCircuitMutationResult{Status: status, State: state}, nil
}

func (s *AccountCircuitRuntimeStore) runtimeKeys() []string {
	return []string{s.keys.states, s.keys.due, s.keys.closed, s.keys.escalation, s.keys.revisions, s.keys.scopeRuntime, s.keys.runtimeScopes, s.keys.accountRuntimes, s.keys.runtimeAccounts, s.keys.indexMeta, s.keys.ledgerRevisions}
}

func (s *AccountCircuitRuntimeStore) validateIdentity(accountID string, scope GatewayAccountCircuitScope) error {
	if !validAccountCircuitRevisionText(accountID, 256) || accountCircuitRuntimeAccountID(scope.AccountRuntimeKey) != accountID {
		return fmt.Errorf("account circuit runtime account identity is invalid")
	}
	return ValidateGatewayAccountCircuitScope(scope)
}

func (s *AccountCircuitRuntimeStore) validateMutationIdentity(accountID string, scope GatewayAccountCircuitScope, dispatchRevision int64, transitionID string) error {
	if err := s.validateIdentity(accountID, scope); err != nil {
		return err
	}
	if dispatchRevision < 1 || validateRuntimeText(transitionID, 256, "transition id") != nil {
		return fmt.Errorf("account circuit transition identity is invalid")
	}
	return nil
}

func (s *AccountCircuitRuntimeStore) validateTransitionIdentity(identity GatewayAccountCircuitTransitionIdentity) error {
	if identity.Generation < 0 {
		return fmt.Errorf("account circuit generation is invalid")
	}
	return s.validateMutationIdentity(identity.AccountID, identity.Scope, identity.DispatchRevision, identity.TransitionID)
}

func (s *AccountCircuitRuntimeStore) validateLeaseIdentity(identity GatewayAccountCircuitTransitionIdentity, leaseID string, leaseUntil time.Time) error {
	if err := s.validateTransitionIdentity(identity); err != nil {
		return err
	}
	if err := validateRuntimeText(leaseID, 256, "lease id"); err != nil {
		return err
	}
	now := s.resolveNow(identity.Now)
	if leaseUntil.IsZero() || !leaseUntil.After(now) || leaseUntil.Sub(now) > s.maxLease {
		return fmt.Errorf("account circuit lease deadline is invalid")
	}
	return nil
}

func (s *AccountCircuitRuntimeStore) resolveNow(value time.Time) time.Time {
	if value.IsZero() {
		value = s.now()
	}
	return value.UTC()
}

func runtimeScopeWire(scope GatewayAccountCircuitScope) accountCircuitRuntimeScopeWire {
	return accountCircuitRuntimeScopeWire{Kind: string(scope.Kind), AccountRuntimeKey: scope.AccountRuntimeKey, KeyFingerprint: scope.KeyFingerprint, ProtocolProfile: scope.ProtocolProfile, RequestLane: scope.RequestLane, ModelBucket: scope.ModelBucket}
}

func runtimeStateFromWire(value accountCircuitRuntimeStateWire) (GatewayAccountCircuitState, error) {
	dispatchRevision, err := runtimeWireRevision(value.DispatchRevision)
	if err != nil {
		return GatewayAccountCircuitState{}, err
	}
	ledgerRevision := int64(0)
	if value.LedgerRevision != "" {
		ledgerRevision, err = runtimeWireRevision(value.LedgerRevision)
		if err != nil {
			return GatewayAccountCircuitState{}, err
		}
	}
	scope := GatewayAccountCircuitScope{Kind: GatewayAccountCircuitScopeKind(value.Scope.Kind), AccountRuntimeKey: value.Scope.AccountRuntimeKey, KeyFingerprint: value.Scope.KeyFingerprint, ProtocolProfile: value.Scope.ProtocolProfile, RequestLane: value.Scope.RequestLane, ModelBucket: value.Scope.ModelBucket}
	openedAt := runtimeWireTime(value.OpenedAtMS)
	retryAt := runtimeWireTime(value.RetryAtMS)
	var lease *GatewayAccountCircuitLease
	if value.Lease != nil {
		if value.Lease.LeaseUntilMS <= 0 {
			return GatewayAccountCircuitState{}, fmt.Errorf("account circuit runtime lease is invalid")
		}
		lease = &GatewayAccountCircuitLease{Kind: GatewayAccountCircuitLeaseKind(value.Lease.Kind), ID: value.Lease.LeaseID, Until: time.UnixMilli(value.Lease.LeaseUntilMS).UTC()}
	}
	state := GatewayAccountCircuitState{ScopeKey: value.ScopeKey, Scope: scope, Phase: GatewayAccountCircuitPhase(value.Phase), Generation: value.Generation, DispatchRevision: dispatchRevision, LedgerRevision: ledgerRevision, TransitionID: value.TransitionID, BackoffAttempt: value.BackoffAttempt, RecoverySuccessCount: value.RecoverySuccessCount, OpenedAt: openedAt, RetryAt: retryAt, FailureReason: value.FailureReason, Lease: lease, HalfOpenOrigin: GatewayAccountCircuitPhase(value.HalfOpenOrigin), IncidentID: value.IncidentID, ShadowedByIncidentID: value.ShadowedByIncidentID, ChildIncidentIDs: canonicalRuntimeStrings(value.ChildIncidentIDs), ChildScopeKeys: canonicalRuntimeStrings(value.ChildScopeKeys), RequiredRecoveryScopeKeys: canonicalRuntimeStrings(value.RequiredRecoveryScopeKeys), RecoveryEvidenceScopeKeys: canonicalRuntimeStrings(value.RecoveryEvidenceScopeKeys), UpdatedAt: time.UnixMilli(value.UpdatedAtMS).UTC()}
	if err := ValidateGatewayAccountCircuitState(state); err != nil {
		return GatewayAccountCircuitState{}, err
	}
	return state, nil
}

func runtimeStateToWire(state GatewayAccountCircuitState) (accountCircuitRuntimeStateWire, error) {
	if err := ValidateGatewayAccountCircuitState(state); err != nil {
		return accountCircuitRuntimeStateWire{}, err
	}
	value := accountCircuitRuntimeStateWire{
		ScopeKey: state.ScopeKey, Scope: runtimeScopeWire(state.Scope), Phase: string(state.Phase), Generation: state.Generation,
		DispatchRevision: strconv.FormatInt(state.DispatchRevision, 10), TransitionID: state.TransitionID,
		BackoffAttempt: state.BackoffAttempt, RecoverySuccessCount: state.RecoverySuccessCount,
		FailureReason: state.FailureReason, HalfOpenOrigin: string(state.HalfOpenOrigin), IncidentID: state.IncidentID,
		ShadowedByIncidentID: state.ShadowedByIncidentID, ChildIncidentIDs: append([]string(nil), state.ChildIncidentIDs...),
		ChildScopeKeys: append([]string(nil), state.ChildScopeKeys...), RequiredRecoveryScopeKeys: append([]string(nil), state.RequiredRecoveryScopeKeys...),
		RecoveryEvidenceScopeKeys: append([]string(nil), state.RecoveryEvidenceScopeKeys...), UpdatedAtMS: state.UpdatedAt.UTC().UnixMilli(),
	}
	if state.LedgerRevision > 0 {
		value.LedgerRevision = strconv.FormatInt(state.LedgerRevision, 10)
	}
	if state.OpenedAt != nil {
		milliseconds := state.OpenedAt.UTC().UnixMilli()
		value.OpenedAtMS = &milliseconds
	}
	if state.RetryAt != nil {
		milliseconds := state.RetryAt.UTC().UnixMilli()
		value.RetryAtMS = &milliseconds
	}
	if state.Lease != nil {
		value.Lease = &accountCircuitRuntimeLeaseWire{Kind: string(state.Lease.Kind), LeaseID: state.Lease.ID, LeaseUntilMS: state.Lease.Until.UTC().UnixMilli()}
	}
	return value, nil
}

func runtimeWireRevision(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("account circuit runtime revision is invalid")
	}
	return parsed, nil
}

func runtimeWireTime(value *int64) *time.Time {
	if value == nil {
		return nil
	}
	parsed := time.UnixMilli(*value).UTC()
	return &parsed
}

func canonicalRuntimeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index := 1; index < len(result); index++ {
		if result[index-1] == result[index] {
			return nil
		}
	}
	return result
}

func runtimeRedisBytes(value any) ([]byte, error) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return typed, nil
	default:
		return nil, fmt.Errorf("unexpected account circuit Redis result type %T", value)
	}
}

func redisInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	default:
		return 0, fmt.Errorf("redis integer value is invalid")
	}
}

func accountCircuitRuntimeAccountID(runtimeKey string) string {
	if index := strings.Index(runtimeKey, ":authorized:"); index > 0 {
		return runtimeKey[:index]
	}
	return runtimeKey
}

func validateRuntimeText(value string, maxBytes int, name string) error {
	if !validAccountCircuitRevisionText(value, maxBytes) {
		return fmt.Errorf("account circuit %s is invalid", name)
	}
	return nil
}

func validRuntimeOutcome(value GatewayAccountCircuitCompletionOutcome) bool {
	return value == GatewayAccountCircuitCompletionFramingComplete || value == GatewayAccountCircuitCompletionTransportFailure || value == GatewayAccountCircuitCompletionUnknown
}

func mustGatewayAccountCircuitScopeKey(scope GatewayAccountCircuitScope) string {
	value, err := GatewayAccountCircuitScopeKey(scope)
	if err != nil {
		panic(err)
	}
	return value
}

var _ GatewayAccountCircuitStore = (*AccountCircuitRuntimeStore)(nil)
