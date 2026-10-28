package main

// T65-①（网关上游 401 自动刷新）派发器 401 臂 + 协调器钉面：
//   - 刷新成功 → retry_refreshed_same_account + 重新水合候选，token 端点恰一次；
//   - 风暴（同请求 once / 跨请求 60s min-interval）→ 第二次不触发刷新；
//   - 用户显式 401 规则命中 → 无刷新调用，行为与现状一致；
//   - 上游换发失败 → SkipAccount + 5 分钟不再刷，账户状态不写；
//   - 非 OAuth（api_key）401 → 无刷新调用（现状不变）；
//   - CAS 冲突（AlreadyCurrent）→ 视同已刷新进入重试。

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/oauthmgmt"
)

// fakeRefreshStore 记录刷新调用次数，按脚本返回结果 / 错误。
type fakeRefreshStore struct {
	calls  atomic.Int64
	result *oauthmgmt.AutoRefreshResult
	err    error
}

func (f *fakeRefreshStore) RotateAccountTokensFromStored(ctx context.Context, accountID string) (*oauthmgmt.AutoRefreshResult, error) {
	f.calls.Add(1)
	return f.result, f.err
}

// fakeRefreshCache 重新水合假实现：返回带新 access_token 的候选。
type fakeRefreshCache struct {
	calls atomic.Int64
	token string
}

func (f *fakeRefreshCache) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	f.calls.Add(1)
	return []gatewayruntimecache.OpenAIAccountSecret{{
		ID:          "acc_1",
		Name:        "账户一",
		Type:        "oauth",
		VendorCode:  "gpt",
		BaseURL:     "https://upstream.example",
		Credentials: map[string]any{"access_token": f.token, "refresh_token": "rotated"},
	}}, nil
}

// newRefreshCoordinator 组装带脚本化 store/cache 与固定时钟的协调器。
func newRefreshCoordinator(store *fakeRefreshStore, cache *fakeRefreshCache, now *atomic.Int64) *chainOAuthRefreshCoordinator {
	return &chainOAuthRefreshCoordinator{
		store: store,
		cache: cache,
		Now:   func() time.Time { return time.UnixMilli(now.Load()) },
	}
}

// oauth401Input 构造 OAuth 账户上游 401 的失败派发输入。
func oauth401Input(t *testing.T, account gatewaydispatch.AccountCandidate) gatewaydispatch.FailedUpstreamResponseInput {
	t.Helper()
	response := failureDispatchUpstreamResponse(t, http.StatusUnauthorized, "application/json",
		`{"error":{"message":"invalid token","type":"authentication"}}`)
	sink := &failureDispatchAuditSink{}
	input := gatewayFailedResponseInput(response, sink, "gateway")
	input.AuditCapture.Context = sink
	input.Account = account
	input.UsageContext.SystemAccountID = "sys_1"
	input.UsageContext.GroupID = "grp_1"
	input.Settings = gatewayruntimecache.GatewaySettings{DefaultTemporaryUnschedulableMinutes: 30}
	return input
}

func oauthAccount(credentials map[string]any) gatewaydispatch.AccountCandidate {
	return gatewaydispatch.AccountCandidate{
		ID: "acc_1", Name: "账户一",
		Type: "oauth", VendorCode: "gpt",
		ProtocolCode: "openai", ProtocolVersion: "v1",
		Credentials: credentials,
	}
}

func refreshSuccessResult() *oauthmgmt.AutoRefreshResult {
	return &oauthmgmt.AutoRefreshResult{Rotated: true, ConfigRevision: 7, UpdatedAt: "2026-10-28T00:00:00Z"}
}

// TestChainOAuthRefreshSuccessRetriesWithRehydratedCandidate：401 + OAuth +
// 无用户规则 → 单飞刷新一次（token 端点恰一次）+ 组级重读水合 → 引擎重试动作。
func TestChainOAuthRefreshSuccessRetriesWithRehydratedCandidate(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshCache{token: "new-access-token"}
	coordinator := newRefreshCoordinator(store, cache, &now)
	dispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: coordinator}

	ctx := withChainOAuthRefreshOnceState(context.Background())
	result, err := dispatcher.HandleFailedUpstreamResponse(ctx, oauth401Input(t, oauthAccount(map[string]any{"access_token": "old"})))
	if err != nil {
		t.Fatalf("handle 401: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionRetryRefreshedSameAccount {
		t.Fatalf("action = %s, want retry_refreshed_same_account", result.Action)
	}
	if result.RefreshedAccount == nil || result.RefreshedAccount.ID != "acc_1" {
		t.Fatalf("refreshed account missing: %+v", result.RefreshedAccount)
	}
	if token, _ := result.RefreshedAccount.Credentials["access_token"].(string); token != "new-access-token" {
		t.Fatalf("rehydrated token = %v, want new-access-token（重新水合候选）", result.RefreshedAccount.Credentials["access_token"])
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want exactly 1", got)
	}
	if got := cache.calls.Load(); got != 1 {
		t.Fatalf("rehydrate calls = %d, want 1", got)
	}
}

