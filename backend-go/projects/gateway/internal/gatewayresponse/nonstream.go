package gatewayresponse

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// 非流式管道与终态，对齐 finalization.ts 的 handleNonStreamUpstreamResponse /
// FinalizeHandledUpstreamResponse / finalizeNonStreamResponseAfterSseHeartbeat
// 与 upstream/body.ts 的非流式管道。

// NonStreamResponseInspectionMaxBytes 对齐 nonStreamResponseInspectionMaxBytes。
const NonStreamResponseInspectionMaxBytes = 1024 * 1024

// NonStreamResponseCaptureBytes 对齐 nonStreamResponseCaptureBytes。
const NonStreamResponseCaptureBytes = 2 * 1024 * 1024

// NonStreamUsageTailCaptureBytes 对齐 nonStreamUsageTailCaptureBytes。
const NonStreamUsageTailCaptureBytes = 256 * 1024

// NonStreamPipeResult 对齐 NonStreamPipeResult 的消费子集。
type NonStreamPipeResult struct {
	CapturedBody            []byte
	CapturedBodyText        string
	DiagnosticBodyText      string
	UsageTailText           string
	FirstByteMs             *int64
	TransferredBytes        int64
	CaptureTruncated        bool
	FullyBuffered           bool
	InspectionLimitExceeded bool
}

// NonStreamPipeInput 对齐 pipeNonStreamUpstreamResponse 的入参子集。
type NonStreamPipeInput struct {
	Body         UpstreamBody
	Downstream   StreamDownstream
	StartedAtMs  int64
	CaptureBytes int
	CaptureBody  bool
	// InspectBytes>0 启用有界整体缓冲（ForInspection 路径）。
	InspectBytes         int
	RequireFullyBuffered bool
	// FirstByteDeadlineMs 对齐 pipeNonStreamUpstreamResponse 的
	// firstByteDeadlineMs：速度优先普通路由软截止（绝对时刻 =
	// DeadlineStartedAtMs + 值），只约束首个 body 分片的读取。
	FirstByteDeadlineMs *int64
	// DeadlineStartedAtMs 是软截止的计时基准（attempt 时刻，Node
	// routes.ts:1575 给响应面传 attemptStartedAt）。nil 时回退 StartedAtMs。
	// EffectiveDeadlineMs 的定义域是「相对 attemptStart 的时长」，与请求级
	// StartedAtMs 之间隔着 preflight/并发等待/候选轮换——用请求开始当基准会把
	// 这段耗时重复扣除，响应面截止相对 fetch 面提前。
	DeadlineStartedAtMs *int64
	// OnFirstByteDeadline 对齐 onFirstByteDeadline：软截止到点后的路由决策
	// 回调（abort → 抛 configured_deadline 首字超时；continue → 继续等待）。
	// 与 HandleUpstreamResponseInput.OnFirstByteDeadline 同为包内签名
	// （error 返回对齐 handler throw 决策错误）。
	OnFirstByteDeadline FirstByteDeadlineHandler
	// OnFirstByteDeadlineSuperseded 对齐 onFirstByteDeadlineSuperseded：读
	// 胜出且本管线允许原始字节推翻截止决策时通知（对齐 Node body.ts:169 与
	// :305 双管线——纯透传管线 true、检查管线 false）。
	OnFirstByteDeadlineSuperseded func()
	Signal                        interface{ Done() <-chan struct{} }
	PrepareDownstream             func()
	OnChunkRead                   func(chunk []byte)
	OnChunkWritten                func(bytesWritten int64)
	OnBodyCompleted               func(transferredBytes int64)
	OnFirstByte                   func()
	NowMs                         func() int64
}

