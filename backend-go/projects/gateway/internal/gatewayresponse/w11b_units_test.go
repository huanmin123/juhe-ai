package gatewayresponse

// w11b 波次：nonstreamjson.go 协议校验分支、finalize.go 共享辅助、
// heartbeat / sink / models / streamresult / errors / codexcontract /
// inspection / precommit 等单元分支的覆盖补齐。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func w11bParseJSON(t *testing.T, text string) GatewayNonStreamJsonBody {
	t.Helper()
	return ParseGatewayNonStreamJsonBody(text, true, http.Header{"Content-Type": []string{"application/json"}})
}

func TestW11BProtocolValidatedNonStreamResponseFamilies(t *testing.T) {
	validJSON := func(text string) GatewayNonStreamJsonBody {
		body := w11bParseJSON(t, text)
		if body.Status != NonStreamJSONStatusValid {
			t.Fatalf("parse %q => %s", text, body.Status)
		}
		return body
	}
	// 状态码与有效性门。
	if ProtocolValidatedNonStreamResponse(validJSON(`{}`), 199, "unknown", "/x") {
		t.Fatal("1xx 不通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{}`), 300, "unknown", "/x") {
		t.Fatal("3xx 不通过")
	}
	invalid := GatewayNonStreamJsonBody{Status: NonStreamJSONStatusEmpty}
	if ProtocolValidatedNonStreamResponse(invalid, 200, "unknown", "/x") {
		t.Fatal("非 valid 不通过")
	}
	if ProtocolValidatedNonStreamResponse(GatewayNonStreamJsonBody{Status: NonStreamJSONStatusValid, Value: []any{}}, 200, "unknown", "/x") {
		t.Fatal("根节点非对象不通过")
	}
	// 顶层失败终态。
	if ProtocolValidatedNonStreamResponse(validJSON(`{"error":{"message":"m"}}`), 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("顶层 error 不通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{"type":"error"}`), 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("type=error 不通过")
	}
	// 管理路径跳过顶层失败判定。
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"error":{"message":"m"},"id":"f1"}`), 200, "unknown", "/v1/files/f1/content") {
		t.Fatal("管理资源路径应跳过顶层失败判定")
	}

	// models 路径短路。
	modelsBody := validJSON(`{"data":[{"id":"m1"}]}`)
	if !ProtocolValidatedNonStreamResponse(modelsBody, 200, "unknown", "/v1/models") {
		t.Fatal("/v1/models + data 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"name":"m1"}`), 200, "unknown", "/v1beta/models") {
		t.Fatal("/v1beta/models + name 通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{}`), 200, "unknown", "/v1/models") {
		t.Fatal("/v1/models 空对象不通过")
	}

	// chat_completions。
	chatOK := validJSON(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	if !ProtocolValidatedNonStreamResponse(chatOK, 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("chat 有效 choice 通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{"choices":[{"message":{"error":{"code":"x"}}}]}`), 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("message.error 不通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"choices":[{"text":"legacy"}]}`), 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("text choice 通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{"choices":[]}`), 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("空 choices 不通过")
	}

	// responses。
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"object":"response","id":"r1","output":[]}`), 200, "responses", "/v1/responses") {
		t.Fatal("responses 有效通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{"object":"response","id":"r1","output":[],"status":"failed"}`), 200, "responses", "/v1/responses") {
		t.Fatal("responses failed 不通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{"object":"response","output":[]}`), 200, "responses", "/v1/responses") {
		t.Fatal("responses 缺 id 不通过")
	}

	// messages / models family / token counting。
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"type":"message","content":[{"type":"text"}]}`), 200, "messages", "/v1/messages") {
		t.Fatal("messages 通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{"type":"message"}`), 200, "messages", "/v1/messages") {
		t.Fatal("messages 缺 content 不通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"models":[{"id":"m"}]}`), 200, "models", "/v1/models") {
		t.Fatal("models family 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"input_tokens":3}`), 200, "message_token_counting", "/v1/messages/count_tokens") {
		t.Fatal("token counting 通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{}`), 200, "message_token_counting", "/v1/messages/count_tokens") {
		t.Fatal("token counting 缺字段不通过")
	}

	// gemini 族。
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"candidates":[]}`), 200, "generate_content", "/v1beta/models/g:generateContent") {
		t.Fatal("generate_content 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"promptFeedback":{}}`), 200, "stream_generate_content", "/x") {
		t.Fatal("promptFeedback 通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{}`), 200, "generate_content", "/x") {
		t.Fatal("gemini 空不通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"totalTokens":5}`), 200, "count_tokens", "/x") {
		t.Fatal("count_tokens 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"embedding":{}}`), 200, "embed_content", "/x") {
		t.Fatal("embed_content 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"embeddings":[{}]}`), 200, "embed_content", "/x") {
		t.Fatal("embeddings 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"id":"i1"}`), 200, "interactions", "/v1/interactions") {
		t.Fatal("interactions 通过")
	}

	// unknown 族。
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"data":[{}]}`), 200, "unknown", "/v1/embeddings") {
		t.Fatal("embeddings path 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"results":[{}]}`), 200, "unknown", "/v1/moderations") {
		t.Fatal("moderations path 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"text":"hi"}`), 200, "unknown", "/v1/audio/transcriptions") {
		t.Fatal("audio path 通过")
	}
	if !ProtocolValidatedNonStreamResponse(validJSON(`{"id":"f1"}`), 200, "unknown", "/v1/files") {
		t.Fatal("files path 通过")
	}
	if ProtocolValidatedNonStreamResponse(validJSON(`{}`), 200, "unknown", "/v1/other") {
		t.Fatal("unknown 其余路径不通过")
	}
}

func TestW11BValidateBufferedJsonProtocolResponseFamilies(t *testing.T) {
	valid := func(text string) GatewayNonStreamJsonBody { return w11bParseJSON(t, text) }
	// 非 2xx 与超限。
	if ValidateBufferedJsonProtocolResponse(GatewayNonStreamJsonBody{}, false, false, "unknown", "/x") != nil {
		t.Fatal("非 OK 不校验")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, true, "unknown", "/x"); failure == nil || !strings.Contains(failure.Message, "上限") {
		t.Fatalf("limit failure = %+v", failure)
	}
	if failure := ValidateBufferedJsonProtocolResponse(GatewayNonStreamJsonBody{Status: NonStreamJSONStatusNotJSON}, true, false, "unknown", "/x"); failure == nil {
		t.Fatal("not_json 应失败")
	}
	if failure := ValidateBufferedJsonProtocolResponse(GatewayNonStreamJsonBody{Status: NonStreamJSONStatusValid, Value: "str"}, true, false, "unknown", "/x"); failure == nil {
		t.Fatal("根节点非对象应失败")
	}
	// 顶层失败终态（带/不带消息、responses 家族豁免）。
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"error":{"message":"boom"}}`), true, false, "chat_completions", "/x"); failure == nil || !strings.Contains(failure.Message, "boom") {
		t.Fatalf("error message failure = %+v", failure)
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"status":"failed"}`), true, false, "chat_completions", "/x"); failure == nil || !strings.Contains(failure.Message, "失败终态") {
		t.Fatalf("status failed = %+v", failure)
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"error":{"message":"x"}}`), true, false, "responses", "/x"); failure == nil || strings.Contains(failure.Message, "失败终态") {
		t.Fatalf("responses 家族豁免顶层失败判定，failure = %+v", failure)
	}
	// chat 无效 choice。
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"choices":[{"bad":1}]}`), true, false, "chat_completions", "/x"); failure == nil {
		t.Fatal("chat 无效 choice")
	}
	if ValidateBufferedJsonProtocolResponse(valid(`{"choices":[{"text":"ok"}]}`), true, false, "chat_completions", "/x") != nil {
		t.Fatal("chat 有效 choice 通过")
	}
	// messages / models。
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"type":"message","content":[]}`), true, false, "messages", "/x"); failure == nil {
		t.Fatal("messages 空 content")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "models", "/x"); failure == nil {
		t.Fatal("models 空对象")
	}
	if ValidateBufferedJsonProtocolResponse(valid(`{"object":"model"}`), true, false, "models", "/x") != nil {
		t.Fatal("models object=model 通过")
	}
	// message_token_counting / count_tokens / embed_content / interactions。
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "message_token_counting", "/x"); failure == nil {
		t.Fatal("token counting 缺字段")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "count_tokens", "/x"); failure == nil {
		t.Fatal("count_tokens 缺字段")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "embed_content", "/x"); failure == nil {
		t.Fatal("embed_content 缺字段")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "interactions", "/x"); failure == nil {
		t.Fatal("interactions 缺字段")
	}
	if ValidateBufferedJsonProtocolResponse(valid(`{"name":"i1"}`), true, false, "interactions", "/x") != nil {
		t.Fatal("interactions name 通过")
	}
	// gemini。
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "generate_content", "/x"); failure == nil {
		t.Fatal("gemini 空对象")
	}
	if ValidateBufferedJsonProtocolResponse(valid(`{"promptFeedback":{}}`), true, false, "stream_generate_content", "/x") != nil {
		t.Fatal("gemini promptFeedback 通过")
	}
	// unknown 族路径。
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "unknown", "/v1/embeddings"); failure == nil {
		t.Fatal("embeddings data 必须数组")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "unknown", "/v1/moderations"); failure == nil {
		t.Fatal("moderations results 必须数组")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "unknown", "/v1/audio/transcriptions"); failure == nil {
		t.Fatal("audio 缺 text")
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{}`), true, false, "unknown", "/v1/files/abc"); failure == nil {
		t.Fatal("files 缺 id/data")
	}
	if ValidateBufferedJsonProtocolResponse(valid(`{"id":"f"}`), true, false, "unknown", "/v1/files/abc") != nil {
		t.Fatal("files id 通过")
	}
	// responses failed 终态。
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"status":"failed","error":{"message":"resp boom"}}`), true, false, "responses", "/x"); failure == nil || !strings.Contains(failure.Message, "resp boom") {
		t.Fatalf("responses failed = %+v", failure)
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"status":"failed"}`), true, false, "responses", "/x"); failure == nil || failure.ErrorCode != "upstream_protocol_failure" {
		t.Fatalf("responses failed no message = %+v", failure)
	}
	if failure := ValidateBufferedJsonProtocolResponse(valid(`{"object":"response"}`), true, false, "responses", "/x"); failure == nil {
		t.Fatal("responses 缺结构")
	}
	if ValidateBufferedJsonProtocolResponse(valid(`{"object":"response","id":"r","output":[]}`), true, false, "responses", "/x") != nil {
		t.Fatal("responses 完整通过")
	}
}

