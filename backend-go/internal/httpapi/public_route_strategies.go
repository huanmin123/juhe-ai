package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/publicroutestrategies"
)

type publicRouteStrategyService interface {
	List(r *http.Request, input publicroutestrategies.ListInput) (publicroutestrategies.RouteStrategyListResponse, error)
	Add(r *http.Request, input publicroutestrategies.AddInput) (publicroutestrategies.RouteStrategyResponse, error)
	Update(r *http.Request, input publicroutestrategies.UpdateInput) (publicroutestrategies.RouteStrategyResponse, error)
	Delete(r *http.Request, input publicroutestrategies.DeleteInput) (publicroutestrategies.RouteStrategyResponse, error)
}

type publicRouteStrategyServiceAdapter struct {
	service *publicroutestrategies.Service
}

func (s publicRouteStrategyServiceAdapter) List(r *http.Request, input publicroutestrategies.ListInput) (publicroutestrategies.RouteStrategyListResponse, error) {
	return s.service.List(r.Context(), input)
}

func (s publicRouteStrategyServiceAdapter) Add(r *http.Request, input publicroutestrategies.AddInput) (publicroutestrategies.RouteStrategyResponse, error) {
	return s.service.Add(r.Context(), input)
}

func (s publicRouteStrategyServiceAdapter) Update(r *http.Request, input publicroutestrategies.UpdateInput) (publicroutestrategies.RouteStrategyResponse, error) {
	return s.service.Update(r.Context(), input)
}

func (s publicRouteStrategyServiceAdapter) Delete(r *http.Request, input publicroutestrategies.DeleteInput) (publicroutestrategies.RouteStrategyResponse, error) {
	return s.service.Delete(r.Context(), input)
}

func NewPublicRouteStrategyHandlers(service *publicroutestrategies.Service) map[string]http.Handler {
	return newPublicRouteStrategyHandlers(publicRouteStrategyServiceAdapter{service: service})
}

func newPublicRouteStrategyHandlers(service publicRouteStrategyService) map[string]http.Handler {
	handler := publicRouteStrategyHandler{service: service}
	return map[string]http.Handler{
		"route-strategy-list":   http.HandlerFunc(handler.list),
		"route-strategy-add":    http.HandlerFunc(handler.add),
		"route-strategy-update": http.HandlerFunc(handler.update),
		"route-strategy-delete": http.HandlerFunc(handler.delete),
	}
}

type publicRouteStrategyHandler struct {
	service publicRouteStrategyService
}

func (h publicRouteStrategyHandler) list(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicRouteStrategyListQuery(r.URL.Query())
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicRouteStrategyList(input))
		return
	}
	response, err := h.service.List(r, input)
	if err != nil {
		writePublicRouteStrategyServiceError(w, err, "路由策略列表读取失败", "list")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicRouteStrategyHandler) add(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicRouteStrategyAddBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusCreated, mockPublicRouteStrategyResponse("mock", input.TargetUsername, publicroutestrategies.RouteStrategySummary{
			ID:                  "mock_route_strategy_public",
			Name:                input.Name,
			Mode:                publicRouteStrategyDefaultString(input.Mode, publicroutestrategies.ModeNormal),
			Status:              publicRouteStrategyDefaultString(input.Status, publicroutestrategies.StatusActive),
			IsDefault:           false,
			NormalRoutingConfig: &publicroutestrategies.NormalRoutingConfig{SchedulingPreference: "cost_first"},
			GroupBindings:       mockPublicRouteStrategyBindings(input.GroupBindings),
			APIKeyCount:         0,
			CreatedAt:           "2026-01-01T00:00:00Z",
			UpdatedAt:           "2026-01-01T00:00:00Z",
		}))
		return
	}
	response, err := h.service.Add(r, input)
	if err != nil {
		writePublicRouteStrategyServiceError(w, err, "路由策略新增失败", "add")
		return
	}
	writeData(w, http.StatusCreated, response)
}

