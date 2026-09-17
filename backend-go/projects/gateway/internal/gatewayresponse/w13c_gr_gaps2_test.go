package gatewayresponse

// w13c 覆盖率补充测试（第三批）：决策墙钟、pipeError 归一化、finalize 分支、
// 检查策略匹配与非流式 JSON 判定。

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---- streamread：首字节 deadline 与 precommit 墙钟决策 ----

func TestW13CDecidePrecommitDeadlineArms(t *testing.T) {
	// decide 以 pipe 时钟判定 precommit 是否已到点：三段统一 100_000，
	// 与 pendingRead 时钟一致；若沿用助手默认 1_000，fired 场景会退化成
	// 真实等待近百秒的计时器竞速。
	// 墙钟已到（fired）+ pending 未 settle：precommit 胜出并通知 superseded。
	pipe, _ := w11bNewFinalPipe(t, w13cNowMs(100_000))
	pastDeadline := int64(99_000)
	pipe.options.ResponsePrecommitDeadlineAtMs = &pastDeadline
	superseded := 0
	pipe.options.OnFirstByteDeadlineSuperseded = func() { superseded++ }
	unsettled := newPendingRead(make(chan ChunkResult), func() int64 { return 100_000 })
	decision := pipe.decideFirstByteDeadlineAfterPendingRead(unsettled, FirstByteDeadlineInput{})
	if !decision.precommitDeadline || decision.read {
		t.Fatalf("fired+unsettled = %+v", decision)
	}
	if superseded != 1 {
		t.Fatalf("superseded notifications = %d", superseded)
	}

	// 墙钟未到 + pending 在墙钟前 settle：读取成功。
	futureDeadline := int64(100_050)
	pipe2, _ := w11bNewFinalPipe(t, w13cNowMs(100_000))
	pipe2.options.ResponsePrecommitDeadlineAtMs = &futureDeadline
	settled := newPendingRead(singleChunkChannel(ChunkResult{Data: []byte("x")}), func() int64 { return 100_000 })
	time.Sleep(50 * time.Millisecond)
	resolved := pipe2.decideFirstByteDeadlineAfterPendingRead(settled, FirstByteDeadlineInput{})
	if !resolved.read {
		t.Fatalf("future+settled = %+v", resolved)
	}

	// 墙钟未到但定时器先触发（chunk 未到达）：precommit 胜出（80ms 计时器）。
	pipe3, _ := w11bNewFinalPipe(t, w13cNowMs(100_000))
	pipe3.options.ResponsePrecommitDeadlineAtMs = w13cInt64(100_080)
	never := newPendingRead(make(chan ChunkResult), func() int64 { return 100_000 })
	decision3 := pipe3.decideFirstByteDeadlineAfterPendingRead(never, FirstByteDeadlineInput{})
	if !decision3.precommitDeadline || decision3.read {
		t.Fatalf("timer must win with a late chunk: %+v", decision3)
	}

	// precommit 剩余时间非法（已过期）→ 读取前直接报错。
	expired, _ := w11bNewFinalPipe(t, w13cNowMs(100_000))
	expired.options.ResponsePrecommitDeadlineAtMs = w13cInt64(99_000)
	if _, err := expired.readNextStreamChunk(); err == nil {
		t.Fatal("expired precommit must fail fast")
	}
}

func w13cNowMs(value int64) func(*StreamPipeOptions) {
	return func(o *StreamPipeOptions) { o.NowMs = func() int64 { return value } }
}

func w13cInt64(value int64) *int64 { return &value }

// ---- pipefinal：handlePipeError 归一化路径 ----

