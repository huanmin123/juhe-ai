package oauthmgmt

// T65-②：Gemini Code Assist 自动探测与 onboarding 的建号链行为测试。
//
// 触发面契约：gemini google_oauth 建号两条路由（create-from-code /
// create-from-refresh-token，经 handleCreate → enrichOutcome 钩子）在
// oauth_type ∈ {code_assist, google_one} 且凭据未携带 projectId 时，换发成功
// 后、落库前执行一次 loadCodeAssist → onboardUser；ai_studio、显式 projectId、
// reauthorize 与 rotation 链全程零探测调用。请求面契约：探测经账户绑定代理
// 出站、携带既有 cloudcode-pa 出站同一口径的 Gemini CLI user-agent
// （upstreamidentity.EffectiveGeminiCLIUserAgent）、JSON 头与 metadata 体。
//
// 测试面说明：探测出站走注入的 mockExchanger（httptest 同款桩边界，不出网），
// 落库断言经 testEnv.accountCredentials 解密回读；PG 夹具见 pgkit_test.go。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamidentity"
)

const (
	t65GeminiTokenPayload = `{"access_token":"gem-probe-access","refresh_token":"gem-probe-refresh","expires_in":3600,"token_type":"Bearer"}`
	// t65LoadProject 命中臂：loadCodeAssist 直接返回 currentTier +
	// cloudaicompanionProject（参考实现同字段名）。
	t65LoadProject = `{"currentTier":{"id":"standard-tier"},"cloudaicompanionProject":"gem-proj-detect"}`
)

// t65ProbeCalls 按 v1internal 动作统计探测调用次数。
func t65ProbeCalls(calls []exchangeCall, action string) []exchangeCall {
	matched := []exchangeCall{}
	for _, call := range calls {
		if strings.HasSuffix(call.URL, "/v1internal:"+action) {
			matched = append(matched, call)
		}
	}
	return matched
}

// t65GeminiProbeScript 组一个按 URL 分发的响应脚本：token 端点回固定令牌，
// loadCodeAssist/onboardUser 各回给定载荷，其余 URL 记为意外调用（500，让外层
// 状态断言失败）。
func t65GeminiProbeScript(t *testing.T, loadPayload func() (int, string), onboardPayload func() (int, string)) func(int, exchangeCall) (int, string) {
	return func(_ int, call exchangeCall) (int, string) {
		switch {
		case call.URL == GeminiOAuthTokenURL:
			return http.StatusOK, t65GeminiTokenPayload
		case strings.HasSuffix(call.URL, "/v1internal:loadCodeAssist"):
			status, body := loadPayload()
			return status, body
		case strings.HasSuffix(call.URL, "/v1internal:onboardUser"):
			if onboardPayload == nil {
				t.Fatalf("unexpected onboardUser call: %s", call.URL)
			}
			status, body := onboardPayload()
			return status, body
		default:
			t.Fatalf("unexpected upstream call: %s", call.URL)
			return http.StatusInternalServerError, `{}`
		}
	}
}

