package gatewayhybrid

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

func routeService(affinity *AffinityService, selector TargetGroupSelector, diagnostics *mockDiagnostics) *RouteService {
	return NewRouteService(affinity, selector, &mockIdentity{}, diagnostics)
}

func routeView() *GatewayRequestView {
	return &GatewayRequestView{
		Method:        "POST",
		Path:          "/v1/chat/completions",
		ContentType:   "application/json",
		RawBody:       []byte(`{"model":"gpt-5","messages":[]}`),
		BodyAvailable: true,
	}
}

func scoringScript(level int) *mockDispatcher {
	body := `{"choices":[{"message":{"content":"{\"level\":` + strconvItoaTest(level) + `}"}}]}`
	return &mockDispatcher{script: []dispatchOutcome{{
		success: successDispatch("scoring-acct", "scoring-group", 200, body, gatewayprotoEmptyUsage()),
	}}}
}

func strconvItoaTest(value int) string {
	digits := "0123456789"
	if value == 0 {
		return "0"
	}
	out := ""
	for value > 0 {
		out = string(digits[value%10]) + out
		value /= 10
	}
	return out
}

func selectionFor(groupID string, model string) *TargetGroupSelection {
	return &TargetGroupSelection{
		GroupID:     groupID,
		GroupAccess: GroupUsageAccessMetadata{ProviderCode: "openai", SchedulingPolicy: "cost_first"},
		Accounts:    []OpenAIAccountSecret{{ID: "acct-" + groupID}},
		ResponseInspectionPolicies: []ResponseInspectionPolicySummary{{PolicyID: "policy-" + groupID}},
	}
}

