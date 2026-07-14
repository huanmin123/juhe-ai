package managementaccounttestoptions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	hybridProviderCode = "hybrid"

	openAIProtocolCode        = "openai"
	openAIProtocolVersion     = "v1"
	anthropicProtocolCode     = "anthropic"
	anthropicProtocolVersion  = "v1"
	geminiProtocolCode        = "gemini"
	geminiProtocolVersion     = "v1beta"
	geminiOpenAIChatProfileID = "profile_gemini_openai_chat_v1beta"
	deepSeekAnthropicProfile  = "profile_deepseek_anthropic_v1"
	glmCodingAnthropicProfile = "profile_glm_coding_anthropic_v1"

	chatCompletionsFamily       = "chat_completions"
	responsesFamily             = "responses"
	messagesFamily              = "messages"
	generateContentFamily       = "generate_content"
	streamGenerateContentFamily = "stream_generate_content"
)

var (
	openAIEndpointModes = []string{
		"chat_json",
		"chat_sse",
		"responses_json",
		"responses_sse",
	}
	openAIChatEndpointModes = []string{
		"chat_json",
		"chat_sse",
	}
	openAIResponsesEndpointModes = []string{
		"responses_json",
		"responses_sse",
	}
	anthropicEndpointModes = []string{
		"messages_json",
		"messages_sse",
		"message_token_counting",
	}
	anthropicMessagesEndpointModes = []string{
		"messages_json",
		"messages_sse",
	}
	geminiEndpointModes = []string{
		"generate_content_json",
		"generate_content_sse",
		"count_tokens",
		"embed_content",
	}
	geminiDefaultEndpointModes = []string{
		"generate_content_json",
		"generate_content_sse",
		"count_tokens",
	}
	hybridEndpointModes = []string{
		"chat_json",
		"chat_sse",
		"responses_json",
		"responses_sse",
		"messages_json",
		"messages_sse",
		"message_token_counting",
		"generate_content_json",
		"generate_content_sse",
		"count_tokens",
		"embed_content",
	}
)

type ModelCatalog interface {
	Models(ctx context.Context, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error)
}

type CredentialCodec interface {
	DecryptJSON(value string) (map[string]any, error)
}

type ServiceOptions struct {
	Reader          port.ManagementAccountTestOptionsReader
	ModelCatalog    ModelCatalog
	CredentialCodec CredentialCodec
}

type Service struct {
	reader          port.ManagementAccountTestOptionsReader
	modelCatalog    ModelCatalog
	credentialCodec CredentialCodec
}

type Input struct {
	AccountID       string
	SystemAccountID string
}

type ModelOption struct {
	Model                 string   `json:"model"`
	SupportedAPIProtocols []string `json:"supportedApiProtocols"`
	TestEndpointModes     []string `json:"testEndpointModes"`
}

type Result struct {
	AccountID               string        `json:"accountId"`
	DefaultModel            string        `json:"defaultModel"`
	Models                  []ModelOption `json:"models"`
	TestEndpointModes       []string      `json:"testEndpointModes"`
	DefaultTestEndpointMode string        `json:"defaultTestEndpointMode"`
}

type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

func ValidationMessage(err error) (string, bool) {
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		return "", false
	}
	if strings.TrimSpace(validationErr.Message) == "" {
		return "请求参数无效", true
	}
	return validationErr.Message, true
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	return &Service{
		reader:          opts.Reader,
		modelCatalog:    opts.ModelCatalog,
		credentialCodec: opts.CredentialCodec,
	}
}