func TestW11BValidateChatChoiceHelper(t *testing.T) {
	if isValidChatCompletionChoice("not-object") {
		t.Fatal("非对象不通过")
	}
	if isValidChatCompletionChoice(map[string]any{"error": map[string]any{}}) {
		t.Fatal("choice.error 不通过")
	}
	if !isValidChatCompletionChoice(map[string]any{"message": map[string]any{"content": "ok"}}) {
		t.Fatal("message 通过")
	}
	if isValidChatCompletionChoice(map[string]any{"message": map[string]any{"error": map[string]any{}}}) {
		t.Fatal("message.error 不通过")
	}
	if !isValidChatCompletionChoice(map[string]any{"text": "t"}) {
		t.Fatal("text 通过")
	}
	if isValidChatCompletionChoice(map[string]any{}) {
		t.Fatal("空 choice 不通过")
	}
}

// ---- finalize.go 共享辅助 ----

func TestW11BFinalizeHelpers(t *testing.T) {
	input, _ := newInputFixture(nil, 200, nil)
	if input.nowMs() == nil || input.logger() == nil || input.driver() == nil {
		t.Fatal("缺省辅助必须可用")
	}
	withDeps, _ := newInputFixture(nil, 200, nil)
	withDeps.Deps = &FinalizationDeps{NowMs: func() int64 { return 42 }, Logger: nopStreamLogger{}}
	if withDeps.nowMs()() != 42 {
		t.Fatal("注入时钟未生效")
	}
	if withDeps.logger() == nil {
		t.Fatal("注入 logger 未生效")
	}
	// nowMsOf 同语义。
	if nowMsOf(&withDeps)() != 42 {
		t.Fatal("nowMsOf 注入未生效")
	}
	if nowMsOf(&input) == nil {
		t.Fatal("nowMsOf 缺省必须可用")
	}

	// filterManagementPolicies。
	policies := []gatewayruntimecache.ResponseInspectionPolicySummary{
		{ID: "default_openai_transient_precommit_error", DefaultRule: true},
		{ID: "default_openai_context_window_error", DefaultRule: true},
		{ID: "user-policy-1"},
	}
	filtered := filterManagementPolicies(policies, nil)
	if len(filtered) != 2 {
		t.Fatalf("filtered = %+v", filtered)
	}
	if len(filterManagementPolicies(policies, &ClientStrategyView{InterpretSemantics: true})) != 3 {
		t.Fatal("interpretSemantics 保留全量")
	}

	// firstByteBudgeted / timeoutsWithDisabled。
	budgeted, _ := newInputFixture(nil, 200, nil)
	value := int64(1000)
	budgeted.FirstByteTimeoutMs = &value
	if !budgeted.firstByteBudgeted() {
		t.Fatal("配置了首字超时应预算")
	}
	budgeted.TimeoutProfile.TimeoutsDisabled = true
	if budgeted.firstByteBudgeted() {
		t.Fatal("禁用超时后不预算")
	}
	if timeoutsWithDisabled(TimeoutProfile{TimeoutsDisabled: true}, &value) != nil {
		t.Fatal("禁用时返回 nil")
	}
	if timeoutsWithDisabled(TimeoutProfile{}, &value) == nil {
		t.Fatal("未禁用时保留原值")
	}

	// usageWithObservedModel / inspectionUpstreamErrorCode。
	usage := usageWithObservedModel(gatewayproto.EmptyUsage(), "observed-model")
	if usage.UpstreamResponseModel != "observed-model" {
		t.Fatalf("usage = %+v", usage)
	}
	if usageWithObservedModel(gatewayproto.EmptyUsage(), "").UpstreamResponseModel != "" {
		t.Fatal("空观察模型不覆盖")
	}
	result := StreamPipeResult{}
	if result.inspectionUpstreamErrorCode() != "" {
		t.Fatal("无决策时为空")
	}
	result.ResponseInspection = &ResponseInspectionDecision{UpstreamErrorCode: "code-x"}
	if result.inspectionUpstreamErrorCode() != "code-x" {
		t.Fatal("决策错误码未透出")
	}

	// bodyTextForSnapshot / stringOrBytes。
	if bodyTextForSnapshot(StreamPipeResult{BodyOmission: &StreamBodyOmissionSummary{}, ResponseBodyText: "x"}) != "" {
		t.Fatal("省略时快照正文为空")
	}
	if bodyTextForSnapshot(StreamPipeResult{ResponseBodyText: "x"}) != "x" {
		t.Fatal("正文透出")
	}
	if stringOrBytes(nil, nil) != "" || stringOrBytes([]byte("a"), []byte("b")) != "a" || stringOrBytes(nil, []byte("b")) != "b" {
		t.Fatal("stringOrBytes 语义")
	}

	// usageRequestSnapshotWithOmission。
	view := usageRequestSnapshotWithOmission(usageContextFixture(), &StreamBodyOmissionSummary{Reason: "image_stream_payload"})
	if !view.OmittedBody || view.BodyOmission == nil {
		t.Fatalf("view = %+v", view)
	}
	view = usageRequestSnapshotWithOmission(usageContextFixture(), nil)
	if view.OmittedBody || view.BodyOmission != nil {
		t.Fatalf("view = %+v", view)
	}
}