func (h publicRouteStrategyHandler) update(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicRouteStrategyUpdateBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicRouteStrategyResponse("mock", publicRouteStrategyStringValue(input.TargetUsername), publicroutestrategies.RouteStrategySummary{
			ID:                  input.RouteStrategyID,
			Name:                publicRouteStrategyStringPtrValue(input.Name, "公开接口策略路由"),
			Mode:                publicRouteStrategyStringPtrValue(input.Mode, publicroutestrategies.ModeNormal),
			Status:              publicRouteStrategyStringPtrValue(input.Status, publicroutestrategies.StatusActive),
			IsDefault:           false,
			NormalRoutingConfig: &publicroutestrategies.NormalRoutingConfig{SchedulingPreference: "cost_first"},
			GroupBindings:       mockPublicRouteStrategyBindings(input.GroupBindings.Value()),
			APIKeyCount:         0,
			CreatedAt:           "2026-01-01T00:00:00Z",
			UpdatedAt:           "2026-01-01T00:00:00Z",
		}))
		return
	}
	response, err := h.service.Update(r, input)
	if err != nil {
		writePublicRouteStrategyServiceError(w, err, "路由策略修改失败", "update")
		return
	}
	writeData(w, http.StatusOK, response)
}

func (h publicRouteStrategyHandler) delete(w http.ResponseWriter, r *http.Request) {
	input, err := parsePublicRouteStrategyDeleteBody(r)
	if err != nil {
		writeMessageError(w, http.StatusBadRequest, err.Error())
		return
	}
	if isPublicAPITestToken(r) {
		writeData(w, http.StatusOK, mockPublicRouteStrategyResponse("mock", publicRouteStrategyStringValue(input.TargetUsername), publicroutestrategies.RouteStrategySummary{
			ID:            input.RouteStrategyID,
			Name:          "公开接口策略路由",
			Mode:          publicroutestrategies.ModeNormal,
			Status:        publicroutestrategies.StatusDisabled,
			IsDefault:     false,
			GroupBindings: mockPublicRouteStrategyBindings(nil),
			APIKeyCount:   0,
			CreatedAt:     "2026-01-01T00:00:00Z",
			UpdatedAt:     "2026-01-01T00:00:00Z",
		}))
		return
	}
	response, err := h.service.Delete(r, input)
	if err != nil {
		writePublicRouteStrategyServiceError(w, err, "路由策略删除失败", "delete")
		return
	}
	writeData(w, http.StatusOK, response)
}

func parsePublicRouteStrategyListQuery(values url.Values) (publicroutestrategies.ListInput, error) {
	if err := rejectUnknownQueryKeys(values, map[string]bool{
		"targetUsername": true,
		"keyword":        true,
		"mode":           true,
		"status":         true,
		"page":           true,
		"pageSize":       true,
	}); err != nil {
		return publicroutestrategies.ListInput{}, err
	}
	targetUsername, err := requiredQueryString(values, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicroutestrategies.ListInput{}, err
	}
	keyword, err := optionalQueryString(values, "keyword", 0, 120)
	if err != nil {
		return publicroutestrategies.ListInput{}, err
	}
	mode, err := optionalQueryEnum(values, "mode", []string{
		publicroutestrategies.ModeNormal,
		publicroutestrategies.ModeHybridSmart,
		publicroutestrategies.ModeWeighted,
		publicroutestrategies.ModeFailover,
		publicroutestrategies.ModeRoundRobin,
		"all",
	})
	if err != nil {
		return publicroutestrategies.ListInput{}, err
	}
	status, err := optionalQueryEnum(values, "status", []string{publicroutestrategies.StatusActive, publicroutestrategies.StatusDisabled, "all"})
	if err != nil {
		return publicroutestrategies.ListInput{}, err
	}
	page, err := optionalQueryInt(values, "page", 1, 0)
	if err != nil {
		return publicroutestrategies.ListInput{}, err
	}
	pageSize, err := optionalQueryInt(values, "pageSize", 1, 100)
	if err != nil {
		return publicroutestrategies.ListInput{}, err
	}
	return publicroutestrategies.ListInput{TargetUsername: targetUsername, Keyword: keyword, Mode: mode, Status: status, Page: page, PageSize: pageSize}, nil
}

