package openaicompat

// codex-responses-chat-bridge 响应侧增量状态的补充覆盖测试：reasoning/text
// 输出项、custom 工具输入解包、estimated usage、Finish 中断兜底与完成通知。

import (
	"strings"
	"testing"
)

func TestCovAppendResponsesReasoningAndText(t *testing.T) {
	state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{DefaultModel: "m", IDPrefix: "pfx"})
	t.Run("reasoning 首 delta 与续 delta", func(t *testing.T) {
		first := state.AppendResponsesReasoningDelta("想")
		if len(first) != 2 {
			t.Fatalf("首次应产出 added + delta 两个事件：%v", first)
		}
		if covSseName(t, first[0]) != "response.output_item.added" {
			t.Errorf("首事件 = %q", covSseName(t, first[0]))
		}
		delta := covSseData(t, first[1])
		if delta["delta"] != "想" || delta["item_id"] != state.ReasoningID {
			t.Errorf("reasoning delta = %v", delta)
		}
		second := state.AppendResponsesReasoningDelta("考")
		if len(second) != 1 {
			t.Fatalf("续 delta 只应产出一个事件：%v", second)
		}
		if state.ReasoningText != "想考" {
			t.Errorf("ReasoningText = %q", state.ReasoningText)
		}
	})
	t.Run("text 首 delta", func(t *testing.T) {
		first := state.AppendResponsesTextDelta("答")
		if len(first) != 3 {
			t.Fatalf("首次应产出 added + part.added + delta：%v", first)
		}
		if covSseName(t, first[0]) != "response.output_item.added" {
			t.Errorf("首事件 = %q", covSseName(t, first[0]))
		}
		if covSseName(t, first[1]) != "response.content_part.added" {
			t.Errorf("part 事件 = %q", covSseName(t, first[1]))
		}
		state.AppendResponsesTextDelta("案")
		if state.OutputText != "答案" {
			t.Errorf("OutputText = %q", state.OutputText)
		}
	})
	t.Run("完成 open items 顺序与形态", func(t *testing.T) {
		out := state.CompleteOpenOutputItems()
		names := []string{}
		for _, event := range out {
			names = append(names, covSseName(t, event))
		}
		// reasoning 先创建（index 0）先收尾；text（index 1）随后三连。
		want := []string{"response.output_item.done", "response.output_text.done", "response.content_part.done", "response.output_item.done"}
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Fatalf("收尾事件序列 = %v", names)
		}
		doneReasoning := covSseData(t, out[0])
		item := covMap(t, doneReasoning["item"])
		if covText(t, item["type"]) != "reasoning" {
			t.Fatalf("首个应为 reasoning done：%v", doneReasoning)
		}
		summary := covSlice(t, item["summary"])
		if covText(t, covMap(t, summary[0])["text"]) != "想考" {
			t.Errorf("reasoning summary = %v", summary)
		}
		if _, exists := item["encrypted_content"]; !exists {
			t.Errorf("reasoning done 应带 encrypted_content=null：%v", item)
		}
		if len(state.OutputItems) != 2 {
			t.Fatalf("OutputItems 应记录 2 个 item：%d", len(state.OutputItems))
		}
		if again := state.CompleteOpenOutputItems(); len(again) != 0 {
			t.Errorf("已完成 items 不应重复收尾：%v", again)
		}
	})
	t.Run("reasoningIndexOrZero 缺省", func(t *testing.T) {
		fresh := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		if got := fresh.reasoningIndexOrZero(); got != 0 {
			t.Errorf("缺省 reasoning index = %d", got)
		}
	})
}

