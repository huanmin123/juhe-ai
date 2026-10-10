package main

// 普通路由速度优先总时间兜底截止（设计 6.3-6.7）的 chain 侧测试：到期决策
// 闭包（记样本/写出检查/预占切号）、响应轮软观察 timer、完成观测（压缩守
// 卫 + 维度独立去重）、总时间切号消费与 port 透传。外部面 stub 复用既有
// w1 家族（w1FakeLocks / w1FakeConcurrencyStore / w1LoopWithEngine）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// speedFirstTotalTimeFake 分维度记录决策面调用（首字与总时间通道独立计数，
// 供"并存各自记录"与"压缩守卫首字零调用"断言）。
type speedFirstTotalTimeFake struct {
	// mu 保护全部计数与切片：软观察 timer / transport timer 回调跑在独立
	// goroutine 上，与测试主 goroutine 的断言轮询构成并发读写（race 门禁
	// 修复），所有 Record* 写入与断言读取必须持锁。
	mu sync.Mutex

	degraded map[string]bool

	firstSlowCalls    int
	firstSuccessCalls int
	firstSlowReasons  []string

	totalSlowCalls      int
	totalSuccessCalls   int
	totalSlowReasons    []string
	totalSuccessArgs    [][2]int64
	totalSlowLastConfig *gatewayproxyhealth.SpeedFirstRuntimeConfig

	degradedErr   error
	totalSlowErr  error
	eligibleErr   error
	degradedCalls int
}

func (f *speedFirstTotalTimeFake) lock() func() {
	f.mu.Lock()
	return f.mu.Unlock
}

func (f *speedFirstTotalTimeFake) OrderAsync(context.Context, []gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	if f.eligibleErr != nil {
		return gatewaydispatch.LatencyDegradationOrder{}, f.eligibleErr
	}
	return gatewaydispatch.LatencyDegradationOrder{}, nil
}

func (f *speedFirstTotalTimeFake) IsAccountLatencyDegradedAsync(_ context.Context, account gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput) (bool, error) {
	f.degradedCalls++
	if f.degradedErr != nil {
		return false, f.degradedErr
	}
	if f.degradedCalls > 1 && f.eligibleErr != nil {
		return false, f.eligibleErr
	}
	return f.degraded[account.ID], nil
}

func (f *speedFirstTotalTimeFake) RecordFirstByteSlowAsync(_ context.Context, _ gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, _ *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, reason string) (*gatewayproxyhealth.LatencySlowResult, error) {
	defer f.lock()()
	f.firstSlowCalls++
	f.firstSlowReasons = append(f.firstSlowReasons, reason)
	return &gatewayproxyhealth.LatencySlowResult{SlowCount: 1}, nil
}

func (f *speedFirstTotalTimeFake) RecordFirstByteSuccessAsync(context.Context, gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
	defer f.lock()()
	f.firstSuccessCalls++
	return &gatewayproxyhealth.LatencySuccessResult{Cleared: true}, nil
}

func (f *speedFirstTotalTimeFake) RecordTotalTimeSlowAsync(_ context.Context, _ gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, reason string) (*gatewayproxyhealth.LatencySlowResult, error) {
	defer f.lock()()
	f.totalSlowCalls++
	f.totalSlowReasons = append(f.totalSlowReasons, reason)
	f.totalSlowLastConfig = chainSpeedFirstRuntimeConfigOf(config)
	if f.totalSlowErr != nil {
		return nil, f.totalSlowErr
	}
	return &gatewayproxyhealth.LatencySlowResult{SlowCount: 1, Degraded: f.degradedForSlow()}, nil
}

// degradedForSlow 只在 RecordTotalTimeSlowAsync 持锁内调用（sync.Mutex
// 不可重入）：调用方已持 f.mu，这里不再加锁。
func (f *speedFirstTotalTimeFake) degradedForSlow() bool {
	return f.totalSlowCalls >= 2
}

// totalSlowCallsSnapshot 带锁读取总时间慢样本计数（timer goroutine 与测试
// 主 goroutine 并发，race 门禁要求读取持锁）。
func (f *speedFirstTotalTimeFake) totalSlowCallsSnapshot() int {
	defer f.lock()()
	return f.totalSlowCalls
}

func (f *speedFirstTotalTimeFake) RecordTotalTimeSuccessAsync(_ context.Context, _ gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, _ *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, effectiveDeadlineMs int64, elapsedMs int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
	defer f.lock()()
	f.totalSuccessCalls++
	f.totalSuccessArgs = append(f.totalSuccessArgs, [2]int64{effectiveDeadlineMs, elapsedMs})
	return &gatewayproxyhealth.LatencySuccessResult{Cleared: true}, nil
}

