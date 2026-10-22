// Producer is the in-process F3 audit write path (去跨进程战役第四刀). It
// replaces the retired loopback HMAC input server as the chain audit sink and
// mirrors the operationlog.Producer contract: fire-and-forget capture that
// never fails the gateway request. Per record it renews the shared owner
// lease, persists through the store (validation + normalization live inside
// store.Persist exactly as for the retired HTTP writes) and appends the
// hot-search mirror for non-ignored writes.
//
// Concurrency contract (2026-09-18 hardening): records flow through a bounded
// queue drained by a fixed worker pool, so a slow store cannot pile up one
// goroutine (and its captured payload) per audit record on the request path.
// A full queue drops the record with a warn carrying the running drop total —
// capture stays fire-and-forget and never blocks the business request.
//
// Byte budget (W14, 2026-10-22): a single AuditLogInput can carry multi-MB
// request/response bodies, so the item count cap alone allowed a theoretical
// tens-of-GB residency under a slow store. Queue admission now also accounts
// estimated payload bytes against producerQueueMaxBytes; over-budget and
// single-oversize captures drop with the same fire-and-forget semantics.
// Depth/bytes gauges (QueueDepth/QueueBytes) expose the residency for
// scraping.
package auditlog

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// producerQueueCapacity bounds the number of in-flight audit inputs; the
	// store is the bottleneck (SQLite single-writer / pooled PG), so the queue
	// absorbs bursts instead of unbounded goroutine growth.
	producerQueueCapacity = 4096
	// producerWorkers caps concurrent store writers; renewal + persist +
	// hot-search run sequentially per record inside one worker.
	producerWorkers = 4

	// Estimation bases: small fields (ids, statuses, timestamps, small strings)
	// roll into fixed per-record/per-attempt envelopes; the dominant payload
	// bodies and long-tail error text count exactly.
	producerRecordBaseBytes  = 512
	producerAttemptBaseBytes = 256
	producerPayloadEnvelope  = 128
)

// producerQueueMaxBytes bounds the estimated byte residency of the audit
// pipeline (queued + in-flight persistence; bodies dominate, see
// estimateAuditInputBytes). 256MB keeps the queue a buffer, not a retention
// surface, even with full-capture bodies. Var only so package tests can pin a
// small budget; production must keep the default.
var producerQueueMaxBytes = int64(256) * 1024 * 1024

// queuedAudit pairs the input with its estimated byte size so the worker can
// release the budget exactly at dequeue time.
type queuedAudit struct {
	input AuditLogInput
	bytes int
}

type Producer struct {
	store Store
	lease LeaseSource
	cfg   Config
	log   producerLogger

	queue      chan queuedAudit
	start      sync.Once
	dropped    atomic.Int64
	queueBytes atomic.Int64
	// persistDuration is the optional push hook for the per-record
	// persistence latency histogram (result "ok" | "error"); nil keeps the
	// producer silent.
	persistDuration func(result string, durationMS int64)
}

type producerLogger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// LeaseSource exposes the currently held owner lease. The LeaseKeeper
// implements it; producers read the lease per record so every write fences
// with the live fence token (reacquire rotates the token when the supervisor
// revives a lost keeper — a frozen snapshot would fence every subsequent
// write until a process restart).
type LeaseSource interface {
	Lease() OwnerLease
}

// fixedLeaseSource pins one lease value for callers without a keeper (tests,
// static compositions).
type fixedLeaseSource OwnerLease

func (s fixedLeaseSource) Lease() OwnerLease { return OwnerLease(s) }

// StaticLease pins one lease value behind the LeaseSource interface for
// callers without a keeper (tests, static compositions).
//
// 预留导出 API：当前全仓无生产调用方，仅供静态组合/测试场景使用。
func StaticLease(lease OwnerLease) LeaseSource { return fixedLeaseSource(lease) }

func (p *Producer) warn(msg string, args ...any) {
	if p.log != nil {
		p.log.Warn(msg, args...)
	}
}

// NewProducer binds the producer to the shared lease source (the process-wide
// LeaseKeeper owned by main, or fixedLeaseSource for static compositions)
// and the persistence config. The renewal lifecycle stays with the keeper;
// the producer only extends the same lease per record, mirroring the
// operationlog.Producer contract.
func NewProducer(store Store, lease LeaseSource, cfg Config, log producerLogger) *Producer {
	return &Producer{
		store: store,
		lease: lease,
		cfg:   cfg,
		log:   log,
		queue: make(chan queuedAudit, producerQueueCapacity),
	}
}

// SetPersistDurationObserver registers the per-record persistence latency
// hook (result "ok" | "error"). Startup wiring only; nil (the zero state)
// keeps the producer silent.
func (p *Producer) SetPersistDurationObserver(observer func(result string, durationMS int64)) {
	if p == nil {
		return
	}
	p.persistDuration = observer
}

