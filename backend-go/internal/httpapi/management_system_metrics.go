package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementstats"
)

const managementSystemMetricsRequestTimeout = 120 * time.Second

var managementSystemMetricsDatePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type managementSystemMetricsService interface {
	SystemMetrics(context.Context, managementstats.SystemMetricsQuery) (managementstats.SystemMetricsOverview, error)
}

func NewManagementSystemMetricsHandler(service *managementstats.Service) http.Handler {
	return newManagementSystemMetricsHandler(service)
}

func newManagementSystemMetricsHandler(service managementSystemMetricsService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" || service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		query, message := parseManagementSystemMetricsQuery(r.URL.Query())
		if message != "" {
			writeMessageError(w, http.StatusBadRequest, message)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), managementSystemMetricsRequestTimeout)
		defer cancel()
		result, err := service.SystemMetrics(ctx, query)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func parseManagementSystemMetricsQuery(values url.Values) (managementstats.SystemMetricsQuery, string) {
	startDate, message := managementSystemMetricsDateQuery(values, "startDate", "开始")
	if message != "" {
		return managementstats.SystemMetricsQuery{}, message
	}
	endDate, message := managementSystemMetricsDateQuery(values, "endDate", "结束")
	if message != "" {
		return managementstats.SystemMetricsQuery{}, message
	}
	return managementstats.SystemMetricsQuery{StartDate: startDate, EndDate: endDate}, ""
}

func managementSystemMetricsDateQuery(values url.Values, key string, label string) (string, string) {
	items, exists := values[key]
	if !exists {
		return "", ""
	}
	if len(items) != 1 {
		return "", "监控日期范围不合法"
	}
	value := strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
	if !managementSystemMetricsDatePattern.MatchString(value) {
		return "", label + "日期格式应为 YYYY-MM-DD"
	}
	return value, ""
}
