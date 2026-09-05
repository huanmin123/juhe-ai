package main

// G20 phase-2 flip deliverable: the top-level /v1 HTTP orchestrator (Node
// handleOpenAIGatewayRequest, backend/src/modules/gateway/routes.ts second
// half). The stage order, error exits and SSE semantics mirror the Node call
// sequence:
//
//	request.accepted log -> request snapshot -> audit capture ->
//	preauth (runtime resolution + guards) -> preflight
//	(route-action finalize) -> engine dispatch loop ->
//	response handling (stream pipe / non-stream) -> finalization.
//
// The deep per-branch server-retry loops of the Node source (speed-first
// cutover, codex encrypted-content recovery, account-lock lease carry) run
// inside the frozen Go engine / response slices; this file sequences them and
// renders the Node error exits.

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// gatewayTrafficSource mirrors normalizeOpenAIGatewayTrafficSource(undefined).
const gatewayTrafficSource = "gateway"

// gatewayChain is the assembled /v1 chain.
type gatewayChain struct {
	preauth            *gatewaypreauth.Service
	engine             *gatewaydispatch.Engine
	observability      gatewaypreauth.Observability
	clock              gatewaypreauth.Clock
	bodyPipeline       *gatewaybody.Middleware
	finalizationUsage  gatewayusage.UsageRecorder
	auditSettings      gatewayusage.AuditLogSettingsSource
	auditDispatcher    gatewayusage.AuditDispatcher
	usageModelResolver gatewayusage.UsageModelResolver
	// compat answers the openai-compatible files / vector-stores families
	// ahead of the Node 404 JSON (chain_openaicompat.go). Nil keeps the pure
	// protocol chain (compose tests).
	compat *chainCompatDispatcher
}

// ServeHTTP implements http.Handler over the /v1 prefix.
func (c *gatewayChain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.handleOpenAIGatewayRequest(w, r)
}

