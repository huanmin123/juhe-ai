package port

import (
	"context"
	"errors"
	"time"
)

var (
	ErrManagementExternalIntegrationSourceNotFound                = errors.New("management external integration source not found")
	ErrManagementExternalIntegrationSourceBuiltInUpdateRestricted = errors.New("management external integration source built-in update restricted")
	ErrManagementExternalIntegrationSourceNameExists              = errors.New("management external integration source name exists")
)

type ManagementExternalIntegrationSourceUpdateInput struct {
	SourceID       string
	HasName        bool
	Name           string
	HasStatus      bool
	Status         string
	HasScopes      bool
	ScopesJSON     string
	HasRateLimits  bool
	RateLimitsJSON string
	HasExpiresAt   bool
	ExpiresAt      *time.Time
	HasNotes       bool
	Notes          *string
	UpdatedAt      time.Time
}

type ManagementExternalIntegrationSourceUpdateResult struct {
	BeforeSource ManagementExternalIntegrationSourceListRow
	BeforeTokens []ManagementExternalIntegrationSourcePrimaryTokenRow
	AfterSource  ManagementExternalIntegrationSourceListRow
	AfterTokens  []ManagementExternalIntegrationSourcePrimaryTokenRow
}

type ManagementExternalIntegrationSourceUpdater interface {
	UpdateManagementExternalIntegrationSource(
		ctx context.Context,
		input ManagementExternalIntegrationSourceUpdateInput,
		validate func(ManagementExternalIntegrationSourceUpdateResult) error,
	) (ManagementExternalIntegrationSourceUpdateResult, error)
}
