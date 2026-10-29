package providerconnectionmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// §10 逐类映射规则（docs/functions/供应商与上游协议面重构设计.md :483-518）
// 的 dry-run 判定实现。本文件同时承载判定所需的 registry 知识字面量（§1.1
// preset 表、§2.2 域 lane 与 preset 词表、旧 profile→preset 固定表）；完整
// 编译期 registry 属 shared/contracts（§1.1），正式执行切片对齐。
//
// 切片口径（manifest notes 同步记录）：
//   - 旧凭据密文不解密：credential fingerprint = SHA-256(ciphertext)；
//     规则 12 的 Base URL 优先级跳过步骤②（credentials JSON）；google_oauth
//     的 oauth_type 提升不可判定 → accounts_blocked（fail-closed）。
//   - 模型 supported_endpoint_modes_json 由 supported_api_protocols_json 经
//     与 seed 投影同表的 protocol→mode 映射推导（modelSeedProtocolEndpointModes
//     同词表）；未知协议或空推导 → models_blocked（缺失）。
//   - 连接 enabled 恒 true；连接 mode 集合 = preset 词表 ∩ 域 lane（旧账户
//     已启用 mode 在加密凭据内，本切片不可读）。
//   - 规则 11 历史行按表清点、全部标 blocked，不进 snapshot/hash。

// ── §1.1 vendor/registry 词表 ─────────────────────────────────────────────

var realVendorCodes = map[string]bool{
	"gpt": true, "xai": true, "deepseek": true, "anthropic": true, "gemini": true,
	"glm": true, "minimax": true, "volcengine": true, "qwen": true,
}

// legacyProviderAllowlist 是规则 0 的 11 值 binary 精确允许集合。
var legacyProviderAllowlist = map[string]bool{
	"gpt": true, "xai": true, "deepseek": true, "anthropic": true, "gemini": true,
	"glm": true, "minimax": true, "volcengine": true, "qwen": true,
	"openai": true, "hybrid": true,
}

// driverAccountTypes 是 §1.1 注册表的 (driver → account_type) 允许集。
var driverAccountTypes = map[string]map[string]bool{
	"generic_http": {"api_key": true},
	"gpt":          {"api_key": true, "oauth": true},
	"xai":          {"api_key": true, "oauth": true},
	"deepseek":     {"api_key": true},
	"anthropic":    {"api_key": true, "oauth": true},
	"gemini":       {"api_key": true, "google_oauth": true},
	"glm":          {"api_key": true},
	"minimax":      {"api_key": true},
	"volcengine":   {"api_key": true},
	"qwen":         {"api_key": true},
}

// presetSurfaces 是 §1.1 preset → protocol_surface_code（"" = 纯媒体 NULL）。
var presetSurfaces = map[string]string{
	"generic_openai_v1":         "openai_v1",
	"generic_anthropic_v1":      "anthropic_v1",
	"generic_gemini_v1beta":     "gemini_v1beta",
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
	"volcengine_openspeech":     "",
	"qwen_dashscope":            "openai_v1",
}

// presetDefaultBaseURLs 是 §1.1 的 17 个 preset 默认 Base URL（"" = registry
// SQL NULL；规则 12 步骤④ fallback）。
var presetDefaultBaseURLs = map[string]string{
	"generic_openai_v1":         "",
	"generic_anthropic_v1":      "",
	"generic_gemini_v1beta":     "",
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
	"volcengine_openspeech":     "https://openspeech.bytedance.com",
	"qwen_dashscope":            "https://dashscope.aliyuncs.com",
}

// ── §2.2 域 lane 与 preset 词表 ───────────────────────────────────────────

var domainLanes = map[string][]string{
	"chat":  {"chat_json", "chat_sse", "responses_json", "responses_sse", "messages_json", "messages_sse", "message_token_counting", "generate_content_json", "generate_content_sse", "interactions_json", "interactions_sse", "count_tokens", "embed_content"},
	"video": {"video_create", "video_get", "video_content", "video_cancel"},
	"audio": {"audio_speech", "audio_transcription_json", "audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel", "realtime_session"},
	"image": {"images_json"},
}

var presetVocabularies = map[string][]string{
	"generic_openai_v1":         openaiFamilyVocabulary,
	"generic_anthropic_v1":      {"messages_json", "messages_sse", "message_token_counting"},
	"generic_gemini_v1beta":     geminiNativeVocabulary,
	"gpt_openai_v1":             openaiFamilyVocabulary,
	"xai_openai_v1":             openaiFamilyVocabulary,
	"deepseek_openai_v1":        openaiFamilyVocabulary,
	"deepseek_anthropic_v1":     {"messages_json", "messages_sse"},
	"anthropic_v1":              {"messages_json", "messages_sse", "message_token_counting"},
	"gemini_openai_chat_v1beta": {"chat_json", "chat_sse"},
	"gemini_native_v1beta":      geminiNativeVocabulary,
	"glm_general_openai_v1":     {"chat_json", "chat_sse", "video_create", "video_get", "video_content", "video_cancel", "audio_speech", "audio_transcription_json"},
	"glm_coding_openai_v1":      {"chat_json", "chat_sse", "video_create", "video_get", "video_content", "video_cancel", "audio_speech", "audio_transcription_json"},
	"glm_coding_anthropic_v1":   {"messages_json", "messages_sse"},
	"minimax_media":             {"audio_speech", "video_create", "video_get", "video_content", "video_cancel"},
	"volcengine_ark":            {"chat_json", "chat_sse", "images_json", "video_create", "video_get", "video_content", "video_cancel"},
	"volcengine_openspeech":     {"audio_speech", "audio_transcription_json", "audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel"},
	"qwen_dashscope":            {"chat_json", "chat_sse", "video_create", "video_get", "video_content", "video_cancel", "audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel"},
}

// openaiFamilyVocabulary 是 *_openai_v1、generic_openai_v1 的 16 值词表
// （gpt.oauth / xai.oauth 的 {responses_json,responses_sse} 收窄随正式执行
// registry 对齐，dry-run 判定不受影响：旧账户均为独立 profile 行）。
var openaiFamilyVocabulary = []string{
	"chat_json", "chat_sse", "responses_json", "responses_sse", "images_json",
	"audio_speech", "audio_transcription_json", "audio_job_create", "audio_job_get",
	"audio_job_content", "audio_job_cancel", "video_create", "video_get",
	"video_content", "video_cancel", "realtime_session",
}

// geminiNativeVocabulary 是 gemini_native_v1beta、generic_gemini_v1beta 的
// 11 值词表。
var geminiNativeVocabulary = []string{
	"generate_content_json", "generate_content_sse", "interactions_json",
	"interactions_sse", "count_tokens", "embed_content", "audio_speech",
	"video_create", "video_get", "video_content", "video_cancel",
}

// mode25 是 §2.2 的 25 值 endpoint mode 词表。
var mode25 = func() map[string]bool {
	set := map[string]bool{}
	for _, lane := range domainLanes {
		for _, mode := range lane {
			set[mode] = true
		}
	}
	return set
}()

// textModes 是文本 mode 集合（= chat lane；规则 10 的 unpriced 判定域）。
var textModes = func() map[string]bool {
	set := map[string]bool{}
	for _, mode := range domainLanes["chat"] {
		set[mode] = true
	}
	return set
}()

// ── 规则 1/2/3 的旧 profile 与映射族固定表 ────────────────────────────────

type profileTarget struct {
	provider string // 期望的旧 accounts.provider_code（binary 精确）
	preset   string
}

