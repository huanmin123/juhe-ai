package gatewayresponse

import (
	"net/http"
	"regexp"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 上游响应处理编排，对齐 finalization.ts。usage 记录与账号副作用经 ports
// 交接（G17 / G13 消费）；本包冻结触发点与字段形状。

// GatewayUpstreamResponse 对齐 GatewayUpstreamResponse 的消费子集。
type GatewayUpstreamResponse struct {
	Status int
	Header http.Header
	// Body 为 nil 表示空响应体。
	Body UpstreamBody
	// UpstreamResponseModelObservation 对齐 upstreamResponseModelObservation?.model。
	UpstreamResponseModel string
}

// OK 对齐 upstreamResponse.ok。
func (r *GatewayUpstreamResponse) OK() bool { return r.Status >= 200 && r.Status < 300 }

// ClientStrategyView 是 finalization 消费的 client strategy 投影（G18 冻结面）。
type ClientStrategyView struct {
	ClientProfile                     string
	DownstreamProtocol                string
	// InterpretSemantics 对齐 gatewayClientAllowsUpstreamSemanticInterpretation。
	InterpretSemantics                bool
	// RetryPreCommitProtocolError 对齐
	// retryCoordination.preCommitFailureSignal === 'protocol_error_event'。
	RetryPreCommitProtocolError       bool
	// AllowClientSourceAccountAvoidance 对齐 allowClientSourceAccountAvoidance。
	AllowClientSourceAccountAvoidance bool
	// CodexCompactionExpected 对齐 codexCompactionExpected。
	CodexCompactionExpected           bool
}

// FinalizationDeps 汇总编排端口；允许 nil（缺省短路，与 Node 的可选依赖一致）。
type FinalizationDeps struct {
	UsageRecords   UsageAttemptRecorder
	AccountEffects AccountFailureEffects
	UsageFallback  StreamUsageFallbackHook
	Logger         StreamLogger
	NowMs          func() int64
}

// StreamUsageFallbackHook 对齐 applyGatewayProtocolStreamUsageFallbackForRequest
//（由 G02-G04 的 driver fallback 装配；缺省恒等）。
type StreamUsageFallbackHook func(driver ResponseDriverPort, usage gatewayproto.ParsedUsage, input UsageFallbackInput) (gatewayproto.ParsedUsage, bool, *int, *int)

// UsageFallbackInput 对齐 fallback options。
type UsageFallbackInput struct {
	Completed             bool
	OutputReceived        bool
	EstimatedOutputTokens int
	HasEstimatedOutput    bool
}

// HandleUpstreamResponseInput 对齐 HandleUpstreamResponseInput。
type HandleUpstreamResponseInput struct {
	Req               *gatewaypreauth.GatewayRequest
	Downstream        StreamDownstream
	Account           AccountView
	UpstreamResponse  *GatewayUpstreamResponse
	UpstreamURL       string
	AuditAttemptID    string
	AuditCapture      AttemptAuditCapture
	Settings          gatewayruntimecache.GatewaySettings
	TimeoutProfile    TimeoutProfile
	UsageContext      gatewaypreauth.GatewayFailureUsageContext
	StartedAtMs       int64
	Signal            interface {
		Done() <-chan struct{}
		Err() error
	}
	FirstByteTimeoutMs            *int64
	FirstByteDeadlineMs           *int64
	ResponsePrecommitDeadlineAtMs *int64
	OnFirstByteDeadline           FirstByteDeadlineHandler
	OnFirstByteDeadlineSuperseded func()
	SessionAffinityKey            string
	ClientStrategy                *ClientStrategyView
	ResponseInspectionPolicies    []gatewayruntimecache.ResponseInspectionPolicySummary
	MarkFirstOutput               func()
	DownstreamCommitState         *DownstreamCommitState
	Driver                        ResponseDriverPort
	Deps                          *FinalizationDeps
}

func (input *HandleUpstreamResponseInput) nowMs() func() int64 {
	if input.Deps != nil && input.Deps.NowMs != nil {
		return input.Deps.NowMs
	}
	return defaultNowMs
}

func (input *HandleUpstreamResponseInput) logger() StreamLogger {
	if input.Deps != nil && input.Deps.Logger != nil {
		return input.Deps.Logger
	}
	return nopStreamLogger{}
}

func (input *HandleUpstreamResponseInput) driver() ResponseDriverPort {
	if input.Driver != nil {
		return input.Driver
	}
	return NewOpenAIResponseDriver()
}

// effectiveInspectionPolicies 对齐 runtimeResponseInspectionPoliciesForInput：
// interpretSemantics 时全量，否则只保留通用安全的 system_default 策略。
func (input *HandleUpstreamResponseInput) effectiveInspectionPolicies() []RuntimeResponseInspectionPolicy {
	accountRules := accountRulesFromView(input.Account)
	return ResolveRuntimeResponseInspectionPolicies(
		input.Account.GetProtocolCode(),
		input.Account.GetProviderCode(),
		accountRules,
		filterManagementPolicies(input.ResponseInspectionPolicies, input.ClientStrategy),
	)
}

func accountRulesFromView(account AccountView) []AccountResponseInspectionRule {
	// 账户凭据内嵌规则（credentials.response_inspection_rules）由 G13/G15 的
	// 账户装配提供；视图暂不携带，保持空。
	return nil
}

func filterManagementPolicies(policies []gatewayruntimecache.ResponseInspectionPolicySummary, strategy *ClientStrategyView) []gatewayruntimecache.ResponseInspectionPolicySummary {
	if strategy != nil && strategy.InterpretSemantics {
		return policies
	}
	genericSafe := map[string]bool{
		"default_openai_transient_precommit_error":    true,
		"default_anthropic_transient_precommit_error": true,
		"default_gemini_transient_precommit_error":    true,
	}
	filtered := make([]gatewayruntimecache.ResponseInspectionPolicySummary, 0, len(policies))
	for _, policy := range policies {
		if policy.DefaultRule && !genericSafe[policy.ID] {
			continue
		}
		filtered = append(filtered, policy)
	}
	return filtered
}

// emptyUpstreamProtocolFailure 对齐 emptyUpstreamProtocolFailure。
func emptyUpstreamProtocolFailure() gatewayproto.ErrorPayload {
	return gatewayproto.ErrorPayload{
		Code:    "upstream_protocol_failure",
		Message: "上游返回空响应，缺少请求协议要求的终态",
	}
}

// unexpectedEmptyUpstreamProtocolResponse 的 statusCode 判定 + DELETE interactions
// 白名单（isSuccessfulEmptyUpstreamResponseAllowed 的形状由调用方传入判定）。
func (input *HandleUpstreamResponseInput) successfulEmptyUpstreamAllowed() bool {
	req := input.Req
	if req == nil {
		return false
	}
	if req.MethodUpper() != "DELETE" {
		return false
	}
	normalized := normalizeV1BetaPrefixPath(LowercasedRequestPath(req.PathAndQuery()))
	if !interactionsResourcePattern.MatchString(normalized) {
		return false
	}
	return input.driver().EndpointFamilyForPath(req.PathAndQuery()) == gatewayproto.EndpointFamilyInteractions
}

// HandleStreamUpstreamResponse 对齐 handleStreamUpstreamResponse。
func HandleStreamUpstreamResponse(input HandleUpstreamResponseInput) (UpstreamResponseHandlingResult, error) {
	if input.UpstreamResponse.Body == nil {
		if input.UpstreamResponse.Status == 204 || input.UpstreamResponse.Status == 205 {
			if !input.successfulEmptyUpstreamAllowed() {
				errorPayload := emptyUpstreamProtocolFailure()
				input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
					StatusCode:      input.UpstreamResponse.Status,
					ResponseHeaders: input.UpstreamResponse.Header,
					ResponseBody:    []byte{},
					Success:         false,
					ErrorPhase:      "upstream_response",
					ErrorCode:       errorPayload.Code,
					ErrorMessage:    errorPayload.Message,
				})
				return UpstreamResponseHandlingResult{
					Usage:        usageWithObservedModel(gatewayproto.EmptyUsage(), input.UpstreamResponse.UpstreamResponseModel),
					FirstTokenMs: int64PtrOf(nowMsOf(&input)() - input.StartedAtMs),
					ErrorPayload: errorPayload,
				}, nil
			}
		}
		prepareUpstreamResponseForDownstream(input.Downstream, input.UpstreamResponse, true)
		input.DownstreamCommitState.MarkTransportCommitted(0)
		input.Downstream.End()
		input.DownstreamCommitState.MarkSemanticCommitted(0)
		return UpstreamResponseHandlingResult{
			Usage:        gatewayproto.EmptyUsage(),
			FirstTokenMs: int64PtrOf(nowMsOf(&input)() - input.StartedAtMs),
		}, nil
	}

	driver := input.driver()
	effectivePolicies := input.effectiveInspectionPolicies()
	clientStrategy := input.ClientStrategy
	logger := input.logger()

	shouldMutateAccountForStreamFailure := func(errorCode string, context StreamFailureContext) bool {
		if input.firstByteBudgeted() {
			if errorCode == FirstByteTimeoutErrorCode &&
				context.DownstreamBytesWritten == 0 && !context.OutputReceived {
				return false
			}
		}
		return true
	}

	var codexTurnFailureRemembered bool
	pipeResult, pipeErr := PipeUpstreamStream(PipeUpstreamStreamInput{
		UpstreamBody: input.UpstreamResponse.Body,
		Downstream:   input.Downstream,
		TimeoutProfile: input.TimeoutProfile,
		StartedAtMs:  input.StartedAtMs,
		HandleStreamFailure: func(message string, errorCode string, context StreamFailureContext) error {
			if input.Deps != nil && input.Deps.AccountEffects != nil {
				if err := input.Deps.AccountEffects.HandleStreamFailure(
					input.Account, message, errorCode, context,
					shouldMutateAccountForStreamFailure(errorCode, context),
				); err != nil {
					return err
				}
			}
			if context.AvailabilityProbeEligible && input.Deps != nil && input.Deps.AccountEffects != nil {
				input.Deps.AccountEffects.DispatchRequestFailureAccountHealthCheck(
					input.UsageContext.TrafficSource, input.Account.GetID())
			}
			return nil
		},
		Signal: input.Signal,
		Options: StreamPipeOptions{
			ClientRetryEnabled:                   clientStrategy != nil && clientStrategy.RetryPreCommitProtocolError,
			InterpretProtocolFailures:            clientStrategy == nil || clientStrategy.InterpretSemantics,
			InterpretProtocolFailuresSet:         true,
			RetryBeforeDownstreamWriteUntilOutput: true,
			OnFirstOutput:                        input.MarkFirstOutput,
			CaptureSuccessPayloads:               input.AuditCapture.ShouldCaptureSuccessPayloads(),
			CaptureSuccessPayloadsSet:            true,
			FirstByteTimeoutMs:                   timeoutsWithDisabled(input.TimeoutProfile, input.FirstByteTimeoutMs),
			FirstByteDeadlineMs:                  timeoutsWithDisabled(input.TimeoutProfile, input.FirstByteDeadlineMs),
			ResponsePrecommitDeadlineAtMs:        timeoutsWithDisabled(input.TimeoutProfile, input.ResponsePrecommitDeadlineAtMs),
			OnFirstByteDeadline:                  input.OnFirstByteDeadline,
			OnFirstByteDeadlineSuperseded:        input.OnFirstByteDeadlineSuperseded,
			ResponseInspectionPolicies:           effectivePolicies,
			ResponseInspectionContext: &ResponseInspectionRuntimeContext{
				ClientProfile:              input.clientProfile(),
				AccountClientCompatibility: input.Account.GetClientCompatibility(),
				CodexCompactionExpected:    clientStrategy != nil && clientStrategy.CodexCompactionExpected,
			},
			DownstreamProtocol: clientStrategyDownstreamProtocol(clientStrategy),
			ResponseProtocol:   driver.ResponseProtocol(),
			EndpointFamily:     driver.EndpointFamilyForPath(input.Req.PathAndQuery()),
			PrepareDownstream: func() {
				prepareUpstreamResponseForDownstream(input.Downstream, input.UpstreamResponse, true)
			},
			DownstreamCommitState: input.DownstreamCommitState,
			Driver:                driver.StreamDriver(),
			Logger:                logger,
			NowMs:                 input.nowMs(),
		},
	})
	if pipeErr != nil {
		if IsUpstreamRequestAbortedError(pipeErr) || signalAborted(input.Signal) {
			if input.Deps != nil && input.Deps.AccountEffects != nil {
				input.Deps.AccountEffects.ForgetSessionAffinity(input.SessionAffinityKey, input.Account.GetID())
			}
			if input.Deps != nil && input.Deps.UsageRecords != nil {
				input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
					UsageContext:    input.UsageContext,
					Account:         input.Account,
					StatusCode:      input.UpstreamResponse.Status,
					Success:         false,
					Stream:          true,
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

	// ---- usage fallback ----
	usage := pipeResult.Usage
	if input.Deps != nil && input.Deps.UsageFallback != nil {
		fallbackUsage, estimated, _, _ := input.Deps.UsageFallback(driver, pipeResult.Usage, UsageFallbackInput{
			Completed:             pipeResult.Completed,
			OutputReceived:        pipeResult.OutputReceived,
			EstimatedOutputTokens: pipeResult.EstimatedOutputTokens,
			HasEstimatedOutput:    pipeResult.EstimatedOutputTokens > 0,
		})
		usage = fallbackUsage
		if estimated {
			logger.Warn("gateway_stream_usage_estimated", map[string]any{
				"accountId":         input.Account.GetID(),
				"endpoint":          input.UsageContext.Endpoint,
				"completed":         pipeResult.Completed,
				"outputReceived":    pipeResult.OutputReceived,
				"estimatedOutputTokens": pipeResult.EstimatedOutputTokens,
			}, "上游流式响应缺少 usage，网关已按可见输出估算 token 成本")
		}
	}

	// ---- 检查观察副作用 + 审计元数据 ----
	if input.Deps != nil && input.Deps.AccountEffects != nil {
		for index := range pipeResult.ResponseInspectionObservations {
			observation := &pipeResult.ResponseInspectionObservations[index]
			if err := input.Deps.AccountEffects.ApplyInspectionPolicySideEffects(observation, input.Account, true); err != nil {
				logger.Warn("gateway_upstream_inspection_side_effect_failed", nil, "响应检查策略运行时副作用失败已隔离")
			}
		}
	}
	if pipeResult.ResponseInspection != nil {
		if input.Deps != nil && input.Deps.AccountEffects != nil {
			_ = input.Deps.AccountEffects.ApplyInspectionPolicySideEffects(pipeResult.ResponseInspection, input.Account, true)
		}
		input.AuditCapture.AddGatewayMetadata("response_inspection", inspectionAuditMetadata(pipeResult.ResponseInspection))
	}
	if pipeResult.BodyOmission != nil {
		partTypes := []string(nil)
		if !pipeResult.Completed {
			partTypes = []string{"upstream_response", "gateway_response", "gateway_error"}
		}
		input.AuditCapture.OmitPayloadBodies(OmitPayloadBodiesInput{
			Label:                      "stream_body_omission",
			Metadata:                   bodyOmissionMetadata(pipeResult.BodyOmission),
			PartTypes:                  partTypes,
			AlreadyOmittedPayloadCount: 2,
			AlreadyOmittedBodyBytes:    pipeResult.BodyOmission.TotalUpstreamBytes + pipeResult.BodyOmission.TotalResponseBytes,
		})
	}

	success := pipeResult.Completed && input.UpstreamResponse.OK() && !pipeResult.PassthroughUpstreamFailure
	errorPhase := ""
	errorCode := ""
	errorMessage := ""
	if !(pipeResult.Completed && !pipeResult.PassthroughUpstreamFailure) {
		errorPhase = "stream"
		errorCode = orDefault(pipeResult.ErrorCode, "cyber_policy")
		errorMessage = pipeResult.Message
	}
	input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
		StatusCode:      input.UpstreamResponse.Status,
		ResponseHeaders: input.UpstreamResponse.Header,
		ResponseBody:    pipeResult.AuditUpstreamBody,
		Success:         success,
		ErrorPhase:      errorPhase,
		ErrorCode:       errorCode,
		ErrorMessage:    errorMessage,
	})

	if !pipeResult.Completed {
		return input.finalizeStreamFailure(pipeResult, usage, driver, codexTurnFailureRemembered)
	}

	return UpstreamResponseHandlingResult{
		Usage:                      usage,
		FirstTokenMs:               pipeResult.FirstTokenMs,
		ResponseBodyText:           pipeResult.ResponseBodyText,
		ResponseResourceId:         pipeResult.ResponseResourceId,
		BodyOmission:               pipeResult.BodyOmission,
		ProtocolValidatedSuccess:   input.UpstreamResponse.OK() && pipeResult.ProtocolValidated && !pipeResult.PassthroughUpstreamFailure,
		PassthroughUpstreamFailure: pipeResult.PassthroughUpstreamFailure,
	}, nil
}

