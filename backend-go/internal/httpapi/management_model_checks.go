package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementmodelchecks"
)

type managementModelCheckScope int

const (
	managementModelCheckScopeAdmin managementModelCheckScope = iota
	managementModelCheckScopeSelf
)

type managementModelCheckService interface {
	Options() managementmodelchecks.OptionsResult
	List(r *http.Request, input managementmodelchecks.ListInput) (managementmodelchecks.ListResult, error)
	Active(r *http.Request, actorSystemAccountID string) (managementmodelchecks.ActiveRunSummary, bool, error)
	Detail(r *http.Request, input managementmodelchecks.DetailInput) (managementmodelchecks.RunDetail, bool, error)
}

type managementModelCheckServiceAdapter struct {
	service *managementmodelchecks.Service
}

func (a managementModelCheckServiceAdapter) Options() managementmodelchecks.OptionsResult {
	return managementmodelchecks.Options()
}

func (a managementModelCheckServiceAdapter) List(r *http.Request, input managementmodelchecks.ListInput) (managementmodelchecks.ListResult, error) {
	return a.service.List(r.Context(), input)
}

func (a managementModelCheckServiceAdapter) Active(r *http.Request, actorSystemAccountID string) (managementmodelchecks.ActiveRunSummary, bool, error) {
	return a.service.Active(r.Context(), actorSystemAccountID)
}

func (a managementModelCheckServiceAdapter) Detail(r *http.Request, input managementmodelchecks.DetailInput) (managementmodelchecks.RunDetail, bool, error) {
	return a.service.Detail(r.Context(), input)
}

func NewManagementModelCheckOptionsHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckOptionsHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeAdmin)
}

func NewManagementMyModelCheckOptionsHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckOptionsHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeSelf)
}

func NewManagementModelCheckActiveHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckActiveHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeAdmin)
}

func NewManagementMyModelCheckActiveHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckActiveHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeSelf)
}

func NewManagementModelCheckListHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckListHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeAdmin)
}

func NewManagementMyModelCheckListHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckListHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeSelf)
}

func NewManagementModelCheckDetailHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckDetailHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeAdmin)
}

func NewManagementMyModelCheckDetailHandler(service *managementmodelchecks.Service) http.Handler {
	return newManagementModelCheckDetailHandler(managementModelCheckServiceFrom(service), managementModelCheckScopeSelf)
}

func managementModelCheckServiceFrom(service *managementmodelchecks.Service) managementModelCheckService {
	if service == nil {
		return nil
	}
	return managementModelCheckServiceAdapter{service: service}
}

func newManagementModelCheckOptionsHandler(service managementModelCheckService, scope managementModelCheckScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, allowed, valid := managementModelCheckRequestScope(r, scope, false)
		if !allowed && !valid {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, service.Options())
	})
}

func newManagementModelCheckActiveHandler(service managementModelCheckService, scope managementModelCheckScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, _, allowed, valid := managementModelCheckRequestScope(r, scope, true)
		if !allowed && !valid {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, found, err := service.Active(r, auth.SystemAccountID)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !found {
			writeData(w, http.StatusOK, nil)
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementModelCheckListHandler(service managementModelCheckService, scope managementModelCheckScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, systemAccountID, allowed, valid := managementModelCheckRequestScope(r, scope, false)
		if !allowed && !valid {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input := managementModelCheckListInput(r.URL.Query())
		input.SystemAccountID = systemAccountID
		input.IncludeSystemAccountFields = scope == managementModelCheckScopeAdmin
		result, err := service.List(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementModelCheckDetailHandler(service managementModelCheckService, scope managementModelCheckScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, systemAccountID, allowed, valid := managementModelCheckRequestScope(r, scope, true)
		if !allowed && !valid {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, found, err := service.Detail(r, managementmodelchecks.DetailInput{
			ID:                         chi.URLParam(r, "id"),
			SystemAccountID:            systemAccountID,
			IncludeSystemAccountFields: scope == managementModelCheckScopeAdmin,
		})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "模型检测记录不存在")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementModelCheckRequestScope(r *http.Request, scope managementModelCheckScope, strict bool) (managementauth.Context, string, bool, bool) {
	auth, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(auth.SystemAccountID) == "" {
		return managementauth.Context{}, "", false, false
	}
	if scope == managementModelCheckScopeSelf {
		return auth, auth.SystemAccountID, true, true
	}
	if !managementauth.IsAdminRole(auth.Role) {
		return auth, "", false, true
	}
	values, exists := r.URL.Query()["systemAccountId"]
	if strict && exists && (len(values) != 1 || strings.TrimSpace(values[0]) == "") {
		return auth, "", true, false
	}
	systemAccountID := firstManagementQueryText(r.URL.Query(), "systemAccountId")
	if systemAccountID == "all" {
		systemAccountID = ""
	}
	return auth, systemAccountID, true, true
}

func managementModelCheckListInput(values url.Values) managementmodelchecks.ListInput {
	page, _ := managementModelCheckIntegerQueryValue(values, "page")
	pageSize, pageSizeProvided := managementModelCheckIntegerQueryValue(values, "pageSize")
	return managementmodelchecks.ListInput{
		Page: page, PageSize: pageSize, PageSizeProvided: pageSizeProvided,
		TargetType: firstManagementQueryText(values, "targetType"),
		TargetID:   firstManagementQueryText(values, "targetId"),
		Model:      firstManagementQueryText(values, "model"),
		Level:      firstManagementQueryText(values, "level"),
		Status:     firstManagementQueryText(values, "status"),
		StartAt:    firstManagementQueryText(values, "startAt"),
		EndAt:      firstManagementQueryText(values, "endAt"),
	}
}

func managementModelCheckIntegerQueryValue(values url.Values, key string) (int, bool) {
	items := values[key]
	if len(items) == 0 {
		return 0, false
	}
	text := strings.TrimSpace(items[0])
	if text == "" {
		return 0, false
	}
	end := 0
	if text[0] == '+' || text[0] == '-' {
		end++
	}
	digitStart := end
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == digitStart {
		return 0, false
	}
	value, err := strconv.ParseInt(text[:end], 10, 64)
	if errors.Is(err, strconv.ErrRange) {
		if text[0] == '-' {
			return -int(^uint(0)>>1) - 1, true
		}
		return int(^uint(0) >> 1), true
	}
	if err != nil {
		return 0, false
	}
	return int(value), true
}
