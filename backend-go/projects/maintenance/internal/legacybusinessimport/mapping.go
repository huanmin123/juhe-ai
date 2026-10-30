package legacybusinessimport

// Legacy provider_profile_id → target connection preset. This is the fixed
// table originally pinned by the retired dry-run migration rules; it is
// duplicated here so the rebuild path never depends on that retired engine.
var legacyProfilePresets = map[string]string{
	"profile_gpt_openai_v1":             "gpt_openai_v1",
	"profile_xai_openai_v1":             "xai_openai_v1",
	"profile_deepseek_openai_v1":        "deepseek_openai_v1",
	"profile_deepseek_anthropic_v1":     "deepseek_anthropic_v1",
	"profile_anthropic_anthropic_v1":    "anthropic_v1",
	"profile_gemini_openai_chat_v1beta": "gemini_openai_chat_v1beta",
	"profile_gemini_native_v1beta":      "gemini_native_v1beta",
	"profile_glm_general_openai_v1":     "glm_general_openai_v1",
	"profile_glm_coding_openai_v1":      "glm_coding_openai_v1",
	"profile_glm_coding_anthropic_v1":   "glm_coding_anthropic_v1",
	"profile_minimax_openai_v1":         "minimax_media",
	"profile_volcengine_openai_v1":      "volcengine_ark",
	"profile_qwen_openai_v1":            "qwen_dashscope",
	// Legacy "openai" provider_code accounts were OpenAI-compatible relays with
	// no vendor identity; the design maps them to the generic HTTP driver.
	"profile_openai_openai_v1":             "generic_openai_v1",
	"profile_hybrid_openai_chat_v1":        "generic_openai_v1",
	"profile_hybrid_anthropic_messages_v1": "generic_anthropic_v1",
	"profile_hybrid_gemini_native_v1beta":  "generic_gemini_v1beta",
}

// realVendors is the set of legacy provider_code values that name a real
// upstream vendor. "openai" and "hybrid" deliberately do NOT appear: the
// design maps them to generic_http with a NULL vendor, never to gpt.
var realVendors = map[string]string{
	"gpt":        "gpt",
	"xai":        "xai",
	"deepseek":   "deepseek",
	"anthropic":  "anthropic",
	"gemini":     "gemini",
	"glm":        "glm",
	"minimax":    "minimax",
	"volcengine": "volcengine",
	"qwen":       "qwen",
}

// presetVendors is the vendor implied by a connection preset; the account
// vendor_code must agree with it.
func presetVendor(preset string) string {
	return legacyPresetVendors[preset]
}

var legacyPresetVendors = map[string]string{
	"gpt_openai_v1":             "gpt",
	"xai_openai_v1":             "xai",
	"deepseek_openai_v1":        "deepseek",
	"deepseek_anthropic_v1":     "deepseek",
	"anthropic_v1":              "anthropic",
	"gemini_openai_chat_v1beta": "gemini",
	"gemini_native_v1beta":      "gemini",
	"glm_general_openai_v1":     "glm",
	"glm_coding_openai_v1":      "glm",
	"glm_coding_anthropic_v1":   "glm",
	"minimax_media":             "minimax",
	"volcengine_ark":            "volcengine",
	"qwen_dashscope":            "qwen",
	"generic_openai_v1":         "",
	"generic_anthropic_v1":      "",
	"generic_gemini_v1beta":     "",
}

// presetDriver resolves the connection driver for an account. Real vendors use
// their own driver; generic presets use generic_http.
func presetDriver(preset string) (string, bool) {
	if vendor, ok := legacyPresetVendors[preset]; ok {
		if vendor == "" {
			return "generic_http", true
		}
		return vendor, true
	}
	return "", false
}

// presetSurfaces is the protocol surface a preset speaks; "" means media-only
// (no surface row on the connection).
var presetSurfaces = map[string]string{
	"gpt_openai_v1":             "openai_v1",
	"xai_openai_v1":             "openai_v1",
	"deepseek_openai_v1":        "openai_v1",
	"deepseek_anthropic_v1":     "anthropic_v1",
	"anthropic_v1":              "anthropic_v1",
	"gemini_openai_chat_v1beta": "openai_v1",
	"gemini_native_v1beta":      "gemini_v1beta",
	"glm_general_openai_v1":     "openai_v1",
	"glm_coding_openai_v1":      "openai_v1",
	"glm_coding_anthropic_v1":   "anthropic_v1",
	"minimax_media":             "",
	"volcengine_ark":            "openai_v1",
	"qwen_dashscope":            "openai_v1",
	"generic_openai_v1":         "openai_v1",
	"generic_anthropic_v1":      "anthropic_v1",
	"generic_gemini_v1beta":     "gemini_v1beta",
}

// presetDefaultBaseURL mirrors shared/contracts connector registry defaults for
// every preset the legacy profiles can produce.
var presetDefaultBaseURL = map[string]string{
	"gpt_openai_v1":             "https://api.openai.com/v1",
	"xai_openai_v1":             "https://api.x.ai/v1",
	"deepseek_openai_v1":        "https://api.deepseek.com/v1",
	"deepseek_anthropic_v1":     "https://api.deepseek.com/anthropic/v1",
	"anthropic_v1":              "https://api.anthropic.com/v1",
	"gemini_openai_chat_v1beta": "https://generativelanguage.googleapis.com/v1beta/openai",
	"gemini_native_v1beta":      "https://generativelanguage.googleapis.com/v1beta",
	"glm_general_openai_v1":     "https://open.bigmodel.cn/api/paas/v4",
	"glm_coding_openai_v1":      "https://open.bigmodel.cn/api/coding/paas/v4",
	"glm_coding_anthropic_v1":   "https://open.bigmodel.cn/api/anthropic/v1",
	"minimax_media":             "https://api.minimax.chat/v1",
	"volcengine_ark":            "https://ark.cn-beijing.volces.com/api/v3",
	"qwen_dashscope":            "https://dashscope.aliyuncs.com",
	// generic_* 没有官方地址：账户必须自带 base_url（旧库在加密凭据内保存）。
	// 留空即"必须由凭据提供"，迁移侧对缺失值 fail-closed，不生成占位 host。
	"generic_openai_v1":     "",
	"generic_anthropic_v1":  "",
	"generic_gemini_v1beta": "",
}

// presetVocabulary is the endpoint-mode fallback for accounts whose credential
// JSON carries no supported_endpoint_modes. It mirrors the shared connector
// registry vocabularies (opens with the generic preset surface).
var presetVocabulary = map[string][]string{
	"openai_v1": {
		"chat_json", "chat_sse", "responses_json", "responses_sse", "images_json",
		"audio_speech", "audio_transcription_json",
		"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel",
		"video_create", "video_get", "video_content", "video_cancel",
		"realtime_session",
	},
	"anthropic_v1": {
		"messages_json", "messages_sse", "message_token_counting",
	},
	"gemini_v1beta": {
		"generate_content_json", "generate_content_sse",
		"interactions_json", "interactions_sse",
		"count_tokens", "embed_content", "audio_speech",
	},
}
