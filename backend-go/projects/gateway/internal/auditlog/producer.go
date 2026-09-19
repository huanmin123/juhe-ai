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
)

type Producer struct {
	store Store
	lease OwnerLease
	cfg   Config
	log   producerLogger

	queue   chan AuditLogInput
	start   sync.Once
	dropped atomic.Int64
}

type producerLogger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func (p *Producer) warn(msg string, args ...any) {
	if p.log != nil {
		p.log.Warn(msg, args...)
	}
}

// NewProducer binds the producer to an already-held owner lease (the
// process-wide LeaseKeeper owned by main) and the persistence config. The
// renewal lifecycle stays with the keeper; the producer only extends the same
// lease per record, mirroring the operationlog.Producer contract.
func NewProducer(store Store, lease OwnerLease, cfg Config, log producerLogger) *Producer {
	return &Producer{
		store: store,
		lease: lease,
		cfg:   cfg,
		log:   log,
		queue: make(chan AuditLogInput, producerQueueCapacity),
	}
}

// Capture persists one audit entry asynchronously (fire-and-forget). A lost
// or unrenewable owner lease drops the entry with a warn; persistence errors
// are logged and swallowed — audit capture never fails the business request
// (the hot-search mirror can be rebuilt from the canonical store, so its
// failure is likewise a warn, not an outage). When the bounded queue is full
// the entry is dropped with a warn instead of blocking the caller.
func (p *Producer) Capture(input AuditLogInput) {
	if p == nil || p.store == nil {
		return
	}
	p.start.Do(p.startWorkers)
	select {
	case p.queue <- input:
	default:
		dropped := p.dropped.Add(1)
		p.warn("F3 审计采集队列已满，丢弃本条采集", "droppedTotal", dropped, "traceID", input.TraceID, "auditLogID", input.ID)
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

func (p *Producer) workerLoop() {
	for input := range p.queue {
		p.persistOne(input)
	}
}

// persistOne keeps the retired write sequence (renew → persist → hot search)
// with the same per-record degradation as the original per-record goroutine,
// plus panic isolation: one broken record must not take down the worker pool
// or the process.
func (p *Producer) persistOne(input AuditLogInput) {
	defer func() {
		if recovered := recover(); recovered != nil {
			p.warn("F3 审计采集持久化 panic 已隔离", "panic", recovered, "traceID", input.TraceID, "auditLogID", input.ID)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Extending the lease per record keeps it alive under write activity
	// (the LeaseKeeper ticker covers the idle case). A non-positive TTL
	// would set lease_until to the current instant and self-destruct the
	// fence, so the renewal is skipped instead — the configured
	// composition always passes the real owner-lease TTL.
	if p.cfg.OwnerLease > 0 {
		renewCtx, renewCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer renewCancel()
		renewed, err := p.store.RenewOwnerLease(renewCtx, p.lease, p.cfg.OwnerLease)
		if err != nil || !renewed {
			p.warn("F3 owner lease renewal failed; dropping audit capture", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
			return
		}
	}
	result, err := p.store.Persist(ctx, p.lease, input)
	if err != nil {
		p.warn("F3 审计采集持久化失败", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
		return
	}
	if result.Ignored {
		return
	}
	if _, err := p.store.AppendHotSearch(ctx, p.lease, []AuditLogInput{input}); err != nil {
		p.warn("F3 audit hot-search append failed", "error", err, "traceID", input.TraceID, "auditLogID", input.ID)
	}
}
