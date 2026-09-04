package gatewayrouting

// RouteStrategyMode mirrors the Node RouteStrategyMode union
// (domain/types.ts): 'normal' | 'hybrid_smart' | 'weighted' | 'failover' |
// 'round_robin'.
const (
	RouteStrategyModeNormal      = "normal"
	RouteStrategyModeHybridSmart = "hybrid_smart"
	RouteStrategyModeWeighted    = "weighted"
	RouteStrategyModeFailover    = "failover"
	RouteStrategyModeRoundRobin  = "round_robin"
)

// Row status values (storage/gateway-api-key.repository.ts).
const (
	RowStatusActive   = "active"
	RowStatusDisabled = "disabled"
)

// GroupAccessType values (openai-account-selector.types.ts).
const (
	GroupAccessTypeOwner      = "owner"
	GroupAccessTypeAuthorized = "authorized"
)

// GatewayRequestEndpointFamily mirrors the Node GatewayRequestEndpointFamily
// union (domain/types.ts).
const (
	EndpointFamilyChatCompletions     = "chat_completions"
	EndpointFamilyResponses           = "responses"
	EndpointFamilyMessages            = "messages"
	EndpointFamilyGenerateContent     = "generate_content"
	EndpointFamilyStreamGenerate      = "stream_generate_content"
	EndpointFamilyCountTokens         = "count_tokens"
	EndpointFamilyEmbedContent        = "embed_content"
	EndpointFamilyInteractions        = "interactions"
	EndpointFamilyGeminiModelsPath    = "models"
)

// NormalGatewayModelRouteSource mirrors the Node
// NormalGatewayModelRouteSource union: where the routed model was resolved
// from.
type NormalGatewayModelRouteSource string

const (
	RouteSourceAccountMapping  NormalGatewayModelRouteSource = "account_mapping"
	RouteSourceCatalogProvider NormalGatewayModelRouteSource = "catalog_provider"
)

// APIKeyRow mirrors storage/gateway-api-key.repository.ts
// GatewayApiKeyRow, restricted to the fields the routing layer reads.
type APIKeyRow struct {
	ID                     string
	SystemAccountID        string
	RouteStrategyID        string
	RouteStrategyMode      string
	RouteStrategyConfigJSON string
	SelectedGroupID        string
	Status                 string
	GroupBindings          []GroupBindingRow
}

// GroupBindingRow mirrors storage/gateway-api-key.repository.ts
// GatewayApiKeyGroupBindingRow. Weight is a pointer because Node treats
// undefined/null weight as the default 1 while rejecting out-of-range
// integers (normalizeApiKeyGroupBindingWeight).
type GroupBindingRow struct {
	ID               string
	APIKeyID         string
	SystemAccountID  string
	GroupID          string
	Priority         int64
	Weight           *int64
	Status           string
	ProviderCode     string
	GroupEnabled     int64 // Node group_enabled !== 0 marks the group enabled.
}

// GroupUsageAccessMetadata mirrors storage/openai-account-selector.types.ts
// GroupUsageAccessMetadata. Tri-state optional booleans stay pointers.
type GroupUsageAccessMetadata struct {
	GroupOwnerSystemAccountID         string
	ProviderCode                      string
	GroupAccessType                   string
	GroupType                         string
	SchedulingPolicy                  string
	GroupAuthorizationID              string
	GroupAuthorizationExpiresAt       string
	GroupAuthorizationQuotaLimited    *bool
	GroupAuthorizationSourceType      string
	GroupAuthorizationSourceTeamID    string
}

// UpstreamAccount is the routing-layer projection of Node
// OpenAIAccountSecret (the same projection philosophy as
// internal/gatewayanthropic.UpstreamAccount). It carries exactly the fields
// the model filter and selection result need; credential material stays in
// the dispatch layer.
type UpstreamAccount struct {
	ID                        string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	SupportedModels           []string
	ModelMappings             []gatewayAccountModelMapping
}

// gatewayAccountModelMapping mirrors the AccountModelMapping row shape the
// mapping resolver consumes. It is declared locally (instead of importing
// internal/gatewayopenai's type) so this package stays the single owner of
// its row vocabulary; the projection to gatewayopenai.RuntimeAccount happens
// in runtimeAccount().
type gatewayAccountModelMapping struct {
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
	Enabled                *bool
	RuntimeSource          string
	RuntimeRouteRuleID     string
}

// ResponseInspectionPolicySummary mirrors
// storage/response-inspection-policy.repository.ts
// ResponseInspectionPolicySummary. The normal-route slice always emits an
// empty slice (Node sets responseInspectionPolicies: []); the concrete
// policy materialization belongs to the inspection slices.
type ResponseInspectionPolicySummary struct {
	ID           string
	DefaultRule  bool
	Editable     bool
	Name         string
	Enabled      bool
	Priority     int64
	ScopeType    string
	ProtocolCode string
	ProviderCode string
	Notes        string
	CreatedAt    string
	UpdatedAt    string
}