// legacyProfileTargets 是旧 profile ID → (期望 provider, 目标 preset) 固定表
// （规则 1 的 §1.1 唯一表 + 规则 2 openai + 规则 3 的 Hybrid legacy profile）。
var legacyProfileTargets = map[string]profileTarget{
	"profile_openai_openai_v1":             {provider: "openai", preset: "generic_openai_v1"},
	"profile_gpt_openai_v1":                {provider: "gpt", preset: "gpt_openai_v1"},
	"profile_xai_openai_v1":                {provider: "xai", preset: "xai_openai_v1"},
	"profile_deepseek_openai_v1":           {provider: "deepseek", preset: "deepseek_openai_v1"},
	"profile_deepseek_anthropic_v1":        {provider: "deepseek", preset: "deepseek_anthropic_v1"},
	"profile_anthropic_anthropic_v1":       {provider: "anthropic", preset: "anthropic_v1"},
	"profile_gemini_openai_chat_v1beta":    {provider: "gemini", preset: "gemini_openai_chat_v1beta"},
	"profile_gemini_native_v1beta":         {provider: "gemini", preset: "gemini_native_v1beta"},
	"profile_glm_general_openai_v1":        {provider: "glm", preset: "glm_general_openai_v1"},
	"profile_glm_coding_openai_v1":         {provider: "glm", preset: "glm_coding_openai_v1"},
	"profile_glm_coding_anthropic_v1":      {provider: "glm", preset: "glm_coding_anthropic_v1"},
	"profile_minimax_openai_v1":            {provider: "minimax", preset: "minimax_media"},
	"profile_volcengine_openai_v1":         {provider: "volcengine", preset: "volcengine_ark"},
	"profile_qwen_openai_v1":               {provider: "qwen", preset: "qwen_dashscope"},
	"profile_hybrid_openai_chat_v1":        {provider: "hybrid", preset: "generic_openai_v1"},
	"profile_hybrid_anthropic_messages_v1": {provider: "hybrid", preset: "generic_anthropic_v1"},
	"profile_hybrid_gemini_native_v1beta":  {provider: "hybrid", preset: "generic_gemini_v1beta"},
}

type hybridFamilyTarget struct {
	preset       string
	targetFamily string
}

// hybridFamilyPresets 是规则 3 的旧 upstream_endpoint_family →
// (connection_preset_code, mapping target family) 固定表。
var hybridFamilyPresets = map[string]hybridFamilyTarget{
	"chat_completions":        {preset: "generic_openai_v1", targetFamily: "chat_completions"},
	"responses":               {preset: "generic_openai_v1", targetFamily: "responses"},
	"video_generation":        {preset: "generic_openai_v1", targetFamily: "video_generation"},
	"tts":                     {preset: "generic_openai_v1", targetFamily: "tts"},
	"messages":                {preset: "generic_anthropic_v1", targetFamily: "messages"},
	"generate_content":        {preset: "generic_gemini_v1beta", targetFamily: "generate_content"},
	"stream_generate_content": {preset: "generic_gemini_v1beta", targetFamily: "generate_content"},
}

// hybridFamilyDomains 是规则 3 多域检测的 family → capability domain 映射。
var hybridFamilyDomains = map[string]string{
	"chat_completions": "chat", "responses": "chat", "messages": "chat",
	"generate_content": "chat", "stream_generate_content": "chat",
	"video_generation": "video", "tts": "audio",
}

// mappingSourceFamilies 是 §5.1 的 7 个可映射 source family。
var mappingSourceFamilies = map[string]bool{
	"chat_completions": true, "responses": true, "messages": true,
	"generate_content": true, "stream_generate_content": true,
	"video_generation": true, "tts": true,
}

// protocolEndpointModes 与 maintenance/internal/schema 的 seed 投影
// （modelSeedProtocolEndpointModes）同词表：supported_api_protocols_json →
// §2.2 mode 集合（规则 10 切片口径）。
var protocolEndpointModes = map[string][]string{
	"chat_completions":        {"chat_json", "chat_sse"},
	"responses":               {"responses_json", "responses_sse"},
	"messages":                {"messages_json", "messages_sse"},
	"generate_content":        {"generate_content_json", "generate_content_sse"},
	"stream_generate_content": {"generate_content_sse"},
	"interactions":            {"interactions_json", "interactions_sse"},
	"message_token_counting":  {"message_token_counting"},
	"count_tokens":            {"count_tokens"},
	"embed_content":           {"embed_content"},
	"images":                  {"images_json"},
	"video":                   {"video_create", "video_get", "video_content", "video_cancel"},
	"realtime":                {"realtime_session"},
	"audio_speech":            {"audio_speech"},
	"audio_transcription":     {"audio_transcription_json"},
	"completions":             {},
}

// modelPriceColumns 是规则 10 unpriced 判定的 13 个价格/倍率列。
var modelPriceColumns = []string{
	"input_usd_per_1m", "output_usd_per_1m", "cached_input_usd_per_1m",
	"cache_write_usd_per_1m", "cache_write_1h_usd_per_1m",
	"cache_storage_usd_per_1m_per_hour", "long_context_input_cost_multiplier",
	"long_context_output_cost_multiplier", "image_input_usd_per_1m",
	"image_output_usd_per_1m", "audio_input_usd_per_1m", "audio_output_usd_per_1m",
	"output_usd_per_image",
}

// 规则 12 reason 词表（设计 §10 :515 固定枚举）。
const (
	HealthReasonLegacyProfileUnsupported = "legacy_profile_unsupported"
	HealthReasonBaseURLMissing           = "base_url_missing"
	HealthReasonBaseURLInvalid           = "base_url_invalid"
	HealthReasonModeUnsupported          = "mode_unsupported"
	HealthReasonConnectionZero           = "connection_zero"
	HealthReasonConnectionMany           = "connection_many"
	HealthReasonModelZero                = "model_zero"
	HealthReasonModelMany                = "model_many"
	HealthReasonModelModeMismatch        = "model_mode_mismatch"
)

// 账户/连接阻塞 reason 词表（manifest errors）。
const (
	ReasonProviderUnsupported       = "provider_unsupported"
	ReasonAccountTypeUnsupported    = "account_type_unsupported"
	ReasonAccountTypeDriverMismatch = "account_type_driver_incompatible"
	ReasonOAuthTypeUnresolvable     = "oauth_type_unresolvable_dry_run"
	ReasonCapabilityDomainInvalid   = "capability_domain_invalid"
	ReasonAuthInstanceInvalid       = "authorization_instance_invalid"
	ReasonHybridMultiDomain         = "hybrid_multi_capability_domain"
	ReasonLegacyProfileUnsupported  = "legacy_profile_unsupported"
	ReasonPresetDomainEmpty         = "preset_domain_intersection_empty"
)

// ── 判定上下文与输出 ──────────────────────────────────────────────────────

type derivedConnection struct {
	id            string
	preset        string
	surface       string
	baseURLRaw    string // 规则 12 步骤③/④ 取得的原始值（"" = 无）
	baseURLSource string // legacy_profile | registry_default | ""
	modes         map[string]bool
}

type modelInfo struct {
	id           string
	sourceTable  string
	providerCode string
	model        string
	vendor       string // "" = NULL
	scope        string
	owner        string // system_account_id（"" = NULL）
	status       string
	currency     string
	modes        []string
	blocked      bool
	errors       []string
}

type accountState struct {
	id               string
	providerCode     string
	owner            string
	authInstance     bool
	identityBlocked  bool
	rule             int
	errors           []string
	connections      []derivedConnection
	connectionsValid bool
	supportedModels  map[string]bool // 已映射目标模型 ID（规则 5）
	targetFields     map[string]string
}

type mappingRowRef struct {
	accountID      string
	providerCode   string
	sourceModel    string
	sourceFamily   string
	upstreamModel  string
	upstreamFamily string
	enabled        bool
}

type ruleOutput struct {
	rows            []RowDisposition
	blocked         BlockedCounts
	generated       []GeneratedTargetTable
	publicConflicts []PublicModelConflict
	healthBlocks    []HealthCheckBlock
	notes           []string
}

type rulesContext struct {
	source *sourceData
	now    time.Time

	modelsByProviderModel map[string][]*modelInfo
	models                []*modelInfo
	accountStates         map[string]*accountState
	blockedGroups         map[string]bool
	// mappingM 是规则 13 的 M 集合：(account, wire) → source family 集合。
	mappingM map[string]map[string]bool
}

