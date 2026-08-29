package keymodelruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }
type RedisStore struct {
	client *redis.Client
	prefix string
	gate   OwnerGate
}

func NewRedisStore(url, namespace string, gate OwnerGate) (*RedisStore, error) {
	if strings.TrimSpace(url) == "" || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("key-model Redis URL and namespace are required")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse key-model Redis URL: %w", err)
	}
	client := redis.NewClient(options)
	normalizedNamespace := strings.TrimRight(strings.TrimSpace(namespace), ":")
	// Node's redisNamespacedKey always emits juhe-ai:<namespace>:<key>.
	// Accept both the documented full prefix and the short namespace used by
	// isolated smoke tests, but never create a second juhe-ai: prefix.
	if !strings.HasPrefix(normalizedNamespace, "juhe-ai:") {
		normalizedNamespace = "juhe-ai:" + normalizedNamespace
	}
	return &RedisStore{client: client, prefix: normalizedNamespace + ":gateway-account-circuit-key-model", gate: gate}, nil
}
func (s *RedisStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}
func (s *RedisStore) Ping(ctx context.Context) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	return s.client.Ping(ctx).Err()
}

func (s *RedisStore) ServerNow(ctx context.Context) (time.Time, error) {
	if err := s.requireOwner(); err != nil {
		return time.Time{}, err
	}
	value, err := s.client.Time(ctx).Result()
	return value.UTC(), err
}
func (s *RedisStore) requireOwner() error {
	if s == nil || s.client == nil {
		return errors.New("key-model Redis owner is not initialized")
	}
	if !s.gate.Confirmed || !s.gate.SchemaReady || !s.gate.NodeWriterStopped {
		return errors.New("key-model Redis owner gate is not confirmed")
	}
	return nil
}

func (s *RedisStore) key(kind string, values ...string) string {
	if len(values) == 0 || values[0] == "" {
		return s.prefix + ":" + kind
	}
	return s.prefix + ":" + kind + ":" + values[0]
}
func (s *RedisStore) stateKey(hash string) string     { return s.key("state", hash) }
func (s *RedisStore) admissionKey(hash string) string { return s.key("admission", hash) }
func (s *RedisStore) admissionLeaseKey(hash, attempt string) string {
	return s.key("admissionLease", hash+":"+attempt)
}
func (s *RedisStore) wakeKey(hash string) string             { return s.key("admissionWake", hash) }
func (s *RedisStore) sourceRecoveryKey(source string) string { return s.key("recovery:source", source) }

func (s *RedisStore) Get(ctx context.Context, capability Capability) (State, bool, error) {
	if err := s.requireOwner(); err != nil {
		return State{}, false, err
	}
	hash, err := HashCapability(capability)
	if err != nil {
		return State{}, false, err
	}
	raw, err := s.client.Get(ctx, s.stateKey(hash)).Result()
	if errors.Is(err, redis.Nil) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	state, err := decodeRedisState(raw)
	if err != nil {
		return State{}, false, fmt.Errorf("decode key-model state: %w", err)
	}
	if state.CapabilityHash != hash || state.DispatchRevision != capability.DispatchRevision {
		return State{}, false, errors.New("key-model Redis state fence mismatch")
	}
	return state, true, nil
}

