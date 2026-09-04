package modelcheckauth

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTPHandler is the Gateway-owned authentication lifecycle for the
// management listener. It only uses the in-process Authenticator and never
// forwards a request to Node or another Go project.
type HTTPHandler struct {
	Auth                       *Authenticator
	Captcha                    *CaptchaService
	MaxBody                    int64
	TTL                        int
	TemporaryAccessIPAllowlist []string
	Guard                      *LoginGuard
	guardOnce                  sync.Once
}

type loginRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	CaptchaID   string `json:"captchaId"`
	CaptchaCode string `json:"captchaCode"`
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Gateway auth owner 未接线")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == http.MethodGet && path == "/captcha" {
		h.captcha(w, r)
		return
	}
	if h.Auth == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "Gateway auth owner 未接线")
		return
	}
	switch {
	case r.Method == http.MethodPost && path == "/login":
		h.login(w, r)
	case r.Method == http.MethodPost && path == "/logout":
		h.logout(w, r)
	case r.Method == http.MethodGet && path == "/me":
		h.me(w, r)
	case r.Method == http.MethodPatch && path == "/me":
		h.profile(w, r)
	case r.Method == http.MethodPost && path == "/change-password":
		h.changePassword(w, r)
	case r.Method == http.MethodPost && path == "/temporary-access-tokens":
		h.temporaryAccessToken(w, r)
	case r.Method == http.MethodPost && path == "/temporary-access-tokens/revoke":
		h.revokeTemporaryAccessToken(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *HTTPHandler) captcha(w http.ResponseWriter, r *http.Request) {
	if h.Captcha == nil {
		writeJSON(w, http.StatusOK, map[string]any{"required": false})
		return
	}
	result, err := h.Captcha.Issue(clientIP(r))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "验证码生成失败")
		return
	}
	if result.Blocked {
		if result.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(result.RetryAfter))
		}
		writeJSONError(w, http.StatusTooManyRequests, result.Message)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"required":  true,
		"captchaId": result.Challenge.CaptchaID,
		"image":     result.Challenge.Image,
		"expiresAt": result.Challenge.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (h *HTTPHandler) temporaryAccessToken(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		TTLSeconds int    `json:"ttlSeconds"`
	}
	if err := decodeJSON(r, h.maxBody(), &input); err != nil {
		writeJSONError(w, http.StatusBadRequest, "临时访问令牌参数无效")
		return
	}
	if strings.TrimSpace(input.Username) == "" || input.Password == "" || strings.ContainsAny(input.Username, " \t\r\n") || strings.ContainsAny(input.Password, " \t\r\n") {
		writeJSONError(w, http.StatusBadRequest, "临时访问令牌参数无效")
		return
	}
	if input.TTLSeconds == 0 {
		input.TTLSeconds = 900
	}
	if input.TTLSeconds < 60 || input.TTLSeconds > 3600 {
		writeJSONError(w, http.StatusBadRequest, "临时访问令牌参数无效")
		return
	}
	if !h.temporaryAccessAllowed(clientIP(r)) {
		writeJSONError(w, http.StatusForbidden, "当前来源不在临时访问令牌白名单中")
		return
	}
	ip := clientIP(r)
	if blocked, retry, message := h.loginGuard().Check(ip, input.Username); blocked {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		writeJSONError(w, http.StatusTooManyRequests, message)
		return
	}
	verified, ok, err := h.Auth.VerifySystemAccountCredentials(r.Context(), input.Username, input.Password)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || (verified.Role != "admin" && verified.Role != "super_admin") {
		if blocked, retry, message := h.loginGuard().Failed(ip, input.Username); blocked {
			if retry > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
			}
			writeJSONError(w, http.StatusTooManyRequests, message)
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	if verified.MustChangePassword {
		writeJSONError(w, http.StatusForbidden, "请先修改初始密码")
		return
	}
	issued, issuedOK, err := h.Auth.CreateTemporaryAccessToken(r.Context(), verified.SystemAccountID, verified.CredentialRevision, input.TTLSeconds)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !issuedOK {
		if blocked, retry, message := h.loginGuard().Failed(ip, input.Username); blocked {
			if retry > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
			}
			writeJSONError(w, http.StatusTooManyRequests, message)
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "账号或密码已变更，请重新申请")
		return
	}
	h.loginGuard().Success(ip, verified.Username)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"token": issued.Token, "tokenType": "Bearer", "expiresAt": issued.ExpiresAt})
}

