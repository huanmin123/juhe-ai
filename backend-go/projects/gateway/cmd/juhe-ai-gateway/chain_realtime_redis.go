package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

const realtimeConnectionRedisTTL = 10 * time.Minute

// realtimeConnectionRedis is the cross-instance per-API-key WS connection
// lease set (BUG-0362 同构根治): each live connection is a ZSET member
// `<sessionToken>` with score = lease expiry. The count is ZCARD after
// removing expired members, release is idempotent ZREM, and the 20s WS ping
// only renews the caller's own member. A crashed/lost connection therefore
// cannot be kept alive by other connections' pings, and the old shared-string
// INCR/DECR counter (whose residual was renewed by any active connection) is
// gone.
type realtimeConnectionRedis struct {
	client redis.UniversalClient
	prefix string
	clock  func() int64
}

func newChainRealtimeConnectionLimiter(client redis.UniversalClient, namespace string) *chainRealtimeConnectionLimiter {
	if client == nil || strings.TrimSpace(namespace) == "" {
		return &chainRealtimeConnectionLimiter{}
	}
	return &chainRealtimeConnectionLimiter{
		remote: &realtimeConnectionRedis{
			client: client,
			prefix: rediscfg.NamespacedKey("juhe-ai:realtime-connections", namespace) + ":",
			clock:  func() int64 { return time.Now().UnixMilli() },
		},
	}
}

const realtimeConnectionAcquireScript = `
local key = KEYS[1]
local limit = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local token = ARGV[4]
redis.call('ZREMRANGEBYSCORE', key, '-inf', now)
local current = redis.call('ZCARD', key)
if current >= limit then
  return {0, current}
end
redis.call('ZADD', key, now + ttl, token)
redis.call('PEXPIRE', key, ttl + 300000)
return {1, current + 1}
`

const realtimeConnectionReleaseScript = `
redis.call('ZREM', KEYS[1], ARGV[1])
return 1
`

const realtimeConnectionRefreshScript = `
local existing = redis.call('ZSCORE', KEYS[1], ARGV[1])
if not existing or tonumber(existing) <= tonumber(ARGV[2]) then
  return 0
end
redis.call('ZADD', KEYS[1], tonumber(ARGV[2]) + tonumber(ARGV[3]), ARGV[1])
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[3]) + 300000)
return 1
`

const realtimeConnectionCountScript = `
local now = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
return redis.call('ZCARD', KEYS[1])
`

func (r *realtimeConnectionRedis) key(apiKeyID string) string {
	return r.prefix + apiKeyID + ":slots"
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// newRealtimeConnectionToken 生成连接唯一租约标识（release/refresh 只作用于
// 自己的成员）。
func newRealtimeConnectionToken() string {
	var buffer [8]byte
	_, _ = rand.Read(buffer[:])
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + hex.EncodeToString(buffer[:])
}

func (r *realtimeConnectionRedis) acquire(ctx context.Context, apiKeyID string, limit int64) (string, bool, error) {
	token := newRealtimeConnectionToken()
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	values, err := r.client.Eval(runCtx, realtimeConnectionAcquireScript,
		[]string{r.key(apiKeyID)}, limit, realtimeConnectionRedisTTL.Milliseconds(), r.clock(), token).Result()
	if err != nil {
		return "", false, err
	}
	reply, ok := values.([]interface{})
	if !ok || len(reply) != 2 {
		return "", false, errors.New("realtime connection acquire 脚本返回格式错误")
	}
	acquired, _ := reply[0].(int64)
	if acquired != 1 {
		return "", false, nil
	}
	return token, true, nil
}

func (r *realtimeConnectionRedis) release(ctx context.Context, apiKeyID string, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	return r.client.Eval(runCtx, realtimeConnectionReleaseScript, []string{r.key(apiKeyID)}, token).Err()
}

// refresh 续租本连接的槽成员；Lua 脚本返回 0/1。refreshed=false 表示槽已不
// 存在（过期清理/释放/Redis 重启），不复活也不报错（C12：调用方据此留痕）。
func (r *realtimeConnectionRedis) refresh(ctx context.Context, apiKeyID string, token string) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, nil
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	refreshed, err := r.client.Eval(runCtx, realtimeConnectionRefreshScript,
		[]string{r.key(apiKeyID)}, token, r.clock(), realtimeConnectionRedisTTL.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return refreshed == 1, nil
}

func (r *realtimeConnectionRedis) count(ctx context.Context, apiKeyID string) (int64, error) {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	return r.client.Eval(runCtx, realtimeConnectionCountScript, []string{r.key(apiKeyID)}, r.clock()).Int64()
}
