package keymodelrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
	"github.com/redis/go-redis/v9"
)

const redisGroup = "gateway-account-circuit-key-model"

var namespacePartPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

type RedisConfig struct {
	Enabled   bool
	URL       string
	Namespace string
}

func LoadRedisConfig(getenv func(string) string) (RedisConfig, error) {
	if getenv == nil {
		return RedisConfig{}, errors.New("getenv 不能为空")
	}
	cfg := RedisConfig{URL: strings.TrimSpace(getenv("JUHE_AI_REDIS_URL")), Namespace: strings.TrimSpace(getenv("JUHE_AI_REDIS_NAMESPACE"))}
	// Redis state selects the distributed profile; jobs without a Redis state
	// URL are the standalone topology.
	cfg.Enabled = cfg.URL != ""
	if !cfg.Enabled {
		return cfg, nil
	}
	// 说明：此处曾有一个 `if cfg.URL == ""` 守卫，但该分支内 cfg.Enabled 为真
	// 已确保 URL 非空，属永假死代码，按覆盖率清理授权删除。
	if !namespacePartPattern.MatchString(cfg.Namespace) {
		return RedisConfig{}, errors.New("启用 model-recovery 必须配置合法 JUHE_AI_REDIS_NAMESPACE")
	}
	// 命名空间入口 canonical 化（2026-09-22 对齐 gateway 加载层）：剥除
	// `juhe-ai:` 全前缀配置（如 juhe-ai:dev），避免 NewRedisKeys 直接拼接产生
	// `juhe-ai:juhe-ai:...` 键空间分裂；短名输入逐字节不变。原始值校验先行
	// 保留 fail closed 语义，canonical 结果复检同一规则（canonical 为空同样拒绝）。
	cfg.Namespace = rediscfg.CanonicalRedisNamespace(cfg.Namespace)
	if !namespacePartPattern.MatchString(cfg.Namespace) {
		return RedisConfig{}, errors.New("启用 model-recovery 必须配置合法 JUHE_AI_REDIS_NAMESPACE")
	}
	// 与 gateway key_model_runtime 使用同一域 namespace；否则 recovery 扫的是
	// 基础 namespace，永远看不到 gateway 写在 {ns:keymodel} 下的 due/state。
	cfg.Namespace = rediscfg.DomainNamespace(cfg.Namespace, rediscfg.DomainKeyModel)
	return cfg, nil
}

type RedisKeys struct{ Prefix string }

func NewRedisKeys(namespace string) (RedisKeys, error) {
	if !namespacePartPattern.MatchString(strings.TrimSpace(namespace)) {
		return RedisKeys{}, errors.New("Redis namespace 无效")
	}
	return RedisKeys{Prefix: rediscfg.NamespacedKey("juhe-ai:"+redisGroup, strings.TrimSpace(namespace))}, nil
}
func (k RedisKeys) State(hash string) string { return k.Prefix + ":state:" + hash }
func (k RedisKeys) Lease(hash string) string { return k.Prefix + ":lease:" + hash }
func (k RedisKeys) Due() string              { return k.Prefix + ":due" }
func (k RedisKeys) Closed() string           { return k.Prefix + ":closed" }
func (k RedisKeys) GlobalProbes() string     { return k.Prefix + ":recovery:global" }
func (k RedisKeys) SourceProbes(sourceID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(sourceID)))
	return k.Prefix + ":recovery:source:" + fmt.Sprintf("%x", digest[:])
}

type RedisStore struct {
	client redis.UniversalClient
	keys   RedisKeys
	logger *slog.Logger
}

func OpenRedisStore(cfg RedisConfig) (*RedisStore, error) {
	if !cfg.Enabled {
		return nil, errors.New("model-recovery Redis 未启用")
	}
	client, err := rediscfg.NewUniversalClient(rediscfg.Config{URL: cfg.URL})
	if err != nil {
		return nil, fmt.Errorf("创建 model-recovery Redis 客户端: %w", err)
	}
	keys, err := NewRedisKeys(cfg.Namespace)
	if err != nil {
		return nil, err
	}
	return &RedisStore{client: client, keys: keys, logger: slog.Default()}, nil
}