// speedFirstTotalTimeReasonMatches 校验总时间慢样本文案骨架：前缀 + 起始
// 耗时数字（容忍毫秒翻动，prefix 传 "1210" 匹配 121000..121009）+ 固定阈值。
func speedFirstTotalTimeReasonMatches(reason, elapsedPrefix string, thresholdMs int64) bool {
	const head = "普通路由速度优先总时间耗时 "
	const tailFormat = "ms 超过阈值 %dms"
	if !strings.HasPrefix(reason, head) || !strings.HasSuffix(reason, fmt.Sprintf(tailFormat, thresholdMs)) {
		return false
	}
	rest := strings.TrimPrefix(reason, head)
	rest = strings.TrimSuffix(rest, fmt.Sprintf(tailFormat, thresholdMs))
	return strings.HasPrefix(rest, elapsedPrefix)
}

// speedFirstTotalTimeConfig 构造带总时间两阈值的速度优先运行态配置。
func speedFirstTotalTimeConfig(totalMs, compactionMs int64) *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig {
	return &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		SchedulingPreference:          "speed_first",
		FirstByteDeadlineMs:           int64PtrOf(8_000),
		TotalTimeDeadlineMs:           int64PtrOf(totalMs),
		CompactionTotalTimeDeadlineMs: int64PtrOf(compactionMs),
		Raw:                           map[string]any{"speedFirstConfig": map[string]any{"maxFirstByteRetriesPerRequest": 3}},
	}
}

// totalDeadlineLoop 构造决策/观测共用的 loop 前置：延迟决策面 + 双账户候选
// 窗口 + 速度优先配置 + 文本 lane。压缩门默认关闭（wall 无 nil）。
func totalDeadlineLoop(t *testing.T, fake *speedFirstTotalTimeFake) *v1DispatchLoop {
	t.Helper()
	loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
	loop.c.engine.Latency = fake
	loop.current.UsageContext.SystemAccountID = "sys_1"
	loop.current.UsageContext.GroupID = "grp_main"
	loop.current.APIKeyRecord = &gatewayruntimecache.GatewayAPIKeyRow{RouteStrategyID: "rs_1"}
	loop.current.RequestLane = "text"
	loop.current.Accounts = []gatewaydispatch.AccountCandidate{
		{ID: "acc_1", Name: "慢账户"},
		{ID: "acc_2", Name: "目标账户"},
	}
	loop.current.NormalRouteSpeedFirstConfig = speedFirstTotalTimeConfig(120_000, 300_000)
	return loop
}

// 决策内部错误透传（镜像首字档：慢采样失败向上抛，由入口闭包兜底
// continue；候选降级查询失败同样透传）。
func TestSpeedFirstTotalTimeDeadlineDecisionErrorPropagation(t *testing.T) {
	// 降级查询错误：第一次查询即失败，向上抛由闭包兜底。
	t.Run("降级查询错误透传", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{degradedErr: errors.New("降级查询失败")}
		loop := totalDeadlineLoop(t, fake)
		_, err := loop.speedFirstTotalTimeDeadlineDecision(context.Background(), loop.current,
			gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"},
			60_000, 60_500, loop.speedFirstDecisionsOf(),
			loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig)
		if err == nil {
			t.Fatal("降级查询错误必须透传")
		}
	})

	// 剩余候选评估里的降级查询失败（第 2 次起报错）：同样透传。
	t.Run("剩余候选评估错误透传", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{eligibleErr: errors.New("候选降级查询失败")}
		loop := totalDeadlineLoop(t, fake)
		_, err := loop.speedFirstTotalTimeDeadlineDecision(context.Background(), loop.current,
			gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"},
			60_000, 60_500, loop.speedFirstDecisionsOf(),
			loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig)
		if err == nil {
			t.Fatal("剩余候选评估错误必须透传")
		}
	})
}

