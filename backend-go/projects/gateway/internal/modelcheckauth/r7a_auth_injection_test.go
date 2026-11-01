package modelcheckauth

// R7-A 修复钉住测试（A-H1 / A-H3 / A-H8）：J3b HTTPHandler 的共享守卫
// 注入与 fail-closed、会话 cookie Secure 注入、clientIP trustProxy 注入。
// 全部不依赖外部 PG：守卫臂用 fake 驱动断言（Mock 优先），cookie 用
// logout 清除臂（不触库），clientIP 用临时令牌白名单端到端区分 403/500。

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeSharedGuardDriver 是共享登录守卫驱动替身：记录 Check/Failed/Success
// 调用（ip|username），Check 可注入错误以驱动 fail-closed 臂。
type fakeSharedGuardDriver struct {
	mu        sync.Mutex
	checks    []string
	failed    []string
	success   []string
	checkErr  error
	failedErr error
}

func (g *fakeSharedGuardDriver) record(list *[]string, ip, username string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	*list = append(*list, ip+"|"+username)
}

func (g *fakeSharedGuardDriver) snapshot() ([]string, []string, []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string{}, g.checks...), append([]string{}, g.failed...), append([]string{}, g.success...)
}

func (g *fakeSharedGuardDriver) Check(ip, username string) (bool, int, string, error) {
	g.record(&g.checks, ip, username)
	if g.checkErr != nil {
		return false, 0, "", g.checkErr
	}
	return false, 0, "", nil
}

func (g *fakeSharedGuardDriver) Failed(ip, username string) (bool, int, string, error) {
	g.record(&g.failed, ip, username)
	if g.failedErr != nil {
		return false, 0, "", g.failedErr
	}
	return false, 0, "", nil
}

func (g *fakeSharedGuardDriver) Success(ip, username string) {
	g.record(&g.success, ip, username)
}

func r7aLoginRequest(username, password string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	request.RemoteAddr = "127.0.0.1:8080"
	return request
}

