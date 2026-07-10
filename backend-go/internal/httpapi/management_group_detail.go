package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementgroups"
)

type managementGroupDetailService interface {
	Detail(r *http.Request, input managementgroups.DetailInput) (managementgroups.DetailResult, error)
}

type managementGroupDetailServiceAdapter struct {
	service *managementgroups.Service
}

func (s managementGroupDetailServiceAdapter) Detail(
	r *http.Request,
	input managementgroups.DetailInput,
) (managementgroups.DetailResult, error) {
	return s.service.Detail(r.Context(), input)
}

func NewManagementGroupDetailHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupDetailHandler(managementGroupDetailServiceFrom(service), managementGroupScopeAdmin)
}

func NewManagementMyGroupDetailHandler(service *managementgroups.Service) http.Handler {
	return newManagementGroupDetailHandler(managementGroupDetailServiceFrom(service), managementGroupScopeSelf)
}

func managementGroupDetailServiceFrom(service *managementgroups.Service) managementGroupDetailService {
	if service == nil {
		return nil
	}
	return managementGroupDetailServiceAdapter{service: service}
}

func newManagementGroupDetailHandler(
	service managementGroupDetailService,
	scope managementGroupOptionScope,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
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
		result, err := service.Detail(r, managementgroups.DetailInput{
			ActorSystemAccountID: authContext.SystemAccountID,
			ActorRole:            authContext.Role,
			SystemAccountID:      systemAccountID,
			SelfOnly:             selfOnly,
			GroupID:              chi.URLParam(r, "id"),
		})
		switch {
		case errors.Is(err, managementgroups.ErrGroupNotFound):
			writeMessageError(w, http.StatusNotFound, "分组不存在")
		case err != nil:
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		default:
			writeData(w, http.StatusOK, result)
		}
	})
}

func managementGroupDetailSystemAccountID(values url.Values) (string, string, bool) {
	items, exists := values["systemAccountId"]
	if !exists {
		return "", "", true
	}
	if len(items) != 1 {
		return "", "Expected string, received array", false
	}
	value := strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
	if value == "" {
		return "", "系统账号 ID 不能为空", false
	}
	if value == "all" {
		return "", "", true
	}
	return value, "", true
}
