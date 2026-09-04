package gatewayhotquality

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Redis same-tier exploration store mirroring
// backend/src/modules/gateway/runtime/same-tier-exploration-redis-store.ts.
// The mutation Lua script is carried over verbatim.

// RedisSameTierExplorationStoreOptions mirrors the Node options object.
type RedisSameTierExplorationStoreOptions struct {
	Namespace    string
	Name         string
	StateTtlMs   *int64
	PoolCapacity *int
	Now          func() int64
}

// RedisSameTierExplorationStore mirrors RedisSameTierExplorationStore.
type RedisSameTierExplorationStore struct {
	runner       ScriptRunner
	prefix       string
	stateTtlMs   int64
	poolCapacity int
	now          func() int64
}

// NewRedisSameTierExplorationStore mirrors the Node constructor.
func NewRedisSameTierExplorationStore(runner ScriptRunner, options RedisSameTierExplorationStoreOptions) (*RedisSameTierExplorationStore, error) {
	if runner == nil {
		return nil, errors.New("redisUrl 必须是 1 到 512 字符")
	}
	namespace := normalizedNamespace(options.Namespace)
	if _, err := SanitizeRedisNamespacePart(namespace); err != nil {
		return nil, err
	}
	name := options.Name
	if name == "" {
		name = "gateway"
	}
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:same-tier-exploration:"+safeRedisName(name, "gateway"))
	if err != nil {
		return nil, err
	}
	stateTtlMs := SameTierExplorationStateTTLMS
	if options.StateTtlMs != nil {
		stateTtlMs = *options.StateTtlMs
	}
	poolCapacity := SameTierExplorationPoolCapacity
	if options.PoolCapacity != nil {
		poolCapacity = *options.PoolCapacity
	}
	normalizedTtl, err := explorationPositiveInteger(stateTtlMs, "stateTtlMs")
	if err != nil {
		return nil, err
	}
	if poolCapacity <= 0 {
		return nil, errors.New("poolCapacity 必须是正整数")
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &RedisSameTierExplorationStore{
		runner:       runner,
		prefix:       prefix,
		stateTtlMs:   normalizedTtl,
		poolCapacity: poolCapacity,
		now:          now,
	}, nil
}

// Prefix exposes the resolved key prefix (mirrors
// redisSameTierExplorationStoreKeys().prefix).
func (store *RedisSameTierExplorationStore) Prefix() string {
	return store.prefix
}

// Registry exposes the registry key.
func (store *RedisSameTierExplorationStore) Registry() string {
	return store.prefix + ":registry"
}

// Get mirrors get.
func (store *RedisSameTierExplorationStore) Get(ctx context.Context, input SameTierExplorationGetInput) (*SameTierExplorationState, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	result, err := store.mutate(ctx, "get", map[string]interface{}{
		"operation": "get",
		"poolKey":   input.PoolKey,
		"nowMs":     nowMs,
	}, nowMs)
	if err != nil {
		return nil, err
	}
	return &result.State, nil
}

// Accrue mirrors accrue.
func (store *RedisSameTierExplorationStore) Accrue(ctx context.Context, input SameTierExplorationAccrueInput) (*SameTierExplorationState, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	accrualToken, err := explorationRequiredKey(input.AccrualToken, "accrualToken")
	if err != nil {
		return nil, err
	}
	result, err := store.mutate(ctx, "accrue", map[string]interface{}{
		"operation":    "accrue",
		"poolKey":      input.PoolKey,
		"accrualToken": accrualToken,
		"eligible":     input.Eligible,
		"nowMs":        nowMs,
	}, nowMs)
	if err != nil {
		return nil, err
	}
	return &result.State, nil
}

// Reserve mirrors reserve.
func (store *RedisSameTierExplorationStore) Reserve(ctx context.Context, input SameTierExplorationReserveInput) (*SameTierExplorationReserveResult, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	leaseUntilMs, err := explorationNormalizedNow(input.LeaseUntilMs)
	if err != nil {
		return nil, err
	}
	if leaseUntilMs <= nowMs {
		return nil, errors.New("leaseUntilMs 必须晚于 nowMs")
	}
	if leaseUntilMs > nowMs+store.stateTtlMs {
		return nil, errors.New("leaseUntilMs 不得晚于 pool TTL")
	}
	reservationId, err := explorationRequiredKey(input.ReservationID, "reservationId")
	if err != nil {
		return nil, err
	}
	accountRuntimeKey, err := explorationRequiredKey(input.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return nil, err
	}
	result, err := store.mutate(ctx, "reserve", map[string]interface{}{
		"operation":         "reserve",
		"poolKey":           input.PoolKey,
		"reservationId":     reservationId,
		"accountRuntimeKey": accountRuntimeKey,
		"leaseUntilMs":      leaseUntilMs,
		"nowMs":             nowMs,
	}, nowMs)
	if err != nil {
		return nil, err
	}
	return &SameTierExplorationReserveResult{
		Status:      result.Status,
		State:       result.State,
		Reservation: result.Reservation,
	}, nil
}

// Settle mirrors settle.
func (store *RedisSameTierExplorationStore) Settle(ctx context.Context, input SameTierExplorationSettleInput) (*SameTierExplorationSettleResult, error) {
	nowMs, err := explorationNormalizedNow(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	reservationId, err := explorationRequiredKey(input.ReservationID, "reservationId")
	if err != nil {
		return nil, err
	}
	accountRuntimeKey, err := explorationRequiredKey(input.AccountRuntimeKey, "accountRuntimeKey")
	if err != nil {
		return nil, err
	}
	result, err := store.mutate(ctx, "settle", map[string]interface{}{
		"operation":         "settle",
		"poolKey":           input.PoolKey,
		"reservationId":     reservationId,
		"accountRuntimeKey": accountRuntimeKey,
		"outcome":           input.Outcome,
		"nowMs":             nowMs,
	}, nowMs)
	if err != nil {
		return nil, err
	}
	return &SameTierExplorationSettleResult{
		Status: result.Status,
		State:  result.State,
	}, nil
}

type redisExplorationMutationResult struct {
	Status      string                          `json:"status"`
	State       SameTierExplorationState        `json:"state"`
	Reservation *SameTierExplorationReservation `json:"reservation"`
}

func (store *RedisSameTierExplorationStore) mutate(ctx context.Context, operation string, input map[string]interface{}, nowMs int64) (*redisExplorationMutationResult, error) {
	poolKey, err := explorationRequiredKey(input["poolKey"].(string), "poolKey")
	if err != nil {
		return nil, err
	}
	input["operation"] = operation
	input["poolKey"] = poolKey
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	raw, err := store.runner.Eval(ctx, sameTierExplorationMutationScript,
		[]string{store.stateKey(poolKey), store.prefix + ":registry"},
		strconvInt64(nowMs),
		strconvInt64(store.stateTtlMs),
		strconvInt64(int64(store.poolCapacity)),
		strconvInt64(SameTierExplorationIdentityCapacity),
		operation,
		string(payload))
	if err != nil {
		return nil, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return nil, errors.New("Redis 同层探索状态返回值无效")
	}
	var wire struct {
		Status      string                          `json:"status"`
		State       *redisExplorationStateWire      `json:"state"`
		Reservation *SameTierExplorationReservation `json:"reservation"`
	}
	if err := json.Unmarshal([]byte(encoded), &wire); err != nil {
		return nil, errors.New("Redis 同层探索状态结构无效")
	}
	if wire.State == nil {
		return nil, errors.New("Redis 同层探索状态结构无效")
	}
	state, err := wire.State.toState()
	if err != nil {
		return nil, err
	}
	normalized, err := NormalizeSameTierExplorationState(state, nowMs)
	if err != nil {
		return nil, err
	}
	return &redisExplorationMutationResult{
		Status:      wire.Status,
		State:       *CloneSameTierExplorationState(*normalized),
		Reservation: cloneReservationPtr(wire.Reservation),
	}, nil
}

func (store *RedisSameTierExplorationStore) stateKey(poolKey string) string {
	digest := sha256.Sum256([]byte(poolKey))
	return store.prefix + ":pool:" + hex.EncodeToString(digest[:])
}

// redisExplorationStateWire tolerates the Lua cjson wire variance where empty
// tables arrive as `{}` (older Redis builds) or `[]` (gopher-lua); both mirror
// the Node Array.isArray / `or {}` normalization guards.
type redisExplorationStateWire struct {
	PoolKey                     string          `json:"poolKey"`
	Credit                      float64         `json:"credit"`
	Cursor                      int64           `json:"cursor"`
	Reservations                json.RawMessage `json:"reservations"`
	CooldownUntilMsByRuntimeKey json.RawMessage `json:"cooldownUntilMsByRuntimeKey"`
	AccruedTokens               json.RawMessage `json:"accruedTokens"`
	SettledReservationIDs       json.RawMessage `json:"settledReservationIds"`
	ExpiresAtMs                 int64           `json:"expiresAtMs"`
}

func (wire *redisExplorationStateWire) toState() (SameTierExplorationState, error) {
	cooldown, err := decodeJSONInt64Map(wire.CooldownUntilMsByRuntimeKey)
	if err != nil {
		return SameTierExplorationState{}, err
	}
	state := SameTierExplorationState{
		PoolKey:                     wire.PoolKey,
		Credit:                      wire.Credit,
		Cursor:                      wire.Cursor,
		CooldownUntilMsByRuntimeKey: cooldown,
		ExpiresAtMs:                 wire.ExpiresAtMs,
	}
	reservations, err := decodeJSONArray[SameTierExplorationReservation](wire.Reservations)
	if err != nil {
		return SameTierExplorationState{}, err
	}
	state.Reservations = reservations
	accruedTokens, err := decodeJSONArray[string](wire.AccruedTokens)
	if err != nil {
		return SameTierExplorationState{}, err
	}
	state.AccruedTokens = accruedTokens
	settled, err := decodeJSONArray[string](wire.SettledReservationIDs)
	if err != nil {
		return SameTierExplorationState{}, err
	}
	state.SettledReservationIDs = settled
	return state, nil
}

// decodeJSONArray decodes a JSON array; `{}` / `[]` / null / absent decode to
// an empty slice.
func decodeJSONArray[T any](raw json.RawMessage) ([]T, error) {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed == "null" || trimmed == "{}" || trimmed == "[]" {
		return nil, nil
	}
	var values []T
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, errors.New("Redis 同层探索状态结构无效")
	}
	return values, nil
}

// decodeJSONInt64Map decodes a JSON object; `{}` / `[]` / null / absent
// decode to an empty map.
func decodeJSONInt64Map(raw json.RawMessage) (map[string]int64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 || trimmed == "null" || trimmed == "{}" || trimmed == "[]" {
		return map[string]int64{}, nil
	}
	var values map[string]int64
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, errors.New("Redis 同层探索状态结构无效")
	}
	return values, nil
}

func cloneReservationPtr(reservation *SameTierExplorationReservation) *SameTierExplorationReservation {
	if reservation == nil {
		return nil
	}
	cloned := *reservation
	return &cloned
}

// sameTierExplorationMutationScript mirrors sameTierExplorationMutationScript
// verbatim.
const sameTierExplorationMutationScript = `
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
  if input['eligible'] and not has_value(state['accruedTokens'], token) then
    -- Rolling idempotency window: capacity must not permanently freeze a hot pool.
    while #(state['accruedTokens'] or {}) >= identity_capacity do
      table.remove(state['accruedTokens'], 1)
    end
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
