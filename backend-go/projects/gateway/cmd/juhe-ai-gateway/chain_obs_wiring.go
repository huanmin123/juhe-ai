package main

// 观测暗区接线（G19 收口，按 internal/gatewayobs/doc.go 的预期接线清单）：
//
//   - gatewaycircuit.CircuitService.SetObservabilitySink：账户电路的
//     circuit_dispatch / circuit_mutation / circuit_transition 事件装入
//     gatewayobs.Observation（字段 1:1 同名）后经 Observer.Observe 批量落账。
//   - gatewayhotquality.GatewayHotQualityRuntime 的 Observer / Logger：attempt
//     / hot_quality_mutation / exploration 观察走 ObserveGatewayRouting（本包
//     同形 RoutingObservation），warn 日志落 slog。
//   - gatewayrouting.NewGatewayRequestWallBudget 的 RoutingObserver：budget
//     precommit_clipped 观察（此前两处显式 nil；本文件提供进程级 Observer
//     槽，chain_v1.go newRequestBudgets 消费）。gatewaypreauth/preflight.go
//     的构造点在 internal/gatewaypreauth（越出本写入域），其裁剪消费点只在
//     dispatch 侧（dispatchsingle.go 读 chain 传入的预算对象），当前无观测
//     损失，登记为残留。
//
// panic-safe 契约：全部回调整体 recover（gatewayobs.Observe 本身无
// recover），观测故障只告警、绝不影响主链路。

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayobs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
)

// chainRoutingObserver 是 wall budget 等无构造期句柄的接线点共用的进程级
// Observer 槽；nil（未装配）时各消费点保持既有 nil-observer 语义。
var chainRoutingObserver atomic.Pointer[gatewayobs.Observer]

// chainSlogObserverLogger adapts slog onto the gatewayobs.Logger
// (Info/Warn/Debug fields,message lines).
type chainSlogObserverLogger struct{ inner *slog.Logger }

func (l chainSlogObserverLogger) Info(fields map[string]interface{}, msg string) {
	l.inner.Info(msg, fieldsArgs(convertAnyFields(fields))...)
}
func (l chainSlogObserverLogger) Warn(fields map[string]interface{}, msg string) {
	l.inner.Warn(msg, fieldsArgs(convertAnyFields(fields))...)
}
func (l chainSlogObserverLogger) Debug(fields map[string]interface{}, msg string) {
	l.inner.Debug(msg, fieldsArgs(convertAnyFields(fields))...)
}

func convertAnyFields(fields map[string]interface{}) map[string]any {
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out
}

// newChainCircuitObservabilitySink adapts the gatewaycircuit observability
// sink onto the gatewayobs Observer（RoutingObservabilityEvent → Observation
// 字段 1:1；panic-safe）。
func newChainCircuitObservabilitySink(observer *gatewayobs.Observer) func(event gatewaycircuit.RoutingObservabilityEvent) {
	return func(event gatewaycircuit.RoutingObservabilityEvent) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Warn("账户电路观测回调异常，已保留主链路",
					"event", "gateway_circuit_observability_panic",
					"recover", recovered)
			}
		}()
		if observer == nil {
			return
		}
		observer.Observe(gatewayobs.Observation{
			Kind:      event.Kind,
			Outcome:   event.Outcome,
			Phase:     event.Phase,
			Operation: event.Operation,
			Status:    event.Status,
			LeaseKind: event.LeaseKind,
			From:      event.From,
			To:        event.To,
			Source:    event.Source,
		}, time.Now().UnixMilli())
	}
}

// chainHotQualityObserver adapts the hot-quality runtime observer onto the
// gatewayobs Observer（RoutingObservation 同形投影；panic-safe）。
type chainHotQualityObserver struct{ observer *gatewayobs.Observer }

func (o chainHotQualityObserver) ObserveGatewayRouting(observation gatewayhotquality.RoutingObservation) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Warn("热质量观测回调异常，已保留主链路",
				"event", "gateway_hot_quality_observability_panic",
				"recover", recovered)
		}
	}()
	if o.observer == nil {
		return
	}
	o.observer.ObserveGatewayRouting(gatewayobs.RoutingObservation{
		Kind:      observation.Kind,
		Outcome:   observation.Outcome,
		Operation: observation.Operation,
		Status:    observation.Status,
	})
}

// chainRoutingWallBudgetObserver adapts the gatewayrouting.RoutingObserver
// shape onto the process-level gatewayobs Observer（budget 裁剪观察；
// panic-safe；未装配时 no-op，等价此前 nil observer）。
type chainRoutingWallBudgetObserver struct{ observer *gatewayobs.Observer }

func (o chainRoutingWallBudgetObserver) ObserveRouting(kind, outcome string, nowMs int64) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Warn("路由预算观测回调异常，已保留主链路",
				"event", "gateway_budget_observability_panic",
				"recover", recovered)
		}
	}()
	if o.observer == nil {
		return
	}
	o.observer.ObserveRouting(kind, outcome, nowMs)
}

// routingWallBudgetObserverOf returns the panic-safe wall budget observer for
// the process-level slot（nil 保持 nil-observer 语义）。
func routingWallBudgetObserverOf() gatewayrouting.RoutingObserver {
	observer := chainRoutingObserver.Load()
	if observer == nil {
		return nil
	}
	return chainRoutingWallBudgetObserver{observer: observer}
}

// wireGatewayObservabilityArms 装配观测暗区（chain_runtime.go 组合根调用，
// 位置在 AccountCircuits 与 HotQuality 构造之后）。Observer 取 gatewayobs
// 包级单例（按 runtime 驱动身份构造 store；standalone=memory /
// performance=redis，与 hotquality 同轴），构造失败 fail-fast——与该文件的
// 组合根契约一致。
func wireGatewayObservabilityArms(services *chainRuntimeServices, cfg runtimeConfig) error {
	if services == nil {
		return nil
	}
	observer, err := gatewayobs.GetGatewayRoutingObservability(context.Background(), gatewayobs.RuntimeDriverConfig{
		RuntimeMode:        cfg.RuntimeMode,
		RuntimeStateDriver: cfg.RuntimeStateDriver,
		RedisStateURL:      cfg.RedisStateURL,
		RedisNamespace:     cfg.RedisNamespace,
	}, func(options *gatewayobs.ObserverOptions) {
		if options.Logger == nil {
			options.Logger = chainSlogObserverLogger{inner: slog.Default()}
		}
	})
	if err != nil {
		return err
	}
	chainRoutingObserver.Store(observer)
	if services.AccountCircuits != nil {
		services.AccountCircuits.SetObservabilitySink(newChainCircuitObservabilitySink(observer))
	}
	if services.HotQuality != nil {
		services.HotQuality.Observer = chainHotQualityObserver{observer: observer}
		services.HotQuality.Logger = chainSlogObserverLogger{inner: slog.Default()}
	}
	return nil
}
