package chat

// R5 性能基准（docs/plans/计划-20260918T064119294Z-后端架构与性能优化改革.md
// 波次 R5）：chat 包热路径（上游 SSE 收集、图像结果剥离、transport 请求体
// 组装、工具事件投影、checkpoint 序列化）的 go test -bench 基线。
//
// 可重放约束：固定输入（循环外构造一次）、无真实时间等待（token 计数与
// TokenCount 注入均为纯长度计算，无时钟/随机/网络）；每个 Benchmark 在循环外
// 做一次正确性 sanity check，避免测到被优化的空路径。流式收集基准每轮在
// StopTimer 区间重建 io.Reader（消费型输入），与 response/dispatch harness
// 的 per-iteration 重建模式一致。
//
// 注意：本机初测存在并行负载，数字供热点排序参考；正式基线需空载复测。

import (
	"fmt"
	"strings"
	"testing"
)

// bench 系列包级 sink 防止纯函数调用被编译器消除。
var (
	benchSinkStripped  string
	benchSinkValues    []string
	benchSinkEndpoint  string
	benchSinkBody      map[string]any
	benchSinkToolEvent *ChatGenerationToolEvent
	benchSinkEntries   []CheckpointEntryInput
)

// benchChatDeltaText 是流式基准的固定文本增量（48 字节/事件）。
const benchChatDeltaText = "abcdefghabcdefghabcdefghabcdefghabcdefghabcdefgh"

// benchChatOnDelta 是流式收集的 no-op 增量回调（对应生产侧 appendTextLocked
// 的投影入口位置，回调本身不计业务成本）。
func benchChatOnDelta(string) {}

// benchBuildChatSseStream 构造固定的 OpenAI Chat SSE 成功序列：
// 32 个文本增量 → 2 个工具调用（id/name + 参数分片累计）→ finish+usage →
// [DONE]。事件分片与 w16c_sse_transport_test.go 同源。
func benchBuildChatSseStream() string {
	var sb strings.Builder
	deltaEvent := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + benchChatDeltaText + "\"}}]}\n\n"
	for i := 0; i < 32; i++ {
		sb.WriteString(deltaEvent)
	}
	sb.WriteString(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_bench_alpha","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"go bench\"}"}}]}}]}` + "\n\n")
	sb.WriteString(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":",\"limit\":10}"}}]}}]}` + "\n\n")
	sb.WriteString(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_bench_beta","type":"function","function":{"name":"diagnostic_echo","arguments":"{\"payload\":\"ok\"}"}}]}}]}` + "\n\n")
	sb.WriteString(`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":128,"completion_tokens":64}}` + "\n\n")
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

// BenchmarkChatCollectOpenAIChatSseContentToolUsage 量化 Chat Completions
// 上游 SSE 收集全链：字节读取 + UTF-8 校验 → 事件边界切分 → JSON 解析 →
// 内容累计与增量回调 → 工具参数跨事件拼接 → usage 提取 → 工具调用收尾。
func BenchmarkChatCollectOpenAIChatSseContentToolUsage(b *testing.B) {
	stream := benchBuildChatSseStream()
	result, err := CollectOpenAIChatSse(strings.NewReader(stream), 1<<20, benchChatOnDelta, 0)
	if err != nil {
		b.Fatalf("sanity collect: %v", err)
	}
	if !result.Done || len(result.Content) != 32*len(benchChatDeltaText) ||
		len(result.ToolCalls) != 2 || result.ToolCalls[0].CallID != "call_bench_alpha" ||
		result.ToolCalls[1].ArgumentsJSON != `{"payload":"ok"}` ||
		result.InputTokens == nil || *result.InputTokens != 128 ||
		result.OutputTokens == nil || *result.OutputTokens != 64 {
		b.Fatalf("sanity collect result: done=%v content=%d toolCalls=%+v tokens=%v/%v",
			result.Done, len(result.Content), result.ToolCalls, result.InputTokens, result.OutputTokens)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		reader := strings.NewReader(stream)
		b.StartTimer()
		loopResult, err := CollectOpenAIChatSse(reader, 1<<20, benchChatOnDelta, 0)
		if err != nil || !loopResult.Done {
			b.Fatalf("collect: err=%v done=%v", err, loopResult.Done)
		}
	}
}

