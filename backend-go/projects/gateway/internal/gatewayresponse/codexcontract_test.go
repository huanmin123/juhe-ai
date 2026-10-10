package gatewayresponse

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

func TestCountCodexCompactionOutputItemsFromJSON(t *testing.T) {
	parse := func(text string) any {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	counts := CountCodexCompactionOutputItemsFromJSON(parse(`{"output":[
		{"type":"compaction","encrypted_content":"abc"},
		{"type":"message"},
		{"type":"compaction_summary","encrypted_content":"def"}
	]}`))
	if counts == nil || counts.OutputItemCount != 3 || counts.CompactionItemCount != 2 {
		t.Fatalf("counts = %+v", counts)
	}
	if CountCodexCompactionOutputItemsFromJSON(parse(`{"choices":[]}`)) != nil {
		t.Fatal("non-responses payload has no counts")
	}
	if CountCodexCompactionOutputItemsFromJSON(parse(`{"output":{}}`)) != nil {
		t.Fatal("object output has no counts")
	}
}

func TestCodexCompactionContractMismatchFrame(t *testing.T) {
	if CodexCompactionContractMismatchFrame(CodexCompactionContractMismatchInput{
		OutputItemCount: 3, CompactionItemCount: 1, Transport: "json",
	}) != nil {
		t.Fatal("exactly one compaction item passes without force")
	}
	frame := CodexCompactionContractMismatchFrame(CodexCompactionContractMismatchInput{
		OutputItemCount: 3, CompactionItemCount: 0, Transport: "json",
	})
	if frame == nil {
		t.Fatal("zero compaction items should mismatch")
	}
	if frame.ErrorCode != CodexCompactionContractMismatchErrorCode {
		t.Fatalf("code = %q", frame.ErrorCode)
	}
	want := "Codex Remote Compaction V2 响应结构无效：期望恰好 1 个 compaction output item，实际 0 个，output item 总数 3 个"
	if frame.ErrorMessage != want {
		t.Fatalf("message = %q, want %q", frame.ErrorMessage, want)
	}
	if frame.Protocol != "openai_v1" || frame.EndpointFamily != gatewayproto.EndpointFamilyResponses {
		t.Fatalf("frame = %+v", frame)
	}
	sseFrame := CodexCompactionContractMismatchFrame(CodexCompactionContractMismatchInput{
		OutputItemCount: 1, CompactionItemCount: 0, Transport: "sse", EventType: "response.output_item.done",
	})
	if sseFrame == nil || sseFrame.RawText != "response.output_item.done" {
		t.Fatalf("sse frame = %+v", sseFrame)
	}
	forced := CodexCompactionContractMismatchFrame(CodexCompactionContractMismatchInput{
		OutputItemCount: 1, CompactionItemCount: 1, Transport: "json", Force: true,
	})
	if forced == nil {
		t.Fatal("force bypasses the exactly-one shortcut")
	}
}

func gatewayEndpointFamilyResponses() string { return "responses" }

// TestCodexCompactionExpectedForRequest 锁定请求侧压缩判定的唯一实现
// （gatewaycodex.CodexCompactionExpectedForRequest，调度内核通用化设计 5.2
// 三轨合一后本包不再保留同形拷贝）：经同一 GatewayRequest 输入形状验证
// /responses/compact 端点与 compaction_trigger 触发双条件。
func TestCodexCompactionExpectedForRequest(t *testing.T) {
	newReq := func(method, target string, bodyState *gatewaybody.BodyState, parsed map[string]any) *gatewaypreauth.GatewayRequest {
		req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest(method, target, nil))
		gatewayRequest := &gatewaybody.Request{State: bodyState}
		if parsed != nil {
			gatewayRequest.Body = parsed
		}
		req.Body = gatewayRequest
		return req
	}
	if gatewaycodex.CodexCompactionExpectedForRequest(newReq("GET", "/v1/responses", &gatewaybody.BodyState{}, nil)) {
		t.Fatal("GET never expects compaction")
	}
	if !gatewaycodex.CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses/compact", &gatewaybody.BodyState{}, nil)) {
		t.Fatal("compact endpoint expects compaction")
	}
	if !gatewaycodex.CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses", &gatewaybody.BodyState{CodexCompactionTrigger: true}, nil)) {
		t.Fatal("compaction trigger body state expects compaction")
	}
	if !gatewaycodex.CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses", &gatewaybody.BodyState{}, map[string]any{
		"input": []any{map[string]any{"type": "compaction_trigger"}},
	})) {
		t.Fatal("parsed compaction trigger expects compaction")
	}
	if gatewaycodex.CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses", &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON}, nil)) {
		t.Fatal("scanned json without trigger does not expect compaction")
	}
	if gatewaycodex.CodexCompactionExpectedForRequest(newReq("POST", "/v1/chat/completions", &gatewaybody.BodyState{}, nil)) {
		t.Fatal("chat endpoint never expects compaction")
	}
}

// requestPathHasCompactionTrigger / requestBodyHasCompactionTrigger 已随批次 2
// 遗留清理从 codexcontract.go 删除（唯一实现 gatewaycodex.
// CodexCompactionExpectedForRequest，raw 扫描窗口边界由 gatewaycodex 包
// TestCodexCompactionExpectedRawBodyEdgeWindows 锁定）；原直驱用例一并移除。

func TestCountCodexCompactionOutputItemsFromStreamEvent(t *testing.T) {
	event := gatewayopenai.ParsedStreamEvent{
		EventName: "response.output_item.done",
		Data: map[string]any{
			"item": map[string]any{"type": "compaction", "encrypted_content": "x"},
		},
	}
	counts := CountCodexCompactionOutputItemsFromStreamEvent(event)
	if counts == nil || counts.OutputItemCount != 1 || counts.CompactionItemCount != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	other := gatewayopenai.ParsedStreamEvent{EventName: "response.output_text.done"}
	if CountCodexCompactionOutputItemsFromStreamEvent(other) != nil {
		t.Fatal("non output_item.done has no counts")
	}
}