func TestW11BSuccessfulEmptyUpstreamAllowed(t *testing.T) {
	input, _ := newInputFixture(nil, 204, nil)
	if input.successfulEmptyUpstreamAllowed() {
		t.Fatal("POST /v1/chat/completions 不允许空成功体")
	}
	deleteReq, _ := newInputFixture(nil, 204, nil)
	deleteReq.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("DELETE", "/v1beta/interactions/i-1", nil))
	deleteReq.Driver = NewGeminiResponseDriver()
	if !deleteReq.successfulEmptyUpstreamAllowed() {
		t.Fatal("DELETE interactions 资源允许空成功体")
	}
	wrongPath, _ := newInputFixture(nil, 204, nil)
	wrongPath.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("DELETE", "/v1/interactions", nil))
	if wrongPath.successfulEmptyUpstreamAllowed() {
		t.Fatal("非资源路径不允许")
	}
	nilReq, _ := newInputFixture(nil, 204, nil)
	nilReq.Req = nil
	if nilReq.successfulEmptyUpstreamAllowed() {
		t.Fatal("nil 请求不允许")
	}
}

// ---- HandleStreamUpstreamResponse 空体与中断分支 ----

func TestW11BHandleStreamUpstreamResponseEmptyBodies(t *testing.T) {
	// 204 非 interactions → 空协议失败。
	input, _ := newInputFixture(nil, 204, nil)
	input.Deps = &FinalizationDeps{UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{}}
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "upstream_protocol_failure" {
		t.Fatalf("result = %+v", result)
	}
	// DELETE interactions 204 → 允许空成功。
	input2, _ := newInputFixture(nil, 204, nil)
	input2.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("DELETE", "/v1beta/interactions/i-1", nil))
	input2.Driver = NewGeminiResponseDriver()
	input2.Deps = input.Deps
	result, err = HandleStreamUpstreamResponse(input2)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "" {
		t.Fatalf("result = %+v", result)
	}
	// nil body 非 204 → 空透传成功。
	input3, _ := newInputFixture(nil, 200, nil)
	input3.Deps = input.Deps
	result, err = HandleStreamUpstreamResponse(input3)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorPayload.Code != "" {
		t.Fatalf("result = %+v", result)
	}
}

