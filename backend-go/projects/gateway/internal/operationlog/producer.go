// Producer mirrors Node operation-log.service.ts recordOperationLogAsync:
// fire-and-forget persistence with best-effort error logging, plus the
// safeChange sensitive-field redaction contract. The in-process producer
// replaces the Node HMAC loopback input server for Go-owned slices.
package operationlog

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Producer persists operation logs directly through the store with a held
// owner lease (mirroring RunInputServer's lease lifecycle, minus HTTP).
type Producer struct {
	store Store
	lease OwnerLease
	cfg   Config
	log   slogLogger
}

type slogLogger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

func (p *Producer) warn(msg string, args ...any) {
	if p.log != nil {
		p.log.Warn(msg, args...)
	}
}

// StartProducer acquires the owner lease; the lease renewal lifecycle stays
// owned by RunInputServer-style callers. Producers created via NewProducer
// share an already-held lease.
func NewProducer(store Store, lease OwnerLease, cfg Config, log slogLogger) *Producer {
	return &Producer{store: store, lease: lease, cfg: cfg, log: log}
}

// Record persists one entry asynchronously (fire-and-forget). Errors are
// logged and swallowed: operation logs never fail the business transaction
// (Node recordOperationLogAsync contract).
func (p *Producer) Record(entry Input) {
	if p == nil || p.store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		renewCtx, renewCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer renewCancel()
		renewed, err := p.store.RenewOwnerLease(renewCtx, p.lease, p.cfg.OwnerLease)
		if err != nil || !renewed {
			p.warn("F4 owner lease renewal failed; dropping operation log", "error", err)
			return
		}
		if _, err := p.store.Persist(ctx, p.lease, entry); err != nil {
			p.warn("F4 Go 操作日志提交失败", "error", err)
		}
	}()
}

// SafeChange mirrors operation-log.service.ts safeChange: sensitive fields
// never record values; normal strings truncate at 200 chars; structured
// values are JSON-serialized and truncated at 500.
func SafeChange(field, label string, before, after any, sensitive bool) Change {
	change := Change{Field: field, Label: label}
	if sensitive {
		change.Sensitive = true
		change.Before = unsetOrSet(before)
		change.After = "已变更"
		return change
	}
	change.Before = truncateForLog(before, 200)
	change.After = truncateForLog(after, 200)
	return change
}

func unsetOrSet(value any) string {
	if s, ok := value.(string); ok {
		if strings.TrimSpace(s) == "" {
			return "未设置"
		}
		return "已设置"
	}
	if value == nil {
		return "未设置"
	}
	return "已设置"
}

func truncateForLog(value any, max int) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		if len(v) > max {
			return v[:max] + "..."
		}
		return v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		s := string(encoded)
		if len(s) > 500 {
			s = s[:500] + "..."
		}
		return s
	}
}
