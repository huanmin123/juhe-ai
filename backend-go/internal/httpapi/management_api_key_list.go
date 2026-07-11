package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementapikeys"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAPIKeyScope int

const (
	managementAPIKeyScopeAdmin managementAPIKeyScope = iota
	managementAPIKeyScopeSelf
)

type managementAPIKeyListService interface {
	List(r *http.Request, input managementapikeys.ListInput) (managementapikeys.ListResult, error)
}

type managementAPIKeyListServiceAdapter struct {
	service *managementapikeys.Service
}

func (s managementAPIKeyListServiceAdapter) List(
	r *http.Request,
	input managementapikeys.ListInput,
) (managementapikeys.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func NewManagementAPIKeyListHandler(service *managementapikeys.Service) http.Handler {
	return newManagementAPIKeyListHandler(managementAPIKeyListServiceFrom(service), managementAPIKeyScopeAdmin)
}

func NewManagementMyAPIKeyListHandler(service *managementapikeys.Service) http.Handler {
	return newManagementAPIKeyListHandler(managementAPIKeyListServiceFrom(service), managementAPIKeyScopeSelf)
}

func managementAPIKeyListServiceFrom(service *managementapikeys.Service) managementAPIKeyListService {
	if service == nil {
		return nil
	}
	return managementAPIKeyListServiceAdapter{service: service}
}

func newManagementAPIKeyListHandler(
	service managementAPIKeyListService,
	scope managementAPIKeyScope,
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
		result, err := service.List(r, managementAPIKeyListInput(authContext, r.URL.Query(), scope))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementAPIKeyListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementAPIKeyScope,
) managementapikeys.ListInput {
	page, _ := managementGroupListIntegerQueryValue(values, "page")
	pageSize, pageSizeProvided := managementGroupListIntegerQueryValue(values, "pageSize")
	input := managementapikeys.ListInput{
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorRole:            authContext.Role,
		Page:                 page,
		PageSize:             pageSize,
		PageSizeProvided:     pageSizeProvided,
		Keyword:              firstManagementQueryText(values, "keyword"),
		Status:               managementAPIKeyStatusQueryValue(values),
		RouteStrategyID:      firstManagementQueryText(values, "routeStrategyId"),
	}
	switch scope {
	case managementAPIKeyScopeAdmin:
		systemAccountID := firstManagementQueryText(values, "systemAccountId")
		if systemAccountID != "all" {
			input.SystemAccountID = systemAccountID
		}
	case managementAPIKeyScopeSelf:
		input.SystemAccountID = authContext.SystemAccountID
		input.SelfOnly = true
	}
	return input
}

func managementAPIKeyStatusQueryValue(values url.Values) string {
	switch firstManagementQueryText(values, "status") {
	case "active":
		return "active"
	case "disabled":
		return "disabled"
	default:
		return ""
	}
}
