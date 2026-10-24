package gatewayresponse

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
)

// 流式提交边界受限重放与异常终局处置设计（docs/functions/流式提交边界受限
// 重放与异常终局处置设计.md v2.2）§6 验收矩阵的管道级确定性用例（w22_ 前缀，
// 镜像 W22 生产事故形态）。链级归因三面与耗尽终态在 cmd 包测试覆盖。

// W22 粘连载荷形态：半截 chunk + 错误帧粘连成单个非法 JSON data 载荷。
const w22StickyBadPayload = "data: {\"choices\":[{\"delta\":{\"content\":\"上级\"}]} data: {\"error\":{\"message\":\"upstream sent an error event\",\"type\":\"upstream_error\"}}\n\n"

const w22TopLevelErrorFrame = "data: {\"error\":{\"message\":\"上游流内签名校验失败\",\"code\":\"thinking_signature_invalid\",\"type\":\"invalid_request_error\"}}\n\n"

const w22FinishChunk = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"

// w22WindowOptions 返回生产同源窗口启用选项（文本主路径：预提交缓冲开启 +
// generic 画像不解释语义），NowMs 固定时钟。
func w22WindowOptions(nowMs func() int64) StreamPipeOptions {
	return StreamPipeOptions{
		ClientRetryEnabled:                    false,
		InterpretProtocolFailures:             false,
		InterpretProtocolFailuresSet:          true,
		RetryBeforeDownstreamWriteUntilOutput: true,
		NowMs:                                 nowMs,
	}
}

// w22RunPipeWithCapture 在固定下游捕获字节与中断次数地跑完整管道。
func w22RunPipeWithCapture(t *testing.T, chunks [][]byte, options StreamPipeOptions, recorder *failureRecorder) (result StreamPipeResult, written []byte, interrupts int, err error) {
	t.Helper()
	_, downstream := newDownstream()
	var mu sync.Mutex
	interruptSource := StreamDownstream{
		Res: &capturingWriter{
			inner: downstream.Res,
			onWrite: func(data []byte) {
				mu.Lock()
				written = append(written, data...)
				mu.Unlock()
			},
		},
		Interrupt: func() {
			mu.Lock()
			interrupts++
			mu.Unlock()
		},
	}
	result, err = PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody:        NewSliceUpstreamBody(chunks...),
		Downstream:          interruptSource,
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         1000,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options:             options,
	})
	mu.Lock()
	defer mu.Unlock()
	return result, written, interrupts, err
}

// w22EventFramer 按事件边界切分下游可见字节流（复用拦截面共享分帧实现）。
func w22EventFramer(t *testing.T, stream []byte) [][]byte {
	t.Helper()
	pending := append([]byte(nil), stream...)
	var events [][]byte
	for {
		event := shiftSseEvent(&pending)
		if event == nil {
			break
		}
		events = append(events, event)
	}
	if len(pending) > 0 {
		t.Fatalf("下游字节流存在未完结事件尾: %q", string(pending))
	}
	return events
}

// ---- 验收 1：窗口内失败信号 → 既有提交前失败路径，客户端零字节 ----

func TestW22WindowFailureBeforeCommitKeepsClientAtZeroBytes(t *testing.T) {
	recorder := &failureRecorder{}
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),        // 首个语义事件：入窗缓冲，不写下游
		[]byte(w22TopLevelErrorFrame), // 窗口内失败帧
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(written) != 0 {
		t.Fatalf("窗口内失败必须客户端零字节，written = %q", string(written))
	}
	if interrupts != 0 {
		t.Fatalf("提交前失败不得中断连接，interrupts = %d", interrupts)
	}
	if result.Completed || result.SemanticCommitted {
		t.Fatalf("窗口缓冲未提交，结果不得按完成收尾: %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("失败记录数 = %d, want 1", recorder.count())
	}
	// generic 画像解释关闭（optionsInterpretDisabled）时失败帧按既有语义取
	// 不透明 code（upstream_protocol_failure），换号语义不受影响。
	if last := recorder.last(); last.errorCode != "upstream_protocol_failure" {
		t.Fatalf("errorCode = %q", last.errorCode)
	}
}

