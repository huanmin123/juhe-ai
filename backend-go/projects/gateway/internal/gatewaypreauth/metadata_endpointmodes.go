package gatewaypreauth

import (
	"strings"
)

// SupportedEndpointModes hunk (BUG-0175 D-155). Ports the request-side half of
// domain/openai-endpoint-modes.ts + anthropic/gemini endpoint-mode families:
// the AccountSupportedEndpointMode vocabulary token the current request shape
// requires. Dispatch candidate filtering consumes it against
// account.SupportedEndpointModes (zero consumption before this hunk).

// Account endpoint mode vocabulary (domain/types.ts AccountSupportedEndpointMode).
// M1 同步音频新增 audio_speech（POST /v1/audio/speech）与
// audio_transcription_json（POST /v1/audio/transcriptions|translations）两个
// token（音频设计 §11.6）；M2 视频新增 video_create（POST
// /v1/video/generations）、video_get（GET /v1/video/generations 与
// /v1/video/generations/{id}）、video_content（GET .../{id}/content）、
// video_cancel（DELETE .../{id}）四个
// token（媒体设计 §11.6；任务面端点不设候选过滤 mode，走账户亲和直连，
// 词表仅为能力表达与健康检查形态）；M3f 长音频任务族新增 audio_job_create
// （POST /v1/audio/jobs，候选过滤消费面——按本 token 过滤账户）、audio_job_get
// （GET /v1/audio/jobs 与 /v1/audio/jobs/{id}）、audio_job_content
// （GET /v1/audio/jobs/{id}/content）、audio_job_cancel（DELETE
// /v1/audio/jobs/{id}）四个 token（媒体设计 §4.2/§11.6，契约 §10.2；任务面
// 三 token 同视频族不设候选过滤）；M5b realtime 增 realtime_session（GET
// /v1/realtime WS 升级请求，Realtime 设计 §6——WS 桥接 handler 未交付前
// 词表先行，候选过滤消费面随 handler 生效）；词表与
// accounts.health_check_endpoint_mode CHECK 同步（maintenance
// pg_schema_business_tables.go）。
const (
	EndpointModeImagesJSON             = "images_json"
	EndpointModeChatJSON               = "chat_json"
	EndpointModeChatSSE                = "chat_sse"
	EndpointModeResponsesJSON          = "responses_json"
	EndpointModeResponsesSSE           = "responses_sse"
	EndpointModeMessagesJSON           = "messages_json"
	EndpointModeMessagesSSE            = "messages_sse"
	EndpointModeMessageTokenCounting   = "message_token_counting"
	EndpointModeGenerateContentJSON    = "generate_content_json"
	EndpointModeGenerateContentSSE     = "generate_content_sse"
	EndpointModeCountTokens            = "count_tokens"
	EndpointModeEmbedContent           = "embed_content"
	EndpointModeInteractionsJSON       = "interactions_json"
	EndpointModeInteractionsSSE        = "interactions_sse"
	EndpointModeAudioSpeech            = "audio_speech"
	EndpointModeAudioTranscriptionJSON = "audio_transcription_json"
	EndpointModeVideoCreate            = "video_create"
	EndpointModeVideoGet               = "video_get"
	EndpointModeVideoContent           = "video_content"
	EndpointModeVideoCancel            = "video_cancel"
	EndpointModeAudioJobCreate         = "audio_job_create"
	EndpointModeAudioJobGet            = "audio_job_get"
	EndpointModeAudioJobContent        = "audio_job_content"
	EndpointModeAudioJobCancel         = "audio_job_cancel"
	EndpointModeRealtimeSession        = "realtime_session"
	// 2026-11-20 协议补齐批（《对外协议面对齐 new-api 设计》§4.4/§4.5）：
	// rerank_json 承载 POST /v1/rerank（Cohere/Jina 形态，候选过滤消费面）；
	// embeddings_json 承载 POST /v1/embeddings（OpenAI Embeddings 规范面，
	// 精确路径；/v1/embeddings/multimodal 是方舟私有子路径，维持透传不闸门）。
	EndpointModeRerankJSON     = "rerank_json"
	EndpointModeEmbeddingsJSON = "embeddings_json"
)

