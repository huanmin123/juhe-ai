package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementstats"
)

var managementStatsDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type managementStatsUsageWindowScope int

const (
	managementStatsUsageWindowScopeAdmin managementStatsUsageWindowScope = iota
	managementStatsUsageWindowScopeSelf
)

func NewManagementStatsUsageWindowHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsUsageWindowHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsUsageWindowHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsUsageWindowHandler(service, managementStatsUsageWindowScopeSelf)
}

func newManagementStatsUsageWindowHandler(service *managementstats.Service, scope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if scope == managementStatsUsageWindowScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		window, err := service.UsageWindow(r.Context())
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, window)
	})
}

func NewManagementStatsAccountUsageHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAccountUsageHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsAccountUsageHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAccountUsageHandler(service, managementStatsUsageWindowScopeSelf)
}

func NewManagementStatsAccountUsageSummaryHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAccountUsageSummaryHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsAccountUsageSummaryHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAccountUsageSummaryHandler(service, managementStatsUsageWindowScopeSelf)
}

func NewManagementStatsAccountUsageTrendHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAccountUsageTrendHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsAccountUsageTrendHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAccountUsageTrendHandler(service, managementStatsUsageWindowScopeSelf)
}

func NewManagementStatsAIPerformanceHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAIPerformanceHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsAIPerformanceHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAIPerformanceHandler(service, managementStatsUsageWindowScopeSelf)
}

func NewManagementStatsAIPerformanceSeriesHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAIPerformanceSeriesHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsAIPerformanceSeriesHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAIPerformanceSeriesHandler(service, managementStatsUsageWindowScopeSelf)
}

func NewManagementStatsAIPerformanceAccountsHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAIPerformanceAccountsHandler(service, managementStatsUsageWindowScopeAdmin)
}

func NewManagementMyStatsAIPerformanceAccountsHandler(service *managementstats.Service) http.Handler {
	return newManagementStatsAIPerformanceAccountsHandler(service, managementStatsUsageWindowScopeSelf)
}

func newManagementStatsAccountUsageHandler(service *managementstats.Service, routeScope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readScope, ok := managementStatsReadScope(w, r, routeScope)
		if !ok {
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		values := r.URL.Query()
		if _, provided := values["includeSummary"]; provided {
			writeMessageError(w, http.StatusBadRequest, "account-usage 列表不支持 includeSummary，请使用 /account-usage/summary")
			return
		}
		result, err := service.AccountUsage(r.Context(), readScope, managementstats.AccountUsageInput{
			Page: managementStatsOptionalInteger(values, "page"), PageSize: managementStatsOptionalInteger(values, "pageSize"),
			Keyword: managementStatsFirstText(values, "keyword"), StartDate: managementStatsFirstText(values, "startDate"), EndDate: managementStatsFirstText(values, "endDate"),
			AccountIDs: managementStatsIDList(values, "accountIds"),
		})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementStatsAccountUsageSummaryHandler(service *managementstats.Service, routeScope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readScope, ok := managementStatsReadScope(w, r, routeScope)
		if !ok {
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		values := r.URL.Query()
		result, err := service.AccountUsageSummary(r.Context(), readScope, managementstats.AccountUsageInput{StartDate: managementStatsFirstText(values, "startDate"), EndDate: managementStatsFirstText(values, "endDate")})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementStatsAccountUsageTrendHandler(service *managementstats.Service, routeScope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readScope, ok := managementStatsReadScope(w, r, routeScope)
		if !ok {
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		values := r.URL.Query()
		result, err := service.AccountUsageTrend(r.Context(), readScope, managementstats.AccountUsageTrendInput{StartDate: managementStatsFirstText(values, "startDate"), EndDate: managementStatsFirstText(values, "endDate"), AccountIDs: managementStatsIDList(values, "accountIds")})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementStatsAIPerformanceHandler(service *managementstats.Service, routeScope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readScope, ok := managementStatsReadScope(w, r, routeScope)
		if !ok {
			return
		}
		values := r.URL.Query()
		if managementStatsHasAccountIDs(values) {
			writeMessageError(w, http.StatusBadRequest, "AI 性能基础数据不接受 accountIds，请使用 /ai-performance/series")
			return
		}
		startDate, valid := managementStatsOptionalDate(values, "startDate")
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "开始日期格式应为 YYYY-MM-DD")
			return
		}
		endDate, valid := managementStatsOptionalDate(values, "endDate")
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "结束日期格式应为 YYYY-MM-DD")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := service.AIPerformance(r.Context(), readScope, managementstats.AIPerformanceInput{StartDate: startDate, EndDate: endDate})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementStatsAIPerformanceSeriesHandler(service *managementstats.Service, routeScope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readScope, ok := managementStatsReadScope(w, r, routeScope)
		if !ok {
			return
		}
		values := r.URL.Query()
		startDate, valid := managementStatsOptionalDate(values, "startDate")
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "开始日期格式应为 YYYY-MM-DD")
			return
		}
		endDate, valid := managementStatsOptionalDate(values, "endDate")
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "结束日期格式应为 YYYY-MM-DD")
			return
		}
		accountIDs, message, valid := managementStatsSeriesAccountIDs(values)
		if !valid {
			writeMessageError(w, http.StatusBadRequest, message)
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := service.AIPerformanceSeries(r.Context(), readScope, managementstats.AIPerformanceSeriesInput{StartDate: startDate, EndDate: endDate, AccountIDs: accountIDs})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementStatsAIPerformanceAccountsHandler(service *managementstats.Service, routeScope managementStatsUsageWindowScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readScope, ok := managementStatsReadScope(w, r, routeScope)
		if !ok {
			return
		}
		values := r.URL.Query()
		keyword, valid := managementStatsOptionalSingleText(values, "keyword")
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "AI账户筛选参数不合法")
			return
		}
		limit, valid := managementStatsOptionalBoundedInteger(values, "limit", 1, 50)
		if !valid {
			writeMessageError(w, http.StatusBadRequest, "AI账户筛选参数不合法")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		result, err := service.AIPerformanceAccounts(r.Context(), readScope, managementstats.AIPerformanceAccountsInput{Keyword: keyword, AccountIDs: managementStatsIDList(values, "accountIds"), Limit: limit})
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func managementStatsReadScope(w http.ResponseWriter, r *http.Request, routeScope managementStatsUsageWindowScope) (managementstats.ReadScope, bool) {
	authContext, ok := ManagementAuthContextFromRequest(r)
	if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
		writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
		return managementstats.ReadScope{}, false
	}
	admin := managementauth.IsAdminRole(authContext.Role)
	if routeScope == managementStatsUsageWindowScopeAdmin && !admin {
		writeMessageError(w, http.StatusForbidden, "需要管理员权限")
		return managementstats.ReadScope{}, false
	}
	target := ""
	if routeScope == managementStatsUsageWindowScopeAdmin {
		target = managementStatsFirstText(r.URL.Query(), "systemAccountId")
	}
	return managementstats.ReadScope{ActorSystemAccountID: authContext.SystemAccountID, Admin: admin, TargetSystemAccountID: target}, true
}

func managementStatsFirstText(values url.Values, key string) string {
	items := values[key]
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[0])
}

