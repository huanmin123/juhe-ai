package port

import (
	"context"
	"time"
)

type PublicAPILogCleanupInput struct {
	CutoffCreatedAt time.Time
	Limit           int
}

type PublicAPILogRetentionCleaner interface {
	GetPublicAPILogRetentionDays(ctx context.Context) (int, bool, error)
	CleanupPublicAPILogsBefore(ctx context.Context, input PublicAPILogCleanupInput) (int64, error)
}
