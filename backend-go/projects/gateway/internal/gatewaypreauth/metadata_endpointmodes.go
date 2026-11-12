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
// token（音频设计 §11.6）；M2 视频新增 video_create（POST /v1/videos）、
// video_get（GET /v1/videos 与 /v1/videos/{id}）、video_content
// （GET /v1/videos/{id}/content）、video_cancel（DELETE /v1/videos/{id}）四个
// token（媒体设计 §11.6；任务面端点不设候选过滤 mode，走账户亲和直连，
// 词表仅为能力表达与健康检查形态）；M3f 长音频任务族新增 audio_job_create
// （POST /v1/tasks/audio，候选过滤消费面——按本 token 过滤账户）、audio_job_get
// （GET /v1/tasks/audio/{id}）、audio_job_content
// （GET /v1/tasks/audio/{id}/content）、audio_job_cancel（旧词表为
// DELETE /v1/audio/jobs/{id}；2026-11-12 New API 通用任务面批起对外只注册
// /tasks/audio*，列表/取消面 404，token 保留为能力表达）四个 token（媒体设计
// §4.2/§11.6，三 token 同视频族不设候选过滤）；M5b realtime 增
// realtime_session（GET /v1/realtime WS 升级请求，Realtime 设计 §6——WS 桥接
// handler 未交付前词表先行，候选过滤消费面随 handler 生效）；词表与
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
	EndpointModeRerankJSON             = "rerank_json"
	EndpointModeModerationJSON         = "moderation_json"
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
	path := RequestPathWithoutQuery(req)
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
	case method == "POST" && pathInEndpointFamily(normalizedV1StrippedPath(path), "/embeddings"):
		return EndpointModeEmbedContent
	case method == "POST" && pathInEndpointFamily(normalizedV1StrippedPath(path), "/rerank"):
		return EndpointModeRerankJSON
	case method == "POST" && pathInEndpointFamily(normalizedV1StrippedPath(path), "/moderations"):
		return EndpointModeModerationJSON
	case method == "POST" && (normalizedV1StrippedPath(path) == "/audio/transcriptions" || normalizedV1StrippedPath(path) == "/audio/translations"):
		return EndpointModeAudioTranscriptionJSON
	case method == "POST" && normalizedVideoEndpointPath(path) == "/videos":
		return EndpointModeVideoCreate
	case method == "GET" && videoGetEndpointMode(path) != "":
		return videoGetEndpointMode(path)
	case method == "DELETE" && strings.HasPrefix(normalizedVideoEndpointPath(path), "/videos/"):
		return EndpointModeVideoCancel
	case method == "POST" && normalizedAudioJobEndpointPath(path) == "/audio/jobs":
		return EndpointModeAudioJobCreate
	case method == "GET" && audioJobGetEndpointMode(path) != "":
		return audioJobGetEndpointMode(path)
	case method == "DELETE" && strings.HasPrefix(normalizedAudioJobEndpointPath(path), "/audio/jobs/"):
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
	case method == "POST" && strings.Contains(path, ":generateContent"):
		if stream {
			return EndpointModeGenerateContentSSE
		}
		return EndpointModeGenerateContentJSON
	case method == "POST" && strings.Contains(path, ":streamGenerateContent"):
		return EndpointModeGenerateContentSSE
	case strings.Contains(path, ":countTokens"):
		return EndpointModeCountTokens
	case method == "POST" && strings.Contains(path, ":embedContent"):
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

// normalizedVideoEndpointPath 是视频族 endpoint-mode 判定的路径归一：剥
// /v1 版本前缀后把 New API 兼容面 /video/generations* 改写为内部既有
// /videos* 词表（2026-11-12 起对外唯一视频路径；旧 /videos 只保留在内部
// 判定里，公开路由已 404）。
func normalizedVideoEndpointPath(path string) string {
	stripped := normalizedV1StrippedPath(path)
	if stripped == "/video/generations" {
		return "/videos"
	}
	if rest, ok := strings.CutPrefix(stripped, "/video/generations/"); ok {
		return "/videos/" + rest
	}
	return stripped
}

// videoGetEndpointMode maps the GET /v1/video/generations* family（M2 视频，
// 媒体设计 §4.2/§11.6；2026-11-12 New API 兼容批）：轮询
// （GET /v1/video/generations/{task_id}）是 video_get，产物下载
// （GET .../{task_id}/content）是 video_content。列表形态（GET 集合）在
// 对外返回 404，但 endpoint-mode 词表仍归 video_get（旧 /v1/videos 词表
// 兼容保留）。非本族路径返回空串（调用方不进入 video 判定）。
func videoGetEndpointMode(path string) string {
	stripped := normalizedVideoEndpointPath(path)
	if stripped == "/videos" {
		return EndpointModeVideoGet
	}
	if !strings.HasPrefix(stripped, "/videos/") {
		return ""
	}
	if strings.HasSuffix(stripped, "/content") {
		return EndpointModeVideoContent
	}
	return EndpointModeVideoGet
}

// normalizedAudioJobEndpointPath 是长音频任务族 endpoint-mode 判定的路径
// 归一：剥 /v1 版本前缀后把 New API 通用任务面 /tasks/audio* 改写为内部
// 既有 /audio/jobs* 词表（2026-11-12 起对外唯一长音频任务路径；旧
// /audio/jobs 只保留在内部判定里，公开路由已 404）。
func normalizedAudioJobEndpointPath(path string) string {
	stripped := normalizedV1StrippedPath(path)
	if stripped == "/tasks/audio" {
		return "/audio/jobs"
	}
	if rest, ok := strings.CutPrefix(stripped, "/tasks/audio/"); ok {
		return "/audio/jobs/" + rest
	}
	return stripped
}

// audioJobGetEndpointMode maps the GET /v1/tasks/audio* family（M3f 长音频，
// 媒体设计 §4.2/§11.6，契约 §10.2；2026-11-12 New API 通用任务面批）：
// 轮询（GET /v1/tasks/audio/{id}）是 audio_job_get，产物下载
// （GET /v1/tasks/audio/{id}/content，转写结果 JSON）是 audio_job_content。
// 列表形态（GET 集合）在对外返回 404，但 endpoint-mode 词表仍归
// audio_job_get（旧 /audio/jobs 词表兼容保留）。非本族路径返回空串。
func audioJobGetEndpointMode(path string) string {
	stripped := normalizedAudioJobEndpointPath(path)
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

// pathInEndpointFamily 判定路径是否命中端点族本身或其子路径（B7 工具端点
// 的子路径同属同一请求形态，如 /embeddings/multimodal）。
func pathInEndpointFamily(path, family string) bool {
	return path == family || strings.HasPrefix(path, family+"/")
}
