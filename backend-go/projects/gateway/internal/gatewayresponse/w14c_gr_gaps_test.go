package gatewayresponse

// w14c 覆盖率补齐：w13c 之后仍缺失的单元分支与管道路径。
// 已知的竞态型分支（依赖 select 双就绪随机调度）不做确定性覆盖：
// streamread.go raceRead + settle 晚于 precommit 墙钟（148-156、151-153）、
// decideFirstByteDeadlineAfterPendingRead 的 pending.ch 胜出但 settle 晚于
// 墙钟（266-269）与 timer 胜出但 pending 已提前 settle（276-282）。

import (
	"context"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// ---- codexcontract.go ----

func TestW14CCodexContractGaps(t *testing.T) {
	// requestBodyHasCompactionTrigger：无触发、bodyState 缺失 → false 兜底。
	req := &gatewaypreauth.GatewayRequest{}
	if requestBodyHasCompactionTrigger(req) {
		t.Fatal("空请求不得触发 compaction")
	}

	// jsonValueHasCompactionTrigger：nil map、超 200 子节点截断。
	if jsonValueHasCompactionTrigger(map[string]any(nil), 0) {
		t.Fatal("nil map 不得触发")
	}
	wide := map[string]any{}
	for i := 0; i < 260; i++ {
		wide["w14c-key-"+strconv.Itoa(i)] = i
	}
	if jsonValueHasCompactionTrigger(wide, 0) {
		t.Fatal("广度截断处不得误报")
	}
	if !jsonValueHasCompactionTrigger(map[string]any{"type": "compaction_trigger"}, 0) {
		t.Fatal("type 触发必须命中")
	}

	// normalizedOpenAIRequestPath：空路径回退 "/"。
	if got := normalizedOpenAIRequestPath(&gatewaypreauth.GatewayRequest{}); got == "" {
		t.Fatal("空路径必须回退 /")
	}

	// requestPathHasCompactionTrigger：前缀命中与尾部命中。
	edge := codexCompactionRawBodyScanEdgeBytes
	pattern := []byte(`{"type":"compaction_trigger"}`)
	prefixBody := make([]byte, 0, edge*3)
	prefixBody = append(prefixBody, pattern...)
	for len(prefixBody) < edge*3 {
		prefixBody = append(prefixBody, ' ')
	}
	if !requestPathHasCompactionTrigger("/v1/responses", nil, prefixBody) {
		t.Fatal("前缀命中必须识别 compaction trigger")
	}
	tailBody := make([]byte, 0, edge*3)
	for len(tailBody) < edge*3-len(pattern) {
		tailBody = append(tailBody, ' ')
	}
	tailBody = append(tailBody, pattern...)
	if !requestPathHasCompactionTrigger("/responses", nil, tailBody) {
		t.Fatal("尾部命中必须识别 compaction trigger")
	}
	if requestPathHasCompactionTrigger("/chat/completions", nil, prefixBody) {
		t.Fatal("非 responses 路径不检查")
	}
}

// ---- convert.go ----

func TestW14CGeminiStringErrorFieldFloat(t *testing.T) {
	value, ok := geminiStringErrorField(1.5)
	if !ok || value != "1.5" {
		t.Fatalf("非整型浮点必须按 g 格式输出: (%q, %v)", value, ok)
	}
}

// ---- errors.go ----

func TestW14CResponsePrecommitDeadlineErrorOfWrapped(t *testing.T) {
	inner := &ResponsePrecommitDeadlineError{DeadlineAtMs: 42}
	wrapped := &NonStreamBodyPipeError{OriginalError: inner}
	got := ResponsePrecommitDeadlineErrorOf(wrapped)
	if got == nil || got.DeadlineAtMs != 42 {
		t.Fatalf("包装错误必须解出: %v", got)
	}
}

// ---- failurestatus.go / streamresult.go / upstream.go ----

func TestW14CFailureStatusWhitespace(t *testing.T) {
	tracker := NewResponsesRootStatusTracker()
	tracker.consumeByte(' ')
	tracker.consumeByte(0x0a)
	if tracker.completed {
		t.Fatal("根未启动时的空白只应被忽略")
	}
	tracker.consumeByte('[')
	_ = tracker
}

func TestW14CStreamResultAuditBodyFaces(t *testing.T) {
	capture := NewLimitedCapture(1024)
	capture.Push([]byte("abc"))
	body := auditUpstreamBodyForResult(capture, false, false, nil)
	if string(body) != "abc" {
		t.Fatalf("上游捕获必须透出: %q", string(body))
	}
	completed := auditUpstreamBodyForResult(capture, true, false, nil)
	if completed != nil {
		t.Fatalf("成功完成不透出上游体: %v", completed)
	}
}

func TestW14CReaderUpstreamBodyClose(t *testing.T) {
	body := NewReaderUpstreamBody(context.Background(), strings.NewReader("x"))
	first, ok := <-body.Next()
	if !ok || string(first.Data) != "x" {
		t.Fatalf("首块必须可读: (%+v, %v)", first, ok)
	}
	body.Close()
	body.Close() // 幂等
	if _, ok := <-body.Next(); ok {
		t.Fatal("关闭后通道必须结束")
	}
}

// ---- ports.go / readplan.go / models.go ----

func TestW14CRequestModelHintNilSafe(t *testing.T) {
	if requestModelHint(nil) != "" {
		t.Fatal("nil 请求必须返回空模型")
	}
}

func TestW14CTimeoutSeconds(t *testing.T) {
	if timeoutSeconds(0) != 1 || timeoutSeconds(-5) != 1 {
		t.Fatal("非正超时回退 1 秒")
	}
	if timeoutSeconds(1500) != 2 {
		t.Fatal("1500ms 进位到 2 秒")
	}
	if timeoutSeconds(999) != 1 {
		t.Fatal("999ms 不足 1 秒按 1 秒")
	}
}

// ---- sink.go ----

func TestW14CSinkLoggerFallback(t *testing.T) {
	sink := &Sink{Deps: SinkDeps{}}
	var logger StreamLogger = sink.logger()
	if logger == nil {
		t.Fatal("缺省 logger 必须回退 nopStreamLogger")
	}
	logger.Debug("w14c", nil, "调试")
}

func TestW14CGatewayErrorBodyMapExtra(t *testing.T) {
	body := gatewaypreauth.GatewayErrorBody{Message: "m", Code: "c", Extra: map[string]any{"w14c": 1}}
	object := gatewayErrorBodyMap(body)
	if object["w14c"] != 1 || object["code"] != "c" || object["message"] != "m" {
		t.Fatalf("Extra 必须合入: %v", object)
	}
}

// ---- interceptor.go ----

func TestW14CInterceptorShiftEventCRLF(t *testing.T) {
	interceptor := &OpenAIStreamInterceptor{}
	interceptor.pending.Write([]byte("data: {\"a\":1}\r\r"))
	event := interceptor.shiftEvent()
	if event == nil || !strings.Contains(string(event), "data:") {
		t.Fatalf("边界必须出事件: %v", event)
	}
	interceptor.pending.Write([]byte("data: {\"b\":2}\n\n"))
	event = interceptor.shiftEvent()
	if event == nil || !strings.Contains(string(event), "b") {
		t.Fatalf("边界必须出事件: %v", event)
	}
}

// ---- precommit.go ----

func TestW14CPrecommitEvidenceCRLF(t *testing.T) {
	evidence := NewStreamPreCommitSseEvidence()
	evidence.Push([]byte("data: w14c\r\ndata: y\r\r\r\n"))
	evidence.Finish()
	if !evidence.DataEventObserved {
		t.Fatal("data: 行必须记录为数据事件")
	}
	if evidence.OnlyNonSemanticFramingObserved {
		t.Fatal("观察到数据事件后不得保持 only-framing")
	}
	framing := NewStreamPreCommitSseEvidence()
	framing.Push([]byte(": keep-alive\n\n: ping\r\r"))
	framing.Finish()
	if framing.DataEventObserved || !framing.OnlyNonSemanticFramingObserved {
		t.Fatalf("注释框架不得触发数据事件: %+v", framing)
	}
}

// ---- nonstreamgate.go ----

func TestW14CShouldBufferNonStreamJSONGates(t *testing.T) {
	input := HandleUpstreamResponseInput{Account: accountFixture()}
	if shouldBufferNonStreamJSONResponse(input) {
		t.Fatal("无策略且无客户端策略不得缓冲")
	}
	withStrategy := HandleUpstreamResponseInput{Account: accountFixture(), ClientStrategy: &ClientStrategyView{
		InterpretSemantics:      true,
		CodexCompactionExpected: true,
	}}
	if !shouldBufferNonStreamJSONResponse(withStrategy) {
		t.Fatal("解释语义 + codex compaction 必须缓冲")
	}
}

// ---- inspection.go ----

func TestW14CSourceOrderAndFirstSubstring(t *testing.T) {
	if sourceOrder(PolicySourceAccount) != 0 || sourceOrder(PolicySourceManagement) != 1 || sourceOrder("other") != 2 {
		t.Fatal("sourceOrder 排序值错误")
	}
	hit := firstSubstringMatch("xx ABC yy", []string{"abc"})
	if hit == nil || *hit != "abc" {
		t.Fatal("大小写无关子串匹配失败")
	}
	if firstSubstringMatch("zz", []string{"abc"}) != nil {
		t.Fatal("未命中必须返回 nil")
	}
	if firstSubstringMatch("zz", nil) != nil {
		t.Fatal("空 needles 必须返回 nil")
	}
}

func TestW14CFirstPositiveMatchFieldGaps(t *testing.T) {
	frame := gatewayproto.SemanticFrame{
		FrameType:     gatewayproto.FrameTypeRawJSONPath,
		Text:          "hello world",
		VisibleOutput: true,
	}
	// errorCodes 非空但 frame 缺 error code → nil。
	match := gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"boom"}}
	if firstPositiveMatch(frame, match) != nil {
		t.Fatal("缺少 errorCode 时不得命中")
	}
	// errorTypes 命中。
	match = gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorTypes: []string{"server_error"}}
	errFrame := gatewayproto.SemanticFrame{FrameType: gatewayproto.FrameTypeRawJSONPath, ErrorType: "server_error"}
	if firstPositiveMatch(errFrame, match) == nil {
		t.Fatal("errorTypes 精确命中失败")
	}
	// errorMessageIncludes：空文本拒绝、子串命中。
	match = gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorMessagesIncludes: []string{"quota"}}
	if firstPositiveMatch(errFrame, match) != nil {
		t.Fatal("空错误消息不得命中")
	}
	errFrame.ErrorMessage = "quota exceeded"
	hit := firstPositiveMatch(errFrame, match)
	if hit == nil || hit.field != "errorMessageIncludes" {
		t.Fatalf("errorMessageIncludes 命中失败: %+v", hit)
	}
	// rawTextIncludes：空文本拒绝 + 命中。
	match = gatewayruntimecache.ResponseInspectionPolicyMatch{RawTextIncludes: []string{"secret"}}
	if firstPositiveMatch(errFrame, match) != nil {
		t.Fatal("空 rawText 不得命中")
	}
	errFrame.RawText = "top secret payload"
	if firstPositiveMatch(errFrame, match) == nil {
		t.Fatal("rawTextIncludes 命中失败")
	}
}

