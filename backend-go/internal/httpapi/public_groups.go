package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"juhe-ai/backend-go/internal/modules/publicgroups"
)

type publicGroupService interface {
	List(r *http.Request, input publicgroups.ListInput) (publicgroups.GroupListResponse, error)
	Add(r *http.Request, input publicgroups.AddInput) (publicgroups.GroupResponse, error)
	Update(r *http.Request, input publicgroups.UpdateInput) (publicgroups.GroupResponse, error)
	Delete(r *http.Request, input publicgroups.DeleteInput) (publicgroups.GroupResponse, error)
}

type publicGroupServiceAdapter struct {
	service *publicgroups.Service
}

func (s publicGroupServiceAdapter) List(r *http.Request, input publicgroups.ListInput) (publicgroups.GroupListResponse, error) {
	return s.service.List(r.Context(), input)
}

func (s publicGroupServiceAdapter) Add(r *http.Request, input publicgroups.AddInput) (publicgroups.GroupResponse, error) {
	return s.service.Add(r.Context(), input)
}

func (s publicGroupServiceAdapter) Update(r *http.Request, input publicgroups.UpdateInput) (publicgroups.GroupResponse, error) {
	return s.service.Update(r.Context(), input)
}

func (s publicGroupServiceAdapter) Delete(r *http.Request, input publicgroups.DeleteInput) (publicgroups.GroupResponse, error) {
	return s.service.Delete(r.Context(), input)
}

func NewPublicGroupHandlers(service *publicgroups.Service) map[string]http.Handler {
	return newPublicGroupHandlers(publicGroupServiceAdapter{service: service})
}

func newPublicGroupHandlers(service publicGroupService) map[string]http.Handler {
	handler := publicGroupHandler{service: service}
	return map[string]http.Handler{
		"group-list":   http.HandlerFunc(handler.list),
		"group-add":    http.HandlerFunc(handler.add),
		"group-update": http.HandlerFunc(handler.update),
		"group-delete": http.HandlerFunc(handler.delete),
	}
}

type publicGroupHandler struct {
	service publicGroupService
}

func (h publicGroupHandler) list(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicGroupListQuery(r.URL.Query())
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicGroupList(input))
		return
	}
	response, err := h.service.List(r, input)
	if err != nil {
		writePublicGroupServiceError(w, err, "分组列表读取失败")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicGroupHandler) add(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicGroupAddBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusCreated, mockPublicGroupResponse("mock", input.TargetUsername, publicgroups.GroupSummary{
			ID:           "mock_group_public",
			Name:         input.Name,
			ProviderCode: input.ProviderCode,
			Description:  input.Description,
			Enabled:      input.Enabled == nil || *input.Enabled,
			GroupType:    publicGroupDefaultString(input.GroupType, publicgroups.DefaultGroupType),
			IsDefault:    false,
		}))
		return
	}
	response, err := h.service.Add(r, input)
	if err != nil {
		writePublicGroupServiceError(w, err, "分组新增失败")
		return
	}
	writeData(w, http.StatusCreated, response)
}

func (h publicGroupHandler) update(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicGroupUpdateBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicGroupResponse("mock", publicGroupStringValue(input.TargetUsername), publicgroups.GroupSummary{
			ID:           input.GroupID,
			Name:         publicGroupStringPtrValue(input.Name, "公开分组"),
			ProviderCode: publicGroupStringPtrValue(input.ProviderCode, "gpt"),
			Description:  input.Description.Value(),
			Enabled:      input.Enabled == nil || *input.Enabled,
			GroupType:    publicGroupStringPtrValue(input.GroupType, publicgroups.DefaultGroupType),
			IsDefault:    false,
		}))
		return
	}
	response, err := h.service.Update(r, input)
	if err != nil {
		writePublicGroupServiceError(w, err, "分组修改失败")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicGroupHandler) delete(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicGroupDeleteBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicGroupResponse("mock", publicGroupStringValue(input.TargetUsername), publicgroups.GroupSummary{
			ID:           input.GroupID,
			Name:         "公开分组",
			ProviderCode: "gpt",
			Enabled:      true,
			GroupType:    publicgroups.DefaultGroupType,
			IsDefault:    false,
		}))
		return
	}
	response, err := h.service.Delete(r, input)
	if err != nil {
		writePublicGroupServiceError(w, err, "分组删除失败")
		return
	}
	writeData(w, http.StatusOK, response)
}

