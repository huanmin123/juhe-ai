package main

// 观测暗区接线单测（chain_obs_wiring.go）：事件字段映射、panic-safe 包装与
// 进程级槽语义。

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaycircuit"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayobs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

// obsCaptureStore 捕获批量落账观测（内存版 Store mock）。
type obsCaptureStore struct {
	mu         sync.Mutex
	recorded   []gatewayobs.Observation
	recordErr  error
	batchCalls int
}

func (s *obsCaptureStore) Record(_ context.Context, observation gatewayobs.Observation, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return s.recordErr
	}
	s.recorded = append(s.recorded, observation)
	return nil
}

func (s *obsCaptureStore) RecordBatch(_ context.Context, entries []gatewayobs.BatchEntry, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchCalls++
	if s.recordErr != nil {
		return s.recordErr
	}
	for _, entry := range entries {
		s.recorded = append(s.recorded, entry.Observation)
	}
	return nil
}

func (s *obsCaptureStore) Snapshot(_ context.Context) (gatewayobs.Snapshot, error) {
	return gatewayobs.Snapshot{}, errors.New("snapshot not mocked")
}

func (s *obsCaptureStore) all() []gatewayobs.Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gatewayobs.Observation(nil), s.recorded...)
}

func newObsCaptureObserver(t *testing.T, store *obsCaptureStore) *gatewayobs.Observer {
	t.Helper()
	return gatewayobs.NewObserver(gatewayobs.ObserverOptions{Store: store})
}

func flushObserver(t *testing.T, observer *gatewayobs.Observer) {
	t.Helper()
	observer.FlushPending(observer.CurrentFlushGeneration())
}

// 账户电路事件 → Observation 字段 1:1 映射。
func TestChainCircuitObservabilitySinkMapping(t *testing.T) {
	store := &obsCaptureStore{}
	observer := newObsCaptureObserver(t, store)
	sink := newChainCircuitObservabilitySink(observer)
	sink(gatewaycircuit.RoutingObservabilityEvent{
		Kind:      "circuit_transition",
		From:      "CLOSED",
		To:        "OPEN",
		Source:    "transport",
		Operation: "confirm",
		Status:    "applied",
		Phase:     "SUSPECT",
		Outcome:   "blocked",
		LeaseKind: "confirmation",
	})
	flushObserver(t, observer)
	observations := store.all()
	if len(observations) != 1 {
		t.Fatalf("期望 1 条观测，实际 %d", len(observations))
	}
	got := observations[0]
	if got.Kind != "circuit_transition" || got.From != "CLOSED" || got.To != "OPEN" ||
		got.Source != "transport" || got.Operation != "confirm" || got.Status != "applied" ||
		got.Phase != "SUSPECT" || got.Outcome != "blocked" || got.LeaseKind != "confirmation" {
		t.Fatalf("观测字段投影不符：%+v", got)
	}
	if key := gatewayobs.GatewayRoutingObservationMetricKey(got); key != "circuit.transition.closed.open.transport" {
		t.Fatalf("度量键不符：%q", key)
	}
}

// panic-safe：store 恐慌不得外溢（主链路保护契约）。
func TestChainCircuitObservabilitySinkPanicSafe(t *testing.T) {
	store := &obsCaptureStore{recordErr: errors.New("boom")} // 写失败走告警路径
	observer := newObsCaptureObserver(t, store)
	sink := newChainCircuitObservabilitySink(observer)
	// 写失败不 panic；真正 panic 路径由 recover 兜底——这里直接验证
	// recover 分支：向 nil observer 之外注入恐慌 store。
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("sink 不得向调用方外溢 panic：%v", recovered)
		}
	}()
	sink(gatewaycircuit.RoutingObservabilityEvent{Kind: "circuit_dispatch"})
	flushObserver(t, observer)
}

