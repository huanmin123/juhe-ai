package port

import (
	"context"
	"time"
)

type ManagementAccountBatchEditTarget struct {
	AccountID      string
	ConfigRevision int
}

type ManagementAccountBatchEditInput struct {
	SystemAccountID string
	Targets         []ManagementAccountBatchEditTarget
	Updates         map[string]any
	Now             time.Time
}

type ManagementAccountBatchEditAccount struct {
	ID                  string
	SystemAccountID     string
	Name                string
	ProviderCode        string
	ProtocolCode        string
	ProtocolVersion     string
	Type                string
	Status              string
	ConcurrencyLimit    int
	Priority            int
	SuperPriority       bool
	FallbackEnabled     bool
	Schedulable         bool
	HealthCheckModel    string
	HealthCheckEndpoint string
	AccountExpiresAt    *time.Time
	Availability        map[string]any
	Notes               *string
	ConfigRevision      int
}

type ManagementAccountBatchEditResult struct {
	BatchID       string
	ChangedFields []string
	Accounts      []ManagementAccountBatchEditAccount
}

type ManagementAccountBatchEditReader interface {
	LoadManagementAccountBatchEditContext(context.Context, string, []string) ([]ManagementAccountBatchEditAccount, bool, error)
}

type ManagementAccountBatchEditor interface {
	UpdateManagementAccountsBatch(context.Context, ManagementAccountBatchEditInput) (ManagementAccountBatchEditResult, bool, error)
}