// TestJ3bSharedGuardInjectionAndSharedMountInstance 钉住 A-H1：登录先过
// 注入的共享守卫（Check 收到真实 ip|username），认证器失败（nil db）后
// 走 500；两次挂载（3307 别名与主端口根 mux 的同款形态）共享同一
// handler 实例即共享同一守卫实例，不产生双预算。
func TestJ3bSharedGuardInjectionAndSharedMountInstance(t *testing.T) {
	guard := &fakeSharedGuardDriver{}
	handler := &HTTPHandler{Auth: &Authenticator{db: nil}, SharedGuard: guard}
	// nil db 认证器把登录判为凭据不匹配（ok=false）：Check → Failed → 401
	// 全程经注入驱动，可端到端断言失败计数进入共享 guard。
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r7aLoginRequest("Admin", "secret"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("nil db 登录必须按凭据不匹配 401: %d %s", recorder.Code, recorder.Body.String())
	}
	checks, failed, _ := guard.snapshot()
	if len(checks) != 1 || checks[0] != "127.0.0.1|Admin" {
		t.Fatalf("共享守卫 Check 未收到注入调用: %+v", checks)
	}
	if len(failed) != 1 || failed[0] != "127.0.0.1|Admin" {
		t.Fatalf("登录失败计数必须进注入的共享 guard: %+v", failed)
	}
	// 两次挂载引用同一 handler（main.go j3bManagementMount 的装配形态）：
	// 两个 mux 各挂一次，守卫调用必须落在同一实例上。
	muxOne, muxTwo := http.NewServeMux(), http.NewServeMux()
	muxOne.Handle("/auth/", http.StripPrefix("/auth", handler))
	muxTwo.Handle("/auth/", http.StripPrefix("/auth", handler))
	for _, mux := range []http.Handler{muxOne, muxTwo} {
		recorder = httptest.NewRecorder()
		mux.ServeHTTP(recorder, func() *http.Request {
			request := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
			request.RemoteAddr = "127.0.0.1:8080"
			return request
		}())
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("第二次挂载必须仍走同一 handler: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	checks, failed, _ = guard.snapshot()
	if len(checks) != 3 || len(failed) != 3 {
		t.Fatalf("两次挂载必须共享同一守卫实例（累计 3 次 Check/Failed）: checks=%+v failed=%+v", checks, failed)
	}
}

// TestJ3bSharedGuardFailClosed 钉住 A-H1 fail-closed 臂：注入驱动的
// Check 故障必须回 500「服务器内部错误」，不得读作"未锁定"继续认证。
func TestJ3bSharedGuardFailClosed(t *testing.T) {
	guard := &fakeSharedGuardDriver{checkErr: errors.New("state store down")}
	handler := &HTTPHandler{Auth: &Authenticator{db: nil}, SharedGuard: guard}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r7aLoginRequest("Admin", "secret"))
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "服务器内部错误") {
		t.Fatalf("守卫存储故障必须 fail-closed 500: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestJ3bLoginGuardDriverSelection 钉住 loginGuard 驱动选择与 Failed
// 包装：注入驱动优先于内存臂；未锁定时放行后续 401 契约，驱动故障时
// fail-closed 500。
func TestJ3bLoginGuardDriverSelection(t *testing.T) {
	guard := &fakeSharedGuardDriver{}
	handler := &HTTPHandler{SharedGuard: guard}
	if handler.loginGuard() != SharedLoginGuardDriver(guard) {
		t.Fatal("注入的共享驱动必须优先于内存臂")
	}
	recorder := httptest.NewRecorder()
	if !handler.recordLoginFailure(recorder, "198.51.100.7", "admin") {
		t.Fatal("未锁定时必须放行后续 401 契约")
	}
	_, failed, _ := guard.snapshot()
	if len(failed) != 1 || failed[0] != "198.51.100.7|admin" {
		t.Fatalf("Failed 计数必须进注入驱动: %+v", failed)
	}
	guard.failedErr = errors.New("state store down")
	recorder = httptest.NewRecorder()
	if handler.recordLoginFailure(recorder, "198.51.100.7", "admin") {
		t.Fatal("驱动故障必须 fail-closed")
	}
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "服务器内部错误") {
		t.Fatalf("驱动故障必须 500 统一文案: %d %s", recorder.Code, recorder.Body.String())
	}
	// 无注入时保持内存臂（测试兼容）：惰性构造 *LoginGuard。
	legacy := &HTTPHandler{}
	if _, ok := legacy.loginGuard().(*LoginGuard); !ok {
		t.Fatal("未注入时必须回落进程内内存臂")
	}
}

// TestJ3bSessionCookieSecureFollowsInjection 钉住 A-H3：cookie Secure
// 属性随注入（logout 清除臂不触库即可断言 SetCookie 属性）；未注入时
// 保持既有无 Secure 行为。
func TestJ3bSessionCookieSecureFollowsInjection(t *testing.T) {
	cookieByName := func(recorder *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		for _, cookie := range recorder.Result().Cookies() {
			if cookie.Name == SessionCookieName {
				return cookie
			}
		}
		t.Fatalf("缺少 %s cookie", SessionCookieName)
		return nil
	}
	secure := &HTTPHandler{Auth: &Authenticator{db: nil}, CookieSecure: true}
	recorder := httptest.NewRecorder()
	secure.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("logout status=%d", recorder.Code)
	}
	if cookie := cookieByName(recorder); !cookie.Secure {
		t.Fatalf("注入 CookieSecure 后 cookie 必须 Secure: %+v", cookie)
	}
	plain := &HTTPHandler{Auth: &Authenticator{db: nil}}
	recorder = httptest.NewRecorder()
	plain.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if cookie := cookieByName(recorder); cookie.Secure {
		t.Fatalf("未注入时必须保持既有无 Secure 行为: %+v", cookie)
	}
}

// TestJ3bClientIPTrustProxyInjection 钉住 A-H8：注入 trustProxyCount 后
// clientIP 取 XFF 受信条目（临时令牌白名单按真实客户端 IP 命中/拒绝，
// 即 byIP 判定按真实 IP 隔离），未注入时忽略 XFF、保持 RemoteAddr 语义。
func TestJ3bClientIPTrustProxyInjection(t *testing.T) {
	temporaryRequest := func(xff string) *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/temporary-access-tokens", strings.NewReader(`{"username":"admin","password":"secret","ttlSeconds":120}`))
		request.RemoteAddr = "10.0.0.2:1234"
		if xff != "" {
			request.Header.Set("X-Forwarded-For", xff)
		}
		return request
	}
	// 白名单写真实客户端 IP：trustProxy=1 时经 XFF 命中（通过白名单后
	// 进入认证，nil db → 500，而非白名单 403）。
	handler := &HTTPHandler{Auth: &Authenticator{db: nil}, TemporaryAccessIPAllowlist: []string{"203.0.113.7"}, TrustProxyCount: 1}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, temporaryRequest("203.0.113.7"))
	// 白名单按真实客户端 IP（XFF 受信条目）命中：通过白名单进入守卫与
	// 认证（nil db 凭据不匹配 → 401），而非白名单 403——即 byIP 判定已
	// 按 XFF 解析值隔离，不再全员归并为代理 IP。
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "账号或密码错误") {
		t.Fatalf("trustProxy 注入后白名单必须按真实客户端 IP 命中（进入认证 401）: %d %s", recorder.Code, recorder.Body.String())
	}
	// 不同真实客户端 IP 仍在白名单外 → 403（byIP 桶按 XFF 解析值隔离）。
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, temporaryRequest("198.51.100.5"))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("白名单外真实 IP 必须拒绝: %d %s", recorder.Code, recorder.Body.String())
	}
	// 未注入（TrustProxyCount=0）：忽略可伪造 XFF，保持 RemoteAddr 语义。
	legacy := &HTTPHandler{Auth: &Authenticator{db: nil}, TemporaryAccessIPAllowlist: []string{"10.0.0.2"}}
	recorder = httptest.NewRecorder()
	legacy.ServeHTTP(recorder, temporaryRequest("203.0.113.7"))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("未注入时必须按 RemoteAddr 判定白名单（10.0.0.2 命中→进入认证 401）: %d %s", recorder.Code, recorder.Body.String())
	}
	// 解析方法回退：无 XFF 时 trustProxy 注入面也回落 RemoteAddr。
	if got := handler.clientIP(httptest.NewRequest(http.MethodGet, "/", nil)); got != "192.0.2.1" {
		t.Fatalf("无 XFF 必须回落 RemoteAddr，got %q", got)
	}
}
