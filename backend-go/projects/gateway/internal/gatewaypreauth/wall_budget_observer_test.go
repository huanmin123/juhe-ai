package gatewaypreauth

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// recordingRoutingObserver captures ObserveRouting events for slot-wiring
// assertions.
type recordingRoutingObserver struct {
	kinds    []string
	outcomes []string
	nows     []int64
}

func (o *recordingRoutingObserver) ObserveRouting(kind, outcome string, nowMs int64) {
	o.kinds = append(o.kinds, kind)
	o.outcomes = append(o.outcomes, outcome)
	o.nows = append(o.nows, nowMs)
}

// TestRoutingWallBudgetObserverSlot pins the process-level slot semantics:
// unset keeps the nil-observer contract, assembly stores the observer, and
// clearing restores nil.
func TestRoutingWallBudgetObserverSlot(t *testing.T) {
	previous := RoutingWallBudgetObserverOf()
	t.Cleanup(func() { SetRoutingWallBudgetObserver(previous) })

	// 未装配（或单测先行置位后显式清空）：nil-observer 语义。
	SetRoutingWallBudgetObserver(nil)
	if RoutingWallBudgetObserverOf() != nil {
		t.Fatalf("清空后槽必须返回 nil（保持既有 nil-observer 语义）")
	}

	// 置位后取到同一观察者实例。
	observer := &recordingRoutingObserver{}
	SetRoutingWallBudgetObserver(observer)
	if RoutingWallBudgetObserverOf() != gatewayrouting.RoutingObserver(observer) {
		t.Fatalf("置位后必须返回同一观察者")
	}
}

// TestPreflightWallBudgetCarriesObserver 钉住 preflight.go 预算构造点的接线
// 形态：NewGatewayRequestWallBudget 以槽值作为 observer（与
// PrepareOpenAIGatewayDispatchContext 内的构造表达式同形），装配后裁剪事件
// 必须落观察者，未装配时保持无观察。
func TestPreflightWallBudgetCarriesObserver(t *testing.T) {
	previous := RoutingWallBudgetObserverOf()
	t.Cleanup(func() { SetRoutingWallBudgetObserver(previous) })

	// 装配后：构造点传槽值，precommit 裁剪必须观测到。
	observer := &recordingRoutingObserver{}
	SetRoutingWallBudgetObserver(observer)
	budgetMs := int64(100_000)
	budget, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: 0,
		BudgetMs:            &budgetMs,
		Now:                 func() int64 { return 0 },
	}, RoutingWallBudgetObserverOf())
	if err != nil {
		t.Fatalf("构造 wall budget：%v", err)
	}
	clipped, err := budget.ClipFirstByteDeadlineMs(gatewayrouting.FirstByteDeadlineClipInput{
		NowMs:                          int64Ptr(0),
		FirstByteDeadlineMs:            50_000,
		UncommittedAttemptDeadlineAtMs: int64Ptr(30_000),
	})
	if err != nil {
		t.Fatalf("裁剪首字节时限：%v", err)
	}
	if clipped != 30_000 {
		t.Fatalf("clipped = %d, want 30000", clipped)
	}
	if len(observer.kinds) != 1 || observer.kinds[0] != "budget" ||
		observer.outcomes[0] != "precommit_clipped" || observer.nows[0] != 0 {
		t.Fatalf("budget 裁剪观测不符：%+v", observer)
	}

	// 未装配：同一构造形态保持 nil-observer（不 panic、不观测）。
	SetRoutingWallBudgetObserver(nil)
	silent, err := gatewayrouting.NewGatewayRequestWallBudget(gatewayrouting.GatewayRequestWallBudgetOptions{
		RequestAcceptedAtMs: 0,
		BudgetMs:            &budgetMs,
		Now:                 func() int64 { return 0 },
	}, RoutingWallBudgetObserverOf())
	if err != nil {
		t.Fatalf("构造无观察 wall budget：%v", err)
	}
	if _, err := silent.ClipFirstByteDeadlineMs(gatewayrouting.FirstByteDeadlineClipInput{
		NowMs:                          int64Ptr(0),
		FirstByteDeadlineMs:            50_000,
		UncommittedAttemptDeadlineAtMs: int64Ptr(30_000),
	}); err != nil {
		t.Fatalf("无观察裁剪不应报错：%v", err)
	}
}
