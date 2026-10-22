package gatewayusage

// 超级分组双层身份（设计 §4.3）：请求级审计 route_group_id 恒为路由绑定目标
// （超级分组），group_id 为实际服务成员组；尝试级 member_group_id 同步成员组。

import (
	"testing"
)

// superGroupIdentityCapture 构造一个启用审计的捕获上下文。
func superGroupIdentityCapture(t *testing.T, dispatcher *recordingAuditDispatcher) *AuditCaptureContext {
	t.Helper()
	capture := w13dCapture(dispatcher, func(input *AuditCaptureInput) {
		input.Settings = FixedAuditLogSettingsSource{Settings: auditSettings(true, 0)}
	})
	capture.BindContext(AuditGatewayContext{
		SystemAccountID: "sys",
		APIKeyID:        "key",
		GroupID:         "grp_super",
		RouteGroupID:    "grp_super",
	})
	return capture
}

func TestSuperGroupRequestAuditKeepsRouteAndServedGroup(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := superGroupIdentityCapture(t, dispatcher)

	// 两个成员组：第一个失败，第二个成功——请求级 group_id 必须取成功成员组。
	failedID := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream-a", Method: "POST",
		GroupIDOverride: "grp_member_a",
	})
	capture.CompleteAttempt(failedID, CompleteAttemptInput{
		StatusCode: intPointer(500), Success: false,
		ErrorPhase: "upstream_response", ErrorMessage: "boom",
	})
	successID := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 1,
		UpstreamURL: "https://upstream-b", Method: "POST",
		GroupIDOverride: "grp_member_b",
	})
	capture.CompleteAttempt(successID, CompleteAttemptInput{
		StatusCode: intPointer(200), Success: true,
	})
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeSuccess, Success: true, StatusCode: intPointer(200)})

	log := dispatcher.all()
	if len(log) != 1 {
		t.Fatalf("audit log count = %d, want 1", len(log))
	}
	if log[0].RouteGroupID != "grp_super" {
		t.Fatalf("route_group_id = %q, want grp_super", log[0].RouteGroupID)
	}
	if log[0].GroupID != "grp_member_b" {
		t.Fatalf("group_id = %q, want grp_member_b（实际服务成员组）", log[0].GroupID)
	}
	for _, attempt := range log[0].Attempts {
		want := "grp_member_a"
		if attempt.AttemptIndex == 1 {
			want = "grp_member_b"
		}
		if attempt.GroupID != want {
			t.Fatalf("attempt %d group_id = %q, want %q", attempt.AttemptIndex, attempt.GroupID, want)
		}
		if attempt.MemberGroupID != want {
			t.Fatalf("attempt %d member_group_id = %q, want %q", attempt.AttemptIndex, attempt.MemberGroupID, want)
		}
	}
}

// 无成功尝试时（全失败）请求级 group_id 回落请求级绑定组，route_group_id 仍为
// 路由目标。
func TestSuperGroupRequestAuditFallsBackWithoutServedAttempt(t *testing.T) {
	dispatcher := &recordingAuditDispatcher{}
	capture := superGroupIdentityCapture(t, dispatcher)

	attemptID := capture.StartAttempt(StartAttemptInput{
		Account: testAccount(), AttemptIndex: 0,
		UpstreamURL: "https://upstream-a", Method: "POST",
		GroupIDOverride: "grp_member_a",
	})
	capture.CompleteAttempt(attemptID, CompleteAttemptInput{
		StatusCode: intPointer(502), Success: false,
		ErrorPhase: "upstream_response", ErrorMessage: "boom",
	})
	capture.Finalize(FinalizeAuditInput{Outcome: AuditOutcomeUpstreamFailed, Success: false, StatusCode: intPointer(502)})

	log := dispatcher.all()
	if len(log) != 1 {
		t.Fatalf("audit log count = %d, want 1", len(log))
	}
	if log[0].RouteGroupID != "grp_super" {
		t.Fatalf("route_group_id = %q, want grp_super", log[0].RouteGroupID)
	}
	if log[0].GroupID != "grp_super" {
		t.Fatalf("group_id = %q, want grp_super（无成功尝试时回落绑定组）", log[0].GroupID)
	}
}

// RouteGroupID 必须与 GroupID 独立合并：绑定路由目标后，后续只绑 GroupID 的
// 调用不得把 route_group_id 覆盖掉，也不得反向覆盖 group_id。
func TestBindContextKeepsRouteGroupIndependent(t *testing.T) {
	capture := w13dCapture(nil, nil)
	capture.BindContext(AuditGatewayContext{GroupID: "grp_super", RouteGroupID: "grp_super"})
	capture.BindContext(AuditGatewayContext{GroupID: "grp_member"})
	if got := capture.gatewayContextSnapshot(); got.GroupID != "grp_member" || got.RouteGroupID != "grp_super" {
		t.Fatalf("group=%q route=%q, want grp_member/grp_super", got.GroupID, got.RouteGroupID)
	}
	// 空 RouteGroupID 不覆盖已有值。
	capture.BindContext(AuditGatewayContext{})
	if got := capture.gatewayContextSnapshot(); got.RouteGroupID != "grp_super" {
		t.Fatalf("route=%q, want grp_super", got.RouteGroupID)
	}
}
