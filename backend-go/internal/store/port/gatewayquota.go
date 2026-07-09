package port

import (
	"context"
	"time"
)

type GatewayQuotaCosts struct {
	Hourly  float64 `json:"hourly"`
	Daily   float64 `json:"daily"`
	Weekly  float64 `json:"weekly"`
	Monthly float64 `json:"monthly"`
	Total   float64 `json:"total"`
}

type GatewayQuotaCostLookupInput struct {
	Key               string
	SystemAccountID   string
	ScopeType         string
	ScopeID           string
	StatDate          string
	StatWeek          string
	StatMonth         string
	HourlyWindowHours int
}

type GatewayQuotaSnapshotAPIKeyRow struct {
	ID              string
	SystemAccountID string
	Limits          ManagementRequestQuotaLimits
}

type GatewayQuotaSnapshotAuthorizationRow struct {
	ID                           string
	ResourceOwnerSystemAccountID string
	GranteeSystemAccountID       string
	ResourceType                 string
	ResourceID                   string
	EffectiveSourceTeamID        string
	Limits                       ManagementRequestQuotaLimits
}

type GatewayQuotaSnapshotTeamAuthorizationRow struct {
	AuthorizationID                     string
	ResourceOwnerSystemAccountID        string
	AuthorizationGranteeSystemAccountID string
	ResourceType                        string
	ResourceID                          string
	AuthorizationInstanceAccountID      string
	EffectiveSourceTeamID               string
	Limits                              ManagementRequestQuotaLimits
}

type GatewayQuotaSnapshotRows[T any] struct {
	Rows     []T
	Complete bool
}

type GatewayQuotaSnapshotReader interface {
	ListGatewayQuotaSnapshotAPIKeys(ctx context.Context, limit int) (GatewayQuotaSnapshotRows[GatewayQuotaSnapshotAPIKeyRow], error)
	ListGatewayQuotaSnapshotAuthorizations(ctx context.Context, limit int) (GatewayQuotaSnapshotRows[GatewayQuotaSnapshotAuthorizationRow], error)
	ListGatewayQuotaSnapshotTeamAuthorizations(ctx context.Context, limit int) (GatewayQuotaSnapshotRows[GatewayQuotaSnapshotTeamAuthorizationRow], error)
	LoadGatewayQuotaSnapshotCosts(ctx context.Context, inputs []GatewayQuotaCostLookupInput) (map[string]GatewayQuotaCosts, error)
}

type GatewayQuotaSnapshotBuildRecord struct {
	GeneratedAt                  time.Time
	CostEntries                  int
	AuthorizationEntries         int
	CostEntriesComplete          bool
	AuthorizationEntriesComplete bool
}
