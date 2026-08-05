package port

import (
	"context"
	"time"
)

type GatewayPreflightAPIKeyRecord struct {
	ID                                  string
	SystemAccountID                     string
	APIKeyStatus                        string
	ExpiresAt                           *time.Time
	QuotaLimits                         ManagementRequestQuotaLimits
	SystemAccountStatus                 string
	SystemAccountImageGenerationEnabled bool
	RouteStrategyID                     string
	RouteStrategyStatus                 string
	RouteStrategyMode                   string
	RouteStrategyConfigJSON             *string
	RouteDispatchGeneration             int64
}

type GatewayPreflightBindingRecord struct {
	ID              string
	APIKeyID        string
	SystemAccountID string
	GroupID         string
	Priority        int
	Weight          int
	Status          string
	ProviderCode    string
	GroupEnabled    bool
	AccessExpiresAt *time.Time
	CreatedAt       time.Time
}

type GatewayPreflightSettingsRecord struct {
	GatewayTextRawBodyLimitMegabytes           int
	DefaultTemporaryUnschedulableMinutes       int
	TemporaryUnschedulableRetryIntervalSeconds int
	TemporaryUnschedulableRetryAttempts        int
	TextFirstResponseTimeoutSeconds            int
	TextStreamIdleTimeoutSeconds               int
	TextUncommittedAttemptMaxLifetimeSeconds   int
	ImageFirstResponseTimeoutSeconds           int
	ImageStreamIdleTimeoutSeconds              int
	ImageUncommittedAttemptMaxLifetimeSeconds  int
	NoAvailableAccountWaitTimeoutSeconds       int
	StreamFailureThresholdCount                int
	StreamFailureThresholdWindowMinutes        int
}

type GatewayPreflightQuotaCostEntry struct {
	SystemAccountID   string
	ScopeType         string
	ScopeID           string
	HourlyWindowHours int
	Costs             GatewayQuotaCosts
}

// GatewayAuthorizationQuotaSnapshotEntry is the per-authorization decision
// published by the quota snapshot worker. It is shared by API-key preflight
// and cross-group fallback selection; neither path may infer a missing
// decision from an incomplete snapshot.
type GatewayAuthorizationQuotaSnapshotEntry struct {
	ScopeType       string
	AuthorizationID string
	Allowed         bool
}

type GatewayPreflightQuotaSnapshot struct {
	GeneratedAt                  string
	CostEntries                  []GatewayPreflightQuotaCostEntry
	AuthorizationEntries         []GatewayAuthorizationQuotaSnapshotEntry
	CostEntriesComplete          bool
	AuthorizationEntriesComplete bool
}

type GatewayPreflightReader interface {
	LoadGatewayPreflightAPIKey(ctx context.Context, keyHash string) (GatewayPreflightAPIKeyRecord, bool, error)
	ListGatewayPreflightBindings(ctx context.Context, apiKeyID string, routeStrategyID string, systemAccountID string, now time.Time, limit int) ([]GatewayPreflightBindingRecord, error)
	LoadGatewayPreflightSettings(ctx context.Context) (GatewayPreflightSettingsRecord, error)
}

type GatewayPreflightQuotaSnapshotReader interface {
	LoadGatewayPreflightQuotaSnapshotCurrent(ctx context.Context) (GatewayPreflightQuotaSnapshot, bool, error)
}
