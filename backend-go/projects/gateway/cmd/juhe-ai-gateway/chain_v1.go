package main

// G20 phase-2 flip deliverable: the top-level /v1 HTTP orchestrator (Node
// handleOpenAIGatewayRequest, backend/src/modules/gateway/routes.ts second
// half). The stage order, error exits and SSE semantics mirror the Node call
// sequence:
//
//	preauth (runtime resolution + guards) -> body pipeline ->
//	request.accepted log -> request snapshot -> audit capture ->
//	preflight (route-action fallback loop) -> engine dispatch loop
//	(with the api-key group fallback switch) ->
//	response handling (stream pipe / non-stream) -> finalization.
//
// The preauth + body stages run first because the archived Node mounts them
// as server-level middlewares ahead of openAIGatewayRouter (server.ts
// 488-500); the accepted log (routes.ts:287), the usage request snapshot
// (:296) and the audit capture (:297) therefore observe the parsed body.
//
// The deep per-branch server-retry loops of the Node source (speed-first
// cutover, codex encrypted-content recovery, account-lock lease carry) run
// inside the frozen Go engine / response slices; this file sequences them,
// walks the Node resolveRouteAction / switchToFallbackGroup fallback loops
// and renders the Node error exits.

import (
	"context"
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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// gatewayTrafficSource mirrors normalizeOpenAIGatewayTrafficSource(undefined).
const gatewayTrafficSource = "gateway"

// gatewayChain is the assembled /v1 chain.
type gatewayChain struct {
	preauth             *gatewaypreauth.Service
	engine              *gatewaydispatch.Engine
	observability       gatewaypreauth.Observability
	clock               gatewaypreauth.Clock
	bodyPipeline        *gatewaybody.Middleware
	speedFirstAdmission *chainSpeedFirstBodyAdmissionGate
	finalizationUsage   gatewayusage.UsageRecorder
	auditSettings       gatewayusage.AuditLogSettingsSource
	auditDispatcher     gatewayusage.AuditDispatcher
	usageModelResolver  gatewayusage.UsageModelResolver
	// compat answers the openai-compatible files / vector-stores families.
	// Deliberate Go enhancement over the archived Node server.ts order: the
	// archived Node mounted these routers AFTER
	// rejectUnrecognizedGatewayProtocolRequest (server.ts:490-494), so their
	// non-protocol paths 404'd through the gate and stayed unreachable; Go
	// mounts the family ahead of the protocol check on purpose. Nil keeps the
	// pure protocol chain (compose tests).
	compat *chainCompatDispatcher
}

// ServeHTTP implements http.Handler over the /v1 prefix.
func (c *gatewayChain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.handleOpenAIGatewayRequest(w, r)
}

// handleOpenAIGatewayRequest mirrors handleOpenAIGatewayRequest (routes.ts)
// preceded by the server.ts gateway middleware chain it replaces
// (rejectUnrecognizedGatewayProtocolRequest -> preauth -> body pipeline).
func (c *gatewayChain) handleOpenAIGatewayRequest(w http.ResponseWriter, r *http.Request) {
	// Protocol gate. The openai-compatible families answer ahead of the gate
	// on purpose (see the gatewayChain.compat note): that ordering is a Go
	// enhancement, NOT the archived Node server.ts order, where the same
	// routers sat behind rejectUnrecognizedGatewayProtocolRequest. Every
	// other non-protocol /v1 path keeps the Node 404 JSON contract.
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

	// ---- pre-auth stage (request/preauth.ts middleware order) ----
	if err := c.preauth.PreResolveGatewayRuntime(ctx, res, req, func() {}); err != nil {
		c.handleOrchestratorError(err, req, res, startedAt, endpoint)
		return
	}
	if res.HeadersSent() || writableEndedOf(res) {
		return
	}

	// ---- body pipeline (rejectGatewayRawBodyByContentLength ->
	//      admitSpeedFirstRequestBody -> parseGatewayRawBody -> capture) ----
	if c.bodyPipeline != nil {
		if c.bodyPipeline.RejectByContentLength(w, r) {
			return
		}
		// speed-first + high-concurrency groups admit request bodies through a
		// bounded queue (Node server.ts admitSpeedFirstRequestBody); 429
		// backpressure replaces the body stages entirely.
		if c.speedFirstAdmission != nil {
			admission, admErr := c.speedFirstAdmission.AdmitBody(r.Context(), req, res, requestLane)
			if admErr != nil {
				c.handleOrchestratorError(admErr, req, res, startedAt, endpoint)
				return
			}
			if admission.Handled {
				return
			}
			if admission.Release != nil {
				defer admission.Release()
			}
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

	// ---- request acceptance + audit capture (routes.ts:287 / :296 / :297) ----
	// Node runs these inside handleOpenAIGatewayRequest, i.e. after the
	// server-level preauth and body middlewares: the accepted log and the
	// capture creation see the parsed body (snapshot body state, capture
	// rawBody — capture.service.ts addClientRequestPayload reads req.rawBody
	// once the body middleware has attached it).
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
	// Node finally (routes.ts:2645): an un-finalized capture is canceled at
	// request end so its active-capture slot is recycled. Idempotent; the
	// failure paths below may cancel earlier.
	defer gatewaypreauth.CancelAuditCapture(auditCapture)

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
		// Node routes.ts:509-517.
		gatewaypreauth.CancelAuditCapture(auditCapture)
		c.observability.LogRequestStage("preflight.failed", map[string]any{
			"traceId": traceID, "error": err.Error(),
		}, "unexpected_failure", c.clock.Now())
		c.handleOrchestratorError(err, req, res, startedAt, endpoint)
		return
	}

	// ---- per-request coordination budgets (routes.ts:258/263) ----
	budgets, err := newRequestBudgets(traceID, startedAt, c.clock)
	if err != nil {
		c.handleOrchestratorError(err, req, res, startedAt, endpoint)
		return
	}
	serverRetryBudget := gatewaypreauth.NewServerRetryBudget(0, c.clock)

	loop := &v1DispatchLoop{
		c:                   c,
		req:                 req,
		res:                 res,
		auditCapture:        auditCapture,
		requestSnapshot:     requestSnapshot,
		budgets:             budgets,
		serverRetryBudget:   serverRetryBudget,
		startedAt:           startedAt,
		endpoint:            endpoint,
		traceID:             traceID,
		actionVisitedGroups: map[string]bool{},
		enteredGroups:       map[string]bool{},
	}
	context, err := loop.resolveRouteAction(ctx, preflight)
	if err != nil {
		// Node runs resolveRouteAction outside the preflight try (routes.ts:
		// 518-520); a fallback preparation error propagates to the express
		// error middleware.
		c.handleOrchestratorError(err, req, res, startedAt, endpoint)
		return
	}
	if context == nil {
		// Node routes.ts:521-529: undefined after the route-action fallback
		// loop, or a preflight step completed the request.
		gatewaypreauth.CancelAuditCapture(auditCapture)
		c.observability.LogRequestStage("preflight.rejected", map[string]any{
			"traceId": traceID, "failureReason": "preflight_rejected",
		}, "expected_failure", c.clock.Now())
		return
	}
	c.observability.LogRequestStage("preflight.completed", map[string]any{
		"traceId":               traceID,
		"groupId":               context.UsageContext.GroupID,
		"apiKeyId":              context.UsageContext.APIKeyID,
		"candidateAccountCount": len(context.Accounts),
		"routeStrategyId":       routeStrategyIDOf(context.APIKeyRecord),
	}, "success", c.clock.Now())

	loop.current = context
	loop.enteredGroups[context.UsageContext.GroupID] = true
	loop.run(ctx)
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
	// Node routes.ts:1550-1553: shouldHandleAsStream = upstreamResponse.ok &&
	// shouldHandle... . A complete non-2xx is already the terminal upstream
	// response; never interpret a missing/misleading SSE content type as a
	// stream and replace the provider error body (429/503 with an
	// text/event-stream content type) with a gateway event.
	handleAsStream := shouldHandleOpenAIUpstreamResponseAsStreamWithStatus(
		upstream.Status(), upstream.ContentType(), streamRequest)
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
		// Node has no dedicated response-handler error exit: the failure
		// falls through to the top-level catch contract. A committed
		// downstream stays untouched (bare disconnect); an unwritten one
		// renders the fixed 503 upstream copy. (V6: the previous 502
		// 上游响应处理失败/upstream_error exit had no Node source.)
		if !res.HeadersSent() && !commitState.TransportCommitted {
			gatewaypreauth.SendGatewayJSONError(res, http.StatusServiceUnavailable,
				gatewaypreauth.GatewayErrorPayloadOf("上游暂时不可用，请重试", "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode),
				gatewaypreauth.SendGatewayErrorOptions{Protocol: clientErrorProtocol(req)})
		}
		return
	}
	gatewayresponse.FinalizeHandledUpstreamResponse(*input, handling)
}

// ---------------------------------------------------------------------------
// dispatch loop with the api-key group fallback (routes.ts second half)
// ---------------------------------------------------------------------------

// v1DispatchLoop carries the mutable per-request state of the Node dispatch
// loop (routes.ts:537-566 locals): the current preflight context, the
// visited-group markers of resolveRouteAction (routeActionVisitedGroupIds) and
// switchToFallbackGroup (enteredRouteGroupIds), and the fallback hop counter.
type v1DispatchLoop struct {
	c                   *gatewayChain
	req                 *gatewaypreauth.GatewayRequest
	res                 *gatewaypreauth.TrackingWriter
	auditCapture        gatewaypreauth.AuditCaptureContext
	requestSnapshot     gatewaypreauth.UsageRequestSnapshot
	budgets             requestBudgets
	serverRetryBudget   *gatewaypreauth.ServerRetryBudget
	startedAt           int64
	endpoint            string
	traceID             string
	current             *gatewaypreauth.DispatchContext
	actionVisitedGroups map[string]bool
	enteredGroups       map[string]bool
	fallbackSwitches    int
}

// v1FallbackSwitch mirrors the switchToFallbackGroup return union
// (routes.ts:570-572).
type v1FallbackSwitch string

const (
	v1FallbackNone      v1FallbackSwitch = "none"
	v1FallbackSwitched  v1FallbackSwitch = "switched"
	v1FallbackCompleted v1FallbackSwitch = "completed"
)

// run mirrors the Node while(true) dispatch loop: fetch the first available
// upstream for the current group context and hand the response to the
// response layer; classify dispatch errors, switching to the fallback group
// before rendering the terminal exits.
func (l *v1DispatchLoop) run(ctx context.Context) {
	for {
		current := l.current
		coordination := &gatewaydispatch.RequestCoordinationContext{
			Scope:                    gatewaydispatch.CoordinationScopeGatewayRequest,
			ServerRetryBudget:        l.serverRetryBudget,
			GatewayRequestWallBudget: l.budgets.wall,
			RouteCoordinationBudget:  l.budgets.coordination,
			RequestAttemptTracker:    l.budgets.tracker,
		}
		dispatched, dispatchErr := l.c.engine.FetchFirstAvailableUpstream(ctx, gatewaydispatch.FetchFirstAvailableUpstreamArgs{
			Req:                             l.req,
			Accounts:                        current.Accounts,
			Settings:                        current.ActiveGatewaySettings,
			UsageContext:                    current.UsageContext,
			AuditCapture:                    l.c.engineAuditCapture(l.auditCapture),
			SessionAffinityKey:              current.SessionAffinityKey,
			Signal:                          ctx,
			ClientIPAccountAvoidanceTracker: current.ClientIPAccountAvoidance,
			RequestLane:                     string(current.RequestLane),
			GroupSchedulingPolicy:           current.GroupSchedulingPolicy,
			AccountStateMutationEnabled:     current.UsageContext.TrafficSource == gatewayTrafficSource,
			RequestClientCompatibility:      current.ClientStrategy.RequestClientCompatibility,
			ModelPriority:                   current.ModelPriority,
			AllowPrecheckHalfOpen:           current.PrecheckHalfOpenEligible,
			RequestCoordination:             coordination,
			WaitForRecoverableFailures:      true,
		})
		if dispatchErr == nil {
			// ---- response piping + finalization (response/finalization.ts) ----
			l.c.handleUpstreamResponse(l.req, l.res, l.auditCapture, current, dispatched, l.startedAt, current.ActiveGatewaySettings, l.budgets)
			return
		}
		if l.settleDispatchError(ctx, dispatchErr) {
			return
		}
	}
}

// settleDispatchError maps one dispatch-loop error onto the Node error
// handling (routes.ts:1285-1487 catch + the top-level catch 2532-2638). It
// returns true when the request is settled (response written, aborted, or
// terminal exit rendered) and false when the loop should continue on the
// switched fallback group.
func (l *v1DispatchLoop) settleDispatchError(ctx context.Context, dispatchErr error) bool {
	// Node top-level catch: known errors (downstream closed, agent guidance,
	// validation / codex adapter, diagnostic timeout/cancel) render their own
	// contracts before the exhaustion exits.
	if l.c.preauth.HandleGatewayRequestKnownErrorResponse(gatewaypreauth.KnownErrorResponseInput{
		Req:          l.req,
		Res:          l.res,
		AuditCapture: l.auditCapture,
		Err:          dispatchErr,
		Signal:       ctx,
	}) {
		return true
	}
	var aborted *gatewaydispatch.UpstreamRequestAbortedError
	if errors.As(dispatchErr, &aborted) {
		// Downstream closed / request aborted: no response contract.
		return true
	}
	var wall *gatewaydispatch.GatewayRequestWallBudgetExhaustedError
	if errors.As(dispatchErr, &wall) {
		if wall.BudgetKind == gatewaydispatch.WallBudgetKindCoordination {
			// Node routes.ts:1346-1370: the coordination kind hands the
			// request back to the client instead of the wall 503.
			l.auditCapture.AddGatewayMetadata("gateway_request_client_handoff", map[string]any{
				"reason":          "route_coordination_budget_exhausted",
				"wallRemainingMs": wall.WallRemainingMs,
			})
			l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
				message:        "网关请求协调预算已到，请客户端重试并重新选择可用账户",
				usageContext:   l.current.UsageContext,
				clientStrategy: &l.current.ClientStrategy,
			})
			return true
		}
		// The wall kind keeps the fixed 503 wall exit (review ruling V4; the
		// Go engine surfaces the wall error instead of the Node while-loop
		// continue, whose continuation the engine budget loop internalizes).
		message := "网关请求时间预算已用尽，请稍后重试"
		l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             l.req,
			Res:             l.res,
			AuditCapture:    l.auditCapture,
			UsageContext:    l.current.UsageContext,
			StartedAt:       l.startedAt,
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
		return true
	}
	var attempt *gatewaydispatch.UpstreamAttemptError
	if errors.As(dispatchErr, &attempt) {
		if !attempt.TerminalUpstreamFailure {
			// Node 1469-1478: try the fallback group before the exhaustion
			// exit. A switched fallback continues the loop; a completed one
			// means the fallback preflight settled the request.
			reason := "upstream_accounts_exhausted"
			if attempt.AgentGuidanceResponse != nil {
				reason = "account_scoped_agent_guidance_exhausted"
			}
			switch fallback, fallbackErr := l.switchToFallbackGroup(ctx, reason); {
			case fallbackErr != nil:
				// Node: the switch error propagates to the top-level catch.
				l.renderUnexpectedDispatchFailure(fallbackErr)
				return true
			case fallback == v1FallbackCompleted:
				return true
			case fallback == v1FallbackSwitched:
				return false
			}
		}
		l.renderDispatchExhausted(attempt)
		return true
	}
	// Node top-level catch: an unexpected dispatch error keeps the 503
	// upstream contract — never the orchestrator 500 (V5).
	l.renderUnexpectedDispatchFailure(dispatchErr)
	return true
}

