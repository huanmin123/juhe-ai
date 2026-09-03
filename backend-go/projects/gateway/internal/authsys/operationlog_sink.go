package authsys

import (
	"context"
	"net/http"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/operationlog"
)

// OperationLogProducerSink adapts authsys entries to the F4 operationlog
// producer (in-process persistence replacing the Node HMAC loopback).
type OperationLogProducerSink struct {
	Producer   *operationlog.Producer
	MaxChanges int // mirror of system setting operationLogMaxChangesPerRecord (1..500)
}

func (s *OperationLogProducerSink) Record(entry OperationLogEntry, r *http.Request) {
	if s == nil || s.Producer == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	input := operationlog.Input{
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
		DetailLevel:                   "summary",
		VisibilityScope:               "targeted",
		ClientIP:                      kernel.Context(r).ClientIP,
		Method:                        r.Method,
		Path:                          r.URL.Path,
		CreatedAt:                     now,
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
			Before:    change.Before,
			After:     change.After,
			Sensitive: change.Sensitive,
		})
	}
	maxChanges := s.MaxChanges
	if maxChanges <= 0 {
		maxChanges = 500
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

var _ OperationLogSink = (*OperationLogProducerSink)(nil)

var _ = context.Background
