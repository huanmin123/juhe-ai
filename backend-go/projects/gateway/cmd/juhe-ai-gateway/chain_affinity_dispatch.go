package main

// 会话亲和 dispatch 读写面合流（第二轮确认补修）。修复前 dispatch 引擎的
// engine.Affinity 装配的是 newLocalSessionAffinity（进程内存 map + 24h TTL），
// 而 preauth 侧经 sessionAffinityAdapter 消费完整的 G14 AffinityService
// （Redis cacheDriver 时绑定落 Redis）——多实例/重启下 dispatch 写的亲和
// preauth 读不到、preauth 的 Redis 亲和 dispatch 也不用，读写两个数据面。
//
// chainSessionAffinityDispatchPort 把 gatewaydispatch.SessionAffinityPort 的
// 绑定读写面（Order/Claim/Remember/Forget + 账户内 Key 黏性读/最终成功 Key
// 写回）接到与 preauth 同一 AffinityService 实例上。
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

// ClaimWithStateAsync 实现 gatewaydispatch.SessionAffinityClaimStatePort：
// 额外返回 claim 前是否已有绑定，供名额规划区分新会话占位 owner 与既有会话。
func (p *chainSessionAffinityDispatchPort) ClaimWithStateAsync(ctx context.Context, sessionAffinityKey, proposedAccountID string, scope gatewaydispatch.AffinityScope) (string, bool, bool) {
	return p.service.ClaimOpenAIAccountForSessionWithStateAsync(ctx, sessionAffinityKey, proposedAccountID, chainAffinityScopePtr(scope))
}

// RememberAsync mirrors rememberOpenAIAccountForSessionAsync（同步 claim 语义，
// 引擎在尝试成功后调用）。
func (p *chainSessionAffinityDispatchPort) RememberAsync(ctx context.Context, sessionAffinityKey, accountID string, scope gatewaydispatch.AffinityScope) {
	p.service.RememberOpenAIAccountForSessionAsync(ctx, sessionAffinityKey, accountID, chainAffinityScopePtr(scope))
}

// RebindAsync 实现 gatewaydispatch.SessionAffinityRebindPort（账户亲和同层
// latch）：同 owner 刷新 30 分钟 lease；owner 变化时 CAS 只允许当前 owner 仍是
// expectedOwner 才改绑。失败只返回 false（服务内已告警），不改变请求结果。
func (p *chainSessionAffinityDispatchPort) RebindAsync(ctx context.Context, sessionAffinityKey, expectedOwner, newOwner string, scope gatewaydispatch.AffinityScope) bool {
	return p.service.RebindOpenAIAccountForSessionAsync(ctx, sessionAffinityKey, expectedOwner, newOwner, chainAffinityScopePtr(scope))
}

// PlanSessionSlot 实现 gatewaydispatch.SessionAffinitySlotPort：准备期按账户
// 名额选择首选账户，owner 名额满时在预算内等待后回退到空闲名额账户。
func (p *chainSessionAffinityDispatchPort) PlanSessionSlot(ctx context.Context, sessionAffinityKey, ownerAccountID string, ownerEstablished bool, candidates []gatewaydispatch.SessionSlotCandidate, waitBudgetMs int64) (gatewaydispatch.SessionAffinitySlotPlan, error) {
	converted := make([]gatewaysession.SessionSlotCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		converted = append(converted, gatewaysession.SessionSlotCandidate{
			AccountID:        candidate.AccountID,
			ConcurrencyLimit: candidate.ConcurrencyLimit,
			RPMLimit:         candidate.RPMLimit,
		})
	}
	plan, err := p.service.PlanSessionSlot(ctx, sessionAffinityKey, ownerAccountID, ownerEstablished, converted, waitBudgetMs)
	return gatewaydispatch.SessionAffinitySlotPlan{
		PreferredAccountID: plan.PreferredAccountID,
		SlotActive:         plan.SlotActive,
		Fallback:           plan.Fallback,
		WaitedMs:           plan.WaitedMs,
	}, err
}

// ConfirmSessionSlot 实现最终成功后的名额结算。
func (p *chainSessionAffinityDispatchPort) ConfirmSessionSlot(ctx context.Context, sessionAffinityKey, expectedOwner, newOwner string, cap int) bool {
	return p.service.ConfirmSessionSlot(ctx, sessionAffinityKey, expectedOwner, newOwner, cap)
}

// ForgetAsync mirrors forgetOpenAIAccountForSessionAsync（Redis 清理失败
// warn 后吞错返回 nil，Node catch 分叉；失败派发链不因 forget 失败重试）。
func (p *chainSessionAffinityDispatchPort) ForgetAsync(ctx context.Context, sessionAffinityKey, accountID string) error {
	return p.service.ForgetOpenAIAccountForSessionAsync(ctx, sessionAffinityKey, accountID)
}

// StickyApiKeyFingerprintAsync 委托 AffinityService 的账户内 Key 黏性读：
// 绑定缺失、过期、owner 不匹配或读取失败在服务内降级为空串并告警，读不刷 TTL。
func (p *chainSessionAffinityDispatchPort) StickyApiKeyFingerprintAsync(ctx context.Context, sessionAffinityKey, accountID string) string {
	return p.service.StickyOpenAIApiKeyFingerprintAsync(ctx, sessionAffinityKey, accountID)
}

// RememberApiKeyFingerprintAsync 委托 AffinityService 的最终协议成功 Key 写回：
// 只更新现存同 owner 绑定（不创建、不改 owner）；记录缺失 / 过期 / 异 owner /
// 非法指纹在服务内跳过，Redis 侧最多两轮 GET + CAS。
func (p *chainSessionAffinityDispatchPort) RememberApiKeyFingerprintAsync(ctx context.Context, sessionAffinityKey, accountID, upstreamApiKeyFingerprint string) {
	p.service.RememberOpenAIApiKeyFingerprintAsync(ctx, sessionAffinityKey, accountID, upstreamApiKeyFingerprint)
}

// chainDispatchOrderingOptionsOf maps gatewaydispatch.AffinityOrderingOptions
// onto the G14 DispatchOrderingOptions. AccountCandidate 与
// gatewayruntimecache.OpenAIAccountSecret 是同一命名类型的别名，账户切片直接
// 透传；ModelPriority 两侧行结构同构（RequestedModel / SourceEndpointFamily /
// RankByAccountID），逐字段拷贝。
func chainDispatchOrderingOptionsOf(options gatewaydispatch.AffinityOrderingOptions) gatewaysession.DispatchOrderingOptions {
	converted := gatewaysession.DispatchOrderingOptions{
		GroupType:        options.GroupType,
		SchedulingPolicy: options.SchedulingPolicy,
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
func newChainSessionAffinityDispatchPort(services *sessionIdentityServices) gatewaydispatch.SessionAffinityPort {
	if services == nil || services.Affinity == nil || !services.Affinity.UsesRedisSessionAffinity() {
		return nil
	}
	return &chainSessionAffinityDispatchPort{service: services.Affinity}
}