// renderDispatchExhausted mirrors the Node top-level catch for the
// UpstreamAttemptError branch (routes.ts:2551-2638): the client payload is
// the fixed copy pair (no candidate accounts vs. retryable upstream), the
// detailed last-attempt diagnostics stay on the audit/log surface
// (upstream-dispatch.ts buildUpstreamAttemptFailureMessage,
// dispatch-exhaustion-classifier.ts).
func (l *v1DispatchLoop) renderDispatchExhausted(attempt *gatewaydispatch.UpstreamAttemptError) {
	lastAttempt := attempt.LastAttempt
	fields := map[string]any{
		"event":         "gateway_dispatch_exhausted",
		"endpoint":      l.current.UsageContext.Endpoint,
		"apiKeyId":      l.current.UsageContext.APIKeyID,
		"groupId":       l.current.UsageContext.GroupID,
		"trafficSource": l.current.UsageContext.TrafficSource,
	}
	failureReason, upstreamStatus := classifyGatewayDispatchExhaustion(lastAttempt)
	fields["failureReason"] = failureReason
	if upstreamStatus != nil {
		fields["upstreamStatus"] = upstreamStatus
	}
	if lastAttempt != nil {
		fields["lastAttemptAccountId"] = lastAttempt.AccountID
	}
	fields["failedAccountIds"] = attempt.FailedAccountIDs
	l.c.observability.Logger().Warn("gateway_dispatch_exhausted", fields, "网关上游调度已耗尽")

	payloadMessage := "上游暂时不可用，请重试"
	payloadCode := gatewaypreauth.GatewayStreamClientRetryErrorCode
	if lastAttempt == nil {
		// Node: message === '没有可用的上游账户' — the no-candidate attempt
		// error carries no last attempt.
		payloadMessage = "没有可用的上游账户"
		payloadCode = "no_available_upstream_account"
	}
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             l.res,
		AuditCapture:    l.auditCapture,
		UsageContext:    l.current.UsageContext,
		StartedAt:       l.startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(payloadMessage, "service_unavailable", payloadCode),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    payloadCode,
			ErrorMessage: attempt.Message,
		},
		RecordUsage:  boolPtr(lastAttempt == nil),
		FailureScope: "upstream",
	})
}

