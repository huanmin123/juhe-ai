package port

import "context"

type ManagementAccountTestDispatchAccount struct {
	ID                        string
	Name                      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	AccessType                string
	HealthCheckModel          string
	HealthCheckEndpointMode   string
}

type ManagementAccountTestDispatchCreateInput struct {
	TaskID                    string
	SessionID                 string
	AccountID                 string
	AccountName               string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	AccountType               string
	Diagnostics               string
	Model                     string
	TestEndpointMode          string
	DraftAccountEncrypted     string
	Access                    ManagementAccountTestAccess
}

type ManagementAccountTestDispatchStore interface {
	ResolveManagementAccountTestAccount(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestDispatchAccount, bool, error)
	CreateManagementAccountTestTask(context.Context, ManagementAccountTestDispatchCreateInput) (ManagementAccountTestTask, bool, error)
	GetManagementAccountTestTask(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestTask, bool, error)
	MarkManagementAccountTestEnqueueFailed(context.Context, string, ManagementAccountTestAccess, string) (ManagementAccountTestTask, bool, error)
}