func TestSpeedFirstTotalTimeDeadlineDecision(t *testing.T) {
	run := func(t *testing.T, fake *speedFirstTotalTimeFake, mutate func(loop *v1DispatchLoop)) (gatewaydispatch.FirstByteDeadlineAction, *v1DispatchLoop) {
		t.Helper()
		loop := totalDeadlineLoop(t, fake)
		if mutate != nil {
			mutate(loop)
		}
		action, err := loop.speedFirstTotalTimeDeadlineDecision(context.Background(), loop.current,
			gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"},
			120_000, 120_500, loop.speedFirstDecisionsOf(),
			loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig)
		if err != nil {
			t.Fatalf("决策错误 = %v", err)
		}
		return action, loop
	}

	// 未确认慢：只记总时间慢样本，继续当前上游（软观察）。
	t.Run("未确认慢只记样本继续", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		action, loop := run(t, fake, nil)
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
		if fake.totalSlowCalls != 1 {
			t.Fatalf("totalSlowCalls = %d", fake.totalSlowCalls)
		}
		if loop.speedFirstTotalTimeSlowObservedForAttempt == nil {
			t.Fatal("总时间去重标记必须写入")
		}
	})

	// 确认慢 + 未写出 + 有候选：预占成功并返回 abort，载荷槽写入。
	t.Run("确认慢未写出切号", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{degraded: map[string]bool{"acc_1": true}}
		store := &w1FakeConcurrencyStore{}
		action, loop := run(t, fake, func(loop *v1DispatchLoop) {
			loop.c.engine.Concurrency = store
		})
		if action != gatewaydispatch.FirstByteDeadlineActionAbort {
			t.Fatalf("action = %v", action)
		}
		if loop.speedFirstCutoverReservation == nil {
			t.Fatal("切号预留必须写入 loop 携带槽")
		}
		if loop.speedFirstTotalTimeCutoverSignal == nil || loop.speedFirstTotalTimeCutoverSignal.accountID != "acc_1" {
			t.Fatalf("载荷槽 = %+v", loop.speedFirstTotalTimeCutoverSignal)
		}
		if len(store.acquired) == 0 {
			t.Fatal("切号目标必须尝试获取并发槽")
		}
	})

	// 已向下游写出：只记样本继续等待，不进入切号裁决；审计携带
	// dimension=total_time 与 retryBlockedReason=downstream_committed（设计 6.7）。
	t.Run("已写出只记样本", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{degraded: map[string]bool{"acc_1": true}}
		store := &w1FakeConcurrencyStore{}
		rec := &recordingMetadataCapture{}
		action, loop := run(t, fake, func(loop *v1DispatchLoop) {
			loop.c.engine.Concurrency = store
			loop.waitCommitState = &gatewayresponse.DownstreamCommitState{SemanticCommitted: true}
			loop.auditCapture = rec
		})
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
		if fake.totalSlowCalls != 1 {
			t.Fatalf("慢样本必须照记 totalSlowCalls = %d", fake.totalSlowCalls)
		}
		if loop.speedFirstCutoverReservation != nil || loop.speedFirstTotalTimeCutoverSignal != nil {
			t.Fatal("已写出请求不得预占切号")
		}
		observed := rec.byLabel("normal_route_speed_first_total_time_cutover_blocked")
		if observed == nil {
			t.Fatal("已写出切号阻断必须写审计 normal_route_speed_first_total_time_cutover_blocked")
		}
		if observed["dimension"] != gatewayproxyhealth.LatencyDimensionTotalTime {
			t.Fatalf("dimension = %v", observed["dimension"])
		}
		if observed["retryBlockedReason"] != "downstream_committed" {
			t.Fatalf("retryBlockedReason = %v", observed["retryBlockedReason"])
		}
	})

	// 重试上限耗尽：只记样本继续。
	t.Run("重试上限耗尽", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{degraded: map[string]bool{"acc_1": true}}
		action, _ := run(t, fake, func(loop *v1DispatchLoop) {
			loop.speedFirstByteRetryCount = 3
		})
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
	})

	// 跨账户锁阻断：只记样本继续（与首字同型）。
	t.Run("锁阻断", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{degraded: map[string]bool{"acc_1": true}}
		action, loop := run(t, fake, func(loop *v1DispatchLoop) {
			loop.c.engine.Locks = &w1FakeLocks{view: &gatewaydispatch.AccountLockStateView{BlocksCrossAccount: true}}
		})
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
		if loop.speedFirstCutoverReservation != nil {
			t.Fatal("锁阻断不得预占切号")
		}
	})

	// 慢采样错误透传（闭包兜底 continue）。
	t.Run("慢采样错误透传", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{totalSlowErr: errors.New("总时间采样失败")}
		loop := totalDeadlineLoop(t, fake)
		_, err := loop.speedFirstTotalTimeDeadlineDecision(context.Background(), loop.current,
			gatewaydispatch.AccountCandidate{ID: "acc_1"},
			120_000, 120_500, loop.speedFirstDecisionsOf(),
			loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig)
		if err == nil {
			t.Fatal("慢采样错误必须透传")
		}
	})
}

func TestSettleTotalTimeCutoverError(t *testing.T) {
	// 有载荷槽 + 预留：预留接回 loop 携带槽、窗口收窄到保留目标、返回 false 续环。
	t.Run("确认切号收窄重派", func(t *testing.T) {
		loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
		reservation := w1Reservation(t)
		view := speedFirstReservationViewOf(reservation)
		loop.recordAttachedSpeedFirstReservation(view, reservation)
		loop.speedFirstCutoverReservation = reservation
		loop.speedFirstTotalTimeCutoverSignal = &speedFirstTotalTimeCutoverSignal{
			accountID:   "acc_slow",
			accountName: "慢账户",
			thresholdMs: 300_000,
			elapsedMs:   300_500,
		}
		settled := loop.settleTotalTimeCutoverError(context.Background(), &gatewaydispatch.NormalRouteTotalTimeTimeoutError{
			Message:   "总时间截止",
			TimeoutMs: 300_000,
			ElapsedMs: 300_500,
		})
		if settled {
			t.Fatal("预留携带臂不得结算")
		}
		if loop.speedFirstByteRetryCount != 1 {
			t.Fatalf("retry count = %d", loop.speedFirstByteRetryCount)
		}
		if loop.speedFirstCutoverReservation != reservation {
			t.Fatal("预留必须接回为具体预留")
		}
		if _, narrowed := loop.speedFirstRetryCandidateAccountIds["acc_target"]; !narrowed {
			t.Fatalf("候选窗口 = %v", loop.speedFirstRetryCandidateAccountIds)
		}
	})
	// 无载荷槽（决策未确认切号的 abort）：按耗尽契约渲染。
	t.Run("无载荷槽耗尽渲染", func(t *testing.T) {
		loop, _ := w1LoopWithEngine(t, &w1FakeLocks{})
		settled := loop.settleTotalTimeCutoverError(context.Background(), &gatewaydispatch.NormalRouteTotalTimeTimeoutError{
			Message:   "总时间截止",
			TimeoutMs: 120_000,
		})
		if !settled {
			t.Fatal("无载荷槽必须按耗尽结算")
		}
	})
}

