package gatewayclientip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// RuntimeStateDriver values mirror runtimeConfig.runtimeStateDriver.
const (
	RuntimeStateDriverMemory = "memory"
	RuntimeStateDriverRedis  = "redis"
)

// stateOperationTimeout mirrors the runRedisOperationWithDeadline budget of
// shared/runtime-state-store.ts (timeoutMs: 3_000).
const stateOperationTimeout = 3 * time.Second

// RuntimeStateStore mirrors the consumed RuntimeStateStore surface of
// shared/runtime-state-store.ts: JSON get/set/delete with TTL. The memory
// driver keeps per-entry expiresAt; the Redis driver mirrors
// RedisRuntimeStateStore including the malformed-JSON delete-on-read.
type RuntimeStateStore interface {
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, value any, ttlMs int64) error
	Delete(ctx context.Context, key string) error
}

// NewMemoryRuntimeStateStore mirrors MemoryRuntimeStateStore. clock injects
// time for expiry checks.
func NewMemoryRuntimeStateStore(clock Clock) RuntimeStateStore {
	return &memoryRuntimeStateStore{clock: clock, entries: map[string]memoryStateEntry{}}
}

type memoryStateEntry struct {
	value     json.RawMessage
	expiresAt time.Time
}

type memoryRuntimeStateStore struct {
	clock   Clock
	entries map[string]memoryStateEntry
}

func (s *memoryRuntimeStateStore) getFresh(key string) (json.RawMessage, bool) {
	entry, ok := s.entries[key]
	if !ok {
		return nil, false
	}
	if !entry.expiresAt.After(s.clock.Now()) {
		delete(s.entries, key)
		return nil, false
	}
	return entry.value, true
}

func (s *memoryRuntimeStateStore) GetJSON(_ context.Context, key string, dst any) (bool, error) {
	raw, ok := s.getFresh(key)
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

func (s *memoryRuntimeStateStore) SetJSON(_ context.Context, key string, value any, ttlMs int64) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.entries[key] = memoryStateEntry{value: encoded, expiresAt: s.clock.Now().Add(normalizeTtlMsDuration(ttlMs))}
	return nil
}

func (s *memoryRuntimeStateStore) Delete(_ context.Context, key string) error {
	delete(s.entries, key)
	return nil
}

// redisRuntimeStateStore mirrors RedisRuntimeStateStore for one named store:
// keys are "<namespace>:state:<name>:<key>" following the Node
// redisNamespacedKey(`juhe-ai:state:<name>:`) layout.
type redisRuntimeStateStore struct {
	client     *redis.Client
	keyPrefix  string
	encodeName func(value any) ([]byte, error)
}

// NewRedisRuntimeStateStore mirrors createRuntimeStateStore(name) with
// runtimeStateDriver === 'redis'. namespace is runtimeConfig.redis.namespace;
// the Node namespace root "juhe-ai:" is always applied on top exactly once.
func NewRedisRuntimeStateStore(url, namespace, name string) (RuntimeStateStore, func(), error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil, errors.New("JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		return nil, nil, err
	}
	client := redis.NewClient(options)
	prefix, err := stateStoreKeyPrefix(namespace, name)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return &redisRuntimeStateStore{client: client, keyPrefix: prefix}, func() { _ = client.Close() }, nil
}

// stateStoreKeyPrefix mirrors redisNamespacedKey(`juhe-ai:state:<name>:`):
// the namespace root is "juhe-ai:" and the configured namespace is inserted
// right after it, sanitized like sanitizeRedisNamespacePart.
func stateStoreKeyPrefix(namespace, name string) (string, error) {
	sanitizedNamespace, err := sanitizeRedisNamespacePart(namespace)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("juhe-ai:%s:state:%s:", sanitizedNamespace, sanitizeRedisKeyPart(name)), nil
}

// sanitizeRedisNamespacePart mirrors shared/redis-namespace.ts.
func sanitizeRedisNamespacePart(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	normalized = namespaceSanitizePattern.ReplaceAllString(normalized, "_")
	normalized = strings.Trim(normalized, "_")
	if normalized == "" {
		return "", errors.New("Redis namespace 不能为空")
	}
	return normalized, nil
}

var namespaceSanitizePattern = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

// sanitizeRedisKeyPart mirrors the runtime-state-store / queue key sanitizer.
func sanitizeRedisKeyPart(value string) string {
	normalized := keySanitizePattern.ReplaceAllString(strings.TrimSpace(value), "_")
	if normalized == "" {
		return "default"
	}
	return normalized
}

var keySanitizePattern = regexp.MustCompile(`[^a-zA-Z0-9:_-]`)

func (s *redisRuntimeStateStore) redisKey(key string) string {
	return s.keyPrefix + key
}

func (s *redisRuntimeStateStore) run(ctx context.Context, operation func(ctx context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, stateOperationTimeout)
	defer cancel()
	return operation(runCtx)
}

func (s *redisRuntimeStateStore) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
	var raw string
	err := s.run(ctx, func(runCtx context.Context) error {
		var getErr error
		raw, getErr = s.client.Get(runCtx, s.redisKey(key)).Result()
		return getErr
	})
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if jsonErr := json.Unmarshal([]byte(raw), dst); jsonErr != nil {
		// Node: parse failure deletes the poisoned key and reads as undefined.
		_ = s.Delete(ctx, key)
		return false, nil
	}
	return true, nil
}

func (s *redisRuntimeStateStore) SetJSON(ctx context.Context, key string, value any, ttlMs int64) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.run(ctx, func(runCtx context.Context) error {
		return s.client.Set(runCtx, s.redisKey(key), encoded, normalizeTtlMsDuration(ttlMs)).Err()
	})
}

func (s *redisRuntimeStateStore) Delete(ctx context.Context, key string) error {
	return s.run(ctx, func(runCtx context.Context) error {
		return s.client.Del(runCtx, s.redisKey(key)).Err()
	})
}

// normalizeTtlMsDuration mirrors normalizeTtlMs (min 1ms).
func normalizeTtlMsDuration(ttlMs int64) time.Duration {
	if ttlMs < 1 {
		return time.Millisecond
	}
	return time.Duration(ttlMs) * time.Millisecond
}
