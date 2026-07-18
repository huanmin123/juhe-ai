package port

import "context"

const (
	RuntimeLogLevelAll   = "all"
	RuntimeLogLevelTrace = "trace"
	RuntimeLogLevelDebug = "debug"
	RuntimeLogLevelInfo  = "info"
	RuntimeLogLevelWarn  = "warn"
	RuntimeLogLevelError = "error"
	RuntimeLogLevelFatal = "fatal"
)

type ManagementRuntimeLogListInput struct {
	TraceID string
	Level   string
	Event   string
	Keyword string
	StartAt string
	EndAt   string
	Limit   int
	Offset  int
}

type ManagementRuntimeLogSummary struct {
	ID           string
	Time         string
	Level        string
	TraceID      *string
	Event        *string
	Message      *string
	ErrorMessage *string
	CreatedAt    string
}

type ManagementRuntimeLog struct {
	ManagementRuntimeLogSummary
	RawJSON string
}

type ManagementRuntimeLogListResult struct {
	Items   []ManagementRuntimeLogSummary
	HasMore bool
}

type ManagementRuntimeLogReader interface {
	ListManagementRuntimeLogs(
		ctx context.Context,
		input ManagementRuntimeLogListInput,
	) (ManagementRuntimeLogListResult, error)
	GetManagementRuntimeLog(ctx context.Context, id string) (ManagementRuntimeLog, bool, error)
}
