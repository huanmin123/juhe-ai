package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayopenai"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/openaicompat/openaicompatbridge"
	"github.com/huanminabc/juhe-ai/backend-go-platform/safego"
)

// 2026-11-07 跨协议 Responses↔Chat 桥退役：本文件原 B-4 跨族请求/响应转换面
// （chat / anthropic / gemini native 客户端方向的缓冲与流式转换、chat→responses
// 回转、chainBridgeUsageSlot / carrier 的桥接 usage 快照供给链）随桥整体删除；
// 桥接流 usage 快照的唯一写入方在桥路径，gatewayresponse.HandleUpstreamResponse-
// Input.BridgeUsage 供给链一并退役。保留面只有 Gemini Code Assist（google_oauth
// code_assist/google_one）上游 {response:...} envelope 的原生解包——它不依赖
// 跨协议映射，映射命中（含协议等价改名）与 nil 映射两条路径都要执行
// （Node transformGeminiCodeAssistUpstreamResponse 在 gemini driver 链首位）。

// chainBridgeResponseTransformer implements
// gatewaydispatch.UpstreamResponseTransformer over the Gemini Code Assist
// unwrap; responses without a mapping pass through after the unwrap check.
type chainBridgeResponseTransformer struct{}

// newChainBridgeResponseTransformer builds the composition-root transformer.
func newChainBridgeResponseTransformer() *chainBridgeResponseTransformer {
	return &chainBridgeResponseTransformer{}
}

// TransformUpstreamResponseForAccount implements
// gatewaydispatch.UpstreamResponseTransformer.
func (t *chainBridgeResponseTransformer) TransformUpstreamResponseForAccount(
	input gatewaydispatch.UpstreamResponseTransformInput,
) (*gatewaydispatch.GatewayUpstreamResponse, error) {
	req := input.Req
	response := input.Response
	if req == nil || response == nil || !response.OK() || response.Body == nil {
		return response, nil
	}
	if transformed, handled, err := transformUtilityUpstreamResponse(input); handled {
		return transformed, err
	}
	mapping := chainBridgeResponseMappingOf(req, input.Account)
	return t.transformGeminiCodeAssistIfApplicable(input, mapping, response)
}

// bridgeStreamPipedResponse 把上游响应体替换为 io.Pipe：后台 goroutine 按事件
// 边界增量消费上游流并逐事件转换写入管道（Node for-await 生成器语义），下游
// 拿到的响应体在上游 EOF 之前即可读到首个转换事件。EOF 正常关写；读取错误
// （含上游断开、请求取消触发的传输错误）经 CloseWithError 传播为下游读取
// 错误（Node abort 分支的截断语义）；下游提前关闭时管道写失败，goroutine
// 退出并关闭上游体（并发槽释放 + 请求取消）。
func bridgeStreamPipedResponse(
	response *gatewaydispatch.GatewayUpstreamResponse,
	headers http.Header,
	pump func(src io.Reader, dst io.Writer) error,
) *gatewaydispatch.GatewayUpstreamResponse {
	reader, writer := io.Pipe()
	go func() {
		defer safego.Handle("juheai.chain_bridge_response.stream_pump", func(recovered any) {
			// panic 兜底：以错误关闭管道写端，避免下游读取永久阻塞。
			_ = writer.CloseWithError(fmt.Errorf("流式泵异常终止: %v", recovered))
		})
		// goroutine 独占上游体的消费责任：EOF 或任一错误都关闭上游体
		//（slotReleasingBody 的 release/cancel 幂等）。
		defer response.Body.Close()
		if err := pump(response.Body, writer); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
		_ = writer.Close()
	}()
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), headers, reader)
}

