package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
	"juhe-ai/backend-go/internal/store/port"
)

type managementSystemTeamService interface {
	Create(ctx context.Context, input managementsystemteams.CreateInput) (managementsystemteams.Summary, error)
}

func NewManagementSystemTeamCreateHandlerWithOperationLog(service *managementsystemteams.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementSystemTeamCreateHandler(service, newManagementOperationLogOptions(opts))
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

type managementSystemTeamCreatePayload struct {
	Name        string
	Description *string
	Status      string
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