func (s *Service) Get(ctx context.Context, input Input) (Result, bool, error) {
	if s.reader == nil {
		return Result{}, false, fmt.Errorf("management account test options reader is required")
	}
	account, found, err := s.reader.GetManagementAccountTestOptionsSource(ctx, port.ManagementAccountTestOptionsInput{
		AccountID:       strings.TrimSpace(input.AccountID),
		SystemAccountID: strings.TrimSpace(input.SystemAccountID),
	})
	if err != nil || !found {
		return Result{}, found, err
	}
	healthCheckModel := strings.TrimSpace(account.HealthCheckModel)
	ownerSystemAccountID := strings.TrimSpace(account.OwnerSystemAccountID)
	if ownerSystemAccountID == "" {
		return Result{}, false, fmt.Errorf("账户归属数据异常，无法读取测试模型")
	}
	if s.credentialCodec == nil {
		return Result{}, false, fmt.Errorf("management account test options credential codec is required")
	}
	credentials, err := s.credentialCodec.DecryptJSON(account.CredentialsEncrypted)
	if err != nil {
		return Result{}, false, err
	}
	if s.modelCatalog == nil {
		return Result{}, false, fmt.Errorf("management account test options model catalog is required")
	}
	catalog, err := s.modelCatalog.Models(ctx, managementprovidermodels.ModelListInput{
		ProviderCode:    account.ProviderCode,
		SystemAccountID: ownerSystemAccountID,
		IncludeInactive: false,
		IncludeUnpriced: true,
	})
	if err != nil {
		return Result{}, false, err
	}

	accountEndpointModes := accountManualTestEndpointModes(account, credentials)
	eligibleModels := make([]ModelOption, 0, len(catalog))
	for _, item := range catalog {
		if item.Status != "active" || !isAccountManualTestModel(item, account) {
			continue
		}
		eligibleModels = append(eligibleModels, ModelOption{
			Model:                 item.Model,
			SupportedAPIProtocols: append([]string{}, item.SupportedAPIProtocols...),
			TestEndpointModes: accountManualTestEndpointModesForModel(
				account,
				item,
				catalog,
				accountEndpointModes,
			),
		})
	}
	defaultModel := findModel(eligibleModels, healthCheckModel)
	if defaultModel == nil {
		return Result{}, false, &ValidationError{Message: "账户检查模型已不在当前供应商可用目录中，请先修正账户检查模型：" + healthCheckModel}
	}

	testEndpointModes := append([]string{}, defaultModel.TestEndpointModes...)
	if len(testEndpointModes) == 0 {
		return Result{}, false, &ValidationError{Message: "账户上游接口能力中没有可用于连接测试的请求形态"}
	}

	models := make([]ModelOption, 0, len(eligibleModels))
	for _, model := range eligibleModels {
		if len(model.TestEndpointModes) > 0 {
			models = append(models, model)
		}
	}
	return Result{
		AccountID:               account.ID,
		DefaultModel:            healthCheckModel,
		Models:                  models,
		TestEndpointModes:       testEndpointModes,
		DefaultTestEndpointMode: testEndpointModes[0],
	}, true, nil
}

func findModel(models []ModelOption, model string) *ModelOption {
	for index := range models {
		if models[index].Model == model {
			return &models[index]
		}
	}
	return nil
}

func accountManualTestEndpointModesForModel(
	account port.ManagementAccountTestOptionsSource,
	model managementprovidermodels.ModelCatalogItem,
	catalog []managementprovidermodels.ModelCatalogItem,
	accountEndpointModes []string,
) []string {
	output := make([]string, 0, len(accountEndpointModes))
	for _, mode := range accountEndpointModes {
		sourceFamily := endpointModeProtocol(mode)
		mapping := resolveAccountModelMapping(account, model.Model, sourceFamily)
		if mapping == nil {
			if modelSupportsProtocol(model, sourceFamily) {
				output = append(output, mode)
			}
			continue
		}
		if !modelSupportsProtocol(model, sourceFamily) {
			continue
		}
		upstreamModel := findCatalogModel(catalog, mapping.UpstreamModel)
		if upstreamModel == nil || modelSupportsProtocol(*upstreamModel, mapping.UpstreamEndpointFamily) {
			output = append(output, mode)
		}
	}
	return output
}

func endpointModeProtocol(mode string) string {
	switch mode {
	case "chat_json", "chat_sse":
		return chatCompletionsFamily
	case "responses_json", "responses_sse":
		return responsesFamily
	case "messages_json", "messages_sse":
		return messagesFamily
	case "generate_content_sse":
		return streamGenerateContentFamily
	default:
		return generateContentFamily
	}
}

func modelSupportsProtocol(item managementprovidermodels.ModelCatalogItem, protocol string) bool {
	if len(item.SupportedAPIProtocols) == 0 {
		return true
	}
	for _, candidate := range item.SupportedAPIProtocols {
		if candidate == protocol {
			return true
		}
	}
	return false
}

