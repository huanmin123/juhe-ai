package oauthrefresh

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Refresh failure state mirrors the openai-oauth-access-token-refresh.service
// failure bookkeeping: per-account counters guarded by the account
// config_revision, a backoffUntil timestamp and the local-configuration
// failure counter that drives the terminal stopped state. The state lives in
// the process memory map by default (sqlite deployments) or in the state Redis
// (redis runtime state driver) behind Scripter.
type RefreshFailureState struct {
	Count                   int64  `json:"count"`
	LocalConfigurationCount int64  `json:"localConfigurationCount"`
	BackoffUntil            int64  `json:"backoffUntil"`
	ConfigRevision          int64  `json:"configRevision"`
	Applied                 bool   `json:"applied"`
	MutationID              string `json:"mutationId"`
	Snapshot                string `json:"-"`
}

// FailureKind mirrors the two Node failure classes: local configuration
// failures count toward the terminal stopped state, everything upstream or
// runtime related only backs off.
type FailureKind string

// Failure kinds.
const (
	FailureKindLocalConfiguration FailureKind = "local_configuration"
	FailureKindUntrustedUpstream  FailureKind = "untrusted_upstream_or_runtime"
)

// FailureStateTTL is the Redis TTL of the failure record
// (openAIOAuthRefreshFailureStateTtlMs = 7 days).
const FailureStateTTL = 7 * 24 * time.Hour

// FailureStateStore is the persistence boundary of the failure state.
type FailureStateStore interface {
	Record(ctx context.Context, accountID string, backoffUntil int64, kind FailureKind, configRevision int64) (RefreshFailureState, error)
	Read(ctx context.Context, accountID string, now int64, configRevision int64) (*RefreshFailureState, error)
	Clear(ctx context.Context, accountID string, guard RefreshFailureState) error
	CleanupBackoff(now int64)
}

// NewMemoryFailureStateStore builds the in-process store.
func NewMemoryFailureStateStore() *MemoryFailureStateStore { return &MemoryFailureStateStore{} }

// MemoryFailureStateStore mirrors refreshFailureStateByAccountId.
type MemoryFailureStateStore struct {
	mu     sync.Mutex
	values map[string]RefreshFailureState
}

// Record implements FailureStateStore with the exact revision semantics of the
// Node map path.
func (m *MemoryFailureStateStore) Record(_ context.Context, accountID string, backoffUntil int64, kind FailureKind, configRevision int64) (RefreshFailureState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]RefreshFailureState{}
	}
	normalizedRevision := normalizedRevisionOr(configRevision)
	mutationID := newMutationID()
	previous, ok := m.values[accountID]
	if ok && previous.ConfigRevision > normalizedRevision {
		return RefreshFailureState{Count: previous.Count, LocalConfigurationCount: previous.LocalConfigurationCount, BackoffUntil: previous.BackoffUntil, ConfigRevision: previous.ConfigRevision, MutationID: previous.MutationID, Snapshot: previous.Snapshot, Applied: false}, nil
	}
	var previousForRevision *RefreshFailureState
	if ok && previous.ConfigRevision == normalizedRevision {
		copy := previous
		previousForRevision = &copy
	}
	next := RefreshFailureState{
		Count:                   previousForRevisionCount(previousForRevision) + 1,
		LocalConfigurationCount: 0,
		BackoffUntil:            backoffUntil,
		ConfigRevision:          normalizedRevision,
		Applied:                 true,
		MutationID:              mutationID,
	}
	if previousForRevision != nil && previousForRevision.BackoffUntil > next.BackoffUntil {
		next.BackoffUntil = previousForRevision.BackoffUntil
	}
	if kind == FailureKindLocalConfiguration {
		next.LocalConfigurationCount = previousForRevisionLocalCount(previousForRevision) + 1
	}
	m.values[accountID] = next
	return next, nil
}

func previousForRevisionCount(state *RefreshFailureState) int64 {
	if state == nil {
		return 0
	}
	return state.Count
}

