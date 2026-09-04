package gatewayaccounteffects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// KeyModelRedisGroup mirrors the redis group name shared with Node
// (redisNamespacedKey('gateway-account-circuit-key-model')) and with
// jobs/internal/keymodelrecovery (redisGroup).
const KeyModelRedisGroup = "gateway-account-circuit-key-model"

// KeyModelRedisKeys mirrors KeyModelRedisKeys. The layout overlaps the
// jobs/keymodelrecovery RedisKeys for the state/due/closed family (same
// canonical hash, same state JSON); receipt/admission/wake/fence keys are
// gateway-runtime-only extensions.
type KeyModelRedisKeys struct {
	Prefix string
}

var keyModelNamespacePartPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// NewKeyModelRedisKeys builds the key family; the namespace follows the
// `juhe-ai:<namespace>:<group>` convention shared with the jobs runner.
func NewKeyModelRedisKeys(namespace string) (KeyModelRedisKeys, error) {
	if !keyModelNamespacePartPattern.MatchString(strings.TrimSpace(namespace)) {
		return KeyModelRedisKeys{}, errors.New("Redis namespace 无效")
	}
	return KeyModelRedisKeys{Prefix: "juhe-ai:" + strings.TrimSpace(namespace) + ":" + KeyModelRedisGroup}, nil
}

// State mirrors keys.state(hash).
func (k KeyModelRedisKeys) State(hash string) string { return k.Prefix + ":state:" + hash }

// Due mirrors keys.due.
func (k KeyModelRedisKeys) Due() string { return k.Prefix + ":due" }

// Closed mirrors keys.closed.
func (k KeyModelRedisKeys) Closed() string { return k.Prefix + ":closed" }

// Receipt mirrors keys.receipt(intentId).
func (k KeyModelRedisKeys) Receipt(intentID string) string { return k.Prefix + ":receipt:" + intentID }

// Admission mirrors keys.admission(hash).
func (k KeyModelRedisKeys) Admission(hash string) string { return k.Prefix + ":admission:" + hash }

// AdmissionLease mirrors keys.admissionLease(hash, attemptId).
func (k KeyModelRedisKeys) AdmissionLease(hash, attemptID string) string {
	return k.Prefix + ":admissionLease:" + hash + ":" + attemptID
}

// AdmissionWake mirrors keys.admissionWake(hash).
func (k KeyModelRedisKeys) AdmissionWake(hash string) string { return k.Prefix + ":admissionWake:" + hash }

// MainProbeFence mirrors keys.mainProbeFence(hash).
func (k KeyModelRedisKeys) MainProbeFence(hash string) string {
	return k.Prefix + ":mainProbeFence:" + hash
}

// J1Confirmation mirrors keys.j1Confirmation(sourceHash, revision).
func (k KeyModelRedisKeys) J1Confirmation(sourceHash string, revision int64) string {
	return k.Prefix + ":j1Confirmation:" + sourceHash + ":" + strconv.FormatInt(revision, 10)
}

// AdmissionEvents mirrors keys.admissionEvents.
func (k KeyModelRedisKeys) AdmissionEvents() string { return k.Prefix + ":admission-events" }

// Capacity mirrors keys.capacity.
func (k KeyModelRedisKeys) Capacity() string { return k.Prefix + ":capacity" }

// KeyModelRedisStoreOptions carries the constructor arguments.
type KeyModelRedisStoreOptions struct {
	RedisURL  string
	Namespace string
	// EvalRunner replaces EVAL in tests (mirrors the Node evalRunner hook).
	EvalRunner func(script string, keys []string, args []string) (any, error)
}

// RedisKeyModelRuntimeStore mirrors RedisKeyModelRuntimeStore.
type RedisKeyModelRuntimeStore struct {
	redisURL   string
	keys       KeyModelRedisKeys
	evalRunner func(script string, keys []string, args []string) (any, error)

	once   sync.Once
	client *redis.Client
	opts   *redis.Options
	err    error
}

// NewRedisKeyModelRuntimeStore mirrors the constructor.
func NewRedisKeyModelRuntimeStore(options KeyModelRedisStoreOptions) (*RedisKeyModelRuntimeStore, error) {
	redisURL, err := requireKeyModelText(options.RedisURL, "redisUrl")
	if err != nil {
		return nil, errors.New("启用 Key-model Redis state 必须配置 JUHE_AI_REDIS_STATE_URL")
	}
	keys, err := NewKeyModelRedisKeys(options.Namespace)
	if err != nil {
		return nil, err
	}
	return &RedisKeyModelRuntimeStore{
		redisURL:   redisURL,
		keys:       keys,
		evalRunner: options.EvalRunner,
	}, nil
}

