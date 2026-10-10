package main

// Codex 压缩请求免超时传导修复的回归测试（生产「上游流式请求 120s 内未返回
// 首段数据」根因的三处传导断线）：
//
//   - 断点 A2：loop 构造的 RequestCoordinationContext 必须从 wall budget 的
//     Unbounded 携带 TimeoutsDisabled=true（调度内核通用化设计 5.2 三轨合一：
//     压缩豁免唯一形状判定在 preflight，经 wall budget Unbounded 承载），非
//     Unbounded 保持 false；总时间档位同源判 extended；
//   - 断点 B：响应层 TimeoutProfile 必须原样使用 dispatched
//     （UpstreamDispatchResult）携带的 profile——TimeoutsDisabled=true 时
//     响应层禁超时（BuildGatewayStreamReadPlan 返回 nil）；dispatched 零值
//     profile 时回退 settings 派生（与 Node 响应层直传 dispatch 结果的契约
//     一致）。
//
// 断点 A1（chain_v1.go 从 preflight.DispatchContext 回收 budgets）在编排入口
// 内联，由行为面测试（handleUpstreamResponse 直调）+ 既有链路测试兜底。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// unboundedTestWallBudget 构造 Unbounded wall budget（preflight 对压缩请求
// WithoutLimit 后回写 DispatchContext 的实例形态）。
func unboundedTestWallBudget(t *testing.T) *gatewayrouting.GatewayRequestWallBudget {
	t.Helper()
	budget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: 1728000000000,
		Unbounded:           true,
		Now:                 func() int64 { return 1728000000000 },
	}, nil)
	if err != nil {
		t.Fatalf("构造 Unbounded wall budget: %v", err)
	}
	return budget
}

// TestV1LoopCoordinationTimeoutsDisabledMirrorsWallBudget：断点 A2——wall budget
// Unbounded（压缩请求经 preflight WithoutLimit 的实例）→ coordination 的
// TimeoutsDisabled 置位（设计 5.2 三轨合一后的参数化入参）；有界 / nil wall
// budget → false（默认超时行为）。coordination 的预算实例必须逐指针来自
// loop.budgets。
func TestV1LoopCoordinationTimeoutsDisabledMirrorsWallBudget(t *testing.T) {
	sink := &recordingFailureSink{}

	t.Run("unbounded_wall_budget_sets_timeouts_disabled", func(t *testing.T) {
		loop := newV1TestLoop(t, sink)
		budgets, err := newRequestBudgets("trace_compact", loop.startedAt, gatewaypreauth.SystemClock{})
		if err != nil {
			t.Fatalf("构造请求预算: %v", err)
		}
		budgets.wall = unboundedTestWallBudget(t)
		loop.budgets = budgets

		coordination := loop.newRequestCoordination()
		if !coordination.TimeoutsDisabled {
			t.Fatalf("Unbounded wall budget 必须置位 TimeoutsDisabled")
		}
		if coordination.TotalTimeLane != gatewaydispatch.TotalTimeLaneExtended {
			t.Fatalf("超时豁免请求总时间档位必须判 extended, got %v", coordination.TotalTimeLane)
		}
		if coordination.GatewayRequestWallBudget != budgets.wall {
			t.Fatal("coordination 必须携带 loop.budgets.wall 同一实例")
		}
		if coordination.RouteCoordinationBudget != budgets.coordination {
			t.Fatal("coordination 必须携带 loop.budgets.coordination 同一实例")
		}
		if coordination.RequestAttemptTracker != budgets.tracker {
			t.Fatal("coordination 必须携带 loop.budgets.tracker 同一实例")
		}
	})

	t.Run("bounded_wall_budget_keeps_timeouts_enabled", func(t *testing.T) {
		loop := newV1TestLoop(t, sink)
		budgets, err := newRequestBudgets("trace_plain", loop.startedAt, gatewaypreauth.SystemClock{})
		if err != nil {
			t.Fatalf("构造请求预算: %v", err)
		}
		loop.budgets = budgets

		coordination := loop.newRequestCoordination()
		if coordination.TimeoutsDisabled {
			t.Fatalf("有界 wall budget 必须保持 TimeoutsDisabled=false")
		}
		if coordination.TotalTimeLane != gatewaydispatch.TotalTimeLaneNormal {
			t.Fatalf("普通请求总时间档位必须判 normal, got %v", coordination.TotalTimeLane)
		}
	})

	t.Run("nil_wall_budget_keeps_timeouts_enabled", func(t *testing.T) {
		loop := newV1TestLoop(t, sink)
		coordination := loop.newRequestCoordination()
		if coordination.TimeoutsDisabled {
			t.Fatalf("nil wall budget 必须保持 TimeoutsDisabled=false")
		}
	})
}

