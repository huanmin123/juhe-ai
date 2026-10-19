// 连接器 registry：vendor、connection driver、connection preset、protocol surface、
// capability domain 与 endpoint mode 的编译期唯一事实源。
//
// 数据与计数契约来自 docs/functions/供应商与上游协议面重构设计.md：
//   - §1.1（:29-50 的 20 行注册表）展开恰有 49 个允许连接组合与 162 个单 mode 组合；
//   - §1（:95）的公共协议面 upstream operation template 清单（OpenAI 15 条、Anthropic 2 条、Gemini native 8 条）；
//   - §2.2 的域 lane（chat 13、video 4、audio 7、image 1）、preset 词表（8 行）与 driver 收窄规则。
//
// 49 与 162 必须由本文件的组合行 + 词表 + 域 lane + 收窄规则推导，
// 禁止另建第二份手写清单互相对账（设计 §1.1「计数来源和阻断规则」）。

package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// ---------------------------------------------------------------------------
// 枚举常量（设计 §1 与 §2.2 的完整取值空间）
// ---------------------------------------------------------------------------

// 真实厂商标识（设计 §1 身份表，恰好 9 个值；未知厂商在数据库中为 SQL NULL）。
const (
	VendorGPT        = "gpt"
	VendorXAI        = "xai"
	VendorDeepSeek   = "deepseek"
	VendorAnthropic  = "anthropic"
	VendorGemini     = "gemini"
	VendorGLM        = "glm"
	VendorMiniMax    = "minimax"
	VendorVolcengine = "volcengine"
	VendorQwen       = "qwen"
)

// 连接 driver 标识（connection_driver_code，设计 §1 身份表）：
// 9 个厂商 driver + generic_http，恰好 10 个值。
// generic_http 是连接器而不是供应商，不出现在 vendors 表与供应商目录（设计 §1）。
const (
	DriverGenericHTTP = "generic_http"
	DriverGPT         = "gpt"
	DriverXAI         = "xai"
	DriverDeepSeek    = "deepseek"
	DriverAnthropic   = "anthropic"
	DriverGemini      = "gemini"
	DriverGLM         = "glm"
	DriverMiniMax     = "minimax"
	DriverVolcengine  = "volcengine"
	DriverQwen        = "qwen"
)

// 协议面标识（protocol_surface_code，设计 §1 身份表，恰好 3 个值）。
const (
	SurfaceOpenAIV1     = "openai_v1"
	SurfaceAnthropicV1  = "anthropic_v1"
	SurfaceGeminiV1Beta = "gemini_v1beta"
)

// 账户类型（设计 §1.1 注册表 account type 列，恰好 3 个值）。
const (
	AccountTypeAPIKey      = "api_key"
	AccountTypeOAuth       = "oauth"
	AccountTypeGoogleOAuth = "google_oauth"
)

// OAuth type（设计 §1.1 注册表 OAuth type 列与附加规则 4，恰好 3 个值）。
// 注册表中 oauth_type 为 “—” 的行 registry 内以空字符串表达，数据库侧必须为 SQL NULL。
const (
	OAuthTypeCodeAssist = "code_assist"
	OAuthTypeGoogleOne  = "google_one"
	OAuthTypeAIStudio   = "ai_studio"
)

// 认证策略引用键（设计 §1.1 :58 preset record 的认证策略引用，恰好 4 个合法值）。
// generic_http 三种 API Key 认证按设计 §1 附加规则 1 固定认证头；
// 真实厂商 preset 不重复定义认证，由各自 driver 实现（含 gpt/xai 的 oauth 行）。
const (
	AuthStrategyGenericBearer           = "generic_bearer"
	AuthStrategyGenericAnthropicHeaders = "generic_anthropic_headers"
	AuthStrategyGenericGoogAPIKey       = "generic_goog_api_key"
	AuthStrategyDriverDefined           = "driver_defined"
)

// 账户能力域（capability_domain，设计 §2，恰好 4 个值）。
const (
	DomainChat  = "chat"
	DomainVideo = "video"
	DomainAudio = "audio"
	DomainImage = "image"
)

// 连接预设标识（connection_preset_code，设计 §1.1 注册表，恰好 17 个值）。
const (
	PresetGenericOpenAIV1        = "generic_openai_v1"
	PresetGenericAnthropicV1     = "generic_anthropic_v1"
	PresetGenericGeminiV1Beta    = "generic_gemini_v1beta"
	PresetGPTOpenAIV1            = "gpt_openai_v1"
	PresetXAIOpenAIV1            = "xai_openai_v1"
	PresetDeepSeekOpenAIV1       = "deepseek_openai_v1"
	PresetDeepSeekAnthropicV1    = "deepseek_anthropic_v1"
	PresetAnthropicV1            = "anthropic_v1"
	PresetGeminiOpenAIChatV1Beta = "gemini_openai_chat_v1beta"
	PresetGeminiNativeV1Beta     = "gemini_native_v1beta"
	PresetGLMGeneralOpenAIV1     = "glm_general_openai_v1"
	PresetGLMCodingOpenAIV1      = "glm_coding_openai_v1"
	PresetGLMCodingAnthropicV1   = "glm_coding_anthropic_v1"
	PresetMiniMaxMedia           = "minimax_media"
	PresetVolcengineArk          = "volcengine_ark"
	PresetVolcengineOpenSpeech   = "volcengine_openspeech"
	PresetQwenDashScope          = "qwen_dashscope"
)

