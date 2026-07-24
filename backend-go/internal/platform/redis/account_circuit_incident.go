package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"juhe-ai/backend-go/internal/store/port"
)

const defaultAccountCircuitRuntimeCapacity = 10000

const restoreAccountCircuitIncidentLua = `
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
local state = cjson.decode(ARGV[1])
local account_id = ARGV[2]
local runtime_key = ARGV[3]
local scope_key = ARGV[4]
local incoming_revision = tonumber(ARGV[5])
local incoming_generation = tonumber(ARGV[6])
local incoming_updated_at = tonumber(ARGV[7])
local now_ms = tonumber(ARGV[8])
local retention_ms = tonumber(ARGV[9])
local capacity = tonumber(ARGV[10])
local retained_until_ms = tonumber(ARGV[11])
local incoming_ledger_revision = tonumber(ARGV[12])

local function require_type(key, expected)
  local actual = redis.call('TYPE', key)['ok']
  if actual ~= 'none' and actual ~= expected then return false end
  return true
end
if not require_type(states_key, 'hash') or not require_type(due_key, 'zset')
  or not require_type(closed_key, 'zset') or not require_type(escalation_key, 'hash')
  or not require_type(revisions_key, 'hash') or not require_type(scope_runtime_key, 'hash')
  or not require_type(runtime_scopes_key, 'hash') or not require_type(account_runtimes_key, 'hash')
  or not require_type(runtime_accounts_key, 'hash') or not require_type(index_meta_key, 'hash')
  or not require_type(ledger_revisions_key, 'hash') then
  return redis.error_reply('invalid account circuit Redis key type')
end
local index_status = redis.call('HGET', index_meta_key, 'status')
if index_status and index_status ~= 'building' then
  return redis.error_reply('account circuit runtime index is not available for legacy restore')
end

local function decode_array(raw)
  if not raw then return {} end
  local values = cjson.decode(raw)
  if type(values) ~= 'table' then return nil end
  local count = 0
  for key, value in pairs(values) do
    if type(key) ~= 'number' or key < 1 or key ~= math.floor(key) or type(value) ~= 'string' then return nil end
    count = count + 1
  end
  if count ~= #values then return nil end
  return values
end

local function add_sorted(values, target)
  for _, value in ipairs(values) do if value == target then return values end end
  table.insert(values, target)
  table.sort(values)
  return values
end

local function remove_value(values, target)
  local result = {}
  for _, value in ipairs(values) do if value ~= target then table.insert(result, value) end end
  return result
end

local function persist_array(hash_key, field, values)
  if #values == 0 then redis.call('HDEL', hash_key, field)
  else redis.call('HSET', hash_key, field, cjson.encode(values)) end
end

local function register_scope()
  local old_runtime = redis.call('HGET', scope_runtime_key, scope_key)
  if old_runtime and old_runtime ~= runtime_key then return false end
  local old_account = redis.call('HGET', runtime_accounts_key, runtime_key)
  if old_account and old_account ~= account_id then return false end
  local scopes = decode_array(redis.call('HGET', runtime_scopes_key, runtime_key))
  local runtimes = decode_array(redis.call('HGET', account_runtimes_key, account_id))
  if not scopes or not runtimes then return false end
  redis.call('HSET', scope_runtime_key, scope_key, runtime_key)
  redis.call('HSET', runtime_accounts_key, runtime_key, account_id)
  persist_array(runtime_scopes_key, runtime_key, add_sorted(scopes, scope_key))
  persist_array(account_runtimes_key, account_id, add_sorted(runtimes, runtime_key))
  return true
end

local function validate_scope_index()
  local old_runtime = redis.call('HGET', scope_runtime_key, scope_key)
  if old_runtime and old_runtime ~= runtime_key then return false end
  local old_account = redis.call('HGET', runtime_accounts_key, runtime_key)
  if old_account and old_account ~= account_id then return false end
  if not decode_array(redis.call('HGET', runtime_scopes_key, runtime_key)) then return false end
  if not decode_array(redis.call('HGET', account_runtimes_key, account_id)) then return false end
  return true
end

local function validate_remove_scope(target_scope)
  local target_runtime = redis.call('HGET', scope_runtime_key, target_scope)
  if not target_runtime then return true end
  local scopes = decode_array(redis.call('HGET', runtime_scopes_key, target_runtime))
  if not scopes then return false end
  local target_account = redis.call('HGET', runtime_accounts_key, target_runtime)
  if target_account then
    local runtimes = decode_array(redis.call('HGET', account_runtimes_key, target_account))
    if not runtimes then return false end
  end
  return true
end

local function remove_scope(target_scope)
  local target_runtime = redis.call('HGET', scope_runtime_key, target_scope)
  local target_account = target_runtime and redis.call('HGET', runtime_accounts_key, target_runtime) or nil
  local scopes = target_runtime and decode_array(redis.call('HGET', runtime_scopes_key, target_runtime)) or {}
  if not scopes then return false end
  local remaining_scopes = remove_value(scopes, target_scope)
  local runtimes = target_account and decode_array(redis.call('HGET', account_runtimes_key, target_account)) or {}
  if not runtimes then return false end
  redis.call('HDEL', states_key, target_scope)
  redis.call('ZREM', due_key, target_scope)
  redis.call('ZREM', closed_key, target_scope)
  redis.call('HDEL', scope_runtime_key, target_scope)
  if target_runtime then
    persist_array(runtime_scopes_key, target_runtime, remaining_scopes)
    if #remaining_scopes == 0 and not redis.call('HGET', escalation_key, target_runtime) then
      redis.call('HDEL', runtime_accounts_key, target_runtime)
      if target_account then persist_array(account_runtimes_key, target_account, remove_value(runtimes, target_runtime)) end
    end
  end
  return true
end

if not incoming_revision or incoming_revision < 1 or incoming_revision ~= math.floor(incoming_revision)
  or not incoming_generation or incoming_generation < 0 or incoming_generation ~= math.floor(incoming_generation)
  or not incoming_ledger_revision or incoming_ledger_revision < 1 or incoming_ledger_revision ~= math.floor(incoming_ledger_revision)
  or not incoming_updated_at or not now_ms or not capacity or capacity < 1 then
  return redis.error_reply('invalid incident restore input')
end

local tombstone_raw = redis.call('HGET', revisions_key, account_id)
local tombstone = tombstone_raw and tonumber(tombstone_raw) or nil
if tombstone_raw and not tombstone then return redis.error_reply('invalid revision tombstone') end
if tombstone and tombstone > incoming_revision then
  return cjson.encode({ status = 'stale', currentRevision = tombstone, closedStates = 0 })
end

local existing_raw = redis.call('HGET', states_key, scope_key)
local existing_entry = existing_raw and cjson.decode(existing_raw) or nil
if not validate_scope_index() then return redis.error_reply('invalid incident runtime index') end
local projected_ledger_raw = redis.call('HGET', ledger_revisions_key, scope_key)
local projected_dispatch_revision = nil
local projected_ledger_revision = nil
local projected_transition_id = nil
if projected_ledger_raw then
  local decoded_ledger = cjson.decode(projected_ledger_raw)
  if type(decoded_ledger) == 'table' then
    projected_dispatch_revision = tonumber(decoded_ledger['dispatchRevision'])
    projected_ledger_revision = tonumber(decoded_ledger['ledgerRevision'])
    projected_transition_id = decoded_ledger['transitionId']
  else
    projected_dispatch_revision = 0
    projected_ledger_revision = tonumber(decoded_ledger)
  end
  if not projected_dispatch_revision or not projected_ledger_revision then return redis.error_reply('invalid incident ledger tombstone') end
end
if projected_dispatch_revision and projected_dispatch_revision > incoming_revision then
  return cjson.encode({ status = 'stale', currentRevision = projected_dispatch_revision, closedStates = 0 })
end
if projected_dispatch_revision == incoming_revision and projected_ledger_revision > incoming_ledger_revision then
  local existing_matches_incarnation = not existing_entry
  if existing_entry then
    local existing_incarnation = existing_entry['state']
    local existing_incarnation_ledger = tonumber(existing_incarnation['ledgerRevision'] or 0)
    existing_matches_incarnation = tonumber(existing_incarnation['dispatchRevision']) == incoming_revision
      and existing_incarnation['transitionId'] == state['transitionId']
      and tonumber(existing_incarnation['generation']) == incoming_generation
      and existing_incarnation_ledger and existing_incarnation_ledger <= 1
  end
  local new_incarnation = incoming_ledger_revision == 1
    and projected_transition_id ~= state['transitionId']
    and existing_matches_incarnation
  if not new_incarnation then
    return cjson.encode({ status = 'ledger_conflict', currentRevision = incoming_revision, closedStates = 0 })
  end
end
if existing_entry then
  local existing = existing_entry['state']
  local existing_revision = tonumber(existing['dispatchRevision'])
  local existing_generation = tonumber(existing['generation'])
  local existing_updated_at = tonumber(existing['updatedAtMs'])
  local existing_ledger_revision = tonumber(existing['ledgerRevision'] or 0)
  if not existing_revision or not existing_generation or not existing_updated_at or not existing_ledger_revision then
    return redis.error_reply('invalid existing incident state')
  end
  if existing_revision > incoming_revision then
    return cjson.encode({ status = 'stale', currentRevision = existing_revision, closedStates = 0 })
  end
  if existing_revision == incoming_revision and existing_generation > incoming_generation then
    return cjson.encode({ status = 'ledger_conflict', currentRevision = incoming_revision, closedStates = 0 })
  end
  if existing_revision == incoming_revision and existing_ledger_revision > incoming_ledger_revision then
    return cjson.encode({ status = 'ledger_conflict', currentRevision = incoming_revision, closedStates = 0 })
  end
  if existing_revision == incoming_revision and existing_ledger_revision == incoming_ledger_revision and (
    existing_generation > incoming_generation
    or (existing_generation == incoming_generation and existing_updated_at >= incoming_updated_at)
  ) then
	if existing_generation > incoming_generation then
	  return cjson.encode({ status = 'ledger_conflict', currentRevision = incoming_revision, closedStates = 0 })
	end
    local existing_state = existing_entry['state']
    local existing_phase = existing_state['phase']
    local existing_due = nil
    local existing_expires_at = nil
    if existing_phase == 'CLOSED' then
      existing_expires_at = tonumber(existing_entry['closedExpiresAtMs'])
      if not existing_expires_at then return redis.error_reply('invalid existing closed deadline') end
    elseif existing_state['lease'] then
      existing_due = tonumber(existing_state['lease']['leaseUntilMs'])
      if not existing_due then return redis.error_reply('invalid existing lease deadline') end
    elseif existing_phase == 'OPEN' or existing_phase == 'RECOVERING' then
      existing_due = tonumber(existing_state['retryAtMs'] or existing_state['updatedAtMs'])
      if not existing_due then return redis.error_reply('invalid existing retry deadline') end
    else
      existing_due = tonumber(existing_state['updatedAtMs'])
      if not existing_due then return redis.error_reply('invalid existing update deadline') end
    end
    if not register_scope() then return redis.error_reply('invalid incident runtime index') end
    if existing_phase == 'CLOSED' then
      if existing_expires_at <= now_ms then
        if not validate_remove_scope(scope_key) or not remove_scope(scope_key) then return redis.error_reply('invalid incident runtime index') end
      else
        redis.call('ZREM', due_key, scope_key)
        redis.call('ZADD', closed_key, existing_expires_at, scope_key)
      end
    else
      redis.call('ZADD', due_key, existing_due, scope_key)
      redis.call('ZREM', closed_key, scope_key)
    end
    redis.call('HSETNX', index_meta_key, 'version', '1')
    redis.call('HSETNX', index_meta_key, 'status', 'building')
    redis.call('HSET', ledger_revisions_key, scope_key, cjson.encode({ dispatchRevision = incoming_revision, ledgerRevision = incoming_ledger_revision, transitionId = state['transitionId'] }))
    return cjson.encode({ status = 'idempotent', currentRevision = incoming_revision, closedStates = 0 })
  end
end

local expired = redis.call('ZRANGEBYSCORE', closed_key, '-inf', now_ms, 'LIMIT', 0, capacity)
for _, expired_scope in ipairs(expired) do
  if not validate_remove_scope(expired_scope) then return redis.error_reply('invalid incident runtime index') end
end
for _, expired_scope in ipairs(expired) do
  if not remove_scope(expired_scope) then return redis.error_reply('invalid incident runtime index') end
end
if state['phase'] == 'CLOSED' and retained_until_ms > 0 and retained_until_ms <= now_ms then
  if not validate_remove_scope(scope_key) or not remove_scope(scope_key) then return redis.error_reply('invalid incident runtime index') end
  redis.call('HSETNX', index_meta_key, 'version', '1')
  redis.call('HSETNX', index_meta_key, 'status', 'building')
  redis.call('HSET', ledger_revisions_key, scope_key, cjson.encode({ dispatchRevision = incoming_revision, ledgerRevision = incoming_ledger_revision, transitionId = state['transitionId'] }))
  return cjson.encode({ status = 'applied', currentRevision = incoming_revision, closedStates = 0 })
end
if not existing_entry and tonumber(redis.call('HLEN', states_key)) >= capacity then
  local evict = redis.call('ZRANGE', closed_key, 0, 0)
  if #evict == 0 then
    return cjson.encode({ status = 'capacity_exhausted', currentRevision = incoming_revision, closedStates = 0 })
  end
  if not validate_remove_scope(evict[1]) or not remove_scope(evict[1]) then return redis.error_reply('invalid incident runtime index') end
end

local entry = { state = state, replayIds = { state['transitionId'] }, replayOrder = { state['transitionId'] } }
redis.call('HSET', states_key, scope_key, cjson.encode(entry))
redis.call('ZREM', due_key, scope_key)
redis.call('ZREM', closed_key, scope_key)
if state['phase'] == 'CLOSED' then
  local expires_at = retained_until_ms > 0 and retained_until_ms or (now_ms + retention_ms)
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
if not register_scope() then return redis.error_reply('invalid incident runtime index') end
redis.call('HSETNX', index_meta_key, 'version', '1')
redis.call('HSETNX', index_meta_key, 'status', 'building')
redis.call('HSET', ledger_revisions_key, scope_key, cjson.encode({ dispatchRevision = incoming_revision, ledgerRevision = incoming_ledger_revision, transitionId = state['transitionId'] }))
return cjson.encode({ status = 'applied', currentRevision = incoming_revision, closedStates = 0 })
`

