package main

// G17 审计捕获桥：把 gatewayusage.AuditCaptureContext 适配进 frozen 的
// gatewaypreauth.AuditCaptureContext 与 gatewayresponse.AttemptAuditCapture
// 面（请求捕获构造、引擎/响应层适配器与类型投影）。自 chain_v1.go 按职责
// 拆出（REFACTOR-0007 文件内拆分）；被移动的函数体逐字节保持。

import (
	"log/slog"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// newAuditCapture builds the G17 audit capture context for one request.
func (c *gatewayChain) newAuditCapture(
	req *gatewaypreauth.GatewayRequest,
	traceID string,
	startedAt int64,
) gatewaypreauth.AuditCaptureContext {
	model, _ := gatewaypreauth.RequestModel(req)
	headers := map[string]any{}
	for key, values := range req.HTTP.Header {
		if len(values) > 0 {
			headers[strings.ToLower(key)] = values[0]
		}
	}
	concrete := gatewayusage.NewAuditCaptureContext(gatewayusage.AuditCaptureInput{
		TraceID:        traceID,
		ClientIP:       req.ClientIP,
		StartedAtMs:    startedAt,
		TrafficSource:  gatewayTrafficSource,
		Method:         req.MethodUpper(),
		Path:           req.Path(),
		OriginalURL:    req.PathAndQuery(),
		UserAgent:      req.Header("user-agent"),
		Model:          model,
		Stream:         gatewaypreauth.RequestStream(req),
		RawBody:        rawBodySnapshotOf(req),
		RequestHeaders: headers,
		Settings:       c.auditSettings,
		Dispatcher:     c.auditDispatcher,
		Models:         c.usageModelResolver,
		Logger:         slogLogger{inner: slog.Default()},
		StageLogger:    chainAuditStageLogger{},
	})
	return preauthAuditCapture{inner: concrete}
}

// chainAuditStageLogger 把 gatewayusage.AuditStageLogger（三参
// logRequestStage 契约）接进 package main 既有共享发射面
// chainEmitGatewayRequestStage：audit.finalize 与其他 gateway.request.stage
// 一样先入 kernel 请求累积器，再按 gatewayRequestStageLogLevel 策略写出
// 同形独立日志行，不旁路观测管线。阶段起点取发射时刻（finalize 的
// fields 已自带 traceId）。
type chainAuditStageLogger struct{}

func (chainAuditStageLogger) LogRequestStage(stage string, fields map[string]any, outcome string) {
	chainEmitGatewayRequestStage(slog.Default(), stage, fields, outcome, time.Now())
}

// engineAuditCapture adapts the frozen capture context into the dispatch
// capture: the G17 capture implements the attempt-level surface, so the
// engine factory sink stays unused.
func (c *gatewayChain) engineAuditCapture(capture gatewaypreauth.AuditCaptureContext) gatewaydispatch.AuditCapture {
	concrete := auditCaptureConcrete(capture)
	if concrete != nil {
		return gatewaydispatch.AuditCapture{
			Context: capture,
			Sink:    chainAttemptAuditSink{capture: concrete},
		}
	}
	return gatewaydispatch.AuditCapture{Context: capture}
}

// auditCaptureConcrete narrows the frozen context to the concrete G17
// capture when the request built one.
func auditCaptureConcrete(capture gatewaypreauth.AuditCaptureContext) *gatewayusage.AuditCaptureContext {
	if adapted, ok := capture.(preauthAuditCapture); ok {
		return adapted.inner
	}
	return nil
}

// responseAuditCaptureOf adapts the concrete capture into the
// gatewayresponse.AttemptAuditCapture surface (CompleteAttempt input unions
// + FinalizeLazy + OmitPayloadBodies delegation).
func responseAuditCaptureOf(capture gatewaypreauth.AuditCaptureContext) gatewayresponse.AttemptAuditCapture {
	if concrete := auditCaptureConcrete(capture); concrete != nil {
		return chainResponseAuditCapture{capture: concrete}
	}
	return nil
}

// preauthAuditCapture bridges the gatewayusage capture into the frozen
// gatewaypreauth.AuditCaptureContext (the union types differ).
type preauthAuditCapture struct {
	inner *gatewayusage.AuditCaptureContext
}

func (c preauthAuditCapture) BindContext(context gatewaypreauth.AuditGatewayContext) {
	c.inner.BindContext(gatewayusage.AuditGatewayContext{
		SystemAccountID:   context.SystemAccountID,
		APIKeyID:          context.APIKeyID,
		GroupID:           context.GroupID,
		ProviderCode:      context.ProviderCode,
		TrafficSource:     context.TrafficSource,
		SessionID:         context.SessionID,
		SessionClientType: context.SessionClientType,
		ConversationKey:   context.ConversationKey,
	})
}

func (c preauthAuditCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	c.inner.AddGatewayMetadata(label, metadata)
}

// Cancel exposes the capture lifecycle (gatewaypreauth.AuditCaptureCanceller):
// the chain cancels an un-finalized capture on the preflight failure /
// rejection paths and at request end (routes.ts:515/527/2645).
func (c preauthAuditCapture) Cancel() { c.inner.Cancel() }

func (c preauthAuditCapture) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	converted := gatewayusage.FinalizeAuditInput{
		Success:      input.Success,
		ErrorPhase:   input.ErrorPhase,
		ErrorCode:    input.ErrorCode,
		ErrorMessage: input.ErrorMessage,
	}
	if input.Outcome != "" {
		converted.Outcome = gatewayusage.AuditOutcome(input.Outcome)
	}
	status := input.StatusCode
	converted.StatusCode = &status
	if input.ResponseHeaders != nil {
		converted.ResponseHeaders = input.ResponseHeaders
	}
	if input.ResponseBody != "" {
		converted.ResponseBody = []byte(input.ResponseBody)
		converted.HasResponseBody = true
	}
	if input.ResponsePartType != "" {
		converted.ResponsePartType = gatewayusage.AuditPayloadPartType(input.ResponsePartType)
	}
	c.inner.Finalize(converted)
}

