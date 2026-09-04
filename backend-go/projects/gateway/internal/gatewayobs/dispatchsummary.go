package gatewayobs

import "context"

// GatewayRoutingDispatchSummary mirrors shared/request-context.ts 的
// GatewayRoutingDispatchSummary（含 JSON 序列化字段名）。
type GatewayRoutingDispatchSummary struct {
	ObservedEvents           int64 `json:"observedEvents"`
	DroppedEvents            int64 `json:"droppedEvents"`
	AttemptsStarted          int64 `json:"attemptsStarted"`
	AttemptsCompleted        int64 `json:"attemptsCompleted"`
	AttemptsFailed           int64 `json:"attemptsFailed"`
	CircuitTransitions       int64 `json:"circuitTransitions"`
	CircuitSkips             int64 `json:"circuitSkips"`
	CircuitCasConflicts      int64 `json:"circuitCasConflicts"`
	CircuitLeasesAcquired    int64 `json:"circuitLeasesAcquired"`
	CircuitLeasesRejected    int64 `json:"circuitLeasesRejected"`
	HotQualityDeduplications int64 `json:"hotQualityDeduplications"`
	HotQualityConflicts      int64 `json:"hotQualityConflicts"`
	ExplorationsReserved     int64 `json:"explorationsReserved"`
	ExplorationsDispatched   int64 `json:"explorationsDispatched"`
	TierEscapes              int64 `json:"tierEscapes"`
	WallBudgetExhausted      int64 `json:"wallBudgetExhausted"`
	PrecommitClipped         int64 `json:"precommitClipped"`
	ClientHandoffs           int64 `json:"clientHandoffs"`
}

// perRequestObservationLimit mirrors perRequestObservationLimit.
const perRequestObservationLimit = 128

// DispatchSummaryHolder 承载每请求惰性创建的 dispatch summary，等价于 Node
// RequestContext.gatewayRoutingDispatchSummary 的 `??=` 语义：观察发生前
// Summary 保持 nil（请求侧不输出空摘要），首次观察时才创建。
type DispatchSummaryHolder struct {
	Summary *GatewayRoutingDispatchSummary
}

type dispatchSummaryKey struct{}

// WithDispatchSummaryHolder 在 ctx 上挂载 summary 载体（等价
// withRequestContext 环境里 summary 可用）。
func WithDispatchSummaryHolder(ctx context.Context) context.Context {
	return context.WithValue(ctx, dispatchSummaryKey{}, &DispatchSummaryHolder{})
}

// DispatchSummaryHolderFromContext 返回载体；无请求上下文时为 nil。
func DispatchSummaryHolderFromContext(ctx context.Context) *DispatchSummaryHolder {
	if ctx == nil {
		return nil
	}
	holder, _ := ctx.Value(dispatchSummaryKey{}).(*DispatchSummaryHolder)
	return holder
}

// DispatchSummaryFromContext 返回已存在的 summary（不创建）。
func DispatchSummaryFromContext(ctx context.Context) *GatewayRoutingDispatchSummary {
	holder := DispatchSummaryHolderFromContext(ctx)
	if holder == nil {
		return nil
	}
	return holder.Summary
}

func emptyDispatchSummary() *GatewayRoutingDispatchSummary {
	return &GatewayRoutingDispatchSummary{}
}

// captureRequestDispatchSummary mirrors captureRequestDispatchSummary：固定
// 事件上限内按 kind 累计，超出只累计 droppedEvents。
func captureRequestDispatchSummary(ctx context.Context, observation Observation) {
	holder := DispatchSummaryHolderFromContext(ctx)
	if holder == nil {
		return
	}
	if holder.Summary == nil {
		holder.Summary = emptyDispatchSummary()
	}
	summary := holder.Summary
	if summary.ObservedEvents >= perRequestObservationLimit {
		summary.DroppedEvents = saturatedAdd(summary.DroppedEvents, 1)
		return
	}
	summary.ObservedEvents = saturatedAdd(summary.ObservedEvents, 1)
	switch observation.Kind {
	case KindAttempt:
		switch observation.Outcome {
		case "started":
			summary.AttemptsStarted = saturatedAdd(summary.AttemptsStarted, 1)
		case "completed":
			summary.AttemptsCompleted = saturatedAdd(summary.AttemptsCompleted, 1)
		default:
			summary.AttemptsFailed = saturatedAdd(summary.AttemptsFailed, 1)
		}
	case KindCircuitTransition:
		if observation.From != observation.To {
			summary.CircuitTransitions = saturatedAdd(summary.CircuitTransitions, 1)
		}
	case KindCircuitMutation:
		if observation.Status != "applied" {
			summary.CircuitSkips = saturatedAdd(summary.CircuitSkips, 1)
		}
		if observation.Status == "stale_generation" || observation.Status == "stale_dispatch_revision" || observation.Status == "state_mismatch" {
			summary.CircuitCasConflicts = saturatedAdd(summary.CircuitCasConflicts, 1)
		}
		if observation.LeaseKind != "" {
			if observation.Status == "applied" {
				summary.CircuitLeasesAcquired = saturatedAdd(summary.CircuitLeasesAcquired, 1)
			} else {
				summary.CircuitLeasesRejected = saturatedAdd(summary.CircuitLeasesRejected, 1)
			}
		}
	case KindCircuitDispatch:
		summary.CircuitSkips = saturatedAdd(summary.CircuitSkips, 1)
	case KindHotQualityMutation:
		if observation.Status == "idempotent" {
			summary.HotQualityDeduplications = saturatedAdd(summary.HotQualityDeduplications, 1)
		}
		if observation.Status == "conflict" {
			summary.HotQualityConflicts = saturatedAdd(summary.HotQualityConflicts, 1)
		}
	case KindExploration:
		if observation.Outcome == "reserved" {
			summary.ExplorationsReserved = saturatedAdd(summary.ExplorationsReserved, 1)
		}
		if observation.Outcome == "dispatched" {
			summary.ExplorationsDispatched = saturatedAdd(summary.ExplorationsDispatched, 1)
		}
	case KindTierEscape:
		if observation.Outcome == "applied" {
			summary.TierEscapes = saturatedAdd(summary.TierEscapes, 1)
		}
	case KindBudget:
		if observation.Outcome == "wall_exhausted" {
			summary.WallBudgetExhausted = saturatedAdd(summary.WallBudgetExhausted, 1)
		}
		if observation.Outcome == "precommit_clipped" {
			summary.PrecommitClipped = saturatedAdd(summary.PrecommitClipped, 1)
		}
		if observation.Outcome == "client_handoff" {
			summary.ClientHandoffs = saturatedAdd(summary.ClientHandoffs, 1)
		}
	}
}