var restoreAccountCircuitIncidentScript = goredis.NewScript(restoreAccountCircuitIncidentLua)

type accountCircuitIncidentRuntimeState struct {
	ScopeKey             string                              `json:"scopeKey"`
	Scope                accountCircuitIncidentRuntimeScope  `json:"scope"`
	Phase                string                              `json:"phase"`
	Generation           int                                 `json:"generation"`
	DispatchRevision     string                              `json:"dispatchRevision"`
	LedgerRevision       string                              `json:"ledgerRevision"`
	TransitionID         string                              `json:"transitionId"`
	IncidentID           string                              `json:"incidentId,omitempty"`
	BackoffAttempt       int                                 `json:"backoffAttempt"`
	RecoverySuccessCount int                                 `json:"recoverySuccessCount"`
	OpenedAtMS           *int64                              `json:"openedAtMs,omitempty"`
	RetryAtMS            *int64                              `json:"retryAtMs,omitempty"`
	Lease                *accountCircuitIncidentRuntimeLease `json:"lease,omitempty"`
	HalfOpenOrigin       string                              `json:"halfOpenOrigin,omitempty"`
	UpdatedAtMS          int64                               `json:"updatedAtMs"`
}

type accountCircuitIncidentRuntimeScope struct {
	Kind              string `json:"kind"`
	AccountRuntimeKey string `json:"accountRuntimeKey"`
	KeyFingerprint    string `json:"keyFingerprint,omitempty"`
	ProtocolProfile   string `json:"protocolProfile,omitempty"`
	RequestLane       string `json:"requestLane,omitempty"`
	ModelBucket       string `json:"modelBucket,omitempty"`
}

