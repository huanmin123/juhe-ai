package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemteams"
	"juhe-ai/backend-go/internal/store/port"
)

type managementSystemTeamService interface {
	List(ctx context.Context, input managementsystemteams.ListInput) (managementsystemteams.ListResult, error)
	Detail(ctx context.Context, teamID string, systemAccountID string) (managementsystemteams.Detail, bool, error)
	Create(ctx context.Context, input managementsystemteams.CreateInput) (managementsystemteams.Summary, error)
	Update(ctx context.Context, input managementsystemteams.UpdateInput) (managementsystemteams.UpdateResult, bool, error)
	AddMembers(ctx context.Context, input managementsystemteams.AddMembersInput) (managementsystemteams.AddMembersResult, bool, error)
	RemoveMember(ctx context.Context, input managementsystemteams.RemoveMemberInput) (managementsystemteams.RemoveMemberResult, bool, error)
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

func NewManagementSystemTeamMembersAddHandlerWithOperationLog(service *managementsystemteams.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementSystemTeamMembersAddHandler(service, newManagementOperationLogOptions(opts))
}

func NewManagementSystemTeamMemberDeleteHandlerWithOperationLog(service *managementsystemteams.Service, opts ManagementOperationLogOptions) http.Handler {
	return newManagementSystemTeamMemberDeleteHandler(service, newManagementOperationLogOptions(opts))
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
			writeData(w, http.StatusOK, compactManagementSystemTeamDetail(result))
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
		writeData(w, http.StatusCreated, compactManagementSystemTeamSummary(result))
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
		writeData(w, http.StatusOK, compactManagementSystemTeamDetail(result.Team))
	})
}

func newManagementSystemTeamMembersAddHandler(service managementSystemTeamService, opts managementOperationLogOptions) http.Handler {
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
		payload, ok := decodeManagementSystemTeamMembersAddPayload(w, r)
		if !ok {
			return
		}
		result, found, err := service.AddMembers(r.Context(), managementsystemteams.AddMembersInput{
			TeamID:           teamID,
			SystemAccountID:  systemAccountID,
			SystemAccountIDs: payload.SystemAccountIDs,
			CreatedBy:        authContext.SystemAccountID,
		})
		if errors.Is(err, managementsystemteams.ErrSystemTeamMemberInvalid) {
			writeMessageError(w, http.StatusBadRequest, "团队成员参数不合法")
			return
		}
		if err != nil {
			if isManagementSystemTeamUserFacingError(err) {
				writeMessageError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "添加团队成员失败")
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "团队不存在或已停用")
			return
		}

		recordSystemTeamMembersAddOperationLog(r, authContext, result, opts)
		writeData(w, http.StatusOK, compactManagementSystemTeamDetail(result.Team))
	})
}

func newManagementSystemTeamMemberDeleteHandler(service managementSystemTeamService, opts managementOperationLogOptions) http.Handler {
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
		memberID := strings.TrimSpace(chi.URLParam(r, "memberId"))
		if teamID == "" || memberID == "" {
			writeMessageError(w, http.StatusBadRequest, "团队成员参数不合法")
			return
		}
		result, found, err := service.RemoveMember(r.Context(), managementsystemteams.RemoveMemberInput{
			TeamID:          teamID,
			MemberID:        memberID,
			SystemAccountID: systemAccountID,
			UpdatedBy:       authContext.SystemAccountID,
		})
		if errors.Is(err, managementsystemteams.ErrSystemTeamMemberInvalid) {
			writeMessageError(w, http.StatusBadRequest, "团队成员参数不合法")
			return
		}
		if err != nil {
			if isManagementSystemTeamUserFacingError(err) {
				writeMessageError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeMessageError(w, http.StatusInternalServerError, "移除团队成员失败")
			return
		}
		if !found {
			writeMessageError(w, http.StatusNotFound, "团队成员不存在")
			return
		}

		recordSystemTeamMemberRemoveOperationLog(r, authContext, result, opts)
		writeData(w, http.StatusOK, compactManagementSystemTeamDetail(result.Team))
	})
}

type managementSystemTeamDetailResponse struct {
	ID          string                               `json:"id"`
	Name        string                               `json:"name"`
	Description string                               `json:"description,omitempty"`
	Status      string                               `json:"status"`
	MemberCount int                                  `json:"memberCount"`
	Members     []managementSystemTeamMemberResponse `json:"members"`
	CreatedAt   string                               `json:"createdAt"`
}

type managementSystemTeamMemberResponse struct {
	ID                string `json:"id"`
	SystemAccountID   string `json:"systemAccountId"`
	SystemAccountName string `json:"systemAccountName,omitempty"`
	JoinedAt          string `json:"joinedAt"`
}

func compactManagementSystemTeamSummary(summary managementsystemteams.Summary) managementSystemTeamDetailResponse {
	return managementSystemTeamDetailResponse{
		ID: summary.ID, Name: summary.Name, Description: summary.Description, Status: summary.Status,
		MemberCount: summary.ActiveMemberCount, Members: []managementSystemTeamMemberResponse{}, CreatedAt: summary.CreatedAt,
	}
}

