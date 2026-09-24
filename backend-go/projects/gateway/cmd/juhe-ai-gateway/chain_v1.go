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
	"time"

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
	// responseAccountEffects 是 W4-B（BUG-0175 D-132）的响应层账户副作用
	// 面（配置策略避让 / 桶避让写侧）；nil 保持 finalization 的缺席守卫。
	responseAccountEffects gatewayresponse.AccountFailureEffects
	// speed-first（D-114，routes.ts:542-546）的 per-request 状态在
	// v1DispatchLoop 上；组合级字段到此为止。
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
	// E2E-FINDING #6：门面是三协议并集（Node isGatewayProtocolRequest），
	// anthropic / gemini native 面放行进同一条分发链。
	req := gatewaypreauth.NewGatewayRequest(r)
	if !gatewayIsProtocolRequest(req) {
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
	// SwitchTarget（切号冻结目标）请求级载体：派发链内第一个完成上游请求构造
	// 的账户在此冻结有效上游目标，此后跨账户切换 / 分组回退 / 重派窗口一律
	// 按冻结目标后置过滤（切号时有效上游目标设计 §3.1/§4）。初始 preflight
	// 无冻结目标，过滤惰性。
	ctx = gatewaydispatch.WithSwitchTargetCapture(ctx, &gatewaydispatch.SwitchTargetCapture{})
	startedAt := c.preauth.NowMs()
	res := gatewaypreauth.NewTrackingWriter(w)
	req.ClientIP = requestClientIP(req)
	// W1a 链级 traceID 与 kernel 对齐：kernel RequestContextMiddleware 已为
	// 每请求解析 TraceID（Traceparent/X-Trace-Id/X-Correlation-Id 头优先，
	// 否则 UUID）并回写响应头 X-Trace-Id，这里改用同一来源，使
	// request.accepted/preflight 日志、审计 capture、usage 与客户端拿到的
	// X-Trace-Id 同源可查。kernel.Context 兜底恒非空；CreateTraceID 回落
	// 保留以防 kernel.Context 未来行为变化。
	traceID := kernel.Context(r).TraceID
	if traceID == "" {
		traceID = c.observability.CreateTraceID()
	}
	// 阶段/尝试累积器接线：kernel RequestContext 挂在本请求上下文上，经
	// 进程级 traceId 槽回写（gatewaypreauth.Observability.LogRequestStage
	// 签名无 ctx/req，既有调用点零改动入库的唯一通道）；注销与注册在同一
	// 请求生命周期内成对，timing_summary 在链返回后的 kernel finish 点
	// 读取快照。
	requestStageCtx := kernel.Context(r)
	chainRegisterRequestStageRecorder(traceID, requestStageCtx)
	defer chainUnregisterRequestStageRecorder(traceID, requestStageCtx)
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
	// D-119（BUG-0175）recorder hunk：把 runtime 快照挂进请求上下文——
	// chainBodyRejectionRecorder 在 body 拒绝面读取身份（Node 的
	// req.gatewayRuntime 同源；gatewaybody 无法引用 gatewaypreauth 类型）。
	if req.Runtime != nil {
		r = r.WithContext(context.WithValue(r.Context(), chainGatewayRuntimeKey{}, req.Runtime))
		req.HTTP = r
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
			// HandleParserRejection 对非 nil parserErr 恒写完响应并返回 true
			// （gatewaybody.Middleware 唯一 false 出口是 perr == nil），
			// 因此这里不需要回落到 orchestrator 错误路径。
			_ = c.bodyPipeline.HandleParserRejection(w, r, parserErr)
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
	// 主 dispatch 循环复用 preflight 构造的 server retry budget（等待上限来自
	// NoAvailableAccountWaitTimeoutSeconds）；preflight 未装配 DispatchContext
	// 时（请求已在其内部完成，不会进入 dispatch）才新建。此前这里无条件
	// NewServerRetryBudget(0)→WaitBudgetMs=1，循环内并发排队/恢复等待的预算
	// 与 preflight/fallback 路径脱节，上限实际回落到各自策略默认。
	serverRetryBudget := gatewaypreauth.NewServerRetryBudget(0, c.clock)
	if preflight.DispatchContext != nil && preflight.DispatchContext.ServerRetryBudget != nil {
		serverRetryBudget = preflight.DispatchContext.ServerRetryBudget
	}

	// D-109（BUG-0175）客户端 IP 并发槽生命周期：Node
	// attachClientIpSlotRelease（routes.ts:2751-2756）把 release 以 once 语义
	// 挂到 res 的 finish/close；组切换时重新 attach（routes.ts:539/651/784）。
	// Go 的等价响应终态是 handler 返回——每个 DispatchContext 的 release 都
	// 收集进来，handler 退出时逐个调用（release 本身 once 幂等，槽不再泄漏
	// 到 180s TTL）。
	releases := &clientIPSlotReleaseList{}
	defer releases.ReleaseAll()

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
		releases:            releases,
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
	releases.Add(context.ReleaseClientIPConcurrency)
	// D-120（BUG-0175）SSE 等待心跳装配（preflight.ts:855-860）：等待预算在
	// BeginNoAvailableWait/PauseNoAvailableWait 边沿起停心跳，长等待期间向
	// 下游写 SSE 保活块，防止空闲超时断连。非 SSE 下游协议心跳为 nil
	//（SetWaitObserver(nil) 清除，与 Node setWaitObserver(undefined) 一致）。
	// 提交状态跨心跳与响应处理共享：心跳标记 transport committed 后，上游若
	// 返回非流式响应，由 finalizeNonStreamResponseAfterSseHeartbeat 收尾。
	loop.waitCommitState = &gatewayresponse.DownstreamCommitState{}
	loop.waitHeartbeat = gatewayresponse.CreateGatewaySseWaitHeartbeat(gatewayresponse.HeartbeatDeps{
		Res:                res,
		DownstreamProtocol: context.ClientStrategy.DownstreamProtocol,
		DownstreamCommit:   loop.waitCommitState,
		Signal:             ctx,
	})
	if loop.waitHeartbeat != nil {
		serverRetryBudget.SetWaitObserver(&gatewaypreauth.ServerRetryBudgetWaitObserver{
			OnWaitStarted: loop.waitHeartbeat.Start,
			OnWaitPaused:  loop.waitHeartbeat.Stop,
		})
	}
	defer loop.stopWaitHeartbeat()
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

// recordUpstreamFetchHeadersStage 是 handleUpstreamResponse 的
// upstream.fetch_headers 阶段埋点（对齐 Node upstream-attempts.ts:138）：
// 每次拿到上游响应头入库一条带引擎真实 AttemptIndex / AuditAttemptIndex 的
// stage。同一 attempt 恰一条 stage：失败派发器已入库的尝试（gateway 流量
// 非 2xx 终态标记 UpstreamStageRecorded）在此不再重复发射；索引字段的入库
// 折算（max(attemptIndex+1, auditAttemptIndex)）由共享记录路径
// chainRecordRequestStageToKernel 承担。
func (c *gatewayChain) recordUpstreamFetchHeadersStage(context *gatewaypreauth.DispatchContext, dispatched gatewaydispatch.UpstreamDispatchResult) {
	if dispatched.UpstreamStageRecorded {
		return
	}
	upstream := dispatched.Response
	if upstream == nil {
		return
	}
	c.observability.LogRequestStage("upstream.fetch_headers", map[string]any{
		"traceId":           context.UsageContext.TraceID,
		"accountId":         dispatched.Account.ID,
		"attemptIndex":      dispatched.AttemptIndex,
		"auditAttemptIndex": dispatched.AuditAttemptIndex,
		"statusCode":        upstream.Status(),
		"ok":                upstream.Status() >= http.StatusOK && upstream.Status() < http.StatusMultipleChoices,
	}, "success", time.UnixMilli(dispatched.AttemptStartedAt))
	if recorder := chainRequestStageRecorderOf(context.UsageContext.TraceID); recorder != nil {
		// W1a：按发生次数累计真实尝试数（RecordUpstreamAttempt 的索引折算
		// 已由上方 fields 驱动；此处计数兜底引擎未带出索引的旧构造路径）。
		recorder.RecordGatewayAttempt()
	}
}

// handleUpstreamResponse mirrors handleStreamUpstreamResponse /
// handleNonStreamUpstreamResponse + finalizeHandledUpstreamResponse. It
// returns the handling result so the dispatch loop can consume a
// RetryUpstream verdict (Node routes.ts:1899); the zero result means the
// request settled inside this method.
func (c *gatewayChain) handleUpstreamResponse(
	req *gatewaypreauth.GatewayRequest,
	res *gatewaypreauth.TrackingWriter,
	auditCapture gatewaypreauth.AuditCaptureContext,
	context *gatewaypreauth.DispatchContext,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	startedAt int64,
	settings gatewayruntimecache.GatewaySettings,
	budgets requestBudgets,
	commitState *gatewayresponse.DownstreamCommitState,
) gatewayresponse.UpstreamResponseHandlingResult {
	ctx := req.HTTP.Context()
	if commitState == nil {
		commitState = &gatewayresponse.DownstreamCommitState{}
	}
	upstream := dispatched.Response
	c.recordUpstreamFetchHeadersStage(context, dispatched)
	streamRequest := gatewaypreauth.IsOpenAIStreamRequest(req)
	// Node routes.ts:1550-1553: shouldHandleAsStream = upstreamResponse.ok &&
	// shouldHandle... . A complete non-2xx is already the terminal upstream
	// response; never interpret a missing/misleading SSE content type as a
	// stream and replace the provider error body (429/503 with an
	// text/event-stream content type) with a gateway event.
	handleAsStream := shouldHandleOpenAIUpstreamResponseAsStreamWithStatus(
		upstream.Status(), upstream.ContentType(), streamRequest)
	// D-97（BUG-0175）+ P2：上游响应模型归因。观察器已在 dispatch attempt 内、
	// 桥转换之前挂到原始上游流（engine.ObserveUpstreamResponseModel 钩子，
	// Node upstream-attempts.ts:180-210 观察先于 transform），归因的是上游
	// 原生模型（modelVersion / 上游 model），不是转换后的客户端形态。这里把
	// slot 的发布接入响应快照：发布发生在原始上游流 EOF / 提前关闭时——先于
	// 下游消费完成，finalization 读取字段时值已就位，与 Node getter 的惰性
	// 求值时序一致。
	responseSnapshot := &gatewayresponse.GatewayUpstreamResponse{
		Status: upstream.Status(),
		Header: upstream.Header,
	}
	dispatched.UpstreamResponseModelSlot.Bind(func(model string) {
		responseSnapshot.UpstreamResponseModel = model
	})
	responseSnapshot.Body = gatewayresponse.NewReaderUpstreamBody(ctx, upstream.Body)
	input := &gatewayresponse.HandleUpstreamResponseInput{
		Req:                        req,
		Downstream:                 gatewayresponse.StreamDownstream{Res: res},
		Account:                    gatewayresponse.OpenAIAccountView{Account: dispatched.Account},
		UpstreamResponse:           responseSnapshot,
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
		MarkFirstOutput:            firstOutputMetricMarkOf(dispatched.MarkFirstOutput, startedAt, req.MethodUpper()),
		DownstreamCommitState:      commitState,
		Deps: &gatewayresponse.FinalizationDeps{
			UsageRecords: chainFinalizationUsage{recorder: c.finalizationUsage},
			Logger:       gatewayResponseLogger{inner: slog.Default()},
			NowMs:        func() int64 { return c.preauth.NowMs() },
			// W4-B（BUG-0175）D-132 接线：响应检查的运行态副作用写侧
			//（avoid_account_ttl / avoid_upstream_bucket_ttl 跨请求落地）。
			AccountEffects: c.responseAccountEffects,
		},
	}
	if dispatched.ResponsePrecommitDeadlineAtMs != nil {
		input.ResponsePrecommitDeadlineAtMs = dispatched.ResponsePrecommitDeadlineAtMs
	}
	// 速度优先普通路由非流式首字截止（Node 响应面把 normalRouteFirstByteDeadline
	// / onFirstByteDeadline 传入 handleNonStreamUpstreamResponse）：尝试级软截止
	// 与决策回调透传给响应管道，R5 场景 chain 收到配置的 firstByteDeadlineMs。
	// superseded 通知绑定尝试协调器：原始字节推翻截止时释放切换预留。
	// 仅非流式装配（B2 复审修复）：流式 StreamPipe 的截止激活属于独立机制——
	// 其 abort 错误是 gatewayresponse 包内类型，不进下方 cutover verdict 臂，
	// 激活会造成「流式截止 → 固定 503 + 切换预留悬挂」；流式 cutover 补齐前
	// 保持流式不激活（对齐 HEAD 行为）。
	// B1 复审修复：EffectiveDeadlineMs 是相对 attemptStart 的时长
	// （gatewayrouting/firstbytedeadline.go DeadlineAtMs = attemptStartedAtMs +
	// effectiveDeadlineMs），竞速基准必须用 dispatched.AttemptStartedAt，
	// 不能用请求级 startedAt（attempt 前的 preflight/并发等待会被重复扣除，
	// 响应面截止相对 fetch 面提前）。
	if !handleAsStream &&
		dispatched.NormalRouteFirstByteDeadline != nil && dispatched.NormalRouteFirstByteDeadline.EffectiveDeadlineMs > 0 {
		deadlineMs := dispatched.NormalRouteFirstByteDeadline.EffectiveDeadlineMs
		input.FirstByteDeadlineMs = &deadlineMs
		input.DeadlineStartedAtMs = &dispatched.AttemptStartedAt
		if dispatched.OnFirstByteDeadline != nil {
			// engine 决策回调是 dispatch 版签名（单返回值）；适配为响应面
			// 的包内签名（error 返回对齐 handler throw 决策错误）。
			onDeadline := dispatched.OnFirstByteDeadline
			input.OnFirstByteDeadline = func(in gatewayresponse.FirstByteDeadlineInput) (gatewayresponse.FirstByteDeadlineAction, error) {
				action := onDeadline(gatewaydispatch.FirstByteDeadlineDecisionInput{
					ElapsedMs: in.ElapsedMs,
					TimeoutMs: in.TimeoutMs,
					Transport: in.Transport,
				})
				return gatewayresponse.FirstByteDeadlineAction(action), nil
			}
		}
		if dispatched.FirstByteDeadlineCoordinator != nil {
			input.OnFirstByteDeadlineSuperseded = dispatched.FirstByteDeadlineCoordinator.Supersede
		}
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
		// 速度优先非流式首字截止竞速的 configured_deadline abort（Node
		// routes.ts catch 响应段的 deadline 分支）：凭尝试协调器转移的切换
		// 预留转 cutover verdict，由 dispatch loop 的
		// settleSpeedFirstCutoverError 消费（收窄重派或耗尽退出），不落入
		// 下方固定 503。engine attempt loop 只覆盖 fetch 阶段错误，响应段
		// 只能由 chain 层接入。流式 body 阶段截止错误是 gatewayresponse 包
		// 内错误类型且当前未在 chain 激活流式 body 截止，不进本臂。
		var firstByteTimeoutErr *gatewaydispatch.GatewayFirstByteTimeoutError
		if errors.As(err, &firstByteTimeoutErr) &&
			firstByteTimeoutErr.Source == gatewaydispatch.FirstByteTimeoutSourceConfiguredDeadline &&
			dispatched.NormalRouteFirstByteDeadline != nil {
			var cutoverReservation any
			if dispatched.FirstByteDeadlineCoordinator != nil {
				cutoverReservation = dispatched.FirstByteDeadlineCoordinator.TransferForCutover()
			}
			return gatewayresponse.UpstreamResponseHandlingResult{
				FirstByteDeadlineCutover: true,
				CutoverReservationView:   cutoverReservation,
				ErrorCode:                firstByteTimeoutErr.Code(),
				Message:                  firstByteTimeoutErr.Message,
			}
		}
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
		return gatewayresponse.UpstreamResponseHandlingResult{}
	}
	if handling.RetryUpstream {
		// Node consumes the retry verdict before the finalize-usage path
		// (routes.ts:1899 vs finalizeHandledUpstreamResponse): a retrying
		// attempt records no completion usage.
		return handling
	}
	gatewayresponse.FinalizeHandledUpstreamResponse(*input, handling)
	return handling
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

// firstOutputMetricMarkOf wraps the dispatch first-output slot with the
// prometheus first-output histogram (D-190; Node routes.ts:1507
// recordGatewayFirstOutputMetric(Date.now() - requestContext.startedAt,
// requestContext.method)). The original slot always runs first; a nil slot
// keeps only the metric record.
func firstOutputMetricMarkOf(markFirstOutput func(), startedAt int64, method string) func() {
	return func() {
		if markFirstOutput != nil {
			markFirstOutput()
		}
		gatewayusage.RecordGatewayFirstOutputMetric(nowMillis()-startedAt, method)
	}
}

func nowMillis() int64 { return time.Now().UnixMilli() }

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

// timeoutProfileOf projects the runtime settings + lane onto the response
// timeout profile (mirrors the dispatch timeoutProfile the result carries;
// the response layer re-derives the budget values).
func timeoutProfileOf(settings gatewayruntimecache.GatewaySettings, lane string) gatewayresponse.TimeoutProfile {
	profile := gatewayrouting.GatewayTimeoutProfileForLane(gatewayrouting.GatewayTimeoutSettings{
		TextFirstResponseTimeoutSeconds:           settings.TextFirstResponseTimeoutSeconds,
		TextNonStreamFirstResponseTimeoutSeconds:  settings.TextNonStreamFirstResponseTimeoutSeconds,
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
		// G19 观测接线：budget precommit_clipped 裁剪观察（chain_obs_wiring.go
		// 进程级槽；未装配时 nil 保持既有无观察语义）。
	}, routingWallBudgetObserverOf())
	if err != nil {
		return requestBudgets{}, fmt.Errorf("create gateway request wall budget: %w", err)
	}
	tracker, err := gatewayrouting.NewGatewayRequestAttemptTracker(nil)
	if err != nil {
		return requestBudgets{}, fmt.Errorf("create gateway request attempt tracker: %w", err)
	}
	return requestBudgets{wall: wall, coordination: coordination, tracker: tracker}, nil
}
