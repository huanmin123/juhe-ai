package oidc

// w9e 覆盖率战役：补 oidc 路由层禁用/503/守卫臂、客户端认证辅助、
// 重定向匹配与损坏行的可达分支。全部基于 mock SQLite env，不触网络。

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

var (
	w9eTransactionFieldPattern = regexp.MustCompile(`name="transaction_id" value="([^"]*)"`)
	w9eCSRFFieldPattern        = regexp.MustCompile(`name="csrf_token" value="([^"]*)"`)
)

func w9eMount(deps *Deps) *kernel.Kernel {
	k := kernel.New(kernel.Options{})
	deps.Mount(k)
	return k
}

func w9eFirstMatch(t *testing.T, pattern *regexp.Regexp, body string) string {
	t.Helper()
	match := pattern.FindStringSubmatch(body)
	if len(match) < 2 {
		t.Fatalf("pattern %v 未命中: %.300s", pattern, body)
	}
	return match[1]
}

func TestW9EOIDCHelperUnits(t *testing.T) {
	if got := pathOnly("/oauth/token?x=1"); got != "/oauth/token" {
		t.Fatalf("pathOnly = %q", got)
	}
	// Deps.Now 为 nil 时回退 time.Now（只验证非零值语义）。
	deps := &Deps{}
	if deps.now().IsZero() {
		t.Fatal("now 回退不应为零值")
	}
	if (&OAuthRouteError{Message: "m"}).Error() != "m" {
		t.Fatal("OAuthRouteError.Error 契约不符")
	}
	if (&OidcUnavailableError{}).Error() == "" {
		t.Fatal("OidcUnavailableError.Error 不应为空")
	}
	// writeRouteError 三个分支。
	rec := httptest.NewRecorder()
	deps.writeRouteError(rec, &OidcUnavailableError{})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	deps.writeRouteError(rec, &OAuthRouteError{Code: "invalid_request", Message: "bad", StatusCode: 400})
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("route error = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	deps.writeRouteError(rec, errW9EStub)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "server_error") {
		t.Fatalf("default = %d %s", rec.Code, rec.Body.String())
	}
	// secondsUntil。
	future := isoMillis(time.Now().Add(time.Hour))
	if got, err := secondsUntil(future, time.Now()); err != nil || got <= 0 {
		t.Fatalf("secondsUntil = %d err=%v", got, err)
	}
	if got, err := secondsUntil(isoMillis(time.Now().Add(-time.Hour)), time.Now()); err != nil || got != 0 {
		t.Fatalf("过期 secondsUntil = %d err=%v", got, err)
	}
	if _, err := secondsUntil("not-a-time", time.Now()); err == nil {
		t.Fatal("非法时间必须失败")
	}
	// Basic 认证解析。
	r := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	if _, _, ok := basicAuthParts(r); ok {
		t.Fatal("无 Basic 头应 false")
	}
	r.Header.Set("Authorization", "Basic !!!!not-base64!!!")
	if _, _, ok := basicAuthParts(r); ok {
		t.Fatal("坏 base64 应 false")
	}
	r.Header.Set("Authorization", "Basic "+b64Encode("nocolon"))
	if _, _, ok := basicAuthParts(r); ok {
		t.Fatal("无冒号应 false")
	}
	r.Header.Set("Authorization", "Basic "+b64Encode(":secret"))
	if _, _, ok := basicAuthParts(r); ok {
		t.Fatal("空 id 应 false")
	}
	r.Header.Set("Authorization", "Basic "+b64Encode("id:sec"))
	if id, secret, ok := basicAuthParts(r); !ok || id != "id" || secret != "sec" {
		t.Fatalf("basicAuthParts = %q %q %v", id, secret, ok)
	}
	// token 请求 client id 提取。
	if got := clientIDFromTokenRequest(r, nil); got != "id" {
		t.Fatalf("Basic client id = %q", got)
	}
	r2 := httptest.NewRequest(http.MethodPost, "/oauth/token", nil)
	r2.Header.Set("Authorization", "Basic broken")
	if got := clientIDFromTokenRequest(r2, nil); got != "" {
		t.Fatalf("坏 Basic 应回退空, got %q", got)
	}
	// normalizeScopes：分隔、去重、空白。
	got := normalizeScopes("openid  profile\nopenid\tprofile")
	if len(got) != 2 || got[0] != "openid" || got[1] != "profile" {
		t.Fatalf("normalizeScopes = %v", got)
	}
}