func TestObserveSpeedFirstTotalTimeOutcome(t *testing.T) {
	baseLoop := func(t *testing.T, fake *speedFirstTotalTimeFake) (*v1DispatchLoop, *gatewaypreauth.DispatchContext) {
		t.Helper()
		loop := totalDeadlineLoop(t, fake)
		return loop, loop.current
	}
	nowMs := time.Now().UnixMilli()
	slowDispatched := gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
		AttemptStartedAt: nowMs - 121_000,
	}
	fastDispatched := gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
		AttemptStartedAt: nowMs - 1_000,
	}
	firstToken := int64(500)
	handling := gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken}

	// 超阈值补记一次；timer 已记样本（去重标记非 nil）时不再补记。
	t.Run("超阈值补记与去重", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop, current := baseLoop(t, fake)
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, slowDispatched, handling)
		if fake.totalSlowCalls != 1 {
			t.Fatalf("totalSlowCalls = %d", fake.totalSlowCalls)
		}
		// elapsed 取真实时钟（NowMs - AttemptStartedAt），构造基准与观测点
		// 之间允许毫秒级翻动，断言只锚定文案骨架与档位阈值。
		if len(fake.totalSlowReasons) != 1 || !speedFirstTotalTimeReasonMatches(fake.totalSlowReasons[0], "1210", 120_000) {
			t.Fatalf("slow reasons = %v", fake.totalSlowReasons)
		}
		// timer 路径已写去重标记：完成观测不再补记（每 attempt 一次）。
		loop.speedFirstTotalTimeSlowObservedForAttempt = &gatewayproxyhealth.LatencySlowResult{}
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, slowDispatched, handling)
		if fake.totalSlowCalls != 1 {
			t.Fatalf("同 attempt 必须去重 totalSlowCalls = %d", fake.totalSlowCalls)
		}
	})

	// 达标：记恢复样本，effectiveDeadlineMs 用选定档阈值（同一样本一把尺）。
	t.Run("达标记恢复", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop, current := baseLoop(t, fake)
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, fastDispatched, handling)
		if fake.totalSuccessCalls != 1 {
			t.Fatalf("totalSuccessCalls = %d", fake.totalSuccessCalls)
		}
		args := fake.totalSuccessArgs[0]
		if args[0] != 120_000 {
			t.Fatalf("effectiveDeadlineMs = %d, want 120000", args[0])
		}
		if args[1] <= 0 || args[1] > 120_000 {
			t.Fatalf("elapsedMs = %d", args[1])
		}
	})

	// 压缩守卫：压缩请求（wall unbounded）不走首字通道（慢补记与恢复均禁），
	// 总时间通道照常生效。
	t.Run("压缩守卫首字禁用", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop, current := baseLoop(t, fake)
		loop.budgets.wall = &gatewayrouting.GatewayRequestWallBudget{Unbounded: true}
		// 压缩请求首字豁免：config.FirstByteDeadlineMs 为 nil。
		loop.current.NormalRouteSpeedFirstConfig = &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
			SchedulingPreference:          "speed_first",
			TotalTimeDeadlineMs:           int64PtrOf(120_000),
			CompactionTotalTimeDeadlineMs: int64PtrOf(300_000),
			Raw:                           map[string]any{"speedFirstConfig": map[string]any{"maxFirstByteRetriesPerRequest": 3}},
		}
		// 压缩 + 首字超阈值（125s）+ 总时间超压缩档阈值（300s）：首字通道零
		// 调用，总时间按压缩档阈值补记（成功压缩首输出 125s 属正常形态）。
		longFirstToken := int64(125_000)
		compactionSlowDispatched := gatewaydispatch.UpstreamDispatchResult{
			Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
			AttemptStartedAt: nowMs - 301_000,
		}
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, compactionSlowDispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &longFirstToken})
		if fake.firstSlowCalls != 0 || fake.firstSuccessCalls != 0 {
			t.Fatalf("压缩请求不得进入首字通道 slow=%d success=%d", fake.firstSlowCalls, fake.firstSuccessCalls)
		}
		if fake.totalSlowCalls != 1 {
			t.Fatalf("压缩请求总时间补记缺失 totalSlowCalls = %d", fake.totalSlowCalls)
		}
		if len(fake.totalSlowReasons) != 1 || !speedFirstTotalTimeReasonMatches(fake.totalSlowReasons[0], "3010", 300_000) {
			t.Fatalf("压缩档必须用 300s 阈值 slow reasons = %v", fake.totalSlowReasons)
		}
		// 压缩 + 首字达标：同样不进首字恢复通道。
		fake2 := &speedFirstTotalTimeFake{}
		loop2, current2 := baseLoop(t, fake2)
		loop2.budgets.wall = &gatewayrouting.GatewayRequestWallBudget{Unbounded: true}
		loop2.current.NormalRouteSpeedFirstConfig = loop.current.NormalRouteSpeedFirstConfig
		loop2.observeSpeedFirstResponseOutcome(context.Background(), current2, fastDispatched, handling)
		if fake2.firstSuccessCalls != 0 {
			t.Fatalf("压缩请求不得记首字恢复 successCalls = %d", fake2.firstSuccessCalls)
		}
		if fake2.totalSuccessCalls != 1 {
			t.Fatalf("压缩请求总时间恢复缺失 totalSuccessCalls = %d", fake2.totalSuccessCalls)
		}
	})

	// 首字与总时间同 attempt 并存：两条样本各自记录（维度独立，互不吞并）。
	t.Run("首字总时间并存各自记录", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop, current := baseLoop(t, fake)
		// 首字 9s 超首字阈值（8s）、总时间 121s 超普通档阈值（120s）。
		slowFirstToken := int64(9_000)
		loop.observeSpeedFirstResponseOutcome(context.Background(), current, slowDispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slowFirstToken})
		if fake.firstSlowCalls != 1 || fake.totalSlowCalls != 1 {
			t.Fatalf("slow calls first=%d total=%d", fake.firstSlowCalls, fake.totalSlowCalls)
		}
		// 首字去重标记不抑制总时间补记（维度独立去重）。
		loop2 := totalDeadlineLoop(t, fake)
		loop2.speedFirstSlowObservedForAttempt = &gatewayproxyhealth.LatencySlowResult{}
		loop2.observeSpeedFirstResponseOutcome(context.Background(), loop2.current, slowDispatched,
			gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &slowFirstToken})
		if fake.totalSlowCalls != 2 {
			t.Fatalf("首字标记不得抑制总时间补记 totalSlowCalls = %d", fake.totalSlowCalls)
		}
	})
}

