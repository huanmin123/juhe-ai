package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementoperationlogs"
)

type managementOperationLogScope int

const (
	managementOperationLogScopeAdmin managementOperationLogScope = iota
	managementOperationLogScopeSelf
)

type managementOperationLogService interface {
	List(r *http.Request, input managementoperationlogs.ListInput) (managementoperationlogs.ListResult, error)
	Detail(r *http.Request, input managementoperationlogs.DetailInput) (managementoperationlogs.Detail, bool, error)
}

type managementOperationLogServiceAdapter struct {
	service *managementoperationlogs.Service
}

type managementOperationLogListResponse struct {
	Items    []managementOperationLogListItem `json:"items"`
	Total    int                              `json:"total"`
	HasMore  bool                             `json:"hasMore"`
	Page     int                              `json:"page"`
	PageSize int                              `json:"pageSize"`
}

type managementOperationLogListItem struct {
	ID                              string `json:"id"`
	TraceID                         string `json:"traceId,omitempty"`
	ActorSystemAccountID            string `json:"actorSystemAccountId"`
	ActorDisplayName                string `json:"actorDisplayName,omitempty"`
	ActorSystemAccountName          string `json:"actorSystemAccountName,omitempty"`
	OperationScopeSystemAccountID   string `json:"operationScopeSystemAccountId,omitempty"`
	OperationScopeSystemAccountName string `json:"operationScopeSystemAccountName,omitempty"`
	Module                          string `json:"module"`
	Action                          string `json:"action"`
	Summary                         string `json:"summary"`
	CreatedAt                       string `json:"createdAt"`
}

func (s managementOperationLogServiceAdapter) List(r *http.Request, input managementoperationlogs.ListInput) (managementoperationlogs.ListResult, error) {
	return s.service.List(r.Context(), input)
}

func (s managementOperationLogServiceAdapter) Detail(r *http.Request, input managementoperationlogs.DetailInput) (managementoperationlogs.Detail, bool, error) {
	return s.service.Detail(r.Context(), input)
}

func NewManagementOperationLogsHandler(service *managementoperationlogs.Service) http.Handler {
	return newManagementOperationLogsHandler(managementOperationLogServiceAdapter{service: service}, managementOperationLogScopeAdmin)
}

func NewManagementMyOperationLogsHandler(service *managementoperationlogs.Service) http.Handler {
	return newManagementOperationLogsHandler(managementOperationLogServiceAdapter{service: service}, managementOperationLogScopeSelf)
}

func newManagementOperationLogsHandler(service managementOperationLogService, scope managementOperationLogScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		input, allowed := managementOperationLogListInput(authContext, r.URL.Query(), scope)
		if !allowed {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if id := strings.TrimSpace(chi.URLParam(r, "id")); id != "" {
			detail, found, err := service.Detail(r, managementoperationlogs.DetailInput{
				ID:                    id,
				ViewerSystemAccountID: input.ViewerSystemAccountID,
			})
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !found {
				writeMessageError(w, http.StatusNotFound, "操作日志不存在")
				return
			}
			writeData(w, http.StatusOK, detail)
			return
		}
		result, err := service.List(r, input)
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, managementOperationLogListResponseFromService(result))
	})
}

func managementOperationLogListResponseFromService(result managementoperationlogs.ListResult) managementOperationLogListResponse {
	items := make([]managementOperationLogListItem, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, managementOperationLogListItem{
			ID:                              item.ID,
			TraceID:                         item.TraceID,
			ActorSystemAccountID:            item.ActorSystemAccountID,
			ActorDisplayName:                item.ActorDisplayName,
			ActorSystemAccountName:          item.ActorSystemAccountName,
			OperationScopeSystemAccountID:   item.OperationScopeSystemAccountID,
			OperationScopeSystemAccountName: item.OperationScopeSystemAccountName,
			Module:                          item.Module,
			Action:                          item.Action,
			Summary:                         item.Summary,
			CreatedAt:                       item.CreatedAt,
		})
	}
	return managementOperationLogListResponse{
		Items:    items,
		Total:    result.Total,
		HasMore:  result.HasMore,
		Page:     result.Page,
		PageSize: result.PageSize,
	}
}

func managementOperationLogListInput(
	authContext managementauth.Context,
	values url.Values,
	scope managementOperationLogScope,
) (managementoperationlogs.ListInput, bool) {
	input := parseManagementOperationLogListQuery(values)
	switch scope {
	case managementOperationLogScopeAdmin:
		if !managementauth.IsAdminRole(authContext.Role) {
			return managementoperationlogs.ListInput{}, false
		}
	case managementOperationLogScopeSelf:
		input.ViewerSystemAccountID = authContext.SystemAccountID
		input.ActorSystemAccountID = ""
		input.AffectedSystemAccountID = ""
		input.OperationScopeSystemAccountID = ""
	}
	return input, true
}

func parseManagementOperationLogListQuery(values url.Values) managementoperationlogs.ListInput {
	startAt, endAt := managementDateTimeRangeQueryValue(
		firstManagementQueryText(values, "startAt"),
		firstManagementQueryText(values, "endAt"),
	)
	return managementoperationlogs.ListInput{
		Page:                          managementIntegerQueryValue(values, "page"),
		PageSize:                      managementIntegerQueryValue(values, "pageSize"),
		SummaryKeyword:                firstManagementQueryText(values, "summaryKeyword"),
		Module:                        firstManagementQueryText(values, "module"),
		Action:                        firstManagementQueryText(values, "action"),
		ResourceType:                  firstManagementQueryText(values, "resourceType"),
		ResourceID:                    firstManagementQueryText(values, "resourceId"),
		TraceID:                       firstManagementQueryText(values, "traceId"),
		StartAt:                       startAt,
		EndAt:                         endAt,
		ActorSystemAccountID:          firstManagementQueryText(values, "actorSystemAccountId"),
		AffectedSystemAccountID:       firstManagementQueryText(values, "affectedSystemAccountId"),
		OperationScopeSystemAccountID: firstManagementQueryText(values, "operationScopeSystemAccountId"),
	}
}

func managementDateTimeRangeQueryValue(startText string, endText string) (time.Time, time.Time) {
	startAt := managementDateTimeQueryValue(startText)
	endAt := managementDateTimeQueryValue(endText)
	if !startAt.IsZero() && !endAt.IsZero() && startAt.After(endAt) {
		return endAt, startAt
	}
	return startAt, endAt
}

func managementDateTimeQueryValue(text string) time.Time {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		value, err := time.Parse(layout, text)
		if err == nil {
			return value.UTC()
		}
	}
	return time.Time{}
}