func TestW11BHandleStreamUpstreamResponseAbortRecords(t *testing.T) {
	// 客户端断开：管道返回 abort 错误，记录 usage/审计后透传错误。
	input, _ := newInputFixture(newChanBody(), 200, nil)
	usage := &mockUsageRecords{}
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: effects, NowMs: func() int64 { return 1000 }}
	signal := make(chan struct{})
	close(signal)
	input.Signal = chanSignal{ch: signal}
	_, err := HandleStreamUpstreamResponse(input)
	if err == nil || !IsUpstreamRequestAbortedError(err) {
		t.Fatalf("err = %v", err)
	}
	if len(usage.completed) != 1 || usage.completed[0].ErrorMessage != DownstreamConnectionClosedMessage {
		t.Fatalf("completed = %+v", usage.completed)
	}
	if len(effects.affinity) != 1 {
		t.Fatalf("affinity = %v", effects.affinity)
	}
}

func TestW11BHandleStreamUpstreamResponseUsageFallbackEstimated(t *testing.T) {
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(chatFinishChunk), []byte(chatDoneChunk)), 200, nil)
	input.Deps = &FinalizationDeps{
		UsageRecords: &mockUsageRecords{}, AccountEffects: &mockAccountEffects{},
		NowMs: func() int64 { return 1000 },
		UsageFallback: func(driver ResponseDriverPort, usage gatewayproto.ParsedUsage, fallbackInput UsageFallbackInput) (gatewayproto.ParsedUsage, bool, *int, *int) {
			estimated := 12
			usage.OutputTokens = &estimated
			return usage, true, nil, &estimated
		},
	}
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

