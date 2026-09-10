package chat

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// CollectChatResponsesSse 是 OpenAI Responses SSE 采集器的契约入口：按空行分帧、
// 按 data JSON 的 type 分发事件、汇聚正文/用量/工具调用与续答项目，并在缺少
// response.completed 时整体失败。本文件覆盖采集主流程、分块解析器与图像
// base64 剥离状态机。

func errReaderW3() io.Reader {
	return io.MultiReader(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n"), errAtEOFReader{})
}

type errAtEOFReader struct{}

func (errAtEOFReader) Read([]byte) (int, error) { return 0, errors.New("读取失败") }

func responsesBlockW3(eventName, dataJSON string) string {
	if eventName == "" {
		return "data: " + dataJSON + "\n\n"
	}
	return "event: " + eventName + "\ndata: " + dataJSON + "\n\n"
}

// TestCollectResponsesSseW3HappyPath 覆盖文本增量 + 用量 + 函数调用完成的完整链路。
func TestCollectResponsesSseW3HappyPath(t *testing.T) {
	payload := responsesBlockW3("response.output_text.delta", `{"type":"response.output_text.delta","delta":"你好"}`) +
		responsesBlockW3("response.reasoning_summary_text.delta", `{"type":"response.reasoning_summary_text.delta","delta":"思考"}`) +
		responsesBlockW3("response.completed", `{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5},"output":[{"type":"reasoning"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"tool","arguments":"{\"a\":1}","status":"completed"}]}}`)
	var events []ChatResponsesEvent
	result, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, func(event ChatResponsesEvent) error {
		events = append(events, event)
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.Content != "你好" {
		t.Fatalf("Content = %q, 期望 你好", result.Content)
	}
	if result.InputTokens == nil || *result.InputTokens != 10 || result.OutputTokens == nil || *result.OutputTokens != 5 {
		t.Fatalf("用量不正确: %+v", result)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].CallID != "call_1" || result.ToolCalls[0].ToolName != "tool" || result.ToolCalls[0].ArgumentsJSON != `{"a":1}` {
		t.Fatalf("工具调用不正确: %+v", result.ToolCalls)
	}
	if result.ToolCalls[0].SourceOrder != 1 {
		t.Fatalf("SourceOrder = %d, 期望按输出序号 1", result.ToolCalls[0].SourceOrder)
	}
	if len(result.ContinuationItems) != 2 {
		t.Fatalf("续答项目数量 = %d, 期望 2", len(result.ContinuationItems))
	}
	if len(events) != 3 {
		t.Fatalf("事件数量 = %d, 期望 3", len(events))
	}
}

// TestCollectResponsesSseW3CompletedItemsFallback 覆盖 completed 无 output 时
// 从 output_item.done 事件重建输出（含乱序 index 排序）。
func TestCollectResponsesSseW3CompletedItemsFallback(t *testing.T) {
	doneItem := func(index int, callID string) string {
		return responsesBlockW3("response.output_item.done", `{"type":"response.output_item.done","output_index":`+itoa(index)+`,"item":{"type":"function_call","id":"fc_`+itoa(index)+`","call_id":"`+callID+`","name":"tool","arguments":"{}","status":"completed"}}`)
	}
	payload := doneItem(2, "call_2") + doneItem(0, "call_0") +
		responsesBlockW3("response.completed", `{"type":"response.completed","response":{"usage":{},"output":[]}}`)
	result, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, nil)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("工具调用数量 = %d, 期望 2", len(result.ToolCalls))
	}
	// 重建顺序必须按 output_index 升序：call_0 在前。
	if result.ToolCalls[0].CallID != "call_0" || result.ToolCalls[1].CallID != "call_2" {
		t.Fatalf("重建顺序不正确: %+v", result.ToolCalls)
	}
	if result.ToolCalls[0].SourceOrder != 0 {
		t.Fatalf("SourceOrder = %d, 期望 0", result.ToolCalls[0].SourceOrder)
	}
}

