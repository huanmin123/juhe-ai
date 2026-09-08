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
	RedisURL            string
	Client              redis.Cmdable
	Namespace           string
	Name                string
	Clock               func() int64
	Logger              Logger
	StateOperationTO    time.Duration
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
		client:  client,
		keys:    redisAccountConcurrencyStoreKeys(name, namespace),
		clock:   clock,
		logger:  opts.Logger,
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
local now_ms = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])
redis.call('HINCRBY', key, field, 1)
redis.call('PEXPIRE', key, ttl_ms)
return {1}
`

const redisAccountConcurrencyReleaseScript = `
local key = KEYS[1]
local field = ARGV[1]
local newValue = redis.call('HINCRBY', key, field, -1)
if newValue <= 0 then
  redis.call('DEL', key)
end
return {newValue}
`

// AcquireAccountConcurrency increments the concurrency counter for an account.
func (r *RedisAccountConcurrency) AcquireAccountConcurrency(ctx context.Context, accountID string, lane string, ttlMs int64) error {
	key := r.accountConcurrencyKey(accountID)
	field := lane
	if field == "" {
		field = AccountConcurrencyLaneText
	}
	nowMs := r.clock()
	_, err := r.client.Eval(ctx, redisAccountConcurrencyAcquireScript, []string{key},
		field, strconv.Itoa(int(nowMs)), strconv.Itoa(int(ttlMs))).Result()
	return err
}

// ReleaseAccountConcurrency decrements the concurrency counter for an account.
func (r *RedisAccountConcurrency) ReleaseAccountConcurrency(ctx context.Context, accountID string, lane string) error {
	key := r.accountConcurrencyKey(accountID)
	field := lane
	if field == "" {
		field = AccountConcurrencyLaneText
	}
	_, err := r.client.Eval(ctx, redisAccountConcurrencyReleaseScript, []string{key}, field).Result()
	return err
}
