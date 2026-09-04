package usagewriter

import (
	"context"
	"errors"
	"sync"
	"time"
)

// 直接异步写 + 分片 + 失败终态，对照 backend/src/modules/gateway/usage/
// record-queue.service.ts 的本地队列路径。Redis Stream 队列与 ingest-worker
// IPC 分派按总计划在 Go 侧消灭：本 writer 只保留进程内一条路径——
// 有界 pending 队列 + 单后台 goroutine 批量落库。

// 队列与批量边界默认值（record-queue.service.ts / runtime.ts 默认）。
const (
	// DefaultQueueMaxItems mirrors usageRecordQueueMaxItems (10000).
	DefaultQueueMaxItems = 10_000
	// DefaultQueueMaxBytes mirrors usageRecordQueueMaxMb * 1024 * 1024 (64MB).
	DefaultQueueMaxBytes = 64 * 1024 * 1024
	// DefaultBatchSize mirrors usageRecordBatchSize (1000).
	DefaultBatchSize = 1_000
	// DefaultFlushBatchMaxBytes mirrors usageRecordFlushBatchMaxMb * 1024 * 1024 (8MB).
	DefaultFlushBatchMaxBytes = 8 * 1024 * 1024
	// DefaultFlushIntervalMs mirrors usageRecordFlushIntervalMs (500).
	DefaultFlushIntervalMs = 500
	// DefaultRetryDelayMs mirrors fixedRetryPolicy('usage_record_queue_flush', 1000).
	DefaultRetryDelayMs = 1_000
	// DefaultShutdownFlushMaxBatches mirrors usageRecordShutdownFlushMaxBatches (100).
	DefaultShutdownFlushMaxBatches = 100
	// slowUsageRecordFlushMs mirrors the slow-flush threshold.
	slowUsageRecordFlushMs = 500
	// queueSaturationWarnRatio 与最小间隔 mirror maybeLogUsageRecordQueueSaturation.
	queueSaturationWarnRatio = 0.8
	queueSaturationWarnGapMs = 60_000
	// droppedLogSampleLimit mirrors `droppedCount > 10 && % 100 !== 0`.
	droppedLogSampleLimit = 10
)

// Config carries the writer knobs; zero values fall back to the Node
// defaults above.
type Config struct {
	QueueMaxItems           int
	QueueMaxBytes           int
	BatchSize               int
	FlushBatchMaxBytes      int
	FlushIntervalMs         int
	RetryDelayMs            int
	ShutdownFlushMaxBatches int
	// MaxWriteAttempts caps the retries of one pending batch before it moves
	// to the dead-letter terminal state. 0 (the default) retries forever,
	// which is the Node local-queue behavior; the cap exists because the Go
	// side has no Redis Stream pending backlog to fall back on.
	MaxWriteAttempts int
	// ShardCount mirrors runtimeConfig.usageShardCount.
	ShardCount int
	// ShardRoot is the SQLite usage-shard root (planning only; the store
	// owns the files).
	ShardRoot string
	// FreezePricing enables the enqueue-time pricing freeze (the Node Redis
	// Stream path freezes before enqueue; the Go single path freezes here
	// when enabled).
	FreezePricing bool
	// CatalogSnapshot mirrors databaseDriver !== 'postgres' for
	// usageRecordPricingSnapshotForWrite's catalog attempt.
	CatalogSnapshot bool
}

// OverflowSpool ports the performance-mode disk compensation
// (persistUsageRecordForQueueOverflow): consulted when the in-process queue
// is full. nil disables it (records drop with the overflow counters, the
// Node non-performance behavior).
type OverflowSpool interface {
	Persist(ctx Ctx, input UsageRecordInput) error
}

