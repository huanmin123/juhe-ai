package gatewayaccounteffects

import (
	"context"
	"crypto/rand"
	"errors"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Account API key transient failure statuses mirror
// Exclude<AccountApiKeyRuntimeStatus, 'active' | 'disabled'>.
type AccountApiKeyFailureStatus string

// Failure statuses.
const (
	APIKeyStatusTemporaryUnavailable AccountApiKeyFailureStatus = "temporary_unavailable"
	APIKeyStatusRateLimited          AccountApiKeyFailureStatus = "rate_limited"
	APIKeyStatusError                AccountApiKeyFailureStatus = "error"
)

// AccountApiKeyTransientTarget mirrors AccountApiKeyTransientTarget.
type AccountApiKeyTransientTarget struct {
	AccountID      string
	KeyFingerprint string
	KeyIndex       *int
}

// AccountApiKeyTransientState mirrors AccountApiKeyTransientState; the JSON
// tags are the Redis wire contract shared with Node.
type AccountApiKeyTransientState struct {
	SchemaVersion   int                        `json:"schemaVersion"`
	AccountID       string                     `json:"accountId"`
	KeyFingerprint  string                     `json:"keyFingerprint"`
	KeyIndex        *int                       `json:"keyIndex,omitempty"`
	Generation      string                     `json:"generation"`
	LastObservedAtMs int64                     `json:"lastObservedAtMs"`
	ObservationKind string                     `json:"observationKind"` // 'failure' | 'success'
	FailureCount    int                        `json:"failureCount"`
	Status          AccountApiKeyFailureStatus `json:"status,omitempty"`
	SuppressUntilMs *int64                     `json:"suppressUntilMs,omitempty"`
}

// AccountApiKeyTransientMutationReason mirrors the reason union.
type AccountApiKeyTransientMutationReason string

// Mutation reasons.
const (
	TransientReasonApplied         AccountApiKeyTransientMutationReason = "applied"
	TransientReasonStaleGeneration AccountApiKeyTransientMutationReason = "stale_generation"
	TransientReasonMissingState    AccountApiKeyTransientMutationReason = "missing_state"
)

// AccountApiKeyTransientMutationResult mirrors AccountApiKeyTransientMutationResult.
type AccountApiKeyTransientMutationResult struct {
	Applied bool                                 `json:"applied"`
	Reason  AccountApiKeyTransientMutationReason `json:"reason"`
	State   *AccountApiKeyTransientState         `json:"state,omitempty"`
}

// AccountApiKeyTransientDispatchState mirrors AccountApiKeyTransientDispatchState.
type AccountApiKeyTransientDispatchState struct {
	State      *AccountApiKeyTransientState
	Suppressed bool
}

// AccountApiKeyTransientStateStore mirrors AccountApiKeyTransientStateStore.
type AccountApiKeyTransientStateStore interface {
	RecordFailure(ctx context.Context, input TransientMutationInput) (AccountApiKeyTransientMutationResult, error)
	RecordSuccess(ctx context.Context, input TransientMutationInput) (AccountApiKeyTransientMutationResult, error)
	LoadMany(ctx context.Context, accountID string, keyFingerprints []string) ([]AccountApiKeyTransientDispatchState, error)
}

// TransientMutationInput carries the recordFailure/recordSuccess arguments.
type TransientMutationInput struct {
	Target            AccountApiKeyTransientTarget
	Status            AccountApiKeyFailureStatus // failure only
	ExpectedGeneration string
}

// Transient store defaults (account-api-key-transient-redis-store.ts).
const (
	TransientMinimumStateTtlMs        = int64(25 * 60 * 60_000)
	TransientDefaultStateTtlMs        = int64(48 * 60 * 60_000)
	TransientDefaultFailureCounterWindowMs = int64(10 * 60_000)
	TransientDefaultStoreName         = "gateway-account-api-key-transient-avoidance"
)

var transientSuppressionDelayMs = []int64{3_000, 5_000, 10_000}

// RedisAccountApiKeyTransientStateStoreOptions mirrors
// RedisAccountApiKeyTransientStateStoreOptions.
type RedisAccountApiKeyTransientStateStoreOptions struct {
	RedisURL                        string
	Namespace                       string
	Name                            string
	StateTtlMs                      int64
	SuppressionDelayMs              []int64
	FailureCounterWindowMs          int64
	AllowUnsafeShortStateTtlForTest bool
}

// RedisAccountApiKeyTransientStateStore is the go-redis port of
// RedisAccountApiKeyTransientStateStore. The two Lua scripts are byte
// identical to the Node originals: they are the executable contract shared
// with in-flight Node gateway processes.
type RedisAccountApiKeyTransientStateStore struct {
	redisURL              string
	keyPrefix             string
	stateTtlMs            int64
	suppressionDelayMs    []int64
	failureCounterWindowMs int64

	once   sync.Once
	client *redis.Client
	opts   *redis.Options
	err    error
}

// NewRedisAccountApiKeyTransientStateStore mirrors the constructor
// validations with the exact Node error messages.
func NewRedisAccountApiKeyTransientStateStore(options RedisAccountApiKeyTransientStateStoreOptions) (*RedisAccountApiKeyTransientStateStore, error) {
	redisURL, redisURLErr := requireText(options.RedisURL, "redisUrl")
	if redisURLErr != nil {
		return nil, redisURLErr
	}
	name := sanitizeRedisKeyPart(orDefaultString(options.Name, TransientDefaultStoreName))
	if strings.TrimSpace(options.Namespace) == "" {
		return nil, errors.New("Redis namespace 不能为空")
	}
	stateTtlMs := options.StateTtlMs
	if stateTtlMs == 0 {
		stateTtlMs = TransientDefaultStateTtlMs
	}
	if stateTtlMs <= 0 {
		return nil, errors.New("stateTtlMs 必须是正整数")
	}
	suppressionDelays := options.SuppressionDelayMs
	if len(suppressionDelays) == 0 {
		suppressionDelays = append([]int64(nil), transientSuppressionDelayMs...)
	}
	for index, value := range suppressionDelays {
		if value <= 0 {
			return nil, fmt.Errorf("suppressionDelayMs[%d] 必须是正整数", index)
		}
	}
	failureCounterWindowMs := options.FailureCounterWindowMs
	if failureCounterWindowMs == 0 {
		failureCounterWindowMs = TransientDefaultFailureCounterWindowMs
	}
	if failureCounterWindowMs <= 0 {
		return nil, errors.New("failureCounterWindowMs 必须是正整数")
	}
	if !options.AllowUnsafeShortStateTtlForTest && stateTtlMs < TransientMinimumStateTtlMs {
		return nil, fmt.Errorf("stateTtlMs 不得少于 %dms，必须覆盖网关最大在途请求", TransientMinimumStateTtlMs)
	}
	maxDelay := suppressionDelays[0]
	for _, value := range suppressionDelays {
		if value > maxDelay {
			maxDelay = value
		}
	}
	if stateTtlMs < maxDelay {
		return nil, errors.New("stateTtlMs 不得短于最大 suppression delay")
	}
	// redisNamespacedKey(`juhe-ai:state:<name>:state:`).
	return &RedisAccountApiKeyTransientStateStore{
		redisURL:              redisURL,
		keyPrefix:             "juhe-ai:" + strings.TrimSpace(options.Namespace) + ":state:" + name + ":state:",
		stateTtlMs:            stateTtlMs,
		suppressionDelayMs:    append([]int64(nil), suppressionDelays...),
		failureCounterWindowMs: failureCounterWindowMs,
	}, nil
}

// stateKey mirrors the private stateKey: sha256 of `${accountId}\0${keyFingerprint}`.
func (s *RedisAccountApiKeyTransientStateStore) stateKey(target AccountApiKeyTransientTarget) string {
	identity := target.AccountID + "\x00" + target.KeyFingerprint
	digest := sha256.Sum256([]byte(identity))
	return s.keyPrefix + hex.EncodeToString(digest[:])
}

func (s *RedisAccountApiKeyTransientStateStore) clientForUse(ctx context.Context) (*redis.Client, error) {
	s.once.Do(func() {
		opts, err := redis.ParseURL(s.redisURL)
		if err != nil {
			s.err = err
			return
		}
		s.opts = opts
		s.client = redis.NewClient(opts)
	})
	if s.err != nil {
		return nil, s.err
	}
	return s.client, nil
}

// Close releases the underlying client.
func (s *RedisAccountApiKeyTransientStateStore) Close() error {
	s.once.Do(func() {})
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// RecordFailure implements AccountApiKeyTransientStateStore.
func (s *RedisAccountApiKeyTransientStateStore) RecordFailure(ctx context.Context, input TransientMutationInput) (AccountApiKeyTransientMutationResult, error) {
	target, err := normalizeTransientTarget(input.Target)
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	expectedGeneration, err := requireText(input.ExpectedGeneration, "expectedGeneration")
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	return s.mutate(ctx, transientMutationArgs{
		operation:          "failure",
		target:             target,
		status:             input.Status,
		expectedGeneration: expectedGeneration,
	})
}

// RecordSuccess implements AccountApiKeyTransientStateStore.
func (s *RedisAccountApiKeyTransientStateStore) RecordSuccess(ctx context.Context, input TransientMutationInput) (AccountApiKeyTransientMutationResult, error) {
	target, err := normalizeTransientTarget(input.Target)
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	expectedGeneration, err := requireText(input.ExpectedGeneration, "expectedGeneration")
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	return s.mutate(ctx, transientMutationArgs{
		operation:          "success",
		target:             target,
		expectedGeneration: expectedGeneration,
	})
}

type transientMutationArgs struct {
	operation         string
	target            AccountApiKeyTransientTarget
	status             AccountApiKeyFailureStatus
	expectedGeneration string
}

func (s *RedisAccountApiKeyTransientStateStore) mutate(ctx context.Context, input transientMutationArgs) (AccountApiKeyTransientMutationResult, error) {
	client, err := s.clientForUse(ctx)
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	keyIndex := ""
	if input.target.KeyIndex != nil {
		keyIndex = fmt.Sprintf("%d", *input.target.KeyIndex)
	}
	raw, err := client.Eval(ctx, redisAccountApiKeyTransientMutationScript, []string{s.stateKey(input.target)},
		input.operation,
		input.target.AccountID,
		input.target.KeyFingerprint,
		keyIndex,
		string(input.status),
		input.expectedGeneration,
		newUUID(),
		fmt.Sprintf("%d", s.stateTtlMs),
		mustJSON(s.suppressionDelayMs),
		fmt.Sprintf("%d", s.failureCounterWindowMs),
	).Result()
	if err != nil {
		return AccountApiKeyTransientMutationResult{}, err
	}
	rawText, ok := raw.(string)
	if !ok {
		return AccountApiKeyTransientMutationResult{}, errors.New("Redis API Key transient mutation 返回值无效")
	}
	var parsed AccountApiKeyTransientMutationResult
	if err := json.Unmarshal([]byte(rawText), &parsed); err != nil {
		return AccountApiKeyTransientMutationResult{}, errors.New("Redis API Key transient mutation 返回值无效")
	}
	if parsed.Reason != TransientReasonApplied && parsed.Reason != TransientReasonStaleGeneration && parsed.Reason != TransientReasonMissingState {
		return AccountApiKeyTransientMutationResult{}, errors.New("Redis API Key transient mutation 结构无效")
	}
	if parsed.State != nil {
		state, stateErr := parseTransientStateValue(*parsed.State)
		if stateErr != nil {
			return AccountApiKeyTransientMutationResult{}, errors.New("Redis API Key transient mutation 结构无效")
		}
		parsed.State = state
	}
	return parsed, nil
}

// LoadMany implements AccountApiKeyTransientStateStore.
func (s *RedisAccountApiKeyTransientStateStore) LoadMany(ctx context.Context, accountIDInput string, keyFingerprintsInput []string) ([]AccountApiKeyTransientDispatchState, error) {
	accountID, err0 := requireText(accountIDInput, "accountId")
	if err0 != nil {
		return nil, err0
	}
	fingerprints := uniqueNonEmpty(keyFingerprintsInput)
	if len(fingerprints) == 0 {
		return []AccountApiKeyTransientDispatchState{}, nil
	}
	redisKeys := make([]string, 0, len(fingerprints))
	identities := make([]map[string]string, 0, len(fingerprints))
	for _, fingerprint := range fingerprints {
		redisKeys = append(redisKeys, s.stateKey(AccountApiKeyTransientTarget{AccountID: accountID, KeyFingerprint: fingerprint}))
		identities = append(identities, map[string]string{
			"accountId":      accountID,
			"keyFingerprint": fingerprint,
			"generation":     newUUID(),
		})
	}
	client, err := s.clientForUse(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := client.Eval(ctx, redisAccountApiKeyTransientLoadScript, redisKeys,
		fmt.Sprintf("%d", s.stateTtlMs),
		mustJSON(identities),
	).Result()
	if err != nil {
		return nil, err
	}
	rawText, ok := raw.(string)
	if !ok {
		return nil, errors.New("Redis API Key transient load 返回值无效")
	}
	var payload struct {
		States []AccountApiKeyTransientDispatchState `json:"states"`
	}
	if err := json.Unmarshal([]byte(rawText), &payload); err != nil || payload.States == nil {
		return nil, errors.New("Redis API Key transient load 结构无效")
	}
	states := make([]AccountApiKeyTransientDispatchState, 0, len(payload.States))
	fingerprintSet := map[string]struct{}{}
	for _, fingerprint := range fingerprints {
		fingerprintSet[fingerprint] = struct{}{}
	}
	for _, item := range payload.States {
		if item.State == nil {
			continue
		}
		state, stateErr := parseTransientStateValue(*item.State)
		if stateErr != nil || state.AccountID != accountID {
			continue
		}
		if _, known := fingerprintSet[state.KeyFingerprint]; !known {
			continue
		}
		states = append(states, AccountApiKeyTransientDispatchState{State: state, Suppressed: item.Suppressed})
	}
	return states, nil
}

// DeleteManyForTest removes the raw state keys (deleteManyForTest).
func (s *RedisAccountApiKeyTransientStateStore) DeleteManyForTest(ctx context.Context, targets []AccountApiKeyTransientTarget) error {
	keys := make([]string, 0, len(targets))
	for _, target := range targets {
		normalized, err := normalizeTransientTarget(target)
		if err != nil {
			return err
		}
		keys = append(keys, s.stateKey(normalized))
	}
	if len(keys) == 0 {
		return nil
	}
	client, err := s.clientForUse(ctx)
	if err != nil {
		return err
	}
	return client.Del(ctx, keys...).Err()
}

// SetRawStateForTest writes a raw value (setRawStateForTest).
func (s *RedisAccountApiKeyTransientStateStore) SetRawStateForTest(ctx context.Context, target AccountApiKeyTransientTarget, rawValue string) error {
	client, err := s.clientForUse(ctx)
	if err != nil {
		return err
	}
	normalized, err := normalizeTransientTarget(target)
	if err != nil {
		return err
	}
	return client.Set(ctx, s.stateKey(normalized), rawValue, time.Duration(s.stateTtlMs)*time.Millisecond).Err()
}

// redisAccountApiKeyTransientMutationScript mirrors
// redisAccountApiKeyTransientMutationScript byte for byte.
const redisAccountApiKeyTransientMutationScript = `
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

// redisAccountApiKeyTransientLoadScript mirrors
// redisAccountApiKeyTransientLoadScript byte for byte.
const redisAccountApiKeyTransientLoadScript = `
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

// parseTransientStateValue mirrors parseStateValue.
func parseTransientStateValue(candidate AccountApiKeyTransientState) (*AccountApiKeyTransientState, error) {
	if candidate.SchemaVersion != 1 ||
		candidate.AccountID == "" ||
		candidate.KeyFingerprint == "" ||
		candidate.Generation == "" ||
		candidate.LastObservedAtMs < 0 ||
		candidate.LastObservedAtMs > safeIntegerMax ||
		(candidate.ObservationKind != "failure" && candidate.ObservationKind != "success") ||
		candidate.FailureCount < 0 ||
		(candidate.KeyIndex != nil && (*candidate.KeyIndex < 0 || int64(*candidate.KeyIndex) > safeIntegerMax)) {
		return nil, errors.New("无效 transient state")
	}
	if candidate.ObservationKind == "failure" {
		if candidate.Status != APIKeyStatusTemporaryUnavailable && candidate.Status != APIKeyStatusRateLimited && candidate.Status != APIKeyStatusError {
			return nil, errors.New("无效 transient state")
		}
		if candidate.SuppressUntilMs == nil || *candidate.SuppressUntilMs < 0 {
			return nil, errors.New("无效 transient state")
		}
	}
	return &candidate, nil
}

func normalizeTransientTarget(target AccountApiKeyTransientTarget) (AccountApiKeyTransientTarget, error) {
	accountID, err := requireText(target.AccountID, "accountId")
	if err != nil {
		return AccountApiKeyTransientTarget{}, err
	}
	fingerprint, err := requireText(target.KeyFingerprint, "keyFingerprint")
	if err != nil {
		return AccountApiKeyTransientTarget{}, err
	}
	normalized := AccountApiKeyTransientTarget{AccountID: accountID, KeyFingerprint: fingerprint}
	if target.KeyIndex != nil {
		if *target.KeyIndex < 0 {
			return AccountApiKeyTransientTarget{}, errors.New("keyIndex 必须是非负整数")
		}
		index := *target.KeyIndex
		normalized.KeyIndex = &index
	}
	return normalized, nil
}

var redisKeyUnsafePart = regexp.MustCompile(`[^a-zA-Z0-9:_-]`)

func sanitizeRedisKeyPart(value string) string {
	normalized := strings.TrimSpace(value)
	normalized = redisKeyUnsafePart.ReplaceAllString(normalized, "_")
	normalized = strings.Trim(normalized, "_")
	if normalized == "" {
		return "default"
	}
	return normalized
}

func requireText(value string, name string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", fmt.Errorf("%s 不能为空", name)
	}
	return normalized, nil
}

func uniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func orDefaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func newUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

