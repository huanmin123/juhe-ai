package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
	"juhe-ai/backend-go/internal/store/port"
)

type managementSystemTeamService interface {
	List(ctx context.Context, input managementsystemteams.ListInput) (managementsystemteams.ListResult, error)
	Detail(ctx context.Context, teamID string, systemAccountID string) (managementsystemteams.Detail, bool, error)
	Create(ctx context.Context, input managementsystemteams.CreateInput) (managementsystemteams.Summary, error)
	Update(ctx context.Context, input managementsystemteams.UpdateInput) (managementsystemteams.UpdateResult, bool, error)
}

func NewManagementSystemTeamsHandler(service *managementsystemteams.Service) http.Handler {
	return newManagementSystemTeamsHandler(service, managementSystemTeamScopeAdmin)
}

func NewManagementMySystemTeamsHandler(service *managementsystemteams.Service) http.Handler {
	return newManagementSystemTeamsHandler(service, managementSystemTeamScopeSelf)
}

func NewManagementSystemTeamCreateHandlerWithOperationLog(service *managementsystemteams.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementSystemTeamCreateHandler(service, newManagementOperationLogOptions(opts))
}

func NewManagementSystemTeamPatchHandlerWithOperationLog(service *managementsystemteams.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementSystemTeamPatchHandler(service, newManagementOperationLogOptions(opts))
}

type managementSystemTeamScope string

const (
	managementSystemTeamScopeAdmin managementSystemTeamScope = "admin"
	managementSystemTeamScopeSelf  managementSystemTeamScope = "self"
)

func newManagementSystemTeamsHandler(service managementSystemTeamService, scope managementSystemTeamScope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if scope == managementSystemTeamScopeAdmin && !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		systemAccountID, validScope := managementSystemTeamScopedSystemAccountID(authContext, r.URL.Query(), scope)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		teamID := strings.TrimSpace(chi.URLParam(r, "id"))
		if teamID != "" {
			result, found, err := service.Detail(r.Context(), teamID, systemAccountID)
			if errors.Is(err, managementsystemteams.ErrSystemTeamReadInvalid) {
				writeMessageError(w, http.StatusBadRequest, "团队 ID 不合法")
				return
			}
			if err != nil {
				writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
			if !found {
				writeMessageError(w, http.StatusNotFound, "团队不存在")
				return
			}
			writeData(w, http.StatusOK, result)
			return
		}
		result, err := service.List(r.Context(), managementSystemTeamListInput(systemAccountID, r.URL.Query()))
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		writeData(w, http.StatusOK, result)
	})
}

func newManagementSystemTeamCreateHandler(service managementSystemTeamService, opts managementOperationLogOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if !validManagementScopeQuery(r) {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}

		payload, ok := decodeManagementSystemTeamCreatePayload(w, r)
		if !ok {
			return
		}

		result, err := service.Create(r.Context(), managementsystemteams.CreateInput{
			Name:        payload.Name,
			Description: payload.Description,
			Status:      payload.Status,
			CreatedBy:   authContext.SystemAccountID,
		})
		if errors.Is(err, managementsystemteams.ErrSystemTeamCreateInvalid) {
			writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
			return
		}
		if errors.Is(err, managementsystemteams.ErrSystemTeamNameExists) {
			writeMessageError(w, http.StatusConflict, "团队名称已存在")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "创建团队失败")
			return
		}

		recordSystemTeamCreateOperationLog(r, authContext, result, opts)
		writeData(w, http.StatusCreated, result)
	})
}

func newManagementSystemTeamPatchHandler(service managementSystemTeamService, opts managementOperationLogOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok || strings.TrimSpace(authContext.SystemAccountID) == "" {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if !managementauth.IsAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		if service == nil {
			writeMessageError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		systemAccountID, validScope := managementSystemTeamScopedSystemAccountID(authContext, r.URL.Query(), managementSystemTeamScopeAdmin)
		if !validScope {
			writeMessageError(w, http.StatusBadRequest, "查询参数不合法")
			return
		}
		teamID := strings.TrimSpace(chi.URLParam(r, "id"))
		if teamID == "" {
			writeMessageError(w, http.StatusBadRequest, "团队 ID 不合法")
			return
		}
		payload, ok := decodeManagementSystemTeamPatchPayload(w, r)
		if !ok {
			return
		}

		result, found, err := service.Update(r.Context(), managementsystemteams.UpdateInput{
			TeamID:          teamID,
			SystemAccountID: systemAccountID,
			Name:            payload.Name,
			HasDescription:  payload.HasDescription,
			Description:     payload.Description,
			Status:          payload.Status,
			UpdatedBy:       authContext.SystemAccountID,
		})
		if errors.Is(err, managementsystemteams.ErrSystemTeamUpdateInvalid) {
			writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
			return
		}
		if errors.Is(err, managementsystemteams.ErrSystemTeamNameExists) {
			writeMessageError(w, http.StatusConflict, "团队名称已存在")
			return
		}
		if err != nil {
			if isManagementSystemTeamUserFacingError(err) {
				writeMessageError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "更新团队失败")
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "团队不存在")
			return
		}

		recordSystemTeamUpdateOperationLog(r, authContext, result, opts)
		writeData(w, http.StatusOK, result.Team)
	})
}