func resolveAccountModelMapping(
	account port.ManagementAccountTestOptionsSource,
	requestedModel string,
	sourceEndpointFamily string,
) *port.ManagementAccountTestModelMapping {
	model := strings.TrimSpace(requestedModel)
	if model == "" || !isModelMappingSourceEndpointFamily(sourceEndpointFamily) {
		return nil
	}
	if account.ProviderProtocolProfileID == geminiOpenAIChatProfileID && sourceEndpointFamily == messagesFamily {
		return nil
	}
	for index := range account.ModelMappings {
		mapping := &account.ModelMappings[index]
		if !mapping.Enabled || mapping.SourceModel != model || mapping.SourceEndpointFamily != sourceEndpointFamily {
			continue
		}
		if mapping.UpstreamModel == mapping.SourceModel && mapping.UpstreamEndpointFamily == mapping.SourceEndpointFamily {
			return nil
		}
		if !isModelMappingRuntimeConversionSupported(*mapping, account) {
			return nil
		}
		return mapping
	}
	return nil
}

func isModelMappingSourceEndpointFamily(value string) bool {
	switch value {
	case chatCompletionsFamily, responsesFamily, messagesFamily, generateContentFamily, streamGenerateContentFamily:
		return true
	default:
		return false
	}
}

func isModelMappingRuntimeConversionSupported(
	mapping port.ManagementAccountTestModelMapping,
	account port.ManagementAccountTestOptionsSource,
) bool {
	source := mapping.SourceEndpointFamily
	upstream := mapping.UpstreamEndpointFamily
	if source == upstream || source == streamGenerateContentFamily && upstream == generateContentFamily {
		return true
	}
	if source == responsesFamily && upstream == chatCompletionsFamily && isProtocol(account, openAIProtocolCode, openAIProtocolVersion) {
		return true
	}
	if !isHybridProvider(account.ProviderCode) {
		return false
	}
	return source == responsesFamily && upstream == chatCompletionsFamily ||
		source == messagesFamily && upstream == chatCompletionsFamily ||
		(source == generateContentFamily || source == streamGenerateContentFamily) && upstream == chatCompletionsFamily ||
		source == chatCompletionsFamily && upstream == messagesFamily ||
		source == responsesFamily && upstream == messagesFamily ||
		(source == generateContentFamily || source == streamGenerateContentFamily) && upstream == messagesFamily ||
		source == chatCompletionsFamily && upstream == generateContentFamily ||
		source == responsesFamily && upstream == generateContentFamily ||
		source == messagesFamily && upstream == generateContentFamily
}

func findCatalogModel(
	catalog []managementprovidermodels.ModelCatalogItem,
	model string,
) *managementprovidermodels.ModelCatalogItem {
	for index := range catalog {
		if catalog[index].Model == model {
			return &catalog[index]
		}
	}
	return nil
}

func isAccountManualTestModel(item managementprovidermodels.ModelCatalogItem, account port.ManagementAccountTestOptionsSource) bool {
	if item.Mode == "image" || item.Mode == "audio" {
		return false
	}
	if len(item.SupportedAPIProtocols) == 0 {
		return true
	}

	var allowed map[string]struct{}
	switch {
	case isHybridProvider(account.ProviderCode):
		allowed = stringSet("chat_completions", "responses", "messages", "generate_content", "stream_generate_content")
	case isProtocol(account, openAIProtocolCode, openAIProtocolVersion):
		allowed = stringSet("chat_completions", "responses")
	case isProtocol(account, anthropicProtocolCode, anthropicProtocolVersion):
		allowed = stringSet("messages")
	case isProtocol(account, geminiProtocolCode, geminiProtocolVersion):
		allowed = stringSet("generate_content", "stream_generate_content")
	default:
		return false
	}
	for _, protocol := range item.SupportedAPIProtocols {
		if _, ok := allowed[protocol]; ok {
			return true
		}
	}
	return false
}

