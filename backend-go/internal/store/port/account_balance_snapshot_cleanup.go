package port

import (
	"context"
	"time"
)

type AccountBalanceSnapshotCleanupInput struct {
	AccountID       string
	SystemAccountID string
	UpdatedBefore   time.Time
	Reason          string
}

type AccountBalanceSnapshotCleanupStore interface {
	DeleteAccountBalanceSnapshot(ctx context.Context, input AccountBalanceSnapshotCleanupInput) error
}
