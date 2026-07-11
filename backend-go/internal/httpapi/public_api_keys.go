package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/publicapikeys"
)

type publicAPIKeyService interface {
	List(r *http.Request, input publicapikeys.ListInput) (publicapikeys.APIKeyListResponse, error)
	Add(r *http.Request, input publicapikeys.AddInput) (publicapikeys.APIKeyResponse, error)
	Update(r *http.Request, input publicapikeys.UpdateInput) (publicapikeys.APIKeyResponse, error)
	Delete(r *http.Request, input publicapikeys.DeleteInput) (publicapikeys.APIKeyResponse, error)
}

type publicAPIKeyServiceAdapter struct {
	service *publicapikeys.Service
}

func (s publicAPIKeyServiceAdapter) List(r *http.Request, input publicapikeys.ListInput) (publicapikeys.APIKeyListResponse, error) {
	return s.service.List(r.Context(), input)
}

func (s publicAPIKeyServiceAdapter) Add(r *http.Request, input publicapikeys.AddInput) (publicapikeys.APIKeyResponse, error) {
	return s.service.Add(r.Context(), input)
}

func (s publicAPIKeyServiceAdapter) Update(r *http.Request, input publicapikeys.UpdateInput) (publicapikeys.APIKeyResponse, error) {
	return s.service.Update(r.Context(), input)
}

func (s publicAPIKeyServiceAdapter) Delete(r *http.Request, input publicapikeys.DeleteInput) (publicapikeys.APIKeyResponse, error) {
	return s.service.Delete(r.Context(), input)
}

func NewPublicAPIKeyHandlers(service *publicapikeys.Service) map[string]http.Handler {
	return newPublicAPIKeyHandlers(publicAPIKeyServiceAdapter{service: service})
}

func newPublicAPIKeyHandlers(service publicAPIKeyService) map[string]http.Handler {
	handler := publicAPIKeyHandler{service: service}
	return map[string]http.Handler{
		"api-key-list":   http.HandlerFunc(handler.list),
		"api-key-add":    http.HandlerFunc(handler.add),
		"api-key-update": http.HandlerFunc(handler.update),
		"api-key-delete": http.HandlerFunc(handler.delete),
	}
}

type publicAPIKeyHandler struct {
	service publicAPIKeyService
}

func (h publicAPIKeyHandler) list(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAPIKeyListQuery(r.URL.Query())
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicAPIKeyList(input))
		return
	}
	response, err := h.service.List(r, input)
	if err != nil {
		writePublicAPIKeyServiceError(w, err, "list")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicAPIKeyHandler) add(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAPIKeyAddBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusCreated, mockPublicAPIKeyResponse("mock", input.TargetUsername, publicapikeys.APIKeySummary{
			ID:                  "mock_api_key_public",
			Name:                input.Name,
			KeyPrefix:           "sk-mock-",
			KeySuffix:           "mocktail",
			Key:                 "sk-mock-public-api-key-secret",
			Status:              publicAPIKeyDefaultString(input.Status, publicapikeys.StatusActive),
			RouteStrategyID:     input.RouteStrategyID,
			RouteStrategyName:   "公开接口策略路由",
			RouteStrategyMode:   "normal",
			RouteStrategyStatus: publicapikeys.StatusActive,
		}))
		return
	}
	response, err := h.service.Add(r, input)
	if err != nil {
		writePublicAPIKeyServiceError(w, err, "add")
		return
	}
	writeData(w, http.StatusCreated, response)
}

