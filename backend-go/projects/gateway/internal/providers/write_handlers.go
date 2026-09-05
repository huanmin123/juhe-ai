// write_handlers.go carries the remaining write family handlers:
// PATCH /{code}/models/{modelId} (the built-in fork with the admin gate and
// operation log plus the custom fork with ownership), DELETE
// /{code}/models/{modelId} (the AI-account binding guard) and PUT
// /{code}/default-health-check-model (personal preference vs system default
// with the visibility validation).
package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// patchModel serves PATCH /{code}/models/{modelId}: ids outside the
// custom_model_ prefix resolve the built-in provider_model_catalog fork
// first; everything else rides the custom ownership path.
func (d *Deps) patchModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	auth := authsys.AuthContextFrom(r)
	body, ok := decodeModelBody(w, r)
	if !ok {
		return
	}
	modelID := r.PathValue("modelId")
	if !strings.HasPrefix(modelID, "custom_model_") {
		builtIn, err := d.Store.findBuiltInModelPatchState(ctx, modelID)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if builtIn != nil {
			d.patchBuiltInModel(w, r, auth, builtIn, body)
			return
		}
	}
	owner := ""
	if !isAdminRole(auth.Role) {
		owner = auth.SystemAccountID
	}
	existing, err := d.Store.findCustomProviderModelByID(ctx, modelID, owner)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if existing == nil || existing.ProviderCode != r.PathValue("code") {
		kernel.WriteNotFound(w, "自定义模型不存在")
		return
	}
	if !canMutateCustomModel(existing.Scope, stringValueOrEmpty(existing.SystemAccountID), auth) {
		kernel.WriteError(w, http.StatusForbidden, "无权修改该自定义模型")
		return
	}
	parsed, ok := parseCustomModelBody(body, customModelParseOptions{
		allowNotes:               true,
		requireExpectedUpdatedAt: true,
		statusValues:             []string{"draft", "active", "disabled"},
	})
	if !ok {
		kernel.WriteBadRequest(w, "自定义模型参数无效")
		return
	}
	if existing.UpdatedAt != parsed.expectedUpdatedAt {
		kernel.WriteError(w, http.StatusConflict, "模型已被其他操作更新，请刷新后重试")
		return
	}
	fields := make([]string, 0, len(parsed.present))
	for key := range parsed.present {
		fields = append(fields, key)
	}
	next := mergedCustomModelUpsertInput(existing, parsed)
	if requiresCustomModelPatchValidation(parsed) {
		if message := validateCustomModelPricing(existing.ProviderCode, next); message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
	}
	cleanup := (*defaultReferenceCleanupInput)(nil)
	if customModelDefaultUsabilityTransitioned(existing, next) {
		ownerScope := stringValueOrEmpty(existing.SystemAccountID)
		clearSystemDefault := existing.Scope == catalogScopeGlobal
		if clearSystemDefault {
			ownerScope = ""
		}
		input, err := d.Store.defaultReferenceCleanupTargets(ctx, existing.ProviderCode, ownerScope, existing.Model, clearSystemDefault)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		cleanup = input
	}
	outcome, err := d.Store.patchCustomProviderModel(ctx, existing, next, fields, parsed.expectedUpdatedAt, owner, cleanup)
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	if outcome.Kind == "conflict" {
		kernel.WriteError(w, http.StatusConflict, "模型已被其他操作更新，请刷新后重试")
		return
	}
	kernel.WriteOK(w, mutationResultBody(outcome.Record.ID, outcome.Record.ProviderCode, outcome.Record.Model,
		outcome.Record.Status, outcome.Record.UpdatedAt, outcome.ClearedDefaultHealthCheckProviderCodes), "")
}

