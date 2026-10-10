package main

// v1DispatchLoop 族：/v1 请求的派发循环核心（Node routes.ts second half）——
// dispatch while(true) 循环、调度错误结算与终端渲染、route-action /
// api-key 分组回退切换、请求级耗尽集与 client-IP 并发槽释放收集。
// 自 chain_v1.go 按职责拆出（REFACTOR-0007 文件内拆分）；被移动的函数体
// 逐字节保持。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

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
	// exhaustedAccounts is the request-level exhausted set (Node
	// exhaustedAccountIds, routes.ts:568): every non-recoverable failed
	// account of an UpstreamAttemptError enters it, and switchToFallbackGroup
	// hands it to the fallback candidate window as excludedAccountIds
	// (routes.ts:625).
	exhaustedAccounts map[string]struct{}
	// streamRetryExcludedAccounts is the per-group stream server-retry
	// exclusion set (Node streamServerRetryExcludedAccountIds, routes.ts:540):
	// accounts the response layer's RetryUpstream verdict asked to avoid.
	// switchToFallbackGroup resets it on a group switch (routes.ts:652).
	streamRetryExcludedAccounts map[string]struct{}
	// streamServerRetryCount mirrors Node streamServerRetryCount (routes.ts:
	// 541) for the stream_server_retry_dispatch audit metadata.
	streamServerRetryCount int
	// lastPreCommitSwitchSignature（BUG-0298）记录上一次"检查拦截命中且加密
	// 上下文信号未命中"换号的上游失败签名（错误码+文案）：连续两次签名一致
	// 判定同源（同一上游拒绝形状），停止级联候选，走既有耗尽契约。仅覆盖
	// ResponseInspection 非 nil 的换号 verdict；检查未命中换号（无决策对象可
	// 签名）与恢复臂重放（专用通道）不受影响。切组时不重置本签名——这是裁决
	// 而非遗漏：触发跨组即停需两组的首末失败逐字节同码同文案，对另一账户池
	// 仍是系统性上游故障的较强证据；勿按与 streamRetryExcludedAccounts 的
	// 对称性在此补重置。
	lastPreCommitSwitchSignature string
	// compactWaitHeartbeat 是 codex 压缩 SSE 请求在上游派发等待期的专属保活
	// 心跳（Node activeCompactSseWaitHeartbeat，routes.ts:1229-1239：10s 间隔
	// 写出 compaction 保活块，防客户端/中间层在压缩长等待期空闲断连）。
	// 每轮 fetch 前创建 Start、fetch 返回即停；nil = 非压缩等待保活场景。
	compactWaitHeartbeat *gatewayresponse.GatewaySseWaitHeartbeat
	// compactWaitKeepalive 是压缩等待保活判定的准备层预计算布尔（调度内核
	// 通用化设计 5.6）：preflight 上下文就绪后由 compactWaitKeepaliveOf 一次
	// 判定，预算等待心跳排除（handler 装配点）与每轮压缩保活挂载
	//（startCompactSseWaitHeartbeat）只消费该布尔，不再每轮重投影画像条件。
	// 判定输入（请求流式性 + codex 画像 + compaction 期望 + responses_sse
	// 协议）均为请求级冻结量，组回退换代重解析同一请求不改变取值。
	compactWaitKeepalive bool
	// forceRecoverableFailureWait 镜像 Node routes.ts:566/1483：切组未成但仍有
	// 可恢复账户且等待预算未到 recoverable_later 交接点时置位——下一轮引擎
	// 调用强制进入可恢复等待（优先等即将恢复的账户，保住亲和）。
	forceRecoverableFailureWait bool
	// ---- W4-B（BUG-0175 D-114）speed-first per-request 状态
	//（routes.ts:542-546 locals）----
	// speedFirstByteRetryCount 是本请求已执行的速度优先切号次数（上限
	// maxFirstByteRetriesPerRequest）；组切换时清零（routes.ts:656/787）。
	speedFirstByteRetryCount int
	// speedFirstRetryCandidateAccountIds 非空时把候选窗口收窄到保留目标
	//（routes.ts:921-923）。
	speedFirstRetryCandidateAccountIds map[string]struct{}
	// speedFirstCutoverReservation 携带跨次派发的切换预留（目标并发槽）；
	// 每次派发前取走（routes.ts:1103-1104）。
	speedFirstCutoverReservation *gatewayhotquality.SpeedFirstCutoverReservation
	// speedFirstAttachedViews 记录已 attach 到引擎协调器的视图 → 具体预留
	// 映射：cutover 错误把视图带回循环时据此恢复 TakeForAccount 能力。
	speedFirstAttachedViews map[*gatewaydispatch.SpeedFirstCutoverReservationView]*gatewayhotquality.SpeedFirstCutoverReservation
	// speedFirstSlowObservedForAttempt 记录本次尝试的首字慢观察（
	// routes.ts:1109 闭包写入、2395 响应观测读取，避免重复记录）。
	speedFirstSlowObservedForAttempt *gatewayproxyhealth.LatencySlowResult
	// speedFirstTotalTimeSlowObservedForAttempt 是总时间维度的同 attempt 去重
	// 标记（设计 6.3：每 attempt 每维度恰好一次；与首字标记按维度独立，互不
	// 吞并）。写入点：transport 总时间决策闭包、响应轮总时间软观察 timer、
	// 完成观测补记；清零点：每轮 fetch 前、组切换 resetSpeedFirstState。
	speedFirstTotalTimeSlowObservedForAttempt *gatewayproxyhealth.LatencySlowResult
	// speedFirstTotalTimeObservationMu 串行化总时间去重标记的读改写：决策
	// 闭包（transport timer goroutine）与响应轮软观察 timer goroutine、完成
	// 观测（主循环）三个写入点并发可达，双记即双慢样本。
	speedFirstTotalTimeObservationMu sync.Mutex
	// speedFirstTotalTimeCutoverSignal 是总时间决策闭包确认切号后的载荷槽
	//（timer goroutine 写、settleDispatchError 主循环读；Abort 错误类型不携
	// 带账户信息——transport 构造点只有 handler 返回值，账户快照留在闭包）。
	speedFirstTotalTimeCutoverSignal *speedFirstTotalTimeCutoverSignal
	// releases 收集每个 DispatchContext 的 client-IP 并发槽释放闭包
	//（D-109；Node attachClientIpSlotRelease 在组切换时重新 attach）。
	releases *clientIPSlotReleaseList
	// waitCommitState / waitHeartbeat 是 D-120 SSE 等待心跳的请求级共享状态：
	// 心跳写出的 transport-commit 标记必须与响应处理看到的是同一个实例。
	waitCommitState *gatewayresponse.DownstreamCommitState
	waitHeartbeat   *gatewayresponse.GatewaySseWaitHeartbeat
	// chatTarget 是 AI 问答会话绑定模式的调度覆盖目标（设计 §6；仅聊天进程内
	// 执行器注入）。非零时禁用 API Key 分组回退切换，绑定会话永不逃逸出绑定
	// 作用域，耗尽走既有"无可用账户"终态；外部请求恒为零值，行为不变。
	chatTarget chatDispatchTarget
	// roundStartedAtMs 是本轮上游派发的起点毫秒（每轮 FetchFirstAvailableUpstream
	// 调用前刷新）：upstream.dispatch.failed 耗尽埋点的阶段起点口径仍含引擎内
	// 候选/排队，但远小于整请求 startedAt 口径；0 表示未记录（回落 startedAt）。
	roundStartedAtMs int64
	// pendingCodexRecovery 携带 BUG-0289 流内加密上下文恢复的一次性同账户
	// 重放载荷：settleResponseStreamServerRetry 恢复臂写入，下一轮
	// newRequestCoordination 注入 RequestBodyOverride / SameAccountRetry /
	// SemanticRetryID 后一次性清空。nil = 无待重放。
	pendingCodexRecovery *v1PendingCodexEncryptedContentRecovery
}

// v1PendingCodexEncryptedContentRecovery 是恢复重放载荷（BUG-0289）。AccountID
// 取当前 dispatched.Account.ID；Body 是响应层基于引擎实际发送体清理后的重放体
// （已过模型映射，RequestBodyOverride 注入点在其后、不会二次映射，见
// dispatchsingle.go:397-400）；SemanticRetryID 作请求级 attempt 去重键。
type v1PendingCodexEncryptedContentRecovery struct {
	AccountID       string
	Body            []byte
	SemanticRetryID string
	Metadata        *gatewaycodex.CodexEncryptedContentRecoveryMetadata
	Signal          string
}

// v1FallbackSwitch mirrors the switchToFallbackGroup return union
// (routes.ts:570-572).
type v1FallbackSwitch string

const (
	v1FallbackNone      v1FallbackSwitch = "none"
	v1FallbackSwitched  v1FallbackSwitch = "switched"
	v1FallbackCompleted v1FallbackSwitch = "completed"
)

// stopWaitHeartbeat 终止 D-120 等待心跳：请求 handler 返回即下游终态
// （Node 由 res 的 close/error 监听承载），防止心跳 goroutine 泄漏或在
// 响应结束后继续写出。
func (l *v1DispatchLoop) stopWaitHeartbeat() {
	if l != nil && l.waitHeartbeat != nil {
		l.waitHeartbeat.Stop()
	}
}

// newRequestCoordination 构造每轮派发的引擎协调上下文（Node routes.ts 主循环
// 顶部的 coordination 装配）。超时豁免与总时间档位是链面预算好的参数（调度
// 内核通用化设计 5.2 三轨合一）：压缩豁免以 preflight 单点判定经 wall budget
// Unbounded 承载（compaction 请求在 preflight 内 WithoutLimit），coordination
// 据此携带 TimeoutsDisabled / TotalTimeLane，引擎不再感知"压缩"概念、不做
// 请求形状扫描；其余请求保持零值（normal 档、不豁免）。
func (l *v1DispatchLoop) newRequestCoordination() *gatewaydispatch.RequestCoordinationContext {
	coordination := &gatewaydispatch.RequestCoordinationContext{
		Scope:                    gatewaydispatch.CoordinationScopeGatewayRequest,
		ServerRetryBudget:        l.serverRetryBudget,
		GatewayRequestWallBudget: l.budgets.wall,
		RouteCoordinationBudget:  l.budgets.coordination,
		RequestAttemptTracker:    l.budgets.tracker,
	}
	coordination.TimeoutsDisabled = l.budgets.wall != nil && l.budgets.wall.Unbounded
	coordination.TotalTimeLane = speedFirstTotalTimeLaneOf(l.budgets.wall, l.req)
	// BUG-0289 一次性消费：待重放的加密上下文清理体钉回同账户。钉住与去重
	// 走两个正交通道：SameAccountRetry 把候选窗口塌缩到该账户并放行注册
	// 预检（upstreamdispatch.go:536-538/:591-593），RetryID 必须留空——
	// SameAccountRetryID 注册通道与 SemanticRetryID 互斥（routecoordination.go
	// :1076 mode conflict），且同账户重试模式还要求引擎内预留（:1084 not
	// registered）；去重经 SemanticRetryID 通道（dispatchsingle.go:754 →
	// CanAttemptAccount :897-906，(SemanticRetryID, accountRuntimeKey,
	// physicalCredentialKey) 首次出现即放行——首次尝试未携带语义 ID）。
	// RetryID 留空还使引擎 activeSameAccountRetryID 为空，API Key 选择按
	// SameAccountRetry!=nil 语义剥掉旧指纹重选（dispatchsingle.go:291-302，
	// 排除集按引擎调用重置，同 Key 可再选）。override 体注入在模型映射之后
	// （dispatchsingle.go:398-400，不再二次映射）。注入后立即清空，后续轮次
	// （含组切换）不再携带。
	if l.pendingCodexRecovery != nil {
		pending := l.pendingCodexRecovery
		l.pendingCodexRecovery = nil
		coordination.SemanticRetryID = pending.SemanticRetryID
		coordination.RequestBodyOverride = &gatewaydispatch.RequestBodyOverride{
			AccountID: pending.AccountID,
			Body:      pending.Body,
		}
		sameAccount := gatewaydispatch.AccountCandidate{}
		if l.current != nil {
			for _, account := range l.current.Accounts {
				if account.ID == pending.AccountID {
					sameAccount = account
					break
				}
			}
		}
		if sameAccount.ID != "" {
			coordination.SameAccountRetry = &gatewaydispatch.SameAccountRetry{
				// RetryID 留空：见上方通道互斥注释。
				RetryID: "",
				Account: sameAccount,
			}
		}
	}
	return coordination
}

