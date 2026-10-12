package chat

// 主对话协议 resolver 表驱动矩阵（PLAN-20261012T121500000Z D4/D3 契约）：
// profile × 供应商 → 请求形态/上游协议/桥接；端点模式与 SupportedModels 校验；
// 映射 UpstreamEndpointFamily/UpstreamModel 覆盖；目录过滤键与 hosted 联网
// 能力判定。全部为纯函数单测（无 IO、无时钟）。

import (
	"strings"
	"testing"
)

// resolverAccount 构造最小协议解析账户。
func resolverAccount(providerCode, profileID, protocolCode, accountType string, modes ...string) ChatTransportAccount {
	return ChatTransportAccount{
		ID:                     "account-resolver",
		Type:                   accountType,
		ProviderCode:           providerCode,
		ProviderProtocolProfileID: profileID,
		ProtocolCode:              protocolCode,
		SupportedEndpointModes:    modes,
	}
}

func chatMode(protocol ChatTransportProtocol) string {
	return chatRequiredEndpointMode(protocol)
}

func TestResolveChatRequestProtocolMatrix(t *testing.T) {
	cases := []struct {
		name string
		account ChatTransportAccount
		model string
		wantRequest    ChatTransportProtocol
		wantUpstream   ChatTransportProtocol
		wantBridge     bool
		wantUpstreamModel string
	}{
		{
			name: "anthropic 原生 → responses 桥接 messages",
			account: resolverAccount("anthropic", "profile_anthropic_v1", "anthropic", "api_key", chatMode(upstreamProtocolMessages)),
			model: "claude-sonnet-4",
			wantRequest: ProtocolResponses, wantUpstream: upstreamProtocolMessages, wantBridge: true,
		},
		{
			name: "deepseek anthropic 档案 → responses 桥接 messages",
			account: resolverAccount("deepseek", "profile_deepseek_anthropic_v1", "", "api_key", chatMode(upstreamProtocolMessages)),
			model: "deepseek-chat",
			wantRequest: ProtocolResponses, wantUpstream: upstreamProtocolMessages, wantBridge: true,
		},
		{
			name: "glm anthropic 档案 → responses 桥接 messages",
			account: resolverAccount("glm", "profile_glm_coding_anthropic_v1", "", "api_key", chatMode(upstreamProtocolMessages)),
			model: "glm-4.6",
			wantRequest: ProtocolResponses, wantUpstream: upstreamProtocolMessages, wantBridge: true,
		},
		{
			name: "gemini native → chat 桥接 generate_content",
			account: resolverAccount("gemini", "profile_gemini_native_v1beta", "gemini", "api_key", chatMode(upstreamProtocolGenerateContent)),
			model: "gemini-2.5-pro",
			wantRequest: ProtocolChatCompletions, wantUpstream: upstreamProtocolGenerateContent, wantBridge: true,
		},
		{
			name: "gemini openai 兼容档案 → chat 直连",
			account: resolverAccount("gemini", "profile_gemini_openai_chat_v1beta", "gemini", "api_key", chatMode(ProtocolChatCompletions)),
			model: "gemini-2.5-flash",
			wantRequest: ProtocolChatCompletions, wantUpstream: ProtocolChatCompletions, wantBridge: false,
		},
		{
			name: "openai provider → responses 直连",
			account: resolverAccount("openai", "profile_openai_openai_v1", "openai", "api_key", chatMode(ProtocolResponses)),
			model: "gpt-5",
			wantRequest: ProtocolResponses, wantUpstream: ProtocolResponses, wantBridge: false,
		},
		{
			name: "gpt provider → responses 直连",
			account: resolverAccount("gpt", "profile_gpt_openai_v1", "", "api_key", chatMode(ProtocolResponses)),
			model: "gpt-5.6",
			wantRequest: ProtocolResponses, wantUpstream: ProtocolResponses, wantBridge: false,
		},
		{
			name: "deepseek → chat 直连",
			account: resolverAccount("deepseek", "profile_deepseek_openai_v1", "", "api_key", chatMode(ProtocolChatCompletions)),
			model: "deepseek-chat",
			wantRequest: ProtocolChatCompletions, wantUpstream: ProtocolChatCompletions, wantBridge: false,
		},
		{
			name: "glm → chat 直连",
			account: resolverAccount("glm", "profile_glm_openai_v1", "", "api_key", chatMode(ProtocolChatCompletions)),
			model: "glm-4.6",
			wantRequest: ProtocolChatCompletions, wantUpstream: ProtocolChatCompletions, wantBridge: false,
		},
		{
			name: "xai → chat 直连",
			account: resolverAccount("xai", "profile_xai_openai_v1", "", "api_key", chatMode(ProtocolChatCompletions)),
			model: "grok-4",
			wantRequest: ProtocolChatCompletions, wantUpstream: ProtocolChatCompletions, wantBridge: false,
		},
		{
			name: "volcengine → chat 直连",
			account: resolverAccount("volcengine", "profile_volcengine_openai_v1", "", "api_key", chatMode(ProtocolChatCompletions)),
			model: "doubao-pro",
			wantRequest: ProtocolChatCompletions, wantUpstream: ProtocolChatCompletions, wantBridge: false,
		},
		{
			name: "qwen → chat 直连",
			account: resolverAccount("qwen", "profile_qwen_openai_v1", "", "api_key", chatMode(ProtocolChatCompletions)),
			model: "qwen-max",
			wantRequest: ProtocolChatCompletions, wantUpstream: ProtocolChatCompletions, wantBridge: false,
		},
		{
			name: "未知档案 + OAuth → responses 兜底",
			account: resolverAccount("", "", "", "oauth", chatMode(ProtocolResponses)),
			model: "gpt-5",
			wantRequest: ProtocolResponses, wantUpstream: ProtocolResponses, wantBridge: false,
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			route, err := resolveChatRequestProtocol(item.account, item.model)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if route.RequestProtocol != item.wantRequest || route.UpstreamProtocol != item.wantUpstream ||
				route.BridgeRequired != item.wantBridge {
				t.Fatalf("route = %+v, want request=%s upstream=%s bridge=%v",
					route, item.wantRequest, item.wantUpstream, item.wantBridge)
			}
			if item.wantUpstreamModel != "" && route.UpstreamModel != item.wantUpstreamModel {
				t.Fatalf("upstreamModel = %q, want %q", route.UpstreamModel, item.wantUpstreamModel)
			}
		})
	}
}