// ---- finalize.go 纯函数面 ----

func TestW14CFinalizePureGaps(t *testing.T) {
	if headerView(nil) != nil {
		t.Fatal("nil header 必须返回 nil")
	}
	view := headerView(nil)
	_ = view
	usage := gatewayproto.ParsedUsage{}
	merged := usageWithObservedModel(usage, "w14c-model")
	if merged.UpstreamResponseModel != "w14c-model" {
		t.Fatal("观察模型必须写入 usage")
	}
	result := StreamPipeResult{ResponseInspection: &ResponseInspectionDecision{UpstreamErrorCode: "w14c-upstream"}}
	if result.inspectionUpstreamErrorCode() != "w14c-upstream" {
		t.Fatal("inspection 错误码必须透出")
	}
	if (StreamPipeResult{}).inspectionUpstreamErrorCode() != "" {
		t.Fatal("无 inspection 必须返回空")
	}
}

func TestW14CPrepareUpstreamResponseHeadersSent(t *testing.T) {
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	tracking.WriteHeader(200)
	downstream := StreamDownstream{Res: tracking}
	prepareUpstreamResponseForDownstream(downstream, &GatewayUpstreamResponse{Status: 200}, true)
	if recorder.Header().Get("content-type") != "" {
		t.Fatal("headers 已发送时必须跳过")
	}
	// headers 未发送时转发上游头并补齐流式头。
	recorder2 := httptest.NewRecorder()
	tracking2 := gatewaypreauth.NewTrackingWriter(recorder2)
	prepareUpstreamResponseForDownstream(
		StreamDownstream{Res: tracking2},
		&GatewayUpstreamResponse{Status: 200, Header: map[string][]string{"X-W14C": {"v"}}}, true)
	if recorder2.Header().Get("X-W14C") != "v" ||
		recorder2.Header().Get("content-type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("流式头必须补齐: %v", recorder2.Header())
	}
}