func previousForRevisionLocalCount(state *RefreshFailureState) int64 {
	if state == nil {
		return 0
	}
	return state.LocalConfigurationCount
}

// Read implements FailureStateStore with the Node revision guards: newer
// revisions shadow the record, older revisions clear it; expired backoffs read
// as zero.
func (m *MemoryFailureStateStore) Read(_ context.Context, accountID string, now int64, configRevision int64) (*RefreshFailureState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.values[accountID]
	if !ok {
		return nil, nil
	}
	normalizedRevision := normalizedRevisionOr(configRevision)
	if state.ConfigRevision > normalizedRevision {
		return nil, nil
	}
	if state.ConfigRevision < normalizedRevision {
		delete(m.values, accountID)
		return nil, nil
	}
	backoffUntil := state.BackoffUntil
	if backoffUntil <= now {
		backoffUntil = 0
	}
	return &RefreshFailureState{
		Count:                   state.Count,
		LocalConfigurationCount: state.LocalConfigurationCount,
		BackoffUntil:            backoffUntil,
		ConfigRevision:          state.ConfigRevision,
		Applied:                 true,
		MutationID:              state.MutationID,
		Snapshot:                state.Snapshot,
	}, nil
}

// Clear implements FailureStateStore with the mutation/revision guards of the
// Node map path.
func (m *MemoryFailureStateStore) Clear(_ context.Context, accountID string, guard RefreshFailureState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.values[accountID]
	if !ok {
		return nil
	}
	if guard.MutationID != "" && current.MutationID != guard.MutationID {
		return nil
	}
	if guard.ConfigRevision != 0 && current.ConfigRevision != guard.ConfigRevision {
		return nil
	}
	delete(m.values, accountID)
	return nil
}

// snapshotCount reports the tracked account count
// (refreshFailureStateByAccountId.size in refreshCandidateFetchLimit).
func (m *MemoryFailureStateStore) snapshotCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.values)
}

// CleanupBackoff mirrors cleanupRefreshFailureBackoff: at cycle start expired
// backoffs reset to zero (with a fresh mutation id so stale clears do not
// delete the newer record).
func (m *MemoryFailureStateStore) CleanupBackoff(now int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for accountID, state := range m.values {
		if state.BackoffUntil <= now {
			state.BackoffUntil = 0
			state.MutationID = newMutationID()
			m.values[accountID] = state
		}
	}
}

// Scripter is the minimal Redis command boundary of the Lua-backed failure
// store (go-redis compatible: Eval returning one row).
type Scripter interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
	Get(ctx context.Context, key string) (string, error)
}

// RedisFailureStateStore mirrors the redisRecordRefreshFailureScript /
// redisCompareDeleteRefreshFailureScript pair byte-for-byte.
type RedisFailureStateStore struct {
	redis Scripter
	now   func() time.Time
}

// errRedisShape guards malformed Eval replies.
var errRedisShape = errors.New("OAuth 刷新失败状态 Redis 返回结构无效")

// NewRedisFailureStateStore builds the Redis-backed store.
func NewRedisFailureStateStore(redis Scripter) *RedisFailureStateStore {
	return &RedisFailureStateStore{redis: redis, now: func() time.Time { return time.Now() }}
}