func (s *RedisStore) AdmitForeground(ctx context.Context, capability Capability, attemptID string) (ForegroundDecision, ForegroundPermit, uint64, error) {
	if err := s.requireOwner(); err != nil {
		return "", ForegroundPermit{}, 0, err
	}
	hash, err := HashCapability(capability)
	if err != nil {
		return "", ForegroundPermit{}, 0, err
	}
	if strings.TrimSpace(attemptID) == "" {
		return "", ForegroundPermit{}, 0, errors.New("attempt id is required")
	}
	result, err := admitScript.Run(ctx, s.client, []string{s.stateKey(hash), s.admissionKey(hash), s.admissionLeaseKey(hash, attemptID), s.wakeKey(hash), s.key("mainProbeFence", hash)}, attemptID, "90000", strconv.Itoa(ForegroundLimit), strconv.FormatInt(capability.DispatchRevision, 10)).Result()
	if err != nil {
		return "", ForegroundPermit{}, 0, err
	}
	values, ok := result.([]interface{})
	if !ok || len(values) < 3 {
		return "", ForegroundPermit{}, 0, errors.New("invalid key-model admission result")
	}
	wake, err := parseUint(values[1])
	if err != nil {
		return "", ForegroundPermit{}, 0, err
	}
	switch status := stringValue(values[0]); status {
	case "busy":
		return ForegroundBusy, ForegroundPermit{}, wake, nil
	case "blocked":
		return ForegroundBlocked, ForegroundPermit{}, wake, nil
	case "admitted", "idempotent":
		until, err := parseInt64(values[2])
		if err != nil {
			return "", ForegroundPermit{}, 0, err
		}
		return ForegroundAdmitted, ForegroundPermit{CapabilityHash: hash, AttemptID: attemptID, LeaseUntil: time.UnixMilli(until).UTC()}, wake, nil
	default:
		return "", ForegroundPermit{}, 0, fmt.Errorf("unknown key-model admission status %q", status)
	}
}

func (s *RedisStore) ReleaseForeground(ctx context.Context, permit ForegroundPermit) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	result, err := releaseScript.Run(ctx, s.client, []string{s.admissionKey(permit.CapabilityHash), s.admissionLeaseKey(permit.CapabilityHash, permit.AttemptID), s.wakeKey(permit.CapabilityHash), s.key("admission-events")}, permit.CapabilityHash, permit.AttemptID).Result()
	if err != nil {
		return false, err
	}
	values, ok := result.([]interface{})
	if !ok || len(values) == 0 {
		return false, errors.New("invalid key-model release result")
	}
	value, err := parseInt64(values[0])
	return value == 1, err
}

func (s *RedisStore) RenewForeground(ctx context.Context, permit ForegroundPermit) (ForegroundPermit, bool, error) {
	if err := s.requireOwner(); err != nil {
		return ForegroundPermit{}, false, err
	}
	result, err := renewScript.Run(ctx, s.client, []string{s.admissionKey(permit.CapabilityHash), s.admissionLeaseKey(permit.CapabilityHash, permit.AttemptID)}, permit.AttemptID, "90000").Result()
	if err != nil {
		return ForegroundPermit{}, false, err
	}
	values, ok := result.([]interface{})
	if !ok || len(values) < 2 {
		return ForegroundPermit{}, false, errors.New("invalid key-model renew result")
	}
	if stringValue(values[0]) == "lost" {
		return ForegroundPermit{}, false, nil
	}
	until, err := parseInt64(values[1])
	if err != nil {
		return ForegroundPermit{}, false, err
	}
	return ForegroundPermit{CapabilityHash: permit.CapabilityHash, AttemptID: permit.AttemptID, LeaseUntil: time.UnixMilli(until).UTC()}, true, nil
}

func (s *RedisStore) RecordFailure(ctx context.Context, capability Capability, now time.Time, receiptID string) (MutationStatus, State, error) {
	return s.RecordFailureIntent(ctx, FailureIntent{
		IntentID: receiptID, RequestID: receiptID, AttemptID: receiptID,
		Capability: capability, ObservedAt: now,
	})
}

// FailureIntent keeps the durable receipt identity separate from the actual
// foreground attempt lease. Node releases the attempt permit atomically while
// recording the first failure; callers must provide Permit when one exists.
type FailureIntent struct {
	IntentID   string
	RequestID  string
	AttemptID  string
	Capability Capability
	ObservedAt time.Time
	Permit     *ForegroundPermit
}