// renderUnexpectedDispatchFailure mirrors the Node top-level catch for
// non-attempt errors (routes.ts:2564-2572 + 2616-2638): the 503 upstream
// contract with the fixed copy; the error detail stays on the log/audit
// surface.
func (l *v1DispatchLoop) renderUnexpectedDispatchFailure(dispatchErr error) {
	l.c.observability.Logger().Warn("gateway_request_unexpected_error", map[string]any{
		"event":    "gateway_request_unexpected_error",
		"endpoint": l.current.UsageContext.Endpoint,
		"apiKeyId": l.current.UsageContext.APIKeyID,
		"groupId":  l.current.UsageContext.GroupID,
		"error":    dispatchErr.Error(),
	}, "网关请求处理出现未预期异常")
	payload := gatewaypreauth.GatewayErrorPayloadOf("上游暂时不可用，请重试", "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode)
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             l.res,
		AuditCapture:    l.auditCapture,
		UsageContext:    l.current.UsageContext,
		StartedAt:       l.startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: payload,
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
			ErrorMessage: dispatchErr.Error(),
		},
		RecordUsage:  boolPtr(true),
		FailureScope: "upstream",
	})
}

// resolveRouteAction mirrors resolveRouteAction (routes.ts:433-487): walk
// route actions, attempting the api-key group fallback before rendering a
// terminal action. A nil result means the request ended inside the loop
// (terminal action rendered, or the fallback preflight completed/rejected it).
func (l *v1DispatchLoop) resolveRouteAction(ctx context.Context, initial gatewaypreauth.PreflightResult) (*gatewaypreauth.DispatchContext, error) {
	result := initial
	for result.IsRouteAction() {
		action := result.RouteAction
		groupID := action.UsageContext.GroupID
		mayTryFallback := action.Coordination.Outcome != gatewaypreauth.RouteOutcomeClientHandoff &&
			action.InteractionResourceAffinity == nil &&
			!l.actionVisitedGroups[groupID]
		l.actionVisitedGroups[groupID] = true
		if mayTryFallback {
			actionAPIKeyRecord := action.APIKeyRecord
			if action.GroupFallbackAPIKeyRecord != nil {
				actionAPIKeyRecord = action.GroupFallbackAPIKeyRecord
			}
			fallback, err := l.c.preauth.PrepareAPIKeyGroupFallbackDispatchContext(ctx, gatewaypreauth.APIKeyGroupFallbackDispatchInput{
				Req:                        l.req,
				Res:                        l.res,
				AuditCapture:               l.auditCapture,
				Options:                    l.actionFallbackOptions(action),
				StartedAt:                  l.startedAt,
				TraceID:                    l.traceID,
				ClientIP:                   l.req.ClientIP,
				Endpoint:                   l.endpoint,
				RequestSnapshot:            l.requestSnapshot,
				Signal:                     ctx,
				Reason:                     action.Coordination.Reason,
				APIKeyRecord:               actionAPIKeyRecord,
				GroupFallbackAPIKeyRecord:  actionAPIKeyRecord,
				SystemAccountID:            action.UsageContext.SystemAccountID,
				APIKeyID:                   action.UsageContext.APIKeyID,
				GroupID:                    groupID,
				TrafficSource:              action.UsageContext.TrafficSource,
				RequestLane:                action.RequestLane,
				RequestClientCompatibility: action.ClientStrategy.RequestClientCompatibility,
				RoutePlanSnapshot:          action.RoutePlanSnapshot,
			})
			if err != nil {
				return nil, err
			}
			if fallback.Attempted {
				if !isEmptyPreflightResult(fallback.Context) {
					result = fallback.Context
					continue
				}
				// attempted && undefined: the fallback preflight completed or
				// rejected the request (Node 481).
				return nil, nil
			}
		}
		l.finalizeRouteAction(action)
		return nil, nil
	}
	return result.DispatchContext, nil
}