// redisRecordRefreshFailureScript mirrors the Node record script: the stored
// revision shadows newer-account records, same-revision records accumulate
// counters and the backoff keeps the max, newer revision resets the backoff.
const redisRecordRefreshFailureScript = `
local raw = redis.call('GET', KEYS[1])
local count = 0
local local_configuration_count = 0
local config_revision = tonumber(ARGV[4])
local stored_revision = 0
local stored_backoff_until = 0
local stored_mutation_id = ''
if raw then
  local ok, decoded = pcall(cjson.decode, raw)
  if ok and decoded then
    stored_revision = tonumber(decoded['configRevision']) or 0
    stored_backoff_until = tonumber(decoded['backoffUntil']) or 0
    stored_mutation_id = tostring(decoded['mutationId'] or '')
    if stored_revision > config_revision then
      return {
        tonumber(decoded['count']) or 0,
        stored_backoff_until,
        tonumber(decoded['localConfigurationCount']) or 0,
        stored_revision,
        0,
        stored_mutation_id,
        raw
      }
    end
    if stored_revision == config_revision then
      count = tonumber(decoded['count']) or 0
      local_configuration_count = tonumber(decoded['localConfigurationCount']) or 0
    else
      stored_backoff_until = 0
    end
  end
end
count = count + 1
local backoff_until = math.max(stored_backoff_until, tonumber(ARGV[1]) or 0)
local ttl_ms = tonumber(ARGV[2])
local is_local_configuration = tonumber(ARGV[3]) or 0
if is_local_configuration == 1 then
  local_configuration_count = local_configuration_count + 1
else
  local_configuration_count = 0
end
local mutation_id = ARGV[5]
local payload = cjson.encode({ count = count, localConfigurationCount = local_configuration_count, backoffUntil = backoff_until, configRevision = config_revision, mutationId = mutation_id })
redis.call('SET', KEYS[1], payload, 'PX', ttl_ms)
return {count, backoff_until, local_configuration_count, config_revision, 1, mutation_id, payload}
`

// redisCompareDeleteRefreshFailureScript mirrors the Node compare-delete
// script.
const redisCompareDeleteRefreshFailureScript = `
local raw = redis.call('GET', KEYS[1])
if raw and raw == ARGV[1] then
  local ok, decoded = pcall(cjson.decode, raw)
  if ok and decoded and tonumber(ARGV[2]) > 0 and tonumber(decoded['configRevision']) ~= tonumber(ARGV[2]) then
    return 0
  end
  return redis.call('DEL', KEYS[1])
end
return 0
`

// Record implements FailureStateStore through the record script.
func (r *RedisFailureStateStore) Record(ctx context.Context, accountID string, backoffUntil int64, kind FailureKind, configRevision int64) (RefreshFailureState, error) {
	normalizedRevision := normalizedRevisionOr(configRevision)
	mutationID := newMutationID()
	result, err := r.redis.Eval(ctx, redisRecordRefreshFailureScript,
		[]string{failureStateKey(accountID)},
		formatInt64(clampInt64(backoffUntil, 0, math.MaxInt64)),
		formatInt64(FailureStateTTL.Milliseconds()),
		kindBool(kind == FailureKindLocalConfiguration),
		formatInt64(normalizedRevision),
		mutationID)
	if err != nil {
		return RefreshFailureState{}, err
	}
	values, ok := result.([]any)
	if !ok {
		return RefreshFailureState{}, errRedisShape
	}
	state := RefreshFailureState{
		Count:                   clampInt64(numericRedis(values, 0, 1), 1, math.MaxInt64),
		BackoffUntil:            clampInt64(numericRedis(values, 1, backoffUntil), 0, math.MaxInt64),
		LocalConfigurationCount: clampInt64(numericRedis(values, 2, localCountFallback(kind)), 0, math.MaxInt64),
		ConfigRevision:          clampInt64(numericRedis(values, 3, normalizedRevision), 1, math.MaxInt64),
		Applied:                 numericRedis(values, 4, 0) == 1,
		MutationID:              stringRedis(values, 5, mutationID),
	}
	if snapshot := stringRedisPtr(values, 6); snapshot != nil {
		state.Snapshot = *snapshot
	} else {
		state.Snapshot = stringRedis(values, 6, "")
	}
	return state, nil
}

