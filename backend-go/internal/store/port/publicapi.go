package port

import (
	"context"
	"errors"
	"time"
)

type PublicAPIRateLimitRule struct {
	WindowSeconds int
	MaxRequests   int
}

type PublicAPIAuthRecord struct {
	SourceRefID      string
	SourceName       string
	SourceStatus     string
	SourceScopes     []string
	SourceRateLimits []PublicAPIRateLimitRule
	SourceExpiresAt  *time.Time
	SourceLastUsedAt *time.Time

	TokenID         string
	TokenName       string
	TokenPrefix     string
	TokenStatus     string
	TokenScopes     []string
	TokenExpiresAt  *time.Time
	TokenLastUsedAt *time.Time
}

type PublicAPIAuthLastUsedTouch struct {
	SourceRefID string
	TokenID     string
	Now         time.Time
	TouchSource bool
	TouchToken  bool
}

type PublicAPIAuthStore interface {
	FindPublicAPIAuthTokenByHash(ctx context.Context, tokenHash string) (PublicAPIAuthRecord, bool, error)
	TouchPublicAPIAuthLastUsed(ctx context.Context, touch PublicAPIAuthLastUsedTouch) error
}

type PublicAPILogCaptureStatus string

const (
	PublicAPILogCaptureComplete  PublicAPILogCaptureStatus = "complete"
	PublicAPILogCaptureTruncated PublicAPILogCaptureStatus = "truncated"
	PublicAPILogCaptureEmpty     PublicAPILogCaptureStatus = "empty"
	PublicAPILogCaptureDropped   PublicAPILogCaptureStatus = "dropped"
)

type PublicAPILogInput struct {
	ID                    string
	TraceID               string
	SourceRefID           string
	SourceName            string
	TokenID               string
	TokenName             string
	TokenPrefix           string
	IsTestToken           bool
	Method                string
	Path                  string
	QueryString           string
	ClientIP              string
	UserAgent             string
	StatusCode            *int
	Success               bool
	DurationMs            *int64
	RequestSizeBytes      int64
	ResponseSizeBytes     int64
	RequestCaptureStatus  PublicAPILogCaptureStatus
	ResponseCaptureStatus PublicAPILogCaptureStatus
	RequestData           map[string]any
	ResponseData          map[string]any
	ErrorCode             string
	ErrorMessage          string
	StartedAt             time.Time
	EndedAt               time.Time
	CreatedAt             time.Time
}

type PublicAPILogStore interface {
	InsertPublicAPILog(ctx context.Context, input PublicAPILogInput) error
}

type PublicGroupTarget struct {
	ID          string
	Username    string
	DisplayName string
	Status      string
	Created     bool
}

