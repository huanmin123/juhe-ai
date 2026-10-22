// 超级分组设计 §7 的链级 fixture：成员级配额门（dispatch-member-quota）、
// 成员行停用跳过（dispatch-member-disabled-skip）、空候选超级目标
// （dispatch-skipped-super-records-no-candidate）、usage 实际服务成员组
// （usage-member-group）与审计双身份（audit-route-and-member）。
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// superMemberGroup 描述超级分组的一个成员组及其账号。
type superMemberGroup struct {
	groupID   string
	accountID string
}

// superSeedFixture 在 chainFixture 基线上种超级分组栈：group_main（acc_1）
// 与第二个标准成员组共同组成 enabled 超级分组 grp_super，normal 策略只绑定
// 该超级分组。
func superSeedFixture(t *testing.T, second superMemberGroup) *chainFixture {
	t.Helper()
	fixture := newChainFixture(t)
	now := chainRouteTestNow
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := fixture.db.Exec(query, args...); err != nil {
			t.Fatalf("seed super fixture: %v: %v", query, err)
		}
	}
	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type) VALUES (?, ?, 'openai', 1, 'personal')`,
		second.groupID, fixture.systemAccount)
	seed(`INSERT INTO accounts (
			id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
			name, type, status, schedulable, credentials_encrypted, deleted_at
		) VALUES (?, ?, 'openai', 'prof_1', 'openai', 'v1', '超级成员账户', 'api_key', 'active', 1, ?, NULL)`,
		second.accountID, fixture.systemAccount, w1vEncrypt(t, map[string]any{"api_key": "sk-super-member-key"}))
	seed(`INSERT INTO group_accounts (group_id, system_account_id, account_id, enabled, created_at) VALUES (?, ?, ?, 1, ?)`,
		second.groupID, fixture.systemAccount, second.accountID, now)
	chainSeedAccountConnection(t, fixture.db, second.accountID, "https://super-member.example.invalid/v1")
	seed(`INSERT INTO account_supported_models (account_id, provider_code, model, model_id, created_at) VALUES (?, 'openai', 'gpt-test', 'mdl_gpt-test', ?)`,
		second.accountID, now)

	seed(`INSERT INTO groups (id, system_account_id, provider_code, enabled, group_type, group_kind) VALUES ('grp_super', ?, 'openai', 1, 'personal', 'super')`,
		fixture.systemAccount)
	seed(`INSERT INTO super_group_members (id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
		VALUES ('sgm_a', 'grp_super', ?, 0, 1, ?, ?)`, fixture.groupID, now, now)
	seed(`INSERT INTO super_group_members (id, super_group_id, member_group_id, priority, enabled, created_at, updated_at)
		VALUES ('sgm_b', 'grp_super', ?, 1, 1, ?, ?)`, second.groupID, now, now)
	seed(`INSERT INTO route_strategies (id, system_account_id, name, mode, config_json, status) VALUES ('rs_super', ?, '超级分组路由', 'normal', NULL, 'active')`,
		fixture.systemAccount)
	seed(`INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at)
		VALUES ('rsg_super', 'rs_super', ?, 'grp_super', 1, 1, 'active', ?)`, fixture.systemAccount, now)
	seed(`UPDATE api_keys SET route_strategy_id = 'rs_super' WHERE id = 'key_1'`)
	fixture.cache = w1FreshCacheFor(t, fixture)
	return fixture
}

// superSuccessUpstream 返回固定成功帧的 mock 上游。
func superSuccessUpstream(t *testing.T, content string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-super","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"` + content + `"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// superFailureUpstream 返回 500（可重试上游失败）的 mock 上游。
func superFailureUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"超级成员故障"}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSuperGroupChainFixtures(t *testing.T) {
	t.Run("usage-member-group", func(t *testing.T) {
		second := superMemberGroup{groupID: "group_compat", accountID: "acc_compat"}
		fixture := superSeedFixture(t, second)
		pointAccountAt(t, fixture, fixture.accountID, superFailureUpstream(t).URL)
		pointAccountAt(t, fixture, second.accountID, superSuccessUpstream(t, "超级成员命中").URL)

		chain, spoolDir, shutdown := composeChainWithSpool(t, fixture, nil)
		defer shutdown()
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
		if status != http.StatusOK {
			t.Fatalf("status=%d body=%s", status, body)
		}
		if !strings.Contains(body, "超级成员命中") {
			t.Fatalf("body missing member content: %s", body)
		}

		records := waitForSpoolRecordsUntil(t, spoolDir, func(snapshots []gatewayusage.UsageRecordInput) bool {
			for _, record := range snapshots {
				if record.Success && record.AccountID == second.accountID {
					return true
				}
			}
			return false
		})
		var success *gatewayusage.UsageRecordInput
		for index := range records {
			if records[index].Success && records[index].AccountID == second.accountID {
				success = &records[index]
				break
			}
		}
		if success == nil {
			t.Fatalf("no success usage record for %s: %+v", second.accountID, records)
		}
		if success.GroupID != second.groupID {
			t.Fatalf("usage group = %q, want member group %s (never the super group)", success.GroupID, second.groupID)
		}
		for _, record := range records {
			if record.GroupID == "grp_super" {
				t.Fatalf("usage must not record the super group as served group: %+v", record)
			}
		}
	})

	t.Run("audit-route-and-member", func(t *testing.T) {
		second := superMemberGroup{groupID: "group_compat", accountID: "acc_compat"}
		fixture := superSeedFixture(t, second)
		pointAccountAt(t, fixture, fixture.accountID, superFailureUpstream(t).URL)
		pointAccountAt(t, fixture, second.accountID, superSuccessUpstream(t, "审计成员命中").URL)

		dispatcher := &capturingAuditDispatcher{}
		chain, _, shutdown := composeChainWithSpool(t, fixture, func(deps *chainRuntimeDeps) {
			deps.AuditLogEnabled = func() bool { return true }
			deps.AuditUsageDispatch = dispatcher
		})
		defer shutdown()
		status, body := w1vServeV1(t, chain, fixture.apiKeySecret, `{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`, nil)
		if status != http.StatusOK {
			t.Fatalf("status=%d body=%s", status, body)
		}
		var log *gatewayusage.AuditLogInput
		for _, captured := range dispatcher.snapshot() {
			candidate := captured
			log = &candidate
		}
		if log == nil {
			t.Fatal("audit log 未派发")
		}
		// 双层身份（设计 §4.3）：route_group_id 为路由目标（超级分组），
		// group_id 为实际服务成员组。
		if log.RouteGroupID != "grp_super" {
			t.Fatalf("request-level audit RouteGroupID = %q, want grp_super", log.RouteGroupID)
		}
		if log.GroupID != second.groupID {
			t.Fatalf("request-level audit GroupID = %q, want 实际服务成员组 %s", log.GroupID, second.groupID)
		}
		attemptGroups := map[string]string{}
		attemptMembers := map[string]string{}
		for _, attempt := range log.Attempts {
			attemptGroups[attempt.AccountID] = attempt.GroupID
			attemptMembers[attempt.AccountID] = attempt.MemberGroupID
		}
		if attemptGroups[fixture.accountID] != fixture.groupID {
			t.Fatalf("attempt(%s) group = %q, want %s", fixture.accountID, attemptGroups[fixture.accountID], fixture.groupID)
		}
		if attemptGroups[second.accountID] != second.groupID {
			t.Fatalf("attempt(%s) group = %q, want %s", second.accountID, attemptGroups[second.accountID], second.groupID)
		}
		if attemptMembers[second.accountID] != second.groupID {
			t.Fatalf("attempt(%s) member_group_id = %q, want %s", second.accountID, attemptMembers[second.accountID], second.groupID)
		}
	})

	t.Run("outer-429-rate-limit", func(t *testing.T) {
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
				{ID: "rsg_a", APIKeyID: "key_1", GroupID: "grp_super_a", Status: "active", GroupEnabled: 1, Priority: 1, GroupKind: "super",
					MemberGroupIDs: []string{"group_sa1", "group_sa2"}},
				{ID: "rsg_b", APIKeyID: "key_1", GroupID: "grp_super_b", Status: "active", GroupEnabled: 1, Priority: 2, GroupKind: "super",
					MemberGroupIDs: []string{"group_sb1", "group_sb2"}},
			},
		}
		quota := &stubQuotaChecker{deniedAll: true}
		resolver := &chainRouteResolver{cache: fixture.cache, normal: normal, quota: quota}
		result, err := resolver.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
			Req: routeRequest("gpt-test"), APIKeyRecord: record,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if result.Outcome != gatewaypreauth.NormalRouteOutcomeFailed || result.StatusCode != 429 ||
			result.Code != "rate_limit_exceeded" {
			t.Fatalf("outer 429 = %+v", result)
		}
		if quota.calls < 2 {
			t.Fatalf("quota checks = %d, want both super targets attempted", quota.calls)
		}
		if len(result.TargetOutcomes) != 2 {
			t.Fatalf("target outcomes = %+v, want both targets", result.TargetOutcomes)
		}
		for index, groupID := range []string{"grp_super_a", "grp_super_b"} {
			if result.TargetOutcomes[index].GroupID != groupID || result.TargetOutcomes[index].Classification != "capacity_exhausted" {
				t.Fatalf("outcome[%d] = %+v, want %s/capacity_exhausted", index, result.TargetOutcomes[index], groupID)
			}
		}
	})

	t.Run("dispatch-member-quota", func(t *testing.T) {
		second := superMemberGroup{groupID: "group_compat", accountID: "acc_compat"}
		fixture := superSeedFixture(t, second)
		normal := gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{})
		record := &gatewayruntimecache.GatewayAPIKeyRow{
			ID: "key_1", SystemAccountID: fixture.systemAccount,
			RouteStrategyID: "rs_super", RouteStrategyMode: gatewayruntimecache.RouteStrategyModeNormal,
			SelectedGroupID: "grp_super", Status: "active",
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{
				ID: "rsg_super", APIKeyID: "key_1", GroupID: "grp_super", Status: "active",
				GroupEnabled: 1, Priority: 1, GroupKind: "super",
				MemberGroupIDs: []string{fixture.groupID, second.groupID},
			}},
		}
		resolver := &chainRouteResolver{cache: fixture.cache, normal: normal,
			quota: &stubQuotaChecker{denied: map[string]bool{fixture.accountID: true}}}
		result, err := resolver.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
			Req: routeRequest("gpt-test"), APIKeyRecord: record,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if result.Outcome != gatewaypreauth.NormalRouteOutcomeSelected {
			t.Fatalf("outcome = %s", result.Outcome)
		}
		if result.GroupID != "grp_super" {
			t.Fatalf("window group = %q, want grp_super", result.GroupID)
		}
		if len(result.Accounts) != 1 || result.Accounts[0].ID != second.accountID {
			t.Fatalf("accounts = %v, want [%s]", accountSecretIDs(result.Accounts), second.accountID)
		}
		if bound := result.Accounts[0].BoundGroupID; bound == nil || *bound != second.groupID {
			t.Fatalf("survivor stamp = %v, want %s", bound, second.groupID)
		}
		// 设计 §4.2：成功路径也要留证被配额剔除的成员。
		if len(result.MemberSkips) != 1 || result.MemberSkips[0].GroupID != fixture.groupID ||
			result.MemberSkips[0].Reason != "capacity_exhausted" {
			t.Fatalf("member skips = %+v, want [%s/capacity_exhausted]", result.MemberSkips, fixture.groupID)
		}
		var status string
		var schedulable int
		if err := fixture.db.QueryRow(`SELECT status, schedulable FROM accounts WHERE id = ?`, fixture.accountID).Scan(&status, &schedulable); err != nil {
			t.Fatal(err)
		}
		if status != "active" || schedulable != 1 {
			t.Fatalf("quota denial must not disable member account: %s/%d", status, schedulable)
		}

		deniedAll := &chainRouteResolver{cache: fixture.cache, normal: normal,
			quota: &stubQuotaChecker{deniedAll: true}}
		denied, err := deniedAll.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
			Req: routeRequest("gpt-test"), APIKeyRecord: record,
		})
		if err != nil {
			t.Fatalf("resolve denied: %v", err)
		}
		if denied.Outcome != gatewaypreauth.NormalRouteOutcomeFailed || denied.StatusCode != 429 ||
			denied.Code != "rate_limit_exceeded" {
			t.Fatalf("all members denied = %+v, want 429 rate_limit_exceeded", denied)
		}
		if len(denied.TargetOutcomes) != 1 || len(denied.TargetOutcomes[0].MemberSkips) != 2 {
			t.Fatalf("denied outcomes = %+v, want both member skips", denied.TargetOutcomes)
		}
		for _, skip := range denied.TargetOutcomes[0].MemberSkips {
			if skip.Reason != "capacity_exhausted" {
				t.Fatalf("denied member skip = %+v, want capacity_exhausted", skip)
			}
		}
	})

	t.Run("dispatch-member-disabled-skip", func(t *testing.T) {
		second := superMemberGroup{groupID: "group_compat", accountID: "acc_compat"}
		fixture := superSeedFixture(t, second)
		normal := gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{})
		record := &gatewayruntimecache.GatewayAPIKeyRow{
			ID: "key_1", SystemAccountID: fixture.systemAccount,
			RouteStrategyID: "rs_super", RouteStrategyMode: gatewayruntimecache.RouteStrategyModeNormal,
			SelectedGroupID: "grp_super", Status: "active",
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{{
				ID: "rsg_super", APIKeyID: "key_1", GroupID: "grp_super", Status: "active",
				GroupEnabled: 1, Priority: 1, GroupKind: "super",
				MemberGroupIDs:         []string{second.groupID},
				DisabledMemberGroupIDs: []string{fixture.groupID},
			}},
		}
		resolver := &chainRouteResolver{cache: fixture.cache, normal: normal}
		result, err := resolver.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
			Req: routeRequest("gpt-test"), APIKeyRecord: record,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if result.Outcome != gatewaypreauth.NormalRouteOutcomeSelected || result.GroupID != "grp_super" {
			t.Fatalf("outcome = %+v", result)
		}
		found := false
		for _, skip := range result.MemberSkips {
			if skip.GroupID == fixture.groupID && skip.Reason == "member_disabled" {
				found = true
			}
		}
		if !found {
			t.Fatalf("member skips = %+v, want %s/member_disabled", result.MemberSkips, fixture.groupID)
		}
	})

	t.Run("dispatch-skipped-super-records-no-candidate", func(t *testing.T) {
		// S 的成员全部无模型能力 → 目标选择层直接跳到普通分组 G2；
		// S 必须以 no_candidate 进入目标级审计（设计 §4.2）。
		fixture := seedSuperOuterFixture(t, "failover",
			superOuterTarget{groupID: "grp_super_a", kind: "super", members: []superOuterMember{
				{groupID: "group_sa1", accountID: "acc_sa1"},
				{groupID: "group_sa2", accountID: "acc_sa2"},
			}},
			superOuterTarget{groupID: "group_standard_b", kind: "standard", accountID: "acc_sb"},
		)
		if _, err := fixture.db.Exec(`DELETE FROM account_supported_models WHERE account_id IN ('acc_sa1','acc_sa2')`); err != nil {
			t.Fatal(err)
		}
		fixture.cache = w1FreshCacheFor(t, fixture)
		normal := gatewayrouting.NewNormalModelRouteService(chainRoutingCache{cache: fixture.cache}, chainCapabilityFilter{})
		record := &gatewayruntimecache.GatewayAPIKeyRow{
			ID: "key_1", SystemAccountID: fixture.systemAccount,
			RouteStrategyID: "rs_outer", RouteStrategyMode: gatewayruntimecache.RouteStrategyModeFailover,
			SelectedGroupID: "grp_super_a", Status: "active",
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
				{ID: "rsg_a", APIKeyID: "key_1", GroupID: "grp_super_a", Status: "active", GroupEnabled: 1, Priority: 1,
					GroupKind: "super", MemberGroupIDs: []string{"group_sa1", "group_sa2"}},
				{ID: "rsg_b", APIKeyID: "key_1", GroupID: "group_standard_b", Status: "active", GroupEnabled: 1, Priority: 2},
			},
		}
		resolver := &chainRouteResolver{cache: fixture.cache, normal: normal}
		result, err := resolver.ResolveNormalGatewayModelRoute(context.Background(), gatewaypreauth.NormalRouteInput{
			Req: routeRequest("gpt-test"), APIKeyRecord: record,
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if result.Outcome != gatewaypreauth.NormalRouteOutcomeSelected || result.GroupID != "group_standard_b" {
			t.Fatalf("outcome = %+v, want standard B selected", result)
		}
		if len(result.TargetOutcomes) != 1 || result.TargetOutcomes[0].GroupID != "grp_super_a" ||
			result.TargetOutcomes[0].Classification != "no_candidate" {
			t.Fatalf("target outcomes = %+v, want grp_super_a/no_candidate", result.TargetOutcomes)
		}
	})
}

// TestOrderSuperGroupAccountsByPool 钉住全局 route_rank 序：解析池中后成员
// 的 direct 命中必须排在前成员的 mapping 命中之前（设计 §4.2）。
func TestOrderSuperGroupAccountsByPool(t *testing.T) {
	accounts := []gatewayruntimecache.OpenAIAccountSecret{
		{ID: "acc_a_direct"}, {ID: "acc_a_mapped"}, {ID: "acc_b_direct"},
	}
	pool := []gatewayrouting.UpstreamAccount{
		{ID: "acc_a_direct"}, {ID: "acc_b_direct"}, {ID: "acc_a_mapped"},
	}
	ordered := orderSuperGroupAccountsByPool(accounts, pool)
	if got := accountSecretIDs(ordered); len(got) != 3 ||
		got[0] != "acc_a_direct" || got[1] != "acc_b_direct" || got[2] != "acc_a_mapped" {
		t.Fatalf("ordered = %v", got)
	}
}
