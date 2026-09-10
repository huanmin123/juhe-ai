// grok SSO 设备流（groksso.go）脚本化补测：convert 各阶段失败（SSO 未授权、
// 上游 5xx、device 响应不完整、未进 consent/done）、cookie 捕获与请求头、
// 重定向处理（POST→GET、跨域拒绝、缺 Location、次数上限、超大响应）与
// pollToken 的 pending/slow_down/access_denied/超时分支。全部走注入的
// 脚本传输与固定时钟，无网络、无 sleep。
package oauthmgmt

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// wcClock 可推进的固定时钟。
type wcClock struct{ nowMs int64 }

func (c *wcClock) Now() time.Time { return time.UnixMilli(c.nowMs) }

// wcScriptedDevice 按脚本返回响应并记录请求。
type wcScriptedDevice struct {
	steps   []SSODeviceResponse
	request []SSODeviceRequest
}

func (s *wcScriptedDevice) Do(_ context.Context, request SSODeviceRequest) (SSODeviceResponse, error) {
	s.request = append(s.request, request)
	if len(s.steps) == 0 {
		return SSODeviceResponse{StatusCode: 500, Body: "script exhausted"}, nil
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	return step, nil
}

// wcDeviceSuccessSteps 返回一条完整成功的设备流脚本。
func wcDeviceSuccessSteps(tokenBody string) []SSODeviceResponse {
	return []SSODeviceResponse{
		ssoStep(http.StatusOK, nil, "<html>accounts</html>"),
		ssoStep(http.StatusOK, nil, `{"device_code":"dc","user_code":"UC","verification_uri_complete":"https://auth.x.ai/device?user_code=UC","interval":1,"expires_in":600}`),
		ssoStep(http.StatusOK, nil, "<html>device</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/consent"}, ""),
		ssoStep(http.StatusOK, nil, "<html>consent</html>"),
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/oauth2/device/done"}, ""),
		ssoStep(http.StatusOK, nil, "<html>done</html>"),
		ssoStep(http.StatusOK, nil, tokenBody),
	}
}

// wcConvert 用给定脚本执行一次设备流转换。
func wcConvert(t *testing.T, steps []SSODeviceResponse, clock *wcClock) (*grokRawToken, error) {
	t.Helper()
	device := &wcScriptedDevice{steps: steps}
	deps := SSODeviceDeps{
		Request: device,
		Sleep:   func(context.Context, time.Duration) error { return nil },
		Now:     clock.Now,
	}
	return convertGrokSSOToOAuth(context.Background(), "sso-token-value", deps)
}

// TestWCS SO token normalization helpers live here.

// TestWCConvertFailures：convert 各阶段失败分支。
func TestWCConvertFailures(t *testing.T) {
	clock := &wcClock{nowMs: 1_000_000}
	cases := []struct {
		name    string
		steps   []SSODeviceResponse
		message string
	}{
		{"SSO 未授权 401", []SSODeviceResponse{ssoStep(http.StatusUnauthorized, nil, "<html>sign-in</html>")}, "xAI SSO 未授权"},
		{"SSO 页 500", []SSODeviceResponse{ssoStep(http.StatusInternalServerError, nil, "boom")}, "校验 Grok Web SSO 失败：xAI OAuth HTTP 500"},
		{"device 启动失败", append(wcDeviceSuccessSteps(`{}`)[:1], ssoStep(http.StatusInternalServerError, nil, "boom")), "启动 xAI device flow 失败：xAI OAuth HTTP 500"},
		{"device 响应不完整", append(wcDeviceSuccessSteps(`{}`)[:1], ssoStep(http.StatusOK, nil, `{"device_code":"dc"}`)), "xAI device flow 响应不完整"},
		{"验证页 500", append(wcDeviceSuccessSteps(`{}`)[:2], ssoStep(http.StatusInternalServerError, nil, "boom")), "打开 xAI device 验证页失败：xAI OAuth HTTP 500"},
		{"verify 失败", append(wcDeviceSuccessSteps(`{}`)[:3], ssoStep(http.StatusInternalServerError, nil, "boom")), "校验 xAI device code 失败：xAI OAuth HTTP 500"},
		{"未进 consent", append(wcDeviceSuccessSteps(`{}`)[:3], ssoStep(http.StatusOK, nil, "<html>no-consent</html>")), "xAI device 验证未进入 consent 页面"},
		{"approve 失败", append(wcDeviceSuccessSteps(`{}`)[:5], ssoStep(http.StatusInternalServerError, nil, "boom")), "批准 xAI device code 失败：xAI OAuth HTTP 500"},
		{"未进 done", append(wcDeviceSuccessSteps(`{}`)[:5], ssoStep(http.StatusOK, nil, "<html>no-done</html>")), "xAI device 授权未进入 done 页面"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := wcConvert(t, testCase.steps, clock)
			if err == nil || err.Error() != testCase.message {
				t.Fatalf("得到 %v，期望 %q", err, testCase.message)
			}
		})
	}
	// 空 SSO token。
	_, err := convertGrokSSOToOAuth(context.Background(), "  ", SSODeviceDeps{})
	if err == nil || err.Error() != "xAI SSO 未授权" {
		t.Fatalf("空 token: %v", err)
	}
}

