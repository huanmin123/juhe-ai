package gatewayhotquality

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Redis hot quality store mirroring
// backend/src/modules/gateway/runtime/hot-quality-redis-store.ts. The Lua
// scripts are carried over verbatim so Node and Go runtimes speak the same
// Redis payload during the migration window.

// RedisHotQualityStoreOptions mirrors RedisHotQualityStoreOptions. namespace
// replaces the Node implicit runtimeConfig.redis.namespace.
type RedisHotQualityStoreOptions struct {
	Namespace       string
	Name            string
	KeyCapacity     *int
	AttemptCapacity *int
	KeyTtlMs        *int64
	TerminalTtlMs   *int64
	Now             func() int64
}

// RedisHotQualityStoreKeys mirrors RedisHotQualityStoreKeys.
type RedisHotQualityStoreKeys struct {
	Prefix           string
	HotRegistry      string
	AttemptRegistry  string
	TerminalRegistry string
	Metrics          string
}

// ScriptRunner mirrors the Node RedisCommandClient.eval surface so the store
// logic is mockable independently of the Redis wire (Mock-first). The
// production adapter wraps *redis.Client.
type ScriptRunner interface {
	Eval(ctx context.Context, script string, keys []string, args ...string) (interface{}, error)
}

// RedisScriptRunner adapts go-redis to ScriptRunner.
type RedisScriptRunner struct {
	client *redis.Client
}

// NewRedisScriptRunner builds the production adapter.
func NewRedisScriptRunner(client *redis.Client) *RedisScriptRunner {
	return &RedisScriptRunner{client: client}
}

// Eval runs one Lua script; a nil reply surfaces as the redis.Nil error and
// is normalized to an empty result by the store layer.
func (runner *RedisScriptRunner) Eval(ctx context.Context, script string, keys []string, args ...string) (interface{}, error) {
	redisArgs := make([]interface{}, len(args))
	for index, arg := range args {
		redisArgs[index] = arg
	}
	return runner.client.Eval(ctx, script, keys, redisArgs...).Result()
}

// RedisHotQualityStore mirrors RedisHotQualityStore.
type RedisHotQualityStore struct {
	runner          ScriptRunner
	namespace       string
	keys            RedisHotQualityStoreKeys
	keyCapacity     int
	attemptCapacity int
	keyTtlMs        int64
	terminalTtlMs   int64
	now             func() int64
}