// PipeNonStreamUpstreamResponse 对齐 pipeNonStreamUpstreamResponse /
// pipeNonStreamUpstreamResponseForInspection：分片转发、有界捕获、usage tail；
// InspectBytes>0 时先整体缓冲（协议校验要求完整文档），超过窗口才转为透传。
// FullyBuffered=true 时下游尚未写入，由调用方在检查通过后发送完整正文。
func PipeNonStreamUpstreamResponse(input NonStreamPipeInput) (NonStreamPipeResult, error) {
	// Body 所有权收口（Node for-await 的 return() 语义）：signal 中断 abort、
	// 读错误 partialFailure、正常 EOF 与 panic 面统一关闭上游体；Close 幂等，
	// 与调用方 HandleNonStreamUpstreamResponse 的收口重复安全。
	if input.Body != nil {
		defer input.Body.Close()
	}
	nowMs := input.NowMs
	if nowMs == nil {
		nowMs = defaultNowMs
	}
	captureLimit := -1
	if input.CaptureBody {
		captureLimit = input.CaptureBytes
		if captureLimit == 0 {
			captureLimit = NonStreamResponseCaptureBytes
		}
	}
	capture := NewLimitedCapture(captureLimit)
	usageTail := NewRollingCapture(NonStreamUsageTailCaptureBytes)
	var result NonStreamPipeResult
	var firstByteMs *int64
	transferred := int64(0)
	committedAny := false
	inspectionMode := input.InspectBytes > 0
	bufferOverflow := false
	// 检查窗口内的原始分片缓冲（整分片保存，超限冲刷时不丢跨界分片的
	// 尾部字节）。
	var bufferedChunks [][]byte
	bufferedBytes := 0

	markFirstByte := func() {
		if firstByteMs != nil {
			return
		}
		value := nowMs() - input.StartedAtMs
		firstByteMs = &value
		if input.OnFirstByte != nil {
			input.OnFirstByte()
		}
	}
	writeThrough := func(chunk []byte) error {
		if !committedAny && input.PrepareDownstream != nil {
			committedAny = true
			input.PrepareDownstream()
		}
		written, err := input.Downstream.Res.Write(chunk)
		FlushGateway(input.Downstream.Res)
		transferred += int64(written)
		if input.OnChunkWritten != nil {
			input.OnChunkWritten(int64(written))
		}
		return err
	}
	partialFailure := func(original error) (NonStreamPipeResult, error) {
		result.TransferredBytes = transferred
		result.FirstByteMs = firstByteMs
		result.CapturedBody = capture.Buffer()
		result.CapturedBodyText, _ = capture.ToText()
		result.DiagnosticBodyText, _ = capture.ToDiagnosticText()
		result.UsageTailText, _ = usageTail.Text()
		result.CaptureTruncated = capture.IsTruncated()
		return result, &NonStreamBodyPipeError{OriginalError: original, PartialResult: partialResultOf(result)}
	}

	// 速度优先首块竞速（R5）：配置了软截止时，首个 body 分片的读取与截止
	// 竞速（Node pipeNonStreamUpstreamResponse / ForInspection 的首块
	// readFirstNonStreamChunkWithDeadlines）；后续分片保持原逻辑。
	deadlineRaced := false
	var racedChunk ChunkResult
	var racedDone bool
	var racedErr error
	if input.FirstByteDeadlineMs != nil && *input.FirstByteDeadlineMs > 0 {
		// 检查管线（InspectBytes>0）只有完整语义响应可推翻截止（supersede
		// false，Node body.ts:305）；纯透传管线原始字节即可推翻（true，
		// body.ts:169）。
		racedChunk, racedDone, racedErr = raceFirstNonStreamChunkWithDeadline(input, !inspectionMode, nowMs)
		deadlineRaced = true
	}

	for {
		if signalAbortedChannel(input.Signal) {
			return result, &UpstreamRequestAbortedError{Message: ErrUpstreamRequestAbortedMessage, UpstreamRequestStarted: true}
		}
		var chunkResult ChunkResult
		if deadlineRaced {
			deadlineRaced = false
			if racedErr != nil {
				if IsUpstreamRequestAbortedError(racedErr) {
					return result, racedErr
				}
				return partialFailure(racedErr)
			}
			if racedDone {
				break
			}
			chunkResult = racedChunk
		} else {
			var ok bool
			chunkResult, ok = <-input.Body.Next()
			if !ok {
				break
			}
		}
		if chunkResult.Err != nil {
			if errors.Is(chunkResult.Err, io.EOF) {
				break
			}
			return partialFailure(chunkResult.Err)
		}
		chunk := chunkResult.Data
		if input.OnChunkRead != nil {
			input.OnChunkRead(chunk)
		}
		capture.Push(chunk)
		usageTail.Push(chunk)

		if inspectionMode && !bufferOverflow {
			bufferedChunks = append(bufferedChunks, chunk)
			bufferedBytes += len(chunk)
			if bufferedBytes > input.InspectBytes {
				markFirstByte()
				bufferOverflow = true
				result.InspectionLimitExceeded = true
				if input.RequireFullyBuffered {
					// requireFullyBuffered=true（协议校验要求完整文档）：超限即
					// 停止转发，不做边透传；下游零字节，由调用方按“拒绝透传
					// 未验证正文”收 502（Node validateBufferedJsonProtocolResponse
					// 的 protocolValidationLimitExceeded 分支）。
					break
				}
				// 超过检查窗口：冲刷完整缓冲字节并转透传，边转发并跳过完整
				// 语义检查（Node inspection window 溢出）。
				for _, buffered := range bufferedChunks {
					if len(buffered) == 0 {
						continue
					}
					if err := writeThrough(buffered); err != nil {
						return partialFailure(err)
					}
				}
				bufferedChunks = nil
				continue
			}
			continue
		}
		markFirstByte()
		if err := writeThrough(chunk); err != nil {
			return partialFailure(err)
		}
	}
	result.TransferredBytes = transferred
	result.FirstByteMs = firstByteMs
	result.CapturedBody = capture.CompleteBuffer()
	if text, ok := capture.ToText(); ok {
		result.CapturedBodyText = text
	}
	if diagnostic, ok := capture.ToDiagnosticText(); ok {
		result.DiagnosticBodyText = diagnostic
	}
	if text, ok := usageTail.Text(); ok {
		result.UsageTailText = text
	}
	result.CaptureTruncated = capture.IsTruncated()
	if inspectionMode && !bufferOverflow {
		// 完整缓冲：不写下游，由调用方检查后发送（res.send(completeBody)）。
		result.FullyBuffered = true
		completeBody := bytes.Join(bufferedChunks, nil)
		result.CapturedBody = completeBody
		result.CapturedBodyText = string(completeBody)
	}
	return result, nil
}

// SendFullyBufferedNonStreamBody 把完整缓冲正文写给下游（对齐 Node 的
// res.send(downstreamBody) 提交序列）。
func SendFullyBufferedNonStreamBody(input NonStreamPipeInput, body []byte) error {
	if input.PrepareDownstream != nil {
		input.PrepareDownstream()
	}
	if _, err := input.Downstream.Res.Write(body); err != nil {
		return err
	}
	FlushGateway(input.Downstream.Res)
	if input.OnChunkWritten != nil {
		input.OnChunkWritten(int64(len(body)))
	}
	return nil
}

// nonStreamFirstChunkFuture 把首个 body 分片的读取收敛为单值 future：数据源
// goroutine 是 body.Next() 的唯一消费者，结果经缓存共享，done 关闭广播就绪。
// 主流程 select 与截止决策（经 pendingRead 工厂）竞争唤醒，先到者触发缓存，
// 不存在第二个 channel 消费者，避免双消费与并发 Await 死锁。
type nonStreamFirstChunkFuture struct {
	done  chan struct{}
	mu    sync.Mutex
	chunk ChunkResult
}

func observeNonStreamFirstChunk(body UpstreamBody) *nonStreamFirstChunkFuture {
	future := &nonStreamFirstChunkFuture{done: make(chan struct{})}
	go func() {
		result, ok := <-body.Next()
		if !ok {
			// 上游 future 源被关闭（管道 Close / 连接中断）按干净 EOF 收敛。
			result = ChunkResult{Err: io.EOF}
		}
		future.mu.Lock()
		future.chunk = result
		future.mu.Unlock()
		close(future.done)
	}()
	return future
}

func (f *nonStreamFirstChunkFuture) settle() ChunkResult {
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chunk
}

