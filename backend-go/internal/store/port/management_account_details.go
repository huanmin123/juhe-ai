package port

import "context"

type ManagementAccountDetailInput struct {
	AccountID       string
	SystemAccountID string
}

type ManagementAccountDetailSource struct {
	ID                    string
	SourceAccountID       string
	AccessType            string
	ProviderCode          string
	ProtocolCode          string
	ProtocolVersion       string
	Type                  string
	ConfigRevision        int
	CredentialsEncrypted  string
	HasActiveManualSource bool
	DetailJSON            string
}

type ManagementAccountAPIKeyRuntimeState struct {
	KeyFingerprint      string
	KeyIndex            int
	Status              string
	FailureCount        int
	ConsecutiveFailures int
	SuccessCount        int64
	CooldownUntil       string
	NextProbeAt         string
	LastAttemptAt       string
	LastSuccessAt       string
	LastFailureAt       string
	LastErrorCode       string
	LastErrorMessage    string
	LastTraceID         string
}

type ManagementAccountDetailReader interface {
	GetManagementAccountDetailSource(ctx context.Context, input ManagementAccountDetailInput) (ManagementAccountDetailSource, bool, error)
	ListManagementAccountAPIKeyRuntimeStates(ctx context.Context, accountID string) ([]ManagementAccountAPIKeyRuntimeState, error)
}
