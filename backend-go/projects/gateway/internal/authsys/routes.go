package authsys

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// MountAuth registers the auth route family on the kernel
// (prefix /__aisys__/api/auth). Contract mirrors auth.routes.ts.
func (d *Deps) MountAuth(k *kernel.Kernel, cookieSameSite string, cookieSecure bool) {
	prefix := "/__aisys__/api/auth"
	k.RegisterFunc("GET "+prefix+"/captcha", d.getCaptcha)
	k.RegisterFunc("POST "+prefix+"/login", d.postLogin(cookieSameSite, cookieSecure))
	k.RegisterFunc("POST "+prefix+"/logout", d.postLogout(cookieSameSite, cookieSecure))
	k.Register("GET "+prefix+"/me", d.RequireSession(false)(http.HandlerFunc(d.getMe)))
	k.Register("GET "+prefix+"/profile", d.RequireSession(false)(http.HandlerFunc(d.getProfile)))
	k.Register("PATCH "+prefix+"/me", d.RequireSession(true)(http.HandlerFunc(d.patchMe)))
	k.Register("POST "+prefix+"/change-password", d.RequireSession(true)(http.HandlerFunc(d.postChangePassword)))
	k.RegisterFunc("POST "+prefix+"/temporary-access-tokens", d.postTemporaryAccessToken)
	k.Register("POST "+prefix+"/temporary-access-tokens/revoke", d.RequireSession(true)(http.HandlerFunc(d.postTemporaryAccessTokenRevoke)))
}

func (d *Deps) getCaptcha(w http.ResponseWriter, r *http.Request) {
	if d.CaptchaDisabled {
		kernel.WriteOK(w, map[string]any{"required": false}, "")
		return
	}
	result, err := d.Captcha.Issue(kernel.Context(r).ClientIP)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "验证码生成失败")
		return
	}
	if result.Blocked {
		if result.RetryAfter > 0 {
			w.Header().Set("Retry-After", itoaText(result.RetryAfter))
		}
		message := result.Message
		if message == "" {
			message = "验证码请求过于频繁，请稍后再试"
		}
		kernel.WriteError(w, http.StatusTooManyRequests, message)
		return
	}
	kernel.WriteOK(w, map[string]any{
		"required":  true,
		"captchaId": result.Challenge.CaptchaID,
		"image":     result.Challenge.Image,
		"expiresAt": result.Challenge.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, "")
}

func (d *Deps) postLogin(cookieSameSite string, cookieSecure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Username    string  `json:"username"`
			Password    string  `json:"password"`
			CaptchaID   *string `json:"captchaId"`
			CaptchaCode *string `json:"captchaCode"`
		}
		if !kernel.DecodeJSON(w, r, &body) {
			return
		}
		if body.Username == "" || body.Password == "" {
			kernel.WriteBadRequest(w, "登录参数无效")
			return
		}
		if hasWhitespace(body.Username) || hasWhitespace(body.Password) {
			kernel.WriteBadRequest(w, "用户名和密码不能包含空格")
			return
		}
		if !d.CaptchaDisabled {
			if body.CaptchaID == nil || body.CaptchaCode == nil || *body.CaptchaID == "" || *body.CaptchaCode == "" {
				kernel.WriteBadRequest(w, "登录参数无效")
				return
			}
			if !d.Captcha.Verify(*body.CaptchaID, *body.CaptchaCode) {
				kernel.WriteError(w, http.StatusBadRequest, "验证码错误或已过期")
				return
			}
		}
		clientIP := loginClientIP(r)
		if blocked, retryAfter, message := d.LoginGuard.Check(clientIP, body.Username); blocked {
			setRetryAfter(w, retryAfter)
			kernel.WriteError(w, http.StatusTooManyRequests, message)
			return
		}
		verified, ok, err := d.Port.VerifyCredentials(r.Context(), body.Username, body.Password)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !ok {
			if blocked, retryAfter, message := d.LoginGuard.Failed(clientIP, body.Username); blocked {
				setRetryAfter(w, retryAfter)
				kernel.WriteError(w, http.StatusTooManyRequests, message)
				return
			}
			kernel.WriteError(w, http.StatusUnauthorized, "账号或密码错误")
			return
		}
		issued, issuedOK, err := d.Port.CreateSession(r.Context(), verified.SystemAccountID, verified.CredentialRevision, 14)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !issuedOK {
			if blocked, retryAfter, message := d.LoginGuard.Failed(clientIP, body.Username); blocked {
				setRetryAfter(w, retryAfter)
				kernel.WriteError(w, http.StatusTooManyRequests, message)
				return
			}
			kernel.WriteError(w, http.StatusUnauthorized, "账号或密码已变更，请重新登录")
			return
		}
		d.LoginGuard.Success(clientIP, verified.Username)
		SetSessionCookie(w, issued.Token, cookieSameSite, cookieSecure, sessionMaxAge)
		kernel.WriteOK(w, CurrentUserSummary{
			ID: verified.SystemAccountID, Username: verified.Username, DisplayName: verified.DisplayName,
			Role: verified.Role, MustChangePassword: verified.MustChangePassword,
		}, "")
	}
}

