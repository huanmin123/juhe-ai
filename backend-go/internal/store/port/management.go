package port

import (
	"context"
	"errors"
	"time"
)

type ManagementSessionAccount struct {
	SessionID          string
	TokenHash          string
	ExpiresAt          time.Time
	LastSeenAt         time.Time
	AccountID          string
	Username           string
	DisplayName        string
	Role               string
	Status             string
	MustChangePassword bool
}

type ManagementSessionReader interface {
	FindManagementSessionByTokenHash(ctx context.Context, tokenHash string) (ManagementSessionAccount, bool, error)
}

type ManagementSessionRevoker interface {
	RevokeManagementSessionByTokenHash(ctx context.Context, tokenHash string) error
}

type ManagementSessionTouchInput struct {
	SessionID string
	TouchedAt time.Time
	Cutoff    time.Time
}

type ManagementSessionToucher interface {
	TouchManagementSession(ctx context.Context, input ManagementSessionTouchInput) error
}

type ManagementCurrentUserProfile struct {
	ID                 string
	Username           string
	DisplayName        string
	Role               string
	MustChangePassword bool
}

type ManagementCurrentUserProfileUpdateInput struct {
	SystemAccountID string
	DisplayName     string
	UpdatedAt       time.Time
}

type ManagementCurrentUserProfileUpdateResult struct {
	Before  ManagementCurrentUserProfile
	Account ManagementCurrentUserProfile
}

var ErrManagementProfileDisplayNameExists = errors.New("management profile display name exists")
var ErrManagementSystemAccountDisplayNameExists = errors.New("management system account display name exists")
var ErrManagementSystemAccountUsernameExists = errors.New("management system account username exists")

type ManagementCurrentUserProfileWriter interface {
	UpdateManagementCurrentUserProfile(ctx context.Context, input ManagementCurrentUserProfileUpdateInput) (ManagementCurrentUserProfileUpdateResult, bool, error)
}

type ManagementSystemAccountPasswordCredential struct {
	ID           string
	Username     string
	Status       string
	PasswordHash string
}

type ManagementCurrentUserPasswordUpdateInput struct {
	SystemAccountID string
	PasswordHash    string
	UpdatedAt       time.Time
}

type ManagementCurrentUserPasswordChanger interface {
	FindManagementSystemAccountPasswordByUsername(ctx context.Context, username string) (ManagementSystemAccountPasswordCredential, bool, error)
	UpdateManagementCurrentUserPassword(ctx context.Context, input ManagementCurrentUserPasswordUpdateInput) (ManagementSystemAccountSummary, bool, error)
	RevokeOtherManagementSessionsForAccount(ctx context.Context, systemAccountID string, keepSessionID string) error
}

type ManagementLoginSessionInput struct {
	SystemAccountID      string
	VerifiedPasswordHash string
	SessionID            string
	TokenHash            string
	LoggedInAt           time.Time
	ExpiresAt            time.Time
}

type ManagementLoginSessionResult struct {
	Account          ManagementSystemAccountSummary
	SessionID        string
	SessionExpiresAt time.Time
}

type ManagementLoginStore interface {
	FindManagementSystemAccountPasswordByUsername(ctx context.Context, username string) (ManagementSystemAccountPasswordCredential, bool, error)
	CompleteManagementLogin(ctx context.Context, input ManagementLoginSessionInput) (ManagementLoginSessionResult, bool, error)
}

type ManagementProxyOption struct {
	ID      string
	Name    string
	Type    string
	Enabled bool
}

type ManagementProxySummary struct {
	ID                string
	SystemAccountID   string
	Name              string
	Description       *string
	Type              string
	Host              string
	Port              int
	Username          *string
	PasswordEncrypted *string
	Enabled           bool
	TestStatus        string
	LatencyMs         *int
	OutboundIP        *string
	OutboundRegion    *string
	LastTestMessage   *string
	LastTestedAt      *time.Time
}

type ManagementProxyAccountBinding struct {
	ID   string
	Name string
}

type ManagementProxyListInput struct {
	Keyword string
	Limit   int
	Offset  int
}

type ManagementProxyListResult struct {
	Items   []ManagementProxySummary
	HasMore bool
}

type ManagementProxyOptionListInput struct {
	Keyword string
	Limit   int
}

