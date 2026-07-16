package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementroutestrategies"
	"juhe-ai/backend-go/internal/store/port"
)

type managementRouteStrategyDeleteService interface {
	Delete(
		r *http.Request,
		input managementroutestrategies.DeleteInput,
	) (managementroutestrategies.DeleteResult, error)
}

type managementRouteStrategyDeleteServiceAdapter struct {
	service *managementroutestrategies.Service
}

func (s managementRouteStrategyDeleteServiceAdapter) Delete(
	r *http.Request,
	input managementroutestrategies.DeleteInput,
) (managementroutestrategies.DeleteResult, error) {
	return s.service.Delete(r.Context(), input)
}

func NewManagementRouteStrategyDeleteHandlerWithOperationLog(
	service *managementroutestrategies.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementRouteStrategyDeleteHandler(
		managementRouteStrategyDeleteServiceFrom(service),
		managementRouteStrategyScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyRouteStrategyDeleteHandlerWithOperationLog(
	service *managementroutestrategies.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementRouteStrategyDeleteHandler(
		managementRouteStrategyDeleteServiceFrom(service),
		managementRouteStrategyScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementRouteStrategyDeleteServiceFrom(
	service *managementroutestrategies.Service,
) managementRouteStrategyDeleteService {
	if service == nil {
		return nil
	}
	return managementRouteStrategyDeleteServiceAdapter{service: service}
}

func newManagementRouteStrategyDeleteHandler(
	service managementRouteStrategyDeleteService,
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

		systemAccountID := ""
		selfOnly := scope == managementRouteStrategyScopeSelf
		if !selfOnly {
			var message string
			systemAccountID, message, ok = managementGroupDetailSystemAccountID(
				r.URL.Query(),
			)
			if !ok {
				writeMessageError(w, http.StatusBadRequest, message)
				return
			}
		}

		result, err := service.Delete(r, managementroutestrategies.DeleteInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             selfOnly,
			RouteStrategyID:      chi.URLParam(r, "id"),
		})
		if !writeManagementRouteStrategyDeleteError(w, err) {
			return
		}
		if result.Committed {
			recordManagementRouteStrategyDeleteOperationLog(
				r,
				authContext,
				scope,
				result,
				logOptions,
			)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func writeManagementRouteStrategyDeleteError(
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
	if message, ok := managementroutestrategies.DeleteConflictMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return false
	}
	if message, ok := managementroutestrategies.ValidationMessage(err); ok {
		writeMessageError(w, http.StatusBadRequest, message)
		return false
	}
	var internalErr *managementroutestrategies.DeleteInternalError
	if errors.As(err, &internalErr) {
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return false
	}
	writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
	return false
}

func recordManagementRouteStrategyDeleteOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementRouteStrategyOptionScope,
	result managementroutestrategies.DeleteResult,
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
	statusCode := http.StatusNoContent
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
		Action:                        "delete",
		OperationKey:                  "route_strategies.delete",
		ResourceType:                  "route_strategy",
		ResourceID:                    result.Before.ID,
		ResourceName:                  result.Before.Name,
		Summary:                       "删除策略路由：" + result.Before.Name,
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
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

var _ managementRouteStrategyDeleteService = managementRouteStrategyDeleteServiceAdapter{}
