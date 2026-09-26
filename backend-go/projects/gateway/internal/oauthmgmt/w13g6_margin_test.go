// w13g6_margin_test.go lifts the remaining statement coverage of the
// oauthmgmt package past the 95% gate with a durable margin.
//
// 不可达语句归因登记（本文件登记，供覆盖率审计核对）：
//   - anthropic.go:145 回调缺 state 臂：URL/查询/fragment 三种 requiresState 形式均在
//     extractAnthropicCodeAndState 第 125 行的同义校验处提前报错，145 为纵深防御重复判定。
//   - crypto.go:32 rand.Read 失败臂与 crypto.go:91/95 newGCM 错误臂：sha256(secret) 恒为合法
//     AES-256 密钥，crypto/aes 与 cipher.NewGCM 对其不返回错误；crypto/rand 读取不失败。
//   - routes.go:126 guarded 包装的 auth==nil 臂：全部路由先经过 RequireAdmin/RequireSession 中间件，
//     匿名请求在中间件即被 401，包装内判定为纵深防御。
//   - groksso.go:517 captureCookies 的"带过期时间但已过期"清理臂：hasExpiry 仅在 max-age>0 时置位，
//     expiresAt = now()+seconds 恒晚于同一 now()，条件恒假。
//   - routes.go 725/791/855（updated==nil）与 store.go:683（affected != 1）：RotateCredentials 的
//     乐观锁 0 行以 RevisionConflictError 表达，单请求内先经 703/769/833 的版本核对，nil/0 行
//     结果需要同一账户行并发写入，顺序测试不可构造。
//   - routes.go:925 findGroupForProvider 错误臂：resolveProviderProfile 与群组查询共用同一 DB，
//     前者成功则后者不产生独立错误（无逐查询故障注入点）。
package oauthmgmt

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// ---------------------------------------------------------------------------
// 纯函数与解析器
// ---------------------------------------------------------------------------

func TestW13g6UnitArms(t *testing.T) {
	if itoa64(0) != "0" || itoa64(65) != "65" {
		t.Fatal("itoa64 arms")
	}
	if text, ok := bodyString(map[string]any{"k": 5}, "k"); ok || text != "" {
		t.Fatalf("bodyString non-string: %q %v", text, ok)
	}
	if got, ok := parseStringSlice([]any{"a", "b", "c"}, 2); ok || got != nil {
		t.Fatalf("parseStringSlice cap: %v %v", got, ok)
	}
	// enrichGeminiTokenInfo 的静态档位兜底臂。
	if got := enrichGeminiTokenInfo(&geminiTokenInfo{OAuthType: "ai_studio"}).TierID; got != "aistudio_free" {
		t.Fatalf("ai_studio default tier: %s", got)
	}
	if got := enrichGeminiTokenInfo(&geminiTokenInfo{OAuthType: "google_one"}).TierID; got != "google_one_free" {
		t.Fatalf("google_one default tier: %s", got)
	}
	if got := enrichGeminiTokenInfo(&geminiTokenInfo{OAuthType: "code_assist"}).TierID; got != "gcp_standard" {
		t.Fatalf("code_assist default tier: %s", got)
	}
	// buildGeminiOAuthCredentials 的 fallback==nil 拾取臂（562-564：info 侧为空且无 fallback）。
	credentials := buildGeminiOAuthCredentials(&geminiTokenInfo{AccessToken: "at", RefreshToken: ""}, nil)
	if _, exists := credentials["refresh_token"]; exists {
		t.Fatalf("refresh_token without fallback must be omitted: %v", credentials["refresh_token"])
	}
	if _, exists := credentials["scope"]; exists {
		t.Fatalf("scope without fallback must be omitted: %v", credentials["scope"])
	}
	// crypto 错误臂。
	if _, err := encryptJSON(testSecret, map[string]any{"x": make(chan int)}); err == nil {
		t.Fatal("encryptJSON marshal error arm")
	}
	var target map[string]any
	if err := decryptJSON(testSecret, "v1:!!!!:!!!!:!!!!", &target); err == nil {
		t.Fatal("decryptJSON base64 error arm")
	}
	// 日志写入的 nil 防御臂。
	(&Deps{}).recordUpdateLog(nil, providerPlan{}, AccessScope{}, nil, nil, nil, "w13g6", "w13g6")
}

