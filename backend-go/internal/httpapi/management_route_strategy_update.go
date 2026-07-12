package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/store/port"
)

type managementRouteStrategyUpdateService interface {
	PrepareUpdate(
		r *http.Request,
		input managementroutestrategies.UpdateInput,
	) error
	Update(
		r *http.Request,
		input managementroutestrategies.UpdateInput,
	) (managementroutestrategies.UpdateResult, error)
}

type managementRouteStrategyUpdateServiceAdapter struct {
	service *managementroutestrategies.Service
}

func (s managementRouteStrategyUpdateServiceAdapter) PrepareUpdate(
	r *http.Request,
	input managementroutestrategies.UpdateInput,
) error {
	return s.service.PrepareUpdate(r.Context(), input)
}

func (s managementRouteStrategyUpdateServiceAdapter) Update(
	r *http.Request,
	input managementroutestrategies.UpdateInput,
) (managementroutestrategies.UpdateResult, error) {
	return s.service.Update(r.Context(), input)
}

func NewManagementRouteStrategyUpdateHandler(
	service *managementroutestrategies.Service,
) http.Handler {
	return newManagementRouteStrategyUpdateHandler(
		managementRouteStrategyUpdateServiceFrom(service),
		managementRouteStrategyScopeAdmin,
		managementOperationLogOptions{},
	)
}

func NewManagementMyRouteStrategyUpdateHandler(
	service *managementroutestrategies.Service,
) http.Handler {
	return newManagementRouteStrategyUpdateHandler(
		managementRouteStrategyUpdateServiceFrom(service),
		managementRouteStrategyScopeSelf,
		managementOperationLogOptions{},
	)
}

