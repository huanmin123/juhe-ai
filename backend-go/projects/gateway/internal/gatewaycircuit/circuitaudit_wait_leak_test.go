package gatewaycircuit

// circuitaudit_wait_leak_test.go — R3 抢跑结算无泄漏验收（原复现：timer
// goroutine 永久泄漏）。
//
// 已修复的缺陷（wait.go）：
//
//   - scheduleScopeLocked 为每个有等待者的 scope spawn 一个 timer goroutine，
//     它阻塞在 `select { case <-done: ...; case <-c.stopped(): return }`；
//   - stopped() 恒返回 nil channel，goroutine 唯一的退路是 timer 自然到期
//     触发 close(done)；
//   - 当等待者被 NotifyOne / NotifyOneForRuntimeKey 抢跑结算时，settleWaiter
//     对 wasHead 调用 scope.timerStop()（旧实现只调 timer.Stop()）并把
//     timerDone/timerStop 置 nil —— timer 被停掉，close(done) 永远不会执行，
//     已 spawn 的 timer goroutine 永久阻塞。
//
// 修复：默认 newTimer 的 stop 闭包在 Stop 后幂等 close(done)（sync.Once 与
// 自然 fire 的回调共享），被抢跑废弃的 goroutine 立即从 <-done 醒来退出；
// 同时 goroutine 醒来后只在 scope.timerDone 仍是自己这枚 timer 时才清句柄，
// 防止误清 settleWaiter 为后续等待者新调度的 timer。
//
// 生产触发条件（原缺陷）：任何走 RecoverableUnavailable 等待协调的请求
//（账号 SUSPECT/OPEN 期间候补等待），只要等待被通知结算（账号恢复、
// runtimeKey 通知、其他等待者抢跑）而不是 timer 自然到期，就泄漏一个
// goroutine。长期运行的网关进程中持续累积。
//
// 确定性说明：不依赖时序竞争。timer 定为 5000ms（远超测试时长），抢跑结算
// 时 timer.Stop() 必然成功（timer 尚未 fire）；修复后 stop 闭包立即
// close(done)，被废弃的 goroutine 必然醒来退出。每轮"注册 + 抢跑"净泄漏
// 必须为 0。仅 settle 等待使用短 sleep（任务允许的例外）。

import (
	"runtime"
	"testing"
	"time"
)

func TestCircuitAuditR3WaitCoordinatorNoGoroutineLeakOnPreemptiveSettle(t *testing.T) {
	const waiters = 5
	// 固定假时钟：delay = notBefore(1000+5000) - now(1000) = 5000ms，timer 在
	// 测试生命周期内绝不自然 fire。
	coordinator := NewWaitCoordinator(WaitCoordinatorOptions{
		Now: func() int64 { return 1_000 },
	})

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	for i := 0; i < waiters; i++ {
		scopeKey := "circuit-audit-wait-scope-" + itoaCircuitAudit(i)
		results := make(chan string, 1)
		go func() {
			results <- coordinator.WaitForTurn(WaitTurnInput{
				ScopeKey:     scopeKey,
				Reason:       "recoverable_unavailable",
				DelayMs:      5_000,
				DeadlineAtMs: 1_000 + 60_000,
				RuntimeKeys:  []string{"circuit-audit-runtime-key"},
			})
		}()

		// 轮询 Snapshot 直到本等待者注册完成（timer goroutine 已 spawn）。
		// 每一轮同一时刻只有一个在飞等待者（上一轮已结算退出），所以判断
		// WaiterCount >= 1 且 TimerCount >= 1 即可。
		registered := false
		for attempt := 0; attempt < 200; attempt++ {
			snapshot := coordinator.Snapshot()
			if snapshot.WaiterCount >= 1 && snapshot.TimerCount >= 1 {
				registered = true
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if !registered {
			t.Fatalf("第 %d 个等待者未能在时限内注册（Snapshot=%+v）", i+1, coordinator.Snapshot())
		}

		// 抢跑结算：等待者立即拿到结果，timer 被 Stop；修复后 stop 闭包幂等
		// close(done)，spawn 出去的 timer goroutine 必须随即退出。
		if !coordinator.NotifyOne(scopeKey, "recoverable_unavailable") {
			t.Fatalf("第 %d 个 NotifyOne 未结算任何等待者", i+1)
		}
		select {
		case turn := <-results:
			if turn != TurnReady {
				t.Fatalf("抢跑结算结果 = %s, want ready", turn)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("第 %d 个等待者未被结算", i+1)
		}
		// 留出时间让等待者与 timer goroutine 退出，保证后续计数稳定（任务
		// 允许的 settle sleep 例外）。
		time.Sleep(50 * time.Millisecond)
	}

	// scopes 已全部清空。
	if snapshot := coordinator.Snapshot(); snapshot.WaiterCount != 0 || snapshot.TimerCount != 0 || snapshot.ScopeCount != 0 {
		t.Fatalf("结算后 Snapshot 应全空，got %+v", snapshot)
	}

	// 验收断言：抢跑结算后 timer goroutine 全部退出，goroutine 净增为 0
	//（等待者 goroutine 均已返回，timer goroutine 经 close(done) 醒来退出）。
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if leaked := after - baseline; leaked != 0 {
		t.Fatalf("goroutine 净增 = %d, want 0（baseline=%d after=%d）：抢跑结算后 timer goroutine 必须全部退出",
			leaked, baseline, after)
	}
	t.Logf("circuit-audit R3: goroutine baseline=%d after=%d leaked=%d", baseline, after, after-baseline)
}

func itoaCircuitAudit(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