// t65AssertProbeShape 断言一次 cloudcode-pa v1internal 调用的请求面契约：
// Bearer 新 access token、JSON 头、既有 cloudcode-pa 出站同一口径的 Gemini CLI
// user-agent、CLI 仿真 metadata 体、经账户绑定代理出站（wantProxy 非空时比对）。
func t65AssertProbeShape(t *testing.T, call exchangeCall, action string, wantMetadata bool, wantProxy string) {
	t.Helper()
	if call.Headers["authorization"] != "Bearer gem-probe-access" {
		t.Fatalf("%s authorization: %v", action, call.Headers)
	}
	if call.Headers["content-type"] != "application/json" {
		t.Fatalf("%s content-type: %v", action, call.Headers)
	}
	if wantUA := upstreamidentity.EffectiveGeminiCLIUserAgent(); call.Headers["user-agent"] != wantUA {
		t.Fatalf("%s user-agent: got %q, want %q（既有 cloudcode-pa 出站同一口径）", action, call.Headers["user-agent"], wantUA)
	}
	if wantProxy != "" && call.ProxyURL != wantProxy {
		t.Fatalf("%s proxyURL: got %q, want %q（账户绑定代理出站）", action, call.ProxyURL, wantProxy)
	}
	if !wantMetadata {
		return
	}
	var payload struct {
		TierID   string `json:"tierId"`
		Metadata struct {
			IdeType    string `json:"ideType"`
			Platform   string `json:"platform"`
			PluginType string `json:"pluginType"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(call.Body), &payload); err != nil {
		t.Fatalf("%s body decode: %v (%s)", action, err, call.Body)
	}
	if payload.Metadata.IdeType != "ANTIGRAVITY" || payload.Metadata.Platform != "PLATFORM_UNSPECIFIED" ||
		payload.Metadata.PluginType != "GEMINI" {
		t.Fatalf("%s metadata: %s", action, call.Body)
	}
}

// TestT65GeminiCreateFromCodeDetectsProject：code_assist 建号、用户未填
// project，loadCodeAssist 命中 → 建号 201、落库凭据含探测 project 与归一
// tier、loadCodeAssist 恰调 1 次、onboardUser 零调用；请求头与代理经由断言。
func TestT65GeminiCreateFromCodeDetectsProject(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	insertProxyProfile(t, env, "proxy-probe", "http", "203.0.113.10", 38465, "", "", true)
	proxyURL, err := env.store.proxyProfileRequestURL(context.Background(), "proxy-probe")
	if err != nil {
		t.Fatal(err)
	}
	env.exchanger.respond = t65GeminiProbeScript(t, func() (int, string) {
		return http.StatusOK, t65LoadProject
	}, nil)

	code, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url", `{"oauthType":"code_assist"}`)
	if code != http.StatusOK {
		t.Fatalf("auth-url: %d %v", code, authPayload)
	}
	authData := dataMap(t, authPayload)
	sessionID := authData["sessionId"].(string)
	state := authData["state"].(string)

	createBody := fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"https://codeassist.google.com/authcode?code=gem-code&state=%s","proxyProfileId":"proxy-probe"}`,
		sessionID, url.QueryEscape(state))
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-code", createBody)
	if code != http.StatusCreated {
		t.Fatalf("create-from-code: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)

	credentials := env.accountCredentials(t, accountID)
	if credentials["project_id"] != "gem-proj-detect" {
		t.Fatalf("project_id: %v", credentials["project_id"])
	}
	// "standard-tier" 经 canonicalGeminiTierID 归一为 gcp_standard。
	if credentials["tier_id"] != "gcp_standard" {
		t.Fatalf("tier_id: %v", credentials["tier_id"])
	}
	if owner := env.count(t, `SELECT COUNT(*) FROM accounts WHERE id = ? AND system_account_id = ? AND vendor_code = 'gemini' AND type = 'google_oauth'`, accountID, adminID); owner != 1 {
		t.Fatalf("account row: %d", owner)
	}

	calls := env.exchanger.recorded()
	if got := t65ProbeCalls(calls, "loadCodeAssist"); len(got) != 1 {
		t.Fatalf("loadCodeAssist calls: %d", len(got))
	}
	if got := t65ProbeCalls(calls, "onboardUser"); len(got) != 0 {
		t.Fatalf("onboardUser calls: %d", len(got))
	}
	t65AssertProbeShape(t, t65ProbeCalls(calls, "loadCodeAssist")[0], "loadCodeAssist", true, proxyURL)
}

// TestT65GeminiCreateOnboardsNewAccount：新账号无 project（loadCodeAssist 无
// cloudaicompanionProject）→ onboardUser 被调且带归一可用 tierId → 回填
// project 与档位落库。
func TestT65GeminiCreateOnboardsNewAccount(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	var onboardBody string
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		switch {
		case call.URL == GeminiOAuthTokenURL:
			return http.StatusOK, t65GeminiTokenPayload
		case strings.HasSuffix(call.URL, "/v1internal:loadCodeAssist"):
			// 新账号：无 currentTier/cloudaicompanionProject，仅允许档位
			// （free-tier 不可归一、standard-tier 可归一 → tierId 取后者）。
			return http.StatusOK, `{"allowedTiers":[{"id":"free-tier","isDefault":true},{"id":"standard-tier","isDefault":false}]}`
		case strings.HasSuffix(call.URL, "/v1internal:onboardUser"):
			onboardBody = call.Body
			return http.StatusOK, `{"done":true,"response":{"cloudaicompanionProject":{"id":"gem-proj-onboarded","name":"gem-proj-onboarded"}}}`
		default:
			t.Fatalf("unexpected upstream call: %s", call.URL)
			return http.StatusInternalServerError, `{}`
		}
	}

	// 空 tierId 不构成显式回落标记（canonicalGeminiTierID 归一为空）。
	createBody := `{"refreshToken":"gem-refresh-raw","oauthType":"code_assist","tierId":""}`
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token", createBody)
	if code != http.StatusCreated {
		t.Fatalf("create-from-refresh-token: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)

	if onboardBody == "" {
		t.Fatal("onboardUser must be called for a project-less new account")
	}
	var payload struct {
		TierID string `json:"tierId"`
	}
	if err := json.Unmarshal([]byte(onboardBody), &payload); err != nil {
		t.Fatalf("onboardUser body: %v (%s)", err, onboardBody)
	}
	if payload.TierID != "standard-tier" {
		t.Fatalf("onboardUser tierId: got %q, want standard-tier（allowedTiers 中 canonicalGeminiTierID 可归一档位）", payload.TierID)
	}

	credentials := env.accountCredentials(t, accountID)
	if credentials["project_id"] != "gem-proj-onboarded" || credentials["tier_id"] != "gcp_standard" {
		t.Fatalf("credentials: project_id=%v tier_id=%v", credentials["project_id"], credentials["tier_id"])
	}
	calls := env.exchanger.recorded()
	if got := t65ProbeCalls(calls, "loadCodeAssist"); len(got) != 1 {
		t.Fatalf("loadCodeAssist calls: %d", len(got))
	}
	if got := t65ProbeCalls(calls, "onboardUser"); len(got) != 1 {
		t.Fatalf("onboardUser calls: %d", len(got))
	}
}

// TestT65GeminiCreateOnboardingFailureFallbacks：探测失败三分支——
//   - 无任何回落（未提交 project/tier）→ 400，文案含「未能自动开通」并带原因；
//   - 显式提交 tierId → 按现状继续建号（201，档位保留，无 project）；
//   - 显式提交 projectId → 全程零探测调用，现状不变。
func TestT65GeminiCreateOnboardingFailureFallbacks(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL == GeminiOAuthTokenURL {
			return http.StatusOK, t65GeminiTokenPayload
		}
		// loadCodeAssist/onboardUser 一律上游不可用。
		return http.StatusServiceUnavailable, `{"error":"backend"}`
	}

	// 分支 1：无回落 → 400 + 行动文案（含原因），不产出无 project 账户。
	code, failed := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gem-refresh-raw","oauthType":"code_assist"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("no-fallback create: %d %v", code, failed)
	}
	message, _ := failed["message"].(string)
	if !strings.Contains(message, "未能自动开通 Code Assist 项目") || !strings.Contains(message, "请手填 projectId") {
		t.Fatalf("no-fallback message: %q", message)
	}
	if !strings.Contains(message, "HTTP 503") {
		t.Fatalf("no-fallback message must carry the upstream reason: %q", message)
	}
	if rows := env.count(t, `SELECT COUNT(*) FROM accounts WHERE vendor_code = 'gemini'`); rows != 0 {
		t.Fatalf("no account must be created on onboarding failure: %d", rows)
	}

	// 分支 2：显式 tierId → 按现状继续建号（201），档位保留、无 project。
	code, fallback := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gem-refresh-raw","oauthType":"code_assist","tierId":"pro"}`)
	if code != http.StatusCreated {
		t.Fatalf("explicit-tier fallback create: %d %v", code, fallback)
	}
	fallbackCredentials := env.accountCredentials(t, dataMap(t, fallback)["id"].(string))
	if fallbackCredentials["tier_id"] != "gcp_standard" {
		t.Fatalf("explicit-tier fallback tier_id: %v", fallbackCredentials["tier_id"])
	}
	if _, present := fallbackCredentials["project_id"]; present {
		t.Fatalf("explicit-tier fallback must not carry a project_id: %v", fallbackCredentials["project_id"])
	}

	// 分支 3：显式 projectId → 全程零探测调用（基线截断前两分支的 503 探测）。
	probeBaseline := len(env.exchanger.recorded())
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != GeminiOAuthTokenURL {
			t.Fatalf("explicit projectId must skip probing, got: %s", call.URL)
		}
		return http.StatusOK, t65GeminiTokenPayload
	}
	code, explicit := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gem-refresh-raw","oauthType":"code_assist","projectId":"user-proj-1"}`)
	if code != http.StatusCreated {
		t.Fatalf("explicit project create: %d %v", code, explicit)
	}
	explicitCredentials := env.accountCredentials(t, dataMap(t, explicit)["id"].(string))
	if explicitCredentials["project_id"] != "user-proj-1" {
		t.Fatalf("explicit project_id: %v", explicitCredentials["project_id"])
	}
	for _, call := range env.exchanger.recorded()[probeBaseline:] {
		if strings.Contains(call.URL, "/v1internal:") {
			t.Fatalf("explicit projectId must skip probing, got: %s", call.URL)
		}
	}
}

// TestT65GeminiAIStudioSkipsProbing：ai_studio 建号全程零探测调用（静态档位
// 兜底不变）。
func TestT65GeminiAIStudioSkipsProbing(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != GeminiOAuthTokenURL {
			t.Fatalf("ai_studio must skip probing, got: %s", call.URL)
		}
		return http.StatusOK, t65GeminiTokenPayload
	}

	code, created := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gem-refresh-raw","oauthType":"ai_studio","clientId":"studio-client","clientSecret":"studio-secret"}`)
	if code != http.StatusCreated {
		t.Fatalf("ai_studio create: %d %v", code, created)
	}
	credentials := env.accountCredentials(t, dataMap(t, created)["id"].(string))
	if credentials["tier_id"] != "aistudio_free" {
		t.Fatalf("ai_studio tier_id: %v", credentials["tier_id"])
	}
	calls := env.exchanger.recorded()
	if got := t65ProbeCalls(calls, "loadCodeAssist"); len(got) != 0 {
		t.Fatalf("ai_studio loadCodeAssist calls: %d", len(got))
	}
	if got := t65ProbeCalls(calls, "onboardUser"); len(got) != 0 {
		t.Fatalf("ai_studio onboardUser calls: %d", len(got))
	}
}