func parsePublicGroupListQuery(values url.Values) (publicgroups.ListInput, error) {
	if err := rejectUnknownQueryKeys(values, map[string]bool{
		"targetUsername": true,
		"providerCode":   true,
		"keyword":        true,
		"page":           true,
		"pageSize":       true,
	}); err != nil {
		return publicgroups.ListInput{}, err
	}
	targetUsername, err := requiredQueryString(values, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicgroups.ListInput{}, err
	}
	providerCode, err := optionalQueryString(values, "providerCode", 1, 60)
	if err != nil {
		return publicgroups.ListInput{}, err
	}
	keyword, err := optionalQueryString(values, "keyword", 0, 80)
	if err != nil {
		return publicgroups.ListInput{}, err
	}
	page, err := optionalQueryInt(values, "page", 1, 0)
	if err != nil {
		return publicgroups.ListInput{}, err
	}
	pageSize, err := optionalQueryInt(values, "pageSize", 1, 100)
	if err != nil {
		return publicgroups.ListInput{}, err
	}
	return publicgroups.ListInput{
		TargetUsername: targetUsername,
		ProviderCode:   providerCode,
		Keyword:        keyword,
		Page:           page,
		PageSize:       pageSize,
	}, nil
}

func parsePublicGroupAddBody(r *http.Request) (publicgroups.AddInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername":    true,
		"targetDisplayName": true,
		"name":              true,
		"providerCode":      true,
		"description":       true,
		"enabled":           true,
		"groupType":         true,
	})
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	targetUsername, err := requiredBodyString(body, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	targetDisplayName, err := optionalBodyString(body, "targetDisplayName", 1, 80)
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	name, err := requiredBodyString(body, "name", 1, 80, "分组名称不能为空")
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	providerCode, err := requiredBodyString(body, "providerCode", 1, 60, "供应商编码不能为空")
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	description, err := optionalBodyNullableString(body, "description", 500)
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	enabled, err := optionalBodyBool(body, "enabled")
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	groupType, err := optionalBodyEnum(body, "groupType", []string{publicgroups.DefaultGroupType, publicgroups.GroupTypeHighConcurrency})
	if err != nil {
		return publicgroups.AddInput{}, err
	}
	return publicgroups.AddInput{
		TargetUsername:    targetUsername,
		TargetDisplayName: targetDisplayName,
		Name:              name,
		ProviderCode:      providerCode,
		Description:       description,
		Enabled:           enabled,
		GroupType:         groupType,
	}, nil
}

func parsePublicGroupUpdateBody(r *http.Request) (publicgroups.UpdateInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername": true,
		"groupId":        true,
		"name":           true,
		"providerCode":   true,
		"description":    true,
		"enabled":        true,
		"groupType":      true,
	})
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	groupID, err := requiredBodyString(body, "groupId", 1, 120, "groupId 不能为空")
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	name, err := optionalBodyStringPtr(body, "name", 1, 80)
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	providerCode, err := optionalBodyStringPtr(body, "providerCode", 1, 60)
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	description, err := optionalBodyNullableStringState(body, "description", 500)
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	enabled, err := optionalBodyBool(body, "enabled")
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	groupType, err := optionalBodyEnumPtr(body, "groupType", []string{publicgroups.DefaultGroupType, publicgroups.GroupTypeHighConcurrency})
	if err != nil {
		return publicgroups.UpdateInput{}, err
	}
	if name == nil && providerCode == nil && !description.Set() && enabled == nil && groupType == nil {
		return publicgroups.UpdateInput{}, fmt.Errorf("分组修改至少提供一个要修改的字段")
	}
	return publicgroups.UpdateInput{
		TargetUsername: targetUsername,
		GroupID:        groupID,
		Name:           name,
		ProviderCode:   providerCode,
		Description:    description,
		Enabled:        enabled,
		GroupType:      groupType,
	}, nil
}

func parsePublicGroupDeleteBody(r *http.Request) (publicgroups.DeleteInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername": true,
		"groupId":        true,
	})
	if err != nil {
		return publicgroups.DeleteInput{}, err
	}
	groupID, err := requiredBodyString(body, "groupId", 1, 120, "groupId 不能为空")
	if err != nil {
		return publicgroups.DeleteInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicgroups.DeleteInput{}, err
	}
	return publicgroups.DeleteInput{TargetUsername: targetUsername, GroupID: groupID}, nil
}