// engineSchedulingExclusionsOf 把 preauth 端口镜像的通用调度排除集投影为引擎
// 入参形状（调度内核通用化设计 5.1；两包镜像类型逐字段复制）。nil 保持 nil。
func engineSchedulingExclusionsOf(exclusions *gatewaypreauth.SchedulingExclusions) *gatewaydispatch.SchedulingExclusions {
	if exclusions == nil {
		return nil
	}
	return &gatewaydispatch.SchedulingExclusions{
		ExcludedAccountIDs: append([]string(nil), exclusions.ExcludedAccountIDs...),
	}
}

// engineDispatchSegmentsOf 把 preauth 端口镜像的分派段列表投影为引擎入参形状
// （逐字段复制；AccountCandidate 两侧同为 gatewayruntimecache.OpenAIAccountSecret
// 别名，切片只读复用）。空列表保持 nil，引擎按未携带段元数据合成单一活动段。
func engineDispatchSegmentsOf(segments []gatewaypreauth.DispatchSegment) []gatewaydispatch.DispatchSegment {
	if len(segments) == 0 {
		return nil
	}
	out := make([]gatewaydispatch.DispatchSegment, 0, len(segments))
	for _, segment := range segments {
		out = append(out, gatewaydispatch.DispatchSegment{
			OpaqueSegmentID: segment.OpaqueSegmentID,
			Tier: gatewaydispatch.DispatchPriorityTier{
				ModelRank:    segment.Tier.ModelRank,
				FallbackRank: segment.Tier.FallbackRank,
				SuperRank:    segment.Tier.SuperRank,
				Priority:     segment.Tier.Priority,
			},
			Accounts: segment.Accounts,
		})
	}
	return out
}

// compactWaitKeepaliveOf 对齐 Node routes.ts:2939-2948
// shouldKeepCodexCompactSseAliveDuringUpstreamWait：流式 codex 压缩请求（G18
// 上下文 codexCompactionExpected）且下游 responses_sse。这是保活判定的唯一
// 推导点（调度内核通用化设计 5.6）：preflight 上下文就绪后调用一次写入
// loop.compactWaitKeepalive，链面消费点只读该预计算布尔——三要素均为请求级
// 冻结量（画像/压缩期望/协议来自 ClientStrategy 对同一请求的冻结解析，组回退
// 换代重解析不改变取值），单次判定与既有每轮重判行为等价。
func compactWaitKeepaliveOf(req *gatewaypreauth.GatewayRequest, context *gatewaypreauth.DispatchContext) bool {
	view := clientStrategyViewOf(context)
	return gatewaypreauth.RequestStream(req) &&
		view.ClientProfile == "codex" && view.CodexCompactionExpected &&
		view.DownstreamProtocol == "responses_sse"
}

// startCompactSseWaitHeartbeat 对齐 Node routes.ts:1229-1239：满足挂载条件时
// 挂 10s 间隔的 compaction 保活心跳并立即 Start（首个保活块即刻写出）。
// 提交状态与预算心跳共享 loop.waitCommitState 单实例（语义已提交后心跳自动
// 停写）。挂载门是准备层预计算布尔 loop.compactWaitKeepalive（设计 5.6）。
func (l *v1DispatchLoop) startCompactSseWaitHeartbeat(ctx context.Context, current *gatewaypreauth.DispatchContext) {
	if !l.compactWaitKeepalive {
		return
	}
	heartbeat := gatewayresponse.CreateGatewaySseWaitHeartbeat(gatewayresponse.HeartbeatDeps{
		Res:                          l.res,
		DownstreamProtocol:           clientStrategyViewOf(current).DownstreamProtocol,
		DownstreamCommit:             l.waitCommitState,
		Signal:                       ctx,
		IntervalMs:                   10_000,
		EmitCodexCompactionKeepalive: true,
	})
	if heartbeat == nil {
		return
	}
	l.compactWaitHeartbeat = heartbeat
	heartbeat.Start()
}

// stopCompactSseWaitHeartbeat 停止本轮压缩等待保活（Stop 幂等）：fetch 返回
// 即上游等待期结束，真实流转发由响应管道接管；请求终态由 run 收尾兜底。
func (l *v1DispatchLoop) stopCompactSseWaitHeartbeat() {
	if l.compactWaitHeartbeat != nil {
		l.compactWaitHeartbeat.Stop()
		l.compactWaitHeartbeat = nil
	}
}

// waitForRecoverableFailures 镜像 Node routes.ts:1281-1283：单分组（无备用
// 可切）、切组跳数已达分组上限、或强制等待置位（账户即将恢复且预算未到
// recoverable_later 交接点）时，引擎在本轮内等待可恢复失败；多分组未切完
// 的默认是 false——引擎立即上抛，链面先切备用分组（横向有健康账户时明显
// 优于干等当前组恢复）。此前恒 true，多分组请求在备用分组健康时也要干等
// 到当前组预算交接才切组。
func (l *v1DispatchLoop) waitForRecoverableFailures(current *gatewaypreauth.DispatchContext) bool {
	bindings := 0
	if current.APIKeyRecord != nil {
		bindings = len(current.APIKeyRecord.GroupBindings)
	}
	return l.forceRecoverableFailureWait ||
		bindings <= 1 ||
		l.fallbackSwitches >= bindings-1
}

// adoptDispatchContextBudgets 在 loop.current 换代点（初始 resolveRouteAction
// 产物 + dispatch 期间 switchToFallbackGroup 切组）回收 preflight 构造/更新后
// 的请求级预算实例（对齐 Node routes.ts 主循环始终引用 currentPreflight 携带
// 的 budget）：RouteAction→fallback 与切组路径的 fallback preflight 同样会在
// compaction 分支产出 WithoutLimit 的 Unbounded wall budget，统一在此收口，
// 防止 coordination 继续引用换代前的自建/旧实例。字段为 nil 时保持原值
// （preflight 保证恒填，防御保持兜底实例）。
func (l *v1DispatchLoop) adoptDispatchContextBudgets(context *gatewaypreauth.DispatchContext) {
	if context == nil {
		return
	}
	if context.ServerRetryBudget != nil {
		l.serverRetryBudget = context.ServerRetryBudget
	}
	if context.GatewayRequestWallBudget != nil {
		l.budgets.wall = context.GatewayRequestWallBudget
	}
	if context.RouteCoordinationBudget != nil {
		l.budgets.coordination = context.RouteCoordinationBudget
	}
	if context.RequestAttemptTracker != nil {
		l.budgets.tracker = context.RequestAttemptTracker
	}
}

