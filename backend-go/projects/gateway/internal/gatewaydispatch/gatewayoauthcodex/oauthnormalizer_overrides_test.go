package gatewayoauthcodex

import (
	"context"
	"encoding/json"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"strings"
	"testing"
)

// D-151（BUG-0175）gpt 账户请求覆盖：Mock 优先——目录能力通过
// GptRequestOverrideModelCatalog 端口注入，覆盖读取/应用按归档
// providers/drivers/gpt/request-overrides.ts 的分支回放。

type mockGptOverrideCatalog struct {
	items []GptRequestOverrideModelCatalogItem
	err   error
	// lastProviderCode 记录最后一次目录查询用的供应商身份，用于钉住覆盖
	// 运行时的身份解析（VendorCode）。
	lastProviderCode string
}

func (m *mockGptOverrideCatalog) ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]GptRequestOverrideModelCatalogItem, error) {
	m.lastProviderCode = providerCode
	if m.err != nil {
		return nil, m.err
	}
	if !includeUnpriced {
		// The override resolver always passes includeUnpriced=true; a false
		// value must not silently drop catalog rows in the mock either.
		return m.items, nil
	}
	return m.items, nil
}

func overrideAccount(credentials map[string]any) gatewayruntimecache.OpenAIAccountSecret {
	return gatewayruntimecache.OpenAIAccountSecret{
		ID:                          "acc-1",
		VendorCode:                "gpt",
		AccountOwnerSystemAccountID: "sys-1",
		Credentials:                 credentials,
	}
}

