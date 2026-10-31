package main

// T65-① 超级分组路由修订钉面（DD-E1 深挖复审修复批）：路由绑定超级分组时，
// 失败臂 UsageContext.GroupID 是窗口组（超级分组本身），而 OAuth 账户绑定行
// 落在成员分组（routing 层组标 BoundGroupID）——按窗口组重水合恒 miss，同账
// 户新凭据重试一次（T65-① 契约）对超级组流量恒不发生。钉面断言：
//   - 超级分组场景：重水合按账户自带 BoundGroupID（成员分组）直读，命中后
//     引擎动作 retry_refreshed_same_account，候选组标补齐为成员分组（usage
//     组作用域与初次尝试同口径），共享请求级上下文不被改写；
//   - 普通分组场景：候选无组标（自有账户）按窗口组读取，返回候选不被触碰
//     （行为逐字节不变）。

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// fakeRefreshGroupCache 记录每次重水合读取的 (groupID, systemAccountID)，并按
// servedGroups 模拟真实绑定行窗口语义：只有账户实际绑定的组能返回候选，其余
// 组（含超级分组 id）查 group_accounts 恒空。
type fakeRefreshGroupCache struct {
	calls        atomic.Int64
	servedGroups map[string]bool
	seenGroupIDs []string
}

func (f *fakeRefreshGroupCache) ListCachedOpenAIAccountsForGroupAsync(ctx context.Context, groupID, systemAccountID string, options gatewayruntimecache.CachedOpenAIAccountsForGroupOptions) ([]gatewayruntimecache.OpenAIAccountSecret, error) {
	f.calls.Add(1)
	f.seenGroupIDs = append(f.seenGroupIDs, groupID)
	if !f.servedGroups[groupID] {
		return []gatewayruntimecache.OpenAIAccountSecret{}, nil
	}
	return []gatewayruntimecache.OpenAIAccountSecret{{
		ID:          "acc_1",
		Name:        "账户一",
		Type:        "oauth",
		VendorCode:  "gpt",
		BaseURL:     "https://upstream.example",
		Credentials: map[string]any{"access_token": "new-access-token", "refresh_token": "rotated"},
	}}, nil
}

// superGroup401Input 在 oauth401Input 基础上把窗口组置为超级分组本身，账户带
// routing 层成员分组组标（超级分组设计 §4.2：窗口组保留超级分组，账号组标为
// 实际成员分组）。
func superGroup401Input(t *testing.T, account gatewaydispatch.AccountCandidate, windowGroupID, boundGroupID string) gatewaydispatch.FailedUpstreamResponseInput {
	t.Helper()
	input := oauth401Input(t, account)
	input.UsageContext.GroupID = windowGroupID
	account.BoundGroupID = &boundGroupID
	input.Account = account
	return input
}

// TestChainOAuthRefreshSuperGroupRouteRehydratesViaBoundGroup：路由目标为超级
// 分组时 401 → 单飞刷新成功 → 重水合按账户 BoundGroupID（成员分组）读取（不
// 查超级分组 id）→ 引擎以同账户新凭据重试一次；候选组标补齐成员分组，共享
// 请求级 usage 上下文保持窗口组不变。
func TestChainOAuthRefreshSuperGroupRouteRehydratesViaBoundGroup(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshGroupCache{servedGroups: map[string]bool{"grp_member": true}}
	coordinator := &chainOAuthRefreshCoordinator{store: store, cache: cache, Now: func() time.Time { return time.UnixMilli(now.Load()) }}
	dispatcher := &chainFailureDispatcher{policy: newChainErrorPolicyService(chainErrorPolicyDeps{}), oauthRefresh: coordinator}

	input := superGroup401Input(t, oauthAccount(map[string]any{"access_token": "old"}), "grp_super", "grp_member")
	result, err := dispatcher.HandleFailedUpstreamResponse(withChainOAuthRefreshOnceState(context.Background()), input)
	if err != nil {
		t.Fatalf("handle 401 under super group route: %v", err)
	}
	if result.Action != gatewaydispatch.FailedResponseActionRetryRefreshedSameAccount {
		t.Fatalf("action = %s, want retry_refreshed_same_account（同账户新凭据重试一次）", result.Action)
	}
	if got := cache.calls.Load(); got != 1 {
		t.Fatalf("rehydrate calls = %d, want 1", got)
	}
	if len(cache.seenGroupIDs) != 1 || cache.seenGroupIDs[0] != "grp_member" {
		t.Fatalf("rehydrate group reads = %v, want [grp_member]（按成员分组直读，不查超级分组 id）", cache.seenGroupIDs)
	}
	if result.RefreshedAccount == nil || result.RefreshedAccount.ID != "acc_1" {
		t.Fatalf("refreshed account missing: %+v", result.RefreshedAccount)
	}
	if token, _ := result.RefreshedAccount.Credentials["access_token"].(string); token != "new-access-token" {
		t.Fatalf("rehydrated token = %v, want new-access-token", result.RefreshedAccount.Credentials["access_token"])
	}
	if result.RefreshedAccount.BoundGroupID == nil || *result.RefreshedAccount.BoundGroupID != "grp_member" {
		bound := "nil"
		if result.RefreshedAccount.BoundGroupID != nil {
			bound = *result.RefreshedAccount.BoundGroupID
		}
		t.Fatalf("rehydrated BoundGroupID = %s, want grp_member（重试候选组标与初次尝试同口径）", bound)
	}
	if input.UsageContext.GroupID != "grp_super" {
		t.Fatalf("request-level usage context mutated: %s, want grp_super（不改写共享上下文）", input.UsageContext.GroupID)
	}
}

// TestChainOAuthRefreshRehydrateOrdinaryGroupUnchanged：普通分组路由（候选无
// 组标，自有账户）重水合仍按窗口组读取，返回候选不被触碰（BoundGroupID 保持
// loader 原样）——既有路径行为不变。
func TestChainOAuthRefreshRehydrateOrdinaryGroupUnchanged(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC).UnixMilli())
	store := &fakeRefreshStore{result: refreshSuccessResult()}
	cache := &fakeRefreshGroupCache{servedGroups: map[string]bool{"grp_1": true}}
	coordinator := &chainOAuthRefreshCoordinator{store: store, cache: cache, Now: func() time.Time { return time.UnixMilli(now.Load()) }}

	input := oauth401Input(t, oauthAccount(map[string]any{"access_token": "old"}))
	hydrated := coordinator.refreshForRetry(withChainOAuthRefreshOnceState(context.Background()), input)
	if hydrated == nil {
		t.Fatal("rehydrate miss under ordinary group route")
	}
	if len(cache.seenGroupIDs) != 1 || cache.seenGroupIDs[0] != "grp_1" {
		t.Fatalf("rehydrate group reads = %v, want [grp_1]（按窗口组读取，参数不变）", cache.seenGroupIDs)
	}
	if hydrated.BoundGroupID != nil {
		t.Fatalf("ordinary-route candidate must stay untouched, BoundGroupID = %s", *hydrated.BoundGroupID)
	}
}