// raceFirstNonStreamChunkWithDeadline 对齐 readFirstNonStreamChunkWithDeadlines
// （upstream/body.ts 的非流式首块读取竞速）：首块读取与软截止竞速，到点后
// 等待路由决策——abort 抛 configured_deadline 首字超时；continue 继续等待
// 原始读；决策期间读已 settle 时按 pendingReadSupersedesDeadline 分支（纯
// 透传管线原始字节推翻截止并通知 superseded；检查管线只有语义决策可推翻，
// abort 决策按「仍未返回完整语义响应」超时）。决策与错误语义单一事实源是
// gatewaydispatch 的导出决策原语；此处镜像的仅是竞速骨架，因为本管道的
// 数据源是 UpstreamBody 的 channel future，而非 dispatch 管道的 io.Reader。
func raceFirstNonStreamChunkWithDeadline(
	input NonStreamPipeInput,
	pendingReadSupersedesDeadline bool,
	nowMs func() int64,
) (ChunkResult, bool, error) {
	deadlineMs := *input.FirstByteDeadlineMs
	var abortCh <-chan struct{}
	if input.Signal != nil {
		abortCh = input.Signal.Done()
	}
	future := observeNonStreamFirstChunk(input.Body)
	pendingRead := gatewaydispatch.ObserveFirstBytePendingRead(func() (ChunkResult, error) {
		chunk := future.settle()
		return chunk, chunk.Err
	})
	// 包内 handler（error 返回对齐 handler throw）桥接为 dispatch 决策原语
	// 的 handler：throw 经 panic 交给 runDeadlineHandler 的 recover 转为
	// decisionError，保持单一事实源的决策语义与 panic 保护。
	var dispatchDeadlineHandler gatewaydispatch.FirstByteDeadlineHandler
	if input.OnFirstByteDeadline != nil {
		handler := input.OnFirstByteDeadline
		dispatchDeadlineHandler = func(in gatewaydispatch.FirstByteDeadlineDecisionInput) gatewaydispatch.FirstByteDeadlineAction {
			action, handlerErr := handler(FirstByteDeadlineInput{
				ElapsedMs: in.ElapsedMs,
				TimeoutMs: in.TimeoutMs,
				Transport: in.Transport,
			})
			if handlerErr != nil {
				panic(handlerErr)
			}
			return gatewaydispatch.FirstByteDeadlineAction(action)
		}
	}

	deadlineStartedAtMs := input.StartedAtMs
	if input.DeadlineStartedAtMs != nil {
		deadlineStartedAtMs = *input.DeadlineStartedAtMs
	}
	softDeadlineAt := deadlineStartedAtMs + deadlineMs
	remainingMs := softDeadlineAt - nowMs()
	if remainingMs > 0 {
		timer := time.NewTimer(time.Duration(remainingMs) * time.Millisecond)
		select {
		case <-timer.C:
			// 软截止到点 → 路由决策。
		case <-abortCh:
			timer.Stop()
			return ChunkResult{}, false, &UpstreamRequestAbortedError{Message: ErrUpstreamRequestAbortedMessage, UpstreamRequestStarted: true}
		case <-future.done:
			timer.Stop()
			chunk := future.settle()
			return resolveNonStreamFirstChunkResult(chunk)
		}
	}

	// 软截止到点：等待路由决策（handler 拥有共享的切换预留与慢观察审计）。
	decision := gatewaydispatch.DecideFirstByteDeadlineAfterPendingRead(
		pendingRead,
		dispatchDeadlineHandler,
		gatewaydispatch.FirstByteDeadlineDecisionInput{
			ElapsedMs: nowMs() - deadlineStartedAtMs,
			TimeoutMs: deadlineMs,
			Transport: "non_stream",
		},
		gatewaydispatch.FirstByteDeadlineDecisionWaitOptions{},
	)
	if decision.Type == gatewaydispatch.DeadlineDecisionRead {
		// 决策期间原始读已 settle：按管线语义判定能否推翻截止。
		if pendingReadSupersedesDeadline {
			if input.OnFirstByteDeadlineSuperseded != nil {
				input.OnFirstByteDeadlineSuperseded()
			}
			return resolveNonStreamFirstChunkResult(decision.Result)
		}
		if decision.DecisionError != nil {
			return ChunkResult{}, false, decision.DecisionError
		}
		if decision.Action == gatewaydispatch.FirstByteDeadlineActionAbort {
			return ChunkResult{}, false, nonStreamConfiguredDeadlineError(deadlineMs, true)
		}
		return resolveNonStreamFirstChunkResult(decision.Result)
	}
	if decision.Error != nil {
		// handler panic / 返回错误且读未 settle：决策失败按原始错误上抛。
		return ChunkResult{}, false, decision.Error
	}
	if decision.Action == gatewaydispatch.FirstByteDeadlineActionAbort {
		return ChunkResult{}, false, nonStreamConfiguredDeadlineError(deadlineMs, false)
	}

	// continue：软截止已观测且不再重建计时器（Node 循环重复但 soft 已
	// observed），等待原始读或客户端中断。
	for {
		select {
		case <-future.done:
			chunk := future.settle()
			return resolveNonStreamFirstChunkResult(chunk)
		case <-abortCh:
			return ChunkResult{}, false, &UpstreamRequestAbortedError{Message: ErrUpstreamRequestAbortedMessage, UpstreamRequestStarted: true}
		}
	}
}

// resolveNonStreamFirstChunkResult 把首块 future 结果收敛为管道循环入参：
// Err == io.EOF 表示干净 EOF（done），读取错误原样上抛。
func resolveNonStreamFirstChunkResult(chunk ChunkResult) (ChunkResult, bool, error) {
	if errors.Is(chunk.Err, io.EOF) {
		return ChunkResult{}, true, nil
	}
	return chunk, false, nil
}

// nonStreamConfiguredDeadlineError 对齐 dispatch 侧 configured_deadline 首字
// 超时；semanticWait 区分 timer 胜出（首个字节）与检查管线语义决策（完整
// 语义响应）两个消息分支。
func nonStreamConfiguredDeadlineError(deadlineMs int64, semanticWait bool) error {
	suffix := "首个字节"
	if semanticWait {
		suffix = "完整语义响应"
	}
	return &gatewaydispatch.GatewayFirstByteTimeoutError{
		Message:   "上游非流式响应 " + itoa(ceilDiv(deadlineMs, 1000)) + "s 后仍未返回" + suffix,
		TimeoutMs: deadlineMs,
		Source:    gatewaydispatch.FirstByteTimeoutSourceConfiguredDeadline,
	}
}

func partialResultOf(result NonStreamPipeResult) NonStreamPipeResult {
	return result
}

func signalAbortedChannel(signal interface{ Done() <-chan struct{} }) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal.Done():
		return true
	default:
		return false
	}
}