func managementStatsOptionalSingleText(values url.Values, key string) (string, bool) {
	items := values[key]
	if len(items) > 1 {
		return "", false
	}
	return managementStatsFirstText(values, key), true
}

func managementStatsOptionalInteger(values url.Values, key string) int {
	value, _ := managementGroupListIntegerQueryValue(values, key)
	return value
}

func managementStatsOptionalBoundedInteger(values url.Values, key string, low, high int) (int, bool) {
	items := values[key]
	if len(items) > 1 {
		return 0, false
	}
	if managementStatsFirstText(values, key) == "" {
		return 0, true
	}
	value, provided := managementGroupListIntegerQueryValue(values, key)
	if !provided || value < low || value > high {
		return 0, false
	}
	return value, true
}

func managementStatsOptionalDate(values url.Values, key string) (string, bool) {
	text, valid := managementStatsOptionalSingleText(values, key)
	return text, valid && (text == "" || managementStatsDatePattern.MatchString(text))
}

func managementStatsIDList(values url.Values, key string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, raw := range values[key] {
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			result = append(result, item)
		}
	}
	return result
}

func managementStatsHasAccountIDs(values url.Values) bool {
	for key := range values {
		if key == "accountIds" || strings.HasPrefix(key, "accountIds[") {
			return true
		}
	}
	return false
}

func managementStatsSeriesAccountIDs(values url.Values) ([]string, string, bool) {
	for key := range values {
		if strings.HasPrefix(key, "accountIds[") && key != "accountIds[]" {
			return nil, "accountIds 仅支持重复参数 accountIds=value", false
		}
	}
	rawValues := append(append([]string{}, values["accountIds"]...), values["accountIds[]"]...)
	if len(rawValues) < 1 || len(rawValues) > 20 {
		return nil, "accountIds 必须重复传入 1 到 20 个", false
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(rawValues))
	for _, raw := range rawValues {
		if strings.Contains(raw, ",") {
			return nil, "accountIds 不接受 CSV，必须使用重复参数", false
		}
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, "accountIds 不能为空", false
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, "", true
}