// ---- 验收 1b：ParseError 事件字节隔离，下游可见字节全为合法 SSE 事件 ----

func TestW22ParseErrorEventsIsolatedFromDownstreamStream(t *testing.T) {
	logger := &w22CaptureLogger{}
	options := w22WindowOptions(func() int64 { return 1000 })
	options.Logger = logger
	recorder := &failureRecorder{}
	result, written, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		[]byte(w22StickyBadPayload), // 解析失败事件：字节级隔离
		[]byte(chatFinishChunk),
		[]byte(chatDoneChunk),
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("隔离后流应正常完成: %+v", result)
	}
	if result.ParseErrorEventCount != 1 {
		t.Fatalf("ParseErrorEventCount = %d, want 1", result.ParseErrorEventCount)
	}
	if strings.Contains(string(written), "上级") || strings.Contains(string(written), "upstream sent an error event") {
		t.Fatalf("坏事件原文不得透传下游: %q", string(written))
	}
	// 客户端回放验证：下游可见字节全为合法 SSE 事件（逐事件 JSON 可解析），
	// 内容小缺口（坏事件被丢弃）可接受。
	for index, event := range w22EventFramer(t, written) {
		if gatewayopenai.ParseSseEventText(string(event)).DataParseError {
			t.Fatalf("下游事件 %d 无法 JSON 解析: %q", index, string(event))
		}
	}
	if !strings.Contains(string(written), "你好") || !strings.Contains(string(written), "[DONE]") {
		t.Fatalf("合法事件应保序透传: %q", string(written))
	}
	// 每请求首个 WARN 留痕（fields 含 endpoint/eventIndex/计数与字节摘要）。
	warn := logger.warnOf("gateway_stream_event_parse_error")
	if warn == nil {
		t.Fatalf("缺少 gateway_stream_event_parse_error WARN，events=%+v", logger.events())
	}
	if warn.fields["eventIndex"] == nil || warn.fields["parseErrorEventCount"] != 1 {
		t.Fatalf("WARN fields 缺事件序号/计数: %+v", warn.fields)
	}
	if snippet, _ := warn.fields["parseErrorSnippet"].(string); !strings.Contains(snippet, "上级") {
		t.Fatalf("WARN 应携带原始字节摘要: %+v", warn.fields)
	}
	if second := logger.warnOf("gateway_stream_event_parse_error"); second != warn {
		t.Fatalf("解析失败 WARN 每请求只发一条")
	}
}

// ---- 验收 2：窗口内收到终止事件 → 立即 flush，流正常完成 ----

func TestW22WindowTerminalFlushesImmediatelyAndCompletes(t *testing.T) {
	recorder := &failureRecorder{}
	result, written, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		[]byte(w22FinishChunk),
		[]byte(chatDoneChunk),
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("终止事件应正常完成: %+v", result)
	}
	if !result.ProtocolValidated {
		t.Fatalf("终止事件写入后应保持协议验证成功语义: %+v", result)
	}
	if !strings.Contains(string(written), "你好") || !strings.Contains(string(written), "[DONE]") {
		t.Fatalf("窗口缓冲与终止事件都应随 flush 到达客户端: %q", string(written))
	}
	if recorder.count() != 0 {
		t.Fatalf("失败记录数 = %d", recorder.count())
	}
}

// ---- 验收 3：窗口超 B（追加前判定，该 chunk 随 flush 写出）→ 提交，其后失败按提交后契约断开 ----