// patchBuiltInModel renders the built-in fork of PATCH: the provider-code
// match, the admin gate, the built-in schema, the optimistic concurrency,
// the completeness validation and the operation log.
func (d *Deps) patchBuiltInModel(w http.ResponseWriter, r *http.Request, auth *authsys.AuthContext, builtIn *ModelCatalogItem, body map[string]json.RawMessage) {
	if builtIn.ProviderCode != r.PathValue("code") {
		kernel.WriteNotFound(w, "模型不存在")
		return
	}
	if !isAdminRole(auth.Role) {
		kernel.WriteError(w, http.StatusForbidden, "只有管理员可以维护内置模型配置")
		return
	}
	parsed, ok := parseCustomModelBody(body, customModelParseOptions{
		allowCatalogVisible:      true,
		requireExpectedUpdatedAt: true,
		statusValues:             []string{"active", "disabled"},
	})
	if !ok {
		kernel.WriteBadRequest(w, "内置模型配置参数无效")
		return
	}
	if builtIn.UpdatedAt != parsed.expectedUpdatedAt {
		kernel.WriteError(w, http.StatusConflict, "模型已被其他操作更新，请刷新后重试")
		return
	}
	submitted := builtInSubmittedFields(parsed)
	patch := configurationChanges(builtIn, submitted)
	if len(patch) == 0 {
		kernel.WriteOK(w, mutationResultBody(builtIn.ID, builtIn.ProviderCode, builtIn.Model, builtIn.Status, builtIn.UpdatedAt, nil), "")
		return
	}
	next := mergedBuiltInItem(builtIn, patch)
	if requiresBuiltInModelPatchValidation(patch) {
		if message := validateBuiltInCapabilities(builtIn.ProviderCode, next); message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
		if message := validateBuiltInModelCompleteness(next); message != "" {
			kernel.WriteBadRequest(w, message)
			return
		}
	}
	cleanup := (*defaultReferenceCleanupInput)(nil)
	if builtinDefaultUsabilityTransitioned(builtIn, next) {
		input, err := d.Store.defaultReferenceCleanupTargets(r.Context(), builtIn.ProviderCode, "", builtIn.Model, true)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		cleanup = input
	}
	saved, err := d.Store.patchBuiltInModelConfiguration(r.Context(), builtIn, patch, parsed.expectedUpdatedAt, cleanup)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if saved == nil {
		kernel.WriteError(w, http.StatusConflict, "模型已被其他操作更新，请刷新后重试")
		return
	}
	d.recordModelConfigurationLog(r, auth, builtIn, next, patch)
	kernel.WriteOK(w, mutationResultBody(saved.ID, saved.ProviderCode, saved.Model, saved.Status, saved.UpdatedAt, nil), "")
}

// recordModelConfigurationLog ports the recordOperationLogAsync call of the
// built-in PATCH route.
func (d *Deps) recordModelConfigurationLog(r *http.Request, auth *authsys.AuthContext, before, after *ModelCatalogItem, patch []builtinPatchField) {
	if d.Sink == nil {
		return
	}
	fields := make([]string, 0, len(patch))
	for _, field := range patch {
		fields = append(fields, field.Name)
	}
	d.Sink.Record(authsys.OperationLogEntry{
		ActorSystemAccountID: auth.SystemAccountID,
		ActorUsername:        auth.Username,
		ActorDisplayName:     auth.DisplayName,
		ActorRole:            auth.Role,
		Mode:                 "write",
		Module:               "providers",
		Action:               "update_model_configuration",
		OperationKey:         "providers.update_model_configuration",
		ResourceType:         "provider_model",
		ResourceID:           before.ID,
		ResourceName:         before.Model,
		Summary:              "更新模型配置：" + after.Model,
		DetailLevel:          "full",
		VisibilityScope:      "admin_only",
		Changes: []authsys.OperationLogChange{{
			Field:  "configuration",
			Label:  "模型配置",
			Before: string(mustJSON(builtInPatchSnapshot(before, fields))),
			After:  string(mustJSON(builtInPatchSnapshot(after, fields))),
		}},
	}, r)
}

func builtInPatchSnapshot(item *ModelCatalogItem, fields []string) map[string]any {
	snapshot := map[string]any{}
	for _, field := range fields {
		snapshot[field] = builtInCurrentValue(item, field)
	}
	return snapshot
}

