// Pipeline replaces the Node queue layer (public-api-log-queue.service.ts):
// a process-internal bounded channel instead of the Redis Stream / IPC / array
// queue hops. Overflow semantics mirror the Node local queue exactly — the
// newest record is dropped when the queue is full, the drop is counted and
// warned (first drop, then every 100th), a batch is at most 50 records, a
// failed batch write is retained at the head and retried after 1s, and
// shutdown drains up to 100 batches without losing already queued records.
package publicapilogs

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Pipeline defaults mirror publicApiLogQueue* constants.
const (
	DefaultQueueMaxItems    = 5000
	DefaultQueueMaxBytes    = 32 * 1024 * 1024
	DefaultFlushBatchSize   = 50
	DefaultRetryDelay       = time.Second
	DefaultShutdownMaxBatch = 100
	defaultDropWarnInterval = 100

	// estimateMaxNodes mirrors estimatePublicApiLogBytes' maxNodes cap.
	estimateMaxNodes = 20000
)

// BatchWriter is the persistence port; *Store implements it and tests inject
// mocks.
type BatchWriter interface {
	InsertBatch(ctx context.Context, inputs []Input) error
}

// Logger mirrors the operationlog slogLogger port: *slog.Logger satisfies it.
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// Runtime mirrors getPublicApiLogQueueRuntime.
type Runtime struct {
	QueueLength       int64 `json:"queueLength"`
	QueueBytes        int64 `json:"queueBytes"`
	DroppedCount      int64 `json:"droppedCount"`
	FlushFailureCount int64 `json:"flushFailureCount"`
}

// Config carries the injectable knobs; zero values fall back to the Node
// constants.
type Config struct {
	QueueMaxItems    int
	QueueMaxBytes    int64
	FlushBatchSize   int
	RetryDelay       time.Duration
	ShutdownMaxBatch int
	Now              func() time.Time
	NewQueueID       func() string
	Logger           Logger
}

// queuedLog mirrors QueuedPublicApiLog.
type queuedLog struct {
	input Input
	bytes int
}

// Pipeline is the single-consumer async batch writer. Enqueue is safe for
// concurrent use; a single background goroutine drains the queue.
type Pipeline struct {
	writer BatchWriter
	cfg    Config
	ch     chan queuedLog

	queueLen    atomic.Int64
	queueBytes  atomic.Int64
	dropped     atomic.Int64
	flushFailed atomic.Int64

	closing   atomic.Bool
	closeOnce sync.Once
	stopWait  chan struct{}
	done      chan struct{}
}

// NewPipeline builds the pipeline and starts the background flusher.
func NewPipeline(writer BatchWriter, cfg Config) *Pipeline {
	if cfg.QueueMaxItems <= 0 {
		cfg.QueueMaxItems = DefaultQueueMaxItems
	}
	if cfg.QueueMaxBytes <= 0 {
		cfg.QueueMaxBytes = DefaultQueueMaxBytes
	}
	if cfg.FlushBatchSize <= 0 {
		cfg.FlushBatchSize = DefaultFlushBatchSize
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = DefaultRetryDelay
	}
	if cfg.ShutdownMaxBatch <= 0 {
		cfg.ShutdownMaxBatch = DefaultShutdownMaxBatch
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewQueueID == nil {
		cfg.NewQueueID = func() string { return newQueueID(cfg.Now) }
	}
	p := &Pipeline{
		writer:   writer,
		cfg:      cfg,
		ch:       make(chan queuedLog, cfg.QueueMaxItems),
		stopWait: make(chan struct{}),
		done:     make(chan struct{}),
	}
	go p.run()
	return p
}

// Enqueue mirrors enqueuePublicApiLog: assign the stable queue id, estimate
// the payload size and drop the record when the queue is full. It never
// blocks the caller.
func (p *Pipeline) Enqueue(input Input) bool {
	if p == nil || p.writer == nil {
		return false
	}
	if input.ID == "" {
		input.ID = p.cfg.NewQueueID()
	}
	item := queuedLog{input: input, bytes: estimateInputBytes(input, DefaultQueueMaxBytes+1, estimateMaxNodes)}
	if int64(item.bytes) > p.cfg.QueueMaxBytes {
		p.recordDrop(item)
		return false
	}
	if p.closing.Load() {
		p.recordDrop(item)
		return false
	}
	if p.queueLen.Load() >= int64(p.cfg.QueueMaxItems) || p.queueBytes.Load()+int64(item.bytes) > p.cfg.QueueMaxBytes {
		p.recordDrop(item)
		return false
	}
	select {
	case p.ch <- item:
		p.queueLen.Add(1)
		p.queueBytes.Add(int64(item.bytes))
		return true
	default:
		p.recordDrop(item)
		return false
	}
}

// recordDrop mirrors the overflow warning cadence: the first drop and every
// 100th drop are logged.
func (p *Pipeline) recordDrop(item queuedLog) {
	count := p.dropped.Add(1)
	if count == 1 || count%defaultDropWarnInterval == 0 {
		p.logWarn("公开接口日志队列已满，丢弃日志记录",
			"event", "public_api_log_queue_overflow",
			"itemBytes", item.bytes,
			"droppedPublicApiLogCount", count,
			"traceId", item.input.TraceID,
			"path", item.input.Path)
	}
}

func (p *Pipeline) logWarn(msg string, args ...any) {
	if p.cfg.Logger != nil {
		p.cfg.Logger.Warn(msg, args...)
	}
}

// Runtime returns the queue counters.
func (p *Pipeline) Runtime() Runtime {
	return Runtime{
		QueueLength:       p.queueLen.Load(),
		QueueBytes:        p.queueBytes.Load(),
		DroppedCount:      p.dropped.Load(),
		FlushFailureCount: p.flushFailed.Load(),
	}
}

// Close mirrors flushPublicApiLogQueueForShutdown: stop accepting new
// records, drain up to ShutdownMaxBatch batches of FlushBatchSize (which covers
// the full default capacity) and return. A failing batch write ends the drain,
// and ctx bounds the wait so shutdown can never hang. The channel is never
// closed, so a concurrent Enqueue can never panic; a record that slips in
// after the drain finished stays lost-by-design (the approved in-process loss
// semantics), matching the best-effort window Node's own beforeExit flush has.
func (p *Pipeline) Close(ctx context.Context) {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.closing.Store(true)
		close(p.stopWait)
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-p.done:
	case <-ctx.Done():
	}
}