var errW9EStub = &w9eStubError{}

type w9eStubError struct{}

func (*w9eStubError) Error() string { return "stub" }

func b64Encode(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func TestW9EOIDCDisabledArmsAllEndpoints(t *testing.T) {
	base := newStoreEnv(t)
	deps := &Deps{Store: base.store, OIDCEnabled: false, OIDCIssuer: oidcTestIssuer, Now: base.clock.Now}
	k := w9eMount(deps)
	do := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		rec := httptest.NewRecorder()
		k.Handler().ServeHTTP(rec, req)
		return rec
	}
	targets := []struct {
		method, path string
	}{
		{http.MethodGet, "/.well-known/openid-configuration"},
		{http.MethodGet, "/oauth/jwks"},
		{http.MethodGet, "/oauth/authorize"},
		{http.MethodPost, "/oauth/authorize/decision"},
		{http.MethodPost, "/oauth/device_authorization"},
		{http.MethodGet, "/oauth/device"},
		{http.MethodPost, "/oauth/device/decision"},
		{http.MethodPost, "/oauth/token"},
		{http.MethodPost, "/oauth/token/renew"},
		{http.MethodPost, "/oauth/revoke"},
		{http.MethodGet, "/oauth/userinfo"},
	}
	for _, item := range targets {
		rec := do(item.method, item.path)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "OIDC Provider 未启用") {
			t.Fatalf("%s %s = %d %s", item.method, item.path, rec.Code, rec.Body.String())
		}
	}
}

func TestW9EOIDCKeyMissingReturnsUnavailable(t *testing.T) {
	env := newRouteEnv(t)
	// ensureKey 中间件会在空表时惰性自举新密钥，清空行不会触发 503；
	// 直接删除签名密钥表使 EnsureSigningKey 失败 → 全部端点 503。
	mustExec(t, env.db, `DROP TABLE oauth_signing_keys`)
	targets := []struct {
		method, path string
	}{
		{http.MethodGet, "/.well-known/openid-configuration"},
		{http.MethodGet, "/oauth/jwks"},
		{http.MethodGet, "/oauth/authorize"},
		{http.MethodPost, "/oauth/authorize/decision"},
		{http.MethodPost, "/oauth/device_authorization"},
		{http.MethodGet, "/oauth/device"},
		{http.MethodPost, "/oauth/device/decision"},
		{http.MethodPost, "/oauth/token"},
		{http.MethodPost, "/oauth/token/renew"},
		{http.MethodPost, "/oauth/revoke"},
		{http.MethodGet, "/oauth/userinfo"},
	}
	for _, item := range targets {
		rec := env.do(t, item.method, item.path, nil, "")
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "temporarily_unavailable") {
			t.Fatalf("%s %s = %d %s", item.method, item.path, rec.Code, rec.Body.String())
		}
	}
}