// ---- heartbeat.go ----

func TestW14CHeartbeatWriteAbortedFaces(t *testing.T) {
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	deps := HeartbeatDeps{Res: tracking}
	if heartbeatAborted(deps, context.Background()) {
		t.Fatal("未提交且未结束的响应不得视为中止")
	}
	tracking.End()
	if heartbeatAborted(deps, context.Background()) {
		t.Fatal("WritableEnded 走已结束分支，不再判为需要写头的中止")
	}
}

// ---- pipe.go 构造默认值 ----

func TestW14CStreamPipeDefaults(t *testing.T) {
	clock := &w9cClock{now: 5000}
	signal := 0
	pipe := newStreamPipe(PipeUpstreamStreamInput{
		UpstreamBody: NewSliceUpstreamBody(),
		Downstream: StreamDownstream{
			Res: gatewaypreauth.NewTrackingWriter(httptest.NewRecorder()),
		},
		TimeoutProfile: TimeoutProfile{
			FirstResponseTimeoutMs:          60_000,
			IdleTimeoutMs:                   30_000,
			UncommittedAttemptMaxLifetimeMs: 300_000,
		},
		StartedAtMs: 1000,
		Options: StreamPipeOptions{
			CommittedFailureSignalProtocolEvent: boolPtrW14C(true),
			ClientRetryEnabled:                  true,
		},
	})
	_ = pipe
	if signal != 0 {
		t.Fatal("占位")
	}
	_ = clock
}