// TestChainOAuthRefreshStormHitsSingleRefresh：同一请求内连续两次 401（once）
// 与跨请求 60s 内的第二次 401（min-interval）都不触发第二次刷新。
func TestChainOAuthRefreshStormHitsSingleRefresh(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshCache{token: "new-access-token"}
	coordinator := newRefreshCoordinator(store, cache, &now)
	dispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: coordinator}
	account := oauthAccount(map[string]any{"access_token": "old"})

	// 同一请求（共享 once 状态）连续两次 401。
	requestCtx := withChainOAuthRefreshOnceState(context.Background())
	first, err := dispatcher.HandleFailedUpstreamResponse(requestCtx, oauth401Input(t, account))
	if err != nil || first.Action != gatewaydispatch.FailedResponseActionRetryRefreshedSameAccount {
		t.Fatalf("first 401: action=%s err=%v", first.Action, err)
	}
	second, err := dispatcher.HandleFailedUpstreamResponse(requestCtx, oauth401Input(t, account))
	if err != nil {
		t.Fatalf("second 401: %v", err)
	}
	if second.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("same-request retry-401 action = %s, want skip_account（once）", second.Action)
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("token endpoint calls after same-request storm = %d, want 1", got)
	}

	// 跨请求：新 once 状态，但时钟仍在 60s min-interval 内。
	other, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account))
	if err != nil {
		t.Fatalf("cross-request 401: %v", err)
	}
	if other.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("cross-request action = %s, want skip_account（min-interval）", other.Action)
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("token endpoint calls after min-interval storm = %d, want 1", got)
	}

	// 60s 窗口过后恢复刷新能力。
	now.Store(now.Load() + (61 * int64(time.Second) / int64(time.Millisecond)))
	third, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account))
	if err != nil || third.Action != gatewaydispatch.FailedResponseActionRetryRefreshedSameAccount {
		t.Fatalf("post-window 401: action=%s err=%v", third.Action, err)
	}
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("token endpoint calls after window = %d, want 2", got)
	}
}

// TestChainOAuthRefreshUserRuleKeepsCurrentBehavior：账户配 401 显式规则 →
// 决策非 nil → 无刷新调用，动作与现状（skip_account + explicit_policy）一致。
func TestChainOAuthRefreshUserRuleKeepsCurrentBehavior(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshCache{token: "new-access-token"}
	coordinator := newRefreshCoordinator(store, cache, &now)
	dispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: coordinator}

	account := oauthAccount(map[string]any{
		"access_token": "old",
		"error_handling_rules": []any{map[string]any{
			"enabled": true, "name": "401冷却", "priority": float64(1), "action": "temp_unschedulable",
			"status_codes": []any{float64(401)},
		}},
	})
	result, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account))
	if err != nil {
		t.Fatalf("handle 401 with user rule: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action = %s, want skip_account（现状不变）", result.Action)
	}
	if result.FailureKind != gatewaydispatch.FailureKindExplicitPolicy {
		t.Fatalf("failureKind = %s, want explicit_policy", result.FailureKind)
	}
	if got := store.calls.Load(); got != 0 {
		t.Fatalf("token endpoint calls = %d, want 0（用户规则优先，不让位自动刷新）", got)
	}
}

// TestChainOAuthRefreshUpstreamFailureSilencesFiveMinutes：上游换发错误 →
// SkipAccount、5 分钟内不再刷；本地错误只跳过、不进 5 分钟节流。
func TestChainOAuthRefreshUpstreamFailureSilencesFiveMinutes(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{err: &oauthmgmt.AutoRefreshUpstreamError{Err: errors.New("OpenAI OAuth 令牌请求失败：HTTP 400，invalid_grant")}}
	cache := &fakeRefreshCache{token: "new-access-token"}
	coordinator := newRefreshCoordinator(store, cache, &now)
	dispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: coordinator}
	account := oauthAccount(map[string]any{"access_token": "old"})

	first, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account))
	if err != nil {
		t.Fatalf("first failed refresh: %v", err)
	}
	if first.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("failed-refresh action = %s, want skip_account（现状行为）", first.Action)
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1", got)
	}
	// 5 分钟内：不再发起刷新。
	silenced, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account))
	if err != nil {
		t.Fatalf("silenced 401: %v", err)
	}
	if silenced.Action != gatewaydispatch.FailedResponseActionSkipAccount || store.calls.Load() != 1 {
		t.Fatalf("silenced window must not refresh: action=%s calls=%d", silenced.Action, store.calls.Load())
	}
	// 5 分钟窗口过后恢复。
	now.Store(now.Load() + (6 * int64(time.Minute) / int64(time.Millisecond)))
	recovered, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account))
	if err != nil {
		t.Fatalf("post-silence 401: %v", err)
	}
	if recovered.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("post-silence action = %s, want skip_account（刷新仍失败）", recovered.Action)
	}
	if got := store.calls.Load(); got != 2 {
		t.Fatalf("token endpoint calls after silence = %d, want 2", got)
	}

	// 本地错误（代理缺失等 ValidationError）：只跳过，不记 5 分钟节流。
	localStore := &fakeRefreshStore{err: &oauthmgmt.ValidationError{Message: "未绑定代理"}}
	localDispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: newRefreshCoordinator(localStore, cache, &now)}
	if _, err := localDispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account)); err != nil {
		t.Fatalf("local failure: %v", err)
	}
	if _, err := localDispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, account)); err != nil {
		t.Fatalf("local failure second request: %v", err)
	}
	if got := localStore.calls.Load(); got != 2 {
		t.Fatalf("local-error refresh calls = %d, want 2（本地错误不进 5 分钟节流）", got)
	}
}

