// write_routes.go ports the Node providers write family
// (providers.routes.ts:184-606): POST/PATCH/DELETE /{code}/models and
// PUT /{code}/default-health-check-model, including the zod request
// contracts (strict objects, trimmed strings, nullable numbers), the
// session-level auth posture (per-route admin forks only), the optimistic
// concurrency (expectedUpdatedAt -> 409), the configuration-template
// inheritance, the custom-model pricing/capability validation, the built-in
// model completeness validation and the AI-account binding guard.
package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// customModelProtocolEnum mirrors the customModelSchema protocol enum.
var customModelProtocolEnum = map[string]bool{
	"chat_completions": true, "responses": true, "messages": true,
	"message_token_counting": true, "generate_content": true,
	"stream_generate_content": true, "count_tokens": true,
	"embed_content": true, "interactions": true,
	"completions": true, "images": true,
}

// providerModelValidationFields mirrors providerModelValidationFields.
var providerModelValidationFields = map[string]bool{
	"mode": true, "supportedApiProtocols": true, "supportedServiceTiers": true,
	"supportedReasoningEfforts": true, "defaultReasoningEffort": true,
	"inputUsdPer1M": true, "outputUsdPer1M": true, "cachedInputUsdPer1M": true,
	"cacheWriteUsdPer1M": true, "cacheWrite1hUsdPer1M": true,
	"cacheStorageUsdPer1MPerHour": true, "serviceTierPrices": true,
	"imageInputUsdPer1M": true, "imageOutputUsdPer1M": true,
	"audioInputUsdPer1M": true, "audioOutputUsdPer1M": true,
	"outputUsdPerImage": true,
}

// customModelParseOptions selects the schema variant: POST (full object),
// custom PATCH (no model/scope/template, expectedUpdatedAt required) and
// built-in PATCH (no notes trio, catalogVisible, active|disabled status).
type customModelParseOptions struct {
	requireModel             bool
	allowTemplateID          bool
	allowScope               bool
	allowNotes               bool
	allowCatalogVisible      bool
	requireExpectedUpdatedAt bool
	statusValues             []string
}

// customModelParsedInput is the parsed request body: the typed values for
// the keys present in the JSON object plus the raw tier keys the route
// validators consume.
type customModelParsedInput struct {
	present                 map[string]bool
	model                   string
	scope                   string
	status                  string
	configurationTemplateID string
	expectedUpdatedAt       string
	catalogVisible          *bool
	values                  customProviderModelUpsertInput
	serviceTierPriceKeysRaw []string
}

// decodeModelBody reads the JSON object body; non-object bodies decode to an
// empty record (Node requestRecord).
func decodeModelBody(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		kernel.WriteBadRequest(w, "请求体必须是 JSON 对象")
		return nil, false
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return map[string]json.RawMessage{}, true
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		kernel.WriteBadRequest(w, "请求体必须是 JSON 对象")
		return nil, false
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return map[string]json.RawMessage{}, true
	}
	result := make(map[string]json.RawMessage, len(object))
	for key, value := range object {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		result[key] = encoded
	}
	return result, true
}

// jsonOrderedKeys returns the body keys sorted (deterministic; Node uses
// insertion order, which carries no behavioral weight beyond SQL SET order).
func jsonOrderedKeys(body map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	for index := 1; index < len(keys); index++ {
		for position := index; position > 0 && keys[position] < keys[position-1]; position-- {
			keys[position], keys[position-1] = keys[position-1], keys[position]
		}
	}
	return keys
}

