package gatewayresponse

// BUG-0289 回归测试：200 流内失败（encrypted_context_invalid）的恢复臂。
// 生产事实：上游 200 SSE 首事件 response.failed(code=encrypted_context_invalid)，
// default_openai_response_error（system_default, retry_no_avoidance）拦截后两个
// 既有服务端重试分支都判否（无 ReplayAuthority/AccountSwitch、
// ResponseInspection != nil 互斥短路），失败文案"请重试"交回客户端导致 Codex
// 死重试。恢复臂从结构化决策字段分类信号，用引擎实际发送的请求体构造清理重放
// verdict；无可清理内容时客户端文案改为恢复终态（不再说"请重试"），上游错误码
// 归因保持 encrypted_context_invalid。

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// bug0289ProductionFailedEvent 是生产形态的流内失败 SSE 首事件。
const bug0289ProductionFailedEvent = "event: response.failed\n" +
	`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"encrypted_context_invalid","message":"The upstream could not validate encrypted continuation context. Rebuild the conversation context in the client."}}}` + "\n\n"

// bug0289ResponsesRequestBody 携带 reasoning.encrypted_content 与已映射的
// model 字段（引擎 dispatched.RequestBody 语义）。
const bug0289ResponsesRequestBody = `{"model":"gpt-test","stream":true,"previous_response_id":"resp_prev_1","input":[{"type":"reasoning","summary":[],"encrypted_content":"opaque-payload"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

// bug0289DefaultResponseErrorPolicy 注入与生产系统默认规则
// default_openai_response_error 同形的策略摘要（sqlinspection.go:242）。
func bug0289DefaultResponseErrorPolicy() gatewayruntimecache.ResponseInspectionPolicySummary {
	return gatewayruntimecache.ResponseInspectionPolicySummary{
		ID:           "default_openai_response_error",
		DefaultRule:  true,
		Name:         "OpenAI response.error",
		Enabled:      true,
		Priority:     3,
		ScopeType:    "protocol",
		ProtocolCode: "openai",
		Match:        gatewayruntimecache.ResponseInspectionPolicyMatch{JSONPathsExists: []string{"response.error"}},
		Action:       "retry_no_avoidance",
	}
}

func bug0289NewResponsesInput(t *testing.T, body UpstreamBody) (HandleUpstreamResponseInput, *httptest.ResponseRecorder, *mockAuditCapture) {
	t.Helper()
	input, recorder := newInputFixture(body, 200, nil)
	// 生产场景是 /v1/responses 流式请求。
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/responses", nil))
	input.RequestBody = []byte(bug0289ResponsesRequestBody)
	// Codex 客户端策略：重试信号 + 语义解释 + responses_sse 下行协议
	//（缺失 DownstreamProtocol 时协议失败事件渲染为 nil，客户端 body 为空）。
	input.ClientStrategy = &ClientStrategyView{RetryPreCommitProtocolError: true, InterpretSemantics: true, DownstreamProtocol: "responses_sse"}
	input.ResponseInspectionPolicies = []gatewayruntimecache.ResponseInspectionPolicySummary{bug0289DefaultResponseErrorPolicy()}
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	return input, recorder, input.AuditCapture.(*mockAuditCapture)
}

func TestBug0289StreamEncryptedContextRecoveryVerdict(t *testing.T) {
	input, recorder, audit := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(bug0289ProductionFailedEvent)))
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.RetryUpstream {
		t.Fatalf("必须产出服务端重试 verdict: %+v", result)
	}
	if result.RetryReason != StreamServerRetryCodexEncryptedContentRecovery {
		t.Fatalf("retryReason = %q", result.RetryReason)
	}
	if result.CompatibilityRecoverySignal != gatewaycodex.SignalEncryptedContextInvalid {
		t.Fatalf("compatibilityRecoverySignal = %q", result.CompatibilityRecoverySignal)
	}
	if result.ExcludeCurrentAccount {
		t.Fatal("恢复重放不得排除当前账户")
	}
	if result.SameAccountRetryEligible {
		t.Fatal("恢复重放走专用钉住通道，不走 SameAccountRetryEligible")
	}
	if result.RecoverySemanticRetryID != "codex_encrypted_content_cleanup:"+gatewaycodex.SignalEncryptedContextInvalid {
		t.Fatalf("recoverySemanticRetryID = %q", result.RecoverySemanticRetryID)
	}
	if len(result.RecoveryBody) == 0 {
		t.Fatal("recoveryBody = nil")
	}
	if strings.Contains(string(result.RecoveryBody), "encrypted_content") {
		t.Fatalf("recoveryBody 必须已清除 encrypted_content: %s", result.RecoveryBody)
	}
	if !strings.Contains(string(result.RecoveryBody), `"model":"gpt-test"`) {
		t.Fatalf("recoveryBody 必须保留已映射 model 字段: %s", result.RecoveryBody)
	}
	if result.RecoveryMetadata == nil || result.RecoveryMetadata.Signal != gatewaycodex.SignalEncryptedContextInvalid {
		t.Fatalf("recoveryMetadata = %+v", result.RecoveryMetadata)
	}
	metadata := audit.findMetadata("codex_encrypted_content_recovery_retry")
	if metadata == nil {
		t.Fatalf("codex_encrypted_content_recovery_retry metadata 缺失: %v", audit.metadataLabels())
	}
	if metadata["accountId"] != "acc-1" || metadata["signal"] != gatewaycodex.SignalEncryptedContextInvalid {
		t.Fatalf("metadata = %+v", metadata)
	}
	if _, hasStrategy := metadata["strategy"]; !hasStrategy {
		t.Fatalf("metadata 缺 strategy 键: %+v", metadata)
	}
	if _, hasBefore := metadata["bodyBytesBefore"]; !hasBefore {
		t.Fatalf("metadata 缺 bodyBytesBefore 键: %+v", metadata)
	}
	if _, hasAfter := metadata["bodyBytesAfter"]; !hasAfter {
		t.Fatalf("metadata 缺 bodyBytesAfter 键: %+v", metadata)
	}
	if recorder.Body.String() != "" {
		t.Fatalf("恢复 verdict 不得向客户端写失败终态: %q", recorder.Body.String())
	}
}

// TestBug0289StreamEncryptedContextNotRecoverableFinalCopy：信号命中但请求体
// 无可清理项 → 不重试，客户端文案改为恢复终态（含"请新建会话"，不含
// "请重试"），上游错误码归因保持 encrypted_context_invalid。
func TestBug0289StreamEncryptedContextNotRecoverableFinalCopy(t *testing.T) {
	plainBody := `{"model":"gpt-test","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	input, recorder, audit := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(bug0289ProductionFailedEvent)))
	input.RequestBody = []byte(plainBody)
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.RetryUpstream || !result.AlreadyFinalized {
		t.Fatalf("不可恢复场景必须落到客户端终态: %+v", result)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, gatewaycodex.CodexEncryptedContentRecoveryExhaustedMessage) {
		t.Fatalf("客户端 body 必须含恢复终态文案: %q", body)
	}
	if strings.Contains(body, "请重试") {
		t.Fatalf("客户端 body 不得再携带可重试文案: %q", body)
	}
	if len(audit.finalized) != 1 {
		t.Fatalf("finalized = %d", len(audit.finalized))
	}
	if audit.finalized[0].ErrorCode != "encrypted_context_invalid" {
		t.Fatalf("audit 错误码归因必须保持 encrypted_context_invalid: %+v", audit.finalized[0])
	}
	skipped := audit.findMetadata("codex_encrypted_content_recovery_skipped")
	if skipped == nil {
		t.Fatalf("codex_encrypted_content_recovery_skipped metadata 缺失: %v", audit.metadataLabels())
	}
	if skipped["accountId"] != "acc-1" || skipped["signal"] != gatewaycodex.SignalEncryptedContextInvalid {
		t.Fatalf("skipped metadata = %+v", skipped)
	}
	if _, hasReason := skipped["reason"]; !hasReason {
		t.Fatalf("skipped metadata 缺 reason 键: %+v", skipped)
	}
	// usage 失败记录的错误码归因不被文案覆盖（既有管道改写面保持不变）。
	if len(audit.completed) != 1 {
		t.Fatalf("completed = %d", len(audit.completed))
	}
}