func (s *RedisStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// SetLogger 注入组合根 logger，使 ListDue 的脏 state 隔离告警进入统一日志
// 出口（NewRunner 装配时自动接线；nil 回落 slog.Default）。
func (s *RedisStore) SetLogger(logger *slog.Logger) {
	if s == nil || logger == nil {
		return
	}
	s.logger = logger
}
func (s *RedisStore) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

func (s *RedisStore) ServerNow(ctx context.Context) (time.Time, error) {
	value, err := s.client.Time(ctx).Result()
	if err != nil {
		return time.Time{}, err
	}
	return value.UTC(), nil
}

func (s *RedisStore) ListDue(ctx context.Context, now time.Time, limit int64) ([]State, error) {
	if limit < 1 {
		return nil, errors.New("model-recovery due limit 必须为正数")
	}
	hashes, err := s.client.ZRangeByScore(ctx, s.keys.Due(), &redis.ZRangeBy{Min: "-inf", Max: fmt.Sprintf("%d", now.UnixMilli()), Offset: 0, Count: limit}).Result()
	if err != nil {
		return nil, err
	}
	states := make([]State, 0, len(hashes))
	for _, hash := range hashes {
		raw, readErr := s.client.Get(ctx, s.keys.State(hash)).Bytes()
		if readErr == redis.Nil {
			// 缺失 state 的 due 成员永远无法处理，若保留会在每轮扫描中占用
			// 有界窗口；与坏 state 同口径从 due 集合移除后继续。
			if err := s.client.ZRem(ctx, s.keys.Due(), hash).Err(); err != nil {
				return nil, fmt.Errorf("清理 model-recovery 缺失 state 的 due 成员: %w", err)
			}
			s.warnRejectedDueState(hash, "missing_state")
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		var state State
		if err := json.Unmarshal(raw, &state); err != nil || state.CapabilityHash != hash {
			reason := "invalid_state"
			if err == nil {
				reason = "capability_hash_mismatch"
			}
			// 单条脏 state 不得阻塞整轮恢复扫描：移除该 due 成员并告警后继续。
			if zerr := s.client.ZRem(ctx, s.keys.Due(), hash).Err(); zerr != nil {
				return nil, fmt.Errorf("清理 model-recovery 损坏 due 成员: %w", zerr)
			}
			s.warnRejectedDueState(hash, reason)
			continue
		}
		// HALF_OPEN 仅在探测租约过期（或缺失）时重新进入 due；租约仍有效的
		// state 留给现有 owner，避免扫描层与 Redis acquire 产生无意义竞争。
		if state.Phase == HalfOpen && !recoveryDue(state, now) {
			continue
		}
		states = append(states, state)
	}
	sort.SliceStable(states, func(i, j int) bool {
		if states[i].Phase != states[j].Phase {
			return states[i].Phase == Recovering
		}
		return states[i].RetryAt.Before(states[j].RetryAt)
	})
	return states, nil
}

const capabilityHashLogPrefixLength = 12

// warnRejectedDueState 记录被隔离的 due 成员。只输出 capabilityHash 前缀与
// 拒绝原因，不输出 state 原文或任何凭据内容。
func (s *RedisStore) warnRejectedDueState(hash, reason string) {
	logger := s.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("model-recovery due state rejected",
		"event", "model_recovery_due_state_rejected",
		"capabilityHashPrefix", capabilityHashLogPrefix(hash),
		"reason", reason)
}

func capabilityHashLogPrefix(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > capabilityHashLogPrefixLength {
		return hash[:capabilityHashLogPrefixLength]
	}
	return hash
}

func (s *RedisStore) CleanClosed(ctx context.Context, limit int64) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("model-recovery closed cleanup limit 必须在 1..1000")
	}
	result, err := s.client.Eval(ctx, cleanClosedScript, []string{s.keys.Closed(), s.keys.Due(), s.keys.Prefix + ":capacity"}, limit, s.keys.Prefix+":state:").Int64()
	return result, err
}