// TestWCPollTokenBranches：token 轮询的 pending/slow_down/拒绝/超时分支。
func TestWCPollTokenBranches(t *testing.T) {
	clock := &wcClock{nowMs: 1_000_000}
	// pending 两次后成功。
	steps := wcDeviceSuccessSteps(`{}`)
	pendingSteps := append([]SSODeviceResponse{}, steps[:7]...)
	pendingSteps = append(pendingSteps,
		ssoStep(http.StatusBadRequest, nil, `{"error":"authorization_pending"}`),
		ssoStep(http.StatusBadRequest, nil, `{"error":"slow_down"}`),
		ssoStep(http.StatusOK, nil, `{"access_token":"at","refresh_token":"rt","id_token":"id","expires_in":600,"scope":"s"}`),
	)
	token, err := wcConvert(t, pendingSteps, clock)
	if err != nil {
		t.Fatalf("pending 后成功: %v", err)
	}
	if token.AccessToken != "at" || token.RefreshToken != "rt" || token.ExpiresIn != 600 {
		t.Fatalf("token 投影: %+v", token)
	}

	// access_denied → 400。
	deniedSteps := append(append([]SSODeviceResponse{}, steps[:7]...),
		ssoStep(http.StatusBadRequest, nil, `{"error":"access_denied"}`))
	if _, err = wcConvert(t, deniedSteps, clock); err == nil || err.Error() != "xAI device 授权被拒绝或已过期" {
		t.Fatalf("access_denied: %v", err)
	}
	// expired_token 同义。
	expiredSteps := append(append([]SSODeviceResponse{}, steps[:7]...),
		ssoStep(http.StatusBadRequest, nil, `{"error":"expired_token"}`))
	if _, err = wcConvert(t, expiredSteps, clock); err == nil || err.Error() != "xAI device 授权被拒绝或已过期" {
		t.Fatalf("expired_token: %v", err)
	}
	// 轮询 4xx 非 pending → 502 带详情。
	httpFailSteps := append(append([]SSODeviceResponse{}, steps[:7]...),
		ssoStep(http.StatusTooManyRequests, nil, `{"error":"rate_limited","error_description":"slow"}`))
	if _, err = wcConvert(t, httpFailSteps, clock); err == nil ||
		err.Error() != "xAI token 轮询失败：slow（HTTP 429）" {
		t.Fatalf("轮询 4xx: %v", err)
	}
	// 2xx 无 access_token 且无 error → 502。
	emptyOKSteps := append(append([]SSODeviceResponse{}, steps[:7]...),
		ssoStep(http.StatusOK, nil, `{"foo":1}`))
	if _, err = wcConvert(t, emptyOKSteps, clock); err == nil || !strings.Contains(err.Error(), "xAI token 轮询失败") {
		t.Fatalf("空成功响应: %v", err)
	}
	// 持续 pending 直到超期 → 轮询超时（时钟推进需大于 75s 上限）。
	longPending := append([]SSODeviceResponse{}, steps[:7]...)
	for index := 0; index < 40; index++ {
		longPending = append(longPending, ssoStep(http.StatusBadRequest, nil, `{"error":"authorization_pending"}`))
	}
	advancing := &wcClock{nowMs: 1_000_000}
	advancingNow := advancing.Now
	device := &wcScriptedDevice{steps: longPending}
	deps := SSODeviceDeps{
		Request: device,
		Sleep: func(_ context.Context, delay time.Duration) error {
			advancing.nowMs += int64(delay) + 1000
			return nil
		},
		Now: advancingNow,
	}
	if _, err = convertGrokSSOToOAuth(context.Background(), "sso-token-value", deps); err == nil ||
		err.Error() != "xAI device flow token 轮询超时" {
		t.Fatalf("轮询超时: %v", err)
	}
	// Sleep 取消。
	cancelSteps := append(append([]SSODeviceResponse{}, steps[:7]...),
		ssoStep(http.StatusBadRequest, nil, `{"error":"authorization_pending"}`))
	device = &wcScriptedDevice{steps: cancelSteps}
	deps = SSODeviceDeps{
		Request: device,
		Sleep:   func(context.Context, time.Duration) error { return context.Canceled },
		Now:     clock.Now,
	}
	if _, err = convertGrokSSOToOAuth(context.Background(), "sso-token-value", deps); err == nil ||
		err.Error() != "xAI SSO 转换已取消或超时" {
		t.Fatalf("取消: %v", err)
	}
}

