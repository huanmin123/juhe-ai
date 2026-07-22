package port

import (
	"context"
	"time"
)

// GatewayClientCatalogReader is the read-only persistence boundary used by
// gateway model discovery. Implementations must apply personal visibility by
// SystemAccountID. Logical provider expansion must tag every returned row with
// RequestedProviderCode so the service can retain the original request scope.
type GatewayClientCatalogReader interface {
	ListGatewayClientCatalogProviders(ctx context.Context) ([]GatewayClientCatalogProvider, error)
	ListGatewayClientCatalogModels(ctx context.Context, input GatewayClientCatalogModelListInput) ([]GatewayClientCatalogModel, error)
}

type GatewayClientCatalogProvider struct {
	Code    string
	Enabled bool
}

type GatewayClientCatalogModelListInput struct {
	LogicalProviderCodes []string
	SystemAccountID      string
}

type GatewayClientCatalogModel struct {
	// RequestedProviderCode is the logical provider scope that produced this
	// row. It differs from ProviderCode when openai or hybrid expands to a
	// protocol-compatible source provider.
	RequestedProviderCode         string
	ProviderCode                  string
	Model                         string
	Scope                         string
	SystemAccountID               string
	Status                        string
	CatalogVisible                bool
	ReleaseDate                   string
	CreatedAt                     time.Time
	SupportedAPIProtocols         []string
	SupportedServiceTiers         []string
	CodexSupportedReasoningLevels []string
	CodexDefaultReasoningLevel    string
	CodexMultiAgentVersion        string
	ContextWindowTokens           *int
	MaxInputTokens                *int
	MaxOutputTokens               *int
	PricingNotes                  string
	CapabilityNotes               string
	Notes                         string

	InputUSDPer1M        *float64
	OutputUSDPer1M       *float64
	CachedInputUSDPer1M  *float64
	CacheWriteUSDPer1M   *float64
	CacheWrite1hUSDPer1M *float64
	ImageInputUSDPer1M   *float64
	ImageOutputUSDPer1M  *float64
	AudioInputUSDPer1M   *float64
	AudioOutputUSDPer1M  *float64
	OutputUSDPerImage    *float64
	ServiceTierPrices    map[string]GatewayClientCatalogPriceSet
}

type GatewayClientCatalogPriceSet struct {
	InputUSDPer1M        *float64
	OutputUSDPer1M       *float64
	CachedInputUSDPer1M  *float64
	CacheWriteUSDPer1M   *float64
	CacheWrite1hUSDPer1M *float64
	ImageInputUSDPer1M   *float64
	ImageOutputUSDPer1M  *float64
	AudioInputUSDPer1M   *float64
	AudioOutputUSDPer1M  *float64
	OutputUSDPerImage    *float64
}
