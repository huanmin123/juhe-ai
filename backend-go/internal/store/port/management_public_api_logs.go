package port

import (
	"context"
	"time"
)

type ManagementPublicAPILogResultFilter string

const (
	ManagementPublicAPILogResultAll     ManagementPublicAPILogResultFilter = "all"
	ManagementPublicAPILogResultSuccess ManagementPublicAPILogResultFilter = "success"
	ManagementPublicAPILogResultFailed  ManagementPublicAPILogResultFilter = "failed"
)

type ManagementPublicAPILogListInput struct {
	TraceID     string
	SourceRefID string
	Path        string
	Result      ManagementPublicAPILogResultFilter
	StatusCode  *int
	ClientIP    string
	StartAt     time.Time
	EndAt       time.Time
	Limit       int
	Offset      int
}

type ManagementPublicAPILogSummary struct {
	ID                    string
	TraceID               *string
	SourceRefID           *string
	SourceName            *string
	TokenID               *string
	TokenName             *string
	TokenPrefix           *string
	IsTestToken           bool
	Method                string
	Path                  string
	QueryString           *string
	ClientIP              *string
	UserAgent             *string
	StatusCode            *int
	Success               bool
	DurationMs            *int64
	RequestSizeBytes      int64
	ResponseSizeBytes     int64
	RequestCaptureStatus  PublicAPILogCaptureStatus
	ResponseCaptureStatus PublicAPILogCaptureStatus
	ErrorCode             *string
	ErrorMessage          *string
	StartedAt             time.Time
	EndedAt               time.Time
	CreatedAt             time.Time
}

type ManagementPublicAPILogDetail struct {
	ManagementPublicAPILogSummary
	RequestDataJSON  string
	ResponseDataJSON string
}

type ManagementPublicAPILogListResult struct {
	Items   []ManagementPublicAPILogSummary
	HasMore bool
}

type ManagementPublicAPILogReader interface {
	ListManagementPublicAPILogs(
		ctx context.Context,
		input ManagementPublicAPILogListInput,
	) (ManagementPublicAPILogListResult, error)
	GetManagementPublicAPILog(
		ctx context.Context,
		id string,
	) (ManagementPublicAPILogDetail, bool, error)
}
