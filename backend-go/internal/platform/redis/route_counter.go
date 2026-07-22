package redis

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type RouteCounterMode string

const (
	RouteCounterModeRoundRobin RouteCounterMode = "round-robin"
	RouteCounterModeWeighted   RouteCounterMode = "weighted"

	RouteCounterMaxTTL = 30 * 24 * time.Hour
)

const routeCounterLua = `
local modulo = tonumber(ARGV[1])
local ttl_ms = tonumber(ARGV[2])
if modulo == nil or modulo <= 0 then
  return redis.error_reply('route counter modulo must be greater than zero')
end
if ttl_ms == nil or ttl_ms <= 0 then
  return redis.error_reply('route counter ttl must be greater than zero')
end
local value = redis.call('INCR', KEYS[1])
redis.call('PEXPIRE', KEYS[1], ttl_ms)
return (value - 1) % modulo
`

var routeCounterScript = goredis.NewScript(routeCounterLua)

type RouteCounter struct {
	namespace string
	run       func(context.Context, string, int64, int64) (int64, error)
}

func NewRouteCounter(client *Client, namespace string) (*RouteCounter, error) {
	if client == nil || client.client == nil {
		return nil, fmt.Errorf("redis state client is required")
	}
	normalizedNamespace, err := normalizeAccountConcurrencyNamespace(namespace)
	if err != nil {
		return nil, err
	}
	return &RouteCounter{
		namespace: normalizedNamespace,
		run: func(ctx context.Context, key string, modulo int64, ttlMillis int64) (int64, error) {
			return routeCounterScript.Run(
				ctx,
				client.client,
				[]string{key},
				strconv.FormatInt(modulo, 10),
				strconv.FormatInt(ttlMillis, 10),
			).Int64()
		},
	}, nil
}

func (c *RouteCounter) NextIndex(
	ctx context.Context,
	strategyID string,
	mode RouteCounterMode,
	modulo int,
) (int, error) {
	if c == nil || c.run == nil {
		return 0, fmt.Errorf("route counter is required")
	}
	if ctx == nil {
		return 0, fmt.Errorf("route counter context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if modulo <= 0 {
		return 0, fmt.Errorf("route counter modulo must be greater than zero")
	}
	key, err := routeCounterKey(c.namespace, strategyID, mode)
	if err != nil {
		return 0, err
	}
	index, err := c.run(ctx, key, int64(modulo), RouteCounterMaxTTL.Milliseconds())
	if err != nil {
		return 0, fmt.Errorf("advance route counter: %w", err)
	}
	if index < 0 || index >= int64(modulo) {
		return 0, fmt.Errorf("route counter returned out-of-range index %d for modulo %d", index, modulo)
	}
	return int(index), nil
}

func routeCounterKey(namespace string, strategyID string, mode RouteCounterMode) (string, error) {
	normalizedNamespace, err := normalizeAccountConcurrencyNamespace(namespace)
	if err != nil {
		return "", err
	}
	strategyID = strings.TrimSpace(strategyID)
	if strategyID == "" {
		return "", fmt.Errorf("route strategy id is required")
	}
	switch mode {
	case RouteCounterModeRoundRobin, RouteCounterModeWeighted:
	default:
		return "", fmt.Errorf("unsupported route counter mode %q", mode)
	}
	encodedStrategyID := base64.RawURLEncoding.EncodeToString([]byte(strategyID))
	// Node omitted the deployment namespace here; Go keeps strategy sharing
	// without allowing deployments on the same Redis server to share counters.
	return "juhe-ai:" + normalizedNamespace + ":route-state:api-key-group:" + string(mode) + ":" + encodedStrategyID, nil
}