func TestW13g6CredentialsPatchParsers(t *testing.T) {
	// OpenAI patch 分支。
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"supported_endpoint_modes": []any{}}); ok {
		t.Fatal("openai empty modes must fail")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"service_tier_override": 7}); ok {
		t.Fatal("openai non-string tier must fail")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"service_tier_override": "  "}); ok {
		t.Fatal("openai blank tier must fail")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"service_tier_override": "ultra"}); ok {
		t.Fatal("openai unknown tier must fail")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"reasoning_effort_override": "mega"}); ok {
		t.Fatal("openai unknown effort must fail")
	}
	if _, ok := parseOpenAICredentialsPatch(map[string]any{"unknown": 1}); ok {
		t.Fatal("openai unknown key must fail")
	}
	if out, ok := parseOpenAICredentialsPatch(map[string]any{
		"supported_endpoint_modes": []any{"chat", "responses"},
		"quota_recovery_policy":    map[string]any{"a": 1},
	}); !ok || len(out) != 2 {
		t.Fatalf("openai valid patch: %v %v", out, ok)
	}
	// Anthropic patch 分支。
	if _, ok := parseAnthropicCredentialsPatch(map[string]any{"base_url": 3}); ok {
		t.Fatal("anthropic non-string base_url must fail")
	}
	if _, ok := parseAnthropicCredentialsPatch(map[string]any{"base_url": " "}); ok {
		t.Fatal("anthropic blank base_url must fail")
	}
	if out, ok := parseAnthropicCredentialsPatch(map[string]any{"base_url": " https://a.b "}); !ok || out["base_url"] != "https://a.b" {
		t.Fatalf("anthropic valid patch: %v %v", out, ok)
	}
	// Gemini patch 分支（base_url 必须是 URL）。
	if _, ok := parseGeminiCredentialsPatch(map[string]any{"base_url": "not-a-url"}); ok {
		t.Fatal("gemini bad base_url must fail")
	}
	if out, ok := parseGeminiCredentialsPatch(map[string]any{"base_url": "https://g.c"}); !ok || out["base_url"] != "https://g.c" {
		t.Fatalf("gemini valid patch: %v %v", out, ok)
	}
	// Grok patch 分支。
	if _, ok := parseGrokCredentialsPatch(map[string]any{"base_url": 9}); ok {
		t.Fatal("grok non-string base_url must fail")
	}
	if _, ok := parseGrokCredentialsPatch(map[string]any{"nope": 1}); ok {
		t.Fatal("grok unknown key must fail")
	}
	// safePatch 分发到各 plan 闭包（plans.go 的 patch 字面量与 providers.go 分发行）。
	for _, plan := range providerPlans() {
		if _, ok := safePatch(plan, map[string]any{}); !ok {
			t.Fatalf("%s absent patch must pass", plan.slug)
		}
		if _, ok := safePatch(plan, map[string]any{"credentialsPatch": "bad"}); ok {
			t.Fatalf("%s non-object patch must fail", plan.slug)
		}
		if _, ok := safePatch(plan, map[string]any{"credentialsPatch": map[string]any{"nope": 1}}); ok {
			t.Fatalf("%s unknown patch key must fail", plan.slug)
		}
	}
}

// ---------------------------------------------------------------------------
// 真实 SSO 传输与设备流臂
// ---------------------------------------------------------------------------

func TestW13g6RealSSORequesterArms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/landing", http.StatusFound)
			return
		}
		w.Header().Set("set-cookie", "a=1")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	// 默认 requester：headers 注入臂（77-79）与 302 的 CheckRedirect 臂（68-70）。
	sso := defaultSSODeviceRequester()
	response, err := sso.Do(context.Background(), SSODeviceRequest{
		Method: "POST", URL: server.URL + "/start", Body: "a=b",
		Headers: map[string]string{"x-w13g6": "1"},
	})
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("plain sso request: %+v %v", response, err)
	}
	redirect, err := sso.Do(context.Background(), SSODeviceRequest{Method: "GET", URL: server.URL + "/redirect"})
	if err != nil || redirect.StatusCode != 302 {
		t.Fatalf("redirect sso request: %+v %v", redirect, err)
	}
	// Store 未注入 requester 时的兜底构造（store.go 139-141）。
	store := w13g6NewStore(t, nil, nil)
	if store.ssoDeps().Request == nil {
		t.Fatal("default sso requester must be wired")
	}
}