func (d *Deps) postLogout(cookieSameSite string, cookieSecure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookies := ParseCookie(r.Header.Get("Cookie"))
		if token := cookies[SessionCookieName]; token != "" {
			if err := d.Port.RevokeToken(r.Context(), token); err != nil {
				kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
				return
			}
		}
		ClearSessionCookie(w, cookieSameSite, cookieSecure)
		kernel.WriteOK(w, map[string]any{"loggedOut": true}, "")
	}
}

func (d *Deps) getMe(w http.ResponseWriter, r *http.Request) {
	auth := AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	kernel.WriteOK(w, currentUserSummary(auth), "")
}

func (d *Deps) getProfile(w http.ResponseWriter, r *http.Request) {
	auth := AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	summary, err := d.Accounts.FindByID(r.Context(), auth.SystemAccountID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if summary.ID == "" {
		kernel.WriteError(w, http.StatusNotFound, "系统账户不存在")
		return
	}
	kernel.WriteOK(w, summary, "")
}

func (d *Deps) patchMe(w http.ResponseWriter, r *http.Request) {
	auth := AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if auth.MustChangePassword {
		writeMustChange(w)
		return
	}
	var body struct {
		DisplayName *string `json:"displayName"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if body.DisplayName == nil || *body.DisplayName == "" {
		kernel.WriteBadRequest(w, "用户资料参数无效")
		return
	}
	if hasWhitespace(*body.DisplayName) {
		kernel.WriteBadRequest(w, "显示名称不能包含空格")
		return
	}
	displayName := strings.TrimSpace(*body.DisplayName)
	before, err := d.Accounts.FindByID(r.Context(), auth.SystemAccountID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if before.ID == "" {
		kernel.WriteError(w, http.StatusNotFound, "系统账户不存在")
		return
	}
	if before.DisplayName == displayName {
		kernel.WriteOK(w, currentUserSummary(auth), "")
		return
	}
	_, err = d.Accounts.Patch(r.Context(), auth.SystemAccountID, PatchInput{
		ExpectedUpdatedAt: before.UpdatedAt,
		DisplayName:       &displayName,
	})
	if err != nil {
		var conflict *ConflictError
		if errors.As(err, &conflict) {
			kernel.WriteError(w, http.StatusConflict, conflict.Message)
			return
		}
		kernel.WriteError(w, http.StatusInternalServerError, "修改显示名称失败")
		return
	}
	updated, err := d.Accounts.FindByID(r.Context(), auth.SystemAccountID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	d.recordOperationLog(r, OperationLogEntry{
		OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "self",
		Module: "system_accounts", Action: "update", OperationKey: "auth.update_profile",
		ResourceType: "system_account", ResourceID: auth.SystemAccountID, ResourceName: updated.DisplayName,
		Summary: "修改显示名称：" + updated.DisplayName,
		Changes: []OperationLogChange{{Field: "displayName", Label: "显示名称", Before: before.DisplayName, After: updated.DisplayName}},
	})
	kernel.WriteOK(w, CurrentUserSummary{
		ID:                 updated.ID,
		Username:           updated.Username,
		DisplayName:        updated.DisplayName,
		Role:               updated.Role,
		MustChangePassword: updated.MustChangePassword,
	}, "")
}

func (d *Deps) postChangePassword(w http.ResponseWriter, r *http.Request) {
	auth := AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	var body struct {
		OldPassword *string `json:"oldPassword"`
		NewPassword *string `json:"newPassword"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	if body.NewPassword == nil || len(*body.NewPassword) < 4 {
		kernel.WriteBadRequest(w, "密码参数无效")
		return
	}
	if hasWhitespace(*body.NewPassword) || (body.OldPassword != nil && hasWhitespace(*body.OldPassword)) {
		kernel.WriteBadRequest(w, "登录密码不能包含空格")
		return
	}
	if !auth.MustChangePassword {
		if body.OldPassword == nil || *body.OldPassword == "" {
			kernel.WriteBadRequest(w, "请填写当前密码")
			return
		}
		verified, ok, err := d.Port.VerifyCredentials(r.Context(), auth.Username, *body.OldPassword)
		if err != nil {
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		if !ok || verified.SystemAccountID != auth.SystemAccountID {
			kernel.WriteBadRequest(w, "当前密码不正确")
			return
		}
	}
	updated, err := d.Accounts.UpdatePassword(r.Context(), auth.SystemAccountID, *body.NewPassword)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "修改密码失败")
		return
	}
	_ = updated
	if err := d.Port.RevokeOtherSessions(r.Context(), auth.SystemAccountID, auth.SessionID); err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "修改密码失败")
		return
	}
	kernel.WriteOK(w, CurrentUserSummary{
		ID: updated.ID, Username: auth.Username, DisplayName: auth.DisplayName,
		Role: auth.Role, MustChangePassword: false,
	}, "")
}