// actionFallbackOptions mirrors the option bag Node passes from the route
// action (routes.ts:449-459): the shared budgets and the per-group runtime
// fields travel into the fallback preflight.
func (l *v1DispatchLoop) actionFallbackOptions(action *gatewaypreauth.RouteAction) *gatewaypreauth.PreflightOptions {
	return &gatewaypreauth.PreflightOptions{
		TrafficSource:              gatewayTrafficSource,
		RequestLane:                action.RequestLane,
		ServerRetryBudget:          action.ServerRetryBudget,
		GatewayRequestWallBudget:   action.GatewayRequestWallBudget,
		RouteCoordinationBudget:    action.RouteCoordinationBudget,
		RequestAttemptTracker:      action.RequestAttemptTracker,
		DownstreamCommitState:      action.DownstreamCommitState,
		NormalRouteFirstByteConfig: action.NormalRouteFirstByteConfig,
	}
}

// switchToFallbackGroup mirrors switchToFallbackGroup (routes.ts:570-662).
func (l *v1DispatchLoop) switchToFallbackGroup(ctx context.Context, reason string) (v1FallbackSwitch, error) {
	current := l.current
	if current.InteractionResourceAffinity != nil {
		return v1FallbackNone, nil
	}
	// Node 576-578 + 624: the agent-guidance reason may elevate to the
	// group-fallback key record; both records default to the current one.
	groupFallbackRecord := current.APIKeyRecord
	if current.GroupFallbackAPIKeyRecord != nil {
		groupFallbackRecord = current.GroupFallbackAPIKeyRecord
	}
	fallbackAPIKeyRecord := current.APIKeyRecord
	if reason == "account_scoped_agent_guidance_exhausted" {
		fallbackAPIKeyRecord = groupFallbackRecord
	}
	groupBindingCount := 0
	if fallbackAPIKeyRecord != nil {
		groupBindingCount = len(fallbackAPIKeyRecord.GroupBindings)
	}
	if groupBindingCount > 0 && l.fallbackSwitches >= groupBindingCount {
		l.auditCapture.AddGatewayMetadata("api_key_group_route_fallback_skipped", map[string]any{
			"reason":              reason,
			"groupBindingCount":   groupBindingCount,
			"fallbackSwitchCount": l.fallbackSwitches,
			"skippedReason":       "fallback_hop_limit",
		})
		return v1FallbackNone, nil
	}
	fallback, err := l.c.preauth.PrepareAPIKeyGroupFallbackDispatchContext(ctx, gatewaypreauth.APIKeyGroupFallbackDispatchInput{
		Req:                        l.req,
		Res:                        l.res,
		AuditCapture:               l.auditCapture,
		Options:                    l.fallbackOptions(current),
		StartedAt:                  l.startedAt,
		TraceID:                    l.traceID,
		ClientIP:                   l.req.ClientIP,
		Endpoint:                   l.endpoint,
		RequestSnapshot:            l.requestSnapshot,
		Signal:                     ctx,
		Reason:                     reason,
		APIKeyRecord:               fallbackAPIKeyRecord,
		GroupFallbackAPIKeyRecord:  groupFallbackRecord,
		SystemAccountID:            current.UsageContext.SystemAccountID,
		APIKeyID:                   current.UsageContext.APIKeyID,
		GroupID:                    current.UsageContext.GroupID,
		TrafficSource:              current.UsageContext.TrafficSource,
		RequestLane:                current.RequestLane,
		RequestClientCompatibility: current.ClientStrategy.RequestClientCompatibility,
		RoutePlanSnapshot:          current.RoutePlanSnapshot,
	})
	if err != nil {
		return "", err
	}
	if !fallback.Attempted {
		return v1FallbackNone, nil
	}
	// An empty fallback context means the fallback preflight completed the
	// request (Node 630-633).
	if isEmptyPreflightResult(fallback.Context) {
		return v1FallbackCompleted, nil
	}
	next, err := l.resolveRouteAction(ctx, fallback.Context)
	if err != nil {
		return "", err
	}
	if next == nil {
		return v1FallbackCompleted, nil
	}
	l.fallbackSwitches++
	// Node 640-642: a repeated group target stops the switch (the hop still
	// counts).
	if l.enteredGroups[next.UsageContext.GroupID] {
		return v1FallbackNone, nil
	}
	l.enteredGroups[next.UsageContext.GroupID] = true
	// Node 644-651 transfers the client-ip slot and settles the hot-quality
	// reservation; those lifecycle ports stay engine-internal in Go. The
	// per-group retry resets ride on the fresh DispatchContext.
	l.current = next
	return v1FallbackSwitched, nil
}