// handleOpenAIGatewayRequest mirrors handleOpenAIGatewayRequest (routes.ts)
// preceded by the server.ts gateway middleware chain it replaces
// (rejectUnrecognizedGatewayProtocolRequest -> body pipeline capture).
func (c *gatewayChain) handleOpenAIGatewayRequest(w http.ResponseWriter, r *http.Request) {
	// rejectUnrecognizedGatewayProtocolRequest: the openai-compatible files /
	// vector-stores families mount ahead of the protocol check (Node
	// server.ts gateway middleware order); every other non-protocol /v1 path
	// keeps the Node 404 JSON contract.
	req := gatewaypreauth.NewGatewayRequest(r)
	if !gatewayopenaiIsProtocolPath(req.PathAndQuery()) {
		if c.compat != nil {
			c.compat.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"资源不存在"}`))
		return
	}
	ctx := r.Context()
	startedAt := c.preauth.NowMs()
	res := gatewaypreauth.NewTrackingWriter(w)
	req.ClientIP = requestClientIP(req)
	traceID := c.observability.TraceID()
	if traceID == "" {
		traceID = c.observability.CreateTraceID()
	}
	endpoint := gatewaypreauth.RequestEndpoint(req)
	requestLane := gatewaypreauth.ResolveOpenAIGatewayRequestLane(req)
	model, _ := gatewaypreauth.RequestModel(req)
	stream := gatewaypreauth.RequestStream(req)
	c.observability.LogRequestStage("request.accepted", map[string]any{
		"traceId":       traceID,
		"method":        req.MethodUpper(),
		"endpoint":      endpoint,
		"requestLane":   string(requestLane),
		"trafficSource": gatewayTrafficSource,
		"model":         model,
		"stream":        stream,
	}, "success", c.clock.Now())

	requestSnapshot := usageRequestSnapshotOf(req, traceID)
	auditCapture := c.newAuditCapture(req, traceID, startedAt)

	// ---- pre-auth stage (request/pre-auth.ts middleware order) ----
	if err := c.preauth.PreResolveGatewayRuntime(ctx, res, req, func() {}); err != nil {
		c.handleOrchestratorError(err, req, res, startedAt, endpoint)
		return
	}
	if res.HeadersSent() || writableEndedOf(res) {
		return
	}

	// ---- body pipeline (rejectGatewayRawBodyByContentLength ->
	//      parseGatewayRawBody -> captureGatewayRawBody) ----
	if c.bodyPipeline != nil {
		if c.bodyPipeline.RejectByContentLength(w, r) {
			return
		}
		rawBody, parserErr := c.bodyPipeline.ReadRawBody(w, r)
		if parserErr != nil {
			if c.bodyPipeline.HandleParserRejection(w, r, parserErr) {
				return
			}
			c.handleOrchestratorError(fmt.Errorf("网关请求体读取失败"), req, res, startedAt, endpoint)
			return
		}
		bodyReq, captureErr := c.bodyPipeline.Capture(w, r, rawBody)
		if captureErr != nil {
			c.handleOrchestratorError(fmt.Errorf("网关请求体解析失败: %w", captureErr), req, res, startedAt, endpoint)
			return
		}
		if bodyReq == nil {
			// A body rejection (too large / in-flight / lane) already wrote
			// the response (Node next(false) chain exit).
			return
		}
		req.Body = bodyReq
	}

	// ---- preflight (request/preflight.ts) ----
	preflight, err := c.preauth.PrepareOpenAIGatewayDispatchContext(ctx, gatewaypreauth.PreflightInput{
		Req:             req,
		Res:             res,
		AuditCapture:    auditCapture,
		Options:         c.preflightOptions(requestLane),
		StartedAt:       startedAt,
		TraceID:         traceID,
		ClientIP:        req.ClientIP,
		Endpoint:        endpoint,
		RequestSnapshot: requestSnapshot,
		Signal:          ctx,
	})
	if err != nil {
		c.observability.LogRequestStage("preflight.failed", map[string]any{
			"traceId": traceID, "error": err.Error(),
		}, "unexpected_failure", c.clock.Now())
		c.handleOrchestratorError(err, req, res, startedAt, endpoint)
		return
	}
	if preflight.IsRouteAction() {
		c.finalizeRouteAction(preflight.RouteAction, req, res, auditCapture, startedAt)
		return
	}
	if preflight.DispatchContext == nil {
		// The request completed inside a preflight step (Node undefined).
		c.observability.LogRequestStage("preflight.rejected", map[string]any{
			"traceId": traceID, "failureReason": "preflight_rejected",
		}, "expected_failure", c.clock.Now())
		return
	}
	context := preflight.DispatchContext
	c.observability.LogRequestStage("preflight.completed", map[string]any{
		"traceId":               traceID,
		"groupId":               context.UsageContext.GroupID,
		"apiKeyId":              context.UsageContext.APIKeyID,
		"candidateAccountCount": len(context.Accounts),
	}, "success", c.clock.Now())

	// ---- dispatch loop (dispatch/upstream-dispatch.ts) ----
	settings := context.ActiveGatewaySettings
	budgets, err := newRequestBudgets(traceID, startedAt, c.clock)
	if err != nil {
		c.handleOrchestratorError(err, req, res, startedAt, endpoint)
		return
	}
	serverRetryBudget := gatewaypreauth.NewServerRetryBudget(0, c.clock)
	coordination := &gatewaydispatch.RequestCoordinationContext{
		Scope:                    gatewaydispatch.CoordinationScopeGatewayRequest,
		ServerRetryBudget:        serverRetryBudget,
		GatewayRequestWallBudget: budgets.wall,
		RouteCoordinationBudget:  budgets.coordination,
		RequestAttemptTracker:    budgets.tracker,
	}
	dispatched, dispatchErr := c.engine.FetchFirstAvailableUpstream(ctx, gatewaydispatch.FetchFirstAvailableUpstreamArgs{
		Req:                             req,
		Accounts:                        context.Accounts,
		Settings:                        settings,
		UsageContext:                    context.UsageContext,
		AuditCapture:                    c.engineAuditCapture(auditCapture),
		SessionAffinityKey:              context.SessionAffinityKey,
		Signal:                          ctx,
		ClientIPAccountAvoidanceTracker: context.ClientIPAccountAvoidance,
		RequestLane:                     string(context.RequestLane),
		GroupSchedulingPolicy:           context.GroupSchedulingPolicy,
		AccountStateMutationEnabled:     context.UsageContext.TrafficSource == gatewayTrafficSource,
		RequestClientCompatibility:      context.ClientStrategy.RequestClientCompatibility,
		ModelPriority:                   context.ModelPriority,
		AllowPrecheckHalfOpen:           context.PrecheckHalfOpenEligible,
		RequestCoordination:             coordination,
		WaitForRecoverableFailures:      true,
	})
	if dispatchErr != nil {
		c.handleDispatchError(dispatchErr, req, res, auditCapture, context, startedAt, endpoint)
		return
	}

	// ---- response piping + finalization (response/finalization.ts) ----
	c.handleUpstreamResponse(req, res, auditCapture, context, dispatched, startedAt, settings, budgets)
}

// handleUpstreamResponse mirrors handleStreamUpstreamResponse /
// handleNonStreamUpstreamResponse + finalizeHandledUpstreamResponse.
func (c *gatewayChain) handleUpstreamResponse(
	req *gatewaypreauth.GatewayRequest,
	res *gatewaypreauth.TrackingWriter,
	auditCapture gatewaypreauth.AuditCaptureContext,
	context *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	startedAt int64,
	settings gatewayruntimecache.GatewaySettings,
	budgets requestBudgets,
) {
	ctx := req.HTTP.Context()
	commitState := &gatewayresponse.DownstreamCommitState{}
	upstream := dispatched.Response
	streamRequest := gatewaypreauth.IsOpenAIStreamRequest(req)
	handleAsStream := gatewayresponse.ShouldHandleOpenAIUpstreamResponseAsStream(upstream.ContentType(), streamRequest)
	input := &gatewayresponse.HandleUpstreamResponseInput{
		Req:        req,
		Downstream: gatewayresponse.StreamDownstream{Res: res},
		Account:    gatewayresponse.OpenAIAccountView{Account: dispatched.Account},
		UpstreamResponse: &gatewayresponse.GatewayUpstreamResponse{
			Status: upstream.Status(),
			Header: upstream.Header,
			Body:   gatewayresponse.NewReaderUpstreamBody(ctx, upstream.Body),
		},
		UpstreamURL:                dispatched.UpstreamURL,
		AuditAttemptID:             dispatched.AuditAttemptID,
		AuditCapture:               responseAuditCaptureOf(auditCapture),
		Settings:                   settings,
		TimeoutProfile:             timeoutProfileOf(settings, string(context.RequestLane)),
		UsageContext:               context.UsageContext,
		StartedAtMs:                startedAt,
		Signal:                     ctx,
		SessionAffinityKey:         context.SessionAffinityKey,
		ClientStrategy:             clientStrategyViewOf(context),
		ResponseInspectionPolicies: context.ResponseInspectionPolicies,
		MarkFirstOutput:            dispatched.MarkFirstOutput,
		DownstreamCommitState:      commitState,
		Deps: &gatewayresponse.FinalizationDeps{
			UsageRecords: chainFinalizationUsage{recorder: c.finalizationUsage},
			Logger:       gatewayResponseLogger{inner: slog.Default()},
			NowMs:        func() int64 { return c.preauth.NowMs() },
		},
	}
	if dispatched.ResponsePrecommitDeadlineAtMs != nil {
		input.ResponsePrecommitDeadlineAtMs = dispatched.ResponsePrecommitDeadlineAtMs
	}
	var (
		handling gatewayresponse.UpstreamResponseHandlingResult
		err      error
	)
	if handleAsStream {
		handling, err = gatewayresponse.HandleStreamUpstreamResponse(*input)
	} else {
		handling, err = gatewayresponse.HandleNonStreamUpstreamResponse(*input)
	}
	if err != nil {
		if !res.HeadersSent() {
			gatewaypreauth.SendGatewayJSONError(res, http.StatusBadGateway,
				gatewaypreauth.GatewayErrorPayloadOf("上游响应处理失败", "upstream_error"),
				gatewaypreauth.SendGatewayErrorOptions{Protocol: clientErrorProtocol(req)})
		}
		return
	}
	gatewayresponse.FinalizeHandledUpstreamResponse(*input, handling)
}

// finalizeRouteAction mirrors the Node finalizeRouteAction: the route-action
// failure / blocked / exhausted exits.
func (c *gatewayChain) finalizeRouteAction(
	action *gatewaypreauth.RouteAction,
	req *gatewaypreauth.GatewayRequest,
	res gatewaypreauth.GatewayResponseWriter,
	auditCapture gatewaypreauth.AuditCaptureContext,
	startedAt int64,
) {
	if writableEndedOf(res) {
		return
	}
	if action.Failure != nil {
		failure := action.Failure
		if failure.RetryAfterMs != nil && !res.HeadersSent() {
			retryAfterSeconds := (*failure.RetryAfterMs + 999) / 1000
			if retryAfterSeconds < 1 {
				retryAfterSeconds = 1
			}
			res.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))
		}
		c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             req,
			Res:             res,
			AuditCapture:    auditCapture,
			UsageContext:    action.UsageContext,
			StartedAt:       startedAt,
			StatusCode:      failure.StatusCode,
			ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(failure.Message, failure.ErrorType, failure.ErrorCode),
			Audit: gatewaypreauth.FailureAudit{
				Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
				ErrorPhase:   failure.ErrorPhase,
				ErrorCode:    failure.ErrorCode,
				ErrorMessage: failure.Message,
			},
			FailureAttribution: failure.FailureAttribution,
		})
		return
	}
	temporarilyBlocked := action.Coordination.Outcome == "temporarily_blocked"
	message := "当前路由没有可用的上游账户"
	if temporarilyBlocked {
		message = "当前路由暂时没有可派发账户，请稍后重试"
	}
	c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             req,
		Res:             res,
		AuditCapture:    auditCapture,
		UsageContext:    action.UsageContext,
		StartedAt:       startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(message, "service_unavailable", "upstream_retryable_error"),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    "upstream_retryable_error",
			ErrorMessage: message,
		},
		FailureScope: "upstream",
	})
}

// handleDispatchError maps the dispatch-loop error unions onto the Node
// error exits (classifyGatewayDispatchExhaustion + wall-budget exhaustion).
func (c *gatewayChain) handleDispatchError(
	err error,
	req *gatewaypreauth.GatewayRequest,
	res gatewaypreauth.GatewayResponseWriter,
	auditCapture gatewaypreauth.AuditCaptureContext,
	context *gatewaypreauth.DispatchContext,
	startedAt int64,
	endpoint string,
) {
	var aborted *gatewaydispatch.UpstreamRequestAbortedError
	if errors.As(err, &aborted) {
		// Downstream closed / request aborted: no response contract.
		return
	}
	var attempt *gatewaydispatch.UpstreamAttemptError
	if errors.As(err, &attempt) {
		message := attempt.Message
		if message == "" {
			message = "上游账户请求失败"
		}
		c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             req,
			Res:             res,
			AuditCapture:    auditCapture,
			UsageContext:    context.UsageContext,
			StartedAt:       startedAt,
			StatusCode:      http.StatusServiceUnavailable,
			ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(message, "service_unavailable", "upstream_retryable_error"),
			Audit: gatewaypreauth.FailureAudit{
				Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
				ErrorPhase:   "dispatch",
				ErrorCode:    "upstream_retryable_error",
				ErrorMessage: message,
			},
			FailureScope: "upstream",
		})
		return
	}
	var wall *gatewaydispatch.GatewayRequestWallBudgetExhaustedError
	if errors.As(err, &wall) {
		message := "网关请求时间预算已用尽，请稍后重试"
		c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             req,
			Res:             res,
			AuditCapture:    auditCapture,
			UsageContext:    context.UsageContext,
			StartedAt:       startedAt,
			StatusCode:      http.StatusServiceUnavailable,
			ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(message, "service_unavailable", "gateway_request_wall_budget_exhausted"),
			Audit: gatewaypreauth.FailureAudit{
				Outcome:      gatewaypreauth.AuditOutcomeGatewayFailed,
				ErrorPhase:   "dispatch",
				ErrorCode:    "gateway_request_wall_budget_exhausted",
				ErrorMessage: message,
			},
			FailureScope: "upstream",
		})
		return
	}
	c.handleOrchestratorError(err, req, res, startedAt, endpoint)
}

// handleOrchestratorError mirrors handleGatewayDbServiceUnavailable + the
// express error fallback: a local db-service outage renders 503 with the
// verbatim message; everything else renders the 500 contract.
func (c *gatewayChain) handleOrchestratorError(
	err error,
	req *gatewaypreauth.GatewayRequest,
	res gatewaypreauth.GatewayResponseWriter,
	startedAt int64,
	endpoint string,
) {
	if res.HeadersSent() {
		return
	}
	message := err.Error()
	if dbServiceUnavailableMessage(message) {
		c.observability.Logger().Warn("gateway_db_service_unavailable", map[string]any{
			"event": "gateway_db_service_unavailable", "endpoint": endpoint, "error": message,
		}, "网关 DB service 不可用")
		gatewaypreauth.SendGatewayJSONError(res, http.StatusServiceUnavailable,
			gatewaypreauth.GatewayErrorPayloadOf(message, "service_unavailable"),
			gatewaypreauth.SendGatewayErrorOptions{Protocol: clientErrorProtocol(req)})
		return
	}
	gatewaypreauth.SendGatewayJSONError(res, http.StatusInternalServerError,
		gatewaypreauth.GatewayErrorPayloadOf("网关内部错误，请稍后重试", "internal_error"),
		gatewaypreauth.SendGatewayErrorOptions{Protocol: clientErrorProtocol(req)})
}

// dbServiceUnavailableMessage mirrors dbServiceUnavailableMessage.
func dbServiceUnavailableMessage(message string) bool {
	prefixes := []string{
		"本地数据库服务暂时不可用",
		"本地数据库服务未就绪",
		"本地数据库服务请求超时",
		"本地数据库服务已退出",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

// preflightOptions mirrors the OpenAIGatewayRequestPreflightOptions the /v1
// entry passes.
func (c *gatewayChain) preflightOptions(requestLane gatewayproto.RequestLane) *gatewaypreauth.PreflightOptions {
	return &gatewaypreauth.PreflightOptions{
		TrafficSource: gatewayTrafficSource,
		RequestLane:   requestLane,
	}
}

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
	})
	return preauthAuditCapture{inner: concrete}
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

// timeoutProfileOf projects the runtime settings + lane onto the response
// timeout profile (mirrors the dispatch timeoutProfile the result carries;
// the response layer re-derives the budget values).
func timeoutProfileOf(settings gatewayruntimecache.GatewaySettings, lane string) gatewayresponse.TimeoutProfile {
	profile := gatewayrouting.GatewayTimeoutProfileForLane(gatewayrouting.GatewayTimeoutSettings{
		TextFirstResponseTimeoutSeconds:           settings.TextFirstResponseTimeoutSeconds,
		TextStreamIdleTimeoutSeconds:              settings.TextStreamIdleTimeoutSeconds,
		TextUncommittedAttemptMaxLifetimeSeconds:  settings.TextUncommittedAttemptMaxLifetimeSeconds,
		ImageFirstResponseTimeoutSeconds:          settings.ImageFirstResponseTimeoutSeconds,
		ImageStreamIdleTimeoutSeconds:             settings.ImageStreamIdleTimeoutSeconds,
		ImageUncommittedAttemptMaxLifetimeSeconds: settings.ImageUncommittedAttemptMaxLifetimeSeconds,
		NoAvailableAccountWaitTimeoutSeconds:      settings.NoAvailableAccountWaitTimeoutSeconds,
	}, gatewayproto.RequestLane(lane), false)
	return gatewayresponse.TimeoutProfile{
		FirstResponseTimeoutMs:          profile.FirstResponseTimeoutMs,
		IdleTimeoutMs:                   profile.IdleTimeoutMs,
		UncommittedAttemptMaxLifetimeMs: profile.UncommittedAttemptMaxLifetimeMs,
		TimeoutsDisabled:                profile.TimeoutsDisabled,
	}
}

// clientStrategyViewOf projects the preflight client strategy into the
// finalization view (G18 frozen subset; the semantic-interpretation gate
// mirrors gatewayClientAllowsUpstreamSemanticInterpretation).
func clientStrategyViewOf(context *gatewaypreauth.DispatchContext) *gatewayresponse.ClientStrategyView {
	strategy := context.ClientStrategy
	interpret := true
	if strategy.ClientProfile == "" {
		interpret = true
	}
	return &gatewayresponse.ClientStrategyView{
		ClientProfile:      strategy.ClientProfile,
		DownstreamProtocol: strategy.DownstreamProtocol,
		InterpretSemantics: interpret,
	}
}

// gatewayResponseLogger adapts slog to the response StreamLogger.
type gatewayResponseLogger struct{ inner *slog.Logger }

func (l gatewayResponseLogger) Debug(event string, fields map[string]any, message string) {
	l.inner.Debug(message, append([]any{"event", event}, fieldsArgs(fields)...)...)
}

func (l gatewayResponseLogger) Info(event string, fields map[string]any, message string) {
	l.inner.Info(message, append([]any{"event", event}, fieldsArgs(fields)...)...)
}

func (l gatewayResponseLogger) Warn(event string, fields map[string]any, message string) {
	l.inner.Warn(message, append([]any{"event", event}, fieldsArgs(fields)...)...)
}

// writableEndedOf mirrors res.writableEnded for the tracking writer.
func writableEndedOf(res gatewaypreauth.GatewayResponseWriter) bool {
	if tracking, ok := res.(*gatewaypreauth.TrackingWriter); ok {
		return tracking.WritableEnded()
	}
	return false
}

// requestClientIP mirrors extractClientIp over the kernel-resolved context.
func requestClientIP(req *gatewaypreauth.GatewayRequest) string {
	if ip, ok := gatewaypreauth.ExtractClientIP(req); ok {
		return ip
	}
	return ""
}

// usageRequestSnapshotOf mirrors buildUsageRequestSnapshot.
func usageRequestSnapshotOf(req *gatewaypreauth.GatewayRequest, traceID string) gatewaypreauth.UsageRequestSnapshot {
	snapshot := gatewaypreauth.UsageRequestSnapshot{
		Method:      req.MethodUpper(),
		Path:        req.Path(),
		OriginalURL: req.PathAndQuery(),
		ClientIP:    requestClientIP(req),
		TraceID:     traceID,
	}
	if state := req.BodyState(); state != nil {
		snapshot.RequestedServiceTier = state.ServiceTier
		if state.ReasoningEffort != nil {
			snapshot.RequestedReasoningEffort = *state.ReasoningEffort
		}
	}
	return snapshot
}

// rawBodySnapshotOf mirrors the audit capture's rawBody read.
func rawBodySnapshotOf(req *gatewaypreauth.GatewayRequest) []byte {
	if req == nil || req.Body == nil {
		return nil
	}
	return req.Body.RawBody
}

// clientErrorProtocol mirrors gatewayProtocolClientErrorProtocolForRequest
// with the openai fallback for unknown requests.
func clientErrorProtocol(req *gatewaypreauth.GatewayRequest) gatewaypreauth.GatewayErrorProtocol {
	protocol, err := gatewaypreauth.GatewayProtocolClientErrorProtocolForRequest(req)
	if err != nil {
		return gatewaypreauth.GatewayErrorProtocolOpenAI
	}
	return protocol
}

// requestBudgets bundles the per-request coordination budgets.
type requestBudgets struct {
	wall         *gatewayrouting.GatewayRequestWallBudget
	coordination *gatewayrouting.RouteCoordinationBudget
	tracker      *gatewayrouting.GatewayRequestAttemptTracker
}

// newRequestBudgets mirrors the Node budget construction at the top of
// handleOpenAIGatewayRequest (RouteCoordinationBudget + wall budget +
// request attempt tracker).
func newRequestBudgets(traceID string, acceptedAtMs int64, clock gatewaypreauth.Clock) (requestBudgets, error) {
	now := func() int64 { return clock.Now().UnixMilli() }
	coordination, err := gatewayrouting.NewRouteCoordinationBudget(gatewayrouting.RouteCoordinationBudgetOptions{
		RequestID: traceID,
		Now:       now,
	})
	if err != nil {
		return requestBudgets{}, fmt.Errorf("create route coordination budget: %w", err)
	}
	wall, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: acceptedAtMs,
		Now:                 now,
	}, nil)
	if err != nil {
		return requestBudgets{}, fmt.Errorf("create gateway request wall budget: %w", err)
	}
	tracker, err := gatewayrouting.NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		return requestBudgets{}, fmt.Errorf("create gateway request attempt tracker: %w", err)
	}
	return requestBudgets{wall: wall, coordination: coordination, tracker: tracker}, nil
}