func managementSystemTeamScopedSystemAccountID(authContext managementauth.Context, values url.Values, scope managementSystemTeamScope) (string, bool) {
	switch scope {
	case managementSystemTeamScopeSelf:
		return authContext.SystemAccountID, true
	case managementSystemTeamScopeAdmin:
		rawValues, exists := values["systemAccountId"]
		if !exists {
			return "", true
		}
		var selected string
		for _, raw := range rawValues {
			value := strings.TrimSpace(raw)
			if value == "" {
				return "", false
			}
			if value == "all" {
				continue
			}
			if selected == "" {
				selected = value
			}
		}
		return selected, true
	default:
		return "", false
	}
}

func managementSystemTeamListInput(systemAccountID string, values url.Values) managementsystemteams.ListInput {
	return managementsystemteams.ListInput{
		SystemAccountID: systemAccountID,
		Keyword:         firstManagementQueryText(values, "keyword"),
		Page:            managementIntegerQueryValue(values, "page"),
		PageSize:        managementIntegerQueryValue(values, "pageSize"),
	}
}

type managementSystemTeamCreatePayload struct {
	Name        string
	Description *string
	Status      string
}

type managementSystemTeamPatchPayload struct {
	Name           *string
	HasDescription bool
	Description    *string
	Status         *string
}

func decodeManagementSystemTeamCreatePayload(w http.ResponseWriter, r *http.Request) (managementSystemTeamCreatePayload, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementSystemTeamCreatePayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementSystemTeamCreatePayload{}, false
	}

	name, _ := payload["name"].(string)
	var description *string
	if raw, exists := payload["description"]; exists {
		if text, ok := raw.(string); ok {
			description = &text
		} else if raw != nil {
			writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
			return managementSystemTeamCreatePayload{}, false
		}
	}
	var status string
	if raw, exists := payload["status"]; exists {
		if text, ok := raw.(string); ok {
			if text != "active" && text != "disabled" {
				writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
				return managementSystemTeamCreatePayload{}, false
			}
			status = text
		} else {
			writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
			return managementSystemTeamCreatePayload{}, false
		}
	}
	for field := range payload {
		switch field {
		case "name", "description", "status":
			continue
		default:
			writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
			return managementSystemTeamCreatePayload{}, false
		}
	}
	if strings.TrimSpace(name) == "" {
		writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
		return managementSystemTeamCreatePayload{}, false
	}
	return managementSystemTeamCreatePayload{Name: name, Description: description, Status: status}, true
}

func decodeManagementSystemTeamPatchPayload(w http.ResponseWriter, r *http.Request) (managementSystemTeamPatchPayload, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementSystemTeamPatchPayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementSystemTeamPatchPayload{}, false
	}

	result := managementSystemTeamPatchPayload{}
	for field, raw := range payload {
		switch field {
		case "name":
			text, ok := raw.(string)
			if !ok || strings.TrimSpace(text) == "" {
				writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
				return managementSystemTeamPatchPayload{}, false
			}
			result.Name = &text
		case "description":
			result.HasDescription = true
			if raw == nil {
				continue
			}
			text, ok := raw.(string)
			if !ok {
				writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
				return managementSystemTeamPatchPayload{}, false
			}
			result.Description = &text
		case "status":
			text, ok := raw.(string)
			if !ok || (text != "active" && text != "disabled") {
				writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
				return managementSystemTeamPatchPayload{}, false
			}
			result.Status = &text
		default:
			writeMessageError(w, http.StatusBadRequest, "团队参数不合法")
			return managementSystemTeamPatchPayload{}, false
		}
	}
	return result, true
}