// benchBuildResponsesSseStream 构造固定的 OpenAI Responses SSE 成功序列：
// 24 个文本增量 → 4 个 reasoning 增量 → function_call added/参数增量×4/done →
// response.completed（usage + output 数组）。事件形态与 w16c_sse_transport_test.go
// 与 w3_responses_sse_test.go 同源。
func benchBuildResponsesSseStream() string {
	var sb strings.Builder
	textEvent := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + benchChatDeltaText + "\"}\n\n"
	for i := 0; i < 24; i++ {
		sb.WriteString(textEvent)
	}
	reasoningEvent := `event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","delta":"分析问题要点"}` + "\n\n"
	for i := 0; i < 4; i++ {
		sb.WriteString(reasoningEvent)
	}
	sb.WriteString(`event: response.output_item.added` + "\n" + `data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_bench_1","call_id":"call_bench_alpha","name":"web_search","arguments":""}}` + "\n\n")
	argsDeltaEvent := `event: response.function_call_arguments.delta` + "\n" + `data: {"type":"response.function_call_arguments.delta","item_id":"fc_bench_1","delta":"{\"query\":\"go bench\"}"}` + "\n\n"
	for i := 0; i < 4; i++ {
		sb.WriteString(argsDeltaEvent)
	}
	sb.WriteString(`event: response.output_item.done` + "\n" + `data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_bench_1","call_id":"call_bench_alpha","name":"web_search","arguments":"{\"query\":\"go bench\",\"limit\":10}","status":"completed"}}` + "\n\n")
	sb.WriteString(`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"id":"resp_bench","usage":{"input_tokens":128,"output_tokens":64},"output":[{"type":"reasoning","id":"rs_bench"},{"type":"function_call","id":"fc_bench_1","call_id":"call_bench_alpha","name":"web_search","arguments":"{\"query\":\"go bench\",\"limit\":10}","status":"completed"}]}}` + "\n\n")
	return sb.String()
}

// BenchmarkChatCollectChatResponsesSseCompleted 量化 Responses 上游 SSE 收集
// 全链：块解析（事件名正则 + JSON 解码 + 图像显式判定）→ 文本/推理累计 →
// 工具参数增量累计 → 终态 continuation 归一与工具调用归一 → usage 提取。
func BenchmarkChatCollectChatResponsesSseCompleted(b *testing.B) {
	stream := benchBuildResponsesSseStream()
	result, err := CollectChatResponsesSse(strings.NewReader(stream), 1<<20, 0, nil, nil)
	if err != nil {
		b.Fatalf("sanity collect: %v", err)
	}
	if len(result.Content) != 24*len(benchChatDeltaText) ||
		result.InputTokens == nil || *result.InputTokens != 128 ||
		result.OutputTokens == nil || *result.OutputTokens != 64 ||
		len(result.ToolCalls) != 1 || result.ToolCalls[0].CallID != "call_bench_alpha" ||
		len(result.ContinuationItems) != 2 {
		b.Fatalf("sanity collect result: content=%d tokens=%v/%v toolCalls=%+v continuation=%d",
			len(result.Content), result.InputTokens, result.OutputTokens, result.ToolCalls, len(result.ContinuationItems))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		reader := strings.NewReader(stream)
		b.StartTimer()
		loopResult, err := CollectChatResponsesSse(reader, 1<<20, 0, nil, nil)
		if err != nil || len(loopResult.ToolCalls) != 1 {
			b.Fatalf("collect: err=%v toolCalls=%d", err, len(loopResult.ToolCalls))
		}
	}
}

// benchBuildImageResultDoc 构造含两个图像 base64 结果字段（result / b64_json，
// 各约 7.7 KiB、无转义字符）的终态 JSON 文档，与 stripImageResultStrings 的
// 扫描契约一致。
func benchBuildImageResultDoc() string {
	payload := strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVphYmNkZWZnaGlqa2xtbm9wcXJzdHV2d3h5ejAxMjM0NTY3ODk", 92)
	return `[{"type":"image_generation_call","call_id":"call_bench_1","result":"` + payload +
		`"},{"type":"image_generation_call","call_id":"call_bench_2","b64_json":"` + payload + `"}]`
}

// BenchmarkChatStripImageResultStrings 量化图像结果 base64 剥离扫描器
// （Responses 图像事件内存 spool 读取的热路径）：逐字符状态机 + 结果值收集。
func BenchmarkChatStripImageResultStrings(b *testing.B) {
	doc := benchBuildImageResultDoc()
	stripped, values, err := stripImageResultStrings(doc, "result", "b64_json")
	if err != nil {
		b.Fatalf("sanity strip: %v", err)
	}
	if len(values) != 2 || values[0] == "" || values[0] != values[1] ||
		strings.Contains(stripped, values[0]) ||
		!strings.Contains(stripped, `"result"`) || !strings.Contains(stripped, `"b64_json"`) {
		b.Fatalf("sanity strip result: values=%d strippedKeepsPayload=%v", len(values), strings.Contains(stripped, "QUJD"))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkStripped, benchSinkValues, err = stripImageResultStrings(doc, "result", "b64_json")
		if err != nil || len(benchSinkValues) != 2 {
			b.Fatalf("strip: err=%v values=%d", err, len(benchSinkValues))
		}
	}
}