// finalizeStreamFailure 对齐 handleStreamUpstreamResponse 的 !completed 分支。
func (input *HandleUpstreamResponseInput) finalizeStreamFailure(pipeResult StreamPipeResult, usage gatewayproto.ParsedUsage, driver ResponseDriverPort, codexTurnFailureRemembered bool) (UpstreamResponseHandlingResult, error) {
	if input.Deps != nil && input.Deps.AccountEffects != nil {
		input.Deps.AccountEffects.ForgetSessionAffinity(input.SessionAffinityKey, input.Account.GetID())
	}
	errorCode := pipeResult.ErrorCode
	if errorCode == "" && pipeResult.SemanticCommitted {
		errorCode = "upstream_protocol_failure"
	}
	if input.Deps != nil && input.Deps.UsageRecords != nil {
		input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
			UsageContext:      input.UsageContext,
			Account:           input.Account,
			StatusCode:        input.UpstreamResponse.Status,
			Success:           false,
			Stream:            true,
			FirstTokenMs:      pipeResult.FirstTokenMs,
			StartedAtMs:       input.StartedAtMs,
			Usage:             usageWithObservedModel(usage, input.UpstreamResponse.UpstreamResponseModel),
			ErrorCode:         orDefault(errorCode, pipeResult.inspectionUpstreamErrorCode()),
			RequestSnapshot:   usageRequestSnapshotWithOmission(input.UsageContext, pipeResult.BodyOmission),
			ResponseSnapshot:  streamFailureResponseSnapshot(input, pipeResult),
			ErrorMessage:      pipeResult.Message,
		})
	}

	// ---- 服务端重试判定（response inspection / pre-commit failure）----
	responseState := PreCommitResponseState{
		HeadersSent:   input.Downstream.Res.HeadersSent(),
		WritableEnded: input.Downstream.WritableEnded(),
		Destroyed:     input.Downstream.DestroyedNow(),
	}
	preCommitProtocolError := input.ClientStrategy != nil && input.ClientStrategy.RetryPreCommitProtocolError
	if !pipeResult.SemanticCommitted && pipeResult.ResponseInspection != nil &&
		ShouldRetryResponseInspectionDecisionOnServer(pipeResult.ResponseInspection, responseState) {
		clientFacingErrorCode := PreCommitStreamServerRetryErrorCode(pipeResult, preCommitProtocolError)
		input.AuditCapture.AddGatewayMetadata("response_inspection_server_retry", map[string]any{
			"policyId":             pipeResult.ResponseInspection.PolicyID,
			"accountSwitch":        pipeResult.ResponseInspection.AccountSwitch,
			"errorCode":            orDefault(pipeResult.ResponseInspection.UpstreamErrorCode, pipeResult.ErrorCode),
			"clientFacingErrorCode": clientFacingErrorCode,
			"accountId":            input.Account.GetID(),
		})
		return UpstreamResponseHandlingResult{
			RetryUpstream:            true,
			RetryReason:              StreamServerRetryResponseInspection,
			SameAccountRetryEligible: IsTransientPrecommitUpstreamFailureDecision(pipeResult.ResponseInspection),
			ResponseInspection:       pipeResult.ResponseInspection,
			ExcludeCurrentAccount:    ShouldExcludeCurrentAccountForStreamServerRetry(pipeResult.ResponseInspection),
			Message:                  pipeResult.Message,
			ErrorCode:                clientFacingErrorCode,
			UncommittedResponseBody:  pipeResult.UncommittedResponseBody,
			TransportFailure:         pipeResult.TransportFailure,
		}, nil
	}
	if !pipeResult.SemanticCommitted && pipeResult.ResponseInspection == nil &&
		ShouldRetryPreCommitStreamFailureOnServer(pipeResult, responseState) {
		clientFacingErrorCode := PreCommitStreamServerRetryErrorCode(pipeResult, preCommitProtocolError)
		input.AuditCapture.AddGatewayMetadata("pre_commit_stream_server_retry", map[string]any{
			"errorCode":            pipeResult.ErrorCode,
			"clientFacingErrorCode": clientFacingErrorCode,
			"message":              pipeResult.Message,
			"downstreamBytesWritten": pipeResult.DownstreamBytesWritten,
			"outputReceived":       pipeResult.OutputReceived,
			"accountId":            input.Account.GetID(),
		})
		return UpstreamResponseHandlingResult{
			RetryUpstream:            true,
			RetryReason:              StreamServerRetryPreCommitStreamFailure,
			SameAccountRetryEligible: pipeResult.TransportFailure != nil,
			ResponseInspection:       pipeResult.ResponseInspection,
			ExcludeCurrentAccount:    true,
			Message:                  pipeResult.Message,
			ErrorCode:                clientFacingErrorCode,
			UncommittedResponseBody:  pipeResult.UncommittedResponseBody,
			TransportFailure:         pipeResult.TransportFailure,
		}, nil
	}

	// ---- 客户端失败终态补写 ----
	clientFailureResponseBody := writePreCommitStreamFailureToClient(input, pipeResult, driver)
	input.AuditCapture.Finalize(gatewaypreauth.AuditFinalizeInput{
		Outcome:          "stream_failed",
		Success:          false,
		StatusCode:       input.UpstreamResponse.Status,
		ResponseBody:     stringOrBytes(clientFailureResponseBody, pipeResult.AuditResponseBody),
		ResponsePartType: "gateway_response",
		ErrorPhase:       "stream",
		ErrorCode:        orDefault(pipeResult.inspectionUpstreamErrorCode(), pipeResult.ErrorCode),
		ErrorMessage:     pipeResult.Message,
	})
	return UpstreamResponseHandlingResult{
		AlreadyFinalized:    true,
		ErrorCode:           pipeResult.ErrorCode,
		TransportFailure:    pipeResult.TransportFailure,
		GatewayLocalFailure: pipeResult.GatewayLocalFailure,
	}, nil
}