// Done exposes completion for tests.
func (p *Pipeline) Done() <-chan struct{} { return p.done }

// run is the flusher loop: accumulate a head batch (at most FlushBatchSize),
// write it, retry the retained batch after the delay on failure.
func (p *Pipeline) run() {
	defer close(p.done)
	ctx := context.Background()
	var batch []queuedLog
	var batchBytes int64

	topUp := func() {
		for len(batch) < p.cfg.FlushBatchSize {
			select {
			case item := <-p.ch:
				batch = append(batch, item)
				batchBytes += int64(item.bytes)
			default:
				return
			}
		}
	}

	for {
		topUp()
		if len(batch) == 0 {
			// Nothing available: block for the first record or shutdown.
			select {
			case item := <-p.ch:
				batch = append(batch, item)
				batchBytes += int64(item.bytes)
			case <-p.stopWait:
				p.drainShutdown(ctx, &batch, &batchBytes)
				return
			}
			topUp()
		}
		if p.writeBatch(ctx, batch, batchBytes) {
			batch = batch[:0]
			batchBytes = 0
			continue
		}
		// The failed batch stays at the head; wait out the retry delay. A
		// concurrent Close interrupts the wait and switches to the bounded
		// drain.
		timer := time.NewTimer(p.cfg.RetryDelay)
		select {
		case <-timer.C:
		case <-p.stopWait:
			timer.Stop()
			p.drainShutdown(ctx, &batch, &batchBytes)
			return
		}
	}
}

// drainShutdown mirrors flushPublicApiLogQueueBatches({drain:true,
// maxBatches:ShutdownMaxBatch}): keep writing batches until the queue is empty;
// the first failing batch ends the drain. Only called after Close.
func (p *Pipeline) drainShutdown(ctx context.Context, batch *[]queuedLog, batchBytes *int64) {
	for batches := 0; batches < p.cfg.ShutdownMaxBatch; batches++ {
		if len(*batch) == 0 {
			for len(*batch) < p.cfg.FlushBatchSize {
				select {
				case item := <-p.ch:
					*batch = append(*batch, item)
					*batchBytes += int64(item.bytes)
					continue
				default:
				}
				break
			}
			if len(*batch) == 0 {
				return
			}
		}
		if !p.writeBatch(ctx, *batch, *batchBytes) {
			return
		}
		*batch = nil
		*batchBytes = 0
	}
}

// writeBatch mirrors flushPublicApiLogQueueBatch: on success the records leave
// the queue counters; on failure they stay counted, the failure counter is
// bumped and a warning is logged. The caller never propagates the error: a
// failing log write must not fail the capture path.
func (p *Pipeline) writeBatch(ctx context.Context, batch []queuedLog, batchBytes int64) bool {
	inputs := make([]Input, len(batch))
	for i, item := range batch {
		inputs[i] = item.input
	}
	err := p.writer.InsertBatch(ctx, inputs)
	if err == nil {
		p.queueLen.Add(-int64(len(batch)))
		p.queueBytes.Add(-batchBytes)
		return true
	}
	p.flushFailed.Add(1)
	first := batch[0].input
	p.logWarn("公开接口日志批量写入失败，已保留批次等待重试",
		"event", "public_api_log_batch_write_failed",
		"batchSize", len(batch),
		"batchBytes", batchBytes,
		"pendingCount", p.queueLen.Load(),
		"pendingBytes", p.queueBytes.Load(),
		"flushFailureCount", p.flushFailed.Load(),
		"method", first.Method,
		"path", first.Path,
		"statusCode", first.StatusCode,
		"traceId", first.TraceID)
	return false
}