// Keys exposes the key family.
func (s *RedisKeyModelRuntimeStore) Keys() KeyModelRedisKeys { return s.keys }

// Close releases the underlying client.
func (s *RedisKeyModelRuntimeStore) Close() error {
	s.once.Do(func() {})
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

func (s *RedisKeyModelRuntimeStore) clientForUse(ctx context.Context) (*redis.Client, error) {
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

// Get implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) Get(ctx context.Context, capability CapabilityKey) (*KeyModelState, error) {
	hash, err := CapabilityHash(capability)
	if err != nil {
		return nil, err
	}
	value, err := s.readWithSingleRetry(ctx, s.keys.State(hash))
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	state, err := parseKeyModelState(*value, hash, capability.DispatchRevision)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// RecordFailure implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) RecordFailure(ctx context.Context, intent KeyModelFailureIntent) (KeyModelFailureResult, error) {
	if err := validateFailureIntent(intent); err != nil {
		return KeyModelFailureResult{}, err
	}
	state, err := CreateKeyModelOpenState(intent.Capability, intent.ObservedAtMs)
	if err != nil {
		return KeyModelFailureResult{}, err
	}
	hash := state.CapabilityHash
	if intent.Permit != nil && intent.Permit.CapabilityHash != hash {
		return KeyModelFailureResult{}, errors.New("Key-model 失败意图 permit 与 CapabilityKey 不匹配")
	}
	attemptID := intent.AttemptID
	if intent.Permit != nil {
		attemptID = intent.Permit.AttemptID
	}
	result, err := s.evalWithSingleRetry(ctx, "Key-model 失败意图写入", recordKeyModelFailureScript, []string{
		s.keys.State(state.CapabilityHash),
		s.keys.Due(),
		s.keys.Receipt(intent.IntentID),
		s.keys.Capacity(),
		s.keys.Admission(hash),
		s.keys.AdmissionLease(hash, attemptID),
		s.keys.AdmissionWake(hash),
		s.keys.AdmissionEvents(),
		s.keys.Closed(),
	}, []string{
		string(mustJSON(state)),
		strconv.FormatInt(intent.Capability.DispatchRevision, 10),
		"50000",
		strconv.FormatInt(5*60_000, 10),
		attemptID,
		hash,
	})
	if err != nil {
		return KeyModelFailureResult{}, err
	}
	array, err := redisArray(result)
	if err != nil {
		return KeyModelFailureResult{}, err
	}
	status, err := redisString(array[0])
	if err != nil {
		return KeyModelFailureResult{}, err
	}
	switch status {
	case "capacity_exhausted", "stale":
		return KeyModelFailureResult{Status: KeyModelMutationStatus(status)}, nil
	case "applied", "idempotent":
	default:
		return KeyModelFailureResult{}, fmt.Errorf("Key-model 失败意图返回未知状态：%s", status)
	}
	parsed, err := parseKeyModelState(redisValueString(array[1]), state.CapabilityHash, intent.Capability.DispatchRevision)
	if err != nil {
		return KeyModelFailureResult{}, err
	}
	return KeyModelFailureResult{Status: KeyModelMutationStatus(status), State: &parsed, Applied: status == "applied"}, nil
}

// AdmitForeground implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) AdmitForeground(ctx context.Context, capability CapabilityKey, attemptID string) (KeyModelAdmissionResult, error) {
	hash, err := CapabilityHash(capability)
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	normalizedAttemptID, err := requireKeyModelText(attemptID, "attemptId")
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	result, err := s.evalWithSingleRetry(ctx, "Key-model foreground admission", admitKeyModelForegroundScript, []string{
		s.keys.State(hash),
		s.keys.Admission(hash),
		s.keys.AdmissionLease(hash, normalizedAttemptID),
		s.keys.AdmissionWake(hash),
		s.keys.MainProbeFence(hash),
	}, []string{
		normalizedAttemptID,
		strconv.FormatInt(KeyModelForegroundPrecommitLeaseMs, 10),
		strconv.Itoa(KeyModelForegroundLimit),
		strconv.FormatInt(capability.DispatchRevision, 10),
	})
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	array, err := redisArray(result)
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	status, err := redisString(array[0])
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	wakeSequence, err := finiteRedisInteger(array[1])
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	if status == "busy" || status == "blocked" {
		return KeyModelAdmissionResult{Status: KeyModelForegroundDecision(status), WakeSequence: wakeSequence}, nil
	}
	if status != "admitted" && status != "idempotent" {
		return KeyModelAdmissionResult{}, fmt.Errorf("Key-model admission 返回未知状态：%s", status)
	}
	leaseUntilMs, err := finiteRedisInteger(array[2])
	if err != nil {
		return KeyModelAdmissionResult{}, err
	}
	return KeyModelAdmissionResult{
		Status: ForegroundAdmitted,
		Permit: &KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: normalizedAttemptID, LeaseUntilMs: leaseUntilMs},
	}, nil
}