// writePreCommitStreamFailureToClient 对齐 writePreCommitStreamFailureToClient。
func writePreCommitStreamFailureToClient(input *HandleUpstreamResponseInput, pipeResult StreamPipeResult, driver ResponseDriverPort) []byte {
	if pipeResult.SemanticCommitted ||
		input.Downstream.WritableEnded() ||
		input.Downstream.DestroyedNow() ||
		pipeResult.ErrorCode == "" {
		return nil
	}
	if !input.Downstream.Res.HeadersSent() {
		prepareUpstreamResponseForDownstream(input.Downstream, input.UpstreamResponse, true)
	}
	downstreamProtocol := clientStrategyDownstreamProtocol(input.ClientStrategy)
	failureEvent := gatewaypreauth.BuildGatewayStreamFailureEventForProtocol(
		pipeResult.Message, pipeResult.ErrorCode,
		gatewaypreauth.GatewayErrorProtocol(driver.ClientErrorProtocol()),
		gatewaypreauth.OpenAIGatewayDownstreamProtocol(downstreamProtocol),
	)
	chunks := make([][]byte, 0, 2)
	if len(pipeResult.UncommittedResponseBody) > 0 {
		chunks = append(chunks, pipeResult.UncommittedResponseBody)
	}
	if len(failureEvent) > 0 {
		chunks = append(chunks, failureEvent)
	}
	for _, chunk := range chunks {
		_, _ = input.Downstream.Res.Write(chunk)
	}
	FlushGateway(input.Downstream.Res)
	input.Downstream.End()
	if len(chunks) == 0 {
		return nil
	}
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	out := make([]byte, 0, total)
	for _, chunk := range chunks {
		out = append(out, chunk...)
	}
	return out
}

