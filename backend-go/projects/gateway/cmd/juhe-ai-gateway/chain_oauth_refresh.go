package main

// T65-①（网关上游 401 自动刷新）：gateway 进程内 OAuth 401 刷新协调器。
//
// 触发与资格（与 chain_ports.go HandleFailedUpstreamResponse 的 401 臂合取）：
// 上游响应 401 + 账户 Type ∈ {oauth, google_oauth} + 错误策略 Decide 为 nil
// （用户显式 401 规则命中时完全维持现状）+ 自动轮换同资格（非媒体域、非
// DeferAutomaticSameAccountKeyRotation，chain_ports.go automaticRotation 排除
// 条件对齐）。链面臂只把 401/decision/lane 事实交进来，本组件负责类型门、
// 请求级 once、节流、单飞与重新水合。
//
// 行为要点：
//   - per-account 单飞（golang.org/x/sync/singleflight）：同一账户并发 401 只
//     发起一次刷新，后来者等待并共享结果；单飞内闭包持有发起方 ctx（刷新是
//     账户域工作，令牌端点调用自带 25s/60s 独立超时，见 oauthmgmt transport）。
//   - 节流：成功（含 CAS 冲突视同成功）后 60s 内同一账户不再自动刷新；上游
//     换发失败（invalid_grant 等任何 AutoRefreshUpstreamError）后 5 分钟内不再
//     自动刷。节流只作用于「是否再刷」，不改变账户状态、不写终态——上游响应
//     永不终态原则不变。
//   - 重新水合：轮换提交后既有 finishRotationSideEffects 已同步清 runtime
//     cache（oauthmgmt invalidation.go → inval.Bus → ClearGatewayRuntimeCache），
//     这里按请求的 group 读回组级列表并按账户 ID 取回含新凭据的候选，交引擎
//     同账户重试一次。水合失败 / 列表缺席返回 nil，调用方落回现状 SkipAccount。
//
// 令牌值绝不落日志：日志只含 account id、vendor、revision 与错误摘要。

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oauthmgmt"
	"golang.org/x/sync/singleflight"
)

const (
	// chainOAuthRefreshMinInterval 是同一账户两次自动刷新的最小间隔：60s 内已
	// 刷过（成功或 CAS 视同成功）则跳过刷新，直接走现状 SkipAccount。
	chainOAuthRefreshMinInterval = 60 * time.Second
	// chainOAuthRefreshFailureSilence 是上游换发失败后的「不再自动刷」窗口；
	// 只关自动刷新，不影响既有 skip / key-rotation / 健康检查语义。
	chainOAuthRefreshFailureSilence = 5 * time.Minute
	// chainOAuthRefreshEntryTTL 是节流条目的保留窗：markRefreshed / markFailure
	// 顺带清扫早于 now-TTL 的陈旧键。10min 大于 5min 失败静默窗（也大于 60s
	// 最小间隔），被清扫的键对节流判定必然已无影响，只防长运行时两个 map 随
	// 出现过的账户数无界增长。
	chainOAuthRefreshEntryTTL = 10 * time.Minute
)

// chainOAuthRefreshStore 是 oauthmgmt.Store 自动刷新入口的窄口（测试以假实现
// 替换；生产装配传组合根构造的 *oauthmgmt.Store）。
type chainOAuthRefreshStore interface {
	RotateAccountTokensFromStored(ctx context.Context, accountID string) (*oauthmgmt.AutoRefreshResult, error)
}

// chainOAuthRefreshRuntimeCache 是重新水合读面的窄口：轮换失效后按组重读账户
// 候选（与引擎候选水合同一投影，AccountCandidate 即 OpenAIAccountSecret 别名）。
type chainOAuthRefreshRuntimeCache interface {
	ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error)
}

