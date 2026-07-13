package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/publicaccounts"
)

type publicAccountService interface {
	List(r *http.Request, input publicaccounts.ListInput) (publicaccounts.AccountListResponse, error)
	Add(r *http.Request, input publicaccounts.AddInput) (publicaccounts.AccountResponse, error)
	Update(r *http.Request, input publicaccounts.UpdateInput) (publicaccounts.AccountResponse, error)
	Delete(r *http.Request, input publicaccounts.DeleteInput) (publicaccounts.AccountResponse, error)
}

type publicAccountServiceAdapter struct {
	service *publicaccounts.Service
}

func (s publicAccountServiceAdapter) List(r *http.Request, input publicaccounts.ListInput) (publicaccounts.AccountListResponse, error) {
	return s.service.List(r.Context(), input)
}

func (s publicAccountServiceAdapter) Add(r *http.Request, input publicaccounts.AddInput) (publicaccounts.AccountResponse, error) {
	return s.service.Add(r.Context(), input)
}

func (s publicAccountServiceAdapter) Update(r *http.Request, input publicaccounts.UpdateInput) (publicaccounts.AccountResponse, error) {
	return s.service.Update(r.Context(), input)
}

func (s publicAccountServiceAdapter) Delete(r *http.Request, input publicaccounts.DeleteInput) (publicaccounts.AccountResponse, error) {
	return s.service.Delete(r.Context(), input)
}

func NewPublicAccountHandlers(service *publicaccounts.Service) map[string]http.Handler {
	return newPublicAccountHandlers(publicAccountServiceAdapter{service: service})
}

func newPublicAccountHandlers(service publicAccountService) map[string]http.Handler {
	handler := publicAccountHandler{service: service}
	return map[string]http.Handler{
		"account-list":   http.HandlerFunc(handler.list),
		"account-add":    http.HandlerFunc(handler.add),
		"account-update": http.HandlerFunc(handler.update),
		"account-delete": http.HandlerFunc(handler.delete),
	}
}

type publicAccountHandler struct {
	service publicAccountService
}

func (h publicAccountHandler) list(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAccountListQuery(r.URL.Query())
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicAccountList(input))
		return
	}
	response, err := h.service.List(r, input)
	if err != nil {
		writePublicAccountServiceError(w, err, "账号列表读取失败", "list")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicAccountHandler) add(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAccountAddBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusCreated, mockPublicAccountResponse("mock", input.TargetUsername, publicaccounts.AccountSummary{
			ID:                        "mock_account_public",
			Name:                      input.Name,
			ProviderCode:              input.ProviderCode,
			ProviderProtocolProfileID: input.ProviderProtocolProfileID,
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			Type:                      publicaccounts.AccountTypeAPIKey,
			ClientCompatibility:       publicaccounts.DefaultClientCompat,
			Status:                    mockPublicAccountAddStatus(input.Status),
			SupportedModels:           input.SupportedModels.Value(),
			HealthCheckEndpointFamily: publicAccountDefaultString(input.HealthCheckEndpointFamily, "responses"),
			BoundGroupID:              "mock_group_public",
			BoundGroupName:            input.TargetGroupName,
			Schedulable:               false,
			AvailabilitySchedule:      input.AvailabilitySchedule.Value(),
		}))
		return
	}
	response, err := h.service.Add(r, input)
	if err != nil {
		writePublicAccountServiceError(w, err, "账号新增失败", "add")
		return
	}
	writeData(w, http.StatusCreated, response)
}