// endpoint mode（设计 §2.2 域 lane 的并集，恰好 25 个值）。
const (
	// chat lane（13 个）。
	ModeChatJSON             = "chat_json"
	ModeChatSSE              = "chat_sse"
	ModeResponsesJSON        = "responses_json"
	ModeResponsesSSE         = "responses_sse"
	ModeMessagesJSON         = "messages_json"
	ModeMessagesSSE          = "messages_sse"
	ModeMessageTokenCounting = "message_token_counting"
	ModeGenerateContentJSON  = "generate_content_json"
	ModeGenerateContentSSE   = "generate_content_sse"
	ModeInteractionsJSON     = "interactions_json"
	ModeInteractionsSSE      = "interactions_sse"
	ModeCountTokens          = "count_tokens"
	ModeEmbedContent         = "embed_content"

	// video lane（4 个）。
	ModeVideoCreate  = "video_create"
	ModeVideoGet     = "video_get"
	ModeVideoContent = "video_content"
	ModeVideoCancel  = "video_cancel"

	// audio lane（7 个）。
	ModeAudioSpeech            = "audio_speech"
	ModeAudioTranscriptionJSON = "audio_transcription_json"
	ModeAudioJobCreate         = "audio_job_create"
	ModeAudioJobGet            = "audio_job_get"
	ModeAudioJobContent        = "audio_job_content"
	ModeAudioJobCancel         = "audio_job_cancel"
	ModeRealtimeSession        = "realtime_session"

	// image lane（1 个）。
	ModeImagesJSON = "images_json"
)

// ---------------------------------------------------------------------------
// 域 lane（设计 §2.2「域 lane 固定如下」表）
// ---------------------------------------------------------------------------

// capabilityDomainLanes 是每个 capability domain 允许的 endpoint mode 集合。
var capabilityDomainLanes = map[string][]string{
	DomainChat: {
		ModeChatJSON, ModeChatSSE, ModeResponsesJSON, ModeResponsesSSE,
		ModeMessagesJSON, ModeMessagesSSE, ModeMessageTokenCounting,
		ModeGenerateContentJSON, ModeGenerateContentSSE,
		ModeInteractionsJSON, ModeInteractionsSSE, ModeCountTokens, ModeEmbedContent,
	},
	DomainVideo: {ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel},
	DomainAudio: {
		ModeAudioSpeech, ModeAudioTranscriptionJSON, ModeAudioJobCreate,
		ModeAudioJobGet, ModeAudioJobContent, ModeAudioJobCancel, ModeRealtimeSession,
	},
	DomainImage: {ModeImagesJSON},
}

// ---------------------------------------------------------------------------
// 公共协议面 operation path template（设计 §1 :95 固定清单，逐条照抄）
// ---------------------------------------------------------------------------

// 模板格式固定为 METHOD + 一个 ASCII 空格 + 以 / 开头的 path；
// {id}/{model} 是单段占位符。本地列表入口（GET /v1/videos、GET /v1/audio/jobs）
// 不进入 operation template，也不选择上游（设计 §1 :95）。
var (
	// openAIOperationTemplates 是 openai_v1 公共清单（15 条）。
	openAIOperationTemplates = []string{
		"POST /v1/chat/completions",
		"POST /v1/responses",
		"POST /v1/images/generations",
		"POST /v1/audio/speech",
		"POST /v1/audio/transcriptions",
		"POST /v1/audio/translations",
		"POST /v1/audio/jobs",
		"GET /v1/audio/jobs/{id}",
		"GET /v1/audio/jobs/{id}/content",
		"DELETE /v1/audio/jobs/{id}",
		"POST /v1/videos",
		"GET /v1/videos/{id}",
		"GET /v1/videos/{id}/content",
		"DELETE /v1/videos/{id}",
		"GET /v1/realtime",
	}

	// anthropicOperationTemplates 是 anthropic_v1 公共清单（2 条）。
	anthropicOperationTemplates = []string{
		"POST /v1/messages",
		"POST /v1/messages/count_tokens",
	}

	// geminiNativeOperationTemplates 是 gemini_v1beta 公共清单（8 条）。
	geminiNativeOperationTemplates = []string{
		"POST /v1beta/models/{model}:generateContent",
		"POST /v1beta/models/{model}:streamGenerateContent",
		"POST /v1beta/models/{model}:countTokens",
		"POST /v1beta/models/{model}:embedContent",
		"POST /v1beta/interactions",
		"GET /v1beta/interactions/{id}",
		"DELETE /v1beta/interactions/{id}",
		"POST /v1beta/interactions/{id}/cancel",
	}
)

// 媒体 preset additional templates 由媒体协议契约固定
// （设计 §1 :95：厂商媒体 preset 的 additional templates 必须由该 preset record
// 明确列出）。下列模板逐条来自 docs/functions/媒体上游协议契约与Mock上游规格.md
// 的现役上游端点，按章节注明来源；本地列表入口类端点与取消语义缺席的端点
// 不注册（如 volcengine 长转写无取消端点）。
var (
	// minimax_media：§8.1 视频创建与轮询、§8.2 同步 TTS。
	miniMaxMediaOperationTemplates = []string{
		"POST /v1/video_generation",
		"GET /v1/query/video_generation",
		"POST /v1/t2a_v2",
	}
	// volcengine_ark：§9.1 seedance 视频任务创建与轮询。
	volcengineArkOperationTemplates = []string{
		"POST /api/v3/contents/generations/tasks",
		"GET /api/v3/contents/generations/tasks/{id}",
	}
	// volcengine_openspeech：§9.2 豆包语音合成 + 长转写 submit/query 两段（无取消端点）。
	volcengineOpenSpeechOperationTemplates = []string{
		"POST /api/v3/tts/create",
		"POST /api/v3/auc/bigmodel/submit",
		"POST /api/v3/auc/bigmodel/query",
	}
	// qwen_dashscope：§11 录音文件识别提交 + §10.1 万相视频创建 +
	// §10.1/§11 两域共用的 DashScope 任务轮询。
	qwenDashScopeOperationTemplates = []string{
		"POST /api/v1/services/audio/asr/transcription",
		"POST /api/v1/services/aigc/video-generation/video-synthesis",
		"GET /api/v1/tasks/{task_id}",
	}
)

