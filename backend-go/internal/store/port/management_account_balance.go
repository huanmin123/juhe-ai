package port

import "context"

type ManagementAccountBalanceInput struct {
	AccountID       string
	SystemAccountID string
}

type ManagementAccountBalanceSnapshot struct {
	AccountID       string
	SystemAccountID string
	Status          string
	SnapshotJSON    string
	NextRefreshAt   string
	UpdatedAt       string
}

type ManagementAccountBalanceCandidate struct {
	AccountID            string
	SystemAccountID      string
	ProviderCode         string
	ProtocolCode         string
	ProtocolVersion      string
	Type                 string
	CredentialsEncrypted string
}

type ManagementAccountBalanceReader interface {
	GetManagementAccountBalanceSnapshot(ctx context.Context, input ManagementAccountBalanceInput) (ManagementAccountBalanceSnapshot, bool, error)
	GetManagementAccountBalanceCandidate(ctx context.Context, input ManagementAccountBalanceInput) (ManagementAccountBalanceCandidate, bool, error)
}

type ManagementAccountBalanceWriter interface {
	UpsertManagementAccountBalanceSnapshot(ctx context.Context, snapshot ManagementAccountBalanceSnapshot) error
}