// Logger ports the structured writer logs (record-queue.service.ts pino
// calls), including the byte-identical Chinese copy.
type Logger interface {
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

type queuedRecord struct {
	input    UsageRecordInput
	bytes    int
	enqueued time.Time
}

// Runtime mirrors getUsageRecordQueueRuntime (writer-pool counters dropped:
// the child-process pool is eliminated on the Go side).
type Runtime struct {
	QueueLength                  int    `json:"queueLength"`
	QueueBytes                   int    `json:"queueBytes"`
	OldestCreatedAt              string `json:"oldestCreatedAt,omitempty"`
	DroppedCount                 int    `json:"droppedCount"`
	RetainedOverflowWarningCount int    `json:"retainedOverflowWarningCount"`
	DroppedOverflowCount         int    `json:"droppedOverflowCount"`
	DroppedOversizeCount         int    `json:"droppedOversizeCount"`
	DroppedDispatchCount         int    `json:"droppedDispatchCount"`
	DeadLetterCount              int    `json:"deadLetterCount"`
	FlushFailureCount            int    `json:"flushFailureCount"`
	OldestQueuedMs               int64  `json:"oldestQueuedMs"`
	LastFlushMs                  int64  `json:"lastFlushMs"`
	MaxFlushMs                   int64  `json:"maxFlushMs"`
	SlowFlushCount               int    `json:"slowFlushCount"`
	LastSlowFlushAt              string `json:"lastSlowFlushAt,omitempty"`
	HandledRecords               int    `json:"handledRecords"`
	WrittenRecords               int    `json:"writtenRecords"`
}

// Writer is the direct async usage-record writer: bounded in-process queue,
// single background flush goroutine, shard-routed batch writes through the
// ShardStore port, retry with the fixed policy and a dead-letter terminal
// state for exhausted batches.
type Writer struct {
	config    Config
	clock     Clock
	store     ShardStore
	idFactory UsageRecordIDFactory
	catalog   CatalogPricing
	spool     OverflowSpool
	logger    Logger
	// RetryWait allows tests to skip the fixed retry backoff; nil sleeps the
	// real delay, abortable by Close.
	RetryWait func(ctx Ctx, delay time.Duration) bool

	mu           sync.Mutex
	pending      []queuedRecord
	pendingBytes int
	started      bool
	stopped      bool
	notify       chan struct{}
	stop         chan struct{}
	done         chan struct{}
	stopOnce     sync.Once

	droppedDispatchCount         int
	droppedOverflowCount         int
	droppedOversizeCount         int
	retainedOverflowWarningCount int
	deadLetterCount              int
	deadLetters                  []UsageRecordInput
	flushFailureCount            int
	handledRecords               int
	writtenRecords               int
	lastFlushMs                  int64
	maxFlushMs                   int64
	slowFlushCount               int
	lastSlowFlushAt              string
	lastQueueSaturationWarningAt time.Time
}

// Option configures optional writer ports.
type Option func(*Writer)

// WithLogger injects the logger.
func WithLogger(logger Logger) Option { return func(w *Writer) { w.logger = logger } }

// WithCatalog injects the pricing freeze catalog port.
func WithCatalog(catalog CatalogPricing) Option { return func(w *Writer) { w.catalog = catalog } }

// WithOverflowSpool injects the overflow compensation spool.
func WithOverflowSpool(spool OverflowSpool) Option { return func(w *Writer) { w.spool = spool } }

// WithIDFactory injects the usage-record id factory (defaults to
// generateUsageRecordId over randomUUID entropy).
func WithIDFactory(factory UsageRecordIDFactory) Option {
	return func(w *Writer) { w.idFactory = factory }
}

// NewWriter builds the writer; Start launches the flush loop.
func NewWriter(config Config, store ShardStore, clock Clock, options ...Option) *Writer {
	if config.QueueMaxItems <= 0 {
		config.QueueMaxItems = DefaultQueueMaxItems
	}
	if config.QueueMaxBytes <= 0 {
		config.QueueMaxBytes = DefaultQueueMaxBytes
	}
	if config.BatchSize <= 0 {
		config.BatchSize = DefaultBatchSize
	}
	if config.FlushBatchMaxBytes <= 0 {
		config.FlushBatchMaxBytes = DefaultFlushBatchMaxBytes
	}
	if config.FlushIntervalMs <= 0 {
		config.FlushIntervalMs = DefaultFlushIntervalMs
	}
	if config.RetryDelayMs <= 0 {
		config.RetryDelayMs = DefaultRetryDelayMs
	}
	if config.ShutdownFlushMaxBatches <= 0 {
		config.ShutdownFlushMaxBatches = DefaultShutdownFlushMaxBatches
	}
	config.ShardCount = ShardCount(config.ShardCount)
	if clock == nil {
		clock = SystemClock{}
	}
	writer := &Writer{
		config: config,
		clock:  clock,
		store:  store,
		notify: make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	for _, option := range options {
		option(writer)
	}
	if writer.idFactory == nil {
		shardCount := config.ShardCount
		idClock := clock
		writer.idFactory = IDFactoryFunc(func(createdAt string) string {
			id, err := GenerateUsageRecordID(idClock, createdAt, NewRandomUUID(), shardCount)
			if err != nil {
				// The instant was already validated by normalize; keep the
				// record routable with a clock-derived bucket key.
				return "usage_" + BucketDateKeyFromClock(idClock) + "_s" + FormatShardID(StableShardID(createdAt, shardCount)) + "_" + itoa64(idClock.Now().UnixMilli()) + "_" + SanitizeShardEntropy(createdAt)
			}
			return id
		})
	}
	return writer
}

// Enqueue mirrors enqueueUsageRecord (the UsageRecorder port contract):
// normalize, freeze pricing facts at the enqueue instant, then admit into
// the bounded in-process queue. Admission failures are terminal for the
// record: oversize/overflow records are spooled (performance mode) or
// dropped with the Node counters and sampled log copy.
func (w *Writer) Enqueue(ctx Ctx, input UsageRecordInput) error {
	normalized, err := NormalizeUsageRecordInput(input, w.clock, w.idFactory)
	if err != nil {
		return err
	}
	if w.config.FreezePricing {
		normalized = FreezeUsageRecordPricingFacts(ctx, normalized, w.catalog, w.config.CatalogSnapshot)
	}
	bytes := EstimateUsageRecordBytes(normalized, w.config.QueueMaxBytes)

	var saturation float64
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return errors.New("usage writer 已停止，拒绝写入使用记录")
	}
	if bytes > w.config.QueueMaxBytes {
		// Oversize records never enter the queue (the Node local-queue
		// terminal drop; the spool would reject them too).
		w.recordLocalDropLocked(queuedRecord{input: normalized, bytes: bytes}, "oversize")
		w.mu.Unlock()
		return nil
	}
	if len(w.pending) >= w.config.QueueMaxItems || w.pendingBytes+bytes > w.config.QueueMaxBytes {
		w.mu.Unlock()
		// 溢出补偿（performance 语义）：先落 spool；spool 成功则记录不丢，
		// spool 未配置或失败时按 Node 本地队列语义丢弃并计数。
		if !w.spoolOverflow(ctx, normalized) {
			w.mu.Lock()
			w.recordLocalDropLocked(queuedRecord{input: normalized, bytes: bytes}, "overflow")
			w.mu.Unlock()
		}
		return nil
	}
	w.pending = append(w.pending, queuedRecord{
		input:    normalized,
		bytes:    bytes,
		enqueued: w.clock.Now(),
	})
	w.pendingBytes += bytes
	w.handledRecords++
	saturation = w.queueSaturationRatioLocked()
	saturationWarnDue := saturation >= queueSaturationWarnRatio &&
		w.clock.Now().Sub(w.lastQueueSaturationWarningAt) >= queueSaturationWarnGapMs*time.Millisecond
	if saturationWarnDue {
		w.lastQueueSaturationWarningAt = w.clock.Now()
	}
	pendingCount := len(w.pending)
	batchReady := pendingCount >= w.config.BatchSize
	w.mu.Unlock()

	if saturationWarnDue && w.logger != nil {
		w.logger.Warn("数据库写队列达到 80% 容量；IO 任务将继续排队，DB worker 保持受控并发", map[string]any{
			"event":           "usage_record_db_write_queue_saturated",
			"saturationRatio": saturation,
			"pendingCount":    pendingCount,
			"maxItems":        w.config.QueueMaxItems,
			"maxBytes":        w.config.QueueMaxBytes,
		})
	}
	// scheduleUsageRecordFlush contract: a full batch flushes immediately,
	// anything else waits for the flush-interval ticker (the notify channel
	// is the Node 0-delay timer; the ticker is the interval timer).
	if batchReady {
		w.signal()
	}
	return nil
}

// spoolOverflow mirrors persistUsageRecordForQueueOverflow: the
// performance-mode compensation for records that could not be admitted.
// Returns true when the record was persisted. A failed spool drops the
// record with the dispatch failure counter, mirroring
// recordUsageRecordDispatchFailure.
func (w *Writer) spoolOverflow(ctx Ctx, input UsageRecordInput) bool {
	if w.spool == nil {
		return false
	}
	if err := w.spool.Persist(ctx, input); err != nil {
		w.mu.Lock()
		w.droppedDispatchCount++
		count := w.droppedDispatchCount
		w.mu.Unlock()
		if w.logger != nil {
			w.logger.Warn("使用记录投递后台 worker 失败，已跳过投递", map[string]any{
				"event":                "usage_record_queue_dispatch_failed",
				"usageRecordId":        input.ID,
				"traceId":              input.TraceID,
				"trafficSource":        input.TrafficSource,
				"systemAccountId":      input.SystemAccountID,
				"endpoint":             input.Endpoint,
				"statusCode":           input.StatusCode,
				"errorCode":            input.ErrorCode,
				"droppedDispatchCount": count,
				"error":                err.Error(),
			})
		}
	}
	return true
}

// recordLocalDropLocked mirrors recordUsageRecordLocalDrop (caller holds mu).
func (w *Writer) recordLocalDropLocked(item queuedRecord, reason string) {
	if reason == "overflow" {
		w.droppedOverflowCount++
		w.retainedOverflowWarningCount++
	} else {
		w.droppedOversizeCount++
	}
	droppedCount := w.droppedDispatchCount + w.droppedOverflowCount + w.droppedOversizeCount
	if droppedCount > droppedLogSampleLimit && droppedCount%100 != 0 {
		return
	}
	if w.logger == nil {
		return
	}
	w.logger.Warn("使用记录队列达到保护上限，已丢弃新记录", map[string]any{
		"event":                "usage_record_queue_dropped",
		"reason":               reason,
		"usageRecordId":        item.input.ID,
		"traceId":              item.input.TraceID,
		"trafficSource":        item.input.TrafficSource,
		"systemAccountId":      item.input.SystemAccountID,
		"endpoint":             item.input.Endpoint,
		"statusCode":           item.input.StatusCode,
		"bytes":                item.bytes,
		"pendingCount":         len(w.pending),
		"pendingBytes":         w.pendingBytes,
		"droppedOverflowCount": w.droppedOverflowCount,
		"droppedOversizeCount": w.droppedOversizeCount,
	})
}

func (w *Writer) queueSaturationRatioLocked() float64 {
	itemRatio := 0.0
	if w.config.QueueMaxItems > 0 {
		itemRatio = float64(len(w.pending)) / float64(w.config.QueueMaxItems)
	}
	byteRatio := 0.0
	if w.config.QueueMaxBytes > 0 {
		byteRatio = float64(w.pendingBytes) / float64(w.config.QueueMaxBytes)
	}
	if itemRatio > byteRatio {
		return itemRatio
	}
	return byteRatio
}

func (w *Writer) signal() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// Start launches the background flush loop once.
func (w *Writer) Start() {
	w.mu.Lock()
	if w.started || w.stopped {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()
	go w.run()
}

func (w *Writer) run() {
	defer close(w.done)
	ticker := time.NewTicker(time.Duration(w.config.FlushIntervalMs) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-w.notify:
			w.flushCycle()
		case <-ticker.C:
			w.flushCycle()
		case <-w.stop:
			w.flushShutdown()
			return
		}
	}
}

// flushCycle mirrors flushUsageRecordQueue: flush batches until empty or
// failure; on failure keep the batch and wait the fixed retry delay (or the
// injected RetryWait).
func (w *Writer) flushCycle() {
	for {
		w.mu.Lock()
		if len(w.pending) == 0 {
			w.mu.Unlock()
			return
		}
		batch, batchBytes := w.peekBatchLocked()
		w.mu.Unlock()
		if len(batch) == 0 {
			return
		}

		startedAt := time.Now()
		inserted, err := w.writeBatch(batch)
		w.recordFlushDuration(time.Since(startedAt))
		if err == nil {
			w.removeBatch(batch, batchBytes, inserted)
			continue
		}
		if w.handleFlushFailure(batch, err) {
			// Retry budget exhausted: the batch moved to the dead-letter
			// terminal state; flushing continues with the next batch.
			w.deadLetterBatch(batch)
			continue
		}
		if !w.waitRetry() {
			return
		}
	}
}

// handleFlushFailure books one failed batch: counters and the log copy.
// Returns true when the batch exhausted its retry budget (the caller moves
// it to the dead-letter state), false to keep it queued for the fixed retry.
// The budget tracks the writer's consecutive flush failures (the Node
// flushFailureCount semantics: reset on success), which for the head batch
// equals its retry count.
func (w *Writer) handleFlushFailure(batch []queuedRecord, err error) bool {
	w.mu.Lock()
	w.flushFailureCount++
	failureCount := w.flushFailureCount
	attemptLimit := w.config.MaxWriteAttempts
	exhausted := attemptLimit > 0 && failureCount >= attemptLimit
	w.mu.Unlock()

	if w.logger != nil {
		if exhausted {
			w.logger.Error("使用记录队列写入重试耗尽，已转入死信终态", map[string]any{
				"event":             "usage_record_queue_dead_letter",
				"batchSize":         len(batch),
				"attempts":          attemptLimit,
				"flushFailureCount": failureCount,
				"error":             err.Error(),
			})
		} else {
			runtime := w.Runtime()
			w.logger.Error("使用记录队列写入失败，已保留记录等待重试", map[string]any{
				"event":             "usage_record_queue_flush_failed",
				"batchSize":         len(batch),
				"pendingCount":      runtime.QueueLength,
				"pendingBytes":      runtime.QueueBytes,
				"flushFailureCount": failureCount,
				"error":             err.Error(),
			})
		}
	}
	return exhausted
}

// writeBatch mirrors createUsageRecordsBatchAsync: build the write plan for
// the batch inputs and hand it to the store.
func (w *Writer) writeBatch(batch []queuedRecord) (int, error) {
	inputs := make([]UsageRecordInput, 0, len(batch))
	for _, item := range batch {
		inputs = append(inputs, item.input)
	}
	ctx := context.Background()
	plan, err := BuildWritePlan(ctx, inputs, WritePlanOptions{
		Postgres:               false,
		CatalogSnapshotEnabled: w.config.CatalogSnapshot,
		Catalog:                w.catalog,
		ShardCount:             w.config.ShardCount,
		ShardRoot:              w.config.ShardRoot,
	}, w.clock)
	if err != nil {
		return 0, err
	}
	return w.store.WriteBatch(ctx, plan)
}

func (w *Writer) recordFlushDuration(elapsed time.Duration) {
	rounded := elapsed.Milliseconds()
	if rounded < 0 {
		rounded = 0
	}
	w.mu.Lock()
	w.lastFlushMs = rounded
	if rounded > w.maxFlushMs {
		w.maxFlushMs = rounded
	}
	if rounded >= slowUsageRecordFlushMs {
		w.slowFlushCount++
		w.lastSlowFlushAt = w.clock.Now().UTC().Format(timeRFC3339Millis)
	}
	w.mu.Unlock()
}

// peekBatchLocked mirrors peekUsageRecordFlushBatch (caller holds mu): the
// head-limited batch honoring the byte budget, always admitting the first
// record.
func (w *Writer) peekBatchLocked() ([]queuedRecord, int) {
	limit := len(w.pending)
	if w.config.BatchSize < limit {
		limit = w.config.BatchSize
	}
	count := 0
	bytes := 0
	for count < limit && count < len(w.pending) {
		next := w.pending[count]
		if count > 0 && bytes+next.bytes > w.config.FlushBatchMaxBytes {
			break
		}
		bytes += next.bytes
		count++
	}
	batch := make([]queuedRecord, count)
	copy(batch, w.pending[:count])
	return batch, bytes
}

// removeBatch mirrors removeUsageRecordFlushBatch after a successful write.
func (w *Writer) removeBatch(batch []queuedRecord, batchBytes int, inserted int) {
	w.mu.Lock()
	w.pending = w.pending[len(batch):]
	w.pendingBytes -= batchBytes
	if w.pendingBytes < 0 {
		w.pendingBytes = 0
	}
	w.writtenRecords += inserted
	w.flushFailureCount = 0
	w.mu.Unlock()
}

// deadLetterBatch moves an exhausted batch into the terminal dead-letter
// state: counted, bounded retention of the newest records for diagnostics.
// The consecutive-failure budget resets for the next batch.
func (w *Writer) deadLetterBatch(batch []queuedRecord) {
	const deadLetterRetention = 100
	w.mu.Lock()
	for _, item := range batch {
		w.deadLetterCount++
		w.deadLetters = append(w.deadLetters, item.input)
	}
	if len(w.deadLetters) > deadLetterRetention {
		w.deadLetters = w.deadLetters[len(w.deadLetters)-deadLetterRetention:]
	}
	w.pending = w.pending[len(batch):]
	w.flushFailureCount = 0
	w.mu.Unlock()
}

// waitRetry mirrors scheduleUsageRecordFlush(retryDelayMs): wait the fixed
// policy delay, abortable by Close. Returns false when the writer stopped.
func (w *Writer) waitRetry() bool {
	delay := time.Duration(w.config.RetryDelayMs) * time.Millisecond
	if w.RetryWait != nil {
		return w.RetryWait(context.Background(), delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-w.stop:
		return false
	}
}

// flushShutdown mirrors flushUsageRecordQueueForShutdown: drain with
// retryOnFailure=false and the shutdown batch cap. Called from the flush
// loop after the stop signal; a failing batch stops the drain (Node
// retryOnFailure=false keeps the batch queued and the process exits).
func (w *Writer) flushShutdown() {
	for batch := 0; batch < w.config.ShutdownFlushMaxBatches; batch++ {
		w.mu.Lock()
		if len(w.pending) == 0 {
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		before := w.PendingCount()
		w.flushOnceShutdown()
		if w.PendingCount() >= before {
			return
		}
	}
}

// flushOnceShutdown flushes exactly one batch without retry.
func (w *Writer) flushOnceShutdown() {
	w.mu.Lock()
	if len(w.pending) == 0 {
		w.mu.Unlock()
		return
	}
	batch, batchBytes := w.peekBatchLocked()
	w.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	startedAt := time.Now()
	inserted, err := w.writeBatch(batch)
	w.recordFlushDuration(time.Since(startedAt))
	if err != nil {
		w.mu.Lock()
		w.flushFailureCount++
		w.mu.Unlock()
		if w.logger != nil {
			runtime := w.Runtime()
			w.logger.Error("使用记录队列写入失败，已保留记录等待重试", map[string]any{
				"event":        "usage_record_queue_flush_failed",
				"batchSize":    len(batch),
				"pendingCount": runtime.QueueLength,
				"pendingBytes": runtime.QueueBytes,
				"error":        err.Error(),
			})
		}
		return
	}
	w.removeBatch(batch, batchBytes, inserted)
}

// Drain synchronously flushes everything queued (flushAllUsageRecordQueue:
// drain=true, retryOnFailure=false) without stopping the writer.
func (w *Writer) Drain() {
	for batch := 0; batch < w.config.ShutdownFlushMaxBatches; batch++ {
		before := w.PendingCount()
		if before == 0 {
			return
		}
		w.flushOnceShutdown()
		if w.PendingCount() >= before {
			return
		}
	}
}

// Close stops the writer, drains the queue like the Node shutdown hooks
// (flushUsageRecordQueueForShutdown + closeUsageRecordWriterPool) and waits
// for the flush loop to finish or the context to expire.
func (w *Writer) Close(ctx Ctx) {
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	w.stopOnce.Do(func() { close(w.stop) })
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
	case <-ctx.Done():
	}
}

// Runtime mirrors getUsageRecordQueueRuntime.
func (w *Writer) Runtime() Runtime {
	w.mu.Lock()
	defer w.mu.Unlock()
	oldest := ""
	oldestQueuedMs := int64(0)
	if len(w.pending) > 0 {
		oldest = w.pending[0].input.CreatedAt
		for _, item := range w.pending {
			if item.input.CreatedAt < oldest {
				oldest = item.input.CreatedAt
			}
		}
		oldestQueuedMs = w.clock.Now().Sub(w.pending[0].enqueued).Milliseconds()
		if oldestQueuedMs < 0 {
			oldestQueuedMs = 0
		}
	}
	return Runtime{
		QueueLength:                  len(w.pending),
		QueueBytes:                   w.pendingBytes,
		OldestCreatedAt:              oldest,
		DroppedCount:                 w.droppedDispatchCount + w.droppedOverflowCount + w.droppedOversizeCount + w.deadLetterCount,
		RetainedOverflowWarningCount: w.retainedOverflowWarningCount,
		DroppedOverflowCount:         w.droppedOverflowCount,
		DroppedOversizeCount:         w.droppedOversizeCount,
		DroppedDispatchCount:         w.droppedDispatchCount,
		DeadLetterCount:              w.deadLetterCount,
		FlushFailureCount:            w.flushFailureCount,
		OldestQueuedMs:               oldestQueuedMs,
		LastFlushMs:                  w.lastFlushMs,
		MaxFlushMs:                   w.maxFlushMs,
		SlowFlushCount:               w.slowFlushCount,
		LastSlowFlushAt:              w.lastSlowFlushAt,
		HandledRecords:               w.handledRecords,
		WrittenRecords:               w.writtenRecords,
	}
}

// PendingCount mirrors pendingUsageRecordCount.
func (w *Writer) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

// DeadLetters returns a copy of the retained dead-letter records (newest
// last), the terminal-state diagnostics for exhausted batches.
func (w *Writer) DeadLetters() []UsageRecordInput {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]UsageRecordInput, len(w.deadLetters))
	copy(out, w.deadLetters)
	return out
}