// ---------------------------------------------------------------------------
// preset 完整 mode 词表（设计 §2.2 preset 词表 8 行；收窄前的基线集合）
// ---------------------------------------------------------------------------

var (
	// openAIVocabulary：*_openai_v1 与 generic_openai_v1 共用（16 个 mode）。
	openAIVocabulary = []string{
		ModeChatJSON, ModeChatSSE, ModeResponsesJSON, ModeResponsesSSE, ModeImagesJSON,
		ModeAudioSpeech, ModeAudioTranscriptionJSON,
		ModeAudioJobCreate, ModeAudioJobGet, ModeAudioJobContent, ModeAudioJobCancel,
		ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel,
		ModeRealtimeSession,
	}

	// anthropicVocabulary：*_anthropic_v1 与 generic_anthropic_v1 共用（3 个 mode）。
	anthropicVocabulary = []string{ModeMessagesJSON, ModeMessagesSSE, ModeMessageTokenCounting}

	// geminiNativeVocabulary：gemini_native_v1beta 与 generic_gemini_v1beta 共用（11 个 mode）。
	geminiNativeVocabulary = []string{
		ModeGenerateContentJSON, ModeGenerateContentSSE,
		ModeInteractionsJSON, ModeInteractionsSSE,
		ModeCountTokens, ModeEmbedContent, ModeAudioSpeech,
		ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel,
	}

	// geminiOpenAIChatVocabulary：gemini_openai_chat_v1beta（2 个 mode）。
	geminiOpenAIChatVocabulary = []string{ModeChatJSON, ModeChatSSE}

	// 以下 4 行为厂商媒体 preset 词表（设计 §2.2 :174-177）。
	miniMaxMediaVocabulary = []string{
		ModeAudioSpeech, ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel,
	}
	volcengineArkVocabulary = []string{
		ModeChatJSON, ModeChatSSE, ModeImagesJSON,
		ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel,
	}
	volcengineOpenSpeechVocabulary = []string{
		ModeAudioSpeech, ModeAudioTranscriptionJSON,
		ModeAudioJobCreate, ModeAudioJobGet, ModeAudioJobContent, ModeAudioJobCancel,
	}
	qwenDashScopeVocabulary = []string{
		ModeChatJSON, ModeChatSSE,
		ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel,
		ModeAudioJobCreate, ModeAudioJobGet, ModeAudioJobContent, ModeAudioJobCancel,
	}
)

// driver 收窄词表（设计 §2.2 收窄规则）。
var (
	// responsesOnlyVocabulary：gpt.oauth 与 xai.oauth 的 OpenAI 词表收窄。
	responsesOnlyVocabulary = []string{ModeResponsesJSON, ModeResponsesSSE}

	// glmOpenAINarrowedVocabulary：glm 两个 OpenAI 预设的收窄词表。
	glmOpenAINarrowedVocabulary = []string{
		ModeChatJSON, ModeChatSSE,
		ModeVideoCreate, ModeVideoGet, ModeVideoContent, ModeVideoCancel,
		ModeAudioSpeech, ModeAudioTranscriptionJSON,
	}

	// messagesOnlyVocabulary：deepseek_anthropic_v1 与 glm_coding_anthropic_v1 的收窄词表。
	messagesOnlyVocabulary = []string{ModeMessagesJSON, ModeMessagesSSE}
)

// ---------------------------------------------------------------------------
// driver record（设计 §1.1 driver 维度与 §1 :58 的 credential_schema_key 要求）
// ---------------------------------------------------------------------------

// connectorDriverRecord 描述一个 connection driver 的身份与凭据注册事实。
type connectorDriverRecord struct {
	// Code 是 connection_driver_code。
	Code string
	// Vendor 是 driver 绑定的真实厂商；空字符串表达 SQL NULL（generic_http）。
	Vendor string
	// AccountTypes 是该 driver 允许的 account type 集合。
	AccountTypes []string
	// OAuthTypes 是 account_type=google_oauth 时允许的 oauth type 集合；
	// 其余 driver 行为空，表示 oauth_type 必须为 SQL NULL。
	OAuthTypes []string
	// CredentialSchemaKeys 按 account type 给出现有凭据校验器的稳定键，
	// 键格式固定为 driver + "." + account_type（如 gpt.oauth）。
	CredentialSchemaKeys map[string]string
}

