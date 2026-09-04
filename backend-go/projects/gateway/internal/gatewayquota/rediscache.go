package gatewayquota

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// redisRootPrefix mirrors redis-namespace.ts.
const redisRootPrefix = "juhe-ai:"

var redisNamespaceSanitizePattern = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

// SanitizeRedisNamespacePart mirrors sanitizeRedisNamespacePart.
func SanitizeRedisNamespacePart(value string) (string, error) {
	normalized := strings.Trim(redisNamespaceSanitizePattern.ReplaceAllString(strings.TrimSpace(value), "_"), "_")
	if normalized == "" {
		return "", errors.New("Redis namespace 不能为空")
	}
	return normalized, nil
}

// RedisNamespacedKey mirrors redisNamespacedKey: the effective prefix is
// juhe-ai:<namespace>:, an existing juhe-ai: root on the key is collapsed so
// keys never double the root prefix.
func RedisNamespacedKey(namespace, key string) (string, error) {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return "", errors.New("Redis key 不能为空")
	}
	namespacePart, err := SanitizeRedisNamespacePart(namespace)
	if err != nil {
		return "", err
	}
	prefix := redisRootPrefix + namespacePart + ":"
	if strings.HasPrefix(normalized, prefix) {
		return normalized, nil
	}
	if strings.HasPrefix(normalized, redisRootPrefix) {
		return prefix + normalized[len(redisRootPrefix):], nil
	}
	return prefix + normalized, nil
}

// sanitizeRedisKeyPart mirrors cache.ts sanitizeRedisKeyPart.
func sanitizeRedisKeyPart(value string) string {
	sanitized := regexp.MustCompile(`[^a-zA-Z0-9:_-]`).ReplaceAllString(strings.TrimSpace(value), "_")
	if sanitized == "" {
		return "default"
	}
	return sanitized
}

// normalizeTtlMs mirrors normalizeTtlMs.
func normalizeTtlMs(ttl time.Duration) time.Duration {
	if ttl < time.Millisecond {
		return time.Millisecond
	}
	return ttl
}

// redisOperationTimeout mirrors runRedisOperationWithDeadline timeoutMs 3000.
const redisOperationTimeout = 3 * time.Second

// sharedCacheVersionTTL mirrors the 30d namespace version retention.
const sharedCacheVersionTTL = 30 * 24 * time.Hour

// RedisRuntimeState is the RedisRuntimeStateStore subset for the quota
// snapshot: GET/SET/DEL of JSON documents under
// juhe-ai:<namespace>:juhe-ai:state:<storeName>:<key>.
type RedisRuntimeState struct {
	client *redis.Client
	prefix string
}

// NewRedisRuntimeStateStore mirrors createRuntimeStateStore('...') with the
// redis driver (keyPrefix = redisNamespacedKey(`juhe-ai:state:<name>:`)).
func NewRedisRuntimeStateStore(client *redis.Client, namespace, storeName string) (*RedisRuntimeState, error) {
	if client == nil {
		return nil, errors.New("gatewayquota redis runtime state requires a redis client")
	}
	prefix, err := RedisNamespacedKey(namespace, "juhe-ai:state:"+sanitizeRedisKeyPart(storeName)+":")
	if err != nil {
		return nil, err
	}
	return &RedisRuntimeState{client: client, prefix: prefix}, nil
}

func (s *RedisRuntimeState) key(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", errors.New("Redis key 不能为空")
	}
	return s.prefix + key, nil
}

// GetJSON mirrors RedisRuntimeStateStore.getJson: a malformed document is
// deleted and reported as absent; transport errors propagate.
func (s *RedisRuntimeState) GetJSON(ctx context.Context, storeName, key string, target any) (bool, error) {
	ctx, cancel := context.WithTimeout(ensureCtx(ctx), redisOperationTimeout)
	defer cancel()
	location, err := s.key(key)
	if err != nil {
		return false, err
	}
	raw, err := s.client.Get(ctx, location).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		_ = s.client.Del(ctx, location).Err()
		return false, nil
	}
	return true, nil
}

// SetJSON mirrors setJson.
func (s *RedisRuntimeState) SetJSON(ctx context.Context, storeName, key string, value any, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ensureCtx(ctx), redisOperationTimeout)
	defer cancel()
	location, err := s.key(key)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, location, payload, normalizeTtlMs(ttl)).Err()
}

// Delete mirrors delete.
func (s *RedisRuntimeState) Delete(ctx context.Context, storeName, key string) error {
	ctx, cancel := context.WithTimeout(ensureCtx(ctx), redisOperationTimeout)
	defer cancel()
	location, err := s.key(key)
	if err != nil {
		return err
	}
	return s.client.Del(ctx, location).Err()
}

// RedisSharedCache is the RedisSharedJsonCache subset used by the quota
// caches: versioned value keys plus a per-version sorted-set index so Clear
// removes exactly the tracked keys (byte-compatible with Node's layout, so
// the Go gateway and the Node server share entries during migration).
type RedisSharedCache struct {
	client         *redis.Client
	keyPrefix      string
	indexKeyPrefix string
	versionKey     string
}

