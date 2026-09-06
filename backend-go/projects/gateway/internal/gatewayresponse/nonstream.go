package gatewayresponse

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

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
	CapturedBody         []byte
	CapturedBodyText     string
	DiagnosticBodyText   string
	UsageTailText        string
	FirstByteMs          *int64
	TransferredBytes     int64
	CaptureTruncated     bool
	FullyBuffered        bool
	InspectionLimitExceeded bool
}

// NonStreamPipeInput 对齐 pipeNonStreamUpstreamResponse 的入参子集。
type NonStreamPipeInput struct {
	Body            UpstreamBody
	Downstream      StreamDownstream
	StartedAtMs     int64
	CaptureBytes    int
	CaptureBody     bool
	// InspectBytes>0 启用有界整体缓冲（ForInspection 路径）。
	InspectBytes         int
	RequireFullyBuffered bool
	Signal               interface{ Done() <-chan struct{} }
	PrepareDownstream    func()
	OnChunkRead          func(chunk []byte)
	OnChunkWritten       func(bytesWritten int64)
	OnBodyCompleted      func(transferredBytes int64)
	OnFirstByte          func()
	NowMs                func() int64
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
	inspection := NewLimitedCapture(input.InspectBytes)
	var result NonStreamPipeResult
	var firstByteMs *int64
	transferred := int64(0)
	committedAny := false
	inspectionMode := input.InspectBytes > 0
	bufferOverflow := false

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

	for {
		if signalAbortedChannel(input.Signal) {
			return result, &UpstreamRequestAbortedError{Message: ErrUpstreamRequestAbortedMessage, UpstreamRequestStarted: true}
		}
		chunkResult, ok := <-input.Body.Next()
		if !ok {
			break
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
			inspection.Push(chunk)
			if inspection.IsTruncated() {
				// 超过检查窗口：冲刷缓冲并转透传（Node inspection window 溢出）。
				bufferOverflow = true
				result.InspectionLimitExceeded = true
				markFirstByte()
				if err := writeThrough(inspection.Buffer()); err != nil {
					return partialFailure(err)
				}
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
	if inspectionMode && !bufferOverflow && inspection.Buffer() != nil {
		// 完整缓冲：不写下游，由调用方检查后发送（res.send(completeBody)）。
		result.FullyBuffered = true
		result.CapturedBody = inspection.Buffer()
		result.CapturedBodyText = string(inspection.Buffer())
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
		pipeResult, pipeErr = PipeNonStreamUpstreamResponse(NonStreamPipeInput{
			Body:         input.UpstreamResponse.Body,
			Downstream:   input.Downstream,
			StartedAtMs:  input.StartedAtMs,
			CaptureBody:  !input.UpstreamResponse.OK() || input.AuditCapture.ShouldCaptureSuccessPayloads() || responseEndpointFamily == gatewayproto.EndpointFamilyResponses,
			InspectBytes: NonStreamResponseInspectionMaxBytes,
			Signal:       input.Signal,
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
		})
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

	// 2xx 协议校验：要求完整缓冲；未通过时按上游协议失败终态 502 收尾。
	if protocolValidationEnabled && pipeResult.FullyBuffered && !pipeResult.InspectionLimitExceeded {
		parsedForValidation := ParseGatewayNonStreamJsonBody(responseBodyText, len(responseBodyText) > 0, input.UpstreamResponse.Header)
		if failure := ValidateBufferedJsonProtocolResponse(parsedForValidation, true, false, string(responseEndpointFamily), LowercasedRequestPath(input.Req.PathAndQuery())); failure != nil {
			return input.finalizeBufferedJSONProtocolFailure(failure, parsedForValidation, pipeResult, responseBody, responseBodyText, driver)
		}
		// 检查通过：发送完整正文（Node res.send(downstreamBody)）。
		forwardInput := NonStreamPipeInput{
			Downstream:   input.Downstream,
			StartedAtMs:  input.StartedAtMs,
			Signal:       input.Signal,
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

// finalizeBufferedJsonProtocolFailure 对齐 finalizeBufferedJsonProtocolFailure：
// 完整但无效的 2xx 是本次尝试的确凿失败，按 502 返回协议诊断。
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
			StatusCode:      input.UpstreamResponse.Status,
			Success:         false,
			Stream:          false,
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
	input.AuditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:          "upstream_failed",
		Success:          false,
		StatusCode:       502,
		ResponseBody:     marshalClientPayload(clientPayload),
		ResponsePartType: "gateway_error",
		ErrorPhase:       "upstream_response",
		ErrorCode:        failure.ErrorCode,
		ErrorMessage:     failure.Message,
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
				UpstreamURL:  input.UpstreamURL,
				StatusCode:   input.UpstreamResponse.Status,
				Headers:      headerView(input.UpstreamResponse.Header),
				BodyText:     result.ResponseBodyText,
			}
		}
		input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
			UsageContext:              input.UsageContext,
			Account:                   input.Account,
			Stream:                    true,
			StatusCode:                input.UpstreamResponse.Status,
			Success:                   forwardedResponseSuccessful,
			ProtocolValidatedSuccess:  forwardedResponseSuccessful && result.ProtocolValidatedSuccess,
			AccountAPIKeySuccessAlreadyRecorded: true,
			FirstTokenMs:              result.FirstTokenMs,
			StartedAtMs:               input.StartedAtMs,
			Usage:                     usageWithObservedModel(result.Usage, observedModel),
			ErrorCode:                 finalErrorCode,
			ErrorMessage:              finalErrorMessage,
			FailureAttribution:        failureAttributionFor(forwardedResponseSuccessful),
			RequestSnapshot:           requestSnapshot,
			ResponseSnapshot:          responseSnapshot,
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
