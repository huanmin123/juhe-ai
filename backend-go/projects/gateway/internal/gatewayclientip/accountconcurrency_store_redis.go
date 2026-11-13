package gatewayclientip

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
	redis "github.com/redis/go-redis/v9"
)

// RedisAccountConcurrencyOptions configures Redis-backed account concurrency.
type RedisAccountConcurrencyOptions struct {
	RedisURL  string
	Client    redis.UniversalClient
	Namespace string
	Name      string
	// ProjectionNamespace 是账户列表投影 dirty/generation 队列的命名空间
	// （DomainCircuit 域，与 jobs account-list-availability-projection 读取面
	// 一致）；空值关闭投影通知写入（单测/旧组合零开销）。
	ProjectionNamespace string
	Clock               func() int64
	Logger              Logger
	StateOperationTO    time.Duration
}

// RedisAccountConcurrency implements AccountConcurrencySource using Redis.
//
// 并发槽是「槽集合」而不是可漂移的计数器（BUG-0361 根治）：
//   - 每个占位是 total ZSET 与 lane ZSET 中的一个成员，member = "<lane>:<token>"，
//     score = 该槽的绝对到期时间（租约）；
//   - 当前并发 = 清理过期成员后的 ZCARD，天然等于真实槽数；释放是 ZREM
//     （幂等），不存在「先 +1 后 -1」的计数器漂移；
//   - 槽租约由调用方传入（生产 30m，覆盖墙钟上限），过期成员在 acquire /
//     读面按 now 惰性清理；进程崩溃、release RPC 超时、acquire EVAL
//     at-most-once 等所有异常窗口都收敛为「租约到期自动消失」；
//   - 键级 TTL 只作空闲账户的整体回收（槽租约 + 5m 宽限），不再作为
//     6h 泄漏兜底，也不会被持续流量无限续期。
//
// RPM 仍在旧 HASH 键（rpm_bucket/rpm_count）：RPM 是窗口累计计数，语义与
// 槽集合无关；旧 HASH 的 total/text/image/realtime 并发字段在 acquire 时清理。
type RedisAccountConcurrency struct {
	client redis.UniversalClient
	keys   redisAccountConcurrencyKeys
	clock  func() int64
	logger Logger
}

type redisAccountConcurrencyKeys struct {
	accountConcurrency   string
	projectionGeneration string
	projectionDirty      string
}

func redisAccountConcurrencyStoreKeys(name, namespace, projectionNamespace string) redisAccountConcurrencyKeys {
	keys := redisAccountConcurrencyKeys{
		accountConcurrency: rediscfg.NamespacedKey("juhe-ai:"+name+":account-concurrency", namespace),
	}
	if strings.TrimSpace(projectionNamespace) != "" {
		keys.projectionGeneration = rediscfg.NamespacedKey("juhe-ai:account-concurrency-projection-generation", projectionNamespace)
		keys.projectionDirty = rediscfg.NamespacedKey("juhe-ai:account-concurrency-projection-dirty", projectionNamespace)
	}
	return keys
}

// projectionEnabled 报告投影通知键是否已配置。
func (k redisAccountConcurrencyKeys) projectionEnabled() bool {
	return k.projectionDirty != "" && k.projectionGeneration != ""
}

// NewRedisAccountConcurrency creates a Redis-backed account concurrency source.
func NewRedisAccountConcurrency(opts RedisAccountConcurrencyOptions) (*RedisAccountConcurrency, error) {
	client := opts.Client
	var err error
	if client == nil {
		if strings.TrimSpace(opts.RedisURL) == "" {
			return nil, errors.New("account concurrency Redis URL 不能为空")
		}
		client, err = rediscfg.NewUniversalClient(rediscfg.Config{URL: opts.RedisURL})
		if err != nil {
			return nil, err
		}
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
		keys:   redisAccountConcurrencyStoreKeys(name, namespace, opts.ProjectionNamespace),
		clock:  clock,
		logger: opts.Logger,
	}, nil
}

// Close closes the Redis client. The production chain constructs the tracker
// with the shared runtime-state client and therefore never calls Close; the
// client is owned by chainRuntimeServices.
func (r *RedisAccountConcurrency) Close() {
	if r.client != nil {
		_ = r.client.Close()
	}
}

