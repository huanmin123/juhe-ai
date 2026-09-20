package main

// gatewayusage.Service 组合根端口接线（审计缺口收口）。五个未接 With* 中
// 三个存在 Go 侧真实组件，在此适配并接线（chain_compose.go usageService
// 装配处）；两个保持 nil 的端口与原因：
//
//   - WithModelResolver：端口入参 UsageModelAccount 是丢字段投影（不带
//     ModelMappings），真实解析组件 gatewayopenai.ResolveAccountModelMapping
//     必须读账户 modelMappings 才能工作；扩端口需改 internal/gatewayusage
//     （越出本写入域）。接「空映射解析器」与现状完全等价，属空实现充数，
//     不接。
//   - WithAccountAPIKeySuccess：消费点 Service.RecordCompletedUpstreamAttempt
//     在当前 Go 运行链无调用方（完成尝试记账走 chainFinalizationUsage 直投
//     recorder），接线不产生任何行为；且真实组件
//     gatewayaccounteffects.AccountAPIKeyEffects.RecordSuccess 依赖
//     SelectedAPIKeyFingerprint 等运行态字段，端口投影同样不带。
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
// Node usageSemanticForProfile（providers/drivers/registry.ts）的 Go 落地面。
// 注册表 profile 级覆盖（hybrid driver 的 anthropic-messages / gemini-native
// profile → anthropic/gemini）未随 Go 移植（当前项目仅启用 OpenAI 供应商，
// Go 侧无 hybrid profile 常量），落 registry 的 providerCode 驱动回退——与
// gatewayresponse 的 usageSemanticForProviderCode 同一词汇表（钉住测试对照）。
type chainUsageSemanticResolver struct{}

func (chainUsageSemanticResolver) UsageSemanticForProfile(profile *gatewayusage.ProviderProtocolProfile) string {
	if profile != nil {
		switch strings.ToLower(strings.TrimSpace(profile.ProviderCode)) {
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