// ReleaseForeground implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) ReleaseForeground(ctx context.Context, permit KeyModelForegroundPermit) (bool, error) {
	hash, err := requiredCapabilityHash(permit.CapabilityHash)
	if err != nil {
		return false, err
	}
	attemptID, err := requireKeyModelText(permit.AttemptID, "attemptId")
	if err != nil {
		return false, err
	}
	result, err := s.evalWithSingleRetry(ctx, "Key-model foreground permit 释放", releaseKeyModelForegroundScript, []string{
		s.keys.Admission(hash),
		s.keys.AdmissionLease(hash, attemptID),
		s.keys.AdmissionWake(hash),
		s.keys.AdmissionEvents(),
	}, []string{hash, attemptID})
	if err != nil {
		return false, err
	}
	array, err := redisArray(result)
	if err != nil {
		return false, err
	}
	value, err := finiteRedisInteger(array[0])
	if err != nil {
		return false, err
	}
	return value == 1, nil
}

// RenewForeground implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) RenewForeground(ctx context.Context, permit KeyModelForegroundPermit) (*KeyModelForegroundPermit, error) {
	hash, err := requiredCapabilityHash(permit.CapabilityHash)
	if err != nil {
		return nil, err
	}
	attemptID, err := requireKeyModelText(permit.AttemptID, "attemptId")
	if err != nil {
		return nil, err
	}
	result, err := s.evalWithSingleRetry(ctx, "Key-model foreground permit 续租", renewKeyModelForegroundScript, []string{
		s.keys.Admission(hash),
		s.keys.AdmissionLease(hash, attemptID),
	}, []string{attemptID, strconv.FormatInt(KeyModelForegroundPrecommitLeaseMs, 10)})
	if err != nil {
		return nil, err
	}
	array, err := redisArray(result)
	if err != nil {
		return nil, err
	}
	status, err := redisString(array[0])
	if err != nil {
		return nil, err
	}
	if status == "lost" {
		return nil, nil
	}
	if status != "renewed" {
		return nil, fmt.Errorf("Key-model foreground 续租返回未知状态：%s", status)
	}
	leaseUntilMs, err := finiteRedisInteger(array[1])
	if err != nil {
		return nil, err
	}
	return &KeyModelForegroundPermit{CapabilityHash: hash, AttemptID: attemptID, LeaseUntilMs: leaseUntilMs}, nil
}

// RecordMainProbeFailure implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) RecordMainProbeFailure(ctx context.Context, capability CapabilityKey, permit KeyModelForegroundPermit) error {
	hash, err := CapabilityHash(capability)
	if err != nil {
		return err
	}
	if permit.CapabilityHash != hash {
		return errors.New("MainProbe fence permit 与 CapabilityKey 不匹配")
	}
	_, err = s.evalWithSingleRetry(ctx, "MainProbe foreground fence 写入", recordMainProbeFenceScript, []string{
		s.keys.MainProbeFence(hash),
		s.keys.Admission(hash),
		s.keys.AdmissionLease(hash, permit.AttemptID),
		s.keys.AdmissionWake(hash),
		s.keys.AdmissionEvents(),
	}, []string{permit.AttemptID, hash, "90000"})
	return err
}