// run mirrors the Node while(true) dispatch loop: fetch the first available
// upstream for the current group context and hand the response to the
// response layer; classify dispatch errors, switching to the fallback group
// before rendering the terminal exits.
func (l *v1DispatchLoop) run(ctx context.Context) {
	// routes.ts:2643: a leftover cutover reservation releases with the request.
	defer l.releasePendingSpeedFirstReservation()
	// routes.ts:2640 finally：压缩等待保活心跳随请求终态兜底停止（Stop 幂等，
	// 常规路径已在 fetch 返回时停止）。
	defer l.stopCompactSseWaitHeartbeat()
	for {
		current := l.current
		coordination := l.newRequestCoordination()
		// W4-B（BUG-0175）D-114 接线：普通路由首字截止配置与速度优先决策
		// 闭包（Node normalRouteFirstByteConfig / onNormalRouteFirstByteDeadline，
		// routes.ts:1272-1273 的 coordination 注入）。
		coordination.NormalRouteFirstByteConfig = chainFirstByteConfigOf(current.NormalRouteFirstByteConfig)
		coordination.OnNormalRouteFirstByteDeadline = l.onNormalRouteFirstByteDeadline(ctx, current)
		// 总时间兜底截止接线（设计 6.3/6.6）：速度优先运行态配置原样透传，
		// 到期决策闭包镜像首字（记慢样本 → 安全切号裁决）。
		coordination.NormalRouteSpeedFirstConfig = current.NormalRouteSpeedFirstConfig
		coordination.OnNormalRouteTotalTimeDeadline = l.onNormalRouteTotalTimeDeadline(ctx, current)
		// W4-B（BUG-0175）D-114 接线：取走上次切号留下的并发槽预留
		//（routes.ts:1103-1104 dispatchCutoverReservation）。
		dispatchReservation := l.speedFirstCutoverReservation
		l.speedFirstCutoverReservation = nil
		l.speedFirstSlowObservedForAttempt = nil
		// 总时间去重标记与切号信号槽由 transport timer goroutine 并发读写
		//（major-2 修复）：轮级清零必须持同一把观察互斥锁，否则上一 attempt
		// 的迟到回调会在清零后把标记写回（内存模型违规 + 下一样本被误去重）。
		l.speedFirstTotalTimeObservationMu.Lock()
		l.speedFirstTotalTimeSlowObservedForAttempt = nil
		l.speedFirstTotalTimeCutoverSignal = nil
		l.speedFirstTotalTimeObservationMu.Unlock()
		// Node dispatches streamRetryDispatchAccounts(accounts,
		// streamServerRetryExcludedAccountIds) (routes.ts:942): the accounts a
		// previous response-layer RetryUpstream verdict excluded never re-enter
		// the candidate window of the current group.
		dispatchAccounts := streamRetryDispatchAccounts(current.Accounts, l.streamRetryExcludedAccounts)
		// routes.ts:921-923: a speed-first cutover narrows the window to the
		// reserved target first.
		if l.speedFirstRetryCandidateAccountIds != nil {
			narrowed := make([]gatewaydispatch.AccountCandidate, 0, len(l.speedFirstRetryCandidateAccountIds))
			for _, account := range dispatchAccounts {
				if _, reserved := l.speedFirstRetryCandidateAccountIds[account.ID]; reserved {
					narrowed = append(narrowed, account)
				}
			}
			dispatchAccounts = narrowed
		}
		// SwitchTarget（切号冻结目标）：本请求一旦有账户完成上游请求构造，所有
		// 重派窗口（流式重试、速度优先切换、调度错误重派、分组回退）在推进前
		// 按冻结目标后置过滤；首个候选选择（无冻结目标）不受影响。
		dispatchAccounts = l.filterAccountsForFrozenSwitchTarget(ctx, dispatchAccounts)
		// 本轮上游派发起点：引擎调用单点在循环体内，每轮刷新（含回退分组
		// 切换后的下一轮），供耗尽埋点的阶段起点口径使用。
		l.roundStartedAtMs = time.Now().UnixMilli()
		// codex 压缩 SSE 的派发等待期保活（Node routes.ts:1229-1239）：fetch
		// 前挂 10s compaction 保活心跳，fetch 返回（等待期结束）即停。
		l.startCompactSseWaitHeartbeat(ctx, current)
		dispatched, dispatchErr := l.c.engine.FetchFirstAvailableUpstream(ctx, gatewaydispatch.FetchFirstAvailableUpstreamArgs{
			Req:                             l.req,
			Accounts:                        dispatchAccounts,
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
			// 通用调度排除集 + 分派段（调度内核通用化设计 5.1，批次 1）：内核
			// 按分派段推进，段内让位、普通候选耗尽后翻回，替代原 Codex turn
			// 避让的整体过滤 + last-resort 翻回两段画像分支。
			SchedulingExclusions:       engineSchedulingExclusionsOf(current.SchedulingExclusions),
			DispatchSegments:           engineDispatchSegmentsOf(current.DispatchSegments),
			RequestCoordination:        coordination,
			WaitForRecoverableFailures: l.waitForRecoverableFailures(current),
			// W4-B（BUG-0175）D-114：速度优先切换预留（Node
			// preAcquiredConcurrency 参数）。
			PreAcquiredConcurrency: speedFirstReservationHandleOf(dispatchReservation),
		})
		l.stopCompactSseWaitHeartbeat()
		if dispatchErr == nil {
			// ---- response piping + finalization (response/finalization.ts) ----
			// F13（E2E-FINDING #13 第二层）：账户并发槽随本轮派发迭代释放。
			// keepConcurrencySlot=true 时 dispatch 内 releaseTransientState 是空操作，
			// 槽必须在响应管道结束后交给排队者（Node attachAccountSlotRelease
			// 挂在 res finish/close；Go 等价点是 handleUpstreamResponse 返回）。
			// 嵌套函数 + defer：循环体内不能用函数级 defer（会拖到 run 返回），
			// onceFunc 幂等，panic 与 RetryUpstream 切号都不会漏槽或双释放。
			//
			// BUG-0267：整个响应轮（handling 消费 → post-verdict 结算 → 分支树）
			// 包进 responseRound 闭包，defer 承载 Node routes.ts:2495-2521 attempt
			// finally 的兜底结算（settleTransferredUpstreamAttemptsSafely）。闭包
			// 返回 true = 请求已终态（run 返回），false = 继续下一轮派发（等价
			// 原先各分支的 return / continue）。
			responseRoundTerminal := func() (settled bool) {
				// typed nil 防御：*gatewaycircuit.Attempt(nil) /
				// *gatewayaccounteffects.GatewayKeyModelAttempt(nil) 直接装箱进
				// 接口会得到非 nil 接口（方法调用 nil receiver panic），引擎
				// 未装配时字段为 nil 的路径必须保持接口 nil。
				circuitHandle := postVerdictCircuitHandle(dispatched.AccountCircuitAttempt)
				keyModelHandle := postVerdictKeyModelHandle(dispatched.KeyModelAttempt)
				// 总时间兜底截止的响应轮软观察 timer（设计 6.3 采样点 1 的
				// body 阶段承接）：transport 总时间 timer 只覆盖响应头之前的
				// 请求阶段（startRequestPhaseTimer 的 responseReceived 守卫），
				// 响应头到达后的到期观察由本 timer 承接——只记慢样本（软观察
				// 采样点 1），不中断当前响应轮；完成时采样点 2/3 由
				// observeSpeedFirstResponseOutcome 承接。attempt 生命周期销毁：
				// responseRound 返回即 finished 置位 + stop。
				totalTimeObserverFinished := &atomic.Int32{}
				stopTotalTimeObserver := l.armTotalTimeDeadlineObserver(ctx, current, dispatched, totalTimeObserverFinished)
				defer func() {
					totalTimeObserverFinished.Store(1)
					stopTotalTimeObserver()
				}()
				defer l.settleTransferredUpstreamAttemptsSafely(ctx, dispatched.Account.ID, circuitHandle, keyModelHandle)
				handling := func() gatewayresponse.UpstreamResponseHandlingResult {
					defer func() {
						if dispatched.ReleaseConcurrency != nil {
							dispatched.ReleaseConcurrency()
						}
						// BUG-0247 项 1：响应轮终态统一消费重试租约释放回调
						//（Node routes.ts:2498-2501 finally 的
						// releaseAccountLockRetryLease(accountLockLeaseScheduleNextRetry)）。
						// 引擎构造结果时已摘走 activeAccountLockRetryLease（引擎出口
						// defer 因此不兜底），此前 chain 面零消费——"被选中但未协议
						// 验证成功"路径的派发租约悬挂至 5 分钟 lease_until 过期
						//（chain_account_locks.go 派发租约 300s），同账户重试
						// AcquireRetryLease 返回 WaitMs 空耗墙钟预算。
						// scheduleNextRetry 恒 false 对齐 Node 语义：Node 仅在
						// same-account 重试携带路径（routes.ts:1914/:1950/:2143/:2160/
						// :2180/:2274）置 true，Go 链面无这些路径——流式服务端重试
						// 与速度优先切换均为跨账户轮换（Node 对应分支同样以 false
						// 释放），首字截止切号的预留由 settleFirstByteDeadlineCutover-
						// Verdict 消费、与本租约无关。once 语义防双释放。
						if dispatched.ReleaseAccountLockRetryLease != nil {
							dispatched.ReleaseAccountLockRetryLease(false)
						}
						// 半开探测租约释放（Node routes.ts:2520 finally 的
						// releaseHalfOpenLease）：keepConcurrencySlot=true 时引擎
						// releaseTransientState 不释放、所有权移交链面，此前链面
						// 零消费——选中 2xx 尝试若持有半开认领，租约悬挂至 180s
						// TTL（localSuppressionHalfOpenLeaseMs），刚成功服务请求的
						// 健康账户被压在 half-open 排除态且不可再认领。nil 检查
						// 兼容无租约路径；成功侧由 confirmProtocolSuccessSideEffects
						// 先行 ConfirmHalfOpenSuccess（Node :2484），此处兜底其余
						// 终态路径，once 语义防双结算。
						// 顺序敏感点：本 defer（Release）先于 confirmProtocolSuccess
						// SideEffects（Confirm）执行——Node 相反（confirm :2484 在
						// release :2494 前）。当前无害：生产适配器 CompleteSuccess
						// 恒 false 且 gateway 流量受 automaticAccountStateMutation
						// Allowed 门约束为 no-op；未来接入带真实 CompleteSuccess 的
						// 租约（Node precheck 家族）时需调整为 Confirm 先行，否则
						// Release 清 leaseID 会使 Confirm 失效。
						if dispatched.ReleaseHalfOpenLease != nil {
							dispatched.ReleaseHalfOpenLease()
						}
					}()
					return l.c.handleUpstreamResponse(l.req, l.res, l.auditCapture, current, dispatched, l.startedAt, current.ActiveGatewaySettings, l.budgets, l.waitCommitState)
				}()
				// BUG-0267 post-verdict 结算块（Node routes.ts:1780-1877）：引擎
				// OK 臂已收回前置 ReportFramingComplete，circuit confirmation 与
				// keyModel 尝试在此按 body 处理后的实际结局分类结算；账户锁的
				// upstream_body_transport_failure 失败记录同点补齐（Node :1813-
				// 1823）。结算先于下方分支树（Node 的结算块在 alreadyFinalized
				// 早退与 retryUpstream 消费之前），三类分支（cutover / 终态 /
				// 重派）统一经过。
				var postVerdictLockRecorder postVerdictAccountLockFailureRecorder
				if l.c != nil && l.c.engine != nil {
					postVerdictLockRecorder = l.c.engine.Locks
				}
				l.settlePostVerdictUpstreamAttempts(ctx, dispatched, handling, circuitHandle, keyModelHandle, postVerdictLockRecorder)
				if handling.FirstByteDeadlineCutover {
					// BUG-0241 留档收口：首字截止切号的本 attempt 以 timeout 终态
					// 结算（Node catch 响应段 timeout 族；速度优先排序正依赖该
					// 信号识别慢账户），再走 cutover 消费端。
					if dispatched.HotQualityAttempt != nil {
						dispatched.HotQualityAttempt.RecordTerminal(ctx, gatewaydispatch.HotQualityTerminal{
							OutcomeClass: gatewaydispatch.HotQualityOutcomeTimeout,
							FailureScope: "protocol_model",
							Source:       "gateway_transport",
						})
					}
					// R5：非流式管线 configured_deadline 首字超时的切号 verdict
					// （Node routes.ts catch 响应段）交给既有 cutover 消费端：
					// 收窄到保留目标重派（false → continue）或耗尽退出（true）。
					// 预留已在响应面 TransferForCutover 转移进 verdict，此处只
					// 消费、不重复转移（Transfer 为 active→transferred once 语义）。
					if l.settleFirstByteDeadlineCutoverVerdict(ctx, dispatched, handling) {
						return true
					}
					return false
				}
				if !handling.RetryUpstream {
					// routes.ts:1841-1860: the unified hot-quality terminal
					// settlement runs first (BUG-0241): completed_response on
					// protocol-validated success (with the explicit first-byte
					// sample) / upstream_response_failure on the diagnostic
					// forwarded response.
					l.settleHotQualityTerminal(ctx, dispatched, handling)
					// routes.ts:2393-2455: the speed-first response observation
					// (slow/success sampling) runs once the response completed
					// without a server-retry verdict.
					l.observeSpeedFirstResponseOutcome(ctx, current, dispatched, handling)
					// routes.ts:2478-2486: the final protocol oracle confirms the
					// pending sibling Key failures + the winning Key success.
					l.confirmProtocolSuccessSideEffects(ctx, dispatched, handling)
					return true
				}
				// BUG-0241 留档收口：响应期重试族的当前 attempt 在换号前结算诊断
				// 终态（重试轮次是新 attempt 与新 lifecycle），对齐 Node 统一
				// 结算点对所有响应轮生效的语义。
				l.settleHotQualityTerminal(ctx, dispatched, handling)
				// D1: the response layer asked for a server-side account switch
				// (Node routes.ts:1899 `if (handledResponse.retryUpstream)`); the
				// loop continues on the remaining candidates or settles the
				// exhausted exit — never an empty 200.
				if l.settleResponseStreamServerRetry(ctx, dispatched, handling) {
					return true
				}
				return false
			}()
			if responseRoundTerminal {
				return
			}
			continue
		}
		if l.settleDispatchError(ctx, dispatchErr) {
			return
		}
	}
}