var connectorDriverRecords = []connectorDriverRecord{
	{Code: DriverGenericHTTP, Vendor: "", AccountTypes: []string{AccountTypeAPIKey},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "generic_http.api_key"}},
	{Code: DriverGPT, Vendor: VendorGPT, AccountTypes: []string{AccountTypeAPIKey, AccountTypeOAuth},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "gpt.api_key", AccountTypeOAuth: "gpt.oauth"}},
	{Code: DriverXAI, Vendor: VendorXAI, AccountTypes: []string{AccountTypeAPIKey, AccountTypeOAuth},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "xai.api_key", AccountTypeOAuth: "xai.oauth"}},
	{Code: DriverDeepSeek, Vendor: VendorDeepSeek, AccountTypes: []string{AccountTypeAPIKey},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "deepseek.api_key"}},
	{Code: DriverAnthropic, Vendor: VendorAnthropic, AccountTypes: []string{AccountTypeAPIKey, AccountTypeOAuth},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "anthropic.api_key", AccountTypeOAuth: "anthropic.oauth"}},
	{Code: DriverGemini, Vendor: VendorGemini, AccountTypes: []string{AccountTypeAPIKey, AccountTypeGoogleOAuth},
		OAuthTypes:           []string{OAuthTypeCodeAssist, OAuthTypeGoogleOne, OAuthTypeAIStudio},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "gemini.api_key", AccountTypeGoogleOAuth: "gemini.google_oauth"}},
	{Code: DriverGLM, Vendor: VendorGLM, AccountTypes: []string{AccountTypeAPIKey},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "glm.api_key"}},
	{Code: DriverMiniMax, Vendor: VendorMiniMax, AccountTypes: []string{AccountTypeAPIKey},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "minimax.api_key"}},
	{Code: DriverVolcengine, Vendor: VendorVolcengine, AccountTypes: []string{AccountTypeAPIKey},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "volcengine.api_key"}},
	{Code: DriverQwen, Vendor: VendorQwen, AccountTypes: []string{AccountTypeAPIKey},
		CredentialSchemaKeys: map[string]string{AccountTypeAPIKey: "qwen.api_key"}},
}

// ---------------------------------------------------------------------------
// preset record（设计 §1.1 preset 维度与 §2.2 preset 词表）
// ---------------------------------------------------------------------------

// connectorPresetRecord 描述一个 connection preset 的协议、URL、认证与词表注册事实
// （设计 §1.1 :58：每个 preset record 必须同时包含 operation_path_templates[]、
// allowed_base_path_suffixes[]、认证策略引用、URL builder key、协议面和完整 mode 集合，
// 以及 nullable default_base_url，见设计 :58-80 的 17 值表）。
type connectorPresetRecord struct {
	// Code 是 connection_preset_code。
	Code string
	// URLBuilderKey 是 route builder 选择键；按设计 §1.1 以 preset code 充当。
	URLBuilderKey string
	// AuthStrategyKey 是认证策略引用（设计 §1.1 :58）。
	AuthStrategyKey string
	// Surface 是绑定的协议面；空字符串表达纯媒体连接（SQL NULL，设计 §1.1 表）。
	Surface string
	// DefaultBaseURL 是 nullable default_base_url（设计 :58-80）：只用于迁移
	// fallback、seed 和 UI 初始值，不覆盖 API 明确提交的 baseUrl；generic 三
	// preset 固定为 NULL（registry 内空串表达 SQL NULL）。
	DefaultBaseURL string
	// AllowedBasePathSuffixes 是允许的 service/version 前缀后缀集合；
	// 空字符串表示根路径（设计 §2.1 base_url 约束）。
	AllowedBasePathSuffixes []string
	// OperationPathTemplates 是 upstream operation path 模板（METHOD /path）。
	OperationPathTemplates []string
	// Vocabulary 是收窄前的完整 mode 词表（设计 §2.2 preset 词表）。
	Vocabulary []string
}