func (h publicAccountHandler) update(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAccountUpdateBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicAccountResponse("mock", publicAccountStringValue(input.TargetUsername), publicaccounts.AccountSummary{
			ID:                        input.AccountID,
			Name:                      publicAccountStringPtrValue(input.Name, "公开账号"),
			ProviderCode:              publicAccountStringPtrValue(input.ProviderCode, "gpt"),
			ProviderProtocolProfileID: publicAccountStringPtrValue(input.ProviderProtocolProfileID, "profile_gpt_openai_v1"),
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			Type:                      publicaccounts.AccountTypeAPIKey,
			ClientCompatibility:       publicaccounts.DefaultClientCompat,
			Status:                    publicAccountStringPtrValue(input.Status, publicaccounts.StatusActive),
			SupportedModels:           input.SupportedModels.Value(),
			HealthCheckEndpointFamily: publicAccountStringPtrValue(input.HealthCheckEndpointFamily, "responses"),
			BoundGroupID:              "mock_group_public",
			BoundGroupName:            publicAccountStringPtrValue(input.TargetGroupName, "公开分组"),
			Schedulable:               publicAccountStringPtrValue(input.Status, publicaccounts.StatusActive) == publicaccounts.StatusActive,
			AvailabilitySchedule:      input.AvailabilitySchedule.Value(),
		}))
		return
	}
	response, err := h.service.Update(r, input)
	if err != nil {
		writePublicAccountServiceError(w, err, "账号修改失败", "update")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicAccountHandler) delete(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicAccountDeleteBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicAccountResponse("mock", publicAccountStringValue(input.TargetUsername), publicaccounts.AccountSummary{
			ID:                        input.AccountID,
			Name:                      "公开账号",
			ProviderCode:              publicAccountStringPtrValue(input.ProviderCode, "gpt"),
			ProviderProtocolProfileID: publicAccountStringPtrValue(input.ProviderProtocolProfileID, "profile_gpt_openai_v1"),
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			Type:                      publicaccounts.AccountTypeAPIKey,
			ClientCompatibility:       publicaccounts.DefaultClientCompat,
			Status:                    publicaccounts.StatusDisabled,
			HealthCheckEndpointFamily: "responses",
			BoundGroupID:              "mock_group_public",
			BoundGroupName:            publicAccountStringPtrValue(input.TargetGroupName, "公开分组"),
			Schedulable:               false,
		}))
		return
	}
	response, err := h.service.Delete(r, input)
	if err != nil {
		writePublicAccountServiceError(w, err, "账号删除失败", "delete")
		return
	}
	writeData(w, http.StatusOK, response)
}

