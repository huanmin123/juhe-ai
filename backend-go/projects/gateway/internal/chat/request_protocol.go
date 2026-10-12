package chat

import (
	"errors"
	"strings"
)

// 主对话协议解析（PLAN-20261012T121500000Z D4「协议按供应商主推直连」）：
// 废除 stream_route 的 chat_completions 硬编码，按绑定账户真实 protocol
// profile、模型映射与 SupportedEndpointModes 解析请求形态。主轮、模型列表、
// 模型能力、压缩与图片观察共用这一份实现（单一实现，不按调用方分叉）。
//
// 解析方向（D4 表 + 档案词表 accountscore/endpointmodes.go）：
//
//	profile 含 anthropic（anthropic 原生 + deepseek/glm anthropic 档案）→
//	    请求形态 responses，经网关桥接 responses→messages；
//	gemini native 档案 → 请求形态 chat_completions，经桥接 chat→generate_content；
//	gemini openai 兼容档案（profile_gemini_openai_chat_v1beta）→ chat_completions 直连；
//	openai / gpt（含 codex OAuth 与 API Key）→ responses 直连；
//	deepseek / glm / xai / minimax / volcengine / qwen 等 OpenAI 兼容档案 →
//	    chat_completions 直连；
//	未知/空档案按 provider code 上表方向兜底，仍无法判定 → 明确错误不猜测。

const (
	upstreamProtocolMessages        ChatTransportProtocol = "messages"
	upstreamProtocolGenerateContent ChatTransportProtocol = "generate_content"
)

// chatResolvedRoute 是一次协议解析结果：RequestProtocol 是我方发给 /v1 链的
// 请求形态（决定请求体构造与 SSE 消费器），UpstreamProtocol 是绑定账户的原生
// 上游协议（决定模型目录过滤键），BridgeRequired 表示两者不同（经既有桥接）。
type chatResolvedRoute struct {
	RequestProtocol  ChatTransportProtocol
	UpstreamProtocol ChatTransportProtocol
	BridgeRequired   bool
	UpstreamModel    string
}

// chatAccountNativeModelFilterKey 返回模型目录过滤使用的原生协议键：直连时即
// 请求形态；桥接账户按上游原生协议判定目录声明（Claude 目录声明 messages 即可，
// 不要求它声明 responses；Gemini native 声明 generate_content）。
func (r *chatResolvedRoute) catalogFilterProtocol() ChatTransportProtocol {
	if r.BridgeRequired {
		return r.UpstreamProtocol
	}
	return r.RequestProtocol
}

// chatRequiredEndpointMode 把上游协议映射到账户必须声明的端点模式
// （responses→responses_sse、chat→chat_sse、messages→messages_sse、
// generate_content→generate_content_sse）。
func chatRequiredEndpointMode(protocol ChatTransportProtocol) string {
	switch protocol {
	case ProtocolResponses:
		return "responses_sse"
	case ProtocolChatCompletions:
		return "chat_sse"
	case upstreamProtocolMessages:
		return "messages_sse"
	case upstreamProtocolGenerateContent:
		return "generate_content_sse"
	}
	return ""
}

// chatUpstreamProtocolForAccount 从账户真实档案推导原生上游协议。
func chatUpstreamProtocolForAccount(account ChatTransportAccount) (ChatTransportProtocol, error) {
	profile := strings.ToLower(strings.TrimSpace(account.ProviderProtocolProfileID))
	protocolCode := normalizeProviderToken(account.ProtocolCode)
	providerCode := normalizeProviderToken(account.ProviderCode)
	switch {
	case protocolCode == "anthropic" || strings.Contains(profile, "anthropic"):
		return upstreamProtocolMessages, nil
	case protocolCode == "gemini" && !strings.Contains(profile, "openai"):
		return upstreamProtocolGenerateContent, nil
	case strings.Contains(profile, "gemini"):
		// gemini 的 openai 兼容档案（profile_gemini_openai_chat_v1beta）：
		// chat_completions 直连。
		return ProtocolChatCompletions, nil
	}
	// openai 协议族按供应商主推方向（D4 表）。
	switch providerCode {
	case "openai", "gpt":
		return ProtocolResponses, nil
	case "deepseek", "glm", "xai", "minimax", "volcengine", "qwen", "hybrid":
		return ProtocolChatCompletions, nil
	}
	// 未知/空档案按账户接入方式兜底：OAuth 账户（codex）端点模式为
	// Responses-only，主推 responses；仍无法判定显式失败。
	if account.Type == "oauth" {
		return ProtocolResponses, nil
	}
	return "", errors.New("无法判定绑定账户的供应商协议档案，请检查账户协议配置")
}

// chatRequestProtocolForUpstream 把上游协议映射为合法主推请求形态：messages
// 走 responses 桥接、generate_content 走 chat 桥接；responses/chat 直连。
func chatRequestProtocolForUpstream(upstream ChatTransportProtocol) (ChatTransportProtocol, bool) {
	switch upstream {
	case upstreamProtocolMessages:
		return ProtocolResponses, true
	case upstreamProtocolGenerateContent:
		return ProtocolChatCompletions, true
	case ProtocolResponses:
		return ProtocolResponses, false
	case ProtocolChatCompletions:
		return ProtocolChatCompletions, false
	}
	return "", false
}

