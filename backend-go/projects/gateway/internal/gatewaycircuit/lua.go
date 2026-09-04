package gatewaycircuit

// The Lua scripts below are the Node account-circuit scripts verbatim
// (backend/src/modules/gateway/runtime/account-circuit-redis-store.ts at git
// HEAD). They are the authoritative transition semantics for the shared
// runtime state; the Go store only builds the same JSON payloads and parses
// the same cjson responses.

const redisAccountCircuitTransitionScript = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
local capacity_saturated_key = KEYS[5]
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
  if tonumber(redis.call('HLEN', states_key)) < capacity then redis.call('DEL', capacity_saturated_key) end
end

local function cleanup_closed()
  local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, math.min(capacity, 256))
  for _, key in ipairs(expired) do
    local raw = redis.call('HGET', states_key, key)
    if not raw then
      redis.call('ZREM', due_key, key)
      redis.call('ZREM', closed_key, key)
    else
      local entry = cjson.decode(raw)
      local state = entry['state']
      local expires_at = tonumber(entry['closedExpiresAtMs'])
      if state['phase'] == 'CLOSED' and (not expires_at or expires_at <= now_ms) then
        remove_scope(key)
      elseif state['phase'] == 'CLOSED' then
        redis.call('ZADD', closed_key, expires_at, key)
      else
        redis.call('ZREM', closed_key, key)
      end
    end
  end
  return #expired
end

local function due_at(state)
  if state['phase'] == 'CLOSED' then return nil end
  local lease = state['lease']
  if lease then return tonumber(lease['leaseUntilMs']) end
  if state['phase'] == 'SUSPECT' or state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING' then
    return tonumber(state['retryAtMs'])
  end
  return nil
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
  if state['phase'] ~= 'CLOSED' then
    local required = tonumber(state['confirmationFailuresRequired'] or 1)
    if not required or required ~= math.floor(required) or required < 1 or required > 5 then
      error('invalid confirmationFailuresRequired')
    end
    state['confirmationFailuresRequired'] = required
    local count = tonumber(state['confirmationFailureCount'] or 0)
    if not count or count ~= math.floor(count) or count < 0 or count > required then
      error('invalid confirmationFailureCount')
    end
    state['confirmationFailureCount'] = count
    local evidence = state['failureEvidenceKeys'] or {}
    local normalized_evidence = {}
    local seen_evidence = {}
    for _, evidence_key in ipairs(evidence) do
      if type(evidence_key) ~= 'string' or string.len(evidence_key) ~= 64 or not string.match(evidence_key, '^[a-f0-9]+$') then
        error('invalid failureEvidenceKeys')
      end
      if not seen_evidence[evidence_key] then
        seen_evidence[evidence_key] = true
        table.insert(normalized_evidence, evidence_key)
      end
    end
    while #normalized_evidence > required + 1 do table.remove(normalized_evidence, 1) end
    state['failureEvidenceKeys'] = normalized_evidence
    if state['phase'] == 'SUSPECT' and not state['retryAtMs'] then
      local lease = state['lease']
      state['retryAtMs'] = lease and tonumber(lease['leaseUntilMs']) or tonumber(state['updatedAtMs'])
    end
  end
  local lease = state['lease']
  if lease and tonumber(lease['leaseUntilMs']) <= now_ms then
    if lease['kind'] == 'confirmation' then
      state['lease'] = nil
      state['retryAtMs'] = now_ms
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

local function is_older_numeric_revision(candidate, current)
  local candidate_number = tonumber(candidate)
  local current_number = tonumber(current)
  return candidate_number and current_number and current_number > candidate_number
end

local function response(status, state, related_states)
  return cjson.encode({ status = status, state = state, relatedStates = related_states or {} })
end

local function replayed(entry)
  if not entry or not input['transitionId'] then return false end
  for _, transition_id in ipairs(entry['replayOrder'] or {}) do
    if transition_id == input['transitionId'] then return true end
  end
  return false
end

local function remember(entry, transition_id)
  local target_transition_id = transition_id or input['transitionId']
  local order = entry['replayOrder'] or {}
  for _, replay_transition_id in ipairs(order) do
    if replay_transition_id == target_transition_id then return end
  end
  table.insert(order, target_transition_id)
  while #order > replay_limit do table.remove(order, 1) end
  entry['replayOrder'] = order
end

local function apply(entry, related_states)
  remember(entry)
  persist(entry)
  return response('applied', entry['state'], related_states)
end

local function hierarchy_transition_id(action, parent_transition_id, parent_incident_id, child_state)
  local digest = redis.sha1hex(
    action .. string.char(0)
    .. parent_transition_id .. string.char(0)
    .. parent_incident_id .. string.char(0)
    .. child_state['scopeKey'] .. string.char(0)
    .. tostring(child_state['generation'])
  )
  return 'hierarchy:' .. action .. ':' .. digest
