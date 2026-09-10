package gatewaydispatch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"
)

// 首字截止决策 / 序列化标记 / Codex 客户端头的单元测试。
// 时间控制：注入 NowMs；pending read 用预 settled 通道保证确定性。

// injectNowMs 替换包级时钟并在测试结束恢复。
func injectNowMs(t *testing.T, now func() int64) {
	t.Helper()
	previous := NowMs
	NowMs = now
	t.Cleanup(func() { NowMs = previous })
}

// preSettledPendingRead 返回一个已 settle 的 pending read（与 Decide* 的
// 同步 settle 语义一致：读完成先于决策）。
func preSettledPendingRead(t *testing.T, result chunkResult, err error, settledAt int64) *ObservedFirstBytePendingRead[chunkResult] {
	injectNowMs(t, func() int64 { return settledAt })
	return ObserveFirstBytePendingRead(func() (chunkResult, error) {
		return result, err
	})
}

func TestObservedFirstBytePendingReadLifecycle(t *testing.T) {
	base := int64(1_000)
	injectNowMs(t, func() int64 { return base })
	observed := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		return chunkResult{n: 3}, nil
	})
	// 轮询 settle 标记（goroutine 调度无固定顺序，用短超时上限保证确定性收敛）。
	deadline := time.Now().Add(2 * time.Second)
	for !observed.IsSettled() {
		if time.Now().After(deadline) {
			t.Fatal("pending read 未在时限内 settle")
		}
		time.Sleep(time.Millisecond)
	}
	settledAt, ok := observed.SettledAtMs()
	if !ok || settledAt != base {
		t.Fatalf("settledAt = %d ok=%v", settledAt, ok)
	}
	// Await 可重复调用（重复 Await 语义）。
	for i := 0; i < 2; i++ {
		result, err := observed.Await()
		if err != nil || result.n != 3 {
			t.Fatalf("Await#%d = %+v %v", i, result, err)
		}
	}
}

func TestObservedFirstBytePendingReadError(t *testing.T) {
	observed := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		return chunkResult{}, io.EOF
	})
	result, err := observed.Await()
	if err != io.EOF {
		t.Fatalf("err = %v", err)
	}
	if result.n != 0 {
		t.Fatalf("n = %d", result.n)
	}
}

func TestDecideFirstByteDeadlineNilHandlerAborts(t *testing.T) {
	observed := ObserveFirstBytePendingRead(func() (chunkResult, error) {
		return chunkResult{n: 1}, nil // 永不 settle 的等待由决策先行返回
	})
	result := DecideFirstByteDeadlineAfterPendingRead(observed, nil, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{})
	if result.Type != DeadlineDecisionAction || result.Action != FirstByteDeadlineActionAbort {
		t.Fatalf("result = %#v", result)
	}
	if result.Error != nil {
		t.Fatalf("error = %v", result.Error)
	}
}

func TestDecideFirstByteDeadlineHandlerContinueWithSettledRead(t *testing.T) {
	observed := preSettledPendingRead(t, chunkResult{n: 5}, nil, 2_000)
	awaitPendingReadSettled(t, observed)
	result := DecideFirstByteDeadlineAfterPendingRead(observed, func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		return FirstByteDeadlineActionContinue
	}, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{})
	if result.Type != DeadlineDecisionRead || result.Result.n != 5 {
		t.Fatalf("result = %#v", result)
	}
	if result.Action != FirstByteDeadlineActionContinue || result.SettledAtMs != 2_000 {
		t.Fatalf("action = %q settledAt = %d", result.Action, result.SettledAtMs)
	}
}

// 决策 handler 的错误经 panic 注入（runDeadlineHandler 的 recover 语义），
// 未 settle 的 pending read 时以 action 形态返回错误。
func TestDecideFirstByteDeadlineHandlerErrorUnsettled(t *testing.T) {
	handlerErr := errors.New("决策失败")
	blocked := &ObservedFirstBytePendingRead[chunkResult]{outcome: make(chan readOutcome[chunkResult], 1)}
	result := DecideFirstByteDeadlineAfterPendingRead(blocked, func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		panic(handlerErr)
	}, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{})
	if result.Type != DeadlineDecisionAction {
		t.Fatalf("type = %q", result.Type)
	}
	if !errors.Is(result.Error, handlerErr) {
		t.Fatalf("error = %v", result.Error)
	}
}

