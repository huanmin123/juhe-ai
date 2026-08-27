package modelcheckhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckauth"
)

// NewAdminAuthorizeFunc adapts the durable Node-compatible management session
// into J3b's Go-owned transport scope. It never delegates authentication to
// Node or a sidecar. The operation scope initially equals the actor; a later
// scoped-admin resolver may replace only SystemAccountID after authorization.
func NewAdminAuthorizeFunc(auth *modelcheckauth.Authenticator) AuthorizeFunc {
	return func(ctx context.Context, request *http.Request) (Scope, error) {
		if auth == nil {
			return Scope{}, &HTTPError{Status: http.StatusServiceUnavailable, Message: "模型检测管理认证未初始化"}
		}
		authorizationValues, hasAuthorization := request.Header["Authorization"]
		authorization := ""
		if hasAuthorization {
			if len(authorizationValues) != 1 {
				return Scope{}, &HTTPError{Status: http.StatusUnauthorized, Message: modelcheckauth.ErrInvalidToken.Error()}
			}
			authorization = authorizationValues[0]
		}
		actor, err := auth.RequireAdmin(ctx, authorization, request.Header.Get("Cookie"))
		if err != nil {
			return Scope{}, managementAuthError(err)
		}
		filter, err := requestedSystemAccountFilter(request)
		if err != nil {
			return Scope{}, err
		}
		scope := Scope{SystemAccountID: actor.SystemAccountID, ActorSystemAccountID: actor.SystemAccountID, ActorRole: actor.Role}
		if filter != "" {
			scope.SystemAccountID, scope.SystemAccountFilterID = filter, filter
		}
		return scope, nil
	}
}

// ManagementTargetScopeReader performs the bounded, direct lookup needed for
// an administrator request without a systemAccountId filter. It intentionally
// exposes only the account's logical owning scope, never credentials.
type ManagementTargetScopeReader interface {
	ResolveManagementSystemAccount(context.Context, string) (string, error)
}

func NewAdminTargetScopeResolver(reader ManagementTargetScopeReader) ResolveScopeFunc {
	return func(ctx context.Context, scope Scope, command Command) (Scope, error) {
		if reader == nil {
			return Scope{}, &HTTPError{Status: http.StatusServiceUnavailable, Message: "模型检测管理目标作用域未初始化"}
		}
		if scope.SystemAccountFilterID != "" {
			return scope, nil
		}
		systemAccountID, err := reader.ResolveManagementSystemAccount(ctx, command.TargetID)
		if err != nil {
			return Scope{}, &HTTPError{Status: http.StatusBadRequest, Message: "模型检测目标账户不存在或不可用"}
		}
		scope.SystemAccountID = systemAccountID
		return scope, nil
	}
}

func managementAuthError(err error) error {
	switch {
	case errors.Is(err, modelcheckauth.ErrInvalidToken), errors.Is(err, modelcheckauth.ErrLoginRequired), errors.Is(err, modelcheckauth.ErrSessionExpired):
		return &HTTPError{Status: http.StatusUnauthorized, Message: err.Error()}
	case errors.Is(err, modelcheckauth.ErrMustChange):
		return &HTTPError{Status: http.StatusForbidden, Message: err.Error(), Code: "must_change_password"}
	case errors.Is(err, modelcheckauth.ErrForbidden):
		return &HTTPError{Status: http.StatusForbidden, Message: err.Error()}
	default:
		return &HTTPError{Status: http.StatusServiceUnavailable, Message: "模型检测管理认证暂不可用"}
	}
}

func requestedSystemAccountFilter(request *http.Request) (string, error) {
	values := request.URL.Query()["systemAccountId"]
	if len(values) > 1 {
		return "", &HTTPError{Status: http.StatusBadRequest, Message: "查询参数不合法"}
	}
	if len(values) == 0 {
		return "", nil
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		return "", &HTTPError{Status: http.StatusBadRequest, Message: "系统账号 ID 不能为空"}
	}
	if value == "all" {
		return "", nil
	}
	return value, nil
}
