package gatewaypreauth

import (
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// 进程级 wall budget Observer 槽（G19 观测接线收尾）：preflight 的
// GatewayRequestWallBudget 构造点（preflight.go 逐请求预算构造）没有构造期
// 句柄，观察者由组合根（cmd/juhe-ai-gateway 的 wireGatewayObservabilityArms，
// chain runtime 装配阶段）经 SetRoutingWallBudgetObserver 置位，构成本包与
// cmd 之间唯一的接线通道（本包不能反向 import cmd）。
//
// 时序：槽在每次请求的预算构造点读取（PrepareOpenAIGatewayDispatchContext
// 逐请求执行），而置位发生在进程启动的 chain 装配阶段——所有服务路径的预算
// 构造都晚于置位，构造时读槽即取到当前值，无需 boot 期快照；装配前构造的
// 预算（单测、组合根之前的工具路径）保持既有 nil-observer 语义。
//
// panic-safe 契约由置位方保证（cmd 侧 chainRoutingWallBudgetObserver 整体
// recover，观测故障只告警），本槽只做传递，不包裹不吞错。

// routingWallBudgetObserverSlot is the process-level slot; nil pointer means
// "not assembled" and keeps the pre-existing nil-observer semantics.
var routingWallBudgetObserverSlot atomic.Pointer[gatewayrouting.RoutingObserver]

// SetRoutingWallBudgetObserver installs the process-level wall budget
// observer（组合根装配调用；传 nil 恢复 nil-observer 语义）。
func SetRoutingWallBudgetObserver(observer gatewayrouting.RoutingObserver) {
	if observer == nil {
		routingWallBudgetObserverSlot.Store(nil)
		return
	}
	routingWallBudgetObserverSlot.Store(&observer)
}

// RoutingWallBudgetObserverOf returns the current slot value（未置位返回
// nil，保持既有 nil-observer 语义）。预算构造点逐请求调用，取值即当前装配
// 状态。
func RoutingWallBudgetObserverOf() gatewayrouting.RoutingObserver {
	if observer := routingWallBudgetObserverSlot.Load(); observer != nil {
		return *observer
	}
	return nil
}