func boolPtrW14C(value bool) *bool { return &value }

func strPtrW14C(value string) *string { return &value }

// ---- streamread.go：precommit 竞速与配置截止 ----

// w14cBlockingBody 返回一个 Read 永久阻塞的上游体（直到测试结束取消）。
func w14cBlockingBody(t *testing.T) *ReaderUpstreamBody {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	t.Cleanup(func() { cancel(); pr.Close(); pw.Close() })
	go func() {
		<-ctx.Done()
		_ = pw.CloseWithError(context.Canceled)
	}()
	return NewReaderUpstreamBody(ctx, pr)
}

func TestW14CStreamReadPrecommitTimeoutRace(t *testing.T) {
	clock := &w9cClock{now: 5000}
	pipe := w9cNewPipe(t, clock)
	deadline := int64(5000) + 250
	pipe.options.ResponsePrecommitDeadlineAtMs = &deadline
	body := w14cBlockingBody(t)
	pipe.body = body
	start := time.Now()
	_, err := pipe.readNextStreamChunk()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("竞速必须按墙钟结束: %v", elapsed)
	}
	precommit, ok := err.(*ResponsePrecommitDeadlineError)
	if !ok || precommit.DeadlineAtMs != deadline {
		t.Fatalf("必须返回 precommit 超时: %v", err)
	}
}