func TestW13g6SSOFlowArms(t *testing.T) {
	fixed := func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	newFlow := func(steps []SSODeviceResponse) *grokSSODeviceFlow {
		return &grokSSODeviceFlow{deps: SSODeviceDeps{
			Request: &scriptedSSORequester{steps: steps},
			Sleep:   func(context.Context, time.Duration) error { return nil },
			Now:     fixed,
		}, cookies: map[string]grokSSOCookie{}}
	}

	// pollToken：亚秒 interval 收敛（301-303）+ 轮询失败带 detail（357-361）。
	flow := newFlow([]SSODeviceResponse{{StatusCode: 500, Body: `{"error":"boom-code","error_description":"boom-desc"}`}})
	if _, err := flow.pollToken(context.Background(), "w13g6-dev", 100*time.Millisecond, time.Minute); err == nil ||
		!strings.Contains(err.Error(), "boom-desc") {
		t.Fatalf("poll failure detail: %v", err)
	}
	// 非失败状态码带/不带 detail 的两个兜底臂（362-367）。
	flowDetail := newFlow([]SSODeviceResponse{{StatusCode: 100, Body: `{"error_description":"odd-desc"}`}})
	if _, err := flowDetail.pollToken(context.Background(), "w13g6-dev", time.Second, time.Minute); err == nil ||
		!strings.Contains(err.Error(), "odd-desc") {
		t.Fatalf("poll odd status detail: %v", err)
	}
	flowPlain := newFlow([]SSODeviceResponse{{StatusCode: 100, Body: `{}`}})
	if _, err := flowPlain.pollToken(context.Background(), "w13g6-dev", time.Second, time.Minute); err == nil ||
		!strings.Contains(err.Error(), "HTTP 100") {
		t.Fatalf("poll odd status plain: %v", err)
	}
	// request 重定向链：Location 解析失败 -> 不受信任主机（422-424）。
	flow2 := newFlow([]SSODeviceResponse{{StatusCode: 302, Headers: map[string]string{"location": "://bad"}}})
	if _, err := flow2.request(context.Background(), http.MethodGet, "https://auth.x.ai/w13g6", nil); err == nil ||
		!strings.Contains(err.Error(), "不受信任") {
		t.Fatalf("redirect parse failure: %v", err)
	}
	// captureCookies 的 URL 解析失败臂（446-448）。
	flow3 := newFlow(nil)
	flow3.captureCookies(map[string]string{"set-cookie": "a=1"}, "://bad")
	// cookieHeader：host-only 域不匹配（547-548）与 domain 域不匹配（550-551）。
	flow3.captureCookies(map[string]string{"set-cookie": "host=1; Path=/x"}, "https://svc.example.com/x/y")
	flow3.captureCookies(map[string]string{"set-cookie": "dom=1; Domain=example.com; Path=/x"}, "https://svc.example.com/x/y")
	if got := flow3.cookieHeader("https://other.example.com/x"); got != "dom=1" {
		t.Fatalf("host-only mismatch filter: %q", got)
	}
	if got := flow3.cookieHeader("https://www.notexample.com/x"); got != "" {
		t.Fatalf("domain mismatch filter: %q", got)
	}
	if got := flow3.cookieHeader("://bad"); got != "" {
		t.Fatal("bad cookie URL must render empty")
	}
}

// ---------------------------------------------------------------------------
// Store 构造辅助
// ---------------------------------------------------------------------------

type w13g6StaticError string

func (e w13g6StaticError) Error() string { return string(e) }

// w13g6NewStore 在 env.db（env 为 nil 时新建内存库）上构造 Store。
func w13g6NewStore(t *testing.T, env *testEnv, exchanger TokenExchanger, opts ...Option) *Store {
	t.Helper()
	var db *sql.DB
	if env == nil {
		fresh, err := sql.Open("sqlite", "file:w13g6-sso-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = fresh.Close() })
		db = fresh
	} else {
		db = env.db
	}
	if exchanger == nil {
		exchanger = ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
			return TokenHTTPResponse{}, w13g6StaticError("w13g6 exchanger not configured")
		})
	}
	accountStore, err := accounts.NewStore(db, false, testSecret, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 默认关闭 requiresProxy 拦截（夹具账户不带代理）；调用方显式传入的
	// WithProxyRequired 排在后面，可覆盖默认值。
	opts = append([]Option{WithProxyRequired(false)}, opts...)
	store, err := NewStore(db, false, testSecret, accountStore, exchanger, nil, nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// staticExchanger always answers 200 with the given body.
func staticExchanger(body string) TokenExchanger {
	return ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: 200, Body: body}, nil
	})
}

