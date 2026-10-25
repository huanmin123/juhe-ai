package gatewayresponse

import (
	"strings"
	"testing"
)

// 流式提交边界受限重放与异常终局处置设计 v2.3 §3.2.3（L1 空完成终局判定）与
// §3.2.0（L2 帧级协议形状护栏）的管道级确定性用例（w32_ 前缀，夹具为 2026-10-26
// 生产实证空壳流三帧原文）。链级归因三面在 cmd 包测试覆盖（chain_v1_w32_ 前缀）。

// 生产实证三帧原文（xiaoyi.loc.cc / napi.kid1412.qzz.io，deepseek-v4.1-flash
// 空壳成功流，审计 blob 字节级取证）：全部合法 JSON、无 error 对象。
const w32EmptyShellFrame1 = "data: {\"id\":\"\",\"object\":\"\",\"created\":0,\"model\":\"\",\"system_fingerprint\":null,\"choices\":null,\"usage\":null}\n\n"

const w32EmptyShellFrame2 = "data: {\"id\":\"\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"\",\"system_fingerprint\":\"\",\"choices\":[],\"usage\":{\"prompt_tokens\":168586,\"completion_tokens\":0,\"total_tokens\":168586}}\n\n"

const w32EmptyShellFrame3 = "data: [DONE]\n\n"

// w32PreciseOptions 返回精确画像选项（InterpretProtocolFailures 缺省 true）。
func w32PreciseOptions(nowMs func() int64) StreamPipeOptions {
	return StreamPipeOptions{
		RetryBeforeDownstreamWriteUntilOutput: true,
		NowMs:                                 nowMs,
	}
}

// ---- 验收 14：生产三帧空壳流 → 提交前失败、缓冲丢弃、下游零字节、新码 ----

func TestW32EmptyCompletionBeforeDownstreamCommitSettlesFailure(t *testing.T) {
	recorder := &failureRecorder{}
	logger := &w22CaptureLogger{}
	options := w32PreciseOptions(func() int64 { return 1000 })
	options.Logger = logger
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(w32EmptyShellFrame1), // choices:null：L2 隔离 + 计数
		[]byte(w32EmptyShellFrame2), // usage-only（choices 空数组）：不算输出
		[]byte(w32EmptyShellFrame3), // [DONE]：终态 → L1 空完成
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed {
		t.Fatalf("空完成不得按 success 收尾: %+v", result)
	}
	if result.ErrorCode != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("errorCode = %q, want %q", result.ErrorCode, UpstreamStreamEmptyCompletionCode)
	}
	if len(written) != 0 || interrupts != 0 {
		t.Fatalf("提交前空完成客户端零字节且不中断: written=%q interrupts=%d", string(written), interrupts)
	}
	if len(result.UncommittedResponseBody) != 0 {
		t.Fatalf("预提交缓冲应被丢弃, body = %q", string(result.UncommittedResponseBody))
	}
	if result.TransportCommitted || result.SemanticCommitted {
		t.Fatalf("提交前失败不得置提交位: %+v", result)
	}
	if result.OutputReceived {
		t.Fatalf("空完成流不应有输出: %+v", result)
	}
	// L2：首帧 choices:null 被隔离并计数（解析成功，不进 ParseError 计数）。
	if result.InvalidProtocolEventCount != 1 || result.ParseErrorEventCount != 0 {
		t.Fatalf("L2 计数 = invalid:%d parse:%d, want 1/0", result.InvalidProtocolEventCount, result.ParseErrorEventCount)
	}
	if logger.warnOf("gateway_stream_event_invalid_protocol") == nil {
		t.Fatal("协议形状异常帧应留 WARN")
	}
	if recorder.count() != 1 {
		t.Fatalf("failure records = %d", recorder.count())
	}
	last := recorder.last()
	if last.errorCode != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("失败归因 errorCode = %q", last.errorCode)
	}
	if last.context.OutputReceived {
		t.Fatalf("失败上下文不应有输出: %+v", last.context)
	}
	// 探针零派发（§3.2.3：availabilityProbeEligible=false，同 abnormal_eof）。
	if last.context.AvailabilityProbeEligibleSet && last.context.AvailabilityProbeEligible {
		t.Fatalf("空完成失败不得派发可用性探针: %+v", last.context)
	}
	// 失败结算日志事件与 fields（成功/失败结算观测面对齐）。
	var failureLogged bool
	for _, event := range logger.events() {
		if event.event == "gateway_stream_empty_completion_before_downstream_commit" {
			failureLogged = true
			if event.fields["invalidProtocolEventCount"] != 1 || event.fields["parseErrorEventCount"] != 0 {
				t.Fatalf("失败结算 fields 缺计数: %+v", event.fields)
			}
		}
	}
	if !failureLogged {
		t.Fatalf("缺少 gateway_stream_empty_completion_before_downstream_commit 事件, events=%+v", logger.events())
	}
}

