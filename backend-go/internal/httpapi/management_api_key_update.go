package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

const managementAPIKeyUpdateMaxBodyBytes = 256 << 10

type managementAPIKeyUpdateService interface {
	Update(r *http.Request, input managementapikeys.UpdateInput) (managementapikeys.UpdateResult, error)
}

type managementAPIKeyUpdateServiceAdapter struct {
	service *managementapikeys.Service
}

func (s managementAPIKeyUpdateServiceAdapter) Update(
	r *http.Request,
	input managementapikeys.UpdateInput,
) (managementapikeys.UpdateResult, error) {
	return s.service.Update(r.Context(), input)
}

func NewManagementAPIKeyUpdateHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyUpdateHandler(
		managementAPIKeyUpdateServiceFrom(service),
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAPIKeyUpdateHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyUpdateHandler(
		managementAPIKeyUpdateServiceFrom(service),
		managementAPIKeyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementAPIKeyUpdateServiceFrom(
	service *managementapikeys.Service,
) managementAPIKeyUpdateService {
	if service == nil {
		return nil
	}
	return managementAPIKeyUpdateServiceAdapter{service: service}
}

func newManagementAPIKeyUpdateHandler(
	service managementAPIKeyUpdateService,
	scope managementAPIKeyScope,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setManagementAPIKeySecretHeaders(w)
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAPIKeyScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		targetSystemAccountID, selfOnly, queryError := managementAPIKeyUpdateScope(authContext, r, scope)
		if queryError != "" {
			writeMessageError(w, http.StatusBadRequest, queryError)
			return
		}
		payload, ok := decodeManagementAPIKeyUpdatePayload(w, r)
		if !ok {
			return
		}
		result, err := service.Update(r, managementapikeys.UpdateInput{
			ActorSystemAccountID:    authContext.SystemAccountID,
			ActorRole:               authContext.Role,
			SystemAccountID:         targetSystemAccountID,
			SelfOnly:                selfOnly,
			APIKeyID:                chi.URLParam(r, "id"),
			HasName:                 payload.HasName,
			Name:                    payload.Name,
			HasDescription:          payload.HasDescription,
			Description:             payload.Description,
			HasRouteStrategyID:      payload.HasRouteStrategyID,
			RouteStrategyID:         payload.RouteStrategyID,
			HasStatus:               payload.HasStatus,
			Status:                  payload.Status,
			HasExpiresAt:            payload.HasExpiresAt,
			ExpiresAt:               payload.ExpiresAt,
			HasQuotaLimits:          payload.HasQuotaLimits,
			QuotaLimits:             payload.QuotaLimits,
			HasAvailabilitySchedule: payload.HasAvailabilitySchedule,
			AvailabilitySchedule:    payload.AvailabilitySchedule,
		})
		errorStatus, errorMessage, failed := managementAPIKeyUpdateErrorResponse(err)
		statusCode := http.StatusOK
		if failed {
			statusCode = errorStatus
		}
		if result.Committed {
			recordManagementAPIKeyUpdateOperationLog(r, authContext, scope, result, statusCode, logOptions)
		}
		if failed {
			writeMessageError(w, errorStatus, errorMessage)
			return
		}
		if scope == managementAPIKeyScopeSelf {
			result.After.SystemAccountID = ""
			result.After.SystemAccountName = ""
		}
		writeData(w, http.StatusOK, result.After)
	})
}

func managementAPIKeyUpdateScope(
	authContext managementauth.Context,
	r *http.Request,
	scope managementAPIKeyScope,
) (string, bool, string) {
	actorSystemAccountID := strings.TrimSpace(authContext.SystemAccountID)
	if scope == managementAPIKeyScopeSelf {
		return actorSystemAccountID, true, ""
	}
	rawValues, exists := r.URL.Query()["systemAccountId"]
	if !exists {
		return "", false, ""
	}
	if len(rawValues) != 1 {
		return "", false, "Expected string, received array"
	}
	targetSystemAccountID := strings.TrimSpace(rawValues[0])
	if targetSystemAccountID == "" {
		return "", false, "系统账号 ID 不能为空"
	}
	if targetSystemAccountID == "all" {
		targetSystemAccountID = ""
	}
	return targetSystemAccountID, false, ""
}

type managementAPIKeyUpdatePayload struct {
	HasName                 bool
	Name                    string
	HasDescription          bool
	Description             any
	HasRouteStrategyID      bool
	RouteStrategyID         string
	HasStatus               bool
	Status                  string
	HasExpiresAt            bool
	ExpiresAt               any
	HasQuotaLimits          bool
	QuotaLimits             any
	HasAvailabilitySchedule bool
	AvailabilitySchedule    any
}

func decodeManagementAPIKeyUpdatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementAPIKeyUpdatePayload, bool) {
	limited := http.MaxBytesReader(w, r.Body, managementAPIKeyUpdateMaxBodyBytes)
	body, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil {
		writeManagementGroupCreateBodyError(w, err)
		return managementAPIKeyUpdatePayload{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementAPIKeyUpdatePayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementAPIKeyUpdatePayload{}, false
	}
	raw, ok := decoded.(map[string]any)
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
		return managementAPIKeyUpdatePayload{}, false
	}
	if len(raw) == 0 {
		writeMessageError(w, http.StatusBadRequest, "请提供要修改的 API Key 内容")
		return managementAPIKeyUpdatePayload{}, false
	}

	var payload managementAPIKeyUpdatePayload
	for field, value := range raw {
		switch field {
		case "name":
			payload.HasName = true
			payload.Name, ok = value.(string)
		case "description":
			payload.HasDescription = true
			payload.Description = value
			if value != nil {
				_, ok = value.(string)
			}
		case "routeStrategyId":
			payload.HasRouteStrategyID = true
			payload.RouteStrategyID, ok = value.(string)
		case "status":
			payload.HasStatus = true
			payload.Status, ok = value.(string)
		case "expiresAt":
			payload.HasExpiresAt = true
			payload.ExpiresAt = value
			if value != nil {
				_, ok = value.(string)
			}
		case "quotaLimits":
			payload.HasQuotaLimits = true
			payload.QuotaLimits = value
			if value != nil {
				_, ok = value.(map[string]any)
			}
		case "availabilitySchedule":
			payload.HasAvailabilitySchedule = true
			payload.AvailabilitySchedule = value
			if value != nil {
				_, ok = value.(map[string]any)
			}
		default:
			ok = false
		}
		if !ok {
			writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
			return managementAPIKeyUpdatePayload{}, false
		}
	}
	return payload, true
}

func managementAPIKeyUpdateErrorResponse(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	if message, ok := managementapikeys.APIKeyNameExistsMessage(err); ok {
		return http.StatusConflict, message, true
	}
	switch {
	case errors.Is(err, managementapikeys.ErrAPIKeyNotFound):
		return http.StatusNotFound, "API Key 不存在", true
	case errors.Is(err, managementapikeys.ErrAPIKeyDefaultRouteChange):
		return http.StatusBadRequest, managementapikeys.ErrAPIKeyDefaultRouteChange.Error(), true
	case errors.Is(err, managementapikeys.ErrAPIKeyRouteStrategyMissing),
		errors.Is(err, managementapikeys.ErrAPIKeyRouteStrategyOff):
		return http.StatusBadRequest, err.Error(), true
	case errors.Is(err, managementapikeys.ErrAPIKeyUpdateInvalid):
		return http.StatusBadRequest, "API Key 参数无效", true
	default:
		if managementapikeys.IsAPIKeyUpdateValidationError(err) {
			return http.StatusBadRequest, err.Error(), true
		}
		return http.StatusInternalServerError, "服务器内部错误", true
	}
}

func recordManagementAPIKeyUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAPIKeyScope,
	result managementapikeys.UpdateResult,
	statusCode int,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil {
		return
	}
	mode := "self"
	if scope == managementAPIKeyScopeAdmin {
		mode = "admin"
	}
	input := port.OperationLogInput{
		ID:                            opts.newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.OwnerSystemAccountID,
		Mode:                          mode,
		Module:                        "api_keys",
		Action:                        "update",
		OperationKey:                  "api_keys.update",
		ResourceType:                  "api_key",
		ResourceID:                    result.After.ID,
		ResourceName:                  result.After.Name,
		Summary:                       "更新 API Key：" + result.After.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       managementAPIKeyUpdateOperationChanges(result),
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
		CreatedAt: time.Now().UTC(),
	}
	if opts.now != nil {
		input.CreatedAt = opts.now().UTC()
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func managementAPIKeyUpdateOperationChanges(
	result managementapikeys.UpdateResult,
) []port.OperationLogChange {
	before := result.Before
	after := result.After
	changes := make([]port.OperationLogChange, 0, 7)
	uncertain := result.UncertainOperationLogFields
	changes = appendManagementAPIKeyUpdateChange(changes, uncertain, "name", "名称", before.Name, after.Name)
	changes = appendManagementAPIKeyUpdateChange(changes, uncertain, "description", "说明", before.Description, after.Description)
	changes = appendManagementAPIKeyUpdateChange(
		changes,
		uncertain,
		"routeStrategyId",
		"策略路由",
		before.RouteStrategyID,
		after.RouteStrategyID,
	)
	changes = appendManagementAPIKeyUpdateChange(changes, uncertain, "status", "状态", before.Status, after.Status)
	changes = appendManagementAPIKeyUpdateChange(changes, uncertain, "expiresAt", "过期时间", before.ExpiresAt, after.ExpiresAt)
	changes = appendManagementAPIKeyUpdateChange(
		changes,
		uncertain,
		"quotaLimits",
		"额度限制",
		before.QuotaLimits,
		after.QuotaLimits,
	)
	changes = appendManagementAPIKeyUpdateChange(
		changes,
		uncertain,
		"availabilitySchedule",
		"时间计划",
		before.AvailabilitySchedule,
		after.AvailabilitySchedule,
	)
	return changes
}

func appendManagementAPIKeyUpdateChange(
	changes []port.OperationLogChange,
	uncertain map[string]bool,
	field string,
	label string,
	before any,
	after any,
) []port.OperationLogChange {
	if uncertain[field] || reflect.DeepEqual(before, after) {
		return changes
	}
	return append(changes, port.OperationLogChange{
		Field:  field,
		Label:  label,
		Before: before,
		After:  after,
	})
}

var _ managementAPIKeyUpdateService = managementAPIKeyUpdateServiceAdapter{}