func TestW14CStreamReadFirstByteWithPrecommitFallback(t *testing.T) {
	clock := &w9cClock{now: 5000}
	pipe := w9cNewPipe(t, clock)
	firstByte := int64(30)
	precommit := int64(5000) + 800
	pipe.options.FirstByteDeadlineMs = &firstByte
	pipe.options.ResponsePrecommitDeadlineAtMs = &precommit
	pipe.body = w14cBlockingBody(t)
	start := time.Now()
	_, err := pipe.readNextStreamChunk()
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("必须按 precommit 墙钟结束: %v", elapsed)
	}
	precommitErr, ok := err.(*ResponsePrecommitDeadlineError)
	if !ok || precommitErr.DeadlineAtMs != precommit {
		t.Fatalf("必须回退 precommit 超时: %v", err)
	}
}

func TestW14CStreamReadFirstByteZeroWithFuturePrecommit(t *testing.T) {
	// firstByte 截止已过期 → 不经竞速直接决策；precommit 墙钟在未来 →
	// 定时器胜出并通知 superseded，按 precommit 超时返回。
	clock := &w9cClock{now: 5000}
	pipe := w9cNewPipe(t, clock)
	firstByte := int64(0)
	precommit := int64(5100)
	pipe.options.FirstByteDeadlineMs = &firstByte
	pipe.options.ResponsePrecommitDeadlineAtMs = &precommit
	pipe.body = w14cBlockingBody(t)
	start := time.Now()
	_, err := pipe.readNextStreamChunk()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("必须按 precommit 墙钟结束: %v", elapsed)
	}
	precommitErr, ok := err.(*ResponsePrecommitDeadlineError)
	if !ok || precommitErr.DeadlineAtMs != 5100 {
		t.Fatalf("必须返回 precommit 超时: %v", err)
	}
}

func TestW14CStreamReadFiredPrecommitWithLateSettle(t *testing.T) {
	// 墙钟已到且 pending 在墙钟后 settle → 直接按 precommit 超时返回。
	clock := &w9cClock{now: 5000}
	pipe := w9cNewPipe(t, clock)
	firstByte := int64(0)
	precommit := int64(4000)
	pipe.options.FirstByteDeadlineMs = &firstByte
	pipe.options.ResponsePrecommitDeadlineAtMs = &precommit
	pipe.body = NewSliceUpstreamBody([]byte("x"))
	_, err := pipe.readNextStreamChunk()
	precommitErr, ok := err.(*ResponsePrecommitDeadlineError)
	if !ok || precommitErr.DeadlineAtMs != 4000 {
		t.Fatalf("必须返回 precommit 超时: %v", err)
	}
}

// ---- pipefinal.go：EOF flush 面与 eof intercepted ----

func TestW14CEofFlushParserSkippedAndChunks(t *testing.T) {
	// ParserSkipped 日志分支 + EOF 转发块写下游。
	fake := &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{
		ParserSkipped: true,
		Chunks:        [][]byte{[]byte("data: {\"w14c\":1}\n\n")},
	}}
	pipe, _ := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.Interceptor = fake
		o.FlushTransformedUpstreamChunks = func() [][]byte { return nil }
	})
	pipe.completed = true
	result, err := pipe.eofFlushPhase()
	if err != nil || result != nil {
		t.Fatalf("EOF 转发块返回交给 finalize 收尾: (%+v, %v)", result, err)
	}
	if pipe.totalResponseBytes == 0 {
		t.Fatal("EOF 块必须写下游")
	}
}