func TestReadGptAccountRequestOverrides(t *testing.T) {
	overrides, err := ReadGptAccountRequestOverrides(map[string]any{
		"service_tier_override":     "priority",
		"reasoning_effort_override": "high",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overrides.ServiceTier != "priority" || overrides.ReasoningEffort != "high" {
		t.Fatalf("overrides = %+v", overrides)
	}
	empty, err := ReadGptAccountRequestOverrides(map[string]any{})
	if err != nil || empty.ServiceTier != "" || empty.ReasoningEffort != "" {
		t.Fatalf("empty overrides = %+v err=%v", empty, err)
	}
	if _, err := ReadGptAccountRequestOverrides(map[string]any{"service_tier_override": "has space"}); err == nil {
		t.Fatal("expected invalid token error for whitespace token")
	} else if !strings.Contains(err.Error(), "service_tier_override") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyGptAccountRequestOverridesBodyResponses(t *testing.T) {
	body := map[string]any{
		"model":            "gpt-5.3",
		"input":            "hi",
		"reasoning":        map[string]any{"effort": "low"},
		"reasoning_effort": "low",
	}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		VendorCode: "gpt",
		Credentials: map[string]any{
			"service_tier_override":     "flex",
			"reasoning_effort_override": "high",
		},
		EndpointFamily: "responses",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{
			SupportedServiceTiers:     []string{"flex", "priority"},
			SupportedReasoningEfforts: []string{"high"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overridden["service_tier"] != "flex" {
		t.Errorf("service_tier = %v", overridden["service_tier"])
	}
	reasoning := overridden["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning = %v", overridden["reasoning"])
	}
	if _, hasLegacy := overridden["reasoning_effort"]; hasLegacy {
		t.Errorf("reasoning_effort must be removed on responses bodies")
	}
}

func TestApplyGptAccountRequestOverridesBodyChatCompletions(t *testing.T) {
	body := map[string]any{"model": "gpt-5.3", "messages": []any{}, "reasoning": map[string]any{"effort": "low"}}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		VendorCode:      "gpt",
		Credentials:       map[string]any{"reasoning_effort_override": "minimal"},
		EndpointFamily:    "chat_completions",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"minimal"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overridden["reasoning_effort"] != "minimal" {
		t.Errorf("reasoning_effort = %v", overridden["reasoning_effort"])
	}
	if _, has := overridden["reasoning"]; has {
		t.Errorf("reasoning must be removed on chat bodies")
	}
}

func TestApplyGptAccountRequestOverridesBodyServiceTierDefaultDeletes(t *testing.T) {
	body := map[string]any{"model": "m", "service_tier": "priority"}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		VendorCode:      "gpt",
		Credentials:       map[string]any{"service_tier_override": "default"},
		EndpointFamily:    "responses",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedServiceTiers: []string{"priority"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := overridden["service_tier"]; has {
		t.Errorf("service_tier=default must delete the field")
	}
	// 降级用例④：default 恒为「强制标准档」——模型未声明任何档位
	// （capabilities 为 nil）也删除客户端 service_tier（新语义，取代旧
	// “能力未声明则惰性”）。
	overridden, err = ApplyGptAccountRequestOverridesBody(map[string]any{"model": "m", "service_tier": "flex"}, GptAccountOverrideInput{
		VendorCode:   "gpt",
		Credentials:    map[string]any{"service_tier_override": "default"},
		EndpointFamily: "responses",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := overridden["service_tier"]; has {
		t.Errorf("service_tier=default must delete the field even without capability declarations")
	}
}

// 降级用例①：配置 max、最终模型只声明 low,medium → 写档位序不高于 max 的
// 最高档 medium（2026-10-18 降级语义，取代旧“精确匹配否则丢弃即惰性”断言）。
func TestApplyGptAccountRequestOverridesBodyDegradesReasoningEffort(t *testing.T) {
	body := map[string]any{"model": "m", "input": "hi", "reasoning": map[string]any{"effort": "low"}}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		VendorCode:      "gpt",
		Credentials:       map[string]any{"reasoning_effort_override": "max"},
		EndpointFamily:    "responses",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"low", "medium"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reasoning := overridden["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Fatalf("reasoning = %v, want degraded medium", overridden["reasoning"])
	}
}

// 降级用例②：配置 minimal、最终模型只声明 high（全部高于配置值）→ 无可用
// 档位，不改写字段，保留客户端原 reasoning.effort。
func TestApplyGptAccountRequestOverridesBodyKeepsClientEffortWithoutDegradeTarget(t *testing.T) {
	body := map[string]any{"model": "m", "input": "hi", "reasoning": map[string]any{"effort": "low"}}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		VendorCode:      "gpt",
		Credentials:       map[string]any{"reasoning_effort_override": "minimal"},
		EndpointFamily:    "responses",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"high"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	encodedOriginal, _ := json.Marshal(body)
	encodedResult, _ := json.Marshal(overridden)
	if string(encodedOriginal) != string(encodedResult) {
		t.Fatalf("body must keep client reasoning: %s -> %s", encodedOriginal, encodedResult)
	}
}

// 降级用例③：服务等级不降级——priority 未被最终模型声明 → 不写入，客户端
// 原 service_tier 原样保留。
func TestApplyGptAccountRequestOverridesBodyKeepsClientServiceTierWhenUnsupported(t *testing.T) {
	body := map[string]any{"model": "m", "input": "hi", "service_tier": "flex"}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		VendorCode:      "gpt",
		Credentials:       map[string]any{"service_tier_override": "priority"},
		EndpointFamily:    "responses",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedServiceTiers: []string{"flex"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overridden["service_tier"] != "flex" {
		t.Fatalf("service_tier = %v, want client value flex kept", overridden["service_tier"])
	}
}

// 降级用例⑤（跨族）：配置 max、gemini 族模型声明 minimal,low,medium,high →
// 写档位序不高于 max 的最高档 high。
func TestApplyGptAccountRequestOverridesBodyDegradesAcrossFamilies(t *testing.T) {
	body := map[string]any{"model": "gemini-2.5-flash", "contents": []any{}}
	out, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		Credentials:       map[string]any{"reasoning_effort_override": "max"},
		EndpointFamily:    "gemini_generate_content",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"minimal", "low", "medium", "high"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	generationConfig := out["generationConfig"].(map[string]any)
	thinkingConfig := generationConfig["thinkingConfig"].(map[string]any)
	if thinkingConfig["thinkingLevel"] != "high" {
		t.Fatalf("thinkingLevel = %v, want degraded high", thinkingConfig["thinkingLevel"])
	}
}

func TestApplyGptAccountRequestOverridesBodyCompactSkipsReasoning(t *testing.T) {
	body := map[string]any{"model": "m"}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		VendorCode:      "gpt",
		Credentials:       map[string]any{"reasoning_effort_override": "high"},
		EndpointFamily:    "responses",
		Compact:           true,
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"high"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, has := overridden["reasoning"]; has {
		t.Errorf("compact must skip reasoning overrides")
	}
}

func TestApplyGptAccountRequestOverridesBodyInvalidValueIsAccountScoped(t *testing.T) {
	_, err := ApplyGptAccountRequestOverridesBody(map[string]any{}, GptAccountOverrideInput{
		VendorCode:   "gpt",
		Credentials:    map[string]any{"service_tier_override": "fast"},
		EndpointFamily: "responses",
	})
	if err == nil {
		t.Fatal("expected override error")
	}
	var overrideErr *GptAccountRequestOverrideError
	if !AsOverrideError(err, &overrideErr) {
		t.Fatalf("error type = %T", err)
	}
}

// 供应商感知值域断言：gpt 枚举是 gpt 专属 wire 约束；其它供应商（含未知/空
// providerCode）只保留 token 形状校验，目录精确声明的供应商原生值直接应用。
func TestApplyGptAccountRequestOverridesBodyProviderAwareEnum(t *testing.T) {
	capabilities := &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"ultra"}}

	// 非 gpt 供应商（deepseek）+ 目录声明的 gpt 枚举外值 → 写入 body 且不报错。
	overridden, err := ApplyGptAccountRequestOverridesBody(
		map[string]any{"model": "deepseek-chat", "messages": []any{}},
		GptAccountOverrideInput{
			VendorCode:      "deepseek",
			Credentials:       map[string]any{"reasoning_effort_override": "ultra"},
			EndpointFamily:    "chat_completions",
			ModelCapabilities: capabilities,
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overridden["reasoning_effort"] != "ultra" {
		t.Fatalf("reasoning_effort = %v, want ultra", overridden["reasoning_effort"])
	}

	// gpt 供应商同值仍走专属枚举并拒绝（错误文案逐点保留）。
	_, err = ApplyGptAccountRequestOverridesBody(
		map[string]any{"model": "gpt-5.3", "input": "hi"},
		GptAccountOverrideInput{
			VendorCode:      "gpt",
			Credentials:       map[string]any{"reasoning_effort_override": "ultra"},
			EndpointFamily:    "responses",
			ModelCapabilities: capabilities,
		})
	if err == nil || err.Error() != "GPT 账户请求覆盖字段 reasoning_effort_override 无效" {
		t.Fatalf("err = %v, want gpt 枚举拒绝", err)
	}

	// 未知/空 providerCode 跳过 gpt 枚举：遗漏传入只会少一层冗余校验，
	// 值域仍由目录能力门决定（声明过的值应用）。
	overridden, err = ApplyGptAccountRequestOverridesBody(
		map[string]any{"model": "m", "messages": []any{}},
		GptAccountOverrideInput{
			Credentials:       map[string]any{"reasoning_effort_override": "ultra"},
			EndpointFamily:    "chat_completions",
			ModelCapabilities: capabilities,
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if overridden["reasoning_effort"] != "ultra" {
		t.Fatalf("empty provider reasoning_effort = %v, want ultra", overridden["reasoning_effort"])
	}
}

// 上游 body 路径（非 OAuth api_key 运行时）同样供应商感知：deepseek 目录声明
// ultra → 应用；gpt 账户同值 → 保留硬失败。
func TestApplyGptAccountRequestOverridesToUpstreamBodyProviderAware(t *testing.T) {
	catalog := &mockGptOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{{
		Model:                     "deepseek-chat",
		SupportedReasoningEfforts: []string{"ultra"},
	}}}
	previous := gptRequestOverrideModelCatalog
	gptRequestOverrideModelCatalog = catalog
	defer func() { gptRequestOverrideModelCatalog = previous }()

	deepseek := gatewayruntimecache.OpenAIAccountSecret{
		ID: "acc-1", VendorCode: "deepseek", AccountOwnerSystemAccountID: "sys-1",
		Credentials: map[string]any{"reasoning_effort_override": "ultra"},
	}
	out, err := ApplyGptAccountRequestOverridesToUpstreamBody(
		context.Background(), []byte(`{"model":"deepseek-chat","messages":[]}`), deepseek, "chat_completions", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(out), `"reasoning_effort":"ultra"`) {
		t.Fatalf("ultra must be applied: %s", out)
	}

	gptAccount := overrideAccount(map[string]any{"reasoning_effort_override": "ultra"})
	_, err = ApplyGptAccountRequestOverridesToUpstreamBody(
		context.Background(), []byte(`{}`), gptAccount, "responses", false, "gpt-x")
	if err == nil || err.Error() != "GPT 账户请求覆盖字段 reasoning_effort_override 无效" {
		t.Fatalf("err = %v, want gpt 枚举拒绝", err)
	}
}

// hook 接线：ApplyOpenAIOAuthCodexAccountRequestOverrides 必须把
// account.VendorCode 传入 GptAccountOverrideInput（组合根 codexAccountOf 补齐
// 后供应商感知断言才能拿到供应商）。
func TestApplyOpenAIOAuthCodexAccountRequestOverridesPassesVendorCode(t *testing.T) {
	previous := gptAccountRequestOverridesHook
	gptAccountRequestOverridesHook = ApplyGptAccountRequestOverridesBody
	t.Cleanup(func() { gptAccountRequestOverridesHook = previous })

	body := map[string]any{"model": "deepseek-chat", "input": "hi"}
	err := ApplyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
		Account: OpenAIOAuthCodexAccount{
			ID:           "acc-1",
			VendorCode: "deepseek",
			Credentials:  map[string]any{"reasoning_effort_override": "ultra"},
		},
		RequestOverrideModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"ultra"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reasoning, ok := body["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "ultra" {
		t.Fatalf("reasoning = %v, want effort ultra", body["reasoning"])
	}
}

// hook 路径同样按 VendorCode 识别身份（供应商解耦过渡形态）：只有 vendor 列
// 且值为 gpt 时，gpt 专属枚举必须照常生效。
func TestApplyOpenAIOAuthCodexAccountRequestOverridesPrefersVendorCode(t *testing.T) {
	previous := gptAccountRequestOverridesHook
	gptAccountRequestOverridesHook = ApplyGptAccountRequestOverridesBody
	t.Cleanup(func() { gptAccountRequestOverridesHook = previous })

	body := map[string]any{"model": "gpt-5.3", "input": "hi"}
	err := ApplyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
		Account: OpenAIOAuthCodexAccount{
			ID:          "acc-1",
			VendorCode:  "gpt",
			Credentials: map[string]any{"reasoning_effort_override": "ultra"},
		},
		RequestOverrideModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"ultra"}},
	})
	if err == nil {
		t.Fatal("VendorCode=gpt 时 ultra 必须被 gpt 枚举拒绝")
	}
}

// hook 路径负向钉子：VendorCode 若在 hook 接线处漏传（账户被当成未知
// 供应商跳过 gpt 枚举），gpt 账户的枚举外覆盖值会被静默跳过（无能力声明时
// 保持惰性 / 有能力声明时直接应用），本用例必须失败。
func TestApplyOpenAIOAuthCodexAccountRequestOverridesRejectsGptEnumViaVendorCode(t *testing.T) {
	previous := gptAccountRequestOverridesHook
	gptAccountRequestOverridesHook = ApplyGptAccountRequestOverridesBody
	t.Cleanup(func() { gptAccountRequestOverridesHook = previous })

	// gpt 账户 + 枚举外 reasoning_effort_override：即目录声明了 ultra，
	// gpt 专属 wire 枚举仍必须先拒绝。
	body := map[string]any{"model": "gpt-5.3", "input": "hi"}
	err := ApplyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
		Account: OpenAIOAuthCodexAccount{
			ID:           "acc-1",
			VendorCode: "gpt",
			Credentials:  map[string]any{"reasoning_effort_override": "ultra"},
		},
		RequestOverrideModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"ultra"}},
	})
	if err == nil || err.Error() != "GPT 账户请求覆盖字段 reasoning_effort_override 无效" {
		t.Fatalf("err = %v, want gpt 枚举拒绝", err)
	}
	if _, changed := body["reasoning"]; changed {
		t.Fatalf("拒绝时 body 不得改写：%v", body)
	}

	// gpt 账户 + 枚举外 service_tier_override 同路径拒绝。
	body = map[string]any{"model": "gpt-5.3", "input": "hi"}
	err = ApplyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
		Account: OpenAIOAuthCodexAccount{
			ID:           "acc-1",
			VendorCode: "gpt",
			Credentials:  map[string]any{"service_tier_override": "turbo"},
		},
		RequestOverrideModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedServiceTiers: []string{"turbo"}},
	})
	if err == nil || err.Error() != "GPT 账户请求覆盖字段 service_tier_override 无效" {
		t.Fatalf("err = %v, want gpt 枚举拒绝", err)
	}
	if _, changed := body["service_tier"]; changed {
		t.Fatalf("拒绝时 body 不得改写：%v", body)
	}
}

