package authsys

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// BUG-0248 契约 1：LoginGuard.Failed 返回非空错误时（D8 fail-closed 的 500
// 分支），failureReason 必须记录 guard 错误本身，而不是早已为 nil 的外层 err
// （WriteErrorCause 仅在 cause != nil && status >= 500 时 RecordFailureReason，
// 传 nil 会让登录风暴中状态存储故障表现为大量无因 500）。

const bug0248GuardReason = "bug0248 guard state store down"

// bug0248FailingGuard 只让 Failed 失败（Check/Success 正常），驱动三个
// guardErr 分支。
type bug0248FailingGuard struct{}

func (bug0248FailingGuard) Check(ip, username string) (bool, int, string, error) {
	return false, 0, "", nil
}

func (bug0248FailingGuard) Failed(ip, username string) (bool, int, string, error) {
	return false, 0, "", errors.New(bug0248GuardReason)
}

func (bug0248FailingGuard) Success(ip, username string) {}

// bug0248Invoke 直接驱动 handler 并捕获请求级 failureReason：外层套
// RequestContextMiddleware（否则 Context(r) 每次返回临时对象，记录无处落），
// 内层在 handler 返回后读回同一 RequestContext 的 FailureReason。
func bug0248Invoke(handler http.Handler, method, target, body string) (*httptest.ResponseRecorder, string) {
	reason := ""
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		reason = kernel.Context(r).FailureReason()
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	// 直接调用 handler 时没有真实传输层补写 Content-Length 头，而
	// kernel.DecodeJSON 的 hasRequestBody 读的是头（非 r.ContentLength），
	// 不补头会让 body 被当作空体跳过解码。
	request.Header.Set("Content-Length", strconv.Itoa(len(body)))
	// 临时访问令牌路径要求来源 IP 命中白名单（newTestEnv 注入 127.0.0.1）。
	request.RemoteAddr = "127.0.0.1:1234"
	kernel.RequestContextMiddleware(0)(wrapped).ServeHTTP(recorder, request)
	return recorder, reason
}

func TestBug0248LoginGuardFailedErrorRecordsFailureReason(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	seedAccount(t, deps, "bug0248a", "pass-1234", "admin")
	originalGuard := deps.LoginGuard
	originalPort := deps.Port
	t.Cleanup(func() {
		deps.LoginGuard = originalGuard
		deps.Port = originalPort
	})
	login := deps.postLogin("lax", false)
	t.Run("密码错误且 Failed 存储故障时 failureReason 记录 guard 错误", func(t *testing.T) {
		deps.LoginGuard = bug0248FailingGuard{}
		recorder, reason := bug0248Invoke(login, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"bug0248a","password":"wrong-pass"}`)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500", recorder.Code)
		}
		if reason != bug0248GuardReason {
			t.Fatalf("failureReason=%q want %q", reason, bug0248GuardReason)
		}
	})
	t.Run("会话签发被拒且 Failed 存储故障时 failureReason 记录 guard 错误", func(t *testing.T) {
		deps.LoginGuard = bug0248FailingGuard{}
		deps.Port = sessionCreationRejectedPort{Port: originalPort}
		recorder, reason := bug0248Invoke(login, http.MethodPost, "/__aisys__/api/auth/login", `{"username":"bug0248a","password":"pass-1234"}`)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500", recorder.Code)
		}
		if reason != bug0248GuardReason {
			t.Fatalf("failureReason=%q want %q", reason, bug0248GuardReason)
		}
	})
	t.Run("临时访问令牌失败计数存储故障时 failureReason 记录 guard 错误", func(t *testing.T) {
		deps.LoginGuard = bug0248FailingGuard{}
		deps.Port = originalPort
		recorder, reason := bug0248Invoke(http.HandlerFunc(deps.postTemporaryAccessToken), http.MethodPost, "/__aisys__/api/auth/temporary-access-tokens", `{"username":"bug0248a","password":"wrong-pass","ttlSeconds":900}`)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500", recorder.Code)
		}
		if reason != bug0248GuardReason {
			t.Fatalf("failureReason=%q want %q", reason, bug0248GuardReason)
		}
	})
}