func TestCovCodexCustomToolInput(t *testing.T) {
	t.Run("input 键解包", func(t *testing.T) {
		got := codexCustomToolInputFromChatArguments(`{"input":"*** Begin Patch\n+x"}`, "any_tool")
		if got != "*** Begin Patch\n+x" {
			t.Errorf("input 解包 = %q", got)
		}
	})
	t.Run("apply_patch 结构化 files", func(t *testing.T) {
		got := codexCustomToolInputFromChatArguments(
			`{"files":[{"path":"a.txt","content":"l1\r\nl2"},{"path":"","content":"skip"},{"path":"b\n.txt","content":"skip"}]}`,
			"apply_patch")
		want := "*** Add File: a.txt\n+l1\n+l2\n*** End Patch\n"
		if got != want {
			t.Errorf("apply_patch 解包 = %q，期望 %q", got, want)
		}
	})
	t.Run("无有效 files 回退原文", func(t *testing.T) {
		if got := codexCustomToolInputFromChatArguments(`{"files":"bad"}`, "apply_patch"); got != `{"files":"bad"}` {
			t.Errorf("非数组 files 应回退原文：%q", got)
		}
		if got := codexCustomToolInputFromChatArguments(`{"files":[]}`, "apply_patch"); got != `{"files":[]}` {
			t.Errorf("空 files 应回退原文：%q", got)
		}
	})
	t.Run("非 JSON 回退原文", func(t *testing.T) {
		if got := codexCustomToolInputFromChatArguments("raw text", "apply_patch"); got != "raw text" {
			t.Errorf("非 JSON 应回退：%q", got)
		}
	})
	t.Run("codexApplyPatchInputFromStructuredFiles 边界", func(t *testing.T) {
		if got := codexApplyPatchInputFromStructuredFiles("not-array"); got != "" {
			t.Errorf("非数组 = %q", got)
		}
		got := codexApplyPatchInputFromStructuredFiles([]any{
			nil,
			map[string]any{"path": "a", "content": 5},
			map[string]any{"path": "b", "content": "ok"},
		})
		if got != "*** Add File: b\n+ok\n*** End Patch\n" {
			t.Errorf("混合条目 = %q", got)
		}
	})
}

