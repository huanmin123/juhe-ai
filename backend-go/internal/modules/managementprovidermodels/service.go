package managementprovidermodels

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	openAIProviderCode = "openai"
	hybridProviderCode = "hybrid"

	protocolOpenAI    = "openai"
	protocolAnthropic = "anthropic"
	protocolGemini    = "gemini"

	CustomProviderModelSavedReason   = "custom_provider_model_saved"
	CustomProviderModelDeletedReason = "custom_provider_model_deleted"
)

var ErrProviderNotFound = errors.New("provider not found")
var ErrCustomProviderModelNotFound = errors.New("custom provider model not found")

type Store interface {
	port.ManagementProviderModelCatalogReader
	port.ManagementProviderDefaultHealthCheckModelWriter
	port.ManagementCustomProviderModelWriter
}

type CustomProviderModelInvalidator interface {
	InvalidateCustomProviderModelChanged(ctx context.Context, reason string) error
}

type ServiceOptions struct {
	Store       Store
	Invalidator CustomProviderModelInvalidator
	NewID       func(prefix string) string
}

type Service struct {
	store       Store
	invalidator CustomProviderModelInvalidator
	newID       func(prefix string) string
}

type ModelOptionListInput struct {
	SystemAccountID string
	Protocol        string
}

type ModelListInput struct {
	ProviderCode    string
	SystemAccountID string
	IncludeInactive bool
	IncludeUnpriced bool
}

type DefaultHealthCheckModelInput struct {
	ProviderCode         string
	ActorSystemAccountID string
	ActorRole            string
	Model                string
}

type CustomModelMutation struct {
	Invalid                   bool
	Scope                     OptionalString
	Model                     OptionalString
	Status                    OptionalString
	Mode                      OptionalString
	SupportedAPIProtocols     OptionalStringList
	SupportedServiceTiers     OptionalStringList
	SupportedReasoningEfforts OptionalStringList
	DefaultReasoningEffort    OptionalString
	PricingModel              OptionalString
	ReleaseDate               OptionalString
	ShutdownDate              OptionalString
	ContextWindowTokens       OptionalInt
	MaxOutputTokens           OptionalInt
	InputUSDPer1M             OptionalFloat
	OutputUSDPer1M            OptionalFloat
	CachedInputUSDPer1M       OptionalFloat
	CacheWriteUSDPer1M        OptionalFloat
	ImageInputUSDPer1M        OptionalFloat
	ImageOutputUSDPer1M       OptionalFloat
	AudioInputUSDPer1M        OptionalFloat
	AudioOutputUSDPer1M       OptionalFloat
	OutputUSDPerImage         OptionalFloat
	PricingNotes              OptionalString
	CapabilityNotes           OptionalString
	Notes                     OptionalString
}

type OptionalString struct {
	Set   bool
	Value string
}

type OptionalStringList struct {
	Set   bool
	Value []string
}

type OptionalInt struct {
	Set   bool
	Value *int
}

type OptionalFloat struct {
	Set   bool
	Value *float64
}

type CustomModelCreateInput struct {
	ProviderCode          string
	ActorSystemAccountID  string
	ActorRole             string
	TargetSystemAccountID string
	Fields                CustomModelMutation
}

type CustomModelUpdateInput struct {
	ProviderCode         string
	ID                   string
	ActorSystemAccountID string
	ActorRole            string
	Fields               CustomModelMutation
}

type CustomModelDeleteInput struct {
	ProviderCode         string
	ID                   string
	ActorSystemAccountID string
	ActorRole            string
}

type CustomModelDeleteResult struct {
	Deleted bool `json:"deleted"`
}

type ModelOption struct {
	ProviderCode              string   `json:"providerCode"`
	Model                     string   `json:"model"`
	SupportedAPIProtocols     []string `json:"supportedApiProtocols,omitempty"`
	SupportedServiceTiers     []string `json:"supportedServiceTiers,omitempty"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts,omitempty"`
	DefaultReasoningEffort    string   `json:"defaultReasoningEffort,omitempty"`
}

type DefaultHealthCheckModelResult struct {
	ProviderCode            string `json:"providerCode"`
	DefaultHealthCheckModel string `json:"defaultHealthCheckModel"`
}

type DefaultHealthCheckModelValidationError struct {
	Message string
}

func (e *DefaultHealthCheckModelValidationError) Error() string {
	return e.Message
}

func DefaultHealthCheckModelValidationMessage(err error) (string, bool) {
	var validationErr *DefaultHealthCheckModelValidationError
	if !errors.As(err, &validationErr) {
		return "", false
	}
	if strings.TrimSpace(validationErr.Message) == "" {
		return "默认检查模型参数无效", true
	}
	return validationErr.Message, true
}

type CustomModelValidationError struct {
	Message string
}

func (e *CustomModelValidationError) Error() string {
	return e.Message
}

type CustomModelForbiddenError struct {
	Message string
}

func (e *CustomModelForbiddenError) Error() string {
	return e.Message
}

type CustomModelBoundError struct {
	Message string
}

func (e *CustomModelBoundError) Error() string {
	return e.Message
}

func CustomModelValidationMessage(err error) (string, bool) {
	var validationErr *CustomModelValidationError
	if !errors.As(err, &validationErr) {
		return "", false
	}
	if strings.TrimSpace(validationErr.Message) == "" {
		return "自定义模型参数无效", true
	}
	return validationErr.Message, true
}

func CustomModelForbiddenMessage(err error) (string, bool) {
	var forbiddenErr *CustomModelForbiddenError
	if !errors.As(err, &forbiddenErr) {
		return "", false
	}
	if strings.TrimSpace(forbiddenErr.Message) == "" {
		return "无权操作该自定义模型", true
	}
	return forbiddenErr.Message, true
}

func CustomModelBoundMessage(err error) (string, bool) {
	var boundErr *CustomModelBoundError
	if !errors.As(err, &boundErr) {
		return "", false
	}
	return boundErr.Message, true
}