// TestWCDeviceRequestRedirects：请求层的重定向/信任/大小分支。
func TestWCDeviceRequestRedirects(t *testing.T) {
	clock := &wcClock{nowMs: 1_000_000}
	newFlow := func(steps []SSODeviceResponse) (*grokSSODeviceFlow, *wcScriptedDevice) {
		device := &wcScriptedDevice{steps: steps}
		flow := &grokSSODeviceFlow{deps: SSODeviceDeps{
			Request: device,
			Sleep:   func(context.Context, time.Duration) error { return nil },
			Now:     clock.Now,
		}, cookies: map[string]grokSSOCookie{}}
		return flow, device
	}

	// 302 → POST 转 GET。
	flow, device := newFlow([]SSODeviceResponse{
		ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/next"}, ""),
		ssoStep(http.StatusOK, nil, "landing"),
	})
	response, err := flow.request(context.Background(), http.MethodPost, "https://auth.x.ai/start", map[string]string{"a": "b"})
	if err != nil || response.StatusCode != 200 || response.FinalURL != "https://auth.x.ai/next" {
		t.Fatalf("302 跟随: %+v %v", response, err)
	}
	if device.request[1].Method != http.MethodGet || device.request[1].Body != "" {
		t.Fatalf("302 必须 POST→GET: %+v", device.request[1])
	}

	// 307 保留 POST 与 body。
	flow, device = newFlow([]SSODeviceResponse{
		ssoStep(http.StatusTemporaryRedirect, map[string]string{"location": "https://auth.x.ai/keep"}, ""),
		ssoStep(http.StatusOK, nil, "ok"),
	})
	if _, err = flow.request(context.Background(), http.MethodPost, "https://auth.x.ai/start", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("307: %v", err)
	}
	if device.request[1].Method != http.MethodPost || device.request[1].Body == "" {
		t.Fatalf("307 保留 POST: %+v", device.request[1])
	}

	// 缺 Location。
	flow, _ = newFlow([]SSODeviceResponse{ssoStep(http.StatusFound, nil, "")})
	if _, err = flow.request(context.Background(), http.MethodGet, "https://auth.x.ai/x", nil); err == nil ||
		err.Error() != "xAI OAuth 重定向缺少 Location" {
		t.Fatalf("缺 Location: %v", err)
	}

	// 重定向到不受信任主机。
	flow, _ = newFlow([]SSODeviceResponse{
		ssoStep(http.StatusFound, map[string]string{"location": "https://evil.example/x"}, ""),
	})
	if _, err = flow.request(context.Background(), http.MethodGet, "https://auth.x.ai/x", nil); err == nil ||
		err.Error() != "xAI OAuth 重定向到不受信任的主机" {
		t.Fatalf("不受信任重定向: %v", err)
	}

	// 超过次数上限。
	loop := []SSODeviceResponse{}
	for index := 0; index <= grokSSOMaxRedirects+2; index++ {
		loop = append(loop, ssoStep(http.StatusFound, map[string]string{"location": "https://auth.x.ai/loop"}, ""))
	}
	flow, _ = newFlow(loop)
	if _, err = flow.request(context.Background(), http.MethodGet, "https://auth.x.ai/x", nil); err == nil ||
		err.Error() != "xAI OAuth 重定向次数过多" {
		t.Fatalf("重定向上限: %v", err)
	}

	// 超大响应。
	flow, _ = newFlow([]SSODeviceResponse{ssoStep(200, nil, strings.Repeat("x", 2*1024*1024+1))})
	if _, err = flow.request(context.Background(), http.MethodGet, "https://auth.x.ai/x", nil); err == nil ||
		err.Error() != "xAI OAuth 响应超过 2 MiB" {
		t.Fatalf("超大响应: %v", err)
	}

	// 初始 URL 不受信任。
	flow, _ = newFlow(nil)
	if _, err = flow.request(context.Background(), http.MethodGet, "https://evil.example/x", nil); err == nil ||
		err.Error() != "xAI OAuth URL 不受信任" {
		t.Fatalf("初始不受信任: %v", err)
	}
}

