package managementaccountdraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

var (
	ErrInvalid              = errors.New("账户草稿测试参数无效")
	ErrGroupInvalid         = errors.New("账户分组无效")
	ErrProviderInvalid      = errors.New("账户供应商或协议档案不可用")
	ErrBalanceUnsupported   = errors.New("当前账户不支持上游余额查询")
	ErrBalanceConfigInvalid = errors.New("余额查询配置无效")
)

var (
	jsonPointerPattern = regexp.MustCompile(`^(?:/(?:[^~/]|~[01])*)*$`)
	decimalPattern     = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
)

type ValidationError struct {
	Cause   error
	Message string
}

func (e *ValidationError) Error() string {
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return ErrInvalid.Error()
}

func (e *ValidationError) Unwrap() error {
	if e.Cause != nil {
		return e.Cause
	}
	return ErrInvalid
}

type GroupReader interface {
	ListManagementGroupOptions(context.Context, port.ManagementGroupOptionListInput) ([]port.ManagementGroupOption, error)
}

type ProviderReader interface {
	ListManagementProviderOptions(context.Context, port.ManagementProviderOptionListInput) ([]port.ManagementProviderOption, error)
}

type Options struct {
	Groups    GroupReader
	Providers ProviderReader
	NewID     func(string) string
}

type Service struct {
	groups    GroupReader
	providers ProviderReader
	newID     func(string) string
}

type ModelMapping struct {
	SourceModel            string `json:"sourceModel"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily"`
	UpstreamModel          string `json:"upstreamModel"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily"`
	Enabled                *bool  `json:"enabled,omitempty"`
}

type Account struct {
	ProviderCode              string         `json:"providerCode"`
	ProviderProtocolProfileID string         `json:"providerProtocolProfileId"`
	Name                      string         `json:"name"`
	Type                      string         `json:"type"`
	Credentials               map[string]any `json:"credentials,omitempty"`
	SupportedModels           []string       `json:"supportedModels,omitempty"`
	HealthCheckModel          string         `json:"healthCheckModel"`
	HealthCheckEndpointMode   string         `json:"healthCheckEndpointMode"`
	ModelMappings             []ModelMapping `json:"modelMappings,omitempty"`
	ConcurrencyLimit          int            `json:"concurrencyLimit,omitempty"`
	Priority                  int            `json:"priority,omitempty"`
	SuperPriorityEnabled      bool           `json:"superPriorityEnabled,omitempty"`
	FallbackEnabled           bool           `json:"fallbackEnabled,omitempty"`
	ProxyProfileID            *string        `json:"proxyProfileId,omitempty"`
	GroupID                   string         `json:"groupId"`
	AccountExpiresAt          *string        `json:"accountExpiresAt,omitempty"`
	AvailabilitySchedule      map[string]any `json:"availabilitySchedule,omitempty"`
	Notes                     string         `json:"notes,omitempty"`
}

type Input struct {
	Access  port.ManagementAccountTestAccess
	Account Account
}

type Snapshot struct {
	ID                        string         `json:"id"`
	OwnerSystemAccountID      string         `json:"ownerSystemAccountId"`
	GroupID                   string         `json:"groupId"`
	GroupName                 string         `json:"groupName"`
	ProviderCode              string         `json:"providerCode"`
	ProviderProtocolProfileID string         `json:"providerProtocolProfileId"`
	ProtocolCode              string         `json:"protocolCode"`
	ProtocolVersion           string         `json:"protocolVersion"`
	Name                      string         `json:"name"`
	Type                      string         `json:"type"`
	Credentials               map[string]any `json:"credentials"`
	ConcurrencyLimit          int            `json:"concurrencyLimit"`
	Priority                  int            `json:"priority"`
	SuperPriorityEnabled      bool           `json:"superPriorityEnabled"`
	FallbackEnabled           bool           `json:"fallbackEnabled"`
	ClientCompatibility       string         `json:"clientCompatibility"`
	SupportedModels           []string       `json:"supportedModels"`
	HealthCheckModel          string         `json:"healthCheckModel"`
	HealthCheckEndpointMode   string         `json:"healthCheckEndpointMode"`
	ModelMappings             []ModelMapping `json:"modelMappings"`
	ProxyProfileID            string         `json:"proxyProfileId,omitempty"`
	AccountExpiresAt          string         `json:"accountExpiresAt,omitempty"`
	AvailabilitySchedule      map[string]any `json:"availabilitySchedule,omitempty"`
	Notes                     string         `json:"notes,omitempty"`
}