func NewManagementRouteStrategyUpdateHandlerWithOperationLog(
	service *managementroutestrategies.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementRouteStrategyUpdateHandler(
		managementRouteStrategyUpdateServiceFrom(service),
		managementRouteStrategyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyRouteStrategyUpdateHandlerWithOperationLog(
	service *managementroutestrategies.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementRouteStrategyUpdateHandler(
		managementRouteStrategyUpdateServiceFrom(service),
		managementRouteStrategyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementRouteStrategyUpdateServiceFrom(
	service *managementroutestrategies.Service,
) managementRouteStrategyUpdateService {
	if service == nil {
		return nil
	}
	return managementRouteStrategyUpdateServiceAdapter{service: service}
}

type managementRouteStrategyUpdatePayload struct {
	HasName             bool
	Name                string
	HasDescription      bool
	Description         *string
	HasMode             bool
	Mode                string
	HasStatus           bool
	Status              string
	HasGroupBindings    bool
	GroupBindings       []managementroutestrategies.CreateGroupBindingInput
	NormalRoutingConfig managementroutestrategies.ConfigInput
	HybridRoutingConfig managementroutestrategies.ConfigInput
}

func newManagementRouteStrategyUpdateHandler(
	service managementRouteStrategyUpdateService,
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
		systemAccountID, ok := managementRouteStrategyUpdateOwner(
			r.URL.Query(),
			scope,
		)
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		input := managementroutestrategies.UpdateInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             scope == managementRouteStrategyScopeSelf,
			RouteStrategyID:      chi.URLParam(r, "id"),
		}
		if !writeManagementRouteStrategyUpdateError(
			w,
			service.PrepareUpdate(r, input),
		) {
			return
		}
		payload, ok := decodeManagementRouteStrategyUpdatePayload(w, r)
		if !ok {
			return
		}
		input.HasName = payload.HasName
		input.Name = payload.Name
		input.HasDescription = payload.HasDescription
		input.Description = payload.Description
		input.HasMode = payload.HasMode
		input.Mode = payload.Mode
		input.HasStatus = payload.HasStatus
		input.Status = payload.Status
		input.HasGroupBindings = payload.HasGroupBindings
		input.GroupBindings = payload.GroupBindings
		input.NormalRoutingConfig = payload.NormalRoutingConfig
		input.HybridRoutingConfig = payload.HybridRoutingConfig
		result, err := service.Update(r, input)
		if !writeManagementRouteStrategyUpdateError(w, err) {
			return
		}
		recordManagementRouteStrategyUpdateOperationLog(
			r,
			authContext,
			scope,
			result,
			logOptions,
		)
		writeData(w, http.StatusOK, result.RouteStrategy)
	})
}

func managementRouteStrategyUpdateOwner(
	values url.Values,
	scope managementRouteStrategyOptionScope,
) (string, bool) {
	if scope == managementRouteStrategyScopeSelf {
		return "", true
	}
	systemAccountID, _, ok := managementGroupDetailSystemAccountID(values)
	return systemAccountID, ok
}

func decodeManagementRouteStrategyUpdatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementRouteStrategyUpdatePayload, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, managementGroupCreateMaxBodyBytes))
	if err != nil {
		writeManagementGroupCreateBodyError(w, err)
		return managementRouteStrategyUpdatePayload{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
		return managementRouteStrategyUpdatePayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
		return managementRouteStrategyUpdatePayload{}, false
	}
	raw, ok := value.(map[string]any)
	if !ok || raw == nil || len(raw) == 0 ||
		!managementRouteStrategyAllowedKeys(
			raw,
			"name",
			"description",
			"mode",
			"status",
			"groupBindings",
			"normalRoutingConfig",
			"hybridRoutingConfig",
		) {
		writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
		return managementRouteStrategyUpdatePayload{}, false
	}

	payload := managementRouteStrategyUpdatePayload{}
	if value, exists := raw["name"]; exists {
		name, ok := value.(string)
		if !ok {
			return invalidManagementRouteStrategyUpdatePayload(w)
		}
		payload.HasName = true
		payload.Name = name
	}
	if value, exists := raw["description"]; exists {
		payload.HasDescription = true
		switch typed := value.(type) {
		case nil:
		case string:
			if len(utf16.Encode([]rune(managementRouteStrategyTrimText(typed)))) > 200 {
				return invalidManagementRouteStrategyUpdatePayload(w)
			}
			payload.Description = &typed
		default:
			return invalidManagementRouteStrategyUpdatePayload(w)
		}
	}
	if value, exists := raw["mode"]; exists {
		mode, ok := value.(string)
		if !ok {
			return invalidManagementRouteStrategyUpdatePayload(w)
		}
		payload.HasMode = true
		payload.Mode = mode
	}
	if value, exists := raw["status"]; exists {
		status, ok := value.(string)
		if !ok {
			return invalidManagementRouteStrategyUpdatePayload(w)
		}
		payload.HasStatus = true
		payload.Status = status
	}
	if value, exists := raw["groupBindings"]; exists {
		bindings, ok := managementRouteStrategyUpdateBindings(value)
		if !ok {
			return invalidManagementRouteStrategyUpdatePayload(w)
		}
		payload.HasGroupBindings = true
		payload.GroupBindings = bindings
	}
	if value, exists := raw["normalRoutingConfig"]; exists {
		if value != nil {
			if _, ok := value.(map[string]any); !ok {
				return invalidManagementRouteStrategyUpdatePayload(w)
			}
		}
		payload.NormalRoutingConfig = managementroutestrategies.NewConfigInput(value, true)
	}
	if value, exists := raw["hybridRoutingConfig"]; exists {
		if value != nil {
			if _, ok := value.(map[string]any); !ok {
				return invalidManagementRouteStrategyUpdatePayload(w)
			}
		}
		payload.HybridRoutingConfig = managementroutestrategies.NewConfigInput(value, true)
	}
	return payload, true
}

func managementRouteStrategyUpdateBindings(
	value any,
) ([]managementroutestrategies.CreateGroupBindingInput, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	bindings := make([]managementroutestrategies.CreateGroupBindingInput, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok || record == nil ||
			!managementRouteStrategyAllowedKeys(
				record,
				"groupId",
				"priority",
				"weight",
				"status",
			) {
			return nil, false
		}
		groupID, ok := record["groupId"].(string)
		if !ok {
			return nil, false
		}
		binding := managementroutestrategies.CreateGroupBindingInput{
			GroupID: groupID,
		}
		if value, exists := record["priority"]; exists {
			priority, ok := managementRouteStrategyInteger(value, math.MinInt, math.MaxInt)
			if !ok {
				return nil, false
			}
			binding.Priority = priority
			binding.PrioritySet = true
		}
		if value, exists := record["weight"]; exists {
			weight, ok := managementRouteStrategyInteger(value, math.MinInt, math.MaxInt)
			if !ok {
				return nil, false
			}
			binding.Weight = weight
			binding.WeightSet = true
		}
		if value, exists := record["status"]; exists {
			status, ok := value.(string)
			if !ok {
				return nil, false
			}
			binding.Status = status
			binding.StatusSet = true
		}
		bindings = append(bindings, binding)
	}
	return bindings, true
}

func invalidManagementRouteStrategyUpdatePayload(
	w http.ResponseWriter,
) (managementRouteStrategyUpdatePayload, bool) {
	writeMessageError(w, http.StatusBadRequest, "策略路由参数无效")
	return managementRouteStrategyUpdatePayload{}, false
}

