// Producer mirrors Node operation-log.service.ts recordOperationLogAsync:
// fire-and-forget persistence with best-effort error logging, plus the
// safeChange sensitive-field redaction contract. Since 去跨进程战役第四刀 it
// is the only F4 write path (the loopback HMAC input server is deleted; the
// authsys management-plane sink and every other writer go through here).
package operationlog

import (
	"context"
	"encoding/json"
	"time"
	"unicode/utf16"
)

// Producer persists operation logs directly through the store with a held
// owner lease (the process-wide LeaseKeeper owns the renewal lifecycle; the
// producer only extends the same lease per record).
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

// NewProducer binds the producer to an already-held lease shared with the
// resident F4 owner component (retention).
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
				p.warn("F4 owner lease renewal failed; dropping operation log", "error", err)
				return
			}
		}
		if _, err := p.store.Persist(ctx, p.lease, entry); err != nil {
			p.warn("F4 Go 操作日志提交失败", "error", err)
		}
	}()
}

// SafeChange mirrors operation-log.service.ts safeChange (:181-192): sensitive
// fields never record values and an emptied value shows 未设置 (Node :186-187 —
// undefined/null/'' → '未设置', otherwise 已设置 before / 已变更 after); normal
// fields go through normalizeSafeValue.
func SafeChange(field, label string, before, after any, sensitive bool) Change {
	change := Change{Field: field, Label: label}
	if sensitive {
		change.Sensitive = true
		change.Before = sensitiveAuditValue(before, "已设置")
		change.After = sensitiveAuditValue(after, "已变更")
		return change
	}
	change.Before = normalizeSafeValue(before)
	change.After = normalizeSafeValue(after)
	return change
}

// sensitiveAuditValue mirrors operation-log.service.ts:186-187. The emptiness
// test is the strict Node check (`=== undefined || === null || === ''`); Go
// nil covers undefined/null (both serialize as an omitted/null JSON value).
func sensitiveAuditValue(value any, setLabel string) string {
	if value == nil {
		return "未设置"
	}
	if empty, ok := value.(string); ok && empty == "" {
		return "未设置"
	}
	return setLabel
}

// truncateUTF16Units mirrors the Node `String.prototype.slice(0, limit)` cuts
// in normalizeSafeValue (operation-log.service.ts:224,237): the limit counts
// UTF-16 code units (astral runes count twice, the same convention as the
// announcements utf16Length precedent) and the cut lands on a rune boundary,
// so the result is always valid UTF-8. The boolean reports whether any
// content was dropped.
func truncateUTF16Units(value string, limit int) (string, bool) {
	units := 0
	for index, symbol := range value {
		width := utf16.RuneLen(symbol)
		if units+width > limit {
			return value[:index], true
		}
		units += width
	}
	return value, false
}

// normalizeSafeValue mirrors operation-log.service.ts normalizeSafeValue
// (:222-239): undefined/null, numbers and booleans keep their native type
// (Go nil serializes as an omitted field, the closest equivalent of Node's
// `undefined`); strings truncate at 200 UTF-16 code units with an ellipsis;
// every other value is JSON-serialized and hard-cut at 500 UTF-16 code units
// without any appended marker — all cuts stay on rune boundaries.
func normalizeSafeValue(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if truncated, cut := truncateUTF16Units(v, 200); cut {
			return truncated + "..."
		}
		return v
	case json.Number:
		// A json.Number is a number in the Node sense and must survive as a
		// JSON number, not become a quoted string.
		return v
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			// Unreachable for JSON-representable values (channels, funcs and
			// complex numbers cannot appear in audited changes); Node's
			// String(value) fallback has no stable Go equivalent.
			return ""
		}
		if truncated, cut := truncateUTF16Units(string(encoded), 500); cut {
			return truncated
		}
		return string(encoded)
	}
}