func TestW22WindowByteLimitCommitsChunkBeforeAppend(t *testing.T) {
	bigDelta := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + strings.Repeat("输", 3000) + "\"}}]}\n\n"
	recorder := &failureRecorder{}
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(bigDelta),              // 超 B：不入缓冲、随 flush 写出（提交）
		[]byte(w22TopLevelErrorFrame), // 提交后失败帧
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(string(written), "输") {
		t.Fatalf("超限 chunk 应随 flush 写出: len(written)=%d", len(written))
	}
	if !result.SemanticCommitted {
		t.Fatalf("超 B flush 应已语义提交: %+v", result)
	}
	if interrupts != 1 {
		t.Fatalf("提交后失败按既有契约应恰好中断一次，interrupts = %d", interrupts)
	}
	if result.Completed {
		t.Fatalf("提交后失败不得按完成收尾: %+v", result)
	}
}

// ---- 验收 4：W22 形态（粘连载荷 + 干净 EOF，generic）→ abnormal_eof 失败结算 ----

func TestW22AbnormalEOFBeforeCommitSettlesFailure(t *testing.T) {
	recorder := &failureRecorder{}
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		[]byte(w22StickyBadPayload), // 粘连载荷：隔离且计数
		// 干净 EOF（SliceUpstreamBody 耗尽即 EOF），无终止事件
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("异常终局不得收敛 success: %+v", result)
	}
	if result.ErrorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("errorCode = %q, want %q", result.ErrorCode, UpstreamStreamAbnormalEOFCode)
	}
	if result.ParseErrorEventCount != 1 {
		t.Fatalf("ParseErrorEventCount = %d, want 1", result.ParseErrorEventCount)
	}
	if len(written) != 0 || interrupts != 0 {
		t.Fatalf("提交前异常终局客户端零字节且不中断: written=%q interrupts=%d", string(written), interrupts)
	}
	last := recorder.last()
	if last.errorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("失败归因 errorCode = %q", last.errorCode)
	}
	// 探针零派发（§3.2.2：availabilityProbeEligible=false）。
	if last.context.AvailabilityProbeEligibleSet && last.context.AvailabilityProbeEligible {
		t.Fatalf("异常终局失败不得派发可用性探针: %+v", last.context)
	}
}

// N1（终审复查）回归：可解析失败帧缺终止空行即 EOF——Finish 冲出
// FailedReceived（隐含 Terminal），不得漏进 generic 收敛臂判 success，
// 应归 abnormal_eof 失败结算（提交前形态：客户端零字节、换号裁决）。
func TestW22AbnormalEOFUnfinishedTrailingFrameSettlesFailure(t *testing.T) {
	recorder := &failureRecorder{}
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		// 合法 JSON 失败帧，但无尾随空行即 EOF（inspector.Finish 冲出）
		[]byte("data: {\"error\":{\"code\":\"e\",\"type\":\"upstream_error\"}}"),
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("失败帧缺空行收尾不得收敛 success: %+v", result)
	}
	if result.ErrorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("errorCode = %q, want %q", result.ErrorCode, UpstreamStreamAbnormalEOFCode)
	}
	if len(written) != 0 || interrupts != 0 {
		t.Fatalf("提交前异常终局客户端零字节且不中断: written=%q interrupts=%d", string(written), interrupts)
	}
	if last := recorder.last(); last.errorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("失败归因 errorCode = %q", last.errorCode)
	}
}