// ClearMainProbeFence implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) ClearMainProbeFence(ctx context.Context, fence KeyModelFenceReference, winnerKeyFingerprint string) (bool, error) {
	hash, err := requiredCapabilityHash(fence.CapabilityHash)
	if err != nil {
		return false, err
	}
	keyFingerprint, err := requireKeyModelText(fence.KeyFingerprint, "keyFingerprint")
	if err != nil {
		return false, err
	}
	winner, err := requireKeyModelText(winnerKeyFingerprint, "winnerKeyFingerprint")
	if err != nil {
		return false, err
	}
	if keyFingerprint != winner {
		return false, nil
	}
	if !isSafeInteger(fence.DispatchRevision) || fence.DispatchRevision < 1 {
		return false, nil
	}
	ownerID, err := requireKeyModelText(fence.OwnerID, "ownerId")
	if err != nil {
		return false, err
	}
	result, err := s.evalWithSingleRetry(ctx, "MainProbe fence 清理", clearMainProbeFenceScript, []string{
		s.keys.MainProbeFence(hash),
	}, []string{ownerID})
	if err != nil {
		return false, err
	}
	array, err := redisArray(result)
	if err != nil {
		return false, err
	}
	value, err := finiteRedisInteger(array[0])
	if err != nil {
		return false, err
	}
	return value == 1, nil
}

// DeferMainProbeFence implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) DeferMainProbeFence(ctx context.Context, fence KeyModelFenceReference) (bool, error) {
	hash, err := requiredCapabilityHash(fence.CapabilityHash)
	if err != nil {
		return false, err
	}
	ownerID, err := requireKeyModelText(fence.OwnerID, "ownerId")
	if err != nil {
		return false, err
	}
	result, err := s.evalWithSingleRetry(ctx, "MainProbe fence unknown 延后", deferMainProbeFenceScript, []string{
		s.keys.MainProbeFence(hash),
	}, []string{ownerID, strconv.FormatInt(KeyModelMainProbeUnknownRetryMs, 10)})
	if err != nil {
		return false, err
	}
	array, err := redisArray(result)
	if err != nil {
		return false, err
	}
	value, err := finiteRedisInteger(array[0])
	if err != nil {
		return false, err
	}
	return value == 1, nil
}

// ClaimJ1Confirmation implements KeyModelRuntimeStore.
func (s *RedisKeyModelRuntimeStore) ClaimJ1Confirmation(ctx context.Context, sourceAccountID string, dispatchRevision int64) (bool, error) {
	if _, err := requireKeyModelText(sourceAccountID, "credentialSourceAccountId"); err != nil {
		return false, err
	}
	sourceHash, err := CapabilityHash(CapabilityKey{
		CredentialSourceAccountID: sourceAccountID,
		KeyFingerprint:            "j1-confirmation",
		ClientModel:               "j1-confirmation",
		ClientEndpointFamily:      "j1-confirmation",
		FinalUpstreamModel:        "j1-confirmation",
		UpstreamEndpointMode:      "j1-confirmation",
		DispatchRevision:          dispatchRevision,
	})
	if err != nil {
		return false, err
	}
	result, err := s.evalWithSingleRetry(ctx, "Key-model J1 confirmation 限频", claimJ1ConfirmationScript, []string{
		s.keys.J1Confirmation(sourceHash, dispatchRevision),
	}, []string{strconv.FormatInt(2*60_000, 10)})
	if err != nil {
		return false, err
	}
	array, err := redisArray(result)
	if err != nil {
		return false, err
	}
	status, err := redisString(array[0])
	if err != nil {
		return false, err
	}
	return status == "claimed", nil
}

func (s *RedisKeyModelRuntimeStore) readWithSingleRetry(ctx context.Context, key string) (*string, error) {
	client, err := s.clientForUse(ctx)
	if err != nil {
		return nil, err
	}
	value, firstErr := client.Get(ctx, key).Result()
	if firstErr == nil {
		return &value, nil
	}
	if errors.Is(firstErr, redis.Nil) {
		return nil, nil
	}
	time.Sleep(50 * time.Millisecond)
	value, secondErr := client.Get(ctx, key).Result()
	if secondErr == nil {
		return &value, nil
	}
	if errors.Is(secondErr, redis.Nil) {
		return nil, nil
	}
	return nil, fmt.Errorf("Key-model Redis state 连续两次读取失败: %v; %v", firstErr, secondErr)
}

func (s *RedisKeyModelRuntimeStore) evalWithSingleRetry(ctx context.Context, operationName, script string, keys, args []string) (any, error) {
	result, firstErr := s.eval(ctx, script, keys, args)
	if firstErr == nil {
		return result, nil
	}
	time.Sleep(50 * time.Millisecond)
	result, secondErr := s.eval(ctx, script, keys, args)
	if secondErr == nil {
		return result, nil
	}
	return nil, fmt.Errorf("%s连续两次失败: %v; %v", operationName, firstErr, secondErr)
}