type accountCircuitIncidentRuntimeLease struct {
	Kind         string `json:"kind"`
	LeaseID      string `json:"leaseId"`
	LeaseUntilMS int64  `json:"leaseUntilMs"`
}

type AccountCircuitIncidentRestorer struct {
	client    *Client
	keys      accountCircuitRevisionKeys
	runtime   *AccountCircuitRuntimeStore
	retention time.Duration
	capacity  int
	now       func() time.Time
	restore   func(context.Context, accountCircuitRevisionKeys, port.GatewayAccountCircuitIncident, []byte, time.Time, time.Duration, int) ([]byte, error)
}

func NewAccountCircuitIncidentRestorer(client *Client, retention time.Duration, capacity int) (*AccountCircuitIncidentRestorer, error) {
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
		capacity = defaultAccountCircuitRuntimeCapacity
	}
	if capacity < 1 || capacity > 1000000 {
		return nil, fmt.Errorf("account circuit runtime capacity is invalid")
	}
	keys, err := accountCircuitRevisionRedisKeys(client.namespace, AccountCircuitRedisStoreName)
	if err != nil {
		return nil, err
	}
	restorer := &AccountCircuitIncidentRestorer{client: client, keys: keys, retention: retention, capacity: capacity, now: time.Now}
	restorer.restore = restorer.runRestore
	return restorer, nil
}

