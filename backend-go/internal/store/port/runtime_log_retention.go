package port

import (
	"context"
	"errors"
)

var ErrRuntimeLogRetentionDeferred = errors.New("runtime log retention deferred")

type RuntimeLogRetentionCleanupInput struct {
	// GoExclusiveIndexCleanupOwner asserts that the Node index-cleanup subowner is stopped.
	GoExclusiveIndexCleanupOwner bool
	CutoffISO                    string
	Limit                        int
}

type RuntimeLogRetentionCleaner interface {
	GetRuntimeLogIndexRetentionDays(ctx context.Context) (int, bool, error)
	CleanupRuntimeLogIndexBefore(ctx context.Context, input RuntimeLogRetentionCleanupInput) (int64, error)
	CleanupCompletedRuntimeLogFileCursorsBefore(ctx context.Context, input RuntimeLogRetentionCleanupInput) (int64, error)
}
