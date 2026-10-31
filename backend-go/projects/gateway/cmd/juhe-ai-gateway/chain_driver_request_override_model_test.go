package main

// DD-F1 / DD-F2 修复钉子（gpt 账户请求覆盖的「真实上游模型/协议」判定）：
//   - DD-F1：覆盖能力解析的模型判定目标与 endpoint family 同口径——映射命中
//     时取映射右侧 UpstreamModel（AI账户上游接口能力设计 §3「命中映射时，以
//     映射右侧确定真实上游模型和协议」；GPT请求服务等级与思考级别覆盖设计
//     §8.2「能力校验和字段形态都针对真实上游模型与协议」），不再按请求模型
//     判定；无映射行为回归不变。
//   - DD-F2：service_tier 写面按最终 endpoint family 分支——跨协议映射目标
//     为 anthropic_messages 时不写 service_tier（requestoverride anthropic
//     族服务等级空集）；openai 直连行为不变。
//
// 目录能力通过 GptRequestOverrideModelCatalog 端口注入 Mock（Mock 优先），
// 源/目标模型行声明不同能力以区分「按请求模型」与「按最终模型」两种判定。

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayrouting"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// overrideModelTestCatalog 是覆盖能力目录 Mock：按精确模型名返回声明行。
type overrideModelTestCatalog struct {
	items []gatewaydispatch.GptRequestOverrideModelCatalogItem
}

func (c *overrideModelTestCatalog) ListGptRequestOverrideModelCatalog(ctx context.Context, providerCode, systemAccountID string, includeUnpriced bool) ([]gatewaydispatch.GptRequestOverrideModelCatalogItem, error) {
	return c.items, nil
}

func overrideModelTestDriver(catalog *overrideModelTestCatalog) *chainProviderDriver {
	driver := newChainProviderDriver()
	driver.gptRequestOverrideRuntime = gatewaydispatch.GptRequestOverrideRuntime{Catalog: catalog}
	return driver
}

func decodeOverrideTestBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("decode body: %v body=%s", err, body)
	}
	return parsed
}

// TestChainDriverOverrideDegradesByMappedUpstreamModel 钉住 DD-F1：映射命中
// 时覆盖能力按映射右侧 UpstreamModel 的目录行解析——目标模型 reasoning 档位
// 低于源行声明时按目标模型降级，不再给最终模型写出超档值（修复前按请求模型
// gpt-source 行解析会原样写出 xhigh）。
func TestChainDriverOverrideDegradesByMappedUpstreamModel(t *testing.T) {
	catalog := &overrideModelTestCatalog{items: []gatewaydispatch.GptRequestOverrideModelCatalogItem{
		{Model: "gpt-source", SupportedReasoningEfforts: []string{"high", "xhigh"}},
		{Model: "gpt-target", SupportedReasoningEfforts: []string{"low", "medium", "high"}},
	}}
	driver := overrideModelTestDriver(catalog)
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_override_map",
		VendorCode:   "gpt",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.example/v1",
		APIKey:       "gk-1",
		Credentials: map[string]any{
			"reasoning_effort_override": "xhigh",
		},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "gpt-source",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyChatCompletions,
			UpstreamModel:          "gpt-target",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyChatCompletions,
			Enabled:                true,
		}},
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-source","messages":[{"role":"user","content":"hi"}]}`)

	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	body := decodeOverrideTestBody(t, parts.Body)
	if body["model"] != "gpt-target" {
		t.Fatalf("映射后出站 model = %v, want gpt-target", body["model"])
	}
	if body["reasoning_effort"] != "high" {
		t.Fatalf("按目标模型行降级 reasoning_effort = %v, want high（不得写出超档 xhigh）", body["reasoning_effort"])
	}
}