// chatAccountModelMapping 取账户上对 source model 生效的模型映射（第一个启用
// 项；与既有 chatTransportAccountSupportsProtocol 的映射选择同序）。
func chatAccountModelMapping(account ChatTransportAccount, model string) *ChatTransportModelMapping {
	for index := range account.ModelMappings {
		item := &account.ModelMappings[index]
		if item.Enabled != nil && !*item.Enabled {
			continue
		}
		if item.SourceModel == model {
			return item
		}
	}
	return nil
}

// resolveChatRequestProtocol 解析绑定账户在指定模型上的主推请求形态：
//   - 先以账户实际 profile（或映射 UpstreamEndpointFamily）确定原生上游协议；
//   - 再选择合法主推请求形态并校验 SupportedEndpointModes 与 SupportedModels
//     （不匹配返回明确稳定错误，不猜测 fallback）。
func resolveChatRequestProtocol(account ChatTransportAccount, model string) (*chatResolvedRoute, error) {
	upstream := ChatTransportProtocol("")
	mapping := chatAccountModelMapping(account, model)
	if mapping != nil && mapping.UpstreamEndpointFamily != "" {
		upstream = ChatTransportProtocol(mapping.UpstreamEndpointFamily)
	} else {
		resolved, err := chatUpstreamProtocolForAccount(account)
		if err != nil {
			return nil, err
		}
		upstream = resolved
	}
	request, bridged := chatRequestProtocolForUpstream(upstream)
	if request == "" {
		return nil, errors.New("无法判定绑定账户的主推协议，请检查账户协议配置")
	}
	requiredMode := chatRequiredEndpointMode(upstream)
	modeSupported := false
	for _, mode := range account.SupportedEndpointModes {
		if mode == requiredMode {
			modeSupported = true
			break
		}
	}
	if !modeSupported {
		return nil, errors.New("绑定账户端点模式不支持解析出的上游协议（缺少 " + requiredMode + "），请检查账户端点配置")
	}
	resolvedModel := model
	if mapping != nil && mapping.UpstreamModel != "" {
		resolvedModel = mapping.UpstreamModel
	}
	// SupportedModels 收敛（与既有 chatTransportAccountSupportsProtocol 同型）：
	// 判定键是路由后的上游模型。
	if len(account.SupportedModels) > 0 {
		found := false
		for _, candidate := range account.SupportedModels {
			if candidate == resolvedModel {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("模型不在绑定账户的可用模型范围内，请切换模型或检查账户模型配置")
		}
	}
	return &chatResolvedRoute{
		RequestProtocol:  request,
		UpstreamProtocol: upstream,
		BridgeRequired:   bridged,
		UpstreamModel:    resolvedModel,
	}, nil
}

// chatRouteAccountForModel 在收敛后的单账户视图上解析主推路由：账户与模型
// 逐个解析，第一个成功项即返回（会话绑定收敛为单账户，多元素仅为兼容余量）。
func chatRouteAccountForModel(accounts []ChatTransportAccount, model string) (*ChatTransportAccount, *chatResolvedRoute, error) {
	var lastErr error = errors.New("当前 API Key 没有可用于该模型的对话路由，请切换模型或检查账户映射")
	for index := range accounts {
		route, err := resolveChatRequestProtocol(accounts[index], model)
		if err != nil {
			lastErr = err
			continue
		}
		return &accounts[index], route, nil
	}
	return nil, nil, lastErr
}

// chatScopeRequestProtocol 是 chatRouteAccountForModel 的只取形态（不需要
// 账户本体时使用）。
func chatScopeRequestProtocol(accounts []ChatTransportAccount, model string) (*chatResolvedRoute, error) {
	_, route, err := chatRouteAccountForModel(accounts, model)
	return route, err
}

// chatAccountSupportsRequestProtocol 报告账户是否可以以指定请求形态路由该
// 模型（模型列表/能力过滤口径）：解析成功且请求形态一致即支持。
func chatAccountSupportsRequestProtocol(account ChatTransportAccount, model string, protocol ChatTransportProtocol) bool {
	route, err := resolveChatRequestProtocol(account, model)
	if err != nil {
		return false
	}
	return route.RequestProtocol == protocol
}

// chatHostedWebSearchSupported 判定绑定路由是否可用上游 hosted web_search
//（D3）：仅原生 Responses 直连账户且目录在该模型行声明
// supportedToolsByProtocol["responses"] 含 web_search。桥接账户对 hosted 工具
// 按 guidance 降级不真实执行，不提供该能力。
func chatHostedWebSearchSupported(route *chatResolvedRoute, catalog []ProviderModelCatalogItem, model string) bool {
	if route == nil || route.RequestProtocol != ProtocolResponses || route.BridgeRequired {
		return false
	}
	for _, item := range catalog {
		if item.Model != model {
			continue
		}
		if containsString(item.SupportedToolsByProtocol[string(ProtocolResponses)], "web_search") {
			return true
		}
	}
	return false
}
