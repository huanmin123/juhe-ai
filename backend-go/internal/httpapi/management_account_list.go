package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementaccountlist"
	"juhe-ai/backend-go/internal/modules/managementauth"
)

type managementAccountListScope int

const (
	managementAccountListScopeAdmin managementAccountListScope = iota
	managementAccountListScopeSelf
)

type managementAccountListService interface {
	List(*http.Request, managementaccountlist.Input) (managementaccountlist.Result, error)
}

type managementAccountListServiceAdapter struct {
	service *managementaccountlist.Service
}

func (s managementAccountListServiceAdapter) List(r *http.Request, input managementaccountlist.Input) (managementaccountlist.Result, error) {
	return s.service.List(r.Context(), input)
}

func NewManagementAccountListHandler(service *managementaccountlist.Service) http.Handler {
	return newManagementAccountListHandler(accountListServiceFrom(service), managementAccountListScopeAdmin)
}

func NewManagementMyAccountListHandler(service *managementaccountlist.Service) http.Handler {
	return newManagementAccountListHandler(accountListServiceFrom(service), managementAccountListScopeSelf)
}

func accountListServiceFrom(service *managementaccountlist.Service) managementAccountListService {
	if service == nil {
		return nil
	}
	return managementAccountListServiceAdapter{service: service}
}

func newManagementAccountListHandler(service managementAccountListService, scope managementAccountListScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementAccountListScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := service.List(r, managementAccountListInput(authContext, r.URL.Query(), scope))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeData(w, http.StatusOK, result)
	})
}

func managementAccountListInput(authContext managementauth.Context, values url.Values, scope managementAccountListScope) managementaccountlist.Input {
	page, _ := strconv.Atoi(firstManagementQueryText(values, "page"))
	pageSizeText := firstManagementQueryText(values, "pageSize")
	pageSize, pageSizeErr := strconv.Atoi(pageSizeText)
	input := managementaccountlist.Input{
		ActorSystemAccountID: authContext.SystemAccountID, ActorRole: authContext.Role,
		Page: page, PageSize: pageSize, PageSizeProvided: pageSizeText != "" && pageSizeErr == nil,
		Keyword: firstManagementQueryText(values, "keyword"), ProviderCode: firstManagementQueryText(values, "providerCode"),
		GroupID: firstManagementQueryText(values, "groupId"), Type: firstManagementQueryText(values, "type"),
		Statuses: accountListStatusValues(values), Schedulable: firstManagementQueryText(values, "schedulable"),
		TagIDs: accountListTagValues(values),
		Sorts:  accountListSortValues(values),
	}
	if scope == managementAccountListScopeSelf {
		input.SystemAccountID = authContext.SystemAccountID
		input.SelfOnly = true
	} else if value := firstManagementQueryText(values, "systemAccountId"); value != "all" {
		input.SystemAccountID = value
	}
	return input
}

func accountListStatusValues(values url.Values) []string {
	allowed := map[string]struct{}{"active": {}, "pending_test": {}, "disabled": {}, "error": {}, "rate_limited": {}, "temporary_unavailable": {}}
	result := []string{}
	for _, raw := range values["status"] {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if _, ok := allowed[value]; ok {
				result = append(result, value)
			}
		}
	}
	return result
}

func accountListSortValues(values url.Values) []managementaccountlist.Sort {
	allowed := map[string]struct{}{"priority": {}, "superPriority": {}, "fallback": {}, "qualityScore": {}, "name": {}, "type": {}, "providerCode": {}, "systemAccount": {}, "concurrency": {}, "status": {}, "accountExpiresAt": {}, "lastUsedAt": {}}
	for _, raw := range values["sorts"] {
		for _, part := range strings.Split(raw, ",") {
			pieces := strings.Split(strings.TrimSpace(part), ":")
			if len(pieces) != 2 {
				continue
			}
			field, order := strings.TrimSpace(pieces[0]), strings.TrimSpace(pieces[1])
			if _, ok := allowed[field]; ok && (order == "asc" || order == "desc") {
				return []managementaccountlist.Sort{{Field: field, Order: order}}
			}
		}
	}
	return nil
}

func accountListTagValues(values url.Values) []string {
	result := make([]string, 0, 100)
	for _, raw := range values["tagIds"] {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if value != "" {
				result = append(result, value)
			}
			if len(result) == 100 {
				return result
			}
		}
	}
	return result
}
