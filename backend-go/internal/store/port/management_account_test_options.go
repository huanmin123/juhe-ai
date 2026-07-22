package port

import "context"

type ManagementAccountTestOptionsInput struct {
	AccountID       string
	SystemAccountID string
}

type ManagementAccountTestOptionListSource struct {
	ID                        string
	OwnerSystemAccountID      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	ClientCompatibility       string
	HealthCheckModel          string
}

type ManagementAccountTestModelCatalogInput struct {
	ProviderCode    string
	SystemAccountID string
	Keyword         string
	Limit           int
	SelectedIDs     []string
	ModelIDs        []string
}

type ManagementAccountTestModelCapabilitiesSourceInput struct {
	AccountID       string
	SystemAccountID string
	Model           string
}

type ManagementAccountTestModelCatalogItem struct {
	ID                    string
	ProviderCode          string
	Model                 string
	Scope                 string
	Mode                  string
	SupportedAPIProtocols []string
}

type ManagementAccountTestModelMapping struct {
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
	Enabled                bool
}

type ManagementAccountTestOptionsSource struct {
	ID                        string
	OwnerSystemAccountID      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	ClientCompatibility       string
	HealthCheckModel          string
	HealthCheckEndpointMode   string
	CredentialsEncrypted      string
	ModelMappings             []ManagementAccountTestModelMapping
}

type ManagementAccountTestOptionsReader interface {
	GetManagementAccountTestOptionsSource(ctx context.Context, input ManagementAccountTestOptionsInput) (ManagementAccountTestOptionsSource, bool, error)
}

type ManagementAccountTestOptionReader interface {
	GetManagementAccountTestOptionListSource(ctx context.Context, input ManagementAccountTestOptionsInput) (ManagementAccountTestOptionListSource, bool, error)
	GetManagementAccountTestModelCapabilitiesSource(ctx context.Context, input ManagementAccountTestModelCapabilitiesSourceInput) (ManagementAccountTestOptionsSource, bool, error)
	ListManagementAccountTestModelCatalog(ctx context.Context, input ManagementAccountTestModelCatalogInput) ([]ManagementAccountTestModelCatalogItem, error)
}
