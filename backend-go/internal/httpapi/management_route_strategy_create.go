package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/store/port"
)

type managementRouteStrategyCreateService interface {
	Create(
		r *http.Request,
		input managementroutestrategies.CreateInput,
	) (managementroutestrategies.DetailResult, error)
}

type managementRouteStrategyCreateServiceAdapter struct {
	service *managementroutestrategies.Service
}

func (s managementRouteStrategyCreateServiceAdapter) Create(
	r *http.Request,
	input managementroutestrategies.CreateInput,
) (managementroutestrategies.DetailResult, error) {
	return s.service.Create(r.Context(), input)
}

func NewManagementRouteStrategyCreateHandler(
	service *managementroutestrategies.Service,
) http.Handler {
	return newManagementRouteStrategyCreateHandler(
		managementRouteStrategyCreateServiceFrom(service),
		managementRouteStrategyScopeAdmin,
		managementOperationLogOptions{},
	)
}

func NewManagementMyRouteStrategyCreateHandler(
	service *managementroutestrategies.Service,
) http.Handler {
	return newManagementRouteStrategyCreateHandler(
		managementRouteStrategyCreateServiceFrom(service),
		managementRouteStrategyScopeSelf,
		managementOperationLogOptions{},
	)
}

func NewManagementRouteStrategyCreateHandlerWithOperationLog(
	service *managementroutestrategies.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementRouteStrategyCreateHandler(
		managementRouteStrategyCreateServiceFrom(service),
		managementRouteStrategyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyRouteStrategyCreateHandlerWithOperationLog(
	service *managementroutestrategies.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementRouteStrategyCreateHandler(
		managementRouteStrategyCreateServiceFrom(service),
		managementRouteStrategyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementRouteStrategyCreateServiceFrom(
	service *managementroutestrategies.Service,
) managementRouteStrategyCreateService {
	if service == nil {
		return nil
	}
	return managementRouteStrategyCreateServiceAdapter{service: service}
}

type managementRouteStrategyCreatePayload struct {
	Name                string
	Description         *string
	Mode                string
	ModeSet             bool
	Status              string
	StatusSet           bool
	GroupBindings       []managementroutestrategies.CreateGroupBindingInput
	NormalRoutingConfig managementroutestrategies.ConfigInput
	HybridRoutingConfig managementroutestrategies.ConfigInput
}

func managementRouteStrategyCreateJSONBodyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentEncoding := strings.TrimSpace(r.Header.Get("Content-Encoding"))
		if contentEncoding != "" && !strings.EqualFold(contentEncoding, "identity") {
			writeMessageError(w, http.StatusUnsupportedMediaType, "请求体无效")
			return
		}
		managementGroupCreateJSONBodyMiddleware(next).ServeHTTP(w, r)
	})
}

func newManagementRouteStrategyCreateHandler(
	service managementRouteStrategyCreateService,
	scope managementRouteStrategyOptionScope,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if scope == managementRouteStrategyScopeAdmin &&
			!managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		ownerSystemAccountID, includeOwner, validScope :=
			managementRouteStrategyCreateScope(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		payload, ok := decodeManagementRouteStrategyCreatePayload(w, r)
		if !ok {
			return
		}
		result, err := service.Create(r, managementroutestrategies.CreateInput{
			SystemAccountID:            ownerSystemAccountID,
			IncludeSystemAccountFields: includeOwner,
			Name:                       payload.Name,
			Description:                payload.Description,
			Mode:                       payload.Mode,
			ModeSet:                    payload.ModeSet,
			Status:                     payload.Status,
			StatusSet:                  payload.StatusSet,
			GroupBindings:              payload.GroupBindings,
			NormalRoutingConfig:        payload.NormalRoutingConfig,
			HybridRoutingConfig:        payload.HybridRoutingConfig,
		})
		if !writeManagementRouteStrategyCreateError(w, err) {
			return
		}
		recordManagementRouteStrategyCreateOperationLog(
			r,
			authContext,
			scope,
			ownerSystemAccountID,
			result,
			logOptions,
		)
		writeData(w, http.StatusCreated, result)
	})
}

