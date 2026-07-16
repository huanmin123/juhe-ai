package port

import (
	"context"
	"errors"
	"time"
)

var (
	ErrManagementExternalIntegrationSourceTokenNotFound = errors.New(
		"management external integration source token not found",
	)
	ErrManagementExternalIntegrationSourceBuiltInTokenUpdateRestricted = errors.New(
		"management external integration source built-in token update restricted",
	)
)

type ManagementExternalIntegrationSourceTokenUpdateInput struct {
	SourceID     string
	TokenID      string
	HasName      bool
	Name         string
	HasStatus    bool
	Status       string
	HasScopes    bool
	ScopesJSON   string
	HasExpiresAt bool
	ExpiresAt    *time.Time
	UpdatedAt    time.Time
}

type ManagementExternalIntegrationSourceTokenUpdateResult struct {
	BeforeToken ManagementExternalIntegrationSourcePrimaryTokenRow
	AfterToken  ManagementExternalIntegrationSourcePrimaryTokenRow
}

type ManagementExternalIntegrationSourceTokenUpdater interface {
	UpdateManagementExternalIntegrationSourceToken(
		ctx context.Context,
		input ManagementExternalIntegrationSourceTokenUpdateInput,
		validate func(ManagementExternalIntegrationSourceTokenUpdateResult) error,
	) (ManagementExternalIntegrationSourceTokenUpdateResult, error)
}
