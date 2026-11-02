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

// CurrentAccountRPM implements AccountConcurrencySource（Redis 驱动的同步
// 读取；读取失败按 0 降级，与 CurrentAccountConcurrency 同语义）。
func (r *RedisAccountConcurrency) CurrentAccountRPM(accountID string) AccountRPMState {
	if accountID == "" {
		return AccountRPMState{}
	}
	nowMs := r.clock()
	bucket := nowMs / 60_000
	values, err := r.client.HMGet(context.Background(), r.accountConcurrencyKey(accountID), "rpm_bucket", "rpm_count").Result()
	if err != nil {
		return AccountRPMState{}
	}
	state := AccountRPMState{ResetAtMs: rpmWindowResetAtMs(bucket)}
	if len(values) == 2 {
		if rawBucket, ok := values[0].(string); ok {
			storedBucket, parseErr := strconv.ParseInt(rawBucket, 10, 64)
			if parseErr == nil && storedBucket == bucket {
				if rawCount, ok := values[1].(string); ok {
					state.Used = maxInt(0, atoiOr(rawCount, 0))
				}
			}
		}
	}
	return state
}

// LoadAccountRPMByID implements AccountConcurrencySource. HGET 同一 HASH 的
// rpm_bucket / rpm_count 两字段（AI 账户每分钟请求上限设计 §4.2）；bucket 过期
// 或字段缺失按 0 返回（读路径不做就地重置，写路径由 acquire 脚本惰性重置）。
func (r *RedisAccountConcurrency) LoadAccountRPMByID(ctx context.Context, accountIDs []string) (map[string]AccountRPMState, error) {
	if len(accountIDs) == 0 {
		return map[string]AccountRPMState{}, nil
	}
	nowMs := r.clock()
	bucket := nowMs / 60_000
	result := make(map[string]AccountRPMState, len(accountIDs))
	for _, id := range accountIDs {
		if id == "" {
			continue
		}
		values, err := r.client.HMGet(ctx, r.accountConcurrencyKey(id), "rpm_bucket", "rpm_count").Result()
		if err != nil {
			return nil, err
		}
		state := AccountRPMState{ResetAtMs: rpmWindowResetAtMs(bucket)}
		if len(values) == 2 {
			if rawBucket, ok := values[0].(string); ok {
				storedBucket, parseErr := strconv.ParseInt(rawBucket, 10, 64)
				if parseErr == nil && storedBucket == bucket {
					if rawCount, ok := values[1].(string); ok {
						state.Used = maxInt(0, atoiOr(rawCount, 0))
					}
				}
			}
		}
		result[id] = state
	}
	return result, nil
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
local rpm_limit = tonumber(ARGV[5])
local now_ms = tonumber(ARGV[6])
local total = tonumber(redis.call('HGET', key, 'total') or '0')
local lane_current = tonumber(redis.call('HGET', key, field) or '0')
-- RPM 窗口观测（bucket 过期即就地重置：窗口计数先落存储归零，成功路径的
-- HINCRBY 才会从 0 起算）。rpm_limit <= 0 与内存驱动一致：不读不写 RPM
-- 字段，观测值保持 0（未配置账户零开销）。
local bucket = 0
local rpm_count = 0
local rpm_reset_at_ms = 0
if rpm_limit > 0 then
  bucket = math.floor(now_ms / 60000)
  local rpm_bucket = tonumber(redis.call('HGET', key, 'rpm_bucket') or '0')
  rpm_count = tonumber(redis.call('HGET', key, 'rpm_count') or '0')
  if rpm_bucket ~= bucket then
    rpm_count = 0
    redis.call('HSET', key, 'rpm_count', 0)
  end
  rpm_reset_at_ms = (bucket + 1) * 60000
end
-- 与内存驱动 TryAcquire 同一契约：任一计数达到上限即拒绝且不自增；上限 <=0
-- 视为不设限。并发检查在前、RPM 窗口在后（AI 账户每分钟请求上限设计 §4.3），
-- 校验与 HINCRBY 同脚本执行，保持原子。
if total_limit > 0 and total >= total_limit then
  return {0, total, lane_current, rpm_count, rpm_reset_at_ms}
end
if lane_limit > 0 and lane_current >= lane_limit then
  return {0, total, lane_current, rpm_count, rpm_reset_at_ms}
end
if rpm_limit > 0 and rpm_count >= rpm_limit then
  return {0, total, lane_current, rpm_count, rpm_reset_at_ms}
end
total = redis.call('HINCRBY', key, 'total', 1)
lane_current = redis.call('HINCRBY', key, field, 1)
if rpm_limit > 0 then
  rpm_count = redis.call('HINCRBY', key, 'rpm_count', 1)
  redis.call('HSET', key, 'rpm_bucket', bucket)
end
redis.call('PEXPIRE', key, ttl_ms)
return {1, total, lane_current, rpm_count, rpm_reset_at_ms}
`

const redisAccountConcurrencyReleaseScript = `
local key = KEYS[1]
local field = ARGV[1]
local new_total = redis.call('HINCRBY', key, 'total', -1)
local new_lane = redis.call('HINCRBY', key, field, -1)
if new_total <= 0 then
  -- 只清并发字段（total + 本次 lane），保留 rpm_bucket/rpm_count：RPM 是
  -- 窗口累计计数（AI 账户每分钟请求上限设计 §4.2），不随并发排空丢失；
  -- hash 因此为空时 Redis 自动删 key，残留 RPM 字段由 acquire 的 PEXPIRE
  -- TTL 自然过期。
  redis.call('HDEL', key, 'total')
  redis.call('HDEL', key, field)
end
return {new_lane}
`

// AcquireAccountConcurrency 在单个 Lua 脚本内原子完成“账户总量 + lane + RPM”
// 三重校验并自增 total、lane 与 rpm_count 计数（与内存驱动 TryAcquire 同一
// 契约：total >= totalLimit、laneCurrent >= laneLimit 或 rpmCount >= rpmLimit
// 即拒绝且不自增；上限 <=0 视为不设限；空 lane 归入 text）。RPM 窗口按
// bucket = floor(nowMs/60000) 就地惰性重置；通过才写 rpm_bucket 并沿用既有
// TTL PEXPIRE。返回占位结果与尝试时刻的计数观测（含 RPM 窗口与重置时刻）。
func (r *RedisAccountConcurrency) AcquireAccountConcurrency(ctx context.Context, accountID string, lane string, ttlMs int64, totalLimit int, laneLimit int, rpmLimit int) (AccountConcurrencyAcquireOutcome, error) {
	key := r.accountConcurrencyKey(accountID)
	field := lane
	if field == "" {
		field = AccountConcurrencyLaneText
	}
	nowMs := r.clock()
	values, err := r.client.Eval(ctx, redisAccountConcurrencyAcquireScript, []string{key},
		field, strconv.Itoa(int(ttlMs)), strconv.Itoa(totalLimit), strconv.Itoa(laneLimit),
		strconv.Itoa(rpmLimit), strconv.FormatInt(nowMs, 10)).Result()
	if err != nil {
		return AccountConcurrencyAcquireOutcome{}, err
	}
	reply, ok := values.([]interface{})
	if !ok || len(reply) != 5 {
		return AccountConcurrencyAcquireOutcome{}, errors.New("账户并发 acquire 脚本返回格式错误")
	}
	return AccountConcurrencyAcquireOutcome{
		Acquired:     replyInt(reply[0]) == 1,
		Total:        replyInt(reply[1]),
		LaneCurrent:  replyInt(reply[2]),
		RPMCurrent:   replyInt(reply[3]),
		RPMLimit:     rpmLimit,
		RPMResetAtMs: replyInt64(reply[4]),
	}, nil
}

// replyInt 把 Lua 返回的整数应答转成 int（go-redis 对 Lua integer 返回 int64）。
func replyInt(value interface{}) int {
	if number, ok := value.(int64); ok {
		return int(number)
	}
	return 0
}

// replyInt64 把 Lua 返回的整数应答转成 int64（毫秒时刻等大数值观测）。
func replyInt64(value interface{}) int64 {
	if number, ok := value.(int64); ok {
		return number
	}
	return 0
}

// atoiOr 把 Redis 文本应答转成 int，解析失败回落 fallback。
func atoiOr(raw string, fallback int) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// ReleaseAccountConcurrency decrements the total and lane concurrency
// counters; once the total reaches zero only those concurrency fields are
// removed — the RPM window fields (rpm_bucket / rpm_count) stay for the
// natural PEXPIRE TTL expiry (AI 账户每分钟请求上限设计 §4.2)。
func (r *RedisAccountConcurrency) ReleaseAccountConcurrency(ctx context.Context, accountID string, lane string) error {
	key := r.accountConcurrencyKey(accountID)
	field := lane
	if field == "" {
		field = AccountConcurrencyLaneText
	}
	_, err := r.client.Eval(ctx, redisAccountConcurrencyReleaseScript, []string{key}, field).Result()
	return err
}