func (s *RedisStore) RecordFailureIntent(ctx context.Context, intent FailureIntent) (MutationStatus, State, error) {
	if err := s.requireOwner(); err != nil {
		return "", State{}, err
	}
	if strings.TrimSpace(intent.IntentID) == "" || strings.TrimSpace(intent.RequestID) == "" || strings.TrimSpace(intent.AttemptID) == "" || intent.ObservedAt.IsZero() {
		return "", State{}, errors.New("key-model failure intent is incomplete")
	}
	state, err := Open(intent.Capability, intent.ObservedAt)
	if err != nil {
		return "", State{}, err
	}
	raw, err := encodeRedisState(state)
	if err != nil {
		return "", State{}, err
	}
	attemptID := intent.AttemptID
	if intent.Permit != nil {
		if intent.Permit.CapabilityHash != state.CapabilityHash {
			return "", State{}, errors.New("key-model failure permit capability mismatch")
		}
		if strings.TrimSpace(intent.Permit.AttemptID) == "" {
			return "", State{}, errors.New("key-model failure permit attempt id is required")
		}
		attemptID = intent.Permit.AttemptID
	}
	result, err := recordFailureScript.Run(ctx, s.client, []string{s.stateKey(state.CapabilityHash), s.key("due"), s.key("receipt", intent.IntentID), s.key("capacity"), s.admissionKey(state.CapabilityHash), s.admissionLeaseKey(state.CapabilityHash, attemptID), s.wakeKey(state.CapabilityHash), s.key("admission-events"), s.key("closed")}, string(raw), strconv.FormatInt(intent.Capability.DispatchRevision, 10), "100000", strconv.FormatInt((5*time.Minute).Milliseconds(), 10), attemptID, state.CapabilityHash).Result()
	if err != nil {
		return "", State{}, err
	}
	values, ok := result.([]interface{})
	if !ok || len(values) < 2 {
		return "", State{}, errors.New("invalid key-model failure result")
	}
	status := MutationStatus(stringValue(values[0]))
	if status == StatusStale {
		return status, State{}, nil
	}
	encoded := stringValue(values[1])
	var parsed State
	if encoded != "" {
		parsed, err = decodeRedisState(encoded)
		if err != nil {
			return "", State{}, err
		}
	}
	return status, parsed, nil
}

func (s *RedisStore) ListDue(ctx context.Context, now time.Time, limit int) ([]State, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	if limit < 1 {
		return nil, errors.New("key-model due limit must be positive")
	}
	hashes, err := s.client.ZRangeByScore(ctx, s.key("due"), &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(now.UnixMilli(), 10), Offset: 0, Count: int64(limit)}).Result()
	if err != nil {
		return nil, err
	}
	out := make([]State, 0, len(hashes))
	for _, hash := range hashes {
		raw, getErr := s.client.Get(ctx, s.stateKey(hash)).Result()
		if errors.Is(getErr, redis.Nil) {
			continue
		}
		if getErr != nil {
			return nil, getErr
		}
		state, decodeErr := decodeRedisState(raw)
		if decodeErr != nil {
			return nil, errors.New("invalid key-model due state")
		}
		if state.Phase == PhaseOpen || state.Phase == PhaseRecovering {
			out = append(out, state)
		}
	}
	return out, nil
}

func (s *RedisStore) RecordMainProbeFence(ctx context.Context, capability Capability, ownerID string, lease time.Duration) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	hash, err := HashCapability(capability)
	if err != nil {
		return err
	}
	if ownerID == "" || lease <= 0 {
		return errors.New("main-probe fence identity is invalid")
	}
	_, err = recordMainProbeFenceScript.Run(ctx, s.client, []string{
		s.key("mainProbeFence", hash),
		s.admissionKey(hash),
		s.admissionLeaseKey(hash, ownerID),
		s.wakeKey(hash),
		s.key("admission-events"),
	}, ownerID, hash, lease.Milliseconds()).Result()
	return err
}

func (s *RedisStore) ClearMainProbeFence(ctx context.Context, capability Capability, ownerID string) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	hash, err := HashCapability(capability)
	if err != nil {
		return false, err
	}
	result, err := clearFenceScript.Run(ctx, s.client, []string{s.key("mainProbeFence", hash)}, ownerID).Int()
	return result == 1, err
}