// benchDiagnosticToolDef 是 transport 请求组装基准的最小内部工具定义
// （compileChatInternalTools 只读取展示字段，不触达 Execute）。
func benchDiagnosticToolDef() *toolDefinition {
	return &toolDefinition{
		ID:          "diagnostic_echo",
		Version:     "1",
		ModelName:   "diagnostic_echo",
		Description: "回显输入的探测工具（bench 夹具）。",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"payload": map[string]any{"type": "string"}},
			"required":   []string{"payload"},
		},
		MaxArgumentBytes: 4096,
		MaxResultBytes:   4096,
		TimeoutMs:        1000,
		Environments:     []string{"development", "production"},
	}
}

// benchBuildTransportInput 构造固定的 chat_completions 请求组装输入：
// 8 条历史 + 生成参数 + 内部工具 + 工具往返消息。输入跨迭代只读复用。
func benchBuildTransportInput() ChatTransportRequestInput {
	temperature, topP := 0.7, 0.9
	maxOutputTokens, seed := 4096.0, float64(42)
	history := make([]ChatTransportMessage, 0, 8)
	for i := 0; i < 8; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		history = append(history, ChatTransportMessage{Role: role, Content: fmt.Sprintf("历史消息 %02d：请继续基于上下文作答。", i)})
	}
	return ChatTransportRequestInput{
		Protocol:          ProtocolChatCompletions,
		Instructions:      "你是聚合网关的聊天助手，回答使用简体中文并保持要点先行。",
		Model:             "gpt-4o",
		History:           history,
		CurrentContent:    "总结当前网关链路的性能基线结论。",
		ToolContinuation:  []any{map[string]any{"role": "tool", "tool_call_id": "call_bench_alpha", "content": "{\"hits\":3}"}},
		InternalTools:     []*toolDefinition{benchDiagnosticToolDef()},
		ReasoningEffort:   "medium",
		ServiceTier:       "auto",
		GenerationParameters: &ChatGenerationParameters{Temperature: &temperature, TopP: &topP, MaxOutputTokens: &maxOutputTokens, Seed: &seed},
		PromptCacheKey:    "cache-key-bench",
	}
}

// BenchmarkChatBuildChatTransportRequestChatCompletions 量化上游请求体组装
// （每次生成轮次执行）：消息归一拼接 → 内部工具 schema 编译 → 生成参数
// 展开 → cache key / reasoning / service_tier 字段装配。
func BenchmarkChatBuildChatTransportRequestChatCompletions(b *testing.B) {
	input := benchBuildTransportInput()
	endpoint, body := buildChatTransportRequest(input)
	messages, _ := body["messages"].([]any)
	tools, _ := body["tools"].([]map[string]any)
	if endpoint != "/v1/chat/completions" || body["model"] != "gpt-4o" ||
		len(messages) != 11 || len(tools) != 1 ||
		body["temperature"] != 0.7 || body["prompt_cache_key"] != "cache-key-bench" ||
		body["reasoning_effort"] != "medium" {
		b.Fatalf("sanity request: endpoint=%q model=%v messages=%d tools=%d body=%v",
			endpoint, body["model"], len(messages), len(tools), body)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkEndpoint, benchSinkBody = buildChatTransportRequest(input)
		if benchSinkEndpoint != "/v1/chat/completions" || len(benchSinkBody) == 0 {
			b.Fatalf("request: endpoint=%q", benchSinkEndpoint)
		}
	}
}