// HandleNonStreamUpstreamResponse 对齐 handleNonStreamUpstreamResponse。
// 失败分类、审计触发点与 usage 组装逐字段对齐；上游非流式管道由
// PipeNonStreamUpstreamResponse 承担。
func HandleNonStreamUpstreamResponse(input HandleUpstreamResponseInput) (UpstreamResponseHandlingResult, error) {
	// Body 所有权收口：覆盖管道进入前的 signal 已 abort 早退，以及管道与
	// 后置 finalize 序列的全部返回路径；Close 幂等。
	if input.UpstreamResponse != nil && input.UpstreamResponse.Body != nil {
		defer input.UpstreamResponse.Body.Close()
	}
	if signalAborted(input.Signal) {
		return UpstreamResponseHandlingResult{}, &UpstreamRequestAbortedError{Message: ErrUpstreamRequestAbortedMessage, UpstreamRequestStarted: true}
	}
	if input.DownstreamCommitState.TransportCommitted && !input.DownstreamCommitState.SemanticCommitted {
		return input.finalizeNonStreamResponseAfterSseHeartbeat()
	}
	firstOutputMarked := false
	driver := input.driver()
	responseEndpointFamily := driver.EndpointFamilyForPath(input.Req.PathAndQuery())
	responsesTracker := (*ResponsesRootStatusTracker)(nil)
	if responseEndpointFamily == gatewayproto.EndpointFamilyResponses {
		responsesTracker = NewResponsesRootStatusTracker()
	}
	transportResponseSuccessful := input.UpstreamResponse.OK()
	// Protocol-shaped endpoints must validate a complete 2xx even when the
	// provider lies about content-type（Node nonStreamJsonProtocolValidationAllowed）。
	protocolValidationEnabled := transportResponseSuccessful &&
		nonStreamJsonProtocolValidationAllowed(input, responseEndpointFamily)

	var pipeResult NonStreamPipeResult
	var pipeErr error
	if input.UpstreamResponse.Body == nil {
		if input.UpstreamResponse.Status == 204 || input.UpstreamResponse.Status == 205 {
			if !input.successfulEmptyUpstreamAllowed() {
				emptyProtocolFailure := emptyUpstreamProtocolFailure()
				input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
					StatusCode:      input.UpstreamResponse.Status,
					ResponseHeaders: input.UpstreamResponse.Header,
					ResponseBody:    []byte{},
					Success:         false,
					ErrorPhase:      "upstream_response",
					ErrorCode:       emptyProtocolFailure.Code,
					ErrorMessage:    emptyProtocolFailure.Message,
				})
				return UpstreamResponseHandlingResult{
					Usage:        gatewayproto.EmptyUsage(),
					FirstTokenMs: int64PtrOf(nowMsOf(&input)() - input.StartedAtMs),
					ErrorPayload: emptyProtocolFailure,
				}, nil
			}
		}
		prepareUpstreamResponseForDownstream(input.Downstream, input.UpstreamResponse, false)
		input.DownstreamCommitState.MarkTransportCommitted(0)
		input.Downstream.End()
		input.DownstreamCommitState.MarkSemanticCommitted(0)
		pipeResult.FirstByteMs = int64PtrOf(nowMsOf(&input)() - input.StartedAtMs)
		if input.MarkFirstOutput != nil {
			input.MarkFirstOutput()
		}
	} else {
		// Node inspectJsonResponse（finalization.ts:944-949）：上游错误体、协议
		// 校验路径，或 JSON 内容类型且策略/语义要求缓冲时才走有界检查缓冲；
		// 其余正文走纯透传管道（如 audio/speech 二进制），不再无条件 1MB 缓冲。
		inspectJSON := !input.UpstreamResponse.OK() || protocolValidationEnabled ||
			(isOpenAIJSONResponseContentType(input.UpstreamResponse.Header.Get("Content-Type")) &&
				shouldBufferNonStreamJSONResponse(input))
		pipeSpec := NonStreamPipeInput{
			Body:        input.UpstreamResponse.Body,
			Downstream:  input.Downstream,
			StartedAtMs: input.StartedAtMs,
			CaptureBody: !input.UpstreamResponse.OK() || input.AuditCapture.ShouldCaptureSuccessPayloads() || responseEndpointFamily == gatewayproto.EndpointFamilyResponses,
			Signal:      input.Signal,
			PrepareDownstream: func() {
				prepareUpstreamResponseForDownstream(input.Downstream, input.UpstreamResponse, false)
				input.DownstreamCommitState.MarkTransportCommitted(0)
			},
			OnChunkRead: func(chunk []byte) {
				if responsesTracker != nil {
					responsesTracker.Push(chunk)
				}
			},
			OnChunkWritten: func(bytesWritten int64) {
				input.DownstreamCommitState.MarkSemanticCommitted(bytesWritten)
			},
			OnBodyCompleted: func(transferredBytes int64) {
				if transferredBytes == 0 {
					input.DownstreamCommitState.MarkSemanticCommitted(0)
				}
			},
			OnFirstByte: func() {
				if input.MarkFirstOutput != nil {
					input.MarkFirstOutput()
				}
			},
			NowMs: nowMsOf(&input),
			// 速度优先软截止与决策回调透传（对齐流式 StreamPipeOptions 的
			// FirstByteDeadlineMs / OnFirstByteDeadline 装配）。
			FirstByteDeadlineMs:           timeoutsWithDisabled(input.TimeoutProfile, input.FirstByteDeadlineMs),
			DeadlineStartedAtMs:           input.DeadlineStartedAtMs,
			OnFirstByteDeadline:           input.OnFirstByteDeadline,
			OnFirstByteDeadlineSuperseded: input.OnFirstByteDeadlineSuperseded,
		}
		if inspectJSON {
			pipeSpec.InspectBytes = NonStreamResponseInspectionMaxBytes
			// Node requireFullyBuffered: protocolValidationEnabled。
			pipeSpec.RequireFullyBuffered = protocolValidationEnabled
		}
		pipeResult, pipeErr = PipeNonStreamUpstreamResponse(pipeSpec)
	}
	if pipeErr != nil {
		return input.handleNonStreamPipeError(pipeErr)
	}

	responseBody := pipeResult.CapturedBody
	responseBodyText := pipeResult.CapturedBodyText
	if !input.UpstreamResponse.OK() && responseBodyText == "" {
		responseBodyText = pipeResult.DiagnosticBodyText
	}
	if pipeResult.CaptureTruncated && input.UpstreamResponse.OK() {
		responseBodyText = ""
	}

	// 完整缓冲与协议校验的提交契约（finalization.ts:1007-1086）：
	// - 协议校验开启且超过验证窗口：管道已停止转发，按“拒绝透传未验证正文”
	//   以 502 协议诊断收尾（validateBufferedJsonProtocolResponse 的
	//   protocolValidationLimitExceeded 分支）。
	// - 协议校验开启且完整缓冲：校验通过后发送完整正文（res.send(downstreamBody)）。
	// - 协议校验关闭但完整缓冲（上游 4xx/5xx 错误体、策略要求缓冲的小 2xx）：
	//   原样发送缓冲正文，客户端收到上游状态与正文（D-108 空 200 修复）。
	// - 非必需缓冲超限：管道已边转发，仅记录检查省略告警。
	if protocolValidationEnabled && pipeResult.InspectionLimitExceeded {
		parsedForValidation := ParseGatewayNonStreamJsonBody(responseBodyText, len(responseBodyText) > 0, input.UpstreamResponse.Header)
		// limitExceeded=true 时 ValidateBufferedJsonProtocolResponse 必返回失败。
		failure := ValidateBufferedJsonProtocolResponse(parsedForValidation, true, true, string(responseEndpointFamily), LowercasedRequestPath(input.Req.PathAndQuery()))
		return input.finalizeBufferedJSONProtocolFailure(failure, parsedForValidation, pipeResult, responseBody, responseBodyText, driver)
	}
	if pipeResult.FullyBuffered {
		// 检查策略主链（D-112）：完整缓冲 JSON 先跑响应检查策略（含 codex
		// 契约帧），命中时由此收尾（失败改写或服务端换号重试）；未命中回退
		// 协议校验与原样发送。
		parsedForInspection := ParseGatewayNonStreamJsonBody(responseBodyText, len(responseBodyText) > 0, input.UpstreamResponse.Header)
		if handled := input.inspectBufferedGatewayJSONResponse(InspectBufferedGatewayJSONArgs{
			ResponseBody:              pipeResult.CapturedBody,
			ResponseBodyText:          responseBodyText,
			ParsedJSONBody:            parsedForInspection,
			FirstTokenMs:              pipeResult.FirstByteMs,
			ProtocolValidationEnabled: protocolValidationEnabled,
		}); handled != nil {
			return *handled, nil
		}
		if protocolValidationEnabled {
			parsedForValidation := ParseGatewayNonStreamJsonBody(responseBodyText, len(responseBodyText) > 0, input.UpstreamResponse.Header)
			if failure := ValidateBufferedJsonProtocolResponse(parsedForValidation, true, false, string(responseEndpointFamily), LowercasedRequestPath(input.Req.PathAndQuery())); failure != nil {
				return input.finalizeBufferedJSONProtocolFailure(failure, parsedForValidation, pipeResult, responseBody, responseBodyText, driver)
			}
		}
		// 检查通过（或校验关闭）：发送完整正文（Node res.send(downstreamBody)）。
		if pipeResult.FirstByteMs == nil {
			value := nowMsOf(&input)() - input.StartedAtMs
			pipeResult.FirstByteMs = &value
		}
		forwardInput := NonStreamPipeInput{
			Downstream:  input.Downstream,
			StartedAtMs: input.StartedAtMs,
			Signal:      input.Signal,
			PrepareDownstream: func() {
				prepareUpstreamResponseForDownstream(input.Downstream, input.UpstreamResponse, false)
				input.DownstreamCommitState.MarkTransportCommitted(0)
			},
			OnChunkWritten: func(bytesWritten int64) {
				input.DownstreamCommitState.MarkSemanticCommitted(bytesWritten)
			},
			NowMs: nowMsOf(&input),
		}
		if err := SendFullyBufferedNonStreamBody(forwardInput, pipeResult.CapturedBody); err != nil {
			return UpstreamResponseHandlingResult{}, err
		}
		markFirstOutputOnce(&firstOutputMarked, input.MarkFirstOutput)
	} else if pipeResult.InspectionLimitExceeded {
		input.logger().Warn("gateway_non_stream_response_inspection_omitted", map[string]any{
			"accountId":        input.Account.GetID(),
			"statusCode":       input.UpstreamResponse.Status,
			"transferredBytes": pipeResult.TransferredBytes,
			"inspectBytes":     NonStreamResponseInspectionMaxBytes,
			"endpoint":         input.UsageContext.Endpoint,
		}, "网关非流式 JSON 响应超过检查窗口，已边转发并跳过完整语义检查")
	}
	// Responses 根节点失败终态扫描。
	responsesFailedTerminal := transportResponseSuccessful &&
		responseEndpointFamily == gatewayproto.EndpointFamilyResponses &&
		responsesTracker != nil && responsesTracker.HasFailedStatus()

	usage := gatewayproto.EmptyUsage()
	var parsedJsonBody GatewayNonStreamJsonBody
	if responseBodyText != "" || pipeResult.CapturedBodyText != "" {
		text := responseBodyText
		if text == "" {
			text = pipeResult.UsageTailText
		}
		if text != "" {
			parsedJsonBody = ParseGatewayNonStreamJsonBody(text, true, input.UpstreamResponse.Header)
		}
	}
	if parsedJsonBody.Status == NonStreamJSONStatusValid {
		usage = driver.ExtractUsageFromJSONValue(parsedJsonBody.Value)
	} else if pipeResult.UsageTailText != "" {
		usage = driver.ExtractUsageFromJSONTextFragment(pipeResult.UsageTailText, parsedJsonBody.Status == NonStreamJSONStatusInvalid)
	}
	_ = responseBody
	var errorPayload gatewayproto.ErrorPayload
	if !input.UpstreamResponse.OK() {
		if parsedJsonBody.Status == NonStreamJSONStatusValid {
			errorPayload = driver.ParseErrorPayloadFromJSONValue(parsedJsonBody.Value)
		} else {
			errorPayload = driver.ParseErrorPayload(responseBodyText, input.UpstreamResponse.Header)
		}
	}
	if responsesFailedTerminal && errorPayload.Code != "upstream_protocol_failure" {
		errorPayload = gatewayproto.ErrorPayload{
			Code:    "upstream_protocol_failure",
			Message: "上游 Responses 返回失败终态",
		}
	}
	forwardedResponseSuccessful := transportResponseSuccessful && !responsesFailedTerminal

	// 图像 JSON 正文省略。
	var bodyOmission *StreamBodyOmissionSummary
	if forwardedResponseSuccessful {
		bodyOmission = nonStreamImageResponseBodyOmission(firstNonEmpty(responseBodyText, pipeResult.CapturedBodyText, pipeResult.UsageTailText), pipeResult.CapturedBody, parsedJsonBody)
		if bodyOmission != nil {
			responseBodyText = ""
			pipeResult.CapturedBody = nil
			pipeResult.CapturedBodyText = ""
		}
	}
	responseBody = pipeResult.CapturedBody
	input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
		StatusCode:      input.UpstreamResponse.Status,
		ResponseHeaders: input.UpstreamResponse.Header,
		ResponseBody:    responseBody,
		Success:         forwardedResponseSuccessful,
		ErrorPhase:      errorPhaseFor(forwardedResponseSuccessful),
		ErrorCode:       errorPayload.Code,
		ErrorMessage:    errorPayload.Message,
	})

	protocolValidated := forwardedResponseSuccessful && ProtocolValidatedNonStreamResponse(
		parsedJsonBody,
		input.UpstreamResponse.Status,
		string(responseEndpointFamily),
		LowercasedRequestPath(input.Req.PathAndQuery()),
	)
	return UpstreamResponseHandlingResult{
		Usage:                      usage,
		FirstTokenMs:               pipeResult.FirstByteMs,
		ResponseBodyText:           responseBodyText,
		BodyOmission:               bodyOmission,
		ProtocolValidatedSuccess:   protocolValidated,
		PassthroughUpstreamFailure: false,
		ErrorPayload:               errorPayload,
	}, nil
}

