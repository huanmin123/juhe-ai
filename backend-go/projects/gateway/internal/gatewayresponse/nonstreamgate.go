package gatewayresponse

import (
	"mime"
)

// 非流式检查门判定，对齐 finalization.ts:944-949 的 inspectJsonResponse 组成
// 部分：isOpenAIJsonResponseContentType 与 shouldBufferNonStreamJsonResponse
//（finalization.ts:1485-1504）。独立成文件承载 D-108 的缓冲进入条件。

// isOpenAIJSONResponseContentType 对齐 isOpenAIJsonResponseContentType：OpenAI
// 兼容 JSON 响应媒体类型。归档 responses.ts 已裁剪，按 media type 保守重建：
// 主类型 application/json（允许 charset 等参数）。
func isOpenAIJSONResponseContentType(contentType string) bool {
	return mediaTypeOfContentType(contentType) == "application/json"
}

// mediaTypeOfContentType 解析 Content-Type 的主类型；解析失败返回空串。
func mediaTypeOfContentType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return mediaType
}

// shouldBufferNonStreamJSONResponse 对齐 shouldBufferNonStreamJsonResponse：
// 运行时检查策略非空，或客户端允许上游语义解释且期望 codex compaction 时，
// 协议校验关闭的 2xx JSON 也需要整体缓冲（供策略/语义检查后发送）。
// 归档中的 hybridRoute 质量检查与 Gemini interaction create 条件依赖的编排
// 字段尚未迁移到 Go 输入面，此处保持缺席。
func shouldBufferNonStreamJSONResponse(input HandleUpstreamResponseInput) bool {
	if len(input.effectiveInspectionPolicies()) > 0 {
		return true
	}
	if input.ClientStrategy == nil {
		return false
	}
	return input.ClientStrategy.InterpretSemantics && input.ClientStrategy.CodexCompactionExpected
}