// builtInSubmittedFields converts the parsed body into the submitted patch
// field list (JSON value semantics preserved).
func builtInSubmittedFields(parsed *customModelParsedInput) []builtinPatchField {
	fields := []builtinPatchField{}
	if parsed.status != "" {
		fields = append(fields, builtinPatchField{Name: "status", Value: parsed.status})
	}
	if parsed.catalogVisible != nil {
		fields = append(fields, builtinPatchField{Name: "catalogVisible", Value: *parsed.catalogVisible})
	}
	values := parsed.values
	for key := range parsed.present {
		switch key {
		case "mode":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.Mode)})
		case "supportedApiProtocols":
			fields = append(fields, builtinPatchField{Name: key, Value: anySlice(values.SupportedAPIProtocols)})
		case "supportedServiceTiers":
			fields = append(fields, builtinPatchField{Name: key, Value: anySlice(values.SupportedServiceTiers)})
		case "supportedReasoningEfforts":
			fields = append(fields, builtinPatchField{Name: key, Value: anySlice(values.SupportedReasoningEfforts)})
		case "defaultReasoningEffort":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.DefaultReasoningEffort)})
		case "releaseDate":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.ReleaseDate)})
		case "shutdownDate":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.ShutdownDate)})
		case "contextWindowTokens":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.ContextWindowTokens)})
		case "maxInputTokens":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.MaxInputTokens)})
		case "maxOutputTokens":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.MaxOutputTokens)})
		case "inputUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.InputUsdPer1M)})
		case "outputUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.OutputUsdPer1M)})
		case "cachedInputUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.CachedInputUsdPer1M)})
		case "cacheWriteUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.CacheWriteUsdPer1M)})
		case "cacheWrite1hUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.CacheWrite1hUsdPer1M)})
		case "cacheStorageUsdPer1MPerHour":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.CacheStorageUsdPer1MPerHour)})
		case "serviceTierPrices":
			fields = append(fields, builtinPatchField{Name: key, Value: serviceTierPricesToAny(values.ServiceTierPrices)})
		case "imageInputUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.ImageInputUsdPer1M)})
		case "imageOutputUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.ImageOutputUsdPer1M)})
		case "audioInputUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.AudioInputUsdPer1M)})
		case "audioOutputUsdPer1M":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.AudioOutputUsdPer1M)})
		case "outputUsdPerImage":
			fields = append(fields, builtinPatchField{Name: key, Value: jsonNullable(values.OutputUsdPerImage)})
		}
	}
	return fields
}