func TestArmTotalTimeDeadlineObserver(t *testing.T) {
	// 到期软观察：只记慢样本，不切号；去重后不重复。
	t.Run("到期记样本", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop := totalDeadlineLoop(t, fake)
		loop.current.NormalRouteSpeedFirstConfig = speedFirstTotalTimeConfig(50, 300_000)
		nowMs := time.Now().UnixMilli()
		dispatched := gatewaydispatch.UpstreamDispatchResult{
			Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
			AttemptStartedAt: nowMs - 100,
		}
		finished := &atomic.Int32{}
		stop := loop.armTotalTimeDeadlineObserver(context.Background(), loop.current, dispatched, finished)
		defer stop()
		deadline := time.Now().Add(2 * time.Second)
		for fake.totalSlowCallsSnapshot() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if got := fake.totalSlowCallsSnapshot(); got != 1 {
			t.Fatalf("totalSlowCalls = %d", got)
		}
		if loop.speedFirstCutoverReservation != nil {
			t.Fatal("软观察 timer 不得切号")
		}
		// 去重：同一 attempt 二次到点不重复记录。
		loop.observeTotalTimeDeadlineSample(context.Background(), loop.current, dispatched, 50)
		if got := fake.totalSlowCallsSnapshot(); got != 1 {
			t.Fatalf("去重失败 totalSlowCalls = %d", got)
		}
	})
	// finished 置位（响应轮已返回）后到点不触发。
	t.Run("完成后不触发", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop := totalDeadlineLoop(t, fake)
		loop.current.NormalRouteSpeedFirstConfig = speedFirstTotalTimeConfig(50, 300_000)
		dispatched := gatewaydispatch.UpstreamDispatchResult{
			Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
			AttemptStartedAt: time.Now().UnixMilli() - 100,
		}
		finished := &atomic.Int32{}
		finished.Store(1)
		stop := loop.armTotalTimeDeadlineObserver(context.Background(), loop.current, dispatched, finished)
		defer stop()
		time.Sleep(100 * time.Millisecond)
		if got := fake.totalSlowCallsSnapshot(); got != 0 {
			t.Fatalf("完成后不得触发 totalSlowCalls = %d", got)
		}
	})
}

