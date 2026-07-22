package httpapi

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementruntimeloggrep"
)

type managementRuntimeLogGrepService interface {
	Grep(context.Context, managementruntimeloggrep.Input) managementruntimeloggrep.Result
	Runtime(context.Context) (managementruntimeloggrep.Runtime, error)
}

func NewManagementRuntimeLogGrepHandler(service *managementruntimeloggrep.Service) http.Handler {
	return newManagementRuntimeLogGrepHandler(service)
}

func newManagementRuntimeLogGrepHandler(service managementRuntimeLogGrepService) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), managementRuntimeLogRequestTimeout)
		defer cancel()
		path := strings.TrimRight(r.URL.Path, "/")
		if strings.HasSuffix(path, "/runtime-logs/grep-options") {
			runtime, err := service.Runtime(ctx)
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			writeData(w, http.StatusOK, runtime)
			return
		}
		if strings.HasSuffix(path, "/runtime-logs/grep") {
			writeData(w, http.StatusOK, service.Grep(ctx, parseManagementRuntimeLogGrepQuery(r.URL.Query())))
			return
		}
		writeError(w, http.StatusNotFound, "接口不存在")
	})
}

func parseManagementRuntimeLogGrepQuery(values url.Values) managementruntimeloggrep.Input {
	keywords := append([]string(nil), values["keywords"]...)
	keywords = append(keywords, values["keyword"]...)
	startAt, endAt := managementRuntimeLogDateTimeRangeQueryValue(
		managementRuntimeLogQueryText(values, "startAt"),
		managementRuntimeLogQueryText(values, "endAt"),
	)
	return managementruntimeloggrep.Input{
		Keywords: keywords,
		Limit:    managementRuntimeLogGrepLimit(values),
		StartAt:  startAt,
		EndAt:    endAt,
	}
}

func managementRuntimeLogGrepLimit(values url.Values) int {
	items := values["limit"]
	if len(items) == 0 {
		return 0
	}
	text := strings.TrimFunc(items[0], managementGroupListECMAScriptWhitespace)
	value, ok := managementGroupListNumber(text)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return int(math.Trunc(value))
}
