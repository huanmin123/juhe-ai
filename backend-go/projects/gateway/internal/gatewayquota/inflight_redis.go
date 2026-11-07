package gatewayquota

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// redisInflightQuota stores reserved cost in a single Redis hash plus an
// expiry ZSET. All keys share one hash tag (`<namespace>:quota`) so the
// multi-scope check/commit Lua script is atomic on Redis Cluster as well as
// single-node deployments.
type redisInflightQuota struct {
	client      redis.UniversalClient
	hashKey     string
	expiryKey   string
	operationTO time.Duration
}

type redisInflightSnapshotState struct {
	ScopeKey        string
	ReservedCostUsd float64
}

const redisInflightOperationTimeout = 3 * time.Second

const redisInflightReserveScript = `
local hash = KEYS[1]
local zset = KEYS[2]
local cost = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local expired = redis.call('ZRANGEBYSCORE', zset, '-inf', now, 'LIMIT', 0, 200)
for _, member in ipairs(expired) do
  redis.call('HDEL', hash, member)
  redis.call('ZREM', zset, member)
end
local count = (#ARGV - 3) / 16
local idx = 4
for i = 1, count do
  local token = ARGV[idx]
  local base = idx + 1
  local reserved = tonumber(redis.call('HGET', hash, token) or '0') or 0
  local projected = reserved + cost
  for w = 1, 5 do
    local current = tonumber(ARGV[base + w - 1])
    local enabled = tonumber(ARGV[base + 5 + w - 1])
    local limit = tonumber(ARGV[base + 10 + w - 1])
    if enabled == 1 and current + projected > limit then
      return {0, i}
    end
  end
  idx = base + 15
end
idx = 4
for i = 1, count do
  local token = ARGV[idx]
  redis.call('HINCRBYFLOAT', hash, token, cost)
  redis.call('ZADD', zset, now + ttl, token)
  idx = idx + 16
end
return {1, 0}
`

const redisInflightReleaseScript = `
local hash = KEYS[1]
local zset = KEYS[2]
local cost = tonumber(ARGV[1])
for i = 2, #ARGV do
  local token = ARGV[i]
  local current = tonumber(redis.call('HGET', hash, token) or '0') or 0
  local next = current - cost
  if next <= 0 then
    redis.call('HDEL', hash, token)
    redis.call('ZREM', zset, token)
  else
    redis.call('HSET', hash, token, tostring(next))
  end
end
return 1
`

const redisInflightSnapshotScript = `
local hash = KEYS[1]
local zset = KEYS[2]
local now = tonumber(ARGV[1])
local entries = redis.call('HGETALL', hash)
local out = {}
for i = 1, #entries, 2 do
  local token = entries[i]
  local expiry = tonumber(redis.call('ZSCORE', zset, token) or '0') or 0
  if expiry > now then
    table.insert(out, token)
    table.insert(out, entries[i + 1])
  else
    redis.call('HDEL', hash, token)
    redis.call('ZREM', zset, token)
  end
end
return out
`

func newRedisInflightQuota(client redis.UniversalClient, namespace string) (*redisInflightQuota, error) {
	if client == nil {
		return nil, errors.New("inflight quota Redis client 不能为空")
	}
	ns := rediscfg.CanonicalRedisNamespace(namespace)
	if ns == "" {
		ns = "default"
	}
	tag := ns + ":quota"
	return &redisInflightQuota{
		client:      client,
		hashKey:     "juhe-ai:{" + tag + "}:inflight",
		expiryKey:   "juhe-ai:{" + tag + "}:inflight-expiry",
		operationTO: redisInflightOperationTimeout,
	}, nil
}

func (r *redisInflightQuota) reserve(ctx context.Context, input ReserveInput) (InflightDecision, []inflightRelease, error) {
	scopes := []InflightScope{{
		ScopeKey:     reserveStateKey(input),
		Limits:       input.Limits,
		CurrentCosts: input.CurrentCosts,
	}}
	return r.reserveScopesInternal(ctx, scopes, normalizedCost(input.EstimatedCostUsd))
}

func (r *redisInflightQuota) reserveScopes(ctx context.Context, input ScopeReserveInput) (InflightDecision, []inflightRelease, error) {
	scopes := make([]InflightScope, 0, len(input.Scopes))
	for _, scope := range input.Scopes {
		if scope.ScopeKey == "" {
			continue
		}
		scopes = append(scopes, scope)
	}
	return r.reserveScopesInternal(ctx, scopes, normalizedCost(input.EstimatedCostUsd))
}