type BalanceCustomConfig struct {
	Path             string `json:"path"`
	RemainingPointer string `json:"remainingPointer,omitempty"`
	TotalPointer     string `json:"totalPointer,omitempty"`
	UsedPointer      string `json:"usedPointer,omitempty"`
	Divisor          string `json:"divisor,omitempty"`
}

type BalanceQueryConfig struct {
	Adapter                 string               `json:"adapter"`
	IntervalMinutes         int                  `json:"intervalMinutes,omitempty"`
	PreferredBuiltinAdapter string               `json:"preferredBuiltinAdapter,omitempty"`
	Custom                  *BalanceCustomConfig `json:"custom,omitempty"`
}

func NewService(opts Options) *Service {
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string { return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "") }
	}
	return &Service{groups: opts.Groups, providers: opts.Providers, newID: newID}
}

func (s *Service) Prepare(ctx context.Context, input Input) (Snapshot, error) {
	if s == nil || s.groups == nil || s.providers == nil {
		return Snapshot{}, fmt.Errorf("management account draft dependencies are required")
	}
	account := normalizeAccount(input.Account)
	if err := validateBasicAccount(account, input.Access); err != nil {
		return Snapshot{}, err
	}

	groups, err := s.groups.ListManagementGroupOptions(ctx, port.ManagementGroupOptionListInput{
		SystemAccountID:            strings.TrimSpace(input.Access.FilterSystemAccountID),
		IncludeSystemAccountFields: true,
		IDs:                        []string{account.GroupID},
		Limit:                      1,
		ManageableOnly:             true,
	})
	if err != nil {
		return Snapshot{}, err
	}
	if len(groups) != 1 || strings.TrimSpace(groups[0].ID) != account.GroupID || strings.TrimSpace(groups[0].ProviderCode) != account.ProviderCode {
		return Snapshot{}, validation(ErrGroupInvalid, ErrGroupInvalid.Error())
	}
	group := groups[0]
	owner := strings.TrimSpace(group.OwnerSystemAccountID)
	if owner == "" {
		owner = strings.TrimSpace(group.SystemAccountID)
	}
	if owner == "" {
		return Snapshot{}, validation(ErrGroupInvalid, "账户分组缺少归属用户，无法测试")
	}

	providers, err := s.providers.ListManagementProviderOptions(ctx, port.ManagementProviderOptionListInput{SystemAccountID: owner})
	if err != nil {
		return Snapshot{}, err
	}
	provider, profile := findProviderProfile(providers, account.ProviderCode, account.ProviderProtocolProfileID)
	if provider == nil || profile == nil || !provider.Enabled || !profile.Enabled || !contains(profile.AccountTypes, account.Type) {
		return Snapshot{}, validation(ErrProviderInvalid, fmt.Sprintf("供应商 %s 不支持账户类型 %s", account.ProviderCode, account.Type))
	}
	if !supportedProtocol(profile.ProtocolCode) {
		return Snapshot{}, validation(ErrProviderInvalid, "当前协议档案不支持账户草稿测试")
	}

	credentials, err := normalizeCredentials(account, *profile)
	if err != nil {
		return Snapshot{}, err
	}
	supportedModels := normalizedStrings(account.SupportedModels, 500)
	if len(supportedModels) == 0 {
		supportedModels = normalizedStrings(provider.DefaultSupportedModels, 500)
	}
	if len(supportedModels) == 0 || !contains(supportedModels, account.HealthCheckModel) {
		return Snapshot{}, validation(ErrInvalid, "账户检查模型必须包含在支持模型中")
	}
	if err := validateEndpoint(account, credentials, profile.ProtocolCode); err != nil {
		return Snapshot{}, err
	}
	if err := validateModelMappings(account.ModelMappings); err != nil {
		return Snapshot{}, err
	}

	snapshot := Snapshot{
		ID: s.newID("acctdraft"), OwnerSystemAccountID: owner, GroupID: account.GroupID, GroupName: strings.TrimSpace(group.Name),
		ProviderCode: account.ProviderCode, ProviderProtocolProfileID: account.ProviderProtocolProfileID,
		ProtocolCode: strings.TrimSpace(profile.ProtocolCode), ProtocolVersion: strings.TrimSpace(profile.ProtocolVersion),
		Name: account.Name, Type: account.Type, Credentials: credentials,
		ConcurrencyLimit: account.ConcurrencyLimit, Priority: account.Priority,
		SuperPriorityEnabled: account.SuperPriorityEnabled, FallbackEnabled: account.FallbackEnabled,
		ClientCompatibility: clientCompatibility(account.ProviderCode, account.Type, profile.ProtocolCode),
		SupportedModels:     supportedModels, HealthCheckModel: account.HealthCheckModel,
		HealthCheckEndpointMode: account.HealthCheckEndpointMode, ModelMappings: append([]ModelMapping(nil), account.ModelMappings...),
		AvailabilitySchedule: cloneMap(account.AvailabilitySchedule), Notes: account.Notes,
	}
	if account.ProxyProfileID != nil {
		snapshot.ProxyProfileID = strings.TrimSpace(*account.ProxyProfileID)
	}
	if account.AccountExpiresAt != nil {
		snapshot.AccountExpiresAt = strings.TrimSpace(*account.AccountExpiresAt)
	}
	return snapshot, nil
}

