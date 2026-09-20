package main

// routestrategies 组合根接线适配器：M06 切片两个 nil-safe 端口的生产实现
// （internal/routestrategies/core.go 的 SetValidationCacheInvalidator /
// SetSpeedFirstRuntimeFacade）。
//
//	ValidationCacheInvalidator → *inval.Bus（topic:gateway_api_key_validation_cache，
//	                           对照 apikeys.BusInvalidator.InvalidateValidation
//	                           的既有失效链）
//	SpeedFirstRuntimeFacade    → gatewayproxyhealth.LatencyDegradationService
//	                           （Node route-strategy-speed-first-runtime.facade.ts
//	                           的 store 读 + per-strategy 清理）

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// gatewayAPIKeyValidationBusInvalidator 把 *inval.Bus 适配成
// routestrategies.ValidationCacheInvalidator（Node
// notifyGatewayApiKeyValidationCacheInvalidation）。发布落在
// apikeys.BusInvalidator.InvalidateValidation 同一条 K5 主题上；
// gatewayruntimecache.handleAPIKeyValidationTopicInvalidation 对不带
// apiKeyId 后缀的 reason（"route_strategy_updated"）做无差别全清
// （InvalidateGatewayRuntimeCacheByAPIKeyID("", nil)），对齐归档
// gateway-api-key.repository.ts registerGatewayApiKeyValidationCacheInvalidator
// 的无 apiKeyId 分支（clearGatewayApiKeyValidationCacheAsync）。
//
// 与 Bus 链的关系：mutations.go 的同一个 runtime-relevant PATCH 先发
// topic:gateway_runtime_cache（该 reason 同样在 Node runtime invalidator 的
// shouldInvalidateGatewayApiKeyProcessCache 名单内，双覆盖是 Node 原生形态）。
// 本显式失效与之重复但幂等（全清 + 代际推进），保留它是为了维持 Node 的
// 双通知契约：未来任一主题的处理面分化时，validation-cache 失效不会静默丢失。
//
// 与 Node 500 契约的偏差：Node 把失效失败渲染为 500
// （GatewayApiKeyValidationCacheInvalidationError → PATCH 500 策略路由已更新，
// 但 API Key validation cache 失效失败）。K5 总线的 Invalidate 同步执行且没有
// 失败通道（共享 Redis 发布失败只在总线内部记日志），所以本适配器恒返回 nil，
// 500 分支结构性不可达；routestrategies 的端口错误语义原样保留（未来可失败的
// 适配器仍会走到 Node 的 500 渲染）。
type gatewayAPIKeyValidationBusInvalidator struct{ bus *inval.Bus }

func (i gatewayAPIKeyValidationBusInvalidator) InvalidateValidationCache(reason string) error {
	if i.bus == nil {
		return nil
	}
	i.bus.Invalidate(inval.TopicGatewayAPIKeyValidation, reason)
	return nil
}

// routeStrategySpeedFirstFacade 把 gatewayproxyhealth.LatencyDegradationService
// 适配成 routestrategies.SpeedFirstRuntimeFacade（Node
// route-strategy-speed-first-runtime.facade.ts：原始降级运行态读 +
// per-strategy 清理；账户去重与降级计数在 routestrategies 侧完成）。
// 存储错误以 available=false 交给路由层渲染 runtimeAvailable:false
// （Node facade 的 unavailable 形态，请求不失败）。
type routeStrategySpeedFirstFacade struct {
	service *gatewayproxyhealth.LatencyDegradationService
}

func (f routeStrategySpeedFirstFacade) ListDegradedRuntime(ctx context.Context, systemAccountID *string, routeStrategyIDs []string) ([]routestrategies.SpeedFirstRuntimeItem, bool, error) {
	if f.service == nil {
		return nil, false, nil
	}
	items, err := f.service.ListNormalRouteLatencyDegradedRuntime(ctx, gatewayproxyhealth.ListNormalRouteLatencyDegradedRuntimeInput{
		SystemAccountID:  systemAccountID,
		RouteStrategyIDs: routeStrategyIDs,
	})
	if err != nil {
		return nil, false, err
	}
	out := make([]routestrategies.SpeedFirstRuntimeItem, 0, len(items))
	for _, item := range items {
		out = append(out, routestrategies.SpeedFirstRuntimeItem{
			AccountID:                      item.AccountID,
			AccountName:                    item.AccountName,
			Scope:                          routestrategies.SpeedFirstRuntimeScope(item.ScopeRouteStrategyID, item.ScopeGroupID),
			SlowCount:                      item.SlowCount,
			SlowTriggerCount:               item.SlowTriggerCount,
			SlowWindowSeconds:              item.SlowWindowSeconds,
			DegradedUntil:                  item.DegradedUntil,
			NextProbeAt:                    item.NextProbeAt,
			RecoverySuccessCount:           item.RecoverySuccessCount,
			RequiredRecoverySuccessCount:   item.RequiredRecoverySuccessCount,
			RecoveryProbeRoundAttemptCount: item.RecoveryProbeRoundAttemptCount,
			RecoveryProbeRoundSuccessCount: item.RecoveryProbeRoundSuccessCount,
			Reason:                         item.Reason,
		})
	}
	return out, true, nil
}

func (f routeStrategySpeedFirstFacade) ClearDegradedRuntime(ctx context.Context, routeStrategyID string) (int, error) {
	if f.service == nil {
		return 0, nil
	}
	cleared, err := f.service.ClearNormalRouteLatencyDegradationForRouteStrategy(ctx, routeStrategyID)
	return int(cleared), err
}
