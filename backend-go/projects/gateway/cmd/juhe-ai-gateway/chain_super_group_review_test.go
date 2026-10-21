// 复审修复回归（超级分组设计 §4.2/§6.5）：
//   - 迁移生成的超级分组必须 enabled=1 才能被 normal 策略绑定；
//   - 外层目标快照必须包含普通分组目标，不能把 429 写死；
//   - 未知/退役路由模式不得静默降级为 normal；
//   - 组回退候选必须展开超级分组成员。
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 首个超级分组成员配额全被拒绝时，外层快照必须继续尝试普通分组目标。
func TestSuperGroupOuterSnapshotFallsThroughToStandardTarget(t *testing.T) {
	fixture := seedSuperOuterFixture(t, "weighted",
		superOuterTarget{groupID: "grp_super_a", kind: "super", members: []superOuterMember{
			{groupID: "group_sa1", accountID: "acc_sa1"},
			{groupID: "group_sa2", accountID: "acc_sa2"},
		}},
		superOuterTarget{groupID: "group_standard_b", kind: "standard", accountID: "acc_sb"},
	)
	normal := gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{})
	record := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "key_1", SystemAccountID: fixture.systemAccount,
		RouteStrategyID: "rs_outer", RouteStrategyMode: gatewayruntimecache.RouteStrategyModeWeighted,
		SelectedGroupID: "grp_super_a", Status: "active",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "rsg_a", APIKeyID: "key_1", GroupID: "grp_super_a", Status: "active", GroupEnabled: 1, Priority: 1,
				GroupKind: "super", MemberGroupIDs: []string{"group_sa1", "group_sa2"}},
			{ID: "rsg_b", APIKeyID: "key_1", GroupID: "group_standard_b", Status: "active", GroupEnabled: 1, Priority: 2},
		},
	}
	resolver := &chainRouteResolver{cache: fixture.cache, normal: normal, quota: &stubQuotaChecker{deniedAll: true}}
	result, err := resolver.ResolveNormalGatewayModelRoute(t.Context(), gatewaypreauth.NormalRouteInput{
		Req: superReviewRouteRequest("gpt-test"), APIKeyRecord: record,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if result.Outcome != gatewaypreauth.NormalRouteOutcomeSelected {
		t.Fatalf("outcome = %s (targets=%+v), want selected via standard fallback", result.Outcome, result.TargetOutcomes)
	}
	if result.GroupID != "group_standard_b" {
		t.Fatalf("window group = %q, want group_standard_b", result.GroupID)
	}
	if len(result.TargetOutcomes) != 1 || result.TargetOutcomes[0].Classification != "capacity_exhausted" {
		t.Fatalf("target outcomes = %+v, want super target capacity_exhausted", result.TargetOutcomes)
	}
	if len(result.TargetOutcomes[0].MemberSkips) != 2 {
		t.Fatalf("member skips = %+v, want both members", result.TargetOutcomes[0].MemberSkips)
	}
}

// 全部目标都是 capacity 时终态必须是 429；全部 no_candidate 时是 503
// model_target_group_unavailable。
func TestSuperGroupOuterTerminalClassification(t *testing.T) {
	fixture := seedSuperOuterFixture(t, "weighted",
		superOuterTarget{groupID: "grp_super_a", kind: "super", members: []superOuterMember{
			{groupID: "group_sa1", accountID: "acc_sa1"},
			{groupID: "group_sa2", accountID: "acc_sa2"},
		}},
		superOuterTarget{groupID: "grp_super_b", kind: "super", members: []superOuterMember{
			{groupID: "group_sb1", accountID: "acc_sb1"},
			{groupID: "group_sb2", accountID: "acc_sb2"},
		}},
	)
	normal := gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{})
	record := &gatewayruntimecache.GatewayAPIKeyRow{
		ID: "key_1", SystemAccountID: fixture.systemAccount,
		RouteStrategyID: "rs_outer", RouteStrategyMode: gatewayruntimecache.RouteStrategyModeWeighted,
		SelectedGroupID: "grp_super_a", Status: "active",
		GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
			{ID: "rsg_a", APIKeyID: "key_1", GroupID: "grp_super_a", Status: "active", GroupEnabled: 1, Priority: 1,
				GroupKind: "super", MemberGroupIDs: []string{"group_sa1", "group_sa2"}},
			{ID: "rsg_b", APIKeyID: "key_1", GroupID: "grp_super_b", Status: "active", GroupEnabled: 1, Priority: 2,
				GroupKind: "super", MemberGroupIDs: []string{"group_sb1", "group_sb2"}},
		},
	}

	// 两个目标都是超级分组且成员配额全被拒绝 → 全 capacity → 429。普通分组
	// 目标不经解析期成员配额批查，故此处改用双超级分组绑定。
	allSuper := *record
	allSuper.GroupBindings = []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
		record.GroupBindings[0],
		{ID: "rsg_b", APIKeyID: "key_1", GroupID: "grp_super_b", Status: "active", GroupEnabled: 1, Priority: 2,
			GroupKind: "super", MemberGroupIDs: []string{"group_sb1", "group_sb2"}},
	}
	record = &allSuper
	denied := (&chainRouteResolver{cache: fixture.cache, normal: normal, quota: &stubQuotaChecker{deniedAll: true}})
	quotaResult, err := denied.ResolveNormalGatewayModelRoute(t.Context(), gatewaypreauth.NormalRouteInput{
		Req: superReviewRouteRequest("gpt-test"), APIKeyRecord: record,
	})
	if err != nil {
		t.Fatalf("resolve quota: %v", err)
	}
	if quotaResult.Outcome != gatewaypreauth.NormalRouteOutcomeFailed || quotaResult.StatusCode != 429 ||
		quotaResult.Code != "rate_limit_exceeded" {
		t.Fatalf("quota terminal = %+v, want 429 rate_limit_exceeded", quotaResult)
	}

	// 普通分组目标也没有模型能力 → 全 no_candidate。
	if _, err := fixture.db.Exec(`DELETE FROM account_supported_models WHERE account_id IN ('acc_sa1','acc_sa2','acc_sb1','acc_sb2')`); err != nil {
		t.Fatal(err)
	}
	fixture.cache = w1FreshCacheFor(t, fixture)
	noCandidate := (&chainRouteResolver{cache: fixture.cache, normal: gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{}),
		quota: &stubQuotaChecker{}})
	noCandidateResult, err := noCandidate.ResolveNormalGatewayModelRoute(t.Context(), gatewaypreauth.NormalRouteInput{
		Req: superReviewRouteRequest("gpt-test"), APIKeyRecord: record,
	})
	if err != nil {
		t.Fatalf("resolve no-candidate: %v", err)
	}
	if noCandidateResult.StatusCode != http.StatusServiceUnavailable || noCandidateResult.Code != "model_target_group_unavailable" {
		t.Fatalf("no-candidate terminal = %+v", noCandidateResult)
	}
}

// superReviewRouteRequest 构造带模型的最小链级请求。
func superReviewRouteRequest(model string) *gatewaypreauth.GatewayRequest {
	request := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	request.Body = &gatewaybody.Request{
		RawBody: []byte(`{"model":"` + model + `"}`),
		Body:    map[string]any{"model": model},
		State:   &gatewaybody.BodyState{Model: &model},
	}
	return request
}

// 未知模式（含退役 merge）在运行时装载侧必须显式失败，不得静默按 normal 服务。
func TestRuntimeCacheRejectsRetiredRouteMode(t *testing.T) {
	if gatewayruntimecache.IsSupportedRouteStrategyMode("merge") {
		t.Fatal("merge 不得判定为受支持模式")
	}
	if got := gatewayruntimecache.NormalizeRouteStrategyMode("merge"); got != "merge" {
		t.Fatalf("merge 归一 = %q，必须原样返回交由装载侧拒绝", got)
	}
}