// ---- writePreCommitStreamFailureToClient ----

func TestW11BWritePreCommitStreamFailureToClient(t *testing.T) {
	input, recorder := newInputFixture(nil, 200, nil)
	input.ClientStrategy = &ClientStrategyView{DownstreamProtocol: "responses_sse"}
	pipeResult := StreamPipeResult{Message: "上游失败", ErrorCode: "upstream_protocol_failure", UncommittedResponseBody: []byte("data: partial\n\n")}
	body := writePreCommitStreamFailureToClient(&input, pipeResult, input.Driver)
	if body == nil || !strings.Contains(string(body), "partial") || !strings.Contains(string(body), "response.failed") {
		t.Fatalf("body = %q", body)
	}
	if recorder.Body.Len() == 0 {
		t.Fatal("应写出未提交正文与失败事件")
	}
	// SemanticCommitted → 不写。
	committed := StreamPipeResult{SemanticCommitted: true, Message: "m", ErrorCode: "c"}
	if writePreCommitStreamFailureToClient(&input, committed, input.Driver) != nil {
		t.Fatal("语义已提交时不补写")
	}
	// 无 errorCode → 不写。
	noCode := StreamPipeResult{Message: "m"}
	if writePreCommitStreamFailureToClient(&input, noCode, input.Driver) != nil {
		t.Fatal("无错误码时不补写")
	}
	// 无 chunks（无未提交正文且协议不生成事件）→ nil。
	plain := StreamPipeResult{Message: "m", ErrorCode: "c"}
	plainInput, plainRecorder := newInputFixture(nil, 200, nil)
	plainInput.ClientStrategy = &ClientStrategyView{DownstreamProtocol: "chat_completions_sse"}
	if got := writePreCommitStreamFailureToClient(&plainInput, plain, plainInput.Driver); got != nil {
		t.Fatalf("无事件时 = %q", got)
	}
	_ = plainRecorder
}

// ---- heartbeat ----