// applyRules 对已加载 source 数据执行规则 0-13（纯内存判定，零 I/O）。
func applyRules(source *sourceData, now time.Time) *ruleOutput {
	ctx := &rulesContext{
		source:                source,
		now:                   now,
		modelsByProviderModel: map[string][]*modelInfo{},
		accountStates:         map[string]*accountState{},
		blockedGroups:         map[string]bool{},
		mappingM:              map[string]map[string]bool{},
	}
	out := &ruleOutput{}
	ctx.applyProviderRows(out)
	ctx.applyProfileRows(out)
	ctx.applyProfileFamilyRows(out)
	ctx.applyModelRows(out)
	ctx.applyAccountRows(out)
	ctx.applySupportedModelRows(out)
	ctx.applyMappingRows(out)
	ctx.applyHealthChecks(out)
	ctx.applyPublicModelConflicts(out)
	ctx.applyGroupRows(out)
	ctx.applyGroupAccountRows(out)
	ctx.applyRouteStrategyGroupRows(out)
	ctx.applyHistoryRows(out)
	out.notes = sliceNotes()
	return out
}

// sliceNotes 是随 manifest 输出的切片边界与判定口径说明。
func sliceNotes() []string {
	return []string{
		"切片边界：本 manifest 由 --migrate-provider-connection-model --dry-run 产出；正式执行（manifest+backup 消费分支）尚未交付，历史 usage/audit 列改造完成后提供。",
		"凭据口径：dry-run 不解密旧 credentials_encrypted；credential fingerprint = SHA-256(ciphertext UTF-8 bytes) 十六进制。",
		"规则 12 口径：Base URL 优先级跳过步骤②（credentials JSON 需解密），按 ①源列（旧 schema 无该列）→③profile base_url→④registry 默认值执行；generic_* preset 的 registry 默认值为 SQL NULL。",
		"oauth_type 口径：google_oauth 账户的 oauth_type 提升需解密旧 JSON，dry-run 不可判定，fail-closed 计入 accounts_blocked（reason=oauth_type_unresolvable_dry_run）。",
		"规则 10 口径：supported_endpoint_modes_json 由 supported_api_protocols_json 经 seed 同表 protocol→mode 映射推导；未知协议或空推导计 models_blocked（manifest 修复补集属正式执行切片）。",
		"连接口径：连接 enabled 恒 true；连接 mode 集合 = preset 词表 ∩ 域 lane（旧账户已启用 mode 在加密凭据内不可读）；每 (账户, preset) 恰一行连接，connection ID 为确定性派生（sha256(account_id|preset) 截断）。",
		"规则 11 口径：历史 usage/audit/check 行按表清点并全部标 blocked，不进 source snapshot/hash；逐行 source identity 转换待正式执行切片。",
		"规则 9 口径：本切片读取表集不含 resource_authorizations；授权实例只校验三字段完整性、来源账户存在且非实例、owner 与来源账户 system_account_id 一致，授权行存在性/resource_type/grantee 校验待正式执行切片。",
	}
}

// ── providers / profiles / families ─────────────────────────────────────

func (c *rulesContext) applyProviderRows(out *ruleOutput) {
	table := c.source.table("providers")
	for index := range table.rows {
		row := &table.rows[index]
		code := cellString(row.values["code"])
		rowOut := RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			Rule:        1,
			Evidence:    []Evidence{newEvidence(table, row, []string{"code", "name"}, c.now)},
		}
		switch {
		case realVendorCodes[code]:
			rowOut.Disposition = DispositionMapped
			rowOut.TargetTable = "vendors"
			rowOut.TargetPK = []string{code}
			rowOut.TargetFields = map[string]string{"code": code, "name": cellString(row.values["name"])}
			rowOut.RuleNote = "规则 1/§9.1：真实厂商行更名为 vendors，保留 id/code"
		case code == "openai" || code == "hybrid":
			rowOut.Disposition = DispositionMapped
			rowOut.RuleNote = "§9.1：旧 openai/hybrid 供应商行按设计删除（无目标行）；身份迁移由账户规则 2/3 承接"
		default:
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = []string{"provider_row_unknown"}
			rowOut.RuleNote = "providers 行不在 §1.1 九真实厂商与 §9.1 退役集合内，正式执行前需人工裁决"
		}
		out.rows = append(out.rows, rowOut)
	}
}

func (c *rulesContext) applyProfileRows(out *ruleOutput) {
	table := c.source.table("provider_protocol_profiles")
	for index := range table.rows {
		row := &table.rows[index]
		out.rows = append(out.rows, RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			Disposition: DispositionMapped,
			Rule:        1,
			RuleNote:    "§9.3：protocol profile 行由编译期 connection preset registry 取代（无目标行）；profile 判定见账户规则 1/2/3",
			TargetFields: map[string]string{
				"profile_id":    cellString(row.values["id"]),
				"provider_code": cellString(row.values["provider_code"]),
			},
			Evidence: []Evidence{newEvidence(table, row, []string{"id", "provider_code", "protocol_code", "protocol_version", "base_url"}, c.now)},
		})
	}
}

func (c *rulesContext) applyProfileFamilyRows(out *ruleOutput) {
	table := c.source.table("provider_protocol_profile_families")
	for index := range table.rows {
		row := &table.rows[index]
		out.rows = append(out.rows, RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			Disposition: DispositionMapped,
			Rule:        1,
			RuleNote:    "§9.3：profile family 绑定行随 profile 面退役（无目标行）",
		})
	}
}

// ── 规则 10：模型合并 ─────────────────────────────────────────────────────

func (c *rulesContext) applyModelRows(out *ruleOutput) {
	identityRows := map[string][]*modelInfo{}
	for _, tableName := range []string{"provider_model_catalog", "custom_provider_models"} {
		table := c.source.table(tableName)
		for index := range table.rows {
			row := &table.rows[index]
			info := c.buildModelInfo(table, row)
			c.models = append(c.models, info)
			key := info.providerCode + "\x00" + info.model
			c.modelsByProviderModel[key] = append(c.modelsByProviderModel[key], info)
			identity := info.vendor + "\x00" + info.scope + "\x00" + info.owner + "\x00" + info.model
			identityRows[identity] = append(identityRows[identity], info)
		}
	}
	// 同一 (vendor, scope, owner, upstream_model) 身份出现多行 → 规则 10 的
	// 两源冲突，全部阻塞（manifest 逐字段指定保留行属正式执行切片）。
	for _, infos := range identityRows {
		if len(infos) < 2 {
			continue
		}
		for _, info := range infos {
			info.blocked = true
			info.errors = append(info.errors, "duplicate_model_identity")
		}
	}
	for _, info := range c.models {
		rowOut := RowDisposition{
			SourceTable:  info.sourceTable,
			SourcePK:     []string{info.id},
			TargetTable:  "models",
			TargetPK:     []string{info.id},
			TargetFields: c.modelTargetFields(info),
			Rule:         10,
			RuleNote:     "规则 10：模型表合并按原模型 ID 保留 ID",
		}
		if info.blocked {
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = info.errors
			out.blocked.ModelsBlocked++
		} else {
			rowOut.Disposition = DispositionMerged
		}
		out.rows = append(out.rows, rowOut)
	}
}

func (c *rulesContext) buildModelInfo(table *sourceTable, row *sourceRow) *modelInfo {
	info := &modelInfo{
		id:           cellString(row.values["id"]),
		sourceTable:  table.def.name,
		providerCode: cellString(row.values["provider_code"]),
		model:        cellString(row.values["model"]),
		status:       cellString(row.values["status"]),
	}
	// vendor 推导：9 真实厂商原样；openai/hybrid → NULL vendor（与账户规则
	// 2/3 同口径）；其余未知 → 阻塞（不能唯一确定 vendor）。
	switch {
	case realVendorCodes[info.providerCode]:
		info.vendor = info.providerCode
	case info.providerCode == "openai" || info.providerCode == "hybrid":
		info.vendor = ""
	default:
		info.blocked = true
		info.errors = append(info.errors, "model_vendor_unsupported")
	}
	if table.def.name == "provider_model_catalog" {
		info.scope = "builtin"
		info.currency = "USD"
	} else {
		info.scope = cellString(row.values["scope"])
		info.owner = cellString(row.values["system_account_id"])
		info.currency = cellString(row.values["currency"])
		if info.scope != "personal" && info.scope != "global" {
			info.blocked = true
			info.errors = append(info.errors, "model_scope_invalid")
		}
		if info.scope == "personal" && info.owner == "" {
			info.blocked = true
			info.errors = append(info.errors, "model_personal_owner_missing")
		}
	}
	modes, deriveErr := deriveEndpointModes(cellString(row.values["supported_api_protocols_json"]))
	if deriveErr != nil {
		info.blocked = true
		info.errors = append(info.errors, "supported_endpoint_modes_missing", deriveErr.Error())
	} else {
		info.modes = modes
	}
	// 文本 mode 无 vendor 价格 → models_blocked（unpriced 判定 = 13 价格列全
	// NULL 且 service_tier_prices_json 为空对象；§10 口径的切片简化）。
	if !info.blocked && rowHasTextMode(info.modes) && modelUnpriced(row) {
		info.blocked = true
		info.errors = append(info.errors, "text_mode_unpriced")
	}
	return info
}

