package authsys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

// maxOperationLogChangesPerRecord mirrors the Node
// operationLogMaxChangesPerRecord upper bound (operation-log.service.ts:272-280:
// 1..500); the zero value keeps the unconfigured default of the same 500.
const maxOperationLogChangesPerRecord = 500

// SinkLogger is the drop-report channel of OperationLogProducerSink (same
// shape as the operationlog producer logger); the composition root wires its
// slog-backed producerLogger, tests record into a spy.
type SinkLogger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// OperationLogProducerSink adapts authsys entries to the F4 operationlog
// producer (in-process persistence replacing the Node HMAC loopback).
type OperationLogProducerSink struct {
	Producer   *operationlog.Producer
	MaxChanges int // mirror of system setting operationLogMaxChangesPerRecord (1..500)
	// Logger receives the invalid-MaxChanges drop report (Node
	// logOperationLogFailure); nil falls back to the default slog logger.
	Logger SinkLogger
}

// newOperationLogID mirrors Node recordOperationLog's `input.id ??
// newId('oplog')` (database.ts newId: prefix_ms_hex8). Without it every
// record failed normalization ("operation log input missing id") and the
// management plane persisted no operation logs at all.
func newOperationLogID(now time.Time) string {
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("oplog_%d", now.UnixMilli())
	}
	return fmt.Sprintf("oplog_%d_%s", now.UnixMilli(), hex.EncodeToString(random[:]))
}

func (s *OperationLogProducerSink) Record(entry OperationLogEntry, r *http.Request) {
	if s == nil || s.Producer == nil {
		return
	}
	now := time.Now().UTC()
	input := operationlog.Input{
		ID:                            newOperationLogID(now),
		ActorSystemAccountID:          entry.ActorSystemAccountID,
		ActorUsername:                 entry.ActorUsername,
		ActorDisplayName:              entry.ActorDisplayName,
		ActorRole:                     entry.ActorRole,
		OperationScopeSystemAccountID: entry.OperationScopeSystemAccountID,
		Mode:                          entry.Mode,
		Module:                        entry.Module,
		Action:                        entry.Action,
		OperationKey:                  entry.OperationKey,
		ResourceType:                  entry.ResourceType,
		ResourceID:                    entry.ResourceID,
		ResourceName:                  entry.ResourceName,
		Summary:                       entry.Summary,
		// M12 deferral (K4 sink extension): entries may now carry the Node
		// OperationLogInput detailLevel/visibilityScope/metadata. Empty
		// strings keep the historical slice contract ("summary"/"targeted")
		// so pre-extension producers are byte-identical.
		DetailLevel:     "summary",
		VisibilityScope: "targeted",
		ClientIP:        kernel.Context(r).ClientIP,
		Method:          r.Method,
		Path:            r.URL.Path,
		CreatedAt:       now.Format(time.RFC3339Nano),
	}
	if entry.DetailLevel != "" {
		input.DetailLevel = entry.DetailLevel
	}
	if entry.VisibilityScope != "" {
		input.VisibilityScope = entry.VisibilityScope
	}
	if len(entry.Metadata) > 0 {
		input.Metadata = append(json.RawMessage(nil), entry.Metadata...)
	}
	if entry.StatusCode != nil {
		statusCode := *entry.StatusCode
		input.StatusCode = &statusCode
	}
	for _, target := range entry.Targets {
		input.Targets = append(input.Targets, operationlog.Target{
			TargetType:                 target.TargetType,
			TargetID:                   target.TargetID,
			TargetName:                 target.TargetName,
			TargetOwnerSystemAccountID: target.TargetOwnerSystemAccountID,
			Relation:                   target.Relation,
		})
	}
	if trace := kernel.Context(r).TraceID; trace != "" {
		input.TraceID = trace
	}
	if userAgent := r.Header.Get("User-Agent"); userAgent != "" {
		input.UserAgent = userAgent
	}
	for _, change := range entry.Changes {
		input.Changes = append(input.Changes, operationlog.Change{
			Field:     change.Field,
			Label:     change.Label,
			Before:    changeValue(change.BeforeValue, change.Before),
			After:     changeValue(change.AfterValue, change.After),
			Sensitive: change.Sensitive,
		})
	}
	// Node operation-log.service.ts:272-280: the configured maximum must be a
	// finite integer within 1..500 — an out-of-range setting throws inside
	// recordOperationLogUnsafe, recordOperationLog's catch drops the whole
	// entry and logs the original error (logOperationLogFailure :104-113). The
	// zero value stays the unconfigured default (existing Go composition
	// contract: 未配置 = 默认 500); only an explicitly invalid configuration
	// (negative / above the cap) takes the drop path.
	maxChanges := s.MaxChanges
	if maxChanges == 0 {
		maxChanges = maxOperationLogChangesPerRecord
	}
	if maxChanges < 1 || maxChanges > maxOperationLogChangesPerRecord {
		s.reportInvalidMaxChanges(now, entry, maxChanges)
		return
	}
	if len(input.Changes) > maxChanges {
		remaining := len(input.Changes) - maxChanges
		input.Changes = input.Changes[:maxChanges]
		input.Changes = append(input.Changes, operationlog.Change{
			Field: "__truncated__",
			Label: "其余变更",
			After: "还有 " + itoa(remaining) + " 项变更未展开",
		})
	}
	for _, viewer := range entry.Viewers {
		input.Viewers = append(input.Viewers, operationlog.Viewer{
			SystemAccountID:  viewer.SystemAccountID,
			VisibilityReason: viewer.Reason,
		})
	}
	s.Producer.Record(input)
}