var connectorPresetRecords = []connectorPresetRecord{
	{Code: PresetGenericOpenAIV1, URLBuilderKey: PresetGenericOpenAIV1, AuthStrategyKey: AuthStrategyGenericBearer, Surface: SurfaceOpenAIV1,
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  openAIOperationTemplates,
		Vocabulary:              openAIVocabulary},
	{Code: PresetGenericAnthropicV1, URLBuilderKey: PresetGenericAnthropicV1, AuthStrategyKey: AuthStrategyGenericAnthropicHeaders, Surface: SurfaceAnthropicV1,
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  anthropicOperationTemplates,
		Vocabulary:              anthropicVocabulary},
	{Code: PresetGenericGeminiV1Beta, URLBuilderKey: PresetGenericGeminiV1Beta, AuthStrategyKey: AuthStrategyGenericGoogAPIKey, Surface: SurfaceGeminiV1Beta,
		AllowedBasePathSuffixes: []string{"", "/v1beta"},
		OperationPathTemplates:  geminiNativeOperationTemplates,
		Vocabulary:              geminiNativeVocabulary},
	{Code: PresetGPTOpenAIV1, URLBuilderKey: PresetGPTOpenAIV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://api.openai.com/v1",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  openAIOperationTemplates,
		Vocabulary:              openAIVocabulary},
	{Code: PresetXAIOpenAIV1, URLBuilderKey: PresetXAIOpenAIV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://api.x.ai/v1",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  openAIOperationTemplates,
		Vocabulary:              openAIVocabulary},
	{Code: PresetDeepSeekOpenAIV1, URLBuilderKey: PresetDeepSeekOpenAIV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://api.deepseek.com",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  openAIOperationTemplates,
		Vocabulary:              openAIVocabulary},
	{Code: PresetDeepSeekAnthropicV1, URLBuilderKey: PresetDeepSeekAnthropicV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceAnthropicV1,
		DefaultBaseURL:          "https://api.deepseek.com/anthropic",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  anthropicOperationTemplates,
		Vocabulary:              anthropicVocabulary},
	{Code: PresetAnthropicV1, URLBuilderKey: PresetAnthropicV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceAnthropicV1,
		DefaultBaseURL:          "https://api.anthropic.com/v1",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  anthropicOperationTemplates,
		Vocabulary:              anthropicVocabulary},
	{Code: PresetGeminiOpenAIChatV1Beta, URLBuilderKey: PresetGeminiOpenAIChatV1Beta, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://generativelanguage.googleapis.com/v1beta/openai",
		AllowedBasePathSuffixes: []string{"", "/v1beta/openai"},
		OperationPathTemplates:  []string{"POST /v1/chat/completions"},
		Vocabulary:              geminiOpenAIChatVocabulary},
	{Code: PresetGeminiNativeV1Beta, URLBuilderKey: PresetGeminiNativeV1Beta, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceGeminiV1Beta,
		DefaultBaseURL:          "https://generativelanguage.googleapis.com",
		AllowedBasePathSuffixes: []string{"", "/v1beta"},
		OperationPathTemplates:  geminiNativeOperationTemplates,
		Vocabulary:              geminiNativeVocabulary},
	{Code: PresetGLMGeneralOpenAIV1, URLBuilderKey: PresetGLMGeneralOpenAIV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://open.bigmodel.cn/api/paas/v4",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  openAIOperationTemplates,
		Vocabulary:              openAIVocabulary},
	{Code: PresetGLMCodingOpenAIV1, URLBuilderKey: PresetGLMCodingOpenAIV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://open.bigmodel.cn/api/coding/paas/v4",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  openAIOperationTemplates,
		Vocabulary:              openAIVocabulary},
	{Code: PresetGLMCodingAnthropicV1, URLBuilderKey: PresetGLMCodingAnthropicV1, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceAnthropicV1,
		DefaultBaseURL:          "https://open.bigmodel.cn/api/anthropic",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  anthropicOperationTemplates,
		Vocabulary:              anthropicVocabulary},
	{Code: PresetMiniMaxMedia, URLBuilderKey: PresetMiniMaxMedia, AuthStrategyKey: AuthStrategyDriverDefined, Surface: "",
		DefaultBaseURL:          "https://api.minimax.chat",
		AllowedBasePathSuffixes: []string{""},
		OperationPathTemplates:  miniMaxMediaOperationTemplates,
		Vocabulary:              miniMaxMediaVocabulary},
	{Code: PresetVolcengineArk, URLBuilderKey: PresetVolcengineArk, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://ark.cn-beijing.volces.com",
		AllowedBasePathSuffixes: []string{"", "/v1"},
		OperationPathTemplates:  volcengineArkOperationTemplates,
		Vocabulary:              volcengineArkVocabulary},
	{Code: PresetVolcengineOpenSpeech, URLBuilderKey: PresetVolcengineOpenSpeech, AuthStrategyKey: AuthStrategyDriverDefined, Surface: "",
		DefaultBaseURL:          "https://openspeech.bytedance.com",
		AllowedBasePathSuffixes: []string{""},
		OperationPathTemplates:  volcengineOpenSpeechOperationTemplates,
		Vocabulary:              volcengineOpenSpeechVocabulary},
	{Code: PresetQwenDashScope, URLBuilderKey: PresetQwenDashScope, AuthStrategyKey: AuthStrategyDriverDefined, Surface: SurfaceOpenAIV1,
		DefaultBaseURL:          "https://dashscope.aliyuncs.com",
		AllowedBasePathSuffixes: []string{"", "/compatible-mode/v1"},
		OperationPathTemplates:  qwenDashScopeOperationTemplates,
		Vocabulary:              qwenDashScopeVocabulary},
}

// ---------------------------------------------------------------------------
// 组合行（设计 §1.1 :29-50 的 20 行表，逐行照抄）
// ---------------------------------------------------------------------------

// connectorComboRow 是注册表的一行：driver 凭据行 × preset × 允许的 capability domain。
// AccountTypes/OAuthTypes 为多值时按笛卡尔积展开；OAuthTypes 为空表示该行
// oauth_type 必须为 SQL NULL（设计 §1.1 :52）。
type connectorComboRow struct {
	Driver       string
	AccountTypes []string
	OAuthTypes   []string
	Preset       string
	Domains      []string
}

var connectorComboRows = []connectorComboRow{
	{Driver: DriverGenericHTTP, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGenericOpenAIV1,
		Domains: []string{DomainChat, DomainVideo, DomainAudio, DomainImage}},
	{Driver: DriverGenericHTTP, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGenericAnthropicV1,
		Domains: []string{DomainChat}},
	{Driver: DriverGenericHTTP, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGenericGeminiV1Beta,
		Domains: []string{DomainChat, DomainVideo, DomainAudio}},
	{Driver: DriverGPT, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGPTOpenAIV1,
		Domains: []string{DomainChat, DomainVideo, DomainAudio, DomainImage}},
	{Driver: DriverGPT, AccountTypes: []string{AccountTypeOAuth}, Preset: PresetGPTOpenAIV1,
		Domains: []string{DomainChat}},
	{Driver: DriverXAI, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetXAIOpenAIV1,
		Domains: []string{DomainChat, DomainVideo}},
	{Driver: DriverXAI, AccountTypes: []string{AccountTypeOAuth}, Preset: PresetXAIOpenAIV1,
		Domains: []string{DomainChat}},
	{Driver: DriverDeepSeek, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetDeepSeekOpenAIV1,
		Domains: []string{DomainChat}},
	{Driver: DriverDeepSeek, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetDeepSeekAnthropicV1,
		Domains: []string{DomainChat}},
	{Driver: DriverAnthropic, AccountTypes: []string{AccountTypeAPIKey, AccountTypeOAuth}, Preset: PresetAnthropicV1,
		Domains: []string{DomainChat}},
	{Driver: DriverGemini, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGeminiOpenAIChatV1Beta,
		Domains: []string{DomainChat}},
	{Driver: DriverGemini, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGeminiNativeV1Beta,
		Domains: []string{DomainChat, DomainVideo, DomainAudio}},
	{Driver: DriverGemini, AccountTypes: []string{AccountTypeGoogleOAuth},
		OAuthTypes: []string{OAuthTypeCodeAssist, OAuthTypeGoogleOne, OAuthTypeAIStudio},
		Preset:     PresetGeminiNativeV1Beta,
		Domains:    []string{DomainChat, DomainVideo, DomainAudio}},
	{Driver: DriverGLM, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGLMGeneralOpenAIV1,
		Domains: []string{DomainChat, DomainVideo, DomainAudio}},
	{Driver: DriverGLM, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGLMCodingOpenAIV1,
		Domains: []string{DomainChat, DomainVideo, DomainAudio}},
	{Driver: DriverGLM, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetGLMCodingAnthropicV1,
		Domains: []string{DomainChat}},
	{Driver: DriverMiniMax, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetMiniMaxMedia,
		Domains: []string{DomainVideo, DomainAudio}},
	{Driver: DriverVolcengine, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetVolcengineArk,
		Domains: []string{DomainChat, DomainVideo, DomainImage}},
	{Driver: DriverVolcengine, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetVolcengineOpenSpeech,
		Domains: []string{DomainAudio}},
	{Driver: DriverQwen, AccountTypes: []string{AccountTypeAPIKey}, Preset: PresetQwenDashScope,
		Domains: []string{DomainChat, DomainVideo, DomainAudio}},
}