func compactManagementSystemTeamDetail(detail managementsystemteams.Detail) managementSystemTeamDetailResponse {
	result := compactManagementSystemTeamSummary(detail.Summary)
	result.MemberCount = 0
	result.Members = make([]managementSystemTeamMemberResponse, 0, len(detail.Members))
	for _, member := range detail.Members {
		if member.Status != "active" {
			continue
		}
		result.Members = append(result.Members, managementSystemTeamMemberResponse{
			ID: member.ID, SystemAccountID: member.SystemAccountID,
			SystemAccountName: member.SystemAccountName, JoinedAt: member.JoinedAt,
		})
	}
	result.MemberCount = len(result.Members)
	return result
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

type managementSystemTeamMembersAddPayload struct {
	SystemAccountIDs []string
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

func decodeManagementSystemTeamMembersAddPayload(w http.ResponseWriter, r *http.Request) (managementSystemTeamMembersAddPayload, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementSystemTeamMembersAddPayload{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeMessageError(w, http.StatusBadRequest, "请求体无效")
		return managementSystemTeamMembersAddPayload{}, false
	}
	var ids []string
	for field, raw := range payload {
		switch field {
		case "systemAccountIds":
			values, ok := raw.([]any)
			if !ok {
				writeMessageError(w, http.StatusBadRequest, "团队成员参数不合法")
				return managementSystemTeamMembersAddPayload{}, false
			}
			ids = make([]string, 0, len(values))
			for _, value := range values {
				text, ok := value.(string)
				if !ok || strings.TrimSpace(text) == "" {
					writeMessageError(w, http.StatusBadRequest, "团队成员参数不合法")
					return managementSystemTeamMembersAddPayload{}, false
				}
				ids = append(ids, text)
			}
		default:
			writeMessageError(w, http.StatusBadRequest, "团队成员参数不合法")
			return managementSystemTeamMembersAddPayload{}, false
		}
	}
	if len(ids) == 0 {
		writeMessageError(w, http.StatusBadRequest, "团队成员参数不合法")
		return managementSystemTeamMembersAddPayload{}, false
	}
	return managementSystemTeamMembersAddPayload{SystemAccountIDs: ids}, true
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
	if opts.submitter == nil {
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
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "admin",
		Module:               "system_teams",
		Action:               "create",
		OperationKey:         "system_teams.create",
		ResourceType:         "system_team",
		ResourceID:           result.ID,
		ResourceName:         result.Name,
		Summary:              "创建系统团队：" + result.Name,
		DetailLevel:          "full",
		VisibilityScope:      "targeted",
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
			VisibilityReason: "actor_self",
			DetailLevel:      "full",
		}},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func recordSystemTeamUpdateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemteams.UpdateResult,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil {
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
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "admin",
		Module:               "system_teams",
		Action:               "update",
		OperationKey:         "system_teams.update",
		ResourceType:         "system_team",
		ResourceID:           result.Team.ID,
		ResourceName:         result.Team.Name,
		Summary:              "更新系统团队：" + result.Team.Name,
		DetailLevel:          "full",
		VisibilityScope:      "targeted",
		Changes:              systemTeamUpdateOperationChanges(result.Before, result.Team.Summary),
		Metadata: map[string]any{
			"authorizationChanged": result.AuthorizationChanged,
		},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Targets:    systemTeamMemberOperationTargets(result.Team.Members, nil),
		Viewers:    systemTeamUpdateOperationViewers(authContext.SystemAccountID, result.Team.Members),
		CreatedAt:  now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func recordSystemTeamMembersAddOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemteams.AddMembersResult,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil {
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
	addedMembers := systemTeamAddedMembers(result.Before.Members, result.Team.Members)
	statusCode := http.StatusOK
	input := port.OperationLogInput{
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "admin",
		Module:               "system_teams",
		Action:               "add_members",
		OperationKey:         "system_teams.add_members",
		ResourceType:         "system_team",
		ResourceID:           result.Team.ID,
		ResourceName:         result.Team.Name,
		Summary:              "添加团队成员：" + result.Team.Name,
		DetailLevel:          "full",
		VisibilityScope:      "targeted",
		Changes: []port.OperationLogChange{{
			Field:  "members",
			Label:  "新增成员",
			Before: nil,
			After:  systemTeamMemberNames(addedMembers),
		}},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Targets:    systemTeamMemberOperationTargets(result.Team.Members, addedMembers),
		Viewers:    systemTeamMemberOperationViewers(authContext.SystemAccountID, result.Team.Members, addedMembers),
		CreatedAt:  now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}

func recordSystemTeamMemberRemoveOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemteams.RemoveMemberResult,
	opts managementOperationLogOptions,
) {
	if opts.submitter == nil {
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
		ID:                   newLogID(),
		TraceID:              requestIDFromContext(r.Context()),
		ActorSystemAccountID: authContext.SystemAccountID,
		ActorUsername:        authContext.Username,
		ActorDisplayName:     authContext.DisplayName,
		ActorRole:            authContext.Role,
		Mode:                 "admin",
		Module:               "system_teams",
		Action:               "remove_member",
		OperationKey:         "system_teams.remove_member",
		ResourceType:         "system_team",
		ResourceID:           result.Team.ID,
		ResourceName:         result.Team.Name,
		Summary:              "移除团队成员：" + result.Team.Name,
		DetailLevel:          "full",
		VisibilityScope:      "targeted",
		Changes: []port.OperationLogChange{{
			Field:  "member",
			Label:  "移除成员",
			Before: systemTeamMemberDisplayName(result.RemovedMember),
			After:  nil,
		}},
		Method:     r.Method,
		Path:       r.URL.Path,
		StatusCode: &statusCode,
		ClientIP:   opts.clientIP.FromRequest(r),
		UserAgent:  r.UserAgent(),
		Targets:    systemTeamMemberOperationTargets(result.Team.Members, []managementsystemteams.MemberSummary{result.RemovedMember}),
		Viewers:    systemTeamMemberOperationViewers(authContext.SystemAccountID, result.Team.Members, []managementsystemteams.MemberSummary{result.RemovedMember}),
		CreatedAt:  now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
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
		detailLevel := "full"
		key := systemAccountID + "\x00" + reason + "\x00" + detailLevel
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		viewers = append(viewers, port.OperationLogViewerInput{
			SystemAccountID:  systemAccountID,
			VisibilityReason: reason,
			DetailLevel:      detailLevel,
		})
	}
	addViewer(actorSystemAccountID, "actor_self")
	for _, member := range members {
		addViewer(member.SystemAccountID, "team_member")
	}
	return viewers
}

func systemTeamAddedMembers(before []managementsystemteams.MemberSummary, after []managementsystemteams.MemberSummary) []managementsystemteams.MemberSummary {
	beforeIDs := make(map[string]struct{}, len(before))
	for _, member := range before {
		id := strings.TrimSpace(member.SystemAccountID)
		if id != "" {
			beforeIDs[id] = struct{}{}
		}
	}
	added := make([]managementsystemteams.MemberSummary, 0)
	for _, member := range after {
		id := strings.TrimSpace(member.SystemAccountID)
		if id == "" {
			continue
		}
		if _, ok := beforeIDs[id]; ok {
			continue
		}
		added = append(added, member)
	}
	return added
}

func systemTeamMemberNames(members []managementsystemteams.MemberSummary) string {
	names := make([]string, 0, len(members))
	for _, member := range members {
		if name := systemTeamMemberDisplayName(member); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "、")
}

func systemTeamMemberDisplayName(member managementsystemteams.MemberSummary) string {
	if value := strings.TrimSpace(member.SystemAccountName); value != "" {
		return value
	}
	if value := strings.TrimSpace(member.Username); value != "" {
		return value
	}
	return strings.TrimSpace(member.SystemAccountID)
}

func systemTeamMemberOperationTargets(primary []managementsystemteams.MemberSummary, extra []managementsystemteams.MemberSummary) []port.OperationLogTargetInput {
	targets := make([]port.OperationLogTargetInput, 0, len(primary)+len(extra))
	addTarget := func(member managementsystemteams.MemberSummary, relation string) {
		systemAccountID := strings.TrimSpace(member.SystemAccountID)
		if systemAccountID == "" {
			return
		}
		targets = append(targets, port.OperationLogTargetInput{
			TargetType:                 "system_account",
			TargetID:                   systemAccountID,
			TargetName:                 systemTeamMemberDisplayName(member),
			TargetOwnerSystemAccountID: systemAccountID,
			Relation:                   relation,
		})
	}
	for _, member := range primary {
		addTarget(member, "team_member")
	}
	for _, member := range extra {
		addTarget(member, "team_member")
	}
	return targets
}

func systemTeamMemberOperationViewers(
	actorSystemAccountID string,
	primary []managementsystemteams.MemberSummary,
	extra []managementsystemteams.MemberSummary,
) []port.OperationLogViewerInput {
	viewers := make([]port.OperationLogViewerInput, 0, len(primary)+len(extra)+1)
	seen := map[string]struct{}{}
	addViewer := func(systemAccountID string, reason string) {
		systemAccountID = strings.TrimSpace(systemAccountID)
		if systemAccountID == "" {
			return
		}
		detailLevel := "full"
		key := systemAccountID + "\x00" + reason + "\x00" + detailLevel
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		viewers = append(viewers, port.OperationLogViewerInput{
			SystemAccountID:  systemAccountID,
			VisibilityReason: reason,
			DetailLevel:      detailLevel,
		})
	}
	addViewer(actorSystemAccountID, "actor_self")
	for _, member := range primary {
		addViewer(member.SystemAccountID, "team_member")
	}
	for _, member := range extra {
		addViewer(member.SystemAccountID, "team_member")
	}
	return viewers
}

func isManagementSystemTeamUserFacingError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "授权团队") ||
		strings.Contains(message, "团队成员") ||
		strings.Contains(message, "单个授权团队") ||
		strings.Contains(message, "不能授权给资源所有者自己")
}
