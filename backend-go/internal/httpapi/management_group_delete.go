package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
	"juhe-ai/backend-go/internal/store/port"
)

const managementGroupDeleteOperationLogStrategyLimit = 20

type managementGroupDeleteService interface {
	Delete(r *http.Request, input managementgroups.DeleteInput) (managementgroups.DeleteResult, error)
}

type managementGroupDeleteServiceAdapter struct {
	service *managementgroups.Service
}

func (s managementGroupDeleteServiceAdapter) Delete(
	r *http.Request,
	input managementgroups.DeleteInput,
) (managementgroups.DeleteResult, error) {
	return s.service.Delete(r.Context(), input)
}

func NewManagementGroupDeleteHandlerWithOperationLog(
	service *managementgroups.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementGroupDeleteHandler(
		managementGroupDeleteServiceFrom(service),
		managementGroupScopeAdmin,
		newManagementOperationLogOptions(opts),
	)
}

func NewManagementMyGroupDeleteHandlerWithOperationLog(
	service *managementgroups.Service,
	opts ManagementOperationLogOptions,
) http.Handler {
	return newManagementGroupDeleteHandler(
		managementGroupDeleteServiceFrom(service),
		managementGroupScopeSelf,
		newManagementOperationLogOptions(opts),
	)
}

func managementGroupDeleteServiceFrom(service *managementgroups.Service) managementGroupDeleteService {
	if service == nil {
		return nil
	}
	return managementGroupDeleteServiceAdapter{service: service}
}

func newManagementGroupDeleteHandler(
	service managementGroupDeleteService,
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

		result, err := service.Delete(r, managementgroups.DeleteInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             selfOnly,
			GroupID:              chi.URLParam(r, "id"),
		})
		switch {
		case errors.Is(err, managementgroups.ErrGroupNotFound):
			writeMessageError(w, http.StatusNotFound, "分组不存在")
		case errors.Is(err, managementgroups.ErrGroupDefaultDelete):
			writeMessageError(w, http.StatusBadRequest, err.Error())
		case managementGroupDeleteRejectedError(w, err):
		case err != nil:
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		default:
			recordManagementGroupDeleteOperationLog(r, authContext, scope, result, logOptions)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func managementGroupDeleteRejectedError(w http.ResponseWriter, err error) bool {
	message, ok := managementgroups.UpdateRejectedMessage(err)
	if !ok {
		return false
	}
	writeMessageError(w, http.StatusBadRequest, message)
	return true
}

type managementGroupDeletedRouteStrategyLogEntry struct {
	RouteStrategyID   string `json:"routeStrategyId"`
	RouteStrategyName string `json:"routeStrategyName"`
	RemovedGroupID    string `json:"removedGroupId"`
	RemovedGroupName  string `json:"removedGroupName"`
}

func recordManagementGroupDeleteOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	scope managementGroupOptionScope,
	result managementgroups.DeleteResult,
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
	affectedStrategies := managementGroupDeleteOperationLogStrategies(
		result.Before,
		result.AffectedRouteStrategies,
	)
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
		Module:                        "groups",
		Action:                        "delete",
		OperationKey:                  "groups.delete",
		ResourceType:                  "group",
		ResourceID:                    result.Before.ID,
		ResourceName:                  result.Before.Name,
		Summary:                       "删除分组：" + result.Before.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: managementGroupDeleteOperationLogChanges(
			result.Before.Name,
			result.AffectedRouteStrategies,
		),
		Metadata: managementGroupDeleteOperationLogMetadata(
			result.AffectedRouteStrategies,
			affectedStrategies,
		),
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Targets: managementGroupDeleteOperationLogTargets(
			result.OwnerSystemAccountID,
			result.AffectedRouteStrategies,
		),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  result.OwnerSystemAccountID,
			VisibilityReason: "resource_owner",
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func managementGroupDeleteOperationLogStrategies(
	group port.ManagementGroupMutationSummary,
	strategies []port.ManagementGroupDeletedRouteStrategy,
) []managementGroupDeletedRouteStrategyLogEntry {
	limit := min(len(strategies), managementGroupDeleteOperationLogStrategyLimit)
	result := make([]managementGroupDeletedRouteStrategyLogEntry, 0, limit)
	for _, strategy := range strategies[:limit] {
		result = append(result, managementGroupDeletedRouteStrategyLogEntry{
			RouteStrategyID:   strategy.ID,
			RouteStrategyName: strategy.Name,
			RemovedGroupID:    group.ID,
			RemovedGroupName:  group.Name,
		})
	}
	return result
}

func managementGroupDeleteOperationLogChanges(
	groupName string,
	affectedStrategies []port.ManagementGroupDeletedRouteStrategy,
) []port.OperationLogChange {
	changes := []port.OperationLogChange{{
		Field:  "deleted",
		Label:  "删除状态",
		Before: false,
		After:  true,
	}}
	if len(affectedStrategies) == 0 {
		return changes
	}
	return append(changes, port.OperationLogChange{
		Field: "affectedRouteStrategies",
		Label: "影响的策略路由",
		After: managementGroupDeleteAffectedRouteStrategySummary(groupName, affectedStrategies),
	})
}

func managementGroupDeleteAffectedRouteStrategySummary(
	groupName string,
	strategies []port.ManagementGroupDeletedRouteStrategy,
) string {
	limit := min(len(strategies), 3)
	parts := make([]string, 0, limit+1)
	for _, strategy := range strategies[:limit] {
		parts = append(parts, strategy.Name+"：移除分组 "+groupName)
	}
	if len(strategies) > limit {
		parts = append(parts, "另有 "+fmt.Sprintf("%d", len(strategies)-limit)+" 个策略路由受影响")
	}
	return strings.Join(parts, "；")
}

func managementGroupDeleteOperationLogMetadata(
	strategies []port.ManagementGroupDeletedRouteStrategy,
	entries []managementGroupDeletedRouteStrategyLogEntry,
) map[string]any {
	if len(strategies) == 0 {
		return nil
	}
	return map[string]any{
		"affectedRouteStrategyCount": len(strategies),
		"affectedRouteStrategies":    entries,
	}
}

func managementGroupDeleteOperationLogTargets(
	ownerSystemAccountID string,
	strategies []port.ManagementGroupDeletedRouteStrategy,
) []port.OperationLogTargetInput {
	limit := min(len(strategies), managementGroupDeleteOperationLogStrategyLimit)
	targets := make([]port.OperationLogTargetInput, 0, limit)
	for _, strategy := range strategies[:limit] {
		targets = append(targets, port.OperationLogTargetInput{
			TargetType:                 "route_strategy",
			TargetID:                   strategy.ID,
			TargetName:                 strategy.Name,
			TargetOwnerSystemAccountID: ownerSystemAccountID,
			Relation:                   "affected",
		})
	}
	return targets
}