// TestV1LoopAdoptsDispatchContextBudgets：换代点预算回收（RouteAction→fallback
// 与 switchToFallbackGroup 的加固）——adoptDispatchContextBudgets 必须把
// DispatchContext 携带的请求级实例（compaction 时含 WithoutLimit 的 Unbounded
// wall budget）回填 loop.budgets / loop.serverRetryBudget，adopt 后
// newRequestCoordination 立即置位 TimeoutsDisabled；nil 字段保持原值（防御），
// nil context 无操作。
func TestV1LoopAdoptsDispatchContextBudgets(t *testing.T) {
	sink := &recordingFailureSink{}

	newBudgetPair := func(t *testing.T, traceID string) (*gatewayrouting.GatewayRequestWallBudget, *gatewayrouting.RouteCoordinationBudget, *gatewayrouting.GatewayRequestAttemptTracker) {
		t.Helper()
		budgets, err := newRequestBudgets(traceID, 1728000000000, gatewaypreauth.SystemClock{})
		if err != nil {
			t.Fatalf("构造请求预算: %v", err)
		}
		return budgets.wall, budgets.coordination, budgets.tracker
	}

	t.Run("fallback_context_with_unbounded_budget_adopted", func(t *testing.T) {
		loop := newV1TestLoop(t, sink)
		selfWall, selfCoord, selfTracker := newBudgetPair(t, "trace_self")
		loop.budgets = requestBudgets{wall: selfWall, coordination: selfCoord, tracker: selfTracker}

		fallbackWall := unboundedTestWallBudget(t)
		_, fallbackCoord, fallbackTracker := newBudgetPair(t, "trace_fallback")
		fallbackRetry := gatewaypreauth.NewServerRetryBudget(270, gatewaypreauth.SystemClock{})
		loop.adoptDispatchContextBudgets(&gatewaypreauth.DispatchContext{
			ServerRetryBudget:        fallbackRetry,
			GatewayRequestWallBudget: fallbackWall,
			RouteCoordinationBudget:  fallbackCoord,
			RequestAttemptTracker:    fallbackTracker,
		})

		if loop.budgets.wall != fallbackWall || !loop.budgets.wall.Unbounded {
			t.Fatal("换代后 loop.budgets.wall 必须是 DispatchContext 携带的 Unbounded 实例")
		}
		if loop.budgets.coordination != fallbackCoord || loop.budgets.tracker != fallbackTracker {
			t.Fatal("换代后 coordination/tracker 必须逐指针替换为 DispatchContext 实例")
		}
		if loop.serverRetryBudget != fallbackRetry {
			t.Fatal("换代后 serverRetryBudget 必须替换为 DispatchContext 实例")
		}
		if !loop.newRequestCoordination().TimeoutsDisabled {
			t.Fatalf("adopt 后 coordination 必须置位 TimeoutsDisabled")
		}
	})

	t.Run("nil_fields_keep_existing_instances", func(t *testing.T) {
		loop := newV1TestLoop(t, sink)
		selfWall, selfCoord, selfTracker := newBudgetPair(t, "trace_keep")
		loop.budgets = requestBudgets{wall: selfWall, coordination: selfCoord, tracker: selfTracker}
		selfRetry := loop.serverRetryBudget

		loop.adoptDispatchContextBudgets(&gatewaypreauth.DispatchContext{})
		if loop.budgets.wall != selfWall || loop.budgets.coordination != selfCoord ||
			loop.budgets.tracker != selfTracker || loop.serverRetryBudget != selfRetry {
			t.Fatal("nil 预算字段必须保持 loop 原有实例")
		}
	})

	t.Run("nil_context_is_noop", func(t *testing.T) {
		loop := newV1TestLoop(t, sink)
		selfWall, selfCoord, selfTracker := newBudgetPair(t, "trace_noop")
		loop.budgets = requestBudgets{wall: selfWall, coordination: selfCoord, tracker: selfTracker}
		loop.adoptDispatchContextBudgets(nil)
		if loop.budgets.wall != selfWall {
			t.Fatal("nil context 不得改动 loop.budgets")
		}
	})

	// 回归锁定（复审阻断项）：编排入口的 wait observer 挂载在 adopt 之后的
	// loop.serverRetryBudget 上——挂载到最终实例后，等待边沿必须仍能驱动
	// 心跳起停（D-120 保活语义）。挂载实例若是被 adopt 丢弃的自建实例，
	// 下面的边沿回调不会触发。
	t.Run("wait_observer_edges_fire_on_adopted_retry_budget", func(t *testing.T) {
		loop := newV1TestLoop(t, sink)
		fallbackRetry := gatewaypreauth.NewServerRetryBudget(270, gatewaypreauth.SystemClock{})
		loop.adoptDispatchContextBudgets(&gatewaypreauth.DispatchContext{ServerRetryBudget: fallbackRetry})
		if loop.serverRetryBudget != fallbackRetry {
			t.Fatal("adopt 必须先替换 serverRetryBudget 实例")
		}

		started, paused := false, false
		loop.serverRetryBudget.SetWaitObserver(&gatewaypreauth.ServerRetryBudgetWaitObserver{
			OnWaitStarted: func() { started = true },
			OnWaitPaused:  func() { paused = true },
		})
		now := int64(1728000000000)
		loop.serverRetryBudget.BeginNoAvailableWait(&now)
		if !started {
			t.Fatal("adopt 后实例的等待边沿必须触发 OnWaitStarted（心跳保活）")
		}
		loop.serverRetryBudget.PauseNoAvailableWait(&now)
		if !paused {
			t.Fatal("adopt 后实例的等待暂停边沿必须触发 OnWaitPaused（心跳停止）")
		}
	})
}