func (h *HTTPHandler) revokeTemporaryAccessToken(w http.ResponseWriter, r *http.Request) {
	token, err := requestToken(r)
	if err != nil || !temporaryToken.MatchString(token) {
		writeJSONError(w, http.StatusBadRequest, "只能撤销当前临时访问令牌")
		return
	}
	if _, err := h.Auth.AuthenticateTokenForSession(r.Context(), token); err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err := h.Auth.RevokeToken(r.Context(), token); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

func (h *HTTPHandler) temporaryAccessAllowed(ip string) bool {
	return TemporaryAccessIPAllowed(ip, h.TemporaryAccessIPAllowlist)
}

// TemporaryAccessIPAllowed applies the Node temporary-access source policy.
// The allowlist is an exact IP list; an empty list intentionally denies every
// source. IPv4-mapped IPv6 notation is normalized at the comparison boundary
// because both Go listeners and reverse proxies may expose it that way.
func TemporaryAccessIPAllowed(clientIP string, allowlist []string) bool {
	clientIP = normalizeTemporaryAccessIP(clientIP)
	if clientIP == "" {
		return false
	}
	for _, allowed := range allowlist {
		if normalizeTemporaryAccessIP(allowed) == clientIP {
			return true
		}
	}
	return false
}

func normalizeTemporaryAccessIP(value string) string {
	normalized := strings.TrimSpace(value)
	if len(normalized) >= len("::ffff:") && strings.EqualFold(normalized[:len("::ffff:")], "::ffff:") {
		return normalized[len("::ffff:"):]
	}
	return normalized
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return strings.TrimPrefix(strings.TrimSpace(host), "::ffff:")
	}
	return strings.TrimPrefix(strings.TrimSpace(r.RemoteAddr), "::ffff:")
}

func (h *HTTPHandler) login(w http.ResponseWriter, r *http.Request) {
	var input loginRequest
	if err := decodeJSON(r, h.maxBody(), &input); err != nil || strings.TrimSpace(input.Username) == "" || input.Password == "" || strings.ContainsAny(input.Username, " \t\r\n") || strings.ContainsAny(input.Password, " \t\r\n") {
		writeJSONError(w, http.StatusBadRequest, "登录参数无效")
		return
	}
	if h.Captcha != nil {
		if strings.TrimSpace(input.CaptchaID) == "" || strings.TrimSpace(input.CaptchaCode) == "" || !h.Captcha.Verify(strings.TrimSpace(input.CaptchaID), input.CaptchaCode) {
			writeJSONError(w, http.StatusBadRequest, "验证码错误或已过期")
			return
		}
	}
	ip := clientIP(r)
	if blocked, retry, message := h.loginGuard().Check(ip, input.Username); blocked {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		writeJSONError(w, http.StatusTooManyRequests, message)
		return
	}
	ttl := h.TTL
	if ttl <= 0 {
		ttl = 14
	}
	session, account, ok, err := h.Auth.Login(r.Context(), input.Username, input.Password, ttl)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		if blocked, retry, message := h.loginGuard().Failed(ip, input.Username); blocked {
			if retry > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(retry))
			}
			writeJSONError(w, http.StatusTooManyRequests, message)
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	h.loginGuard().Success(ip, account.Username)
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: session.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(time.Until(session.ExpiresAt).Seconds())})
	writeJSON(w, http.StatusOK, map[string]any{"id": account.SystemAccountID, "username": account.Username, "displayName": account.DisplayName, "role": account.Role, "mustChangePassword": account.MustChangePassword})
}

func (h *HTTPHandler) loginGuard() *LoginGuard {
	h.guardOnce.Do(func() {
		if h.Guard == nil {
			h.Guard = NewLoginGuard(nil)
		}
	})
	return h.Guard
}

func (h *HTTPHandler) logout(w http.ResponseWriter, r *http.Request) {
	token, _ := cookieToken(r)
	if token != "" {
		if err := h.Auth.RevokeToken(r.Context(), token); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"loggedOut": true})
}

