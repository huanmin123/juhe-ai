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
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
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
	// finalizationPricing / finalizationSyncPricingAllowed 是完成尝试记录
	// （chainFinalizationUsage）的同步定价面注入：catalog 与 usageService 共
	// 享同一实例，gate 与 ServiceConfig.SyncPricingAllowed 同源
	// （chain_compose.go 的 chainSyncPricingAllowed）。
	finalizationPricing            gatewayusage.PricingCatalog
	finalizationSyncPricingAllowed bool
	// finalizationEnqueueFailures 是完成/失败尝试用量记录入队失败的进程级
	// 计数本体（每请求构造的 chainFinalizationUsage.enqueueFailures 指向它）：
	// 计数器放链根结构体上，采样（前 10 条逐条、之后每 100 条一条）跨请求
	// 累计，不随每请求构造的 chainFinalizationUsage 归零。
	finalizationEnqueueFailures int64
	auditSettings               gatewayusage.AuditLogSettingsSource
	auditDispatcher             gatewayusage.AuditDispatcher
	usageModelResolver          gatewayusage.UsageModelResolver
	// responseAccountEffects 是 W4-B（BUG-0175 D-132）的响应层账户副作用
	// 面（配置策略避让 / 桶避让写侧）；nil 保持 finalization 的缺席守卫。
	responseAccountEffects gatewayresponse.AccountFailureEffects
	// anthropicUsageHeaders 是 anthropic（Claude OAuth）unified rate limit
	// 响应头的成功面持久化窄口（AI账户Grok用量快照设计 §8.2；失败面在
	// chainFailureDispatcher.anthropicUsageHeaders）；nil 保持静默。
	anthropicUsageHeaders gatewaycodex.AnthropicUsageHeadersDispatcher
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
	// AI 问答会话绑定模式的调度覆盖目标（设计 §6）：仅聊天进程内执行器注入
	// AI 问答会话绑定账户的调度覆盖目标（账户唯一绑定设计 §5/§7）：仅聊天进程
	// 内执行器注入的绑定账户目标生效；外部 HTTP 请求恒为空，后续候选覆盖、
	// 分组上下文对齐与回退禁用全部短路，调度行为与现状一致。
	chatTarget, hasChatTarget := chatDispatchTargetFromContext(ctx)
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
		// 请求终态释放 in-flight 预算（Node releaseGatewayRequestBodyInFlightBytes
		// 挂 res 'finish'/'close'；Go 的等价终态是 handler 返回）：正常返回、
		// 提前 return 与 panic 展开（kernel recover 中间件兜底）都经过本 defer，
		// 通过 Capture 进入主链的请求不再泄漏预算。ReleaseInFlight 幂等
		// （InFlightLease.Release 自带 released 标志；已释放后 Lease 置 nil），
		// 413（chain_preflight rejectOversizedAutoDowngrade）等中途已释放的路径
		// 这里是 no-op，不会双重退还预算。
		defer bodyReq.ReleaseInFlight()
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
	// HTTP 完成观测 subject：链入口 defer 处 complete 一次；audit capture
	// （http_completed_at / http_duration_ms 的 flush 等待源）与 sink 失败
	// usage（CompletedAtMs）从同一 subject 读取完成时刻。经请求上下文传递
	// 给 sink 侧 Observe（对齐上方 D-119 runtime 快照的传递模式）。
	httpCompletion := newGatewayHTTPCompletion()
	if req.HTTP != nil {
		r = req.HTTP.WithContext(context.WithValue(req.HTTP.Context(), chainHTTPCompletionKey, httpCompletion))
		req.HTTP = r
	}
	auditCapture := c.newAuditCapture(req, traceID, startedAt, httpCompletion)
	// Node finally (routes.ts:2645): an un-finalized capture is canceled at
	// request end so its active-capture slot is recycled. Idempotent; the
	// failure paths below may cancel earlier.
	defer gatewaypreauth.CancelAuditCapture(auditCapture)
	// 完成信号在本 defer 注册（LIFO：先于上方 cancel defer 执行）——审计
	// flush 与 sink 失败记录先拿到真实完成时刻，再回收 capture 活跃槽。
	// complete 幂等，对 Finalize/Cancel 已收尾的路径无副作用；startedAt 取
	// c.preauth.NowMs()（UnixMilli），此处直接取 wall clock 同域绝对毫秒。
	defer httpCompletion.complete(time.Now().UnixMilli())

	// ---- preflight (request/preflight.ts) ----
	preflightOptions := c.preflightOptions(requestLane)
	var chatScope chatDispatchTargetScope
	if hasChatTarget {
		// 派发候选装配前收敛到绑定作用域（group 固定候选组 / account 按启用
		// 分组解析承载分组后收敛单账户）；解析失败按 preflight 缓存读失败同一
		// 处理面退出，不脱离绑定作用域派发。
		scope, scopeErr := c.resolveChatDispatchTargetScope(ctx, req, chatTarget)
		if scopeErr != nil {
			c.handleOrchestratorError(scopeErr, req, res, startedAt, endpoint)
			return
		}
		chatScope = scope
		preflightOptions.CandidateAccounts = chatScope.accounts
		// 可靠性批次2（缺陷5）：注入候选的可恢复等待作用域——绑定作用域候选
		// 全部临时不可用时 preflight 按该作用域等待恢复，而不是直接终态失败。
		preflightOptions.RecoverableCandidateScope = chatScope.recoverableScope
	}
	preflight, err := c.preauth.PrepareOpenAIGatewayDispatchContext(ctx, gatewaypreauth.PreflightInput{
		Req:             req,
		Res:             res,
		AuditCapture:    auditCapture,
		Options:         preflightOptions,
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
	// serverRetryBudget 与 budgets 先以自建实例装配（与 preflight 未装配
	// DispatchContext 的兜底同值）；loop.current 换代点（初始 resolveRouteAction
	// 产物 + dispatch 期间 switchToFallbackGroup）统一经
	// adoptDispatchContextBudgets 回收 preflight 构造/更新后的请求级实例——
	// codex 压缩请求在 preflight 内对 wall budget 调用 WithoutLimit()（Unbounded
	// 实例），RouteAction→fallback 与切组换代同样携带，统一收口避免各路径
	// 漏回收（对齐 Node routes.ts 主循环始终引用 currentPreflight 携带的
	// budget 实例）。
	serverRetryBudget := gatewaypreauth.NewServerRetryBudget(0, c.clock)

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
		chatTarget:          chatTarget,
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
	if hasChatTarget {
		// 窗口级分组上下文对齐到生效分组（group=绑定分组；account=承载分组，
		// 即候选解析的命中组）：调度策略 + usage/审计窗口组；绑定目标请求的
		// 既有 preflight.completed 埋点随之反映真实窗口组。
		if err := c.applyChatDispatchGroupContext(ctx, chatDispatchSystemAccountID(req), chatScope.groupID, context); err != nil {
			c.handleOrchestratorError(err, req, res, startedAt, endpoint)
			return
		}
	}
	// 换代点统一回收（初始 preflight 直接产出与 RouteAction→fallback 产物都在
	// 这里进入主循环）：compaction 的 Unbounded wall budget 等请求级实例随
	// context 生效，coordination 构造据此重导出 TimeoutPolicy。
	loop.adoptDispatchContextBudgets(context)
	// D-120（BUG-0175）SSE 等待心跳装配（preflight.ts:855-860）：等待预算在
	// BeginNoAvailableWait/PauseNoAvailableWait 边沿起停心跳，长等待期间向
	// 下游写 SSE 保活块，防止空闲超时断连。非 SSE 下游协议心跳为 nil
	//（SetWaitObserver(nil) 清除，与 Node setWaitObserver(undefined) 一致）。
	// observer 必须挂在 adopt 后的最终实例上：初始 preflight 恒构造新
	// ServerRetryBudget（编排入口 options 不带预算），adopt 必然替换自建实例，
	// 挂载点在 adopt 之后才不会落在被丢弃的实例上（切组换代经 options 同源
	// 复用实例，observer 随之保持有效）。提交状态跨心跳与响应处理共享：
	// 心跳标记 transport committed 后，上游若返回非流式响应，由
	// finalizeNonStreamResponseAfterSseHeartbeat 收尾。
	loop.waitCommitState = &gatewayresponse.DownstreamCommitState{}
	loop.waitHeartbeat = gatewayresponse.CreateGatewaySseWaitHeartbeat(gatewayresponse.HeartbeatDeps{
		Res:                res,
		DownstreamProtocol: context.ClientStrategy.DownstreamProtocol,
		DownstreamCommit:   loop.waitCommitState,
		Signal:             ctx,
	})
	if loop.waitHeartbeat != nil && !shouldKeepCompactSseAliveDuringUpstreamWait(req, context) {
		// 预算等待心跳只在非压缩保活场景挂载（复审竞态修复）：压缩场景的
		// compact 保活心跳覆盖整个 fetch 窗口（含等待期，保活块语义更精确），
		// 是预算心跳等待期职责的超集；两个心跳 goroutine 并发写同一
		// TrackingWriter / DownstreamCommitState 在 Go 下是真实 data race
		//（Node 靠单线程事件循环天然串行），收敛为单实例。
		loop.serverRetryBudget.SetWaitObserver(&gatewaypreauth.ServerRetryBudgetWaitObserver{
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

// persistAnthropicUsageHeadersOnGatewayTraffic 是 anthropic 用量头成功面挂载
// 的可测门：仅网关流量持久化 unified rate limit 头，其余流量静默；账户资格
// 与头存在性门在 gatewaycodex.PersistAnthropicUsageHeadersIfNeeded 内。
func persistAnthropicUsageHeadersOnGatewayTraffic(
	ctx context.Context,
	trafficSource string,
	account gatewaydispatch.AccountCandidate,
	headers http.Header,
	dispatcher gatewaycodex.AnthropicUsageHeadersDispatcher,
) {
	if trafficSource != gatewayTrafficSource {
		return
	}
	gatewaycodex.PersistAnthropicUsageHeadersIfNeeded(ctx, account, headers,
		gatewaycodex.AnthropicUsageSnapshotSource, dispatcher)
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
	// Node routes.ts:1554-1556 的 Go 成功面对称位（到达响应管道的 2xx 终态）：
	// anthropic OAuth 账户的 unified rate limit 头在此 fire-and-forget 持久化
	// （AI账户Grok用量快照设计 §8.2；非 2xx 终态走 chain_ports.go 失败面；
	// source 按规格固定 anthropic_unified_headers，不分成功/失败来源）。
	// nil 派发器与不合格账户在 helper 内静默。
	persistAnthropicUsageHeadersOnGatewayTraffic(ctx, context.UsageContext.TrafficSource,
		dispatched.Account, responseHTTPHeaderOf(upstream), c.anthropicUsageHeaders)
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
	// 响应层超时锚点对齐 Node routes.ts:1574/:1604 的 attemptStartedAt
	// （从 dispatch 结果解构）：首字/语义结果/生命周期预算按每个 attempt 的
	// 开始时刻独立计量，不共享请求级起点——否则第一个账户耗掉的时间直接从
	// 后续账户的 120s 首字预算里扣除，换号重试的流一建立就预算耗尽秒败。
	// 首字统计口径同源 attempt 级。零值（engine 未带出）回退请求级 startedAt。
	// Prometheus 首字直方图例外（MarkFirstOutput 仍传请求级 startedAt）：
	// Node routes.ts:1507-1508 recordGatewayFirstOutputMetric 以请求入口
	// requestContext.startedAt 为锚，attempt 级只用于 :1509 markFirstByte。
	attemptStartedAt := dispatched.AttemptStartedAt
	if attemptStartedAt == 0 {
		attemptStartedAt = startedAt
	}
	input := &gatewayresponse.HandleUpstreamResponseInput{
		Req:                        req,
		Downstream:                 gatewayresponse.StreamDownstream{Res: res},
		Account:                    gatewayresponse.OpenAIAccountView{Account: dispatched.Account},
		UpstreamResponse:           responseSnapshot,
		UpstreamURL:                dispatched.UpstreamURL,
		AuditAttemptID:             dispatched.AuditAttemptID,
		AuditCapture:               responseAuditCaptureOf(auditCapture),
		Settings:                   settings,
		TimeoutProfile:             responseTimeoutProfileOf(dispatched, settings, string(context.RequestLane)),
		UsageContext:               context.UsageContext,
		StartedAtMs:                attemptStartedAt,
		Signal:                     ctx,
		SessionAffinityKey:         context.SessionAffinityKey,
		ClientStrategy:             clientStrategyViewOf(context),
		ResponseInspectionPolicies: context.ResponseInspectionPolicies,
		MarkFirstOutput:            firstOutputMetricMarkOf(dispatched.MarkFirstOutput, startedAt, req.MethodUpper()),
		DownstreamCommitState:      commitState,
		Deps: &gatewayresponse.FinalizationDeps{
			UsageRecords: &chainFinalizationUsage{
				recorder:           c.finalizationUsage,
				pricing:            c.finalizationPricing,
				syncPricingAllowed: c.finalizationSyncPricingAllowed,
				enqueueFailures:    &c.finalizationEnqueueFailures,
				// 请求模型、请求侧 endpoint family 与模型解析器按请求注入：
				// finalize 侧映射记账必须与派发链改写同源（requestedModel 供
				// 失败行使用，完成行自带 RequestedModel）。
				requestedModel:       requestModelHintOf(req),
				sourceEndpointFamily: requestMappingSourceFamilyOf(req),
				models:               c.usageModelResolver,
			},
			Logger: gatewayResponseLogger{inner: slog.Default()},
			NowMs:  func() int64 { return c.preauth.NowMs() },
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
		// 非 cutover 响应管道错误兜底补关联线索：流式臂的管道层已有
		// gateway_stream_pipe_error warn；非流式臂与流式提前返回路径
		// （StreamBeforeDownstreamCommitError）无管道层 warn，本条 Debug
		// 即链面唯一线索。带 traceId 与错误原文，供链面 trace 关联；
		// 下方固定 503 契约不变。
		slog.Debug("上游响应管道处理失败，进入固定 503 兜底",
			"event", "gateway_upstream_response_pipeline_failed",
			"traceId", context.UsageContext.TraceID,
			"error", err.Error())
		if !res.HeadersSent() && !commitState.TransportCommitted {
			gatewaypreauth.SendGatewayJSONError(res, http.StatusServiceUnavailable,
				gatewaypreauth.GatewayErrorPayloadOf("上游暂时不可用，请重试", "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode),
				gatewaypreauth.SendGatewayErrorOptions{Protocol: clientErrorProtocol(req)})
		}
		// BUG-0267：管线错误兜底 503 已渲染即终态（AlreadyFinalized 语义），且
		// 无上游传输归因（GatewayLocalFailure）——对齐 Node 响应处理 throw 路径
		// 的 neutral 结算（routes.ts:1667-1673 reportUnknown/reportUnknown +
		// recordTerminal unknown/request_lifecycle），否则 post-verdict 结算块把
		// 零值 handling 误分类为"完整转发"（circuit framing-complete 治愈证据）。
		return gatewayresponse.UpstreamResponseHandlingResult{AlreadyFinalized: true, GatewayLocalFailure: true}
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
	// invented copy. 5xx 必须自带根因：入口编排错误（body Capture /
	// newRequestBudgets / resolveRouteAction / applyChatDispatchGroupContext
	// 等分支共用本出口）在此补进程 Error 日志，并经 WriteErrorCause 把根因
	// 记入 kernel 完成日志 failureReason——该 helper 记录根因后仍复用
	// WriteError 构造响应，客户端响应体逐字节不变
	//（internal/kernel/envelope.go:102-107）。
	orchestratorTraceID := ""
	if req.HTTP != nil {
		orchestratorTraceID = kernel.Context(req.HTTP).TraceID
	}
	slog.Error("网关编排入口处理失败，已返回 500",
		"event", "gateway_orchestrator_entry_failed",
		"traceId", orchestratorTraceID,
		"endpoint", endpoint,
		"error", message)
	if req.HTTP != nil {
		kernel.WriteErrorCause(req.HTTP, res, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
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

// responseTimeoutProfileOf 对齐 Node 契约：响应层原样使用 dispatch 结果携带
// 的 timeout profile（upstream-dispatch.ts 的结果字段 → routes.ts 直传
// handleStreamUpstreamResponse）——压缩等已禁超时的 profile 在 dispatch 层
// 判定后原样生效，响应层不得用 settings 重建覆盖。dispatched.TimeoutProfile
// 为零值（引擎未带出，如构造点之外的旧路径）时回退 settings 派生（与
// UpstreamDispatchResult.AttemptIndex 零值兜底的既有模式一致）。
func responseTimeoutProfileOf(dispatched gatewaydispatch.UpstreamDispatchResult, settings gatewayruntimecache.GatewaySettings, lane string) gatewayresponse.TimeoutProfile {
	profile := dispatched.TimeoutProfile
	if profile.FirstResponseTimeoutMs == 0 && profile.IdleTimeoutMs == 0 &&
		profile.UncommittedAttemptMaxLifetimeMs == 0 && !profile.TimeoutsDisabled {
		return timeoutProfileOf(settings, lane)
	}
	return dispatchTimeoutProfileOf(profile)
}

// dispatchTimeoutProfileOf 把 dispatch 层的 GatewayTimeoutProfile 投影到响应
// 层消费的字段子集（与 timeoutProfileOf 的手工映射同语义，只映射
// gatewayresponse.TimeoutProfile 的字段集）。
func dispatchTimeoutProfileOf(profile gatewayrouting.GatewayTimeoutProfile) gatewayresponse.TimeoutProfile {
	return gatewayresponse.TimeoutProfile{
		FirstResponseTimeoutMs:          profile.FirstResponseTimeoutMs,
		IdleTimeoutMs:                   profile.IdleTimeoutMs,
		UncommittedAttemptMaxLifetimeMs: profile.UncommittedAttemptMaxLifetimeMs,
		TimeoutsDisabled:                profile.TimeoutsDisabled,
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
// clientStrategyViewOf 把 preflight 冻结的 G18 客户端策略上下文投影到响应层
// 消费面（Node routes.ts 的 clientStrategy 输入，finalization 侧按同名字段消费）。
// 投影源是 strategy.Opaque 携带的完整 gatewaycodex 上下文（G18 冻结快照，
// chain_v1_streamretry.go 的 clientStrategyPreCommitFailureSignal 同模式）；
// Opaque 缺失（测试 mock / 无策略装配）时仅按 ClientProfile 判定解释门，
// 压缩期望与预提交重试信号保持 false（fail-closed）。
func clientStrategyViewOf(context *gatewaypreauth.DispatchContext) *gatewayresponse.ClientStrategyView {
	strategy := context.ClientStrategy
	view := &gatewayresponse.ClientStrategyView{
		ClientProfile:      strategy.ClientProfile,
		DownstreamProtocol: strategy.DownstreamProtocol,
		// InterpretSemantics 对齐 gatewayClientAllowsUpstreamSemanticInterpretation
		//（client-profiles/strategy.ts）：仅 codex / claude_code / gemini_cli 三画像
		// 允许上游响应语义解释，普通 openai 兼容客户端不解释。
		InterpretSemantics: gatewaycodex.GatewayClientAllowsUpstreamSemanticInterpretation(
			gatewaycodex.OpenAIGatewayClientStrategyContext{ClientProfile: strategy.ClientProfile}),
	}
	resolved, ok := strategy.Opaque.(gatewaycodex.OpenAIGatewayClientStrategyContext)
	if !ok {
		return view
	}
	view.CodexCompactionExpected = resolved.CodexCompactionExpected
	view.AllowClientSourceAccountAvoidance = resolved.AllowClientSourceAccountAvoidance
	view.RetryPreCommitProtocolError = resolved.RetryCoordination.PreCommitFailureSignal == gatewaycodex.FailureSignalProtocolErrorEvent
	return view
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
// 2026-09-25：优先取内核 RequestContext 的 ClientIP（trust-proxy XFF 解析，
// 与审计/管理面同源）；preauth.ExtractClientIP 兜底只看套接字地址，在
// Edge/Caddy/Traefik 之后是内部跳板地址而非客户端（生产用量/审计实证）。
func requestClientIP(req *gatewaypreauth.GatewayRequest) string {
	if req.HTTP != nil {
		if ctx := kernel.Context(req.HTTP); ctx != nil && ctx.ClientIP != "" {
			return ctx.ClientIP
		}
	}
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
