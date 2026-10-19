package gatewayoauthcodex

import (
	"context"
	"errors"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// 自 gatewaydispatch portsandprep_test.go / finalgap_test.go 随被测对象迁入
// （REFACTOR-0006 阶段 A）。T15 收敛后进程级全局与 Set* 已删除：目录端口、
// 候选展开器与覆盖应用 hook 全部经显式传参注入（GptRequestOverrideRuntime /
// OpenAIOAuthCodexNormalizeInput.ApplyGptAccountRequestOverrides）。

type stubOverrideCatalog struct {
	items []GptRequestOverrideModelCatalogItem
	err   error
}

func TestResolveGptRequestOverrideModelCapabilitiesPorts(t *testing.T) {
	runtime := GptRequestOverrideRuntime{
		Catalog: &stubOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{{
			Model:                     "gpt-test",
			SupportedServiceTiers:     []string{"flex"},
			SupportedReasoningEfforts: []string{"high"},
		}}},
		// 套件内其他用例可能传入自定义展开器，这里固定为恒等展开。
		ModelCandidates: func(providerCode, model string) []string { return []string{model} },
	}
	account := gatewayruntimecache.OpenAIAccountSecret{
		ID: "a-1", VendorCode: "openai",
		Credentials: map[string]any{
			"service_tier_override":     "flex",
			"reasoning_effort_override": "high",
		},
	}
	capabilities, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-test", runtime)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if capabilities == nil || len(capabilities.SupportedServiceTiers) != 1 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	// 目录错误透传。
	erroringRuntime := GptRequestOverrideRuntime{Catalog: &stubOverrideCatalog{err: errors.New("目录爆炸")}}
	if _, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-test", erroringRuntime); err == nil {
		t.Fatal("目录错误应透传")
	}
	// 未命中模型返回 nil。
	missingRuntime := GptRequestOverrideRuntime{Catalog: &stubOverrideCatalog{items: []GptRequestOverrideModelCatalogItem{{Model: "other"}}}}
	if caps, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-test", missingRuntime); err != nil || caps != nil {
		t.Fatalf("未命中 = %#v %v", caps, err)
	}
	// 目录端口缺席（runtime 零值）→ 覆盖惰性（能力解析恒 nil）。
	if caps, err := ResolveGptRequestOverrideModelCapabilities(context.Background(), account, "gpt-test", GptRequestOverrideRuntime{}); err != nil || caps != nil {
		t.Fatalf("未注入目录端口应惰性 = %#v %v", caps, err)
	}
}

func TestGptRequestOverrideRuntimeCandidatesExpander(t *testing.T) {
	// 显式候选展开器随 runtime 传入并被采用。
	runtime := GptRequestOverrideRuntime{ModelCandidates: func(providerCode, model string) []string {
		return []string{model, "alias-" + model}
	}}
	candidates := runtime.modelCandidatesOf()("openai", "gpt-test")
	if len(candidates) != 2 || candidates[1] != "alias-gpt-test" {
		t.Fatalf("candidates = %#v", candidates)
	}
	// nil 展开器回落包内默认实现（请求模型 + trim 归一，与收敛前全局初始值
	// 同一实现）。
	defaultCandidates := GptRequestOverrideRuntime{}.modelCandidatesOf()("openai", " m ")
	if len(defaultCandidates) != 2 || defaultCandidates[0] != " m " || defaultCandidates[1] != "m" {
		t.Fatalf("默认展开器 = %#v", defaultCandidates)
	}
}

// T15 显式注入：覆盖应用 hook 经 OpenAIOAuthCodexNormalizeInput 传入并被
// ApplyOpenAIOAuthCodexAccountRequestOverrides 调用。
func TestApplyOpenAIOAuthCodexAccountRequestOverridesExplicitInjection(t *testing.T) {
	called := false
	hook := func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
		called = true
		body["service_tier"] = "flex"
		return body, nil
	}
	body := map[string]any{"model": "gpt-test"}
	if err := ApplyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{
		ApplyGptAccountRequestOverrides: hook,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !called || body["service_tier"] != "flex" {
		t.Fatalf("hook 未生效: %#v", body)
	}
	// hook 返回相同内容时不改写。
	unchanged := map[string]any{"model": "gpt-test"}
	if err := ApplyOpenAIOAuthCodexAccountRequestOverrides(unchanged, OpenAIOAuthCodexNormalizeInput{
		ApplyGptAccountRequestOverrides: func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
			return body, nil
		},
	}); err != nil {
		t.Fatalf("apply unchanged: %v", err)
	}
}

// T15 负路径钉子：端口未注入 → 覆盖惰性（body 不改写、不报错），不存在任何
// 进程级回退。
func TestApplyOpenAIOAuthCodexAccountRequestOverridesInertWithoutPort(t *testing.T) {
	body := map[string]any{"model": "gpt-test"}
	if err := ApplyOpenAIOAuthCodexAccountRequestOverrides(body, OpenAIOAuthCodexNormalizeInput{}); err != nil {
		t.Fatalf("未注入端口应惰性: %v", err)
	}
	if _, changed := body["service_tier"]; changed {
		t.Fatalf("未注入端口时 body 不得改写：%v", body)
	}
}

func TestNormalizeOpenAIOAuthCodexParsedBodyOverrideHookError(t *testing.T) {
	_, err := NormalizeOpenAIOAuthCodexParsedBody(map[string]any{"model": "gpt-test", "input": "hi"}, OpenAIOAuthCodexNormalizeInput{
		ApplyGptAccountRequestOverrides: func(body map[string]any, input GptAccountOverrideInput) (map[string]any, error) {
			return nil, &GptAccountRequestOverrideError{Message: "覆盖值非法"}
		},
	})
	if !IsOpenAIOAuthCodexAdapterError(err) || err.Error() != "覆盖值非法" {
		t.Fatalf("expected account-scoped adapter error, got %v", err)
	}
}

func (s *stubOverrideCatalog) ListGptRequestOverrideModelCatalog(context.Context, string, string, bool) ([]GptRequestOverrideModelCatalogItem, error) {
	return s.items, s.err
}
