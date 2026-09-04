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
	mu      sync.Mutex
	clock   Clock
	idFactory UsageRecordIDFactory
	records []UsageRecordInput
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
// queue + overflow spool + the UsageRecorder port. It mirrors
// dispatchUsageRecord (records.ts) composed with
// dispatchGatewayUsageFinalization (failure-finalization.service.ts).
type FinalizationDispatch struct {
	queue    *GatewayUsageFinalizationQueue
	recorder UsageRecorder
	// overflow ports persistUsageRecordForQueueOverflow; only consulted in
	// performance mode (OverflowEnabled).
	overflow        DispatchOverflowSpool
	OverflowEnabled bool
	clock           Clock
	logger          Logger
}

// NewFinalizationDispatch wires the pipeline over the recorder port. maxItems /
// maxConcurrency <= 0 fall back to the Node defaults
// (usageFinalizationMaxItems 2048, concurrency.globalMax 32).
func NewFinalizationDispatch(recorder UsageRecorder, overflow DispatchOverflowSpool, maxItems int, maxConcurrency int) *FinalizationDispatch {
	queue := NewGatewayUsageFinalizationQueue(maxItems, maxConcurrency)
	return &FinalizationDispatch{
		queue:    queue,
		recorder: recorder,
		overflow: overflow,
		clock:    SystemClock{},
	}
}

// DispatchUsageRecord mirrors dispatchUsageRecord: admit
// enqueueUsageRecord(input) with the byte budget, spooling on overflow when
// the performance-mode overflow factory is configured.
func (d *FinalizationDispatch) DispatchUsageRecord(ctx Ctx, input UsageRecordInput, bytes int) error {
	task := func(ctx Ctx) error {
		return d.recorder.EnqueueUsageRecord(ctx, input)
	}
	var overflowFactory func(Ctx) error
	if d.OverflowEnabled && d.overflow != nil {
		overflowFactory = func(ctx Ctx) error {
			return d.overflow.PersistOverflow(ctx, input)
		}
	}
	return d.queue.Dispatch(ctx, task, bytes, overflowFactory)
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
	PendingCount      int
	QueuedCount       int
	QueuedBytes       int
	ActiveCount       int
	DroppedCount      int
	AdmissionWaitCount int
	OverflowSpoolCount int
	MaxItems          int
	MaxBytes          int
	MaxConcurrency    int
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

	mu                sync.Mutex
	cond              *sync.Cond
	queued            []taskUnit
	queuedBytes       int
	active            int
	admissionWaitCount int
	overflowSpoolCount int
	waiters           int
	wg                sync.WaitGroup
}

// gatewayUsageFinalizationDefaults mirror the Node runtime defaults.
const (
	defaultUsageFinalizationMaxItems       = 2048
	defaultUsageFinalizationMaxConcurrency = 32
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

// Dispatch mirrors dispatchGatewayUsageFinalization. bytes above the single
// task budget errors; a full queue with an overflow factory spools instead
// of waiting; otherwise admission blocks until capacity frees up. The task
// runs asynchronously; its error is swallowed and logged (Node
// trackGatewayUsageFinalization contract) so delivery never blocks the
// gateway request path.
func (q *GatewayUsageFinalizationQueue) Dispatch(ctx Ctx, task func(Ctx) error, bytes int, overflowFactory func(Ctx) error) error {
	if bytes < 0 {
		bytes = 0
	}
	if bytes > q.maxBytes {
		return errors.New("网关使用记录异步收尾任务超过单条容量上限")
	}
	q.mu.Lock()
	if !q.hasCapacityLocked(bytes) && overflowFactory != nil {
		q.overflowSpoolCount++
		q.mu.Unlock()
		q.track(overflowFactory)
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
		DroppedCount:       0,
		AdmissionWaitCount: q.admissionWaitCount,
		OverflowSpoolCount: q.overflowSpoolCount,
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
