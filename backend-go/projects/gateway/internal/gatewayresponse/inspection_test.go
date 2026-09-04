package gatewayresponse

import (
	"encoding/json"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func strPtr(value string) *string { return &value }

func TestResolveRuntimeResponseInspectionPoliciesOrdering(t *testing.T) {
	policies := ResolveRuntimeResponseInspectionPolicies("openai", "p1",
		[]AccountResponseInspectionRule{
			{Name: "account-rule", Enabled: true, Priority: 5, Action: "retry_next_account"},
		},
		[]gatewayruntimecache.ResponseInspectionPolicySummary{
			{ID: "mgmt_provider", Enabled: true, Priority: 1, ScopeType: "provider", ProtocolCode: "openai", ProviderCode: strPtr("p1"), Action: "retry_next_account"},
			{ID: "mgmt_protocol", Enabled: true, Priority: 1, ScopeType: "protocol", ProtocolCode: "openai", Action: "retry_next_account"},
			{ID: "mgmt_other_provider", Enabled: true, ScopeType: "provider", ProtocolCode: "openai", ProviderCode: strPtr("other"), Action: "retry_next_account"},
		},
	)
	if len(policies) != 3 {
		t.Fatalf("policies = %+v", policies)
	}
	// 账户规则优先，其次 provider 作用域管理策略，再次 protocol 作用域。
	if policies[0].ID != "account_rule_1" || policies[1].ID != "mgmt_provider" || policies[2].ID != "mgmt_protocol" {
		t.Fatalf("order = %v %v %v", policies[0].ID, policies[1].ID, policies[2].ID)
	}
	if policies[0].Source != PolicySourceAccount || policies[1].Source != PolicySourceManagement {
		t.Fatalf("sources = %q %q", policies[0].Source, policies[1].Source)
	}
	if policies[0].ExecutionMode != "enforce" || policies[0].AccountSwitch != "request_next_account" || !policies[0].RetryEnabled {
		t.Fatalf("account runtime = %+v", policies[0])
	}
}

func TestInspectResponseSemanticFramesActions(t *testing.T) {
	policy := RuntimeResponseInspectionPolicy{
		ID: "policy_1", Source: PolicySourceManagement, Name: "禁词", Enabled: true,
		ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: false,
		Match: gatewayruntimecache.ResponseInspectionPolicyMatch{OutputTextIncludes: []string{"机密"}},
	}
	frames := []gatewayproto.SemanticFrame{
		{FrameType: gatewayproto.FrameTypeOutputTextDone, Transport: gatewayproto.TransportSSE,
			EndpointFamily: gatewayproto.EndpointFamilyChatCompletions, Text: "包含机密内容", VisibleOutput: true},
	}
	result := InspectResponseSemanticFrames(frames, []RuntimeResponseInspectionPolicy{policy}, false, "sse", nil)
	if result.Decision == nil {
		t.Fatal("expected decision")
	}
	if result.Decision.Action != "replace_with_failure" || result.Decision.TriggerPhase != "before_downstream_write" {
		t.Fatalf("decision = %+v", result.Decision)
	}
	if result.Decision.RewriteMessage != "响应命中检查策略：禁词" {
		t.Fatalf("rewrite message = %q（Node 取 frame.errorMessage ?? 命中提示）", result.Decision.RewriteMessage)
	}
	if result.Decision.MatchedField != "outputTextIncludes" || result.Decision.MatchedValue != "机密" {
		t.Fatalf("match = %+v", result.Decision)
	}

	// dry_run → 仅观察。
	policy.ExecutionMode = "dry_run"
	result = InspectResponseSemanticFrames(frames, []RuntimeResponseInspectionPolicy{policy}, false, "sse", nil)
	if result.Decision != nil || len(result.Observations) != 1 {
		t.Fatalf("dry_run result = %+v", result)
	}

	// discard_event 仅对 SSE 生效；JSON 传输降级 dry_run。
	policy.ExecutionMode = "enforce"
	policy.DataHandling = "discard_event"
	sseResult := InspectResponseSemanticFrames(frames, []RuntimeResponseInspectionPolicy{policy}, false, "sse", nil)
	if sseResult.Decision == nil || sseResult.Decision.Action != "discard_event" {
		t.Fatalf("sse discard result = %+v", sseResult)
	}
	// Node：discard_event 在 JSON 传输降级为 dry_run action；由于
	// executionMode 仍为 enforce，决策按拦截路径返回。
	jsonResult := InspectResponseSemanticFrames(frames, []RuntimeResponseInspectionPolicy{policy}, true, "json", nil)
	if jsonResult.Decision == nil || jsonResult.Decision.Action != "dry_run" {
		t.Fatalf("json discard-downgrade result = %+v", jsonResult)
	}
	if jsonResult.Decision.TriggerPhase != "after_downstream_write" {
		t.Fatalf("trigger phase = %q", jsonResult.Decision.TriggerPhase)
	}
	// dry_run executionMode 才是纯观察路径。
	observePolicy := policy
	observePolicy.ExecutionMode = "dry_run"
	observeResult := InspectResponseSemanticFrames(frames, []RuntimeResponseInspectionPolicy{observePolicy}, true, "json", nil)
	if observeResult.Decision != nil || len(observeResult.Observations) != 1 {
		t.Fatalf("observe result = %+v", observeResult)
	}
}

func TestInspectResponseSemanticFramesErrorMatch(t *testing.T) {
	policy := RuntimeResponseInspectionPolicy{
		ID: "default_openai_context_window_error", Source: PolicySourceSystemDefault, Name: "上下文超限",
		Enabled: true, ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true,
		ScopeType: "provider", ProviderCode: "openai", AccountSwitch: "request_next_account",
		Match:         gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"context_length_exceeded"}},
	}
	frames := []gatewayproto.SemanticFrame{
		{FrameType: gatewayproto.FrameTypeError, Transport: gatewayproto.TransportJSON,
			EndpointFamily: gatewayproto.EndpointFamilyChatCompletions,
			ErrorCode:      "context_length_exceeded", ErrorMessage: "上下文过长"},
	}
	result := InspectResponseSemanticFrames(frames, []RuntimeResponseInspectionPolicy{policy}, false, "json", &ResponseInspectionRuntimeContext{ClientProfile: "codex"})
	if result.Decision == nil {
		t.Fatal("expected decision")
	}
	if result.Decision.ReplayAuthority != "system_default_retry_next_account" {
		t.Fatalf("replay authority = %q", result.Decision.ReplayAuthority)
	}
	if result.Decision.Reason != "before_downstream_write_response_failure" {
		t.Fatalf("system default reason = %q", result.Decision.Reason)
	}
	payload := ResponseInspectionFailurePayloadForDecision(result.Decision, true)
	if payload.ErrorCode != gatewaypreauthRetryCode() || payload.Message != GatewayStreamClientRetryMessage {
		t.Fatalf("failure payload = %+v", payload)
	}
	payload = ResponseInspectionFailurePayloadForDecision(result.Decision, false)
	if payload.ErrorCode != "context_length_exceeded" {
		t.Fatalf("no-retry payload = %+v", payload)
	}
}