// accountConcurrencyKey 是 RPM HASH 键（旧键名保持兼容：jobs/其它读面只
// 读 rpm_bucket/rpm_count）；并发槽事实源是 slotsKey/laneSlotsKey。
func (r *RedisAccountConcurrency) accountConcurrencyKey(accountID string) string {
	return r.keys.accountConcurrency + ":" + accountID
}

// slotsKey 是账户并发槽总集合（member="<lane>:<token>"，score=到期毫秒）。
func (r *RedisAccountConcurrency) slotsKey(accountID string) string {
	return r.accountConcurrencyKey(accountID) + ":slots"
}

// laneSlotsKey 是账户某 lane 的并发槽集合（同 member/score，便于 lane 计数）。
func (r *RedisAccountConcurrency) laneSlotsKey(accountID, lane string) string {
	return r.slotsKey(accountID) + ":" + lane
}

func normalizeAccountConcurrencyLane(lane string) string {
	if strings.TrimSpace(lane) == "" {
		return AccountConcurrencyLaneText
	}
	return lane
}

func accountConcurrencySlotMember(lane, token string) string {
	return normalizeAccountConcurrencyLane(lane) + ":" + token
}

// newAccountConcurrencySlotToken 生成进程内全局唯一的槽标识（release/refresh
// 凭它定位 ZSET 成员）。token 不含冒号，避免与 member 的 "<lane>:" 前缀歧义。
func newAccountConcurrencySlotToken() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + randomHex8()
}

// LoadAccountCurrentConcurrencyByID implements AccountConcurrencySource.
func (r *RedisAccountConcurrency) LoadAccountCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error) {
	if len(accountIDs) == 0 {
		return map[string]int{}, nil
	}
	result := make(map[string]int, len(accountIDs))
	for _, id := range accountIDs {
		if strings.TrimSpace(id) == "" {
			continue
		}
		concurrency, err := r.countSlots(ctx, id, "")
		if err != nil {
			return nil, err
		}
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
		if strings.TrimSpace(id) == "" {
			continue
		}
		concurrency, err := r.countSlots(ctx, id, lane)
		if err != nil {
			return nil, err
		}
		result[id] = concurrency
	}
	return result, nil
}

// countSlots 清理过期槽后返回集合大小（lane 为空 = 账户总量）。
func (r *RedisAccountConcurrency) countSlots(ctx context.Context, accountID string, lane string) (int, error) {
	key := r.slotsKey(accountID)
	if strings.TrimSpace(lane) != "" {
		key = r.laneSlotsKey(accountID, lane)
	}
	raw, err := r.client.Eval(ctx, redisAccountConcurrencyCountScript, []string{key}, r.clock()).Result()
	if err != nil {
		return 0, err
	}
	return replyInt(raw), nil
}