func managementRouteStrategyCreateScope(
	authContext managementauth.Context,
	values url.Values,
	scope managementRouteStrategyOptionScope,
) (string, bool, bool) {
	ownerSystemAccountID := strings.TrimSpace(authContext.SystemAccountID)
	switch scope {
	case managementRouteStrategyScopeSelf:
		return ownerSystemAccountID, false, true
	case managementRouteStrategyScopeAdmin:
		rawValues, exists := values["systemAccountId"]
		if !exists {
			return ownerSystemAccountID, true, true
		}
		if len(rawValues) != 1 {
			return "", false, false
		}
		selectedSystemAccountID := strings.TrimSpace(rawValues[0])
		if selectedSystemAccountID == "" {
			return "", false, false
		}
		if selectedSystemAccountID == "all" {
			return ownerSystemAccountID, true, true
		}
		return selectedSystemAccountID, true, true
	default:
		return "", false, false
	}
}

func decodeManagementRouteStrategyCreatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementRouteStrategyCreatePayload, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, managementGroupCreateMaxBodyBytes))
	if err != nil {
		writeManagementGroupCreateBodyError(w, err)
		return managementRouteStrategyCreatePayload{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementRouteStrategyCreatePayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementRouteStrategyCreatePayload{}, false
	}
	record, ok := value.(map[string]any)
	if !ok || record == nil {
		writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
		return managementRouteStrategyCreatePayload{}, false
	}
	if !managementRouteStrategyAllowedKeys(record,
		"name",
		"description",
		"mode",
		"status",
		"groupBindings",
		"normalRoutingConfig",
		"hybridRoutingConfig",
	) {
		writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
		return managementRouteStrategyCreatePayload{}, false
	}

	name, ok := managementRouteStrategyRequiredText(record["name"])
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "请填写策略路由名称")
		return managementRouteStrategyCreatePayload{}, false
	}
	payload := managementRouteStrategyCreatePayload{Name: name}
	if description, exists := record["description"]; exists {
		switch typed := description.(type) {
		case nil:
		case string:
			text := managementRouteStrategyTrimText(typed)
			if len(utf16.Encode([]rune(text))) > 200 {
				writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
				return managementRouteStrategyCreatePayload{}, false
			}
			payload.Description = &text
		default:
			writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
			return managementRouteStrategyCreatePayload{}, false
		}
	}
	if mode, exists := record["mode"]; exists {
		text, ok := mode.(string)
		if !ok || !managementRouteStrategyStringIn(
			text,
			"normal",
			"hybrid_smart",
			"weighted",
			"failover",
			"round_robin",
		) {
			writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
			return managementRouteStrategyCreatePayload{}, false
		}
		payload.Mode = text
		payload.ModeSet = true
	}
	if status, exists := record["status"]; exists {
		text, ok := status.(string)
		if !ok || !managementRouteStrategyStringIn(text, "active", "disabled") {
			writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
			return managementRouteStrategyCreatePayload{}, false
		}
		payload.Status = text
		payload.StatusSet = true
	}
	bindings, ok := managementRouteStrategyCreateBindings(record["groupBindings"])
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "策略路由至少需要绑定一个分组")
		return managementRouteStrategyCreatePayload{}, false
	}
	payload.GroupBindings = bindings

	normalValue, normalSet := record["normalRoutingConfig"]
	if normalSet && !managementRouteStrategyNormalConfigShape(normalValue) {
		writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
		return managementRouteStrategyCreatePayload{}, false
	}
	hybridValue, hybridSet := record["hybridRoutingConfig"]
	if hybridSet && !managementRouteStrategyHybridConfigShape(hybridValue) {
		writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
		return managementRouteStrategyCreatePayload{}, false
	}
	payload.NormalRoutingConfig = managementroutestrategies.NewConfigInput(
		normalValue,
		normalSet,
	)
	payload.HybridRoutingConfig = managementroutestrategies.NewConfigInput(
		hybridValue,
		hybridSet,
	)
	return payload, true
}