// TestCollectResponsesSseW3ArgumentDeltas 覆盖 function_call 参数增量累积：
// output_item.done 的 item 缺 arguments 时用增量拼回。
func TestCollectResponsesSseW3ArgumentDeltas(t *testing.T) {
	payload := responsesBlockW3("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"tool"}}`) +
		responsesBlockW3("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"{\"a\":"}`) +
		responsesBlockW3("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"1}"}`) +
		responsesBlockW3("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"item_1","call_id":"call_1","name":"tool","status":"completed"}}`) +
		responsesBlockW3("response.completed", `{"type":"response.completed","response":{"output":[]}}`)
	result, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, nil)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ArgumentsJSON != `{"a":1}` {
		t.Fatalf("参数增量未拼回: %+v", result.ToolCalls)
	}
	if len(result.ContinuationItems) != 1 {
		t.Fatalf("续答项目数量 = %d, 期望 1", len(result.ContinuationItems))
	}
}

// TestCollectResponsesSseW3FailedEvent 覆盖 response.failed 三种载荷形态。
func TestCollectResponsesSseW3FailedEvent(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"response 载荷", `{"type":"response.failed","response":{"error":{"message":"上游失败"}}}`},
		{"error 载荷", `{"type":"response.failed","error":{"message":"上游失败"}}`},
		{"空载荷", `{"type":"response.failed"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := responsesBlockW3("response.failed", testCase.data) +
				responsesBlockW3("response.completed", `{"type":"response.completed","response":{}}`)
			var failed bool
			_, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, func(event ChatResponsesEvent) error {
				if event.Type == "failed" {
					failed = true
				}
				return nil
			}, nil)
			if err != nil {
				t.Fatalf("采集失败: %v", err)
			}
			if !failed {
				t.Fatalf("未收到 failed 事件")
			}
		})
	}
}

// TestCollectResponsesSseW3ImageCompleted 覆盖图像完成事件的 base64 抽取与去重。
func TestCollectResponsesSseW3ImageCompleted(t *testing.T) {
	imageDone := responsesBlockW3("response.image_generation_call.completed",
		`{"type":"response.image_generation_call.completed","call_id":"img_1","revised_prompt":"一只猫","result":"QUJD"}`)
	payload := imageDone + imageDone +
		responsesBlockW3("response.completed", `{"type":"response.completed","response":{}}`)
	var sinks []string
	_, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, func(callID, revisedPrompt string, chunks []string) error {
		sinks = append(sinks, callID+"|"+revisedPrompt+"|"+strings.Join(chunks, ","))
		return nil
	})
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if len(sinks) != 1 {
		t.Fatalf("同一 call_id 的图像完成应去重，实际回调 %d 次", len(sinks))
	}
	if sinks[0] != "img_1|一只猫|QUJD" {
		t.Fatalf("图像结果不正确: %q", sinks[0])
	}
}

// TestCollectResponsesSseW3OversizeImageEvent 覆盖超过 64 KiB 的图像事件放行。
func TestCollectResponsesSseW3OversizeImageEvent(t *testing.T) {
	bigResult := strings.Repeat("Q", 70*1024)
	payload := responsesBlockW3("response.image_generation_call.completed",
		`{"type":"response.image_generation_call.completed","call_id":"img_big","result":"`+bigResult+`"}`) +
		responsesBlockW3("response.completed", `{"type":"response.completed","response":{}}`)
	var length int
	_, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, func(callID string, _ string, chunks []string) error {
		if callID == "img_big" {
			length = len(strings.Join(chunks, ""))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("大体积图像事件不应报错: %v", err)
	}
	if length != 70*1024 {
		t.Fatalf("base64 载荷长度 = %d, 期望 %d", length, 70*1024)
	}
}

// TestCollectResponsesSseW3Errors 表驱动覆盖各种失败路径与错误文案。
func TestCollectResponsesSseW3Errors(t *testing.T) {
	oversizeDelta := `{"type":"response.output_text.delta","delta":"` + strings.Repeat("字", 40*1024) + `"}`
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"缺少 completed", responsesBlockW3("", `{"type":"response.output_text.delta","delta":"hi"}`), "上游 Responses 流缺少 response.completed"},
		{"非图像事件超 64KiB", responsesBlockW3("", oversizeDelta), "上游 Responses 单个事件超过 64 KiB 上限"},
		{"事件数超限", responsesBlockW3("", `{"type":"response.output_text.delta","delta":"a"}`) + responsesBlockW3("", `{"type":"response.output_text.delta","delta":"b"}`) + responsesBlockW3("response.completed", `{"type":"response.completed","response":{}}`), "事件数量超过 2 上限"},
		{"无效 UTF-8", "data: {\"delta\":\"\xff\"}\n\n", "上游返回了无效的 SSE JSON"},
		{"悬挂图像块", "event: response.image_generation_call.completed\ndata: {\"result\":\"" + strings.Repeat("Q", 70*1024), "图像 SSE 事件被截断"},
		{"悬挂普通块超限", "data: {\"delta\":\"" + strings.Repeat("字", 40*1024), "上游 Responses 单个事件超过 64 KiB 上限"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := CollectChatResponsesSse(strings.NewReader(testCase.payload), maxMessageBytes, 2, nil, nil)
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("err = %v, 期望包含 %q", err, testCase.wantErr)
			}
		})
	}
	t.Run("读取失败", func(t *testing.T) {
		if _, err := CollectChatResponsesSse(errReaderW3(), maxMessageBytes, 0, nil, nil); err == nil || !strings.Contains(err.Error(), "读取失败") {
			t.Fatalf("err = %v, 期望读取失败透传", err)
		}
	})
	t.Run("正文超上限", func(t *testing.T) {
		_, err := CollectChatResponsesSse(strings.NewReader(responsesBlockW3("", `{"type":"response.output_text.delta","delta":"0123456789ABCDEF"}`)), 8, 0, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "192 KiB 上限") {
			t.Fatalf("err = %v, 期望正文超限", err)
		}
	})
	t.Run("辅助过程超上限", func(t *testing.T) {
		delta := `{"type":"response.reasoning_summary_text.delta","delta":"` + strings.Repeat("a", 50*1024) + `"}`
		payload := strings.Repeat(responsesBlockW3("", delta), 5)
		_, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "模型结构化过程超过 192 KiB 上限") {
			t.Fatalf("err = %v, 期望辅助过程超限", err)
		}
	})
	t.Run("工具参数超上限", func(t *testing.T) {
		delta := `{"type":"response.function_call_arguments.delta","item_id":"item_1","delta":"` + strings.Repeat("a", 40*1024) + `"}`
		payload := strings.Repeat(responsesBlockW3("", delta), 3)
		_, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "单个工具参数超过 64 KiB 上限") {
			t.Fatalf("err = %v, 期望工具参数超限", err)
		}
	})
	t.Run("onEvent 错误透传", func(t *testing.T) {
		_, err := CollectChatResponsesSse(strings.NewReader(responsesBlockW3("", `{"type":"response.output_text.delta","delta":"x"}`)), maxMessageBytes, 0, func(ChatResponsesEvent) error {
			return errors.New("投影失败")
		}, nil)
		if err == nil || err.Error() != "投影失败" {
			t.Fatalf("err = %v, 期望 onEvent 错误透传", err)
		}
	})
	t.Run("onImageResult 错误透传", func(t *testing.T) {
		payload := responsesBlockW3("response.image_generation_call.completed", `{"type":"response.image_generation_call.completed","call_id":"img_1","result":"QUJD"}`)
		_, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, func(string, string, []string) error {
			return errors.New("落盘失败")
		})
		if err == nil || err.Error() != "落盘失败" {
			t.Fatalf("err = %v, 期望 onImageResult 错误透传", err)
		}
	})
	t.Run("function_call 缺字段", func(t *testing.T) {
		payload := responsesBlockW3("response.completed", `{"type":"response.completed","response":{"output":[{"type":"function_call","call_id":"call_1","name":"tool","status":"completed"}]}}`)
		_, err := CollectChatResponsesSse(strings.NewReader(payload), maxMessageBytes, 0, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "缺少 call_id、name 或 arguments") {
			t.Fatalf("err = %v, 期望缺字段报错", err)
		}
	})
}

// TestParseResponsesBlockW3 表驱动覆盖分块解析器的全部事件类型。
func TestParseResponsesBlockW3(t *testing.T) {
	cases := []struct {
		name      string
		block     string
		eventType string
	}{
		{"DONE 哨兵", "data: [DONE]", ""},
		{"空数据", "data: ", ""},
		{"非 JSON", "data: not-json", ""},
		{"文本增量", responsesBlockW3("", `{"type":"response.output_text.delta","delta":"hi"}`), "text_delta"},
		{"推理增量", responsesBlockW3("", `{"type":"response.reasoning_text.delta","delta":"hmm"}`), "reasoning_delta"},
		{"图像开始", responsesBlockW3("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"image_generation_call","id":"img_1"}}`), "image_started"},
		{"工具开始", responsesBlockW3("", `{"type":"response.output_item.added","item":{"type":"web_search_call","id":"ws_1"}}`), "tool_started"},
		{"未知输出项", responsesBlockW3("", `{"type":"response.output_item.added","item":{"type":"message"}}`), ""},
		{"工具完成", responsesBlockW3("", `{"type":"response.output_item.done","output_index":3,"item":{"type":"file_search_call","id":"fs_1"}}`), "tool_completed"},
		{"推理完成", responsesBlockW3("", `{"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning"}}`), "reasoning_completed"},
		{"未知完成项", responsesBlockW3("", `{"type":"response.output_item.done","output_index":1,"item":{"type":"message"}}`), ""},
		{"completed", responsesBlockW3("", `{"type":"response.completed","response":{"output":[]}}`), "completed"},
		{"事件名兜底类型", responsesBlockW3("response.output_text.delta", `{"delta":"hi"}`), "text_delta"},
		{"图像事件名直传", responsesBlockW3("response.image_generation_call.failed", `{"call_id":"img_1"}`), "image_failed"},
		{"图像 partial", responsesBlockW3("response.image_generation_call.partial_image", `{"call_id":"img_1"}`), "image_updated"},
		{"图像 in_progress", responsesBlockW3("response.image_generation_call.in_progress", `{"call_id":"img_1"}`), "image_updated"},
		{"图像完成无结果", responsesBlockW3("response.image_generation_call.completed", `{"call_id":"img_1"}`), "image_failed"},
		// 行为存疑：output_item.done 内嵌 image_generation_call 时不会命中图像分支，
		// 事件被丢弃（added 分支有显式处理，done 分支没有）。
		{"图像 output_item done", responsesBlockW3("response.output_item.done", `{"item":{"type":"image_generation_call","call_id":"img_1","result":"QUJD"}}`), ""},
		{"图像 output_item added", responsesBlockW3("response.output_item.added", `{"item":{"type":"image_generation_call","call_id":"img_1"}}`), "image_started"},
		{"图像 generation 前缀", responsesBlockW3("image_generation.partial", `{"call_id":"img_1"}`), "image_started"},
		{"图像默认开始", responsesBlockW3("response.image_generation_call.other", `{"call_id":"img_1"}`), "image_started"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed := parseResponsesBlock(testCase.block)
			got := ""
			if parsed.event != nil {
				got = parsed.event.Type
			}
			if got != testCase.eventType {
				t.Fatalf("event type = %q, 期望 %q", got, testCase.eventType)
			}
		})
	}
	t.Run("图像完成携带 base64", func(t *testing.T) {
		parsed := parseResponsesBlock(responsesBlockW3("response.image_generation_call.completed", `{"call_id":"img_1","result":"QUJD"}`))
		if parsed.event == nil || parsed.event.Type != "image_completed" || parsed.imageResultData == "" {
			t.Fatalf("解析结果不正确: %+v", parsed)
		}
		if chunks := extractImageResultChunks(parsed.imageResultData); len(chunks) != 1 || chunks[0] != "QUJD" {
			t.Fatalf("chunks = %v, 期望 [QUJD]", chunks)
		}
	})
	t.Run("输出项 index 记录", func(t *testing.T) {
		parsed := parseResponsesBlock(responsesBlockW3("", `{"type":"response.output_item.done","output_index":7,"item":{"type":"reasoning"}}`))
		if parsed.completedOutputItem == nil || parsed.completedOutputItem.index != 7 {
			t.Fatalf("completedOutputItem = %+v, 期望 index 7", parsed.completedOutputItem)
		}
	})
}