func (r *redisInflightQuota) reserveScopesInternal(ctx context.Context, scopes []InflightScope, cost float64) (InflightDecision, []inflightRelease, error) {
	if cost <= 0 || len(scopes) == 0 {
		return InflightDecision{Allowed: true}, nil, nil
	}
	nowMs := time.Now().UnixMilli()
	args := make([]any, 0, 3+len(scopes)*16)
	args = append(args, strconv.FormatFloat(cost, 'f', -1, 64), reservationLeakTTL.Milliseconds(), nowMs)
	for _, scope := range scopes {
		args = append(args, scopeToken(scope.ScopeKey),
			scope.CurrentCosts.Hourly, scope.CurrentCosts.Daily, scope.CurrentCosts.Weekly,
			scope.CurrentCosts.Monthly, scope.CurrentCosts.Total)
		enabled, limits := scopeLimitArgs(scope.Limits)
		for _, value := range enabled {
			args = append(args, value)
		}
		for _, value := range limits {
			args = append(args, value)
		}
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), r.operationTO)
	defer cancel()
	raw, err := r.client.Eval(runCtx, redisInflightReserveScript, []string{r.hashKey, r.expiryKey}, args...).Result()
	if err != nil {
		return InflightDecision{}, nil, fmt.Errorf("inflight quota reserve: %w", err)
	}
	reply, ok := raw.([]interface{})
	if !ok || len(reply) != 2 {
		return InflightDecision{}, nil, errors.New("inflight quota reserve 脚本返回格式错误")
	}
	if int64Reply(reply[0]) != 1 {
		deniedIndex := int(int64Reply(reply[1]))
		denied := ""
		if deniedIndex >= 1 && deniedIndex <= len(scopes) {
			denied = scopes[deniedIndex-1].ScopeKey
		}
		return InflightDecision{
			Allowed:             false,
			EstimatedCostUsd:    cost,
			HasEstimatedCostUsd: true,
			DeniedScopeKey:      denied,
		}, nil, nil
	}
	releases := make([]inflightRelease, 0, len(scopes))
	for _, scope := range scopes {
		releases = append(releases, inflightRelease{stateKey: scope.ScopeKey, costUsd: cost})
	}
	return InflightDecision{
		Allowed:             true,
		EstimatedCostUsd:    cost,
		HasEstimatedCostUsd: true,
	}, releases, nil
}

func scopeLimitArgs(limits RequestQuotaLimits) ([5]int, [5]float64) {
	var enabled [5]int
	var values [5]float64
	if limits.Hourly != nil {
		if limits.Hourly.Enabled {
			enabled[0] = 1
		}
		values[0] = limits.Hourly.Limit
	}
	if limits.Daily != nil {
		if limits.Daily.Enabled {
			enabled[1] = 1
		}
		values[1] = limits.Daily.Limit
	}
	if limits.Weekly != nil {
		if limits.Weekly.Enabled {
			enabled[2] = 1
		}
		values[2] = limits.Weekly.Limit
	}
	if limits.Monthly != nil {
		if limits.Monthly.Enabled {
			enabled[3] = 1
		}
		values[3] = limits.Monthly.Limit
	}
	if limits.Total != nil {
		if limits.Total.Enabled {
			enabled[4] = 1
		}
		values[4] = limits.Total.Limit
	}
	return enabled, values
}

func (r *redisInflightQuota) release(ctx context.Context, releases []inflightRelease) error {
	if len(releases) == 0 {
		return nil
	}
	// All releases created by one reservation share the same cost.
	cost := releases[0].costUsd
	args := make([]any, 0, 1+len(releases))
	args = append(args, strconv.FormatFloat(cost, 'f', -1, 64))
	for _, entry := range releases {
		args = append(args, scopeToken(entry.stateKey))
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), r.operationTO)
	defer cancel()
	return r.client.Eval(runCtx, redisInflightReleaseScript, []string{r.hashKey, r.expiryKey}, args...).Err()
}

func (r *redisInflightQuota) snapshot(ctx context.Context) ([]redisInflightSnapshotState, error) {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), r.operationTO)
	defer cancel()
	raw, err := r.client.Eval(runCtx, redisInflightSnapshotScript,
		[]string{r.hashKey, r.expiryKey}, time.Now().UnixMilli()).Result()
	if err != nil {
		return nil, err
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, errors.New("inflight quota snapshot 脚本返回格式错误")
	}
	out := make([]redisInflightSnapshotState, 0, len(items)/2)
	for i := 0; i+1 < len(items); i += 2 {
		token, _ := items[i].(string)
		cost, err := strconv.ParseFloat(fmt.Sprint(items[i+1]), 64)
		if err != nil {
			continue
		}
		scopeKey, err := decodeScopeToken(token)
		if err != nil {
			continue
		}
		out = append(out, redisInflightSnapshotState{ScopeKey: scopeKey, ReservedCostUsd: cost})
	}
	return out, nil
}

func (r *redisInflightQuota) clear(ctx context.Context) error {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), r.operationTO)
	defer cancel()
	return r.client.Del(runCtx, r.hashKey, r.expiryKey).Err()
}

func scopeToken(scopeKey string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(scopeKey))
}

func decodeScopeToken(token string) (string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func int64Reply(value interface{}) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case string:
		parsed, _ := strconv.ParseInt(typed, 10, 64)
		return parsed
	default:
		return 0
	}
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
