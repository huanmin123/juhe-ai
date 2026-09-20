package main

// gatewayusage.Service 组合根端口接线（审计缺口收口）。五个未接 With* 中
// 四个存在 Go 侧真实组件，在此适配并接线（chain_compose.go usageService
// 装配处）；一个端口已随死代码清理删除：
//
//   - WithModelResolver（2026-09-20 收口接线）：UsageModelAccount 增加
//     ModelMappings 字段（构造点 usageModelAccountOf 从派发候选的完整
//     secret 投影），usageModelResolverAdapter（chain_ports.go）据此调
//     gatewayopenai.ResolveAccountModelMapping 完成真实解析——映射后上游
//     模型名进记账，按模型取价不再吃到请求别名。
//   - WithAccountAPIKeySuccess（2026-09-20 删除）：消费点
//     Service.RecordCompletedUpstreamAttempt 在当前 Go 运行链无调用方（完成
//     尝试记账走 chainFinalizationUsage 直投 recorder），端口保持 nil 不产生
//     任何行为；真实组件 gatewayaccounteffects.AccountAPIKeyEffects 的
//     RecordSuccess 依赖 SelectedAPIKeyFingerprint 等运行态字段，端口投影
//     同样不带。端口、字段与其消费块已从 gatewayusage 删除（删除前实现见
//     git 历史）。
//
// 本文件只组合既有真实组件，不新增业务语义。

import (
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayresponse"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
)

// chainUsageDefaultProviderCode implements
// gatewayusage.DefaultUsageProviderCodeResolver: Node
// defaultGatewayUsageProviderCode() = GPT_VENDOR_CODE；Go 镜像常量
// gatewaydispatch.GPTVendorCode（gatewayupstream_bridge.go 再导出）。
type chainUsageDefaultProviderCode struct{}

func (chainUsageDefaultProviderCode) DefaultUsageProviderCode() string {
	return gatewaydispatch.GPTVendorCode
}

// chainUsageSemanticResolver implements gatewayusage.UsageSemanticResolver:
// Node usageSemanticForProfile（providers/drivers/registry.ts）的 Go 落地面，
// 与 gatewayresponse 的 usageSemanticForProviderCode 同一词汇表（钉住测试
// 对照）。registry 的 providerCode 驱动回退与 hybrid profile 级覆盖均已落地：
// anthropic/gemini 真实供应商按 providerCode 分派；hybrid（ProviderCode 归一
// 后为 "hybrid"，Go 种子两个 hybrid 档案 maintenance pg_schema 均如此）按
// 档案 ID 决定语义——anthropic-messages 档案 → anthropic、gemini-native
// 档案 → gemini、其余（openai chat 等）→ openai。档案 ID 用小写包含判断，
// 兼容 gemini-native 形态（profile_hybrid_gemini_native_v1beta，Node 存在、
// Go 种子暂未种）及未来新增档案命名。
type chainUsageSemanticResolver struct{}

func (chainUsageSemanticResolver) UsageSemanticForProfile(profile *gatewayusage.ProviderProtocolProfile) string {
	if profile != nil {
		provider := strings.ToLower(strings.TrimSpace(profile.ProviderCode))
		if provider == "hybrid" {
			// hybrid profile 级覆盖（Node registry hybrid driver 的档案分派）：
			// 档案 ID 命名含目标语义词汇，包含判断即足够；未命中保持 openai
			// 兜底（openai chat 档案与无法识别档案同路径）。
			id := strings.ToLower(profile.ProfileID)
			switch {
			case strings.Contains(id, "anthropic"):
				return "anthropic"
			case strings.Contains(id, "gemini"):
				return "gemini"
			}
			return "openai"
		}
		switch provider {
		case "anthropic":
			return "anthropic"
		case "gemini":
			return "gemini"
		}
	}
	return "openai"
}

// chainUsageProtocolErrorParser implements
// gatewayusage.ProtocolErrorPayloadParser over the three real response
// drivers（gatewayresponse ResponseDriverPort 的 ParseErrorPayload，逐协议
// 对齐 Node parseGatewayProtocolErrorPayload 的驱动分派）。端口消费端
// （gatewayusage firstStringField/stringField）经 jsonRecordField 读返回值
// 的顶层 code/type/message 键，故投影为 map（与 chain_ports.go
// usageErrorPayloadOf 同形）。
type chainUsageProtocolErrorParser struct {
	openai    gatewayresponse.ResponseDriverPort
	anthropic gatewayresponse.ResponseDriverPort
	gemini    gatewayresponse.ResponseDriverPort
}

func newChainUsageProtocolErrorParser() chainUsageProtocolErrorParser {
	return chainUsageProtocolErrorParser{
		openai:    gatewayresponse.NewOpenAIResponseDriver(),
		anthropic: gatewayresponse.NewAnthropicResponseDriver(),
		gemini:    gatewayresponse.NewGeminiResponseDriver(),
	}
}

func (p chainUsageProtocolErrorParser) ParseProtocolErrorPayload(account gatewayusage.UsageModelAccount, bodyText string, headers map[string]any) any {
	payload := p.driverFor(account).ParseErrorPayload(bodyText, chainUsageHTTPHeaderOf(headers))
	if !payload.HasEvidence() {
		// 无证据回落 nil：usage 层保持自身兜底（errorMessage 从
		// input.ErrorMessage → BodyText 逐级回落），等价 Node 驱动缺席。
		return nil
	}
	return chainUsageErrorPayloadMap(payload)
}

func (p chainUsageProtocolErrorParser) driverFor(account gatewayusage.UsageModelAccount) gatewayresponse.ResponseDriverPort {
	protocol := ""
	if account.Profile != nil {
		protocol = strings.ToLower(strings.TrimSpace(account.Profile.ProtocolCode))
	}
	switch protocol {
	case "anthropic", "anthropic_v1":
		return p.anthropic
	case "gemini", "gemini_v1beta":
		return p.gemini
	default:
		// openai / openai_v1 / 缺省 profile：openai 驱动（hybrid 账户协议面
		// 同为 openai）。
		return p.openai
	}
}

// chainUsageHTTPHeaderOf projects the usage-layer header object onto
// http.Header for the driver parsers（值形态来自 responseHeadersOf 投影：
// string 为主，宽容 []string / []any）。
func chainUsageHTTPHeaderOf(headers map[string]any) http.Header {
	out := http.Header{}
	for name, value := range headers {
		switch typed := value.(type) {
		case string:
			out.Set(name, typed)
		case []string:
			for _, item := range typed {
				out.Add(name, item)
			}
		case []any:
			for _, item := range typed {
				if text, ok := item.(string); ok {
					out.Add(name, text)
				}
			}
		}
	}
	return out
}

// chainUsageErrorPayloadMap projects the protocol ErrorPayload onto the
// map shape the usage port consumers read（仅非空键写入，等价
// usageErrorPayloadOf）。
func chainUsageErrorPayloadMap(payload gatewayproto.ErrorPayload) map[string]any {
	value := map[string]any{}
	if payload.Code != "" {
		value["code"] = payload.Code
	}
	if payload.Type != "" {
		value["type"] = payload.Type
	}
	if payload.Message != "" {
		value["message"] = payload.Message
	}
	return value
}
