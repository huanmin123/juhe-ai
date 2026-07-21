package port

import (
	"context"
	"time"
)

type AccountBalanceAutoDetectLookup struct {
	AccountID      string
	ConfigRevision int
}

type AccountBalanceAutoDetectCandidate struct {
	AccountID            string
	SystemAccountID      string
	ConfigRevision       int
	CredentialsEncrypted string
	ProxyProfileID       string
}

type AccountBalanceAutoDetectCommit struct {
	AccountID              string
	SystemAccountID        string
	ExpectedConfigRevision int
	ConfigJSON             string
	SnapshotStatus         string
	SnapshotJSON           string
	CompletedAt            time.Time
	NextRefreshAt          time.Time
}

type AccountBalanceAutoDetectStore interface {
	LoadAccountBalanceAutoDetectCandidate(ctx context.Context, input AccountBalanceAutoDetectLookup) (AccountBalanceAutoDetectCandidate, bool, error)
	CommitAccountBalanceAutoDetect(ctx context.Context, input AccountBalanceAutoDetectCommit) (bool, error)
}
