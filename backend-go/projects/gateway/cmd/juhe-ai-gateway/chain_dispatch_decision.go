package main

// W1b：网关调度决策日志接线。候选准备完成时，gatewaydispatch 引擎经
// SetDispatchDecisionObserver（进程级观察槽，chain_compose.go 组合根装配）
// 发出 DispatchDecisionEvent；本文件把事件投影为一条 slog 结构化日志
// gateway_dispatch_decision——每请求（含每次重派）至多一条，级别 info，
// skippedTop 截断防大分组日志爆炸。
//
// panic-safe 契约：观察回调整体 recover（与 chain_obs_wiring.go 的观测
// adapter 同契约），观测故障只告警、绝不影响主链路。

import (
	"log/slog"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
)

// chainDispatchDecisionSkippedTopCap 是决策日志 skippedTop 的条数上限
// （与引擎侧摘要列表截断同量级；双层各自保护审计与日志两个出口）。
const chainDispatchDecisionSkippedTopCap = 20

// chainDispatchDecisionObserver 把引擎决策事件投影到 slog。
type chainDispatchDecisionObserver struct {
	logger *slog.Logger
}

// newChainDispatchDecisionObserver 构造观察回调（nil logger 回落 slog.
// Default()，与 newSlogObservability 同风格）。
func newChainDispatchDecisionObserver(logger *slog.Logger) func(gatewaydispatch.DispatchDecisionEvent) {
	if logger == nil {
		logger = slog.Default()
	}
	observer := &chainDispatchDecisionObserver{logger: logger}
	return observer.observe
}

func (o *chainDispatchDecisionObserver) observe(event gatewaydispatch.DispatchDecisionEvent) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Warn("网关调度决策日志回调异常，已保留主链路",
				"event", "gateway_dispatch_decision_observer_panic",
				"recover", recovered)
		}
	}()
	summary := event.Summary
	fields := map[string]any{
		"event":              "gateway_dispatch_decision",
		"traceId":            event.TraceID,
		"groupId":            event.GroupID,
		"apiKeyId":           event.APIKeyID,
		"candidateTotal":     summary.CandidateTotal,
		"eligibleCount":      summary.EligibleCount,
		"modelRankAvailable": summary.ModelRankAvailable,
		"durationMs":         event.DurationMs,
	}
	if event.SystemAccountID != "" {
		fields["systemAccountId"] = event.SystemAccountID
	}
	if event.TrafficSource != "" {
		fields["trafficSource"] = event.TrafficSource
	}
	if summary.SelectedAccountID != "" {
		fields["selectedAccountId"] = summary.SelectedAccountID
	}
	// preFilterSkipped：窗口之前能力/模型过滤的逐账户跳过明细（W1b 续）。
	// 带计数；明细超过上限时截断并置位（引擎侧已按过滤执行顺序产出）。
	// 注意：引擎侧 cappedSlice 已截断到同上限，此分支是日志出口的第二层
	// 防御，正常数据流不触发（仅直接构造超限事件的调用方可达）。
	if summary.PreFilterSkippedCount > 0 {
		fields["preFilterSkippedCount"] = summary.PreFilterSkippedCount
		preFilterTop := summary.PreFilterSkipped
		if len(preFilterTop) > chainDispatchDecisionSkippedTopCap {
			preFilterTop = preFilterTop[:chainDispatchDecisionSkippedTopCap]
			fields["preFilterSkippedTruncated"] = true
		}
		top := make([]map[string]any, 0, len(preFilterTop))
		for _, skip := range preFilterTop {
			top = append(top, map[string]any{"id": skip.AccountID, "reason": skip.Reason})
		}
		fields["preFilterSkipped"] = top
	}
	// skippedTop：摘要 skipped 的截断投影（引擎侧已按 ID 稳定排序，截断
	// 窗口确定可回放）。
	skippedTop := summary.Skipped
	if len(skippedTop) > chainDispatchDecisionSkippedTopCap {
		skippedTop = skippedTop[:chainDispatchDecisionSkippedTopCap]
		fields["skippedTopTruncated"] = true
	}
	if len(skippedTop) > 0 {
		top := make([]map[string]any, 0, len(skippedTop))
		for _, skip := range skippedTop {
			top = append(top, map[string]any{"id": skip.AccountID, "reason": skip.Reason})
		}
		fields["skippedTop"] = top
		fields["skippedCount"] = len(summary.Skipped)
	}
	if len(summary.Busy) > 0 {
		fields["busyAccountIds"] = summary.Busy
	}
	if len(summary.Suppressed) > 0 {
		fields["suppressedAccountIds"] = summary.Suppressed
	}
	if len(summary.Degraded) > 0 {
		fields["degradedAccountIds"] = summary.Degraded
	}
	if len(summary.Avoided) > 0 {
		fields["avoidedAccountIds"] = summary.Avoided
	}
	o.logger.Info("gateway_dispatch_decision", fieldsArgs(fields)...)
}
