package main

// 会话亲和 dispatch 读写面合流（第二轮确认补修）。修复前 dispatch 引擎的
// engine.Affinity 装配的是 newLocalSessionAffinity（进程内存 map + 24h TTL），
// 而 preauth 侧经 sessionAffinityAdapter 消费完整的 G14 AffinityService
// （Redis cacheDriver 时绑定落 Redis）——多实例/重启下 dispatch 写的亲和
// preauth 读不到、preauth 的 Redis 亲和 dispatch 也不用，读写两个数据面。
//
// chainSessionAffinityDispatchPort 把 gatewaydispatch.SessionAffinityPort 的
// 绑定读写四方法（Order/Claim/Remember/Forget）接到与 preauth 同一
// AffinityService 实例上；高并发忙判定（AreHighConcurrencyAccountsBusyForLane
// Async）继续走 localSessionAffinity：该维度是 F13 确立的「与 engine 并发槽 /
// high-concurrency 队列共用同一活计数 tracker」同源接线，而
// AffinityService.cfg.Concurrency 生产未装配（nil，Redis runtime-state 下恒
// 不忙），为不回退既有忙判定语义，此维度保持本进程事实源。
//
// 装配条件与 preauth 侧一致：deps.Identity.Affinity.UsesRedisSessionAffinity()
// （Redis cacheDriver）才启用本端口；memory cacheDriver（含组合测试
// deps.Identity == nil）保持 localSessionAffinity——「网关链端口显式降级」
// warn 只在该真实降级路径触发。

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaysession"
)

// chainSessionAffinityDispatchPort implements gatewaydispatch.SessionAffinityPort
// over the shared G14 AffinityService.
type chainSessionAffinityDispatchPort struct {
	service *gatewaysession.AffinityService
	// busy 承接高并发忙判定的活计数事实源（F13 同源），见文件头注释。
	busy *localSessionAffinity
}

// OrderAsync mirrors orderOpenAIAccountsBySessionAffinityAsync：排序经
// AffinityService 读 Redis 绑定（preauth / 失败 forget 写入的同一键空间），
// 并完整传递 dispatch 排序选项（GroupType / SchedulingPolicy / ModelPriority /
// TrafficMigrationScope，gatewaydispatch.AffinityOrderingOptions →
// gatewaysession.DispatchOrderingOptions；此前 localSessionAffinity 的签名
// 丢弃了 options）。TrafficMigrationScope 借 OrderAsync 的 options 传入，
// 服务侧用它读 traffic-migration 偏好——与 Node
// orderOpenAIAccountsBySessionAffinityAsync 的选项面一致。
func (p *chainSessionAffinityDispatchPort) OrderAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, sessionAffinityKey string, options gatewaydispatch.AffinityOrderingOptions) ([]gatewaydispatch.AccountCandidate, error) {
	return p.service.OrderOpenAIAccountsBySessionAffinityAsync(ctx, accounts, sessionAffinityKey, chainDispatchOrderingOptionsOf(options))
}

// ClaimAsync mirrors claimOpenAIAccountForSessionAsync（first binder wins；
// 重复 claim 同一账户刷新 TTL）。Redis 写失败时服务返回 ("", false) 并
// warn——引擎侧调用点忽略 ok 位、把空 claimedAccountID 按「无亲和」处理，
// 与 Node catch 分叉一致。
func (p *chainSessionAffinityDispatchPort) ClaimAsync(ctx context.Context, sessionAffinityKey, proposedAccountID string, scope gatewaydispatch.AffinityScope) (string, bool) {
	return p.service.ClaimOpenAIAccountForSessionAsync(ctx, sessionAffinityKey, proposedAccountID, chainAffinityScopePtr(scope))
}

// RememberAsync mirrors rememberOpenAIAccountForSessionAsync（同步 claim 语义，
// 引擎在尝试成功后调用）。
func (p *chainSessionAffinityDispatchPort) RememberAsync(ctx context.Context, sessionAffinityKey, accountID string, scope gatewaydispatch.AffinityScope) {
	p.service.RememberOpenAIAccountForSessionAsync(ctx, sessionAffinityKey, accountID, chainAffinityScopePtr(scope))
}

