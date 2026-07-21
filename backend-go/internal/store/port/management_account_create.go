package port

import (
	"context"
	"time"
)

type ManagementAccountCreateInput struct {
	ID                                  string
	SystemAccountID                     string
	ProviderCode                        string
	ProviderProtocolProfileID           string
	Name                                string
	Type                                string
	Status                              string
	CredentialsEncrypted                string
	CredentialFingerprint               string
	HealthCheckModel                    string
	HealthCheckEndpointMode             string
	ConcurrencyLimit                    int
	Priority                            int
	SuperPriorityEnabled                bool
	FallbackEnabled                     bool
	Schedulable                         bool
	SupportedModels                     []string
	GroupID                             string
	ProxyProfileID                      string
	AccountExpiresAt                    *time.Time
	AvailabilityScheduleJSON            *string
	TemporaryUnavailableContinuousProbe int
	Notes                               *string
	CreatedAt                           time.Time
	UpdatedAt                           time.Time
}

type ManagementAccountCreateResult struct {
	Account map[string]any
}

type ManagementAccountCreator interface {
	CreateManagementAccount(context.Context, ManagementAccountCreateInput) (ManagementAccountCreateResult, error)
}
