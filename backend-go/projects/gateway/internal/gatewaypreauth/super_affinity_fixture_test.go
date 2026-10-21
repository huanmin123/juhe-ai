// 超级分组设计 §7 affinity-super-scope（fixture:affinity-super-scope）：weighted/round_robin 绑定多个超级
// 分组时，组级会话亲和只在绑定层（route_group=S1/S2）选择目标，成员展开不
// 参与亲和、也不改变 usage 窗口组。这里用 round_robin（亲和适用范围）连续
// 20 次命中验证黏住组始终是超级分组本身。
package gatewaypreauth

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func superStickyStaticRow() gatewayruntimecache.GatewayRuntime {
	row := validRuntimeRow()
	row.RouteStrategyMode = gatewayruntimecache.RouteStrategyModeRoundRobin
	row.RouteStrategyID = "strategy_super_rr"
	row.SelectedGroupID = "grp_super_1"
	row.GroupBindings = []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		{ID: "binding_s1", APIKeyID: row.ID, GroupID: "grp_super_1", Status: "active", GroupEnabled: 1,
			GroupKind: "super", MemberGroupIDs: []string{"grp_m1", "grp_m2"}},
		{ID: "binding_s2", APIKeyID: row.ID, GroupID: "grp_super_2", Status: "active", GroupEnabled: 1,
			GroupKind: "super", MemberGroupIDs: []string{"grp_m3", "grp_m4"}},
	}
	account := gatewayruntimecache.OpenAIAccountSecret{ID: "acc_super_sticky", ProviderCode: "openai", Status: "active"}
	return gatewayruntimecache.GatewayRuntime{
		APIKey:               row,
		Settings:             gatewayruntimecache.GatewaySettings{NoAvailableAccountWaitTimeoutSeconds: 30},
		GroupAccess:          &gatewayruntimecache.GroupUsageAccessMetadata{GroupAccessType: "owner"},
		Accounts:             []gatewayruntimecache.OpenAIAccountSecret{account},
		StaticResolutionOnly: true,
	}
}

func TestSuperGroupAffinityFixture(t *testing.T) {
	affinity := &fakeGroupAffinityPort{bindings: map[string]string{"gak:conv_super": "grp_super_2"}}
	staticRow := superStickyStaticRow()
	cache := &groupStickyRuntimeCache{
		fakeRuntimeCache: fakeRuntimeCache{
			runtimeByKey: map[string]gatewayruntimecache.GatewayRuntime{"sk-good": staticRow},
			groupAccess:  &gatewayruntimecache.GroupUsageAccessMetadata{},
			accounts:     staticRow.Accounts,
		},
		staticRow: staticRow,
	}
	account := staticRow.Accounts[0]
	service, _, _ := newTestService(t, func(s *Service) {
		s.RuntimeCache = cache
		s.GroupAffinity = affinity
		s.SessionIdentity = &fixedSessionIdentity{identity: SessionIdentity{SessionID: "session_super", ConversationKey: "conv_super"}}
		s.Candidates = whSuccessfulCandidates(account)
		s.Codex = &fakeCodex{compactResult: CodexCompactPreflightResult{Accounts: []gatewayruntimecache.OpenAIAccountSecret{account}}}
	})

	for attempt := 0; attempt < 20; attempt++ {
		context, _ := groupStickyPreflight(t, service, &PreflightOptions{DispatchStaticResolution: true})
		if context.UsageContext.GroupID != "grp_super_2" {
			t.Fatalf("attempt %d window group = %q, want grp_super_2 (never a member)", attempt, context.UsageContext.GroupID)
		}
	}
	for _, input := range cache.recordedSelects() {
		switch input.PreferredGroupID {
		case "", "grp_super_1", "grp_super_2":
		default:
			t.Fatalf("sticky preferred group = %q, must stay a route binding", input.PreferredGroupID)
		}
	}
	if len(cache.recordedSelects()) != 20 {
		t.Fatalf("select calls = %d, want 20", len(cache.recordedSelects()))
	}
}
