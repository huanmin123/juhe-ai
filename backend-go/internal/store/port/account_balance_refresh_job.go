package port

import (
	"context"
	"time"
)

type AccountBalanceRefreshCandidate struct {
	ID                     string
	SystemAccountID        string
	ConfigRevision         int
	CredentialsEncrypted   string
	BalanceQueryConfigJSON string
	NextRefreshAt          *time.Time
	StateUpdatedAt         time.Time
	ProxyProfileID         *string
}

type AccountBalanceRefreshJobReader interface {
	ListAccountBalanceRefreshRecoveryCandidates(ctx context.Context, limit int) ([]AccountBalanceRefreshCandidate, error)
	ListAccountBalanceRefreshDueCandidates(ctx context.Context, now time.Time, limit int) ([]AccountBalanceRefreshCandidate, error)
}
