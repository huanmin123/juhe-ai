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

type OperationLogListInput struct {
	SummaryKeyword                string
	Module                        string
	Action                        string
	ResourceType                  string
	ResourceID                    string
	ActorSystemAccountID          string
	AffectedSystemAccountID       string
	OperationScopeSystemAccountID string
	TraceID                       string
	StartAt                       time.Time
	EndAt                         time.Time
	Limit                         int
	Offset                        int
}

type OperationLogVisibleListInput struct {
	ViewerSystemAccountID string
	List                  OperationLogListInput
}

type OperationLogDetailInput struct {
	ID                    string
	ViewerSystemAccountID string
}

type OperationLogListResult struct {
	Items   []OperationLogSummary
	HasMore bool
}

type OperationLogSummary struct {
	ID                              string
	TraceID                         string
	ActorSystemAccountID            string
	ActorUsername                   string
	ActorDisplayName                string
	ActorSystemAccountName          string
	ActorRole                       string
	OperationScopeSystemAccountID   string
	OperationScopeSystemAccountName string
	Mode                            string
	Module                          string
	Action                          string
	OperationKey                    string
	ResourceType                    string
	ResourceID                      string
	ResourceName                    string
	Summary                         string
	DetailLevel                     string
	VisibilityScope                 string
	Changes                         []OperationLogChange
	Metadata                        map[string]any
	Method                          string
	Path                            string
	StatusCode                      *int
	ClientIP                        string
	UserAgent                       string
	CreatedAt                       time.Time
	ViewerDetailLevel               string
}

type OperationLogTargetSummary struct {
	ID                           string
	TargetType                   string
	TargetID                     string
	TargetName                   string
	TargetOwnerSystemAccountID   string
	TargetOwnerSystemAccountName string
	Relation                     string
	CreatedAt                    time.Time
}

type OperationLogViewerSummary struct {
	SystemAccountID   string
	SystemAccountName string
	VisibilityReason  string
	DetailLevel       string
	CreatedAt         time.Time
}

type OperationLogDetail struct {
	Summary OperationLogSummary
	Targets []OperationLogTargetSummary
	Viewers []OperationLogViewerSummary
}

type OperationLogReader interface {
	ListOperationLogs(ctx context.Context, input OperationLogListInput) (OperationLogListResult, error)
	ListVisibleOperationLogs(ctx context.Context, input OperationLogVisibleListInput) (OperationLogListResult, error)
	GetOperationLogDetail(ctx context.Context, input OperationLogDetailInput) (OperationLogDetail, bool, error)
}
