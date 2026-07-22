package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/store/port"
)

type managementGroupUpdateService interface {
	Update(r *http.Request, input managementgroups.UpdateInput) (managementgroups.UpdateResult, error)
}

type managementGroupUpdateServiceAdapter struct {
	service *managementgroups.Service
}

func (s managementGroupUpdateServiceAdapter) Update(
	r *http.Request,
	input managementgroups.UpdateInput,
) (managementgroups.UpdateResult, error) {
	return s.service.Update(r.Context(), input)
}

func NewManagementGroupUpdateHandlerWithOperationLog(
	service *managementgroups.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementGroupUpdateHandler(
		managementGroupUpdateServiceFrom(service),
		managementGroupScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyGroupUpdateHandlerWithOperationLog(
	service *managementgroups.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementGroupUpdateHandler(
		managementGroupUpdateServiceFrom(service),
		managementGroupScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementGroupUpdateServiceFrom(service *managementgroups.Service) managementGroupUpdateService {
	if service == nil {
		return nil
	}
	return managementGroupUpdateServiceAdapter{service: service}
}

func newManagementGroupUpdateHandler(
	service managementGroupUpdateService,
	scope managementGroupOptionScope,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if scope == managementGroupScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		systemAccountID := ""
		selfOnly := scope == managementGroupScopeSelf
		if !selfOnly {
			var message string
			systemAccountID, message, ok = managementGroupDetailSystemAccountID(r.URL.Query())
			if !ok {
				writeMessageError(w, http.StatusBadRequest, message)
				return
			}
		}
		payload, ok := decodeManagementGroupUpdatePayload(w, r)
		if !ok {
			return
		}
		result, err := service.Update(r, managementgroups.UpdateInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             selfOnly,
			GroupID:              chi.URLParam(r, "id"),
			HasName:              payload.HasName,
			Name:                 payload.Name,
			HasProviderCode:      payload.HasProviderCode,
			ProviderCode:         payload.ProviderCode,
			HasDescription:       payload.HasDescription,
			Description:          payload.Description,
			HasEnabled:           payload.HasEnabled,
			Enabled:              payload.Enabled,
			HasGroupType:         payload.HasGroupType,
			GroupType:            payload.GroupType,
			HasSchedulingPolicy:  payload.HasSchedulingPolicy,
			SchedulingPolicy:     payload.SchedulingPolicy,
		})
		switch {
		case errors.Is(err, managementgroups.ErrGroupNotFound):
			writeMessageError(w, http.StatusNotFound, "分组不存在")
		case errors.Is(err, managementgroups.ErrGroupDefaultReadonly):
			writeMessageError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, managementgroups.ErrGroupProviderHasAccounts):
			writeMessageError(w, http.StatusBadRequest, err.Error())
		case managementGroupUpdateProviderError(w, err):
		case managementGroupUpdateRejectedError(w, err):
		case err != nil:
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		default:
			recordManagementGroupUpdateOperationLog(r, authContext, scope, result, logOptions)
			writeData(w, http.StatusOK, result.Group)
		}
	})
}

type managementGroupUpdatePayload struct {
	HasName             bool
	Name                string
	HasProviderCode     bool
	ProviderCode        string
	HasDescription      bool
	Description         *string
	HasEnabled          bool
	Enabled             bool
	HasGroupType        bool
	GroupType           string
	HasSchedulingPolicy bool
	SchedulingPolicy    *managementgroups.SchedulingPolicyInput
}

func decodeManagementGroupUpdatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementGroupUpdatePayload, bool) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, managementGroupCreateMaxBodyBytes))
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return managementGroupUpdatePayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return managementGroupUpdatePayload{}, false
	}
	if len(raw) == 0 {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return managementGroupUpdatePayload{}, false
	}

	payload := managementGroupUpdatePayload{}
	for field, value := range raw {
		switch field {
		case "name":
			text, ok := requiredManagementGroupString(raw, field)
			if !ok {
				writeMessageError(w, http.StatusBadRequest, "分组参数无效")
				return managementGroupUpdatePayload{}, false
			}
			payload.HasName = true
			payload.Name = text
		case "providerCode":
			text, ok := requiredManagementGroupString(raw, field)
			if !ok {
				writeMessageError(w, http.StatusBadRequest, "分组参数无效")
				return managementGroupUpdatePayload{}, false
			}
			payload.HasProviderCode = true
			payload.ProviderCode = text
		case "description":
			var description string
			if isManagementGroupJSONNull(value) || json.Unmarshal(value, &description) != nil {
				writeMessageError(w, http.StatusBadRequest, "分组参数无效")
				return managementGroupUpdatePayload{}, false
			}
			description = strings.TrimSpace(description)
			payload.HasDescription = true
			payload.Description = &description
		case "enabled":
			var enabled bool
			if isManagementGroupJSONNull(value) || json.Unmarshal(value, &enabled) != nil {
				writeMessageError(w, http.StatusBadRequest, "分组参数无效")
				return managementGroupUpdatePayload{}, false
			}
			payload.HasEnabled = true
			payload.Enabled = enabled
		case "groupType":
			var groupType string
			if json.Unmarshal(value, &groupType) != nil ||
				(groupType != "personal" && groupType != "high_concurrency") {
				writeMessageError(w, http.StatusBadRequest, "分组参数无效")
				return managementGroupUpdatePayload{}, false
			}
			payload.HasGroupType = true
			payload.GroupType = groupType
		case "schedulingPolicy":
			policy, ok := decodeManagementGroupSchedulingPolicy(w, value)
			if !ok {
				return managementGroupUpdatePayload{}, false
			}
			payload.HasSchedulingPolicy = true
			payload.SchedulingPolicy = policy
		default:
			writeMessageError(w, http.StatusBadRequest, "分组参数无效")
			return managementGroupUpdatePayload{}, false
		}
	}
	return payload, true
}

