package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

const managementAPIKeyCreateMaxBodyBytes = 256 << 10

const managementAPIKeyCreateValidationContextKey contextKey = "management_api_key_create_validation"

type managementAPIKeyCreateService interface {
	Create(r *http.Request, input managementapikeys.CreateInput) (managementapikeys.CreateResult, error)
}

type managementAPIKeyCreateServiceAdapter struct {
	service *managementapikeys.Service
}

func (s managementAPIKeyCreateServiceAdapter) Create(
	r *http.Request,
	input managementapikeys.CreateInput,
) (managementapikeys.CreateResult, error) {
	return s.service.Create(r.Context(), input)
}

func NewManagementAPIKeyCreateHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyCreateHandler(
		managementAPIKeyCreateServiceFrom(service),
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAPIKeyCreateHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyCreateHandler(
		managementAPIKeyCreateServiceFrom(service),
		managementAPIKeyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementAPIKeyCreateServiceFrom(
	service *managementapikeys.Service,
) managementAPIKeyCreateService {
	if service == nil {
		return nil
	}
	return managementAPIKeyCreateServiceAdapter{service: service}
}

func newManagementAPIKeyCreateHandler(
	service managementAPIKeyCreateService,
	scope managementAPIKeyScope,
	logOptions managementOperationLogOptions,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		validated, validatedOK := managementAPIKeyCreateValidationFromRequest(r)
		if !validatedOK {
			ownerSystemAccountID, selfOnly, queryError := managementAPIKeyCreateScope(authContext, r, scope)
			if queryError != "" {
				writeMessageError(w, http.StatusBadRequest, queryError)
				return
			}
			payload, ok := decodeManagementAPIKeyCreatePayload(w, r)
			if !ok {
				return
			}
			validated = managementAPIKeyCreateValidation{
				OwnerSystemAccountID: ownerSystemAccountID,
				SelfOnly:             selfOnly,
				Payload:              payload,
			}
		}
		result, err := service.Create(r, managementapikeys.CreateInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      validated.OwnerSystemAccountID,
			SelfOnly:             validated.SelfOnly,
			Name:                 validated.Payload.Name,
			Description:          validated.Payload.Description,
			RouteStrategyID:      validated.Payload.RouteStrategyID,
			Status:               validated.Payload.Status,
			ExpiresAt:            validated.Payload.ExpiresAt,
			QuotaLimits:          validated.Payload.QuotaLimits,
			AvailabilitySchedule: validated.Payload.AvailabilitySchedule,
		})
		if !writeManagementAPIKeyCreateError(w, err) {
			return
		}
		if scope == managementAPIKeyScopeSelf {
			result.SystemAccountID = ""
			result.SystemAccountName = ""
		}
		setManagementAPIKeySecretHeaders(w)
		writeJSON(w, http.StatusCreated, DataResponse{
			Data:    result,
			Message: "API Key 已创建，请立即复制完整密钥",
		})
		recordManagementAPIKeyCreateOperationLog(r, authContext, scope, result, logOptions)
	})
}

func managementAPIKeyCreateScope(
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
		return actorSystemAccountID, false, ""
	}
	if len(rawValues) != 1 {
		return "", false, "Expected string, received array"
	}
	targetSystemAccountID := strings.TrimSpace(rawValues[0])
	if targetSystemAccountID == "" {
		return "", false, "系统账号 ID 不能为空"
	}
	if targetSystemAccountID == "all" {
		targetSystemAccountID = actorSystemAccountID
	}
	return targetSystemAccountID, false, ""
}

type managementAPIKeyCreatePayload struct {
	Name                 string
	Description          any
	RouteStrategyID      string
	Status               string
	ExpiresAt            any
	QuotaLimits          any
	AvailabilitySchedule any
}

type managementAPIKeyCreateValidation struct {
	OwnerSystemAccountID string
	SelfOnly             bool
	Payload              managementAPIKeyCreatePayload
}