// TestWCCookieJar：捕获/过期/路径/域匹配与请求头排序。
func TestWCCookieJar(t *testing.T) {
	clock := &wcClock{nowMs: 1_000_000}
	flow := &grokSSODeviceFlow{deps: SSODeviceDeps{Now: clock.Now}, cookies: map[string]grokSSOCookie{}}

	// 捕获：domain 属性、path 缺省、max-age 删除与过期。
	flow.captureCookies(map[string]string{"set-cookie": "a=1; Domain=x.ai; Path=/deep; Max-Age=100"}, "https://auth.x.ai/deep/page")
	flow.captureCookies(map[string]string{"set-cookie": "b=2"}, "https://auth.x.ai/account/login")
	flow.captureCookies(map[string]string{"set-cookie": "gone=3; Max-Age=0"}, "https://auth.x.ai/")
	flow.captureCookies(map[string]string{"set-cookie": "expired=4; Max-Age=-5"}, "https://auth.x.ai/")
	flow.captureCookies(map[string]string{"set-cookie": "other=5; Domain=evil.example"}, "https://auth.x.ai/")
	flow.captureCookies(map[string]string{"set-cookie": "bad=" + strings.Repeat("x", 20000)}, "https://auth.x.ai/")

	// a 的 path=/deep；b 捕获于 /account/login，缺省 path=/account。
	deepHeader := flow.cookieHeader("https://auth.x.ai/deep/page")
	if !strings.Contains(deepHeader, "a=1") || strings.Contains(deepHeader, "b=2") {
		t.Fatalf("deep 页 cookie 头: %q", deepHeader)
	}
	accountHeader := flow.cookieHeader("https://auth.x.ai/account/login")
	if !strings.Contains(accountHeader, "b=2") || strings.Contains(accountHeader, "a=1") {
		t.Fatalf("account 页 cookie 头: %q", accountHeader)
	}
	if strings.Contains(deepHeader, "gone=") || strings.Contains(deepHeader, "other=") || strings.Contains(deepHeader, "bad=") {
		t.Fatalf("被删/跨域/超长 cookie 不得出现: %q", deepHeader)
	}
	// path 匹配：/deep cookie 不匹配其他路径请求。
	if strings.Contains(flow.cookieHeader("https://auth.x.ai/other"), "a=1") {
		t.Fatalf("path 不匹配的 cookie 不得出现")
	}

	// 单元分支。
	if !cookieDomainMatches("auth.x.ai", "x.ai") || cookieDomainMatches("auth.x.ai", "evil.example") {
		t.Fatalf("cookieDomainMatches")
	}
	if !cookiePathMatches("/deep/page", "/deep") || cookiePathMatches("/deepage", "/deep") {
		t.Fatalf("cookiePathMatches")
	}
	if defaultCookiePath("/deep/page") != "/deep" || defaultCookiePath("/") != "/" || defaultCookiePath("relative") != "/" || defaultCookiePath("/x") != "/" {
		t.Fatalf("defaultCookiePath")
	}
	if !isTrustedXAIAuthURL("https://auth.x.ai/x") || isTrustedXAIAuthURL("https://x.ai.evil.com/") ||
		isTrustedXAIAuthURL("http://auth.x.ai/") || isTrustedXAIAuthURL("https://user@auth.x.ai/") {
		t.Fatalf("isTrustedXAIAuthURL")
	}
	if sanitizeSSOToken(" a\r\nb\x00 ") != "ab" {
		t.Fatalf("sanitizeSSOToken: %q", sanitizeSSOToken(" a\r\nb\x00 "))
	}
	if minDuration(3, 5) != 3 || minDuration(9, 5) != 5 {
		t.Fatalf("minDuration")
	}
	if value, err := parseLeadingInt("42"); err != nil || value != 42 {
		t.Fatalf("parseLeadingInt: %d %v", value, err)
	}
	if _, err := parseLeadingInt("abc"); err == nil {
		t.Fatalf("parseLeadingInt 非数")
	}
	if value, err := parseLeadingInt("-7"); err != nil || value != -7 {
		t.Fatalf("parseLeadingInt 负数: %d %v", value, err)
	}
	// 设备错误类型。
	if got := grokSSOHTTPError("阶段失败", 500); got.StatusCode != 502 || got.Error() != "阶段失败：xAI OAuth HTTP 500" {
		t.Fatalf("grokSSOHTTPError: %+v", got)
	}
}

// TestWCNormalizeGrokSSOTokens：SSO token 归一化矩阵。
func TestWCNormalizeGrokSSOTokens(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"裸值", "abc", "abc"},
		{"cookie 前缀", "Cookie: sso=abc", "abc"},
		{"键值对 sso", "other=9; sso=abc; x=1", "abc"},
		{"键值对 sso-rw", "sso-rw=rwval", "rwval"},
		{"带换行", "ab\ncd", "abcd"},
		{"空白", "  ", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := normalizeGrokSSOToken(testCase.input); got != testCase.want {
				t.Fatalf("得到 %q，期望 %q", got, testCase.want)
			}
		})
	}
	tokens := normalizeGrokSSOImportTokens([]string{"b", "a\nb", " c "}, "a")
	if len(tokens) != 3 || tokens[0] != "a" || tokens[1] != "b" || tokens[2] != "c" {
		t.Fatalf("导入归一去重: %v", tokens)
	}
	if tokens := normalizeGrokSSOImportTokens(nil, ""); len(tokens) != 0 {
		t.Fatalf("空输入: %v", tokens)
	}
}
