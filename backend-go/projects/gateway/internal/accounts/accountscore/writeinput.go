package accountscore

// Write-path input contracts shared between the facade write surface and the
// accountstransfer subdomain (REFACTOR-0005 阶段 C 下沉). These are pure DTO
// definitions — the normalization/validation logic that consumes them stays in
// the facade (设计 v2：写入侧归一化留门面) and reaches the transfer subdomain
// through the Deps function ports.

// CreationStatus mirrors accountCreationStatusInput: the user-facing creation
// choice plus the derived guarded write flags (Node overrides the body fields
// with these in accounts.routes.ts).
type CreationStatus struct {
	Status                 string
	SkipInitialHealthCheck bool
	Schedulable            bool
}

// AccountCreationStatusInput mirrors accountCreationStatusInput.
func AccountCreationStatusInput(value any) CreationStatus {
	status := "pending_test"
	if text, ok := value.(string); ok && (text == "active" || text == "disabled") {
		status = text
	}
	return CreationStatus{
		Status:                 status,
		SkipInitialHealthCheck: status == "active",
		Schedulable:            status == "active",
	}
}

// CreateInput is the validated create payload (accountCreateSchema subset the
// store consumes); nil pointers mean the field was absent.
type CreateInput struct {
	ProviderCode              string
	ProviderProtocolProfileID string
	Name                      string
	AccountType               string
	Credentials               Credentials
	SupportedModels           []string
	HealthCheckModel          *string
	HealthCheckEndpointMode   *string
	ModelMappings             []ModelMapping
	Tags                      []string
	Status                    CreationStatus
	ConcurrencyLimit          *int
	Priority                  *int
	SuperPriorityEnabled      *bool
	FallbackEnabled           *bool
	ProxyProfileID            *string
	GroupID                   *string
	AccountExpiresAt          *string
	AvailabilitySchedule      any
	Notes                     *string
	BalanceQueryEnabled       bool
	// BalanceQueryConfigCanonical carries the normalized config JSON (the
	// create body parser already ran normalizeAccountBalanceConfig, exactly
	// like the Node route); nil means the request did not include a config.
	BalanceQueryConfigCanonical *string
	// TemporaryUnavailableContinuousProbeEnabled mirrors the
	// normalizeOptionalBooleanInput tri-state: nil = not provided (defaults
	// to enabled), false persists the explicit opt-out.
	TemporaryUnavailableContinuousProbeEnabled *bool
}

// EndpointModeDefaultContext mirrors the exported credential write context
// (credentials_normalize.go): the resolved provider/protocol identity the
// write-side normalization resolves endpoint modes against. A nil context at
// the facade entry means Node's `{ accountType }` fallback.
type EndpointModeDefaultContext struct {
	ProviderCode              string
	AccountType               string
	ClientCompatibility       string
	ProtocolCode              string
	ProtocolVersion           string
	ProviderProtocolProfileID string
}

// Tag validation limits（原门面 store.go maxTagNameLength/maxTagsPerAccount，
// 导入域标签解析器共用；门面保留同名常量别名）.
const (
	MaxTagNameLength  = 40
	MaxTagsPerAccount = 24
)

// AccountHealthCheckEndpointModes mirrors the allowed health-check endpoint
// mode set（原门面 write.go accountHealthCheckEndpointModes 常量表，导入域字段
// 解析器共用；门面保留同名别名）.
var AccountHealthCheckEndpointModes = map[string]bool{
	"images_json": true, "chat_json": true, "chat_sse": true,
	"responses_json": true, "responses_sse": true,
	"messages_json": true, "messages_sse": true,
	"generate_content_json": true, "generate_content_sse": true,
	"interactions_json": true, "interactions_sse": true,
}