func writeManagementRouteStrategyUpdateError(
	w http.ResponseWriter,
	err error,
) bool {
	if err == nil {
		return true
	}
	if message, ok := managementroutestrategies.NotFoundMessage(err); ok {
		writeMessageError(w, http.StatusNotFound, message)
		return false
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

func recordManagementRouteStrategyUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementRouteStrategyOptionScope,
	result managementroutestrategies.UpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.client == nil {
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
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.OwnerSystemAccountID,
		Mode:                          mode,
		Module:                        "route_strategies",
		Action:                        "update",
		OperationKey:                  "route_strategies.update",
		ResourceType:                  "route_strategy",
		ResourceID:                    result.RouteStrategy.ID,
		ResourceName:                  result.RouteStrategy.Name,
		Summary:                       "更新策略路由：" + result.RouteStrategy.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       managementRouteStrategyUpdateOperationChanges(result),
		Method:                        r.Method,
		Path:                          r.URL.Path,
		StatusCode:                    &statusCode,
		ClientIP:                      opts.clientIP.FromRequest(r),
		UserAgent:                     r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  result.OwnerSystemAccountID,
			VisibilityReason: "resource_owner",
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func managementRouteStrategyUpdateOperationChanges(
	result managementroutestrategies.UpdateResult,
) []port.OperationLogChange {
	before := result.Before
	after := result.RouteStrategy
	beforeBindings := managementRouteStrategyUpdateBindingLogValues(
		before.GroupBindings,
	)
	afterBindings := managementRouteStrategyUpdateBindingLogValues(
		after.GroupBindings,
	)
	changes := make([]port.OperationLogChange, 0, 7)
	changes = appendManagementRouteStrategyUpdateChange(
		changes, "name", "名称", before.Name, after.Name,
	)
	changes = appendManagementRouteStrategyUpdateChange(
		changes, "description", "说明", before.Description, after.Description,
	)
	changes = appendManagementRouteStrategyUpdateChange(
		changes, "mode", "路由模式", before.Mode, after.Mode,
	)
	changes = appendManagementRouteStrategyUpdateChange(
		changes, "status", "状态", before.Status, after.Status,
	)
	changes = appendManagementRouteStrategyUpdateChange(
		changes, "groupBindings", "绑定分组", beforeBindings, afterBindings,
	)
	changes = appendManagementRouteStrategyUpdateChange(
		changes,
		"normalRoutingConfig",
		"普通路由调度配置",
		before.NormalRoutingConfig,
		after.NormalRoutingConfig,
	)
	changes = appendManagementRouteStrategyUpdateChange(
		changes,
		"hybridRoutingConfig",
		"混合智能路由配置",
		before.HybridRoutingConfig,
		after.HybridRoutingConfig,
	)
	return changes
}

type managementRouteStrategyUpdateBindingLogValue struct {
	GroupID      string `json:"groupId"`
	GroupName    string `json:"groupName"`
	ProviderCode string `json:"providerCode"`
	Priority     int    `json:"priority"`
	Weight       int    `json:"weight"`
	Status       string `json:"status"`
	GroupEnabled bool   `json:"groupEnabled"`
}

func managementRouteStrategyUpdateBindingLogValues(
	bindings []managementroutestrategies.GroupBindingSummary,
) []managementRouteStrategyUpdateBindingLogValue {
	values := make(
		[]managementRouteStrategyUpdateBindingLogValue,
		0,
		len(bindings),
	)
	for _, binding := range bindings {
		values = append(values, managementRouteStrategyUpdateBindingLogValue{
			GroupID:      binding.GroupID,
			GroupName:    binding.GroupName,
			ProviderCode: binding.ProviderCode,
			Priority:     binding.Priority,
			Weight:       binding.Weight,
			Status:       binding.Status,
			GroupEnabled: binding.GroupEnabled,
		})
	}
	sort.Slice(values, func(left int, right int) bool {
		if values[left].Priority != values[right].Priority {
			return values[left].Priority < values[right].Priority
		}
		return values[left].GroupID < values[right].GroupID
	})
	return values
}

func appendManagementRouteStrategyUpdateChange(
	changes []port.OperationLogChange,
	field string,
	label string,
	before any,
	after any,
) []port.OperationLogChange {
	if reflect.DeepEqual(before, after) {
		return changes
	}
	return append(changes, port.OperationLogChange{
		Field:  field,
		Label:  label,
		Before: before,
		After:  after,
	})
}

var _ managementRouteStrategyUpdateService = managementRouteStrategyUpdateServiceAdapter{}
