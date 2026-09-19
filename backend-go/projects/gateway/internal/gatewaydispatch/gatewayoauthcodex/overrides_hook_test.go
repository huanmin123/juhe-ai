package gatewayoauthcodex

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 自 gatewaydispatch portsandprep_test.go / finalgap_test.go 随被测对象迁入
// （REFACTOR-0006 阶段 A）：hook 注入用例需同包访问私有注入变量。

type stubOverrideCatalog struct {
	items []GptRequestOverrideModelCatalogItem
	err   error
}

func TestSetGptRequestOverrideModelCatalogPort(t *testing.T) {
	previous := gptRequestOverrideModelCatalog
	previousCandidates := gptRequestOverrideModelCandidates
	t.Cleanup(func() {
		gptRequestOverrideModelCatalog = previous
		gptRequestOverrideModelCandidates = previousCandidates
	})
	// 套件内其他测试可能替换过全局展开器，这里固定为恒等展开。
	SetGptRequestOverrideModelCandidates(func(providerCode, model string) []string { return []string{model} })

	SetGptRequestOverrideModelCatalog(&stubOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{{
		Model:                     "gpt-test",
		SupportedServiceTiers:     []string{"flex"},
		SupportedReasoningEfforts: []string{"high"},
	}}})
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID: "a-1", ProviderCode: "openai",
		Credentials: map[string]any{
			"service_tier_override":     "flex",
			"reasoning_effort_override": "high",
		},
	}
	capabilities, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-test")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if capabilities == nil || len(capabilities.SupportedServiceTiers) != 1 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	// 目录错误透传。
	SetGptRequestOverrideModelCatalog(&stubOverrideCatalog{err: errors.New("目录爆炸")})
	if _, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-test"); err == nil {
		t.Fatal("目录错误应透传")
	}
	// 未命中模型返回 nil。
	SetGptRequestOverrideModelCatalog(&stubOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{{Model: "other"}}})
	if caps, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-test"); err != nil || caps != nil {
		t.Fatalf("未命中 = %#v %v", caps, err)
	}
}

func TestSetGptRequestOverrideModelCandidatesExpander(t *testing.T) {
	previous := gptRequestOverrideModelCandidates
	t.Cleanup(func() { gptRequestOverrideModelCandidates = previous })
	SetGptRequestOverrideModelCandidates(func(providerCode, model string) []string {
		return []string{model, "alias-" + model}
	})
	candidates := gptRequestOverrideModelCandidates("openai", "gpt-test")
	if len(candidates) != 2 || candidates[1] != "alias-gpt-test" {
		t.Fatalf("candidates = %#v", candidates)
	}
	// nil 展开器为 no-op（保持当前注入）。
	SetGptRequestOverrideModelCandidates(nil)
	if got := gptRequestOverrideModelCandidates("openai", "m"); len(got) != 2 {
		t.Fatalf("nil 展开器应保持注入, got %#v", got)
	}
}

func TestSetGptAccountRequestOverridesHook(t *testing.T) {
	previous := gptAccountRequestOverridesHook
	t.Cleanup(func() { gptAccountRequestOverridesHook = previous })
	called := false
	SetGptAccountRequestOverridesHook(func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
		called = true
		body["service_tier"] = "flex"
		return body, nil
	})
	body := map[string]any{"model": "gpt-test"}
	if err := ApplyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !called || body["service_tier"] != "flex" {
		t.Fatalf("hook 未生效: %#v", body)
	}
	// hook 返回相同内容时不改写。
	unchanged := map[string]any{"model": "gpt-test"}
	SetGptAccountRequestOverridesHook(func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
		return body, nil
	})
	if err := ApplyOpenAIOAuthCodexAccountRequestOverrides(unchanged, OpenAIOAuthCodexNormalizeInput{}); err != nil {
		t.Fatalf("apply unchanged: %v", err)
	}
}

func TestNormalizeOpenAIOAuthCodexParsedBodyOverrideHookError(t *testing.T) {
	previous := gptAccountRequestOverridesHook
	SetGptAccountRequestOverridesHook(func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
		return nil, &GptAccountRequestOverrideError{Message: "覆盖值非法"}
	})
	t.Cleanup(func() { gptAccountRequestOverridesHook = previous })
	_, err := NormalizeOpenAIOAuthCodexParsedBody(map[string]any{"model": "gpt-test", "input": "hi"}, OpenAIOAuthCodexNormalizeInput{})
	if !IsOpenAIOAuthCodexAdapterError(err) || err.Error() != "覆盖值非法" {
		t.Fatalf("expected account-scoped adapter error, got %v", err)
	}
}
func (s *stubOverrideCatalog) ListGptRequestOverrideModelCatalog(context.Context, string, string, bool) ([]GptRequestOverrideModelCatalogItem, error) {
	return s.items, s.err
}
