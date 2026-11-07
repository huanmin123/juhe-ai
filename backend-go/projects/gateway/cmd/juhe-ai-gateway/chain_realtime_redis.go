package main

import (
	"context"
	"errors"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

const realtimeConnectionRedisTTL = 10 * time.Minute

// realtimeConnectionRedis is the cross-instance per-API-key WS connection
// counter. The TTL is a crash-leak backstop, not the session lifetime: the
// bridge refreshes the key on every 20s ping, so a crashed instance loses its
// connection slots within 10 minutes while active sessions stay counted.
type realtimeConnectionRedis struct {
	client redis.UniversalClient
	prefix string
}

func newChainRealtimeConnectionLimiter(client redis.UniversalClient, namespace string) *chainRealtimeConnectionLimiter {
	if client == nil || strings.TrimSpace(namespace) == "" {
		return &chainRealtimeConnectionLimiter{}
	}
	return &chainRealtimeConnectionLimiter{
		remote: &realtimeConnectionRedis{
			client: client,
			prefix: rediscfg.NamespacedKey("juhe-ai:realtime-connections", namespace) + ":",
		},
	}
}

const realtimeConnectionAcquireScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
local limit = tonumber(ARGV[1])
if current >= limit then
  return {0, current}
end
local next = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return {1, next}
`

const realtimeConnectionReleaseScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0')
if current <= 1 then
  redis.call('DEL', KEYS[1])
  return 0
end
return redis.call('DECR', KEYS[1])
`

const realtimeConnectionRefreshScript = `
if redis.call('EXISTS', KEYS[1]) == 1 then
  return redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return 0
`

func (r *realtimeConnectionRedis) key(apiKeyID string) string {
	return r.prefix + apiKeyID
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (r *realtimeConnectionRedis) acquire(ctx context.Context, apiKeyID string, limit int64) (bool, error) {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	values, err := r.client.Eval(runCtx, realtimeConnectionAcquireScript,
		[]string{r.key(apiKeyID)}, limit, realtimeConnectionRedisTTL.Milliseconds()).Result()
	if err != nil {
		return false, err
	}
	reply, ok := values.([]interface{})
	if !ok || len(reply) != 2 {
		return false, errors.New("realtime connection acquire 脚本返回格式错误")
	}
	acquired, _ := reply[0].(int64)
	return acquired == 1, nil
}

func (r *realtimeConnectionRedis) release(ctx context.Context, apiKeyID string) error {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	return r.client.Eval(runCtx, realtimeConnectionReleaseScript, []string{r.key(apiKeyID)}).Err()
}

func (r *realtimeConnectionRedis) refresh(ctx context.Context, apiKeyID string) error {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	return r.client.Eval(runCtx, realtimeConnectionRefreshScript,
		[]string{r.key(apiKeyID)}, realtimeConnectionRedisTTL.Milliseconds()).Err()
}

func (r *realtimeConnectionRedis) count(ctx context.Context, apiKeyID string) (int64, error) {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), 3*time.Second)
	defer cancel()
	value, err := r.client.Get(runCtx, r.key(apiKeyID)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return value, err
}