func TestDecideFirstByteDeadlineHandlerPanicBecomesError(t *testing.T) {
	blocked := &ObservedFirstBytePendingRead[chunkResult]{outcome: make(chan readOutcome[chunkResult], 1)}
	handlerErr := errors.New("handler 崩溃")
	result := DecideFirstByteDeadlineAfterPendingRead(blocked, func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		panic(handlerErr)
	}, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{})
	if result.Type != DeadlineDecisionAction {
		t.Fatalf("type = %q", result.Type)
	}
	// error 值的 panic 原样透传（保留原始错误链）。
	if !errors.Is(result.Error, handlerErr) {
		t.Fatalf("error = %v", result.Error)
	}
	// 非错误 panic 值包装为 deadlineHandlerPanic。
	result = DecideFirstByteDeadlineAfterPendingRead(blocked, func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		panic("字符串崩溃")
	}, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{})
	var panicErr *deadlineHandlerPanic
	if !errorsAs(result.Error, &panicErr) || panicErr.Error() != "网关首字截止决策失败" {
		t.Fatalf("error = %v", result.Error)
	}
}

// 已 settle 的 pending read 时，handler 错误随读结果以 read 形态返回
// （Node: decision.then(notify) 的合并语义）。
func TestDecideFirstByteDeadlineHandlerErrorWithSettledRead(t *testing.T) {
	handlerErr := errors.New("决策失败")
	observed := preSettledPendingRead(t, chunkResult{n: 9}, io.EOF, 3_000)
	awaitPendingReadSettled(t, observed)
	result := DecideFirstByteDeadlineAfterPendingRead(observed, func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		panic(handlerErr)
	}, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{})
	if result.Type != DeadlineDecisionRead {
		t.Fatalf("type = %q", result.Type)
	}
	if !errors.Is(result.DecisionError, handlerErr) {
		t.Fatalf("decision error = %v", result.DecisionError)
	}
	if result.Error != io.EOF || result.Result.n != 9 || result.SettledAtMs != 3_000 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunDeadlineHandlerErrorPath(t *testing.T) {
	handlerErr := errors.New("决策失败")
	action, err := runDeadlineHandler(func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		panic(handlerErr)
	}, FirstByteDeadlineDecisionInput{})
	if err == nil {
		t.Fatal("panic 必须转换为错误")
	}
	if action != "" {
		t.Fatalf("action = %q", action)
	}
	if !errors.Is(err, handlerErr) {
		t.Fatalf("error 链必须保留原始错误: %v", err)
	}
	// 正常路径透传 action。
	action, err = runDeadlineHandler(func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		return FirstByteDeadlineActionContinue
	}, FirstByteDeadlineDecisionInput{})
	if err != nil || action != FirstByteDeadlineActionContinue {
		t.Fatalf("action = %q err = %v", action, err)
	}
}

func TestDecideFirstByteDeadlinePrecommitWallWins(t *testing.T) {
	base := int64(5_000)
	notifyCalled := false
	observed := preSettledPendingRead(t, chunkResult{n: 4}, nil, base)
	awaitPendingReadSettled(t, observed)
	result := DecideFirstByteDeadlineAfterPendingRead(observed, func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		return FirstByteDeadlineActionContinue
	}, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{
		ResponsePrecommitDeadlineAtMs: ptrInt64(base - 100), // 已过期的墙钟
		OnResponsePrecommitDeadline:   func() { notifyCalled = true },
	})
	if !notifyCalled {
		t.Fatal("墙钟胜出必须通知 precommit 回调")
	}
	if result.Type != DeadlineDecisionResponsePrecommit {
		t.Fatalf("type = %q", result.Type)
	}
	var deadlineErr *GatewayResponsePrecommitDeadlineError
	if !errorsAs(result.Error, &deadlineErr) || deadlineErr.DeadlineAtMs != base-100 {
		t.Fatalf("error = %v", result.Error)
	}
}

