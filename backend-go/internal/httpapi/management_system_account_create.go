package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/modules/managementauth"
	"juhe-ai/backend-go/internal/modules/managementsystemaccounts"
	"juhe-ai/backend-go/internal/store/port"
)

type managementSystemAccountCreateService interface {
	Create(ctx context.Context, input managementsystemaccounts.CreateInput) (managementsystemaccounts.CreateResult, error)
}

func NewManagementSystemAccountCreateHandlerWithOperationLog(service managementSystemAccountCreateService, opts ManagementOperationLogOptions) http.Handler {
	optsInternal := newManagementOperationLogOptions(opts)
	return newManagementSystemAccountCreateHandler(service, optsInternal)
}

func newManagementSystemAccountCreateHandler(service managementSystemAccountCreateService, opts managementOperationLogOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authContext, ok := ManagementAuthContextFromRequest(r)
		if !ok {
			writeMessageError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if !managementauth.IsSuperAdminRole(authContext.Role) {
			writeMessageError(w, http.StatusForbidden, "需要超级管理员权限")
			return
		}
		var payload map[string]any
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&payload); err != nil || payload == nil {
			writeMessageError(w, http.StatusBadRequest, "请求体无效")
			return
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			writeMessageError(w, http.StatusBadRequest, "请求体无效")
			return
		}

		username, _ := payload["username"].(string)
		displayName, _ := payload["displayName"].(string)
		password, _ := payload["password"].(string)

		var description *string
		if raw, exists := payload["description"]; exists {
			if text, ok := raw.(string); ok {
				description = &text
			} else if raw != nil {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return
			}
		}

		var role string
		if raw, exists := payload["role"]; exists {
			if text, ok := raw.(string); ok {
				if text != "admin" && text != "user" {
					writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
					return
				}
				role = text
			} else {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return
			}
		}

		var status string
		if raw, exists := payload["status"]; exists {
			if text, ok := raw.(string); ok {
				if text != "active" && text != "disabled" {
					writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
					return
				}
				status = text
			} else {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return
			}
		}

		var mustChangePassword *bool
		if raw, exists := payload["mustChangePassword"]; exists {
			if value, ok := raw.(bool); ok {
				mustChangePassword = &value
			} else {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return
			}
		}

		var imageGenerationEnabled *bool
		if raw, exists := payload["imageGenerationEnabled"]; exists {
			if value, ok := raw.(bool); ok {
				imageGenerationEnabled = &value
			} else {
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return
			}
		}

		for field := range payload {
			switch field {
			case "username", "displayName", "description", "password", "role", "status", "mustChangePassword", "imageGenerationEnabled":
				continue
			default:
				writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
				return
			}
		}

		if username == "" || displayName == "" || password == "" {
			writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
			return
		}
		if strings.ContainsFunc(username, unicode.IsSpace) {
			writeMessageError(w, http.StatusBadRequest, "用户名不能包含空格")
			return
		}
		if strings.ContainsFunc(displayName, unicode.IsSpace) {
			writeMessageError(w, http.StatusBadRequest, "用户名称不能包含空格")
			return
		}
		if strings.ContainsFunc(password, unicode.IsSpace) {
			writeMessageError(w, http.StatusBadRequest, "登录密码不能包含空格")
			return
		}

		result, err := service.Create(r.Context(), managementsystemaccounts.CreateInput{
			Username:               username,
			DisplayName:            displayName,
			Description:            description,
			Password:               password,
			Role:                   role,
			Status:                 status,
			MustChangePassword:     mustChangePassword,
			ImageGenerationEnabled: imageGenerationEnabled,
		})
		if errors.Is(err, managementsystemaccounts.ErrSystemAccountCreateInvalid) {
			writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrSystemAccountWhitespace) {
			writeMessageError(w, http.StatusBadRequest, "系统账户参数无效")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrSystemAccountUsernameExists) {
			writeMessageError(w, http.StatusConflict, "用户账户已存在")
			return
		}
		if errors.Is(err, managementsystemaccounts.ErrSystemAccountDisplayNameExists) {
			writeMessageError(w, http.StatusConflict, "用户名称已存在")
			return
		}
		if err != nil {
			writeMessageError(w, http.StatusInternalServerError, "创建系统账户失败")
			return
		}

		recordSystemAccountCreateOperationLog(r, authContext, result, opts)
		writeData(w, http.StatusCreated, result.Account)
	})
}

func recordSystemAccountCreateOperationLog(
	r *http.Request,
	authContext managementauth.Context,
	result managementsystemaccounts.CreateResult,
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
	changes := []port.OperationLogChange{
		{Field: "username", Label: "用户账户", Before: nil, After: result.Account.Username},
		{Field: "displayName", Label: "用户名称", Before: nil, After: result.Account.DisplayName},
		{Field: "role", Label: "角色", Before: nil, After: result.Account.Role},
		{Field: "status", Label: "状态", Before: nil, After: result.Account.Status},
		{Field: "imageGenerationEnabled", Label: "支持图像生成", Before: nil, After: result.Account.ImageGenerationEnabled},
		{Field: "password", Label: "登录密码", Before: nil, After: "已设置", Sensitive: true},
	}
	input := port.OperationLogInput{
		ID:                            newLogID(),
		TraceID:                       requestIDFromContext(r.Context()),
		ActorSystemAccountID:          authContext.SystemAccountID,
		ActorUsername:                 authContext.Username,
		ActorDisplayName:              authContext.DisplayName,
		ActorRole:                     authContext.Role,
		OperationScopeSystemAccountID: result.Account.ID,
		Mode:                          "admin",
		Module:                        "system_accounts",
		Action:                        "create",
		OperationKey:                  "system_accounts.create",
		ResourceType:                  "system_account",
		ResourceID:                    result.Account.ID,
		ResourceName:                  result.Account.DisplayName,
		Summary:                       "创建系统账户：" + result.Account.DisplayName,
		DetailLevel:                   "full",
		VisibilityScope:               "targeted",
		Changes:                       changes,
		Method:                        r.Method,
		Path:                          r.URL.Path,
		StatusCode:                    &statusCode,
		ClientIP:                      opts.clientIP.FromRequest(r),
		UserAgent:                     r.UserAgent(),
		Viewers: []port.OperationLogViewerInput{
			{
				SystemAccountID:  result.Account.ID,
				VisibilityReason: "admin_managed_my_resource",
				DetailLevel:      "full",
			},
		},
		CreatedAt: now().UTC(),
	}
	enqueueManagementOperationLog(r.Context(), opts, input)
}