// fallbackOptions mirrors the option bag Node passes from the current
// preflight (routes.ts:597-607).
func (l *v1DispatchLoop) fallbackOptions(current *gatewaypreauth.DispatchContext) *gatewaypreauth.PreflightOptions {
	return &gatewaypreauth.PreflightOptions{
		TrafficSource:              gatewayTrafficSource,
		RequestLane:                current.RequestLane,
		ServerRetryBudget:          current.ServerRetryBudget,
		GatewayRequestWallBudget:   current.GatewayRequestWallBudget,
		RouteCoordinationBudget:    current.RouteCoordinationBudget,
		RequestAttemptTracker:      current.RequestAttemptTracker,
		DownstreamCommitState:      current.DownstreamCommitState,
		NormalRouteFirstByteConfig: current.NormalRouteFirstByteConfig,
	}
}

// streamServerRetryExhaustedInput mirrors sendStreamServerRetryExhaustedResponse's
// consumed input (routes.ts:2966-2982).
type streamServerRetryExhaustedInput struct {
	message        string
	usageContext   gatewaypreauth.GatewayFailureUsageContext
	clientStrategy *gatewaypreauth.ClientStrategyContext
}

// sendStreamServerRetryExhaustedResponse renders the stream server-retry
// exhausted / client-handoff contract (routes.ts:2966 →
// sendPreCommitStreamRetryExhaustedResponse:3109-3133). The Go frozen surface
// carries neither the retryCoordination failure signal nor a committed-
// downstream tracker across the dispatch boundary, so the pre-commit
// HTTP-error branch is the reachable rendering: 503 + fixed copy +
// stream_failed audit + recordUsage:false.
func (l *v1DispatchLoop) sendStreamServerRetryExhaustedResponse(input streamServerRetryExhaustedInput) {
	message := input.message
	if message == "" {
		message = "服务端流式重试未找到可用账号"
	}
	if input.clientStrategy != nil {
		l.auditCapture.AddGatewayMetadata("stream_server_retry_exhausted", map[string]any{
			"retryReason":        "pre_commit_stream_failure",
			"responseMode":       "pre_commit_http_error",
			"clientProfile":      input.clientStrategy.ClientProfile,
			"downstreamProtocol": input.clientStrategy.DownstreamProtocol,
		})
	}
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             l.res,
		AuditCapture:    l.auditCapture,
		UsageContext:    input.usageContext,
		StartedAt:       l.startedAt,
		StatusCode:      http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(message, "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeStreamFailed,
			ErrorPhase:   "stream",
			ErrorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
			ErrorMessage: message,
		},
		FailureScope:      "upstream",
		RecordUsage:       boolPtr(false),
		UsageErrorMessage: message,
	})
}