// NewAccountCircuitRuntimeOwnerIncidentRestorer restores durable incidents
// through the ready-index runtime owner. The legacy constructor remains only
// for the building-index compatibility projector and refuses ready metadata.
func NewAccountCircuitRuntimeOwnerIncidentRestorer(client *Client, retention time.Duration, capacity int) (*AccountCircuitIncidentRestorer, error) {
	runtime, err := NewAccountCircuitRuntimeStore(client, retention, capacity)
	if err != nil {
		return nil, err
	}
	return &AccountCircuitIncidentRestorer{client: client, keys: runtime.keys, runtime: runtime, retention: runtime.retention, capacity: runtime.capacity, now: runtime.now}, nil
}

func (r *AccountCircuitIncidentRestorer) WithNow(now func() time.Time) *AccountCircuitIncidentRestorer {
	if now != nil {
		r.now = now
	}
	return r
}

func (r *AccountCircuitIncidentRestorer) RestoreGatewayAccountCircuitIncident(ctx context.Context, incident port.GatewayAccountCircuitIncident) (port.GatewayAccountCircuitRevisionProjection, error) {
	if r == nil || r.restore == nil {
		if r == nil || r.runtime == nil {
			return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit incident restorer is required")
		}
		return r.restoreWithRuntimeOwner(ctx, incident)
	}
	state, err := accountCircuitIncidentRuntimeStateFromIncident(incident)
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	rawState, err := json.Marshal(state)
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("encode account circuit incident runtime state: %w", err)
	}
	raw, err := r.restore(ctx, r.keys, incident, rawState, r.now().UTC(), r.retention, r.capacity)
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("restore account circuit incident: %w", err)
	}
	var result port.GatewayAccountCircuitRevisionProjection
	if err := json.Unmarshal(raw, &result); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("decode account circuit incident restore: %w", err)
	}
	if result.Status == "capacity_exhausted" {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit runtime capacity exhausted")
	}
	if result.Status == "ledger_conflict" {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit runtime generation is ahead of durable ledger")
	}
	if err := validateAccountCircuitRevisionProjection(result); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	return result, nil
}

