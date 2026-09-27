package main

// 列表运行时并发装配回归：chainAccountConcurrencyReader 适配器必须转发同一
// 进程内 tracker 实例的总量读数（分组列表 accountStats.CurrentConcurrency 与
// 账户列表 currentConcurrency 的 hydrate 事实源，与 dispatch 引擎同源，
// E2E-FINDING #13 同源原则的管理面延伸）。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayclientip"
)

func TestChainAccountConcurrencyReaderForwardsTracker(t *testing.T) {
	tracker := gatewayclientip.NewMemoryAccountConcurrency(nil)
	tracker.Acquire("acc-live", gatewayclientip.AccountConcurrencyLaneText)
	tracker.Acquire("acc-live", gatewayclientip.AccountConcurrencyLaneText)
	tracker.Acquire("acc-live", gatewayclientip.AccountConcurrencyLaneImage)
	tracker.Acquire("acc-other", gatewayclientip.AccountConcurrencyLaneText)

	reader := chainAccountConcurrencyReader{tracker: tracker}
	currents, err := reader.LoadCurrentConcurrencyByID(context.Background(), []string{"acc-live", "acc-missing"})
	if err != nil {
		t.Fatalf("tracker read must not fail: %v", err)
	}
	if currents["acc-live"] != 3 {
		t.Fatalf("total-lane read must sum text+image lanes, got %v", currents)
	}
	if currents["acc-missing"] != 0 {
		t.Fatalf("untracked accounts must read 0, got %v", currents)
	}

	// 接口满足性：适配器必须同时满足 groups / accounts 两个管理面端口。
	var _ interface {
		LoadCurrentConcurrencyByID(ctx context.Context, accountIDs []string) (map[string]int, error)
	} = reader
}
