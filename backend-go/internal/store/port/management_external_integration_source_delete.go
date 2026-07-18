package port

import (
	"context"
	"errors"
)

var ErrManagementExternalIntegrationSourceBuiltInDeleteRestricted = errors.New(
	"management external integration source built-in delete restricted",
)

type ManagementExternalIntegrationSourceDeleteResult struct {
	SourceID   string
	SourceName string
	TokenCount int64
}

type ManagementExternalIntegrationSourceDeleter interface {
	DeleteManagementExternalIntegrationSource(
		ctx context.Context,
		sourceID string,
	) (ManagementExternalIntegrationSourceDeleteResult, error)
}