// estimateAuditInputBytes approximates the queue residency of one input
// without serializing it: payload bodies count exactly (they dominate), long
// error/URL/model text counts exactly, and the remaining small fields roll
// into fixed per-record/per-attempt envelopes. The estimate mirrors the
// publicapilogs queue-budget accounting intent (bounded admission, not
// retention).
func estimateAuditInputBytes(input AuditLogInput) int {
	total := producerRecordBaseBytes
	total += len(input.ErrorMessage) + len(input.ErrorCode) + len(input.ErrorPhase)
	total += len(input.TraceID) + len(input.UserAgent) + len(input.Path) + len(input.QueryString)
	for index := range input.Attempts {
		attempt := &input.Attempts[index]
		total += producerAttemptBaseBytes
		total += len(attempt.ErrorMessage) + len(attempt.ErrorCode) + len(attempt.UpstreamURL) + len(attempt.UpstreamModel)
	}
	for index := range input.Payloads {
		payload := &input.Payloads[index]
		total += producerPayloadEnvelope + len(payload.Body.Bytes)
	}
	return total
}

// Capture persists one audit entry asynchronously (fire-and-forget). A lost
// or unrenewable owner lease drops the entry with a warn; persistence errors
// are logged and swallowed — audit capture never fails the business request
// (the hot-search mirror can be rebuilt from the canonical store, so its
// failure is likewise a warn, not an outage). When the bounded queue is full
// or the byte budget is exhausted the entry is dropped with a warn instead of
// blocking the caller.
func (p *Producer) Capture(input AuditLogInput) {
	if p == nil || p.store == nil {
		return
	}
	p.start.Do(p.startWorkers)
	bytes := estimateAuditInputBytes(input)
	if bytes > int(producerQueueMaxBytes) {
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集单条超出队列字节预算，丢弃本条采集", "droppedTotal", dropped, "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	if int64(len(p.queue))+1 > producerQueueCapacity || p.queueBytes.Load()+int64(bytes) > producerQueueMaxBytes {
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	select {
	case p.queue <- queuedAudit{input: input, bytes: bytes}:
		p.queueBytes.Add(int64(bytes))
	default:
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "queuedBytes", p.queueBytes.Load(), "bytes", bytes, "traceID", input.TraceID, "auditLogID", input.ID)
	}
}

func (p *Producer) startWorkers() {
	for i := 0; i < producerWorkers; i++ {
		go p.workerLoop()
	}
}

// DroppedTotal exposes the running count of captures dropped because the
// bounded queue was full (Prometheus scrape seam).
func (p *Producer) DroppedTotal() int64 {
	if p == nil {
		return 0
	}
	return p.dropped.Load()
}

// QueueDepth exposes the current number of queued captures (Prometheus gauge
// seam).
func (p *Producer) QueueDepth() int {
	if p == nil {
		return 0
	}
	return len(p.queue)
}

// QueueBytes exposes the estimated byte residency of queued captures
// (Prometheus gauge seam).
func (p *Producer) QueueBytes() int64 {
	if p == nil {
		return 0
	}
	return p.queueBytes.Load()
}

func (p *Producer) workerLoop() {
	for item := range p.queue {
		p.persistOne(item.input)
		// Release the byte budget only after persistence settles so the
		// gauge covers queued + in-flight residency (exactly the residency
		// W14 bounds); a slow store therefore consumes the budget and
		// triggers drops instead of letting residency grow without bound.
		p.queueBytes.Add(-int64(item.bytes))
	}
}

// persistOne keeps the retired write sequence (renew → persist → hot search)
// with the same per-record degradation as the original per-record goroutine,
// plus panic isolation: one broken record must not take down the worker pool
// or the process.
func (p *Producer) persistOne(input AuditLogInput) {
	startedAt := time.Now()
	outcome := "ok"
	defer func() {
		if recovered := recover(); recovered != nil {
			p.warn("F3 审计采集持久化 panic 已隔离", "panic", recovered, "traceID", input.TraceID, "auditLogID", input.ID)
			p.observePersistDuration("panic", startedAt)
			return
		}
		p.observePersistDuration(outcome, startedAt)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lease := p.lease.Lease()
	// Extending the lease per record keeps it alive under write activity
	// (the LeaseKeeper ticker covers the idle case). A non-positive TTL
	// would set lease_until to the current instant and self-destruct the
	// fence, so the renewal is skipped instead — the configured
	// composition always passes the real owner-lease TTL.
	if p.cfg.OwnerLease > 0 {
		renewCtx, renewCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer renewCancel()
		renewed, err := p.store.RenewOwnerLease(renewCtx, lease, p.cfg.OwnerLease)
		if err != nil || !renewed {
			outcome = "error"
			p.warn("F3 owner lease renewal failed; dropping audit capture", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
			return
		}
	}
	persistResult, err := p.store.Persist(ctx, lease, input)
	if err != nil {
		outcome = "error"
		p.warn("F3 审计采集持久化失败", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	if persistResult.Ignored {
		p.warn("F3 重复或迟到的审计输入被忽略，跳过本次持久化",
			"event", "audit_input_ignored",
			"traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	if _, err := p.store.AppendHotSearch(ctx, lease, []AuditLogInput{input}); err != nil {
		p.warn("F3 audit hot-search append failed", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
	}
}

// observePersistDuration reports the per-record persistence latency through
// the optional observer (result "ok" | "error" | "panic").
func (p *Producer) observePersistDuration(result string, startedAt time.Time) {
	if p == nil || p.persistDuration == nil {
		return
	}
	p.persistDuration(result, time.Since(startedAt).Milliseconds())
}
