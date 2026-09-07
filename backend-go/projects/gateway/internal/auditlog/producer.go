// Producer is the in-process F3 audit write path (去跨进程战役第四刀). It
// replaces the retired loopback HMAC input server as the chain audit sink and
// mirrors the operationlog.Producer contract: fire-and-forget capture that
// never fails the gateway request. Per record it renews the shared owner
// lease, persists through the store (validation + normalization live inside
// store.Persist exactly as for the retired HTTP writes) and appends the
// hot-search mirror for non-ignored writes.
package auditlog

import (
	"context"
	"time"
)

type Producer struct {
	store Store
	lease OwnerLease
	cfg   Config
	log   producerLogger
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
	return &Producer{store: store, lease: lease, cfg: cfg, log: log}
}

// Capture persists one audit entry asynchronously (fire-and-forget). A lost
// or unrenewable owner lease drops the entry with a warn; persistence errors
// are logged and swallowed — audit capture never fails the business request
// (the hot-search mirror can be rebuilt from the canonical store, so its
// failure is likewise a warn, not an outage).
func (p *Producer) Capture(input AuditLogInput) {
	if p == nil || p.store == nil {
		return
	}
	go func() {
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
	}()
}
