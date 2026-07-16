package managementprovidermodels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

var gptProviderModelServiceTiers = map[string]struct{}{"priority": {}, "flex": {}}
var gptProviderModelReasoningEfforts = map[string]struct{}{
	"none": {}, "minimal": {}, "low": {}, "medium": {}, "high": {}, "xhigh": {}, "max": {},
}

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
	Logger      *slog.Logger
}

type Service struct {
	store       Store
	invalidator CustomProviderModelInvalidator
	newID       func(prefix string) string
	logger      *slog.Logger
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
	ConfigurationTemplateID   OptionalString
	Scope                     OptionalString
	Model                     OptionalString
	Status                    OptionalString
	Mode                      OptionalString
	SupportedAPIProtocols     OptionalStringList
	SupportedServiceTiers     OptionalStringList
	SupportedReasoningEfforts OptionalStringList
	DefaultReasoningEffort    OptionalString
	ReleaseDate               OptionalString
	ShutdownDate              OptionalString
	ContextWindowTokens       OptionalInt
	MaxInputTokens            OptionalInt
	MaxOutputTokens           OptionalInt
	InputUSDPer1M             OptionalFloat
	OutputUSDPer1M            OptionalFloat
	CachedInputUSDPer1M       OptionalFloat
	CacheWriteUSDPer1M        OptionalFloat
	CacheWrite1hUSDPer1M      OptionalFloat
	ServiceTierPrices         OptionalProviderModelPriceMap
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

type OptionalProviderModelPriceMap struct {
	Set   bool
	Value map[string]ModelPriceSet
}

type ModelPriceSet = port.ManagementProviderModelPriceSet

type CustomModelCreateInput struct {
	ProviderCode          string
	ActorSystemAccountID  string
	ActorRole             string
	TargetSystemAccountID string
	Fields                CustomModelMutation
	TraceID               string
}

type CustomModelUpdateInput struct {
	ProviderCode         string
	ID                   string
	ActorSystemAccountID string
	ActorRole            string
	Fields               CustomModelMutation
	TraceID              string
}

type CustomModelDeleteInput struct {
	ProviderCode         string
	ID                   string
	ActorSystemAccountID string
	ActorRole            string
	TraceID              string
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

func (item ModelOption) MarshalJSON() ([]byte, error) {
	type modelOptionAlias ModelOption
	return json.Marshal(struct {
		modelOptionAlias
		DefaultReasoningEffort *string `json:"defaultReasoningEffort"`
	}{
		modelOptionAlias:       modelOptionAlias(item),
		DefaultReasoningEffort: nullableReasoningEffort(item.DefaultReasoningEffort),
	})
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
	ID                              string                                          `json:"id,omitempty"`
	ProviderCode                    string                                          `json:"providerCode"`
	Model                           string                                          `json:"model"`
	Scope                           string                                          `json:"scope"`
	Status                          string                                          `json:"status"`
	SystemAccountID                 string                                          `json:"systemAccountId,omitempty"`
	Mode                            string                                          `json:"mode,omitempty"`
	CatalogOrder                    *int                                            `json:"catalogOrder,omitempty"`
	ReleaseDate                     string                                          `json:"releaseDate,omitempty"`
	ShutdownDate                    string                                          `json:"shutdownDate,omitempty"`
	ContextWindowTokens             *int                                            `json:"contextWindowTokens,omitempty"`
	SupportedAPIProtocols           []string                                        `json:"supportedApiProtocols"`
	SupportedServiceTiers           []string                                        `json:"supportedServiceTiers"`
	SupportedReasoningEfforts       []string                                        `json:"supportedReasoningEfforts"`
	DefaultReasoningEffort          string                                          `json:"defaultReasoningEffort,omitempty"`
	CodexSupportedReasoningLevels   []string                                        `json:"codexSupportedReasoningLevels"`
	CodexDefaultReasoningLevel      string                                          `json:"codexDefaultReasoningLevel,omitempty"`
	CodexMultiAgentVersion          string                                          `json:"codexMultiAgentVersion,omitempty"`
	InputUSDPer1M                   *float64                                        `json:"inputUsdPer1M,omitempty"`
	OutputUSDPer1M                  *float64                                        `json:"outputUsdPer1M,omitempty"`
	CachedInputUSDPer1M             *float64                                        `json:"cachedInputUsdPer1M,omitempty"`
	CacheWriteUSDPer1M              *float64                                        `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hUSDPer1M            *float64                                        `json:"cacheWrite1hUsdPer1M,omitempty"`
	ServiceTierPrices               map[string]port.ManagementProviderModelPriceSet `json:"serviceTierPrices,omitempty"`
	LongContextInputTokenThreshold  *int                                            `json:"longContextInputTokenThreshold,omitempty"`
	LongContextInputCostMultiplier  *float64                                        `json:"longContextInputCostMultiplier,omitempty"`
	LongContextOutputCostMultiplier *float64                                        `json:"longContextOutputCostMultiplier,omitempty"`
	ImageInputUSDPer1M              *float64                                        `json:"imageInputUsdPer1M,omitempty"`
	ImageOutputUSDPer1M             *float64                                        `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUSDPer1M              *float64                                        `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUSDPer1M             *float64                                        `json:"audioOutputUsdPer1M,omitempty"`
	OutputUSDPerImage               *float64                                        `json:"outputUsdPerImage,omitempty"`
	MaxInputTokens                  *int                                            `json:"maxInputTokens,omitempty"`
	MaxOutputTokens                 *int                                            `json:"maxOutputTokens,omitempty"`
	MaxTokens                       *int                                            `json:"maxTokens,omitempty"`
	SupportsPromptCaching           bool                                            `json:"supportsPromptCaching"`
	SupportsServiceTier             bool                                            `json:"supportsServiceTier"`
	CatalogVisible                  bool                                            `json:"catalogVisible"`
	PricingNotes                    string                                          `json:"pricingNotes,omitempty"`
	CapabilityNotes                 string                                          `json:"capabilityNotes,omitempty"`
	Notes                           string                                          `json:"notes,omitempty"`
	CreatedAt                       string                                          `json:"createdAt,omitempty"`
	UpdatedAt                       string                                          `json:"updatedAt,omitempty"`
	Source                          string                                          `json:"source"`
}

func (item ModelCatalogItem) MarshalJSON() ([]byte, error) {
	type modelCatalogItemAlias ModelCatalogItem
	return json.Marshal(struct {
		modelCatalogItemAlias
		DefaultReasoningEffort *string `json:"defaultReasoningEffort"`
	}{
		modelCatalogItemAlias:  modelCatalogItemAlias(item),
		DefaultReasoningEffort: nullableReasoningEffort(item.DefaultReasoningEffort),
	})
}

func nullableReasoningEffort(value string) *string {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return nil
	}
	return &normalized
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
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: opts.Store, invalidator: opts.Invalidator, newID: newID, logger: logger}
}

func (s *Service) ModelOptions(ctx context.Context, input ModelOptionListInput) ([]ModelOption, error) {
	if s.store == nil {
		return nil, fmt.Errorf("management provider model store is required")
	}
	providerCodes, err := s.optionProviderCodes(ctx, input.Protocol)
	if err != nil {
		return nil, err
	}
	catalogSources, err := s.modelOptionCatalogSources(ctx, providerCodes)
	if err != nil {
		return nil, err
	}
	builtInCodes, customCodes := modelOptionCatalogQueryProviderCodes(catalogSources)
	rows, err := s.store.ListManagementProviderModelCatalog(ctx, port.ManagementProviderModelCatalogListInput{
		BuiltInProviderCodes: builtInCodes,
		CustomProviderCodes:  customCodes,
		SystemAccountID:      strings.TrimSpace(input.SystemAccountID),
		IncludeInactive:      false,
	})
	if err != nil {
		return nil, err
	}
	items := make([]port.ManagementProviderModelCatalogItem, 0, len(rows))
	for _, source := range catalogSources {
		catalogRows := modelOptionCatalogItems(rows, source)
		items = append(items, sortCatalogItems(mergeCatalogItems(catalogRows, mergeKeyModel))...)
	}
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
	if scope == "personal" && ownerSystemAccountID == "" {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "请选择模型归属的系统账户"}
	}
	var template *port.ManagementProviderModelCatalogItem
	if input.Fields.ConfigurationTemplateID.Set {
		resolved, err := s.resolveConfigurationTemplate(ctx, provider.Code, ownerSystemAccountID, input.Fields.ConfigurationTemplateID.Value)
		if err != nil {
			return ModelCatalogItem{}, err
		}
		template = &resolved
	}
	saveInput, err := customModelSaveInputFromCreate(provider.Code, scope, ownerSystemAccountID, actorSystemAccountID, input.Fields, template)
	if err != nil {
		return ModelCatalogItem{}, err
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
	s.invalidateCustomProviderModel(ctx, CustomProviderModelSavedReason, input.TraceID)
	return catalogItemFromPort(saved), nil
}

func (s *Service) UpdateCustomModel(ctx context.Context, input CustomModelUpdateInput) (ModelCatalogItem, error) {
	if s.store == nil {
		return ModelCatalogItem{}, fmt.Errorf("management provider model store is required")
	}
	builtIn, foundBuiltIn, err := s.findBuiltInModelByID(ctx, strings.TrimSpace(input.ProviderCode), strings.TrimSpace(input.ID))
	if err != nil {
		return ModelCatalogItem{}, err
	}
	if foundBuiltIn {
		return s.updateBuiltInModelConfiguration(ctx, builtIn, input)
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
	if input.Fields.ConfigurationTemplateID.Set {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "配置模板只能在新建模型时使用"}
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
	s.invalidateCustomProviderModel(ctx, CustomProviderModelSavedReason, input.TraceID)
	if saved.Status != "active" {
		if err := s.clearDefaultHealthCheckModelReferences(ctx, saved); err != nil {
			return ModelCatalogItem{}, err
		}
	}
	return catalogItemFromPort(saved), nil
}

func (s *Service) findBuiltInModelByID(ctx context.Context, providerCode string, id string) (port.ManagementProviderModelCatalogItem, bool, error) {
	builtInCodes, _, err := s.sourceProviderCodes(ctx, providerCode)
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, false, err
	}
	rows, err := s.store.ListManagementProviderModelCatalog(ctx, port.ManagementProviderModelCatalogListInput{
		BuiltInProviderCodes: builtInCodes, CustomProviderCodes: []string{}, IncludeInactive: true,
	})
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, false, err
	}
	for _, row := range rows {
		if row.Scope == "built_in" && row.ID == id && row.ProviderCode == providerCode {
			return row, true, nil
		}
	}
	return port.ManagementProviderModelCatalogItem{}, false, nil
}

func (s *Service) updateBuiltInModelConfiguration(ctx context.Context, existing port.ManagementProviderModelCatalogItem, input CustomModelUpdateInput) (ModelCatalogItem, error) {
	if !isAdminRole(input.ActorRole) {
		return ModelCatalogItem{}, &CustomModelForbiddenError{Message: "只有管理员可以维护内置模型配置"}
	}
	if input.Fields.Invalid || !builtInModelConfigurationMutationOnly(input.Fields) {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "内置模型配置参数无效"}
	}
	if input.Fields.Status.Set && strings.TrimSpace(input.Fields.Status.Value) == "draft" {
		return ModelCatalogItem{}, &CustomModelValidationError{Message: "内置模型配置参数无效"}
	}
	configuration := customModelSaveInputFromExisting(existing, strings.TrimSpace(input.ActorSystemAccountID))
	configuration.Mode = customModelModeFromCatalog(existing)
	if err := applyCustomModelMutableFields(&configuration, input.Fields, false); err != nil {
		return ModelCatalogItem{}, err
	}
	persisted, found, err := s.store.UpdateManagementBuiltInProviderModelPrices(ctx, port.ManagementBuiltInProviderModelPriceUpdateInput{
		ID: existing.ID, ProviderCode: existing.ProviderCode,
		Status: builtInProviderModelOptionalString(input.Fields.Status), Mode: builtInProviderModelOptionalString(input.Fields.Mode),
		SupportedAPIProtocols:     builtInProviderModelOptionalStringList(input.Fields.SupportedAPIProtocols),
		SupportedServiceTiers:     builtInProviderModelOptionalStringList(input.Fields.SupportedServiceTiers),
		SupportedReasoningEfforts: builtInProviderModelOptionalStringList(input.Fields.SupportedReasoningEfforts),
		DefaultReasoningEffort:    builtInProviderModelOptionalString(input.Fields.DefaultReasoningEffort),
		ReleaseDate:               builtInProviderModelOptionalString(input.Fields.ReleaseDate), ShutdownDate: builtInProviderModelOptionalString(input.Fields.ShutdownDate),
		ContextWindowTokens: builtInProviderModelOptionalInt(input.Fields.ContextWindowTokens), MaxInputTokens: builtInProviderModelOptionalInt(input.Fields.MaxInputTokens), MaxOutputTokens: builtInProviderModelOptionalInt(input.Fields.MaxOutputTokens),
		InputUSDPer1M: builtInProviderModelOptionalFloat(input.Fields.InputUSDPer1M), OutputUSDPer1M: builtInProviderModelOptionalFloat(input.Fields.OutputUSDPer1M),
		CachedInputUSDPer1M: builtInProviderModelOptionalFloat(input.Fields.CachedInputUSDPer1M), CacheWriteUSDPer1M: builtInProviderModelOptionalFloat(input.Fields.CacheWriteUSDPer1M),
		CacheWrite1hUSDPer1M: builtInProviderModelOptionalFloat(input.Fields.CacheWrite1hUSDPer1M),
		ServiceTierPrices: port.ManagementProviderModelOptionalPriceMap{
			Present: input.Fields.ServiceTierPrices.Set,
			Value:   cloneProviderModelPriceMap(input.Fields.ServiceTierPrices.Value),
		},
		ImageInputUSDPer1M:  builtInProviderModelOptionalFloat(input.Fields.ImageInputUSDPer1M),
		ImageOutputUSDPer1M: builtInProviderModelOptionalFloat(input.Fields.ImageOutputUSDPer1M),
		AudioInputUSDPer1M:  builtInProviderModelOptionalFloat(input.Fields.AudioInputUSDPer1M),
		AudioOutputUSDPer1M: builtInProviderModelOptionalFloat(input.Fields.AudioOutputUSDPer1M),
		OutputUSDPerImage:   builtInProviderModelOptionalFloat(input.Fields.OutputUSDPerImage),
	})
	if err != nil {
		return ModelCatalogItem{}, err
	}
	if !found {
		return ModelCatalogItem{}, ErrCustomProviderModelNotFound
	}
	s.invalidateCustomProviderModel(ctx, CustomProviderModelSavedReason, input.TraceID)
	updated := existing
	updated.Status = persisted.Status
	updated.Mode = persisted.Mode
	updated.SupportedAPIProtocols = append([]string(nil), persisted.SupportedAPIProtocols...)
	updated.SupportedServiceTiers = append([]string(nil), persisted.SupportedServiceTiers...)
	updated.SupportedReasoningEfforts = append([]string(nil), persisted.SupportedReasoningEfforts...)
	updated.DefaultReasoningEffort = persisted.DefaultReasoningEffort
	updated.ReleaseDate = persisted.ReleaseDate
	updated.ShutdownDate = persisted.ShutdownDate
	updated.ContextWindowTokens = cloneIntPtr(persisted.ContextWindowTokens)
	updated.MaxInputTokens = cloneIntPtr(persisted.MaxInputTokens)
	updated.MaxOutputTokens = cloneIntPtr(persisted.MaxOutputTokens)
	updated.InputUSDPer1M = cloneFloatPtr(persisted.InputUSDPer1M)
	updated.OutputUSDPer1M = cloneFloatPtr(persisted.OutputUSDPer1M)
	updated.CachedInputUSDPer1M = cloneFloatPtr(persisted.CachedInputUSDPer1M)
	updated.CacheWriteUSDPer1M = cloneFloatPtr(persisted.CacheWriteUSDPer1M)
	updated.CacheWrite1hUSDPer1M = cloneFloatPtr(persisted.CacheWrite1hUSDPer1M)
	updated.ServiceTierPrices = cloneProviderModelPriceMap(persisted.ServiceTierPrices)
	updated.ImageInputUSDPer1M = cloneFloatPtr(persisted.ImageInputUSDPer1M)
	updated.ImageOutputUSDPer1M = cloneFloatPtr(persisted.ImageOutputUSDPer1M)
	updated.AudioInputUSDPer1M = cloneFloatPtr(persisted.AudioInputUSDPer1M)
	updated.AudioOutputUSDPer1M = cloneFloatPtr(persisted.AudioOutputUSDPer1M)
	updated.OutputUSDPerImage = cloneFloatPtr(persisted.OutputUSDPerImage)
	updated.UpdatedAt = persisted.UpdatedAt
	result := catalogItemFromPort(updated)
	result.UpdatedAt = formatOptionalTime(persisted.UpdatedAt)
	return result, nil
}

func builtInProviderModelOptionalString(value OptionalString) port.ManagementProviderModelOptionalString {
	return port.ManagementProviderModelOptionalString{Present: value.Set, Value: strings.TrimSpace(value.Value)}
}

func builtInProviderModelOptionalStringList(value OptionalStringList) port.ManagementProviderModelOptionalStringList {
	return port.ManagementProviderModelOptionalStringList{Present: value.Set, Value: append([]string(nil), value.Value...)}
}

func builtInProviderModelOptionalInt(value OptionalInt) port.ManagementProviderModelOptionalInt {
	return port.ManagementProviderModelOptionalInt{Present: value.Set, Value: cloneIntPtr(value.Value)}
}

func builtInProviderModelOptionalFloat(value OptionalFloat) port.ManagementProviderModelOptionalFloat {
	return port.ManagementProviderModelOptionalFloat{Present: value.Set, Value: value.Value}
}

func builtInModelConfigurationMutationOnly(fields CustomModelMutation) bool {
	return customModelMutationHasAnyField(fields) && !(fields.Scope.Set || fields.Model.Set || fields.ConfigurationTemplateID.Set || fields.PricingNotes.Set || fields.CapabilityNotes.Set || fields.Notes.Set)
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
		s.invalidateCustomProviderModel(ctx, CustomProviderModelDeletedReason, input.TraceID)
		if err := s.clearDefaultHealthCheckModelReferences(ctx, existing); err != nil {
			return CustomModelDeleteResult{}, err
		}
	}
	return CustomModelDeleteResult{Deleted: deleted}, nil
}

func (s *Service) optionProviderCodes(ctx context.Context, protocol string) ([]string, error) {
	enabledCodes, err := s.store.ListManagementEnabledModelProviderCodes(ctx)
	if err != nil {
		return nil, err
	}
	enabledCodes = dedupeStrings(enabledCodes)
	protocolCode, protocolVersion, ok := protocolFilter(strings.TrimSpace(protocol))
	if !ok {
		return enabledCodes, nil
	}
	protocolCodes, err := s.store.ListManagementProviderCodesByProtocol(ctx, protocolCode, protocolVersion)
	if err != nil {
		return nil, err
	}
	allowed := stringSet(dedupeStrings(protocolCodes))
	output := make([]string, 0, len(enabledCodes))
	for _, code := range enabledCodes {
		if _, ok := allowed[code]; ok {
			output = append(output, code)
		}
	}
	return output, nil
}

type modelOptionCatalogSource struct {
	builtInProviderCodes []string
	customProviderCodes  []string
}

func (s *Service) modelOptionCatalogSources(ctx context.Context, providerCodes []string) ([]modelOptionCatalogSource, error) {
	sources := make([]modelOptionCatalogSource, 0, len(providerCodes))
	for _, providerCode := range providerCodes {
		code := strings.TrimSpace(providerCode)
		switch code {
		case "", hybridProviderCode:
			continue
		case openAIProviderCode:
			openAIProtocolCodes, err := s.store.ListManagementProviderCodesByProtocol(ctx, protocolOpenAI, "v1")
			if err != nil {
				return nil, err
			}
			builtInCodes, customCodes := openAIModelOptionSourceProviderCodes(openAIProtocolCodes)
			sources = append(sources, modelOptionCatalogSource{
				builtInProviderCodes: builtInCodes,
				customProviderCodes:  customCodes,
			})
		default:
			sources = append(sources, modelOptionCatalogSource{
				builtInProviderCodes: []string{code},
				customProviderCodes:  []string{code},
			})
		}
	}
	return sources, nil
}

func openAIModelOptionSourceProviderCodes(providerCodes []string) ([]string, []string) {
	childCodes := make([]string, 0, len(providerCodes))
	for _, providerCode := range dedupeStrings(providerCodes) {
		if providerCode == openAIProviderCode {
			continue
		}
		childCodes = append(childCodes, providerCode)
	}
	builtInCodes := append([]string(nil), childCodes...)
	customCodes := append(append([]string(nil), childCodes...), openAIProviderCode)
	return builtInCodes, customCodes
}

func modelOptionCatalogQueryProviderCodes(sources []modelOptionCatalogSource) ([]string, []string) {
	builtInCodes := []string{}
	customCodes := []string{}
	for _, source := range sources {
		builtInCodes = append(builtInCodes, source.builtInProviderCodes...)
		customCodes = append(customCodes, source.customProviderCodes...)
	}
	return dedupeStrings(builtInCodes), dedupeStrings(customCodes)
}

func modelOptionCatalogItems(rows []port.ManagementProviderModelCatalogItem, source modelOptionCatalogSource) []port.ManagementProviderModelCatalogItem {
	items := make([]port.ManagementProviderModelCatalogItem, 0, len(rows))
	appendMatching := func(providerCodes []string, builtIn bool) {
		for _, providerCode := range providerCodes {
			for _, item := range rows {
				if !strings.EqualFold(strings.TrimSpace(item.ProviderCode), providerCode) {
					continue
				}
				if (strings.TrimSpace(item.Scope) == "built_in") != builtIn {
					continue
				}
				items = append(items, item)
			}
		}
	}
	appendMatching(source.builtInProviderCodes, true)
	appendMatching(source.customProviderCodes, false)
	return items
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
		codes := []string{}
		for _, protocol := range [][2]string{
			{protocolOpenAI, "v1"},
			{protocolAnthropic, "v1"},
			{protocolGemini, "v1beta"},
		} {
			protocolCodes, err := s.store.ListManagementProviderCodesByProtocol(ctx, protocol[0], protocol[1])
			if err != nil {
				return nil, nil, err
			}
			codes = append(codes, protocolCodes...)
		}
		codes = dedupeStrings(codes)
		sourceCodes := make([]string, 0, len(codes))
		for _, sourceCode := range codes {
			if sourceCode != hybridProviderCode {
				sourceCodes = append(sourceCodes, sourceCode)
			}
		}
		return append([]string(nil), sourceCodes...), append([]string(nil), sourceCodes...), nil
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
	template *port.ManagementProviderModelCatalogItem,
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
	if template != nil {
		applyConfigurationTemplate(&saveInput, *template)
	}
	if err := applyCustomModelMutableFields(&saveInput, fields, template == nil); err != nil {
		return port.ManagementCustomProviderModelSaveInput{}, err
	}
	return saveInput, nil
}

func (s *Service) resolveConfigurationTemplate(ctx context.Context, providerCode string, systemAccountID string, id string) (port.ManagementProviderModelCatalogItem, error) {
	templateID := strings.TrimSpace(id)
	if templateID == "" {
		return port.ManagementProviderModelCatalogItem{}, &CustomModelValidationError{Message: "配置模板不可用"}
	}
	builtInProviderCodes, customProviderCodes, err := s.sourceProviderCodes(ctx, providerCode)
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	items, err := s.store.ListManagementProviderModelCatalog(ctx, port.ManagementProviderModelCatalogListInput{
		BuiltInProviderCodes: builtInProviderCodes,
		CustomProviderCodes:  customProviderCodes,
		SystemAccountID:      strings.TrimSpace(systemAccountID),
		IncludeInactive:      true,
	})
	if err != nil {
		return port.ManagementProviderModelCatalogItem{}, err
	}
	mergeKey := mergeKeyModel
	if strings.TrimSpace(providerCode) == hybridProviderCode {
		mergeKey = mergeKeyProviderModel
	}
	for _, item := range mergeCatalogItems(items, mergeKey) {
		if item.ID == templateID && item.Status == "active" {
			return item, nil
		}
	}
	return port.ManagementProviderModelCatalogItem{}, &CustomModelValidationError{Message: "配置模板不可用"}
}

func applyConfigurationTemplate(input *port.ManagementCustomProviderModelSaveInput, template port.ManagementProviderModelCatalogItem) {
	input.Mode = customModelModeFromCatalog(template)
	input.SupportedAPIProtocols = append([]string(nil), template.SupportedAPIProtocols...)
	input.SupportedServiceTiers = append([]string(nil), template.SupportedServiceTiers...)
	input.SupportedReasoningEfforts = append([]string(nil), template.SupportedReasoningEfforts...)
	input.DefaultReasoningEffort = template.DefaultReasoningEffort
	input.ReleaseDate = template.ReleaseDate
	input.ShutdownDate = template.ShutdownDate
	input.ContextWindowTokens = cloneIntPtr(template.ContextWindowTokens)
	input.MaxInputTokens = cloneIntPtr(template.MaxInputTokens)
	input.MaxOutputTokens = cloneIntPtr(template.MaxOutputTokens)
	input.InputUSDPer1M = cloneFloatPtr(template.InputUSDPer1M)
	input.OutputUSDPer1M = cloneFloatPtr(template.OutputUSDPer1M)
	input.CachedInputUSDPer1M = cloneFloatPtr(template.CachedInputUSDPer1M)
	input.CacheWriteUSDPer1M = cloneFloatPtr(template.CacheWriteUSDPer1M)
	input.CacheWrite1hUSDPer1M = cloneFloatPtr(template.CacheWrite1hUSDPer1M)
	input.ServiceTierPrices = cloneProviderModelPriceMap(template.ServiceTierPrices)
	input.ImageInputUSDPer1M = cloneFloatPtr(template.ImageInputUSDPer1M)
	input.ImageOutputUSDPer1M = cloneFloatPtr(template.ImageOutputUSDPer1M)
	input.AudioInputUSDPer1M = cloneFloatPtr(template.AudioInputUSDPer1M)
	input.AudioOutputUSDPer1M = cloneFloatPtr(template.AudioOutputUSDPer1M)
	input.OutputUSDPerImage = cloneFloatPtr(template.OutputUSDPerImage)
	input.PricingNotes = template.PricingNotes
	input.CapabilityNotes = template.CapabilityNotes
	input.Notes = template.Notes
}

func customModelModeFromCatalog(item port.ManagementProviderModelCatalogItem) string {
	mode := strings.TrimSpace(item.Mode)
	if mode == "image" || mode == "audio" {
		return mode
	}
	for _, protocol := range item.SupportedAPIProtocols {
		switch protocol {
		case "images":
			return "image"
		case "audio":
			return "audio"
		}
	}
	return "text"
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
		ReleaseDate:               item.ReleaseDate,
		ShutdownDate:              item.ShutdownDate,
		ContextWindowTokens:       cloneIntPtr(item.ContextWindowTokens),
		MaxInputTokens:            cloneIntPtr(item.MaxInputTokens),
		MaxOutputTokens:           cloneIntPtr(item.MaxOutputTokens),
		InputUSDPer1M:             cloneFloatPtr(item.InputUSDPer1M),
		OutputUSDPer1M:            cloneFloatPtr(item.OutputUSDPer1M),
		CachedInputUSDPer1M:       cloneFloatPtr(item.CachedInputUSDPer1M),
		CacheWriteUSDPer1M:        cloneFloatPtr(item.CacheWriteUSDPer1M),
		CacheWrite1hUSDPer1M:      cloneFloatPtr(item.CacheWrite1hUSDPer1M),
		ServiceTierPrices:         cloneProviderModelPriceMap(item.ServiceTierPrices),
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
		limit := 16
		if input.ProviderCode == "gpt" {
			limit = 2
		}
		if len(fields.SupportedServiceTiers.Value) > limit {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		serviceTiers, err := normalizeCustomModelCapabilityList(fields.SupportedServiceTiers.Value)
		if err != nil {
			return err
		}
		input.SupportedServiceTiers = serviceTiers
	} else if create {
		input.SupportedServiceTiers = []string{}
	}
	if fields.SupportedReasoningEfforts.Set {
		limit := 16
		if input.ProviderCode == "gpt" {
			limit = 7
		}
		if len(fields.SupportedReasoningEfforts.Value) > limit {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		reasoningEfforts, err := normalizeCustomModelCapabilityList(fields.SupportedReasoningEfforts.Value)
		if err != nil {
			return err
		}
		input.SupportedReasoningEfforts = reasoningEfforts
	} else if create {
		input.SupportedReasoningEfforts = []string{}
	}
	if fields.DefaultReasoningEffort.Set {
		defaultReasoningEffort := strings.TrimSpace(fields.DefaultReasoningEffort.Value)
		if defaultReasoningEffort != "" && !validCustomModelCapabilityToken(defaultReasoningEffort) {
			return &CustomModelValidationError{Message: "自定义模型参数无效"}
		}
		input.DefaultReasoningEffort = defaultReasoningEffort
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
	if fields.MaxInputTokens.Set {
		if err := validateOptionalNonnegativeInt(fields.MaxInputTokens.Value); err != nil {
			return err
		}
		input.MaxInputTokens = cloneIntPtr(fields.MaxInputTokens.Value)
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
	if err := applyOptionalCustomModelFloat(&input.CacheWrite1hUSDPer1M, fields.CacheWrite1hUSDPer1M); err != nil {
		return err
	}
	if fields.ServiceTierPrices.Set {
		input.ServiceTierPrices = cloneProviderModelPriceMap(fields.ServiceTierPrices.Value)
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
	return fields.ConfigurationTemplateID.Set ||
		fields.Scope.Set ||
		fields.Model.Set ||
		fields.Status.Set ||
		fields.Mode.Set ||
		fields.SupportedAPIProtocols.Set ||
		fields.SupportedServiceTiers.Set ||
		fields.SupportedReasoningEfforts.Set ||
		fields.DefaultReasoningEffort.Set ||
		fields.ReleaseDate.Set ||
		fields.ShutdownDate.Set ||
		fields.ContextWindowTokens.Set ||
		fields.MaxInputTokens.Set ||
		fields.MaxOutputTokens.Set ||
		fields.InputUSDPer1M.Set ||
		fields.OutputUSDPer1M.Set ||
		fields.CachedInputUSDPer1M.Set ||
		fields.CacheWriteUSDPer1M.Set ||
		fields.CacheWrite1hUSDPer1M.Set ||
		fields.ServiceTierPrices.Set ||
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

func normalizeCustomModelCapabilityList(values []string) ([]string, error) {
	output := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if value != strings.TrimSpace(value) || !validCustomModelCapabilityToken(value) {
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
	isTextModel := mode == "" || mode == "text"
	if err := validateServiceTierPriceKeys(mode, input.SupportedServiceTiers, input.ServiceTierPrices); err != nil {
		return err
	}
	if !isTextModel {
		if hasCapabilities {
			return &CustomModelValidationError{Message: "只有文本自定义模型支持服务等级和思考能力配置"}
		}
		return nil
	}
	if strings.TrimSpace(input.ProviderCode) == "gpt" {
		for _, value := range input.SupportedServiceTiers {
			if _, ok := gptProviderModelServiceTiers[value]; !ok {
				return &CustomModelValidationError{Message: "自定义模型参数无效"}
			}
		}
		for _, value := range input.SupportedReasoningEfforts {
			if _, ok := gptProviderModelReasoningEfforts[value]; !ok {
				return &CustomModelValidationError{Message: "自定义模型参数无效"}
			}
		}
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
	_ = ctx
	_ = ownerSystemAccountID
	hasDirectPriceConfigured := customModelSaveInputHasDirectPrice(input)
	if input.Status == "active" && !hasDirectPriceConfigured {
		return &CustomModelValidationError{Message: "启用的自定义模型必须配置价格"}
	}
	return nil
}

func customModelSaveInputHasDirectPrice(input port.ManagementCustomProviderModelSaveInput) bool {
	switch strings.TrimSpace(input.Mode) {
	case "image":
		return input.ImageInputUSDPer1M != nil || input.ImageOutputUSDPer1M != nil || input.OutputUSDPerImage != nil
	case "audio":
		return input.AudioInputUSDPer1M != nil || input.AudioOutputUSDPer1M != nil
	default:
		return input.InputUSDPer1M != nil || input.OutputUSDPer1M != nil || input.CachedInputUSDPer1M != nil ||
			input.CacheWriteUSDPer1M != nil || input.CacheWrite1hUSDPer1M != nil || providerModelPriceMapHasAnyPrice(input.ServiceTierPrices)
	}
}

func validateServiceTierPriceKeys(mode string, supported []string, prices map[string]port.ManagementProviderModelPriceSet) error {
	if len(prices) == 0 {
		return nil
	}
	if strings.TrimSpace(mode) == "image" || strings.TrimSpace(mode) == "audio" {
		return &CustomModelValidationError{Message: "只有文本自定义模型支持服务档位价格"}
	}
	supportedSet := stringSet(supported)
	for tier := range prices {
		if _, ok := supportedSet[strings.TrimSpace(tier)]; !ok {
			return &CustomModelValidationError{Message: "服务档位价格必须属于模型支持的服务等级"}
		}
	}
	return nil
}

func providerModelPriceMapHasAnyPrice(prices map[string]port.ManagementProviderModelPriceSet) bool {
	for _, price := range prices {
		if price.InputUSDPer1M != nil || price.OutputUSDPer1M != nil || price.CachedInputUSDPer1M != nil ||
			price.CacheWriteUSDPer1M != nil || price.CacheWrite1hUSDPer1M != nil || price.ImageInputUSDPer1M != nil ||
			price.ImageOutputUSDPer1M != nil || price.AudioInputUSDPer1M != nil || price.AudioOutputUSDPer1M != nil || price.OutputUSDPerImage != nil {
			return true
		}
	}
	return false
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

func (s *Service) invalidateCustomProviderModel(ctx context.Context, reason string, traceID string) {
	if s.invalidator == nil {
		return
	}
	if err := s.invalidator.InvalidateCustomProviderModelChanged(ctx, reason); err != nil {
		s.logger.WarnContext(ctx, "模型已保存，但缓存同步失败",
			slog.String("event", "model_cache_sync_failed_after_commit"),
			slog.String("reason", reason),
			slog.String("trace_id", strings.TrimSpace(traceID)),
			slog.Any("error", err),
		)
	}
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
	_ = all
	return hasDirectPrice(item)
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
		serviceTiers := normalizeCatalogCapabilityList(item.SupportedServiceTiers)
		reasoningEfforts := normalizeCatalogCapabilityList(item.SupportedReasoningEfforts)
		defaultReasoningEffort := strings.TrimSpace(item.DefaultReasoningEffort)
		if !validCustomModelCapabilityToken(defaultReasoningEffort) {
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

func normalizeCatalogCapabilityList(values []string) []string {
	output := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if !validCustomModelCapabilityToken(normalized) {
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

func validCustomModelCapabilityToken(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		if index > 0 && (char == '.' || char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func catalogItemFromPort(item port.ManagementProviderModelCatalogItem) ModelCatalogItem {
	supportedServiceTiers := normalizeCatalogCapabilityList(item.SupportedServiceTiers)
	supportedReasoningEfforts := normalizeCatalogCapabilityList(item.SupportedReasoningEfforts)
	defaultReasoningEffort := strings.TrimSpace(item.DefaultReasoningEffort)
	if !stringListContains(supportedReasoningEfforts, defaultReasoningEffort) {
		defaultReasoningEffort = ""
	}
	output := ModelCatalogItem{
		ID:                              item.ID,
		ProviderCode:                    item.ProviderCode,
		Model:                           item.Model,
		Scope:                           item.Scope,
		Status:                          item.Status,
		SystemAccountID:                 item.SystemAccountID,
		Mode:                            item.Mode,
		CatalogOrder:                    cloneIntPtr(item.CatalogOrder),
		ReleaseDate:                     item.ReleaseDate,
		ShutdownDate:                    item.ShutdownDate,
		ContextWindowTokens:             cloneIntPtr(item.ContextWindowTokens),
		SupportedAPIProtocols:           dedupeStrings(item.SupportedAPIProtocols),
		SupportedServiceTiers:           supportedServiceTiers,
		SupportedReasoningEfforts:       supportedReasoningEfforts,
		DefaultReasoningEffort:          defaultReasoningEffort,
		InputUSDPer1M:                   cloneFloatPtr(item.InputUSDPer1M),
		OutputUSDPer1M:                  cloneFloatPtr(item.OutputUSDPer1M),
		CachedInputUSDPer1M:             cloneFloatPtr(item.CachedInputUSDPer1M),
		CacheWriteUSDPer1M:              cloneFloatPtr(item.CacheWriteUSDPer1M),
		CacheWrite1hUSDPer1M:            cloneFloatPtr(item.CacheWrite1hUSDPer1M),
		ServiceTierPrices:               cloneProviderModelPriceMap(item.ServiceTierPrices),
		LongContextInputTokenThreshold:  cloneIntPtr(item.LongContextInputTokenThreshold),
		LongContextInputCostMultiplier:  cloneFloatPtr(item.LongContextInputCostMultiplier),
		LongContextOutputCostMultiplier: cloneFloatPtr(item.LongContextOutputCostMultiplier),
		ImageInputUSDPer1M:              cloneFloatPtr(item.ImageInputUSDPer1M),
		ImageOutputUSDPer1M:             cloneFloatPtr(item.ImageOutputUSDPer1M),
		AudioInputUSDPer1M:              cloneFloatPtr(item.AudioInputUSDPer1M),
		AudioOutputUSDPer1M:             cloneFloatPtr(item.AudioOutputUSDPer1M),
		OutputUSDPerImage:               cloneFloatPtr(item.OutputUSDPerImage),
		MaxInputTokens:                  cloneIntPtr(item.MaxInputTokens),
		MaxOutputTokens:                 cloneIntPtr(item.MaxOutputTokens),
		MaxTokens:                       cloneIntPtr(item.MaxTokens),
		SupportsPromptCaching:           item.SupportsPromptCaching,
		SupportsServiceTier:             len(supportedServiceTiers) > 0,
		CatalogVisible:                  item.CatalogVisible,
		PricingNotes:                    item.PricingNotes,
		CapabilityNotes:                 item.CapabilityNotes,
		Notes:                           item.Notes,
		Source:                          item.Source,
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

func cloneProviderModelPriceMap(value map[string]port.ManagementProviderModelPriceSet) map[string]port.ManagementProviderModelPriceSet {
	if len(value) == 0 {
		return map[string]port.ManagementProviderModelPriceSet{}
	}
	output := make(map[string]port.ManagementProviderModelPriceSet, len(value))
	for tier, prices := range value {
		output[tier] = port.ManagementProviderModelPriceSet{
			InputUSDPer1M: cloneFloatPtr(prices.InputUSDPer1M), OutputUSDPer1M: cloneFloatPtr(prices.OutputUSDPer1M),
			CachedInputUSDPer1M: cloneFloatPtr(prices.CachedInputUSDPer1M), CacheWriteUSDPer1M: cloneFloatPtr(prices.CacheWriteUSDPer1M),
			CacheWrite1hUSDPer1M: cloneFloatPtr(prices.CacheWrite1hUSDPer1M), ImageInputUSDPer1M: cloneFloatPtr(prices.ImageInputUSDPer1M),
			ImageOutputUSDPer1M: cloneFloatPtr(prices.ImageOutputUSDPer1M), AudioInputUSDPer1M: cloneFloatPtr(prices.AudioInputUSDPer1M),
			AudioOutputUSDPer1M: cloneFloatPtr(prices.AudioOutputUSDPer1M), OutputUSDPerImage: cloneFloatPtr(prices.OutputUSDPerImage),
		}
	}
	return output
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
