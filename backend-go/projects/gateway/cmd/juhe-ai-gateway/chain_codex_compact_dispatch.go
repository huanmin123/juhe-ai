package main

// T5（BUG-0274 残余收口）：Codex 桥 compact 合成摘要 dispatcher 的生产实现。
// gatewaycodex.CompactSummaryDispatcher seam（Node compact-preflight.ts 的
// fetchFirstAvailableUpstream + readUpstreamBodyLimited + hotQualityAttempt
// 记账 + releaseConcurrency）在 Go 侧直接复用冻结的 dispatch 引擎：
//
//	input（透传句柄）→ FetchFirstAvailableUpstreamArgs 投影 → 引擎候选循环 →
//	成功结果 ReadUpstreamBodyLimited 有限读回 → CompactUpstreamExchange。
//
// 成功/失败结算口径归消费者（BUG-0241：读回完成即结算质量终态；引擎错误、
// 读回错误经消费者 502 收口），本适配器只承载引擎入参投影与读回装配。

import (
	"context"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycodex"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// chainCompactSummaryDispatcher adapts gatewaydispatch.Engine to the
// gatewaycodex.CompactSummaryDispatcher seam. auditOf 是既有链面投影
// （gatewayChain.engineAuditCapture）的绑定方法值，适配器不复制审计装配。
type chainCompactSummaryDispatcher struct {
	engine  *gatewaydispatch.Engine
	auditOf func(gatewaypreauth.AuditCaptureContext) gatewaydispatch.AuditCapture
}

// DispatchCompactSummary implements gatewaycodex.CompactSummaryDispatcher.
func (d *chainCompactSummaryDispatcher) DispatchCompactSummary(ctx context.Context, input gatewaycodex.CompactSummaryDispatchInput) (*gatewaycodex.CompactUpstreamExchange, error) {
	// AccountStateMutationEnabled / WaitForRecoverableFailures 与主环同源推导
	//（chain_v1_loop.go:429 AccountStateMutationEnabled =
	// UsageContext.TrafficSource == gatewayTrafficSource），不照抄 Node
	// compact-preflight 的字面量 true/true。取舍理由（Go 引擎内一致性优先）：
	//   - 状态突变门沿用主环口径——探针/诊断等非网关流量不写账户状态；
	//   - WaitForRecoverableFailures 取 true 臂的依据：合成摘要在固定候选窗
	//     口上派发（preflight 已过滤出桥账户），没有主环的备用分组切换循环，
	//     等价主环 `bindings <= 1`（单窗口等待恢复而非快速耗尽）；非网关
	//     流量随状态突变门一并关闭，保持引擎内两闸同源。
	gatewayTraffic := input.UsageContext.TrafficSource == gatewayTrafficSource
	dispatched, err := d.engine.FetchFirstAvailableUpstream(ctx, gatewaydispatch.FetchFirstAvailableUpstreamArgs{
		Req:                             input.SyntheticRequest,
		Accounts:                        input.Accounts,
		Settings:                        input.Settings,
		UsageContext:                    input.UsageContext,
		AuditCapture:                    d.auditOf(input.AuditCapture),
		Signal:                          input.Signal,
		RequestLane:                     input.RequestLane,
		GroupSchedulingPolicy:           input.GroupSchedulingPolicy,
		AccountStateMutationEnabled:     gatewayTraffic,
		RequestClientCompatibility:      input.RequestClientCompatibility,
		ModelPriority:                   input.ModelPriority,
		ClientIPAccountAvoidanceTracker: chainCompactClientIPAvoidanceOf(input.ClientIPAccountAvoidance),
		RequestCoordination:             chainCompactDispatchCoordinationOf(input.RequestCoordination),
		WaitForRecoverableFailures:      gatewayTraffic,
	})
	if err != nil {
		return nil, err
	}
	response := dispatched.Response
	exchange := &gatewaycodex.CompactUpstreamExchange{
		Account:            dispatched.Account,
		UpstreamOK:         response.OK(),
		UpstreamStatus:     response.Status(),
		RecordTerminal:     chainCompactRecordTerminalOf(ctx, dispatched.HotQualityAttempt),
		ReleaseConcurrency: dispatched.ReleaseConcurrency,
	}
	// 有限读回（Node readUpstreamBodyLimited）：1MB 上限 + 首字经
	// markFirstOutput 进本尝试热质量生命周期（lifecycle 内只记首个有效值）。
	// 读失败时 exchange 随错误一并返回：消费者 catch 路径用同一 exchange
	// 结算 read_interruption 终态并释放并发槽（Node catch recordTerminal +
	// releaseConcurrency 同构），适配器不得提前 release 造成槽泄漏或双结算。
	maxBytes := int64(gatewaycodex.CompactSummaryMaxBytes)
	startedAt := input.StartedAt
	read, readErr := gatewayupstream.ReadUpstreamBodyLimited(ctx, response.Body, gatewayupstream.LimitedBodyReadInput{
		MaxBytes:    &maxBytes,
		StartedAt:   &startedAt,
		Signal:      input.Signal,
		OnFirstByte: dispatched.MarkFirstOutput,
	})
	// 读回终止后关闭上游体（ReadUpstreamBodyLimited 只在截断/读错误路径
	// 内部关闭；干净 EOF 时由这里释放 keep-alive 连接；重复 Close 幂等）。
	_ = gatewayupstream.CloseReader(response.Body)
	if readErr != nil {
		return exchange, readErr
	}
	exchange.BodyText = read.BodyText
	exchange.Truncated = read.Truncated
	if read.FirstByteMs != nil {
		exchange.FirstByteMs = *read.FirstByteMs
	}
	return exchange, nil
}

// chainCompactDispatchCoordinationOf 把 preauth 端口镜像的请求协调上下文投影
// 为引擎入参形状（gatewaypreauth/ports.go CodexRequestCoordination →
// gatewaydispatch.RequestCoordinationContext 同名字段 1:1；两包镜像类型逐字
// 段复制）。TimeoutsDisabled 按主环同源推导恢复
// （chain_v1_loop.go newRequestCoordination：wall budget Unbounded = 超时豁免，
// 压缩请求在 preflight 内 WithoutLimit → Unbounded）；TotalTimeLane 不在端口
// 镜像内，保持零值（normal 档）。容器非目标类型（组合测试注入）返回 nil，
// 引擎按缺协调显式报错（errMissingCoordination）。
func chainCompactDispatchCoordinationOf(raw any) *gatewaydispatch.RequestCoordinationContext {
	coordination, ok := raw.(gatewaypreauth.CodexRequestCoordination)
	if !ok {
		return nil
	}
	projected := &gatewaydispatch.RequestCoordinationContext{
		Scope:                    coordination.Scope,
		ServerRetryBudget:        coordination.ServerRetryBudget,
		GatewayRequestWallBudget: coordination.GatewayRequestWallBudget,
		RouteCoordinationBudget:  coordination.RouteCoordinationBudget,
		RequestAttemptTracker:    coordination.RequestAttemptTracker,
	}
	projected.TimeoutsDisabled = coordination.GatewayRequestWallBudget != nil && coordination.GatewayRequestWallBudget.Unbounded
	return projected
}

// chainCompactClientIPAvoidanceOf 把放宽为 any 的透传句柄收窄回引擎入参类型
// （gatewaypreauth.ClientIPAccountAvoidanceTracker 即引擎入参类型；断言失败
// = 句柄缺席，保持 nil）。
func chainCompactClientIPAvoidanceOf(raw any) gatewaypreauth.ClientIPAccountAvoidanceTracker {
	if tracker, ok := raw.(gatewaypreauth.ClientIPAccountAvoidanceTracker); ok {
		return tracker
	}
	return nil
}

// chainCompactRecordTerminalOf 把引擎带出的热质量 attempt 生命周期包成消费者
// 终态闭包。FirstByteMs 按 attempt 生命周期既有口径 int64 → *float64
// （gatewayhotquality RecordTerminal：显式值优先、nil 回落 MarkFirstByte 缓存；
// 0 视为无显式样本走缓存回落，与 Node terminal.firstByteMs ?? firstByteMs
// 同语义）。句柄缺席（引擎未装配热质量工厂）返回 nil，消费者按无终态跳过。
func chainCompactRecordTerminalOf(ctx context.Context, attempt gatewaydispatch.HotQualityAttemptLifecycle) func(gatewaycodex.CompactQualityTerminal) {
	if attempt == nil {
		return nil
	}
	return func(terminal gatewaycodex.CompactQualityTerminal) {
		var firstByteMs *float64
		if terminal.FirstByteMs > 0 {
			value := float64(terminal.FirstByteMs)
			firstByteMs = &value
		}
		attempt.RecordTerminal(ctx, gatewaydispatch.HotQualityTerminal{
			OutcomeClass: terminal.OutcomeClass,
			FailureScope: terminal.FailureScope,
			Source:       terminal.Source,
			FirstByteMs:  firstByteMs,
		})
	}
}
