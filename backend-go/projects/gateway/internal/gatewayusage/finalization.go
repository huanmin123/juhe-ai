package gatewayusage

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Write-delivery pipeline mirroring failure-finalization.service.ts
// (dispatchGatewayUsageFinalization) and the frozen enqueueUsageRecord
// boundary of record-queue.service.ts.

// MemoryUsageRecorder is the in-memory UsageRecorder mock: it applies
// normalizeUsageRecordInput exactly like Node's enqueueUsageRecord first
// step and retains the normalized records. The real Redis Stream / IPC /
// spool-backed writer is assembled by the J-F/G20 wave.
type MemoryUsageRecorder struct {
	mu        sync.Mutex
	clock     Clock
	idFactory UsageRecordIDFactory
	records   []UsageRecordInput
	// Failures forces EnqueueUsageRecord to fail (port-failure tests).
	Failures int
	// Delivered counts successful enqueues.
	Delivered int
	// NormalizeErrors surfaces normalization failures to tests.
	NormalizeErrors []error
}

// NewMemoryUsageRecorder builds the recorder. clock/idFactory nil default to
// the system clock / no id factory.
func NewMemoryUsageRecorder(clock Clock, idFactory UsageRecordIDFactory) *MemoryUsageRecorder {
	if clock == nil {
		clock = SystemClock{}
	}
	return &MemoryUsageRecorder{clock: clock, idFactory: idFactory}
}

// EnqueueUsageRecord implements the UsageRecorder port. It never blocks the
// caller on write errors beyond returning the error (the dispatch pipeline
// swallows and logs it, mirroring Node trackGatewayUsageFinalization).
// Failures counts the next injected port failures (decremented per call) so
// tests can exercise "投递失败不阻塞主流程".
func (m *MemoryUsageRecorder) EnqueueUsageRecord(ctx context.Context, input UsageRecordInput) error {
	m.mu.Lock()
	failures := m.Failures
	if failures > 0 {
		m.Failures = failures - 1
		m.mu.Unlock()
		return errors.New("usage record delivery unavailable (mock)")
	}
	m.mu.Unlock()
	normalized, err := NormalizeUsageRecordInput(input, m.clock, m.idFactory)
	if err != nil {
		m.mu.Lock()
		m.NormalizeErrors = append(m.NormalizeErrors, err)
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	m.records = append(m.records, normalized)
	m.Delivered++
	m.mu.Unlock()
	return nil
}

// Records returns a copy of the retained normalized records.
func (m *MemoryUsageRecorder) Records() []UsageRecordInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]UsageRecordInput, len(m.records))
	copy(out, m.records)
	return out
}

// LastRecord returns the most recent record.
func (m *MemoryUsageRecorder) LastRecord() (UsageRecordInput, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.records) == 0 {
		return UsageRecordInput{}, false
	}
	return m.records[len(m.records)-1], true
}

// SetFailures arms the injected port failure (negative disables).
func (m *MemoryUsageRecorder) SetFailures(count int) {
	m.mu.Lock()
	m.Failures = count
	m.mu.Unlock()
}

// taskUnit mirrors a queuedGatewayUsageFinalizations entry.
type taskUnit struct {
	task  func(Ctx) error
	bytes int
}

// FinalizationDispatch is the write-delivery pipeline: bounded admission
// queue + the UsageRecorder port. It mirrors dispatchUsageRecord (records.ts)
// composed with dispatchGatewayUsageFinalization
// (failure-finalization.service.ts).
type FinalizationDispatch struct {
	queue    *GatewayUsageFinalizationQueue
	recorder UsageRecorder
	clock    Clock
	logger   Logger
}

// NewFinalizationDispatch wires the pipeline over the recorder port. maxItems /
// maxConcurrency <= 0 fall back to the Node defaults
// (usageFinalizationMaxItems 2048, concurrency.globalMax 5000 via
// JUHE_AI_CONCURRENCY_GLOBAL_MAX).
func NewFinalizationDispatch(recorder UsageRecorder, maxItems int, maxConcurrency int) *FinalizationDispatch {
	queue := NewGatewayUsageFinalizationQueue(maxItems, maxConcurrency)
	return &FinalizationDispatch{
		queue:    queue,
		recorder: recorder,
		clock:    SystemClock{},
	}
}

// EnableDropOnFull 是生产 gateway 的组合策略：usage 收尾队列满时直接丢弃
// 并计数，不反压请求协程（运营统计可丢，2026-11-09 用户裁决）。
func (d *FinalizationDispatch) EnableDropOnFull() { d.queue.EnableDropOnFull() }

// WithLogger 注入收尾队列的丢弃/失败日志端口。
func (d *FinalizationDispatch) WithLogger(logger Logger) *FinalizationDispatch {
	d.queue.WithLogger(logger)
	return d
}