func (c *rulesContext) modelTargetFields(info *modelInfo) map[string]string {
	fields := map[string]string{
		"id":             info.id,
		"vendor_code":    orNull(info.vendor),
		"public_model":   info.model,
		"upstream_model": info.model,
		"scope":          info.scope,
		"status":         info.status,
		"currency":       orNull(info.currency),
	}
	if info.sourceTable == "provider_model_catalog" {
		fields["scope_note"] = "规则 10：provider_model_catalog.scope 固定迁为 builtin；缺 currency 补 USD"
	} else {
		fields["scope_note"] = "规则 10：custom scope/owner/currency 原样保留"
	}
	if len(info.modes) > 0 {
		fields["supported_endpoint_modes_json"] = stringifyModes(info.modes)
	} else {
		fields["supported_endpoint_modes_json"] = "缺失（需 manifest 逐行指定，规则 10）"
	}
	return fields
}

// deriveEndpointModes 从 supported_api_protocols_json 推导 §2.2 mode 集合
// （ASCII 升序、唯一）；未知协议或空推导返回错误（= 缺失，规则 10）。
func deriveEndpointModes(protocolsJSON string) ([]string, error) {
	trimmed := strings.TrimSpace(protocolsJSON)
	if trimmed == "" {
		return nil, fmt.Errorf("supported_api_protocols_json 为空")
	}
	var protocols []string
	if err := json.Unmarshal([]byte(trimmed), &protocols); err != nil {
		return nil, fmt.Errorf("supported_api_protocols_json 解析失败: %w", err)
	}
	seen := map[string]bool{}
	modes := []string{}
	for _, protocol := range protocols {
		declared, known := protocolEndpointModes[protocol]
		if !known {
			return nil, fmt.Errorf("未知 supported API protocol %q", protocol)
		}
		for _, mode := range declared {
			if !seen[mode] {
				seen[mode] = true
				modes = append(modes, mode)
			}
		}
	}
	if len(modes) == 0 {
		return nil, fmt.Errorf("协议声明未推导出任何 endpoint mode")
	}
	sort.Strings(modes)
	return modes, nil
}

func rowHasTextMode(modes []string) bool {
	for _, mode := range modes {
		if textModes[mode] {
			return true
		}
	}
	return false
}

// modelUnpriced 判定目录行价格字段是否全空（13 价格列 NULL 且
// service_tier_prices_json 为空对象/数组/空串）。
func modelUnpriced(row *sourceRow) bool {
	for _, column := range modelPriceColumns {
		if row.values[column].Type != CellNull {
			return false
		}
	}
	tier := strings.TrimSpace(cellString(row.values["service_tier_prices_json"]))
	return tier == "" || tier == "{}" || tier == "[]"
}

func stringifyModes(modes []string) string {
	encoded, err := json.Marshal(modes)
	if err != nil {
		panic(fmt.Sprintf("providerconnectionmigration: 序列化 mode 集合失败: %v", err))
	}
	return string(encoded)
}

// ── 规则 0/1/1a/1b/2/3/7/9：账户判定 ─────────────────────────────────────

func (c *rulesContext) applyAccountRows(out *ruleOutput) {
	table := c.source.table("accounts")
	for index := range table.rows {
		row := &table.rows[index]
		state := c.buildAccountState(table, row, out)
		c.accountStates[state.id] = state
		rowOut := RowDisposition{
			SourceTable:  table.def.name,
			SourcePK:     row.pkDisplay(),
			TargetTable:  "accounts",
			TargetPK:     []string{state.id},
			TargetFields: state.targetFields,
			Rule:         state.rule,
			Evidence: []Evidence{newEvidence(table, row, []string{
				"id", "provider_code", "provider_protocol_profile_id", "type",
				"capability_domain", "config_revision", "health_check_endpoint_mode",
				"health_check_model",
			}, c.now)},
		}
		if state.identityBlocked {
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = state.errors
		} else {
			rowOut.Disposition = DispositionMapped
			rowOut.Errors = state.errors
			for _, conn := range state.connections {
				rowOut.TargetIDs = append(rowOut.TargetIDs, conn.id)
			}
		}
		out.rows = append(out.rows, rowOut)
	}
	c.emitGeneratedTargets(out)
}

func (c *rulesContext) buildAccountState(table *sourceTable, row *sourceRow, out *ruleOutput) *accountState {
	state := &accountState{
		id:              cellString(row.values["id"]),
		owner:           cellString(row.values["system_account_id"]),
		supportedModels: map[string]bool{},
		targetFields:    map[string]string{},
	}
	rawProvider := row.values["provider_code"]
	provider := cellString(rawProvider)
	state.providerCode = provider
	state.targetFields["credential_fingerprint"] = credentialFingerprint(row.values["credentials_encrypted"])

	// 规则 0：provider_code binary 精确校验（NULL/空/首尾空白/大小写变体/
	// 未知一律 accounts_blocked，每无效账户只计 1 次，子表行不重复计数）。
	if rawProvider.Type == CellNull || strings.TrimSpace(provider) == "" || strings.TrimSpace(provider) != provider || !legacyProviderAllowlist[provider] {
		state.identityBlocked = true
		state.rule = 0
		state.errors = append(state.errors, ReasonProviderUnsupported)
		state.targetFields["provider_code_source"] = providerDisplay(rawProvider)
		out.blocked.AccountsBlocked++
		return state
	}

	// 授权实例判别（规则 9）：三字段按 SQL NULL/空串为缺席的切片口径。
	authID := cellString(row.values["authorization_instance_authorization_id"])
	sourceID := cellString(row.values["authorization_instance_source_account_id"])
	ownerID := cellString(row.values["authorization_instance_owner_system_account_id"])
	present := 0
	for _, value := range []string{authID, sourceID, ownerID} {
		if value != "" {
			present++
		}
	}
	if present > 0 {
		state.authInstance = true
		state.rule = 9
		if present < 3 {
			state.identityBlocked = true
			state.errors = append(state.errors, ReasonAuthInstanceInvalid+":authorization_fields_incomplete")
			out.blocked.AccountsBlocked++
			return state
		}
		sourceRow := c.findAccountRow(sourceID)
		if sourceRow == nil {
			state.identityBlocked = true
			state.errors = append(state.errors, ReasonAuthInstanceInvalid+":source_account_missing")
			out.blocked.AccountsBlocked++
			return state
		}
		sourceAuthID := cellString(sourceRow.values["authorization_instance_authorization_id"])
		sourceOwner := cellString(sourceRow.values["system_account_id"])
		if sourceAuthID != "" {
			state.identityBlocked = true
			state.errors = append(state.errors, ReasonAuthInstanceInvalid+":source_is_authorization_instance")
			out.blocked.AccountsBlocked++
			return state
		}
		if sourceOwner != ownerID {
			state.identityBlocked = true
			state.errors = append(state.errors, ReasonAuthInstanceInvalid+":source_owner_mismatch")
			out.blocked.AccountsBlocked++
			return state
		}
		// 有效授权实例：不复制连接面（规则 9）；旧 type 原值只写 discarded。
		state.targetFields["vendor_code"] = "NULL"
		state.targetFields["account_type"] = "NULL"
		state.targetFields["oauth_type"] = "NULL"
		state.targetFields["config_revision"] = "0"
		state.targetFields["authorization_instance_authorization_id"] = authID
		state.targetFields["authorization_instance_source_account_id"] = sourceID
		state.targetFields["authorization_instance_owner_system_account_id"] = ownerID
		state.targetFields["discarded_legacy_fields"] = "type=" + cellString(row.values["type"]) + "（规则 1a：实例旧 type 原值仅入 discarded）"
		state.targetFields["connections_note"] = "规则 9：授权实例不复制 connections/models/mappings/health-check"
		return state
	}

	// 规则 1a：旧 type binary 迁移 + driver 相容性。
	accountType := cellString(row.values["type"])
	switch accountType {
	case "api_key", "oauth", "google_oauth":
	default:
		state.identityBlocked = true
		state.rule = 1
		state.errors = append(state.errors, ReasonAccountTypeUnsupported)
		out.blocked.AccountsBlocked++
		return state
	}
	driver := provider
	if provider == "openai" || provider == "hybrid" {
		driver = "generic_http"
	}
	if !driverAccountTypes[driver][accountType] {
		state.identityBlocked = true
		state.rule = 1
		state.errors = append(state.errors, ReasonAccountTypeDriverMismatch)
		out.blocked.AccountsBlocked++
		return state
	}
	// oauth_type 提升需解密（切片口径 fail-closed）；api_key/oauth 目标列
	// 固定 SQL NULL。
	if accountType == "google_oauth" {
		state.identityBlocked = true
		state.rule = 1
		state.errors = append(state.errors, ReasonOAuthTypeUnresolvable)
		out.blocked.AccountsBlocked++
		return state
	}

	// 规则 7：能力域按已物化 capability_domain 原样迁移；缺失/未知阻塞。
	domain := cellString(row.values["capability_domain"])
	if _, known := domainLanes[domain]; !known {
		state.identityBlocked = true
		state.rule = 7
		state.errors = append(state.errors, ReasonCapabilityDomainInvalid)
		out.blocked.AccountsBlocked++
		return state
	}

	// 规则 1b：config_revision = max(1, source)；NULL/0/负数写 1。
	sourceRevision := 1
	if row.values["config_revision"].Type == CellInteger {
		if parsed, err := strconv.ParseInt(row.values["config_revision"].Str, 10, 64); err == nil && parsed > 1 {
			sourceRevision = int(parsed)
		}
	}

	// 规则 1/2/3：vendor/driver/preset 推导与连接生成。
	switch provider {
	case "hybrid":
		state.rule = 3
		c.buildHybridAccount(state, table, row, domain, sourceRevision, out)
		return state
	case "openai":
		state.rule = 2
	default:
		state.rule = 1
	}
	profileID := cellString(row.values["provider_protocol_profile_id"])
	target, known := legacyProfileTargets[profileID]
	switch {
	case !known || target.provider != provider:
		state.connectionsValid = false
		state.errors = append(state.errors, ReasonLegacyProfileUnsupported)
		out.blocked.ConnectionsBlocked++
	case !intersectNonEmpty(presetVocabularies[target.preset], domainLanes[domain]):
		state.connectionsValid = false
		state.errors = append(state.errors, ReasonPresetDomainEmpty)
		out.blocked.ConnectionsBlocked++
	default:
		state.connectionsValid = true
		state.connections = append(state.connections, c.newDerivedConnection(state.id, target.preset, table, row))
	}
	fillResourceAccountFields(state, provider, driver, accountType, domain, sourceRevision)
	return state
}

