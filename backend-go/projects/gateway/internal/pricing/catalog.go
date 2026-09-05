package pricing

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// isOpenAICompatibleProviderCode mirrors provider-protocol
// isOpenAICompatibleProviderCode: 'openai' plus the 'gpt' vendor code.
func isOpenAICompatibleProviderCode(providerCode string) bool {
	normalized := normalizeProviderToken(providerCode)
	return normalized == "openai" || normalized == "gpt"
}

// canonicalOpenAIModelAlias mirrors canonicalOpenAIModelAlias.
func canonicalOpenAIModelAlias(model string) string {
	if model == "gpt-5.6" {
		return "gpt-5.6-sol"
	}
	return model
}

// modelDateSuffixPattern mirrors /-(?:\d{4}-\d{2}-\d{2}|\d{8})$/.
var modelDateSuffixPattern = regexp.MustCompile(`-(?:\d{4}-\d{2}-\d{2}|\d{8})$`)

// stripModelDateSuffix removes one trailing date suffix (YYYY-MM-DD or
// YYYYMMDD) from a model id.
func stripModelDateSuffix(model string) string {
	return modelDateSuffixPattern.ReplaceAllString(model, "")
}

// unavailableOpenAIModels mirrors unavailableOpenAIModels.
var unavailableOpenAIModels = map[string]bool{
	"chatgpt-4o-latest":         true,
	"codex-mini-latest":         true,
	"gpt-5.3-codex-spark":       true,
	"o1-2024-12-17":             true,
	"o1-pro-2025-03-19":         true,
	"o1-mini":                   true,
	"o3-mini-2025-01-31":        true,
	"o4-mini-2025-04-16":        true,
	"gpt-4-0125-preview":        true,
	"gpt-4-1106-vision-preview": true,
	"gpt-4-0314":                true,
	"gpt-4-32k":                 true,
	"gpt-4-32k-0314":            true,
	"gpt-4-32k-0613":            true,
}