func TestResolveChatRequestProtocolRejections(t *testing.T) {
	cases := []struct {
		name    string
		account ChatTransportAccount
		model   string
		wantErr string
	}{
		{
			name:    "未知档案 + API Key 无可判定供应商 → 显式错误",
			account: resolverAccount("unknown-provider", "profile_unknown_v1", "", "api_key", chatMode(ProtocolChatCompletions)),
			model:   "mystery-model",
			wantErr: "无法判定",
		},
		{
			name:    "端点模式缺失（deepseek 无 chat_sse）",
			account: resolverAccount("deepseek", "profile_deepseek_openai_v1", "", "api_key", chatMode(ProtocolResponses)),
			model:   "deepseek-chat",
			wantErr: "chat_sse",
		},
		{
			name: "SupportedModels 不含请求模型",
			account: func() ChatTransportAccount {
				account := resolverAccount("deepseek", "", "", "api_key", chatMode(ProtocolChatCompletions))
				account.SupportedModels = []string{"deepseek-chat"}
				return account
			}(),
			model:   "deepseek-reasoner",
			wantErr: "可用模型范围",
		},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			_, err := resolveChatRequestProtocol(item.account, item.model)
			if err == nil {
				t.Fatalf("期望失败，实际解析成功")
			}
			if !strings.Contains(err.Error(), item.wantErr) {
				t.Fatalf("error = %q, want contains %q", err.Error(), item.wantErr)
			}
		})
	}
}

func TestResolveChatRequestProtocolModelMappingOverrides(t *testing.T) {
	enabled := true

	t.Run("UpstreamEndpointFamily 覆盖档案方向（deepseek → responses 直连）", func(t *testing.T) {
		account := resolverAccount("deepseek", "profile_deepseek_openai_v1", "", "api_key", chatMode(ProtocolResponses), chatMode(ProtocolChatCompletions))
		account.ModelMappings = []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: "gpt-5", UpstreamModel: "deepseek-reasoner",
			UpstreamEndpointFamily: string(ProtocolResponses),
		}}
		route, err := resolveChatRequestProtocol(account, "gpt-5")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if route.RequestProtocol != ProtocolResponses || route.UpstreamProtocol != ProtocolResponses || route.BridgeRequired {
			t.Fatalf("route = %+v, want responses 直连", route)
		}
		if route.UpstreamModel != "deepseek-reasoner" {
			t.Fatalf("upstreamModel = %q", route.UpstreamModel)
		}
	})

	t.Run("UpstreamModel 参与 SupportedModels 收敛", func(t *testing.T) {
		account := resolverAccount("deepseek", "", "", "api_key", chatMode(ProtocolChatCompletions))
		account.SupportedModels = []string{"deepseek-chat"}
		account.ModelMappings = []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: "gpt-5", UpstreamModel: "deepseek-chat",
		}}
		route, err := resolveChatRequestProtocol(account, "gpt-5")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if route.UpstreamModel != "deepseek-chat" {
			t.Fatalf("upstreamModel = %q", route.UpstreamModel)
		}
	})

	t.Run("禁用映射不参与匹配", func(t *testing.T) {
		disabled := false
		account := resolverAccount("deepseek", "", "", "api_key", chatMode(ProtocolChatCompletions))
		account.ModelMappings = []ChatTransportModelMapping{{
			Enabled: &disabled, SourceModel: "gpt-5", UpstreamEndpointFamily: string(ProtocolResponses),
		}}
		route, err := resolveChatRequestProtocol(account, "gpt-5")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if route.RequestProtocol != ProtocolChatCompletions {
			t.Fatalf("request = %s, want chat_completions（禁用映射应回退档案方向）", route.RequestProtocol)
		}
	})
}

