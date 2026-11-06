package modelcheckauth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// SharedLoginGuardDriver 是共享登录守卫驱动契约（R7-A A-H1）：与主面
// authsys.LoginGuardDriver 同形——Check/Failed 携带错误，错误即
// fail-closed（Redis 状态存储故障必须回 500，不得读作"未锁定"，D8
// 裁决同款）。*LoginGuard（进程内内存臂）与主面 authsys.SharedLoginGuard
// （Redis 运行态）都满足本接口；生产装配注入后者，两次挂载共享同一
// 实例与同一预算。
type SharedLoginGuardDriver interface {
	Check(ip, username string) (blocked bool, retryAfter int, message string, err error)
	Failed(ip, username string) (blocked bool, retryAfter int, message string, err error)
	Success(ip, username string)
}

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
	// SharedGuard 是与主管理面共享的登录守卫驱动（R7-A A-H1）：nil 时
	// loginGuard() 惰性回落进程内内存臂（测试兼容、既有语义不变）。
	SharedGuard SharedLoginGuardDriver
	// CookieSecure 与主面 cfg.CookieSecure 同源（R7-A A-H3）：J3b 会话
	// cookie 的 Secure 属性随配置，不再硬编码——同名 cookie 两面互覆盖
	// 时属性一致。未注入（false）保持既有行为。
	CookieSecure bool
	// TrustProxyCount 与主面 kernel 同源（R7-A A-H8）：>0 时 clientIP 按
	// kernel.ExtractClientIP 从 X-Forwarded-For 解析受信跳位后的真实来源
	// IP（反代后不再把全员归并为代理 IP）；0/未注入回落 r.RemoteAddr。
	TrustProxyCount int
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
	result, err := h.Captcha.Issue(h.clientIP(r))
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
	if !h.temporaryAccessAllowed(h.clientIP(r)) {
		writeJSONError(w, http.StatusForbidden, "当前来源不在临时访问令牌白名单中")
		return
	}
	ip := h.clientIP(r)
	if !h.checkLoginAllowed(w, ip, input.Username) {
		return
	}
	verified, ok, err := h.Auth.VerifySystemAccountCredentials(r.Context(), input.Username, input.Password)
	if err != nil {
		writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if !ok || (verified.Role != "admin" && verified.Role != "super_admin") {
		if !h.recordLoginFailure(w, ip, input.Username) {
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
		writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if !issuedOK {
		if !h.recordLoginFailure(w, ip, input.Username) {
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
		writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", err)
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

// clientIP（方法形态）按主面同源契约解析请求来源（R7-A A-H8）：注入
// TrustProxyCount>0 时与主面 kernel 中间件同款 kernel.ExtractClientIP——
// XFF 链从右数第 trustProxyCount 个受信条目作答，反代后不再把全员归并
// 为代理 IP（临时令牌白名单与守卫 byIP 桶因此按真实客户端隔离）；
// 未注入或 0 回落 RemoteAddr 语义（直连场景与既有行为一致）。
func (h *HTTPHandler) clientIP(r *http.Request) string {
	if h != nil && h.TrustProxyCount > 0 {
		return kernel.ExtractClientIP(r, h.TrustProxyCount)
	}
	return clientIP(r)
}

// checkLoginAllowed 是 fail-closed 的守卫 Check 包装（R7-A A-H1）：驱动
// 错误按主面 authsys postLogin 同款 500「服务器内部错误」处理（原文仅
// 入日志）；blocked 时写出 429 + Retry-After。返回 false 表示响应已
// 写出，调用方直接返回。
func (h *HTTPHandler) checkLoginAllowed(w http.ResponseWriter, ip, username string) bool {
	blocked, retry, message, guardErr := h.loginGuard().Check(ip, username)
	if guardErr != nil {
		slog.Error("J3b 登录守卫状态存储故障",
			"event", "j3b_login_guard_error", "op", "check", "error", guardErr.Error())
		writeJSONError(w, http.StatusInternalServerError, "服务器内部错误")
		return false
	}
	if blocked {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		writeJSONError(w, http.StatusTooManyRequests, message)
		return false
	}
	return true
}

// recordLoginFailure 是 fail-closed 的守卫 Failed 包装（R7-A A-H1）：
// 语义同 checkLoginAllowed；未锁定时返回 true 交由调用方继续既有 401
// 契约。
func (h *HTTPHandler) recordLoginFailure(w http.ResponseWriter, ip, username string) bool {
	blocked, retry, message, guardErr := h.loginGuard().Failed(ip, username)
	if guardErr != nil {
		slog.Error("J3b 登录守卫状态存储故障",
			"event", "j3b_login_guard_error", "op", "failed", "error", guardErr.Error())
		writeJSONError(w, http.StatusInternalServerError, "服务器内部错误")
		return false
	}
	if blocked {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		writeJSONError(w, http.StatusTooManyRequests, message)
		return false
	}
	return true
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
	ip := h.clientIP(r)
	if !h.checkLoginAllowed(w, ip, input.Username) {
		return
	}
	ttl := h.TTL
	if ttl <= 0 {
		ttl = 14
	}
	session, account, ok, err := h.Auth.Login(r.Context(), input.Username, input.Password, ttl)
	if err != nil {
		writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", err)
		return
	}
	if !ok {
		if !h.recordLoginFailure(w, ip, input.Username) {
			return
		}
		writeJSONError(w, http.StatusUnauthorized, "账号或密码错误")
		return
	}
	h.loginGuard().Success(ip, account.Username)
	// Secure 随装配注入（R7-A A-H3）：与主面 cfg.CookieSecure 同源，
	// 同名 cookie 两面互覆盖时属性一致，不再硬编码无 Secure。
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: session.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.CookieSecure, MaxAge: int(time.Until(session.ExpiresAt).Seconds())})
	writeJSON(w, http.StatusOK, map[string]any{"id": account.SystemAccountID, "username": account.Username, "displayName": account.DisplayName, "role": account.Role, "mustChangePassword": account.MustChangePassword})
}

// loginGuard 返回登录守卫：注入的共享驱动优先（生产为 Redis 运行态，
// 与主面同预算、fail-closed 生效）；未注入时惰性回落进程内内存臂
// （测试兼容，单面语义与既有行为一致）。
func (h *HTTPHandler) loginGuard() SharedLoginGuardDriver {
	if h.SharedGuard != nil {
		return h.SharedGuard
	}
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
			writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: h.CookieSecure, MaxAge: -1})
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
		writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", err)
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
	var revision string
	if !actor.MustChangePassword {
		verified, ok, verifyErr := h.Auth.VerifySystemAccountCredentials(r.Context(), actor.Username, input.OldPassword)
		if verifyErr != nil {
			writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", verifyErr)
			return
		}
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "当前密码不正确")
			return
		}
		// Verify may have transparently rehashed a legacy PBKDF2 hash; the
		// returned revision is the post-upgrade fence and must be used for
		// the CAS change instead of the pre-verification revision.
		revision = verified.CredentialRevision
	} else {
		revision, err = h.Auth.CurrentCredentialRevision(r.Context(), actor.SystemAccountID)
		if err != nil || revision == "" {
			writeJSONError(w, http.StatusNotFound, "系统账户不存在")
			return
		}
	}
	changed, err := h.Auth.ChangePassword(r.Context(), actor.SystemAccountID, revision, input.NewPassword, actor.SessionID)
	if err != nil {
		writeJSONErrorCause(w, http.StatusInternalServerError, "服务器内部错误", err)
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

// cookieToken 解析独立 listener `/auth/` 登录面的会话 cookie。
// BUG-0256 收尾：删除本包最后一个 first-wins 手写解析，委托
// resolveToken 的 cookie 分支，与主面 ParseCookie 统一 last-wins。
func cookieToken(r *http.Request) (string, error) {
	return resolveToken("", r.Header.Get("Cookie"))
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

// writeJSONErrorCause 写出固定 message 的错误响应，并把内部错误原文只写入
// 日志（SEC-05）：数据库/存储错误、SQL 片段与内部标识不得回显给客户端。
func writeJSONErrorCause(w http.ResponseWriter, status int, message string, err error) {
	if err != nil {
		slog.Error("J3b 请求处理失败",
			"event", "j3b_request_failed", "status", status, "error", err.Error())
	}
	writeJSONError(w, status, message)
}

var _ http.Handler = (*HTTPHandler)(nil)