// ---- 共享辅助 ----

func (input *HandleUpstreamResponseInput) clientProfile() string {
	if input.ClientStrategy != nil && input.ClientStrategy.ClientProfile != "" {
		return input.ClientStrategy.ClientProfile
	}
	return input.driver().DefaultClientProfile()
}

func (input *HandleUpstreamResponseInput) firstByteBudgeted() bool {
	if input.TimeoutProfile.TimeoutsDisabled {
		return false
	}
	return input.FirstByteTimeoutMs != nil || input.FirstByteDeadlineMs != nil
}

func clientStrategyDownstreamProtocol(strategy *ClientStrategyView) string {
	if strategy == nil {
		return ""
	}
	return strategy.DownstreamProtocol
}

func timeoutsWithDisabled(profile TimeoutProfile, value *int64) *int64 {
	if profile.TimeoutsDisabled {
		return nil
	}
	return value
}

func signalAborted(signal interface{ Done() <-chan struct{} }) bool {
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

func prepareUpstreamResponseForDownstream(downstream StreamDownstream, upstreamResponse *GatewayUpstreamResponse, shouldHandleAsStream bool) {
	// 对齐 downstream-headers.ts 的 prepareUpstreamResponseForDownstream：headers
	// 已发送时跳过；流式补齐 content-type 与 no-cache 头。
	if downstream.Res.HeadersSent() {
		return
	}
	if !upstreamResponse.OK() {
		kernelMarkUpstreamForMetrics(downstream)
	}
	header := downstream.Res.Header()
	status := upstreamResponse.Status
	if status != 0 {
		downstream.Res.WriteHeader(status)
	}
	if shouldHandleAsStream && header.Get("content-type") == "" {
		header.Set("content-type", "text/event-stream; charset=utf-8")
	}
	if shouldHandleAsStream {
		if header.Get("cache-control") == "" {
			header.Set("cache-control", "no-cache, no-transform")
		}
		header.Set("x-accel-buffering", "no")
		FlushGateway(downstream.Res)
	}
}

// kernelMarkUpstreamForMetrics 预留：上游失败的 http metric scope 由 kernel
// 指标中间件（G12/G15）承接。
func kernelMarkUpstreamForMetrics(downstream StreamDownstream) {}

func usageWithObservedModel(usage gatewayproto.ParsedUsage, observedModel string) gatewayproto.ParsedUsage {
	if observedModel != "" {
		usage.UpstreamResponseModel = observedModel
	}
	return usage
}

func int64PtrOf(value int64) *int64 { return &value }

func (r StreamPipeResult) inspectionUpstreamErrorCode() string {
	if r.ResponseInspection != nil {
		return r.ResponseInspection.UpstreamErrorCode
	}
	return ""
}

func headerView(header http.Header) map[string]string {
	if header == nil {
		return nil
	}
	out := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			out[key] = values[0]
		}
	}
	return out
}

