package port

import (
	"context"
	"errors"
	"time"
)

var ErrManagementExternalIntegrationSourceTokenHashExists = errors.New(
	"management external integration source token hash exists",
)

type ManagementExternalIntegrationSourceCreateInput struct {
	SourceID             string
	Name                 string
	Status               string
	ScopesJSON           string
	RateLimitsJSON       string
	ExpiresAt            *time.Time
	Notes                *string
	TokenID              string
	TokenName            string
	TokenHash            string
	TokenSecretEncrypted string
	TokenPrefix          string
	TokenSuffix          string
	TokenStatus          string
	TokenScopesJSON      string
	TokenExpiresAt       *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ManagementExternalIntegrationSourceCreateResult struct {
	Source ManagementExternalIntegrationSourceListRow
	Token  ManagementExternalIntegrationSourcePrimaryTokenRow
}

type ManagementExternalIntegrationSourceCreator interface {
	CreateManagementExternalIntegrationSource(
		ctx context.Context,
		input ManagementExternalIntegrationSourceCreateInput,
	) (ManagementExternalIntegrationSourceCreateResult, error)
}
