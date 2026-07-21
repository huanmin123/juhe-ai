package port

import "context"

const ManagementAccountExportMaxAccounts = 50

type ManagementAccountExportFilter struct {
	Keyword      string
	ProviderCode string
	GroupID      string
	TagIDs       []string
	Type         string
	Statuses     []string
	Schedulable  string
}

type ManagementAccountExportInput struct {
	SystemAccountID string
	AccountIDs      []string
	Filter          ManagementAccountExportFilter
}

type ManagementAccountExportAccount struct {
	ID                                  string
	Name                                string
	ProviderCode                        string
	ProviderProtocolProfileID           string
	ProtocolCode                        string
	ProtocolVersion                     string
	Type                                string
	Status                              string
	SystemAccountID                     string
	CredentialsEncrypted                string
	GroupID                             string
	GroupName                           string
	ProxyProfileID                      string
	ProxyName                           string
	ProxyType                           string
	ProxyHost                           string
	ProxyPort                           int
	ProxyUsername                       string
	ProxyPasswordEncrypted              string
	ProxyDescription                    string
	ProxyEnabled                        bool
	ConcurrencyLimit                    int
	Priority                            int
	SuperPriorityEnabled                bool
	FallbackEnabled                     bool
	Schedulable                         bool
	SupportedModelsJSON                 string
	HealthCheckModel                    string
	HealthCheckEndpointMode             string
	TemporaryUnavailableContinuousProbe bool
	ModelMappingsJSON                   string
	TagsJSON                            string
	AccountExpiresAt                    string
	AvailabilityScheduleJSON            string
	Notes                               string
}

type ManagementAccountExportPage struct {
	Items   []ManagementAccountExportAccount
	Matched int
	HasMore bool
	NextID  string
}

type ManagementAccountExportReader interface {
	ListManagementAccountExportBatch(ctx context.Context, input ManagementAccountExportInput, afterID string, limit int) (ManagementAccountExportPage, error)
}