func markFirstOutputOnce(marked *bool, markFirstOutput func()) {
	if *marked || markFirstOutput == nil {
		return
	}
	*marked = true
	markFirstOutput()
}

func errorPhaseFor(forwarded bool) string {
	if forwarded {
		return ""
	}
	return "upstream_response"
}

// handleNonStreamPipeError 对齐非流式管道错误的 downstream/body-interrupted 分支。
func (input *HandleUpstreamResponseInput) handleNonStreamPipeError(pipeErr error) (UpstreamResponseHandlingResult, error) {
	if IsUpstreamRequestAbortedError(pipeErr) || signalAborted(input.Signal) {
		if input.Deps != nil && input.Deps.UsageRecords != nil {
			input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
				UsageContext:    input.UsageContext,
				Account:         input.Account,
				RequestedModel:  requestModelHint(input.Req),
				StatusCode:      input.UpstreamResponse.Status,
				Success:         false,
				Stream:          false,
				StartedAtMs:     input.StartedAtMs,
				Usage:           usageWithObservedModel(gatewayproto.EmptyUsage(), input.UpstreamResponse.UpstreamResponseModel),
				ErrorMessage:    DownstreamConnectionClosedMessage,
				RequestSnapshot: usageRequestSnapshotView(input.UsageContext),
				ResponseSnapshot: &UsageResponseSnapshotView{
					UpstreamURL:  input.UpstreamURL,
					StatusCode:   input.UpstreamResponse.Status,
					Headers:      headerView(input.UpstreamResponse.Header),
					ErrorMessage: DownstreamConnectionClosedMessage,
				},
			})
		}
		input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
			StatusCode:      input.UpstreamResponse.Status,
			ResponseHeaders: input.UpstreamResponse.Header,
			Success:         false,
			ErrorPhase:      "downstream",
			ErrorMessage:    DownstreamConnectionClosedMessage,
		})
	}
	return UpstreamResponseHandlingResult{}, pipeErr
}