type ModelCatalogItem struct {
	ID                            string   `json:"id,omitempty"`
	ProviderCode                  string   `json:"providerCode"`
	Model                         string   `json:"model"`
	Scope                         string   `json:"scope"`
	Status                        string   `json:"status"`
	SystemAccountID               string   `json:"systemAccountId,omitempty"`
	PricingModel                  string   `json:"pricingModel,omitempty"`
	Mode                          string   `json:"mode,omitempty"`
	CatalogOrder                  *int     `json:"catalogOrder,omitempty"`
	ReleaseDate                   string   `json:"releaseDate,omitempty"`
	ShutdownDate                  string   `json:"shutdownDate,omitempty"`
	ContextWindowTokens           *int     `json:"contextWindowTokens,omitempty"`
	SupportedAPIProtocols         []string `json:"supportedApiProtocols"`
	SupportedServiceTiers         []string `json:"supportedServiceTiers"`
	SupportedReasoningEfforts     []string `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort        string   `json:"defaultReasoningEffort,omitempty"`
	CodexSupportedReasoningLevels []string `json:"codexSupportedReasoningLevels"`
	CodexDefaultReasoningLevel    string   `json:"codexDefaultReasoningLevel,omitempty"`
	CodexMultiAgentVersion        string   `json:"codexMultiAgentVersion,omitempty"`
	InputUSDPer1M                 *float64 `json:"inputUsdPer1M,omitempty"`
	OutputUSDPer1M                *float64 `json:"outputUsdPer1M,omitempty"`
	CachedInputUSDPer1M           *float64 `json:"cachedInputUsdPer1M,omitempty"`
	CacheWriteUSDPer1M            *float64 `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hUSDPer1M          *float64 `json:"cacheWrite1hUsdPer1M,omitempty"`
	ImageInputUSDPer1M            *float64 `json:"imageInputUsdPer1M,omitempty"`
	ImageOutputUSDPer1M           *float64 `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUSDPer1M            *float64 `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUSDPer1M           *float64 `json:"audioOutputUsdPer1M,omitempty"`
	OutputUSDPerImage             *float64 `json:"outputUsdPerImage,omitempty"`
	MaxInputTokens                *int     `json:"maxInputTokens,omitempty"`
	MaxOutputTokens               *int     `json:"maxOutputTokens,omitempty"`
	MaxTokens                     *int     `json:"maxTokens,omitempty"`
	SupportsPromptCaching         bool     `json:"supportsPromptCaching"`
	SupportsServiceTier           bool     `json:"supportsServiceTier"`
	CatalogVisible                bool     `json:"catalogVisible"`
	PricingNotes                  string   `json:"pricingNotes,omitempty"`
	CapabilityNotes               string   `json:"capabilityNotes,omitempty"`
	Notes                         string   `json:"notes,omitempty"`
	CreatedAt                     string   `json:"createdAt,omitempty"`
	UpdatedAt                     string   `json:"updatedAt,omitempty"`
	Source                        string   `json:"source"`
}

func NewService(store Store) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store})
}

func NewServiceWithOptions(opts ServiceOptions) *Service {
	newID := opts.NewID
	if newID == nil {
		newID = func(prefix string) string {
			return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	return &Service{store: opts.Store, invalidator: opts.Invalidator, newID: newID}
}

func (s *Service) ModelOptions(ctx context.Context, input ModelOptionListInput) ([]ModelOption, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management provider model store is required")
	}
	providerCodes, err := s.optionProviderCodes(ctx, input.Protocol)
	if err != nil {
		return nil, err
	}
	builtInCodes, customCodes := optionSourceProviderCodes(providerCodes)
	rows, err := s.store.ListManagementProviderModelCatalog(ctx, port.ManagementProviderModelCatalogListInput{
		BuiltInProviderCodes: builtInCodes,
		CustomProviderCodes:  customCodes,
		SystemAccountID:      strings.TrimSpace(input.SystemAccountID),
		IncludeInactive:      false,
	})
	if err != nil {
		return nil, err
	}
	items := sortCatalogItems(rows)
	return dedupeModelOptions(items), nil
}

func (s *Service) Models(ctx context.Context, input ModelListInput) ([]ModelCatalogItem, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management provider model store is required")
	}
	providerCode := strings.TrimSpace(input.ProviderCode)
	provider, found, err := s.store.FindManagementProviderModelProvider(ctx, providerCode)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrProviderNotFound
	}
	builtInCodes, customCodes, err := s.sourceProviderCodes(ctx, provider.Code)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListManagementProviderModelCatalog(ctx, port.ManagementProviderModelCatalogListInput{
		BuiltInProviderCodes: builtInCodes,
		CustomProviderCodes:  customCodes,
		SystemAccountID:      strings.TrimSpace(input.SystemAccountID),
		IncludeInactive:      input.IncludeInactive,
	})
	if err != nil {
		return nil, err
	}
	mergeKey := mergeKeyModel
	if provider.Code == hybridProviderCode {
		mergeKey = mergeKeyProviderModel
	}
	items := sortCatalogItems(mergeCatalogItems(rows, mergeKey))
	if !input.IncludeUnpriced {
		items = filterPricedCatalogItems(items)
	}
	output := make([]ModelCatalogItem, 0, len(items))
	for _, item := range items {
		output = append(output, catalogItemFromPort(item))
	}
	return output, nil
}

func (s *Service) SetDefaultHealthCheckModel(ctx context.Context, input DefaultHealthCheckModelInput) (DefaultHealthCheckModelResult, error) {
	if s.store == nil {
		return DefaultHealthCheckModelResult{}, fmt.Errorf("management provider model store is required")
	}
	providerCode := strings.TrimSpace(input.ProviderCode)
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	model := strings.TrimSpace(input.Model)
	if actorSystemAccountID == "" {
		return DefaultHealthCheckModelResult{}, &DefaultHealthCheckModelValidationError{Message: "请选择要设置默认检查模型的系统账户"}
	}
	if model == "" {
		return DefaultHealthCheckModelResult{}, &DefaultHealthCheckModelValidationError{Message: "默认检查模型参数无效"}
	}
	systemScope := isAdminRole(input.ActorRole)
	catalogSystemAccountID := actorSystemAccountID
	if systemScope {
		catalogSystemAccountID = ""
	}
	models, err := s.Models(ctx, ModelListInput{
		ProviderCode:    providerCode,
		SystemAccountID: catalogSystemAccountID,
		IncludeInactive: true,
		IncludeUnpriced: true,
	})
	if err != nil {
		return DefaultHealthCheckModelResult{}, err
	}
	selected := findDefaultHealthCheckModelCandidate(models, model)
	if selected == nil {
		return DefaultHealthCheckModelResult{}, &DefaultHealthCheckModelValidationError{Message: "模型不在当前用户可见目录中：" + model}
	}
	if systemScope && selected.Scope == "personal" {
		return DefaultHealthCheckModelResult{}, &DefaultHealthCheckModelValidationError{Message: "系统默认检查模型不能选择个人模型"}
	}
	if !isActiveCatalogItem(*selected) {
		return DefaultHealthCheckModelResult{}, &DefaultHealthCheckModelValidationError{Message: "只能把启用模型设置为默认检查模型"}
	}
	if !isCatalogItemUsableForAccountTest(*selected) {
		return DefaultHealthCheckModelResult{}, &DefaultHealthCheckModelValidationError{Message: "默认检查模型只能选择文本生成模型"}
	}
	var saved port.ManagementProviderDefaultHealthCheckModelPreference
	if systemScope {
		saved, err = s.store.SetManagementProviderSystemDefaultHealthCheckModel(ctx, port.ManagementProviderSystemDefaultHealthCheckModelInput{
			ProviderCode: providerCode,
			Model:        selected.Model,
		})
	} else {
		saved, err = s.store.SetManagementProviderDefaultHealthCheckModel(ctx, port.ManagementProviderDefaultHealthCheckModelInput{
			ProviderCode:    providerCode,
			SystemAccountID: actorSystemAccountID,
			Model:           selected.Model,
		})
	}
	if err != nil {
		return DefaultHealthCheckModelResult{}, err
	}
	return DefaultHealthCheckModelResult{
		ProviderCode:            saved.ProviderCode,
		DefaultHealthCheckModel: saved.Model,
	}, nil
}

