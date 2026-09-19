package gatewayresponse

// R5 性能基准（docs/plans/计划-20260918T064119294Z-后端架构与性能优化改革.md
// 波次 R5）：/v1 网关链 response 侧热路径的 go test -bench 基线。
//
// 可重放约束：固定 SSE 分片输入（chatDeltaChunk / chatFinishChunk / chatDoneChunk
// 与既有流测试同源）、NowMs 注入固定时钟（无真实时间等待）、循环外一次性
// 正确性 sanity check；夹具复用既有 mock（newInputFixture / mockUsageRecords /
// mockAccountEffects / SliceUpstreamBody）。
//
// 已知热路径成本（本基准目的即量化它）：raceStreamReadWithDeadlines 每次分片
// 读取都分配真实 time.After 定时器并走 reflect.Select，属于当前生产行为。
//
// 注意：本机初测存在并行负载，数字供热点排序参考；正式基线需空载复测。

import (
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// benchStreamChunks 是 OpenAI chat SSE 成功序列：delta → finish+usage → [DONE]。
var benchStreamChunks = [][]byte{
	[]byte(chatDeltaChunk),
	[]byte(chatFinishChunk),
	[]byte(chatDoneChunk),
}

// benchNonStreamChatPayload 与 finalize_test.go 的 chat JSON 成功用例同源。
const benchNonStreamChatPayload = `{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"你好"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}`

func benchTimeoutProfile() TimeoutProfile {
	return TimeoutProfile{
		FirstResponseTimeoutMs:          60_000,
		IdleTimeoutMs:                   30_000,
		UncommittedAttemptMaxLifetimeMs: 300_000,
	}
}

func benchNoopStreamFailure(string, string, StreamFailureContext) error { return nil }

func benchFinalizationDeps() *FinalizationDeps {
	return &FinalizationDeps{
		UsageRecords:   &mockUsageRecords{},
		AccountEffects: &mockAccountEffects{},
		NowMs:          func() int64 { return 1000 },
	}
}

// BenchmarkResponsePipeUpstreamStreamOpenAIChatSSE 量化 SSE 泵本体：分片读取
// 竞速（reflect.Select + 定时器分配）→ SSE 事件切分 → openai 语义检查 →
// 下游 passthrough 写出 → usage 提取。NowMs 固定为 StartedAtMs，超时分支
// 永不触发。
func BenchmarkResponsePipeUpstreamStreamOpenAIChatSSE(b *testing.B) {
	sanity, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody:        NewSliceUpstreamBody(benchStreamChunks...),
		Downstream:          StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile:      benchTimeoutProfile(),
		StartedAtMs:         1000,
		HandleStreamFailure: benchNoopStreamFailure,
		Signal:              staticSignal(),
		Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
	})
	if err != nil {
		b.Fatalf("sanity pipe: %v", err)
	}
	if !sanity.Completed || sanity.Usage.InputTokens == nil || *sanity.Usage.InputTokens != 5 ||
		sanity.Usage.OutputTokens == nil || *sanity.Usage.OutputTokens != 7 {
		b.Fatalf("sanity pipe result: completed=%v usage=%+v", sanity.Completed, sanity.Usage)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		input := PipeUpstreamStreamInput{
			UpstreamBody:        NewSliceUpstreamBody(benchStreamChunks...),
			Downstream:          StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
			TimeoutProfile:      benchTimeoutProfile(),
			StartedAtMs:         1000,
			HandleStreamFailure: benchNoopStreamFailure,
			Signal:              staticSignal(),
			Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
		}
		b.StartTimer()
		result, err := PipeUpstreamStream(input)
		if err != nil {
			b.Fatalf("pipe: %v", err)
		}
		if !result.Completed {
			b.Fatal("pipe not completed")
		}
	}
}

// BenchmarkResponsePipeUpstreamStreamOpenAIChatSSETimeoutsDisabled 是 SSE 泵的
// 定时器隔离对照：TimeoutsDisabled=true 时 read plan 为 nil、竞速零 time.After
// 分配。与上一基准的差值即“超时竞速（定时器分配 + reflect.Select 多路）”的
// 每次泵成本。
func BenchmarkResponsePipeUpstreamStreamOpenAIChatSSETimeoutsDisabled(b *testing.B) {
	disabledProfile := TimeoutProfile{TimeoutsDisabled: true}
	sanity, err := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody:        NewSliceUpstreamBody(benchStreamChunks...),
		Downstream:          StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
		TimeoutProfile:      disabledProfile,
		StartedAtMs:         1000,
		HandleStreamFailure: benchNoopStreamFailure,
		Signal:              staticSignal(),
		Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
	})
	if err != nil {
		b.Fatalf("sanity pipe: %v", err)
	}
	if !sanity.Completed || sanity.Usage.InputTokens == nil || *sanity.Usage.InputTokens != 5 ||
		sanity.Usage.OutputTokens == nil || *sanity.Usage.OutputTokens != 7 {
		b.Fatalf("sanity pipe result: completed=%v usage=%+v", sanity.Completed, sanity.Usage)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		input := PipeUpstreamStreamInput{
			UpstreamBody:        NewSliceUpstreamBody(benchStreamChunks...),
			Downstream:          StreamDownstream{Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder())},
			TimeoutProfile:      disabledProfile,
			StartedAtMs:         1000,
			HandleStreamFailure: benchNoopStreamFailure,
			Signal:              staticSignal(),
			Options:             StreamPipeOptions{NowMs: func() int64 { return 1000 }},
		}
		b.StartTimer()
		result, err := PipeUpstreamStream(input)
		if err != nil {
			b.Fatalf("pipe: %v", err)
		}
		if !result.Completed {
			b.Fatal("pipe not completed")
		}
	}
}