// TestStripImageResultStringsW3 覆盖 base64 剥离状态机的正反两条路径。
func TestStripImageResultStringsW3(t *testing.T) {
	t.Run("正常抽取", func(t *testing.T) {
		data := `{"a":1,"result":"QUJD","b64_json":"WFla","name":"r\"esult"}`
		sanitized, values, err := stripImageResultStrings(data, "result", "b64_json")
		if err != nil {
			t.Fatalf("剥离失败: %v", err)
		}
		if !strings.Contains(sanitized, `"a":1`) || strings.Contains(sanitized, "QUJD") {
			t.Fatalf("净化载荷不正确: %s", sanitized)
		}
		if !reflect.DeepEqual(values, []string{"QUJD", "WFla"}) {
			t.Fatalf("values = %v", values)
		}
	})
	t.Run("result 内出现转义", func(t *testing.T) {
		if _, _, err := stripImageResultStrings(`{"result":"AB\\CD"}`, "result"); err == nil || !strings.Contains(err.Error(), "不允许 JSON 转义") {
			t.Fatalf("err = %v, 期望转义报错", err)
		}
	})
	t.Run("result 为空字符串", func(t *testing.T) {
		if _, _, err := stripImageResultStrings(`{"result":""}`, "result"); err == nil || !strings.Contains(err.Error(), "不能为空") {
			t.Fatalf("err = %v, 期望空值报错", err)
		}
	})
	t.Run("JSON 截断", func(t *testing.T) {
		if _, _, err := stripImageResultStrings(`{"result":"QUJD`, "result"); err == nil || !strings.Contains(err.Error(), "被截断") {
			t.Fatalf("err = %v, 期望截断报错", err)
		}
	})
	t.Run("同名字符串键不误伤", func(t *testing.T) {
		data := `{"nameresult":"keep","result":"PAYLOAD"}`
		_, values, err := stripImageResultStrings(data, "result")
		if err != nil {
			t.Fatalf("剥离失败: %v", err)
		}
		if !reflect.DeepEqual(values, []string{"PAYLOAD"}) {
			t.Fatalf("values = %v", values)
		}
	})
}