func (s *Service) CreateCustomModel(ctx context.Context, input CustomModelCreateInput) (ModelCatalogItem, error) {
	if s.store == nil {
		return ModelCatalogItem{}, fmt.Errorf("management provider model store is required")
	}
	providerCode := strings.TrimSpace(input.ProviderCode)
	provider, found, err := s.store.FindManagementProviderModelProvider(ctx, providerCode)
	if err != nil {
		return ModelCatalogItem{}, err
	}
	if !found {
		return ModelCatalogItem{}, ErrProviderNotFound
	}
	if input.Fields.Invalid {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	actorSystemAccountID := strings.TrimSpace(input.ActorSystemAccountID)
	scope, err := createCustomModelScope(input.Fields.Scope)
	if err != nil {
		return ModelCatalogItem{}, err
	}
	if scope == "global" && !isAdminRole(input.ActorRole) {
		return ModelCatalogItem{}, &CustomModelForbiddenError{Message: "只有管理员可以创建全局模型"}
	}
	ownerSystemAccountID := ""
	if scope == "personal" {
		if isAdminRole(input.ActorRole) {
			ownerSystemAccountID = strings.TrimSpace(input.TargetSystemAccountID)
		} else {
			ownerSystemAccountID = actorSystemAccountID
		}
	}
	saveInput, err := customModelSaveInputFromCreate(provider.Code, scope, ownerSystemAccountID, actorSystemAccountID, input.Fields)
	if err != nil {
		return ModelCatalogItem{}, err
	}
	if scope == "personal" && ownerSystemAccountID == "" {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "请选择模型归属的系统账户"}
	}
	if err := s.validateCustomModelPricing(ctx, saveInput, ownerSystemAccountID); err != nil {
		return ModelCatalogItem{}, err
	}
	existing, found, err := s.store.FindManagementCustomProviderModelByScope(ctx, port.ManagementCustomProviderModelScopeInput{
		ProviderCode:    saveInput.ProviderCode,
		Scope:           saveInput.Scope,
		SystemAccountID: saveInput.SystemAccountID,
		Model:           saveInput.Model,
	})
	if err != nil {
		return ModelCatalogItem{}, err
	}
	if found {
		saveInput.ID = existing.ID
	} else if strings.TrimSpace(saveInput.ID) == "" {
		saveInput.ID = s.newID("custom_model")
	}
	saved, err := s.store.SaveManagementCustomProviderModel(ctx, saveInput)
	if err != nil {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "自定义模型保存失败"}
	}
	s.invalidateCustomProviderModel(ctx, CustomProviderModelSavedReason)
	return catalogItemFromPort(saved), nil
}

func (s *Service) UpdateCustomModel(ctx context.Context, input CustomModelUpdateInput) (ModelCatalogItem, error) {
	if s.store == nil {
		return ModelCatalogItem{}, fmt.Errorf("management provider model store is required")
	}
	existing, found, err := s.store.FindManagementCustomProviderModel(ctx, strings.TrimSpace(input.ID))
	if err != nil {
		return ModelCatalogItem{}, err
	}
	if !found || strings.TrimSpace(existing.ProviderCode) != strings.TrimSpace(input.ProviderCode) {
		return ModelCatalogItem{}, ErrCustomProviderModelNotFound
	}
	if !canMutateCustomProviderModel(existing.Scope, existing.SystemAccountID, input.ActorSystemAccountID, input.ActorRole) {
		return ModelCatalogItem{}, &CustomModelForbiddenError{Message: "无权修改该自定义模型"}
	}
	if input.Fields.Invalid || !customModelMutationHasAnyField(input.Fields) {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	saveInput := customModelSaveInputFromExisting(existing, strings.TrimSpace(input.ActorSystemAccountID))
	if err := applyCustomModelPatch(&saveInput, input.Fields); err != nil {
		return ModelCatalogItem{}, err
	}
	if err := s.validateCustomModelPricing(ctx, saveInput, saveInput.SystemAccountID); err != nil {
		return ModelCatalogItem{}, err
	}
	saved, err := s.store.SaveManagementCustomProviderModel(ctx, saveInput)
	if err != nil {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "自定义模型保存失败"}
	}
	s.invalidateCustomProviderModel(ctx, CustomProviderModelSavedReason)
	if saved.Status != "active" {
		if err := s.clearDefaultHealthCheckModelReferences(ctx, saved); err != nil {
			return ModelCatalogItem{}, err
		}
	}
	return catalogItemFromPort(saved), nil
}