func TestW22AbnormalEOFAfterCommitInterruptsWithCleanBytes(t *testing.T) {
	bigDelta := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + strings.Repeat("输", 3000) + "\"}}]}\n\n"
	recorder := &failureRecorder{}
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(bigDelta),            // 超 B 提交（窗口近似 no-op 的形态）
		[]byte(w22StickyBadPayload), // 粘连载荷：隔离不透传
		// 干净 EOF
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.ErrorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("异常终局应按 %q 失败结算: %+v", UpstreamStreamAbnormalEOFCode, result)
	}
	if interrupts != 1 {
		t.Fatalf("提交后异常终局应真·网络级断开，interrupts = %d", interrupts)
	}
	// 客户端已提交内容保留、坏字节不透传（隔离断言）。
	if !strings.Contains(string(written), "输") {
		t.Fatalf("已提交的语义输出应先于断开到达客户端")
	}
	if strings.Contains(string(written), "上级") || strings.Contains(string(written), "upstream sent an error event") {
		t.Fatalf("坏事件原文不得透传下游: %q", string(written))
	}
	for index, event := range w22EventFramer(t, written) {
		if gatewayopenai.ParseSseEventText(string(event)).DataParseError {
			t.Fatalf("下游事件 %d 无法 JSON 解析: %q", index, string(event))
		}
	}
	if last := recorder.last(); last.errorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("失败归因 errorCode = %q", last.errorCode)
	}
}

// ---- 验收 4b：窗口期静默行为（真实截止 timer）----

// w22LivePipe 构造可驱动 chunk 的短 T 管道并异步运行：written/interrupts 是
// 并发安全的下游观测闭包。StartedAtMs 与真实时钟对齐（读计划以墙钟算剩余）。
func w22LivePipe(t *testing.T, timeoutMs int64, options StreamPipeOptions) (body *chanBody, written func() string, interrupts func() int, outcomeCh <-chan w22RunOutcome) {
	t.Helper()
	body = newChanBody()
	recorder := &failureRecorder{}
	_, downstream := newDownstream()
	startedAtMs := time.Now().UnixMilli()
	var mu sync.Mutex
	var bytes []byte
	interruptCount := 0
	pipe := newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: body,
		Downstream: StreamDownstream{
			Res: &capturingWriter{inner: downstream.Res, onWrite: func(data []byte) {
				mu.Lock()
				bytes = append(bytes, data...)
				mu.Unlock()
			}},
			Interrupt: func() {
				mu.Lock()
				interruptCount++
				mu.Unlock()
			},
		},
		TimeoutProfile:      TimeoutProfile{FirstResponseTimeoutMs: 60_000, IdleTimeoutMs: 30_000, UncommittedAttemptMaxLifetimeMs: 300_000},
		StartedAtMs:         startedAtMs,
		HandleStreamFailure: recorder.handle,
		Signal:              staticSignal(),
		Options:             options,
	})
	// 真实截止 T 以包内字段覆写为短值（§3.1：窗口参数为包内常量，测试面覆写）。
	pipe.replayWindowTimeoutMs = timeoutMs
	outcomeChannel := make(chan w22RunOutcome, 1)
	go func() {
		result, err := pipe.run()
		outcomeChannel <- w22RunOutcome{result: result, err: err}
	}()
	return body, func() string {
			mu.Lock()
			defer mu.Unlock()
			return string(bytes)
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return interruptCount
		}, outcomeChannel
}

type w22RunOutcome struct {
	result StreamPipeResult
	err    error
}

