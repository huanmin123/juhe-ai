package port

import (
	"context"
	"time"
)

type ManagementAccountImportOptions struct {
	CreateMissingGroups  bool
	CreateMissingProxies bool
	SkipDuplicates       bool
}

type ManagementAccountImportProxy struct {
	Ref               string
	ID                string
	Name              string
	Type              string
	Host              string
	Port              int
	Username          string
	PasswordEncrypted string
	Description       string
	Enabled           bool
}

type ManagementAccountImportAccount struct {
	Index                               int
	Ref                                 string
	ID                                  string
	Name                                string
	ProviderCode                        string
	ProviderProtocolProfileID           string
	Type                                string
	Status                              string
	CredentialsEncrypted                string
	CredentialFingerprint               string
	GroupID                             string
	GroupName                           string
	ProxyRef                            string
	ProxyProfileID                      string
	ConcurrencyLimit                    int
	Priority                            int
	SuperPriorityEnabled                bool
	FallbackEnabled                     bool
	SupportedModels                     []string
	HealthCheckModel                    string
	HealthCheckEndpointMode             string
	TemporaryUnavailableContinuousProbe bool
	AccountExpiresAt                    *time.Time
	AvailabilityScheduleJSON            *string
	Notes                               *string
}

type ManagementAccountImportInput struct {
	SystemAccountID string
	Options         ManagementAccountImportOptions
	Proxies         []ManagementAccountImportProxy
	Accounts        []ManagementAccountImportAccount
	Now             time.Time
}

type ManagementAccountImportResult struct {
	Imported int
	Skipped  int
	Summary  any
}

type ManagementAccountImporter interface {
	Import(context.Context, ManagementAccountImportInput) (ManagementAccountImportResult, error)
}
