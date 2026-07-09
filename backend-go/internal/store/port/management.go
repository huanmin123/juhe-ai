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

type ManagementSessionSummary struct {
	ID         string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
}

type ManagementSessionListInput struct {
	SystemAccountID string
	Now             time.Time
	Limit           int
	Offset          int
}

type ManagementSessionListResult struct {
	Items   []ManagementSessionSummary
	HasMore bool
}

type ManagementSessionRevokeInput struct {
	SystemAccountID string
	SessionID       string
}

type ManagementSessionManager interface {
	ListManagementSessionsForAccount(ctx context.Context, input ManagementSessionListInput) (ManagementSessionListResult, error)
	RevokeManagementSessionForAccount(ctx context.Context, input ManagementSessionRevokeInput) (bool, error)
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

type ManagementProxyOptionListInput struct {
	Keyword string
	Limit   int
}

type ManagementProxyOptionReader interface {
	ListManagementProxyOptions(ctx context.Context, input ManagementProxyOptionListInput) ([]ManagementProxyOption, error)
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
	ID                     string
	SystemAccountID        string
	SystemAccountName      string
	OwnerSystemAccountID   string
	OwnerSystemAccountName string
	Name                   string
	ProviderCode           string
	Enabled                bool
	IsDefault              bool
	GroupType              string
	SchedulingPolicy       map[string]any
	AccessType             string
}

type ManagementAuthorizationGranteeGroupOptionListInput struct {
	GranteeSystemAccountID     string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	ProviderCode               string
	Limit                      int
	PreferDefault              bool
}

type ManagementAuthorizationOptionReader interface {
	ListManagementAuthorizationGranteeAccounts(ctx context.Context, input ManagementAuthorizationPrincipalOptionListInput) ([]ManagementAuthorizationGranteeAccountOption, error)
	ListManagementAuthorizationGranteeTeams(ctx context.Context, input ManagementAuthorizationPrincipalOptionListInput) ([]ManagementAuthorizationGranteeTeamOption, error)
	ListManagementAuthorizationGranteeGroups(ctx context.Context, input ManagementAuthorizationGranteeGroupOptionListInput) ([]ManagementAuthorizationGranteeGroupOption, error)
}

type ManagementProviderEndpointFamily struct {
	Code        string
	Name        string
	Description string
}

type ManagementProviderProtocolProfile struct {
	ID               string
	ProviderCode     string
	Name             string
	Description      string
	Enabled          bool
	ProtocolCode     string
	ProtocolVersion  string
	BaseURL          string
	DefaultTestModel string
	AccountTypes     []string
	Capabilities     []string
	EndpointFamilies []ManagementProviderEndpointFamily
}

type ManagementProviderOption struct {
	ID                       string
	Code                     string
	Name                     string
	ParentCode               string
	Description              string
	Enabled                  bool
	DefaultProtocolProfileID string
	ProtocolCode             string
	ProtocolVersion          string
	BaseURL                  string
	DefaultTestModel         string
	DefaultSupportedModels   []string
	AccountTypes             []string
	Capabilities             []string
	ProtocolProfiles         []ManagementProviderProtocolProfile
}

type ManagementProviderOptionListInput struct {
	SystemAccountID string
}

type ManagementProviderOptionReader interface {
	ListManagementProviderOptions(ctx context.Context, input ManagementProviderOptionListInput) ([]ManagementProviderOption, error)
}

type ManagementProviderModelProvider struct {
	Code       string
	Enabled    bool
	ParentCode string
}

type ManagementProviderModelCatalogItem struct {
	ID                    string
	ProviderCode          string
	Model                 string
	Scope                 string
	SystemAccountID       string
	Status                string
	Mode                  string
	CatalogOrder          *int
	ReleaseDate           string
	ShutdownDate          string
	SupportedAPIProtocols []string
	PricingModel          string
	ContextWindowTokens   *int
	MaxInputTokens        *int
	MaxOutputTokens       *int
	MaxTokens             *int
	InputUSDPer1M         *float64
	OutputUSDPer1M        *float64
	CachedInputUSDPer1M   *float64
	CacheWriteUSDPer1M    *float64
	CacheWrite1hUSDPer1M  *float64
	ImageInputUSDPer1M    *float64
	ImageOutputUSDPer1M   *float64
	AudioInputUSDPer1M    *float64
	AudioOutputUSDPer1M   *float64
	OutputUSDPerImage     *float64
	SupportsPromptCaching bool
	SupportsServiceTier   bool
	CatalogVisible        bool
	Source                string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ManagementProviderModelCatalogListInput struct {
	BuiltInProviderCodes []string
	CustomProviderCodes  []string
	SystemAccountID      string
	IncludeInactive      bool
}

type ManagementProviderDefaultTestModelPreference struct {
	ProviderCode string
	Model        string
}

type ManagementProviderDefaultTestModelInput struct {
	ProviderCode    string
	SystemAccountID string
	Model           string
}

type ManagementProviderModelCatalogReader interface {
	FindManagementProviderModelProvider(ctx context.Context, code string) (ManagementProviderModelProvider, bool, error)
	ListManagementEnabledModelProviderCodes(ctx context.Context) ([]string, error)
	ListManagementProviderCodesByProtocol(ctx context.Context, protocolCode string, protocolVersion string) ([]string, error)
	ListManagementProviderModelCatalog(ctx context.Context, input ManagementProviderModelCatalogListInput) ([]ManagementProviderModelCatalogItem, error)
}

type ManagementProviderDefaultTestModelWriter interface {
	SetManagementProviderDefaultTestModel(ctx context.Context, input ManagementProviderDefaultTestModelInput) (ManagementProviderDefaultTestModelPreference, error)
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

type ManagementGroupOption struct {
	ID                     string
	SystemAccountID        string
	SystemAccountName      string
	OwnerSystemAccountID   string
	OwnerSystemAccountName string
	Name                   string
	ProviderCode           string
	Enabled                bool
	IsDefault              bool
	GroupType              string
	SchedulingPolicy       map[string]any
	AccessType             string
	GroupAuthorizationID   string
	AuthorizationStatus    string
	AuthorizationExpiresAt *time.Time
	AuthorizationLimits    map[string]any
}

type ManagementGroupAccountOption struct {
	ID                     string
	SystemAccountID        string
	SystemAccountName      string
	OwnerSystemAccountID   string
	OwnerSystemAccountName string
	Name                   string
	ProviderCode           string
	Enabled                bool
	IsDefault              bool
	GroupType              string
	SchedulingPolicy       map[string]any
	AccessType             string
	GroupAuthorizationID   string
	AuthorizationStatus    string
	AuthorizationExpiresAt *time.Time
	AuthorizationLimits    map[string]any
	AccountIDs             []string
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

type ManagementGroupOptionReader interface {
	ListManagementGroupOptions(ctx context.Context, input ManagementGroupOptionListInput) ([]ManagementGroupOption, error)
	ListManagementGroupAccountOptions(ctx context.Context, input ManagementGroupOptionListInput) ([]ManagementGroupAccountOption, error)
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
