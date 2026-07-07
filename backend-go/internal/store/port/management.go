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