// transformGeminiCodeAssistIfApplicable unwraps the Code Assist {response:...}
// wrapper for google_oauth code_assist/google_one accounts (Node
// transformGeminiCodeAssistUpstreamResponse: 流式逐事件 unwrap、非流式收集
// 合并 text)。
func (t *chainBridgeResponseTransformer) transformGeminiCodeAssistIfApplicable(
	input gatewaydispatch.UpstreamResponseTransformInput,
	mapping *gatewayproto.ResolvedModelMapping,
	response *gatewaydispatch.GatewayUpstreamResponse,
) (*gatewaydispatch.GatewayUpstreamResponse, error) {
	if !geminiAccountUsesCodeAssistRuntime(input.Account) || !isGeminiCodeAssistGenerationRequest(input.Req) {
		return response, nil
	}
	stream := geminiCodeAssistDownstreamStreamForMapping(input.Req, mapping)
	// 流式 unwrap 走增量管道（Node 流式分支逐事件 unwrap）：事件边界到达即
	// 解包转发，上游未 EOF 时下游已可读到首个解包事件；非流式收集合并 text
	// 保持 buffer 语义。
	if stream {
		return bridgeStreamPipedResponse(response, response.Header.Clone(), openaicompatbridge.PumpUnwrapGeminiCodeAssistSse), nil
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	_ = response.Body.Close()
	// stream 已在上方管道分支 return（明显不可达守卫，w14a 删除）；此处恒为
	// 非流式收集合并。
	transformed := openaicompatbridge.CollectGeminiCodeAssistSseBuffer(body)
	if !strings.Contains(strings.ToLower(response.ContentType()), "json") {
		// The non-stream collection consumes the SSE stream into one JSON
		// payload (Node sets the JSON content type).
		headers := response.Header.Clone()
		headers.Set("Content-Type", "application/json; charset=utf-8")
		headers.Del("Content-Length")
		return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), headers, io.NopCloser(strings.NewReader(string(transformed)))), nil
	}
	return gatewaydispatch.NewGatewayUpstreamResponseForTransform(response.Status(), response.Header.Clone(), io.NopCloser(strings.NewReader(string(transformed)))), nil
}

// geminiCodeAssistDownstreamStreamForMapping mirrors geminiCodeAssistDownstreamStream:
// 协议等价映射（同族改名，含 Gemini stream 归一）里 openai 族 source 跟随
// 客户端 stream 标志，否则按 gemini streamGenerateContent 路径形态判定。
func geminiCodeAssistDownstreamStreamForMapping(req *gatewaypreauth.GatewayRequest, mapping *gatewayproto.ResolvedModelMapping) bool {
	if mapping != nil {
		source := openaicompatbridge.NormalizeEndpointFamily(mapping.SourceEndpointFamily)
		if source == openaicompatbridge.FamilyChatCompletions || source == openaicompatbridge.FamilyResponses || source == openaicompatbridge.FamilyAnthropicMessages {
			return gatewaypreauth.RequestStream(req)
		}
	}
	return isGeminiStreamGenerateContentRequestPath(req)
}

func isGeminiStreamGenerateContentRequestPath(req *gatewaypreauth.GatewayRequest) bool {
	if req == nil {
		return false
	}
	return strings.Contains(gatewaypreauth.RequestPathWithoutQuery(req), ":streamGenerateContent")
}

// chainBridgeResponseMappingOf resolves the account mapping for the request
// through the same resolver the request side uses（协议等价映射 + Gemini
// Code Assist 响应解包共用）。
func chainBridgeResponseMappingOf(req *gatewaypreauth.GatewayRequest, account gatewaydispatch.AccountCandidate) *gatewayproto.ResolvedModelMapping {
	requestedModel := ""
	if model, ok := gatewaypreauth.RequestModel(req); ok {
		requestedModel = model
	}
	if requestedModel == "" {
		return nil
	}
	mappings := make([]gatewayopenai.AccountModelMapping, 0, len(account.ModelMappings))
	for _, mapping := range account.ModelMappings {
		enabled := mapping.Enabled
		runtimeSource := ""
		if mapping.RuntimeSource != nil {
			runtimeSource = *mapping.RuntimeSource
		}
		runtimeRouteRuleID := ""
		if mapping.RuntimeRouteRuleID != nil {
			runtimeRouteRuleID = *mapping.RuntimeRouteRuleID
		}
		mappings = append(mappings, gatewayopenai.AccountModelMapping{
			SourceModel:            mapping.SourceModel,
			SourceEndpointFamily:   mapping.SourceEndpointFamily,
			UpstreamModel:          mapping.UpstreamModel,
			UpstreamEndpointFamily: mapping.UpstreamEndpointFamily,
			Enabled:                &enabled,
			RuntimeSource:          runtimeSource,
			RuntimeRouteRuleID:     runtimeRouteRuleID,
		})
	}
	runtimeAccount := &gatewayopenai.RuntimeAccount{
		ModelMappings: mappings,
	}
	resolved := gatewayopenai.ResolveAccountModelMapping(runtimeAccount, requestedModel, requestMappingSourceFamilyOf(req))
	if resolved == nil {
		return nil
	}
	return &gatewayproto.ResolvedModelMapping{
		SourceModel:            resolved.SourceModel,
		SourceEndpointFamily:   resolved.SourceEndpointFamily,
		UpstreamModel:          resolved.UpstreamModel,
		UpstreamEndpointFamily: resolved.UpstreamEndpointFamily,
		RuntimeSource:          resolved.RuntimeSource,
		RuntimeRouteRuleID:     resolved.RuntimeRouteRuleID,
	}
}
