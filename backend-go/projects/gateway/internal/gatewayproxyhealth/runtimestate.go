package gatewayproxyhealth

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// RuntimeStateStore ports the consumed surface of
// backend/src/shared/runtime-state-store.ts. GetJSON/GetJSONMany return the
// raw JSON bytes so compare-set callers can resend the exact stored bytes as
// the expected value (byte-identical CAS against Node-written entries).
// Node's getDeleteJson/incr are not consumed by this slice and are not ported.
type RuntimeStateStore interface {
	GetJSON(ctx context.Context, key string) (json.RawMessage, error)
	GetJSONMany(ctx context.Context, keys []string) ([]json.RawMessage, error)
	SetJSON(ctx context.Context, key string, value any, ttlMs int64) error
	// CompareSetJSON: expected == nil requires the key to be absent;
	// otherwise the stored value must equal the expected bytes.
	CompareSetJSON(ctx context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error)
	CompareDeleteJSON(ctx context.Context, key string, expected json.RawMessage) (bool, error)
	Delete(ctx context.Context, key string) error
	AcquireLock(ctx context.Context, key string, ttlMs int64, token string) (bool, error)
	RenewLock(ctx context.Context, key string, ttlMs int64, token string) (bool, error)
	ReleaseLock(ctx context.Context, key, token string) error
}

type memoryStateEntry struct {
	value     json.RawMessage
	expiresAt int64
}

// MemoryRuntimeStateStore mirrors Node MemoryRuntimeStateStore: one shared
// map per store name, expiry enforced on read, JSON-string equality for CAS.
type MemoryRuntimeStateStore struct {
	mu      sync.Mutex
	clock   Clock
	entries map[string]memoryStateEntry
}

// NewMemoryRuntimeStateStore builds the memory driver.
func NewMemoryRuntimeStateStore(clock Clock) *MemoryRuntimeStateStore {
	return &MemoryRuntimeStateStore{
		clock:   clock,
		entries: map[string]memoryStateEntry{},
	}
}

func (s *MemoryRuntimeStateStore) nowMs() int64 { return ClockNowMs(s.clock) }

func (s *MemoryRuntimeStateStore) freshEntryLocked(key string) *memoryStateEntry {
	entry, ok := s.entries[key]
	if !ok {
		return nil
	}
	if entry.expiresAt <= s.nowMs() {
		delete(s.entries, key)
		return nil
	}
	copy := entry
	return &copy
}

// GetJSON mirrors getJson.
func (s *MemoryRuntimeStateStore) GetJSON(_ context.Context, key string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.freshEntryLocked(key)
	if entry == nil {
		return nil, nil
	}
	return append(json.RawMessage(nil), entry.value...), nil
}

// GetJSONMany mirrors getJsonMany.
func (s *MemoryRuntimeStateStore) GetJSONMany(_ context.Context, keys []string) ([]json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	output := make([]json.RawMessage, len(keys))
	for i, key := range keys {
		if entry := s.freshEntryLocked(key); entry != nil {
			output[i] = append(json.RawMessage(nil), entry.value...)
		}
	}
	return output, nil
}

// SetJSON mirrors setJson.
func (s *MemoryRuntimeStateStore) SetJSON(_ context.Context, key string, value any, ttlMs int64) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = memoryStateEntry{value: encoded, expiresAt: s.nowMs() + normalizeTTLms(ttlMs)}
	return nil
}

// CompareSetJSON mirrors compareSetJson (stringified JSON equality).
func (s *MemoryRuntimeStateStore) CompareSetJSON(_ context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error) {
	encodedNext, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.freshEntryLocked(key)
	if expected == nil {
		if current != nil {
			return false, nil
		}
	} else {
		if current == nil || !rawJSONEqual(json.RawMessage(current.value), expected) {
			return false, nil
		}
	}
	s.entries[key] = memoryStateEntry{value: encodedNext, expiresAt: s.nowMs() + normalizeTTLms(ttlMs)}
	return true, nil
}

// CompareDeleteJSON mirrors compareDeleteJson.
func (s *MemoryRuntimeStateStore) CompareDeleteJSON(_ context.Context, key string, expected json.RawMessage) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.freshEntryLocked(key)
	if current == nil || !rawJSONEqual(json.RawMessage(current.value), expected) {
		return false, nil
	}
	delete(s.entries, key)
	return true, nil
}