func usageRequestSnapshotView(context gatewaypreauth.GatewayFailureUsageContext) *UsageRequestSnapshotView {
	return &UsageRequestSnapshotView{
		Method:                   context.RequestSnapshot.Method,
		Path:                     context.RequestSnapshot.Path,
		OriginalURL:              context.RequestSnapshot.OriginalURL,
		ClientIP:                 context.RequestSnapshot.ClientIP,
		TraceID:                  context.RequestSnapshot.TraceID,
		RequestedServiceTier:     context.RequestSnapshot.RequestedServiceTier,
		RequestedReasoningEffort: context.RequestSnapshot.RequestedReasoningEffort,
	}
}

func usageRequestSnapshotWithOmission(context gatewaypreauth.GatewayFailureUsageContext, omission *StreamBodyOmissionSummary) *UsageRequestSnapshotView {
	view := usageRequestSnapshotView(context)
	if omission != nil {
		view.OmittedBody = true
		view.BodyOmission = omission
	}
	return view
}

func streamFailureResponseSnapshot(input *HandleUpstreamResponseInput, pipeResult StreamPipeResult) *UsageResponseSnapshotView {
	return &UsageResponseSnapshotView{
		UpstreamURL:  input.UpstreamURL,
		StatusCode:   input.UpstreamResponse.Status,
		Headers:      headerView(input.UpstreamResponse.Header),
		BodyText:     bodyTextForSnapshot(pipeResult),
		BodyOmission: pipeResult.BodyOmission,
		ErrorMessage: pipeResult.Message,
	}
}