func gatewaypreauthRetryCode() string { return "upstream_retryable_error" }

func TestMatchRuntimeResponseInspectionPolicyCodexContractProvenance(t *testing.T) {
	policy := RuntimeResponseInspectionPolicy{
		ID: CodexCompactionContractPolicyID, Source: PolicySourceSystemDefault, Enabled: true,
		ExecutionMode: "enforce", DataHandling: "replace_with_failure",
		ScopeType: "provider", ProviderCode: "openai",
		Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{CodexCompactionContractMismatchErrorCode}},
	}
	contractFrame := gatewayproto.SemanticFrame{
		FrameType: gatewayproto.FrameTypeRawJSONPath,
		ErrorCode: CodexCompactionContractMismatchErrorCode,
	}
	if MatchRuntimeResponseInspectionPolicy(contractFrame, []RuntimeResponseInspectionPolicy{policy}, nil) == nil {
		t.Fatal("contract frame matches compaction policy")
	}
	plainFrame := gatewayproto.SemanticFrame{FrameType: gatewayproto.FrameTypeError, ErrorCode: CodexCompactionContractMismatchErrorCode}
	if MatchRuntimeResponseInspectionPolicy(plainFrame, []RuntimeResponseInspectionPolicy{policy}, nil) != nil {
		t.Fatal("non-contract frame must not match compaction policy")
	}
}

func TestJSONPathExists(t *testing.T) {
	var value any
	if err := json.Unmarshal([]byte(`{"output":[{"type":"compaction"}],"tool":{"enabled":true},"empty":{}}`), &value); err != nil {
		t.Fatal(err)
	}
	if !jsonPathExists(value, "output.0.type") {
		t.Fatal("nested array path exists")
	}
	if !jsonPathExists(value, "tool.enabled") {
		t.Fatal("bool true counts as meaningful")
	}
	if jsonPathExists(value, "output.5.type") {
		t.Fatal("out of range index")
	}
	if jsonPathExists(value, "missing.path") {
		t.Fatal("missing path")
	}
	if jsonPathExists(value, "") {
		t.Fatal("empty path")
	}
	if jsonPathExists(value, "empty") {
		t.Fatal("empty object has no meaningful value")
	}
}

func TestSnippetAround(t *testing.T) {
	snippet := snippetAround("前文上下文，这里是机密信息，这里是后文长内容补充补充补充", "机密")
	if snippet == "" || !containsString(snippet, "机密") {
		t.Fatalf("snippet = %q", snippet)
	}
	long := make([]rune, 300)
	for i := range long {
		long[i] = rune('字')
	}
	trimmed := snippetAround(string(long), "没有")
	if len([]rune(trimmed)) != textMatchSnippetChars {
		t.Fatalf("fallback snippet length = %d", len([]rune(trimmed)))
	}
}