// delayedSSEReader 模拟首段延迟的 SSE 上游：首次 Read 前阻塞 delay，再逐块
// 输出 payload（用于观察响应层 first_chunk 预算是否掐断慢首段流）。
type delayedSSEReader struct {
	delay   time.Duration
	waited  bool
	payload string
}

func (r *delayedSSEReader) Read(p []byte) (int, error) {
	if !r.waited {
		time.Sleep(r.delay)
		r.waited = true
	}
	if len(r.payload) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.payload)
	r.payload = r.payload[n:]
	return n, nil
}

func (r *delayedSSEReader) Close() error { return nil }

// slowCompactionSSEPayload 是完整可终结的 openai chat SSE 片段。
func slowCompactionSSEPayload() string {
	return "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"压缩慢首段内容\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
}

// dispatchedTimeoutProfileHarness 直调 handleUpstreamResponse：上游 SSE 首段
// 延迟 firstChunkDelay（超过回退派生的 first_chunk 预算），返回下游 recorder
// 与 handling 结果。attemptStartedAt 模拟 dispatch 结果携带的 attempt 起始时刻
// （0 = 引擎未带出，回退请求级 startedAt）。
func dispatchedTimeoutProfileHarness(t *testing.T, profile gatewayrouting.GatewayTimeoutProfile, settings gatewayruntimecache.GatewaySettings, firstChunkDelay time.Duration, attemptStartedAt int64) (*httptest.ResponseRecorder, gatewayresponse.UpstreamResponseHandlingResult) {
	t.Helper()
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	// 完整流式管道需要真实 gatewayusage capture（stub capture 经
	// responseAuditCaptureOf 落成 nil 接口；chain_v1_r5_cutover_test 同模式）。
	loop.auditCapture = preauthAuditCapture{inner: &gatewayusage.AuditCaptureContext{}}

	request := bodyAttachedRequest(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true,"messages":[]}`)
	recorder := httptest.NewRecorder()
	res := gatewaypreauth.NewTrackingWriter(recorder)
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_compact", Name: "压缩账户"},
		TimeoutProfile:   profile,
		AttemptStartedAt: attemptStartedAt,
		Response: gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusOK,
			http.Header{"Content-Type": []string{"text/event-stream"}},
			&delayedSSEReader{delay: firstChunkDelay, payload: slowCompactionSSEPayload()}),
	}
	handling := loop.c.handleUpstreamResponse(request, res, loop.auditCapture, loop.current, dispatched,
		loop.startedAt, settings, requestBudgets{}, nil)
	return recorder, handling
}

// dispatchedProfileFallbackSettings 是零值 dispatched profile 回退派生的
// settings 形态：first_chunk 预算 1s、attempt lifetime 10s（生产压缩事故的
// 预算关系——首响应预算先于 lifetime 掐断）。
func dispatchedProfileFallbackSettings() gatewayruntimecache.GatewaySettings {
	return gatewayruntimecache.GatewaySettings{
		TextFirstResponseTimeoutSeconds:          1,
		TextUncommittedAttemptMaxLifetimeSeconds: 10,
	}
}

// TestV1HandleUpstreamResponseHonorsDispatchedTimeoutsDisabled：断点 B 行为面
// ——dispatched 携带 TimeoutsDisabled=true（压缩请求在 dispatch 层已判定的
// profile）时，慢首段 SSE（1.6s，超过回退派生的 1s first_chunk 预算）完整送达
// 下游；同一慢上游在 dispatched 零值 profile（回退 settings 派生）下被 1s
// first_chunk 预算掐断（生产「上游流式请求 Ns 内未返回首段数据」路径，
// pre-commit 失败交上层换号，下游无内容）。
func TestV1HandleUpstreamResponseHonorsDispatchedTimeoutsDisabled(t *testing.T) {
	t.Run("dispatched_timeouts_disabled_keeps_slow_stream_alive", func(t *testing.T) {
		recorder, handling := dispatchedTimeoutProfileHarness(t,
			gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
			dispatchedProfileFallbackSettings(), 1600*time.Millisecond, 0)
		if handling.RetryUpstream || handling.AlreadyFinalized {
			t.Fatalf("禁超时慢首段流必须正常完成: %+v", handling)
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200, body=%s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "压缩慢首段内容") {
			t.Fatalf("慢首段内容必须完整送达下游: %s", recorder.Body.String())
		}
	})

	t.Run("zero_dispatched_profile_falls_back_to_settings_budget", func(t *testing.T) {
		recorder, handling := dispatchedTimeoutProfileHarness(t,
			gatewayrouting.GatewayTimeoutProfile{}, dispatchedProfileFallbackSettings(), 1600*time.Millisecond, 0)
		if !handling.RetryUpstream || handling.ErrorCode == "" {
			t.Fatalf("回退 1s first_chunk 预算必须掐断慢首段流（失败交上层）: %+v", handling)
		}
		if strings.Contains(handling.Message, "内未返回首段数据") != true {
			t.Fatalf("失败信息必须是首段预算超时契约: %q", handling.Message)
		}
		if strings.Contains(recorder.Body.String(), "压缩慢首段内容") {
			t.Fatalf("慢首段内容不得送达下游（已被预算掐断）: %s", recorder.Body.String())
		}
	})
}

// TestV1HandleUpstreamResponseAttemptAnchoredBudget：响应层超时锚点必须是
// attempt 级（dispatched.AttemptStartedAt，对齐 Node routes.ts:1574/:1604 从
// upstreamResult 解构 attemptStartedAt 传给响应层）——多账户换号重试时每个
// attempt 的 first_chunk 预算独立计量，不共享请求级起点。回归场景（生产
// BUG-0266 的 attempt2-9 秒败形态）：首响应预算 5s，attempt 锚点比请求级
// 起点晚 10s（attemptStartedAt=now+10s、loop.startedAt 为脚手架固定远古值，
// 预算由差值决定，等价于"请求已进行 10s 后换号"）；共享请求级起点则预算
// 5000-10000 ≤ 0 → 流一建立立即「未返回首段数据」秒败；attempt 级锚点则
// 预算完整，1.2s 慢首段正常送达。
func TestV1HandleUpstreamResponseAttemptAnchoredBudget(t *testing.T) {
	settings := gatewayruntimecache.GatewaySettings{
		TextFirstResponseTimeoutSeconds:          5,
		TextStreamIdleTimeoutSeconds:             30,
		TextUncommittedAttemptMaxLifetimeSeconds: 60,
	}
	// attempt 锚点比请求级起点晚 10s（预算判定只看锚点差值）。
	attemptStartedAt := time.Now().UnixMilli() + 10_000

	recorder, handling := dispatchedTimeoutProfileHarness(t,
		gatewayrouting.GatewayTimeoutProfile{}, settings, 1200*time.Millisecond, attemptStartedAt)
	if handling.RetryUpstream || handling.AlreadyFinalized {
		t.Fatalf("attempt 级锚点下换号账户必须拥有完整首响应预算（不得秒败）: %+v", handling)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "压缩慢首段内容") {
		t.Fatalf("慢首段内容必须完整送达下游: %s", recorder.Body.String())
	}

	// 对照：零值 AttemptStartedAt（引擎未带出）回退请求级锚点——请求已过
	// 预算上限时立即掐断，保留既有兜底行为。
	sink := &recordingFailureSink{}
	loop := newV1TestLoop(t, sink)
	loop.auditCapture = preauthAuditCapture{inner: &gatewayusage.AuditCaptureContext{}}
	request := bodyAttachedRequest(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"gpt-test","stream":true,"messages":[]}`)
	recorder2 := httptest.NewRecorder()
	res := gatewaypreauth.NewTrackingWriter(recorder2)
	// 请求级 startedAt 定在 10s 前：零值 attempt 锚点回退后预算耗尽。
	staleStartedAt := time.Now().UnixMilli() - 10_000
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account:        gatewaydispatch.AccountCandidate{ID: "acc_plain", Name: "普通账户"},
		TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{},
		Response: gatewaydispatch.NewGatewayUpstreamResponseForTransform(http.StatusOK,
			http.Header{"Content-Type": []string{"text/event-stream"}},
			&delayedSSEReader{delay: 1200 * time.Millisecond, payload: slowCompactionSSEPayload()}),
	}
	handling2 := loop.c.handleUpstreamResponse(request, res, loop.auditCapture, loop.current, dispatched,
		staleStartedAt, settings, requestBudgets{}, nil)
	if !handling2.RetryUpstream {
		t.Fatalf("零值锚点回退请求级起点时预算耗尽必须掐断: %+v", handling2)
	}
}

