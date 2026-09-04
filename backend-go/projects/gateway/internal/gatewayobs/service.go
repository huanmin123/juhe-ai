package gatewayobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// 路由观测服务，逐行为对齐
// backend/src/modules/gateway/observability/routing-observability.service.ts。
// Node 的模块级单例函数在此成为显式 *Observer；包级 Get* 函数保留单例语义。

// failureLogThrottleMs mirrors failureLogThrottleMs.
const failureLogThrottleMs = 30_000

// Logger mirrors getRequestLogger() 的 info/warn/debug 消费面。
type Logger interface {
	Info(fields map[string]interface{}, msg string)
	Warn(fields map[string]interface{}, msg string)
	Debug(fields map[string]interface{}, msg string)
}

// NopLogger 丢弃全部日志（等价无消费者）。
func NopLogger() Logger { return nopLogger{} }

type nopLogger struct{}

func (nopLogger) Info(fields map[string]interface{}, msg string)  {}
func (nopLogger) Warn(fields map[string]interface{}, msg string)  {}
func (nopLogger) Debug(fields map[string]interface{}, msg string) {}

// RuntimeDriverConfig carries the runtimeConfig inputs the Node singleton
// reads (same shape as gatewayhotquality.RuntimeDriverConfig).
type RuntimeDriverConfig struct {
	RuntimeMode        string // 'standalone' | 'performance'
	RuntimeStateDriver string // 'memory' | 'redis'
	RedisStateURL      string
	RedisNamespace     string
}

// ObserverOptions carries the injected dependencies of Observer.
type ObserverOptions struct {
	// Store 缺省由包级单例按 RuntimeDriverConfig 构造；显式注入用于测试。
	Store Store
	// Logger 缺省丢弃。
	Logger Logger
	// Now 注入时钟；缺省 time.Now().UnixMilli()。
	Now func() int64
	// ContextSource 为无 ctx 的 port 入口（ObserveGatewayRouting /
	// ObserveRouting / Observe）提供请求上下文，替代 Node 的 async-local
	// storage；缺省无请求上下文（不采集 dispatch summary）。
	ContextSource func() context.Context
}

// Observer mirrors the observeGatewayRouting / recordGatewayRoutingObservation
// module surface as an explicit type.
type Observer struct {
	store         Store
	logger        Logger
	now           func() int64
	contextSource func() context.Context
	// schedule 镜像 queueMicrotask；缺省 goroutine，测试可替换为同步调用。
	schedule func(observer *Observer, generation uint64)

	mu                        sync.Mutex
	pendingOrder              []string
	pendingObservations       map[string]BatchEntry
	pendingObservationNowMs   int64
	observationFlushScheduled bool
	observationGeneration     uint64
	lastFailureLogAtMs        int64
}

// NewObserver builds an explicit observer (DI; tests inject mocks).
func NewObserver(options ObserverOptions) *Observer {
	observer := &Observer{
		store:               options.Store,
		logger:              options.Logger,
		pendingObservations: make(map[string]BatchEntry),
		schedule:            defaultFlushSchedule,
	}
	if observer.logger == nil {
		observer.logger = NopLogger()
	}
	if options.Now != nil {
		observer.now = options.Now
	} else {
		observer.now = func() int64 { return time.Now().UnixMilli() }
	}
	observer.contextSource = options.ContextSource
	return observer
}

