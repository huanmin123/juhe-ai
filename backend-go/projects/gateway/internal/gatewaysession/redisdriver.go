package gatewaysession

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Redis driver projection of shared/redis-client.ts + shared/redis-namespace.ts
// as consumed by runtime/session-affinity.service.ts.

// RedisClient mirrors the consumed RedisCommandClient surface.
type RedisClient interface {
	// Get returns (nil, nil) when the key is missing.
	Get(ctx context.Context, key string) (*string, error)
	// SetPX writes with a millisecond TTL (SET key value PX ttl).
	SetPX(ctx context.Context, key string, value string, ttlMs int64) error
	Del(ctx context.Context, keys ...string) error
	// Eval runs a Lua script; the result mirrors redis.eval (integers stay
	// int64, arrays decode to []any of strings).
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)
	// SendCommand mirrors client.sendCommand([...]) for the ZSET / PEXPIRE
	// helpers.
	SendCommand(ctx context.Context, args ...any) (any, error)
}

// GoRedisClient adapts a go-redis universal client to RedisClient.
type GoRedisClient struct {
	Client goredis.UniversalClient
}

// NewGoRedisClient wraps an existing go-redis client.
func NewGoRedisClient(client goredis.UniversalClient) *GoRedisClient {
	return &GoRedisClient{Client: client}
}

// DialGoRedisClient builds a go-redis client from a redis:// URL.
func DialGoRedisClient(redisURL string) (*GoRedisClient, error) {
	options, err := goredis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return &GoRedisClient{Client: goredis.NewClient(options)}, nil
}

// Get implements RedisClient.
func (c *GoRedisClient) Get(ctx context.Context, key string) (*string, error) {
	value, err := c.Client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// SetPX implements RedisClient.
func (c *GoRedisClient) SetPX(ctx context.Context, key string, value string, ttlMs int64) error {
	return c.Client.Set(ctx, key, value, time.Duration(ttlMs)*time.Millisecond).Err()
}

// Del implements RedisClient.
func (c *GoRedisClient) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return c.Client.Del(ctx, keys...).Err()
}

// Eval implements RedisClient.
func (c *GoRedisClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return c.Client.Eval(ctx, script, keys, args...).Result()
}

// SendCommand implements RedisClient.
func (c *GoRedisClient) SendCommand(ctx context.Context, args ...any) (any, error) {
	return c.Client.Do(ctx, args...).Result()
}

// redisNamespacedKey mirrors shared/redis-namespace.ts redisNamespacedKey.
const redisRootPrefix = "juhe-ai:"

var redisNamespaceSanitizePattern = regexp.MustCompile(`[^a-zA-Z0-9_.:-]+`)

// RedisNamespacedKey mirrors redisNamespacedKey with an explicit namespace
// value (runtimeConfig.redis.namespace projection).
func RedisNamespacedKey(namespace string, key string) (string, error) {
	normalized := strings.TrimSpace(key)
	if normalized == "" {
		return "", errors.New("Redis key 不能为空")
	}
	namespacePrefix, err := RedisNamespacePrefix(namespace)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(normalized, namespacePrefix) {
		return normalized, nil
	}
	if strings.HasPrefix(normalized, redisRootPrefix) {
		return namespacePrefix + normalized[len(redisRootPrefix):], nil
	}
	return namespacePrefix + normalized, nil
}

// RedisNamespacePrefix mirrors redisNamespacePrefix; the caller injects the
// configured namespace (the env-derived default lives in platform config).
func RedisNamespacePrefix(namespace string) (string, error) {
	sanitized, err := SanitizeRedisNamespacePart(namespace)
	if err != nil {
		return "", err
	}
	return redisRootPrefix + sanitized + ":", nil
}

// SanitizeRedisNamespacePart mirrors sanitizeRedisNamespacePart.
func SanitizeRedisNamespacePart(value string) (string, error) {
	normalized := strings.Trim(redisNamespaceSanitizePattern.ReplaceAllString(strings.TrimSpace(value), "_"), "_")
	if normalized == "" {
		return "", errors.New("Redis namespace 不能为空")
	}
	return normalized, nil
}

