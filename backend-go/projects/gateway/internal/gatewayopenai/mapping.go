package gatewayopenai

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
)

// AccountModelMapping mirrors AccountModelMapping: an account-level model
// mapping row.
type AccountModelMapping struct {
	SourceModel            string
	SourceEndpointFamily   string
	UpstreamModel          string
	UpstreamEndpointFamily string
	// Enabled mirrors enabled !== false; nil counts as enabled.
	Enabled            *bool
	RuntimeSource      string
	RuntimeRouteRuleID string
}

// RuntimeAccount mirrors OpenAIModelMappingRuntimeAccount. 供应商解耦 B4 W2
// 收口：ProviderCode / ProviderProtocolProfileID / ProtocolCode /
// ProtocolVersion 过渡投影已随 hybrid 门、旧 profile 排除与 OpenAI 档案亲和
// 判定一并拆除（设计 §2 终态：映射 admissible 只由映射行决定，§5.2 矩阵外
// family 与 mode 合法性由写侧 validateEntryMappingFamilyLinkage 保证）；
// account 参数仅保留 nil 语义（无模型事实主体恒不可执行）。
type RuntimeAccount struct {
	ModelMappings []AccountModelMapping
}

// sourceEndpointFamilies are the families eligible for model mapping
// (isAccountModelMappingSourceEndpointFamily).
func isAccountModelMappingSourceEndpointFamily(value string) bool {
	switch value {
	case FamilyChatCompletions,
		FamilyResponses,
		FamilyAnthropicMessages,
		FamilyGeminiGenerateContent,
		FamilyGeminiStreamGenerate,
		// M4b 媒体映射族（媒体设计 §9 hybrid 行）：/v1/videos 创建形态解析
		// video_generation 源映射、/v1/audio/speech 形态解析 tts 源映射（同族
		// 模型名改写，isOpenAIModelMappingRuntimeConversionSupported 的
		// source==upstream 分支放行）。
		FamilyVideoGeneration,
		FamilyTts:
		return true
	}
	return false
}

// ResolveAccountModelMapping mirrors resolveOpenAIAccountModelMapping.
func ResolveAccountModelMapping(account *RuntimeAccount, requestedModel, sourceEndpointFamily string) *gatewayproto.ResolvedModelMapping {
	model := strings.TrimSpace(requestedModel)
	if model == "" || sourceEndpointFamily == "" {
		return nil
	}
	if !isAccountModelMappingSourceEndpointFamily(sourceEndpointFamily) {
		return nil
	}
	mappings := accountMappings(account)
	var mapping *AccountModelMapping
	for index := range mappings {
		item := &mappings[index]
		if item.Enabled != nil && !*item.Enabled {
			continue
		}
		if item.SourceModel == model && item.SourceEndpointFamily == sourceEndpointFamily {
			mapping = item
			break
		}
	}
	if mapping == nil {
		return nil
	}
	// 命中行之后的三道判定（恒等拒绝、转换矩阵，连同已满足的启用、源族
	// 可接受集）统一走 IsAdmissibleModelMapping 唯一函数源；请求级预检与
	// 循环内启用跳过保持既有早退语义不变。旧协议档案排除（Gemini
	// OpenAI-chat 档案拒绝 anthropic messages 源族）已随供应商解耦 B4 W2
	// 拆除：写侧 family 联动校验（account_entry_mappings §5.3）是替代约束。
	if !IsAdmissibleModelMapping(*mapping, account) {
		return nil
	}
	resolved := &gatewayproto.ResolvedModelMapping{
		SourceModel:            mapping.SourceModel,
		SourceEndpointFamily:   mapping.SourceEndpointFamily,
		UpstreamModel:          mapping.UpstreamModel,
		UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
		RuntimeSource:          mapping.RuntimeSource,
		RuntimeRouteRuleID:     mapping.RuntimeRouteRuleID,
	}
	return resolved
}

