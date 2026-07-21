package port

import (
	"context"
	"time"
)

type ManagementExternalIntegrationSourceListInput struct {
	Status  string
	Keyword string
	Limit   int
	Offset  int
}

type ManagementExternalIntegrationSourceListRow struct {
	ID             string
	Name           string
	Status         string
	ScopesJSON     string
	RateLimitsJSON string
	ExpiresAt      *time.Time
	Notes          *string
	LastUsedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type ManagementExternalIntegrationSourceTokenStatsRow struct {
	SourceRefID      string
	TokenCount       int64
	ActiveTokenCount int64
}

type ManagementExternalIntegrationSourcePrimaryTokenRow struct {
	SourceRefID string
	ID          string
	Name        string
	TokenPrefix string
	TokenSuffix string
	Status      string
	ScopesJSON  string
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	RevokedAt   *time.Time
}

type ManagementExternalIntegrationSourceListReader interface {
	ListManagementExternalIntegrationSources(
		ctx context.Context,
		input ManagementExternalIntegrationSourceListInput,
	) ([]ManagementExternalIntegrationSourceListRow, error)
	ListManagementExternalIntegrationSourcePrimaryTokens(
		ctx context.Context,
		sourceIDs []string,
	) ([]ManagementExternalIntegrationSourcePrimaryTokenRow, error)
}

type ManagementExternalIntegrationSourceDetailReader interface {
	FindManagementExternalIntegrationSource(
		ctx context.Context,
		sourceID string,
	) (ManagementExternalIntegrationSourceListRow, bool, error)
	ListManagementExternalIntegrationSourceTokens(
		ctx context.Context,
		sourceID string,
	) ([]ManagementExternalIntegrationSourcePrimaryTokenRow, error)
}

type ManagementExternalIntegrationSourceTokenSecretReader interface {
	FindManagementExternalIntegrationSourceTokenSecret(
		ctx context.Context,
		sourceID string,
		tokenID string,
	) (encrypted string, found bool, err error)
}