func publicGroupBodyMap(r *http.Request, allowed map[string]bool) (map[string]any, error) {
	value, ok := PublicAPIRequestBodyFromRequest(r)
	if !ok || value == nil {
		return map[string]any{}, nil
	}
	body, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("请求体必须是对象")
	}
	for key := range body {
		if !allowed[key] {
			return nil, fmt.Errorf("请求体包含未知字段：%s", key)
		}
	}
	return body, nil
}

func rejectUnknownQueryKeys(values url.Values, allowed map[string]bool) error {
	for key := range values {
		if !allowed[key] {
			return fmt.Errorf("查询参数包含未知字段：%s", key)
		}
	}
	return nil
}

func requiredQueryString(values url.Values, key string, minLen int, maxLen int, message string) (string, error) {
	value, err := optionalQueryString(values, key, minLen, maxLen)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s", message)
	}
	return value, nil
}

func optionalQueryString(values url.Values, key string, minLen int, maxLen int) (string, error) {
	items, ok := values[key]
	if !ok {
		return "", nil
	}
	if len(items) != 1 {
		return "", fmt.Errorf("%s 参数无效", key)
	}
	value := strings.TrimSpace(items[0])
	if value == "" {
		if minLen > 0 {
			return "", fmt.Errorf("%s 参数无效", key)
		}
		return "", nil
	}
	if utf8.RuneCountInString(value) < minLen || (maxLen > 0 && utf8.RuneCountInString(value) > maxLen) {
		return "", fmt.Errorf("%s 参数无效", key)
	}
	return value, nil
}

func optionalQueryInt(values url.Values, key string, minValue int, maxValue int) (int, error) {
	raw, err := optionalQueryString(values, key, 1, 20)
	if err != nil || raw == "" {
		return 0, err
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || (maxValue > 0 && value > maxValue) {
		return 0, fmt.Errorf("%s 参数无效", key)
	}
	return value, nil
}

func requiredBodyString(body map[string]any, key string, minLen int, maxLen int, message string) (string, error) {
	value, err := optionalBodyString(body, key, minLen, maxLen)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s", message)
	}
	return value, nil
}

func optionalBodyString(body map[string]any, key string, minLen int, maxLen int) (string, error) {
	value, ok := body[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串", key)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		if minLen > 0 {
			return "", fmt.Errorf("%s 不能为空", key)
		}
		return "", nil
	}
	if utf8.RuneCountInString(text) < minLen || (maxLen > 0 && utf8.RuneCountInString(text) > maxLen) {
		return "", fmt.Errorf("%s 长度无效", key)
	}
	return text, nil
}

func optionalBodyStringPtr(body map[string]any, key string, minLen int, maxLen int) (*string, error) {
	if _, ok := body[key]; !ok {
		return nil, nil
	}
	value, err := optionalBodyString(body, key, minLen, maxLen)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, fmt.Errorf("%s 不能为空", key)
	}
	return &value, nil
}

func optionalBodyNullableString(body map[string]any, key string, maxLen int) (*string, error) {
	state, err := optionalBodyNullableStringState(body, key, maxLen)
	if err != nil || !state.Set() {
		return nil, err
	}
	return state.Value(), nil
}

func optionalBodyNullableStringState(body map[string]any, key string, maxLen int) (publicgroups.OptionalString, error) {
	value, ok := body[key]
	if !ok {
		return publicgroups.NewOptionalString(nil, false), nil
	}
	if value == nil {
		return publicgroups.NewOptionalString(nil, true), nil
	}
	text, ok := value.(string)
	if !ok {
		return publicgroups.NewOptionalString(nil, false), fmt.Errorf("%s 必须是字符串", key)
	}
	text = strings.TrimSpace(text)
	if maxLen > 0 && utf8.RuneCountInString(text) > maxLen {
		return publicgroups.NewOptionalString(nil, false), fmt.Errorf("%s 长度无效", key)
	}
	if text == "" {
		return publicgroups.NewOptionalString(nil, true), nil
	}
	return publicgroups.NewOptionalString(&text, true), nil
}

func optionalBodyBool(body map[string]any, key string) (*bool, error) {
	value, ok := body[key]
	if !ok || value == nil {
		return nil, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return nil, fmt.Errorf("%s 必须是布尔值", key)
	}
	return &boolean, nil
}

