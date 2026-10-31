package main

// 演练场派发与目录/传输投影类型（原 internal/chat 同名类型随 my-chat 会话面
// 退役迁入 cmd 包本地，字段与 JSON tag 保持逐字一致）：
//
//   - GenerationExecutor / GenerationDispatchRequest / GenerationDispatchResponse
//     是 /v1 派发端口（chatGatewayExecutor 的 Dispatch 契约；演练场固定派发
//     适配器与调度覆盖通道消费）。
//   - ChatTransportAccount / ChatTransportModelMapping 是 runtime cache 账户
//     快照到传输视图的投影（chat-pinned 直取端口返回形状）。
//   - ProviderModelCatalogItem / ChatGenerationParameterCapability 是供应商
//     模型目录条目的传输小结构（与 gatewayruntimecache 大结构字段 tag 同形，
//     纯 JSON 数据往返投影）。
//
// 这些类型是纯数据镜像，不承载任何 my-chat 会话语义；internal/chat 包已随
// AI 问答 → 演练场退役（docs/functions/演练场设计.md §8）整包删除。

import (
	"context"
	"io"
	"net/http"
)

// GenerationExecutor 是上游派发端口（进程内直驱 /v1 链 handler；演练场固定
// 派发与内部身份通道经该端口进入网关链）。组合根绑定生产实现，测试用 mock。
type GenerationExecutor interface {
	Dispatch(ctx context.Context, req GenerationDispatchRequest) (*GenerationDispatchResponse, error)
}

// GenerationDispatchRequest 携带一次派发的 RequestInit 形状输入。
type GenerationDispatchRequest struct {
	Path    string
	Method  string
	Headers map[string]string
	Body    []byte
}

// GenerationDispatchResponse 是上游响应的被消费子集：状态行加（流式）响应体。
type GenerationDispatchResponse struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// ChatTransportAccount 是传输选型消费的运行时账户快照子集。
type ChatTransportAccount struct {
	ID                     string                      `json:"id"`
	Type                   string                      `json:"type"`
	ProviderCode           string                      `json:"providerCode"`
	SupportedEndpointModes []string                    `json:"supportedEndpointModes,omitempty"`
	SupportedModels        []string                    `json:"supportedModels,omitempty"`
	ModelMappings          []ChatTransportModelMapping `json:"modelMappings,omitempty"`
}

// ChatTransportModelMapping 是账户模型映射子集。
type ChatTransportModelMapping struct {
	Enabled                *bool  `json:"enabled,omitempty"`
	SourceModel            string `json:"sourceModel"`
	UpstreamModel          string `json:"upstreamModel,omitempty"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily,omitempty"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily,omitempty"`
}

// ChatGenerationParameterCapability 是生成参数能力域条目。
type ChatGenerationParameterCapability struct {
	Parameter    string  `json:"parameter"`
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	Step         float64 `json:"step"`
	DefaultValue float64 `json:"defaultValue"`
}

// ProviderModelCatalogItem 是供应商模型目录条目的传输小结构。
type ProviderModelCatalogItem struct {
	Model                     string   `json:"model"`
	ProviderCode              string   `json:"providerCode"`
	SupportsPromptCaching     *bool    `json:"supportsPromptCaching,omitempty"`
	SupportedReasoningEfforts []string `json:"supportedReasoningEfforts,omitempty"`
	DefaultReasoningEffort    *string  `json:"defaultReasoningEffort,omitempty"`
	SupportedServiceTiers     []string `json:"supportedServiceTiers,omitempty"`
	ContextWindowTokens       *int64   `json:"contextWindowTokens,omitempty"`
	MaxInputTokens            *int64   `json:"maxInputTokens,omitempty"`
	MaxOutputTokens           *int64   `json:"maxOutputTokens,omitempty"`
	SupportedAPIProtocols     []string `json:"supportedApiProtocols,omitempty"`
	InputModalities           []string `json:"inputModalities,omitempty"`
	OutputModalities          []string `json:"outputModalities,omitempty"`
	// SupportedToolsByProtocol 是「协议 × 工具」矩阵（键为协议枚举、值为该协议
	// 下可用工具集）。一维 SupportedTools 已退场（工具体系设计 6.4/§2.10）。
	SupportedToolsByProtocol map[string][]string `json:"supportedToolsByProtocol,omitempty"`
	// GenerationParameterCapabilities 镜像生成参数能力域（BUG-0175 D-185）。
	// Nil 行按 providerCode/model/maxOutputTokens 派生同一张表，与归档目录
	// 快照装配完全一致。
	GenerationParameterCapabilities map[string][]ChatGenerationParameterCapability `json:"generationParameterCapabilities,omitempty"`
}