func TestIsHybridRoutableRequest(t *testing.T) {
	tests := []struct {
		name string
		view *GatewayRequestView
		want bool
	}{
		{"post json raw body", routeView(), true},
		{"post json parsed body", &GatewayRequestView{Method: "POST", ContentType: "application/json", BodyAvailable: true}, true},
		{"get method", &GatewayRequestView{Method: "GET", ContentType: "application/json", RawBody: []byte("{}")}, false},
		{"non json content type", &GatewayRequestView{Method: "POST", ContentType: "text/plain", RawBody: []byte("{}")}, false},
		{"empty body", &GatewayRequestView{Method: "POST", ContentType: "application/json"}, false},
		{"method case insensitive", &GatewayRequestView{Method: "post", ContentType: "application/json", RawBody: []byte("{}")}, true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsHybridRoutableRequest(testCase.view); got != testCase.want {
				t.Fatalf("routable = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestResolveSkipsNonHybridRoutes(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: "normal", HybridRoutingConfig: config}
	service := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), &mockSelector{}, &mockDiagnostics{})
	result, err := service.Resolve(context.Background(), RouteInput{
		View: routeView(), Body: &mockBodyGateway{object: NewOrderedJSON()}, APIKeyRecord: record,
	}, NewScoringService(testClock(&now), &mockDispatcher{}, nil, nil, nil))
	if err != nil || result.Outcome != RouteOutcomeSkipped || result.Reason != RouteSkipNotHybridStrategy {
		t.Fatalf("result = %+v err=%v", result, err)
	}

	record = APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart}
	result, err = service.Resolve(context.Background(), RouteInput{
		View: routeView(), Body: &mockBodyGateway{object: NewOrderedJSON()}, APIKeyRecord: record,
	}, NewScoringService(testClock(&now), &mockDispatcher{}, nil, nil, nil))
	if err != nil || result.Outcome != RouteOutcomeSkipped || result.Reason != RouteSkipNotHybridStrategy {
		t.Fatalf("result = %+v err=%v", result, err)
	}

	record = APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	result, err = service.Resolve(context.Background(), RouteInput{
		View: &GatewayRequestView{Method: "GET", ContentType: "application/json"}, Body: &mockBodyGateway{}, APIKeyRecord: record,
	}, NewScoringService(testClock(&now), &mockDispatcher{}, nil, nil, nil))
	if err != nil || result.Outcome != RouteOutcomeSkipped || result.Reason != RouteSkipNotJSONPost {
		t.Fatalf("result = %+v err=%v", result, err)
	}
}

func TestResolveSelectsLevelRouteWithAffinityAndRewrite(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	body := &mockBodyGateway{object: mustParseObject(t, `{"model":"gpt-5","messages":[]}`), replaceOK: true}
	selector := &mockSelector{selections: map[string]*TargetGroupSelection{
		"gpt-5-mini": selectionFor("group-mini", "gpt-5-mini"),
		"gpt-5":      selectionFor("group-high", "gpt-5"),
	}}
	diagnostics := &mockDiagnostics{}
	scoring := scoringScript(3) // level 3 → gpt-5-mini route
	service := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), selector, diagnostics)
	result, err := service.Resolve(context.Background(), RouteInput{
		View: routeView(), Body: body, APIKeyRecord: record, TraceID: "trace-1", Endpoint: "/ep", Audit: &mockAudit{},
	}, NewScoringService(testClock(&now), scoring, &mockRecorder{}, nil, nil))
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if result.Outcome != RouteOutcomeSelected || result.TargetModel != "gpt-5-mini" || result.GroupID != "group-mini" {
		t.Fatalf("result = %+v", result)
	}
	if result.APIKeyRecord.SelectedGroupID != "group-mini" {
		t.Fatalf("selected group = %s", result.APIKeyRecord.SelectedGroupID)
	}
	if result.AffinityApplied || result.ScoringFallbackApplied {
		t.Fatalf("flags = %+v", result)
	}
	if body.replacedModel != "gpt-5-mini" {
		t.Fatalf("rewritten model = %s", body.replacedModel)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].ID != "acct-group-mini" {
		t.Fatalf("accounts = %+v", result.Accounts)
	}
	if len(result.ResponseInspectionPolicies) != 1 {
		t.Fatalf("policies = %+v", result.ResponseInspectionPolicies)
	}
	// One diagnostics publish with the selected contract keys (undefined
	// values are dropped exactly like JSON.stringify).
	diagnostics.mu.Lock()
	published := len(diagnostics.values)
	var rendered string
	if published > 0 {
		rendered = NodeJSONStringify(diagnostics.values[0])
	}
	diagnostics.mu.Unlock()
	if published != 1 {
		t.Fatalf("diagnostics publishes = %d", published)
	}
	for _, key := range []string{`"traceId":"trace-1"`, `"outcome":"selected"`, `"level":3`, `"targetGroupId":"group-mini"`, `"affinityApplied":false`, `"levelRange":[1,5]`} {
		if !strings.Contains(rendered, key) {
			t.Fatalf("diagnostics missing %s in %s", key, rendered)
		}
	}
	if strings.Contains(rendered, "scoringFallbackApplied") || strings.Contains(rendered, "upgradedFromModel") {
		t.Fatalf("undefined-valued keys must be dropped: %s", rendered)
	}
}

func TestResolveUpgradesThroughHigherLevelRoutes(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	// Lower tier unavailable, upper tier available.
	selector := &mockSelector{selections: map[string]*TargetGroupSelection{
		"gpt-5": selectionFor("group-high", "gpt-5"),
	}}
	body := &mockBodyGateway{object: mustParseObject(t, `{"model":"gpt-5-mini"}`), replaceOK: true}
	service := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), selector, &mockDiagnostics{})
	result, err := service.Resolve(context.Background(), RouteInput{
		View: routeView(), Body: body, APIKeyRecord: record, TraceID: "trace-2", Endpoint: "/ep",
	}, NewScoringService(testClock(&now), scoringScript(3), &mockRecorder{}, nil, nil))
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if result.Outcome != RouteOutcomeSelected || result.TargetModel != "gpt-5" || result.GroupID != "group-high" {
		t.Fatalf("result = %+v", result)
	}
	if body.replacedModel != "gpt-5" {
		t.Fatalf("rewritten model = %s", body.replacedModel)
	}
}

