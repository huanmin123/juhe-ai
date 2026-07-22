package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
	"juhe-ai/backend-go/internal/store/port"
)

type managementProviderModelService interface {
	ModelSelectionOptions(r *http.Request, input managementprovidermodels.ModelSelectionOptionListInput) ([]managementprovidermodels.ModelSelectionOption, error)
	Models(r *http.Request, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error)
	ModelCapabilities(r *http.Request, input managementprovidermodels.ModelCapabilitiesInput) (managementprovidermodels.ModelCapabilities, error)
	SetDefaultHealthCheckModel(r *http.Request, input managementprovidermodels.DefaultHealthCheckModelInput) (managementprovidermodels.DefaultHealthCheckModelResult, error)
	CreateCustomModel(r *http.Request, input managementprovidermodels.CustomModelCreateInput) (managementprovidermodels.ModelCatalogItem, error)
	UpdateCustomModelWithSnapshots(r *http.Request, input managementprovidermodels.CustomModelUpdateInput) (managementprovidermodels.CustomModelUpdateResult, error)
	DeleteCustomModel(r *http.Request, input managementprovidermodels.CustomModelDeleteInput) (managementprovidermodels.CustomModelDeleteResult, error)
}

type managementProviderModelServiceAdapter struct {
	service *managementprovidermodels.Service
}

func (s managementProviderModelServiceAdapter) ModelSelectionOptions(r *http.Request, input managementprovidermodels.ModelSelectionOptionListInput) ([]managementprovidermodels.ModelSelectionOption, error) {
	return s.service.ModelSelectionOptions(r.Context(), input)
}

func (s managementProviderModelServiceAdapter) Models(r *http.Request, input managementprovidermodels.ModelListInput) ([]managementprovidermodels.ModelCatalogItem, error) {
	return s.service.Models(r.Context(), input)
}

func (s managementProviderModelServiceAdapter) ModelCapabilities(r *http.Request, input managementprovidermodels.ModelCapabilitiesInput) (managementprovidermodels.ModelCapabilities, error) {
	return s.service.ModelCapabilities(r.Context(), input)
}

func (s managementProviderModelServiceAdapter) SetDefaultHealthCheckModel(r *http.Request, input managementprovidermodels.DefaultHealthCheckModelInput) (managementprovidermodels.DefaultHealthCheckModelResult, error) {
	return s.service.SetDefaultHealthCheckModel(r.Context(), input)
}

func (s managementProviderModelServiceAdapter) CreateCustomModel(r *http.Request, input managementprovidermodels.CustomModelCreateInput) (managementprovidermodels.ModelCatalogItem, error) {
	return s.service.CreateCustomModel(r.Context(), input)
}

func (s managementProviderModelServiceAdapter) UpdateCustomModelWithSnapshots(r *http.Request, input managementprovidermodels.CustomModelUpdateInput) (managementprovidermodels.CustomModelUpdateResult, error) {
	return s.service.UpdateCustomModelWithSnapshots(r.Context(), input)
}

func (s managementProviderModelServiceAdapter) DeleteCustomModel(r *http.Request, input managementprovidermodels.CustomModelDeleteInput) (managementprovidermodels.CustomModelDeleteResult, error) {
	return s.service.DeleteCustomModel(r.Context(), input)
}

func NewManagementProviderModelOptionsHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderModelOptionsHandler(managementProviderModelServiceAdapter{service: service})
}

func NewManagementProviderModelsHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderModelsHandler(managementProviderModelServiceAdapter{service: service})
}

func NewManagementProviderModelCapabilitiesHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderModelCapabilitiesHandler(managementProviderModelServiceAdapter{service: service})
}

func NewManagementProviderDefaultHealthCheckModelHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderDefaultHealthCheckModelHandler(managementProviderModelServiceAdapter{service: service})
}

func NewManagementProviderCustomModelCreateHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderCustomModelCreateHandler(managementProviderModelServiceAdapter{service: service})
}

func NewManagementProviderCustomModelUpdateHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderCustomModelUpdateHandler(managementProviderModelServiceAdapter{service: service})
}

func NewManagementProviderCustomModelUpdateHandlerWithOperationLog(service *managementprovidermodels.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementProviderCustomModelUpdateHandler(
		managementProviderModelServiceAdapter{service: service},
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementProviderCustomModelDeleteHandler(service *managementprovidermodels.Service) http.Handler {
	return newManagementProviderCustomModelDeleteHandler(managementProviderModelServiceAdapter{service: service})
}

func newManagementProviderModelOptionsHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, message := managementProviderModelSelectionInput(r, authContext)
		if message != "" {
			writeMessageError(w, http.StatusBadRequest, message)
			return
		}
		options, err := service.ModelSelectionOptions(r, input)
		if err != nil {
			if errors.Is(err, managementprovidermodels.ErrProviderNotFound) {
				writeMessageError(w, http.StatusNotFound, "供应商不存在或已停用")
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, options)
	})
}