func (s *RedisKeyModelRuntimeStore) eval(ctx context.Context, script string, keys, args []string) (any, error) {
	if s.evalRunner != nil {
		return s.evalRunner(script, keys, args)
	}
	client, err := s.clientForUse(ctx)
	if err != nil {
		return nil, err
	}
	deadline := time.Duration(KeyModelForegroundRedisOperationTimeoutMs) * time.Millisecond
	evalCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	return client.Eval(evalCtx, script, keys, toAnySlice(args)).Result()
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

// recordKeyModelFailureScript mirrors recordKeyModelFailureScript byte for
// byte; it is the executable contract shared with in-flight Node processes.
const recordKeyModelFailureScript = `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local incoming = cjson.decode(ARGV[1])
incoming.lastObservedAtMs = now
incoming.retryAtMs = now + 5000
local function releasePermit()
  if redis.call('DEL', KEYS[6]) == 0 then return end
  redis.call('ZREM', KEYS[5], ARGV[5])
  local wake = redis.call('INCR', KEYS[7])
  redis.call('PUBLISH', KEYS[8], ARGV[6] .. ':' .. tostring(wake))
end
local receipt = redis.call('GET', KEYS[3])
if receipt then
  releasePermit()
  local current = redis.call('GET', KEYS[1])
  return {'idempotent', current or receipt}
end
local currentRaw = redis.call('GET', KEYS[1])
if currentRaw then
  local current = cjson.decode(currentRaw)
  if tonumber(current.dispatchRevision) > tonumber(ARGV[2]) then releasePermit(); return {'stale', ''} end
  if tonumber(current.dispatchRevision) == tonumber(ARGV[2]) and current.phase ~= 'CLOSED' then
    current.lastObservedAtMs = now
    current.lastOutcome = 'upstream_not_complete'
    local encoded = cjson.encode(current)
    redis.call('SET', KEYS[1], encoded)
    redis.call('SET', KEYS[3], encoded, 'PX', ARGV[4])
    releasePermit()
    return {'idempotent', encoded}
  end
  incoming.generation = tonumber(current.generation or 0) + 1
elseif tonumber(redis.call('GET', KEYS[4]) or '0') >= tonumber(ARGV[3]) then
  local removed = 0
  local closed = redis.call('ZRANGE', KEYS[9], 0, 999)
  for _, hash in ipairs(closed) do
    local closedState = redis.call('GET', string.gsub(KEYS[1], '[^:]+$', hash))
    if closedState and cjson.decode(closedState).phase == 'CLOSED' then
      redis.call('DEL', string.gsub(KEYS[1], '[^:]+$', hash))
      redis.call('ZREM', KEYS[2], hash)
      redis.call('DECR', KEYS[4])
      removed = removed + 1
      if tonumber(redis.call('GET', KEYS[4]) or '0') < tonumber(ARGV[3]) then break end
    end
    redis.call('ZREM', KEYS[9], hash)
  end
  if tonumber(redis.call('GET', KEYS[4]) or '0') >= tonumber(ARGV[3]) then
    releasePermit()
    return {'capacity_exhausted', ''}
  end
  redis.call('INCR', KEYS[4])
else
  redis.call('INCR', KEYS[4])
end
local encoded = cjson.encode(incoming)
redis.call('SET', KEYS[1], encoded)
redis.call('ZADD', KEYS[2], incoming.retryAtMs, incoming.capabilityHash)
redis.call('SET', KEYS[3], encoded, 'PX', ARGV[4])
releasePermit()
return {'applied', encoded}
`

// clearMainProbeFenceScript mirrors clearMainProbeFenceScript.
const clearMainProbeFenceScript = `
local current = redis.call('GET', KEYS[1])
if not current then return {0} end
if current ~= ARGV[1] then return {0} end
redis.call('DEL', KEYS[1])
return {1}
`

// deferMainProbeFenceScript mirrors deferMainProbeFenceScript.
const deferMainProbeFenceScript = `
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then return {0} end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return {1}
`

// admitKeyModelForegroundScript mirrors admitKeyModelForegroundScript.
const admitKeyModelForegroundScript = `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local existing = redis.call('GET', KEYS[3])
if existing then return {'idempotent', redis.call('GET', KEYS[4]) or '0', existing} end
local stateRaw = redis.call('GET', KEYS[1])
if stateRaw then
  local state = cjson.decode(stateRaw)
  if tonumber(state.dispatchRevision) == tonumber(ARGV[4]) and state.phase ~= 'CLOSED' then
    return {'blocked', redis.call('GET', KEYS[4]) or '0', '0'}
  end
end
if redis.call('EXISTS', KEYS[5]) == 1 then return {'blocked', redis.call('GET', KEYS[4]) or '0', '0'} end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
local count = tonumber(redis.call('ZCARD', KEYS[2]) or '0')
if count >= tonumber(ARGV[3]) then return {'busy', redis.call('GET', KEYS[4]) or '0', '0'} end
local leaseUntil = now + tonumber(ARGV[2])
redis.call('SET', KEYS[3], leaseUntil, 'PX', ARGV[2])
redis.call('ZADD', KEYS[2], leaseUntil, ARGV[1])
redis.call('PEXPIRE', KEYS[2], ARGV[2])
return {'admitted', redis.call('GET', KEYS[4]) or '0', tostring(leaseUntil)}
`

// releaseKeyModelForegroundScript mirrors releaseKeyModelForegroundScript.
const releaseKeyModelForegroundScript = `
if redis.call('DEL', KEYS[2]) == 0 then return {0, redis.call('GET', KEYS[3]) or '0'} end
redis.call('ZREM', KEYS[1], ARGV[2])
local wake = redis.call('INCR', KEYS[3])
redis.call('PUBLISH', KEYS[4], ARGV[1] .. ':' .. tostring(wake))
return {1, wake}
`

// renewKeyModelForegroundScript mirrors renewKeyModelForegroundScript.
const renewKeyModelForegroundScript = `
local existing = redis.call('GET', KEYS[2])
if not existing then return {'lost', '0'} end
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local leaseUntil = now + tonumber(ARGV[2])
redis.call('SET', KEYS[2], leaseUntil, 'PX', ARGV[2])
redis.call('ZADD', KEYS[1], leaseUntil, ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return {'renewed', tostring(leaseUntil)}
`

// recordMainProbeFenceScript mirrors recordMainProbeFenceScript.
const recordMainProbeFenceScript = `
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
if redis.call('DEL', KEYS[3]) == 1 then redis.call('ZREM', KEYS[2], ARGV[1]) end
local wake = redis.call('INCR', KEYS[4])
redis.call('PUBLISH', KEYS[5], ARGV[2] .. ':' .. tostring(wake))
return {'applied', wake}
`

// claimJ1ConfirmationScript mirrors claimJ1ConfirmationScript.
const claimJ1ConfirmationScript = `
if redis.call('SET', KEYS[1], '1', 'NX', 'PX', ARGV[1]) then return {'claimed'} end
return {'limited'}
`

// keyModelStateWire mirrors the Lua cjson payload: cjson omits nil fields and
// writes numbers as numbers, matching the omitempty JSON tags of KeyModelState.
func parseKeyModelState(value string, expectedHash string, expectedRevision int64) (KeyModelState, error) {
	var parsed KeyModelState
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return KeyModelState{}, err
	}
	if parsed.CapabilityHash != expectedHash || parsed.DispatchRevision != expectedRevision {
		return KeyModelState{}, errors.New("Key-model Redis state 完整性校验失败")
	}
	return parsed, nil
}

func redisArray(value any) ([]any, error) {
	array, ok := value.([]any)
	if !ok {
		return nil, errors.New("Key-model Redis 返回值必须为数组")
	}
	return array, nil
}

func redisString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	default:
		return "", fmt.Errorf("Key-model Redis 返回值必须为字符串：%v", value)
	}
}

func redisValueString(value any) string {
	if text, err := redisString(value); err == nil {
		return text
	}
	return ""
}

func finiteRedisInteger(value any) (int64, error) {
	var normalized float64
	switch typed := value.(type) {
	case int64:
		normalized = float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, fmt.Errorf("Key-model Redis 数字结果无效：%v", value)
		}
		normalized = parsed
	default:
		return 0, fmt.Errorf("Key-model Redis 数字结果无效：%v", value)
	}
	if normalized < 0 || normalized != float64(int64(normalized)) || normalized > float64(safeIntegerMax) {
		return 0, fmt.Errorf("Key-model Redis 数字结果无效：%v", value)
	}
	return int64(normalized), nil
}