// TestResponsesHelpersW3 覆盖零散 helper 的边界。
func TestResponsesHelpersW3(t *testing.T) {
	if got := normalizeResponsesContinuationItem(map[string]any{"type": "reasoning"}); len(got) != 1 {
		t.Fatalf("非 function_call 应原样返回: %v", got)
	}
	fallback := normalizeResponsesContinuationItem(map[string]any{"type": "function_call", "call_id": "call-AB!"})
	if fallback["id"] != "fc_-AB_" {
		t.Fatalf("fallback id = %v, 期望 fc_-AB_", fallback["id"])
	}
	if fallback["status"] != "completed" {
		t.Fatalf("补齐 status = %v, 期望 completed", fallback["status"])
	}
	longID := normalizeResponsesContinuationItem(map[string]any{"type": "function_call", "call_id": strings.Repeat("a", 80)})
	if len(longID["id"].(string)) > 63 {
		t.Fatalf("fallback id 超长: %v", longID["id"])
	}
	kept := normalizeResponsesContinuationItem(map[string]any{"type": "function_call", "call_id": "call_1", "id": "own_id"})
	if kept["id"] != "own_id" {
		t.Fatalf("已有 id 不应被覆盖: %v", kept["id"])
	}
	if objectItem("not-a-map") == nil || len(objectItem(map[string]any{"a": 1})) != 1 {
		t.Fatalf("objectItem 契约不正确")
	}
	if got := stringValueItem(map[string]any{"k": "  v  "}, "k", "missing"); got != "  v  " {
		t.Fatalf("stringValueItem 应返回原值: %q", got)
	}
	if got := stringValueItem(map[string]any{"k": 1}, "k"); got != "" {
		t.Fatalf("stringValueItem 非字符串应返回空: %q", got)
	}
	if values := sortInt64sCopyW3([]int64{3, 1, 2}); !reflect.DeepEqual(values, []int64{1, 2, 3}) {
		t.Fatalf("sortInt64s 结果不正确: %v", values)
	}
	if got := responsesImageCallID(map[string]any{"call_id": " c1 "}); got != "c1" {
		t.Fatalf("responsesImageCallID = %q", got)
	}
	if got := responsesImageCallID(map[string]any{"id": "id1"}); got != "id1" {
		t.Fatalf("responsesImageCallID id 兜底失败: %q", got)
	}
	if isPendingImageBlock("event: response.image_generation_call.completed") != true {
		t.Fatalf("isPendingImageBlock 事件名命中失败")
	}
	if isPendingImageBlock(`{"type":"image_generation_call"}`) != true {
		t.Fatalf("isPendingImageBlock type 命中失败")
	}
	if isPendingImageBlock("plain data") {
		t.Fatalf("isPendingImageBlock 误报")
	}
	if isExplicitImageBlock("", "image_generation.custom") != true {
		t.Fatalf("isExplicitImageBlock 前缀命中失败")
	}
	if got := extractEventName("event:  response.completed \n"); got != "response.completed" {
		t.Fatalf("extractEventName = %q", got)
	}
	if got := sseDataJSON("data: a\ndata: b"); got != "a\nb" {
		t.Fatalf("sseDataJSON 多行合并失败: %q", got)
	}
	if got := extractJSONStringField(`{"call_id":"abc"}`, "call_id"); got != "abc" {
		t.Fatalf("extractJSONStringField = %q", got)
	}
	if hasJSONStringField(`{"result":"x"}`, "result") != true {
		t.Fatalf("hasJSONStringField 命中失败")
	}
	if completedResponseImages(map[string]any{"output": []any{map[string]any{"type": "image_generation_call", "id": "i1", "result": "x"}, map[string]any{"type": "image_generation_call", "id": "i2"}, "junk"}}) == nil {
		t.Fatalf("completedResponseImages 不应返回 nil")
	}
	if got := ChatGenerationErrorMessage(GenErrInternal); got == "" {
		t.Fatalf("ChatGenerationErrorMessage 不应返回空")
	}
}

func sortInt64sCopyW3(values []int64) []int64 {
	out := append([]int64{}, values...)
	sortInt64s(out)
	return out
}
