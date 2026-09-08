package gatewayresponse

import (
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// 非流式 JSON 响应检查主链，对齐 non-stream-json-inspection.ts 的
// inspectBufferedGatewayJsonResponse：完整缓冲 JSON 在发送前先跑响应检查
// 策略（D-112 语义帧提取端口此前零生产调用），命中时按决策改写失败或服务端
// 换号重试；未命中回退协议校验与原样转发。

// InspectBufferedGatewayJSONArgs 对齐 inspectBufferedGatewayJsonResponse 的
// 响应事实入参（请求/账户/策略面由 HandleUpstreamResponseInput 承载）。
type InspectBufferedGatewayJSONArgs struct {
	// ResponseBody / ResponseBodyText 是完整缓冲正文（Node completeBody /
	// completeBodyText）。
	ResponseBody     []byte
	ResponseBodyText string
	// ParsedJSONBody 允许调用方传入已解析结果；Status 为空时按正文解析。
	ParsedJSONBody GatewayNonStreamJsonBody
	FirstTokenMs   *int64
	// ProtocolValidationEnabled 对齐 protocolValidationEnabled。
	ProtocolValidationEnabled bool
	// ProtocolValidationLimitExceeded 对齐 protocolValidationLimitExceeded。
	ProtocolValidationLimitExceeded bool
}

// inspectBufferedGatewayJSONResponse 对齐 inspectBufferedGatewayJsonResponse。
// 返回 nil 表示未接管（调用方继续协议校验与原样转发缓冲正文）。
func (input *HandleUpstreamResponseInput) inspectBufferedGatewayJSONResponse(args InspectBufferedGatewayJSONArgs) *UpstreamResponseHandlingResult {
	parsedJSONBody := args.ParsedJSONBody
	if parsedJSONBody.Status == "" {
		parsedJSONBody = ParseGatewayNonStreamJsonBody(args.ResponseBodyText, len(args.ResponseBodyText) > 0, input.UpstreamResponse.Header)
	}
	driver := input.driver()
	endpointFamily := string(driver.EndpointFamilyForPath(input.Req.PathAndQuery()))
	requestPath := LowercasedRequestPath(input.Req.PathAndQuery())
	protocolFailureResult := func() *UpstreamResponseHandlingResult {
		if !args.ProtocolValidationEnabled {
			return nil
		}
		failure := ValidateBufferedJsonProtocolResponse(parsedJSONBody, true, args.ProtocolValidationLimitExceeded, endpointFamily, requestPath)
		if failure == nil {
			return nil
		}
		result, err := input.finalizeBufferedJSONProtocolFailure(failure, parsedJSONBody, NonStreamPipeResult{
			FirstByteMs: args.FirstTokenMs,
		}, args.ResponseBody, args.ResponseBodyText, driver)
		if err != nil {
			return nil
		}
		return &result
	}

	if parsedJSONBody.Status == NonStreamJSONStatusValid &&
		IsCodexResponsesCyberPolicyFailedJSON(input.UpstreamResponse.Status, endpointFamily, input.clientProfile(), parsedJSONBody.Value) {
		return nil
	}
	if parsedJSONBody.Status != NonStreamJSONStatusValid {
		// Node: DELETE interactions 空成功体放行；其余无效体回退协议校验。
		if parsedJSONBody.Status == NonStreamJSONStatusEmpty && input.UpstreamResponse.OK() && input.successfulEmptyUpstreamAllowed() {
			return nil
		}
		return protocolFailureResult()
	}
	if IsGatewayGeneratedResponsesFailure(parsedJSONBody.Value, endpointFamily) {
		return nil
	}
	interpretUpstreamResponseSemantics := input.ClientStrategy != nil && input.ClientStrategy.InterpretSemantics
	policyCount := len(input.ResponseInspectionPolicies)
	if !interpretUpstreamResponseSemantics && policyCount == 0 {
		return protocolFailureResult()
	}
	context := &ResponseInspectionRuntimeContext{
		ClientProfile:              input.clientProfile(),
		AccountClientCompatibility: input.Account.GetClientCompatibility(),
		CodexCompactionExpected:    input.ClientStrategy != nil && input.ClientStrategy.CodexCompactionExpected,
	}
	if context.ClientProfile == "generic_anthropic" && policyCount == 0 {
		return protocolFailureResult()
	}
	frames := driver.ExtractJSONSemanticFrames(parsedJSONBody.Value, input.Req.PathAndQuery())
	if context.CodexCompactionExpected && context.ClientProfile == "codex" && context.AccountClientCompatibility == "codex_responses" {
		counts := CountCodexCompactionOutputItemsFromJSON(parsedJSONBody.Value)
		var contractCounts CodexCompactionContractCounts
		if counts != nil {
			contractCounts = *counts
		}
		if mismatchFrame := CodexCompactionContractMismatchFrame(CodexCompactionContractMismatchInput{
			OutputItemCount:     contractCounts.OutputItemCount,
			CompactionItemCount: contractCounts.CompactionItemCount,
			Transport:           "json",
		}); mismatchFrame != nil {
			frames = append(frames, *mismatchFrame)
		}
	}
	if len(frames) == 0 {
		return protocolFailureResult()
	}
	policies := ResolveRuntimeResponseInspectionPolicies(
		input.Account.GetProtocolCode(),
		input.Account.GetProviderCode(),
		accountRulesFromView(input.Account),
		input.ResponseInspectionPolicies,
	)
	inspection := InspectResponseSemanticFrames(frames, policies, false, "json", context)
	input.applyInspectionObservations(inspection.Observations)
	if inspection.Decision == nil {
		return protocolFailureResult()
	}
	decision := inspection.Decision
	input.applyInspectionDecisionSideEffects(decision)
	input.AuditCapture.AddGatewayMetadata("response_inspection", inspectionAuditMetadata(decision))
	usage := driver.ExtractUsageFromJSONValue(parsedJSONBody.Value)
	message := orDefault(decision.UpstreamErrorMessage, orDefault(decision.RewriteMessage, "JSON 响应命中检查策略："+orDefault(decision.PolicyName, orDefault(decision.PolicyID, "未命名策略"))))
	errorCode := orDefault(decision.RewriteErrorCode, orDefault(decision.UpstreamErrorCode, "response_inspection_matched"))
	input.forgetSessionAffinityForFailure()

	input.AuditCapture.CompleteAttempt(input.AuditAttemptID, AttemptAuditInput{
		StatusCode:      input.UpstreamResponse.Status,
		ResponseHeaders: input.UpstreamResponse.Header,
		ResponseBody:    args.ResponseBody,
		Success:         false,
		ErrorPhase:      "response_inspection",
		ErrorCode:       errorCode,
		ErrorMessage:    message,
	})
	if input.Deps != nil && input.Deps.UsageRecords != nil {
		input.Deps.UsageRecords.RecordCompletedUpstreamAttempt(CompletedAttemptInput{
			UsageContext:    input.UsageContext,
			Account:         input.Account,
			StatusCode:      input.UpstreamResponse.Status,
			Success:         false,
			Stream:          gatewaypreauth.IsOpenAIStreamRequest(input.Req),
			FirstTokenMs:    args.FirstTokenMs,
			StartedAtMs:     input.StartedAtMs,
			Usage:           usageWithObservedModel(usage, input.UpstreamResponse.UpstreamResponseModel),
			ErrorCode:       errorCode,
			RequestSnapshot: usageRequestSnapshotView(input.UsageContext),
			ResponseSnapshot: &UsageResponseSnapshotView{
				UpstreamURL:  input.UpstreamURL,
				StatusCode:   input.UpstreamResponse.Status,
				Headers:      headerView(input.UpstreamResponse.Header),
				BodyText:     args.ResponseBodyText,
				ErrorMessage: message,
			},
			ErrorMessage: message,
		})
	}

	responseState := PreCommitResponseState{
		HeadersSent:   input.Downstream.Res.HeadersSent(),
		WritableEnded: input.Downstream.WritableEnded(),
		Destroyed:     input.Downstream.DestroyedNow(),
	}
	if !input.DownstreamCommitState.SemanticCommitted &&
		input.DownstreamCommitState.DownstreamBytesWritten == 0 &&
		ShouldRetryResponseInspectionDecisionOnServer(decision, responseState) {
		return &UpstreamResponseHandlingResult{
			RetryUpstream:            true,
			RetryReason:              StreamServerRetryResponseInspection,
			SameAccountRetryEligible: IsTransientPrecommitUpstreamFailureDecision(decision),
			ResponseInspection:       decision,
			ExcludeCurrentAccount:    ShouldExcludeCurrentAccountForStreamServerRetry(decision),
			Message:                  message,
			ErrorCode:                errorCode,
		}
	}

	markHTTPMetricFailureScope("upstream")
	responsePayload := gatewaypreauth.GatewayErrorPayloadOf(message, "response_inspection_failed", errorCode)
	clientErrorProtocol := gatewaypreauth.GatewayErrorProtocol(driver.ClientErrorProtocol())
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
		ErrorPhase:       "response_inspection",
		ErrorCode:        errorCode,
		ErrorMessage:     message,
	}
	input.finalizeAuditWithExtras(finalizeInput, AuditFinalizeExtras{
		AccountID:    input.Account.GetID(),
		FirstTokenMs: args.FirstTokenMs,
	})
	return &UpstreamResponseHandlingResult{AlreadyFinalized: true, ErrorCode: errorCode}
}