// Delete mirrors delete.
func (s *MemoryRuntimeStateStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	return nil
}

// AcquireLock mirrors acquireLock (SET NX semantics).
func (s *MemoryRuntimeStateStore) AcquireLock(_ context.Context, key string, ttlMs int64, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.freshEntryLocked(key) != nil {
		return false, nil
	}
	s.entries[key] = memoryStateEntry{value: json.RawMessage(jsonEscapeString(token)), expiresAt: s.nowMs() + normalizeTTLms(ttlMs)}
	return true, nil
}

// RenewLock mirrors renewLock (token match extends TTL).
func (s *MemoryRuntimeStateStore) RenewLock(_ context.Context, key string, ttlMs int64, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.freshEntryLocked(key)
	if entry == nil || !lockTokenEqual(entry.value, token) {
		return false, nil
	}
	entry.expiresAt = s.nowMs() + normalizeTTLms(ttlMs)
	s.entries[key] = *entry
	return true, nil
}

// ReleaseLock mirrors releaseLock (token match deletes).
func (s *MemoryRuntimeStateStore) ReleaseLock(_ context.Context, key, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.freshEntryLocked(key); entry != nil && lockTokenEqual(entry.value, token) {
		delete(s.entries, key)
	}
	return nil
}

func lockTokenEqual(raw json.RawMessage, token string) bool {
	var stored string
	if err := json.Unmarshal(raw, &stored); err != nil {
		return false
	}
	return stored == token
}

func jsonEscapeString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func rawJSONEqual(left, right json.RawMessage) bool {
	return string(left) == string(right)
}

func normalizeTTLms(ttlMs int64) int64 {
	if ttlMs < 1 {
		return 1
	}
	return ttlMs
}

// redisStateClient is the go-redis command subset the Redis runtime-state
// driver needs. *redis.Client satisfies it; tests supply a fake.
type redisStateClient interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	MGet(ctx context.Context, keys ...string) *redis.SliceCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
}