func (s *RedisStore) DeferMainProbeFence(ctx context.Context, capability Capability, ownerID string, retry time.Duration) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	hash, err := HashCapability(capability)
	if err != nil {
		return false, err
	}
	result, err := deferFenceScript.Run(ctx, s.client, []string{s.key("mainProbeFence", hash)}, ownerID, strconv.FormatInt(retry.Milliseconds(), 10)).Int()
	return result == 1, err
}

func (s *RedisStore) ClaimJ1Confirmation(ctx context.Context, sourceAccountID string, revision int64) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	if strings.TrimSpace(sourceAccountID) == "" || revision < 1 {
		return false, errors.New("J1 confirmation identity is invalid")
	}
	hash, err := HashCapability(Capability{CredentialSourceAccountID: sourceAccountID, KeyFingerprint: "j1-confirmation", ClientModel: "j1-confirmation", ClientEndpointFamily: "j1-confirmation", FinalUpstreamModel: "j1-confirmation", UpstreamEndpointMode: "j1-confirmation", DispatchRevision: revision})
	if err != nil {
		return false, err
	}
	result, err := s.client.SetNX(ctx, s.key("j1Confirmation", hash+":"+strconv.FormatInt(revision, 10)), "1", 2*time.Minute).Result()
	return result, err
}

func (s *RedisStore) AcquireRecovery(ctx context.Context, candidate State, leaseID string, continuationWaiting, sourceContinuationWaiting bool) (State, MutationStatus, error) {
	if err := s.requireOwner(); err != nil {
		return State{}, "", err
	}
	if leaseID == "" {
		return State{}, StatusLeaseMismatch, errors.New("recovery lease id is required")
	}
	result, err := acquireRecoveryScript.Run(ctx, s.client, []string{s.stateKey(candidate.CapabilityHash), s.key("recovery:lease", candidate.CapabilityHash), s.key("due"), s.key("recovery:global"), s.sourceRecoveryKey(candidate.CredentialSourceAccountID)}, strconv.Itoa(candidate.Generation), strconv.FormatInt(candidate.DispatchRevision, 10), leaseID, strconv.FormatInt((45*time.Second).Milliseconds(), 10), boolArg(continuationWaiting), boolArg(sourceContinuationWaiting), string(candidate.Phase)).Result()
	if err != nil {
		return State{}, "", err
	}
	values, ok := result.([]interface{})
	if !ok || len(values) != 2 {
		return State{}, "", errors.New("invalid recovery acquire result")
	}
	status := MutationStatus(stringValue(values[0]))
	if status != StatusApplied {
		return candidate, status, nil
	}
	state, err := decodeRedisState(stringValue(values[1]))
	return state, status, err
}

func (s *RedisStore) RenewRecovery(ctx context.Context, state State, leaseID string) (bool, error) {
	if err := s.requireOwner(); err != nil {
		return false, err
	}
	result, err := renewRecoveryScript.Run(ctx, s.client, []string{s.stateKey(state.CapabilityHash), s.key("recovery:lease", state.CapabilityHash), s.key("recovery:global"), s.sourceRecoveryKey(state.CredentialSourceAccountID)}, strconv.Itoa(state.Generation), strconv.FormatInt(state.DispatchRevision, 10), leaseID, strconv.FormatInt((45*time.Second).Milliseconds(), 10)).Int()
	return result == 1, err
}

func (s *RedisStore) CommitRecovery(ctx context.Context, prior State, next State, leaseID string) (MutationStatus, error) {
	if err := s.requireOwner(); err != nil {
		return "", err
	}
	encoded, err := encodeRedisState(next)
	if err != nil {
		return "", err
	}
	retryAt := int64(0)
	if !next.RetryAt.IsZero() {
		retryAt = next.RetryAt.UnixMilli()
	}
	result, err := commitRecoveryScript.Run(ctx, s.client, []string{s.stateKey(prior.CapabilityHash), s.key("recovery:lease", prior.CapabilityHash), s.key("due"), s.key("recovery:global"), s.sourceRecoveryKey(prior.CredentialSourceAccountID), s.key("closed")}, strconv.Itoa(prior.Generation), strconv.FormatInt(prior.DispatchRevision, 10), leaseID, string(encoded), strconv.FormatInt(retryAt, 10), string(next.Phase)).Text()
	return MutationStatus(result), err
}