func (h publicAPIKeyHandler) update(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAPIKeyUpdateBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicAPIKeyResponse("mock", publicAPIKeyStringValue(input.TargetUsername), publicapikeys.APIKeySummary{
			ID:                  input.APIKeyID,
			Name:                publicAPIKeyStringPtrValue(input.Name, "公开 API Key"),
			KeyPrefix:           "sk-mock-",
			KeySuffix:           "mocktail",
			Status:              publicAPIKeyStringPtrValue(input.Status, publicapikeys.StatusActive),
			RouteStrategyID:     publicAPIKeyStringPtrValue(input.RouteStrategyID, "mock_route_strategy_public"),
			RouteStrategyName:   "公开接口策略路由",
			RouteStrategyMode:   "normal",
			RouteStrategyStatus: publicapikeys.StatusActive,
		}))
		return
	}
	response, err := h.service.Update(r, input)
	if err != nil {
		writePublicAPIKeyServiceError(w, err, "update")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicAPIKeyHandler) delete(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAPIKeyDeleteBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicAPIKeyResponse("mock", publicAPIKeyStringValue(input.TargetUsername), publicapikeys.APIKeySummary{
			ID:                  input.APIKeyID,
			Name:                "公开 API Key",
			KeyPrefix:           "sk-mock-",
			KeySuffix:           "mocktail",
			Status:              publicapikeys.StatusDisabled,
			RouteStrategyID:     "mock_route_strategy_public",
			RouteStrategyName:   "公开接口策略路由",
			RouteStrategyMode:   "normal",
			RouteStrategyStatus: publicapikeys.StatusActive,
		}))
		return
	}
	response, err := h.service.Delete(r, input)
	if err != nil {
		writePublicAPIKeyServiceError(w, err, "delete")
		return
	}
	writeData(w, http.StatusOK, response)
}

func parsePublicAPIKeyListQuery(values url.Values) (publicapikeys.ListInput, error) {
	if err := rejectUnknownQueryKeys(values, map[string]bool{
		"targetUsername":  true,
		"routeStrategyId": true,
		"keyword":         true,
		"status":          true,
		"page":            true,
		"pageSize":        true,
	}); err != nil {
		return publicapikeys.ListInput{}, err
	}
	targetUsername, err := requiredQueryString(values, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicapikeys.ListInput{}, err
	}
	routeStrategyID, err := optionalQueryString(values, "routeStrategyId", 1, 120)
	if err != nil {
		return publicapikeys.ListInput{}, err
	}
	keyword, err := optionalQueryString(values, "keyword", 0, 120)
	if err != nil {
		return publicapikeys.ListInput{}, err
	}
	status, err := optionalQueryEnum(values, "status", []string{publicapikeys.StatusActive, publicapikeys.StatusDisabled, "all"})
	if err != nil {
		return publicapikeys.ListInput{}, err
	}
	page, err := optionalQueryInt(values, "page", 1, 0)
	if err != nil {
		return publicapikeys.ListInput{}, err
	}
	pageSize, err := optionalQueryInt(values, "pageSize", 1, 100)
	if err != nil {
		return publicapikeys.ListInput{}, err
	}
	return publicapikeys.ListInput{
		TargetUsername:  targetUsername,
		RouteStrategyID: routeStrategyID,
		Keyword:         keyword,
		Status:          status,
		Page:            page,
		PageSize:        pageSize,
	}, nil
}

func parsePublicAPIKeyAddBody(r *http.Request) (publicapikeys.AddInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername":       true,
		"name":                 true,
		"description":          true,
		"routeStrategyId":      true,
		"status":               true,
		"expiresAt":            true,
		"quotaLimits":          true,
		"availabilitySchedule": true,
	})
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	targetUsername, err := requiredBodyString(body, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	name, err := requiredBodyString(body, "name", 1, 120, "API Key 名称不能为空")
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	description, err := optionalBodyNullableString(body, "description", 200)
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	routeStrategyID, err := requiredBodyString(body, "routeStrategyId", 1, 120, "routeStrategyId 不能为空")
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	status, err := optionalBodyEnum(body, "status", []string{publicapikeys.StatusActive, publicapikeys.StatusDisabled})
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	expiresAtState, err := optionalBodyNullableStringState(body, "expiresAt", 120)
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	quotaLimits, err := optionalBodyJSONValue(body, "quotaLimits")
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	availabilitySchedule, err := optionalBodyJSONValue(body, "availabilitySchedule")
	if err != nil {
		return publicapikeys.AddInput{}, err
	}
	return publicapikeys.AddInput{
		TargetUsername:       targetUsername,
		Name:                 name,
		Description:          description,
		RouteStrategyID:      routeStrategyID,
		Status:               status,
		ExpiresAt:            expiresAtState.Value(),
		QuotaLimits:          quotaLimits,
		AvailabilitySchedule: availabilitySchedule,
	}, nil
}