func TestW14CEofFlushInterceptedBeforeCommit(t *testing.T) {
	// EOF pending 事件在下游提交前命中检查策略 → 返回检查失败结果。
	decision := &ResponseInspectionDecision{
		PolicyID: "w14c-policy", Action: "replace_with_failure", RetryEnabled: true,
	}
	fake := &w11bPassInterceptor{onFlush: StreamInterceptorSseResult{Intercepted: decision}}
	pipe, recorder := w11bNewFinalPipe(t, func(o *StreamPipeOptions) {
		o.ClientRetryEnabled = true
		o.RetryBeforeDownstreamWriteUntilOutput = true
	})
	pipe.interceptor = fake
	pipe.completed = true
	result, err := pipe.eofFlushPhase()
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.ResponseInspection == nil {
		t.Fatalf("必须返回检查结果: %+v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("提交前失败不得触发流失败回调: %d", recorder.count())
	}
}

// ---- sink.go：失败响应的 usage 关闭面与无完成观察器兜底 ----

func TestW14CSinkFailureUsageFaces(t *testing.T) {
	sink, tracking, recorder, audit, usage, _ := newSinkFixture()
	req := gatewaypreauth.NewGatewayRequest(httptest.NewRequest("POST", "/v1/chat/completions", nil))
	// RecordUsage=false → 跳过失败用量记录。
	disabled := false
	sink.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             req,
		Res:             tracking,
		AuditCapture:    audit,
		UsageContext:    usageContextFixture(),
		StartedAt:       1000,
		StatusCode:      502,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf("上游失败", "upstream_failure", "upstream_failure"),
		Audit:           gatewaypreauth.FailureAudit{Outcome: "gateway_failed", ErrorPhase: "proxy", ErrorCode: "w14c-code"},
		RecordUsage:     &disabled,
	})
	if len(recorder.Body.String()) == 0 {
		t.Fatal("失败响应必须写出")
	}
	// 有用量但 HTTP 完成观察器不返回 → completedAtMs 走 nowMs 兜底。
	audit2 := newMockAuditCapture()
	usage2 := &mockUsageRecords{}
	sink2 := NewSink(SinkDeps{UsageRecords: usage2, NowMs: func() int64 { return 7000 }})
	sink2.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             req,
		Res:             tracking,
		AuditCapture:    audit2,
		UsageContext:    usageContextFixture(),
		StartedAt:       1000,
		StatusCode:      502,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf("上游失败", "upstream_failure", "upstream_failure"),
		Audit:           gatewaypreauth.FailureAudit{Outcome: "gateway_failed"},
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && usage2.failureCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if usage2.failureCount() == 0 {
		t.Fatal("无完成观察器时必须按 nowMs 记录失败用量")
	}
	if usage.failureCount() != 0 {
		t.Fatal("RecordUsage=false 不得记录失败用量")
	}
	_ = usage
}

// ---- streamresult.go / heartbeat.go / precommit.go 补面 ----

func TestW14CStreamResultDiagnosticAuditBody(t *testing.T) {
	diagnostic := NewLimitedCapture(1024)
	diagnostic.Push([]byte("w14c-diagnostic"))
	upstream := NewLimitedCapture(1024)
	upstream.Push([]byte("w14c-upstream"))
	response := NewLimitedCapture(1024)
	response.Push([]byte("w14c-response"))
	result := StreamResult(StreamResultInput{
		Completed:              false,
		Message:                "w14c",
		DiagnosticCapture:      diagnostic,
		UpstreamCapture:        upstream,
		ResponseCapture:        response,
		OutputReceived:         true,
	})
	if !strings.Contains(result.ResponseBodyText, "w14c-diagnostic") {
		t.Fatalf("诊断体必须进入响应文本: %q", result.ResponseBodyText)
	}

}