func optionalBodyEnum(body map[string]any, key string, allowed []string) (string, error) {
	value, ok := body[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s 必须是字符串", key)
	}
	text = strings.TrimSpace(text)
	for _, item := range allowed {
		if text == item {
			return text, nil
		}
	}
	return "", fmt.Errorf("%s 取值无效", key)
}

func optionalBodyEnumPtr(body map[string]any, key string, allowed []string) (*string, error) {
	if _, ok := body[key]; !ok {
		return nil, nil
	}
	value, err := optionalBodyEnum(body, key, allowed)
	if err != nil {
		return nil, err
	}
	if value == "" {
		return nil, fmt.Errorf("%s 取值无效", key)
	}
	return &value, nil
}

func writePublicGroupServiceError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, publicgroups.ErrTargetNotFound):
		writeMessageError(w, http.StatusNotFound, "目标用户不存在："+publicGroupErrorDetail(err))
	case errors.Is(err, publicgroups.ErrTargetDisabled):
		writeMessageError(w, http.StatusBadRequest, "目标用户已停用："+publicGroupErrorDetail(err))
	case errors.Is(err, publicgroups.ErrProviderNotFound):
		writeMessageError(w, http.StatusBadRequest, "不支持的供应商："+publicGroupErrorDetail(err))
	case errors.Is(err, publicgroups.ErrProviderDisabled):
		writeMessageError(w, http.StatusBadRequest, "供应商已停用："+publicGroupErrorDetail(err))
	case errors.Is(err, publicgroups.ErrGroupNotFound):
		writeMessageError(w, http.StatusNotFound, "分组不存在")
	case errors.Is(err, publicgroups.ErrDuplicateGroupName):
		writeMessageError(w, http.StatusConflict, "同一供应商下分组名称已存在："+publicGroupErrorDetail(err))
	case errors.Is(err, publicgroups.ErrDefaultGroupReadonly):
		writeMessageError(w, http.StatusBadRequest, "默认分组不允许修改")
	case errors.Is(err, publicgroups.ErrDefaultGroupDelete):
		writeMessageError(w, http.StatusBadRequest, "默认分组不能删除")
	case errors.Is(err, publicgroups.ErrGroupProviderHasAccount):
		writeMessageError(w, http.StatusBadRequest, "已有账户的分组不允许修改供应商")
	case errors.Is(err, publicgroups.ErrRouteStrategyWouldLose):
		writeMessageError(w, http.StatusBadRequest, publicGroupErrorDetail(err))
	default:
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = fallback
		}
		writeMessageError(w, http.StatusBadRequest, message)
	}
}

func publicGroupErrorDetail(err error) string {
	text := err.Error()
	if index := strings.Index(text, ": "); index >= 0 && index+2 < len(text) {
		return strings.TrimSpace(text[index+2:])
	}
	return strings.TrimSpace(text)
}

func isPublicAPITestToken(r *http.Request) bool {
	authContext, ok := PublicAPIAuthContextFromRequest(r)
	return ok && authContext.IsTestToken
}

func mockPublicGroupList(input publicgroups.ListInput) publicgroups.GroupListResponse {
	username := publicGroupDefaultString(input.TargetUsername, "huanmin")
	return publicgroups.GroupListResponse{
		Source:      "stats",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Target: publicgroups.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		Page:           max(1, input.Page),
		PageSize:       publicGroupDefaultInt(input.PageSize, 50),
		PageUpperBound: 1,
		HasMore:        false,
		Items: []publicgroups.GroupSummary{{
			ID:           "mock_group_public",
			Name:         "公开分组",
			ProviderCode: publicGroupDefaultString(input.ProviderCode, "gpt"),
			Enabled:      true,
			GroupType:    publicgroups.DefaultGroupType,
			IsDefault:    false,
		}},
	}
}

func mockPublicGroupResponse(action string, username string, group publicgroups.GroupSummary) publicgroups.GroupResponse {
	username = publicGroupDefaultString(username, "huanmin")
	return publicgroups.GroupResponse{
		Source:      "stats",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Action:      action,
		Target: publicgroups.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		Group: &group,
	}
}

func publicGroupDefaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func publicGroupDefaultInt(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func publicGroupStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func publicGroupStringPtrValue(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}