func TestChainLatencyDegradationPortTotalTimeRecords(t *testing.T) {
	service := gatewayproxyhealth.NewLatencyDegradationService(gatewayproxyhealth.NewMemoryRuntimeStateStore(nil), nil, gatewayproxyhealth.LatencyDegradationOptions{
		LockRetryDelay: func(int) {},
	})
	port := chainLatencyDegradationPort{service: service}
	scope := &gatewaydispatch.LatencyScopeInput{SystemAccountID: "sys-1", RouteStrategyID: "rs-1", GroupID: "grp-1"}
	config := speedFirstTotalTimeConfig(120_000, 300_000)
	config.Raw["speedFirstConfig"] = map[string]any{"slowTriggerCount": 2}
	account := gatewaydispatch.AccountCandidate{ID: "acc-slow"}

	// 总时间慢样本两次触发降级（total_time 通道计数）。
	if _, err := port.RecordTotalTimeSlowAsync(context.Background(), account, scope, config, "总时间慢 1"); err != nil {
		t.Fatalf("slow 1: %v", err)
	}
	if _, err := port.RecordTotalTimeSlowAsync(context.Background(), account, scope, config, "总时间慢 2"); err != nil {
		t.Fatalf("slow 2: %v", err)
	}
	degraded, err := port.IsAccountLatencyDegradedAsync(context.Background(), account, scope)
	if err != nil || !degraded {
		t.Fatalf("degraded = %v err %v", degraded, err)
	}
	// 达标门槛：elapsed 超过 effectiveDeadlineMs 的样本被忽略（服务侧门槛）。
	blocked, err := port.RecordTotalTimeSuccessAsync(context.Background(), account, scope, config, 120_000, 130_000)
	if err != nil || blocked != nil {
		t.Fatalf("超阈值达标必须被门槛忽略 = %+v err %v", blocked, err)
	}
	// 达标累计 recoverySuccessCount（默认 3）后清理降级。
	for i := 1; i <= 3; i++ {
		result, err := port.RecordTotalTimeSuccessAsync(context.Background(), account, scope, config, 120_000, int64(1_000*i))
		if err != nil {
			t.Fatalf("success %d: %v", i, err)
		}
		if i == 3 && (result == nil || !result.Cleared) {
			t.Fatalf("第三次达标应清理，got %+v", result)
		}
	}
	cleared, err := port.IsAccountLatencyDegradedAsync(context.Background(), account, scope)
	if err != nil || cleared {
		t.Fatalf("cleared = %v err %v", cleared, err)
	}
	// nil 端口直通。
	var nilPort chainLatencyDegradationPort
	result, err := nilPort.RecordTotalTimeSlowAsync(context.Background(), account, scope, config, "慢")
	if err != nil || result != nil {
		t.Fatalf("nil port = %+v err %v", result, err)
	}
}

// 压缩端到端切号（设计 6.9 压缩项）：压缩形态（wall 无界 + 首字阈值缺省）
// 下总时间决策照常工作——确认慢 + 未写出 + 有候选 → abort + 预占 + 载荷槽
// 按压缩档阈值；切号后 compact 契约重查为既有链（chain_v1 主流程）。
func TestSpeedFirstTotalTimeDeadlineDecisionCompactionLane(t *testing.T) {
	fake := &speedFirstTotalTimeFake{degraded: map[string]bool{"acc_1": true}}
	store := &w1FakeConcurrencyStore{}
	loop := totalDeadlineLoop(t, fake)
	// 压缩形态：wall 无界（preflight 压缩判定承载，设计 5.2 三轨合一）+
	// 首字阈值缺省（preflight 对压缩请求只豁免首字段）。
	loop.budgets.wall = &gatewayrouting.GatewayRequestWallBudget{Unbounded: true}
	loop.current.NormalRouteSpeedFirstConfig = &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		SchedulingPreference:          "speed_first",
		CompactionTotalTimeDeadlineMs: int64PtrOf(300_000),
		Raw:                           map[string]any{"speedFirstConfig": map[string]any{"maxFirstByteRetriesPerRequest": 3}},
	}
	loop.c.engine.Concurrency = store
	action, err := loop.speedFirstTotalTimeDeadlineDecision(context.Background(), loop.current,
		gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"},
		300_000, 300_500, loop.speedFirstDecisionsOf(),
		loop.speedFirstLatencyScopeOf(loop.current), loop.current.NormalRouteSpeedFirstConfig)
	if err != nil {
		t.Fatalf("决策错误 = %v", err)
	}
	if action != gatewaydispatch.FirstByteDeadlineActionAbort {
		t.Fatalf("压缩请求确认慢必须可切号，action = %v", action)
	}
	if loop.speedFirstTotalTimeCutoverSignal == nil || loop.speedFirstTotalTimeCutoverSignal.thresholdMs != 300_000 {
		t.Fatalf("载荷槽必须携带压缩档阈值 = %+v", loop.speedFirstTotalTimeCutoverSignal)
	}
	if len(store.acquired) == 0 {
		t.Fatal("切号目标必须尝试获取并发槽")
	}
}