// BenchmarkChatProjectToolEventUpdate 量化流式工具事件投影的更新路径
// （stream_execute.go 每个工具事件执行 projectToolEvent +
// chatGenerationToolEventProjection 两次投影）：已有工具块的状态/Item 原地
// 更新 + 对外工具事件构造。预置 4 个工具块保证跨迭代零增长。
func BenchmarkChatProjectToolEventUpdate(b *testing.B) {
	blocks := &[]*assistantBlock{
		{Type: "tool_call", CallID: "t1", ToolType: "web_search", Status: asstStarted},
		{Type: "tool_call", CallID: "t2", ToolType: "web_search", Status: asstStarted},
		{Type: "tool_call", CallID: "t3", ToolType: "web_search", Status: asstStarted},
		{Type: "tool_call", CallID: "t4", ToolType: "web_search", Status: asstStarted},
	}
	item := map[string]any{"id": "t2", "type": "web_search", "delta": "{\"query\":\"go\"}"}

	projectToolEvent(blocks, "tool_updated", item)
	if (*blocks)[1].Status != "updated" || (*blocks)[1].CallID != "t2" {
		b.Fatalf("sanity projection blocks = %+v", *blocks)
	}
	projection := chatGenerationToolEventProjection("tool_updated", item)
	if projection == nil || projection.ID != "t2" || projection.Status != "updated" {
		b.Fatalf("sanity tool event = %+v", projection)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		projectToolEvent(blocks, "tool_updated", item)
		benchSinkToolEvent = chatGenerationToolEventProjection("tool_updated", item)
		if (*blocks)[1].Status != "updated" || benchSinkToolEvent == nil {
			b.Fatal("projection update failed")
		}
	}
}

// benchBuildMemorySnapshot 构造固定的大容量记忆快照（32 条持久记忆 +
// 8 条工具结果 + 4 条图像记忆等），供 checkpoint 序列化基准使用。
func benchBuildMemorySnapshot() memorySnapshot {
	durable := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		durable = append(durable, fmt.Sprintf("用户偏好条目 %02d：偏好简体中文、要点先行并附带最小代码示例。", i))
	}
	completed := make([]string, 0, 16)
	for i := 0; i < 16; i++ {
		completed = append(completed, fmt.Sprintf("已完成：梳理模块 %02d 的调用链并补充基准覆盖。", i))
	}
	toolResults := make([]map[string]any, 0, 8)
	for i := 0; i < 8; i++ {
		toolResults = append(toolResults, map[string]any{
			"name":   fmt.Sprintf("diagnostic_echo_%02d", i),
			"result": "探测结果：目标服务健康，平均延迟 42ms，未发现错误日志。",
		})
	}
	imageMemories := make([]map[string]any, 0, 4)
	for i := 0; i < 4; i++ {
		imageMemories = append(imageMemories, map[string]any{
			"assetId":       fmt.Sprintf("chat_asset_%032d", i),
			"summary":       "截图显示网关监控面板，QPS 平稳，错误率接近 0。",
			"ocr":           []string{"QPS 1200", "错误率 0.1%"},
			"relevantFacts": []string{"高峰时段为 20:00-22:00"},
			"uncertainties": []string{},
		})
	}
	return memorySnapshot{
		DurableMemory:        durable,
		CurrentGoal:          "为 chat 包热路径补齐 R5 基准并确认无回归；输出可重放的基线数字。",
		Constraints:          []string{"不得修改既有文件", "禁用真实网络", "固定时钟与固定输入"},
		Decisions:            []string{"基准夹具复用既有测试模式", "数字标注并行负载"},
		Completed:            completed,
		Pending:              []string{"空载复测基线", "评估 SSE 收集的分配热点"},
		ImportantToolResults: toolResults,
		ImageMemories:        imageMemories,
		RecentUserIntent:     "继续推进后端性能优化改革的 R5 波次。",
		Uncertainties:        []string{"并行负载对绝对值的影响幅度"},
	}
}

// BenchmarkChatCheckpointSnapshotEntries 量化 checkpoint 序列化
// （snapshotEntries → checkpointEntry：逐条 json.Marshal + TokenCount 估算，
// 压缩服务落库前的热路径）。TokenCount 注入固定长度估算，与时钟/随机解耦。
func BenchmarkChatCheckpointSnapshotEntries(b *testing.B) {
	service := &CompactionService{TokenCount: func(text string) int { return (len(text) + 3) / 4 }}
	snapshot := benchBuildMemorySnapshot()
	entries := snapshotEntries(service, snapshot)
	if len(entries) != 4 || entries[0].Kind != "durable_memory" || entries[3].Kind != "image_observation" ||
		entries[0].TokenCount == nil || *entries[0].TokenCount <= 0 ||
		entries[0].TrustLevel != "assistant_derived" || len(entries[0].Content) == 0 {
		b.Fatalf("sanity entries = %+v", entries)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkEntries = snapshotEntries(service, snapshot)
		if len(benchSinkEntries) != 4 {
			b.Fatalf("entries = %d", len(benchSinkEntries))
		}
	}
}