// buildHybridAccount 实现规则 3 的 Hybrid 判定：映射族 → preset 集合、多域
// 检测、mode 交集与确定性连接生成。
func (c *rulesContext) buildHybridAccount(state *accountState, table *sourceTable, row *sourceRow, domain string, sourceRevision int, out *ruleOutput) {
	fillResourceAccountFields(state, "hybrid", "generic_http", cellString(row.values["type"]), domain, sourceRevision)
	profileID := cellString(row.values["provider_protocol_profile_id"])
	// 账户级默认 profile：表外 profile_hybrid_* → connections_blocked（规则 3，
	// 不按前缀猜测）。
	defaultPreset := ""
	if profileID != "" {
		target, known := legacyProfileTargets[profileID]
		if !known || target.provider != "hybrid" {
			state.connectionsValid = false
			state.errors = append(state.errors, ReasonLegacyProfileUnsupported)
			out.blocked.ConnectionsBlocked++
			return
		}
		defaultPreset = target.preset
	}
	// 多域检测：全部 mapping 必须落在同一 capability domain，否则该账户一次
	// accounts_blocked，子 mapping 不重复计数（规则 3）。
	domains := map[string]bool{}
	for _, mapping := range c.mappingsByAccount(state.id) {
		if familyDomain, known := hybridFamilyDomains[mapping.upstreamFamily]; known {
			domains[familyDomain] = true
		}
	}
	if len(domains) > 1 {
		state.identityBlocked = true
		state.errors = append(state.errors, ReasonHybridMultiDomain)
		out.blocked.AccountsBlocked++
		return
	}
	// 映射族 → preset 集合（规则 3 的 upstream_endpoint_family 表）。
	presets := map[string]bool{}
	for _, mapping := range c.mappingsByAccount(state.id) {
		if target, known := hybridFamilyPresets[mapping.upstreamFamily]; known {
			presets[target.preset] = true
		}
	}
	if len(presets) == 0 && defaultPreset != "" {
		// 无任何可用 mapping 目标族时回落账户级默认 profile 的 preset。
		presets[defaultPreset] = true
	}
	if len(presets) == 0 {
		state.connectionsValid = false
		state.errors = append(state.errors, ReasonLegacyProfileUnsupported)
		out.blocked.ConnectionsBlocked++
		return
	}
	ordered := make([]string, 0, len(presets))
	for preset := range presets {
		ordered = append(ordered, preset)
	}
	sort.Strings(ordered)
	for _, preset := range ordered {
		if !intersectNonEmpty(presetVocabularies[preset], domainLanes[domain]) {
			state.connectionsValid = false
			state.errors = append(state.errors, ReasonPresetDomainEmpty)
			out.blocked.ConnectionsBlocked++
			return
		}
	}
	state.connectionsValid = true
	for _, preset := range ordered {
		state.connections = append(state.connections, c.newDerivedConnection(state.id, preset, table, row))
	}
}

// fillResourceAccountFields 填充资源账户的目标字段（规则 1/1a/1b/2/3/7）。
func fillResourceAccountFields(state *accountState, provider, driver, accountType, domain string, sourceRevision int) {
	state.targetFields["vendor_code"] = orNull(vendorOfProvider(provider))
	state.targetFields["connection_driver_code"] = driver
	state.targetFields["account_type"] = accountType
	state.targetFields["oauth_type"] = "NULL"
	state.targetFields["capability_domain"] = domain
	state.targetFields["source_config_revision"] = strconv.Itoa(sourceRevision)
	state.targetFields["target_config_revision"] = strconv.Itoa(sourceRevision)
	state.targetFields["config_revision_rule"] = "规则 1b：target = max(1, source)，不隐式递增"
}

func vendorOfProvider(provider string) string {
	if realVendorCodes[provider] {
		return provider
	}
	return "" // openai/hybrid → generic_http 账户 vendor NULL
}

// newDerivedConnection 生成一条确定性派生连接：规则 12 的 Base URL 优先级
// ③profile base_url → ④registry 默认值（步骤①旧 schema 无账户 Base URL 列、
// 步骤②credentials JSON 需解密，均按切片口径跳过并记录于 notes）。
func (c *rulesContext) newDerivedConnection(accountID, preset string, table *sourceTable, row *sourceRow) derivedConnection {
	conn := derivedConnection{
		id:      deterministicConnectionID(accountID, preset),
		preset:  preset,
		surface: presetSurfaces[preset],
		modes:   map[string]bool{},
	}
	if table != nil && row != nil {
		if profileID := cellString(row.values["provider_protocol_profile_id"]); profileID != "" {
			if profileRow := c.findProfileRow(profileID); profileRow != nil {
				conn.baseURLRaw = cellString(profileRow.values["base_url"])
				conn.baseURLSource = "legacy_profile"
			}
		}
	}
	if conn.baseURLRaw == "" {
		if def := presetDefaultBaseURLs[preset]; def != "" {
			conn.baseURLRaw = def
			conn.baseURLSource = "registry_default"
		}
	}
	for _, mode := range presetVocabularies[preset] {
		conn.modes[mode] = true
	}
	return conn
}

