package port

import "context"

type ManagementAccountTestOptionsInput struct {
	AccountID       string
	SystemAccountID string
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
}

type ManagementAccountTestOptionsReader interface {
	GetManagementAccountTestOptionsSource(ctx context.Context, input ManagementAccountTestOptionsInput) (ManagementAccountTestOptionsSource, bool, error)
}