func TestResolveGptRequestOverrideModelCapabilities(t *testing.T) {
	catalog := &mockGptOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{
		{Model: "gpt-5.3", SupportedServiceTiers: []string{"priority"}, SupportedReasoningEfforts: []string{"high", "xhigh"}},
	}}
	previous := gptRequestOverrideModelCatalog
	gptRequestOverrideModelCatalog = catalog
	defer func() { gptRequestOverrideModelCatalog = previous }()

	account := overrideAccount(map[string]any{"reasoning_effort_override": "xhigh"})
	capabilities, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-5.3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capabilities == nil || len(capabilities.SupportedReasoningEfforts) != 2 {
		t.Fatalf("capabilities = %+v", capabilities)
	}

	// 无覆盖配置 → 不解析目录（Node 早退）。
	noOverrides, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), overrideAccount(nil), "gpt-5.3")
	if err != nil || noOverrides != nil {
		t.Fatalf("no-override resolution = %+v err=%v", noOverrides, err)
	}

	// 目录未命中 → nil（覆盖保持惰性）。
	missing, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-9")
	if err != nil || missing != nil {
		t.Fatalf("missing-model resolution = %+v err=%v", missing, err)
	}
}

// 供应商身份只认 VendorCode（供应商与上游协议面重构设计 §1.1/§2.1，代码不做
// 旧列回落）：身份在场时按该值查目录，为空时覆盖保持惰性、不猜身份。
func TestResolveGptRequestOverrideModelCapabilitiesUsesVendorCodeIdentity(t *testing.T) {
	catalog := &mockGptOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{
		{Model: "grok-4.7", SupportedReasoningEfforts: []string{"low", "high"}},
	}}
	previous := gptRequestOverrideModelCatalog
	gptRequestOverrideModelCatalog = catalog
	defer func() { gptRequestOverrideModelCatalog = previous }()

	account := overrideAccount(map[string]any{"reasoning_effort_override": "high"})
	account.VendorCode = "xai"
	capabilities, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "grok-4.7")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capabilities == nil || len(capabilities.SupportedReasoningEfforts) != 2 {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if catalog.lastProviderCode != "xai" {
		t.Fatalf("目录查询身份 = %q, want xai", catalog.lastProviderCode)
	}

	// 身份为空 → 不查目录（覆盖惰性，不猜身份）。
	empty := account
	empty.VendorCode = ""
	catalog.lastProviderCode = ""
	resolved, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), empty, "grok-4.7")
	if err != nil || resolved != nil {
		t.Fatalf("无身份解析 = %+v err=%v", resolved, err)
	}
	if catalog.lastProviderCode != "" {
		t.Fatalf("无身份时不应查目录，实际身份 = %q", catalog.lastProviderCode)
	}
}

