package gatewayrouting

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisRouteCounterScript mirrors the Node Lua script: INCR the counter,
// refresh its TTL, and return the zero-based modulo index.
const redisRouteCounterScript = `
      local value = redis.call('INCR', KEYS[1])
      redis.call('PEXPIRE', KEYS[1], ARGV[2])
      return (value - 1) % tonumber(ARGV[1])
    `

// RedisRouteStateCounter is the go-redis implementation of RedisRouteCounter
// (Node runRedisOperationWithDeadline + client.eval in
// api-key-group-route-selector.service.ts). One client is shared per URL,
// mirroring the Node redis-client client generation cache.
type RedisRouteStateCounter struct {
	URL     string
	TTLMS   int64
	Timeout time.Duration

	once   sync.Once
	client *redis.Client
	opts   *redis.Options
	err    error
}

// NewRedisRouteStateCounter builds a counter for the given state URL.
func NewRedisRouteStateCounter(stateURL string) *RedisRouteStateCounter {
	return &RedisRouteStateCounter{
		URL:     stateURL,
		TTLMS:   RedisRouteStateTtlMs,
		Timeout: time.Duration(RedisRouteStateOperationTimeoutMs) * time.Millisecond,
	}
}

func (c *RedisRouteStateCounter) clientForUse() (*redis.Client, error) {
	c.once.Do(func() {
		opts, err := redis.ParseURL(c.URL)
		if err != nil {
			c.err = err
			return
		}
		c.opts = opts
		c.client = redis.NewClient(opts)
	})
	return c.client, c.err
}

// NextRouteCounterIndex mirrors nextRedisRouteCounterIndex's Redis leg: a
// 3-second operation deadline wraps the EVAL, and a non-numeric result fails
// with the original Chinese message.
func (c *RedisRouteStateCounter) NextRouteCounterIndex(ctx context.Context, key string, modulo int64) (int64, error) {
	client, err := c.clientForUse()
	if err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	operationCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	result, err := client.Eval(operationCtx, redisRouteCounterScript, []string{key},
		strconv.FormatInt(modulo, 10),
		strconv.FormatInt(c.TTLMS, 10),
	).Result()
	if err != nil {
		return 0, err
	}
	switch value := result.(type) {
	case int64:
		if value < 0 {
			// Node clamps with Math.max(0, Math.trunc(index)).
			return 0, nil
		}
		return value, nil
	case string:
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return 0, &RouteStateURLError{Message: ErrRedisRouteCounterInvalidResult}
		}
		if parsed < 0 {
			return 0, nil
		}
		return parsed, nil
	default:
		return 0, &RouteStateURLError{Message: ErrRedisRouteCounterInvalidResult}
	}
}