// ---- 验收 14：同夹具 generic 与精确两画像同判 ----

func TestW32EmptyCompletionGenericAndPreciseProfilesBothFail(t *testing.T) {
	for _, profile := range []struct {
		name    string
		options StreamPipeOptions
	}{
		{name: "generic", options: w22WindowOptions(func() int64 { return 1000 })},
		{name: "precise", options: w32PreciseOptions(func() int64 { return 1000 })},
	} {
		t.Run(profile.name, func(t *testing.T) {
			recorder := &failureRecorder{}
			result, written, _, err := w22RunPipeWithCapture(t, [][]byte{
				[]byte(w32EmptyShellFrame1),
				[]byte(w32EmptyShellFrame2),
				[]byte(w32EmptyShellFrame3),
			}, profile.options, recorder)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if result.Completed || result.ErrorCode != UpstreamStreamEmptyCompletionCode {
				t.Fatalf("%s 画像应按 %q 失败结算: %+v", profile.name, UpstreamStreamEmptyCompletionCode, result)
			}
			if len(written) != 0 {
				t.Fatalf("%s 画像提交前应零字节: %q", profile.name, string(written))
			}
			if recorder.count() != 1 || recorder.last().errorCode != UpstreamStreamEmptyCompletionCode {
				t.Fatalf("%s 画像失败记录 = %+v", profile.name, recorder.values)
			}
		})
	}
}

// ---- 验收 15：同夹具去掉 [DONE] 的干净 EOF（generic）维持 success（判定 4 未动）----

func TestW32EmptyStreamCleanEOFWithoutTerminalStaysSuccess(t *testing.T) {
	recorder := &failureRecorder{}
	logger := &w22CaptureLogger{}
	options := w22WindowOptions(func() int64 { return 1000 })
	options.Logger = logger
	result, _, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(w32EmptyShellFrame1),
		[]byte(w32EmptyShellFrame2),
		// 干净 EOF，无 [DONE]
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed || result.Message != "已完成" {
		t.Fatalf("判定 4：无终止事件干净 EOF 零输出应维持 success: %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("success 收敛不得记失败: %+v", recorder.values)
	}
	// L2 照常隔离 + 计数（纯观测面，不参与终局判定）。
	if result.InvalidProtocolEventCount != 1 {
		t.Fatalf("InvalidProtocolEventCount = %d, want 1", result.InvalidProtocolEventCount)
	}
	// 成功结算 fields 携带 invalidProtocolEventCount（generic 收敛臂 Info）。
	var successLogged bool
	for _, event := range logger.events() {
		if event.event == "gateway_stream_finished_success" {
			successLogged = true
			if event.fields["invalidProtocolEventCount"] != 1 || event.fields["parseErrorEventCount"] != 0 {
				t.Fatalf("成功结算 fields 缺计数: %+v", event.fields)
			}
		}
	}
	if !successLogged {
		t.Fatalf("缺少 gateway_stream_finished_success 结算事件, events=%+v", logger.events())
	}
}

// ---- 验收 16：内容流中间夹 choices:null 帧 → 隔离、内容完整透传、流 success ----

const w32InvalidChoicesNullFrame = "data: {\"id\":\"\",\"object\":\"\",\"created\":0,\"model\":\"\",\"choices\":null}\n\n"

func TestW32InvalidProtocolFrameIsolatedInsideContentStream(t *testing.T) {
	recorder := &failureRecorder{}
	logger := &w22CaptureLogger{}
	options := w22WindowOptions(func() int64 { return 1000 })
	options.Logger = logger
	result, written, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(chatDeltaChunk),             // 真实内容
		[]byte(w32InvalidChoicesNullFrame), // 协议形状异常帧：隔离
		[]byte(chatFinishChunk),
		[]byte(chatDoneChunk),
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("内容流应正常完成: %+v", result)
	}
	if result.InvalidProtocolEventCount != 1 {
		t.Fatalf("InvalidProtocolEventCount = %d, want 1", result.InvalidProtocolEventCount)
	}
	if recorder.count() != 0 {
		t.Fatalf("L2 不判失败，失败记录数 = %d", recorder.count())
	}
	// 内容完整保序，异常帧原文不透传。
	if !strings.Contains(string(written), "你好") || !strings.Contains(string(written), "[DONE]") {
		t.Fatalf("合法事件应保序透传: %q", string(written))
	}
	if strings.Contains(string(written), `"choices":null`) {
		t.Fatalf("协议形状异常帧不得透传下游: %q", string(written))
	}
	// 每请求首个 WARN 留痕（fields 含计数与字节摘要）。
	warn := logger.warnOf("gateway_stream_event_invalid_protocol")
	if warn == nil {
		t.Fatalf("缺少 gateway_stream_event_invalid_protocol WARN，events=%+v", logger.events())
	}
	if warn.fields["invalidProtocolEventCount"] != 1 {
		t.Fatalf("WARN fields 缺计数: %+v", warn.fields)
	}
	if snippet, _ := warn.fields["invalidProtocolSnippet"].(string); !strings.Contains(snippet, "choices") {
		t.Fatalf("WARN 应携带原始字节摘要: %+v", warn.fields)
	}
}