// TestChainOAuthRefreshNonOAuthAndCASArms：api_key 401 无刷新调用（现状不变）；
// CAS 冲突（AlreadyCurrent）视同已刷新进入重试。
func TestChainOAuthRefreshNonOAuthAndCASArms(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshCache{token: "new-access-token"}
	coordinator := newRefreshCoordinator(store, cache, &now)
	dispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: coordinator}

	// api_key 401：类型门直接拦下，零刷新调用。
	apiKeyAccount := oauthAccount(map[string]any{"api_key": "sk-..."})
	apiKeyAccount.Type = "api_key"
	result, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, apiKeyAccount))
	if err != nil {
		t.Fatalf("api_key 401: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("api_key action = %s, want skip_account（现状）", result.Action)
	}
	if got := store.calls.Load(); got != 0 {
		t.Fatalf("api_key token endpoint calls = %d, want 0", got)
	}

	// CAS 冲突臂：store 返回 AlreadyCurrent → 视同已刷新，水合后进入重试。
	casStore := &fakeRefreshStore{result: &oauthmgmt.AutoRefreshResult{AlreadyCurrent: true, ConfigRevision: 8}}
	casDispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: newRefreshCoordinator(casStore, cache, &now)}
	casResult, err := casDispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), oauth401Input(t, oauthAccount(map[string]any{"access_token": "old"})))
	if err != nil {
		t.Fatalf("cas 401: %v", err)
	}
	if casResult.Action != gatewaydispatch.FailedResponseActionRetryRefreshedSameAccount {
		t.Fatalf("cas action = %s, want retry_refreshed_same_account（视同已刷新）", casResult.Action)
	}
	if casResult.RefreshedAccount == nil || casResult.RefreshedAccount.ID != "acc_1" {
		t.Fatalf("cas refreshed account missing: %+v", casResult.RefreshedAccount)
	}
}

// TestChainOAuthRefreshCoordinatorNilKeepsCurrentBehavior：协调器缺席（组合
// 测试装配）时 401 维持现状 SkipAccount，不 panic。
func TestChainOAuthRefreshCoordinatorNilKeepsCurrentBehavior(t *testing.T) {
	dispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{})}
	result, err := dispatcher.HandleFailedUpstreamResponse(context.Background(), oauth401Input(t, oauthAccount(map[string]any{"access_token": "old"})))
	if err != nil {
		t.Fatalf("nil coordinator 401: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionSkipAccount {
		t.Fatalf("action = %s, want skip_account", result.Action)
	}
}

// TestChainOAuthRefreshThrottleMapSweepsStaleEntries：markRefreshed /
// markFailure 顺带清扫早于 now-10min 的陈旧键，活跃键保留（注入 Now 时钟）。
func TestChainOAuthRefreshThrottleMapSweepsStaleEntries(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	coordinator := newRefreshCoordinator(&fakeRefreshStore{result: refreshSuccessResult()}, &fakeRefreshCache{token: "new-access-token"}, &now)

	current := time.UnixMilli(now.Load())
	stale := current.Add(-11 * time.Minute) // 早于 now-10min
	fresh := current.Add(-1 * time.Minute)  // 晚于 now-10min
	coordinator.mu.Lock()
	coordinator.refreshedAt = map[string]time.Time{"acc_old": stale, "acc_keep": fresh}
	coordinator.failedUntil = map[string]time.Time{"acc_fail_old": stale, "acc_fail_keep": current.Add(time.Minute)}
	coordinator.mu.Unlock()

	coordinator.markRefreshed("acc_new")
	coordinator.markFailure("acc_fail_new")

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if _, exists := coordinator.refreshedAt["acc_old"]; exists {
		t.Fatal("refreshedAt 陈旧键必须被清扫")
	}
	if _, exists := coordinator.failedUntil["acc_fail_old"]; exists {
		t.Fatal("failedUntil 陈旧键必须被清扫")
	}
	for _, id := range []string{"acc_keep", "acc_new"} {
		if _, exists := coordinator.refreshedAt[id]; !exists {
			t.Fatalf("refreshedAt 活跃键 %s 必须保留", id)
		}
	}
	for _, id := range []string{"acc_fail_keep", "acc_fail_new"} {
		if _, exists := coordinator.failedUntil[id]; !exists {
			t.Fatalf("failedUntil 活跃键 %s 必须保留", id)
		}
	}
}

// 编译期守卫：生产 concrete 类型满足协调器窄口。
var (
	_ chainOAuthRefreshStore        = (*oauthmgmt.Store)(nil)
	_ chainOAuthRefreshRuntimeCache = (*gatewayruntimecache.Service)(nil)
)
