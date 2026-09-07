package main

// Redis 账户 API Key 轮转计数器适配器（BUG-0174 B-3 波1遗留接线）：
// gatewaydispatch.APIKeyRotationCounter 的 Redis 版，逐条移植归档
// storage/account-api-key-rotation.ts:269-299
// nextRedisAccountApiKeyRotationIndex：
//
//	键名：  juhe-ai:route-state:account-api-key:<strategy>:<base64url(accountId)>
//	        （归档 :297-299 字面量键，不走 redisNamespacedKey 命名空间后缀）
//	脚本：  INCR + PEXPIRE 30d，返回 (value - 1) % modulo（:282-289 原文）
//	超时：  runRedisOperationWithDeadline 3s（:47）
//	TTL：   redisAccountApiKeyRotationTtlMs = 30*24*60*60*1000（:46）
//
// 组合根在 runtimeStateDriver === 'redis' 时经
// chainRuntimeServices.StateClient（Node runtimeConfig.redis.stateUrl 同源
// 客户端）注入 Engine.KeyRotation；客户端为 nil（memory 驱动）时适配器返回
// nil，引擎回落包级进程内计数器 defaultAPIKeyRotationCounter——不新增硬依赖
// （与 turn-retry 状态存储 chain_turn_retry_redis.go 的装配分叉一致）。

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// chainAccountAPIKeyRotationTtlMs mirrors redisAccountApiKeyRotationTtlMs
// (归档 :46)：30 天。
const chainAccountAPIKeyRotationTtlMs = 30 * 24 * 60 * 60 * 1000

// chainAccountAPIKeyRotationTimeout mirrors
// redisAccountApiKeyRotationOperationTimeoutMs (归档 :47)。
const chainAccountAPIKeyRotationTimeout = 3 * time.Second

// chainAccountAPIKeyRotationScript mirrors the archived eval script verbatim
// (归档 :283-289).
var chainAccountAPIKeyRotationScript = redis.NewScript(`
      local value = redis.call('INCR', KEYS[1])
      redis.call('PEXPIRE', KEYS[1], ARGV[2])
      return (value - 1) % tonumber(ARGV[1])
    `)

// chainAPIKeyRotationRedisCounter implements
// gatewaydispatch.APIKeyRotationCounter over the shared runtime-state client.
type chainAPIKeyRotationRedisCounter struct {
	client *redis.Client
}

// NextIndex mirrors nextRedisAccountApiKeyRotationIndex: INCR + PEXPIRE 30d on
// the literal route-state key, returning (value - 1) % modulo in [0, modulo).
func (c *chainAPIKeyRotationRedisCounter) NextIndex(ctx context.Context, accountID, strategy string, modulo int) (int, error) {
	if modulo <= 0 {
		return 0, nil
	}
	if c == nil || c.client == nil {
		return 0, fmt.Errorf("账户 API Key 轮换 Redis 计数器缺少客户端")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, chainAccountAPIKeyRotationTimeout)
	defer cancel()
	index, err := chainAccountAPIKeyRotationScript.Run(callCtx, c.client,
		[]string{chainAccountAPIKeyRotationKey(accountID, strategy)},
		strconv.Itoa(modulo), strconv.FormatInt(chainAccountAPIKeyRotationTtlMs, 10)).Int()
	if err != nil {
		return 0, err
	}
	if index < 0 {
		return 0, nil
	}
	return index, nil
}

// chainAccountAPIKeyRotationKey mirrors redisAccountApiKeyRotationKey
// (归档 :297-299)：Buffer.from(accountId).toString('base64url') 即无填充
// base64url。
func chainAccountAPIKeyRotationKey(accountID, strategy string) string {
	return "juhe-ai:route-state:account-api-key:" + strategy + ":" +
		base64.RawURLEncoding.EncodeToString([]byte(accountID))
}

// newChainAPIKeyRotationCounterOrNil builds the adapter for the chain deps: a
// nil client (runtimeStateDriver !== 'redis') yields nil so the engine keeps
// the in-process counter fallback.
func newChainAPIKeyRotationCounterOrNil(client *redis.Client) gatewaydispatch.APIKeyRotationCounter {
	if client == nil {
		return nil
	}
	return &chainAPIKeyRotationRedisCounter{client: client}
}

// compile-time: the adapter satisfies the rotation counter port.
var _ gatewaydispatch.APIKeyRotationCounter = (*chainAPIKeyRotationRedisCounter)(nil)