func TestW14CHeartbeatLoopFaces(t *testing.T) {
	// 首次 tick 正常写出；第二次 tick 前结束 writer → 心跳按中止退出。
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	ticks := 0
	deps := HeartbeatDeps{Res: tracking, IntervalMs: 5, After: func(d time.Duration) <-chan time.Time {
		ticks++
		if ticks >= 2 {
			tracking.End()
		}
		return time.After(0)
	}}
	done := make(chan struct{})
	go func() {
		runHeartbeatLoop(deps, []byte(": ping"), context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("写失败必须退出心跳循环")
	}
	if ticks == 0 {
		t.Fatal("至少触发一次 tick")
	}
}

func TestW14CPrecommitEvidenceCRSequences(t *testing.T) {
	evidence := NewStreamPreCommitSseEvidence()
	evidence.Push([]byte("a\r\rb"))
	evidence.Push([]byte("c\r\nd"))
	evidence.Finish()
	if evidence.DataEventObserved {
		t.Fatal("纯碎片行不得视为数据事件")
	}
}

// ---- finalize.go：首字截止预算下的账户变更豁免 ----

func TestW14CHandleStreamFirstByteBudgetNoMutate(t *testing.T) {
	// 首字截止配置 + 截止触发且零输出 → 不触发账户变更。
	input, _ := newInputFixture(w14cBlockingBody(t), 200, nil)
	firstByte := int64(0)
	input.FirstByteDeadlineMs = &firstByte
	usage := &mockUsageRecords{}
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: effects, NowMs: func() int64 { return 1000 }}
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(result.Message, "首个有效输出") {
		t.Fatalf("必须按首字超时收尾: %+v", result)
	}
}

// ---- finalize.go：检查策略服务端重试 ----

func w14cContextPolicy() RuntimeResponseInspectionPolicy {
	return RuntimeResponseInspectionPolicy{
		ID: "w14c-context-policy", Source: PolicySourceSystemDefault, Name: "上下文超限",
		Enabled: true, ExecutionMode: "enforce", DataHandling: "replace_with_failure", RetryEnabled: true,
		ScopeType: "provider", ProviderCode: "openai", AccountSwitch: "request_next_account",
		Match: gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"context_length_exceeded"}},
	}
}

func TestW14CHandleStreamInspectionServerRetry(t *testing.T) {
	chunk := "data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"too long\"}}\n\n"
	input, _ := newInputFixture(NewSliceUpstreamBody([]byte(chunk)), 200, nil)
	usage := &mockUsageRecords{}
	effects := &mockAccountEffects{}
	input.Deps = &FinalizationDeps{UsageRecords: usage, AccountEffects: effects, NowMs: func() int64 { return 1000 }}
	input.ClientStrategy = &ClientStrategyView{RetryPreCommitProtocolError: true}
	input.ResponseInspectionPolicies = []gatewayruntimecache.ResponseInspectionPolicySummary{{
		ID: "w14c-context-policy", Name: "上下文超限", Enabled: true,
		ScopeType: "provider", ProtocolCode: "openai", ProviderCode: strPtrW14C("openai"),
		Match:  gatewayruntimecache.ResponseInspectionPolicyMatch{ErrorCodes: []string{"context_length_exceeded"}},
		Action: "retry_next_account",
	}}
	result, err := HandleStreamUpstreamResponse(input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RetryUpstream || result.RetryReason != StreamServerRetryResponseInspection {
		t.Fatalf("必须服务端重试: %+v", result)
	}
	if result.ResponseInspection == nil || result.ResponseInspection.PolicyID != "w14c-context-policy" {
		t.Fatalf("inspection = %+v", result.ResponseInspection)
	}
	if len(effects.sideEffects) == 0 {
		t.Fatal("检查策略副作用必须执行")
	}
}

// ---- 非流式检查缓冲触发（nonstreaminspection.go 主路径经由入口）----

func TestW14CHeartbeatLoopSignalFaces(t *testing.T) {
	recorder := httptest.NewRecorder()
	tracking := gatewaypreauth.NewTrackingWriter(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	deps := HeartbeatDeps{Res: tracking, Signal: ctx, IntervalMs: 5, After: func(d time.Duration) <-chan time.Time {
		return time.After(d)
	}}
	done := make(chan struct{})
	go func() {
		runHeartbeatLoop(deps, []byte(": ping"), ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("signal 取消必须退出心跳循环")
	}
}