// parseCustomModelBody mirrors the zod custom model family: strict objects,
// trimmed strings, nullable numbers, enum arrays and the canonical
// expectedUpdatedAt.
func parseCustomModelBody(body map[string]json.RawMessage, options customModelParseOptions) (*customModelParsedInput, bool) {
	parsed := &customModelParsedInput{present: map[string]bool{}}
	statusAllowed := map[string]bool{}
	for _, value := range options.statusValues {
		statusAllowed[value] = true
	}
	fail := func() (*customModelParsedInput, bool) {
		return nil, false
	}
	stringValue := func(raw json.RawMessage) (string, bool) {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", false
		}
		return text, true
	}
	trimmedString := func(raw json.RawMessage) (string, bool) {
		text, ok := stringValue(raw)
		if !ok {
			return "", false
		}
		return strings.TrimSpace(text), true
	}
	nullableTrimmedString := func(raw json.RawMessage) (*string, bool) {
		var text *string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, false
		}
		if text == nil {
			return nil, true
		}
		trimmed := strings.TrimSpace(*text)
		return &trimmed, true
	}
	nullableDate := func(raw json.RawMessage) (*string, bool) {
		value, ok := nullableTrimmedString(raw)
		if !ok {
			return nil, false
		}
		if value == nil {
			return nil, true
		}
		if !customModelDatePattern.MatchString(*value) {
			return nil, false
		}
		return value, true
	}
	nullableInteger := func(raw json.RawMessage) (*int64, bool) {
		var number *float64
		if err := json.Unmarshal(raw, &number); err != nil {
			return nil, false
		}
		if number == nil {
			return nil, true
		}
		if *number != float64(int64(*number)) || *number < 0 {
			return nil, false
		}
		integer := int64(*number)
		return &integer, true
	}
	nullableNumber := func(raw json.RawMessage) (*float64, bool) {
		var number *float64
		if err := json.Unmarshal(raw, &number); err != nil {
			return nil, false
		}
		if number == nil {
			return nil, true
		}
		if *number < 0 {
			return nil, false
		}
		return number, true
	}
	stringArray := func(raw json.RawMessage, allowed map[string]bool) ([]string, bool) {
		var items []any
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, false
		}
		output := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok || !allowed[text] {
				return nil, false
			}
			output = append(output, text)
		}
		return output, true
	}
	tokenArray := func(raw json.RawMessage) ([]string, bool) {
		var items []any
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, false
		}
		if len(items) > 16 {
			return nil, false
		}
		output := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok || !customModelCapabilityTokenPattern.MatchString(text) {
				return nil, false
			}
			output = append(output, text)
		}
		return output, true
	}

	for _, key := range jsonOrderedKeys(body) {
		raw := body[key]
		switch key {
		case "configurationTemplateId":
			if !options.allowTemplateID {
				return fail()
			}
			value, ok := trimmedString(raw)
			if !ok || value == "" {
				return fail()
			}
			parsed.configurationTemplateID = value
		case "scope":
			if !options.allowScope {
				return fail()
			}
			value, ok := trimmedString(raw)
			if !ok || (value != "personal" && value != "global") {
				return fail()
			}
			parsed.scope = value
		case "model":
			if !options.requireModel {
				return fail()
			}
			value, ok := trimmedString(raw)
			if !ok || value == "" {
				return fail()
			}
			parsed.model = value
		case "status":
			value, ok := trimmedString(raw)
			if !ok || !statusAllowed[value] {
				return fail()
			}
			parsed.present[key] = true
			parsed.status = value
		case "catalogVisible":
			if !options.allowCatalogVisible {
				return fail()
			}
			var value bool
			if err := json.Unmarshal(raw, &value); err != nil {
				return fail()
			}
			parsed.present[key] = true
			parsed.catalogVisible = &value
		case "mode":
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil {
				return fail()
			}
			if value != nil && *value != "text" && *value != "image" {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.Mode = value
		case "supportedApiProtocols":
			value, ok := stringArray(raw, customModelProtocolEnum)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.SupportedAPIProtocols = value
		case "supportedServiceTiers":
			value, ok := tokenArray(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.SupportedServiceTiers = value
		case "supportedReasoningEfforts":
			value, ok := tokenArray(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.SupportedReasoningEfforts = value
		case "defaultReasoningEffort":
			value, ok := nullableTrimmedString(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.DefaultReasoningEffort = value
		case "releaseDate":
			value, ok := nullableDate(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.ReleaseDate = value
		case "shutdownDate":
			value, ok := nullableDate(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.ShutdownDate = value
		case "contextWindowTokens":
			value, ok := nullableInteger(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.ContextWindowTokens = value
		case "maxInputTokens":
			value, ok := nullableInteger(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.MaxInputTokens = value
		case "maxOutputTokens":
			value, ok := nullableInteger(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.MaxOutputTokens = value
		case "inputUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.InputUsdPer1M = value
		case "outputUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.OutputUsdPer1M = value
		case "cachedInputUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.CachedInputUsdPer1M = value
		case "cacheWriteUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.CacheWriteUsdPer1M = value
		case "cacheWrite1hUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.CacheWrite1hUsdPer1M = value
		case "cacheStorageUsdPer1MPerHour":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.CacheStorageUsdPer1MPerHour = value
		case "serviceTierPrices":
			prices, tierKeys, ok := parseServiceTierPrices(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.serviceTierPriceKeysRaw = tierKeys
			parsed.values.ServiceTierPrices = prices
		case "imageInputUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.ImageInputUsdPer1M = value
		case "imageOutputUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.ImageOutputUsdPer1M = value
		case "audioInputUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.AudioInputUsdPer1M = value
		case "audioOutputUsdPer1M":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.AudioOutputUsdPer1M = value
		case "outputUsdPerImage":
			value, ok := nullableNumber(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			parsed.values.OutputUsdPerImage = value
		case "pricingNotes", "capabilityNotes", "notes":
			if !options.allowNotes {
				return fail()
			}
			value, ok := nullableTrimmedString(raw)
			if !ok {
				return fail()
			}
			parsed.present[key] = true
			switch key {
			case "pricingNotes":
				parsed.values.PricingNotes = value
			case "capabilityNotes":
				parsed.values.CapabilityNotes = value
			case "notes":
				parsed.values.Notes = value
			}
		case "expectedUpdatedAt":
			if !options.requireExpectedUpdatedAt {
				return fail()
			}
			value, ok := trimmedString(raw)
			if !ok {
				return fail()
			}
			canonical, ok := canonicalRfc3339Instant(value)
			if !ok {
				return fail()
			}
			parsed.expectedUpdatedAt = canonical
		default:
			return fail()
		}
	}
	if options.requireExpectedUpdatedAt {
		if parsed.expectedUpdatedAt == "" {
			return fail()
		}
		if !parsed.hasContentBeyondExpectedUpdatedAt() {
			return fail()
		}
	}
	return parsed, true
}

func (p *customModelParsedInput) hasContentBeyondExpectedUpdatedAt() bool {
	for key := range p.present {
		if key != "expectedUpdatedAt" {
			return true
		}
	}
	if p.status != "" || p.model != "" || p.scope != "" || p.configurationTemplateID != "" || p.catalogVisible != nil {
		return true
	}
	return false
}

// parseServiceTierPrices mirrors the serviceTierPrices record schema: keys
// trimmed to 1..64 chars, strict ModelPriceSet values with nullable
// non-negative numbers. It also returns the raw tier keys carrying at least
// one defined price (the route validator's serviceTierPriceKeys).
func parseServiceTierPrices(raw json.RawMessage) (map[string]ModelPriceSet, []string, bool) {
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, nil, false
	}
	prices := map[string]ModelPriceSet{}
	definedTiers := []string{}
	for rawTier, rawSet := range decoded {
		tier := strings.TrimSpace(rawTier)
		if tier == "" || len(tier) > 64 {
			return nil, nil, false
		}
		set, ok := rawSet.(map[string]any)
		if !ok {
			return nil, nil, false
		}
		for key := range set {
			switch key {
			case "inputUsdPer1M", "outputUsdPer1M", "cachedInputUsdPer1M", "cacheWriteUsdPer1M",
				"cacheWrite1hUsdPer1M", "cacheStorageUsdPer1MPerHour", "imageInputUsdPer1M",
				"imageOutputUsdPer1M", "audioInputUsdPer1M", "audioOutputUsdPer1M", "outputUsdPerImage":
			default:
				return nil, nil, false
			}
		}
		var parsed ModelPriceSet
		hasValue := false
		strict := func(key string, target **float64) bool {
			value, present := set[key]
			if !present || value == nil {
				return true
			}
			number, ok := value.(float64)
			if !ok || number < 0 {
				return false
			}
			copied := number
			*target = &copied
			hasValue = true
			return true
		}
		ok = strict("inputUsdPer1M", &parsed.InputUsdPer1M) &&
			strict("outputUsdPer1M", &parsed.OutputUsdPer1M) &&
			strict("cachedInputUsdPer1M", &parsed.CachedInputUsdPer1M) &&
			strict("cacheWriteUsdPer1M", &parsed.CacheWriteUsdPer1M) &&
			strict("cacheWrite1hUsdPer1M", &parsed.CacheWrite1hUsdPer1M) &&
			strict("cacheStorageUsdPer1MPerHour", &parsed.CacheStorageUsdPer1MPerHour) &&
			strict("imageInputUsdPer1M", &parsed.ImageInputUsdPer1M) &&
			strict("imageOutputUsdPer1M", &parsed.ImageOutputUsdPer1M) &&
			strict("audioInputUsdPer1M", &parsed.AudioInputUsdPer1M) &&
			strict("audioOutputUsdPer1M", &parsed.AudioOutputUsdPer1M) &&
			strict("outputUsdPerImage", &parsed.OutputUsdPerImage)
		if !ok {
			return nil, nil, false
		}
		prices[tier] = parsed
		if hasValue {
			definedTiers = append(definedTiers, tier)
		}
	}
	return prices, definedTiers, true
}

// canonicalRfc3339Instant mirrors rfc3339InstantSchema: Z or numeric offset
// required, canonical UTC millis output.
func canonicalRfc3339Instant(value string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", false
	}
	// Node rejects timezone-less values via its regex; time.Parse shares that
	// behavior for RFC3339 input.
	return parsed.Truncate(time.Millisecond).UTC().Format("2006-01-02T15:04:05.000Z07:00"), true
}

// --- handlers ------------------------------------------------------------

// createModel serves POST /{code}/models.
func (d *Deps) createModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	auth := authsys.AuthContextFrom(r)
	provider, err := d.Store.FindDefinition(ctx, r.PathValue("code"))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if provider == nil {
		kernel.WriteNotFound(w, "供应商不存在")
		return
	}
	body, ok := decodeModelBody(w, r)
	if !ok {
		return
	}
	parsed, ok := parseCustomModelBody(body, customModelParseOptions{
		requireModel: true, allowTemplateID: true, allowScope: true, allowNotes: true,
		statusValues: []string{"draft", "active", "disabled"},
	})
	if !ok || parsed.model == "" {
		kernel.WriteBadRequest(w, "自定义模型参数无效")
		return
	}
	scope := parsed.scope
	if scope == "" {
		scope = "personal"
	}
	if scope == "global" && !isAdminRole(auth.Role) {
		kernel.WriteError(w, http.StatusForbidden, "只有管理员可以创建全局模型")
		return
	}
	owner := ""
	if scope != "global" {
		owner = requestSystemAccountID(r)
		if owner == "" {
			kernel.WriteBadRequest(w, "请选择模型归属的系统账户")
			return
		}
	}
	effective := customProviderModelUpsertInput{Model: parsed.model}
	if parsed.configurationTemplateID != "" {
		catalog, err := d.Store.listProviderModelCatalog(ctx, provider.Code, owner, true, true)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		var template *ModelCatalogItem
		for index := range catalog {
			if catalog[index].ID == parsed.configurationTemplateID && catalog[index].Status == "active" {
				template = &catalog[index]
				break
			}
		}
		if template == nil {
			kernel.WriteBadRequest(w, "配置模板不可用")
			return
		}
		effective = customModelInputFromConfigurationTemplate(template)
	}
	applyParsedCustomModelFields(&effective, parsed)
	effective.Model = parsed.model
	effective.DefaultReasoningEffort = nil
	if message := validateCustomModelPricing(provider.Code, effective); message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	effective.ProviderCode = provider.Code
	effective.Scope = scope
	effective.SystemAccountID = owner
	effective.Status = parsed.status
	if effective.Status == "" {
		effective.Status = "active"
	}
	effective.ActorSystemAccountID = auth.SystemAccountID
	saved, err := d.Store.upsertCustomProviderModel(ctx, effective)
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	item := customCatalogItemFromRecord(saved)
	writeCreatedJSON(w, map[string]any{"data": item})
}

// writeCreatedJSON renders the 201 {data} envelope (Node
// res.status(201).json(ok(saved))).
func writeCreatedJSON(w http.ResponseWriter, payload map[string]any) {
	kernel.WriteJSON(w, http.StatusCreated, payload)
}

// applyParsedCustomModelFields spreads the submitted keys over the inherited
// defaults ({...inherited, ...submitted}).
func applyParsedCustomModelFields(target *customProviderModelUpsertInput, parsed *customModelParsedInput) {
	for key := range parsed.present {
		switch key {
		case "mode":
			target.Mode = parsed.values.Mode
		case "supportedApiProtocols":
			target.SupportedAPIProtocols = parsed.values.SupportedAPIProtocols
		case "supportedServiceTiers":
			target.SupportedServiceTiers = parsed.values.SupportedServiceTiers
		case "supportedReasoningEfforts":
			target.SupportedReasoningEfforts = parsed.values.SupportedReasoningEfforts
		case "defaultReasoningEffort":
			target.DefaultReasoningEffort = parsed.values.DefaultReasoningEffort
		case "releaseDate":
			target.ReleaseDate = parsed.values.ReleaseDate
		case "shutdownDate":
			target.ShutdownDate = parsed.values.ShutdownDate
		case "contextWindowTokens":
			target.ContextWindowTokens = parsed.values.ContextWindowTokens
		case "maxInputTokens":
			target.MaxInputTokens = parsed.values.MaxInputTokens
		case "maxOutputTokens":
			target.MaxOutputTokens = parsed.values.MaxOutputTokens
		case "inputUsdPer1M":
			target.InputUsdPer1M = parsed.values.InputUsdPer1M
		case "outputUsdPer1M":
			target.OutputUsdPer1M = parsed.values.OutputUsdPer1M
		case "cachedInputUsdPer1M":
			target.CachedInputUsdPer1M = parsed.values.CachedInputUsdPer1M
		case "cacheWriteUsdPer1M":
			target.CacheWriteUsdPer1M = parsed.values.CacheWriteUsdPer1M
		case "cacheWrite1hUsdPer1M":
			target.CacheWrite1hUsdPer1M = parsed.values.CacheWrite1hUsdPer1M
		case "cacheStorageUsdPer1MPerHour":
			target.CacheStorageUsdPer1MPerHour = parsed.values.CacheStorageUsdPer1MPerHour
		case "serviceTierPrices":
			target.ServiceTierPrices = parsed.values.ServiceTierPrices
		case "imageInputUsdPer1M":
			target.ImageInputUsdPer1M = parsed.values.ImageInputUsdPer1M
		case "imageOutputUsdPer1M":
			target.ImageOutputUsdPer1M = parsed.values.ImageOutputUsdPer1M
		case "audioInputUsdPer1M":
			target.AudioInputUsdPer1M = parsed.values.AudioInputUsdPer1M
		case "audioOutputUsdPer1M":
			target.AudioOutputUsdPer1M = parsed.values.AudioOutputUsdPer1M
		case "outputUsdPerImage":
			target.OutputUsdPerImage = parsed.values.OutputUsdPerImage
		case "pricingNotes":
			target.PricingNotes = parsed.values.PricingNotes
		case "capabilityNotes":
			target.CapabilityNotes = parsed.values.CapabilityNotes
		case "notes":
			target.Notes = parsed.values.Notes
		case "status":
			target.Status = parsed.status
		}
	}
}

// customModelInputFromConfigurationTemplate ports the same-named helper.
func customModelInputFromConfigurationTemplate(template *ModelCatalogItem) customProviderModelUpsertInput {
	mode := "text"
	if template.Mode != nil && *template.Mode == "image" {
		mode = "image"
	}
	for _, protocol := range template.SupportedAPIProtocols {
		if protocol == "images" {
			mode = "image"
		}
	}
	protocols := []string{}
	for _, protocol := range template.SupportedAPIProtocols {
		if protocol == "audio" || protocol == "realtime" {
			continue
		}
		protocols = append(protocols, protocol)
	}
	return customProviderModelUpsertInput{
		Mode:                        &mode,
		SupportedAPIProtocols:       protocols,
		SupportedServiceTiers:       copyStringSlice(template.SupportedServiceTiers),
		SupportedReasoningEfforts:   copyStringSlice(template.SupportedReasoningEfforts),
		DefaultReasoningEffort:      nil,
		ReleaseDate:                 template.ReleaseDate,
		ShutdownDate:                template.ShutdownDate,
		ContextWindowTokens:         template.ContextWindowTokens,
		MaxInputTokens:              template.MaxInputTokens,
		MaxOutputTokens:             template.MaxOutputTokens,
		InputUsdPer1M:               template.InputUsdPer1M,
		OutputUsdPer1M:              template.OutputUsdPer1M,
		CachedInputUsdPer1M:         template.CachedInputUsdPer1M,
		CacheWriteUsdPer1M:          template.CacheWriteUsdPer1M,
		CacheWrite1hUsdPer1M:        template.CacheWrite1hUsdPer1M,
		CacheStorageUsdPer1MPerHour: template.CacheStorageUsdPer1MPerHour,
		ServiceTierPrices:           cloneServiceTierPrices(template.ServiceTierPrices),
		ImageInputUsdPer1M:          template.ImageInputUsdPer1M,
		ImageOutputUsdPer1M:         template.ImageOutputUsdPer1M,
		AudioInputUsdPer1M:          template.AudioInputUsdPer1M,
		AudioOutputUsdPer1M:         template.AudioOutputUsdPer1M,
		OutputUsdPerImage:           template.OutputUsdPerImage,
		PricingNotes:                template.PricingNotes,
		CapabilityNotes:             template.CapabilityNotes,
		Notes:                       template.Notes,
	}
}

func cloneServiceTierPrices(prices map[string]ModelPriceSet) map[string]ModelPriceSet {
	output := map[string]ModelPriceSet{}
	for tier, set := range prices {
		output[tier] = set
	}
	return output
}

// validateCustomModelPricing ports validateCustomModelPricing; "" = valid.
func validateCustomModelPricing(providerCode string, input customProviderModelUpsertInput) string {
	status := input.Status
	if status == "" {
		status = "active"
	}
	hasDirectPrice := customInputHasDirectPrice(input)
	if message := validateCustomModelCapabilities(providerCode, input); message != "" {
		return message
	}
	if status == "active" && !hasDirectPrice {
		return "启用的自定义模型必须配置完整当前价格"
	}
	return ""
}

// validateCustomModelCapabilities ports validateCustomModelCapabilities.
func validateCustomModelCapabilities(providerCode string, input customProviderModelUpsertInput) string {
	serviceTiers := input.SupportedServiceTiers
	reasoningEfforts := input.SupportedReasoningEfforts
	defaultReasoningEffort := ""
	if input.DefaultReasoningEffort != nil {
		defaultReasoningEffort = *input.DefaultReasoningEffort
	}
	isTextModel := input.Mode == nil || *input.Mode == "text"
	if message := validateServiceTierPriceKeys(input.Mode, serviceTiers, input.ServiceTierPrices); message != "" {
		return message
	}
	if !isTextModel && (len(serviceTiers) > 0 || len(reasoningEfforts) > 0 || defaultReasoningEffort != "") {
		return "只有文本自定义模型支持服务等级和思考能力配置"
	}
	if normalizeProviderToken(providerCode) == "gpt" {
		gptServiceTiers := map[string]bool{"priority": true, "flex": true}
		gptReasoningEfforts := map[string]bool{"none": true, "minimal": true, "low": true, "medium": true, "high": true, "xhigh": true, "max": true}
		if len(serviceTiers) > 2 || len(reasoningEfforts) > 7 {
			return "自定义模型参数无效"
		}
		for _, tier := range serviceTiers {
			if !gptServiceTiers[tier] {
				return "自定义模型参数无效"
			}
		}
		for _, effort := range reasoningEfforts {
			if !gptReasoningEfforts[effort] {
				return "自定义模型参数无效"
			}
		}
	}
	if defaultReasoningEffort != "" {
		known := false
		for _, effort := range reasoningEfforts {
			if effort == defaultReasoningEffort {
				known = true
				break
			}
		}
		if !known {
			return "默认思考级别必须属于支持的思考级别"
		}
	}
	return ""
}

// validateServiceTierPriceKeys ports validateServiceTierPriceKeys.
func validateServiceTierPriceKeys(mode *string, supportedServiceTiers []string, serviceTierPrices map[string]ModelPriceSet) string {
	keys := serviceTierPriceKeysWithPrices(serviceTierPrices)
	if len(keys) == 0 {
		return ""
	}
	if mode != nil && (*mode == "image" || *mode == "audio") {
		return "只有文本自定义模型支持服务档位价格"
	}
	known := map[string]bool{}
	for _, tier := range supportedServiceTiers {
		known[tier] = true
	}
	for _, tier := range keys {
		if !known[tier] {
			return "服务档位价格必须属于模型支持的服务等级"
		}
	}
	return ""
}

// serviceTierPriceKeysWithPrices ports serviceTierPriceKeys: tiers carrying
// at least one finite non-negative price.
func serviceTierPriceKeysWithPrices(prices map[string]ModelPriceSet) []string {
	output := []string{}
	for tier, set := range prices {
		if modelPriceSetDefined(set) {
			output = append(output, strings.TrimSpace(tier))
		}
	}
	for index := 1; index < len(output); index++ {
		for position := index; position > 0 && output[position] < output[position-1]; position-- {
			output[position], output[position-1] = output[position-1], output[position]
		}
	}
	filtered := output[:0]
	for _, tier := range output {
		if tier != "" {
			filtered = append(filtered, tier)
		}
	}
	return filtered
}

// customInputHasDirectPrice ports customInputHasDirectPrice.
func customInputHasDirectPrice(input customProviderModelUpsertInput) bool {
	mode := "text"
	if input.Mode != nil {
		mode = *input.Mode
	}
	if mode == "image" {
		return input.ImageInputUsdPer1M != nil || input.ImageOutputUsdPer1M != nil || input.OutputUsdPerImage != nil
	}
	if mode == "audio" {
		return input.AudioInputUsdPer1M != nil || input.AudioOutputUsdPer1M != nil
	}
	return input.InputUsdPer1M != nil || input.OutputUsdPer1M != nil || input.CachedInputUsdPer1M != nil ||
		input.CacheWriteUsdPer1M != nil || input.CacheWrite1hUsdPer1M != nil ||
		input.CacheStorageUsdPer1MPerHour != nil ||
		len(serviceTierPriceKeysWithPrices(input.ServiceTierPrices)) > 0
}

// validateBuiltInModelCompleteness ports validateBuiltInModelCompleteness.
func validateBuiltInModelCompleteness(next *ModelCatalogItem) string {
	if next.ReleaseDate == nil {
		return "内置模型必须配置发布时间"
	}
	if len(next.SupportedAPIProtocols) == 0 {
		return "内置模型必须配置接口协议"
	}
	if !builtInItemHasDirectPrice(next) {
		return "内置模型必须配置当前价格"
	}
	mode := ""
	if next.Mode != nil {
		mode = *next.Mode
	}
	isTextModel := !strings.HasPrefix(mode, "image") && !strings.HasPrefix(mode, "audio") && mode != "embedding"
	if isTextModel && (next.ContextWindowTokens == nil || *next.ContextWindowTokens == 0) &&
		(next.MaxInputTokens == nil || *next.MaxInputTokens == 0) {
		return "内置文本模型必须配置上下文或最大输入容量"
	}
	return ""
}

func builtInItemHasDirectPrice(item *ModelCatalogItem) bool {
	return hasDirectPrice(*item)
}

// canMutateCustomModel ports canMutateCustomModel.
func canMutateCustomModel(scope, ownerSystemAccountID string, auth *authsys.AuthContext) bool {
	if scope == "global" {
		return isAdminRole(auth.Role)
	}
	return ownerSystemAccountID == auth.SystemAccountID || isAdminRole(auth.Role)
}

// customModelBoundToAccountMessage ports customModelBoundToAccountMessage.
func customModelBoundToAccountMessage(summary *customProviderModelBindingSummary) string {
	details := []string{}
	if summary.SupportedModelAccountCount > 0 {
		details = append(details, itoaInt64(summary.SupportedModelAccountCount)+" 个账户支持模型")
	}
	if summary.MappingSourceAccountCount > 0 {
		details = append(details, itoaInt64(summary.MappingSourceAccountCount)+" 个账户映射下游模型")
	}
	if summary.MappingUpstreamAccountCount > 0 {
		details = append(details, itoaInt64(summary.MappingUpstreamAccountCount)+" 个账户映射上游模型")
	}
	if len(details) > 0 {
		return "模型已绑定 AI 账户，不能删除；请先从" + strings.Join(details, "、") + "中移除后再删除"
	}
	return "模型已绑定 AI 账户，不能删除；请先解除账户绑定后再删除"
}

// isProviderModelUsableForAccountTest ports isProviderModelUsableForAccountTest.
func isProviderModelUsableForAccountTest(item *ModelCatalogItem) bool {
	if item.Mode != nil && (*item.Mode == "image" || *item.Mode == "audio") {
		return false
	}
	if len(item.SupportedAPIProtocols) == 0 {
		return true
	}
	textProtocols := map[string]bool{"chat_completions": true, "responses": true, "messages": true,
		"generate_content": true, "stream_generate_content": true}
	for _, protocol := range item.SupportedAPIProtocols {
		if textProtocols[protocol] {
			return true
		}
	}
	return false
}

// providerModelIsUsableAsDefault ports providerModelIsUsableAsDefault.
func providerModelIsUsableAsDefault(status string, catalogVisible *bool, shutdownDate *string, mode *string, protocols []string) bool {
	if status != "active" || (catalogVisible != nil && !*catalogVisible) {
		return false
	}
	if shutdownDate != nil {
		trimmed := strings.TrimSpace(*shutdownDate)
		if trimmed != "" && trimmed <= todayUTCString() {
			return false
		}
	}
	if mode != nil && (*mode == "image" || *mode == "audio") {
		return false
	}
	if len(protocols) == 0 {
		return true
	}
	textProtocols := map[string]bool{"chat_completions": true, "responses": true, "messages": true,
		"generate_content": true, "stream_generate_content": true}
	for _, protocol := range protocols {
		if textProtocols[protocol] {
			return true
		}
	}
	return false
}

func todayUTCString() string {
	return time.Now().UTC().Format("2006-01-02")
}

// providerModelDefaultReferenceCleanupInput ports the same-named route
// helper: the cleanup targets fan the provider code out through the protocol
// families so every surface that mirrors the model is cleaned.
func (s *Store) defaultReferenceCleanupTargets(ctx context.Context, providerCode, systemAccountID, model string, clearSystemDefault bool) (*defaultReferenceCleanupInput, error) {
	codes, err := s.providerModelDefaultReferenceCodes(ctx, providerCode)
	if err != nil {
		return nil, err
	}
	targets := make([]defaultReferenceCleanupTarget, 0, len(codes))
	for _, code := range codes {
		customSources, err := s.ModelCatalogSourceProviderCodes(ctx, code)
		if err != nil {
			return nil, err
		}
		targets = append(targets, defaultReferenceCleanupTarget{
			ProviderCode:               code,
			BuiltInSourceProviderCodes: modelCatalogBuiltInSourceProviderCodes(code, customSources),
			CustomSourceProviderCodes:  customSources,
		})
	}
	return &defaultReferenceCleanupInput{
		Model:              model,
		Targets:            targets,
		SystemAccountID:    systemAccountID,
		ClearSystemDefault: clearSystemDefault,
	}, nil
}

// providerModelDefaultReferenceCodes ports providerModelDefaultReferenceCodes.
func (s *Store) providerModelDefaultReferenceCodes(ctx context.Context, providerCode string) ([]string, error) {
	normalized := strings.TrimSpace(providerCode)
	if normalized == "" || isHybridProviderCode(normalized) {
		if normalized == "" {
			return []string{}, nil
		}
		return []string{normalized}, nil
	}
	definition, err := s.FindDefinition(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return []string{normalized}, nil
	}
	protocolCodes := map[string]bool{}
	for _, profile := range definition.ProtocolProfiles {
		if profile.Enabled {
			protocolCodes[profile.ProtocolCode] = true
		}
	}
	seen := map[string]bool{normalized: true}
	codes := []string{normalized}
	if protocolCodes["openai"] {
		if !seen[openaiProviderCode] {
			seen[openaiProviderCode] = true
			codes = append(codes, openaiProviderCode)
		}
	}
	if protocolCodes["openai"] || protocolCodes["anthropic"] || protocolCodes["gemini"] {
		if !seen[hybridProviderCode] {
			seen[hybridProviderCode] = true
			codes = append(codes, hybridProviderCode)
		}
	}
	return codes, nil
}

// customCatalogItemFromRecord ports toCustomCatalogItem for save responses.
func customCatalogItemFromRecord(record *customProviderModelRecord) ModelCatalogItem {
	source := "custom-personal"
	if record.Scope == catalogScopeGlobal {
		source = "custom-global"
	}
	item := ModelCatalogItem{
		ID:                    record.ID,
		ProviderCode:          record.ProviderCode,
		Model:                 record.Model,
		Mode:                  record.Mode,
		ReleaseDate:           record.ReleaseDate,
		ShutdownDate:          record.ShutdownDate,
		SupportedAPIProtocols: copyStringSlice(record.SupportedAPIProtocols),
		InputModalities:       []string{},
		OutputModalities:      []string{},
		SupportedTools:        []string{},
		GenerationParameterCapabilities: generationParameterCapabilitiesToAny(
			generationParameterCapabilitiesForModel(record.ProviderCode, record.Model, record.MaxOutputTokens)),
		InputUsdPer1M:                 record.InputUsdPer1M,
		OutputUsdPer1M:                record.OutputUsdPer1M,
		CachedInputUsdPer1M:           record.CachedInputUsdPer1M,
		CacheWriteUsdPer1M:            record.CacheWriteUsdPer1M,
		CacheWrite1hUsdPer1M:          record.CacheWrite1hUsdPer1M,
		CacheStorageUsdPer1MPerHour:   record.CacheStorageUsdPer1MPerHour,
		ServiceTierPrices:             cloneServiceTierPrices(record.ServiceTierPrices),
		ImageInputUsdPer1M:            record.ImageInputUsdPer1M,
		ImageOutputUsdPer1M:           record.ImageOutputUsdPer1M,
		AudioInputUsdPer1M:            record.AudioInputUsdPer1M,
		AudioOutputUsdPer1M:           record.AudioOutputUsdPer1M,
		OutputUsdPerImage:             record.OutputUsdPerImage,
		MaxInputTokens:                record.MaxInputTokens,
		MaxOutputTokens:               record.MaxOutputTokens,
		SupportedServiceTiers:         copyStringSlice(record.SupportedServiceTiers),
		SupportedReasoningEfforts:     copyStringSlice(record.SupportedReasoningEfforts),
		DefaultReasoningEffort:        record.DefaultReasoningEffort,
		CodexSupportedReasoningLevels: []string{},
		SupportsPromptCaching:         record.CachedInputUsdPer1M != nil,
		SupportsServiceTier:           len(record.SupportedServiceTiers) > 0,
		Source:                        source,
		Scope:                         record.Scope,
		Status:                        record.Status,
		SystemAccountID:               record.SystemAccountID,
		ContextWindowTokens:           record.ContextWindowTokens,
		PricingNotes:                  record.PricingNotes,
		CapabilityNotes:               record.CapabilityNotes,
		Notes:                         record.Notes,
		CreatedAt:                     record.CreatedAt,
		UpdatedAt:                     record.UpdatedAt,
	}
	return item
}

// mutationResultBody ports providerModelMutationResult.
func mutationResultBody(id, providerCode, model, status, updatedAt string, clearedProviderCodes []string) map[string]any {
	body := map[string]any{
		"id":           id,
		"providerCode": providerCode,
		"model":        model,
		"status":       status,
		"updatedAt":    updatedAt,
	}
	if len(clearedProviderCodes) > 0 {
		body["defaultHealthCheckModelCleared"] = true
	}
	return body
}