func (s *RedisStore) Acquire(ctx context.Context, candidate State, leaseID string, continuationWaiting bool, sourceContinuationWaiting bool) (State, MutationStatus, error) {
	result, err := s.client.Eval(ctx, acquireRecoveryLeaseScript, []string{s.keys.State(candidate.CapabilityHash), s.keys.Lease(candidate.CapabilityHash), s.keys.Due(), s.keys.GlobalProbes(), s.keys.SourceProbes(candidate.CredentialSourceAccountID)}, candidate.Generation, candidate.DispatchRevision, leaseID, ProbeLease.Milliseconds(), boolArg(continuationWaiting), boolArg(sourceContinuationWaiting), string(candidate.Phase)).Slice()
	if err != nil {
		return State{}, "", err
	}
	if len(result) != 2 {
		return State{}, "", errors.New("model-recovery acquire 返回结构无效")
	}
	status := MutationStatus(fmt.Sprint(result[0]))
	if status != Applied {
		return candidate, status, nil
	}
	var state State
	if err := json.Unmarshal([]byte(fmt.Sprint(result[1])), &state); err != nil {
		return State{}, "", err
	}
	return state, status, nil
}

func (s *RedisStore) Renew(ctx context.Context, state State, leaseID string) (bool, error) {
	result, err := s.client.Eval(ctx, renewRecoveryLeaseScript, []string{s.keys.State(state.CapabilityHash), s.keys.Lease(state.CapabilityHash), s.keys.GlobalProbes(), s.keys.SourceProbes(state.CredentialSourceAccountID)}, state.Generation, state.DispatchRevision, leaseID, ProbeLease.Milliseconds()).Int()
	return result == 1, err
}

func (s *RedisStore) Commit(ctx context.Context, prior State, next State, leaseID string) (MutationStatus, error) {
	encoded, err := json.Marshal(next)
	if err != nil {
		return "", err
	}
	retryAt := int64(0)
	if !next.RetryAt.IsZero() {
		retryAt = next.RetryAt.UnixMilli()
	}
	result, err := s.client.Eval(ctx, commitRecoveryResultScript, []string{s.keys.State(prior.CapabilityHash), s.keys.Lease(prior.CapabilityHash), s.keys.Due(), s.keys.GlobalProbes(), s.keys.SourceProbes(prior.CredentialSourceAccountID), s.keys.Closed()}, prior.Generation, prior.DispatchRevision, leaseID, string(encoded), retryAt, string(next.Phase)).Text()
	return MutationStatus(result), err
}