func managementAPIKeyCreateValidationMiddleware(
	scope managementAPIKeyScope,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authContext, ok := ManagementAuthContextFromRequest(r)
			if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			ownerSystemAccountID, selfOnly, queryError := managementAPIKeyCreateScope(authContext, r, scope)
			if queryError != "" {
				writeMessageError(w, http.StatusBadRequest, queryError)
				return
			}
			payload, ok := decodeManagementAPIKeyCreatePayload(w, r)
			if !ok {
				return
			}
			validated := managementAPIKeyCreateValidation{
				OwnerSystemAccountID: ownerSystemAccountID,
				SelfOnly:             selfOnly,
				Payload:              payload,
			}
			ctx := context.WithValue(r.Context(), managementAPIKeyCreateValidationContextKey, validated)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func managementAPIKeyCreateValidationFromRequest(
	r *http.Request,
) (managementAPIKeyCreateValidation, bool) {
	if r == nil {
		return managementAPIKeyCreateValidation{}, false
	}
	validated, ok := r.Context().Value(managementAPIKeyCreateValidationContextKey).(managementAPIKeyCreateValidation)
	return validated, ok
}

func decodeManagementAPIKeyCreatePayload(
	w http.ResponseWriter,
	r *http.Request,
) (managementAPIKeyCreatePayload, bool) {
	limited := http.MaxBytesReader(w, r.Body, managementAPIKeyCreateMaxBodyBytes)
	body, err := io.ReadAll(limited)
	_ = limited.Close()
	if err != nil {
		writeManagementGroupCreateBodyError(w, err)
		return managementAPIKeyCreatePayload{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementAPIKeyCreatePayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementAPIKeyCreatePayload{}, false
	}
	raw, ok := decoded.(map[string]any)
	if !ok {
		writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
		return managementAPIKeyCreatePayload{}, false
	}
	for field := range raw {
		switch field {
		case "name", "description", "routeStrategyId", "status", "expiresAt", "quotaLimits", "availabilitySchedule":
		default:
			writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
			return managementAPIKeyCreatePayload{}, false
		}
	}

	name, ok := raw["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
		return managementAPIKeyCreatePayload{}, false
	}
	routeStrategyID, ok := raw["routeStrategyId"].(string)
	if !ok || strings.TrimSpace(routeStrategyID) == "" {
		writeMessageError(w, http.StatusBadRequest, "API Key 必须绑定策略路由")
		return managementAPIKeyCreatePayload{}, false
	}
	payload := managementAPIKeyCreatePayload{
		Name:            name,
		RouteStrategyID: routeStrategyID,
	}
	if value, exists := raw["description"]; exists {
		if value != nil {
			if _, ok := value.(string); !ok {
				writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
				return managementAPIKeyCreatePayload{}, false
			}
		}
		payload.Description = value
	}
	if value, exists := raw["status"]; exists {
		status, ok := value.(string)
		if !ok || (status != "active" && status != "disabled") {
			writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
			return managementAPIKeyCreatePayload{}, false
		}
		payload.Status = status
	}
	if value, exists := raw["expiresAt"]; exists {
		if value != nil {
			if _, ok := value.(string); !ok {
				writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
				return managementAPIKeyCreatePayload{}, false
			}
		}
		payload.ExpiresAt = value
	}
	if value, exists := raw["quotaLimits"]; exists {
		if value != nil {
			if _, ok := value.(map[string]any); !ok {
				writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
				return managementAPIKeyCreatePayload{}, false
			}
		}
		payload.QuotaLimits = value
	}
	if value, exists := raw["availabilitySchedule"]; exists {
		if value != nil {
			if _, ok := value.(map[string]any); !ok {
				writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
				return managementAPIKeyCreatePayload{}, false
			}
		}
		payload.AvailabilitySchedule = value
	}
	return payload, true
}

func managementAPIKeyCreateJSONBodyMiddleware(next http.Handler) http.Handler {
	return managementGroupCreateJSONBodyMiddleware(next)
}

func writeManagementAPIKeyCreateError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if message, ok := managementapikeys.APIKeyNameExistsMessage(err); ok {
		writeMessageError(w, http.StatusConflict, message)
		return false
	}
	switch {
	case errors.Is(err, managementapikeys.ErrAPIKeyRouteStrategyMissing),
		errors.Is(err, managementapikeys.ErrAPIKeyRouteStrategyOff):
		writeMessageError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, managementapikeys.ErrAPIKeyCreateInvalid):
		writeMessageError(w, http.StatusBadRequest, "API Key 参数无效")
	default:
		if managementapikeys.IsAPIKeyCreateValidationError(err) {
			writeMessageError(w, http.StatusBadRequest, err.Error())
		} else {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		}
	}
	return false
}

func recordManagementAPIKeyCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAPIKeyScope,
	result managementapikeys.CreateResult,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil {
		return
	}
	mode := "self"
	if scope == managementAPIKeyScopeAdmin {
		mode = "admin"
	}
	statusCode := http.StatusCreated
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
		Action:                        "create",
		OperationKey:                  "api_keys.create",
		ResourceType:                  "api_key",
		ResourceID:                    result.ID,
		ResourceName:                  result.Name,
		Summary:                       "创建 API Key：" + result.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{Field: "name", Label: "名称", Before: nil, After: result.Name},
			{Field: "status", Label: "状态", Before: nil, After: result.Status},
			{Field: "routeStrategyId", Label: "策略路由", Before: nil, After: result.RouteStrategyID},
			{Field: "availabilitySchedule", Label: "时间计划", Before: nil, After: result.AvailabilitySchedule},
			{Field: "key", Label: "密钥标识", Before: nil, After: fmt.Sprintf("%s...%s", result.KeyPrefix, result.KeySuffix)},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
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

var _ managementAPIKeyCreateService = managementAPIKeyCreateServiceAdapter{}