func TestCatalogFilterProtocol(t *testing.T) {
	t.Run("桥接账户按上游原生协议过滤", func(t *testing.T) {
		route := &chatResolvedRoute{RequestProtocol: ProtocolResponses, UpstreamProtocol: upstreamProtocolMessages, BridgeRequired: true}
		if got := route.catalogFilterProtocol(); got != upstreamProtocolMessages {
			t.Fatalf("filter = %s, want messages", got)
		}
	})
	t.Run("直连账户按请求形态过滤", func(t *testing.T) {
		route := &chatResolvedRoute{RequestProtocol: ProtocolChatCompletions, UpstreamProtocol: ProtocolChatCompletions}
		if got := route.catalogFilterProtocol(); got != ProtocolChatCompletions {
			t.Fatalf("filter = %s", got)
		}
	})
}

func TestChatAccountSupportsRequestProtocolMatrix(t *testing.T) {
	deepseek := resolverAccount("deepseek", "", "", "api_key", chatMode(ProtocolChatCompletions), chatMode(ProtocolResponses))
	if !chatAccountSupportsRequestProtocol(deepseek, "m", ProtocolChatCompletions) {
		t.Fatal("deepseek 应支持 chat 直连")
	}
	if chatAccountSupportsRequestProtocol(deepseek, "m", ProtocolResponses) {
		t.Fatal("deepseek 不应主推 responses（openai 兼容档案无该方向）")
	}
	gpt := resolverAccount("gpt", "", "", "api_key", chatMode(ProtocolResponses))
	if !chatAccountSupportsRequestProtocol(gpt, "m", ProtocolResponses) {
		t.Fatal("gpt 应支持 responses 直连")
	}
	if chatAccountSupportsRequestProtocol(gpt, "m", ProtocolChatCompletions) {
		t.Fatal("gpt 不应主推 chat_completions")
	}
}

func TestChatHostedWebSearchSupportedMatrix(t *testing.T) {
	responsesCatalog := []ProviderModelCatalogItem{{
		Model:                    "gpt-5",
		SupportedToolsByProtocol: map[string][]string{"responses": {"web_search"}, "chat_completions": {"function_calling"}},
	}}
	plainCatalog := []ProviderModelCatalogItem{{
		Model:                    "gpt-5",
		SupportedToolsByProtocol: map[string][]string{"chat_completions": {"function_calling"}},
	}}
	responsesRoute := &chatResolvedRoute{RequestProtocol: ProtocolResponses, UpstreamProtocol: ProtocolResponses}
	bridgedRoute := &chatResolvedRoute{RequestProtocol: ProtocolResponses, UpstreamProtocol: upstreamProtocolMessages, BridgeRequired: true}
	chatRoute := &chatResolvedRoute{RequestProtocol: ProtocolChatCompletions, UpstreamProtocol: ProtocolChatCompletions}

	cases := []struct {
		name string
		route *chatResolvedRoute
		catalog []ProviderModelCatalogItem
		want bool
	}{
		{"responses 直连 + 目录声明 web_search", responsesRoute, responsesCatalog, true},
		{"responses 直连 + 目录未声明", responsesRoute, plainCatalog, false},
		{"桥接账户不提供 hosted 搜索", bridgedRoute, responsesCatalog, false},
		{"chat 直连不提供 hosted 搜索", chatRoute, responsesCatalog, false},
		{"目录无该模型行", responsesRoute, nil, false},
		{"空路由", nil, responsesCatalog, false},
	}
	for _, item := range cases {
		item := item
		t.Run(item.name, func(t *testing.T) {
			if got := chatHostedWebSearchSupported(item.route, item.catalog, "gpt-5"); got != item.want {
				t.Fatalf("supported = %v, want %v", got, item.want)
			}
		})
	}
}