// TestV1ResponseTimeoutProfileOf：断点 B 传导函数——dispatched profile 非零值
// 时逐字段投影（含 TimeoutsDisabled）；零值时回退 settings 派生。
// BuildGatewayStreamReadPlan(profile)==nil 是响应层禁超时的行为断言
// （timeoutsDisabled → 无 read plan）。
func TestV1ResponseTimeoutProfileOf(t *testing.T) {
	readPlanNil := func(t *testing.T, profile gatewayresponse.TimeoutProfile) {
		t.Helper()
		if plan := gatewayresponse.BuildGatewayStreamReadPlan(profile, 0, gatewayresponse.StreamReadPlanStatus{
			WaitingForFirstChunk: true,
		}, 0); plan != nil {
			t.Fatalf("禁超时 profile 必须无 read plan: %+v", plan)
		}
	}

	t.Run("dispatched_disabled_profile_disables_read_plan", func(t *testing.T) {
		profile := responseTimeoutProfileOf(gatewaydispatch.UpstreamDispatchResult{
			TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{TimeoutsDisabled: true},
		}, gatewayruntimecache.GatewaySettings{}, "text")
		if !profile.TimeoutsDisabled {
			t.Fatalf("TimeoutsDisabled 必须原样传导: %+v", profile)
		}
		readPlanNil(t, profile)
	})

	t.Run("dispatched_profile_maps_consumed_fields", func(t *testing.T) {
		profile := responseTimeoutProfileOf(gatewaydispatch.UpstreamDispatchResult{
			TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{
				FirstResponseTimeoutMs:          5000,
				NonStreamFirstResponseTimeoutMs: 6000,
				FirstByteTimeoutMs:              7000,
				IdleTimeoutMs:                   8000,
				UncommittedAttemptMaxLifetimeMs: 9000,
				NoAvailableAccountWaitMs:        10000,
			},
		}, gatewayruntimecache.GatewaySettings{}, "text")
		if profile.TimeoutsDisabled {
			t.Fatalf("非禁超时 profile 不得误判禁超时: %+v", profile)
		}
		if profile.FirstResponseTimeoutMs != 5000 || profile.IdleTimeoutMs != 8000 ||
			profile.UncommittedAttemptMaxLifetimeMs != 9000 {
			t.Fatalf("响应层消费字段必须逐字段映射: %+v", profile)
		}
		// 非 image 泳道的 image 设置不得泄漏进回退无关的直传 profile。
		settings := gatewayruntimecache.GatewaySettings{TextFirstResponseTimeoutSeconds: 33, ImageFirstResponseTimeoutSeconds: 44}
		derived := responseTimeoutProfileOf(gatewaydispatch.UpstreamDispatchResult{
			TimeoutProfile: gatewayrouting.GatewayTimeoutProfile{FirstResponseTimeoutMs: 5000},
		}, settings, "text")
		if derived.FirstResponseTimeoutMs != 5000 {
			t.Fatalf("dispatched 非零 profile 必须优先于 settings: %+v", derived)
		}
	})

	t.Run("zero_dispatched_profile_falls_back_to_settings_derivation", func(t *testing.T) {
		settings := gatewayruntimecache.GatewaySettings{
			TextFirstResponseTimeoutSeconds:          33,
			TextStreamIdleTimeoutSeconds:             22,
			TextUncommittedAttemptMaxLifetimeSeconds: 11,
		}
		profile := responseTimeoutProfileOf(gatewaydispatch.UpstreamDispatchResult{}, settings, "text")
		if profile.TimeoutsDisabled {
			t.Fatalf("settings 回退派生不得禁超时: %+v", profile)
		}
		if profile.FirstResponseTimeoutMs != 33000 || profile.IdleTimeoutMs != 22000 ||
			profile.UncommittedAttemptMaxLifetimeMs != 11000 {
			t.Fatalf("回退必须来自 settings 派生: %+v", profile)
		}
		if plan := gatewayresponse.BuildGatewayStreamReadPlan(profile, 0, gatewayresponse.StreamReadPlanStatus{
			WaitingForFirstChunk: true,
		}, 0); plan == nil {
			t.Fatal("非禁超时 profile 必须有 read plan")
		}
	})
}