type PublicGroupSummary struct {
	ID              string
	SystemAccountID string
	Name            string
	ProviderCode    string
	Description     *string
	Enabled         bool
	GroupType       string
	IsDefault       bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type PublicGroupListInput struct {
	SystemAccountID string
	ProviderCode    string
	Keyword         string
	Page            int
	PageSize        int
}

type PublicGroupListPage struct {
	Items          []PublicGroupSummary
	Page           int
	PageSize       int
	PageUpperBound int
	HasMore        bool
}

type PublicGroupCreateInput struct {
	ID              string
	SystemAccountID string
	Name            string
	ProviderCode    string
	Description     *string
	Enabled         bool
	GroupType       string
	Now             time.Time
}

type PublicGroupUpdateInput struct {
	ID              string
	SystemAccountID string
	Name            string
	ProviderCode    string
	Description     *string
	Enabled         bool
	GroupType       string
	Now             time.Time
}

type PublicGroupTargetCreateInput struct {
	ID           string
	Username     string
	DisplayName  string
	Description  string
	PasswordHash string
	Now          time.Time
}

var (
	ErrPublicGroupDuplicateName           = errors.New("public group duplicate name")
	ErrPublicGroupTargetDuplicateUsername = errors.New("public group target duplicate username")
)

type PublicGroupStore interface {
	FindPublicGroupTargetByUsername(ctx context.Context, username string) (PublicGroupTarget, bool, error)
	FindPublicGroupTargetByID(ctx context.Context, id string) (PublicGroupTarget, bool, error)
	CreatePublicGroupTarget(ctx context.Context, input PublicGroupTargetCreateInput) (PublicGroupTarget, error)
	ProviderEnabled(ctx context.Context, providerCode string) (bool, bool, error)
	ListPublicGroups(ctx context.Context, input PublicGroupListInput) (PublicGroupListPage, error)
	FindPublicGroupByID(ctx context.Context, groupID string) (PublicGroupSummary, bool, error)
	FindExistingPublicGroupByName(ctx context.Context, systemAccountID string, providerCode string, name string) (PublicGroupSummary, bool, error)
	CreatePublicGroup(ctx context.Context, input PublicGroupCreateInput) (PublicGroupSummary, error)
	UpdatePublicGroup(ctx context.Context, input PublicGroupUpdateInput) (PublicGroupSummary, bool, error)
	DeletePublicGroup(ctx context.Context, groupID string, systemAccountID string) (bool, error)
	PublicGroupAccountCount(ctx context.Context, groupID string) (int64, error)
	PublicGroupActiveRouteStrategyLossCount(ctx context.Context, groupID string) (int64, error)
}

type PublicGroupTransactor interface {
	PublicGroupInTx(ctx context.Context, fn func(ctx context.Context, store PublicGroupStore) error) error
}

type PublicRouteStrategyMode string

const (
	PublicRouteStrategyModeNormal      PublicRouteStrategyMode = "normal"
	PublicRouteStrategyModeHybridSmart PublicRouteStrategyMode = "hybrid_smart"
	PublicRouteStrategyModeWeighted    PublicRouteStrategyMode = "weighted"
	PublicRouteStrategyModeFailover    PublicRouteStrategyMode = "failover"
	PublicRouteStrategyModeRoundRobin  PublicRouteStrategyMode = "round_robin"
)

type PublicRouteStrategyStatus string

const (
	PublicRouteStrategyStatusActive   PublicRouteStrategyStatus = "active"
	PublicRouteStrategyStatusDisabled PublicRouteStrategyStatus = "disabled"
)

type PublicRouteStrategyNormalRoutingConfig struct {
	SchedulingPreference string
	SpeedFirstConfig     *PublicRouteStrategySpeedFirstConfig
}

type PublicRouteStrategySpeedFirstConfig struct {
	FirstByteThresholdMs          int
	SlowTriggerCount              int
	SlowWindowSeconds             int
	RecoverySuccessCount          int
	ProbeIntervalSeconds          int
	DegradedTTLSeconds            int
	MaxFirstByteRetriesPerRequest int
}

type PublicRouteStrategyGroupBindingSummary struct {
	ID           string
	GroupID      string
	GroupName    string
	ProviderCode string
	Priority     int
	Weight       int
	Status       PublicRouteStrategyStatus
	GroupEnabled bool
}

type PublicRouteStrategySummary struct {
	ID              string
	SystemAccountID string
	Name            string
	Description     *string
	Mode            PublicRouteStrategyMode
	Status          PublicRouteStrategyStatus
	IsDefault       bool
	ConfigJSON      *string
	GroupBindings   []PublicRouteStrategyGroupBindingSummary
	APIKeyCount     int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type PublicRouteStrategyListInput struct {
	SystemAccountID string
	Keyword         string
	Mode            string
	Status          string
	Page            int
	PageSize        int
}

type PublicRouteStrategyListPage struct {
	Items          []PublicRouteStrategySummary
	Page           int
	PageSize       int
	PageUpperBound int
	HasMore        bool
}

type PublicRouteStrategyBindableGroup struct {
	ID              string
	SystemAccountID string
	Name            string
	ProviderCode    string
	Enabled         bool
}

type PublicRouteStrategyGroupBindingCreateInput struct {
	ID       string
	GroupID  string
	Priority int
	Weight   int
	Status   PublicRouteStrategyStatus
}

type PublicRouteStrategyCreateInput struct {
	ID              string
	SystemAccountID string
	Name            string
	Description     *string
	Mode            PublicRouteStrategyMode
	Status          PublicRouteStrategyStatus
	ConfigJSON      *string
	Bindings        []PublicRouteStrategyGroupBindingCreateInput
	Now             time.Time
}

type PublicRouteStrategyUpdateInput struct {
	ID              string
	SystemAccountID string
	Name            string
	Description     *string
	Mode            PublicRouteStrategyMode
	Status          PublicRouteStrategyStatus
	ConfigJSON      *string
	Bindings        []PublicRouteStrategyGroupBindingCreateInput
	Now             time.Time
}

var ErrPublicRouteStrategyDuplicateName = errors.New("public route strategy duplicate name")

type PublicRouteStrategyStore interface {
	FindPublicRouteStrategyTargetByUsername(ctx context.Context, username string) (PublicGroupTarget, bool, error)
	FindPublicRouteStrategyTargetByID(ctx context.Context, id string) (PublicGroupTarget, bool, error)
	ListPublicRouteStrategies(ctx context.Context, input PublicRouteStrategyListInput) (PublicRouteStrategyListPage, error)
	FindPublicRouteStrategyByID(ctx context.Context, routeStrategyID string) (PublicRouteStrategySummary, bool, error)
	FindPublicRouteStrategyBindableGroups(ctx context.Context, systemAccountID string, groupIDs []string) ([]PublicRouteStrategyBindableGroup, error)
	CreatePublicRouteStrategy(ctx context.Context, input PublicRouteStrategyCreateInput) (PublicRouteStrategySummary, error)
	UpdatePublicRouteStrategy(ctx context.Context, input PublicRouteStrategyUpdateInput) (PublicRouteStrategySummary, bool, error)
	DeletePublicRouteStrategy(ctx context.Context, routeStrategyID string, systemAccountID string) (bool, error)
	PublicRouteStrategyAPIKeyCount(ctx context.Context, routeStrategyID string, systemAccountID string) (int64, error)
}

type PublicRouteStrategyTransactor interface {
	PublicRouteStrategyInTx(ctx context.Context, fn func(ctx context.Context, store PublicRouteStrategyStore) error) error
}

type PublicAPIKeyStatus string

const (
	PublicAPIKeyStatusActive   PublicAPIKeyStatus = "active"
	PublicAPIKeyStatusDisabled PublicAPIKeyStatus = "disabled"
)

type PublicAPIKeySummary struct {
	ID                              string
	SystemAccountID                 string
	Name                            string
	Description                     *string
	RouteStrategyID                 string
	RouteStrategyName               string
	RouteStrategyMode               PublicRouteStrategyMode
	RouteStrategyStatus             PublicRouteStrategyStatus
	Status                          PublicAPIKeyStatus
	IsDefault                       bool
	KeyPrefix                       string
	KeySuffix                       string
	ExpiresAt                       *time.Time
	QuotaLimitsJSON                 *string
	AvailabilityScheduleJSON        *string
	AvailabilityScheduleNextCheckAt *time.Time
	LastUsedAt                      *time.Time
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
}

type PublicAPIKeyRouteStrategyRef struct {
	ID              string
	SystemAccountID string
	Name            string
	Mode            PublicRouteStrategyMode
	Status          PublicRouteStrategyStatus
}

type PublicAPIKeyListInput struct {
	SystemAccountID string
	RouteStrategyID string
	Keyword         string
	Status          string
	Page            int
	PageSize        int
}

type PublicAPIKeyListPage struct {
	Items          []PublicAPIKeySummary
	Page           int
	PageSize       int
	PageUpperBound int
	HasMore        bool
}

type PublicAPIKeyCreateInput struct {
	ID                              string
	SystemAccountID                 string
	RouteStrategyID                 string
	Name                            string
	Description                     *string
	KeyHash                         string
	KeyPrefix                       string
	KeySuffix                       string
	Status                          PublicAPIKeyStatus
	ExpiresAt                       *time.Time
	QuotaLimitsJSON                 *string
	AvailabilityScheduleJSON        *string
	AvailabilityScheduleNextCheckAt *time.Time
	Now                             time.Time
}

type PublicAPIKeyUpdateInput struct {
	ID                              string
	SystemAccountID                 string
	RouteStrategyID                 string
	Name                            string
	Description                     *string
	Status                          PublicAPIKeyStatus
	ExpiresAt                       *time.Time
	QuotaLimitsJSON                 *string
	AvailabilityScheduleJSON        *string
	AvailabilityScheduleNextCheckAt *time.Time
	Now                             time.Time
}

var (
	ErrPublicAPIKeyDuplicateName = errors.New("public api key duplicate name")
	ErrPublicAPIKeyDuplicateHash = errors.New("public api key duplicate hash")
)

type PublicAPIKeyStore interface {
	FindPublicAPIKeyTargetByUsername(ctx context.Context, username string) (PublicGroupTarget, bool, error)
	FindPublicAPIKeyTargetByID(ctx context.Context, id string) (PublicGroupTarget, bool, error)
	ListPublicAPIKeys(ctx context.Context, input PublicAPIKeyListInput) (PublicAPIKeyListPage, error)
	FindPublicAPIKeyByID(ctx context.Context, apiKeyID string) (PublicAPIKeySummary, bool, error)
	FindPublicAPIKeyRouteStrategy(ctx context.Context, systemAccountID string, routeStrategyID string) (PublicAPIKeyRouteStrategyRef, bool, error)
	CreatePublicAPIKey(ctx context.Context, input PublicAPIKeyCreateInput) (PublicAPIKeySummary, error)
	UpdatePublicAPIKey(ctx context.Context, input PublicAPIKeyUpdateInput) (PublicAPIKeySummary, bool, error)
	DeletePublicAPIKey(ctx context.Context, apiKeyID string, systemAccountID string) (bool, error)
}

type PublicAPIKeyTransactor interface {
	PublicAPIKeyInTx(ctx context.Context, fn func(ctx context.Context, store PublicAPIKeyStore) error) error
}

type PublicAccountStatus string

const (
	PublicAccountStatusActive               PublicAccountStatus = "active"
	PublicAccountStatusPendingTest          PublicAccountStatus = "pending_test"
	PublicAccountStatusDisabled             PublicAccountStatus = "disabled"
	PublicAccountStatusError                PublicAccountStatus = "error"
	PublicAccountStatusRateLimited          PublicAccountStatus = "rate_limited"
	PublicAccountStatusTemporaryUnavailable PublicAccountStatus = "temporary_unavailable"
)

type PublicAccountProviderProfile struct {
	ID                      string
	ProviderCode            string
	Name                    string
	Enabled                 bool
	ProviderEnabled         bool
	ProtocolCode            string
	ProtocolVersion         string
	AccountTypesJSON        string
	DefaultSupportedModels  []string
	DefaultHealthCheckModel string
}

type PublicAccountGroupRef struct {
	ID              string
	SystemAccountID string
	Name            string
	ProviderCode    string
	Enabled         bool
	GroupType       string
	Created         bool
}

type PublicAccountSummary struct {
	ID                        string
	SystemAccountID           string
	Name                      string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Type                      string
	Status                    PublicAccountStatus
	CredentialsEncrypted      string
	CredentialFingerprint     *string
	CredentialMask            string
	ClientCompatibility       string
	SupportedModels           []string
	HealthCheckModel          string
	BoundGroupID              *string
	BoundGroupName            *string
	Schedulable               bool
	AvailabilityScheduleJSON  *string
	ConcurrencyLimit          int
	Priority                  int
	Notes                     *string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type PublicAccountListInput struct {
	SystemAccountID           string
	ProviderCode              string
	ProviderProtocolProfileID string
	GroupID                   string
	Keyword                   string
	Type                      string
	Status                    string
	Schedulable               string
	Page                      int
	PageSize                  int
}

type PublicAccountListPage struct {
	Items          []PublicAccountSummary
	Page           int
	PageSize       int
	PageUpperBound int
	HasMore        bool
}

type PublicAccountNameLookupInput struct {
	SystemAccountID           string
	ProviderCode              string
	ProviderProtocolProfileID string
	GroupID                   string
	Name                      string
}

type PublicAccountCreateInput struct {
	ID                        string
	SystemAccountID           string
	GroupID                   string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Name                      string
	Type                      string
	Status                    PublicAccountStatus
	CredentialsEncrypted      string
	CredentialFingerprint     *string
	CredentialMask            string
	ClientCompatibility       string
	SupportedModels           []string
	HealthCheckModel          string
	Schedulable               bool
	AvailabilityScheduleJSON  *string
	ConcurrencyLimit          int
	Priority                  int
	Notes                     *string
	Now                       time.Time
}

type PublicAccountUpdateInput struct {
	ID                       string
	SystemAccountID          string
	ProviderCode             string
	Name                     string
	Status                   PublicAccountStatus
	CredentialsEncrypted     string
	CredentialFingerprint    *string
	CredentialMask           string
	SupportedModels          []string
	SupportedModelsChanged   bool
	Schedulable              bool
	AvailabilityScheduleJSON *string
	ConcurrencyLimit         int
	Priority                 int
	Notes                    *string
	Now                      time.Time
}

var ErrPublicAccountDuplicateName = errors.New("public account duplicate name")

type PublicAccountStore interface {
	FindPublicAccountTargetByUsername(ctx context.Context, username string) (PublicGroupTarget, bool, error)
	FindPublicAccountTargetByID(ctx context.Context, id string) (PublicGroupTarget, bool, error)
	CreatePublicAccountTarget(ctx context.Context, input PublicGroupTargetCreateInput) (PublicGroupTarget, error)
	FindPublicAccountProviderProfile(ctx context.Context, systemAccountID string, providerCode string, profileID string) (PublicAccountProviderProfile, bool, error)
	FindExistingPublicAccountGroupByName(ctx context.Context, systemAccountID string, providerCode string, name string) (PublicAccountGroupRef, bool, error)
	CreatePublicAccountGroup(ctx context.Context, input PublicGroupCreateInput) (PublicAccountGroupRef, error)
	FindPublicAccountGroupByID(ctx context.Context, groupID string) (PublicAccountGroupRef, bool, error)
	ListPublicAccounts(ctx context.Context, input PublicAccountListInput) (PublicAccountListPage, error)
	FindPublicAccountByID(ctx context.Context, accountID string) (PublicAccountSummary, bool, error)
	FindExistingPublicAccountByNameInGroup(ctx context.Context, input PublicAccountNameLookupInput) (PublicAccountSummary, bool, error)
	CreatePublicAccount(ctx context.Context, input PublicAccountCreateInput) (PublicAccountSummary, error)
	UpdatePublicAccount(ctx context.Context, input PublicAccountUpdateInput) (PublicAccountSummary, bool, error)
	DeletePublicAccount(ctx context.Context, accountID string, systemAccountID string, deletedBy string, now time.Time) (bool, error)
}

type PublicAccountTransactor interface {
	PublicAccountInTx(ctx context.Context, fn func(ctx context.Context, store PublicAccountStore) error) error
}