func (c *rulesContext) emitGeneratedTargets(out *ruleOutput) {
	connectionIDs := []string{}
	modeIDs := []string{}
	for _, state := range c.accountStates {
		for _, conn := range state.connections {
			connectionIDs = append(connectionIDs, conn.id)
			for _, mode := range sortedStringKeys(conn.modes) {
				modeIDs = append(modeIDs, conn.id+"/"+mode)
			}
		}
	}
	sort.Strings(connectionIDs)
	sort.Strings(modeIDs)
	if len(connectionIDs) > 0 {
		out.generated = append(out.generated, GeneratedTargetTable{
			TargetTable: "account_connections",
			Reason:      "规则 1/2/3：由账户 profile/映射族唯一推导的连接行（无 source row；确定性 ID）",
			IDs:         connectionIDs,
		})
	}
	if len(modeIDs) > 0 {
		out.generated = append(out.generated, GeneratedTargetTable{
			TargetTable: "account_endpoint_modes",
			Reason:      "规则 7 切片口径：preset 词表 ∩ 域 lane 交集（<connection_id>/<mode> 复合标识）",
			IDs:         modeIDs,
		})
	}
}

// ── 规则 5：account_supported_models ─────────────────────────────────────

func (c *rulesContext) applySupportedModelRows(out *ruleOutput) {
	table := c.source.table("account_supported_models")
	for index := range table.rows {
		row := &table.rows[index]
		accountID := cellString(row.values["account_id"])
		state := c.accountStates[accountID]
		rowOut := RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			TargetTable: "account_supported_models",
			TargetPK:    row.pkDisplay(),
			Rule:        5,
			TargetFields: map[string]string{
				"account_id":    accountID,
				"provider_code": cellString(row.values["provider_code"]),
				"model":         cellString(row.values["model"]),
			},
		}
		switch {
		case state == nil:
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = []string{"supported_model_account_missing"}
			out.blocked.SupportedModelsBlocked++
		case state.identityBlocked:
			rowOut.Disposition = DispositionBlocked
			rowOut.RuleNote = "规则 0：无效账户的子表行不重复计数（supported models/mappings/health_checks 不加）"
		default:
			key := cellString(row.values["provider_code"]) + "\x00" + cellString(row.values["model"])
			candidates := c.modelsByProviderModel[key]
			switch {
			case len(candidates) == 0:
				rowOut.Disposition = DispositionBlocked
				rowOut.Errors = []string{"supported_model_target_missing"}
				out.blocked.SupportedModelsBlocked++
			case len(candidates) > 1:
				rowOut.Disposition = DispositionBlocked
				rowOut.Errors = []string{"supported_model_target_ambiguous"}
				out.blocked.SupportedModelsBlocked++
			case candidates[0].blocked:
				rowOut.Disposition = DispositionBlocked
				rowOut.Errors = []string{"supported_model_target_blocked"}
				out.blocked.SupportedModelsBlocked++
			case candidates[0].status != "active":
				rowOut.Disposition = DispositionBlocked
				rowOut.Errors = []string{"supported_model_target_inactive"}
				out.blocked.SupportedModelsBlocked++
			case !modelOwnerVisible(candidates[0], state.owner):
				rowOut.Disposition = DispositionBlocked
				rowOut.Errors = []string{"supported_model_target_invisible"}
				out.blocked.SupportedModelsBlocked++
			default:
				rowOut.Disposition = DispositionMapped
				rowOut.TargetFields["target_model_id"] = candidates[0].id
				rowOut.TargetIDs = []string{candidates[0].id}
				state.supportedModels[candidates[0].id] = true
			}
		}
		out.rows = append(out.rows, rowOut)
	}
}

func modelOwnerVisible(info *modelInfo, accountOwner string) bool {
	switch info.scope {
	case "builtin", "global":
		return true
	case "personal":
		return info.owner != "" && info.owner == accountOwner
	default:
		return false
	}
}

// ── 规则 3/4/6/6a：account_model_mappings ────────────────────────────────

func (c *rulesContext) mappingsByAccount(accountID string) []*mappingRowRef {
	var result []*mappingRowRef
	table := c.source.table("account_model_mappings")
	for index := range table.rows {
		if ref := newMappingRowRef(&table.rows[index]); ref.accountID == accountID {
			result = append(result, ref)
		}
	}
	return result
}

func newMappingRowRef(row *sourceRow) *mappingRowRef {
	return &mappingRowRef{
		accountID:      cellString(row.values["account_id"]),
		providerCode:   cellString(row.values["provider_code"]),
		sourceModel:    cellString(row.values["source_model"]),
		sourceFamily:   cellString(row.values["source_endpoint_family"]),
		upstreamModel:  cellString(row.values["upstream_model"]),
		upstreamFamily: cellString(row.values["upstream_endpoint_family"]),
		enabled:        cellString(row.values["enabled"]) == "1",
	}
}

func (c *rulesContext) applyMappingRows(out *ruleOutput) {
	table := c.source.table("account_model_mappings")
	for index := range table.rows {
		row := &table.rows[index]
		mapping := newMappingRowRef(row)
		state := c.accountStates[mapping.accountID]
		ruleNumber := 6
		ruleNote := "规则 6/6a：source_model 原样转字符串（不要求匹配目录）；target connection/model/family/mode 全部校验"
		if state != nil && state.providerCode == "hybrid" {
			ruleNumber = 4
			ruleNote = "规则 3/4：Hybrid 映射目标按 (provider_code, upstream_model) 精确匹配旧模型行，经规则 3 表定 preset"
		}
		fields := map[string]string{
			"account_id":               mapping.accountID,
			"source_model":             mapping.sourceModel,
			"source_endpoint_family":   mapping.sourceFamily,
			"upstream_endpoint_family": mapping.upstreamFamily,
			"enabled":                  boolDisplay(mapping.enabled),
		}
		rowOut := RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			TargetTable: "account_entry_mappings",
			TargetPK:    row.pkDisplay(),
			Rule:        ruleNumber,
			RuleNote:    ruleNote,
			TargetFields: fields,
			Evidence: []Evidence{newEvidence(table, row, []string{
				"account_id", "provider_code", "source_model", "source_endpoint_family",
				"upstream_model", "upstream_endpoint_family", "enabled",
			}, c.now)},
		}
		switch {
		case state == nil:
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = []string{"mapping_account_missing"}
			out.blocked.MappingsBlocked++
		case state.identityBlocked:
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = state.errors
			rowOut.RuleNote = joinNote(rowOut.RuleNote, "规则 0：无效账户的子表行不重复计数")
		default:
			if blockedReason := c.resolveMapping(mapping, state, fields); blockedReason != "" {
				rowOut.Disposition = DispositionBlocked
				rowOut.Errors = []string{blockedReason}
				out.blocked.MappingsBlocked++
			} else {
				rowOut.Disposition = DispositionMapped
				rowOut.TargetIDs = []string{fields["target_connection_id"], fields["target_model_id"]}
				if mapping.enabled {
					key := mapping.accountID + "\x00" + mapping.sourceModel
					if c.mappingM[key] == nil {
						c.mappingM[key] = map[string]bool{}
					}
					c.mappingM[key][mapping.sourceFamily] = true
				}
			}
		}
		out.rows = append(out.rows, rowOut)
	}
}

// resolveMapping 完成规则 6a 的 target connection/model/family/mode 校验；
// 返回空串表示映射可保留（enabled 与否皆同：disabled mapping 仍必须有合法
// target），否则返回阻塞 reason。
func (c *rulesContext) resolveMapping(mapping *mappingRowRef, state *accountState, fields map[string]string) string {
	if !mappingSourceFamilies[mapping.sourceFamily] {
		return "mapping_source_family_unsupported"
	}
	target, known := hybridFamilyPresets[mapping.upstreamFamily]
	if !known {
		return "mapping_upstream_family_unsupported"
	}
	fields["target_endpoint_family"] = target.targetFamily
	// 规则 4：目标模型按 (provider_code, upstream_model) 精确匹配旧模型行。
	candidates := c.modelsByProviderModel[mapping.providerCode+"\x00"+mapping.upstreamModel]
	switch {
	case len(candidates) == 0:
		return "mapping_target_model_missing"
	case len(candidates) > 1:
		return "mapping_target_model_ambiguous"
	}
	model := candidates[0]
	if model.blocked {
		return "mapping_target_model_blocked"
	}
	if model.status != "active" {
		return "mapping_target_model_inactive"
	}
	if !modelOwnerVisible(model, state.owner) {
		return "mapping_target_model_invisible"
	}
	fields["target_model_id"] = model.id
	// target connection：账户的 derived connection（规则 3 生成的 preset 行）。
	var conn *derivedConnection
	for index := range state.connections {
		if state.connections[index].preset == target.preset {
			conn = &state.connections[index]
			break
		}
	}
	if conn == nil {
		return "mapping_target_connection_missing"
	}
	fields["target_connection_id"] = conn.id
	fields["target_connection_preset_code"] = conn.preset
	// 规则 6a：至少一个可执行 mode（target model modes ∩ connection modes）。
	if !intersectNonEmpty(model.modes, sortedStringKeys(conn.modes)) {
		return "mapping_target_mode_unsupported"
	}
	return ""
}

