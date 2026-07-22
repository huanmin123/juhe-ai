package port

import (
	"context"
	"time"
)

const MaxAPIKeyRecordCleanupTargetLimit = 100

type APIKeyRecordCleanupRunInput struct {
	Limit         int
	AttemptedAt   time.Time
	BlockedReason string
}

type APIKeyRecordCleanupRunResult struct {
	Attempted int64
	Deferred  int64
}

type APIKeyRecordCleanupRunner interface {
	RunAPIKeyRecordCleanupOnce(
		ctx context.Context,
		input APIKeyRecordCleanupRunInput,
	) (APIKeyRecordCleanupRunResult, error)
}