func parsePublicAPIKeyUpdateBody(r *http.Request) (publicapikeys.UpdateInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername":       true,
		"apiKeyId":             true,
		"name":                 true,
		"description":          true,
		"routeStrategyId":      true,
		"status":               true,
		"expiresAt":            true,
		"quotaLimits":          true,
		"availabilitySchedule": true,
	})
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	apiKeyID, err := requiredBodyString(body, "apiKeyId", 1, 120, "apiKeyId 不能为空")
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	name, err := optionalBodyStringPtr(body, "name", 1, 120)
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	descriptionState, err := optionalBodyNullableStringState(body, "description", 200)
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	routeStrategyID, err := optionalBodyStringPtr(body, "routeStrategyId", 1, 120)
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	status, err := optionalBodyEnumPtr(body, "status", []string{publicapikeys.StatusActive, publicapikeys.StatusDisabled})
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	expiresAtState, err := optionalBodyNullableStringState(body, "expiresAt", 120)
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	quotaLimits, err := optionalBodyJSONValue(body, "quotaLimits")
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	availabilitySchedule, err := optionalBodyJSONValue(body, "availabilitySchedule")
	if err != nil {
		return publicapikeys.UpdateInput{}, err
	}
	if name == nil && !descriptionState.Set() && routeStrategyID == nil && status == nil && !expiresAtState.Set() && !quotaLimits.Set() && !availabilitySchedule.Set() {
		return publicapikeys.UpdateInput{}, fmt.Errorf("API Key 修改至少提供一个要修改的字段")
	}
	return publicapikeys.UpdateInput{
		TargetUsername:       targetUsername,
		APIKeyID:             apiKeyID,
		Name:                 name,
		Description:          publicapikeys.NewOptionalString(descriptionState.Value(), descriptionState.Set()),
		RouteStrategyID:      routeStrategyID,
		Status:               status,
		ExpiresAt:            publicapikeys.NewOptionalString(expiresAtState.Value(), expiresAtState.Set()),
		QuotaLimits:          quotaLimits,
		AvailabilitySchedule: availabilitySchedule,
	}, nil
}

func parsePublicAPIKeyDeleteBody(r *http.Request) (publicapikeys.DeleteInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername": true,
		"apiKeyId":       true,
	})
	if err != nil {
		return publicapikeys.DeleteInput{}, err
	}
	apiKeyID, err := requiredBodyString(body, "apiKeyId", 1, 120, "apiKeyId 不能为空")
	if err != nil {
		return publicapikeys.DeleteInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicapikeys.DeleteInput{}, err
	}
	return publicapikeys.DeleteInput{TargetUsername: targetUsername, APIKeyID: apiKeyID}, nil
}

func optionalBodyJSONValue(body map[string]any, key string) (publicapikeys.JSONValue, error) {
	value, ok := body[key]
	if !ok {
		return publicapikeys.NewJSONValue(nil, false), nil
	}
	if value == nil {
		return publicapikeys.NewJSONValue(nil, true), nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return publicapikeys.NewJSONValue(nil, false), fmt.Errorf("%s 必须是对象", key)
	}
	return publicapikeys.NewJSONValue(record, true), nil
}