// ---- 验收 17：tool-call-only 流（finish_reason=tool_calls + [DONE]）不误伤 ----

const w32ToolCallChunk = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\":\\\"hangzhou\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"

func TestW32ToolCallOnlyStreamStaysSuccess(t *testing.T) {
	recorder := &failureRecorder{}
	result, _, _, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(w32ToolCallChunk),
		[]byte(chatDoneChunk),
	}, w32PreciseOptions(func() int64 { return 1000 }), recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.Completed {
		t.Fatalf("tool-call-only 流不构成空完成，应 success: %+v", result)
	}
	if !result.OutputReceived {
		t.Fatalf("tool_calls 应按既有计数语义置位输出: %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("失败记录数 = %d", recorder.count())
	}
}

// ---- 验收 17：usage-only 空壳帧序列超预提交缓冲 → 提交后兜底断开 ----

func TestW32EmptyCompletionAfterCommitFallbackInterrupts(t *testing.T) {
	// 单帧约 96KB（padding 撑大 usage 载荷），三帧累计越过 256KB 预提交缓冲：
	// 第三帧追加前触发 flush 提交前两帧并直写自身，终态 [DONE] 到达时已越过
	// 提交边界 → 走提交后兜底分支（真·网络级断开）。
	bigUsageOnlyFrame := "data: {\"id\":\"\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"\",\"system_fingerprint\":\"\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0,\"total_tokens\":1,\"padding\":\"" + strings.Repeat("x", 96*1024) + "\"}}\n\n"
	recorder := &failureRecorder{}
	logger := &w22CaptureLogger{}
	options := w32PreciseOptions(func() int64 { return 1000 })
	options.Logger = logger
	result, written, interrupts, err := w22RunPipeWithCapture(t, [][]byte{
		[]byte(bigUsageOnlyFrame),
		[]byte(bigUsageOnlyFrame),
		[]byte(bigUsageOnlyFrame),
		[]byte(w32EmptyShellFrame3),
	}, options, recorder)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if result.Completed || result.ErrorCode != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("提交后空完成应按 %q 失败结算: %+v", UpstreamStreamEmptyCompletionCode, result)
	}
	if interrupts != 1 {
		t.Fatalf("提交后空完成应真·网络级断开一次，interrupts = %d", interrupts)
	}
	if len(written) == 0 {
		t.Fatal("已越过提交边界的 usage 帧应已到达下游")
	}
	if recorder.count() != 1 || recorder.last().errorCode != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("失败记录 = %+v", recorder.values)
	}
	var interruptedLogged bool
	for _, event := range logger.events() {
		if event.event == "gateway_stream_empty_completion_interrupted" {
			interruptedLogged = true
		}
	}
	if !interruptedLogged {
		t.Fatalf("缺少 gateway_stream_empty_completion_interrupted 事件, events=%+v", logger.events())
	}
}

// ---- 结算观测面与 verdict 面（finalize 层，镜像 TestW22AbnormalEOFPreCommit-
// ProducesServerRetryVerdict 形态）：换号 verdict 带 ExcludeCurrentAccount、
// ErrorCode 原样（透传豁免生效）、usage 失败行、探针零派发 ----