// TestSpeedFirstTotalTimeLaneOf 锁定链面总时间档位判定（调度内核通用化设计
// 5.2：超时豁免布尔 + 估算输入 token 与 SpeedFirstLargeInputTokenThreshold
// 的比较在链面完成后经 TotalTimeLane 传入引擎；原 dispatch 侧
// ResolveNormalRouteTotalTimeDeadline 的大输入选档分支随批次 2 移到此处）。
func TestSpeedFirstTotalTimeLaneOf(t *testing.T) {
	// 与引擎侧估算同输入形态：直接构造带 RawBody 的 GatewayRequest（chain
	// 物化前的视图，估算按 rawBody 字符加权）。
	plainReq := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
		RawBody: []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
	}}
	largeBody := `{"model":"gpt-test","messages":[{"role":"user","content":"` +
		strings.Repeat("长输入内容用于字符加权估算", 14000) + `"}]}`
	largeReq := &gatewaypreauth.GatewayRequest{Body: &gatewaybody.Request{
		RawBody: []byte(largeBody),
	}}
	if gatewaydispatch.TotalTimeLane(0) != gatewaydispatch.TotalTimeLaneNormal {
		t.Fatal("TotalTimeLane 零值必须是 normal 档")
	}

	t.Run("nil预算与nil请求判normal", func(t *testing.T) {
		if lane := speedFirstTotalTimeLaneOf(nil, nil); lane != gatewaydispatch.TotalTimeLaneNormal {
			t.Fatalf("nil wall + nil req 判 normal, got %v", lane)
		}
	})

	t.Run("超时豁免恒extended", func(t *testing.T) {
		wall := &gatewayrouting.GatewayRequestWallBudget{Unbounded: true}
		if lane := speedFirstTotalTimeLaneOf(wall, nil); lane != gatewaydispatch.TotalTimeLaneExtended {
			t.Fatalf("Unbounded wall（压缩豁免承载）判 extended, got %v", lane)
		}
	})

	t.Run("大输入估算判extended", func(t *testing.T) {
		tokens, _ := gatewayopenai.EstimateRequestInputTokens(largeReq.Body.Body, largeReq.Body.RawBody)
		if tokens < routestrategies.SpeedFirstLargeInputTokenThreshold {
			t.Fatalf("夹具必须达到大输入阈值: %d", tokens)
		}
		if lane := speedFirstTotalTimeLaneOf(nil, largeReq); lane != gatewaydispatch.TotalTimeLaneExtended {
			t.Fatalf("估算输入 ≥ SpeedFirstLargeInputTokenThreshold 判 extended, got %v", lane)
		}
	})

	t.Run("普通小请求判normal", func(t *testing.T) {
		if lane := speedFirstTotalTimeLaneOf(nil, plainReq); lane != gatewaydispatch.TotalTimeLaneNormal {
			t.Fatalf("小输入普通请求判 normal, got %v", lane)
		}
	})
}

// 引擎级同账户重试不变量（设计 6.9）：轮内二次完成观测（引擎内重试不触发
// loop 轮级清零）不得重复计数——去重标记只在换轮/组切换时重置。
func TestObserveSpeedFirstTotalTimeEngineRetryKeepsDedup(t *testing.T) {
	fake := &speedFirstTotalTimeFake{}
	loop := totalDeadlineLoop(t, fake)
	nowMs := time.Now().UnixMilli()
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
		AttemptStartedAt: nowMs - 121_000,
	}
	firstToken := int64(500)
	handling := gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken}
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current, dispatched, handling)
	// 引擎内同账户重试：不经过 loop 轮级清零，直接二次完成观测。
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current, dispatched, handling)
	if fake.totalSlowCalls != 1 {
		t.Fatalf("轮内重试不得重复计数 totalSlowCalls = %d", fake.totalSlowCalls)
	}
}

// 压缩档达标样本的 effectiveDeadlineMs 必须是压缩档阈值（K21：同一样本
// 一把尺的压缩侧锚定）。
func TestObserveSpeedFirstTotalTimeCompactionSuccessUsesCompactionDeadline(t *testing.T) {
	fake := &speedFirstTotalTimeFake{}
	loop := totalDeadlineLoop(t, fake)
	loop.budgets.wall = &gatewayrouting.GatewayRequestWallBudget{Unbounded: true}
	loop.current.NormalRouteSpeedFirstConfig = &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		SchedulingPreference:          "speed_first",
		CompactionTotalTimeDeadlineMs: int64PtrOf(300_000),
		Raw:                           map[string]any{"speedFirstConfig": map[string]any{}},
	}
	nowMs := time.Now().UnixMilli()
	dispatched := gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
		AttemptStartedAt: nowMs - 1_000,
	}
	firstToken := int64(500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current, dispatched,
		gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if fake.totalSuccessCalls != 1 {
		t.Fatalf("totalSuccessCalls = %d", fake.totalSuccessCalls)
	}
	if len(fake.totalSuccessArgs) != 1 || fake.totalSuccessArgs[0][0] != 300_000 {
		t.Fatalf("压缩档达标阈值 = %v，want 300000", fake.totalSuccessArgs)
	}
	if fake.firstSuccessCalls != 0 {
		t.Fatalf("压缩请求不得进入首字恢复通道 firstSuccessCalls = %d", fake.firstSuccessCalls)
	}
}

