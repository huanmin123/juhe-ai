package port

import (
	"context"
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

type ManagementProviderModelCatalogReader interface {
	FindManagementProviderModelProvider(ctx context.Context, code string) (ManagementProviderModelProvider, bool, error)
	ListManagementEnabledModelProviderCodes(ctx context.Context) ([]string, error)
	ListManagementProviderCodesByProtocol(ctx context.Context, protocolCode string, protocolVersion string) ([]string, error)
	ListManagementProviderModelCatalog(ctx context.Context, input ManagementProviderModelCatalogListInput) ([]ManagementProviderModelCatalogItem, error)
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
	AccountIDs             []string
}

type ManagementGroupOptionListInput struct {
	SystemAccountID            string
	IncludeSystemAccountFields bool
	IDs                        []string
	Keyword                    string
	ProviderCode               string
	Limit                      int
	PreferDefault              bool
}

type ManagementGroupOptionReader interface {
	ListManagementGroupOptions(ctx context.Context, input ManagementGroupOptionListInput) ([]ManagementGroupOption, error)
	ListManagementGroupAccountOptions(ctx context.Context, input ManagementGroupOptionListInput) ([]ManagementGroupAccountOption, error)
}

type ManagementAccountOption struct {
	ID                        string
	SystemAccountID           string
	SystemAccountName         string
	OwnerSystemAccountID      string
	OwnerSystemAccountName    string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	Name                      string
	Type                      string
	Status                    string
	AccountExpiresAt          *time.Time
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

type ManagementAccountOptionReader interface {
	ListManagementAccountOptions(ctx context.Context, input ManagementAccountOptionListInput) ([]ManagementAccountOption, error)
	ListManagementAccountTags(ctx context.Context, input ManagementAccountTagListInput) ([]ManagementAccountTag, error)
}