// DispatchUsageRecord mirrors dispatchUsageRecord: admit
// enqueueUsageRecord(input) with the byte budget.
//
// 使用日志抑制（AI 问答免 Key 直调内部通道 §3.1）：抑制记录在这里丢弃，而不是
// 只交给 recorder 的 EnqueueUsageRecord——recorder 入口另有第二道闸，但本入口
// 是全部 usage 写入面（含绕过 finalization 的直投路径）的唯一持久化闸口。
func (d *FinalizationDispatch) DispatchUsageRecord(ctx Ctx, input UsageRecordInput, bytes int) error {
	if input.SuppressUsage {
		return nil
	}
	task := func(ctx Ctx) error {
		return d.recorder.EnqueueUsageRecord(ctx, input)
	}
	return d.queue.Dispatch(ctx, task, bytes)
}

// WaitForIdle mirrors waitForGatewayFailureUsageFinalizationsIdle.
func (d *FinalizationDispatch) WaitForIdle(timeoutMs int) bool {
	return d.queue.WaitForIdle(timeoutMs)
}

// Runtime mirrors getGatewayUsageFinalizationRuntime.
func (d *FinalizationDispatch) Runtime() GatewayUsageFinalizationRuntime {
	return d.queue.Runtime()
}

// GatewayUsageFinalizationRuntime mirrors GatewayUsageFinalizationRuntime.
type GatewayUsageFinalizationRuntime struct {
	PendingCount       int
	QueuedCount        int
	QueuedBytes        int
	ActiveCount        int
	AdmissionWaitCount int
	// DroppedCount 是满队且启用 drop-on-full 时丢弃的任务数（非业务统计数据，
	// 2026-11-09 用户裁决允许丢弃，优先保证请求不被反压）。
	DroppedCount   int
	MaxItems       int
	MaxBytes       int
	MaxConcurrency int
}

// GatewayUsageFinalizationQueue mirrors the module state of
// failure-finalization.service.ts: bounded queued tasks, bounded active
// concurrency, capacity waiters and overflow spooling.
type GatewayUsageFinalizationQueue struct {
	clock  Clock
	logger Logger

	maxItems       int
	maxBytes       int
	maxConcurrency int

	mu                 sync.Mutex
	cond               *sync.Cond
	queued             []taskUnit
	queuedBytes        int
	active             int
	admissionWaitCount int
	droppedCount       int
	dropOnFull         bool
	waiters            int
	wg                 sync.WaitGroup
}

// gatewayUsageFinalizationDefaults mirror the Node runtime defaults
// (failure-finalization.service.ts: gatewayUsageFinalizationMaxConcurrency =
// runtimeConfig.concurrency.globalMax, i.e. JUHE_AI_CONCURRENCY_GLOBAL_MAX
// default 5000). maxItems stays 2048: it is the queued-tasks memory bound,
// not a throughput limit. Node 的 5000=globalMax 是全进程共享 governor 预算；
// Go 此处为收尾队列独占上限，实际 DB 写并发仍受 PG 池封顶。
const (
	defaultUsageFinalizationMaxItems       = 2048
	defaultUsageFinalizationMaxConcurrency = 5000
)

// NewGatewayUsageFinalizationQueue builds the queue.
func NewGatewayUsageFinalizationQueue(maxItems int, maxConcurrency int) *GatewayUsageFinalizationQueue {
	if maxItems <= 0 {
		maxItems = defaultUsageFinalizationMaxItems
	}
	if maxConcurrency <= 0 {
		maxConcurrency = defaultUsageFinalizationMaxConcurrency
	}
	queue := &GatewayUsageFinalizationQueue{
		clock:          SystemClock{},
		maxItems:       maxItems,
		maxBytes:       gatewayUsageFinalizationTaskMaxBytes,
		maxConcurrency: maxConcurrency,
	}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

// WithLogger injects the failure logger.
func (q *GatewayUsageFinalizationQueue) WithLogger(logger Logger) *GatewayUsageFinalizationQueue {
	q.logger = logger
	return q
}

// WithClock injects the clock.
func (q *GatewayUsageFinalizationQueue) WithClock(clock Clock) *GatewayUsageFinalizationQueue {
	q.clock = clock
	return q
}

// EnableDropOnFull 把满队行为从无限等待改为计数丢弃。生产 gateway 的
// usage 记录属运营统计（2026-11-09 用户裁决），不允许反压请求路径。
func (q *GatewayUsageFinalizationQueue) EnableDropOnFull() {
	q.mu.Lock()
	q.dropOnFull = true
	q.mu.Unlock()
}

// Dispatch mirrors dispatchGatewayUsageFinalization. bytes above the single
// task budget errors; a full queue drops (drop-on-full) or blocks until
// capacity frees up. The task runs asynchronously; its error is swallowed and
// logged (Node trackGatewayUsageFinalization contract) so delivery never
// blocks the gateway request path.
func (q *GatewayUsageFinalizationQueue) Dispatch(ctx Ctx, task func(Ctx) error, bytes int) error {
	if bytes < 0 {
		bytes = 0
	}
	if bytes > q.maxBytes {
		return errors.New("网关使用记录异步收尾任务超过单条容量上限")
	}
	q.mu.Lock()
	if !q.hasCapacityLocked(bytes) && q.dropOnFull {
		q.droppedCount++
		dropped := q.droppedCount
		q.mu.Unlock()
		if dropped <= 10 || dropped%100 == 0 {
			if q.logger != nil {
				q.logger.Warn("网关用量收尾队列已满，记录已丢弃（运营统计可丢）", map[string]any{
					"event":              "gateway_usage_finalization_dropped",
					"droppedCount":       dropped,
					"queuedCount":        len(q.queued),
					"queuedBytes":        q.queuedBytes,
					"admissionWaitCount": q.admissionWaitCount,
				})
			}
		}
		return nil
	}
	for !q.hasCapacityLocked(bytes) {
		q.admissionWaitCount++
		q.waiters++
		q.cond.Wait()
		q.waiters--
	}
	q.queued = append(q.queued, taskUnit{task: task, bytes: bytes})
	q.queuedBytes += bytes
	q.mu.Unlock()
	q.pump()
	return nil
}

// Track mirrors trackGatewayUsageFinalization: run one task with error
// capture.
func (q *GatewayUsageFinalizationQueue) Track(task func(Ctx) error) {
	q.track(task)
}

func (q *GatewayUsageFinalizationQueue) track(task func(Ctx) error) {
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				q.logRecovery(recovered, "gateway_usage_finalization_failed")
			}
		}()
		if err := task(context.Background()); err != nil {
			q.logError(err, "gateway_usage_finalization_failed")
		}
	}()
}