func parsePublicAccountListQuery(values url.Values) (publicaccounts.ListInput, error) {
	if err := rejectUnknownQueryKeys(values, map[string]bool{
		"targetUsername":            true,
		"targetGroupName":           true,
		"providerCode":              true,
		"providerProtocolProfileId": true,
		"groupId":                   true,
		"keyword":                   true,
		"type":                      true,
		"status":                    true,
		"schedulable":               true,
		"page":                      true,
		"pageSize":                  true,
	}); err != nil {
		return publicaccounts.ListInput{}, err
	}
	targetUsername, err := requiredQueryString(values, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	targetGroupName, err := optionalQueryString(values, "targetGroupName", 1, 80)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	providerCode, err := optionalQueryString(values, "providerCode", 1, 60)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	profileID, err := optionalQueryString(values, "providerProtocolProfileId", 1, 120)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	groupID, err := optionalQueryString(values, "groupId", 1, 120)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	keyword, err := optionalQueryString(values, "keyword", 0, 120)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	accountType, err := optionalQueryString(values, "type", 0, 60)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	status, err := optionalQueryString(values, "status", 0, 200)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	schedulable, err := optionalQueryEnum(values, "schedulable", []string{"all", "enabled", "disabled", "cooling"})
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	page, err := optionalQueryInt(values, "page", 1, 0)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	pageSize, err := optionalQueryInt(values, "pageSize", 1, 100)
	if err != nil {
		return publicaccounts.ListInput{}, err
	}
	return publicaccounts.ListInput{
		TargetUsername:            targetUsername,
		TargetGroupName:           targetGroupName,
		ProviderCode:              providerCode,
		ProviderProtocolProfileID: profileID,
		GroupID:                   groupID,
		Keyword:                   keyword,
		Type:                      accountType,
		Status:                    status,
		Schedulable:               schedulable,
		Page:                      page,
		PageSize:                  pageSize,
	}, nil
}

func parsePublicAccountAddBody(r *http.Request) (publicaccounts.AddInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername":            true,
		"targetDisplayName":         true,
		"targetGroupName":           true,
		"providerCode":              true,
		"providerProtocolProfileId": true,
		"name":                      true,
		"type":                      true,
		"baseUrl":                   true,
		"apiKey":                    true,
		"supportedModels":           true,
		"healthCheckEndpointFamily": true,
		"status":                    true,
		"concurrencyLimit":          true,
		"priority":                  true,
		"availabilitySchedule":      true,
		"notes":                     true,
	})
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	targetUsername, err := requiredBodyString(body, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	targetDisplayName, err := optionalBodyString(body, "targetDisplayName", 1, 80)
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	targetGroupName, err := requiredBodyString(body, "targetGroupName", 1, 80, "targetGroupName 不能为空")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	providerCode, err := requiredBodyString(body, "providerCode", 1, 60, "供应商编码不能为空")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	profileID, err := requiredBodyString(body, "providerProtocolProfileId", 1, 120, "providerProtocolProfileId 不能为空")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	name, err := requiredBodyString(body, "name", 1, 120, "账号名称不能为空")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	accountType, err := requiredBodyEnum(body, "type", []string{publicaccounts.AccountTypeAPIKey}, "公开账号接口仅支持 API Key 账户")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	baseURL, err := requiredBodyString(body, "baseUrl", 1, 500, "baseUrl 不能为空")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	apiKey, err := requiredBodyString(body, "apiKey", 1, 1000, "apiKey 不能为空")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	supportedModels, err := optionalBodyStringListValue(body, "supportedModels", 1, 120, 500)
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	healthCheckEndpointFamily, err := optionalBodyEnum(body, "healthCheckEndpointFamily", []string{"chat_completions", "responses", "messages", "generate_content"})
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	status, err := optionalBodyEnum(body, "status", []string{publicaccounts.StatusActive, publicaccounts.StatusDisabled})
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	concurrencyLimit, err := optionalBodyIntPtr(body, "concurrencyLimit", 1, 100000)
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	priority, err := optionalBodyIntPtr(body, "priority", 0, 100000)
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	availabilitySchedule, err := optionalPublicAccountJSONValue(body, "availabilitySchedule")
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	notes, err := optionalBodyStringPtr(body, "notes", 0, 1000)
	if err != nil {
		return publicaccounts.AddInput{}, err
	}
	return publicaccounts.AddInput{
		TargetUsername:            targetUsername,
		TargetDisplayName:         targetDisplayName,
		TargetGroupName:           targetGroupName,
		ProviderCode:              providerCode,
		ProviderProtocolProfileID: profileID,
		Name:                      name,
		Type:                      accountType,
		BaseURL:                   baseURL,
		APIKey:                    apiKey,
		SupportedModels:           supportedModels,
		HealthCheckEndpointFamily: healthCheckEndpointFamily,
		Status:                    status,
		ConcurrencyLimit:          concurrencyLimit,
		Priority:                  priority,
		AvailabilitySchedule:      availabilitySchedule,
		Notes:                     notes,
	}, nil
}