// ForgetAsync mirrors forgetOpenAIAccountForSessionAsync（Redis 清理失败
// warn 后吞错返回 nil，Node catch 分叉；失败派发链不因 forget 失败重试）。
func (p *chainSessionAffinityDispatchPort) ForgetAsync(ctx context.Context, sessionAffinityKey, accountID string) error {
	return p.service.ForgetOpenAIAccountForSessionAsync(ctx, sessionAffinityKey, accountID)
}

// AreHighConcurrencyAccountsBusyForLaneAsync 委托 localSessionAffinity 的
// 活计数实现（F13 同源：与 engine 并发槽 / high-concurrency 队列共用同一
// tracker；AffinityService 的忙判定读 cfg.Concurrency，生产未装配）。
func (p *chainSessionAffinityDispatchPort) AreHighConcurrencyAccountsBusyForLaneAsync(ctx context.Context, accounts []gatewaydispatch.AccountCandidate, options gatewaydispatch.HighConcurrencyBusyOptions) (bool, error) {
	return p.busy.AreHighConcurrencyAccountsBusyForLaneAsync(ctx, accounts, options)
}

// chainDispatchOrderingOptionsOf maps gatewaydispatch.AffinityOrderingOptions
// onto the G14 DispatchOrderingOptions. AccountCandidate 与
// gatewayruntimecache.OpenAIAccountSecret 是同一命名类型的别名，账户切片直接
// 透传；ModelPriority 两侧行结构同构（RequestedModel / SourceEndpointFamily /
// RankByAccountID），逐字段拷贝。
func chainDispatchOrderingOptionsOf(options gatewaydispatch.AffinityOrderingOptions) gatewaysession.DispatchOrderingOptions {
	converted := gatewaysession.DispatchOrderingOptions{
		GroupType: options.GroupType,
	}
	if options.SchedulingPolicy != nil {
		// GroupSchedulingPolicy 是 map[string]any 的命名别名，服务侧按
		// policyInput map 消费（scheduling.go），解引用共享底层 map 即可。
		converted.SchedulingPolicy = *options.SchedulingPolicy
	}
	if options.ModelPriority != nil {
		converted.ModelPriority = &gatewaysession.GatewayAccountModelPriority{
			RequestedModel:       options.ModelPriority.RequestedModel,
			SourceEndpointFamily: options.ModelPriority.SourceEndpointFamily,
			RankByAccountID:      options.ModelPriority.RankByAccountID,
		}
	}
	if options.TrafficMigrationScope != nil {
		converted.TrafficMigrationScope = chainAffinityScopePtr(*options.TrafficMigrationScope)
	}
	return converted
}

// chainAffinityScopePtr maps the frozen dispatch affinity scope onto the G14
// scope（同构三字段：SystemAccountID / APIKeyID / GroupID）。
func chainAffinityScopePtr(scope gatewaydispatch.AffinityScope) *gatewaysession.OpenAIGatewaySessionAffinityScope {
	return &gatewaysession.OpenAIGatewaySessionAffinityScope{
		SystemAccountID: scope.SystemAccountID,
		APIKeyID:        scope.APIKeyID,
		GroupID:         scope.GroupID,
	}
}

// newChainSessionAffinityDispatchPort picks the dispatch-side session affinity
// port at assembly time: the shared AffinityService when its cache driver is
// Redis（与 preauth 读侧同一数据面），否则 nil——调用方保持
// localSessionAffinity 显式降级（warn 保留在该路径）。
func newChainSessionAffinityDispatchPort(local *localSessionAffinity, services *sessionIdentityServices) gatewaydispatch.SessionAffinityPort {
	if services == nil || services.Affinity == nil || !services.Affinity.UsesRedisSessionAffinity() {
		return nil
	}
	return &chainSessionAffinityDispatchPort{service: services.Affinity, busy: local}
}