// ---------------------------------------------------------------------------
// 派生数据（49 连接组合与 162 单 mode 组合的唯一来源）
// ---------------------------------------------------------------------------

// ConnectorCombo 是一个允许的连接组合（设计 §1.1 五元组 + driver/preset 投影字段）。
// Vendor/OAuthType/Surface 为空字符串时等价于数据库 SQL NULL。
type ConnectorCombo struct {
	// Driver 是 connection_driver_code。
	Driver string
	// Vendor 是 driver 绑定的真实厂商；generic_http 为空字符串（SQL NULL）。
	Vendor string
	// AccountType 是账户类型。
	AccountType string
	// OAuthType 是 google_oauth 行的 oauth type；其余行为空字符串（SQL NULL）。
	OAuthType string
	// Preset 是 connection_preset_code。
	Preset string
	// Surface 是 preset 绑定的协议面；纯媒体 preset 为空字符串（SQL NULL）。
	Surface string
	// Domain 是 capability domain。
	Domain string
}

// EndpointModeCombo 是 ConnectorCombo 的单 mode 展开：一个连接组合允许的每个
// endpoint mode 各一条（设计 §1.1 :54 的 162 条清单）。
type EndpointModeCombo struct {
	// Driver/Vendor/AccountType/OAuthType/Preset/Surface/Domain 语义与 ConnectorCombo 相同。
	Driver      string
	Vendor      string
	AccountType string
	OAuthType   string
	Preset      string
	Surface     string
	Domain      string
	// Mode 是该组合允许的单个 endpoint mode。
	Mode string
	// AllowedBaseSuffixes 是 preset 注册的 base path suffix 集合副本。
	AllowedBaseSuffixes []string
	// OperationTemplates 是 preset 注册的 operation path template 集合副本。
	OperationTemplates []string
}

var (
	connectorDriverRecordIndex = buildDriverRecordIndex(connectorDriverRecords)
	connectorPresetRecordIndex = buildPresetRecordIndex(connectorPresetRecords)
)

var (
	// connectorCombos 是 49 条允许连接组合的排序稳定清单。
	connectorCombos = buildConnectorCombos()
	// endpointModeCombos 是 162 条单 mode 组合的排序稳定清单。
	endpointModeCombos = buildEndpointModeCombos()
	// connectorComboIndex 是五元组判定索引。
	connectorComboIndex = buildConnectorComboIndex(connectorCombos)
	// registryCanonicalJSON 是 fingerprint 的 canonical 输入（设计 §1.1 :56）。
	registryCanonicalJSON = buildRegistryCanonicalJSON(endpointModeCombos)
)

func buildDriverRecordIndex(records []connectorDriverRecord) map[string]connectorDriverRecord {
	index := make(map[string]connectorDriverRecord, len(records))
	for _, record := range records {
		index[record.Code] = record
	}
	return index
}

func buildPresetRecordIndex(records []connectorPresetRecord) map[string]connectorPresetRecord {
	index := make(map[string]connectorPresetRecord, len(records))
	for _, record := range records {
		index[record.Code] = record
	}
	return index
}

// PresetDefaultBaseURL 返回 preset 注册的 nullable default_base_url（设计
// :58-80 的 17 值表；registry 内空串表达 SQL NULL）。语义与 record 字段一致：
// 只用于草稿缺省回退、迁移 fallback、seed 和 UI 初始值，不覆盖 API 明确提交
// 的 baseUrl；generic 三 preset 与未知 preset 返回空串。
func PresetDefaultBaseURL(preset string) string {
	if record, ok := connectorPresetRecordIndex[preset]; ok {
		return record.DefaultBaseURL
	}
	return ""
}

// connectorComboLess 按 driver、account type、oauth type、preset、domain 字典序比较。
func connectorComboLess(a, b ConnectorCombo) bool {
	if a.Driver != b.Driver {
		return a.Driver < b.Driver
	}
	if a.AccountType != b.AccountType {
		return a.AccountType < b.AccountType
	}
	if a.OAuthType != b.OAuthType {
		return a.OAuthType < b.OAuthType
	}
	if a.Preset != b.Preset {
		return a.Preset < b.Preset
	}
	return a.Domain < b.Domain
}

// endpointModeComboLess 在 connectorComboLess 之上追加 mode 字典序。
func endpointModeComboLess(a, b EndpointModeCombo) bool {
	if a.Driver != b.Driver {
		return a.Driver < b.Driver
	}
	if a.AccountType != b.AccountType {
		return a.AccountType < b.AccountType
	}
	if a.OAuthType != b.OAuthType {
		return a.OAuthType < b.OAuthType
	}
	if a.Preset != b.Preset {
		return a.Preset < b.Preset
	}
	if a.Domain != b.Domain {
		return a.Domain < b.Domain
	}
	return a.Mode < b.Mode
}

