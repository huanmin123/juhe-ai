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
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproxyhealth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// speedFirstTotalTimeFake 分维度记录决策面调用（首字与总时间通道独立计数，
// 供"并存各自记录"与"压缩守卫首字零调用"断言）。
type speedFirstTotalTimeFake struct {
	degraded map[string]bool

	firstSlowCalls    int
	firstSuccessCalls int
	firstSlowReasons  []string

	totalSlowCalls      int
	totalSuccessCalls   int
	totalSlowReasons    []string
	totalSuccessArgs    [][2]int64
	totalSlowLastConfig *gatewayproxyhealth.SpeedFirstRuntimeConfig

	degradedErr  error
	totalSlowErr error
}

func (f *speedFirstTotalTimeFake) OrderAsync(context.Context, []gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, *gatewaydispatch.ModelPriority) (gatewaydispatch.LatencyDegradationOrder, error) {
	return gatewaydispatch.LatencyDegradationOrder{}, nil
}

func (f *speedFirstTotalTimeFake) IsAccountLatencyDegradedAsync(_ context.Context, account gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput) (bool, error) {
	if f.degradedErr != nil {
		return false, f.degradedErr
	}
	return f.degraded[account.ID], nil
}

func (f *speedFirstTotalTimeFake) RecordFirstByteSlowAsync(_ context.Context, _ gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, _ *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, reason string) (*gatewayproxyhealth.LatencySlowResult, error) {
	f.firstSlowCalls++
	f.firstSlowReasons = append(f.firstSlowReasons, reason)
	return &gatewayproxyhealth.LatencySlowResult{SlowCount: 1}, nil
}

func (f *speedFirstTotalTimeFake) RecordFirstByteSuccessAsync(context.Context, gatewaydispatch.AccountCandidate, *gatewaydispatch.LatencyScopeInput, *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
	f.firstSuccessCalls++
	return &gatewayproxyhealth.LatencySuccessResult{Cleared: true}, nil
}

func (f *speedFirstTotalTimeFake) RecordTotalTimeSlowAsync(_ context.Context, _ gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, config *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, reason string) (*gatewayproxyhealth.LatencySlowResult, error) {
	f.totalSlowCalls++
	f.totalSlowReasons = append(f.totalSlowReasons, reason)
	f.totalSlowLastConfig = chainSpeedFirstRuntimeConfigOf(config)
	if f.totalSlowErr != nil {
		return nil, f.totalSlowErr
	}
	return &gatewayproxyhealth.LatencySlowResult{SlowCount: 1, Degraded: f.degradedForSlow()}, nil
}

func (f *speedFirstTotalTimeFake) degradedForSlow() bool {
	return f.totalSlowCalls >= 2
}

func (f *speedFirstTotalTimeFake) RecordTotalTimeSuccessAsync(_ context.Context, _ gatewaydispatch.AccountCandidate, _ *gatewaydispatch.LatencyScopeInput, _ *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig, effectiveDeadlineMs int64, elapsedMs int64) (*gatewayproxyhealth.LatencySuccessResult, error) {
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

	// 已向下游写出：只记样本继续等待，不进入切号裁决。
	t.Run("已写出只记样本", func(t *testing.T) {
		fake := &speedFirstTotalTimeFake{degraded: map[string]bool{"acc_1": true}}
		store := &w1FakeConcurrencyStore{}
		action, loop := run(t, fake, func(loop *v1DispatchLoop) {
			loop.c.engine.Concurrency = store
			loop.waitCommitState = &gatewayresponse.DownstreamCommitState{SemanticCommitted: true}
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
		for fake.totalSlowCalls == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if fake.totalSlowCalls != 1 {
			t.Fatalf("totalSlowCalls = %d", fake.totalSlowCalls)
		}
		if loop.speedFirstCutoverReservation != nil {
			t.Fatal("软观察 timer 不得切号")
		}
		// 去重：同一 attempt 二次到点不重复记录。
		loop.observeTotalTimeDeadlineSample(context.Background(), loop.current, dispatched, 50)
		if fake.totalSlowCalls != 1 {
			t.Fatalf("去重失败 totalSlowCalls = %d", fake.totalSlowCalls)
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
		if fake.totalSlowCalls != 0 {
			t.Fatalf("完成后不得触发 totalSlowCalls = %d", fake.totalSlowCalls)
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