func (r *AccountCircuitIncidentRestorer) restoreWithRuntimeOwner(ctx context.Context, incident port.GatewayAccountCircuitIncident) (port.GatewayAccountCircuitRevisionProjection, error) {
	if r.runtime == nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit runtime owner restorer is required")
	}
	compatibilityState, err := accountCircuitIncidentRuntimeStateFromIncident(incident)
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	raw, err := json.Marshal(compatibilityState)
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("encode account circuit runtime owner incident: %w", err)
	}
	var wire accountCircuitRuntimeStateWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("decode account circuit runtime owner incident: %w", err)
	}
	state, err := runtimeStateFromWire(wire)
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	result, err := r.runtime.RestoreGatewayAccountCircuit(ctx, port.GatewayAccountCircuitRestoreInput{
		AccountID: incident.AccountID, State: state, RetainedUntil: incident.RetainedUntil, Now: r.now().UTC(),
	})
	if err != nil {
		return port.GatewayAccountCircuitRevisionProjection{}, err
	}
	switch result.Status {
	case port.GatewayAccountCircuitMutationApplied:
		return port.GatewayAccountCircuitRevisionProjection{Status: port.GatewayAccountCircuitRevisionApplied, CurrentRevision: incident.DispatchRevision}, nil
	case port.GatewayAccountCircuitMutationIdempotent:
		return port.GatewayAccountCircuitRevisionProjection{Status: port.GatewayAccountCircuitRevisionIdempotent, CurrentRevision: result.State.DispatchRevision}, nil
	case port.GatewayAccountCircuitMutationStaleDispatchRevision:
		return port.GatewayAccountCircuitRevisionProjection{Status: port.GatewayAccountCircuitRevisionStale, CurrentRevision: result.State.DispatchRevision}, nil
	case port.GatewayAccountCircuitMutationStaleGeneration:
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit runtime generation is ahead of durable ledger")
	case port.GatewayAccountCircuitMutationCapacityExhausted:
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit runtime capacity exhausted")
	default:
		return port.GatewayAccountCircuitRevisionProjection{}, fmt.Errorf("account circuit runtime owner restore result is invalid")
	}
}

