package main

// gatewayusage 组合根端口接线单测（chain_usage_wiring.go）：三个真实适配器
// 的映射用例 + 装配组合冒烟（服务层消费形态验证）。

import (
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

func TestChainUsageDefaultProviderCode(t *testing.T) {
	if got := (chainUsageDefaultProviderCode{}).DefaultUsageProviderCode(); got != "gpt" {
		t.Fatalf("默认 providerCode 必须 = GPT_VENDOR_CODE（gpt），实际 %q", got)
	}
}

func TestChainUsageSemanticResolver(t *testing.T) {
	resolver := chainUsageSemanticResolver{}
	cases := []struct {
		name      string
		provider  string
		protocol  string
		profileID string
		expect    string
	}{
		{name: "anthropic", provider: "anthropic", protocol: "anthropic_v1", expect: "anthropic"},
		{name: "gemini", provider: "gemini", protocol: "gemini_v1beta", expect: "gemini"},
		{name: "gpt", provider: "gpt", protocol: "openai_v1", expect: "openai"},
		{name: "openai-compatible", provider: "openai-compatible", protocol: "openai_v1", expect: "openai"},
		{name: "空 provider", provider: "", protocol: "anthropic_v1", expect: "openai"},
		// hybrid profile 级覆盖：语义按档案 ID 决定（对齐 Node registry hybrid
		// driver 的 anthropic-messages / gemini-native / openai chat 分派）。
		{
			name:      "hybrid anthropic messages 档案",
			provider:  "hybrid",
			protocol:  "anthropic",
			profileID: "profile_hybrid_anthropic_messages_v1",
			expect:    "anthropic",
		},
		{
			name:      "hybrid gemini native 档案",
			provider:  "hybrid",
			protocol:  "gemini",
			profileID: "profile_hybrid_gemini_native_v1beta",
			expect:    "gemini",
		},
		{
			name:      "hybrid openai chat 档案",
			provider:  "hybrid",
			protocol:  "openai",
			profileID: "profile_hybrid_openai_chat_v1",
			expect:    "openai",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolver.UsageSemanticForProfile(&gatewayusage.ProviderProtocolProfile{
				ProviderCode: tc.provider,
				ProtocolCode: tc.protocol,
				ProfileID:    tc.profileID,
			})
			if got != tc.expect {
				t.Fatalf("profile(%s/%s/%s) 语义不符：期望 %q，实际 %q", tc.provider, tc.protocol, tc.profileID, tc.expect, got)
			}
		})
	}
	if got := resolver.UsageSemanticForProfile(nil); got != "openai" {
		t.Fatalf("nil profile 必须回落 openai，实际 %q", got)
	}
}

// TestChainUsageSemanticResolverMatchesGatewayResponseVocabulary 钉住与
// gatewayresponse usageSemanticForProviderCode 的词汇表一致（同一 Node
// registry 语义的两个落点不得漂移）。
func TestChainUsageSemanticResolverMatchesGatewayResponseVocabulary(t *testing.T) {
	resolver := chainUsageSemanticResolver{}
	for _, provider := range []string{"anthropic", "gemini", "gpt", "openai-compatible", "glm", "deepseek", "xai", ""} {
		got := resolver.UsageSemanticForProfile(&gatewayusage.ProviderProtocolProfile{ProviderCode: provider})
		switch provider {
		case "anthropic":
			if got != "anthropic" {
				t.Fatalf("provider %q：期望 anthropic，实际 %q", provider, got)
			}
		case "gemini":
			if got != "gemini" {
				t.Fatalf("provider %q：期望 gemini，实际 %q", provider, got)
			}
		default:
			if got != "openai" {
				t.Fatalf("provider %q：期望 openai 回退，实际 %q", provider, got)
			}
		}
	}
}