end

local cleanup_count = cleanup_closed()

local function reserve_capacity()
  if tonumber(redis.call('HLEN', states_key)) < capacity then
    redis.call('DEL', capacity_saturated_key)
    return true
  end
  if cleanup_count >= math.min(capacity, 256) then
    redis.call('SET', capacity_saturated_key, '1')
    return false
  end
  local evict = redis.call('ZRANGE', closed_key, 0, 0)
  if #evict == 0 then
    redis.call('SET', capacity_saturated_key, '1')
    return false
  end
  remove_scope(evict[1])
  if tonumber(redis.call('HLEN', states_key)) >= capacity then
    redis.call('SET', capacity_saturated_key, '1')
    return false
  end
  redis.call('DEL', capacity_saturated_key)
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
  state['confirmationFailuresRequired'] = nil
  state['confirmationFailureCount'] = nil
  state['failureEvidenceKeys'] = nil
end

local function close(entry)
  local state = entry['state']
  local parent_incident_id = state['incidentId']
  local child_scope_keys = state['childScopeKeys'] or {}
  local child_incident_ids = state['childIncidentIds'] or {}
  local related_states = {}
  for index, child_scope_key in ipairs(child_scope_keys) do
    local child_raw = redis.call('HGET', states_key, child_scope_key)
    if child_raw then
      local child_entry = cjson.decode(child_raw)
      local child_state = child_entry['state']
      local current_child_incident_id = child_state['incidentId'] or (child_scope_key .. '@' .. tostring(child_state['generation']))
      if child_state['dispatchRevision'] == state['dispatchRevision']
        and child_incident_ids[index] == current_child_incident_id
        and child_state['shadowedByIncidentId'] == parent_incident_id then
        local relationship_transition_id = hierarchy_transition_id(
          'unshadow', input['transitionId'], parent_incident_id, child_state
        )
        child_state['transitionId'] = relationship_transition_id
        child_state['shadowedByIncidentId'] = nil
        child_state['updatedAtMs'] = now_ms
        remember(child_entry, relationship_transition_id)
        redis.call('HSET', states_key, child_scope_key, cjson.encode(child_entry))
        table.insert(related_states, child_state)
      end
    end
  end
  local is_account_scope = state['scope']['kind'] == 'account'
  if is_account_scope then
    redis.call('HDEL', escalation_key, state['scope']['accountRuntimeKey'])
  end
  state['phase'] = 'CLOSED'
  state['transitionId'] = input['transitionId']
  state['backoffAttempt'] = 0
  state['recoverySuccessCount'] = 0
  state['updatedAtMs'] = now_ms
  clear_optional_state(state)
  if is_account_scope and parent_incident_id and #child_scope_keys > 0 then
    state['childScopeKeys'] = child_scope_keys
    state['childIncidentIds'] = child_incident_ids
  end
  entry['closedExpiresAtMs'] = now_ms + closed_retention_ms
  return apply(entry, related_states)
end

local backoffs = { 3000, 5000, 10000, 30000, 60000, 120000, 300000, 600000, 900000 }
local function passive_delay(base, seed)
  base = math.max(1, math.floor(tonumber(base) or 1))
  local window
  if base < 60000 then
    window = math.floor(base / 2)
  elseif base < 3600000 then
    window = 30000
  elseif base < 86400000 then
    window = 1800000
  elseif base < 604800000 then
    window = 3600000
  else
    window = 28800000
  end
  window = math.min(window, math.floor(base / 2))
  if window <= 0 then return base end
  local digest = redis.sha1hex(tostring(seed or ''))
  local sample = tonumber(string.sub(digest, 1, 8), 16)
  local offset = (sample % (window * 2 + 1)) - window
  if offset == 0 then offset = 1 end
  return math.max(1, base + offset)
end
local function jittered_backoff(base, attempt, generation, suffix)
  if attempt < 5 then return base end
  return passive_delay(base, scope_key .. ':' .. tostring(generation) .. ':' .. tostring(attempt) .. (suffix or ''))