// TestChainDriverOverrideWithoutMappingKeepsRequestModel 钉住 DD-F1 无映射
// 回归：未命中映射时覆盖能力仍按请求模型目录行解析，行为不变。
func TestChainDriverOverrideWithoutMappingKeepsRequestModel(t *testing.T) {
	catalog := &overrideModelTestCatalog{items: []gatewaydispatch.GptRequestOverrideModelCatalogItem{
		{Model: "gpt-source", SupportedReasoningEfforts: []string{"high", "xhigh"}},
		{Model: "gpt-target", SupportedReasoningEfforts: []string{"low", "medium", "high"}},
	}}
	driver := overrideModelTestDriver(catalog)
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_override_nomap",
		VendorCode:   "gpt",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.example/v1",
		APIKey:       "gk-1",
		Credentials: map[string]any{
			"reasoning_effort_override": "xhigh",
		},
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-source","messages":[{"role":"user","content":"hi"}]}`)

	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	body := decodeOverrideTestBody(t, parts.Body)
	if body["model"] != "gpt-source" {
		t.Fatalf("无映射出站 model = %v, want gpt-source", body["model"])
	}
	if body["reasoning_effort"] != "xhigh" {
		t.Fatalf("无映射按请求模型行判定 reasoning_effort = %v, want xhigh（行为回归不变）", body["reasoning_effort"])
	}
}

// TestChainDriverServiceTierStrippedForAnthropicMappedTarget 钉住 DD-F2：
// 跨协议映射目标为 anthropic_messages 时 service_tier 被剥离（最终 body 无
// 该字段），reasoning 覆盖仍按 anthropic wire 写 output_config.effort；目录
// 解析按映射右侧模型行（DD-F1 同源）。openai 直连回归见
// TestChainDriverServiceTierKeptForOpenAIDirect。
func TestChainDriverServiceTierStrippedForAnthropicMappedTarget(t *testing.T) {
	catalog := &overrideModelTestCatalog{items: []gatewaydispatch.GptRequestOverrideModelCatalogItem{
		{Model: "claude-target", SupportedServiceTiers: []string{"priority"}, SupportedReasoningEfforts: []string{"high"}},
	}}
	driver := overrideModelTestDriver(catalog)
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_override_bridge",
		VendorCode:   "gpt",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.example/v1",
		APIKey:       "gk-1",
		Credentials: map[string]any{
			"service_tier_override":     "priority",
			"reasoning_effort_override": "high",
		},
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "gpt-source",
			SourceEndpointFamily:   gatewayrouting.EndpointFamilyResponses,
			UpstreamModel:          "claude-target",
			UpstreamEndpointFamily: gatewayrouting.EndpointFamilyMessages,
			Enabled:                true,
		}},
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/responses", `{"model":"gpt-source","input":"hi"}`)

	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	body := decodeOverrideTestBody(t, parts.Body)
	if body["model"] != "claude-target" {
		t.Fatalf("映射后出站 model = %v, want claude-target", body["model"])
	}
	if _, has := body["service_tier"]; has {
		t.Fatalf("anthropic_messages 目标不得写入 service_tier: %v", body)
	}
	outputConfig, ok := body["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "high" {
		t.Fatalf("output_config = %v, want effort=high（reasoning 分支不受影响）", body["output_config"])
	}
	if _, has := body["messages"]; !has {
		t.Fatalf("anthropic 桥接 body 缺少 messages: %v", body)
	}
}

// TestChainDriverServiceTierKeptForOpenAIDirect 钉住 DD-F2 openai 直连回归：
// 未映射请求维持 service_tier 能力门写入不变。
func TestChainDriverServiceTierKeptForOpenAIDirect(t *testing.T) {
	catalog := &overrideModelTestCatalog{items: []gatewaydispatch.GptRequestOverrideModelCatalogItem{
		{Model: "gpt-x", SupportedServiceTiers: []string{"priority"}},
	}}
	driver := overrideModelTestDriver(catalog)
	account := gatewaydispatch.AccountCandidate{
		ID:           "acc_override_direct",
		VendorCode:   "gpt",
		ProtocolCode: "openai",
		Type:         "api_key",
		BaseURL:      "https://api.example/v1",
		APIKey:       "gk-1",
		Credentials: map[string]any{
			"service_tier_override": "priority",
		},
	}
	req := mappedURLTestRequest(t, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`)

	parts, err := driver.buildGatewayUpstreamRequestParts(context.Background(), req, account, gatewaydispatch.UsageIdentity{}, "")
	if err != nil {
		t.Fatalf("build parts: %v", err)
	}
	body := decodeOverrideTestBody(t, parts.Body)
	if body["service_tier"] != "priority" {
		t.Fatalf("openai 直连 service_tier = %v, want priority（行为回归不变）", body["service_tier"])
	}
}