// settleResponseStreamServerRetry mirrors the Node retryUpstream consumption
// (routes.ts:1899-2398) for the reasons the Go response layer produces
// (response_inspection / pre_commit_stream_failure). The deep per-branch
// server-retry loops that stay engine-internal in Go (speed-first cutover,
// codex encrypted-content recovery, account-lock lease carry)
// never reach this method, and the same-account retry reservation
// (routes.ts:2245-2300) is a Node dispatch-loop nicety the Go chain does not
// carry: the verdict rotates to the next account instead. Returns true when
// the request settled (terminal response rendered) and false when the loop
// should re-dispatch on the remaining candidates.
func (l *v1DispatchLoop) settleResponseStreamServerRetry(
	ctx context.Context,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
) bool {
	// A RetryUpstream verdict is a pre-commit decision (the response layer only
	// produces it while canRetryUpstream); a writable-ended downstream can no
	// longer take a different account's response.
	if writableEndedOf(l.res) {
		return true
	}
	// BUG-0289 恢复臂：codex 加密上下文流内失败的清理重放 verdict。挂在既有
	// 排除集/计数/metadata 之前——恢复轮钉回同账户（下一轮
	// newRequestCoordination 消费 pending 状态注入 override/钉住/语义重试 ID），
	// 既不把当前账户加入 streamRetryExcludedAccounts，也不计入
	// stream_server_retry_dispatch 的换号重试计数。恢复重放是一轮：pending
	// 注入即清空，重放再失败走既有派发错误结算（含换号/切组）。
	if handling.RetryReason == gatewayresponse.StreamServerRetryCodexEncryptedContentRecovery {
		metadata := recoveryMetadataOrZero(handling.RecoveryMetadata)
		l.auditCapture.AddGatewayMetadata("codex_encrypted_content_recovery_retry", map[string]any{
			"accountId":                             dispatched.Account.ID,
			"retryCount":                            l.streamServerRetryCount + 1,
			"signal":                                handling.CompatibilityRecoverySignal,
			"strategy":                              metadata.Strategy,
			"removedReasoningEncryptedContentCount": metadata.RemovedReasoningEncryptedContentCount,
			"removedFunctionOutputEncryptedContentCount": metadata.RemovedFunctionOutputEncryptedContentCount,
			"removedAgentMessageEncryptedContentCount":   metadata.RemovedAgentMessageEncryptedContentCount,
			"removedCompactionEncryptedContentCount":     metadata.RemovedCompactionEncryptedContentCount,
			"removedReasoningItemCount":                  metadata.RemovedReasoningItemCount,
			"removedAgentMessageItemCount":               metadata.RemovedAgentMessageItemCount,
			"removedCompactionItemCount":                 metadata.RemovedCompactionItemCount,
			"bodyBytesBefore":                            metadata.BodyBytesBefore,
			"bodyBytesAfter":                             metadata.BodyBytesAfter,
		})
		l.pendingCodexRecovery = &v1PendingCodexEncryptedContentRecovery{
			AccountID:       dispatched.Account.ID,
			Body:            handling.RecoveryBody,
			SemanticRetryID: handling.RecoverySemanticRetryID,
			Metadata:        handling.RecoveryMetadata,
			Signal:          handling.CompatibilityRecoverySignal,
		}
		return false
	}
	current := l.current
	accountID := dispatched.Account.ID
	// ---- BUG-0298：信号未命中换号的同签名即停与会话级避让 ----
	// 该换号面（检查拦截命中且加密上下文信号未命中）的失败与"来源×账户"绑定：
	// 加密续状态仅生成它的上游会话可验证。①换号被接受时解除失败账户的会话
	// 亲和并记录来源级短 TTL 避让（含后台探活），同一会话的后续请求把该账户
	// 排到同层候选之后，不再反复先撞它；②连续两次换号失败且上游错误码+文案
	// 完全一致判定同源，停止级联候选，走既有耗尽契约。签名与避让仅覆盖
	// ResponseInspection 非 nil 的换号；恢复臂重放（上方专用通道）与检查未
	// 命中换号（无决策对象可签名）行为不变。避让不写账户健康/熔断状态
	// （预提交零输出失败契约不变），协作方未装配时显式降级。
	if handling.RetryReason == gatewayresponse.StreamServerRetryPreCommitStreamFailure && handling.ResponseInspection != nil {
		signature := handling.ResponseInspection.UpstreamErrorCode + "\x1f" + handling.ResponseInspection.UpstreamErrorMessage
		if signature == l.lastPreCommitSwitchSignature {
			l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, current, "pre_commit_switch_same_signature")
			l.auditCapture.AddGatewayMetadata("pre_commit_stream_server_retry_stopped", map[string]any{
				"reason":    "same_signature",
				"accountId": accountID,
				"errorCode": handling.ErrorCode,
			})
			l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
				message:        handling.Message,
				retryReason:    handling.RetryReason,
				errorCode:      handling.ErrorCode,
				decision:       handling.ResponseInspection,
				usageContext:   current.UsageContext,
				clientStrategy: &current.ClientStrategy,
			})
			return true
		}
		l.lastPreCommitSwitchSignature = signature
		if l.c.streamFailureAvoidance != nil {
			l.c.streamFailureAvoidance.scheduleStreamSwitchClientSourceAvoidance(ctx, l.req, current.UsageContext, current.SessionAffinityKey, dispatched.Account, handling.ResponseInspection.UpstreamErrorCode, handling.ResponseInspection.UpstreamErrorMessage, dispatched.AuditAttemptID)
		}
	}
	// Node 2301-2307: a policy-requested exclusion puts the current account
	// into the per-group stream-retry excluded set.
	policyRequestedAccountExclusion := handling.ExcludeCurrentAccount
	if policyRequestedAccountExclusion {
		if l.streamRetryExcludedAccounts == nil {
			l.streamRetryExcludedAccounts = map[string]struct{}{}
		}
		l.streamRetryExcludedAccounts[accountID] = struct{}{}
	}
	l.streamServerRetryCount++
	remaining := streamRetryDispatchAccounts(current.Accounts, l.streamRetryExcludedAccounts)
	l.auditCapture.AddGatewayMetadata("stream_server_retry_dispatch", map[string]any{
		"retryReason":                     handling.RetryReason,
		"retryCount":                      l.streamServerRetryCount,
		"candidateCount":                  len(current.Accounts),
		"remainingCandidateCount":         len(remaining),
		"elapsedMs":                       l.c.preauth.NowMs() - l.startedAt,
		"accountId":                       accountID,
		"excludedAccountIds":              stringSetKeys(l.streamRetryExcludedAccounts),
		"excludeCurrentAccount":           handling.ExcludeCurrentAccount,
		"currentRequestAccountExcluded":   policyRequestedAccountExclusion,
		"policyRequestedAccountExclusion": policyRequestedAccountExclusion,
		"errorCode":                       handling.ErrorCode,
	})
	if handling.ResponseInspection != nil {
		l.auditCapture.AddGatewayMetadata("stream_server_retry_policy", map[string]any{
			"policyId":      handling.ResponseInspection.PolicyID,
			"policyName":    handling.ResponseInspection.PolicyName,
			"accountSwitch": handling.ResponseInspection.AccountSwitch,
			"retryEnabled":  handling.ResponseInspection.RetryEnabled,
		})
	}
	// Node 2326-2356: a response-inspection retry that does not change the
	// dispatch (no account exclusion) stops with the exhausted contract.
	if handling.RetryReason == gatewayresponse.StreamServerRetryResponseInspection &&
		handling.ResponseInspection != nil && !policyRequestedAccountExclusion {
		// Node routes.ts:2332: the terminal failure confirms the pending
		// client-IP account failures before the response renders.
		l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, current, "response_inspection_no_dispatch_change")
		l.auditCapture.AddGatewayMetadata("response_inspection_server_retry_stopped", map[string]any{
			"reason":        "no_dispatch_change",
			"accountId":     accountID,
			"policyId":      handling.ResponseInspection.PolicyID,
			"policyName":    handling.ResponseInspection.PolicyName,
			"accountSwitch": handling.ResponseInspection.AccountSwitch,
			"retryEnabled":  handling.ResponseInspection.RetryEnabled,
			"errorCode":     handling.ErrorCode,
		})
		l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
			message:        handling.Message,
			retryReason:    handling.RetryReason,
			errorCode:      handling.ErrorCode,
			decision:       handling.ResponseInspection,
			usageContext:   current.UsageContext,
			clientStrategy: &current.ClientStrategy,
		})
		return true
	}
	if len(remaining) == 0 {
		// Node 2362-2366: the group's candidate window is empty — the excluded
		// accounts join the request-level exhausted set and the fallback group
		// gets its chance before the exhausted exit.
		for accountID := range l.streamRetryExcludedAccounts {
			if l.exhaustedAccounts == nil {
				l.exhaustedAccounts = map[string]struct{}{}
			}
			l.exhaustedAccounts[accountID] = struct{}{}
		}
		fallbackReason := streamServerRetryFallbackReason(handling.RetryReason)
		switch fallback, fallbackErr := l.switchToFallbackGroup(ctx, fallbackReason); {
		case fallbackErr != nil:
			// Node: the switch error propagates to the top-level catch.
			l.renderUnexpectedDispatchFailure(ctx, fallbackErr)
			return true
		case fallback == v1FallbackCompleted:
			return true
		case fallback == v1FallbackSwitched:
			return false
		}
		// Node 2386-2397: no fallback switch → the stream server-retry
		// exhausted contract (503, recordUsage:false), never an empty 200.
		// Node routes.ts:2374: the pending client-IP account failures are
		// confirmed before the exhausted response renders.
		l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, current, "stream_server_retry_exhausted")
		l.sendStreamServerRetryExhaustedResponse(streamServerRetryExhaustedInput{
			message:        handling.Message,
			retryReason:    handling.RetryReason,
			errorCode:      handling.ErrorCode,
			decision:       handling.ResponseInspection,
			usageContext:   current.UsageContext,
			clientStrategy: &current.ClientStrategy,
		})
		return true
	}
	// Node 2398: candidates remain — continue the dispatch loop.
	return false
}

// recoveryMetadataOrZero 是恢复臂 metadata 读取的 nil 安全视图：verdict 正常
// 携带 BuildCodexEncryptedContentRecoveryRetry 产出的 Metadata，nil 仅是旧
// 构造路径的防御形态。
func recoveryMetadataOrZero(metadata *gatewaycodex.CodexEncryptedContentRecoveryMetadata) gatewaycodex.CodexEncryptedContentRecoveryMetadata {
	if metadata == nil {
		return gatewaycodex.CodexEncryptedContentRecoveryMetadata{}
	}
	return *metadata
}