// applyInspectionObservations 对齐 applyResponseInspectionObservationDecisions：
// 观察级决策逐一应用运行时副作用，并写入观察审计元数据。
func (input *HandleUpstreamResponseInput) applyInspectionObservations(observations []ResponseInspectionDecision) {
	if len(observations) == 0 {
		return
	}
	for index := range observations {
		input.applyInspectionDecisionSideEffects(&observations[index])
	}
	metadata := make([]map[string]any, 0, len(observations))
	for index := range observations {
		metadata = append(metadata, inspectionAuditMetadata(&observations[index]))
	}
	input.AuditCapture.AddGatewayMetadata("response_inspection_observations", map[string]any{
		"count":        len(observations),
		"omittedCount": nil,
		"observations": metadata,
	})
}

// applyInspectionDecisionSideEffects 对齐
// applyResponseInspectionPolicyRuntimeSideEffects 的委托面（G13 装配）。
func (input *HandleUpstreamResponseInput) applyInspectionDecisionSideEffects(decision *ResponseInspectionDecision) {
	if input.Deps == nil || input.Deps.AccountEffects == nil {
		return
	}
	if err := input.Deps.AccountEffects.ApplyInspectionPolicySideEffects(decision, input.Account, true); err != nil {
		input.logger().Warn("gateway_upstream_inspection_side_effect_failed", nil, "响应检查策略运行时副作用失败已隔离")
	}
}

