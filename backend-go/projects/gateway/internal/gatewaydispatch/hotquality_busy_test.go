package gatewaydispatch

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func gatewayDispatchTestInt(value int) *int { return &value }

// TestSplitHardBusyForHotQuality 钉住硬忙候选与热质量排序的隔离：硬忙账户
// 不进入热质量比较，排序完成后整体追加到尾部；全硬忙/单候选保持原列表。
func TestSplitHardBusyForHotQuality(t *testing.T) {
	accounts := []AccountCandidate{
		{ID: "idle", ConcurrencyLimit: 4, CurrentConcurrency: gatewayDispatchTestInt(0)},
		{ID: "hard", ConcurrencyLimit: 4, CurrentConcurrency: gatewayDispatchTestInt(4)},
		{ID: "nil-current", ConcurrencyLimit: 4},
		{ID: "over", ConcurrencyLimit: 4, CurrentConcurrency: gatewayDispatchTestInt(9)},
	}
	dispatchable, hardBusy := splitHardBusyForHotQuality(accounts)
	if len(dispatchable) != 2 || dispatchable[0].ID != "idle" || dispatchable[1].ID != "nil-current" {
		t.Fatalf("dispatchable = %+v", dispatchable)
	}
	if len(hardBusy) != 2 || hardBusy[0].ID != "hard" || hardBusy[1].ID != "over" {
		t.Fatalf("hardBusy = %+v", hardBusy)
	}

	allBusy := []AccountCandidate{
		{ID: "a", ConcurrencyLimit: 1, CurrentConcurrency: gatewayDispatchTestInt(1)},
		{ID: "b", ConcurrencyLimit: 2, CurrentConcurrency: gatewayDispatchTestInt(2)},
	}
	dispatchable, hardBusy = splitHardBusyForHotQuality(allBusy)
	if len(dispatchable) != 2 || dispatchable[0].ID != "a" || dispatchable[1].ID != "b" {
		t.Fatalf("all-busy dispatchable = %+v", dispatchable)
	}
	if hardBusy != nil {
		t.Fatalf("all-busy hardBusy = %+v, want nil", hardBusy)
	}

	single := []AccountCandidate{{ID: "only", ConcurrencyLimit: 1, CurrentConcurrency: gatewayDispatchTestInt(1)}}
	dispatchable, hardBusy = splitHardBusyForHotQuality(single)
	if len(dispatchable) != 1 || dispatchable[0].ID != "only" || hardBusy != nil {
		t.Fatalf("single = %+v/%+v", dispatchable, hardBusy)
	}
}

// TestSoftBusyAccountIDsFor 钉住软阈值集合计算：达到 min(策略阈值, 硬上限)
// 的账户标记 softBusy；策略 nil/关闭时集合为空（零开销）。
func TestSoftBusyAccountIDsFor(t *testing.T) {
	accounts := []AccountCandidate{
		{ID: "soft", ConcurrencyLimit: 4, CurrentConcurrency: gatewayDispatchTestInt(2)},
		{ID: "idle", ConcurrencyLimit: 4, CurrentConcurrency: gatewayDispatchTestInt(1)},
		{ID: "nil-current", ConcurrencyLimit: 4},
		{ID: "at-hard-limit", ConcurrencyLimit: 2, CurrentConcurrency: gatewayDispatchTestInt(2)},
	}
	policy := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: 2, ClientIPConcurrencyLimit: 0}
	got := softBusyAccountIDsFor(accounts, policy)
	if len(got) != 2 || !got["soft"] || !got["at-hard-limit"] || got["idle"] || got["nil-current"] {
		t.Fatalf("softBusy set = %+v", got)
	}
	if len(softBusyAccountIDsFor(accounts, nil)) != 0 {
		t.Fatal("nil policy must yield empty softBusy set")
	}
	disabled := &gatewayruntimecache.GroupSchedulingPolicy{DefaultSoftConcurrency: -1, ClientIPConcurrencyLimit: 0}
	if len(softBusyAccountIDsFor(accounts, disabled)) != 0 {
		t.Fatal("disabled policy must yield empty softBusy set")
	}
}
