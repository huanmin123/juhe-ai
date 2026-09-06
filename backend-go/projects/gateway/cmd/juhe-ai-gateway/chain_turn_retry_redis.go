package main

// Redis driver for the codex turn-retry avoidance state: the composition-root
// TurnRetryStateStore adapter (gatewaycodex.TurnRetryStateStore = the consumed
// surface of shared/runtime-state-store.ts: getJson / compareSetJson / incr).
//
// The gateway module cannot import the shared Node store implementation and
// the other gateway Redis drivers (gatewayhybrid / gatewayquota) expose
// different store-name-keyed surfaces, so this file replicates the exact
// Redis contract of RedisRuntimeStateStore('gateway-codex-turn-retry'):
//
//	key space:  redisNamespacedKey(`juhe-ai:state:gateway-codex-turn-retry:`) + key
//	            (the keys handed in by the TurnRetryService already carry the
//	            Node "state:" / "generation:" prefixes of
//	            codex-turn-retry.service.ts)
//	getJson:    GET → redis.Nil → absent; an unparseable value deletes and
//	            reads as absent (Node catch);
//	compareSet: the compareSetJsonScript verbatim (empty expected = must not
//	            exist; otherwise byte equality; SET .. PX on success);
//	incr:       the incrWithMaxScript verbatim without max (first write sets
//	            the PX window, later increments keep it).
//
// nil client (runtimeStateDriver !== 'redis') keeps the TurnRetryService on
// its in-memory driver.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
)

// chainTurnRetryRedisStoreName mirrors createRuntimeStateStore('gateway-codex-turn-retry').
const chainTurnRetryRedisStoreName = "gateway-codex-turn-retry"

// chainTurnRetryCompareSetScript mirrors shared/runtime-state-store.ts
// compareSetJsonScript verbatim.
var chainTurnRetryCompareSetScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if ARGV[1] == '' then
  if current then
    return 0
  end
elseif current ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
return 1
`)

// chainTurnRetryIncrScript mirrors incrWithMaxScript verbatim (ARGV[2] empty:
// no max).
var chainTurnRetryIncrScript = redis.NewScript(`
local current = tonumber(redis.call('GET', KEYS[1]) or '0') or 0
local next_value = current + 1
if ARGV[2] ~= '' and next_value > tonumber(ARGV[2]) then
  return next_value
end
if current == 0 then
  redis.call('SET', KEYS[1], tostring(next_value), 'PX', ARGV[1])
else
  redis.call('INCR', KEYS[1])
  if redis.call('PTTL', KEYS[1]) < 0 then
    redis.call('PEXPIRE', KEYS[1], ARGV[1])
  end
end
return next_value
`)

// chainTurnRetryRedisStateStore implements gatewaycodex.TurnRetryStateStore
// over the runtime-state redis client.
type chainTurnRetryRedisStateStore struct {
	client *redis.Client
	prefix string
}

// newChainTurnRetryRedisStateStore builds the adapter; a nil client returns
// nil so the caller keeps the memory driver (Node runtimeStateDriver !==
// 'redis' fork).
func newChainTurnRetryRedisStateStore(client *redis.Client, namespace string) (*chainTurnRetryRedisStateStore, error) {
	if client == nil {
		return nil, nil
	}
	prefix, err := chainTurnRetryKeyPrefix(namespace)
	if err != nil {
		return nil, err
	}
	return &chainTurnRetryRedisStateStore{client: client, prefix: prefix}, nil
}

// chainTurnRetryKeyPrefix mirrors redisNamespacedKey(`juhe-ai:state:<name>:`):
// accept the short namespace or the full juhe-ai: prefix, never double-prefix
// (same normalization the sibling gateway drivers use).
func chainTurnRetryKeyPrefix(namespace string) (string, error) {
	normalized := strings.TrimRight(strings.TrimSpace(namespace), ":")
	if normalized == "" {
		return "", errors.New("codex turn retry redis 驱动缺少 namespace")
	}
	if !strings.HasPrefix(normalized, "juhe-ai:") {
		normalized = "juhe-ai:" + normalized
	}
	return fmt.Sprintf("%s:state:%s:", normalized, chainTurnRetryRedisStoreName), nil
}

func (s *chainTurnRetryRedisStateStore) key(key string) string {
	return s.prefix + key
}

// GetJSON mirrors getJson: an absent or unparseable value reads as absent
// (the unparseable value deletes like the Node catch).
func (s *chainTurnRetryRedisStateStore) GetJSON(ctx context.Context, key string) (json.RawMessage, error) {
	raw, err := s.client.Get(ctx, s.key(key)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(raw)) {
		_ = s.client.Del(ctx, s.key(key)).Err()
		return nil, nil
	}
	return json.RawMessage(raw), nil
}

// CompareSetJSON mirrors compareSetJson: an empty expected requires the key
// to be absent; otherwise the stored bytes must equal the expected JSON
// (compareSetJsonScript, PX window on success).
func (s *chainTurnRetryRedisStateStore) CompareSetJSON(ctx context.Context, key string, expected json.RawMessage, next any, ttlMs int64) (bool, error) {
	encoded, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	expectedArg := ""
	if len(expected) > 0 {
		expectedArg = string(expected)
	}
	result, err := chainTurnRetryCompareSetScript.Run(ctx, s.client, []string{s.key(key)}, expectedArg, encoded, chainNormalizedTtlMs(ttlMs)).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

// Incr mirrors incr without max (incrWithMaxScript): the first write opens
// the TTL window, later increments preserve it.
func (s *chainTurnRetryRedisStateStore) Incr(ctx context.Context, key string, ttlMs int64) (int64, error) {
	result, err := chainTurnRetryIncrScript.Run(ctx, s.client, []string{s.key(key)}, chainNormalizedTtlMs(ttlMs), "").Int()
	if err != nil {
		return 0, err
	}
	return int64(result), nil
}

// newChainTurnRetryRedisStateStoreOrNil builds the adapter for the chain
// deps: a nil client (runtimeStateDriver !== 'redis') yields nil so the
// TurnRetryService keeps its memory driver; a construction failure fails the
// assembly with the named cause instead of degrading silently.
func newChainTurnRetryRedisStateStoreOrNil(client *redis.Client, namespace string) gatewaycodex.TurnRetryStateStore {
	store, err := newChainTurnRetryRedisStateStore(client, namespace)
	if err != nil {
		slog.Error("codex turn retry redis 状态驱动构建失败", "event", "chain_turn_retry_redis_store_failed", "error", err)
		return nil
	}
	if store == nil {
		return nil
	}
	return store
}

// compile-time: the adapter satisfies the turn-retry store port.
var _ gatewaycodex.TurnRetryStateStore = (*chainTurnRetryRedisStateStore)(nil)

// chainTurnRetryRedisDeadlineMS mirrors the Node runRedisOperationWithDeadline
// default (3s); the call sites bound it through their own context deadlines.
const chainTurnRetryRedisDeadlineMS = 3_000
