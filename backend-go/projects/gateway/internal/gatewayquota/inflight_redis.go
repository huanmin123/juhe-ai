package gatewayquota

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// redisInflightQuota stores in-flight quota reservations as a per-reservation
// slot set plus a scope aggregate hash.
//
// BUG-0362 同构根治：旧实现用「scope token -> 累计成本」一个 HASH 字段 +
// 一条 ZSET 分数表示所有预留；同 scope 的任意新预留都会把该 scope 的过期
// 时间重写为 now+30m，于是单个泄漏预留会被同 scope 的其他流量永久续命，
// 且 release 按聚合成本相减（重试/迟到释放会多减）。现模型：
//   - 每次 reserve 生成唯一 reservationToken，每个 scope 一条独立 ZSET 成员
//     `<scopeToken>|<reservationToken>`（score=本预留自己的到期时间）；
//   - entries HASH 记录成员 -> 本次预留成本；scope 聚合 HASH 只用于限额判定；
//   - release 按成员存在性判定后删除并精确扣减（幂等，重复/迟到释放 no-op）；
//   - 过期清理按成员逐条扣减，不再受同 scope 其他预留影响。
//
// Legacy 兼容：旧数据在聚合 HASH 里是裸 scopeToken、ZSET 成员也不含 `|`；
// 过期清理与 snapshot 会把旧成员按「整 scope 归零」处理，新预留走新格式。
type redisInflightQuota struct {
	client      redis.UniversalClient
	hashKey     string
	entriesKey  string
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
local entries = KEYS[2]
local zset = KEYS[3]
local cost = ARGV[1]
local cost_num = tonumber(cost)
local ttl = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local reservation = ARGV[4]

-- 清理过期预留：每个成员独立扣减，旧格式（裸 scopeToken）整 scope 归零。
local expired = redis.call('ZRANGEBYSCORE', zset, '-inf', now, 'LIMIT', 0, 200)
for _, member in ipairs(expired) do
  local sep = string.find(member, '|', 1, true)
  if sep then
    local scope = string.sub(member, 1, sep - 1)
    local entry_cost = redis.call('HGET', entries, member)
    if entry_cost then
      redis.call('HDEL', entries, member)
      local next_cost = redis.call('HINCRBYFLOAT', hash, scope, -tonumber(entry_cost))
      if tonumber(next_cost) <= 0 then
        redis.call('HDEL', hash, scope)
      end
    end
  else
    redis.call('HDEL', hash, member)
  end
  redis.call('ZREM', zset, member)
end

-- 限额判定仍读 scope 聚合（旧/新预留都计入）。
local count = (#ARGV - 4) / 16
local idx = 5
for i = 1, count do
  local scope = ARGV[idx]
  local reserved = tonumber(redis.call('HGET', hash, scope) or '0') or 0
  local projected = reserved + cost_num
  for w = 1, 5 do
    local current = tonumber(ARGV[idx + w])
    local enabled = tonumber(ARGV[idx + 5 + w])
    local limit = tonumber(ARGV[idx + 10 + w])
    if enabled == 1 and current + projected > limit then
      return {0, i}
    end
  end
  idx = idx + 16
end

-- 占位：每个 scope 一条独立成员 + 聚合成本。
idx = 5
for i = 1, count do
  local scope = ARGV[idx]
  local member = scope .. '|' .. reservation
  redis.call('HINCRBYFLOAT', hash, scope, cost_num)
  redis.call('HSET', entries, member, cost)
  redis.call('ZADD', zset, now + ttl, member)
  idx = idx + 16
end
return {1, 0}
`

const redisInflightReleaseScript = `
local hash = KEYS[1]
local entries = KEYS[2]
local zset = KEYS[3]
local cost = ARGV[1]
local cost_num = tonumber(cost)
local reservation = ARGV[2]
-- 成员存在才扣减（幂等）：重复释放、过期清理后的迟到释放均为 no-op。
for i = 3, #ARGV do
  local member = ARGV[i] .. '|' .. reservation
  if redis.call('HGET', entries, member) then
    redis.call('HDEL', entries, member)
    redis.call('ZREM', zset, member)
    local next_cost = redis.call('HINCRBYFLOAT', hash, ARGV[i], -cost_num)
    if tonumber(next_cost) <= 0 then
      redis.call('HDEL', hash, ARGV[i])
    end
  end
end
return 1
`

const redisInflightSnapshotScript = `
local hash = KEYS[1]
local entries = KEYS[2]
local zset = KEYS[3]
local now = tonumber(ARGV[1])
local expired = redis.call('ZRANGEBYSCORE', zset, '-inf', now, 'LIMIT', 0, 200)
for _, member in ipairs(expired) do
  local sep = string.find(member, '|', 1, true)
  if sep then
    local scope = string.sub(member, 1, sep - 1)
    local entry_cost = redis.call('HGET', entries, member)
    if entry_cost then
      redis.call('HDEL', entries, member)
      local next_cost = redis.call('HINCRBYFLOAT', hash, scope, -tonumber(entry_cost))
      if tonumber(next_cost) <= 0 then
        redis.call('HDEL', hash, scope)
      end
    end
  else
    redis.call('HDEL', hash, member)
  end
  redis.call('ZREM', zset, member)
end
return redis.call('HGETALL', hash)
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
		entriesKey:  "juhe-ai:{" + tag + "}:inflight-entries",
		expiryKey:   "juhe-ai:{" + tag + "}:inflight-expiry",
		operationTO: redisInflightOperationTimeout,
	}, nil
}

// newInflightReservationToken 生成本次预留的唯一标识（release 凭它精确定位
// 自己的成员）。
func newInflightReservationToken() string {
	var buffer [8]byte
	_, _ = rand.Read(buffer[:])
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + hex.EncodeToString(buffer[:])
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
	reservationToken := newInflightReservationToken()
	args := make([]any, 0, 4+len(scopes)*16)
	args = append(args, strconv.FormatFloat(cost, 'f', -1, 64), reservationLeakTTL.Milliseconds(), nowMs, reservationToken)
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
	raw, err := r.client.Eval(runCtx, redisInflightReserveScript,
		[]string{r.hashKey, r.entriesKey, r.expiryKey}, args...).Result()
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
		releases = append(releases, inflightRelease{
			stateKey:         scope.ScopeKey,
			costUsd:          cost,
			reservationToken: reservationToken,
		})
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
	// All releases created by one reservation share the same cost/token.
	cost := releases[0].costUsd
	reservationToken := releases[0].reservationToken
	args := make([]any, 0, 2+len(releases))
	args = append(args, strconv.FormatFloat(cost, 'f', -1, 64), reservationToken)
	for _, entry := range releases {
		args = append(args, scopeToken(entry.stateKey))
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), r.operationTO)
	defer cancel()
	return r.client.Eval(runCtx, redisInflightReleaseScript,
		[]string{r.hashKey, r.entriesKey, r.expiryKey}, args...).Err()
}

func (r *redisInflightQuota) snapshot(ctx context.Context) ([]redisInflightSnapshotState, error) {
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), r.operationTO)
	defer cancel()
	raw, err := r.client.Eval(runCtx, redisInflightSnapshotScript,
		[]string{r.hashKey, r.entriesKey, r.expiryKey}, time.Now().UnixMilli()).Result()
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
	return r.client.Del(runCtx, r.hashKey, r.entriesKey, r.expiryKey).Err()
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