// IsAdmissibleModelMapping 判定单条账户模型映射行在给定账户（模型事实主体）
// 上是否为运行时可执行的映射：启用 + 源端点族在可接受集 + 非恒等改写 + 运行
// 时转换矩阵支持（网关模型列表账户并集设计 4.2）。与
// ResolveAccountModelMapping 内联的同一组判定共享唯一函数源；矩阵演化时
// 两侧自动同步，禁止在任何调用方复刻第二份判定。判定只依赖映射行，可静态
// 执行；account 为 nil 时无模型事实主体，与 ResolveAccountModelMapping
// （nil 账户解析不到任何行）保持一致返回 false。跨协议映射不再按
// provider/provider 档案排除 source family（供应商解耦 B4 W2 拆门）：映射行
// family/mode 合法性由写侧 validateEntryMappingFamilyLinkage（§5.2 矩阵 +
// §5.3 mode 交集）在保存时保证，运行时按账户声明放行。
func IsAdmissibleModelMapping(mapping AccountModelMapping, account *RuntimeAccount) bool {
	if account == nil {
		return false
	}
	if mapping.Enabled != nil && !*mapping.Enabled {
		return false
	}
	if !isAccountModelMappingSourceEndpointFamily(mapping.SourceEndpointFamily) {
		return false
	}
	if mapping.UpstreamModel == mapping.SourceModel && mapping.UpstreamEndpointFamily == mapping.SourceEndpointFamily {
		return false
	}
	return isOpenAIModelMappingRuntimeConversionSupported(&mapping, account)
}

func accountMappings(account *RuntimeAccount) []AccountModelMapping {
	if account == nil {
		return nil
	}
	return account.ModelMappings
}

// IsProtocolEquivalentEndpointFamily reports whether source / target form one of
// the 7 allowed protocol-equivalent mapping pairs: the same family, or the
// Gemini native stream→generate normalization. Cross-protocol pairs (including
// the retired responses→chat_completions bridge) return false.
func IsProtocolEquivalentEndpointFamily(source, target string) bool {
	return source == target ||
		(source == FamilyGeminiStreamGenerate && target == FamilyGeminiGenerateContent)
}

// isOpenAIModelMappingRuntimeConversionSupported 是 §5.2 7 pair 矩阵的运行时
// 投影：只放行协议等价映射（同族改名，或 Gemini
// stream_generate_content→generate_content 流式归一）。2026-11-07 跨协议模型
// 映射退役，OpenAI v1 Responses→Chat Completions bridge 放行一并删除；写侧
// accounts.entryMappingPairTargetModes 与本处由 golden
// （accounts/testdata/entry_mapping_pairs.json）锁定同一口径。
func isOpenAIModelMappingRuntimeConversionSupported(mapping *AccountModelMapping, account *RuntimeAccount) bool {
	return IsProtocolEquivalentEndpointFamily(mapping.SourceEndpointFamily, mapping.UpstreamEndpointFamily)
}

// NormalizeAnthropicFamily reports whether the token is the anthropic
// messages upstream family (either protocol name or the stored row token).
func NormalizeAnthropicFamily(family string) bool {
	return family == "anthropic_messages" || family == "messages"
}

// ModelMappedUpstreamPathAndQuery 保留作为 chain URL 构建器的映射改写挂钩
// （BUG-0178）。7 pair 协议等价矩阵下不再有任何需要改写上游路径的映射组合：
// 同族改名沿用客户端路径；Gemini stream 归一与原生 Code Assist 的路径/mode
// 改写由 chain_driver 的 gemini 原生分支
// （geminiGenerateContentModelMappedUpstreamPathAndQuery）承担。跨族 case
// （responses→chat_completions 的 /chat/completions 改写、anthropic/gemini
// 桥路径改写）随 2026-11-07 跨协议映射退役删除；即使上游传入退役组合，也
// 保持原路径返回 false，不再为其合成桥路径。
func ModelMappedUpstreamPathAndQuery(originalPathAndQuery string, _ *gatewayproto.ResolvedModelMapping) (string, bool) {
	return originalPathAndQuery, false
}
