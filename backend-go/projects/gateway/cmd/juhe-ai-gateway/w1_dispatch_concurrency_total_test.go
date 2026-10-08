package main

// 账户并发硬上限（concurrency_limit = “硬并发上限，达到后不再派新请求”）在
// chain 并发存储适配层的契约测试：TryAcquireAsync 必须原子执行 total + lane
// 双重校验——混合 lane 流量下账户总量不得突破硬上限，并发争抢不得超卖
// （相对 Node shared/account-concurrency 的移植回归修复）。

import (
	"context"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

func TestW1FConcurrencyStoreTotalCapAcrossMixedLanes(t *testing.T) {
	tracker := gatewayclientip.NewMemoryAccountConcurrency(nil)
	store := newChainConcurrencyStore(tracker)
	ctx := context.Background()
	const totalLimit = 3
	imageLaneLimit := 2

	// text lane 独占获取至总量硬上限。
	for index := 0; index < totalLimit; index++ {
		slot, err := store.TryAcquireAsync(ctx, "acc-1", totalLimit, gatewaydispatch.AccountConcurrencyAcquireOptions{})
		if err != nil || !slot.Acquired || slot.Current != index+1 {
			t.Fatalf("text 第 %d 次获取 = %+v err %v, want 成功", index+1, slot, err)
		}
	}
	// 契约：total 达硬上限后 image lane（laneLimit 未满）必须被拒，且总量计
	// 数不再增长。
	blocked, err := store.TryAcquireAsync(ctx, "acc-1", totalLimit, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "image", LaneLimit: &imageLaneLimit})
	if err != nil || blocked.Acquired {
		t.Fatalf("混合流量 image 获取 = %+v err %v, want 拒绝", blocked, err)
	}
	if blocked.Current != totalLimit || blocked.LaneCurrent != 0 {
		t.Fatalf("拒绝槽位观测 = %+v, want total=%d laneCurrent=0", blocked, totalLimit)
	}
	currents, err := store.LoadCurrentAsync(ctx, []string{"acc-1"})
	if err != nil || currents["acc-1"] != totalLimit {
		t.Fatalf("混合流量下 total = %+v err %v, want %d（不超硬上限）", currents, err, totalLimit)
	}

	// 契约：image lane 独占时可获取到自身 laneLimit；lane 满后拒绝且 slot 字
	// 段支持“lane 满但总量未满”的区分（LaneCurrent>=LaneLimit &&
	// Current<Limit，accountConcurrencyLimitMessage 的判据）。
	for index := 0; index < imageLaneLimit; index++ {
		laneSlot, err := store.TryAcquireAsync(ctx, "acc-2", totalLimit, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "image", LaneLimit: &imageLaneLimit})
		if err != nil || !laneSlot.Acquired || laneSlot.LaneCurrent != index+1 {
			t.Fatalf("image 第 %d 次获取 = %+v err %v, want 成功", index+1, laneSlot, err)
		}
	}
	laneBlocked, err := store.TryAcquireAsync(ctx, "acc-2", totalLimit, gatewaydispatch.AccountConcurrencyAcquireOptions{Lane: "image", LaneLimit: &imageLaneLimit})
	if err != nil || laneBlocked.Acquired {
		t.Fatalf("image lane 满后获取 = %+v err %v, want 拒绝", laneBlocked, err)
	}
	if laneBlocked.LaneCurrent < laneBlocked.LaneLimit || laneBlocked.Current >= laneBlocked.Limit {
		t.Fatalf("lane 满槽位观测 = %+v, want lane 达限且总量未满", laneBlocked)
	}
}

func TestW1FConcurrencyStoreConcurrentContention(t *testing.T) {
	store := newChainConcurrencyStore(gatewayclientip.NewMemoryAccountConcurrency(nil))
	ctx := context.Background()
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
			slot, err := store.TryAcquireAsync(ctx, "acc-1", totalLimit, gatewaydispatch.AccountConcurrencyAcquireOptions{})
			if err == nil && slot.Acquired {
				successes <- struct{}{}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(successes)

	// 契约：全部争抢完成后成功数恰好等于硬上限，total 计数 == 成功数。
	if got := len(successes); got != totalLimit {
		t.Fatalf("并发成功数 = %d, want 恰好 %d", got, totalLimit)
	}
	currents, err := store.LoadCurrentAsync(ctx, []string{"acc-1"})
	if err != nil || currents["acc-1"] != totalLimit {
		t.Fatalf("total = %+v err %v, want %d", currents, err, totalLimit)
	}
}