func TestChainUsageProtocolErrorParserDispatch(t *testing.T) {
	parser := newChainUsageProtocolErrorParser()
	cases := []struct {
		name       string
		protocol   string
		body       string
		expectCode string
		expectMsg  string
	}{
		{
			name:       "openai 形态",
			protocol:   "openai_v1",
			body:       `{"error":{"code":"insufficient_quota","type":"insufficient_quota","message":"You exceeded your current quota"}}`,
			expectCode: "insufficient_quota",
			expectMsg:  "You exceeded your current quota",
		},
		{
			name:       "anthropic 形态",
			protocol:   "anthropic_v1",
			body:       `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
			expectCode: "authentication_error",
			expectMsg:  "invalid x-api-key",
		},
		{
			name:       "gemini 形态",
			protocol:   "gemini_v1beta",
			body:       `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`,
			expectCode: "429",
			expectMsg:  "Resource has been exhausted",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := parser.ParseProtocolErrorPayload(gatewayusage.UsageModelAccount{
				Profile: &gatewayusage.ProviderProtocolProfile{ProtocolCode: tc.protocol},
			}, tc.body, map[string]any{"Content-Type": "application/json"})
			record, ok := payload.(map[string]any)
			if !ok {
				t.Fatalf("返回必须是 map[string]any，实际 %T", payload)
			}
			if record["code"] != tc.expectCode {
				t.Fatalf("code 不符：期望 %q，实际 %v", tc.expectCode, record["code"])
			}
			if record["message"] != tc.expectMsg {
				t.Fatalf("message 不符：期望 %q，实际 %v", tc.expectMsg, record["message"])
			}
		})
	}
}

// 无证据 / 非法 body 必须回落 nil（usage 层保持自身兜底），协议分派缺省
// 落 openai 驱动。
func TestChainUsageProtocolErrorParserFallbacks(t *testing.T) {
	parser := newChainUsageProtocolErrorParser()
	account := gatewayusage.UsageModelAccount{Profile: &gatewayusage.ProviderProtocolProfile{ProtocolCode: "anthropic_v1"}}
	if got := parser.ParseProtocolErrorPayload(account, "not json at all", map[string]any{}); got != nil {
		t.Fatalf("非 JSON body 必须回落 nil，实际 %v", got)
	}
	// 缺省 profile：openai 驱动。
	defaultAccount := gatewayusage.UsageModelAccount{}
	payload := parser.ParseProtocolErrorPayload(defaultAccount,
		`{"error":{"message":"boom","code":"server_error"}}`, nil)
	record, ok := payload.(map[string]any)
	if !ok || record["code"] != "server_error" {
		t.Fatalf("缺省 profile 必须经 openai 驱动解析，实际 %v", payload)
	}
}

// 组合冒烟：三个适配器经真实 With* 装配后，服务层失败记账携带 providerCode
// 兜底与语义字段（对齐 chain_compose.go usageService 装配链）。
func TestChainUsageWiringSmokeRecordGatewayFailure(t *testing.T) {
	recorder := gatewayusage.NewMemoryUsageRecorder(nil, nil)
	dispatch := gatewayusage.NewFinalizationDispatch(recorder, nil, 8, 2)
	service := gatewayusage.NewService(dispatch, gatewayusage.ServiceConfig{SyncPricingAllowed: true}).
		WithUsageSemantics(chainUsageSemanticResolver{}).
		WithDefaultProviderCode(chainUsageDefaultProviderCode{}).
		WithProtocolErrorParser(newChainUsageProtocolErrorParser())
	err := service.RecordGatewayFailure(nil, gatewayusage.GatewayFailureUsageContext{
		GatewayUsageContext: gatewayusage.GatewayUsageContext{
			TraceID:  "trace-smoke",
			Endpoint: "POST /v1/chat/completions",
		},
		// ProviderCode 刻意留空：兜底端口必须补 GPT_VENDOR_CODE。
	}, gatewayusage.RecordGatewayFailureInput{
		StatusCode:      502,
		StartedAtMs:     1700000000000,
		CompletedAtMs:   1700000001000,
		ResponsePayload: map[string]any{"error": map[string]any{"message": "upstream boom", "code": "bad_gateway"}},
		Stream:          false,
	})
	if err != nil {
		t.Fatalf("失败记账返回错误：%v", err)
	}
	// 收尾队列异步投递：等待空闲后再断言落账。
	if !dispatch.WaitForIdle(2000) {
		t.Fatalf("收尾队列未在超时内空闲")
	}
	records := recorder.Records()
	if len(records) != 1 {
		t.Fatalf("期望 1 条失败记录，实际 %d", len(records))
	}
	record := records[0]
	if record.ProviderCode != "gpt" {
		t.Fatalf("providerCode 兜底不符：期望 gpt，实际 %q", record.ProviderCode)
	}
	if record.UsageSemantic != "openai" {
		t.Fatalf("语义字段不符：期望 openai，实际 %q", record.UsageSemantic)
	}
	if !strings.Contains(record.ErrorMessage, "upstream boom") {
		t.Fatalf("errorMessage 应取协议 payload 的 message，实际 %q", record.ErrorMessage)
	}
}

// TestChainUsageModelResolver 钉住 usageModelResolverAdapter 的真实解析语义
// （2026-09-20 接线，对齐 Node registry.ts resolveGatewayUsageModel）：命中
// 账户映射→解析为上游模型（modelMappingSource = runtimeSource ?? 'account'）；
// 无映射 / 禁用 / 未命中 / family 不在映射范围→回退请求模型原样透传。
func TestChainUsageModelResolver(t *testing.T) {
	resolver := usageModelResolverAdapter{}
	enabled := true
	disabled := false
	mapping := func(enabled *bool, runtimeSource string) gatewayopenai.AccountModelMapping {
		return gatewayopenai.AccountModelMapping{
			SourceModel:            "gpt-5",
			SourceEndpointFamily:   gatewayopenai.FamilyChatCompletions,
			UpstreamModel:          "gpt-4.1-mini",
			UpstreamEndpointFamily: gatewayopenai.FamilyChatCompletions,
			Enabled:                enabled,
			RuntimeSource:          runtimeSource,
		}
	}
	accountWith := func(mappings []gatewayopenai.AccountModelMapping) gatewayusage.UsageModelAccount {
		return gatewayusage.UsageModelAccount{
			ProviderCode: "gpt",
			Profile: &gatewayusage.ProviderProtocolProfile{
				ProviderCode:    "gpt",
				ProtocolCode:    gatewayopenai.ProtocolCode,
				ProtocolVersion: gatewayopenai.ProtocolVersion,
			},
			ModelMappings: mappings,
		}
	}
	cases := []struct {
		name                 string
		account              gatewayusage.UsageModelAccount
		requestedModel       string
		sourceEndpointFamily string
		wantUpstream         string
		wantApplied          bool
		wantSource           string
	}{
		{
			name:                 "命中映射解析为上游模型",
			account:              accountWith([]gatewayopenai.AccountModelMapping{mapping(&enabled, "")}),
			requestedModel:       "gpt-5",
			sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
			wantUpstream:         "gpt-4.1-mini",
			wantApplied:          true,
			wantSource:           "account",
		},
		{
			name:                 "命中映射携带 runtimeSource",
			account:              accountWith([]gatewayopenai.AccountModelMapping{mapping(&enabled, "route-rule")}),
			requestedModel:       "gpt-5",
			sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
			wantUpstream:         "gpt-4.1-mini",
			wantApplied:          true,
			wantSource:           "route-rule",
		},
		{
			name:                 "无映射回退请求模型",
			account:              accountWith(nil),
			requestedModel:       "gpt-5",
			sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
			wantUpstream:         "gpt-5",
			wantApplied:          false,
			wantSource:           "",
		},
		{
			name:                 "映射禁用回退请求模型",
			account:              accountWith([]gatewayopenai.AccountModelMapping{mapping(&disabled, "")}),
			requestedModel:       "gpt-5",
			sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
			wantUpstream:         "gpt-5",
			wantApplied:          false,
			wantSource:           "",
		},
		{
			name:                 "源模型未命中回退请求模型",
			account:              accountWith([]gatewayopenai.AccountModelMapping{mapping(&enabled, "")}),
			requestedModel:       "gpt-5-mini",
			sourceEndpointFamily: gatewayopenai.FamilyChatCompletions,
			wantUpstream:         "gpt-5-mini",
			wantApplied:          false,
			wantSource:           "",
		},
		{
			name:                 "family 不在映射范围回退请求模型",
			account:              accountWith([]gatewayopenai.AccountModelMapping{mapping(&enabled, "")}),
			requestedModel:       "gpt-5",
			sourceEndpointFamily: "embeddings",
			wantUpstream:         "gpt-5",
			wantApplied:          false,
			wantSource:           "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolver.ResolveUsageModel(tc.account, tc.requestedModel, tc.sourceEndpointFamily)
			if got.UpstreamModel != tc.wantUpstream {
				t.Fatalf("upstreamModel 不符：期望 %q，实际 %q", tc.wantUpstream, got.UpstreamModel)
			}
			if got.ModelMappingApplied != tc.wantApplied {
				t.Fatalf("modelMappingApplied 不符：期望 %v，实际 %v", tc.wantApplied, got.ModelMappingApplied)
			}
			if got.ModelMappingSource != tc.wantSource {
				t.Fatalf("modelMappingSource 不符：期望 %q，实际 %q", tc.wantSource, got.ModelMappingSource)
			}
			if got.SourceEndpointFamily != tc.sourceEndpointFamily {
				t.Fatalf("sourceEndpointFamily 不符：期望 %q，实际 %q", tc.sourceEndpointFamily, got.SourceEndpointFamily)
			}
			wantUpstreamFamily := tc.sourceEndpointFamily
			if tc.wantApplied {
				wantUpstreamFamily = gatewayopenai.FamilyChatCompletions
			}
			if got.UpstreamEndpointFamily != wantUpstreamFamily {
				t.Fatalf("upstreamEndpointFamily 不符：期望 %q，实际 %q", wantUpstreamFamily, got.UpstreamEndpointFamily)
			}
		})
	}
}

// TestUsageModelAccountOfCarriesMappings 钉住构造点投影：派发候选的映射行
// 必须随 UsageModelAccount.ModelMappings 进入 usage 侧（2026-09-20 前该投影
// 丢字段，解析适配器无数据可用）。
func TestUsageModelAccountOfCarriesMappings(t *testing.T) {
	runtimeSource := "route-rule"
	candidate := gatewaydispatch.AccountCandidate{
		ID:                        "acct-1",
		Name:                      "acct",
		ProviderCode:              "gpt",
		ProviderProtocolProfileID: "openai-default",
		ProtocolCode:              gatewayopenai.ProtocolCode,
		ProtocolVersion:           gatewayopenai.ProtocolVersion,
		ModelMappings: []gatewayruntimecache.AccountModelMapping{{
			SourceModel:            "gpt-5",
			SourceEndpointFamily:   gatewayopenai.FamilyChatCompletions,
			UpstreamModel:          "gpt-4.1-mini",
			UpstreamEndpointFamily: gatewayopenai.FamilyChatCompletions,
			Enabled:                true,
			RuntimeSource:          &runtimeSource,
		}},
	}
	projected := usageModelAccountOf(candidate)
	if len(projected.ModelMappings) != 1 {
		t.Fatalf("映射行必须随投影携带，实际 %d 行", len(projected.ModelMappings))
	}
	got := projected.ModelMappings[0]
	if got.SourceModel != "gpt-5" || got.UpstreamModel != "gpt-4.1-mini" ||
		got.SourceEndpointFamily != gatewayopenai.FamilyChatCompletions ||
		got.UpstreamEndpointFamily != gatewayopenai.FamilyChatCompletions {
		t.Fatalf("映射行投影字段不符：%+v", got)
	}
	if got.Enabled == nil || !*got.Enabled {
		t.Fatalf("Enabled 必须投影为启用指针：%v", got.Enabled)
	}
	if got.RuntimeSource != "route-rule" {
		t.Fatalf("runtimeSource 必须解引用携带，实际 %q", got.RuntimeSource)
	}
	// 端到端：投影账户经解析适配器必须解析出上游模型。
	resolution := usageModelResolverAdapter{}.ResolveUsageModel(
		projected, "gpt-5", gatewayopenai.FamilyChatCompletions)
	if !resolution.ModelMappingApplied || resolution.UpstreamModel != "gpt-4.1-mini" {
		t.Fatalf("投影账户解析不符：%+v", resolution)
	}
	if resolution.ModelMappingSource != "route-rule" {
		t.Fatalf("modelMappingSource 不符：期望 route-rule，实际 %q", resolution.ModelMappingSource)
	}
}