// gpt 专属枚举断言同样必须按 VendorCode 识别身份：只有 vendor 列时也要拦住
// ultra（若运行时仍只读恒空的旧列，这条会因“未知供应商跳过枚举”而放行）。
// 另两条判别臂钉死"禁止旧列回落"（功能开发指导 §5.1）：旧列有值但新列为空时
// 必须保持惰性；两列同时有值时必须只认 VendorCode。
func TestApplyGptAccountRequestOverridesToUpstreamBodyUsesVendorCodeIdentity(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3","input":"hi"}`)

	// 只有 VendorCode=gpt：ultra 被 gpt 专属枚举拒绝。
	account := overrideAccount(map[string]any{"reasoning_effort_override": "ultra"})
	account.VendorCode = "gpt"
	_, err := ApplyGptAccountRequestOverridesToUpstreamBody(context.Background(), body, account, "responses", false, "gpt-5.3")
	if err == nil {
		t.Fatal("只有 VendorCode=gpt 时 ultra 必须被 gpt 枚举拒绝")
	}
	if !strings.Contains(err.Error(), "reasoning_effort_override 无效") {
		t.Fatalf("错误文案 = %v", err)
	}

	// 回落复活判别：旧列有值、新列为空 → 身份为空，保持惰性（若实现里出现
	// ProviderCode 回落，ultra 会被 gpt 枚举拒绝，本臂失败）。
	legacyOnly := overrideAccount(map[string]any{"reasoning_effort_override": "ultra"})
	legacyOnly.VendorCode = ""
	legacyOnly.ProviderCode = "gpt"
	if _, err := ApplyGptAccountRequestOverridesToUpstreamBody(context.Background(), body, legacyOnly, "responses", false, "gpt-5.3"); err != nil {
		t.Fatalf("旧列必须不参与身份解析（禁止回落）：%v", err)
	}

	// 双载体判别：两列同时有值时只认 VendorCode（若回落到旧列 gpt，ultra 会被拒绝，本臂失败）。
	vendorWins := overrideAccount(map[string]any{"reasoning_effort_override": "ultra"})
	vendorWins.VendorCode = "deepseek"
	vendorWins.ProviderCode = "gpt"
	if _, err := ApplyGptAccountRequestOverridesToUpstreamBody(context.Background(), body, vendorWins, "responses", false, "gpt-5.3"); err != nil {
		t.Fatalf("两列在场必须只认 VendorCode（禁止回落到旧列）：%v", err)
	}
}

func TestApplyGptAccountRequestOverridesToUpstreamBody(t *testing.T) {
	catalog := &mockGptOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{
		{Model: "gpt-5.3", SupportedServiceTiers: []string{"priority"}},
	}}
	previous := gptRequestOverrideModelCatalog
	gptRequestOverrideModelCatalog = catalog
	defer func() { gptRequestOverrideModelCatalog = previous }()

	account := overrideAccount(map[string]any{"service_tier_override": "priority"})
	body := []byte(`{"model":"gpt-5.3","input":"hi"}`)
	out, err := ApplyGptAccountRequestOverridesToUpstreamBody(context.Background(), body, account, "responses", false, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output not json: %v", err)
	}
	if parsed["service_tier"] != "priority" {
		t.Fatalf("service_tier = %v", parsed["service_tier"])
	}

	// 无覆盖配置时 body 原样返回（零解析路径）。
	plain := overrideAccount(nil)
	out, err = ApplyGptAccountRequestOverridesToUpstreamBody(context.Background(), body, plain, "responses", false, "")
	if err != nil || string(out) != string(body) {
		t.Fatalf("body should stay untouched: %s err=%v", out, err)
	}
}