func (s Snapshot) Map() (map[string]any, error) {
	payload, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func ValidateBalanceDraft(snapshot Snapshot, config BalanceQueryConfig) error {
	if snapshot.Type != "api_key" {
		return validation(ErrBalanceUnsupported, "上游余额查询仅支持 API Key 账户")
	}
	if len(effectiveAPIKeys(snapshot.Credentials)) != 1 {
		return validation(ErrBalanceUnsupported, "上游余额查询需要一个有效的 API Key")
	}
	_, err := NormalizeBalanceConfig(config)
	return err
}

func ValidateTestEndpoint(snapshot Snapshot, endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || !contains(protocolEndpointModes(snapshot.ProviderCode, snapshot.ProtocolCode), endpoint) {
		return validation(ErrInvalid, "账户检查协议与供应商协议档案不兼容")
	}
	if raw, exists := snapshot.Credentials["supported_endpoint_modes"]; exists {
		modes, ok := stringValues(raw)
		if !ok || !contains(modes, endpoint) {
			return validation(ErrInvalid, "账户检查协议未在上游接口能力中启用")
		}
	}
	return nil
}

func NormalizeBalanceConfig(config BalanceQueryConfig) (BalanceQueryConfig, error) {
	config.Adapter = strings.TrimSpace(config.Adapter)
	config.PreferredBuiltinAdapter = strings.TrimSpace(config.PreferredBuiltinAdapter)
	if config.IntervalMinutes == 0 {
		config.IntervalMinutes = 5
	}
	if config.IntervalMinutes < 1 || config.IntervalMinutes > 10 {
		return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, "余额刷新周期必须在 1 到 10 分钟之间")
	}
	switch config.Adapter {
	case "builtin":
		if config.Custom != nil || config.PreferredBuiltinAdapter != "" && !contains([]string{"sub2api", "newapi", "litellm", "user_balance"}, config.PreferredBuiltinAdapter) {
			return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, ErrBalanceConfigInvalid.Error())
		}
	case "custom":
		if config.Custom == nil || config.PreferredBuiltinAdapter != "" {
			return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, "自定义查询必须提供查询配置")
		}
		custom := *config.Custom
		custom.Path = strings.TrimSpace(custom.Path)
		custom.RemainingPointer = strings.TrimSpace(custom.RemainingPointer)
		custom.TotalPointer = strings.TrimSpace(custom.TotalPointer)
		custom.UsedPointer = strings.TrimSpace(custom.UsedPointer)
		custom.Divisor = strings.TrimSpace(custom.Divisor)
		if !strings.HasPrefix(custom.Path, "/") || strings.HasPrefix(custom.Path, "//") {
			return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, "自定义查询地址必须是同源相对路径")
		}
		for _, pointer := range []string{custom.RemainingPointer, custom.TotalPointer, custom.UsedPointer} {
			if pointer != "" && !jsonPointerPattern.MatchString(pointer) {
				return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, "余额字段必须是合法 JSON Pointer")
			}
		}
		hasRemaining := custom.RemainingPointer != ""
		hasTotalAndUsed := custom.TotalPointer != "" && custom.UsedPointer != ""
		if hasRemaining == hasTotalAndUsed {
			return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, "自定义查询必须配置余额 JSON Pointer，或同时配置总额和已用 JSON Pointer")
		}
		if custom.Divisor != "" && (!decimalPattern.MatchString(custom.Divisor) || decimalIsZero(custom.Divisor)) {
			return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, "自定义金额除数必须是正数")
		}
		config.Custom = &custom
	default:
		return BalanceQueryConfig{}, validation(ErrBalanceConfigInvalid, "余额查询类型无效")
	}
	return config, nil
}