func TestW11BHeartbeatHelpers(t *testing.T) {
	deps := HeartbeatDeps{}
	if deps.signalDone() != nil {
		t.Fatal("nil signal Done 为 nil")
	}
	// signalDone 透传 signal channel。
	canceled, cancelSignal := context.WithCancel(context.Background())
	cancelSignal()
	deps.Signal = canceled
	if deps.signalDone() == nil {
		t.Fatal("signal Done 应透传")
	}
	// heartbeatAborted。
	ctxCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !heartbeatAborted(deps, ctxCanceled) {
		t.Fatal("ctx 取消即中止")
	}
	commit := &DownstreamCommitState{}
	deps2 := HeartbeatDeps{DownstreamCommit: commit}
	if heartbeatAborted(deps2, context.Background()) {
		t.Fatal("未提交不应中止")
	}
	commit.MarkSemanticCommitted(1)
	if !heartbeatAborted(deps2, context.Background()) {
		t.Fatal("语义提交后中止")
	}
	// writeHeartbeatChunk 首写补齐 SSE 头。
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	deps3 := HeartbeatDeps{Res: tracking}
	if !writeHeartbeatChunk(deps3, []byte(": keep-alive\n\n")) {
		t.Fatal("首写应成功")
	}
	if recorder.Code != 200 || recorder.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("headers: code=%d ct=%q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	// 语义提交后不写。
	deps3.DownstreamCommit = &DownstreamCommitState{}
	deps3.DownstreamCommit.MarkSemanticCommitted(1)
	if writeHeartbeatChunk(deps3, []byte("x")) {
		t.Fatal("语义提交后不应写出")
	}
}

// ---- sink / models 辅助 ----

func TestW11BSinkAndModelsHelpers(t *testing.T) {
	bare := NewSink(SinkDeps{})
	if bare.observeCompletion(nil) != nil {
		t.Fatal("无完成观察器时为 nil")
	}
	if bare.loadCatalog("sys", nil) != nil {
		t.Fatal("无目录加载器时为 nil")
	}
	sink, _, _, _, _, _ := newSinkFixture()
	_ = sink
	// gatewayErrorProtocolForRequest。
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/messages", nil))
	if gatewayErrorProtocolForRequest(req) != gatewaypreauth.GatewayErrorProtocolAnthropic {
		t.Fatal("messages 路径为 anthropic 协议")
	}
	geminiReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1beta/models/g:generateContent", nil))
	if gatewayErrorProtocolForRequest(geminiReq) != gatewaypreauth.GatewayErrorProtocolGemini {
		t.Fatal("gemini 路径为 gemini 协议")
	}
	if gatewayErrorProtocolForRequest(nil) != gatewaypreauth.GatewayErrorProtocolOpenAI {
		t.Fatal("缺省 openai 协议")
	}
	// normalizedProviderCodeList / modelsUsageProviderCode。
	list := normalizedProviderCodeList([]string{" OpenAI ", "openai", "", "Anthropic"})
	if len(list) != 2 || list[0] != "openai" || list[1] != "anthropic" {
		t.Fatalf("list = %v", list)
	}
	if modelsUsageProviderCode(nil, "fallback") != "fallback" {
		t.Fatal("fallback 生效")
	}
	if modelsUsageProviderCode(nil, "") != "openai_compatible" {
		t.Fatal("缺省 openai_compatible")
	}
	if modelsUsageProviderCode([]string{"anthropic"}, "") != "anthropic" {
		t.Fatal("首项优先")
	}
	// hasNonEmptyQueryParam / isOpenAIModelsRequest。
	queryReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models?limit=3", nil))
	if !hasNonEmptyQueryParam(queryReq, "limit") || hasNonEmptyQueryParam(queryReq, "after") {
		t.Fatal("query 参数语义")
	}

	if !isOpenAIModelsRequest(queryReq) {
		t.Fatal("GET /v1/models 是 models 请求")
	}
	postReq := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/models", nil))
	if isOpenAIModelsRequest(postReq) {
		t.Fatal("POST /v1/models 不是 models 请求")
	}
	if isOpenAIModelsRequest(nil) {
		t.Fatal("nil 请求 false")
	}
}

// ---- streamresult / errors / failurestatus ----

func TestW11BStreamResultAndErrorsHelpers(t *testing.T) {
	// asStartedTransport 解包 wrapped 错误。
	inner := &StartedBodyTransportError{Err: errors.New("reset"), Name: "ReadError", Code: "ECONNRESET"}
	wrapped := fmt.Errorf("layer: %w", inner)
	var target *StartedBodyTransportError
	if !asStartedTransport(wrapped, &target) || target.Code != "ECONNRESET" {
		t.Fatal("wrapped transport 解包失败")
	}
	if asStartedTransport(nil, &target) {
		t.Fatal("nil 错误不解包")
	}
	// ResponsePrecommitDeadlineErrorOf。
	deadline := &ResponsePrecommitDeadlineError{DeadlineAtMs: 5000}
	if ResponsePrecommitDeadlineErrorOf(nil) != nil {
		t.Fatal("nil 错误 nil")
	}
	if got := ResponsePrecommitDeadlineErrorOf(deadline); got != deadline {
		t.Fatalf("direct = %v", got)
	}
	wrappedPipe := &NonStreamBodyPipeError{OriginalError: deadline}
	if got := ResponsePrecommitDeadlineErrorOf(wrappedPipe); got == nil {
		t.Fatal("pipe 包装应还原 deadline")
	}
	wrappedOther := &NonStreamBodyPipeError{OriginalError: errors.New("x")}
	if ResponsePrecommitDeadlineErrorOf(wrappedOther) != nil {
		t.Fatal("其它原始错误 nil")
	}
	// appendStringByte 截断与空上下文。
	tracker := NewResponsesRootStatusTracker()
	tracker.appendStringByte('a') // stringContext 为空时直接返回
	if len(tracker.stringRaw) != 0 {
		t.Fatal("空上下文不捕获")
	}
}

// ---- codexcontract ----

func TestW11BCodexCompactionTriggerHelpers(t *testing.T) {
	if jsonValueHasCompactionTrigger(nil, 0) {
		t.Fatal("标量默认 false")
	}
	if jsonValueHasCompactionTrigger(map[string]any{"type": "compaction_trigger"}, 0) != true {
		t.Fatal("compaction_trigger 命中")
	}
	nested := map[string]any{"items": []any{map[string]any{"type": "other"}, map[string]any{"type": "compaction_trigger"}}}
	if !jsonValueHasCompactionTrigger(nested, 0) {
		t.Fatal("嵌套命中")
	}
	if jsonValueHasCompactionTrigger(map[string]any{"a": 1}, 9) {
		t.Fatal("深度超限 false")
	}
	// requestPathHasCompactionTrigger。
	if requestPathHasCompactionTrigger("/v1/chat/completions", nil, nil) {
		t.Fatal("非 /responses 路径 false")
	}
	scanned := &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON}
	if requestPathHasCompactionTrigger("/v1/responses", scanned, []byte(`{"type":"compaction_trigger"}`)) {
		t.Fatal("scanned 状态且无标记时 false")
	}
	triggered := &gatewaybody.BodyState{CodexCompactionTrigger: true}
	if !requestPathHasCompactionTrigger("/v1/responses", triggered, nil) {
		t.Fatal("已标记触发 true")
	}
	if !requestPathHasCompactionTrigger("/v1/responses", nil, []byte(`{"type":"compaction_trigger"}`)) {
		t.Fatal("小正文扫描命中")
	}
	if requestPathHasCompactionTrigger("/v1/responses", nil, []byte(`{}`)) {
		t.Fatal("无匹配 false")
	}
	if requestPathHasCompactionTrigger("/v1/responses", nil, nil) {
		t.Fatal("空正文 false")
	}
	// 大正文边缘扫描（prefix 命中）。
	big := []byte(`{"instructions":"` + strings.Repeat("x", codexCompactionRawBodyScanEdgeBytes) + `","type":"compaction_trigger"}`)
	if !requestPathHasCompactionTrigger("/v1/responses", nil, big) {
		t.Fatal("尾部扫描命中")
	}
}