func buildConnectorCombos() []ConnectorCombo {
	var combos []ConnectorCombo
	for _, row := range connectorComboRows {
		driver, ok := connectorDriverRecordIndex[row.Driver]
		if !ok {
			panic("connector registry: 组合行引用了未注册 driver " + row.Driver)
		}
		preset, ok := connectorPresetRecordIndex[row.Preset]
		if !ok {
			panic("connector registry: 组合行引用了未注册 preset " + row.Preset)
		}
		for _, accountType := range row.AccountTypes {
			oauthTypes := row.OAuthTypes
			if len(oauthTypes) == 0 {
				oauthTypes = []string{""}
			}
			for _, oauthType := range oauthTypes {
				for _, domain := range row.Domains {
					combos = append(combos, ConnectorCombo{
						Driver:      row.Driver,
						Vendor:      driver.Vendor,
						AccountType: accountType,
						OAuthType:   oauthType,
						Preset:      row.Preset,
						Surface:     preset.Surface,
						Domain:      domain,
					})
				}
			}
		}
	}
	sort.Slice(combos, func(i, j int) bool { return connectorComboLess(combos[i], combos[j]) })
	return combos
}

func buildEndpointModeCombos() []EndpointModeCombo {
	combos := make([]EndpointModeCombo, 0, 4*len(connectorCombos))
	for _, combo := range connectorCombos {
		preset := connectorPresetRecordIndex[combo.Preset]
		for _, mode := range AllowedModes(combo.Domain, combo.Preset, combo.AccountType, combo.OAuthType) {
			combos = append(combos, EndpointModeCombo{
				Driver:              combo.Driver,
				Vendor:              combo.Vendor,
				AccountType:         combo.AccountType,
				OAuthType:           combo.OAuthType,
				Preset:              combo.Preset,
				Surface:             combo.Surface,
				Domain:              combo.Domain,
				Mode:                mode,
				AllowedBaseSuffixes: append([]string(nil), preset.AllowedBasePathSuffixes...),
				OperationTemplates:  append([]string(nil), preset.OperationPathTemplates...),
			})
		}
	}
	sort.Slice(combos, func(i, j int) bool { return endpointModeComboLess(combos[i], combos[j]) })
	return combos
}

func connectorComboKey(driver, accountType, oauthType, preset, domain string) string {
	return driver + "\x00" + accountType + "\x00" + oauthType + "\x00" + preset + "\x00" + domain
}

func buildConnectorComboIndex(combos []ConnectorCombo) map[string]struct{} {
	index := make(map[string]struct{}, len(combos))
	for _, combo := range combos {
		index[connectorComboKey(combo.Driver, combo.AccountType, combo.OAuthType, combo.Preset, combo.Domain)] = struct{}{}
	}
	return index
}

// ---------------------------------------------------------------------------
// 收窄规则与 AllowedModes（设计 §2.2）
// ---------------------------------------------------------------------------

// presetVocabulary 返回 preset 在指定 account type / oauth type 下收窄后的 mode 词表
// （设计 §2.2：AllowedModes = DomainLane(capability_domain) ∩ PresetVocabulary(...)）。
// 收窄只作用于真实 driver 的注册行：gpt.oauth 与 xai.oauth 收窄为 responses 双 mode；
// glm 两个 OpenAI 预设与 deepseek_anthropic_v1、glm_coding_anthropic_v1 各自收窄；
// generic_http 的 preset 不参与任何收窄规则；
// gemini google_oauth 三种 oauth type 与 api_key 同词表，不按 oauth_type 再收窄。
// oauthType 参数当前不产生额外收窄，保留以对齐设计公式的入参形状。
func presetVocabulary(preset, accountType, oauthType string) map[string]struct{} {
	_ = oauthType
	record, ok := connectorPresetRecordIndex[preset]
	if !ok {
		return map[string]struct{}{}
	}
	vocabulary := record.Vocabulary
	switch {
	case accountType == AccountTypeOAuth && (preset == PresetGPTOpenAIV1 || preset == PresetXAIOpenAIV1):
		vocabulary = responsesOnlyVocabulary
	case preset == PresetGLMGeneralOpenAIV1 || preset == PresetGLMCodingOpenAIV1:
		vocabulary = glmOpenAINarrowedVocabulary
	case preset == PresetDeepSeekAnthropicV1 || preset == PresetGLMCodingAnthropicV1:
		vocabulary = messagesOnlyVocabulary
	}
	set := make(map[string]struct{}, len(vocabulary))
	for _, mode := range vocabulary {
		set[mode] = struct{}{}
	}
	return set
}

// AllowedModes 返回 DomainLane(domain) ∩ PresetVocabulary(preset, accountType, oauthType)，
// 按 ASCII 升序排序；未知域、未知 preset 或空交集返回空切片（设计 §2.2）。
func AllowedModes(domain, preset, accountType, oauthType string) []string {
	lane, ok := capabilityDomainLanes[domain]
	if !ok {
		return []string{}
	}
	vocabulary := presetVocabulary(preset, accountType, oauthType)
	modes := make([]string, 0, len(lane))
	for _, mode := range lane {
		if _, ok := vocabulary[mode]; ok {
			modes = append(modes, mode)
		}
	}
	sort.Strings(modes)
	return modes
}

// ---------------------------------------------------------------------------
// 公开 API（设计 §1.1：三个项目绑定同一 tuple，不得复制允许集合）
// ---------------------------------------------------------------------------