const (
	redisCompareSetJSONScript = `
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '' then
  if current then
    return 0
  end
elseif current ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1
`
	redisCompareDeleteJSONScript = `
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
return redis.call('DEL', KEYS[1])
`
	redisReleaseLockScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`
	redisRenewLockScript = `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`
)

const redisStateOperationTimeout = 3 * time.Second

// RedisRuntimeStateStore mirrors Node RedisRuntimeStateStore: namespace-prefixed
// string keys, JSON bodies, PX TTLs and Lua CAS. Key layout is byte-identical
// to the Node driver (juhe-ai:<namespace>:state:<name>:<key>) so both stacks
// share one state space during the migration window.
type RedisRuntimeStateStore struct {
	client redisStateClient
	prefix string
}

// NewRedisRuntimeStateStore builds the Redis driver. namespace accepts the
// short namespace or the full juhe-ai: prefix (mirrors redisNamespacedKey).
func NewRedisRuntimeStateStore(client redisStateClient, namespace, name string) (*RedisRuntimeStateStore, error) {
	if client == nil || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("gateway proxy health Redis client and namespace are required")
	}
	return &RedisRuntimeStateStore{
		client: client,
		prefix: namespacedRedisKey(namespace, "state:"+sanitizeRedisKeyPart(name)+":"),
	}, nil
}

// namespacedRedisKey mirrors redisNamespacedKey: the root prefix juhe-ai: is
// never doubled.
func namespacedRedisKey(namespace, key string) string {
	namespacePrefix := "juhe-ai:" + sanitizeRedisNamespacePart(namespace) + ":"
	normalized := strings.TrimSpace(key)
	if strings.HasPrefix(normalized, namespacePrefix) {
		return normalized
	}
	if strings.HasPrefix(normalized, "juhe-ai:") {
		return namespacePrefix + normalized[len("juhe-ai:"):]
	}
	return namespacePrefix + normalized
}

// sanitizeRedisNamespacePart mirrors the Node helper of the same name.
func sanitizeRedisNamespacePart(value string) string {
	normalized := strings.TrimSpace(value)
	var builder strings.Builder
	prevUnderscore := false
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == ':' || r == '-' {
			builder.WriteRune(r)
			prevUnderscore = false
			continue
		}
		// Node replaces runs with a single underscore.
		if !prevUnderscore {
			builder.WriteByte('_')
			prevUnderscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return result
	}
	return result
}

// sanitizeRedisKeyPart mirrors the runtime-state-store helper: character
// substitution is one-to-one and the empty result becomes 'default'.
func sanitizeRedisKeyPart(value string) string {
	normalized := strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '.' || r == ':' || r == '-' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	if builder.Len() == 0 {
		return "default"
	}
	return builder.String()
}

func (s *RedisRuntimeStateStore) key(key string) string { return s.prefix + key }

func (s *RedisRuntimeStateStore) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, redisStateOperationTimeout)
}

// GetJSON mirrors getJson: missing or unparseable values read as absent
// (unparseable values are deleted like the Node catch).
func (s *RedisRuntimeStateStore) GetJSON(ctx context.Context, key string) (json.RawMessage, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	raw, err := s.client.Get(ctx, s.key(key)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(raw)) {
		_ = s.Delete(ctx, key)
		return nil, nil
	}
	return json.RawMessage(raw), nil
}

// GetJSONMany mirrors getJsonMany (MGET + malformed-key cleanup).
func (s *RedisRuntimeStateStore) GetJSONMany(ctx context.Context, keys []string) ([]json.RawMessage, error) {
	if len(keys) == 0 {
		return []json.RawMessage{}, nil
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	redisKeys := make([]string, len(keys))
	for i, key := range keys {
		redisKeys[i] = s.key(key)
	}
	values, err := s.client.MGet(ctx, redisKeys...).Result()
	if err != nil {
		return nil, err
	}
	output := make([]json.RawMessage, len(redisKeys))
	var malformed []string
	for i, value := range values {
		raw, ok := value.(string)
		if !ok || raw == "" {
			continue
		}
		if !json.Valid([]byte(raw)) {
			malformed = append(malformed, redisKeys[i])
			continue
		}
		output[i] = json.RawMessage(raw)
	}
	if len(malformed) > 0 {
		_ = s.client.Del(ctx, malformed...).Err()
	}
	return output, nil
}

// SetJSON mirrors setJson (JSON body, PX TTL).
func (s *RedisRuntimeStateStore) SetJSON(ctx context.Context, key string, value any, ttlMs int64) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Set(ctx, s.key(key), encoded, time.Duration(normalizeTTLms(ttlMs))*time.Millisecond).Err()
}

// CompareSetJSON mirrors compareSetJson (Lua, expected bytes or absent).
func (s *RedisRuntimeStateStore) CompareSetJSON(ctx context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error) {
	encodedNext, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	expectedValue := ""
	if expected != nil {
		expectedValue = string(expected)
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	result, err := s.client.Eval(ctx, redisCompareSetJSONScript, []string{s.key(key)},
		expectedValue, string(encodedNext), normalizeTTLms(ttlMs)).Result()
	if err != nil {
		return false, err
	}
	return numericRedisResult(result) == 1, nil
}

// CompareDeleteJSON mirrors compareDeleteJson.
func (s *RedisRuntimeStateStore) CompareDeleteJSON(ctx context.Context, key string, expected json.RawMessage) (bool, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	result, err := s.client.Eval(ctx, redisCompareDeleteJSONScript, []string{s.key(key)}, string(expected)).Result()
	if err != nil {
		return false, err
	}
	return numericRedisResult(result) == 1, nil
}

// Delete mirrors delete.
func (s *RedisRuntimeStateStore) Delete(ctx context.Context, key string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Del(ctx, s.key(key)).Err()
}

// AcquireLock mirrors acquireLock (SET ... NX PX).
func (s *RedisRuntimeStateStore) AcquireLock(ctx context.Context, key string, ttlMs int64, token string) (bool, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.SetNX(ctx, s.key(key), token, time.Duration(normalizeTTLms(ttlMs))*time.Millisecond).Result()
}

// RenewLock mirrors renewLock.
func (s *RedisRuntimeStateStore) RenewLock(ctx context.Context, key string, ttlMs int64, token string) (bool, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	result, err := s.client.Eval(ctx, redisRenewLockScript, []string{s.key(key)}, token, normalizeTTLms(ttlMs)).Result()
	if err != nil {
		return false, err
	}
	return numericRedisResult(result) == 1, nil
}

// ReleaseLock mirrors releaseLock.
func (s *RedisRuntimeStateStore) ReleaseLock(ctx context.Context, key, token string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	return s.client.Eval(ctx, redisReleaseLockScript, []string{s.key(key)}, token).Err()
}

func numericRedisResult(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