// NewRedisSharedCache mirrors new RedisSharedJsonCache({name}).
func NewRedisSharedCache(client *redis.Client, namespace, name string) (*RedisSharedCache, error) {
	if client == nil {
		return nil, errors.New("gatewayquota redis shared cache requires a redis client")
	}
	safeName := sanitizeRedisKeyPart(name)
	keyPrefix, err := RedisNamespacedKey(namespace, "juhe-ai:cache:"+safeName+":")
	if err != nil {
		return nil, err
	}
	indexKeyPrefix, err := RedisNamespacedKey(namespace, "juhe-ai:cache-index:"+safeName+":")
	if err != nil {
		return nil, err
	}
	versionKey, err := RedisNamespacedKey(namespace, "juhe-ai:cache-version:"+safeName)
	if err != nil {
		return nil, err
	}
	return &RedisSharedCache{client: client, keyPrefix: keyPrefix, indexKeyPrefix: indexKeyPrefix, versionKey: versionKey}, nil
}

// nextCacheVersion mirrors nextCacheVersion (`${Date.now()}-${hex}`).
func nextCacheVersion(nowMs int64) string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%d-%s", nowMs, hex.EncodeToString(buf))
}

// namespaceVersion mirrors namespaceVersionWithClient: reads the current
// version, optionally creating it; without creation a missing version yields
// the read-only-miss marker.
func (c *RedisSharedCache) namespaceVersion(ctx context.Context, createVersion bool) (string, error) {
	existing, err := c.client.Get(ctx, c.versionKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	if !createVersion {
		return "read-only-miss", nil
	}
	version := nextCacheVersion(time.Now().UnixMilli())
	inserted, err := c.client.SetNX(ctx, c.versionKey, version, sharedCacheVersionTTL).Result()
	if err != nil {
		return "", err
	}
	if inserted {
		return version, nil
	}
	current, err := c.client.Get(ctx, c.versionKey).Result()
	if err != nil {
		return version, nil
	}
	return current, nil
}

// Get mirrors RedisSharedJsonCache.get: versioned GET + JSON decode with
// delete-on-corruption.
func (c *RedisSharedCache) Get(ctx context.Context, key string, target any) (bool, error) {
	ctx, cancel := context.WithTimeout(ensureCtx(ctx), redisOperationTimeout)
	defer cancel()
	version, err := c.namespaceVersion(ctx, false)
	if err != nil {
		return false, err
	}
	location := c.keyPrefix + version + ":" + key
	raw, err := c.client.Get(ctx, location).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		_ = c.client.Del(ctx, location).Err()
		return false, nil
	}
	return true, nil
}

// Set mirrors RedisSharedJsonCache.set: versioned SET with PX, index
// tracking (ZADD score=now, PEXPIRE max(ttl, 60s)) and overflow trimming.
func (c *RedisSharedCache) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ensureCtx(ctx), redisOperationTimeout)
	defer cancel()
	ttlMs := normalizeTtlMs(ttl)
	version, err := c.namespaceVersion(ctx, true)
	if err != nil {
		return err
	}
	location := c.keyPrefix + version + ":" + key
	indexKey := c.indexKeyPrefix + version
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := c.client.Set(ctx, location, payload, ttlMs).Err(); err != nil {
		return err
	}
	if err := c.client.ZAdd(ctx, indexKey, redis.Z{Score: float64(time.Now().UnixMilli()), Member: location}).Err(); err != nil {
		return err
	}
	indexTTL := ttlMs
	if indexTTL < time.Minute {
		indexTTL = time.Minute
	}
	if err := c.client.PExpire(ctx, indexKey, indexTTL).Err(); err != nil {
		return err
	}
	count, err := c.client.ZCard(ctx, indexKey).Result()
	if err != nil {
		return err
	}
	overflow := count - int64(apiKeyQuotaCacheMax)
	if overflow <= 0 {
		return nil
	}
	stale, err := c.client.ZRange(ctx, indexKey, 0, overflow-1).Result()
	if err != nil || len(stale) == 0 {
		return err
	}
	if err := c.client.Del(ctx, stale...).Err(); err != nil {
		return err
	}
	return c.client.ZRem(ctx, indexKey, toAnySlice(stale)...).Err()
}

// Clear mirrors RedisSharedJsonCache.clear: delete every indexed key of the
// current version, drop the index and rotate the version.
func (c *RedisSharedCache) Clear(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ensureCtx(ctx), redisOperationTimeout)
	defer cancel()
	version, err := c.namespaceVersion(ctx, true)
	if err != nil {
		return err
	}
	indexKey := c.indexKeyPrefix + version
	indexedKeys, err := c.client.ZRange(ctx, indexKey, 0, -1).Result()
	if err != nil {
		return err
	}
	if len(indexedKeys) > 0 {
		if err := c.client.Del(ctx, indexedKeys...).Err(); err != nil {
			return err
		}
	}
	if err := c.client.Del(ctx, indexKey).Err(); err != nil {
		return err
	}
	return c.client.Set(ctx, c.versionKey, nextCacheVersion(time.Now().UnixMilli()), sharedCacheVersionTTL).Err()
}
