package port

import (
	"context"
	"errors"
	"time"
)

var ErrManagementExternalIntegrationSourceBuiltInTokenCreateRestricted = errors.New(
	"management external integration source built-in token create restricted",
)

type ManagementExternalIntegrationSourceTokenCreateInput struct {
	TokenID              string
	SourceID             string
	Name                 string
	TokenHash            string
	TokenSecretEncrypted string
	TokenPrefix          string
	TokenSuffix          string
	Status               string
	ScopesJSON           string
	ExpiresAt            *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ManagementExternalIntegrationSourceTokenCreateResult struct {
	Source         ManagementExternalIntegrationSourceListRow
	Tokens         []ManagementExternalIntegrationSourcePrimaryTokenRow
	CreatedTokenID string
}

type ManagementExternalIntegrationSourceTokenCreator interface {
	CreateManagementExternalIntegrationSourceToken(
		ctx context.Context,
		input ManagementExternalIntegrationSourceTokenCreateInput,
	) (ManagementExternalIntegrationSourceTokenCreateResult, error)
}
