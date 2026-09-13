package authsys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// wlFakeStateStore 是 RedisStateStore 的进程内确定性实现：用 map 模拟
// JSON 状态存储并支持错误注入，用于在不依赖 Redis 的情况下驱动
// captcha/login-guard 及各 500 分支。
type wlFakeStateStore struct {
	mu      sync.Mutex
	values  map[string]string
	incrErr error
}

func newWlFakeStateStore() *wlFakeStateStore {
	return &wlFakeStateStore{values: map[string]string{}}
}

func (s *wlFakeStateStore) GetJSON(_ context.Context, key string, dst any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.values[key]
	if !ok {
		return false, nil
	}
	if json.Unmarshal([]byte(raw), dst) != nil {
		delete(s.values, key)
		return false, nil
	}
	return true, nil
}

func (s *wlFakeStateStore) GetDeleteJSON(ctx context.Context, key string, dst any) (bool, error) {
	ok, err := s.GetJSON(ctx, key, dst)
	s.mu.Lock()
	delete(s.values, key)
	s.mu.Unlock()
	return ok, err
}

func (s *wlFakeStateStore) SetJSON(_ context.Context, key string, value any, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.values[key] = string(encoded)
	return nil
}

func (s *wlFakeStateStore) DeleteJSON(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func (s *wlFakeStateStore) Incr(_ context.Context, key string, _ int64, max int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.incrErr != nil {
		return 0, s.incrErr
	}
	var current int64
	if raw, ok := s.values[key]; ok {
		_ = json.Unmarshal([]byte(raw), &current)
	}
	current++
	if max >= 0 && current > max {
		return current, nil
	}
	encoded, _ := json.Marshal(current)
	s.values[key] = string(encoded)
	return current, nil
}

func (s *wlFakeStateStore) raw(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.values[key]
	return raw, ok
}

func (s *wlFakeStateStore) preset(key string, value any) {
	encoded, _ := json.Marshal(value)
	s.mu.Lock()
	s.values[key] = string(encoded)
	s.mu.Unlock()
}

func TestWlGetCaptchaContracts(t *testing.T) {
	deps, _, server := newTestEnv(t)
	t.Run("禁用时不需要验证码", func(t *testing.T) {
		response, payload := getJSON(t, server, "/__aisys__/api/auth/captcha", "")
		data, _ := payload["data"].(map[string]any)
		if response.StatusCode != http.StatusOK || data["required"] != false {
			t.Fatalf("status=%d payload=%v", response.StatusCode, payload)
		}
	})
	t.Run("启用时签发挑战", func(t *testing.T) {
		deps.CaptchaDisabled = false
		response, payload := getJSON(t, server, "/__aisys__/api/auth/captcha", "")
		data, _ := payload["data"].(map[string]any)
		if response.StatusCode != http.StatusOK || data["required"] != true {
			t.Fatalf("status=%d payload keys=%v", response.StatusCode, payload)
		}
		if id, _ := data["captchaId"].(string); id == "" {
			t.Fatalf("captchaId 缺失: %v", payload)
		}
		if image, _ := data["image"].(string); !strings.HasPrefix(image, "data:image/png;base64,") {
			t.Fatalf("image=%q", data["image"])
		}
	})
	t.Run("签发被限流", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.preset("issue:127.0.0.1", int64(60))
		deps.Captcha = NewSharedCaptchaService(store, deps.Now)
		response, _ := getJSON(t, server, "/__aisys__/api/auth/captcha", "")
		if response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status=%d", response.StatusCode)
		}
		if got := response.Header.Get("Retry-After"); got != "60" {
			t.Fatalf("Retry-After=%q", got)
		}
	})
	t.Run("状态存储失败返回 500", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.incrErr = errors.New("redis down")
		deps.Captcha = NewSharedCaptchaService(store, deps.Now)
		response, _ := getJSON(t, server, "/__aisys__/api/auth/captcha", "")
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
}

