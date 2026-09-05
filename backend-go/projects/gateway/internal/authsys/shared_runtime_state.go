package authsys

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

// redisStateOperationTimeout mirrors runRedisOperationWithDeadline
// (shared/runtime-state-store.ts timeoutMs: 3_000).
const redisStateOperationTimeout = 3 * time.Second

// RedisStateStore is the consumed slice of the Node RuntimeStateStore
// (shared/runtime-state-store.ts) the shared captcha / login-guard drivers
// need: JSON read, atomic read-and-delete consumption, TTL writes, deletes,
// and the bounded incr counter. Keys are namespaced exactly like Node so the
// migration period keeps Go/Node interoperable on the same Redis state.
type RedisStateStore interface {
	GetJSON(ctx context.Context, key string, dst any) (bool, error)
	GetDeleteJSON(ctx context.Context, key string, dst any) (bool, error)
	SetJSON(ctx context.Context, key string, value any, ttlMs int64) error
	DeleteJSON(ctx context.Context, key string) error
	// Incr mirrors incr(key, {ttlMs, max}): the post-increment value is
	// returned; when max >= 0 and the next value would exceed it, the value is
	// returned without being written (Node incrWithMaxScript).
	Incr(ctx context.Context, key string, ttlMs int64, max int64) (int64, error)
}

// redisNamespacedStateStore is the go-redis implementation of RedisStateStore
// mirroring RedisRuntimeStateStore: keys are
// "<juhe-ai>:<namespace>:state:<name>:<key>" following
// redisNamespacedKey(`juhe-ai:state:<name>:`).
type redisNamespacedStateStore struct {
	client *redis.Client
	prefix string
}

// NewRedisNamespacedStateStore mirrors createRuntimeStateStore(name) with
// runtimeStateDriver === 'redis'. namespace is JUHE_AI_REDIS_NAMESPACE; the
// Node root "juhe-ai:" is always applied exactly once on top of it.
func NewRedisNamespacedStateStore(url, namespace, name string) (RedisStateStore, func(), error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil, errors.New("JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		return nil, nil, fmt.Errorf("parse redis state url: %w", err)
	}
	client := redis.NewClient(options)
	prefix, err := redisStateStoreKeyPrefix(namespace, name)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return &redisNamespacedStateStore{client: client, prefix: prefix}, func() { _ = client.Close() }, nil
}

// redisStateStoreKeyPrefix mirrors redisNamespacedKey(`juhe-ai:state:<name>:`)
// (shared/redis-namespace.ts): the namespace root is "juhe-ai:" and the
// configured namespace is inserted right after it, sanitized like
// sanitizeRedisNamespacePart.
func redisStateStoreKeyPrefix(namespace, name string) (string, error) {
	normalized := strings.TrimSpace(namespace)
	normalized = redisNamespaceSanitizePattern.ReplaceAllString(normalized, "_")
	normalized = strings.Trim(normalized, "_")
	if normalized == "" {
		return "", errors.New("Redis namespace 不能为空")
	}
	return fmt.Sprintf("juhe-ai:%s:state:%s:", normalized, sanitizeRedisStateKeyPart(name)), nil
}

var redisNamespaceSanitizePattern = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

// sanitizeRedisStateKeyPart mirrors sanitizeRedisKeyPart.
func sanitizeRedisStateKeyPart(value string) string {
	normalized := redisStateKeySanitizePattern.ReplaceAllString(strings.TrimSpace(value), "_")
	if normalized == "" {
		return "default"
	}
	return normalized
}

var redisStateKeySanitizePattern = regexp.MustCompile(`[^a-zA-Z0-9:_-]`)

func (s *redisNamespacedStateStore) redisKey(key string) string {
	return s.prefix + key
}

func (s *redisNamespacedStateStore) run(ctx context.Context, operation func(runCtx context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, redisStateOperationTimeout)
	defer cancel()
	return operation(runCtx)
}

func (s *redisNamespacedStateStore) GetJSON(ctx context.Context, key string, dst any) (bool, error) {
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
		_ = s.DeleteJSON(ctx, key)
		return false, nil
	}
	return true, nil
}

func (s *redisNamespacedStateStore) GetDeleteJSON(ctx context.Context, key string, dst any) (bool, error) {
	var raw string
	err := s.run(ctx, func(runCtx context.Context) error {
		var getErr error
		raw, getErr = s.client.GetDel(runCtx, s.redisKey(key)).Result()
		return getErr
	})
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if json.Unmarshal([]byte(raw), dst) != nil {
		return false, nil
	}
	return true, nil
}

func (s *redisNamespacedStateStore) SetJSON(ctx context.Context, key string, value any, ttlMs int64) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.run(ctx, func(runCtx context.Context) error {
		return s.client.Set(runCtx, s.redisKey(key), encoded, normalizeRedisTTL(ttlMs)).Err()
	})
}

func (s *redisNamespacedStateStore) DeleteJSON(ctx context.Context, key string) error {
	return s.run(ctx, func(runCtx context.Context) error {
		return s.client.Del(runCtx, s.redisKey(key)).Err()
	})
}

// incrWithMaxScript mirrors incrWithMaxScript in runtime-state-store.ts:
// a fresh key starts at 1 with the TTL; an existing key increments in place
// (repairing a missing TTL); an over-max next value is returned unwritten.
const incrWithMaxScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0') or 0
local next_value = current + 1
if ARGV[2] ~= '' and next_value > tonumber(ARGV[2]) then
  return next_value
end
if current == 0 then
  redis.call('SET', KEYS[1], tostring(next_value), 'PX', ARGV[1])
else
  redis.call('INCR', KEYS[1])
  if redis.call('PTTL', KEYS[1]) < 0 then
    redis.call('PEXPIRE', KEYS[1], ARGV[1])
  end
end
return next_value
`

func (s *redisNamespacedStateStore) Incr(ctx context.Context, key string, ttlMs int64, max int64) (int64, error) {
	var result int64
	err := s.run(ctx, func(runCtx context.Context) error {
		var evalErr error
		result, evalErr = s.client.Eval(runCtx, incrWithMaxScript, []string{s.redisKey(key)},
			formatRedisMillis(ttlMs), formatRedisMax(max)).Int64()
		return evalErr
	})
	return result, err
}

func formatRedisMillis(ttlMs int64) string {
	if ttlMs < 1 {
		ttlMs = 1
	}
	return fmt.Sprintf("%d", ttlMs)
}

func formatRedisMax(max int64) string {
	if max < 0 {
		return ""
	}
	return fmt.Sprintf("%d", max)
}

func normalizeRedisTTL(ttlMs int64) time.Duration {
	if ttlMs < 1 {
		return time.Millisecond
	}
	return time.Duration(ttlMs) * time.Millisecond
}