// finalizeRouteAction mirrors the Node finalizeRouteAction: the route-action
// failure / client-handoff / blocked / exhausted exits (routes.ts:373-432).
func (l *v1DispatchLoop) finalizeRouteAction(action *gatewaypreauth.RouteAction) {
	res := l.res
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
		l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
			Req:             l.req,
			Res:             res,
			AuditCapture:    l.auditCapture,
			UsageContext:    action.UsageContext,
			StartedAt:       l.startedAt,
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
	if action.Coordination.Outcome == gatewaypreauth.RouteOutcomeClientHandoff {
		// Node 398-411: the client-handoff outcome renders the stream server
		// retry exhausted contract instead of the exhausted-accounts copy.
		l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
			message:        "当前路由暂时无法继续派发，请客户端重试并重新选择可用账户",
			usageContext:   action.UsageContext,
			clientStrategy: &action.ClientStrategy,
		})
		return
	}
	temporarilyBlocked := action.Coordination.Outcome == "temporarily_blocked"
	message := "当前路由没有可用的上游账户"
	if temporarilyBlocked {
		message = "当前路由暂时没有可派发账户，请稍后重试"
	}
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:             l.req,
		Res:             res,
		AuditCapture:    l.auditCapture,
		UsageContext:    action.UsageContext,
		StartedAt:       l.startedAt,
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