// jsonNullable widens a pointer into the JSON value space (nil = null).
func jsonNullable[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

// mergedBuiltInItem applies the diffed patch onto the current row for the
// validation reads ({...builtIn, ...configurationPatch}).
func mergedBuiltInItem(current *ModelCatalogItem, patch []builtinPatchField) *ModelCatalogItem {
	next := *current
	for _, field := range patch {
		switch field.Name {
		case "status":
			if text, ok := field.Value.(string); ok {
				next.Status = text
			}
		case "catalogVisible":
			if visible, ok := field.Value.(bool); ok {
				next.CatalogVisible = &visible
			}
		case "mode":
			next.Mode = pointerFromJSON[string](field.Value)
		case "supportedApiProtocols":
			next.SupportedAPIProtocols = stringSliceFromJSON(field.Value)
		case "supportedServiceTiers":
			next.SupportedServiceTiers = stringSliceFromJSON(field.Value)
		case "supportedReasoningEfforts":
			next.SupportedReasoningEfforts = stringSliceFromJSON(field.Value)
		case "defaultReasoningEffort":
			next.DefaultReasoningEffort = pointerFromJSON[string](field.Value)
		case "releaseDate":
			next.ReleaseDate = pointerFromJSON[string](field.Value)
		case "shutdownDate":
			next.ShutdownDate = pointerFromJSON[string](field.Value)
		case "contextWindowTokens":
			next.ContextWindowTokens = pointerFromJSON[int64](field.Value)
		case "maxInputTokens":
			next.MaxInputTokens = pointerFromJSON[int64](field.Value)
		case "maxOutputTokens":
			next.MaxOutputTokens = pointerFromJSON[int64](field.Value)
		case "inputUsdPer1M":
			next.InputUsdPer1M = pointerFromJSON[float64](field.Value)
		case "outputUsdPer1M":
			next.OutputUsdPer1M = pointerFromJSON[float64](field.Value)
		case "cachedInputUsdPer1M":
			next.CachedInputUsdPer1M = pointerFromJSON[float64](field.Value)
		case "cacheWriteUsdPer1M":
			next.CacheWriteUsdPer1M = pointerFromJSON[float64](field.Value)
		case "cacheWrite1hUsdPer1M":
			next.CacheWrite1hUsdPer1M = pointerFromJSON[float64](field.Value)
		case "cacheStorageUsdPer1MPerHour":
			next.CacheStorageUsdPer1MPerHour = pointerFromJSON[float64](field.Value)
		case "serviceTierPrices":
			next.ServiceTierPrices = normalizeServiceTierPricesValue(serviceTierPricesFromAny(field.Value))
		case "imageInputUsdPer1M":
			next.ImageInputUsdPer1M = pointerFromJSON[float64](field.Value)
		case "imageOutputUsdPer1M":
			next.ImageOutputUsdPer1M = pointerFromJSON[float64](field.Value)
		case "audioInputUsdPer1M":
			next.AudioInputUsdPer1M = pointerFromJSON[float64](field.Value)
		case "audioOutputUsdPer1M":
			next.AudioOutputUsdPer1M = pointerFromJSON[float64](field.Value)
		case "outputUsdPerImage":
			next.OutputUsdPerImage = pointerFromJSON[float64](field.Value)
		}
	}
	return &next
}

func pointerFromJSON[T int64 | float64 | string](value any) *T {
	typed, ok := value.(T)
	if !ok {
		return nil
	}
	return &typed
}

func stringSliceFromJSON(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{}
	}
	output := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			output = append(output, text)
		}
	}
	return output
}

// validateBuiltInCapabilities renders validateCustomModelCapabilities over a
// merged built-in row (same rules, catalog-item input).
func validateBuiltInCapabilities(providerCode string, next *ModelCatalogItem) string {
	input := customProviderModelUpsertInput{
		Mode:                      next.Mode,
		SupportedServiceTiers:     next.SupportedServiceTiers,
		SupportedReasoningEfforts: next.SupportedReasoningEfforts,
		ServiceTierPrices:         next.ServiceTierPrices,
	}
	return validateCustomModelCapabilities(providerCode, input)
}

// requiresCustomModelPatchValidation ports requiresCustomModelPatchValidation.
func requiresCustomModelPatchValidation(parsed *customModelParsedInput) bool {
	if parsed.status == "active" {
		return true
	}
	for key := range parsed.present {
		if providerModelValidationFields[key] {
			return true
		}
	}
	return false
}

// requiresBuiltInModelPatchValidation ports requiresBuiltInModelPatchValidation.
func requiresBuiltInModelPatchValidation(patch []builtinPatchField) bool {
	for _, field := range patch {
		if providerModelValidationFields[field.Name] || field.Name == "status" && field.Value == "active" {
			return true
		}
		switch field.Name {
		case "supportedApiProtocols", "releaseDate", "contextWindowTokens", "maxInputTokens", "maxOutputTokens":
			return true
		}
	}
	return false
}

// customModelDefaultUsabilityTransitioned ports
// providerModelDefaultUsabilityTransitionedToUnavailable for custom records:
// next is the merged record, custom rows carry catalogVisible = true.
func customModelDefaultUsabilityTransitioned(current *customProviderModelRecord, next customProviderModelUpsertInput) bool {
	before := providerModelIsUsableAsDefault(current.Status, boolPtr(true), current.ShutdownDate, current.Mode, current.SupportedAPIProtocols)
	afterStatus := next.Status
	if afterStatus == "" {
		afterStatus = current.Status
	}
	after := providerModelIsUsableAsDefault(afterStatus, boolPtr(true), next.ShutdownDate, next.Mode, next.SupportedAPIProtocols)
	return before && !after
}