func (r *AccountCircuitIncidentRestorer) runRestore(ctx context.Context, keys accountCircuitRevisionKeys, incident port.GatewayAccountCircuitIncident, state []byte, now time.Time, retention time.Duration, capacity int) ([]byte, error) {
	retainedUntilMS := int64(0)
	if incident.RetainedUntil != nil {
		retainedUntilMS = incident.RetainedUntil.UTC().UnixMilli()
	}
	value, err := restoreAccountCircuitIncidentScript.Run(ctx, r.client.client, []string{
		keys.states, keys.due, keys.closed, keys.escalation, keys.revisions,
		keys.scopeRuntime, keys.runtimeScopes, keys.accountRuntimes, keys.runtimeAccounts, keys.indexMeta, keys.ledgerRevisions,
	}, string(state), incident.AccountID, incident.AccountRuntimeKey, incident.CircuitScopeKey,
		strconv.FormatInt(incident.DispatchRevision, 10), strconv.Itoa(incident.Generation),
		strconv.FormatInt(incident.UpdatedAt.UTC().UnixMilli(), 10), strconv.FormatInt(now.UnixMilli(), 10),
		strconv.FormatInt(retention.Milliseconds(), 10), strconv.Itoa(capacity), strconv.FormatInt(retainedUntilMS, 10),
		strconv.FormatInt(incident.LedgerRevision, 10)).Result()
	if err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), nil
	case []byte:
		return typed, nil
	default:
		return nil, fmt.Errorf("unexpected account circuit incident restore result type %T", value)
	}
}