func w22WaitFor(t *testing.T, deadline time.Duration, predicate func() bool) {
	t.Helper()
	waitDeadline := time.Now().Add(deadline)
	for time.Now().Before(waitDeadline) {
		if predicate() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func TestW22WindowDeadlineFlushesSilentStream(t *testing.T) {
	options := w22WindowOptions(func() int64 { return time.Now().UnixMilli() })
	body, written, _, outcomeCh := w22LivePipe(t, 60, options)
	body.push([]byte(chatDeltaChunk))
	// 静默超 T：timer 到点强制关窗 flush（真实截止，不依赖后续 chunk 驱动）。
	w22WaitFor(t, 2*time.Second, func() bool {
		select {
		case outcome := <-outcomeCh:
			t.Fatalf("管道提前结束: %+v %v", outcome.result, outcome.err)
		default:
		}
		return strings.Contains(written(), "你好")
	})
	body.push([]byte(chatDoneChunk))
	outcome := <-outcomeCh
	if outcome.err != nil {
		t.Fatalf("err = %v", outcome.err)
	}
	if !outcome.result.Completed {
		t.Fatalf("静默 flush 后流应正常完成: %+v", outcome.result)
	}
	if !outcome.result.SemanticCommitted {
		t.Fatalf("T 到点 flush 应已语义提交: %+v", outcome.result)
	}
	if outcome.result.WindowDurationMs == nil || *outcome.result.WindowDurationMs <= 0 {
		t.Fatalf("静默关窗应记录窗口时长: %+v", outcome.result.WindowDurationMs)
	}
}

func TestW22WindowSilenceThenReadErrorFailsAfterCommit(t *testing.T) {
	options := w22WindowOptions(func() int64 { return time.Now().UnixMilli() })
	body, written, interrupts, outcomeCh := w22LivePipe(t, 60, options)
	body.push([]byte(chatDeltaChunk))
	// 静默超 T flush（提交），随后读错误 → 既有 handlePipeError 失败路径。
	w22WaitFor(t, 2*time.Second, func() bool {
		select {
		case outcome := <-outcomeCh:
			t.Fatalf("管道提前结束: %+v %v", outcome.result, outcome.err)
		default:
		}
		return strings.Contains(written(), "你好")
	})
	body.fail(errors.New("上游连接被重置"))
	outcome := <-outcomeCh
	if outcome.result.Completed {
		t.Fatalf("静默后读错误应按失败收尾: %+v", outcome.result)
	}
	if !outcome.result.SemanticCommitted {
		t.Fatalf("T 到点 flush 应已语义提交: %+v", outcome.result)
	}
	if interrupts() != 1 {
		t.Fatalf("提交后读错误按既有契约中断一次，interrupts = %d", interrupts())
	}
}

// ---- 验收 5：终止事件 + 混入 ParseError → success + WARN（判定 1 优先于 3）----

func TestW22TerminalWithMixedParseErrorStaysSuccess(t *testing.T) {
	logger := &w22CaptureLogger{}
	options := w22WindowOptions(func() int64 { return 1000 })
	options.Logger = logger
	recorder := &failureRecorder{}
	result, _, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		[]byte(w22StickyBadPayload),
		[]byte(chatDoneChunk),
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("终止事件已到 = 上游自愈完成，应保持 success: %+v", result)
	}
	if result.ParseErrorEventCount != 1 {
		t.Fatalf("ParseErrorEventCount = %d, want 1", result.ParseErrorEventCount)
	}
	if logger.warnOf("gateway_stream_event_parse_error") == nil {
		t.Fatal("混入 ParseError 的成功流仍应留 WARN")
	}
	if recorder.count() != 0 {
		t.Fatalf("失败记录数 = %d", recorder.count())
	}
}

// ---- 验收 7/9：窗口资格排除面（桥接流 / 副作用请求）保持首个语义事件即提交 ----

func TestW22WindowDisabledCommitsFirstSemanticChunkImmediately(t *testing.T) {
	recorder := &failureRecorder{}
	options := w22WindowOptions(func() int64 { return 1000 })
	options.PreCommitReplayWindowDisabled = true
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		[]byte(w22TopLevelErrorFrame),
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(string(written), "你好") {
		t.Fatalf("排除面保持现状行为：首个语义事件即写下游: %q", string(written))
	}
	if !result.SemanticCommitted {
		t.Fatalf("排除面应立即语义提交: %+v", result)
	}
	if interrupts != 1 {
		t.Fatalf("提交后失败帧按既有契约中断一次，interrupts = %d", interrupts)
	}
}

// ---- 验收 8：精确画像 × 窗口（窗口内失败换号无感）----

func TestW22PreciseProfileWindowFailureStaysUncommitted(t *testing.T) {
	recorder := &failureRecorder{}
	// 精确画像（InterpretProtocolFailures 默认 true）+ 窗口启用：失败在窗口内
	// 仍走提交前失败路径（客户端零字节），窗口外才落提交后中断。
	options := StreamPipeOptions{
		RetryBeforeDownstreamWriteUntilOutput: true,
		NowMs:                                 func() int64 { return 1000 },
	}
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		// 干净 EOF 无终止：精确画像 missing-terminal，窗口内 → 提交前可救回
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(written) != 0 || interrupts != 0 {
		t.Fatalf("窗口内 missing-terminal 应提交前失败（零字节不中断）: written=%q interrupts=%d", string(written), interrupts)
	}
	if result.Completed || result.SemanticCommitted {
		t.Fatalf("窗口内 missing-terminal 不得收敛: %+v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("失败记录数 = %d", recorder.count())
	}
}

// ---- 验收 12：先 ParseError 后 Skipped 的流归因 abnormal_eof（判定 3 优先于 Skipped 分支）----

func TestW22ParseErrorThenParserSkippedAttributesAbnormalEOF(t *testing.T) {
	oversizedLine := "data: " + strings.Repeat("x", 300*1024) + "\n\n"
	recorder := &failureRecorder{}
	result, written, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		[]byte(w22StickyBadPayload), // 先 ParseError
		[]byte(oversizedLine),       // 后 Skipped（超大行触发解析跳过）
		// 干净 EOF
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.ErrorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("先 ParseError 后 Skipped 应归因 abnormal_eof，got %q (%+v)", result.ErrorCode, result)
	}
	if result.Completed {
		t.Fatalf("异常终局不得收敛 success: %+v", result)
	}
	if strings.Contains(string(written), "上级") {
		t.Fatalf("坏事件原文不得透传: %q", string(written))
	}
}

// ---- 验收「受控时间推进」：windowDurationMs / downstreamFirstWriteMs 可观测 ----
//
// 确定性口径：同一时钟刻内开窗并关窗（SliceUpstreamBody 顺序供片），时长为
// 0、首字写出时刻为 0——证明两字段的开窗/关窗接线与记录点；窗口时长的真实
// 间隔值由 TestW22WindowDeadlineFlushesSilentStream（真实时钟 + 真实截止）
// 以 WindowDurationMs > 0 锁定。

func TestW22WindowDurationObservableWithControlledClock(t *testing.T) {
	now := int64(1000)
	clock := func() int64 { return now }
	recorder := &failureRecorder{}
	result, _, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk), // 窗口开启（t=1000）
		[]byte(chatDoneChunk),  // 终止事件关窗 flush（同刻）
	}, w22WindowOptions(clock), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("结果应按完成收尾: %+v", result)
	}
	if result.WindowDurationMs == nil || *result.WindowDurationMs != 0 {
		t.Fatalf("windowDurationMs = %v, want 0（同刻开窗关窗）", result.WindowDurationMs)
	}
	if result.DownstreamFirstWriteMs == nil || *result.DownstreamFirstWriteMs != 0 {
		t.Fatalf("downstreamFirstWriteMs = %v, want 0（含窗口停留的客户端首字延迟口径）", result.DownstreamFirstWriteMs)
	}
	if result.FirstTokenMs == nil {
		t.Fatal("firstTokenMs（上游侧口径，不含窗口停留）应照常记录")
	}
}