// shouldHandleOpenAIUpstreamResponseAsStreamWithStatus mirrors routes.ts:1550:
// shouldHandleAsStream = upstreamResponse.ok &&
// shouldHandleOpenAIUpstreamResponseAsStream({contentType, streamRequest}).
// The status gate is explicit (not gatewaydispatch.GatewayUpstreamResponse.OK)
// so the decision stays unit-testable over plain vectors.
func shouldHandleOpenAIUpstreamResponseAsStreamWithStatus(status int, contentType string, streamRequest bool) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices &&
		gatewayresponse.ShouldHandleOpenAIUpstreamResponseAsStream(contentType, streamRequest)
}

// classifyGatewayDispatchExhaustion mirrors
// response/dispatch-exhaustion-classifier.ts.
func classifyGatewayDispatchExhaustion(lastAttempt *gatewaydispatch.UpstreamAttempt) (string, any) {
	if lastAttempt == nil {
		return "no_available_account", nil
	}
	switch lastAttempt.UpstreamURL {
	case "account:api_key_pool_unavailable":
		return "api_key_pool_unavailable", nil
	case "account:locally_suppressed":
		return "all_accounts_locally_suppressed", nil
	case "concurrency:limit":
		return "account_concurrency_exhausted", nil
	}
	if lastAttempt.HasStatus && lastAttempt.Status > 0 {
		return "upstream_http_error", lastAttempt.Status
	}
	return "upstream_transport_error", nil
}

// isEmptyPreflightResult mirrors the Node undefined member of the
// DispatchContext | RouteAction | undefined union.
func isEmptyPreflightResult(result gatewaypreauth.PreflightResult) bool {
	return result.DispatchContext == nil && result.RouteAction == nil
}

// routeStrategyIDOf mirrors routes.ts:535 preflight.apiKeyRecord?.route_strategy_id.
func routeStrategyIDOf(record *gatewayruntimecache.GatewayAPIKeyRow) string {
	if record == nil {
		return ""
	}
	return record.RouteStrategyID
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
	// Express error middleware fallback (server.ts:525-539): the plain
	// {"message":"服务器内部错误"} 500 body — no gateway error envelope, no
	// invented copy.
	kernel.WriteError(res, http.StatusInternalServerError, "服务器内部错误")
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
