package keymodelrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

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
	cfg := RedisConfig{URL: strings.TrimSpace(getenv("JUHE_AI_REDIS_STATE_URL")), Namespace: strings.TrimSpace(getenv("JUHE_AI_REDIS_NAMESPACE"))}
	// The runtime guard is part of the Redis-state profile and has no kill
	// switch. Jobs without a Redis state URL are the standalone topology.
	cfg.Enabled = cfg.URL != ""
	if !cfg.Enabled {
		return cfg, nil
	}
	if cfg.URL == "" {
		return RedisConfig{}, errors.New("启用 model-recovery 必须配置 JUHE_AI_REDIS_STATE_URL")
	}
	if !namespacePartPattern.MatchString(cfg.Namespace) {
		return RedisConfig{}, errors.New("启用 model-recovery 必须配置合法 JUHE_AI_REDIS_NAMESPACE")
	}
	return cfg, nil
}

type RedisKeys struct{ Prefix string }

func NewRedisKeys(namespace string) (RedisKeys, error) {
	if !namespacePartPattern.MatchString(strings.TrimSpace(namespace)) {
		return RedisKeys{}, errors.New("Redis namespace 无效")
	}
	return RedisKeys{Prefix: "juhe-ai:" + strings.TrimSpace(namespace) + ":" + redisGroup}, nil
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
	client *redis.Client
	keys   RedisKeys
}

func OpenRedisStore(cfg RedisConfig) (*RedisStore, error) {
	if !cfg.Enabled {
		return nil, errors.New("model-recovery Redis 未启用")
	}
	options, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("解析 model-recovery Redis URL: %w", err)
	}
	keys, err := NewRedisKeys(cfg.Namespace)
	if err != nil {
		return nil, err
	}
	return &RedisStore{client: redis.NewClient(options), keys: keys}, nil
}

func (s *RedisStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
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
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		var state State
		if json.Unmarshal(raw, &state) != nil || state.CapabilityHash != hash {
			return nil, errors.New("model-recovery Redis state 完整性校验失败")
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

func (s *RedisStore) CleanClosed(ctx context.Context, limit int64) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("model-recovery closed cleanup limit 必须在 1..1000")
	}
	result, err := s.client.Eval(ctx, cleanClosedScript, []string{s.keys.Closed(), s.keys.Due(), s.keys.Prefix + ":capacity"}, limit, s.keys.Prefix+":state:").Int64()
	return result, err
}

func (s *RedisStore) Acquire(ctx context.Context, candidate State, leaseID string, continuationWaiting bool) (State, MutationStatus, error) {
	result, err := s.client.Eval(ctx, acquireRecoveryLeaseScript, []string{s.keys.State(candidate.CapabilityHash), s.keys.Lease(candidate.CapabilityHash), s.keys.Due(), s.keys.GlobalProbes(), s.keys.SourceProbes(candidate.CredentialSourceAccountID)}, candidate.Generation, candidate.DispatchRevision, leaseID, ProbeLease.Milliseconds(), boolArg(continuationWaiting), string(candidate.Phase)).Slice()
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
	result, err := s.client.Eval(ctx, commitRecoveryResultScript, []string{s.keys.State(prior.CapabilityHash), s.keys.Lease(prior.CapabilityHash), s.keys.Due(), s.keys.GlobalProbes(), s.keys.SourceProbes(prior.CredentialSourceAccountID), s.keys.Closed()}, prior.Generation, prior.DispatchRevision, leaseID, string(encoded), retryAt, next.Phase).Text()
	return MutationStatus(result), err
}

const acquireRecoveryLeaseScript = `
local redisTime = redis.call('TIME')
local now = tonumber(redisTime[1]) * 1000 + math.floor(tonumber(redisTime[2]) / 1000)
local raw = redis.call('GET', KEYS[1])
if not raw then return {'stale', ''} end
local state = cjson.decode(raw)
if tonumber(state.generation) ~= tonumber(ARGV[1]) or tonumber(state.dispatchRevision) ~= tonumber(ARGV[2]) then return {'stale', raw} end
if state.phase ~= 'OPEN' and state.phase ~= 'RECOVERING' then return {'not_due', raw} end
if tonumber(redis.call('ZSCORE', KEYS[3], state.capabilityHash) or '0') > now then return {'not_due', raw} end
if redis.call('SET', KEYS[2], ARGV[3], 'NX', 'PX', ARGV[4]) == false then return {'lease_mismatch', raw} end
redis.call('ZREMRANGEBYSCORE', KEYS[4], '-inf', now)
redis.call('ZREMRANGEBYSCORE', KEYS[5], '-inf', now)
local globalLimit = 32
local sourceLimit = 2
if ARGV[5] == 'true' and ARGV[6] == 'OPEN' then globalLimit = 24; sourceLimit = 1 end
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