func validManagementScopeQuery(r *http.Request) bool {
	rawValues, exists := r.URL.Query()["systemAccountId"]
	if !exists {
		return true
	}
	for _, raw := range rawValues {
		if strings.TrimSpace(raw) == "" {
			return false
		}
	}
	return true
}

func recordSystemTeamCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemteams.Summary,
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
		newLogID = func() string {
			return "oplog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	statusCode := http.StatusCreated
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: authContext.SystemAccountID,
		Mode:                          "admin",
		Module:                        "system_teams",
		Action:                        "create",
		OperationKey:                  "system_teams.create",
		ResourceType:                  "system_team",
		ResourceID:                    result.ID,
		ResourceName:                  result.Name,
		Summary:                       "创建系统团队：" + result.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes: []port.OperationLogChange{
			{Field: "name", Label: "团队名称", Before: nil, After: result.Name},
			{Field: "description", Label: "说明", Before: nil, After: result.Description},
			{Field: "status", Label: "状态", Before: nil, After: result.Status},
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{{
			SystemAccountID:  authContext.SystemAccountID,
			VisibilityReason: "team_creator",
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if _, err := operationlogjob.EnqueueWrite(enqueueCtx, opts.client, input); err != nil && opts.logger != nil {
		opts.logger.Warn("管理端操作日志入队失败",
			slog.String("event", "operation_log_enqueue_failed"),
			slog.String("operation_key", input.OperationKey),
			slog.String("resource_id", input.ResourceID),
			slog.String("request_id", input.TraceID),
			slog.Any("error", err),
		)
	}
}

func recordSystemTeamUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemteams.UpdateResult,
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
		newLogID = func() string {
			return "oplog_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
	}
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: authContext.SystemAccountID,
		Mode:                          "admin",
		Module:                        "system_teams",
		Action:                        "update",
		OperationKey:                  "system_teams.update",
		ResourceType:                  "system_team",
		ResourceID:                    result.Team.ID,
		ResourceName:                  result.Team.Name,
		Summary:                       "更新系统团队：" + result.Team.Name,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       systemTeamUpdateOperationChanges(result.Before, result.Team.Summary),
		Metadata: map[string]any{
			"authorizationChanged": result.AuthorizationChanged,
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Viewers:    systemTeamUpdateOperationViewers(authContext.SystemAccountID, result.Team.Members),
		CreatedAt:  now().UTC(),
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if _, err := operationlogjob.EnqueueWrite(enqueueCtx, opts.client, input); err != nil && opts.logger != nil {
		opts.logger.Warn("管理端操作日志入队失败",
			slog.String("event", "operation_log_enqueue_failed"),
			slog.String("operation_key", input.OperationKey),
			slog.String("resource_id", input.ResourceID),
			slog.String("request_id", input.TraceID),
			slog.Any("error", err),
		)
	}
}

func systemTeamUpdateOperationChanges(before managementsystemteams.Summary, after managementsystemteams.Summary) []port.OperationLogChange {
	changes := make([]port.OperationLogChange, 0, 3)
	if before.Name != after.Name {
		changes = append(changes, port.OperationLogChange{Field: "name", Label: "团队名称", Before: before.Name, After: after.Name})
	}
	if before.Description != after.Description {
		changes = append(changes, port.OperationLogChange{Field: "description", Label: "说明", Before: before.Description, After: after.Description})
	}
	if before.Status != after.Status {
		changes = append(changes, port.OperationLogChange{Field: "status", Label: "状态", Before: before.Status, After: after.Status})
	}
	return changes
}

func systemTeamUpdateOperationViewers(actorSystemAccountID string, members []managementsystemteams.MemberSummary) []port.OperationLogViewerInput {
	viewers := make([]port.OperationLogViewerInput, 0, len(members)+1)
	seen := map[string]struct{}{}
	addViewer := func(systemAccountID string, reason string) {
		systemAccountID = strings.TrimSpace(systemAccountID)
		if systemAccountID == "" {
			return
		}
		if _, ok := seen[systemAccountID]; ok {
			return
		}
		seen[systemAccountID] = struct{}{}
		viewers = append(viewers, port.OperationLogViewerInput{
			SystemAccountID:  systemAccountID,
			VisibilityReason: reason,
			DetailLevel:      "full",
		})
	}
	addViewer(actorSystemAccountID, "team_updater")
	for _, member := range members {
		addViewer(member.SystemAccountID, "team_member")
	}
	return viewers
}

func isManagementSystemTeamUserFacingError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "授权团队") || strings.Contains(message, "不能授权给资源所有者自己")
}