// ---- inspection / precommit 纯辅助 ----

func TestW11BInspectionAndPrecommitHelpers(t *testing.T) {
	if orOpenAIProtocol("") != "openai" || orOpenAIProtocol("anthropic") != "anthropic" {
		t.Fatal("orOpenAIProtocol 语义")
	}
	if hasJSONPathMeaningfulValue(nil) || hasJSONPathMeaningfulValue(false) || hasJSONPathMeaningfulValue("  ") || hasJSONPathMeaningfulValue([]any{}) || hasJSONPathMeaningfulValue(map[string]any{}) {
		t.Fatal("空语义值 false")
	}
	if !hasJSONPathMeaningfulValue(true) || !hasJSONPathMeaningfulValue("x") || !hasJSONPathMeaningfulValue([]any{1}) || !hasJSONPathMeaningfulValue(3.14) {
		t.Fatal("非空语义值 true")
	}
	if !stringSliceContains([]string{"a", "b"}, "b") || stringSliceContains([]string{"a"}, "z") {
		t.Fatal("stringSliceContains 语义")
	}
	// precommit evidence：CRLF、注释、data 事件。
	evidence := NewStreamPreCommitSseEvidence()
	evidence.Push([]byte(": comment\r\n\r\n"))
	if !evidence.OnlyNonSemanticFramingObserved || evidence.DataEventObserved {
		t.Fatal("纯注释保持非语义")
	}
	evidence.Push([]byte("data: hello\n\n"))
	if !evidence.DataEventObserved || !evidence.DataPayloadStarted {
		t.Fatalf("data 事件: observed=%v started=%v", evidence.DataEventObserved, evidence.DataPayloadStarted)
	}
	evidence2 := NewStreamPreCommitSseEvidence()
	evidence2.Push([]byte("data:payload"))
	evidence2.Finish()
	if !evidence2.DataEventObserved || !evidence2.DataPayloadStarted {
		t.Fatal("无换行 data 经 Finish 归档")
	}
	// 非语义字段（event/id）行。
	evidence3 := NewStreamPreCommitSseEvidence()
	evidence3.Push([]byte("event: x\nid: 1\n\n"))
	if !evidence3.OnlyNonSemanticFramingObserved || evidence3.DataEventObserved {
		t.Fatal("纯元字段保持非语义")
	}
}