// finalizeBufferedJsonProtocolFailure 的 Go 版：完整但无效的 2xx 是本次尝试
// 的确凿失败，按 502 返回协议诊断。会话亲和遗忘与 http metric 标注对齐
// non-stream-json-inspection.ts 的 finalizeBufferedJsonProtocolFailure。
func (input *HandleUpstreamResponseInput) finalizeBufferedJSONProtocolFailure(
	failure *ProtocolFailure,
	parsedJsonBody GatewayNonStreamJsonBody,
	pipeResult NonStreamPipeResult,
	responseBody []byte,
	responseBodyText string,
	driver ResponseDriverPort,
) (UpstreamResponseHandlingResult, error) {
	var usage gatewayproto.ParsedUsage
	if parsedJsonBody.Status == NonStreamJSONStatusValid {
		usage = driver.ExtractUsageFromJSONValue(parsedJsonBody.Value)
	} else {
		text := responseBodyText
		if text == "" {
			text = pipeResult.UsageTailText
		}
		usage = driver.ExtractUsageFromJSONTextFragment(text, true)
	}
	input.forgetSessionAffinityForFailure()
	input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
		StatusCode:      input.UpstreamResponse.Status,
		ResponseHeaders: input.UpstreamResponse.Header,
		ResponseBody:    responseBody,
		Success:         false,
		ErrorPhase:      "upstream_response",
		ErrorCode:       failure.ErrorCode,
		ErrorMessage:    failure.Message,
	})
	if input.Deps != nil && input.Deps.UsageRecords != nil {
		input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
			UsageContext:    input.UsageContext,
			Account:         input.Account,
			RequestedModel:  requestModelHint(input.Req),
			StatusCode:      input.UpstreamResponse.Status,
			Success:         false,
			Stream:          gatewaypreauth.IsOpenAIStreamRequest(input.Req),
			FirstTokenMs:    pipeResult.FirstByteMs,
			StartedAtMs:     input.StartedAtMs,
			Usage:           usageWithObservedModel(usage, input.UpstreamResponse.UpstreamResponseModel),
			ErrorCode:       failure.ErrorCode,
			ErrorMessage:    failure.Message,
			RequestSnapshot: usageRequestSnapshotView(input.UsageContext),
			ResponseSnapshot: &UsageResponseSnapshotView{
				UpstreamURL:  input.UpstreamURL,
				StatusCode:   input.UpstreamResponse.Status,
				Headers:      headerView(input.UpstreamResponse.Header),
				BodyText:     responseBodyText,
				ErrorMessage: failure.Message,
			},
		})
	}
	if input.Deps != nil && input.Deps.AccountEffects != nil {
		input.Deps.AccountEffects.DispatchRequestFailureAccountHealthCheck(input.UsageContext.TrafficSource, input.Account.GetID())
	}
	clientErrorProtocol := gatewaypreauth.GatewayErrorProtocol(driver.ClientErrorProtocol())
	responsePayload := gatewaypreauth.GatewayErrorPayloadOf(failure.Message, "upstream_response_error", failure.ErrorCode)
	clientPayload := gatewaypreauth.GatewayErrorPayloadForProtocol(responsePayload, clientErrorProtocol)
	sendGatewayErrorResponseForSink(input.Downstream.Res, 502, responsePayload, gatewaypreauth.SendGatewayErrorResponseOptions{
		Protocol: clientErrorProtocol,
	})
	finalizeInput := gatewaypreauth.AuditFinalizeInput{
		Outcome:          "upstream_failed",
		Success:          false,
		StatusCode:       502,
		ResponseHeaders:  responseHeadersToObject(input.Downstream.Res.Header()),
		ResponseBody:     marshalClientPayload(clientPayload),
		ResponsePartType: "gateway_error",
		ErrorPhase:       "upstream_response",
		ErrorCode:        failure.ErrorCode,
		ErrorMessage:     failure.Message,
	}
	input.finalizeAuditWithExtras(finalizeInput, AuditFinalizeExtras{
		AccountID:    input.Account.GetID(),
		FirstTokenMs: pipeResult.FirstByteMs,
	})
	return UpstreamResponseHandlingResult{AlreadyFinalized: true, ErrorCode: failure.ErrorCode}, nil
}