// statusExchanger always answers with the given status and body.
func statusExchanger(status int, body string) TokenExchanger {
	return ExchangerFunc(func(context.Context, TokenHTTPRequest) (TokenHTTPResponse, error) {
		return TokenHTTPResponse{StatusCode: status, Body: body}, nil
	})
}

// ---------------------------------------------------------------------------
// 四家 OAuth 兑换臂
// ---------------------------------------------------------------------------

func TestW13g6ExchangeArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// --- anthropic：URL 回调缺 state 在 extract 即报错；裸 code 走通兑换 -----
	plain := w13g6NewStore(t, env, staticExchanger(`{"access_token":"at","expires_in":100,"refresh_token":"rt"}`))
	plain.sessions.set(anthropicSessionNamespace, "w13g6-anthropic", anthropicOAuthSession{State: "st"}, time.Minute)
	if _, err := plain.exchangeAnthropicAuthorizationCode(ctx, "w13g6-anthropic", "https://a.example/cb?code=cd&state=", "", ""); err == nil ||
		err.Error() != "Anthropic 授权结果必须包含 code，URL 或查询形式还必须包含 state" {
		t.Fatalf("anthropic missing state: %v", err)
	}
	info, err := plain.exchangeAnthropicAuthorizationCode(ctx, "w13g6-anthropic", "cd", "", "")
	if err != nil || info == nil || info.AccessToken != "at" {
		t.Fatalf("anthropic bare-code exchange: %+v %v", info, err)
	}
	textBody := w13g6NewStore(t, env, statusExchanger(500, "plain-upstream-body"))
	textBody.sessions.set(anthropicSessionNamespace, "w13g6-anthropic", anthropicOAuthSession{State: "st"}, time.Minute)
	if _, err := textBody.exchangeAnthropicAuthorizationCode(ctx, "w13g6-anthropic", "cd", "", ""); err == nil ||
		!strings.Contains(err.Error(), "plain-upstream-body") {
		t.Fatalf("anthropic plain detail: %v", err)
	}

	// --- 篡改会话使 compareDelete 失败（四家） ------------------------------
	var storeRef *Store
	mutatingEx := ExchangerFunc(func(_ context.Context, _ TokenHTTPRequest) (TokenHTTPResponse, error) {
		store := storeRef
		if store != nil {
			store.sessions.set(anthropicSessionNamespace, "w13g6-a2", "w13g6-tampered", time.Minute)
			store.sessions.set(openAIOAuthSessionNamespace, "w13g6-o2", "w13g6-tampered", time.Minute)
			store.sessions.set(grokSessionNamespace, "w13g6-g2", "w13g6-tampered", time.Minute)
			store.sessions.set(geminiSessionNamespace, "w13g6-gm2", "w13g6-tampered", time.Minute)
		}
		return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","expires_in":100}`}, nil
	})
	mutating := w13g6NewStore(t, env, mutatingEx)
	storeRef = mutating

	mutating.sessions.set(anthropicSessionNamespace, "w13g6-a2", anthropicOAuthSession{State: "st"}, time.Minute)
	if _, err := mutating.exchangeAnthropicAuthorizationCode(ctx, "w13g6-a2", "cd", "", ""); err == nil ||
		err.Error() != "Anthropic OAuth 会话已消费，请重新发起授权" {
		t.Fatalf("anthropic consumed: %v", err)
	}
	mutating.sessions.set(openAIOAuthSessionNamespace, "w13g6-o2", openAIOAuthSession{State: "st"}, time.Minute)
	if _, err := mutating.exchangeOpenAIAuthorizationCode(ctx, "w13g6-o2", "https://o.example/?code=c&state=st", "", ""); err == nil ||
		err.Error() != "OAuth 会话已消费，请重新发起授权" {
		t.Fatalf("openai consumed: %v", err)
	}
	mutating.sessions.set(grokSessionNamespace, "w13g6-g2", grokOAuthSession{State: "st"}, time.Minute)
	if _, err := mutating.exchangeGrokAuthorizationCode(ctx, "w13g6-g2", "cd", "", ""); err == nil ||
		!strings.Contains(err.Error(), "会话已消费") {
		t.Fatalf("grok consumed: %v", err)
	}
	mutating.sessions.set(geminiSessionNamespace, "w13g6-gm2", geminiOAuthSession{State: "st"}, time.Minute)
	if _, err := mutating.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w13g6-gm2", CallbackURL: "https://g.example/?code=c&state=st",
	}, ""); err == nil || err.Error() != "Gemini OAuth 会话已消费，请重新发起授权" {
		t.Fatalf("gemini consumed: %v", err)
	}

	// --- grok：空授权码（空回调解析为零值 authorization）/ 会话损坏 / 上游纯文本 ---
	if _, err := plain.exchangeGrokAuthorizationCode(ctx, "w13g6-none", "", "", ""); err == nil ||
		err.Error() != "Grok OAuth 授权码不能为空" {
		t.Fatalf("grok empty code: %v", err)
	}
	plain.sessions.set(grokSessionNamespace, "w13g6-g3", "just-a-string", time.Minute)
	if _, err := plain.exchangeGrokAuthorizationCode(ctx, "w13g6-g3", "cd", "", ""); err == nil ||
		!strings.Contains(err.Error(), "会话不存在或已过期") {
		t.Fatalf("grok corrupt session: %v", err)
	}
	grokText := w13g6NewStore(t, env, statusExchanger(500, "grok-plain-body"))
	grokText.sessions.set(grokSessionNamespace, "w13g6-g5", grokOAuthSession{State: "st"}, time.Minute)
	if _, err := grokText.exchangeGrokAuthorizationCode(ctx, "w13g6-g5", "cd", "", ""); err == nil ||
		!strings.Contains(err.Error(), "grok-plain-body") {
		t.Fatalf("grok plain detail: %v", err)
	}

	// --- gemini：回调 error 组合 / 上游 detail 组合 / expires_in 收敛 ---
	// URL 回调带 description 时以 description 为 detail；仅 error 时以 code 兜底（299）。
	geminiCallback := w13g6NewStore(t, env, staticExchanger("unused"))
	if _, err := geminiCallback.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w13g6-gm3", CallbackURL: "https://g.example/?error=e1&error_description=d1",
	}, ""); err == nil || !strings.Contains(err.Error(), "d1") {
		t.Fatalf("gemini callback description detail: %v", err)
	}
	if _, err := geminiCallback.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w13g6-gm3", CallbackURL: "https://g.example/?error=e1",
	}, ""); err == nil || !strings.Contains(err.Error(), "e1") {
		t.Fatalf("gemini callback code fallback detail: %v", err)
	}
	geminiBoth := w13g6NewStore(t, env, statusExchanger(500, `{"error":"e1","error_description":"d1"}`))
	geminiBoth.sessions.set(geminiSessionNamespace, "w13g6-gm4", geminiOAuthSession{State: "st"}, time.Minute)
	if _, err := geminiBoth.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w13g6-gm4", CallbackURL: "https://g.example/?code=c&state=st",
	}, ""); err == nil || !strings.Contains(err.Error(), "e1: d1") {
		t.Fatalf("gemini error+description detail: %v", err)
	}
	geminiRaw := w13g6NewStore(t, env, statusExchanger(500, "gemini-raw-body"))
	geminiRaw.sessions.set(geminiSessionNamespace, "w13g6-gm5", geminiOAuthSession{State: "st"}, time.Minute)
	if _, err := geminiRaw.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w13g6-gm5", CallbackURL: "https://g.example/?code=c&state=st",
	}, ""); err == nil || !strings.Contains(err.Error(), "gemini-raw-body") {
		t.Fatalf("gemini raw body detail: %v", err)
	}
	geminiClamp := w13g6NewStore(t, env, staticExchanger(`{"access_token":"at","expires_in":5}`))
	geminiClamp.sessions.set(geminiSessionNamespace, "w13g6-gm6", geminiOAuthSession{State: "st"}, time.Minute)
	clamped, err := geminiClamp.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w13g6-gm6", CallbackURL: "https://g.example/?code=c&state=st",
	}, "")
	if err != nil || clamped == nil || clamped.ExpiresIn != 5 {
		t.Fatalf("gemini clamp exchange: %+v %v", clamped, err)
	}

	// --- openai：client_id 兜底 --------------------------------------------
	openaiForm := w13g6NewStore(t, env, staticExchanger(`{"access_token":"at","expires_in":100,"id_token":"a.b.c"}`))
	openaiInfo, err := openaiForm.requestOpenAIToken(ctx, map[string]string{"grant_type": "refresh_token", "refresh_token": "r"}, "")
	if err != nil || openaiInfo == nil || openaiInfo.ClientID != OpenAIOAuthClientID {
		t.Fatalf("openai default client id: %+v %v", openaiInfo, err)
	}
	if _, err := openaiForm.refreshOpenAIToken(ctx, "rt", " ", ""); err != nil {
		t.Fatalf("openai refresh with blank client id: %v", err)
	}

	// --- 上游传输错误臂（四家 request*Token 的 exchange err） ----------------
	failing := w13g6NewStore(t, env, nil)
	failing.sessions.set(anthropicSessionNamespace, "w13g6-fa", anthropicOAuthSession{State: "st"}, time.Minute)
	if _, err := failing.exchangeAnthropicAuthorizationCode(ctx, "w13g6-fa", "cd", "", ""); err == nil {
		t.Fatal("anthropic transport error arm")
	}
	failing.sessions.set(openAIOAuthSessionNamespace, "w13g6-fo", openAIOAuthSession{State: "st"}, time.Minute)
	if _, err := failing.exchangeOpenAIAuthorizationCode(ctx, "w13g6-fo", "https://o.example/?code=c&state=st", "", ""); err == nil {
		t.Fatal("openai transport error arm")
	}
	failing.sessions.set(grokSessionNamespace, "w13g6-fg", grokOAuthSession{State: "st"}, time.Minute)
	if _, err := failing.exchangeGrokAuthorizationCode(ctx, "w13g6-fg", "cd", "", ""); err == nil {
		t.Fatal("grok transport error arm")
	}
	failing.sessions.set(geminiSessionNamespace, "w13g6-fgm", geminiOAuthSession{State: "st"}, time.Minute)
	if _, err := failing.exchangeGeminiAuthorizationCode(ctx, geminiExchangeOptions{
		SessionID: "w13g6-fgm", CallbackURL: "https://g.example/?code=c&state=st",
	}, ""); err == nil {
		t.Fatal("gemini transport error arm")
	}
}