// TestT65GeminiReauthorizeStaysProbeFree：reauthorize-from-code 不经建号链
// enrichment 钩子——存量账户（含 project）重授权全程零探测调用，行为保持
// T65-② 之前现状。
func TestT65GeminiReauthorizeStaysProbeFree(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	env.exchanger.respond = func(_ int, call exchangeCall) (int, string) {
		if call.URL != GeminiOAuthTokenURL {
			t.Fatalf("reauthorize flow must not probe, got: %s", call.URL)
		}
		return http.StatusOK, t65GeminiTokenPayload
	}

	// 建号（显式 projectId，零探测）→ 账户行。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/create-from-refresh-token",
		`{"refreshToken":"gem-refresh-raw","oauthType":"code_assist","projectId":"reauth-proj"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %v", code, created)
	}
	accountID := dataMap(t, created)["id"].(string)
	var revision int64
	if err := env.db.QueryRow(fixtureBind(`SELECT config_revision FROM accounts WHERE id = ?`), accountID).Scan(&revision); err != nil {
		t.Fatal(err)
	}

	// 新会话重授权：无 projectId 会话（会话盲）也不触发探测。
	code, authPayload := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/auth-url", `{"oauthType":"code_assist"}`)
	if code != http.StatusOK {
		t.Fatalf("auth-url: %d %v", code, authPayload)
	}
	authData := dataMap(t, authPayload)
	reauthBody := fmt.Sprintf(`{"sessionId":%q,"callbackUrl":"https://codeassist.google.com/authcode?code=gem-code-2&state=%s","expectedConfigRevision":%d}`,
		authData["sessionId"].(string), url.QueryEscape(authData["state"].(string)), revision)
	code, reauthorized := env.do(t, http.MethodPost, "/__aisys__/api/gemini-oauth/accounts/"+accountID+"/reauthorize-from-code", reauthBody)
	if code != http.StatusOK {
		t.Fatalf("reauthorize-from-code: %d %v", code, reauthorized)
	}

	calls := env.exchanger.recorded()
	if got := t65ProbeCalls(calls, "loadCodeAssist"); len(got) != 0 {
		t.Fatalf("reauthorize loadCodeAssist calls: %d", len(got))
	}
	if got := t65ProbeCalls(calls, "onboardUser"); len(got) != 0 {
		t.Fatalf("reauthorize onboardUser calls: %d", len(got))
	}
	// 轮换后凭据保留存量 project_id（mergeRotationCredentials 语义不变）。
	var sealed string
	if err := env.db.QueryRow(fixtureBind(`SELECT credentials_encrypted FROM accounts WHERE id = ?`), accountID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	credentials := map[string]any{}
	if err := decryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	if credentials["project_id"] != "reauth-proj" {
		t.Fatalf("reauthorized project_id: %v", credentials["project_id"])
	}
}

// TestT65GeminiOnboardTierPicker：tierId 挑选规则单测——可归一档位优先
// （isDefault 优先、其次上游顺序），无可用档位回落原始档位，再回落
// paidTier/currentTier 原始值，最后 free-tier 兜底。
func TestT65GeminiOnboardTierPicker(t *testing.T) {
	decode := func(raw string) map[string]any {
		load := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &load); err != nil {
			t.Fatalf("fixture decode: %v", err)
		}
		return load
	}
	cases := []struct {
		name         string
		oauthType    string
		load         string
		fallbackTier string
		want         string
	}{
		{"default canonicalizable tier wins", "code_assist",
			`{"allowedTiers":[{"id":"free-tier","isDefault":true},{"id":"standard-tier","isDefault":false}]}`, "", "standard-tier"},
		{"default canonical tier", "code_assist",
			`{"allowedTiers":[{"id":"standard-tier","isDefault":true}]}`, "", "standard-tier"},
		{"non-canonical allowed tiers fall back to raw default", "google_one",
			`{"allowedTiers":[{"id":"free-tier","isDefault":true}]}`, "", "free-tier"},
		{"no allowed tiers falls back to detected tier raw", "code_assist",
			`{}`, "legacy-tier", "legacy-tier"},
		{"empty response floors to free-tier", "code_assist", ``, "", "free-tier"},
		{"enterprise ultra maps", "code_assist",
			`{"allowedTiers":[{"id":"pro-tier","isDefault":false},{"id":"ultra-tier","isDefault":true}]}`, "", "ultra-tier"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			load := map[string]any{}
			if testCase.load != "" {
				load = decode(testCase.load)
			}
			if got := geminiOnboardUserTierID(testCase.oauthType, load, testCase.fallbackTier); got != testCase.want {
				t.Fatalf("geminiOnboardUserTierID: got %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestT65GeminiProbeFieldForms：loadCodeAssist 响应字段形态兼容——paidTier
// 优先于 currentTier、字符串与 {id} 对象两形态、cloudaicompanionProject 三键
// 兼容（与参考实现 extractCloudaicompanionProject 同构）。
func TestT65GeminiProbeFieldForms(t *testing.T) {
	decode := func(raw string) map[string]any {
		load := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &load); err != nil {
			t.Fatalf("fixture decode: %v", err)
		}
		return load
	}
	if got := geminiLoadTierID(decode(`{"paidTier":{"id":"pro-tier"},"currentTier":"free-tier"}`)); got != "pro-tier" {
		t.Fatalf("paidTier priority: %q", got)
	}
	if got := geminiLoadTierID(decode(`{"currentTier":{"id":"standard-tier"}}`)); got != "standard-tier" {
		t.Fatalf("currentTier object form: %q", got)
	}
	if got := geminiLoadTierID(decode(`{}`)); got != "" {
		t.Fatalf("absent tier: %q", got)
	}
	if got := extractGeminiCompanionProject(decode(`{"cloudaicompanionProject":"proj-string"}`)); got != "proj-string" {
		t.Fatalf("project string form: %q", got)
	}
	if got := extractGeminiCompanionProject(decode(`{"project":{"id":"proj-object"}}`)); got != "proj-object" {
		t.Fatalf("project object form: %q", got)
	}
	if got := extractGeminiCompanionProject(nil); got != "" {
		t.Fatalf("nil response: %q", got)
	}
}