// AllowedConnectorCombos 返回全部 49 个允许连接组合，
// 按 driver、account type、oauth type、preset、domain 的字典序稳定排序。
// 返回值是 registry 内部数据的只读视图，调用方不得修改。
func AllowedConnectorCombos() []ConnectorCombo {
	return connectorCombos
}

// AllowedEndpointModeCombos 返回全部 162 个单 mode 组合，
// 按 connectorCombos 同一排序规则追加 mode 字典序。
// 返回值是 registry 内部数据的只读视图，调用方不得修改。
func AllowedEndpointModeCombos() []EndpointModeCombo {
	return endpointModeCombos
}

// IsAllowedConnectorCombo 判定五元组是否为 registry 允许的连接组合；
// oauthType 为空字符串表示 SQL NULL，未注册组合一律拒绝（设计 §1.1）。
func IsAllowedConnectorCombo(driver, accountType, oauthType, preset, domain string) bool {
	_, ok := connectorComboIndex[connectorComboKey(driver, accountType, oauthType, preset, domain)]
	return ok
}

// ConnectorComboVerdict 是笛卡尔积判定辅助，语义与 IsAllowedConnectorCombo 相同，
// 供外部遍历全组合空间（10 driver × 3 account type × 4 oauth 值 × 17 preset × 4 domain
// = 8160）时统计允许/拒绝结论。
func ConnectorComboVerdict(driver, accountType, oauthType, preset, domain string) (allowed bool) {
	return IsAllowedConnectorCombo(driver, accountType, oauthType, preset, domain)
}

// ComboDriverCounts 返回 49 条连接组合按 driver 的计数。
func ComboDriverCounts() map[string]int {
	counts := make(map[string]int, len(connectorCombos))
	for _, combo := range connectorCombos {
		counts[combo.Driver]++
	}
	return counts
}

// ModeComboDriverCounts 返回 162 条单 mode 组合按 driver 的计数。
func ModeComboDriverCounts() map[string]int {
	counts := make(map[string]int, len(endpointModeCombos))
	for _, combo := range endpointModeCombos {
		counts[combo.Driver]++
	}
	return counts
}

// RegistryFingerprint 返回 registry 的 SHA-256 指纹（64 位小写 hex）。
// 计算方式（设计 §1.1 :56）：对 162 条单 mode 元素组成的 UTF-8 compact JSON 数组的
// 原始 bytes 计算 SHA-256；数组元素按 ASCII 字典序排序；每个元素字段顺序固定为
// driver、vendor、accountType、oauthType、preset、surface、domain、mode、
// allowedBaseSuffixes、operationTemplates、defaultBaseUrl；vendor/oauthType/surface
// 为空时输出 JSON null；allowedBaseSuffixes 与 operationTemplates 按 ASCII 排序；
// defaultBaseUrl 是标量，按注册表 canonical bytes 原样写入，不参与排序，
// 空串（generic 三 preset）输出 JSON null（设计 :81）；分隔紧凑（无空格、无缩进）。
func RegistryFingerprint() string {
	sum := sha256.Sum256([]byte(registryCanonicalJSON))
	return hex.EncodeToString(sum[:])
}

// registryFingerprintElement 是 canonical JSON 的单元素 struct；
// 字段声明顺序即序列化顺序，必须与设计 §1.1 :56 固定格式逐字段一致。
type registryFingerprintElement struct {
	Driver              string   `json:"driver"`
	Vendor              *string  `json:"vendor"`
	AccountType         string   `json:"accountType"`
	OAuthType           *string  `json:"oauthType"`
	Preset              string   `json:"preset"`
	Surface             *string  `json:"surface"`
	Domain              string   `json:"domain"`
	Mode                string   `json:"mode"`
	AllowedBaseSuffixes []string `json:"allowedBaseSuffixes"`
	OperationTemplates  []string `json:"operationTemplates"`
	// DefaultBaseURL 是标量尾字段（设计 :56/:81）：按 canonical bytes 原样
	// 写入、不参与排序；空串经 nullableJSONString 输出 JSON null。
	DefaultBaseURL *string `json:"defaultBaseUrl"`
}

func nullableJSONString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// buildRegistryCanonicalJSON 生成 fingerprint 的 canonical 紧凑 JSON 数组文本。
func buildRegistryCanonicalJSON(modeCombos []EndpointModeCombo) string {
	elements := make([]string, 0, len(modeCombos))
	for _, combo := range modeCombos {
		preset := connectorPresetRecordIndex[combo.Preset]
		suffixes := append([]string(nil), combo.AllowedBaseSuffixes...)
		sort.Strings(suffixes)
		templates := append([]string(nil), combo.OperationTemplates...)
		sort.Strings(templates)
		raw, err := json.Marshal(registryFingerprintElement{
			Driver:              combo.Driver,
			Vendor:              nullableJSONString(combo.Vendor),
			AccountType:         combo.AccountType,
			OAuthType:           nullableJSONString(combo.OAuthType),
			Preset:              combo.Preset,
			Surface:             nullableJSONString(combo.Surface),
			Domain:              combo.Domain,
			Mode:                combo.Mode,
			AllowedBaseSuffixes: suffixes,
			OperationTemplates:  templates,
			DefaultBaseURL:      nullableJSONString(preset.DefaultBaseURL),
		})
		if err != nil {
			// registry 数据只含 string 与 []string，序列化失败属于不可恢复的注册表错误。
			panic("connector registry: fingerprint 元素序列化失败: " + err.Error())
		}
		elements = append(elements, string(raw))
	}
	sort.Strings(elements)
	var buffer bytes.Buffer
	buffer.WriteByte('[')
	for i, element := range elements {
		if i > 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString(element)
	}
	buffer.WriteByte(']')
	return buffer.String()
}