// chainResponseAuditCapture bridges the G17 capture into
// gatewayresponse.AttemptAuditCapture.
type chainResponseAuditCapture struct {
	capture *gatewayusage.AuditCaptureContext
}

func (c chainResponseAuditCapture) BindContext(context gatewaypreauth.AuditGatewayContext) {
	preauthAuditCapture{inner: c.capture}.BindContext(context)
}

func (c chainResponseAuditCapture) AddGatewayMetadata(label string, metadata map[string]any) {
	c.capture.AddGatewayMetadata(label, metadata)
}

func (c chainResponseAuditCapture) Finalize(input gatewaypreauth.AuditFinalizeInput) {
	preauthAuditCapture{inner: c.capture}.Finalize(input)
}

func (c chainResponseAuditCapture) CompleteAttempt(attemptID string, input gatewayresponse.AttemptAuditInput) {
	converted := gatewayusage.CompleteAttemptInput{
		Success:      input.Success,
		ErrorPhase:   input.ErrorPhase,
		ErrorCode:    input.ErrorCode,
		ErrorMessage: input.ErrorMessage,
	}
	status := input.StatusCode
	converted.StatusCode = &status
	if input.ResponseHeaders != nil {
		if headers, ok := input.ResponseHeaders.(map[string]any); ok {
			converted.ResponseHeaders = headers
		}
	}
	if len(input.ResponseBody) > 0 {
		converted.ResponseBody = input.ResponseBody
		converted.HasResponseBody = true
	}
	c.capture.CompleteAttempt(attemptID, converted)
}

func (c chainResponseAuditCapture) FinalizeLazy(provider func() gatewaypreauth.AuditFinalizeInput) {
	c.capture.FinalizeLazy(func() gatewayusage.FinalizeAuditInput {
		converted := gatewayusage.FinalizeAuditInput{}
		input := provider()
		converted.Success = input.Success
		converted.ErrorPhase = input.ErrorPhase
		converted.ErrorCode = input.ErrorCode
		converted.ErrorMessage = input.ErrorMessage
		if input.Outcome != "" {
			converted.Outcome = gatewayusage.AuditOutcome(input.Outcome)
		}
		status := input.StatusCode
		converted.StatusCode = &status
		if input.ResponseBody != "" {
			converted.ResponseBody = []byte(input.ResponseBody)
			converted.HasResponseBody = true
		}
		if input.ResponsePartType != "" {
			converted.ResponsePartType = gatewayusage.AuditPayloadPartType(input.ResponsePartType)
		}
		return converted
	})
}

func (c chainResponseAuditCapture) OmitPayloadBodies(input gatewayresponse.OmitPayloadBodiesInput) {
	partTypes := make([]gatewayusage.AuditPayloadPartType, 0, len(input.PartTypes))
	for _, partType := range input.PartTypes {
		partTypes = append(partTypes, gatewayusage.AuditPayloadPartType(partType))
	}
	c.capture.OmitPayloadBodies(gatewayusage.OmitPayloadBodiesInput{
		Label:                      input.Label,
		Metadata:                   input.Metadata,
		PartTypes:                  partTypes,
		AlreadyOmittedPayloadCount: input.AlreadyOmittedPayloadCount,
		AlreadyOmittedBodyBytes:    int(input.AlreadyOmittedBodyBytes),
	})
}

func (c chainResponseAuditCapture) ShouldCaptureSuccessPayloads() bool {
	return c.capture.ShouldCaptureSuccessPayloads()
}