func managementRouteStrategyCreateBindings(
	value any,
) ([]managementroutestrategies.CreateGroupBindingInput, bool) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 || len(items) > 20 {
		return nil, false
	}
	bindings := make([]managementroutestrategies.CreateGroupBindingInput, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok || record == nil || !managementRouteStrategyAllowedKeys(
			record,
			"groupId",
			"priority",
			"weight",
			"status",
		) {
			return nil, false
		}
		groupID, ok := managementRouteStrategyRequiredText(record["groupId"])
		if !ok {
			return nil, false
		}
		binding := managementroutestrategies.CreateGroupBindingInput{GroupID: groupID}
		if priority, exists := record["priority"]; exists {
			value, ok := managementRouteStrategyInteger(priority, 1, math.MaxInt)
			if !ok {
				return nil, false
			}
			binding.Priority = value
			binding.PrioritySet = true
		}
		if weight, exists := record["weight"]; exists {
			value, ok := managementRouteStrategyInteger(weight, 1, 100)
			if !ok {
				return nil, false
			}
			binding.Weight = value
			binding.WeightSet = true
		}
		if status, exists := record["status"]; exists {
			text, ok := status.(string)
			if !ok || !managementRouteStrategyStringIn(text, "active", "disabled") {
				return nil, false
			}
			binding.Status = text
			binding.StatusSet = true
		}
		bindings = append(bindings, binding)
	}
	return bindings, true
}

func managementRouteStrategyNormalConfigShape(value any) bool {
	if value == nil {
		return true
	}
	record, ok := value.(map[string]any)
	if !ok || record == nil || !managementRouteStrategyAllowedKeys(
		record,
		"schedulingPreference",
		"speedFirstConfig",
	) {
		return false
	}
	if preference, exists := record["schedulingPreference"]; exists {
		if _, ok := preference.(string); !ok {
			return false
		}
	}
	speed, exists := record["speedFirstConfig"]
	if !exists {
		return true
	}
	speedRecord, ok := speed.(map[string]any)
	if !ok || speedRecord == nil || !managementRouteStrategyAllowedKeys(
		speedRecord,
		"firstByteThresholdMs",
		"slowTriggerCount",
		"slowWindowSeconds",
		"recoverySuccessCount",
		"probeIntervalSeconds",
		"degradedTtlSeconds",
		"maxFirstByteRetriesPerRequest",
	) {
		return false
	}
	return managementRouteStrategyOptionalIntegerShape(speedRecord,
		"firstByteThresholdMs",
		"slowTriggerCount",
		"slowWindowSeconds",
		"recoverySuccessCount",
		"probeIntervalSeconds",
		"degradedTtlSeconds",
		"maxFirstByteRetriesPerRequest",
	)
}

func managementRouteStrategyHybridConfigShape(value any) bool {
	if value == nil {
		return true
	}
	record, ok := value.(map[string]any)
	if !ok || record == nil || !managementRouteStrategyAllowedKeys(
		record,
		"scoringGroupId",
		"scoringModel",
		"scoringContextMode",
		"qualityPreference",
		"scoringTimeoutMs",
		"scoringFallbackMaxLevel",
		"scoringCacheEnabled",
		"scoringCacheTtlSeconds",
		"cacheAffinityEnabled",
		"affinityTtlSeconds",
		"switchMinLevelDelta",
		"downgradeConsecutiveLowCount",
		"levelRoutes",
		"qualityInspection",
	) {
		return false
	}
	if !managementRouteStrategyOptionalStringShape(
		record,
		"scoringGroupId",
		"scoringModel",
		"scoringContextMode",
		"qualityPreference",
	) || !managementRouteStrategyOptionalBooleanShape(
		record,
		"scoringCacheEnabled",
		"cacheAffinityEnabled",
	) || !managementRouteStrategyOptionalIntegerShape(
		record,
		"scoringTimeoutMs",
		"scoringFallbackMaxLevel",
		"scoringCacheTtlSeconds",
		"affinityTtlSeconds",
		"switchMinLevelDelta",
		"downgradeConsecutiveLowCount",
	) {
		return false
	}
	if routes, exists := record["levelRoutes"]; exists {
		items, ok := routes.([]any)
		if !ok {
			return false
		}
		for _, item := range items {
			route, ok := item.(map[string]any)
			if !ok || route == nil || !managementRouteStrategyAllowedKeys(
				route,
				"minLevel",
				"maxLevel",
				"targetModel",
				"enabled",
			) || !managementRouteStrategyRequiredIntegerShape(route, "minLevel", "maxLevel") ||
				!managementRouteStrategyOptionalStringShape(route, "targetModel") ||
				!managementRouteStrategyOptionalBooleanShape(route, "enabled") {
				return false
			}
		}
	}
	if quality, exists := record["qualityInspection"]; exists {
		qualityRecord, ok := quality.(map[string]any)
		if !ok || qualityRecord == nil || !managementRouteStrategyAllowedKeys(
			qualityRecord,
			"enabled",
			"scoringGroupId",
			"scoringModel",
			"triggerMode",
			"maxTriggerLevel",
			"maxRetries",
			"failureAction",
			"unavailableAction",
		) || !managementRouteStrategyOptionalBooleanShape(qualityRecord, "enabled") ||
			!managementRouteStrategyOptionalStringShape(
				qualityRecord,
				"scoringGroupId",
				"scoringModel",
				"triggerMode",
				"failureAction",
				"unavailableAction",
			) || !managementRouteStrategyOptionalIntegerShape(
			qualityRecord,
			"maxTriggerLevel",
			"maxRetries",
		) {
			return false
		}
	}
	return true
}

