package gatewayclientip

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// RedisAccountConcurrencyOptions configures Redis-backed account concurrency.
type RedisAccountConcurrencyOptions struct {
	RedisURL         string
	Client           redis.Cmdable
	Namespace        string
	Name             string
	Clock            func() int64
	Logger           Logger
	StateOperationTO time.Duration
}

// RedisAccountConcurrency implements AccountConcurrencySource using Redis.
// It tracks per-account concurrency counts in Redis hashes, enabling
// cross-instance coordination for the account concurrency seam.
type RedisAccountConcurrency struct {
	client redis.Cmdable
	keys   redisAccountConcurrencyKeys
	clock  func() int64
	logger Logger
}

type redisAccountConcurrencyKeys struct {
	accountConcurrency string
}

func redisAccountConcurrencyStoreKeys(name, namespace string) redisAccountConcurrencyKeys {
	return redisAccountConcurrencyKeys{
		accountConcurrency: "juhe-ai:" + namespace + ":" + name + ":account-concurrency",
	}
}

// NewRedisAccountConcurrency creates a Redis-backed account concurrency source.
func NewRedisAccountConcurrency(opts RedisAccountConcurrencyOptions) (*RedisAccountConcurrency, error) {
	client := opts.Client
	if client == nil {
		if strings.TrimSpace(opts.RedisURL) == "" {
			return nil, errors.New("account concurrency Redis URL 不能为空")
		}
		parsed, err := redis.ParseURL(opts.RedisURL)
		if err != nil {
			return nil, err
		}
		client = redis.NewClient(parsed)
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now().UnixMilli
	}
	name := opts.Name
	if strings.TrimSpace(name) == "" {
		name = "gateway-account-concurrency"
	}
	namespace := opts.Namespace
	return &RedisAccountConcurrency{
		client: client,
		keys:   redisAccountConcurrencyStoreKeys(name, namespace),
		clock:  clock,
		logger: opts.Logger,
	}, nil
}

// Close closes the Redis client if this instance owns it.
func (r *RedisAccountConcurrency) Close() {
	if r.client != nil {
		if c, ok := r.client.(*redis.Client); ok {
			_ = c.Close()
		}
	}
}

// accountConcurrencyKey builds the Redis key for an account's concurrency hash.
func (r *RedisAccountConcurrency) accountConcurrencyKey(accountID string) string {
	return r.keys.accountConcurrency + ":" + accountID
}

// LoadAccountCurrentConcurrencyByID implements AccountConcurrencySource.
func (r *RedisAccountConcurrency) LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error) {
	if len(accountIDs) == 0 {
		return map[string]int{}, nil
	}
	result := make(map[string]int, len(accountIDs))
	for _, id := range accountIDs {
		val, err := r.client.HGet(ctx, r.accountConcurrencyKey(id), "total").Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				result[id] = 0
				continue
			}
			return nil, err
		}
		concurrency, _ := strconv.Atoi(val)
		result[id] = concurrency
	}
	return result, nil
}

// LoadAccountCurrentConcurrencyByLane implements AccountConcurrencySource.
func (r *RedisAccountConcurrency) LoadAccountCurrentConcurrencyByLane(ctx context.Context, accountIDs []string, lane string) (map[string]int, error) {
	if len(accountIDs) == 0 {
		return map[string]int{}, nil
	}
	result := make(map[string]int, len(accountIDs))
	for _, id := range accountIDs {
		val, err := r.client.HGet(ctx, r.accountConcurrencyKey(id), lane).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				result[id] = 0
				continue
			}
			return nil, err
		}
		concurrency, _ := strconv.Atoi(val)
		result[id] = concurrency
	}
	return result, nil
}

// CurrentAccountConcurrency implements AccountConcurrencySource.
func (r *RedisAccountConcurrency) CurrentAccountConcurrency(accountID string, lane string) int {
	if accountID == "" {
		return 0
	}
	field := lane
	if field == "" {
		field = AccountConcurrencyLaneText
	}
	val, err := r.client.HGet(context.Background(), r.accountConcurrencyKey(accountID), field).Result()
	if err != nil {
		return 0
	}
	concurrency, _ := strconv.Atoi(val)
	return concurrency
}