func normalizeAccount(account Account) Account {
	account.ProviderCode = strings.TrimSpace(account.ProviderCode)
	account.ProviderProtocolProfileID = strings.TrimSpace(account.ProviderProtocolProfileID)
	account.Name = strings.TrimSpace(account.Name)
	account.Type = strings.TrimSpace(account.Type)
	account.HealthCheckModel = strings.TrimSpace(account.HealthCheckModel)
	account.HealthCheckEndpointMode = strings.TrimSpace(account.HealthCheckEndpointMode)
	account.GroupID = strings.TrimSpace(account.GroupID)
	account.Notes = strings.TrimSpace(account.Notes)
	if account.ConcurrencyLimit == 0 {
		account.ConcurrencyLimit = 20
	}
	return account
}

func validateBasicAccount(account Account, access port.ManagementAccountTestAccess) error {
	if strings.TrimSpace(access.ActorSystemAccountID) == "" || account.ProviderCode == "" || account.ProviderProtocolProfileID == "" || account.Name == "" || account.Type == "" || account.HealthCheckModel == "" || account.GroupID == "" || account.HealthCheckEndpointMode == "" {
		return validation(ErrInvalid, ErrInvalid.Error())
	}
	if account.ConcurrencyLimit < 1 || account.ConcurrencyLimit > 100000 || account.Priority < 0 || account.SuperPriorityEnabled && account.FallbackEnabled {
		return validation(ErrInvalid, ErrInvalid.Error())
	}
	if len(account.SupportedModels) > 500 || len(account.ModelMappings) > 500 {
		return validation(ErrInvalid, ErrInvalid.Error())
	}
	if account.AccountExpiresAt != nil && strings.TrimSpace(*account.AccountExpiresAt) != "" {
		if _, err := time.Parse(time.RFC3339, strings.TrimSpace(*account.AccountExpiresAt)); err != nil {
			return validation(ErrInvalid, "账户到期时间无效")
		}
	}
	if account.AvailabilitySchedule != nil {
		if _, err := json.Marshal(account.AvailabilitySchedule); err != nil {
			return validation(ErrInvalid, "账户可用时段无效")
		}
	}
	return nil
}

func findProviderProfile(providers []port.ManagementProviderOption, providerCode, profileID string) (*port.ManagementProviderOption, *port.ManagementProviderProtocolProfile) {
	for index := range providers {
		provider := &providers[index]
		if strings.TrimSpace(provider.Code) != providerCode {
			continue
		}
		for profileIndex := range provider.ProtocolProfiles {
			profile := &provider.ProtocolProfiles[profileIndex]
			if strings.TrimSpace(profile.ID) == profileID && strings.TrimSpace(profile.ProviderCode) == providerCode {
				return provider, profile
			}
		}
		return provider, nil
	}
	return nil, nil
}