func TestResolveFailsWhenNoTargetGroupAvailable(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	selector := &mockSelector{selections: map[string]*TargetGroupSelection{}}
	diagnostics := &mockDiagnostics{}
	service := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), selector, diagnostics)
	result, err := service.Resolve(context.Background(), RouteInput{
		View: routeView(), Body: &mockBodyGateway{object: NewOrderedJSON()}, APIKeyRecord: record, TraceID: "trace-3", Endpoint: "/ep",
	}, NewScoringService(testClock(&now), scoringScript(3), &mockRecorder{}, nil, nil))
	if err != nil || result.Outcome != RouteOutcomeFailed || result.Reason != RouteFailTargetGroupUnavailable {
		t.Fatalf("result = %+v err=%v", result, err)
	}
	if result.TargetModel2 != "gpt-5-mini" {
		t.Fatalf("failed targetModel = %s", result.TargetModel2)
	}
}

func TestResolveScoringFallbackPath(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	// Scoring dispatch fails; fallback searches enabled routes <= fallback max level 5.
	failedScoring := &mockDispatcher{script: []dispatchOutcome{{
		failure: failureDispatch("hybrid_scoring_failed", "boom", "acct", "group", 500, false),
	}}}
	selector := &mockSelector{selections: map[string]*TargetGroupSelection{
		"gpt-5-mini": selectionFor("group-mini", "gpt-5-mini"),
	}}
	body := &mockBodyGateway{object: mustParseObject(t, `{"model":"gpt-5"}`), replaceOK: true}
	audit := &mockAudit{}
	diagnostics := &mockDiagnostics{}
	service := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), selector, diagnostics)
	result, err := service.Resolve(context.Background(), RouteInput{
		View: routeView(), Body: body, APIKeyRecord: record, TraceID: "trace-4", Endpoint: "/ep", Audit: audit,
	}, NewScoringService(testClock(&now), failedScoring, &mockRecorder{}, nil, nil))
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if result.Outcome != RouteOutcomeSelected || !result.ScoringFallbackApplied || result.AffinityApplied {
		t.Fatalf("result = %+v", result)
	}
	if result.TargetModel != "gpt-5-mini" || result.Scoring.ErrorCode != "hybrid_scoring_failed" {
		t.Fatalf("result = %+v", result)
	}
	if body.replacedModel != "gpt-5-mini" {
		t.Fatalf("rewritten model = %s", body.replacedModel)
	}
	// Audit metadata captured under the hybrid_route label.
	if len(audit.labels) != 1 || audit.labels[0] != "hybrid_route" {
		t.Fatalf("audit labels = %v", audit.labels)
	}
	rendered := NodeJSONStringify(audit.metadatas[0])
	for _, key := range []string{`"scoringFallbackApplied":true`, `"scoringFallbackReason":"hybrid_scoring_failed"`, `"scoringFallbackMaxLevel":5`, `"affinityApplied":false`} {
		if !strings.Contains(rendered, key) {
			t.Fatalf("diagnostics missing %s in %s", key, rendered)
		}
	}
}

func TestResolveScoringFallbackUnavailableFails(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	failedScoring := &mockDispatcher{script: []dispatchOutcome{{
		failure: failureDispatch("hybrid_scoring_failed", "boom", "", "", 0, false),
	}}}
	selector := &mockSelector{}
	service := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), selector, &mockDiagnostics{})
	result, err := service.Resolve(context.Background(), RouteInput{
		View: routeView(), Body: &mockBodyGateway{object: NewOrderedJSON()}, APIKeyRecord: record, TraceID: "trace-5", Endpoint: "/ep",
	}, NewScoringService(testClock(&now), failedScoring, &mockRecorder{}, nil, nil))
	if err != nil || result.Outcome != RouteOutcomeFailed || result.Reason != RouteFailScoringFallbackGone {
		t.Fatalf("result = %+v err=%v", result, err)
	}
}