func managementRouteStrategyAllowedKeys(record map[string]any, allowed ...string) bool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range record {
		if _, ok := allowedSet[key]; !ok {
			return false
		}
	}
	return true
}

func managementRouteStrategyOptionalStringShape(
	record map[string]any,
	keys ...string,
) bool {
	for _, key := range keys {
		if value, exists := record[key]; exists {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	return true
}

func managementRouteStrategyOptionalBooleanShape(
	record map[string]any,
	keys ...string,
) bool {
	for _, key := range keys {
		if value, exists := record[key]; exists {
			if _, ok := value.(bool); !ok {
				return false
			}
		}
	}
	return true
}

func managementRouteStrategyOptionalIntegerShape(
	record map[string]any,
	keys ...string,
) bool {
	for _, key := range keys {
		if value, exists := record[key]; exists {
			if _, ok := managementRouteStrategyInteger(value, math.MinInt, math.MaxInt); !ok {
				return false
			}
		}
	}
	return true
}

func managementRouteStrategyRequiredIntegerShape(
	record map[string]any,
	keys ...string,
) bool {
	for _, key := range keys {
		value, exists := record[key]
		if !exists {
			return false
		}
		if _, ok := managementRouteStrategyInteger(value, math.MinInt, math.MaxInt); !ok {
			return false
		}
	}
	return true
}

func managementRouteStrategyInteger(value any, minimum int, maximum int) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	numeric, err := number.Float64()
	if err != nil || math.IsNaN(numeric) || math.IsInf(numeric, 0) ||
		numeric != math.Trunc(numeric) ||
		numeric < float64(minimum) ||
		numeric > float64(maximum) {
		return 0, false
	}
	return int(numeric), true
}

func managementRouteStrategyRequiredText(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = managementRouteStrategyTrimText(text)
	return text, text != ""
}

func managementRouteStrategyTrimText(value string) string {
	return strings.TrimFunc(value, managementGroupListECMAScriptWhitespace)
}

func managementRouteStrategyStringIn(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func writeManagementRouteStrategyCreateError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if message, ok := managementroutestrategies.NameExistsMessage(err); ok {
		writeMessageError(w, http.StatusConflict, message)
		return false
	}
	if message, ok := managementroutestrategies.ValidationMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return false
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	return false
}

func recordManagementRouteStrategyCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementRouteStrategyOptionScope,
	ownerSystemAccountID string,
	result managementroutestrategies.DetailResult,
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
	mode := "self"
	if scope == managementRouteStrategyScopeAdmin {
		mode = "admin"
	}
	statusCode := http.StatusCreated
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: ownerSystemAccountID,
		Mode:                          mode,
		Module:                        "route_strategies",
		Action:                        "create",
		OperationKey:                  "route_strategies.create",
		ResourceType:                  "route_strategy",
		ResourceID:                    result.ID,
		ResourceName:                  result.Name,
		Summary:                       "创建策略路由：" + result.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{Field: "name", Label: "名称", Before: nil, After: result.Name},
			{Field: "mode", Label: "路由模式", Before: nil, After: result.Mode},
			{Field: "status", Label: "状态", Before: nil, After: result.Status},
			{Field: "groupBindings", Label: "绑定分组", Before: nil, After: result.GroupBindings},
			{Field: "normalRoutingConfig", Label: "普通路由调度配置", Before: nil, After: result.NormalRoutingConfig},
			{Field: "hybridRoutingConfig", Label: "混合智能路由配置", Before: nil, After: result.HybridRoutingConfig},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  ownerSystemAccountID,
			VisibilityReason: "resource_owner",
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}