func (s *Service) clearDefaultHealthCheckModelReferences(ctx context.Context, model port.ManagementProviderModelCatalogItem) error {
	if _, err := s.store.ClearManagementProviderDefaultHealthCheckModelIfModel(ctx, port.ManagementProviderDefaultHealthCheckModelClearInput{
		ProviderCode:    model.ProviderCode,
		SystemAccountID: model.SystemAccountID,
		Model:           model.Model,
	}); err != nil {
		return err
	}
	if model.Scope == "global" {
		if _, err := s.store.ClearManagementProviderSystemDefaultHealthCheckModelIfModel(ctx, port.ManagementProviderSystemDefaultHealthCheckModelClearInput{
			ProviderCode: model.ProviderCode,
			Model:        model.Model,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) DeleteCustomModel(ctx context.Context, input CustomModelDeleteInput) (CustomModelDeleteResult, error) {
	if s.store == nil {
		return CustomModelDeleteResult{}, fmt.Errorf("management provider model store is required")
	}
	existing, found, err := s.store.FindManagementCustomProviderModel(ctx, strings.TrimSpace(input.ID))
	if err != nil {
		return CustomModelDeleteResult{}, err
	}
	if !found || strings.TrimSpace(existing.ProviderCode) != strings.TrimSpace(input.ProviderCode) {
		return CustomModelDeleteResult{}, ErrCustomProviderModelNotFound
	}
	if !canMutateCustomProviderModel(existing.Scope, existing.SystemAccountID, input.ActorSystemAccountID, input.ActorRole) {
		return CustomModelDeleteResult{}, &CustomModelForbiddenError{Message: "无权删除该自定义模型"}
	}
	bindings, err := s.store.GetManagementCustomProviderModelBindingSummary(ctx, port.ManagementCustomProviderModelBindingInput{
		ProviderCode:    existing.ProviderCode,
		Model:           existing.Model,
		Scope:           existing.Scope,
		SystemAccountID: existing.SystemAccountID,
	})
	if err != nil {
		return CustomModelDeleteResult{}, err
	}
	if bindings.TotalAccountCount > 0 {
		return CustomModelDeleteResult{}, &CustomModelBoundError{Message: customProviderModelBoundMessage(bindings)}
	}
	deleted, err := s.store.DeleteManagementCustomProviderModel(ctx, existing.ID)
	if err != nil {
		return CustomModelDeleteResult{}, err
	}
	if deleted {
		s.invalidateCustomProviderModel(ctx, CustomProviderModelDeletedReason)
		if err := s.clearDefaultHealthCheckModelReferences(ctx, existing); err != nil {
			return CustomModelDeleteResult{}, err
		}
	}
	return CustomModelDeleteResult{Deleted: deleted}, nil
}

func (s *Service) optionProviderCodes(ctx context.Context, protocol string) ([]string, error) {
	protocolCode, protocolVersion, ok := protocolFilter(strings.TrimSpace(protocol))
	if ok {
		codes, err := s.store.ListManagementProviderCodesByProtocol(ctx, protocolCode, protocolVersion)
		if err != nil {
			return nil, err
		}
		return dedupeStrings(codes), nil
	}
	codes, err := s.store.ListManagementEnabledModelProviderCodes(ctx)
	if err != nil {
		return nil, err
	}
	return dedupeStrings(codes), nil
}

func optionSourceProviderCodes(providerCodes []string) ([]string, []string) {
	builtInCodes := make([]string, 0, len(providerCodes))
	customCodes := make([]string, 0, len(providerCodes))
	for _, providerCode := range providerCodes {
		code := strings.TrimSpace(providerCode)
		if code == "" {
			continue
		}
		if code == hybridProviderCode {
			continue
		}
		if code != openAIProviderCode {
			builtInCodes = append(builtInCodes, code)
		}
		customCodes = append(customCodes, code)
	}
	return dedupeStrings(builtInCodes), dedupeStrings(customCodes)
}

func (s *Service) sourceProviderCodes(ctx context.Context, providerCode string) ([]string, []string, error) {
	code := strings.TrimSpace(providerCode)
	switch code {
	case hybridProviderCode:
		codes, err := s.store.ListManagementEnabledModelProviderCodes(ctx)
		if err != nil {
			return nil, nil, err
		}
		builtInCodes, customCodes := optionSourceProviderCodes(codes)
		return builtInCodes, customCodes, nil
	case openAIProviderCode:
		codes, err := s.store.ListManagementProviderCodesByProtocol(ctx, protocolOpenAI, "v1")
		if err != nil {
			return nil, nil, err
		}
		builtInCodes, customCodes := optionSourceProviderCodes(codes)
		return builtInCodes, customCodes, nil
	default:
		return []string{code}, []string{code}, nil
	}
}

func protocolFilter(protocol string) (string, string, bool) {
	switch protocol {
	case protocolOpenAI:
		return protocolOpenAI, "v1", true
	case protocolAnthropic:
		return protocolAnthropic, "v1", true
	case protocolGemini:
		return protocolGemini, "v1beta", true
	default:
		return "", "", false
	}
}

var customProviderModelAPIProtocols = map[string]struct{}{
	"chat_completions":        {},
	"responses":               {},
	"messages":                {},
	"message_token_counting":  {},
	"generate_content":        {},
	"stream_generate_content": {},
	"count_tokens":            {},
	"embed_content":           {},
	"completions":             {},
	"images":                  {},
	"audio":                   {},
	"realtime":                {},
}

var customProviderModelServiceTiers = map[string]struct{}{
	"priority": {},
	"flex":     {},
}

var customProviderModelReasoningEfforts = map[string]struct{}{
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
	"max":     {},
}

func createCustomModelScope(scope OptionalString) (string, error) {
	if !scope.Set {
		return "personal", nil
	}
	value := strings.TrimSpace(scope.Value)
	if value != "personal" && value != "global" {
		return "", &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	return value, nil
}

func customModelSaveInputFromCreate(
	providerCode string,
	scope string,
	systemAccountID string,
	actorSystemAccountID string,
	fields CustomModelMutation,
) (port.ManagementCustomProviderModelSaveInput, error) {
	model := strings.TrimSpace(fields.Model.Value)
	if !fields.Model.Set || model == "" {
		return port.ManagementCustomProviderModelSaveInput{}, &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	status := "active"
	if fields.Status.Set {
		status = strings.TrimSpace(fields.Status.Value)
	}
	if !validCustomModelStatus(status) {
		return port.ManagementCustomProviderModelSaveInput{}, &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	saveInput := port.ManagementCustomProviderModelSaveInput{
		ProviderCode:         strings.TrimSpace(providerCode),
		Model:                model,
		Scope:                scope,
		SystemAccountID:      strings.TrimSpace(systemAccountID),
		Status:               status,
		ActorSystemAccountID: strings.TrimSpace(actorSystemAccountID),
	}
	if err := applyCustomModelMutableFields(&saveInput, fields, true); err != nil {
		return port.ManagementCustomProviderModelSaveInput{}, err
	}
	return saveInput, nil
}

func customModelSaveInputFromExisting(item port.ManagementProviderModelCatalogItem, actorSystemAccountID string) port.ManagementCustomProviderModelSaveInput {
	return port.ManagementCustomProviderModelSaveInput{
		ID:                        item.ID,
		ProviderCode:              item.ProviderCode,
		Model:                     item.Model,
		Scope:                     item.Scope,
		SystemAccountID:           item.SystemAccountID,
		Status:                    item.Status,
		Mode:                      item.Mode,
		SupportedAPIProtocols:     append([]string(nil), item.SupportedAPIProtocols...),
		SupportedServiceTiers:     append([]string(nil), item.SupportedServiceTiers...),
		SupportedReasoningEfforts: append([]string(nil), item.SupportedReasoningEfforts...),
		DefaultReasoningEffort:    item.DefaultReasoningEffort,
		PricingModel:              item.PricingModel,
		ReleaseDate:               item.ReleaseDate,
		ShutdownDate:              item.ShutdownDate,
		ContextWindowTokens:       cloneIntPtr(item.ContextWindowTokens),
		MaxOutputTokens:           cloneIntPtr(item.MaxOutputTokens),
		InputUSDPer1M:             cloneFloatPtr(item.InputUSDPer1M),
		OutputUSDPer1M:            cloneFloatPtr(item.OutputUSDPer1M),
		CachedInputUSDPer1M:       cloneFloatPtr(item.CachedInputUSDPer1M),
		CacheWriteUSDPer1M:        cloneFloatPtr(item.CacheWriteUSDPer1M),
		ImageInputUSDPer1M:        cloneFloatPtr(item.ImageInputUSDPer1M),
		ImageOutputUSDPer1M:       cloneFloatPtr(item.ImageOutputUSDPer1M),
		AudioInputUSDPer1M:        cloneFloatPtr(item.AudioInputUSDPer1M),
		AudioOutputUSDPer1M:       cloneFloatPtr(item.AudioOutputUSDPer1M),
		OutputUSDPerImage:         cloneFloatPtr(item.OutputUSDPerImage),
		PricingNotes:              item.PricingNotes,
		CapabilityNotes:           item.CapabilityNotes,
		Notes:                     item.Notes,
		ActorSystemAccountID:      strings.TrimSpace(actorSystemAccountID),
	}
}

func applyCustomModelPatch(input *port.ManagementCustomProviderModelSaveInput, fields CustomModelMutation) error {
	if fields.Scope.Set {
		if _, err := createCustomModelScope(fields.Scope); err != nil {
			return err
		}
	}
	if fields.Model.Set {
		model := strings.TrimSpace(fields.Model.Value)
		if model == "" {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		if model != strings.TrimSpace(input.Model) {
			return &CustomModelValidationError{Message: "模型 ID 创建后不能修改"}
		}
	}
	return applyCustomModelMutableFields(input, fields, false)
}

func applyCustomModelMutableFields(input *port.ManagementCustomProviderModelSaveInput, fields CustomModelMutation, create bool) error {
	if fields.Status.Set {
		status := strings.TrimSpace(fields.Status.Value)
		if !validCustomModelStatus(status) {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		input.Status = status
	}
	if input.Status == "" {
		input.Status = "active"
	}
	if fields.Mode.Set {
		mode := strings.TrimSpace(fields.Mode.Value)
		if mode != "" && mode != "text" && mode != "image" && mode != "audio" {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		input.Mode = mode
	}
	if fields.SupportedAPIProtocols.Set {
		protocols, err := normalizeCustomModelProtocols(fields.SupportedAPIProtocols.Value)
		if err != nil {
			return err
		}
		input.SupportedAPIProtocols = protocols
	} else if create {
		input.SupportedAPIProtocols = []string{}
	}
	if fields.SupportedServiceTiers.Set {
		if len(fields.SupportedServiceTiers.Value) > 2 {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		serviceTiers, err := normalizeCustomModelCapabilityList(fields.SupportedServiceTiers.Value, customProviderModelServiceTiers)
		if err != nil {
			return err
		}
		input.SupportedServiceTiers = serviceTiers
	} else if create {
		input.SupportedServiceTiers = []string{}
	}
	if fields.SupportedReasoningEfforts.Set {
		if len(fields.SupportedReasoningEfforts.Value) > 7 {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		reasoningEfforts, err := normalizeCustomModelCapabilityList(fields.SupportedReasoningEfforts.Value, customProviderModelReasoningEfforts)
		if err != nil {
			return err
		}
		input.SupportedReasoningEfforts = reasoningEfforts
	} else if create {
		input.SupportedReasoningEfforts = []string{}
	}
	if fields.DefaultReasoningEffort.Set {
		defaultReasoningEffort := fields.DefaultReasoningEffort.Value
		if defaultReasoningEffort != "" {
			if _, ok := customProviderModelReasoningEfforts[defaultReasoningEffort]; !ok {
				return &CustomModelValidationError{Message: "自定义模型参数无效"}
			}
		}
		input.DefaultReasoningEffort = defaultReasoningEffort
	}
	if fields.PricingModel.Set {
		input.PricingModel = strings.TrimSpace(fields.PricingModel.Value)
	}
	if fields.ReleaseDate.Set {
		input.ReleaseDate = strings.TrimSpace(fields.ReleaseDate.Value)
	}
	if fields.ShutdownDate.Set {
		input.ShutdownDate = strings.TrimSpace(fields.ShutdownDate.Value)
	}
	if err := validateOptionalCustomModelDate(input.ReleaseDate); err != nil {
		return err
	}
	if err := validateOptionalCustomModelDate(input.ShutdownDate); err != nil {
		return err
	}
	if fields.ContextWindowTokens.Set {
		if err := validateOptionalNonnegativeInt(fields.ContextWindowTokens.Value); err != nil {
			return err
		}
		input.ContextWindowTokens = cloneIntPtr(fields.ContextWindowTokens.Value)
	}
	if fields.MaxOutputTokens.Set {
		if err := validateOptionalNonnegativeInt(fields.MaxOutputTokens.Value); err != nil {
			return err
		}
		input.MaxOutputTokens = cloneIntPtr(fields.MaxOutputTokens.Value)
	}
	if err := applyOptionalCustomModelFloat(&input.InputUSDPer1M, fields.InputUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.OutputUSDPer1M, fields.OutputUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.CachedInputUSDPer1M, fields.CachedInputUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.CacheWriteUSDPer1M, fields.CacheWriteUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.ImageInputUSDPer1M, fields.ImageInputUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.ImageOutputUSDPer1M, fields.ImageOutputUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.AudioInputUSDPer1M, fields.AudioInputUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.AudioOutputUSDPer1M, fields.AudioOutputUSDPer1M); err != nil {
		return err
	}
	if err := applyOptionalCustomModelFloat(&input.OutputUSDPerImage, fields.OutputUSDPerImage); err != nil {
		return err
	}
	if fields.PricingNotes.Set {
		input.PricingNotes = strings.TrimSpace(fields.PricingNotes.Value)
	}
	if fields.CapabilityNotes.Set {
		input.CapabilityNotes = strings.TrimSpace(fields.CapabilityNotes.Value)
	}
	if fields.Notes.Set {
		input.Notes = strings.TrimSpace(fields.Notes.Value)
	}
	return validateCustomModelRequestCapabilities(*input)
}

func customModelMutationHasAnyField(fields CustomModelMutation) bool {
	return fields.Scope.Set ||
		fields.Model.Set ||
		fields.Status.Set ||
		fields.Mode.Set ||
		fields.SupportedAPIProtocols.Set ||
		fields.SupportedServiceTiers.Set ||
		fields.SupportedReasoningEfforts.Set ||
		fields.DefaultReasoningEffort.Set ||
		fields.PricingModel.Set ||
		fields.ReleaseDate.Set ||
		fields.ShutdownDate.Set ||
		fields.ContextWindowTokens.Set ||
		fields.MaxOutputTokens.Set ||
		fields.InputUSDPer1M.Set ||
		fields.OutputUSDPer1M.Set ||
		fields.CachedInputUSDPer1M.Set ||
		fields.CacheWriteUSDPer1M.Set ||
		fields.ImageInputUSDPer1M.Set ||
		fields.ImageOutputUSDPer1M.Set ||
		fields.AudioInputUSDPer1M.Set ||
		fields.AudioOutputUSDPer1M.Set ||
		fields.OutputUSDPerImage.Set ||
		fields.PricingNotes.Set ||
		fields.CapabilityNotes.Set ||
		fields.Notes.Set
}

func validCustomModelStatus(status string) bool {
	return status == "draft" || status == "active" || status == "disabled"
}

func normalizeCustomModelProtocols(values []string) ([]string, error) {
	output := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		protocol := strings.TrimSpace(value)
		if _, ok := customProviderModelAPIProtocols[protocol]; !ok {
			return nil, &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		if _, exists := seen[protocol]; exists {
			continue
		}
		seen[protocol] = struct{}{}
		output = append(output, protocol)
	}
	return output, nil
}

func normalizeCustomModelCapabilityList(values []string, allowed map[string]struct{}) ([]string, error) {
	output := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := allowed[value]; !ok {
			return nil, &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		output = append(output, value)
	}
	return output, nil
}

func validateCustomModelRequestCapabilities(input port.ManagementCustomProviderModelSaveInput) error {
	hasCapabilities := len(input.SupportedServiceTiers) > 0 ||
		len(input.SupportedReasoningEfforts) > 0 ||
		input.DefaultReasoningEffort != ""
	mode := strings.TrimSpace(input.Mode)
	isGPTTextModel := strings.TrimSpace(input.ProviderCode) == "gpt" && (mode == "" || mode == "text")
	if !isGPTTextModel {
		if hasCapabilities {
			return &CustomModelValidationError{Message: "只有 GPT 文本自定义模型支持服务等级和思考能力配置"}
		}
		return nil
	}
	defaultReasoningEffort := input.DefaultReasoningEffort
	if defaultReasoningEffort == "" {
		return nil
	}
	for _, effort := range input.SupportedReasoningEfforts {
		if effort == defaultReasoningEffort {
			return nil
		}
	}
	return &CustomModelValidationError{Message: "默认思考级别必须属于支持的思考级别"}
}

func validateOptionalCustomModelDate(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	for index, char := range value {
		if index == 4 || index == 7 {
			continue
		}
		if char < '0' || char > '9' {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
	}
	return nil
}

func validateOptionalNonnegativeInt(value *int) error {
	if value == nil {
		return nil
	}
	if *value < 0 {
		return &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	if *value > math.MaxInt32 {
		return &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	return nil
}

func applyOptionalCustomModelFloat(target **float64, value OptionalFloat) error {
	if !value.Set {
		return nil
	}
	if value.Value != nil && *value.Value < 0 {
		return &CustomModelValidationError{Message: "自定义模型参数无效"}
	}
	*target = cloneFloatPtr(value.Value)
	return nil
}

func (s *Service) validateCustomModelPricing(ctx context.Context, input port.ManagementCustomProviderModelSaveInput, ownerSystemAccountID string) error {
	hasDirectPriceConfigured := customModelSaveInputHasDirectPrice(input)
	pricingModel := strings.TrimSpace(input.PricingModel)
	if hasDirectPriceConfigured && pricingModel != "" {
		return &CustomModelValidationError{Message: "自定义模型不能同时配置直接价格和 pricingModel"}
	}
	if input.Status == "active" && !hasDirectPriceConfigured && pricingModel == "" {
		return &CustomModelValidationError{Message: "启用的自定义模型必须配置价格或 pricingModel"}
	}
	if pricingModel == "" {
		return nil
	}
	if strings.TrimSpace(input.Model) == pricingModel {
		return &CustomModelValidationError{Message: "pricingModel 不能指向当前模型自身"}
	}
	builtInCodes, customCodes, err := s.sourceProviderCodes(ctx, input.ProviderCode)
	if err != nil {
		return err
	}
	catalog, err := s.store.ListManagementProviderModelCatalog(ctx, port.ManagementProviderModelCatalogListInput{
		BuiltInProviderCodes: builtInCodes,
		CustomProviderCodes:  customCodes,
		SystemAccountID:      strings.TrimSpace(ownerSystemAccountID),
		IncludeInactive:      true,
	})
	if err != nil {
		return err
	}
	var target *port.ManagementProviderModelCatalogItem
	for index := range catalog {
		if strings.TrimSpace(catalog[index].Model) == pricingModel {
			target = &catalog[index]
			break
		}
	}
	if target == nil {
		return &CustomModelValidationError{Message: "pricingModel 不存在：" + pricingModel}
	}
	if strings.TrimSpace(target.PricingModel) != "" {
		return &CustomModelValidationError{Message: "pricingModel 只能指向有直接价格的模型，不能递归指向另一个 pricingModel"}
	}
	if !hasDirectPrice(*target) {
		return &CustomModelValidationError{Message: "pricingModel 缺少直接价格：" + pricingModel}
	}
	return nil
}

func customModelSaveInputHasDirectPrice(input port.ManagementCustomProviderModelSaveInput) bool {
	return input.InputUSDPer1M != nil ||
		input.OutputUSDPer1M != nil ||
		input.CachedInputUSDPer1M != nil ||
		input.CacheWriteUSDPer1M != nil ||
		input.ImageInputUSDPer1M != nil ||
		input.ImageOutputUSDPer1M != nil ||
		input.AudioInputUSDPer1M != nil ||
		input.AudioOutputUSDPer1M != nil ||
		input.OutputUSDPerImage != nil
}

func canMutateCustomProviderModel(scope string, ownerSystemAccountID string, actorSystemAccountID string, actorRole string) bool {
	if scope == "global" {
		return isAdminRole(actorRole)
	}
	actorID := strings.TrimSpace(actorSystemAccountID)
	return actorID != "" && (isAdminRole(actorRole) || actorID == strings.TrimSpace(ownerSystemAccountID))
}

func isAdminRole(role string) bool {
	return role == "admin" || role == "super_admin"
}

func customProviderModelBoundMessage(bindings port.ManagementCustomProviderModelBindingSummary) string {
	parts := []string{}
	if bindings.SupportedModelAccountCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 个账户支持模型", bindings.SupportedModelAccountCount))
	}
	if bindings.MappingSourceAccountCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 个账户映射下游模型", bindings.MappingSourceAccountCount))
	}
	if bindings.MappingUpstreamAccountCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 个账户映射上游模型", bindings.MappingUpstreamAccountCount))
	}
	if len(parts) == 0 {
		return "模型已绑定 AI 账户，不能删除"
	}
	return "模型已绑定 AI 账户，不能删除；请先从" + strings.Join(parts, "、") + "中移除后再删除"
}

func (s *Service) invalidateCustomProviderModel(ctx context.Context, reason string) {
	if s.invalidator == nil {
		return
	}
	_ = s.invalidator.InvalidateCustomProviderModelChanged(ctx, reason)
}

type mergeKeyFunc func(port.ManagementProviderModelCatalogItem) string

func mergeKeyModel(item port.ManagementProviderModelCatalogItem) string {
	return strings.TrimSpace(item.Model)
}

func mergeKeyProviderModel(item port.ManagementProviderModelCatalogItem) string {
	return strings.ToLower(strings.TrimSpace(item.ProviderCode)) + "\n" + strings.TrimSpace(item.Model)
}

func mergeCatalogItems(items []port.ManagementProviderModelCatalogItem, keyFunc mergeKeyFunc) []port.ManagementProviderModelCatalogItem {
	merged := map[string]port.ManagementProviderModelCatalogItem{}
	order := []string{}
	for _, item := range items {
		key := keyFunc(item)
		if key == "" {
			continue
		}
		previous, exists := merged[key]
		if !exists {
			order = append(order, key)
			merged[key] = item
			continue
		}
		if catalogPriority(item) >= catalogPriority(previous) {
			merged[key] = item
		}
	}
	output := make([]port.ManagementProviderModelCatalogItem, 0, len(order))
	for _, key := range order {
		output = append(output, merged[key])
	}
	return output
}

func findDefaultHealthCheckModelCandidate(items []ModelCatalogItem, model string) *ModelCatalogItem {
	normalized := strings.TrimSpace(model)
	for index := range items {
		if strings.TrimSpace(items[index].Model) == normalized {
			return &items[index]
		}
	}
	return nil
}

func isActiveCatalogItem(item ModelCatalogItem) bool {
	status := strings.TrimSpace(item.Status)
	return status == "" || status == "active"
}

func isCatalogItemUsableForAccountTest(item ModelCatalogItem) bool {
	switch strings.TrimSpace(item.Mode) {
	case "image", "audio":
		return false
	}
	if len(item.SupportedAPIProtocols) == 0 {
		return true
	}
	for _, protocol := range item.SupportedAPIProtocols {
		switch strings.TrimSpace(protocol) {
		case "chat_completions", "responses", "messages", "generate_content", "stream_generate_content":
			return true
		}
	}
	return false
}

func catalogPriority(item port.ManagementProviderModelCatalogItem) int {
	switch item.Scope {
	case "personal":
		return 3
	case "global":
		return 2
	default:
		return 1
	}
}

func sortCatalogItems(items []port.ManagementProviderModelCatalogItem) []port.ManagementProviderModelCatalogItem {
	output := append([]port.ManagementProviderModelCatalogItem(nil), items...)
	sort.SliceStable(output, func(i, j int) bool {
		return compareCatalogItems(output[i], output[j]) < 0
	})
	return output
}

func compareCatalogItems(left, right port.ManagementProviderModelCatalogItem) int {
	sameProvider := strings.EqualFold(left.ProviderCode, right.ProviderCode)
	if sameProvider {
		if result := compareOptionalInt(left.CatalogOrder, right.CatalogOrder); result != 0 {
			return result
		}
	}
	leftReleaseDate := strings.TrimSpace(left.ReleaseDate)
	rightReleaseDate := strings.TrimSpace(right.ReleaseDate)
	if leftReleaseDate != "" && rightReleaseDate != "" && leftReleaseDate != rightReleaseDate {
		if leftReleaseDate > rightReleaseDate {
			return -1
		}
		return 1
	}
	if leftReleaseDate != "" && rightReleaseDate == "" {
		return -1
	}
	if leftReleaseDate == "" && rightReleaseDate != "" {
		return 1
	}
	if !sameProvider {
		if result := compareOptionalInt(left.CatalogOrder, right.CatalogOrder); result != 0 {
			return result
		}
	}
	if result := strings.Compare(left.Model, right.Model); result != 0 {
		return result
	}
	return strings.Compare(left.ID, right.ID)
}

func compareOptionalInt(left, right *int) int {
	if left != nil && right != nil && *left != *right {
		if *left < *right {
			return -1
		}
		return 1
	}
	return 0
}

func filterPricedCatalogItems(items []port.ManagementProviderModelCatalogItem) []port.ManagementProviderModelCatalogItem {
	output := make([]port.ManagementProviderModelCatalogItem, 0, len(items))
	for _, item := range items {
		if hasResolvablePrice(item, items) {
			output = append(output, item)
		}
	}
	return output
}

func hasResolvablePrice(item port.ManagementProviderModelCatalogItem, all []port.ManagementProviderModelCatalogItem) bool {
	if hasDirectPrice(item) {
		return true
	}
	pricingModel := strings.TrimSpace(item.PricingModel)
	if pricingModel == "" {
		return false
	}
	for _, target := range all {
		if strings.TrimSpace(target.Model) == pricingModel && target.PricingModel == "" && hasDirectPrice(target) {
			return true
		}
	}
	return false
}

func hasDirectPrice(item port.ManagementProviderModelCatalogItem) bool {
	return item.InputUSDPer1M != nil ||
		item.OutputUSDPer1M != nil ||
		item.CachedInputUSDPer1M != nil ||
		item.CacheWriteUSDPer1M != nil ||
		item.CacheWrite1hUSDPer1M != nil ||
		item.ImageInputUSDPer1M != nil ||
		item.ImageOutputUSDPer1M != nil ||
		item.AudioInputUSDPer1M != nil ||
		item.AudioOutputUSDPer1M != nil ||
		item.OutputUSDPerImage != nil
}

func dedupeModelOptions(items []port.ManagementProviderModelCatalogItem) []ModelOption {
	result := []ModelOption{}
	seen := map[string]int{}
	defaultCandidates := [][]string{}
	for _, item := range items {
		providerCode := strings.TrimSpace(item.ProviderCode)
		model := strings.TrimSpace(item.Model)
		if providerCode == "" || model == "" {
			continue
		}
		key := strings.ToLower(providerCode) + "\n" + model
		protocols := dedupeStrings(item.SupportedAPIProtocols)
		serviceTiers := normalizeCatalogCapabilityList(item.SupportedServiceTiers, customProviderModelServiceTiers)
		reasoningEfforts := normalizeCatalogCapabilityList(item.SupportedReasoningEfforts, customProviderModelReasoningEfforts)
		defaultReasoningEffort := strings.TrimSpace(item.DefaultReasoningEffort)
		if _, ok := customProviderModelReasoningEfforts[defaultReasoningEffort]; !ok {
			defaultReasoningEffort = ""
		}
		if index, exists := seen[key]; exists {
			result[index].SupportedAPIProtocols = dedupeStrings(append(result[index].SupportedAPIProtocols, protocols...))
			result[index].SupportedServiceTiers = dedupeStrings(append(result[index].SupportedServiceTiers, serviceTiers...))
			result[index].SupportedReasoningEfforts = dedupeStrings(append(result[index].SupportedReasoningEfforts, reasoningEfforts...))
			if defaultReasoningEffort != "" {
				defaultCandidates[index] = append(defaultCandidates[index], defaultReasoningEffort)
			}
			continue
		}
		seen[key] = len(result)
		result = append(result, ModelOption{
			ProviderCode:              providerCode,
			Model:                     model,
			SupportedAPIProtocols:     protocols,
			SupportedServiceTiers:     serviceTiers,
			SupportedReasoningEfforts: reasoningEfforts,
		})
		defaultCandidates = append(defaultCandidates, nil)
		if defaultReasoningEffort != "" {
			defaultCandidates[len(defaultCandidates)-1] = append(defaultCandidates[len(defaultCandidates)-1], defaultReasoningEffort)
		}
	}
	for index := range result {
		supported := stringSet(result[index].SupportedReasoningEfforts)
		for _, candidate := range defaultCandidates[index] {
			if _, ok := supported[candidate]; ok {
				result[index].DefaultReasoningEffort = candidate
				break
			}
		}
	}
	return result
}

func normalizeCatalogCapabilityList(values []string, allowed map[string]struct{}) []string {
	output := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if _, ok := allowed[normalized]; !ok {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		output = append(output, normalized)
	}
	return output
}

func catalogItemFromPort(item port.ManagementProviderModelCatalogItem) ModelCatalogItem {
	supportedServiceTiers := normalizeCatalogCapabilityList(item.SupportedServiceTiers, customProviderModelServiceTiers)
	supportedReasoningEfforts := normalizeCatalogCapabilityList(item.SupportedReasoningEfforts, customProviderModelReasoningEfforts)
	defaultReasoningEffort := strings.TrimSpace(item.DefaultReasoningEffort)
	if !stringListContains(supportedReasoningEfforts, defaultReasoningEffort) {
		defaultReasoningEffort = ""
	}
	output := ModelCatalogItem{
		ID:                        item.ID,
		ProviderCode:              item.ProviderCode,
		Model:                     item.Model,
		Scope:                     item.Scope,
		Status:                    item.Status,
		SystemAccountID:           item.SystemAccountID,
		PricingModel:              item.PricingModel,
		Mode:                      item.Mode,
		CatalogOrder:              cloneIntPtr(item.CatalogOrder),
		ReleaseDate:               item.ReleaseDate,
		ShutdownDate:              item.ShutdownDate,
		ContextWindowTokens:       cloneIntPtr(item.ContextWindowTokens),
		SupportedAPIProtocols:     dedupeStrings(item.SupportedAPIProtocols),
		SupportedServiceTiers:     supportedServiceTiers,
		SupportedReasoningEfforts: supportedReasoningEfforts,
		DefaultReasoningEffort:    defaultReasoningEffort,
		InputUSDPer1M:             cloneFloatPtr(item.InputUSDPer1M),
		OutputUSDPer1M:            cloneFloatPtr(item.OutputUSDPer1M),
		CachedInputUSDPer1M:       cloneFloatPtr(item.CachedInputUSDPer1M),
		CacheWriteUSDPer1M:        cloneFloatPtr(item.CacheWriteUSDPer1M),
		CacheWrite1hUSDPer1M:      cloneFloatPtr(item.CacheWrite1hUSDPer1M),
		ImageInputUSDPer1M:        cloneFloatPtr(item.ImageInputUSDPer1M),
		ImageOutputUSDPer1M:       cloneFloatPtr(item.ImageOutputUSDPer1M),
		AudioInputUSDPer1M:        cloneFloatPtr(item.AudioInputUSDPer1M),
		AudioOutputUSDPer1M:       cloneFloatPtr(item.AudioOutputUSDPer1M),
		OutputUSDPerImage:         cloneFloatPtr(item.OutputUSDPerImage),
		MaxInputTokens:            cloneIntPtr(item.MaxInputTokens),
		MaxOutputTokens:           cloneIntPtr(item.MaxOutputTokens),
		MaxTokens:                 cloneIntPtr(item.MaxTokens),
		SupportsPromptCaching:     item.SupportsPromptCaching,
		SupportsServiceTier:       len(supportedServiceTiers) > 0,
		CatalogVisible:            item.CatalogVisible,
		PricingNotes:              item.PricingNotes,
		CapabilityNotes:           item.CapabilityNotes,
		Notes:                     item.Notes,
		Source:                    item.Source,
	}
	if item.Scope == "built_in" {
		output.CodexSupportedReasoningLevels = dedupeStrings(item.CodexSupportedReasoningLevels)
		output.CodexDefaultReasoningLevel = strings.TrimSpace(item.CodexDefaultReasoningLevel)
		output.CodexMultiAgentVersion = strings.TrimSpace(item.CodexMultiAgentVersion)
	} else {
		output.CodexSupportedReasoningLevels = []string{}
	}
	if item.Scope != "built_in" {
		output.CreatedAt = formatOptionalTime(item.CreatedAt)
		output.UpdatedAt = formatOptionalTime(item.UpdatedAt)
	}
	return output
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	output := *value
	return &output
}

func cloneFloatPtr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	output := *value
	return &output
}

func dedupeStrings(values []string) []string {
	output := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		text := strings.TrimSpace(value)
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		output = append(output, text)
	}
	return output
}

func stringSet(values []string) map[string]struct{} {
	output := make(map[string]struct{}, len(values))
	for _, value := range values {
		output[value] = struct{}{}
	}
	return output
}

func stringListContains(values []string, target string) bool {
	if target == "" {
		return false
	}
	_, ok := stringSet(values)[target]
	return ok
}