func bodyTextForSnapshot(pipeResult StreamPipeResult) string {
	if pipeResult.BodyOmission != nil {
		return ""
	}
	return pipeResult.ResponseBodyText
}

func bodyOmissionMetadata(omission *StreamBodyOmissionSummary) map[string]any {
	metadata := map[string]any{
		"omitted":            true,
		"reason":             omission.Reason,
		"message":            omission.Message,
		"totalUpstreamBytes": omission.TotalUpstreamBytes,
		"totalResponseBytes": omission.TotalResponseBytes,
		"imageOutputReceived": omission.ImageOutputReceived,
	}
	if omission.SseEventCount > 0 {
		metadata["sseEventCount"] = omission.SseEventCount
	}
	if omission.LastSseEventType != "" {
		metadata["lastSseEventType"] = omission.LastSseEventType
	}
	if len(omission.RecentSseEventTypes) > 0 {
		metadata["recentSseEventTypes"] = omission.RecentSseEventTypes
	}
	return metadata
}

// inspectionAuditMetadata 对齐 responseInspectionAuditMetadata 的消费子集。
func inspectionAuditMetadata(decision *ResponseInspectionDecision) map[string]any {
	metadata := map[string]any{
		"reason":           decision.Reason,
		"action":           decision.Action,
		"transport":        decision.Transport,
		"triggerPhase":     decision.TriggerPhase,
		"endpointFamily":   string(decision.EndpointFamily),
		"frameType":        decision.FrameType,
		"downstreamWritten": decision.DownstreamWritten,
	}
	if decision.PolicyID != "" {
		metadata["policyId"] = decision.PolicyID
	}
	if decision.PolicyName != "" {
		metadata["policyName"] = decision.PolicyName
	}
	if decision.PolicySource != "" {
		metadata["policySource"] = decision.PolicySource
	}
	if decision.UpstreamErrorCode != "" {
		metadata["upstreamErrorCode"] = decision.UpstreamErrorCode
	}
	if decision.RewriteErrorCode != "" {
		metadata["rewriteErrorCode"] = decision.RewriteErrorCode
	}
	if decision.AccountSwitch != "" {
		metadata["accountSwitch"] = decision.AccountSwitch
	}
	if decision.MatchedField != "" {
		metadata["matchedField"] = decision.MatchedField
	}
	if decision.MatchedValue != "" {
		metadata["matchedValue"] = decision.MatchedValue
	}
	if decision.MatchedSnippet != "" {
		metadata["matchedSnippet"] = decision.MatchedSnippet
	}
	return metadata
}

func stringOrBytes(primary []byte, fallback []byte) string {
	if len(primary) > 0 {
		return string(primary)
	}
	if len(fallback) > 0 {
		return string(fallback)
	}
	return ""
}

var interactionsResourcePattern = regexp.MustCompile(`^/interactions/[^/]+$`)

// nowMsOf 返回注入时钟（nil 回退真实时间）。
func nowMsOf(input *HandleUpstreamResponseInput) func() int64 {
	if input.Deps != nil && input.Deps.NowMs != nil {
		return input.Deps.NowMs
	}
	return defaultNowMs
}