// ---- EOF 早退面的隔离器尾字节回归：主循环持有的未完结事件尾字节不得随
// EOF 空列表早退丢失（既有截断行为保持）----

func TestW22IsolatorTailSurvivesEOFWithoutFurtherChunks(t *testing.T) {
	recorder := &failureRecorder{}
	result, written, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),
		// 合法 JSON 但无终止空行即 EOF：隔离器持有尾字节，EOF 段无后继分片；
		// 尾字节随既有 generic 收敛 flush 到达客户端（非法 JSON 尾字节形态由
		// TestW22AbnormalEOFUnfinishedTrailingFrameSettlesFailure 覆盖）。
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial-tail\"}}]}"),
	}, w22WindowOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("generic EOF 收敛应照常完成: %+v", result)
	}
	if result.ParseErrorEventCount != 0 {
		t.Fatalf("合法 JSON 未完结事件不得计解析失败: %d", result.ParseErrorEventCount)
	}
	if !strings.Contains(string(written), "partial-tail") {
		t.Fatalf("未完结尾字节应随既有收敛路径到达客户端: %q", string(written))
	}
}

// ---- 隔离器单元语义 ----

func TestW22ParseErrorIsolatorUnitSemantics(t *testing.T) {
	iso := &parseErrorEventIsolator{}
	// 完整坏事件：字节不外泄。
	out := iso.push([]byte(w22StickyBadPayload))
	if out != nil && strings.Contains(string(out), "上级") {
		t.Fatalf("坏事件字节应被隔离: %q", string(out))
	}
	// 完整好事件：原样透传。
	out = iso.push([]byte(chatDeltaChunk))
	if !strings.Contains(string(out), "你好") {
		t.Fatalf("好事件应透传: %q", string(out))
	}
	// 跨 chunk 半截坏事件：前缀持有到完结，坏字节全程不外泄。
	out = iso.push([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"残\"}]} dat"))
	if strings.Contains(string(out), "残") {
		t.Fatalf("未完结坏事件前缀应被持有: %q", string(out))
	}
	out = iso.push([]byte("a: {\"error\":{}}\n\n"))
	if strings.Contains(string(out), "残") || strings.Contains(string(out), "error") {
		t.Fatalf("坏事件完整字节应被隔离: %q", string(out))
	}
	if iso.dropped != 2 {
		t.Fatalf("dropped = %d, want 2", iso.dropped)
	}
	snippet, index := iso.firstDropped()
	if !strings.Contains(snippet, "上级") || index != 1 {
		t.Fatalf("首个坏事件摘要/序号 = %q/%d", snippet, index)
	}
	// EOF 未完结尾字节放行（不新造截断语义）。
	iso.push([]byte("data: {\"partial\""))
	if tail := iso.flushPending(); !strings.Contains(string(tail), "partial") {
		t.Fatalf("EOF 未完结尾应放行: %q", string(tail))
	}
}