// ── 规则 12：health-check 三字段 ─────────────────────────────────────────

func (c *rulesContext) applyHealthChecks(out *ruleOutput) {
	accounts := c.source.table("accounts")
	for _, id := range sortedAccountIDs(c.accountStates) {
		state := c.accountStates[id]
		if state.authInstance || state.identityBlocked || !state.connectionsValid {
			// 规则 9/12：实例不生成 health-check；身份或连接面已阻塞的账户不
			// 重复计入 health_checks_blocked（设计 :514/:517）。
			continue
		}
		row := c.findAccountRow(id)
		if row == nil {
			continue
		}
		reason, candidateConnIDs, candidateModelIDs := c.resolveHealthCheck(state, row)
		if reason == "" {
			continue
		}
		out.blocked.HealthChecksBlocked++
		modeHash := canonicalFieldHash(row.values["health_check_endpoint_mode"])
		modelHash := canonicalFieldHash(row.values["health_check_model"])
		if candidateConnIDs == nil {
			candidateConnIDs = []string{}
		}
		if candidateModelIDs == nil {
			candidateModelIDs = []string{}
		}
		out.healthBlocks = append(out.healthBlocks, HealthCheckBlock{
			ResourceAccountID:      state.id,
			Reason:                 reason,
			CandidateConnectionIDs: candidateConnIDs,
			CandidateModelIDs:      candidateModelIDs,
			SourceFieldsHash:       modeHash + modelHash,
		})
		for rowIndex := range out.rows {
			rowOut := &out.rows[rowIndex]
			if rowOut.SourceTable == accounts.def.name && len(rowOut.SourcePK) == 1 && rowOut.SourcePK[0] == id {
				rowOut.RuleNote = joinNote(rowOut.RuleNote, "规则 12：health-check 解析失败 reason="+reason)
				break
			}
		}
	}
}

// resolveHealthCheck 按规则 12 固定顺序解析：旧 mode ∈ 25 值 → 候选
// connection 恰一个 → Base URL 存在且合法 → 旧模型 binary 唯一解析且
// active/owner-visible/属支持模型/声明该 mode。任一失败返回 reason（每资源
// 账户只计 1 次，三字段不得部分写入）。
func (c *rulesContext) resolveHealthCheck(state *accountState, row *sourceRow) (string, []string, []string) {
	oldMode := cellString(row.values["health_check_endpoint_mode"])
	if oldMode == "" || !mode25[oldMode] {
		return HealthReasonModeUnsupported, nil, nil
	}
	var candidates []derivedConnection
	for _, conn := range state.connections {
		if conn.modes[oldMode] {
			candidates = append(candidates, conn)
		}
	}
	connIDs := make([]string, 0, len(candidates))
	for _, conn := range candidates {
		connIDs = append(connIDs, conn.id)
	}
	switch len(candidates) {
	case 0:
		return HealthReasonConnectionZero, connIDs, nil
	case 1:
	default:
		return HealthReasonConnectionMany, connIDs, nil
	}
	conn := candidates[0]
	if conn.baseURLRaw == "" {
		return HealthReasonBaseURLMissing, connIDs, nil
	}
	if _, valid := canonicalBaseURL(conn.baseURLRaw); !valid {
		return HealthReasonBaseURLInvalid, connIDs, nil
	}
	oldModel := cellString(row.values["health_check_model"])
	if oldModel == "" {
		return HealthReasonModelZero, connIDs, nil
	}
	matches := c.modelsByProviderModel[state.providerCode+"\x00"+oldModel]
	modelIDs := make([]string, 0, len(matches))
	for _, info := range matches {
		modelIDs = append(modelIDs, info.id)
	}
	switch len(matches) {
	case 0:
		return HealthReasonModelZero, connIDs, modelIDs
	case 1:
	default:
		return HealthReasonModelMany, connIDs, modelIDs
	}
	model := matches[0]
	// 最终模型必须同时满足 active、owner-visible、属于该账户支持模型并声明
	// 该 mode；任一不满足归入 model_mode_mismatch（detail 由字段说明区分，
	// reason 枚举按设计 :515 固定词表收敛）。
	if model.blocked || model.status != "active" || !modelOwnerVisible(model, state.owner) ||
		!state.supportedModels[model.id] || !containsString(model.modes, oldMode) {
		return HealthReasonModelModeMismatch, connIDs, modelIDs
	}
	state.targetFields["health_check_connection_id"] = conn.id
	state.targetFields["health_check_endpoint_mode"] = oldMode
	state.targetFields["health_check_model_id"] = model.id
	state.targetFields["health_check_base_url_source"] = conn.baseURLSource
	return "", nil, nil
}

// ── 规则 13：public_model 冲突 ───────────────────────────────────────────

func (c *rulesContext) applyPublicModelConflicts(out *ruleOutput) {
	for _, id := range sortedAccountIDs(c.accountStates) {
		state := c.accountStates[id]
		if state.authInstance || state.identityBlocked {
			continue
		}
		// D：支持、active、owner-visible 且 mode 与任一派生连接相交的 direct
		// (model_id, public_model)；M：target 全部通过校验的 enabled mapping。
		connectionModes := map[string]bool{}
		for _, conn := range state.connections {
			for mode := range conn.modes {
				connectionModes[mode] = true
			}
		}
		directByWire := map[string][]string{}
		for modelID := range state.supportedModels {
			info := c.modelByID(modelID)
			if info == nil || info.blocked || info.status != "active" || !modelOwnerVisible(info, state.owner) {
				continue
			}
			if !intersectNonEmpty(info.modes, sortedStringKeys(connectionModes)) {
				continue
			}
			directByWire[info.model] = append(directByWire[info.model], info.id)
		}
		wires := map[string]bool{}
		for wire := range directByWire {
			wires[wire] = true
		}
		for key := range c.mappingM {
			if parts := strings.SplitN(key, "\x00", 2); len(parts) == 2 && parts[0] == id {
				wires[parts[1]] = true
			}
		}
		for _, wire := range sortedStringKeys(wires) {
			directIDs := dedupeStrings(directByWire[wire])
			mappingIDs := sortedStringKeys(c.mappingM[id+"\x00"+wire])
			conflicted := len(directIDs) >= 2 || (len(directIDs) >= 1 && len(mappingIDs) >= 1)
			if !conflicted {
				continue
			}
			out.publicConflicts = append(out.publicConflicts, PublicModelConflict{
				ResourceAccountID: id,
				WireModel:         wire,
				DirectModelIDs:    directIDs,
				MappingIDs:        mappingIDs,
			})
			out.blocked.PublicModelConflicts++
		}
	}
}

// ── 规则 8：groups / group_accounts / route_strategy_groups ─────────────