func parsePublicAccountUpdateBody(r *http.Request) (publicaccounts.UpdateInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"accountId":                 true,
		"targetUsername":            true,
		"targetGroupName":           true,
		"providerCode":              true,
		"providerProtocolProfileId": true,
		"name":                      true,
		"type":                      true,
		"baseUrl":                   true,
		"apiKey":                    true,
		"supportedModels":           true,
		"healthCheckEndpointFamily": true,
		"status":                    true,
		"concurrencyLimit":          true,
		"priority":                  true,
		"availabilitySchedule":      true,
		"notes":                     true,
	})
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	accountID, err := requiredBodyString(body, "accountId", 1, 120, "accountId 不能为空")
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	targetGroupName, err := optionalBodyStringPtr(body, "targetGroupName", 1, 80)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	providerCode, err := optionalBodyStringPtr(body, "providerCode", 1, 60)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	profileID, err := optionalBodyStringPtr(body, "providerProtocolProfileId", 1, 120)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	name, err := optionalBodyStringPtr(body, "name", 1, 120)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	accountType, err := optionalBodyEnumPtr(body, "type", []string{publicaccounts.AccountTypeAPIKey})
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	baseURL, err := optionalBodyStringPtr(body, "baseUrl", 1, 500)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	apiKey, err := optionalBodyStringPtr(body, "apiKey", 1, 1000)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	supportedModels, err := optionalBodyStringListValue(body, "supportedModels", 1, 120, 500)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	healthCheckEndpointFamily, err := optionalBodyEnumPtr(body, "healthCheckEndpointFamily", []string{"chat_completions", "responses", "messages", "generate_content"})
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	status, err := optionalBodyEnumPtr(body, "status", []string{publicaccounts.StatusActive, publicaccounts.StatusDisabled})
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	concurrencyLimit, err := optionalBodyIntPtr(body, "concurrencyLimit", 1, 100000)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	priority, err := optionalBodyIntPtr(body, "priority", 0, 100000)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	availabilitySchedule, err := optionalPublicAccountJSONValue(body, "availabilitySchedule")
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	notesState, err := optionalBodyNullableStringState(body, "notes", 1000)
	if err != nil {
		return publicaccounts.UpdateInput{}, err
	}
	if name == nil && accountType == nil && baseURL == nil && apiKey == nil && !supportedModels.Set() && healthCheckEndpointFamily == nil &&
		status == nil && concurrencyLimit == nil && priority == nil && !availabilitySchedule.Set() && !notesState.Set() {
		return publicaccounts.UpdateInput{}, fmt.Errorf("账号修改至少提供一个要修改的字段")
	}
	return publicaccounts.UpdateInput{
		AccountID:                 accountID,
		TargetUsername:            targetUsername,
		TargetGroupName:           targetGroupName,
		ProviderCode:              providerCode,
		ProviderProtocolProfileID: profileID,
		Name:                      name,
		Type:                      accountType,
		BaseURL:                   baseURL,
		APIKey:                    apiKey,
		SupportedModels:           supportedModels,
		HealthCheckEndpointFamily: healthCheckEndpointFamily,
		Status:                    status,
		ConcurrencyLimit:          concurrencyLimit,
		Priority:                  priority,
		AvailabilitySchedule:      availabilitySchedule,
		Notes:                     publicaccounts.NewOptionalString(notesState.Value(), notesState.Set()),
	}, nil
}

func parsePublicAccountDeleteBody(r *http.Request) (publicaccounts.DeleteInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"accountId":                 true,
		"targetUsername":            true,
		"targetGroupName":           true,
		"providerCode":              true,
		"providerProtocolProfileId": true,
	})
	if err != nil {
		return publicaccounts.DeleteInput{}, err
	}
	accountID, err := requiredBodyString(body, "accountId", 1, 120, "accountId 不能为空")
	if err != nil {
		return publicaccounts.DeleteInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicaccounts.DeleteInput{}, err
	}
	targetGroupName, err := optionalBodyStringPtr(body, "targetGroupName", 1, 80)
	if err != nil {
		return publicaccounts.DeleteInput{}, err
	}
	providerCode, err := optionalBodyStringPtr(body, "providerCode", 1, 60)
	if err != nil {
		return publicaccounts.DeleteInput{}, err
	}
	profileID, err := optionalBodyStringPtr(body, "providerProtocolProfileId", 1, 120)
	if err != nil {
		return publicaccounts.DeleteInput{}, err
	}
	return publicaccounts.DeleteInput{
		AccountID:                 accountID,
		TargetUsername:            targetUsername,
		TargetGroupName:           targetGroupName,
		ProviderCode:              providerCode,
		ProviderProtocolProfileID: profileID,
	}, nil
}

