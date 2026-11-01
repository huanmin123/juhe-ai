package authsys

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/businessauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// DD2-E8/DD2-E9 回归（2026-10-31）：改密面非 mustChange 分支复用登录面
// LoginGuard（同 10 次/10min 阈值、15min 锁定、429/锁定文案与 D8 fail-closed），
// mustChange 首改分支不经守卫，改密成功补记 auth.change_password 操作日志。

// dd2e8AlwaysFailPort 是仅拒绝凭据校验的端口桩：锁定阈值测试不需要真实账户，
// 失败路径在触碰 Accounts 之前就返回。
type dd2e8AlwaysFailPort struct {
	businessauth.Port
}

func (dd2e8AlwaysFailPort) VerifyCredentials(context.Context, string, string) (modelcheckauth.VerifiedCredentials, bool, error) {
	return modelcheckauth.VerifiedCredentials{}, false, nil
}

// dd2e8SpyGuard 委托内部驱动并计数 Check/Failed/Success 调用。
type dd2e8SpyGuard struct {
	inner     LoginGuardDriver
	mu        sync.Mutex
	checks    int
	faileds   int
	successes int
}

func (g *dd2e8SpyGuard) Check(ip, username string) (bool, int, string, error) {
	g.mu.Lock()
	g.checks++
	g.mu.Unlock()
	return g.inner.Check(ip, username)
}

func (g *dd2e8SpyGuard) Failed(ip, username string) (bool, int, string, error) {
	g.mu.Lock()
	g.faileds++
	g.mu.Unlock()
	return g.inner.Failed(ip, username)
}

func (g *dd2e8SpyGuard) Success(ip, username string) {
	g.mu.Lock()
	g.successes++
	g.mu.Unlock()
	g.inner.Success(ip, username)
}

func (g *dd2e8SpyGuard) counts() (int, int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.checks, g.faileds, g.successes
}

// TestDD2E8ChangePasswordLoginGuardLockout 钉住阈值与登录面完全同款：连续
// 错旧密码第 10 次触发锁定（429 + Retry-After + 登录面同款文案），第 11 次
// 被 Check 短路。内存驱动无外部依赖，默认回归即执行。
func TestDD2E8ChangePasswordLoginGuardLockout(t *testing.T) {
	deps := &Deps{
		Port:            dd2e8AlwaysFailPort{},
		LoginGuard:      modelcheckauth.NewLoginGuard(time.Now),
		CaptchaDisabled: true,
	}
	auth := &AuthContext{SystemAccountID: "acc", Username: "wlguard", Role: "admin", SessionID: "s1"}
	call := func() *httptest.ResponseRecorder {
		request := wlJSONRequest(http.MethodPost, "/change-password", `{"oldPassword":"wrong-pass","newPassword":"new-pass-9"}`)
		request = request.WithContext(WithAuthContext(request.Context(), auth))
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		return recorder
	}
	for attempt := 1; attempt <= 9; attempt++ {
		got := call()
		if got.Code != http.StatusBadRequest || !strings.Contains(got.Body.String(), "当前密码不正确") {
			t.Fatalf("第 %d 次错旧密码应 400 当前密码不正确: status=%d body=%s", attempt, got.Code, got.Body.String())
		}
	}
	blocked := call()
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("第 10 次应触发锁定 429: status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	if retryAfter := blocked.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatal("锁定响应必须携带 Retry-After")
	}
	message := strings.TrimSpace(blocked.Body.String())
	if !strings.Contains(message, "尝试过于频繁，请稍后再试") && !strings.Contains(message, "账号暂时锁定，请稍后再试") {
		t.Fatalf("锁定文案必须与登录面同款: %s", message)
	}
	again := call()
	if again.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定期间第 11 次应被 Check 短路 429: status=%d body=%s", again.Code, again.Body.String())
	}
}