func managementProviderModelSelectionInput(r *http.Request, authContext managementauth.Context) (managementprovidermodels.ModelSelectionOptionListInput, string) {
	values := r.URL.Query()
	protocol := firstManagementQueryText(values, "protocol")
	if protocol != "" && protocol != "openai" && protocol != "anthropic" && protocol != "gemini" {
		return managementprovidermodels.ModelSelectionOptionListInput{}, "protocol 必须是 openai、anthropic 或 gemini"
	}
	limit := 50
	if rawLimit := firstManagementQueryText(values, "limit"); rawLimit != "" {
		parsed, err := strconv.ParseFloat(rawLimit, 64)
		if err != nil || math.Trunc(parsed) != parsed || parsed < 1 || parsed > 50 {
			return managementprovidermodels.ModelSelectionOptionListInput{}, "limit 必须是 1 到 50 的整数"
		}
		limit = int(parsed)
	}
	return managementprovidermodels.ModelSelectionOptionListInput{
		ProviderCode:    firstManagementQueryText(values, "providerCode"),
		SystemAccountID: managementScopedSystemAccountID(authContext, values),
		Protocol:        protocol,
		Keyword:         firstManagementQueryText(values, "keyword"),
		Limit:           limit,
		SelectedIDs:     managementProviderModelSelectedIDs(values["selectedIds"]),
	}, ""
}

func managementProviderModelSelectedIDs(values []string) []string {
	items := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		for _, candidate := range strings.Split(value, ",") {
			id := strings.TrimSpace(candidate)
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			items = append(items, id)
			if len(items) == 50 {
				return items
			}
		}
	}
	return items
}

func newManagementProviderModelsHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		includeInactive, _ := managementBooleanQueryValue(r.URL.Query(), "includeInactive")
		includeUnpriced, _ := managementBooleanQueryValue(r.URL.Query(), "includeUnpriced")
		models, err := service.Models(r, managementprovidermodels.ModelListInput{
			ProviderCode:    chi.URLParam(r, "code"),
			SystemAccountID: managementScopedSystemAccountID(authContext, r.URL.Query()),
			IncludeInactive: includeInactive,
			IncludeUnpriced: includeUnpriced,
		})
		if err != nil {
			if errors.Is(err, managementprovidermodels.ErrProviderNotFound) {
				writeMessageError(w, http.StatusNotFound, "供应商不存在")
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, models)
	})
}

func newManagementProviderModelCapabilitiesHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		modelID, err := url.PathUnescape(chi.URLParam(r, "modelId"))
		if err != nil {
			writeMessageError(w, http.StatusNotFound, "模型不存在")
			return
		}
		capabilities, err := service.ModelCapabilities(r, managementprovidermodels.ModelCapabilitiesInput{
			ProviderCode:    chi.URLParam(r, "code"),
			SystemAccountID: managementScopedSystemAccountID(authContext, r.URL.Query()),
			Model:           modelID,
		})
		if err != nil {
			if errors.Is(err, managementprovidermodels.ErrProviderNotFound) {
				writeMessageError(w, http.StatusNotFound, "供应商不存在或已停用")
				return
			}
			if errors.Is(err, managementprovidermodels.ErrModelCapabilitiesNotFound) {
				writeMessageError(w, http.StatusNotFound, "模型不存在")
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, capabilities)
	})
}

func newManagementProviderDefaultHealthCheckModelHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		var payload struct {
			Model string `json:"model"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil || strings.TrimSpace(payload.Model) == "" {
			writeMessageError(w, http.StatusBadRequest, "默认检查模型参数无效")
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "默认检查模型参数无效")
			return
		}
		result, err := service.SetDefaultHealthCheckModel(r, managementprovidermodels.DefaultHealthCheckModelInput{
			ProviderCode:         chi.URLParam(r, "code"),
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			Model:                payload.Model,
		})
		if err != nil {
			if errors.Is(err, managementprovidermodels.ErrProviderNotFound) {
				writeMessageError(w, http.StatusNotFound, "供应商不存在")
				return
			}
			if message, ok := managementprovidermodels.DefaultHealthCheckModelValidationMessage(err); ok {
				writeMessageError(w, http.StatusBadRequest, message)
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementProviderCustomModelCreateHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		fields, ok := decodeManagementProviderCustomModelBody(w, r, false)
		if !ok {
			return
		}
		result, err := service.CreateCustomModel(r, managementprovidermodels.CustomModelCreateInput{
			ProviderCode:          chi.URLParam(r, "code"),
			ActorSystemAccountID:  authContext.SystemAccountID,
			ActorRole:             authContext.Role,
			TargetSystemAccountID: managementScopedSystemAccountID(authContext, r.URL.Query()),
			Fields:                fields,
			TraceID:               requestIDFromContext(r.Context()),
		})
		if err != nil {
			writeManagementProviderCustomModelError(w, err)
			return
		}
		writeData(w, http.StatusCreated, result)
	})
}

func newManagementProviderCustomModelUpdateHandler(service managementProviderModelService, logOptions ...managementOperationLogOptions) http.Handler {
	operationLogs := effectiveManagementOperationLogOptions(logOptions)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		fields, ok := decodeManagementProviderCustomModelBody(w, r, true)
		if !ok {
			return
		}
		result, err := service.UpdateCustomModelWithSnapshots(r, managementprovidermodels.CustomModelUpdateInput{
			ProviderCode:         chi.URLParam(r, "code"),
			ID:                   chi.URLParam(r, "id"),
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			Fields:               fields,
			TraceID:              requestIDFromContext(r.Context()),
		})
		if err != nil {
			writeManagementProviderCustomModelError(w, err)
			return
		}
		if result.After.Scope == "built_in" {
			recordManagementProviderModelConfigurationUpdateOperationLog(r, authContext, result.Before, result.After, operationLogs)
		}
		writeData(w, http.StatusOK, result.After)
	})
}

func recordManagementProviderModelConfigurationUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	before managementprovidermodels.ModelCatalogItem,
	result managementprovidermodels.ModelCatalogItem,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil {
		return
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	newLogID := opts.newLogID
	if newLogID == nil {
		newLogID = defaultManagementOperationLogID
	}
	statusCode := http.StatusOK
	enqueueManagementOperationLog(r.Context(), opts, port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: authContext.SystemAccountID,
		Mode:                          "admin",
		Module:                        "providers",
		Action:                        "update_model_configuration",
		OperationKey:                  "providers.update_model_configuration",
		ResourceType:                  "provider_model",
		ResourceID:                    result.ID,
		ResourceName:                  result.Model,
		Summary:                       "更新模型配置：" + result.Model,
		DetailLevel:                   "full",
		VisibilityScope:               "admin_only",
		Changes: []port.OperationLogChange{{
			Field:  "configuration",
			Label:  "模型配置",
			Before: managementProviderModelConfigurationSnapshot(before),
			After:  managementProviderModelConfigurationSnapshot(result),
		}},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		CreatedAt:  now().UTC(),
	})
}

func managementProviderModelConfigurationSnapshot(item managementprovidermodels.ModelCatalogItem) map[string]any {
	return map[string]any{
		"status": item.Status, "mode": item.Mode, "supportedApiProtocols": item.SupportedAPIProtocols,
		"supportedServiceTiers": item.SupportedServiceTiers, "supportedReasoningEfforts": item.SupportedReasoningEfforts,
		"defaultReasoningEffort": item.DefaultReasoningEffort, "releaseDate": item.ReleaseDate, "shutdownDate": item.ShutdownDate,
		"contextWindowTokens": item.ContextWindowTokens, "maxInputTokens": item.MaxInputTokens, "maxOutputTokens": item.MaxOutputTokens,
		"inputUsdPer1M": item.InputUSDPer1M, "outputUsdPer1M": item.OutputUSDPer1M,
		"cachedInputUsdPer1M": item.CachedInputUSDPer1M, "cacheWriteUsdPer1M": item.CacheWriteUSDPer1M,
		"cacheWrite1hUsdPer1M": item.CacheWrite1hUSDPer1M, "serviceTierPrices": item.ServiceTierPrices,
		"imageInputUsdPer1M": item.ImageInputUSDPer1M, "imageOutputUsdPer1M": item.ImageOutputUSDPer1M,
		"audioInputUsdPer1M": item.AudioInputUSDPer1M, "audioOutputUsdPer1M": item.AudioOutputUSDPer1M,
		"outputUsdPerImage": item.OutputUSDPerImage,
	}
}

func newManagementProviderCustomModelDeleteHandler(service managementProviderModelService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := service.DeleteCustomModel(r, managementprovidermodels.CustomModelDeleteInput{
			ProviderCode:         chi.URLParam(r, "code"),
			ID:                   chi.URLParam(r, "id"),
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			TraceID:              requestIDFromContext(r.Context()),
		})
		if err != nil {
			writeManagementProviderCustomModelError(w, err)
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func writeManagementProviderCustomModelError(w http.ResponseWriter, err error) {
	if errors.Is(err, managementprovidermodels.ErrProviderNotFound) {
		writeMessageError(w, http.StatusNotFound, "供应商不存在")
		return
	}
	if errors.Is(err, managementprovidermodels.ErrCustomProviderModelNotFound) {
		writeMessageError(w, http.StatusNotFound, "自定义模型不存在")
		return
	}
	if message, ok := managementprovidermodels.CustomModelForbiddenMessage(err); ok {
		writeMessageError(w, http.StatusForbidden, message)
		return
	}
	if message, ok := managementprovidermodels.CustomModelBoundMessage(err); ok {
		writeMessageError(w, http.StatusConflict, message)
		return
	}
	if message, ok := managementprovidermodels.CustomModelValidationMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
}

func decodeManagementProviderCustomModelBody(w http.ResponseWriter, r *http.Request, _ bool) (managementprovidermodels.CustomModelMutation, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeManagementProviderCustomModelBodyError(w, err)
		return managementprovidermodels.CustomModelMutation{}, false
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementprovidermodels.CustomModelMutation{}, false
	}
	fields := managementprovidermodels.CustomModelMutation{}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		fields.Invalid = true
		return fields, true
	}
	for field, raw := range payload {
		switch field {
		case "configurationTemplateId":
			value, ok := decodeManagementProviderCustomModelRequiredString(raw)
			value = strings.TrimSpace(value)
			if !ok || value == "" {
				fields.Invalid = true
				continue
			}
			fields.ConfigurationTemplateID = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "scope":
			value, ok := decodeManagementProviderCustomModelRequiredString(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.Scope = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "model":
			value, ok := decodeManagementProviderCustomModelRequiredString(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.Model = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "status":
			value, ok := decodeManagementProviderCustomModelRequiredString(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.Status = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "catalogVisible":
			value, ok := decodeManagementProviderCustomModelBool(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.CatalogVisible = managementprovidermodels.OptionalBool{Set: true, Value: value}
		case "mode":
			value, ok := decodeManagementProviderCustomModelNullableString(raw, false)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.Mode = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "supportedApiProtocols":
			value, ok := decodeManagementProviderCustomModelStringList(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.SupportedAPIProtocols = managementprovidermodels.OptionalStringList{Set: true, Value: value}
		case "supportedServiceTiers":
			value, ok := decodeManagementProviderCustomModelStringList(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.SupportedServiceTiers = managementprovidermodels.OptionalStringList{Set: true, Value: value}
		case "supportedReasoningEfforts":
			value, ok := decodeManagementProviderCustomModelStringList(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.SupportedReasoningEfforts = managementprovidermodels.OptionalStringList{Set: true, Value: value}
		case "defaultReasoningEffort":
			value, ok := decodeManagementProviderCustomModelNullableString(raw, false)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.DefaultReasoningEffort = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "serviceTierPrices":
			var value map[string]managementprovidermodels.ModelPriceSet
			if string(raw) == "null" {
				value = map[string]managementprovidermodels.ModelPriceSet{}
			} else if decoded, ok := decodeManagementProviderModelPriceMap(raw); !ok {
				fields.Invalid = true
				continue
			} else {
				value = decoded
			}
			fields.ServiceTierPrices = managementprovidermodels.OptionalProviderModelPriceMap{Set: true, Value: value}
		case "releaseDate":
			value, ok := decodeManagementProviderCustomModelNullableString(raw, false)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.ReleaseDate = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "shutdownDate":
			value, ok := decodeManagementProviderCustomModelNullableString(raw, false)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.ShutdownDate = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "contextWindowTokens":
			value, ok := decodeManagementProviderCustomModelNullableInt(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.ContextWindowTokens = managementprovidermodels.OptionalInt{Set: true, Value: value}
		case "maxInputTokens":
			value, ok := decodeManagementProviderCustomModelNullableInt(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.MaxInputTokens = managementprovidermodels.OptionalInt{Set: true, Value: value}
		case "maxOutputTokens":
			value, ok := decodeManagementProviderCustomModelNullableInt(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.MaxOutputTokens = managementprovidermodels.OptionalInt{Set: true, Value: value}
		case "inputUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.InputUSDPer1M = value
		case "outputUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.OutputUSDPer1M = value
		case "cachedInputUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.CachedInputUSDPer1M = value
		case "cacheWriteUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.CacheWriteUSDPer1M = value
		case "cacheWrite1hUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.CacheWrite1hUSDPer1M = value
		case "imageInputUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.ImageInputUSDPer1M = value
		case "imageOutputUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.ImageOutputUSDPer1M = value
		case "audioInputUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.AudioInputUSDPer1M = value
		case "audioOutputUsdPer1M":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.AudioOutputUSDPer1M = value
		case "outputUsdPerImage":
			value, ok := decodeManagementProviderCustomModelOptionalFloat(raw)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.OutputUSDPerImage = value
		case "pricingNotes":
			value, ok := decodeManagementProviderCustomModelNullableString(raw, true)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.PricingNotes = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "capabilityNotes":
			value, ok := decodeManagementProviderCustomModelNullableString(raw, true)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.CapabilityNotes = managementprovidermodels.OptionalString{Set: true, Value: value}
		case "notes":
			value, ok := decodeManagementProviderCustomModelNullableString(raw, true)
			if !ok {
				fields.Invalid = true
				continue
			}
			fields.Notes = managementprovidermodels.OptionalString{Set: true, Value: value}
		default:
			fields.Invalid = true
		}
	}
	return fields, true
}

func validManagementProviderModelPriceMap(value map[string]managementprovidermodels.ModelPriceSet) bool {
	for tier, prices := range value {
		name := strings.TrimSpace(tier)
		if name == "" || name == "default" || name == "standard" || len(name) > 64 {
			return false
		}
		for _, price := range []*float64{prices.InputUSDPer1M, prices.OutputUSDPer1M, prices.CachedInputUSDPer1M,
			prices.CacheWriteUSDPer1M, prices.CacheWrite1hUSDPer1M, prices.ImageInputUSDPer1M, prices.ImageOutputUSDPer1M,
			prices.AudioInputUSDPer1M, prices.AudioOutputUSDPer1M, prices.OutputUSDPerImage} {
			if price != nil && (*price < 0 || math.IsNaN(*price) || math.IsInf(*price, 0)) {
				return false
			}
		}
	}
	return true
}

func decodeManagementProviderModelPriceMap(raw json.RawMessage) (map[string]managementprovidermodels.ModelPriceSet, bool) {
	var encoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil || encoded == nil {
		return nil, false
	}
	value := make(map[string]managementprovidermodels.ModelPriceSet, len(encoded))
	for tier, priceRaw := range encoded {
		decoder := json.NewDecoder(bytes.NewReader(priceRaw))
		decoder.DisallowUnknownFields()
		var prices managementprovidermodels.ModelPriceSet
		if err := decoder.Decode(&prices); err != nil {
			return nil, false
		}
		var extra struct{}
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, false
		}
		value[tier] = prices
	}
	return value, validManagementProviderModelPriceMap(value)
}

func decodeManagementProviderCustomModelRequiredString(raw json.RawMessage) (string, bool) {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", false
	}
	return *value, true
}

func decodeManagementProviderCustomModelBool(raw json.RawMessage) (bool, bool) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

func decodeManagementProviderCustomModelNullableString(raw json.RawMessage, allowEmptyString bool) (string, bool) {
	if string(raw) == "null" {
		return "", true
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", false
	}
	if !allowEmptyString && strings.TrimSpace(*value) == "" {
		return "", false
	}
	return *value, true
}

func decodeManagementProviderCustomModelStringList(raw json.RawMessage) ([]string, bool) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

func decodeManagementProviderCustomModelNullableInt(raw json.RawMessage) (*int, bool) {
	if string(raw) == "null" {
		return nil, true
	}
	var value *float64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil || math.Trunc(*value) != *value {
		return nil, false
	}
	if *value < math.MinInt32 || *value > math.MaxInt32 {
		return nil, false
	}
	output := int(*value)
	return &output, true
}

func decodeManagementProviderCustomModelOptionalFloat(raw json.RawMessage) (managementprovidermodels.OptionalFloat, bool) {
	if string(raw) == "null" {
		return managementprovidermodels.OptionalFloat{Set: true}, true
	}
	var value *float64
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return managementprovidermodels.OptionalFloat{}, false
	}
	return managementprovidermodels.OptionalFloat{Set: true, Value: value}, true
}

func writeManagementProviderCustomModelBodyError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeMessageError(w, http.StatusRequestEntityTooLarge, "请求体过大")
		return
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return
	}
	writeMessageError(w, http.StatusBadRequest, "自定义模型参数无效")
}