func normalizeCredentials(account Account, profile port.ManagementProviderProtocolProfile) (map[string]any, error) {
	credentials := cloneMap(account.Credentials)
	if len(credentials) == 0 {
		return nil, validation(ErrInvalid, "账户凭据不能为空")
	}
	if account.Type == "api_key" && len(effectiveAPIKeys(credentials)) == 0 {
		return nil, validation(ErrInvalid, "API Key 账户必须提供有效凭据")
	}
	if account.Type == "oauth" && strings.TrimSpace(textValue(credentials["base_url"])) == "" {
		baseURL := strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		credentials["base_url"] = baseURL
	}
	return credentials, nil
}

func validateEndpoint(account Account, credentials map[string]any, protocol string) error {
	mode := account.HealthCheckEndpointMode
	if !contains(protocolEndpointModes(account.ProviderCode, protocol), mode) {
		return validation(ErrInvalid, "账户检查协议与供应商协议档案不兼容")
	}
	if raw, exists := credentials["supported_endpoint_modes"]; exists {
		modes, ok := stringValues(raw)
		if !ok || len(modes) == 0 || !contains(modes, mode) {
			return validation(ErrInvalid, "账户检查协议未在上游接口能力中启用")
		}
	}
	return nil
}

func validateModelMappings(mappings []ModelMapping) error {
	sources := []string{"chat_completions", "responses", "messages", "generate_content", "stream_generate_content"}
	upstreams := []string{"chat_completions", "responses", "messages", "generate_content"}
	for _, mapping := range mappings {
		if strings.TrimSpace(mapping.SourceModel) == "" || strings.TrimSpace(mapping.UpstreamModel) == "" || !contains(sources, mapping.SourceEndpointFamily) || !contains(upstreams, mapping.UpstreamEndpointFamily) {
			return validation(ErrInvalid, "账户模型映射无效")
		}
	}
	return nil
}

func protocolEndpointModes(providerCode, protocol string) []string {
	if strings.EqualFold(strings.TrimSpace(providerCode), "hybrid") {
		return []string{"chat_json", "chat_sse", "responses_json", "responses_sse", "messages_json", "messages_sse", "generate_content_json", "generate_content_sse"}
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}
	case "anthropic":
		return []string{"messages_json", "messages_sse"}
	case "gemini":
		return []string{"generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse"}
	default:
		return nil
	}
}

func supportedProtocol(protocol string) bool {
	return contains([]string{"openai", "anthropic", "gemini"}, strings.ToLower(strings.TrimSpace(protocol)))
}

func clientCompatibility(providerCode, accountType, protocol string) string {
	if strings.EqualFold(strings.TrimSpace(providerCode), "gpt") && strings.EqualFold(strings.TrimSpace(protocol), "openai") && (accountType == "oauth" || accountType == "api_key") {
		return "codex_responses"
	}
	return "openai_standard"
}

func effectiveAPIKeys(credentials map[string]any) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0)
	if values, ok := stringValues(credentials["api_keys"]); ok {
		for _, value := range values {
			if value == "" {
				continue
			}
			if _, exists := seen[value]; !exists {
				seen[value] = struct{}{}
				result = append(result, value)
			}
		}
	}
	if len(result) == 0 {
		if value := strings.TrimSpace(textValue(credentials["api_key"])); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func normalizedStrings(values []string, limit int) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func stringValues(value any) ([]string, bool) {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []string:
		raw = make([]any, len(typed))
		for index := range typed {
			raw[index] = typed[index]
		}
	default:
		return nil, false
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		text = strings.TrimSpace(text)
		if text != "" {
			result = append(result, text)
		}
	}
	return result, true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) {
			return true
		}
	}
	return false
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func textValue(value any) string {
	text, _ := value.(string)
	return text
}

func decimalIsZero(value string) bool {
	number, err := strconv.ParseFloat(value, 64)
	return err != nil || number == 0
}

func validation(cause error, message string) error {
	return &ValidationError{Cause: cause, Message: message}
}