// 决策入口闭包（onNormalRouteTotalTimeDeadline）的包装行为：早退与错误
// 兜底（释放残留预留 + 清载荷槽 + 审计 + continue 当前上游）。
func TestOnNormalRouteTotalTimeDeadlineClosure(t *testing.T) {
	input := gatewaydispatch.TotalTimeDeadlineDecisionInput{ElapsedMs: 60_500, TimeoutMs: 60_000}
	account := gatewaydispatch.AccountCandidate{ID: "acc_1", Name: "慢账户"}

	// 配置缺席（非速度优先策略）：闭包 continue，不触碰决策面。
	t.Run("配置缺席继续", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop := totalDeadlineLoop(t, fake)
		loop.current.NormalRouteSpeedFirstConfig = nil
		action := loop.onNormalRouteTotalTimeDeadline(context.Background(), loop.current)(input, account, 60_000)
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
		if fake.totalSlowCalls != 0 {
			t.Fatalf("配置缺席不得触碰决策面 totalSlowCalls = %d", fake.totalSlowCalls)
		}
	})

	// 决策面缺席（组合根未装配延迟服务）：continue。
	t.Run("决策面缺席继续", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{}
		loop := totalDeadlineLoop(t, fake)
		loop.c.engine.Latency = nil
		action := loop.onNormalRouteTotalTimeDeadline(context.Background(), loop.current)(input, account, 60_000)
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("action = %v", action)
		}
	})

	// 决策错误：释放残留预留与载荷槽、写审计、continue 当前上游。
	t.Run("决策错误兜底清理", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{totalSlowErr: errors.New("总时间慢采样写入失败")}
		store := &w1FakeConcurrencyStore{}
		loop := totalDeadlineLoop(t, fake)
		loop.c.engine.Concurrency = store
		rec := &recordingMetadataCapture{}
		loop.auditCapture = rec
		// 模拟上一路径残留：兜底段必须释放预留并清载荷槽。
		loop.speedFirstCutoverReservation = w1Reservation(t)
		loop.speedFirstTotalTimeCutoverSignal = &speedFirstTotalTimeCutoverSignal{accountID: "acc_stale"}
		action := loop.onNormalRouteTotalTimeDeadline(context.Background(), loop.current)(input, account, 60_000)
		if action != gatewaydispatch.FirstByteDeadlineActionContinue {
			t.Fatalf("决策错误必须 continue，action = %v", action)
		}
		if loop.speedFirstCutoverReservation != nil || loop.speedFirstTotalTimeCutoverSignal != nil {
			t.Fatal("兜底必须释放残留预留并清载荷槽")
		}
		if rec.byLabel("normal_route_speed_first_local_decision_failed") == nil {
			t.Fatal("决策错误必须写审计 normal_route_speed_first_local_decision_failed")
		}
	})

	// 正常委托：确认慢透传 abort（闭包不改变决策结果）。
	t.Run("正常委托透传abort", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{degraded: map[string]bool{"acc_1": true}}
		loop := totalDeadlineLoop(t, fake)
		loop.c.engine.Concurrency = &w1FakeConcurrencyStore{}
		action := loop.onNormalRouteTotalTimeDeadline(context.Background(), loop.current)(input, account, 60_000)
		if action != gatewaydispatch.FirstByteDeadlineActionAbort {
			t.Fatalf("确认慢必须透传 abort，action = %v", action)
		}
		if fake.totalSlowCalls != 1 {
			t.Fatalf("totalSlowCalls = %d", fake.totalSlowCalls)
		}
	})
}

// 软观察样本记录的静默分支：配置缺席时不写审计不触碰决策面。
func TestObserveTotalTimeDeadlineSampleConfigNil(t *testing.T) {
	fake := &speedFirstTotalTimeFake{}
	loop := totalDeadlineLoop(t, fake)
	rec := &recordingMetadataCapture{}
	loop.auditCapture = rec
	loop.current.NormalRouteSpeedFirstConfig = nil
	loop.observeTotalTimeDeadlineSample(context.Background(), loop.current, gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
		AttemptStartedAt: time.Now().UnixMilli() - 60_000,
	}, 60_000)
	if fake.totalSlowCalls != 0 || len(rec.labels) != 0 {
		t.Fatalf("配置缺席必须静默: slowCalls=%d labels=%v", fake.totalSlowCalls, rec.labels)
	}
}

// 副作用 lane（图片等）不参与总时间维度：软观察不 arm、完成观测静默
// （speedFirstTotalTimeThresholdMsOf 的 lane 门，设计 6.2）。
func TestTotalTimeDeadlineSilentOnNonTextLane(t *testing.T) {
	fake := &speedFirstTotalTimeFake{}
	loop := totalDeadlineLoop(t, fake)
	loop.current.RequestLane = "image"
	finished := &atomic.Int32{}
	stop := loop.armTotalTimeDeadlineObserver(context.Background(), loop.current, gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
		AttemptStartedAt: time.Now().UnixMilli() - 120_000,
	}, finished)
	defer stop()
	if fake.totalSlowCalls != 0 {
		t.Fatalf("非 text lane 不得记总时间样本 totalSlowCalls = %d", fake.totalSlowCalls)
	}
	firstToken := int64(500)
	loop.observeSpeedFirstResponseOutcome(context.Background(), loop.current, gatewaydispatch.UpstreamDispatchResult{
		Account:          gatewaydispatch.AccountCandidate{ID: "acc_1"},
		AttemptStartedAt: time.Now().UnixMilli() - 120_000,
	}, gatewayresponse.UpstreamResponseHandlingResult{FirstTokenMs: &firstToken})
	if fake.totalSlowCalls != 0 || fake.totalSuccessCalls != 0 {
		t.Fatalf("非 text lane 完成观测必须静默 slow=%d success=%d", fake.totalSlowCalls, fake.totalSuccessCalls)
	}
}