func parsePublicRouteStrategyAddBody(r *http.Request) (publicroutestrategies.AddInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername":      true,
		"name":                true,
		"description":         true,
		"mode":                true,
		"status":              true,
		"groupBindings":       true,
		"normalRoutingConfig": true,
		"hybridRoutingConfig": true,
	})
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	targetUsername, err := requiredBodyString(body, "targetUsername", 2, 80, "targetUsername 不能为空")
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	name, err := requiredBodyString(body, "name", 1, 120, "策略路由名称不能为空")
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	description, err := optionalBodyNullableString(body, "description", 200)
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	mode, err := optionalBodyEnum(body, "mode", routeStrategyModes())
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	status, err := optionalBodyEnum(body, "status", []string{publicroutestrategies.StatusActive, publicroutestrategies.StatusDisabled})
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	groupBindings, err := requiredRouteStrategyGroupBindings(body)
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	normalConfig, err := optionalRouteStrategyConfigValue(body, "normalRoutingConfig")
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	hybridConfig, err := optionalRouteStrategyConfigValue(body, "hybridRoutingConfig")
	if err != nil {
		return publicroutestrategies.AddInput{}, err
	}
	return publicroutestrategies.AddInput{
		TargetUsername:      targetUsername,
		Name:                name,
		Description:         description,
		Mode:                mode,
		Status:              status,
		GroupBindings:       groupBindings,
		NormalRoutingConfig: normalConfig,
		HybridRoutingConfig: hybridConfig,
	}, nil
}

func parsePublicRouteStrategyUpdateBody(r *http.Request) (publicroutestrategies.UpdateInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername":      true,
		"routeStrategyId":     true,
		"name":                true,
		"description":         true,
		"mode":                true,
		"status":              true,
		"groupBindings":       true,
		"normalRoutingConfig": true,
		"hybridRoutingConfig": true,
	})
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	routeStrategyID, err := requiredBodyString(body, "routeStrategyId", 1, 120, "routeStrategyId 不能为空")
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	name, err := optionalBodyStringPtr(body, "name", 1, 120)
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	descriptionState, err := optionalBodyNullableStringState(body, "description", 200)
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	mode, err := optionalBodyEnumPtr(body, "mode", routeStrategyModes())
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	status, err := optionalBodyEnumPtr(body, "status", []string{publicroutestrategies.StatusActive, publicroutestrategies.StatusDisabled})
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	groupBindings, err := optionalRouteStrategyGroupBindings(body)
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	normalConfig, err := optionalRouteStrategyConfigValue(body, "normalRoutingConfig")
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	hybridConfig, err := optionalRouteStrategyConfigValue(body, "hybridRoutingConfig")
	if err != nil {
		return publicroutestrategies.UpdateInput{}, err
	}
	if name == nil && !descriptionState.Set() && mode == nil && status == nil && !groupBindings.Set() && !normalConfig.Set() && !hybridConfig.Set() {
		return publicroutestrategies.UpdateInput{}, fmt.Errorf("路由策略修改至少提供一个要修改的字段")
	}
	return publicroutestrategies.UpdateInput{
		TargetUsername:      targetUsername,
		RouteStrategyID:     routeStrategyID,
		Name:                name,
		Description:         publicroutestrategies.NewOptionalString(descriptionState.Value(), descriptionState.Set()),
		Mode:                mode,
		Status:              status,
		GroupBindings:       groupBindings,
		NormalRoutingConfig: normalConfig,
		HybridRoutingConfig: hybridConfig,
	}, nil
}

func parsePublicRouteStrategyDeleteBody(r *http.Request) (publicroutestrategies.DeleteInput, error) {
	body, err := publicGroupBodyMap(r, map[string]bool{
		"targetUsername":  true,
		"routeStrategyId": true,
	})
	if err != nil {
		return publicroutestrategies.DeleteInput{}, err
	}
	routeStrategyID, err := requiredBodyString(body, "routeStrategyId", 1, 120, "routeStrategyId 不能为空")
	if err != nil {
		return publicroutestrategies.DeleteInput{}, err
	}
	targetUsername, err := optionalBodyStringPtr(body, "targetUsername", 2, 80)
	if err != nil {
		return publicroutestrategies.DeleteInput{}, err
	}
	return publicroutestrategies.DeleteInput{TargetUsername: targetUsername, RouteStrategyID: routeStrategyID}, nil
}

func requiredRouteStrategyGroupBindings(body map[string]any) ([]publicroutestrategies.GroupBindingInput, error) {
	value, ok := body["groupBindings"]
	if !ok {
		return nil, fmt.Errorf("路由策略至少需要绑定一个分组")
	}
	return routeStrategyGroupBindingsFromValue(value)
}

func optionalRouteStrategyGroupBindings(body map[string]any) (publicroutestrategies.OptionalGroupBindings, error) {
	value, ok := body["groupBindings"]
	if !ok {
		return publicroutestrategies.NewOptionalGroupBindings(nil, false), nil
	}
	bindings, err := routeStrategyGroupBindingsFromValue(value)
	if err != nil {
		return publicroutestrategies.NewOptionalGroupBindings(nil, false), err
	}
	return publicroutestrategies.NewOptionalGroupBindings(bindings, true), nil
}