func TestW9EOIDCMountWithNilLimiter(t *testing.T) {
	base := newStoreEnv(t)
	deps := &Deps{Store: base.store, Limiter: nil, OIDCEnabled: true, OIDCIssuer: oidcTestIssuer, Now: base.clock.Now}
	k := w9eMount(deps)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil)
	rec := httptest.NewRecorder()
	k.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), oidcTestIssuer) {
		t.Fatalf("nil limiter discovery = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9EOIDCAuthorizeTransactionClientInvalid(t *testing.T) {
	env := newRouteEnv(t)
	// 创建授权事务，再把 client 置为 disabled → "Client 或回调地址无效"。
	create := env.get(t, "/oauth/authorize"+env.authorizeQuery(nil), sessionCookie(env.sessionToken))
	if create.Code != http.StatusOK {
		t.Fatalf("authorize create = %d", create.Code)
	}
	mustExec(t, env.db, `UPDATE oauth_clients SET status='disabled' WHERE client_id=?`, env.publicID)
	transactionID := extractTransactionID(t, create.Body.String())
	rec := env.get(t, "/oauth/authorize?transaction_id="+transactionID, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Client 或回调地址无效") {
		t.Fatalf("dead client authorize = %d %s", rec.Code, rec.Body.String())
	}
	// 不存在的 transaction id → invalid_request。
	missing := env.get(t, "/oauth/authorize?transaction_id=00000000-0000-0000-0000-000000000000", nil)
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), "授权请求不存在或已过期") {
		t.Fatalf("missing transaction = %d %s", missing.Code, missing.Body.String())
	}
}

func extractTransactionID(t *testing.T, html string) string {
	t.Helper()
	return w9eFirstMatch(t, w9eTransactionFieldPattern, html)
}

func TestW9EOIDCDecisionGuards(t *testing.T) {
	env := newRouteEnv(t)
	// 非法参数。
	bad := env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": "short", "csrf_token": "x", "decision": "allow",
	}, nil)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "授权确认请求无效") {
		t.Fatalf("bad decision = %d %s", bad.Code, bad.Body.String())
	}
	// 无会话。
	create := env.get(t, "/oauth/authorize"+env.authorizeQuery(nil), sessionCookie(env.sessionToken))
	transactionID := extractTransactionID(t, create.Body.String())
	csrf := extractField(t, create.Body.String(), "csrf_token")
	noSession := env.postForm(t, "/oauth/authorize/decision", map[string]string{
		"transaction_id": transactionID, "csrf_token": csrf, "decision": "allow",
	}, nil)
	if noSession.Code != http.StatusBadRequest || !strings.Contains(noSession.Body.String(), "授权确认请求无效") {
		t.Fatalf("无会话 decision = %d %s", noSession.Code, noSession.Body.String())
	}
}

func extractField(t *testing.T, html, name string) string {
	t.Helper()
	if name == "csrf_token" {
		return w9eFirstMatch(t, w9eCSRFFieldPattern, html)
	}
	t.Fatalf("未知字段 %s", name)
	return ""
}