func writePublicAPIKeyServiceError(w http.ResponseWriter, err error, operation string) {
	switch {
	case errors.Is(err, publicapikeys.ErrTargetNotFound):
		status := http.StatusBadRequest
		if operation == "list" {
			status = http.StatusNotFound
		}
		writeMessageError(w, status, "目标用户不存在："+publicAPIKeyErrorDetail(err))
	case errors.Is(err, publicapikeys.ErrTargetDisabled):
		writeMessageError(w, http.StatusBadRequest, "目标用户已停用："+publicAPIKeyErrorDetail(err))
	case errors.Is(err, publicapikeys.ErrAPIKeyNotFound):
		writeMessageError(w, http.StatusNotFound, "API Key 不存在")
	case errors.Is(err, publicapikeys.ErrDuplicateAPIKeyName):
		writeMessageError(w, http.StatusConflict, "API Key 名称已存在："+publicAPIKeyErrorDetail(err))
	case errors.Is(err, publicapikeys.ErrRouteStrategyNotFound):
		writeMessageError(w, http.StatusBadRequest, "策略路由不存在："+publicAPIKeyErrorDetail(err))
	case errors.Is(err, publicapikeys.ErrRouteStrategyDisabled):
		writeMessageError(w, http.StatusBadRequest, "策略路由已停用："+publicAPIKeyErrorDetail(err))
	case errors.Is(err, publicapikeys.ErrDefaultAPIKeyDelete):
		writeMessageError(w, http.StatusBadRequest, "默认 API Key 不允许删除")
	case errors.Is(err, publicapikeys.ErrDefaultAPIKeyRouteStrategyChange):
		writeMessageError(w, http.StatusBadRequest, "默认 API Key 不允许更换策略路由")
	case errors.Is(err, publicapikeys.ErrDeleteTransactorRequired):
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	case errors.Is(err, publicapikeys.ErrInvalidExpiresAt),
		errors.Is(err, publicapikeys.ErrInvalidQuotaLimits),
		errors.Is(err, publicapikeys.ErrInvalidAvailabilitySchedule):
		writeMessageError(w, http.StatusBadRequest, publicAPIKeyErrorDetail(err))
	default:
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	}
}

func publicAPIKeyErrorDetail(err error) string {
	text := err.Error()
	if index := strings.Index(text, ": "); index >= 0 && index+2 < len(text) {
		return strings.TrimSpace(text[index+2:])
	}
	return strings.TrimSpace(text)
}

func mockPublicAPIKeyList(input publicapikeys.ListInput) publicapikeys.APIKeyListResponse {
	username := publicAPIKeyDefaultString(input.TargetUsername, "huanmin")
	status := input.Status
	if status == "" || status == "all" {
		status = publicapikeys.StatusActive
	}
	return publicapikeys.APIKeyListResponse{
		Source:      "mock",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Target: publicapikeys.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		Page:           max(1, input.Page),
		PageSize:       publicAPIKeyDefaultInt(input.PageSize, 50),
		PageUpperBound: 1,
		HasMore:        false,
		Items: []publicapikeys.APIKeySummary{{
			ID:                  "mock_api_key_public",
			Name:                publicAPIKeyDefaultString(input.Keyword, "公开 API Key"),
			KeyPrefix:           "sk-mock-",
			KeySuffix:           "mocktail",
			Status:              status,
			RouteStrategyID:     publicAPIKeyDefaultString(input.RouteStrategyID, "mock_route_strategy_public"),
			RouteStrategyName:   "公开接口策略路由",
			RouteStrategyMode:   "normal",
			RouteStrategyStatus: publicapikeys.StatusActive,
		}},
	}
}

func mockPublicAPIKeyResponse(action string, username string, apiKey publicapikeys.APIKeySummary) publicapikeys.APIKeyResponse {
	username = publicAPIKeyDefaultString(username, "huanmin")
	return publicapikeys.APIKeyResponse{
		Source:      "mock",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Action:      action,
		Target: publicapikeys.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		APIKey: &apiKey,
	}
}

func publicAPIKeyDefaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func publicAPIKeyDefaultInt(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func publicAPIKeyStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func publicAPIKeyStringPtrValue(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}