// chainOAuthRefreshCoordinator 汇聚单飞、节流与重新水合。零值不可用；生产由
// compose.go 构造并经 chainRuntimeDeps 注入失败派发器，nil 协调器 = 自动刷新
// 整体缺席（组合测试既有 SkipAccount 语义不变）。
type chainOAuthRefreshCoordinator struct {
	store chainOAuthRefreshStore
	cache chainOAuthRefreshRuntimeCache
	// Now 供测试钉住节流时钟；nil 用 time.Now。
	Now func() time.Time

	group singleflight.Group

	mu          sync.Mutex
	refreshedAt map[string]time.Time // 成功（含 already-current）时间 → 60s 最小间隔
	failedUntil map[string]time.Time // 上游换发失败 → 5 分钟不再自动刷
}

func (c *chainOAuthRefreshCoordinator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// refreshForRetry 是派发器 401 臂的唯一入口：返回重新水合的候选表示「已刷新，
// 可同账户重试一次」；返回 nil 表示不重试，调用方维持现状 SkipAccount。
func (c *chainOAuthRefreshCoordinator) refreshForRetry(ctx context.Context, input gatewaydispatch.FailedUpstreamResponseInput) *gatewaydispatch.AccountCandidate {
	if c == nil || c.store == nil || c.cache == nil {
		return nil
	}
	account := input.Account
	// 类型门：只处理 OAuth 家族；api_key 等类型 401 维持现状。
	if account.Type != "oauth" && account.Type != "google_oauth" {
		return nil
	}
	// 请求级 once：同一请求内同一账户至多触发一次刷新尝试（重试后的 401 不得
	// 再次触发刷新——与 60s min-interval 双保险）。未安装请求级状态（非链面
	// 调用方）时不阻断，由 min-interval 兜底。
	if !chainOAuthRefreshClaimAttempt(ctx, account.ID) {
		return nil
	}
	now := c.now()
	c.mu.Lock()
	if until, ok := c.failedUntil[account.ID]; ok && now.Before(until) {
		c.mu.Unlock()
		return nil
	}
	if at, ok := c.refreshedAt[account.ID]; ok && now.Sub(at) < chainOAuthRefreshMinInterval {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	// 单飞：同一账户并发 401 合并为一次刷新，后来者等待并共享同一结果。
	value, err, _ := c.group.Do(account.ID, func() (any, error) {
		return c.rotateAndRehydrate(ctx, input), nil
	})
	if err != nil {
		return nil
	}
	candidate, _ := value.(*gatewaydispatch.AccountCandidate)
	return candidate
}

// rotateAndRehydrate 执行一次刷新 + 重新水合，返回候选或 nil（nil 一律落回
// 现状 SkipAccount）。成功 / 失败各落一条结构化日志（不含任何令牌值）。
func (c *chainOAuthRefreshCoordinator) rotateAndRehydrate(ctx context.Context, input gatewaydispatch.FailedUpstreamResponseInput) *gatewaydispatch.AccountCandidate {
	account := input.Account
	result, err := c.store.RotateAccountTokensFromStored(ctx, account.ID)
	if err != nil {
		var upstream *oauthmgmt.AutoRefreshUpstreamError
		if errors.As(err, &upstream) {
			c.markFailure(account.ID)
			slog.Warn("OAuth 令牌自动刷新失败（上游换发错误）",
				"event", "gateway_oauth_token_auto_refresh_failed",
				"accountId", account.ID,
				"vendor", account.VendorCode,
				"error", err)
		} else {
			// 本地错误（代理缺失 / plan 不支持 / 存储错误）：只跳过，不进
			// 5 分钟节流——下一次请求可随配置修正立即恢复自动刷新。
			slog.Warn("OAuth 令牌自动刷新未执行（本地错误）",
				"event", "gateway_oauth_token_auto_refresh_skipped",
				"accountId", account.ID,
				"vendor", account.VendorCode,
				"error", err)
		}
		return nil
	}
	c.markRefreshed(account.ID)
	slog.Info("OAuth 令牌自动刷新完成",
		"event", "gateway_oauth_token_auto_refreshed",
		"accountId", account.ID,
		"vendor", account.VendorCode,
		"alreadyCurrent", result.AlreadyCurrent,
		"configRevision", result.ConfigRevision)
	hydrated := c.rehydrateAccount(ctx, input)
	if hydrated == nil {
		slog.Warn("OAuth 令牌自动刷新后重新水合失败，本请求按现状跳过账户",
			"event", "gateway_oauth_token_auto_refresh_rehydrate_missed",
			"accountId", account.ID,
			"vendor", account.VendorCode,
			"groupId", input.UsageContext.GroupID)
	}
	return hydrated
}

// rehydrateAccount 轮换提交后按组重读候选：runtime cache 已被轮换副作用同步
// 清空，此处读取即 DB 现读投影（新凭据）。取不到（组缺席 / 账户离组）返回 nil。
func (c *chainOAuthRefreshCoordinator) rehydrateAccount(ctx context.Context, input gatewaydispatch.FailedUpstreamResponseInput) *gatewaydispatch.AccountCandidate {
	groupID := strings.TrimSpace(input.UsageContext.GroupID)
	if groupID == "" {
		return nil
	}
	accounts, err := c.cache.ListCachedOpenAIAccountsForGroupAsync(ctx, groupID, input.UsageContext.SystemAccountID, gatewayruntimecache.CachedOpenAIAccountsForGroupOptions{})
	if err != nil {
		return nil
	}
	for index := range accounts {
		if accounts[index].ID == input.Account.ID {
			return &accounts[index]
		}
	}
	return nil
}

// sweepStaleThrottleEntries 清扫早于 cutoff 的节流键（调用方已持锁）：键值早于
// now-TTL 即陈旧，删与不删对节流判定无影响，只为容量有界。
func sweepStaleThrottleEntries(m map[string]time.Time, cutoff time.Time) {
	for id, at := range m {
		if at.Before(cutoff) {
			delete(m, id)
		}
	}
}

func (c *chainOAuthRefreshCoordinator) markRefreshed(accountID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshedAt == nil {
		c.refreshedAt = map[string]time.Time{}
	}
	now := c.now()
	c.refreshedAt[accountID] = now
	sweepStaleThrottleEntries(c.refreshedAt, now.Add(-chainOAuthRefreshEntryTTL))
}

func (c *chainOAuthRefreshCoordinator) markFailure(accountID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failedUntil == nil {
		c.failedUntil = map[string]time.Time{}
	}
	now := c.now()
	c.failedUntil[accountID] = now.Add(chainOAuthRefreshFailureSilence)
	sweepStaleThrottleEntries(c.failedUntil, now.Add(-chainOAuthRefreshEntryTTL))
}

// ---------------------------------------------------------------------------
// 请求级 once 标记（chain_v1.go 链入口安装，与 requestFastExhaustState 同位）
// ---------------------------------------------------------------------------

type chainOAuthRefreshOnceKey struct{}

// chainOAuthRefreshOnceState 是一次客户端调用的请求级「已触发刷新」标记：
// 同一请求内同一账户至多进入一次刷新路径。
type chainOAuthRefreshOnceState struct {
	mu        sync.Mutex
	attempted map[string]struct{}
}

// withChainOAuthRefreshOnceState 把请求级 once 状态安装到请求 ctx（链入口一次）。
func withChainOAuthRefreshOnceState(ctx context.Context) context.Context {
	return context.WithValue(ctx, chainOAuthRefreshOnceKey{}, &chainOAuthRefreshOnceState{attempted: map[string]struct{}{}})
}

// chainOAuthRefreshClaimAttempt 原子 check-and-mark：false = 该账户本请求已触发
// 过刷新（直接落回 SkipAccount）。状态未安装（非派发链调用方）恒 true。
func chainOAuthRefreshClaimAttempt(ctx context.Context, accountID string) bool {
	state, _ := ctx.Value(chainOAuthRefreshOnceKey{}).(*chainOAuthRefreshOnceState)
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, done := state.attempted[accountID]; done {
		return false
	}
	state.attempted[accountID] = struct{}{}
	return true
}