// settleDispatchError maps one dispatch-loop error onto the Node error
// handling (routes.ts:1285-1487 catch + the top-level catch 2532-2638). It
// returns true when the request is settled (response written, aborted, or
// terminal exit rendered) and false when the loop should continue on the
// switched fallback group.
func (l *v1DispatchLoop) settleDispatchError(ctx context.Context, dispatchErr error) bool {
	// 总时间兜底截止切号（设计 6.6）：transport 总时间 timer 到点决策确认切
	// 号后销毁请求，NormalRouteTotalTimeTimeoutError 沿引擎错误通路原样上抛
	// （引擎错误分类不识别该类型，unproven rethrow 透传），此处凭决策闭包写
	// 下的载荷槽构造 cutover 消费（收窄到保留目标重派或耗尽退出）。首字截止
	// 的分支在下方既有 errors.As 臂；两维度共享切号链与上限。
	var totalTimeout *gatewaydispatch.NormalRouteTotalTimeTimeoutError
	if errors.As(dispatchErr, &totalTimeout) {
		if l.settleTotalTimeCutoverError(ctx, totalTimeout) {
			return true
		}
		return false
	}
	// W4-B（BUG-0175）D-114：速度优先切号错误（routes.ts:1295-1345）。持有
	// 切换预留时收窄到保留目标重派；预留缺席/已锁定走耗尽退出。
	var cutover *gatewaydispatch.NormalRouteFirstByteCutoverError
	if errors.As(dispatchErr, &cutover) {
		if l.settleSpeedFirstCutoverError(ctx, cutover, gatewayproxyhealth.LatencyDimensionFirstByte, 0) {
			return true
		}
		return false
	}
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
				retryReason:    "pre_commit_stream_failure",
				errorCode:      gatewaypreauth.GatewayStreamClientRetryErrorCode,
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
			// Node 1425-1433: only the non-recoverable failed accounts enter
			// the exhausted set; recoverable failures stay retryable.
			l.exhaustDispatchFailedAccounts(attempt)
			// routes.ts:1432-1452: a narrowed speed-first window whose target
			// failed falls back to the full candidate window (failed accounts
			// excluded); an empty window continues to the fallback below.
			if l.speedFirstRetryCandidateAccountIds != nil {
				for _, id := range attempt.FailedAccountIDs {
					if l.streamRetryExcludedAccounts == nil {
						l.streamRetryExcludedAccounts = map[string]struct{}{}
					}
					l.streamRetryExcludedAccounts[id] = struct{}{}
				}
				l.speedFirstRetryCandidateAccountIds = nil
				remainingSpeedFirstAccounts := streamRetryDispatchAccounts(l.current.Accounts, l.streamRetryExcludedAccounts)
				l.auditCapture.AddGatewayMetadata("normal_route_speed_first_reserved_target_exhausted", map[string]any{
					"failedAccountIds":             attempt.FailedAccountIDs,
					"recoverableAccountIds":        attempt.RecoverableAccountIDs,
					"remainingCandidateAccountIds": chainAccountIDsOf(remainingSpeedFirstAccounts),
				})
				if len(remainingSpeedFirstAccounts) > 0 {
					return false
				}
			}
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
				l.renderUnexpectedDispatchFailure(ctx, fallbackErr)
				return true
			case fallback == v1FallbackCompleted:
				return true
			case fallback == v1FallbackSwitched:
				return false
			case fallback == v1FallbackNone && len(attempt.RecoverableAccountIDs) > 0 &&
				!l.serverRetryBudget.HandoffRequired(gatewaypreauth.AvailabilityRecoverableLater, nil):
				// Node routes.ts:1479-1486：切组未成但仍有可恢复账户、等待预算
				// 未到 recoverable_later 交接点——置强制等待并继续循环，下一轮
				// 引擎调用进入可恢复等待（优先等即将恢复的账户，保住亲和）；
				// 预算已到交接点则落入下方耗尽渲染。
				l.forceRecoverableFailureWait = true
				return false
			}
		}
		l.renderDispatchExhausted(ctx, attempt)
		return true
	}
	// Node top-level catch: an unexpected dispatch error keeps the 503
	// upstream contract — never the orchestrator 500 (V5).
	l.renderUnexpectedDispatchFailure(ctx, dispatchErr)
	return true
}