func (s *RedisStore) CleanClosed(ctx context.Context, limit int64) (int64, error) {
	if err := s.requireOwner(); err != nil {
		return 0, err
	}
	if limit < 1 || limit > 1000 {
		return 0, errors.New("closed cleanup limit must be 1..1000")
	}
	return cleanClosedScript.Run(ctx, s.client, []string{s.key("closed"), s.key("due"), s.key("capacity")}, limit, s.key("state")+":").Int64()
}

func encodeRedisState(state State) ([]byte, error) {
	value := map[string]any{
		"credentialSourceAccountId": state.CredentialSourceAccountID, "keyFingerprint": state.KeyFingerprint,
		"clientModel": state.ClientModel, "clientEndpointFamily": state.ClientEndpointFamily,
		"finalUpstreamModel": state.FinalUpstreamModel, "upstreamEndpointMode": state.UpstreamEndpointMode,
		"dispatchRevision": state.DispatchRevision, "capabilityHash": state.CapabilityHash,
		"generation": state.Generation, "phase": state.Phase, "backoffAttempt": state.BackoffAttempt,
		"recoverySuccessCount": state.RecoverySuccessCount, "lastObservedAtMs": state.LastObservedAt.UnixMilli(),
	}
	if !state.RetryAt.IsZero() {
		value["retryAtMs"] = state.RetryAt.UnixMilli()
	}
	if !state.LastRecoverySuccessAt.IsZero() {
		value["lastRecoverySuccessAtMs"] = state.LastRecoverySuccessAt.UnixMilli()
	}
	if state.LastOutcome != "" {
		value["lastOutcome"] = state.LastOutcome
	}
	if state.ProbeLease != nil {
		value["probeLease"] = map[string]any{"leaseId": state.ProbeLease.ID, "leaseUntilMs": state.ProbeLease.Until.UnixMilli(), "priorSuccessCount": state.ProbeLease.PriorSuccesses}
	}
	return json.Marshal(value)
}

