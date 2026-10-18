package oauthmgmt

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
)

// grok OAuth 请求覆盖字段（service_tier_override / reasoning_effort_override）
// 三条写入链路的端到端断言（T2）：创建（create-from-refresh-token 经
// credentialsPatch → CreateOAuthAccount → accounts 写入面归一化）、轮换
// （RotateCredentials 落库校验）、重授权（HTTP reauthorize-from-refresh-token，
// 落库共用 RotateCredentials）。每条链路覆盖合法值（保留且出站生效）、缺省值
// （不落字段、运行时不改写请求）、非法值（拒绝且不写库）。运行时消费方只读
// 引用 gatewayoauthcodex.ApplyGptAccountRequestOverridesBody（xai 账户运行时
// 只做 token 形状断言，值域由写入面/落库校验保障）。

// grokOverrideRuntimeOutbound 用读回的凭据驱动运行时覆盖应用函数，返回出站
// body（Responses endpoint family + 声明能力的模型目录能力）。
func grokOverrideRuntimeOutbound(t *testing.T, credentials map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"model": "grok-4", "stream": true}
	out, err := gatewayoauthcodex.ApplyGptAccountRequestOverridesBody(body, gatewayoauthcodex.GptAccountOverrideInput{
		Credentials:    credentials,
		VendorCode:     ProviderXAI,
		EndpointFamily: "responses",
		ModelCapabilities: &gatewayoauthcodex.GptRequestOverrideModelCapabilities{
			SupportedServiceTiers:     []string{"priority", "flex"},
			SupportedReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"},
		},
	})
	if err != nil {
		t.Fatalf("运行时覆盖应用失败: %v", err)
	}
	return out
}

func grokOverrideAssertApplied(t *testing.T, credentials map[string]any) {
	t.Helper()
	out := grokOverrideRuntimeOutbound(t, credentials)
	if out["service_tier"] != "priority" {
		t.Fatalf("出站 service_tier 未生效: %v", out["service_tier"])
	}
	reasoning, _ := out["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "max" {
		t.Fatalf("出站 reasoning.effort 未生效: %v", out["reasoning"])
	}
}

func grokOverrideAssertInert(t *testing.T, credentials map[string]any) {
	t.Helper()
	out := grokOverrideRuntimeOutbound(t, credentials)
	if _, ok := out["service_tier"]; ok {
		t.Fatalf("缺省覆盖不应改写 service_tier: %v", out)
	}
	if _, ok := out["reasoning"]; ok {
		t.Fatalf("缺省覆盖不应注入 reasoning: %v", out)
	}
}