// BenchmarkResponseHandleStreamUpstreamResponseOpenAIChatSSE 量化流式编排全链
// （/v1 消费的导出入口）：driver 解析 → 检查策略装配 → SSE 泵 → usage fallback →
// 审计 completeAttempt。per-iteration 重建 input/mocks（writer 与审计记录
// 一次性消费），用 StopTimer/StartTimer 移出计量区。
func BenchmarkResponseHandleStreamUpstreamResponseOpenAIChatSSE(b *testing.B) {
	input, _ := newInputFixture(NewSliceUpstreamBody(benchStreamChunks...), 200, nil)
	input.Deps = benchFinalizationDeps()
	sanity, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		b.Fatalf("sanity stream handle: %v", err)
	}
	if sanity.AlreadyFinalized || sanity.RetryUpstream || !sanity.ProtocolValidatedSuccess ||
		sanity.Usage.OutputTokens == nil || *sanity.Usage.OutputTokens != 7 {
		b.Fatalf("sanity stream result: %+v", sanity)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		runInput, _ := newInputFixture(NewSliceUpstreamBody(benchStreamChunks...), 200, nil)
		runInput.Deps = benchFinalizationDeps()
		b.StartTimer()
		result, err := HandleStreamUpstreamResponse(runInput)
		if err != nil {
			b.Fatalf("stream handle: %v", err)
		}
		if !result.ProtocolValidatedSuccess {
			b.Fatalf("stream result: %+v", result)
		}
	}
}

// BenchmarkResponseHandleNonStreamUpstreamResponseChatJSON 量化非流式编排：
// JSON 读取计划 → 协议结构校验 → usage 提取 → 下游透传 → 审计 completeAttempt。
func BenchmarkResponseHandleNonStreamUpstreamResponseChatJSON(b *testing.B) {
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(benchNonStreamChatPayload)), 200,
		map[string]string{"Content-Type": "application/json"})
	sanity, err := HandleNonStreamUpstreamResponse(input)
	if err != nil {
		b.Fatalf("sanity non-stream: %v", err)
	}
	if sanity.AlreadyFinalized || !sanity.ProtocolValidatedSuccess ||
		sanity.Usage.InputTokens == nil || *sanity.Usage.InputTokens != 8 ||
		sanity.Usage.OutputTokens == nil || *sanity.Usage.OutputTokens != 2 {
		b.Fatalf("sanity non-stream result: %+v", sanity)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		runInput, _ := newInputFixture(NewSliceUpstreamBody([]byte(benchNonStreamChatPayload)), 200,
			map[string]string{"Content-Type": "application/json"})
		runInput.Deps = benchFinalizationDeps()
		b.StartTimer()
		result, err := HandleNonStreamUpstreamResponse(runInput)
		if err != nil {
			b.Fatalf("non-stream: %v", err)
		}
		if !result.ProtocolValidatedSuccess {
			b.Fatalf("non-stream result: %+v", result)
		}
	}
}

// BenchmarkResponseFinalizeHandledUpstreamResponseSuccess 量化 usage/审计收尾
// （G17 触发点）：usage 快照组装 → RecordCompletedUpstreamAttempt → 审计
// finalize(success)。per-iteration 重建 usage mock（记录切片一次性消费）。
func BenchmarkResponseFinalizeHandledUpstreamResponseSuccess(b *testing.B) {
	deps := &FinalizationDeps{UsageRecords: &mockUsageRecords{}}
	input, _ := newInputFixture(nil, 200, nil)
	input.Deps = deps
	result := UpstreamResponseHandlingResult{
		Usage:                    gatewayprotoParsedUsage(3, 4),
		ProtocolValidatedSuccess: true,
	}
	FinalizeHandledUpstreamResponse(input, result)
	usage := deps.UsageRecords.(*mockUsageRecords)
	if len(usage.completed) != 1 || !usage.completed[0].Success ||
		usage.completed[0].Usage.InputTokens == nil || *usage.completed[0].Usage.InputTokens != 3 {
		b.Fatalf("sanity finalize records = %+v", usage.completed)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		runInput, _ := newInputFixture(nil, 200, nil)
		runDeps := &FinalizationDeps{UsageRecords: &mockUsageRecords{}}
		runInput.Deps = runDeps
		runResult := UpstreamResponseHandlingResult{
			Usage:                    gatewayprotoParsedUsage(3, 4),
			ProtocolValidatedSuccess: true,
		}
		b.StartTimer()
		FinalizeHandledUpstreamResponse(runInput, runResult)
	}
}