// finalizeNonStreamResponseAfterSseHeartbeat 对齐
// finalizeNonStreamResponseAfterSseHeartbeat。
func (input *HandleUpstreamResponseInput) finalizeNonStreamResponseAfterSseHeartbeat() (UpstreamResponseHandlingResult, error) {
	message := "等待可用账户期间已建立 SSE 保活连接，但上游返回了非流式响应，请客户端重试"
	input.UpstreamResponse.Body.Close()
	driver := input.driver()
	failureEvent := gatewaypreauth.BuildGatewayStreamFailureEventForProtocol(
		gatewaypreauth.GatewayStreamClientRetryMessage,
		gatewaypreauth.GatewayStreamClientRetryErrorCode,
		gatewaypreauth.GatewayErrorProtocol(driver.ClientErrorProtocol()),
		gatewaypreauth.OpenAIGatewayDownstreamProtocol(clientStrategyDownstreamProtocol(input.ClientStrategy)),
	)
	tracking, isTracking := input.Downstream.Res.(*gatewaypreauth.TrackingWriter)
	writableEnded := isTracking && tracking.WritableEnded()
	destroyed := input.Downstream.DestroyedNow()
	if len(failureEvent) > 0 && !writableEnded && !destroyed {
		_, _ = input.Downstream.Res.Write(failureEvent)
		FlushGateway(input.Downstream.Res)
		input.DownstreamCommitState.MarkSemanticCommitted(int64(len(failureEvent)))
		input.Downstream.End()
	} else if !writableEnded && !destroyed {
		input.Downstream.InterruptNow()
	}
	input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
		StatusCode:      input.UpstreamResponse.Status,
		ResponseHeaders: input.UpstreamResponse.Header,
		Success:         false,
		ErrorPhase:      "downstream",
		ErrorCode:       "downstream_transport_conflict",
		ErrorMessage:    message,
	})
	if input.Deps != nil && input.Deps.UsageRecords != nil {
		input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
			UsageContext:    input.UsageContext,
			Account:         input.Account,
			RequestedModel:  requestModelHint(input.Req),
			StatusCode:      input.UpstreamResponse.Status,
			Success:         false,
			Stream:          true,
			StartedAtMs:     input.StartedAtMs,
			Usage:           usageWithObservedModel(gatewayproto.EmptyUsage(), input.UpstreamResponse.UpstreamResponseModel),
			ErrorCode:       "downstream_transport_conflict",
			ErrorMessage:    message,
			RequestSnapshot: usageRequestSnapshotView(input.UsageContext),
			ResponseSnapshot: &UsageResponseSnapshotView{
				UpstreamURL:  input.UpstreamURL,
				StatusCode:   input.UpstreamResponse.Status,
				Headers:      headerView(input.UpstreamResponse.Header),
				ErrorMessage: message,
			},
		})
	}
	input.AuditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:          "stream_failed",
		Success:          false,
		StatusCode:       input.Downstream.Res.StatusCode(),
		ResponseBody:     string(failureEvent),
		ResponsePartType: "gateway_response",
		ErrorPhase:       "downstream",
		ErrorCode:        "downstream_transport_conflict",
		ErrorMessage:     message,
	})
	return UpstreamResponseHandlingResult{AlreadyFinalized: true}, nil
}

// nonStreamImageResponseBodyOmission 对齐 nonStreamImageResponseBodyOmission。
func nonStreamImageResponseBodyOmission(bodyText string, capturedBody []byte, parsedJsonBody GatewayNonStreamJsonBody) *StreamBodyOmissionSummary {
	if bodyText == "" || !nonStreamBodyLooksLikeImageGenerationPayload(bodyText, parsedJsonBody) {
		return nil
	}
	bodyBytes := int64(len(bodyText))
	if capturedBody != nil {
		bodyBytes = int64(len(capturedBody))
	}
	return &StreamBodyOmissionSummary{
		Reason:              "image_json_payload",
		Message:             "图像 JSON 正文已省略，避免在日志和审计中保存图片字节",
		TotalUpstreamBytes:  bodyBytes,
		TotalResponseBytes:  bodyBytes,
		ImageOutputReceived: true,
	}
}

func nonStreamBodyLooksLikeImageGenerationPayload(bodyText string, parsedJsonBody GatewayNonStreamJsonBody) bool {
	if parsedJsonBody.Status == NonStreamJSONStatusValid {
		return jsonContainsImageGenerationResult(parsedJsonBody.Value)
	}
	return strings.Contains(bodyText, `"type":"image_generation_call"`) && strings.Contains(bodyText, `"result"`)
}