func TestW32EmptyCompletionPreCommitProducesServerRetryVerdict(t *testing.T) {
	input, recorder := newInputFixture(NewSliceUpstreamBody(
		[]byte(w32EmptyShellFrame1), []byte(w32EmptyShellFrame2), []byte(w32EmptyShellFrame3),
	), 200, nil)
	input.ClientStrategy = &ClientStrategyView{ClientProfile: "generic"}
	usage := &mockUsageRecords{}
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: effects, NowMs: func() int64 { return 1000 }}

	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !result.RetryUpstream {
		t.Fatalf("提交前空完成应换号重放: %+v", result)
	}
	if result.RetryReason != StreamServerRetryEmptyCompletion || !result.ExcludeCurrentAccount {
		t.Fatalf("换号 verdict = %+v", result)
	}
	if result.ErrorCode != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("verdict errorCode = %q, want %q（透传豁免生效）", result.ErrorCode, UpstreamStreamEmptyCompletionCode)
	}
	if recorder.Body.String() != "" {
		t.Fatalf("客户端零业务字节，body = %q", recorder.Body.String())
	}
	if len(usage.completed) != 1 {
		t.Fatalf("usage records = %d", len(usage.completed))
	}
	record := usage.completed[0]
	if record.Success || record.ErrorCode != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("usage record = %+v", record)
	}
	if len(effects.healthChecks) != 0 {
		t.Fatalf("空完成失败不得派发可用性探针: %+v", effects.healthChecks)
	}
}

// PreCommitStreamServerRetryErrorCode 透传豁免单元：新码不受客户端 strategy
// preCommitFailureSignal=protocol_error_event 改写臂影响（同 abnormal_eof 样板）。
func TestW32PreCommitStreamServerRetryErrorCodePassthrough(t *testing.T) {
	result := StreamPipeResult{ErrorCode: UpstreamStreamEmptyCompletionCode}
	if got := PreCommitStreamServerRetryErrorCode(result, true); got != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("protocol_error_event 臂下新码被改写: %q", got)
	}
	if got := PreCommitStreamServerRetryErrorCode(result, false); got != UpstreamStreamEmptyCompletionCode {
		t.Fatalf("默认臂下新码被改写: %q", got)
	}
	// abnormal_eof 豁免保持；普通失败码仍按既有语义改写。
	abnormal := StreamPipeResult{ErrorCode: UpstreamStreamAbnormalEOFCode}
	if got := PreCommitStreamServerRetryErrorCode(abnormal, true); got != UpstreamStreamAbnormalEOFCode {
		t.Fatalf("abnormal_eof 豁免回归破坏: %q", got)
	}
	ordinary := StreamPipeResult{Message: "上游流式响应已中断", ErrorCode: "stream_interrupted"}
	if got := PreCommitStreamServerRetryErrorCode(ordinary, true); got == ordinary.ErrorCode {
		t.Fatalf("普通失败码应按既有改写语义走 protocol_error_event 臂: %q", got)
	}
}

// ---- L1 作用域限定 openai 驱动域（设计 §3.2.3 豁免面，实施验证修正）：
// anthropic / gemini 域存在合法零输出终态面（gemini interactions create 的
// interaction.completed 经 firstGeminiFinishReason 投影为终态而天然零输出），
// L1 不启用，维持各自现状语义。 ----

func TestW32EmptyCompletionNotAppliedOutsideOpenAIDriverDomain(t *testing.T) {
	for _, driver := range []struct {
		name   string
		driver StreamDriver
		stream string
	}{
		{
			name:   "anthropic",
			driver: anthropicStreamDriver{},
			// message_stop 是 anthropic 协议终态（gatewayanthropic/streaminspection.go）；
			// 流内无任何 content delta——零输出终态流维持 success 现状。
			stream: "event: message_start\ndata: {\"type\":\"message_start\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
		{
			name:   "gemini",
			driver: geminiStreamDriver{},
			// interaction.completed 经 status 投影为终态、零输出（interactions
			// create 合法形态）——维持 success 现状，不判空完成。
			stream: "event: interaction.created\ndata: {\"type\":\"interaction.created\",\"interaction\":{\"id\":\"ix_w32\"}}\n\nevent: interaction.completed\ndata: {\"type\":\"interaction.completed\",\"interaction\":{\"id\":\"ix_w32\",\"status\":\"completed\"}}\n\n",
		},
	} {
		t.Run(driver.name, func(t *testing.T) {
			recorder := &failureRecorder{}
			options := StreamPipeOptions{
				Driver:                                driver.driver,
				RetryBeforeDownstreamWriteUntilOutput: true,
				NowMs:                                 func() int64 { return 1000 },
			}
			result, err := runPipe(NewSliceUpstreamBody([]byte(driver.stream)), nil, options, recorder, 1000)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !result.Completed {
				t.Fatalf("%s 驱动域零输出终态流应维持现状 success（L1 不启用）: %+v", driver.name, result)
			}
			if recorder.count() != 0 || result.ErrorCode == UpstreamStreamEmptyCompletionCode {
				t.Fatalf("%s 驱动域不得按空完成失败结算: records=%+v errorCode=%q", driver.name, recorder.values, result.ErrorCode)
			}
		})
	}
}