func TestResolveAffinitySticksRoute(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	view := routeView()
	view.ConversationKey = "conv-sticky"
	selector := &mockSelector{selections: map[string]*TargetGroupSelection{
		"gpt-5":      selectionFor("group-high", "gpt-5"),
		"gpt-5-mini": selectionFor("group-mini", "gpt-5-mini"),
	}}
	affinity := NewAffinityService(testClock(&now), &mockIdentity{}, nil)
	service := routeService(affinity, selector, &mockDiagnostics{})
	scoringSvc := NewScoringService(testClock(&now), scoringScript(7), &mockRecorder{}, nil, nil)
	body1 := &mockBodyGateway{object: mustParseObject(t, `{"model":"gpt-5"}`), replaceOK: true}
	first, err := service.Resolve(context.Background(), RouteInput{
		View: view, Body: body1, APIKeyRecord: record, TraceID: "t1", Endpoint: "/ep",
	}, scoringSvc)
	if err != nil || first.TargetModel != "gpt-5" {
		t.Fatalf("first = %+v err=%v", first, err)
	}
	// Same session scores the low route with a small delta: affinity sticks to
	// the previous high route.
	scoringLow := scoringScript(3)
	body2 := &mockBodyGateway{object: mustParseObject(t, `{"model":"gpt-5-mini"}`), replaceOK: true}
	second, err := service.Resolve(context.Background(), RouteInput{
		View: view, Body: body2, APIKeyRecord: record, TraceID: "t2", Endpoint: "/ep",
	}, NewScoringService(testClock(&now), scoringLow, &mockRecorder{}, nil, nil))
	if err != nil {
		t.Fatalf("second error: %v", err)
	}
	if second.Outcome != RouteOutcomeSelected || !second.AffinityApplied || second.TargetModel != "gpt-5" {
		t.Fatalf("second = %+v", second)
	}
}

func TestRewriteHybridRequestModelErrorPaths(t *testing.T) {
	ctx := context.Background()
	if err := RewriteHybridRequestModel(ctx, &mockBodyGateway{rawBody: []byte{}}, "m"); err == nil || err.Error() != "混合路由无法改写空请求体" {
		t.Fatalf("err = %v", err)
	}
	if err := RewriteHybridRequestModel(ctx, &mockBodyGateway{rawBody: []byte(`[1,2]`), parseValue: []any{1.0, 2.0}}, "m"); err == nil || err.Error() != "混合路由请求体必须是 JSON 对象" {
		t.Fatalf("err = %v", err)
	}
	if err := RewriteHybridRequestModel(ctx, &mockBodyGateway{rawBody: []byte(`{"a":1}`), replaceParsed: false}, "m"); err == nil || err.Error() != "混合路由模型改写失败" {
		t.Fatalf("err = %v", err)
	}
	body := &mockBodyGateway{rawBody: []byte(`{"a":1}`), replaceParsed: true}
	if err := RewriteHybridRequestModel(ctx, body, "m"); err != nil || body.replacedModel != "m" {
		t.Fatalf("err = %v replaced = %s", err, body.replacedModel)
	}
}

func TestResolveNextUpgradePath(t *testing.T) {
	now := time.Now()
	config := hybridConfig()
	record := APIKeyRecord{ID: "key", SystemAccountID: "sys", RouteStrategyMode: routestrategies.ModeHybridSmart, HybridRoutingConfig: config}
	selector := &mockSelector{selections: map[string]*TargetGroupSelection{
		"gpt-5": selectionFor("group-high", "gpt-5"),
	}}
	body := &mockBodyGateway{object: mustParseObject(t, `{"model":"gpt-5-mini"}`), replaceOK: true}
	service := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), selector, &mockDiagnostics{})
	current := routestrategies.HybridLevelRoute{MinLevel: 1, MaxLevel: 5, TargetModel: "gpt-5-mini", Enabled: true}
	next, err := service.ResolveNext(context.Background(), RouteInput{
		View: routeView(), Body: body, APIKeyRecord: record,
	}, current)
	if err != nil || next == nil || next.TargetModel != "gpt-5" || next.GroupID != "group-high" {
		t.Fatalf("next = %+v err=%v", next, err)
	}
	if next.APIKeyRecord.SelectedGroupID != "group-high" {
		t.Fatalf("selected group = %s", next.APIKeyRecord.SelectedGroupID)
	}
	if body.replacedModel != "gpt-5" {
		t.Fatalf("rewritten model = %s", body.replacedModel)
	}
	// No higher target → nil.
	selector2 := &mockSelector{}
	service2 := routeService(NewAffinityService(testClock(&now), &mockIdentity{}, nil), selector2, &mockDiagnostics{})
	next, err = service2.ResolveNext(context.Background(), RouteInput{
		View: routeView(), Body: &mockBodyGateway{object: NewOrderedJSON()}, APIKeyRecord: record,
	}, current)
	if err != nil || next != nil {
		t.Fatalf("next = %+v err=%v", next, err)
	}
}