const acquireRecoveryLeaseScript = `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local raw = redis.call('GET', KEYS[1])
if not raw then return {'stale', ''} end
local state = cjson.decode(raw)
if tonumber(state.generation) ~= tonumber(ARGV[1]) or tonumber(state.dispatchRevision) ~= tonumber(ARGV[2]) then return {'stale', raw} end
if state.phase ~= 'OPEN' and state.phase ~= 'RECOVERING' and state.phase ~= 'HALF_OPEN' then return {'not_due', raw} end
if state.phase == 'HALF_OPEN' then
  -- 过期租约接管（设计 §8.5）：旧 owner 的 lease 键已随 45s TTL 消失时才
  -- 允许新 worker 接管；键仍存在说明 owner 活跃（或续租中），保持 not_due。
  if redis.call('EXISTS', KEYS[2]) == 1 then return {'not_due', raw} end
else
  if tonumber(redis.call('ZSCORE', KEYS[3], state.capabilityHash) or '0') > now then return {'not_due', raw} end
end
if redis.call('SET', KEYS[2], ARGV[3], 'NX', 'PX', ARGV[4]) == false then return {'lease_mismatch', raw} end
redis.call('ZREMRANGEBYSCORE', KEYS[4], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[5], '-inf', now)
local globalLimit = 32
local sourceLimit = 2
if ARGV[5] == 'true' and ARGV[6] == 'true' and ARGV[7] == 'OPEN' then globalLimit = 24; sourceLimit = 1 end
if tonumber(redis.call('ZCARD', KEYS[4])) >= globalLimit or tonumber(redis.call('ZCARD', KEYS[5])) >= sourceLimit then
  redis.call('DEL', KEYS[2])
  return {'not_due', raw}
end
local leaseUntil = now + tonumber(ARGV[4])
redis.call('ZADD', KEYS[4], leaseUntil, ARGV[3])
redis.call('ZADD', KEYS[5], leaseUntil, ARGV[3])
state.phase = 'HALF_OPEN'
state.probeLease = {leaseId=ARGV[3], leaseUntilMs=leaseUntil, priorSuccessCount=tonumber(state.recoverySuccessCount or 0)}
local encoded = cjson.encode(state)
redis.call('SET', KEYS[1], encoded)
return {'applied', encoded}
`

const renewRecoveryLeaseScript = `
local raw = redis.call('GET', KEYS[1])
if not raw or redis.call('GET', KEYS[2]) ~= ARGV[3] then return 0 end
local state = cjson.decode(raw)
if tonumber(state.generation) ~= tonumber(ARGV[1]) or tonumber(state.dispatchRevision) ~= tonumber(ARGV[2]) then return 0 end
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local leaseUntil = now + tonumber(ARGV[4])
redis.call('PEXPIRE', KEYS[2], ARGV[4])
redis.call('ZADD', KEYS[3], leaseUntil, ARGV[3])
redis.call('ZADD', KEYS[4], leaseUntil, ARGV[3])
state.probeLease.leaseUntilMs = leaseUntil
redis.call('SET', KEYS[1], cjson.encode(state))
return 1
`

const commitRecoveryResultScript = `
local raw = redis.call('GET', KEYS[1])
if not raw or redis.call('GET', KEYS[2]) ~= ARGV[3] then return 'stale' end
local state = cjson.decode(raw)
if tonumber(state.generation) ~= tonumber(ARGV[1]) or tonumber(state.dispatchRevision) ~= tonumber(ARGV[2]) then return 'stale' end
redis.call('SET', KEYS[1], ARGV[4])
redis.call('DEL', KEYS[2])
redis.call('ZREM', KEYS[4], ARGV[3])
redis.call('ZREM', KEYS[5], ARGV[3])
if ARGV[6] == 'CLOSED' then redis.call('ZREM', KEYS[3], state.capabilityHash) else redis.call('ZADD', KEYS[3], ARGV[5], state.capabilityHash) end
if ARGV[6] == 'CLOSED' then
  local redisTime = redis.call('TIME')
  local retainedUntil = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000) + 300000
  redis.call('ZADD', KEYS[6], retainedUntil, state.capabilityHash)
else
  redis.call('ZREM', KEYS[6], state.capabilityHash)
end
return 'applied'
`

const cleanClosedScript = `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local hashes = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now, 'LIMIT', 0, ARGV[1])
local removed = 0
for _, hash in ipairs(hashes) do
  local stateKey = ARGV[2] .. hash
  local raw = redis.call('GET', stateKey)
  if raw and cjson.decode(raw).phase == 'CLOSED' then
    redis.call('DEL', stateKey)
    redis.call('ZREM', KEYS[2], hash)
    redis.call('DECR', KEYS[3])
    removed = removed + 1
  end
  redis.call('ZREM', KEYS[1], hash)
end
return removed
`

func boolArg(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