// forgetSessionAffinityForFailure 对齐
// forgetOpenAIAccountForSessionAsync(sessionAffinityKey, account.id)。
func (input *HandleUpstreamResponseInput) forgetSessionAffinityForFailure() {
	if input.Deps != nil && input.Deps.AccountEffects != nil {
		input.Deps.AccountEffects.ForgetSessionAffinity(input.SessionAffinityKey, input.Account.GetID())
	}
}

// finalizeAuditWithExtras 优先走扩展审计面（携带 accountId 与 firstTokenMs，
// 对齐 Node finalize 的附加字段）；未实现时回退冻结 Finalize。
func (input *HandleUpstreamResponseInput) finalizeAuditWithExtras(finalizeInput gatewaypreauth.AuditFinalizeInput, extras AuditFinalizeExtras) {
	if extender, ok := input.AuditCapture.(AuditFinalizeExtender); ok {
		extender.FinalizeExtended(finalizeInput, extras)
		return
	}
	input.AuditCapture.Finalize(finalizeInput)
}

// AuditFinalizeExtras 承载 Node finalize 输入里 G05 冻结面未覆盖的附加字段
// （accountId / firstTokenMs）。
type AuditFinalizeExtras struct {
	AccountID    string
	FirstTokenMs *int64
}

// AuditFinalizeExtender 是 G17 审计捕获的可选扩展面：与 AttemptAuditInput 的
// 扩展字段对应，由 chain 审计适配器实现；缺省回退冻结 Finalize。
type AuditFinalizeExtender interface {
	FinalizeExtended(input gatewaypreauth.AuditFinalizeInput, extras AuditFinalizeExtras)
}