// TestDD2E8ChangePasswordGuardContracts 钉住守卫分支形态：预置用户名锁返回
// 登录面同款「账号暂时锁定」429；守卫状态存储读/写故障按 D8 fail-closed
// 返回 500，绝不降级为「未锁定」。
func TestDD2E8ChangePasswordGuardContracts(t *testing.T) {
	t.Run("用户名预置锁定返回登录面同款 429", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.preset("login:username:wlguard:lock", time.Now().Add(time.Minute).UnixMilli())
		deps := &Deps{
			Port:            dd2e8AlwaysFailPort{},
			LoginGuard:      NewSharedLoginGuard(store, time.Now),
			CaptchaDisabled: true,
		}
		request := wlJSONRequest(http.MethodPost, "/change-password", `{"oldPassword":"wrong-pass","newPassword":"new-pass-9"}`)
		request = request.WithContext(WithAuthContext(request.Context(), &AuthContext{SystemAccountID: "acc", Username: "wlguard", Role: "admin", SessionID: "s1"}))
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "账号暂时锁定，请稍后再试") {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Retry-After") == "" {
			t.Fatal("锁定响应必须携带 Retry-After")
		}
	})
	t.Run("守卫锁检查存储故障返回 500", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.getJSONErr = errors.New("state down")
		deps := &Deps{
			Port:            dd2e8AlwaysFailPort{},
			LoginGuard:      NewSharedLoginGuard(store, time.Now),
			CaptchaDisabled: true,
		}
		request := wlJSONRequest(http.MethodPost, "/change-password", `{"oldPassword":"wrong-pass","newPassword":"new-pass-9"}`)
		request = request.WithContext(WithAuthContext(request.Context(), &AuthContext{SystemAccountID: "acc", Username: "wlguard", Role: "admin", SessionID: "s1"}))
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("守卫失败计数存储故障返回 500", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.incrErr = errors.New("state down")
		deps := &Deps{
			Port:            dd2e8AlwaysFailPort{},
			LoginGuard:      NewSharedLoginGuard(store, time.Now),
			CaptchaDisabled: true,
		}
		request := wlJSONRequest(http.MethodPost, "/change-password", `{"oldPassword":"wrong-pass","newPassword":"new-pass-9"}`)
		request = request.WithContext(WithAuthContext(request.Context(), &AuthContext{SystemAccountID: "acc", Username: "wlguard", Role: "admin", SessionID: "s1"}))
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}

// TestDD2E8ChangePasswordMustChangeBranchSkipsGuard 钉住 mustChange 首改分支
// 完全不经守卫：请求体携带错误旧密码也不计入任何 Check/Failed/Success。
// 依赖 PG 夹具（UpdatePassword 真实落库），未授权外部依赖时 SKIP。
func TestDD2E8ChangePasswordMustChangeBranchSkipsGuard(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	account := seedAccount(t, deps, "wlchgmust", "old-pass-1", "admin")
	deps.LoginGuard = &dd2e8SpyGuard{inner: deps.LoginGuard}
	forced := &AuthContext{SystemAccountID: account.ID, Username: "wlchgmust", Role: "admin", MustChangePassword: true, SessionID: "s2"}
	request := wlJSONRequest(http.MethodPost, "/change-password", `{"oldPassword":"deliberately-wrong","newPassword":"new-pass-9"}`)
	request = request.WithContext(WithAuthContext(request.Context(), forced))
	recorder := httptest.NewRecorder()
	deps.postChangePassword(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("mustChange 首改必须成功: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	checks, faileds, successes := deps.LoginGuard.(*dd2e8SpyGuard).counts()
	if checks != 0 || faileds != 0 || successes != 0 {
		t.Fatalf("mustChange 分支不得触碰守卫: check=%d failed=%d success=%d", checks, faileds, successes)
	}
}

// TestDD2E8ChangePasswordSuccessRecordsOperationLogAndClearsGuard 钉住：
// 正确改密成功后恰好写一条 auth.change_password 操作日志（不携带密码值），
// 且成功清理守卫失败计数（9 次错旧密码 + 1 次成功后，下一次错误仍 400）。
// 依赖 PG 夹具，未授权外部依赖时 SKIP。
func TestDD2E8ChangePasswordSuccessRecordsOperationLogAndClearsGuard(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	account := seedAccount(t, deps, "wlchglog", "old-pass-1", "admin")
	sink := &wlCaptureSink{}
	deps.Sink = sink
	auth := &AuthContext{SystemAccountID: account.ID, Username: "wlchglog", Role: "admin", SessionID: "s3"}
	call := func(body string) *httptest.ResponseRecorder {
		request := wlJSONRequest(http.MethodPost, "/change-password", body)
		request = request.WithContext(WithAuthContext(request.Context(), auth))
		recorder := httptest.NewRecorder()
		deps.postChangePassword(recorder, request)
		return recorder
	}
	for attempt := 1; attempt <= 9; attempt++ {
		if got := call(`{"oldPassword":"wrong-pass","newPassword":"new-pass-9"}`); got.Code != http.StatusBadRequest {
			t.Fatalf("第 %d 次错旧密码应 400: status=%d body=%s", attempt, got.Code, got.Body.String())
		}
	}
	got := call(`{"oldPassword":"old-pass-1","newPassword":"new-pass-9"}`)
	if got.Code != http.StatusOK {
		t.Fatalf("正确改密应成功: status=%d body=%s", got.Code, got.Body.String())
	}
	entries := sink.entries
	if len(entries) != 1 {
		t.Fatalf("改密成功必须恰好一条操作日志: %d", len(entries))
	}
	entry := entries[0]
	if entry.OperationKey != "auth.change_password" || entry.Action != "change_password" || entry.Mode != "self" || entry.Module != "system_accounts" {
		t.Fatalf("操作日志键族不符: %+v", entry)
	}
	if entry.OperationScopeSystemAccountID != account.ID || entry.ResourceID != account.ID || entry.ResourceType != "system_account" {
		t.Fatalf("操作日志作用域不符: %+v", entry)
	}
	if entry.ActorUsername != "wlchglog" {
		t.Fatalf("操作日志 actor 必须回填当前用户: %+v", entry)
	}
	if entry.Summary != "修改登录密码" {
		t.Fatalf("摘要不得携带密码值: %q", entry.Summary)
	}
	if len(entry.Changes) != 1 {
		t.Fatalf("变更集必须是单条 password: %+v", entry.Changes)
	}
	change := entry.Changes[0]
	if change.Field != "password" || change.Label != "登录密码" || change.Before != "已设置" || change.After != "已变更" || !change.Sensitive {
		t.Fatalf("password 变更形态不符: %+v", change)
	}
	for _, banned := range []string{"old-pass-1", "new-pass-9"} {
		if strings.Contains(entry.Summary, banned) {
			t.Fatalf("摘要不得包含密码值: %q", entry.Summary)
		}
		for _, logged := range entry.Changes {
			if strings.Contains(logged.Before, banned) || strings.Contains(logged.After, banned) {
				t.Fatalf("变更值不得包含密码值: %+v", logged)
			}
		}
	}
	// 成功改密必须清理守卫失败计数（Success）：否则上次残留已达阈值边缘。
	if after := call(`{"oldPassword":"still-wrong","newPassword":"another-pass"}`); after.Code != http.StatusBadRequest {
		t.Fatalf("成功后守卫计数应已清理，下一次错误应 400: status=%d body=%s", after.Code, after.Body.String())
	}
}