// TestBug0289StreamEncryptedContextNotApplicableGuards：非 openai 协议决策或
// 非 responses 请求不进入恢复臂，行为与现状完全一致（守卫测试，防回归）。
// 现状基线：default_openai_response_error 拦截后两个重试分支都判否，失败以
// 客户端失败终态（"请重试"文案）交接。
func TestBug0289StreamEncryptedContextNotApplicableGuards(t *testing.T) {
	t.Run("non responses request keeps existing behavior", func(t *testing.T) {
		input, recorder, _ := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(bug0289ProductionFailedEvent)))
		input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
		result, err := HandleStreamUpstreamResponse(input)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if result.RetryReason == StreamServerRetryCodexEncryptedContentRecovery {
			t.Fatalf("chat 请求不得进入恢复臂: %+v", result)
		}
		if !result.AlreadyFinalized {
			t.Fatalf("chat 请求保持既有客户端失败终态行为: %+v", result)
		}
		if !strings.Contains(recorder.Body.String(), GatewayStreamClientRetryMessage) {
			t.Fatalf("chat 请求保持既有可重试文案: %q", recorder.Body.String())
		}
	})
	t.Run("non openai protocol decision keeps existing behavior", func(t *testing.T) {
		input, _, _ := bug0289NewResponsesInput(t, NewSliceUpstreamBody([]byte(bug0289ProductionFailedEvent)))
		policy := bug0289DefaultResponseErrorPolicy()
		policy.ProtocolCode = "other-protocol"
		input.ResponseInspectionPolicies = []gatewayruntimecache.ResponseInspectionPolicySummary{policy}
		result, err := HandleStreamUpstreamResponse(input)
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if result.RetryReason == StreamServerRetryCodexEncryptedContentRecovery {
			t.Fatalf("非 openai 协议不得进入恢复臂: %+v", result)
		}
		// 非 openai 协议策略不命中 openai 账户作用域 → 无检查决策 → 恢复臂
		// 结构性不可达，失败走既有 pre-commit 重试（现状基线）。
		if result.ResponseInspection != nil {
			t.Fatalf("非 openai 协议策略不得产生检查决策: %+v", result.ResponseInspection)
		}
		if !result.RetryUpstream || result.RetryReason != StreamServerRetryPreCommitStreamFailure {
			t.Fatalf("非 openai 协议保持既有 pre-commit 重试行为: %+v", result)
		}
	})
}

// ---- mockAuditCapture 辅助（包内测试共享类型的只读视图） ----

func (m *mockAuditCapture) metadataLabels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	labels := make([]string, 0, len(m.metadata))
	for _, entry := range m.metadata {
		if label, ok := entry["__label"].(string); ok {
			labels = append(labels, label)
		}
	}
	return labels
}

func (m *mockAuditCapture) findMetadata(label string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.metadata {
		if entry["__label"] == label {
			return entry
		}
	}
	return nil
}