// encodeURIComponent mirrors the ECMAScript encodeURIComponent: unreserved
// characters stay verbatim, everything else is percent-encoded per UTF-8 byte
// with uppercase hex.
func encodeURIComponent(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Lua scripts, byte-identical to the Node service.
const redisSetSessionAffinityBindingScript = `
local current = redis.call('GET', KEYS[1])
local expected = ARGV[1]
if expected == '' then
  if current then
    return 0
  end
elseif current ~= expected then
  return 0
end
local new_value = ARGV[2]
local binding_ttl_ms = ARGV[3]
local index_ttl_ms = ARGV[4]
local expires_at = ARGV[5]
local old_index_count = tonumber(ARGV[6])
local session_key = ARGV[7]
for i = 1, old_index_count do
  redis.call('ZREM', KEYS[1 + i], session_key)
end
redis.call('SET', KEYS[1], new_value, 'PX', binding_ttl_ms)
for i = old_index_count + 2, #KEYS do
  redis.call('ZADD', KEYS[i], expires_at, session_key)
  redis.call('PEXPIRE', KEYS[i], index_ttl_ms)
end
return 1
`

const redisDeleteSessionAffinityBindingScript = `
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then
  return 0
end
redis.call('DEL', KEYS[1])
for i = 2, #KEYS do
  redis.call('ZREM', KEYS[i], ARGV[2])
end
return 1
`

const redisRefreshSessionAffinityBindingScript = `
local current = redis.call('GET', KEYS[1])
if current ~= ARGV[1] then
  return 0
end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
for i = 2, #KEYS do
  redis.call('ZADD', KEYS[i], ARGV[3], ARGV[4])
  redis.call('PEXPIRE', KEYS[i], ARGV[5])
end
return 1
`

// lazyRedisClient mirrors redisSessionAffinityClient's getRedisClient(url)
// lazy dial; the per-URL process-wide pool of Node collapses to one client
// per service instance (the service is a process singleton in production).
type lazyRedisClient struct {
	mu       sync.Mutex
	url      string
	delegate RedisClient
}

func newLazyRedisClient(redisURL string) *lazyRedisClient {
	return &lazyRedisClient{url: redisURL}
}

func (l *lazyRedisClient) client() (RedisClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.delegate != nil {
		return l.delegate, nil
	}
	if l.url == "" {
		return nil, errors.New("JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置")
	}
	client, err := DialGoRedisClient(l.url)
	if err != nil {
		return nil, err
	}
	l.delegate = client
	return client, nil
}

// Close drops the lazily dialed client (for tests).
func (l *lazyRedisClient) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if closer, ok := l.delegate.(interface{ Close() error }); ok {
		l.delegate = nil
		return closer.Close()
	}
	l.delegate = nil
	return nil
}

// Get implements RedisClient.
func (l *lazyRedisClient) Get(ctx context.Context, key string) (*string, error) {
	client, err := l.client()
	if err != nil {
		return nil, err
	}
	return client.Get(ctx, key)
}

// SetPX implements RedisClient.
func (l *lazyRedisClient) SetPX(ctx context.Context, key string, value string, ttlMs int64) error {
	client, err := l.client()
	if err != nil {
		return err
	}
	return client.SetPX(ctx, key, value, ttlMs)
}

// Del implements RedisClient.
func (l *lazyRedisClient) Del(ctx context.Context, keys ...string) error {
	client, err := l.client()
	if err != nil {
		return err
	}
	return client.Del(ctx, keys...)
}

// Eval implements RedisClient.
func (l *lazyRedisClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	client, err := l.client()
	if err != nil {
		return nil, err
	}
	return client.Eval(ctx, script, keys, args...)
}

// SendCommand implements RedisClient.
func (l *lazyRedisClient) SendCommand(ctx context.Context, args ...any) (any, error) {
	client, err := l.client()
	if err != nil {
		return nil, err
	}
	return client.SendCommand(ctx, args...)
}
