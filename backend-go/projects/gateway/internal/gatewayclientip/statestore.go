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

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
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

// runtimeStateLuaStore 是支持 Lua 原子计算的存储扩展（DD-A1：熔断计数路径
// 的读改写必须原子，否则并发打点丢更新使熔断灵敏度退化）。可选接口：只由
// Redis 驱动实现（高频同 key 计数下沉 Redis 单线程，与 ratelimit/
// penalty-window 的原子计数惯例同款；WATCH 乐观锁在热点 key 高并发下冲突
// 重试雪崩，不适用）；未实现该接口的存储（测试 fake）按"驱动不支持"错误
// 透传，与连接失败同语义。
type runtimeStateLuaStore interface {
	// EvalLua 在存储侧原子执行一段 Lua 脚本。keys 传未加前缀的存储内 key，
	// 与 GetJSON/SetJSON/Delete 同规则统一拼接命名空间前缀。
	EvalLua(ctx context.Context, script string, keys []string, args ...any) (any, error)
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
	client     redis.UniversalClient
	keyPrefix  string
}

// NewRedisRuntimeStateStore mirrors createRuntimeStateStore(name) with
// runtimeStateDriver === 'redis'. namespace is runtimeConfig.redis.namespace;
// the Node namespace root "juhe-ai:" is always applied on top exactly once.
func NewRedisRuntimeStateStore(url, namespace, name string) (RuntimeStateStore, func(), error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil, errors.New("JUHE_AI_REDIS_URL 在 Redis runtime state driver 下必须配置")
	}
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: url})
	if err != nil {
		return nil, nil, err
	}
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
	return rediscfg.NamespacedKey(fmt.Sprintf("juhe-ai:state:%s:", sanitizeRedisKeyPart(name)), sanitizedNamespace), nil
}

// sanitizeRedisNamespacePart mirrors shared/redis-namespace.ts（清洗收敛到
// shared/platform/rediscfg，空值 error 文案保留在调用点）。
func sanitizeRedisNamespacePart(value string) (string, error) {
	normalized := rediscfg.SanitizeRedisNamespacePart(value)
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

// EvalLua 实现 runtimeStateLuaStore：Redis 驱动的原子脚本执行
//（stateOperationTimeout 预算内）。keys 传未加前缀的存储内 key，与
// GetJSON/SetJSON/Delete 同规则统一拼接命名空间前缀（脚本内 KEYS[n] 看到
// 的是完整 redis key）。
func (s *redisRuntimeStateStore) EvalLua(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = s.redisKey(key)
	}
	var result any
	err := s.run(ctx, func(runCtx context.Context) error {
		var evalErr error
		result, evalErr = s.client.Eval(runCtx, script, prefixedKeys, args...).Result()
		return evalErr
	})
	return result, err
}

// normalizeTtlMsDuration mirrors normalizeTtlMs (min 1ms).
func normalizeTtlMsDuration(ttlMs int64) time.Duration {
	if ttlMs < 1 {
		return time.Millisecond
	}
	return time.Duration(ttlMs) * time.Millisecond
}