// requestContext 解析当前请求上下文（显式 ctx 优先，其次 ContextSource）。
func (observer *Observer) requestContext(explicit context.Context) context.Context {
	if explicit != nil {
		return explicit
	}
	if observer.contextSource != nil {
		if ctx := observer.contextSource(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

// RecordGatewayRoutingObservation mirrors recordGatewayRoutingObservation:
// direct store write; a failed write logs throttled and reports false.
func (observer *Observer) RecordGatewayRoutingObservation(ctx context.Context, observation Observation, nowMs int64) bool {
	if nowMs < 0 || !isSafeInteger(nowMs) {
		nowMs = observer.now()
	}
	requestCtx := observer.requestContext(ctx)
	captureRequestDispatchSummary(requestCtx, observation)
	observer.logRoutingObservation(observation)
	if observer.store == nil {
		observer.logWriteFailure(nowMs, observation.Kind, observer.storeUnavailableError(), "网关路由观测写入失败")
		return false
	}
	if err := observer.store.Record(ctx, observation, nowMs); err != nil {
		observer.logWriteFailure(nowMs, observation.Kind, err, "网关路由观测写入失败")
		return false
	}
	return true
}

// ObserveGatewayRouting implements the gatewayhotquality.RoutingObserver
// shape (local RoutingObservation struct; wiring adapts the nominal type).
func (observer *Observer) ObserveGatewayRouting(observation RoutingObservation) {
	observer.Observe(Observation{
		Kind:      observation.Kind,
		Outcome:   observation.Outcome,
		Operation: observation.Operation,
		Status:    observation.Status,
	}, observer.now())
}

// ObserveRouting implements the gatewayrouting.RoutingObserver shape
// (ObserveRouting(kind, outcome, nowMs)) — same signature, so *Observer
// satisfies that interface directly.
func (observer *Observer) ObserveRouting(kind, outcome string, nowMs int64) {
	observer.Observe(Observation{Kind: kind, Outcome: outcome}, nowMs)
}

// Observe mirrors observeGatewayRouting: summary + log + batched store write
// flushed off the hot path (Node queueMicrotask / Go goroutine).
func (observer *Observer) Observe(observation Observation, nowMs int64) {
	normalized := nowMs
	if normalized < 0 || !isSafeInteger(normalized) {
		normalized = observer.now()
	}
	captureRequestDispatchSummary(observer.requestContext(nil), observation)
	observer.logRoutingObservation(observation)

	key := GatewayRoutingObservationMetricKey(observation)
	var generation uint64
	observer.mu.Lock()
	if entry, exists := observer.pendingObservations[key]; exists {
		entry.Count = saturatedAdd(entry.Count, 1)
		observer.pendingObservations[key] = entry
	} else {
		observer.pendingObservations[key] = BatchEntry{Observation: observation, Count: 1}
		observer.pendingOrder = append(observer.pendingOrder, key)
	}
	if normalized > observer.pendingObservationNowMs {
		observer.pendingObservationNowMs = normalized
	}
	if observer.observationFlushScheduled {
		observer.mu.Unlock()
		return
	}
	observer.observationFlushScheduled = true
	generation = observer.observationGeneration
	observer.mu.Unlock()
	observer.schedule(observer, generation)
}

// defaultFlushSchedule mirrors queueMicrotask(() => { void flush(gen) }).
func defaultFlushSchedule(observer *Observer, generation uint64) {
	go observer.FlushPending(generation)
}

// FlushPending mirrors flushPendingObservations: guarded by the generation so
// a reset aborts stale flushes.
func (observer *Observer) FlushPending(generation uint64) {
	observer.mu.Lock()
	if generation != observer.observationGeneration {
		observer.mu.Unlock()
		return
	}
	observer.observationFlushScheduled = false
	batch := make([]BatchEntry, 0, len(observer.pendingOrder))
	for _, key := range observer.pendingOrder {
		if entry, exists := observer.pendingObservations[key]; exists {
			batch = append(batch, entry)
		}
	}
	observer.pendingObservations = make(map[string]BatchEntry)
	observer.pendingOrder = nil
	nowMs := observer.pendingObservationNowMs
	observer.pendingObservationNowMs = 0
	observer.mu.Unlock()

	if len(batch) == 0 {
		return
	}
	if observer.store == nil {
		observer.logWriteFailure(nowMs, "batch", observer.storeUnavailableError(), "网关路由观测批量写入失败")
		return
	}
	if err := observer.store.RecordBatch(context.Background(), batch, nowMs); err != nil {
		observer.logWriteFailure(nowMs, "batch", err, "网关路由观测批量写入失败")
	}
}

// CurrentFlushGeneration exposes the generation for explicit FlushPending
// callers (drain/test).
func (observer *Observer) CurrentFlushGeneration() uint64 {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.observationGeneration
}

func (observer *Observer) storeUnavailableError() error {
	return errors.New("routing observability store 未注入")
}

// logWriteFailure mirrors the throttled
// gateway_routing_observability_write_failed warn.
func (observer *Observer) logWriteFailure(nowMs int64, observationKind string, err error, msg string) {
	observer.mu.Lock()
	if nowMs-observer.lastFailureLogAtMs < failureLogThrottleMs {
		observer.mu.Unlock()
		return
	}
	observer.lastFailureLogAtMs = nowMs
	observer.mu.Unlock()
	observer.logger.Warn(map[string]interface{}{
		"event":           "gateway_routing_observability_write_failed",
		"observationKind": observationKind,
		"error":           err,
	}, msg)
}

// logRoutingObservation mirrors logRoutingObservation field for field.
func (observer *Observer) logRoutingObservation(observation Observation) {
	logger := observer.logger
	if observation.Kind == KindCircuitTransition {
		if observation.From == observation.To {
			return
		}
		fields := map[string]interface{}{
			"event":  "gateway_account_circuit_transition",
			"from":   observation.From,
			"to":     observation.To,
			"source": observation.Source,
		}
		if observation.To == "OPEN" {
			logger.Warn(fields, "账户短电路状态转换")
		} else {
			logger.Info(fields, "账户短电路状态转换")
		}
		return
	}
	if observation.Kind == KindCircuitMutation && observation.Status != "applied" {
		fields := map[string]interface{}{
			"event":     "gateway_account_circuit_dispatch_skipped",
			"operation": observation.Operation,
			"status":    observation.Status,
		}
		if observation.LeaseKind != "" {
			fields["leaseKind"] = observation.LeaseKind
		}
		logger.Debug(fields, "账户短电路派发被跳过")
		return
	}
	if observation.Kind == KindCircuitDispatch {
		logger.Debug(map[string]interface{}{
			"event":   "gateway_account_circuit_dispatch_skipped",
			"outcome": observation.Outcome,
			"phase":   observation.Phase,
		}, "账户短电路派发被跳过")
	}
}

// abandon mirrors resetGatewayRoutingObservabilityForTest 的 pending 清理：
// 丢弃未落账批次并使在途 flush 失效。
func (observer *Observer) abandon() {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.observationGeneration += 1
	observer.pendingObservations = make(map[string]BatchEntry)
	observer.pendingOrder = nil
	observer.pendingObservationNowMs = 0
	observer.observationFlushScheduled = false
	observer.lastFailureLogAtMs = 0
}

// ---------------------------------------------------------------------------
// 包级单例（getGatewayRoutingObservabilityStore 语义）
// ---------------------------------------------------------------------------

var observabilitySingleton = struct {
	sync.Mutex
	observer *Observer
	identity string
}{}

// RoutingObservabilityStoreIdentity mirrors routingObservabilityStoreIdentity.
func RoutingObservabilityStoreIdentity(config RuntimeDriverConfig) (string, error) {
	if config.RuntimeMode == "standalone" {
		return "standalone:memory", nil
	}
	redisURL := strings.TrimSpace(config.RedisStateURL)
	if config.RuntimeStateDriver != "redis" || redisURL == "" {
		return "", errors.New("performance routing observability 要求 Redis runtime state")
	}
	digest := sha256.Sum256([]byte(redisURL))
	return "performance:redis:" + hex.EncodeToString(digest[:]), nil
}

// buildRoutingObservabilityStore mirrors getGatewayRoutingObservabilityStore
// 的 store 构造分支（identity 校验先行，与 Node 一致）。
func buildRoutingObservabilityStore(ctx context.Context, config RuntimeDriverConfig) (Store, error) {
	if _, err := RoutingObservabilityStoreIdentity(config); err != nil {
		return nil, err
	}
	if config.RuntimeMode == "standalone" {
		if config.RuntimeStateDriver != "memory" {
			return nil, errors.New("standalone routing observability 要求 memory runtime state driver")
		}
		return NewMemoryGatewayRoutingObservabilityStore(), nil
	}
	if config.RuntimeStateDriver != "redis" {
		return nil, errors.New("performance routing observability 要求 redis runtime state driver")
	}
	redisURL := strings.TrimSpace(config.RedisStateURL)
	if redisURL == "" {
		return nil, errors.New("performance routing observability 缺少 JUHE_AI_REDIS_STATE_URL")
	}
	client, err := GetRedisClient(ctx, redisURL)
	if err != nil {
		return nil, err
	}
	return NewRedisGatewayRoutingObservabilityStore(NewRedisCommandClient(client), redisURL, config.RedisNamespace, "gateway-routing-observability")
}

// GetGatewayRoutingObservabilityStore returns the singleton store for the
// runtime identity.
func GetGatewayRoutingObservabilityStore(ctx context.Context, config RuntimeDriverConfig) (Store, error) {
	identity, err := RoutingObservabilityStoreIdentity(config)
	if err != nil {
		return nil, err
	}
	observabilitySingleton.Lock()
	defer observabilitySingleton.Unlock()
	if observabilitySingleton.observer != nil && observabilitySingleton.identity == identity {
		return observabilitySingleton.observer.store, nil
	}
	store, err := buildRoutingObservabilityStore(ctx, config)
	if err != nil {
		return nil, err
	}
	observabilitySingleton.observer = NewObserver(ObserverOptions{Store: store})
	observabilitySingleton.identity = identity
	return store, nil
}

// GetGatewayRoutingObservability returns the singleton observer.
func GetGatewayRoutingObservability(ctx context.Context, config RuntimeDriverConfig, options ...func(*ObserverOptions)) (*Observer, error) {
	identity, err := RoutingObservabilityStoreIdentity(config)
	if err != nil {
		return nil, err
	}
	observabilitySingleton.Lock()
	defer observabilitySingleton.Unlock()
	if observabilitySingleton.observer != nil && observabilitySingleton.identity == identity {
		return observabilitySingleton.observer, nil
	}
	store, err := buildRoutingObservabilityStore(ctx, config)
	if err != nil {
		return nil, err
	}
	observerOptions := ObserverOptions{Store: store}
	for _, apply := range options {
		apply(&observerOptions)
	}
	observabilitySingleton.observer = NewObserver(observerOptions)
	observabilitySingleton.identity = identity
	return observabilitySingleton.observer, nil
}

// GetGatewayRoutingObservabilitySnapshot mirrors
// getGatewayRoutingObservabilitySnapshot.
func GetGatewayRoutingObservabilitySnapshot(ctx context.Context, config RuntimeDriverConfig) (Snapshot, error) {
	store, err := GetGatewayRoutingObservabilityStore(ctx, config)
	if err != nil {
		return Snapshot{}, err
	}
	return store.Snapshot(ctx)
}

// ResetGatewayRoutingObservabilityForTest mirrors
// resetGatewayRoutingObservabilityForTest.
func ResetGatewayRoutingObservabilityForTest() {
	observabilitySingleton.Lock()
	defer observabilitySingleton.Unlock()
	if observabilitySingleton.observer != nil {
		observabilitySingleton.observer.abandon()
	}
	observabilitySingleton.observer = nil
	observabilitySingleton.identity = ""
}