func TestCovCodexStreamFullRoundTrip(t *testing.T) {
	var completed *CodexBridgeCompletionPayload
	notifyCount := 0
	toolAdapters := map[string]CodexBridgeToolAdapter{
		"shell_chat": {Kind: "custom", ChatName: "shell_chat", ResponsesName: "shell", Namespace: "exec"},
	}
	options := CodexResponsesChatBridgeTransformOptions{
		Enabled:                true,
		DefaultModel:           "m",
		IDPrefix:               "br",
		EstimatedInputTokens:   int64Ptr(42),
		ToolAdaptersByChatName: toolAdapters,
		OnCompleted: func(payload CodexBridgeCompletionPayload) {
			completed = &payload
			notifyCount++
		},
	}
	state := NewCodexChatToResponsesState(options)
	events := []string{
		// reasoning + 文本 + custom 工具（分片到齐）。
		`data: {"choices":[{"delta":{"reasoning_content":"推"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"文"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"u1","function":{"name":"shell_"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"chat","arguments":"{\"input\":\"do\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	var collected []string
	for _, eventText := range events {
		collected = append(collected, state.ProcessChatSseEvent(eventText)...)
		state.NotifyCodexResponsesChatBridgeCompletion()
	}
	joined := strings.Join(collected, "")
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"response.reasoning_summary_text.delta",
		"response.output_text.delta",
		`"type":"custom_tool_call"`,
		`"namespace":"exec"`,
		"event: response.completed",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("流缺少 %s：\n%s", want, joined)
		}
	}
	// custom 工具完成项的 input 解包 {input}。
	if !strings.Contains(joined, `"input":"do"`) {
		t.Errorf("custom 工具 input 应解包：\n%s", joined)
	}
	if state.Usage == nil {
		t.Fatal("usage 事件应记录")
	}
	if state.Usage["input_tokens"] != float64(7) {
		t.Errorf("usage = %v", state.Usage)
	}
	if completed == nil {
		t.Fatal("OnCompleted 应被通知一次")
	}
	if completed.ResponseID != state.ResponseID || len(completed.OutputItems) != 3 {
		t.Errorf("完成负载 = %+v", completed)
	}
	// 幂等：重复通知不应触发 handler。
	state.NotifyCodexResponsesChatBridgeCompletion()
	if notifyCount != 1 {
		t.Errorf("通知次数 = %d，期望 1", notifyCount)
	}
	if state.Finish() != nil {
		t.Errorf("TerminalReceived 后 Finish 应返回 nil")
	}
	if again := state.ProcessChatSseEvent(events[0]); again != nil {
		t.Errorf("完成后事件应忽略：%v", again)
	}
}

func TestCovCodexStreamFailurePaths(t *testing.T) {
	t.Run("未知工具调用在 finish 时失败", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		state.ProcessChatSseEvent(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"u","function":{"name":"ghost","arguments":"{}"}}]}}]}` + "\n\n")
		out := state.ProcessChatSseEvent(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n")
		if !strings.Contains(strings.Join(out, ""), `"code":"codex_bridge_unknown_tool_call"`) {
			t.Fatalf("未知工具应失败：%v", out)
		}
		if state.FailureCode != "codex_bridge_unknown_tool_call" {
			t.Errorf("FailureCode = %q", state.FailureCode)
		}
	})
	t.Run("[DONE] 携带 pending 工具失败", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		state.ProcessChatSseEvent(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"half"}}]}}]}` + "\n\n")
		out := state.ProcessChatSseEvent("data: [DONE]\n\n")
		if !strings.Contains(strings.Join(out, ""), "codex_bridge_unknown_tool_call") {
			t.Fatalf("[DONE] 带未解析工具应失败：%v", out)
		}
	})
	t.Run("finishReasonFailures 映射", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{
			FinishReasonFailures: map[string]CodexResponsesChatBridgeFinishReasonFailure{
				"content_filter": {Code: "blocked", Message: "被拦"},
			},
		})
		out := state.ProcessChatSseEvent(`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"content_filter"}]}` + "\n\n")
		joined := strings.Join(out, "")
		if !strings.Contains(joined, `"code":"blocked"`) || !strings.Contains(joined, `"message":"被拦"`) {
			t.Fatalf("finish failure 映射失败：%s", joined)
		}
		if !strings.Contains(joined, "response.output_item.done") {
			t.Errorf("失败前应先完成 open items：%s", joined)
		}
	})
	t.Run("error 事件", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		out := state.ProcessChatSseEvent("event: error\ndata: " + `{"error":{"message":"上游崩","code":"boom"}}` + "\n\n")
		joined := strings.Join(out, "")
		if !strings.Contains(joined, `"code":"boom"`) || !strings.Contains(joined, "response.failed") {
			t.Fatalf("error 事件应转 response.failed：%s", joined)
		}
		if again := state.ProcessChatSseEvent("data: [DONE]\n\n"); again != nil {
			t.Errorf("失败后幂等：%v", again)
		}
	})
	t.Run("Finish 中断兜底", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{})
		out := state.Finish()
		joined := strings.Join(out, "")
		if !strings.Contains(joined, `"code":"upstream_stream_interrupted"`) {
			t.Fatalf("中断应失败流：%s", joined)
		}
		// 已 started 时 Finish 失败不再重复 ensure start。
		if strings.Contains(joined, "response.created") {
			t.Errorf("未 start 过不应补 response.created：%s", joined)
		}
	})
	t.Run("estimated usage 回退", func(t *testing.T) {
		state := NewCodexChatToResponsesState(CodexResponsesChatBridgeTransformOptions{
			EstimatedInputTokens: int64Ptr(30),
		})
		state.AppendResponsesTextDelta("hello world") // ASCII 11 字符 -> 3 token
		usage := state.CompletedResponsesUsage()
		if usage["input_tokens"] != float64(30) {
			t.Errorf("estimated input = %v", usage["input_tokens"])
		}
		if usage["output_tokens"] == float64(0) {
			t.Errorf("estimated output 应大于 0：%v", usage)
		}
		if EstimatedBridgeOutputTokens := state.EstimatedBridgeOutputTokens(); EstimatedBridgeOutputTokens < 0 {
			t.Errorf("估算不应为负：%d", EstimatedBridgeOutputTokens)
		}
	})
	t.Run("Buffer JSON 失败快照", func(t *testing.T) {
		body := "event: error\ndata: " + `{"error":{"message":"崩"}}` + "\n\n"
		got := TransformChatCompletionsSseBufferToResponsesJSON([]byte(body), CodexResponsesChatBridgeTransformOptions{})
		if !strings.Contains(string(got), `"status":"failed"`) || !strings.Contains(string(got), "崩") {
			t.Fatalf("失败流应渲染 failed 快照：%s", got)
		}
	})
}

func TestCovEstimateCodexInputTokens(t *testing.T) {
	if got := EstimateCodexResponsesRequestInputTokens(nil); got != 0 {
		t.Errorf("nil body = %d", got)
	}
	if got := EstimateCodexResponsesRequestInputTokens(map[string]any{"input": "hello"}); got <= 0 {
		t.Errorf("非空 body 应大于 0：%d", got)
	}
}