func accountManualTestEndpointModes(account port.ManagementAccountTestOptionsSource, credentials map[string]any) []string {
	enabled := stringSet(normalizedAccountEndpointModes(account, credentials)...)
	ordered := accountTestEndpointModeOrder(account)
	output := make([]string, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, mode := range ordered {
		if _, duplicate := seen[mode]; duplicate {
			continue
		}
		seen[mode] = struct{}{}
		if !isGenerationEndpointMode(mode) {
			continue
		}
		if _, ok := enabled[mode]; ok {
			output = append(output, mode)
		}
	}
	return output
}

func normalizedAccountEndpointModes(account port.ManagementAccountTestOptionsSource, credentials map[string]any) []string {
	raw := credentials["supported_endpoint_modes"]
	switch {
	case isHybridProvider(account.ProviderCode):
		return normalizeEndpointModesForRuntime(raw, hybridEndpointModes, hybridEndpointModes)
	case isProtocol(account, anthropicProtocolCode, anthropicProtocolVersion):
		defaults := anthropicEndpointModes
		if account.ProviderProtocolProfileID == deepSeekAnthropicProfile || account.ProviderProtocolProfileID == glmCodingAnthropicProfile {
			defaults = anthropicMessagesEndpointModes
		}
		return normalizeEndpointModesForRuntime(raw, anthropicEndpointModes, defaults)
	case isProtocol(account, geminiProtocolCode, geminiProtocolVersion):
		return normalizeEndpointModesForRuntime(raw, geminiEndpointModes, geminiDefaultEndpointModes)
	case isProtocol(account, openAIProtocolCode, openAIProtocolVersion):
		return normalizeEndpointModesForRuntime(raw, openAIEndpointModes, defaultOpenAIEndpointModes(account))
	default:
		return []string{}
	}
}

func defaultOpenAIEndpointModes(account port.ManagementAccountTestOptionsSource) []string {
	if account.Type == "oauth" {
		return openAIResponsesEndpointModes
	}
	switch normalizeToken(account.ProviderCode) {
	case "gpt":
		return openAIEndpointModes
	case "openai", "deepseek", "glm", "gemini", hybridProviderCode:
		return openAIChatEndpointModes
	}
	if account.ClientCompatibility == "codex_responses" {
		return openAIEndpointModes
	}
	return openAIEndpointModes
}

func normalizeEndpointModesForRuntime(raw any, allowed []string, defaults []string) []string {
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []string:
		values = make([]any, len(typed))
		for index, value := range typed {
			values[index] = value
		}
	default:
		return append([]string{}, defaults...)
	}

	allowedSet := stringSet(allowed...)
	output := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		mode, ok := value.(string)
		if !ok {
			continue
		}
		if _, ok = allowedSet[mode]; !ok {
			continue
		}
		if _, duplicate := seen[mode]; duplicate {
			continue
		}
		seen[mode] = struct{}{}
		output = append(output, mode)
	}
	if len(output) == 0 {
		return append([]string{}, defaults...)
	}
	return output
}

func accountTestEndpointModeOrder(account port.ManagementAccountTestOptionsSource) []string {
	defaultMode := account.HealthCheckEndpointMode
	switch {
	case isHybridProvider(account.ProviderCode):
		return []string{
			defaultMode,
			"chat_json",
			"chat_sse",
			"responses_json",
			"responses_sse",
			"messages_json",
			"messages_sse",
			"generate_content_json",
			"generate_content_sse",
		}
	case isProtocol(account, anthropicProtocolCode, anthropicProtocolVersion):
		return []string{defaultMode, "messages_json", "messages_sse"}
	case isProtocol(account, geminiProtocolCode, geminiProtocolVersion):
		return []string{defaultMode, "generate_content_json", "generate_content_sse"}
	case account.Type == "oauth":
		return []string{defaultMode, "responses_json", "responses_sse"}
	default:
		return []string{defaultMode, "chat_sse", "responses_sse", "chat_json", "responses_json"}
	}
}

func isGenerationEndpointMode(mode string) bool {
	switch mode {
	case "chat_json", "chat_sse", "responses_json", "responses_sse", "messages_json", "messages_sse", "generate_content_json", "generate_content_sse":
		return true
	default:
		return false
	}
}

func isHybridProvider(providerCode string) bool {
	return normalizeToken(providerCode) == hybridProviderCode
}

func isProtocol(account port.ManagementAccountTestOptionsSource, code string, version string) bool {
	return normalizeToken(account.ProtocolCode) == code && normalizeToken(account.ProtocolVersion) == version
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func stringSet(values ...string) map[string]struct{} {
	output := make(map[string]struct{}, len(values))
	for _, value := range values {
		output[value] = struct{}{}
	}
	return output
}