func TestW13CHandlePipeErrorAbortedArms(t *testing.T) {
	// 终态已写 + aborted：按成功收尾。
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.terminalEventWritten = true
	pipe.downstreamCommit.SemanticCommitted = true
	result, err := pipe.handlePipeError(errors.New("w13c abort after terminal"))
	if err != nil || !result.Completed {
		t.Fatalf("terminal abort result = %+v err=%v", result, err)
	}

	// aborted + 语义已提交但不完整：OnIncompleteClientAbort 回调。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	pipe2.downstreamCommit.SemanticCommitted = true
	pipe2.totalResponseBytes = 128
	pipe2.inspector.PushChunk([]byte(chatDeltaChunk))
	callbacks := 0
	pipe2.options.OnIncompleteClientAbort = func(IncompleteClientAbortContext) error {
		callbacks++
		return errors.New("w13c callback failure")
	}
	result2, err2 := pipe2.handlePipeError(&UpstreamRequestAbortedError{Message: ErrUpstreamRequestAbortedMessage, UpstreamRequestStarted: true})
	if err2 == nil {
		t.Fatal("aborted without terminal must surface the original error")
	}
	if callbacks != 1 {
		t.Fatalf("incomplete abort callbacks = %d", callbacks)
	}
	_ = result2

	// BeforeDownstreamCommitError 解包。
	pipe3, _ := w11bNewFinalPipe(t, nil)
	inner := errors.New("w13c before commit")
	result3, err3 := pipe3.handlePipeError(&StreamBeforeDownstreamCommitError{OriginalError: inner})
	if !errors.Is(err3, inner) || result3.Completed {
		t.Fatalf("before-commit unwrap = %+v err=%v", result3, err3)
	}

	// precommit deadline 错误：已提交响应时中断。
	pipe4, _ := w11bNewFinalPipe(t, nil)
	pipe4.totalResponseBytes = 64
	result4, err4 := pipe4.handlePipeError(&ResponsePrecommitDeadlineError{DeadlineAtMs: 900})
	if err4 != nil || result4.Completed {
		t.Fatalf("deadline result = %+v err=%v", result4, err4)
	}
}

func TestW13CHandlePipeErrorTerminalIgnored(t *testing.T) {
	pipe, _ := w11bNewFinalPipe(t, nil)
	pipe.terminalEventWritten = true
	result, err := pipe.handlePipeError(errors.New("w13c late failure"))
	if err != nil || !result.Completed {
		t.Fatalf("terminal ignored result = %+v err=%v", result, err)
	}
}

func TestW13CFinalizeAfterLoopGenericEOF(t *testing.T) {
	// interpretProtocolFailures 关闭 + 数据事件已观察 → 通用 EOF 按成功收尾。
	pipe, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.InterpretProtocolFailuresSet = true
		o.InterpretProtocolFailures = false
	})
	pipe.preCommitSseEvidence.Push([]byte("data: {\"type\":\"ping\"}\n\n"))
	pipe.completed = true
	result, err := pipe.finalizeAfterLoop()
	if err != nil || !result.Completed {
		t.Fatalf("generic eof = %+v err=%v", result, err)
	}

	// 无终止事件 → finalizeMissingTerminal。
	pipe2, _ := w11bNewFinalPipe(t, nil)
	pipe2.completed = true
	result2, err2 := pipe2.finalizeAfterLoop()
	if err2 != nil {
		t.Fatal(err2)
	}
	if result2.Completed || result2.ErrorCode == "" {
		t.Fatalf("missing terminal = %+v", result2)
	}
}

// ---- inspection：策略匹配 ----

func w13cPolicy(enabled bool) RuntimeResponseInspectionPolicy {
	return RuntimeResponseInspectionPolicy{
		ID: "w13c-policy", Source: PolicySourceAccount, Enabled: enabled,
		ExecutionMode: "enforce", DataHandling: "replace_with_failure",
	}
}