// RequestSupportedEndpointMode mirrors openAIEndpointModeForGatewayRequest +
// the per-protocol request-shape mappers: the endpoint-mode token the request
// would exercise on a native account of its protocol family. An empty result
// means the request shape carries no gated mode (the caller must not filter).
func RequestSupportedEndpointMode(req *GatewayRequest) string {
	if req == nil {
		return ""
	}
	method := req.MethodUpper()
	// 协议门与链守卫按大小写不敏感匹配路径；mode 映射必须使用同一归一，
	// 否则 /v1/Embeddings、/v1/RERANK 会通过入口却在候选过滤时丢失 mode。
	path := strings.ToLower(RequestPathWithoutQuery(req))
	stream := RequestStream(req)
	switch {
	case method == "POST" && strings.Contains(path, "/chat/completions"):
		if stream {
			return EndpointModeChatSSE
		}
		return EndpointModeChatJSON
	case method == "POST" && normalizedV1StrippedPath(path) == "/responses":
		if stream {
			return EndpointModeResponsesSSE
		}
		return EndpointModeResponsesJSON
	case method == "POST" && (normalizedV1StrippedPath(path) == "/images" || strings.HasPrefix(normalizedV1StrippedPath(path), "/images/")):
		return EndpointModeImagesJSON
	case method == "POST" && normalizedV1StrippedPath(path) == "/audio/speech":
		return EndpointModeAudioSpeech
	case method == "POST" && (normalizedV1StrippedPath(path) == "/audio/transcriptions" || normalizedV1StrippedPath(path) == "/audio/translations"):
		return EndpointModeAudioTranscriptionJSON
	case method == "POST" && normalizedV1StrippedPath(path) == "/embeddings":
		return EndpointModeEmbeddingsJSON
	case method == "POST" && normalizedV1StrippedPath(path) == "/rerank":
		return EndpointModeRerankJSON
	case method == "POST" && normalizedV1StrippedPath(path) == "/video/generations":
		return EndpointModeVideoCreate
	case method == "GET" && videoGetEndpointMode(path) != "":
		return videoGetEndpointMode(path)
	case method == "DELETE" && strings.HasPrefix(normalizedV1StrippedPath(path), "/video/generations/"):
		return EndpointModeVideoCancel
	case method == "POST" && normalizedV1StrippedPath(path) == "/audio/jobs":
		return EndpointModeAudioJobCreate
	case method == "GET" && audioJobGetEndpointMode(path) != "":
		return audioJobGetEndpointMode(path)
	case method == "DELETE" && strings.HasPrefix(normalizedV1StrippedPath(path), "/audio/jobs/"):
		return EndpointModeAudioJobCancel
	case method == "GET" && normalizedV1StrippedPath(path) == "/realtime":
		return EndpointModeRealtimeSession
	case method == "POST" && normalizedV1StrippedPath(path) == "/messages":
		if stream {
			return EndpointModeMessagesSSE
		}
		return EndpointModeMessagesJSON
	case method == "POST" && strings.HasSuffix(path, "/messages/count_tokens"):
		return EndpointModeMessageTokenCounting
	case method == "POST" && strings.Contains(path, ":generatecontent"):
		if stream {
			return EndpointModeGenerateContentSSE
		}
		return EndpointModeGenerateContentJSON
	case method == "POST" && strings.Contains(path, ":streamgeneratecontent"):
		return EndpointModeGenerateContentSSE
	case strings.Contains(path, ":counttokens"):
		return EndpointModeCountTokens
	case method == "POST" && strings.Contains(path, ":embedcontent"):
		return EndpointModeEmbedContent
	case method == "POST" && strings.Contains(path, "/interactions"):
		if stream {
			return EndpointModeInteractionsSSE
		}
		return EndpointModeInteractionsJSON
	default:
		return ""
	}
}

// RequestPathWithoutQuery returns the request path with the query string and
// any /v1-style protocol prefix preserved for suffix matching.
func RequestPathWithoutQuery(req *GatewayRequest) string {
	pathAndQuery := req.PathAndQuery()
	if pathAndQuery == "" {
		pathAndQuery = req.Path()
	}
	path := strings.SplitN(pathAndQuery, "?", 2)[0]
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

// normalizedV1StrippedPath strips a leading "/v1" or "/v1beta" protocol
// version segment (mirrors the Node ^\/v1(?=\/|$) replacement used by the
// endpoint family helpers).
func normalizedV1StrippedPath(path string) string {
	for _, prefix := range []string{"/v1/", "/v1beta/"} {
		if strings.HasPrefix(path, prefix) {
			return path[len(prefix)-1:]
		}
	}
	if path == "/v1" || path == "/v1beta" {
		return "/"
	}
	return path
}

// videoGetEndpointMode maps the GET /v1/video/generations* family（设计
// 《对外协议面对齐 new-api 设计》§4.2）：列表（GET /v1/video/generations）与
// 轮询（GET .../{task_id}）是 video_get，产物下载（GET .../{task_id}/content）
// 是 video_content。非本族路径返回空串（调用方不进入 video 判定）。
func videoGetEndpointMode(path string) string {
	stripped := normalizedV1StrippedPath(path)
	if stripped == "/video/generations" {
		return EndpointModeVideoGet
	}
	if !strings.HasPrefix(stripped, "/video/generations/") {
		return ""
	}
	if strings.HasSuffix(stripped, "/content") {
		return EndpointModeVideoContent
	}
	return EndpointModeVideoGet
}

// audioJobGetEndpointMode maps the GET /v1/audio/jobs* family（M3f 长音频，
// 媒体设计 §4.2/§11.6，契约 §10.2）：列表（GET /v1/audio/jobs）与轮询
// （GET /v1/audio/jobs/{id}）是 audio_job_get，产物下载
// （GET /v1/audio/jobs/{id}/content，转写结果 JSON）是 audio_job_content。
// 非本族路径返回空串。
func audioJobGetEndpointMode(path string) string {
	stripped := normalizedV1StrippedPath(path)
	if stripped == "/audio/jobs" {
		return EndpointModeAudioJobGet
	}
	if !strings.HasPrefix(stripped, "/audio/jobs/") {
		return ""
	}
	if strings.HasSuffix(stripped, "/content") {
		return EndpointModeAudioJobContent
	}
	return EndpointModeAudioJobGet
}