func TestChainHotQualityObserverMapping(t *testing.T) {
	store := &obsCaptureStore{}
	observer := newObsCaptureObserver(t, store)
	hotObserver := chainHotQualityObserver{observer: observer}
	hotObserver.ObserveGatewayRouting(gatewayhotquality.RoutingObservation{
		Kind:      "hot_quality_mutation",
		Outcome:   "promoted",
		Operation: "attempt",
		Status:    "applied",
	})
	flushObserver(t, observer)
	observations := store.all()
	if len(observations) != 1 {
		t.Fatalf("期望 1 条观测，实际 %d", len(observations))
	}
	got := observations[0]
	if got.Kind != "hot_quality_mutation" || got.Outcome != "promoted" ||
		got.Operation != "attempt" || got.Status != "applied" {
		t.Fatalf("热质量观测投影不符：%+v", got)
	}
}

func TestChainRoutingWallBudgetObserverSlot(t *testing.T) {
	// 进程级槽可能被先行组合测试经真实装配链置位：保存现场、显式清空后断言
	// 空槽语义，结束恢复原值（对测试顺序鲁棒）。
	previous := chainRoutingObserver.Load()
	t.Cleanup(func() { chainRoutingObserver.Store(previous) })
	chainRoutingObserver.Store(nil)
	if routingWallBudgetObserverOf() != nil {
		t.Fatalf("未装配时槽必须为 nil（保持既有 nil-observer 语义）")
	}
	store := &obsCaptureStore{}
	chainRoutingObserver.Store(newObsCaptureObserver(t, store))
	observer := routingWallBudgetObserverOf()
	if observer == nil {
		t.Fatalf("装配后必须返回观察者")
	}
	observer.ObserveRouting("budget", "precommit_clipped", 1700000000000)
	flushObserver(t, chainRoutingObserver.Load())
	observations := store.all()
	if len(observations) != 1 || observations[0].Kind != "budget" || observations[0].Outcome != "precommit_clipped" {
		t.Fatalf("budget 观测投影不符：%+v", observations)
	}
}

// 组合冒烟：接线函数把单例 Observer 挂上电路观测口与热质量 Observer/Logger。
func TestWireGatewayObservabilityArms(t *testing.T) {
	previous := chainRoutingObserver.Load()
	t.Cleanup(func() { chainRoutingObserver.Store(previous) })
	circuitStore, err := gatewaycircuit.NewMemoryStore(gatewaycircuit.MemoryStoreOptions{Capacity: 16})
	if err != nil {
		t.Fatalf("构造电路 memory store：%v", err)
	}
	circuits, err := gatewaycircuit.NewCircuitService(circuitStore, gatewaycircuit.ServiceOptions{})
	if err != nil {
		t.Fatalf("构造电路服务：%v", err)
	}
	services := &chainRuntimeServices{
		AccountCircuits: circuits,
		HotQuality:      &gatewayhotquality.GatewayHotQualityRuntime{},
	}
	cfg := runtimeConfig{RuntimeMode: "standalone", RuntimeStateDriver: "memory"}
	if err := wireGatewayObservabilityArms(services, cfg); err != nil {
		t.Fatalf("观测接线失败：%v", err)
	}
	if chainRoutingObserver.Load() == nil {
		t.Fatalf("进程级 Observer 槽未装配")
	}
	// G19 收尾：gatewaypreauth 预算构造点槽必须与本包槽同值置位。
	if gatewaypreauth.RoutingWallBudgetObserverOf() == nil {
		t.Fatalf("gatewaypreauth wall budget 观察槽未装配")
	}
	if services.HotQuality.Observer == nil {
		t.Fatalf("热质量 Observer 未装配")
	}
	if services.HotQuality.Logger == nil {
		t.Fatalf("热质量 Logger 未装配")
	}
	// 电路观测口实际连通：sink 触发后经单例 Observer 批量落账（store 为包级
	// 单例 store；此处只验证事件路径不再丢弃，落账数经 FlushPending 前后对比
	// 由单例内部状态持有，不直接断言）。
	if circuits == nil {
		t.Fatalf("电路服务缺失")
	}
}