func optionalPublicAccountJSONValue(body map[string]any, key string) (publicaccounts.JSONValue, error) {
	value, ok := body[key]
	if !ok {
		return publicaccounts.NewJSONValue(nil, false), nil
	}
	if value == nil {
		return publicaccounts.NewJSONValue(nil, true), nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return publicaccounts.NewJSONValue(nil, false), fmt.Errorf("%s 必须是对象", key)
	}
	return publicaccounts.NewJSONValue(record, true), nil
}

func optionalBodyStringList(body map[string]any, key string, minLen int, maxLen int, maxItems int) ([]string, error) {
	value, ok := body[key]
	if !ok {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 必须是数组", key)
	}
	if len(items) > maxItems {
		return nil, fmt.Errorf("%s 数量无效", key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s 项必须是字符串", key)
		}
		text = strings.TrimSpace(text)
		if text == "" || len([]rune(text)) < minLen || (maxLen > 0 && len([]rune(text)) > maxLen) {
			return nil, fmt.Errorf("%s 项长度无效", key)
		}
		out = append(out, text)
	}
	return out, nil
}

func optionalBodyStringListValue(body map[string]any, key string, minLen int, maxLen int, maxItems int) (publicaccounts.StringListValue, error) {
	if _, ok := body[key]; !ok {
		return publicaccounts.NewStringListValue(nil, false), nil
	}
	items, err := optionalBodyStringList(body, key, minLen, maxLen, maxItems)
	if err != nil {
		return publicaccounts.NewStringListValue(nil, false), err
	}
	return publicaccounts.NewStringListValue(items, true), nil
}

func optionalBodyIntPtr(body map[string]any, key string, minValue int, maxValue int) (*int, error) {
	if _, ok := body[key]; !ok {
		return nil, nil
	}
	value, ok := body[key]
	if !ok || value == nil {
		return nil, fmt.Errorf("%s 必须是整数", key)
	}
	number, ok := value.(json.Number)
	if !ok {
		return nil, fmt.Errorf("%s 必须是整数", key)
	}
	parsed, err := number.Int64()
	if err != nil || parsed < int64(minValue) || (maxValue > 0 && parsed > int64(maxValue)) {
		return nil, fmt.Errorf("%s 取值无效", key)
	}
	result := int(parsed)
	return &result, nil
}

func requiredBodyEnum(body map[string]any, key string, allowed []string, message string) (string, error) {
	value, err := optionalBodyEnum(body, key, allowed)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s", message)
	}
	return value, nil
}

func writePublicAccountServiceError(w http.ResponseWriter, err error, fallback string, operation string) {
	switch {
	case errors.Is(err, publicaccounts.ErrTargetNotFound):
		status := http.StatusBadRequest
		if operation == "list" {
			status = http.StatusNotFound
		}
		writeMessageError(w, status, "目标用户不存在："+publicAccountErrorDetail(err))
	case errors.Is(err, publicaccounts.ErrTargetDisabled):
		writeMessageError(w, http.StatusBadRequest, "目标用户已停用："+publicAccountErrorDetail(err))
	case errors.Is(err, publicaccounts.ErrAccountNotFound):
		writeMessageError(w, http.StatusNotFound, "账号不存在")
	case errors.Is(err, publicaccounts.ErrDuplicateAccountName):
		writeMessageError(w, http.StatusConflict, "账号已存在："+publicAccountErrorDetail(err))
	case errors.Is(err, publicaccounts.ErrProviderDisabled):
		writeMessageError(w, http.StatusBadRequest, "供应商已停用："+publicAccountErrorDetail(err))
	case errors.Is(err, publicaccounts.ErrProviderProfileNotFound):
		writeMessageError(w, http.StatusBadRequest, "供应商未配置协议档案："+publicAccountErrorDetail(err))
	case errors.Is(err, publicaccounts.ErrProviderProfileDisabled):
		writeMessageError(w, http.StatusBadRequest, "供应商协议档案已停用："+publicAccountErrorDetail(err))
	case errors.Is(err, publicaccounts.ErrUnsupportedAccountType):
		writeMessageError(w, http.StatusBadRequest, publicAccountErrorDetail(err))
	case errors.Is(err, publicaccounts.ErrTargetGroupRequired),
		errors.Is(err, publicaccounts.ErrGroupNotFound),
		errors.Is(err, publicaccounts.ErrGroupProviderMismatch),
		errors.Is(err, publicaccounts.ErrInvalidCredentials),
		errors.Is(err, publicaccounts.ErrInvalidBaseURL),
		errors.Is(err, publicaccounts.ErrInvalidAPIKey),
		errors.Is(err, publicaccounts.ErrInvalidSupportedModels),
		errors.Is(err, publicaccounts.ErrInvalidHealthCheckModel),
		errors.Is(err, publicaccounts.ErrInvalidHealthCheckEndpointFamily),
		errors.Is(err, publicaccounts.ErrInvalidAvailability),
		errors.Is(err, publicaccounts.ErrInvalidDispatchField),
		errors.Is(err, publicaccounts.ErrInvalidStatusTransition),
		errors.Is(err, publicaccounts.ErrCredentialCodecUnusable):
		writeMessageError(w, http.StatusBadRequest, publicAccountErrorDetail(err))
	default:
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = fallback
		}
		writeMessageError(w, http.StatusBadRequest, message)
	}
}