// CurrentAccountConcurrency implements AccountConcurrencySource.
func (r *RedisAccountConcurrency) CurrentAccountConcurrency(accountID string, lane string) int {
	if strings.TrimSpace(accountID) == "" {
		return 0
	}
	concurrency, err := r.countSlots(context.Background(), accountID, lane)
	if err != nil {
		r.warnReadFailure("redis_account_concurrency_read_failed", accountID, err)
		return 0
	}
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
		r.warnReadFailure("redis_account_rpm_read_failed", accountID, err)
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

// warnReadFailure 记录同步读面 Redis 失败。同步读面为展示/抑制投影，失败按
// 0 降级不会放行请求；日志用于发现基础设施异常。
func (r *RedisAccountConcurrency) warnReadFailure(event, accountID string, err error) {
	if r == nil || r.logger == nil || err == nil {
		return
	}
	r.logger.Warn(event, map[string]any{
		"accountId": accountID,
		"err":       err.Error(),
	}, "Redis 账户并发读面失败，按 0 降级")
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
local slots_key = KEYS[1]
local lane_key = KEYS[2]
local rpm_key = KEYS[3]
local lane = ARGV[1]
local token = ARGV[2]
local ttl_ms = tonumber(ARGV[3])
local total_limit = tonumber(ARGV[4])
local lane_limit = tonumber(ARGV[5])
local rpm_limit = tonumber(ARGV[6])
local now_ms = tonumber(ARGV[7])
-- ARGV[8]=投影通知开关（'1' 启用）；ARGV[9]=account_id。启用时
-- KEYS[4]=generation HASH、KEYS[5]=dirty ZSET（DomainCircuit 域，jobs 投影读面）。
local projection = ARGV[8]
local account_id = ARGV[9]

-- 槽集合：先清理过期租约，再以 ZCARD 作为当前并发（计数即槽集合大小）。
redis.call('ZREMRANGEBYSCORE', slots_key, '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', lane_key, '-inf', now_ms)
local total = redis.call('ZCARD', slots_key)
local lane_current = redis.call('ZCARD', lane_key)

-- 迁移清理：旧实现的并发计数字段不再是事实源（只保留 RPM 字段）。
redis.call('HDEL', rpm_key, 'total', 'text', 'image', 'realtime')

-- RPM 窗口观测（bucket 过期即就地重置：窗口计数先落存储归零，成功路径的
-- HINCRBY 才会从 0 起算）。rpm_limit <= 0 与内存驱动一致：不读不写 RPM
-- 字段，观测值保持 0（未配置账户零开销）。
local bucket = 0
local rpm_count = 0
local rpm_reset_at_ms = 0
if rpm_limit > 0 then
  bucket = math.floor(now_ms / 60000)
  local rpm_bucket = tonumber(redis.call('HGET', rpm_key, 'rpm_bucket') or '0')
  rpm_count = tonumber(redis.call('HGET', rpm_key, 'rpm_count') or '0')
  if rpm_bucket ~= bucket then
    rpm_count = 0
    redis.call('HSET', rpm_key, 'rpm_count', 0)
  end
  rpm_reset_at_ms = (bucket + 1) * 60000
  -- 拒绝路径也保证键有 TTL：窗口重置的 HSET 可能刚创建 key。
  redis.call('PEXPIRE', rpm_key, 120000)
end
-- 与内存驱动 TryAcquire 同一契约：任一计数达到上限即拒绝且不自增；上限 <=0
-- 视为不设限。并发检查在前、RPM 窗口在后（AI 账户每分钟请求上限设计 §4.3）。
if total_limit > 0 and total >= total_limit then
  return {0, total, lane_current, rpm_count, rpm_reset_at_ms}
end
if lane_limit > 0 and lane_current >= lane_limit then
  return {0, total, lane_current, rpm_count, rpm_reset_at_ms}
end
if rpm_limit > 0 and rpm_count >= rpm_limit then
  return {0, total, lane_current, rpm_count, rpm_reset_at_ms}
end

local member = lane .. ':' .. token
local expiry = now_ms + ttl_ms
redis.call('ZADD', slots_key, expiry, member)
redis.call('ZADD', lane_key, expiry, member)
-- 键级 TTL 只作空闲账户整体回收：槽租约 + 5m 宽限。
local key_ttl = ttl_ms + 300000
redis.call('PEXPIRE', slots_key, key_ttl)
redis.call('PEXPIRE', lane_key, key_ttl)
if rpm_limit > 0 then
  rpm_count = redis.call('HINCRBY', rpm_key, 'rpm_count', 1)
  redis.call('HSET', rpm_key, 'rpm_bucket', bucket)
  local remaining = redis.call('PTTL', rpm_key)
  if remaining < 120000 then
    redis.call('PEXPIRE', rpm_key, 120000)
  end
end
if projection == '1' then
  redis.call('HINCRBY', KEYS[4], account_id, 1)
  redis.call('ZADD', KEYS[5], now_ms, account_id)
end
return {1, total + 1, lane_current + 1, rpm_count, rpm_reset_at_ms}
`

const redisAccountConcurrencyReleaseScript = `
local slots_key = KEYS[1]
local lane_key = KEYS[2]
local member = ARGV[1]
local projection = ARGV[2]
local account_id = ARGV[3]
local now_ms = tonumber(ARGV[4])
-- ZREM 幂等：重复释放、release RPC 超时后重试都不会造成负计数。
redis.call('ZREM', slots_key, member)
redis.call('ZREM', lane_key, member)
if projection == '1' then
  redis.call('HINCRBY', KEYS[3], account_id, 1)
  redis.call('ZADD', KEYS[4], now_ms, account_id)
end
return 1
`

const redisAccountConcurrencyRefreshScript = `
local slots_key = KEYS[1]
local lane_key = KEYS[2]
local member = ARGV[1]
local expiry = tonumber(ARGV[2])
local key_ttl = tonumber(ARGV[3])
local now_ms = tonumber(ARGV[4])
-- 槽已被释放、或租约已过期但尚未被清理时不再复活（返回 0）。
local existing = redis.call('ZSCORE', slots_key, member)
if not existing or tonumber(existing) <= now_ms then
  return 0
end
redis.call('ZADD', slots_key, expiry, member)
redis.call('ZADD', lane_key, expiry, member)
redis.call('PEXPIRE', slots_key, key_ttl)
redis.call('PEXPIRE', lane_key, key_ttl)
return 1
`

const redisAccountConcurrencyCountScript = `
local key = KEYS[1]
local now_ms = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', key, '-inf', now_ms)
return redis.call('ZCARD', key)
`

// redisAccountConcurrencyConsumeRPMScript 是同一并发槽内后续真实上游尝试的
// RPM 消费脚本（T161）：只动 rpm_bucket/rpm_count，不改并发槽集合；窗口按
// bucket 就地惰性重置，达到上限拒绝且不自增，通过则 HINCRBY 并续 TTL。
const redisAccountConcurrencyConsumeRPMScript = `
local key = KEYS[1]
local now_ms = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])
local bucket = math.floor(now_ms / 60000)
local reset_at_ms = (bucket + 1) * 60000
local stored_bucket = tonumber(redis.call('HGET', key, 'rpm_bucket') or '0')
local count = tonumber(redis.call('HGET', key, 'rpm_count') or '0')
if stored_bucket ~= bucket then
  redis.call('HSET', key, 'rpm_count', 0)
  count = 0
