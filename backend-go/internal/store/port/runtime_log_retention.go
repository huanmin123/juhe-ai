package port

import "context"

type RuntimeLogRetentionCleanupInput struct {
	CutoffISO string
	Limit     int
}

type RuntimeLogRetentionCleaner interface {
	GetRuntimeLogIndexRetentionDays(ctx context.Context) (int, bool, error)
	CleanupRuntimeLogIndexBefore(ctx context.Context, input RuntimeLogRetentionCleanupInput) (int64, error)
	CleanupCompletedRuntimeLogFileCursorsBefore(ctx context.Context, input RuntimeLogRetentionCleanupInput) (int64, error)
}