// renderDispatchExhausted mirrors the Node top-level catch for the
// UpstreamAttemptError branch (routes.ts:2551-2638): the client payload is
// the fixed copy pair (no candidate accounts vs. retryable upstream), the
// detailed last-attempt diagnostics stay on the audit/log surface
// (upstream-dispatch.ts buildUpstreamAttemptFailureMessage,
// dispatch-exhaustion-classifier.ts).
func (l *v1DispatchLoop) renderDispatchExhausted(ctx context.Context, attempt *gatewaydispatch.UpstreamAttemptError) {
	lastAttempt := attempt.LastAttempt
	fields := map[string]any{
		"event":         "gateway_dispatch_exhausted",
		"traceId":       l.traceID,
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
	// upstream.dispatch.failed 耗尽点埋点（对齐 Node routes.ts:1383）：调度
	// 错误确认为 UpstreamAttemptError 终态时登记一条。阶段起点口径：本轮
	// 上游派发起点 roundStartedAtMs（每轮 FetchFirstAvailableUpstream 前刷新，
	// 仍含引擎内候选/排队耗时，但远小于整请求 startedAt 口径）；
	// roundStartedAtMs 未记录（0）时回落请求开始时刻（旧行为）。Go 派发循环
	// 没有 Node 每轮的 upstreamDispatchStartedAt 计时；expected_failure 恒为
	// warn 级，不受该时长影响。
	stageStartedAt := time.UnixMilli(l.startedAt)
	if l.roundStartedAtMs > 0 {
		stageStartedAt = time.UnixMilli(l.roundStartedAtMs)
	}
	chainEmitGatewayRequestStage(slog.Default(), "upstream.dispatch.failed", map[string]any{
		"traceId":         l.traceID,
		"failureReason":   failureReason,
		"expectedFailure": true,
	}, "expected_failure", stageStartedAt)

	payloadMessage := "上游暂时不可用，请重试"
	payloadCode := gatewaypreauth.GatewayStreamClientRetryErrorCode
	if lastAttempt == nil {
		// Node: message === '没有可用的上游账户' — the no-candidate attempt
		// error carries no last attempt.
		payloadMessage = "没有可用的上游账户"
		payloadCode = "no_available_upstream_account"
	}
	// Node routes.ts:2619: the gateway failure response confirms the pending
	// client-IP account failures first (the dispatch_exhausted_protocol_retry
	// branch at routes.ts:2576 has no Go rendering — V5 kept the fixed 503
	// upstream contract — so this single confirm covers the terminal exit).
	l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, l.current, "gateway_failure_response")
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
func (l *v1DispatchLoop) renderUnexpectedDispatchFailure(ctx context.Context, dispatchErr error) {
	l.c.observability.Logger().Warn("gateway_request_unexpected_error", map[string]any{
		"event":    "gateway_request_unexpected_error",
		"traceId":  l.traceID,
		"endpoint": l.current.UsageContext.Endpoint,
		"apiKeyId": l.current.UsageContext.APIKeyID,
		"groupId":  l.current.UsageContext.GroupID,
		"error":    dispatchErr.Error(),
	}, "网关请求处理出现未预期异常")
	payload := gatewaypreauth.GatewayErrorPayloadOf("上游暂时不可用，请重试", "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode)
	// Node routes.ts:2619: the same terminal confirm precedes the gateway
	// failure response.
	l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, l.current, "gateway_failure_response")
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
		// 绑定目标会话（group/account 模式）不做 API Key 分组回退：候选窗口已
		// 收敛到绑定作用域，回退切换会逃逸出绑定分组；route action 直接走既有
		// 终态渲染（无候选 = "当前路由没有可用的上游账户"语义）。
		mayTryFallback := !l.chatTarget.pinned() &&
			action.Coordination.Outcome != gatewaypreauth.RouteOutcomeClientHandoff &&
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

// exhaustDispatchFailedAccounts adds the attempt's non-recoverable failed
// accounts to the request-level exhausted set (Node routes.ts:1425-1433:
// failedAccountIds minus recoverableAccountIds; recoverable failures keep the
// account retryable).
func (l *v1DispatchLoop) exhaustDispatchFailedAccounts(attempt *gatewaydispatch.UpstreamAttemptError) {
	if len(attempt.FailedAccountIDs) == 0 {
		return
	}
	recoverable := make(map[string]struct{}, len(attempt.RecoverableAccountIDs))
	for _, id := range attempt.RecoverableAccountIDs {
		recoverable[id] = struct{}{}
	}
	if l.exhaustedAccounts == nil {
		l.exhaustedAccounts = make(map[string]struct{})
	}
	for _, id := range attempt.FailedAccountIDs {
		if _, isRecoverable := recoverable[id]; isRecoverable {
			continue
		}
		l.exhaustedAccounts[id] = struct{}{}
	}
}

// switchToFallbackGroup mirrors switchToFallbackGroup (routes.ts:570-662).
func (l *v1DispatchLoop) switchToFallbackGroup(ctx context.Context, reason string) (v1FallbackSwitch, error) {
	current := l.current
	if current.InteractionResourceAffinity != nil {
		return v1FallbackNone, nil
	}
	// 绑定目标会话（group/account 模式）禁止跨分组回退：候选窗口已收敛到绑定
	// 作用域，切组会让绑定会话落到 Key 路由的其他分组。返回 none 后调用方走
	// 既有耗尽终态（"无可用账户"语义），不新造错误分支。
	if l.chatTarget.pinned() {
		l.auditCapture.AddGatewayMetadata("api_key_group_route_fallback_skipped", map[string]any{
			"reason":        reason,
			"skippedReason": "chat_dispatch_target_pinned",
			"groupId":       current.UsageContext.GroupID,
		})
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
		// Node 625: the request-level exhausted set filters every fallback
		// candidate group window.
		ExcludedAccountIDs: l.exhaustedAccounts,
		RoutePlanSnapshot:  current.RoutePlanSnapshot,
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
	// D-109（BUG-0175）：新分组的 DispatchContext 带新的 client-IP 并发槽
	//（Node routes.ts:651 重新 attach release；旧槽在 handler 终态统一释放）。
	l.releases.Add(next.ReleaseClientIPConcurrency)
	// Node 644-651 transfers the client-ip slot and settles the hot-quality
	// reservation; those lifecycle ports stay engine-internal in Go. The
	// per-group retry resets ride on the fresh DispatchContext.
	// 换代点预算回收：切组 fallback preflight 产出的请求级实例（compaction 时
	// 含 WithoutLimit 的 Unbounded wall budget）随之生效。
	l.adoptDispatchContextBudgets(next)
	l.current = next
	// Node 652-657: a switched fallback resets the per-group stream server-
	// retry bookkeeping (streamServerRetryExcludedAccountIds /
	// streamServerRetryCount) and the W4-B speed-first cutover state
	// (routes.ts:654-659).
	l.streamRetryExcludedAccounts = map[string]struct{}{}
	l.streamServerRetryCount = 0
	l.resetSpeedFirstState()
	return v1FallbackSwitched, nil
}

// filterAccountsForFrozenSwitchTarget 在重派前按本请求已冻结的切号目标后置
// 过滤候选窗口：与冻结源同账户的候选（同账户 Key 轮换 / 同账户重试）不是
// 切换点，语义不变。冻结目标不可解析（不变量违规）时 fail-closed——停止跨
// 账户切号，仅保留冻结源账户，并输出一次 switch_target_unresolved 结构化
// 诊断（设计文档 §4/§6）。
func (l *v1DispatchLoop) filterAccountsForFrozenSwitchTarget(ctx context.Context, accounts []gatewaydispatch.AccountCandidate) []gatewaydispatch.AccountCandidate {
	gate := gatewaydispatch.SwitchTargetGateFromContext(ctx)
	if gate == nil || !gate.Frozen() {
		return accounts
	}
	filtered := gate.FilterAccounts(accounts)
	if gate.Unresolved() && gate.MarkUnresolvedDiagnosed() {
		fields := map[string]any{
			"event":    "switch_target_unresolved",
			"endpoint": l.current.UsageContext.Endpoint,
			"apiKeyId": l.current.UsageContext.APIKeyID,
			"groupId":  l.current.UsageContext.GroupID,
			"traceId":  l.traceID,
		}
		if sourceID := gatewaydispatch.SwitchTargetGateSourceOf(gate); sourceID != "" {
			fields["sourceAccountId"] = sourceID
		}
		l.auditCapture.AddGatewayMetadata("switch_target_unresolved", fields)
		l.c.observability.Logger().Warn("switch_target_unresolved", fields, "切号冻结目标不可解析，已停止跨账户切号")
	}
	return filtered
}

// chainAccountIDsOf 投影候选 id 列表（审计元数据用）。
func chainAccountIDsOf(accounts []gatewaydispatch.AccountCandidate) []string {
	out := make([]string, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, account.ID)
	}
	return out
}

// exhaustDispatchFailedAccountID 把单个账号加入请求级耗尽集。
func (l *v1DispatchLoop) exhaustDispatchFailedAccountID(accountID string) {
	if accountID == "" {
		return
	}
	if l.exhaustedAccounts == nil {
		l.exhaustedAccounts = make(map[string]struct{})
	}
	l.exhaustedAccounts[accountID] = struct{}{}
}

// renderDispatchExhaustedWithMessage 渲染速度优先耗尽的固定 503 契约
// （Node UpstreamAttemptError transportFailureKind=timeout 的顶层 catch）。
func (l *v1DispatchLoop) renderDispatchExhaustedWithMessage(ctx context.Context, message, accountID, accountName string) {
	l.c.observability.Logger().Warn("gateway_dispatch_exhausted", map[string]any{
		"event":                  "gateway_dispatch_exhausted",
		"traceId":                l.traceID,
		"failureReason":          "first_byte_timeout",
		"lastAttemptAccountId":   accountID,
		"lastAttemptAccountName": accountName,
		"endpoint":               l.current.UsageContext.Endpoint,
		"apiKeyId":               l.current.UsageContext.APIKeyID,
		"groupId":                l.current.UsageContext.GroupID,
		"trafficSource":          l.current.UsageContext.TrafficSource,
	}, "网关上游调度已耗尽")
	l.confirmClientIPAccountAvoidanceAfterFinalFailure(ctx, l.current, "gateway_failure_response")
	l.c.preauth.Responses.SendGatewayFailureResponse(gatewaypreauth.FailureResponseInput{
		Req:          l.req,
		Res:          l.res,
		AuditCapture: l.auditCapture,
		UsageContext: l.current.UsageContext,
		StartedAt:    l.startedAt,
		StatusCode:   http.StatusServiceUnavailable,
		ResponsePayload: gatewaypreauth.GatewayErrorPayloadOf(
			"上游暂时不可用，请重试", "service_unavailable", gatewaypreauth.GatewayStreamClientRetryErrorCode),
		Audit: gatewaypreauth.FailureAudit{
			Outcome:      gatewaypreauth.AuditOutcomeUpstreamFailed,
			ErrorPhase:   "dispatch",
			ErrorCode:    gatewaypreauth.GatewayStreamClientRetryErrorCode,
			ErrorMessage: message,
		},
		RecordUsage:  boolPtr(false),
		FailureScope: "upstream",
	})
}

// settleHotQualityTerminal 镜像 Node routes.ts:1841-1860 响应体处理完成后的
// 统一热质量终态结算点（BUG-0241：链面消费引擎经 UpstreamDispatchResult 带出的
// HotQualityAttempt 句柄；留档收口批次放开 AlreadyFinalized 渲染族与
// RetryUpstream 响应期重试族的结算，分派对齐 Node 五臂）：
//   - 响应期重试族（RetryUpstream：response_inspection / pre_commit 等 2xx
//     提交前校验失败被重试，不经 attemptoutcomes 两条引擎失败路径）→
//     upstream_response_failure / none / upstream_response；其中用户配置的
//     响应检查策略（configured_response_policy，Node 的
//     explicitUserPolicyRetry）→ explicit_policy_failure / account /
//     explicit_policy；重试轮次是新的 attempt 与新 lifecycle（attemptId 含
//     attemptIndex，Node 同构），本 attempt 以诊断失败终态收口；
//   - AlreadyFinalized 渲染族的传输失败（TransportFailure.Kind）→
//     timeout / read_interruption / transport_failure，failureScope
//     protocol_model、source gateway_transport（Node
//     hotQualityOutcomeForTransportFailure）；
//   - AlreadyFinalized 渲染族的本地失败/下游冲突（无 TransportFailure）→
//     unknown / none / request_lifecycle（Node neutral /
//     requestLocalProtocolFailure 臂）；
//   - 协议验证成功（ProtocolValidatedSuccess 隐含 2xx + 校验通过 + 非透传
//     失败）→ completed_response / none / request_lifecycle；
//   - 未验证但完整转发的响应（非 2xx 透传 / 校验未过的转发）→
//     upstream_response_failure / none / upstream_response。
//
// 引擎失败路径的重试（非 2xx 失败响应 / 传输错误）已在 attemptoutcomes.go
// 两条失败路径（:163/:347）结算，本函数不与其重叠（lifecycle terminalOnce
// 幂等，首个结算生效）；载荷统一携带显式首字样本 FirstTokenMs（Node 的
// firstByteMs 对所有 outcomeClass 臂统一求值）。句柄 nil（引擎未装配热质量
// 工厂）时为中性 no-op。
func (l *v1DispatchLoop) settleHotQualityTerminal(
	ctx context.Context,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
) {
	if dispatched.HotQualityAttempt == nil {
		return
	}
	terminal := gatewaydispatch.HotQualityTerminal{
		OutcomeClass: gatewaydispatch.HotQualityOutcomeUpstreamResponseFailure,
		FailureScope: "none",
		Source:       "upstream_response",
	}
	switch {
	case handling.RetryUpstream && isExplicitPolicyRetry(handling):
		terminal = gatewaydispatch.HotQualityTerminal{
			OutcomeClass: gatewaydispatch.HotQualityOutcomeExplicitPolicyFailure,
			FailureScope: "account",
			Source:       "explicit_policy",
		}
	case handling.RetryUpstream:
		// response_inspection（非用户策略）/ pre_commit_stream_failure 等
		// 响应期重试的当前 attempt 诊断终态。
	case handling.TransportFailure != nil:
		terminal = gatewaydispatch.HotQualityTerminal{
			OutcomeClass: transportFailureOutcomeClass(handling.TransportFailure.Kind),
			FailureScope: "protocol_model",
			Source:       "gateway_transport",
		}
	case handling.AlreadyFinalized:
		// 本地渲染族（网关本地失败 / 下游冲突 / neutral 终止）。
		terminal = gatewaydispatch.HotQualityTerminal{
			OutcomeClass: gatewaydispatch.HotQualityOutcomeUnknown,
			FailureScope: "none",
			Source:       "request_lifecycle",
		}
	case handling.ProtocolValidatedSuccess:
		terminal = gatewaydispatch.HotQualityTerminal{
			OutcomeClass: gatewaydispatch.HotQualityOutcomeCompletedResponse,
			FailureScope: "none",
			Source:       "request_lifecycle",
		}
	}
	if handling.FirstTokenMs != nil {
		value := float64(*handling.FirstTokenMs)
		terminal.FirstByteMs = &value
	}
	dispatched.HotQualityAttempt.RecordTerminal(ctx, terminal)
}

// isExplicitPolicyRetry 识别用户配置的响应检查策略触发的重试（Node
// routes.ts 的 explicitUserPolicyRetry）。判定载体是响应层决策的
// ReplayAuthority：用户配置策略（account/management 来源）被授予
// explicit_user_policy，系统默认策略授予 system_default_retry_next_account
// ——原先按 Reason == "configured_response_policy" 判定会把系统默认策略的
// 重试也计成显式策略失败（BUG-0267 判定差登记；影响面仅热质量
// outcomeClass/failureScope）。非空 ReplayAuthority 只由响应检查决策产生，
// 无需再叠加 RetryReason 前置条件。
func isExplicitPolicyRetry(handling gatewayresponse.UpstreamResponseHandlingResult) bool {
	return handling.RetryUpstream &&
		handling.ResponseInspection != nil &&
		handling.ResponseInspection.ReplayAuthority == "explicit_user_policy"
}

// transportFailureOutcomeClass mirrors hotQualityOutcomeForTransportFailure
// （Node routes.ts:2747-2749）：timeout→timeout、read_incomplete→
// read_interruption、其余→transport_failure。
func transportFailureOutcomeClass(kind string) string {
	switch kind {
	case "timeout":
		return gatewaydispatch.HotQualityOutcomeTimeout
	case "read_incomplete":
		return gatewaydispatch.HotQualityOutcomeReadInterruption
	default:
		return gatewaydispatch.HotQualityOutcomeTransportFailure
	}
}

// confirmProtocolSuccessSideEffects 镜像 routes.ts:2478-2486 的最终协议
// 成功结算：挂起的同账户 Key 轮转失败确认 + 胜出 Key 的成功记录（D-111）。
// 结算错误不改写已提交的下游响应（Node 顶层 catch 同样只记日志）。
func (l *v1DispatchLoop) confirmProtocolSuccessSideEffects(ctx context.Context, dispatched gatewaydispatch.UpstreamDispatchResult, handling gatewayresponse.UpstreamResponseHandlingResult) {
	if !handling.ProtocolValidatedSuccess {
		return
	}
	if dispatched.ConfirmSameAccountApiKeyFailures != nil {
		if err := dispatched.ConfirmSameAccountApiKeyFailures(); err != nil {
			l.c.observability.Logger().Warn("gateway_account_api_key_rotation_confirm_failed", map[string]any{
				"event":     "gateway_account_api_key_rotation_confirm_failed",
				"traceId":   l.traceID,
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "已确认同账户 API Key 轮转失败结算未完成")
		}
	}
	if dispatched.ConfirmAccountAPIKeySuccess != nil {
		if err := dispatched.ConfirmAccountAPIKeySuccess(); err != nil {
			l.c.observability.Logger().Warn("gateway_account_api_key_success_settlement_failed", map[string]any{
				"event":     "gateway_account_api_key_success_settlement_failed",
				"traceId":   l.traceID,
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "账户 API Key 成功结算未完成")
		}
	}
	// 成功侧结算：把 ENGAGED 锁复位为 LOCKED_IDLE
	// 与 Node routes.ts:2481-2483 completeAccountLockSuccessAsync 行为一致。
	if dispatched.ConfirmAccountLockSuccess != nil {
		if err := dispatched.ConfirmAccountLockSuccess(); err != nil {
			l.c.observability.Logger().Warn("gateway_account_lock_success_settlement_failed", map[string]any{
				"event":     "gateway_account_lock_success_settlement_failed",
				"traceId":   l.traceID,
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "账户锁成功结算未完成")
		}
	}
	// 半开探测的成功确认（Node routes.ts:2484 confirmHalfOpenSuccess）：
	// 引擎对 gateway 流量的回调受 automaticAccountStateMutationAllowed 门
	// 约束（探针流量 only），此处对齐 Node 调用形态——回调为 nil 或 no-op
	// 时自然无害，协议成功即释放半开租约的成功语义不再依赖 finally 兜底。
	if dispatched.ConfirmHalfOpenSuccess != nil {
		dispatched.ConfirmHalfOpenSuccess()
	}
}

// ---------------------------------------------------------------------------
// BUG-0267 post-verdict 结算块（Node routes.ts:1780-1877 + :2509-2521）
// ---------------------------------------------------------------------------

// postVerdictCircuitAttempt 是 post-verdict 结算块消费的熔断尝试句柄面
// （生产为引擎带出的 *gatewaycircuit.Attempt，UpstreamDispatchResult 字段的
// 方法子集；测试注入计数闭包）。方法语义与 gatewaycircuit.Attempt 一致。
type postVerdictCircuitAttempt interface {
	IsConfirmation() bool
	ReportTransportFailure(ctx context.Context, failure gatewaycircuit.TransportFailure) (gatewaycircuit.FailureDecision, error)
	ReportFramingComplete(ctx context.Context) (*gatewaycircuit.MutationResult, error)
	ReportUnknown(ctx context.Context) (*gatewaycircuit.MutationResult, error)
}

// postVerdictKeyModelAttempt 是 post-verdict 结算块消费的 key-model 尝试
// 句柄面（生产为 *gatewayaccounteffects.GatewayKeyModelAttempt 的方法子集；
// 测试注入计数闭包）。Report 族是 terminalOnce 幂等结算（首个终态生效）。
type postVerdictKeyModelAttempt interface {
	ReportCompleteSuccess(ctx context.Context) error
	ReportUpstreamNotComplete(ctx context.Context) error
	ReportUnknown(ctx context.Context) error
}

// postVerdictAccountLockFailureRecorder 是账户锁失败记录的消费面（生产为
// gatewaydispatch.AccountLocks 的方法子集；测试注入计数闭包）。
type postVerdictAccountLockFailureRecorder interface {
	RecordFailureAsync(ctx context.Context, accountID, reason string, observation *gatewaydispatch.AccountLockObservation) error
}

// postVerdictClassification 承载 Node routes.ts:1750-1803 判定变量组的 Go 投影。
type postVerdictClassification struct {
	neutralSchedulingTermination bool
	hardFirstByteCutover         bool
	transportFailure             *gatewayresponse.StreamTransportFailure
	explicitUserPolicyRetry      bool
	requestLocalProtocolFailure  bool
	protocolValidatedSuccess     bool
}

// classifyPostVerdictOutcome 对齐 Node routes.ts:1750-1803 的判定组。字段映射：
//   - responseRetryUpstream / responseErrorCode → handling.RetryUpstream /
//     handling.ErrorCode；neutralRequestWallTermination（:1752）→ ErrorCode 等于
//     gateway_request_wall_budget_exhausted（GatewayRequestWallBudgetExhaustedCode）；
//     gatewayLocalFailure（:1753）→ handling.GatewayLocalFailure；
//   - normalRouteFirstByteCutover（:1754-1756，Node 是 retryUpstream &&
//     retryReason==='normal_route_first_byte_timeout'）→ Go 响应面把同一场景
//     编码为 handling.FirstByteDeadlineCutover（chain_v1.go 的 cutover verdict
//     构造点），limiting factor 取尝试级 dispatched.NormalRouteFirstByteDeadline；
//   - hard/neutral cutover 按 limitingFactor 划分（:1768-1775）：
//     configured / wall_precommit → neutral（调度决策，网关主动停读仍在存活的
//     响应——既非传输失败也非帧完成证据）；lane_timeout / uncommitted_attempt
//     → hard（合成 {kind:'timeout', reason} 传输失败，:1781-1787）；
//   - transportFailure 三元（:1777-1790）：neutral → nil；hard → 原生
//     TransportFailure 或合成 timeout；其余（非 neutral cutover）→ 原生
//     TransportFailure（含 nil）。
//   - BUG-0289 增补：codex 加密上下文清理重放轮（RetryReason =
//     StreamServerRetryCodexEncryptedContentRecovery）并入 neutral
//     （网关主动的同账户 body 变体重放，非传输失败也非帧完成证据）。
func classifyPostVerdictOutcome(
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
) postVerdictClassification {
	neutralRequestWallTermination := handling.ErrorCode == gatewayresponse.GatewayRequestWallBudgetExhaustedCode
	gatewayLocalFailure := handling.GatewayLocalFailure
	normalRouteFirstByteCutover := handling.FirstByteDeadlineCutover
	// BUG-0289：codex 加密上下文清理重放轮是网关主动发起的请求体变体重放
	//（同账户、body 去加密上下文），本 attempt 的失败是"加密上下文被拒"的
	// 确定性上游拒绝，既非传输失败、也非帧完成治愈证据——归入中性
	// （circuit ReportUnknown / keyModel ReportUnknown / 账户锁不记失败），
	// 与既有注释表口径一致；不得落入 transportFailure==nil 的
	// ReportFramingComplete 臂把 SUSPECT 推进 RECOVERING。
	codexEncryptedContentRecoveryRetry := handling.RetryUpstream &&
		handling.RetryReason == gatewayresponse.StreamServerRetryCodexEncryptedContentRecovery
	limitingFactor := ""
	if dispatched.NormalRouteFirstByteDeadline != nil {
		limitingFactor = dispatched.NormalRouteFirstByteDeadline.LimitingFactor
	}
	neutralNormalRouteFirstByteCutover := normalRouteFirstByteCutover &&
		(limitingFactor == gatewayrouting.FirstByteLimitingFactorConfigured ||
			limitingFactor == gatewayrouting.FirstByteLimitingFactorWallPrecommit)
	hardNormalRouteFirstByteCutover := normalRouteFirstByteCutover &&
		(limitingFactor == gatewayrouting.FirstByteLimitingFactorLaneTimeout ||
			limitingFactor == gatewayrouting.FirstByteLimitingFactorUncommittedAttempt)
	neutralSchedulingTermination := neutralRequestWallTermination || neutralNormalRouteFirstByteCutover || gatewayLocalFailure ||
		codexEncryptedContentRecoveryRetry
	var transportFailure *gatewayresponse.StreamTransportFailure
	switch {
	case neutralSchedulingTermination:
		// A configured speed-first deadline is a scheduling decision. The
		// gateway deliberately stopped reading this otherwise-live response,
		// so it is neither transport-failure nor framing-complete evidence.
		transportFailure = nil
	case hardNormalRouteFirstByteCutover:
		if handling.TransportFailure != nil {
			transportFailure = handling.TransportFailure
		} else {
			reason := handling.Message
			if reason == "" {
				reason = "普通路由首字硬截止已到达"
			}
			transportFailure = &gatewayresponse.StreamTransportFailure{Kind: "timeout", Reason: reason}
		}
	case handling.TransportFailure != nil:
		transportFailure = handling.TransportFailure
	}
	// explicitUserPolicyRetry（:1791-1796）：Node 判 responseInspection.
	// replayAuthority === 'explicit_user_policy'（注意区别于热质量结算用的
	// isExplicitPolicyRetry 的 Reason 字段——ReplayAuthority 还要求决策携带
	// 账户切换语义，是它的真子集）。
	explicitUserPolicyRetry := handling.RetryUpstream &&
		handling.RetryReason == gatewayresponse.StreamServerRetryResponseInspection &&
		handling.ResponseInspection != nil &&
		handling.ResponseInspection.ReplayAuthority == "explicit_user_policy"
	requestLocalProtocolFailure := handling.RetryUpstream &&
		handling.RetryReason == gatewayresponse.StreamServerRetryUpstreamProtocolFailure
	protocolValidatedSuccess := !handling.RetryUpstream && handling.ProtocolValidatedSuccess
	return postVerdictClassification{
		neutralSchedulingTermination: neutralSchedulingTermination,
		hardFirstByteCutover:         hardNormalRouteFirstByteCutover,
		transportFailure:             transportFailure,
		explicitUserPolicyRetry:      explicitUserPolicyRetry,
		requestLocalProtocolFailure:  requestLocalProtocolFailure,
		protocolValidatedSuccess:     protocolValidatedSuccess,
	}
}

// settlePostVerdictUpstreamAttempts 镜像 Node routes.ts:1780-1877 的 post-verdict
// 结算块（结算错误只记日志不改写已提交的下游，与 confirmProtocolSuccess
// SideEffects 同风格）。三类消费者的分类表（Node :1813-1823 / :1839-1848 /
// :1865-1876）：
//
//	circuit  = transportFailure 且未中止且非 downstream_connection_closed →
//	           ReportTransportFailure(kind, reason)；neutral → ReportUnknown；
//	           其余非 transportFailure → ReportFramingComplete；transportFailure
//	           但已中止/下游关闭 → 不结算（兜底 defer 以 unknown 收口，对齐
//	           Node 同形态下 circuitDecision undefined 且第二支不触发）；
//	           BUG-0289：codex 加密上下文清理重放轮并入 neutral（ReportUnknown），
//	           不计治愈证据；
//	keyModel = neutral || requestLocalProtocolFailure || explicitUserPolicyRetry
//	           → ReportUnknown；protocolValidatedSuccess → ReportCompleteSuccess；
//	           其余 → ReportUpstreamNotComplete；
//	账户锁   = transportFailure 且未中止且非 downstream_connection_closed 且
//	           !neutral 且 !requestLocalProtocolFailure 且 !hardCutover →
//	           RecordFailureAsync('upstream_body_transport_failure')。
//
// 与 Node 的顺序偏差（无行为耦合，各消费者独立记账）：Node 先锁记录（:1813）
// 再 circuit transport-failure（:1839）；Go 本块整体在 settleHotQualityTerminal
// 之后执行。cutover 分支内本块先于 BUG-0241 的 hotQuality timeout 终态执行
// （Node :1841 在 :1865 前），hotQuality 与 circuit/keyModel 为独立句柄。
func (l *v1DispatchLoop) settlePostVerdictUpstreamAttempts(
	ctx context.Context,
	dispatched gatewaydispatch.UpstreamDispatchResult,
	handling gatewayresponse.UpstreamResponseHandlingResult,
	circuitAttempt postVerdictCircuitAttempt,
	keyModelAttempt postVerdictKeyModelAttempt,
	lockRecorder postVerdictAccountLockFailureRecorder,
) {
	class := classifyPostVerdictOutcome(dispatched, handling)
	aborted := ctx.Err() != nil
	downstreamClosed := handling.ErrorCode == "downstream_connection_closed"

	// 账户锁失败侧（Node :1813-1823）：accountLockTrafficEnabled 与引擎同源
	//（TrafficSource == gateway，即链面传给引擎的 AccountStateMutationEnabled）。
	if class.transportFailure != nil && !aborted && !downstreamClosed &&
		!class.neutralSchedulingTermination && !class.requestLocalProtocolFailure && !class.hardFirstByteCutover &&
		lockRecorder != nil &&
		l.current != nil && l.current.UsageContext.TrafficSource == gatewayTrafficSource {
		if err := lockRecorder.RecordFailureAsync(ctx, dispatched.Account.ID, "upstream_body_transport_failure", dispatched.AccountLockObservation); err != nil {
			l.c.observability.Logger().Warn("gateway_account_lock_failure_record_failed", map[string]any{
				"event":     "gateway_account_lock_failure_record_failed",
				"traceId":   l.traceID,
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "响应期传输失败的账户锁失败记录未完成")
		}
	}

	// circuit 结算（Node :1839-1848 + :1865-1869）。
	if circuitAttempt != nil {
		if class.transportFailure != nil && !aborted && !downstreamClosed {
			if _, err := circuitAttempt.ReportTransportFailure(ctx, gatewaycircuit.TransportFailure{
				Kind:   class.transportFailure.Kind,
				Reason: class.transportFailure.Reason,
			}); err != nil {
				l.c.observability.Logger().Warn("gateway_account_circuit_transport_failure_report_failed", map[string]any{
					"event":     "gateway_account_circuit_transport_failure_report_failed",
					"traceId":   l.traceID,
					"accountId": dispatched.Account.ID,
					"error":     err.Error(),
				}, "账户熔断传输失败结算未完成")
			}
		} else if class.neutralSchedulingTermination {
			if _, err := circuitAttempt.ReportUnknown(ctx); err != nil {
				l.c.observability.Logger().Warn("gateway_account_circuit_unknown_report_failed", map[string]any{
					"event":     "gateway_account_circuit_unknown_report_failed",
					"traceId":   l.traceID,
					"accountId": dispatched.Account.ID,
					"error":     err.Error(),
				}, "账户熔断中性结算未完成")
			}
		} else if class.transportFailure == nil {
			if _, err := circuitAttempt.ReportFramingComplete(ctx); err != nil {
				l.c.observability.Logger().Warn("gateway_account_circuit_framing_complete_report_failed", map[string]any{
					"event":     "gateway_account_circuit_framing_complete_report_failed",
					"traceId":   l.traceID,
					"accountId": dispatched.Account.ID,
					"error":     err.Error(),
				}, "账户熔断帧完成结算未完成")
			}
		}
	}

	// keyModel 结算（Node :1870-1876）。
	if keyModelAttempt != nil {
		var err error
		switch {
		case class.neutralSchedulingTermination || class.requestLocalProtocolFailure || class.explicitUserPolicyRetry:
			err = keyModelAttempt.ReportUnknown(ctx)
		case class.protocolValidatedSuccess:
			err = keyModelAttempt.ReportCompleteSuccess(ctx)
		default:
			err = keyModelAttempt.ReportUpstreamNotComplete(ctx)
		}
		if err != nil {
			l.c.observability.Logger().Warn("gateway_key_model_attempt_report_failed", map[string]any{
				"event":     "gateway_key_model_attempt_report_failed",
				"traceId":   l.traceID,
				"accountId": dispatched.Account.ID,
				"error":     err.Error(),
			}, "key-model 尝试终态结算未完成")
		}
	}
}

// settleTransferredUpstreamAttemptsSafely 镜像 Node routes.ts:2509-2521 finally +
// :2649-2666 settleTransferredAccountCircuitAttemptSafely：引擎 OK 臂不再前置
// 结算（BUG-0267），句柄移交链面后，主结算块（settlePostVerdictUpstreamAttempts）
// 未覆盖的路径（panic、分支树早退）由本兜底收口。circuit 仅对 confirmation 尝试
// 结算（observer 尝试 no-op）；reportUnknown 重试首个 pin 定的终态意图——Go 的
// settleConfirmation 与 Node 同构（confirmationSettlementIntent 先到先得、二次
// 调用返回同一 memoized 结果，首个终态不被推翻），失败重试一次后告警留痕并
// 等待租约到期；keyModel 无条件 reportUnknown（terminalOnce 幂等，主结算已定
// 终态时为 no-op）。挂在响应轮闭包的 defer 上，即 Node 的 attempt 作用域终点。
func (l *v1DispatchLoop) settleTransferredUpstreamAttemptsSafely(
	ctx context.Context,
	accountID string,
	circuitAttempt postVerdictCircuitAttempt,
	keyModelAttempt postVerdictKeyModelAttempt,
) {
	if circuitAttempt != nil && circuitAttempt.IsConfirmation() {
		// 复审对齐：兜底结算用 WithoutCancel——请求 ctx 取消（客户端断开）时
		// 结算仍须落库释放租约（引擎侧同模式 business/gateway_dispatch），用
		// 原 ctx 会退到告警+租约到期，R2 的悬挂担忧在此路径复发。
		settleCtx := context.WithoutCancel(ctx)
		settled := false
		var lastErr error
		for retry := 0; retry < 2 && !settled; retry++ {
			if _, err := circuitAttempt.ReportUnknown(settleCtx); err != nil {
				lastErr = err
			} else {
				settled = true
			}
		}
		if !settled {
			l.c.observability.Logger().Warn("gateway_account_circuit_transferred_confirmation_settlement_failed", map[string]any{
				"event":     "gateway_account_circuit_transferred_confirmation_settlement_failed",
				"traceId":   l.traceID,
				"accountId": accountID,
				"error":     lastErr.Error(),
			}, "账户 confirmation 在上游处理结束后结算失败，保留原终态意图并等待租约到期")
		}
	}
	if keyModelAttempt != nil {
		_ = keyModelAttempt.ReportUnknown(context.WithoutCancel(ctx))
	}
}

// postVerdictCircuitHandle 把引擎带出的具体熔断句柄装箱为结算接口；nil 指针
// 保持接口 nil（typed nil 装箱陷阱见调用点注释）。
func postVerdictCircuitHandle(attempt *gatewaycircuit.Attempt) postVerdictCircuitAttempt {
	if attempt == nil {
		return nil
	}
	return attempt
}

// postVerdictKeyModelHandle 把引擎带出的具体 key-model 句柄装箱为结算接口；
// nil 指针保持接口 nil。
func postVerdictKeyModelHandle(attempt *gatewayaccounteffects.GatewayKeyModelAttempt) postVerdictKeyModelAttempt {
	if attempt == nil {
		return nil
	}
	return attempt
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

// confirmClientIPAccountAvoidanceAfterFinalFailure mirrors
// confirmCurrentClientIpAccountAvoidanceAfterFinalFailure (routes.ts:2690-2725):
// once the request failed back to the client, the tracker's pending account
// failures become client-IP avoidance entries immediately instead of waiting
// for the next request's success confirm. The tracker rides the DispatchContext
// (Node preflight.clientIpAccountAvoidanceTracker) and the avoidance service is
// the G05 factory the preauth service was assembled with.
func (l *v1DispatchLoop) confirmClientIPAccountAvoidanceAfterFinalFailure(ctx context.Context, context *gatewaypreauth.DispatchContext, reason string) {
	if gatewayusage.IsAccountDiagnosticTrafficSource(context.UsageContext.TrafficSource) {
		return
	}
	avoidance, _ := l.c.preauth.AccountAvoidance.(*gatewayclientip.Avoidance)
	tracker, _ := context.ClientIPAccountAvoidance.(*gatewayclientip.AvoidanceTracker)
	if avoidance == nil || tracker == nil {
		return
	}
	settings := context.ActiveGatewaySettings
	result, err := avoidance.ConfirmAfterFinalFailureAsync(ctx, tracker, &settings)
	if err != nil {
		// Node: a rejection here would abandon the terminal render and jump to
		// the finally block. Go keeps the terminal render (a failed avoidance
		// confirmation must not swallow the client exit) and logs instead.
		l.c.observability.Logger().Warn("gateway_client_ip_account_avoidance_confirm_failed", map[string]any{
			"event":  "gateway_client_ip_account_avoidance_confirm_failed",
			"reason": reason,
			"error":  err.Error(),
		}, "客户端 IP 级账号回避终态确认失败")
		return
	}
	if len(result.ConfirmedAccountIDs) == 0 {
		return
	}
	l.c.observability.Logger().Warn("gateway_client_ip_account_failure_confirmed_after_final_failure", map[string]any{
		"event":               "gateway_client_ip_account_failure_confirmed_after_final_failure",
		"reason":              reason,
		"confirmedAccountIds": result.ConfirmedAccountIDs,
		"systemAccountId":     context.UsageContext.SystemAccountID,
		"apiKeyId":            context.UsageContext.APIKeyID,
		"groupId":             context.UsageContext.GroupID,
		"clientIp":            context.UsageContext.ClientIP,
	}, "请求失败已返回客户端，客户端 IP 级账号回避状态已立即确认")
	l.auditCapture.AddGatewayMetadata("client_ip_account_avoidance_update", map[string]any{
		"reason":              reason,
		"confirmedAccountIds": result.ConfirmedAccountIDs,
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
			retryReason:    "pre_commit_stream_failure",
			errorCode:      gatewaypreauth.GatewayStreamClientRetryErrorCode,
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

// clientIPSlotReleaseList 收集每个 DispatchContext 的 client-IP 并发槽释放
// 闭包（D-109，BUG-0175；Node routes.ts attachClientIpSlotRelease 的
// res.once('finish'/'close') 语义：每次 preflight / 组切换都会追加一个
// release，响应终态统一触发）。release 本身是 once 幂等的。
type clientIPSlotReleaseList struct {
	mu       sync.Mutex
	releases []func()
}

// Add appends one release closure; nil releases are skipped.
func (l *clientIPSlotReleaseList) Add(release func()) {
	if release == nil {
		return
	}
	l.mu.Lock()
	l.releases = append(l.releases, release)
	l.mu.Unlock()
}

// ReleaseAll runs every collected release once (idempotent per slot).
func (l *clientIPSlotReleaseList) ReleaseAll() {
	l.mu.Lock()
	releases := l.releases
	l.releases = nil
	l.mu.Unlock()
	for _, release := range releases {
		release()
	}
}