func publicAccountErrorDetail(err error) string {
	text := err.Error()
	if index := strings.Index(text, ": "); index >= 0 && index+2 < len(text) {
		return strings.TrimSpace(text[index+2:])
	}
	return strings.TrimSpace(text)
}

func mockPublicAccountList(input publicaccounts.ListInput) publicaccounts.AccountListResponse {
	username := publicAccountDefaultString(input.TargetUsername, "huanmin")
	concurrency := 20
	priority := 0
	status := publicAccountDefaultString(input.Status, publicaccounts.StatusActive)
	return publicaccounts.AccountListResponse{
		Source:      "mock",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Target: publicaccounts.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		Page:           max(1, input.Page),
		PageSize:       publicAccountDefaultInt(input.PageSize, 50),
		PageUpperBound: 1,
		HasMore:        false,
		Items: []publicaccounts.AccountSummary{{
			ID:                        "mock_account_public",
			Name:                      publicAccountDefaultString(input.Keyword, "公开账号"),
			ProviderCode:              publicAccountDefaultString(input.ProviderCode, "gpt"),
			ProviderProtocolProfileID: publicAccountDefaultString(input.ProviderProtocolProfileID, "profile_gpt_openai_v1"),
			ProtocolCode:              "openai",
			ProtocolVersion:           "v1",
			Type:                      publicAccountDefaultString(input.Type, publicaccounts.AccountTypeAPIKey),
			ClientCompatibility:       publicaccounts.DefaultClientCompat,
			Status:                    status,
			SupportedModels:           []string{"gpt-5.5"},
			HealthCheckEndpointFamily: "responses",
			BoundGroupID:              publicAccountDefaultString(input.GroupID, "mock_group_public"),
			BoundGroupName:            publicAccountDefaultString(input.TargetGroupName, "公开分组"),
			Schedulable:               status == publicaccounts.StatusActive,
			ConcurrencyLimit:          &concurrency,
			Priority:                  &priority,
		}},
	}
}

func mockPublicAccountResponse(action string, username string, account publicaccounts.AccountSummary) publicaccounts.AccountResponse {
	username = publicAccountDefaultString(username, "huanmin")
	return publicaccounts.AccountResponse{
		Source:      "mock",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Action:      action,
		Target: publicaccounts.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		Account: &account,
	}
}

func mockPublicAccountAddStatus(status string) string {
	if strings.TrimSpace(status) == publicaccounts.StatusDisabled {
		return publicaccounts.StatusDisabled
	}
	return publicaccounts.StatusPendingTest
}

func publicAccountDefaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) == "all" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func publicAccountDefaultInt(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func publicAccountStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func publicAccountStringPtrValue(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}
