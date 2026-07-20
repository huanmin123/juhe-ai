package port

import "context"

type ManagementAccountStatusSnapshotInput struct {
	AccountIDs      []string
	SystemAccountID string
}

type ManagementAccountStatusProjection struct {
	ID                                       string
	SystemAccountID                          string
	Name                                     string
	Status                                   string
	Schedulable                              bool
	AccountExpiresAt                         string
	CooldownUntil                            string
	LastErrorCode                            string
	LastErrorMessage                         string
	LastErrorTraceID                         string
	LastHealthCheckAt                        string
	NextHealthCheckAt                        string
	LastHealthCheckStatusCode                int
	LastHealthCheckErrorCode                 string
	LastHealthCheckErrorMessage              string
	LastHealthCheckTraceID                   string
	LastUsedAt                               string
	AuthorizationID                          string
	AuthorizationStatus                      string
	AuthorizationExpiresAt                   string
	AuthorizationInstanceSourceAccountID     string
	AuthorizationInstanceSourceAccountStatus string
	AuthorizationInstanceSourceSchedulable   bool
	AuthorizationInstanceSourceExpiresAt     string
	BoundGroupID                             string
	BoundGroupName                           string
	GroupBindStatus                          string
	TodayUsageJSON                           string
}

type ManagementAccountStatusSnapshotReader interface {
	ListManagementAccountStatusProjections(ctx context.Context, input ManagementAccountStatusSnapshotInput) ([]ManagementAccountStatusProjection, error)
}