func TestW9EOIDCFormBodyRequiresFormContentType(t *testing.T) {
	env := newRouteEnv(t)
	// 非 form 内容类型按空表单处理：token 流会走到客户端认证失败（401）。
	rec := env.do(t, http.MethodPost, "/oauth/token", map[string]string{"Content-Type": "application/json"}, `{}`)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_client") {
		t.Fatalf("非 form 内容类型 = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9EMatchesRegisteredRedirectURI(t *testing.T) {
	registered := []string{"https://app.example.com/callback", "http://127.0.0.1:7777/callback"}
	if !matchesRegisteredRedirectUri(registered, "https://app.example.com/callback") {
		t.Fatal("精确匹配应通过")
	}
	if matchesRegisteredRedirectUri(registered, "https://app.example.com/other") {
		t.Fatal("不同路径不应通过")
	}
	if matchesRegisteredRedirectUri(registered, "https://evil.example.com/callback") {
		t.Fatal("不同主机不应通过")
	}
	// 回环主机必须 http + 回环 host。
	if !matchesRegisteredRedirectUri(registered, "http://127.0.0.1:7777/callback") {
		t.Fatal("回环匹配应通过")
	}
	// Node 语义：回环主机的端口不参与匹配（同 host/path/query 即通过）。
	if !matchesRegisteredRedirectUri(registered, "http://127.0.0.1:9999/callback") {
		t.Fatal("回环端口不同也应通过（端口灵活）")
	}
	if matchesRegisteredRedirectUri(registered, "https://127.0.0.1:7777/callback") {
		t.Fatal("回环匹配要求 http scheme")
	}
	if matchesRegisteredRedirectUri(registered, "http://127.0.0.1:7777/callback#frag") {
		t.Fatal("带 fragment 的请求不应通过")
	}
	if !isLoopbackHostname("::1") || !isLoopbackHostname("127.0.0.1") || isLoopbackHostname("example.com") {
		t.Fatal("isLoopbackHostname 契约不符")
	}
}

func TestW9EOIDCCorruptTransactionRows(t *testing.T) {
	env := newRouteEnv(t)
	create := env.get(t, "/oauth/authorize"+env.authorizeQuery(nil), sessionCookie(env.sessionToken))
	transactionID := extractTransactionID(t, create.Body.String())
	// 损坏 state 密文 → FindAuthorizationTransaction 报错 → 500。
	mustExec(t, env.db, `UPDATE oauth_authorization_transactions SET state_ciphertext='bad.bad.bad' WHERE id=?`, transactionID)
	rec := env.get(t, "/oauth/authorize?transaction_id="+transactionID, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("损坏 state = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9EOIDCTokenWithCorruptSigningKeyPreflight(t *testing.T) {
	env := newRouteEnv(t)
	// 私钥密文损坏：renew 走到客户端认证（401），说明密钥预检在认证之后。
	mustExec(t, env.db, `UPDATE oauth_signing_keys SET private_key_ciphertext='corrupt.corrupt.corrupt'`)
	grantID := "grant-corrupt"
	env.insertGrant(t, grantID, env.clock.Now().Add(24*time.Hour))
	env.insertAccessTokenRow(t, "corrupt-token", grantID, env.clock.Now(), env.clock.Now().Add(time.Hour))
	rec := env.postForm(t, "/oauth/token/renew", map[string]string{"current_access_token": "corrupt-token"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("corrupt key renew = %d %s", rec.Code, rec.Body.String())
	}
}

func TestW9EOIDCDeviceFlowGuards(t *testing.T) {
	env := newRouteEnv(t)
	// 未认证客户端。
	rec := env.postForm(t, "/oauth/device_authorization", map[string]string{"scope": "openid"}, nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_client") {
		t.Fatalf("device unauth = %d %s", rec.Code, rec.Body.String())
	}
	// 公开客户端 + 空 scope。
	publicAuth := map[string]string{"client_id": env.publicID}
	rec = env.postForm(t, "/oauth/device_authorization", publicAuth, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_scope") {
		t.Fatalf("device no scope = %d %s", rec.Code, rec.Body.String())
	}
	// profile 缺 openid。
	rec = env.postForm(t, "/oauth/device_authorization", map[string]string{"client_id": env.publicID, "scope": "profile"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("device profile-only = %d %s", rec.Code, rec.Body.String())
	}
	// openid 缺 nonce。
	rec = env.postForm(t, "/oauth/device_authorization", map[string]string{"client_id": env.publicID, "scope": "openid"}, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "必须提供 nonce") {
		t.Fatalf("device openid no nonce = %d %s", rec.Code, rec.Body.String())
	}
	// getDevice 无 user_code → 输入页 HTML。
	page := env.get(t, "/oauth/device", nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<form") {
		t.Fatalf("device entry = %d", page.Code)
	}
	// user_code 无会话 → 登录跳转。
	redirect := env.get(t, "/oauth/device?user_code=ABCD-1234", nil)
	if redirect.Code != http.StatusFound || !strings.Contains(redirect.Header().Get("Location"), "/__aisys__/login") {
		t.Fatalf("device redirect = %d %v", redirect.Code, redirect.Header().Get("Location"))
	}
}