// NewRedisHotQualityStore mirrors the RedisHotQualityStore constructor.
func NewRedisHotQualityStore(runner ScriptRunner, options RedisHotQualityStoreOptions) (*RedisHotQualityStore, error) {
	if runner == nil {
		return nil, errors.New("Redis 热质量缺少 redisUrl")
	}
	namespace := normalizedNamespace(options.Namespace)
	if _, err := SanitizeRedisNamespacePart(namespace); err != nil {
		return nil, err
	}
	name := options.Name
	if name == "" {
		name = "gateway-hot-quality"
	}
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:hot-quality:"+safeRedisName(name, "gateway-hot-quality"))
	if err != nil {
		return nil, err
	}
	keys := RedisHotQualityStoreKeys{
		Prefix:           prefix,
		HotRegistry:      prefix + ":registry:hot",
		AttemptRegistry:  prefix + ":registry:attempt",
		TerminalRegistry: prefix + ":registry:terminal",
		Metrics:          prefix + ":metrics",
	}
	keyCapacity := 10_000
	if options.KeyCapacity != nil {
		keyCapacity = *options.KeyCapacity
	}
	attemptCapacity := 100_000
	if options.AttemptCapacity != nil {
		attemptCapacity = *options.AttemptCapacity
	}
	keyTtlMs := HotQualityKeyTTLMS
	if options.KeyTtlMs != nil {
		keyTtlMs = *options.KeyTtlMs
	}
	terminalTtlMs := HotQualityTerminalTTLMS
	if options.TerminalTtlMs != nil {
		terminalTtlMs = *options.TerminalTtlMs
	}
	normalizedKeyCapacity, err := positiveIntegerInt(keyCapacity, "keyCapacity")
	if err != nil {
		return nil, err
	}
	normalizedAttemptCapacity, err := positiveIntegerInt(attemptCapacity, "attemptCapacity")
	if err != nil {
		return nil, err
	}
	normalizedKeyTtl, err := positiveIntegerInt64(keyTtlMs, "keyTtlMs")
	if err != nil {
		return nil, err
	}
	normalizedTerminalTtl, err := positiveIntegerInt64(terminalTtlMs, "terminalTtlMs")
	if err != nil {
		return nil, err
	}
	if normalizedTerminalTtl < HotQualityTerminalTTLMS {
		return nil, fmt.Errorf("terminalTtlMs 不得少于 %dms", HotQualityTerminalTTLMS)
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &RedisHotQualityStore{
		runner:          runner,
		namespace:       namespace,
		keys:            keys,
		keyCapacity:     normalizedKeyCapacity,
		attemptCapacity: normalizedAttemptCapacity,
		keyTtlMs:        normalizedKeyTtl,
		terminalTtlMs:   normalizedTerminalTtl,
		now:             now,
	}, nil
}

// Keys exposes the resolved Redis key layout.
func (store *RedisHotQualityStore) Keys() RedisHotQualityStoreKeys {
	return store.keys
}

// RecordAttempt mirrors recordAttempt.
func (store *RedisHotQualityStore) RecordAttempt(ctx context.Context, input HotQualityRecordAttemptInput) (*HotQualityAttemptMutationResult, error) {
	scope, err := NormalizeHotQualityScope(input.Scope)
	if err != nil {
		return nil, err
	}
	attemptId, err := boundedIdentity(input.AttemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	nowMs, err := normalizedNowMs(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	result, err := store.mutate(ctx, "record_attempt", &redisHotQualityMutationPayload{
		Operation: "record_attempt",
		AttemptID: attemptId,
		Scope:     scope,
		NowMs:     nowMs,
	})
	if err != nil {
		return nil, err
	}
	return &HotQualityAttemptMutationResult{
		Status:         result.Status,
		RequestedScope: derefScope(result.RequestedScope),
		EffectiveScope: derefScope(result.EffectiveScope),
	}, nil
}

// RecordTerminal mirrors recordTerminal.
func (store *RedisHotQualityStore) RecordTerminal(ctx context.Context, input HotQualityRecordTerminalInput) (*HotQualityTerminalMutationResult, error) {
	scope, err := NormalizeHotQualityScope(input.Scope)
	if err != nil {
		return nil, err
	}
	if err := assertOutcomeClass(input.OutcomeClass); err != nil {
		return nil, err
	}
	if err := assertFailureScope(input.FailureScope); err != nil {
		return nil, err
	}
	if err := assertTerminalSource(input.Source); err != nil {
		return nil, err
	}
	attemptId, err := boundedIdentity(input.AttemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	terminalOutcomeId, err := boundedIdentity(input.TerminalOutcomeID, "terminalOutcomeId")
	if err != nil {
		return nil, err
	}
	nowMs, err := normalizedNowMs(derefOrDefault(input.NowMs, store.now))
	if err != nil {
		return nil, err
	}
	payload := &redisHotQualityMutationPayload{
		Operation:         "record_terminal",
		AttemptID:         attemptId,
		Scope:             scope,
		TerminalOutcomeID: terminalOutcomeId,
		OutcomeClass:      input.OutcomeClass,
		FailureScope:      input.FailureScope,
		Source:            input.Source,
		NowMs:             nowMs,
	}
	if input.FirstByteMs != nil {
		normalized, err := NormalizedFirstByteMs(*input.FirstByteMs)
		if err != nil {
			return nil, err
		}
		payload.FirstByteMs = &normalized
	}
	result, err := store.mutate(ctx, "record_terminal", payload)
	if err != nil {
		return nil, err
	}
	return &HotQualityTerminalMutationResult{
		Status:         result.Status,
		Terminal:       result.Terminal,
		EffectiveScope: result.EffectiveScope,
	}, nil
}

// Get mirrors get.
func (store *RedisHotQualityStore) Get(ctx context.Context, scopeInput HotQualityScope, nowMs *int64) (*HotQualitySnapshot, error) {
	scope, err := NormalizeHotQualityScope(scopeInput)
	if err != nil {
		return nil, err
	}
	scopeKey, err := HotQualityScopeKey(scope)
	if err != nil {
		return nil, err
	}
	normalizedNow, err := normalizedNowMs(derefOrDefault(nowMs, store.now))
	if err != nil {
		return nil, err
	}
	raw, err := store.runner.Eval(ctx, redisHotQualityReadEntryScript,
		[]string{redisHotQualityEntryKey(store.keys, scopeKey), store.keys.HotRegistry},
		strconvInt64(normalizedNow))
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return nil, nil
	}
	entry, err := parseRedisHotQualityEntry(encoded)
	if err != nil {
		return nil, err
	}
	buckets := make([]HotQualityBucketState, 0, len(entry.Buckets))
	// Node reads Object.values(entry.buckets); integer-like keys iterate in
	// ascending numeric order there, so mirror that ordering.
	minuteKeys := make([]int64, 0, len(entry.Buckets))
	for minuteKey := range entry.Buckets {
		minute, ok := atoi64(minuteKey)
		if !ok {
			continue
		}
		minuteKeys = append(minuteKeys, minute)
	}
	sortInt64s(minuteKeys)
	for _, minute := range minuteKeys {
		bucket := entry.Buckets[strconvInt64(minute)]
		bucket.UpstreamResponseFailures = int64OrZero(bucket.UpstreamResponseFailures)
		buckets = append(buckets, bucket)
	}
	return CreateHotQualitySnapshot(HotQualitySnapshotState{
		ScopeKey:    entry.ScopeKey,
		Scope:       entry.Scope,
		Buckets:     buckets,
		ExpiresAtMs: entry.ExpiresAtMs,
	}, normalizedNow), nil
}

// GetTerminal mirrors getTerminal.
func (store *RedisHotQualityStore) GetTerminal(ctx context.Context, attemptID string, nowMs *int64) (*HotQualityTerminalRecord, error) {
	attemptId, err := boundedIdentity(attemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	normalizedNow, err := normalizedNowMs(derefOrDefault(nowMs, store.now))
	if err != nil {
		return nil, err
	}
	raw, err := store.runner.Eval(ctx, redisHotQualityReadTerminalScript,
		[]string{redisHotQualityAttemptKey(store.keys, attemptId), store.keys.AttemptRegistry},
		strconvInt64(normalizedNow))
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return nil, nil
	}
	return parseTerminalRecord(encoded)
}

// Stats mirrors stats.
func (store *RedisHotQualityStore) Stats(ctx context.Context, nowMs *int64) (*HotQualityStoreStats, error) {
	normalizedNow, err := normalizedNowMs(derefOrDefault(nowMs, store.now))
	if err != nil {
		return nil, err
	}
	raw, err := store.runner.Eval(ctx, redisHotQualityStatsScript,
		[]string{store.keys.HotRegistry, store.keys.AttemptRegistry, store.keys.TerminalRegistry, store.keys.Metrics},
		strconvInt64(normalizedNow))
	if err != nil {
		return nil, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return nil, errors.New("Redis 热质量统计返回值无效")
	}
	var parsed struct {
		KeyCount                    *float64 `json:"keyCount"`
		AttemptIdentityCount        *float64 `json:"attemptIdentityCount"`
		TerminalIdentityCount       *float64 `json:"terminalIdentityCount"`
		KeyCreationRefusals         *float64 `json:"keyCreationRefusals"`
		HighCardinalityDegradations *float64 `json:"highCardinalityDegradations"`
		AttemptCapacityRefusals     *float64 `json:"attemptCapacityRefusals"`
		TerminalQualityKeyMisses    *float64 `json:"terminalQualityKeyMisses"`
	}
	if err := json.Unmarshal([]byte(encoded), &parsed); err != nil {
		return nil, errors.New("Redis 热质量统计返回值无效")
	}
	keyCount, err := redisNumericValue(parsed.KeyCount, false, "keyCount")
	if err != nil {
		return nil, err
	}
	attemptIdentityCount, err := redisNumericValue(parsed.AttemptIdentityCount, false, "attemptIdentityCount")
	if err != nil {
		return nil, err
	}
	terminalIdentityCount, err := redisNumericValue(parsed.TerminalIdentityCount, false, "terminalIdentityCount")
	if err != nil {
		return nil, err
	}
	keyCreationRefusals, err := redisNumericValue(parsed.KeyCreationRefusals, true, "keyCreationRefusals")
	if err != nil {
		return nil, err
	}
	highCardinalityDegradations, err := redisNumericValue(parsed.HighCardinalityDegradations, true, "highCardinalityDegradations")
	if err != nil {
		return nil, err
	}
	attemptCapacityRefusals, err := redisNumericValue(parsed.AttemptCapacityRefusals, true, "attemptCapacityRefusals")
	if err != nil {
		return nil, err
	}
	terminalQualityKeyMisses, err := redisNumericValue(parsed.TerminalQualityKeyMisses, true, "terminalQualityKeyMisses")
	if err != nil {
		return nil, err
	}
	return &HotQualityStoreStats{
		KeyCount:                    keyCount,
		AttemptIdentityCount:        attemptIdentityCount,
		TerminalIdentityCount:       terminalIdentityCount,
		KeyCreationRefusals:         keyCreationRefusals,
		HighCardinalityDegradations: highCardinalityDegradations,
		AttemptCapacityRefusals:     attemptCapacityRefusals,
		TerminalQualityKeyMisses:    terminalQualityKeyMisses,
	}, nil
}

// redisHotQualityMutationPayload mirrors the Lua input JSON payload.
type redisHotQualityMutationPayload struct {
	Operation         string          `json:"operation"`
	AttemptID         string          `json:"attemptId"`
	Scope             HotQualityScope `json:"scope"`
	NowMs             int64           `json:"nowMs"`
	TerminalOutcomeID string          `json:"terminalOutcomeId,omitempty"`
	OutcomeClass      string          `json:"outcomeClass,omitempty"`
	FailureScope      string          `json:"failureScope,omitempty"`
	Source            string          `json:"source,omitempty"`
	FirstByteMs       *int64          `json:"firstByteMs,omitempty"`

	RequestedScope    HotQualityScope `json:"requestedScope"`
	RequestedScopeKey string          `json:"requestedScopeKey"`
	FallbackScope     HotQualityScope `json:"fallbackScope"`
	FallbackScopeKey  string          `json:"fallbackScopeKey"`
}

type redisHotQualityMutationResponse struct {
	Status         string                    `json:"status"`
	RequestedScope *HotQualityScope          `json:"requestedScope"`
	EffectiveScope *HotQualityScope          `json:"effectiveScope"`
	Terminal       *HotQualityTerminalRecord `json:"terminal"`
}

func (store *RedisHotQualityStore) mutate(ctx context.Context, operation string, input *redisHotQualityMutationPayload) (*redisHotQualityMutationResponse, error) {
	requestedScope := input.Scope
	requestedScopeKey, err := HotQualityScopeKey(requestedScope)
	if err != nil {
		return nil, err
	}
	fallbackScope, err := ProtocolHotQualityScope(requestedScope)
	if err != nil {
		return nil, err
	}
	fallbackScopeKey, err := HotQualityScopeKey(fallbackScope)
	if err != nil {
		return nil, err
	}
	terminalOutcomeId := input.TerminalOutcomeID
	if terminalOutcomeId == "" {
		// Node: typeof input.terminalOutcomeId === 'string' ? ... : `attempt-${attemptId}`
		terminalOutcomeId = "attempt-" + input.AttemptID
	}
	input.Operation = operation
	input.RequestedScope = requestedScope
	input.RequestedScopeKey = requestedScopeKey
	input.FallbackScope = fallbackScope
	input.FallbackScopeKey = fallbackScopeKey
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	raw, err := store.runner.Eval(ctx, redisHotQualityMutationScript,
		[]string{
			redisHotQualityEntryKey(store.keys, requestedScopeKey),
			redisHotQualityEntryKey(store.keys, fallbackScopeKey),
			redisHotQualityAttemptKey(store.keys, input.AttemptID),
			redisHotQualityTerminalKey(store.keys, terminalOutcomeId),
			store.keys.HotRegistry,
			store.keys.AttemptRegistry,
			store.keys.TerminalRegistry,
			store.keys.Metrics,
		},
		string(payload),
		strconvInt64(int64(store.keyCapacity)),
		strconvInt64(int64(store.attemptCapacity)),
		strconvInt64(store.keyTtlMs),
		strconvInt64(store.terminalTtlMs))
	if err != nil {
		return nil, err
	}
	encoded, ok := redisStringResult(raw)
	if !ok || encoded == "" {
		return nil, errors.New("Redis 热质量 mutation 返回值无效")
	}
	var result redisHotQualityMutationResponse
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		return nil, errors.New("Redis 热质量 mutation 返回值无效")
	}
	if result.Status == "" {
		return nil, errors.New("Redis 热质量 mutation 结构无效")
	}
	// Node re-clones the nested objects; Go value decoding already yields
	// freshly allocated structures.
	return &result, nil
}

type redisHotQualityEntry struct {
	ScopeKey    string                           `json:"scopeKey"`
	Scope       HotQualityScope                  `json:"scope"`
	Buckets     map[string]HotQualityBucketState `json:"buckets"`
	ExpiresAtMs int64                            `json:"expiresAtMs"`
}

func redisHotQualityEntryKey(keys RedisHotQualityStoreKeys, scopeKey string) string {
	return keys.Prefix + ":entry:" + redisIdentityHash(scopeKey)
}

func redisHotQualityAttemptKey(keys RedisHotQualityStoreKeys, attemptId string) string {
	return keys.Prefix + ":attempt:" + redisIdentityHash(attemptId)
}

func redisHotQualityTerminalKey(keys RedisHotQualityStoreKeys, terminalOutcomeId string) string {
	return keys.Prefix + ":terminal:" + redisIdentityHash(terminalOutcomeId)
}

func redisIdentityHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func parseRedisHotQualityEntry(value string) (*redisHotQualityEntry, error) {
	var wire struct {
		ScopeKey    string                           `json:"scopeKey"`
		Scope       *HotQualityScope                 `json:"scope"`
		Buckets     map[string]HotQualityBucketState `json:"buckets"`
		ExpiresAtMs *int64                           `json:"expiresAtMs"`
	}
	if err := json.Unmarshal([]byte(value), &wire); err != nil {
		return nil, errors.New("Redis 热质量 entry 结构无效")
	}
	if wire.ScopeKey == "" || wire.Scope == nil || wire.Buckets == nil || wire.ExpiresAtMs == nil {
		return nil, errors.New("Redis 热质量 entry 结构无效")
	}
	scope, err := NormalizeHotQualityScope(*wire.Scope)
	if err != nil {
		return nil, err
	}
	return &redisHotQualityEntry{
		ScopeKey:    wire.ScopeKey,
		Scope:       scope,
		Buckets:     wire.Buckets,
		ExpiresAtMs: *wire.ExpiresAtMs,
	}, nil
}

func parseTerminalRecord(value string) (*HotQualityTerminalRecord, error) {
	var wire struct {
		TerminalOutcomeID string `json:"terminalOutcomeId"`
		OutcomeClass      string `json:"outcomeClass"`
		FailureScope      string `json:"failureScope"`
		Source            string `json:"source"`
		CreatedAtMs       *int64 `json:"createdAtMs"`
	}
	if err := json.Unmarshal([]byte(value), &wire); err != nil {
		return nil, errors.New("Redis 热质量终态结构无效")
	}
	if wire.TerminalOutcomeID == "" || wire.OutcomeClass == "" || wire.FailureScope == "" || wire.Source == "" || wire.CreatedAtMs == nil {
		return nil, errors.New("Redis 热质量终态结构无效")
	}
	return &HotQualityTerminalRecord{
		TerminalOutcomeID: wire.TerminalOutcomeID,
		OutcomeClass:      wire.OutcomeClass,
		FailureScope:      wire.FailureScope,
		Source:            wire.Source,
		CreatedAtMs:       *wire.CreatedAtMs,
	}, nil
}

func redisStringResult(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	}
	return "", false
}

// redisNumericValue mirrors numericValue: required=false applies the Node
// `?? 0` default for the refusals metrics.
func redisNumericValue(value *float64, withDefault bool, name string) (int64, error) {
	if value == nil {
		if withDefault {
			return 0, nil
		}
		return 0, fmt.Errorf("Redis 热质量 %s 返回值无效", name)
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return 0, fmt.Errorf("Redis 热质量 %s 返回值无效", name)
	}
	return int64(*value), nil
}

func safeRedisName(value string, fallback string) string {
	normalized := strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == ':', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	if builder.Len() == 0 {
		return fallback
	}
	return builder.String()
}

func strconvInt64(value int64) string {
	return fmt.Sprintf("%d", value)
}

func int64OrZero(value int64) int64 { return value }

func atoi64(value string) (int64, bool) {
	var parsed int64
	negatives := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if i == 0 && c == '-' {
			negatives = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		parsed = parsed*10 + int64(c-'0')
	}
	if negatives {
		parsed = -parsed
	}
	return parsed, true
}

func sortInt64s(values []int64) {
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
}

func derefScope(scope *HotQualityScope) HotQualityScope {
	if scope == nil {
		return HotQualityScope{}
	}
	return *scope
}

// redisClientCache mirrors shared/redis-client.getRedisClient: one client per
// URL for the process lifetime.
var redisClientCache = struct {
	sync.Mutex
	clients map[string]*redis.Client
}{clients: make(map[string]*redis.Client)}

// GetRedisClient mirrors getRedisClient.
func GetRedisClient(ctx context.Context, redisURL string) (*redis.Client, error) {
	normalized := strings.TrimSpace(redisURL)
	if normalized == "" {
		return nil, errors.New("Redis 连接串不能为空")
	}
	redisClientCache.Lock()
	defer redisClientCache.Unlock()
	if client, ok := redisClientCache.clients[normalized]; ok {
		return client, nil
	}
	options, err := redis.ParseURL(normalized)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	redisClientCache.clients[normalized] = client
	return client, nil
}

// Both real dispatch accounting and terminal projection are committed by this
// one Lua script (mirrors redisHotQualityMutationScript verbatim).
const redisHotQualityMutationScript = `
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
    upstreamResponseFailures = 0,
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
  elseif outcome == 'upstream_response_failure' then
    bucket['upstreamResponseFailures'] = increment(bucket['upstreamResponseFailures'])
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
  if input['firstByteMs'] ~= nil and outcome ~= 'upstream_response_failure' and outcome ~= 'unknown' and outcome ~= 'client_cancellation' then
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

const redisHotQualityReadEntryScript = `
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

const redisHotQualityReadTerminalScript = `
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

const redisHotQualityStatsScript = `
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
