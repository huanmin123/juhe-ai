package port

import (
	"context"
	"time"
)

type ManagementClientIPPolicyType string

const (
	ManagementClientIPPolicyTypeAllowlist ManagementClientIPPolicyType = "allowlist"
)

type ManagementClientIPPolicyStatus string

const (
	ManagementClientIPPolicyStatusActive   ManagementClientIPPolicyStatus = "active"
	ManagementClientIPPolicyStatusDisabled ManagementClientIPPolicyStatus = "disabled"
)

type ManagementClientIPRegistryRow struct {
	IPHash   string
	ClientIP string
}

type ManagementClientIPPolicySummary struct {
	ID                        string
	IPHash                    string
	PolicyType                ManagementClientIPPolicyType
	Status                    ManagementClientIPPolicyStatus
	Reason                    *string
	ExpiresAt                 *time.Time
	CreatedBySystemAccountID  string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	DisabledAt                *time.Time
	DisabledBySystemAccountID *string
	DisabledReason            *string
}

type ManagementClientIPPolicyDisableInput struct {
	IPHash               string
	ActorSystemAccountID string
	Reason               string
	Now                  time.Time
}

type ManagementClientIPAllowlistCreateInput struct {
	ID                   string
	IPHash               string
	Reason               *string
	ActorSystemAccountID string
	Now                  time.Time
}

type ManagementClientIPPolicyStore interface {
	LockManagementClientIPRegistry(
		ctx context.Context,
		ipHash string,
	) (ManagementClientIPRegistryRow, bool, error)
	DisableActiveManagementClientIPPolicies(
		ctx context.Context,
		input ManagementClientIPPolicyDisableInput,
	) (int64, error)
	InsertManagementClientIPAllowlistPolicy(
		ctx context.Context,
		input ManagementClientIPAllowlistCreateInput,
	) (ManagementClientIPPolicySummary, error)
	DisableActiveManagementClientIPAllowlistPolicies(
		ctx context.Context,
		input ManagementClientIPPolicyDisableInput,
	) (int64, error)
}

type ManagementClientIPPolicyTransactor interface {
	ManagementClientIPPolicyInTx(
		ctx context.Context,
		fn func(context.Context, ManagementClientIPPolicyStore) error,
	) error
}