func TestW13CMatchRuntimePolicyArms(t *testing.T) {
	frame := gatewayproto.SemanticFrame{Text: "hello world", VisibleOutput: true}

	// 禁用策略跳过。
	if MatchRuntimeResponseInspectionPolicy(frame, []RuntimeResponseInspectionPolicy{w13cPolicy(false)}, nil) != nil {
		t.Fatal("disabled policy must be skipped")
	}

	// outputTextIncludes 命中。
	policy := w13cPolicy(true)
	policy.Match = gatewayruntimecache.ResponseInspectionPolicyMatch{OutputTextIncludes: []string{"world"}}
	match := MatchRuntimeResponseInspectionPolicy(frame, []RuntimeResponseInspectionPolicy{policy}, nil)
	if match == nil || match.MatchedField != "outputTextIncludes" {
		t.Fatalf("match = %+v", match)
	}

	// 输出排除词命中 → 整体跳过。
	exclude := w13cPolicy(true)
	exclude.Match = gatewayruntimecache.ResponseInspectionPolicyMatch{
		OutputTextIncludes: []string{"hello"}, OutputTextExcludes: []string{"world"},
	}
	if MatchRuntimeResponseInspectionPolicy(frame, []RuntimeResponseInspectionPolicy{exclude}, nil) != nil {
		t.Fatal("excluded output text must skip the policy")
	}

	// 首个正则匹配失败：不可见输出直接否定。
	invisible := firstPositiveMatch(gatewayproto.SemanticFrame{Text: "x"}, gatewayruntimecache.ResponseInspectionPolicyMatch{
		OutputTextIncludes: []string{"x"},
	})
	if invisible != nil {
		t.Fatalf("invisible output = %+v", invisible)
	}
	if firstPositiveMatch(frame, gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"boom"}}) != nil {
		t.Fatal("missing error code must not match")
	}
	if firstPositiveMatch(frame, gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorMessagesIncludes: []string{"x"}}) != nil {
		t.Fatal("empty error message must not match")
	}
	if firstPositiveMatch(frame, gatewayruntimecache.ResponseInspectionPolicyMatch{RawTextIncludes: []string{"x"}}) != nil {
		t.Fatal("empty raw text must not match")
	}
	// snippet 为空时回退首个匹配。
	fallback := firstPositiveMatch(frame, gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"w13c"}})
	_ = fallback

	// 决策动作矩阵。
	base := w13cPolicy(true)
	dryRun := base
	dryRun.ExecutionMode = "dry_run"
	if got := responseInspectionDecisionAction(dryRun, "sse"); got != "dry_run" {
		t.Fatalf("dry run action = %q", got)
	}
	discard := base
	discard.DataHandling = "discard_event"
	if got := responseInspectionDecisionAction(discard, "sse"); got != "discard_event" {
		t.Fatalf("discard sse action = %q", got)
	}
	if got := responseInspectionDecisionAction(discard, "json"); got != "dry_run" {
		t.Fatalf("discard json action = %q", got)
	}
	discardResponse := base
	discardResponse.DataHandling = "discard_response"
	if got := responseInspectionDecisionAction(discardResponse, "sse"); got != "discard_response" {
		t.Fatalf("discard_response action = %q", got)
	}
	if got := responseInspectionDecisionAction(base, "sse"); got != "replace_with_failure" {
		t.Fatalf("default action = %q", got)
	}
}