func (d *Deps) postTemporaryAccessToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		TTLSeconds *int   `json:"ttlSeconds"`
	}
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	ttlSeconds := 900
	if body.TTLSeconds != nil {
		ttlSeconds = *body.TTLSeconds
	}
	if body.Username == "" || body.Password == "" || ttlSeconds < 60 || ttlSeconds > 3600 {
		kernel.WriteBadRequest(w, "临时访问令牌参数无效")
		return
	}
	if hasWhitespace(body.Username) || hasWhitespace(body.Password) {
		kernel.WriteBadRequest(w, "临时访问令牌参数无效")
		return
	}
	clientIP := loginClientIP(r)
	if !modelcheckauth.TemporaryAccessIPAllowed(clientIP, d.TemporaryAccessIPAllowlist) {
		kernel.WriteError(w, http.StatusForbidden, "当前来源不在临时访问令牌白名单中")
		return
	}
	if blocked, retryAfter, message := d.LoginGuard.Check(clientIP, body.Username); blocked {
		setRetryAfter(w, retryAfter)
		kernel.WriteError(w, http.StatusTooManyRequests, message)
		return
	}
	verified, ok, err := d.Port.VerifyCredentials(r.Context(), body.Username, body.Password)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if !ok || !IsAdminRole(verified.Role) {
		d.LoginGuard.Failed(clientIP, body.Username)
		kernel.WriteError(w, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	if verified.MustChangePassword {
		writeMustChange(w)
		return
	}
	temporary, temporaryOK, err := d.Port.CreateTemporaryToken(r.Context(), verified.SystemAccountID, verified.CredentialRevision, ttlSeconds)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if !temporaryOK {
		kernel.WriteError(w, http.StatusUnauthorized, "账号或密码已变更，请重新申请")
		return
	}
	d.LoginGuard.Success(clientIP, verified.Username)
	d.recordOperationLog(r, OperationLogEntry{
		ActorSystemAccountID: verified.SystemAccountID, ActorUsername: verified.Username,
		ActorDisplayName: verified.DisplayName, ActorRole: verified.Role,
		OperationScopeSystemAccountID: verified.SystemAccountID, Mode: "self",
		Module: "auth", Action: "create", OperationKey: "auth.temporary_access_token.create",
		ResourceType: "system_session", ResourceID: temporary.SessionID, ResourceName: "temporary-access-token",
		Summary: "申请临时访问令牌，" + itoaText(ttlSeconds) + " 秒后过期",
	})
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, map[string]any{
		"token":     temporary.Token,
		"tokenType": "Bearer",
		"expiresAt": temporary.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}, "")
}

func (d *Deps) postTemporaryAccessTokenRevoke(w http.ResponseWriter, r *http.Request) {
	cookies := ParseCookie(r.Header.Get("Cookie"))
	kind, token, temporary := ResolveSystemAccessToken(r.Header.Get("Authorization"), cookies[SessionCookieName])
	if kind != "token" || !temporary {
		kernel.WriteBadRequest(w, "只能撤销当前临时访问令牌")
		return
	}
	auth := AuthContextFrom(r)
	if auth == nil {
		kernel.WriteError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	d.recordOperationLog(r, OperationLogEntry{
		OperationScopeSystemAccountID: auth.SystemAccountID, Mode: "self",
		Module: "auth", Action: "delete", OperationKey: "auth.temporary_access_token.revoke",
		ResourceType: "system_session", ResourceID: auth.SessionID, ResourceName: "temporary-access-token",
		Summary: "撤销当前临时访问令牌",
	})
	if err := d.Port.RevokeToken(r.Context(), token); err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "撤销临时访问令牌失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	kernel.WriteOK(w, map[string]any{"revoked": true}, "")
}

func writeMustChange(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"message":"请先修改初始密码","code":"must_change_password"}`))
}

func setRetryAfter(w http.ResponseWriter, seconds int) {
	if seconds > 0 {
		w.Header().Set("Retry-After", itoaText(seconds))
	}
}

func loginClientIP(r *http.Request) string {
	ip := kernel.Context(r).ClientIP
	if ip == "" {
		return "unknown"
	}
	return ip
}

func itoaText(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func boolPtr(value bool) *bool { return &value }

var _ = modelcheckauth.ErrSessionExpired