type ManagementProxyCreateInput struct {
	ID                string
	SystemAccountID   string
	Name              string
	Description       *string
	Type              string
	Host              string
	Port              int
	Username          *string
	PasswordEncrypted *string
	Enabled           bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ManagementProxyUpdateInput struct {
	ID                          string
	Name                        *string
	Description                 ManagementProxyNullableTextPatch
	Type                        *string
	Host                        *string
	Port                        *int
	Username                    ManagementProxyNullableTextPatch
	PasswordEncrypted           *string
	PasswordEncryptedWasChanged bool
	Enabled                     *bool
	UpdatedAt                   time.Time
}

type ManagementProxyNullableTextPatch struct {
	Set   bool
	Value *string
}

type ManagementProxyTestStateInput struct {
	ID              string
	TestStatus      string
	LatencyMs       *int
	OutboundIP      ManagementProxyNullableTextPatch
	OutboundRegion  ManagementProxyNullableTextPatch
	LastTestMessage string
	LastTestedAt    time.Time
	UpdatedAt       time.Time
}

type ManagementProxyUpdateResult struct {
	Before         ManagementProxySummary
	Proxy          ManagementProxySummary
	ResetTestState bool
}

type ManagementProxyAccountBindingListInput struct {
	ProxyID string
	Limit   int
}

type ManagementProxyOptionReader interface {
	ListManagementProxyOptions(ctx context.Context, input ManagementProxyOptionListInput) ([]ManagementProxyOption, error)
}

type ManagementProxyReader interface {
	ManagementProxyOptionReader
	ListManagementProxies(ctx context.Context, input ManagementProxyListInput) (ManagementProxyListResult, error)
}

var ErrManagementProxyNameExists = errors.New("management proxy name exists")
var ErrManagementProxyInUse = errors.New("management proxy in use")

type ManagementProxyWriter interface {
	ManagementProxyReader
	FindManagementProxy(ctx context.Context, id string) (ManagementProxySummary, bool, error)
	ListManagementProxyAccountBindings(ctx context.Context, input ManagementProxyAccountBindingListInput) ([]ManagementProxyAccountBinding, error)
	CreateManagementProxy(ctx context.Context, input ManagementProxyCreateInput) (ManagementProxySummary, error)
	UpdateManagementProxy(ctx context.Context, input ManagementProxyUpdateInput) (ManagementProxyUpdateResult, bool, error)
	UpdateManagementProxyTestState(ctx context.Context, input ManagementProxyTestStateInput) (ManagementProxySummary, bool, error)
	DeleteManagementProxy(ctx context.Context, id string) (bool, error)
}

type ManagementSystemAccountOption struct {
	ID          string
	Username    string
	DisplayName string
	Status      string
}

type ManagementSystemAccountSummary struct {
	ID                     string
	Username               string
	DisplayName            string
	Description            string
	Role                   string
	Status                 string
	MustChangePassword     bool
	ImageGenerationEnabled bool
	LastLoginAt            *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type ManagementSystemAccountListInput struct {
	Keyword string
	Limit   int
	Offset  int
}

type ManagementSystemAccountListResult struct {
	Items   []ManagementSystemAccountSummary
	HasMore bool
}

type ManagementSystemAccountOptionListInput struct {
	IDs     []string
	Keyword string
	Limit   int
}

type ManagementSystemAccountOptionReader interface {
	ListManagementSystemAccounts(ctx context.Context, input ManagementSystemAccountListInput) (ManagementSystemAccountListResult, error)
	ListManagementSystemAccountOptions(ctx context.Context, input ManagementSystemAccountOptionListInput) ([]ManagementSystemAccountOption, error)
}

type ManagementSystemAccountPasswordResetInput struct {
	SystemAccountID       string
	PasswordHash          string
	HasMustChangePassword bool
	MustChangePassword    bool
	UpdatedAt             time.Time
}

type ManagementSystemAccountPasswordResetResult struct {
	Before              ManagementSystemAccountSummary
	Account             ManagementSystemAccountSummary
	RevokedSessionCount int
}

type ManagementSystemAccountPasswordResetter interface {
	ResetManagementSystemAccountPassword(ctx context.Context, input ManagementSystemAccountPasswordResetInput) (ManagementSystemAccountPasswordResetResult, bool, error)
}

type ManagementSystemAccountStatusUpdateInput struct {
	SystemAccountID string
	Status          string
	UpdatedAt       time.Time
}

type ManagementSystemAccountStatusUpdateResult struct {
	Before                      ManagementSystemAccountSummary
	Account                     ManagementSystemAccountSummary
	RevokedSessionCount         int
	BlockedLastActiveSuperAdmin bool
}

type ManagementSystemAccountStatusUpdater interface {
	UpdateManagementSystemAccountStatus(ctx context.Context, input ManagementSystemAccountStatusUpdateInput) (ManagementSystemAccountStatusUpdateResult, bool, error)
}

type ManagementSystemAccountImageGenerationUpdateInput struct {
	SystemAccountID        string
	ImageGenerationEnabled bool
	UpdatedAt              time.Time
}

type ManagementSystemAccountImageGenerationUpdateResult struct {
	Before  ManagementSystemAccountSummary
	Account ManagementSystemAccountSummary
}

type ManagementSystemAccountImageGenerationUpdater interface {
	UpdateManagementSystemAccountImageGeneration(ctx context.Context, input ManagementSystemAccountImageGenerationUpdateInput) (ManagementSystemAccountImageGenerationUpdateResult, bool, error)
}

type ManagementSystemAccountProfileUpdateInput struct {
	SystemAccountID       string
	HasDisplayName        bool
	DisplayName           string
	HasDescription        bool
	Description           *string
	HasRole               bool
	Role                  string
	HasMustChangePassword bool
	MustChangePassword    bool
	UpdatedAt             time.Time
}

type ManagementSystemAccountProfileUpdateResult struct {
	Before                      ManagementSystemAccountSummary
	Account                     ManagementSystemAccountSummary
	BlockedLastActiveSuperAdmin bool
}

type ManagementSystemAccountProfileUpdater interface {
	UpdateManagementSystemAccountProfile(ctx context.Context, input ManagementSystemAccountProfileUpdateInput) (ManagementSystemAccountProfileUpdateResult, bool, error)
}

type ManagementSystemAccountUpdateInput struct {
	SystemAccountID           string
	HasDisplayName            bool
	DisplayName               string
	HasDescription            bool
	Description               *string
	HasPassword               bool
	PasswordHash              string
	HasRole                   bool
	Role                      string
	HasStatus                 bool
	Status                    string
	HasMustChangePassword     bool
	MustChangePassword        bool
	HasImageGenerationEnabled bool
	ImageGenerationEnabled    bool
	UpdatedAt                 time.Time
}

type ManagementSystemAccountUpdateResult struct {
	Before                      ManagementSystemAccountSummary
	Account                     ManagementSystemAccountSummary
	RevokedSessionCount         int
	BlockedLastActiveSuperAdmin bool
}

type ManagementSystemAccountUpdater interface {
	UpdateManagementSystemAccount(ctx context.Context, input ManagementSystemAccountUpdateInput) (ManagementSystemAccountUpdateResult, bool, error)
}

type ManagementSystemAccountCreateInput struct {
	ID                     string
	Username               string
	DisplayName            string
	Description            *string
	Role                   string
	Status                 string
	PasswordHash           string
	MustChangePassword     bool
	ImageGenerationEnabled bool
	DefaultAPIKeys         []ManagementDefaultAPIKeyCreateInput
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type ManagementSystemAccountCreateResult struct {
	Account          ManagementSystemAccountSummary
	DefaultGroupIDs  []string
	DefaultAPIKeyIDs []string
}

type ManagementDefaultAPIKeyCreateInput struct {
	ID                 string
	KeyHash            string
	KeyPrefix          string
	KeySuffix          string
	KeySecretEncrypted string
}

type ManagementSystemAccountCreator interface {
	CreateManagementSystemAccount(ctx context.Context, input ManagementSystemAccountCreateInput) (ManagementSystemAccountCreateResult, error)
}

type ManagementAuthorizationGranteeAccountOption struct {
	ID          string
	Username    string
	DisplayName string
	Status      string
}

type ManagementAuthorizationGranteeTeamOption struct {
	ID     string
	Name   string
	Status string
}

type ManagementSystemTeamSummary struct {
	ID                string
	Name              string
	Description       string
	Status            string
	MemberCount       int
	ActiveMemberCount int
	CreatedBy         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ManagementSystemTeamMemberSummary struct {
	ID                string
	TeamID            string
	SystemAccountID   string
	SystemAccountName string
	Username          string
	MemberRole        string
	Status            string
	JoinedAt          time.Time
	RemovedAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ManagementSystemTeamDetail struct {
	ManagementSystemTeamSummary
	Members []ManagementSystemTeamMemberSummary
}

type ManagementSystemTeamListInput struct {
	SystemAccountID string
	Keyword         string
	Limit           int
	Offset          int
}

type ManagementSystemTeamListResult struct {
	Items   []ManagementSystemTeamSummary
	HasMore bool
}

type ManagementSystemTeamCreateInput struct {
	ID          string
	Name        string
	Description *string
	Status      string
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

var ErrManagementSystemTeamNameExists = errors.New("management system team name exists")

type ManagementSystemTeamCreator interface {
	CreateManagementSystemTeam(ctx context.Context, input ManagementSystemTeamCreateInput) (ManagementSystemTeamSummary, error)
}

type ManagementSystemTeamUpdateInput struct {
	TeamID          string
	SystemAccountID string
	HasName         bool
	Name            string
	HasDescription  bool
	Description     *string
	HasStatus       bool
	Status          string
	UpdatedBy       string
	UpdatedAt       time.Time
}

type ManagementSystemTeamUpdateResult struct {
	Before               ManagementSystemTeamSummary
	Team                 ManagementSystemTeamDetail
	AuthorizationChanged bool
}

type ManagementSystemTeamUpdater interface {
	UpdateManagementSystemTeam(ctx context.Context, input ManagementSystemTeamUpdateInput) (ManagementSystemTeamUpdateResult, bool, error)
}

type ManagementSystemTeamMemberAddInput struct {
	TeamID           string
	SystemAccountID  string
	SystemAccountIDs []string
	CreatedBy        string
	UpdatedAt        time.Time
}

type ManagementSystemTeamMemberAddResult struct {
	Before ManagementSystemTeamDetail
	Team   ManagementSystemTeamDetail
}

type ManagementSystemTeamMemberRemoveInput struct {
	TeamID          string
	MemberID        string
	SystemAccountID string
	UpdatedBy       string
	UpdatedAt       time.Time
}

type ManagementSystemTeamMemberRemoveResult struct {
	Before        ManagementSystemTeamDetail
	Team          ManagementSystemTeamDetail
	RemovedMember ManagementSystemTeamMemberSummary
}

type ManagementSystemTeamMemberManager interface {
	AddManagementSystemTeamMembers(ctx context.Context, input ManagementSystemTeamMemberAddInput) (ManagementSystemTeamMemberAddResult, bool, error)
	RemoveManagementSystemTeamMember(ctx context.Context, input ManagementSystemTeamMemberRemoveInput) (ManagementSystemTeamMemberRemoveResult, bool, error)
}

type ManagementSystemTeamReader interface {
	ListManagementSystemTeams(ctx context.Context, input ManagementSystemTeamListInput) (ManagementSystemTeamListResult, error)
	FindManagementSystemTeam(ctx context.Context, teamID string, systemAccountID string) (ManagementSystemTeamDetail, bool, error)
}

type ManagementAuthorizationPrincipalOptionListInput struct {
	IDs     []string
	Keyword string
	Limit   int
}

type ManagementAuthorizationGranteeGroupOption struct {
	ID   string
	Name string
}

type ManagementAuthorizationGranteeGroupOptionListInput struct {
	GranteeSystemAccountID string
	IDs                    []string
	Keyword                string
	ProviderCode           string
	Limit                  int
	PreferDefault          bool
}

type ManagementAuthorizationOptionReader interface {
	ListManagementAuthorizationGranteeAccounts(ctx context.Context, input ManagementAuthorizationPrincipalOptionListInput) ([]ManagementAuthorizationGranteeAccountOption, error)
	ListManagementAuthorizationGranteeTeams(ctx context.Context, input ManagementAuthorizationPrincipalOptionListInput) ([]ManagementAuthorizationGranteeTeamOption, error)
	ListManagementAuthorizationGranteeGroups(ctx context.Context, input ManagementAuthorizationGranteeGroupOptionListInput) ([]ManagementAuthorizationGranteeGroupOption, error)
}

type ManagementRequestQuotaLimit struct {
	Enabled bool    `json:"enabled"`
	Limit   float64 `json:"limit"`
}

type ManagementRequestHourlyQuotaLimit struct {
	Enabled bool    `json:"enabled"`
	Hours   int     `json:"hours"`
	Limit   float64 `json:"limit"`
}

type ManagementRequestQuotaLimits struct {
	Hourly  *ManagementRequestHourlyQuotaLimit `json:"hourly,omitempty"`
	Daily   *ManagementRequestQuotaLimit       `json:"daily,omitempty"`
	Weekly  *ManagementRequestQuotaLimit       `json:"weekly,omitempty"`
	Monthly *ManagementRequestQuotaLimit       `json:"monthly,omitempty"`
	Total   *ManagementRequestQuotaLimit       `json:"total,omitempty"`
}

type ManagementAccountUsageSummary struct {
	RequestCount       int64      `json:"requestCount"`
	InputTokens        int64      `json:"inputTokens"`
	OutputTokens       int64      `json:"outputTokens"`
	CacheReadTokens    int64      `json:"cacheReadTokens"`
	CacheReadCost      float64    `json:"cacheReadCost"`
	CacheWriteTokens   int64      `json:"cacheWriteTokens"`
	CacheWrite1hTokens int64      `json:"cacheWrite1hTokens"`
	CacheWriteCost     float64    `json:"cacheWriteCost"`
	ThinkingTokens     int64      `json:"thinkingTokens"`
	InputImageTokens   int64      `json:"inputImageTokens"`
	OutputImageTokens  int64      `json:"outputImageTokens"`
	TotalTokens        int64      `json:"totalTokens"`
	TotalCost          float64    `json:"totalCost"`
	LastUsedAt         *time.Time `json:"lastUsedAt,omitempty"`
}

type ManagementAPIKeyListInput struct {
	SystemAccountID string
	Keyword         string
	Status          string
	RouteStrategyID string
	Limit           int
	Offset          int
}

type ManagementAPIKeyListRow struct {
	ID                       string
	SystemAccountID          string
	SystemAccountName        string
	Name                     string
	Description              *string
	KeyPrefix                string
	KeySuffix                string
	Status                   string
	IsDefault                bool
	RouteStrategyID          string
	RouteStrategyName        string
	RouteStrategyMode        string
	RouteStrategyStatus      string
	ExpiresAt                *time.Time
	QuotaLimitsJSON          *string
	AvailabilityScheduleJSON *string
}

type ManagementAPIKeyListPage struct {
	Rows    []ManagementAPIKeyListRow
	HasMore bool
}

var (
	ErrManagementAPIKeyRouteStrategyNotFound = errors.New("management API Key route strategy not found")
	ErrManagementAPIKeyRouteStrategyDisabled = errors.New("management API Key route strategy disabled")
	ErrManagementAPIKeyNameExists            = errors.New("management API Key name exists")
	ErrManagementAPIKeyHashExists            = errors.New("management API Key hash exists")
	ErrManagementAPIKeyNotFound              = errors.New("management API Key not found")
	ErrManagementAPIKeyDefaultRouteChange    = errors.New("management default API Key route change")
	ErrManagementAPIKeyDefaultDelete         = errors.New("management default API Key delete")
)

type ManagementAPIKeyCreateInput struct {
	ID                              string
	SystemAccountID                 string
	RouteStrategyID                 string
	Name                            string
	Description                     *string
	KeyHash                         string
	KeyPrefix                       string
	KeySuffix                       string
	KeySecretEncrypted              string
	Status                          string
	IsDefault                       bool
	ExpiresAt                       *time.Time
	QuotaLimitsJSON                 *string
	HourlyQuotaHours                *int
	AvailabilityScheduleJSON        *string
	AvailabilityScheduleNextCheckAt *time.Time
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
}

type ManagementAPIKeyCreator interface {
	CreateManagementAPIKey(ctx context.Context, input ManagementAPIKeyCreateInput) (ManagementAPIKeyListRow, error)
}

type ManagementAPIKeyUpdateInput struct {
	APIKeyID                        string
	OwnerSystemAccountID            string
	HasName                         bool
	Name                            string
	HasDescription                  bool
	Description                     *string
	HasRouteStrategyID              bool
	RouteStrategyID                 string
	HasStatus                       bool
	Status                          string
	HasExpiresAt                    bool
	ExpiresAt                       *time.Time
	HasQuotaLimits                  bool
	QuotaLimitsJSON                 *string
	HourlyQuotaHours                *int
	HasAvailabilitySchedule         bool
	AvailabilityScheduleJSON        *string
	AvailabilityScheduleNextCheckAt *time.Time
	UpdatedAt                       time.Time
}

type ManagementAPIKeyUpdateResult struct {
	Before ManagementAPIKeyListRow
	After  ManagementAPIKeyListRow
}

type ManagementAPIKeyUpdater interface {
	UpdateManagementAPIKey(
		ctx context.Context,
		input ManagementAPIKeyUpdateInput,
	) (ManagementAPIKeyUpdateResult, error)
}

type ManagementAPIKeyDeleteInput struct {
	APIKeyID             string
	OwnerSystemAccountID string
	DeletedAt            time.Time
}

type ManagementAPIKeyDeleteResult struct {
	APIKeyID             string
	Name                 string
	OwnerSystemAccountID string
}

type ManagementAPIKeyDeleter interface {
	DeleteManagementAPIKey(
		ctx context.Context,
		input ManagementAPIKeyDeleteInput,
	) (ManagementAPIKeyDeleteResult, error)
}

type ManagementAPIKeyUsageScope struct {
	SystemAccountID string
	APIKeyID        string
}

type ManagementAPIKeyUsageRow struct {
	SystemAccountID string
	APIKeyID        string
	Usage           ManagementAccountUsageSummary
}

type ManagementAPIKeyListReader interface {
	ListManagementAPIKeys(ctx context.Context, input ManagementAPIKeyListInput) (ManagementAPIKeyListPage, error)
	ListManagementAPIKeyUsageTotals(ctx context.Context, scopes []ManagementAPIKeyUsageScope) ([]ManagementAPIKeyUsageRow, error)
}

type ManagementAPIKeySecretScope struct {
	APIKeyID        string
	SystemAccountID string
}

type ManagementAPIKeySecretRow struct {
	ID                 string
	SystemAccountID    string
	Name               string
	KeyPrefix          string
	KeySuffix          string
	KeySecretEncrypted *string
}

type ManagementAPIKeySecretUpdateInput struct {
	APIKeyID           string
	SystemAccountID    string
	KeyHash            string
	KeyPrefix          string
	KeySuffix          string
	KeySecretEncrypted string
	UpdatedAt          time.Time
}

type ManagementAPIKeySecretStore interface {
	FindManagementAPIKeySecret(ctx context.Context, input ManagementAPIKeySecretScope) (ManagementAPIKeySecretRow, bool, error)
	LockManagementAPIKeySecretRefreshTarget(ctx context.Context, input ManagementAPIKeySecretScope) (ManagementAPIKeyListRow, bool, error)
	UpdateManagementAPIKeySecret(ctx context.Context, input ManagementAPIKeySecretUpdateInput) (bool, error)
}

type ManagementAPIKeySecretTransactor interface {
	ManagementAPIKeySecretInTx(
		ctx context.Context,
		fn func(context.Context, ManagementAPIKeySecretStore) error,
	) error
}

type ManagementAccountUsageStatsRange struct {
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
	Days      int    `json:"days"`
	MaxDays   int    `json:"maxDays"`
}

type ManagementResourceAuthorizationSourceSummary struct {
	ID              string     `json:"id"`
	AuthorizationID string     `json:"authorizationId"`
	SourceType      string     `json:"sourceType"`
	SourceTeamID    string     `json:"sourceTeamId,omitempty"`
	SourceTeamName  string     `json:"sourceTeamName,omitempty"`
	Status          string     `json:"status"`
	ActivatedAt     *time.Time `json:"activatedAt,omitempty"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	EndedReason     string     `json:"endedReason,omitempty"`
	CreatedBy       string     `json:"createdBy"`
	CreatedAt       time.Time  `json:"createdAt"`
	RevokedBy       string     `json:"revokedBy,omitempty"`
	RevokedAt       *time.Time `json:"revokedAt,omitempty"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type ManagementResourceAuthorizationSummary struct {
	ID                             string                                         `json:"id"`
	ResourceType                   string                                         `json:"resourceType"`
	ResourceID                     string                                         `json:"resourceId"`
	ResourceName                   string                                         `json:"resourceName,omitempty"`
	ResourceOwnerSystemAccountID   string                                         `json:"resourceOwnerSystemAccountId"`
	ResourceOwnerSystemAccountName string                                         `json:"resourceOwnerSystemAccountName,omitempty"`
	GranteeType                    string                                         `json:"granteeType,omitempty"`
	GranteeSystemAccountID         string                                         `json:"granteeSystemAccountId,omitempty"`
	GranteeSystemAccountName       string                                         `json:"granteeSystemAccountName,omitempty"`
	GranteeUsername                string                                         `json:"granteeUsername,omitempty"`
	GranteeTeamID                  string                                         `json:"granteeTeamId,omitempty"`
	GranteeTeamName                string                                         `json:"granteeTeamName,omitempty"`
	Scope                          string                                         `json:"scope"`
	Status                         string                                         `json:"status"`
	Remark                         string                                         `json:"remark,omitempty"`
	ExpiresAt                      *time.Time                                     `json:"expiresAt,omitempty"`
	Limits                         ManagementRequestQuotaLimits                   `json:"limits,omitempty"`
	ResourceAccountExpiresAt       *time.Time                                     `json:"resourceAccountExpiresAt,omitempty"`
	EffectiveSourceType            string                                         `json:"effectiveSourceType,omitempty"`
	EffectiveSourceTeamID          string                                         `json:"effectiveSourceTeamId,omitempty"`
	EffectiveSourceTeamName        string                                         `json:"effectiveSourceTeamName,omitempty"`
	ActivatedAt                    *time.Time                                     `json:"activatedAt,omitempty"`
	LastSourceChangedAt            *time.Time                                     `json:"lastSourceChangedAt,omitempty"`
	AuthorizationSources           []ManagementResourceAuthorizationSourceSummary `json:"authorizationSources"`
	Usage                          ManagementAccountUsageSummary                  `json:"usage"`
	LastUsedAt                     *time.Time                                     `json:"lastUsedAt,omitempty"`
	CreatedBy                      string                                         `json:"createdBy"`
	CreatedAt                      time.Time                                      `json:"createdAt"`
	RevokedBy                      string                                         `json:"revokedBy,omitempty"`
	RevokedAt                      *time.Time                                     `json:"revokedAt,omitempty"`
	RevokedReason                  string                                         `json:"revokedReason,omitempty"`
	UpdatedAt                      time.Time                                      `json:"updatedAt"`
}

type ManagementResourceAuthorizationListInput struct {
	AuthorizationID              string
	ActorSystemAccountID         string
	CanAccessAll                 bool
	ScopedSystemAccountID        string
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeSystemAccountID       string
	TeamID                       string
	Status                       string
	Direction                    string
	SourceType                   string
	Keyword                      string
	Limit                        int
	Offset                       int
}

type ManagementResourceAuthorizationListResult struct {
	Items   []ManagementResourceAuthorizationSummary
	HasMore bool
}

type ManagementAuthorizationUsageOverviewInput struct {
	ActorSystemAccountID   string
	CanAccessAll           bool
	ScopedSystemAccountID  string
	ResourceType           string
	ResourceID             string
	TeamID                 string
	GranteeSystemAccountID string
	StartDate              string
	EndDate                string
	Limit                  int
	Offset                 int
}

type ManagementAuthorizationTeamUsageRow struct {
	ID                            string                        `json:"id"`
	TeamID                        string                        `json:"teamId"`
	TeamName                      string                        `json:"teamName"`
	Status                        string                        `json:"status"`
	ResourceType                  string                        `json:"resourceType,omitempty"`
	ResourceID                    string                        `json:"resourceId,omitempty"`
	ResourceName                  string                        `json:"resourceName,omitempty"`
	AccountID                     string                        `json:"accountId,omitempty"`
	AccountName                   string                        `json:"accountName,omitempty"`
	AccountOwnerSystemAccountID   string                        `json:"accountOwnerSystemAccountId,omitempty"`
	AccountOwnerSystemAccountName string                        `json:"accountOwnerSystemAccountName,omitempty"`
	Usage                         ManagementAccountUsageSummary `json:"usage"`
	LastUsedAt                    *time.Time                    `json:"lastUsedAt,omitempty"`
}

type ManagementAuthorizationTeamUsageOverviewResult struct {
	Summary ManagementAccountUsageSummary
	Rows    []ManagementAuthorizationTeamUsageRow
	HasMore bool
}

type ManagementAuthorizationUserUsageRow struct {
	ID                            string                        `json:"id"`
	SystemAccountID               string                        `json:"systemAccountId"`
	UserName                      string                        `json:"userName"`
	Username                      string                        `json:"username,omitempty"`
	TeamNames                     []string                      `json:"teamNames,omitempty"`
	ResourceType                  string                        `json:"resourceType,omitempty"`
	ResourceID                    string                        `json:"resourceId,omitempty"`
	ResourceName                  string                        `json:"resourceName,omitempty"`
	AccountID                     string                        `json:"accountId,omitempty"`
	AccountName                   string                        `json:"accountName,omitempty"`
	AccountOwnerSystemAccountID   string                        `json:"accountOwnerSystemAccountId,omitempty"`
	AccountOwnerSystemAccountName string                        `json:"accountOwnerSystemAccountName,omitempty"`
	SourceLabels                  []string                      `json:"sourceLabels"`
	Usage                         ManagementAccountUsageSummary `json:"usage"`
	LastUsedAt                    *time.Time                    `json:"lastUsedAt,omitempty"`
}

type ManagementAuthorizationUserUsageOverviewResult struct {
	Summary ManagementAccountUsageSummary
	Rows    []ManagementAuthorizationUserUsageRow
	HasMore bool
}

type ManagementAuthorizationUsageOverviewReader interface {
	ListManagementAuthorizationTeamUsageOverview(ctx context.Context, input ManagementAuthorizationUsageOverviewInput) (ManagementAuthorizationTeamUsageOverviewResult, error)
	ListManagementAuthorizationUserUsageOverview(ctx context.Context, input ManagementAuthorizationUsageOverviewInput) (ManagementAuthorizationUserUsageOverviewResult, error)
}

type ManagementResourceAuthorizationUsageDetail struct {
	SystemAccountID   string `json:"systemAccountId"`
	SystemAccountName string `json:"systemAccountName,omitempty"`
	Username          string `json:"username,omitempty"`
	ManagementAccountUsageSummary
	RangeUsage ManagementAccountUsageSummary `json:"rangeUsage"`
}

type ManagementResourceAuthorizationUsageResult struct {
	Summary                     ManagementResourceAuthorizationSummary
	UsageBySystemAccount        []ManagementResourceAuthorizationUsageDetail
	UsageBySystemAccountTotal   int
	UsageBySystemAccountHasMore bool
}

type ManagementResourceAuthorizationUsageInput struct {
	AuthorizationID       string
	ActorSystemAccountID  string
	CanAccessAll          bool
	ScopedSystemAccountID string
	StartDate             string
	EndDate               string
	Limit                 int
	Offset                int
}

type ManagementResourceAuthorizationUsageReader interface {
	FindManagementResourceAuthorizationUsage(ctx context.Context, input ManagementResourceAuthorizationUsageInput) (ManagementResourceAuthorizationUsageResult, bool, error)
}

type ManagementUsageStatsTimezoneReader interface {
	GetManagementUsageStatsTimezone(ctx context.Context) (string, bool, error)
}

type ManagementAuthorizationUsageRangeWindowRefreshInput struct {
	Ranges      []ManagementAccountUsageStatsRange
	RefreshedAt time.Time
}

type ManagementAuthorizationUsageRangeWindowRefreshResult struct {
	Ranges   int
	TeamRows int64
	UserRows int64
}

type ManagementAuthorizationUsageRangeWindowRefresher interface {
	RefreshManagementAuthorizationUsageRangeWindows(ctx context.Context, input ManagementAuthorizationUsageRangeWindowRefreshInput) (ManagementAuthorizationUsageRangeWindowRefreshResult, error)
}

type ManagementResourceAuthorizationGetInput struct {
	AuthorizationID       string
	ActorSystemAccountID  string
	CanAccessAll          bool
	ScopedSystemAccountID string
}

type ManagementResourceAuthorizationCreateInput struct {
	ResourceType                    string
	ResourceID                      string
	ResourceOwnerSystemAccountID    string
	GranteeType                     string
	GranteeID                       string
	TargetGroupID                   string
	Remark                          string
	HasRemark                       bool
	ExpiresAt                       *time.Time
	Limits                          ManagementRequestQuotaLimits
	LimitsJSON                      *string
	LimitHourlyWindowHours          int
	AuthorizationInstanceSecretJSON string
	ActorSystemAccountID            string
	CreatedAt                       time.Time
}

type ManagementResourceAuthorizationCreator interface {
	CreateManagementResourceAuthorization(ctx context.Context, input ManagementResourceAuthorizationCreateInput) (ManagementResourceAuthorizationSummary, error)
}

type ManagementResourceAuthorizationLister interface {
	ListManagementResourceAuthorizations(ctx context.Context, input ManagementResourceAuthorizationListInput) (ManagementResourceAuthorizationListResult, error)
}

type ManagementResourceAuthorizationGetter interface {
	FindManagementResourceAuthorization(ctx context.Context, input ManagementResourceAuthorizationGetInput) (ManagementResourceAuthorizationSummary, bool, error)
}

type ManagementResourceAuthorizationReturnInput struct {
	AuthorizationID        string
	GranteeSystemAccountID string
	ActorSystemAccountID   string
	ReturnedAt             time.Time
}

type ManagementResourceAuthorizationReturner interface {
	ReturnManagementResourceAuthorizationForGrantee(ctx context.Context, input ManagementResourceAuthorizationReturnInput) (ManagementResourceAuthorizationSummary, bool, error)
}

type ManagementResourceAuthorizationReturnResourceInput struct {
	ResourceType           string
	ResourceID             string
	GranteeSystemAccountID string
	ActorSystemAccountID   string
	ReturnedAt             time.Time
}

type ManagementResourceAuthorizationResourceReturner interface {
	ReturnManagementResourceAuthorizationForGranteeByResource(ctx context.Context, input ManagementResourceAuthorizationReturnResourceInput) (ManagementResourceAuthorizationSummary, bool, error)
}

type ManagementResourceAuthorizationUpdateInput struct {
	AuthorizationID        string
	ActorSystemAccountID   string
	CanAccessAll           bool
	ScopedSystemAccountID  string
	HasStatus              bool
	Status                 string
	HasExpiresAt           bool
	ExpiresAt              *time.Time
	HasLimits              bool
	LimitsJSON             *string
	LimitHourlyWindowHours int
	UpdatedAt              time.Time
}

type ManagementResourceAuthorizationUpdater interface {
	UpdateManagementResourceAuthorization(ctx context.Context, input ManagementResourceAuthorizationUpdateInput) (ManagementResourceAuthorizationSummary, bool, error)
}

type ManagementResourceAuthorizationRevokeInput struct {
	AuthorizationID       string
	ActorSystemAccountID  string
	CanAccessAll          bool
	ScopedSystemAccountID string
	RevokedAt             time.Time
}

type ManagementResourceAuthorizationRevoker interface {
	RevokeManagementResourceAuthorization(ctx context.Context, input ManagementResourceAuthorizationRevokeInput) (ManagementResourceAuthorizationSummary, bool, error)
}

type ManagementResourceAuthorizationExpirySweepInput struct {
	Limit     int
	ExpiredAt time.Time
}

type ManagementResourceAuthorizationExpiryFanout struct {
	AuthorizationID              string
	ResourceType                 string
	ResourceID                   string
	ResourceOwnerSystemAccountID string
	GranteeType                  string
	GranteeSystemAccountID       string
	GranteeTeamID                string
}

type ManagementResourceAuthorizationExpirySweepResult struct {
	Expired        int
	Authorizations []ManagementResourceAuthorizationExpiryFanout
}

type ManagementResourceAuthorizationExpirySweeper interface {
	ExpireDueManagementResourceAuthorizations(ctx context.Context, input ManagementResourceAuthorizationExpirySweepInput) (ManagementResourceAuthorizationExpirySweepResult, error)
}

type ManagementProviderEndpointFamily struct {
	Code        string
	Name        string
	Description string
}

type ManagementProviderProtocolProfile struct {
	ID                      string
	ProviderCode            string
	Name                    string
	Description             string
	Enabled                 bool
	ProtocolCode            string
	ProtocolVersion         string
	BaseURL                 string
	DefaultHealthCheckModel string
	AccountTypes            []string
	Capabilities            []string
	EndpointFamilies        []ManagementProviderEndpointFamily
}

type ManagementProviderOption struct {
	ID                            string
	Code                          string
	Name                          string
	ParentCode                    string
	Description                   string
	Enabled                       bool
	DefaultProtocolProfileID      string
	ProtocolCode                  string
	ProtocolVersion               string
	BaseURL                       string
	DefaultHealthCheckModel       string
	SystemDefaultHealthCheckModel string
	DefaultSupportedModels        []string
	AccountTypes                  []string
	Capabilities                  []string
	ProtocolProfiles              []ManagementProviderProtocolProfile
}

type ManagementProviderListInput struct {
	SystemAccountID string
}

type ManagementProviderOptionListInput struct {
	SystemAccountID string
}

type ManagementProviderOptionReader interface {
	ListManagementProviderOptions(ctx context.Context, input ManagementProviderOptionListInput) ([]ManagementProviderOption, error)
}

type ManagementProviderReader interface {
	ManagementProviderOptionReader
	ListManagementProviders(ctx context.Context, input ManagementProviderListInput) ([]ManagementProviderOption, error)
}

type ManagementProviderModelProvider struct {
	Code       string
	Enabled    bool
	ParentCode string
}

type ManagementProviderModelCatalogItem struct {
	ID                                      string
	ProviderCode                            string
	Model                                   string
	Scope                                   string
	SystemAccountID                         string
	Status                                  string
	Mode                                    string
	CatalogOrder                            *int
	ReleaseDate                             string
	ShutdownDate                            string
	SupportedAPIProtocols                   []string
	SupportedServiceTiers                   []string
	SupportedReasoningEfforts               []string
	DefaultReasoningEffort                  string
	CodexSupportedReasoningLevels           []string
	CodexDefaultReasoningLevel              string
	CodexMultiAgentVersion                  string
	ContextWindowTokens                     *int
	MaxInputTokens                          *int
	MaxOutputTokens                         *int
	MaxTokens                               *int
	InputUSDPer1M                           *float64
	OutputUSDPer1M                          *float64
	CachedInputUSDPer1M                     *float64
	CacheWriteUSDPer1M                      *float64
	CacheWrite1hUSDPer1M                    *float64
	ServiceTierPrices                       map[string]ManagementProviderModelPriceSet
	LongContextInputTokenThreshold          *int
	LongContextInputTokenThresholdInclusive bool
	LongContextInputCostMultiplier          *float64
	LongContextOutputCostMultiplier         *float64
	ImageInputUSDPer1M                      *float64
	ImageOutputUSDPer1M                     *float64
	AudioInputUSDPer1M                      *float64
	AudioOutputUSDPer1M                     *float64
	OutputUSDPerImage                       *float64
	SupportsPromptCaching                   bool
	SupportsServiceTier                     bool
	CatalogVisible                          bool
	PricingNotes                            string
	CapabilityNotes                         string
	Notes                                   string
	CreatedBy                               string
	UpdatedBy                               string
	Source                                  string
	CreatedAt                               time.Time
	UpdatedAt                               time.Time
}

type ManagementProviderModelPriceSet struct {
	InputUSDPer1M        *float64 `json:"inputUsdPer1M,omitempty"`
	OutputUSDPer1M       *float64 `json:"outputUsdPer1M,omitempty"`
	CachedInputUSDPer1M  *float64 `json:"cachedInputUsdPer1M,omitempty"`
	CacheWriteUSDPer1M   *float64 `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hUSDPer1M *float64 `json:"cacheWrite1hUsdPer1M,omitempty"`
	ImageInputUSDPer1M   *float64 `json:"imageInputUsdPer1M,omitempty"`
	ImageOutputUSDPer1M  *float64 `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUSDPer1M   *float64 `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUSDPer1M  *float64 `json:"audioOutputUsdPer1M,omitempty"`
	OutputUSDPerImage    *float64 `json:"outputUsdPerImage,omitempty"`
}

type ManagementProviderModelCatalogListInput struct {
	BuiltInProviderCodes []string
	CustomProviderCodes  []string
	SystemAccountID      string
	IncludeInactive      bool
}

type ManagementProviderDefaultHealthCheckModelPreference struct {
	ProviderCode string
	Model        string
}

type ManagementProviderDefaultHealthCheckModelInput struct {
	ProviderCode    string
	SystemAccountID string
	Model           string
}

type ManagementProviderSystemDefaultHealthCheckModelInput struct {
	ProviderCode string
	Model        string
}

type ManagementProviderDefaultHealthCheckModelClearInput struct {
	ProviderCode    string
	SystemAccountID string
	Model           string
}

type ManagementProviderSystemDefaultHealthCheckModelClearInput struct {
	ProviderCode string
	Model        string
}

type ManagementCustomProviderModelSaveInput struct {
	ID                        string
	ProviderCode              string
	Model                     string
	Scope                     string
	SystemAccountID           string
	Status                    string
	CatalogVisible            bool
	Mode                      string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string
	ReleaseDate               string
	ShutdownDate              string
	ContextWindowTokens       *int
	MaxInputTokens            *int
	MaxOutputTokens           *int
	InputUSDPer1M             *float64
	OutputUSDPer1M            *float64
	CachedInputUSDPer1M       *float64
	CacheWriteUSDPer1M        *float64
	CacheWrite1hUSDPer1M      *float64
	ServiceTierPrices         map[string]ManagementProviderModelPriceSet
	ImageInputUSDPer1M        *float64
	ImageOutputUSDPer1M       *float64
	AudioInputUSDPer1M        *float64
	AudioOutputUSDPer1M       *float64
	OutputUSDPerImage         *float64
	PricingNotes              string
	CapabilityNotes           string
	Notes                     string
	ActorSystemAccountID      string
}

type ManagementCustomProviderModelUpdateInput struct {
	ID                        string
	ProviderCode              string
	ActorSystemAccountID      string
	ActorRole                 string
	Status                    ManagementProviderModelOptionalString
	CatalogVisible            ManagementProviderModelOptionalBool
	Mode                      ManagementProviderModelOptionalString
	SupportedAPIProtocols     ManagementProviderModelOptionalStringList
	SupportedServiceTiers     ManagementProviderModelOptionalStringList
	SupportedReasoningEfforts ManagementProviderModelOptionalStringList
	DefaultReasoningEffort    ManagementProviderModelOptionalString
	ReleaseDate               ManagementProviderModelOptionalString
	ShutdownDate              ManagementProviderModelOptionalString
	ContextWindowTokens       ManagementProviderModelOptionalInt
	MaxInputTokens            ManagementProviderModelOptionalInt
	MaxOutputTokens           ManagementProviderModelOptionalInt
	InputUSDPer1M             ManagementProviderModelOptionalFloat
	OutputUSDPer1M            ManagementProviderModelOptionalFloat
	CachedInputUSDPer1M       ManagementProviderModelOptionalFloat
	CacheWriteUSDPer1M        ManagementProviderModelOptionalFloat
	CacheWrite1hUSDPer1M      ManagementProviderModelOptionalFloat
	ServiceTierPrices         ManagementProviderModelOptionalPriceMap
	ImageInputUSDPer1M        ManagementProviderModelOptionalFloat
	ImageOutputUSDPer1M       ManagementProviderModelOptionalFloat
	AudioInputUSDPer1M        ManagementProviderModelOptionalFloat
	AudioOutputUSDPer1M       ManagementProviderModelOptionalFloat
	OutputUSDPerImage         ManagementProviderModelOptionalFloat
	PricingNotes              ManagementProviderModelOptionalString
	CapabilityNotes           ManagementProviderModelOptionalString
	Notes                     ManagementProviderModelOptionalString
}

type ManagementCustomProviderModelUpdateResult struct {
	Before ManagementProviderModelCatalogItem
	After  ManagementProviderModelCatalogItem
}

type ManagementCustomProviderModelUpdateValidate func(ManagementCustomProviderModelUpdateResult) error

type ManagementCustomProviderModelScopeInput struct {
	ProviderCode    string
	Scope           string
	SystemAccountID string
	Model           string
}

type ManagementCustomProviderModelBindingInput struct {
	ProviderCode    string
	Model           string
	Scope           string
	SystemAccountID string
}

type ManagementCustomProviderModelBindingSummary struct {
	SupportedModelAccountCount  int
	MappingSourceAccountCount   int
	MappingUpstreamAccountCount int
	TotalAccountCount           int
}

type ManagementProviderModelOptionalFloat struct {
	Present bool
	Value   *float64
}

type ManagementProviderModelOptionalString struct {
	Present bool
	Value   string
}

type ManagementProviderModelOptionalStringList struct {
	Present bool
	Value   []string
}

type ManagementProviderModelOptionalInt struct {
	Present bool
	Value   *int
}

type ManagementProviderModelOptionalBool struct {
	Present bool
	Value   bool
}

type ManagementProviderModelOptionalPriceMap struct {
	Present bool
	Value   map[string]ManagementProviderModelPriceSet
}

type ManagementBuiltInProviderModelPriceUpdateInput struct {
	ID                        string
	ProviderCode              string
	Status                    ManagementProviderModelOptionalString
	CatalogVisible            ManagementProviderModelOptionalBool
	Mode                      ManagementProviderModelOptionalString
	SupportedAPIProtocols     ManagementProviderModelOptionalStringList
	SupportedServiceTiers     ManagementProviderModelOptionalStringList
	SupportedReasoningEfforts ManagementProviderModelOptionalStringList
	DefaultReasoningEffort    ManagementProviderModelOptionalString
	ReleaseDate               ManagementProviderModelOptionalString
	ShutdownDate              ManagementProviderModelOptionalString
	ContextWindowTokens       ManagementProviderModelOptionalInt
	MaxInputTokens            ManagementProviderModelOptionalInt
	MaxOutputTokens           ManagementProviderModelOptionalInt
	InputUSDPer1M             ManagementProviderModelOptionalFloat
	OutputUSDPer1M            ManagementProviderModelOptionalFloat
	CachedInputUSDPer1M       ManagementProviderModelOptionalFloat
	CacheWriteUSDPer1M        ManagementProviderModelOptionalFloat
	CacheWrite1hUSDPer1M      ManagementProviderModelOptionalFloat
	ServiceTierPrices         ManagementProviderModelOptionalPriceMap
	ImageInputUSDPer1M        ManagementProviderModelOptionalFloat
	ImageOutputUSDPer1M       ManagementProviderModelOptionalFloat
	AudioInputUSDPer1M        ManagementProviderModelOptionalFloat
	AudioOutputUSDPer1M       ManagementProviderModelOptionalFloat
	OutputUSDPerImage         ManagementProviderModelOptionalFloat
}

type ManagementProviderModelConfigurationSnapshot struct {
	ID                        string
	ProviderCode              string
	Status                    string
	CatalogVisible            bool
	Mode                      string
	SupportedAPIProtocols     []string
	SupportedServiceTiers     []string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string
	ReleaseDate               string
	ShutdownDate              string
	ContextWindowTokens       *int
	MaxInputTokens            *int
	MaxOutputTokens           *int
	InputUSDPer1M             *float64
	OutputUSDPer1M            *float64
	CachedInputUSDPer1M       *float64
	CacheWriteUSDPer1M        *float64
	CacheWrite1hUSDPer1M      *float64
	ServiceTierPrices         map[string]ManagementProviderModelPriceSet
	ImageInputUSDPer1M        *float64
	ImageOutputUSDPer1M       *float64
	AudioInputUSDPer1M        *float64
	AudioOutputUSDPer1M       *float64
	OutputUSDPerImage         *float64
	UpdatedAt                 time.Time
}

type ManagementBuiltInProviderModelPriceUpdateResult struct {
	Before ManagementProviderModelConfigurationSnapshot
	After  ManagementProviderModelConfigurationSnapshot
}

type ManagementBuiltInProviderModelUpdateValidate func(ManagementBuiltInProviderModelPriceUpdateResult) error

type ManagementProviderModelCatalogReader interface {
	FindManagementProviderModelProvider(ctx context.Context, code string) (ManagementProviderModelProvider, bool, error)
	ListManagementEnabledModelProviderCodes(ctx context.Context) ([]string, error)
	ListManagementProviderCodesByProtocol(ctx context.Context, protocolCode string, protocolVersion string) ([]string, error)
	ListManagementProviderModelCatalog(ctx context.Context, input ManagementProviderModelCatalogListInput) ([]ManagementProviderModelCatalogItem, error)
}

type ManagementProviderDefaultHealthCheckModelWriter interface {
	SetManagementProviderDefaultHealthCheckModel(ctx context.Context, input ManagementProviderDefaultHealthCheckModelInput) (ManagementProviderDefaultHealthCheckModelPreference, error)
	ClearManagementProviderDefaultHealthCheckModelIfModel(ctx context.Context, input ManagementProviderDefaultHealthCheckModelClearInput) (bool, error)
	SetManagementProviderSystemDefaultHealthCheckModel(ctx context.Context, input ManagementProviderSystemDefaultHealthCheckModelInput) (ManagementProviderDefaultHealthCheckModelPreference, error)
	ClearManagementProviderSystemDefaultHealthCheckModelIfModel(ctx context.Context, input ManagementProviderSystemDefaultHealthCheckModelClearInput) (bool, error)
}

type ManagementCustomProviderModelWriter interface {
	FindManagementCustomProviderModel(ctx context.Context, id string) (ManagementProviderModelCatalogItem, bool, error)
	FindManagementCustomProviderModelByScope(ctx context.Context, input ManagementCustomProviderModelScopeInput) (ManagementProviderModelCatalogItem, bool, error)
	SaveManagementCustomProviderModel(ctx context.Context, input ManagementCustomProviderModelSaveInput) (ManagementProviderModelCatalogItem, error)
	UpdateManagementCustomProviderModel(ctx context.Context, input ManagementCustomProviderModelUpdateInput, validate ManagementCustomProviderModelUpdateValidate) (ManagementCustomProviderModelUpdateResult, bool, error)
	DeleteManagementCustomProviderModel(ctx context.Context, id string) (bool, error)
	GetManagementCustomProviderModelBindingSummary(ctx context.Context, input ManagementCustomProviderModelBindingInput) (ManagementCustomProviderModelBindingSummary, error)
	UpdateManagementBuiltInProviderModelPrices(ctx context.Context, input ManagementBuiltInProviderModelPriceUpdateInput, validate ManagementBuiltInProviderModelUpdateValidate) (ManagementBuiltInProviderModelPriceUpdateResult, bool, error)
}

type ManagementRouteStrategyOption struct {
	ID                string
	SystemAccountID   string
	SystemAccountName string
	Name              string
	Mode              string
	Status            string
	IsDefault         bool
}

type ManagementRouteStrategyOptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	Limit                      int
	ActiveOnly                 bool
}

type ManagementRouteStrategyOptionReader interface {
	ListManagementRouteStrategyOptions(ctx context.Context, input ManagementRouteStrategyOptionListInput) ([]ManagementRouteStrategyOption, error)
}

type ManagementRouteStrategyListInput struct {
	SystemAccountID string
	Keyword         string
	Mode            string
	Status          string
	Limit           int
	Offset          int
}

type ManagementRouteStrategyListRow struct {
	ID                string
	SystemAccountID   string
	SystemAccountName string
	Name              string
	Description       *string
	Mode              string
	Status            string
	IsDefault         bool
	ConfigJSON        *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ManagementRouteStrategyListPage struct {
	Rows    []ManagementRouteStrategyListRow
	HasMore bool
}

type ManagementRouteStrategyScope struct {
	ID              string
	SystemAccountID string
}

type ManagementRouteStrategyGroupBinding struct {
	ID           string
	GroupID      string
	GroupName    string
	ProviderCode string
	Priority     int
	Weight       int
	Status       string
	GroupEnabled bool
}

type ManagementRouteStrategyListEnrichment struct {
	ID                  string
	SystemAccountID     string
	GroupBindingPreview []ManagementRouteStrategyGroupBinding
	BindingCount        int64
	APIKeyCount         int64
}

type ManagementRouteStrategyDetailInput struct {
	RouteStrategyID string
	SystemAccountID string
}

type ManagementRouteStrategyDetailRow struct {
	ManagementRouteStrategyListRow
	GroupBindings []ManagementRouteStrategyGroupBinding
	APIKeyCount   int64
}

type ManagementRouteStrategyListReader interface {
	ListManagementRouteStrategies(ctx context.Context, input ManagementRouteStrategyListInput) (ManagementRouteStrategyListPage, error)
	ListManagementRouteStrategyListEnrichment(ctx context.Context, scopes []ManagementRouteStrategyScope) ([]ManagementRouteStrategyListEnrichment, error)
}

type ManagementRouteStrategyDetailReader interface {
	FindManagementRouteStrategyDetail(ctx context.Context, input ManagementRouteStrategyDetailInput) (ManagementRouteStrategyDetailRow, bool, error)
}

type ManagementGroupOption struct {
	ID                                 string
	SystemAccountID                    string
	SystemAccountName                  string
	OwnerSystemAccountID               string
	OwnerSystemAccountName             string
	Name                               string
	ProviderCode                       string
	Enabled                            bool
	IsDefault                          bool
	GroupType                          string
	SchedulingPolicy                   map[string]any
	AccessType                         string
	GroupAuthorizationID               string
	AuthorizationStatus                string
	AuthorizationExpiresAt             *time.Time
	AuthorizationLimits                map[string]any
	HasActiveManualAuthorizationSource bool
}

type ManagementGroupAccountOption struct {
	ID                                 string
	SystemAccountID                    string
	SystemAccountName                  string
	OwnerSystemAccountID               string
	OwnerSystemAccountName             string
	Name                               string
	ProviderCode                       string
	Enabled                            bool
	IsDefault                          bool
	GroupType                          string
	SchedulingPolicy                   map[string]any
	AccessType                         string
	GroupAuthorizationID               string
	AuthorizationStatus                string
	AuthorizationExpiresAt             *time.Time
	AuthorizationLimits                map[string]any
	HasActiveManualAuthorizationSource bool
	AccountIDs                         []string
}

type ManagementGroupOptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	ProviderCode               string
	Limit                      int
	ManageableOnly             bool
	PreferDefault              bool
}

type ManagementGroupListInput struct {
	SystemAccountID string
	Limit           int
	Offset          int
}

type ManagementGroupListRow struct {
	ID                      string
	SystemAccountID         string
	SystemAccountName       string
	Name                    string
	ProviderCode            string
	Description             *string
	Enabled                 bool
	IsDefault               bool
	GroupType               string
	SchedulingPolicyJSON    *string
	AccessType              string
	GroupAuthorizationID    string
	AuthorizationStatus     string
	AuthorizationExpiresAt  *time.Time
	AuthorizationLimitsJSON *string
	EffectiveUpdatedAt      time.Time
}

type ManagementGroupListPage struct {
	Rows    []ManagementGroupListRow
	HasMore bool
}

// ManagementGroupStatusSnapshotRow is the deliberately narrow visibility
// projection used by the progressive group status endpoint.
type ManagementGroupStatusSnapshotRow struct {
	ID                   string
	SystemAccountID      string
	AccessType           string
	GroupAuthorizationID string
}

type ManagementGroupStatusSnapshotInput struct {
	SystemAccountID string
	GroupIDs        []string
}

type ManagementGroupAccountStatsRow struct {
	SystemAccountID    string
	GroupID            string
	Total              int
	Available          int
	Active             int
	Disabled           int
	Error              int
	RateLimited        int
	CurrentConcurrency int
	ConcurrencyLimit   int
}

type ManagementGroupUsageLookupInput struct {
	Key             string
	SystemAccountID string
	ScopeType       string
	ScopeID         string
}

type ManagementGroupUsageRow struct {
	Key             string
	SystemAccountID string
	ScopeType       string
	ScopeID         string
	Usage           ManagementAccountUsageSummary
}

type ManagementGroupAuthorizationSourceRow struct {
	AuthorizationID string
	SourceType      string
	Status          string
	SourceTeamName  string
}

type ManagementGroupDetailInput struct {
	GroupID         string
	SystemAccountID string
}

type ManagementGroupDetailReader interface {
	FindManagementGroupDetail(ctx context.Context, input ManagementGroupDetailInput) (ManagementGroupListRow, bool, error)
	ListManagementGroupDetailAccountIDs(ctx context.Context, input ManagementGroupDetailInput) ([]string, error)
	ListManagementGroupDetailAuthorizationSources(ctx context.Context, input ManagementGroupDetailInput) ([]ManagementResourceAuthorizationSourceSummary, error)
}

type ManagementGroupCreateInput struct {
	ID                   string
	SystemAccountID      string
	Name                 string
	ProviderCode         string
	Description          *string
	Enabled              bool
	GroupType            string
	SchedulingPolicyJSON *string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ManagementGroupSummary struct {
	ID                   string
	SystemAccountID      string
	Name                 string
	ProviderCode         string
	Description          *string
	Enabled              bool
	IsDefault            bool
	GroupType            string
	SchedulingPolicyJSON *string
}

type ManagementGroupUpdateInput struct {
	GroupID                     string
	ActorSystemAccountID        string
	CanAccessAll                bool
	EffectiveSystemAccountID    string
	HasName                     bool
	Name                        string
	HasProviderCode             bool
	ProviderCode                string
	HasDescription              bool
	Description                 *string
	HasEnabled                  bool
	Enabled                     bool
	HasGroupType                bool
	GroupType                   string
	HasSchedulingPolicy         bool
	SchedulingPolicyJSON        *string
	DefaultSchedulingPolicyJSON string
	UpdatedAt                   time.Time
}

type ManagementGroupMutationSummary struct {
	ID                   string
	Name                 string
	ProviderCode         string
	Description          *string
	Enabled              bool
	IsDefault            bool
	GroupType            string
	SchedulingPolicyJSON *string
}

type ManagementGroupUpdateResult struct {
	Before                   ManagementGroupMutationSummary
	After                    ManagementGroupMutationSummary
	AccessType               string
	OwnerSystemAccountID     string
	EffectiveSystemAccountID string
	GroupAuthorizationID     string
}

type ManagementGroupDeleteInput struct {
	GroupID                  string
	CanAccessAll             bool
	EffectiveSystemAccountID string
	DeletedAt                time.Time
	Now                      time.Time
}

type ManagementGroupDeletedRouteStrategy struct {
	ID   string
	Name string
}

type ManagementGroupDeleteResult struct {
	Before                  ManagementGroupMutationSummary
	OwnerSystemAccountID    string
	AffectedRouteStrategies []ManagementGroupDeletedRouteStrategy
}

var (
	ErrManagementGroupSystemAccountNotFound  = errors.New("management group system account not found")
	ErrManagementGroupProviderNotFound       = errors.New("management group provider not found")
	ErrManagementGroupProviderDisabled       = errors.New("management group provider disabled")
	ErrManagementGroupNameExists             = errors.New("management group name exists")
	ErrManagementGroupNotFound               = errors.New("management group not found")
	ErrManagementGroupDefaultReadonly        = errors.New("management group default readonly")
	ErrManagementGroupProviderHasAccounts    = errors.New("management group provider has accounts")
	ErrManagementGroupAuthorizedFields       = errors.New("management group authorized fields")
	ErrManagementGroupRouteStrategyWouldLose = errors.New("management group route strategy would lose")
)

type ManagementGroupOptionReader interface {
	ListManagementGroupOptions(ctx context.Context, input ManagementGroupOptionListInput) ([]ManagementGroupOption, error)
	ListManagementGroupAccountOptions(ctx context.Context, input ManagementGroupOptionListInput) ([]ManagementGroupAccountOption, error)
}

type ManagementGroupAuthorizationOptionRow struct {
	ID         string
	Name       string
	AccessType string
}

type ManagementGroupAuthorizationOptionReader interface {
	ListManagementGroupAuthorizationOptions(ctx context.Context, input ManagementGroupOptionListInput) ([]ManagementGroupAuthorizationOptionRow, error)
}

type ManagementGroupListPageReader interface {
	ListManagementGroups(ctx context.Context, input ManagementGroupListInput) (ManagementGroupListPage, error)
}

type ManagementGroupStatusSnapshotReader interface {
	ListManagementGroupStatusSnapshotRows(ctx context.Context, input ManagementGroupStatusSnapshotInput) ([]ManagementGroupStatusSnapshotRow, error)
}

type ManagementGroupAccountStatsReader interface {
	ListManagementGroupAccountStats(ctx context.Context, groupIDs []string) ([]ManagementGroupAccountStatsRow, error)
}

type ManagementGroupUsageReader interface {
	ListManagementGroupUsageTotals(ctx context.Context, inputs []ManagementGroupUsageLookupInput) ([]ManagementGroupUsageRow, error)
	ListManagementGroupUsageDaily(ctx context.Context, statDate string, inputs []ManagementGroupUsageLookupInput) ([]ManagementGroupUsageRow, error)
}

type ManagementGroupAuthorizationSourceReader interface {
	ListManagementGroupAuthorizationSources(ctx context.Context, authorizationIDs []string) ([]ManagementGroupAuthorizationSourceRow, error)
}

type ManagementGroupListReader interface {
	ManagementGroupListPageReader
	ManagementGroupAccountStatsReader
	ManagementGroupUsageReader
	ManagementGroupAuthorizationSourceReader
}

type ManagementGroupCreator interface {
	CreateManagementGroup(ctx context.Context, input ManagementGroupCreateInput) (ManagementGroupSummary, error)
}

type ManagementGroupUpdater interface {
	UpdateManagementGroup(ctx context.Context, input ManagementGroupUpdateInput) (ManagementGroupUpdateResult, error)
}

type ManagementGroupDeleter interface {
	DeleteManagementGroup(ctx context.Context, input ManagementGroupDeleteInput) (ManagementGroupDeleteResult, error)
}

type ManagementAccountOption struct {
	ID                                        string
	SystemAccountID                           string
	SystemAccountName                         string
	OwnerSystemAccountID                      string
	OwnerSystemAccountName                    string
	ProviderCode                              string
	ProviderProtocolProfileID                 string
	ProtocolCode                              string
	ProtocolVersion                           string
	Name                                      string
	Type                                      string
	Status                                    string
	AccessType                                string
	AccountAuthorizationID                    string
	AuthorizationStatus                       string
	AuthorizationExpiresAt                    *time.Time
	AuthorizationInstanceSourceAccountID      string
	AuthorizationInstanceOwnerSystemAccountID string
	AccountExpiresAt                          *time.Time
	HasActiveManualAuthorizationSource        bool
}

type ManagementAccountOptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	ProviderCode               string
	GroupID                    string
	TagIDs                     []string
	Type                       string
	Statuses                   []string
	Schedulable                string
	Limit                      int
	Offset                     int
}

type ManagementAccountTag struct {
	ID           string
	Name         string
	AccountCount int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ManagementAccountTagListInput struct {
	SystemAccountID string
}

type ManagementAccountTagDeleteInput struct {
	TagID           string
	SystemAccountID string
}

type ManagementAccountTagUpsertInput struct {
	ID   string
	Name string
}

type ManagementAccountTagUpdateInput struct {
	AccountID       string
	SystemAccountID string
	Tags            []ManagementAccountTagUpsertInput
}

type ManagementAccountTagUpdateResult struct {
	Account      ManagementAccountTagUpdateAccount
	PreviousTags []ManagementAccountTag
}

type ManagementAccountTagUpdateAccount struct {
	ID                   string
	SystemAccountID      string
	OwnerSystemAccountID string
	Name                 string
	Tags                 []ManagementAccountTag
}

var ErrManagementAccountTagInUse = errors.New("management account tag in use")

type ManagementAccountOptionReader interface {
	ListManagementAccountOptions(ctx context.Context, input ManagementAccountOptionListInput) ([]ManagementAccountOption, error)
	ListManagementAccountTags(ctx context.Context, input ManagementAccountTagListInput) ([]ManagementAccountTag, error)
	DeleteManagementAccountTag(ctx context.Context, input ManagementAccountTagDeleteInput) (bool, error)
	UpdateManagementAccountTags(ctx context.Context, input ManagementAccountTagUpdateInput) (ManagementAccountTagUpdateResult, bool, error)
}

type AccountAuthorizationGranteeReader interface {
	ListAccountAuthorizationGranteeIDs(ctx context.Context, accountID string) ([]string, error)
}