func (c *rulesContext) applyGroupRows(out *ruleOutput) {
	table := c.source.table("groups")
	nameConflict := map[int]bool{}
	defaultConflict := map[int]bool{}
	byOwnerName := map[string][]int{}
	defaultsByOwner := map[string][]int{}
	for index := range table.rows {
		row := &table.rows[index]
		owner := cellString(row.values["system_account_id"])
		name := strings.TrimSpace(cellString(row.values["name"]))
		byOwnerName[owner+"\x00"+name] = append(byOwnerName[owner+"\x00"+name], index)
		if cellString(row.values["is_default"]) == "1" {
			defaultsByOwner[owner] = append(defaultsByOwner[owner], index)
		}
	}
	for _, indices := range byOwnerName {
		if len(indices) > 1 {
			for _, index := range indices {
				nameConflict[index] = true
			}
		}
	}
	for _, indices := range defaultsByOwner {
		if len(indices) > 1 {
			for _, index := range indices {
				defaultConflict[index] = true
			}
		}
	}
	for index := range table.rows {
		row := &table.rows[index]
		name := strings.TrimSpace(cellString(row.values["name"]))
		rowOut := RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			TargetTable: "groups",
			TargetPK:    row.pkDisplay(),
			Rule:        8,
			RuleNote:    "规则 8：provider_code 丢弃；唯一身份 (system_account_id, name) binary",
			TargetFields: map[string]string{
				"system_account_id": cellString(row.values["system_account_id"]),
				"name":              name,
				"group_kind":        "standard",
				"is_default":        cellString(row.values["is_default"]),
			},
			Evidence: []Evidence{newEvidence(table, row, []string{"id", "system_account_id", "name", "is_default"}, c.now)},
		}
		switch {
		case name == "" || len(name) > 128 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = []string{"group_name_invalid"}
			out.blocked.GroupsBlocked++
			c.blockedGroups[cellString(row.values["id"])] = true
		case nameConflict[index]:
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = []string{"group_name_conflict"}
			rowOut.RuleNote = joinNote(rowOut.RuleNote, "resolution 留待 manifest 修复（rename/merge）")
			out.blocked.GroupsBlocked++
			c.blockedGroups[cellString(row.values["id"])] = true
		case defaultConflict[index]:
			rowOut.Disposition = DispositionBlocked
			rowOut.Errors = []string{"group_default_conflict"}
			rowOut.RuleNote = joinNote(rowOut.RuleNote, "每 owner 至多一个默认组；resolution 留待 manifest 修复")
			out.blocked.GroupsBlocked++
			c.blockedGroups[cellString(row.values["id"])] = true
		default:
			rowOut.Disposition = DispositionMapped
		}
		out.rows = append(out.rows, rowOut)
	}
}

func (c *rulesContext) applyGroupAccountRows(out *ruleOutput) {
	table := c.source.table("group_accounts")
	for index := range table.rows {
		row := &table.rows[index]
		rowOut := RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			TargetTable: "group_accounts",
			TargetPK:    row.pkDisplay(),
			Rule:        8,
			RuleNote:    "规则 8：成员关系保留 (group_id, account_id) 唯一键",
		}
		if c.groupRowBlocked(cellString(row.values["group_id"]), cellString(row.values["account_id"])) {
			rowOut.Disposition = DispositionBlocked
			rowOut.RuleNote = joinNote(rowOut.RuleNote, "引用的 group/账户行阻塞；成员行不重复计入 blocked 计数")
		} else {
			rowOut.Disposition = DispositionMapped
		}
		out.rows = append(out.rows, rowOut)
	}
}

func (c *rulesContext) applyRouteStrategyGroupRows(out *ruleOutput) {
	table := c.source.table("route_strategy_groups")
	for index := range table.rows {
		row := &table.rows[index]
		rowOut := RowDisposition{
			SourceTable: table.def.name,
			SourcePK:    row.pkDisplay(),
			TargetTable: "route_strategy_groups",
			TargetPK:    row.pkDisplay(),
			Rule:        8,
			RuleNote:    "规则 8a：merge 策略的超级分组转换（生成 super_group_members、策略改写 normal）待正式执行切片",
		}
		if c.groupRowBlocked(cellString(row.values["group_id"]), "") {
			rowOut.Disposition = DispositionBlocked
			rowOut.RuleNote = joinNote(rowOut.RuleNote, "引用的 group 行阻塞；绑定行不重复计入 groups_blocked")
		} else {
			rowOut.Disposition = DispositionMapped
		}
		out.rows = append(out.rows, rowOut)
	}
}

// groupRowBlocked 判定引用行是否指向阻塞的 group（或 identity-blocked 账户）。
// 引用行的 disposition 相应标 blocked，但不重复计入 blocked 计数（与规则 0
// 的每无效账户只计 1 次同一纪律）。
func (c *rulesContext) groupRowBlocked(groupID, accountID string) bool {
	if c.blockedGroups[groupID] {
		return true
	}
	if accountID != "" {
		if state, exists := c.accountStates[accountID]; exists && state.identityBlocked {
			return true
		}
	}
	return false
}

// ── 规则 11：历史行按表清点 ──────────────────────────────────────────────

func (c *rulesContext) applyHistoryRows(out *ruleOutput) {
	for _, def := range historySourceTables {
		count := c.source.historyCounts[def.name]
		out.rows = append(out.rows, RowDisposition{
			SourceTable:    def.name,
			SourcePK:       []string{},
			SourceRowCount: count,
			Disposition:    DispositionBlocked,
			Rule:           11,
			RuleNote:       "规则 11：历史行按表清点，disposition 全部 blocked；逐行 source identity 转换待正式执行切片",
			Errors:         []string{"history_rows_unmatched"},
		})
		out.blocked.HistoryRowsUnmatched += count
	}
}

// findAccountRow 按 accounts 主键定位源行（授权实例来源校验、规则 12 使用）。
func (c *rulesContext) findAccountRow(id string) *sourceRow {
	return c.source.table("accounts").findRow(id)
}

// findProfileRow 按 provider_protocol_profiles 主键定位源行（规则 12 Base URL
// 步骤③使用）。
func (c *rulesContext) findProfileRow(id string) *sourceRow {
	return c.source.table("provider_protocol_profiles").findRow(id)
}

// modelByID 按目标模型 ID 定位规则 10 合并产物（规则 13 D 集合构建）。
func (c *rulesContext) modelByID(id string) *modelInfo {
	for _, info := range c.models {
		if info.id == id {
			return info
		}
	}
	return nil
}

// ── 小工具 ───────────────────────────────────────────────────────────────

func newEvidence(table *sourceTable, row *sourceRow, columns []string, now time.Time) Evidence {
	fields := map[string]string{}
	for _, column := range columns {
		if cell, present := row.values[column]; present {
			fields[column] = canonicalFieldHash(cell)
		}
	}
	return Evidence{
		SourceTable:    table.def.name,
		SourcePK:       row.pkDisplay(),
		SourceFields:   fields,
		RetrievedAtUTC: now.UTC().Format(time.RFC3339Nano),
	}
}

// canonicalFieldHash 对 cell 的 canonical JSON bytes 取 SHA-256（证据口径：
// 记录源列名与 canonical 值 hash，不落明文）。
func canonicalFieldHash(cell Cell) string {
	var builder strings.Builder
	cell.canonicalJSON().encode(&builder)
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:])
}

// credentialFingerprint 的切片口径：SHA-256(ciphertext UTF-8 bytes) 十六进制；
// NULL 凭据返回 "NULL"。不做解密、不落明文。
func credentialFingerprint(cell Cell) string {
	if cell.Type == CellNull {
		return "NULL"
	}
	digest := sha256.Sum256([]byte(cell.Str))
	return hex.EncodeToString(digest[:])
}

// canonicalBaseURL 按 §1.1/规则 12 的切片口径校验：TrimSpace 后必须为绝对
// http/https URL（host 必填、无 userinfo/query/fragment）。第二个返回值区分
// "合法"；空串表示无值（missing）。
func canonicalBaseURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", true
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return trimmed, true
}

func deterministicConnectionID(accountID, preset string) string {
	digest := sha256.Sum256([]byte(accountID + "\x00" + preset))
	return "conn-" + hex.EncodeToString(digest[:])[:16]
}

func intersectNonEmpty(left []string, right []string) bool {
	set := map[string]bool{}
	for _, item := range right {
		set[item] = true
	}
	for _, item := range left {
		if set[item] {
			return true
		}
	}
	return false
}

func sortedStringKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func cellString(cell Cell) string {
	if cell.Type == CellNull {
		return ""
	}
	return cell.Str
}

func orNull(value string) string {
	if value == "" {
		return "NULL"
	}
	return value
}

func boolDisplay(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func providerDisplay(cell Cell) string {
	if cell.Type == CellNull {
		return "SQL NULL"
	}
	return "text:" + cell.Str
}

func joinNote(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "；" + addition
}

func sortedAccountIDs(states map[string]*accountState) []string {
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