func TestWlPostLoginValidationBranches(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "wllogin", "pass-1234", "admin")
	tests := []struct {
		name, body string
		want       int
	}{
		{"非 JSON body", `not-json`, http.StatusBadRequest},
		{"缺用户名", `{"password":"x"}`, http.StatusBadRequest},
		{"含空格", `{"username":"a b","password":"p w"}`, http.StatusBadRequest},
		{"密码错误", `{"username":"wllogin","password":"wrong-pass"}`, http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, _ := postJSON(t, server, "/__aisys__/api/auth/login", test.body, "")
			if response.StatusCode != test.want {
				t.Fatalf("status=%d want=%d", response.StatusCode, test.want)
			}
		})
	}
	t.Run("验证码必填", func(t *testing.T) {
		deps.CaptchaDisabled = false
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"wllogin","password":"pass-1234"}`, "")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("验证码错误拒绝", func(t *testing.T) {
		deps.CaptchaDisabled = false
		deps.Captcha = NewSharedCaptchaService(newWlFakeStateStore(), deps.Now)
		body := `{"username":"wllogin","password":"pass-1234","captchaId":"c1","captchaCode":"AAAAA"}`
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", body, "")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("验证码通过后登录成功", func(t *testing.T) {
		deps.CaptchaDisabled = false
		store := newWlFakeStateStore()
		deps.Captcha = NewSharedCaptchaService(store, deps.Now)
		result, err := deps.Captcha.Issue("127.0.0.1")
		if err != nil {
			t.Fatalf("签发验证码失败: %v", err)
		}
		// fake store 中保留了正确答案，回填后应通过验证。
		raw, ok := store.raw("challenge:" + result.Challenge.CaptchaID)
		if !ok {
			t.Fatal("挑战未写入状态存储")
		}
		var record captchaChallengeRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			t.Fatalf("挑战记录非法: %v", err)
		}
		body := fmt.Sprintf(`{"username":"wllogin","password":"pass-1234","captchaId":%q,"captchaCode":%q}`, result.Challenge.CaptchaID, record.Answer)
		response, payload := postJSON(t, server, "/__aisys__/api/auth/login", body, "")
		data, _ := payload["data"].(map[string]any)
		if response.StatusCode != http.StatusOK || data["username"] != "wllogin" {
			t.Fatalf("status=%d payload=%v", response.StatusCode, payload)
		}
	})
}

func TestWlPostLoginLockoutAndServerErrors(t *testing.T) {
	deps, _, server := newTestEnv(t)
	originalPort := deps.Port
	seedAccount(t, deps, "wllock", "pass-1234", "admin")
	t.Run("IP 锁定优先", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.preset("login:ip:127.0.0.1:lock", time.Now().Add(time.Minute).UnixMilli())
		deps.LoginGuard = NewSharedLoginGuard(store, deps.Now)
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"wllock","password":"pass-1234"}`, "")
		if response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.LoginGuard = modelcheckauth.NewLoginGuard(deps.Now)
	})
	t.Run("凭据校验失败返回 500", func(t *testing.T) {
		deps.Port = verifyCredentialsFailingPort{Port: originalPort, err: errors.New("boom")}
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"wllock","password":"pass-1234"}`, "")
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
	})
	t.Run("会话创建失败返回 401", func(t *testing.T) {
		deps.Port = sessionCreationRejectedPort{Port: originalPort}
		response, _ := postJSON(t, server, "/__aisys__/api/auth/login", `{"username":"wllock","password":"pass-1234"}`, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
	})
}

func TestWlAuthHandlersDirectContracts(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	recorder := httptest.NewRecorder()
	deps.getMe(recorder, httptest.NewRequest(http.MethodGet, "/me", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 getMe status=%d", recorder.Code)
	}
	account := seedAccount(t, deps, "wldirect", "pass-1234", "admin")
	t.Run("getMe 已登录", func(t *testing.T) {
		request := withWlAuth(httptest.NewRequest(http.MethodGet, "/me", nil), account.ID, "wldirect", "admin", false)
		recorder := httptest.NewRecorder()
		deps.getMe(recorder, request)
		var payload struct {
			Data CurrentUserSummary `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload.Data.Username != "wldirect" {
			t.Fatalf("payload=%s err=%v", recorder.Body.String(), err)
		}
	})
	t.Run("getProfile 账户缺失返回 404", func(t *testing.T) {
		request := withWlAuth(httptest.NewRequest(http.MethodGet, "/profile", nil), "missing-id", "ghost", "admin", false)
		recorder := httptest.NewRecorder()
		deps.getProfile(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("getProfile 返回有效限额", func(t *testing.T) {
		request := withWlAuth(httptest.NewRequest(http.MethodGet, "/profile", nil), account.ID, "wldirect", "admin", false)
		recorder := httptest.NewRecorder()
		deps.getProfile(recorder, request)
		var payload struct {
			Data ProfileResponse `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("body=%s err=%v", recorder.Body.String(), err)
		}
		if payload.Data.EffectiveRequestLimits.Timezone != "Asia/Shanghai" {
			t.Fatalf("timezone=%q", payload.Data.EffectiveRequestLimits.Timezone)
		}
	})
	t.Run("patchMe 参数校验链", func(t *testing.T) {
		tests := []struct {
			name, body string
			want       int
		}{
			{"缺 displayName", `{}`, http.StatusBadRequest},
			{"空 displayName", `{"displayName":""}`, http.StatusBadRequest},
			{"含空格", `{"displayName":"a b"}`, http.StatusBadRequest},
		}
		for _, test := range tests {
			request := withWlAuth(wlJSONRequest(http.MethodPatch, "/me", test.body), account.ID, "wldirect", "admin", false)
			recorder := httptest.NewRecorder()
			deps.patchMe(recorder, request)
			if recorder.Code != test.want {
				t.Fatalf("%s status=%d body=%s", test.name, recorder.Code, recorder.Body.String())
			}
		}
	})
	t.Run("patchMe 同名短路", func(t *testing.T) {
		request := withWlAuth(wlJSONRequest(http.MethodPatch, "/me", `{"displayName":"WLDIRECT_Name"}`), account.ID, "wldirect", "admin", false)
		recorder := httptest.NewRecorder()
		deps.patchMe(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("patchMe 改名成功并记录操作日志", func(t *testing.T) {
		sink := &wlCaptureSink{}
		deps.Sink = sink
		request := withWlAuth(wlJSONRequest(http.MethodPatch, "/me", `{"displayName":"新名字"}`), account.ID, "wldirect", "admin", false)
		recorder := httptest.NewRecorder()
		deps.patchMe(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if len(sink.entries) != 1 || sink.entries[0].OperationKey != "auth.update_profile" || sink.entries[0].ActorUsername != "wldirect" {
			t.Fatalf("操作日志=%+v", sink.entries)
		}
	})
	t.Run("patchMe 账户缺失返回 404", func(t *testing.T) {
		request := withWlAuth(wlJSONRequest(http.MethodPatch, "/me", `{"displayName":"x"}`), "missing", "ghost", "admin", false)
		recorder := httptest.NewRecorder()
		deps.patchMe(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status=%d", recorder.Code)
		}
	})
	t.Run("patchMe 必须先改密", func(t *testing.T) {
		request := withWlAuth(wlJSONRequest(http.MethodPatch, "/me", `{"displayName":"x"}`), account.ID, "wldirect", "admin", true)
		recorder := httptest.NewRecorder()
		deps.patchMe(recorder, request)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "must_change_password") {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

// wlCaptureSink 捕获操作日志条目。
type wlCaptureSink struct {
	mu      sync.Mutex
	entries []OperationLogEntry
}

func (s *wlCaptureSink) Record(entry OperationLogEntry, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

// wlJSONRequest 构造带 JSON 媒体类型与 Content-Length 头的请求：
// kernel.DecodeJSON 通过 Content-Length 头判断是否有请求体，
// httptest.NewRequest 只填充字段不填充该头。
func wlJSONRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return request
}

func withWlAuth(request *http.Request, accountID, username, role string, mustChange bool) *http.Request {
	return request.WithContext(WithAuthContext(request.Context(), &AuthContext{
		SystemAccountID: accountID, Username: username, DisplayName: strings.ToUpper(username) + "_Name",
		Role: role, MustChangePassword: mustChange, SessionID: "wl-session",
	}))
}

func TestWlPostChangePasswordContracts(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	account := seedAccount(t, deps, "wlpasswd", "old-pass-1", "admin")
	call := func(body string, auth *AuthContext) *httptest.ResponseRecorder {
		request := wlJSONRequest(http.MethodPost, "/change-password", body)
		if auth != nil {
			request = request.WithContext(WithAuthContext(request.Context(), auth))
		}
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		return recorder
	}
	if got := call(`{}`, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("未登录 status=%d", got.Code)
	}
	auth := &AuthContext{SystemAccountID: account.ID, Username: "wlpasswd", Role: "admin", SessionID: "s1"}
	tests := []struct {
		name, body string
		want       int
	}{
		{"密码过短", `{"newPassword":"abc"}`, http.StatusBadRequest},
		{"密码含空格", `{"newPassword":"a b c d"}`, http.StatusBadRequest},
		{"缺当前密码", `{"newPassword":"new-pass-9"}`, http.StatusBadRequest},
		{"当前密码错误", `{"oldPassword":"wrong-pass","newPassword":"new-pass-9"}`, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := call(test.body, auth); got.Code != test.want {
				t.Fatalf("%s status=%d body=%s", test.name, got.Code, got.Body.String())
			}
		})
	}
	t.Run("携带正确旧密码修改成功", func(t *testing.T) {
		got := call(`{"oldPassword":"old-pass-1","newPassword":"new-pass-9"}`, auth)
		if got.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
		}
		verified, ok, err := deps.Port.VerifyCredentials(context.Background(), "wlpasswd", "new-pass-9")
		if err != nil || !ok || verified.SystemAccountID != account.ID {
			t.Fatalf("新密码验证失败: ok=%v err=%v", ok, err)
		}
	})
	t.Run("强制改密账户免旧密码", func(t *testing.T) {
		forced := &AuthContext{SystemAccountID: account.ID, Username: "wlpasswd", Role: "admin", MustChangePassword: true, SessionID: "s2"}
		got := call(`{"newPassword":"another-pass"}`, forced)
		if got.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
		}
		if updated, err := deps.Accounts.FindByID(context.Background(), account.ID); err != nil || updated.MustChangePassword {
			t.Fatalf("改密后必须清除强制标记: %+v err=%v", updated, err)
		}
	})
}

func TestWlPostTemporaryAccessTokenContracts(t *testing.T) {
	deps, _, server := newTestEnv(t)
	seedAccount(t, deps, "wltempadmin", "admin-pass-1", "admin")
	seedAccount(t, deps, "wltempuser", "user-pass-1", "user")
	tests := []struct {
		name, body string
		want       int
	}{
		{"非 JSON", `not-json`, http.StatusBadRequest},
		{"非对象 JSON", `[]`, http.StatusBadRequest},
		{"未知字段", `{"username":"a","extra":1}`, http.StatusBadRequest},
		{"username 非字符串", `{"username":123}`, http.StatusBadRequest},
		{"TTL 为 null", `{"username":"wltempadmin","password":"admin-pass-1","ttlSeconds":null}`, http.StatusBadRequest},
		{"TTL 非整数", `{"username":"wltempadmin","password":"admin-pass-1","ttlSeconds":90.5}`, http.StatusBadRequest},
		{"TTL 过小", `{"username":"wltempadmin","password":"admin-pass-1","ttlSeconds":10}`, http.StatusBadRequest},
		{"TTL 过大", `{"username":"wltempadmin","password":"admin-pass-1","ttlSeconds":99999}`, http.StatusBadRequest},
		{"缺密码", `{"username":"wltempadmin"}`, http.StatusBadRequest},
		{"含空格", `{"username":"a b","password":"p w"}`, http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", test.body, "")
			if response.StatusCode != test.want {
				t.Fatalf("%s status=%d", test.name, response.StatusCode)
			}
		})
	}
	t.Run("非白名单来源拒绝", func(t *testing.T) {
		deps.TemporaryAccessIPAllowlist = []string{"10.0.0.1"}
		response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"wltempadmin","password":"admin-pass-1"}`, "")
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.TemporaryAccessIPAllowlist = []string{"127.0.0.1"}
	})
	t.Run("普通用户拒绝", func(t *testing.T) {
		response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"wltempuser","password":"user-pass-1"}`, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("管理员成功签发", func(t *testing.T) {
		response, payload := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"wltempadmin","password":"admin-pass-1","ttlSeconds":120}`, "")
		data, _ := payload["data"].(map[string]any)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d payload=%v", response.StatusCode, payload)
		}
		if token, _ := data["token"].(string); !strings.HasPrefix(token, "juhe_tmp_") {
			t.Fatalf("token=%v", data["token"])
		}
		if data["tokenType"] != "Bearer" {
			t.Fatalf("tokenType=%v", data["tokenType"])
		}
	})
}