end
if count >= limit then
  return {0, count, reset_at_ms}
end
count = redis.call('HINCRBY', key, 'rpm_count', 1)
redis.call('HSET', key, 'rpm_bucket', bucket)
local remaining = redis.call('PTTL', key)
if remaining < ttl_ms then
  redis.call('PEXPIRE', key, ttl_ms)
end
return {1, count, reset_at_ms}
`

// redisAccountConcurrencyRPMTTLMs 是 RPM 窗口字段的最小兜底 TTL（2 个窗口）；
// 只延长不缩短。
const redisAccountConcurrencyRPMTTLMs = 120_000

// AcquireAccountConcurrency 在单个 Lua 脚本内原子完成「过期清理 + 账户总量 +
// lane + RPM」校验并按槽集合占位（与内存驱动 TryAcquire 同一契约：total >=
// totalLimit、laneCurrent >= laneLimit 或 rpmCount >= rpmLimit 即拒绝且不占位；
// 上限 <=0 视为不设限；空 lane 归入 text）。成功时返回本次槽 token（release/
// refresh 凭它定位 ZSET 成员）与尝试时刻的计数观测。
func (r *RedisAccountConcurrency) AcquireAccountConcurrency(ctx context.Context, accountID string, lane string, ttlMs int64, totalLimit int, laneLimit int, rpmLimit int) (AccountConcurrencyAcquireOutcome, error) {
	normalizedLane := normalizeAccountConcurrencyLane(lane)
	token := newAccountConcurrencySlotToken()
	nowMs := r.clock()
	projection := "0"
	projectionKeys := []string{
		r.slotsKey(accountID), r.laneSlotsKey(accountID, normalizedLane), r.accountConcurrencyKey(accountID),
		r.accountConcurrencyKey(accountID), r.accountConcurrencyKey(accountID),
	}
	if r.keys.projectionEnabled() {
		projection = "1"
		projectionKeys[3] = r.keys.projectionGeneration
		projectionKeys[4] = r.keys.projectionDirty
	}
	values, err := r.client.Eval(ctx, redisAccountConcurrencyAcquireScript, projectionKeys,
		normalizedLane, token, strconv.Itoa(int(ttlMs)), strconv.Itoa(totalLimit), strconv.Itoa(laneLimit),
		strconv.Itoa(rpmLimit), strconv.FormatInt(nowMs, 10), projection, accountID).Result()
	if err != nil {
		return AccountConcurrencyAcquireOutcome{}, err
	}
	reply, ok := values.([]interface{})
	if !ok || len(reply) != 5 {
		return AccountConcurrencyAcquireOutcome{}, errors.New("账户并发 acquire 脚本返回格式错误")
	}
	outcome := AccountConcurrencyAcquireOutcome{
		Acquired:     replyInt(reply[0]) == 1,
		Total:        replyInt(reply[1]),
		LaneCurrent:  replyInt(reply[2]),
		RPMCurrent:   replyInt(reply[3]),
		RPMLimit:     rpmLimit,
		RPMResetAtMs: replyInt64(reply[4]),
	}
	if outcome.Acquired {
		outcome.Token = token
	}
	return outcome, nil
}

// RefreshAccountConcurrency 续租一个已存在的槽（realtime 长连接心跳调用）：
// 只更新 ZSET 成员 score 与键 TTL；槽已被释放/过期清理时返回 false 且不复活。
func (r *RedisAccountConcurrency) RefreshAccountConcurrency(ctx context.Context, accountID string, lane string, token string, ttlMs int64) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, errors.New("账户并发 refresh 缺少 slot token")
	}
	normalizedLane := normalizeAccountConcurrencyLane(lane)
	member := accountConcurrencySlotMember(normalizedLane, token)
	expiry := r.clock() + ttlMs
	keyTTL := ttlMs + 300000
	raw, err := r.client.Eval(ctx, redisAccountConcurrencyRefreshScript,
		[]string{r.slotsKey(accountID), r.laneSlotsKey(accountID, normalizedLane)},
		member, strconv.FormatInt(expiry, 10), strconv.FormatInt(keyTTL, 10),
		strconv.FormatInt(r.clock(), 10)).Result()
	if err != nil {
		return false, err
	}
	return replyInt(raw) == 1, nil
}

// ConsumeRPM 在 Redis HASH 内原子消费一次 RPM（T161）：与 acquire 共用
// rpm_bucket/rpm_count 字段；首次占位由 AcquireAccountConcurrency 计数，本方法
// 只服务同一并发槽内的后续真实上游尝试。达到上限返回 allowed=false 与当前
// 窗口观测，计数不变；通过则 HINCRBY，并在现有 TTL 短于 RPM 兜底时延长。
func (r *RedisAccountConcurrency) ConsumeRPM(ctx context.Context, accountID string, rpmLimit int) (bool, int, int64, error) {
	if rpmLimit <= 0 {
		return true, 0, 0, nil
	}
	nowMs := r.clock()
	values, err := r.client.Eval(ctx, redisAccountConcurrencyConsumeRPMScript,
		[]string{r.accountConcurrencyKey(accountID)},
		strconv.FormatInt(nowMs, 10), strconv.Itoa(rpmLimit),
		strconv.Itoa(redisAccountConcurrencyRPMTTLMs)).Result()
	if err != nil {
		return false, 0, 0, err
	}
	reply, ok := values.([]interface{})
	if !ok || len(reply) != 3 {
		return false, 0, 0, errors.New("账户 RPM 消费脚本返回格式错误")
	}
	return replyInt(reply[0]) == 1, replyInt(reply[1]), replyInt64(reply[2]), nil
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

// ReleaseAccountConcurrency 从槽集合移除本次占位（ZREM 幂等）：重复调用、
// release RPC 超时后重试都不会造成负计数；槽不存在时静默成功。RPM 窗口字段
// （rpm_bucket / rpm_count）保留，由键 TTL 自然过期（设计 §4.2）。
func (r *RedisAccountConcurrency) ReleaseAccountConcurrency(ctx context.Context, accountID string, lane string, token string) error {
	if strings.TrimSpace(token) == "" {
		return errors.New("账户并发 release 缺少 slot token")
	}
	normalizedLane := normalizeAccountConcurrencyLane(lane)
	member := accountConcurrencySlotMember(normalizedLane, token)
	projection := "0"
	projectionKeys := []string{
		r.slotsKey(accountID), r.laneSlotsKey(accountID, normalizedLane),
		r.slotsKey(accountID), r.slotsKey(accountID),
	}
	if r.keys.projectionEnabled() {
		projection = "1"
		projectionKeys[2] = r.keys.projectionGeneration
		projectionKeys[3] = r.keys.projectionDirty
	}
	_, err := r.client.Eval(ctx, redisAccountConcurrencyReleaseScript, projectionKeys,
		member, projection, accountID, strconv.FormatInt(r.clock(), 10)).Result()
	return err
}
