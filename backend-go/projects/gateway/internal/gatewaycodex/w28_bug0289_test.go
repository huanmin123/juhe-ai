package gatewaycodex

// BUG-0289 回归测试：encrypted_context_invalid 信号识别补齐、结构化决策字段
// 分类入口（ClassifyCodexEncryptedContentFailureParts）与核心清理构造
// （BuildCodexEncryptedContentRecoveryRetry）。生产形态：Codex 客户端
// /v1/responses 流式请求的上游 200 SSE 首事件 response.failed 携带
// code=encrypted_context_invalid，网关此前不认识该码（信号白名单缺失），
// 流内失败面无恢复路径，客户端死重试。

import (
	"context"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
)

// bug0289ProductionSseErrorText 是生产取证的流内失败载体：上游 200 SSE 首事件
// response.failed，error 嵌套在 response.error 下。
const bug0289ProductionSseErrorText = "event: response.failed\n" +
	`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"encrypted_context_invalid","message":"The upstream could not validate encrypted continuation context. Rebuild the conversation context in the client."}}}` + "\n\n"

// bug0289ProductionRequestBody 是生产形态的 responses 请求体：携带上一轮
// reasoning.encrypted_content 与模型映射后的 model 字段。
const bug0289ProductionRequestBody = `{"model":"gpt-test","stream":true,"previous_response_id":"resp_prev_1","input":[{"type":"reasoning","summary":[],"encrypted_content":"opaque-payload"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

func TestBug0289ClassifyExactErrorCode(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"exact code padded", "  Encrypted_Context_Invalid ", SignalEncryptedContextInvalid},
		{"production sse data line", bug0289ProductionSseErrorText, SignalEncryptedContextInvalid},
		{"plain json", `{"code":"encrypted_context_invalid"}`, SignalEncryptedContextInvalid},
		{"nested error object", `{"error":{"code":"encrypted_context_invalid"}}`, SignalEncryptedContextInvalid},
		{"unrelated code", `{"code":"rate_limit_exceeded"}`, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ClassifyCodexEncryptedContentRecoverySignal(testCase.text); got != testCase.want {
				t.Fatalf("signal = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestBug0289ClassifyMessageHeuristic(t *testing.T) {
	// 仅消息载体（无精确码）走 structured payload 的消息启发：启发消费面把
	// "could not validate" 变体与既有解密失败族同映射（精确码在场时优先，
	// 生产 SSE 形态由精确码命中 encrypted_context_invalid，见上表）。
	payload := `{"type":"error","message":"The upstream could not validate encrypted continuation context. Rebuild the conversation context in the client."}`
	if got := ClassifyCodexEncryptedContentRecoverySignal(payload); got != SignalEncryptedContentDecryptionFailed {
		t.Fatalf("signal = %q, want %q", got, SignalEncryptedContentDecryptionFailed)
	}
}

func TestBug0289ClassifyParts(t *testing.T) {
	cases := []struct {
		name         string
		errorCode    string
		errorMessage string
		want         string
	}{
		{"exact code wins", "encrypted_context_invalid", "", SignalEncryptedContextInvalid},
		{"exact code beats message", "encrypted_context_invalid", "totally unrelated", SignalEncryptedContextInvalid},
		{
			"message heuristic shares legacy mapping",
			"",
			"The upstream could not validate encrypted continuation context. Rebuild the conversation context in the client.",
			SignalEncryptedContentDecryptionFailed,
		},
		{"decryption message keeps legacy signal", "", "encrypted content could not be decoded", SignalEncryptedContentDecryptionFailed},
		{"needs encrypted premise", "", "could not validate the request", ""},
		{"no signal", "rate_limit_exceeded", "encrypted payload exploded", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ClassifyCodexEncryptedContentFailureParts(testCase.errorCode, testCase.errorMessage)
			if got != testCase.want {
				t.Fatalf("ClassifyCodexEncryptedContentFailureParts(%q, %q) = %q, want %q",
					testCase.errorCode, testCase.errorMessage, got, testCase.want)
			}
		})
	}
}

// TestBug0289RecoverSharesNewSignal：非 2xx 失败面（RecoverCodexEncryptedContent）
// 共享 Classify 后自动识别 encrypted_context_invalid（chain_ports.go 调用方
// 无需改动即受益）。
func TestBug0289RecoverSharesNewSignal(t *testing.T) {
	req := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	result := RecoverCodexEncryptedContent(context.Background(), EncryptedContentRecoveryInput{
		Req:               req,
		Account:           openAIAccount(),
		Body:              []byte(bug0289ProductionRequestBody),
		UpstreamErrorText: bug0289ProductionSseErrorText,
	})
	if result.Action != RecoveryActionRetryWithBodyVariant {
		t.Fatalf("action = %q, want %q", result.Action, RecoveryActionRetryWithBodyVariant)
	}
	if !strings.Contains(string(result.Body), `"model":"gpt-test"`) {
		t.Fatalf("recovery body must keep the mapped model: %s", result.Body)
	}
	if strings.Contains(string(result.Body), "encrypted_content") {
		t.Fatalf("recovery body must drop encrypted content: %s", result.Body)
	}
	if result.SemanticRetryID != "codex_encrypted_content_cleanup:"+SignalEncryptedContextInvalid {
		t.Fatalf("semanticRetryId = %q", result.SemanticRetryID)
	}
	if result.Metadata == nil || result.Metadata.Signal != SignalEncryptedContextInvalid {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
}

// TestBug0289BuildCoreFromProductionShape：核心清理构造直测——结构化分类
// （ClassifyParts）产出的信号喂给 Build，产出与既有 Recover 相同契约的清理体。
func TestBug0289BuildCoreFromProductionShape(t *testing.T) {
	signal := ClassifyCodexEncryptedContentFailureParts("encrypted_context_invalid", "")
	if signal != SignalEncryptedContextInvalid {
		t.Fatalf("signal = %q", signal)
	}
	result := BuildCodexEncryptedContentRecoveryRetry([]byte(bug0289ProductionRequestBody), signal)
	if result.Action != RecoveryActionRetryWithBodyVariant {
		t.Fatalf("action = %q", result.Action)
	}
	if !strings.Contains(string(result.Body), `"model":"gpt-test"`) {
		t.Fatalf("body must keep the model field: %s", result.Body)
	}
	if strings.Contains(string(result.Body), "encrypted_content") {
		t.Fatalf("body must drop encrypted content: %s", result.Body)
	}
	if !strings.Contains(string(result.Body), `"type":"message"`) {
		t.Fatalf("body must keep surviving input items: %s", result.Body)
	}
	if result.SemanticRetryID != "codex_encrypted_content_cleanup:"+SignalEncryptedContextInvalid {
		t.Fatalf("semanticRetryId = %q", result.SemanticRetryID)
	}
	metadata := result.Metadata
	if metadata == nil {
		t.Fatal("metadata = nil")
	}
	if metadata.Strategy != "codex_encrypted_content_cleanup" || metadata.Signal != SignalEncryptedContextInvalid {
		t.Fatalf("metadata = %+v", metadata)
	}
	if metadata.RemovedReasoningEncryptedContentCount != 1 {
		t.Fatalf("removedReasoningEncryptedContentCount = %d, want 1", metadata.RemovedReasoningEncryptedContentCount)
	}
	if !metadata.PreservedPreviousResponseID {
		t.Fatal("preservedPreviousResponseID = false, want true")
	}
	if metadata.BodyBytesBefore != len(bug0289ProductionRequestBody) || metadata.BodyBytesAfter != len(result.Body) {
		t.Fatalf("body bytes before/after = %d/%d", metadata.BodyBytesBefore, metadata.BodyBytesAfter)
	}
}

// TestBug0289BuildCoreNotRecoverable：无可清理内容时保持既有 not_recoverable
// 诊断形状（Signal + Reason），供响应层落入终态文案分支。
func TestBug0289BuildCoreNotRecoverable(t *testing.T) {
	result := BuildCodexEncryptedContentRecoveryRetry([]byte(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[]}]}`), SignalEncryptedContextInvalid)
	if result.Action != RecoveryActionNotRecoverable {
		t.Fatalf("action = %q", result.Action)
	}
	if result.Signal != SignalEncryptedContextInvalid || result.Reason != RecoveryReasonNoRemovableEncryptedContent {
		t.Fatalf("signal/reason = %q/%q", result.Signal, result.Reason)
	}
}

// TestBug0289IsOpenAIResponsesRequest：/v1/responses 族判定包装（复用既有
// endpoint family 通道），chat 路径不命中。
func TestBug0289IsOpenAIResponsesRequest(t *testing.T) {
	responses := newTestRequest(t, "POST", "/v1/responses", nil, nil)
	if !IsOpenAIResponsesRequest(responses) {
		t.Fatal("/v1/responses must match the responses family")
	}
	chat := newTestRequest(t, "POST", "/v1/chat/completions", nil, nil)
	if IsOpenAIResponsesRequest(chat) {
		t.Fatal("/v1/chat/completions must not match the responses family")
	}
	if IsOpenAIResponsesRequest(nil) {
		t.Fatal("nil request must not match")
	}
	_ = gatewayopenai.FamilyResponses
}
