package gatewayresponse

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaygemini"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// Gemini interactions 亲和写入接线（审计 P1）的响应层行为契约：
// create 在下游提交前回调（Node finalization.ts:483 流式 beforeDownstreamCommit /
// :1054 非流式 res.send 前 rememberGeminiInteractionBeforeDownstreamCommit）；
// 失败上抛 AffinityUnavailableError 阻断提交；资源 ID 经 inspector → convert →
// pipe 快照回带（p.responseResourceId → StreamPipeResult → handling）。

const interactionCreateSseChunk = "event: interaction.created\n" +
	"data: {\"type\":\"interaction.created\",\"interaction\":{\"id\":\"ix_sse\"}}\n" +
	"\n" +
	"event: interaction.completed\n" +
	"data: {\"type\":\"interaction.completed\",\"interaction\":{\"id\":\"ix_sse\",\"status\":\"completed\"}}\n" +
	"\n"

const interactionCreateJsonBody = `{"id":"ix_json","status":"completed","steps":[]}`

func interactionAffinityStreamInput(t *testing.T) (HandleUpstreamResponseInput, *httptest.ResponseRecorder) {
	t.Helper()
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(interactionCreateSseChunk)), 200, map[string]string{"Content-Type": "text/event-stream"})
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1beta/interactions", nil))
	input.Driver = NewGeminiResponseDriver()
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	return input, recorder
}

func interactionAffinityNonStreamInput(t *testing.T) (HandleUpstreamResponseInput, *httptest.ResponseRecorder) {
	t.Helper()
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(interactionCreateJsonBody)), 200, map[string]string{"Content-Type": "application/json"})
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1beta/interactions", nil))
	input.Driver = NewGeminiResponseDriver()
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	return input, recorder
}

// 流式 create：提交前回调收到 inspector 提取的 interaction ID，随后正常提交。
func TestInteractionAffinityStreamCreateBeforeCommit(t *testing.T) {
	input, recorder := interactionAffinityStreamInput(t)
	var hookIDs []string
	input.InteractionAffinityRemember = func(responseResourceId string) error {
		hookIDs = append(hookIDs, responseResourceId)
		if recorder.Body.Len() > 0 {
			t.Fatal("亲和写入必须先于下游提交")
		}
		return nil
	}
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(hookIDs) != 1 || hookIDs[0] != "ix_sse" {
		t.Fatalf("before-commit 回调 = %v，期望 [ix_sse]", hookIDs)
	}
	if result.ResponseResourceId != "ix_sse" {
		t.Fatalf("结果回带 = %q，期望 ix_sse", result.ResponseResourceId)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("响应应已提交")
	}
}

// 流式 create：亲和写失败 → 提交前钩子上抛 AffinityUnavailableError，下游未写。
func TestInteractionAffinityStreamCreateFailureBlocksCommit(t *testing.T) {
	input, recorder := interactionAffinityStreamInput(t)
	input.InteractionAffinityRemember = func(string) error {
		return &gatewaygemini.AffinityUnavailableError{Op: "remember", Err: errors.New("store down")}
	}
	_, err := HandleStreamUpstreamResponse(input)
	var affinityErr *gatewaygemini.AffinityUnavailableError
	if !errors.As(err, &affinityErr) {
		t.Fatalf("err = %v，期望 AffinityUnavailableError", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("失败时下游不应提交，已写 %d 字节", recorder.Body.Len())
	}
	if input.DownstreamCommitState.TransportCommitted {
		t.Fatal("失败时不应标记传输已提交")
	}
}

// 非流式 create：从解析后的响应体提取 ID 后、res.send 前回调。
func TestInteractionAffinityNonStreamCreateBeforeSend(t *testing.T) {
	input, recorder := interactionAffinityNonStreamInput(t)
	var hookIDs []string
	input.InteractionAffinityRemember = func(responseResourceId string) error {
		hookIDs = append(hookIDs, responseResourceId)
		if recorder.Body.Len() > 0 {
			t.Fatal("亲和写入必须先于 res.send")
		}
		return nil
	}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(hookIDs) != 1 || hookIDs[0] != "ix_json" {
		t.Fatalf("before-send 回调 = %v，期望 [ix_json]", hookIDs)
	}
	if !result.ProtocolValidatedSuccess {
		t.Fatalf("result = %+v", result)
	}
	if recorder.Body.String() != interactionCreateJsonBody {
		t.Fatalf("client body = %q", recorder.Body.String())
	}
}

// 非流式 create：亲和写失败 → 上抛且 res.send 未发生。
func TestInteractionAffinityNonStreamCreateFailureBlocksSend(t *testing.T) {
	input, recorder := interactionAffinityNonStreamInput(t)
	input.InteractionAffinityRemember = func(string) error {
		return &gatewaygemini.AffinityUnavailableError{Op: "remember", Err: errors.New("store down")}
	}
	_, err := HandleNonStreamUpstreamResponse(input)
	var affinityErr *gatewaygemini.AffinityUnavailableError
	if !errors.As(err, &affinityErr) {
		t.Fatalf("err = %v，期望 AffinityUnavailableError", err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("失败时 res.send 不应发生，已写 %d 字节", recorder.Body.Len())
	}
}

// DELETE interactions 204 空成功体放行（《Gemini账号接入.md》:189）：空体例外
// 在两道协议校验必须一致，第二道不得把空体误判「不是有效 JSON」502（存量缺陷回归）。
func TestInteractionAffinityDeleteEmptyBodyAllowed(t *testing.T) {
	input, recorder := newInputFixture(NewSliceUpstreamBody(), 204, nil)
	input.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("DELETE", "/v1beta/interactions/ix_1", nil))
	input.Driver = NewGeminiResponseDriver()
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}
	result, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "" {
		t.Fatalf("DELETE 204 空体应放行: %+v", result.ErrorPayload)
	}
	if recorder.Code != 204 {
		t.Fatalf("DELETE 204 应原样放行: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