func managementGroupUpdateProviderError(w http.ResponseWriter, err error) bool {
	if message, ok := managementgroups.ProviderNotFoundMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return true
	}
	if message, ok := managementgroups.ProviderDisabledMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return true
	}
	if message, ok := managementgroups.NameExistsMessage(err); ok {
		writeMessageError(w, http.StatusConflict, message)
		return true
	}
	if _, ok := managementgroups.ValidationMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, "分组参数无效")
		return true
	}
	return false
}

func managementGroupUpdateRejectedError(w http.ResponseWriter, err error) bool {
	message, ok := managementgroups.UpdateRejectedMessage(err)
	if !ok {
		return false
	}
	writeMessageError(w, http.StatusBadRequest, message)
	return true
}

func recordManagementGroupUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementGroupOptionScope,
	result managementgroups.UpdateResult,
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
	if scope == managementGroupScopeAdmin {
		mode = "admin"
	}
	operationScopeSystemAccountID := result.OwnerSystemAccountID
	viewerReason := "resource_owner"
	summary := "更新分组：" + result.Group.Name
	if result.AccessType == "authorized" {
		operationScopeSystemAccountID = result.EffectiveSystemAccountID
		viewerReason = "authorization_grantee"
		summary = "更新授权分组使用配置：" + result.Group.Name
	}
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: operationScopeSystemAccountID,
		Mode:                          mode,
		Module:                        "groups",
		Action:                        "update",
		OperationKey:                  "groups.update",
		ResourceType:                  "group",
		ResourceID:                    result.Group.ID,
		ResourceName:                  result.Group.Name,
		Summary:                       summary,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       managementGroupUpdateOperationChanges(result),
		Method:                        r.Method,
		Path:                          r.URL.Path,
		StatusCode:                    &statusCode,
		ClientIP:                      opts.clientIP.FromRequest(r),
		UserAgent:                     r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  operationScopeSystemAccountID,
			VisibilityReason: viewerReason,
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func managementGroupUpdateOperationChanges(result managementgroups.UpdateResult) []port.OperationLogChange {
	before := result.Before
	after := result.Group
	changes := make([]port.OperationLogChange, 0, 6)
	changes = appendManagementGroupUpdateChange(changes, "name", "名称", before.Name, after.Name)
	changes = appendManagementGroupUpdateChange(changes, "providerCode", "供应商", before.ProviderCode, after.ProviderCode)
	changes = appendManagementGroupUpdateChange(changes, "description", "说明", before.Description, after.Description)
	changes = appendManagementGroupUpdateChange(changes, "groupType", "分组类型", before.GroupType, after.GroupType)
	changes = appendManagementGroupUpdateChange(
		changes,
		"schedulingPolicy",
		"调度策略",
		managementGroupSchedulingPolicyLogValue(before.SchedulingPolicyJSON),
		managementGroupSchedulingPolicyValue(after.SchedulingPolicy),
	)
	changes = appendManagementGroupUpdateChange(changes, "enabled", "启用状态", before.Enabled, after.Enabled)
	return changes
}

func appendManagementGroupUpdateChange(
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

func managementGroupSchedulingPolicyLogValue(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	var decoded any
	if json.Unmarshal([]byte(*value), &decoded) != nil {
		return *value
	}
	return decoded
}

func managementGroupSchedulingPolicyValue(value *managementgroups.SchedulingPolicy) any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if json.Unmarshal(encoded, &decoded) != nil {
		return value
	}
	return decoded
}