// ---- 窗口资格单元语义（precommit.go CanKeepStreamPreCommitChunk）----

func TestW22WindowCanKeepChunkSemantics(t *testing.T) {
	state := &PreCommitBufferState{Buffering: true}
	state.Windowing = true
	state.WindowStartMs = 1000
	// 窗口内放行语义 chunk。
	if !CanKeepStreamPreCommitChunk(state, PreCommitInspectionState{OutputReceived: true}, []byte("chunk"), 0, PreCommitResponseState{}) {
		t.Fatal("窗口内应放行语义 chunk")
	}
	// 终态语义事件任何阶段不入缓冲（不变量 1）。
	if CanKeepStreamPreCommitChunk(state, PreCommitInspectionState{OutputReceived: true, TerminalReceived: true}, []byte("done"), 0, PreCommitResponseState{}) {
		t.Fatal("终态事件不得入窗")
	}
	// 超过窗口上限 B：该 chunk 不入缓冲（追加前判定）。
	state.WindowBytes = StreamReplayWindowMaxBytes
	if CanKeepStreamPreCommitChunk(state, PreCommitInspectionState{OutputReceived: true}, []byte("more"), 0, PreCommitResponseState{}) {
		t.Fatal("达 B 后 chunk 不得入缓冲")
	}
	// 窗口外保持既有语义：首个语义事件后不再缓冲。
	state.Windowing = false
	state.WindowBytes = 0
	if CanKeepStreamPreCommitChunk(state, PreCommitInspectionState{OutputReceived: true}, []byte("chunk"), 0, PreCommitResponseState{}) {
		t.Fatal("窗口外语义 chunk 不得缓冲（既有立即提交语义）")
	}
	if !CanKeepStreamPreCommitChunk(state, PreCommitInspectionState{}, []byte("data: x"), 0, PreCommitResponseState{}) {
		t.Fatal("窗口外非语义 chunk 仍可缓冲")
	}
}