func TestDecideFirstByteDeadlinePrecommitReadSettledBeforeDeadline(t *testing.T) {
	base := int64(5_000)
	observed := preSettledPendingRead(t, chunkResult{n: 4}, nil, base-200)
	awaitPendingReadSettled(t, observed)
	// 决策时刻的当前时钟晚于墙钟截止：墙钟已过期。
	injectNowMs(t, func() int64 { return base })
	result := DecideFirstByteDeadlineAfterPendingRead(observed, func(FirstByteDeadlineDecisionInput) FirstByteDeadlineAction {
		return FirstByteDeadlineActionContinue
	}, FirstByteDeadlineDecisionInput{}, FirstByteDeadlineDecisionWaitOptions{
		ResponsePrecommitDeadlineAtMs: ptrInt64(base - 100),
	})
	if result.Type != DeadlineDecisionRead {
		t.Fatalf("先 settle 的读应胜出, type = %q", result.Type)
	}
	if result.DecisionError == nil {
		t.Fatal("决策错误必须随读结果带回")
	}
}

func TestFinishDeadlineDecisionBranches(t *testing.T) {
	// 未 settle + 无决策错误 → action。
	blocked := &ObservedFirstBytePendingRead[chunkResult]{outcome: make(chan readOutcome[chunkResult], 1)}
	result := finishDeadlineDecision(blocked, FirstByteDeadlineActionContinue, nil)
	if result.Type != DeadlineDecisionAction || result.Action != FirstByteDeadlineActionContinue {
		t.Fatalf("result = %#v", result)
	}
	// 未 settle + 决策错误 → action + error。
	decisionErr := errors.New("x")
	result = finishDeadlineDecision(blocked, FirstByteDeadlineActionContinue, decisionErr)
	if result.Type != DeadlineDecisionAction || result.Error != decisionErr {
		t.Fatalf("result = %#v", result)
	}
	// 已 settle → read 形态（先等 goroutine 完成 settle，保证确定性）。
	settled := preSettledPendingRead(t, chunkResult{n: 2}, nil, 100)
	awaitPendingReadSettled(t, settled)
	result = finishDeadlineDecision(settled, FirstByteDeadlineActionContinue, nil)
	if result.Type != DeadlineDecisionRead || result.Result.n != 2 || result.SettledAtMs != 100 {
		t.Fatalf("result = %#v", result)
	}
}