func TestWlTemporaryTokenRevokeContracts(t *testing.T) {
	deps, _, server := newTestEnv(t)
	originalPort := deps.Port
	seedAccount(t, deps, "wlrevoke", "admin-pass-1", "admin")
	issue := func() string {
		response, payload := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"wlrevoke","password":"admin-pass-1"}`, "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("签发失败 status=%d payload=%v", response.StatusCode, payload)
		}
		data, _ := payload["data"].(map[string]any)
		token, _ := data["token"].(string)
		return token
	}
	postRevoke := func(token string) int {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/__aisys__/api/auth/temporary-access-tokens/revoke", strings.NewReader(`{}`))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response.StatusCode
	}
	t.Run("缺少凭据在会话层拒绝", func(t *testing.T) {
		// revoke 挂在 RequireSession 后：无任何凭据时中间件先返回 401。
		if got := postRevoke(""); got != http.StatusUnauthorized {
			t.Fatalf("status=%d", got)
		}
	})
	t.Run("普通会话令牌不能撤销", func(t *testing.T) {
		cookie := login(t, server, "wlrevoke", "admin-pass-1")
		response, _ := postJSON(t, server, "/__aisys__/api/auth/temporary-access-tokens/revoke", `{}`, cookie)
		// handler 收到非临时 token 必须以 400 拒绝（只能撤销当前临时令牌）。
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("撤销成功", func(t *testing.T) {
		if got := postRevoke(issue()); got != http.StatusOK {
			t.Fatalf("status=%d", got)
		}
	})
	t.Run("撤销失败返回 500", func(t *testing.T) {
		token := issue()
		deps.Port = revokeFailingPort{Port: originalPort, err: errors.New("boom")}
		if got := postRevoke(token); got != http.StatusInternalServerError {
			t.Fatalf("status=%d", got)
		}
		deps.Port = originalPort
	})
}

func TestWlSessionMiddlewareBranches(t *testing.T) {
	deps, _, server := newTestEnv(t)
	originalPort := deps.Port
	seedAccount(t, deps, "wlmiddleware", "pass-1234", "user")
	t.Run("非法 Authorization 拒绝", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/__aisys__/api/auth/me", nil)
		request.Header.Set("Authorization", "Basic abc")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("未配置开发自动登录且无令牌返回 401", func(t *testing.T) {
		response, _ := getJSON(t, server, "/__aisys__/api/auth/me", "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("配置开发自动登录但账户缺失返回 500", func(t *testing.T) {
		deps.DevAutoLoginUsername = "ghost-user"
		response, _ := getJSON(t, server, "/__aisys__/api/auth/me", "")
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.DevAutoLoginUsername = ""
	})
	t.Run("开发自动登录成功放行", func(t *testing.T) {
		deps.DevAutoLoginUsername = "wlmiddleware"
		response, payload := getJSON(t, server, "/__aisys__/api/auth/me", "")
		data, _ := payload["data"].(map[string]any)
		if response.StatusCode != http.StatusOK || data["username"] != "wlmiddleware" {
			t.Fatalf("status=%d payload=%v", response.StatusCode, payload)
		}
		deps.DevAutoLoginUsername = ""
	})
	t.Run("用户限流拦截", func(t *testing.T) {
		cookie := login(t, server, "wlmiddleware", "pass-1234")
		deps.AuthenticatedRateLimit = func(w http.ResponseWriter, _ *http.Request, _ string) bool {
			w.WriteHeader(http.StatusTooManyRequests)
			return false
		}
		response, _ := getJSON(t, server, "/__aisys__/api/auth/me", cookie)
		if response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.AuthenticatedRateLimit = nil
	})
	t.Run("角色门槛", func(t *testing.T) {
		cookie := login(t, server, "wlmiddleware", "pass-1234")
		response, _ := getJSON(t, server, "/__aisys__/api/system-accounts", cookie)
		// user 角色访问 admin 接口必须 403。
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("会话过期返回 401", func(t *testing.T) {
		cookie := login(t, server, "wlmiddleware", "pass-1234")
		deps.Port = authenticateFailingPort{Port: originalPort, err: modelcheckauth.ErrSessionExpired}
		response, _ := getJSON(t, server, "/__aisys__/api/auth/me", cookie)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
	})
	t.Run("会话校验内部错误返回 500", func(t *testing.T) {
		cookie := login(t, server, "wlmiddleware", "pass-1234")
		deps.Port = authenticateFailingPort{Port: originalPort, err: errors.New("boom")}
		response, _ := getJSON(t, server, "/__aisys__/api/auth/me", cookie)
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf("status=%d", response.StatusCode)
		}
		deps.Port = originalPort
	})
}

func TestWlSystemAccountsListContracts(t *testing.T) {
	deps, _, server := newTestEnv(t)
	admin := seedAccount(t, deps, "wllistadmin", "pass-1234", "admin")
	seedAccount(t, deps, "wllistuser", "pass-1234", "user")
	cookie := login(t, server, "wllistadmin", "pass-1234")
	t.Run("分页与非法分页参数", func(t *testing.T) {
		response, payload := getJSON(t, server, "/__aisys__/api/system-accounts?page=abc&pageSize=-3", cookie)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", response.StatusCode)
		}
		data, _ := payload["data"].(map[string]any)
		// 非法 page 回落默认 1；pageSize=-3 可解析则按原值透传。
		if data["page"] != float64(1) || data["pageSize"] != float64(-3) {
			t.Fatalf("page=%v pageSize=%v", data["page"], data["pageSize"])
		}
	})
	t.Run("options 过滤", func(t *testing.T) {
		response, _ := getJSON(t, server, "/__aisys__/api/system-accounts/options?ids="+admin.ID+",missing&keyword=wl", cookie)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
	t.Run("未登录访问列表拒绝", func(t *testing.T) {
		response, _ := getJSON(t, server, "/__aisys__/api/system-accounts", "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status=%d", response.StatusCode)
		}
	})
}