func TestW13CResolvePoliciesScopeArms(t *testing.T) {
	provider := "openai"
	summary := gatewayruntimecache.ResponseInspectionPolicySummary{
		ID: "mgmt-1", Enabled: true, ScopeType: "provider", ProtocolCode: "openai", ProviderCode: &provider,
	}
	if !policyMatchesAccountScope(summary, "openai", "openai") {
		t.Fatal("matching provider scope must resolve")
	}
	if policyMatchesAccountScope(summary, "openai", "anthropic") {
		t.Fatal("mismatched provider must not resolve")
	}
	if policyMatchesAccountScope(gatewayruntimecache.ResponseInspectionPolicySummary{Enabled: false}, "openai", "openai") {
		t.Fatal("disabled policy must not resolve")
	}
	protocolScope := summary
	protocolScope.ScopeType = "protocol"
	protocolScope.ProviderCode = nil
	if !policyMatchesAccountScope(protocolScope, "openai", "anything") {
		t.Fatal("protocol scope ignores provider")
	}

	// 排序：account 在 management 之前。
	resolved := ResolveRuntimeResponseInspectionPolicies("openai", "openai",
		[]AccountResponseInspectionRule{{Name: "rule", Enabled: true, Priority: 5}},
		[]gatewayruntimecache.ResponseInspectionPolicySummary{summary})
	if len(resolved) != 2 || resolved[0].Source != PolicySourceAccount || resolved[1].Source != PolicySourceManagement {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestW13CJSONPathAndSnippetArms(t *testing.T) {
	decoded := map[string]any{"a": map[string]any{"b": false}, "list": []any{}, "n": 1.0}
	if jsonPathExists(decoded, "a.b") {
		t.Fatal("false value is not meaningful")
	}
	if jsonPathExists(decoded, "a.c") {
		t.Fatal("missing key is not meaningful")
	}
	if jsonPathExists(decoded, "list.x") {
		t.Fatal("index into object must fail")
	}
	if jsonPathExists(decoded, "list") {
		t.Fatal("empty array is not a meaningful value")
	}
	if !jsonPathExists(decoded, "n") {
		t.Fatal("number value is meaningful")
	}
	frame := gatewayproto.SemanticFrame{RawJSON: []byte(`{"a":{"b":false}}`)}
	if firstJSONPathMatch(frame, []string{"a"}) != nil {
		t.Fatal("raw json bytes without decode must not match")
	}
	frame.RawJSON = nil
	frame.RawJSONPaths = []string{"a"}
	if got := firstJSONPathMatch(frame, []string{"a", "z"}); got == nil || *got != "a" {
		t.Fatalf("raw json paths = %v", got)
	}
	// snippetAround 找不到匹配时回退前缀。
	snippet := snippetAround("short", "missing-needle")
	if snippet != "short" {
		t.Fatalf("fallback snippet = %q", snippet)
	}
}

// ---- nonstreamjson：协议族合法形态 ----

func w13cJSONBody(value any) GatewayNonStreamJsonBody {
	return GatewayNonStreamJsonBody{Status: NonStreamJSONStatusValid, Value: value}
}

func TestW13CProtocolFamilyShapeArms(t *testing.T) {
	if ProtocolValidatedNonStreamResponse(w13cJSONBody(map[string]any{"choices": []any{map[string]any{"error": map[string]any{}}}}), 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("choice error must invalidate chat_completions")
	}
	if !ProtocolValidatedNonStreamResponse(w13cJSONBody(map[string]any{"choices": []any{map[string]any{"text": "hi"}}}), 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("text choice must validate")
	}
	if ProtocolValidatedNonStreamResponse(w13cJSONBody(map[string]any{"id": "r1", "output": []any{}, "object": "response", "status": "failed"}), 200, "responses", "/v1/responses") {
		t.Fatal("failed response must invalidate")
	}
	if !ProtocolValidatedNonStreamResponse(w13cJSONBody(map[string]any{"type": "message", "content": []any{}}), 200, "messages", "/v1/messages") {
		t.Fatal("anthropic message must validate")
	}
	if !ProtocolValidatedNonStreamResponse(w13cJSONBody(map[string]any{"name": "m"}), 200, "models", "/v1/models") {
		t.Fatal("model name must validate")
	}
	if ProtocolValidatedNonStreamResponse(w13cJSONBody(map[string]any{"input_tokens": "x"}), 200, "message_token_counting", "/v1/messages/count_tokens") {
		t.Fatal("string token count must invalidate")
	}
	// 非 2xx 与非法 JSON 状态直接否定。
	if ProtocolValidatedNonStreamResponse(w13cJSONBody(map[string]any{"choices": []any{}}), 500, "chat_completions", "/v1/chat/completions") {
		t.Fatal("non-2xx must invalidate")
	}
	if ProtocolValidatedNonStreamResponse(GatewayNonStreamJsonBody{Status: NonStreamJSONStatusInvalid}, 200, "chat_completions", "/v1/chat/completions") {
		t.Fatal("invalid body must invalidate")
	}
}

func TestW13CGatewayGeneratedFailureAndCyberArms(t *testing.T) {
	if IsGatewayGeneratedResponsesFailure("not-object", "responses") {
		t.Fatal("non-object must be false")
	}
	if IsGatewayGeneratedResponsesFailure(map[string]any{"status": "ok"}, "responses") {
		t.Fatal("non-failed status must be false")
	}
	if IsGatewayGeneratedResponsesFailure(map[string]any{"status": "failed", "metadata": "x"}, "responses") {
		t.Fatal("non-object metadata must be false")
	}
	generated := IsGatewayGeneratedResponsesFailure(map[string]any{
		"status": "failed", "metadata": map[string]any{"gateway_generated_failure": true},
	}, "responses")
	if !generated {
		t.Fatal("gateway generated failure must be true")
	}

	ok200 := 200
	if IsCodexResponsesCyberPolicyFailedJSON(ok200, "responses", "codex", map[string]any{}) {
		t.Fatal("2xx must be false")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(500, "chat_completions", "codex", map[string]any{}) {
		t.Fatal("non-responses family must be false")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(500, "responses", "generic", map[string]any{}) {
		t.Fatal("non-codex profile must be false")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(500, "responses", "codex", "not-object") {
		t.Fatal("non-object body must be false")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(500, "responses", "codex", map[string]any{"status": "failed"}) {
		t.Fatal("missing error object must be false")
	}
	if IsCodexResponsesCyberPolicyFailedJSON(500, "responses", "codex", map[string]any{
		"status": "failed", "error": map[string]any{"code": 7},
	}) {
		t.Fatal("non-string code must be false")
	}
	if !IsCodexResponsesCyberPolicyFailedJSON(500, "responses", "codex", map[string]any{
		"status": "failed", "error": map[string]any{"code": "cyber_policy"},
	}) {
		t.Fatal("cyber policy failure must be true")
	}
}

// ---- heartbeat：循环与写出臂 ----

type w13cFailResponseWriter struct {
	header http.Header
}

func (w *w13cFailResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *w13cFailResponseWriter) Write([]byte) (int, error) { return 0, errors.New("w13c write failure") }
func (w *w13cFailResponseWriter) WriteHeader(int)           {}

func TestW13CHeartbeatLoopArms2(t *testing.T) {
	// 写入失败 → 循环立即退出。
	failWriter := gatewaypreauth.NewTrackingWriter(&w13cFailResponseWriter{})
	done := make(chan struct{})
	go func() {
		runHeartbeatLoop(HeartbeatDeps{Res: failWriter, IntervalMs: 20}, []byte(": hb\n\n"), context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat loop must exit after a write failure")
	}
}

// ---- codexcontract / precommit / streamresult 散点 ----

func TestW13CCompactionRequestPathArms(t *testing.T) {
	scanned := &gatewaybody.BodyState{JSONParseStatus: gatewaybody.JSONParseStatusScannedJSON}
	if requestPathHasCompactionTrigger("/v1/responses", scanned, []byte(`{"type":"compaction_trigger"}`)) {
		t.Fatal("scanned JSON state must skip the raw scan")
	}
	compacted := &gatewaybody.BodyState{CodexCompactionTrigger: true}
	if !requestPathHasCompactionTrigger("/v1/responses", compacted, nil) {
		t.Fatal("compaction state must short-circuit true")
	}
	// jsonValue 数组截断与 map 广度截断。
	array := make([]any, 600)
	array[499] = map[string]any{"type": "compaction_trigger"}
	if !jsonValueHasCompactionTrigger(array, 0) {
		t.Fatal("trigger within the scan limit must be found")
	}
	wide := map[string]any{"type": "other"}
	if jsonValueHasCompactionTrigger(wide, 0) {
		t.Fatal("non-trigger object must be false")
	}
}

func TestW13CPreCommitBufferPushArms(t *testing.T) {
	state := NewPreCommitBufferState(true)
	for i := 0; i < 3; i++ {
		AppendStreamPreCommitChunk(state, []byte("x"))
	}
	ClearStreamPreCommitChunks(state)
	if len(state.Chunks) != 0 {
		t.Fatalf("cleared chunks = %d", len(state.Chunks))
	}
}

func TestW13CPublicStreamFailureMessageArms(t *testing.T) {
	if got := PublicStreamFailureMessage(nil, true, nil); got == "" {
		t.Fatal("protocol failure must render a message")
	}
	planTimeout := &StreamReadPlanTimeoutError{Message: "读取超时"}
	if got := PublicStreamFailureMessage(planTimeout, false, nil); got != "读取超时" {
		t.Fatalf("plan timeout message = %q", got)
	}
	firstByte := &FirstByteTimeoutError{Message: "首字节超时"}
	if got := PublicStreamFailureMessage(firstByte, false, nil); got != "首字节超时" {
		t.Fatalf("first byte message = %q", got)
	}
	transport := &StreamTransportFailure{Reason: "connection_reset"}
	if got := PublicStreamFailureMessage(nil, false, transport); got != "connection_reset" {
		t.Fatalf("transport message = %q", got)
	}
	if got := PublicStreamFailureMessage(nil, false, nil); got != "网关处理流式响应失败" {
		t.Fatalf("fallback message = %q", got)
	}
	if IsGatewayLocalStreamFailure(nil, true, nil) {
		t.Fatal("protocol failure is not gateway-local")
	}
}