// awaitPendingReadSettled 轮询等待 pending read 的 goroutine 完成 settle。
func awaitPendingReadSettled[T any](t *testing.T, observed *ObservedFirstBytePendingRead[T]) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !observed.IsSettled() {
		if time.Now().After(deadline) {
			t.Fatal("pending read 未在时限内 settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNotifyResponsePrecommitDeadlineRecovers(t *testing.T) {
	notifyResponsePrecommitDeadline(nil) // nil 安全
	called := false
	notifyResponsePrecommitDeadline(func() { called = true })
	if !called {
		t.Fatal("回调必须被调用")
	}
	// 回调 panic 被吞掉（决策路径不因通知失败而崩溃）。
	notifyResponsePrecommitDeadline(func() { panic("通知失败") })
}

func TestWaitForDelayMsZeroAndCancellation(t *testing.T) {
	if err := waitForDelayMs(context.Background(), 0); err != nil {
		t.Fatalf("零延迟: %v", err)
	}
	if err := waitForDelayMs(context.Background(), -1); err != nil {
		t.Fatalf("负延迟: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForDelayMs(canceled, 10_000); err == nil {
		t.Fatal("已取消信号必须立刻返回错误")
	}
	if err := waitForDelayMs(context.Background(), 1); err != nil {
		t.Fatalf("1ms 延迟: %v", err)
	}
}

func TestMaxMinInt64(t *testing.T) {
	if maxInt64(1, 2) != 2 || maxInt64(2, 1) != 2 || minInt64(1, 2) != 1 || minInt64(2, 1) != 1 {
		t.Fatal("max/min 语义不符")
	}
}

// ---------------------------------------------------------------------------
// serialized.go
// ---------------------------------------------------------------------------

func TestGatewayCodexHistorySanitizedFlag(t *testing.T) {
	body := []byte(`{"input":[1,2]}`)
	if IsGatewayCodexHistorySanitized(body) {
		t.Fatal("未标记的 body 不应视为已清洗")
	}
	if got := MarkGatewayCodexHistorySanitized(body); string(got) != string(body) {
		t.Fatal("标记应原样返回 body")
	}
	if !IsGatewayCodexHistorySanitized(body) {
		t.Fatal("标记后必须命中")
	}
	// 不同内容不命中。
	if IsGatewayCodexHistorySanitized([]byte(`{"input":[3]}`)) {
		t.Fatal("不同内容不应命中")
	}
}

func TestMarkCodexHistorySanitizedCapacityEviction(t *testing.T) {
	gatewaySerializedFlagsMu.Lock()
	previous := gatewayCodexSanitizedBodies
	// 构造满容量注册表（白盒：验证逐出分支不 panic 且仍写入新键）。
	gatewayCodexSanitizedBodies = make(map[string]struct{}, gatewaySerializedFlagCapacity+1)
	for i := 0; i < gatewaySerializedFlagCapacity; i++ {
		gatewayCodexSanitizedBodies["old-"+intToStringTest(i)] = struct{}{}
	}
	gatewaySerializedFlagsMu.Unlock()
	t.Cleanup(func() {
		gatewaySerializedFlagsMu.Lock()
		gatewayCodexSanitizedBodies = previous
		gatewaySerializedFlagsMu.Unlock()
	})
	MarkGatewayCodexHistorySanitized([]byte("fresh"))
	gatewaySerializedFlagsMu.Lock()
	defer gatewaySerializedFlagsMu.Unlock()
	if len(gatewayCodexSanitizedBodies) > gatewaySerializedFlagCapacity {
		t.Fatalf("容量应受限, got %d", len(gatewayCodexSanitizedBodies))
	}
	if _, ok := gatewayCodexSanitizedBodies["fresh"]; !ok {
		t.Fatal("新键必须写入")
	}
}

func TestGatewaySerializedJSONObjectAndSerialize(t *testing.T) {
	if GatewaySerializedJSONObject([]byte("not-json")) != nil {
		t.Fatal("非法 JSON 返回 nil")
	}
	if GatewaySerializedJSONObject([]byte("[1]")) != nil {
		t.Fatal("数组不是对象")
	}
	object := GatewaySerializedJSONObject([]byte(`{"a":1}`))
	if object == nil || object["a"] != float64(1) {
		t.Fatalf("object = %#v", object)
	}
	if SerializeGatewayJSONObject(nil) == nil {
		t.Fatal("可序列化输入不返回 nil")
	}
	// 含 channel 的值无法 JSON 序列化 → nil。
	if SerializeGatewayJSONObject(map[string]any{"ch": make(chan int)}) != nil {
		t.Fatal("不可序列化输入应返回 nil")
	}
}

// ---------------------------------------------------------------------------
// clientheaders.go
// ---------------------------------------------------------------------------

func TestIsOpenAICodexClientHeadersIdentityMatrix(t *testing.T) {
	cases := []struct {
		headers http.Header
		want    bool
	}{
		{http.Header{"Originator": []string{"codex_cli_rs"}}, true},
		{http.Header{"Originator": []string{" Codex "}}, true},
		{http.Header{"User-Agent": []string{"codex/1.0"}}, true},
		{http.Header{"User-Agent": []string{"codexwsl"}}, false},
		{http.Header{"User-Agent": []string{"Mozilla/5.0"}}, false},
		{http.Header{}, false},
	}
	for index, testCase := range cases {
		if got := IsOpenAICodexClientHeaders(testCase.headers); got != testCase.want {
			t.Fatalf("case %d: got %v want %v", index, got, testCase.want)
		}
	}
}

func TestNormalizeOpenAICodexClientHeaders(t *testing.T) {
	headers := http.Header{}
	NormalizeOpenAICodexClientHeaders(headers, "gpt-5.6-sol")
	if headers.Get("Originator") != OpenAICodexOriginator {
		t.Fatalf("Originator = %q", headers.Get("Originator"))
	}
	if headers.Get("User-Agent") != OpenAICodexUserAgent {
		t.Fatalf("User-Agent = %q", headers.Get("User-Agent"))
	}
	if headers.Get("Session-Id") == "" || headers.Get("Thread-Id") == "" || headers.Get("X-Client-Request-Id") == "" {
		t.Fatal("合成会话头缺失")
	}
	if headers.Get("X-Codex-Beta-Features") != "remote_compaction_v2" {
		t.Fatalf("beta features = %q", headers.Get("X-Codex-Beta-Features"))
	}
	metadata := parsedCodexTurnMetadata(headers.Get("X-Codex-Turn-Metadata"))
	if metadata == nil {
		t.Fatal("turn metadata 缺失")
	}
	if headers.Get("X-Codex-Window-Id") == "" {
		t.Fatal("window id 缺失")
	}
	// lite 模型写入 lite 头。
	if headers.Get(OpenAICodexResponsesLiteHeader) != "true" {
		t.Fatalf("lite header = %q", headers.Get(OpenAICodexResponsesLiteHeader))
	}
	// 非 lite 模型删除 stale 的 lite 头（需全新非 Codex 头以走完整归一化）。
	headersNonLite := http.Header{OpenAICodexResponsesLiteHeader: []string{"true"}}
	NormalizeOpenAICodexClientHeaders(headersNonLite, "gpt-4.1")
	if headersNonLite.Get(OpenAICodexResponsesLiteHeader) != "" {
		t.Fatal("非 lite 模型必须删除 lite 头")
	}
	// 既有值不被覆盖（setHeaderIfMissing 语义）。
	headers2 := http.Header{"Session-Id": []string{"existing"}}
	NormalizeOpenAICodexClientHeaders(headers2, "gpt-4.1")
	if headers2.Get("Session-Id") != "existing" {
		t.Fatalf("已有 Session-Id 被覆盖: %q", headers2.Get("Session-Id"))
	}
	// 已是 Codex 客户端时不再归一化。
	headers3 := http.Header{"Originator": []string{"codex_cli_rs"}}
	NormalizeOpenAICodexClientHeaders(headers3, "gpt-5.6-sol")
	if headers3.Get("User-Agent") != "" {
		t.Fatal("Codex 客户端头不应被改写")
	}
}

func TestUsesOpenAICodexResponsesLite(t *testing.T) {
	if !UsesOpenAICodexResponsesLite(" GPT-5.6-SOL ") {
		t.Fatal("lite 模型集应忽略大小写与空白")
	}
	if UsesOpenAICodexResponsesLite("gpt-4.1") {
		t.Fatal("非 lite 模型不应命中")
	}
}

func TestNormalizeOpenAICodexResponsesLiteBodyFullMerge(t *testing.T) {
	// 非 Codex 头：先归一化头再合并 client_metadata。
	headers := http.Header{}
	body := map[string]any{"model": "gpt-5.6-sol"}
	NormalizeOpenAICodexResponsesLiteBody(body, "gpt-5.6-sol", headers)
	clientMetadata, ok := body["client_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("client_metadata 缺失: %#v", body)
	}
	if clientMetadata["turn_id"] == "" || clientMetadata["session_id"] == "" || clientMetadata["thread_id"] == "" {
		t.Fatalf("client_metadata 字段缺失: %#v", clientMetadata)
	}
	if clientMetadata["x-codex-window-id"] == "" || clientMetadata["x-codex-installation-id"] == "" {
		t.Fatalf("codex 扩展字段缺失: %#v", clientMetadata)
	}
	if _, ok := clientMetadata["x-codex-turn-metadata"]; !ok {
		t.Fatal("x-codex-turn-metadata 缺失")
	}
	if body["prompt_cache_key"] == "" {
		t.Fatal("prompt_cache_key 应回落 session id")
	}
	// lite 模型注入 reasoning.context 与 parallel_tool_calls。
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["context"] != "all_turns" {
		t.Fatalf("reasoning = %#v", body["reasoning"])
	}
	if body["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %#v", body["parallel_tool_calls"])
	}
	// 既有 reasoning 字段保留。
	body2 := map[string]any{"model": "gpt-5.6-sol", "reasoning": map[string]any{"effort": "high"}}
	NormalizeOpenAICodexResponsesLiteBody(body2, "gpt-5.6-sol", http.Header{"Originator": []string{"codex_cli_rs"}})
	reasoning2 := body2["reasoning"].(map[string]any)
	if reasoning2["effort"] != "high" || reasoning2["context"] != "all_turns" {
		t.Fatalf("reasoning 合并 = %#v", reasoning2)
	}
	// 非 lite 模型不改写 reasoning。
	body3 := map[string]any{"model": "gpt-4.1"}
	NormalizeOpenAICodexResponsesLiteBody(body3, "gpt-4.1", http.Header{"Originator": []string{"codex_cli_rs"}})
	if _, has := body3["reasoning"]; has {
		t.Fatal("非 lite 模型不应注入 reasoning")
	}
	if _, has := body3["parallel_tool_calls"]; has {
		t.Fatal("非 lite 模型不应注入 parallel_tool_calls")
	}
}

func TestNormalizeOpenAICodexResponsesLiteBodyPreservesPromptCacheKeyAndMetadata(t *testing.T) {
	headers := http.Header{}
	body := map[string]any{
		"prompt_cache_key": "preset",
		"client_metadata":  map[string]any{"turn_id": "old-turn", "custom": "keep"},
	}
	NormalizeOpenAICodexResponsesLiteBody(body, "gpt-4.1", headers)
	clientMetadata := body["client_metadata"].(map[string]any)
	if clientMetadata["custom"] != "keep" {
		t.Fatal("既有 metadata 自定义键应保留")
	}
	if clientMetadata["turn_id"] == "old-turn" {
		t.Fatal("合成 metadata 应覆盖既有 turn_id")
	}
	if body["prompt_cache_key"] != "preset" {
		t.Fatalf("已有 prompt_cache_key 不被覆盖, got %#v", body["prompt_cache_key"])
	}
}

func TestSyntheticCodexTurnMetadataFallbacks(t *testing.T) {
	// 完整既有 metadata：保留既有值 + Extra 键。
	headers := http.Header{}
	headers.Set("X-Codex-Turn-Metadata", `{"installation_id":"inst","session_id":"sess","thread_id":"thread","turn_id":"turn","window_id":"win","request_kind":"compact","thread_source":"api","sandbox":"workspace-write","turn_started_at_unix_ms":123,"custom_key":"kept"}`)
	metadata := syntheticCodexTurnMetadata(headers)
	if metadata.SessionID != "sess" || metadata.ThreadID != "thread" || metadata.TurnID != "turn" || metadata.WindowID != "win" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.InstallationID != "inst" || metadata.RequestKind != "compact" || metadata.ThreadSource != "api" || metadata.Sandbox != "workspace-write" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata.TurnStartedAtUnixMs != 123 {
		t.Fatalf("turn started = %d", metadata.TurnStartedAtUnixMs)
	}
	if metadata.Extra["custom_key"] != "kept" {
		t.Fatalf("extra = %#v", metadata.Extra)
	}
	// MarshalJSON 合并 Extra 与类型化字段。
	encoded, err := metadata.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	roundTripped := parsedCodexTurnMetadata(string(encoded))
	if roundTripped["custom_key"] != "kept" || roundTripped["session_id"] != "sess" {
		t.Fatalf("round trip = %#v", roundTripped)
	}
	// 头部回退：session 取 Session-Id 头，thread 取 Thread-Id 头。
	headers2 := http.Header{"Session-Id": []string{"sess-header"}, "Thread-Id": []string{"thread-header"}, "X-Client-Request-Id": []string{"turn-header"}, "X-Codex-Installation-Id": []string{"inst-header"}, "X-Codex-Window-Id": []string{"win-header"}}
	metadata2 := syntheticCodexTurnMetadata(headers2)
	if metadata2.SessionID != "sess-header" || metadata2.ThreadID != "thread-header" || metadata2.TurnID != "turn-header" {
		t.Fatalf("header fallback = %#v", metadata2)
	}
	if metadata2.InstallationID != "inst-header" || metadata2.WindowID != "win-header" {
		t.Fatalf("header fallback = %#v", metadata2)
	}
	// 无头无 metadata：thread 回落 session，window 回落 thread+":0"。
	metadata3 := syntheticCodexTurnMetadata(http.Header{})
	if metadata3.SessionID == "" || metadata3.ThreadID != metadata3.SessionID {
		t.Fatalf("默认回退 = %#v", metadata3)
	}
	if metadata3.WindowID != metadata3.ThreadID+":0" {
		t.Fatalf("window 回退 = %q", metadata3.WindowID)
	}
	if metadata3.RequestKind != "turn" || metadata3.ThreadSource != "user" || metadata3.Sandbox != "none" {
		t.Fatalf("默认枚举 = %#v", metadata3)
	}
	if metadata3.Extra != nil {
		t.Fatalf("无 metadata 时 Extra 应为 nil, got %#v", metadata3.Extra)
	}
}

func TestExtraMetadataKeys(t *testing.T) {
	extra := extraMetadataKeys(map[string]any{
		"session_id": "s", "custom": 1,
	})
	if _, ok := extra["session_id"]; ok {
		t.Fatal("已知键不应进入 extra")
	}
	if extra["custom"] != 1 {
		t.Fatalf("extra = %#v", extra)
	}
	if extraMetadataKeys(map[string]any{"session_id": "s"}) != nil {
		t.Fatal("全部已知键时 extra 为 nil")
	}
}

func TestParsedCodexTurnMetadataInvalid(t *testing.T) {
	if parsedCodexTurnMetadata("") != nil {
		t.Fatal("空串返回 nil")
	}
	if parsedCodexTurnMetadata("not json") != nil {
		t.Fatal("非法 JSON 返回 nil")
	}
	if parsedCodexTurnMetadata("[1]") != nil {
		t.Fatal("数组不是对象")
	}
}

func TestCodexIdentityPrefix(t *testing.T) {
	cases := map[string]bool{
		"codex": true, "codex_cli": true, "codex-cli": true, "codex/cli": true,
		"CODEX cli": true, "codex\tcli": true, "codexwsl": false,
		"prefix-codex": false, "": false,
	}
	for value, want := range cases {
		if got := codexIdentityPrefix(value); got != want {
			t.Fatalf("codexIdentityPrefix(%q) = %v, 期望 %v", value, got, want)
		}
	}
}

func TestHeaderGetTrimmedAndSetIfMissingAndFirstNonEmpty(t *testing.T) {
	headers := http.Header{"X": []string{"  v  "}}
	if headerGetTrimmed(headers, "X") != "v" {
		t.Fatalf("trimmed = %q", headerGetTrimmed(headers, "X"))
	}
	setHeaderIfMissing(headers, "X", "other")
	setHeaderIfMissing(headers, "Y", "new")
	if headers.Get("X") != "  v  " || headers.Get("Y") != "new" {
		t.Fatalf("headers = %#v", headers)
	}
	if firstNonEmptyString("", "  ", "a", "b") != "a" {
		t.Fatal("firstNonEmptyString 语义不符")
	}
	if firstNonEmptyString(" ", "") != "" {
		t.Fatal("全空白应返回空串")
	}
}

// ---------------------------------------------------------------------------
// 并发安全冒烟：slot 并发 Set/Get 不产生数据竞争（-race 下验证）。
// ---------------------------------------------------------------------------

func TestUpstreamResponseModelSlotConcurrentAccess(t *testing.T) {
	slot := &UpstreamResponseModelSlot{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slot.Set("model")
			_ = slot.Get()
		}()
	}
	wg.Wait()
	if slot.Get() != "model" {
		t.Fatalf("final = %q", slot.Get())
	}
}
