package port

import (
	"context"
	"time"
)

type OperationLogChange struct {
	Field     string `json:"field"`
	Label     string `json:"label"`
	Before    any    `json:"before,omitempty"`
	After     any    `json:"after,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type OperationLogTargetInput struct {
	TargetType                 string
	TargetID                   string
	TargetName                 string
	TargetOwnerSystemAccountID string
	Relation                   string
}

type OperationLogViewerInput struct {
	SystemAccountID  string
	VisibilityReason string
	DetailLevel      string
}

type OperationLogInput struct {
	ID                            string
	TraceID                       string
	ActorSystemAccountID          string
	ActorUsername                 string
	ActorDisplayName              string
	ActorRole                     string
	OperationScopeSystemAccountID string
	Mode                          string
	Module                        string
	Action                        string
	OperationKey                  string
	ResourceType                  string
	ResourceID                    string
	ResourceName                  string
	Summary                       string
	DetailLevel                   string
	VisibilityScope               string
	Changes                       []OperationLogChange
	Metadata                      map[string]any
	Method                        string
	Path                          string
	StatusCode                    *int
	ClientIP                      string
	UserAgent                     string
	Targets                       []OperationLogTargetInput
	Viewers                       []OperationLogViewerInput
	CreatedAt                     time.Time
}

type OperationLogStore interface {
	InsertOperationLog(ctx context.Context, input OperationLogInput) error
}