func jsonContainsImageGenerationResult(value any) bool {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if jsonContainsImageGenerationResult(item) {
				return true
			}
		}
		return false
	case map[string]any:
		if typed["type"] == "image_generation_call" {
			if result, isString := typed["result"].(string); isString && result != "" {
				return true
			}
		}
		for _, child := range typed {
			if jsonContainsImageGenerationResult(child) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// nonStreamJsonProtocolValidationAllowed 对齐 nonStreamJsonProtocolValidationAllowed。
func nonStreamJsonProtocolValidationAllowed(input HandleUpstreamResponseInput, endpointFamily gatewayproto.ResponseEndpointFamily) bool {
	if !input.UpstreamResponse.OK() {
		return false
	}
	requestPath := LowercasedRequestPath(input.Req.PathAndQuery())
	if isKnownBinaryGatewayDownloadPath(requestPath) {
		return false
	}
	// 图像生图响应（b64_json）体积随图片大小无上限增长，1MiB 校验窗口会把
	// 大图整单拒绝（AI 问答 generate_image 的唯一数据通道就是本响应，被拒即
	// 生图失败）。图像路径改走纯透传，错误体仍由 !OK() 分支缓冲诊断。
	if imagePathPattern.MatchString(normalizeV1PrefixPath(requestPath)) {
		return false
	}
	return endpointFamily != gatewayproto.EndpointFamilyUnknown || isKnownNonStreamJSONRequestPath(requestPath)
}

func isKnownNonStreamJSONRequestPath(requestPath string) bool {
	normalized := normalizeV1PrefixPath(requestPath)
	if normalized == "/models" || normalized == "/embeddings" || normalized == "/moderations" {
		return true
	}
	if imagePathPattern.MatchString(normalized) {
		return true
	}
	if audioPathPattern.MatchString(normalized) {
		return true
	}
	if batchesPattern.MatchString(normalized) || normalized == "/files" || fileItemPattern.MatchString(normalized) {
		return true
	}
	return false
}

var (
	imagePathPattern = regexp.MustCompile(`^/images/(?:generations|edits|variations)$`)
	batchesPattern   = regexp.MustCompile(`^/(?:batches|fine_tuning|vector_stores)(?:/|$)`)
	fileItemPattern  = regexp.MustCompile(`^/files/[^/]+$`)
)

// FinalizeHandledUpstreamResponse 对齐 finalizeHandledUpstreamResponse：
// usage 记录（G17）、审计收尾与上游协议失败的 502 渲染。
func FinalizeHandledUpstreamResponse(input HandleUpstreamResponseInput, result UpstreamResponseHandlingResult) {
	driver := input.driver()
	passthroughUpstreamFailure := result.PassthroughUpstreamFailure
	upstreamProtocolFailure := !passthroughUpstreamFailure && result.ErrorPayload.Code == "upstream_protocol_failure"
	responsesFailedTerminal := !passthroughUpstreamFailure &&
		input.UpstreamResponse.OK() &&
		driver.EndpointFamilyForPath(input.Req.PathAndQuery()) == gatewayproto.EndpointFamilyResponses &&
		(upstreamProtocolFailure || ResponsesFailureStatusFromCapturedJSON(result.ResponseBodyText))
	forwardedResponseSuccessful := input.UpstreamResponse.OK() && !passthroughUpstreamFailure && !responsesFailedTerminal && !upstreamProtocolFailure
	finalErrorCode := result.ErrorPayload.Code
	finalErrorMessage := result.ErrorPayload.Message
	if finalErrorCode == "" {
		switch {
		case passthroughUpstreamFailure:
			finalErrorCode = "cyber_policy"
			finalErrorMessage = "上游返回 Codex cyber_policy 失败终态"
		case responsesFailedTerminal:
			finalErrorCode = "upstream_protocol_failure"
			finalErrorMessage = "上游 Responses 返回失败终态"
		case upstreamProtocolFailure:
			finalErrorCode = "upstream_protocol_failure"
			finalErrorMessage = "上游响应违反请求协议终态"
		}
	}
	observedModel := input.UpstreamResponse.UpstreamResponseModel

	if input.Deps != nil && input.Deps.UsageRecords != nil {
		var requestSnapshot *UsageRequestSnapshotView
		if result.BodyOmission != nil {
			requestSnapshot = usageRequestSnapshotWithOmission(input.UsageContext, result.BodyOmission)
		} else if !forwardedResponseSuccessful {
			requestSnapshot = usageRequestSnapshotView(input.UsageContext)
		}
		var responseSnapshot *UsageResponseSnapshotView
		if result.BodyOmission != nil {
			responseSnapshot = &UsageResponseSnapshotView{
				UpstreamURL:  input.UpstreamURL,
				StatusCode:   input.UpstreamResponse.Status,
				Headers:      headerView(input.UpstreamResponse.Header),
				BodyOmission: result.BodyOmission,
			}
		} else if !forwardedResponseSuccessful {
			responseSnapshot = &UsageResponseSnapshotView{
				UpstreamURL: input.UpstreamURL,
				StatusCode:  input.UpstreamResponse.Status,
				Headers:     headerView(input.UpstreamResponse.Header),
				BodyText:    result.ResponseBodyText,
			}
		}
		input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
			UsageContext:                        input.UsageContext,
			Account:                             input.Account,
			RequestedModel:                      requestModelHint(input.Req),
			Stream:                              true,
			StatusCode:                          input.UpstreamResponse.Status,
			Success:                             forwardedResponseSuccessful,
			ProtocolValidatedSuccess:            forwardedResponseSuccessful && result.ProtocolValidatedSuccess,
			AccountAPIKeySuccessAlreadyRecorded: true,
			FirstTokenMs:                        result.FirstTokenMs,
			StartedAtMs:                         input.StartedAtMs,
			Usage:                               usageWithObservedModel(result.Usage, observedModel),
			ErrorCode:                           finalErrorCode,
			ErrorMessage:                        finalErrorMessage,
			FailureAttribution:                  failureAttributionFor(forwardedResponseSuccessful),
			RequestSnapshot:                     requestSnapshot,
			ResponseSnapshot:                    responseSnapshot,
		})
	}
	if result.BodyOmission != nil {
		input.AuditCapture.OmitPayloadBodies(OmitPayloadBodiesInput{
			Label:     "non_stream_body_omission",
			Metadata:  bodyOmissionMetadata(result.BodyOmission),
			PartTypes: []string{"upstream_response"},
		})
	}
	finalStatusCode := input.UpstreamResponse.Status
	finalResponseBody := result.ResponseBodyText
	if result.BodyOmission != nil {
		finalResponseBody = ""
	}
	if upstreamProtocolFailure && !input.Downstream.Res.HeadersSent() && !input.Downstream.WritableEnded() && !input.Downstream.DestroyedNow() {
		message := orDefault(finalErrorMessage, "上游响应违反请求协议终态")
		responsePayload := gatewaypreauth.GatewayErrorPayloadOf(message, "upstream_response_error", finalErrorCode)
		clientErrorProtocol := gatewaypreauth.GatewayErrorProtocol(driver.ClientErrorProtocol())
		clientPayload := gatewaypreauth.GatewayErrorPayloadForProtocol(responsePayload, clientErrorProtocol)
		sendGatewayErrorResponseForSink(input.Downstream.Res, 502, responsePayload, gatewaypreauth.SendGatewayErrorResponseOptions{
			Protocol: clientErrorProtocol,
		})
		finalStatusCode = 502
		finalResponseBody = marshalClientPayload(clientPayload)
	}
	input.AuditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:          auditOutcomeFor(forwardedResponseSuccessful),
		Success:          forwardedResponseSuccessful,
		StatusCode:       finalStatusCode,
		ResponseBody:     finalResponseBody,
		ResponsePartType: responsePartTypeFor(forwardedResponseSuccessful),
		ErrorPhase:       errorPhaseFor(forwardedResponseSuccessful),
		ErrorCode:        finalErrorCode,
		ErrorMessage:     finalErrorMessage,
	})
}

func failureAttributionFor(forwarded bool) string {
	if forwarded {
		return ""
	}
	return "opaque_upstream"
}

func auditOutcomeFor(forwarded bool) string {
	if forwarded {
		return gatewaypreauth.AuditOutcomeSuccess
	}
	return "upstream_failed"
}

func responsePartTypeFor(forwarded bool) string {
	if forwarded {
		return "gateway_response"
	}
	return "gateway_error"
}

// jsonBytesOf 序列化辅助（保留给响应快照）。
func jsonBytesOf(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

// isKnownBinaryGatewayDownloadPath 对齐 isKnownBinaryGatewayDownloadPath。
func isKnownBinaryGatewayDownloadPath(requestPath string) bool {
	normalized := normalizeV1PrefixPath(requestPath)
	return binaryDownloadPattern.MatchString(normalized)
}

var binaryDownloadPattern = regexp.MustCompile(`^/files/[^/]+/content(?:/|$)|^/vector_stores/[^/]+/files/[^/]+/content(?:/|$)`)