// ---------------------------------------------------------------------------
// Store 层错误臂
// ---------------------------------------------------------------------------

// w13g6SeedOAuthAccount 插入一个最小 OAuth 账户行。
func w13g6SeedOAuthAccount(t *testing.T, env *testEnv, id, owner, provider, profile, protocol, version, accountType, creds string, revision int) {
	t.Helper()
	const now = "2026-01-01T00:00:00.000Z"
	if _, err := env.db.Exec(`INSERT INTO accounts (id, config_revision, system_account_id, provider_code,
		provider_protocol_profile_id, protocol_code, protocol_version, name, type, status,
		credentials_encrypted, credential_mask, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, '', ?, ?)`,
		id, revision, owner, provider, profile, protocol, version, "w13g6-"+id, accountType, creds, now, now); err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

func TestW13g6StoreArms(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	owner := env.login(t, "root", "root-pass", "super_admin")
	store := env.store

	// 必选档案不匹配 -> ValidationError（store.go 307-309）。
	if _, err := store.resolveProviderProfile(ctx, "gpt", "profile_gpt_openai_v1", "oauth", "profile_other"); err == nil ||
		!strings.Contains(err.Error(), "不支持") {
		t.Fatalf("profile mismatch: %v", err)
	}
	// 账户缺失 -> (nil, nil)（628-630）。
	account, err := store.findRotationAccount(ctx, "w13g6-missing", AccessScope{})
	if err != nil || account != nil {
		t.Fatalf("missing rotation account: %v %v", account, err)
	}
	// 凭据解密失败 -> CredentialsUnavailableError（643-645）。
	w13g6SeedOAuthAccount(t, env, "w13g6-garbage", owner, "gpt", "profile_gpt_openai_v1", "openai", "v1", "oauth", "garbage", 1)
	if _, err := store.findRotationAccount(ctx, "w13g6-garbage", AccessScope{}); err == nil {
		t.Fatal("garbage credentials must fail to decrypt")
	}
	// credentials==nil 与 configRevision<1 的兜底臂（547-552）。
	sealed, err := encryptJSON(testSecret, map[string]any{"refresh_token": "rt", "client_id": "cid"})
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}
	w13g6SeedOAuthAccount(t, env, "w13g6-rev0", owner, "gpt", "profile_gpt_openai_v1", "openai", "v1", "oauth", sealed, 0)
	if _, err := store.RotateCredentials(ctx, RotateCredentialsInput{
		AccountID:                         "w13g6-rev0",
		ExpectedConfigRevision:            1,
		ExpectedProviderCode:              "gpt",
		ExpectedAccountType:               "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		Credentials:                       nil,
		Access:                            AccessScope{},
	}); err == nil {
		t.Fatal("revision-0 rotation must fail (rotation guard)")
	}
	// closed DB：findRotationAccount / RotateCredentials 错误臂（631/653/680）。
	closed, err := sql.Open("sqlite", "file:w13g6-closed-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	closedStore := w13g6NewStore(t, nil, nil)
	closedStore.db = closed
	if _, err := closedStore.findRotationAccount(ctx, "w13g6-x", AccessScope{}); err == nil {
		t.Fatal("closed findRotationAccount must fail")
	}
	if _, err := closedStore.RotateCredentials(ctx, RotateCredentialsInput{AccountID: "w13g6-x"}); err == nil {
		t.Fatal("closed RotateCredentials must fail")
	}
}

// ---------------------------------------------------------------------------
// 路由层错误臂
// ---------------------------------------------------------------------------

// w13g6Do 复用 env 的会话 Cookie 向指定 server 发请求。
func w13g6Do(t *testing.T, env *testEnv, server *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	env.mu.Lock()
	for name, value := range env.jar {
		request.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	env.mu.Unlock()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	return response.StatusCode, payload
}

func TestW13g6RouteBodyArms(t *testing.T) {
	env := newTestEnv(t)
	owner := env.login(t, "root", "root-pass", "super_admin")

	// auth-url 空 JSON 体 -> body==nil 归一（513-515）。
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/openai-oauth/auth-url", `null`); code != http.StatusOK {
		t.Fatalf("auth-url null body: %d", code)
	}
	// create-from-code 空体 -> body==nil 归一后 strict 失败（562-567）。
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-code", ``); code != http.StatusBadRequest {
		t.Fatalf("create empty body: %d", code)
	}
	// refresh 空 JSON 体（674-676）。
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w13g6-x/refresh-token", `null`); code != http.StatusBadRequest {
		t.Fatalf("refresh null body: %d", code)
	}
	// reauthorize-from-code：空 systemAccountId 查询（738-741）。
	if code, payload := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w13g6-x/reauthorize-from-code?systemAccountId=", `{"expectedConfigRevision":1}`); code != http.StatusBadRequest || payload["message"] != "系统账号 ID 不能为空" {
		t.Fatalf("reauth blank scope: %d %v", code, payload)
	}
	// reauthorize-from-code：body==nil（748-750）与 strict 失败（751-754）。
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w13g6-x/reauthorize-from-code", `null`); code != http.StatusBadRequest {
		t.Fatalf("reauth null body: %d", code)
	}
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w13g6-x/reauthorize-from-code", `{"unexpected":1,"expectedConfigRevision":1}`); code != http.StatusBadRequest {
		t.Fatalf("reauth strict body: %d", code)
	}
	// reauthorize-from-refresh-token：空 scope（802-805）与 body==nil（812-814）。
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/w13g6-x/reauthorize-from-refresh-token?systemAccountId=", `{"expectedConfigRevision":1}`); code != http.StatusBadRequest {
		t.Fatalf("reauth-refresh blank scope: %d", code)
	}
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/w13g6-x/reauthorize-from-refresh-token", `null`); code != http.StatusBadRequest {
		t.Fatalf("reauth-refresh null body: %d", code)
	}
	// credentialsPatch 分发到各 plan 闭包（plans.go 15/106/191/363 + providers.go 75）。
	for slug, patch := range map[string]string{
		"openai":    `{"sessionId":"s","callbackUrl":"c","credentialsPatch":{"quota_recovery_policy":{"x":1}}}`,
		"anthropic": `{"sessionId":"s","callbackUrl":"c","credentialsPatch":{"base_url":"https://a.b"}}`,
		"gemini":    `{"sessionId":"s","callbackUrl":"c","credentialsPatch":{"base_url":"https://g.c"}}`,
	} {
		code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/"+slug+"-oauth/create-from-code", patch)
		if code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Fatalf("%s create with patch: %d", slug, code)
		}
	}
	if code, _ := w13g6Do(t, env, env.server, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"r","credentialsPatch":{"quota_recovery_policy":{"x":1}}}`); code != http.StatusBadRequest {
		t.Fatalf("grok create-refresh with patch: %d", code)
	}

	// --- 自定义 Store 服务器：上游脚本 + 失败 advancer ----------------------
	scripted := ExchangerFunc(func(_ context.Context, request TokenHTTPRequest) (TokenHTTPResponse, error) {
		switch {
		case strings.Contains(request.URL, "openai"):
			return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","expires_in":100,"id_token":"a.b.c","refresh_token":"r2"}`}, nil
		case strings.Contains(request.URL, "googleapis"), strings.Contains(request.URL, "gemini"):
			return TokenHTTPResponse{StatusCode: 200, Body: `{"access_token":"at","expires_in":1000}`}, nil
		default:
			return TokenHTTPResponse{StatusCode: 500, Body: "boom"}, nil
		}
	})
	failingAdvancer := failingDispatchAdvancer{failAccountID: "w13g6-gpt-acc"}
	custom := w13g6NewStore(t, env, scripted, WithDispatchRevisionAdvancer(&failingAdvancer))
	sealed, err := encryptJSON(testSecret, map[string]any{"refresh_token": "rt", "client_id": "cid"})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	w13g6SeedOAuthAccount(t, env, "w13g6-gpt-acc", owner, "gpt", "profile_gpt_openai_v1", "openai", "v1", "oauth", sealed, 1)
	w13g6SeedOAuthAccount(t, env, "w13g6-xai-acc", owner, "xai", "profile_xai_openai_v1", "openai", "v1", "oauth", sealed, 1)
	w13g6SeedOAuthAccount(t, env, "w13g6-gem-acc", owner, "gemini", "profile_gemini_native_v1beta", "gemini", "v1beta", "google_oauth", sealed, 1)

	k := kernel.New(kernel.Options{CompressionDisabled: true})
	(&Deps{Store: custom, Auth: env.deps, Sink: env.sink}).Mount(k)
	server := httptest.NewServer(k.Handler())
	t.Cleanup(server.Close)

	// refresh：RotateCredentials 内 advancer 失败 -> writeOAuthError（721-724）。
	if code, _ := w13g6Do(t, env, server, http.MethodPost, "/__aisys__/api/openai-oauth/accounts/w13g6-gpt-acc/refresh-token",
		`{"expectedConfigRevision":1}`); code != http.StatusInternalServerError && code != http.StatusBadGateway {
		t.Fatalf("refresh advancer error: %d", code)
	}
	// 存储层同臂（693-695）。
	if _, err := custom.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:                         "w13g6-gpt-acc",
		ExpectedConfigRevision:            1,
		ExpectedProviderCode:              "gpt",
		ExpectedAccountType:               "oauth",
		ExpectedProviderProtocolProfileID: "profile_gpt_openai_v1",
		Credentials:                       map[string]any{"refresh_token": "r2"},
		Access:                            AccessScope{},
	}); err == nil {
		t.Fatal("advancer error must propagate")
	}
	// grok refreshStored 上游失败（plans.go 431-433）。
	if code, _ := w13g6Do(t, env, server, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/w13g6-xai-acc/refresh-token",
		`{"expectedConfigRevision":1}`); code != http.StatusBadGateway && code != http.StatusInternalServerError {
		t.Fatalf("grok refreshStored error: %d", code)
	}
	// grok reauthorize-from-refresh：refreshInput 上游失败（851-854）。
	if code, _ := w13g6Do(t, env, server, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/w13g6-xai-acc/reauthorize-from-refresh-token",
		`{"refreshToken":"r","expectedConfigRevision":1}`); code != http.StatusBadGateway && code != http.StatusInternalServerError {
		t.Fatalf("grok reauth-refresh error: %d", code)
	}
	// gemini reauthorize-from-refresh：refreshInput 的 body 优先拾取（327-329），全链成功。
	if code, payload := w13g6Do(t, env, server, http.MethodPost, "/__aisys__/api/gemini-oauth/accounts/w13g6-gem-acc/reauthorize-from-refresh-token",
		`{"refreshToken":"r","clientId":"c","clientSecret":"cs","expectedConfigRevision":1}`); code != http.StatusOK {
		t.Fatalf("gemini reauth-refresh: %d %v", code, payload)
	}
	// gemini create-from-refresh：无 safePatch 的 tokenOutcome（plans.go 252-254）。
	if code, payload := w13g6Do(t, env, server, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"r","clientId":"c"}`); code != http.StatusOK && code != http.StatusBadRequest {
		t.Fatalf("gemini create-from-refresh: %d %v", code, payload)
	}
}

// failingDispatchAdvancer only fails the revision advance for one account,
// so sibling rotation flows stay reachable on the same store.
type failingDispatchAdvancer struct {
	failAccountID string
}

func (f *failingDispatchAdvancer) AdvanceDispatchRevisionFamily(_ context.Context, _ *sql.Tx, accountID, _ string, _ int64) error {
	if accountID == f.failAccountID {
		return w13g6StaticError("w13g6 advancer failure")
	}
	return nil
}
