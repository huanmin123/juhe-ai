package gatewaydispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// D-151（BUG-0175）gpt 账户请求覆盖：Mock 优先——目录能力通过
// GptRequestOverrideModelCatalog 端口注入，覆盖读取/应用按归档
// providers/drivers/gpt/request-overrides.ts 的分支回放。

type mockGptOverrideCatalog struct {
	items []GptRequestOverrideModelCatalogItem
	err   error
}

func (m *mockGptOverrideCatalog) ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]GptRequestOverrideModelCatalogItem, error) {
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

func overrideAccount(credentials map[string]any) AccountCandidate {
	return AccountCandidate{
		ID:                          "acc-1",
		ProviderCode:                "gpt",
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
}

func TestApplyGptAccountRequestOverridesBodyUnsupportedCapabilityStaysInert(t *testing.T) {
	body := map[string]any{"model": "m", "input": "hi"}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
		Credentials:       map[string]any{"reasoning_effort_override": "max"},
		EndpointFamily:    "responses",
		ModelCapabilities: &GptRequestOverrideModelCapabilities{SupportedReasoningEfforts: []string{"low"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	encodedOriginal, _ := json.Marshal(body)
	encodedResult, _ := json.Marshal(overridden)
	if string(encodedOriginal) != string(encodedResult) {
		t.Fatalf("body changed: %s -> %s", encodedOriginal, encodedResult)
	}
}

func TestApplyGptAccountRequestOverridesBodyCompactSkipsReasoning(t *testing.T) {
	body := map[string]any{"model": "m"}
	overridden, err := ApplyGptAccountRequestOverridesBody(body, GptAccountOverrideInput{
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
		Credentials:    map[string]any{"service_tier_override": "fast"},
		EndpointFamily: "responses",
	})
	if err == nil {
		t.Fatal("expected override error")
	}
	var overrideErr *GptAccountRequestOverrideError
	if !asOverrideError(err, &overrideErr) {
		t.Fatalf("error type = %T", err)
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