// reportInvalidMaxChanges mirrors Node logOperationLogFailure
// (operation-log.service.ts:104-113): the dropped entry is reported with the
// original validation error (the verbatim Node range message) plus its
// identity fields, instead of silently clamping an invalid configuration.
func (s *OperationLogProducerSink) reportInvalidMaxChanges(now time.Time, entry OperationLogEntry, configured int) {
	err := fmt.Errorf("系统设置 operationLogMaxChangesPerRecord 必须在 1 到 %d 之间", maxOperationLogChangesPerRecord)
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("F4 operation log entry dropped: invalid max changes",
		"error", err,
		"event", "operation_log_input_max_changes_invalid",
		"operationLogId", newOperationLogID(now),
		"module", entry.Module,
		"action", entry.Action,
		"configured", configured)
}

var _ OperationLogSink = (*OperationLogProducerSink)(nil)

var _ = context.Background

// maxCompositeChangeValueRender mirrors normalizeSafeValue's JSON.stringify
// 500-unit cap (operation-log.service.ts) for objects/arrays handed in via
// the M05/M07 value fields.
const maxCompositeChangeValueRender = 500

// changeValue renders a change value for the F4 Input: a non-nil native value
// wins over its string sibling, composite values (objects/arrays) flatten to
// JSON text exactly like Node normalizeSafeValue (native null/number/boolean
// pass through untouched), and an empty string collapses to nil so the
// omitempty contract keeps the property absent.
func changeValue(value any, text string) any {
	if value != nil {
		switch typed := value.(type) {
		case json.RawMessage:
			// Callers that need byte-exact object rendering (Node
			// JSON.stringify keeps insertion order) pass pre-encoded JSON.
			return truncateChangeRender(string(typed))
		case map[string]any, []any:
			encoded, err := json.Marshal(value)
			if err != nil {
				return truncateChangeRender(fmt.Sprintf("%v", value))
			}
			return truncateChangeRender(string(encoded))
		default:
			return value
		}
	}
	if text == "" {
		return nil
	}
	return text
}

func truncateChangeRender(text string) string {
	runes := []rune(text)
	if len(runes) > maxCompositeChangeValueRender {
		return string(runes[:maxCompositeChangeValueRender])
	}
	return text
}
