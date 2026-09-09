package main

// compose_prewarm.go — 网关 API Key 校验缓存预热接线（Node server.ts:181 的
// fire-and-forget 启动序列，verdict-ap prewarmGatewayApiKeyValidationCacheAsync
// TRULY_MISSING 补齐）：组合完成后异步执行一次预热，成功记
// gateway_api_key_cache_prewarmed（带预热 Key 数），失败记
// gateway_api_key_cache_prewarm_failed——两条事件名与 Node 逐字一致，失败只
// 告警不阻断启动（Node `.catch(warn)` 契约）。Node 的
// performanceNodeRole === 'control-replica' 跳过分支在 Go 网关进程无对等角色
// （Go 网关恒为业务持有者），无需对应开关。

import (
	"context"
	"log/slog"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// gatewayAPIKeyCachePrewarmTimeout bounds the whole startup pass; the natural
// per-read budget rides GatewayRuntimeLoadTimeout inside the models reads.
const gatewayAPIKeyCachePrewarmTimeout = 60 * time.Second

// startGatewayAPIKeyCachePrewarm runs the prewarm once, detached from the
// supervisor lifecycle (the cache is read-only state; an in-flight pass dies
// with the process at shutdown without holding locks).
func startGatewayAPIKeyCachePrewarm(cache *gatewayruntimecache.Service, logger *slog.Logger) {
	if cache == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), gatewayAPIKeyCachePrewarmTimeout)
		defer cancel()
		apiKeyCount, err := cache.PrewarmGatewayAPIKeyValidationCache(ctx)
		if err != nil {
			logger.Warn("gateway_api_key_cache_prewarm_failed",
				"err", err.Error(),
				"apiKeyCount", apiKeyCount)
			return
		}
		logger.Info("gateway_api_key_cache_prewarmed", "apiKeyCount", apiKeyCount)
	}()
}