func grokOverrideAccountCount(t *testing.T, env *testEnv) int {
	t.Helper()
	count := 0
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestGrokOverrideCreateLink 创建链路：credentialsPatch → CreateOAuthAccount
// → accounts.Create 写入面归一化（token 形状 + 协议族值域）。
func TestGrokOverrideCreateLink(t *testing.T) {
	env := newTestEnv(t)
	env.w14dLoginAdmin(t)
	env.exchanger.respond = staticToken(grokTokenPayload("grok-create-1"))

	// 合法值：patch 携带两个覆盖字段（服务等级带首尾空白，parser trim 后
	// 放行），落库保留，运行时出站生效。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"grok-r0","providerProtocolProfileId":"profile_xai_openai_v1",`+
			`"credentialsPatch":{"service_tier_override":" priority ","reasoning_effort_override":"max"}}`)
	if code != http.StatusCreated {
		t.Fatalf("合法创建: %d %v", code, created)
	}
	appliedID := dataMap(t, created)["id"].(string)
	credentials := env.accountCredentials(t, appliedID)
	if credentials["service_tier_override"] != "priority" || credentials["reasoning_effort_override"] != "max" {
		t.Fatalf("合法覆盖字段未保留: %v", credentials)
	}
	grokOverrideAssertApplied(t, credentials)

	// 缺省值：不携带 credentialsPatch，覆盖字段不落库，运行时不改写请求。
	// （refreshToken 与上一次不同：创建路由的防重指纹含 refreshToken。）
	code, created = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"grok-r1","providerProtocolProfileId":"profile_xai_openai_v1"}`)
	if code != http.StatusCreated {
		t.Fatalf("缺省创建: %d %v", code, created)
	}
	plainID := dataMap(t, created)["id"].(string)
	credentials = env.accountCredentials(t, plainID)
	if _, ok := credentials["service_tier_override"]; ok {
		t.Fatalf("缺省创建不应落 service_tier_override: %v", credentials)
	}
	if _, ok := credentials["reasoning_effort_override"]; ok {
		t.Fatalf("缺省创建不应落 reasoning_effort_override: %v", credentials)
	}
	grokOverrideAssertInert(t, credentials)

	// 非法值：ultra 不在 openai 族思考级别全集，写入面拒绝、账户不落库
	// （HTTP 层按既有 writeCreateError → writeOAuthError 映射为 502 回退文案）。
	before := grokOverrideAccountCount(t, env)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/create-from-refresh-token",
		`{"refreshToken":"grok-r2","providerProtocolProfileId":"profile_xai_openai_v1",`+
			`"credentialsPatch":{"reasoning_effort_override":"ultra"}}`)
	if code != http.StatusBadGateway || payload["message"] != "Grok 刷新令牌授权失败" {
		t.Fatalf("非法创建: %d %v", code, payload)
	}
	if after := grokOverrideAccountCount(t, env); after != before {
		t.Fatalf("非法创建不应落库: before=%d after=%d", before, after)
	}
}

// TestGrokOverrideRotateLink 轮换链路：RotateCredentials 落库校验（store 层
// 导出 API 直驱，即存量 token 刷新的持久化入口 routes.go refreshToken）。
func TestGrokOverrideRotateLink(t *testing.T) {
	env := newTestEnv(t)
	env.w14dSeedRotatableAccountWithCreds(t, "grok-ov-rot", "xai", ProfileXAIOpenAIV1, "openai", "v1",
		map[string]any{"access_token": "a1", "refresh_token": "r1", "base_url": GrokOAuthBaseURL}, true)
	revision := int64(1)
	rotate := func(credentials map[string]any) (*RotationResult, error) {
		result, err := env.store.RotateCredentials(context.Background(), RotateCredentialsInput{
			AccountID:                         "grok-ov-rot",
			ExpectedConfigRevision:            revision,
			ExpectedProviderCode:              ProviderXAI,
			ExpectedAccountType:               "oauth",
			ExpectedProviderProtocolProfileID: ProfileXAIOpenAIV1,
			Credentials:                       credentials,
			Access:                            AccessScope{IsAdmin: true},
		})
		if err == nil && result != nil && result.Changed {
			revision = result.ConfigRevision
		}
		return result, err
	}

	// 合法值：覆盖字段随轮换凭据落库保留，出站生效。
	result, err := rotate(map[string]any{
		"access_token": "a2", "refresh_token": "r2", "base_url": GrokOAuthBaseURL,
		"expires_at": "2026-01-02T03:04:05.000Z",
		"service_tier_override": "priority", "reasoning_effort_override": "max",
	})
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("合法轮换: result=%v err=%v", result, err)
	}
	credentials := env.accountCredentials(t, "grok-ov-rot")
	if credentials["access_token"] != "a2" || credentials["service_tier_override"] != "priority" ||
		credentials["reasoning_effort_override"] != "max" {
		t.Fatalf("合法轮换凭据未保留: %v", credentials)
	}
	grokOverrideAssertApplied(t, credentials)

	// 缺省值：不带覆盖字段轮换，字段不落库、行为不变。
	result, err = rotate(map[string]any{
		"access_token": "a3", "refresh_token": "r3", "base_url": GrokOAuthBaseURL,
		"expires_at": "2026-01-02T03:04:05.000Z",
	})
	if err != nil || result == nil || !result.Changed {
		t.Fatalf("缺省轮换: result=%v err=%v", result, err)
	}
	credentials = env.accountCredentials(t, "grok-ov-rot")
	if _, ok := credentials["service_tier_override"]; ok {
		t.Fatalf("缺省轮换不应落 service_tier_override: %v", credentials)
	}
	if _, ok := credentials["reasoning_effort_override"]; ok {
		t.Fatalf("缺省轮换不应落 reasoning_effort_override: %v", credentials)
	}
	grokOverrideAssertInert(t, credentials)

	// 非法值（族集）：ultra 不在 openai 族思考级别全集，ValidationError 拒绝
	// 且 revision 与凭据保持原样（事务内拒绝、不写库）。
	result, err = rotate(map[string]any{
		"access_token": "a4", "refresh_token": "r4", "base_url": GrokOAuthBaseURL,
		"reasoning_effort_override": "ultra",
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || validation.Message != "思考级别覆盖无效" {
		t.Fatalf("非法轮换(ultra): result=%v err=%v", result, err)
	}
	var storedRevision int64
	if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, "grok-ov-rot").Scan(&storedRevision); err != nil {
		t.Fatal(err)
	}
	if storedRevision != revision {
		t.Fatalf("非法轮换不应推进 revision: stored=%d want=%d", storedRevision, revision)
	}
	credentials = env.accountCredentials(t, "grok-ov-rot")
	if credentials["access_token"] != "a3" {
		t.Fatalf("非法轮换不应换凭据: %v", credentials["access_token"])
	}

	// 非法值（token 形状）：首尾空白非法（IsToken 不做 trim 归一）。
	if _, err := rotate(map[string]any{
		"access_token": "a5", "refresh_token": "r5", "base_url": GrokOAuthBaseURL,
		"service_tier_override": " priority",
	}); err == nil || err.Error() != "服务等级覆盖无效" {
		t.Fatalf("非法轮换(空白): err=%v", err)
	}

	// 非法值（非字符串形状）。
	if _, err := rotate(map[string]any{
		"access_token": "a6", "refresh_token": "r6", "base_url": GrokOAuthBaseURL,
		"reasoning_effort_override": 7,
	}); err == nil || err.Error() != "思考级别覆盖无效" {
		t.Fatalf("非法轮换(非字符串): err=%v", err)
	}

	// 族集按协议族判定：anthropic 族服务等级为空集，任何非 default 档被拒。
	env.w14dSeedRotatableAccountWithCreds(t, "grok-ov-anth", "anthropic", ProfileAnthropicV1, "anthropic", "v1",
		map[string]any{"access_token": "ant-a", "refresh_token": "ant-r"}, true)
	if _, err := env.store.RotateCredentials(context.Background(), RotateCredentialsInput{
		AccountID:                         "grok-ov-anth",
		ExpectedConfigRevision:            1,
		ExpectedProviderCode:              ProviderAnthropic,
		ExpectedAccountType:               "oauth",
		ExpectedProviderProtocolProfileID: ProfileAnthropicV1,
		Credentials: map[string]any{
			"access_token": "ant-a2", "refresh_token": "ant-r2",
			"service_tier_override": "priority",
		},
		Access: AccessScope{IsAdmin: true},
	}); err == nil || err.Error() != "服务等级覆盖无效" {
		t.Fatalf("anthropic 族服务等级应被拒: err=%v", err)
	}
}

// TestGrokOverrideReauthorizeLink 重授权链路：HTTP
// reauthorize-from-refresh-token（持久化共用 RotateCredentials）。重授权
// strict body 不收覆盖字段，既有覆盖配置来自存量凭据（设计 §7.3：重授权只
// 刷新 token 与 OAuth 元数据，不清除覆盖配置）。
func TestGrokOverrideReauthorizeLink(t *testing.T) {
	env := newTestEnv(t)
	// 统一经 w14dLoginAdmin（按 env 缓存）：下方 seed 辅助内部也走它，直调
	// login 会重复建 root 触发唯一约束冲突。
	env.w14dLoginAdmin(t)
	env.exchanger.respond = staticToken(grokTokenPayload("grok-reauth-token"))

	// 合法值：存量凭据带合法覆盖 → 重授权保留覆盖字段并换 token → 出站生效。
	env.w14dSeedRotatableAccountWithCreds(t, "grok-ov-keep", "xai", ProfileXAIOpenAIV1, "openai", "v1",
		map[string]any{"access_token": "keep-a", "refresh_token": "keep-r", "base_url": GrokOAuthBaseURL,
			"service_tier_override": "priority", "reasoning_effort_override": "max"}, true)
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/grok-ov-keep/reauthorize-from-refresh-token",
		`{"refreshToken":"grok-r-next","expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, payload)["configRevision"] != float64(2) {
		t.Fatalf("合法重授权: %d %v", code, payload)
	}
	credentials := env.accountCredentials(t, "grok-ov-keep")
	if credentials["access_token"] != "grok-reauth-token" {
		t.Fatalf("重授权未换 token: %v", credentials["access_token"])
	}
	if credentials["service_tier_override"] != "priority" || credentials["reasoning_effort_override"] != "max" {
		t.Fatalf("重授权清除了既有覆盖配置: %v", credentials)
	}
	grokOverrideAssertApplied(t, credentials)

	// 缺省值：存量凭据不带覆盖 → 重授权后仍无字段、行为不变。
	env.w14dSeedRotatableAccountWithCreds(t, "grok-ov-plain", "xai", ProfileXAIOpenAIV1, "openai", "v1",
		map[string]any{"access_token": "plain-a", "refresh_token": "plain-r", "base_url": GrokOAuthBaseURL}, true)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/grok-ov-plain/reauthorize-from-refresh-token",
		`{"refreshToken":"grok-r-next","expectedConfigRevision":1}`)
	if code != http.StatusOK || dataMap(t, payload)["configRevision"] != float64(2) {
		t.Fatalf("缺省重授权: %d %v", code, payload)
	}
	credentials = env.accountCredentials(t, "grok-ov-plain")
	if _, ok := credentials["service_tier_override"]; ok {
		t.Fatalf("缺省重授权不应落 service_tier_override: %v", credentials)
	}
	if _, ok := credentials["reasoning_effort_override"]; ok {
		t.Fatalf("缺省重授权不应落 reasoning_effort_override: %v", credentials)
	}
	grokOverrideAssertInert(t, credentials)

	// 非法值：存量凭据带族集外值（模拟存量脏数据）→ 重授权被落库校验拒绝
	// （HTTP 层按既有 writeOAuthError 未分类分支映射 502 回退文案），revision
	// 与凭据原样保留（token 不换、畸形值不被静默清洗）。
	env.w14dSeedRotatableAccountWithCreds(t, "grok-ov-dirty", "xai", ProfileXAIOpenAIV1, "openai", "v1",
		map[string]any{"access_token": "dirty-a", "refresh_token": "dirty-r", "base_url": GrokOAuthBaseURL,
			"reasoning_effort_override": "ultra"}, true)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/grok-oauth/accounts/grok-ov-dirty/reauthorize-from-refresh-token",
		`{"refreshToken":"grok-r-next","expectedConfigRevision":1}`)
	if code != http.StatusBadGateway || payload["message"] != "Grok 刷新令牌重新授权失败" {
		t.Fatalf("非法重授权: %d %v", code, payload)
	}
	var storedRevision int64
	if err := env.db.QueryRow(`SELECT config_revision FROM accounts WHERE id = ?`, "grok-ov-dirty").Scan(&storedRevision); err != nil {
		t.Fatal(err)
	}
	if storedRevision != 1 {
		t.Fatalf("非法重授权不应推进 revision: %d", storedRevision)
	}
	credentials = env.accountCredentials(t, "grok-ov-dirty")
	if credentials["access_token"] != "dirty-a" || credentials["reasoning_effort_override"] != "ultra" {
		t.Fatalf("非法重授权凭据被改动: %v", credentials)
	}
}