func accountCircuitIncidentRuntimeStateFromIncident(incident port.GatewayAccountCircuitIncident) (accountCircuitIncidentRuntimeState, error) {
	if !validAccountCircuitRevisionText(incident.CircuitScopeKey, 2048) || !validAccountCircuitRevisionText(incident.AccountID, 256) || !validAccountCircuitRevisionText(incident.AccountRuntimeKey, 1024) || !validAccountCircuitRevisionText(incident.IncidentID, 256) || !validAccountCircuitRevisionText(incident.TransitionID, 256) || incident.DispatchRevision < 1 || incident.LedgerRevision < 1 || incident.Generation < 0 || incident.UpdatedAt.IsZero() {
		return accountCircuitIncidentRuntimeState{}, fmt.Errorf("account circuit incident is invalid")
	}
	scope := accountCircuitIncidentRuntimeScope{Kind: incident.ScopeKind, AccountRuntimeKey: incident.AccountRuntimeKey}
	switch incident.ScopeKind {
	case "account":
	case "key":
		scope.KeyFingerprint = incident.KeyFingerprint
		if scope.KeyFingerprint == "" {
			return accountCircuitIncidentRuntimeState{}, fmt.Errorf("account circuit key scope is invalid")
		}
	case "protocol_model":
		scope.ProtocolProfile = incident.ProtocolCode
		scope.RequestLane = incident.RequestLane
		scope.ModelBucket = incident.ModelFamily
		if !validAccountCircuitRevisionText(scope.ProtocolProfile, 256) || (scope.RequestLane != "text" && scope.RequestLane != "image") || !validAccountCircuitRevisionText(scope.ModelBucket, 512) {
			return accountCircuitIncidentRuntimeState{}, fmt.Errorf("account circuit protocol scope is invalid")
		}
	default:
		return accountCircuitIncidentRuntimeState{}, fmt.Errorf("account circuit scope kind is invalid")
	}
	phase := incident.State
	if phase == "PERSISTING" || phase == "SHADOWED_BY_PERSISTENT" {
		phase = "OPEN"
	}
	switch phase {
	case "CLOSED", "SUSPECT", "OPEN", "HALF_OPEN", "RECOVERING":
	default:
		return accountCircuitIncidentRuntimeState{}, fmt.Errorf("account circuit incident phase is invalid")
	}
	if phase == "CLOSED" && incident.RetainedUntil == nil {
		return accountCircuitIncidentRuntimeState{}, fmt.Errorf("closed account circuit incident retention is required")
	}
	state := accountCircuitIncidentRuntimeState{
		ScopeKey: incident.CircuitScopeKey, Scope: scope, Phase: phase, Generation: incident.Generation,
		DispatchRevision: strconv.FormatInt(incident.DispatchRevision, 10), TransitionID: incident.TransitionID,
		LedgerRevision: strconv.FormatInt(incident.LedgerRevision, 10),
		IncidentID:     incident.IncidentID, BackoffAttempt: incident.BackoffLevel,
		RecoverySuccessCount: incident.RecoveringSuccesses, UpdatedAtMS: incident.UpdatedAt.UTC().UnixMilli(),
	}
	if incident.OpenUntil != nil {
		value := incident.UpdatedAt.UTC().UnixMilli()
		state.OpenedAtMS = &value
	}
	if incident.NextTransitionAt != nil {
		value := incident.NextTransitionAt.UTC().UnixMilli()
		state.RetryAtMS = &value
	}
	if incident.LeaseID != "" && incident.LeaseUntil != nil {
		kind := incident.LeasePurpose
		if kind != "confirmation" && kind != "half_open" && kind != "recovery" {
			kind = ""
		}
		if kind != "" {
			state.Lease = &accountCircuitIncidentRuntimeLease{Kind: kind, LeaseID: incident.LeaseID, LeaseUntilMS: incident.LeaseUntil.UTC().UnixMilli()}
			if phase == "HALF_OPEN" {
				if kind == "recovery" {
					state.HalfOpenOrigin = "RECOVERING"
				} else {
					state.HalfOpenOrigin = "OPEN"
				}
			}
		}
	}
	return state, nil
}

var _ port.GatewayAccountCircuitIncidentRestorer = (*AccountCircuitIncidentRestorer)(nil)