func decodeRedisState(raw string) (State, error) {
	var value struct {
		CredentialSourceAccountID string  `json:"credentialSourceAccountId"`
		KeyFingerprint            string  `json:"keyFingerprint"`
		ClientModel               string  `json:"clientModel"`
		ClientEndpointFamily      string  `json:"clientEndpointFamily"`
		FinalUpstreamModel        string  `json:"finalUpstreamModel"`
		UpstreamEndpointMode      string  `json:"upstreamEndpointMode"`
		DispatchRevision          int64   `json:"dispatchRevision"`
		CapabilityHash            string  `json:"capabilityHash"`
		Generation                int     `json:"generation"`
		Phase                     Phase   `json:"phase"`
		BackoffAttempt            int     `json:"backoffAttempt"`
		RecoverySuccessCount      int     `json:"recoverySuccessCount"`
		LastObservedAtMS          int64   `json:"lastObservedAtMs"`
		RetryAtMS                 *int64  `json:"retryAtMs"`
		LastRecoverySuccessAtMS   *int64  `json:"lastRecoverySuccessAtMs"`
		LastOutcome               Outcome `json:"lastOutcome"`
		ProbeLease                *struct {
			ID             string `json:"leaseId"`
			UntilMS        int64  `json:"leaseUntilMs"`
			PriorSuccesses int    `json:"priorSuccessCount"`
		} `json:"probeLease"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return State{}, err
	}
	state := State{Capability: Capability{CredentialSourceAccountID: value.CredentialSourceAccountID, KeyFingerprint: value.KeyFingerprint, ClientModel: value.ClientModel, ClientEndpointFamily: value.ClientEndpointFamily, FinalUpstreamModel: value.FinalUpstreamModel, UpstreamEndpointMode: value.UpstreamEndpointMode, DispatchRevision: value.DispatchRevision}, CapabilityHash: value.CapabilityHash, Generation: value.Generation, Phase: value.Phase, BackoffAttempt: value.BackoffAttempt, RecoverySuccessCount: value.RecoverySuccessCount, LastObservedAt: time.UnixMilli(value.LastObservedAtMS).UTC(), LastOutcome: value.LastOutcome}
	if value.RetryAtMS != nil {
		state.RetryAt = time.UnixMilli(*value.RetryAtMS).UTC()
	}
	if value.LastRecoverySuccessAtMS != nil {
		state.LastRecoverySuccessAt = time.UnixMilli(*value.LastRecoverySuccessAtMS).UTC()
	}
	if value.ProbeLease != nil {
		state.ProbeLease = &Lease{ID: value.ProbeLease.ID, Until: time.UnixMilli(value.ProbeLease.UntilMS).UTC(), PriorSuccesses: value.ProbeLease.PriorSuccesses}
	}
	return state, nil
}

var admitScript = redis.NewScript(`local existing=redis.call('GET',KEYS[3]); if existing then return {'idempotent',redis.call('GET',KEYS[4]) or '0',existing} end; local raw=redis.call('GET',KEYS[1]); if raw then local state=cjson.decode(raw); if tonumber(state.dispatchRevision)==tonumber(ARGV[4]) and state.phase~='CLOSED' then return {'blocked',redis.call('GET',KEYS[4]) or '0','0'} end end; if redis.call('EXISTS',KEYS[5])==1 then return {'blocked',redis.call('GET',KEYS[4]) or '0','0'} end; local now=tonumber(redis.call('TIME')[1])*1000+math.floor(tonumber(redis.call('TIME')[2])/1000); redis.call('ZREMRANGEBYSCORE',KEYS[2],'-inf',now); if redis.call('ZCARD',KEYS[2])>=tonumber(ARGV[3]) then return {'busy',redis.call('GET',KEYS[4]) or '0','0'} end; local leaseUntil=now+tonumber(ARGV[2]); redis.call('SET',KEYS[3],leaseUntil,'PX',ARGV[2]); redis.call('ZADD',KEYS[2],leaseUntil,ARGV[1]); redis.call('PEXPIRE',KEYS[2],ARGV[2]); return {'admitted',redis.call('GET',KEYS[4]) or '0',tostring(leaseUntil)}`)
var releaseScript = redis.NewScript(`if redis.call('DEL',KEYS[2])==0 then return {0,redis.call('GET',KEYS[3]) or '0'} end; redis.call('ZREM',KEYS[1],ARGV[2]); local wake=redis.call('INCR',KEYS[3]); redis.call('PUBLISH',KEYS[4],ARGV[1]..':'..tostring(wake)); return {1,wake}`)
var renewScript = redis.NewScript(`if not redis.call('GET',KEYS[2]) then return {'lost','0'} end; local now=tonumber(redis.call('TIME')[1])*1000+math.floor(tonumber(redis.call('TIME')[2])/1000); local leaseUntil=now+tonumber(ARGV[2]); redis.call('SET',KEYS[2],leaseUntil,'PX',ARGV[2]); redis.call('ZADD',KEYS[1],leaseUntil,ARGV[1]); redis.call('PEXPIRE',KEYS[1],ARGV[2]); return {'renewed',tostring(leaseUntil)}`)
var clearFenceScript = redis.NewScript(`if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end; redis.call('DEL',KEYS[1]); return 1`)
var deferFenceScript = redis.NewScript(`if redis.call('GET',KEYS[1]) ~= ARGV[1] then return 0 end; redis.call('PEXPIRE',KEYS[1],ARGV[2]); return 1`)
var recordMainProbeFenceScript = redis.NewScript(`redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[3]); if redis.call('DEL',KEYS[3])==1 then redis.call('ZREM',KEYS[2],ARGV[1]) end; local wake=redis.call('INCR',KEYS[4]); redis.call('PUBLISH',KEYS[5],ARGV[2]..':'..tostring(wake)); return {'applied',wake}`)
var acquireRecoveryScript = redis.NewScript(`local raw=redis.call('GET',KEYS[1]); if not raw then return {'stale',''} end; local state=cjson.decode(raw); if tonumber(state.generation)~=tonumber(ARGV[1]) or tonumber(state.dispatchRevision)~=tonumber(ARGV[2]) then return {'stale',raw} end; if state.phase~='OPEN' and state.phase~='RECOVERING' then return {'not_due',raw} end; local now=tonumber(redis.call('TIME')[1])*1000+math.floor(tonumber(redis.call('TIME')[2])/1000); if tonumber(state.retryAtMs or 0)>now then return {'not_due',raw} end; if redis.call('SET',KEYS[2],ARGV[3],'NX','PX',ARGV[4])==false then return {'lease_mismatch',raw} end; redis.call('ZREMRANGEBYSCORE',KEYS[4],'-inf',now); redis.call('ZREMRANGEBYSCORE',KEYS[5],'-inf',now); local globalLimit=100000; local sourceLimit=100000; if tonumber(redis.call('ZCARD',KEYS[4]))>=globalLimit or tonumber(redis.call('ZCARD',KEYS[5]))>=sourceLimit then redis.call('DEL',KEYS[2]); return {'not_due',raw} end; local leaseUntil=now+tonumber(ARGV[4]); redis.call('ZADD',KEYS[4],leaseUntil,ARGV[3]); redis.call('ZADD',KEYS[5],leaseUntil,ARGV[3]); state.phase='HALF_OPEN'; state.probeLease={leaseId=ARGV[3],leaseUntilMs=leaseUntil,priorSuccessCount=tonumber(state.recoverySuccessCount or 0)}; local encoded=cjson.encode(state); redis.call('SET',KEYS[1],encoded); return {'applied',encoded}`)
var renewRecoveryScript = redis.NewScript(`local raw=redis.call('GET',KEYS[1]); if not raw or redis.call('GET',KEYS[2])~=ARGV[3] then return 0 end; local state=cjson.decode(raw); if tonumber(state.generation)~=tonumber(ARGV[1]) or tonumber(state.dispatchRevision)~=tonumber(ARGV[2]) then return 0 end; local now=tonumber(redis.call('TIME')[1])*1000+math.floor(tonumber(redis.call('TIME')[2])/1000); local leaseUntil=now+tonumber(ARGV[4]); redis.call('PEXPIRE',KEYS[2],ARGV[4]); redis.call('ZADD',KEYS[3],leaseUntil,ARGV[3]); redis.call('ZADD',KEYS[4],leaseUntil,ARGV[3]); state.probeLease.leaseUntilMs=leaseUntil; redis.call('SET',KEYS[1],cjson.encode(state)); return 1`)
var commitRecoveryScript = redis.NewScript(`local raw=redis.call('GET',KEYS[1]); if not raw or redis.call('GET',KEYS[2])~=ARGV[3] then return 'stale' end; local state=cjson.decode(raw); if tonumber(state.generation)~=tonumber(ARGV[1]) or tonumber(state.dispatchRevision)~=tonumber(ARGV[2]) then return 'stale' end; redis.call('SET',KEYS[1],ARGV[4]); redis.call('DEL',KEYS[2]); redis.call('ZREM',KEYS[4],ARGV[3]); redis.call('ZREM',KEYS[5],ARGV[3]); if ARGV[6]=='CLOSED' then redis.call('ZREM',KEYS[3],state.capabilityHash); local now=tonumber(redis.call('TIME')[1])*1000+math.floor(tonumber(redis.call('TIME')[2])/1000); redis.call('ZADD',KEYS[6],now+300000,state.capabilityHash) else redis.call('ZADD',KEYS[3],ARGV[5],state.capabilityHash); redis.call('ZREM',KEYS[6],state.capabilityHash) end; return 'applied'`)
var cleanClosedScript = redis.NewScript(`local now=tonumber(redis.call('TIME')[1])*1000+math.floor(tonumber(redis.call('TIME')[2])/1000); local hashes=redis.call('ZRANGEBYSCORE',KEYS[1],'-inf',now,'LIMIT',0,ARGV[1]); local removed=0; for _,hash in ipairs(hashes) do local stateKey=ARGV[2]..hash; local raw=redis.call('GET',stateKey); if raw and cjson.decode(raw).phase=='CLOSED' then redis.call('DEL',stateKey); redis.call('ZREM',KEYS[2],hash); redis.call('DECR',KEYS[3]); removed=removed+1 end; redis.call('ZREM',KEYS[1],hash) end; return removed`)
var recordFailureScript = redis.NewScript(`local now=tonumber(redis.call('TIME')[1])*1000+math.floor(tonumber(redis.call('TIME')[2])/1000); local incoming=cjson.decode(ARGV[1]); incoming.lastObservedAtMs=now; incoming.retryAtMs=now+5000; local function releasePermit() if redis.call('DEL',KEYS[6])==1 then redis.call('ZREM',KEYS[5],ARGV[5]); local wake=redis.call('INCR',KEYS[7]); redis.call('PUBLISH',KEYS[8],ARGV[6]..':'..tostring(wake)); end end; local receipt=redis.call('GET',KEYS[3]); if receipt then releasePermit(); return {'idempotent',redis.call('GET',KEYS[1]) or receipt} end; local raw=redis.call('GET',KEYS[1]); if raw then local current=cjson.decode(raw); if tonumber(current.dispatchRevision)>tonumber(ARGV[2]) then releasePermit(); return {'stale',''} end; if tonumber(current.dispatchRevision)==tonumber(ARGV[2]) and current.phase~='CLOSED' then current.lastObservedAtMs=now; current.lastOutcome='upstream_not_complete'; local encoded=cjson.encode(current); redis.call('SET',KEYS[1],encoded); redis.call('SET',KEYS[3],encoded,'PX',ARGV[4]); releasePermit(); return {'idempotent',encoded} end; incoming.generation=tonumber(current.generation or 0)+1 end; local capacity=tonumber(redis.call('GET',KEYS[4]) or '0'); if capacity>=tonumber(ARGV[3]) then local removed=0; for _, hash in ipairs(redis.call('ZRANGE',KEYS[9],0,999)) do local old=redis.call('GET',string.gsub(KEYS[1],'[^:]+$',hash)); if old then local parsed=cjson.decode(old); if parsed.phase=='CLOSED' then redis.call('DEL',string.gsub(KEYS[1],'[^:]+$',hash)); redis.call('ZREM',KEYS[9],hash); redis.call('DECR',KEYS[4]); removed=removed+1; if tonumber(redis.call('GET',KEYS[4]) or '0')<tonumber(ARGV[3]) then break end end end end; capacity=tonumber(redis.call('GET',KEYS[4]) or '0'); if capacity>=tonumber(ARGV[3]) then releasePermit(); return {'capacity_exhausted',''} end end; redis.call('INCR',KEYS[4]); local encoded=cjson.encode(incoming); redis.call('SET',KEYS[1],encoded); redis.call('ZADD',KEYS[2],incoming.retryAtMs,incoming.capabilityHash); redis.call('SET',KEYS[3],encoded,'PX',ARGV[4]); releasePermit(); return {'applied',encoded}`)

func stringValue(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}
func parseInt64(v interface{}) (int64, error) { return strconv.ParseInt(stringValue(v), 10, 64) }
func parseUint(v interface{}) (uint64, error) { return strconv.ParseUint(stringValue(v), 10, 64) }

func boolArg(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