end
local function open(entry)
  local state = entry['state']
  local attempt = tonumber(state['backoffAttempt'] or 0) + 1
  local index = math.min(#backoffs, attempt)
  state['phase'] = 'OPEN'
  state['transitionId'] = input['transitionId']
  state['incidentId'] = state['incidentId'] or input['transitionId']
  state['backoffAttempt'] = attempt
  state['recoverySuccessCount'] = 0
  state['recoveryEvidenceScopeKeys'] = {}
  state['openedAtMs'] = now_ms
  state['retryAtMs'] = now_ms + jittered_backoff(backoffs[index], attempt, tonumber(state['generation'] or 0))
  state['failureReason'] = input['reason'] or state['failureReason']
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['updatedAtMs'] = now_ms
  entry['closedExpiresAtMs'] = nil
  return apply(entry)
end

local function enter_recovering(entry)
  local state = entry['state']
  local backoff_attempt = state['phase'] == 'SUSPECT' and 0 or tonumber(state['backoffAttempt'] or 0)
  state['phase'] = 'RECOVERING'
  state['transitionId'] = input['transitionId']
  state['backoffAttempt'] = backoff_attempt
  state['recoverySuccessCount'] = 0
  state['recoveryEvidenceScopeKeys'] = {}
  state['confirmationFailureCount'] = 0
  state['failureEvidenceKeys'] = {}
  state['failureReason'] = nil
  state['lease'] = nil
  state['halfOpenOrigin'] = nil
  state['retryAtMs'] = now_ms + 3000
  state['updatedAtMs'] = now_ms
  entry['closedExpiresAtMs'] = nil
  return apply(entry)
end

local entry = load_entry()
local operation = input['operation']

if operation == 'get' then
  if entry then return response('applied', entry['state']) end
  if tonumber(redis.call('HLEN', states_key)) < capacity then
    redis.call('DEL', capacity_saturated_key)
    return response('applied', closed_state())
  end
  if redis.call('EXISTS', capacity_saturated_key) == 1 then
    if cleanup_count >= math.min(capacity, 256) then
      return response('capacity_exhausted', input['capacityState'])
    end
    local evict = redis.call('ZRANGE', closed_key, 0, 0)
    if #evict > 0 then
      remove_scope(evict[1])
      if tonumber(redis.call('HLEN', states_key)) < capacity then
        redis.call('DEL', capacity_saturated_key)
        return response('applied', closed_state())
      end
    end
    return response('capacity_exhausted', input['capacityState'])
  end
  return response('applied', closed_state())
end

if operation == 'suspect' then
  if replayed(entry) then return response('idempotent', entry['state']) end
  if entry and entry['state']['dispatchRevision']
    and entry['state']['dispatchRevision'] ~= input['dispatchRevision'] then
    return response('stale_dispatch_revision', entry['state'])
  end
  if entry and entry['state']['phase'] ~= 'CLOSED' then return response('state_mismatch', entry['state']) end
  if not entry and not reserve_capacity() then return response('capacity_exhausted', input['capacityState']) end
  local generation = entry and tonumber(entry['state']['generation']) + 1 or 1
  local state = closed_state()
  state['phase'] = 'SUSPECT'
  state['generation'] = generation
  state['dispatchRevision'] = input['dispatchRevision']
  state['transitionId'] = input['transitionId']
  state['failureReason'] = input['reason']
  state['confirmationFailuresRequired'] = tonumber(input['confirmationFailuresRequired'])
  state['confirmationFailureCount'] = 0
  state['failureEvidenceKeys'] = { input['failureEvidenceKey'] }
  state['incidentId'] = input['transitionId']
  state['retryAtMs'] = now_ms + 3000
  state['updatedAtMs'] = now_ms
  entry = entry or { replayOrder = {} }
  entry['state'] = state
  entry['closedExpiresAtMs'] = nil
  return apply(entry)
end

if operation == 'replace_revision' then
  if replayed(entry) then return response('idempotent', entry['state']) end
  if entry and entry['state']['dispatchRevision'] == input['dispatchRevision'] then
    return response('idempotent', entry['state'])
  end
  if entry and is_older_numeric_revision(input['dispatchRevision'], entry['state']['dispatchRevision']) then
    return response('stale_dispatch_revision', entry['state'])
  end
  if not entry and not reserve_capacity() then return response('capacity_exhausted', input['capacityState']) end
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
  if state['phase'] ~= 'SUSPECT' or state['shadowedByIncidentId'] then return response('state_mismatch', state) end
  local expected_evidence = input['expectedFailureEvidenceKey']
  if expected_evidence then
    local evidence = state['failureEvidenceKeys'] or {}
    if evidence[#evidence] ~= expected_evidence then return response('state_mismatch', state) end
  end
  local confirmation_evidence = input['confirmationEvidenceKey']
  if confirmation_evidence then
    for _, evidence_key in ipairs(state['failureEvidenceKeys'] or {}) do
      if evidence_key == confirmation_evidence then return response('state_mismatch', state) end
    end
  end
  if state['lease'] then return response('state_mismatch', state) end
  if not state['retryAtMs'] or tonumber(state['retryAtMs']) > now_ms then return response('not_due', state) end
  state['transitionId'] = input['transitionId']
  state['lease'] = { kind = 'confirmation', leaseId = input['leaseId'], leaseUntilMs = input['leaseUntilMs'] }
  state['updatedAtMs'] = now_ms
  return apply(entry)
end

if operation == 'close_suspect_observer' or operation == 'close_suspect_key_rotation' then
  if state['phase'] ~= 'SUSPECT' or state['shadowedByIncidentId'] then return response('state_mismatch', state) end
  local evidence = state['failureEvidenceKeys'] or {}
  if evidence[#evidence] ~= input['expectedFailureEvidenceKey'] then return response('state_mismatch', state) end
  if operation == 'close_suspect_observer' then
    for _, evidence_key in ipairs(evidence) do
      if evidence_key == input['observerEvidenceKey'] then return response('state_mismatch', state) end
    end
  end
  return close(entry)
end

if operation == 'complete_confirmation' then
  if state['phase'] ~= 'SUSPECT' then return response('state_mismatch', state) end
  local lease = state['lease']
  if not lease or lease['kind'] ~= 'confirmation' or lease['leaseId'] ~= input['leaseId'] then
    return response('lease_mismatch', state)
  end
  if input['outcome'] == 'framing_complete' then
    if input['framingCompleteDisposition'] == 'closed' then return close(entry) end
    return enter_recovering(entry)
  end
  if input['outcome'] == 'transport_failure' then
    local required = tonumber(state['confirmationFailuresRequired'] or 1)
    if not required or required < 1 or required > 5 then required = 1 end
    local evidence = state['failureEvidenceKeys'] or {}
    local evidence_key = input['failureEvidenceKey']
    local independent = true
    for _, existing_key in ipairs(evidence) do
      if existing_key == evidence_key then independent = false end
    end
    local count = tonumber(state['confirmationFailureCount'] or 0)
    if independent then
      table.insert(evidence, evidence_key)
      while #evidence > required + 1 do table.remove(evidence, 1) end
      count = count + 1
    end
    state['confirmationFailuresRequired'] = required
    state['confirmationFailureCount'] = count
    state['failureEvidenceKeys'] = evidence
    state['transitionId'] = input['transitionId']
    state['backoffAttempt'] = 0
    state['failureReason'] = input['reason'] or state['failureReason']
    state['lease'] = nil
    state['retryAtMs'] = now_ms + 3000
    state['updatedAtMs'] = now_ms
    if count >= required then return open(entry) end
    return apply(entry)
  end
  local attempt = tonumber(state['backoffAttempt'] or 0) + 1
  local index = math.min(#backoffs, attempt)
  state['transitionId'] = input['transitionId']
  state['backoffAttempt'] = attempt
  state['lease'] = nil
  state['retryAtMs'] = now_ms + jittered_backoff(backoffs[index], attempt, tonumber(state['generation'] or 0), ':confirmation-unknown')
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
    local attempt = tonumber(state['backoffAttempt'] or 0) + 1
    local index = math.min(#backoffs, attempt)
    state['phase'] = state['halfOpenOrigin'] or 'OPEN'
    state['transitionId'] = input['transitionId']
    state['backoffAttempt'] = attempt
    state['lease'] = nil
    state['halfOpenOrigin'] = nil
    state['retryAtMs'] = now_ms + jittered_backoff(backoffs[index], attempt, tonumber(state['generation'] or 0), ':unknown')
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
    if required then
      local seen = false
      for _, key in ipairs(recovery_evidence) do
        if key == evidence_scope_key then seen = true end
      end
      if not seen then table.insert(recovery_evidence, evidence_scope_key) end
    end
  end
  if success_count >= 3 then return close(entry) end
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

const redisAccountCircuitEscalationScript = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local evidence_key = KEYS[4]
local capacity_saturated_key = KEYS[5]
local input = cjson.decode(ARGV[1])
local capacity = tonumber(ARGV[2])
local closed_retention_ms = tonumber(ARGV[3])
local replay_limit = tonumber(ARGV[4])
local now_ms = tonumber(input['nowMs'])
local protocol_scope_key = input['scopeKey']
local account_scope_key = input['accountScopeKey']
local runtime_key = input['scope']['accountRuntimeKey']

local function response(status, state, scope_count, failure_count, related_states)
  return cjson.encode({
    status = status,
    accountState = state,
    protocolScopeCount = scope_count,
    confirmedFailureCount = failure_count,
    relatedStates = related_states or {}
  })
end

local function remove_scope(key)
  redis.call('HDEL', states_key, key)
  redis.call('ZREM', due_key, key)
  redis.call('ZREM', closed_key, key)
  if tonumber(redis.call('HLEN', states_key)) < capacity then redis.call('DEL', capacity_saturated_key) end
end

local function reserve_capacity()
  local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, math.min(capacity, 256))
  for _, key in ipairs(expired) do
    local raw = redis.call('HGET', states_key, key)
    if not raw then
      redis.call('ZREM', due_key, key)
      redis.call('ZREM', closed_key, key)
    else
      local entry = cjson.decode(raw)
      local state = entry['state']
      local expires_at = tonumber(entry['closedExpiresAtMs'])
      if state['phase'] == 'CLOSED' and (not expires_at or expires_at <= now_ms) then
        remove_scope(key)
      elseif state['phase'] == 'CLOSED' then
        redis.call('ZADD', closed_key, expires_at, key)
      else
        redis.call('ZREM', closed_key, key)
      end
    end
  end
  if tonumber(redis.call('HLEN', states_key)) < capacity then
    redis.call('DEL', capacity_saturated_key)
    return true
  end
  if #expired >= math.min(capacity, 256) then
    redis.call('SET', capacity_saturated_key, '1')
    return false
  end
  local evict = redis.call('ZRANGE', closed_key, 0, 0)
  if #evict == 0 then
    redis.call('SET', capacity_saturated_key, '1')
    return false
  end
  remove_scope(evict[1])
  if tonumber(redis.call('HLEN', states_key)) >= capacity then
    redis.call('SET', capacity_saturated_key, '1')
    return false
  end
  redis.call('DEL', capacity_saturated_key)
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
if #scopes < tonumber(input['distinctScopeThreshold']) then
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

local function merge_relationships(current_keys, current_ids, additions_keys, additions_ids)
  local result_keys = current_keys or {}
  local result_ids = current_ids or {}
  local changed = false
  for addition_index, key in ipairs(additions_keys) do
    local existing_index = nil
    for index, existing_key in ipairs(result_keys) do
      if existing_key == key then existing_index = index break end
    end
    if not existing_index then
      table.insert(result_keys, key)
      table.insert(result_ids, additions_ids[addition_index])
      changed = true
    elseif result_ids[existing_index] ~= additions_ids[addition_index] then
      result_ids[existing_index] = additions_ids[addition_index]
      changed = true
    end
  end
  return result_keys, result_ids, changed
end

local function remember(entry, transition_id)
  local order = entry['replayOrder'] or {}
  for _, replay_transition_id in ipairs(order) do
    if replay_transition_id == transition_id then return end
  end
  table.insert(order, transition_id)
  while #order > replay_limit do table.remove(order, 1) end
  entry['replayOrder'] = order
end

local function hierarchy_transition_id(action, parent_transition_id, parent_incident_id, child_state)
  local digest = redis.sha1hex(
    action .. string.char(0)
    .. parent_transition_id .. string.char(0)
    .. parent_incident_id .. string.char(0)
    .. child_state['scopeKey'] .. string.char(0)
    .. tostring(child_state['generation'])
  )
  return 'hierarchy:' .. action .. ':' .. digest
end

local function shadow_children(parent_incident_id, scope_keys, incident_ids, parent_transition_id)
  local related_states = {}
  for index, key in ipairs(scope_keys) do
    local raw = redis.call('HGET', states_key, key)
    if raw then
      local entry = cjson.decode(raw)
      local child_state = entry['state']
      local current_child_incident_id = child_state['incidentId'] or (key .. '@' .. tostring(child_state['generation']))
      if child_state['phase'] ~= 'CLOSED'
        and child_state['dispatchRevision'] == input['dispatchRevision']
        and incident_ids[index] == current_child_incident_id
        and child_state['shadowedByIncidentId'] == nil then
        local relationship_transition_id = hierarchy_transition_id(
          'shadow', parent_transition_id, parent_incident_id, child_state
        )
        child_state['transitionId'] = relationship_transition_id
        child_state['shadowedByIncidentId'] = parent_incident_id
        child_state['updatedAtMs'] = now_ms
        remember(entry, relationship_transition_id)
        redis.call('HSET', states_key, key, cjson.encode(entry))
        table.insert(related_states, child_state)
      end
    end
  end
  return related_states
end

if account_entry and account_state['phase'] ~= 'CLOSED' then
  if account_state['dispatchRevision'] ~= input['dispatchRevision'] then
    return response('stale_dispatch_revision', account_state, #scopes, failure_total)
  end
  local incident_id = account_state['incidentId'] or account_state['transitionId']
  local relationship_changed = account_state['incidentId'] ~= incident_id
  account_state['incidentId'] = incident_id
  local merged_scope_keys, merged_incident_ids, merged_changed = merge_relationships(
    account_state['childScopeKeys'], account_state['childIncidentIds'], child_scope_keys, child_incident_ids
  )
  account_state['childScopeKeys'] = merged_scope_keys
  account_state['childIncidentIds'] = merged_incident_ids
  account_state['requiredRecoveryScopeKeys'] = merge_unique(account_state['requiredRecoveryScopeKeys'], merged_scope_keys)
  if relationship_changed or merged_changed then
    account_state['transitionId'] = input['accountTransitionId']
    account_state['updatedAtMs'] = now_ms
    remember(account_entry, input['accountTransitionId'])
  end
  account_entry['state'] = account_state
  redis.call('HSET', states_key, account_scope_key, cjson.encode(account_entry))
  local related_states = shadow_children(
    incident_id, child_scope_keys, child_incident_ids, input['accountTransitionId']
  )
  return response('already_active', account_state, #scopes, failure_total, related_states)
end

if not account_entry and not reserve_capacity() then
  return response('capacity_exhausted', input['capacityAccountState'], #scopes, failure_total)
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
-- miniredis-compat adaptation: the Node script assigns the shared
-- child_scope_keys table to two fields, which real Redis cjson encodes fine
-- but miniredis's gopher-json rejects as a nested reference. The copy is
-- semantically identical.
local required_recovery_scope_keys = {}
for _, required_key in ipairs(child_scope_keys) do table.insert(required_recovery_scope_keys, required_key) end
account_state['requiredRecoveryScopeKeys'] = required_recovery_scope_keys
account_state['recoveryEvidenceScopeKeys'] = {}
account_state['updatedAtMs'] = now_ms
account_entry = { state = account_state, replayOrder = { incident_id } }
redis.call('HSET', states_key, account_scope_key, cjson.encode(account_entry))
redis.call('ZADD', due_key, account_state['retryAtMs'], account_scope_key)
redis.call('ZREM', closed_key, account_scope_key)
local related_states = shadow_children(incident_id, child_scope_keys, child_incident_ids, incident_id)
return response('escalated', account_state, #scopes, failure_total, related_states)
`

const redisAccountCircuitClearEscalationScript = `
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

const redisAccountCircuitListDueScript = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local now = tonumber(ARGV[1])
local result_limit = tonumber(ARGV[2])
local scan_limit = tonumber(ARGV[3])
local retained_offset = tonumber(ARGV[4])
if now == nil or result_limit == nil or result_limit < 1 or scan_limit == nil or scan_limit < 1
  or retained_offset == nil or retained_offset < 0 then
  return redis.error_reply('invalid list-due arguments')
end

local result = {}
local scanned = 0
local exhausted = false
local batch_size = math.min(scan_limit, 128)
while #result < result_limit and scanned < scan_limit and not exhausted do
  local take = math.min(batch_size, scan_limit - scanned)
  local scope_keys = redis.call('ZRANGEBYSCORE', due_key, '-inf', now, 'LIMIT', retained_offset, take)
  if #scope_keys == 0 then
    exhausted = true
    break
  end
  local retained_in_batch = 0
  for _, scope_key in ipairs(scope_keys) do
    scanned = scanned + 1
    local encoded = redis.call('HGET', states_key, scope_key)
    if not encoded then
      redis.call('ZREM', due_key, scope_key)
    else
      local ok, entry = pcall(cjson.decode, encoded)
      if not ok or type(entry) ~= 'table' or type(entry['state']) ~= 'table' then
        return redis.error_reply('invalid account circuit state')
      end
      local state = entry['state']
      local phase = state['phase']
      local due_at = nil
      local lease = state['lease']
      if type(lease) == 'table' then
        due_at = tonumber(lease['leaseUntilMs'])
        if due_at == nil then return redis.error_reply('account circuit lease missing leaseUntilMs') end
      elseif phase == 'SUSPECT' or phase == 'OPEN' or phase == 'RECOVERING' then
        due_at = tonumber(state['retryAtMs'])
        if due_at == nil then return redis.error_reply('active account circuit state missing retryAtMs') end
      elseif phase == 'HALF_OPEN' then
        return redis.error_reply('half-open account circuit state missing lease')
      elseif phase ~= 'CLOSED' then
        return redis.error_reply('invalid account circuit phase')
      end

      if due_at == nil then
        redis.call('ZREM', due_key, scope_key)
      elseif due_at > now then
        redis.call('ZADD', due_key, due_at, scope_key)
      else
        table.insert(result, scope_key)
        retained_in_batch = retained_in_batch + 1
        if #result >= result_limit then break end
      end
    end
  end
  retained_offset = retained_offset + retained_in_batch
  if #scope_keys < take then exhausted = true end
end
return cjson.encode({
  scopeKeys = result,
  scanned = scanned,
  nextOffset = retained_offset,
  exhausted = exhausted
})
`

const redisAccountCircuitSizeScript = `
local now_ms = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local cleanup_limit = tonumber(ARGV[3])
if not cleanup_limit or cleanup_limit < 1 or cleanup_limit > 256 then
  return redis.error_reply('invalid account circuit cleanup limit')
end
local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', now_ms, 'LIMIT', 0, cleanup_limit)
for _, scope_key in ipairs(expired) do
  local raw = redis.call('HGET', KEYS[1], scope_key)
  if not raw then
    redis.call('ZREM', KEYS[2], scope_key)
    redis.call('ZREM', KEYS[3], scope_key)
  else
    local entry = cjson.decode(raw)
    local state = entry['state']
    local expires_at = tonumber(entry['closedExpiresAtMs'])
    if state['phase'] == 'CLOSED' and (not expires_at or expires_at <= now_ms) then
      redis.call('HDEL', KEYS[1], scope_key)
      redis.call('ZREM', KEYS[2], scope_key)
      redis.call('ZREM', KEYS[3], scope_key)
    elseif state['phase'] == 'CLOSED' then
      redis.call('ZADD', KEYS[3], expires_at, scope_key)
    else
      redis.call('ZREM', KEYS[3], scope_key)
    end
  end
end
local size = redis.call('HLEN', KEYS[1])
if tonumber(size) < capacity then redis.call('DEL', KEYS[4]) end
return cjson.encode({ size = tonumber(size), processed = #expired })
`

const redisAccountCircuitRestoreScript = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local capacity_saturated_key = KEYS[4]
local state = cjson.decode(ARGV[1])
local now_ms = tonumber(ARGV[2])
local closed_retention_ms = tonumber(ARGV[3])
local capacity = tonumber(ARGV[4])
local capacity_state = cjson.decode(ARGV[5])
local replay_limit = tonumber(ARGV[6])
local scope_key = state['scopeKey']
local function remember(entry, transition_id)
  local order = entry['replayOrder'] or {}
  for _, replay_transition_id in ipairs(order) do
    if replay_transition_id == transition_id then return end
  end
  table.insert(order, transition_id)
  while #order > replay_limit do table.remove(order, 1) end
  entry['replayOrder'] = order
end
local function hierarchy_transition_id(action, parent_state, child_state)
  local digest = redis.sha1hex(
    action .. string.char(0)
    .. parent_state['transitionId'] .. string.char(0)
    .. parent_state['incidentId'] .. string.char(0)
    .. child_state['scopeKey'] .. string.char(0)
    .. tostring(child_state['generation'])
  )
  return 'hierarchy:' .. action .. ':' .. digest
end
local function project_parent_relationship(parent_state)
  local related_states = {}
  if parent_state['scope']['kind'] ~= 'account' or not parent_state['incidentId'] then return related_states end
  local child_incident_ids = parent_state['childIncidentIds'] or {}
  for index, child_scope_key in ipairs(parent_state['childScopeKeys'] or {}) do
    local child_raw = redis.call('HGET', states_key, child_scope_key)
    if child_raw then
      local child_entry = cjson.decode(child_raw)
      local child_state = child_entry['state']
      local current_child_incident_id = child_state['incidentId']
        or (child_scope_key .. '@' .. tostring(child_state['generation']))
      if child_state['dispatchRevision'] == parent_state['dispatchRevision']
        and child_incident_ids[index] == current_child_incident_id
        and tonumber(child_state['updatedAtMs'] or 0) <= tonumber(parent_state['updatedAtMs'] or 0) then
        if parent_state['phase'] == 'CLOSED' then
          if child_state['shadowedByIncidentId'] == parent_state['incidentId'] then
            local relationship_transition_id = hierarchy_transition_id('unshadow', parent_state, child_state)
            child_state['transitionId'] = relationship_transition_id
            child_state['shadowedByIncidentId'] = nil
            child_state['updatedAtMs'] = parent_state['updatedAtMs']
            remember(child_entry, relationship_transition_id)
            redis.call('HSET', states_key, child_scope_key, cjson.encode(child_entry))
            table.insert(related_states, child_state)
          end
        elseif child_state['phase'] ~= 'CLOSED' and child_state['shadowedByIncidentId'] == nil then
          local relationship_transition_id = hierarchy_transition_id('shadow', parent_state, child_state)
          child_state['transitionId'] = relationship_transition_id
          child_state['shadowedByIncidentId'] = parent_state['incidentId']
          child_state['updatedAtMs'] = parent_state['updatedAtMs']
          remember(child_entry, relationship_transition_id)
          redis.call('HSET', states_key, child_scope_key, cjson.encode(child_entry))
          table.insert(related_states, child_state)
        end
      end
    end
  end
  return related_states
end
local existing_raw = redis.call('HGET', states_key, scope_key)
if existing_raw then
  local existing = cjson.decode(existing_raw)['state']
  local existing_revision = tonumber(existing['dispatchRevision'])
  local incoming_revision = tonumber(state['dispatchRevision'])
  if existing_revision and incoming_revision and existing_revision > incoming_revision then
    return cjson.encode({ status = 'stale_dispatch_revision', state = existing })
  end
  local existing_generation = tonumber(existing['generation'] or 0)
  local incoming_generation = tonumber(state['generation'] or 0)
  if existing_generation > incoming_generation
    or (existing_generation == incoming_generation and tonumber(existing['updatedAtMs'] or 0) >= tonumber(state['updatedAtMs'] or 0)) then
    local related_states = project_parent_relationship(existing)
    return cjson.encode({ status = 'idempotent', state = existing, relatedStates = related_states })
  end
end
if not existing_raw then
  local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, math.min(capacity, 256))
  for _, expired_scope_key in ipairs(expired) do
    local raw = redis.call('HGET', states_key, expired_scope_key)
    if not raw then
      redis.call('ZREM', due_key, expired_scope_key)
      redis.call('ZREM', closed_key, expired_scope_key)
    else
      local entry = cjson.decode(raw)
      local existing_state = entry['state']
      local expires_at = tonumber(entry['closedExpiresAtMs'])
      if existing_state['phase'] == 'CLOSED' and (not expires_at or expires_at <= now_ms) then
        redis.call('HDEL', states_key, expired_scope_key)
        redis.call('ZREM', due_key, expired_scope_key)
        redis.call('ZREM', closed_key, expired_scope_key)
      elseif existing_state['phase'] == 'CLOSED' then
        redis.call('ZADD', closed_key, expires_at, expired_scope_key)
      else
        redis.call('ZREM', closed_key, expired_scope_key)
      end
    end
  end
  if tonumber(redis.call('HLEN', states_key)) >= capacity then
    if #expired >= math.min(capacity, 256) then
      redis.call('SET', capacity_saturated_key, '1')
      return cjson.encode({ status = 'capacity_exhausted', state = capacity_state })
    end
    local evict = redis.call('ZRANGE', closed_key, 0, 0)
    if #evict == 0 then
      redis.call('SET', capacity_saturated_key, '1')
      return cjson.encode({ status = 'capacity_exhausted', state = capacity_state })
    end
    redis.call('HDEL', states_key, evict[1])
    redis.call('ZREM', due_key, evict[1])
    redis.call('ZREM', closed_key, evict[1])
    if tonumber(redis.call('HLEN', states_key)) >= capacity then
      redis.call('SET', capacity_saturated_key, '1')
      return cjson.encode({ status = 'capacity_exhausted', state = capacity_state })
    end
  end
  redis.call('DEL', capacity_saturated_key)
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
elseif state['phase'] == 'SUSPECT' or state['phase'] == 'OPEN' or state['phase'] == 'RECOVERING' then
  redis.call('ZADD', due_key, tonumber(state['retryAtMs'] or state['updatedAtMs']), scope_key)
end
local related_states = project_parent_relationship(state)
return cjson.encode({ status = 'applied', state = state, relatedStates = related_states })
`

const redisAccountCircuitAccountRevisionScript = `
local states_key = KEYS[1]
local due_key = KEYS[2]
local closed_key = KEYS[3]
local escalation_key = KEYS[4]
local runtime_key = ARGV[1]
local dispatch_revision = ARGV[2]
local transition_id = ARGV[3]
local now_ms = tonumber(ARGV[4])
local retention_ms = tonumber(ARGV[5])
local states_cursor = ARGV[6] or '0'
local evidence_cursor = ARGV[7] or '0'
local states_scan = { 'done', {} }
if states_cursor ~= 'done' then
  states_scan = redis.call('HSCAN', states_key, states_cursor, 'COUNT', 128)
  if states_scan[1] == '0' then states_scan[1] = 'done' end
end
local values = states_scan[2]
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
    state['confirmationFailuresRequired'] = nil
    state['confirmationFailureCount'] = nil
    state['failureEvidenceKeys'] = nil
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
local evidence_scan = { 'done', {} }
if evidence_cursor ~= 'done' then
  evidence_scan = redis.call('HSCAN', escalation_key, evidence_cursor, 'MATCH', runtime_key .. '*', 'COUNT', 128)
  if evidence_scan[1] == '0' then evidence_scan[1] = 'done' end
end
local evidence_values = evidence_scan[2]
for index = 1, #evidence_values, 2 do
  local evidence_runtime_key = evidence_values[index]
  local evidence = cjson.decode(evidence_values[index + 1])
  if matches_runtime_key(evidence_runtime_key)
    and evidence['dispatchRevision'] ~= dispatch_revision
    and not is_older_revision(evidence['dispatchRevision']) then
    redis.call('HDEL', escalation_key, evidence_runtime_key)
  end
end
return cjson.encode({
  statesCursor = states_scan[1],
  evidenceCursor = evidence_scan[1],
  changed = changed
})
`