func routeStrategyGroupBindingsFromValue(value any) ([]publicroutestrategies.GroupBindingInput, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("groupBindings 必须是数组")
	}
	if len(items) < 1 || len(items) > 20 {
		return nil, fmt.Errorf("groupBindings 数量无效")
	}
	out := make([]publicroutestrategies.GroupBindingInput, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("策略路由分组绑定项无效")
		}
		if err := rejectUnknownBodyMapKeys(record, map[string]bool{
			"groupId":  true,
			"priority": true,
			"weight":   true,
			"status":   true,
		}); err != nil {
			return nil, err
		}
		groupID, err := requiredBodyString(record, "groupId", 1, 120, "策略路由分组不能为空")
		if err != nil {
			return nil, err
		}
		priority, err := optionalBodyInt(record, "priority", 1, 0)
		if err != nil {
			return nil, err
		}
		weight, err := optionalBodyInt(record, "weight", 1, 100)
		if err != nil {
			return nil, err
		}
		status, err := optionalBodyEnum(record, "status", []string{publicroutestrategies.StatusActive, publicroutestrategies.StatusDisabled})
		if err != nil {
			return nil, err
		}
		out = append(out, publicroutestrategies.GroupBindingInput{GroupID: groupID, Priority: priority, Weight: weight, Status: status})
	}
	return out, nil
}

func optionalRouteStrategyConfigValue(body map[string]any, key string) (publicroutestrategies.ConfigValue, error) {
	value, ok := body[key]
	if !ok {
		return publicroutestrategies.NewConfigValue(nil, false), nil
	}
	if value == nil {
		return publicroutestrategies.NewConfigValue(nil, true), nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return publicroutestrategies.NewConfigValue(nil, false), fmt.Errorf("%s 必须是对象", key)
	}
	return publicroutestrategies.NewConfigValue(record, true), nil
}

func optionalBodyInt(body map[string]any, key string, minValue int, maxValue int) (int, error) {
	value, ok := body[key]
	if !ok || value == nil {
		return 0, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s 必须是整数", key)
	}
	parsed, err := number.Int64()
	if err != nil || parsed < int64(minValue) || (maxValue > 0 && parsed > int64(maxValue)) {
		return 0, fmt.Errorf("%s 取值无效", key)
	}
	return int(parsed), nil
}

func optionalQueryEnum(values url.Values, key string, allowed []string) (string, error) {
	value, err := optionalQueryString(values, key, 1, 80)
	if err != nil || value == "" {
		return "", err
	}
	for _, item := range allowed {
		if value == item {
			return value, nil
		}
	}
	return "", fmt.Errorf("%s 取值无效", key)
}

func rejectUnknownBodyMapKeys(body map[string]any, allowed map[string]bool) error {
	for key := range body {
		if !allowed[key] {
			return fmt.Errorf("请求体包含未知字段：%s", key)
		}
	}
	return nil
}

func routeStrategyModes() []string {
	return []string{
		publicroutestrategies.ModeNormal,
		publicroutestrategies.ModeHybridSmart,
		publicroutestrategies.ModeWeighted,
		publicroutestrategies.ModeFailover,
		publicroutestrategies.ModeRoundRobin,
	}
}

func writePublicRouteStrategyServiceError(w http.ResponseWriter, err error, fallback string, operation string) {
	switch {
	case errors.Is(err, publicroutestrategies.ErrTargetNotFound):
		status := http.StatusBadRequest
		if operation == "list" {
			status = http.StatusNotFound
		}
		writeMessageError(w, status, "目标用户不存在："+publicRouteStrategyErrorDetail(err))
	case errors.Is(err, publicroutestrategies.ErrTargetDisabled):
		writeMessageError(w, http.StatusBadRequest, "目标用户已停用："+publicRouteStrategyErrorDetail(err))
	case errors.Is(err, publicroutestrategies.ErrRouteStrategyNotFound):
		writeMessageError(w, http.StatusNotFound, "路由策略不存在")
	case errors.Is(err, publicroutestrategies.ErrDuplicateRouteStrategyName):
		writeMessageError(w, http.StatusConflict, "策略路由名称已存在："+publicRouteStrategyErrorDetail(err))
	case errors.Is(err, publicroutestrategies.ErrDefaultRouteStrategyDelete):
		writeMessageError(w, http.StatusBadRequest, "默认策略路由不允许删除")
	case errors.Is(err, publicroutestrategies.ErrRouteStrategyAPIKeysInUse):
		writeMessageError(w, http.StatusBadRequest, "策略路由已被 "+publicRouteStrategyErrorDetail(err)+" 个 API Key 使用，请先解绑")
	case errors.Is(err, publicroutestrategies.ErrGroupBoundary),
		errors.Is(err, publicroutestrategies.ErrInvalidBinding),
		errors.Is(err, publicroutestrategies.ErrInvalidConfig):
		writeMessageError(w, http.StatusBadRequest, publicRouteStrategyErrorDetail(err))
	default:
		message := strings.TrimSpace(err.Error())
		if message == "" {
			message = fallback
		}
		writeMessageError(w, http.StatusBadRequest, message)
	}
}