// builtinDefaultUsabilityTransitioned ports the same check for built-ins.
func builtinDefaultUsabilityTransitioned(before, after *ModelCatalogItem) bool {
	was := providerModelIsUsableAsDefault(before.Status, before.CatalogVisible, before.ShutdownDate, before.Mode, before.SupportedAPIProtocols)
	now := providerModelIsUsableAsDefault(after.Status, after.CatalogVisible, after.ShutdownDate, after.Mode, after.SupportedAPIProtocols)
	return was && !now
}

func boolPtr(value bool) *bool { return &value }

// mergedCustomModelUpsertInput builds the merged next input
// ({...existing, ...submittedPatch, defaultReasoningEffort: null, scope}).
func mergedCustomModelUpsertInput(existing *customProviderModelRecord, parsed *customModelParsedInput) customProviderModelUpsertInput {
	next := customProviderModelUpsertInput{
		ProviderCode:                existing.ProviderCode,
		Model:                       existing.Model,
		Scope:                       existing.Scope,
		SystemAccountID:             stringValueOrEmpty(existing.SystemAccountID),
		Status:                      existing.Status,
		Mode:                        existing.Mode,
		SupportedAPIProtocols:       existing.SupportedAPIProtocols,
		SupportedServiceTiers:       existing.SupportedServiceTiers,
		SupportedReasoningEfforts:   existing.SupportedReasoningEfforts,
		DefaultReasoningEffort:      nil,
		ReleaseDate:                 existing.ReleaseDate,
		ShutdownDate:                existing.ShutdownDate,
		ContextWindowTokens:         existing.ContextWindowTokens,
		MaxInputTokens:              existing.MaxInputTokens,
		MaxOutputTokens:             existing.MaxOutputTokens,
		InputUsdPer1M:               existing.InputUsdPer1M,
		OutputUsdPer1M:              existing.OutputUsdPer1M,
		CachedInputUsdPer1M:         existing.CachedInputUsdPer1M,
		CacheWriteUsdPer1M:          existing.CacheWriteUsdPer1M,
		CacheWrite1hUsdPer1M:        existing.CacheWrite1hUsdPer1M,
		CacheStorageUsdPer1MPerHour: existing.CacheStorageUsdPer1MPerHour,
		ServiceTierPrices:           existing.ServiceTierPrices,
		ImageInputUsdPer1M:          existing.ImageInputUsdPer1M,
		ImageOutputUsdPer1M:         existing.ImageOutputUsdPer1M,
		AudioInputUsdPer1M:          existing.AudioInputUsdPer1M,
		AudioOutputUsdPer1M:         existing.AudioOutputUsdPer1M,
		OutputUsdPerImage:           existing.OutputUsdPerImage,
		PricingNotes:                existing.PricingNotes,
		CapabilityNotes:             existing.CapabilityNotes,
		Notes:                       existing.Notes,
	}
	applyParsedCustomModelFields(&next, parsed)
	next.DefaultReasoningEffort = nil
	return next
}

func stringValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// deleteModel serves DELETE /{code}/models/{modelId}: ownership, the
// AI-account binding guard and the transactional default-reference cleanup.
func (d *Deps) deleteModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	auth := authsys.AuthContextFrom(r)
	owner := ""
	if !isAdminRole(auth.Role) {
		owner = auth.SystemAccountID
	}
	existing, err := d.Store.findCustomProviderModelByID(ctx, r.PathValue("modelId"), owner)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if existing == nil || existing.ProviderCode != r.PathValue("code") {
		kernel.WriteNotFound(w, "自定义模型不存在")
		return
	}
	if !canMutateCustomModel(existing.Scope, stringValueOrEmpty(existing.SystemAccountID), auth) {
		kernel.WriteError(w, http.StatusForbidden, "无权删除该自定义模型")
		return
	}
	bindings, err := d.Store.customProviderModelBindings(ctx, existing.ProviderCode, existing.Model, existing.Scope, stringValueOrEmpty(existing.SystemAccountID))
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if bindings.TotalAccountCount > 0 {
		kernel.WriteError(w, http.StatusConflict, customModelBoundToAccountMessage(bindings))
		return
	}
	ownerScope := stringValueOrEmpty(existing.SystemAccountID)
	clearSystemDefault := existing.Scope == catalogScopeGlobal
	if clearSystemDefault {
		ownerScope = ""
	}
	cleanup, err := d.Store.defaultReferenceCleanupTargets(ctx, existing.ProviderCode, ownerScope, existing.Model, clearSystemDefault)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	deleted, err := d.Store.deleteCustomProviderModel(ctx, existing.ID, owner, cleanup)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, map[string]any{"deleted": deleted}, "")
}

// putDefaultHealthCheckModel serves PUT /{code}/default-health-check-model:
// the management fork saves the system default, everyone else saves a
// personal preference after the catalog-visibility validation.
func (d *Deps) putDefaultHealthCheckModel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
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
	model, ok := parseHealthCheckModelBody(body)
	if !ok {
		kernel.WriteBadRequest(w, "默认检查模型参数无效")
		return
	}
	saveAsSystemDefault := isManagementProviderRequest(r)
	target := ""
	if !saveAsSystemDefault {
		target = requestSystemAccountID(r)
		if target == "" {
			kernel.WriteBadRequest(w, "请选择要设置默认检查模型的系统账户")
			return
		}
	}
	validatedModel, message, err := d.validateDefaultHealthCheckModelSelection(ctx, provider.Code, target, model)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	if saveAsSystemDefault {
		if err := d.Store.UpsertSystemDefaultHealthCheckModel(ctx, provider.Code, validatedModel); err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
	} else {
		if err := d.Store.UpsertDefaultHealthCheckModelPreference(ctx, target, provider.Code, validatedModel); err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
	}
	kernel.WriteOK(w, map[string]any{
		"providerCode":            provider.Code,
		"defaultHealthCheckModel": validatedModel,
	}, "")
}

// parseHealthCheckModelBody mirrors defaultHealthCheckModelSchema (strict).
func parseHealthCheckModelBody(body map[string]json.RawMessage) (string, bool) {
	for key := range body {
		if key != "model" {
			return "", false
		}
	}
	raw, ok := body["model"]
	if !ok {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", false
	}
	return trimmed, true
}

// validateDefaultHealthCheckModelSelection ports
// validateDefaultHealthCheckModelSelection: (validated model, "" = valid).
func (d *Deps) validateDefaultHealthCheckModelSelection(ctx context.Context, providerCode, systemAccountID, model string) (string, string, error) {
	activeCatalog, err := d.Store.ListProviderModelsForRequest(ctx, providerCode, systemAccountID, false, true)
	if err != nil {
		return "", "", err
	}
	for index := range activeCatalog {
		if strings.TrimSpace(activeCatalog[index].Model) != model {
			continue
		}
		if !isProviderModelUsableForAccountTest(&activeCatalog[index]) {
			return "", "默认检查模型只能选择文本生成模型", nil
		}
		return activeCatalog[index].Model, "", nil
	}
	// 非活动目录仅用于给出具体诊断，不应让同名的过期来源遮蔽活动来源。
	inactiveCatalog, err := d.Store.ListProviderModelsForRequest(ctx, providerCode, systemAccountID, true, true)
	if err != nil {
		return "", "", err
	}
	for index := range inactiveCatalog {
		if strings.TrimSpace(inactiveCatalog[index].Model) != model {
			continue
		}
		if !isProviderModelUsableForAccountTest(&inactiveCatalog[index]) {
			return "", "默认检查模型只能选择文本生成模型", nil
		}
		return "", "只能把当前可用的模型设置为默认检查模型", nil
	}
	return "", "模型不在当前用户可见目录中：" + model, nil
}
