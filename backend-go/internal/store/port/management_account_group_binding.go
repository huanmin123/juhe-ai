package port

import (
	"context"
	"time"
)

type ManagementAccountGroupBindingInput struct {
	AccountID                string
	GroupID                  string
	EffectiveSystemAccountID string
	CanAccessAll             bool
	UpdatedAt                time.Time
}

type ManagementAccountGroupBindingAccount struct {
	ID                        string
	SystemAccountID           string
	Name                      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	Status                    string
	ClientCompatibility       string
	BoundGroupID              string
	BoundGroupName            string
	Schedulable               bool
	ConcurrencyLimit          int
	Priority                  int
	SuperPriorityEnabled      bool
	FallbackEnabled           bool
	HealthCheckModel          string
}

type ManagementAccountGroupBindingResult struct {
	Account         ManagementAccountGroupBindingAccount
	PreviousGroupID string
}

type ManagementAccountGroupBinder interface {
	BindManagementAccountGroup(ctx context.Context, input ManagementAccountGroupBindingInput) (ManagementAccountGroupBindingResult, bool, error)
}