// Read implements FailureStateStore through GET + JSON decode with the same
// revision guards and clearing behaviour as Node.
func (r *RedisFailureStateStore) Read(ctx context.Context, accountID string, now int64, configRevision int64) (*RefreshFailureState, error) {
	raw, err := r.redis.Get(ctx, failureStateKey(accountID))
	if err != nil {
		return nil, nil
	}
	if raw == "" {
		return nil, nil
	}
	var parsed struct {
		Count                   *int64 `json:"count"`
		LocalConfigurationCount *int64 `json:"localConfigurationCount"`
		BackoffUntil            *int64 `json:"backoffUntil"`
		ConfigRevision          *int64 `json:"configRevision"`
		MutationID              string `json:"mutationId"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		_ = r.Clear(ctx, accountID, RefreshFailureState{Snapshot: raw})
		return nil, nil
	}
	normalizedRevision := normalizedRevisionOr(configRevision)
	if parsed.ConfigRevision == nil || *parsed.ConfigRevision < 1 {
		_ = r.Clear(ctx, accountID, RefreshFailureState{Snapshot: raw})
		return nil, nil
	}
	stored := *parsed.ConfigRevision
	if stored > normalizedRevision {
		return nil, nil
	}
	if stored < normalizedRevision {
		_ = r.Clear(ctx, accountID, RefreshFailureState{Snapshot: raw, ConfigRevision: stored})
		return nil, nil
	}
	count := int64(0)
	if parsed.Count != nil {
		count = *parsed.Count
	}
	localCount := int64(0)
	if parsed.LocalConfigurationCount != nil {
		localCount = *parsed.LocalConfigurationCount
	}
	backoffUntil := int64(0)
	if parsed.BackoffUntil != nil {
		backoffUntil = *parsed.BackoffUntil
	}
	if backoffUntil <= now {
		backoffUntil = 0
	}
	return &RefreshFailureState{
		Count:                   count,
		LocalConfigurationCount: localCount,
		BackoffUntil:            backoffUntil,
		ConfigRevision:          stored,
		Applied:                 true,
		MutationID:              parsed.MutationID,
		Snapshot:                raw,
	}, nil
}

// Clear implements FailureStateStore through the compare-delete script.
func (r *RedisFailureStateStore) Clear(ctx context.Context, accountID string, guard RefreshFailureState) error {
	if guard.Snapshot == "" {
		return nil
	}
	_, err := r.redis.Eval(ctx, redisCompareDeleteRefreshFailureScript,
		[]string{failureStateKey(accountID)},
		guard.Snapshot, formatInt64(guard.ConfigRevision))
	return err
}

// CleanupBackoff is a no-op on Redis: the record TTL owns expiry.
func (r *RedisFailureStateStore) CleanupBackoff(int64) {}

func numericRedis(values []any, index int, fallback int64) int64 {
	if index >= len(values) {
		return fallback
	}
	switch value := values[index].(type) {
	case int64:
		return value
	case float64:
		return int64(value)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return fallback
		}
		return parsed
	}
	return fallback
}

func stringRedis(values []any, index int, fallback string) string {
	if value := stringRedisPtr(values, index); value != nil {
		return *value
	}
	return fallback
}

func stringRedisPtr(values []any, index int) *string {
	if index >= len(values) {
		return nil
	}
	switch value := values[index].(type) {
	case string:
		if value == "" {
			return nil
		}
		return &value
	case []byte:
		if len(value) == 0 {
			return nil
		}
		text := string(value)
		return &text
	}
	return nil
}

func kindBool(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func localCountFallback(kind FailureKind) int64 {
	if kind == FailureKindLocalConfiguration {
		return 1
	}
	return 0
}

func normalizedRevisionOr(value int64) int64 {
	if value >= 1 {
		return value
	}
	return 1
}

func clampInt64(value, min, max int64) int64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func newMutationID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func formatInt64(value int64) string { return strconv.FormatInt(value, 10) }

// failureStateKey mirrors redisRefreshFailureStateKey:
// juhe-ai:state:openai-oauth-refresh-failure:{base64url(sha256(accountId))}.
func failureStateKey(accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return "juhe-ai:state:openai-oauth-refresh-failure:" + base64.RawURLEncoding.EncodeToString(sum[:])
}
