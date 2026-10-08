package gatewayclientip

// 账户并发硬上限（总量 + lane 双重校验）的 TryAcquire 契约测试：恢复 Node
// tryAcquireAccountConcurrency 的“current >= limit || laneCurrent >=
// laneLimit 即拒绝”语义——混合 lane 流量下 total 不得突破硬上限，检查与占
// 用在单个临界区内原子完成。

import (
	"context"
	"sync"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
)

func TestMemoryAccountConcurrencyTryAcquireTotalCapAcrossLanes(t *testing.T) {
	tracker := NewMemoryAccountConcurrency(nil)
	const totalLimit = 3
	imageLaneLimit := 2

	// text lane 独占获取至总量硬上限。
	for index := 0; index < totalLimit; index++ {
		outcome := tracker.TryAcquire("acc-1", totalLimit, AccountConcurrencyLaneText, totalLimit)
		if !outcome.Acquired || outcome.Total != index+1 || outcome.LaneCurrent != index+1 {
			t.Fatalf("text 第 %d 次获取 = %+v, want 成功且计数同步", index+1, outcome)
		}
	}
	if got := tracker.CurrentAccountConcurrency("acc-1", ""); got != totalLimit {
		t.Fatalf("text 占满后 total=%d want %d", got, totalLimit)
	}

	// 契约：total 达硬上限后，image lane 即使自身 laneLimit 未满也必须被拒
	// （总量半边生效），且拒绝路径不自增任何计数。
	outcome := tracker.TryAcquire("acc-1", totalLimit, AccountConcurrencyLaneImage, imageLaneLimit)
	if outcome.Acquired || outcome.Total != totalLimit || outcome.LaneCurrent != 0 {
		t.Fatalf("混合流量 image 获取 = %+v, want 拒绝且计数不变", outcome)
	}
	if got := tracker.CurrentAccountConcurrency("acc-1", ""); got != totalLimit {
		t.Fatalf("拒绝后 total=%d want %d", got, totalLimit)
	}
	if got := tracker.CurrentAccountConcurrency("acc-1", AccountConcurrencyLaneImage); got != 0 {
		t.Fatalf("拒绝后 image lane=%d want 0", got)
	}

	// 契约：image lane 独占时仍可获取到自身 laneLimit（此时 total 未达硬上
	// 限，由 lane 半边判断）。
	imageTracker := NewMemoryAccountConcurrency(nil)
	for index := 0; index < imageLaneLimit; index++ {
		laneOutcome := imageTracker.TryAcquire("acc-2", totalLimit, AccountConcurrencyLaneImage, imageLaneLimit)
		if !laneOutcome.Acquired || laneOutcome.Total != index+1 || laneOutcome.LaneCurrent != index+1 {
			t.Fatalf("image 第 %d 次获取 = %+v, want 成功", index+1, laneOutcome)
		}
	}
	// image lane 满后拒绝：total 未达硬上限也拒绝（lane 半边生效）。
	laneFull := imageTracker.TryAcquire("acc-2", totalLimit, AccountConcurrencyLaneImage, imageLaneLimit)
	if laneFull.Acquired || laneFull.Total != imageLaneLimit || laneFull.LaneCurrent != imageLaneLimit {
		t.Fatalf("image lane 满后获取 = %+v, want 拒绝且计数不变", laneFull)
	}

	// 上限 <=0 视为不设限：不拒绝、照常自增。
	unlimited := tracker.TryAcquire("acc-3", 0, AccountConcurrencyLaneText, 0)
	if !unlimited.Acquired || unlimited.Total != 1 {
		t.Fatalf("不设限获取 = %+v, want 成功", unlimited)
	}
}

func TestMemoryAccountConcurrencyTryAcquireConcurrentContention(t *testing.T) {
	tracker := NewMemoryAccountConcurrency(nil)
	const totalLimit = 8
	const workers = 32

	var wg sync.WaitGroup
	successes := make(chan struct{}, workers)
	start := make(chan struct{})
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if tracker.TryAcquire("acc-1", totalLimit, AccountConcurrencyLaneText, totalLimit).Acquired {
				successes <- struct{}{}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(successes)

	// 契约：全部争抢完成后成功数恰好等于硬上限（不得超卖），total 与 lane
	// 计数 == 成功数。
	if got := len(successes); got != totalLimit {
		t.Fatalf("并发成功数 = %d, want 恰好 %d", got, totalLimit)
	}
	if got := tracker.CurrentAccountConcurrency("acc-1", ""); got != totalLimit {
		t.Fatalf("total = %d, want 成功数 %d", got, totalLimit)
	}
	if got := tracker.CurrentAccountConcurrency("acc-1", AccountConcurrencyLaneText); got != totalLimit {
		t.Fatalf("text lane 计数 = %d, want %d", got, totalLimit)
	}
}

func TestRedisAccountConcurrencyAcquireEnforcesLimits(t *testing.T) {
	server := miniredis.RunT(t)
	instance, _ := newTestRedisAccountConcurrency(t, server, "dev")
	ctx := context.Background()

	// 契约：totalLimit=2 时前两次成功，第三次拒绝且两个计数都不自增。
	for index := 0; index < 2; index++ {
		outcome, err := instance.AcquireAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneText, 60_000, 2, 2)
		if err != nil || !outcome.Acquired || outcome.Total != index+1 {
			t.Fatalf("第 %d 次 acquire = %+v err %v, want 成功", index+1, outcome, err)
		}
	}
	outcome, err := instance.AcquireAccountConcurrency(ctx, "acc-1", AccountConcurrencyLaneImage, 60_000, 2, 2)
	if err != nil || outcome.Acquired || outcome.Total != 2 || outcome.LaneCurrent != 0 {
		t.Fatalf("total 达限后 image acquire = %+v err %v, want 拒绝且不自增", outcome, err)
	}
	if got := instance.CurrentAccountConcurrency("acc-1", ""); got != 2 {
		t.Fatalf("拒绝后 total=%d want 2", got)
	}

	// 契约：lane 半边——image laneLimit=1 时首次成功、第二次拒绝（total 未
	// 达上限）。
	laneOutcome, err := instance.AcquireAccountConcurrency(ctx, "acc-2", AccountConcurrencyLaneImage, 60_000, 3, 1)
	if err != nil || !laneOutcome.Acquired || laneOutcome.LaneCurrent != 1 {
		t.Fatalf("image 首次 acquire = %+v err %v, want 成功", laneOutcome, err)
	}
	laneFull, err := instance.AcquireAccountConcurrency(ctx, "acc-2", AccountConcurrencyLaneImage, 60_000, 3, 1)
	if err != nil || laneFull.Acquired || laneFull.Total != 1 || laneFull.LaneCurrent != 1 {
		t.Fatalf("image lane 满后 acquire = %+v err %v, want 拒绝", laneFull, err)
	}
	// redis 驱动 total 字段经 ByID 读取（CurrentAccountConcurrency 空 lane 回落 text 字段）。
	byID, err := instance.LoadAccountCurrentConcurrencyByID(ctx, []string{"acc-2"})
	if err != nil || byID["acc-2"] != 1 {
		t.Fatalf("lane 拒绝后 total=%+v err %v want 1", byID, err)
	}
}
