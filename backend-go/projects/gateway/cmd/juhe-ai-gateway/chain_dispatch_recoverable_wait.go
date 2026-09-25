package main

// dispatch 引擎 RecoverableWait 接线（第二轮确认补修）。
// gatewaydispatch.Engine.RecoverableWait（RecoverableSuppressionWaiter）此前
// 生产无赋值——upstreamdispatch.go 的 `if e.RecoverableWait != nil` 恒 false，
// 全部候选被本地抑制时直接快速耗尽退出（503），不具备「等待候选恢复」能力。
// preauth 侧等价能力已装配（chainRuntimeServices.Recoverable =
// gatewaycircuit.NewPreAuthRecoverableWait）。
//
// chainDispatchRecoverableWait 把 G11 PreAuthRecoverableWait 等待引擎适配到
// dispatch 端口：dispatch 侧状态类型是 gatewaydispatch.SuppressionFilterResult
//（Refresh/IsReady/NextRetryAfterMs 三个闭包），等待引擎消费的是
// StateWaitRefresh 的扁平三元组（ready / hasRetryAfter / retryAfterMs）——
// 本适配器在 refresh 采样时套用调用方闭包完成换算，并回传最后一次采样的
// 完整状态（与 upstreamdispatch.go 消费侧 `waitState` 的
// AllSuppressed/Accounts 读取一致）。当前生产本地抑制写面已退场（恒空，
// AllSuppressed 不触发），本接线属端口完备性修复：suppression 重新启用时
// 等待分支不再踩空。
//
// 复用 chainRuntimeServices.DispatchRecoverableWait 与 preauth Recoverable
// 同一 PreAuthRecoverableWait 实例（无状态：Coordinator + Logger + Options），
// 唤醒面共用 DefaultRecoverableWaitCoordinator（compose.go 的
// WakeRecoverableWaiter 通知同一 coordinator）。

import (
	"context"
	"sync"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// chainDispatchRecoverableWait implements gatewaydispatch.RecoverableSuppressionWaiter
// over the G11 wait engine.
type chainDispatchRecoverableWait struct {
	wait *gatewaycircuit.PreAuthRecoverableWait
}

// WaitForState runs the recoverable wait loop over the dispatch suppression
// state. 超时（skipped）不视为错误：返回最后一次采样的状态，由消费侧的
// AllSuppressed 分叉决定耗尽退出；Refresh 错误与信号取消原样透传（消费侧
// 把 context.Canceled 转为请求中止）。
func (w *chainDispatchRecoverableWait) WaitForState(ctx context.Context, input gatewaydispatch.SuppressionWaitInput) (gatewaydispatch.SuppressionFilterResult, error) {
	var (
		stateMu sync.Mutex
		latest  gatewaydispatch.SuppressionFilterResult
	)
	refresh := gatewaycircuit.StateWaitRefresh(func(ctx context.Context) (bool, bool, int64, error) {
		sampled, err := input.Refresh(ctx)
		if err != nil {
			return false, false, 0, err
		}
		stateMu.Lock()
		latest = sampled
		stateMu.Unlock()
		ready := input.IsReady(sampled)
		if retryAfter := input.NextRetryAfterMs(sampled); retryAfter != nil {
			return ready, true, *retryAfter, nil
		}
		return ready, false, 0, nil
	})
	_, _, err := w.wait.WaitForStateLoop(ctx, gatewaycircuit.StateWaitInput{
		ScopeKey:                 input.ScopeKey,
		Reason:                   input.Reason,
		AuditCapture:             input.AuditCapture,
		MaxWaitMs:                input.MaxWaitMs,
		RequestStartedAtMs:       input.RequestStartedAtMs,
		DeadlineAtMs:             input.DeadlineAtMs,
		RouteCoordinationBudget:  input.RouteCoordinationBudget,
		GatewayRequestWallBudget: input.GatewayRequestWallBudget,
		Signal:                   input.Signal,
		Refresh:                  refresh,
	})
	if err != nil {
		return gatewaydispatch.SuppressionFilterResult{}, err
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	return latest, nil
}
