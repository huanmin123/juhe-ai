package gatewaydispatch

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 极小尾巴：生命周期门面方法与分组回退端口适配。

func TestAttemptLifecycleFacadeDelegates(t *testing.T) {
	firstByteCalls := 0
	terminalCalls := 0
	facade := &attemptLifecycleFacade{
		MarkFirstByteFunc:  func(*float64) { firstByteCalls++ },
		RecordTerminalFunc: func(context.Context, HotQualityTerminal) { terminalCalls++ },
	}
	var facadeAsLifecycle HotQualityAttemptLifecycle = facade
	facadeAsLifecycle.MarkFirstByte(nil)
	facadeAsLifecycle.RecordTerminal(context.Background(), HotQualityTerminal{OutcomeClass: HotQualityOutcomeTransportFailure})
	if firstByteCalls != 1 || terminalCalls != 1 {
		t.Fatalf("firstByte=%d terminal=%d", firstByteCalls, terminalCalls)
	}
}

func TestResolveNextGroupFallbackCandidatePortAdapts(t *testing.T) {
	pipeline, engine, _, _ := newPipeline(t)
	req := newTestRequest(t, `{"model":"gpt-test","stream":true}`)
	engine.Cache = &fakeCacheFallback{
		accounts: map[string][]AccountCandidate{
			"group-2": testAccounts("b-1"),
		},
	}
	cursor := 0
	snapshot, err := gatewayrouting.CreateGatewayRoutePlanSnapshot(gatewayrouting.CreateGatewayRoutePlanSnapshotInput[string]{
		RoutePlanID:           "plan-tiny",
		Mode:                  "ordered",
		RequestAcceptedAtMs:   NowMs(),
		OrderedAllowedTargets: []string{"group-1", "group-2"},
		Cursor:                &cursor,
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	input := gatewaypreauth.GroupFallbackCandidateInput{
		Req:    req,
		Reason: "upstream_accounts_exhausted",
		APIKeyRecord: &gatewayruntimecache.GatewayAPIKeyRow{
			ID: "apikey-1",
			GroupBindings: []gatewayruntimecache.GatewayAPIKeyGroupBindingRow{
				{GroupID: "group-1", Status: "active", GroupEnabled: 1},
				{GroupID: "group-2", Status: "active", GroupEnabled: 1},
			},
		},
		SystemAccountID:   "system-1",
		GroupID:           "group-1",
		RequestLane:       "text",
		RoutePlanSnapshot: snapshot,
	}
	candidate, found, err := pipeline.ResolveNextGroupFallbackCandidate(context.Background(), input)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !found || candidate.GroupID != "group-2" {
		t.Fatalf("candidate = %#v found=%v", candidate, found)
	}
}