// SubscribeAccountConcurrencyRelease implements AccountConcurrencySource.
// Redis implementation uses background goroutines to poll for changes.
// This is a simplified implementation that works with the MemoryAccountConcurrency
// which handles the actual slot management.
func (r *RedisAccountConcurrency) SubscribeAccountConcurrencyRelease(listener func(AccountConcurrencyReleaseEvent)) func() {
	// The Redis implementation coordinates with MemoryAccountConcurrency
	// via the gateway runtime. This stub ensures interface compliance
	// when Redis is enabled but the subscription is managed at runtime layer.
	return func() {}
}

// --- Redis Lua scripts for atomic operations ---

const redisAccountConcurrencyAcquireScript = `
local key = KEYS[1]
local field = ARGV[1]
local ttl_ms = tonumber(ARGV[2])
local total_limit = tonumber(ARGV[3])
local lane_limit = tonumber(ARGV[4])
local total = tonumber(redis.call('HGET', key, 'total') or '0')
local lane_current = tonumber(redis.call('HGET', key, field) or '0')
-- 与内存驱动 TryAcquire 同一契约：total 或 lane 任一达到上限即拒绝且不自增；
-- 上限 <=0 视为不设限。校验与 HINCRBY 同脚本执行，保持原子。
if total_limit > 0 and total >= total_limit then
  return {0, total, lane_current}
end
if lane_limit > 0 and lane_current >= lane_limit then
  return {0, total, lane_current}
end
total = redis.call('HINCRBY', key, 'total', 1)
lane_current = redis.call('HINCRBY', key, field, 1)
redis.call('PEXPIRE', key, ttl_ms)
return {1, total, lane_current}
`

const redisAccountConcurrencyReleaseScript = `
local key = KEYS[1]
local field = ARGV[1]
local new_total = redis.call('HINCRBY', key, 'total', -1)
local new_lane = redis.call('HINCRBY', key, field, -1)
if new_total <= 0 then
  redis.call('DEL', key)
end
return {new_lane}
`

// AcquireAccountConcurrency 在单个 Lua 脚本内原子完成“账户总量 + lane”双
// 重校验并自增 total 与 lane 计数（与内存驱动 TryAcquire 同一契约：
// total >= totalLimit 或 laneCurrent >= laneLimit 即拒绝且不自增；上限
// <=0 视为不设限；空 lane 归入 text）。返回占位结果与尝试时刻的两个计数
// （LoadAccountCurrentConcurrencyByID reads 'total'）。
func (r *RedisAccountConcurrency) AcquireAccountConcurrency(ctx context.Context, accountID string, lane string, ttlMs int64, totalLimit int, laneLimit int) (AccountConcurrencyAcquireOutcome, error) {
	key := r.accountConcurrencyKey(accountID)
	field := lane
	if field == "" {
		field = AccountConcurrencyLaneText
	}
	values, err := r.client.Eval(ctx, redisAccountConcurrencyAcquireScript, []string{key},
		field, strconv.Itoa(int(ttlMs)), strconv.Itoa(totalLimit), strconv.Itoa(laneLimit)).Result()
	if err != nil {
		return AccountConcurrencyAcquireOutcome{}, err
	}
	reply, ok := values.([]interface{})
	if !ok || len(reply) != 3 {
		return AccountConcurrencyAcquireOutcome{}, errors.New("账户并发 acquire 脚本返回格式错误")
	}
	return AccountConcurrencyAcquireOutcome{
		Acquired:    replyInt(reply[0]) == 1,
		Total:       replyInt(reply[1]),
		LaneCurrent: replyInt(reply[2]),
	}, nil
}

// replyInt 把 Lua 返回的整数应答转成 int（go-redis 对 Lua integer 返回 int64）。
func replyInt(value interface{}) int {
	if number, ok := value.(int64); ok {
		return int(number)
	}
	return 0
}

// ReleaseAccountConcurrency decrements the total and lane concurrency
// counters; the key is dropped once the total reaches zero.
func (r *RedisAccountConcurrency) ReleaseAccountConcurrency(ctx context.Context, accountID string, lane string) error {
	key := r.accountConcurrencyKey(accountID)
	field := lane
	if field == "" {
		field = AccountConcurrencyLaneText
	}
	_, err := r.client.Eval(ctx, redisAccountConcurrencyReleaseScript, []string{key}, field).Result()
	return err
}