// ---- 验收 6/7 回归锚点在既有用例（TestW13CFinalizeAfterLoopGenericEOF /
// TestWdCommittedFailureAlwaysInterrupts / passthrough 与桥接链级用例），本文件
// 不重复。----

// ---- finalize 层：提交前异常终局/失败帧的换号 verdict 生产（验收 1 / 4 /
// 10 / 11 的单 attempt 面；候选循环与 503 渲染由 chain 面既有逻辑消费）----

func TestW22AbnormalEOFPreCommitProducesServerRetryVerdict(t *testing.T) {
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(w22StickyBadPayload)), 200, nil)
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "generic"}
	usage := &mockUsageRecords{}
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: effects, NowMs: func() int64 { return 1000 }}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.RetryUpstream {
		t.Fatalf("提交前异常终局应换号重放: %+v", result)
	}
	if result.RetryReason != StreamServerRetryPreCommitStreamFailure || !result.ExcludeCurrentAccount {
		t.Fatalf("换号 verdict = %+v", result)
	}
	if result.ErrorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("verdict errorCode = %q, want %q（归因三面判别键）", result.ErrorCode, UpstreamStreamAbnormalEOFCode)
	}
	if recorder.Body.String() != "" {
		t.Fatalf("客户端零业务字节，body = %q", recorder.Body.String())
	}
	// usage 以失败口径入账（Success=false + error_code）。
	if len(usage.completed) != 1 {
		t.Fatalf("usage records = %d", len(usage.completed))
	}
	record := usage.completed[0]
	if record.Success || record.ErrorCode != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("usage record = %+v", record)
	}
	// 探针零派发。
	if len(effects.healthChecks) != 0 {
		t.Fatalf("异常终局不得派发可用性探针: %+v", effects.healthChecks)
	}
}

func TestW22WindowFailurePreCommitProducesServerRetryVerdict(t *testing.T) {
	input, recorder := newInputFixture(NewSliceUpstreamBody([]byte(chatDeltaChunk), []byte(w22TopLevelErrorFrame)), 200, nil)
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "generic"}
	usage := &mockUsageRecords{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: &mockAccountEffects{}, NowMs: func() int64 { return 1000 }}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.RetryUpstream || !result.ExcludeCurrentAccount {
		t.Fatalf("窗口内失败帧应换号重放: %+v", result)
	}
	if recorder.Body.String() != "" {
		t.Fatalf("客户端零业务字节，body = %q", recorder.Body.String())
	}
	if len(usage.completed) != 1 || usage.completed[0].Success {
		t.Fatalf("usage records = %+v", usage.completed)
	}
}

// ---- 测试辅助：日志捕获 ----

type w22LogEvent struct {
	event  string
	fields map[string]any
}

type w22CaptureLogger struct {
	mu     sync.Mutex
	logged []w22LogEvent
}

func (l *w22CaptureLogger) Debug(event string, fields map[string]any, _ string) {
	l.record(event, fields)
}

func (l *w22CaptureLogger) Info(event string, fields map[string]any, _ string) {
	l.record(event, fields)
}

func (l *w22CaptureLogger) Warn(event string, fields map[string]any, _ string) {
	l.record(event, fields)
}

func (l *w22CaptureLogger) record(event string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logged = append(l.logged, w22LogEvent{event: event, fields: fields})
}

func (l *w22CaptureLogger) events() []w22LogEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]w22LogEvent(nil), l.logged...)
}

func (l *w22CaptureLogger) warnOf(event string) *w22LogEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	for index := range l.logged {
		if l.logged[index].event == event {
			return &l.logged[index]
		}
	}
	return nil
}