func isUnavailableOpenAIModel(model string) bool {
	if unavailableOpenAIModels[model] {
		return true
	}
	for _, prefix := range []string{
		"gpt-4.5-preview",
		"gpt-4-turbo-preview",
		"gpt-4o-realtime-preview",
		"gpt-4o-mini-realtime-preview",
		"gpt-4o-audio-preview",
		"gpt-4o-mini-audio-preview",
		"gpt-4o-search-preview",
		"gpt-4o-mini-search-preview",
		"o1-preview",
	} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// buildOpenAIModelCandidates mirrors buildOpenAIModelCandidates (insertion
// order preserved — the first matching candidate wins).
func buildOpenAIModelCandidates(model string) []string {
	candidates := newCandidateSet()
	if withoutDate := stripModelDateSuffix(model); withoutDate != model {
		candidates.add(withoutDate)
	}
	for _, rule := range []struct{ prefix, base string }{
		{"gpt-5.6-sol-", "gpt-5.6-sol"},
		{"gpt-5.6-terra-", "gpt-5.6-terra"},
		{"gpt-5.6-luna-", "gpt-5.6-luna"},
		{"gpt-5.5-", "gpt-5.5"},
		{"gpt-5.4-mini-", "gpt-5.4-mini"},
		{"gpt-5.4-nano-", "gpt-5.4-nano"},
		{"gpt-5.4-", "gpt-5.4"},
		{"gpt-image-2-", "gpt-image-2"},
		{"gpt-4.1-nano-", "gpt-4.1-nano"},
		{"gpt-4.1-mini-", "gpt-4.1-mini"},
		{"gpt-4.1-", "gpt-4.1"},
	} {
		if strings.HasPrefix(model, rule.prefix) {
			candidates.add(rule.base)
		}
	}
	if model == "gpt-5.3-codex" {
		candidates.add("gpt-5.3-codex")
	}
	return candidates.list
}

// glmModelCandidateBases mirrors glmModelCandidateBases sorted by length desc.
var glmModelCandidateBasesBySpecificity = sortStringsByLengthDesc([]string{
	"glm-5.3", "glm-5.2", "glm-5.1", "glm-5-turbo", "glm-5",
	"glm-4.7-flashx", "glm-4.7-flash", "glm-4.7", "glm-4.6",
	"glm-4.5-airx", "glm-4.5-air", "glm-4.5-flash", "glm-4.5-x", "glm-4.5",
})

// anthropicModelCandidateBases mirrors anthropicModelCandidateBases sorted by
// length desc.
var anthropicModelCandidateBasesBySpecificity = sortStringsByLengthDesc([]string{
	"best", "fable", "opus", "opus[1m]", "opusplan", "sonnet", "sonnet[1m]", "haiku",
	"claude-fable-5-1", "claude-fable-5", "claude-mythos-5", "claude-mythos-preview",
	"claude-fake-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6",
	"claude-opus-4-6-thinking", "antigravity-claude-opus-4-6-thinking",
	"antigravity/claude-opus-4-6-thinking", "google/antigravity-claude-opus-4-6-thinking",
	"google-antigravity/claude-opus-4-6-thinking", "google-antigravity:claude-opus-4-6-thinking",
	"claude-opus-4-6-antigravity", "claude-opus-4-5", "claude-sonnet-4-6",
	"claude-sonnet-4-6-antigravity", "antigravity-claude-sonnet-4-6",
	"antigravity/claude-sonnet-4-6", "google/antigravity-claude-sonnet-4-6",
	"google-antigravity/claude-opus-4-6-thinking",
	"google-antigravity:claude-sonnet-4-6-thinking",
	"claude-sonnet-4-6-thinking", "antigravity-claude-sonnet-4-6-thinking",
	"google-antigravity:claude-sonnet-4-6",
	"claude-sonnet-4-5", "claude-haiku-4-5",
})

func buildGlmModelCandidates(model string) []string {
	candidates := newCandidateSet()
	if withoutDate := stripModelDateSuffix(model); withoutDate != model {
		candidates.add(withoutDate)
	}
	for _, base := range glmModelCandidateBasesBySpecificity {
		if model == base || strings.HasPrefix(model, base+"-") {
			candidates.add(base)
		}
	}
	return candidates.list
}

func buildAnthropicModelCandidates(model string) []string {
	candidates := newCandidateSet()
	if withoutDate := stripModelDateSuffix(model); withoutDate != model {
		candidates.add(withoutDate)
	}
	for _, base := range anthropicModelCandidateBasesBySpecificity {
		if model == base || strings.HasPrefix(model, base+"-") {
			candidates.add(base)
		}
	}
	return candidates.list
}

func buildDeepSeekModelCandidates(model string) []string {
	candidates := newCandidateSet()
	if withoutDate := stripModelDateSuffix(model); withoutDate != model {
		candidates.add(withoutDate)
	}
	return candidates.list
}

func buildGeminiModelCandidates(model string) []string {
	candidates := newCandidateSet()
	withoutModelsPrefix := strings.TrimPrefix(model, "models/")
	if withoutModelsPrefix != model {
		candidates.add(withoutModelsPrefix)
	}
	for _, base := range geminiModelCandidateBasesBySpecificity {
		if model == base || withoutModelsPrefix == base {
			candidates.add(base)
		}
	}
	return candidates.list
}

func buildXAIModelCandidates(model string) []string {
	withoutDate := stripModelDateSuffix(model)
	if withoutDate == model {
		return nil
	}
	return []string{withoutDate}
}

// geminiModelCandidateBasesBySpecificity mirrors the gemini model list sorted
// by length desc (computed from the snapshot rows).
var geminiModelCandidateBasesBySpecificity = func() []string {
	bases := make([]string, 0, len(geminiModelPricingData))
	for i := range geminiModelPricingData {
		bases = append(bases, geminiModelPricingData[i].Model)
	}
	return sortStringsByLengthDesc(bases)
}()

func sortStringsByLengthDesc(values []string) []string {
	out := append([]string(nil), values...)
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// candidateSet preserves insertion order like the Node Set conversions.
type candidateSet struct {
	seen map[string]bool
	list []string
}

func newCandidateSet() *candidateSet {
	return &candidateSet{seen: map[string]bool{}}
}

func (s *candidateSet) add(value string) {
	if s.seen[value] {
		return
	}
	s.seen[value] = true
	s.list = append(s.list, value)
}

// openAIModelReleaseDates mirrors openAIModelReleaseDates.
var openAIModelReleaseDates = map[string]string{
	"gpt-5.6-sol": "2026-06-26", "gpt-5.6-terra": "2026-06-26", "gpt-5.6-luna": "2026-06-26",
	"gpt-5.5": "2026-04-23", "gpt-5.5-pro": "2026-04-23", "gpt-image-2": "2026-04-21",
	"gpt-5.4-mini": "2026-03-17", "gpt-5.4-nano": "2026-03-17", "gpt-5.4": "2026-03-05",
	"gpt-5.4-pro": "2026-03-05", "gpt-5.3-codex": "2026-02-24", "gpt-image-1.5": "2025-12-16",
	"gpt-5.2": "2025-12-11", "gpt-5.2-pro": "2025-12-11", "gpt-5.1": "2025-11-13",
	"gpt-5-pro": "2025-10-06", "gpt-image-1-mini": "2025-10-06", "gpt-5": "2025-08-07",
	"gpt-5-mini": "2025-08-07", "gpt-5-nano": "2025-08-07", "o3-pro": "2025-06-10",
	"gpt-image-1": "2025-04-23", "o3": "2025-04-16", "o4-mini": "2025-04-16",
	"gpt-4.1": "2025-04-14", "gpt-4.1-mini": "2025-04-14", "gpt-4.1-nano": "2025-04-14",
	"o1-pro": "2025-03-19", "o3-mini": "2025-01-31", "o1": "2024-12-17",
	"gpt-4o-mini": "2024-07-18", "gpt-4o": "2024-05-13", "gpt-4-turbo": "2024-04-09",
	"gpt-3.5-turbo-0125": "2024-02-01", "gpt-4-1106-preview": "2023-11-06",
	"gpt-3.5-turbo-1106": "2023-11-06", "gpt-4": "2023-03-14", "gpt-3.5-turbo": "2023-03-01",
	"gpt-4-0613": "2023-06-13",
}

// extractModelReleaseDate mirrors extractModelReleaseDate: a trailing
// YYYY-MM-DD inside the model id.
func extractModelReleaseDate(model string) string {
	match := regexp.MustCompile(`-(\d{4}-\d{2}-\d{2})$`).FindStringSubmatch(model)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// inferOpenAIModelApiProtocols mirrors inferOpenAIModelApiProtocols.
func inferOpenAIModelApiProtocols(item *rawModel) []string {
	if len(item.SupportedAPIProtocols) > 0 {
		return append([]string(nil), item.SupportedAPIProtocols...)
	}
	model := strings.TrimSpace(item.Model)
	mode := strings.TrimSpace(item.Mode)
	if mode == "image_generation" || strings.HasPrefix(model, "gpt-image") || strings.HasPrefix(model, "dall-e") {
		return []string{"images"}
	}
	if mode == "completion" {
		return []string{"completions"}
	}
	if strings.Contains(model, "codex") || strings.Contains(model, "-pro") {
		return []string{"responses"}
	}
	if mode == "responses" {
		return []string{"responses"}
	}
	if mode == "chat" || strings.HasPrefix(model, "gpt-") || strings.HasPrefix(model, "o") {
		return []string{"chat_completions", "responses"}
	}
	return []string{}
}

// openAIModelReleaseDate mirrors the openai driver getModelReleaseDate:
// release_date ?? suffix-extract ?? known map.
func openAIModelReleaseDate(item *rawModel) string {
	if item.ReleaseDate != "" {
		return item.ReleaseDate
	}
	if fromSuffix := extractModelReleaseDate(item.Model); fromSuffix != "" {
		return fromSuffix
	}
	return openAIModelReleaseDates[item.Model]
}

// suffixOrRawReleaseDate mirrors the non-openai drivers:
// release_date ?? suffix-extract.
func suffixOrRawReleaseDate(item *rawModel) string {
	if item.ReleaseDate != "" {
		return item.ReleaseDate
	}
	return extractModelReleaseDate(item.Model)
}

// inferAPIProtocols mirrors the per-driver inferModelApiProtocols defaults.
func (e *providerEntry) inferAPIProtocols(item *rawModel) []string {
	switch e.providerID {
	case "openai-compatible":
		return inferOpenAIModelApiProtocols(item)
	case "anthropic":
		if len(item.SupportedAPIProtocols) > 0 {
			return append([]string(nil), item.SupportedAPIProtocols...)
		}
		return []string{}
	case "deepseek", "glm":
		if len(item.SupportedAPIProtocols) > 0 {
			return append([]string(nil), item.SupportedAPIProtocols...)
		}
		return []string{"chat_completions"}
	case "gemini":
		if len(item.SupportedAPIProtocols) > 0 {
			return append([]string(nil), item.SupportedAPIProtocols...)
		}
		return []string{"generate_content", "stream_generate_content", "count_tokens"}
	default: // xai
		if len(item.SupportedAPIProtocols) > 0 {
			return append([]string(nil), item.SupportedAPIProtocols...)
		}
		return []string{}
	}
}

// modelReleaseDate mirrors the per-driver getModelReleaseDate.
func (e *providerEntry) modelReleaseDate(item *rawModel) string {
	if e.providerID == "openai-compatible" {
		return openAIModelReleaseDate(item)
	}
	return suffixOrRawReleaseDate(item)
}

// buildModelCandidates mirrors the per-driver buildModelCandidates.
func (e *providerEntry) buildModelCandidates(normalizedModel string) []string {
	switch e.providerID {
	case "openai-compatible":
		return buildOpenAIModelCandidates(normalizedModel)
	case "anthropic":
		return buildAnthropicModelCandidates(normalizedModel)
	case "deepseek":
		return buildDeepSeekModelCandidates(normalizedModel)
	case "glm":
		return buildGlmModelCandidates(normalizedModel)
	case "gemini":
		return buildGeminiModelCandidates(normalizedModel)
	default:
		return buildXAIModelCandidates(normalizedModel)
	}
}

// isUnavailableModel mirrors the per-driver isUnavailableModel (openai only).
func (e *providerEntry) isUnavailableModel(normalizedModel string) bool {
	if e.providerID != "openai-compatible" {
		return false
	}
	return isUnavailableOpenAIModel(normalizedModel)
}

// hasModelShutdown mirrors hasModelShutdown: shutdown_date <= asOfDate
// (ISO-date string comparison).
func hasModelShutdown(item *rawModel, asOfDate string) bool {
	return item.ShutdownDate != "" && item.ShutdownDate <= asOfDate
}

// currentUTCDate mirrors currentUtcDate.
func currentUTCDate() string {
	return time.Now().UTC().Format("2006-01-02")
}

// toProviderModelPricing mirrors toProviderModelPricing. providerCode is the
// normalized caller provider code (Node passes normalizedProviderCode, not
// the internal driver id), so downstream policy lookup via
// providerBillingPolicyForProvider sees the same token as Node does.
func toProviderModelPricing(item *rawModel, entry *providerEntry, providerCode string) *Pricing {
	supportedServiceTiers := append([]string(nil), item.SupportedServiceTiers...)
	catalogVisible := item.CatalogVisible == nil || *item.CatalogVisible
	return &Pricing{
		ProviderCode: providerCode,
		Model:        item.Model,
		Mode:         item.Mode,
		CatalogOrder: item.CatalogOrder,
		ReleaseDate:  entry.modelReleaseDate(item),
		ShutdownDate: item.ShutdownDate,
		PriceSet: PriceSet{
			InputUsdPer1M:               perMillion(item.InputCostPerToken),
			OutputUsdPer1M:              perMillion(item.OutputCostPerToken),
			CachedInputUsdPer1M:         perMillion(item.CacheReadInputTokenCost),
			CacheWriteUsdPer1M:          perMillion(item.CacheCreationInputTokenCost),
			CacheWrite1hUsdPer1M:        perMillion(item.CacheCreationInputTokenCostAbove1hr),
			CacheStorageUsdPer1MPerHour: perMillion(item.CacheStorageInputTokenCostPerHour),
			ImageInputUsdPer1M:          perMillion(item.InputCostPerImageToken),
			ImageOutputUsdPer1M:         perMillion(item.OutputCostPerImageToken),
			AudioInputUsdPer1M:          perMillion(item.InputCostPerAudioToken),
			AudioOutputUsdPer1M:         perMillion(item.OutputCostPerAudioToken),
			OutputUsdPerImage:           normalizePrice(item.OutputCostPerImage),
		},
		CachedImageInputUsdPer1M: perMillion(item.CacheReadInputImageTokenCost),
		ServiceTierPrices:        rawServiceTierPrices(item),

		SupportedAPIProtocols:     entry.inferAPIProtocols(item),
		InputModalities:           append([]string(nil), item.InputModalities...),
		OutputModalities:          append([]string(nil), item.OutputModalities...),
		SupportedTools:            append([]string(nil), item.SupportedTools...),
		SupportedServiceTiers:     supportedServiceTiers,
		SupportedReasoningEfforts: append([]string(nil), item.SupportedReasoningEfforts...),
		DefaultReasoningEffort:    item.DefaultReasoningEffort,

		CodexSupportedReasoningLevels: append([]string(nil), item.CodexSupportedReasoningLevels...),
		CodexDefaultReasoningLevel:    item.CodexDefaultReasoningLevel,
		CodexMultiAgentVersion:        item.CodexMultiAgentVersion,

		ContextWindowTokens: item.ContextWindowTokens,
		MaxInputTokens:      item.MaxInputTokens,
		MaxOutputTokens:     item.MaxOutputTokens,
		MaxTokens:           item.MaxTokens,

		LongContextInputTokenThreshold:          item.LongContextInputTokenThreshold,
		LongContextInputTokenThresholdInclusive: item.LongContextInputTokenThresholdInclusive,
		LongContextInputCostMultiplier:          item.LongContextInputCostMultiplier,
		LongContextOutputCostMultiplier:         item.LongContextOutputCostMultiplier,

		SupportsPromptCaching: item.SupportsPromptCaching,
		SupportsServiceTier:   len(supportedServiceTiers) > 0,
		CatalogVisible:        catalogVisible,

		SourcePricingCurrency:   item.SourcePricingCurrency,
		SourceExchangeRateToUsd: item.SourceExchangeRateToUsd,
		SourceExchangeRateDate:  item.SourceExchangeRateDate,
		SourcePricingNote:       item.SourcePricingNote,
		Source:                  entry.pricingSource,
	}
}

// normalizePrice mirrors normalizePrice.
func normalizePrice(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	return v
}

// rawServiceTierPrices mirrors rawServiceTierPrices: the priority/flex/batch
// tier sets derived from the raw per-field tier columns; the audio output
// price intentionally reuses the standard output_cost_per_audio_token.
func rawServiceTierPrices(item *rawModel) ServiceTierPrices {
	tiers := ServiceTierPrices{}
	add := func(tier string, set PriceSet) {
		if priceSetDefined(set) {
			tiers[tier] = set
		}
	}
	add("priority", PriceSet{
		InputUsdPer1M:               perMillion(item.InputCostPerTokenPriority),
		OutputUsdPer1M:              perMillion(item.OutputCostPerTokenPriority),
		CachedInputUsdPer1M:         perMillion(item.CacheReadInputTokenCostPriority),
		CacheWriteUsdPer1M:          perMillion(item.CacheCreationInputTokenCostPriority),
		CacheWrite1hUsdPer1M:        perMillion(item.CacheCreationInputTokenCostAbove1hrPriority),
		CacheStorageUsdPer1MPerHour: perMillion(item.CacheStorageInputTokenCostPerHourPriority),
		AudioInputUsdPer1M:          perMillion(item.InputCostPerAudioTokenPriority),
		AudioOutputUsdPer1M:         perMillion(item.OutputCostPerAudioToken),
	})
	add("flex", PriceSet{
		InputUsdPer1M:               perMillion(item.InputCostPerTokenFlex),
		OutputUsdPer1M:              perMillion(item.OutputCostPerTokenFlex),
		CachedInputUsdPer1M:         perMillion(item.CacheReadInputTokenCostFlex),
		CacheWriteUsdPer1M:          perMillion(item.CacheCreationInputTokenCostFlex),
		CacheWrite1hUsdPer1M:        perMillion(item.CacheCreationInputTokenCostAbove1hrFlex),
		CacheStorageUsdPer1MPerHour: perMillion(item.CacheStorageInputTokenCostPerHourFlex),
		AudioInputUsdPer1M:          perMillion(item.InputCostPerAudioTokenFlex),
		AudioOutputUsdPer1M:         perMillion(item.OutputCostPerAudioToken),
	})
	add("batch", PriceSet{
		InputUsdPer1M:  perMillion(item.InputCostPerTokenBatch),
		OutputUsdPer1M: perMillion(item.OutputCostPerTokenBatch),
	})
	if len(tiers) == 0 {
		return nil
	}
	return tiers
}

// priceSetDefined reports whether at least one price is set.
func priceSetDefined(set PriceSet) bool {
	return hasAnyRate(set)
}

// compareProviderModels mirrors compareProviderModels.
func compareProviderModels(left, right *Pricing) int {
	if lo, ro, ok := catalogOrderPair(left, right); ok && lo != ro {
		if lo < ro {
			return -1
		}
		return 1
	}
	if left.ReleaseDate != "" && right.ReleaseDate != "" && left.ReleaseDate != right.ReleaseDate {
		// right.localeCompare(left): release date descending.
		if right.ReleaseDate < left.ReleaseDate {
			return -1
		}
		return 1
	}
	if left.ReleaseDate != "" && right.ReleaseDate == "" {
		return -1
	}
	if left.ReleaseDate == "" && right.ReleaseDate != "" {
		return 1
	}
	return strings.Compare(left.Model, right.Model)
}

// catalogOrderPair extracts the shared-catalog-order comparison per
// compareSharedCatalogOrder: only defined pairs participate.
func catalogOrderPair(left, right *Pricing) (lo, ro int, ok bool) {
	if left.CatalogOrder != nil && right.CatalogOrder != nil {
		return *left.CatalogOrder, *right.CatalogOrder, true
	}
	return 0, 0, false
}
