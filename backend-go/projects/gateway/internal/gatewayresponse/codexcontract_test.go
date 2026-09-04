package gatewayresponse

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
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
	if CodexCompactionExpectedForRequest(newReq("GET", "/v1/responses", &gatewaybody.BodyState{}, nil)) {
		t.Fatal("GET never expects compaction")
	}
	if !CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses/compact", &gatewaybody.BodyState{}, nil)) {
		t.Fatal("compact endpoint expects compaction")
	}
	if !CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses", &gatewaybody.BodyState{CodexCompactionTrigger: true}, nil)) {
		t.Fatal("compaction trigger body state expects compaction")
	}
	if !CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses", &gatewaybody.BodyState{}, map[string]any{
		"input": []any{map[string]any{"type": "compaction_trigger"}},
	})) {
		t.Fatal("parsed compaction trigger expects compaction")
	}
	if CodexCompactionExpectedForRequest(newReq("POST", "/v1/responses", &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON}, nil)) {
		t.Fatal("scanned json without trigger does not expect compaction")
	}
	if CodexCompactionExpectedForRequest(newReq("POST", "/v1/chat/completions", &gatewaybody.BodyState{}, nil)) {
		t.Fatal("chat endpoint never expects compaction")
	}
}

func TestRequestBodyHasCompactionTriggerRawScan(t *testing.T) {
	pattern := `"type": "compaction_trigger"`
	small := []byte(`{"model":"gpt-5","input":[` + pattern + `]}`)
	if !requestPathHasCompactionTrigger("/v1/responses", nil, small) {
		t.Fatal("small body scan finds trigger")
	}
	head := strings.Repeat("a", 128) + pattern + strings.Repeat("b", codexCompactionRawBodyScanEdgeBytes)
	if !requestPathHasCompactionTrigger("/v1/responses", nil, []byte(head)) {
		t.Fatal("head scan finds trigger inside prefix window")
	}
	tail := strings.Repeat("a", codexCompactionRawBodyScanEdgeBytes) + strings.Repeat("b", codexCompactionRawBodyScanEdgeBytes-30) + pattern
	if !requestPathHasCompactionTrigger("/v1/responses", nil, []byte(tail)) {
		t.Fatal("tail scan finds trigger inside suffix window")
	}
	middle := []byte(strings.Repeat("a", codexCompactionRawBodyScanEdgeBytes) + "xx" + pattern + "yy" + strings.Repeat("b", codexCompactionRawBodyScanEdgeBytes))
	if requestPathHasCompactionTrigger("/v1/responses", nil, middle) {
		t.Fatal("trigger outside both edge windows is not scanned")
	}
}

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