func publicRouteStrategyErrorDetail(err error) string {
	text := err.Error()
	if index := strings.Index(text, ": "); index >= 0 && index+2 < len(text) {
		return strings.TrimSpace(text[index+2:])
	}
	return strings.TrimSpace(text)
}

func mockPublicRouteStrategyList(input publicroutestrategies.ListInput) publicroutestrategies.RouteStrategyListResponse {
	username := publicRouteStrategyDefaultString(input.TargetUsername, "huanmin")
	mode := input.Mode
	if mode == "" || mode == "all" {
		mode = publicroutestrategies.ModeNormal
	}
	status := input.Status
	if status == "" || status == "all" {
		status = publicroutestrategies.StatusActive
	}
	return publicroutestrategies.RouteStrategyListResponse{
		Source:      "mock",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Target: publicroutestrategies.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		Page:           max(1, input.Page),
		PageSize:       publicRouteStrategyDefaultInt(input.PageSize, 20),
		PageUpperBound: 1,
		HasMore:        false,
		Items: []publicroutestrategies.RouteStrategySummary{{
			ID:                  "mock_route_strategy_public",
			Name:                publicRouteStrategyDefaultString(input.Keyword, "公开接口策略路由"),
			Mode:                mode,
			Status:              status,
			IsDefault:           false,
			NormalRoutingConfig: &publicroutestrategies.NormalRoutingConfig{SchedulingPreference: "cost_first"},
			GroupBindings:       mockPublicRouteStrategyBindings(nil),
			APIKeyCount:         0,
			CreatedAt:           "2026-01-01T00:00:00Z",
			UpdatedAt:           "2026-01-01T00:00:00Z",
		}},
	}
}

func mockPublicRouteStrategyResponse(action string, username string, routeStrategy publicroutestrategies.RouteStrategySummary) publicroutestrategies.RouteStrategyResponse {
	username = publicRouteStrategyDefaultString(username, "huanmin")
	return publicroutestrategies.RouteStrategyResponse{
		Source:      "mock",
		GeneratedAt: "2026-01-01T00:00:00Z",
		Action:      action,
		Target: publicroutestrategies.Target{
			Username:        username,
			DisplayName:     username,
			SystemAccountID: "mock_system_account",
			Created:         false,
		},
		RouteStrategy: &routeStrategy,
	}
}

func mockPublicRouteStrategyBindings(inputs []publicroutestrategies.GroupBindingInput) []publicroutestrategies.GroupBindingSummary {
	if len(inputs) == 0 {
		inputs = []publicroutestrategies.GroupBindingInput{{GroupID: "mock_group_public", Priority: 1, Weight: 1, Status: publicroutestrategies.StatusActive}}
	}
	out := make([]publicroutestrategies.GroupBindingSummary, 0, len(inputs))
	for index, input := range inputs {
		priority := input.Priority
		if priority <= 0 {
			priority = index + 1
		}
		weight := input.Weight
		if weight <= 0 {
			weight = 1
		}
		status := publicRouteStrategyDefaultString(input.Status, publicroutestrategies.StatusActive)
		out = append(out, publicroutestrategies.GroupBindingSummary{
			ID:           fmt.Sprintf("mock_route_strategy_group_%d", index+1),
			GroupID:      publicRouteStrategyDefaultString(input.GroupID, "mock_group_public"),
			GroupName:    fmt.Sprintf("公开接口分组%d", index+1),
			ProviderCode: "mock_provider",
			Priority:     priority,
			Weight:       weight,
			Status:       status,
			GroupEnabled: true,
		})
	}
	return out
}

func publicRouteStrategyDefaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func publicRouteStrategyDefaultInt(value int, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func publicRouteStrategyStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func publicRouteStrategyStringPtrValue(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}
