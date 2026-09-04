package gatewayhybrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// Redis adapters for the hybrid runtime state (affinity bindings) and the
// scoring shared JSON cache. Key layout mirrors the Node drivers:
//
//   runtime state: juhe-ai:<ns>:state:gateway-hybrid-route-affinity:<key>
//   shared cache:  juhe-ai:<ns>:cache:gateway:hybrid-scoring-result:<key>
//
// redisNamespacedKey prepends juhe-ai:<namespace>: unless already present.

// RedisRuntimeStateStore is the Redis runtime-state driver behind
// RuntimeStateStore (Node RedisRuntimeStateStore for
// 'gateway-hybrid-route-affinity').
type RedisRuntimeStateStore struct {
	client *redis.Client
	prefix string
}

// NewRedisRuntimeStateStore builds the affinity state store. namespace
// accepts the short namespace or the full juhe-ai: prefix.
func NewRedisRuntimeStateStore(client *redis.Client, namespace string) (*RedisRuntimeStateStore, error) {
	if client == nil || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("hybrid affinity Redis client and namespace are required")
	}
	return &RedisRuntimeStateStore{
		client: client,
		prefix: namespacedKey(namespace, "state:gateway-hybrid-route-affinity:"),
	}, nil
}

// GetJSON mirrors getJson: missing or unparseable values read as absent
// (unparseable values are deleted like the Node catch).
func (store *RedisRuntimeStateStore) GetJSON(ctx context.Context, key string, value any) (bool, error) {
	raw, err := store.client.Get(ctx, store.prefix+key).Result()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), value); err != nil {
		_ = store.client.Del(ctx, store.prefix+key).Err()
		return false, nil
	}
	return true, nil
}

// SetJSON mirrors setJson (JSON body, PX TTL).
func (store *RedisRuntimeStateStore) SetJSON(ctx context.Context, key string, value any, ttlMs int64) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return store.client.Set(ctx, store.prefix+key, encoded, time.Duration(ttlMs)*time.Millisecond).Err()
}

// RedisSharedJSONCache is the Redis cache driver behind SharedJSONCache
// (Node DriverSharedJsonCache for 'gateway:hybrid-scoring-result'). Clear
// uses a prefix scan, mirroring the Node background clear semantics.
type RedisSharedJSONCache struct {
	client *redis.Client
	prefix string
}

// NewRedisSharedJSONCache builds the scoring shared cache adapter.
func NewRedisSharedJSONCache(client *redis.Client, namespace string) (*RedisSharedJSONCache, error) {
	if client == nil || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("hybrid scoring Redis client and namespace are required")
	}
	return &RedisSharedJSONCache{
		client: client,
		prefix: namespacedKey(namespace, "cache:gateway:hybrid-scoring-result:"),
	}, nil
}

// Get mirrors DriverSharedJsonCache.get: missing keys read as nil.
func (cache *RedisSharedJSONCache) Get(ctx context.Context, key string) (*HybridScoringCacheEntry, error) {
	raw, err := cache.client.Get(ctx, cache.prefix+key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entry HybridScoringCacheEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil, nil
	}
	return &entry, nil
}

// Set mirrors DriverSharedJsonCache.set (JSON body, PX TTL).
func (cache *RedisSharedJSONCache) Set(ctx context.Context, key string, entry HybridScoringCacheEntry, ttlMs int64) error {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return cache.client.Set(ctx, cache.prefix+key, encoded, time.Duration(ttlMs)*time.Millisecond).Err()
}

// Clear deletes every entry under the cache prefix (best effort, mirroring
// clearSharedJsonCacheInBackground).
func (cache *RedisSharedJSONCache) Clear(ctx context.Context) error {
	pattern := cache.prefix + "*"
	var cursor uint64
	for {
		keys, next, err := cache.client.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := cache.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// namespacedKey mirrors redisNamespacedKey + the Go-side normalization used
// by business/key_model_runtime: accept the short namespace or the full
// juhe-ai: prefix, never double-prefix.
func namespacedKey(namespace string, key string) string {
	normalized := strings.TrimRight(strings.TrimSpace(namespace), ":")
	if !strings.HasPrefix(normalized, "juhe-ai:") {
		normalized = "juhe-ai:" + normalized
	}
	return fmt.Sprintf("%s:%s", normalized, key)
}
