package port

import (
	"context"
	"time"
)

const AccountHealthCheckMaxPageSize = 200

type AccountHealthCheckCandidate struct {
	ID             string
	ConfigRevision int
	Status         string
	Schedulable    bool
	BoundGroupID   string
	ExpiresAt      *time.Time
	NextCheckAt    *time.Time
}

type AccountHealthCheckCandidatePage struct {
	Items      []AccountHealthCheckCandidate
	NextCursor string
	HasMore    bool
}

type AccountHealthCheckCandidateReader interface {
	ListAccountHealthCheckCandidates(ctx context.Context, afterID string, limit int, now time.Time) (AccountHealthCheckCandidatePage, error)
}

type AccountHealthCheckCurrentReader interface {
	GetAccountHealthCheckCandidate(ctx context.Context, accountID string, now time.Time) (AccountHealthCheckCandidate, bool, error)
}
