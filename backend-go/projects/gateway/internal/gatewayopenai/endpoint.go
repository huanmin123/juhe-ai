package gatewayopenai

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayproto"
	"github.com/huanminabc/juhe-ai/backend-go-platform/upstreamenvelope"
)

// endpointFamilyFromPath mirrors openAIEndpointFamilyFromPath
// (domain/openai-endpoint-modes.ts): lowercase containment match.
func endpointFamilyFromPath(value string) (string, bool) {
	path := strings.ToLower(strings.TrimSpace(value))
	if path == "" {
		return "", false
	}
	if strings.Contains(path, "/chat/completions") {
		return FamilyChatCompletions, true
	}
	if strings.Contains(path, "/responses") {
		return FamilyResponses, true
	}
	if strings.Contains(path, "/embeddings") {
		return FamilyEmbeddings, true
	}
	if strings.Contains(path, "/rerank") {
		return FamilyRerank, true
	}
	if strings.Contains(path, "/moderations") {
		return FamilyModeration, true
	}
	return "", false
}

// responseEndpointFamilyFromPath mirrors openAIResponseEndpointFamilyFromRequest.
func responseEndpointFamilyFromPath(path string) gatewayproto.ResponseEndpointFamily {
	if strings.Contains(path, "/chat/completions") {
		return gatewayproto.EndpointFamilyChatCompletions
	}
	if strings.Contains(path, "/responses") {
		return gatewayproto.EndpointFamilyResponses
	}
	if strings.Contains(path, "/embeddings") {
		return gatewayproto.EndpointFamilyEmbedContent
	}
	if strings.Contains(path, "/rerank") {
		return gatewayproto.EndpointFamilyRerank
	}
	if strings.Contains(path, "/moderations") {
		return gatewayproto.EndpointFamilyModeration
	}
	return gatewayproto.EndpointFamilyUnknown
}

// normalizedPathWithoutVersion strips a leading "/v1" path segment
// (mirrors the Node ^\/v1(?=\/|$) replacement).
func normalizedPathWithoutVersion(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path == "/v1" {
		return "/"
	}
	if strings.HasPrefix(path, "/v1/") {
		return path[len("/v1"):]
	}
	return path
}

// SplitPathAndQuery mirrors splitPathAndQuery.
func SplitPathAndQuery(pathAndQuery string) (path, query string) {
	index := strings.Index(pathAndQuery, "?")
	if index < 0 {
		return pathAndQuery, ""
	}
	return pathAndQuery[:index], pathAndQuery[index:]
}

// IsProtocolRequestPath mirrors isOpenAIProtocolRequestPath: the request
// path belongs to the OpenAI protocol surface.
func IsProtocolRequestPath(originalPathAndQuery string) bool {
	if _, ok := endpointFamilyFromPath(originalPathAndQuery); ok {
		return true
	}
	path, _ := SplitPathAndQuery(originalPathAndQuery)
	path = strings.ToLower(strings.TrimSpace(path))
	normalized := normalizedPathWithoutVersion(path)
	switch {
	case normalized == "/models":
		return true
	case normalized == "/images", strings.HasPrefix(normalized, "/images/"):
		return true
	case normalized == "/embeddings", strings.HasPrefix(normalized, "/embeddings/"):
		return true
	case normalized == "/rerank", strings.HasPrefix(normalized, "/rerank/"):
		return true
	case normalized == "/moderations", strings.HasPrefix(normalized, "/moderations/"):
		return true
	case normalized == "/audio", strings.HasPrefix(normalized, "/audio/"):
		return true
	// M2 视频任务面（媒体设计 §4.2）：2026-11-12 New API 兼容批起对外唯一
	// 视频路径族是 /video/generations*（POST 创建、GET 轮询、GET content）
	// ——/v1 子树整棵挂在链上，协议门必须放行新路径，链入口再对旧 /videos
	// 显式硬 404；任务面列表/取消在链内按 New API 语义 404。
	case normalized == "/video/generations", strings.HasPrefix(normalized, "/video/generations/"):
		return true
	// M3f 长音频任务面同批迁到 New API 通用任务面 /tasks/audio*（创建/轮询/
	// content）；旧 /audio/jobs 族在链入口硬 404（内部词表保留供形态判定）。
	case normalized == "/tasks/audio", strings.HasPrefix(normalized, "/tasks/audio/"):
		return true
	// 旧 /videos 词表保留在协议门（内部形态判定与历史测试复用）；公开面由
	// chain 入口 chainIsLegacyVideoPath 硬 404，不对外服务。
	case normalized == "/videos", strings.HasPrefix(normalized, "/videos/"):
		return true
	// M5b realtime（Realtime 设计 §2）：ephemeral token 签发端点进协议面
	//（POST /v1/realtime/client_secrets，preauth API Key 认证后链内短路）；
	// GET /v1/realtime WS 升级面随 M5b2 WS 桥接 handler 放行（精确路径，
	// 升级请求形态——方法与子路径细分由链内 realtime 面裁决：非 GET 404、
	// client_secrets 子路径仍走签发分支）。
	case normalized == "/realtime", normalized == "/realtime/client_secrets":
		return true
	}
	return false
}

// IsModelsRequest mirrors isOpenAIModelsRequest: GET /v1/models.
func IsModelsRequest(method, originalPathAndQuery string) bool {
	if !strings.EqualFold(method, "GET") {
		return false
	}
	path, _ := SplitPathAndQuery(originalPathAndQuery)
	return normalizedPathWithoutVersion(path) == "/models"
}

// BuildUpstreamURL 按上游 Base URL 版本根语义 v2 直拼最终上游 URL：
// base_url 即版本根（如 https://ark.cn-beijing.volces.com/api/v3），直拼
// opPath（入站路径剥去可选 /v1 前缀的剩余路径 + query）。拼接规则统一收敛
// 到共享 upstreamenvelope.JoinUpstreamURL（R1：TrimRight 去尾斜杠直拼，不
// 补插、不剥离、不去重版本段——原 D-152 补 /v1 与 gemini-openai-chat 豁免
// 分支随 v2 契约删除）；base 为空或 opPath 非 / 前缀显式报错。入站 /v1
// 剥离（upstreamPathSuffix）保留在入站边界，属客户端入站语义（R5）。
func BuildUpstreamURL(baseURL, pathAndQuery string) (string, error) {
	return upstreamenvelope.JoinUpstreamURL(baseURL, upstreamPathSuffix(pathAndQuery))
}

func upstreamPathSuffix(pathAndQuery string) string {
	path, query := SplitPathAndQuery(pathAndQuery)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	pathWithoutVersion := normalizedPathWithoutVersion(path)
	if pathWithoutVersion == "/" {
		pathWithoutVersion = ""
	}
	return pathWithoutVersion + query
}

// endpointModeForShape mirrors openAIEndpointModeForRequestShape.
func endpointModeForShape(path string, stream bool) (gatewayproto.EndpointMode, bool) {
	family, ok := endpointFamilyFromPath(path)
	if !ok {
		return "", false
	}
	if family == FamilyChatCompletions {
		if stream {
			return gatewayproto.EndpointModeChatSSE, true
		}
		return gatewayproto.EndpointModeChatJSON, true
	}
	switch family {
	case FamilyEmbeddings:
		return gatewayproto.EndpointModeEmbedContent, true
	case FamilyRerank:
		return gatewayproto.EndpointModeRerankJSON, true
	case FamilyModeration:
		return gatewayproto.EndpointModeModerationJSON, true
	}
	if stream {
		return gatewayproto.EndpointModeResponsesSSE, true
	}
	return gatewayproto.EndpointModeResponsesJSON, true
}
