package managementprovidermodels

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	openAIProviderCode = "openai"
	hybridProviderCode = "hybrid"

	protocolOpenAI    = "openai"
	protocolAnthropic = "anthropic"
	protocolGemini    = "gemini"
)

var ErrProviderNotFound = errors.New("provider not found")

type Store interface {
	port.ManagementProviderModelCatalogReader
}

type Service struct {
	store Store
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

type ModelOption struct {
	ProviderCode          string   `json:"providerCode"`
	Model                 string   `json:"model"`
	SupportedAPIProtocols []string `json:"supportedApiProtocols,omitempty"`
}

type ModelCatalogItem struct {
	ID                    string   `json:"id,omitempty"`
	ProviderCode          string   `json:"providerCode"`
	Model                 string   `json:"model"`
	Scope                 string   `json:"scope"`
	Status                string   `json:"status"`
	SystemAccountID       string   `json:"systemAccountId,omitempty"`
	PricingModel          string   `json:"pricingModel,omitempty"`
	Mode                  string   `json:"mode,omitempty"`
	CatalogOrder          *int     `json:"catalogOrder,omitempty"`
	ReleaseDate           string   `json:"releaseDate,omitempty"`
	ShutdownDate          string   `json:"shutdownDate,omitempty"`
	ContextWindowTokens   *int     `json:"contextWindowTokens,omitempty"`
	SupportedAPIProtocols []string `json:"supportedApiProtocols"`
	InputUSDPer1M         *float64 `json:"inputUsdPer1M,omitempty"`
	OutputUSDPer1M        *float64 `json:"outputUsdPer1M,omitempty"`
	CachedInputUSDPer1M   *float64 `json:"cachedInputUsdPer1M,omitempty"`
	CacheWriteUSDPer1M    *float64 `json:"cacheWriteUsdPer1M,omitempty"`
	CacheWrite1hUSDPer1M  *float64 `json:"cacheWrite1hUsdPer1M,omitempty"`
	ImageInputUSDPer1M    *float64 `json:"imageInputUsdPer1M,omitempty"`
	ImageOutputUSDPer1M   *float64 `json:"imageOutputUsdPer1M,omitempty"`
	AudioInputUSDPer1M    *float64 `json:"audioInputUsdPer1M,omitempty"`
	AudioOutputUSDPer1M   *float64 `json:"audioOutputUsdPer1M,omitempty"`
	OutputUSDPerImage     *float64 `json:"outputUsdPerImage,omitempty"`
	MaxInputTokens        *int     `json:"maxInputTokens,omitempty"`
	MaxOutputTokens       *int     `json:"maxOutputTokens,omitempty"`
	MaxTokens             *int     `json:"maxTokens,omitempty"`
	SupportsPromptCaching bool     `json:"supportsPromptCaching"`
	SupportsServiceTier   bool     `json:"supportsServiceTier"`
	CatalogVisible        bool     `json:"catalogVisible"`
	PricingNotes          string   `json:"pricingNotes,omitempty"`
	CapabilityNotes       string   `json:"capabilityNotes,omitempty"`
	Notes                 string   `json:"notes,omitempty"`
	CreatedAt             string   `json:"createdAt,omitempty"`
	UpdatedAt             string   `json:"updatedAt,omitempty"`
	Source                string   `json:"source"`
}

func NewService(store Store) *Service {
	return &Service{store: store}
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

func (s *Service) expandProviderCodes(ctx context.Context, providerCodes []string) ([]string, []string, error) {
	builtInCodes := []string{}
	customCodes := []string{}
	for _, providerCode := range providerCodes {
		builtIn, custom, err := s.sourceProviderCodes(ctx, providerCode)
		if err != nil {
			return nil, nil, err
		}
		builtInCodes = append(builtInCodes, builtIn...)
		customCodes = append(customCodes, custom...)
	}
	return dedupeStrings(builtInCodes), dedupeStrings(customCodes), nil
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
	for _, item := range items {
		providerCode := strings.TrimSpace(item.ProviderCode)
		model := strings.TrimSpace(item.Model)
		if providerCode == "" || model == "" {
			continue
		}
		key := strings.ToLower(providerCode) + "\n" + model
		protocols := dedupeStrings(item.SupportedAPIProtocols)
		if index, exists := seen[key]; exists {
			result[index].SupportedAPIProtocols = dedupeStrings(append(result[index].SupportedAPIProtocols, protocols...))
			continue
		}
		seen[key] = len(result)
		result = append(result, ModelOption{
			ProviderCode:          providerCode,
			Model:                 model,
			SupportedAPIProtocols: protocols,
		})
	}
	return result
}

func catalogItemFromPort(item port.ManagementProviderModelCatalogItem) ModelCatalogItem {
	output := ModelCatalogItem{
		ID:                    item.ID,
		ProviderCode:          item.ProviderCode,
		Model:                 item.Model,
		Scope:                 item.Scope,
		Status:                item.Status,
		SystemAccountID:       item.SystemAccountID,
		PricingModel:          item.PricingModel,
		Mode:                  item.Mode,
		CatalogOrder:          cloneIntPtr(item.CatalogOrder),
		ReleaseDate:           item.ReleaseDate,
		ShutdownDate:          item.ShutdownDate,
		ContextWindowTokens:   cloneIntPtr(item.ContextWindowTokens),
		SupportedAPIProtocols: append([]string(nil), item.SupportedAPIProtocols...),
		InputUSDPer1M:         cloneFloatPtr(item.InputUSDPer1M),
		OutputUSDPer1M:        cloneFloatPtr(item.OutputUSDPer1M),
		CachedInputUSDPer1M:   cloneFloatPtr(item.CachedInputUSDPer1M),
		CacheWriteUSDPer1M:    cloneFloatPtr(item.CacheWriteUSDPer1M),
		CacheWrite1hUSDPer1M:  cloneFloatPtr(item.CacheWrite1hUSDPer1M),
		ImageInputUSDPer1M:    cloneFloatPtr(item.ImageInputUSDPer1M),
		ImageOutputUSDPer1M:   cloneFloatPtr(item.ImageOutputUSDPer1M),
		AudioInputUSDPer1M:    cloneFloatPtr(item.AudioInputUSDPer1M),
		AudioOutputUSDPer1M:   cloneFloatPtr(item.AudioOutputUSDPer1M),
		OutputUSDPerImage:     cloneFloatPtr(item.OutputUSDPerImage),
		MaxInputTokens:        cloneIntPtr(item.MaxInputTokens),
		MaxOutputTokens:       cloneIntPtr(item.MaxOutputTokens),
		MaxTokens:             cloneIntPtr(item.MaxTokens),
		SupportsPromptCaching: item.SupportsPromptCaching,
		SupportsServiceTier:   item.SupportsServiceTier,
		CatalogVisible:        item.CatalogVisible,
		Source:                item.Source,
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