// ---- nonstream 协议校验门 ----

func TestW11BNonStreamJsonProtocolValidationAllowed(t *testing.T) {
	input, _ := newInputFixture(nil, 200, nil)
	if !nonStreamJsonProtocolValidationAllowed(input, gatewayproto.EndpointFamilyChatCompletions) {
		t.Fatal("已知 endpoint family 允许校验")
	}
	modelsInput, _ := newInputFixture(nil, 200, nil)
	modelsInput.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("GET", "/v1/models", nil))
	if !nonStreamJsonProtocolValidationAllowed(modelsInput, gatewayproto.EndpointFamilyUnknown) {
		t.Fatal("已知 JSON 请求路径允许校验")
	}
	otherPath, _ := newInputFixture(nil, 200, nil)
	otherPath.Req = gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions/unknown-endpoint", nil))
	if nonStreamJsonProtocolValidationAllowed(otherPath, gatewayproto.EndpointFamilyUnknown) {
		t.Fatal("unknown 家族且未知路径不允许")
	}
	notOK, _ := newInputFixture(nil, 500, nil)
	if nonStreamJsonProtocolValidationAllowed(notOK, gatewayproto.EndpointFamilyChatCompletions) {
		t.Fatal("非 2xx 不校验")
	}
}

func TestW11BIsOpenAIContentHelpers(t *testing.T) {
	if !IsOpenAIJsonResponseContentType("application/json; charset=utf-8") || !IsOpenAIJsonResponseContentType("application/vnd.x+json") {
		t.Fatal("JSON 内容类型判定")
	}
	if IsOpenAIJsonResponseContentType("text/event-stream") {
		t.Fatal("SSE 非 JSON")
	}
	if !IsOpenAIBinaryResponseContentType("image/png") || !IsOpenAIBinaryResponseContentType("application/pdf") || !IsOpenAIBinaryResponseContentType("audio/wav") {
		t.Fatal("二进制判定")
	}
	if IsOpenAIBinaryResponseContentType("application/json") {
		t.Fatal("JSON 非二进制")
	}
	if !ShouldHandleOpenAIUpstreamResponseAsStream("text/event-stream", false) {
		t.Fatal("SSE 恒流式")
	}
	if ShouldHandleOpenAIUpstreamResponseAsStream("application/json", false) {
		t.Fatal("非流式请求 JSON 非流式")
	}
	if ShouldHandleOpenAIUpstreamResponseAsStream("application/json", true) {
		t.Fatal("流式请求 JSON 响应仍非流式")
	}
	if ShouldHandleOpenAIUpstreamResponseAsStream("application/pdf", true) {
		t.Fatal("二进制非流式")
	}
	if !ShouldHandleOpenAIUpstreamResponseAsStream("text/plain", true) {
		t.Fatal("流式请求的未知类型按流式处理")
	}
}
