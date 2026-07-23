package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/store/port"
)

type managementAPIKeyDeleteService interface {
	Delete(r *http.Request, input managementapikeys.DeleteInput) (managementapikeys.DeleteResult, error)
}

type managementAPIKeyDeleteServiceAdapter struct {
	service *managementapikeys.Service
}

func (s managementAPIKeyDeleteServiceAdapter) Delete(
	r *http.Request,
	input managementapikeys.DeleteInput,
) (managementapikeys.DeleteResult, error) {
	return s.service.Delete(r.Context(), input)
}

func NewManagementAPIKeyDeleteHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyDeleteHandler(
		managementAPIKeyDeleteServiceFrom(service),
		managementAPIKeyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyAPIKeyDeleteHandlerWithOperationLog(
	service *managementapikeys.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementAPIKeyDeleteHandler(
		managementAPIKeyDeleteServiceFrom(service),
		managementAPIKeyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementAPIKeyDeleteServiceFrom(
	service *managementapikeys.Service,
) managementAPIKeyDeleteService {
	if service == nil {
		return nil
	}
	return managementAPIKeyDeleteServiceAdapter{service: service}
}

func newManagementAPIKeyDeleteHandler(
	service managementAPIKeyDeleteService,
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
		result, err := service.Delete(r, managementapikeys.DeleteInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      targetSystemAccountID,
			SelfOnly:             selfOnly,
			APIKeyID:             chi.URLParam(r, "id"),
		})
		errorStatus, errorMessage, failed := managementAPIKeyDeleteErrorResponse(err)
		statusCode := http.StatusNoContent
		if failed {
			statusCode = errorStatus
		}
		if result.Committed {
			recordManagementAPIKeyDeleteOperationLog(
				r,
				authContext,
				scope,
				result,
				statusCode,
				logOptions,
			)
		}
		if failed {
			writeMessageError(w, errorStatus, errorMessage)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func managementAPIKeyDeleteErrorResponse(err error) (int, string, bool) {
	if err == nil {
		return 0, "", false
	}
	switch {
	case errors.Is(err, managementapikeys.ErrAPIKeyNotFound):
		return http.StatusNotFound, "API Key 不存在", true
	case errors.Is(err, managementapikeys.ErrAPIKeyDefaultDelete):
		return http.StatusConflict, "默认 API Key 不允许删除", true
	case errors.Is(err, managementapikeys.ErrAPIKeyChatDelete):
		return http.StatusConflict, "AI 对话 API Key 不允许删除", true
	case errors.Is(err, managementapikeys.ErrAPIKeyDeleteInvalid):
		return http.StatusBadRequest, "API Key 参数无效", true
	default:
		return http.StatusInternalServerError, "服务器内部错误", true
	}
}

func recordManagementAPIKeyDeleteOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementAPIKeyScope,
	result managementapikeys.DeleteResult,
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
		Action:                        "delete",
		OperationKey:                  "api_keys.delete",
		ResourceType:                  "api_key",
		ResourceID:                    result.APIKeyID,
		ResourceName:                  result.Name,
		Summary:                       "删除 API Key：" + result.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{{
			Field:  "deleted",
			Label:  "删除状态",
			Before: false,
			After:  true,
		}},
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

var _ managementAPIKeyDeleteService = managementAPIKeyDeleteServiceAdapter{}