func (h *HTTPHandler) me(w http.ResponseWriter, r *http.Request) {
	token, err := requestToken(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}
	actor, err := h.Auth.AuthenticateTokenForSessionNoTouch(r.Context(), token)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": actor.SystemAccountID, "username": actor.Username, "displayName": actor.DisplayName, "role": actor.Role, "mustChangePassword": actor.MustChangePassword})
}

func (h *HTTPHandler) profile(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, false)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var input struct {
		DisplayName string `json:"displayName"`
	}
	if err := decodeJSON(r, h.maxBody(), &input); err != nil || strings.TrimSpace(input.DisplayName) == "" || strings.ContainsAny(input.DisplayName, " \t\r\n") {
		writeJSONError(w, http.StatusBadRequest, "用户资料参数无效")
		return
	}
	changed, err := h.Auth.UpdateDisplayName(r.Context(), actor.SystemAccountID, strings.TrimSpace(input.DisplayName))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !changed {
		writeJSONError(w, http.StatusNotFound, "系统账户不存在")
		return
	}
	actor.DisplayName = strings.TrimSpace(input.DisplayName)
	writeJSON(w, http.StatusOK, map[string]any{"id": actor.SystemAccountID, "username": actor.Username, "displayName": actor.DisplayName, "role": actor.Role, "mustChangePassword": actor.MustChangePassword})
}

func (h *HTTPHandler) changePassword(w http.ResponseWriter, r *http.Request) {
	actor, err := h.actor(r, true)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}
	var input struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := decodeJSON(r, h.maxBody(), &input); err != nil || len(input.NewPassword) < 4 || strings.ContainsAny(input.NewPassword, " \t\r\n") || strings.ContainsAny(input.OldPassword, " \t\r\n") {
		writeJSONError(w, http.StatusBadRequest, "密码参数无效")
		return
	}
	revision, err := h.Auth.CurrentCredentialRevision(r.Context(), actor.SystemAccountID)
	if err != nil || revision == "" {
		writeJSONError(w, http.StatusNotFound, "系统账户不存在")
		return
	}
	if !actor.MustChangePassword {
		verified, ok, verifyErr := h.Auth.VerifySystemAccountCredentials(r.Context(), actor.Username, input.OldPassword)
		if verifyErr != nil {
			writeJSONError(w, http.StatusInternalServerError, verifyErr.Error())
			return
		}
		if !ok || verified.CredentialRevision != revision {
			writeJSONError(w, http.StatusBadRequest, "当前密码不正确")
			return
		}
	}
	changed, err := h.Auth.ChangePassword(r.Context(), actor.SystemAccountID, revision, input.NewPassword, actor.SessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !changed {
		writeJSONError(w, http.StatusConflict, "密码已变更，请重新提交")
		return
	}
	actor.MustChangePassword = false
	writeJSON(w, http.StatusOK, map[string]any{"id": actor.SystemAccountID, "username": actor.Username, "displayName": actor.DisplayName, "role": actor.Role, "mustChangePassword": false})
}

func (h *HTTPHandler) actor(r *http.Request, touch bool) (Actor, error) {
	token, err := requestToken(r)
	if err != nil {
		return Actor{}, err
	}
	if touch {
		return h.Auth.AuthenticateTokenForSession(r.Context(), token)
	}
	return h.Auth.AuthenticateTokenForSessionNoTouch(r.Context(), token)
}

func requestToken(r *http.Request) (string, error) {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return resolveToken(r.Header.Get("Authorization"), "")
	}
	return cookieToken(r)
}

func cookieToken(r *http.Request) (string, error) {
	for _, cookie := range strings.Split(r.Header.Get("Cookie"), ";") {
		name, value, found := strings.Cut(strings.TrimSpace(cookie), "=")
		if found && name == SessionCookieName {
			decoded, err := url.PathUnescape(value)
			if err != nil || decoded == "" {
				return "", ErrLoginRequired
			}
			return decoded, nil
		}
	}
	return "", ErrLoginRequired
}

func decodeJSON(r *http.Request, maxBody int64, dst any) error {
	if maxBody <= 0 {
		maxBody = 64 << 10
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("请求体必须是单个 JSON 对象")
	}
	return nil
}

func (h *HTTPHandler) maxBody() int64 {
	if h.MaxBody > 0 {
		return h.MaxBody
	}
	return 64 << 10
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": message})
}

var _ http.Handler = (*HTTPHandler)(nil)