// logError reports a task failure through the logger port (Node default
// onError of trackGatewayUsageFinalization).
func (q *GatewayUsageFinalizationQueue) logError(err error, event string) {
	if q.logger != nil {
		q.logger.Warn("网关使用记录异步收尾失败", map[string]any{"event": event, "error": err})
	}
}

// logRecovery converts a recovered panic into the failure log path.
func (q *GatewayUsageFinalizationQueue) logRecovery(recovered any, event string) {
	if err, ok := recovered.(error); ok {
		q.logError(err, event)
		return
	}
	q.logError(errors.New(displayString(recovered)), event)
}

// pump mirrors pumpGatewayUsageFinalizations.
func (q *GatewayUsageFinalizationQueue) pump() {
	q.mu.Lock()
	for q.active < q.maxConcurrency && len(q.queued) > 0 {
		queued := q.queued[0]
		q.queued = q.queued[1:]
		q.queuedBytes -= queued.bytes
		if q.queuedBytes < 0 {
			q.queuedBytes = 0
		}
		q.active++
		q.cond.Broadcast()
		q.wg.Add(1)
		go func(unit taskUnit) {
			defer q.wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					q.logRecovery(recovered, "gateway_usage_finalization_failed")
				}
				q.mu.Lock()
				q.active--
				q.mu.Unlock()
				q.cond.Broadcast()
				q.pump()
			}()
			if err := unit.task(context.Background()); err != nil {
				q.logError(err, "gateway_usage_finalization_failed")
			}
		}(queued)
	}
	q.mu.Unlock()
}

func (q *GatewayUsageFinalizationQueue) hasCapacityLocked(bytes int) bool {
	return len(q.queued) < q.maxItems && q.queuedBytes+bytes <= q.maxBytes
}

// PendingCount mirrors getPendingGatewayFailureUsageFinalizationCount.
func (q *GatewayUsageFinalizationQueue) PendingCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queued) + q.active + q.waiters
}

// Runtime mirrors getGatewayUsageFinalizationRuntime.
func (q *GatewayUsageFinalizationQueue) Runtime() GatewayUsageFinalizationRuntime {
	q.mu.Lock()
	defer q.mu.Unlock()
	return GatewayUsageFinalizationRuntime{
		PendingCount:       len(q.queued) + q.active + q.waiters,
		QueuedCount:        len(q.queued),
		QueuedBytes:        q.queuedBytes,
		ActiveCount:        q.active,
		AdmissionWaitCount: q.admissionWaitCount,
		DroppedCount:       q.droppedCount,
		MaxItems:           q.maxItems,
		MaxBytes:           q.maxBytes,
		MaxConcurrency:     q.maxConcurrency,
	}
}

// WaitForIdle mirrors waitForGatewayFailureUsageFinalizationsIdle: poll
// every 5ms until pending drops to zero (with one immediate recheck) or the
// timeout elapses.
func (q *GatewayUsageFinalizationQueue) WaitForIdle(timeoutMs int) bool {
	if timeoutMs < 1 {
		timeoutMs = 1
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for {
		if q.PendingCount() == 0 {
			if q.PendingCount() == 0 {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}
