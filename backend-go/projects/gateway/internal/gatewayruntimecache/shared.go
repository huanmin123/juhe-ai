package gatewayruntimecache

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// SharedCache mirrors the Node createSharedJsonCache surface actually used by
// runtime-cache.service.ts: typed get (JSON decode), set with TTL and a whole
// cache clear. A get miss or decode failure must behave like Node: the caller
// falls through to the loader and never poisons the process cache.
type SharedCache interface {
	Get(ctx context.Context, key string, dst any) (bool, error)
	Set(ctx context.Context, key string, value any, ttl time.Duration) error
	Clear(ctx context.Context) error
}

// SharedCacheFactory builds named shared caches. Name strings are the Node
// cache names ("gateway:settings", "gateway:group-usage-access", ...) so the
// Redis key layout stays auditable next to the Node deployment.
type SharedCacheFactory interface {
	Cache(name string) SharedCache
}

// SharedCacheNamespaceFunc adapts a function to SharedCacheFactory.
type SharedCacheNamespaceFunc func(name string) SharedCache

// Cache implements SharedCacheFactory.
func (f SharedCacheNamespaceFunc) Cache(name string) SharedCache { return f(name) }

// RedisSharedCache is the go-redis backed SharedCache. Keys are
// "<namespace>:<name>:<key>" following the Node redisNamespacedKey convention
// (namespace already carries the juhe-ai: root, same as key_model_runtime).
type RedisSharedCache struct {
	client *redis.Client
	prefix string
}

// NewRedisSharedCacheFactory parses the Redis URL and returns a factory that
// namespaced every cache under "<namespace>:<name>:".
func NewRedisSharedCacheFactory(url, namespace string) (SharedCacheFactory, func(), error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil, errors.New("gatewayruntimecache Redis URL 不能为空")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		return nil, nil, err
	}
	client := redis.NewClient(options)
	normalized := strings.TrimRight(strings.TrimSpace(namespace), ":")
	if !strings.HasPrefix(normalized, "juhe-ai:") {
		normalized = "juhe-ai:" + normalized
	}
	if normalized == "juhe-ai:" {
		normalized = "juhe-ai"
	}
	closeFunc := func() { _ = client.Close() }
	return SharedCacheNamespaceFunc(func(name string) SharedCache {
		return &RedisSharedCache{client: client, prefix: normalized + ":" + name + ":"}
	}), closeFunc, nil
}

func (c *RedisSharedCache) fullKey(key string) string { return c.prefix + key }

// Get mirrors the Node JSON cache read: a Redis miss returns (false, nil).
func (c *RedisSharedCache) Get(ctx context.Context, key string, dst any) (bool, error) {
	raw, err := c.client.Get(ctx, c.fullKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

// Set stores the JSON encoded value with the TTL (Node uses SET ... EX/PX).
func (c *RedisSharedCache) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.client.Set(ctx, c.fullKey(key), encoded, ttl).Err()
}

// Clear deletes every key under the cache prefix (SCAN + DEL batches).
func (c *RedisSharedCache) Clear(ctx context.Context) error {
	pattern := c.prefix + "*"
	var cursor uint64
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 200).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := c.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}
